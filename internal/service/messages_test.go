package service

import (
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/testutil"
)

// 連絡の service（messages.go）のテスト。土台は添付のテストと同じ attachEnv（アプリ用の DB 利用者で動かす）。
// demo の参加者は editor（ed）と viewer（vi）。root は利用者の admin で demo に参加していない。out は other だけの参加者。

func (e *attachEnv) send(a Actor, to, user, session, body, issue string) (*store.Message, error) {
	return e.s.SendMessage(e.ctx, a, MessageSendInput{Project: e.demo.Slug, To: to, User: user, Session: session, Body: body, Issue: issue})
}

func (e *attachEnv) login(a Actor) string {
	t := e.t
	t.Helper()
	u, err := store.UserByID(e.ctx, e.s.DB, a.UserID)
	if err != nil {
		t.Fatal(err)
	}
	return u.Login
}

func (e *attachEnv) unreadBodies(a Actor) []string {
	e.t.Helper()
	res, err := e.s.Inbox(e.ctx, a, e.demo.Slug, InboxQuery{Unread: true})
	if err != nil {
		e.t.Fatalf("受信箱: %v", err)
	}
	if res.Unread != len(res.Messages) || len(res.UnreadIDs) != res.Unread {
		e.t.Fatalf("未読の数が合わない: unread=%d messages=%d ids=%d", res.Unread, len(res.Messages), len(res.UnreadIDs))
	}
	out := []string{}
	for _, m := range res.Messages {
		out = append(out, m.Body)
	}
	return out
}

func inSession(a Actor, id string) Actor {
	a.SessionID = id
	return a
}

// 宛先でない利用者には見えず、宛先には見える（同じテストの対照）。読める者は宛先と送り手だけ。
func TestMessageVisibility(t *testing.T) {
	e := newAttachEnv(t)
	toViewer, err := e.send(e.editor, store.MessageToUser, e.login(e.viewer), "", "viewer だけへの知らせ", "")
	if err != nil {
		t.Fatalf("送信: %v", err)
	}
	toSess, err := e.send(e.editor, store.MessageToSession, e.login(e.viewer), "sess-v", "viewer のセッションへの知らせ", "")
	if err != nil {
		t.Fatalf("送信: %v", err)
	}
	all, err := e.send(e.editor, store.MessageToProject, "", "", "全員への知らせ", "")
	if err != nil {
		t.Fatalf("送信: %v", err)
	}
	// 宛先の利用者には見える（対照）。Web と手で打つ CLI のようにセッションの無い読み手は、持ち主として別のセッション宛ても見る
	if got := e.unreadBodies(e.viewer); len(got) != 3 {
		t.Fatalf("宛先の未読: %q（3 件のはず）", got)
	}
	// 宛先でない利用者: 参加していない管理者はプロジェクト宛てだけ読める。他人宛ては一覧にも 1 件の取得にも出ない
	if got := e.unreadBodies(e.root); len(got) != 1 || got[0] != "全員への知らせ" {
		t.Errorf("参加していない管理者の未読: %q", got)
	}
	for _, id := range []int64{toViewer.ID, toSess.ID} {
		if _, err := e.s.GetMessage(e.ctx, e.root, e.demo.Slug, id); memErrCode(err) != "not_found" {
			t.Errorf("管理者が他人宛て %d を取得: %v", id, err)
		}
		if _, err := e.s.GetMessage(e.ctx, e.viewer, e.demo.Slug, id); err != nil {
			t.Errorf("宛先が %d を取得: %v", id, err)
		}
		if _, err := e.s.GetMessage(e.ctx, e.editor, e.demo.Slug, id); err != nil {
			t.Errorf("送り手が %d を取得: %v", id, err)
		}
	}
	// 他人のセッション ID を名乗っても読めない（持ち主は利用者で決まる）
	if got := e.unreadBodies(inSession(e.root, "sess-v")); len(got) != 1 {
		t.Errorf("セッション ID を名乗った管理者の未読: %q", got)
	}
	// 参加していない利用者は 404
	if _, err := e.s.Inbox(e.ctx, e.outside, e.demo.Slug, InboxQuery{}); memErrCode(err) != "not_found" {
		t.Errorf("参加者でない受信箱: %v", err)
	}
	if _, err := e.s.GetMessage(e.ctx, e.outside, e.demo.Slug, all.ID); memErrCode(err) != "not_found" {
		t.Errorf("参加者でない取得: %v", err)
	}
	// 読めない連絡の id は既読にしても何も足さない
	if n, err := e.s.MarkMessagesRead(e.ctx, e.root, e.demo.Slug, []int64{toViewer.ID}); err != nil || n != 0 {
		t.Errorf("他人宛ての既読: %d %v", n, err)
	}
	// 送った一覧は送り手にだけ出る
	sent, err := e.s.Inbox(e.ctx, e.editor, e.demo.Slug, InboxQuery{Sent: true})
	if err != nil || len(sent.Messages) != 3 {
		t.Fatalf("送った一覧: %+v %v", sent, err)
	}
	if sent, err := e.s.Inbox(e.ctx, e.viewer, e.demo.Slug, InboxQuery{Sent: true}); err != nil || len(sent.Messages) != 0 {
		t.Errorf("送っていない利用者の送った一覧: %+v %v", sent, err)
	}
}

// viewer は送れない。参加者でない宛先は、無い利用者と同じ理由で拒む。
func TestMessageSendPermissions(t *testing.T) {
	e := newAttachEnv(t)
	if _, err := e.send(e.viewer, store.MessageToProject, "", "", "viewer から", ""); memErrCode(err) != "forbidden" {
		t.Errorf("viewer の送信: %v", err)
	}
	if _, err := e.send(e.root, store.MessageToProject, "", "", "参加していない管理者から", ""); memErrCode(err) != "forbidden" {
		t.Errorf("参加していない管理者の送信: %v", err)
	}
	if _, err := e.send(e.outside, store.MessageToProject, "", "", "外から", ""); memErrCode(err) != "not_found" {
		t.Errorf("参加者でない送信: %v", err)
	}
	var texts []string
	for _, login := range []string{e.login(e.outside), "no-such-user"} {
		_, err := e.send(e.editor, store.MessageToUser, login, "", "届かない", "")
		if memErrCode(err) != "message_recipient" {
			t.Fatalf("%s: %v", login, err)
		}
		texts = append(texts, i18n.Text(i18n.JA, err))
	}
	if texts[0] != texts[1] {
		t.Errorf("参加者でない宛先と無い利用者で理由が違う: %q / %q", texts[0], texts[1])
	}
	if e.count("SELECT COUNT(*) FROM messages") != 0 {
		t.Errorf("拒んだ連絡が残った")
	}
	// 宛先の組み合わせ
	for _, c := range []struct{ to, user, session string }{
		{"", "", ""}, {"everyone", "", ""}, {store.MessageToProject, "me", ""}, {store.MessageToProject, "", "s"},
		{store.MessageToUser, "", ""}, {store.MessageToUser, "me", "s"}, {store.MessageToSession, "me", ""}, {store.MessageToSession, "", "s"},
	} {
		if _, err := e.send(e.editor, c.to, c.user, c.session, "本文", ""); memErrCode(err) != "invalid_argument" {
			t.Errorf("%+v: %v", c, err)
		}
	}
	// 対照: 自分宛て（me）は通る
	if _, err := e.send(e.editor, store.MessageToUser, "me", "", "自分への覚え書き", ""); err != nil {
		t.Fatalf("自分宛て: %v", err)
	}
}

// 本文の検査。資格情報の形は拒まれ、理由に値が出ない。
func TestMessageBodyChecks(t *testing.T) {
	e := newAttachEnv(t)
	_, err := e.send(e.editor, store.MessageToProject, "", "", "接続の設定\n"+tSecretValue+"\n", "")
	if memErrCode(err) != "message_secret" {
		t.Fatalf("資格情報: %v", err)
	}
	for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
		if msg := i18n.Text(lang, err); strings.Contains(msg, "Synthetic0Value9") || !strings.Contains(msg, "2") {
			t.Errorf("理由（%s）に値が出ているか行番号が無い: %s", lang, msg)
		}
	}
	if _, err := e.send(e.editor, store.MessageToSession, "me", tSecretValue, "本文", ""); memErrCode(err) != "message_secret" {
		t.Errorf("宛先のセッションの資格情報: %v", err)
	}
	if _, err := e.s.SendMessage(e.ctx, e.editor, MessageSendInput{Project: e.demo.Slug, To: "project", Body: "本文", MachineLabel: tSecretValue}); memErrCode(err) != "message_secret" && memErrCode(err) != "invalid_argument" {
		t.Errorf("端末のラベルの資格情報: %v", err)
	}
	if _, err := e.send(e.editor, store.MessageToProject, "", "", strings.Repeat("あ", MaxMessageBytes/3+1), ""); memErrCode(err) != "message_too_large" {
		t.Errorf("上限を超える本文: %v", err)
	}
	for _, body := range []string{"", " \n", "a\x1b[2Jb", "a\x00b", string([]byte{0xff})} {
		if _, err := e.send(e.editor, store.MessageToProject, "", "", body, ""); memErrCode(err) != "invalid_argument" {
			t.Errorf("本文 %q: %v", body, err)
		}
	}
	if n := e.count("SELECT COUNT(*) FROM messages"); n != 0 {
		t.Errorf("拒んだ連絡が %d 件残った", n)
	}
	// 対照: 上限ちょうどの本文と改行・タブは通る
	if _, err := e.send(e.editor, store.MessageToProject, "", "", strings.Repeat("a", MaxMessageBytes-3)+"\n\t\n", ""); err != nil {
		t.Fatalf("上限ちょうどの本文: %v", err)
	}
}

// 既読はセッションごと。プロジェクト宛ては同じ利用者の別のセッションにも届き、送ったセッションには届かない。
func TestMessageReadsPerSession(t *testing.T) {
	e := newAttachEnv(t)
	me := inSession(e.editor, "sess-send")
	if _, err := e.send(me, store.MessageToProject, "", "", "全員へ", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.send(e.editor, store.MessageToSession, e.login(e.viewer), "sess-a", "A だけへ", ""); err != nil {
		t.Fatal(err)
	}
	a, b := inSession(e.viewer, "sess-a"), inSession(e.viewer, "sess-b")
	if got := e.unreadBodies(a); len(got) != 2 || got[0] != "A だけへ" {
		t.Fatalf("A の未読: %q（セッション宛てが先のはず）", got)
	}
	if got := e.unreadBodies(b); len(got) != 1 || got[0] != "全員へ" {
		t.Fatalf("B の未読: %q", got)
	}
	// 送ったセッションには届かず、同じ利用者の別のセッションには届く
	if got := e.unreadBodies(me); len(got) != 0 {
		t.Errorf("送ったセッションの未読: %q", got)
	}
	if got := e.unreadBodies(inSession(e.editor, "sess-other")); len(got) != 1 {
		t.Errorf("送り手の別のセッションの未読: %q", got)
	}
	res, err := e.s.Inbox(e.ctx, a, e.demo.Slug, InboxQuery{Unread: true})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := e.s.MarkMessagesRead(e.ctx, a, e.demo.Slug, res.UnreadIDs); err != nil || n != 2 {
		t.Fatalf("既読: %d %v", n, err)
	}
	// 2 度目は足さない
	if n, err := e.s.MarkMessagesRead(e.ctx, a, e.demo.Slug, res.UnreadIDs); err != nil || n != 0 {
		t.Fatalf("2 度目の既読: %d %v", n, err)
	}
	if got := e.unreadBodies(a); len(got) != 0 {
		t.Errorf("既読の後の A: %q", got)
	}
	if got := e.unreadBodies(b); len(got) != 1 {
		t.Errorf("A の既読が B に効いた: %q", got)
	}
	// 既読も含めた一覧には残る
	if all, err := e.s.Inbox(e.ctx, a, e.demo.Slug, InboxQuery{}); err != nil || len(all.Messages) != 2 || !all.Messages[0].Read {
		t.Errorf("一覧: %+v %v", all, err)
	}
	if n, err := e.s.UnreadMessages(e.ctx, b, e.demo.Slug); err != nil || n != 1 {
		t.Errorf("未読の件数: %d %v", n, err)
	}
}

// イシューを添えると「連絡:」のコメントが残る。別のプロジェクトのイシューは拒み、連絡もコメントも残さない。
func TestMessageIssueComment(t *testing.T) {
	e := newAttachEnv(t)
	it := e.issue
	m, err := e.send(e.editor, store.MessageToUser, e.login(e.viewer), "", "このイシューの検査を回します", it.Item.ID)
	if err != nil {
		t.Fatalf("送信: %v", err)
	}
	if m.IssueDisplay != it.Item.ID {
		t.Errorf("連絡のイシュー: %q", m.IssueDisplay)
	}
	if n := e.count("SELECT COUNT(*) FROM comments WHERE content LIKE '連絡: このイシューの検査を回します%'"); n != 1 {
		t.Errorf("コメントの数: %d", n)
	}
	if _, err := e.send(e.editor, store.MessageToProject, "", "", "無いイシュー", "XX-9999"); memErrCode(err) != "not_found" {
		t.Errorf("無いイシュー: %v", err)
	}
	if n := e.count("SELECT COUNT(*) FROM messages"); n != 1 {
		t.Errorf("連絡の行の数: %d", n)
	}
}

// 保持期間を過ぎた連絡は読みの経路に出ない。0 は無期限（行は消えない）。
func TestMessageRetention(t *testing.T) {
	e := newAttachEnv(t)
	base := e.s.Now
	e.s.Now = func() time.Time { return base().AddDate(0, 0, -40) }
	old, err := e.send(e.editor, store.MessageToUser, e.login(e.viewer), "", "古い知らせ", "")
	e.s.Now = base
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.send(e.editor, store.MessageToUser, e.login(e.viewer), "", "新しい知らせ", ""); err != nil {
		t.Fatal(err)
	}
	if got := e.unreadBodies(e.viewer); len(got) != 1 || got[0] != "新しい知らせ" {
		t.Errorf("既定の 30 日: %q", got)
	}
	if _, err := e.s.GetMessage(e.ctx, e.viewer, e.demo.Slug, old.ID); memErrCode(err) != "not_found" {
		t.Errorf("期限切れの取得: %v", err)
	}
	if _, _, err := store.SetMessageRetentionDays(e.ctx, e.admin, 0, store.SettingChange{Via: "command"}); err != nil {
		t.Fatal(err)
	}
	if got := e.unreadBodies(e.viewer); len(got) != 2 {
		t.Errorf("無期限: %q（対照。行は残っているので 2 件のはず）", got)
	}
}

// 連絡と既読の表は追記専用（アプリ用の利用者で UPDATE・DELETE できない）。
func TestMessageTablesAreAppendOnly(t *testing.T) {
	e := newAttachEnv(t)
	m, err := e.send(e.editor, store.MessageToProject, "", "", "残る知らせ", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.MarkMessagesRead(e.ctx, e.viewer, e.demo.Slug, []int64{m.ID}); err != nil {
		t.Fatalf("前提が崩れています: アプリ用の利用者で既読を足せない: %v", err)
	}
	want := "command denied"
	if testutil.SQLite() {
		want = "append-only"
	}
	for _, stmt := range []string{"UPDATE messages SET body = 'x' WHERE id = ?", "DELETE FROM messages WHERE id = ?",
		"UPDATE message_reads SET session_id = 'x' WHERE message_id = ?", "DELETE FROM message_reads WHERE message_id = ?"} {
		_, err := e.app.ExecContext(e.ctx, stmt, m.ID)
		if !store.IsAppendOnlyViolation(err) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q で拒否", stmt, err, want)
		}
	}
}

// アーカイブしたプロジェクトは参加者の一覧から外れるので、無いものと同じ 404 になる。
func TestMessageArchivedProject(t *testing.T) {
	e := newAttachEnv(t)
	if _, err := e.admin.ExecContext(e.ctx, "UPDATE projects SET archived_at = ? WHERE id = ?", e.s.Now(), e.demo.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.send(e.editor, store.MessageToProject, "", "", "届かない", ""); memErrCode(err) != "not_found" {
		t.Errorf("アーカイブ済みへの送信: %v", err)
	}
}

// 参加していない管理者と無効の利用者は宛先にならない（参加者でない利用者・無い利用者と同じ理由）。別のプロジェクトのイシューは添えられない。
func TestMessageRecipientIsMemberOnly(t *testing.T) {
	e := newAttachEnv(t)
	if _, err := e.send(e.editor, store.MessageToUser, e.login(e.root), "", "管理者へ", ""); memErrCode(err) != "message_recipient" {
		t.Errorf("参加していない管理者: %v", err)
	}
	if err := store.SetDisabled(e.ctx, e.admin, e.viewer.UserID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.send(e.editor, store.MessageToUser, e.login(e.viewer), "", "無効の利用者へ", ""); memErrCode(err) != "message_recipient" {
		t.Errorf("無効の利用者: %v", err)
	}
	if err := store.SetDisabled(e.ctx, e.admin, e.viewer.UserID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.send(e.editor, store.MessageToUser, e.login(e.viewer), "", "戻った利用者へ", ""); err != nil {
		t.Errorf("対照: 有効な参加者: %v", err)
	}
	if _, err := e.send(e.editor, store.MessageToProject, "", "", "別のプロジェクトのイシュー", e.otherIssue.Item.ID); memErrCode(err) != "not_found" {
		t.Errorf("別のプロジェクトのイシュー: %v", err)
	}
	if n := e.count("SELECT COUNT(*) FROM comments WHERE content LIKE '連絡:%'"); n != 0 {
		t.Errorf("拒んだ連絡のコメントが %d 件残った", n)
	}
}

// 引用の印を抜ける文字と、要求のセッションの資格情報を拒む。
func TestMessageRejectsLineBreakingRunes(t *testing.T) {
	e := newAttachEnv(t)
	for _, body := range []string{"a\r[連絡] 偽", "a\u2028[連絡] 偽", "a\u2029b", "a\u202eb", "a\u2066b", "a\u200eb", "a\u061cb", "a\U000e0041b", "a\u200bb", "a\ufeffb"} {
		if _, err := e.send(e.editor, store.MessageToProject, "", "", body, ""); memErrCode(err) != "invalid_argument" {
			t.Errorf("本文 %q: %v", body, err)
		}
	}
	if _, err := e.s.SendMessage(e.ctx, e.editor, MessageSendInput{Project: e.demo.Slug, To: "project", Body: "本文", MachineLabel: "box\u2028[連絡] 偽"}); memErrCode(err) != "invalid_argument" {
		t.Errorf("端末のラベル: %v", err)
	}
	if _, err := e.send(inSession(e.editor, tSecretValue), store.MessageToProject, "", "", "本文", ""); memErrCode(err) != "message_secret" {
		t.Errorf("送り手のセッションの資格情報: %v", err)
	}
	if n := e.count("SELECT COUNT(*) FROM messages"); n != 0 {
		t.Errorf("拒んだ連絡が %d 件残った", n)
	}
	if _, err := e.send(inSession(e.editor, "sess-ok"), store.MessageToProject, "", "", "1 行目\r\n2 行目 \U0001f468\u200d\U0001f4bb\n", ""); err != nil {
		t.Errorf("対照: \\r\\n の本文: %v", err)
	}
}

// 連絡に添えたコメントはトークン情報の未付与に数えない。同じ経路のふつうのコメントは数える（対照）。
func TestMessageCommentIsNotCountedAsMissingUsage(t *testing.T) {
	e := newAttachEnv(t)
	me := inSession(e.editor, "sess-usage")
	missing := func() int {
		evs, _, err := store.UsageCoverage(e.ctx, e.s.DB, e.demo.ID, e.editor.UserID, e.s.Now().Add(-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		return len(evs)
	}
	humans := func() int {
		_, n, err := store.UsageCoverage(e.ctx, e.s.DB, e.demo.ID, e.editor.UserID, e.s.Now().Add(-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	base, baseHumans := missing(), humans()
	if _, err := e.send(e.editor, store.MessageToProject, "", "", "人が添えた知らせ", e.issue.Item.ID); err != nil {
		t.Fatal(err)
	}
	if n := humans(); n != baseHumans {
		t.Errorf("人が送った連絡のコメントが数えられた: %d → %d", baseHumans, n)
	}
	if _, err := e.send(me, store.MessageToProject, "", "", "イシューに添えた知らせ", e.issue.Item.ID); err != nil {
		t.Fatal(err)
	}
	if n := missing(); n != base {
		t.Errorf("連絡のコメントが数えられた: %d → %d", base, n)
	}
	row, err := e.s.Locate(e.ctx, e.issue.Item.ID, e.demo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Comment(e.ctx, me, e.demo, row.ID, "ふつうのコメント"); err != nil {
		t.Fatal(err)
	}
	if n := missing(); n != base+1 {
		t.Errorf("前提が崩れています: ふつうのコメントが数えられない: %d → %d", base, n)
	}
	if _, err := e.s.Comment(e.ctx, e.editor, e.demo, row.ID, "人のコメント"); err != nil {
		t.Fatal(err)
	}
	if n := humans(); n != baseHumans+1 {
		t.Errorf("前提が崩れています: 人のコメントが数えられない: %d → %d", baseHumans, n)
	}
}
