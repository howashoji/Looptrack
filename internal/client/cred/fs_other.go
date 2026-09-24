//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly || windows)

package cred

import "os"

// flock の無い OS（配布の対象外）。権限の確認は POSIX と同じにし、ロックはしない。

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

func mkdirPrivate(dir string) error { return os.MkdirAll(dir, 0o700) }

func lockFile(string) (func(), error) { return func() {}, nil }
