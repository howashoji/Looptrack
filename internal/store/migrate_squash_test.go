package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/mdformat"
	"github.com/howashoji/looptrack/migrations"
)

// 1.0.0 でマイグレーションを 0001_init.sql の 1 本に統合した。
// 統合前の 0001〜0013（0011 は欠番）は testdata/legacy/（SQLite 版は testdata/legacy/sqlite/）に、
// テストの材料としてだけ置く（バイナリには埋め込まない）。SQL の -- コメントは除いてある。列の COMMENT は
// スキーマの一部なので当時のまま（統合後に書き直した列の説明は squashCommentRewrites に並べる）。
const legacyDir = "testdata/legacy"

func legacyFS(t *testing.T) fs.FS {
	t.Helper()
	fsys := os.DirFS(legacyDir)
	migs, err := LoadMigrations(fsys)
	if err != nil || len(migs) != len(legacyMigrations) {
		t.Fatalf("旧マイグレーション: %d 本 %v, want %d 本", len(migs), err, len(legacyMigrations))
	}
	for _, m := range migs {
		if legacyMigrations[m.Version] != m.Name {
			t.Fatalf("旧マイグレーション %s が legacyMigrations（migrate.go）に無い", m.Name)
		}
	}
	return fsys
}

// migrationsUpTo は fsys のマイグレーションのうち version 以下だけを返す（SQLite 版も同じ番号まで）。
// 途中まで適用した DB を作るのと、統合後の 0001 だけを当てた DB を作る（0002 以降は当てない）のに使う。
func migrationsUpTo(t *testing.T, fsys fs.FS, version int) fs.FS {
	t.Helper()
	migs, err := LoadMigrations(fsys)
	if err != nil {
		t.Fatal(err)
	}
	out := fstest.MapFS{}
	for _, m := range migs {
		if m.Version <= version {
			out[m.Name] = &fstest.MapFile{Data: []byte(m.SQL)}
		}
	}
	if sub, err := fs.Sub(fsys, SQLiteMigrationsDir); err == nil {
		if lite, err := LoadMigrations(sub); err == nil {
			for _, m := range lite {
				if m.Version <= version {
					out[SQLiteMigrationsDir+"/"+m.Name] = &fstest.MapFile{Data: []byte(m.SQL)}
				}
			}
		}
	}
	return out
}

// afterSquash は統合後の 0001 より後のマイグレーション（0002…）の名前。足すたびに増えるので、
// 「書き換えの後の migrate が当てるもの」はここから作る（本数を数字で書かない）。
func afterSquash(t *testing.T) []string {
	t.Helper()
	migs, err := LoadMigrations(migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, m := range migs {
		if m.Version > 1 {
			out = append(out, m.Name)
		}
	}
	return out
}

// squashCommentRewrites は、統合で書き直した列の説明（旧 → 新）。表ごと。
// 本番の列の COMMENT は旧のまま残る（説明だけの違いで、型・既定値・索引は同じ）。ここに無い違いはテストが失敗する。
var squashCommentRewrites = map[string][][2]string{
	"projects": {
		{"COMMENT '最後に採番した番号（旧 counter ファイル）'", "COMMENT '最後に採番した番号'"},
		{"COMMENT 'プロジェクト別ルール（DESIGN.md §5）'", "COMMENT 'プロジェクト別ルール'"},
	},
	"usage_reports": {
		{"COMMENT '画面からの作成依頼（IM-0062）の ID'", "COMMENT '画面からの作成依頼（usage_report_requests）の ID'"}, // 旧スキーマの文字列は変えられない（testdata/legacy と一致させる）
	},
	"project_guides": {
		{"COMMENT '登録元（例: docs/projects/app.md）'", "COMMENT '登録元（例: docs/projects/<slug>.md）'"},
	},
}

// mysqlSchema は全表の SHOW CREATE TABLE を返す（schema_migrations を含む）。
func mysqlSchema(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query("SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE()")
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	rows.Close()
	out := map[string]string{}
	for _, tbl := range tables {
		var name, ddl string
		if err := db.QueryRow("SHOW CREATE TABLE `"+tbl+"`").Scan(&name, &ddl); err != nil {
			t.Fatal(err)
		}
		out[tbl] = ddl
	}
	return out
}

// tableRows は表の全行を文字列にして返す（初期データの比較用。schema_migrations は applied_at が違うので呼ばない）。
func tableRows(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query("SELECT * FROM `" + table + "`")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out []string
	for rows.Next() {
		vals := make([]sql.RawBytes, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		var parts []string
		for i, v := range vals {
			parts = append(parts, fmt.Sprintf("%s=%q", cols[i], v))
		}
		out = append(out, strings.Join(parts, " "))
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// (a) 旧 0001〜0013 を順に当てた DB と (b) 統合後の 0001 だけを当てた DB（0002 以降は当てない。統合が正しいかの比較なので）で、全表の SHOW CREATE TABLE
// （列順・型・照合順序・既定値・索引・外部キー・CHECK）と初期データが一致すること。
func TestSquashedSchemaMatchesLegacy(t *testing.T) {
	ctx := context.Background()
	legacyDB, _, _ := testDB(t)
	squashedDB, _, _ := testDB(t)
	if _, err := Migrate(ctx, legacyDB, legacyFS(t)); err != nil {
		t.Fatalf("旧マイグレーション: %v", err)
	}
	if _, err := Migrate(ctx, squashedDB, migrationsUpTo(t, migrations.FS, 1)); err != nil {
		t.Fatalf("統合後のマイグレーション: %v", err)
	}
	a, b := mysqlSchema(t, legacyDB), mysqlSchema(t, squashedDB)
	for tbl, rewrites := range squashCommentRewrites {
		for _, r := range rewrites {
			if !strings.Contains(a[tbl], r[0]) {
				t.Errorf("%s: 書き直し前の説明 %s が旧スキーマに無い（squashCommentRewrites を直す）", tbl, r[0])
			}
			a[tbl] = strings.Replace(a[tbl], r[0], r[1], 1)
		}
	}
	if ka, kb := strings.Join(sortedKeys(a), " "), strings.Join(sortedKeys(b), " "); ka != kb {
		t.Fatalf("表の構成が違います\n旧:   %s\n統合: %s", ka, kb)
	}
	if len(b) != 23 {
		t.Errorf("表の数 = %d, want 23（22 表 + schema_migrations）", len(b))
	}
	for _, tbl := range sortedKeys(a) {
		if a[tbl] != b[tbl] {
			t.Errorf("%s の定義が違います\n--- 旧 0001〜0013\n%s\n--- 統合後の 0001\n%s", tbl, a[tbl], b[tbl])
		}
		if tbl == "schema_migrations" {
			continue
		}
		if ra, rb := tableRows(t, legacyDB, tbl), tableRows(t, squashedDB, tbl); strings.Join(ra, "\n") != strings.Join(rb, "\n") {
			t.Errorf("%s の初期データが違います\n旧:   %q\n統合: %q", tbl, ra, rb)
		}
	}
}

// runSquashSQL は deploy/ の書き換え SQL を 1 本の接続で文ごとに流す（mysql / sqlite3 のクライアントで流すのと同じ）。
// 途中で失敗したら、クライアントが接続を閉じたときと同じく、確定していない変更を戻してロックを放す。
func runSquashSQL(t *testing.T, db *sql.DB, file string) ([]string, error) {
	t.Helper()
	// 書き換えの SQL は 1.0.0 より前の開発版の DB（公開前の配置）だけに要るので、公開物には入れていない
	// （private/ は git archive が外す）。公開物で回したときは、ここから先を省略する（拒否の判定までは確かめ済み）。
	src, err := os.ReadFile(filepath.Join("..", "..", "private", "deploy", file))
	if errors.Is(err, fs.ErrNotExist) {
		t.Skipf("%s が無い（公開物では private/ を含まない）", file)
	}
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var results []string
	for _, stmt := range SplitStatements(string(src)) {
		if strings.HasPrefix(strings.ToUpper(stmt), "SELECT CASE") {
			var r string
			if err := conn.QueryRowContext(ctx, stmt).Scan(&r); err != nil {
				return results, err
			}
			results = append(results, r)
			continue
		}
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			conn.ExecContext(ctx, "ROLLBACK") //nolint:errcheck
			if !IsSQLite(db) {
				conn.ExecContext(ctx, "DO RELEASE_LOCK(CONCAT('im_migrate:', DATABASE()))") //nolint:errcheck
			}
			return results, err
		}
	}
	return results, nil
}

func migrationRecords(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query("SELECT version, name FROM schema_migrations ORDER BY version")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v int
		var name string
		if err := rows.Scan(&v, &name); err != nil {
			t.Fatal(err)
		}
		out = append(out, name)
	}
	return strings.Join(out, " ")
}

// seedLegacyData は旧 0001〜0013 を当てた DB に、利用者・プロジェクト・イシュー（コメント・担当者つき）・運用文書・記録を入れる。
func seedLegacyData(t *testing.T, db *sql.DB) (issueID int64) {
	t.Helper()
	ctx := context.Background()
	uid, err := CreateUser(ctx, db, "alice", "Alice", "hash", "admin")
	if err != nil {
		t.Fatal(err)
	}
	pid, err := CreateProject(ctx, db, Project{Slug: "req", Prefix: "REQ", Width: 4, Name: "要件"})
	if err != nil {
		t.Fatal(err)
	}
	doc := &mdformat.Document{
		Front:             []mdformat.Field{{Key: "id", Value: "REQ-0001"}, {Key: "title", Value: "統合前のイシュー"}, {Key: "status", Value: "In Progress"}},
		BodyMain:          "本文",
		GapNL:             2,
		HasCommentSection: true,
		Comments:          []mdformat.Comment{{TS: "2026-09-19 10:00", Content: "コメント"}},
		TrailNL:           1,
	}
	issueID, err = InsertDocument(ctx, db, pid, 1, "REQ-0001-x.md", doc, "cli")
	if err != nil {
		t.Fatal(err)
	}
	if err := SetAssignee(ctx, db, issueID, uid); err != nil {
		t.Fatal(err)
	}
	if err := SetProjectGuide(ctx, db, pid, "# 運用", "docs/projects/req.md"); err != nil {
		t.Fatal(err)
	}
	if err := InsertEvent(ctx, db, Event{ProjectID: pid, IssueID: issueID, Kind: "status", Author: Author{UserID: uid, Via: "cli", At: time.Now()}, Detail: map[string]string{"to": "In Progress"}}); err != nil {
		t.Fatal(err)
	}
	return issueID
}

// checkSeededData は seedLegacyData の内容が新しいサーバ（統合後のマイグレーション）で読めることを確かめる。
func checkSeededData(t *testing.T, db *sql.DB, issueID int64) {
	t.Helper()
	ctx := context.Background()
	u, err := UserByLogin(ctx, db, "alice")
	if err != nil || u.DisplayName != "Alice" {
		t.Errorf("利用者: %+v %v", u, err)
	}
	p, err := ProjectBySlug(ctx, db, "req")
	if err != nil {
		t.Fatal(err)
	}
	docs, err := LoadDocuments(ctx, db, p.ID)
	if err != nil || len(docs) != 1 {
		t.Fatalf("イシュー: %d 件 %v", len(docs), err)
	}
	d := docs[0].Doc
	if d.BodyMain != "本文" || len(d.Comments) != 1 || d.Comments[0].Content != "コメント" || docs[0].Assignee.Login != "alice" {
		t.Errorf("イシュー: %+v 担当 %+v", d, docs[0].Assignee)
	}
	if g, err := GetProjectGuide(ctx, db, p.ID); err != nil || g.Content != "# 運用" {
		t.Errorf("運用文書: %+v %v", g, err)
	}
	acts, err := IssueActivity(ctx, db, []string{"REQ-0001"}, time.Time{})
	if err != nil || len(acts) == 0 {
		t.Errorf("記録: %+v %v", acts, err)
	}
	_ = issueID
}

// 旧 0001〜0013 を適用済みの DB（本番）を 1.0.0 にする:
//   - 書き換える前に新しいサーバの migrate が来たら、何も変えずに手順を示して拒否する
//   - 書き換えの SQL（private/deploy/squash-1.0.0.sql）で記録を 0001 の 1 行にする。もう一度流しても何もしない（冪等）
//   - 書き換えた後の migrate は 0001 より後（0002…）だけを当てる。既存のデータがそのまま読める
func TestSquashUpgradesLegacyMySQL(t *testing.T) {
	ctx := context.Background()
	db, _, _ := testDB(t)
	if _, err := Migrate(ctx, db, legacyFS(t)); err != nil {
		t.Fatal(err)
	}
	issueID := seedLegacyData(t, db)
	before := migrationRecords(t, db)

	_, err := Migrate(ctx, db, migrations.FS)
	if msg := i18n.Text(i18n.JA, err); err == nil || !strings.Contains(msg, "0001_init.sql の 1 行") || !strings.Contains(msg, "1.0.0 より前の形式") || strings.Contains(msg, "sqlite3") {
		t.Fatalf("書き換え前の migrate: err = %v, want 手順を示して拒否", err)
	}
	if got := migrationRecords(t, db); got != before {
		t.Fatalf("拒否したのに記録が変わった: %s", got)
	}

	var appliedAt string
	db.QueryRow("SELECT applied_at FROM schema_migrations WHERE version = 1").Scan(&appliedAt)
	res, err := runSquashSQL(t, db, "squash-1.0.0.sql")
	if err != nil || len(res) != 1 || !strings.HasPrefix(res[0], "書き換えました") {
		t.Fatalf("書き換え: %q %v", res, err)
	}
	if got := migrationRecords(t, db); got != "0001_init.sql" {
		t.Fatalf("書き換え後の記録 = %s", got)
	}
	var appliedAfter string
	db.QueryRow("SELECT applied_at FROM schema_migrations WHERE version = 1").Scan(&appliedAfter)
	if appliedAfter != appliedAt {
		t.Errorf("0001 の applied_at が変わった: %s → %s", appliedAt, appliedAfter)
	}
	res, err = runSquashSQL(t, db, "squash-1.0.0.sql")
	if err != nil || len(res) != 1 || !strings.HasPrefix(res[0], "書き換え済み") {
		t.Fatalf("2 回目の書き換え: %q %v（冪等でない）", res, err)
	}

	applied, err := Migrate(ctx, db, migrations.FS)
	if want := afterSquash(t); err != nil || strings.Join(applied, ",") != strings.Join(want, ",") {
		t.Fatalf("書き換え後の migrate: applied=%v err=%v, want %v（0001 より後だけ）", applied, err, want)
	}
	checkSeededData(t, db, issueID)

	// ロックを放していること（looptrack migrate と同じロック）。ロックの名前はスキーマ名で修飾されている
	// （migrateLockName）ので、この使い捨て DB の名前のロックを誰かが持っていれば、それは放し忘れである
	// （並行する他のパッケージの migrate は別の DB 名なので、名前が衝突しない）。
	var schema string
	if err := db.QueryRow("SELECT DATABASE()").Scan(&schema); err != nil {
		t.Fatalf("DATABASE(): %v", err)
	}
	var holder sql.NullInt64
	if err := db.QueryRow("SELECT IS_USED_LOCK(?)", migrateLockName(schema)).Scan(&holder); err != nil {
		t.Fatalf("IS_USED_LOCK: %v", err)
	}
	if holder.Valid {
		t.Errorf("ロックが残っている（接続 %d が %q を持っている）", holder.Int64, migrateLockName(schema))
	}
}

// 旧マイグレーションを途中まで（0007 まで）当てた DB: 書き換え SQL も新しい migrate も何も変えずに拒否する。
func TestSquashRefusesPartialLegacyMySQL(t *testing.T) {
	ctx := context.Background()
	db, _, _ := testDB(t)
	if _, err := Migrate(ctx, db, migrationsUpTo(t, legacyFS(t), 7)); err != nil {
		t.Fatal(err)
	}
	before := migrationRecords(t, db)
	if _, err := runSquashSQL(t, db, "squash-1.0.0.sql"); err == nil || !strings.Contains(err.Error(), "中止") {
		t.Fatalf("書き換え: err = %v, want 中止", err)
	}
	if got := migrationRecords(t, db); got != before {
		t.Fatalf("中止したのに記録が変わった: %s", got)
	}
	if _, err := Migrate(ctx, db, migrations.FS); err == nil || !strings.Contains(i18n.Text(i18n.JA, err), "途中まで") {
		t.Fatalf("migrate: err = %v, want 途中までの拒否", err)
	}
	// 記録だけがそろっていてスキーマが足りない DB も書き換えない
	db2, _, _ := testDB(t)
	if _, err := Migrate(ctx, db2, migrationsUpTo(t, legacyFS(t), 12)); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db2, "INSERT INTO schema_migrations (version, name) VALUES (13, '0013_agent_install_client.sql')")
	if _, err := runSquashSQL(t, db2, "squash-1.0.0.sql"); err == nil || !strings.Contains(err.Error(), "中止") {
		t.Fatalf("列の無い DB の書き換え: err = %v, want 中止", err)
	}
	// 空の schema_migrations も中止
	db3, _, _ := testDB(t)
	mustExec(t, db3, "CREATE TABLE schema_migrations (version INT UNSIGNED NOT NULL PRIMARY KEY, name VARCHAR(255) NOT NULL, applied_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6))")
	if _, err := runSquashSQL(t, db3, "squash-1.0.0.sql"); err == nil || !strings.Contains(err.Error(), "中止") {
		t.Fatalf("空の記録の書き換え: err = %v, want 中止", err)
	}
}

func TestCheckAppliedRecords(t *testing.T) {
	migs := []Migration{{Version: 1, Name: "0001_init.sql"}, {Version: 2, Name: "0002_new.sql"}}
	if err := checkAppliedRecords(map[int]string{1: "0001_init.sql"}, migs, false); err != nil {
		t.Errorf("統合後の記録: %v", err)
	}
	if err := checkAppliedRecords(map[int]string{}, migs, false); err != nil {
		t.Errorf("空の DB: %v", err)
	}
	if err := checkAppliedRecords(map[int]string{1: "0001_init.sql", 2: "0002_other.sql"}, migs, false); err == nil {
		t.Error("同じ番号で名前の違う記録を受け付けた")
	}
	full := map[int]string{}
	for v, n := range legacyMigrations {
		full[v] = n
	}
	if err := checkAppliedRecords(full, migs, true); err == nil || !strings.Contains(i18n.Text(i18n.JA, err), "sqlite3 で適用記録") {
		t.Errorf("旧形式（SQLite）: %v", err)
	}
	delete(full, 13)
	if err := checkAppliedRecords(full, migs, false); err == nil || !strings.Contains(i18n.Text(i18n.JA, err), "0013_agent_install_client.sql") {
		t.Errorf("途中まで: %v", err)
	}
}

// sqliteSchema は SQLite の表ごとの構成（列・索引・外部キー・CHECK の名前）とトリガを返す。
// ALTER TABLE ADD COLUMN は列を末尾に足すため、列は名前順で比べる（MySQL 版は列順まで比べる）。
func sqliteSchema(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	q := func(query string, args ...any) []string {
		rows, err := db.Query(query, args...)
		if err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		defer rows.Close()
		cols, _ := rows.Columns()
		var out []string
		for rows.Next() {
			vals := make([]sql.NullString, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			var parts []string
			for _, v := range vals {
				parts = append(parts, fmt.Sprintf("%q", v.String))
			}
			out = append(out, strings.Join(parts, " "))
		}
		sort.Strings(out)
		return out
	}
	chk := regexp.MustCompile(`CONSTRAINT (chk_\w+) CHECK`)
	out := map[string]string{}
	for _, row := range q("SELECT name, sql FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'") {
		var name string
		fmt.Sscanf(row, "%q", &name)
		var ddl string
		db.QueryRow("SELECT sql FROM sqlite_master WHERE name = ?", name).Scan(&ddl)
		var checks []string
		for _, m := range chk.FindAllStringSubmatch(ddl, -1) {
			checks = append(checks, m[1])
		}
		sort.Strings(checks)
		var idx []string
		for _, ix := range q("SELECT name, \"unique\", origin, partial FROM pragma_index_list(?)", name) {
			var ixName string
			fmt.Sscanf(ix, "%q", &ixName)
			idx = append(idx, ix+" ("+strings.Join(q("SELECT seqno, name FROM pragma_index_info(?)", ixName), ", ")+")")
		}
		sort.Strings(idx)
		out[name] = "columns:\n  " + strings.Join(q("SELECT name, type, \"notnull\", dflt_value, pk FROM pragma_table_info(?)", name), "\n  ") +
			"\nindexes:\n  " + strings.Join(idx, "\n  ") +
			"\nforeign keys:\n  " + strings.Join(q("SELECT \"table\", \"from\", \"to\", on_update, on_delete FROM pragma_foreign_key_list(?)", name), "\n  ") +
			"\nchecks: " + strings.Join(checks, " ")
	}
	for _, row := range q("SELECT name, tbl_name, sql FROM sqlite_master WHERE type = 'trigger'") {
		var name string
		fmt.Sscanf(row, "%q", &name)
		out["trigger "+name] = row
	}
	return out
}

// SQLite 版も (a) 旧 0001〜0013 と (b) 統合後の 0001 だけ（0002 以降は当てない）で同じ構成になること。
func TestSquashedSQLiteSchemaMatchesLegacy(t *testing.T) {
	ctx := context.Background()
	legacyDB, _ := sqliteTestDB(t)
	squashedDB, _ := sqliteTestDB(t)
	if _, err := Migrate(ctx, legacyDB, legacyFS(t)); err != nil {
		t.Fatalf("旧マイグレーション: %v", err)
	}
	if _, err := Migrate(ctx, squashedDB, migrationsUpTo(t, migrations.FS, 1)); err != nil {
		t.Fatalf("統合後のマイグレーション: %v", err)
	}
	a, b := sqliteSchema(t, legacyDB), sqliteSchema(t, squashedDB)
	if ka, kb := strings.Join(sortedKeys(a), " "), strings.Join(sortedKeys(b), " "); ka != kb {
		t.Fatalf("表・トリガの構成が違います\n旧:   %s\n統合: %s", ka, kb)
	}
	for _, k := range sortedKeys(a) {
		if a[k] != b[k] {
			t.Errorf("%s が違います\n--- 旧 0001〜0013\n%s\n--- 統合後の 0001\n%s", k, a[k], b[k])
		}
		if !strings.HasPrefix(k, "trigger ") && k != "schema_migrations" {
			ra, rb := tableRows(t, legacyDB, k), tableRows(t, squashedDB, k)
			if strings.Join(ra, "\n") != strings.Join(rb, "\n") {
				t.Errorf("%s の初期データが違います\n旧:   %q\n統合: %q", k, ra, rb)
			}
		}
	}
}

// SQLite: 書き換え前は拒否・private/deploy/squash-1.0.0-sqlite.sql で書き換え（冪等）・書き換え後の migrate は 0001 より後だけ・途中までは中止。
func TestSquashUpgradesLegacySQLite(t *testing.T) {
	ctx := context.Background()
	db, _ := sqliteTestDB(t)
	if _, err := Migrate(ctx, db, legacyFS(t)); err != nil {
		t.Fatal(err)
	}
	issueID := seedLegacyData(t, db)
	if _, err := Migrate(ctx, db, migrations.FS); err == nil || !strings.Contains(i18n.Text(i18n.JA, err), "sqlite3 で適用記録") {
		t.Fatalf("書き換え前の migrate: err = %v, want 手順を示して拒否", err)
	}
	res, err := runSquashSQL(t, db, "squash-1.0.0-sqlite.sql")
	if err != nil || len(res) != 1 || !strings.HasPrefix(res[0], "書き換えました") {
		t.Fatalf("書き換え: %q %v", res, err)
	}
	if got := migrationRecords(t, db); got != "0001_init.sql" {
		t.Fatalf("書き換え後の記録 = %s", got)
	}
	if res, err := runSquashSQL(t, db, "squash-1.0.0-sqlite.sql"); err != nil || len(res) != 1 || !strings.HasPrefix(res[0], "書き換え済み") {
		t.Fatalf("2 回目の書き換え: %q %v", res, err)
	}
	if applied, err := Migrate(ctx, db, migrations.FS); err != nil || strings.Join(applied, ",") != strings.Join(afterSquash(t), ",") {
		t.Fatalf("書き換え後の migrate: applied=%v err=%v, want %v（0001 より後だけ）", applied, err, afterSquash(t))
	}
	checkSeededData(t, db, issueID)

	partial, _ := sqliteTestDB(t)
	if _, err := Migrate(ctx, partial, migrationsUpTo(t, legacyFS(t), 7)); err != nil {
		t.Fatal(err)
	}
	before := migrationRecords(t, partial)
	if _, err := runSquashSQL(t, partial, "squash-1.0.0-sqlite.sql"); err == nil || !strings.Contains(err.Error(), "中止") {
		t.Fatalf("途中までの書き換え: err = %v, want 中止", err)
	}
	if got := migrationRecords(t, partial); got != before {
		t.Fatalf("中止したのに記録が変わった: %s", got)
	}
	if _, err := Migrate(ctx, partial, migrations.FS); err == nil || !strings.Contains(i18n.Text(i18n.JA, err), "途中まで") {
		t.Fatalf("migrate: err = %v, want 途中までの拒否", err)
	}
}
