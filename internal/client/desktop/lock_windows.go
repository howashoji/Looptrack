//go:build windows

package desktop

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// tryLock はロックのファイルに排他のロック（LockFileEx）を掛ける。他のプロセスが持っていれば ok=false。
// ロックはプロセスが終われば OS が外す（異常終了でもロックが残らない）。
func tryLock(path string) (release func(), ok bool, err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, false, err
	}
	h := windows.Handle(f.Fd())
	ol := new(windows.Overlapped)
	err = windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
	if err != nil {
		f.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return func() {
		windows.UnlockFileEx(h, 0, 1, 0, ol)
		f.Close()
	}, true, nil
}
