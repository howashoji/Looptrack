package server

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/deploy"
	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/store"
)

// プロジェクト別ルールが API と CLI のどちらでも同じように強制されることを確かめる。

// ルールの設定は公開物の例（deploy/rules/example.json）から取る。keys を渡すと、その最上位のキー（ルール）だけを持つ
// プロジェクトにする。例は全部の種類のルールを持つので、確かめるルールに関係しないもの（クローズ時のトークン情報など）を外すため。
var (
	statusRules = []string{"forbid_status", "require_comment_before", "done_requires_keyword", "forbid_checkbox_pattern"} // 状態と本文の規則
	verifyRules = []string{"verify"}                                                                                      // 上書きできるルール（検証コマンドの記録）
)

func (e *env) rulesProject(slug string, keys ...string) (store.Project, *apiClient) {
	e.t.Helper()
	ctx := context.Background()
	pr := e.project(slug)
	raw, err := deploy.Rules.ReadFile("rules/example.json")
	if err != nil {
		e.t.Fatal(err)
	}
	if len(keys) > 0 {
		var all map[string]json.RawMessage
		if err := json.Unmarshal(raw, &all); err != nil {
			e.t.Fatal(err)
		}
		sub := map[string]json.RawMessage{}
		for _, k := range keys {
			if all[k] == nil {
				e.t.Fatalf("rules/example.json に %s が無い", k)
			}
			sub[k] = all[k]
		}
		raw, _ = json.Marshal(sub)
	}
	var compact bytes.Buffer
	json.Compact(&compact, raw)
	if err := store.SetRules(ctx, e.db, pr.ID, compact.Bytes()); err != nil {
		e.t.Fatal(err)
	}
	u := e.user(slug+"-ed", "editor-password-1", "member")
	store.SetMember(ctx, e.db, pr.ID, u.ID, "editor")
	return pr, e.apiAs(u)
}

// ruleMessage は例のルール（deploy/rules/example.json）の rule.message に {id} {status} を入れた文言。
func ruleMessage(t *testing.T, rule, id, status string) string {
	raw, _ := deploy.Rules.ReadFile("rules/example.json")
	var m map[string]map[string]any
	json.Unmarshal(raw, &m)
	msg, _ := m[rule]["message"].(string)
	if msg == "" {
		t.Fatalf("rules/example.json に %s.message が無い", rule)
	}
	return strings.NewReplacer("{id}", id, "{status}", status).Replace(msg)
}

type issueState struct {
	version, comments int
	status            string
}

func (a *apiClient) state(id string) issueState {
	var d issueDetailJSON
	a.json(200, "GET", "/issues/"+id, nil, &d)
	return issueState{d.Version, len(d.Comments), d.Status}
}

func (a *apiClient) rule(want string, method, path string, body any) apiErr {
	a.e.t.Helper()
	e := a.fail(422, method, path, body)
	if e.Error.Code != "rule_violation" || e.Error.Rule != want {
		a.e.t.Errorf("%s %s: code=%s rule=%s, want %s", method, path, e.Error.Code, e.Error.Rule, want)
	}
	return e
}

func TestRulesStatus(t *testing.T) {
	e := newEnv(t)
	pr, a := e.rulesProject("ex", statusRules...)
	var created struct {
		Issue issueDetailJSON `json:"issue"`
	}
	a.json(201, "POST", "/projects/ex/issues", map[string]any{"title": "a"}, &created)
	id := created.Issue.ID

	// 1. Backlog への起票・遷移は拒否（ルールの文言のまま）
	a.rule("forbid_status", "POST", "/projects/ex/issues", map[string]any{"title": "b", "status": "Backlog"})
	got := a.rule("forbid_status", "POST", "/issues/"+id+"/status", map[string]any{"status": "Backlog", "comment": "動作確認: PASS"})
	if got.Error.Message != ruleMessage(t, "forbid_status", id, "Backlog") {
		t.Errorf("文言がルールと違う: %s", got.Error.Message)
	}

	// 2. コメント 0 件・同時コメントなしの In Progress / Done は拒否、同時コメント付きなら通る
	before := a.state(id)
	got = a.rule("require_comment_before", "POST", "/issues/"+id+"/status", map[string]any{"status": "In Progress"})
	if got.Error.Message != ruleMessage(t, "require_comment_before", id, "In Progress") {
		t.Errorf("文言がルールと違う: %s", got.Error.Message)
	}
	a.rule("done_requires_keyword", "POST", "/issues/"+id+"/status", map[string]any{"status": "Done"}) // キーワードの判定が先
	a.rule("require_comment_before", "POST", "/projects/ex/issues", map[string]any{"title": "c", "status": "In Progress"})
	if after := a.state(id); after != before {
		t.Errorf("拒否した変更で状態が変わった: %+v → %+v", before, after)
	}
	a.json(200, "POST", "/issues/"+id+"/status", map[string]any{"status": "In Progress", "comment": "着手"}, nil)

	// 3. Done は既存または同時コメントに「動作確認」が必要（起票も含む）
	a.rule("done_requires_keyword", "POST", "/issues/"+id+"/status", map[string]any{"status": "Done", "comment": "単体テスト PASS"})
	got = a.rule("done_requires_keyword", "POST", "/projects/ex/issues", map[string]any{"title": "d", "status": "Done"})
	if !strings.Contains(got.Error.Message, "「Done」で起票することはできません") {
		t.Errorf("起票時の文言: %s", got.Error.Message)
	}
	a.json(200, "POST", "/issues/"+id+"/status", map[string]any{"status": "Done", "comment": "単体 PASS / 動作確認: PASS"}, nil)
	a.json(201, "POST", "/projects/ex/issues", map[string]any{"title": "e"}, &created)
	id2 := created.Issue.ID
	a.json(201, "POST", "/issues/"+id2+"/comments", map[string]any{"text": "動作確認: 対象外（設計のみ）"}, nil)
	a.json(200, "POST", "/issues/"+id2+"/status", map[string]any{"status": "Done"}, nil) // 既存コメントのキーワード
	a.json(200, "POST", "/issues/"+id2+"/status", map[string]any{"status": "Todo"}, nil) // 対象外の状態は自由

	// 4. 受け入れ条件のチェックボックスに反映依頼を書かせない（起票の本文・コメント・全文更新）
	bad := "- [ ] test / staging / 本番への定義反映をユーザーへ依頼した"
	a.rule("forbid_checkbox_pattern", "POST", "/projects/ex/issues", map[string]any{"title": "f", "body": bad})
	a.rule("forbid_checkbox_pattern", "POST", "/issues/"+id2+"/comments", map[string]any{"text": bad})
	a.rule("forbid_checkbox_pattern", "POST", "/issues/"+id2+"/status", map[string]any{"status": "In Progress", "comment": bad})
	var d issueDetailJSON
	a.json(200, "GET", "/issues/"+id2, nil, &d)
	a.fail(422, "PATCH", "/issues/"+id2, map[string]any{"markdown": strings.Replace(d.Markdown, "## コメント", bad+"\n\n## コメント", 1)}, "If-Match", itoa(int64(d.Version)))
	// 過去に書かれた行は、関係ない編集を止めない
	if _, err := e.db.Exec("UPDATE issues SET body_main = CONCAT(body_main, '\\n\\n', ?) WHERE display_id = ?", bad, id2); err != nil {
		t.Fatal(err)
	}
	a.json(200, "GET", "/issues/"+id2, nil, &d)
	a.json(200, "PATCH", "/issues/"+id2, map[string]any{"markdown": strings.Replace(d.Markdown, "（未記入）", "背景", 1)}, &d, "If-Match", itoa(int64(d.Version)))

	// 拒否で採番を消費しない
	p, _ := store.ProjectByID(context.Background(), e.db, pr.ID)
	if p.Counter != 2 {
		t.Errorf("counter = %d, want 2（拒否した起票で採番が進んだ）", p.Counter)
	}

	// ルールの無いプロジェクトは影響を受けない
	_, other := e.rulesProject("free", verifyRules...)
	other.json(201, "POST", "/projects/free/issues", map[string]any{"title": "x", "status": "Backlog", "body": bad}, nil)
}

// TestRulesRemovedZeroBugGate は、撤去したゼロバグゲート（2026-09-20）のキーが projects.rules に残っていても、
// イシューの読み書きが全経路で落ちず、状態変更も止められないことを確かめる（domain.Rules の無視用フィールド）。
func TestRulesRemovedZeroBugGate(t *testing.T) {
	e := newEnv(t)
	pr, a := e.rulesProject("req", verifyRules...)
	// 撤去前の設定（usage・verify に zero_bug_gate が付いた形）をそのまま残したプロジェクト
	if _, err := e.db.Exec(`UPDATE projects SET rules = ? WHERE id = ?`,
		`{"verify":{"require_on_close":true},"zero_bug_gate":{"statuses":["In Progress"],"allow_types":["bug","test"]}}`, pr.ID); err != nil {
		t.Fatal(err)
	}
	var c struct {
		Issue issueJSON `json:"issue"`
	}
	a.json(201, "POST", "/projects/req/issues", map[string]any{"title": "不具合", "type": "bug"}, &c)
	a.json(201, "POST", "/projects/req/issues", map[string]any{"title": "実装", "type": "task"}, &c)
	task := c.Issue.ID
	// 未クローズの bug があっても task を In Progress にできる（ゲートは効かない）
	a.json(200, "POST", "/issues/"+task+"/status", map[string]any{"status": "In Progress", "comment": "着手"}, nil)
	a.json(201, "POST", "/issues/"+task+"/comments", map[string]any{"text": "読み書きも通る"}, nil)
	var n int
	e.db.QueryRow("SELECT COUNT(*) FROM issue_events WHERE kind = 'rule_override' AND project_id = ?", pr.ID).Scan(&n)
	if n != 0 {
		t.Errorf("rule_override = %d, want 0", n)
	}

	// 壊れたルール設定は黙って無視しない（変更を失敗させる）
	e.db.Exec(`UPDATE projects SET rules = '{"zero_bug_gat": {}}' WHERE id = ?`, pr.ID)
	if code, _, _ := a.do("POST", "/issues/"+task+"/comments", map[string]any{"text": "x"}); code != 500 {
		t.Errorf("壊れたルールで %d", code)
	}
	if _, err := domain.ParseRules([]byte(`{"zero_bug_gat": {}}`)); err == nil {
		t.Error("未知のキーを受け入れた")
	}
}

// TestRulesViaCLI は CLI（looptrack issue）でも同じ文言で拒否され、--override が通ることを確かめる。
func TestRulesViaCLI(t *testing.T) {
	e := newEnv(t)
	_, h := e.rulesProject("ex", statusRules...)
	_, r := e.rulesProject("req", verifyRules...)
	h.json(201, "POST", "/projects/ex/issues", map[string]any{"title": "a"}, nil)
	r.json(201, "POST", "/projects/req/issues", map[string]any{"title": "検証つき", "body": verifyBody}, nil)
	run := func(slug, token string, args ...string) cliResult {
		t.Helper()
		return issueCLI(t, e, slug, token)(args...)
	}
	for _, c := range []struct {
		slug, token string
		args        []string
		rule        string
	}{
		{"ex", h.token, []string{"status", "EX-0001", "Backlog"}, "forbid_status"},
		{"ex", h.token, []string{"status", "EX-0001", "In Progress"}, "require_comment_before"},
		{"ex", h.token, []string{"close", "EX-0001", "--comment", "完了"}, "done_requires_keyword"},
		{"ex", h.token, []string{"new", "x", "--status", "Done"}, "done_requires_keyword"},
	} {
		res := run(c.slug, c.token, c.args...)
		want := ""
		if c.args[0] != "new" && c.rule != "done_requires_keyword" {
			want = "エラー: " + ruleMessage(t, c.rule, c.args[1], c.args[2]) + "\n"
		}
		if res.code != 1 || !strings.HasPrefix(res.stderr, "エラー: ") || (want != "" && res.stderr != want) || res.stdout != "" {
			t.Errorf("%v: exit %d\n%s", c.args, res.code, res.stderr)
		}
	}
	// 2 行目はサーバが返す文面（CLI はそのまま出す）。CLI は LOOPTRACK_LANG から Accept-Language を送るので
	// 日本語で届く（cliHomeEnv の LOOPTRACK_LANG=ja）。
	if res := run("ex", h.token, "close", "EX-0001", "--comment", "動作確認: PASS"); res.code != 0 || res.stdout != "EX-0001: Todo → Done\nコメント追記: EX-0001\n" {
		t.Errorf("通るはずの close: %d %q %s", res.code, res.stdout, res.stderr)
	}
	// 上書きできる違反（verify.require_on_close）は --override で通る
	if res := run("req", r.token, "close", "REQ-0001"); res.code != 1 || !strings.Contains(res.stderr, "verify の記録がありません") {
		t.Errorf("verify の拒否: %d %s", res.code, res.stderr)
	}
	if res := run("req", r.token, "close", "REQ-0001", "--override", "利用者指示"); res.code != 0 {
		t.Errorf("--override: %d %s", res.code, res.stderr)
	}
}
