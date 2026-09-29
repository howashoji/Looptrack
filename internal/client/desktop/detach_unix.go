//go:build !windows

package desktop

import "syscall"

// detachAttr は起動し直す新しいインスタンスを別のセッションにする（今のインスタンスが終わっても巻き込まれない）。
func detachAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
