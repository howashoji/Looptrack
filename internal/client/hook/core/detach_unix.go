//go:build !windows

package core

import "syscall"

// detachedAttr は新しいセッションで起動する（setsid。端末・プロセスグループのシグナルが届かない）。
func detachedAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
