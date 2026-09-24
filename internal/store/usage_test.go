package store

import (
	"context"
	"database/sql"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/mdformat"
	"github.com/howashoji/looptrack/migrations"
)

// 付与漏れの判定に使う SQL 断片（usageHostSessionCond / usageOptInCond）と、それが UsageCoverage の
// 数え方に効くこと。SQL の文字列を照合するだけでは「SQL を直す人がテストも一緒に直せば通る」ので、
// 効き目は実際に issue_events の行を入れて、数えた結果で確かめる。

// usageTestDB は使い捨ての DB をマイグレーション済みで返す（MySQL でも SQLite でも走る）。
//
// internal/testutil は internal/store を import しているので、package store のテストからは呼べない
// （import 循環になる）。そのため testutil.Dialect と同じ選び方をここに置く。
func usageTestDB(t *testing.T) *sql.DB {
	t.Helper()
	var db *sql.DB
	switch os.Getenv("LOOPTRACK_TEST_DB") {
	case "skip", "none":
		t.Skip("LOOPTRACK_TEST_DB=skip のため DB を使うテストを省略")
	case "sqlite":
		db, _ = sqliteTestDB(t)
	case "mysql":
		db, _, _ = testDB(t) // testDB は DSN を直読みするので、LOOPTRACK_TEST_DSN が空なら SQLite に落ちずに Skip する
	default:
		if os.Getenv("LOOPTRACK_TEST_DSN") != "" {
			db, _, _ = testDB(t)
		} else {
			db, _ = sqliteTestDB(t)
		}
	}
	if _, err := Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return db
}

// TestUsageCondsNullSafety は、2 つの条件が NULL を空文字に畳んでいること（COALESCE の包み）を固定する（DB 不要）。
//
// ここが守るのは usageHostSessionCond のコメントに書かれた過去の事故だけ:
// 印の無い detail は JSON_EXTRACT が NULL を返し、NULL <> 'host' も NULL になるので、
// COALESCE を外すと印の無い操作まで丸ごと落ちる。条件そのものが正しく効くかは、
// 実データで数える TestUsageCoverageHostAndOptIn が確かめる。
func TestUsageCondsNullSafety(t *testing.T) {
	host := usageHostSessionCond()
	if !strings.Contains(host, `COALESCE(JSON_UNQUOTE(JSON_EXTRACT(e.detail, '$.session_kind')), '')`) {
		t.Errorf("usageHostSessionCond が NULL を空文字に畳んでいない（印の無い操作が丸ごと落ちる）: %s", host)
	}
	if !strings.Contains(host, `<> '`+SessionKindHost+`'`) {
		t.Errorf("usageHostSessionCond が %q を除いていない: %s", SessionKindHost, host)
	}
	optIn := usageOptInCond()
	if !strings.Contains(optIn, `COALESCE(JSON_UNQUOTE(JSON_EXTRACT(e.detail, '$.agent')), '')`) {
		t.Errorf("usageOptInCond が NULL を空文字に畳んでいない: %s", optIn)
	}
	for _, a := range UsageOptInAgents {
		if !strings.Contains(optIn, `'`+a+`'`) {
			t.Errorf("usageOptInCond に %q が入っていない: %s", a, optIn)
		}
	}
}

// TestUsageCoverageHostAndOptIn は、器のセッション ID の印（detail の session_kind）と
// 計測が任意の AI（detail の agent）の条件が、UsageCoverage の数え方にどう効くかを実データで確かめる。
//
// 経路は CLI と MCP の両方を入れる。印は経路ではなくセッション ID の種類を表すので、
// MCP の行でも同じ理由で外れること（これまで CLI の行でしか確かめられていなかった）をここで固定する。
func TestUsageCoverageHostAndOptIn(t *testing.T) {
	db := usageTestDB(t)
	ctx := context.Background()

	pid, err := CreateProject(ctx, db, Project{Slug: "req", Prefix: "REQ", Width: 4, Name: "req"})
	if err != nil {
		t.Fatal(err)
	}
	alice, err := CreateUser(ctx, db, "alice", "alice", "x", "member")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := CreateUser(ctx, db, "bob", "bob", "x", "member")
	if err != nil {
		t.Fatal(err)
	}
	doc := &mdformat.Document{Front: []mdformat.Field{{Key: "id", Value: "REQ-0001"}, {Key: "status", Value: "Todo"}}, GapNL: 2, TrailNL: 1}
	iid, err := InsertDocument(ctx, db, pid, 1, "REQ-0001.md", doc, "cli")
	if err != nil {
		t.Fatal(err)
	}

	at := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	event := func(via, sessionID, detail string, userID int64) {
		t.Helper()
		var sid, det any
		if sessionID != "" {
			sid = sessionID
		}
		if detail != "" {
			det = detail
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO issue_events (project_id, issue_id, at, kind, via, actor_user_id, session_id, detail)
VALUES (?, ?, ?, 'create', ?, ?, ?, ?)`, pid, iid, at, via, userID, sid, det); err != nil {
			t.Fatalf("issue_events (%s, %s): %v", via, sessionID, err)
		}
	}
	// 対象に数えるもの
	event("mcp", "mcp-1", "", alice)              // 印なし（detail が NULL）
	event("mcp", "mcp-2", `{"other":"x"}`, alice) // detail はあるが印が無い（JSON_EXTRACT が NULL）
	event("cli", "cli-sess", "", alice)           // 会話のセッション ID
	event("mcp", "mcp-copilot-on", `{"agent":"copilot"}`, bob)
	// 対象から外れるもの
	event("mcp", "mcp-host", `{"session_kind":"host"}`, alice)    // 器のセッション ID（MCP）
	event("cli", "cli-host", `{"session_kind":"host"}`, alice)    // 器のセッション ID（CLI）
	event("mcp", "mcp-copilot-off", `{"agent":"copilot"}`, alice) // 計測を有効にしていない利用者の Copilot
	// 人の操作（セッション ID なしの CLI）
	event("cli", "", "", alice)

	// bob だけが Copilot の計測を有効にしている（操作の窓の中にスナップショットが届いている）。
	if _, dup, err := InsertUsageSnapshot(ctx, db, UsageSnapshot{
		ProjectID: pid, UserID: bob, Client: "copilot", SessionID: "sess-b", ConversationID: "c-b",
		Trigger: "stop", At: at, DedupeKey: "dedupe-copilot-bob", ReceivedAt: at,
	}); err != nil || dup {
		t.Fatalf("スナップショットの挿入: dup=%v err=%v", dup, err)
	}

	sessions := func(evs []UsageEvent) []string {
		var out []string
		for _, e := range evs {
			out = append(out, e.SessionID)
		}
		sort.Strings(out)
		return out
	}
	since := at.Add(-time.Hour)

	evs, humans, err := UsageCoverage(ctx, db, pid, 0, since)
	if err != nil {
		t.Fatal(err)
	}
	// 器の印の付いた 2 件（MCP・CLI）と、計測を有効にしていない利用者の Copilot の 1 件が落ちる。
	if got, want := strings.Join(sessions(evs), " "), "cli-sess mcp-1 mcp-2 mcp-copilot-on"; got != want {
		t.Errorf("全員の対象 = %q（期待 %q）", got, want)
	}
	// 器の印の付いた操作は humans にも数えない（人の操作はセッション ID なしの CLI の 1 件だけ）。
	if humans != 1 {
		t.Errorf("humans = %d（期待 1。器の印の付いた操作を人の操作に数えている）", humans)
	}

	evs, humans, err = UsageCoverage(ctx, db, pid, alice, since)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(sessions(evs), " "), "cli-sess mcp-1 mcp-2"; got != want {
		t.Errorf("alice の対象 = %q（期待 %q）", got, want)
	}
	if humans != 1 {
		t.Errorf("alice の humans = %d（期待 1）", humans)
	}
}
