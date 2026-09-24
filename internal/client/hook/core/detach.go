package core

// usage の送信を切り離す（hook は操作を待たせずにすぐ 0 で終わり、送信は子プロセスが続ける）。
// 以前の実装は二重 fork + setsid だった。Go は fork できないので自分（looptrack）を起動し直し、仕事は標準入力の JSON で渡す。
// POSIX は setsid（新しいセッション。hook を起動した AI の端末・プロセスグループから外れる）、Windows は DETACHED_PROCESS
// （コンソールを持たない）と CREATE_NEW_PROCESS_GROUP（Ctrl+C が届かない）で起動する（detach_*.go）。

import (
	"errors"
	"os"
	"os/exec"
)

// ChildArgs は切り離した子プロセスの引数（実行ファイルの後ろ）。cmd/looptrack の `hook usage` に届く形。
var ChildArgs = []string{"hook", "usage", usageJobFlag}

// spawnSelf は自分の実行ファイルを ChildArgs で起動し、job を標準入力に書いて待たずに離す（Env.Spawn の既定）。
// 環境変数は親と同じ（API の URL・トークンの場所を引き継ぐ）。標準出力・標準エラーは捨てる。
func spawnSelf(job []byte, dir string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return spawnDetached(exe, ChildArgs, job, dir, nil)
}

// spawnDetached は exe を切り離して起動し、job を標準入力に書いて待たずに離す（environ が nil なら親の環境）。
func spawnDetached(exe string, args []string, job []byte, dir string, environ []string) error {
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	defer w.Close()
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	cmd.Env = environ
	cmd.Stdin = r // *os.File なので子にそのまま渡る（コピーの goroutine を作らない＝親がすぐ終わってよい）
	cmd.SysProcAttr = detachedAttr()
	err = cmd.Start()
	r.Close()
	if err != nil {
		return err
	}
	_, werr := w.Write(job)
	cerr := w.Close()
	rerr := cmd.Process.Release()
	return errors.Join(werr, cerr, rerr)
}
