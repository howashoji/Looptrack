package server

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/store"
)

// プロジェクト管理画面（/im/admin/projects）からの表示名の変更・アーカイブ・戻す。

// projectSlugs は GET /projects の slug を並び順で返す（projectsListJSON は admin_projects_test.go）。
func (a *apiClient) projectSlugs(query string) string {
	a.e.t.Helper()
	var got projectsListJSON
	a.json(200, "GET", "/projects"+query, nil, &got)
	var out []string
	for _, p := range got.Projects {
		out = append(out, p.Slug)
	}
	return strings.Join(out, ",")
}

// 管理者は表示名を変えられ、一覧・ボード・MCP に新しい名前が出る。slug・prefix・採番・発番済みの ID は変わらない。
// 管理者でない利用者にはボタンが出ず（管理画面そのものが 404）、要求を直接送っても 403 で名前は変わらない。
func TestAdminProjectRenameWeb(t *testing.T) {
	e, pr, ed := newAPIEnv(t)
	e.user("root", "root-password-12", "admin")
	ed.json(201, "POST", "/projects/req/issues", map[string]any{"title": "改名の前の課題"}, nil)
	before, _ := store.ProjectBySlug(context.Background(), e.db, "req")

	mc := e.client()
	e.enroll(mc, "ed", "ed-password-123")
	if res, page := e.get(mc, "/im/admin/projects"); res.StatusCode != http.StatusNotFound || strings.Contains(page, "/admin/projects/req/rename") {
		t.Errorf("member の管理画面: %d", res.StatusCode)
	}
	if res, _ := e.formAt(mc, "/im/account", "/im/admin/projects/req/rename", url.Values{"name": {"乗っ取り"}}); res.StatusCode != http.StatusForbidden {
		t.Errorf("member の改名: %d", res.StatusCode)
	}

	c := e.client()
	e.enroll(c, "root", "root-password-12")
	res, page := e.get(c, "/im/admin/projects")
	if res.StatusCode != 200 || !strings.Contains(page, `action="/im/admin/projects/req/rename"`) || !strings.Contains(page, `action="/im/admin/projects/req/archive"`) {
		t.Fatalf("管理者の画面にボタンが無い: %d %s", res.StatusCode, page)
	}
	res, page = e.formAt(c, "/im/admin/projects", "/im/admin/projects/req/rename", url.Values{"name": {" 新しい名前 "}})
	if res.StatusCode != 200 || !strings.Contains(page, "表示名を変更: req（req → 新しい名前）") {
		t.Fatalf("改名: %d %s", res.StatusCode, page)
	}
	after, _ := store.ProjectBySlug(context.Background(), e.db, "req")
	if after.Name != "新しい名前" || after.Slug != "req" || after.Prefix != before.Prefix || after.Width != before.Width || after.Counter != before.Counter || after.ID != pr.ID {
		t.Errorf("改名の後: %+v（前 %+v）", after, before)
	}
	var got projectsListJSON
	ed.json(200, "GET", "/projects", nil, &got)
	if len(got.Projects) != 1 || got.Projects[0].Name != "新しい名前" {
		t.Errorf("一覧の名前: %+v", got)
	}
	ed.json(200, "GET", "/issues/REQ-0001", nil, nil) // 発番済みの ID はそのまま
	if _, board := e.get(mc, "/im/p/req/"); !strings.Contains(board, "新しい名前") {
		t.Error("ボードに新しい名前が出ない")
	}
	if text, _ := e.mcpAs(ed.token, nil).call("list_projects", nil, false); !strings.Contains(text, "新しい名前") {
		t.Errorf("MCP の一覧: %s", text)
	}
	// 空の名前は拒む（名前は変わらない）
	if res, _ := e.formAt(c, "/im/admin/projects", "/im/admin/projects/req/rename", url.Values{"name": {"  "}}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("空の名前: %d", res.StatusCode)
	}
	if p, _ := store.ProjectBySlug(context.Background(), e.db, "req"); p.Name != "新しい名前" {
		t.Errorf("拒否の後の名前: %q", p.Name)
	}
}

// 管理者がアーカイブすると、Web（ハブが読む REST の一覧・ボード）・MCP の list_projects・CLI（REST）の一覧から消え、
// 起票・状態の変更・コメントが REST・MCP・CLI のどの経路でも拒まれる。イシュー・コメント・イベントは DB に残り、
// 戻すと元どおり一覧に出て操作できる。対照のプロジェクト ex は同じ操作が通り続ける。管理者でなければ 403。
func TestAdminProjectArchiveWeb(t *testing.T) {
	e, pr, ed := newAPIEnv(t)
	ctx := context.Background()
	ex := e.project("ex")
	edUser, _ := store.UserByLogin(ctx, e.db, "ed")
	if err := store.SetMember(ctx, e.db, ex.ID, edUser.ID, "editor"); err != nil {
		t.Fatal(err)
	}
	root := e.user("root", "root-password-12", "admin")
	rootAPI := e.apiAs(root)
	ed.json(201, "POST", "/projects/req/issues", map[string]any{"title": "アーカイブ前の課題"}, nil)
	ed.json(201, "POST", "/issues/REQ-0001/comments", map[string]any{"text": "アーカイブ前のコメント"}, nil)
	ed.json(201, "POST", "/projects/ex/issues", map[string]any{"title": "対照の課題"}, nil)
	count := func(q string) int {
		t.Helper()
		var n int
		if err := e.db.QueryRowContext(ctx, q, pr.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	const (
		qIssues   = "SELECT COUNT(*) FROM issues WHERE project_id = ?"
		qComments = "SELECT COUNT(*) FROM comments c JOIN issues i ON i.id = c.issue_id WHERE i.project_id = ?"
		qEvents   = "SELECT COUNT(*) FROM issue_events WHERE project_id = ?"
	)
	nIssues, nComments, nEvents := count(qIssues), count(qComments), count(qEvents)
	archived := func() bool {
		p, err := store.ProjectBySlug(ctx, e.db, "req")
		if err != nil {
			t.Fatal(err)
		}
		return p.Archived
	}
	m := e.mcpAs(ed.token, nil)
	if text, _ := m.call("list_projects", nil, false); !strings.Contains(text, "req（") || !strings.Contains(text, "ex（") {
		t.Fatalf("前提が崩れている: アーカイブ前の MCP の一覧に両方が出ない: %s", text)
	}

	// 管理者でなければ 403（アーカイブも戻すも）
	mc := e.client()
	e.enroll(mc, "ed", "ed-password-123")
	if res, _ := e.formAt(mc, "/im/account", "/im/admin/projects/req/archive", url.Values{"confirm": {"req"}}); res.StatusCode != http.StatusForbidden || archived() {
		t.Errorf("member のアーカイブ: %d archived=%v", res.StatusCode, archived())
	}

	// 確認（slug の入力）が無い・違うなら拒む
	c := e.client()
	e.enroll(c, "root", "root-password-12")
	for _, confirm := range []string{"", "ex", "REQ"} {
		if res, page := e.formAt(c, "/im/admin/projects", "/im/admin/projects/req/archive", url.Values{"confirm": {confirm}}); res.StatusCode != http.StatusBadRequest || archived() ||
			!strings.Contains(page, "確認の欄に slug（req）をそのまま入力してください") {
			t.Errorf("確認 %q: %d archived=%v", confirm, res.StatusCode, archived())
		}
	}
	res, page := e.formAt(c, "/im/admin/projects", "/im/admin/projects/req/archive", url.Values{"confirm": {"req"}})
	if res.StatusCode != 200 || !archived() || !strings.Contains(page, "アーカイブ: req") ||
		!strings.Contains(page, `id="archived-req"`) || strings.Contains(page, `id="p-req"`) || !strings.Contains(page, `id="p-ex"`) ||
		!strings.Contains(page, `action="/im/admin/projects/req/unarchive"`) {
		t.Fatalf("アーカイブ: %d archived=%v %s", res.StatusCode, archived(), page)
	}

	// 一覧から消える: REST（ハブと CLI が読む）・管理者の ?all=1・MCP。ボードも開けない
	cli := []string{"X-Looptrack-Client", "cli"}
	if got := ed.projectSlugs(""); got != "ex" {
		t.Errorf("REST の一覧: %q", got)
	}
	if got := rootAPI.projectSlugs("?all=1"); got != "ex" {
		t.Errorf("管理者の ?all=1: %q", got)
	}
	if text, _ := m.call("list_projects", nil, false); strings.Contains(text, "req（") || !strings.Contains(text, "ex（") {
		t.Errorf("MCP の一覧: %s", text)
	}
	if res, _ := e.get(mc, "/im/p/req/"); res.StatusCode != http.StatusNotFound {
		t.Errorf("ボード: %d", res.StatusCode)
	}
	if res, _ := e.get(mc, "/im/p/ex/"); res.StatusCode != http.StatusOK {
		t.Errorf("対照のボード: %d", res.StatusCode)
	}

	// 書き込みはどの経路でも拒む（REST・CLI の見出し付きの REST・MCP）。対照の ex は通る
	for _, h := range [][]string{nil, cli} {
		ed.fail(404, "POST", "/projects/req/issues", map[string]any{"title": "x"}, h...)
		ed.fail(404, "POST", "/issues/REQ-0001/comments", map[string]any{"text": "x"}, h...)
		ed.fail(404, "POST", "/issues/REQ-0001/status", map[string]any{"status": "In Progress"}, h...)
		ed.json(201, "POST", "/projects/ex/issues", map[string]any{"title": "対照"}, nil, h...)
	}
	m.call("create_issue", map[string]any{"project": "req", "title": "x"}, true)
	m.call("add_comment", map[string]any{"id": "REQ-0001", "text": "x"}, true)
	m.call("set_status", map[string]any{"id": "REQ-0001", "status": "In Progress"}, true)
	m.call("create_issue", map[string]any{"project": "ex", "title": "対照"}, false)

	// DB には残る
	if count(qIssues) != nIssues || count(qComments) != nComments || count(qEvents) != nEvents {
		t.Errorf("アーカイブで行が変わった: issues %d→%d comments %d→%d events %d→%d",
			nIssues, count(qIssues), nComments, count(qComments), nEvents, count(qEvents))
	}

	// 戻す（管理者でなければ 403）
	if res, _ := e.formAt(mc, "/im/account", "/im/admin/projects/req/unarchive", url.Values{}); res.StatusCode != http.StatusForbidden || !archived() {
		t.Errorf("member の戻す: %d archived=%v", res.StatusCode, archived())
	}
	res, page = e.formAt(c, "/im/admin/projects", "/im/admin/projects/req/unarchive", url.Values{})
	if res.StatusCode != 200 || archived() || !strings.Contains(page, "アーカイブから戻す: req") || !strings.Contains(page, `id="p-req"`) || strings.Contains(page, `id="archived-req"`) {
		t.Fatalf("戻す: %d archived=%v %s", res.StatusCode, archived(), page)
	}
	if got := ed.projectSlugs(""); got != "ex,req" && got != "req,ex" {
		t.Errorf("戻した後の REST の一覧: %q", got)
	}
	if text, _ := m.call("list_projects", nil, false); !strings.Contains(text, "req（") {
		t.Errorf("戻した後の MCP の一覧: %s", text)
	}
	ed.json(201, "POST", "/issues/REQ-0001/comments", map[string]any{"text": "戻した後のコメント"}, nil, cli...)
	ed.json(200, "POST", "/issues/REQ-0001/status", map[string]any{"status": "In Progress"}, nil)
	var created struct {
		Issue issueDetailJSON `json:"issue"`
	}
	ed.json(201, "POST", "/projects/req/issues", map[string]any{"title": "戻した後の課題"}, &created)
	if created.Issue.ID != "REQ-0002" {
		t.Errorf("戻した後の採番: %s", created.Issue.ID)
	}
	m.call("add_comment", map[string]any{"id": "REQ-0001", "text": "MCP から"}, false)
}
