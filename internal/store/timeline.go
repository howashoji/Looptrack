package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// タイムライン（timeline_posts・timeline_post_events・timeline_post_receipts・timeline_heads。マイグレーション 0017。docs/server/DESIGN.md §9-9）。
// 前の 3 つは追記専用で、timeline_heads だけを書き換える。いまの状態（最新の版・取り下げ済みか・期限・解除）と、
// 宛先の権限・本文の検査・保持期間の決定は service が持つ。ここは読み書きと、読める範囲の条件の SQL だけを置く。

// タイムラインの書き込みの種類（表の CHECK と同じ値）。update（近況）はマイグレーション 0019 で足した。
const (
	TimelinePostUpdate  = "update"
	TimelinePostInfo    = "info"
	TimelinePostRequest = "request"
	TimelinePostHold    = "hold"
)

// 読む時点で解決する宛先の種類（表の CHECK と同じ値。マイグレーション 0020）。宛先のセッションは書く時点では決めず、
// 読む時点で着手の記録（issue）と在席（branch・machine・others）から解決する。project・user・session は連絡と同じ MessageTo*。
const (
	TimelineToIssue   = "issue"
	TimelineToBranch  = "branch"
	TimelineToMachine = "machine"
	TimelineToOthers  = "others"
)

// TimelineResolvedTargets は読む時点で解決する宛先の種類。参加者の誰にでも当たりうるので、読める範囲は project 宛てと同じ（参加者全員）。
var TimelineResolvedTargets = []string{TimelineToIssue, TimelineToBranch, TimelineToMachine, TimelineToOthers}

// タイムラインの出来事の種類（表の CHECK と同じ値）。
const (
	TimelinePostActionPost     = "post"
	TimelinePostActionRevise   = "revise"
	TimelinePostActionWithdraw = "withdraw"
)

// 受け取りの記録の種類（表の CHECK と同じ値）。
const (
	TimelinePostReceive = "receive"
	TimelinePostAck     = "ack"
)

// TimelinePost はタイムラインの書き込みの変わらない部分と、読む時点の解除の条件の状態（添えたイシューの状態・占有の今の持ち主）。
type TimelinePost struct {
	ID        int64
	ProjectID int64
	Kind      string
	ToKind    string // MessageToProject / MessageToUser / MessageToSession / TimelineTo*
	ToUserID  int64  // project と、読む時点で解決する宛先は 0
	ToLogin   string
	ToSession string
	// ToIssueID・ToIssueDisplay は TimelineToIssue の宛先のイシュー（ほかは 0 と空）
	ToIssueID      int64
	ToIssueDisplay string
	// ToLabel は TimelineToBranch のブランチの名前か TimelineToMachine の端末のラベル（ほかは空）
	ToLabel       string
	SenderUserID  int64
	SenderLogin   string
	SenderMachine string
	SenderSession string
	IssueID       int64 // 添えていなければ 0
	IssueDisplay  string
	// ReleaseIssueID は解除の条件のイシュー（無ければ 0）。ReleaseIssueStatus は読んだ時点のその状態
	ReleaseIssueID      int64
	ReleaseIssueDisplay string
	ReleaseIssueStatus  string
	// ReleaseLease は解除の条件の占有の名前（無ければ空）。ReleaseLeaseAcquiredAt は出した時点の占有の取った時刻
	ReleaseLease           string
	ReleaseLeaseAcquiredAt time.Time
	// LeaseHolderUserID・LeaseAcquiredAt・LeaseExpiresAt は読んだ時点の占有の持ち主（行が無い・空いていれば 0 とゼロ値）
	LeaseHolderUserID int64
	LeaseAcquiredAt   time.Time
	LeaseExpiresAt    time.Time
	TokenID           int64
	Via               string
	CreatedAt         time.Time
}

// TimelinePostEvent はタイムラインの出来事（投稿・改訂・取り下げ）の 1 行。
type TimelinePostEvent struct {
	ID             int64
	TimelinePostID int64
	ProjectID      int64
	Seq            int64
	Action         string
	Version        int64
	Body           string
	// ExpiresAt はこの版の期限（無ければゼロ値）
	ExpiresAt    time.Time
	Reason       string
	ActorUserID  int64
	ActorLogin   string
	ActorMachine string
	ActorSession string
	TokenID      int64
	Via          string
	CreatedAt    time.Time
}

// TimelinePostReceipt はセッションごとの受け取りか了解の 1 行。
type TimelinePostReceipt struct {
	TimelinePostID int64
	UserID         int64
	UserLogin      string
	SessionID      string // 無ければ空文字
	Version        int64
	Kind           string // TimelinePostReceive / TimelinePostAck
	CreatedAt      time.Time
}

// TimelinePostMarks は 1 つの書き込みについて、あるセッションが受け取った・了解した出来事の番号の最大（無ければ 0）。
type TimelinePostMarks struct {
	Received int64
	Acked    int64
}

const timelinePostSelect = `SELECT b.id, b.project_id, b.kind, b.to_kind, COALESCE(b.to_user_id, 0), COALESCE(t.login, ''), b.to_session,
  COALESCE(b.to_issue_id, 0), COALESCE(ti.display_id, ''), b.to_label,
  b.sender_user_id, COALESCE(s.login, ''), b.sender_machine, b.sender_session, COALESCE(b.issue_id, 0), COALESCE(i.display_id, ''),
  COALESCE(b.release_issue_id, 0), COALESCE(ri.display_id, ''), COALESCE(ri.status, ''), b.release_lease, b.release_lease_acquired_at,
  COALESCE(l.holder_user_id, 0), l.acquired_at, l.expires_at, COALESCE(b.token_id, 0), b.via, b.created_at
FROM timeline_posts b
LEFT JOIN users t ON t.id = b.to_user_id
LEFT JOIN users s ON s.id = b.sender_user_id
LEFT JOIN issues i ON i.id = b.issue_id
LEFT JOIN issues ti ON ti.id = b.to_issue_id
LEFT JOIN issues ri ON ri.id = b.release_issue_id
LEFT JOIN leases l ON l.project_id = b.project_id AND l.name = b.release_lease AND b.release_lease <> ''`

func scanTimelinePost(row interface{ Scan(...any) error }) (TimelinePost, error) {
	var b TimelinePost
	var relAcq, leaseAcq, leaseExp sql.NullTime
	err := row.Scan(&b.ID, &b.ProjectID, &b.Kind, &b.ToKind, &b.ToUserID, &b.ToLogin, &b.ToSession, &b.ToIssueID, &b.ToIssueDisplay, &b.ToLabel,
		&b.SenderUserID, &b.SenderLogin,
		&b.SenderMachine, &b.SenderSession, &b.IssueID, &b.IssueDisplay, &b.ReleaseIssueID, &b.ReleaseIssueDisplay, &b.ReleaseIssueStatus,
		&b.ReleaseLease, &relAcq, &b.LeaseHolderUserID, &leaseAcq, &leaseExp, &b.TokenID, &b.Via, &b.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrNotFound
	}
	b.ReleaseLeaseAcquiredAt, b.LeaseAcquiredAt, b.LeaseExpiresAt = relAcq.Time, leaseAcq.Time, leaseExp.Time
	return b, err
}

// timelinePostReadable は読める条件（プロジェクトの中で、プロジェクト宛てか読む時点で解決する宛先か、自分が宛先の利用者か、自分が出したもの）。
// since がゼロ値でなければ、最後の出来事がそれより古い書き込みを外す（保持期間）。ただし最後の出来事が期限を持ち、
// その期限が now より後なら外さない。保持期間を期限の上限（7 日）より短くしても、有効な待機が期限の前に消えないようにするため。
// 最後の出来事が取り下げなら期限を持たないので、取り下げ済みは保持期間で切る。期限を持たない知らせと依頼も保持期間で切る。
func timelinePostReadable(projectID, userID int64, since, now time.Time) (string, []any) {
	cond := "b.project_id = ? AND (b.to_kind IN ('" + MessageToProject + "', '" + strings.Join(TimelineResolvedTargets, "', '") +
		"') OR b.to_user_id = ? OR b.sender_user_id = ?)"
	args := []any{projectID, userID, userID}
	if !since.IsZero() {
		cond += " AND (EXISTS (SELECT 1 FROM timeline_post_events z WHERE z.post_id = b.id AND z.created_at >= ?)" +
			" OR EXISTS (SELECT 1 FROM timeline_post_events x WHERE x.post_id = b.id AND x.expires_at > ?" +
			" AND x.version = (SELECT MAX(y.version) FROM timeline_post_events y WHERE y.post_id = b.id)))"
		args = append(args, since, now)
	}
	return cond, args
}

// InsertTimelinePost は書き込みの変わらない部分を 1 行足し、その id を返す。
func InsertTimelinePost(ctx context.Context, q execQuerier, b TimelinePost) (int64, error) {
	var relAcq any
	if !b.ReleaseLeaseAcquiredAt.IsZero() {
		relAcq = b.ReleaseLeaseAcquiredAt
	}
	res, err := q.ExecContext(ctx, `INSERT INTO timeline_posts (project_id, kind, to_kind, to_user_id, to_session, to_issue_id, to_label, sender_user_id,
  sender_machine, sender_session, issue_id, release_issue_id, release_lease, release_lease_acquired_at, token_id, via, created_at)
  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.ProjectID, b.Kind, b.ToKind, nullID(b.ToUserID), b.ToSession, nullID(b.ToIssueID), b.ToLabel, b.SenderUserID, b.SenderMachine, b.SenderSession,
		nullID(b.IssueID), nullID(b.ReleaseIssueID), b.ReleaseLease, relAcq, nullID(b.TokenID), b.Via, b.CreatedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// EnsureTimelinePostHead はプロジェクトの通し番号の行が無ければ 0 の行を作る。トランザクションの外（その文だけで確定する接続）か、
// 作ったばかりのプロジェクトの取引の中で呼ぶ。
// 行が無いまま FOR UPDATE すると主キーのギャップロックを取り、同時に書く 2 つのトランザクションが互いの INSERT を待って
// デッドロックになる。作るのと握るのを分ける（EnsureLeaseRow と同じ）。
func EnsureTimelinePostHead(ctx context.Context, q execQuerier, projectID int64, now time.Time) error {
	_, err := q.ExecContext(ctx, "INSERT IGNORE INTO timeline_heads (project_id, seq, updated_at) VALUES (?, 0, ?)", projectID, now)
	return err
}

// TimelinePostHeadSeq はプロジェクトの最後に振った通し番号を返す（まだ無ければ 0）。
func TimelinePostHeadSeq(ctx context.Context, q execQuerier, projectID int64) (int64, error) {
	var seq int64
	err := q.QueryRowContext(ctx, "SELECT seq FROM timeline_heads WHERE project_id = ?", projectID).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return seq, err
}

// InsertTimelinePostEvent は出来事を 1 行足す（追記専用。Seq は LockTimelineHead で同じトランザクションの中で取ったもの）。
func InsertTimelinePostEvent(ctx context.Context, q execQuerier, e TimelinePostEvent) error {
	var expires any
	if !e.ExpiresAt.IsZero() {
		expires = e.ExpiresAt
	}
	_, err := q.ExecContext(ctx, `INSERT INTO timeline_post_events (post_id, project_id, seq, action, version, body, expires_at, reason,
  actor_user_id, actor_machine, actor_session, token_id, via, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.TimelinePostID, e.ProjectID, e.Seq, e.Action, e.Version, e.Body, expires, e.Reason,
		e.ActorUserID, truncate(e.ActorMachine, 64), truncate(e.ActorSession, 128), nullID(e.TokenID), e.Via, e.CreatedAt)
	return err
}

// TimelinePostEventsOf は書き込み 1 件の出来事を番号の順に返す。改訂と取り下げはトランザクションの中で LockTimelineHead の後に呼ぶ
// （通し番号の行を握っているので、同じプロジェクトのほかの出来事は割り込まない）。
// 読みはロックつき（FOR SHARE）にする。REPEATABLE READ では取引の最初の普通の SELECT で読む版が決まり、head を待つ間に確定した
// 改訂を普通の読みでは見られないので、古い版から次の版の番号を数えて一意の索引に当たる。ロックつきの読みは確定済みの最新を読む。
func TimelinePostEventsOf(ctx context.Context, q execQuerier, timelinePostID int64) ([]TimelinePostEvent, error) {
	return queryTimelinePostEvents(ctx, q, timelinePostEventSelect+" WHERE e.post_id = ? ORDER BY e.version FOR SHARE", timelinePostID)
}

// ReadableTimelinePosts は利用者が読める書き込みを id の順に返す。id が 0 でなければその 1 件だけを見る。
// since と now は保持期間と期限の判定に使う（timelinePostReadable）。
func ReadableTimelinePosts(ctx context.Context, q execQuerier, projectID, userID int64, since, now time.Time, id int64) ([]TimelinePost, error) {
	cond, args := timelinePostReadable(projectID, userID, since, now)
	if id != 0 {
		cond += " AND b.id = ?"
		args = append(args, id)
	}
	rows, err := q.QueryContext(ctx, timelinePostSelect+" WHERE "+cond+" ORDER BY b.id", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TimelinePost{}
	for rows.Next() {
		b, err := scanTimelinePost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

const timelinePostEventSelect = `SELECT e.id, e.post_id, e.project_id, e.seq, e.action, e.version, e.body, e.expires_at, e.reason,
  e.actor_user_id, COALESCE(u.login, ''), e.actor_machine, e.actor_session, COALESCE(e.token_id, 0), e.via, e.created_at
FROM timeline_post_events e LEFT JOIN users u ON u.id = e.actor_user_id`

func queryTimelinePostEvents(ctx context.Context, q execQuerier, query string, args ...any) ([]TimelinePostEvent, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TimelinePostEvent{}
	for rows.Next() {
		var e TimelinePostEvent
		var expires sql.NullTime
		if err := rows.Scan(&e.ID, &e.TimelinePostID, &e.ProjectID, &e.Seq, &e.Action, &e.Version, &e.Body, &expires, &e.Reason,
			&e.ActorUserID, &e.ActorLogin, &e.ActorMachine, &e.ActorSession, &e.TokenID, &e.Via, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.ExpiresAt = expires.Time
		out = append(out, e)
	}
	return out, rows.Err()
}

// ReadableTimelinePostEvents は利用者が読める書き込みの出来事を、書き込みと番号の順に返す。id が 0 でなければその 1 件だけを見る。
func ReadableTimelinePostEvents(ctx context.Context, q execQuerier, projectID, userID int64, since, now time.Time, id int64) ([]TimelinePostEvent, error) {
	cond, args := timelinePostReadable(projectID, userID, since, now)
	if id != 0 {
		cond += " AND b.id = ?"
		args = append(args, id)
	}
	return queryTimelinePostEvents(ctx, q, timelinePostEventSelect+" WHERE e.post_id IN (SELECT b.id FROM timeline_posts b WHERE "+cond+
		") ORDER BY e.post_id, e.version", args...)
}

// TimelinePostEventsAfter は利用者が読める書き込みの出来事のうち、通し番号が after より大きいものを番号の順に limit 件まで返す。
// 保持期間はほかの読みと同じく書き込みごとに切る（timelinePostReadable）。読める書き込みの出来事は、古い版のものも返す。
func TimelinePostEventsAfter(ctx context.Context, q execQuerier, projectID, userID int64, since, now time.Time, after int64, limit int) ([]TimelinePostEvent, error) {
	cond, args := timelinePostReadable(projectID, userID, since, now)
	all := append([]any{projectID, after}, args...)
	query := timelinePostEventSelect + " WHERE e.project_id = ? AND e.seq > ? AND e.post_id IN (SELECT b.id FROM timeline_posts b WHERE " + cond + ")"
	return queryTimelinePostEvents(ctx, q, query+" ORDER BY e.seq LIMIT ?", append(all, limit)...)
}

// TimelinePostMarksOf はプロジェクトの書き込みについて、利用者のセッションが受け取った・了解した出来事の番号の最大を返す。
func TimelinePostMarksOf(ctx context.Context, q execQuerier, projectID, userID int64, session string) (map[int64]TimelinePostMarks, error) {
	rows, err := q.QueryContext(ctx, `SELECT r.post_id, r.kind, MAX(r.version) FROM timeline_post_receipts r JOIN timeline_posts b ON b.id = r.post_id
WHERE b.project_id = ? AND r.user_id = ? AND r.session_id = ? GROUP BY r.post_id, r.kind`, projectID, userID, session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]TimelinePostMarks{}
	for rows.Next() {
		var id, v int64
		var kind string
		if err := rows.Scan(&id, &kind, &v); err != nil {
			return nil, err
		}
		m := out[id]
		if kind == TimelinePostAck {
			m.Acked = v
		} else {
			m.Received = v
		}
		out[id] = m
	}
	return out, rows.Err()
}

// InsertTimelinePostReceipts は受け取りか了解の行を足し、足した件数を返す。同じ行の 2 度目は何も足さない。
func InsertTimelinePostReceipts(ctx context.Context, q execQuerier, rs []TimelinePostReceipt) (int64, error) {
	if len(rs) == 0 {
		return 0, nil
	}
	values := make([]string, len(rs))
	vals := make([]any, 0, len(rs)*6)
	for i, r := range rs {
		values[i] = "(?, ?, ?, ?, ?, ?)"
		vals = append(vals, r.TimelinePostID, r.UserID, r.SessionID, r.Version, r.Kind, r.CreatedAt)
	}
	res, err := q.ExecContext(ctx, "INSERT IGNORE INTO timeline_post_receipts (post_id, user_id, session_id, version, kind, created_at) VALUES "+
		strings.Join(values, ", "), vals...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// TimelinePostIssueStatus はイシューの今の状態を返す（解除の条件に添えるときに、既にクローズしていないかを確かめる）。
func TimelinePostIssueStatus(ctx context.Context, q execQuerier, issueID int64) (string, error) {
	var st string
	err := q.QueryRowContext(ctx, "SELECT status FROM issues WHERE id = ?", issueID).Scan(&st)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return st, err
}

// TimelinePostReceiptsOf は書き込みの受け取りと了解の行を、利用者・セッション・番号の順に返す（到達の状況）。
func TimelinePostReceiptsOf(ctx context.Context, q execQuerier, timelinePostID int64) ([]TimelinePostReceipt, error) {
	rows, err := q.QueryContext(ctx, `SELECT r.post_id, r.user_id, COALESCE(u.login, ''), r.session_id, r.version, r.kind, r.created_at
FROM timeline_post_receipts r LEFT JOIN users u ON u.id = r.user_id WHERE r.post_id = ? ORDER BY r.user_id, r.session_id, r.version, r.kind`, timelinePostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TimelinePostReceipt{}
	for rows.Next() {
		var r TimelinePostReceipt
		if err := rows.Scan(&r.TimelinePostID, &r.UserID, &r.UserLogin, &r.SessionID, &r.Version, &r.Kind, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
