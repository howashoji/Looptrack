package server

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/store"
)

// 画面からプロジェクトを作る（作った人を admin で参加させる）と、ローカルモードでの全プロジェクトへの参加。

// webCreateIssue は画面のセッション（Cookie + X-CSRF-Token。ボードの画面が API を呼ぶのと同じ形）で起票し、状態を返す。
func (e *env) webCreateIssue(c *http.Client, slug, csrf string) (int, string) {
	e.t.Helper()
	res, body := e.do(c, "POST", "/im/api/v1/projects/"+slug+"/issues", `{"title":"画面からの起票"}`,
		"Content-Type", "application/json", "X-CSRF-Token", csrf, "Origin", e.srv.URL, "Sec-Fetch-Site", "same-origin")
	return res.StatusCode, body
}

func TestAdminCreateProjectTeamMode(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	boss := e.user("boss", "boss-password-12", "admin")
	e.user("member1", "member-password-1", "member")

	c := e.client()
	e.enroll(c, "boss", "boss-password-12")
	_, page := e.get(c, "/im/admin/projects")
	if !strings.Contains(page, `action="/im/admin/projects"`) {
		t.Fatalf("作成のフォームが無い:\n%s", page)
	}
	csrf := csrfOf(t, page)
	if res, _ := e.post(c, "/im/admin/projects", url.Values{"slug": {"web"}, "prefix": {"WEB"}, "name": {"Web"}}); res.StatusCode != http.StatusForbidden {
		t.Errorf("CSRF なし: %d, want 403", res.StatusCode)
	}
	res, body := e.post(c, "/im/admin/projects", url.Values{"csrf": {csrf}, "slug": {"web"}, "name": {"Web サイト"}})
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/im/p/web/" {
		t.Fatalf("作成: %d %s %s", res.StatusCode, res.Header.Get("Location"), body)
	}
	pr, err := store.ProjectBySlug(ctx, e.db, "web")
	if err != nil || pr.Prefix != "WEB" || pr.Name != "Web サイト" || pr.Width != 4 {
		t.Fatalf("プロジェクト = %+v %v", pr, err)
	}
	if role, _ := store.MemberRole(ctx, e.db, pr.ID, boss.ID); role != "admin" {
		t.Errorf("作った人の役割 = %q, want admin", role)
	}
	if code, body := e.webCreateIssue(c, "web", csrf); code != http.StatusCreated {
		t.Errorf("作ったプロジェクトへの起票: %d %s", code, body)
	}
	// 重複・不正な入力は理由を示す（何も作らない）
	for _, tc := range []struct {
		form url.Values
		code int
		want string
	}{
		{url.Values{"slug": {"web"}, "prefix": {"OTHER"}}, http.StatusConflict, "すでに使われています"},
		{url.Values{"slug": {"other"}, "prefix": {"WEB"}}, http.StatusConflict, "すでに使われています"},
		{url.Values{"slug": {"Bad Slug"}}, http.StatusBadRequest, "slug は英小文字"},
		{url.Values{"slug": {"x"}, "prefix": {"x-1"}}, http.StatusBadRequest, "prefix は英大文字"},
	} {
		tc.form.Set("csrf", csrf)
		if res, body := e.post(c, "/im/admin/projects", tc.form); res.StatusCode != tc.code || !strings.Contains(body, tc.want) {
			t.Errorf("%v: %d, want %d %q", tc.form, res.StatusCode, tc.code, tc.want)
		}
	}
	if ps, _ := store.ListProjects(ctx, e.db); len(ps) != 1 {
		t.Errorf("プロジェクトの数 = %d", len(ps))
	}

	// 管理者でなければ作れない（画面は 404）
	m := e.client()
	e.enroll(m, "member1", "member-password-1")
	if res, _ := e.formAt(m, "/im/account", "/im/admin/projects", url.Values{"slug": {"mine"}}); res.StatusCode != http.StatusNotFound {
		t.Errorf("一般の利用者の作成: %d, want 404", res.StatusCode)
	}

	// チームのサーバの権限は変わらない: 参加していないプロジェクトは管理者でも閲覧のみ・参加の行は作らない
	other := e.project("other")
	if code, _ := e.webCreateIssue(c, "other", csrf); code != http.StatusForbidden {
		t.Errorf("参加していないプロジェクトへの起票: %d, want 403", code)
	}
	if role, _ := store.MemberRole(ctx, e.db, other.ID, boss.ID); role != "" {
		t.Errorf("通常モードで参加の行ができた: %q", role)
	}
}

// TestAdminCreateProjectErrorsInLang は、管理画面のプロジェクト作成で入力が不正なときの理由（store.ValidateNewProject）が
// 見ている人の言語で出て、ID（store.err.…）がそのまま出ないことを確かめる（日本語の利用者は上の TestAdminCreateProjectTeamMode）。
// ?lang=ja の要求を対照に置き、同じ入力で日本語の理由が出ることも見る（経路が死んでいて英語の検査が素通りしないように）。
func TestAdminCreateProjectErrorsInLang(t *testing.T) {
	e := newEnv(t)
	e.user("boss", "boss-password-12", "admin")
	c := enClient()
	e.enroll(c, "boss", "boss-password-12")
	for _, tc := range []struct {
		form     url.Values
		want, ja string
	}{
		{url.Values{"slug": {"Bad Slug"}}, `The slug must use only lowercase letters, digits and hyphens, 64 characters or fewer (starting with a lowercase letter or a digit): &#34;Bad Slug&#34;`, "slug は英小文字"},
		{url.Values{"slug": {"x"}, "prefix": {"x-1"}}, "The prefix must start with an uppercase letter", "prefix は英大文字"},
		{url.Values{"slug": {"x"}, "name": {strings.Repeat("n", 256)}}, "Give a display name of 255 characters or fewer", "表示名を 255 文字以内"},
	} {
		_, page := e.get(c, "/im/admin/projects")
		tc.form.Set("csrf", csrfOf(t, page))
		res, body := e.post(c, "/im/admin/projects", tc.form)
		if res.StatusCode != http.StatusBadRequest || !strings.Contains(body, tc.want) || strings.Contains(body, "store.err.") {
			t.Errorf("en %v: %d, want 400 と %q（ID が出ない）\n%s", tc.form, res.StatusCode, tc.want, body)
		}
		if strings.Contains(body, tc.ja) {
			t.Errorf("en %v: 日本語の理由 %q が出ている", tc.form, tc.ja)
		}
		res, body = e.post(c, "/im/admin/projects?lang=ja", tc.form)
		if res.StatusCode != http.StatusBadRequest || !strings.Contains(body, tc.ja) || strings.Contains(body, "store.err.") {
			t.Errorf("ja %v: %d, want 400 と %q", tc.form, res.StatusCode, tc.ja)
		}
	}
	if ps, _ := store.ListProjects(context.Background(), e.db); len(ps) != 0 {
		t.Errorf("不正な入力でプロジェクトができた: %d", len(ps))
	}
}

func TestLocalModeJoinsAllProjects(t *testing.T) {
	e := newLocalEnv(t)
	ctx := context.Background()
	boss := localSetup(t, e) // boss は loc の editor
	loc, _ := store.ProjectBySlug(ctx, e.db, "loc")
	// looptrack project create で作ったプロジェクト（参加者なし）と、別の人だけが参加しているプロジェクト
	cli := e.project("cli")
	shared := e.project("shared")
	member1, _ := store.UserByLogin(ctx, e.db, "member1")
	if err := store.SetMember(ctx, e.db, shared.ID, member1.ID, "editor"); err != nil {
		t.Fatal(err)
	}

	c := e.client()
	_, page := e.get(c, "/im/account")
	csrf := csrfOf(t, page)
	for _, slug := range []string{"cli", "shared"} {
		if code, body := e.webCreateIssue(c, slug, csrf); code != http.StatusCreated {
			t.Errorf("%s への起票: %d %s", slug, code, body)
		}
	}
	for id, want := range map[int64]string{cli.ID: "admin", shared.ID: "admin", loc.ID: "editor"} {
		if role, _ := store.MemberRole(ctx, e.db, id, boss.ID); role != want {
			t.Errorf("project %d の boss の役割 = %q, want %q（既存の行は上書きしない）", id, role, want)
		}
	}
	if role, _ := store.MemberRole(ctx, e.db, shared.ID, member1.ID); role != "editor" {
		t.Errorf("他の人の行が変わった: %q", role)
	}
	// 一覧（参加の行で決まる）にも出る
	if _, body := e.get(c, "/im/api/v1/projects"); !strings.Contains(body, `"cli"`) || !strings.Contains(body, `"shared"`) {
		t.Errorf("一覧: %s", body)
	}
}
