package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/store"
)

// ボード・詳細の起票・状態の変更・コメントのフォーム。フォームは既存の REST API を画面のセッション
// （Cookie + X-CSRF-Token）で呼ぶ。editor 以上にだけ出し、viewer には出さない（API も 403 のまま）。

var boardCSRFRe = regexp.MustCompile(`data-csrf="([^"]+)"`)

// boardSession は利用者でログインしてボードを開き、画面の CSRF トークンを返す。
func (e *env) boardSession(login, password, slug string) (*http.Client, string, string) {
	e.t.Helper()
	c := e.client()
	e.enroll(c, login, password)
	res, page := e.get(c, "/im/p/"+slug+"/")
	if res.StatusCode != http.StatusOK {
		e.t.Fatalf("ボード %s: %d", slug, res.StatusCode)
	}
	m := boardCSRFRe.FindStringSubmatch(page)
	if m == nil {
		e.t.Fatalf("ボードに data-csrf が無い:\n%s", page)
	}
	return c, page, m[1]
}

// formPost は board.js の submitForm と同じ要求（JSON・X-CSRF-Token・同じオリジン）を送る。
func (e *env) formPost(c *http.Client, csrf, path string, body any) (int, map[string]any) {
	e.t.Helper()
	raw, _ := json.Marshal(body)
	res, out := e.do(c, "POST", "/im"+path, string(raw), "Content-Type", "application/json", "X-CSRF-Token", csrf,
		"Origin", e.srv.URL, "Sec-Fetch-Site", "same-origin")
	var v map[string]any
	json.Unmarshal([]byte(out), &v)
	return res.StatusCode, v
}

func errMessage(v map[string]any) string {
	if e, ok := v["error"].(map[string]any); ok {
		s, _ := e["message"].(string)
		return s
	}
	return ""
}

func TestBoardForms(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("req")
	ed := e.user("editor", "editor-password-1", "member")
	vw := e.user("viewer", "viewer-password-1", "member")
	store.SetMember(ctx, e.db, pr.ID, ed.ID, "editor")
	store.SetMember(ctx, e.db, pr.ID, vw.ID, "viewer")
	e.apiAs(ed).json(201, "POST", "/projects/req/issues", map[string]any{"title": "既存"}, nil)

	// viewer: 起票ボタンが無く、board の can_edit は false。フォームの送り先の API はどれも 403
	vc, vpage, vcsrf := e.boardSession("viewer", "viewer-password-1", "req")
	if strings.Contains(vpage, `id="newIssue"`) {
		t.Error("viewer のボードに起票ボタンが出ている")
	}
	var board struct {
		CanEdit bool `json:"can_edit"`
	}
	e.apiAs(vw).json(200, "GET", "/projects/req/board", nil, &board)
	if board.CanEdit {
		t.Error("viewer の can_edit が true")
	}
	for _, r := range []struct {
		path string
		body any
	}{
		{"/api/v1/projects/req/issues", map[string]any{"title": "viewer の起票"}},
		{"/api/v1/issues/REQ-0001/status", map[string]any{"status": "In Progress"}},
		{"/api/v1/issues/REQ-0001/comments", map[string]any{"text": "viewer のコメント"}},
	} {
		if code, v := e.formPost(vc, vcsrf, r.path, r.body); code != http.StatusForbidden || errMessage(v) == "" {
			t.Errorf("viewer %s: %d %v, want 403 と文言", r.path, code, v)
		}
	}

	// editor: 起票ボタンがあり、can_edit は true
	c, page, csrf := e.boardSession("editor", "editor-password-1", "req")
	if !strings.Contains(page, `<button type="button" class="appbar-action" id="newIssue"`) {
		t.Errorf("editor のボードに起票ボタンが無い:\n%s", page)
	}
	e.apiAs(ed).json(200, "GET", "/projects/req/board", nil, &board)
	if !board.CanEdit {
		t.Error("editor の can_edit が false")
	}
	// CSRF トークンが無い・違うときは 403（フォームは必ず X-CSRF-Token を付ける）
	if code, _ := e.formPost(c, "wrong", "/api/v1/projects/req/issues", map[string]any{"title": "x"}); code != http.StatusForbidden {
		t.Errorf("CSRF 違い: %d, want 403", code)
	}

	// 起票フォーム: render.js の formRequest("create") が作る要求（本文 + 受け入れ条件の節）
	code, v := e.formPost(c, csrf, "/api/v1/projects/req/issues", map[string]any{"title": "画面から起票", "type": "bug", "priority": "P1",
		"body": "手順どおりに操作すると落ちる\n\n## 受け入れ条件\n\n- [ ] 落ちない\n- [ ] テストがある"})
	if code != http.StatusCreated {
		t.Fatalf("起票: %d %v", code, v)
	}
	var d issueDetailJSON
	e.apiAs(ed).json(200, "GET", "/issues/REQ-0002", nil, &d)
	if d.Title != "画面から起票" || d.Type != "bug" || d.Priority != "P1" || d.Status != "Todo" {
		t.Errorf("起票した項目: %+v", d.issueJSON)
	}
	// CLI の new と同じ節構成（背景 / 内容 / 受け入れ条件 / コメント）で、雛形の受け入れ条件は重ならない
	want := "## 背景\n\n（未記入）\n\n## 内容\n\n手順どおりに操作すると落ちる\n\n## 受け入れ条件\n\n- [ ] 落ちない\n- [ ] テストがある\n\n## コメント"
	if !strings.Contains(d.Markdown, want) || strings.Count(d.Markdown, "## 受け入れ条件") != 1 {
		t.Errorf("本文の節構成:\n%s", d.Markdown)
	}

	// 状態の変更フォーム（コメント付き）とコメントの追記フォーム
	if code, v := e.formPost(c, csrf, "/api/v1/issues/REQ-0002/status", map[string]any{"status": "In Progress", "comment": "画面から着手"}); code != http.StatusOK {
		t.Fatalf("状態の変更: %d %v", code, v)
	}
	if code, v := e.formPost(c, csrf, "/api/v1/issues/REQ-0002/comments", map[string]any{"text": "画面からのコメント"}); code != http.StatusCreated {
		t.Fatalf("コメント: %d %v", code, v)
	}
	e.apiAs(ed).json(200, "GET", "/issues/REQ-0002", nil, &d)
	if d.Status != "In Progress" || d.Assignee != "editor" || len(d.Comments) != 2 ||
		!strings.Contains(d.Markdown, "画面から着手") || !strings.Contains(d.Markdown, "画面からのコメント") {
		t.Errorf("状態・コメント: status=%s assignee=%s comments=%d\n%s", d.Status, d.Assignee, len(d.Comments), d.Markdown)
	}
}

var boardTextsRe = regexp.MustCompile(`(?s)<script type="application/json" id="i18n">(.*?)</script>`)

// TestBoardFormHeadingFollowsLang は、起票フォームが本文に書く受け入れ条件の見出しと「未記入」が
// 作成者（画面を見ている人）の言語になることを確かめる。ボードの画面が JS に渡す文面（i18n の JSON）から
// 見出しを取り、render.js の buildIssueBody と同じ形の本文で起票して、保存された本文に反対の言語の見出しが
// 混ざらないことを見る。英語の雛形の中に日本語の見出しが 1 つ混ざる、という不具合の再発を捕まえる。
func TestBoardFormHeadingFollowsLang(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("req")
	for i, tc := range []struct {
		lang, login        string
		client             *http.Client
		heading, empty     string
		other, otherEmpty  string
		templateBackground string
	}{
		{"en", "enuser", enClient(), "## Acceptance criteria", "(not written yet)", "受け入れ条件", "未記入", "## Background"},
		{"ja", "jauser", e.client(), "## 受け入れ条件", "（未記入）", "Acceptance criteria", "(not written yet)", "## 背景"},
	} {
		u := e.user(tc.login, tc.login+"-password-1", "member")
		store.SetMember(ctx, e.db, pr.ID, u.ID, "editor")
		c := tc.client
		e.enroll(c, tc.login, tc.login+"-password-1")
		res, page := e.get(c, "/im/p/req/")
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s: ボード %d", tc.lang, res.StatusCode)
		}
		m := boardTextsRe.FindStringSubmatch(page)
		if m == nil {
			t.Fatalf("%s: ボードに文面の JSON が無い", tc.lang)
		}
		var texts map[string]string
		if err := json.Unmarshal([]byte(m[1]), &texts); err != nil {
			t.Fatalf("%s: 文面の JSON が読めない: %v\n%s", tc.lang, err, m[1])
		}
		if texts["body_acceptance"] != tc.heading || texts["body_empty"] != tc.empty {
			t.Fatalf("%s: 画面が渡す見出し = %q / 未記入 = %q, want %q / %q", tc.lang, texts["body_acceptance"], texts["body_empty"], tc.heading, tc.empty)
		}
		boardCSRF := boardCSRFRe.FindStringSubmatch(page)[1]
		// buildIssueBody("", "works", texts) と同じ本文
		body := texts["body_empty"] + "\n\n" + texts["body_acceptance"] + "\n\n- [ ] works"
		code, v := e.formPost(c, boardCSRF, "/api/v1/projects/req/issues", map[string]any{"title": "from the board", "body": body})
		if code != http.StatusCreated {
			t.Fatalf("%s: 起票 %d %v", tc.lang, code, v)
		}
		var d issueDetailJSON
		e.apiAs(u).json(200, "GET", fmt.Sprintf("/issues/REQ-%04d", i+1), nil, &d)
		if strings.Count(d.Markdown, tc.heading) != 1 || !strings.Contains(d.Markdown, tc.templateBackground) {
			t.Errorf("%s: 本文の見出しが作成者の言語でない（%q が 1 回・雛形の %q）:\n%s", tc.lang, tc.heading, tc.templateBackground, d.Markdown)
		}
		if strings.Contains(d.Markdown, tc.other) || strings.Contains(d.Markdown, tc.otherEmpty) {
			t.Errorf("%s: 反対の言語の見出しか「未記入」が混ざった:\n%s", tc.lang, d.Markdown)
		}
	}
}

// TestBoardFormsRuleRejection は、プロジェクト別ルールで拒否された画面からの変更が、画面に出す文言（error.message）と
// 上書きの可否（error.overridable）を返し、上書きできる違反は理由付きなら通ることを確かめる。
// 画面の操作は人の操作なので、usage.require_on_close（AI の操作だけが対象）には掛からない。
func TestBoardFormsRuleRejection(t *testing.T) {
	e := newEnv(t)

	// クローズの条件（done_requires_keyword）: 上書きできない。文言はルールのまま
	e.rulesProject("ex", statusRules...)
	c, _, csrf := e.boardSession("ex-ed", "editor-password-1", "ex")
	if code, v := e.formPost(c, csrf, "/api/v1/projects/ex/issues", map[string]any{"title": "閉じる"}); code != http.StatusCreated {
		t.Fatalf("起票: %d %v", code, v)
	}
	code, v := e.formPost(c, csrf, "/api/v1/issues/EX-0001/status", map[string]any{"status": "Done", "comment": "単体 PASS"})
	if code != http.StatusUnprocessableEntity || errMessage(v) != ruleMessage(t, "done_requires_keyword", "EX-0001", "Done") {
		t.Errorf("クローズの拒否: %d %v", code, v)
	}
	if ov, _ := v["error"].(map[string]any)["overridable"].(bool); ov {
		t.Errorf("上書きできない違反に overridable が付いた: %v", v)
	}

	// 検証コマンドの記録（verify.require_on_close）: 上書きできる。理由付きなら通る
	e.rulesProject("req", verifyRules...)
	c, _, csrf = e.boardSession("req-ed", "editor-password-1", "req")
	e.formPost(c, csrf, "/api/v1/projects/req/issues", map[string]any{"title": "実装", "body": verifyBody})
	code, v = e.formPost(c, csrf, "/api/v1/issues/REQ-0001/status", map[string]any{"status": "Done"})
	if code != http.StatusUnprocessableEntity || !strings.Contains(errMessage(v), "verify の記録がありません") {
		t.Fatalf("verify_required_on_close: %d %v", code, v)
	}
	if ov, _ := v["error"].(map[string]any)["overridable"].(bool); !ov {
		t.Errorf("上書きできる違反なのに overridable が無い: %v", v)
	}
	if code, v := e.formPost(c, csrf, "/api/v1/issues/REQ-0001/status", map[string]any{"status": "Done",
		"override_reason": "画面から理由付きでクローズ"}); code != http.StatusOK {
		t.Errorf("理由付きのクローズ: %d %v", code, v)
	}
}
