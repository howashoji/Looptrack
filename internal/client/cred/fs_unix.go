//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package cred

import (
	"os"
	"syscall"
)

// checkPrivate は group・other の権限があれば PermError。
func checkPrivate(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return &PermError{path}
	}
	return nil
}

// mkdirPrivate は無ければ 0700 で作る（既にあるディレクトリの権限は変えない）。
func mkdirPrivate(dir string) error { return os.MkdirAll(dir, 0o700) }

// lockFile は path を flock で排他ロックする（待つ）。
func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
