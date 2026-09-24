package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/howashoji/looptrack/internal/auth"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/setupwiz"
	"github.com/howashoji/looptrack/internal/store"
)

// 画面版の初回設定（設計 DESIGN.md §5-12）。
//
// ローカルモードで有効な管理者が 0 人の間だけ、画面の経路を初回設定のフォームにする（API・MCP は 503 の setup_required のまま）。
// 答え（④最初の管理者 ⑤二段階認証 ⑥最初のプロジェクト）は looptrack setup と同じ検査（setupwiz.PlanFirstRun）と
// 同じ DB の処理（setupwiz.Provision）で設定する。管理者ができたら二度と出さない（経路を持たず、gate が管理者 0 人のときだけ出す）。
//
// 守り: 受け付けるのは接続元が loopback（127.0.0.1・::1）の要求だけ。Host・Origin・Sec-Fetch-Site は gate の
// localRequestAllowed が先に検査する（DNS rebinding・別サイトからの送信）。フォームの送信は Cookie に結びつけた
// CSRF トークン（double submit。Cookie は HttpOnly・SameSite=Strict）で確かめる。同時の送信は firstRunMu で 1 つずつ行い、
// Provision もトランザクションの中で有効な管理者が 0 人であることを確かめる。

const (
	firstRunPath   = "/first-run"
	firstRunCookie = "looptrack_first_run"
)

// msgFirstRun はローカルモードの「セットアップ未完了」の案内。
func msgFirstRun(lang i18n.Lang) string { return i18n.T(lang, "server.err.first_run_required") }

// loopbackRemote は接続元が loopback か（前段のプロキシの X-Real-IP は見ない。ローカルモードはプロキシを置かない前提）。
func loopbackRemote(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// firstRun は、ローカルモードで管理者が 0 人のときの画面の経路を受ける。
func (s *Server) firstRun(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Retry-After", "60")
	if !loopbackRemote(r) {
		s.cfg.Logger.Warn("first_run", "action", "reject_remote", "remote", r.RemoteAddr)
		http.Error(w, i18n.T(reqLang(r), "server.web.first_run.err_remote"), http.StatusForbidden)
		return
	}
	path := s.cfg.BasePath + firstRunPath
	switch {
	case r.URL.Path == path && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		s.renderFirstRun(w, r, http.StatusOK, "", nil)
	case r.URL.Path == path && r.Method == http.MethodPost:
		s.firstRunSubmit(w, r)
	case r.Method == http.MethodGet || r.Method == http.MethodHead:
		http.Redirect(w, r, path, http.StatusSeeOther)
	default:
		s.render(w, r, http.StatusServiceUnavailable, "setup_required.html", map[string]any{"Message": msgFirstRun})
	}
}

// firstRunToken は Cookie の CSRF トークン（無ければ作って Cookie に入れる）。
func (s *Server) firstRunToken(w http.ResponseWriter, r *http.Request) (string, error) {
	if c, err := r.Cookie(firstRunCookie); err == nil && len(c.Value) == 64 {
		return c.Value, nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(b)
	http.SetCookie(w, &http.Cookie{Name: firstRunCookie, Value: tok, Path: s.cfg.BasePath + firstRunPath,
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteStrictMode})
	return tok, nil
}

func (s *Server) renderFirstRun(w http.ResponseWriter, r *http.Request, status int, msg string, form map[string]string) {
	tok, err := s.firstRunToken(w, r)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if form == nil {
		form = map[string]string{"login": setupwiz.DefaultAdminLogin, "project": setupwiz.DefaultLocalProject}
	}
	s.render(w, r, status, "first_run.html", map[string]any{
		"CSRF": tok, "Error": msg, "Form": form, "MinPassword": auth.MinPasswordLen,
	})
}

func (s *Server) firstRunSubmit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, i18n.T(reqLang(r), "server.web.first_run.err_form"), http.StatusBadRequest)
		return
	}
	c, err := r.Cookie(firstRunCookie)
	got := r.PostFormValue("csrf")
	if err != nil || c.Value == "" || got == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(got)) != 1 {
		http.Error(w, i18n.T(reqLang(r), "server.web.first_run.err_csrf"), http.StatusForbidden)
		return
	}
	a := setupwiz.FirstRunAnswers{
		Login: r.PostFormValue("login"), Name: r.PostFormValue("name"),
		Password: r.PostFormValue("password"), Password2: r.PostFormValue("confirm"),
		TwoFactor:   r.PostFormValue("two_factor"),
		ProjectSlug: r.PostFormValue("project"), ProjectPrefix: r.PostFormValue("project_prefix"), ProjectName: r.PostFormValue("project_name"),
	}
	form := map[string]string{"login": a.Login, "name": a.Name, "two_factor": a.TwoFactor,
		"project": a.ProjectSlug, "project_prefix": a.ProjectPrefix, "project_name": a.ProjectName}
	bad := func(msg string) { s.renderFirstRun(w, r, http.StatusBadRequest, msg, form) }

	s.firstRunMu.Lock()
	defer s.firstRunMu.Unlock()
	if _, err := s.localAdmin(r.Context()); err == nil {
		http.Redirect(w, r, s.cfg.BasePath+"/", http.StatusSeeOther) // 別のタブで済んだ
		return
	}
	s.hashSlot <- struct{}{}
	admin, project, err := setupwiz.PlanFirstRun(a)
	<-s.hashSlot
	if err != nil {
		bad(i18n.Text(reqLang(r), err))
		return
	}
	// 備考（setting_changes.note）は書き込んだときの文面で残るので、設定した人の言語で書く
	// （起票の雛形を作成者の言語で書くのと同じ。後から別の言語で見ても書き換わらない）。
	lang := reqLang(r)
	msg, err := setupwiz.Provision(r.Context(), s.db, admin, project, setupwiz.ProvisionOptions{
		Via: "web", Source: i18n.T(lang, "server.web.first_run.source"), IP: s.clientIP(r), FirstRun: true, Lang: lang})
	switch {
	case errors.Is(err, setupwiz.ErrAlreadySetUp):
		http.Redirect(w, r, s.cfg.BasePath+"/", http.StatusSeeOther)
		return
	case errors.Is(err, setupwiz.ErrLoginTaken):
		bad(i18n.Text(reqLang(r), err))
		return
	case errors.Is(err, store.ErrProjectExists):
		bad(i18n.T(reqLang(r), "server.web.first_run.err_project_exists"))
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: firstRunCookie, Value: "", Path: s.cfg.BasePath + firstRunPath, MaxAge: -1,
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteStrictMode})
	s.cfg.Logger.Info("first_run", "action", "done", "login", admin.Login, "project", project.Slug, "two_factor", admin.TwoFactor, "result", msg)
	http.Redirect(w, r, s.cfg.BasePath+firstRunPath+"/done", http.StatusSeeOther)
}

// firstRunDone は初回設定の最後の画面（GET /first-run/done。ローカルモードだけ）。イシューの画面への入口と、
// Claude Code・Codex・Copilot の MCP の接続設定（looptrack setup の最後の案内と同じ setupwiz.MCPConfigs）を出す。
// 設定の後もいつでも開ける（AI の接続設定を見直すため）。
func (s *Server) firstRunDone(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.cfg.LocalMode {
		s.notFoundPage(w, r, i18n.T(reqLang(r), "server.web.err.page_not_found"))
		return
	}
	projects, _, err := store.MemberProjects(r.Context(), s.db, p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	base := strings.TrimRight(s.cfg.PublicURL, "/")
	if base == "" {
		base = "http://" + r.Host // gate が loopback の名前だけに絞っている
	}
	base += s.cfg.BasePath
	slug, next := "", s.cfg.BasePath+"/"
	if len(projects) > 0 {
		slug, next = projects[0].Slug, s.cfg.BasePath+"/p/"+projects[0].Slug+"/"
	}
	s.render(w, r, http.StatusOK, "first_run_done.html", map[string]any{
		"User": p.User, "CSRF": p.Session.CSRFToken, "URL": base + "/", "Next": next, "Project": slug,
		"MCP": setupwiz.MCPConfigs(reqLang(r), base, slug),
	})
}
