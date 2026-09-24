package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/auth"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

// アカウント設定と利用者管理。
// イシューについて画面は閲覧のみ（DESIGN.md §0）。ここで扱うのはアカウントだけ。
// 変更はすべて POST（web() が TOTP 済みセッションと CSRF を検査する）。結果は同じ画面を再描画して伝える。

// tokenDays は画面で選べるトークンの有効日数（無期限は CLI の管理コマンドだけで作れる）。
var tokenDays = []int{30, 90, 180, 365}

const defaultTokenDays = 90

var loginNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

var memberRoles = service.MemberRoles

// tokenView は画面に出すトークン 1 件。
type tokenView struct {
	ID       int64
	Kind     string
	Name     string
	Prefix   string
	Created  string
	Expires  string
	LastUsed string
	State    string // 有効 / 期限切れ / 失効
	Active   bool
}

// tokenLists は画面のトークン一覧。失効・期限切れは既定で折りたたむため分けて持つ（行は消さない。
// comments / issue_events の token_id が操作したトークンを指すため）。
type tokenLists struct {
	Active   []tokenView
	Inactive []tokenView
}

func (s *Server) tokenViews(ctx context.Context, lang i18n.Lang, userID int64) (tokenLists, error) {
	var out tokenLists
	ts, err := store.ListTokens(ctx, s.db, userID)
	if err != nil {
		return out, err
	}
	now := s.cfg.Now()
	for i := len(ts) - 1; i >= 0; i-- { // 新しい順
		t := ts[i]
		v := tokenView{ID: t.ID, Kind: t.Kind, Name: t.Name, Prefix: t.Prefix, Created: s.fmtWebTime(sql.NullTime{Time: t.CreatedAt, Valid: true}),
			Expires: s.fmtWebTime(t.ExpiresAt), LastUsed: s.fmtWebTime(t.LastUsedAt), State: i18n.T(lang, "server.web.account.token_state_active"), Active: true}
		if !t.ExpiresAt.Valid {
			v.Expires = i18n.T(lang, "server.web.account.token_no_expiry")
		}
		switch {
		case t.RevokedAt.Valid:
			v.State, v.Active = i18n.T(lang, "server.web.account.token_state_revoked", "at", s.fmtWebTime(t.RevokedAt)), false
		case t.ExpiresAt.Valid && !t.ExpiresAt.Time.After(now):
			v.State, v.Active = i18n.T(lang, "server.web.account.token_state_expired"), false
		}
		if v.Active {
			out.Active = append(out.Active, v)
		} else {
			out.Inactive = append(out.Inactive, v)
		}
	}
	return out, nil
}

// fmtWebTime は画面に出す時刻をプロジェクトの現地時刻（svc.Loc）で描く。
// ?format=md・台帳・レポートと同じ時間帯にする（経路で時刻が食い違わないように、時間帯は svc.Loc だけに置く）。
func (s *Server) fmtWebTime(t sql.NullTime) string {
	if !t.Valid {
		return "-"
	}
	return t.Time.In(s.svc.Loc).Format("2006-01-02 15:04")
}

// ---- アカウント設定（全利用者） ----

type accountResult struct {
	Notice   string
	Error    string
	NewToken string // 発行直後だけ表示する平文
	NewName  string
	status   int
}

func (s *Server) accountPage(w http.ResponseWriter, r *http.Request, p *principal) {
	var res accountResult
	switch r.URL.Query().Get("done") {
	case "password":
		res.Notice = i18n.T(reqLang(r), "server.web.account.notice_password_changed")
	case "totp":
		res.Notice = i18n.T(reqLang(r), "server.web.account.notice_totp_on")
	case "totp-off":
		res.Notice = i18n.T(reqLang(r), "server.web.account.notice_totp_off")
	case "lang":
		res.Notice = i18n.T(reqLang(r), "server.web.account.notice_lang_changed")
	}
	s.renderAccount(w, r, p, res)
}

func (s *Server) renderAccount(w http.ResponseWriter, r *http.Request, p *principal, res accountResult) {
	tokens, err := s.tokenViews(r.Context(), reqLang(r), p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	policy, _, err := store.TwoFactorPolicy(r.Context(), s.db)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	status := res.status
	if status == 0 {
		status = http.StatusOK
	}
	s.render(w, r, status, "account.html", map[string]any{
		"User": p.User, "CSRF": p.Session.CSRFToken, "Tokens": tokens, "Days": tokenDays, "DefaultDays": defaultTokenDays,
		"Notice": res.Notice, "Error": res.Error, "NewToken": res.NewToken, "NewName": res.NewName,
		"MinPassword": auth.MinPasswordLen, "LoginURL": s.publicBase(r) + s.cfg.BasePath,
		"RevokeAction": s.cfg.BasePath + "/account/tokens/revoke", "TwoFactorRequired": policy == store.TwoFactorRequired,
	})
}

// verifyCurrentPassword は再認証。ログインと同じ回数制限をかけ、失敗を記録する。
func (s *Server) verifyCurrentPassword(r *http.Request, u store.User, password string) (ok bool, locked bool, err error) {
	ctx := r.Context()
	ip := s.clientIP(r)
	byLogin, byIP, err := store.RecentFailures(ctx, s.db, u.Login, ip, s.cfg.Now().Add(-lockWindow))
	if err != nil {
		return false, false, err
	}
	if byLogin >= lockPerLogin || byIP >= lockPerIP {
		return false, true, nil
	}
	select {
	case s.hashSlot <- struct{}{}:
	case <-ctx.Done():
		return false, false, ctx.Err()
	}
	ok, _ = auth.VerifyPassword(u.PasswordHash, password)
	<-s.hashSlot
	if !ok {
		_ = store.RecordLoginAttempt(ctx, s.db, u.Login, ip, "reauth", false)
	}
	return ok, false, nil
}

// hashNewPassword は 2 回の入力を照合してハッシュにする（同時実行枠を使う）。
// 失敗の理由は ID を持つ error で返す（ここは要求＝言語を知らないので、出す側が文面にする）。
func (s *Server) hashNewPassword(ctx context.Context, pw, confirm string) (string, error) {
	if pw != confirm {
		return "", i18n.Errorf("server.web.account.err_password_mismatch")
	}
	select {
	case s.hashSlot <- struct{}{}:
	case <-ctx.Done():
		return "", i18n.Errorf("server.web.account.err_interrupted")
	}
	hash, err := auth.HashPassword(pw)
	<-s.hashSlot
	if err != nil {
		if errors.Is(err, auth.ErrPasswordPolicy) {
			return "", err
		}
		return "", i18n.Errorf("server.web.account.err_password_unset")
	}
	return hash, nil
}

func (s *Server) accountPassword(w http.ResponseWriter, r *http.Request, p *principal) {
	ctx := r.Context()
	ok, locked, err := s.verifyCurrentPassword(r, p.User, r.PostFormValue("current"))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if locked {
		s.renderAccount(w, r, p, accountResult{Error: msgLocked(reqLang(r)), status: http.StatusTooManyRequests})
		return
	}
	if !ok {
		s.renderAccount(w, r, p, accountResult{Error: i18n.T(reqLang(r), "server.web.account.err_current_password"), status: http.StatusUnauthorized})
		return
	}
	hash, hashErr := s.hashNewPassword(ctx, r.PostFormValue("new"), r.PostFormValue("confirm"))
	if hashErr != nil {
		lang := reqLang(r)
		s.renderAccount(w, r, p, accountResult{
			Error:  i18n.T(lang, "server.web.account.err_password_unchanged", "reason", hashErr),
			status: http.StatusBadRequest})
		return
	}
	// 全セッションを破棄してから、この画面のセッションだけ作り直す
	if err := store.SetPassword(ctx, s.db, p.User.ID, hash); err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := s.startSession(w, r, p.User, true, p.Session.TOTPVerified); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.cfg.Logger.Info("account", "action", "password", "user", p.User.Login, "ip", s.clientIP(r))
	// 新しいセッションの CSRF トークンで描き直す必要があるため、画面は取り直させる
	http.Redirect(w, r, s.cfg.BasePath+"/account?done=password", http.StatusSeeOther)
}

// accountLang は表示の言語の設定を保存する。空（「設定なし」）に戻せる。
//
// 保存した言語は、この利用者のサーバ側の文面（Web・REST・MCP）に効く。端末の LOOPTRACK_LANG は
// これより強い（CLI が X-Looptrack-Lang として送る。優先順は langFor に 1 か所で置いてある）。
//
// ここの i18n.Parse は**利用者の入力を正規形に直し、直せなければ利用者に分かるエラー（400）を返す**
// ためのもので、値の制限そのものではない。制限は store.SetUserLang が持つ（そちらが最後の砦。
// SQLite には CHECK が無く、書き込み経路が増えても守られるようにドメイン操作の側に置いてある）。
func (s *Server) accountLang(w http.ResponseWriter, r *http.Request, p *principal) {
	v := strings.TrimSpace(r.PostFormValue("lang"))
	if v != "" {
		lang, ok := i18n.Parse(v)
		if !ok {
			// 画面の選択肢は「設定なし / 日本語 / English」の 3 つだけなので、ここへ来るのは
			// 画面を通らない要求だけ。それでも文面は利用者の言語で返す。
			s.renderAccount(w, r, p, accountResult{Error: i18n.T(reqLang(r), "server.web.account.err_lang"), status: http.StatusBadRequest})
			return
		}
		v = string(lang)
	}
	if err := store.SetUserLang(r.Context(), s.db, p.User.ID, v); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.cfg.Logger.Info("account", "action", "lang", "user", p.User.Login, "lang", v)
	// 保存した言語で描き直すため、画面は取り直させる（p.User は要求の始めに読んだ古い値）。
	// 取り直した要求の知らせは、保存したばかりの言語で出る（設定が効いていることがそのまま見える）。
	http.Redirect(w, r, s.cfg.BasePath+"/account?done=lang", http.StatusSeeOther)
}

func (s *Server) accountCreateToken(w http.ResponseWriter, r *http.Request, p *principal) {
	name := strings.TrimSpace(r.PostFormValue("name"))
	days, formErr := parseTokenForm(name, r.PostFormValue("days"))
	if formErr != nil {
		s.renderAccount(w, r, p, accountResult{Error: i18n.Text(reqLang(r), formErr), status: http.StatusBadRequest})
		return
	}
	tok, err := s.issueToken(r.Context(), p.User.ID, name, days)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.cfg.Logger.Info("account", "action", "token_create", "user", p.User.Login, "name", name, "days", days)
	s.renderAccount(w, r, p, accountResult{NewToken: tok, NewName: name})
}

// parseTokenForm はトークンの発行の入力を確かめる。理由は ID を持つ error で返す（出す側が文面にする）。
func parseTokenForm(name, daysRaw string) (int, error) {
	if name == "" {
		return 0, i18n.Errorf("server.web.account.err_token_name_empty")
	}
	if len([]rune(name)) > 255 {
		return 0, i18n.Errorf("server.web.account.err_token_name_long")
	}
	days, err := strconv.Atoi(daysRaw)
	if err != nil || !containsInt(tokenDays, days) {
		return 0, i18n.Errorf("server.web.account.err_token_days")
	}
	return days, nil
}

func (s *Server) issueToken(ctx context.Context, userID int64, name string, days int) (string, error) {
	tok, prefix, err := auth.NewPAT()
	if err != nil {
		return "", err
	}
	now := s.cfg.Now()
	exp := now.Add(time.Duration(days) * 24 * time.Hour)
	if _, err := store.CreateToken(ctx, s.db, userID, "pat", name, prefix, auth.HashToken(tok), &exp, now); err != nil {
		return "", err
	}
	return tok, nil
}

func (s *Server) accountRevokeToken(w http.ResponseWriter, r *http.Request, p *principal) {
	id, err := strconv.ParseInt(r.PostFormValue("token"), 10, 64)
	if err == nil {
		err = store.RevokeUserToken(r.Context(), s.db, p.User.ID, id)
	}
	if err != nil {
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, strconv.ErrSyntax) || errors.Is(err, strconv.ErrRange) {
			s.renderAccount(w, r, p, accountResult{Error: i18n.T(reqLang(r), "server.web.account.err_token_missing"), status: http.StatusNotFound})
			return
		}
		s.internalError(w, r, err)
		return
	}
	s.cfg.Logger.Info("account", "action", "token_revoke", "user", p.User.Login, "token_id", id)
	s.renderAccount(w, r, p, accountResult{Notice: i18n.T(reqLang(r), "server.web.account.notice_token_revoked")})
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// ---- 利用者管理（管理者のみ） ----

// admin は管理者以外に 404 を返す（画面の存在を知らせない）。
func (s *Server) admin(next func(http.ResponseWriter, *http.Request, *principal)) http.HandlerFunc {
	return s.web(func(w http.ResponseWriter, r *http.Request, p *principal) {
		if p.User.Role != "admin" {
			s.notFoundPage(w, r, i18n.T(reqLang(r), "server.web.err.page_not_found"))
			return
		}
		next(w, r, p)
	})
}

type adminResult struct {
	Notice   string
	Error    string
	NewToken string
	status   int
	form     map[string]string // 追加に失敗したときの入力の戻し（パスワード以外）
	pending  *replacementForm  // 参加を外すのに代わりの担当者が要るとき
}

func (s *Server) adminUsersPage(w http.ResponseWriter, r *http.Request, p *principal) {
	s.renderAdminUsers(w, r, p, adminResult{})
}

func (s *Server) renderAdminUsers(w http.ResponseWriter, r *http.Request, p *principal, res adminResult) {
	users, err := store.ListUsers(r.Context(), s.db)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	type row struct {
		Login, Name, Role, LastLogin string
		TOTP, Disabled               bool
	}
	rows := make([]row, 0, len(users))
	for _, u := range users {
		rows = append(rows, row{Login: u.Login, Name: u.DisplayName, Role: u.Role, LastLogin: s.fmtWebTime(u.LastLoginAt), TOTP: u.TOTPEnabled, Disabled: u.Disabled})
	}
	status := res.status
	if status == 0 {
		status = http.StatusOK
	}
	form := res.form
	if form == nil {
		form = map[string]string{"role": "member"}
	}
	policy, _, err := store.TwoFactorPolicy(r.Context(), s.db)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.render(w, r, status, "admin_users.html", map[string]any{
		"TwoFactorLabel": store.TwoFactorLabel(reqLang(r), policy), "TwoFactorRequired": policy == store.TwoFactorRequired,
		"User": p.User, "CSRF": p.Session.CSRFToken, "Users": rows, "Notice": res.Notice, "Error": res.Error,
		"Form": form, "MinPassword": auth.MinPasswordLen,
	})
}

func (s *Server) adminCreateUser(w http.ResponseWriter, r *http.Request, p *principal) {
	ctx := r.Context()
	login := strings.TrimSpace(r.PostFormValue("login"))
	name := strings.TrimSpace(r.PostFormValue("name"))
	role := r.PostFormValue("role")
	form := map[string]string{"login": login, "name": name, "role": role}
	bad := func(msg string) {
		s.renderAdminUsers(w, r, p, adminResult{Error: msg, status: http.StatusBadRequest, form: form})
	}
	lang := reqLang(r)
	if !loginNameRe.MatchString(login) {
		bad(i18n.T(lang, "server.web.admin.err_login_name"))
		return
	}
	if len([]rune(name)) > 255 {
		bad(i18n.T(lang, "server.web.admin.err_display_name"))
		return
	}
	if role != "admin" && role != "member" {
		bad(i18n.T(lang, "server.web.admin.err_role"))
		return
	}
	if _, err := store.UserByLogin(ctx, s.db, login); err == nil {
		bad(i18n.T(lang, "server.web.admin.err_login_taken", "login", login))
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		s.internalError(w, r, err)
		return
	}
	hash, hashErr := s.hashNewPassword(ctx, r.PostFormValue("password"), r.PostFormValue("confirm"))
	if hashErr != nil {
		bad(i18n.Text(reqLang(r), hashErr))
		return
	}
	if _, err := store.CreateUser(ctx, s.db, login, name, hash, role); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.cfg.Logger.Info("admin", "action", "user_create", "actor", p.User.Login, "target", login, "role", role)
	notice := i18n.T(lang, "server.web.admin.notice_user_added", "login", login, "role", role)
	if required, err := s.twoFactorRequired(ctx); err == nil && !required {
		notice += i18n.T(lang, "server.web.admin.notice_totp_optional")
	} else {
		notice += i18n.T(lang, "server.web.admin.notice_totp_required")
	}
	s.renderAdminUsers(w, r, p, adminResult{Notice: notice + i18n.T(lang, "server.web.admin.notice_grant_projects")})
}

// targetUser はパスの {login} の利用者。見つからなければ 404 を返して false。
func (s *Server) targetUser(w http.ResponseWriter, r *http.Request) (store.User, bool) {
	u, err := store.UserByLogin(r.Context(), s.db, r.PathValue("login"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.notFoundPage(w, r, i18n.T(reqLang(r), "server.web.err.user_not_found"))
		} else {
			s.internalError(w, r, err)
		}
		return u, false
	}
	return u, true
}

func (s *Server) adminUserPage(w http.ResponseWriter, r *http.Request, p *principal) {
	u, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	s.renderAdminUser(w, r, p, u, adminResult{})
}

func (s *Server) renderAdminUser(w http.ResponseWriter, r *http.Request, p *principal, u store.User, res adminResult) {
	ctx := r.Context()
	// 操作の後の状態を出すため引き直す
	if fresh, err := store.UserByID(ctx, s.db, u.ID); err == nil {
		u = fresh
	}
	ms, err := store.UserMemberships(ctx, s.db, u.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	all, err := store.ListProjects(ctx, s.db)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	tokens, err := s.tokenViews(ctx, reqLang(r), u.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	type projRow struct {
		Slug, Name, Role string
	}
	roles := map[int64]string{}
	for _, m := range ms {
		roles[m.Project.ID] = m.Role
	}
	projects := make([]projRow, 0, len(all))
	for _, pr := range all {
		projects = append(projects, projRow{Slug: pr.Slug, Name: pr.Name, Role: roles[pr.ID]})
	}
	status := res.status
	if status == 0 {
		status = http.StatusOK
	}
	s.render(w, r, status, "admin_user.html", map[string]any{
		"User": p.User, "CSRF": p.Session.CSRFToken, "Target": u, "LastLogin": s.fmtWebTime(u.LastLoginAt),
		"Self": u.ID == p.User.ID, "Projects": projects, "MemberRoles": memberRoles, "Tokens": tokens,
		"Notice": res.Notice, "Error": res.Error, "MinPassword": auth.MinPasswordLen, "Pending": res.pending,
		"RevokeAction": s.cfg.BasePath + "/admin/users/" + u.Login + "/revoke",
	})
}

// adminUserAction は /im/admin/users/{login}/{action} の POST。
func (s *Server) adminUserAction(w http.ResponseWriter, r *http.Request, p *principal) {
	ctx := r.Context()
	u, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	action := r.PathValue("action")
	lang := reqLang(r)
	fail := func(status int, msg string) {
		s.renderAdminUser(w, r, p, u, adminResult{Error: msg, status: status})
	}
	done := func(msg string, attrs ...any) {
		s.cfg.Logger.Info("admin", append([]any{"action", action, "actor", p.User.Login, "target", u.Login}, attrs...)...)
		s.renderAdminUser(w, r, p, u, adminResult{Notice: msg})
	}
	self := u.ID == p.User.ID
	switch action {
	case "role", "disable", "enable", "password", "totp-reset":
		if self {
			fail(http.StatusBadRequest, i18n.T(lang, "server.web.admin.err_self"))
			return
		}
	}

	switch action {
	case "role":
		role := r.PostFormValue("role")
		if role != "admin" && role != "member" {
			fail(http.StatusBadRequest, i18n.T(lang, "server.web.admin.err_role"))
			return
		}
		if u.Role == "admin" && role != "admin" && !u.Disabled {
			if msg := s.keepAnAdmin(ctx, lang); msg != "" {
				fail(http.StatusConflict, msg)
				return
			}
		}
		if err := store.SetRole(ctx, s.db, u.ID, role); err != nil {
			s.internalError(w, r, err)
			return
		}
		done(i18n.T(lang, "server.web.admin.notice_role", "role", role), "role", role)
	case "disable", "enable":
		disable := action == "disable"
		if disable && u.Role == "admin" && !u.Disabled {
			if msg := s.keepAnAdmin(ctx, lang); msg != "" {
				fail(http.StatusConflict, msg)
				return
			}
		}
		if err := store.SetDisabled(ctx, s.db, u.ID, disable); err != nil && !errors.Is(err, store.ErrNotFound) {
			s.internalError(w, r, err)
			return
		}
		if disable {
			done(i18n.T(lang, "server.web.admin.notice_disabled"))
		} else {
			done(i18n.T(lang, "server.web.admin.notice_enabled"))
		}
	case "password":
		hash, hashErr := s.hashNewPassword(ctx, r.PostFormValue("password"), r.PostFormValue("confirm"))
		if hashErr != nil {
			lang := reqLang(r)
			fail(http.StatusBadRequest, i18n.T(lang, "server.web.account.err_password_unchanged", "reason", hashErr))
			return
		}
		if err := store.SetPassword(ctx, s.db, u.ID, hash); err != nil {
			s.internalError(w, r, err)
			return
		}
		done(i18n.T(lang, "server.web.admin.notice_password_reset"))
	case "totp-reset":
		// 未登録の利用者は更新行が 0 件（ErrNotFound）になるが、結果は同じなので成功として扱う
		if err := store.ResetTOTP(ctx, s.db, u.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			s.internalError(w, r, err)
			return
		}
		if required, err := s.twoFactorRequired(ctx); err == nil && !required {
			done(i18n.T(lang, "server.web.admin.notice_totp_reset_optional"))
		} else {
			done(i18n.T(lang, "server.web.admin.notice_totp_reset"))
		}
	case "member":
		slug := r.PostFormValue("project")
		role := r.PostFormValue("role")
		pr, err := store.ProjectBySlug(ctx, s.db, slug)
		if err != nil {
			fail(http.StatusBadRequest, i18n.T(lang, "server.api.err.project_not_found", "project", slug))
			return
		}
		msg, status, need, err := s.setProjectMember(ctx, lang, p, pr, u, role, r.PostFormValue("replacement"))
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if need != nil {
			s.renderAdminUser(w, r, p, u, adminResult{Error: msg, status: status,
				pending: newReplacementForm(lang, s.cfg.BasePath+"/admin/users/"+u.Login+"/member", p, "project", pr.Slug, pr, u, role, need)})
			return
		}
		if status != 0 {
			fail(status, msg)
			return
		}
		done(msg, "project", pr.Slug, "role", role, "replacement", r.PostFormValue("replacement"))
	case "revoke":
		id, err := strconv.ParseInt(r.PostFormValue("token"), 10, 64)
		if err == nil {
			err = store.RevokeUserToken(ctx, s.db, u.ID, id)
		}
		if err != nil {
			if errors.Is(err, store.ErrNotFound) || errors.Is(err, strconv.ErrSyntax) || errors.Is(err, strconv.ErrRange) {
				fail(http.StatusNotFound, i18n.T(lang, "server.web.account.err_token_missing"))
				return
			}
			s.internalError(w, r, err)
			return
		}
		done(i18n.T(lang, "server.web.admin.notice_token_revoked"), "token_id", id)
	default:
		s.notFoundPage(w, r, i18n.T(lang, "server.web.err.page_not_found"))
	}
}

// keepAnAdmin は、管理者を 1 人減らすと有効な管理者が 0 人になる場合にメッセージを返す。
func (s *Server) keepAnAdmin(ctx context.Context, lang i18n.Lang) string {
	n, err := store.CountActiveAdmins(ctx, s.db)
	if err != nil || n <= 1 {
		return i18n.T(lang, "server.web.admin.err_last_admin")
	}
	return ""
}

// setProjectMember はプロジェクトの権限を付ける・変える・外す（role が "" なら外す）。利用者の画面と
// プロジェクト管理の画面の両方がこれを使う（同じ操作は同じ結果になる）。
// プロジェクト権限は管理者の /im/admin/* の操作には関係しないため、自分自身の権限も変えられる（有効な管理者の数には
// 関係しない。ただし管理者も参加していないプロジェクトには書けない）。
// 外す・viewer に下げるとき、対象が担当の未クローズのイシューがあれば replacement（代わりの担当の login か "-"）が要る
// （service.SetMembership。無ければ 409 と、画面に出す件数・選択欄の中身 need を返す）。
// 返り値: 画面に出すメッセージ、失敗の HTTP ステータス（成功は 0）、代わりの担当が要るときの中身、内部エラー。
func (s *Server) setProjectMember(ctx context.Context, lang i18n.Lang, p *principal, pr store.Project, u store.User, role, replacement string) (string, int, *service.ReplacementNeeded, error) {
	if role != "" && !contains(memberRoles, role) {
		return i18n.T(lang, "server.web.admin.err_member_role"), http.StatusBadRequest, nil, nil
	}
	res, err := s.svc.SetMembership(ctx, service.Actor{UserID: p.User.ID, TokenID: p.TokenID, Via: "web", Lang: lang}, pr, u, role, replacement)
	var need *service.ReplacementError
	var se *service.Error
	switch {
	case errors.As(err, &need):
		return i18n.Text(lang, need.Err), http.StatusConflict, &need.Need, nil
	case errors.As(err, &se) && se.Kind == service.Invalid:
		return i18n.Text(lang, se), http.StatusBadRequest, nil, nil
	case err != nil:
		return "", 0, nil, err
	}
	return res.Message, 0, nil, nil
}

// replacementForm は、代わりの担当者を選ぶフォーム（setProjectMember が 409 を返したとき、同じ画面に出す）。
type replacementForm struct {
	Action, CSRF         string
	HiddenName, HiddenID string // プロジェクト管理は login、利用者の画面は project
	Slug, Login, Role    string
	What                 string // 参加を外す / 役割を viewer にする
	Issues               []string
	Candidates           []store.Assignable
}

func newReplacementForm(lang i18n.Lang, action string, p *principal, hiddenName, hiddenID string, pr store.Project, u store.User, role string, need *service.ReplacementNeeded) *replacementForm {
	what := i18n.T(lang, "server.web.admin.replace_remove")
	if role != "" {
		what = i18n.T(lang, "server.web.admin.replace_role", "role", role)
	}
	return &replacementForm{Action: action, CSRF: p.Session.CSRFToken, HiddenName: hiddenName, HiddenID: hiddenID,
		Slug: pr.Slug, Login: u.Login, Role: role, What: what, Issues: need.Issues, Candidates: need.Candidates}
}

// ---- プロジェクト管理（管理者のみ） ----
// 全プロジェクトと参加者・役割を並べ、付与・変更・解除する。自分が参加していないプロジェクトを開く導線もここに置く
// （ハブ・API の一覧・MCP は参加しているプロジェクトだけを出すため）。

func (s *Server) adminProjectsPage(w http.ResponseWriter, r *http.Request, p *principal) {
	s.renderAdminProjects(w, r, p, adminResult{})
}

func (s *Server) renderAdminProjects(w http.ResponseWriter, r *http.Request, p *principal, res adminResult) {
	ctx := r.Context()
	projects, err := store.ListProjects(ctx, s.db)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	members, err := store.AllMembers(ctx, s.db)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	users, err := store.ListUsers(ctx, s.db)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	type userOpt struct{ Login, Name string }
	type projRow struct {
		Slug, Name, Prefix, MyRole string
		Members                    []store.ProjectMember
		Candidates                 []userOpt // まだ参加していない利用者（追加の選択肢）
		SendPrompts                gateRow   // 指示文の作業名を送るか（usage.send_prompts）と直近の変更
	}
	rows := make([]projRow, 0, len(projects))
	for _, pr := range projects {
		row := projRow{Slug: pr.Slug, Name: pr.Name, Prefix: pr.Prefix, Members: members[pr.ID]}
		if row.SendPrompts, err = s.sendPromptsRowOf(ctx, reqLang(r), pr); err != nil {
			s.internalError(w, r, err)
			return
		}
		in := map[string]bool{}
		for _, m := range row.Members {
			in[m.Login] = true
			if m.Login == p.User.Login {
				row.MyRole = m.Role
			}
		}
		for _, u := range users {
			if !in[u.Login] && !u.Disabled {
				row.Candidates = append(row.Candidates, userOpt{u.Login, u.DisplayName})
			}
		}
		rows = append(rows, row)
	}
	status := res.status
	if status == 0 {
		status = http.StatusOK
	}
	s.render(w, r, status, "admin_projects.html", map[string]any{
		"User": p.User, "CSRF": p.Session.CSRFToken, "Projects": rows, "MemberRoles": memberRoles,
		"Notice": res.Notice, "Error": res.Error, "Pending": res.pending, "Form": res.form,
	})
}

// adminCreateProject は POST /im/admin/projects（slug・prefix・name。管理者だけ・CSRF は s.web が検査）。
// プロジェクトを作り、作った人を admin で参加させる（1 つのトランザクション。参加しないと管理者でも閲覧のみのため）。
// 作ったらそのプロジェクトの画面へ移る。
func (s *Server) adminCreateProject(w http.ResponseWriter, r *http.Request, p *principal) {
	ctx := r.Context()
	pr := store.Project{
		Slug:   strings.TrimSpace(r.PostFormValue("slug")),
		Prefix: strings.TrimSpace(r.PostFormValue("prefix")),
		Name:   strings.TrimSpace(r.PostFormValue("name")),
		Width:  4,
	}
	if pr.Prefix == "" {
		pr.Prefix = strings.ToUpper(pr.Slug)
	}
	if pr.Name == "" {
		pr.Name = pr.Slug
	}
	form := map[string]string{"slug": pr.Slug, "prefix": pr.Prefix, "name": pr.Name}
	fail := func(status int, msg string) {
		s.renderAdminProjects(w, r, p, adminResult{Error: msg, status: status, form: form})
	}
	if err := store.ValidateNewProject(pr); err != nil {
		fail(http.StatusBadRequest, i18n.Text(reqLang(r), err))
		return
	}
	err := func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback() //nolint:errcheck
		id, err := store.CreateProject(ctx, tx, pr)
		if err != nil {
			return err
		}
		if err := store.SetMember(ctx, tx, id, p.User.ID, "admin"); err != nil {
			return err
		}
		return tx.Commit()
	}()
	switch {
	case errors.Is(err, store.ErrProjectExists) || store.IsDuplicateKey(err):
		fail(http.StatusConflict, i18n.T(reqLang(r), "server.web.admin.err_project_exists", "slug", pr.Slug, "prefix", pr.Prefix))
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}
	s.cfg.Logger.Info("admin", "action", "project_create", "actor", p.User.Login, "project", pr.Slug, "prefix", pr.Prefix)
	http.Redirect(w, r, s.cfg.BasePath+"/p/"+pr.Slug+"/", http.StatusSeeOther)
}

// adminProjectMember は /im/admin/projects/{slug}/member の POST（login・role。role が空なら外す）。
func (s *Server) adminProjectMember(w http.ResponseWriter, r *http.Request, p *principal) {
	ctx := r.Context()
	lang := reqLang(r)
	fail := func(status int, msg string) {
		s.renderAdminProjects(w, r, p, adminResult{Error: msg, status: status})
	}
	pr, err := store.ProjectBySlug(ctx, s.db, r.PathValue("slug"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail(http.StatusNotFound, i18n.T(lang, "server.api.err.project_not_found", "project", r.PathValue("slug")))
		} else {
			s.internalError(w, r, err)
		}
		return
	}
	login := strings.TrimSpace(r.PostFormValue("login"))
	u, err := store.UserByLogin(ctx, s.db, login)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail(http.StatusBadRequest, i18n.T(lang, "server.web.admin.err_choose_user"))
		} else {
			s.internalError(w, r, err)
		}
		return
	}
	role := r.PostFormValue("role")
	msg, status, need, err := s.setProjectMember(ctx, lang, p, pr, u, role, r.PostFormValue("replacement"))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if need != nil {
		s.renderAdminProjects(w, r, p, adminResult{Error: msg, status: status,
			pending: newReplacementForm(lang, s.cfg.BasePath+"/admin/projects/"+pr.Slug+"/member", p, "login", u.Login, pr, u, role, need)})
		return
	}
	if status != 0 {
		fail(status, msg)
		return
	}
	s.cfg.Logger.Info("admin", "action", "member", "actor", p.User.Login, "target", u.Login, "project", pr.Slug, "role", role,
		"replacement", r.PostFormValue("replacement"), "via", "projects")
	s.renderAdminProjects(w, r, p, adminResult{Notice: msg})
}
