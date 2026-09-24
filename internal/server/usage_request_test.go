package server

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/store"
)

// 画面からのレポート作成依頼（次のセッションの summary に出て、台帳に request_id つきで登録すると消える）。
// MCP の issue_usage。

type requestResp struct {
	ID          int64   `json:"id"`
	SinceLast   bool    `json:"since_last"`
	From        *string `json:"from"`
	To          *string `json:"to"`
	Period      string  `json:"period"`
	Target      string  `json:"target"`
	Note        string  `json:"note"`
	RequestedBy string  `json:"requested_by"`
	Via         string  `json:"via"`
	Done        bool    `json:"done"`
	Report      *struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"report"`
	Command string `json:"command"`
}

type requestListResp struct {
	Items []requestResp `json:"items"`
	Count int           `json:"count"`
}

func TestUsageRequests(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("req")
	other := e.project("other")
	editor := e.user("editor", "editor-password-1", "member")
	viewer := e.user("viewer", "viewer-password-1", "member")
	store.SetMember(ctx, e.db, pr.ID, editor.ID, "editor")
	store.SetMember(ctx, e.db, other.ID, editor.ID, "editor")
	store.SetMember(ctx, e.db, pr.ID, viewer.ID, "viewer")
	a, v := e.apiAs(editor), e.apiAs(viewer)
	a.json(201, "POST", "/projects/req/issues", map[string]any{"title": "一つ目"}, nil)
	b := usageBody("c1", "s1", "issue_op", "REQ-0001", "create", 100, 10, 1)
	b["at"] = "2026-08-10T01:00:00Z"
	a.json(201, "POST", "/projects/req/usage", b, nil)

	// 登録: 閲覧者は 403、入力誤りは 400
	v.fail(http.StatusForbidden, "POST", "/projects/req/usage/requests", map[string]any{"since_last": true})
	for name, body := range map[string]map[string]any{
		"期間なし":       {},
		"前回以降と from": {"since_last": true, "from": "2026-08-01"},
		"from が後":    {"from": "2026-08-10", "to": "2026-08-01"},
		"from が未来":   {"from": "2099-01-01"},
		"対象に改行":      {"since_last": true, "target": "a\nb"},
		"時刻の形":       {"from": "8/1"},
		"知らない項目":     {"since_last": true, "pdf": "x"},
	} {
		if code, _, body := a.do("POST", "/projects/req/usage/requests", body); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
	var created struct {
		Request requestResp `json:"request"`
		Message string      `json:"message"`
	}
	a.json(201, "POST", "/projects/req/usage/requests", map[string]any{"since_last": true, "target": "ラベル api", "note": "月次の報告用"}, &created)
	r1 := created.Request
	if r1.ID == 0 || !r1.SinceLast || r1.From != nil || r1.To != nil || r1.Via != "api" || r1.RequestedBy != "editor" || r1.Done ||
		!strings.Contains(r1.Command, "usage report --request 1") || !strings.Contains(created.Message, "summary") {
		t.Errorf("前回以降の依頼: %+v %s", r1, created.Message)
	}
	a.json(201, "POST", "/projects/req/usage/requests", map[string]any{"from": "2026-08-01", "to": "2026-08-31"}, &created)
	r2 := created.Request
	if r2.From == nil || *r2.From != "2026-07-31T15:00:00Z" || r2.To == nil || *r2.To != "2026-08-31T15:00:00Z" || r2.SinceLast {
		t.Errorf("期間の依頼（日本時間・終了日を含む）: %+v", r2)
	}

	// 一覧（閲覧者も読める）と summary
	var list requestListResp
	v.json(200, "GET", "/projects/req/usage/requests", nil, &list)
	if list.Count != 2 || list.Items[0].ID != r2.ID || list.Items[1].ID != r1.ID {
		t.Errorf("未完了の一覧: %+v", list)
	}
	var sum struct {
		UsageRequests []requestResp `json:"usage_requests"`
	}
	v.json(200, "GET", "/projects/req/summary", nil, &sum)
	if len(sum.UsageRequests) != 2 || sum.UsageRequests[1].Target != "ラベル api" {
		t.Errorf("summary の依頼: %+v", sum.UsageRequests)
	}
	a.json(200, "GET", "/projects/other/summary", nil, &sum)
	if len(sum.UsageRequests) != 0 {
		t.Errorf("別プロジェクトの summary に出た: %+v", sum.UsageRequests)
	}

	// 依頼の期間で集計する（他の期間指定とは併用しない）
	var rep struct {
		From        *string      `json:"from"`
		To          string       `json:"to"`
		SinceLast   bool         `json:"since_last"`
		TotalTokens int64        `json:"total_tokens"`
		Request     *requestResp `json:"request"`
	}
	v.json(200, "GET", "/projects/req/usage/report?request=2", nil, &rep)
	if rep.Request == nil || rep.Request.ID != r2.ID || rep.From == nil || *rep.From != "2026-07-31T15:00:00Z" || rep.To != "2026-08-31T15:00:00Z" || rep.TotalTokens != 110 {
		t.Errorf("依頼 2 の集計: %+v", rep)
	}
	v.json(200, "GET", "/projects/req/usage/report?request=1", nil, &rep)
	if !rep.SinceLast || rep.From != nil || rep.Request.Target != "ラベル api" {
		t.Errorf("依頼 1 の集計: %+v", rep)
	}
	code, _, md := v.do("GET", "/projects/req/usage/report?request=1&format=md", nil)
	if code != 200 || !strings.Contains(string(md), "依頼: #1") || !strings.Contains(string(md), "対象: ラベル api") || !strings.Contains(string(md), "request_id 1") {
		t.Errorf("format=md の依頼: %d %s", code, md)
	}
	a.fail(http.StatusBadRequest, "GET", "/projects/req/usage/report?request=1&since_last=1", nil)
	a.fail(http.StatusBadRequest, "GET", "/projects/req/usage/report?request=x", nil)
	if e := a.fail(http.StatusNotFound, "GET", "/projects/other/usage/report?request=1", nil); !strings.Contains(e.Error.Message, "looptrack issue usage requests") {
		t.Errorf("別プロジェクトの依頼: %s", e.Error.Message)
	}

	// 台帳に request_id つきで登録すると依頼が消える。同じ依頼への 2 回目は 409、無い依頼・別プロジェクトの依頼は 400
	if e := a.fail(http.StatusBadRequest, "POST", "/projects/other/usage/ledger", map[string]any{"name": "x", "to": "2026-08-31", "request_id": r1.ID}); !strings.Contains(e.Error.Message, "依頼 #1 がありません") {
		t.Errorf("別プロジェクトの依頼: %s", e.Error.Message)
	}
	a.fail(http.StatusBadRequest, "POST", "/projects/req/usage/ledger", map[string]any{"name": "x", "to": "2026-08-31", "request_id": 99})
	a.json(201, "POST", "/projects/req/usage/ledger", map[string]any{"name": "8月", "from": "2026-08-01", "to": "2026-08-31", "request_id": r2.ID}, nil)
	if e := a.fail(http.StatusConflict, "POST", "/projects/req/usage/ledger", map[string]any{"name": "8月（再）", "to": "2026-08-31", "request_id": r2.ID}); !strings.Contains(e.Error.Message, "「8月」で完了済み") {
		t.Errorf("完了済みの依頼: %s", e.Error.Message)
	}
	// 検査をすり抜けた同時登録も DB の一意制約で止まる（1 依頼に台帳 1 行まで）
	if _, err := store.InsertUsageReport(ctx, e.app, store.UsageReport{ProjectID: pr.ID, Name: "同時", PeriodTo: time.Now(), DataEnd: time.Now(),
		RequestID: r2.ID, CreatedBy: editor.ID, Via: "api", CreatedAt: time.Now()}); !errors.Is(err, store.ErrDuplicateRequest) {
		t.Errorf("同じ依頼の 2 行目: %v", err)
	}
	v.json(200, "GET", "/projects/req/usage/requests", nil, &list)
	if list.Count != 1 || list.Items[0].ID != r1.ID {
		t.Errorf("完了後の未完了一覧: %+v", list)
	}
	v.json(200, "GET", "/projects/req/usage/requests?all=1", nil, &list)
	if list.Count != 2 || !list.Items[0].Done || list.Items[0].Report == nil || list.Items[0].Report.Name != "8月" || list.Items[0].Command != "" {
		t.Errorf("完了を含む一覧: %+v", list)
	}
	v.json(200, "GET", "/projects/req/summary", nil, &sum)
	if len(sum.UsageRequests) != 1 || sum.UsageRequests[0].ID != r1.ID {
		t.Errorf("完了後の summary: %+v", sum.UsageRequests)
	}

	// 未完了は 20 件まで
	for i := 0; i < maxOpenRequests-1; i++ {
		a.json(201, "POST", "/projects/req/usage/requests", map[string]any{"since_last": true}, nil)
	}
	if e := a.fail(http.StatusConflict, "POST", "/projects/req/usage/requests", map[string]any{"since_last": true}); !strings.Contains(e.Error.Message, "未完了の依頼が 20 件") {
		t.Errorf("上限: %s", e.Error.Message)
	}

	// 追記専用: アプリの権限では書き換え・削除できない
	for _, stmt := range []string{"UPDATE usage_report_requests SET target = 'x'", "DELETE FROM usage_report_requests"} {
		_, err := e.app.Exec(stmt)
		if !store.IsAppendOnlyViolation(err) { // MySQL は権限・SQLite はトリガで拒否する
			t.Errorf("%s: err = %v, want 権限エラー（追記専用）", stmt, err)
		}
	}
}

// 画面: ボードの「レポート作成」→ 依頼のフォーム（editor 以上・CSRF）。
func TestUsageRequestWeb(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("req")
	e.project("secret")
	editor := e.user("editor", "editor-password-1", "member")
	viewer := e.user("viewer", "viewer-password-1", "member")
	store.SetMember(ctx, e.db, pr.ID, editor.ID, "editor")
	store.SetMember(ctx, e.db, pr.ID, viewer.ID, "viewer")

	vc := e.client()
	e.enroll(vc, "viewer", "viewer-password-1")
	if _, body := e.get(vc, "/im/p/req/"); !strings.Contains(body, `href="/im/p/req/report-requests"`) || !strings.Contains(body, "レポート作成") {
		t.Errorf("ボードにレポート作成が無い: %s", body)
	}
	res, body := e.get(vc, "/im/p/req/report-requests")
	if res.StatusCode != 200 || strings.Contains(body, `name="mode"`) || !strings.Contains(body, "editor 以上") {
		t.Errorf("閲覧者の画面: %d %s", res.StatusCode, body)
	}
	if res, _ := e.formAt(vc, "/im/p/req/report-requests", "/im/p/req/report-requests", url.Values{"mode": {"since_last"}}); res.StatusCode != http.StatusForbidden {
		t.Errorf("閲覧者の登録: %d", res.StatusCode)
	}
	if res, _ := e.get(vc, "/im/p/secret/report-requests"); res.StatusCode != http.StatusNotFound {
		t.Errorf("権限の無いプロジェクト: %d", res.StatusCode)
	}

	ec := e.client()
	e.enroll(ec, "editor", "editor-password-1")
	// CSRF なしは 403 で何も登録しない
	if res, _ := e.post(ec, "/im/p/req/report-requests", url.Values{"mode": {"since_last"}}); res.StatusCode != http.StatusForbidden {
		t.Errorf("CSRF なし: %d", res.StatusCode)
	}
	// 期間を指定するのに開始日が無い → 400 で入力を残して描き直す
	res, body = e.formAt(ec, "/im/p/req/report-requests", "/im/p/req/report-requests", url.Values{"mode": {"period"}, "target": {"全体"}})
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(body, "期間を指定してください") || !strings.Contains(body, `value="全体"`) {
		t.Errorf("開始日なし: %d %s", res.StatusCode, body)
	}
	res, _ = e.formAt(ec, "/im/p/req/report-requests", "/im/p/req/report-requests",
		url.Values{"mode": {"period"}, "from": {"2026-08-01"}, "to": {"2026-08-31"}, "target": {"ラベル web"}, "note": {"提出用 <b>"}})
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/im/p/req/report-requests?done=1" {
		t.Fatalf("登録: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	_, body = e.get(ec, "/im/p/req/report-requests?done=1")
	if !strings.Contains(body, "依頼 #1 を登録しました") || !strings.Contains(body, "ラベル web") || !strings.Contains(body, "提出用 &lt;b&gt;") ||
		!strings.Contains(body, "2026-08-01 00:00 〜 2026-09-01 00:00") || !strings.Contains(body, "トークンレポートを作成して（依頼 #1）") {
		t.Errorf("登録後の画面: %s", body)
	}
	var via, target string
	e.db.QueryRow("SELECT via, target FROM usage_report_requests WHERE id = 1").Scan(&via, &target)
	if via != "web" || target != "ラベル web" {
		t.Errorf("記録: via=%s target=%s", via, target)
	}
	// 画面のセッションでも API から登録できる（CSRF ヘッダが要る）
	req, _ := http.NewRequest("POST", e.srv.URL+"/im/api/v1/projects/req/usage/requests", strings.NewReader(`{"since_last":true}`))
	req.Header.Set("Content-Type", "application/json")
	if res, err := ec.Do(req); err != nil || res.StatusCode != http.StatusForbidden {
		t.Errorf("CSRF ヘッダなしの API: %v %v", err, res)
	}
}

// MCP: list_usage_requests・project_summary・usage_report の request_id・add_usage_ledger の request_id と、issue_usage。
func TestUsageRequestMCP(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("req")
	secret := e.project("secret")
	editor := e.user("editor", "editor-password-1", "member")
	store.SetMember(ctx, e.db, pr.ID, editor.ID, "editor")
	root := e.apiAs(e.adminIn("root", "root-password-12", "secret"))
	a := e.apiAs(editor)
	a.json(201, "POST", "/projects/req/issues", map[string]any{"title": "一つ目"}, nil)
	root.json(201, "POST", "/projects/secret/issues", map[string]any{"title": "非公開"}, nil)
	for i, in := range []int64{100, 300} {
		b := usageBody("c1", "s1", "issue_op", "REQ-0001", []string{"create", "comment"}[i], in, 10, int64(i+1))
		b["at"] = []string{"2026-08-01T00:00:00Z", "2026-08-02T00:00:00Z"}[i]
		a.json(201, "POST", "/projects/req/usage", b, nil)
	}
	_ = secret

	m := e.mcpAs(a.token, map[string]string{"X-Looptrack-Project": "req"})
	// issue_usage（REST の GET /issues/{id}/usage と同じ内容）
	var rest usageResp
	a.json(200, "GET", "/issues/REQ-0001/usage", nil, &rest)
	text, data := m.call("issue_usage", map[string]any{"id": "REQ-0001"}, false)
	if !strings.Contains(text, "REQ-0001 トークン消費: 合計 310（段階 2") || !strings.Contains(text, "| issue_op | comment |") ||
		data["total_tokens"] != float64(rest.TotalTokens) || data["stage_count"] != float64(rest.StageCount) {
		t.Errorf("issue_usage: %s %v（REST %+v）", text, data, rest)
	}
	if stages, _ := data["stages"].([]any); len(stages) != 2 {
		t.Errorf("issue_usage の stages: %v", data["stages"])
	}
	if text, _ := m.call("issue_usage", map[string]any{"id": "SECRET-0001"}, true); !strings.Contains(text, "見つかりません") {
		t.Errorf("権限の無いイシュー: %s", text)
	}

	text, _ = m.call("list_usage_requests", map[string]any{}, false)
	if text != "未完了の依頼はありません" {
		t.Errorf("空の依頼: %s", text)
	}
	a.json(201, "POST", "/projects/req/usage/requests", map[string]any{"since_last": true, "note": "至急"}, nil)
	text, data = m.call("list_usage_requests", map[string]any{}, false)
	if !strings.Contains(text, "#1 ") || !strings.Contains(text, "メモ: 至急") || !strings.Contains(text, "usage report --request 1") || data["count"] != float64(1) {
		t.Errorf("list_usage_requests: %s %v", text, data)
	}
	text, data = m.call("project_summary", map[string]any{}, false)
	if !strings.Contains(text, "トークンレポートの作成依頼（未完了 1 件）") || !strings.Contains(text, "次のコマンド:") {
		t.Errorf("project_summary の依頼: %s", text)
	}
	_, rep := m.call("usage_report", map[string]any{"request_id": 1, "format": "json"}, false)
	if rep["total_tokens"] != float64(310) || rep["request"] == nil {
		t.Errorf("usage_report の request_id: %v", rep)
	}
	m.call("usage_report", map[string]any{"request_id": 1, "since_last": true}, true)
	text, _ = m.call("add_usage_ledger", map[string]any{"name": "依頼 1", "to": rep["to"], "data_end": rep["data_end"], "request_id": 1}, false)
	if !strings.Contains(text, "台帳に登録: #") {
		t.Errorf("add_usage_ledger: %s", text)
	}
	if text, _ := m.call("list_usage_requests", map[string]any{}, false); text != "未完了の依頼はありません" {
		t.Errorf("完了後: %s", text)
	}
	if text, _ := m.call("list_usage_requests", map[string]any{"all": true}, false); !strings.Contains(text, "→ 完了（台帳 #1 依頼 1）") {
		t.Errorf("完了を含む一覧: %s", text)
	}
	if text, _ := m.call("project_summary", map[string]any{}, false); strings.Contains(text, "作成依頼") {
		t.Errorf("完了後の project_summary: %s", text)
	}
}

// CLI: summary に依頼が出る → usage report --request → ledger add --from-report で依頼が完了する。
func TestUsageRequestCLI(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("req")
	u := e.user("editor", "editor-password-1", "member")
	store.SetMember(ctx, e.db, pr.ID, u.ID, "editor")
	a := e.apiAs(u)
	a.json(201, "POST", "/projects/req/issues", map[string]any{"title": "一つ目"}, nil)
	b := usageBody("c1", "s1", "issue_op", "REQ-0001", "create", 1000, 234, 1)
	b["at"] = "2026-08-01T00:00:00Z"
	a.json(201, "POST", "/projects/req/usage", b, nil)

	home := t.TempDir()
	// looptrack を起動する（args は looptrack の引数全体。利用者の LOOPTRACK_* は持ち込まない）
	runLT := func(args ...string) (int, string) {
		t.Helper()
		env := append(cliAPIEnv(e.srv.URL+"/im", "req", a.token, home), "XDG_CONFIG_HOME="+filepath.Join(home, "cfg"), "CLAUDE_PROJECT_DIR="+home, "LOOPTRACK_USAGE=0")
		r := runCLI(t, home, env, "", args...)
		return r.code, r.stdout + r.stderr
	}
	run := func(args ...string) (int, string) { t.Helper(); return runLT(append([]string{"issue"}, args...)...) }

	// 依頼が無ければ summary は従来どおり（依頼の節を出さない）
	// 依頼の期間・次のコマンドなどはサーバが文字列にして返す。CLI は LOOPTRACK_LANG から
	// Accept-Language を送るので日本語で届く（cliHomeEnv の LOOPTRACK_LANG=ja）。
	if code, out := run("summary"); code != 0 || strings.Contains(out, "作成依頼") {
		t.Errorf("依頼なしの summary: %d %s", code, out)
	}
	if code, out := run("usage", "requests"); code != 0 || !strings.Contains(out, "未完了の依頼はありません") {
		t.Errorf("空の requests: %d %s", code, out)
	}
	a.json(201, "POST", "/projects/req/usage/requests", map[string]any{"since_last": true, "target": "全体", "note": "月次\n報告"}, nil)
	code, out := run("summary")
	if code != 0 || !strings.Contains(out, "── トークンレポートの作成依頼（未完了 1 件） ──") || !strings.Contains(out, "期間: 前回のレポート以降") ||
		!strings.Contains(out, "メモ: 月次 報告") || !strings.Contains(out, "次のコマンド: 「トークンレポートを作成して（依頼 #1）」") {
		t.Errorf("summary の依頼: %d %s", code, out)
	}
	if code, out := run("summary", "--json"); code != 0 || !strings.Contains(out, `"usage_requests"`) {
		t.Errorf("summary --json: %d %s", code, out)
	}
	if code, out := run("usage", "requests"); code != 0 || !strings.Contains(out, "#1") || !strings.Contains(out, "未完了") || !strings.Contains(out, "--request 1") {
		t.Errorf("usage requests: %d %s", code, out)
	}
	if code, out := run("usage", "report", "--request", "1", "--since-last"); code == 0 || !strings.Contains(out, "同時に指定できません") {
		t.Errorf("--request と --since-last: %d %s", code, out)
	}
	if code, out := run("usage", "report", "--request", "1"); code != 0 || !strings.Contains(out, "依頼: #1") || !strings.Contains(out, "合計 1,234 トークン") {
		t.Errorf("usage report --request: %d %s", code, out)
	}
	code, out = run("usage", "report", "--request", "1", "--json")
	reportFile := filepath.Join(home, "report.json")
	if code != 0 || os.WriteFile(reportFile, []byte(out), 0o644) != nil || !strings.Contains(out, `"request": {`) {
		t.Fatalf("usage report --request --json: %d %s", code, out)
	}
	// skill token-report の PDF（looptrack report pdf）がサーバの集計 JSON をそのまま読める（検査だけ）
	content := filepath.Join(home, "content.json")
	os.WriteFile(content, []byte(`{"title": "トークンレポート", "sections": [{"heading": "1. 全体サマリー", "paragraphs": ["x"]},
  {"heading": "2. データの限界", "paragraphs": ["y"]}]}`), 0o644)
	if code, out := runLT("report", "pdf", "--report", reportFile, "--content", content, "--check"); code != 0 || !strings.Contains(out, "検査 OK: 章 10・表 8（合計 1,234 トークン）") {
		t.Errorf("looptrack report pdf --check: %d %s", code, out)
	}
	// --from-report の request から request_id を自動で付ける → 依頼が完了
	if code, out := run("usage", "ledger", "add", "依頼分", "--from-report", reportFile, "--note", "report.pdf"); code != 0 || !strings.Contains(out, "台帳に登録: #1 依頼分") {
		t.Fatalf("ledger add: %d %s", code, out)
	}
	var request string
	e.db.QueryRow("SELECT CAST(request_id AS CHAR) FROM usage_reports WHERE name = '依頼分'").Scan(&request)
	if request != "1" {
		t.Errorf("request_id = %q", request)
	}
	if code, out := run("summary"); code != 0 || strings.Contains(out, "作成依頼") {
		t.Errorf("完了後の summary: %d %s", code, out)
	}
	if code, out := run("usage", "requests", "--all"); code != 0 || !strings.Contains(out, "完了（台帳 #1 依頼分）") {
		t.Errorf("usage requests --all: %d %s", code, out)
	}
	if code, out := run("usage", "ledger", "add", "依頼分2", "--from-report", reportFile); code == 0 || !strings.Contains(out, "完了済み") {
		t.Errorf("完了済みの依頼への登録: %d %s", code, out)
	}
}
