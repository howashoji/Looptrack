package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/auth"
	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/mdformat"
	"github.com/howashoji/looptrack/internal/store"
)

// REST API（DESIGN.md §4）の統合テスト。

type apiClient struct {
	e      *env
	c      *http.Client
	token  string
	header map[string]string
}

func (e *env) apiAs(u store.User) *apiClient {
	e.t.Helper()
	tok, prefix, _ := auth.NewPAT()
	if _, err := store.CreateToken(context.Background(), e.db, u.ID, "pat", "t", prefix, auth.HashToken(tok), nil, e.clock.Now()); err != nil {
		e.t.Fatal(err)
	}
	return &apiClient{e: e, c: e.client(), token: tok, header: map[string]string{}}
}

func (a *apiClient) do(method, path string, body any, header ...string) (int, http.Header, []byte) {
	a.e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, a.e.srv.URL+"/im/api/v1"+path, rd)
	req.Header.Set("Authorization", "Bearer "+a.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range a.header {
		req.Header.Set(k, v)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	res, err := a.c.Do(req)
	if err != nil {
		a.e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, b
}

// json は応答を v に読み、状態コードが want でなければ失敗させる。
func (a *apiClient) json(want int, method, path string, body, v any, header ...string) http.Header {
	a.e.t.Helper()
	code, h, b := a.do(method, path, body, header...)
	if code != want {
		a.e.t.Fatalf("%s %s: %d %s, want %d", method, path, code, b, want)
	}
	if v != nil {
		if err := json.Unmarshal(b, v); err != nil {
			a.e.t.Fatalf("%s %s: %v: %s", method, path, err, b)
		}
	}
	return h
}

type apiErr struct {
	Error struct {
		Code        string           `json:"code"`
		Message     string           `json:"message"`
		Rule        string           `json:"rule"`
		Overridable bool             `json:"overridable"`
		Current     *issueDetailJSON `json:"current"`
	} `json:"error"`
}

func (a *apiClient) fail(want int, method, path string, body any, header ...string) apiErr {
	a.e.t.Helper()
	var e apiErr
	a.json(want, method, path, body, &e, header...)
	if e.Error.Message == "" {
		a.e.t.Errorf("%s %s: エラー文が空", method, path)
	}
	return e
}

type listJSON struct {
	Items []issueJSON `json:"items"`
	Count int         `json:"count"`
}

func ids(items []issueJSON) string {
	var out []string
	for _, it := range items {
		out = append(out, it.ID)
	}
	return strings.Join(out, ",")
}

func newAPIEnv(t *testing.T) (*env, store.Project, *apiClient) {
	e := newEnv(t)
	// 分の途中の時刻に固定する（実時刻に近くないと Cookie の期限が切れる）
	e.clock.t = time.Now().UTC().Truncate(time.Minute).Add(30 * time.Second)
	pr := e.project("req")
	u := e.user("ed", "ed-password-123", "member")
	if err := store.SetMember(context.Background(), e.db, pr.ID, u.ID, "editor"); err != nil {
		t.Fatal(err)
	}
	return e, pr, e.apiAs(u)
}

// jst は以前の CLI と同じ表示の時刻（日本時間の分単位）。
func jst(t time.Time) string {
	return t.In(time.FixedZone("JST", 9*60*60)).Format("2006-01-02 15:04")
}

func TestIssueLifecycle(t *testing.T) {
	e, pr, ed := newAPIEnv(t)
	t0, t2 := jst(e.clock.Now()), jst(e.clock.Now().Add(2*time.Minute))
	ed.header["X-Looptrack-Client"] = "cli"
	ed.header["X-Looptrack-Session"] = "sess-1"

	// 起票: 採番・既定値・本文・ファイル名。時刻は日本時間の分単位
	var created struct {
		Issue issueDetailJSON `json:"issue"`
		Path  string          `json:"path"`
	}
	h := ed.json(201, "POST", "/projects/req/issues", map[string]any{"title": "要件: ログイン / 認証", "type": "requirement", "priority": "P0",
		"labels": []string{"auth", " b c ", ""}, "body": "本文 🔴"}, &created)
	req := created.Issue
	if req.ID != "REQ-0001" || req.Status != "Todo" || req.Created != t0 || req.Version != 1 || h.Get("ETag") != `"1"` {
		t.Fatalf("起票: %+v etag=%s", req.issueJSON, h.Get("ETag"))
	}
	if strings.Join(req.Labels, "|") != "auth|b c" {
		t.Errorf("labels = %q", req.Labels)
	}
	if created.Path != "open/REQ-0001-要件-ログイン-認証.md" {
		t.Errorf("path = %s", created.Path)
	}
	want := domain.NewDocument("REQ-0001", domain.NewIssueInput{Title: "要件: ログイン / 認証", Type: "requirement", Status: "Todo", Priority: "P0",
		Labels: []string{"auth", "b c"}, Body: "本文 🔴"}, t0, i18n.JA)
	if req.Markdown != mdformat.Render(want) {
		t.Errorf("起票の全文が以前の CLI の new と違う:\n%s", req.Markdown)
	}

	var task, test, blocked issueDetailJSON
	var tmp struct {
		Issue issueDetailJSON `json:"issue"`
	}
	ed.json(201, "POST", "/projects/req/issues", map[string]any{"title": "実装", "priority": "P1", "traces": []string{"req-0001"}}, &tmp)
	task = tmp.Issue
	ed.json(201, "POST", "/projects/req/issues", map[string]any{"title": "テスト", "type": "test", "traces": []string{"REQ-0001", "REQ-0999"}}, &tmp)
	test = tmp.Issue
	ed.json(201, "POST", "/projects/req/issues", map[string]any{"title": "後続", "priority": "P0", "blocked_by": []string{task.ID}}, &tmp)
	blocked = tmp.Issue

	// 入力の誤りは以前の CLI と同じ文言で 400
	if e := ed.fail(400, "POST", "/projects/req/issues", map[string]any{"title": "x", "type": "story"}); e.Error.Message != "--type は requirement / design / task / bug / test / epic のいずれかです（指定値: story）" {
		t.Errorf("type の誤り: %s", e.Error.Message)
	}
	ed.fail(400, "POST", "/projects/req/issues", map[string]any{"title": "x", "unknown": 1})
	ed.fail(400, "POST", "/projects/req/issues", map[string]any{"title": "x", "labels": []string{"a,b"}})

	// 詳細: JSON と全文（show と同じ）
	var d issueDetailJSON
	ed.json(200, "GET", "/issues/req-0002", nil, &d)
	if d.ID != task.ID || d.Body == "" || len(d.Comments) != 0 {
		t.Errorf("詳細: %+v", d)
	}
	code, hdr, md := ed.do("GET", "/issues/REQ-0002?format=md", nil)
	if code != 200 || string(md) != task.Markdown || !strings.HasPrefix(hdr.Get("Content-Type"), "text/markdown") {
		t.Errorf("format=md: %d %s", code, md)
	}
	if e := ed.fail(404, "GET", "/issues/REQ-9999", nil); e.Error.Message != "イシューが見つかりません: REQ-9999" {
		t.Errorf("404 の文言: %s", e.Error.Message)
	}

	// 一覧・ready
	var l listJSON
	ed.json(200, "GET", "/projects/req/issues", nil, &l)
	if ids(l.Items) != "REQ-0001,REQ-0004,REQ-0002,REQ-0003" || l.Count != 4 {
		t.Errorf("一覧（優先度順）: %s", ids(l.Items))
	}
	ed.json(200, "GET", "/projects/req/issues?type=test", nil, &l)
	if ids(l.Items) != "REQ-0003" {
		t.Errorf("type=test: %s", ids(l.Items))
	}
	ed.json(200, "GET", "/projects/req/issues?label=b+c", nil, &l)
	if ids(l.Items) != "REQ-0001" {
		t.Errorf("label: %s", ids(l.Items))
	}
	ed.json(200, "GET", "/projects/req/issues?sort=id&reverse=1", nil, &l)
	if ids(l.Items) != "REQ-0004,REQ-0003,REQ-0002,REQ-0001" {
		t.Errorf("sort=id reverse: %s", ids(l.Items))
	}
	if e := ed.fail(400, "GET", "/projects/req/issues?status=Doing", nil); e.Error.Message != "--status は Backlog / Todo / In Progress / In Review / Done / Canceled のいずれかです（指定値: Doing）" {
		t.Errorf("status の誤り: %s", e.Error.Message)
	}
	ed.fail(400, "GET", "/projects/req/ready?sort=size", nil)
	ed.json(200, "GET", "/projects/req/ready", nil, &l)
	if ids(l.Items) != "REQ-0001,REQ-0002,REQ-0003" {
		t.Errorf("ready（後続は未解決のため出ない）: %s", ids(l.Items))
	}

	// コメント・状態変更（コメント同時）
	e.clock.Add(2 * time.Minute)
	var cm struct {
		Issue   issueJSON `json:"issue"`
		Seq     int       `json:"seq"`
		Message string    `json:"message"`
	}
	ed.json(201, "POST", "/issues/REQ-0002/comments", map[string]any{"text": "\n原因: 1 行目\n2 行目\n"}, &cm)
	if cm.Seq != 1 || cm.Issue.Updated != t2 || cm.Issue.Version != 2 || cm.Message != "コメント追記: REQ-0002" {
		t.Errorf("コメント: %+v", cm)
	}
	var st struct {
		Issue    issueJSON `json:"issue"`
		From, To string
		Messages []string `json:"messages"`
	}
	ed.json(200, "POST", "/issues/REQ-0002/status", map[string]any{"status": "In Progress", "comment": "着手"}, &st)
	if st.From != "Todo" || st.To != "In Progress" || strings.Join(st.Messages, "|") != "REQ-0002: Todo → In Progress|コメント追記: REQ-0002" {
		t.Errorf("状態変更: %+v", st)
	}
	if e := ed.fail(400, "POST", "/issues/REQ-0002/status", map[string]any{"status": "Doing"}); !strings.HasPrefix(e.Error.Message, "status は Backlog") {
		t.Errorf("状態の誤り: %s", e.Error.Message)
	}
	ed.json(200, "GET", "/projects/req/issues?status=In+Progress", nil, &l)
	if ids(l.Items) != "REQ-0002" {
		t.Errorf("status=In Progress: %s", ids(l.Items))
	}
	ed.json(200, "POST", "/issues/REQ-0002/status", map[string]any{"status": "Done", "comment": "検証済み"}, &st)

	// 全文は domain の変更関数（以前の CLI と比較テスト済み）を同じ時刻で当てたものと一致する
	doc, _ := mdformat.Parse(task.Markdown)
	domain.AppendComment(doc, t2, "\n原因: 1 行目\n2 行目\n")
	domain.SetStatus(doc, "In Progress", t2)
	domain.AppendComment(doc, t2, "着手")
	domain.SetStatus(doc, "Done", t2)
	domain.AppendComment(doc, t2, "検証済み")
	// In Progress にした本人（ed）が担当になり、表示の frontmatter に assignee が入る（サーバだけの項目）
	if _, _, got := ed.do("GET", "/issues/REQ-0002?format=md", nil); string(got) != mdformat.Render(domain.WithAssignee(doc, "ed")) {
		t.Errorf("変更後の全文が以前の CLI の結果と違う:\n%s\n---want---\n%s", got, mdformat.Render(domain.WithAssignee(doc, "ed")))
	}

	// クローズ後: 既定の一覧から消え、status=Done / all で出る。後続が ready になる
	ed.json(200, "GET", "/projects/req/issues", nil, &l)
	if strings.Contains(ids(l.Items), "REQ-0002") {
		t.Errorf("クローズ済みが既定の一覧に出る: %s", ids(l.Items))
	}
	ed.json(200, "GET", "/projects/req/issues?status=Done", nil, &l)
	if ids(l.Items) != "REQ-0002" || !l.Items[0].Closed {
		t.Errorf("status=Done: %+v", l.Items)
	}
	ed.json(200, "GET", "/projects/req/issues?all=true&sort=status", nil, &l)
	if ids(l.Items) != "REQ-0001,REQ-0003,REQ-0004,REQ-0002" {
		t.Errorf("all sort=status: %s", ids(l.Items))
	}
	ed.json(200, "GET", "/projects/req/ready", nil, &l)
	if ids(l.Items) != "REQ-0001,REQ-0004,REQ-0003" {
		t.Errorf("クローズ後の ready: %s", ids(l.Items))
	}

	// 更新: If-Match 必須、版の不一致は 409 と現在の内容、状態・クローズ済みは 422
	ed.fail(428, "PATCH", "/issues/REQ-0001", map[string]any{"title": "x"})
	ed.fail(400, "PATCH", "/issues/REQ-0001", map[string]any{"title": "x"}, "If-Match", "abc")
	var up issueDetailJSON
	ed.json(200, "PATCH", "/issues/REQ-0001", map[string]any{"title": "要件: ログイン", "priority": "P1", "refs": []string{"FR-1"}}, &up, "If-Match", `"1"`)
	if up.Title != "要件: ログイン" || up.Priority != "P1" || strings.Join(up.Refs, ",") != "FR-1" || up.Version != 2 || up.FileName != req.FileName {
		t.Errorf("項目の更新: %+v", up.issueJSON)
	}
	conflict := ed.fail(409, "PATCH", "/issues/REQ-0001", map[string]any{"title": "古い版から"}, "If-Match", "1")
	if conflict.Error.Current == nil || conflict.Error.Current.Version != 2 || conflict.Error.Current.Title != "要件: ログイン" {
		t.Errorf("409 に現在の内容が無い: %+v", conflict)
	}
	ed.fail(400, "PATCH", "/issues/REQ-0001", map[string]any{"priority": "P9"}, "If-Match", "2")
	ed.fail(422, "PATCH", "/issues/REQ-0001", map[string]any{"status": "Done"}, "If-Match", "2")
	ed.fail(422, "PATCH", "/issues/REQ-0002", map[string]any{"title": "x"}, "If-Match", "4")
	ed.fail(400, "PATCH", "/issues/REQ-0001", map[string]any{"title": "x", "markdown": up.Markdown}, "If-Match", "2")

	// 全文での更新（edit / push）: 本文は変えられる。id・状態・コメント節は変えられない
	edited := strings.Replace(up.Markdown, "（未記入）", "背景を書いた", 1)
	ed.json(200, "PATCH", "/issues/REQ-0001", map[string]any{"markdown": edited}, &up, "If-Match", "2")
	if !strings.Contains(up.Body, "背景を書いた") || up.Version != 3 {
		t.Errorf("全文の更新: %s", up.Body)
	}
	if e := ed.fail(422, "PATCH", "/issues/REQ-0001", map[string]any{"markdown": strings.Replace(up.Markdown, "status: Todo", "status: Done", 1)}, "If-Match", "3"); e.Error.Code != "immutable_field" {
		t.Errorf("状態の変更: %+v", e)
	}
	ed.json(201, "POST", "/issues/REQ-0001/comments", map[string]any{"text": "コメント"}, nil)
	ed.json(200, "GET", "/issues/REQ-0001", nil, &up)
	if e := ed.fail(422, "PATCH", "/issues/REQ-0001", map[string]any{"markdown": strings.Replace(up.Markdown, "\nコメント", "\n書き換え", 1)}, "If-Match", "4"); e.Error.Code != "comments_changed" {
		t.Errorf("コメントの書き換え: %+v", e)
	}
	ed.fail(400, "PATCH", "/issues/REQ-0001", map[string]any{"markdown": "frontmatter なし"}, "If-Match", "4")
	// コメント追記前の写しからの全文更新は、本文だけ反映しコメントはサーバのものを残す。コメントを増やす全文は拒否
	stale := strings.Replace(edited, "背景を書いた", "背景を直した", 1)
	ed.json(200, "PATCH", "/issues/REQ-0001", map[string]any{"markdown": stale}, &up, "If-Match", "4")
	if !strings.Contains(up.Body, "背景を直した") || len(up.Comments) != 1 || up.Comments[0].Content != "コメント" || !strings.HasSuffix(up.Markdown, "\nコメント\n") {
		t.Errorf("古い写しからの更新でコメントが失われた: %+v", up.Comments)
	}
	ed.fail(422, "PATCH", "/issues/REQ-0001", map[string]any{"markdown": up.Markdown + "\n### 2026-09-17 13:09\n\n勝手に追加\n"}, "If-Match", "5")

	// matrix・summary
	var m struct {
		Rows []struct {
			Requirement issueRefJSON   `json:"requirement"`
			Task        []issueRefJSON `json:"task"`
			Test        []issueRefJSON `json:"test"`
		} `json:"rows"`
		Untested []issueRefJSON `json:"untested"`
		Orphans  []struct {
			Target string   `json:"target"`
			From   []string `json:"from"`
		} `json:"orphans"`
	}
	ed.json(200, "GET", "/projects/req/matrix", nil, &m)
	if len(m.Rows) != 1 || len(m.Rows[0].Task) != 1 || m.Rows[0].Task[0].ID != "REQ-0002" || len(m.Rows[0].Test) != 1 ||
		len(m.Untested) != 0 || len(m.Orphans) != 1 || m.Orphans[0].Target != "REQ-0999" {
		t.Errorf("matrix: %+v", m)
	}
	if _, _, md := ed.do("GET", "/projects/req/matrix?format=md", nil); !strings.Contains(string(md), "| REQ-0001 | Todo | 要件: ログイン | — | REQ-0002(Done) | REQ-0003(Todo) |") {
		t.Errorf("matrix.md: %s", md)
	}
	var sum struct {
		Counts     countsJSON  `json:"counts"`
		Ready      []issueJSON `json:"ready"`
		ReadyTotal int         `json:"ready_total"`
	}
	ed.json(200, "GET", "/projects/req/summary?limit=2", nil, &sum)
	if sum.Counts.Open != 3 || sum.Counts.Ready != 3 || sum.ReadyTotal != 3 || len(sum.Ready) != 2 || sum.Counts.ByStatus["Done"] != 1 {
		t.Errorf("summary: %+v", sum)
	}
	var projects struct {
		Projects []projectSummaryJSON `json:"projects"`
	}
	ed.json(200, "GET", "/projects", nil, &projects)
	if len(projects.Projects) != 1 || projects.Projects[0].Counts == nil || projects.Projects[0].Counts.Open != 3 || projects.Projects[0].Counter != 4 {
		t.Errorf("projects: %+v", projects)
	}

	// イベントとコメントの記録（誰が・どの経路で・どのセッションから）
	var events, withSession int
	e.db.QueryRow("SELECT COUNT(*), SUM(session_id = 'sess-1' AND via = 'cli' AND actor_user_id IS NOT NULL AND token_id IS NOT NULL) FROM issue_events WHERE project_id = ?", pr.ID).Scan(&events, &withSession)
	if events != 12 || withSession != 12 { // 11 件 + In Progress で担当が付いた assign
		t.Errorf("events=%d（cli・セッション付き %d）, want 12", events, withSession)
	}
	var authored int
	e.db.QueryRow("SELECT COUNT(*) FROM comments WHERE author_user_id IS NOT NULL AND token_id IS NOT NULL AND created_at IS NOT NULL AND via = 'cli'").Scan(&authored)
	if authored != 4 {
		t.Errorf("記録者つきコメント = %d, want 4", authored)
	}

	// 鮮度ガード: since より後のイベント
	var act struct {
		Items []activityJSON `json:"items"`
	}
	sinceEpoch := float64(e.clock.Now().UnixNano())/1e9 - 1
	e.clock.Add(time.Minute)
	ed.json(201, "POST", "/issues/REQ-0003/comments", map[string]any{"text": "x"}, nil)
	ed.json(200, "GET", fmt.Sprintf("/activity?ids=REQ-0003,req-0004,NOPE-1&since=%f", sinceEpoch), nil, &act)
	if len(act.Items) != 2 || act.Items[0].ID != "REQ-0003" || act.Items[0].EventsSince != 1 || act.Items[0].LastKind != "comment" ||
		act.Items[1].EventsSince != 0 || act.Items[1].LastKind != "create" || act.Items[0].LastEpoch <= act.Items[1].LastEpoch {
		t.Errorf("activity: %+v", act.Items)
	}
	ed.fail(400, "GET", "/activity", nil)
	_, _ = test, blocked
}

func TestIssuePermissions(t *testing.T) {
	e, pr, ed := newAPIEnv(t)
	ctx := context.Background()
	ed.json(201, "POST", "/projects/req/issues", map[string]any{"title": "a"}, nil)
	other := e.project("priv")
	adm := e.apiAs(e.adminIn("root", "root-password-1", "priv"))
	adm.json(201, "POST", "/projects/priv/issues", map[string]any{"title": "非公開"}, nil)

	viewer := e.user("vi", "vi-password-1234", "member")
	store.SetMember(ctx, e.db, pr.ID, viewer.ID, "viewer")
	vi := e.apiAs(viewer)
	vi.json(200, "GET", "/issues/REQ-0001", nil, nil)
	vi.json(200, "GET", "/projects/req/issues", nil, nil)
	vi.fail(403, "POST", "/projects/req/issues", map[string]any{"title": "b"})
	vi.fail(403, "POST", "/issues/REQ-0001/comments", map[string]any{"text": "b"})
	vi.fail(403, "POST", "/issues/REQ-0001/status", map[string]any{"status": "Done"})
	vi.fail(403, "PATCH", "/issues/REQ-0001", map[string]any{"title": "b"}, "If-Match", "1")

	// 権限の無いプロジェクトのイシューは存在しないのと同じ 404
	for _, c := range []struct{ method, path string }{
		{"GET", "/issues/PRIV-0001"}, {"POST", "/issues/PRIV-0001/comments"}, {"GET", "/projects/priv/issues"},
		{"GET", "/projects/priv/ready"}, {"GET", "/projects/priv/matrix"}, {"GET", "/projects/priv/summary"}, {"POST", "/projects/priv/issues"},
	} {
		if e := ed.fail(404, c.method, c.path, map[string]any{"text": "x", "title": "x"}); strings.Contains(e.Error.Message, "非公開") {
			t.Errorf("%s %s: 内容が漏れている", c.method, c.path)
		}
	}
	var act struct {
		Items []activityJSON `json:"items"`
	}
	ed.json(200, "GET", "/activity?ids=PRIV-0001,REQ-0001", nil, &act)
	if len(act.Items) != 1 || act.Items[0].ID != "REQ-0001" {
		t.Errorf("activity に権限の無いイシューが出る: %+v", act.Items)
	}
	// ?project= でプロジェクト内に限定（ファイルモードの find と同じ範囲）
	adm.json(200, "GET", "/issues/PRIV-0001?project=priv", nil, nil)
	adm.fail(404, "GET", "/issues/PRIV-0001?project=req", nil)
	ed.fail(404, "GET", "/issues/REQ-0001?project=priv", nil)
	_ = other

	var n int
	e.db.QueryRow("SELECT COUNT(*) FROM comments").Scan(&n)
	if n != 0 {
		t.Errorf("拒否した変更でコメントが増えた: %d", n)
	}
}

// TestConcurrentCreate は同時起票 50 件で ID が重複しないことを確かめる。
func TestConcurrentCreate(t *testing.T) {
	e, pr, ed := newAPIEnv(t)
	const n = 50
	var wg sync.WaitGroup
	var mu sync.Mutex
	got := map[string]int{}
	errs := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			code, _, b := ed.do("POST", "/projects/req/issues", map[string]any{"title": fmt.Sprintf("同時 %d", i)})
			if code != 201 {
				errs <- fmt.Sprintf("%d: %s", code, b)
				return
			}
			var res struct {
				Issue issueJSON `json:"issue"`
			}
			json.Unmarshal(b, &res)
			mu.Lock()
			got[res.Issue.ID]++
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}
	var list []string
	for id, c := range got {
		if c != 1 {
			t.Errorf("%s が %d 回発番された", id, c)
		}
		list = append(list, id)
	}
	sort.Strings(list)
	if len(list) != n || list[0] != "REQ-0001" || list[n-1] != "REQ-0050" {
		t.Errorf("発番: %d 件 %v", len(list), list)
	}
	p, _ := store.ProjectByID(context.Background(), e.db, pr.ID)
	var rows, events int
	e.db.QueryRow("SELECT COUNT(*), COUNT(DISTINCT number) FROM issues WHERE project_id = ?", pr.ID).Scan(&rows, &events)
	if p.Counter != n || rows != n || events != n {
		t.Errorf("counter=%d issues=%d 番号の種類=%d, want %d", p.Counter, rows, events, n)
	}
}

// TestConcurrentComments は同じイシューへの同時コメントが欠けずに連番で入ることを確かめる。
func TestConcurrentComments(t *testing.T) {
	e, _, ed := newAPIEnv(t)
	ed.json(201, "POST", "/projects/req/issues", map[string]any{"title": "a"}, nil)
	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if code, _, b := ed.do("POST", "/issues/REQ-0001/comments", map[string]any{"text": fmt.Sprint(i)}); code != 201 {
				t.Errorf("%d: %s", code, b)
			}
		}(i)
	}
	wg.Wait()
	var d issueDetailJSON
	ed.json(200, "GET", "/issues/REQ-0001", nil, &d)
	var maxSeq, cnt int
	e.db.QueryRow("SELECT MAX(seq), COUNT(*) FROM comments").Scan(&maxSeq, &cnt)
	if len(d.Comments) != n || d.Version != n+1 || maxSeq != n || cnt != n {
		t.Errorf("comments=%d version=%d maxSeq=%d count=%d", len(d.Comments), d.Version, maxSeq, cnt)
	}
}

// TestWebSessionAPI はセッションでも API を使え、変更は CSRF トークン必須で via=web になることを確かめる。
func TestWebSessionAPI(t *testing.T) {
	e, pr, _ := newAPIEnv(t)
	u := e.user("web", "web-password-123", "member")
	store.SetMember(context.Background(), e.db, pr.ID, u.ID, "editor")
	c := e.client()
	e.enroll(c, "web", "web-password-123")
	var csrf string
	e.db.QueryRow("SELECT csrf_token FROM web_sessions WHERE user_id = ? AND mfa_passed", u.ID).Scan(&csrf)
	post := func(token string) int {
		req, _ := http.NewRequest("POST", e.srv.URL+"/im/api/v1/projects/req/issues", strings.NewReader(`{"title":"web"}`))
		if token != "" {
			req.Header.Set("X-CSRF-Token", token)
		}
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if code := post(""); code != http.StatusForbidden {
		t.Errorf("CSRF なし: %d", code)
	}
	if code := post(csrf); code != http.StatusCreated {
		t.Fatalf("CSRF あり: %d", code)
	}
	var via string
	e.db.QueryRow("SELECT via FROM issue_events WHERE kind = 'create'").Scan(&via)
	if via != "web" {
		t.Errorf("via = %s", via)
	}
}
