package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

// ID の項目（blocked_by / traces / refs）に空白区切りの値が 1 要素のまま保存されない（API・MCP・全文の更新）。
// labels は名前なので空白を含んでよい。既存データは service.RepairLists（looptrack repair-lists）で補正する。

func TestSpacedIDsRejected(t *testing.T) {
	e, pr, ed := newAPIEnv(t)
	ctx := context.Background()

	// 起票（REST）: ID の項目は 400、labels は通る
	for _, key := range []string{"blocked_by", "traces", "refs"} {
		er := ed.fail(400, "POST", "/projects/req/issues", map[string]any{"title": "x", key: []string{"NFR-SEC-003 NFR-SEC-004"}})
		if er.Error.Code != "invalid_argument" || !strings.Contains(er.Error.Message, key+` の値 "NFR-SEC-003 NFR-SEC-004" は使えません`) ||
			!strings.Contains(er.Error.Message, `["NFR-SEC-003", "NFR-SEC-004"]`) {
			t.Errorf("%s: %+v", key, er.Error)
		}
	}
	ed.fail(400, "POST", "/projects/req/issues", map[string]any{"title": "x", "refs": []string{"FR-1　FR-2"}}) // 全角空白
	var created struct {
		Issue issueDetailJSON `json:"issue"`
	}
	ed.json(201, "POST", "/projects/req/issues", map[string]any{"title": "要件", "labels": []string{"旧ツール 由来"},
		"refs": []string{" FR-1 ", "FR-2"}}, &created)
	it := created.Issue
	if strings.Join(it.Labels, "|") != "旧ツール 由来" || strings.Join(it.Refs, "|") != "FR-1|FR-2" {
		t.Errorf("前後の空白は除き、labels の空白は保つ: %+v", it.issueJSON)
	}

	// 項目の更新（REST）
	ed.fail(400, "PATCH", "/issues/REQ-0001", map[string]any{"refs": []string{"FR-1 FR-3"}}, "If-Match", "1")
	ed.fail(400, "PATCH", "/issues/REQ-0001", map[string]any{"traces": []string{"REQ-0001 REQ-0002"}}, "If-Match", "1")

	// 全文の更新（edit → push）: 新しく入れた空白区切りは 400、カンマ区切りは通る
	md := strings.Replace(it.Markdown, "refs: [FR-1, FR-2]", "refs: [FR-1 FR-3, FR-2]", 1)
	if md == it.Markdown {
		t.Fatalf("refs の行が無い: %s", it.Markdown)
	}
	er := ed.fail(400, "PATCH", "/issues/REQ-0001", map[string]any{"markdown": md}, "If-Match", "1")
	if !strings.Contains(er.Error.Message, `refs の値 "FR-1 FR-3" は使えません`) || !strings.Contains(er.Error.Message, "refs: [FR-1, FR-3]") {
		t.Errorf("全文の更新: %s", er.Error.Message)
	}
	md = strings.Replace(it.Markdown, "refs: [FR-1, FR-2]", "refs: [FR-1, FR-3, FR-2]", 1)
	ed.json(200, "PATCH", "/issues/REQ-0001", map[string]any{"markdown": md}, &it, "If-Match", "1")

	// 補正前の旧データ（既に入っている空白区切り）は、本文だけを直す全文の更新を妨げない
	var rowID int64
	e.db.QueryRow("SELECT id FROM issues WHERE display_id = 'REQ-0001'").Scan(&rowID)
	if _, err := e.db.Exec("UPDATE issue_values SET value = 'FR-1 FR-9' WHERE issue_id = ? AND field = 'refs' AND pos = 0", rowID); err != nil {
		t.Fatal(err)
	}
	ed.json(200, "GET", "/issues/REQ-0001", nil, &it)
	md = strings.Replace(it.Markdown, "（未記入）", "背景を書いた", 1)
	ed.json(200, "PATCH", "/issues/REQ-0001", map[string]any{"markdown": md}, &it, "If-Match", "2")
	if it.Refs[0] != "FR-1 FR-9" {
		t.Errorf("旧データの値: %v", it.Refs)
	}

	// MCP
	u := e.user("ai", "ai-password-1234", "member")
	store.SetMember(ctx, e.db, pr.ID, u.ID, "editor")
	m := e.mcpAs(e.apiAs(u).token, map[string]string{"X-Looptrack-Project": "req"})
	text, _ := m.call("create_issue", map[string]any{"title": "MCP", "refs": []string{"FR-1 FR-2"}}, true)
	if !strings.Contains(text, `refs の値 "FR-1 FR-2" は使えません`) {
		t.Errorf("MCP create_issue: %s", text)
	}
	text, _ = m.call("update_issue", map[string]any{"id": "REQ-0001", "version": 3, "blocked_by": []string{"REQ-0001 REQ-0002"}}, true)
	if !strings.Contains(text, `blocked_by の値 "REQ-0001 REQ-0002" は使えません`) {
		t.Errorf("MCP update_issue: %s", text)
	}
	m.call("create_issue", map[string]any{"title": "MCP ラベル", "labels": []string{"CASE-101 保守リスク再監査 対応"}}, false)
}

func TestRepairSpacedLists(t *testing.T) {
	e, pr, ed := newAPIEnv(t)
	ctx := context.Background()
	ed.json(201, "POST", "/projects/req/issues", map[string]any{"title": "要件", "labels": []string{"旧ツール 由来"},
		"refs": []string{"PLACEHOLDER"}, "traces": []string{"REQ-0002"}}, nil)
	ed.json(201, "POST", "/projects/req/issues", map[string]any{"title": "他", "refs": []string{"NFR-UX-001"}}, nil)
	// 旧データを作る: REQ-0001 の refs を空白区切り 2 要素分・重複つきにし、traces も空白区切りにしてクローズする
	var rowID int64
	e.db.QueryRow("SELECT id FROM issues WHERE display_id = 'REQ-0001'").Scan(&rowID)
	e.db.Exec("UPDATE issue_values SET value = 'BD-UI-047 NFR-UX-001' WHERE issue_id = ? AND field = 'refs'", rowID)
	e.db.Exec("INSERT INTO issue_values (issue_id, field, pos, value) VALUES (?, 'refs', 1, 'NFR-UX-001')", rowID)
	e.db.Exec("UPDATE issue_values SET value = 'REQ-0002  REQ-0003' WHERE issue_id = ? AND field = 'traces'", rowID)
	ed.json(200, "POST", "/issues/REQ-0001/status", map[string]any{"status": "Done"}, nil)
	var before issueDetailJSON
	ed.json(200, "GET", "/issues/REQ-0001", nil, &before)
	var l listJSON
	ed.json(200, "GET", "/projects/req/issues?all=1&ref=BD-UI-047", nil, &l)
	if len(l.Items) != 0 { // 空白区切りの 1 要素は逆引きに出ない（補正前の症状）
		t.Errorf("補正前の逆引き: %s", ids(l.Items))
	}
	ed.fail(422, "PATCH", "/issues/REQ-0001", map[string]any{"refs": []string{"BD-UI-047", "NFR-UX-001"}}, "If-Match", `"`+itoa(int64(before.Version))+`"`)

	svc := service.New(e.app, e.clock.Now) // 本番と同じ権限のアプリ用ユーザーで補正できる
	plan, err := svc.SpacedLists(ctx, pr, []string{"blocked_by", "traces", "refs"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(plan)
	want := `[{"id":"REQ-0001","status":"Done","changes":[{"field":"traces","before":["REQ-0002  REQ-0003"],"after":["REQ-0002","REQ-0003"]},` +
		`{"field":"refs","before":["BD-UI-047 NFR-UX-001","NFR-UX-001"],"after":["BD-UI-047","NFR-UX-001"]}]}]`
	if string(got) != want {
		t.Errorf("dry-run:\n%s\nwant\n%s", got, want)
	}
	if _, err := svc.SpacedLists(ctx, pr, []string{"bogus"}); err == nil {
		t.Error("不明な項目名を受け付けた")
	}
	var version int
	e.db.QueryRow("SELECT version FROM issues WHERE id = ?", rowID).Scan(&version)
	if version != before.Version {
		t.Errorf("dry-run で版が進んだ: %d → %d", before.Version, version)
	}

	e.clock.t = e.clock.t.Add(5 * time.Minute)
	done, err := svc.RepairLists(ctx, service.Actor{Via: "admin"}, pr, []string{"blocked_by", "traces", "refs"}, "空白区切りの ID の分割のテスト")
	if err != nil || len(done) != 1 || done[0].ID != "REQ-0001" {
		t.Fatalf("補正: %v %+v", err, done)
	}
	var after issueDetailJSON
	ed.json(200, "GET", "/issues/REQ-0001", nil, &after)
	if strings.Join(after.Refs, "|") != "BD-UI-047|NFR-UX-001" || strings.Join(after.Traces, "|") != "REQ-0002|REQ-0003" ||
		strings.Join(after.Labels, "|") != "旧ツール 由来" || after.Status != "Done" {
		t.Errorf("補正後: %+v", after.issueJSON)
	}
	if after.Updated != before.Updated || after.Body != before.Body || len(after.Comments) != len(before.Comments) || after.Version != before.Version+1 {
		t.Errorf("updated・本文・コメントは変えず版だけ進む: %+v / %+v", after.issueJSON, before.issueJSON)
	}
	ed.json(200, "GET", "/projects/req/issues?all=1&ref=bd-ui-047", nil, &l)
	if ids(l.Items) != "REQ-0001" {
		t.Errorf("補正後の逆引き: %s", ids(l.Items))
	}
	// 監査記録: kind repair_lists・via admin・変更前後
	var kind, via, detail string
	e.db.QueryRow("SELECT kind, via, detail FROM issue_events WHERE issue_id = ? ORDER BY id DESC LIMIT 1", rowID).Scan(&kind, &via, &detail)
	detail = canonJSON(t, detail)
	if kind != service.RepairKind || via != "admin" || !strings.Contains(detail, `"before":{"refs":["BD-UI-047 NFR-UX-001","NFR-UX-001"]`) ||
		!strings.Contains(detail, `"reason":"空白区切りの ID の分割のテスト"`) || !strings.Contains(detail, `"status":"Done"`) {
		t.Errorf("記録: %s %s %s", kind, via, detail)
	}
	// 2 回目は対象なし
	if done, err := svc.RepairLists(ctx, service.Actor{Via: "admin"}, pr, []string{"refs", "traces"}, "x"); err != nil || len(done) != 0 {
		t.Errorf("2 回目: %v %+v", err, done)
	}
}
