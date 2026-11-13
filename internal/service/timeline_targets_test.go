package service

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/store"
)

// 読む時点で解決する宛先（イシュー・ブランチ・端末・自分以外）のテスト。届く側と届かない側を同じテストに並べる。

func (e *attachEnv) presence(a Actor, label, branch string) {
	e.t.Helper()
	if err := e.s.ReportPresence(e.ctx, a, PresenceInput{Project: e.demo.Slug, Agent: "claude-code", MachineLabel: label, Branch: branch, Directory: "repo"}); err != nil {
		e.t.Fatalf("在席 %s: %v", a.SessionID, err)
	}
}

func (e *attachEnv) start(a Actor, it *Issue) {
	e.t.Helper()
	if _, err := e.s.SetStatus(e.ctx, a, e.demo, it.Row.ID, "In Progress", "", ""); err != nil {
		e.t.Fatalf("着手 %s: %v", it.Item.ID, err)
	}
}

// holdsOf はセッションに効く有効な待機の ID（軽い確認と同じ判定）。
func (e *attachEnv) holdsOf(a Actor) []int64 {
	e.t.Helper()
	c, err := e.s.CheckTimelinePosts(e.ctx, a, e.demo.Slug)
	if err != nil {
		e.t.Fatal(err)
	}
	return c.Holds
}

// deliveryRows は到達の状況の行を「利用者/セッション/出どころ」で並べる。
func (e *attachEnv) deliveryRows(a Actor, id int64) []string {
	e.t.Helper()
	st, err := e.s.TimelinePostDeliveryStatus(e.ctx, a, e.demo.Slug, id)
	if err != nil {
		e.t.Fatal(err)
	}
	var got []string
	for _, r := range st.Rows {
		got = append(got, r.User+"/"+r.Session+"/"+r.ResolvedBy)
	}
	return got
}

// 受け入れ条件: イシューを宛先にした待機が、そのイシューに着手中のセッションに届く。別のイシューに着手中のセッションには届かない。
// 置いた後でそのイシューに着手したセッションにも届き、到達の状況に着手の記録から解決したセッションが並ぶ。
func TestTimelineTargetIssue(t *testing.T) {
	e := newAttachEnv(t)
	e.joinDemo(e.outside, "editor")
	ed := inSession(e.editor, "sess-ed")
	outA, edB := inSession(e.outside, "sess-a"), inSession(e.editor, "sess-b")
	e.start(edB, e.issue2) // 別のイシューに着手中（対照）
	hold := e.mustPost(ed, TimelinePostInput{Kind: store.TimelinePostHold, To: store.TimelineToIssue, ToIssue: e.issue.Item.ID, Body: "push を待ってください"})
	if hold.ToKind != store.TimelineToIssue || hold.ToIssueDisplay != e.issue.Item.ID {
		t.Fatalf("宛先が記録されていない: %+v", hold.TimelinePost)
	}
	if got := e.holdsOf(outA); len(got) != 0 {
		t.Fatalf("まだ着手していないセッションに効いた: %v", got)
	}
	// 置いた後で着手したセッションにも届く
	e.start(outA, e.issue)
	if got := e.holdsOf(outA); !slices.Equal(got, []int64{hold.ID}) {
		t.Errorf("着手中のセッションに効かない: %v", got)
	}
	if p := e.pending(outA, 0); !slices.Equal(timelinePostIDs(p.Unacked), []int64{hold.ID}) {
		t.Errorf("着手中のセッションの未了解: %v", timelinePostIDs(p.Unacked))
	}
	if got := e.holdsOf(edB); len(got) != 0 {
		t.Errorf("別のイシューに着手中のセッションに効いた: %v", got)
	}
	// 同じ利用者でも、そのイシューに着手していない別のセッションには効かない（対照）
	if got := e.holdsOf(inSession(e.outside, "sess-c")); len(got) != 0 {
		t.Errorf("着手していない同じ利用者の別のセッションに効いた: %v", got)
	}
	if got := e.holdsOf(ed); len(got) != 0 {
		t.Errorf("出したセッションに効いた: %v", got)
	}
	if _, err := e.s.AckTimelinePost(e.ctx, edB, TimelinePostAckInput{Project: e.demo.Slug, ID: hold.ID, Version: 1}); memErrCode(err) != "timeline_not_recipient" {
		t.Errorf("宛先に当たらないセッションの了解: %v", err)
	}
	if _, err := e.s.AckTimelinePost(e.ctx, outA, TimelinePostAckInput{Project: e.demo.Slug, ID: hold.ID, Version: 1}); err != nil {
		t.Errorf("着手中のセッションの了解: %v", err)
	}
	// 到達の状況: 在席を送っていない着手のセッションも着手の記録から並ぶ。別のイシューのセッションは在席があっても並ばない
	e.presence(edB, "box-b", "main")
	if got := e.deliveryRows(ed, hold.ID); !slices.Equal(got, []string{"out/sess-a/" + TimelineResolvedByRecord}) {
		t.Errorf("到達の状況の行: %v", got)
	}
	// 宛先のイシューを終えたら誰にも当たらない
	if _, err := e.s.SetStatus(e.ctx, outA, e.demo, e.issue.Row.ID, "Done", "", ""); err != nil {
		t.Fatal(err)
	}
	if got := e.holdsOf(outA); len(got) != 0 {
		t.Errorf("イシューを終えたセッションに効き続けた: %v", got)
	}
	// 読める範囲は参加者全員（宛先が読む時点まで決まらない）
	if _, err := e.s.GetTimelinePost(e.ctx, e.viewer, e.demo.Slug, hold.ID); err != nil {
		t.Errorf("参加者が読めない: %v", err)
	}
	if _, err := e.s.GetTimelinePost(e.ctx, e.outside, e.other.Slug, hold.ID); memErrCode(err) != "not_found" {
		t.Errorf("ほかのプロジェクトから読めた: %v", err)
	}
}

// 受け入れ条件: ブランチ・端末・自分以外も、届く側と届かない側を並べる。自分以外は出したセッションに出ない。
// ブランチと端末は在席の名乗った値なので、置いた後で名乗ったセッションには届き、名乗り替えたセッションからは外れる。
func TestTimelineTargetBranchMachineOthers(t *testing.T) {
	e := newAttachEnv(t)
	e.joinDemo(e.outside, "editor")
	ed := inSession(e.editor, "sess-ed")
	v1, v2, o := inSession(e.viewer, "sess-v1"), inSession(e.viewer, "sess-v2"), inSession(e.outside, "sess-o")
	e.presence(ed, "box-a", "feat-x") // 出したセッションは同じブランチと端末でも外れる
	e.presence(v1, "box-a", "feat-x")
	e.presence(v2, "box-b", "main")
	e.presence(o, "box-b", "feat-x")
	branch := e.mustPost(ed, TimelinePostInput{Kind: store.TimelinePostHold, To: store.TimelineToBranch, ToBranch: "feat-x", Body: "feat-x を待って"})
	machine := e.mustPost(ed, TimelinePostInput{Kind: store.TimelinePostRequest, To: store.TimelineToMachine, ToMachine: "box-b", Body: "box-b で検査を"})
	others := e.mustPost(ed, TimelinePostInput{Kind: store.TimelinePostHold, To: store.TimelineToOthers, Body: "全員待って"})
	applies := func(a Actor, id int64) bool {
		t.Helper()
		v, err := e.s.timelinePostOne(e.ctx, a, e.demo, id)
		if err != nil {
			t.Fatal(err)
		}
		return v.AppliesToMe
	}
	cases := []struct {
		name string
		a    Actor
		id   int64
		want bool
	}{
		{"ブランチ: 同じブランチ", v1, branch.ID, true},
		{"ブランチ: 別の利用者の同じブランチ", o, branch.ID, true},
		{"ブランチ: 別のブランチ（対照）", v2, branch.ID, false},
		{"ブランチ: 出したセッション", ed, branch.ID, false},
		{"端末: 同じ端末", v2, machine.ID, true},
		{"端末: 別の利用者の同じ端末", o, machine.ID, true},
		{"端末: 別の端末（対照）", v1, machine.ID, false},
		{"自分以外: ほかのセッション", v1, others.ID, true},
		{"自分以外: 同じ利用者の別のセッション", inSession(e.editor, "sess-ed2"), others.ID, true},
		{"自分以外: 出したセッション（対照）", ed, others.ID, false},
	}
	for _, c := range cases {
		if got := applies(c.a, c.id); got != c.want {
			t.Errorf("%s: 効くか = %v（期待 %v）", c.name, got, c.want)
		}
	}
	if got := e.holdsOf(ed); slices.Contains(got, others.ID) || slices.Contains(got, branch.ID) {
		t.Errorf("出したセッションの待機に出た: %v", got)
	}
	// 到達の状況は在席から解決し、出どころは presence
	if got := e.deliveryRows(ed, branch.ID); !slices.Equal(got, []string{"out/sess-o/presence", "vi/sess-v1/presence"}) {
		t.Errorf("ブランチの到達の状況: %v", got)
	}
	if got := e.deliveryRows(ed, machine.ID); !slices.Equal(got, []string{"out/sess-o/presence", "vi/sess-v2/presence"}) {
		t.Errorf("端末の到達の状況: %v", got)
	}
	if got := e.deliveryRows(ed, others.ID); !slices.Equal(got, []string{"out/sess-o/presence", "vi/sess-v1/presence", "vi/sess-v2/presence"}) {
		t.Errorf("自分以外の到達の状況: %v", got)
	}
	// 置いた後でそのブランチを名乗ったセッションに届き、名乗り替えたセッションからは外れる
	v3 := inSession(e.viewer, "sess-v3")
	e.presence(v3, "box-c", "feat-x")
	e.presence(v1, "box-a", "main")
	if !applies(v3, branch.ID) {
		t.Error("置いた後でブランチを名乗ったセッションに届かない")
	}
	if applies(v1, branch.ID) {
		t.Error("ブランチを名乗り替えたセッションに効き続けた")
	}
	// 在席の無いセッションにはブランチと端末の宛先は当たらない（自分以外は当たる）
	none := inSession(e.viewer, "sess-none")
	if applies(none, branch.ID) || applies(none, machine.ID) || !applies(none, others.ID) {
		t.Error("在席の無いセッションの判定")
	}
}

// 宛先の値の検査。拒まれる形と、同じ形で通る側の対照。
func TestTimelineTargetValidation(t *testing.T) {
	e := newAttachEnv(t)
	ed := inSession(e.editor, "sess-ed")
	if _, err := e.s.SetStatus(e.ctx, ed, e.demo, e.issue2.Row.ID, "Done", "", ""); err != nil {
		t.Fatal(err)
	}
	bad := []struct {
		name string
		in   TimelinePostInput
		code string
	}{
		{"知らない種類", TimelinePostInput{Kind: store.TimelinePostHold, To: "bogus"}, "invalid_argument"},
		{"知らせはイシュー宛てにできない", TimelinePostInput{Kind: store.TimelinePostInfo, To: store.TimelineToIssue, ToIssue: e.issue.Item.ID}, "invalid_argument"},
		{"近況は自分以外宛てにできない", TimelinePostInput{Kind: store.TimelinePostUpdate, To: store.TimelineToOthers}, "invalid_argument"},
		{"イシュー宛てにイシューが無い", TimelinePostInput{Kind: store.TimelinePostHold, To: store.TimelineToIssue}, "invalid_argument"},
		{"イシュー宛てに利用者も付けた", TimelinePostInput{Kind: store.TimelinePostHold, To: store.TimelineToIssue, ToIssue: e.issue.Item.ID, User: "vi"}, "invalid_argument"},
		{"イシュー宛てにブランチも付けた", TimelinePostInput{Kind: store.TimelinePostHold, To: store.TimelineToIssue, ToIssue: e.issue.Item.ID, ToBranch: "main"}, "invalid_argument"},
		{"全員宛てにブランチを付けた", TimelinePostInput{Kind: store.TimelinePostHold, To: store.MessageToProject, ToBranch: "main"}, "invalid_argument"},
		{"自分以外に端末を付けた", TimelinePostInput{Kind: store.TimelinePostHold, To: store.TimelineToOthers, ToMachine: "box"}, "invalid_argument"},
		{"ブランチに空白", TimelinePostInput{Kind: store.TimelinePostHold, To: store.TimelineToBranch, ToBranch: "feat x"}, "invalid_argument"},
		{"端末のラベルが長すぎる", TimelinePostInput{Kind: store.TimelinePostHold, To: store.TimelineToMachine, ToMachine: strings.Repeat("a", 65)}, "invalid_argument"},
		{"ブランチが長すぎる", TimelinePostInput{Kind: store.TimelinePostHold, To: store.TimelineToBranch, ToBranch: strings.Repeat("b", 65)}, "invalid_argument"},
		{"無いイシュー", TimelinePostInput{Kind: store.TimelinePostHold, To: store.TimelineToIssue, ToIssue: "DEMO-9999"}, "not_found"},
		{"ほかのプロジェクトのイシュー", TimelinePostInput{Kind: store.TimelinePostHold, To: store.TimelineToIssue, ToIssue: e.otherIssue.Item.ID}, "not_found"},
		{"クローズ済みのイシュー", TimelinePostInput{Kind: store.TimelinePostHold, To: store.TimelineToIssue, ToIssue: e.issue2.Item.ID}, "timeline_issue_closed"},
	}
	for _, c := range bad {
		c.in.Body = "本文"
		if _, err := e.post(ed, c.in); memErrCode(err) != c.code {
			t.Errorf("%s: %v（期待 %s）", c.name, err, c.code)
		}
	}
	// 対照: 同じ形の正しい値は通る
	for _, in := range []TimelinePostInput{
		{Kind: store.TimelinePostRequest, To: store.TimelineToIssue, ToIssue: e.issue.Item.ID},
		{Kind: store.TimelinePostHold, To: store.TimelineToBranch, ToBranch: " feat/x-1 "},
		{Kind: store.TimelinePostHold, To: store.TimelineToMachine, ToMachine: "box-a"},
		{Kind: store.TimelinePostRequest, To: store.TimelineToOthers},
		{Kind: store.TimelinePostHold, To: store.TimelineToMachine, ToMachine: strings.Repeat("a", 64)},
		{Kind: store.TimelinePostHold, To: store.TimelineToBranch, ToBranch: strings.Repeat("b", 64)},
	} {
		in.Body = "本文"
		v, err := e.post(ed, in)
		if err != nil {
			t.Errorf("%+v: %v", in, err)
			continue
		}
		if in.ToBranch == " feat/x-1 " && v.ToLabel != "feat/x-1" {
			t.Errorf("ブランチの値が整えられていない: %q", v.ToLabel)
		}
	}
	// 既にある宛先の検査は変わらない（古いクライアントの誤りの文面）
	if _, err := e.post(ed, TimelinePostInput{Kind: store.TimelinePostHold, To: store.MessageToProject, User: "vi", Body: "x"}); err == nil ||
		err.Error() != "プロジェクト宛ての連絡には利用者もセッションも指定できません" {
		t.Errorf("全員宛てに利用者を付けた誤り: %v", err)
	}
}

// 引き取りと戻し: 別のセッションが着手し直すと新しいセッションにだけ当たり、元のセッションは外れる。In Progress から Todo に戻すと誰にも当たらない。
func TestTimelineTargetIssueTakeoverAndReopen(t *testing.T) {
	e := newAttachEnv(t)
	e.joinDemo(e.outside, "editor")
	ed := inSession(e.editor, "sess-ed")
	a1, a2 := inSession(e.outside, "sess-a1"), inSession(e.outside, "sess-a2")
	e.start(a1, e.issue)
	hold := e.mustPost(ed, TimelinePostInput{Kind: store.TimelinePostHold, To: store.TimelineToIssue, ToIssue: e.issue.Item.ID, Body: "待って"})
	if got := e.holdsOf(a1); !slices.Equal(got, []int64{hold.ID}) {
		t.Fatalf("着手したセッションに効かない（前提が崩れています）: %v", got)
	}
	if _, err := e.s.SetStatus(e.ctx, a1, e.demo, e.issue.Row.ID, "Todo", "", ""); err != nil {
		t.Fatal(err)
	}
	if got := append(e.holdsOf(a1), e.holdsOf(a2)...); len(got) != 0 {
		t.Errorf("Todo に戻したイシューの宛先が誰かに当たった: %v", got)
	}
	if got := e.deliveryRows(ed, hold.ID); len(got) != 0 {
		t.Errorf("Todo に戻した後の到達の状況: %v", got)
	}
	e.start(a2, e.issue) // 別のセッションが引き取る
	if got := e.holdsOf(a2); !slices.Equal(got, []int64{hold.ID}) {
		t.Errorf("引き取ったセッションに効かない: %v", got)
	}
	if got := e.holdsOf(a1); len(got) != 0 {
		t.Errorf("元のセッションに効き続けた: %v", got)
	}
	if got := e.deliveryRows(ed, hold.ID); !slices.Equal(got, []string{"out/sess-a2/" + TimelineResolvedByRecord}) {
		t.Errorf("引き取った後の到達の状況: %v", got)
	}
}

// MCP の着手を会話の ID で当てる。接続の ID で記録した着手が会話に結ばれていれば、会話の ID を名乗るセッションに効く。
// 到達の状況では 1 つの着手から 1 行だけ並べ、受け取り・了解の記録がある側（接続の ID）を採る。
func TestTimelineTargetIssueLinkedMCPSession(t *testing.T) {
	e := newAttachEnv(t)
	e.joinDemo(e.outside, "editor")
	ed := inSession(e.editor, "sess-ed")
	conn := inSession(e.outside, MCPSessionPrefix+"c1")
	conn.Via = "mcp"
	e.start(conn, e.issue)
	// 着手のイベントを会話の ID（conv-1）に結ぶ（hook が届けたスナップショットで結ぶ処理と同じ行を作る）
	snapID, _, err := store.InsertUsageSnapshot(e.ctx, e.admin, store.UsageSnapshot{ProjectID: e.demo.ID, UserID: e.outside.UserID, Client: "claude-code",
		SessionID: "conv-1", Trigger: "issue_op", Via: "mcp", At: time.Now(), DedupeKey: "link-0704", ByModel: []byte("{}"), IO: []byte("{}"),
		Human: []byte("{}"), Segments: []byte("[]"), Branches: []byte("[]")})
	if err != nil {
		t.Fatal(err)
	}
	var evID int64
	if err := e.admin.QueryRowContext(e.ctx, "SELECT MAX(id) FROM issue_events WHERE issue_id = ? AND kind = 'status'", e.issue.Row.ID).Scan(&evID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.admin.ExecContext(e.ctx, "INSERT INTO issue_event_sessions (event_id, user_id, session_id, snapshot_id, linked_at) VALUES (?, ?, 'conv-1', ?, ?)",
		evID, e.outside.UserID, snapID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	hold := e.mustPost(ed, TimelinePostInput{Kind: store.TimelinePostHold, To: store.TimelineToIssue, ToIssue: e.issue.Item.ID, Body: "待って"})
	if got := e.holdsOf(inSession(e.outside, "conv-1")); !slices.Equal(got, []int64{hold.ID}) {
		t.Errorf("会話の ID を名乗るセッションに効かない: %v", got)
	}
	if got := e.holdsOf(inSession(e.outside, "conv-2")); len(got) != 0 {
		t.Errorf("結ばれていない会話に効いた（対照）: %v", got)
	}
	// どちらの記録も無ければ会話の ID の 1 行
	if got := e.deliveryRows(ed, hold.ID); !slices.Equal(got, []string{"out/conv-1/" + TimelineResolvedByRecord}) {
		t.Errorf("到達の状況（記録なし）: %v", got)
	}
	// 接続の ID で了解したら、その行だけが並ぶ（会話の ID の 0/0 の行を足さない）
	if _, err := e.s.AckTimelinePost(e.ctx, conn, TimelinePostAckInput{Project: e.demo.Slug, ID: hold.ID, Version: 1}); err != nil {
		t.Fatal(err)
	}
	if got := e.deliveryRows(ed, hold.ID); !slices.Equal(got, []string{"out/" + MCPSessionPrefix + "c1/" + TimelineResolvedByRecord}) {
		t.Errorf("到達の状況（了解の後）: %v", got)
	}
}
