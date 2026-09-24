//go:build !windows

package desktop

import (
	"errors"
	"os"
	"syscall"
)

// tryLock はロックのファイルに排他のロック（flock）を掛ける。他のプロセスが持っていれば ok=false。
// ロックはプロセスが終われば OS が外す（異常終了でもロックが残らない。ファイル自体は残してよい）。
func tryLock(path string) (release func(), ok bool, err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, true, nil
}
