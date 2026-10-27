package store

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// DigestEvent はイシューの最新のセッション要約（DESIGN.md §9-7）1 件。
type DigestEvent struct {
	DisplayID string
	UserID    int64
	SessionID string // issue_events.session_id（記録したままの値）
	// LinkedSessionID は、MCP の要約を後から届いたスナップショットの会話に結んだ値（issue_event_sessions。無ければ空）。
	// どちらで比べるかは service が決める（Starter と同じ）
	LinkedSessionID string
	At              time.Time
	Head            string // コメントの本文の先頭 DigestHeadChars 文字（抜粋とラベルは service が作る）
}

// DigestHeadChars は LatestDigests が読むコメントの本文の先頭の文字数（抜粋の上限より十分に長く、本文の全件は読まない）。
const DigestHeadChars = 1200

// LatestDigests は、プロジェクトのイシュー（表示 ID の一覧。空なら何も引かない）ごとに、最新のセッション要約を返す。
// 要約の印は記録（issue_events）の kind 'comment' の detail.kind = "digest"（service.AddComment が付ける）で、
// その最大の id の記録を最新とし、detail.seq でコメントの本文を引く。要約が無いイシューは含めない。
// 先頭語の LIKE で comments を探さないのは、規則（domain.LeadWord）を SQL に写さずに済むからで、
// 要約は状態の変更と同時には受けない（digest_on_status）ので kind 'status' の記録は見ない。
func LatestDigests(ctx context.Context, q execQuerier, projectID int64, displayIDs []string) ([]DigestEvent, error) {
	if len(displayIDs) == 0 {
		return nil, nil
	}
	args := []any{projectID}
	for _, id := range displayIDs {
		args = append(args, id)
	}
	rows, err := q.QueryContext(ctx, `SELECT i.display_id, COALESCE(e.actor_user_id, 0), COALESCE(e.session_id, ''), COALESCE(l.session_id, ''), e.at,
       LEFT(c.content, `+strconv.Itoa(DigestHeadChars)+`)
FROM issues i
JOIN issue_events e ON e.id = (
  SELECT MAX(x.id) FROM issue_events x
   WHERE x.issue_id = i.id AND x.kind = 'comment' AND JSON_UNQUOTE(JSON_EXTRACT(x.detail, '$.kind')) = 'digest')
JOIN comments c ON c.issue_id = i.id AND c.seq = JSON_UNQUOTE(JSON_EXTRACT(e.detail, '$.seq'))
LEFT JOIN issue_event_sessions l ON l.event_id = e.id
WHERE i.project_id = ? AND i.display_id IN (?`+strings.Repeat(", ?", len(displayIDs)-1)+`)
ORDER BY i.display_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DigestEvent
	for rows.Next() {
		var d DigestEvent
		if err := rows.Scan(&d.DisplayID, &d.UserID, &d.SessionID, &d.LinkedSessionID, &d.At, &d.Head); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
