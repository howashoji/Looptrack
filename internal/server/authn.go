package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/auth"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

const (
	sessionCookie   = "looptrack_session"
	loginCSRFCookie = "looptrack_login_csrf"
)

// principal は認証済みの利用者と経路。
type principal struct {
	User    store.User
	Via     string // web / api
	TokenID int64
	Session *store.Session
}

func (s *Server) setSessionCookie(w http.ResponseWriter, value string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: value, Path: s.cfg.BasePath, Expires: expires,
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: s.cfg.BasePath, MaxAge: -1,
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode})
}

// sessionFrom は Cookie のセッションを検証する（期限・無操作時間・無効化された利用者）。
func (s *Server) sessionFrom(r *http.Request) (*store.Session, store.User, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil, store.User{}, store.ErrNotFound
	}
	ctx := r.Context()
	now := s.cfg.Now()
	hash := auth.HashToken(c.Value)
	sess, err := store.SessionByHash(ctx, s.db, hash, now)
	if err != nil {
		return nil, store.User{}, err
	}
	if now.Sub(sess.LastSeenAt) > sessionIdleTimeout {
		_ = store.DeleteSession(ctx, s.db, hash)
		return nil, store.User{}, store.ErrNotFound
	}
	u, err := store.UserByID(ctx, s.db, sess.UserID)
	if err != nil {
		return nil, u, err
	}
	if u.Disabled {
		_ = store.DeleteSession(ctx, s.db, hash)
		return nil, u, store.ErrNotFound
	}
	// 二段階認証が必須のとき、TOTP を経ずに発行されたセッション（任意の間にパスワードだけで入ったもの）と
	// TOTP 未登録の利用者のセッションは使わせない（再ログインで登録・入力を求める）。
	// 必須への切替時にも破棄するが、切替と同時に発行されたセッションや管理コマンド以外の経路もここで止める。
	// ローカルモードはログインを経ないので見ない。
	if !s.cfg.LocalMode && sess.MFAPassed && (!sess.TOTPVerified || !u.TOTPEnabled) {
		required, err := s.twoFactorRequired(ctx)
		if err != nil {
			return nil, u, err
		}
		if required {
			_ = store.DeleteSession(ctx, s.db, hash)
			return nil, u, store.ErrNotFound
		}
	}
	if now.Sub(sess.LastSeenAt) > 5*time.Minute {
		_ = store.TouchSession(ctx, s.db, hash, now)
	}
	return &sess, u, nil
}

// bearer は Authorization: Bearer のアクセストークンを検証する。
func (s *Server) bearer(r *http.Request) (*principal, bool, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return nil, false, nil
	}
	tok, ok := strings.CutPrefix(h, "Bearer ")
	if !ok || tok == "" {
		return nil, true, errors.New(i18n.T(reqLang(r), "server.api.err.auth_header"))
	}
	ctx := r.Context()
	now := s.cfg.Now()
	t, err := store.TokenByHash(ctx, s.db, auth.HashToken(strings.TrimSpace(tok)))
	if err != nil {
		return nil, true, errors.New(i18n.T(reqLang(r), "server.api.err.token_invalid"))
	}
	if t.RevokedAt.Valid || (t.ExpiresAt.Valid && !t.ExpiresAt.Time.After(now)) {
		return nil, true, errors.New(i18n.T(reqLang(r), "server.api.err.token_expired"))
	}
	u, err := store.UserByID(ctx, s.db, t.UserID)
	if err != nil || u.Disabled {
		return nil, true, errors.New(i18n.T(reqLang(r), "server.api.err.token_user_disabled"))
	}
	if !t.LastUsedAt.Valid || now.Sub(t.LastUsedAt.Time) > time.Minute {
		_ = store.TouchToken(ctx, s.db, t.ID, now)
	}
	return &principal{User: u, Via: "api", TokenID: t.ID}, true, nil
}

// api は API 用の認証（トークン、または TOTP 済みのセッション）。未認証は 401 の JSON。
func (s *Server) api(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.LocalMode {
			s.localAPI(w, r, next)
			return
		}
		p, present, err := s.bearer(r)
		if present {
			if err != nil {
				writeError(w, http.StatusUnauthorized, "unauthorized", err.Error())
				return
			}
		} else {
			sess, u, err := s.sessionFrom(r)
			if err != nil || !sess.MFAPassed {
				writeError(w, http.StatusUnauthorized, "unauthorized", i18n.T(reqLang(r), "server.api.err.auth_required"))
				return
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead && !s.validCSRF(r, sess.CSRFToken) {
				writeError(w, http.StatusForbidden, "csrf", i18n.T(reqLang(r), "server.api.err.csrf"))
				return
			}
			p = &principal{User: u, Via: "web", Session: sess}
		}
		r = r.WithContext(withPrincipal(r.Context(), p))
		setContentLanguage(w, r)
		next.ServeHTTP(w, r)
	})
}

// web は画面用の認証（TOTP 済みのセッション）。未認証はログイン画面へ。POST は CSRF を検査する。
func (s *Server) web(next func(http.ResponseWriter, *http.Request, *principal)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.LocalMode {
			s.localWeb(w, r, next)
			return
		}
		sess, u, err := s.sessionFrom(r)
		if err != nil {
			s.redirectLogin(w, r)
			return
		}
		if !sess.MFAPassed {
			http.Redirect(w, r, s.nextStep(u), http.StatusSeeOther)
			return
		}
		if r.Method == http.MethodPost && !s.validCSRF(r, sess.CSRFToken) {
			http.Error(w, i18n.T(reqLang(r), "server.web.err.csrf"), http.StatusForbidden)
			return
		}
		p := &principal{User: u, Via: "web", Session: sess}
		// 404 画面などコンテキストから利用者を引く処理のために入れる（ヘッダのユーザー名のメニュー）
		r = r.WithContext(withPrincipal(r.Context(), p))
		setContentLanguage(w, r)
		next(w, r, p)
	}
}

// pending は TOTP 入力・登録の画面用（パスワード認証済みで TOTP 未完了のセッション）。
func (s *Server) pending(next func(http.ResponseWriter, *http.Request, *principal)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.LocalMode { // ローカルモードはログインの段階が無い
			http.Redirect(w, r, s.cfg.BasePath+"/", http.StatusSeeOther)
			return
		}
		sess, u, err := s.sessionFrom(r)
		if err != nil {
			s.redirectLogin(w, r)
			return
		}
		if sess.MFAPassed {
			http.Redirect(w, r, s.cfg.BasePath+"/", http.StatusSeeOther)
			return
		}
		if r.Method == http.MethodPost && !s.validCSRF(r, sess.CSRFToken) {
			http.Error(w, i18n.T(reqLang(r), "server.web.err.csrf_login"), http.StatusForbidden)
			return
		}
		next(w, r, &principal{User: u, Via: "web", Session: sess})
	}
}

// twoFactorRequired は二段階認証が必須か（system_settings。未設定は必須）。
func (s *Server) twoFactorRequired(ctx context.Context) (bool, error) {
	policy, _, err := store.TwoFactorPolicy(ctx, s.db)
	if err != nil {
		return true, err
	}
	return policy == store.TwoFactorRequired, nil
}

func (s *Server) nextStep(u store.User) string {
	if u.TOTPEnabled {
		return s.cfg.BasePath + "/login/totp"
	}
	return s.cfg.BasePath + "/login/totp/setup"
}

func (s *Server) redirectLogin(w http.ResponseWriter, r *http.Request) {
	q := url.Values{}
	if r.Method == http.MethodGet && r.URL.Path != s.cfg.BasePath+"/" {
		q.Set("next", r.URL.RequestURI())
	}
	target := s.cfg.BasePath + "/login"
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// safeNext は /im 配下の相対パスだけを許す（オープンリダイレクト防止）。
func (s *Server) safeNext(next string) string {
	if strings.HasPrefix(next, s.cfg.BasePath+"/") && !strings.HasPrefix(next, "//") && !strings.Contains(next, "\\") {
		return next
	}
	return s.cfg.BasePath + "/"
}

func (s *Server) validCSRF(r *http.Request, want string) bool {
	got := r.Header.Get("X-CSRF-Token")
	if got == "" {
		got = r.PostFormValue("csrf")
	}
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
