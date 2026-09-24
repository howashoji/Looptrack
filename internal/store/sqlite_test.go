package store

import (
	"context"
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/migrations"
)

func TestSQLiteSQLRewrite(t *testing.T) {
	now := sqliteNow
	for _, c := range []struct{ in, want string }{
		{"UPDATE users SET last_login_at = CURRENT_TIMESTAMP(6) WHERE id = ?",
			"UPDATE users SET last_login_at = " + now + " WHERE id = ?"},
		{"DELETE FROM web_sessions WHERE expires_at < CURRENT_TIMESTAMP(6) OR last_seen_at < CURRENT_TIMESTAMP(6) - INTERVAL 12 HOUR",
			"DELETE FROM web_sessions WHERE expires_at < " + now + " OR last_seen_at < (strftime('%Y-%m-%d %H:%M:%f', 'now', '-12 hours') || '000')"},
		{"m.received_at >= e.at - INTERVAL 300 SECOND AND s.received_at < e.at + INTERVAL ? SECOND",
			"m.received_at >= (strftime('%Y-%m-%d %H:%M:%f', e.at, '-300 seconds') || '000') AND s.received_at < (strftime('%Y-%m-%d %H:%M:%f', e.at, '+' || ? || ' seconds') || '000')"},
		{"SELECT rules FROM projects WHERE id = ? FOR UPDATE", "SELECT rules FROM projects WHERE id = ?"},
		{"ORDER BY number FOR UPDATE", "ORDER BY number"},
		{"SELECT JSON_UNQUOTE(JSON_EXTRACT(e.detail, '$.to'))", "SELECT (JSON_EXTRACT(e.detail, '$.to'))"},
		{"INSERT INTO project_members (project_id, user_id, role) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE role = VALUES(role)",
			"INSERT INTO project_members (project_id, user_id, role) VALUES (?, ?, ?) ON CONFLICT DO UPDATE SET role = excluded.role"},
		{"INSERT INTO t (a, b) VALUES (?, ?)\nON DUPLICATE KEY UPDATE a = VALUES(a), b = COALESCE(VALUES(b), b)",
			"INSERT INTO t (a, b) VALUES (?, ?)\nON CONFLICT DO UPDATE SET a = excluded.a, b = COALESCE(excluded.b, b)"},
		{"UPDATE users SET disabled_at = NOW() WHERE id = 1", "UPDATE users SET disabled_at = " + now + " WHERE id = 1"},
		{"INSERT IGNORE INTO t VALUES (1)", "INSERT OR IGNORE INTO t VALUES (1)"},
		{"SELECT 'x' FROM DUAL WHERE EXISTS (SELECT 1 FROM users)", "SELECT 'x' WHERE EXISTS (SELECT 1 FROM users)"},
		{"SELECT id FROM issues WHERE title = ?", "SELECT id FROM issues WHERE title = ?"},
		{"SELECT LEFT(c.content, 200) FROM comments c LEFT JOIN users u ON u.id = c.author_user_id",
			"SELECT substr(c.content, 1, 200) FROM comments c LEFT JOIN users u ON u.id = c.author_user_id"},
	} {
		if got := sqliteSQL(c.in); got != c.want {
			t.Errorf("sqliteSQL(%q)\n got  %q\n want %q", c.in, got, c.want)
		}
	}
}

// 同じ番号の MySQL / SQLite のマイグレーションが対になっていること（migrations/embed.go の規則）。
func TestSQLiteMigrationsMirrorMySQL(t *testing.T) {
	my, err := LoadMigrations(migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := fs.Sub(migrations.FS, SQLiteMigrationsDir)
	if err != nil {
		t.Fatal(err)
	}
	lite, err := LoadMigrations(sub)
	if err != nil {
		t.Fatal(err)
	}
	names := func(ms []Migration) []string {
		var out []string
		for _, m := range ms {
			out = append(out, m.Name)
		}
		return out
	}
	if a, b := strings.Join(names(my), " "), strings.Join(names(lite), " "); a != b {
		t.Fatalf("migrations/ と migrations/sqlite/ のファイルが対になっていません（MySQL の NNNN_名前.sql ごとに同じ名前の SQLite 版を置く）\nMySQL:  %s\nSQLite: %s", a, b)
	}
}

func sqliteTestDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "im.db")
	db, err := Open(SQLitePrefix + path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if !IsSQLite(db) {
		t.Fatal("IsSQLite = false")
	}
	return db, path
}

func TestSQLiteMigrateAndDialect(t *testing.T) {
	db, _ := sqliteTestDB(t)
	ctx := context.Background()
	first, err := Migrate(ctx, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) == 0 {
		t.Fatal("1 回目に何も適用されなかった")
	}
	if second, err := Migrate(ctx, db, migrations.FS); err != nil || len(second) != 0 {
		t.Fatalf("2 回目: applied=%v err=%v（冪等でない）", second, err)
	}
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Errorf("journal_mode = %q %v", mode, err)
	}

	// 時刻: Go の time.Time を書いて同じ値（UTC・マイクロ秒）で読める。列・式（MAX）の両方
	at := time.Date(2026, 9, 19, 12, 34, 56, 123456789, time.FixedZone("JST", 9*3600))
	mustExec(t, db, "INSERT INTO users (login, password_hash) VALUES ('alice', 'x')")
	if _, err := db.Exec("UPDATE users SET last_login_at = ? WHERE login = 'alice'", at); err != nil {
		t.Fatal(err)
	}
	var col, agg time.Time
	var created sql.NullTime
	if err := db.QueryRow("SELECT last_login_at, MAX(last_login_at), MIN(created_at) FROM users").Scan(&col, &agg, &created); err != nil {
		t.Fatal(err)
	}
	want := at.UTC().Truncate(time.Microsecond)
	if !col.Equal(want) || !agg.Equal(want) || col.Location() != time.UTC {
		t.Errorf("時刻 = %v / %v, want %v", col, agg, want)
	}
	if !created.Valid || time.Since(created.Time) > time.Minute || time.Since(created.Time) < -time.Minute {
		t.Errorf("既定値の現在時刻 = %v", created)
	}
	// INTERVAL と CURRENT_TIMESTAMP(6) の比較
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM users WHERE created_at > CURRENT_TIMESTAMP(6) - INTERVAL 1 HOUR").Scan(&n); err != nil || n != 1 {
		t.Errorf("INTERVAL: %d %v", n, err)
	}

	// 一意制約・UPSERT
	_, err = db.Exec("INSERT INTO users (login, password_hash) VALUES ('alice', 'y')")
	if !IsDuplicateKey(err) {
		t.Errorf("重複 = %v", err)
	}
	if err := upsertSetting(t, db, "a"); err != nil {
		t.Fatal(err)
	}
	if err := upsertSetting(t, db, "b"); err != nil {
		t.Fatal(err)
	}
	var v string
	db.QueryRow("SELECT value FROM system_settings WHERE name = 'k'").Scan(&v)
	if v != "b" {
		t.Errorf("UPSERT: %q", v)
	}

	// JSON を []byte で渡しても JSON 関数で読める（BLOB を JSONB と解釈させない）
	mustExec(t, db, "INSERT INTO projects (slug, prefix, width, name) VALUES ('p', 'P', 4, 'p')")
	if _, err := db.Exec("INSERT INTO issue_events (project_id, kind, via, detail) VALUES (1, 'status', 'cli', ?)", []byte(`{"to":"Done"}`)); err != nil {
		t.Fatal(err)
	}
	var to string
	if err := db.QueryRow("SELECT JSON_UNQUOTE(JSON_EXTRACT(detail, '$.to')) FROM issue_events").Scan(&to); err != nil || to != "Done" {
		t.Errorf("JSON: %q %v", to, err)
	}

	// 追記専用: トリガで書き換え・削除を拒否する。UnlockAppendOnly の間だけ通る
	for _, stmt := range []string{"UPDATE issue_events SET kind = 'x'", "DELETE FROM issue_events", "DELETE FROM projects"} {
		if _, err := db.Exec(stmt); !IsAppendOnlyViolation(err) {
			t.Errorf("%s: err = %v, want 追記専用の拒否", stmt, err)
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	relock, err := UnlockAppendOnly(ctx, db, tx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("DELETE FROM issue_events"); err != nil {
		t.Fatalf("解除中の削除: %v", err)
	}
	if err := relock(); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("DELETE FROM projects"); !IsAppendOnlyViolation(err) {
		t.Errorf("戻した後の削除: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// LIKE は大文字小文字を区別する（MySQL の utf8mb4_bin と同じ）
	if err := db.QueryRow("SELECT COUNT(*) FROM users WHERE login LIKE 'A%'").Scan(&n); err != nil || n != 0 {
		t.Errorf("LIKE: %d %v", n, err)
	}
}

func upsertSetting(t *testing.T, db *sql.DB, v string) error {
	_, err := db.Exec("INSERT INTO system_settings (name, value) VALUES (?, ?) ON DUPLICATE KEY UPDATE value = VALUES(value)", "k", v)
	return err
}

// 同時の書き込み: 複数の接続から読んでから書くトランザクションを並べても SQLITE_BUSY にならない（BEGIN IMMEDIATE・busy_timeout）。
func TestSQLiteConcurrentWrites(t *testing.T) {
	db, _ := sqliteTestDB(t)
	ctx := context.Background()
	if _, err := Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, "INSERT INTO projects (slug, prefix, width, name) VALUES ('p', 'P', 4, 'p')")
	const workers, each = 8, 20
	errs := make(chan error, workers)
	for w := 0; w < workers; w++ {
		go func() {
			for i := 0; i < each; i++ {
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					errs <- err
					return
				}
				var c int
				if err := tx.QueryRow("SELECT counter FROM projects WHERE id = 1 FOR UPDATE").Scan(&c); err != nil {
					tx.Rollback()
					errs <- err
					return
				}
				if _, err := tx.Exec("UPDATE projects SET counter = ? WHERE id = 1", c+1); err != nil {
					tx.Rollback()
					errs <- err
					return
				}
				if err := tx.Commit(); err != nil {
					errs <- err
					return
				}
			}
			errs <- nil
		}()
	}
	for w := 0; w < workers; w++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	var c int
	db.QueryRow("SELECT counter FROM projects WHERE id = 1").Scan(&c)
	if c != workers*each {
		t.Errorf("counter = %d, want %d（更新が失われた）", c, workers*each)
	}
}

func TestSQLiteMemory(t *testing.T) {
	db, err := Open("sqlite::memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&n); err != nil || n == 0 {
		t.Fatalf("schema_migrations = %d %v", n, err)
	}
	if _, err := Open("sqlite:"); err == nil {
		t.Error("パスの無い sqlite: を受け付けた")
	}
}

// SQLite のスキーマが MySQL と同じ表・列（名前・NULL 可否）を持つこと。MySQL（LOOPTRACK_TEST_DSN）があるときだけ比べる。
func TestSQLiteSchemaMatchesMySQL(t *testing.T) {
	if os.Getenv("LOOPTRACK_TEST_DSN") == "" {
		t.Skip("LOOPTRACK_TEST_DSN が未設定のため MySQL とのスキーマの比較を省略")
	}
	ctx := context.Background()
	my, dbName, _ := testDB(t)
	if _, err := Migrate(ctx, my, migrations.FS); err != nil {
		t.Fatal(err)
	}
	lite, _ := sqliteTestDB(t)
	if _, err := Migrate(ctx, lite, migrations.FS); err != nil {
		t.Fatal(err)
	}
	myCols := map[string][]string{}
	rows, err := my.Query(`SELECT table_name, column_name, is_nullable FROM information_schema.columns WHERE table_schema = ?`, dbName)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var tbl, col, null string
		if err := rows.Scan(&tbl, &col, &null); err != nil {
			t.Fatal(err)
		}
		myCols[tbl] = append(myCols[tbl], col+" null="+null)
	}
	rows.Close()
	liteCols := map[string][]string{}
	rows, err = lite.Query(`SELECT m.name, p.name, p."notnull" FROM sqlite_master m JOIN pragma_table_info(m.name) p
WHERE m.type = 'table' AND m.name NOT LIKE 'sqlite_%' AND m.name <> 'append_only_unlock'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var tbl, col string
		var notNull int
		if err := rows.Scan(&tbl, &col, &notNull); err != nil {
			t.Fatal(err)
		}
		// SQLite の INTEGER PRIMARY KEY は NOT NULL を宣言しても pragma では notnull=1。その他は宣言どおり
		null := "YES"
		if notNull == 1 {
			null = "NO"
		}
		liteCols[tbl] = append(liteCols[tbl], col+" null="+null)
	}
	rows.Close()
	var tables []string
	for tbl := range myCols {
		tables = append(tables, tbl)
	}
	for tbl := range liteCols {
		if _, ok := myCols[tbl]; !ok {
			tables = append(tables, tbl)
		}
	}
	sort.Strings(tables)
	for _, tbl := range tables {
		a, b := myCols[tbl], liteCols[tbl]
		sort.Strings(a)
		sort.Strings(b)
		if strings.Join(a, ", ") != strings.Join(b, ", ") {
			t.Errorf("%s の列が違います（migrations/sqlite の対を直す）\nMySQL:  %s\nSQLite: %s", tbl, strings.Join(a, ", "), strings.Join(b, ", "))
		}
	}
}
