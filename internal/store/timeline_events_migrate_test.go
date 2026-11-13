package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

// checkTimelineEventsMigration は、0018 まで適用して投稿の行を入れた DB に 0019 を当てると、投稿の行と外部キーを保ったまま
// 種類 update を受け、出来事の表と読んだ位置の表ができ、既にあるプロジェクトに通し番号の行ができることを確かめる。
func checkTimelineEventsMigration(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	if _, err := Migrate(ctx, db, migrationsBefore(t, "0019")); err != nil {
		t.Fatal(err)
	}
	pid, err := CreateProject(ctx, db, Project{Slug: "te", Prefix: "TE", Width: 4, Name: "te"})
	if err != nil {
		t.Fatal(err)
	}
	uid, err := CreateUser(ctx, db, "te", "te", "x", "member")
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) error {
		_, err := db.ExecContext(ctx, q, args...)
		return err
	}
	for _, q := range []string{
		"INSERT INTO timeline_posts (id, project_id, kind, to_kind, sender_user_id, via) VALUES (7, ?, 'hold', 'project', ?, 'cli')",
		"INSERT INTO timeline_post_events (post_id, project_id, seq, action, version, body, actor_user_id, via) VALUES (7, ?, 1, 'post', 1, '待機', ?, 'cli')",
	} {
		if err := exec(q, pid, uid); err != nil {
			t.Fatalf("0018 の表への挿入: %v", err)
		}
	}
	if err := exec("INSERT INTO timeline_post_receipts (post_id, user_id, session_id, version, kind) VALUES (7, ?, 's1', 1, 'receive')", uid); err != nil {
		t.Fatal(err)
	}
	if err := exec("INSERT INTO timeline_posts (project_id, kind, to_kind, sender_user_id, via) VALUES (?, 'update', 'project', ?, 'cli')", pid, uid); err == nil {
		t.Fatal("0019 の前に update が入りました（前提が崩れています）")
	}
	applied, err := Migrate(ctx, db, migrationsBefore(t, "0020"))
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || !strings.HasPrefix(applied[0], "0019_") {
		t.Fatalf("適用されたのは 0019 だけのはず: %v", applied)
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	if n := count("SELECT COUNT(*) FROM timeline_posts WHERE id = 7 AND kind = 'hold'") +
		count("SELECT COUNT(*) FROM timeline_post_events WHERE post_id = 7 AND body = '待機'") +
		count("SELECT COUNT(*) FROM timeline_post_receipts WHERE post_id = 7"); n != 3 {
		t.Errorf("投稿の行が残っていない: %d", n)
	}
	if n := count("SELECT COUNT(*) FROM timeline_heads WHERE project_id = ? AND seq = 0", pid); n != 1 {
		t.Errorf("既にあるプロジェクトの通し番号の行: %d", n)
	}
	if err := exec("INSERT INTO timeline_posts (project_id, kind, to_kind, sender_user_id, via) VALUES (?, 'update', 'project', ?, 'cli')", pid, uid); err != nil {
		t.Errorf("update が入りません: %v", err)
	}
	if err := exec("INSERT INTO timeline_posts (project_id, kind, to_kind, sender_user_id, via) VALUES (?, 'bogus', 'project', ?, 'cli')", pid, uid); err == nil {
		t.Error("CHECK に無い種類が入りました")
	}
	// 子の外部キーが作り直した親を指している（肯定と否定）
	if err := exec("INSERT INTO timeline_post_receipts (post_id, user_id, session_id, version, kind) VALUES (7, ?, 's2', 1, 'receive')", uid); err != nil {
		t.Errorf("正しい post_id の受け取りが入りません: %v", err)
	}
	if err := exec("INSERT INTO timeline_post_events (post_id, project_id, seq, action, version, body, actor_user_id, via) VALUES (999, ?, 2, 'post', 1, 'x', ?, 'cli')", pid, uid); err == nil {
		t.Error("無い投稿を指す出来事が入りました（外部キーが切れています）")
	}
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	if err := InsertTimelineEvent(ctx, db, TimelineEvent{ProjectID: pid, Seq: 2, Kind: TimelineEventCommit, ActorUserID: uid, Via: "cli", CreatedAt: now,
		Detail: map[string]any{"sha": "abc1234"}}); err != nil {
		t.Errorf("出来事が入りません: %v", err)
	}
	if err := InsertTimelineEvent(ctx, db, TimelineEvent{ProjectID: pid, Seq: 2, Kind: TimelineEventCommit, ActorUserID: uid, Via: "cli", CreatedAt: now}); err == nil {
		t.Error("同じ通し番号の出来事が 2 つ入りました")
	}
	if err := exec("INSERT INTO timeline_reads (project_id, user_id, session_id, seq) VALUES (?, ?, 's1', 2)", pid, uid); err != nil {
		t.Errorf("読んだ位置が入りません: %v", err)
	}
	if again, err := Migrate(ctx, db, migrationsBefore(t, "0020")); err != nil || len(again) != 0 {
		t.Errorf("2 回目: applied=%v err=%v", again, err)
	}
}

func TestTimelineEventsMigrationSQLite(t *testing.T) {
	db, _ := sqliteTestDB(t)
	checkTimelineEventsMigration(t, db)
	// 追記専用のトリガが新しい表と作り直した表に効いている（対照: 上の追記は通っている）
	for _, q := range []string{"UPDATE timeline_events SET kind = 'commit'", "DELETE FROM timeline_events", "UPDATE timeline_reads SET seq = 9",
		"DELETE FROM timeline_reads", "UPDATE timeline_posts SET kind = 'info'", "DELETE FROM timeline_post_events", "DELETE FROM timeline_post_receipts"} {
		if _, err := db.Exec(q); err == nil {
			t.Errorf("追記専用の表を変えられました: %s", q)
		}
	}
	// 索引が作り直されている
	for _, name := range []string{"k_timeline_posts_project", "k_timeline_posts_to_user", "k_timeline_posts_sender", "k_timeline_post_receipts_user",
		"k_timeline_events_created", "k_timeline_reads_session"} {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?", name).Scan(&n); err != nil || n != 1 {
			t.Errorf("索引 %s: %d %v", name, n, err)
		}
	}
}

func TestTimelineEventsMigrationMySQL(t *testing.T) {
	db, _, _ := testDB(t)
	checkTimelineEventsMigration(t, db)
}
