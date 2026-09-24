package store

import (
	"context"
	"time"
)

// 3 層のループの ②③（DESIGN.md §5-8-6・§5-8-7）。どちらもプロジェクト単位で 1 クエリ。
// summary（SessionStart の 2 秒の制限の内側）・一覧・ボードで共用するため、本文・コメントの全件は読まない。

// PendingFeedback は未応答のフィードバック（先頭が「フィードバック:」のコメント）1 件。
type PendingFeedback struct {
	IssueID   int64
	DisplayID string
	Seq       int
	CreatedAt time.Time
	Excerpt   string // 本文の先頭 200 文字（表示の抜粋は domain.FeedbackExcerpt で作る）
}

// PendingFeedbacks はプロジェクトの未応答のフィードバックを古い順に返す（クローズ済みのイシューも含む）。
// 応答済み = それより後（seq が大きい）に先頭語が「フィードバック」でないコメントがある、または
// それより後（at > created_at）に状態変更がある。コメントは editor 以上しか書けないので役割は引かない。
// 取り込み分（created_at が NULL）は対象外。LIKE の照合順序は utf8mb4_bin（テーブルの既定）。
// 先頭語の規則は domain.LeadWord と同じ（先頭の空白は許さない）。英語の別名「Feedback:」（大小を問わない・§5-13）も含める。
func PendingFeedbacks(ctx context.Context, q execQuerier, projectID int64) ([]PendingFeedback, error) {
	rows, err := q.QueryContext(ctx, `SELECT c.issue_id, i.display_id, c.seq, c.created_at, LEFT(c.content, 200)
  FROM comments c
  JOIN issues i ON i.id = c.issue_id
 WHERE i.project_id = ?
   AND c.created_at IS NOT NULL
   AND `+feedbackLead("c")+`
   AND NOT EXISTS (SELECT 1 FROM comments r
                    WHERE r.issue_id = c.issue_id AND r.seq > c.seq
                      AND NOT `+feedbackLead("r")+`)
   AND NOT EXISTS (SELECT 1 FROM issue_events e
                    WHERE e.issue_id = c.issue_id AND e.kind = 'status' AND e.at > c.created_at)
 ORDER BY c.created_at, c.issue_id, c.seq`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingFeedback
	for rows.Next() {
		var f PendingFeedback
		if err := rows.Scan(&f.IssueID, &f.DisplayID, &f.Seq, &f.CreatedAt, &f.Excerpt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// feedbackLead は列 <alias>.content の先頭語が「フィードバック」かの SQL の条件（domain.LeadWord の LeadFeedback と同じ）。
//   - 日本語: LIKE で前方一致（MySQL は utf8mb4_bin、SQLite は case_sensitive_like で、どちらも大小を区別する）
//   - 英語（Feedback:・大小を問わない）: 先頭 9 文字を LOWER / UPPER の両方で比べる。MySQL の LOWER は Unicode で畳み、
//     K（U+212A ケルビン記号）も k にする。SQLite の LOWER は ASCII だけ。UPPER でも一致を求めると、両方で ASCII の
//     「feedback:」の大小違いだけが残る（K の UPPER は K のまま）。LEFT は SQLite では substr に書き換わる（sqlite.go）
func feedbackLead(alias string) string {
	head := "LEFT(" + alias + ".content, 9)"
	return "(" + alias + ".content LIKE 'フィードバック:%' OR " + alias + ".content LIKE 'フィードバック：%' OR " +
		"(LOWER(" + head + ") = 'feedback:' AND UPPER(" + head + ") = 'FEEDBACK:'))"
}

// ReviewSince はプロジェクトの In Review のイシューごとに、最後に In Review になった時刻を返す
// （状態変更の to、または起票時の status が In Review のイベントの最大の at）。
// 該当するイベントが無いイシュー（取り込み分など）は含めない（呼び出し側が frontmatter の updated で補う）。
func ReviewSince(ctx context.Context, q execQuerier, projectID int64) (map[string]time.Time, error) {
	rows, err := q.QueryContext(ctx, `SELECT i.display_id, MAX(e.at)
  FROM issues i
  JOIN issue_events e ON e.issue_id = i.id
 WHERE i.project_id = ? AND i.status = 'In Review'
   AND ((e.kind = 'status' AND JSON_UNQUOTE(JSON_EXTRACT(e.detail, '$.to')) = 'In Review') OR
        (e.kind = 'create' AND JSON_UNQUOTE(JSON_EXTRACT(e.detail, '$.status')) = 'In Review'))
 GROUP BY i.display_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var id string
		var at time.Time
		if err := rows.Scan(&id, &at); err != nil {
			return nil, err
		}
		out[id] = at
	}
	return out, rows.Err()
}
