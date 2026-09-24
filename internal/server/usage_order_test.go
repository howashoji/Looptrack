package server

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/store"
)

// イシューの段階の一覧は、差分を会話ごとに計算したまま、時刻順（同時刻は ID 順）で返す（REST・MCP 共通）。
func TestIssueUsageChronological(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("req")
	editor := e.user("editor", "editor-password-1", "member")
	store.SetMember(ctx, e.db, pr.ID, editor.ID, "editor")
	a := e.apiAs(editor)
	a.json(201, "POST", "/projects/req/issues", map[string]any{"title": "一つ目"}, nil)

	// 会話 ID の並び（"a-late" < "b-early"）と時刻の並びを逆にする。送る順も時刻と無関係にする
	post := func(conv, op, at string, in int64, responses int64) {
		b := usageBody(conv, "s-"+conv, "issue_op", "REQ-0001", op, in, 0, responses)
		b["at"] = at
		a.json(201, "POST", "/projects/req/usage", b, nil)
	}
	post("a-late", "comment", "2026-09-18T09:18:28Z", 1000, 1) // 後の会話（Codex の例）
	post("b-early", "create", "2026-09-18T01:37:00Z", 100, 1)
	post("b-early", "comment", "2026-09-18T02:55:00Z", 400, 2)
	post("a-late", "status", "2026-09-18T09:30:00Z", 1500, 2)
	post("c-same", "comment", "2026-09-18T02:55:00Z", 30, 1) // b-early の 2 件目と同時刻（ID は後）

	type stage struct {
		ID             int64  `json:"id"`
		At             string `json:"at"`
		ConversationID string `json:"conversation_id"`
		DeltaTotal     int64  `json:"delta_total"`
	}
	var u struct {
		TotalTokens int64   `json:"total_tokens"`
		Stages      []stage `json:"stages"`
	}
	a.json(200, "GET", "/issues/REQ-0001/usage", nil, &u)
	var got []string
	for _, st := range u.Stages {
		got = append(got, fmt.Sprintf("%s %s %d", st.At[11:16], st.ConversationID, st.DeltaTotal))
	}
	// 差分は会話ごと: b-early 100・300、c-same 30、a-late 1000・500
	want := []string{"01:37 b-early 100", "02:55 b-early 300", "02:55 c-same 30", "09:18 a-late 1000", "09:30 a-late 500"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("REST の並び:\n got %v\nwant %v", got, want)
	}
	if u.TotalTokens != 1930 {
		t.Errorf("合計 = %d, want 1930", u.TotalTokens)
	}
	for i := 1; i < len(u.Stages); i++ {
		p, c := u.Stages[i-1], u.Stages[i]
		if p.At > c.At || (p.At == c.At && p.ID > c.ID) {
			t.Errorf("時刻順（同時刻は ID 順）でない: %+v → %+v", p, c)
		}
	}

	// MCP の issue_usage も同じ並び（表と structured content の両方）
	m := e.mcpAs(a.token, map[string]string{"X-Looptrack-Project": "req"})
	text, data := m.call("issue_usage", map[string]any{"id": "REQ-0001"}, false)
	stages, _ := data["stages"].([]any)
	var mcpIDs, restIDs []string
	for _, s := range stages {
		mcpIDs = append(mcpIDs, fmt.Sprint(s.(map[string]any)["id"]))
	}
	for _, s := range u.Stages {
		restIDs = append(restIDs, fmt.Sprint(s.ID))
	}
	if strings.Join(mcpIDs, ",") != strings.Join(restIDs, ",") {
		t.Errorf("MCP の stages の並び %v が REST %v と違う", mcpIDs, restIDs)
	}
	var times []string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "| 2026-") {
			times = append(times, line[13:18])
		}
	}
	if strings.Join(times, ",") != "01:37,02:55,02:55,09:18,09:30" {
		t.Errorf("MCP の表の並び: %v\n%s", times, text)
	}
}
