package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

// SQLitePrefix は LOOPTRACK_DSN で SQLite を選ぶ接頭辞（sqlite:<ファイルのパス>。sqlite::memory: はテスト用）。
const SQLitePrefix = "sqlite:"

// SQLitePath は dsn が SQLite（sqlite:<path>）なら path と true を返す。
func SQLitePath(dsn string) (string, bool) {
	if !strings.HasPrefix(dsn, SQLitePrefix) {
		return "", false
	}
	return strings.TrimPrefix(dsn, SQLitePrefix), true
}

// NormalizeDSN は接続設定を揃える。
//   - 接続ごとの time_zone を UTC にし、ドライバの時刻も UTC で読み書きする。
//     共通 MySQL は TZ=Asia/Tokyo のため、揃えないと CURRENT_TIMESTAMP（日本時間）と Go から渡す時刻（UTC）が
//     9 時間ずれ、セッションの無操作時間やログイン試行の集計を誤る。
//   - utf8mb4、DATETIME を time.Time で読む。
func NormalizeDSN(dsn string) (*mysql.Config, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, err
	}
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params["time_zone"] = "'+00:00'"
	cfg.Params["charset"] = "utf8mb4"
	return cfg, nil
}

// Open は DSN に接続する。sqlite:<path> なら SQLite（sqlite.go）、それ以外は MySQL（NormalizeDSN を通す）。
func Open(dsn string) (*sql.DB, error) {
	if path, ok := SQLitePath(dsn); ok {
		return openSQLite(path)
	}
	cfg, err := NormalizeDSN(dsn)
	if err != nil {
		return nil, err
	}
	return sql.Open("mysql", cfg.FormatDSN())
}

func mysqlErrNumber(err error) uint16 {
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		return me.Number
	}
	return 0
}

// IsDuplicateKey は一意制約（主キー・UNIQUE）の違反かを返す（MySQL 1062・SQLite SQLITE_CONSTRAINT_UNIQUE / PRIMARYKEY）。
func IsDuplicateKey(err error) bool {
	if mysqlErrNumber(err) == 1062 {
		return true
	}
	c := sqliteCode(err)
	return c == sqliteConstraintUnique || c == sqliteConstraintPK
}

// isDuplicateOn は一意制約の違反のうち、MySQL ならキー名 mysqlKey、SQLite なら列 sqliteCols（「表.列」。エラー文の形）のものかを返す。
func isDuplicateOn(err error, mysqlKey, sqliteCols string) bool {
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		return me.Number == 1062 && strings.Contains(me.Message, mysqlKey)
	}
	return IsDuplicateKey(err) && strings.Contains(err.Error(), sqliteCols)
}

// IsRetryable はトランザクションをやり直せば通りうるエラー（MySQL のデッドロック 1213・SQLite の BUSY / LOCKED）かを返す。
func IsRetryable(err error) bool {
	if mysqlErrNumber(err) == 1213 {
		return true
	}
	switch sqliteCode(err) & 0xff {
	case sqliteBusy, sqliteLocked:
		return true
	}
	return false
}
