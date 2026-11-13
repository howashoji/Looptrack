//go:build !windows

package detach

import "syscall"

// attr は新しいセッションで起動する（setsid。端末・プロセスグループのシグナルが届かない）。
func attr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
