package store

import (
	"context"
	"database/sql"
	"time"
)

// トークンレポートの作成依頼（usage_report_requests。追記専用）。
// 完了は依頼の行を書き換えず、台帳 usage_reports の request_id がその依頼を指していることで判定する。

// UsageRequest は依頼の 1 行と、完了していればその台帳の行。
type UsageRequest struct {
	ID               int64
	ProjectID        int64
	SinceLast        bool
	PeriodFrom       *time.Time // since_last のときは nil
	PeriodTo         *time.Time // nil は作成するときの今まで
	Target           string
	Note             string
	RequestedBy      int64
	RequestedByLogin string // 読むときだけ
	TokenID          int64
	Via              string
	CreatedAt        time.Time // 読むときだけ（登録時は InsertUsageRequest の now）
	// 完了（台帳に request_id つきの行がある）。読むときだけ
	ReportID        int64
	ReportName      string
	ReportCreatedAt time.Time
}

// Done は台帳に登録済み（完了）か。
func (r UsageRequest) Done() bool { return r.ReportID != 0 }

// InsertUsageRequest は依頼を 1 行足す。
func InsertUsageRequest(ctx context.Context, q execQuerier, r UsageRequest, now time.Time) (int64, error) {
	var from, to any
	if r.PeriodFrom != nil {
		from = r.PeriodFrom.UTC()
	}
	if r.PeriodTo != nil {
		to = r.PeriodTo.UTC()
	}
	res, err := q.ExecContext(ctx, `INSERT INTO usage_report_requests (project_id, since_last, period_from, period_to, target, note,
  requested_by, token_id, via, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ProjectID, r.SinceLast, from, to, r.Target, nullStr(r.Note), r.RequestedBy, nullInt(r.TokenID), r.Via, now.UTC())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func queryUsageRequests(ctx context.Context, q execQuerier, where string, args ...any) ([]UsageRequest, error) {
	rows, err := q.QueryContext(ctx, `SELECT q.id, q.project_id, q.since_last, q.period_from, q.period_to, q.target, COALESCE(q.note, ''),
  q.requested_by, COALESCE(u.login, ''), COALESCE(q.token_id, 0), q.via, q.created_at,
  COALESCE(r.id, 0), COALESCE(r.name, ''), r.created_at
FROM usage_report_requests q
LEFT JOIN users u ON u.id = q.requested_by
LEFT JOIN usage_reports r ON r.request_id = q.id
WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageRequest
	for rows.Next() {
		var r UsageRequest
		var from, to, done sql.NullTime
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.SinceLast, &from, &to, &r.Target, &r.Note, &r.RequestedBy, &r.RequestedByLogin,
			&r.TokenID, &r.Via, &r.CreatedAt, &r.ReportID, &r.ReportName, &done); err != nil {
			return nil, err
		}
		if from.Valid {
			t := from.Time
			r.PeriodFrom = &t
		}
		if to.Valid {
			t := to.Time
			r.PeriodTo = &t
		}
		if done.Valid {
			r.ReportCreatedAt = done.Time
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UsageRequests はプロジェクトの依頼を新しい順に返す。openOnly なら未完了（台帳に request_id の行が無い）だけ。
func UsageRequests(ctx context.Context, q execQuerier, projectID int64, openOnly bool) ([]UsageRequest, error) {
	where := `q.project_id = ?`
	if openOnly {
		where += ` AND r.id IS NULL`
	}
	return queryUsageRequests(ctx, q, where+` ORDER BY q.id DESC`, projectID)
}

// UsageRequestByID はプロジェクトの依頼 1 件（無ければ ErrNotFound）。
func UsageRequestByID(ctx context.Context, q execQuerier, projectID, id int64) (UsageRequest, error) {
	list, err := queryUsageRequests(ctx, q, `q.project_id = ? AND q.id = ?`, projectID, id)
	if err != nil {
		return UsageRequest{}, err
	}
	if len(list) == 0 {
		return UsageRequest{}, ErrNotFound
	}
	return list[0], nil
}
