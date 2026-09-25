package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// MCP の create_project（管理者だけ）と、setup が「プロジェクトが見つかりません」を返すときの案内。

// TestMCPCreateProject は、管理者が create_project で作ったプロジェクトが list_projects に出て、作った人が admin で参加し、
// 同じ接続から create_issue で起票すると指定した prefix で採番されることを確かめる。
// 対照として、管理者でない利用者・既にある slug と prefix・規則に合わない値は拒まれ、プロジェクトが作られない（既存も変わらない）ことを見る。
func TestMCPCreateProject(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	root := e.user("root", "root-password-12", "admin")
	dave := e.user("dave", "dave-password-12", "member")
	adm := e.mcpAs(e.apiAs(root).token, nil)
	mem := e.mcpAs(e.apiAs(dave).token, nil)

	// ツールの定義: 読み取り専用の注釈を付けない
	tools, err := adm.cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range tools.Tools {
		if tool.Name != "create_project" {
			continue
		}
		found = true
		if tool.Annotations == nil || tool.Annotations.ReadOnlyHint {
			t.Errorf("create_project の注釈: %+v（読み取り専用にしない）", tool.Annotations)
		}
		if tool.Description != i18n.T(i18n.JA, "server.mcp.tool.create_project") {
			t.Errorf("create_project の説明: %q", tool.Description)
		}
	}
	if !found {
		t.Fatal("tools/list に create_project が無い")
	}

	// 管理者が slug・prefix・name を指定して作る（width は既定の 4）
	text, data := adm.call("create_project", map[string]any{"slug": "alpha", "prefix": "ALP", "name": "アルファ"}, false)
	if want := "作成: プロジェクト alpha（表示名 アルファ・接頭辞 ALP・桁数 4。最初の ID は ALP-0001）。あなたは admin で参加しています"; text != want {
		t.Errorf("create_project: %q, want %q", text, want)
	}
	if data["first_id"] != "ALP-0001" {
		t.Errorf("create_project の data: %v", data)
	}

	// list_projects に出て、役割は admin（参加の行がある）
	text, data = adm.call("list_projects", nil, false)
	if !strings.Contains(text, "alpha（アルファ・ALP・権限 admin）") {
		t.Errorf("list_projects に作ったプロジェクトが admin で出ない: %s", text)
	}
	if ps, _ := data["projects"].([]any); len(ps) != 1 {
		t.Errorf("list_projects の件数: %v", data)
	}
	projects, roles, err := store.MemberProjects(ctx, e.db, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].Slug != "alpha" || roles[projects[0].ID] != "admin" || projects[0].Width != 4 {
		t.Errorf("参加の行: %+v %v", projects, roles)
	}

	// 同じ接続から起票でき、ID は指定した prefix で採番される
	text, data = adm.call("create_issue", map[string]any{"project": "alpha", "title": "最初の起票"}, false)
	if data["id"] != "ALP-0001" || !strings.HasPrefix(text, "作成: ALP-0001 最初の起票（version 1）") {
		t.Errorf("create_issue: %q %v", text, data)
	}

	// 対照: 管理者でない利用者は拒まれ、プロジェクトは作られない
	text, _ = mem.call("create_project", map[string]any{"slug": "beta", "prefix": "BET", "name": "ベータ"}, true)
	if want := i18n.T(i18n.JA, "service.err.forbidden.create_project"); text != want {
		t.Errorf("管理者でない利用者: %q, want %q", text, want)
	}
	if _, err := store.ProjectBySlug(ctx, e.db, "beta"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("管理者でない利用者の create_project でプロジェクトが作られた: %v", err)
	}

	// 対照: 既にある slug・既にある prefix は拒まれ、既存のプロジェクトは変わらない
	for _, c := range []struct{ slug, prefix string }{{"alpha", "ZZZ"}, {"alpha2", "ALP"}} {
		text, _ = adm.call("create_project", map[string]any{"slug": c.slug, "prefix": c.prefix, "name": "別"}, true)
		if want := i18n.T(i18n.JA, "service.err.conflict.project_exists", "slug", c.slug, "prefix", c.prefix); text != want {
			t.Errorf("重複 %s/%s: %q, want %q", c.slug, c.prefix, text, want)
		}
	}
	if _, err := store.ProjectBySlug(ctx, e.db, "alpha2"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("prefix の重複でプロジェクトが作られた: %v", err)
	}
	a, err := store.ProjectBySlug(ctx, e.db, "alpha")
	if err != nil || a.Prefix != "ALP" || a.Name != "アルファ" || a.Width != 4 || a.Counter != 1 {
		t.Errorf("既存のプロジェクトが変わった: %+v %v", a, err)
	}

	// 対照: 規則に合わない値（width は 1〜9）は拒まれ、作られない
	text, _ = adm.call("create_project", map[string]any{"slug": "gamma", "width": 12}, true)
	if !strings.Contains(text, "width は 1〜9") {
		t.Errorf("width の誤り: %q", text)
	}
	if _, err := store.ProjectBySlug(ctx, e.db, "gamma"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("規則に合わない値でプロジェクトが作られた: %v", err)
	}
}

// TestMCPSetupProjectMissing は、setup が「プロジェクトが見つかりません」を返すとき、管理者には create_project で作れること
// （確かめる値: slug・既定の prefix・既定の width）を、管理者でない利用者には管理者に頼むことを案内するのを確かめる（日英）。
func TestMCPSetupProjectMissing(t *testing.T) {
	e := newEnv(t)
	root := e.user("root", "root-password-12", "admin")
	dave := e.user("dave", "dave-password-12", "member")
	e.project("exists")
	rootTok, daveTok := e.apiAs(root).token, e.apiAs(dave).token

	args := map[string]any{"project": "job-crawler", "workspace": "/tmp/looptrack-test-ws"}
	for _, c := range []struct {
		name, token, lang string
		hint, other       string // 出るべき案内と、出てはいけない案内の ID
	}{
		{"管理者（ja）", rootTok, "ja", "server.mcp.setup.project_missing_admin", "server.mcp.setup.project_missing_member"},
		{"管理者でない利用者（ja）", daveTok, "ja", "server.mcp.setup.project_missing_member", "server.mcp.setup.project_missing_admin"},
		{"管理者（en）", rootTok, "en", "server.mcp.setup.project_missing_admin", "server.mcp.setup.project_missing_member"},
		{"管理者でない利用者（en）", daveTok, "en", "server.mcp.setup.project_missing_member", "server.mcp.setup.project_missing_admin"},
	} {
		lang := i18n.JA
		if c.lang == "en" {
			lang = i18n.EN
		}
		m := e.mcpAs(c.token, map[string]string{"Accept-Language": c.lang})
		text, _ := m.call("setup", args, true)
		kv := []any{"slug", "job-crawler"}
		if c.hint == "server.mcp.setup.project_missing_admin" {
			kv = append(kv, "prefix", "JOB-CRAWLER", "width", 4)
		}
		want := i18n.T(lang, "server.api.err.project_not_found", "project", "job-crawler") + "\n" + i18n.Msg{ID: c.hint, KV: kv}.In(lang)
		if text != want {
			t.Errorf("%s: %q, want %q", c.name, text, want)
		}
		if other := (i18n.Msg{ID: c.other, KV: kv}).In(lang); strings.Contains(text, other[:len(other)/2]) {
			t.Errorf("%s: 別の立場の案内が出ている: %q", c.name, text)
		}
	}

	// 対照: 案内は「見つからない」ときだけ。あるプロジェクト（管理者は参加していなくても引ける）では付かない
	m := e.mcpAs(rootTok, nil)
	text, _ := m.call("setup", map[string]any{"project": "exists", "workspace": "/tmp/looptrack-test-ws"}, false)
	if strings.Contains(text, "create_project") {
		t.Errorf("あるプロジェクトの setup に create_project の案内が付いた: %q", text)
	}
}
