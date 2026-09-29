// Package dbgrants は MySQL のアプリ用の利用者に与える最小権限（deploy/grants.sql）を、接続先の DB 名・利用者名に合わせて
// 組み立て、管理用の資格情報で流す。looptrack grants（cmd/looptrack/grants.go）とインストーラ（deploy/install.sh）が使う。
//
// 権限の定義は deploy/grants.sql の 1 か所だけに置き、実行ファイルに埋め込んだもの（deploy.GrantsSQL）から取る
// （照合済みの実行ファイルから出るので、取得の鎖の外にある別のファイルを信用しなくてよい）。
package dbgrants

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"

	"github.com/howashoji/looptrack/deploy"
	"github.com/howashoji/looptrack/internal/i18n"
)

// grants.sql に書き込んである DB 名と利用者（ほかの名前の接続先では置き換える）。
const (
	sourceDB   = "im"
	sourceUser = "'im_app'@'%'"
	// UserHost はアプリ用の利用者のホスト部（grants.sql と同じ）。
	UserHost = "%"
)

var (
	// DB 名は引用なしで GRANT … ON <db>.<表> に書くので、引用の要らない文字だけにする
	dbNameRe = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)
	// 利用者名は '…' の中に書くので、引用符・バックスラッシュ・空白を含まないものだけにする
	userRe  = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,32}$`)
	grantRe = regexp.MustCompile(`^GRANT\s.+\sON\s+` + sourceDB + `\.[a-z_]+\s+TO\s+'im_app'@'%';$`)
)

// ValidDBName は GRANT に引用なしで書ける DB 名かを返す。
func ValidDBName(s string) bool { return dbNameRe.MatchString(s) }

// ValidUser は GRANT の '…' に書ける利用者名かを返す。
func ValidUser(s string) bool { return userRe.MatchString(s) }

// Statements は deploy/grants.sql の GRANT 文を、DB 名 db・利用者 user（ホスト %）に置き換えて返す。
// db・user が im・im_app なら grants.sql の GRANT の行とバイト列で同じになる（空白の揃えも保つ）。
func Statements(db, user string) ([]string, error) {
	if !ValidDBName(db) {
		return nil, i18n.Errorf("dbgrants.err.bad_db_name", "name", db)
	}
	if !ValidUser(user) {
		return nil, i18n.Errorf("dbgrants.err.bad_user", "name", user)
	}
	var out []string
	sc := bufio.NewScanner(strings.NewReader(deploy.GrantsSQL))
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t\r")
		if !strings.HasPrefix(line, "GRANT") {
			continue
		}
		if !grantRe.MatchString(line) {
			// grants.sql の形が変わった（置き換えの前提が崩れた）。黙って別の権限を流さない
			return nil, fmt.Errorf("deploy/grants.sql: unexpected GRANT line: %q", line)
		}
		on := strings.Index(line, " ON ")
		rest := strings.TrimLeft(line[on+len(" ON "):], " ")
		pad := line[on+len(" ON ") : len(line)-len(rest)]
		rest = db + "." + strings.TrimPrefix(rest, sourceDB+".")
		rest = strings.Replace(rest, sourceUser, "'"+user+"'@'"+UserHost+"'", 1)
		out = append(out, line[:on]+" ON "+pad+rest)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("deploy/grants.sql: no GRANT lines")
	}
	return out, nil
}

// Admin は管理用の資格情報で繋いだ接続での操作（テストでは偽物に差し替える）。
type Admin interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// DatabaseExists は DB が既にあるかを返す。
func DatabaseExists(ctx context.Context, a Admin, db string) (bool, error) {
	var n int
	err := a.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = ?", db).Scan(&n)
	return n > 0, err
}

// CreateDatabaseSQL は DB を作る文（文字コードは looptrack の表と同じ utf8mb4・utf8mb4_bin）。
// 作らずに止めたときの案内（自分で流す文）にも同じものを出す。db は ValidDBName を満たすもの。
func CreateDatabaseSQL(db string) string {
	return "CREATE DATABASE " + db + " CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"
}

// CreateDatabase は DB を作る（CreateDatabaseSQL）。
func CreateDatabase(ctx context.Context, a Admin, db string) error {
	if !ValidDBName(db) {
		return i18n.Errorf("dbgrants.err.bad_db_name", "name", db)
	}
	_, err := a.ExecContext(ctx, CreateDatabaseSQL(db))
	return err
}

// UserExists はアプリ用の利用者（ホスト %）が既にあるかを返す。
func UserExists(ctx context.Context, a Admin, user string) (bool, error) {
	var n int
	err := a.QueryRowContext(ctx, "SELECT COUNT(*) FROM mysql.user WHERE User = ? AND Host = ?", user, UserHost).Scan(&n)
	return n > 0, err
}

// CreateUser はアプリ用の利用者（ホスト %）を password で作る。password は接続先（LOOPTRACK_DSN）のもの。
// CREATE USER は値の差し込み（?）を受け付けないので、文字列の定数として書く。引用を壊しうる文字（' \ と制御文字）を含む
// パスワードは作らずに断る（手で作ってもらう）。
func CreateUser(ctx context.Context, a Admin, user, password string) error {
	if !ValidUser(user) {
		return i18n.Errorf("dbgrants.err.bad_user", "name", user)
	}
	if password == "" || strings.ContainsAny(password, `'\`) || strings.IndexFunc(password, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return i18n.Errorf("dbgrants.err.password_unquotable", "user", user)
	}
	_, err := a.ExecContext(ctx, "CREATE USER '"+user+"'@'"+UserHost+"' IDENTIFIED BY '"+password+"'")
	return err
}

// TablesExist は looptrack の表（schema_migrations）が DB にあるかを返す。
func TablesExist(ctx context.Context, a Admin, db string) (bool, error) {
	var n int
	err := a.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME = 'schema_migrations'", db).Scan(&n)
	return n > 0, err
}

// Apply は GRANT 文を流す。
func Apply(ctx context.Context, a Admin, stmts []string) error {
	for _, s := range stmts {
		if _, err := a.ExecContext(ctx, s); err != nil {
			return i18n.Wrapf(err, "dbgrants.err.grant", "stmt", s)
		}
	}
	return nil
}
