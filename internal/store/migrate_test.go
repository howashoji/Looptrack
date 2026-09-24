package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/howashoji/looptrack/deploy"
	"github.com/howashoji/looptrack/migrations"
)

func TestSplitStatements(t *testing.T) {
	src := "-- コメント\nCREATE TABLE a (\n  x INT -- 行末コメントは残る\n);\n\n-- もう一つ\nCREATE TABLE b (y INT);\n"
	got := SplitStatements(src)
	if len(got) != 2 || !strings.HasPrefix(got[0], "CREATE TABLE a") || got[1] != "CREATE TABLE b (y INT)" {
		t.Fatalf("got %q", got)
	}
}

func TestLoadMigrations(t *testing.T) {
	fsys := fstest.MapFS{
		"0002_b.sql": {Data: []byte("SELECT 2;")},
		"0001_a.sql": {Data: []byte("SELECT 1;")},
		"README.md":  {Data: []byte("無視される")},
	}
	got, err := LoadMigrations(fsys)
	if err != nil || len(got) != 2 || got[0].Version != 1 || got[1].Name != "0002_b.sql" {
		t.Fatalf("got %+v err %v", got, err)
	}
	dup := fstest.MapFS{"0001_a.sql": {Data: []byte("x")}, "0001_b.sql": {Data: []byte("y")}}
	if _, err := LoadMigrations(dup); err == nil {
		t.Fatal("番号の重複を検出しなかった")
	}
	if _, err := LoadMigrations(migrations.FS); err != nil {
		t.Fatalf("埋め込みのマイグレーション: %v", err)
	}
}

// testDB は LOOPTRACK_TEST_DSN（DB 名なし・管理者権限）の MySQL に使い捨ての DB を作る。
// 例: LOOPTRACK_TEST_DSN='root:imdev@tcp(127.0.0.1:13306)/?parseTime=true'（deploy/dev/compose.yaml）
func testDB(t *testing.T) (admin *sql.DB, dbName string, cfg *mysql.Config) {
	t.Helper()
	dsn := os.Getenv("LOOPTRACK_TEST_DSN")
	if dsn == "" {
		t.Skip("LOOPTRACK_TEST_DSN が未設定のため DB を使うテストを省略（deploy/dev/compose.yaml を参照）")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	root, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	dbName = "im_test_" + randHex(t, 6)
	if _, err := root.Exec("CREATE DATABASE " + dbName + " CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Exec("DROP DATABASE " + dbName) })
	c := cfg.Clone()
	c.DBName = dbName
	admin, err = sql.Open("mysql", c.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	return admin, dbName, c
}

func randHex(t *testing.T, n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func TestMigrateIsIdempotent(t *testing.T) {
	db, _, _ := testDB(t)
	ctx := context.Background()
	first, err := Migrate(ctx, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) == 0 {
		t.Fatal("1 回目に何も適用されなかった")
	}
	second, err := Migrate(ctx, db, migrations.FS)
	if err != nil || len(second) != 0 {
		t.Fatalf("2 回目: applied=%v err=%v（冪等でない）", second, err)
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE()").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 23 { // 22 テーブル + schema_migrations
		t.Errorf("テーブル数 = %d, want 23", n)
	}
}

// TestAppUserCannotRewriteHistory は、アプリ用ユーザーが comments / issue_events を書き換え・削除できず、
// issues / projects を削除できないことを確かめる。
func TestAppUserCannotRewriteHistory(t *testing.T) {
	db, dbName, adminCfg := testDB(t)
	ctx := context.Background()
	if _, err := Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	user, pass := "im_app_t_"+randHex(t, 4), randHex(t, 16)
	if _, err := db.Exec("CREATE USER '" + user + "'@'%' IDENTIFIED BY '" + pass + "'"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec("DROP USER '" + user + "'@'%'") })
	grants := strings.NewReplacer("im.", dbName+".", "'im_app'@'%'", "'"+user+"'@'%'").Replace(deploy.GrantsSQL)
	for _, stmt := range SplitStatements(grants) {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	mustExec(t, db, "INSERT INTO projects (slug, prefix, width, name) VALUES ('t', 'T', 4, 't')")
	mustExec(t, db, "INSERT INTO issues (project_id, number, display_id, file_name, front_keys, body_main) VALUES (1, 1, 'T-0001', 'T-0001-x.md', '[]', '')")

	appCfg := adminCfg.Clone()
	appCfg.User, appCfg.Passwd = user, pass
	app, err := sql.Open("mysql", appCfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	mustExec(t, app, "INSERT INTO comments (issue_id, seq, ts, content, via) VALUES (1, 1, '2026-09-17 12:00', 'c', 'cli')")
	mustExec(t, app, "INSERT INTO issue_events (project_id, issue_id, kind, via) VALUES (1, 1, 'comment', 'cli')")
	mustExec(t, app, "UPDATE issues SET title = 'ok' WHERE id = 1")

	for _, stmt := range []string{
		"UPDATE comments SET content = '改ざん' WHERE issue_id = 1",
		"DELETE FROM comments WHERE issue_id = 1",
		"UPDATE issue_events SET kind = 'x'",
		"DELETE FROM issue_events",
		"DELETE FROM issues WHERE id = 1",
		"DELETE FROM projects",
		"DROP TABLE comments",
	} {
		_, err := app.Exec(stmt)
		var me *mysql.MySQLError
		if !errors.As(err, &me) || (me.Number != 1142 && me.Number != 1227) {
			t.Errorf("%s: err = %v, want 権限エラー（1142）", stmt, err)
		}
	}
}

func mustExec(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	if _, err := db.Exec(q); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// ロックの名前はスキーマ名で修飾する（GET_LOCK の名前は MySQL のインスタンス全体で共有されるため）。
func TestMigrateLockName(t *testing.T) {
	if got := migrateLockName("im"); got != "im_migrate:im" {
		t.Errorf("migrateLockName(im) = %q", got)
	}
	if a, b := migrateLockName("im_test_a"), migrateLockName("im_test_b"); a == b {
		t.Errorf("別のスキーマで同じ名前になった: %q", a)
	}
	// DSN に DB 名が無い（スキーマ未選択）ときは修飾しようがないので従来の固定名。
	if got := migrateLockName(""); got != "im_migrate" {
		t.Errorf("migrateLockName(\"\") = %q", got)
	}
	// MySQL の名前付きロックは 64 文字まで。収まらない長さのスキーマ名はハッシュにする。
	long := strings.Repeat("s", 64)
	got := migrateLockName(long)
	if len(got) > 64 {
		t.Errorf("64 文字を超えた（%d 文字）: %q", len(got), got)
	}
	if !strings.HasPrefix(got, migrateLockPrefix) || got == migrateLockPrefix+long {
		t.Errorf("長いスキーマ名がハッシュになっていない: %q", got)
	}
	if other := migrateLockName(strings.Repeat("t", 64)); other == got {
		t.Errorf("別の長いスキーマ名で同じ名前になった: %q", got)
	}
}

// 別のスキーマの migrate が進行中でも待たされないこと。
// 固定名 'im_migrate' だったころは、使い捨ての DB を作るテストが同じ MySQL 上で 1 本の列に並び、
// 60 秒のロック待ちが積み上がって go test の既定タイムアウトに達していた。
func TestMigrateLockDoesNotBlockOtherSchemas(t *testing.T) {
	held, heldName, _ := testDB(t)
	other, _, _ := testDB(t)

	ctx := context.Background()
	conn, err := held.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 5)", migrateLockName(heldName)).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Valid || got.Int64 != 1 {
		t.Fatalf("先に取るロックを取得できなかった: %v", got)
	}
	defer conn.ExecContext(context.Background(), "DO RELEASE_LOCK(?)", migrateLockName(heldName)) //nolint:errcheck

	// 直列化されていれば GET_LOCK の 60 秒待ちに入る。待ちに入ったことを 30 秒で確定的に失敗させる。
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := Migrate(waitCtx, other, migrations.FS); err != nil {
		t.Fatalf("別のスキーマの migrate が %q のロック待ちで失敗した: %v", migrateLockName(heldName), err)
	}
}
