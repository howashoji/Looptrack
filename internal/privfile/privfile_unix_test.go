//go:build !windows

package privfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestUnixMode(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	if err := WriteFile(p, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Errorf("パーミッション = %o, want 600", st.Mode().Perm())
	}
	for _, mode := range []os.FileMode{0o644, 0o640, 0o606} {
		os.Chmod(p, mode)
		var pe *PermError
		if err := Check(p); !errors.As(err, &pe) {
			t.Errorf("%o: err = %v, want PermError", mode, err)
		}
	}
	os.Chmod(p, 0o400)
	if err := Check(p); err != nil {
		t.Errorf("400: %v", err)
	}
}

// MkdirAll は新しく作るディレクトリを 0700 にし、既にあるディレクトリの権限は変えない。
func TestUnixMkdirAll(t *testing.T) {
	base := t.TempDir()
	d := filepath.Join(base, "a", "data")
	if err := MkdirAll(d); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(d); st.Mode().Perm() != 0o700 {
		t.Errorf("新しいディレクトリ = %o, want 700", st.Mode().Perm())
	}
	os.Chmod(d, 0o755)
	if err := MkdirAll(d); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(d); st.Mode().Perm() != 0o755 {
		t.Errorf("既にあるディレクトリの権限を変えた: %o", st.Mode().Perm())
	}
	f := filepath.Join(base, "file")
	os.WriteFile(f, nil, 0o600)
	if err := MkdirAll(f); err == nil {
		t.Error("ファイルをディレクトリとして受け付けた")
	}
}
