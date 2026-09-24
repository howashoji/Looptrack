//go:build !windows

package privfile

import "os"

func protect(f *os.File) error { return f.Chmod(0o600) }

func protectDir(dir string) error { return os.Chmod(dir, 0o700) }

func check(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return &PermError{path}
	}
	return nil
}
