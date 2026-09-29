package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

// 合鍵（PreToolUse の hook が届ける「ツール呼び出しの ID → 会話のセッション ID」）の確かめ。

// TestSessionBindsBounded は、置き場が有限であること（期限切れを引かない・掃除する・件数の上限）を確かめる（DB は使わない）。
func TestSessionBindsBounded(t *testing.T) {
	c := &clock{t: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
	b := newSessionBinds(c.Now)
	h := func(s string) string {
		v, ok := service.ToolUseHash(s)
		if !ok {
			t.Fatalf("前提が崩れています: ToolUseHash(%q)", s)
		}
		return v
	}

	// 期限の内は引ける（対照）。期限（2 分）ちょうどで引けなくなり、引いたときに捨てる
	b.put(1, h("toolu_a"), "S-a", "")
	c.Add(sessionBindTTL - time.Second)
	if sid, _, ok := b.get(1, h("toolu_a")); !ok || sid != "S-a" {
		t.Fatalf("期限の内の合鍵（対照）が引けない: %q %v", sid, ok)
	}
	c.Add(time.Second)
	if sid, _, ok := b.get(1, h("toolu_a")); ok {
		t.Errorf("期限切れの合鍵を引いた: %q", sid)
	}
	if n := b.len(); n != 0 {
		t.Errorf("引いた期限切れの合鍵が残っている: %d 件", n)
	}

	// 引かれないまま期限が切れたものは、次に置くときの掃除で消える
	for i := range 5 {
		b.put(1, h("toolu_old"+string(rune('a'+i))), "S-old", "")
	}
	if n := b.len(); n != 5 {
		t.Fatalf("前提が崩れています: 5 件置いたのに %d 件", n)
	}
	c.Add(sessionBindTTL)
	b.put(1, h("toolu_new"), "S-new", "")
	if n := b.len(); n != 1 {
		t.Errorf("期限切れの掃除: 残りは 1 件のはずが %d 件", n)
	}

	// 件数の上限: 満ちたら期限の近いものから捨てる。新しいものは引ける
	b = newSessionBinds(c.Now)
	b.max = 3
	for i, id := range []string{"toolu_1", "toolu_2", "toolu_3", "toolu_4"} {
		b.put(1, h(id), "S-"+id, "")
		if i < 3 {
			c.Add(time.Second) // 期限をずらす（toolu_1 がいちばん近い）
		}
	}
	if n := b.len(); n != 3 {
		t.Errorf("上限（3）を超えた: %d 件", n)
	}
	if _, _, ok := b.get(1, h("toolu_1")); ok {
		t.Error("上限を超えたとき、期限のいちばん近い合鍵が捨てられていない")
	}
	if sid, _, ok := b.get(1, h("toolu_4")); !ok || sid != "S-toolu_4" {
		t.Errorf("新しく置いた合鍵が引けない: %q %v", sid, ok)
	}
	// 同じ鍵の置き直しは件数を増やさない（上書き）
	b.put(1, h("toolu_4"), "S-again", "")
	if sid, _, _ := b.get(1, h("toolu_4")); b.len() != 3 || sid != "S-again" {
		t.Errorf("置き直し: %d 件・%q", b.len(), sid)
	}

	// nil の置き場は引けない・置かない（落ちない）
	var none *sessionBinds
	none.put(1, h("toolu_x"), "S", "")
	if _, _, ok := none.get(1, h("toolu_x")); ok {
		t.Error("nil の置き場から引けた")
	}
}

// userID はログイン名の利用者の ID。
func userID(t *testing.T, e *env, login string) int64 {
	t.Helper()
	var id int64
	if err := e.db.QueryRow(`SELECT id FROM users WHERE login = ?`, login).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// bind は合鍵を REST で届ける（hook と同じ口）。
func (a *apiClient) bind(slug, toolUseID, sessionID, kind string) {
	a.e.t.Helper()
	body := map[string]any{"tool_use_id": toolUseID, "session_id": sessionID}
	if kind != "" {
		body["kind"] = kind
	}
	a.json(204, "POST", "/projects/"+slug+"/session-binds", body, nil)
}

// TestSessionBindAPI は REST の口を確かめる。受け取ったものはハッシュだけを置き、形の違う値は 400、
// 書き込み権限が無ければ 403（どちらも置かない）。
func TestSessionBindAPI(t *testing.T) {
	e, pr, ed := newAPIEnv(t)
	edID := userID(t, e, "ed")
	hash := func(s string) string { v, _ := service.ToolUseHash(s); return v }

	// 受理（対照）: 204 で、同じ利用者のハッシュで引ける。生の ID は鍵にも値にも残さない
	ed.bind("req", "toolu_ok-1", "S-1", "")
	if sid, kind, ok := e.s.binds.get(edID, hash("toolu_ok-1")); !ok || sid != "S-1" || kind != "" {
		t.Fatalf("受理した合鍵が引けない: %q %q %v", sid, kind, ok)
	}
	e.s.binds.mu.Lock()
	for k, v := range e.s.binds.m {
		if k.hash == "toolu_ok-1" || v.session == "toolu_ok-1" {
			t.Errorf("生のツール呼び出しの ID を持っている: %+v %+v", k, v)
		}
	}
	e.s.binds.mu.Unlock()
	// 種類は X-Looptrack-Session-Kind と同じ読み方（host は大小を問わず印にし、知らない値は読み捨てる）
	ed.bind("req", "toolu_host", "H-1", "HOST")
	if _, kind, _ := e.s.binds.get(edID, hash("toolu_host")); kind != service.SessionKindHost {
		t.Errorf("host の印: %q", kind)
	}
	ed.bind("req", "toolu_unknown", "S-2", "something-new")
	if _, kind, ok := e.s.binds.get(edID, hash("toolu_unknown")); !ok || kind != "" {
		t.Errorf("知らない種類は読み捨てる: %q %v", kind, ok)
	}

	before := e.s.binds.len()
	for _, c := range []struct {
		name string
		body map[string]any
		code string
	}{
		{"tool_use_id が無い", map[string]any{"session_id": "S"}, "invalid_argument"},
		{"tool_use_id に許さない文字", map[string]any{"tool_use_id": "toolu 1;x", "session_id": "S"}, "invalid_argument"},
		{"tool_use_id が長すぎる", map[string]any{"tool_use_id": strings.Repeat("a", 129), "session_id": "S"}, "invalid_argument"},
		{"session_id が無い", map[string]any{"tool_use_id": "toolu_x"}, "invalid_argument"},
		{"session_id が空白だけ", map[string]any{"tool_use_id": "toolu_x", "session_id": "  "}, "invalid_argument"},
		{"session_id が長すぎる", map[string]any{"tool_use_id": "toolu_x", "session_id": strings.Repeat("s", 129)}, "invalid_argument"},
		{"session_id が接続 ID の印で始まる", map[string]any{"tool_use_id": "toolu_x", "session_id": service.MCPSessionPrefix + "c"}, "invalid_argument"},
		{"session_id に改行", map[string]any{"tool_use_id": "toolu_x", "session_id": "S\nX"}, "invalid_argument"},
		{"session_id に復帰", map[string]any{"tool_use_id": "toolu_x", "session_id": "S\rX"}, "invalid_argument"},
		{"session_id に NUL", map[string]any{"tool_use_id": "toolu_x", "session_id": "S\x00X"}, "invalid_argument"},
		{"session_id にタブ", map[string]any{"tool_use_id": "toolu_x", "session_id": "S\tX"}, "invalid_argument"},
		{"session_id に DEL", map[string]any{"tool_use_id": "toolu_x", "session_id": "S\x7fX"}, "invalid_argument"},
		{"知らない項目", map[string]any{"tool_use_id": "toolu_x", "session_id": "S", "extra": 1}, "invalid_json"},
		{"文字列でない", map[string]any{"tool_use_id": 12345, "session_id": "S"}, "invalid_json"},
	} {
		var res struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		ed.json(400, "POST", "/projects/req/session-binds", c.body, &res)
		if res.Error.Code != c.code {
			t.Errorf("%s: code = %q（%s のはず）", c.name, res.Error.Code, c.code)
		}
	}
	if n := e.s.binds.len(); n != before {
		t.Errorf("400 のときに置いた: %d → %d 件", before, n)
	}

	// 403: 閲覧だけの参加者は置けない（対照は上の editor の受理）
	vi := e.user("vi", "vi-password-123", "member")
	if err := store.SetMember(context.Background(), e.db, pr.ID, vi.ID, "viewer"); err != nil {
		t.Fatal(err)
	}
	viAPI := e.apiAs(vi)
	viAPI.json(403, "POST", "/projects/req/session-binds", map[string]any{"tool_use_id": "toolu_vi", "session_id": "S-vi"}, nil)
	if _, _, ok := e.s.binds.get(vi.ID, hash("toolu_vi")); ok {
		t.Error("403 のときに置いた")
	}
}

// statusSession は、イシューの最後の状態変更のイベントのセッション ID。
func statusSession(t *testing.T, e *env, displayID string) string {
	t.Helper()
	var sid string
	if err := e.db.QueryRow(`SELECT COALESCE(e.session_id, '') FROM issue_events e JOIN issues i ON i.id = e.issue_id
WHERE i.display_id = ? AND e.kind = 'status' ORDER BY e.id DESC LIMIT 1`, displayID).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	return sid
}

// TestMCPSessionFromBind は mcpCallOf の順を確かめる。X-Looptrack-Session が最優先で、無いときは同じ利用者の合鍵を
// 接続 ID や空より優先する。引かない場合（別の利用者・期限切れ・_meta が無い・形が違う）は従来どおり。
// 引かない場合の検査が「経路が死んでいて通った」にならないよう、同じテストの中で引ける場合（対照）も通す。
func TestMCPSessionFromBind(t *testing.T) {
	e, pr, ed := newAPIEnv(t)
	ot := e.user("ot", "ot-password-123", "member")
	if err := store.SetMember(context.Background(), e.db, pr.ID, ot.ID, "editor"); err != nil {
		t.Fatal(err)
	}
	otAPI := e.apiAs(ot)
	m := e.mcpAs(ed.token, map[string]string{"X-Looptrack-Project": "req"})
	mNamed := e.mcpAs(ed.token, map[string]string{"X-Looptrack-Project": "req", "X-Looptrack-Session": "hdr-S"})

	// start は新しいイシューを MCP で In Progress にし、記録されたセッション ID を返す。
	start := func(mc *mcpClient, meta mcp.Meta) string {
		t.Helper()
		var created struct {
			Issue issueDetailJSON `json:"issue"`
		}
		ed.json(201, "POST", "/projects/req/issues", map[string]any{"title": "着手"}, &created)
		mc.callWithMeta("set_status", map[string]any{"id": created.Issue.ID, "status": "In Progress"}, false, meta)
		return statusSession(t, e, created.Issue.ID)
	}
	// 合鍵の無い呼び出しの値（接続 ID の印つきか空。どちらかは SDK の版で決まるので実測する）
	fallback := start(m, nil)
	if fallback != "" && !strings.HasPrefix(fallback, service.MCPSessionPrefix) {
		t.Fatalf("前提が崩れています: 合鍵の無い呼び出しのセッション ID = %q", fallback)
	}
	if got := start(m, toolUseMeta("toolu_nobind")); got != fallback {
		t.Fatalf("前提が崩れています: 合鍵を届けていない呼び出しが %q（%q のはず）", got, fallback)
	}

	// 対照: 同じ利用者の合鍵は接続 ID や空より優先される
	ed.bind("req", "toolu_ctrl", "conv-1", "")
	if got := start(m, toolUseMeta("toolu_ctrl")); got != "conv-1" {
		t.Fatalf("対照: 合鍵が引かれない: %q（conv-1 のはず）", got)
	}

	cases := []struct {
		name  string
		bind  func()
		mc    *mcpClient
		meta  mcp.Meta
		want  string
		after func()
	}{
		{"X-Looptrack-Session が最優先", func() { ed.bind("req", "toolu_hdr", "conv-h", "") }, mNamed, toolUseMeta("toolu_hdr"), "hdr-S", nil},
		{"別の利用者が届けた合鍵", func() { otAPI.bind("req", "toolu_ot", "conv-ot", "") }, m, toolUseMeta("toolu_ot"), fallback, nil},
		{"_meta が無い", func() { ed.bind("req", "toolu_nometa", "conv-n", "") }, m, nil, fallback, nil},
		{"別のキー", func() { ed.bind("req", "toolu_otherkey", "conv-k", "") }, m, mcp.Meta{"other/toolUseId": "toolu_otherkey"}, fallback, nil},
		{"形が違う（文字列でない）", func() { ed.bind("req", "toolu_num", "conv-num", "") }, m, toolUseMeta(12345), fallback, nil},
		{"形が違う（許さない文字）", func() { ed.bind("req", "toolu_sp", "conv-sp", "") }, m, toolUseMeta("toolu sp"), fallback, nil},
		{"期限切れ", func() { ed.bind("req", "toolu_late", "conv-late", ""); e.clock.Add(sessionBindTTL) }, m, toolUseMeta("toolu_late"), fallback, nil},
	}
	for _, c := range cases {
		c.bind()
		if got := start(c.mc, c.meta); got != c.want {
			t.Errorf("%s: session_id = %q（%q のはず）", c.name, got, c.want)
		}
	}
	// 対照をもう一度（期限切れの後の時刻でも、新しく届けた合鍵は引ける）
	ed.bind("req", "toolu_ctrl2", "conv-2", "")
	if got := start(m, toolUseMeta("toolu_ctrl2")); got != "conv-2" {
		t.Errorf("対照（時刻を進めた後）: %q（conv-2 のはず）", got)
	}
}

// TestMCPListAndNextOtherSessionByBind は TestListAndNextOtherSession の MCP 版。
// 同じ利用者・同じ MCP の接続でも、合鍵で会話を見分けて、別の会話の着手は「別のセッションが着手」、
// 自分の会話の着手は自分のものとして扱う（接続 ID だけでは、同じ接続の 2 つの会話を見分けられなかった）。
func TestMCPListAndNextOtherSessionByBind(t *testing.T) {
	e, _, ed := newAPIEnv(t)
	create := func(title, priority string) string {
		var created struct {
			Issue issueDetailJSON `json:"issue"`
		}
		ed.json(201, "POST", "/projects/req/issues", map[string]any{"title": title, "priority": priority}, &created)
		return created.Issue.ID
	}
	a, b := create("会話 1 が着手", "P0"), create("まだ空いている", "P1")
	m := e.mcpAs(ed.token, map[string]string{"X-Looptrack-Project": "req"})
	n := 0
	// as は会話 conv として合鍵を届けてから MCP を呼ぶ（hook と同じ順）
	as := func(conv, tool string, args map[string]any) (string, map[string]any) {
		t.Helper()
		n++
		id := "toolu_" + conv + "_" + string(rune('a'+n))
		ed.bind("req", id, conv, "")
		return m.callWithMeta(tool, args, false, toolUseMeta(id))
	}
	decode := func(data map[string]any, v any) {
		t.Helper()
		raw, _ := json.Marshal(data)
		if err := json.Unmarshal(raw, v); err != nil {
			t.Fatal(err)
		}
	}
	const note = "は別のセッションが着手しています"

	// ① 会話 1 が next で A を始める
	var r1 nextResp
	_, d1 := as("conv-1", "next", map[string]any{})
	decode(d1, &r1)
	if r1.Action != "started" || r1.Issue == nil || r1.Issue.ID != a {
		t.Fatalf("会話 1 の next: %+v", r1)
	}
	if got := statusSession(t, e, a); got != "conv-1" {
		t.Fatalf("着手のセッション ID が会話のものでない: %q", got)
	}

	// ② 会話 2（同じ接続）の next: A を resumed にせず、空いている B を始める。注記に A が出る
	var r2 nextResp
	text2, d2 := as("conv-2", "next", map[string]any{})
	decode(d2, &r2)
	if r2.Action != "started" || r2.Issue == nil || r2.Issue.ID != b {
		t.Fatalf("会話 2 の next: %+v\n%s", r2, text2)
	}
	if len(r2.OtherSessions) != 1 || r2.OtherSessions[0] != a || !strings.Contains(text2, a+" "+note) {
		t.Errorf("会話 2 の next の注記: %v\n%s", r2.OtherSessions, text2)
	}
	if len(r2.CrossPathSessions) != 0 {
		t.Errorf("会話どうしは比べられるのに「判定できない」になった: %v", r2.CrossPathSessions)
	}

	// ③ 会話 2 の list_issues: A に印、自分の B には付かない
	items := func(conv string) map[string]issueJSON {
		t.Helper()
		_, data := as(conv, "list_issues", map[string]any{})
		var v struct {
			Items []issueJSON `json:"items"`
		}
		decode(data, &v)
		out := map[string]issueJSON{}
		for _, it := range v.Items {
			out[it.ID] = it
		}
		return out
	}
	if l := items("conv-2"); !l[a].OtherSession || l[b].OtherSession {
		t.Errorf("会話 2 の list_issues: A=%+v B=%+v", l[a], l[b])
	}

	// ④ 会話 1 の next: 自分の A を着手中として返し（注記なし）、B は別のセッションとして注記する
	var r3 nextResp
	text3, d3 := as("conv-1", "next", map[string]any{})
	decode(d3, &r3)
	if r3.Action != "resumed" || r3.Issue == nil || r3.Issue.ID != a {
		t.Errorf("会話 1 の next は自分の A を返す: %+v\n%s", r3, text3)
	}
	if strings.Contains(text3, a+" "+note) || len(r3.OtherSessions) != 1 || r3.OtherSessions[0] != b {
		t.Errorf("会話 1 の next の注記: %v\n%s", r3.OtherSessions, text3)
	}
	if l := items("conv-1"); l[a].OtherSession || !l[b].OtherSession {
		t.Errorf("会話 1 の list_issues: A=%+v B=%+v", l[a], l[b])
	}
}
