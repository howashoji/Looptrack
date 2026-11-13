package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// タイムラインの自動の出来事（timeline_events）と読んだ位置（timeline_reads）。マイグレーション 0019。docs/server/DESIGN.md §9-9。
// どちらも追記専用。何を載せるか・1 行に何を持たせるか・通し番号をいつ振るかは service が持つ。ここは読み書きだけを置く。

// タイムラインの出来事の種類（表の CHECK と同じ値）。
const (
	TimelineEventIssueCreate  = "issue_create"
	TimelineEventIssueStatus  = "issue_status"
	TimelineEventIssueAssign  = "issue_assign"
	TimelineEventIssueComment = "issue_comment"
	TimelineEventLeaseAcquire = "lease_acquire"
	TimelineEventLeaseRelease = "lease_release"
	TimelineEventLeaseBreak   = "lease_break"
	TimelineEventSessionStart = "session_start"
	TimelineEventSessionEnd   = "session_end"
	TimelineEventCommit       = "commit"
)

// TimelineEvent は自動の出来事 1 行。Detail は 1 行の文面の材料（文面は読む側で組み立てる）。
type TimelineEvent struct {
	ID           int64
	ProjectID    int64
	Seq          int64
	Kind         string
	ActorUserID  int64
	ActorLogin   string
	ActorMachine string
	ActorSession string
	IssueID      int64 // イシューの出来事でなければ 0
	IssueDisplay string
	Detail       map[string]any
	TokenID      int64
	Via          string
	CreatedAt    time.Time
}

// InsertTimelineEvent は出来事を 1 行足す（Seq は LockTimelineHead で同じトランザクションの中で取ったもの）。
func InsertTimelineEvent(ctx context.Context, q execQuerier, e TimelineEvent) error {
	detail := e.Detail
	if detail == nil {
		detail = map[string]any{}
	}
	b, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO timeline_events (project_id, seq, kind, actor_user_id, actor_machine, actor_session, issue_id, detail,
  token_id, via, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ProjectID, e.Seq, e.Kind, e.ActorUserID, truncate(e.ActorMachine, 64), truncate(e.ActorSession, 128), nullID(e.IssueID), string(b),
		nullID(e.TokenID), e.Via, e.CreatedAt)
	return err
}

// LockTimelineHead はプロジェクトの通し番号の行をロックして n 進め、進める前の値を返す（振る番号は戻り値 + 1 から戻り値 + n）。
// 行が無ければ作ってから握る（プロジェクトを作るときとマイグレーションで作るので、ふつうは通らない）。
// 出来事の行と同じトランザクションで、そのトランザクションの最後に 1 度だけ呼ぶ。行を握っている間はほかの出来事を書けないので、
// 番号は確定の順に並び、重ならず欠けない。
func LockTimelineHead(ctx context.Context, tx *sql.Tx, projectID int64, n int, now time.Time) (int64, error) {
	return LockTimelineHeadThen(ctx, tx, projectID, now, func() (int, error) { return n, nil })
}

// LockTimelineHeadThen は LockTimelineHead と同じだが、進める数を行を握った後に count で決める（握っている間に読んだ既存の行で、
// 重ねて書かないものを外すため）。握っている間はほかの取引が出来事を書けない。count の中の読みは、待っている間に先の取引が
// 確定させた行も見えるよう、ロックつきの読みにする（TimelineCommitSHAs）。
func LockTimelineHeadThen(ctx context.Context, tx *sql.Tx, projectID int64, now time.Time, count func() (int, error)) (int64, error) {
	var seq int64
	err := tx.QueryRowContext(ctx, "SELECT seq FROM timeline_heads WHERE project_id = ? FOR UPDATE", projectID).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		if err = EnsureTimelinePostHead(ctx, tx, projectID, now); err != nil {
			return 0, err
		}
		err = tx.QueryRowContext(ctx, "SELECT seq FROM timeline_heads WHERE project_id = ? FOR UPDATE", projectID).Scan(&seq)
	}
	if err != nil {
		return 0, err
	}
	n, err := count()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE timeline_heads SET seq = ?, updated_at = ? WHERE project_id = ?", seq+int64(n), now, projectID); err != nil {
		return 0, err
	}
	return seq, nil
}

// TimelineCommitSHAs は since 以降にプロジェクトへ載ったコミットの出来事の SHA の集合（重ねて載せないための確かめ）。
// detail は MySQL では JSON 型で文字列の形が方言で変わるので、行を読んでから Go で解く。
func TimelineCommitSHAs(ctx context.Context, q execQuerier, projectID int64, since time.Time) (map[string]bool, error) {
	// ロックつきの読み（FOR SHARE）にする。MySQL の REPEATABLE READ では、ロックの無い読みは取引の最初の読みの時点の
	// 写しを見るので、通し番号の行を待っている間に先の取引が確定させた行が見えない。SQLite では方言の書き換えで外れる
	rows, err := q.QueryContext(ctx, "SELECT detail FROM timeline_events WHERE project_id = ? AND created_at >= ? AND kind = ? FOR SHARE",
		projectID, since, TimelineEventCommit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var d struct {
			SHA string `json:"sha"`
		}
		if json.Unmarshal(raw, &d) == nil && d.SHA != "" {
			out[d.SHA] = true
		}
	}
	return out, rows.Err()
}

// ShareLockProject はプロジェクトの行を共有ロックで読む。出来事の行は projects を外部キーで指し、挿入のときに同じ共有ロックを取る。
// 通し番号の行を握った後にそれを取ると、projects の行を先に排他で握る起票（採番）と逆の順になるので、通し番号の前に取っておく。
func ShareLockProject(ctx context.Context, tx *sql.Tx, projectID int64) error {
	var id int64
	err := tx.QueryRowContext(ctx, "SELECT id FROM projects WHERE id = ? FOR SHARE", projectID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// PresenceMachineLabel は利用者のセッションの在席の端末のラベルを返す（行が無ければ空）。
func PresenceMachineLabel(ctx context.Context, q execQuerier, projectID, userID int64, sessionID string) (string, error) {
	var label string
	err := q.QueryRowContext(ctx, "SELECT machine_label FROM session_presence WHERE project_id = ? AND user_id = ? AND session_id = ?",
		projectID, userID, sessionID).Scan(&label)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return label, err
}

// IssueHeadline はイシューの表示の ID・種類・タイトル（出来事の 1 行の材料）。
type IssueHeadline struct {
	Display string
	Type    string
	Title   string
}

// IssueHeadlines はイシューの表示の ID・種類・タイトルを id ごとに返す（無い id は入らない）。
func IssueHeadlines(ctx context.Context, q execQuerier, ids []int64) (map[int64]IssueHeadline, error) {
	out := map[int64]IssueHeadline{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := q.QueryContext(ctx, "SELECT id, display_id, type, title FROM issues WHERE id IN (?"+strings.Repeat(", ?", len(ids)-1)+")", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var h IssueHeadline
		if err := rows.Scan(&id, &h.Display, &h.Type, &h.Title); err != nil {
			return nil, err
		}
		out[id] = h
	}
	return out, rows.Err()
}

// TimelineEventsAfter はプロジェクトの出来事のうち、通し番号が after より大きいものを番号の順に返す（limit は件数の上限）。
func TimelineEventsAfter(ctx context.Context, q execQuerier, projectID, after int64, limit int) ([]TimelineEvent, error) {
	rows, err := q.QueryContext(ctx, `SELECT e.id, e.project_id, e.seq, e.kind, e.actor_user_id, COALESCE(u.login, ''), e.actor_machine, e.actor_session,
  COALESCE(e.issue_id, 0), COALESCE(i.display_id, ''), e.detail, COALESCE(e.token_id, 0), e.via, e.created_at
FROM timeline_events e
LEFT JOIN users u ON u.id = e.actor_user_id
LEFT JOIN issues i ON i.id = e.issue_id
WHERE e.project_id = ? AND e.seq > ? ORDER BY e.seq LIMIT ?`, projectID, after, limit)
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
