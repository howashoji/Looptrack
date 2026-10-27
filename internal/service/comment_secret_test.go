package service

import (
	"strings"
	"testing"
)

// コメントの秘密の検査（プロジェクト別ルール comment.reject_secret・DESIGN.md §9-1）。判定は prepareComment の 1 か所で、
// コメントの追記（AddComment。REST・MCP・CLI・Web の入口）と状態の変更と同時のコメント（setStatus）の両方がそこを通る。

const secretCommentValue = "abcDEF123secretvalue"

// secretComment は秘密の検査に当たる本文（2 行目に Bearer）。
const secretComment = "作業メモ\nAuthorization: Bearer " + secretCommentValue

func TestCommentSecretRejectedByDefault(t *testing.T) {
	e := newAttachEnv(t)
	on := e.demo
	on.Rules = []byte(`{"comment":{"reject_secret":true}}`)
	baseC, baseE := e.emptyCommentCounts(e.issue)
	for _, p := range []struct {
		name string
		rule []byte
	}{{"ルールなし（既定）", e.demo.Rules}, {"true", on.Rules}} {
		pr := e.demo
		pr.Rules = p.rule
		for _, via := range []string{"api", "mcp", "cli", "web"} {
			a := e.editor
			a.Via = via
			if _, err := e.s.AddComment(e.ctx, a, pr, e.issue.Row.ID, CommentInput{Text: secretComment}); errCode(err) != "comment_secret" || errKind(err) != Rejected {
				t.Errorf("%s・%s: AddComment の err = %v, want Rejected comment_secret", p.name, via, err)
			} else if strings.Contains(err.Error(), secretCommentValue) {
				t.Errorf("%s・%s: 拒否の文面に値が出た: %v", p.name, via, err)
			}
		}
		// 状態の変更と同時のコメント: 拒めば状態も変えない
		if _, err := e.s.SetStatus(e.ctx, e.editor, pr, e.issue.Row.ID, "In Progress", secretComment, ""); errCode(err) != "comment_secret" {
			t.Errorf("%s: 状態の変更と同時のコメントの err = %v, want comment_secret", p.name, err)
		}
	}
	if c, ev := e.emptyCommentCounts(e.issue); c != baseC || ev != baseE {
		t.Fatalf("拒んだコメントが残った: コメント %d → %d・記録 %d → %d", baseC, c, baseE, ev)
	}
	if cur, _ := e.s.Detail(e.ctx, e.demo, e.issue.Row.ID); cur.Item.Status != e.issue.Item.Status {
		t.Errorf("拒んだ状態の変更が通った: %s", cur.Item.Status)
	}
	// 対照: 当たらない本文は既定でもそのまま保存され、注記も付かない
	clean := "作業メモ: token=*** で確かめた"
	it, err := e.s.AddComment(e.ctx, e.editor, e.demo, e.issue.Row.ID, CommentInput{Text: clean})
	if err != nil || it.SecretNotice != "" {
		t.Fatalf("当たらない本文が通らない（前提が崩れています）: err=%v notice=%q", err, it.SecretNotice)
	}
	if got := it.Row.Doc.Comments[len(it.Row.Doc.Comments)-1].Content; got != clean {
		t.Errorf("当たらない本文が変わった: %q", got)
	}
	res, err := e.s.SetStatus(e.ctx, e.editor, e.demo, e.issue.Row.ID, "In Progress", clean, "")
	if err != nil {
		t.Fatalf("当たらない本文の状態の変更: %v", err)
	}
	if !res.CommentAdded || res.SecretNotice != "" {
		t.Errorf("当たらない本文の状態の変更: added=%v notice=%q", res.CommentAdded, res.SecretNotice)
	}
}

func TestCommentSecretAllowedWithNotice(t *testing.T) {
	e := newAttachEnv(t)
	off := e.demo
	off.Rules = []byte(`{"comment":{"reject_secret":false}}`)
	baseC, _ := e.emptyCommentCounts(e.issue)

	it, err := e.s.AddComment(e.ctx, e.editor, off, e.issue.Row.ID, CommentInput{Text: secretComment})
	if err != nil {
		t.Fatalf("切のプロジェクトで拒まれた: %v", err)
	}
	if got := it.Row.Doc.Comments[len(it.Row.Doc.Comments)-1].Content; got != secretComment {
		t.Errorf("切のプロジェクトで本文が変わった: %q", got)
	}
	for _, w := range []string{"2", "Bearer …", "comment.reject_secret", e.issue.Item.ID} {
		if !strings.Contains(it.SecretNotice, w) {
			t.Errorf("注記に %q が無い: %q", w, it.SecretNotice)
		}
	}
	if strings.Contains(it.SecretNotice, secretCommentValue) {
		t.Errorf("注記に値が出た: %q", it.SecretNotice)
	}

	// 状態の変更と同時のコメントも保存して注記を返す
	res, err := e.s.SetStatus(e.ctx, e.editor, off, e.issue.Row.ID, "In Progress", secretComment, "")
	if err != nil || !res.CommentAdded || res.Issue.Item.Status != "In Progress" {
		t.Fatalf("切のプロジェクトの状態の変更: err=%v", err)
	}
	if res.SecretNotice != it.SecretNotice {
		t.Errorf("状態の変更の注記 %q が追記の注記 %q と違う", res.SecretNotice, it.SecretNotice)
	}
	if c, _ := e.emptyCommentCounts(e.issue); c != baseC+2 {
		t.Errorf("コメントの数 %d → %d（2 件増えるはず）", baseC, c)
	}

	// 要約はルールに関わらず拒む
	if _, err := e.s.AddComment(e.ctx, e.editor, off, e.issue.Row.ID, CommentInput{Text: digestHead + "token=abc123\n"}); errCode(err) != "comment_secret" {
		t.Errorf("切のプロジェクトの要約の秘密: %v, want comment_secret", err)
	}
	// 対照: 当たらない本文には注記が付かない
	it, err = e.s.AddComment(e.ctx, e.editor, off, e.issue.Row.ID, CommentInput{Text: "作業メモ"})
	if err != nil || it.SecretNotice != "" {
		t.Errorf("当たらない本文に注記が付いた: err=%v notice=%q", err, it.SecretNotice)
	}
}

// next の同時のコメント（setStatus を通る）。拒まれたのは候補ではなくコメントなので、次の候補へ見送らずにその理由で返し、
// どの候補も着手しない。切のプロジェクトでは着手して注記を返す。
func TestNextCommentSecret(t *testing.T) {
	e := newAttachEnv(t)
	statuses := func() []string {
		t.Helper()
		var out []string
		for _, it := range []*Issue{e.issue, e.issue2} {
			cur, err := e.s.Detail(e.ctx, e.demo, it.Row.ID)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, cur.Item.Status)
		}
		return out
	}
	before := statuses()
	if before[0] != "Todo" || before[1] != "Todo" {
		t.Fatalf("前提が崩れています: 候補が Todo でない: %v", before)
	}
	res, err := e.s.Next(e.ctx, e.editor, e.demo, NextOptions{Comment: secretComment})
	if errCode(err) != "comment_secret" {
		t.Fatalf("既定のプロジェクトの next: res=%+v err=%v, want comment_secret", res, err)
	}
	if after := statuses(); after[0] != "Todo" || after[1] != "Todo" {
		t.Errorf("拒んだ next が着手した: %v", after)
	}

	off := e.demo
	off.Rules = []byte(`{"comment":{"reject_secret":false}}`)
	res, err = e.s.Next(e.ctx, e.editor, off, NextOptions{Comment: secretComment})
	if err != nil || res.Action != "started" {
		t.Fatalf("切のプロジェクトの next: err=%v res=%+v", err, res)
	}
	if !strings.Contains(res.SecretNotice, "comment.reject_secret") || strings.Contains(res.SecretNotice, secretCommentValue) {
		t.Errorf("切のプロジェクトの next の注記: %q", res.SecretNotice)
	}
}

// next の同時のコメントの先頭が要約の印のとき（状態の変更に要約は載せない）。拒まれたのは候補ではなくコメントなので、
// 次の候補へ見送らずに digest_on_status で返し、どの候補も着手せず、コメントも残さない。対照: ふつうのコメントなら着手する。
func TestNextCommentDigestOnStatus(t *testing.T) {
	e := newAttachEnv(t)
	statuses := func() []string {
		t.Helper()
		var out []string
		for _, it := range []*Issue{e.issue, e.issue2} {
			cur, err := e.s.Detail(e.ctx, e.demo, it.Row.ID)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, cur.Item.Status)
		}
		return out
	}
	before := statuses()
	if before[0] != "Todo" || before[1] != "Todo" {
		t.Fatalf("前提が崩れています: 候補が Todo でない: %v", before)
	}
	baseC, baseE := e.emptyCommentCounts(e.issue)
	baseC2, baseE2 := e.emptyCommentCounts(e.issue2)
	res, err := e.s.Next(e.ctx, e.editor, e.demo, NextOptions{Comment: digestHead})
	if errCode(err) != "digest_on_status" || errKind(err) != Rejected {
		t.Fatalf("要約の印付きコメントの next: res=%+v err=%v, want Rejected digest_on_status", res, err)
	}
	if after := statuses(); after[0] != "Todo" || after[1] != "Todo" {
		t.Errorf("拒んだ next が着手した: %v", after)
	}
	if c, ev := e.emptyCommentCounts(e.issue); c != baseC || ev != baseE {
		t.Errorf("拒んだコメントが残った: コメント %d → %d・記録 %d → %d", baseC, c, baseE, ev)
	}
	if c, ev := e.emptyCommentCounts(e.issue2); c != baseC2 || ev != baseE2 {
		t.Errorf("拒んだコメントが 2 つ目の候補に残った: コメント %d → %d・記録 %d → %d", baseC2, c, baseE2, ev)
	}

	// 対照: ふつうのコメントなら着手する（拒む経路が候補を全部塞いでいるのではない）
	res, err = e.s.Next(e.ctx, e.editor, e.demo, NextOptions{Comment: "作業メモ: 着手します"})
	if err != nil || res.Action != "started" {
		t.Fatalf("ふつうのコメントの next: err=%v res=%+v", err, res)
	}
	if after := statuses(); after[0] != "In Progress" && after[1] != "In Progress" {
		t.Errorf("ふつうのコメントの next が着手しなかった: %v", after)
	}
}
