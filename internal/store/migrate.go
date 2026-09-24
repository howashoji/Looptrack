// Package store は DB（MySQL・SQLite）への永続化を扱う。SQL は MySQL の方言で書き、SQLite の差は sqlite.go で吸収する。
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/howashoji/looptrack/internal/i18n"
)

var migrationName = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.sql$`)

// Migration は 1 つのスキーマ変更。
type Migration struct {
	Version int
	Name    string
	SQL     string
}

// LoadMigrations は fsys 直下の NNNN_名前.sql を番号順に読む。
func LoadMigrations(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	var out []Migration
	seen := map[int]string{}
	for _, e := range entries {
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		v, _ := strconv.Atoi(m[1])
		if prev, ok := seen[v]; ok {
			return nil, i18n.Errorf("store.err.migrate.duplicate_number", "version", fmt.Sprintf("%04d", v), "prev", prev, "name", e.Name())
		}
		seen[v] = e.Name()
		b, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, Migration{Version: v, Name: e.Name(), SQL: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// SQLiteMigrationsDir は fsys の中の SQLite 用のマイグレーションの置き場（migrations/sqlite）。
// MySQL の NNNN_名前.sql ごとに、同じファイル名の SQLite 版をここに置く（migrations/embed.go）。
const SQLiteMigrationsDir = "sqlite"

// migrateLockPrefix は Migrate を直列化する MySQL の名前付きロックの名前の接頭辞。
//
// GET_LOCK の名前は**スキーマ単位ではなく MySQL のインスタンス全体**で共有される。固定の 'im_migrate' を使うと、
// 別々のスキーマへの migrate どうしまで 1 本の列に並ぶ。臨界区間はスキーマの構築そのもの（0001_init.sql の全体）で、
// テストは使い捨ての DB をテストごとに作る（1 回の go test ./internal/server/ だけで 80 個以上）ため、
// 同じ MySQL を使う実行が増えるほど全員の所要時間が積み上がり、待ちが 60 秒を超えると store.err.migrate.lock で落ちる。
// 直列化が必要なのは「同じスキーマへの同時の migrate」だけなので、ロックの名前をスキーマ名で修飾する。
const migrateLockPrefix = "im_migrate:"

// migrateLockName は schema への migrate を直列化するロックの名前を返す。
// MySQL の名前付きロックは 64 文字までなので、収まらないときはスキーマ名のハッシュにする。
// schema が空（DSN に DB 名が無い）ときは修飾しようがないので、従来どおりの固定名にする。
// 1.0.0 より前の記録を書き換える SQL（開発側で保管）も同じ名前（CONCAT('im_migrate:', DATABASE())）を取る。
func migrateLockName(schema string) string {
	if schema == "" {
		return "im_migrate"
	}
	if name := migrateLockPrefix + schema; len(name) <= 64 {
		return name
	}
	sum := sha256.Sum256([]byte(schema))
	return migrateLockPrefix + hex.EncodeToString(sum[:16])
}

// Migrate は未適用のマイグレーションを番号順に適用し、適用したファイル名を返す。
// MySQL は fsys 直下、SQLite は fsys の sqlite/ のファイルを使う。
// MySQL では同じスキーマに複数プロセスから同時に呼ばれても GET_LOCK で直列化する。DDL は MySQL では暗黙コミットされるため、
// 1 ファイルの途中で失敗した場合は手で戻す必要がある（失敗したファイル名をエラーに含める）。
// SQLite では 1 ファイルを 1 トランザクション（BEGIN IMMEDIATE）で適用する（DDL も巻き戻る）。
func Migrate(ctx context.Context, db *sql.DB, fsys fs.FS) ([]string, error) {
	if IsSQLite(db) {
		sub, err := fs.Sub(fsys, SQLiteMigrationsDir)
		if err != nil {
			return nil, err
		}
		return migrateSQLite(ctx, db, sub)
	}
	migs, err := LoadMigrations(fsys)
	if err != nil {
		return nil, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	var schema sql.NullString
	if err := conn.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&schema); err != nil {
		return nil, err
	}
	lock := migrateLockName(schema.String)

	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 60)", lock).Scan(&got); err != nil {
		return nil, err
	}
	if !got.Valid || got.Int64 != 1 {
		return nil, i18n.Errorf("store.err.migrate.lock")
	}
	defer conn.ExecContext(context.Background(), "DO RELEASE_LOCK(?)", lock) //nolint:errcheck

	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
  version    INT UNSIGNED NOT NULL,
  name       VARCHAR(255) NOT NULL,
  applied_at DATETIME(6)  NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin`); err != nil {
		return nil, err
	}
	applied, err := appliedMigrations(ctx, conn)
	if err != nil {
		return nil, err
	}
	if err := checkAppliedRecords(applied, migs, false); err != nil {
		return nil, err
	}

	var done []string
	for _, m := range migs {
		if _, ok := applied[m.Version]; ok {
			continue
		}
		for i, stmt := range SplitStatements(m.SQL) {
			if _, err := conn.ExecContext(ctx, stmt); err != nil {
				return done, i18n.Wrapf(err, "store.err.migrate.statement", "name", m.Name, "n", i+1)
			}
		}
		if _, err := conn.ExecContext(ctx, "INSERT INTO schema_migrations (version, name) VALUES (?, ?)", m.Version, m.Name); err != nil {
			return done, err
		}
		done = append(done, m.Name)
	}
	return done, nil
}

func migrateSQLite(ctx context.Context, db *sql.DB, fsys fs.FS) ([]string, error) {
	migs, err := LoadMigrations(fsys)
	if err != nil {
		return nil, i18n.Wrapf(err, "store.err.migrate.sqlite_read", "dir", SQLiteMigrationsDir)
	}
	if len(migs) == 0 {
		return nil, i18n.Errorf("store.err.migrate.sqlite_none", "dir", SQLiteMigrationsDir)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
  version    INTEGER  NOT NULL PRIMARY KEY,
  name       TEXT     NOT NULL,
  applied_at DATETIME NOT NULL DEFAULT `+sqliteNow+`
)`); err != nil {
		return nil, err
	}
	recorded, err := appliedMigrations(ctx, db)
	if err != nil {
		return nil, err
	}
	if err := checkAppliedRecords(recorded, migs, true); err != nil {
		return nil, err
	}
	var done []string
	for _, m := range migs {
		applied, err := applySQLiteMigration(ctx, db, m)
		if err != nil {
			return done, err
		}
		if applied {
			done = append(done, m.Name)
		}
	}
	return done, nil
}

// applySQLiteMigration は 1 ファイルを 1 トランザクションで適用する。別のプロセスが先に適用していれば何もしない。
func applySQLiteMigration(ctx context.Context, db *sql.DB, m Migration) (bool, error) {
	tx, err := db.BeginTx(ctx, nil) // BEGIN IMMEDIATE（sqliteDSN）で他のプロセスの適用と直列化する
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck
	var n int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version = ?", m.Version).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}
	for i, stmt := range SplitStatements(m.SQL) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return false, i18n.Wrapf(err, "store.err.migrate.sqlite_statement", "dir", SQLiteMigrationsDir, "name", m.Name, "n", i+1)
		}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (version, name) VALUES (?, ?)", m.Version, m.Name); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// querier は *sql.DB と *sql.Conn の共通部分。
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// appliedMigrations は schema_migrations の記録（version → ファイル名）を返す。
func appliedMigrations(ctx context.Context, q querier) (map[int]string, error) {
	rows, err := q.QueryContext(ctx, "SELECT version, name FROM schema_migrations")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]string{}
	for rows.Next() {
		var v int
		var name string
		if err := rows.Scan(&v, &name); err != nil {
			return nil, err
		}
		out[v] = name
	}
	return out, rows.Err()
}

// legacyMigrations は 1.0.0 より前の開発版の適用記録（version → ファイル名）。
// 1.0.0 でこれらを 0001_init.sql の 1 本に統合した（スキーマは同じ。internal/store の TestSquashedSchemaMatchesLegacy が確かめる）。
// 0001 は統合後も同じ名前なので、2 以降の記録で旧形式かを判定する。0011 は欠番。
var legacyMigrations = map[int]string{
	1:  "0001_init.sql",
	2:  "0002_totp_state.sql",
	3:  "0003_usage_snapshots.sql",
	4:  "0004_usage_reports.sql",
	5:  "0005_project_guides.sql",
	6:  "0006_usage_report_requests.sql",
	7:  "0007_agent_setup.sql",
	8:  "0008_issue_assignee.sql",
	9:  "0009_agent_install_kit.sql",
	10: "0010_two_factor_policy.sql",
	12: "0012_oauth_refresh_tokens.sql",
	13: "0013_agent_install_client.sql",
}

// checkAppliedRecords は、適用記録がこのバイナリのマイグレーションと食い違っていないかを確かめる。
//   - 1.0.0 より前の記録（0002〜0013 の旧ファイル名）が残っていれば拒否する。記録の書き換えは自動では行わず、
//     書き換えを求める。旧記録がそろっていない（途中まで適用した）DB は、書き換えても
//     スキーマが足りないため、1.0.0 より前の版のサーバで最後まで適用するよう求める。
//   - 同じ番号で名前の違う記録があれば拒否する（別の版のマイグレーションを当てた DB）。
func checkAppliedRecords(applied map[int]string, migs []Migration, sqlite bool) error {
	legacy, missing := 0, []string{}
	for v, name := range legacyMigrations {
		if v == 1 {
			continue
		}
		if applied[v] == name {
			legacy++
		} else {
			missing = append(missing, name)
		}
	}
	if legacy > 0 {
		sort.Strings(missing)
		if len(missing) == 0 && applied[1] == legacyMigrations[1] {
			// 記録を書き換える手順は MySQL と SQLite で違うので、ID を分ける
			if sqlite {
				return i18n.Errorf("store.err.migrate.legacy_records_sqlite", "count", len(legacyMigrations))
			}
			return i18n.Errorf("store.err.migrate.legacy_records", "count", len(legacyMigrations))
		}
		if sqlite {
			return i18n.Errorf("store.err.migrate.legacy_partial_sqlite", "missing", strings.Join(missing, " "))
		}
		return i18n.Errorf("store.err.migrate.legacy_partial", "missing", strings.Join(missing, " "))
	}
	for _, m := range migs {
		if name, ok := applied[m.Version]; ok && name != m.Name {
			return i18n.Errorf("store.err.migrate.record_mismatch", "version", fmt.Sprintf("%04d", m.Version), "applied", name, "name", m.Name)
		}
	}
	return nil
}

// SplitStatements は SQL ファイルを文に分ける。行頭の -- コメント行を除き、行末の ; で区切る。
// （ストアドプロシージャ等の ; を含む本文は使わない前提）
func SplitStatements(src string) []string {
	var out []string
	var cur strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		cur.WriteString(line)
		cur.WriteString("\n")
		if strings.HasSuffix(strings.TrimRight(line, " \t\r"), ";") {
			if s := strings.TrimSuffix(strings.TrimSpace(cur.String()), ";"); s != "" {
				out = append(out, s)
			}
			cur.Reset()
		}
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		out = append(out, s)
	}
	return out
}
