package server

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/howashoji/looptrack/internal/auth"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

func msgBadCredentials(lang i18n.Lang) string {
	return i18n.T(lang, "server.web.login.err_credentials")
}

func msgLocked(lang i18n.Lang) string { return i18n.T(lang, "server.web.login.err_locked") }

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if s.cfg.LocalMode { // ローカルモードはログインしない
		http.Redirect(w, r, s.cfg.BasePath+"/", http.StatusSeeOther)
		return
	}
	if sess, u, err := s.sessionFrom(r); err == nil {
		if sess.MFAPassed {
			http.Redirect(w, r, s.safeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, s.nextStep(u), http.StatusSeeOther)
		return
	}
	s.renderLogin(w, r, http.StatusOK, r.URL.Query().Get("next"), "", "")
}

func (s *Server) renderLogin(w http.ResponseWriter, r *http.Request, status int, next, login, msg string) {
	token, _ := auth.RandomToken(32)
	http.SetCookie(w, &http.Cookie{Name: loginCSRFCookie, Value: token, Path: s.cfg.BasePath + "/login", MaxAge: 600,
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteStrictMode})
	s.render(w, r, status, "login.html", map[string]any{"CSRF": token, "Next": next, "Login": login, "Error": msg})
}

func (s *Server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if s.cfg.LocalMode {
		http.Redirect(w, r, s.cfg.BasePath+"/", http.StatusSeeOther)
		return
	}
	ctx := r.Context()
	login := strings.TrimSpace(r.PostFormValue("login"))
	password := r.PostFormValue("password")
	next := r.PostFormValue("next")
	c, err := r.Cookie(loginCSRFCookie)
	if err != nil || subtle.ConstantTimeCompare([]byte(c.Value), []byte(r.PostFormValue("csrf"))) != 1 {
		s.renderLogin(w, r, http.StatusForbidden, next, login, i18n.T(reqLang(r), "server.web.login.err_expired"))
		return
	}
	ip := s.clientIP(r)
	byLogin, byIP, err := store.RecentFailures(ctx, s.db, login, ip, s.cfg.Now().Add(-lockWindow))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if byLogin >= lockPerLogin || byIP >= lockPerIP {
		s.renderLogin(w, r, http.StatusTooManyRequests, next, login, msgLocked(reqLang(r)))
		return
	}

	select {
	case s.hashSlot <- struct{}{}:
	case <-ctx.Done():
		return
	}
	u, uerr := store.UserByLogin(ctx, s.db, login)
	ok := false
	if uerr == nil {
		ok, _ = auth.VerifyPassword(u.PasswordHash, password)
	} else {
		auth.DummyVerify(password)
	}
	<-s.hashSlot

	if !ok || u.Disabled {
		_ = store.RecordLoginAttempt(ctx, s.db, login, ip, "password", false)
		s.renderLogin(w, r, http.StatusUnauthorized, next, login, msgBadCredentials(reqLang(r)))
		return
	}

	// 二段階認証が任意で TOTP 未登録なら、パスワードだけで段階を終える。登録済みなら任意でも TOTP を求める
	required, err := s.twoFactorRequired(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	full := !u.TOTPEnabled && !required
	if err := s.startSession(w, r, u, full, false); err != nil {
		s.internalError(w, r, err)
		return
	}
	if full {
		_ = store.RecordLoginAttempt(ctx, s.db, login, ip, "login", true)
		_ = store.TouchLogin(ctx, s.db, u.ID)
		http.Redirect(w, r, s.safeNext(next), http.StatusSeeOther)
		return
	}
	target := s.nextStep(u)
	if n := s.safeNext(next); n != s.cfg.BasePath+"/" {
		target += "?next=" + urlQueryEscape(n)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// startSession は新しいセッション ID を発行する（セッション固定化を防ぐため、段階が進むたびに作り直す）。
// mfaPassed はログインの段階をすべて終えたか、totpVerified はそのうち TOTP を入力したか。
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u store.User, mfaPassed, totpVerified bool) error {
	_, err := s.createSession(w, r, u, mfaPassed, totpVerified)
	return err
}

// createSession は startSession の本体で、作ったセッションを返す（ローカルモードの自動ログインが使う）。
func (s *Server) createSession(w http.ResponseWriter, r *http.Request, u store.User, mfaPassed, totpVerified bool) (store.Session, error) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		_ = store.DeleteSession(r.Context(), s.db, auth.HashToken(c.Value))
	}
	id, err := auth.RandomToken(32)
	if err != nil {
		return store.Session{}, err
	}
	csrf, err := auth.RandomToken(32)
	if err != nil {
		return store.Session{}, err
	}
	now := s.cfg.Now()
	life := sessionLifetime
	if !mfaPassed {
		life = pendingLifetime
	}
	sess := store.Session{IDHash: auth.HashToken(id), UserID: u.ID, CSRFToken: csrf, MFAPassed: mfaPassed, TOTPVerified: mfaPassed && totpVerified, ExpiresAt: now.Add(life)}
	if err := store.CreateSession(r.Context(), s.db, sess, s.clientIP(r), r.UserAgent()); err != nil {
		return store.Session{}, err
	}
	s.setSessionCookie(w, id, now.Add(life))
	return sess, nil
}

func (s *Server) totpPage(w http.ResponseWriter, r *http.Request, p *principal) {
	if !p.User.TOTPEnabled {
		http.Redirect(w, r, s.cfg.BasePath+"/login/totp/setup", http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, "totp.html", map[string]any{"CSRF": p.Session.CSRFToken, "Next": r.URL.Query().Get("next")})
}

func (s *Server) totpSubmit(w http.ResponseWriter, r *http.Request, p *principal) {
	ctx := r.Context()
	ip := s.clientIP(r)
	next := r.PostFormValue("next")
	fail := func(status int, msg string) {
		s.render(w, r, status, "totp.html", map[string]any{"CSRF": p.Session.CSRFToken, "Next": next, "Error": msg})
	}
	if !p.User.TOTPEnabled {
		http.Redirect(w, r, s.cfg.BasePath+"/login/totp/setup", http.StatusSeeOther)
		return
	}
	byLogin, byIP, err := store.RecentFailures(ctx, s.db, p.User.Login, ip, s.cfg.Now().Add(-lockWindow))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if byLogin >= lockPerLogin || byIP >= lockPerIP {
		_ = store.DeleteSession(ctx, s.db, p.Session.IDHash)
		s.clearSessionCookie(w)
		fail(http.StatusTooManyRequests, msgLocked(reqLang(r)))
		return
	}
	ok, err := s.verifyTOTPCode(ctx, p.User, r.PostFormValue("code"))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !ok {
		_ = store.RecordLoginAttempt(ctx, s.db, p.User.Login, ip, "totp", false)
		fail(http.StatusUnauthorized, i18n.T(reqLang(r), "server.web.login.err_totp_code"))
		return
	}
	s.completeLogin(w, r, p, next)
}

func (s *Server) completeLogin(w http.ResponseWriter, r *http.Request, p *principal, next string) {
	ctx := r.Context()
	if err := s.startSession(w, r, p.User, true, true); err != nil {
		s.internalError(w, r, err)
		return
	}
	_ = store.RecordLoginAttempt(ctx, s.db, p.User.Login, s.clientIP(r), "login", true)
	_ = store.TouchLogin(ctx, s.db, p.User.ID)
	http.Redirect(w, r, s.safeNext(next), http.StatusSeeOther)
}

func (s *Server) totpSetupPage(w http.ResponseWriter, r *http.Request, p *principal) {
	if p.User.TOTPEnabled {
		http.Redirect(w, r, s.cfg.BasePath+"/login/totp", http.StatusSeeOther)
		return
	}
	s.renderSetup(w, r, p, http.StatusOK, r.URL.Query().Get("next"), "")
}

func (s *Server) renderSetup(w http.ResponseWriter, r *http.Request, p *principal, status int, next, msg string) {
	data, err := s.totpEnrollData(r.Context(), p.User)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	required, err := s.twoFactorRequired(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	data["CSRF"], data["Next"], data["Error"], data["Required"] = p.Session.CSRFToken, next, msg, required
	s.render(w, r, status, "totp_setup.html", data)
}

func (s *Server) totpSetupSubmit(w http.ResponseWriter, r *http.Request, p *principal) {
	ctx := r.Context()
	next := r.PostFormValue("next")
	if p.User.TOTPEnabled || p.User.TOTPPending == nil {
		http.Redirect(w, r, s.nextStep(p.User), http.StatusSeeOther)
		return
	}
	ip := s.clientIP(r)
	byLogin, byIP, err := store.RecentFailures(ctx, s.db, p.User.Login, ip, s.cfg.Now().Add(-lockWindow))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if byLogin >= lockPerLogin || byIP >= lockPerIP {
		_ = store.DeleteSession(ctx, s.db, p.Session.IDHash)
		s.clearSessionCookie(w)
		s.renderLogin(w, r, http.StatusTooManyRequests, next, p.User.Login, msgLocked(reqLang(r)))
		return
	}
	secret, err := s.cfg.Box.Open(p.User.TOTPPending)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	step, ok := auth.VerifyTOTP(secret, r.PostFormValue("code"), s.cfg.Now(), 0)
	if !ok {
		_ = store.RecordLoginAttempt(ctx, s.db, p.User.Login, ip, "totp", false)
		s.renderSetup(w, r, p, http.StatusUnauthorized, next, i18n.T(reqLang(r), "server.web.totp.err_code_setup"))
		return
	}
	if err := store.EnableTOTP(ctx, s.db, p.User.ID, step); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.completeLogin(w, r, p, next)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, p *principal) {
	if s.cfg.LocalMode { // ローカルモードはログアウトしない（次の画面で自動的に入り直すため。メニューにも出さない）
		http.Redirect(w, r, s.cfg.BasePath+"/", http.StatusSeeOther)
		return
	}
	_ = store.DeleteSession(r.Context(), s.db, p.Session.IDHash)
	s.clearSessionCookie(w)
	http.Redirect(w, r, s.cfg.BasePath+"/login", http.StatusSeeOther)
}
