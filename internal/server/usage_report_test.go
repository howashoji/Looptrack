package server

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/store"
)

// レポート用の集計（期間指定・前回以降）と台帳。

type reportResp struct {
	From       *string `json:"from"`
	To         string  `json:"to"`
	DataEnd    string  `json:"data_end"`
	SinceLast  bool    `json:"since_last"`
	LastReport *struct {
		Name string `json:"name"`
	} `json:"last_report"`
	TotalTokens  int64 `json:"total_tokens"`
	StageCount   int   `json:"stage_count"`
	Unattributed struct {
		TotalTokens int64 `json:"total_tokens"`
	} `json:"unattributed"`
	ExcludedTokens        int64    `json:"excluded_tokens"`
	ExcludedConversations []string `json:"excluded_conversations"`
	ByIssue               []struct {
		ID          string   `json:"id"`
		Type        string   `json:"type"`
		Labels      []string `json:"labels"`
		TotalTokens int64    `json:"total_tokens"`
	} `json:"by_issue"`
	ByLabel []struct {
		Key         string `json:"key"`
		TotalTokens int64  `json:"total_tokens"`
	} `json:"by_label"`
	ByType []struct {
		Key         string `json:"key"`
		TotalTokens int64  `json:"total_tokens"`
	} `json:"by_type"`
	ByStage []struct {
		Key         string `json:"key"`
		TotalTokens int64  `json:"total_tokens"`
	} `json:"by_stage"`
	Conversations []struct {
		ConversationID string `json:"conversation_id"`
		TotalTokens    int64  `json:"total_tokens"`
	} `json:"conversations"`
}

type ledgerResp struct {
	Items []struct {
		ID                    int64    `json:"id"`
		Name                  string   `json:"name"`
		From                  *string  `json:"from"`
		DataEnd               string   `json:"data_end"`
		ExcludedConversations []string `json:"excluded_conversations"`
		RequestID             *int64   `json:"request_id"`
		CreatedBy             string   `json:"created_by"`
		CreatedAt             string   `json:"created_at"`
		Via                   string   `json:"via"`
	} `json:"items"`
	Count    int     `json:"count"`
	NextFrom *string `json:"next_from"`
}

func TestUsageReportAndLedger(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("req")
	e.project("secret")
	editor := e.user("editor", "editor-password-1", "member")
	viewer := e.user("viewer", "viewer-password-1", "member")
	store.SetMember(ctx, e.db, pr.ID, editor.ID, "editor")
	store.SetMember(ctx, e.db, pr.ID, viewer.ID, "viewer")
	a, v := e.apiAs(editor), e.apiAs(viewer)
	a.json(201, "POST", "/projects/req/issues", map[string]any{"title": "一つ目", "labels": []string{"api", "web"}}, nil)
	a.json(201, "POST", "/projects/req/issues", map[string]any{"title": "二つ目", "type": "bug"}, nil)

	send := func(conv, trigger, issue, op, at string, in int64, excluded bool) {
		t.Helper()
		b := usageBody(conv, "s-"+conv, trigger, issue, op, in, 0, in)
		b["at"], b["excluded"] = at, excluded
		a.json(201, "POST", "/projects/req/usage", b, nil)
	}
	// c1: 期間の前に起票（100）→ 期間内にコメント（+200）→ stop（+50。REQ-0001 へ）
	send("c1", "issue_op", "REQ-0001", "create", "2026-08-01T01:00:00Z", 100, false)
	send("c1", "issue_op", "REQ-0001", "comment", "2026-08-10T01:00:00Z", 300, false)
	send("c1", "stop", "", "", "2026-08-10T02:00:00Z", 350, false)
	// c2: 対象外の会話（期間内 500）
	send("c2", "issue_op", "REQ-0002", "comment", "2026-08-10T03:00:00Z", 500, true)
	// c3: 期間の後。REQ-0002 の起票（70）
	send("c3", "issue_op", "REQ-0002", "create", "2026-08-12T01:00:00Z", 70, false)

	// 期間指定（日付だけの to はその日を含む・日本時間）
	var rep reportResp
	v.json(200, "GET", "/projects/req/usage/report?from=2026-08-05&to=2026-08-11", nil, &rep)
	if rep.TotalTokens != 250 || rep.StageCount != 2 || rep.ExcludedTokens != 500 || len(rep.ExcludedConversations) != 1 || rep.ExcludedConversations[0] != "c2" {
		t.Errorf("期間指定: %+v", rep)
	}
	if rep.From == nil || *rep.From != "2026-08-04T15:00:00Z" || rep.To != "2026-08-11T15:00:00Z" || rep.DataEnd != rep.To {
		t.Errorf("期間の解釈: from=%v to=%s data_end=%s", rep.From, rep.To, rep.DataEnd)
	}
	if len(rep.ByIssue) != 1 || rep.ByIssue[0].ID != "REQ-0001" || rep.ByIssue[0].TotalTokens != 250 || len(rep.ByIssue[0].Labels) != 2 {
		t.Errorf("イシュー別: %+v", rep.ByIssue)
	}
	if len(rep.ByLabel) != 2 || rep.ByLabel[0].TotalTokens != 250 || len(rep.ByType) != 1 || rep.ByType[0].Key != "task" {
		t.Errorf("ラベル別・種類別: %+v %+v", rep.ByLabel, rep.ByType)
	}
	if len(rep.ByStage) != 2 || rep.ByStage[0].Key != "comment" || rep.ByStage[0].TotalTokens != 200 || rep.ByStage[1].Key != "stop" {
		t.Errorf("段階別: %+v", rep.ByStage)
	}
	if len(rep.Conversations) != 1 || rep.Conversations[0].ConversationID != "c1" {
		t.Errorf("会話別（対象外は出ない）: %+v", rep.Conversations)
	}

	// 期間の指定なし・矛盾は 400（次に打つコマンドを含む）
	if e := a.fail(400, "GET", "/projects/req/usage/report", nil); !strings.Contains(e.Error.Message, "--since-last") {
		t.Errorf("期間なし: %s", e.Error.Message)
	}
	a.fail(400, "GET", "/projects/req/usage/report?since_last=1&from=2026-08-01", nil)
	a.fail(400, "GET", "/projects/req/usage/report?from=2026-08-11&to=2026-08-01", nil)
	a.fail(400, "GET", "/projects/req/usage/report?from=8/1", nil)
	a.fail(404, "GET", "/projects/secret/usage/report?since_last=1", nil)

	// 前回以降（台帳が空）は最初のデータから今まで
	v.json(200, "GET", "/projects/req/usage/report?since_last=1", nil, &rep)
	if rep.From != nil || rep.LastReport != nil || rep.TotalTokens != 420 || !rep.SinceLast {
		t.Errorf("台帳が空の前回以降: %+v", rep)
	}

	// 台帳: 閲覧者は登録できない
	entry := map[string]any{"name": "8月前半", "from": "2026-08-01", "to": "2026-08-11", "excluded_conversations": []string{"c2"}, "total_tokens": 350}
	v.fail(http.StatusForbidden, "POST", "/projects/req/usage/ledger", entry)
	var added struct {
		Entry struct {
			ID      int64  `json:"id"`
			DataEnd string `json:"data_end"`
			Via     string `json:"via"`
		} `json:"entry"`
		Message string `json:"message"`
	}
	a.json(201, "POST", "/projects/req/usage/ledger", entry, &added)
	if added.Entry.DataEnd != "2026-08-11T15:00:00Z" || added.Entry.Via != "api" || !strings.Contains(added.Message, "次回の前回以降は 2026-08-12 00:00:00 から") {
		t.Errorf("登録: %+v", added)
	}
	if e := a.fail(http.StatusConflict, "POST", "/projects/req/usage/ledger", entry); !strings.Contains(e.Error.Message, "looptrack issue usage ledger list") {
		t.Errorf("同名: %s", e.Error.Message)
	}
	// 次回の前回以降はデータ終端から（c3 の 70 だけ）
	v.json(200, "GET", "/projects/req/usage/report?since_last=1", nil, &rep)
	if rep.From == nil || *rep.From != "2026-08-11T15:00:00Z" || rep.LastReport == nil || rep.LastReport.Name != "8月前半" || rep.TotalTokens != 70 {
		t.Errorf("登録後の前回以降: %+v", rep)
	}

	// 過去の台帳の取り込み（created_at・データ終端が過去・依頼 ID）。前回以降の起点は最も新しいデータ終端のまま
	var req struct {
		Request struct {
			ID int64 `json:"id"`
		} `json:"request"`
	}
	a.json(201, "POST", "/projects/req/usage/requests", map[string]any{"from": "2026-07-01", "to": "2026-07-31"}, &req)
	old := map[string]any{"name": "7月", "from": "2026-07-01T00:00:00+09:00", "to": "2026-08-01", "data_end": "2026-07-31 18:00",
		"created_at": "2026-08-01 10:00", "request_id": req.Request.ID, "note": "過去の台帳から"}
	a.json(201, "POST", "/projects/req/usage/ledger", old, nil)
	var list ledgerResp
	v.json(200, "GET", "/projects/req/usage/ledger", nil, &list)
	if list.Count != 2 || list.Items[0].Name != "8月前半" || list.Items[1].Name != "7月" || list.NextFrom == nil || *list.NextFrom != "2026-08-11T15:00:00Z" {
		t.Errorf("台帳の一覧: %+v", list)
	}
	if it := list.Items[1]; it.CreatedAt != "2026-08-01T01:00:00Z" || it.DataEnd != "2026-07-31T09:00:00Z" || it.RequestID == nil || *it.RequestID != req.Request.ID || it.CreatedBy != "editor" {
		t.Errorf("取り込んだ行: %+v", it)
	}
	if it := list.Items[0]; len(it.ExcludedConversations) != 1 || it.From == nil {
		t.Errorf("除外した会話: %+v", it)
	}
	// 入力誤り
	for name, body := range map[string]map[string]any{
		"名前なし":     {"to": "2026-08-01"},
		"名前に改行":    {"name": "a\nb", "to": "2026-08-01"},
		"to なし":    {"name": "x"},
		"from が後":  {"name": "x", "from": "2026-08-02", "to": "2026-08-01"},
		"終端が期間の外":  {"name": "x", "from": "2026-08-01", "to": "2026-08-02", "data_end": "2026-08-05"},
		"終端が未来":    {"name": "x", "to": "2099-01-01"},
		"作成日時が未来":  {"name": "x", "to": "2026-08-01", "created_at": "2099-01-01"},
		"時刻の形":     {"name": "x", "to": "2026/08/01"},
		"知らない項目":   {"name": "x", "to": "2026-08-01", "pdf": "x"},
		"負の total": {"name": "x", "to": "2026-08-01", "total_tokens": -1},
	} {
		if code, _, b := a.do("POST", "/projects/req/usage/ledger", body); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, code, b)
		}
	}

	// 表示用の文と xlsx
	code, h, body := v.do("GET", "/projects/req/usage/report?from=2026-08-05&to=2026-08-11&format=md", nil)
	if code != 200 || !strings.Contains(string(body), "## イシュー別") || !strings.Contains(string(body), "| REQ-0001 | task |") {
		t.Errorf("format=md: %d %s", code, body)
	}
	code, h, body = v.do("GET", "/projects/req/usage/report.xlsx?since_last=1", nil)
	if code != 200 || !strings.Contains(h.Get("Content-Type"), "spreadsheetml") || !strings.HasPrefix(string(body), "PK") {
		t.Errorf("xlsx: %d %s", code, h)
	}

	// 追記専用: アプリの権限では書き換え・削除できない
	for _, stmt := range []string{"UPDATE usage_reports SET name = 'x'", "DELETE FROM usage_reports"} {
		_, err := e.app.Exec(stmt)
		if !store.IsAppendOnlyViolation(err) { // MySQL は権限・SQLite はトリガで拒否する
			t.Errorf("%s: err = %v, want 権限エラー（追記専用）", stmt, err)
		}
	}
}

func TestUsageReportMCP(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("req")
	editor := e.user("editor", "editor-password-1", "member")
	viewer := e.user("viewer", "viewer-password-1", "member")
	store.SetMember(ctx, e.db, pr.ID, editor.ID, "editor")
	store.SetMember(ctx, e.db, pr.ID, viewer.ID, "viewer")
	a := e.apiAs(editor)
	a.json(201, "POST", "/projects/req/issues", map[string]any{"title": "一つ目"}, nil)
	b := usageBody("c1", "s1", "issue_op", "REQ-0001", "create", 100, 10, 1)
	b["at"] = "2026-08-01T00:00:00Z"
	a.json(201, "POST", "/projects/req/usage", b, nil)

	m := e.mcpAs(a.token, map[string]string{"X-Looptrack-Project": "req"})
	text, data := m.call("usage_report", map[string]any{"since_last": true}, false)
	if !strings.Contains(text, "合計 110 トークン") || data["total_tokens"] != float64(110) {
		t.Errorf("usage_report: %s %v", text, data["total_tokens"])
	}
	m.call("usage_report", map[string]any{}, true) // 期間なし
	text, _ = m.call("add_usage_ledger", map[string]any{"name": "初回", "to": data["to"], "data_end": data["data_end"], "total_tokens": 110}, false)
	if !strings.Contains(text, "台帳に登録: #") {
		t.Errorf("add_usage_ledger: %s", text)
	}
	text, data = m.call("list_usage_ledger", map[string]any{}, false)
	if !strings.Contains(text, "初回") || data["count"] != float64(1) {
		t.Errorf("list_usage_ledger: %s %v", text, data)
	}
	var via string
	e.db.QueryRow("SELECT via FROM usage_reports").Scan(&via)
	if via != "mcp" {
		t.Errorf("via = %q", via)
	}
	_, data = m.call("usage_report", map[string]any{"since_last": true, "format": "json"}, false)
	if data["total_tokens"] != float64(0) || data["last_report"] == nil {
		t.Errorf("登録後の前回以降: %v", data)
	}
	// 閲覧者は台帳に登録できない
	mv := e.mcpAs(e.apiAs(viewer).token, map[string]string{"X-Looptrack-Project": "req"})
	if text, _ := mv.call("add_usage_ledger", map[string]any{"name": "閲覧者", "to": "2026-08-02"}, true); !strings.Contains(text, "editor 以上") {
		t.Errorf("閲覧者の登録: %s", text)
	}
}

// CLI: looptrack issue usage report / ledger add / ledger list が API を通って動く。
func TestUsageReportCLI(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("req")
	u := e.user("editor", "editor-password-1", "member")
	store.SetMember(ctx, e.db, pr.ID, u.ID, "editor")
	a := e.apiAs(u)
	a.json(201, "POST", "/projects/req/issues", map[string]any{"title": "一つ目", "labels": []string{"api"}}, nil)
	b := usageBody("c1", "s1", "issue_op", "REQ-0001", "create", 1000, 234, 1)
	b["at"] = "2026-08-01T00:00:00Z"
	a.json(201, "POST", "/projects/req/usage", b, nil)

	home := t.TempDir()
	run := func(args ...string) (int, string) {
		t.Helper()
		env := append(cliAPIEnv(e.srv.URL+"/im", "req", a.token, home), "XDG_CONFIG_HOME="+filepath.Join(home, "cfg"), "CLAUDE_PROJECT_DIR="+home, "LOOPTRACK_USAGE=0")
		r := runCLI(t, home, env, "", append([]string{"issue"}, args...)...)
		return r.code, r.stdout + r.stderr
	}

	if code, out := run("usage", "report"); code == 0 || !strings.Contains(out, "--since-last") {
		t.Errorf("期間なし: %d %s", code, out)
	}
	if code, out := run("usage", "report", "--from", "2026-07-01", "--to", "2026-08-31"); code != 0 || !strings.Contains(out, "合計 1,234 トークン") || !strings.Contains(out, "| REQ-0001 | task |") {
		t.Errorf("usage report: %d %s", code, out)
	}
	code, out := run("usage", "report", "--since-last", "--json")
	reportFile := filepath.Join(home, "report.json")
	if code != 0 || os.WriteFile(reportFile, []byte(out), 0o644) != nil || !strings.Contains(out, `"total_tokens": 1234`) {
		t.Fatalf("usage report --json: %d %s", code, out)
	}
	xlsx := filepath.Join(home, "r.xlsx")
	if code, out := run("usage", "report", "--since-last", "--xlsx", xlsx); code != 0 || !strings.Contains(out, "書き出し: ") {
		t.Errorf("usage report --xlsx: %d %s", code, out)
	}
	if st, err := os.Stat(xlsx); err != nil || st.Size() == 0 {
		t.Errorf("xlsx が無い: %v", err)
	}

	if code, out := run("usage", "ledger", "list"); code != 0 || !strings.Contains(out, "台帳は空です") {
		t.Errorf("空の台帳: %d %s", code, out)
	}
	if code, out := run("usage", "ledger", "add", "初回", "--from-report", reportFile, "--note", "report.pdf"); code != 0 || !strings.Contains(out, "台帳に登録: #1 初回") {
		t.Fatalf("ledger add: %d %s", code, out)
	}
	if code, out := run("usage", "ledger", "add", "初回", "--from-report", reportFile); code == 0 || !strings.Contains(out, "同じ名前") {
		t.Errorf("同名: %d %s", code, out)
	}
	a.json(201, "POST", "/projects/req/usage/requests", map[string]any{"since_last": true}, nil) // 依頼 #1
	if code, out := run("usage", "ledger", "add", "過去分", "--from", "2026-07-01", "--to", "2026-07-31", "--created-at", "2026-08-01 09:00", "--excluded", "x1,x2", "--request-id", "1"); code != 0 {
		t.Errorf("ledger add（過去分）: %d %s", code, out)
	}
	if code, out := run("usage", "ledger", "list"); code != 0 || !strings.Contains(out, "初回") || !strings.Contains(out, "過去分") || !strings.Contains(out, "1,234") || !strings.Contains(out, "2 件") {
		t.Errorf("ledger list: %d %s", code, out)
	}
	var excluded, request string
	e.db.QueryRow("SELECT CAST(excluded_conversations AS CHAR), CAST(request_id AS CHAR) FROM usage_reports WHERE name = '過去分'").Scan(&excluded, &request)
	if canonJSON(t, excluded) != `["x1","x2"]` || request != "1" {
		t.Errorf("過去分の行: %s %s", excluded, request)
	}
	// 登録後の前回以降は空
	if code, out := run("usage", "report", "--since-last"); code != 0 || !strings.Contains(out, "合計 0 トークン") || !strings.Contains(out, "前回のレポート「初回」") {
		t.Errorf("登録後: %d %s", code, out)
	}
	// 既存の show は従来どおり
	if code, out := run("usage", "show", "REQ-0001"); code != 0 || !strings.Contains(out, "REQ-0001 トークン消費: 合計 1,234") {
		t.Errorf("usage show: %d %s", code, out)
	}
}
