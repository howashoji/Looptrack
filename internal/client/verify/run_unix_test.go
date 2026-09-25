//go:build unix

package verify

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func shortGraces(t *testing.T) {
	t.Helper()
	tg, pg, kg := termGrace, pipeGrace, killGrace
	termGrace, pipeGrace, killGrace = 500*time.Millisecond, 700*time.Millisecond, 500*time.Millisecond
	t.Cleanup(func() { termGrace, pipeGrace, killGrace = tg, pg, kg })
}

// gone は pid のプロセスが within の間に無くなるか（止めた子孫は init が回収するまでゾンビで残るので、少し待って確かめる）。
func gone(pid int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		// テストが PID 1 のコンテナ（CI）では回収されずにゾンビのまま残る。Linux は状態 Z を止まったとみなす
		if b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil {
			if i := strings.LastIndexByte(string(b), ')'); i >= 0 && strings.HasPrefix(string(b[i+1:]), " Z") {
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func readPIDs(t *testing.T, path string) []int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("pid のファイル: %v", err)
	}
	var out []int
	for _, f := range strings.Fields(string(b)) {
		n, err := strconv.Atoi(f)
		if err != nil {
			t.Fatalf("pid: %q", f)
		}
		out = append(out, n)
	}
	return out
}

func TestRunCommandBasics(t *testing.T) {
	dir := t.TempDir()
	env := Env(append(os.Environ(), "LOOPTRACK_API_URL=http://127.0.0.1:9"), "TST-0001")
	for _, c := range []struct {
		cmd    string
		status string
		code   int
		out    string
	}{
		{"echo ok", StatusOK, 0, "ok\n"},
		{"echo 失敗 >&2; exit 3", StatusFail, 3, "失敗\n"},
		{"kill -TERM $$", StatusFail, 128 + int(syscall.SIGTERM), ""}, // シグナルで終わったら 128 + 番号
		{"cat; echo done", StatusOK, 0, "done\n"},                     // 標準入力は空
		{"pwd", StatusOK, 0, ""},
		{"echo $LOOPTRACK_VERIFY_ID ${LOOPTRACK_API_URL-none}", StatusOK, 0, "TST-0001 none\n"},
	} {
		r := RunCommand(c.cmd, dir, env, 10*time.Second)
		if r.Status != c.status || r.ExitCode == nil || *r.ExitCode != c.code {
			t.Errorf("%q: %+v", c.cmd, r)
			continue
		}
		if c.cmd == "pwd" {
			real, _ := filepath.EvalSymlinks(dir)
			if got, _ := filepath.EvalSymlinks(strings.TrimSpace(r.OutputTail)); got != real {
				t.Errorf("作業ディレクトリ: %q, want %q", r.OutputTail, dir)
			}
			continue
		}
		if r.OutputTail != c.out {
			t.Errorf("%q: 出力 %q, want %q", c.cmd, r.OutputTail, c.out)
		}
	}
}

// 時間切れは子孫（背景の孫・その子）まで止める。
func TestRunCommandTimeoutKillsDescendants(t *testing.T) {
	shortGraces(t)
	dir := t.TempDir()
	pids := filepath.Join(dir, "pids")
	cmd := `sleep 60 & echo $! >> pids; (sleep 60 & echo $! >> pids; wait) & echo $! >> pids; echo started; sleep 60`
	start := time.Now()
	r := RunCommand(cmd, dir, os.Environ(), 700*time.Millisecond)
	if r.Status != StatusTimeout || r.ExitCode != nil {
		t.Fatalf("%+v", r)
	}
	if el := time.Since(start); el > 5*time.Second {
		t.Errorf("時間切れから戻るまでが長い: %v", el)
	}
	if r.OutputTail != "started\n" {
		t.Errorf("時間切れまでの出力: %q", r.OutputTail)
	}
	list := readPIDs(t, pids)
	if len(list) != 3 {
		t.Fatalf("pid: %v", list)
	}
	for _, pid := range list {
		if !gone(pid, 3*time.Second) {
			t.Errorf("子孫 %d が残っている", pid)
			syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

// SIGTERM を無視するものは猶予の後に SIGKILL で止める。
func TestRunCommandTimeoutIgnoresTerm(t *testing.T) {
	shortGraces(t)
	dir := t.TempDir()
	r := RunCommand(`trap '' TERM; sh -c 'trap "" TERM; echo $$ > pid; sleep 60' & wait`, dir, os.Environ(), 500*time.Millisecond)
	if r.Status != StatusTimeout {
		t.Fatalf("%+v", r)
	}
	for _, pid := range readPIDs(t, filepath.Join(dir, "pid")) {
		if !gone(pid, 3*time.Second) {
			t.Errorf("SIGTERM を無視した子孫 %d が残っている", pid)
			syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

// 本体が終わっても背景の子孫が出力を開いたままなら、待ってから止める（結果は本体の終了コード）。
func TestRunCommandBackgroundHoldsOutput(t *testing.T) {
	shortGraces(t)
	dir := t.TempDir()
	start := time.Now()
	r := RunCommand(`sleep 60 & echo $! > pid; echo main`, dir, os.Environ(), 10*time.Second)
	if r.Status != StatusOK || r.ExitCode == nil || *r.ExitCode != 0 || r.OutputTail != "main\n" {
		t.Fatalf("%+v", r)
	}
	if el := time.Since(start); el > 5*time.Second {
		t.Errorf("戻るまでが長い: %v", el)
	}
	for _, pid := range readPIDs(t, filepath.Join(dir, "pid")) {
		if !gone(pid, 3*time.Second) {
			t.Errorf("出力を開いたままの子孫 %d が残っている", pid)
			syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

// 出力を閉じて背景に回したものは止めない（本体は待たずに戻る）。
func TestRunCommandDetachedBackgroundSurvives(t *testing.T) {
	dir := t.TempDir()
	start := time.Now()
	r := RunCommand(`sleep 30 >/dev/null 2>&1 & echo $! > pid`, dir, os.Environ(), 10*time.Second)
	if r.Status != StatusOK {
		t.Fatalf("%+v", r)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("戻るまでが長い: %v", el)
	}
	for _, pid := range readPIDs(t, filepath.Join(dir, "pid")) {
		if gone(pid, 200*time.Millisecond) {
			t.Errorf("出力を閉じた背景のプロセス %d まで止めた", pid)
		}
		syscall.Kill(pid, syscall.SIGKILL)
	}
}

func TestRunCommandLaunchFailure(t *testing.T) {
	t.Setenv("LOOPTRACK_LANG", "ja") // 下の検査は日本語の文面を見る（足す文面はコマンドを走らせた人の言語）
	r := runWith(filepath.Join(t.TempDir(), "no-such-shell"), "echo x", t.TempDir(), os.Environ(), time.Second)
	if r.Status != StatusFail || r.ExitCode != nil || !strings.HasPrefix(r.OutputTail, "起動できません: ") {
		t.Errorf("%+v", r)
	}
}

func TestRunCommandKeepsTail(t *testing.T) {
	r := RunCommand(`i=0; while [ $i -lt 3000 ]; do echo "line $i ....................................."; i=$((i+1)); done; echo token=secret123`,
		t.TempDir(), os.Environ(), 20*time.Second)
	if r.Status != StatusOK || len(r.OutputTail) > OutputBytes || !strings.HasSuffix(r.OutputTail, "line 2999 .....................................\ntoken=***\n") {
		t.Errorf("%d バイト: …%q", len(r.OutputTail), r.OutputTail[max(0, len(r.OutputTail)-80):])
	}
}

// verify が組み立てる環境では go test の結果キャッシュが返らない（案 A）。
// 返ってきたときは注記が付く（案 B）。実物の go を使って確かめる
// （Env から GOFLAGS=-count=1 を外すと、3 回目が cached になり、この検査が落ちる）。
func TestRunCommandGoTestResultCache(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go が見つかりません")
	}
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module verifycache\n\ngo 1.21\n")
	write("x_test.go", "package verifycache\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {}\n")

	// GOFLAGS を持たない環境（キャッシュが効く側）
	var plain []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GOFLAGS=") {
			plain = append(plain, kv)
		}
	}
	const cmd = "go test ./..."
	if r := RunCommand(cmd, dir, plain, 3*time.Minute); r.Status != StatusOK {
		t.Fatalf("1 回目が %s: %s", r.Status, r.OutputTail)
	}
	second := RunCommand(cmd, dir, plain, 3*time.Minute)
	if second.Status != StatusOK {
		t.Fatalf("2 回目が %s: %s", second.Status, second.OutputTail)
	}
	if !strings.Contains(second.OutputTail, "(cached)") {
		t.Skipf("この環境では結果キャッシュが返らないため確かめられません: %s", second.OutputTail)
	}
	if !second.Cached { // 出力に印があるのに注記が付かない = 案 B が効いていない
		t.Errorf("結果キャッシュの印を注記にしていません: %s", second.OutputTail)
	}

	// verify が渡す環境（GOFLAGS=-count=1 が入る側）
	third := RunCommand(cmd, dir, Env(os.Environ(), "TST-0001"), 3*time.Minute)
	if third.Status != StatusOK {
		t.Fatalf("3 回目が %s: %s", third.Status, third.OutputTail)
	}
	if third.Cached {
		t.Errorf("verify の環境で結果キャッシュが返りました（GOFLAGS=-count=1 が効いていない）: %s", third.OutputTail)
	}
}
