package store

import (
	"context"
	"database/sql"
	"io/fs"
	"path"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/howashoji/looptrack/migrations"
)

// migrationsUpTo17 は 0017 までのマイグレーション（MySQL と SQLite の両方）だけを持つファイルの集まりを返す。
func migrationsUpTo17(t *testing.T) fs.FS {
	t.Helper()
	out := fstest.MapFS{}
	err := fs.WalkDir(migrations.FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".sql") || path.Base(p) >= "0018" {
			return err
		}
		b, err := fs.ReadFile(migrations.FS, p)
		if err != nil {
			return err
		}
		out[p] = &fstest.MapFile{Data: b}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// checkTimelineRename は、0017 まで適用して行を入れた DB に 0018 を当てると、行を保ったまま表と列の名前が変わることを確かめる。
// exists は表の有無を返す（方言ごとに違う）。
// indexes は表の索引の名前の一覧を返す（方言ごとに違う）。
func checkTimelineRename(t *testing.T, db *sql.DB, exists func(name string) bool, indexes func(table string) []string, namedUnique bool) {
	t.Helper()
	ctx := context.Background()
	if _, err := Migrate(ctx, db, migrationsUpTo17(t)); err != nil {
		t.Fatal(err)
	}
	for _, old := range []string{"bulletins", "bulletin_events", "bulletin_receipts", "bulletin_heads"} {
		if !exists(old) {
			t.Fatalf("0017 までの DB に %s がありません（前提が崩れています）", old)
		}
	}
	pid, err := CreateProject(ctx, db, Project{Slug: "rn", Prefix: "RN", Width: 4, Name: "rn"})
	if err != nil {
		t.Fatal(err)
	}
	uid, err := CreateUser(ctx, db, "rn", "rn", "x", "member")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO bulletins (id, project_id, kind, to_kind, sender_user_id, via) VALUES (7, ?, 'hold', 'project', ?, 'cli')", []any{pid, uid}},
		{"INSERT INTO bulletin_events (bulletin_id, project_id, seq, action, version, body, actor_user_id, via) VALUES (7, ?, 1, 'post', 1, '待機', ?, 'cli')", []any{pid, uid}},
		{"INSERT INTO bulletin_receipts (bulletin_id, user_id, session_id, version, kind) VALUES (7, ?, 's1', 1, 'receive')", []any{uid}},
		{"INSERT INTO bulletin_heads (project_id, seq) VALUES (?, 1)", []any{pid}},
	} {
		if _, err := db.ExecContext(ctx, q.sql, q.args...); err != nil {
			t.Fatalf("0017 の表への行の挿入: %v\n%s", err, q.sql)
		}
	}

	applied, err := Migrate(ctx, db, migrationsBefore(t, "0019"))
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || !strings.HasPrefix(applied[0], "0018_") {
		t.Fatalf("適用されたのは 0018 だけのはず: %v", applied)
	}
	for _, old := range []string{"bulletins", "bulletin_events", "bulletin_receipts", "bulletin_heads"} {
		if exists(old) {
			t.Errorf("%s が残っています", old)
		}
	}
	for _, name := range []string{"timeline_posts", "timeline_post_events", "timeline_post_receipts", "timeline_heads"} {
		if !exists(name) {
			t.Errorf("%s がありません", name)
		}
	}
	count := func(q string) int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	if n := count("SELECT COUNT(*) FROM timeline_posts WHERE id = 7 AND kind = 'hold'"); n != 1 {
		t.Errorf("timeline_posts の行 = %d, want 1", n)
	}
	if n := count("SELECT COUNT(*) FROM timeline_post_events WHERE post_id = 7 AND seq = 1 AND body = '待機'"); n != 1 {
		t.Errorf("timeline_post_events の行 = %d, want 1", n)
	}
	if n := count("SELECT COUNT(*) FROM timeline_post_receipts WHERE post_id = 7 AND session_id = 's1'"); n != 1 {
		t.Errorf("timeline_post_receipts の行 = %d, want 1", n)
	}
	if n := count("SELECT COUNT(*) FROM timeline_heads WHERE seq = 1"); n != 1 {
		t.Errorf("timeline_heads の行 = %d, want 1", n)
	}
	// 外部キーが新しい名前の表を指している（肯定: 正しい post_id なら入る / 否定: 無い投稿を指す子の行は入らない）
	if _, err := db.ExecContext(ctx, "INSERT INTO timeline_post_receipts (post_id, user_id, session_id, version, kind) VALUES (7, ?, 's2', 1, 'receive')", uid); err != nil {
		t.Errorf("正しい post_id の受け取りが入りません: %v", err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO timeline_post_receipts (post_id, user_id, session_id, version, kind) VALUES (999, ?, 's1', 1, 'receive')", uid); err == nil {
		t.Error("無い投稿を指す受け取りが入りました（外部キーが切れています）")
	}
	// 索引の名前が 0018 で付けたものになっている
	for table, want := range map[string][]string{
		"timeline_posts":         {"k_timeline_posts_project", "k_timeline_posts_to_user", "k_timeline_posts_sender"},
		"timeline_post_events":   {"uk_timeline_post_events_seq", "uk_timeline_post_events_version"},
		"timeline_post_receipts": {"k_timeline_post_receipts_user"},
	} {
		got := strings.Join(indexes(table), " ")
		for _, name := range want {
			if strings.HasPrefix(name, "uk_") && !namedUnique {
				continue // SQLite の UNIQUE 制約の索引は名前を持たない
			}
			if !strings.Contains(" "+got+" ", " "+name+" ") {
				t.Errorf("%s に索引 %s がありません（ある索引: %s）", table, name, got)
			}
		}
		if strings.Contains(got, "bulletin") {
			t.Errorf("%s に旧名の索引が残っています: %s", table, got)
		}
	}
	// 2 回目は何も適用されない
	if again, err := Migrate(ctx, db, migrationsBefore(t, "0019")); err != nil || len(again) != 0 {
		t.Errorf("2 回目: applied=%v err=%v", again, err)
	}
}

func TestTimelineRenameKeepsRowsSQLite(t *testing.T) {
	db, _ := sqliteTestDB(t)
	checkTimelineRename(t, db, func(name string) bool {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}, func(table string) []string {
		rows, err := db.Query("SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = ? AND name NOT LIKE 'sqlite_%'", table)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				t.Fatal(err)
			}
			out = append(out, n)
		}
		return out
	}, false)
	// 追記専用のトリガが新しい表にも効いている
	if _, err := db.Exec("UPDATE timeline_posts SET kind = 'info' WHERE id = 7"); err == nil {
		t.Error("timeline_posts を書き換えられました（追記専用のトリガが消えています）")
	}
	if _, err := db.Exec("DELETE FROM timeline_post_events"); err == nil {
		t.Error("timeline_post_events を削除できました")
	}
	if _, err := db.Exec("UPDATE timeline_post_receipts SET version = 2"); err == nil {
		t.Error("timeline_post_receipts を書き換えられました")
	}
	if _, err := db.Exec("DELETE FROM timeline_post_receipts"); err == nil {
		t.Error("timeline_post_receipts を削除できました")
	}
	// 対照: 追記（INSERT）は通る
	if _, err := db.Exec("INSERT INTO timeline_post_receipts (post_id, user_id, session_id, version, kind) SELECT post_id, user_id, 's3', 1, 'ack' FROM timeline_post_receipts LIMIT 1"); err != nil {
		t.Errorf("追記が拒まれました: %v", err)
	}
}

func TestTimelineRenameKeepsRowsMySQL(t *testing.T) {
	db, _, _ := testDB(t)
	checkTimelineRename(t, db, func(name string) bool {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?", name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}, func(table string) []string {
		// 外部キーの制約名は 0018 で付け替えない。MySQL が外部キーのために作る索引はその制約名を名乗るので、
		// 制約名と同じ名前の索引は旧名の検査から外す。
		rows, err := db.Query(`SELECT DISTINCT s.index_name FROM information_schema.statistics s
			WHERE s.table_schema = DATABASE() AND s.table_name = ?
			AND s.index_name NOT IN (SELECT c.constraint_name FROM information_schema.table_constraints c
				WHERE c.table_schema = DATABASE() AND c.table_name = s.table_name AND c.constraint_type = 'FOREIGN KEY')`, table)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				t.Fatal(err)
			}
			out = append(out, n)
		}
		return out
	}, true)
}
