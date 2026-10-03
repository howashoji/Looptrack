package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
)

// コメントは追記専用で消せない。本文（前後の空白を除く）も添付も無いコメントは、経路を問わずここで拒む。
// 拒んだときは、コメントも記録（issue_events）も残さない。

// emptyCommentCounts はイシューのコメントの数と、コメントの記録の数。
func (e *attachEnv) emptyCommentCounts(it *Issue) (comments, events int) {
	e.t.Helper()
	cur, err := e.s.Detail(e.ctx, e.demo, it.Row.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return len(cur.Row.Doc.Comments), e.count("SELECT COUNT(*) FROM issue_events WHERE issue_id = ? AND kind = 'comment'", it.Row.ID)
}

func TestCommentRejectsEmptyWithoutAttachment(t *testing.T) {
	e := newAttachEnv(t)
	own := e.mustAttach(e.editor, e.issue, "own.txt", "own")
	other := e.mustAttach(e.editor, e.issue2, "other.txt", "other") // 別のイシューの添付

	// 拒否: 本文が空・空白だけ・改行とタブだけ。添付が無い（nil・空・重ねて指さない）
	baseC, baseE := e.emptyCommentCounts(e.issue)
	for _, c := range []struct {
		name, text string
		att        []int64
	}{
		{"空の本文", "", nil},
		{"空白だけ", "   ", nil},
		{"改行とタブだけ", "\n\t \r\n", []int64{}},
		{"全角の空白だけ", "　　", nil},
	} {
		_, err := e.s.CommentAttach(e.ctx, e.editor, e.demo, e.issue.Row.ID, c.text, c.att)
		var se *Error
		if !errors.As(err, &se) || se.Code != "comment_empty" || se.Kind != Invalid {
			t.Errorf("%s: err = %v, want Invalid comment_empty", c.name, err)
			continue
		}
		// 文面は日英の両方にある（言語は呼び出し側の Actor で固定する）
		if ja := i18n.Text(i18n.JA, se); !strings.Contains(ja, e.issue.Item.ID) || !strings.Contains(ja, "添付") {
			t.Errorf("%s: 日本語の文面 %q", c.name, ja)
		}
		if en := i18n.Text(i18n.EN, se); !strings.Contains(en, e.issue.Item.ID) || !strings.Contains(en, "attachment") {
			t.Errorf("%s: 英語の文面 %q", c.name, en)
		}
	}
	// 別のイシューの添付だけを付けても、添付の検査が先に拒む（comment_empty にはならず、何も残らない）
	if _, err := e.s.CommentAttach(e.ctx, e.editor, e.demo, e.issue.Row.ID, "", []int64{other.ID}); errCode(err) != "attachment_not_on_issue" {
		t.Errorf("別のイシューの添付だけ: %v", err)
	}
	// Comment（添付なしの入口）も同じ判定を通る
	if _, err := e.s.Comment(e.ctx, e.editor, e.demo, e.issue.Row.ID, "  "); errCode(err) != "comment_empty" {
		t.Errorf("Comment（空白だけ）: %v", err)
	}
	if c, ev := e.emptyCommentCounts(e.issue); c != baseC || ev != baseE {
		t.Fatalf("拒んだコメントが残った: コメント %d → %d・記録 %d → %d", baseC, c, baseE, ev)
	}

	// 対照: 本文だけ・添付だけ・空白の本文と添付は通る（拒否の経路が広すぎないこと）
	for _, c := range []struct {
		name, text string
		att        []int64
	}{
		{"本文だけ", "本文だけ", nil},
		{"本文の前後が空白", "  前後に空白  ", nil},
		{"添付だけ", "", []int64{own.ID}},
		{"空白の本文と添付", "   ", []int64{own.ID}},
	} {
		before, _ := e.emptyCommentCounts(e.issue)
		if _, err := e.s.CommentAttach(e.ctx, e.editor, e.demo, e.issue.Row.ID, c.text, c.att); err != nil {
			t.Errorf("対照（%s）が拒まれた: %v", c.name, err)
			continue
		}
		if after, _ := e.emptyCommentCounts(e.issue); after != before+1 {
			t.Errorf("対照（%s）: コメントが %d → %d で、1 件増えていない（前提が崩れています）", c.name, before, after)
		}
	}
	if d := e.lastEventDetail(e.issue.Row.ID, "comment"); len(idsOf(d["attachments"])) != 1 {
		t.Errorf("添付だけのコメントの記録に添付が付いていない: %v", d)
	}
}

// 状態の変更と同時のコメントが空白だけのときは、コメントを作らず、状態の変更は通す（本文のあるコメントは対照）。
func TestSetStatusWhitespaceCommentCreatesNoComment(t *testing.T) {
	e := newAttachEnv(t)
	baseC, _ := e.emptyCommentCounts(e.issue)
	res, err := e.s.SetStatus(e.ctx, e.editor, e.demo, e.issue.Row.ID, "In Progress", "  \n ", "")
	if err != nil {
		t.Fatalf("空白だけのコメントで状態の変更が拒まれた: %v", err)
	}
	if res.Issue.Item.Status != "In Progress" {
		t.Errorf("状態 = %q", res.Issue.Item.Status)
	}
	if res.CommentAdded {
		t.Errorf("空白だけのコメントで CommentAdded が true（応答の「コメント追記」の行が出てしまう）")
	}
	if c, _ := e.emptyCommentCounts(e.issue); c != baseC {
		t.Errorf("空白だけのコメントが作られた: %d → %d", baseC, c)
	}
	// 対照: 本文のあるコメントは、状態の変更と同時に作られる
	res, err = e.s.SetStatus(e.ctx, e.editor, e.demo, e.issue.Row.ID, "Todo", "戻します", "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.CommentAdded {
		t.Errorf("本文のあるコメントで CommentAdded が false（前提が崩れています）")
	}
	if c, _ := e.emptyCommentCounts(e.issue); c != baseC+1 {
		t.Errorf("本文のあるコメントが作られていない（前提が崩れています）: %d → %d", baseC, c)
	}
}
