package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/store"
)

// 管理者の既定表示は参加しているプロジェクトだけ。全プロジェクトの管理は /im/admin/projects。
// slug を指定した操作は、参加していなくても読める（ただし書けない。以前は管理者権限で書けた）。

type projectsListJSON struct {
	Projects []projectSummaryJSON `json:"projects"`
}

func slugsOf(ps []projectSummaryJSON) string {
	var out []string
	for _, p := range ps {
		m := "?"
		if p.Member != nil {
			m = map[bool]string{true: "m", false: "-"}[*p.Member]
		}
		out = append(out, p.Slug+":"+p.Role+":"+m)
	}
	return strings.Join(out, ",")
}

func TestAdminDefaultProjects(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, b := e.project("aaa"), e.project("bbb")
	e.project("ccc")
	e.project("ddd")
	root := e.user("root", "root-password-12", "admin")
	dave := e.user("dave", "dave-password-12", "member")
	store.SetMember(ctx, e.db, a.ID, root.ID, "editor")
	store.SetMember(ctx, e.db, b.ID, root.ID, "viewer")
	store.SetMember(ctx, e.db, a.ID, dave.ID, "viewer")
	adm, mem := e.apiAs(root), e.apiAs(dave)

	// 既定は参加している 2 件だけ（役割は project_members の値）
	var got projectsListJSON
	adm.json(200, "GET", "/projects", nil, &got)
	if s := slugsOf(got.Projects); s != "aaa:editor:m,bbb:viewer:m" {
		t.Errorf("admin の既定の一覧: %s", s)
	}
	// ?all=1 は全件（参加していないものは member false。役割は admin ではなく viewer（閲覧のみ））
	adm.json(200, "GET", "/projects?all=1", nil, &got)
	if s := slugsOf(got.Projects); s != "aaa:editor:m,bbb:viewer:m,ccc:viewer:-,ddd:viewer:-" {
		t.Errorf("admin の ?all=1: %s", s)
	}
	adm.json(200, "GET", "/projects?all=0", nil, &got)
	if len(got.Projects) != 2 {
		t.Errorf("?all=0: %s", slugsOf(got.Projects))
	}
	adm.fail(400, "GET", "/projects?all=yes", nil)
	// member は既定の一覧が変わらず、?all=1 は 403
	mem.json(200, "GET", "/projects", nil, &got)
	if s := slugsOf(got.Projects); s != "aaa:viewer:m" {
		t.Errorf("member の一覧: %s", s)
	}
	if e := mem.fail(403, "GET", "/projects?all=1", nil); e.Error.Code != "forbidden" {
		t.Errorf("member の ?all=1: %+v", e)
	}
	mem.fail(404, "GET", "/projects/ccc", nil)
	mem.fail(404, "GET", "/projects/ccc/issues", nil)

	// 参加していない slug を直接指定すると読める。役割は viewer で書けない（以前は admin で書けた）
	var one projectSummaryJSON
	adm.json(200, "GET", "/projects/ccc", nil, &one)
	if one.Role != "viewer" || one.Member == nil || *one.Member {
		t.Errorf("参加していない slug: role=%s member=%v", one.Role, one.Member)
	}
	adm.json(200, "GET", "/projects/bbb", nil, &one)
	// 参加しているプロジェクトは project_members の役割で動く（以前は解決が admin のままだった）
	if one.Role != "viewer" || one.Member == nil || !*one.Member {
		t.Errorf("viewer で参加している slug（役割は project_members の値）: role=%s member=%v", one.Role, one.Member)
	}
	var cr struct {
		Issue issueDetailJSON `json:"issue"`
	}
	adm.fail(403, "POST", "/projects/ccc/issues", map[string]any{"title": "参加していないプロジェクトの起票"})
	// 参加させれば書ける。外すと読むだけに戻る
	store.SetMember(ctx, e.db, e.projectID("ccc"), root.ID, "editor")
	adm.json(201, "POST", "/projects/ccc/issues", map[string]any{"title": "参加していないプロジェクトの起票"}, &cr)
	store.RemoveMember(ctx, e.db, e.projectID("ccc"), root.ID)
	created := cr.Issue
	var list listJSON
	adm.json(200, "GET", "/projects/ccc/issues", nil, &list)
	if list.Count != 1 || list.Items[0].ID != created.ID {
		t.Errorf("参加していないプロジェクトの一覧: %+v", list)
	}
	adm.fail(403, "POST", "/issues/"+created.ID+"/comments?project=ccc", map[string]any{"text": "管理者のコメント"})
	adm.json(200, "GET", "/issues/"+created.ID, nil, nil) // slug なしの ID 解決も全件から
	mem.fail(404, "GET", "/issues/"+created.ID, nil)

	// 画面: ハブは管理者にプロジェクト管理への導線を出す。参加していない /im/p/<slug>/ も開ける
	c := e.client()
	e.enroll(c, "root", "root-password-12")
	res, body := e.get(c, "/im/")
	if res.StatusCode != 200 || !strings.Contains(body, `data-admin="1"`) || !strings.Contains(body, `href="/im/admin/projects"`) {
		t.Errorf("admin のハブ: %d %s", res.StatusCode, body)
	}
	res, body = e.get(c, "/im/api/v1/projects")
	if json.Unmarshal([]byte(body), &got); res.StatusCode != 200 || len(got.Projects) != 2 {
		t.Errorf("セッションの /api/v1/projects: %d %s", res.StatusCode, body)
	}
	if res, body := e.get(c, "/im/p/ddd/"); res.StatusCode != 200 || !strings.Contains(body, `data-slug="ddd"`) {
		t.Errorf("参加していないボード: %d", res.StatusCode)
	}
	mc := e.client()
	e.enroll(mc, "dave", "dave-password-12")
	if _, body := e.get(mc, "/im/"); strings.Contains(body, `data-admin`) || strings.Contains(body, "/im/admin/projects") {
		t.Error("member のハブに管理者の導線が出ている")
	}
	if res, _ := e.get(mc, "/im/p/ddd/"); res.StatusCode != http.StatusNotFound {
		t.Errorf("member の参加していないボード: %d", res.StatusCode)
	}
}

func TestAdminProjectsPage(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.project("aaa")
	e.project("bbb")
	root := e.user("root", "root-password-12", "admin")
	dave := e.user("dave", "dave-password-12", "member")
	e.user("erin", "erin-password-12", "member")
	store.SetMember(ctx, e.db, a.ID, root.ID, "editor")
	store.SetMember(ctx, e.db, a.ID, dave.ID, "viewer")

	// member は 404（GET も POST も）
	mc := e.client()
	e.enroll(mc, "dave", "dave-password-12")
	if res, body := e.get(mc, "/im/admin/projects"); res.StatusCode != http.StatusNotFound || strings.Contains(body, "bbb") {
		t.Errorf("member の GET: %d", res.StatusCode)
	}
	if res, _ := e.formAt(mc, "/im/account", "/im/admin/projects/bbb/member", url.Values{"login": {"dave"}, "role": {"admin"}}); res.StatusCode != http.StatusNotFound {
		t.Errorf("member の POST: %d", res.StatusCode)
	}
	if r, _ := store.MemberRole(ctx, e.db, e.projectID("bbb"), dave.ID); r != "" {
		t.Fatalf("member の POST で権限が付いた: %s", r)
	}

	c := e.client()
	e.enroll(c, "root", "root-password-12")
	res, page := e.get(c, "/im/admin/projects")
	if res.StatusCode != 200 || !strings.Contains(page, `id="p-aaa"`) || !strings.Contains(page, `id="p-bbb"`) ||
		!strings.Contains(page, "自分は editor で参加") || !strings.Contains(page, "自分は参加していない") ||
		!strings.Contains(page, `href="/im/p/bbb/"`) || !strings.Contains(page, `href="/im/admin/users/dave"`) || strings.Contains(page, "<script") {
		t.Fatalf("プロジェクト管理: %d %s", res.StatusCode, page)
	}
	// メニューにも出る
	if !strings.Contains(page, `class="usermenu-item" href="/im/admin/projects"`) {
		t.Error("メニューにプロジェクト管理が無い")
	}
	// CSRF なしは 403 で何も変えない
	if res, _ := e.post(c, "/im/admin/projects/bbb/member", url.Values{"login": {"erin"}, "role": {"editor"}}); res.StatusCode != http.StatusForbidden {
		t.Errorf("CSRF なし: %d", res.StatusCode)
	}
	// 付与・変更・解除（自分自身の参加も変えられる）
	post := func(slug, login, role string, want int) string {
		t.Helper()
		res, body := e.formAt(c, "/im/admin/projects", "/im/admin/projects/"+slug+"/member", url.Values{"login": {login}, "role": {role}})
		if res.StatusCode != want {
			t.Errorf("%s %s %s: %d, want %d", slug, login, role, res.StatusCode, want)
		}
		return body
	}
	post("bbb", "erin", "editor", 200)
	post("aaa", "dave", "editor", 200)
	post("aaa", "root", "", 200)
	post("bbb", "root", "viewer", 200)
	post("bbb", "nobody", "viewer", 400)
	post("bbb", "erin", "owner", 400)
	post("zzz", "erin", "viewer", 404)
	roles := func(slug string) string {
		ms, _ := store.ListMembers(ctx, e.db, e.projectID(slug))
		var out []string
		for _, m := range ms {
			out = append(out, m.Login+":"+m.Role)
		}
		return strings.Join(out, ",")
	}
	if got := roles("aaa"); got != "dave:editor" {
		t.Errorf("aaa の参加者: %s", got)
	}
	if got := roles("bbb"); got != "erin:editor,root:viewer" {
		t.Errorf("bbb の参加者: %s", got)
	}
	// 自分の参加を外すと一覧は参加分に変わり、外したプロジェクトは読むだけになる（以前は書けた）
	adm := e.apiAs(root)
	var got projectsListJSON
	adm.json(200, "GET", "/projects", nil, &got)
	if s := slugsOf(got.Projects); s != "bbb:viewer:m" {
		t.Errorf("変更後の admin の一覧: %s", s)
	}
	adm.json(200, "GET", "/projects/aaa/issues", nil, nil)
	adm.fail(403, "POST", "/projects/aaa/issues", map[string]any{"title": "参加を外したので書けない"})
	// viewer で参加している bbb でも管理画面の操作は影響を受けない。自分の役割を上げれば書ける
	post("bbb", "erin", "viewer", 200)
	adm.fail(403, "POST", "/projects/bbb/issues", map[string]any{"title": "viewer では書けない"})
	post("bbb", "root", "editor", 200)
	adm.json(201, "POST", "/projects/bbb/issues", map[string]any{"title": "役割を上げたので書ける"}, nil)
	post("bbb", "erin", "editor", 200)

	// 利用者の画面の「プロジェクト権限」と同じ結果になる（両方向）
	if res, _ := e.formAt(c, "/im/admin/users/erin", "/im/admin/users/erin/member", url.Values{"project": {"aaa"}, "role": {"viewer"}}); res.StatusCode != 200 {
		t.Errorf("利用者の画面から付与: %d", res.StatusCode)
	}
	_, page = e.get(c, "/im/admin/projects")
	sec := page[strings.Index(page, `id="p-aaa"`):strings.Index(page, `id="p-bbb"`)]
	if !strings.Contains(sec, `value="erin"`) || !strings.Contains(sec, `<option value="viewer" selected>`) {
		t.Errorf("利用者の画面の付与がプロジェクト管理に出ない: %s", sec)
	}
	post("aaa", "erin", "admin", 200)
	_, upage := e.get(c, "/im/admin/users/erin")
	row := upage[strings.Index(upage, `name="project" value="aaa"`):]
	row = row[:strings.Index(row, "</form>")]
	if !strings.Contains(row, `<option value="admin" selected>`) {
		t.Errorf("プロジェクト管理の変更が利用者の画面に出ない: %s", row)
	}
}

// projectID は slug のプロジェクトの ID。
func (e *env) projectID(slug string) int64 {
	e.t.Helper()
	pr, err := store.ProjectBySlug(context.Background(), e.db, slug)
	if err != nil {
		e.t.Fatal(err)
	}
	return pr.ID
}

func TestAdminMCPDefaultProject(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.project("aaa")
	e.project("bbb")
	root := e.user("root", "root-password-12", "admin")
	store.SetMember(ctx, e.db, a.ID, root.ID, "editor")
	adm := e.apiAs(root)
	var cr struct {
		Issue issueDetailJSON `json:"issue"`
	}
	// 参加していない側の 1 件は、一時的に参加して起票しておく（参加していないと書けない）
	store.SetMember(ctx, e.db, e.projectID("bbb"), root.ID, "editor")
	adm.json(201, "POST", "/projects/bbb/issues", map[string]any{"title": "参加していない側"}, &cr)
	store.RemoveMember(ctx, e.db, e.projectID("bbb"), root.ID)
	created := cr.Issue

	m := e.mcpAs(adm.token, nil)
	text, data := m.call("list_projects", nil, false)
	if ps, _ := data["projects"].([]any); len(ps) != 1 || !strings.Contains(text, "aaa") || strings.Contains(text, "bbb") {
		t.Errorf("list_projects: %s", text)
	}
	// project 省略・ヘッダなし: 参加している唯一のプロジェクト
	m.call("project_summary", nil, false)
	if text, _ := m.call("create_issue", map[string]any{"title": "既定で起票"}, false); !strings.HasPrefix(text, "作成: AAA-0001") {
		t.Errorf("既定で起票した先: %s", text)
	}
	// 参加していないプロジェクトも project で指定すれば読める（書けない）
	if text, _ := m.call("get_issue", map[string]any{"id": created.ID, "project": "bbb"}, false); !strings.Contains(text, "参加していない側") {
		t.Errorf("get_issue（参加していない）: %s", text)
	}
	if text, _ := m.call("get_issue", map[string]any{"id": created.ID}, false); !strings.Contains(text, "参加していない側") {
		t.Errorf("get_issue（project 省略）: %s", text)
	}
	if text, _ := m.call("add_comment", map[string]any{"id": created.ID, "project": "bbb", "text": "管理者のコメント"}, true); !strings.Contains(text, "閲覧のみ") {
		t.Errorf("add_comment（参加していない）: %s", text)
	}
	// 参加が 0 件なら指定を求める
	store.RemoveMember(ctx, e.db, a.ID, root.ID)
	if text, _ := m.call("project_summary", nil, true); !strings.Contains(text, "参加しているプロジェクトがありません") {
		t.Errorf("参加 0 件: %s", text)
	}
}

// CLI: LOOPTRACK_PROJECT で参加していないプロジェクトを指定しても読め（ただし書けない）、config は参加していないことを知らせる。
func TestAdminCLINonMember(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.project("aaa")
	e.project("bbb")
	root := e.user("root", "root-password-12", "admin")
	store.SetMember(ctx, e.db, a.ID, root.ID, "editor")
	adm := e.apiAs(root)
	store.SetMember(ctx, e.db, e.projectID("bbb"), root.ID, "editor") // 起票のためだけに一時的に参加する
	adm.json(201, "POST", "/projects/bbb/issues", map[string]any{"title": "参加していない側"}, nil)
	store.RemoveMember(ctx, e.db, e.projectID("bbb"), root.ID)
	home := t.TempDir()
	run := func(slug string, args ...string) cliResult {
		t.Helper()
		env := append(cliAPIEnv(e.srv.URL+"/im", slug, adm.token, home), "CLAUDE_PROJECT_DIR="+home, "LOOPTRACK_USAGE=0")
		return runCLI(t, home, env, "", append([]string{"issue"}, args...)...)
	}
	if r := run("bbb", "list"); r.code != 0 || !strings.Contains(r.stdout, "参加していない側") {
		t.Errorf("LOOPTRACK_PROJECT=bbb list: %+v", r)
	}
	// 参加していないプロジェクトの役割は viewer（以前は admin）で、config は閲覧のみと知らせる
	if r := run("bbb", "config"); r.code != 0 || !strings.Contains(r.stdout, "権限 viewer") || !strings.Contains(r.stdout, "参加していません（閲覧のみ") {
		t.Errorf("LOOPTRACK_PROJECT=bbb config: %+v", r)
	}
	if r := run("bbb", "comment", "BBB-0001", "参加していないので書けない"); r.code == 0 {
		t.Errorf("LOOPTRACK_PROJECT=bbb comment（参加していない）が通った: %+v", r)
	}
	if r := run("aaa", "config"); r.code != 0 || strings.Contains(r.stdout, "参加していません") {
		t.Errorf("LOOPTRACK_PROJECT=aaa config: %+v", r)
	}
	var cfg map[string]any
	r := run("bbb", "config", "--json")
	if err := json.Unmarshal([]byte(r.stdout), &cfg); err != nil || cfg["member"] != false || cfg["role"] != "viewer" {
		t.Errorf("config --json: %s %v", r.stdout, err)
	}
	// viewer で参加したプロジェクトは LOOPTRACK_PROJECT で指定しても読むだけ
	store.SetMember(ctx, e.db, e.projectID("bbb"), root.ID, "viewer")
	if r := run("bbb", "config"); r.code != 0 || !strings.Contains(r.stdout, "権限 viewer") || strings.Contains(r.stdout, "参加していません") {
		t.Errorf("LOOPTRACK_PROJECT=bbb config（viewer）: %+v", r)
	}
	if r := run("bbb", "show", "BBB-0001"); r.code != 0 || !strings.Contains(r.stdout, "参加していない側") {
		t.Errorf("LOOPTRACK_PROJECT=bbb show（viewer）: %+v", r)
	}
	if r := run("bbb", "comment", "BBB-0001", "書けない"); r.code == 0 {
		t.Errorf("LOOPTRACK_PROJECT=bbb comment（viewer）が通った: %+v", r)
	}
	if r := run("bbb", "status", "BBB-0001", "In Progress"); r.code == 0 {
		t.Errorf("LOOPTRACK_PROJECT=bbb status（viewer）が通った: %+v", r)
	}
}

// 管理者でも、参加しているプロジェクトでは project_members の役割で動く（viewer は読むだけ）。
// 参加していないプロジェクトも読むだけ（以前は役割 admin で書けた）。画面・REST・MCP・CLI で同じ判定
// （store.AccessibleProjects）。
func TestAdminMemberRole(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v := e.project("vvv")
	nn := e.project("nnn")
	root := e.adminIn("root", "root-password-12", "vvv", "nnn")
	adm := e.apiAs(root)
	// editor で参加して両方に 1 件ずつ起票してから、vvv は viewer に下げ、nnn は参加を外す
	adm.json(201, "POST", "/projects/vvv/issues", map[string]any{"title": "viewer 側"}, nil)
	adm.json(201, "POST", "/projects/nnn/issues", map[string]any{"title": "参加していない側"}, nil)
	store.SetMember(ctx, e.db, v.ID, root.ID, "viewer")
	store.RemoveMember(ctx, e.db, nn.ID, root.ID)

	// REST: viewer で参加している vvv は読めるが書けない
	var one projectSummaryJSON
	adm.json(200, "GET", "/projects/vvv", nil, &one)
	if one.Role != "viewer" || one.Member == nil || !*one.Member {
		t.Errorf("vvv: role=%s member=%v", one.Role, one.Member)
	}
	adm.json(200, "GET", "/projects/vvv/issues", nil, nil)
	adm.json(200, "GET", "/issues/VVV-0001", nil, nil)
	adm.fail(403, "POST", "/issues/VVV-0001/status", map[string]any{"status": "In Progress"})
	adm.fail(403, "POST", "/issues/VVV-0001/comments", map[string]any{"text": "書けない"})
	adm.fail(403, "POST", "/issues/VVV-0001/comments?project=vvv", map[string]any{"text": "書けない"})
	adm.fail(403, "POST", "/projects/vvv/issues", map[string]any{"title": "書けない"})
	// 参加していない nnn も読めるが書けない（役割 viewer・member false。以前は役割 admin で書けた）
	adm.json(200, "GET", "/projects/nnn", nil, &one)
	if one.Role != "viewer" || one.Member == nil || *one.Member {
		t.Errorf("nnn: role=%s member=%v", one.Role, one.Member)
	}
	adm.json(200, "GET", "/projects/nnn/issues", nil, nil)
	adm.json(200, "GET", "/issues/NNN-0001", nil, nil)
	adm.fail(403, "POST", "/issues/NNN-0001/status", map[string]any{"status": "In Progress"})
	adm.fail(403, "POST", "/issues/NNN-0001/comments", map[string]any{"text": "書けない"})
	adm.fail(403, "POST", "/projects/nnn/issues", map[string]any{"title": "書けない"})

	// MCP: 同じ判定
	m := e.mcpAs(adm.token, nil)
	if text, _ := m.call("get_issue", map[string]any{"id": "VVV-0001", "project": "vvv"}, false); !strings.Contains(text, "viewer 側") {
		t.Errorf("MCP get_issue（viewer）: %s", text)
	}
	m.call("list_issues", map[string]any{"project": "vvv"}, false)
	if text, _ := m.call("get_issue", map[string]any{"id": "NNN-0001", "project": "nnn"}, false); !strings.Contains(text, "参加していない側") {
		t.Errorf("MCP get_issue（参加していない）: %s", text)
	}
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"create_issue", map[string]any{"project": "vvv", "title": "b"}},
		{"add_comment", map[string]any{"id": "VVV-0001", "project": "vvv", "text": "b"}},
		{"add_comment", map[string]any{"id": "VVV-0001", "text": "b"}},
		{"set_status", map[string]any{"id": "VVV-0001", "project": "vvv", "status": "In Progress"}},
		{"update_issue", map[string]any{"id": "VVV-0001", "project": "vvv", "version": 1, "title": "b"}},
		// 参加していないプロジェクトも同じ
		{"create_issue", map[string]any{"project": "nnn", "title": "b"}},
		{"add_comment", map[string]any{"id": "NNN-0001", "project": "nnn", "text": "b"}},
		{"add_comment", map[string]any{"id": "NNN-0001", "text": "b"}},
		{"set_status", map[string]any{"id": "NNN-0001", "project": "nnn", "status": "In Progress"}},
		{"update_issue", map[string]any{"id": "NNN-0001", "project": "nnn", "version": 1, "title": "b"}},
	} {
		if text, _ := m.call(c.tool, c.args, true); !strings.Contains(text, "閲覧のみ") {
			t.Errorf("MCP %s %v: %s", c.tool, c.args["project"], text)
		}
	}
	// 自分を参加させれば書ける（/im/admin/projects と同じ store.SetMember）
	store.SetMember(ctx, e.db, nn.ID, root.ID, "editor")
	m.call("add_comment", map[string]any{"id": "NNN-0001", "project": "nnn", "text": "MCP から"}, false)
	m.call("set_status", map[string]any{"id": "NNN-0001", "project": "nnn", "status": "In Review"}, false)
	store.RemoveMember(ctx, e.db, nn.ID, root.ID)

	// 画面: ボードは開けて、変更フォームは出さない（can_edit false）
	c := e.client()
	e.enroll(c, "root", "root-password-12")
	if res, _ := e.get(c, "/im/p/vvv/"); res.StatusCode != 200 {
		t.Errorf("/im/p/vvv/: %d", res.StatusCode)
	}
	var board struct {
		CanEdit bool `json:"can_edit"`
	}
	for slug, want := range map[string]bool{"vvv": false, "nnn": false} {
		res, body := e.get(c, "/im/api/v1/projects/"+slug+"/board")
		if err := json.Unmarshal([]byte(body), &board); err != nil || res.StatusCode != 200 || board.CanEdit != want {
			t.Errorf("%s の board: %d can_edit=%v %v", slug, res.StatusCode, board.CanEdit, err)
		}
	}

	var n int
	e.db.QueryRow("SELECT COUNT(*) FROM comments c JOIN issues i ON i.id = c.issue_id JOIN projects p ON p.id = i.project_id WHERE p.slug = 'vvv'").Scan(&n)
	if n != 0 {
		t.Errorf("vvv にコメントが書かれた: %d", n)
	}
	e.db.QueryRow("SELECT COUNT(*) FROM comments c JOIN issues i ON i.id = c.issue_id JOIN projects p ON p.id = i.project_id WHERE p.slug = 'nnn'").Scan(&n)
	if n != 1 { // 参加している間の MCP の 1 件だけ
		t.Errorf("nnn のコメント: %d", n)
	}
}

// adminIn は管理者を作り、slugs のプロジェクトに editor で参加させる。管理者も参加していないプロジェクトには
// 書けないため（それまでは参加しなくても役割 admin で書けた）、起票などの準備に使う管理者はこれで作る。
func (e *env) adminIn(login, password string, slugs ...string) store.User {
	e.t.Helper()
	u := e.user(login, password, "admin")
	for _, slug := range slugs {
		if err := store.SetMember(context.Background(), e.db, e.projectID(slug), u.ID, "editor"); err != nil {
			e.t.Fatal(err)
		}
	}
	return u
}
