package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/store"
)

// 3 層のループの ②（In Review の滞留）と ③（未応答のフィードバック）。DESIGN.md §5-8-6・§5-8-7。

type leadSequence struct {
	Name  string `json:"name"`
	Steps []struct {
		Comment *string `json:"comment"`
		Status  string  `json:"status"`
	} `json:"steps"`
	Pending []int `json:"pending"`
}

type leadSequences struct {
	Sequences   []leadSequence `json:"sequences"`
	SequencesEn []leadSequence `json:"sequences_en"` // 英語の別名 Feedback:（§5-13。Go だけが読む）
}

func loadLeadSequences(t *testing.T) leadSequences {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "domain", "testdata", "leadword.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ex leadSequences
	if err := json.Unmarshal(b, &ex); err != nil {
		t.Fatal(err)
	}
	if len(ex.Sequences) != 6 {
		t.Fatalf("判定の例は 6 つ（§5-8-6）: %d", len(ex.Sequences))
	}
	return ex
}

type loopsEnv struct {
	e      *env
	pr     store.Project
	ed     *apiClient
	viewer *apiClient
}

func newLoopsEnv(t *testing.T) *loopsEnv {
	e, pr, ed := newAPIEnv(t)
	vi := e.user("vi", "vi-password-123", "member")
	if err := store.SetMember(context.Background(), e.db, pr.ID, vi.ID, "viewer"); err != nil {
		t.Fatal(err)
	}
	return &loopsEnv{e: e, pr: pr, ed: ed, viewer: e.apiAs(vi)}
}

func (l *loopsEnv) create(title string, extra map[string]any) string {
	l.e.t.Helper()
	body := map[string]any{"title": title}
	for k, v := range extra {
		body[k] = v
	}
	var created struct {
		Issue issueDetailJSON `json:"issue"`
	}
	l.ed.json(201, "POST", "/projects/"+l.pr.Slug+"/issues", body, &created)
	return created.Issue.ID
}

func (l *loopsEnv) comment(id, text string) int {
	l.e.t.Helper()
	var out struct {
		Seq int `json:"seq"`
	}
	l.ed.json(201, "POST", "/issues/"+id+"/comments", map[string]any{"text": text}, &out)
	return out.Seq
}

func (l *loopsEnv) status(id, status string) {
	l.e.t.Helper()
	l.ed.json(200, "POST", "/issues/"+id+"/status", map[string]any{"status": status}, nil)
}

// pending はイシューごとの未応答の seq（store.PendingFeedbacks の結果）。
func (l *loopsEnv) pending() map[string][]int {
	l.e.t.Helper()
	fb, err := store.PendingFeedbacks(context.Background(), l.e.app, l.pr.ID) // 本番と同じ権限のユーザーで引く
	if err != nil {
		l.e.t.Fatal(err)
	}
	out := map[string][]int{}
	for _, f := range fb {
		out[f.DisplayID] = append(out[f.DisplayID], f.Seq)
	}
	return out
}

func (l *loopsEnv) list(query string) []issueJSON {
	l.e.t.Helper()
	var res listJSON
	l.ed.json(200, "GET", "/projects/"+l.pr.Slug+"/issues?"+query, nil, &res)
	return res.Items
}

// 英語の別名「Feedback:」（大小を問わない）も未応答のフィードバックとして数え、先頭語の無い応答で外れる。
// MySQL（utf8mb4_bin）と SQLite（case_sensitive_like）の両方で同じ結果になる（LOOPTRACK_TEST_DB で切り替えて回す）。
func TestPendingFeedbackEnglishExamples(t *testing.T) {
	l := newLoopsEnv(t)
	ex := loadLeadSequences(t)
	if len(ex.SequencesEn) == 0 {
		t.Fatal("sequences_en の例が無い")
	}
	want := map[string][]int{}
	var all []string
	for _, sq := range ex.SequencesEn {
		id := l.create(sq.Name, nil)
		all = append(all, id)
		var seqs []int
		for _, st := range sq.Steps {
			l.e.clock.Add(time.Minute)
			if st.Comment != nil {
				seqs = append(seqs, l.comment(id, *st.Comment))
			} else {
				l.status(id, st.Status)
			}
		}
		for _, k := range sq.Pending {
			want[id] = append(want[id], seqs[k-1])
		}
	}
	got := l.pending()
	for i, sq := range ex.SequencesEn {
		id := all[i]
		if fmt.Sprint(got[id]) != fmt.Sprint(want[id]) {
			t.Errorf("%s（%s）: 未応答 %v, want %v", sq.Name, id, got[id], want[id])
		}
	}
	// 一覧の has_feedback も同じ集合
	var wantIDs []string
	for id := range want {
		wantIDs = append(wantIDs, id)
	}
	sort.Strings(wantIDs)
	if items := l.list("has_feedback=1&sort=id"); ids(items) != strings.Join(wantIDs, ",") {
		t.Errorf("has_feedback の一覧 %s, want %s", ids(items), strings.Join(wantIDs, ","))
	}
	// 先頭語の無いコメントで外れる
	for _, id := range wantIDs {
		l.e.clock.Add(time.Minute)
		l.comment(id, "Filed a follow-up issue")
	}
	if got := l.pending(); len(got) != 0 {
		t.Errorf("応答の後も未応答が残る: %v", got)
	}
}

func TestPendingFeedbackExamples(t *testing.T) {
	l := newLoopsEnv(t)
	ex := loadLeadSequences(t)
	want := map[string][]int{}
	var all []string
	for _, sq := range ex.Sequences {
		id := l.create(sq.Name, nil)
		all = append(all, id)
		var seqs []int
		for _, st := range sq.Steps {
			l.e.clock.Add(time.Minute) // 状態変更はフィードバックより「後」（at > created_at）
			if st.Comment != nil {
				seqs = append(seqs, l.comment(id, *st.Comment))
			} else {
				l.status(id, st.Status)
			}
		}
		for _, k := range sq.Pending {
			want[id] = append(want[id], seqs[k-1])
		}
	}
	got := l.pending()
	for i, sq := range ex.Sequences {
		id := all[i]
		if fmt.Sprint(got[id]) != fmt.Sprint(want[id]) {
			t.Errorf("%s（%s）: 未応答 %v, want %v", sq.Name, id, got[id], want[id])
		}
	}

	// 状態変更と同時のコメントは、そのコメント自身がフィードバックなら応答にならない（at = created_at）
	id := l.create("同時", nil)
	l.e.clock.Add(time.Minute)
	l.ed.json(200, "POST", "/issues/"+id+"/status", map[string]any{"status": "In Progress", "comment": "フィードバック: 状態変更と同時"}, nil)
	if got := l.pending()[id]; len(got) != 1 {
		t.Errorf("状態変更と同時のフィードバックが未応答にならない: %v", got)
	}

	// クローズ済みにも反応は来る。has_feedback は既定でクローズ済みも含め、feedback_pending を付ける
	closed := l.create("クローズ済み", nil)
	l.e.clock.Add(time.Minute)
	l.status(closed, "Done")
	l.e.clock.Add(time.Minute)
	l.comment(closed, "フィードバック：Done の後に来た反応")
	wantIDs := []string{}
	for k := range l.pending() {
		wantIDs = append(wantIDs, k)
	}
	sort.Strings(wantIDs)
	items := l.list("has_feedback=1&sort=id")
	if ids(items) != strings.Join(wantIDs, ",") {
		t.Errorf("has_feedback の一覧 %s, want %s", ids(items), strings.Join(wantIDs, ","))
	}
	for _, it := range items {
		if it.FeedbackPending != len(l.pending()[it.ID]) {
			t.Errorf("%s の feedback_pending %d", it.ID, it.FeedbackPending)
		}
	}
	if strings.Contains(ids(l.list("sort=id")), closed) {
		t.Error("既定の一覧にクローズ済みが出た")
	}
	if strings.Contains(ids(l.list("has_feedback=1&status=Todo")), closed) {
		t.Error("has_feedback と status の併用が効かない")
	}
	if code, _, b := l.ed.do("GET", "/projects/"+l.pr.Slug+"/issues?sort=id", nil); code != 200 || strings.Contains(string(b), "feedback_pending") {
		t.Errorf("has_feedback の無い一覧に feedback_pending が付いた: %s", b)
	}

	// ボードの feedback_pending は同じ集合（絞り込み「未応答の反応」の元）
	var board struct {
		Issues []boardIssueJSON `json:"issues"`
	}
	l.ed.json(200, "GET", "/projects/"+l.pr.Slug+"/board", nil, &board)
	var boardIDs []string
	for _, it := range board.Issues {
		if it.FeedbackPending > 0 {
			boardIDs = append(boardIDs, it.ID)
			if it.FeedbackPending != len(l.pending()[it.ID]) {
				t.Errorf("ボードの %s の feedback_pending %d", it.ID, it.FeedbackPending)
			}
		}
	}
	sort.Strings(boardIDs)
	if strings.Join(boardIDs, ",") != strings.Join(wantIDs, ",") {
		t.Errorf("ボードの未応答 %v, want %v", boardIDs, wantIDs)
	}

	// 応答すると外れる: 先頭語の無いコメント・状態変更
	l.e.clock.Add(time.Minute)
	l.comment(closed, "REQ-0999 で起票して対応する")
	l.e.clock.Add(time.Minute)
	l.status(id, "Todo")
	if strings.Contains(ids(l.list("has_feedback=1")), closed) || strings.Contains(ids(l.list("has_feedback=1")), id) {
		t.Errorf("応答したのに has_feedback に残る: %s", ids(l.list("has_feedback=1")))
	}

	// viewer はフィードバックを登録できない（通常のコメントと同じく 403。§5-8-6）
	l.viewer.fail(403, "POST", "/issues/"+all[0]+"/comments", map[string]any{"text": "フィードバック: viewer から"})

	// MCP list_issues の has_feedback
	m := l.e.mcpAs(l.ed.token, map[string]string{"X-Looptrack-Project": l.pr.Slug})
	text, data := m.call("list_issues", map[string]any{"has_feedback": true, "sort": "id"}, false)
	var mcpIDs []string
	for _, it := range data["items"].([]any) {
		mcpIDs = append(mcpIDs, it.(map[string]any)["id"].(string))
	}
	if strings.Join(mcpIDs, ",") != ids(l.list("has_feedback=1&sort=id")) || !strings.Contains(text, "未応答のフィードバック: ") {
		t.Errorf("MCP list_issues has_feedback: %v\n%s", mcpIDs, text)
	}
}

type summaryRes struct {
	InProgress []issueJSON      `json:"in_progress"`
	InReview   []reviewItemJSON `json:"in_review"`
	Feedback   feedbackJSON     `json:"feedback"`
	Counts     countsJSON       `json:"counts"`
}

func TestSummaryLayers(t *testing.T) {
	l := newLoopsEnv(t)
	real := time.Now().UTC()
	// A は 50 時間 30 分前から、B は 3 時間 30 分前から In Review（時の境目で表示が揺れないよう 30 分ずらす）
	l.e.clock.t = real.Add(-(50*time.Hour + 30*time.Minute))
	a := l.create("長く待っている", nil)
	l.status(a, "In Review")
	l.e.clock.Add(47 * time.Hour)
	b := l.create("起票時から In Review", map[string]any{"status": "In Review"})
	c := l.create("反応のあるもの", nil)
	l.comment(c, "フィードバック: テスター A（9/18）: 保存ボタンが\n見つからない。"+strings.Repeat("長い説明", 30))
	l.e.clock.Add(time.Minute)
	l.comment(c, "フィードバック: テスター B: 同じく")
	d := l.create("別の反応", nil)
	l.e.clock.Add(time.Minute)
	l.comment(d, "フィードバック：短い")
	l.e.clock.t = real

	var res summaryRes
	l.ed.json(200, "GET", "/projects/"+l.pr.Slug+"/summary?limit=1", nil, &res)
	if len(res.InReview) != 2 || res.InReview[0].ID != a || res.InReview[1].ID != b {
		t.Fatalf("in_review の並び（滞留の長い順）: %+v", res.InReview)
	}
	ra, rb := res.InReview[0], res.InReview[1]
	if !ra.ReviewStale || ra.ReviewAge != "2日2時間" || ra.ReviewHours < 50.4 || ra.ReviewHours > 50.6 || rb.ReviewStale || rb.ReviewAge != "3時間" {
		t.Errorf("滞留: %+v / %+v", ra, rb)
	}
	if _, err := time.Parse(time.RFC3339, ra.ReviewSince); err != nil || ra.Title == "" || ra.Status != "In Review" {
		t.Errorf("review_since・既存のキー: %+v", ra)
	}
	if res.Counts.InReviewStale == nil || *res.Counts.InReviewStale != 1 || res.Counts.FeedbackPending == nil || *res.Counts.FeedbackPending != 3 {
		t.Errorf("counts: %+v", res.Counts)
	}
	fb := res.Feedback
	if fb.Count != 3 || fb.IssueCount != 2 || len(fb.Issues) != 1 || fb.Issues[0].ID != c || fb.Issues[0].Pending != 2 ||
		!strings.HasPrefix(fb.Issues[0].Excerpt, "テスター A（9/18）: 保存ボタンが 見つからない。") || !strings.HasSuffix(fb.Issues[0].Excerpt, "…") ||
		len([]rune(fb.Issues[0].Excerpt)) != feedbackExcerptLen+1 {
		t.Errorf("feedback（limit 1）: %+v", fb)
	}

	// CLI（API モード）の表示: ① ② ③ の見出し。② ③ は MCP project_summary の本文と同じ
	cli := newCLIEnv(t, l.e, l.pr.Slug, l.ed.token)
	r := mustCLI(t, cli.run("", "summary", "--limit", "1"), 0, "summary")
	for _, want := range []string{
		"══ ① いまの周（AI の作業） ══\n── 進行中（In Progress） ──\n",
		"══ ② 人の判断待ち（In Review 2 件・48 時間超 1 件） ══\n",
		// 滞留の表示（review_age）はサーバが文字列にして JSON に入れるので、要求の Accept-Language で決まる。
		// CLI は LOOPTRACK_LANG=ja（cliHomeEnv）を Accept-Language として送るので日本語で届く。
		fmt.Sprintf("%-9s %-11s %-11s %-3s %-10s %-7s %s\n", a, "task", "In Review", "P2", "2日2時間", "[48h超]", "長く待っている"),
		fmt.Sprintf("%-9s %-11s %-11s %-3s %-10s %-7s %s\n", b, "task", "In Review", "P2", "3時間", "", "起票時から In Review"),
		"══ ③ 外からの反応（未応答のフィードバック 3 件・2 イシュー） ══\n" + c + " ",
		" から 2 件  テスター A（9/18）: 保存ボタンが 見つからない。",
		"…ほか 1 イシュー（looptrack issue list --has-feedback）\n\n未クローズ ",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("CLI の summary に %q が無い:\n%s", want, r.stdout)
		}
	}
	if strings.Contains(r.stdout, "レビュー待ち") {
		t.Errorf("API モードで旧見出しが残る:\n%s", r.stdout)
	}
	m := l.e.mcpAs(l.ed.token, map[string]string{"X-Looptrack-Project": l.pr.Slug})
	text, data := m.call("project_summary", map[string]any{"limit": 1}, false)
	layers := func(s string) string {
		i, j := strings.Index(s, "══ ② "), strings.Index(s, "未クローズ ")
		if i < 0 || j < i {
			return ""
		}
		return s[i:j]
	}
	if layers(r.stdout) == "" || layers(r.stdout) != layers(text) || !strings.HasPrefix(text, "══ ① いまの周（AI の作業） ══\n") {
		t.Errorf("CLI と MCP の ② ③ が一致しない:\n--- CLI\n%s\n--- MCP\n%s", r.stdout, text)
	}
	if data["feedback"] == nil || data["in_review"].([]any)[0].(map[string]any)["review_stale"] != true {
		t.Errorf("MCP の構造化結果: %v", data)
	}

	// --json は API の値をそのまま渡す（feedback・review_* を含む）
	r = mustCLI(t, cli.run("", "summary", "--json"), 0, "summary --json")
	if !strings.Contains(r.stdout, `"feedback": {`) || !strings.Contains(r.stdout, `"review_stale": true`) || !strings.Contains(r.stdout, `"feedback_pending": 3`) {
		t.Errorf("summary --json:\n%s", r.stdout)
	}

	// 該当なしの層も見出しを出す
	l2 := newLoopsEnv(t)
	cli2 := newCLIEnv(t, l2.e, l2.pr.Slug, l2.ed.token)
	r = mustCLI(t, cli2.run("", "summary"), 0, "summary（空）")
	for _, want := range []string{"══ ② 人の判断待ち（In Review 0 件・48 時間超 0 件） ══\n該当なし\n",
		"══ ③ 外からの反応（未応答のフィードバック 0 件・0 イシュー） ══\n該当なし\n"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("空の summary に %q が無い:\n%s", want, r.stdout)
		}
	}

	// list --has-feedback（CLI）: 未応答のあるものだけ。先頭語の無いコメントで外れる
	r = mustCLI(t, cli.run("", "list", "--has-feedback", "--sort", "id"), 0, "list --has-feedback")
	if !strings.Contains(r.stdout, c+" ") || !strings.Contains(r.stdout, d+" ") || strings.Contains(r.stdout, a+" ") ||
		!strings.Contains(r.stdout, "未応答のフィードバック: "+c+" 2 件・"+d+" 1 件") {
		t.Errorf("list --has-feedback:\n%s", r.stdout)
	}
	mustCLI(t, cli.run("", "comment", c, "方針: 保存ボタンを右上へ移す"), 0, "応答")
	r = mustCLI(t, cli.run("", "list", "--has-feedback"), 0, "list --has-feedback（応答後）")
	if strings.Contains(r.stdout, c+" ") || !strings.Contains(r.stdout, d+" ") {
		t.Errorf("応答後の list --has-feedback:\n%s", r.stdout)
	}
}

// TestSummaryOtherSession: 別のセッションが In Progress にしたものに印が付く（横取りと重複の防止）。
func TestSummaryOtherSession(t *testing.T) {
	l := newLoopsEnv(t)
	real := time.Now().UTC()
	l.e.clock.t = real.Add(-3 * time.Hour)
	l.ed.header["X-Looptrack-Session"] = "s-A"
	a := l.create("セッション A が着手", nil)
	l.status(a, "In Progress")
	l.e.clock.t = real
	b := l.create("同じセッションが着手", nil)
	l.status(b, "In Progress")

	// 別のセッション（s-B）から見ると、A も B も「別のセッションが着手中」
	l.ed.header["X-Looptrack-Session"] = "s-B"
	var res summaryRes
	l.ed.json(200, "GET", "/projects/"+l.pr.Slug+"/summary", nil, &res)
	got := map[string]issueJSON{}
	for _, it := range res.InProgress {
		got[it.ID] = it
	}
	if !got[a].OtherSession || got[a].StartedAgo != "3時間" {
		t.Errorf("A は別セッション・3時間のはず: %+v", got[a])
	}
	if !got[b].OtherSession {
		t.Errorf("B も別セッションのはず: %+v", got[b])
	}

	// 着手した本人（s-A）から見ると印は付かない
	l.ed.header["X-Looptrack-Session"] = "s-A"
	var mine summaryRes
	l.ed.json(200, "GET", "/projects/"+l.pr.Slug+"/summary", nil, &mine)
	for _, it := range mine.InProgress {
		if it.OtherSession {
			t.Errorf("自分が着手したものに印が付いた: %+v", it)
		}
		if it.StartedAgo == "" {
			t.Errorf("経過時間は自分の分にも付く: %+v", it)
		}
	}

	// セッション ID を送らない経路（画面・Copilot）では印を付けない
	delete(l.ed.header, "X-Looptrack-Session")
	var anon summaryRes
	l.ed.json(200, "GET", "/projects/"+l.pr.Slug+"/summary", nil, &anon)
	for _, it := range anon.InProgress {
		if it.OtherSession {
			t.Errorf("セッション ID が無いのに印が付いた: %+v", it)
		}
	}

	// CLI の表示に注記が出る
	l.ed.header["X-Looptrack-Session"] = "s-B"
	cli := newCLIEnv(t, l.e, l.pr.Slug, l.ed.token)
	r := mustCLI(t, cli.run("", "summary"), 0, "summary")
	if !strings.Contains(r.stdout, "は別のセッションが着手しています") {
		t.Errorf("注記が出ていない:\n%s", r.stdout)
	}
}

// TestListAndNextOtherSession: 空きを探す経路のうち issue list と next にも「別のセッションが着手中」が現れる。
// セッション ID を送らない経路（画面・古いクライアント）では従来どおり（印も注記も出ず、着手中を返す）。
func TestListAndNextOtherSession(t *testing.T) {
	l := newLoopsEnv(t)
	l.ed.header["X-Looptrack-Session"] = "s-A"
	a := l.create("セッション A が着手", map[string]any{"priority": "P0"})
	l.status(a, "In Progress")
	b := l.create("まだ空いている", map[string]any{"priority": "P1"})

	// ① list（別のセッション s-B から）: A の着手中に印が付き、空いているものには付かない
	l.ed.header["X-Looptrack-Session"] = "s-B"
	marks := map[string]issueJSON{}
	for _, it := range l.list("sort=id") {
		marks[it.ID] = it
	}
	if !marks[a].OtherSession || marks[a].StartedAgo == "" {
		t.Errorf("list: A に別セッションの印が無い: %+v", marks[a])
	}
	if marks[b].OtherSession {
		t.Errorf("list: 着手されていないものに印が付いた: %+v", marks[b])
	}

	// ② next（s-B）: A を resumed にせず、空いている B を始める。注記に A が出る
	var r nextResp
	l.ed.json(200, "POST", "/projects/"+l.pr.Slug+"/next", map[string]any{}, &r)
	if r.Action != "started" || r.Issue == nil || r.Issue.ID != b {
		t.Fatalf("next（s-B）: %+v", r)
	}
	if len(r.OtherSessions) != 1 || r.OtherSessions[0] != a {
		t.Errorf("next の other_sessions: %v（%s のはず）", r.OtherSessions, a)
	}
	if !strings.Contains(r.Text, a+" は別のセッションが着手しています") {
		t.Errorf("next の注記が無い:\n%s", r.Text)
	}

	// ③ セッション ID を送らない経路は従来どおり: 着手中（A）を resumed で返し、印も注記も出ない
	delete(l.ed.header, "X-Looptrack-Session")
	for _, it := range l.list("sort=id") {
		if it.OtherSession {
			t.Errorf("セッション ID が無いのに印が付いた: %+v", it)
		}
	}
	var anon nextResp
	l.ed.json(200, "POST", "/projects/"+l.pr.Slug+"/next", map[string]any{}, &anon)
	if anon.Action != "resumed" || anon.Issue == nil || anon.Issue.ID != a {
		t.Errorf("セッション ID なしの next は従来どおり A を返す: %+v", anon)
	}
	if len(anon.OtherSessions) != 0 || strings.Contains(anon.Text, "別のセッションが着手しています") {
		t.Errorf("セッション ID なしで注記が出た: %v\n%s", anon.OtherSessions, anon.Text)
	}

	// ④ CLI の list に注記が出る（文面は summary と同じ。言語は CLI の環境で ja に固定）
	cli := newCLIEnv(t, l.e, l.pr.Slug, l.ed.token)
	cli.session = "s-C"
	res := mustCLI(t, cli.run("", "list", "--sort", "id"), 0, "list")
	if !strings.Contains(res.stdout, "は別のセッションが着手しています") || !strings.Contains(res.stdout, a) {
		t.Errorf("CLI の list に注記が無い:\n%s", res.stdout)
	}
}

// TestSessionKindNotes は、着手したセッションの見分けの 3 通りを固定する。
// ① 同じ種類で同じ ID（＝自分。注記なし・除外しない）② 同じ種類で別の ID（＝別のセッション。従来の注記・除外する）
// ③ 種類が違う（CLI のセッション ID と MCP の接続 ID。**着手中として返したうえで**「判定できない」と注記する。
// サーバに両者を結ぶ情報が無いので同一性を言えないが、黙って握りつぶすと見落としの側に倒れるため）。
func TestSessionKindNotes(t *testing.T) {
	const otherNote, crossNote = "は別のセッションが着手しています", "は別の経路（CLI / MCP）で着手されています"
	// ①② 同じ種類（どちらも CLI のセッション ID）
	l := newLoopsEnv(t)
	l.ed.header["X-Looptrack-Session"] = "s-A"
	a := l.create("CLI のセッションが着手", map[string]any{"priority": "P0"})
	l.status(a, "In Progress")

	var mine nextResp
	l.ed.json(200, "POST", "/projects/"+l.pr.Slug+"/next", map[string]any{}, &mine)
	if mine.Action != "resumed" || mine.Issue == nil || mine.Issue.ID != a {
		t.Fatalf("① 同じセッションは着手中として返る: %+v", mine)
	}
	if len(mine.OtherSessions) != 0 || len(mine.CrossPathSessions) != 0 ||
		strings.Contains(mine.Text, otherNote) || strings.Contains(mine.Text, crossNote) {
		t.Errorf("① 自分の着手に注記が出た: other=%v cross=%v\n%s", mine.OtherSessions, mine.CrossPathSessions, mine.Text)
	}
	for _, it := range l.list("sort=id") {
		if it.OtherSession || it.CrossPathSession {
			t.Errorf("① 自分の着手に印が付いた: %+v", it)
		}
	}

	l.ed.header["X-Looptrack-Session"] = "s-B"
	var other nextResp
	l.ed.json(200, "POST", "/projects/"+l.pr.Slug+"/next", map[string]any{}, &other)
	if len(other.OtherSessions) != 1 || other.OtherSessions[0] != a || len(other.CrossPathSessions) != 0 ||
		!strings.Contains(other.Text, a+" "+otherNote) {
		t.Errorf("② 別のセッション: other=%v cross=%v\n%s", other.OtherSessions, other.CrossPathSessions, other.Text)
	}
	if other.Issue != nil && other.Issue.ID == a {
		t.Errorf("② 別のセッションの着手中を返した: %+v", other)
	}
	for _, it := range l.list("sort=id") {
		if it.ID == a && (!it.OtherSession || it.CrossPathSession) {
			t.Errorf("② 印: %+v", it)
		}
	}

	// ③ 種類が違う（MCP の接続 ID で着手 → CLI のセッション ID で見る）
	l2 := newLoopsEnv(t)
	b := l2.create("MCP の接続が着手", map[string]any{"priority": "P0"})
	m := l2.e.mcpAs(l2.ed.token, map[string]string{"X-Looptrack-Project": l2.pr.Slug})
	m.call("set_status", map[string]any{"id": b, "status": "In Progress"}, false)
	l2.ed.header["X-Looptrack-Session"] = "s-A"
	var cross nextResp
	l2.ed.json(200, "POST", "/projects/"+l2.pr.Slug+"/next", map[string]any{}, &cross)
	if cross.Action != "resumed" || cross.Issue == nil || cross.Issue.ID != b {
		t.Fatalf("③ 種類が違うときは着手中として返す: %+v", cross)
	}
	if len(cross.CrossPathSessions) != 1 || cross.CrossPathSessions[0] != b || len(cross.OtherSessions) != 0 ||
		!strings.Contains(cross.Text, b+" "+crossNote) {
		t.Errorf("③ 注記: other=%v cross=%v\n%s", cross.OtherSessions, cross.CrossPathSessions, cross.Text)
	}
	for _, it := range l2.list("sort=id") {
		if it.ID == b && (!it.CrossPathSession || it.OtherSession) {
			t.Errorf("③ 印: %+v", it)
		}
	}
	cli := newCLIEnv(t, l2.e, l2.pr.Slug, l2.ed.token)
	cli.session = "s-A"
	if r := mustCLI(t, cli.run("", "list", "--sort", "id"), 0, "list"); !strings.Contains(r.stdout, crossNote) {
		t.Errorf("③ CLI の list に注記が無い:\n%s", r.stdout)
	}
}
