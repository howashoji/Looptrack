package store

import (
	"context"
	"time"
)

// 在席（session_presence。マイグレーション 0016。docs/server/DESIGN.md §9-9）。
// 利用者とセッションごとの 1 行で、hook が送るたびに書き換える。動いているかの判定・権限・検査は service が持つ。ここは読み書きだけを置く。

// Presence は 1 つのセッションの在席（session_presence の 1 行）。
type Presence struct {
	ProjectID    int64
	UserID       int64
	UserLogin    string // 読むときに users から引いた login
	SessionID    string
	Agent        string
	MachineLabel string
	Branch       string
	HeadSHA      string
	Directory    string
	LastSeenAt   time.Time
}

// UpsertPresence は在席の行を足すか書き換える。キーに利用者が入るので、他人のセッション ID を名乗っても他人の行は変わらない。
func UpsertPresence(ctx context.Context, q execQuerier, p Presence) error {
	now := p.LastSeenAt.UTC()
	_, err := q.ExecContext(ctx, `INSERT INTO session_presence (project_id, user_id, session_id, agent, machine_label, branch, head_sha, directory, created_at, last_seen_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE agent = VALUES(agent), machine_label = VALUES(machine_label), branch = VALUES(branch), head_sha = VALUES(head_sha),
  directory = VALUES(directory), last_seen_at = VALUES(last_seen_at)`,
		p.ProjectID, p.UserID, p.SessionID, p.Agent, p.MachineLabel, p.Branch, p.HeadSHA, p.Directory, now, now)
	return err
}

// DeletePresence は利用者のセッションの在席を消す（無ければ何もしない）。
func DeletePresence(ctx context.Context, q execQuerier, projectID, userID int64, sessionID string) error {
	_, err := q.ExecContext(ctx, "DELETE FROM session_presence WHERE project_id = ? AND user_id = ? AND session_id = ?", projectID, userID, sessionID)
	return err
}

// TrimPresence は利用者の行のうち、新しいほうから keep 行（sessionID の行を含む）より後ろを消す。
func TrimPresence(ctx context.Context, q execQuerier, projectID, userID int64, sessionID string, keep int) error {
	rows, err := q.QueryContext(ctx, `SELECT id FROM session_presence WHERE project_id = ? AND user_id = ? AND session_id <> ?
ORDER BY last_seen_at DESC, id DESC LIMIT 1000 OFFSET ?`, projectID, userID, sessionID, keep-1)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, id := range ids {
		if _, err := q.ExecContext(ctx, "DELETE FROM session_presence WHERE id = ?", id); err != nil {
			return err
		}
	}
	return nil
}

// ActivePresence は since 以降に送ったセッションを新しい順に返す（行数は利用者ごとの上限で抑えてある）。
func ActivePresence(ctx context.Context, q execQuerier, projectID int64, since time.Time) ([]Presence, error) {
	rows, err := q.QueryContext(ctx, `SELECT p.project_id, p.user_id, u.login, p.session_id, p.agent, p.machine_label, p.branch, p.head_sha, p.directory, p.last_seen_at
FROM session_presence p JOIN users u ON u.id = p.user_id
WHERE p.project_id = ? AND p.last_seen_at >= ? ORDER BY p.last_seen_at DESC, p.id DESC`, projectID, since.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Presence
	for rows.Next() {
		var p Presence
		if err := rows.Scan(&p.ProjectID, &p.UserID, &p.UserLogin, &p.SessionID, &p.Agent, &p.MachineLabel, &p.Branch, &p.HeadSHA, &p.Directory, &p.LastSeenAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PurgePresence は before より前に最後に送った行を消す。
func PurgePresence(ctx context.Context, q execQuerier, before time.Time) error {
	_, err := q.ExecContext(ctx, "DELETE FROM session_presence WHERE last_seen_at < ?", before.UTC())
	return err
}
