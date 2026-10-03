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
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/go-sql-driver/mysql"

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

// Requirement は grants.sql の GRANT の 1 行が求めるもの（表と、その表の権限）。
type Requirement struct {
	Table string
	Privs []string // grants.sql に書いた順（SELECT・INSERT・UPDATE・DELETE のどれか）
}

// probes は Check が権限ごとに流す文の形（%[1]s は表の名前、%[2]s は列の名前）。
// どれも行に触れない（WHERE 1 = 0）ので、権限の確かめだけが働く。INSERT と UPDATE は表の列を読まない形にする
// （列を読むと SELECT の権限も要り、SELECT だけが欠けたときに INSERT・UPDATE まで欠けたと数えてしまう）。
var probes = map[string]string{
	"SELECT": "SELECT 1 FROM `%[1]s` WHERE 1 = 0",
	"INSERT": "INSERT INTO `%[1]s` (`%[2]s`) SELECT NULL FROM DUAL WHERE 1 = 0",
	"UPDATE": "UPDATE `%[1]s` SET `%[2]s` = NULL WHERE 1 = 0",
	"DELETE": "DELETE FROM `%[1]s` WHERE 1 = 0",
}

// probeNeedsColumn は、確かめの文に列の名前が要る権限。
var probeNeedsColumn = map[string]bool{"INSERT": true, "UPDATE": true}

// Requirements は deploy/grants.sql の GRANT の行を、表と権限の並びにして返す（Statements と同じ行を読む）。
// 確かめ方を知らない権限が grants.sql に入ったら、黙って飛ばさずに誤りを返す。
func Requirements() ([]Requirement, error) {
	stmts, err := Statements(sourceDB, "im_app")
	if err != nil {
		return nil, err
	}
	var out []Requirement
	for _, s := range stmts {
		on := strings.Index(s, " ON ")
		rest := strings.TrimLeft(s[on+len(" ON "):], " ")
		table := strings.TrimPrefix(rest[:strings.IndexByte(rest, ' ')], sourceDB+".")
		r := Requirement{Table: table}
		for _, p := range strings.Split(strings.TrimPrefix(s[:on], "GRANT"), ",") {
			p = strings.TrimSpace(p)
			if _, ok := probes[p]; !ok {
				return nil, fmt.Errorf("deploy/grants.sql: no way to check privilege %q on %s", p, table)
			}
			r.Privs = append(r.Privs, p)
		}
		out = append(out, r)
	}
	return out, nil
}

// Check は、アプリ用の利用者の接続 app（MySQL）で、grants.sql が挙げる全部の表にそこに書いた権限があるかを確かめ、
// 足りないもの（表と、足りない権限）を返す。そろっていれば空。インストーラ（looptrack grants check）と
// looptrack grants apply の最後の確かめが、どちらもこれを使う（確かめの規則を 1 か所に置くため）。
//
// 権限は SHOW GRANTS を読み解かずに、権限ごとに行に触れない文を実際に流して確かめる。DB 単位の広い権限・ロール経由の
// 権限でも、サーバが実際に許すかどうかで判断できるため。文は取り消すトランザクションの中で流す（行は変わらない）。
// 権限の拒否（1142・1143）だけを不足として数え、表が無い・繋がらないなどほかの誤りはそのまま返す。
func Check(ctx context.Context, app *sql.DB) ([]Requirement, error) {
	reqs, err := Requirements()
	if err != nil {
		return nil, err
	}
	tx, err := app.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // 確かめの文は行に触れないが、念のため必ず取り消す
	var missing []Requirement
	for _, r := range reqs {
		m := Requirement{Table: r.Table}
		for _, p := range r.Privs {
			q := fmt.Sprintf(probes[p], r.Table)
			if probeNeedsColumn[p] {
				var col string
				err := tx.QueryRowContext(ctx, "SELECT COLUMN_NAME FROM information_schema.COLUMNS "+
					"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION LIMIT 1", r.Table).Scan(&col)
				if errors.Is(err, sql.ErrNoRows) {
					// 列が 1 つも見えない = この表に何の権限も無い
					m.Privs = append(m.Privs, p)
					continue
				}
				if err != nil {
					return nil, err
				}
				q = fmt.Sprintf(probes[p], r.Table, col)
			}
			if _, err := tx.ExecContext(ctx, q); err != nil {
				var me *mysql.MySQLError
				if errors.As(err, &me) && (me.Number == 1142 || me.Number == 1143) {
					m.Privs = append(m.Privs, p)
					continue
				}
				return nil, fmt.Errorf("%s: %w", r.Table, err)
			}
		}
		if len(m.Privs) > 0 {
			missing = append(missing, m)
		}
	}
	return missing, nil
}

// FormatMissing は Check の結果を「表 (権限, 権限)」を ; で区切った 1 行にする（案内と誤りの文面に使う。言語に依らない形）。
func FormatMissing(missing []Requirement) string {
	parts := make([]string, 0, len(missing))
	for _, m := range missing {
		parts = append(parts, m.Table+" ("+strings.Join(m.Privs, ", ")+")")
	}
	return strings.Join(parts, "; ")
}
