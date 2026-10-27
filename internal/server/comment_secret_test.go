package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/store"
)

// コメントの秘密の検査（プロジェクト別ルール comment.reject_secret・DESIGN.md §9-1）を REST・MCP・Web のフォーム
// （画面のセッションで REST を呼ぶ）の経路で確かめる。既定（ルールなし）は 422 comment_secret で何も残さず、
// 切のプロジェクトは保存して応答に注記（行番号と規則の語。値なし）を付ける。CLI の出力は internal/clitest の golden。

const secretRESTValue = "abcDEF123secretvalue"

// secretRESTText は 2 行目が秘密の検査（Bearer …）に当たる本文。
const secretRESTText = "作業メモ\nAuthorization: Bearer " + secretRESTValue

func TestCommentSecretRejectedOnAllPaths(t *testing.T) {
	a := newAttachAPIEnv(t)
	baseC, baseE := commentCounts(t, a, a.issue)
	var before issueDetailJSON
	a.ed.json(200, "GET", "/issues/"+a.issue, nil, &before)

	// REST: コメントの追記と状態の変更（日英の文面に行番号と規則の語。値は出さない）
	for _, c := range []struct{ lang, line string }{{"ja", "2 行目"}, {"en", "line 2"}} {
		e := a.ed.fail(422, "POST", "/issues/"+a.issue+"/comments", map[string]any{"text": secretRESTText}, "Accept-Language", c.lang)
		if e.Error.Code != "comment_secret" || strings.Contains(e.Error.Message, secretRESTValue) ||
			!strings.Contains(e.Error.Message, c.line) || !strings.Contains(e.Error.Message, "Bearer …") || !strings.Contains(e.Error.Message, "***") {
			t.Errorf("REST comments（%s）: %+v", c.lang, e.Error)
		}
		e = a.ed.fail(422, "POST", "/issues/"+a.issue+"/status", map[string]any{"status": "In Progress", "comment": secretRESTText}, "Accept-Language", c.lang)
		if e.Error.Code != "comment_secret" || strings.Contains(e.Error.Message, secretRESTValue) || !strings.Contains(e.Error.Message, c.line) {
			t.Errorf("REST status（%s）: %+v", c.lang, e.Error)
		}
	}
	// MCP: add_comment と set_status
	m := a.mcpAs(a.ed.token, map[string]string{"X-Looptrack-Project": a.pr.Slug})
	if text, _ := m.call("add_comment", map[string]any{"id": a.issue, "text": secretRESTText}, true); strings.Contains(text, secretRESTValue) || !strings.Contains(text, "Bearer …") {
		t.Errorf("MCP add_comment: %q", text)
	}
	if text, _ := m.call("set_status", map[string]any{"id": a.issue, "status": "In Progress", "comment": secretRESTText}, true); strings.Contains(text, secretRESTValue) || !strings.Contains(text, "Bearer …") {
		t.Errorf("MCP set_status: %q", text)
	}
	// Web のフォーム（board.js と同じ要求）: 422 のサーバの文面がそのまま返る（画面はフォームの下に出す）
	c, _, csrf := a.boardSession("att-ed", "att-ed-password-1", a.pr.Slug)
	code, v := a.formPost(c, csrf, "/api/v1/issues/"+a.issue+"/comments", map[string]any{"text": secretRESTText})
	if msg := errMessage(v); code != http.StatusUnprocessableEntity || !strings.Contains(msg, "Bearer …") || strings.Contains(msg, secretRESTValue) {
		t.Errorf("Web のコメントのフォーム: %d %q", code, msg)
	}
	code, v = a.formPost(c, csrf, "/api/v1/issues/"+a.issue+"/status", map[string]any{"status": "In Progress", "comment": secretRESTText})
	if msg := errMessage(v); code != http.StatusUnprocessableEntity || !strings.Contains(msg, "Bearer …") {
		t.Errorf("Web の状態の変更のフォーム: %d %q", code, msg)
	}

	// next の同時のコメントも同じ（候補を見送らずにコメントの理由で返し、何も着手しない）
	if e := a.ed.fail(422, "POST", "/projects/"+a.pr.Slug+"/next", map[string]any{"comment": secretRESTText}); e.Error.Code != "comment_secret" || strings.Contains(e.Error.Message, secretRESTValue) {
		t.Errorf("REST next: %+v", e.Error)
	}
	if cs, ev := commentCounts(t, a, a.issue); cs != baseC || ev != baseE {
		t.Fatalf("拒んだコメントが残った: コメント %d → %d・記録 %d → %d", baseC, cs, baseE, ev)
	}
	var after issueDetailJSON
	a.ed.json(200, "GET", "/issues/"+a.issue, nil, &after)
	if after.Status != before.Status {
		t.Errorf("拒んだ状態の変更が通った: %s → %s", before.Status, after.Status)
	}
	// 対照: 当たらない本文はどの経路でも通り、注記は付かない
	var res map[string]any
	a.ed.json(201, "POST", "/issues/"+a.issue+"/comments", map[string]any{"text": "作業メモ: token=*** で確かめた"}, &res)
	if _, ok := res["secret_notice"]; ok {
		t.Errorf("当たらない本文に secret_notice が付いた: %v", res)
	}
	if text, data := m.call("add_comment", map[string]any{"id": a.issue, "text": "作業メモ"}, false); data["secret_notice"] != nil || strings.Contains(text, "comment.reject_secret") {
		t.Errorf("MCP の当たらない本文に注記が付いた: %q %v", text, data)
	}
}

func TestCommentSecretNoticeWhenRuleOff(t *testing.T) {
	a := newAttachAPIEnv(t)
	if err := store.SetRules(context.Background(), a.db, a.pr.ID, []byte(`{"comment":{"reject_secret":false}}`)); err != nil {
		t.Fatal(err)
	}
	stored := func() string {
		t.Helper()
		var d issueDetailJSON
		a.ed.json(200, "GET", "/issues/"+a.issue, nil, &d)
		return d.Comments[len(d.Comments)-1].Content
	}
	checkNotice := func(label string, n any, line string) {
		t.Helper()
		s, _ := n.(string)
		if s == "" || strings.Contains(s, secretRESTValue) || !strings.Contains(s, line) || !strings.Contains(s, "Bearer …") || !strings.Contains(s, "comment.reject_secret") {
			t.Errorf("%s: 注記 %q", label, s)
		}
	}

	// REST: コメントの追記は本文を変えずに保存し、secret_notice を付ける
	var res map[string]any
	a.ed.json(201, "POST", "/issues/"+a.issue+"/comments", map[string]any{"text": secretRESTText}, &res, "Accept-Language", "en")
	checkNotice("REST comments", res["secret_notice"], "line 2")
	if s := stored(); s != secretRESTText {
		t.Errorf("REST: 本文が変わった: %q", s)
	}
	// REST: 状態の変更は messages の行と secret_notice（Web が状態の変更フォームの下に出す）
	res = nil
	a.ed.json(200, "POST", "/issues/"+a.issue+"/status", map[string]any{"status": "In Progress", "comment": secretRESTText}, &res)
	checkNotice("REST status", res["secret_notice"], "2 行目")
	msgs, _ := res["messages"].([]any)
	if len(msgs) == 0 || msgs[len(msgs)-1] != res["secret_notice"] {
		t.Errorf("REST status: messages の最後の行が注記でない: %v", msgs)
	}
	// MCP: 本文の末尾の行と data の secret_notice
	m := a.mcpAs(a.ed.token, map[string]string{"X-Looptrack-Project": a.pr.Slug})
	text, data := m.call("add_comment", map[string]any{"id": a.issue, "text": secretRESTText}, false)
	checkNotice("MCP add_comment", data["secret_notice"], "2 行目")
	if lines := strings.Split(text, "\n"); len(lines) < 2 || lines[1] != data["secret_notice"] {
		t.Errorf("MCP add_comment: 「コメント追記」の次の行が注記でない: %q", text)
	}
	text, data = m.call("set_status", map[string]any{"id": a.issue, "status": "Todo", "comment": secretRESTText}, false)
	checkNotice("MCP set_status", data["secret_notice"], "2 行目")
	if s, _ := data["secret_notice"].(string); !strings.Contains(text, s) {
		t.Errorf("MCP set_status: 本文に注記が無い: %q", text)
	}
	if s := stored(); s != secretRESTText {
		t.Errorf("MCP: 本文が変わった: %q", s)
	}
	// Web のフォーム（board.js と同じ要求）: 201 で secret_notice が返る（画面はコメントのフォームの下に出す）
	c, _, csrf := a.boardSession("att-ed", "att-ed-password-1", a.pr.Slug)
	code, v := a.formPost(c, csrf, "/api/v1/issues/"+a.issue+"/comments", map[string]any{"text": secretRESTText})
	if code != http.StatusCreated {
		t.Fatalf("Web のコメントのフォーム: %d %v", code, v)
	}
	checkNotice("Web", v["secret_notice"], "2 行目")

	// next: 着手して、本文（text）と secret_notice に注記を載せる
	var nx map[string]any
	a.ed.json(200, "POST", "/projects/"+a.pr.Slug+"/next", map[string]any{"comment": secretRESTText}, &nx)
	checkNotice("REST next", nx["secret_notice"], "2 行目")
	if s, _ := nx["secret_notice"].(string); nx["action"] != "started" || !strings.Contains(nx["text"].(string), s) {
		t.Errorf("REST next: action=%v text に注記が無い: %v", nx["action"], nx["text"])
	}

	// 要約はルールに関わらず拒む
	digest := "セッション要約: test-pc\nbranch: main\nhead: ce768dc\nai: claude-code\npassword=" + secretRESTValue
	if e := a.ed.fail(422, "POST", "/issues/"+a.issue+"/comments", map[string]any{"text": digest}); e.Error.Code != "comment_secret" {
		t.Errorf("切のプロジェクトの要約: %+v", e.Error)
	}
	// 対照: 当たらない本文には注記が付かない
	res = nil
	a.ed.json(201, "POST", "/issues/"+a.issue+"/comments", map[string]any{"text": "作業メモ"}, &res)
	if _, ok := res["secret_notice"]; ok {
		t.Errorf("当たらない本文に secret_notice が付いた: %v", res)
	}
}
