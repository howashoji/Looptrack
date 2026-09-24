// Package testutil はテスト用の補助（使い捨てのデータベース等）。
package testutil

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"

	"github.com/howashoji/looptrack/deploy"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/migrations"
)

// テストの DB は環境変数で選ぶ:
//
//	LOOPTRACK_TEST_DB=mysql   LOOPTRACK_TEST_DSN（DB 名なし・管理者権限）の MySQL に使い捨ての DB を作る（LOOPTRACK_TEST_DSN が無ければ省略）
//	LOOPTRACK_TEST_DB=sqlite  一時ディレクトリの SQLite のファイル（外部サービス不要）
//	LOOPTRACK_TEST_DB=skip    DB を使うテストを省略する
//	未設定             LOOPTRACK_TEST_DSN があれば mysql、無ければ sqlite
//
// 例（deploy/dev/compose.yaml）:
//
//	LOOPTRACK_TEST_DSN='root:imdev@tcp(127.0.0.1:13306)/?parseTime=true' go test ./...      # MySQL
//	LOOPTRACK_TEST_DB=sqlite go test ./...                                                   # SQLite
const (
	DialectMySQL  = "mysql"
	DialectSQLite = "sqlite"
)

// Dialect はテストで使う DB の種類（DialectMySQL / DialectSQLite）を返す。省略するときは "" を返す。
func Dialect() string {
	switch os.Getenv("LOOPTRACK_TEST_DB") {
	case DialectMySQL:
		return DialectMySQL
	case DialectSQLite:
		return DialectSQLite
	case "skip", "none":
		return ""
	}
	if os.Getenv("LOOPTRACK_TEST_DSN") != "" {
		return DialectMySQL
	}
	return DialectSQLite
}

// SQLite はテストの DB が SQLite かを返す。
func SQLite() bool { return Dialect() == DialectSQLite }

func skipWithoutDB(t testing.TB) string {
	t.Helper()
	d := Dialect()
	switch {
	case d == "":
		t.Skip("LOOPTRACK_TEST_DB=skip のため DB を使うテストを省略")
	case d == DialectMySQL && os.Getenv("LOOPTRACK_TEST_DSN") == "":
		t.Skip("LOOPTRACK_TEST_DSN が未設定のため DB を使うテストを省略（deploy/dev/compose.yaml を参照）")
	}
	return d
}

// MigratedDB は使い捨ての DB をマイグレーション済みで返す（DB の種類は Dialect）。
func MigratedDB(t testing.TB) *sql.DB {
	t.Helper()
	if skipWithoutDB(t) == DialectSQLite {
		db, _ := sqliteDB(t)
		return db
	}
	db, _, _ := migrated(t)
	return db
}

// AppDB は MigratedDB と同じ使い捨ての DB に、本番と同じ権限（deploy/grants.sql）のアプリ用ユーザーを作り、
// 管理者の接続とアプリ用ユーザーの接続を返す。サーバをアプリ用ユーザーで動かし、権限不足を統合テストで検出するため。
// SQLite には利用者の権限が無いので、同じファイルへの別の接続を 2 つ返す（追記専用はトリガで担保する）。
func AppDB(t testing.TB) (admin, app *sql.DB) {
	t.Helper()
	if skipWithoutDB(t) == DialectSQLite {
		admin, path := sqliteDB(t)
		app, err := store.Open(store.SQLitePrefix + path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { app.Close() })
		return admin, app
	}
	admin, name, cfg := migrated(t)
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	user, pass := "im_app_"+hex.EncodeToString(b[:3]), hex.EncodeToString(b)+"Aa1!"
	if _, err := admin.Exec("CREATE USER '" + user + "'@'%' IDENTIFIED BY '" + pass + "'"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec("DROP USER '" + user + "'@'%'") })
	grants := strings.NewReplacer("im.", name+".", "'im_app'@'%'", "'"+user+"'@'%'").Replace(deploy.GrantsSQL)
	for _, stmt := range store.SplitStatements(grants) {
		if _, err := admin.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	c := cfg.Clone()
	c.User, c.Passwd = user, pass
	app, err := sql.Open("mysql", c.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.Close() })
	return admin, app
}

// sqliteDB は一時ディレクトリに SQLite のファイルを作り、マイグレーション済みの接続とファイルのパスを返す。
func sqliteDB(t testing.TB) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "im.db")
	db, err := store.Open(store.SQLitePrefix + path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() }) // TempDir の削除より先に閉じる（Cleanup は後に登録したものから動く）
	if _, err := store.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return db, path
}

func migrated(t testing.TB) (*sql.DB, string, *mysql.Config) {
	t.Helper()
	dsn := os.Getenv("LOOPTRACK_TEST_DSN")
	if dsn == "" {
		t.Skip("LOOPTRACK_TEST_DSN が未設定のため DB を使うテストを省略（deploy/dev/compose.yaml を参照）")
	}
	cfg, err := store.NormalizeDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	root, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	name := "im_test_" + hex.EncodeToString(b)
	if _, err := root.Exec("CREATE DATABASE " + name + " CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Exec("DROP DATABASE " + name) })
	c := cfg.Clone()
	c.DBName = name
	db, err := sql.Open("mysql", c.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := store.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return db, name, c
}
