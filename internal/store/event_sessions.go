package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// MCP の操作（issue_events）を、後から届いたスナップショットの会話に結ぶ記録（issue_event_sessions。追記専用）。
// 結ぶかどうかの規則は service.LinkMCPEventSessions に置く。ここは与えられた条件で読み書きするだけ。

// LinkSnapshot は結ぶ処理が読むスナップショットの項目。
type LinkSnapshot struct {
	ID          int64
	ProjectID   int64
	UserID      int64
	SessionID   string
	Trigger     string
	Via         string
	ReceivedAt  time.Time
	AttemptedAt time.Time // 0 なら再送でない
}

// UsageSnapshotByDedupe は dedupe_key の行を返す（重複で足さなかった再送も、元の行で結ぶため）。無ければ ErrNotFound。
func UsageSnapshotByDedupe(ctx context.Context, q execQuerier, dedupeKey string) (LinkSnapshot, error) {
	var s LinkSnapshot
	var via sql.NullString
	var attempted sql.NullTime
	err := q.QueryRowContext(ctx, `SELECT id, project_id, user_id, session_id, trigger_kind, via, received_at, attempted_at
FROM usage_snapshots WHERE dedupe_key = ?`, dedupeKey).Scan(&s.ID, &s.ProjectID, &s.UserID, &s.SessionID, &s.Trigger, &via, &s.ReceivedAt, &attempted)
	if errors.Is(err, sql.ErrNoRows) {
		return s, ErrNotFound
	}
	if err != nil {
		return s, err
	}
	s.Via = via.String
	if attempted.Valid {
		s.AttemptedAt = attempted.Time
	}
	return s, nil
}

// EventSessionLink は結ぶ条件（service が組み立てる）。
type EventSessionLink struct {
	Snapshot LinkSnapshot
	// UserID はスナップショットを送ってきた（認証した）利用者。この利用者のイベントだけを結ぶ
	// （スナップショットの行の利用者と同じかは service が確かめる）。
	UserID   int64
	ToolUse  string    // issue_events.detail の "tool_use" と一致させる値（ハッシュ）
	From, To time.Time // e.at がこの範囲（両端を含む）に入るイベントだけ
	// ConnPrefix は、サーバが発行した接続 ID を使ったセッション ID の印（service.MCPSessionPrefix）。
	// session_id が空か、この印で始まるイベントだけを結ぶ（クライアントが名乗ったセッション ID のイベントは結ばない）。
	ConnPrefix string
	LinkedAt   time.Time
}

// LinkEventSessions は条件に合う、まだ結ばれていないイベントをスナップショットの session_id に結び、結んだ件数を返す。
// 何度呼んでも結果は変わらない（結んだイベントは NOT EXISTS で外し、同時に結ぼうとして主キーが重複したものは読み捨てる）。
func LinkEventSessions(ctx context.Context, q execQuerier, l EventSessionLink) (int64, error) {
	res, err := q.ExecContext(ctx, `INSERT INTO issue_event_sessions (event_id, user_id, session_id, snapshot_id, linked_at)
SELECT e.id, e.actor_user_id, ?, ?, ?
FROM issue_events e
WHERE e.project_id = ? AND e.actor_user_id = ? AND e.via = 'mcp'
  AND e.at >= ? AND e.at <= ?
  AND JSON_UNQUOTE(JSON_EXTRACT(e.detail, '$.tool_use')) = ?
  AND (e.session_id IS NULL OR e.session_id = '' OR SUBSTR(e.session_id, 1, ?) = ?)
  AND NOT EXISTS (SELECT 1 FROM issue_event_sessions x WHERE x.event_id = e.id)`,
		l.Snapshot.SessionID, l.Snapshot.ID, l.LinkedAt.UTC(),
		l.Snapshot.ProjectID, l.UserID,
		l.From.UTC(), l.To.UTC(),
		l.ToolUse,
		len(l.ConnPrefix), l.ConnPrefix)
	if err != nil {
		if IsDuplicateKey(err) {
			return 0, nil
		}
		return 0, err
	}
	return res.RowsAffected()
}
