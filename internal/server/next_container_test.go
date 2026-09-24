package server

import (
	"strings"
	"testing"
)

// next は束ね（未クローズの子がある epic 等）と types の対象外を「着手中」として返さない。
// 束ねが In Progress なら、その子孫の候補を先に選ぶ。REST と MCP で同じ結果になる。
func TestNextSkipsContainersAndHonorsTypes(t *testing.T) {
	_, _, ed := newAPIEnv(t)
	create := func(body map[string]any) {
		t.Helper()
		ed.json(201, "POST", "/projects/req/issues", body, nil)
	}
	create(map[string]any{"title": "大枠", "type": "epic", "priority": "P1", "status": "In Progress"})        // 0001: 自分の In Progress の束ね
	create(map[string]any{"title": "別の作業", "priority": "P0"})                                               // 0002: 優先度は上だが束ねの外
	create(map[string]any{"title": "束ねの子", "priority": "P3", "parent": "REQ-0001"})                         // 0003: 束ねの子
	create(map[string]any{"title": "試験", "type": "test", "priority": "P2"})                                 // 0004
	create(map[string]any{"title": "子の無い epic", "type": "epic", "priority": "P0", "status": "In Progress"}) // 0005: 型で対象外

	var r struct {
		nextResp
		Containers   []string `json:"containers"`
		OutsideTypes []string `json:"outside_types"`
	}
	next := func(body map[string]any) {
		t.Helper()
		r.Containers, r.OutsideTypes = nil, nil
		ed.json(200, "POST", "/projects/req/next", body, &r)
	}

	// (1) In Progress の epic を着手中として返さず、束ねの子（P3）を束ねの外の P0 より先に着手する
	next(map[string]any{})
	if r.Action != "started" || r.Issue.ID != "REQ-0003" {
		t.Fatalf("epic を着手中として返した / 子を先に選ばない: %+v", r)
	}
	for _, want := range []string{"着手中として扱わない束ね（未クローズの子があるもの。子から着手する）: REQ-0001",
		"型の指定（epic は既定で対象外）により着手中として扱わない In Progress: REQ-0005"} {
		if !strings.Contains(r.Text, want) {
			t.Errorf("text に %q が無い:\n%s", want, r.Text)
		}
	}
	// 呼び直すと子を着手中として返す（束ねは containers のまま）
	next(map[string]any{})
	if r.Action != "resumed" || r.Issue.ID != "REQ-0003" || strings.Join(r.Containers, ",") != "REQ-0001" ||
		strings.Join(r.OutsideTypes, ",") != "REQ-0005" {
		t.Errorf("resumed: %+v", r)
	}

	// (2) types は着手中の選び方にも効く: task の着手中（REQ-0003）を返さず、test に着手する
	next(map[string]any{"types": []string{"test"}})
	if r.Action != "started" || r.Issue.ID != "REQ-0004" || strings.Join(r.OutsideTypes, ",") != "REQ-0005,REQ-0003" {
		t.Fatalf("types=test: %+v", r)
	}
	next(map[string]any{"types": []string{"test"}})
	if r.Action != "resumed" || r.Issue.ID != "REQ-0004" {
		t.Errorf("types=test の resumed: %+v", r)
	}
	// types に epic を明示しても、子のある epic は着手中として返さない（子の無い epic は返す）
	next(map[string]any{"types": []string{"epic"}})
	if r.Action != "resumed" || r.Issue.ID != "REQ-0005" || strings.Join(r.Containers, ",") != "REQ-0001" {
		t.Errorf("types=epic: %+v", r)
	}

	// MCP の next は REST と同じ本文を返す
	next(map[string]any{"types": []string{"test"}})
	m := ed.e.mcpAs(ed.token, map[string]string{"X-Looptrack-Project": "req"})
	text, data := m.call("next", map[string]any{"types": []string{"test"}}, false)
	if data["action"] != "resumed" || text != r.Text {
		t.Errorf("MCP と REST が違う:\nMCP:\n%s\nREST:\n%s", text, r.Text)
	}
}
