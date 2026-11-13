package service

import (
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/testutil"
)

// タイムラインの service（timeline.go）のテスト。土台は連絡のテストと同じ attachEnv（アプリ用の DB 利用者で動かす）。
// demo の参加者は editor（ed）と viewer（vi）。root は利用者の admin で demo に参加していない。out は other だけの参加者
// （テストによっては demo にも加える）。

func (e *attachEnv) post(a Actor, in TimelinePostInput) (*TimelinePostView, error) {
	in.Project = e.demo.Slug
	return e.s.PostTimelinePost(e.ctx, a, in)
}

func (e *attachEnv) mustPost(a Actor, in TimelinePostInput) *TimelinePostView {
	e.t.Helper()
	v, err := e.post(a, in)
	if err != nil {
		e.t.Fatalf("投稿 %+v: %v", in, err)
	}
	return v
}

func (e *attachEnv) timelinePostList(a Actor) []int64 {
	e.t.Helper()
	res, err := e.s.ListTimelinePosts(e.ctx, a, e.demo.Slug, TimelinePostListQuery{})
	if err != nil {
		e.t.Fatalf("一覧: %v", err)
	}
	ids := []int64{}
	for _, v := range res.TimelinePosts {
		ids = append(ids, v.ID)
	}
	return ids
}

func (e *attachEnv) pending(a Actor, limit int) *TimelinePostPending {
	e.t.Helper()
	res, err := e.s.PendingTimelinePosts(e.ctx, a, e.demo.Slug, limit)
	if err != nil {
		e.t.Fatalf("未達と未了解: %v", err)
	}
	return res
}

func timelinePostIDs(vs []TimelinePostView) []int64 {
	ids := []int64{}
	for _, v := range vs {
		ids = append(ids, v.ID)
	}
	return ids
}

// headSeq はプロジェクト demo の最後の通し番号。
func (e *attachEnv) headSeq() int64 {
	e.t.Helper()
	seq, err := store.TimelinePostHeadSeq(e.ctx, e.s.DB, e.demo.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return seq
}

func (e *attachEnv) joinDemo(a Actor, role string) {
	e.t.Helper()
	if err := store.SetMember(e.ctx, e.admin, e.demo.ID, a.UserID, role); err != nil {
		e.t.Fatal(err)
	}
}

// 投稿・改訂・取り下げ・了解の正常系。改訂と取り下げの後も前の版の本文と書き手が読める。
func TestTimelinePostLifecycle(t *testing.T) {
	e := newAttachEnv(t)
	ed, vi := inSession(e.editor, "sess-ed"), inSession(e.viewer, "sess-vi")
	base := e.headSeq() // 通し番号は自動の出来事（環境を作る起票など）と 1 本なので、投稿の前の値から数える
	v := e.mustPost(ed, TimelinePostInput{Kind: store.TimelinePostHold, To: store.MessageToProject, Body: "本番の反映を待ってください"})
	if v.Version != 1 || v.State != TimelinePostActive || v.Author != "ed" || v.LastSeq != base+1 {
		t.Fatalf("投稿: %+v", v)
	}
	if got := e.timelinePostList(vi); !slices.Equal(got, []int64{v.ID}) {
		t.Fatalf("受け取る側の一覧: %v", got)
	}
	r, err := e.s.ReviseTimelinePost(e.ctx, ed, TimelinePostReviseInput{Project: e.demo.Slug, ID: v.ID, Body: "反映は 15 時まで待ってください"})
	if err != nil {
		t.Fatalf("改訂: %v", err)
	}
	if r.Version != 2 || r.Body != "反映は 15 時まで待ってください" || !r.ExpiresAt.Equal(v.ExpiresAt) || r.LastSeq != base+2 {
		t.Fatalf("改訂の結果: %+v", r)
	}
	if _, err := e.s.AckTimelinePost(e.ctx, vi, TimelinePostAckInput{Project: e.demo.Slug, ID: v.ID, Version: 1}); memErrCode(err) != "timeline_version" {
		t.Errorf("古い版の了解: %v", err)
	}
	ack, err := e.s.AckTimelinePost(e.ctx, vi, TimelinePostAckInput{Project: e.demo.Slug, ID: v.ID, Version: 2})
	if err != nil || ack.Acked != 2 || ack.Received != 2 {
		t.Fatalf("了解: %+v %v", ack, err)
	}
	w, err := e.s.WithdrawTimelinePost(e.ctx, ed, TimelinePostWithdrawInput{Project: e.demo.Slug, ID: v.ID})
	if err != nil || w.State != TimelinePostWithdrawn || w.LastVersion != 3 || w.WithdrawnBy != "ed" {
		t.Fatalf("取り下げ: %+v %v", w, err)
	}
	if got := e.timelinePostList(vi); len(got) != 0 {
		t.Errorf("取り下げの後の一覧: %v", got)
	}
	d, err := e.s.GetTimelinePost(e.ctx, vi, e.demo.Slug, v.ID)
	if err != nil {
		t.Fatalf("取得: %v", err)
	}
	if len(d.Events) != 3 || d.Events[0].Body != "本番の反映を待ってください" || d.Events[0].ActorLogin != "ed" ||
		d.Events[1].Body != "反映は 15 時まで待ってください" || d.Events[2].Action != store.TimelinePostActionWithdraw {
		t.Fatalf("出来事: %+v", d.Events)
	}
	if d.Body != "反映は 15 時まで待ってください" || d.Version != 2 {
		t.Errorf("取り下げ後の最新の版: %+v", d.TimelinePostView)
	}
	if _, err := e.s.ReviseTimelinePost(e.ctx, ed, TimelinePostReviseInput{Project: e.demo.Slug, ID: v.ID, Body: "x"}); memErrCode(err) != "timeline_withdrawn" {
		t.Errorf("取り下げ済みの改訂: %v", err)
	}
	if _, err := e.s.WithdrawTimelinePost(e.ctx, ed, TimelinePostWithdrawInput{Project: e.demo.Slug, ID: v.ID}); memErrCode(err) != "timeline_withdrawn" {
		t.Errorf("2 度目の取り下げ: %v", err)
	}
	if _, err := e.s.AckTimelinePost(e.ctx, vi, TimelinePostAckInput{Project: e.demo.Slug, ID: v.ID, Version: 2}); memErrCode(err) != "timeline_inactive" {
		t.Errorf("取り下げ済みの了解: %v", err)
	}
}

// 権限で拒まれる場合と、同じ操作が通る側の対照。
func TestTimelinePostPermissions(t *testing.T) {
	e := newAttachEnv(t)
	e.joinDemo(e.outside, "editor") // 書いた本人ではない editor
	ed := inSession(e.editor, "sess-ed")
	if _, err := e.post(e.viewer, TimelinePostInput{Kind: store.TimelinePostInfo, To: store.MessageToProject, Body: "viewer から"}); memErrCode(err) != "forbidden" {
		t.Errorf("viewer の投稿: %v", err)
	}
	if _, err := e.post(e.root, TimelinePostInput{Kind: store.TimelinePostInfo, To: store.MessageToProject, Body: "参加していない管理者から"}); memErrCode(err) != "forbidden" {
		t.Errorf("参加していない管理者の投稿: %v", err)
	}
	if n := e.count("SELECT COUNT(*) FROM timeline_posts"); n != 0 {
		t.Errorf("拒んだ投稿が %d 件残った", n)
	}
	v := e.mustPost(ed, TimelinePostInput{Kind: store.TimelinePostRequest, To: store.MessageToProject, Body: "検査を回してください"}) // 対照
	revise := func(a Actor) error {
		_, err := e.s.ReviseTimelinePost(e.ctx, a, TimelinePostReviseInput{Project: e.demo.Slug, ID: v.ID, Body: "直した本文"})
		return err
	}
	if err := revise(e.outside); memErrCode(err) != "timeline_not_author" {
		t.Errorf("本人以外の改訂: %v", err)
	}
	if err := revise(e.viewer); memErrCode(err) != "timeline_not_author" {
		t.Errorf("viewer の改訂: %v", err)
	}
	if err := revise(inSession(e.editor, "sess-other")); err != nil {
		t.Errorf("対照: 本人の別のセッションの改訂: %v", err)
	}
	// 了解: 知らせは了解できない。出したセッション自身は宛先に当たらない
	info := e.mustPost(ed, TimelinePostInput{Kind: store.TimelinePostInfo, To: store.MessageToProject, Body: "知らせ"})
	if _, err := e.s.AckTimelinePost(e.ctx, e.viewer, TimelinePostAckInput{Project: e.demo.Slug, ID: info.ID, Version: 1}); memErrCode(err) != "invalid_argument" {
		t.Errorf("知らせの了解: %v", err)
	}
	if _, err := e.s.AckTimelinePost(e.ctx, ed, TimelinePostAckInput{Project: e.demo.Slug, ID: v.ID, Version: 2}); memErrCode(err) != "timeline_not_recipient" {
		t.Errorf("出したセッションの了解: %v", err)
	}
	if _, err := e.s.AckTimelinePost(e.ctx, e.viewer, TimelinePostAckInput{Project: e.demo.Slug, ID: v.ID, Version: 2}); err != nil {
		t.Errorf("対照: viewer の了解（役割を問わない）: %v", err)
	}
	// 取り下げ: viewer は本人でなければ理由があっても拒む。ほかの editor は理由が要る
	withdraw := func(a Actor, reason string) (*TimelinePostView, error) {
		return e.s.WithdrawTimelinePost(e.ctx, a, TimelinePostWithdrawInput{Project: e.demo.Slug, ID: v.ID, Reason: reason})
	}
	if _, err := withdraw(e.viewer, "止まっているので外します"); memErrCode(err) != "forbidden" {
		t.Errorf("viewer の取り下げ: %v", err)
	}
	if _, err := withdraw(e.outside, ""); memErrCode(err) != "invalid_argument" {
		t.Errorf("理由の無い他人の取り下げ: %v", err)
	}
	w, err := withdraw(e.outside, "依頼した作業は終わっています")
	if err != nil || w.State != TimelinePostWithdrawn || w.WithdrawReason != "依頼した作業は終わっています" || w.WithdrawnBy != "out" {
		t.Fatalf("対照: 理由つきの他人の取り下げ: %+v %v", w, err)
	}
	// 書いた本人は理由なしで取り下げられる
	if _, err := e.s.WithdrawTimelinePost(e.ctx, ed, TimelinePostWithdrawInput{Project: e.demo.Slug, ID: info.ID}); err != nil {
		t.Errorf("対照: 本人の理由なしの取り下げ: %v", err)
	}
}

// 参加していないプロジェクトのタイムラインは無いものと同じ 404 になる。
func TestTimelinePostNonMember(t *testing.T) {
	e := newAttachEnv(t)
	v := e.mustPost(e.editor, TimelinePostInput{Kind: store.TimelinePostInfo, To: store.MessageToProject, Body: "知らせ"})
	if _, err := e.s.GetTimelinePost(e.ctx, e.outside, e.demo.Slug, v.ID); memErrCode(err) != "not_found" {
		t.Errorf("参加者でない取得: %v", err)
	}
	if _, err := e.s.ListTimelinePosts(e.ctx, e.outside, e.demo.Slug, TimelinePostListQuery{}); memErrCode(err) != "not_found" {
		t.Errorf("参加者でない一覧: %v", err)
	}
	if _, err := e.post(e.outside, TimelinePostInput{Kind: store.TimelinePostInfo, To: store.MessageToProject, Body: "外から"}); memErrCode(err) != "not_found" {
		t.Errorf("参加者でない投稿: %v", err)
	}
	if _, err := e.s.GetTimelinePost(e.ctx, e.viewer, e.demo.Slug, v.ID); err != nil {
		t.Errorf("対照: 参加者の取得: %v", err)
	}
}

// 待機の期限: 省くと 2 時間。7 日を超える期限は拒む。期限を過ぎた待機は有効な一覧に出ず、改訂で延ばせる。
func TestTimelinePostHoldExpiry(t *testing.T) {
	e := newAttachEnv(t)
	base := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	now := base
	e.s.Now = func() time.Time { return now }
	ed, vi := inSession(e.editor, "sess-ed"), inSession(e.viewer, "sess-vi")
	if _, err := e.post(ed, TimelinePostInput{Kind: store.TimelinePostHold, To: store.MessageToProject, Body: "待機", TTLSeconds: 7*24*3600 + 1}); memErrCode(err) != "invalid_argument" {
		t.Errorf("7 日を超える期限: %v", err)
	}
	if _, err := e.post(ed, TimelinePostInput{Kind: store.TimelinePostHold, To: store.MessageToProject, Body: "待機", TTLSeconds: -1}); memErrCode(err) != "invalid_argument" {
		t.Errorf("負の期限: %v", err)
	}
	long := e.mustPost(ed, TimelinePostInput{Kind: store.TimelinePostHold, To: store.MessageToProject, Body: "長い待機", TTLSeconds: 7 * 24 * 3600}) // 対照: 上限ちょうど
	if !long.ExpiresAt.Equal(base.Add(7 * 24 * time.Hour)) {
		t.Errorf("上限ちょうどの期限: %v", long.ExpiresAt)
	}
	v := e.mustPost(ed, TimelinePostInput{Kind: store.TimelinePostHold, To: store.MessageToProject, Body: "既定の期限の待機"})
	if !v.ExpiresAt.Equal(base.Add(2 * time.Hour)) {
		t.Fatalf("既定の期限: %v（%v のはず）", v.ExpiresAt, base.Add(2*time.Hour))
	}
	info := e.mustPost(ed, TimelinePostInput{Kind: store.TimelinePostInfo, To: store.MessageToProject, Body: "期限の無い知らせ"})
	if !info.ExpiresAt.IsZero() {
		t.Errorf("知らせの期限: %v", info.ExpiresAt)
	}
	now = base.Add(2*time.Hour - time.Second)
	if got := e.timelinePostList(vi); !slices.Contains(got, v.ID) {
		t.Fatalf("対照: 期限の 1 秒前の一覧に無い: %v", got)
	}
	now = base.Add(2 * time.Hour)
	if got := e.timelinePostList(vi); slices.Contains(got, v.ID) || !slices.Contains(got, info.ID) || !slices.Contains(got, long.ID) {
		t.Errorf("期限を過ぎた一覧: %v", got)
	}
	if d, err := e.s.GetTimelinePost(e.ctx, vi, e.demo.Slug, v.ID); err != nil || d.State != TimelinePostExpired {
		t.Errorf("期限切れの状態: %+v %v", d, err)
	}
	if c, err := e.s.CheckTimelinePosts(e.ctx, vi, e.demo.Slug); err != nil || !slices.Equal(c.Holds, []int64{long.ID}) {
		t.Errorf("軽い確認の待機: %+v %v", c, err)
	}
	// 改訂で延ばす
	r, err := e.s.ReviseTimelinePost(e.ctx, ed, TimelinePostReviseInput{Project: e.demo.Slug, ID: v.ID, TTLSeconds: 3600})
	if err != nil || r.State != TimelinePostActive || !r.ExpiresAt.Equal(now.Add(time.Hour)) || r.Body != "既定の期限の待機" {
		t.Fatalf("延ばす改訂: %+v %v", r, err)
	}
	if got := e.timelinePostList(vi); !slices.Contains(got, v.ID) {
		t.Errorf("延ばした後の一覧: %v", got)
	}
	if _, err := e.s.ReviseTimelinePost(e.ctx, ed, TimelinePostReviseInput{Project: e.demo.Slug, ID: v.ID, TTLSeconds: 7*24*3600 + 1}); memErrCode(err) != "invalid_argument" {
		t.Errorf("改訂で 7 日を超える期限: %v", err)
	}
	if _, err := e.s.ReviseTimelinePost(e.ctx, ed, TimelinePostReviseInput{Project: e.demo.Slug, ID: v.ID}); memErrCode(err) != "invalid_argument" {
		t.Errorf("何も変えない改訂: %v", err)
	}
}

// 解除の条件: イシューが Done になった待機と、占有が返った待機は有効な一覧から外れる。
func TestTimelinePostReleaseConditions(t *testing.T) {
	e := newAttachEnv(t)
	ed, vi := inSession(e.editor, "sess-ed"), inSession(e.viewer, "sess-vi")
	if _, err := e.post(ed, TimelinePostInput{Kind: store.TimelinePostRequest, To: store.MessageToProject, Body: "依頼", ReleaseIssue: e.issue.Item.ID}); memErrCode(err) != "invalid_argument" {
		t.Errorf("待機でない書き込みの解除の条件: %v", err)
	}
	if _, err := e.post(ed, TimelinePostInput{Kind: store.TimelinePostHold, To: store.MessageToProject, Body: "待機", ReleaseLease: "deploy"}); memErrCode(err) != "timeline_lease_not_held" {
		t.Errorf("取られていない占有: %v", err)
	}
	byIssue := e.mustPost(ed, TimelinePostInput{Kind: store.TimelinePostHold, To: store.MessageToProject, Body: "イシューが終わるまで待機", ReleaseIssue: e.issue.Item.ID})
	acq, err := e.s.AcquireLease(e.ctx, ed, LeaseInput{Project: e.demo.Slug, Name: "deploy", Purpose: "本番へ反映する"})
	if err != nil {
		t.Fatal(err)
	}
	byLease := e.mustPost(ed, TimelinePostInput{Kind: store.TimelinePostHold, To: store.MessageToProject, Body: "反映が終わるまで待機", ReleaseLease: "deploy"})
	plain := e.mustPost(ed, TimelinePostInput{Kind: store.TimelinePostHold, To: store.MessageToProject, Body: "条件の無い待機"})
	if got := e.timelinePostList(vi); !slices.Equal(got, []int64{byIssue.ID, byLease.ID, plain.ID}) {
		t.Fatalf("対照: 条件を満たす前の一覧: %v", got)
	}
	if _, err := e.s.SetStatus(e.ctx, e.editor, e.demo, e.issue.Row.ID, "Done", "終えました", ""); err != nil {
		t.Fatalf("Done: %v", err)
	}
	if got := e.timelinePostList(vi); !slices.Equal(got, []int64{byLease.ID, plain.ID}) {
		t.Errorf("イシューが Done になった後の一覧: %v", got)
	}
	if d, err := e.s.GetTimelinePost(e.ctx, vi, e.demo.Slug, byIssue.ID); err != nil || d.State != TimelinePostReleased {
		t.Errorf("イシューで解けた状態: %+v %v", d, err)
	}
	if err := e.s.ReleaseLease(e.ctx, ed, LeaseInput{Project: e.demo.Slug, Name: "deploy", Token: acq.Token}); err != nil {
		t.Fatal(err)
	}
	if got := e.timelinePostList(vi); !slices.Equal(got, []int64{plain.ID}) {
		t.Errorf("占有が返った後の一覧: %v", got)
	}
	// 取り直されても、出した時点の占有ではないので解けたまま
	if _, err := e.s.AcquireLease(e.ctx, e.editor, LeaseInput{Project: e.demo.Slug, Name: "deploy", Purpose: "別の反映"}); err != nil {
		t.Fatal(err)
	}
	if got := e.timelinePostList(vi); !slices.Equal(got, []int64{plain.ID}) {
		t.Errorf("占有が取り直された後の一覧: %v", got)
	}
	// 既にクローズしたイシューは条件にできない
	if _, err := e.post(ed, TimelinePostInput{Kind: store.TimelinePostHold, To: store.MessageToProject, Body: "待機", ReleaseIssue: e.issue.Item.ID}); memErrCode(err) != "timeline_issue_closed" {
		t.Errorf("クローズ済みのイシュー: %v", err)
	}
}

// 未達と未了解。受け取りを記録した版は出ない。改訂で戻る。取り下げは前の版を受け取っていたセッションに 1 度だけ出る。
// 受け取りを記録しない書き込みは、上限で切っても何度取っても残る。
func TestTimelinePostPendingAndReceipts(t *testing.T) {
	e := newAttachEnv(t)
	ed := inSession(e.editor, "sess-ed")
	a, b := inSession(e.viewer, "sess-a"), inSession(e.viewer, "sess-b")
	info := e.mustPost(ed, TimelinePostInput{Kind: store.TimelinePostInfo, To: store.MessageToProject, Body: "知らせ"})
	req := e.mustPost(ed, TimelinePostInput{Kind: store.TimelinePostRequest, To: store.MessageToProject, Body: "依頼"})
	hold := e.mustPost(ed, TimelinePostInput{Kind: store.TimelinePostHold, To: store.MessageToProject, Body: "待機"})
	p := e.pending(a, 0)
	if !slices.Equal(timelinePostIDs(p.Undelivered), []int64{hold.ID, req.ID, info.ID}) || !slices.Equal(timelinePostIDs(p.Unacked), []int64{hold.ID, req.ID}) {
		t.Fatalf("最初の未達 %v・未了解 %v", timelinePostIDs(p.Undelivered), timelinePostIDs(p.Unacked))
	}
	// 出したセッションには出ない
	if p := e.pending(ed, 0); p.UndeliveredTotal != 0 || p.UnackedTotal != 0 {
		t.Errorf("出したセッションの未達: %+v", p)
	}
	// 上限で切っても、受け取りを記録しない限り何度取っても残る
	for i := 0; i < 3; i++ {
		p := e.pending(a, 1)
		if len(p.Undelivered) != 1 || p.Undelivered[0].ID != hold.ID || p.UndeliveredTotal != 3 {
			t.Fatalf("%d 回目の上限つきの未達: %v（全 %d）", i, timelinePostIDs(p.Undelivered), p.UndeliveredTotal)
		}
	}
	if n, err := e.s.RecordTimelinePostReceipts(e.ctx, a, e.demo.Slug, []TimelinePostReceiptInput{{ID: info.ID, Version: 1}}); err != nil || n != 1 {
		t.Fatalf("受け取りの記録: %d %v", n, err)
	}
	if n, err := e.s.RecordTimelinePostReceipts(e.ctx, a, e.demo.Slug, []TimelinePostReceiptInput{{ID: info.ID, Version: 1}, {ID: info.ID, Version: 9}}); err != nil || n != 0 {
		t.Errorf("2 度目と無い版の受け取り: %d %v", n, err)
	}
	if got := timelinePostIDs(e.pending(a, 0).Undelivered); !slices.Equal(got, []int64{hold.ID, req.ID}) {
		t.Errorf("知らせを受け取った後の未達: %v", got)
	}
	if got := timelinePostIDs(e.pending(b, 0).Undelivered); len(got) != 3 {
		t.Errorf("A の受け取りが B に効いた: %v", got)
	}
	if _, err := e.s.RecordTimelinePostReceipts(e.ctx, a, e.demo.Slug, []TimelinePostReceiptInput{{ID: req.ID, Version: 1}, {ID: hold.ID, Version: 1}}); err != nil {
		t.Fatal(err)
	}
	p = e.pending(a, 0)
	if len(p.Undelivered) != 0 || !slices.Equal(timelinePostIDs(p.Unacked), []int64{hold.ID, req.ID}) {
		t.Fatalf("受け取った後: 未達 %v・未了解 %v（届いたことと了解は別）", timelinePostIDs(p.Undelivered), timelinePostIDs(p.Unacked))
	}
	if _, err := e.s.AckTimelinePost(e.ctx, a, TimelinePostAckInput{Project: e.demo.Slug, ID: hold.ID, Version: 1}); err != nil {
		t.Fatal(err)
	}
	if got := timelinePostIDs(e.pending(a, 0).Unacked); !slices.Equal(got, []int64{req.ID}) {
		t.Errorf("了解の後の未了解: %v", got)
	}
	// 改訂すると、了解済みでも未達と未了解に戻る
	if _, err := e.s.ReviseTimelinePost(e.ctx, ed, TimelinePostReviseInput{Project: e.demo.Slug, ID: hold.ID, Body: "待機（範囲を広げた）"}); err != nil {
		t.Fatal(err)
	}
	p = e.pending(a, 0)
	if !slices.Equal(timelinePostIDs(p.Undelivered), []int64{hold.ID}) || !slices.Equal(timelinePostIDs(p.Unacked), []int64{hold.ID, req.ID}) {
		t.Fatalf("改訂の後: 未達 %v・未了解 %v", timelinePostIDs(p.Undelivered), timelinePostIDs(p.Unacked))
	}
	if p.Undelivered[0].Version != 2 || p.Undelivered[0].Acked != 1 {
		t.Errorf("改訂の後の版: %+v", p.Undelivered[0])
	}
	// 取り下げ: 前の版を受け取っていた A には 1 度だけ出る。受け取っていない B には出ない
	if _, err := e.s.WithdrawTimelinePost(e.ctx, ed, TimelinePostWithdrawInput{Project: e.demo.Slug, ID: req.ID}); err != nil {
		t.Fatal(err)
	}
	p = e.pending(a, 0)
	if !slices.Contains(timelinePostIDs(p.Undelivered), req.ID) || slices.Contains(timelinePostIDs(p.Unacked), req.ID) {
		t.Fatalf("取り下げの後の A: 未達 %v・未了解 %v", timelinePostIDs(p.Undelivered), timelinePostIDs(p.Unacked))
	}
	var notice TimelinePostView
	for _, v := range p.Undelivered {
		if v.ID == req.ID {
			notice = v
		}
	}
	if notice.State != TimelinePostWithdrawn || notice.LastVersion != 2 {
		t.Errorf("取り下げの知らせ: %+v", notice)
	}
	if got := timelinePostIDs(e.pending(b, 0).Undelivered); slices.Contains(got, req.ID) {
		t.Errorf("前の版を受け取っていない B に取り下げが出た: %v", got)
	}
	if _, err := e.s.RecordTimelinePostReceipts(e.ctx, a, e.demo.Slug, []TimelinePostReceiptInput{{ID: req.ID, Version: notice.LastVersion}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if got := timelinePostIDs(e.pending(a, 0).Undelivered); slices.Contains(got, req.ID) {
			t.Errorf("%d 回目: 受け取った取り下げがまた出た: %v", i, got)
		}
	}
}

// 通し番号: 並行に投稿しても番号が重ならず欠けず、渡した番号より後の出来事を漏れなく返す。
func TestTimelinePostSeqConcurrent(t *testing.T) {
	e := newAttachEnv(t)
	const n = 20
	base := e.headSeq() // 通し番号は自動の出来事（環境を作る起票など）と 1 本なので、投稿の前の値から数える
	var wg sync.WaitGroup
	seqs := make([]int64, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, err := e.s.PostTimelinePost(e.ctx, inSession(e.editor, "sess-par"), TimelinePostInput{Project: e.demo.Slug, Kind: store.TimelinePostInfo,
				To: store.MessageToProject, Body: "並行の知らせ"})
			errs[i] = err
			if err == nil {
				seqs[i] = v.LastSeq
			}
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("%d 本目の投稿: %v", i, err)
		}
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	for i, s := range seqs {
		if s != base+int64(i+1) {
			t.Fatalf("通し番号が重なるか欠けた: %v", seqs)
		}
	}
	vi := inSession(e.viewer, "sess-vi")
	for _, rel := range []int64{0, 7, n - 1, n} {
		after := base + rel
		res, err := e.s.TimelinePostEventsAfter(e.ctx, vi, e.demo.Slug, after, 0)
		if err != nil {
			t.Fatal(err)
		}
		if res.Seq != base+n || len(res.Events) != int(n-rel) {
			t.Fatalf("after=%d: seq=%d 件数=%d", after, res.Seq, len(res.Events))
		}
		for i, ev := range res.Events {
			if ev.Seq != after+int64(i)+1 {
				t.Fatalf("after=%d の %d 件目の番号: %d", after, i, ev.Seq)
			}
		}
	}
	// 了解と受け取りでは進まない
	if c, err := e.s.CheckTimelinePosts(e.ctx, vi, e.demo.Slug); err != nil || c.Seq != base+n {
		t.Fatalf("軽い確認: %+v %v", c, err)
	}
	if _, err := e.s.RecordTimelinePostReceipts(e.ctx, vi, e.demo.Slug, []TimelinePostReceiptInput{{ID: 1, Version: 1}}); err != nil {
		t.Fatal(err)
	}
	if c, err := e.s.CheckTimelinePosts(e.ctx, vi, e.demo.Slug); err != nil || c.Seq != base+n {
		t.Errorf("受け取りの後の通し番号: %+v %v", c, err)
	}
}

// ほかの利用者だけに宛てた書き込みは、宛先でも送り手でもない利用者に見えない（対照: 宛先と送り手には見える）。到達の状況も同じ。
func TestTimelinePostVisibility(t *testing.T) {
	e := newAttachEnv(t)
	e.joinDemo(e.outside, "viewer") // 宛先でも送り手でもない参加者
	ed := inSession(e.editor, "sess-ed")
	toUser := e.mustPost(ed, TimelinePostInput{Kind: store.TimelinePostHold, To: store.MessageToUser, User: "vi", Body: "vi だけへの待機"})
	toSess := e.mustPost(ed, TimelinePostInput{Kind: store.TimelinePostRequest, To: store.MessageToSession, User: "vi", Session: "sess-v1", Body: "vi のセッションへ"})
	all := e.mustPost(ed, TimelinePostInput{Kind: store.TimelinePostInfo, To: store.MessageToProject, Body: "全員へ"})
	out := inSession(e.outside, "sess-out")
	if got := e.timelinePostList(out); !slices.Equal(got, []int64{all.ID}) {
		t.Errorf("宛先でない参加者の一覧: %v", got)
	}
	if p := e.pending(out, 0); !slices.Equal(timelinePostIDs(p.Undelivered), []int64{all.ID}) {
		t.Errorf("宛先でない参加者の未達: %v", timelinePostIDs(p.Undelivered))
	}
	if ev, err := e.s.TimelinePostEventsAfter(e.ctx, out, e.demo.Slug, 0, 0); err != nil || len(ev.Events) != 1 || ev.Events[0].TimelinePostID != all.ID {
		t.Errorf("宛先でない参加者の出来事: %+v %v", ev, err)
	}
	for _, id := range []int64{toUser.ID, toSess.ID} {
		if _, err := e.s.GetTimelinePost(e.ctx, out, e.demo.Slug, id); memErrCode(err) != "not_found" {
			t.Errorf("宛先でない参加者が %d を取得: %v", id, err)
		}
		if _, err := e.s.TimelinePostDeliveryStatus(e.ctx, out, e.demo.Slug, id); memErrCode(err) != "not_found" {
			t.Errorf("宛先でない参加者が %d の到達の状況: %v", id, err)
		}
		if _, err := e.s.GetTimelinePost(e.ctx, e.root, e.demo.Slug, id); memErrCode(err) != "not_found" {
			t.Errorf("参加していない管理者が %d を取得: %v", id, err)
		}
		// 対照: 宛先と送り手には見える
		if _, err := e.s.GetTimelinePost(e.ctx, e.viewer, e.demo.Slug, id); err != nil {
			t.Errorf("宛先が %d を取得: %v", id, err)
		}
		if _, err := e.s.TimelinePostDeliveryStatus(e.ctx, ed, e.demo.Slug, id); err != nil {
			t.Errorf("送り手が %d の到達の状況: %v", id, err)
		}
	}
	if _, err := e.s.AckTimelinePost(e.ctx, out, TimelinePostAckInput{Project: e.demo.Slug, ID: toUser.ID, Version: 1}); memErrCode(err) != "not_found" {
		t.Errorf("宛先でない参加者の了解: %v", err)
	}
	if n, err := e.s.RecordTimelinePostReceipts(e.ctx, out, e.demo.Slug, []TimelinePostReceiptInput{{ID: toUser.ID, Version: 1}}); err != nil || n != 0 {
		t.Errorf("宛先でない参加者の受け取り: %d %v", n, err)
	}
	// セッション宛ては持ち主の別のセッションには効かない（持ち主には読める）
	if got := e.timelinePostList(inSession(e.viewer, "sess-v2")); !slices.Equal(got, []int64{toUser.ID, all.ID}) {
		t.Errorf("別のセッションの一覧: %v", got)
	}
	if got := e.timelinePostList(inSession(e.viewer, "sess-v1")); !slices.Equal(got, []int64{toUser.ID, toSess.ID, all.ID}) {
		t.Errorf("宛先のセッションの一覧: %v", got)
	}
	// 到達の状況: 受け取り・了解を持つセッションと、宛先に当たる動いているセッション
	v1 := inSession(e.viewer, "sess-v1")
	if _, err := e.s.AckTimelinePost(e.ctx, v1, TimelinePostAckInput{Project: e.demo.Slug, ID: toUser.ID, Version: 1}); err != nil {
		t.Fatal(err)
	}
	for _, a := range []Actor{inSession(e.viewer, "sess-v2"), out, ed} {
		if err := e.s.ReportPresence(e.ctx, a, PresenceInput{Project: e.demo.Slug, Agent: "claude-code", MachineLabel: "box", Branch: "main", Directory: "repo"}); err != nil {
			t.Fatal(err)
		}
	}
	st, err := e.s.TimelinePostDeliveryStatus(e.ctx, v1, e.demo.Slug, toUser.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range st.Rows {
		got = append(got, r.User+"/"+r.Session)
	}
	if !slices.Equal(got, []string{"vi/sess-v1", "vi/sess-v2"}) {
		t.Fatalf("到達の状況の行: %v", got)
	}
	if st.Rows[0].Received != 1 || st.Rows[0].Acked != 1 || !st.Rows[0].LastSeenAt.IsZero() {
		t.Errorf("了解したセッションの行: %+v", st.Rows[0])
	}
	if st.Rows[1].Received != 0 || st.Rows[1].MachineLabel != "box" || st.Rows[1].LastSeenAt.IsZero() {
		t.Errorf("動いているだけのセッションの行: %+v", st.Rows[1])
	}
	// プロジェクト宛ては宛先でない参加者のセッションも並ぶ（出したセッション自身は除く）
	st, err = e.s.TimelinePostDeliveryStatus(e.ctx, out, e.demo.Slug, all.ID)
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	for _, r := range st.Rows {
		got = append(got, r.User+"/"+r.Session)
	}
	if !slices.Equal(got, []string{"out/sess-out", "vi/sess-v2"}) {
		t.Errorf("プロジェクト宛ての到達の状況の行: %v", got)
	}
}

// 資格情報を含む本文・理由は拒まれ、エラーに値が出ない。
func TestTimelinePostRejectsSecrets(t *testing.T) {
	e := newAttachEnv(t)
	ed := inSession(e.editor, "sess-ed")
	_, err := e.post(ed, TimelinePostInput{Kind: store.TimelinePostHold, To: store.MessageToProject, Body: "接続の設定\n" + tSecretValue + "\n"})
	if memErrCode(err) != "message_secret" {
		t.Fatalf("資格情報の本文: %v", err)
	}
	for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
		if msg := i18n.Text(lang, err); strings.Contains(msg, "Synthetic0Value9") || !strings.Contains(msg, "2") {
			t.Errorf("理由（%s）に値が出ているか行番号が無い: %s", lang, msg)
		}
	}
	if n := e.count("SELECT COUNT(*) FROM timeline_posts"); n != 0 {
		t.Errorf("拒んだ投稿が %d 件残った", n)
	}
	v := e.mustPost(ed, TimelinePostInput{Kind: store.TimelinePostHold, To: store.MessageToProject, Body: "待機"}) // 対照
	if _, err := e.s.ReviseTimelinePost(e.ctx, ed, TimelinePostReviseInput{Project: e.demo.Slug, ID: v.ID, Body: tSecretValue}); memErrCode(err) != "message_secret" {
		t.Errorf("資格情報の改訂: %v", err)
	}
	e.joinDemo(e.outside, "editor")
	_, err = e.s.WithdrawTimelinePost(e.ctx, e.outside, TimelinePostWithdrawInput{Project: e.demo.Slug, ID: v.ID, Reason: tSecretValue})
	if memErrCode(err) != "message_secret" || strings.Contains(i18n.Text(i18n.JA, err), "Synthetic0Value9") {
		t.Errorf("資格情報の理由: %v", err)
	}
	if _, err := e.post(inSession(e.editor, tSecretValue), TimelinePostInput{Kind: store.TimelinePostInfo, To: store.MessageToProject, Body: "本文"}); memErrCode(err) != "message_secret" {
		t.Errorf("出したセッションの資格情報: %v", err)
	}
	if n := e.count("SELECT COUNT(*) FROM timeline_post_events"); n != 1 {
		t.Errorf("出来事の数: %d（対照の投稿 1 件だけのはず）", n)
	}
	if _, err := e.post(ed, TimelinePostInput{Kind: store.TimelinePostInfo, To: store.MessageToProject, Body: strings.Repeat("あ", MaxMessageBytes/3+1)}); memErrCode(err) != "message_too_large" {
		t.Errorf("上限を超える本文: %v", err)
	}
	if _, err := e.post(ed, TimelinePostInput{Kind: "notice", To: store.MessageToProject, Body: "本文"}); memErrCode(err) != "invalid_argument" {
		t.Errorf("無い種類: %v", err)
	}
}

// 「同じ利用者が出したか」はサーバが認証した利用者で決まり、名乗るセッション ID を変えても変わらない。
func TestTimelinePostSameUserIgnoresSessionID(t *testing.T) {
	e := newAttachEnv(t)
	v := e.mustPost(inSession(e.editor, "sess-ed"), TimelinePostInput{Kind: store.TimelinePostRequest, To: store.MessageToProject, Body: "依頼"})
	for _, sess := range []string{"", "sess-vi", "sess-ed", "mcp-conn:x"} {
		d, err := e.s.GetTimelinePost(e.ctx, inSession(e.viewer, sess), e.demo.Slug, v.ID)
		if err != nil || d.SameUser {
			t.Errorf("viewer（セッション %q）: SameUser=%v %v", sess, d != nil && d.SameUser, err)
		}
	}
	for _, sess := range []string{"", "sess-other", "sess-vi", "mcp-conn:y"} {
		d, err := e.s.GetTimelinePost(e.ctx, inSession(e.editor, sess), e.demo.Slug, v.ID)
		if err != nil || !d.SameUser {
			t.Errorf("対照: 出した利用者（セッション %q）: SameUser=%v %v", sess, d != nil && d.SameUser, err)
		}
	}
}

// イシューを添えると「タイムライン:」のコメントが残る。無いイシューは拒み、書き込みもコメントも残さない。
func TestTimelinePostIssueComment(t *testing.T) {
	e := newAttachEnv(t)
	v := e.mustPost(e.editor, TimelinePostInput{Kind: store.TimelinePostHold, To: store.MessageToProject, Body: "このイシューの反映を待ってください", Issue: e.issue.Item.ID})
	if v.IssueID != e.issue.Row.ID {
		t.Errorf("書き込みのイシュー: %d", v.IssueID)
	}
	if n := e.count("SELECT COUNT(*) FROM comments WHERE content LIKE 'タイムライン: このイシューの反映を待ってください%'"); n != 1 {
		t.Errorf("コメントの数: %d", n)
	}
	if _, err := e.post(e.editor, TimelinePostInput{Kind: store.TimelinePostInfo, To: store.MessageToProject, Body: "無いイシュー", Issue: "XX-9999"}); memErrCode(err) != "not_found" {
		t.Errorf("無いイシュー: %v", err)
	}
	if _, err := e.post(e.editor, TimelinePostInput{Kind: store.TimelinePostInfo, To: store.MessageToProject, Body: "別のプロジェクト", Issue: e.otherIssue.Item.ID}); memErrCode(err) != "not_found" {
		t.Errorf("別のプロジェクトのイシュー: %v", err)
	}
	if n := e.count("SELECT COUNT(*) FROM timeline_posts"); n != 1 {
		t.Errorf("書き込みの数: %d", n)
	}
}

// 保持期間（message_retention_days を共有）を過ぎた書き込みは読みの経路に出ない。0 は無期限。
func TestTimelinePostRetention(t *testing.T) {
	e := newAttachEnv(t)
	base := e.s.Now
	e.s.Now = func() time.Time { return base().AddDate(0, 0, -40) }
	old := e.mustPost(e.editor, TimelinePostInput{Kind: store.TimelinePostInfo, To: store.MessageToProject, Body: "古い知らせ"})
	e.s.Now = base
	cur := e.mustPost(e.editor, TimelinePostInput{Kind: store.TimelinePostInfo, To: store.MessageToProject, Body: "新しい知らせ"})
	if got := e.timelinePostList(e.viewer); !slices.Equal(got, []int64{cur.ID}) {
		t.Errorf("既定の 30 日: %v", got)
	}
	if _, err := e.s.GetTimelinePost(e.ctx, e.viewer, e.demo.Slug, old.ID); memErrCode(err) != "not_found" {
		t.Errorf("保持期間を過ぎた取得: %v", err)
	}
	if _, _, err := store.SetMessageRetentionDays(e.ctx, e.admin, 0, store.SettingChange{Via: "command"}); err != nil {
		t.Fatal(err)
	}
	if got := e.timelinePostList(e.viewer); len(got) != 2 {
		t.Errorf("無期限: %v（対照。行は残っているので 2 件のはず）", got)
	}
}

// timeline_posts・timeline_post_events・timeline_post_receipts は追記専用（アプリ用の利用者で UPDATE・DELETE できない）。
// 対照として timeline_heads は同じ接続で書き換えられる。
func TestTimelinePostTablesAreAppendOnly(t *testing.T) {
	e := newAttachEnv(t)
	v := e.mustPost(e.editor, TimelinePostInput{Kind: store.TimelinePostRequest, To: store.MessageToProject, Body: "残る依頼"})
	if _, err := e.s.AckTimelinePost(e.ctx, e.viewer, TimelinePostAckInput{Project: e.demo.Slug, ID: v.ID, Version: 1}); err != nil {
		t.Fatalf("前提が崩れています: アプリ用の利用者で了解を足せない: %v", err)
	}
	want := "command denied"
	if testutil.SQLite() {
		want = "append-only"
	}
	for _, stmt := range []string{"UPDATE timeline_posts SET kind = 'info' WHERE id = ?", "DELETE FROM timeline_posts WHERE id = ?",
		"UPDATE timeline_post_events SET body = 'x' WHERE post_id = ?", "DELETE FROM timeline_post_events WHERE post_id = ?",
		"UPDATE timeline_post_receipts SET version = 9 WHERE post_id = ?", "DELETE FROM timeline_post_receipts WHERE post_id = ?"} {
		_, err := e.app.ExecContext(e.ctx, stmt, v.ID)
		if !store.IsAppendOnlyViolation(err) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q で拒否", stmt, err, want)
		}
	}
	if _, err := e.app.ExecContext(e.ctx, "UPDATE timeline_heads SET updated_at = updated_at WHERE project_id = ?", e.demo.ID); err != nil {
		t.Errorf("対照: timeline_heads の書き換え: %v", err)
	}
}

// 通し番号を返す読みは、通し番号を先に読む。2 つの読みの間に投稿を差し込み、結果に入らない投稿が通し番号に数えられて
// いないことを確かめる（逆の順では軽い確認がその投稿を読み飛ばす）。差し込みが呼ばれたことも確かめる。
func TestTimelinePostSeqReadBeforeTimelinePosts(t *testing.T) {
	e := newAttachEnv(t)
	ed, vi := inSession(e.editor, "sess-ed"), inSession(e.viewer, "sess-vi")
	e.mustPost(ed, TimelinePostInput{Kind: store.TimelinePostInfo, To: store.MessageToProject, Body: "前からある知らせ"})
	t.Cleanup(func() { timelinePostBetweenReads = nil })
	// inject は 2 つの読みの間に待機を 1 件投稿し、その通し番号を返す関数を用意する
	inject := func() *int64 {
		var seq int64
		timelinePostBetweenReads = func() {
			timelinePostBetweenReads = nil
			seq = e.mustPost(ed, TimelinePostInput{Kind: store.TimelinePostHold, To: store.MessageToProject, Body: "読みの間の待機"}).LastSeq
		}
		return &seq
	}
	check := func(name string, gotSeq int64, injected int64, seen bool) {
		t.Helper()
		if injected == 0 {
			t.Fatalf("%s: 前提が崩れています: 読みの間の差し込みが呼ばれていない", name)
		}
		if !seen && gotSeq >= injected {
			t.Errorf("%s: 通し番号 %d は差し込んだ投稿（%d）を数えているのに、結果に入っていない", name, gotSeq, injected)
		}
	}
	hasHold := func(vs []TimelinePostView) bool {
		for _, v := range vs {
			if v.Kind == store.TimelinePostHold {
				return true
			}
		}
		return false
	}
	seq := inject()
	l, err := e.s.ListTimelinePosts(e.ctx, vi, e.demo.Slug, TimelinePostListQuery{})
	if err != nil {
		t.Fatal(err)
	}
	check("一覧", l.Seq, *seq, hasHold(l.TimelinePosts))
	seq = inject()
	p, err := e.s.PendingTimelinePosts(e.ctx, vi, e.demo.Slug, 0)
	if err != nil {
		t.Fatal(err)
	}
	check("未達", p.Seq, *seq, hasHold(p.Undelivered) && len(p.Undelivered) == 3)
	seq = inject()
	c, err := e.s.CheckTimelinePosts(e.ctx, vi, e.demo.Slug)
	if err != nil {
		t.Fatal(err)
	}
	check("軽い確認", c.Seq, *seq, len(c.Holds) == 3)
	seq = inject()
	ev, err := e.s.TimelinePostEventsAfter(e.ctx, vi, e.demo.Slug, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	check("出来事", ev.Seq, *seq, len(ev.Events) == 5)
}

// 受け取りと了解のセッション ID は投稿のセッションと同じ検査を通る（到達の状況で送り手に見えるため）。
// 対照: 普通のセッションとセッションの無い読み手（空文字）は通る。
func TestTimelinePostReceiptSessionChecked(t *testing.T) {
	e := newAttachEnv(t)
	v := e.mustPost(inSession(e.editor, "sess-ed"), TimelinePostInput{Kind: store.TimelinePostRequest, To: store.MessageToProject, Body: "依頼"})
	for _, bad := range []struct{ sess, code string }{{tSecretValue, "message_secret"}, {"sess\x1b[31m", "invalid_argument"}} {
		a := inSession(e.viewer, bad.sess)
		if _, err := e.s.RecordTimelinePostReceipts(e.ctx, a, e.demo.Slug, []TimelinePostReceiptInput{{ID: v.ID, Version: 1}}); memErrCode(err) != bad.code {
			t.Errorf("受け取り（%q）: %v", bad.code, err)
		}
		if _, err := e.s.AckTimelinePost(e.ctx, a, TimelinePostAckInput{Project: e.demo.Slug, ID: v.ID, Version: 1}); memErrCode(err) != bad.code {
			t.Errorf("了解（%q）: %v", bad.code, err)
		}
	}
	if n := e.count("SELECT COUNT(*) FROM timeline_post_receipts"); n != 0 {
		t.Fatalf("拒んだ受け取りが %d 件残った", n)
	}
	for _, sess := range []string{"sess-vi", ""} {
		a := inSession(e.viewer, sess)
		if n, err := e.s.RecordTimelinePostReceipts(e.ctx, a, e.demo.Slug, []TimelinePostReceiptInput{{ID: v.ID, Version: 1}}); err != nil || n != 1 {
			t.Errorf("対照: 受け取り（%q）: %d %v", sess, n, err)
		}
		if _, err := e.s.AckTimelinePost(e.ctx, a, TimelinePostAckInput{Project: e.demo.Slug, ID: v.ID, Version: 1}); err != nil {
			t.Errorf("対照: 了解（%q）: %v", sess, err)
		}
	}
}

// 保持期間が期限より短くても、期限の来ていない待機は読める。対照: 期限を過ぎた待機と、期限の無い知らせは保持期間で消える。
func TestTimelinePostRetentionKeepsUnexpired(t *testing.T) {
	e := newAttachEnv(t)
	base := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	now := base
	e.s.Now = func() time.Time { return now }
	if _, _, err := store.SetMessageRetentionDays(e.ctx, e.admin, 1, store.SettingChange{Via: "command"}); err != nil {
		t.Fatal(err)
	}
	ed, vi := inSession(e.editor, "sess-ed"), inSession(e.viewer, "sess-vi")
	hold := e.mustPost(ed, TimelinePostInput{Kind: store.TimelinePostHold, To: store.MessageToProject, Body: "3 日の待機", TTLSeconds: 3 * 24 * 3600})
	info := e.mustPost(ed, TimelinePostInput{Kind: store.TimelinePostInfo, To: store.MessageToProject, Body: "期限の無い知らせ"})
	if got := e.timelinePostList(vi); !slices.Equal(got, []int64{hold.ID, info.ID}) {
		t.Fatalf("前提が崩れています: 投稿した直後の一覧: %v", got)
	}
	now = base.Add(48 * time.Hour)
	if got := e.timelinePostList(vi); !slices.Equal(got, []int64{hold.ID}) {
		t.Errorf("2 日後の一覧: %v（待機だけが残るはず）", got)
	}
	if c, err := e.s.CheckTimelinePosts(e.ctx, vi, e.demo.Slug); err != nil || !slices.Equal(c.Holds, []int64{hold.ID}) {
		t.Errorf("2 日後の軽い確認: %+v %v", c, err)
	}
	if _, err := e.s.GetTimelinePost(e.ctx, vi, e.demo.Slug, info.ID); memErrCode(err) != "not_found" {
		t.Errorf("保持期間を過ぎた知らせの取得: %v", err)
	}
	now = base.Add(72 * time.Hour)
	if got := e.timelinePostList(vi); len(got) != 0 {
		t.Errorf("期限を過ぎた後の一覧: %v", got)
	}
	if _, err := e.s.GetTimelinePost(e.ctx, vi, e.demo.Slug, hold.ID); memErrCode(err) != "not_found" {
		t.Errorf("期限を過ぎ保持期間も過ぎた待機の取得: %v", err)
	}
}

// 通し番号より後の出来事は、ほかの読みと同じく書き込みごとに保持期間で切る（読める書き込みなら古い版の出来事も返す）。
// 対照: 保持期間を過ぎた書き込みの出来事は返さない。
func TestTimelinePostEventsAfterRetentionPerTimelinePost(t *testing.T) {
	e := newAttachEnv(t)
	base := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	now := base.Add(-48 * time.Hour)
	e.s.Now = func() time.Time { return now }
	ed, vi := inSession(e.editor, "sess-ed"), inSession(e.viewer, "sess-vi")
	revised := e.mustPost(ed, TimelinePostInput{Kind: store.TimelinePostInfo, To: store.MessageToProject, Body: "後で直す知らせ"})
	old := e.mustPost(ed, TimelinePostInput{Kind: store.TimelinePostInfo, To: store.MessageToProject, Body: "古いままの知らせ"})
	now = base
	if _, err := e.s.ReviseTimelinePost(e.ctx, ed, TimelinePostReviseInput{Project: e.demo.Slug, ID: revised.ID, Body: "直した知らせ"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SetMessageRetentionDays(e.ctx, e.admin, 1, store.SettingChange{Via: "command"}); err != nil {
		t.Fatal(err)
	}
	res, err := e.s.TimelinePostEventsAfter(e.ctx, vi, e.demo.Slug, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, ev := range res.Events {
		got = append(got, ev.Action+"/"+strconv.FormatInt(ev.TimelinePostID, 10))
	}
	want := []string{"post/" + strconv.FormatInt(revised.ID, 10), "revise/" + strconv.FormatInt(revised.ID, 10)}
	if !slices.Equal(got, want) {
		t.Errorf("出来事: %v（%v のはず。%d は保持期間の外）", got, want, old.ID)
	}
	if d, err := e.s.GetTimelinePost(e.ctx, vi, e.demo.Slug, revised.ID); err != nil || len(d.Events) != len(want) {
		t.Errorf("前提が崩れています: 1 件の取得と出来事の数が合わない: %+v %v", d, err)
	}
}

// 受け取りは読み手のセッションに効く書き込みにだけ記録する。出したセッションと、宛先のセッションでない持ち主の別のセッションは
// 記録されず、到達の状況にも並ばない。対照: 宛先のセッションの受け取りは記録されて並ぶ。
func TestTimelinePostReceiptsOnlyForRecipients(t *testing.T) {
	e := newAttachEnv(t)
	ed := inSession(e.editor, "sess-ed")
	v := e.mustPost(ed, TimelinePostInput{Kind: store.TimelinePostRequest, To: store.MessageToSession, User: "vi", Session: "sess-v1", Body: "v1 への依頼"})
	for _, a := range []Actor{ed, inSession(e.editor, "sess-ed2"), inSession(e.viewer, "sess-v2")} {
		if n, err := e.s.RecordTimelinePostReceipts(e.ctx, a, e.demo.Slug, []TimelinePostReceiptInput{{ID: v.ID, Version: 1}}); err != nil || n != 0 {
			t.Errorf("宛先でないセッション %q の受け取り: %d %v", a.SessionID, n, err)
		}
	}
	if n, err := e.s.RecordTimelinePostReceipts(e.ctx, inSession(e.viewer, "sess-v1"), e.demo.Slug, []TimelinePostReceiptInput{{ID: v.ID, Version: 1}}); err != nil || n != 1 {
		t.Fatalf("対照: 宛先のセッションの受け取り: %d %v", n, err)
	}
	st, err := e.s.TimelinePostDeliveryStatus(e.ctx, ed, e.demo.Slug, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range st.Rows {
		got = append(got, r.User+"/"+r.Session)
	}
	if !slices.Equal(got, []string{"vi/sess-v1"}) {
		t.Errorf("到達の状況の行: %v", got)
	}
}
