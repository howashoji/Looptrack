package server

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

// 3 層のループの ②「人の判断待ち」と ③「外からの反応」（DESIGN.md §5-8-6・§5-8-7）。
// summary（REST・MCP）・一覧の has_feedback・ボードで共用する。
// 表示の文言は以前の CLI（1.0.0 より前）の summary と同じ（loops_test.go で突き合わせる）。

// reviewStaleAfter を超えて In Review に留まっているものに印を付ける。
const reviewStaleAfter = 48 * time.Hour

// feedbackExcerptLen は summary に出す抜粋の文字数。
const feedbackExcerptLen = 80

// reviewItemJSON は summary の in_review[] の 1 件（issueJSON に滞留を足す。既存のキーは変えない）。
type reviewItemJSON struct {
	issueJSON
	ReviewSince string  `json:"review_since"` // 最後に In Review になった時刻（RFC 3339。イベントが無ければ frontmatter の updated）
	ReviewHours float64 `json:"review_hours"` // 滞留（時間・小数 1 桁）
	ReviewStale bool    `json:"review_stale"` // 48 時間超
	ReviewAge   string  `json:"review_age"`   // 表示用（N日M時間 / M時間 / M分）
	// VerifySelfReported は直近の verify の記録が MCP の自己申告（report_verify）か（無ければ省く）
	VerifySelfReported bool `json:"verify_self_reported,omitempty"`
}

type feedbackIssueJSON struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Status  string `json:"status"`
	Pending int    `json:"pending"`  // 未応答の件数
	FirstAt string `json:"first_at"` // 最初の未応答の時刻（RFC 3339）
	Excerpt string `json:"excerpt"`  // 最初の未応答の抜粋（先頭語を除いた 80 文字）
}

type feedbackJSON struct {
	Count      int                 `json:"count"`       // 未応答の件数
	IssueCount int                 `json:"issue_count"` // 未応答を持つイシューの数
	Issues     []feedbackIssueJSON `json:"issues"`      // 古い未応答から上位 limit 件
}

// pendingByIssue は未応答のフィードバックをイシューの display_id ごとに数える。
func (s *Server) pendingByIssue(ctx context.Context, pr store.Project) (map[string]int, []store.PendingFeedback, error) {
	fb, err := store.PendingFeedbacks(ctx, s.db, pr.ID)
	if err != nil {
		return nil, nil, err
	}
	n := map[string]int{}
	for _, f := range fb {
		n[f.DisplayID]++
	}
	return n, fb, nil
}

// feedbackSummary は summary の feedback（古い未応答から limit イシュー）。
func (s *Server) feedbackSummary(ctx context.Context, pr store.Project, rows []service.Issue, limit int) (feedbackJSON, error) {
	n, fb, err := s.pendingByIssue(ctx, pr)
	if err != nil {
		return feedbackJSON{}, err
	}
	byID := map[string]*service.Issue{}
	for i := range rows {
		byID[rows[i].Item.ID] = &rows[i]
	}
	out := feedbackJSON{Count: len(fb), IssueCount: len(n), Issues: []feedbackIssueJSON{}}
	seen := map[string]bool{}
	for _, f := range fb { // 古い順なので、イシューごとの最初が最初の未応答
		if seen[f.DisplayID] {
			continue
		}
		seen[f.DisplayID] = true
		if len(out.Issues) >= limit {
			continue
		}
		it := feedbackIssueJSON{ID: f.DisplayID, Pending: n[f.DisplayID], FirstAt: f.CreatedAt.UTC().Format(time.RFC3339),
			Excerpt: domain.FeedbackExcerpt(f.Excerpt, feedbackExcerptLen)}
		if row, ok := byID[f.DisplayID]; ok {
			it.Title, it.Status = row.Item.Title, row.Item.Status
		}
		out.Issues = append(out.Issues, it)
	}
	return out, nil
}

// reviewItems は In Review のイシューに滞留を付け、滞留の長い順に並べる。
func (s *Server) reviewItems(ctx context.Context, lang i18n.Lang, pr store.Project, items []issueJSON) ([]reviewItemJSON, error) {
	since, err := store.ReviewSince(ctx, s.db, pr.ID)
	if err != nil {
		return nil, err
	}
	self, err := store.SelfReportedInReview(ctx, s.db, pr.ID)
	if err != nil {
		return nil, err
	}
	now := s.svc.Now()
	out := make([]reviewItemJSON, 0, len(items))
	for _, it := range items {
		at, ok := since[it.ID]
		if !ok { // 取り込み分などイベントが無いものは frontmatter の updated（分単位のローカル時刻）
			t, err := time.ParseInLocation("2006-01-02 15:04", it.Updated, s.svc.Loc)
			if err != nil {
				t = now
			}
			at = t
		}
		d := max(now.Sub(at), 0)
		out = append(out, reviewItemJSON{issueJSON: it, ReviewSince: at.UTC().Format(time.RFC3339),
			ReviewHours: math.Round(d.Hours()*10) / 10, ReviewStale: d > reviewStaleAfter, ReviewAge: reviewAge(lang, d), VerifySelfReported: self[it.ID]})
	}
	// 滞留の長い順（同じなら ID 順）
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ReviewSince != out[j].ReviewSince {
			return out[i].ReviewSince < out[j].ReviewSince
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// reviewAge は滞留の表示（N日M時間 / M時間 / M分）。
func reviewAge(lang i18n.Lang, d time.Duration) string {
	m := int(d / time.Minute)
	switch {
	case m < 60:
		return i18n.T(lang, "server.api.summary.age_minutes", "minutes", m)
	case m < 24*60:
		return i18n.T(lang, "server.api.summary.age_hours", "hours", m/60)
	}
	return i18n.T(lang, "server.api.summary.age_days", "days", m/(24*60), "hours", m/60%24)
}

// staleCount は 48 時間超の件数。
func staleCount(items []reviewItemJSON) int {
	n := 0
	for _, it := range items {
		if it.ReviewStale {
			n++
		}
	}
	return n
}

// 見出し（以前の CLI と同じ文言）。
func layerWorkHeading(lang i18n.Lang) string { return i18n.T(lang, "server.api.summary.layer_work") }

func reviewHeading(lang i18n.Lang, items []reviewItemJSON) string {
	return i18n.T(lang, "server.api.summary.layer_review", "count", len(items), "stale", staleCount(items))
}

func feedbackHeading(lang i18n.Lang, fb feedbackJSON) string {
	return i18n.T(lang, "server.api.summary.layer_feedback", "count", fb.Count, "issues", fb.IssueCount)
}

// selfReportedNote は ② の行末に付ける印（直近の verify が MCP の自己申告のとき。以前の CLI と同じ文言）。
func selfReportedNote(lang i18n.Lang) string {
	return i18n.T(lang, "server.api.summary.self_reported_note", "label", service.SelfReportedLabel)
}

// reviewLayerText は ② の見出しと行（以前の CLI と同じ）。
func reviewLayerText(lang i18n.Lang, items []reviewItemJSON) string {
	var b strings.Builder
	b.WriteString(reviewHeading(lang, items) + "\n")
	if len(items) == 0 {
		b.WriteString(i18n.T(lang, "server.api.summary.none") + "\n")
	}
	for _, it := range items {
		mark := ""
		if it.ReviewStale {
			mark = i18n.T(lang, "server.api.summary.stale_mark")
		}
		note := ""
		if it.VerifySelfReported {
			note = selfReportedNote(lang)
		}
		fmt.Fprintf(&b, "%-9s %-11s %-11s %-3s %-10s %-7s %s%s\n", it.ID, it.Type, it.Status, it.Priority, it.ReviewAge, mark, it.Title, note)
	}
	return b.String()
}

// feedbackLayerText は ③ の見出しと行（以前の CLI と同じ）。時刻は loc の分単位。
func feedbackLayerText(lang i18n.Lang, fb feedbackJSON, loc *time.Location) string {
	var b strings.Builder
	b.WriteString(feedbackHeading(lang, fb) + "\n")
	if fb.Count == 0 {
		b.WriteString(i18n.T(lang, "server.api.summary.none") + "\n")
	}
	for _, it := range fb.Issues {
		at := it.FirstAt
		if t, err := time.Parse(time.RFC3339, it.FirstAt); err == nil {
			at = t.In(loc).Format("2006-01-02 15:04")
		}
		fmt.Fprintf(&b, "%-9s %s\n", it.ID, i18n.T(lang, "server.api.summary.feedback_row", "at", at, "count", it.Pending, "excerpt", it.Excerpt))
	}
	if rest := fb.IssueCount - len(fb.Issues); rest > 0 {
		b.WriteString(i18n.T(lang, "server.api.summary.feedback_more", "count", rest) + "\n")
	}
	return b.String()
}

// withFeedbackPending は一覧の各項目に未応答の件数を付ける。only なら未応答のあるものだけを残す。
func withFeedbackPending(items []issueJSON, n map[string]int, only bool) []issueJSON {
	out := make([]issueJSON, 0, len(items))
	for _, it := range items {
		it.FeedbackPending = n[it.ID]
		if only && it.FeedbackPending == 0 {
			continue
		}
		out = append(out, it)
	}
	return out
}

// feedbackNote は has_feedback の一覧の後に付ける件数の行（list --has-feedback と同じ）。
func feedbackNote(lang i18n.Lang, items []issueJSON) string {
	var parts []string
	for _, it := range items {
		if it.FeedbackPending > 0 {
			parts = append(parts, i18n.T(lang, "server.api.summary.feedback_note_item", "id", it.ID, "count", it.FeedbackPending))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return i18n.T(lang, "server.api.summary.feedback_note", "items", strings.Join(parts, "・"))
}

// loopLayers は summary の ②③ を作り、counts に件数を足す（REST と MCP で共用）。
func (s *Server) loopLayers(ctx context.Context, lang i18n.Lang, pr store.Project, rows []service.Issue, inReview []issueJSON, cnt *countsJSON,
	limit int) ([]reviewItemJSON, feedbackJSON, *countsJSON, error) {
	review, err := s.reviewItems(ctx, lang, pr, inReview)
	if err != nil {
		return nil, feedbackJSON{}, nil, err
	}
	fb, err := s.feedbackSummary(ctx, pr, rows, limit)
	if err != nil {
		return nil, feedbackJSON{}, nil, err
	}
	stale, pending := staleCount(review), fb.Count
	cnt.InReviewStale, cnt.FeedbackPending = &stale, &pending
	return review, fb, cnt, nil
}
