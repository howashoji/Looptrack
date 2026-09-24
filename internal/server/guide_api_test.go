package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/guide"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/kit"
)

// guide が共通規則 + プロジェクト別ルール + 運用文書を 1 回で返し、next が規則どおりに着手する。
// REST・MCP・CLI で同じ結果になる。

func TestGuide(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ex, h := e.rulesProject("ex")

	var g struct {
		Project  string       `json:"project"`
		Markdown string       `json:"markdown"`
		Rules    []guide.Rule `json:"rules"`
		Doc      string       `json:"doc"`
	}
	h.json(200, "GET", "/projects/ex/guide", nil, &g)
	for _, want := range []string{"# イシュー管理の使い方 — ex（ex）", "ID は `EX-0001` 形式。あなたの権限: editor", "## 1. 共通規則", "looptrack issue next",
		"## 2. このプロジェクトのルール", "状態 Backlog は使わない", "（`require_comment_before`）", "「動作確認」を含める",
		"受け入れ条件のチェックボックスに",
		"Done / Canceled にするとき、その会話のトークン情報", "ブランチ名から /CASE-\\d+/ で案件を決める", "「## 検証コマンド」節を持つイシューを Done にするとき",
		"「## 受け入れ条件」節を持つイシューを Done にするとき",
		"## 3. このプロジェクトの運用文書", "（未登録。管理者が `looptrack project guide set ex <ファイル>` で登録する"} {
		if !strings.Contains(g.Markdown, want) {
			t.Errorf("guide に %q が無い:\n%s", want, g.Markdown)
		}
	}
	if len(g.Rules) != 7 || g.Doc != "" { // 例は全 7 種のルールを持つ
		t.Errorf("rules=%v doc=%q", g.Rules, g.Doc)
	}

	// 運用文書を登録すると 3 節に入る（見出しは 2 段下がる・コードブロックの中は変えない）
	doc := "# EX の運用\n\n## ラベル\n\n案件名はラベルに入れる。\n\n```bash\n# コメント行\n```\n"
	if err := store.SetProjectGuide(ctx, e.db, ex.ID, doc, "docs/projects/example.md"); err != nil {
		t.Fatal(err)
	}
	_, md := e.get(h.c, "/im/api/v1/projects/ex/guide?format=md", "Authorization", "Bearer "+h.token)
	for _, want := range []string{"> 登録元: docs/projects/example.md（更新 ", "### EX の運用", "#### ラベル", "```bash\n# コメント行\n```"} {
		if !strings.Contains(md, want) {
			t.Errorf("format=md に %q が無い:\n%s", want, md)
		}
	}

	// MCP の guide ツールは同じ Markdown を返す
	m := e.mcpAs(h.token, map[string]string{"X-Looptrack-Project": "ex"})
	text, data := m.call("guide", map[string]any{}, false)
	if text != md || data["doc_source"] != "docs/projects/example.md" {
		t.Errorf("MCP guide が REST と違う:\n%s", text)
	}

	// 登録を消す・権限の無いプロジェクトは 404
	store.SetProjectGuide(ctx, e.db, ex.ID, "", "")
	h.json(200, "GET", "/projects/ex/guide", nil, &g)
	if g.Doc != "" {
		t.Errorf("clear 後も doc がある: %q", g.Doc)
	}
	e.project("other")
	h.fail(404, "GET", "/projects/other/guide", nil)
}

func TestGuideUnknownRuleShownAsJSON(t *testing.T) {
	rules := guide.DescribeRules(i18n.JA, json.RawMessage(`{"later_rule":{"x":true},"zzz_rule":{}}`))
	if len(rules) != 2 || rules[0].Name != "later_rule" || rules[0].Description != `設定: {"x":true}` ||
		rules[1].Name != "zzz_rule" || rules[1].Description != "設定: {}" {
		t.Errorf("%+v", rules)
	}
	// usage は説明にする。中身の無い usage は JSON のまま
	rules = guide.DescribeRules(i18n.JA, json.RawMessage(`{"usage":{"require_on_close":true,"case_pattern":"X-\\d+"}}`))
	if len(rules) != 1 || !strings.Contains(rules[0].Description, "Done / Canceled にするとき") || !strings.Contains(rules[0].Description, "/X-\\d+/ で案件を決める") {
		t.Errorf("usage: %+v", rules)
	}
	if rules = guide.DescribeRules(i18n.JA, json.RawMessage(`{"usage":{}}`)); len(rules) != 1 || rules[0].Description != "設定: {}" {
		t.Errorf("空の usage: %+v", rules)
	}
}

type nextResp struct {
	Action     string              `json:"action"`
	Message    string              `json:"message"`
	Issue      *issueJSON          `json:"issue"`
	From       string              `json:"from"`
	Acceptance string              `json:"acceptance"`
	Related    *relatedJSON        `json:"related"`
	Others     []string            `json:"others"`
	Skipped    []map[string]string `json:"skipped"`
	Text       string              `json:"text"`
	// OtherSessions は同じ利用者の別のセッションが着手中のもの
	OtherSessions []string `json:"other_sessions"`
	// CrossPathSessions は別の経路（CLI / MCP）で着手されたもの（着手中としては返る）
	CrossPathSessions []string `json:"cross_path_sessions"`
}

func TestNext(t *testing.T) {
	e, pr, ed := newAPIEnv(t)
	ctx := context.Background()
	create := func(body map[string]any) {
		t.Helper()
		ed.json(201, "POST", "/projects/req/issues", body, nil)
	}
	create(map[string]any{"title": "要件", "type": "requirement", "priority": "P1"})                                 // 0001: 子（0005）が未クローズ → 見送り
	create(map[string]any{"title": "実装 A", "priority": "P2", "traces": []string{"REQ-0001"}, "parent": "REQ-0001", // 0002
		"body": "作る。\n\n## 受け入れ条件\n\n- [ ] 動く\n- [ ] テストが通る"})
	create(map[string]any{"title": "待ち", "priority": "P0", "blocked_by": []string{"REQ-0002"}}) // 0003: blocked
	create(map[string]any{"title": "大枠", "type": "epic", "priority": "P0"})                     // 0004: epic は既定で対象外
	create(map[string]any{"title": "子", "priority": "P3", "parent": "REQ-0001"})                // 0005
	create(map[string]any{"title": "後回し", "priority": "P0", "status": "Backlog"})               // 0006: Backlog は対象外

	var r nextResp
	// dry-run は状態を変えない
	ed.header["X-Looptrack-Session"] = "sess-1"
	ed.json(200, "POST", "/projects/req/next", map[string]any{"dry_run": true}, &r)
	if r.Action != "would_start" || r.Issue.ID != "REQ-0002" || r.Issue.Status != "Todo" || r.From != "Todo" {
		t.Fatalf("dry-run: %+v", r)
	}
	// 着手: 本文・受け入れ条件・関連
	ed.json(200, "POST", "/projects/req/next", map[string]any{}, &r)
	if r.Action != "started" || r.Issue.ID != "REQ-0002" || r.Issue.Status != "In Progress" || r.Acceptance != "- [ ] 動く\n- [ ] テストが通る" {
		t.Fatalf("started: %+v", r)
	}
	if r.Related.Parent == nil || r.Related.Parent.ID != "REQ-0001" || len(r.Related.Traces) != 1 || r.Related.Traces[0].Status != "Todo" {
		t.Errorf("related: %+v", r.Related)
	}
	for _, want := range []string{"着手: REQ-0002: Todo → In Progress（実装 A）", "親: REQ-0001 [requirement・Todo] 要件", "── 受け入れ条件 ──\n- [ ] 動く",
		"── 全文（version 2） ──\n---\nid: REQ-0002", "looptrack issue close REQ-0002 --comment"} {
		if !strings.Contains(r.Text, want) {
			t.Errorf("text に %q が無い:\n%s", want, r.Text)
		}
	}
	var n int
	e.db.QueryRow("SELECT COUNT(*) FROM issue_events WHERE kind = 'status' AND session_id = 'sess-1'").Scan(&n)
	if n != 1 {
		t.Errorf("状態変更のイベント（セッションつき）= %d", n)
	}
	// 同じセッションで再実行すると着手中のものを返す（状態・版は変わらない）
	ed.json(200, "POST", "/projects/req/next", map[string]any{}, &r)
	if r.Action != "resumed" || r.Issue.ID != "REQ-0002" || r.Issue.Version != 2 {
		t.Errorf("resumed: %+v", r)
	}
	// 別のセッション（並行する別の AI）は横取りせず、次の候補（REQ-0005）に着手する
	ed.header["X-Looptrack-Session"] = "sess-2"
	ed.json(200, "POST", "/projects/req/next", map[string]any{}, &r)
	if r.Action != "started" || r.Issue.ID != "REQ-0005" {
		t.Errorf("別セッション: %+v", r)
	}
	// セッションの無い呼び出し（MCP 等）は利用者単位: 優先度順の先頭を返し、残りを others に
	delete(ed.header, "X-Looptrack-Session")
	ed.json(200, "POST", "/projects/req/next", map[string]any{}, &r)
	if r.Action != "resumed" || r.Issue.ID != "REQ-0002" || strings.Join(r.Others, ",") != "REQ-0005" {
		t.Errorf("利用者単位: %+v", r)
	}
	// 候補が無い
	ed.header["X-Looptrack-Session"] = "sess-3"
	ed.json(200, "POST", "/projects/req/next", map[string]any{}, &r)
	if r.Action != "none" || r.Issue != nil || !strings.Contains(r.Text, "着手可能なイシューはありません") {
		t.Errorf("none: %+v", r)
	}
	// types で epic も対象にできる。不正な型は 400
	ed.json(200, "POST", "/projects/req/next", map[string]any{"dry_run": true, "types": []string{"epic"}}, &r)
	if r.Action != "would_start" || r.Issue.ID != "REQ-0004" {
		t.Errorf("types=epic: %+v", r)
	}
	ed.fail(400, "POST", "/projects/req/next", map[string]any{"types": []string{"story"}})

	// 閲覧のみの権限は 403
	v := e.user("viewer", "viewer-password-1", "member")
	store.SetMember(ctx, e.db, pr.ID, v.ID, "viewer")
	e.apiAs(v).fail(403, "POST", "/projects/req/next", map[string]any{"dry_run": true})
}

func TestNextRules(t *testing.T) {
	e := newEnv(t)
	_, h := e.rulesProject("ex", statusRules...)
	_, r := e.rulesProject("req", verifyRules...)
	var res nextResp

	// コメント必須: コメント 0 件では In Progress にできない → 422 で --comment を案内。コメントを付ければ着手できる
	h.json(201, "POST", "/projects/ex/issues", map[string]any{"title": "a"}, nil)
	err := h.fail(422, "POST", "/projects/ex/next", map[string]any{})
	if err.Error.Rule != "require_comment_before" || !strings.Contains(err.Error.Message, "looptrack issue next --comment") {
		t.Errorf("コメント必須: %+v", err)
	}
	h.json(200, "POST", "/projects/ex/next", map[string]any{"comment": "着手。方針: …"}, &res)
	if res.Action != "started" || res.Issue.ID != "EX-0001" {
		t.Errorf("comment つき: %+v", res)
	}

	// 着手の段階に掛かるルールが無ければ、優先度の順に選ばれ見送りは出ない
	r.json(201, "POST", "/projects/req/issues", map[string]any{"title": "task", "priority": "P0"}, nil)
	r.json(201, "POST", "/projects/req/issues", map[string]any{"title": "bug", "type": "bug", "priority": "P3"}, nil)
	r.json(200, "POST", "/projects/req/next", map[string]any{"dry_run": true}, &res)
	if res.Action != "would_start" || res.Issue.ID != "REQ-0001" || len(res.Skipped) != 0 {
		t.Errorf("next: %+v", res)
	}
	// MCP の next も同じ判定（dry_run）
	m := e.mcpAs(r.token, map[string]string{"X-Looptrack-Project": "req"})
	text, data := m.call("next", map[string]any{"dry_run": true}, false)
	if data["action"] != "would_start" || !strings.Contains(text, "次に着手するもの: REQ-0001 task") || strings.Contains(text, "見送り") {
		t.Errorf("MCP next: %s", text)
	}
	text, _ = m.call("next", map[string]any{}, false)
	if !strings.HasPrefix(text, "着手: REQ-0001: Todo → In Progress") {
		t.Errorf("MCP next 着手: %s", text)
	}
}

func TestDist(t *testing.T) {
	_, _, ed := newAPIEnv(t)
	var list struct {
		Files []distFileJSON `json:"files"`
	}
	ed.json(200, "GET", "/dist", nil, &list)
	if len(list.Files) != len(kit.Names()) {
		t.Fatalf("%+v", list)
	}
	names := map[string]bool{}
	for _, f := range list.Files {
		names[f.Name] = true
		// kit の名前（「/」を含む）はそのままのパスでも、1 セグメントにエンコードしても取れる
		for _, p := range []string{"/dist/" + f.Name, "/dist/" + url.PathEscape(f.Name)} {
			code, h, body := ed.do("GET", p, nil)
			sum := sha256.Sum256(body)
			if code != 200 || hex.EncodeToString(sum[:]) != f.SHA256 || h.Get("X-Looptrack-SHA256") != f.SHA256 || len(body) != f.Size {
				t.Errorf("%s: %d %s", p, code, h.Get("X-Looptrack-SHA256"))
			}
		}
	}
	// kit/core の skill /issue が一覧に載る（サーバ経由の導入で置ける）。
	// 言行一致の Stop hook は廃止したので載らない
	if !names["kit/core/skills/issue/SKILL.md"] {
		t.Errorf("一覧に kit/core/skills/issue/SKILL.md が無い")
	}
	if names["kit/core/hooks/stop-verbal-action-mismatch.sh"] {
		t.Errorf("廃止した言行一致の hook が一覧に載っている")
	}
	if code, h, _ := ed.do("GET", "/dist/kit/core/skills/issue/SKILL.md", nil); code != 200 || !strings.HasPrefix(h.Get("Content-Type"), "text/markdown") {
		t.Errorf("SKILL.md の Content-Type: %d %s", code, h.Get("Content-Type"))
	}
	for _, p := range []string{"/dist/legacy-cli", "/dist/legacy-cli-test", "/dist/kit/README.md", "/dist/kit/embed.go", "/dist/kit%2FREADME.md", "/dist/core/skills/issue/SKILL.md", "/dist/kit/core/../README.md"} {
		if code, _, body := ed.do("GET", p, nil); code == 200 { // 404（一覧に無い）か、「..」はパスの正規化のリダイレクト
			t.Errorf("%s: %d %s", p, code, body)
		}
	}
}

// TestGuideAndNextViaCLI は looptrack issue guide / next が REST の結果をそのまま出すことを確かめる。
func TestGuideAndNextViaCLI(t *testing.T) {
	e, _, ed := newAPIEnv(t)
	ed.json(201, "POST", "/projects/req/issues", map[string]any{"title": "CLI から着手"}, nil)
	run := issueCLI(t, e, "req", ed.token, "LOOPTRACK_USAGE=0", "CLAUDE_CODE_SESSION_ID=cli-sess")
	_, md := e.get(ed.c, "/im/api/v1/projects/req/guide?format=md", "Authorization", "Bearer "+ed.token)
	if res := run("guide"); res.code != 0 || res.stdout != md {
		t.Errorf("guide: %d %s\n%s", res.code, res.stderr, res.stdout)
	}
	if res := run("next", "--dry-run"); res.code != 0 || !strings.HasPrefix(res.stdout, "次に着手するもの: REQ-0001 CLI から着手") {
		t.Errorf("next --dry-run: %d %s %s", res.code, res.stdout, res.stderr)
	}
	res := run("next", "--json")
	var r nextResp
	if res.code != 0 || json.Unmarshal([]byte(res.stdout), &r) != nil || r.Action != "started" || r.Issue.ID != "REQ-0001" {
		t.Errorf("next --json: %d %s %s", res.code, res.stdout, res.stderr)
	}
	if res := run("next"); res.code != 0 || !strings.HasPrefix(res.stdout, "着手中: REQ-0001") {
		t.Errorf("next 再実行: %d %s %s", res.code, res.stdout, res.stderr)
	}
	if res := run("next", "--type", "story"); res.code != 1 || !strings.HasPrefix(res.stderr, "エラー: type は") {
		t.Errorf("next --type story: %d %s", res.code, res.stderr)
	}
}

// TestInitGuideNextClose は「新しいプロジェクトで init → guide → next → close まで、ADD-PROJECT.md の手作業なしで通る」ことを
// 一時ディレクトリで確かめる。init が書いた settings.json の env と looptrack だけで 1 周を回す。
func TestInitGuideNextClose(t *testing.T) {
	e, _, ed := newAPIEnv(t)
	proj, home := t.TempDir(), t.TempDir()
	run := func(env []string, args ...string) cliResult {
		t.Helper()
		return runCLI(t, proj, append(cliHomeEnv(home), env...), "", args...)
	}
	if res := run(nil, "issue", "init", "--project", "req", "--url", e.srv.URL+"/im", "--agent", "claude-code", "--no-loop"); res.code != 0 {
		t.Fatalf("init: %s %s", res.stdout, res.stderr)
	}
	var settings struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal([]byte(mustRead(t, filepath.Join(proj, ".claude", "settings.json"))), &settings); err != nil {
		t.Fatal(err)
	}
	if settings.Env["LOOPTRACK_API_URL"] != e.srv.URL+"/im" || settings.Env["LOOPTRACK_PROJECT"] != "req" {
		t.Fatalf("settings.json の env: %v", settings.Env)
	}
	// Claude Code と同じく settings.json の env を渡す（トークンは利用者の login の代わりに LOOPTRACK_TOKEN）
	env := []string{"CLAUDE_PROJECT_DIR=" + proj, "LOOPTRACK_TOKEN=" + ed.token, "LOOPTRACK_USAGE=0"}
	for k, v := range settings.Env {
		env = append(env, k+"="+v)
	}
	if res := run(env, "issue", "guide"); res.code != 0 || !strings.Contains(res.stdout, "# イシュー管理の使い方 — req（req）") {
		t.Fatalf("guide: %d %s %s", res.code, res.stdout, res.stderr)
	}
	run(env, "issue", "new", "最初の作業")
	if res := run(env, "issue", "next"); res.code != 0 || !strings.HasPrefix(res.stdout, "着手: REQ-0001: Todo → In Progress") {
		t.Fatalf("next: %d %s %s", res.code, res.stdout, res.stderr)
	}
	if res := run(env, "issue", "close", "REQ-0001", "--comment", "受け入れ条件を検証"); res.code != 0 || !strings.HasPrefix(res.stdout, "REQ-0001: In Progress → Done") {
		t.Fatalf("close: %d %s %s", res.code, res.stdout, res.stderr)
	}
	if res := run(env, "issue", "next"); res.code != 0 || !strings.HasPrefix(res.stdout, "着手可能なイシューはありません") {
		t.Errorf("次の周: %d %s %s", res.code, res.stdout, res.stderr)
	}
	// SessionStart の hook（looptrack hook summary）も init の配線どおりに動く
	res := run(env, "hook", "summary", "--agent", "claude-code")
	if res.code != 0 || !strings.Contains(res.stdout, "未クローズ 0 件") {
		t.Errorf("hook summary: %d %s %s", res.code, res.stdout, res.stderr)
	}
	var hooks string
	for _, f := range []string{"settings.json", "settings.local.json"} {
		if b, err := os.ReadFile(filepath.Join(proj, ".claude", f)); err == nil {
			hooks += string(b)
		}
	}
	if !strings.Contains(hooks, "SessionStart") || !strings.Contains(hooks, "hook summary") {
		t.Errorf("init が SessionStart の hook（looptrack hook summary）を配線していない:\n%s", hooks)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// guide は要求の言語で出る（REST は Accept-Language、MCP は接続の言語）。
// composeGuide への reqLang(r) / c.lang の配線を外すと、英語を頼んでも日本語が返ってここが落ちる。
func TestGuideLanguage(t *testing.T) {
	e := newEnv(t)
	_, h := e.rulesProject("lg")

	// 日本語: テストの client() が Accept-Language: ja を付ける。英語: 要求ごとに上書きする
	// （headerTransport は要求が自分で付けた見出しを上書きしない）。
	_, ja := e.get(h.c, "/im/api/v1/projects/lg/guide?format=md", "Authorization", "Bearer "+h.token)
	_, en := e.get(h.c, "/im/api/v1/projects/lg/guide?format=md", "Authorization", "Bearer "+h.token, "Accept-Language", "en")
	if ja == en {
		t.Fatal("REST の guide が Accept-Language で変わらない（reqLang(r) が composeGuide へ渡っていない）")
	}
	for _, want := range []string{"# イシュー管理の使い方 — lg（lg）", "## 1. 共通規則", "### このシステムで何が整うか"} {
		if !strings.Contains(ja, want) {
			t.Errorf("REST ja に %q が無い:\n%s", want, ja)
		}
	}
	for _, want := range []string{"# How to use issue management — lg (lg)", "## 1. Common rules", "### What this system puts in place"} {
		if !strings.Contains(en, want) {
			t.Errorf("REST en に %q が無い:\n%s", want, en)
		}
	}
	// 英語版に日本語の**文面**が残っていないこと（ルールの設定値（keyword「動作確認」・env の正規表現）は
	// プロジェクトが登録した日本語なので、そのまま出るのが正しい。ここでは guide 自身の文面だけを見る）。
	for _, ng := range []string{"共通規則", "このプロジェクトのルール", "### 前提", "設定: "} {
		if strings.Contains(en, ng) {
			t.Errorf("REST en に日本語の文面 %q が混ざっている:\n%s", ng, en)
		}
	}

	// MCP: 接続の言語（Accept-Language）で選ぶ。REST と同じ Markdown になる
	mja := e.mcpAs(h.token, map[string]string{"X-Looptrack-Project": "lg"})
	if text, _ := mja.call("guide", map[string]any{}, false); text != ja {
		t.Errorf("MCP ja が REST ja と違う:\n%s", text)
	}
	men := e.mcpAs(h.token, map[string]string{"X-Looptrack-Project": "lg", "Accept-Language": "en"})
	if text, _ := men.call("guide", map[string]any{}, false); text != en {
		t.Errorf("MCP en が REST en と違う（c.lang が composeGuide へ渡っていない）:\n%s", text)
	}

	// MCP の instructions も接続の言語で変わる（言語ごとの *mcp.Server を getServer が選ぶ）。
	cja := e.mcpAsClient(h.token, map[string]string{"X-Looptrack-Project": "lg"}, "claude-code", "1", "2025-06-18")
	cen := e.mcpAsClient(h.token, map[string]string{"X-Looptrack-Project": "lg", "Accept-Language": "en"}, "claude-code", "1", "2025-06-18")
	gotJA, gotEN := cja.cs.InitializeResult().Instructions, cen.cs.InitializeResult().Instructions
	if gotJA != mcpInstructionsJA {
		t.Errorf("ja の instructions が日本語の定数と違う:\n%s", gotJA)
	}
	if gotEN != mcpInstructionsEN {
		t.Errorf("en の instructions が英語の定数と違う:\n%s", gotEN)
	}
}
