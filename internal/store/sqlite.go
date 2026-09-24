package store

// SQLite で動かすための方言の吸収。
//
// アプリの SQL は MySQL の方言で書いたまま、SQLite の接続では次の 3 か所で差を吸収する（MySQL で発行される SQL は変えない）:
//   - SQL の書き換え（sqliteSQL）: CURRENT_TIMESTAMP(6)・INTERVAL の加減算・FOR UPDATE・JSON_UNQUOTE・
//     ON DUPLICATE KEY UPDATE など、アプリとテストが使っている MySQL 固有の構文だけを SQLite の構文に直す。
//   - 引数の変換（sqliteArgs）: time.Time は UTC の固定長の文字列（sqliteTimeLayout）に、UTF-8 として正しい []byte は TEXT にする
//     （SQLite の JSON 関数は BLOB を JSONB と解釈するため。JSON を []byte で渡している箇所がある）。
//   - 結果の変換（sqliteRows）: 式（MAX(at) など宣言型の無い列）で返る sqliteTimeLayout の文字列を time.Time にする
//     （DATETIME と宣言した列はドライバが time.Time にする）。
//
// 時刻は DATETIME 列に「YYYY-MM-DD HH:MM:SS.ffffff」（UTC・マイクロ秒 6 桁固定）の文字列で持ち、文字列の比較で大小が決まる。
// SQLite の現在時刻（strftime の %f）はミリ秒までなので、DB の現在時刻は末尾 3 桁が 000 になる。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/privfile"
	"modernc.org/sqlite"
)

// SQLiteDriverName は SQLite の接続に使う database/sql のドライバ名（modernc.org/sqlite を包んだもの）。
const SQLiteDriverName = "im-sqlite"

// sqliteTimeLayout は SQLite に保存する時刻の書式（UTC・マイクロ秒 6 桁固定。文字列の比較で大小が決まる）。
const sqliteTimeLayout = "2006-01-02 15:04:05.000000"

// sqliteNow は CURRENT_TIMESTAMP(6) の置き換え（ミリ秒まで。sqliteTimeLayout と同じ桁数にそろえる）。
const sqliteNow = "(strftime('%Y-%m-%d %H:%M:%f', 'now') || '000')"

func init() {
	sql.Register(SQLiteDriverName, &sqliteDriver{})
}

type sqliteDriver struct{}

func (d *sqliteDriver) Open(name string) (driver.Conn, error) {
	c, err := (&sqlite.Driver{}).Open(name)
	if err != nil {
		return nil, err
	}
	return &sqliteConn{c: c}, nil
}

// sqliteConn は modernc.org/sqlite の接続を包み、SQL と引数・結果を変換する。
type sqliteConn struct{ c driver.Conn }

func (c *sqliteConn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}

func (c *sqliteConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	query = sqliteSQL(query)
	var s driver.Stmt
	var err error
	if p, ok := c.c.(driver.ConnPrepareContext); ok {
		s, err = p.PrepareContext(ctx, query)
	} else {
		s, err = c.c.Prepare(query)
	}
	if err != nil {
		return nil, err
	}
	return &sqliteStmt{s: s}, nil
}

func (c *sqliteConn) Close() error { return c.c.Close() }

func (c *sqliteConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *sqliteConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if b, ok := c.c.(driver.ConnBeginTx); ok {
		return b.BeginTx(ctx, opts)
	}
	return c.c.Begin() //nolint:staticcheck
}

func (c *sqliteConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	e, ok := c.c.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return e.ExecContext(ctx, sqliteSQL(query), sqliteArgs(args))
}

func (c *sqliteConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	q, ok := c.c.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	r, err := q.QueryContext(ctx, sqliteSQL(query), sqliteArgs(args))
	if err != nil {
		return nil, err
	}
	return &sqliteRows{Rows: r}, nil
}

func (c *sqliteConn) Ping(ctx context.Context) error {
	if p, ok := c.c.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c *sqliteConn) ResetSession(ctx context.Context) error {
	if r, ok := c.c.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (c *sqliteConn) IsValid() bool {
	if v, ok := c.c.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

type sqliteStmt struct{ s driver.Stmt }

func (s *sqliteStmt) Close() error  { return s.s.Close() }
func (s *sqliteStmt) NumInput() int { return s.s.NumInput() }

func (s *sqliteStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.ExecContext(context.Background(), namedValues(args))
}

func (s *sqliteStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.QueryContext(context.Background(), namedValues(args))
}

func (s *sqliteStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if e, ok := s.s.(driver.StmtExecContext); ok {
		return e.ExecContext(ctx, sqliteArgs(args))
	}
	return s.s.Exec(plainValues(sqliteArgs(args))) //nolint:staticcheck
}

func (s *sqliteStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	var r driver.Rows
	var err error
	if q, ok := s.s.(driver.StmtQueryContext); ok {
		r, err = q.QueryContext(ctx, sqliteArgs(args))
	} else {
		r, err = s.s.Query(plainValues(sqliteArgs(args))) //nolint:staticcheck
	}
	if err != nil {
		return nil, err
	}
	return &sqliteRows{Rows: r}, nil
}

func namedValues(args []driver.Value) []driver.NamedValue {
	out := make([]driver.NamedValue, len(args))
	for i, v := range args {
		out[i] = driver.NamedValue{Ordinal: i + 1, Value: v}
	}
	return out
}

func plainValues(args []driver.NamedValue) []driver.Value {
	out := make([]driver.Value, len(args))
	for i, a := range args {
		out[i] = a.Value
	}
	return out
}

// sqliteRows は式の列（宣言型が空）で返る時刻の文字列を time.Time にする。
type sqliteRows struct {
	driver.Rows
}

func (r *sqliteRows) Next(dest []driver.Value) error {
	if err := r.Rows.Next(dest); err != nil {
		return err
	}
	for i, v := range dest {
		s, ok := v.(string)
		if !ok || !isSQLiteTime(s) || r.ColumnTypeDatabaseTypeName(i) != "" {
			continue
		}
		if t, err := time.Parse(sqliteTimeLayout, s); err == nil {
			dest[i] = t
		}
	}
	return nil
}

func (r *sqliteRows) ColumnTypeDatabaseTypeName(i int) string {
	if c, ok := r.Rows.(driver.RowsColumnTypeDatabaseTypeName); ok {
		return c.ColumnTypeDatabaseTypeName(i)
	}
	return ""
}

func (r *sqliteRows) ColumnTypeNullable(i int) (nullable, ok bool) {
	if c, ok := r.Rows.(driver.RowsColumnTypeNullable); ok {
		return c.ColumnTypeNullable(i)
	}
	return false, false
}

func (r *sqliteRows) ColumnTypeLength(i int) (int64, bool) {
	if c, ok := r.Rows.(driver.RowsColumnTypeLength); ok {
		return c.ColumnTypeLength(i)
	}
	return 0, false
}

// isSQLiteTime は s が sqliteTimeLayout の形（YYYY-MM-DD HH:MM:SS.ffffff）かを返す。
func isSQLiteTime(s string) bool {
	if len(s) != len(sqliteTimeLayout) {
		return false
	}
	for i := 0; i < len(s); i++ {
		c, l := s[i], sqliteTimeLayout[i]
		if l >= '0' && l <= '9' {
			if c < '0' || c > '9' {
				return false
			}
		} else if c != l {
			return false
		}
	}
	return true
}

// sqliteArgs は引数を SQLite に渡す形にする（time.Time → 固定長の UTC 文字列、UTF-8 の []byte → 文字列）。
func sqliteArgs(args []driver.NamedValue) []driver.NamedValue {
	var out []driver.NamedValue
	for i, a := range args {
		var v driver.Value
		switch x := a.Value.(type) {
		case time.Time:
			v = x.UTC().Format(sqliteTimeLayout)
		case []byte:
			if x == nil || !utf8.Valid(x) {
				continue
			}
			v = string(x)
		default:
			continue
		}
		if out == nil {
			out = append([]driver.NamedValue(nil), args...)
		}
		out[i].Value = v
	}
	if out == nil {
		return args
	}
	return out
}

var (
	// 「式 ± INTERVAL n 単位」。式は CURRENT_TIMESTAMP(6) 等・? ・列名（別名付き）・文字列リテラル。
	reSQLiteInterval = regexp.MustCompile(`(CURRENT_TIMESTAMP(?:\(\d?\))?|NOW\(\d?\)|UTC_TIMESTAMP\(\d?\)|\?|'[^']*'|[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)?)\s*([+-])\s*INTERVAL\s+(\?|\d+)\s+(SECOND|MINUTE|HOUR|DAY)\b`)
	reSQLiteNow      = regexp.MustCompile(`\bCURRENT_TIMESTAMP\b(?:\(\d?\))?|\bNOW\(\d?\)|\bUTC_TIMESTAMP\(\d?\)`)
	reSQLiteForUpd   = regexp.MustCompile(`\s+(?:FOR UPDATE|FOR SHARE|LOCK IN SHARE MODE)\b`)
	reSQLiteUpsert   = regexp.MustCompile(`\bON DUPLICATE KEY UPDATE\b`)
	reSQLiteValues   = regexp.MustCompile(`\bVALUES\(\s*([A-Za-z_][A-Za-z0-9_]*)\s*\)`)
	reSQLiteFromDual = regexp.MustCompile(`\s+FROM DUAL\b`)
	reSQLiteLeft     = regexp.MustCompile(`\bLEFT\(([A-Za-z_][A-Za-z0-9_.]*),\s*(\d+)\)`) // LEFT(列, n)（LEFT JOIN は括弧が続かない）
)

var sqliteUnits = map[string]string{"SECOND": "seconds", "MINUTE": "minutes", "HOUR": "hours", "DAY": "days"}

// sqliteSQL は MySQL の方言で書いた SQL を SQLite で動く形に直す。
// 対象はアプリとテストが使っている構文だけ（汎用の変換器ではない）。MySQL の接続では呼ばない。
func sqliteSQL(q string) string {
	if strings.Contains(q, "INTERVAL") {
		q = reSQLiteInterval.ReplaceAllStringFunc(q, func(m string) string {
			p := reSQLiteInterval.FindStringSubmatch(m)
			base := p[1]
			if reSQLiteNow.MatchString(base) && !strings.HasPrefix(base, "'") {
				base = "'now'"
			}
			unit := sqliteUnits[p[4]]
			var mod string
			if p[3] == "?" {
				mod = "'" + p[2] + "' || ? || ' " + unit + "'"
			} else {
				mod = "'" + p[2] + p[3] + " " + unit + "'"
			}
			return "(strftime('%Y-%m-%d %H:%M:%f', " + base + ", " + mod + ") || '000')"
		})
	}
	q = reSQLiteNow.ReplaceAllString(q, sqliteNow)
	q = reSQLiteForUpd.ReplaceAllString(q, "")
	q = strings.ReplaceAll(q, "JSON_UNQUOTE(", "(") // SQLite の json_extract は文字列を引用符なしで返す
	q = strings.ReplaceAll(q, "INSERT IGNORE ", "INSERT OR IGNORE ")
	q = strings.ReplaceAll(q, "LAST_INSERT_ID()", "last_insert_rowid()")
	q = reSQLiteFromDual.ReplaceAllString(q, "")
	q = reSQLiteLeft.ReplaceAllString(q, "substr($1, 1, $2)") // どちらも文字数で数える
	if loc := reSQLiteUpsert.FindStringIndex(q); loc != nil {
		tail := reSQLiteValues.ReplaceAllString(q[loc[1]:], "excluded.$1")
		q = q[:loc[0]] + "ON CONFLICT DO UPDATE SET" + tail
	}
	return q
}

// IsSQLite は db が SQLite の接続（Open に sqlite: の DSN を渡したもの）かを返す。
func IsSQLite(db *sql.DB) bool {
	_, ok := db.Driver().(*sqliteDriver)
	return ok
}

// sqliteCode は SQLite のエラーの拡張結果コードを返す（SQLite のエラーでなければ 0）。
func sqliteCode(err error) int {
	var se *sqlite.Error
	if errors.As(err, &se) {
		return se.Code()
	}
	return 0
}

// SQLite の拡張結果コード（sqlite3.h）。
const (
	sqliteBusy              = 5
	sqliteLocked            = 6
	sqliteConstraintTrigger = 1811 // RAISE(ABORT, …)（追記専用のトリガ）
	sqliteConstraintPK      = 1555
	sqliteConstraintUnique  = 2067
)

// IsAppendOnlyViolation は追記専用の表（comments・issue_events など）を書き換え・削除しようとして拒否されたエラーかを返す。
// MySQL はアプリ用ユーザーの権限（deploy/grants.sql）で、SQLite はトリガ（migrations/sqlite）で拒否する。
func IsAppendOnlyViolation(err error) bool {
	if n := mysqlErrNumber(err); n == 1142 || n == 1227 {
		return true
	}
	return sqliteCode(err) == sqliteConstraintTrigger && strings.Contains(err.Error(), "append-only")
}

// UnlockAppendOnly は管理用の置き換え（transfer の取り込み）のために、tx の間だけ追記専用の表の UPDATE / DELETE を許す。
// SQLite では append_only_unlock に 1 行入れてトリガを外し、返す関数（commit の前に呼ぶ）でその行を消して戻す
// （行は tx の中だけのもので、他の接続からは見えない）。MySQL では何もしない（管理用の資格情報で動かす）。
func UnlockAppendOnly(ctx context.Context, db *sql.DB, tx *sql.Tx) (relock func() error, err error) {
	if !IsSQLite(db) {
		return func() error { return nil }, nil
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO append_only_unlock (id) VALUES (1)"); err != nil {
		return nil, err
	}
	return func() error {
		_, err := tx.ExecContext(ctx, "DELETE FROM append_only_unlock")
		return err
	}, nil
}

// sqliteDSN は sqlite:<path> の <path> を modernc.org/sqlite の DSN にする。
// WAL・busy_timeout・外部キー・LIKE の大文字小文字の区別（MySQL の utf8mb4_bin に合わせる）を接続ごとに設定し、
// トランザクションは BEGIN IMMEDIATE で始める（読んでから書くトランザクションの途中で SQLITE_BUSY にならないよう、書き込みを直列化する）。
func sqliteDSN(path string) (string, error) {
	if path == "" {
		return "", i18n.Errorf("store.err.sqlite.no_path")
	}
	if strings.ContainsAny(path, "?#") {
		return "", i18n.Errorf("store.err.sqlite.bad_path", "path", path)
	}
	params := []string{
		"_pragma=busy_timeout(10000)",
		"_pragma=foreign_keys(1)",
		"_pragma=case_sensitive_like(1)",
		"_txlock=immediate",
	}
	if path != ":memory:" {
		params = append(params, "_pragma=journal_mode(WAL)", "_pragma=synchronous(NORMAL)")
	}
	return path + "?" + strings.Join(params, "&"), nil
}

// openSQLite は SQLite のファイル（または :memory:）を開く。:memory: は接続ごとに別の DB になるため接続を 1 本にする。
// ファイルが無ければ、開く前に本人だけの空のファイルとして作る（SQLiteFiles の説明）。
func openSQLite(path string) (*sql.DB, error) {
	dsn, err := sqliteDSN(path)
	if err != nil {
		return nil, err
	}
	if path != ":memory:" {
		if _, err := privfile.CreateEmpty(path); err != nil {
			return nil, i18n.Wrapf(err, "store.err.sqlite.create", "path", path)
		}
	}
	db, err := sql.Open(SQLiteDriverName, dsn)
	if err != nil {
		return nil, err
	}
	if path == ":memory:" {
		db.SetMaxOpenConns(1)
		db.SetConnMaxLifetime(0)
		db.SetConnMaxIdleTime(0)
	}
	return db, nil
}

// SQLiteFiles は SQLite の DB が使うファイル（本体・-wal・-shm）のパスを返す。
//
// 権限: 中身にはパスワードのハッシュ・暗号化した TOTP の秘密・トークンのハッシュ・イシューの本文が入るので本人だけにする。
//   - 本体: openSQLite が開く前に本人だけの空のファイルとして作る（privfile.CreateEmpty。unix は 0600、Windows は本人だけの ACL）。
//     SQLite に作らせると umask のまま（多くは 0644）になる。
//   - -wal・-shm（unix）: SQLite の unix の VFS は本体の権限（と所有者）をそのまま引き継いで作り、umask で落ちた分は fchmod で戻す
//     （unixOpen の findCreateFileMode・unixOpenSharedMemory・robust_open。modernc.org/sqlite は C の SQLite を Go に変換したもので
//     同じ動き。sqlite_perm_unix_test.go で umask 022 のもと 0600 になることを確かめている）。本体を 0600 にすれば足りる。
//   - -wal・-shm（Windows）: SQLite の Windows の VFS はセキュリティ記述子を指定せずに作るので、ディレクトリの継承する ACE で決まる
//     （本体の ACL は引き継がない）。-wal は最後の接続を閉じると消え、次に開くとき作り直されるので、先に作って保護しても続かない。
//     そのため DB のディレクトリを作るとき（setup・ローカルモードの鍵のファイル）に本人だけの継承する ACL にし（privfile.ProtectDir）、
//     既にあるディレクトリが広いときは起動時の警告（CheckSQLitePerms）で知らせる。
func SQLiteFiles(path string) []string {
	return []string{path, path + "-wal", path + "-shm"}
}

// CheckSQLitePerms は SQLite の DB のファイル（SQLiteFiles）のうち、本人以外も読めるものを返す（無いファイルは飛ばす）。
// 起動時の警告に使う（自動では直さない。サービスの利用者とグループで共有する運用を壊さないため）。
func CheckSQLitePerms(path string) (broad []string) {
	if path == "" || path == ":memory:" {
		return nil
	}
	for _, f := range SQLiteFiles(path) {
		var pe *privfile.PermError
		if err := privfile.Check(f); errors.As(err, &pe) {
			broad = append(broad, f)
		}
	}
	return broad
}
