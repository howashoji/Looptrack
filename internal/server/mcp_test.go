package server

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

// /im/mcp を SDK のクライアントで HTTP 越しに呼ぶ。

// headerTransport は要求に見出しを**補う**（既定として持たせる）。SDK が組み立てる要求には
// 見出しを足せないので、クライアントの側から入れるためのもの。
type headerTransport struct {
	header map[string]string
}

// RoundTrip は、要求が自分で付けていない見出しだけを補う。
// **要求の見出しを上書きしない**: 上書きすると、要求ごとの指定（a.do(..., "Accept-Language", "en") など）が
// 黙って既定（env.client の Accept-Language: ja）に戻り、英語を頼んだのに日本語の応答を読んでいることに
// テストが気づけない（実際にこの上書きで既存の検査が赤くなったことがある。
// lang_web_test.go の apiIn が素の http.Client を使っているのも、この上書きを避けるためだった）。
func (t headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range t.header {
		if r.Header.Get(k) == "" {
			r.Header.Set(k, v)
		}
	}
	return http.DefaultTransport.RoundTrip(r)
}

type mcpClient struct {
	t      *testing.T
	cs     *mcp.ClientSession
	notice string // 直前の結果に付いた導入の指示（text からは除く）
}

// mcpAs は **日本語の AI として接続する**（Accept-Language: ja）。サーバの既定は英語なので、
// これを付けないとツールの結果が英語になり、日本語で書いた検査が当たらない。header で上書きできる。
func (e *env) mcpAs(token string, header map[string]string) *mcpClient {
	e.t.Helper()
	h := map[string]string{"Authorization": "Bearer " + token, "Accept-Language": "ja"}
	for k, v := range header {
		h[k] = v
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: e.srv.URL + "/im/mcp", HTTPClient: &http.Client{Transport: headerTransport{h}}, MaxRetries: -1, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { cs.Close() })
	return &mcpClient{t: e.t, cs: cs}
}

// call はツールを呼び、テキストと構造化データを返す。wantErr ならツールの実行エラーを期待する。
func (m *mcpClient) call(name string, args map[string]any, wantErr bool) (string, map[string]any) {
	m.t.Helper()
	res, err := m.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		m.t.Fatalf("%s: %v", name, err)
	}
	var text []string
	m.notice = ""
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			if tc.Meta[setupNoticeMeta] != nil {
				m.notice = tc.Text
				continue
			}
			text = append(text, tc.Text)
		}
	}
	joined := strings.Join(text, "\n")
	if res.IsError != wantErr {
		m.t.Fatalf("%s %v: isError=%v, want %v: %s", name, args, res.IsError, wantErr, joined)
	}
	// structuredContent は返さない（Claude Code がそれだけを AI に渡し、text の本文・通知を捨てるため）
	if res.StructuredContent != nil {
		m.t.Fatalf("%s: structuredContent を返している: %v", name, res.StructuredContent)
	}
	data := map[string]any{}
	if v := res.Meta[mcpDataMeta]; v != nil {
		b, _ := json.Marshal(v)
		json.Unmarshal(b, &data)
	}
	return joined, data
}

func TestMCPUnauthenticated(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"x","version":"1"}}}`
	for _, auth := range []string{"", "Bearer imp_invalid", "Basic abc"} {
		req, _ := http.NewRequest("POST", e.srv.URL+"/im/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized || !strings.HasPrefix(res.Header.Get("WWW-Authenticate"), "Bearer") {
			t.Errorf("認証 %q: %d %q", auth, res.StatusCode, res.Header.Get("WWW-Authenticate"))
		}
	}
	// Web のセッション Cookie では使えない
	e.user("web", "web-password-123", "admin")
	wc := e.client()
	e.enroll(wc, "web", "web-password-123")
	req, _ := http.NewRequest("POST", e.srv.URL+"/im/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res, err := wc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("セッション Cookie: %d", res.StatusCode)
	}
}

func TestMCPTools(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ex, _ := e.rulesProject("ex", statusRules...)
	req, _ := e.rulesProject("req", verifyRules...)
	u := e.user("ai", "ai-password-1234", "member")
	store.SetMember(ctx, e.db, ex.ID, u.ID, "editor")
	store.SetMember(ctx, e.db, req.ID, u.ID, "editor")
	token := e.apiAs(u).token
	m := e.mcpAs(token, map[string]string{"X-Looptrack-Project": "req", "X-Looptrack-Session": "claude-sess"})

	tools, err := m.cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
		if tool.Description == "" || tool.InputSchema == nil {
			t.Errorf("%s: 説明または入力スキーマが無い", tool.Name)
		}
	}
	sort.Strings(names)
	if got := strings.Join(names, ","); got != "add_comment,add_usage_ledger,assign_issue,create_issue,get_issue,get_matrix,guide,issue_activity,issue_usage,list_issues,list_projects,list_usage_ledger,list_usage_requests,next,project_summary,ready_issues,report_verify,set_status,setup,update_issue,usage_missing,usage_report,verify_issue" {
		t.Errorf("ツール: %s", got)
	}

	// 起票（project はヘッダの既定値）→ 詳細 → 一覧 → ready
	text, data := m.call("create_issue", map[string]any{"title": "MCP の要件", "type": "requirement", "priority": "P1", "labels": []string{"mcp"}}, false)
	// 変更の結果には付与の指示が付く（この利用者のフックはまだ届いていない）
	notice := "\nトークン情報が未付与です。次を実行してください: looptrack issue usage attach "
	if text != "作成: REQ-0001 MCP の要件（version 1）"+notice+"REQ-0001" || data["id"] != "REQ-0001" {
		t.Errorf("create_issue: %s %v", text, data)
	}
	m.call("create_issue", map[string]any{"title": "実装", "traces": []string{"REQ-0001"}, "blocked_by": []string{"REQ-0001"}}, false)
	m.call("create_issue", map[string]any{"project": "ex", "title": "EX 側"}, false)
	text, data = m.call("get_issue", map[string]any{"id": "req-0001"}, false)
	if !strings.HasPrefix(text, "version: 1\n\n---\nid: REQ-0001\n") || data["version"] != float64(1) || data["markdown"] == nil {
		t.Errorf("get_issue: %s", text)
	}
	text, data = m.call("list_issues", map[string]any{}, false)
	if !strings.Contains(text, "REQ-0001  requirement Todo        P1  -        MCP の要件") || data["count"] != float64(2) {
		t.Errorf("list_issues: %s", text)
	}
	_, data = m.call("list_issues", map[string]any{"label": "mcp", "sort": "id", "reverse": true}, false)
	if data["count"] != float64(1) {
		t.Errorf("label 絞り込み: %v", data)
	}
	m.call("list_issues", map[string]any{"status": "Doing"}, true)
	m.call("list_issues", map[string]any{"sort": "size"}, true)
	text, _ = m.call("ready_issues", map[string]any{}, false)
	if !strings.Contains(text, "REQ-0001") || strings.Contains(text, "REQ-0002") {
		t.Errorf("ready_issues: %s", text)
	}

	// コメント・状態変更・本文更新（楽観ロック）
	if text, _ = m.call("add_comment", map[string]any{"id": "REQ-0002", "text": "原因: MCP から"}, false); text != "コメント追記: REQ-0002"+notice+"REQ-0002" {
		t.Errorf("add_comment: %s", text)
	}
	text, _ = m.call("set_status", map[string]any{"id": "REQ-0002", "status": "In Progress", "comment": "着手"}, false)
	if text != "REQ-0002: Todo → In Progress\nコメント追記: REQ-0002"+notice+"REQ-0002" {
		t.Errorf("set_status: %s", text)
	}
	_, data = m.call("get_issue", map[string]any{"id": "REQ-0001"}, false)
	md := strings.Replace(data["markdown"].(string), "## 背景\n\n（未記入）", "## 背景\n\nMCP から書いた背景", 1)
	text, _ = m.call("update_issue", map[string]any{"id": "REQ-0001", "version": 1, "markdown": md}, false)
	if text != "更新: REQ-0001（version 2）"+notice+"REQ-0001" {
		t.Errorf("update_issue: %s", text)
	}
	text, _ = m.call("update_issue", map[string]any{"id": "REQ-0001", "version": 1, "title": "古い版から"}, true)
	if !strings.Contains(text, "他で更新されています") || !strings.Contains(text, "現在の version: 2") || !strings.Contains(text, "MCP から書いた背景") {
		t.Errorf("版の不一致: %s", text)
	}
	m.call("update_issue", map[string]any{"id": "REQ-0001", "version": 2, "priority": "P0"}, false)

	// matrix・summary・activity・projects
	text, data = m.call("get_matrix", map[string]any{}, false)
	if !strings.Contains(text, "| REQ-0001 | Todo | MCP の要件 | — | REQ-0002(In Progress) | — |") || data["requirements"] != float64(1) {
		t.Errorf("get_matrix: %s", text)
	}
	text, _ = m.call("get_matrix", map[string]any{"format": "json"}, false)
	if !strings.Contains(text, `"untested"`) {
		t.Errorf("get_matrix json: %s", text)
	}
	text, _ = m.call("project_summary", map[string]any{"limit": 1}, false)
	if !strings.Contains(text, "── 進行中（In Progress） ──\nID ") || !strings.Contains(text, "全 1 件") || !strings.Contains(text, "══ ② 人の判断待ち（In Review 0 件・48 時間超 0 件） ══\n該当なし") {
		t.Errorf("project_summary: %s", text)
	}
	_, data = m.call("issue_activity", map[string]any{"ids": []string{"REQ-0001", "REQ-0002", "NOPE-1"}}, false)
	if items, _ := data["items"].([]any); len(items) != 2 {
		t.Errorf("issue_activity: %v", data)
	}
	text, _ = m.call("list_projects", nil, false)
	if !strings.Contains(text, "ex（") || !strings.Contains(text, "req（") {
		t.Errorf("list_projects: %s", text)
	}

	// プロジェクト別ルール: REST と同じ文言で拒否。上書きできる違反は理由付きで通る
	text, _ = m.call("set_status", map[string]any{"project": "ex", "id": "EX-0001", "status": "Backlog"}, true)
	if text != ruleMessage(t, "forbid_status", "EX-0001", "Backlog") {
		t.Errorf("forbid_status の文言: %s", text)
	}
	m.call("set_status", map[string]any{"id": "EX-0001", "status": "Done", "comment": "完了"}, true)
	m.call("create_issue", map[string]any{"project": "ex", "title": "x", "status": "Done"}, true)
	m.call("create_issue", map[string]any{"project": "ex", "title": "x", "body": "- [ ] 本番へのデプロイを依頼する"}, true)
	m.call("create_issue", map[string]any{"title": "不具合", "type": "bug"}, false)
	m.call("create_issue", map[string]any{"title": "次の実装", "body": verifyBody}, false)
	text, _ = m.call("set_status", map[string]any{"id": "REQ-0004", "status": "Done"}, true)
	if !strings.Contains(text, "verify の記録がありません") || !strings.Contains(text, "REQ-0004") {
		t.Errorf("verify_required_on_close: %s", text)
	}
	m.call("set_status", map[string]any{"id": "REQ-0004", "status": "Done", "override_reason": "利用者指示"}, false)

	// 記録: MCP の変更はすべて via=mcp・セッション ID・トークン付き
	var total, mcpRows, overrides int
	e.db.QueryRow(`SELECT COUNT(*), SUM(via = 'mcp' AND session_id = 'claude-sess' AND token_id IS NOT NULL AND actor_user_id = ?), SUM(kind = 'rule_override')
FROM issue_events`, u.ID).Scan(&total, &mcpRows, &overrides)
	if total == 0 || total != mcpRows || overrides != 1 {
		t.Errorf("events=%d mcp=%d overrides=%d", total, mcpRows, overrides)
	}
	var viaMCP int
	e.db.QueryRow("SELECT COUNT(*) FROM comments WHERE via = 'mcp' AND author_user_id = ?", u.ID).Scan(&viaMCP)
	if viaMCP != 2 {
		t.Errorf("mcp のコメント = %d, want 2", viaMCP)
	}

	// 既定プロジェクトが決まらないときは一覧を示す
	noHeader := e.mcpAs(token, nil)
	if text, _ := noHeader.call("list_issues", map[string]any{}, true); !strings.Contains(text, "ex, req") {
		t.Errorf("project 未指定: %s", text)
	}
}

func TestMCPPermissions(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr, ed := e.rulesProject("req", verifyRules...)
	ed.json(201, "POST", "/projects/req/issues", map[string]any{"title": "a"}, nil)
	e.project("secret")
	adm := e.apiAs(e.adminIn("root", "root-password-1", "secret"))
	adm.json(201, "POST", "/projects/secret/issues", map[string]any{"title": "非公開"}, nil)

	viewer := e.user("vi", "vi-password-1234", "member")
	store.SetMember(ctx, e.db, pr.ID, viewer.ID, "viewer")
	m := e.mcpAs(e.apiAs(viewer).token, nil) // 利用できるプロジェクトが 1 つなら project 省略可
	m.call("list_issues", map[string]any{}, false)
	m.call("get_issue", map[string]any{"id": "REQ-0001"}, false)
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"create_issue", map[string]any{"title": "b"}},
		{"add_comment", map[string]any{"id": "REQ-0001", "text": "b"}},
		{"set_status", map[string]any{"id": "REQ-0001", "status": "Done"}},
		{"update_issue", map[string]any{"id": "REQ-0001", "version": 1, "title": "b"}},
	} {
		if text, _ := m.call(c.tool, c.args, true); !strings.Contains(text, "閲覧のみ") {
			t.Errorf("%s: %s", c.tool, text)
		}
	}
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"get_issue", map[string]any{"id": "SECRET-0001"}},
		{"add_comment", map[string]any{"id": "SECRET-0001", "text": "x"}},
		{"list_issues", map[string]any{"project": "secret"}},
		{"get_matrix", map[string]any{"project": "secret"}},
	} {
		if text, _ := m.call(c.tool, c.args, true); !strings.Contains(text, "見つかりません") || strings.Contains(text, "非公開") {
			t.Errorf("%s: %s", c.tool, text)
		}
	}
	if text, _ := m.call("issue_activity", map[string]any{"ids": []string{"SECRET-0001"}}, false); text != "該当なし" {
		t.Errorf("issue_activity: %s", text)
	}
	var n int
	e.db.QueryRow("SELECT COUNT(*) FROM issue_events WHERE via = 'mcp'").Scan(&n)
	if n != 0 {
		t.Errorf("拒否した変更が記録された: %d", n)
	}
}

// TestMCPResultShape は、ツール結果の本文と通知が content の text に入り、structuredContent を返さないことを確かめる。
// Claude Code は structuredContent があるとそれだけを AI に渡し、text（guide の本文・【導入が未完了】などの通知）を捨てる。
// 構造化データは _meta（mcpDataMeta）に載せる。出力スキーマは宣言しない（宣言すると structuredContent が必須になる）。
func TestMCPResultShape(t *testing.T) {
	e, _, ed := newAPIEnv(t)
	ctx := context.Background()
	m := e.mcpAsClient(ed.token, map[string]string{"X-Looptrack-Project": "req"}, "claude-code", "2.1.274", "")

	tools, err := m.cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.OutputSchema != nil {
			t.Errorf("%s: 出力スキーマを宣言している", tool.Name)
		}
	}

	raw := func(name string, args map[string]any) (map[string]any, []string) {
		t.Helper()
		res, err := m.cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || res.IsError {
			t.Fatalf("%s: %v %v", name, err, res)
		}
		b, _ := json.Marshal(res)
		var wire map[string]any
		json.Unmarshal(b, &wire)
		if _, ok := wire["structuredContent"]; ok {
			t.Errorf("%s: structuredContent がある: %s", name, b)
		}
		var texts []string
		for _, c := range res.Content {
			tc, ok := c.(*mcp.TextContent)
			if !ok {
				t.Fatalf("%s: text でない content: %T", name, c)
			}
			texts = append(texts, tc.Text)
		}
		meta, _ := wire["_meta"].(map[string]any)
		data, _ := meta[mcpDataMeta].(map[string]any)
		return data, texts
	}

	// guide: 本文（Markdown）が 1 つ目の text。導入の指示は最後の text。構造化データは _meta
	data, texts := raw("guide", map[string]any{})
	if len(texts) != 2 || !strings.HasPrefix(texts[0], "# イシュー管理の使い方 — req（req）") || !strings.Contains(texts[1], "【導入が未完了】Claude Code") {
		t.Errorf("guide の text: %q", texts)
	}
	if data["project"] != "req" {
		t.Errorf("guide の _meta: %v", data)
	}

	// list_issues: 表と導入の指示が text に入る
	ed.json(201, "POST", "/projects/req/issues", map[string]any{"title": "一覧に出る"}, nil)
	data, texts = raw("list_issues", map[string]any{})
	if len(texts) != 2 || !strings.Contains(texts[0], "REQ-0001") || !strings.HasPrefix(texts[1], "【導入が未完了】") {
		t.Errorf("list_issues の text: %q", texts)
	}
	if data["count"] != float64(1) {
		t.Errorf("list_issues の _meta: %v", data)
	}

	// 変更の結果: 付与の指示（トークン情報が未付与です）が text に入る
	_, texts = raw("create_issue", map[string]any{"title": "起票"})
	if len(texts) == 0 || !strings.HasPrefix(texts[0], "作成: REQ-0002 起票") || !strings.Contains(texts[0], "トークン情報が未付与です") {
		t.Errorf("create_issue の text: %q", texts)
	}
}

// TestMCPDescriptionsHaveNoRealSlugs は、ツール・prompts の説明と接続時の instructions に実在のプロジェクトの slug
// （社内のもの）が無いことを確かめる（説明の例にした実在のプロジェクトの slug を、AI がそのまま project に渡して失敗した。公開の観点でも出さない）。
func TestMCPDescriptionsHaveNoRealSlugs(t *testing.T) {
	e, _, ed := newAPIEnv(t)
	m := e.mcpAs(ed.token, map[string]string{"X-Looptrack-Project": "req"})
	var all []string
	tools, err := m.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools.Tools {
		b, _ := json.Marshal(tl) // 説明と入力の schema（引数の説明）
		all = append(all, tl.Name+": "+string(b))
	}
	prompts, err := m.cs.ListPrompts(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range prompts.Prompts {
		b, _ := json.Marshal(p)
		all = append(all, "prompt "+p.Name+": "+string(b))
	}
	if ir := m.cs.InitializeResult(); ir != nil {
		all = append(all, "instructions: "+ir.Instructions)
	}
	all = append(all, "instructions(const ja): "+mcpInstructions(i18n.JA), "instructions(const en): "+mcpInstructions(i18n.EN))
	// 社内のプロジェクトの slug（deploy/rules・docs/projects にあるもの）。英数字・「-」・「_」に挟まれていない語として探す
	// 語は分けて書く（このファイル自身が公開物の検査（deploy/public-scan.sh）に掛からないように）
	slug := regexp.MustCompile(`(?i)(^|[^a-z0-9_-])(` + "req" + "weave|h" + "pc|im|dev" + "-infra" + `)([^a-z0-9_-]|$)`)
	for _, s := range all {
		if loc := slug.FindStringIndex(s); loc != nil {
			t.Errorf("実在のプロジェクトの slug が説明にある: …%s…", s[max(0, loc[0]-60):min(len(s), loc[1]+60)])
		}
	}
	// project 引数の説明は「通常は省略する」を先に書く
	for _, tl := range tools.Tools {
		b, _ := json.Marshal(tl.InputSchema)
		var schema struct {
			Properties map[string]struct {
				Description string `json:"description"`
			} `json:"properties"`
		}
		json.Unmarshal(b, &schema)
		if p, ok := schema.Properties["project"]; ok && !strings.HasPrefix(p.Description, "通常は省略する") {
			t.Errorf("%s の project 引数の説明: %q", tl.Name, p.Description)
		}
	}
}

// TestMCPSessionFromConnection: MCP の接続設定はヘッダを持てないので、X-Looptrack-Session が無いときは
// サーバが initialize で発行した接続 ID（Mcp-Session-Id = mcp_connections.id）でセッションを見分ける。
// 同じ利用者の別の接続から next を呼んでも、相手の着手中を横取りせず、注記でそれと分かる。
func TestMCPSessionFromConnection(t *testing.T) {
	e, _, ed := newAPIEnv(t)
	ed.json(201, "POST", "/projects/req/issues", map[string]any{"title": "先に着手される", "priority": "P0"}, nil)
	ed.json(201, "POST", "/projects/req/issues", map[string]any{"title": "まだ空いている", "priority": "P1"}, nil)

	// 接続 1（X-Looptrack-Session は送らない）が next で REQ-0001 を着手する
	m1 := e.mcpAs(ed.token, map[string]string{"X-Looptrack-Project": "req"})
	if text, _ := m1.call("next", map[string]any{}, false); !strings.Contains(text, "REQ-0001") {
		t.Fatalf("接続 1 の next: %s", text)
	}
	// 接続 ID が issue_events.session_id に入る（空でない・接続の記録と同じ値）
	var sid string
	if err := e.db.QueryRow(`SELECT COALESCE(e.session_id, '') FROM issue_events e JOIN issues i ON i.id = e.issue_id
WHERE i.display_id = 'REQ-0001' AND e.kind = 'status' ORDER BY e.id DESC LIMIT 1`).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	// セッション ID は「印 + 接続 ID」（印はクライアントが名乗るセッション ID と混ぜて比べないため）
	var known int
	e.db.QueryRow(`SELECT COUNT(*) FROM mcp_connections WHERE id = ?`, strings.TrimPrefix(sid, service.MCPSessionPrefix)).Scan(&known)
	if !strings.HasPrefix(sid, service.MCPSessionPrefix) || known != 1 {
		t.Fatalf("接続 ID がセッション ID にならない: session_id=%q mcp_connections=%d", sid, known)
	}

	// 接続 2（同じ利用者・同じトークン）は REQ-0001 を resumed にせず、空いている REQ-0002 を始める
	m2 := e.mcpAs(ed.token, map[string]string{"X-Looptrack-Project": "req"})
	text, data := m2.call("next", map[string]any{}, false)
	if data["action"] != "started" || !strings.Contains(text, "REQ-0002") {
		t.Errorf("接続 2 の next: %s %v", text, data)
	}
	if !strings.Contains(text, "REQ-0001 は別のセッションが着手しています") {
		t.Errorf("接続 2 の next に注記が無い:\n%s", text)
	}
	// list_issues と project_summary にも印が出る（一覧の注記は ID に経過時間が付く: 「REQ-0001（0分） は…」）
	if text, _ := m2.call("list_issues", map[string]any{}, false); !strings.Contains(text, "REQ-0001（") ||
		!strings.Contains(text, "は別のセッションが着手しています") {
		t.Errorf("list_issues の注記:\n%s", text)
	}
	if text, _ := m2.call("project_summary", map[string]any{}, false); !strings.Contains(text, "REQ-0001") {
		t.Errorf("project_summary:\n%s", text)
	}
}
