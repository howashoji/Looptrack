package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// VerifyResult は verify の 1 コマンドの結果（issue_events kind verify の detail.results[]。DESIGN.md §5-8-3）。
type VerifyResult struct {
	Command    string `json:"command"`
	Status     string `json:"status"` // ok / fail / timeout / skipped
	ExitCode   *int   `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
	OutputTail string `json:"output_tail"`
	// Cached は出力に go test の結果キャッシュの印（(cached)）があった注記（無ければ省く。成否は変えない）
	Cached bool `json:"cached,omitempty"`
}

// VerifyDetail は issue_events kind verify の detail。
type VerifyDetail struct {
	BodySHA256 string         `json:"body_sha256"`
	OK         bool           `json:"ok"`
	Passed     int            `json:"passed"`
	Failed     int            `json:"failed"`
	Results    []VerifyResult `json:"results"`
	Host       string         `json:"host,omitempty"`
	Workspace  string         `json:"workspace,omitempty"`
	CommentSeq int            `json:"comment_seq,omitempty"`
	// SelfReported は MCP の report_verify から送られた記録（AI の自己申告）の印（CLI の記録には付かない）
	SelfReported bool `json:"self_reported,omitempty"`
	// Cached は results のどれかに結果キャッシュの印があった注記（無ければ省く。成否・件数は変えない）
	Cached bool `json:"cached,omitempty"`
}

// VerifyEvent は直近の verify の記録。
type VerifyEvent struct {
	At  time.Time
	Via string
	VerifyDetail
}

// LastVerify はイシューの直近 1 件の verify の記録を返す（無ければ nil）。
// 規則 verify.require_on_close は直近の 1 件だけを見る（§5-8-4。k_issue_events_issue_at を使う）。
func LastVerify(ctx context.Context, q execQuerier, issueID int64) (*VerifyEvent, error) {
	var ev VerifyEvent
	var detail []byte
	err := q.QueryRowContext(ctx, `SELECT at, via, detail FROM issue_events
 WHERE issue_id = ? AND kind = 'verify'
 ORDER BY at DESC, id DESC
 LIMIT 1`, issueID).Scan(&ev.At, &ev.Via, &detail)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(detail, &ev.VerifyDetail); err != nil {
		return nil, err
	}
	return &ev, nil
}

// SelfReportedInReview は In Review のイシューのうち、直近の verify の記録が MCP の自己申告（経路 mcp）のものの
// display_id を返す（summary の ② に印を付ける）。直近 1 件だけを見る（規則 verify.require_on_close と同じ）。
func SelfReportedInReview(ctx context.Context, q execQuerier, projectID int64) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT i.display_id, e.via
  FROM issues i
  JOIN issue_events e ON e.id = (
       SELECT e2.id FROM issue_events e2
        WHERE e2.issue_id = i.id AND e2.kind = 'verify'
        ORDER BY e2.at DESC, e2.id DESC
        LIMIT 1)
 WHERE i.project_id = ? AND i.status = 'In Review'`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id, via string
		if err := rows.Scan(&id, &via); err != nil {
			return nil, err
		}
		if via == "mcp" {
			out[id] = true
		}
	}
	return out, rows.Err()
}

// BaselineSections は、イシューの「本文の節ごとのハッシュ（detail.sections）を持つ最初の記録」の
// sections の JSON を返す（DESIGN.md §5-8-4）。無ければ nil（この仕組みより前に
// 起票されたイシューは create のイベントに sections が無いので、これから create / update される
// ものから効く）。中身の読み取りは domain.SectionHashes（規則は domain の 1 か所）。
//
// kind は create と update だけを見る（status・comment・verify は本文の節を変えない）。
// detail に列を足していないので、マイグレーションは要らない。
func BaselineSections(ctx context.Context, q execQuerier, issueID int64) ([]byte, error) {
	var raw []byte
	err := q.QueryRowContext(ctx, `SELECT JSON_EXTRACT(detail, '$.sections') FROM issue_events
 WHERE issue_id = ? AND kind IN ('create', 'update') AND JSON_EXTRACT(detail, '$.sections') IS NOT NULL
 ORDER BY at ASC, id ASC
 LIMIT 1`, issueID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return raw, nil
}
