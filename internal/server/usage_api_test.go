package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/howashoji/looptrack/internal/store"
)

// トークン消費のスナップショットの受け取りと、イシューの段階別の消費。

func usageBody(conv, session, trigger, issue, op string, in, out, responses int64) map[string]any {
	b := map[string]any{
		"client": "claude-code", "client_version": "2.1.271", "session_id": session, "conversation_id": conv,
		"trigger": trigger, "at": "2026-09-18T01:00:00Z", "via": "cli",
		"tokens":    map[string]any{"main": map[string]any{"input": in, "cache_create": 0, "cache_read": 0, "output": out}},
		"responses": responses,
	}
	if issue != "" {
		b["issue"], b["op"] = issue, op
	}
	return b
}

type usageResp struct {
	Issue       string `json:"issue"`
	StageCount  int    `json:"stage_count"`
	TotalTokens int64  `json:"total_tokens"`
	Stages      []struct {
		ID         int64  `json:"id"`
		Trigger    string `json:"trigger"`
		Op         string `json:"op"`
		DeltaTotal int64  `json:"delta_total"`
		Excluded   bool   `json:"excluded"`
	} `json:"stages"`
	ExcludedTotal struct {
		Main struct {
			Input int64 `json:"input"`
		} `json:"main"`
	} `json:"excluded_total"`
	Inconsistent int `json:"inconsistent"`
}

func TestUsageSnapshots(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("req")
	e.project("secret")
	editor := e.user("editor", "editor-password-1", "member")
	viewer := e.user("viewer", "viewer-password-1", "member")
	store.SetMember(ctx, e.db, pr.ID, editor.ID, "editor")
	store.SetMember(ctx, e.db, pr.ID, viewer.ID, "viewer")
	a := e.apiAs(editor)
	a.json(201, "POST", "/projects/req/issues", map[string]any{"title": "一つ目"}, nil)
	a.json(201, "POST", "/projects/req/issues", map[string]any{"title": "二つ目"}, nil)

	// 起票 → 作業 → クローズ → stop → 次の起票、の流れ（累計を送る）
	var res struct {
		ID        int64 `json:"id"`
		Duplicate bool  `json:"duplicate"`
	}
	a.json(201, "POST", "/projects/req/usage", usageBody("c1", "s1", "issue_op", "REQ-0001", "create", 100, 10, 1), &res)
	if res.ID == 0 || res.Duplicate {
		t.Fatalf("1 件目: %+v", res)
	}
	a.json(201, "POST", "/projects/req/usage", usageBody("c1", "s1", "stop", "", "", 300, 30, 3), nil)
	a.json(200, "POST", "/issues/REQ-0001/status", map[string]any{"status": "Done"}, nil)
	a.json(201, "POST", "/projects/req/usage", usageBody("c1", "s1", "issue_op", "req-0001", "status", 500, 50, 5), nil)
	a.json(201, "POST", "/projects/req/usage", usageBody("c1", "s1", "stop", "", "", 600, 60, 6), nil)
	a.json(201, "POST", "/projects/req/usage", usageBody("c1", "s1", "issue_op", "REQ-0002", "create", 700, 70, 7), nil)

	// 同じ内容の再送は増えない（累計で鍵）。tool_use_id があればそれで鍵
	a.json(200, "POST", "/projects/req/usage", usageBody("c1", "s1", "issue_op", "REQ-0002", "create", 700, 70, 7), &res)
	if !res.Duplicate {
		t.Error("再送が重複扱いにならない")
	}
	withTool := usageBody("c1", "s1", "issue_op", "REQ-0002", "comment", 720, 72, 8)
	withTool["tool_use_id"] = "toolu_1"
	a.json(201, "POST", "/projects/req/usage", withTool, nil)
	withTool["tokens"] = map[string]any{"main": map[string]any{"input": 999}}
	a.json(200, "POST", "/projects/req/usage", withTool, &res)
	if !res.Duplicate {
		t.Error("同じ tool_use_id の再送が重複扱いにならない")
	}

	// REQ-0001: 起票前の調査（110）+ stop までの作業（220）+ クローズまで（220）= 550。クローズ後の stop（110）は入らない
	var u usageResp
	a.json(200, "GET", "/issues/REQ-0001/usage", nil, &u)
	if u.Issue != "REQ-0001" || u.StageCount != 3 || u.TotalTokens != 550 || u.Inconsistent != 0 {
		t.Errorf("REQ-0001: %+v", u)
	}
	if len(u.Stages) == 3 && (u.Stages[0].Op != "create" || u.Stages[1].Trigger != "stop" || u.Stages[2].Op != "status" || u.Stages[2].DeltaTotal != 220) {
		t.Errorf("REQ-0001 の段階: %+v", u.Stages)
	}
	// REQ-0002: 起票までの区間（110）+ コメントまで（22）= 132
	a.json(200, "GET", "/issues/REQ-0002/usage", nil, &u)
	if u.StageCount != 2 || u.TotalTokens != 132 {
		t.Errorf("REQ-0002: %+v", u)
	}

	// 手動の付与（usage attach）は manual + issue で、区間はそのイシューへ
	a.json(201, "POST", "/projects/req/usage", usageBody("c1", "s1", "manual", "REQ-0002", "", 730, 73, 9), nil)
	a.json(200, "GET", "/issues/REQ-0002/usage", nil, &u)
	if u.StageCount != 3 || u.TotalTokens != 143 {
		t.Errorf("manual の付与: %+v", u)
	}

	// 対象外の会話は total に入らず excluded_total に出る
	ex := usageBody("c2", "s2", "issue_op", "REQ-0002", "comment", 40, 0, 1)
	ex["excluded"] = true
	a.json(201, "POST", "/projects/req/usage", ex, nil)
	a.json(200, "GET", "/issues/REQ-0002/usage", nil, &u)
	if u.StageCount != 4 || u.TotalTokens != 143 || u.ExcludedTotal.Main.Input != 40 {
		t.Errorf("対象外: %+v", u)
	}

	// 閲覧者は読めるが送れない。権限の無いプロジェクトは 404
	v := e.apiAs(viewer)
	v.json(200, "GET", "/issues/REQ-0001/usage", nil, nil)
	v.json(http.StatusForbidden, "POST", "/projects/req/usage", usageBody("c3", "s3", "stop", "", "", 1, 1, 1), nil)
	v.json(http.StatusNotFound, "POST", "/projects/secret/usage", usageBody("c3", "s3", "stop", "", "", 1, 1, 1), nil)
	if code, _, _ := e.apiAs(viewer).do("GET", "/issues/SEC-0001/usage", nil); code != http.StatusNotFound {
		t.Errorf("他プロジェクトのイシュー: %d", code)
	}

	// 入力誤り
	for name, mod := range map[string]func(map[string]any){
		"client なし":           func(b map[string]any) { b["client"] = "" },
		"client の文字":          func(b map[string]any) { b["client"] = "Claude Code!" },
		"trigger 不正":          func(b map[string]any) { b["trigger"] = "nope" },
		"at 不正":               func(b map[string]any) { b["at"] = "2026-09-18 01:00" },
		"負のトークン":              func(b map[string]any) { b["tokens"] = map[string]any{"main": map[string]any{"input": -1}} },
		"issue_op に issue なし": func(b map[string]any) { delete(b, "issue") },
		"op 不正":               func(b map[string]any) { b["op"] = "close" },
		"stop に issue":        func(b map[string]any) { b["trigger"] = "stop" },
		"manual に op":         func(b map[string]any) { b["trigger"] = "manual" },
		"via 不正":              func(b map[string]any) { b["via"] = "web" },
	} {
		b := usageBody("c9", "s9", "issue_op", "REQ-0001", "comment", 1, 1, 1)
		mod(b)
		if code, _, body := a.do("POST", "/projects/req/usage", b); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
	if code, _, _ := a.do("POST", "/projects/req/usage", usageBody("c9", "s9", "issue_op", "REQ-9999", "comment", 1, 1, 1)); code != http.StatusNotFound {
		t.Errorf("無いイシュー: %d", code)
	}

	// 追記専用: アプリの権限では書き換え・削除できない
	for _, stmt := range []string{"UPDATE usage_snapshots SET main_input = 0", "DELETE FROM usage_snapshots"} {
		_, err := e.app.Exec(stmt)
		if !store.IsAppendOnlyViolation(err) { // MySQL は権限・SQLite はトリガで拒否する
			t.Errorf("%s: err = %v, want 権限エラー（追記専用）", stmt, err)
		}
	}
}
