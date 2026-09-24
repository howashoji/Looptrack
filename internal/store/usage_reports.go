package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/howashoji/looptrack/internal/usage"
)

// トークンレポートの台帳（usage_reports。追記専用）と、レポートの集計に使うスナップショットの読み出し。

// ErrDuplicateName は同じプロジェクトに同じ名前の行があること。
var ErrDuplicateName = errors.New("duplicate name")

// ErrDuplicateRequest は同じ作成依頼（request_id）の行が既にあること（1 依頼に台帳 1 行まで）。
var ErrDuplicateRequest = errors.New("duplicate request")

// UsageReport は台帳の 1 行。
type UsageReport struct {
	ID                    int64
	ProjectID             int64
	Name                  string
	PeriodFrom            *time.Time // nil は最初のデータから
	PeriodTo              time.Time
	DataEnd               time.Time
	ExcludedConversations []string
	TotalTokens           *int64
	Note                  string
	RequestID             int64 // 0 は依頼なし
	CreatedBy             int64
	CreatedByLogin        string // 読むときだけ
	TokenID               int64
	Via                   string
	CreatedAt             time.Time
	RecordedAt            time.Time // 読むときだけ
}

// InsertUsageReport は台帳に 1 行足す。同じ名前が既にあれば ErrDuplicateName。
func InsertUsageReport(ctx context.Context, q execQuerier, r UsageReport) (int64, error) {
	excluded, err := json.Marshal(nonNilStrings(r.ExcludedConversations))
	if err != nil {
		return 0, err
	}
	var from, total any
	if r.PeriodFrom != nil {
		from = r.PeriodFrom.UTC()
	}
	if r.TotalTokens != nil {
		total = *r.TotalTokens
	}
	res, err := q.ExecContext(ctx, `INSERT INTO usage_reports (project_id, name, period_from, period_to, data_end,
  excluded_conversations, total_tokens, note, request_id, created_by, token_id, via, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ProjectID, r.Name, from, r.PeriodTo.UTC(), r.DataEnd.UTC(), excluded, total, nullStr(r.Note),
		nullInt(r.RequestID), r.CreatedBy, nullInt(r.TokenID), r.Via, r.CreatedAt.UTC())
	if err != nil {
		if IsDuplicateKey(err) {
			if isDuplicateOn(err, "uk_usage_reports_request", "usage_reports.request_id") {
				return 0, ErrDuplicateRequest
			}
			return 0, ErrDuplicateName
		}
		return 0, err
	}
	return res.LastInsertId()
}

const usageReportColumns = `r.id, r.project_id, r.name, r.period_from, r.period_to, r.data_end, r.excluded_conversations, r.total_tokens,
  COALESCE(r.note, ''), COALESCE(r.request_id, 0), r.created_by, COALESCE(u.login, ''), COALESCE(r.token_id, 0), r.via, r.created_at, r.recorded_at`

func queryUsageReports(ctx context.Context, q execQuerier, where string, args ...any) ([]UsageReport, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+usageReportColumns+`
FROM usage_reports r LEFT JOIN users u ON u.id = r.created_by
WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageReport
	for rows.Next() {
		var r UsageReport
		var from sql.NullTime
		var excluded []byte
		var total sql.NullInt64
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.Name, &from, &r.PeriodTo, &r.DataEnd, &excluded, &total,
			&r.Note, &r.RequestID, &r.CreatedBy, &r.CreatedByLogin, &r.TokenID, &r.Via, &r.CreatedAt, &r.RecordedAt); err != nil {
			return nil, err
		}
		if from.Valid {
			t := from.Time
			r.PeriodFrom = &t
		}
		if total.Valid {
			v := total.Int64
			r.TotalTokens = &v
		}
		r.ExcludedConversations = []string{}
		if len(excluded) > 0 {
			if err := json.Unmarshal(excluded, &r.ExcludedConversations); err != nil {
				return nil, err
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UsageReports はプロジェクトの台帳を新しいデータ終端の順に返す。
func UsageReports(ctx context.Context, q execQuerier, projectID int64) ([]UsageReport, error) {
	return queryUsageReports(ctx, q, `r.project_id = ? ORDER BY r.data_end DESC, r.id DESC`, projectID)
}

// UsageReportByID は台帳の 1 行を返す（無ければ ErrNotFound）。
func UsageReportByID(ctx context.Context, q execQuerier, projectID, id int64) (UsageReport, error) {
	list, err := queryUsageReports(ctx, q, `r.project_id = ? AND r.id = ?`, projectID, id)
	if err != nil {
		return UsageReport{}, err
	}
	if len(list) == 0 {
		return UsageReport{}, ErrNotFound
	}
	return list[0], nil
}

// LastUsageReport は「前回以降」の起点になる行（データ終端が最も新しい行）。台帳が空なら ErrNotFound。
func LastUsageReport(ctx context.Context, q execQuerier, projectID int64) (UsageReport, error) {
	list, err := queryUsageReports(ctx, q, `r.project_id = ? ORDER BY r.data_end DESC, r.id DESC LIMIT 1`, projectID)
	if err != nil {
		return UsageReport{}, err
	}
	if len(list) == 0 {
		return UsageReport{}, ErrNotFound
	}
	return list[0], nil
}

// UsageForPeriod は、会話記録の時刻 at が [from, to) のスナップショットを 1 件でも含む会話の全スナップショットを返す
// （期間の最初の区間の差分を取るには、期間の前のスナップショットも要る）。from がゼロ値なら下限なし。
func UsageForPeriod(ctx context.Context, q execQuerier, projectID int64, from, to time.Time) ([]usage.Snapshot, error) {
	if from.IsZero() {
		return queryUsage(ctx, q, `s.project_id = ? AND s.conversation_id IN
  (SELECT DISTINCT x.conversation_id FROM usage_snapshots x WHERE x.project_id = ? AND x.at < ?)`,
			projectID, projectID, to.UTC())
	}
	return queryUsage(ctx, q, `s.project_id = ? AND s.conversation_id IN
  (SELECT DISTINCT x.conversation_id FROM usage_snapshots x WHERE x.project_id = ? AND x.at >= ? AND x.at < ?)`,
		projectID, projectID, from.UTC(), to.UTC())
}

func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}
