package desktop

import "syscall"

// detachAttr は Windows では何も足さない（Windows では置き換えないので起動し直しも使わない）。
func detachAttr() *syscall.SysProcAttr { return nil }
