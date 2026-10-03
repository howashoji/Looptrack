package verify

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
)

// tree は起動したコマンドとその子孫をまとめて止める仕掛け（POSIX はプロセスグループ、Windows は Job Object）。
type tree interface {
	// terminate は時間切れのときの最初の止め方（POSIX は SIGTERM。Windows は木ごと止める）。
	terminate()
	// kill は木ごと強制的に止める（POSIX は SIGKILL）。
	kill()
	// close は後始末（Windows は Job のハンドルを閉じる）。
	close()
}

// Shell は検証コマンドを渡すシェル（`<shell> -c <command>`）。見つからなければ error（起動できない失敗にする）。
func Shell() (string, error) { return findShell() }

// RunCommand は 1 コマンドを `<shell> -c` で実行する。
// 時間切れは子孫まで止めて status timeout・exit_code null にする。シグナルで終わったら 128 + 番号（シェルと同じ）。
func RunCommand(command, dir string, env []string, timeout time.Duration) Result {
	return RunCommandTo(command, dir, env, timeout, nil)
}

// RunCommandTo は RunCommand に、出力の全文の書き先 full を足したもの（nil なら RunCommand と同じ）。
// full には切る前の出力をそのままのバイトで書く（マスクも文字コードの読み直しもしない。読む側が FullText で整える）。
// 書き先の誤りは無視する（全文の控えが取れなくても、検証の結果は変えない）。
func RunCommandTo(command, dir string, env []string, timeout time.Duration, full io.Writer) Result {
	shell, err := Shell()
	if err != nil {
		return launchFailed(command, err)
	}
	return runWithFull(shell, command, dir, env, timeout, full)
}

// launchFailed は起動できなかった結果。出力の末尾に足す文面は、コマンドを走らせた人の言語で書く
// （コマンド自身の出力と同じく、手元の端末の言語）。
func launchFailed(command string, err error) Result {
	lang := i18n.FromEnv(os.Getenv)
	return Result{Command: command, Status: StatusFail, OutputTail: Output([]byte(i18n.T(lang, "verify.run.launch_failed", "reason", i18n.Text(lang, err))))}
}

func runWith(shell, command, dir string, env []string, timeout time.Duration) Result {
	return runWithFull(shell, command, dir, env, timeout, nil)
}

func runWithFull(shell, command, dir string, env []string, timeout time.Duration, full io.Writer) Result {
	started := time.Now()
	cmd := exec.Command(shell, "-c", command)
	cmd.Dir, cmd.Env = dir, env
	cmd.Stdin = nil // /dev/null（Windows は NUL）
	r, w, err := os.Pipe()
	if err != nil {
		return launchFailed(command, err)
	}
	cmd.Stdout, cmd.Stderr = w, w
	prepare(cmd)
	if err := cmd.Start(); err != nil {
		w.Close()
		r.Close()
		return launchFailed(command, err)
	}
	w.Close() // 子と子孫だけが書き口を持つ（全員が閉じると読み口が EOF になる）
	t, err := attach(cmd)
	if err != nil {
		// Job に入れられないまま動かすと時間切れで子孫を止められない。本体を止めて起動できない失敗にする
		cmd.Process.Kill()
		cmd.Wait()
		r.Close()
		return launchFailed(command, err)
	}
	defer t.close()

	buf := &tailBuffer{max: KeepBytes}
	var sink io.Writer = buf
	if full != nil {
		sink = io.MultiWriter(buf, ignoreErrors{full})
	}
	readDone := make(chan struct{})
	go func() {
		io.Copy(sink, r)
		close(readDone)
	}()
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var werr error
	timedOut := false
	select {
	case werr = <-waitDone:
	case <-timer.C:
		timedOut = true
		t.terminate()
		select {
		case werr = <-waitDone:
		case <-time.After(termGrace):
			t.kill()
			werr = <-waitDone
		}
	}
	elapsed := time.Since(started)
	select {
	case <-readDone:
	case <-time.After(pipeGrace): // 背景に残ったプロセスが出力を開いたまま
		t.kill()
		select {
		case <-readDone:
		case <-time.After(killGrace):
		}
	}
	r.Close()

	raw := buf.Bytes()
	// 結果キャッシュの印は、切る前（末尾 64KiB）で見る。送る出力（4,000 バイト）から落ちても注記は残す
	res := Result{Command: command, DurationMS: elapsed.Milliseconds(), OutputTail: Output(raw), Cached: Cached(string(raw))}
	if timedOut {
		res.Status = StatusTimeout
		return res
	}
	code := 0
	if werr != nil {
		var ee *exec.ExitError
		if !errors.As(werr, &ee) {
			res.Status = StatusFail
			res.OutputTail = Output(append(raw, []byte("\n"+i18n.T(i18n.FromEnv(os.Getenv), "verify.run.wait_failed", "reason", werr))...))
			return res
		}
		code = exitCode(ee.ProcessState)
	}
	res.ExitCode = &code
	res.Status = StatusOK
	if code != 0 {
		res.Status = StatusFail
	}
	return res
}

// ignoreErrors は書き込みの誤りを飲み込む（全文の控えの失敗で、末尾の保持まで止めないため）。
type ignoreErrors struct{ w io.Writer }

func (e ignoreErrors) Write(p []byte) (int, error) {
	_, _ = e.w.Write(p)
	return len(p), nil
}
