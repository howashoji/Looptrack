package localserve

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// backupMigrations は SQLite 用のマイグレーションを先頭から n 本だけ持つ版（n が大きいほど新しい版）。
// 本物の migrations.FS ではなく小さな表で、「前の版の中身」を控えから読めるかを確かめる。
func backupMigrations(n int) fstest.MapFS {
	all := []struct{ name, sql string }{
		{"0001_init.sql", "CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT NOT NULL);\n"},
		{"0002_a.sql", "ALTER TABLE notes ADD COLUMN a TEXT;\n"},
		{"0003_b.sql", "ALTER TABLE notes ADD COLUMN b TEXT;\n"},
		{"0004_c.sql", "ALTER TABLE notes ADD COLUMN c TEXT;\n"},
		{"0005_d.sql", "ALTER TABLE notes ADD COLUMN d TEXT;\n"},
	}
	fsys := fstest.MapFS{}
	for _, m := range all[:n] {
		fsys[store.SQLiteMigrationsDir+"/"+m.name] = &fstest.MapFile{Data: []byte(m.sql), Mode: 0o644}
	}
	return fsys
}

func openBackupTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := store.Open("sqlite:" + path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func backupFiles(t *testing.T, dbPath string) []string {
	t.Helper()
	entries, err := os.ReadDir(BackupDir(dbPath))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func appliedCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// 適用するものがあると控えが 1 つ増え、無ければ増えない。3 回目の控えで最も古いものが消えて 2 つ残る。
// 控えは本人だけのファイルで、開くと migrate する前の版の中身（適用記録と行）を持つ。新しい DB（適用記録 0 件）は控えない。
func TestMigrateBacksUpBeforeApplying(t *testing.T) {
	t.Setenv("LOOPTRACK_LANG", "ja")
	dbPath := filepath.Join(t.TempDir(), "Looptrack", "looptrack.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db := openBackupTestDB(t, dbPath)
	ctx := context.Background()
	clock := time.Date(2026, 9, 26, 1, 2, 3, 0, time.UTC)
	now := func() time.Time { return clock }
	run := func(n int) {
		t.Helper()
		if err := migrate(ctx, db, dbPath, backupMigrations(n), now, logger); err != nil {
			t.Fatalf("migrate(%d 本の版): %v", n, err)
		}
	}

	// 新しい DB: 適用するものはあるが、控える中身が無いので控えない
	run(2)
	if got := backupFiles(t, dbPath); len(got) != 0 {
		t.Fatalf("新しい DB で控えができた: %v", got)
	}
	if _, err := db.Exec("INSERT INTO notes (body, a) VALUES ('2 本の版で書いた行', 'x')"); err != nil {
		t.Fatal(err)
	}

	// 対照: 適用するものが無ければ控えは増えない
	run(2)
	if got := backupFiles(t, dbPath); len(got) != 0 {
		t.Fatalf("適用するものが無いのに控えができた: %v", got)
	}

	// 1 本適用する版: 控えが 1 つ増える
	run(3)
	first := backupFiles(t, dbPath)
	if want := []string{"looptrack.db.20260926T010203Z"}; strings.Join(first, ",") != strings.Join(want, ",") {
		t.Fatalf("1 回目の控え = %v, want %v", first, want)
	}
	if appliedCount(t, db) != 3 {
		t.Fatalf("適用記録 = %d, want 3", appliedCount(t, db))
	}
	backup1 := filepath.Join(BackupDir(dbPath), first[0])
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(backup1)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("控えの権限 = %o, want 600", fi.Mode().Perm())
		}
	}
	// 控えから開いた DB は、migrate する前の版（2 本）の中身を持つ
	bdb := openBackupTestDB(t, backup1)
	if got := appliedCount(t, bdb); got != 2 {
		t.Errorf("控えの適用記録 = %d, want 2（migrate する前の版）", got)
	}
	var body string
	if err := bdb.QueryRow("SELECT body FROM notes WHERE a = 'x'").Scan(&body); err != nil || body != "2 本の版で書いた行" {
		t.Errorf("控えの行 = %q, %v", body, err)
	}
	if _, err := bdb.Exec("SELECT b FROM notes"); err == nil {
		t.Error("控えに新しい版の列 b がある（migrate の後に取っている）")
	}
	bdb.Close()

	// 対照: もう一度同じ版で起動しても増えない
	run(3)
	if got := backupFiles(t, dbPath); len(got) != 1 {
		t.Fatalf("適用するものが無いのに控えが増えた: %v", got)
	}

	// 2 回目・3 回目: 3 回目で最も古いものが消えて 2 つ残る
	clock = clock.Add(time.Hour)
	run(4)
	if got := backupFiles(t, dbPath); len(got) != 2 {
		t.Fatalf("2 回目の後の控え = %v, want 2 つ", got)
	}
	clock = clock.Add(time.Hour)
	run(5)
	got := backupFiles(t, dbPath)
	want := []string{"looptrack.db.20260926T020203Z", "looptrack.db.20260926T030203Z"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("3 回目の後の控え = %v, want %v（最も古いものが消える）", got, want)
	}
	newest := openBackupTestDB(t, filepath.Join(BackupDir(dbPath), want[1]))
	if got := appliedCount(t, newest); got != 4 {
		t.Errorf("最も新しい控えの適用記録 = %d, want 4", got)
	}
}

// 時計が戻っても、いま作った控えは消さない（名前の順では最も古くなる）。途中で止まった .tmp は消す。
func TestPruneBackupsKeepsNewestAndDropsTmp(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "looptrack.db")
	dir := BackupDir(dbPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{
		"looptrack.db.20260926T020000Z", "looptrack.db.20260926T030000Z", "looptrack.db.20260101T000000Z",
		"looptrack.db.20260926T040000Z.tmp", "other.db.20200101T000000Z", "notes.txt",
	} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pruneBackups(dbPath, filepath.Join(dir, "looptrack.db.20260101T000000Z"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	got := strings.Join(backupFiles(t, dbPath), ",")
	want := "looptrack.db.20260101T000000Z,looptrack.db.20260926T030000Z,notes.txt,other.db.20200101T000000Z"
	if got != want {
		t.Errorf("残った = %s, want %s", got, want)
	}
}

// 控えを作れなければ migrate しない（DB は前の版のまま）。利用者に分かる文面で止める。
// 対照: 置き場を作れるようにすると、同じ DB で控えを作って migrate する。
func TestMigrateStopsWhenBackupFails(t *testing.T) {
	t.Setenv("LOOPTRACK_LANG", "ja")
	dbPath := filepath.Join(t.TempDir(), "looptrack.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db := openBackupTestDB(t, dbPath)
	ctx := context.Background()
	if err := migrate(ctx, db, dbPath, backupMigrations(2), time.Now, logger); err != nil {
		t.Fatal(err)
	}
	// 控えの置き場の名前でふつうのファイルを置き、ディレクトリを作れなくする（OS に依らず失敗する）
	if err := os.WriteFile(BackupDir(dbPath), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := migrate(ctx, db, dbPath, backupMigrations(3), time.Now, logger)
	if err == nil {
		t.Fatal("控えを作れないのに migrate が通った")
	}
	if got := appliedCount(t, db); got != 2 {
		t.Errorf("控えを作れないのに migrate した: 適用記録 = %d, want 2", got)
	}
	ja := i18n.Text(i18n.JA, err)
	if !strings.Contains(ja, "DB の控えを作れなかったので、マイグレーションをせずに止めました") || !strings.Contains(ja, BackupDir(dbPath)) {
		t.Errorf("文面（ja） = %s", ja)
	}
	if en := i18n.Text(i18n.EN, err); !strings.Contains(en, "Could not back up the database before migrating") {
		t.Errorf("文面（en） = %s", en)
	}

	// 対照: 置き場を作れるようにすれば、控えを作って migrate する
	if err := os.Remove(BackupDir(dbPath)); err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, db, dbPath, backupMigrations(3), time.Now, logger); err != nil {
		t.Fatalf("対照の migrate: %v", err)
	}
	if got := appliedCount(t, db); got != 3 {
		t.Errorf("対照: 適用記録 = %d, want 3", got)
	}
	if got := backupFiles(t, dbPath); len(got) != 1 {
		t.Errorf("対照: 控え = %v, want 1 つ", got)
	}
}
