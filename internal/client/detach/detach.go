// Package detach は hook が待たずに離す子プロセスを起動する（usage の送信・在席・タイムラインのコミット）。
// POSIX は setsid（新しいセッション。hook を起動した AI の端末・プロセスグループから外れる）、Windows は DETACHED_PROCESS
// （コンソールを持たない）と CREATE_NEW_PROCESS_GROUP（Ctrl+C が届かない）で起動する（detach_*.go）。
package detach

import (
	"errors"
	"os"
	"os/exec"
)

// Start は exe を切り離して起動し、job を標準入力に書いて待たずに離す（environ が nil なら親の環境）。標準出力・標準エラーは捨てる。
func Start(exe string, args []string, job []byte, dir string, environ []string) error {
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	defer w.Close()
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	cmd.Env = environ
	cmd.Stdin = r // *os.File なので子にそのまま渡る（コピーの goroutine を作らない＝親がすぐ終わってよい）
	cmd.SysProcAttr = attr()
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
