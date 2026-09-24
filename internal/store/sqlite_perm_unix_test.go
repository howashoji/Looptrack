//go:build !windows

package store

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
)

// 新しく作る SQLite の DB・-wal・-shm は umask 022 のもとでも 0600（本人だけ）になる。
// -wal・-shm は SQLite が本体の権限を引き継いで作る（SQLiteFiles の説明）ことをここで確かめる。
func TestSQLiteNewFilesOwnerOnly(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)

	path := filepath.Join(t.TempDir(), "im.db")
	db, err := Open(SQLitePrefix + path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	for _, q := range []string{"CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)", "INSERT INTO t (v) VALUES ('x')"} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	var mode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, %v（WAL で -wal・-shm ができる前提）", mode, err)
	}
	for _, f := range SQLiteFiles(path) {
		st, err := os.Stat(f)
		if err != nil {
			t.Fatalf("%s: %v", filepath.Base(f), err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Errorf("%s のパーミッション = %o, want 600", filepath.Base(f), st.Mode().Perm())
		}
	}
	if broad := CheckSQLitePerms(path); len(broad) != 0 {
		t.Errorf("CheckSQLitePerms = %v, want なし", broad)
	}
}

// 既にある DB（0644 で作られたもの）は開いても権限を変えず、CheckSQLitePerms が本体・-wal・-shm を広いと返す
// （-wal・-shm は本体の 0644 を引き継ぐ）。
func TestSQLiteExistingBroadDB(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)

	path := filepath.Join(t.TempDir(), "im.db")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := Open(SQLitePrefix + path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE t (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o644 {
		t.Errorf("既存の DB の権限が変わった: %o", st.Mode().Perm())
	}
	if got := CheckSQLitePerms(path); !slices.Equal(got, SQLiteFiles(path)) {
		t.Errorf("CheckSQLitePerms = %v, want %v", got, SQLiteFiles(path))
	}
	for _, f := range SQLiteFiles(path) {
		if err := os.Chmod(f, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := CheckSQLitePerms(path); len(got) != 0 {
		t.Errorf("chmod 600 の後の CheckSQLitePerms = %v, want なし", got)
	}
	if got := CheckSQLitePerms(":memory:"); got != nil {
		t.Errorf(":memory: = %v", got)
	}
}
