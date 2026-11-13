package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/migrations"
)

// checkTimelineTargetsMigration は、0019 まで適用して投稿の行を入れた DB に 0020 を当てると、投稿の行と外部キーを保ったまま
// 宛先の種類 issue・branch・machine・others と宛先の列（to_issue_id・to_label）を受けることを確かめる。
// 対照として、0020 の前は issue の宛先が CHECK で入らず、0020 の後も CHECK に無い宛先は入らない。
func checkTimelineTargetsMigration(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	if _, err := Migrate(ctx, db, migrationsBefore(t, "0020")); err != nil {
		t.Fatal(err)
	}
	pid, err := CreateProject(ctx, db, Project{Slug: "tt", Prefix: "TT", Width: 4, Name: "tt"})
	if err != nil {
		t.Fatal(err)
	}
	uid, err := CreateUser(ctx, db, "tt", "tt", "x", "member")
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) error {
		_, err := db.ExecContext(ctx, q, args...)
		return err
	}
	if err := exec("INSERT INTO timeline_posts (id, project_id, kind, to_kind, sender_user_id, via) VALUES (7, ?, 'hold', 'project', ?, 'cli')", pid, uid); err != nil {
		t.Fatal(err)
	}
	if err := exec("INSERT INTO timeline_post_events (post_id, project_id, seq, action, version, body, actor_user_id, via) VALUES (7, ?, 1, 'post', 1, '待機', ?, 'cli')", pid, uid); err != nil {
		t.Fatal(err)
	}
	if err := exec("INSERT INTO timeline_post_receipts (post_id, user_id, session_id, version, kind) VALUES (7, ?, 's1', 1, 'receive')", uid); err != nil {
		t.Fatal(err)
	}
	if err := exec("INSERT INTO timeline_posts (project_id, kind, to_kind, sender_user_id, via) VALUES (?, 'hold', 'others', ?, 'cli')", pid, uid); err == nil {
		t.Fatal("0020 の前に others の宛先が入りました（前提が崩れています）")
	}
	applied, err := Migrate(ctx, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) == 0 || !strings.HasPrefix(applied[0], "0020_") {
		t.Fatalf("0020 が適用されていない: %v", applied)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM timeline_posts WHERE id = 7 AND to_kind = 'project' AND to_label = '' AND to_issue_id IS NULL)
  + (SELECT COUNT(*) FROM timeline_post_events WHERE post_id = 7) + (SELECT COUNT(*) FROM timeline_post_receipts WHERE post_id = 7)`).Scan(&n); err != nil || n != 3 {
		t.Errorf("投稿の行が残っていない: %d %v", n, err)
	}
	res, err := db.ExecContext(ctx, "INSERT INTO issues (project_id, number, display_id, file_name, front_keys, body_main) VALUES (?, 1, 'TT-0001', 'TT-0001-x.md', '[]', '')", pid)
	if err != nil {
		t.Fatal(err)
	}
	iid, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO timeline_posts (project_id, kind, to_kind, to_issue_id, sender_user_id, via) VALUES (?, 'hold', 'issue', ?, ?, 'cli')", []any{pid, iid, uid}},
		{"INSERT INTO timeline_posts (project_id, kind, to_kind, to_label, sender_user_id, via) VALUES (?, 'hold', 'branch', 'feat-x', ?, 'cli')", []any{pid, uid}},
		{"INSERT INTO timeline_posts (project_id, kind, to_kind, to_label, sender_user_id, via) VALUES (?, 'request', 'machine', 'box-a', ?, 'cli')", []any{pid, uid}},
		{"INSERT INTO timeline_posts (project_id, kind, to_kind, sender_user_id, via) VALUES (?, 'hold', 'others', ?, 'cli')", []any{pid, uid}},
	} {
		if err := exec(q.sql, q.args...); err != nil {
			t.Errorf("新しい宛先が入りません: %s: %v", q.sql, err)
		}
	}
	if err := exec("INSERT INTO timeline_posts (project_id, kind, to_kind, sender_user_id, via) VALUES (?, 'hold', 'bogus', ?, 'cli')", pid, uid); err == nil {
		t.Error("CHECK に無い宛先が入りました")
	}
	if err := exec("INSERT INTO timeline_posts (project_id, kind, to_kind, to_issue_id, sender_user_id, via) VALUES (?, 'hold', 'issue', 999999, ?, 'cli')", pid, uid); err == nil {
		t.Error("無いイシューを指す宛先が入りました（外部キーが無い）")
	}
	// 子の外部キーが作り直した親を指している（SQLite は表を作り直す）
	if err := exec("INSERT INTO timeline_post_receipts (post_id, user_id, session_id, version, kind) VALUES (7, ?, 's2', 1, 'receive')", uid); err != nil {
		t.Errorf("正しい post_id の受け取りが入りません: %v", err)
	}
	if err := exec("INSERT INTO timeline_post_events (post_id, project_id, seq, action, version, body, actor_user_id, via) VALUES (999, ?, 2, 'post', 1, 'x', ?, 'cli')", pid, uid); err == nil {
		t.Error("無い投稿を指す出来事が入りました（外部キーが切れています）")
	}
	if again, err := Migrate(ctx, db, migrations.FS); err != nil || len(again) != 0 {
		t.Errorf("2 回目: applied=%v err=%v", again, err)
	}
}

func TestTimelineTargetsMigrationSQLite(t *testing.T) {
	db, _ := sqliteTestDB(t)
	checkTimelineTargetsMigration(t, db)
	for _, q := range []string{"UPDATE timeline_posts SET kind = 'info'", "DELETE FROM timeline_post_events", "DELETE FROM timeline_post_receipts"} {
		if _, err := db.Exec(q); err == nil {
			t.Errorf("追記専用の表を変えられました: %s", q)
		}
	}
	for _, name := range []string{"k_timeline_posts_project", "k_timeline_posts_to_user", "k_timeline_posts_sender", "k_timeline_post_receipts_user"} {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?", name).Scan(&n); err != nil || n != 1 {
			t.Errorf("索引 %s: %d %v", name, n, err)
		}
	}
}

func TestTimelineTargetsMigrationMySQL(t *testing.T) {
	db, _, _ := testDB(t)
	checkTimelineTargetsMigration(t, db)
}
