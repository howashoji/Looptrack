package service

import (
	"context"
	"database/sql"
	"errors"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// タイムライン（DESIGN.md §9-9「タイムライン」）。AI のセッションがほかのセッションへ動作や待機を指示し、受け取る側が読み飛ばしなく確かめる。
// 規則（種類・宛先・権限・本文の検査・期限・解除の条件・保持期間・未達と未了解・同じ利用者か）はこのファイルの 1 か所に置き、
// REST・MCP・CLI はここの関数を呼ぶ。宛先・本文・端末のラベルの検査は連絡（messages.go）の関数をそのまま使う。
//
// 状態は出来事から引く。最新の版は最後の投稿か改訂で、取り下げの出来事があれば取り下げ済み。期限切れと解除（添えたイシューが
// Done か Canceled・添えた占有が返るか切れる）は読む時点で判定し、行は書き換えない。
//
// 出来事の番号（version）は書き込みの中で 1 から振り、取り下げも 1 つ進める。受け取りの記録はこの番号で持つので、
// 「最新の版を受け取ったか」と「取り下げを知らせたか」を同じ形で決められる。
//
// 通し番号はプロジェクトごとに 1 から振り、自動の出来事（timeline_events.go）と 1 本にする。出来事を書く取引の最後に timeline_heads の行を
// 1 度だけロックして進め、同じ取引で出来事の行を書く。受け取りと了解では進めない。
//
// 読める者は宛先（project 宛ては参加者全員）と送り手。「自分に効く」のは宛先に当たり、かつ自分のセッションが出したものでないもの
// （同じ利用者の別のセッションには効く。連絡の未読と同じ）。同じ利用者が出したかは、サーバが認証した利用者で決め、名乗ったセッションでは決めない。
//
// 依頼と待機は、イシュー・ブランチ・端末・自分以外にも宛てられる。宛先のセッションは書く時点では決めず、読む時点で解決する
// （timelinePostHits）。イシューは着手の記録（store.InProgressStarters。在席の着手中のイシューと同じ判定）、ブランチと端末は在席の名乗った値、
// 自分以外は出したセッションのほかの全員。だから置いた後でその作業に入ったセッションにも届き、名乗り替えたセッションからは外れる。
// 読める範囲は project 宛てと同じ参加者全員（誰に当たるかは読む時点まで決まらないため）。

// 期限の既定と上限。待機は期限を必ず持ち、省くと既定になる。知らせと依頼は省けば期限を持たない。
const (
	DefaultTimelinePostHoldTTL = 2 * time.Hour
	MaxTimelinePostTTL         = 7 * 24 * time.Hour
)

// maxTimelinePostReason は取り下げの理由の上限（文字数。表の列と同じ）。
const maxTimelinePostReason = 200

// maxTimelineUpdateBody は近況（update）の本文の上限（文字数）。
const maxTimelineUpdateBody = 500

// maxTimelinePostEvents は出来事を一度に返す件数の上限。defaultTimelinePostEvents は既定。
const (
	defaultTimelinePostEvents = 200
	maxTimelinePostEvents     = 1000
)

// TimelinePostIssueLead はイシューに残すコメントの先頭の語。
const TimelinePostIssueLead = "タイムライン:"

// 書き込みの状態。
const (
	TimelinePostActive    = "active"
	TimelinePostWithdrawn = "withdrawn"
	TimelinePostExpired   = "expired"
	TimelinePostReleased  = "released"
)

// TimelinePostInput は投稿の入力。
type TimelinePostInput struct {
	Project string
	// Kind は種類（update / info / request / hold）。update（近況）は全員宛てだけで、期限・解除の条件・改訂・取り下げ・了解を持たない
	Kind string
	// To・User・Session は宛先（連絡と同じ）。To は依頼と待機に限って issue・branch・machine・others も取る
	To      string
	User    string
	Session string
	// ToIssue・ToBranch・ToMachine は読む時点で解決する宛先の値（To が issue・branch・machine のときだけ）
	ToIssue   string
	ToBranch  string
	ToMachine string
	Body      string
	// TTLSeconds は期限の長さ（秒）。0 なら、待機は既定の 2 時間、知らせと依頼は期限なし
	TTLSeconds int64
	// Issue は添えるイシューの ID（任意）。添えるとそのイシューに「タイムライン:」で始まるコメントも残す
	Issue string
	// ReleaseIssue は解除の条件のイシュー（待機だけ。そのイシューが Done か Canceled になると効力を失う）
	ReleaseIssue string
	// ReleaseLease は解除の条件の占有の名前（待機だけ。いま取られているもの。返るか切れると効力を失う）
	ReleaseLease string
	// MachineLabel は出した端末のラベル
	MachineLabel string
}

// TimelinePostReviseInput は改訂の入力。本文と期限の少なくとも 1 つを指定する（指定しないものは前の版のまま）。
type TimelinePostReviseInput struct {
	Project string
	ID      int64
	Body    string
	// TTLSeconds は今からの期限の長さ（秒）。0 なら前の版の期限のまま
	TTLSeconds   int64
	MachineLabel string
}

// TimelinePostWithdrawInput は取り下げの入力。本人以外が取り下げるときは理由が要る。
type TimelinePostWithdrawInput struct {
	Project      string
	ID           int64
	Reason       string
	MachineLabel string
}

// TimelinePostAckInput は了解の入力。Version は了解する版（最新の版でなければ拒む）。
type TimelinePostAckInput struct {
	Project string
	ID      int64
	Version int64
}

// TimelinePostReceiptInput は受け取りの記録 1 件（書き込みと、本文を文脈に入れた出来事の番号）。
type TimelinePostReceiptInput struct {
	ID      int64
	Version int64
}

// TimelinePostListQuery は一覧の読み方。
type TimelinePostListQuery struct {
	// Sent は、自分が出した有効な書き込みを読む（既定は自分に効く有効な書き込み）
	Sent bool
}

// TimelinePostView は書き込み 1 件の、読む時点の状態。
type TimelinePostView struct {
	store.TimelinePost
	ProjectSlug string
	// Version は最新の版（最後の投稿か改訂の出来事の番号）。Body・ExpiresAt・Author・Machine・Session はその版のもの
	Version   int64
	Body      string
	ExpiresAt time.Time
	Author    string
	// LastVersion は最後の出来事の番号（取り下げを含む）。LastSeq はその通し番号、UpdatedAt はその時刻
	LastVersion int64
	LastSeq     int64
	UpdatedAt   time.Time
	// State は TimelinePostActive / TimelinePostWithdrawn / TimelinePostExpired / TimelinePostReleased
	State string
	// WithdrawReason・WithdrawnBy は取り下げの理由と行った者（取り下げ済みのときだけ）
	WithdrawReason string
	WithdrawnBy    string
	// SameUser は、読み手と同じ利用者が出したか（サーバが認証した利用者で決める）
	SameUser bool
	// AppliesToMe は、読み手のセッションに効くか（宛先に当たり、自分のセッションが出したものでない）
	AppliesToMe bool
	// Received・Acked は読み手のセッションが受け取った・了解した出来事の番号の最大（無ければ 0）
	Received int64
	Acked    int64
}

// Active は効いているか。
func (v TimelinePostView) Active() bool { return v.State == TimelinePostActive }

// Undelivered は読み手のセッションに未達か。有効なら最新の版を受け取っていないもの。
// 取り下げ済みなら、前の版を受け取っていて取り下げをまだ受け取っていないもの（取り下げを 1 度だけ知らせる）。
func (v TimelinePostView) Undelivered() bool {
	if !v.AppliesToMe || v.Kind == store.TimelinePostUpdate { // 近況は受け取りの記録ではなく読んだ位置で届ける
		return false
	}
	if v.Active() {
		return v.Received < v.Version
	}
	return v.State == TimelinePostWithdrawn && v.Received > 0 && v.Received < v.LastVersion
}

// Unacked は読み手のセッションが最新の版をまだ了解していない、有効な依頼か待機か。
func (v TimelinePostView) Unacked() bool {
	return v.AppliesToMe && v.Active() && v.Kind != store.TimelinePostInfo && v.Kind != store.TimelinePostUpdate && v.Acked < v.Version
}

// TimelinePostDetail は書き込み 1 件と出来事（番号の順）。前の版の本文と書き手は出来事に残る。
type TimelinePostDetail struct {
	TimelinePostView
	Events []store.TimelinePostEvent
}

// TimelinePostListResult は一覧。
type TimelinePostListResult struct {
	TimelinePosts []TimelinePostView
	// Seq はプロジェクトの最後の通し番号
	Seq         int64
	CanPost     bool
	User        string
	ProjectSlug string
}

// TimelinePostPending は読み手のセッションの未達と未了解。並びは待機・依頼・知らせの順で、同じ種類は通し番号の順。
// Limit で切っても受け取りは記録しないので、切った分は次にもう一度出る。
type TimelinePostPending struct {
	Undelivered      []TimelinePostView
	Unacked          []TimelinePostView
	UndeliveredTotal int
	UnackedTotal     int
	Seq              int64
	ProjectSlug      string
}

// TimelinePostCheck は軽い確認の応答（プロジェクトの最後の通し番号と、読み手のセッションに効く有効な待機の ID の昇順の並び）。
// ReadSeq・HasPosition はこのセッションの読んだ位置（timeline_read.go）。Behind は位置が無いか最新より小さい（未読か、上限で残した分がある）。
type TimelinePostCheck struct {
	Seq         int64
	Holds       []int64
	ReadSeq     int64
	HasPosition bool
	Behind      bool
}

// 到達の状況の行が宛先に当たった出どころ（TimelinePostDeliveryRow.ResolvedBy）。
const (
	// TimelineResolvedByRecord はサーバの着手の記録で当たった行（イシューの宛先）
	TimelineResolvedByRecord = "record"
	// TimelineResolvedByPresence は在席（セッションが名乗った値）で当たった行
	TimelineResolvedByPresence = "presence"
)

// TimelinePostDeliveryRow は到達の状況の 1 行（利用者とセッションごと）。
type TimelinePostDeliveryRow struct {
	UserID       int64
	User         string
	MachineLabel string
	Session      string
	// Received・Acked は受け取った・了解した出来事の番号の最大（無ければ 0）
	Received int64
	Acked    int64
	// LastSeenAt は最後に在席を送った時刻（動いているセッションとして在席に無ければゼロ値）
	LastSeenAt time.Time
	// ResolvedBy は宛先に当たった出どころ（TimelineResolvedByRecord・TimelineResolvedByPresence）。受け取りか了解の記録だけで並んだ行は空
	ResolvedBy string
}

// TimelinePostDelivery は到達の状況。
type TimelinePostDelivery struct {
	TimelinePost TimelinePostView
	Rows         []TimelinePostDeliveryRow
}

// TimelinePostEventsResult は通し番号より後の出来事。
type TimelinePostEventsResult struct {
	Events []store.TimelinePostEvent
	// Seq はプロジェクトの最後の通し番号
	Seq int64
}

func errTimelinePostNotFound(id int64) *Error {
	return errm(NotFound, "not_found", i18n.M("service.err.timeline.not_found", "id", id))
}

// timelinePostKindOrder は並べる順（待機・依頼・知らせ）。
func timelinePostKindOrder(kind string) int {
	switch kind {
	case store.TimelinePostHold:
		return 0
	case store.TimelinePostRequest:
		return 1
	}
	return 2
}

func sortTimelinePosts(vs []TimelinePostView) {
	sort.SliceStable(vs, func(i, j int) bool {
		if a, b := timelinePostKindOrder(vs[i].Kind), timelinePostKindOrder(vs[j].Kind); a != b {
			return a < b
		}
		return vs[i].LastSeq < vs[j].LastSeq
	})
}

// timelineAddressee は宛先に当たるかを決める側の 1 本（読み手か、到達の状況の行）。Issues・Branches・Machines は読む時点で解決する宛先の材料で、
// Issues は着手の記録でそのセッションが着手した In Progress のイシュー（表示の ID）、Branches と Machines は在席で名乗った値。
// SessionID が空の読み手は持ち主として、その利用者のセッション全部の値を持つ（連絡の未読と同じ）。
type timelineAddressee struct {
	UserID    int64
	SessionID string
	Issues    map[string]bool
	Branches  map[string]bool
	Machines  map[string]bool
}

// timelinePostHits は宛先に当たるか。規則はここだけに置き、読み手の「自分に効く」と到達の状況の行の両方がこれを呼ぶ。
// session が空なら、そのセッションの無い読み手は持ち主として別のセッション宛ても見る（連絡の未読と同じ）。
func timelinePostHits(b store.TimelinePost, w timelineAddressee) bool {
	switch b.ToKind {
	case store.MessageToProject, store.TimelineToOthers: // 自分以外は、出したセッションを外す（timelinePostApplies）のが project と同じ
		return true
	case store.MessageToUser:
		return b.ToUserID == w.UserID
	case store.MessageToSession:
		return b.ToUserID == w.UserID && (w.SessionID == "" || b.ToSession == w.SessionID)
	case store.TimelineToIssue:
		return b.ToIssueDisplay != "" && w.Issues[b.ToIssueDisplay]
	case store.TimelineToBranch:
		return b.ToLabel != "" && w.Branches[b.ToLabel]
	case store.TimelineToMachine:
		return b.ToLabel != "" && w.Machines[b.ToLabel]
	}
	return false
}

// timelinePostOwn は w が書き込みを出したセッションか（出したセッション自身には効かせない）。
func timelinePostOwn(b store.TimelinePost, userID int64, session string) bool {
	return b.SenderUserID == userID && b.SenderSession == session
}

// timelinePostApplies は読み手のセッションに効くか（宛先に当たり、自分のセッションが出したものでない）。
func timelinePostApplies(b store.TimelinePost, w timelineAddressee) bool {
	return !timelinePostOwn(b, w.UserID, w.SessionID) && timelinePostHits(b, w)
}

// timelineResolved は読む時点で解決する宛先か。
func timelineResolved(kind string) bool { return slices.Contains(store.TimelineResolvedTargets, kind) }

// startersOf は着手の記録のうち、利用者のセッションが着手したイシュー（表示の ID）を返す。session が空なら利用者の全部。
// MCP で着手したイシューは会話に結んだ値（LinkedSessionID）でも当たる（在席の着手中のイシューと同じ判定・presenceIssues）。
func startersOf(starters []store.Starter, userID int64, session string) map[string]bool {
	out := map[string]bool{}
	for _, x := range starters {
		if x.UserID == userID && (session == "" || x.SessionID == session || x.LinkedSessionID == session) {
			out[x.DisplayID] = true
		}
	}
	return out
}

// presenceWho は在席の 1 行を、宛先に当たるかを決める側にする。
func presenceWho(r store.Presence, starters []store.Starter) timelineAddressee {
	return timelineAddressee{UserID: r.UserID, SessionID: r.SessionID, Issues: startersOf(starters, r.UserID, r.SessionID),
		Branches: labelSet(r.Branch), Machines: labelSet(r.MachineLabel)}
}

func labelSet(vs ...string) map[string]bool {
	out := map[string]bool{}
	for _, v := range vs {
		if v != "" {
			out[v] = true
		}
	}
	return out
}

// timelineReaderOf は読み手のセッションを、宛先に当たるかを決める側にする。need が偽なら（読む時点で解決する宛先が 1 件も無ければ）
// 着手の記録と在席を引かない。在席は最後に名乗った値を使う（動いているかは問わない。読んでいる本人は動いている）。
// セッションの無い読み手は持ち主として、その利用者の動いているセッションの値を全部持つ。
func (s *Service) timelineReaderOf(ctx context.Context, a Actor, p store.Project, need bool) (timelineAddressee, error) {
	w := timelineAddressee{UserID: a.UserID, SessionID: a.SessionID}
	if !need {
		return w, nil
	}
	starters, err := store.InProgressStarters(ctx, s.DB, p.ID)
	if err != nil {
		return w, err
	}
	w.Issues = startersOf(starters, a.UserID, a.SessionID)
	w.Branches, w.Machines = map[string]bool{}, map[string]bool{}
	if a.SessionID != "" {
		r, ok, err := store.PresenceOf(ctx, s.DB, p.ID, a.UserID, a.SessionID)
		if err != nil {
			return w, err
		}
		if ok {
			w.Branches, w.Machines = labelSet(r.Branch), labelSet(r.MachineLabel)
		}
		return w, nil
	}
	rows, err := store.ActivePresence(ctx, s.DB, p.ID, s.Now().Add(-PresenceActive))
	if err != nil {
		return w, err
	}
	for _, r := range rows {
		if r.UserID == a.UserID {
			maps.Copy(w.Branches, labelSet(r.Branch))
			maps.Copy(w.Machines, labelSet(r.MachineLabel))
		}
	}
	return w, nil
}

// timelinePostReleased は解除の条件を満たしたか（添えたイシューが Done か Canceled・添えた占有が返ったか切れたか取り直されたか）。
func timelinePostReleased(b store.TimelinePost, now time.Time) bool {
	if b.ReleaseIssueID != 0 && slices.Contains(domain.ClosedStatuses, b.ReleaseIssueStatus) {
		return true
	}
	if b.ReleaseLease != "" {
		return b.LeaseHolderUserID == 0 || !b.LeaseAcquiredAt.Equal(b.ReleaseLeaseAcquiredAt) || !now.Before(b.LeaseExpiresAt)
	}
	return false
}

// timelinePostView は書き込みと出来事（番号の順）から、読む時点の状態を組み立てる。
func timelinePostView(b store.TimelinePost, events []store.TimelinePostEvent, slug string, w timelineAddressee, marks store.TimelinePostMarks, now time.Time) TimelinePostView {
	v := TimelinePostView{TimelinePost: b, ProjectSlug: slug, SameUser: b.SenderUserID == w.UserID, AppliesToMe: timelinePostApplies(b, w),
		Received: marks.Received, Acked: marks.Acked}
	withdrawn := false
	for _, e := range events {
		v.LastVersion, v.LastSeq, v.UpdatedAt = e.Version, e.Seq, e.CreatedAt
		if e.Action == store.TimelinePostActionWithdraw {
			withdrawn, v.WithdrawReason, v.WithdrawnBy = true, e.Reason, e.ActorLogin
			continue
		}
		v.Version, v.Body, v.ExpiresAt, v.Author = e.Version, e.Body, e.ExpiresAt, e.ActorLogin
	}
	switch {
	case withdrawn:
		v.State = TimelinePostWithdrawn
	case timelinePostReleased(b, now):
		v.State = TimelinePostReleased
	case !v.ExpiresAt.IsZero() && !now.Before(v.ExpiresAt):
		v.State = TimelinePostExpired
	default:
		v.State = TimelinePostActive
	}
	return v
}

// timelinePostViews は読み手が読める書き込みの、読む時点の状態を id の順に返す。id が 0 でなければその 1 件だけを見る。
func (s *Service) timelinePostViews(ctx context.Context, a Actor, p store.Project, id int64) ([]TimelinePostView, error) {
	since, err := s.messageSince(ctx)
	if err != nil {
		return nil, err
	}
	now := s.Now()
	bs, err := store.ReadableTimelinePosts(ctx, s.DB, p.ID, a.UserID, since, now, id)
	if err != nil || len(bs) == 0 {
		return nil, err
	}
	evs, err := store.ReadableTimelinePostEvents(ctx, s.DB, p.ID, a.UserID, since, now, id)
	if err != nil {
		return nil, err
	}
	byID := map[int64][]store.TimelinePostEvent{}
	for _, e := range evs {
		byID[e.TimelinePostID] = append(byID[e.TimelinePostID], e)
	}
	marks, err := store.TimelinePostMarksOf(ctx, s.DB, p.ID, a.UserID, a.SessionID)
	if err != nil {
		return nil, err
	}
	w, err := s.timelineReaderOf(ctx, a, p, slices.ContainsFunc(bs, func(b store.TimelinePost) bool { return timelineResolved(b.ToKind) }))
	if err != nil {
		return nil, err
	}
	out := make([]TimelinePostView, 0, len(bs))
	for _, b := range bs {
		if len(byID[b.ID]) == 0 { // 出来事の無い行（投稿の取引の外で見えることは無いが、念のため出さない）
			continue
		}
		if id == 0 && b.Kind == store.TimelinePostUpdate { // 近況は効き続ける書き込みではないので、一覧・未達・軽い確認・件数に入れない
			continue
		}
		out = append(out, timelinePostView(b, byID[b.ID], p.Slug, w, marks[b.ID], now))
	}
	return out, nil
}

// timelinePostOne は読み手が読める書き込み 1 件を返す（読めなければ 404）。
func (s *Service) timelinePostOne(ctx context.Context, a Actor, p store.Project, id int64) (TimelinePostView, error) {
	vs, err := s.timelinePostViews(ctx, a, p, id)
	if err != nil {
		return TimelinePostView{}, err
	}
	if len(vs) == 0 {
		return TimelinePostView{}, errTimelinePostNotFound(id)
	}
	return vs[0], nil
}

// timelinePostBetweenReads はテストだけが設定する。timelinePostSeqThen が通し番号を読んだ後、書き込みを読む前に呼ぶ
// （その間に確定した投稿を差し込み、読む順を壊すとテストが落ちるようにする）。
var timelinePostBetweenReads func()

// timelinePostSeqThen はプロジェクトの最後の通し番号を先に読み、その後で read を呼ぶ。通し番号を返す読みはすべてこれを通す。
// 逆の順で読むと、2 つの読みの間に確定した投稿が結果に入らないのに通し番号には数えられ、手元の通し番号と比べる軽い確認が
// その投稿を読み飛ばす。先に読めば、古い通し番号を返しても次の確認でもう 1 回取りに行くだけで済む。
func (s *Service) timelinePostSeqThen(ctx context.Context, p store.Project, read func() error) (int64, error) {
	seq, err := store.TimelinePostHeadSeq(ctx, s.DB, p.ID)
	if err != nil {
		return 0, err
	}
	if timelinePostBetweenReads != nil {
		timelinePostBetweenReads()
	}
	return seq, read()
}

// timelinePostTTL は秒の指定を期限の長さにする。0 は「指定なし」で 0 を返す（既定を当てるかは呼ぶ側が決める）。負と上限超えは拒む。
func timelinePostTTL(seconds int64) (time.Duration, error) {
	if seconds < 0 || seconds > int64(MaxTimelinePostTTL/time.Second) {
		return 0, errm(Invalid, "invalid_argument", i18n.M("service.err.timeline.ttl", "max", int64(MaxTimelinePostTTL/time.Second)))
	}
	return time.Duration(seconds) * time.Second, nil
}

// timelinePostLabel は端末のラベルと、記録に残す要求のセッションを確かめる（どちらも相手に見える。連絡と同じ検査）。
func timelinePostLabel(a Actor, v string) (string, error) {
	label, err := cleanTranscriptLabel("machine_label", v)
	if err != nil {
		return "", err
	}
	if err := checkMessageText("machine_label", label); err != nil {
		return "", err
	}
	return label, checkMessageText("sender_session", a.SessionID)
}

// cleanTimelinePostReason は取り下げの理由を確かめる（任意・1 行・200 文字まで・資格情報を含まない）。理由には入力の値を載せない。
func cleanTimelinePostReason(v string) (string, error) {
	v = strings.TrimSpace(v)
	if utf8.RuneCountInString(v) > maxTimelinePostReason {
		return "", errm(Invalid, "invalid_argument", i18n.M("service.err.timeline.reason", "max", maxTimelinePostReason))
	}
	return v, checkMessageText("reason", v)
}

func timelinePostVia(a Actor) string {
	if a.Via == "" {
		return "api"
	}
	return a.Via
}

// writeTimelinePostEvent は投稿の出来事を書く。fn は取引の最後に通し番号の行を握った後で呼ばれ、書く出来事を返す（ここで前の出来事を読み直す）。
// 通し番号の行を握っている間は同じプロジェクトのほかの出来事が割り込まない。握るまでに確定した出来事は普通の読みでは見えない
// （取引の最初の SELECT でスナップショットが決まる）ので、fn はロックつきの読み（store.TimelinePostEventsOf）で前の出来事を読む。
// prep は取引の中で通し番号より前に行う書き込み（投稿の変わらない部分の行）で、nil でよい。
func (s *Service) writeTimelinePostEvent(ctx context.Context, p store.Project, prep func(tx *sql.Tx) error, fn func(tx *sql.Tx) (store.TimelinePostEvent, error)) error {
	if err := store.EnsureTimelinePostHead(ctx, s.DB, p.ID, s.Now()); err != nil {
		return err
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if err := checkProjectActive(ctx, tx, p); err != nil {
			return err
		}
		if prep != nil {
			if err := prep(tx); err != nil {
				return err
			}
		}
		return queueTimeline(tx, timelineSource{Origin: timelineFromPost, ProjectID: p.ID, post: func(tx *sql.Tx, seq int64, now time.Time) error {
			e, err := fn(tx)
			if err != nil {
				return err
			}
			e.ProjectID, e.Seq, e.CreatedAt = p.ID, seq, now
			return store.InsertTimelinePostEvent(ctx, tx, e)
		}})
	})
}

// checkTimelineUpdate は近況（update）だけの規則を確かめる。宛先は全員だけ（省けば全員）・本文は 500 文字まで・
// 期限と解除の条件は持たない。本文の文字と資格情報の検査はほかの種類と同じ checkMessageBody が掛ける。
func checkTimelineUpdate(in *TimelinePostInput) error {
	if strings.TrimSpace(in.To) == "" {
		in.To = store.MessageToProject
	}
	if strings.TrimSpace(in.To) != store.MessageToProject {
		return errm(Invalid, "invalid_argument", i18n.M("service.err.timeline.update_to"))
	}
	if utf8.RuneCountInString(in.Body) > maxTimelineUpdateBody {
		return errm(Rejected, "message_too_large", i18n.M("service.err.timeline.update_too_long", "max", maxTimelineUpdateBody))
	}
	if in.TTLSeconds != 0 || strings.TrimSpace(in.ReleaseIssue) != "" || strings.TrimSpace(in.ReleaseLease) != "" {
		return errm(Invalid, "invalid_argument", i18n.M("service.err.timeline.update_no_expiry"))
	}
	return nil
}

// checkTimelineTo は宛先の種類と値を確かめ、宛先の種類を返す。project・user・session は連絡と同じ検査（checkMessageTo）。
// issue・branch・machine・others は依頼と待機だけが取り、値は在席と同じ文字の検査を掛ける（ブランチと端末のラベルは在席の値と比べるため）。
// 値はその種類の欄だけに入れる（ほかの欄が入っていたら拒む）。in の ToIssue・ToBranch・ToMachine は整えた値に書き換える。
func checkTimelineTo(kind string, in *TimelinePostInput) (string, error) {
	to := strings.TrimSpace(in.To)
	in.ToIssue, in.ToBranch, in.ToMachine = strings.TrimSpace(in.ToIssue), strings.TrimSpace(in.ToBranch), strings.TrimSpace(in.ToMachine)
	resolved := timelineResolved(to)
	if !resolved {
		if in.ToIssue != "" || in.ToBranch != "" || in.ToMachine != "" {
			return "", errm(Invalid, "invalid_argument", i18n.M("service.err.timeline.to_value_mismatch"))
		}
		if !slices.Contains([]string{store.MessageToProject, store.MessageToUser, store.MessageToSession}, to) {
			return "", errm(Invalid, "invalid_argument", i18n.M("service.err.timeline.to"))
		}
		return checkMessageTo(in.To, in.User, in.Session)
	}
	if kind != store.TimelinePostRequest && kind != store.TimelinePostHold {
		return "", errm(Invalid, "invalid_argument", i18n.M("service.err.timeline.to_resolved_kind", "to", to))
	}
	if strings.TrimSpace(in.User) != "" || strings.TrimSpace(in.Session) != "" {
		return "", errm(Invalid, "invalid_argument", i18n.M("service.err.timeline.to_value_mismatch"))
	}
	want := map[string]string{store.TimelineToIssue: in.ToIssue, store.TimelineToBranch: in.ToBranch, store.TimelineToMachine: in.ToMachine}
	for k, v := range want {
		if (k == to) != (v != "") {
			return "", errm(Invalid, "invalid_argument", i18n.M("service.err.timeline.to_value_mismatch"))
		}
	}
	switch to {
	case store.TimelineToBranch:
		v, err := cleanPresenceText("to_branch", in.ToBranch)
		in.ToBranch = v
		return to, err
	case store.TimelineToMachine:
		v, err := cleanPresenceText("to_machine", in.ToMachine)
		in.ToMachine = v
		return to, err
	}
	return to, nil
}

// PostTimelinePost はタイムラインに書き込む（editor 以上）。Issue を添えたら先にコメントを残す（拒まれたら書き込みも残さない）。
func (s *Service) PostTimelinePost(ctx context.Context, a Actor, in TimelinePostInput) (*TimelinePostView, error) {
	kind := strings.TrimSpace(in.Kind)
	if !slices.Contains([]string{store.TimelinePostUpdate, store.TimelinePostInfo, store.TimelinePostRequest, store.TimelinePostHold}, kind) {
		return nil, errm(Invalid, "invalid_argument", i18n.M("service.err.timeline.kind"))
	}
	update := kind == store.TimelinePostUpdate
	if update {
		if err := checkTimelineUpdate(&in); err != nil {
			return nil, err
		}
	}
	to, err := checkTimelineTo(kind, &in)
	if err != nil {
		return nil, err
	}
	if err := checkMessageBody(in.Body); err != nil {
		return nil, err
	}
	label, err := timelinePostLabel(a, in.MachineLabel)
	if err != nil {
		return nil, err
	}
	session := ""
	if to == store.MessageToSession {
		if session, err = cleanMessageSession(in.Session); err != nil {
			return nil, err
		}
	}
	ttl, err := timelinePostTTL(in.TTLSeconds)
	if err != nil {
		return nil, err
	}
	if kind == store.TimelinePostHold && ttl == 0 {
		ttl = DefaultTimelinePostHoldTTL
	}
	releaseIssue, releaseLease := strings.TrimSpace(in.ReleaseIssue), strings.TrimSpace(in.ReleaseLease)
	if kind != store.TimelinePostHold && (releaseIssue != "" || releaseLease != "") {
		return nil, errm(Invalid, "invalid_argument", i18n.M("service.err.timeline.release_hold_only"))
	}
	u, p, role, err := s.memoryAccess(ctx, a, strings.TrimSpace(in.Project))
	if err != nil {
		return nil, err
	}
	if !canSendMessage(role) {
		return nil, errm(Forbidden, "forbidden", i18n.M("service.err.timeline.forbidden", "slug", p.Slug))
	}
	if err := checkProjectActive(ctx, s.DB, p); err != nil {
		return nil, err
	}
	now := s.Now()
	b := store.TimelinePost{ProjectID: p.ID, Kind: kind, ToKind: to, ToSession: session, ToLabel: in.ToBranch + in.ToMachine, SenderUserID: u.ID,
		SenderMachine: label, SenderSession: a.SessionID, TokenID: a.TokenID, Via: timelinePostVia(a), CreatedAt: now}
	if to == store.TimelineToIssue {
		row, err := s.Locate(ctx, in.ToIssue, p.ID)
		if err != nil {
			return nil, err
		}
		st, err := store.TimelinePostIssueStatus(ctx, s.DB, row.ID)
		if err != nil {
			return nil, err
		}
		if slices.Contains(domain.ClosedStatuses, st) {
			return nil, errm(Rejected, "timeline_issue_closed", i18n.M("service.err.timeline.to_issue_closed", "id", row.DisplayID))
		}
		b.ToIssueID = row.ID
	}
	if to == store.MessageToUser || to == store.MessageToSession {
		target, err := s.messageRecipient(ctx, u, p, in.User)
		if err != nil {
			return nil, err
		}
		b.ToUserID = target.ID
	}
	if releaseIssue != "" {
		row, err := s.Locate(ctx, releaseIssue, p.ID)
		if err != nil {
			return nil, err
		}
		st, err := store.TimelinePostIssueStatus(ctx, s.DB, row.ID)
		if err != nil {
			return nil, err
		}
		if slices.Contains(domain.ClosedStatuses, st) {
			return nil, errm(Rejected, "timeline_issue_closed", i18n.M("service.err.timeline.issue_closed", "id", row.DisplayID))
		}
		b.ReleaseIssueID = row.ID
	}
	if releaseLease != "" {
		name, err := cleanLeaseName(releaseLease)
		if err != nil {
			return nil, err
		}
		l, err := store.LeaseByName(ctx, s.DB, p.ID, name, false)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		if err != nil || !l.Held() || !now.Before(l.ExpiresAt) {
			return nil, errm(Rejected, "timeline_lease_not_held", i18n.M("service.err.timeline.lease_not_held", "name", name))
		}
		b.ReleaseLease, b.ReleaseLeaseAcquiredAt = name, l.AcquiredAt
	}
	if id := strings.TrimSpace(in.Issue); id != "" {
		row, err := s.Locate(ctx, id, p.ID)
		if err != nil {
			return nil, err
		}
		if !update { // 近況は着手中のイシューを添えるだけで、コメントは残さない
			if _, err := s.AddComment(ctx, a, p, row.ID, CommentInput{Text: TimelinePostIssueLead + " " + in.Body, Message: true}); err != nil {
				return nil, err
			}
		}
		b.IssueID = row.ID
	}
	var expires time.Time
	if ttl > 0 {
		expires = now.Add(ttl)
	}
	err = s.writeTimelinePostEvent(ctx, p, func(tx *sql.Tx) error {
		id, err := store.InsertTimelinePost(ctx, tx, b)
		b.ID = id
		return err
	}, func(tx *sql.Tx) (store.TimelinePostEvent, error) {
		return store.TimelinePostEvent{TimelinePostID: b.ID, Action: store.TimelinePostActionPost, Version: 1, Body: in.Body, ExpiresAt: expires,
			ActorUserID: u.ID, ActorMachine: label, ActorSession: a.SessionID, TokenID: a.TokenID, Via: timelinePostVia(a)}, nil
	})
	if err != nil {
		return nil, err
	}
	v, err := s.timelinePostOne(ctx, a, p, b.ID)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// ReviseTimelinePost は書き込みを改訂する（書いた本人で editor 以上だけ）。上書きせず版を足し、前の版の本文と書き手は出来事に残る。
// 期限を延ばすときも改訂する。取り下げ済みは改訂できない。
func (s *Service) ReviseTimelinePost(ctx context.Context, a Actor, in TimelinePostReviseInput) (*TimelinePostView, error) {
	body := in.Body
	if strings.TrimSpace(body) == "" && in.TTLSeconds == 0 {
		return nil, errm(Invalid, "invalid_argument", i18n.M("service.err.timeline.revise_empty"))
	}
	if body != "" {
		if err := checkMessageBody(body); err != nil {
			return nil, err
		}
	}
	ttl, err := timelinePostTTL(in.TTLSeconds)
	if err != nil {
		return nil, err
	}
	label, err := timelinePostLabel(a, in.MachineLabel)
	if err != nil {
		return nil, err
	}
	u, p, role, err := s.memoryAccess(ctx, a, strings.TrimSpace(in.Project))
	if err != nil {
		return nil, err
	}
	cur, err := s.timelinePostOne(ctx, a, p, in.ID)
	if err != nil {
		return nil, err
	}
	if cur.Kind == store.TimelinePostUpdate {
		return nil, errm(Invalid, "invalid_argument", i18n.M("service.err.timeline.update_immutable", "id", in.ID))
	}
	if cur.SenderUserID != u.ID {
		return nil, errm(Forbidden, "timeline_not_author", i18n.M("service.err.timeline.not_author", "id", in.ID))
	}
	if !canSendMessage(role) {
		return nil, errm(Forbidden, "forbidden", i18n.M("service.err.timeline.forbidden", "slug", p.Slug))
	}
	err = s.writeTimelinePostEvent(ctx, p, nil, func(tx *sql.Tx) (store.TimelinePostEvent, error) {
		events, err := store.TimelinePostEventsOf(ctx, tx, in.ID)
		if err != nil {
			return store.TimelinePostEvent{}, err
		}
		var last, content store.TimelinePostEvent
		for _, e := range events {
			last = e
			if e.Action != store.TimelinePostActionWithdraw {
				content = e
			}
		}
		if last.Action == store.TimelinePostActionWithdraw {
			return store.TimelinePostEvent{}, errm(Conflict, "timeline_withdrawn", i18n.M("service.err.timeline.withdrawn", "id", in.ID))
		}
		e := store.TimelinePostEvent{TimelinePostID: in.ID, Action: store.TimelinePostActionRevise, Version: last.Version + 1, Body: content.Body,
			ExpiresAt: content.ExpiresAt, ActorUserID: u.ID, ActorMachine: label, ActorSession: a.SessionID, TokenID: a.TokenID, Via: timelinePostVia(a)}
		if body != "" {
			e.Body = body
		}
		if ttl > 0 {
			e.ExpiresAt = s.Now().Add(ttl)
		}
		return e, nil
	})
	if err != nil {
		return nil, err
	}
	v, err := s.timelinePostOne(ctx, a, p, in.ID)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// WithdrawTimelinePost は書き込みを取り下げる。書いた本人は理由なしで（役割を問わない）、ほかの editor 以上は理由つきで取り下げられる
// （占有を外す規則と同じ）。理由は出来事に残る。
func (s *Service) WithdrawTimelinePost(ctx context.Context, a Actor, in TimelinePostWithdrawInput) (*TimelinePostView, error) {
	reason, err := cleanTimelinePostReason(in.Reason)
	if err != nil {
		return nil, err
	}
	label, err := timelinePostLabel(a, in.MachineLabel)
	if err != nil {
		return nil, err
	}
	u, p, role, err := s.memoryAccess(ctx, a, strings.TrimSpace(in.Project))
	if err != nil {
		return nil, err
	}
	cur, err := s.timelinePostOne(ctx, a, p, in.ID)
	if err != nil {
		return nil, err
	}
	if cur.Kind == store.TimelinePostUpdate {
		return nil, errm(Invalid, "invalid_argument", i18n.M("service.err.timeline.update_immutable", "id", in.ID))
	}
	if cur.SenderUserID != u.ID {
		if !canSendMessage(role) {
			return nil, errm(Forbidden, "forbidden", i18n.M("service.err.timeline.withdraw_forbidden", "id", in.ID))
		}
		if reason == "" {
			return nil, errm(Invalid, "invalid_argument", i18n.M("service.err.timeline.reason_required"))
		}
	}
	err = s.writeTimelinePostEvent(ctx, p, nil, func(tx *sql.Tx) (store.TimelinePostEvent, error) {
		events, err := store.TimelinePostEventsOf(ctx, tx, in.ID)
		if err != nil {
			return store.TimelinePostEvent{}, err
		}
		if len(events) == 0 {
			return store.TimelinePostEvent{}, errTimelinePostNotFound(in.ID)
		}
		last := events[len(events)-1]
		if last.Action == store.TimelinePostActionWithdraw {
			return store.TimelinePostEvent{}, errm(Conflict, "timeline_withdrawn", i18n.M("service.err.timeline.withdrawn", "id", in.ID))
		}
		return store.TimelinePostEvent{TimelinePostID: in.ID, Action: store.TimelinePostActionWithdraw, Version: last.Version + 1, Reason: reason,
			ActorUserID: u.ID, ActorMachine: label, ActorSession: a.SessionID, TokenID: a.TokenID, Via: timelinePostVia(a)}, nil
	})
	if err != nil {
		return nil, err
	}
	v, err := s.timelinePostOne(ctx, a, p, in.ID)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// AckTimelinePost は依頼か待機の最新の版を、このセッションが了解したと記録する（宛先に当たるセッションだけ。役割は問わない）。
// 了解した版は受け取った版でもあるので、受け取りも同じ版で記録する（2 つは別の行に残る）。
func (s *Service) AckTimelinePost(ctx context.Context, a Actor, in TimelinePostAckInput) (*TimelinePostView, error) {
	if err := checkMessageText("session", a.SessionID); err != nil {
		return nil, err
	}
	_, p, _, err := s.memoryAccess(ctx, a, strings.TrimSpace(in.Project))
	if err != nil {
		return nil, err
	}
	cur, err := s.timelinePostOne(ctx, a, p, in.ID)
	if err != nil {
		return nil, err
	}
	if !cur.AppliesToMe {
		return nil, errm(Forbidden, "timeline_not_recipient", i18n.M("service.err.timeline.not_recipient", "id", in.ID))
	}
	if cur.Kind == store.TimelinePostInfo || cur.Kind == store.TimelinePostUpdate {
		return nil, errm(Invalid, "invalid_argument", i18n.M("service.err.timeline.not_ackable"))
	}
	if !cur.Active() {
		return nil, errm(Conflict, "timeline_inactive", i18n.M("service.err.timeline.inactive", "id", in.ID))
	}
	if in.Version != cur.Version {
		return nil, errm(Conflict, "timeline_version", i18n.M("service.err.timeline.version", "id", in.ID, "current", cur.Version, "given", in.Version))
	}
	now := s.Now()
	if _, err := store.InsertTimelinePostReceipts(ctx, s.DB, []store.TimelinePostReceipt{
		{TimelinePostID: in.ID, UserID: a.UserID, SessionID: a.SessionID, Version: cur.Version, Kind: store.TimelinePostReceive, CreatedAt: now},
		{TimelinePostID: in.ID, UserID: a.UserID, SessionID: a.SessionID, Version: cur.Version, Kind: store.TimelinePostAck, CreatedAt: now},
	}); err != nil {
		return nil, err
	}
	v, err := s.timelinePostOne(ctx, a, p, in.ID)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// RecordTimelinePostReceipts は、本文を文脈に入れた書き込みの受け取りをこのセッションに記録し、新しく足した件数を返す。
// 読めない・このセッションに効かない書き込みと、無い番号は黙って飛ばす（連絡の既読と同じ）。
func (s *Service) RecordTimelinePostReceipts(ctx context.Context, a Actor, slug string, items []TimelinePostReceiptInput) (int64, error) {
	if len(items) > maxUnreadIDs {
		return 0, errm(Invalid, "invalid_argument", i18n.M("service.err.timeline.too_many_ids", "max", maxUnreadIDs))
	}
	// 受け取りと了解のセッションは到達の状況で送り手にも見える（投稿の sender_session と同じ検査）
	if err := checkMessageText("session", a.SessionID); err != nil {
		return 0, err
	}
	_, p, _, err := s.memoryAccess(ctx, a, strings.TrimSpace(slug))
	if err != nil {
		return 0, err
	}
	if len(items) == 0 {
		return 0, nil
	}
	vs, err := s.timelinePostViews(ctx, a, p, 0)
	if err != nil {
		return 0, err
	}
	byID := make(map[int64]TimelinePostView, len(vs))
	for _, v := range vs {
		byID[v.ID] = v
	}
	now := s.Now()
	var rows []store.TimelinePostReceipt
	for _, it := range items {
		v, ok := byID[it.ID]
		if !ok || !v.AppliesToMe || it.Version < 1 || it.Version > v.LastVersion {
			continue
		}
		rows = append(rows, store.TimelinePostReceipt{TimelinePostID: it.ID, UserID: a.UserID, SessionID: a.SessionID, Version: it.Version,
			Kind: store.TimelinePostReceive, CreatedAt: now})
	}
	return store.InsertTimelinePostReceipts(ctx, s.DB, rows)
}

// ListTimelinePosts は読み手のセッションに効く有効な書き込みを返す（Sent なら自分が出した有効な書き込み）。並びは待機・依頼・知らせの順。
func (s *Service) ListTimelinePosts(ctx context.Context, a Actor, slug string, q TimelinePostListQuery) (*TimelinePostListResult, error) {
	u, p, role, err := s.memoryAccess(ctx, a, strings.TrimSpace(slug))
	if err != nil {
		return nil, err
	}
	var vs []TimelinePostView
	res := &TimelinePostListResult{TimelinePosts: []TimelinePostView{}, CanPost: canSendMessage(role), User: u.Login, ProjectSlug: p.Slug}
	res.Seq, err = s.timelinePostSeqThen(ctx, p, func() (err error) {
		vs, err = s.timelinePostViews(ctx, a, p, 0)
		return err
	})
	if err != nil {
		return nil, err
	}
	for _, v := range vs {
		if !v.Active() {
			continue
		}
		if (q.Sent && v.SenderUserID == u.ID) || (!q.Sent && v.AppliesToMe) {
			res.TimelinePosts = append(res.TimelinePosts, v)
		}
	}
	sortTimelinePosts(res.TimelinePosts)
	return res, nil
}

// GetTimelinePost は書き込み 1 件と出来事を返す（宛先と送り手だけ。ほかの利用者には無いものと同じ 404）。取り下げ済み・期限切れも返す。
func (s *Service) GetTimelinePost(ctx context.Context, a Actor, slug string, id int64) (*TimelinePostDetail, error) {
	_, p, _, err := s.memoryAccess(ctx, a, strings.TrimSpace(slug))
	if err != nil {
		return nil, err
	}
	v, err := s.timelinePostOne(ctx, a, p, id)
	if err != nil {
		return nil, err
	}
	since, err := s.messageSince(ctx)
	if err != nil {
		return nil, err
	}
	events, err := store.ReadableTimelinePostEvents(ctx, s.DB, p.ID, a.UserID, since, s.Now(), id)
	if err != nil {
		return nil, err
	}
	return &TimelinePostDetail{TimelinePostView: v, Events: events}, nil
}

// PendingTimelinePosts は読み手のセッションの未達と未了解を返す。limit（0 は無制限）で各一覧を切るが、受け取りは記録しないので
// 切った分も記録するまで次にもう一度出る。
func (s *Service) PendingTimelinePosts(ctx context.Context, a Actor, slug string, limit int) (*TimelinePostPending, error) {
	_, p, _, err := s.memoryAccess(ctx, a, strings.TrimSpace(slug))
	if err != nil {
		return nil, err
	}
	var vs []TimelinePostView
	res := &TimelinePostPending{Undelivered: []TimelinePostView{}, Unacked: []TimelinePostView{}, ProjectSlug: p.Slug}
	res.Seq, err = s.timelinePostSeqThen(ctx, p, func() (err error) {
		vs, err = s.timelinePostViews(ctx, a, p, 0)
		return err
	})
	if err != nil {
		return nil, err
	}
	for _, v := range vs {
		if v.Undelivered() {
			res.Undelivered = append(res.Undelivered, v)
		}
		if v.Unacked() {
			res.Unacked = append(res.Unacked, v)
		}
	}
	sortTimelinePosts(res.Undelivered)
	sortTimelinePosts(res.Unacked)
	res.UndeliveredTotal, res.UnackedTotal = len(res.Undelivered), len(res.Unacked)
	if limit > 0 {
		res.Undelivered = res.Undelivered[:min(limit, len(res.Undelivered))]
		res.Unacked = res.Unacked[:min(limit, len(res.Unacked))]
	}
	return res, nil
}

// CheckTimelinePosts は軽い確認の値を返す。手元の値とどちらかが違うときだけ、呼ぶ側が PendingTimelinePosts を取りに行く。
// 期限や解除で効力を失った待機は通し番号を進めないが、待機の ID の並びから外れるので気づける。
func (s *Service) CheckTimelinePosts(ctx context.Context, a Actor, slug string) (*TimelinePostCheck, error) {
	_, p, _, err := s.memoryAccess(ctx, a, strings.TrimSpace(slug))
	if err != nil {
		return nil, err
	}
	var vs []TimelinePostView
	res := &TimelinePostCheck{Holds: []int64{}}
	res.Seq, err = s.timelinePostSeqThen(ctx, p, func() (err error) {
		vs, err = s.timelinePostViews(ctx, a, p, 0)
		return err
	})
	if err != nil {
		return nil, err
	}
	for _, v := range vs {
		if v.Kind == store.TimelinePostHold && v.Active() && v.AppliesToMe {
			res.Holds = append(res.Holds, v.ID)
		}
	}
	slices.Sort(res.Holds)
	if err := s.timelineCheckPosition(ctx, a, p, res); err != nil {
		return nil, err
	}
	return res, nil
}

// TimelinePostEventsAfter は読み手が読める書き込みの出来事のうち、通し番号が after より大きいものを番号の順に返す（limit は 0 で既定）。
// 経路（REST・MCP・CLI）には出していない。未達は受け取りの記録から決める（PendingTimelinePosts）。
func (s *Service) TimelinePostEventsAfter(ctx context.Context, a Actor, slug string, after int64, limit int) (*TimelinePostEventsResult, error) {
	if after < 0 {
		after = 0
	}
	if limit <= 0 {
		limit = defaultTimelinePostEvents
	}
	limit = min(limit, maxTimelinePostEvents)
	_, p, _, err := s.memoryAccess(ctx, a, strings.TrimSpace(slug))
	if err != nil {
		return nil, err
	}
	since, err := s.messageSince(ctx)
	if err != nil {
		return nil, err
	}
	res := &TimelinePostEventsResult{}
	res.Seq, err = s.timelinePostSeqThen(ctx, p, func() (err error) {
		res.Events, err = store.TimelinePostEventsAfter(ctx, s.DB, p.ID, a.UserID, since, s.Now(), after, limit)
		return err
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// TimelinePostDeliveryStatus は到達の状況を返す（その書き込みを読める者だけ）。行は受け取りか了解を持つセッションと、
// 宛先に当たる動いているセッション（在席から引く。出したセッション自身は除く）。宛先に当たったかは読み手と同じ timelinePostApplies で決め、
// 当たった行には出どころ（着手の記録か在席か）を付ける。イシューの宛先は、在席を送っていない着手のセッションも着手の記録から並べる。
func (s *Service) TimelinePostDeliveryStatus(ctx context.Context, a Actor, slug string, id int64) (*TimelinePostDelivery, error) {
	_, p, _, err := s.memoryAccess(ctx, a, strings.TrimSpace(slug))
	if err != nil {
		return nil, err
	}
	v, err := s.timelinePostOne(ctx, a, p, id)
	if err != nil {
		return nil, err
	}
	receipts, err := store.TimelinePostReceiptsOf(ctx, s.DB, id)
	if err != nil {
		return nil, err
	}
	presence, err := store.ActivePresence(ctx, s.DB, p.ID, s.Now().Add(-PresenceActive))
	if err != nil {
		return nil, err
	}
	var starters []store.Starter
	if timelineResolved(v.ToKind) {
		if starters, err = store.InProgressStarters(ctx, s.DB, p.ID); err != nil {
			return nil, err
		}
	}
	type key struct {
		user    int64
		session string
	}
	rows := map[key]*TimelinePostDeliveryRow{}
	row := func(userID int64, login, session string) *TimelinePostDeliveryRow {
		k := key{userID, session}
		if r, ok := rows[k]; ok {
			return r
		}
		r := &TimelinePostDeliveryRow{UserID: userID, User: login, Session: session}
		rows[k] = r
		return r
	}
	for _, r := range receipts {
		x := row(r.UserID, r.UserLogin, r.SessionID)
		if r.Kind == store.TimelinePostAck {
			x.Acked = max(x.Acked, r.Version)
		} else {
			x.Received = max(x.Received, r.Version)
		}
	}
	resolvedBy := TimelineResolvedByPresence
	if v.ToKind == store.TimelineToIssue {
		resolvedBy = TimelineResolvedByRecord
	}
	for _, pr := range presence {
		_, known := rows[key{pr.UserID, pr.SessionID}]
		hit := timelinePostApplies(v.TimelinePost, presenceWho(pr, starters))
		if !known && !hit {
			continue
		}
		x := row(pr.UserID, pr.UserLogin, pr.SessionID)
		x.MachineLabel, x.LastSeenAt = pr.MachineLabel, pr.LastSeenAt
		if hit {
			x.ResolvedBy = resolvedBy
		}
	}
	// イシューの宛先は着手の記録が出どころなので、在席を送っていない着手のセッションも並べる（在席の時刻は空）。
	// MCP で着手して会話に結んだ着手は、記録した値（接続の ID）と結んだ値（会話の ID）の 2 つを持つが、同じ AI なので行は 1 つにする。
	// 在席か受け取り・了解の記録で既に並んでいる側を採り、どちらも無ければ会話の ID（結んだ値。無ければ記録した値）で並べる。
	if v.ToKind == store.TimelineToIssue {
		users := map[int64]string{}
		for _, st := range starters {
			if st.DisplayID != v.ToIssueDisplay {
				continue
			}
			sessions := []string{}
			for _, sess := range []string{st.LinkedSessionID, st.SessionID} {
				if sess != "" && !slices.Contains(sessions, sess) {
					sessions = append(sessions, sess)
				}
			}
			if len(sessions) == 0 || slices.ContainsFunc(sessions, func(sess string) bool { return timelinePostOwn(v.TimelinePost, st.UserID, sess) }) {
				continue
			}
			listed := false
			for _, sess := range sessions {
				if r, ok := rows[key{st.UserID, sess}]; ok {
					r.ResolvedBy, listed = TimelineResolvedByRecord, true
				}
			}
			if listed {
				continue
			}
			if _, ok := users[st.UserID]; !ok {
				u, err := store.UserByID(ctx, s.DB, st.UserID)
				if err != nil {
					return nil, err
				}
				users[st.UserID] = u.Login
			}
			row(st.UserID, users[st.UserID], sessions[0]).ResolvedBy = TimelineResolvedByRecord
		}
	}
	res := &TimelinePostDelivery{TimelinePost: v, Rows: make([]TimelinePostDeliveryRow, 0, len(rows))}
	for _, r := range rows {
		res.Rows = append(res.Rows, *r)
	}
	sort.Slice(res.Rows, func(i, j int) bool {
		if res.Rows[i].User != res.Rows[j].User {
			return res.Rows[i].User < res.Rows[j].User
		}
		return res.Rows[i].Session < res.Rows[j].Session
	})
	return res, nil
}
