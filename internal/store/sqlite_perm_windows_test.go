//go:build windows

package store

import (
	"path/filepath"
	"testing"

	"github.com/howashoji/looptrack/internal/privfile"
)

// Windows: 本人だけにしたディレクトリ（privfile.MkdirAll。setup・ローカルモードで DB のディレクトリを作るとき）に
// 新しく作った DB・-wal・-shm は本人だけ（本体は privfile.CreateEmpty、-wal・-shm はディレクトリの ACE の継承）。
// CI の Windows で動かす。
func TestSQLiteNewFilesOwnerOnlyWindows(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := privfile.MkdirAll(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "im.db")
	db, err := Open(SQLitePrefix + path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE t (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	for _, f := range SQLiteFiles(path) {
		if err := privfile.Check(f); err != nil {
			t.Errorf("%s: %v", filepath.Base(f), err)
		}
	}
	if broad := CheckSQLitePerms(path); len(broad) != 0 {
		t.Errorf("CheckSQLitePerms = %v, want なし", broad)
	}
}
