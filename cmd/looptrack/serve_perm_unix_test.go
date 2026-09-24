//go:build !windows

package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/howashoji/looptrack/internal/store"
)

// 既にある SQLite の DB が本人以外も読める（0644）なら起動時に警告し、直すコマンド（chmod 600）を添える。
// 新しく作った DB（本人だけ）では警告しない。MySQL の DSN では何もしない。
func TestWarnSQLitePerms(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	dir := t.TempDir()

	warn := func(dsn string) string {
		t.Helper()
		var buf bytes.Buffer
		warnSQLitePerms(dsn, slog.New(slog.NewTextHandler(&buf, nil)))
		return buf.String()
	}
	open := func(path string) {
		t.Helper()
		db, err := store.Open(store.SQLitePrefix + path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		if _, err := db.Exec("CREATE TABLE t (id INTEGER PRIMARY KEY)"); err != nil {
			t.Fatal(err)
		}
	}

	broad := filepath.Join(dir, "broad.db")
	if err := os.WriteFile(broad, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	open(broad)
	out := warn(store.SQLitePrefix + broad)
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "本人以外も読めます") || !strings.Contains(out, "chmod 600 '"+broad+"' '"+broad+"-wal' '"+broad+"-shm'") {
		t.Errorf("0644 の DB で警告が出ない: %s", out)
	}

	fresh := filepath.Join(dir, "fresh.db")
	open(fresh)
	if out := warn(store.SQLitePrefix + fresh); out != "" {
		t.Errorf("新しく作った DB で警告が出た: %s", out)
	}
	if out := warn("root:pw@tcp(127.0.0.1:3306)/im"); out != "" {
		t.Errorf("MySQL で警告が出た: %s", out)
	}
}

// ローカルモードの鍵のファイルを作るとき、無い DB のディレクトリは本人だけ（0700）で作る。
func TestLocalSecretKeyDirOwnerOnly(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	dir := filepath.Join(t.TempDir(), "data")
	var buf bytes.Buffer
	if _, err := localSecretKey(filepath.Join(dir, "im.db"), slog.New(slog.NewTextHandler(&buf, nil))); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("ディレクトリの権限 = %v (%v), want 700", st.Mode().Perm(), err)
	}
}
