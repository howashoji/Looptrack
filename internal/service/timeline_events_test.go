package service

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/testutil"
)

// timelineEvents は demo の、通し番号が after より大きい自動の出来事を返す。
func (e *attachEnv) timelineEvents(after int64) []store.TimelineEvent {
	e.t.Helper()
	evs, err := store.TimelineEventsAfter(e.ctx, e.app, e.demo.ID, after, 1000)
	if err != nil {
		e.t.Fatal(err)
	}
	return evs
}

func timelineKinds(evs []store.TimelineEvent) []string {
	out := []string{}
	for _, ev := range evs {
		out = append(out, ev.Kind)
	}
	return out
}

// editor2 は demo に editor で参加する 2 人目の利用者。
func (e *attachEnv) editor2(session string) Actor {
	e.t.Helper()
	id, err := store.CreateUser(e.ctx, e.admin, "ed2", "ed2", "x", "member")
	if err != nil {
		e.t.Fatal(err)
	}
	a := Actor{UserID: id, Via: "cli", Lang: i18n.JA, SessionID: session}
	e.joinDemo(a, "editor")
	return a
}

// 設計書の出来事の表の 8 種（起票・状態の変更・担当の変更・コメント・占有の取得と返却と強制解除・セッションの開始・終了・コミット）が
// それぞれ載る。同じ操作の流れの中で、載せない種類（占有の延長・assignee_override・next の自動の担当・本文の更新）は載らない。
// 載らない側は、元の記録（issue_events・lease_events）には書かれていることを数えて前提にする（経路が死んでいて載らないのではない）。
func TestTimelineEventsKindsAndExclusions(t *testing.T) {
	e := newAttachEnv(t)
	ed := inSession(e.editor, "sess-a")
	ed2 := e.editor2("sess-b")
	base := e.headSeq()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(e.s.ReportPresence(e.ctx, ed, PresenceInput{Project: e.demo.Slug, Agent: "claude-code", MachineLabel: "box-a", Branch: "feat-x", Start: true}))
	it, err := e.s.Create(e.ctx, ed, e.demo, CreateInput{Title: "出来事の確認"}, i18n.JA)
	must(err)
	// In Progress への変更で担当が自動で付く（next と同じ。detail の auto）。状態の変更の 1 行にまとめ、担当の行は出さない
	_, err = e.s.SetStatus(e.ctx, ed, e.demo, it.Row.ID, "In Progress", "着手します", "")
	must(err)
	_, err = e.s.AddComment(e.ctx, ed, e.demo, it.Row.ID, CommentInput{Text: "途中の記録"})
	must(err)
	// 引き取り（assign と assignee_takeover の両方が書かれる。後者は印だけ）
	_, err = e.s.Assign(e.ctx, ed2, e.demo, it.Row.ID, "me", "引き継ぎます")
	must(err)
	// 担当が他人のイシューの本文を担当を替えずに直す（update と assignee_override。どちらも載せない）
	cur, err := e.s.Detail(e.ctx, e.demo, it.Row.ID)
	must(err)
	title := "出来事の確認（直した）"
	_, err = e.s.Update(e.ctx, ed, e.demo, it.Row.ID, cur.Row.Version, Patch{Title: &title, OverrideReason: "誤字を直します"})
	must(err)
	got, err := e.s.AcquireLease(e.ctx, ed, LeaseInput{Project: e.demo.Slug, Name: "deploy", Purpose: "本番の反映"})
	must(err)
	_, err = e.s.ExtendLease(e.ctx, ed, LeaseInput{Project: e.demo.Slug, Name: "deploy", Token: got.Token, TTLSeconds: 600})
	must(err)
	must(e.s.ReleaseLease(e.ctx, ed, LeaseInput{Project: e.demo.Slug, Name: "deploy", Token: got.Token}))
	_, err = e.s.AcquireLease(e.ctx, ed, LeaseInput{Project: e.demo.Slug, Name: "deploy", Purpose: "もう一度"})
	must(err)
	_, err = e.s.BreakLease(e.ctx, ed2, LeaseInput{Project: e.demo.Slug, Name: "deploy", Reason: "止まったまま"})
	must(err)
	_, err = e.s.RecordTimelineCommits(e.ctx, ed, TimelineCommitsInput{Project: e.demo.Slug, Branch: "feat-x",
		Commits: []TimelineCommit{{SHA: "abc1234", Subject: "出来事を足す"}}})
	must(err)
	must(e.s.EndPresence(e.ctx, ed, e.demo.Slug))

	evs := e.timelineEvents(base)
	want := []string{store.TimelineEventSessionStart, store.TimelineEventIssueCreate, store.TimelineEventIssueStatus, store.TimelineEventIssueComment,
		store.TimelineEventIssueAssign, store.TimelineEventLeaseAcquire, store.TimelineEventLeaseRelease, store.TimelineEventLeaseAcquire,
		store.TimelineEventLeaseBreak, store.TimelineEventCommit, store.TimelineEventSessionEnd}
	if !slices.Equal(timelineKinds(evs), want) {
		t.Fatalf("載った出来事: %v\nwant %v", timelineKinds(evs), want)
	}
	for i, ev := range evs {
		if ev.Seq != base+int64(i)+1 {
			t.Errorf("%d 件目の通し番号 %d（%d のはず）", i, ev.Seq, base+int64(i)+1)
		}
		if ev.ActorUserID == ed.UserID && ev.ActorMachine != "box-a" {
			t.Errorf("%s の端末のラベル %q（在席の box-a のはず）", ev.Kind, ev.ActorMachine)
		}
	}
	if d := evs[2].Detail; d["from"] != "Todo" || d["to"] != "In Progress" || d["comment"] != true || d["issue"] != it.Item.ID {
		t.Errorf("状態の変更の材料: %v", d)
	}
	if d := evs[4].Detail; d["to"] != "ed2" || d["takeover"] != true || evs[4].ActorSession != "sess-b" {
		t.Errorf("引き取りの材料: %v %q", d, evs[4].ActorSession)
	}
	if d := evs[8].Detail; d["name"] != "deploy" || d["reason"] != "止まったまま" {
		t.Errorf("強制解除の材料: %v", d)
	}
	if d := evs[9].Detail; d["sha"] != "abc1234" || d["subject"] != "出来事を足す" || d["branch"] != "feat-x" {
		t.Errorf("コミットの材料: %v", d)
	}
	// 前提: 載らない種類も元の記録には書かれている
	for kind, q := range map[string]string{
		"auto の assign":     "SELECT COUNT(*) FROM issue_events WHERE issue_id = ? AND kind = 'assign' AND detail LIKE '%\"auto\"%'",
		"assignee_override": "SELECT COUNT(*) FROM issue_events WHERE issue_id = ? AND kind = 'assignee_override'",
		"update":            "SELECT COUNT(*) FROM issue_events WHERE issue_id = ? AND kind = 'update'",
	} {
		if n := e.count(q, it.Row.ID); n != 1 {
			t.Errorf("前提が崩れています: issue_events の %s が %d 件", kind, n)
		}
	}
	if n := e.count("SELECT COUNT(*) FROM lease_events WHERE action = 'extend'"); n != 1 {
		t.Errorf("前提が崩れています: lease_events の extend が %d 件", n)
	}
}

// 何を載せるかの規則（timelineEventsOf）を源の並びで確かめる。載る源と並べ、載せない種類と行った者の分からない記録が落ちることを見る。
func TestTimelineEventsOfRule(t *testing.T) {
	at := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	src := func(origin, kind string, detail map[string]any) timelineSource {
		return timelineSource{Origin: origin, Kind: kind, Detail: detail, ProjectID: 1, IssueID: 5, UserID: 7, At: at}
	}
	srcs := []timelineSource{
		src(timelineFromIssue, "create", nil),
		src(timelineFromIssue, "update", map[string]any{}),
		src(timelineFromIssue, "verify", map[string]any{}),
		src(timelineFromIssue, "attach", map[string]any{}),
		src(timelineFromIssue, "attach_purge", map[string]any{}),
		src(timelineFromIssue, "rule_override", map[string]any{}),
		src(timelineFromIssue, "assignee_override", map[string]any{}),
		src(timelineFromIssue, "import", map[string]any{}),
		src(timelineFromIssue, "assign", map[string]any{"to": "ed", "auto": true}),
		src(timelineFromIssue, "status", map[string]any{"from": "Todo", "to": "Done"}),
		src(timelineFromIssue, "assign", map[string]any{"to": "ed2"}),
		src(timelineFromIssue, "assignee_takeover", map[string]any{}),
		src(timelineFromIssue, "comment", map[string]any{"kind": "message"}),
		src(timelineFromLease, store.LeaseActionExtend, map[string]any{"name": "x"}),
		src(timelineFromLease, store.LeaseActionAcquire, map[string]any{"name": "x"}),
		{Origin: timelineFromIssue, Kind: "comment", ProjectID: 1, IssueID: 5, UserID: 0, At: at}, // 行った者が分からない
		{Origin: timelineFromPost, ProjectID: 1, UserID: 7},                                       // 投稿は別の表
	}
	evs := timelineEventsOf(srcs)
	want := []string{store.TimelineEventIssueCreate, store.TimelineEventIssueStatus, store.TimelineEventIssueAssign, store.TimelineEventIssueComment,
		store.TimelineEventLeaseAcquire}
	if !slices.Equal(timelineKinds(evs), want) {
		t.Fatalf("載った出来事: %v\nwant %v", timelineKinds(evs), want)
	}
	if evs[1].Detail["comment"] != nil {
		t.Errorf("コメントを添えない状態の変更に comment が付いた: %v", evs[1].Detail)
	}
	if evs[2].Detail["takeover"] != true || evs[3].Detail["message"] != true {
		t.Errorf("引き取りと連絡のコメントの印: %v %v", evs[2].Detail, evs[3].Detail)
	}
}

// 元の記録の取引が失敗したら出来事も残らない。出来事の書き込みが失敗したら元の記録も残らない。
// 対照として、どちらも成功する取引では両方が残る。
func TestTimelineEventsShareTheTransaction(t *testing.T) {
	e := newAttachEnv(t)
	ed := inSession(e.editor, "sess-a")
	countBoth := func() (int, int) {
		return e.count("SELECT COUNT(*) FROM issue_events WHERE kind = 'comment'"), e.count("SELECT COUNT(*) FROM timeline_events WHERE kind = 'issue_comment'")
	}
	record := func(tx *sql.Tx) error {
		return e.s.recordIssueEvent(e.ctx, tx, store.Event{ProjectID: e.demo.ID, IssueID: e.issue.Row.ID, Kind: "comment", SessionID: ed.SessionID,
			Author: e.s.author(ed, e.s.Now()), Detail: map[string]any{}})
	}
	ie0, te0 := countBoth()
	// 元の取引が失敗する
	boom := errors.New("元の取引の失敗")
	if err := e.s.inTx(e.ctx, func(tx *sql.Tx) error {
		if err := record(tx); err != nil {
			return err
		}
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("元の取引: %v", err)
	}
	if ie, te := countBoth(); ie != ie0 || te != te0 {
		t.Errorf("元の取引の失敗の後: issue_events %d→%d・timeline_events %d→%d", ie0, ie, te0, te)
	}
	// 出来事の書き込みが失敗する（無い利用者を行った者にした出来事を同じ取引に足す。外部キーで挿入が拒まれる）
	if err := e.s.inTx(e.ctx, func(tx *sql.Tx) error {
		if err := record(tx); err != nil {
			return err
		}
		return queueTimeline(tx, timelineSource{Origin: timelineFromCommit, ProjectID: e.demo.ID, UserID: 9999999, At: e.s.Now(),
			Detail: map[string]any{"branch": "x", "sha": "abc1234"}})
	}); err == nil {
		t.Fatal("出来事の書き込みが失敗しませんでした（前提が崩れています）")
	}
	if ie, te := countBoth(); ie != ie0 || te != te0 {
		t.Errorf("出来事の失敗の後: issue_events %d→%d・timeline_events %d→%d", ie0, ie, te0, te)
	}
	// 対照: 両方が成功する
	if err := e.s.inTx(e.ctx, record); err != nil {
		t.Fatal(err)
	}
	if ie, te := countBoth(); ie != ie0+1 || te != te0+1 {
		t.Errorf("対照: issue_events %d→%d・timeline_events %d→%d", ie0, ie, te0, te)
	}
	// inTx の外の取引では誤りにする（黙って抜けない）
	tx, err := e.app.BeginTx(e.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck
	if err := queueTimeline(tx, timelineSource{Origin: timelineFromCommit}); err == nil {
		t.Error("inTx の外の取引で出来事を溜められました")
	}
}

// 終了の後に遅れて届いた在席の送信（開始の印なし）では開始が書かれない。対照として開始の印つきの送信では書かれる。
// 開始の印を送らない古いクライアントの在席も、これまでどおり書ける。
func TestTimelinePresenceStartNeedsMark(t *testing.T) {
	e := newAttachEnv(t)
	ed := inSession(e.editor, "sess-a")
	in := PresenceInput{Project: e.demo.Slug, Agent: "codex", MachineLabel: "box-a", Branch: "main"}
	base := e.headSeq()
	if err := e.s.EndPresence(e.ctx, ed, e.demo.Slug); err != nil {
		t.Fatal(err)
	}
	if err := e.s.ReportPresence(e.ctx, ed, in); err != nil { // 終了の後に遅れて届いた送信
		t.Fatal(err)
	}
	if got := timelineKinds(e.timelineEvents(base)); !slices.Equal(got, []string{store.TimelineEventSessionEnd}) {
		t.Fatalf("遅れて届いた送信の後: %v", got)
	}
	if n := e.count("SELECT COUNT(*) FROM session_presence WHERE session_id = 'sess-a'"); n != 1 {
		t.Errorf("印の無い送信で在席が書かれていない: %d", n)
	}
	in.Start = true
	if err := e.s.ReportPresence(e.ctx, ed, in); err != nil {
		t.Fatal(err)
	}
	evs := e.timelineEvents(base)
	if got := timelineKinds(evs); !slices.Equal(got, []string{store.TimelineEventSessionEnd, store.TimelineEventSessionStart}) {
		t.Fatalf("対照: 開始の印つきの送信の後: %v", got)
	}
	if ev := evs[1]; ev.ActorMachine != "box-a" || ev.Detail["agent"] != "codex" || ev.Detail["branch"] != "main" || ev.ActorSession != "sess-a" {
		t.Errorf("開始の材料: %+v", ev)
	}
}

// 資格情報の形をした件名のコミットは件名を載せず、ブランチと SHA だけが載る。対照としてふつうの件名は載る。
// 1 回に 20 件を超えた分は件数だけの 1 行になり、件名は 200 文字で切る。書けるのは editor 以上。
func TestTimelineCommitsWithholdSecretSubjects(t *testing.T) {
	e := newAttachEnv(t)
	ed := inSession(e.editor, "sess-a")
	base := e.headSeq()
	secret := "ghp_" + strings.Repeat("A1", 18)
	long := strings.Repeat("長", 250)
	res, err := e.s.RecordTimelineCommits(e.ctx, ed, TimelineCommitsInput{Project: e.demo.Slug, Branch: "feat-x", Commits: []TimelineCommit{
		{SHA: "1111111", Subject: "鍵を入れた " + secret},
		{SHA: "2222222", Subject: "ふつうの件名\n本文の 2 行目"},
		{SHA: "3333333", Subject: long},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Recorded != 3 || res.Withheld != 1 {
		t.Errorf("結果: %+v", res)
	}
	evs := e.timelineEvents(base)
	if len(evs) != 3 {
		t.Fatalf("出来事 %d 件", len(evs))
	}
	if d := evs[0].Detail; d["subject"] != nil || d["sha"] != "1111111" || d["branch"] != "feat-x" || d["subject_withheld"] != true {
		t.Errorf("資格情報の件名: %v", d)
	}
	var raw string
	if err := e.app.QueryRowContext(e.ctx, "SELECT detail FROM timeline_events WHERE seq = ? AND project_id = ?", evs[0].Seq, e.demo.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "ghp_") {
		t.Errorf("行に資格情報が残った: %s", raw)
	}
	if d := evs[1].Detail; d["subject"] != "ふつうの件名" {
		t.Errorf("対照: ふつうの件名: %v", d)
	}
	if s, _ := evs[2].Detail["subject"].(string); len([]rune(s)) != 200 {
		t.Errorf("長い件名の文字数: %d", len([]rune(s)))
	}
	// 20 件を超えた分は件数だけの 1 行
	var many []TimelineCommit
	for i := 0; i < 23; i++ {
		many = append(many, TimelineCommit{SHA: fmt.Sprintf("%07x", 0xa00000+i), Subject: "c"})
	}
	base = e.headSeq()
	res, err = e.s.RecordTimelineCommits(e.ctx, ed, TimelineCommitsInput{Project: e.demo.Slug, Branch: "feat-x", Commits: many, Omitted: 4})
	if err != nil {
		t.Fatal(err)
	}
	evs = e.timelineEvents(base)
	if res.Recorded != 20 || res.Omitted != 7 || len(evs) != 21 || evs[20].Detail["omitted"] != float64(7) {
		t.Errorf("上限を超えたとき: %+v %d 件 %v", res, len(evs), evs[len(evs)-1].Detail)
	}
	// viewer は書けない
	if _, err := e.s.RecordTimelineCommits(e.ctx, inSession(e.viewer, "sess-v"), TimelineCommitsInput{Project: e.demo.Slug, Branch: "x",
		Commits: []TimelineCommit{{SHA: "abcdef0"}}}); memErrCode(err) != "forbidden" {
		t.Errorf("viewer: %v", err)
	}
	if _, err := e.s.RecordTimelineCommits(e.ctx, ed, TimelineCommitsInput{Project: e.demo.Slug, Branch: "x",
		Commits: []TimelineCommit{{SHA: "not-a-sha"}}}); memErrCode(err) != "invalid_argument" {
		t.Errorf("SHA の形: %v", err)
	}
}

// 近況（update）: 全員宛てだけ・500 文字まで・期限と解除の条件なし・イシューにコメントを残さない・改訂も取り下げも了解もできない。
// 受け取りの記録で届ける一覧（一覧・未達・軽い確認）には入らない。対照として知らせ（info）は入る。
func TestTimelineUpdatePosts(t *testing.T) {
	e := newAttachEnv(t)
	ed, vi := inSession(e.editor, "sess-ed"), inSession(e.viewer, "sess-vi")
	comments := e.count("SELECT COUNT(*) FROM issue_events WHERE kind = 'comment'")
	v := e.mustPost(ed, TimelinePostInput{Kind: store.TimelinePostUpdate, Body: "DEMO-0001 を実装中。次はテスト", Issue: e.issue.Item.ID})
	if v.Kind != store.TimelinePostUpdate || v.ToKind != store.MessageToProject || v.IssueDisplay != e.issue.Item.ID || !v.ExpiresAt.IsZero() || v.Undelivered() {
		t.Fatalf("近況: %+v", v)
	}
	if n := e.count("SELECT COUNT(*) FROM issue_events WHERE kind = 'comment'"); n != comments {
		t.Errorf("近況でイシューにコメントが残った: %d→%d", comments, n)
	}
	for name, in := range map[string]TimelinePostInput{
		"利用者宛て": {Kind: store.TimelinePostUpdate, To: store.MessageToUser, User: "vi", Body: "x"},
		"期限":    {Kind: store.TimelinePostUpdate, Body: "x", TTLSeconds: 60},
		"長すぎる":  {Kind: store.TimelinePostUpdate, Body: strings.Repeat("あ", 501)},
		"資格情報":  {Kind: store.TimelinePostUpdate, Body: "ghp_" + strings.Repeat("A1", 18)},
	} {
		if _, err := e.post(ed, in); err == nil {
			t.Errorf("%s の近況が通った", name)
		}
	}
	if _, err := e.post(ed, TimelinePostInput{Kind: store.TimelinePostUpdate, Body: strings.Repeat("あ", 500)}); err != nil {
		t.Errorf("対照: 500 文字の近況: %v", err)
	}
	if _, err := e.s.ReviseTimelinePost(e.ctx, ed, TimelinePostReviseInput{Project: e.demo.Slug, ID: v.ID, Body: "y"}); err == nil {
		t.Error("近況を改訂できた")
	}
	if _, err := e.s.WithdrawTimelinePost(e.ctx, ed, TimelinePostWithdrawInput{Project: e.demo.Slug, ID: v.ID}); err == nil {
		t.Error("近況を取り下げられた")
	}
	if _, err := e.s.AckTimelinePost(e.ctx, vi, TimelinePostAckInput{Project: e.demo.Slug, ID: v.ID, Version: 1}); err == nil {
		t.Error("近況を了解できた")
	}
	info := e.mustPost(ed, TimelinePostInput{Kind: store.TimelinePostInfo, To: store.MessageToProject, Body: "知らせ"})
	if got := e.timelinePostList(vi); !slices.Equal(got, []int64{info.ID}) {
		t.Errorf("一覧（近況は入らず、対照の知らせは入る）: %v", got)
	}
	if got := timelinePostIDs(e.pending(vi, 0).Undelivered); !slices.Equal(got, []int64{info.ID}) {
		t.Errorf("未達: %v", got)
	}
	// 投稿の通し番号は出来事と 1 本（近況の番号の後に知らせが続く）
	if info.LastSeq <= v.LastSeq {
		t.Errorf("通し番号: 近況 %d・知らせ %d", v.LastSeq, info.LastSeq)
	}
}

// requireMySQL は MySQL でしか確かめられないテストを、MySQL で回していなければ省く。
func requireMySQL(t *testing.T) {
	t.Helper()
	if testutil.Dialect() != testutil.DialectMySQL {
		t.Skip("行ロックの順序とデッドロックは MySQL でしか確かめられない（LOOPTRACK_TEST_DSN が無い）")
	}
}

// 複数のイシューを順にロックする取引（メンバーを閲覧に下げて担当を付け替える）と、同じイシューへの 1 件の変更を並行させても
// デッドロックで失敗しない（head のロックは取引の最後に 1 度だけ取る）。付け替えの出来事はイシューごとに 1 行ずつ載る。
func TestTimelineHeadLastNoDeadlockMySQL(t *testing.T) {
	requireMySQL(t)
	e := newAttachEnv(t)
	ed2 := e.editor2("sess-b")
	// inTx は 1213 を数回までやり直すので、やり直しで緑になったのか 1 度も衝突しなかったのかを数で見分ける（対照は TestInTxRetryCountsDeadlockMySQL）
	retries := inTxRetries.Load()
	defer func() {
		if n := inTxRetries.Load() - retries; n != 0 {
			t.Errorf("取引のやり直しが %d 回ありました（デッドロックが起きている）", n)
		}
	}()
	for round := 0; round < 5; round++ {
		id, err := store.CreateUser(e.ctx, e.admin, fmt.Sprintf("m%d", round), "m", "x", "member")
		if err != nil {
			t.Fatal(err)
		}
		m := Actor{UserID: id, Via: "cli", Lang: i18n.JA, SessionID: "sess-m"}
		e.joinDemo(m, "editor")
		var issues []*Issue
		for i := 0; i < 6; i++ {
			it, err := e.s.Create(e.ctx, m, e.demo, CreateInput{Title: fmt.Sprintf("r%d-%d", round, i), Status: "In Progress"}, i18n.JA)
			if err != nil {
				t.Fatal(err)
			}
			issues = append(issues, it)
		}
		target, err := store.UserByID(e.ctx, e.admin, id)
		if err != nil {
			t.Fatal(err)
		}
		base := e.headSeq()
		var wg sync.WaitGroup
		errs := make(chan error, 20)
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := e.s.SetMembership(e.ctx, inSession(e.root, "sess-root"), e.demo, target, "viewer", "ed2")
			if err == nil && len(res.Reassigned) != len(issues) {
				err = fmt.Errorf("付け替え %d 件", len(res.Reassigned))
			}
			errs <- err
		}()
		for i := len(issues) - 1; i >= 0; i-- { // 付け替えと逆の順でロックする
			wg.Add(1)
			go func(it *Issue) {
				defer wg.Done()
				_, err := e.s.AddComment(e.ctx, ed2, e.demo, it.Row.ID, CommentInput{Text: "並行のコメント"})
				errs <- err
			}(issues[i])
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("round %d: %v", round, err)
			}
		}
		evs := e.timelineEvents(base)
		assigns, comments := 0, 0
		for i, ev := range evs {
			if ev.Seq != base+int64(i)+1 {
				t.Fatalf("round %d: 通し番号が欠けた: %d 件目 %d", round, i, ev.Seq)
			}
			switch ev.Kind {
			case store.TimelineEventIssueAssign:
				assigns++
			case store.TimelineEventIssueComment:
				comments++
			}
		}
		if assigns != len(issues) || comments != len(issues) {
			t.Fatalf("round %d: 付け替え %d・コメント %d", round, assigns, comments)
		}
	}
}

// 並行する取引で、通し番号の小さいほうが後から読めるようになる順序が起きない。読み手は読んだ最大の番号を位置にし、
// 次はそれより大きい番号だけを読む。最後に全部を読み直して、位置より小さいのに読み飛ばした番号が無いことを確かめる。
func TestTimelineSeqVisibleInOrderMySQL(t *testing.T) {
	requireMySQL(t)
	e := newAttachEnv(t)
	ed := inSession(e.editor, "sess-a")
	ed2 := e.editor2("sess-b")
	var issues []*Issue
	for i := 0; i < 4; i++ {
		it, err := e.s.Create(e.ctx, ed, e.demo, CreateInput{Title: fmt.Sprintf("v%d", i)}, i18n.JA)
		if err != nil {
			t.Fatal(err)
		}
		issues = append(issues, it)
	}
	// head を取る直前に少し待たせ、確定の順が入れ替わる機会を広げる
	timelineBeforeHead = func(int64) { time.Sleep(2 * time.Millisecond) }
	defer func() { timelineBeforeHead = nil }()
	base := e.headSeq()
	const writes = 60
	done := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, writes)
	for w := 0; w < writes; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			a := ed
			if w%2 == 1 {
				a = ed2
			}
			_, err := e.s.AddComment(e.ctx, a, e.demo, issues[w%len(issues)].Row.ID, CommentInput{Text: fmt.Sprintf("c%d", w)})
			errs <- err
		}(w)
	}
	go func() { wg.Wait(); close(done) }()
	seen := map[int64]bool{}
	pos := base
	read := func() {
		for _, ev := range e.timelineEvents(pos) {
			seen[ev.Seq] = true
			pos = max(pos, ev.Seq)
		}
	}
	for running := true; running; {
		select {
		case <-done:
			running = false
		default:
			read()
		}
	}
	read()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	all := e.timelineEvents(base)
	if len(all) != writes {
		t.Fatalf("出来事 %d 件（%d のはず）", len(all), writes)
	}
	for _, ev := range all {
		if !seen[ev.Seq] {
			t.Errorf("通し番号 %d を読み飛ばした（読んだ位置より小さい番号が後から現れた）", ev.Seq)
		}
	}
}

// 対照: 2 つの取引が逆の順に行をロックするとデッドロックになり、inTx のやり直しの数が 1 以上になる。
// これが 0 のままなら、TestTimelineHeadLastNoDeadlockMySQL の「やり直し 0 回」は何も確かめていない。
func TestInTxRetryCountsDeadlockMySQL(t *testing.T) {
	requireMySQL(t)
	e := newAttachEnv(t)
	ed := inSession(e.editor, "sess-a")
	var ids []int64
	for i := 0; i < 2; i++ {
		it, err := e.s.Create(e.ctx, ed, e.demo, CreateInput{Title: fmt.Sprintf("d%d", i)}, i18n.JA)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, it.Row.ID)
	}
	before := inTxRetries.Load()
	locked := []chan struct{}{make(chan struct{}), make(chan struct{})}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			first, second := ids[w], ids[1-w]
			attempt := 0
			errs <- e.s.inTx(e.ctx, func(tx *sql.Tx) error {
				attempt++
				var id int64
				if err := tx.QueryRowContext(e.ctx, "SELECT id FROM issues WHERE id = ? FOR UPDATE", first).Scan(&id); err != nil {
					return err
				}
				if attempt == 1 { // 最初の 1 回だけ、相手が 1 つ目の行を握るまで待ってから 2 つ目を取りに行く
					close(locked[w])
					select {
					case <-locked[1-w]:
					case <-time.After(5 * time.Second):
						return errors.New("相手の取引が 1 つ目の行を握りませんでした")
					}
				}
				return tx.QueryRowContext(e.ctx, "SELECT id FROM issues WHERE id = ? FOR UPDATE", second).Scan(&id)
			})
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("前提が崩れています: やり直しの後も失敗しました: %v", err)
		}
	}
	if n := inTxRetries.Load() - before; n < 1 {
		t.Fatalf("前提が崩れています: 逆の順のロックでやり直しが数えられていません（%d 回）", n)
	}
}

// inTx は fn が panic しても取引ごとの収集器の登録を残さない。対照として、fn の中では登録されている。
func TestInTxPanicLeavesNoBatch(t *testing.T) {
	e := newAttachEnv(t)
	count := func() int {
		n := 0
		timelineBatches.Range(func(any, any) bool { n++; return true })
		return n
	}
	before := count()
	inside := -1
	func() {
		defer func() { _ = recover() }()
		_ = e.s.inTx(e.ctx, func(tx *sql.Tx) error {
			inside = count()
			panic("テストの panic")
		})
	}()
	if inside != before+1 {
		t.Fatalf("前提が崩れています: fn の中の登録 %d（%d のはず）", inside, before+1)
	}
	if n := count(); n != before {
		t.Errorf("panic の後に収集器の登録が残った: %d（%d のはず）", n, before)
	}
}

// コミットの経路はセッションの無い送信を拒む（在席とそろえる）。対照としてセッションのある送信は通る。
func TestTimelineCommitsNeedSession(t *testing.T) {
	e := newAttachEnv(t)
	in := TimelineCommitsInput{Project: e.demo.Slug, Branch: "main", Commits: []TimelineCommit{{SHA: "abc1234", Subject: "直す"}}}
	if _, err := e.s.RecordTimelineCommits(e.ctx, e.editor, in); memErrCode(err) != "invalid_argument" {
		t.Errorf("セッションの無いコミット: %v", err)
	}
	if _, err := e.s.RecordTimelineCommits(e.ctx, inSession(e.editor, "sess-a"), in); err != nil {
		t.Errorf("対照: セッションのあるコミット: %v", err)
	}
}

// 同じ投稿を 2 つのセッションが同時に改訂しても、一意の索引の違反（500）にならない。head を待つ間に確定した版を
// ロックつきの読みで読み直すので、両方が通って版が 1 つずつ進む（MySQL 版）。
func TestTimelinePostConcurrentReviseMySQL(t *testing.T) {
	requireMySQL(t)
	e := newAttachEnv(t)
	ed := inSession(e.editor, "sess-a")
	ed2 := inSession(e.editor, "sess-b")
	v := e.mustPost(ed, TimelinePostInput{Kind: store.TimelinePostHold, To: store.MessageToProject, Body: "待ってください"})
	timelineBeforeHead = func(int64) { time.Sleep(5 * time.Millisecond) }
	defer func() { timelineBeforeHead = nil }()
	const rounds = 5
	for round := 0; round < rounds; round++ {
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for _, a := range []Actor{ed, ed2} {
			wg.Add(1)
			go func(a Actor) {
				defer wg.Done()
				_, err := e.s.ReviseTimelinePost(e.ctx, a, TimelinePostReviseInput{Project: e.demo.Slug, ID: v.ID, Body: fmt.Sprintf("r%d", round)})
				errs <- err
			}(a)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("round %d: 並行の改訂が失敗しました: %v", round, err)
			}
		}
	}
	cur, err := e.s.GetTimelinePost(e.ctx, ed, e.demo.Slug, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Version != 1+2*rounds || len(cur.Events) != 1+2*rounds {
		t.Errorf("版 %d・出来事 %d（%d のはず）", cur.Version, len(cur.Events), 1+2*rounds)
	}
}

// 同じ SHA のコミットを 2 本の取引が並んで送っても、出来事は 1 件だけ載る（MySQL だけ）。
// 後の取引は先に読みの時点を決め（checkProjectActive などの非ロックの読み）、通し番号の行の手前で待つ。その間に先の取引が
// 書いて確定する。後の取引が握った後の既存の確かめがロックの無い読みだと、写しに先の行が見えず二重に載る。
func TestTimelineCommitDedupConcurrentMySQL(t *testing.T) {
	requireMySQL(t)
	e := newAttachEnv(t)
	ed := inSession(e.editor, "sess-a")
	ed2 := e.editor2("sess-b")
	base := e.headSeq()
	var calls atomic.Int32
	hold, held := make(chan struct{}), make(chan struct{})
	timelineBeforeHead = func(int64) {
		if calls.Add(1) == 1 { // 最初に来た取引（後で確定させる側）を止める
			close(held)
			<-hold
		}
	}
	defer func() { timelineBeforeHead = nil }()
	in := TimelineCommitsInput{Project: e.demo.Slug, Branch: "feat-x", Commits: []TimelineCommit{{SHA: "def5678", Subject: "並んだコミット"}}}
	errs := make(chan error, 1)
	go func() {
		_, err := e.s.RecordTimelineCommits(e.ctx, ed2, in)
		errs <- err
	}()
	<-held
	if _, err := e.s.RecordTimelineCommits(e.ctx, ed, in); err != nil {
		t.Fatal(err)
	}
	close(hold)
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ev := range e.timelineEvents(base) {
		if ev.Kind == store.TimelineEventCommit {
			n++
		}
	}
	if n != 1 {
		t.Errorf("同じ SHA のコミットが %d 件載った（want 1）", n)
	}
	if calls.Load() != 2 {
		t.Errorf("前提が崩れています: 通し番号の手前に来た取引が %d 本", calls.Load())
	}
}

// 同じ SHA のコミットを 2 回送っても 1 件だけ載る。件数だけの行は送り直すたびに載る（限界。DESIGN に書いた）。
func TestTimelineCommitDedupSequential(t *testing.T) {
	e := newAttachEnv(t)
	ed := inSession(e.editor, "sess-a")
	base := e.headSeq()
	in := TimelineCommitsInput{Project: e.demo.Slug, Branch: "feat-x", Commits: []TimelineCommit{{SHA: "aaa1111", Subject: "1"}, {SHA: "bbb2222", Subject: "2"}}}
	for i := 0; i < 2; i++ {
		if _, err := e.s.RecordTimelineCommits(e.ctx, ed, in); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	for _, ev := range e.timelineEvents(base) {
		if ev.Kind == store.TimelineEventCommit {
			n++
		}
	}
	if n != 2 {
		t.Errorf("コミットの出来事 %d 件（want 2）", n)
	}
}
