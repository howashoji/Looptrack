//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package cred

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/howashoji/looptrack/internal/client/jsonorder"
)

// POSIX（Linux・macOS）: 本人だけが読める（0600・ディレクトリ 0700）ことと、広い権限の拒否。

func TestWrittenFilesAreOwnerOnly(t *testing.T) {
	s := newStore(t)
	old := umask(0) // umask に左右されないこと
	defer umask(old)
	if err := s.SaveEntry("https://a/im", jsonorder.NewObject().Set("token", "imp_x")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(s.Paths.Primary)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("ファイルが 0600 でない: %o", fi.Mode().Perm())
	}
	di, err := os.Stat(filepath.Dir(s.Paths.Primary))
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf("ディレクトリが 0700 でない: %o", di.Mode().Perm())
	}
	unlock, err := s.Lock()
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	li, err := os.Stat(s.Paths.Primary + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	if li.Mode().Perm()&0o077 != 0 {
		t.Errorf("ロックのファイルが他の利用者に開いている: %o", li.Mode().Perm())
	}
}

func TestRejectsGroupOrOtherReadable(t *testing.T) {
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604, 0o660} {
		s := newStore(t)
		seed(t, s, `{}`)
		if err := os.Chmod(s.Paths.Primary, mode); err != nil {
			t.Fatal(err)
		}
		_, err := s.Load()
		var pe *PermError
		if !errors.As(err, &pe) || pe.Path != s.Paths.Primary {
			t.Errorf("%o を拒否しない: %v", mode, err)
			continue
		}
		want := s.Paths.Primary + " の権限が広すぎます（他の利用者が読めます）。chmod 600 " + s.Paths.Primary + " を実行してください"
		if err.Error() != want {
			t.Errorf("文面が違う: %s", err)
		}
	}
}

func umask(m int) int { return syscall.Umask(m) }
