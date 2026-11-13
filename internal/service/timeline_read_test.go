package service

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// タイムラインを読む側（timeline_read.go）のテスト。土台は attachEnv。

func (e *attachEnv) readTL(a Actor, q TimelineReadQuery) *TimelineFeed {
	e.t.Helper()
	f, err := e.s.ReadTimeline(e.ctx, a, e.demo.Slug, q)
	if err != nil {
		e.t.Fatalf("未読: %v", err)
	}
	return f
}

func (e *attachEnv) seeTL(a Actor, seq int64) *TimelineSeen {
	e.t.Helper()
	r, err := e.s.SeeTimeline(e.ctx, a, e.demo.Slug, seq)
	if err != nil {
		e.t.Fatalf("読んだ位置: %v", err)
	}
	return r
}

func (e *attachEnv) checkTL(a Actor) *TimelinePostCheck {
	e.t.Helper()
	c, err := e.s.CheckTimelinePosts(e.ctx, a, e.demo.Slug)
	if err != nil {
		e.t.Fatalf("軽い確認: %v", err)
	}
	return c
}

func rowSeqs(rows []TimelineRow) []int64 {
	out := []int64{}
	for _, r := range rows {
		out = append(out, r.Seq)
	}
	return out
}

// 21 件以上の未読で 20 行までが返り、読んだ位置は返した分までしか進まず、次の読みで続きが返る。軽い確認が「残りがある」と答え、
// 読み切った後は「残りが無い」と答える（対照）。
func TestTimelineUnreadLimitAndContinue(t *testing.T) {
	e := newAttachEnv(t)
	reader := inSession(e.editor, "sess-r")
	writer := e.editor2("sess-w")
	it, err := e.s.Create(e.ctx, writer, e.demo, CreateInput{Title: "未読の確認"}, i18n.JA)
	if err != nil {
		t.Fatal(err)
	}
	e.seeTL(reader, e.headSeq()) // 続きのセッションにする
	if c := e.checkTL(reader); c.Behind || !c.HasPosition {
		t.Fatalf("前提が崩れています: 読み切った位置で残りがある %+v", c)
	}
	for i := 0; i < 25; i++ {
		if _, err := e.s.AddComment(e.ctx, writer, e.demo, it.Row.ID, CommentInput{Text: fmt.Sprintf("c%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	f := e.readTL(reader, TimelineReadQuery{})
	if f.NewSession || f.Now != nil || len(f.Rows) != MaxTimelineReadRows || f.Remaining != 5 {
		t.Fatalf("1 回目: new=%v now=%v rows=%d remaining=%d", f.NewSession, f.Now != nil, len(f.Rows), f.Remaining)
	}
	if f.AdvanceTo != f.Rows[len(f.Rows)-1].Seq || f.AdvanceTo >= f.Seq {
		t.Fatalf("進める位置 %d（返した最後 %d・最新 %d）", f.AdvanceTo, f.Rows[len(f.Rows)-1].Seq, f.Seq)
	}
	if c := e.checkTL(reader); !c.Behind {
		t.Errorf("読む前の軽い確認: %+v", c)
	}
	// 読むだけでは位置は動かない
	if again := e.readTL(reader, TimelineReadQuery{}); !slicesEqual(rowSeqs(again.Rows), rowSeqs(f.Rows)) {
		t.Fatalf("読むだけで位置が動いた: %v / %v", rowSeqs(again.Rows), rowSeqs(f.Rows))
	}
	if r := e.seeTL(reader, f.AdvanceTo); r.Seq != f.AdvanceTo || !r.Advanced {
		t.Fatalf("位置: %+v", r)
	}
	if c := e.checkTL(reader); !c.Behind || c.ReadSeq != f.AdvanceTo {
		t.Errorf("上限で残した後の軽い確認が「残りがある」と答えない: %+v", c)
	}
	g := e.readTL(reader, TimelineReadQuery{})
	if len(g.Rows) != 5 || g.Remaining != 0 || g.Rows[0].Seq != f.AdvanceTo+1 || g.AdvanceTo != g.Seq {
		t.Fatalf("2 回目: rows=%v remaining=%d advance=%d seq=%d", rowSeqs(g.Rows), g.Remaining, g.AdvanceTo, g.Seq)
	}
	e.seeTL(reader, g.AdvanceTo)
	if c := e.checkTL(reader); c.Behind {
		t.Errorf("読み切った後の軽い確認: %+v", c)
	}
	// 位置は戻らず、最新より先にも置かない
	if r := e.seeTL(reader, 1); r.Advanced || r.Seq != g.AdvanceTo {
		t.Errorf("戻す: %+v", r)
	}
	if r := e.seeTL(reader, g.Seq+100); r.Seq != g.Seq {
		t.Errorf("最新より先: %+v", r)
	}
	if r := e.seeTL(reader, -1); r.Advanced || r.Seq != g.AdvanceTo {
		t.Errorf("負の位置: %+v", r)
	}
}

// 0 以下の seen は行を作らない（位置の無いセッションのまま、次の読みも新しいセッションになる）。対照として 1 以上は位置を作る。
func TestTimelineSeenZeroKeepsNewSession(t *testing.T) {
	e := newAttachEnv(t)
	a := inSession(e.editor, "sess-z")
	for _, seq := range []int64{0, -3} {
		if r := e.seeTL(a, seq); r.Advanced {
			t.Errorf("seen %d が位置を作った: %+v", seq, r)
		}
	}
	if f := e.readTL(a, TimelineReadQuery{}); !f.NewSession {
		t.Fatalf("seen 0 の後の読みが新しいセッションでない")
	}
	if e.headSeq() < 1 {
		t.Fatalf("前提が崩れています: 通し番号が 0")
	}
	if r := e.seeTL(a, 1); !r.Advanced {
		t.Fatalf("対照: seen 1 が位置を作らない: %+v", r)
	}
	if f := e.readTL(a, TimelineReadQuery{}); f.NewSession {
		t.Errorf("対照: seen 1 の後も新しいセッション")
	}
}

// セッションの無い読み手は位置を持たない。seen は拒み、ConsumeTimelineFeed も位置を進めない。対照としてセッションのある読み手は進む。
func TestTimelineSeenNeedsSession(t *testing.T) {
	e := newAttachEnv(t)
	bare := e.editor // セッション ID なし
	if _, err := e.s.SeeTimeline(e.ctx, bare, e.demo.Slug, e.headSeq()); memErrCode(err) != "invalid_argument" {
		t.Errorf("セッションの無い seen: %v", err)
	}
	f := e.readTL(bare, TimelineReadQuery{})
	if err := e.s.ConsumeTimelineFeed(e.ctx, bare, f); err != nil {
		t.Fatal(err)
	}
	if n := e.count("SELECT COUNT(*) FROM timeline_reads"); n != 0 {
		t.Errorf("セッションの無い読み手の位置が %d 行できた", n)
	}
	withSession := inSession(e.editor, "sess-c")
	g := e.readTL(withSession, TimelineReadQuery{})
	if err := e.s.ConsumeTimelineFeed(e.ctx, withSession, g); err != nil {
		t.Fatal(err)
	}
	if n := e.count("SELECT COUNT(*) FROM timeline_reads"); n != 1 {
		t.Errorf("対照: セッションのある読み手の位置 %d 行", n)
	}
}

// 「いま」の最新の近況はセッションごとに引く。ほかのセッションが近況を前の実装の上限（新しい順に 500 件）を越えて書いても、
// 別のセッションの最新が欠けない。窓より古い近況は出さない（対照: 実際の窓の中の近況は出る）。
func TestLatestTimelineUpdatesPerSession(t *testing.T) {
	e := newAttachEnv(t)
	a := inSession(e.editor, "sess-a")
	b := e.editor2("sess-b")
	e.mustPost(a, TimelinePostInput{Kind: store.TimelinePostUpdate, Body: "a の古い近況"})
	e.mustPost(a, TimelinePostInput{Kind: store.TimelinePostUpdate, Body: "a の最新"})
	const many = 505
	for i := 0; i < many; i++ {
		e.mustPost(b, TimelinePostInput{Kind: store.TimelinePostUpdate, Body: fmt.Sprintf("b の近況 %d", i)})
	}
	keys := []store.TimelineSessionKey{{UserID: a.UserID, Session: "sess-a"}, {UserID: b.UserID, Session: "sess-b"}, {UserID: b.UserID, Session: "none"}}
	ups, err := store.LatestTimelineUpdates(e.ctx, e.s.DB, e.demo.ID, keys, e.s.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, u := range ups {
		got[u.Session] = u.Body
	}
	if len(ups) != 2 || got["sess-a"] != "a の最新" || got["sess-b"] != fmt.Sprintf("b の近況 %d", many-1) {
		t.Errorf("窓の中のセッションごとの最新: %v", got)
	}
	if ups, err := store.LatestTimelineUpdates(e.ctx, e.s.DB, e.demo.ID, keys, e.s.Now().Add(time.Hour)); err != nil || len(ups) != 0 {
		t.Errorf("窓の外: %v %v", ups, err)
	}
	// 「いま」を通しても、a の最新が b の大量の近況に押し出されない
	for _, x := range []Actor{a, b} {
		if err := e.s.ReportPresence(e.ctx, x, PresenceInput{Project: e.demo.Slug, Agent: "claude-code", MachineLabel: "box"}); err != nil {
			t.Fatal(err)
		}
	}
	now, err := e.s.TimelineNowOf(e.ctx, inSession(e.viewer, "sess-v"), e.demo.Slug)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range now.Sessions {
		if st.Session == "sess-a" && (st.Update == nil || st.Update.Body != "a の最新") {
			t.Errorf("「いま」の a の最新の近況: %+v", st.Update)
		}
	}
	if len(now.Sessions) != 2 {
		t.Errorf("前提が崩れています: 「いま」のセッション %d 本", len(now.Sessions))
	}
}

func rowTypes(rows []TimelineRow) []string {
	out := []string{}
	for _, r := range rows {
		out = append(out, r.Type+":"+r.User+":"+r.Session)
	}
	return out
}

func slicesEqual(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// 自分のセッションが起こした出来事と近況は自分の未読に出ない。同じ利用者の別のセッションと、ほかの利用者には出る。
func TestTimelineUnreadExcludesOwnSession(t *testing.T) {
	e := newAttachEnv(t)
	me := inSession(e.editor, "sess-a")
	mine2 := inSession(e.editor, "sess-a2")
	other := e.editor2("sess-b")
	for _, a := range []Actor{me, mine2, other} {
		e.seeTL(a, e.headSeq())
	}
	if _, err := e.s.Create(e.ctx, me, e.demo, CreateInput{Title: "自分の起票"}, i18n.JA); err != nil {
		t.Fatal(err)
	}
	e.mustPost(me, TimelinePostInput{Kind: store.TimelinePostUpdate, Body: "実装中です"})
	if f := e.readTL(me, TimelineReadQuery{}); len(f.Rows) != 0 || f.AdvanceTo != f.Seq {
		t.Errorf("自分のセッションの未読: %v（advance %d・seq %d）", rowSeqs(f.Rows), f.AdvanceTo, f.Seq)
	}
	for name, a := range map[string]Actor{"同じ利用者の別のセッション": mine2, "ほかの利用者": other} {
		f := e.readTL(a, TimelineReadQuery{})
		if len(f.Rows) != 2 || f.Rows[0].Type != store.TimelineEventIssueCreate || f.Rows[1].Kind != TimelineRowUpdate {
			t.Errorf("%s の未読: %+v", name, f.Rows)
			continue
		}
		if f.Rows[0].SameUser != (a.UserID == me.UserID) {
			t.Errorf("%s の同じ利用者の印: %v", name, f.Rows[0].SameUser)
		}
		if f.Rows[1].Body != "実装中です" || len(f.Rows[1].Quote) != 1 || strings.Contains(f.Rows[1].Text, "実装中です") {
			t.Errorf("%s の近況の行: %+v", name, f.Rows[1])
		}
	}
}

// 7 日より古い出来事は未読に出ない（対照: 1 日前の出来事は出る）。有効な待機は最初の投稿が 7 日を過ぎていても出る。
func TestTimelineUnreadWindow(t *testing.T) {
	e := newAttachEnv(t)
	reader := e.editor2("sess-r")
	writer := inSession(e.editor, "sess-w")
	e.seeTL(reader, e.headSeq())
	base := e.s.Now
	defer func() { e.s.Now = base }()
	e.s.Now = func() time.Time { return base().Add(-8 * 24 * time.Hour) }
	if _, err := e.s.Create(e.ctx, writer, e.demo, CreateInput{Title: "8 日前"}, i18n.JA); err != nil {
		t.Fatal(err)
	}
	hold := e.mustPost(writer, TimelinePostInput{Kind: store.TimelinePostHold, To: store.MessageToProject, Body: "待ってください", TTLSeconds: int64(MaxTimelinePostTTL / time.Second)})
	e.s.Now = func() time.Time { return base().Add(-24 * time.Hour) }
	if _, err := e.s.Create(e.ctx, writer, e.demo, CreateInput{Title: "1 日前"}, i18n.JA); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.ReviseTimelinePost(e.ctx, writer, TimelinePostReviseInput{Project: e.demo.Slug, ID: hold.ID, TTLSeconds: int64(MaxTimelinePostTTL / time.Second)}); err != nil {
		t.Fatal(err)
	}
	e.s.Now = base
	f := e.readTL(reader, TimelineReadQuery{})
	if len(f.Rows) != 1 || f.Rows[0].Title != "1 日前" {
		t.Errorf("窓の中の出来事: %+v", f.Rows)
	}
	if len(f.UnackedHolds) != 1 || f.UnackedHolds[0].ID != hold.ID {
		t.Errorf("7 日を過ぎた有効な待機: %v", timelinePostIDs(f.UnackedHolds))
	}
	// 新しいセッションでも窓の外は件数にも入らない（demo の雛形の起票は今の時刻なので窓の中）
	n := e.readTL(inSession(reader, "sess-new"), TimelineReadQuery{})
	for _, r := range append(n.Rows, f.Rows...) {
		if r.Title == "8 日前" {
			t.Errorf("窓の外の出来事が出た: %+v", r)
		}
	}
	if !n.NewSession || n.Older != 0 || len(n.Rows) < 2 || n.Rows[len(n.Rows)-1].Title != "1 日前" && n.Rows[len(n.Rows)-2].Title != "1 日前" {
		t.Errorf("新しいセッションの窓: rows=%d older=%d %v", len(n.Rows), n.Older, rowTypes(n.Rows))
	}
}

// 新しいセッションは「いま」と直近 20 行を受け取り、それより古いものは件数だけ。位置は最新に置ける。
// 続きのセッションは前の位置からの未読で、「いま」は求めたときだけ付く。
func TestTimelineNewAndContinuingSession(t *testing.T) {
	e := newAttachEnv(t)
	writer := e.editor2("sess-w")
	before := e.readTL(inSession(e.editor, "sess-count"), TimelineReadQuery{})
	pre := len(before.Rows) + before.Older // demo の雛形の分
	if err := e.s.ReportPresence(e.ctx, writer, PresenceInput{Project: e.demo.Slug, Agent: "claude-code", MachineLabel: "box-w", Branch: "feat-w", Start: true}); err != nil {
		t.Fatal(err)
	}
	it, err := e.s.Create(e.ctx, writer, e.demo, CreateInput{Title: "続き", Status: "In Progress"}, i18n.JA)
	if err != nil {
		t.Fatal(err)
	}
	e.mustPost(writer, TimelinePostInput{Kind: store.TimelinePostUpdate, Body: "古い近況"})
	for i := 0; i < 22; i++ {
		if _, err := e.s.AddComment(e.ctx, writer, e.demo, it.Row.ID, CommentInput{Text: fmt.Sprintf("c%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	e.mustPost(writer, TimelinePostInput{Kind: store.TimelinePostUpdate, Body: "新しい近況", Issue: it.Item.ID})
	if _, err := e.s.AcquireLease(e.ctx, writer, LeaseInput{Project: e.demo.Slug, Name: "deploy", Purpose: "反映"}); err != nil {
		t.Fatal(err)
	}
	reader := inSession(e.editor, "sess-new")
	f := e.readTL(reader, TimelineReadQuery{})
	total := pre + 1 + 1 + 1 + 22 + 1 + 1 // 雛形・開始・起票・近況・コメント・近況・占有
	if !f.NewSession || f.HasPosition || len(f.Rows) != MaxTimelineReadRows || f.Older != total-MaxTimelineReadRows || f.Remaining != 0 {
		t.Fatalf("新しいセッション: new=%v rows=%d older=%d remaining=%d %v", f.NewSession, len(f.Rows), f.Older, f.Remaining, rowTypes(f.Rows))
	}
	if f.Rows[len(f.Rows)-1].Seq != f.Seq || f.AdvanceTo != f.Seq {
		t.Errorf("直近の行が最新で終わらない: last=%d seq=%d advance=%d", f.Rows[len(f.Rows)-1].Seq, f.Seq, f.AdvanceTo)
	}
	if f.Now == nil || len(f.Now.Sessions) != 1 || len(f.Now.Leases) != 1 {
		t.Fatalf("「いま」: %+v", f.Now)
	}
	st := f.Now.Sessions[0]
	if st.Machine != "box-w" || st.Branch != "feat-w" || len(st.Issues) != 1 || st.Issues[0] != it.Item.ID || st.Update == nil || st.Update.Body != "新しい近況" {
		t.Errorf("「いま」のセッション: %+v", st)
	}
	if !strings.Contains(st.Text, "`box-w`") || !strings.Contains(f.Now.Leases[0].Text, "deploy") {
		t.Errorf("「いま」の文面: %q / %q", st.Text, f.Now.Leases[0].Text)
	}
	e.seeTL(reader, f.AdvanceTo)
	if _, err := e.s.AddComment(e.ctx, writer, e.demo, it.Row.ID, CommentInput{Text: "後から"}); err != nil {
		t.Fatal(err)
	}
	g := e.readTL(reader, TimelineReadQuery{})
	if g.NewSession || g.Now != nil || len(g.Rows) != 1 || g.Older != 0 || g.Rows[0].Type != store.TimelineEventIssueComment {
		t.Errorf("続きのセッション: new=%v now=%v rows=%+v", g.NewSession, g.Now != nil, g.Rows)
	}
	if h := e.readTL(reader, TimelineReadQuery{Now: true}); h.Now == nil {
		t.Errorf("求めた「いま」が付かない")
	}
}

// 1 行の文面は材料から言語ごとに組み立て、参加者が書いた文（タイトル・件名・目的・理由）は文面に入れず引用に回す。
func TestTimelineRowText(t *testing.T) {
	at := time.Date(2026, 11, 11, 3, 4, 0, 0, time.UTC)
	cases := []struct {
		row   TimelineRow
		ja    string
		en    string
		quote []string
	}{
		{TimelineRow{Type: store.TimelineEventIssueStatus, Issue: "ABC-0001", Title: "題", Detail: map[string]any{"from": "Todo", "to": "Done", "comment": true}},
			"ABC-0001 をコメントを添えて Todo から Done にしました", "moved ABC-0001 from Todo to Done, with a comment", []string{"題"}},
		{TimelineRow{Type: store.TimelineEventIssueAssign, Issue: "ABC-0001", Title: "題", Detail: map[string]any{"to": "ed", "takeover": true}},
			"ABC-0001 を引き取り、担当を ed にしました", "took over ABC-0001", []string{"題"}},
		{TimelineRow{Type: store.TimelineEventLeaseBreak, Purpose: "反映", Reason: "止まった", Detail: map[string]any{"name": "deploy"}},
			"占有 deploy を強制解除しました", "broke the lease deploy", []string{"反映", "止まった"}},
		{TimelineRow{Type: store.TimelineEventCommit, Subject: "直す", Detail: map[string]any{"branch": "main", "sha": "abcdef0123456789"}},
			"`main` にコミット abcdef01 を積みました", "committed abcdef01 on `main`", []string{"直す"}},
		{TimelineRow{Type: store.TimelineEventCommit, Detail: map[string]any{"branch": "main", "sha": "abcdef0", "subject_withheld": true}},
			"件名は資格情報の形か", "the subject is withheld", nil},
		{TimelineRow{Type: store.TimelineEventCommit, Detail: map[string]any{"branch": "main", "omitted": float64(3)}},
			"ほかに 3 件のコミット", "committed 3 more", nil},
		{TimelineRow{Type: store.TimelineEventSessionStart, Machine: "m`x", Detail: map[string]any{"agent": "codex", "branch": "feat"}},
			"セッションを始めました（`codex`・ブランチ `feat`）", "started a session", nil},
	}
	for _, c := range cases {
		c.row.Seq, c.row.User, c.row.At = 7, "ed", at
		ja, q := timelineRowText(i18n.JA, c.row, time.UTC)
		en, _ := timelineRowText(i18n.EN, c.row, time.UTC)
		if !strings.Contains(ja, c.ja) || !strings.Contains(en, c.en) || !strings.HasPrefix(ja, "#7 11-11 03:04 ed（ほかの利用者）") {
			t.Errorf("%s: %q / %q", c.row.Type, ja, en)
		}
		if len(q) != len(c.quote) || (len(q) > 0 && strings.Join(q, "|") != strings.Join(c.quote, "|")) {
			t.Errorf("%s の引用: %q", c.row.Type, q)
		}
		for _, w := range c.quote {
			if strings.Contains(ja, w) {
				t.Errorf("%s: 参加者の文 %q が文面に入った: %q", c.row.Type, w, ja)
			}
		}
		if strings.Contains(ja, "m`x") {
			t.Errorf("バッククォートを含む端末のラベルが文面に入った: %q", ja)
		}
	}
}

// 読んだ位置を進めた後に、それより小さい番号の行が現れない（並行する書き込みと読み。MySQL 版）。
// 読み手は ReadTimeline と SeeTimeline だけで読み進め、最後に全部を読み直して読み飛ばした番号が無いことを確かめる。
func TestTimelineUnreadNoSkipConcurrentMySQL(t *testing.T) {
	requireMySQL(t)
	e := newAttachEnv(t)
	writer := inSession(e.editor, "sess-w")
	writer2 := e.editor2("sess-w2")
	reader := Actor{UserID: writer2.UserID, Via: "cli", Lang: i18n.JA, SessionID: "sess-r"}
	var issues []*Issue
	for i := 0; i < 4; i++ {
		it, err := e.s.Create(e.ctx, writer, e.demo, CreateInput{Title: fmt.Sprintf("n%d", i)}, i18n.JA)
		if err != nil {
			t.Fatal(err)
		}
		issues = append(issues, it)
	}
	e.seeTL(reader, e.headSeq())
	base := e.headSeq()
	timelineBeforeHead = func(int64) { time.Sleep(2 * time.Millisecond) }
	timelineReadBetween = func() { time.Sleep(time.Millisecond) }
	defer func() { timelineBeforeHead, timelineReadBetween = nil, nil }()
	const writes = 60
	var wg sync.WaitGroup
	errs := make(chan error, writes)
	for w := 0; w < writes; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			a := writer
			if w%2 == 1 {
				a = writer2
			}
			_, err := e.s.AddComment(e.ctx, a, e.demo, issues[w%len(issues)].Row.ID, CommentInput{Text: fmt.Sprintf("c%d", w)})
			errs <- err
		}(w)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	seen := map[int64]bool{}
	pos := base
	read := func() {
		f := e.readTL(reader, TimelineReadQuery{})
		for _, r := range f.Rows {
			if r.Seq <= pos {
				t.Errorf("位置 %d より小さい番号 %d が現れた", pos, r.Seq)
			}
			seen[r.Seq] = true
		}
		pos = e.seeTL(reader, f.AdvanceTo).Seq
	}
	for running := true; running; {
		select {
		case <-done:
			running = false
		default:
			read()
		}
	}
	for i := 0; i < 5; i++ {
		read()
	}
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
			t.Errorf("通し番号 %d を読み飛ばした", ev.Seq)
		}
	}
}

// 同じプロジェクトで書き込みが重なったときの head のロックの待ちを測る（MySQL 版。数は DESIGN.md に残す）。
// 1 本で順に書いたときと、8 本で並べて書いたときの 1 件の所要時間の中央値と 95 パーセンタイルを出す。待ちで失敗しないことも確かめる。
func TestTimelineHeadLockWaitMySQL(t *testing.T) {
	requireMySQL(t)
	e := newAttachEnv(t)
	writer := inSession(e.editor, "sess-w")
	var issues []*Issue
	for i := 0; i < 8; i++ {
		it, err := e.s.Create(e.ctx, writer, e.demo, CreateInput{Title: fmt.Sprintf("w%d", i)}, i18n.JA)
		if err != nil {
			t.Fatal(err)
		}
		issues = append(issues, it)
	}
	measure := func(workers, per int) []time.Duration {
		var mu sync.Mutex
		var out []time.Duration
		var wg sync.WaitGroup
		errs := make(chan error, workers*per)
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				for i := 0; i < per; i++ {
					start := time.Now()
					_, err := e.s.AddComment(e.ctx, writer, e.demo, issues[w%len(issues)].Row.ID, CommentInput{Text: "計測"})
					d := time.Since(start)
					errs <- err
					mu.Lock()
					out = append(out, d)
					mu.Unlock()
				}
			}(w)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
		return out
	}
	pct := func(ds []time.Duration, p float64) time.Duration { return ds[int(float64(len(ds)-1)*p)] }
	serial := measure(1, 80)
	parallel := measure(8, 20)
	t.Logf("head のロックの待ち: 1 本 p50=%v p95=%v / 8 本 p50=%v p95=%v（コメント 1 件の所要時間・各 %d 件）",
		pct(serial, 0.5), pct(serial, 0.95), pct(parallel, 0.5), pct(parallel, 0.95), len(serial))
}
