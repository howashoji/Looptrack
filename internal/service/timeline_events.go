package service

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// タイムラインの自動の出来事（DESIGN.md §9-9「タイムラインに載るもの」「タイムラインの表と通し番号」）。
//
// 出来事はサーバが元の記録（issue_events・lease_events・在席・コミット）と同じ取引で書く。元の記録を書く service の各所は
// recordIssueEvent・recordLeaseEvent・queueTimeline を通り、取引ごとの収集器（timelineBatch）に源を溜める。
// 何を載せるかは timelineEventsOf の 1 か所で決める。経路（REST・MCP・CLI）ごとには書かない。
//
// 通し番号は投稿の出来事と 1 本で、timeline_heads の行を取引の最後（inTx が確定する直前の flushTimeline）に 1 度だけロックし、
// その取引が書く出来事にまとめて番号を振る。head を握ったままほかの行（イシュー・占有）のロックを待つとデッドロックになるからで、
// メンバーの削除に伴う担当の付け替えのように 1 つの取引で複数のイシューを順にロックしても、head は最後に 1 度だけ取る。
// 投稿の出来事（timeline_post_events）も同じ収集器に入れ、版の計算は head を握った後に行う。head を握った後に確定する投稿は無いが、
// 握るまでの間に確定した改訂は取引のスナップショットに入らないので、前の出来事はロックつきの読み（store.TimelinePostEventsOf）で読み直す。

// timelineBatches は取引ごとの収集器（inTx が登録し、確定か取り消しで外す）。
var timelineBatches sync.Map // *sql.Tx → *timelineBatch

// timelineBatch は 1 つの取引が書く出来事の源（書いた順）。
type timelineBatch struct {
	srcs []timelineSource
}

// timelineSource は出来事の源 1 件。元の記録の種類と detail のまま持ち、載せるかは flushTimeline の中の timelineEventsOf が決める。
type timelineSource struct {
	// Origin は源の種類（timelineFromIssue・timelineFromLease・timelineFromSession・timelineFromCommit・timelineFromPost）
	Origin string
	// Kind は元の記録の種類（issue_events の kind・lease_events の action・在席の start / end）
	Kind      string
	Detail    map[string]any
	ProjectID int64
	IssueID   int64
	UserID    int64
	Session   string
	// Machine は端末のラベル（空なら在席から引く）
	Machine string
	TokenID int64
	Via     string
	At      time.Time
	// post は投稿の出来事を書く関数（Origin が timelineFromPost のとき）。head を握った後に通し番号を渡して呼ぶ
	post func(tx *sql.Tx, seq int64, now time.Time) error
}

const (
	timelineFromIssue   = "issue"
	timelineFromLease   = "lease"
	timelineFromSession = "session"
	timelineFromCommit  = "commit"
	timelineFromPost    = "post"
)

// 在席の出来事の種類（timelineSource.Kind）。
const (
	timelineSessionStart = "start"
	timelineSessionEnd   = "end"
)

// timelineVia は出来事の経路（表の CHECK の値に丸める）。
func timelineVia(v string) string {
	if slices.Contains([]string{"cli", "web", "mcp", "api", "admin"}, v) {
		return v
	}
	return "api"
}

// queueTimeline は取引の収集器に源を足す。inTx の外の取引では誤りにする（出来事が黙って抜けないように）。
func queueTimeline(tx *sql.Tx, src timelineSource) error {
	b, ok := timelineBatches.Load(tx)
	if !ok {
		return errors.New("timeline: the transaction was not started by inTx")
	}
	b.(*timelineBatch).srcs = append(b.(*timelineBatch).srcs, src)
	return nil
}

// recordIssueEvent はイシューの記録（issue_events）を書き、同じ取引の収集器に源を足す。store.InsertEvent はこれを通して呼ぶ。
func (s *Service) recordIssueEvent(ctx context.Context, tx *sql.Tx, e store.Event) error {
	if err := store.InsertEvent(ctx, tx, e); err != nil {
		return err
	}
	detail, _ := e.Detail.(map[string]any)
	return queueTimeline(tx, timelineSource{Origin: timelineFromIssue, Kind: e.Kind, Detail: detail, ProjectID: e.ProjectID, IssueID: e.IssueID,
		UserID: e.Author.UserID, Session: e.SessionID, TokenID: e.Author.TokenID, Via: e.Author.Via, At: e.Author.At})
}

// recordLeaseEvent は占有の記録（lease_events）を書き、同じ取引の収集器に源を足す。store.InsertLeaseEvent はこれを通して呼ぶ。
func (s *Service) recordLeaseEvent(ctx context.Context, tx *sql.Tx, projectID int64, name string, e store.LeaseEvent) error {
	if err := store.InsertLeaseEvent(ctx, tx, e); err != nil {
		return err
	}
	detail := map[string]any{"name": name, "purpose": e.Purpose}
	if e.Reason != "" {
		detail["reason"] = e.Reason
	}
	return queueTimeline(tx, timelineSource{Origin: timelineFromLease, Kind: e.Action, Detail: detail, ProjectID: projectID, UserID: e.UserID,
		Session: e.SessionID, Machine: e.MachineLabel, TokenID: e.TokenID, Via: e.Via, At: e.CreatedAt})
}

// timelineEventsOf は取引が書いた源の並びから、タイムラインに載せる出来事を書いた順に返す（何を載せるかの規則はここだけ）。
// 投稿の出来事（Origin が timelineFromPost）は別の表に書くので、ここでは返さない（flushTimeline が通し番号だけを振る）。
//
// 載せる: イシューの起票・状態の変更（コメントを添えたらその旨も）・担当の変更（assign。同じ取引の assignee_takeover は「引き取り」の印）・
// コメント（連絡や投稿が残したものも）、占有の取得・返却・強制解除（強制解除は理由も）、セッションの開始と終了、コミット。
// 載せない: update・verify・attach・attach_purge・rule_override・assignee_override・assignee_takeover そのもの・In Progress にしたときに本人へ自動で付く担当（next・set_status・起票。detail の auto）・
// 占有の延長、行った利用者の分からない記録。取り込み（import）は issue_events へ直接書く別の経路で、ここを通らない。
func timelineEventsOf(srcs []timelineSource) []store.TimelineEvent {
	var out []store.TimelineEvent
	for _, e := range timelineEventSlots(srcs) {
		if e != nil {
			out = append(out, *e)
		}
	}
	return out
}

// timelineEventSlots は timelineEventsOf の本体。源と同じ長さで、載せない源の位置は nil にする。
func timelineEventSlots(srcs []timelineSource) []*store.TimelineEvent {
	out := make([]*store.TimelineEvent, len(srcs))
	lastAssign := map[int64]int{} // イシューごとの、最後に載せた担当の変更の位置
	for i, src := range srcs {
		if src.UserID == 0 || src.Origin == timelineFromPost {
			continue
		}
		e := store.TimelineEvent{ProjectID: src.ProjectID, ActorUserID: src.UserID, ActorMachine: src.Machine, ActorSession: src.Session,
			TokenID: src.TokenID, Via: timelineVia(src.Via), CreatedAt: src.At, Detail: map[string]any{}}
		switch src.Origin {
		case timelineFromIssue:
			e.IssueID = src.IssueID
			switch src.Kind {
			case "create":
				e.Kind = store.TimelineEventIssueCreate
			case "status":
				e.Kind = store.TimelineEventIssueStatus
				e.Detail["from"], e.Detail["to"] = src.Detail["from"], src.Detail["to"]
				if _, ok := src.Detail["comment_seq"]; ok {
					e.Detail["comment"] = true
				}
			case "assign":
				if auto, _ := src.Detail["auto"].(bool); auto {
					continue // next が着手と同時に付けた担当は、状態の変更の 1 行にまとめる
				}
				e.Kind = store.TimelineEventIssueAssign
				e.Detail["to"] = src.Detail["to"]
				lastAssign[src.IssueID] = i
			case "assignee_takeover":
				if j, ok := lastAssign[src.IssueID]; ok {
					out[j].Detail["takeover"] = true
				}
				continue
			case "comment":
				e.Kind = store.TimelineEventIssueComment
				if k, _ := src.Detail["kind"].(string); k == "message" {
					e.Detail["message"] = true
				}
			default:
				continue
			}
		case timelineFromLease:
			switch src.Kind {
			case store.LeaseActionAcquire:
				e.Kind = store.TimelineEventLeaseAcquire
			case store.LeaseActionRelease:
				e.Kind = store.TimelineEventLeaseRelease
			case store.LeaseActionBreak:
				e.Kind = store.TimelineEventLeaseBreak
				e.Detail["reason"] = src.Detail["reason"]
			default:
				continue // 延長は持ち主が変わらないので載せない
			}
			e.Detail["name"], e.Detail["purpose"] = src.Detail["name"], src.Detail["purpose"]
		case timelineFromSession:
			switch src.Kind {
			case timelineSessionStart:
				e.Kind = store.TimelineEventSessionStart
				e.Detail["agent"], e.Detail["branch"] = src.Detail["agent"], src.Detail["branch"]
			case timelineSessionEnd:
				e.Kind = store.TimelineEventSessionEnd
			default:
				continue
			}
		case timelineFromCommit:
			e.Kind = store.TimelineEventCommit
			for k, v := range src.Detail {
				e.Detail[k] = v
			}
		default:
			continue
		}
		out[i] = &e
	}
	return out
}

// flushTimeline は取引の収集器にある出来事を書く。inTx が fn の後・確定の前に 1 度だけ呼ぶ。
// プロジェクトごとに、端末のラベルとイシューの見出しを読み、projects の行を共有ロックで読んでから、通し番号の行を 1 度だけロックして
// まとめて番号を振る（ロックの順は「イシュー・占有 → projects → timeline_heads」。起票は projects を先に排他で握るので、
// 出来事の外部キーの共有ロックを head の後に取ると逆の順になる）。
func (s *Service) flushTimeline(ctx context.Context, tx *sql.Tx, b *timelineBatch) error {
	if len(b.srcs) == 0 {
		return nil
	}
	events := timelineEventSlots(b.srcs)
	type slot struct {
		event *store.TimelineEvent
		post  func(tx *sql.Tx, seq int64, now time.Time) error
	}
	// 投稿の出来事と自動の出来事は、源を書いた順に番号を振る
	byProject := map[int64][]slot{}
	var projects []int64
	for i, src := range b.srcs {
		var sl slot
		switch {
		case src.Origin == timelineFromPost:
			sl.post = src.post
		case events[i] != nil:
			sl.event = events[i]
		default:
			continue
		}
		if _, ok := byProject[src.ProjectID]; !ok {
			projects = append(projects, src.ProjectID)
		}
		byProject[src.ProjectID] = append(byProject[src.ProjectID], sl)
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i] < projects[j] })
	now := s.Now()
	for _, pid := range projects {
		slots := byProject[pid]
		type who struct {
			user    int64
			session string
		}
		labels := map[who]string{}
		var issueIDs []int64
		for _, sl := range slots {
			if sl.event == nil {
				continue
			}
			e := sl.event
			if e.ActorMachine == "" && e.ActorSession != "" {
				key := who{e.ActorUserID, e.ActorSession}
				label, ok := labels[key]
				if !ok {
					var err error
					if label, err = store.PresenceMachineLabel(ctx, tx, pid, e.ActorUserID, e.ActorSession); err != nil {
						return err
					}
					labels[key] = label
				}
				e.ActorMachine = label
			}
			if e.IssueID != 0 && !slices.Contains(issueIDs, e.IssueID) {
				issueIDs = append(issueIDs, e.IssueID)
			}
		}
		heads, err := store.IssueHeadlines(ctx, tx, issueIDs)
		if err != nil {
			return err
		}
		for _, sl := range slots {
			if e := sl.event; e != nil && e.IssueID != 0 {
				h := heads[e.IssueID]
				e.Detail["issue"], e.Detail["title"] = h.Display, h.Title
				if e.Kind == store.TimelineEventIssueCreate {
					e.Detail["type"] = h.Type
				}
			}
		}
		if err := store.ShareLockProject(ctx, tx, pid); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if timelineBeforeHead != nil {
			timelineBeforeHead(pid)
		}
		// コミットの出来事は、同じプロジェクトに同じ SHA が読む窓の中に載っていれば重ねて書かない（hook の子が記録を書く前に次の区切りが
		// 同じ範囲を送ったとき）。確かめるのは通し番号の行を握った後で、読みはロックつき（store.TimelineCommitSHAs）なので、
		// 握るのを待っている間に先の取引が確定させた行も見え、並んで送られても二重にならない
		base, err := store.LockTimelineHeadThen(ctx, tx, pid, now, func() (int, error) {
			var err error
			slots, err = dropKnownCommits(ctx, tx, pid, now, slots, func(sl slot) *store.TimelineEvent { return sl.event })
			return len(slots), err
		})
		if err != nil {
			return err
		}
		for i, sl := range slots {
			seq := base + int64(i) + 1
			if sl.post != nil {
				if err := sl.post(tx, seq, now); err != nil {
					return err
				}
				continue
			}
			sl.event.Seq = seq
			if sl.event.CreatedAt.IsZero() {
				sl.event.CreatedAt = now
			}
			if err := store.InsertTimelineEvent(ctx, tx, *sl.event); err != nil {
				return err
			}
		}
	}
	return nil
}

// dropKnownCommits は、読む窓（TimelineReadWindow）の中に同じ SHA のコミットの出来事が既にあるもの・同じ取引の中で重なるものを外す。
// コミットの無い並びでは表を読まない。
func dropKnownCommits[T any](ctx context.Context, tx *sql.Tx, pid int64, now time.Time, slots []T, event func(T) *store.TimelineEvent) ([]T, error) {
	sha := func(sl T) string {
		if e := event(sl); e != nil && e.Kind == store.TimelineEventCommit {
			s, _ := e.Detail["sha"].(string)
			return s
		}
		return ""
	}
	if !slices.ContainsFunc(slots, func(sl T) bool { return sha(sl) != "" }) {
		return slots, nil
	}
	known, err := store.TimelineCommitSHAs(ctx, tx, pid, now.Add(-TimelineReadWindow))
	if err != nil {
		return nil, err
	}
	out := slots[:0:0]
	for _, sl := range slots {
		if h := sha(sl); h != "" {
			if known[h] {
				continue
			}
			known[h] = true
		}
		out = append(out, sl)
	}
	return out, nil
}

// timelineBeforeHead はテストだけが設定する。flushTimeline が通し番号の行をロックする直前に呼ぶ（並行する取引の順序を作るため）。
var timelineBeforeHead func(projectID int64)

// コミットの出来事の上限。
const (
	// MaxTimelineCommits は 1 回に受け取るコミットの数。超えた分は件数だけを 1 行で載せる
	MaxTimelineCommits = 20
	// maxTimelineCommitSubject は件名（1 行目）の上限（文字数）
	maxTimelineCommitSubject = 200
	// maxTimelineCommitOmitted は件数だけを載せる分の上限（名乗った値なので丸める）
	maxTimelineCommitOmitted = 100000
)

var timelineCommitSHARe = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// TimelineCommit はコミット 1 件（SHA と件名）。
type TimelineCommit struct {
	SHA     string
	Subject string
}

// TimelineCommitsInput は hook が送るコミット（前に送った HEAD から今の HEAD まで。古い順）。
type TimelineCommitsInput struct {
	Project string
	Branch  string
	Commits []TimelineCommit
	// Omitted は送らなかったコミットの数（hook が上限で切った分）
	Omitted int
}

// TimelineCommitsResult はコミットを受けた結果。
type TimelineCommitsResult struct {
	// Recorded はコミットとして載せた行の数（件数だけの行は数えない）。Omitted は件数だけを載せた分
	Recorded int
	Omitted  int
	// Withheld は件名を載せなかった数（資格情報の形をしていた・1 行として読めなかった）
	Withheld int
}

// timelineCommitSubject は件名を 1 行目に切り、載せてよい形かを返す。資格情報の形・1 行として読めない文字を含むなら載せない。
func timelineCommitSubject(v string) (string, bool) {
	v, _, _ = strings.Cut(strings.ReplaceAll(v, "\r\n", "\n"), "\n")
	v = strings.TrimSpace(v)
	if !utf8.ValidString(v) || strings.ContainsFunc(v, messageBadRune) {
		return "", false
	}
	if _, ok := domain.FindSecret(v); ok {
		return "", false
	}
	if utf8.RuneCountInString(v) > maxTimelineCommitSubject {
		v = string([]rune(v)[:maxTimelineCommitSubject])
	}
	return v, true
}

// RecordTimelineCommits はコミットをタイムラインの出来事として書く（editor 以上。連絡と同じ。セッションの無い送信は拒む）。
// 件名には連絡と同じ資格情報の検査を掛け、当たったら件名を載せずにブランチと SHA だけを書く（要求そのものは拒まない）。
func (s *Service) RecordTimelineCommits(ctx context.Context, a Actor, in TimelineCommitsInput) (*TimelineCommitsResult, error) {
	// セッションの無い送信は拒む（在席とそろえる。コミットはそのセッションの出来事として載り、自分の未読から外す鍵になる）
	if a.SessionID == "" {
		return nil, errm(Invalid, "invalid_argument", i18n.M("service.err.timeline.commit_no_session"))
	}
	if err := checkMessageText("session", a.SessionID); err != nil {
		return nil, err
	}
	branch, err := cleanPresenceText("branch", in.Branch)
	if err != nil {
		return nil, err
	}
	if in.Omitted < 0 {
		return nil, errm(Invalid, "invalid_argument", i18n.M("service.err.timeline.commit_omitted"))
	}
	commits := in.Commits
	omitted := min(in.Omitted, maxTimelineCommitOmitted)
	if len(commits) > MaxTimelineCommits {
		omitted = min(omitted+len(commits)-MaxTimelineCommits, maxTimelineCommitOmitted)
		commits = commits[:MaxTimelineCommits]
	}
	if len(commits) == 0 && omitted == 0 {
		return nil, errm(Invalid, "invalid_argument", i18n.M("service.err.timeline.commit_empty"))
	}
	res := &TimelineCommitsResult{Recorded: len(commits), Omitted: omitted}
	details := make([]map[string]any, 0, len(commits)+1)
	for _, c := range commits {
		sha := strings.ToLower(strings.TrimSpace(c.SHA))
		if !timelineCommitSHARe.MatchString(sha) {
			return nil, errm(Invalid, "invalid_argument", i18n.M("service.err.timeline.commit_sha"))
		}
		d := map[string]any{"branch": branch, "sha": sha}
		if subject, ok := timelineCommitSubject(c.Subject); ok {
			d["subject"] = subject
		} else {
			d["subject_withheld"] = true
			res.Withheld++
		}
		details = append(details, d)
	}
	if omitted > 0 {
		details = append(details, map[string]any{"branch": branch, "omitted": omitted})
	}
	u, p, role, err := s.memoryAccess(ctx, a, strings.TrimSpace(in.Project))
	if err != nil {
		return nil, err
	}
	if !canSendMessage(role) {
		return nil, errm(Forbidden, "forbidden", i18n.M("service.err.timeline.forbidden", "slug", p.Slug))
	}
	now := s.Now()
	if err := store.EnsureTimelinePostHead(ctx, s.DB, p.ID, now); err != nil {
		return nil, err
	}
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		if err := checkProjectActive(ctx, tx, p); err != nil {
			return err
		}
		for _, d := range details {
			if err := queueTimeline(tx, timelineSource{Origin: timelineFromCommit, ProjectID: p.ID, UserID: u.ID, Session: a.SessionID, TokenID: a.TokenID,
				Via: a.Via, At: now, Detail: d}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}
