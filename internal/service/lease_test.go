package service

import (
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// 占有の service（lease.go）のテスト。土台は記憶と同じ attachEnv。demo の参加者は editor（ed）と viewer（vi）で、
// root は参加していない管理者（viewer として読める）、out は other だけの参加者。

func (e *attachEnv) acquireLease(a Actor, name string, ttl int64) (*LeaseAcquired, error) {
	return e.s.AcquireLease(e.ctx, a, LeaseInput{Project: e.demo.Slug, Name: name, Purpose: "本番へ反映する", TTLSeconds: ttl, MachineLabel: "box-a"})
}

// leaseEditors は demo の editor を n 人作り、別々のセッションの Actor を返す。
func (e *attachEnv) leaseEditors(t *testing.T, n int) []Actor {
	t.Helper()
	out := make([]Actor, 0, n)
	for i := 0; i < n; i++ {
		login := "lease-ed-" + string(rune('a'+i))
		id, err := store.CreateUser(e.ctx, e.admin, login, login, "x", "member")
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SetMember(e.ctx, e.admin, e.demo.ID, id, "editor"); err != nil {
			t.Fatal(err)
		}
		out = append(out, Actor{UserID: id, Via: "cli", Lang: i18n.JA, SessionID: "session-" + login})
	}
	return out
}

// 受け入れ条件: 同じ資源を取り合うと 1 つだけが取れ、もう一方には取っている者と期限が返る。
// 行が無い状態から同時に取るので、行を用意する経路（store.EnsureLeaseRow）も通り、デッドロックのエラーが利用者に返らないことも見る。
func TestLeaseConcurrentAcquireOneWins(t *testing.T) {
	e := newAttachEnv(t)
	actors := e.leaseEditors(t, 8)
	for round := 0; round < 5; round++ {
		name := "deploy.round-" + string(rune('a'+round))
		var wg sync.WaitGroup
		results := make([]error, len(actors))
		start := make(chan struct{})
		for i, a := range actors {
			wg.Add(1)
			go func(i int, a Actor) {
				defer wg.Done()
				<-start
				_, results[i] = e.acquireLease(a, name, 0)
			}(i, a)
		}
		close(start)
		wg.Wait()
		won := 0
		for _, err := range results {
			if err == nil {
				won++
				continue
			}
			var se *Error
			if !errors.As(err, &se) || se.Code != "lease_held" {
				t.Fatalf("%s: 負けた側は lease_held のはず: %v", name, err)
			}
			if se.Lease == nil || se.Lease.HolderLogin == "" || se.Lease.ExpiresAt.IsZero() {
				t.Fatalf("%s: 取っている者と期限が返っていない: %+v", name, se.Lease)
			}
			if !strings.Contains(i18n.Text(i18n.JA, se), se.Lease.HolderLogin) || !strings.Contains(i18n.Text(i18n.JA, se), "本番へ反映する") {
				t.Fatalf("%s: 文面に持ち主と目的が無い: %s", name, i18n.Text(i18n.JA, se))
			}
		}
		if won != 1 {
			t.Fatalf("%s: 取れた数 = %d, want 1", name, won)
		}
	}
}

// 別々の名前を同時に取っても、どれも取れる（行を作る経路がほかの名前を巻き込んで止まらない）。
func TestLeaseConcurrentAcquireDifferentNames(t *testing.T) {
	e := newAttachEnv(t)
	actors := e.leaseEditors(t, 8)
	var wg sync.WaitGroup
	errs := make([]error, len(actors))
	for i, a := range actors {
		wg.Add(1)
		go func(i int, a Actor) {
			defer wg.Done()
			_, errs[i] = e.acquireLease(a, "name-"+string(rune('a'+i)), 0)
		}(i, a)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("%d: %v", i, err)
		}
	}
}

// 受け入れ条件: 期限が切れた後は取れる。切れる前は取れない（同じテストの対照）。取った記録に期限切れからと残る。
func TestLeaseExpiryAllowsTakeover(t *testing.T) {
	e := newAttachEnv(t)
	base := time.Date(2026, 10, 30, 3, 0, 0, 0, time.UTC)
	now := base
	e.s.Now = func() time.Time { return now }
	actors := e.leaseEditors(t, 2)
	if _, err := e.acquireLease(actors[0], "deploy.dev", 600); err != nil {
		t.Fatal(err)
	}
	// 対照: 切れる 1 秒前は取れない
	now = base.Add(599 * time.Second)
	if _, err := e.acquireLease(actors[1], "deploy.dev", 600); memErrCode(err) != "lease_held" {
		t.Fatalf("切れる前: %v", err)
	}
	// 期限ちょうどから取れる
	now = base.Add(600 * time.Second)
	got, err := e.acquireLease(actors[1], "deploy.dev", 600)
	if err != nil {
		t.Fatalf("切れた後: %v", err)
	}
	if got.HolderUserID != actors[1].UserID || !got.ExpiresAt.Equal(now.Add(600*time.Second)) {
		t.Fatalf("取り直した結果: %+v", got)
	}
	d, err := e.s.GetLease(e.ctx, e.editor, e.demo.Slug, "deploy.dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Events) != 2 || !d.Events[0].WasExpired || d.Events[0].PrevUserID != actors[0].UserID || d.Events[1].WasExpired {
		t.Fatalf("記録: %+v", d.Events)
	}
}

// 期限が切れて取り直されていないものは、summary には出ず、一覧には expired で出る。切れた後の延長は取り直しを求め、返すことはできる。
func TestLeaseExpiredVisibilityAndHolderOps(t *testing.T) {
	e := newAttachEnv(t)
	base := time.Date(2026, 10, 30, 3, 0, 0, 0, time.UTC)
	now := base
	e.s.Now = func() time.Time { return now }
	a := e.leaseEditors(t, 1)[0]
	if _, err := e.acquireLease(a, "publish", 60); err != nil {
		t.Fatal(err)
	}
	active, err := e.s.ProjectLeases(e.ctx, e.demo)
	if err != nil || len(active) != 1 { // 対照: 切れる前は summary に出る
		t.Fatalf("切れる前: %v %+v", err, active)
	}
	now = base.Add(61 * time.Second)
	active, err = e.s.ProjectLeases(e.ctx, e.demo)
	if err != nil || len(active) != 0 {
		t.Fatalf("切れた後の summary: %v %+v", err, active)
	}
	list, _, err := e.s.ListLeases(e.ctx, e.viewer, e.demo.Slug)
	if err != nil || len(list) != 1 || !list[0].Expired {
		t.Fatalf("切れた後の一覧: %v %+v", err, list)
	}
	in := LeaseInput{Project: e.demo.Slug, Name: "publish"}
	if _, err := e.s.ExtendLease(e.ctx, a, in); memErrCode(err) != "lease_expired" {
		t.Fatalf("切れた後の延長: %v", err)
	}
	if err := e.s.ReleaseLease(e.ctx, a, in); err != nil {
		t.Fatalf("切れた後に返す: %v", err)
	}
	d, err := e.s.GetLease(e.ctx, e.viewer, e.demo.Slug, "publish")
	if err != nil || d.Held() || d.Events[0].Action != store.LeaseActionRelease || !d.Events[0].WasExpired {
		t.Fatalf("返した後: %v %+v", err, d)
	}
}

// 延ばす・返すのは取っている者（同じ利用者の同じセッション）だけ。別のセッションは同じ利用者でも拒む。
func TestLeaseExtendReleaseOnlyHolder(t *testing.T) {
	e := newAttachEnv(t)
	base := time.Date(2026, 10, 30, 3, 0, 0, 0, time.UTC)
	now := base
	e.s.Now = func() time.Time { return now }
	actors := e.leaseEditors(t, 2)
	holder, other := actors[0], actors[1]
	sameUserOtherSession := holder
	sameUserOtherSession.SessionID = "another-session"
	if _, err := e.acquireLease(holder, "deploy.dev", 600); err != nil {
		t.Fatal(err)
	}
	in := LeaseInput{Project: e.demo.Slug, Name: "deploy.dev", TTLSeconds: 1200, MachineLabel: "box-b"}
	for name, a := range map[string]Actor{"別の利用者": other, "同じ利用者の別のセッション": sameUserOtherSession} {
		if _, err := e.s.ExtendLease(e.ctx, a, in); memErrCode(err) != "lease_not_holder" {
			t.Errorf("%s の延長: %v", name, err)
		}
		if err := e.s.ReleaseLease(e.ctx, a, in); memErrCode(err) != "lease_not_holder" {
			t.Errorf("%s の返却: %v", name, err)
		}
	}
	if _, err := e.acquireLease(holder, "deploy.dev", 600); memErrCode(err) != "lease_held" { // 同じセッションの取り直しも 409
		t.Errorf("同じセッションの 2 回目: %v", err)
	}
	now = base.Add(100 * time.Second)
	got, err := e.s.ExtendLease(e.ctx, holder, in)
	if err != nil || !got.ExpiresAt.Equal(now.Add(1200*time.Second)) || got.Purpose != "本番へ反映する" {
		t.Fatalf("持ち主の延長: %v %+v", err, got)
	}
	if err := e.s.ReleaseLease(e.ctx, holder, in); err != nil {
		t.Fatalf("持ち主の返却: %v", err)
	}
	if err := e.s.ReleaseLease(e.ctx, holder, in); memErrCode(err) != "not_found" {
		t.Errorf("空いているものを返す: %v", err)
	}
	if _, err := e.acquireLease(other, "deploy.dev", 600); err != nil { // 返した後はほかの者が取れる
		t.Fatalf("返した後: %v", err)
	}
}

// 受け入れ条件: 権限の無い利用者は他人の占有を外せず、権限のある利用者は理由つきで外せる。理由が記録に残る。
func TestLeaseBreakRequiresRoleAndReason(t *testing.T) {
	e := newAttachEnv(t)
	actors := e.leaseEditors(t, 2)
	holder, breaker := actors[0], actors[1]
	if _, err := e.acquireLease(holder, "deploy.dev", 0); err != nil {
		t.Fatal(err)
	}
	in := LeaseInput{Project: e.demo.Slug, Name: "deploy.dev", Reason: "取った端末が落ちたので外す", MachineLabel: "box-c"}
	if _, err := e.s.BreakLease(e.ctx, e.viewer, in); memErrCode(err) != "forbidden" {
		t.Fatalf("viewer: %v", err)
	}
	if _, err := e.s.BreakLease(e.ctx, e.root, in); memErrCode(err) != "forbidden" {
		t.Fatalf("参加していない管理者: %v", err)
	}
	if _, err := e.s.BreakLease(e.ctx, e.outside, in); memErrCode(err) != "not_found" {
		t.Fatalf("参加していない利用者: %v", err)
	}
	noReason := in
	noReason.Reason = "  "
	if _, err := e.s.BreakLease(e.ctx, breaker, noReason); memErrCode(err) != "invalid_argument" {
		t.Fatalf("理由なし: %v", err)
	}
	// ここまでのどれも持ち主を変えていない
	d, err := e.s.GetLease(e.ctx, e.viewer, e.demo.Slug, "deploy.dev")
	if err != nil || d.HolderUserID != holder.UserID || len(d.Events) != 1 {
		t.Fatalf("拒否の後: %v %+v", err, d)
	}
	// 対照: editor は理由つきで外せる
	if _, err := e.s.BreakLease(e.ctx, breaker, in); err != nil {
		t.Fatalf("editor: %v", err)
	}
	d, err = e.s.GetLease(e.ctx, e.viewer, e.demo.Slug, "deploy.dev")
	if err != nil || d.Held() || len(d.Events) != 2 {
		t.Fatalf("外した後: %v %+v", err, d)
	}
	ev := d.Events[0]
	if ev.Action != store.LeaseActionBreak || ev.Reason != in.Reason || ev.UserID != breaker.UserID || ev.PrevUserID != holder.UserID ||
		ev.PrevSessionID != holder.SessionID || ev.MachineLabel != "box-c" {
		t.Fatalf("記録: %+v", ev)
	}
	if _, err := e.acquireLease(breaker, "deploy.dev", 0); err != nil {
		t.Fatalf("外した後に取る: %v", err)
	}
}

// 参加者の読み書きの範囲と、入力の検査。
func TestLeaseAccessAndValidation(t *testing.T) {
	e := newAttachEnv(t)
	if _, err := e.acquireLease(e.viewer, "deploy.dev", 0); memErrCode(err) != "forbidden" {
		t.Fatalf("viewer は取れない: %v", err)
	}
	if _, err := e.acquireLease(e.outside, "deploy.dev", 0); memErrCode(err) != "not_found" {
		t.Fatalf("参加していない利用者は無いものと同じ: %v", err)
	}
	if _, _, err := e.s.ListLeases(e.ctx, e.outside, e.demo.Slug); memErrCode(err) != "not_found" {
		t.Fatalf("参加していない利用者の一覧: %v", err)
	}
	if _, _, err := e.s.ListLeases(e.ctx, e.root, e.demo.Slug); err != nil {
		t.Fatalf("参加していない管理者は読める: %v", err)
	}
	if _, err := e.s.GetLease(e.ctx, e.editor, e.demo.Slug, "never-taken"); memErrCode(err) != "not_found" {
		t.Fatalf("取られたことのない名前: %v", err)
	}
	for _, c := range []struct {
		name string
		in   LeaseInput
		code string
	}{
		{"大文字の名前", LeaseInput{Name: "Deploy", Purpose: "x"}, "invalid_argument"},
		{"先頭の点", LeaseInput{Name: ".deploy", Purpose: "x"}, "invalid_argument"},
		{"65 文字の名前", LeaseInput{Name: strings.Repeat("a", 65), Purpose: "x"}, "invalid_argument"},
		{"目的なし", LeaseInput{Name: "deploy"}, "invalid_argument"},
		{"201 文字の目的", LeaseInput{Name: "deploy", Purpose: strings.Repeat("あ", 201)}, "invalid_argument"},
		{"改行のある目的", LeaseInput{Name: "deploy", Purpose: "a\nb"}, "invalid_argument"},
		{"負の期限", LeaseInput{Name: "deploy", Purpose: "x", TTLSeconds: -1}, "invalid_argument"},
		{"上限を超える期限", LeaseInput{Name: "deploy", Purpose: "x", TTLSeconds: 86401}, "invalid_argument"},
		{"資格情報の目的", LeaseInput{Name: "deploy", Purpose: "password=hunter2hunter2"}, "lease_secret"},
	} {
		c.in.Project = e.demo.Slug
		if _, err := e.s.AcquireLease(e.ctx, e.editor, c.in); memErrCode(err) != c.code {
			t.Errorf("%s: %v, want %s", c.name, err, c.code)
		}
	}
	// 対照: 上限ちょうどと、既定の期限は通る
	got, err := e.s.AcquireLease(e.ctx, e.editor, LeaseInput{Project: e.demo.Slug, Name: "a.b-c", Purpose: "x", TTLSeconds: 86400})
	if err != nil || got.ExpiresAt.Sub(got.AcquiredAt) != 24*time.Hour {
		t.Fatalf("上限ちょうど: %v %+v", err, got)
	}
	got, err = e.s.AcquireLease(e.ctx, e.editor, LeaseInput{Project: e.demo.Slug, Name: "a.b-d", Purpose: "x"})
	if err != nil || got.ExpiresAt.Sub(got.AcquiredAt) != time.Hour {
		t.Fatalf("既定: %v %+v", err, got)
	}
}

// 外された者には user 宛ての連絡が届く（理由と資源が本文に入る）。取っていた同じセッションが自分で外したときは送らない（対照）。
func TestLeaseBreakNotifiesHolder(t *testing.T) {
	e := newAttachEnv(t)
	actors := e.leaseEditors(t, 2)
	holder, breaker := actors[0], actors[1]
	unread := func(a Actor) []store.Message {
		t.Helper()
		res, err := e.s.Inbox(e.ctx, a, e.demo.Slug, InboxQuery{Unread: true})
		if err != nil {
			t.Fatal(err)
		}
		return res.Messages
	}
	if _, err := e.acquireLease(holder, "deploy.dev", 0); err != nil {
		t.Fatal(err)
	}
	in := LeaseInput{Project: e.demo.Slug, Name: "deploy.dev", Reason: "取った端末が落ちた", MachineLabel: "box-c"}
	if _, err := e.s.BreakLease(e.ctx, e.viewer, in); memErrCode(err) != "forbidden" {
		t.Fatalf("viewer: %v", err)
	}
	if got := unread(holder); len(got) != 0 {
		t.Fatalf("拒まれた操作で連絡が届いた: %+v", got)
	}
	res, err := e.s.BreakLease(e.ctx, breaker, in)
	if err != nil || res.NoticeFailed || res.Holder == "" {
		t.Fatalf("外す: %v %+v", err, res)
	}
	got := unread(holder)
	if len(got) != 1 || got[0].ToKind != store.MessageToUser || got[0].ToUserID != holder.UserID || got[0].SenderUserID != breaker.UserID ||
		!strings.Contains(got[0].Body, "deploy.dev") || !strings.Contains(got[0].Body, in.Reason) || !strings.Contains(got[0].Body, "本番へ反映する") {
		t.Fatalf("連絡: %+v", got)
	}
	if got := unread(breaker); len(got) != 0 {
		t.Fatalf("外した者に届いた: %+v", got)
	}

	// 同じセッションが自分で外したときは送らない
	if _, err := e.acquireLease(breaker, "publish", 0); err != nil {
		t.Fatal(err)
	}
	in.Name = "publish"
	if res, err := e.s.BreakLease(e.ctx, breaker, in); err != nil || res.NoticeFailed {
		t.Fatalf("自分で外す: %v %+v", err, res)
	}
	sent, err := e.s.Inbox(e.ctx, breaker, e.demo.Slug, InboxQuery{Sent: true})
	if err != nil || len(sent.Messages) != 1 {
		t.Fatalf("送った連絡は 1 件のはず: %v %+v", err, sent)
	}
}

// 持ち主が参加者でなくなっていても外せる。連絡は送れないので NoticeFailed が立つ。
func TestLeaseBreakSucceedsWhenNoticeFails(t *testing.T) {
	e := newAttachEnv(t)
	actors := e.leaseEditors(t, 2)
	if _, err := e.acquireLease(actors[0], "deploy.dev", 0); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveMember(e.ctx, e.admin, e.demo.ID, actors[0].UserID); err != nil {
		t.Fatal(err)
	}
	res, err := e.s.BreakLease(e.ctx, actors[1], LeaseInput{Project: e.demo.Slug, Name: "deploy.dev", Reason: "参加者でなくなった"})
	if err != nil || !res.NoticeFailed {
		t.Fatalf("外す: %v %+v", err, res)
	}
	if _, err := e.acquireLease(actors[1], "deploy.dev", 0); err != nil {
		t.Fatalf("外した後に取る: %v", err)
	}
}

// セッションの無い要求は、同じ利用者でも持ち主と見ない。期限切れの後に別の端末が取り直した占有を、前の端末の遅い返却が消さない。
// 対照は今の鍵の返却で、同じ入力の形で通る。
func TestLeaseStaleReleaseKeepsNextHolder(t *testing.T) {
	e := newAttachEnv(t)
	base := time.Date(2026, 10, 30, 3, 0, 0, 0, time.UTC)
	now := base
	e.s.Now = func() time.Time { return now }
	a := e.leaseEditors(t, 1)[0]
	a.SessionID = "" // 手で打つターミナル。box-a と box-b は同じ利用者
	first, err := e.acquireLease(a, "deploy.dev", 60)
	if err != nil {
		t.Fatal(err)
	}
	now = base.Add(61 * time.Second)
	second, err := e.s.AcquireLease(e.ctx, a, LeaseInput{Project: e.demo.Slug, Name: "deploy.dev", Purpose: "取り直す", MachineLabel: "box-b"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Token == "" || first.Token == second.Token {
		t.Fatalf("鍵は取るたびに変わるはず: %q %q", first.Token, second.Token)
	}
	for name, token := range map[string]string{"鍵なし": "", "古い鍵": first.Token, "でたらめな鍵": "nope"} {
		in := LeaseInput{Project: e.demo.Slug, Name: "deploy.dev", Token: token}
		if err := e.s.ReleaseLease(e.ctx, a, in); memErrCode(err) != "lease_not_holder" {
			t.Errorf("%s の返却: %v", name, err)
		}
		if _, err := e.s.ExtendLease(e.ctx, a, in); memErrCode(err) != "lease_not_holder" {
			t.Errorf("%s の延長: %v", name, err)
		}
	}
	d, err := e.s.GetLease(e.ctx, e.viewer, e.demo.Slug, "deploy.dev")
	if err != nil || !d.Held() || d.MachineLabel != "box-b" {
		t.Fatalf("次の持ち主が消えた: %v %+v", err, d)
	}
	if err := e.s.ReleaseLease(e.ctx, a, LeaseInput{Project: e.demo.Slug, Name: "deploy.dev", Token: second.Token}); err != nil {
		t.Fatalf("今の鍵の返却: %v", err)
	}
}

// セッションが変わっても（MCP の接続の張り直し）鍵で延ばせて返せる。鍵が無ければ別のセッションは通らない。
// 同じセッションは鍵を省けるが、古い鍵を渡せばセッションが同じでも拒む。
func TestLeaseTokenSurvivesSessionChange(t *testing.T) {
	e := newAttachEnv(t)
	a := e.leaseEditors(t, 1)[0]
	got, err := e.acquireLease(a, "publish", 0)
	if err != nil {
		t.Fatal(err)
	}
	reconnected := a
	reconnected.SessionID = "mcp:reconnected"
	in := LeaseInput{Project: e.demo.Slug, Name: "publish"}
	if err := e.s.ReleaseLease(e.ctx, reconnected, in); memErrCode(err) != "lease_not_holder" {
		t.Fatalf("鍵なしの別のセッション: %v", err)
	}
	in.Token = got.Token
	if _, err := e.s.ExtendLease(e.ctx, reconnected, in); err != nil {
		t.Fatalf("鍵の延長: %v", err)
	}
	if err := e.s.ReleaseLease(e.ctx, reconnected, in); err != nil {
		t.Fatalf("鍵の返却: %v", err)
	}
	// 同じセッションの取り直し: 前の鍵は古い鍵になる
	next, err := e.acquireLease(a, "publish", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.ExtendLease(e.ctx, a, in); memErrCode(err) != "lease_not_holder" {
		t.Fatalf("同じセッションの古い鍵: %v", err)
	}
	if _, err := e.s.ExtendLease(e.ctx, a, LeaseInput{Project: e.demo.Slug, Name: "publish"}); err != nil {
		t.Fatalf("同じセッションの鍵なし: %v", err)
	}
	d, err := e.s.GetLease(e.ctx, e.viewer, e.demo.Slug, "publish")
	if err != nil || d.TokenHash != leaseTokenHash(next.Token) || d.TokenHash == next.Token {
		t.Fatalf("DB には鍵そのものを置かない: %v", err)
	}
}

// ちょうど期限の時刻は切れている。1 ナノ秒前は切れていない（対照）。extend の記録は action が extend で期限を持つ。
func TestLeaseExactExpiryAndExtendRecord(t *testing.T) {
	e := newAttachEnv(t)
	base := time.Date(2026, 10, 30, 3, 0, 0, 0, time.UTC)
	now := base
	e.s.Now = func() time.Time { return now }
	a := e.leaseEditors(t, 1)[0]
	if _, err := e.acquireLease(a, "publish", 60); err != nil {
		t.Fatal(err)
	}
	in := LeaseInput{Project: e.demo.Slug, Name: "publish", TTLSeconds: 60, MachineLabel: "box-e"}
	now = base.Add(60*time.Second - time.Microsecond)
	list, _, err := e.s.ListLeases(e.ctx, e.editor, e.demo.Slug)
	if err != nil || list[0].Expired {
		t.Fatalf("切れる直前の一覧: %v %+v", err, list)
	}
	got, err := e.s.ExtendLease(e.ctx, a, in)
	if err != nil || !got.ExpiresAt.Equal(now.Add(60*time.Second)) {
		t.Fatalf("切れる直前の延長: %v %+v", err, got)
	}
	d, err := e.s.GetLease(e.ctx, e.editor, e.demo.Slug, "publish")
	if err != nil || d.Events[0].Action != store.LeaseActionExtend || d.Events[0].MachineLabel != "box-e" || !d.Events[0].ExpiresAt.Equal(got.ExpiresAt) || d.Events[0].WasExpired {
		t.Fatalf("延長の記録: %v %+v", err, d.Events)
	}
	now = got.ExpiresAt
	list, _, err = e.s.ListLeases(e.ctx, e.editor, e.demo.Slug)
	if err != nil || !list[0].Expired {
		t.Fatalf("ちょうど期限の一覧: %v %+v", err, list)
	}
	if _, err := e.s.ExtendLease(e.ctx, a, in); memErrCode(err) != "lease_expired" {
		t.Fatalf("ちょうど期限の延長: %v", err)
	}
}

// --ttl を省いた延長は残りを縮めない。残りが既定より短ければ「今 + 既定」まで延びる（対照）。明示した長さはそのまま効く。
func TestLeaseExtendWithoutTTLNeverShrinks(t *testing.T) {
	e := newAttachEnv(t)
	base := time.Date(2026, 10, 30, 3, 0, 0, 0, time.UTC)
	now := base
	e.s.Now = func() time.Time { return now }
	a := e.leaseEditors(t, 1)[0]
	if _, err := e.acquireLease(a, "long", 6*3600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.acquireLease(a, "short", 60); err != nil {
		t.Fatal(err)
	}
	got, err := e.s.ExtendLease(e.ctx, a, LeaseInput{Project: e.demo.Slug, Name: "long"})
	if err != nil || !got.ExpiresAt.Equal(base.Add(6*time.Hour)) {
		t.Fatalf("長い残り: %v %+v", err, got)
	}
	got, err = e.s.ExtendLease(e.ctx, a, LeaseInput{Project: e.demo.Slug, Name: "short"})
	if err != nil || !got.ExpiresAt.Equal(base.Add(DefaultLeaseTTL)) {
		t.Fatalf("短い残り: %v %+v", err, got)
	}
	got, err = e.s.ExtendLease(e.ctx, a, LeaseInput{Project: e.demo.Slug, Name: "long", TTLSeconds: 60})
	if err != nil || !got.ExpiresAt.Equal(base.Add(time.Minute)) {
		t.Fatalf("明示した長さ: %v %+v", err, got)
	}
}

// 端末のラベル・セッション ID・名前の検査。どの書き込みの経路でも同じ検査を通る。対照は普通の値で通る。
func TestLeaseLineFieldsAreChecked(t *testing.T) {
	e := newAttachEnv(t)
	a := e.leaseEditors(t, 1)[0]
	const secret = "password=hunter2hunter2"
	badSession := a
	badSession.SessionID = "sess‮"
	secretSession := a
	secretSession.SessionID = secret
	for _, c := range []struct {
		name  string
		actor Actor
		in    LeaseInput
		code  string
	}{
		{"ラベルの制御文字", a, LeaseInput{Name: "x", Purpose: "p", MachineLabel: "box\x07"}, "invalid_argument"},
		{"ラベルの書式の文字", a, LeaseInput{Name: "x", Purpose: "p", MachineLabel: "box‮"}, "invalid_argument"},
		{"ラベルの資格情報", a, LeaseInput{Name: "x", Purpose: "p", MachineLabel: secret}, "lease_secret"},
		{"セッションの書式の文字", badSession, LeaseInput{Name: "x", Purpose: "p"}, "invalid_argument"},
		{"セッションの資格情報", secretSession, LeaseInput{Name: "x", Purpose: "p"}, "lease_secret"},
		{"名前の制御文字", a, LeaseInput{Name: "x\x07", Purpose: "p"}, "invalid_argument"},
		{"目的の書式の文字", a, LeaseInput{Name: "x", Purpose: "p‮"}, "invalid_argument"},
	} {
		c.in.Project = e.demo.Slug
		if _, err := e.s.AcquireLease(e.ctx, c.actor, c.in); memErrCode(err) != c.code {
			t.Errorf("%s: %v, want %s", c.name, err, c.code)
		}
	}
	got, err := e.s.AcquireLease(e.ctx, a, LeaseInput{Project: e.demo.Slug, Name: "x", Purpose: "p", MachineLabel: "box-a"})
	if err != nil {
		t.Fatalf("対照: %v", err)
	}
	for name, f := range map[string]func(LeaseInput) error{
		"extend":  func(in LeaseInput) error { _, err := e.s.ExtendLease(e.ctx, a, in); return err },
		"release": func(in LeaseInput) error { return e.s.ReleaseLease(e.ctx, a, in) },
		"break":   func(in LeaseInput) error { _, err := e.s.BreakLease(e.ctx, a, in); return err },
	} {
		in := LeaseInput{Project: e.demo.Slug, Name: "x", Reason: "r", MachineLabel: secret, Token: got.Token}
		if err := f(in); memErrCode(err) != "lease_secret" {
			t.Errorf("%s のラベル: %v", name, err)
		}
	}
}

// lease_events は追記専用（MySQL はアプリ用ユーザーの権限、SQLite はトリガ）。対照として leases の書き換えは同じ接続で通る。
func TestLeaseEventsAreAppendOnly(t *testing.T) {
	e := newAttachEnv(t)
	if _, err := e.acquireLease(e.editor, "publish", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := e.app.ExecContext(e.ctx, "UPDATE leases SET purpose = 'x' WHERE name = 'publish'"); err != nil {
		t.Fatalf("前提が崩れています（leases を書き換えられない）: %v", err)
	}
	if _, err := e.app.ExecContext(e.ctx, "UPDATE lease_events SET reason = 'x'"); err == nil {
		t.Error("lease_events を書き換えられた")
	}
	if _, err := e.app.ExecContext(e.ctx, "DELETE FROM lease_events"); err == nil {
		t.Error("lease_events を消せた")
	}
	d, err := e.s.GetLease(e.ctx, e.editor, e.demo.Slug, "publish")
	if err != nil || len(d.Events) != 1 || d.Events[0].Reason != "" {
		t.Fatalf("記録が変わった: %v %+v", err, d)
	}
}

// アーカイブしたプロジェクトは一覧から外れるので、公開の経路は無いものと同じ 404 になる。
// ロックの中の検査は、権限を確かめた後にアーカイブされた競合のためのもので、古い store.Project を渡した lockHeldLease で確かめる。
func TestLeaseArchivedProjectRejectsWrites(t *testing.T) {
	e := newAttachEnv(t)
	a := e.leaseEditors(t, 1)[0]
	got, err := e.acquireLease(a, "publish", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetProjectArchived(e.ctx, e.admin, e.demo.ID, true); err != nil {
		t.Fatal(err)
	}
	in := LeaseInput{Project: e.demo.Slug, Name: "publish", Reason: "r", Token: got.Token}
	if _, err := e.s.ExtendLease(e.ctx, a, in); memErrCode(err) != "not_found" {
		t.Errorf("延長: %v", err)
	}
	if err := e.s.ReleaseLease(e.ctx, a, in); memErrCode(err) != "not_found" {
		t.Errorf("返却: %v", err)
	}
	if _, err := e.s.BreakLease(e.ctx, a, in); memErrCode(err) != "not_found" {
		t.Errorf("外す: %v", err)
	}
	if _, err := e.acquireLease(a, "other", 0); memErrCode(err) != "not_found" {
		t.Errorf("取る: %v", err)
	}
	lock := func() error {
		return e.s.inTx(e.ctx, func(tx *sql.Tx) error {
			_, err := e.s.lockHeldLease(e.ctx, tx, e.demo, "publish")
			return err
		})
	}
	if err := lock(); memErrCode(err) != "project_archived" {
		t.Errorf("ロックの中の検査: %v", err)
	}
	if _, err := store.SetProjectArchived(e.ctx, e.admin, e.demo.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := lock(); err != nil { // 対照: 同じ呼び出しが戻した後は通る
		t.Fatalf("戻した後のロック: %v", err)
	}
	if err := e.s.ReleaseLease(e.ctx, a, in); err != nil {
		t.Fatalf("戻した後の返却: %v", err)
	}
}
