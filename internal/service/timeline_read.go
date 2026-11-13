package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// タイムラインを読む側（DESIGN.md §9-9「タイムラインの読み飛ばしを無くす仕組み」）。
// 未読（読む窓・自分のセッションを外す・1 回の上限）・新しいセッションと続きのセッションの違い・「いま」の表示・読んだ位置を進める規則は
// このファイルの 1 か所に置き、REST・MCP・CLI と next・set_status の結果はここを呼ぶ。1 行の文面も材料からここで組み立てる。
//
// 読んだ位置は読む側が明示して進める（SeeTimeline）。hook は注入の文を作り終えた後に位置を送るので、読むだけの呼び出し（ReadTimeline）は
// 位置を動かさない。進める値は読んだ結果の AdvanceTo で、返した行のうち最大の番号（残りが無ければ読む前に読んだ最新の通し番号）。
// 通し番号は確定の順に並ぶので（timeline_events.go）、最新の通し番号を先に読み、その番号までの行だけを読めば、位置より小さい番号が後から現れない。

const (
	// TimelineReadWindow は出来事と近況を読む窓。保持期間（message_retention_days）が短ければそちらで切る
	TimelineReadWindow = 7 * 24 * time.Hour
	// MaxTimelineReadRows は 1 回に返す出来事と近況の行の数。超えた分は件数だけを返して未読のまま残す
	MaxTimelineReadRows = 20
)

// 行の種類（TimelineRow.Kind）。
const (
	TimelineRowEvent  = "event"
	TimelineRowUpdate = "update"
)

// TimelineRow は未読の出来事か近況 1 行。Text は材料から組み立てた 1 行で、参加者が書いた文（タイトル・件名・本文・目的・理由）は含まない。
// それらは Quote に入れ、呼び出し側（hook・CLI・MCP）が引用の印を付けて出す。
type TimelineRow struct {
	Seq  int64
	Kind string
	// Type は出来事の種類（store.TimelineEvent*）。近況は "update"
	Type      string
	UserID    int64
	User      string
	Machine   string
	Session   string
	Issue     string
	At        time.Time
	SameUser  bool
	Detail    map[string]any
	Title     string
	Subject   string
	Body      string
	Purpose   string
	Reason    string
	Text      string
	Quote     []string
	updatePID int64
}

// TimelineNowSession は「いま」の表示のセッション 1 本（在席が 15 分以内のもの）。
type TimelineNowSession struct {
	User       string
	SameUser   bool
	Machine    string
	Agent      string
	Branch     string
	Session    string
	Issues     []string
	LastSeenAt time.Time
	// Update はそのセッションの最新の近況（読む窓の中に無ければ nil）
	Update *TimelineRow
	Text   string
}

// TimelineNowLease は「いま」の表示の取られている占有 1 つ。
type TimelineNowLease struct {
	Name      string
	Holder    string
	Machine   string
	Purpose   string
	ExpiresAt time.Time
	Text      string
}

// TimelineNow は「いま」の表示（動いているセッション・取られている占有・このセッションに効く有効な待機と依頼）。
type TimelineNow struct {
	Sessions []TimelineNowSession
	Leases   []TimelineNowLease
	Holds    []TimelinePostView
	Requests []TimelinePostView
}

// TimelineReadQuery は読み方。
type TimelineReadQuery struct {
	// Limit は返す行の数（0 か上限より大きければ MaxTimelineReadRows）
	Limit int
	// Now は続きのセッションでも「いま」の表示を付ける（新しいセッションには常に付ける）
	Now bool
}

// TimelineFeed は読む結果。並べる順は UnackedHolds・UnackedRequests・Now・Rows（古い順）・Undelivered。
type TimelineFeed struct {
	ProjectSlug string
	// Seq は読む前に読んだプロジェクトの最新の通し番号。ReadSeq・HasPosition は読む前のこのセッションの位置
	Seq         int64
	ReadSeq     int64
	HasPosition bool
	// NewSession は位置の無いセッション（「いま」と直近の行を返す）
	NewSession      bool
	UnackedHolds    []TimelinePostView
	UnackedRequests []TimelinePostView
	Now             *TimelineNow
	Rows            []TimelineRow
	// Remaining は続きのセッションで上限を超えて返さなかった未読の数。Older は新しいセッションで窓の中の直近より古い行の数（件数だけ）
	Remaining int
	Older     int
	// Undelivered は未達の投稿のうち未了解に入らないもの（知らせと、取り下げの知らせ）
	Undelivered []TimelinePostView
	// AdvanceTo は SeeTimeline に渡す値（返した行のうち最大の番号。残りが無ければ Seq）
	AdvanceTo int64
}

// Empty は知らせるものが何も無いか（「いま」の表示は数えない）。
func (f *TimelineFeed) Empty() bool {
	return len(f.UnackedHolds) == 0 && len(f.UnackedRequests) == 0 && len(f.Rows) == 0 && len(f.Undelivered) == 0 && f.Remaining == 0 && f.Older == 0
}

// timelineReadSince は読む窓の下限（7 日と保持期間の短いほう）。
func (s *Service) timelineReadSince(ctx context.Context) (time.Time, error) {
	since := s.Now().Add(-TimelineReadWindow)
	ret, err := s.messageSince(ctx)
	if err != nil {
		return time.Time{}, err
	}
	if ret.After(since) {
		since = ret
	}
	return since, nil
}

// ReadTimeline はこのセッションの未読と未了解を返す。位置は動かさない（進めるのは SeeTimeline）。役割は問わない（参加者なら読める）。
func (s *Service) ReadTimeline(ctx context.Context, a Actor, slug string, q TimelineReadQuery) (*TimelineFeed, error) {
	if err := checkMessageText("session", a.SessionID); err != nil {
		return nil, err
	}
	u, p, _, err := s.memoryAccess(ctx, a, strings.TrimSpace(slug))
	if err != nil {
		return nil, err
	}
	return s.readTimeline(ctx, a, u, p, q)
}

func (s *Service) readTimeline(ctx context.Context, a Actor, u store.User, p store.Project, q TimelineReadQuery) (*TimelineFeed, error) {
	limit := q.Limit
	if limit <= 0 || limit > MaxTimelineReadRows {
		limit = MaxTimelineReadRows
	}
	f := &TimelineFeed{ProjectSlug: p.Slug}
	var err error
	if f.ReadSeq, f.HasPosition, err = store.TimelineReadSeq(ctx, s.DB, p.ID, u.ID, a.SessionID); err != nil {
		return nil, err
	}
	f.NewSession = !f.HasPosition
	since, err := s.timelineReadSince(ctx)
	if err != nil {
		return nil, err
	}
	// 最新の通し番号を先に読み、その番号までの行だけを読む（読みの間に確定した行を、位置より小さい番号として後から出さない）
	if f.Seq, err = store.TimelinePostHeadSeq(ctx, s.DB, p.ID); err != nil {
		return nil, err
	}
	if timelineReadBetween != nil {
		timelineReadBetween()
	}
	posts, err := s.timelinePostViews(ctx, a, p, 0)
	if err != nil {
		return nil, err
	}
	filter := store.TimelineFeedFilter{ProjectID: p.ID, After: f.ReadSeq, UpTo: f.Seq, Since: since, ExcludeUser: u.ID, ExcludeSession: a.SessionID}
	if f.NewSession {
		filter.After = 0
	}
	rows, total, err := s.timelineRows(ctx, a, filter, limit, f.NewSession)
	if err != nil {
		return nil, err
	}
	f.Rows = rows
	if f.NewSession {
		f.Older = total - len(rows)
	} else {
		f.Remaining = total - len(rows)
	}
	// 読みの途中で head が進んでも、先に読んだ値までしか読んでいないので、進める位置もその値で止める
	switch {
	case f.Remaining > 0:
		f.AdvanceTo = f.Rows[len(f.Rows)-1].Seq
	default:
		f.AdvanceTo = f.Seq
	}
	if f.HasPosition && f.AdvanceTo < f.ReadSeq {
		f.AdvanceTo = f.ReadSeq
	}
	f.UnackedHolds, f.UnackedRequests, f.Undelivered = []TimelinePostView{}, []TimelinePostView{}, []TimelinePostView{}
	for _, v := range posts {
		switch {
		case v.Unacked() && v.Kind == store.TimelinePostHold:
			f.UnackedHolds = append(f.UnackedHolds, v)
		case v.Unacked():
			f.UnackedRequests = append(f.UnackedRequests, v)
		case v.Undelivered():
			f.Undelivered = append(f.Undelivered, v)
		}
	}
	sortTimelinePosts(f.UnackedHolds)
	sortTimelinePosts(f.UnackedRequests)
	sortTimelinePosts(f.Undelivered)
	if f.NewSession || q.Now {
		if f.Now, err = s.timelineNow(ctx, a, p, posts, since); err != nil {
			return nil, err
		}
	}
	return f, nil
}

// timelineReadBetween はテストだけが設定する。ReadTimeline が最新の通し番号を読んだ後、行を読む前に呼ぶ
// （その間に確定した書き込みを差し込み、位置より小さい番号が後から現れないことを確かめる）。
var timelineReadBetween func()

// timelineRows は条件に当たる出来事と近況を通し番号の順に limit 行まで返し、条件に当たる全体の件数も返す。
// latest なら直近の limit 行（新しいセッション）、そうでなければ古いほうから limit 行（続きのセッション）。
func (s *Service) timelineRows(ctx context.Context, a Actor, f store.TimelineFeedFilter, limit int, latest bool) ([]TimelineRow, int, error) {
	evs, err := store.TimelineEventsIn(ctx, s.DB, f, limit, latest)
	if err != nil {
		return nil, 0, err
	}
	ups, err := store.TimelineUpdatesIn(ctx, s.DB, f, limit, latest)
	if err != nil {
		return nil, 0, err
	}
	ne, err := store.CountTimelineEventsIn(ctx, s.DB, f)
	if err != nil {
		return nil, 0, err
	}
	nu, err := store.CountTimelineUpdatesIn(ctx, s.DB, f)
	if err != nil {
		return nil, 0, err
	}
	rows := make([]TimelineRow, 0, len(evs)+len(ups))
	for _, e := range evs {
		rows = append(rows, timelineEventRow(e, a))
	}
	for _, up := range ups {
		rows = append(rows, timelineUpdateRow(up, a))
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Seq < rows[j].Seq })
	if len(rows) > limit {
		if latest {
			rows = rows[len(rows)-limit:]
		} else {
			rows = rows[:limit]
		}
	}
	lang := a.Lang
	for i := range rows {
		rows[i].Text, rows[i].Quote = timelineRowText(lang, rows[i], s.Loc)
	}
	return rows, ne + nu, nil
}

func timelineDetailString(d map[string]any, k string) string {
	if v, ok := d[k].(string); ok {
		return v
	}
	return ""
}

func timelineEventRow(e store.TimelineEvent, a Actor) TimelineRow {
	r := TimelineRow{Seq: e.Seq, Kind: TimelineRowEvent, Type: e.Kind, UserID: e.ActorUserID, User: e.ActorLogin, Machine: e.ActorMachine, Session: e.ActorSession,
		Issue: e.IssueDisplay, At: e.CreatedAt, SameUser: e.ActorUserID == a.UserID, Detail: e.Detail}
	if r.Detail == nil {
		r.Detail = map[string]any{}
	}
	if r.Issue == "" {
		r.Issue = timelineDetailString(r.Detail, "issue")
	}
	r.Title = timelineDetailString(r.Detail, "title")
	r.Subject = timelineDetailString(r.Detail, "subject")
	r.Purpose = timelineDetailString(r.Detail, "purpose")
	r.Reason = timelineDetailString(r.Detail, "reason")
	return r
}

func timelineUpdateRow(u store.TimelineUpdate, a Actor) TimelineRow {
	return TimelineRow{Seq: u.Seq, Kind: TimelineRowUpdate, Type: store.TimelinePostUpdate, UserID: u.UserID, User: u.UserLogin, Machine: u.Machine,
		Session: u.Session, Issue: u.IssueDisplay, At: u.CreatedAt, SameUser: u.UserID == a.UserID, Detail: map[string]any{}, Body: u.Body, updatePID: u.PostID}
}

// timelineClaim は参加者が名乗った値（端末のラベル・ブランチ・エージェント）をバッククォートで囲む。バッククォートを含めば ? を出す。
func timelineClaim(v string) string {
	switch {
	case v == "":
		return "-"
	case strings.Contains(v, "`"):
		return "?"
	}
	return "`" + v + "`"
}

func timelineWho(lang i18n.Lang, same bool) string {
	if same {
		return i18n.T(lang, "service.timeline.same_user")
	}
	return i18n.T(lang, "service.timeline.other_user")
}

// timelineRowText は 1 行の文面と、引用で出す文（参加者が書いたもの）を組み立てる。
func timelineRowText(lang i18n.Lang, r TimelineRow, loc *time.Location) (string, []string) {
	if loc == nil {
		loc = time.Local
	}
	d := r.Detail
	head := i18n.T(lang, "service.timeline.row.head", "seq", r.Seq, "at", r.At.In(loc).Format("01-02 15:04"), "login", r.User,
		"who", timelineWho(lang, r.SameUser), "machine", timelineClaim(r.Machine))
	var msg string
	var quote []string
	flag := func(k string) bool { v, _ := d[k].(bool); return v }
	switch r.Type {
	case store.TimelineEventIssueCreate:
		msg = i18n.T(lang, "service.timeline.row.issue_create", "issue", r.Issue, "type", timelineDetailString(d, "type"))
		quote = []string{r.Title}
	case store.TimelineEventIssueStatus:
		from, to := timelineDetailString(d, "from"), timelineDetailString(d, "to")
		if flag("comment") {
			msg = i18n.T(lang, "service.timeline.row.issue_status_comment", "issue", r.Issue, "from", from, "to", to)
		} else {
			msg = i18n.T(lang, "service.timeline.row.issue_status", "issue", r.Issue, "from", from, "to", to)
		}
		quote = []string{r.Title}
	case store.TimelineEventIssueAssign:
		if flag("takeover") {
			msg = i18n.T(lang, "service.timeline.row.issue_assign_takeover", "issue", r.Issue, "to", timelineDetailString(d, "to"))
		} else {
			msg = i18n.T(lang, "service.timeline.row.issue_assign", "issue", r.Issue, "to", timelineDetailString(d, "to"))
		}
		quote = []string{r.Title}
	case store.TimelineEventIssueComment:
		if flag("message") {
			msg = i18n.T(lang, "service.timeline.row.issue_comment_message", "issue", r.Issue)
		} else {
			msg = i18n.T(lang, "service.timeline.row.issue_comment", "issue", r.Issue)
		}
		quote = []string{r.Title}
	case store.TimelineEventLeaseAcquire:
		msg = i18n.T(lang, "service.timeline.row.lease_acquire", "name", timelineDetailString(d, "name"))
		quote = []string{r.Purpose}
	case store.TimelineEventLeaseRelease:
		msg = i18n.T(lang, "service.timeline.row.lease_release", "name", timelineDetailString(d, "name"))
		quote = []string{r.Purpose}
	case store.TimelineEventLeaseBreak:
		msg = i18n.T(lang, "service.timeline.row.lease_break", "name", timelineDetailString(d, "name"))
		quote = []string{r.Purpose, r.Reason}
	case store.TimelineEventSessionStart:
		msg = i18n.T(lang, "service.timeline.row.session_start", "agent", timelineClaim(timelineDetailString(d, "agent")),
			"branch", timelineClaim(timelineDetailString(d, "branch")))
	case store.TimelineEventSessionEnd:
		msg = i18n.T(lang, "service.timeline.row.session_end")
	case store.TimelineEventCommit:
		branch := timelineClaim(timelineDetailString(d, "branch"))
		sha := timelineDetailString(d, "sha")
		n, omitted := d["omitted"].(float64)
		switch {
		case sha == "" && omitted:
			msg = i18n.T(lang, "service.timeline.row.commit_omitted", "branch", branch, "count", int(n))
		case flag("subject_withheld"):
			msg = i18n.T(lang, "service.timeline.row.commit_withheld", "branch", branch, "sha", sha[:min(len(sha), 8)])
		default:
			msg = i18n.T(lang, "service.timeline.row.commit", "branch", branch, "sha", sha[:min(len(sha), 8)])
			quote = []string{r.Subject}
		}
	case store.TimelinePostUpdate:
		if r.Issue != "" {
			msg = i18n.T(lang, "service.timeline.row.update_issue", "issue", r.Issue)
		} else {
			msg = i18n.T(lang, "service.timeline.row.update")
		}
		quote = []string{r.Body}
	default:
		msg = r.Type
	}
	out := quote[:0]
	for _, q := range quote {
		if strings.TrimSpace(q) != "" {
			out = append(out, q)
		}
	}
	return head + " " + msg, out
}

// timelineNow は「いま」の表示を組み立てる。動いているセッションからは自分のセッションを外す（summary と同じ）。
func (s *Service) timelineNow(ctx context.Context, a Actor, p store.Project, posts []TimelinePostView, since time.Time) (*TimelineNow, error) {
	now := &TimelineNow{Sessions: []TimelineNowSession{}, Leases: []TimelineNowLease{}, Holds: []TimelinePostView{}, Requests: []TimelinePostView{}}
	sessions, err := s.OtherPresence(ctx, a, p)
	if err != nil {
		return nil, err
	}
	latest := map[string]*TimelineRow{}
	if len(sessions) > 0 {
		keys := make([]store.TimelineSessionKey, 0, len(sessions))
		for _, st := range sessions {
			keys = append(keys, store.TimelineSessionKey{UserID: st.UserID, Session: st.SessionID})
		}
		// セッションごとの最新の近況を引く（件数の上限で古いセッションの近況が欠けないように）
		ups, err := store.LatestTimelineUpdates(ctx, s.DB, p.ID, keys, since)
		if err != nil {
			return nil, err
		}
		for _, up := range ups {
			r := timelineUpdateRow(up, a)
			r.Text, r.Quote = timelineRowText(a.Lang, r, s.Loc)
			latest[fmt.Sprintf("%d\x00%s", up.UserID, up.Session)] = &r
		}
	}
	loc := s.Loc
	if loc == nil {
		loc = time.Local
	}
	for _, st := range sessions {
		ns := TimelineNowSession{User: st.UserLogin, SameUser: st.UserID == a.UserID, Machine: st.MachineLabel, Agent: st.Agent, Branch: st.Branch,
			Session: st.SessionID, Issues: st.Issues, LastSeenAt: st.LastSeenAt, Update: latest[fmt.Sprintf("%d\x00%s", st.UserID, st.SessionID)]}
		if ns.Issues == nil {
			ns.Issues = []string{}
		}
		issues := "-"
		if len(ns.Issues) > 0 {
			issues = strings.Join(ns.Issues, ", ")
		}
		ns.Text = i18n.T(a.Lang, "service.timeline.now.session", "login", ns.User, "who", timelineWho(a.Lang, ns.SameUser), "machine", timelineClaim(ns.Machine),
			"branch", timelineClaim(ns.Branch), "issues", issues, "at", ns.LastSeenAt.In(loc).Format("15:04"))
		now.Sessions = append(now.Sessions, ns)
	}
	held, err := store.HeldLeases(ctx, s.DB, p.ID)
	if err != nil {
		return nil, err
	}
	at := s.Now()
	for _, l := range held {
		if !at.Before(l.ExpiresAt) {
			continue
		}
		nl := TimelineNowLease{Name: l.Name, Holder: l.HolderLogin, Machine: l.MachineLabel, Purpose: l.Purpose, ExpiresAt: l.ExpiresAt}
		nl.Text = i18n.T(a.Lang, "service.timeline.now.lease", "name", l.Name, "login", l.HolderLogin, "machine", timelineClaim(l.MachineLabel),
			"at", l.ExpiresAt.In(loc).Format("01-02 15:04"))
		now.Leases = append(now.Leases, nl)
	}
	for _, v := range posts {
		if !v.Active() || !v.AppliesToMe {
			continue
		}
		switch v.Kind {
		case store.TimelinePostHold:
			now.Holds = append(now.Holds, v)
		case store.TimelinePostRequest:
			now.Requests = append(now.Requests, v)
		}
	}
	sortTimelinePosts(now.Holds)
	sortTimelinePosts(now.Requests)
	return now, nil
}

// TimelineNowOf は「いま」の表示だけを返す（位置は読まない・動かさない）。
func (s *Service) TimelineNowOf(ctx context.Context, a Actor, slug string) (*TimelineNow, error) {
	_, p, _, err := s.memoryAccess(ctx, a, strings.TrimSpace(slug))
	if err != nil {
		return nil, err
	}
	since, err := s.timelineReadSince(ctx)
	if err != nil {
		return nil, err
	}
	posts, err := s.timelinePostViews(ctx, a, p, 0)
	if err != nil {
		return nil, err
	}
	return s.timelineNow(ctx, a, p, posts, since)
}

// TimelineSeen は読んだ位置を進めた結果。
type TimelineSeen struct {
	// Seq は進めた後の位置。Advanced は行を足したか（同じか小さい値なら足さない）
	Seq      int64
	Advanced bool
}

// SeeTimeline はこのセッションの読んだ位置を seq まで進める。位置は戻さず（今の位置以下なら何もしない）、プロジェクトの最新の通し番号より先には置かない。
// 0 以下は何もしない（位置の無いセッションに 0 の行を作ると、次の読みが新しいセッションから続きのセッションに変わるため）。
// セッションの無い要求は拒む（セッションの無い読み手どうしで 1 つの位置を共有してしまうため）。
// 役割は問わない（自分のセッションの位置だけを書く）。セッションは連絡の 1 行の欄と同じ検査を掛ける。
// 渡された値は最新の通し番号までしか確かめない。読んだ結果の advance_to より先の値を渡すと、その間の未読は読んだことになる（DESIGN.md「タイムラインの限界」）。
func (s *Service) SeeTimeline(ctx context.Context, a Actor, slug string, seq int64) (*TimelineSeen, error) {
	if a.SessionID == "" {
		return nil, errm(Invalid, "invalid_argument", i18n.M("service.err.timeline.seen_no_session"))
	}
	if err := checkMessageText("session", a.SessionID); err != nil {
		return nil, err
	}
	u, p, _, err := s.memoryAccess(ctx, a, strings.TrimSpace(slug))
	if err != nil {
		return nil, err
	}
	return s.seeTimeline(ctx, a, u, p, seq)
}

func (s *Service) seeTimeline(ctx context.Context, a Actor, u store.User, p store.Project, seq int64) (*TimelineSeen, error) {
	head, err := store.TimelinePostHeadSeq(ctx, s.DB, p.ID)
	if err != nil {
		return nil, err
	}
	seq = min(seq, head)
	cur, ok, err := store.TimelineReadSeq(ctx, s.DB, p.ID, u.ID, a.SessionID)
	if err != nil {
		return nil, err
	}
	if seq <= 0 || (ok && seq <= cur) {
		return &TimelineSeen{Seq: cur}, nil
	}
	if err := store.InsertTimelineRead(ctx, s.DB, p.ID, u.ID, a.SessionID, seq, s.Now()); err != nil {
		return nil, err
	}
	return &TimelineSeen{Seq: seq, Advanced: true}, nil
}

// ConsumeTimelineFeed は読んだ結果を文脈に入れた後に、位置を AdvanceTo まで進め、出した投稿の受け取りを記録する。
// 結果そのものが AI に届く経路（MCP の next と set_status）だけが使う。セッションの無い読み手は位置を進めない（共有になるため）。
func (s *Service) ConsumeTimelineFeed(ctx context.Context, a Actor, f *TimelineFeed) error {
	if a.SessionID != "" {
		if _, err := s.SeeTimeline(ctx, a, f.ProjectSlug, f.AdvanceTo); err != nil {
			return err
		}
	}
	var items []TimelinePostReceiptInput
	for _, list := range [][]TimelinePostView{f.UnackedHolds, f.UnackedRequests, f.Undelivered} {
		for _, v := range list {
			items = append(items, TimelinePostReceiptInput{ID: v.ID, Version: v.LastVersion})
		}
	}
	if len(items) == 0 {
		return nil
	}
	_, err := s.RecordTimelinePostReceipts(ctx, a, f.ProjectSlug, items[:min(len(items), maxUnreadIDs)])
	return err
}

// timelineCheckPosition は軽い確認の結果に、このセッションの読んだ位置と、位置が最新より小さいか（残りがあるか）を足す。
func (s *Service) timelineCheckPosition(ctx context.Context, a Actor, p store.Project, res *TimelinePostCheck) error {
	var err error
	res.ReadSeq, res.HasPosition, err = store.TimelineReadSeq(ctx, s.DB, p.ID, a.UserID, a.SessionID)
	if err != nil {
		return err
	}
	res.Behind = !res.HasPosition || res.ReadSeq < res.Seq
	return nil
}
