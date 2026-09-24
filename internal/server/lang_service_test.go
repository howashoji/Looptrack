package server

import (
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
)

// サーバが作って返す文面のうち、internal/service・internal/domain が作るもの（ルール違反・next の見送り・
// verify の案内・下位完了の案内）は、**要求の言語**で作る（REST は reqLang・MCP は mcpLang。
// service へは Actor.Lang と引数で持ち回る）。
//
// どれも**日本語と英語の両方**を、要求の言語を固定して確かめる。片方だけだと「サーバが日本語一択」の状態と
// 区別できない。各検査の最初に、両言語の期待値が互いに違うこと（対照が効いていること）も確かめる。

// langHeaderOf は要求の言語を固定する見出し（テストの client() は Accept-Language: ja を補うが、要求が付けた値を上書きしない）。
func langHeaderOf(lang i18n.Lang) []string { return []string{"Accept-Language", string(lang)} }

func mustDiffer(t *testing.T, what, ja, en string) {
	t.Helper()
	if ja == en || !hasJapanese(ja) || hasJapanese(en) {
		t.Fatalf("前提が崩れている（%s の期待値が言語で分かれていない）: ja=%q en=%q", what, ja, en)
	}
}

// プロジェクトが設定した文面（訳さない）に埋める値（起票の {id}）と、既定の文面が要求の言語になる。
// next が全候補を見送ったときの 422（見送りの理由・案内の文）も同じ言語になる。
func TestRuleTextFollowsRequestLang(t *testing.T) {
	v := newVerifyEnv(t, "lr", `{"forbid_status": {"statuses": ["In Review"], "message": "[{id}] {status}"},
		"require_comment_before": {"statuses": ["In Progress"]}}`)

	// 1. 設定された文面に埋める {id}（起票はまだ ID が無いので「新しいイシュー」）
	want := map[i18n.Lang]string{
		i18n.JA: "[" + i18n.T(i18n.JA, "domain.rules.new_issue") + "] In Review",
		i18n.EN: "[" + i18n.T(i18n.EN, "domain.rules.new_issue") + "] In Review",
	}
	mustDiffer(t, "設定された文面", want[i18n.JA], want[i18n.EN])
	for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
		e := v.ed.fail(422, "POST", "/projects/lr/issues", map[string]any{"title": "x", "status": "In Review"}, langHeaderOf(lang)...)
		if e.Error.Rule != "forbid_status" || e.Error.Message != want[lang] {
			t.Errorf("%s: 起票の違反 = %q（rule %s）, want %q", lang, e.Error.Message, e.Error.Rule, want[lang])
		}
	}

	// 2. next が全候補をルールで見送った 422。最初の違反（既定の文面）と、後ろの案内の両方が要求の言語
	var created struct {
		Issue issueDetailJSON `json:"issue"`
	}
	v.ed.json(201, "POST", "/projects/lr/issues", map[string]any{"title": "one", "body": "plain"}, &created, langHeaderOf(i18n.EN)...)
	id := created.Issue.ID
	viol := map[i18n.Lang]string{}
	for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
		viol[lang] = i18n.T(lang, "domain.rules.require_comment_before", "id", id, "status", "In Progress")
	}
	mustDiffer(t, "require_comment_before", viol[i18n.JA], viol[i18n.EN])
	for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
		wantMsg := i18n.T(lang, "service.next.all_skipped", "message", viol[lang], "count", 1, "ids", id,
			"hint_comment", i18n.M("service.next.all_skipped.hint_comment"), "hint_override", "")
		e := v.ed.fail(422, "POST", "/projects/lr/next", map[string]any{}, langHeaderOf(lang)...)
		if e.Error.Rule != "require_comment_before" || e.Error.Message != wantMsg {
			t.Errorf("%s: next の 422 = %q, want %q", lang, e.Error.Message, wantMsg)
		}
		// dry_run でコメントを付けると通る（見送りが言語で変わらないことの対照）
		var n nextJSON
		v.ed.json(200, "POST", "/projects/lr/next", map[string]any{"dry_run": true, "comment": "go"}, &n, langHeaderOf(lang)...)
		if n.Action != "would_start" || n.Issue == nil || n.Issue.ID != id {
			t.Errorf("%s: コメント付きの dry_run が通らない: %+v", lang, n)
		}
	}

	// 3. MCP の next も接続の言語（Accept-Language）で同じ文面になる
	for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
		m := v.mcpAs(v.ed.token, map[string]string{"X-Looptrack-Project": "lr", "Accept-Language": string(lang)})
		text, _ := m.call("next", map[string]any{}, true)
		if !strings.HasPrefix(text, viol[lang]) {
			t.Errorf("%s: MCP next の isError = %q, want 先頭が %q", lang, text, viol[lang])
		}
	}
}

// verify の案内（GET …/verify の message・text、next の直近の記録の行）が要求の言語になる。
func TestVerifyTextFollowsRequestLang(t *testing.T) {
	v := newVerifyEnv(t, "lv", "")
	id := v.create("with verify", verifyBody)
	none := v.create("without verify", "plain")

	noneMsg := map[i18n.Lang]string{i18n.JA: domain.NoVerifyCommandsMsg(none).In(i18n.JA), i18n.EN: domain.NoVerifyCommandsMsg(none).In(i18n.EN)}
	mustDiffer(t, "節なしの message", noneMsg[i18n.JA], noneMsg[i18n.EN])
	lastNone := map[i18n.Lang]string{i18n.JA: i18n.T(i18n.JA, "service.verify.last.none"), i18n.EN: i18n.T(i18n.EN, "service.verify.last.none")}
	mustDiffer(t, "直近の記録なし", lastNone[i18n.JA], lastNone[i18n.EN])

	plan := func(id string, lang i18n.Lang) verifyPlanJSON {
		t.Helper()
		var p verifyPlanJSON
		v.ed.json(200, "GET", "/issues/"+id+"/verify", nil, &p, langHeaderOf(lang)...)
		return p
	}
	for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
		if p := plan(none, lang); p.Message != noneMsg[lang] {
			t.Errorf("%s: 節なしの message = %q, want %q", lang, p.Message, noneMsg[lang])
		}
		p := plan(id, lang)
		run := i18n.T(lang, "service.verify.run", "command", domain.VerifyCommand(id))
		head := i18n.T(lang, "service.verify.heading", "id", id, "count", 2, "sha", p.BodySHA256[:8])
		wantText := head + "\n1. go test ./...\n2. make lint\n" + lastNone[lang] + "\n" + run
		if p.Message != run || p.Text != wantText {
			t.Errorf("%s: GET verify\nmessage %q\ntext:\n%s\nwant:\n%s", lang, p.Message, p.Text, wantText)
		}
		if lang == i18n.EN && hasJapanese(p.Text) {
			t.Errorf("英語の text に日本語が残っている:\n%s", p.Text)
		}
	}

	// next の verify の節（直近の記録の行）と verify.message
	for _, lang := range []i18n.Lang{i18n.EN, i18n.JA} { // 1 回目で着手し、2 回目は着手中を返す
		var n nextJSON
		v.ed.json(200, "POST", "/projects/lv/next", map[string]any{}, &n, langHeaderOf(lang)...)
		if n.Issue == nil || n.Issue.ID != id || n.Verify == nil {
			t.Fatalf("%s: next: %+v", lang, n)
		}
		if !strings.Contains(n.Text, "\n"+lastNone[lang]+"\n") || strings.Contains(n.Text, lastNone[otherLang(lang)]) {
			t.Errorf("%s: next の text の直近の記録の行が要求の言語でない:\n%s", lang, n.Text)
		}
		if want := i18n.T(lang, "service.verify.run", "command", domain.VerifyCommand(id)); n.Verify.Message != want {
			t.Errorf("%s: next の verify.message = %q, want %q", lang, n.Verify.Message, want)
		}
	}

	// 記録の後の 1 行（成否・状態）も要求の言語
	v.record(id)
	for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
		p := plan(id, lang)
		passed := i18n.T(lang, "service.verify.last.passed", "passed", 2, "total", 2)
		current := i18n.T(lang, "service.verify.last.current")
		if !strings.Contains(p.Text, passed) || !strings.Contains(p.Text, current) {
			t.Errorf("%s: 記録の後の text に %q・%q が無い:\n%s", lang, passed, current, p.Text)
		}
		if lang == i18n.EN && hasJapanese(p.Text) {
			t.Errorf("英語の text に日本語が残っている:\n%s", p.Text)
		}
	}

	// MCP verify_issue（節なしは isError で GET の message と同じ文面）
	for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
		m := v.mcpAs(v.ed.token, map[string]string{"X-Looptrack-Project": "lv", "Accept-Language": string(lang)})
		if text, _ := m.call("verify_issue", map[string]any{"id": none}, true); text != noneMsg[lang] {
			t.Errorf("%s: MCP verify_issue（節なし） = %q, want %q", lang, text, noneMsg[lang])
		}
		if text, _ := m.call("verify_issue", map[string]any{"id": id}, false); !strings.HasPrefix(text, plan(id, lang).Text) {
			t.Errorf("%s: MCP verify_issue が GET の text で始まらない:\n%s", lang, text)
		}
	}
}

// 下位がすべて完了した要件の案内（close の応答の messages と requirements_ready）が要求の言語になる。
func TestClosableNoticeFollowsRequestLang(t *testing.T) {
	v := newVerifyEnv(t, "lc", "")
	create := func(title string, extra map[string]any) string {
		t.Helper()
		body := map[string]any{"title": title}
		for k, x := range extra {
			body[k] = x
		}
		var created struct {
			Issue issueDetailJSON `json:"issue"`
		}
		v.ed.json(201, "POST", "/projects/lc/issues", body, &created)
		return created.Issue.ID
	}
	for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
		req := create("requirement "+string(lang), map[string]any{"type": "requirement"})
		child := create("task "+string(lang), map[string]any{"traces": []string{req}})
		c := domain.ClosableRequirement{Requirement: domain.Issue{ID: req}, Done: 1}
		want := c.Notice(lang)
		mustDiffer(t, "下位完了の案内", c.Notice(i18n.JA), c.Notice(i18n.EN))
		var res struct {
			Messages []string       `json:"messages"`
			Ready    []closableJSON `json:"requirements_ready"`
		}
		v.ed.json(200, "POST", "/issues/"+child+"/status", map[string]any{"status": "Done"}, &res, langHeaderOf(lang)...)
		if len(res.Messages) == 0 || res.Messages[len(res.Messages)-1] != want {
			t.Errorf("%s: close の messages = %q, want 末尾が %q", lang, res.Messages, want)
		}
		if len(res.Ready) != 1 || res.Ready[0].Message != want || res.Ready[0].Command != c.CloseCommand(lang) {
			t.Errorf("%s: requirements_ready = %+v", lang, res.Ready)
		}
	}
}

// CLI の next（looptrack issue next）が、LOOPTRACK_LANG の言語でサーバの文面を出す。
// 以前は同じ利用者・同じ環境で list は英語・next は日本語になった（next の文面をサーバが日本語で固定していたため）。
func TestNextLanguageViaCLI(t *testing.T) {
	v := newVerifyEnv(t, "lcli", `{"require_comment_before": {"statuses": ["In Progress"]}}`)
	var created struct {
		Issue issueDetailJSON `json:"issue"`
	}
	v.ed.json(201, "POST", "/projects/lcli/issues", map[string]any{"title": "one", "body": "plain"}, &created, langHeaderOf(i18n.EN)...)
	id := created.Issue.ID
	run := func(lang i18n.Lang) cliResult {
		t.Helper()
		dir, home := t.TempDir(), t.TempDir()
		env := append(cliAPIEnv(v.srv.URL+"/im", "lcli", v.ed.token, home), "CLAUDE_PROJECT_DIR="+dir, "LOOPTRACK_LANG="+string(lang))
		return runCLI(t, dir, env, "", "issue", "next")
	}
	for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
		want := i18n.T(lang, "domain.rules.require_comment_before", "id", id, "status", "In Progress")
		r := run(lang)
		t.Logf("LOOPTRACK_LANG=%s -> exit %d %s", lang, r.code, strings.TrimRight(r.stderr, "\n"))
		if r.code == 0 || !strings.Contains(r.stderr, want) {
			t.Errorf("LOOPTRACK_LANG=%s: exit %d stderr %q, want %q を含む", lang, r.code, r.stderr, want)
		}
		if lang == i18n.EN && hasJapanese(r.stderr) {
			t.Errorf("英語の側に日本語が混ざっています: %q", r.stderr)
		}
	}
}

func otherLang(l i18n.Lang) i18n.Lang {
	if l == i18n.JA {
		return i18n.EN
	}
	return i18n.JA
}
