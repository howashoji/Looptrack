package service

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// 最新のセッション要約（DESIGN.md §9-7・digest_latest.go）。summary と next に載せる値と「別のセッションか」の印。

func TestDigestExcerpt(t *testing.T) {
	// 見出し（branch・head・ai）+ 本文 6 行: 見出しは抜粋に入れず数えもしないので、本文の最後の行（次の一手）まで残る
	label, ex := DigestExcerpt("セッション要約:  home-mac \nbranch: main\nhead: ce768dc\nai: claude-code\n\n## 現在地\n- a\n- b\n- c\n- d\nNext: テストを足す\n")
	if label != "home-mac" {
		t.Errorf("ラベル %q", label)
	}
	if want := "## 現在地\n- a\n- b\n- c\n- d\nNext: テストを足す"; ex != want {
		t.Errorf("抜粋 %q, want %q", ex, want)
	}
	// 見出し + 本文 9 行: 本文の先頭の DigestExcerptLines 行（空行は数えずに落とす）。見出しの読み方は検査と同じ
	// （大小・全角のコロン・前後の空白を問わない）
	body9 := "## 現在地\n- 1\n- 2\n- 3\n- 4\n- 5\n- 6\n- 7\n- 8\n"
	_, ex = DigestExcerpt("セッション要約: m\nBranch： main\n head : ce768dc\nAI: codex\n" + body9)
	if want := "## 現在地\n- 1\n- 2\n- 3\n- 4\n- 5\n- 6\n- 7"; ex != want {
		t.Errorf("見出し + 本文 9 行: %q, want %q", ex, want)
	}
	// 対照: 見出しの無い本文は以前と同じ（印の行を除いた、空でない先頭の DigestExcerptLines 行）
	_, ex = DigestExcerpt("セッション要約: m\n\n" + body9)
	if want := "## 現在地\n- 1\n- 2\n- 3\n- 4\n- 5\n- 6\n- 7"; ex != want {
		t.Errorf("見出しの無い本文: %q, want %q", ex, want)
	}
	// 対照: 見出しの範囲（先頭の DigestExcerptLines 行）より後ろの「head: …」は本文として残す
	_, ex = DigestExcerpt("セッション要約: m\n- 1\n- 2\n- 3\n- 4\n- 5\n- 6\n- 7\nhead: 本文の行\n")
	if want := "- 1\n- 2\n- 3\n- 4\n- 5\n- 6\n- 7\nhead: 本文の行"; ex != want {
		t.Errorf("範囲の外の head: %q, want %q", ex, want)
	}
	// 英語の印・CRLF・空行（見出しだけの要約の抜粋は空）
	label, ex = DigestExcerpt("Session digest: work-pc\r\n\r\nbranch: x\r\n\r\nNext: y\r\n")
	if label != "work-pc" || ex != "Next: y" {
		t.Errorf("英語の印: %q / %q", label, ex)
	}
	if _, ex = DigestExcerpt("Session digest: work-pc\r\nbranch: x\r\n"); ex != "" {
		t.Errorf("見出しだけ: %q", ex)
	}
	// 文字数の上限（切ったら「…」）
	_, ex = DigestExcerpt("セッション要約: m\n" + strings.Repeat("あ", DigestExcerptChars+50))
	if utf8.RuneCountInString(ex) != DigestExcerptChars+1 || !strings.HasSuffix(ex, "…") {
		t.Errorf("上限: %d 文字 %q", utf8.RuneCountInString(ex), ex[len(ex)-10:])
	}
	// 対照: 上限ちょうどは切らない
	_, ex = DigestExcerpt("セッション要約: m\n" + strings.Repeat("あ", DigestExcerptChars))
	if utf8.RuneCountInString(ex) != DigestExcerptChars || strings.HasSuffix(ex, "…") {
		t.Errorf("上限ちょうど: %d 文字", utf8.RuneCountInString(ex))
	}
}

// 要約を書いたセッションと要求のセッションが違うときだけ other_session が true になり、同じなら false、
// 比べられない（要求にセッション ID が無い・CLI と MCP の接続 ID）なら印を持たない。要約が無いイシューは載らず、
// 最新の要約（後から書いたもの）が勝つ。要約でないコメントは最新の要約を変えない。
func TestLatestDigestsSession(t *testing.T) {
	e := newAttachEnv(t)
	writer := e.editor
	writer.SessionID = "sess-A"
	first := digestHead + "\n## 現在地\n- 1 つ目\n"
	if _, err := e.s.AddComment(e.ctx, writer, e.demo, e.issue.Row.ID, CommentInput{Text: first}); err != nil {
		t.Fatal(err)
	}
	ids := []string{e.issue.Item.ID, e.issue2.Item.ID}
	get := func(sess string) map[string]*LatestDigest {
		t.Helper()
		a := e.editor
		a.SessionID = sess
		ds, err := e.s.LatestDigests(e.ctx, a, e.demo, ids)
		if err != nil {
			t.Fatal(err)
		}
		return ds
	}
	ds := get("sess-B")
	d := ds[e.issue.Item.ID]
	if d == nil {
		t.Fatalf("要約が載らない: %v", ds)
	}
	if !d.Other() || d.SessionID != "sess-A" || d.MachineLabel != "home-mac" || !strings.HasPrefix(d.Excerpt, "## 現在地\n") ||
		!strings.Contains(d.Excerpt, "- 1 つ目") || d.CreatedAt.IsZero() {
		t.Errorf("別のセッション: %+v", d)
	}
	if _, ok := ds[e.issue2.Item.ID]; ok || len(ds) != 1 {
		t.Errorf("要約の無いイシューが載った: %v", ds)
	}
	if d := get("sess-A")[e.issue.Item.ID]; d == nil || d.OtherSession == nil || *d.OtherSession || d.Other() {
		t.Errorf("同じセッションは other_session = false のはず: %+v", d)
	}
	for _, sess := range []string{"", MCPSessionPrefix + "c1"} {
		if d := get(sess)[e.issue.Item.ID]; d == nil || d.OtherSession != nil {
			t.Errorf("比べられないセッション %q は印を持たないはず: %+v", sess, d)
		}
	}

	// 後から書いた要約が勝つ（別の端末・別のセッション）。要約でないコメントは最新の要約を変えない
	other := e.editor
	other.SessionID = "sess-C"
	second := "Session digest: work-pc\nbranch: main\nhead: 0123abc\nai: codex\n\nsecond"
	if _, err := e.s.AddComment(e.ctx, other, e.demo, e.issue.Row.ID, CommentInput{Text: second}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.AddComment(e.ctx, writer, e.demo, e.issue.Row.ID, CommentInput{Text: "ただのメモ"}); err != nil {
		t.Fatal(err)
	}
	d = get("sess-A")[e.issue.Item.ID]
	if d == nil || d.MachineLabel != "work-pc" || d.SessionID != "sess-C" || !d.Other() || !strings.HasSuffix(d.Excerpt, "second") {
		t.Errorf("最新の要約: %+v", d)
	}
	// 一覧が空なら何も引かない
	if ds, err := e.s.LatestDigests(e.ctx, e.editor, e.demo, nil); err != nil || len(ds) != 0 {
		t.Errorf("空の一覧: %v %v", ds, err)
	}
}

// next は返すイシューの最新の要約を添える（resumed のとき。別の端末から引き継いだ要約が見える）。
func TestNextLatestDigest(t *testing.T) {
	e := newAttachEnv(t)
	me := e.editor
	me.SessionID = "sess-B"
	if _, err := e.s.SetStatus(e.ctx, me, e.demo, e.issue.Row.ID, "In Progress", "", ""); err != nil {
		t.Fatal(err)
	}
	// 対照: 要約が無ければ nil
	res, err := e.s.Next(e.ctx, me, e.demo, NextOptions{})
	if err != nil || res.Action != "resumed" || res.Issue.Item.ID != e.issue.Item.ID {
		t.Fatalf("前提が崩れています: %+v %v", res, err)
	}
	if res.LatestDigest != nil {
		t.Errorf("要約が無いのに載った: %+v", res.LatestDigest)
	}
	writer := e.editor
	writer.SessionID = "sess-A"
	if _, err := e.s.AddComment(e.ctx, writer, e.demo, e.issue.Row.ID, CommentInput{Text: digestHead + "\n次の一手: テスト"}); err != nil {
		t.Fatal(err)
	}
	res, err = e.s.Next(e.ctx, me, e.demo, NextOptions{})
	if err != nil || res.Action != "resumed" {
		t.Fatalf("next: %+v %v", res, err)
	}
	if !res.LatestDigest.Other() || res.LatestDigest.MachineLabel != "home-mac" || !strings.Contains(res.LatestDigest.Excerpt, "次の一手: テスト") {
		t.Errorf("next の要約: %+v", res.LatestDigest)
	}
}
