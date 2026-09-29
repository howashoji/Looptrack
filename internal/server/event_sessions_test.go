package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

// MCP の着手を、後から届いたスナップショットの会話（session_id）に結ぶ。
// 付与の hook は PostToolUse の tool_use_id をスナップショットに付けて送る。Claude Code はそれと同じ値を
// tools/call の _meta["claudecode/toolUseId"] に入れてくるので、呼び出しの時点でハッシュを残しておき、突き合わせる。

// callMeta は _meta 付きで tools/call を送る（go-sdk のクライアントから。ツールの実行エラーは期待しない）。
func (m *mcpClient) callMeta(name string, args map[string]any, meta mcp.Meta) {
	m.t.Helper()
	res, err := m.cs.CallTool(context.Background(), &mcp.CallToolParams{Meta: meta, Name: name, Arguments: args})
	if err != nil {
		m.t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		var text []string
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				text = append(text, tc.Text)
			}
		}
		m.t.Fatalf("%s %v: isError: %s", name, args, strings.Join(text, "\n"))
	}
}

func toolUseMeta(v any) mcp.Meta { return mcp.Meta{"claudecode/toolUseId": v} }

// lastStatusEvent は、イシューの最後の状態変更のイベントの ID・detail（生の JSON）を返す。
func lastStatusEvent(t *testing.T, e *env, displayID string) (int64, string) {
	t.Helper()
	var id int64
	var detail string
	if err := e.db.QueryRow(`SELECT e.id, COALESCE(e.detail, '') FROM issue_events e JOIN issues i ON i.id = e.issue_id
WHERE i.display_id = ? AND e.kind = 'status' ORDER BY e.id DESC LIMIT 1`, displayID).Scan(&id, &detail); err != nil {
		t.Fatal(err)
	}
	return id, detail
}

// linkedSession は、イベントを結んだセッション ID（結ばれていなければ空）を返す。
func linkedSession(t *testing.T, e *env, eventID int64) string {
	t.Helper()
	var sid string
	err := e.db.QueryRow(`SELECT session_id FROM issue_event_sessions WHERE event_id = ?`, eventID).Scan(&sid)
	if err != nil && !strings.Contains(err.Error(), "no rows") {
		t.Fatal(err)
	}
	return sid
}

// mcpUsageBody は MCP の変更操作のスナップショット（付与の hook が送る形）。
func mcpUsageBody(e *env, session, issue, toolUseID string) map[string]any {
	b := usageBody("c-"+session, session, "issue_op", issue, "status", 100, 10, 1)
	b["via"] = "mcp"
	b["tool_use_id"] = toolUseID
	b["at"] = e.clock.Now().UTC().Format(time.RFC3339Nano)
	return b
}

// TestMCPToolUseRecordedAsHash は、_meta のツール呼び出しの ID を検査し、ハッシュだけを detail の "tool_use" に残すことを確かめる。
// 検査に落ちる値・キーが無い・文字列でない値では "tool_use" を残さない（従来どおり）。対照として、検査を通る値は残る。
func TestMCPToolUseRecordedAsHash(t *testing.T) {
	e, _, ed := newAPIEnv(t)
	m := e.mcpAs(ed.token, map[string]string{"X-Looptrack-Project": "req"})

	valid := "toolu_01AbC-xyz"
	want, ok := service.ToolUseHash(valid)
	if !ok || len(want) != 32 {
		t.Fatalf("前提が崩れています: ToolUseHash(%q) = %q, %v", valid, want, ok)
	}
	cases := []struct {
		name string
		meta mcp.Meta // nil はキー無し
		want string   // 空は "tool_use" が無いこと
	}{
		{"検査を通る値（対照）", toolUseMeta(valid), want},
		{"キーが無い", nil, ""},
		{"別のキーだけ", mcp.Meta{"other/toolUseId": valid}, ""},
		{"空文字", toolUseMeta(""), ""},
		{"許さない文字", toolUseMeta("toolu 01;DROP"), ""},
		{"長すぎる（129 字）", toolUseMeta(strings.Repeat("a", 129)), ""},
		{"文字列でない（数）", toolUseMeta(12345), ""},
		{"文字列でない（オブジェクト）", toolUseMeta(map[string]any{"id": valid}), ""},
	}
	for _, c := range cases {
		var created struct {
			Issue issueDetailJSON `json:"issue"`
		}
		ed.json(201, "POST", "/projects/req/issues", map[string]any{"title": c.name}, &created)
		m.callMeta("set_status", map[string]any{"id": created.Issue.ID, "status": "In Progress"}, c.meta)
		_, raw := lastStatusEvent(t, e, created.Issue.ID)
		var detail map[string]any
		if err := json.Unmarshal([]byte(raw), &detail); err != nil {
			t.Fatalf("%s: detail を読めない: %v %q", c.name, err, raw)
		}
		got, has := detail["tool_use"]
		switch {
		case c.want != "" && got != c.want:
			t.Errorf("%s: tool_use = %v（%s のはず）", c.name, got, c.want)
		case c.want == "" && has:
			t.Errorf("%s: 検査に落ちる値なのに tool_use が残った: %v", c.name, got)
		}
		// 生の値は残さない
		if strings.Contains(raw, valid) {
			t.Errorf("%s: 生のツール呼び出しの ID が detail に残った: %s", c.name, raw)
		}
	}
}

// TestUsageLinksMCPEvents は、スナップショットの受信で結ぶ条件を確かめる。
// 結ぶのは自分の（認証した利用者の）MCP のイベントで、接続 ID か空のセッション ID のもの・窓の中のものだけ。
func TestUsageLinksMCPEvents(t *testing.T) {
	e, pr, ed := newAPIEnv(t)
	ot := e.user("ot", "ot-password-123", "member")
	if err := store.SetMember(context.Background(), e.db, pr.ID, ot.ID, "editor"); err != nil {
		t.Fatal(err)
	}
	otAPI := e.apiAs(ot)
	id := func(title string) string {
		var created struct {
			Issue issueDetailJSON `json:"issue"`
		}
		ed.json(201, "POST", "/projects/req/issues", map[string]any{"title": title}, &created)
		return created.Issue.ID
	}
	mine, others, named, late := id("自分の着手"), id("他人の着手"), id("ヘッダで名乗った着手"), id("窓の外")

	m := e.mcpAs(ed.token, map[string]string{"X-Looptrack-Project": "req"})
	mOt := e.mcpAs(otAPI.token, map[string]string{"X-Looptrack-Project": "req"})
	mNamed := e.mcpAs(ed.token, map[string]string{"X-Looptrack-Project": "req", "X-Looptrack-Session": "hdr-S"})

	m.callMeta("set_status", map[string]any{"id": mine, "status": "In Progress"}, toolUseMeta("toolu_same"))
	mOt.callMeta("set_status", map[string]any{"id": others, "status": "In Progress"}, toolUseMeta("toolu_same")) // 同じ ID・他人
	mNamed.callMeta("set_status", map[string]any{"id": named, "status": "In Progress"}, toolUseMeta("toolu_named"))
	evMine, _ := lastStatusEvent(t, e, mine)
	evOthers, _ := lastStatusEvent(t, e, others)
	evNamed, _ := lastStatusEvent(t, e, named)

	// 自分のスナップショット（会話 S）: 自分のイベントは結ばれ、同じ tool_use を持つ他人のイベントは結ばれない
	ed.json(201, "POST", "/projects/req/usage", mcpUsageBody(e, "S", mine, "toolu_same"), nil)
	if got := linkedSession(t, e, evMine); got != "S" {
		t.Errorf("自分のイベント（対照）が結ばれない: %q", got)
	}
	if got := linkedSession(t, e, evOthers); got != "" {
		t.Errorf("他人のイベントが結ばれた: %q", got)
	}
	// 再送（重複）でも結果は変わらない（1 回の呼び出しが残すイベント（状態・担当）はどれも同じ tool_use を持つので、数は呼び出しの前後で比べる）
	links := func() string {
		t.Helper()
		var n int
		var sessions string
		if err := e.db.QueryRow(`SELECT COUNT(*), COALESCE(MIN(session_id), '') FROM issue_event_sessions`).Scan(&n, &sessions); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%d %s", n, sessions)
	}
	before := links()
	if strings.HasPrefix(before, "0 ") {
		t.Fatalf("前提が崩れています: 結びが 1 件も無い: %s", before)
	}
	ed.json(200, "POST", "/projects/req/usage", mcpUsageBody(e, "S", mine, "toolu_same"), nil)
	if after := links(); after != before {
		t.Errorf("再送で結びが変わった: %s → %s", before, after)
	}
	// 別の会話を名乗る同じ tool_use の送信（別の dedupe_key）でも、結んだ値は変わらない
	ed.json(201, "POST", "/projects/req/usage", mcpUsageBody(e, "S2", mine, "toolu_same"), nil)
	if got := linkedSession(t, e, evMine); got != "S" {
		t.Errorf("結んだ値が後の送信で変わった: %q", got)
	}

	// ヘッダで名乗ったセッション ID のイベントは結ばない
	ed.json(201, "POST", "/projects/req/usage", mcpUsageBody(e, "S", named, "toolu_named"), nil)
	if got := linkedSession(t, e, evNamed); got != "" {
		t.Errorf("ヘッダで名乗ったセッション ID のイベントが結ばれた: %q", got)
	}

	// 窓（store.UsageAttachWindow）の外に届いたスナップショットでは結ばない
	m.callMeta("set_status", map[string]any{"id": late, "status": "In Progress"}, toolUseMeta("toolu_late"))
	evLate, _ := lastStatusEvent(t, e, late)
	e.clock.Add(store.UsageAttachWindow + time.Minute)
	ed.json(201, "POST", "/projects/req/usage", mcpUsageBody(e, "S", late, "toolu_late"), nil)
	if got := linkedSession(t, e, evLate); got != "" {
		t.Errorf("窓の外のイベントが結ばれた: %q", got)
	}

	// 追記専用: 本番と同じ権限のユーザーでは書き換え・削除できない（MySQL は権限、SQLite はトリガで拒否する）。
	// 対照: 結んだ行（evMine）があるので、拒否されなければ実際に書き換わる
	for _, stmt := range []string{"UPDATE issue_event_sessions SET session_id = 'x'", "DELETE FROM issue_event_sessions"} {
		if _, err := e.app.Exec(stmt); err == nil {
			t.Errorf("%s が通った", stmt)
		}
	}
	if got := linkedSession(t, e, evMine); got != "S" {
		t.Errorf("結びが書き換わった: %q", got)
	}
}

// TestLinkedMCPStartSeenFromCLI は、CLI（X-Looptrack-Session = S）から見て、MCP で着手し S に結んだものは自分の着手、
// S' に結んだものは「別のセッションが着手しています」になることを、service.Next（next）と markOtherSession（一覧）の両方で確かめる。
// 器の ID（X-Looptrack-Session-Kind: host）で見るときは結んだ値と比べない（従来どおり「判定できない」の注記）。
func TestLinkedMCPStartSeenFromCLI(t *testing.T) {
	const otherNote, crossNote = "は別のセッションが着手しています", "は別の経路（CLI / MCP）で着手されています"
	l := newLoopsEnv(t)
	b := l.create("MCP で着手し S に結ぶ", map[string]any{"priority": "P0"})
	c := l.create("MCP で着手し S' に結ぶ", map[string]any{"priority": "P1"})
	m := l.e.mcpAs(l.ed.token, map[string]string{"X-Looptrack-Project": l.pr.Slug})
	m.callMeta("set_status", map[string]any{"id": b, "status": "In Progress"}, toolUseMeta("toolu_b"))
	m.callMeta("set_status", map[string]any{"id": c, "status": "In Progress"}, toolUseMeta("toolu_c"))
	l.ed.json(201, "POST", "/projects/"+l.pr.Slug+"/usage", mcpUsageBody(l.e, "s-A", b, "toolu_b"), nil)
	l.ed.json(201, "POST", "/projects/"+l.pr.Slug+"/usage", mcpUsageBody(l.e, "s-B", c, "toolu_c"), nil)

	// next（S = s-A）: b は自分の着手（resumed・注記なし）、c は別のセッション
	l.ed.header["X-Looptrack-Session"] = "s-A"
	var r nextResp
	l.ed.json(200, "POST", "/projects/"+l.pr.Slug+"/next", map[string]any{}, &r)
	if r.Action != "resumed" || r.Issue == nil || r.Issue.ID != b {
		t.Errorf("next: S に結んだものが自分の着手にならない: %+v", r)
	}
	if len(r.OtherSessions) != 1 || r.OtherSessions[0] != c || len(r.CrossPathSessions) != 0 ||
		!strings.Contains(r.Text, c+" "+otherNote) || strings.Contains(r.Text, crossNote) {
		t.Errorf("next の注記: other=%v cross=%v\n%s", r.OtherSessions, r.CrossPathSessions, r.Text)
	}
	// 一覧（markOtherSession）
	marks := map[string]issueJSON{}
	for _, it := range l.list("sort=id") {
		marks[it.ID] = it
	}
	if marks[b].OtherSession || marks[b].CrossPathSession {
		t.Errorf("一覧: S に結んだものに印が付いた: %+v", marks[b])
	}
	if !marks[c].OtherSession || marks[c].CrossPathSession {
		t.Errorf("一覧: S' に結んだものが別のセッションにならない: %+v", marks[c])
	}
	// CLI の list にも同じ注記が出る
	cli := newCLIEnv(t, l.e, l.pr.Slug, l.ed.token)
	cli.session = "s-A"
	if res := mustCLI(t, cli.run("", "list", "--sort", "id"), 0, "list"); !strings.Contains(res.stdout, otherNote) ||
		!strings.Contains(res.stdout, c) || strings.Contains(res.stdout, crossNote) {
		t.Errorf("CLI の list:\n%s", res.stdout)
	}

	// 器の ID（host）で見る: 結んだ値とは比べない。どちらも従来どおり「判定できない」
	l.ed.header["X-Looptrack-Session-Kind"] = "host"
	var h nextResp
	l.ed.json(200, "POST", "/projects/"+l.pr.Slug+"/next", map[string]any{}, &h)
	if len(h.OtherSessions) != 0 || len(h.CrossPathSessions) != 2 || !strings.Contains(h.Text, crossNote) {
		t.Errorf("器の ID で結んだ値と比べた（next）: other=%v cross=%v\n%s", h.OtherSessions, h.CrossPathSessions, h.Text)
	}
	for _, it := range l.list("sort=id") {
		if it.OtherSession || !it.CrossPathSession {
			t.Errorf("器の ID で結んだ値と比べた（一覧）: %+v", it)
		}
	}
}

// TestUnlinkedMCPStartKeepsCrossPathNote は、結びが無いとき（_meta が無い・スナップショットが未着）に
// 従来どおり「別の経路で着手されています」の注記が出ることを確かめる（退行の検査）。対照として、結んだものには出ない。
func TestUnlinkedMCPStartKeepsCrossPathNote(t *testing.T) {
	const crossNote = "は別の経路（CLI / MCP）で着手されています"
	l := newLoopsEnv(t)
	noMeta := l.create("_meta が無い", map[string]any{"priority": "P0"})
	noSnap := l.create("スナップショットが未着", map[string]any{"priority": "P1"})
	linked := l.create("結んだ（対照）", map[string]any{"priority": "P2"})
	m := l.e.mcpAs(l.ed.token, map[string]string{"X-Looptrack-Project": l.pr.Slug})
	m.call("set_status", map[string]any{"id": noMeta, "status": "In Progress"}, false)
	m.callMeta("set_status", map[string]any{"id": noSnap, "status": "In Progress"}, toolUseMeta("toolu_nosnap"))
	m.callMeta("set_status", map[string]any{"id": linked, "status": "In Progress"}, toolUseMeta("toolu_linked"))
	// _meta の無い操作に、同じイシューのスナップショットが届いても結ばない（tool_use が無いので突き合わない）
	l.ed.json(201, "POST", "/projects/"+l.pr.Slug+"/usage", mcpUsageBody(l.e, "s-A", noMeta, "toolu_nometa"), nil)
	l.ed.json(201, "POST", "/projects/"+l.pr.Slug+"/usage", mcpUsageBody(l.e, "s-A", linked, "toolu_linked"), nil)

	l.ed.header["X-Looptrack-Session"] = "s-A"
	var r nextResp
	l.ed.json(200, "POST", "/projects/"+l.pr.Slug+"/next", map[string]any{}, &r)
	cross := strings.Join(r.CrossPathSessions, ",")
	if cross != noMeta+","+noSnap || len(r.OtherSessions) != 0 {
		t.Errorf("next: cross=%v（%s,%s のはず） other=%v\n%s", r.CrossPathSessions, noMeta, noSnap, r.OtherSessions, r.Text)
	}
	if !strings.Contains(r.Text, crossNote) {
		t.Errorf("next の注記:\n%s", r.Text)
	}
	for _, it := range l.list("sort=id") {
		wantCross := it.ID == noMeta || it.ID == noSnap
		if it.CrossPathSession != wantCross || it.OtherSession {
			t.Errorf("一覧の印: %+v（cross=%v のはず）", it, wantCross)
		}
	}
}

// linkEnv は、結ぶ条件の witness（条件を 1 つ壊すと落ちるテスト）の共通の準備。
type linkEnv struct {
	t  *testing.T
	e  *env
	ed *apiClient
	m  *mcpClient
}

func newLinkEnv(t *testing.T) *linkEnv {
	e, _, ed := newAPIEnv(t)
	return &linkEnv{t: t, e: e, ed: ed, m: e.mcpAs(ed.token, map[string]string{"X-Looptrack-Project": "req"})}
}

// start は MCP（セッション ID を名乗らない接続）で着手し、イシューの ID と状態変更のイベントの ID を返す。
func (l *linkEnv) start(title, toolUseID string) (string, int64) {
	l.t.Helper()
	var created struct {
		Issue issueDetailJSON `json:"issue"`
	}
	l.ed.json(201, "POST", "/projects/req/issues", map[string]any{"title": title}, &created)
	l.m.callMeta("set_status", map[string]any{"id": created.Issue.ID, "status": "In Progress"}, toolUseMeta(toolUseID))
	ev, _ := lastStatusEvent(l.t, l.e, created.Issue.ID)
	return created.Issue.ID, ev
}

// control は結ばれる側の対照（正しい形のスナップショットなら結ばれる）。前提が崩れていれば止める。
func (l *linkEnv) control() {
	l.t.Helper()
	id, ev := l.start("対照", "toolu_ctl")
	l.ed.json(201, "POST", "/projects/req/usage", mcpUsageBody(l.e, "S-ctl", id, "toolu_ctl"), nil)
	if got := linkedSession(l.t, l.e, ev); got != "S-ctl" {
		l.t.Fatalf("前提が崩れています: 正しい形のスナップショットで結ばれない: %q", got)
	}
}

// via が cli のスナップショットでは結ばない（MCP の操作のスナップショットではない）。
func TestUsageLinkSkipsCLISnapshot(t *testing.T) {
	l := newLinkEnv(t)
	l.control()
	id, ev := l.start("via cli", "toolu_cli")
	b := mcpUsageBody(l.e, "S", id, "toolu_cli")
	b["via"] = "cli"
	l.ed.json(201, "POST", "/projects/req/usage", b, nil)
	if got := linkedSession(t, l.e, ev); got != "" {
		t.Errorf("via=cli のスナップショットで結ばれた: %q", got)
	}
}

// trigger が issue_op でないスナップショット（stop）では結ばない。
func TestUsageLinkSkipsNonIssueOpSnapshot(t *testing.T) {
	l := newLinkEnv(t)
	l.control()
	_, ev := l.start("trigger stop", "toolu_stop")
	b := mcpUsageBody(l.e, "S", "", "toolu_stop")
	b["trigger"] = "stop"
	delete(b, "issue")
	delete(b, "op")
	l.ed.json(201, "POST", "/projects/req/usage", b, nil)
	if got := linkedSession(t, l.e, ev); got != "" {
		t.Errorf("trigger=stop のスナップショットで結ばれた: %q", got)
	}
}

// session_id が接続 ID の印（mcp-conn:）を持つスナップショットでは結ばない（結ぶと比べる種類が混ざる）。
func TestUsageLinkSkipsConnSessionSnapshot(t *testing.T) {
	l := newLinkEnv(t)
	l.control()
	id, ev := l.start("mcp-conn", "toolu_conn")
	l.ed.json(201, "POST", "/projects/req/usage", mcpUsageBody(l.e, service.MCPSessionPrefix+"x", id, "toolu_conn"), nil)
	if got := linkedSession(t, l.e, ev); got != "" {
		t.Errorf("session_id が mcp-conn: のスナップショットで結ばれた: %q", got)
	}
}

// session_id が空のスナップショットでは結ばない。REST は空の session_id を 400 で拒むので、行を直接足して service を呼ぶ
// （REST の検査を外したときの守り）。対照: 同じ手順で session_id がある行なら結ばれる。
func TestUsageLinkSkipsEmptySessionSnapshot(t *testing.T) {
	l := newLinkEnv(t)
	ctx := context.Background()
	var userID int64
	var projectID int64
	if err := l.e.db.QueryRow(`SELECT id FROM users WHERE login = 'ed'`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := l.e.db.QueryRow(`SELECT id FROM projects WHERE slug = 'req'`).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	link := func(session, toolUseID string) int64 {
		t.Helper()
		key := "k-" + toolUseID
		snap := store.UsageSnapshot{ProjectID: projectID, UserID: userID, Client: "claude-code", SessionID: session, ConversationID: "c",
			Trigger: "issue_op", Via: "mcp", At: l.e.clock.Now(), DedupeKey: key, ReceivedAt: l.e.clock.Now()}
		if _, _, err := store.InsertUsageSnapshot(ctx, l.e.db, snap); err != nil {
			t.Fatal(err)
		}
		n, err := l.e.s.svc.LinkMCPEventSessions(ctx, userID, key, toolUseID)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	_, evCtl := l.start("対照", "toolu_ctl2")
	if n := link("S-direct", "toolu_ctl2"); n == 0 || linkedSession(t, l.e, evCtl) != "S-direct" {
		t.Fatalf("前提が崩れています: 直接足した行で結ばれない: %d", n)
	}
	_, ev := l.start("空の session_id", "toolu_empty")
	if n := link("", "toolu_empty"); n != 0 || linkedSession(t, l.e, ev) != "" {
		t.Errorf("session_id が空のスナップショットで結ばれた: %d", n)
	}
}

// 他人が先に送った行と同じ dedupe_key（同じ session_id・同じツール呼び出しの ID）で自分が送っても、
// 他人の会話に自分のイベントを結ばない（行の利用者と、送ってきた利用者が違う）。
func TestUsageLinkSkipsOtherUsersSnapshotRow(t *testing.T) {
	l := newLinkEnv(t)
	l.control()
	ot := l.e.user("ot", "ot-password-123", "member")
	var projectID int64
	if err := l.e.db.QueryRow(`SELECT id FROM projects WHERE slug = 'req'`).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	if err := store.SetMember(context.Background(), l.e.db, projectID, ot.ID, "editor"); err != nil {
		t.Fatal(err)
	}
	id, ev := l.start("自分の着手", "toolu_shared")
	l.e.apiAs(ot).json(201, "POST", "/projects/req/usage", mcpUsageBody(l.e, "S-ot", id, "toolu_shared"), nil)
	l.ed.json(200, "POST", "/projects/req/usage", mcpUsageBody(l.e, "S-ot", id, "toolu_shared"), nil) // 重複
	if got := linkedSession(t, l.e, ev); got != "" {
		t.Errorf("他人の行（同じ dedupe_key）で自分のイベントが他人の会話に結ばれた: %q", got)
	}
}

// スナップショットの時刻より後のイベントは結ばない（窓の上端）。先に届いた行を、後のイベントの後で再送しても同じ。
func TestUsageLinkSkipsEventAfterSnapshot(t *testing.T) {
	l := newLinkEnv(t)
	l.control()
	var created struct {
		Issue issueDetailJSON `json:"issue"`
	}
	l.ed.json(201, "POST", "/projects/req/issues", map[string]any{"title": "後のイベント"}, &created)
	body := mcpUsageBody(l.e, "S", created.Issue.ID, "toolu_after")
	l.ed.json(201, "POST", "/projects/req/usage", body, nil)
	l.e.clock.Add(time.Minute)
	l.m.callMeta("set_status", map[string]any{"id": created.Issue.ID, "status": "In Progress"}, toolUseMeta("toolu_after"))
	ev, _ := lastStatusEvent(t, l.e, created.Issue.ID)
	l.ed.json(200, "POST", "/projects/req/usage", body, nil) // 重複の再送で、元の行の時刻で結ぶ
	if got := linkedSession(t, l.e, ev); got != "" {
		t.Errorf("スナップショットより後のイベントが結ばれた: %q", got)
	}
}
