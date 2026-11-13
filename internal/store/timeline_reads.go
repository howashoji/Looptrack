package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// タイムラインを読む側（docs/server/DESIGN.md §9-9「タイムラインの読み飛ばしを無くす仕組み」）。
// 読んだ位置（timeline_reads）は追記専用で、進むたびに行を足し、最大の値を読む。未読・読む窓・自分のセッションを外す規則は
// service が持つ。ここは条件の SQL と読み書きだけを置く。

// TimelineReadSeq は利用者のセッションが読んだ位置（受け取った通し番号の最大）を返す。行が無ければ ok が false。
func TimelineReadSeq(ctx context.Context, q execQuerier, projectID, userID int64, session string) (seq int64, ok bool, err error) {
	var v sql.NullInt64
	err = q.QueryRowContext(ctx, "SELECT MAX(seq) FROM timeline_reads WHERE project_id = ? AND user_id = ? AND session_id = ?",
		projectID, userID, session).Scan(&v)
	return v.Int64, v.Valid, err
}

// InsertTimelineRead は読んだ位置の行を 1 行足す（追記専用。位置を戻さないかは呼ぶ側が確かめる）。
func InsertTimelineRead(ctx context.Context, q execQuerier, projectID, userID int64, session string, seq int64, now time.Time) error {
	_, err := q.ExecContext(ctx, "INSERT INTO timeline_reads (project_id, user_id, session_id, seq, created_at) VALUES (?, ?, ?, ?, ?)",
		projectID, userID, truncate(session, 128), seq, now)
	return err
}

// TimelineFeedFilter は未読を読む条件。通し番号が After より大きく UpTo 以下で、Since 以降に書かれたもの。
// ExcludeUser が 0 でなければ、その利用者の ExcludeSession が起こしたもの（自分のセッション）を外す。
type TimelineFeedFilter struct {
	ProjectID      int64
	After          int64
	UpTo           int64
	Since          time.Time
	ExcludeUser    int64
	ExcludeSession string
}

func (f TimelineFeedFilter) cond(seqCol, atCol, userCol, sessionCol string) (string, []any) {
	cond := seqCol + " > ? AND " + seqCol + " <= ?"
	args := []any{f.After, f.UpTo}
	if !f.Since.IsZero() {
		cond += " AND " + atCol + " >= ?"
		args = append(args, f.Since)
	}
	if f.ExcludeUser != 0 {
		cond += " AND NOT (" + userCol + " = ? AND " + sessionCol + " = ?)"
		args = append(args, f.ExcludeUser, f.ExcludeSession)
	}
	return cond, args
}

// order は古い順か、新しい順（newest）。
func feedOrder(col string, newest bool) string {
	if newest {
		return " ORDER BY " + col + " DESC"
	}
	return " ORDER BY " + col
}

// TimelineEventsIn は条件に当たる自動の出来事を limit 件まで返す（newest なら新しいほうから。並びもその順）。
func TimelineEventsIn(ctx context.Context, q execQuerier, f TimelineFeedFilter, limit int, newest bool) ([]TimelineEvent, error) {
	cond, args := f.cond("e.seq", "e.created_at", "e.actor_user_id", "e.actor_session")
	rows, err := q.QueryContext(ctx, `SELECT e.id, e.project_id, e.seq, e.kind, e.actor_user_id, COALESCE(u.login, ''), e.actor_machine, e.actor_session,
  COALESCE(e.issue_id, 0), COALESCE(i.display_id, ''), e.detail, COALESCE(e.token_id, 0), e.via, e.created_at
FROM timeline_events e
LEFT JOIN users u ON u.id = e.actor_user_id
LEFT JOIN issues i ON i.id = e.issue_id
WHERE e.project_id = ? AND `+cond+feedOrder("e.seq", newest)+" LIMIT ?", append(append([]any{f.ProjectID}, args...), limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TimelineEvent{}
	for rows.Next() {
		var e TimelineEvent
		var detail []byte
		if err := rows.Scan(&e.ID, &e.ProjectID, &e.Seq, &e.Kind, &e.ActorUserID, &e.ActorLogin, &e.ActorMachine, &e.ActorSession,
			&e.IssueID, &e.IssueDisplay, &detail, &e.TokenID, &e.Via, &e.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(detail, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountTimelineEventsIn は条件に当たる自動の出来事の件数。
func CountTimelineEventsIn(ctx context.Context, q execQuerier, f TimelineFeedFilter) (int, error) {
	cond, args := f.cond("e.seq", "e.created_at", "e.actor_user_id", "e.actor_session")
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM timeline_events e WHERE e.project_id = ? AND "+cond, append([]any{f.ProjectID}, args...)...).Scan(&n)
	return n, err
}

// TimelineUpdate は近況（update）の投稿 1 件（近況は改訂を持たないので、投稿の出来事 1 行と同じ）。
type TimelineUpdate struct {
	PostID       int64
	Seq          int64
	UserID       int64
	UserLogin    string
	Machine      string
	Session      string
	IssueDisplay string
	Body         string
	CreatedAt    time.Time
}

const timelineUpdateFrom = ` FROM timeline_post_events e JOIN timeline_posts b ON b.id = e.post_id
WHERE e.project_id = ? AND b.kind = '` + TimelinePostUpdate + `' AND e.action = '` + TimelinePostActionPost + `' AND `

// TimelineUpdatesIn は条件に当たる近況を limit 件まで返す（newest なら新しいほうから。並びもその順）。
func TimelineUpdatesIn(ctx context.Context, q execQuerier, f TimelineFeedFilter, limit int, newest bool) ([]TimelineUpdate, error) {
	cond, args := f.cond("e.seq", "e.created_at", "e.actor_user_id", "e.actor_session")
	rows, err := q.QueryContext(ctx, `SELECT b.id, e.seq, e.actor_user_id, COALESCE(u.login, ''), e.actor_machine, e.actor_session, COALESCE(i.display_id, ''), e.body, e.created_at
FROM timeline_post_events e JOIN timeline_posts b ON b.id = e.post_id
LEFT JOIN users u ON u.id = e.actor_user_id
LEFT JOIN issues i ON i.id = b.issue_id
WHERE e.project_id = ? AND b.kind = '`+TimelinePostUpdate+`' AND e.action = '`+TimelinePostActionPost+`' AND `+cond+feedOrder("e.seq", newest)+" LIMIT ?",
		append(append([]any{f.ProjectID}, args...), limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TimelineUpdate{}
	for rows.Next() {
		var u TimelineUpdate
		if err := rows.Scan(&u.PostID, &u.Seq, &u.UserID, &u.UserLogin, &u.Machine, &u.Session, &u.IssueDisplay, &u.Body, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// CountTimelineUpdatesIn は条件に当たる近況の件数。
func CountTimelineUpdatesIn(ctx context.Context, q execQuerier, f TimelineFeedFilter) (int, error) {
	cond, args := f.cond("e.seq", "e.created_at", "e.actor_user_id", "e.actor_session")
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*)"+timelineUpdateFrom+cond, append([]any{f.ProjectID}, args...)...).Scan(&n)
	return n, err
}

// TimelineSessionKey は利用者とセッションの組。
type TimelineSessionKey struct {
	UserID  int64
	Session string
}

// LatestTimelineUpdates は keys のセッションごとに、最新の近況を 1 件ずつ返す（since より前なら返さない）。
// 件数の上限で切らないので、ほかのセッションが近況を書き続けても古いセッションの最新が欠けない。
func LatestTimelineUpdates(ctx context.Context, q execQuerier, projectID int64, keys []TimelineSessionKey, since time.Time) ([]TimelineUpdate, error) {
	if len(keys) == 0 {
		return []TimelineUpdate{}, nil
	}
	who := make([]string, len(keys))
	args := []any{projectID}
	for i, k := range keys {
		who[i] = "(e.actor_user_id = ? AND e.actor_session = ?)"
		args = append(args, k.UserID, k.Session)
	}
	query := `SELECT b.id, e.seq, e.actor_user_id, COALESCE(u.login, ''), e.actor_machine, e.actor_session, COALESCE(i.display_id, ''), e.body, e.created_at
FROM timeline_post_events e JOIN timeline_posts b ON b.id = e.post_id
LEFT JOIN users u ON u.id = e.actor_user_id
LEFT JOIN issues i ON i.id = b.issue_id
WHERE e.project_id = ? AND b.kind = '` + TimelinePostUpdate + `' AND e.action = '` + TimelinePostActionPost + `' AND (` + strings.Join(who, " OR ") + `)
  AND e.seq = (SELECT MAX(x.seq) FROM timeline_post_events x JOIN timeline_posts y ON y.id = x.post_id
    WHERE x.project_id = e.project_id AND y.kind = '` + TimelinePostUpdate + `' AND x.action = '` + TimelinePostActionPost + `'
      AND x.actor_user_id = e.actor_user_id AND x.actor_session = e.actor_session)`
	if !since.IsZero() {
		query += " AND e.created_at >= ?"
		args = append(args, since)
	}
	rows, err := q.QueryContext(ctx, query+" ORDER BY e.seq", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TimelineUpdate{}
	for rows.Next() {
		var u TimelineUpdate
		if err := rows.Scan(&u.PostID, &u.Seq, &u.UserID, &u.UserLogin, &u.Machine, &u.Session, &u.IssueDisplay, &u.Body, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
