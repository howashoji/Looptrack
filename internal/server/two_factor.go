package server

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/howashoji/looptrack/internal/auth"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// 二段階認証（TOTP）の必須 / 任意（設計は DESIGN.md §3・§5-3・§8）。
//
//   - 設定は system_settings の two_factor（required / optional）。未設定は必須として扱う。
//   - 管理者は /im/admin/security で切り替える（管理者以外は 403）。必須 → 任意は操作する管理者の再認証
//     （パスワード、TOTP 登録済みなら確認コードも）を求める。変更は setting_changes に残し、同じ画面に出す。
//   - 任意のとき、各利用者はアカウント設定（/im/account/totp）で TOTP を登録・解除できる。
//     必須のときは解除できない（登録はいつでもできる）。

const settingChangesShown = 50

// adminForbidden は管理者以外に 403 を返す（二段階認証の設定。受け入れ条件が 403 を求めるため、
// 画面の存在を隠す admin() の 404 とは分けている）。
func (s *Server) adminForbidden(next func(http.ResponseWriter, *http.Request, *principal)) http.HandlerFunc {
	return s.web(func(w http.ResponseWriter, r *http.Request, p *principal) {
		if p.User.Role != "admin" {
			http.Error(w, i18n.T(reqLang(r), "server.web.security.err_admin_only"), http.StatusForbidden)
			return
		}
		next(w, r, p)
	})
}

// settingChangeView は変更記録 1 件の表示用。
type settingChangeView struct {
	At, Actor, Via, From, To, Note, IP string
}

func (s *Server) adminSecurityPage(w http.ResponseWriter, r *http.Request, p *principal) {
	s.renderAdminSecurity(w, r, p, adminResult{})
}

func (s *Server) renderAdminSecurity(w http.ResponseWriter, r *http.Request, p *principal, res adminResult) {
	ctx := r.Context()
	policy, set, err := store.TwoFactorPolicy(ctx, s.db)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	changes, err := store.SettingChanges(ctx, s.db, store.SettingTwoFactor, settingChangesShown)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	users, err := store.ListUsers(ctx, s.db)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var registered, unregistered int
	for _, u := range users {
		if u.Disabled {
			continue
		}
		if u.TOTPEnabled {
			registered++
		} else {
			unregistered++
		}
	}
	views := make([]settingChangeView, 0, len(changes))
	for _, c := range changes {
		v := settingChangeView{At: s.fmtWebTime(sql.NullTime{Time: c.At, Valid: true}), Actor: c.ActorLogin,
			From: store.TwoFactorLabel(reqLang(r), c.OldValue), To: store.TwoFactorLabel(reqLang(r), c.NewValue), Note: c.Note, IP: c.IP}
		switch c.Via {
		case "web":
			v.Via = i18n.T(reqLang(r), "server.web.security.via_web")
		case "command":
			v.Via = i18n.T(reqLang(r), "server.web.security.via_command")
		case "migration":
			v.Via = i18n.T(reqLang(r), "server.web.security.via_migration")
		default:
			v.Via = c.Via
		}
		if v.Actor == "" {
			v.Actor = "-"
		}
		views = append(views, v)
	}
	status := res.status
	if status == 0 {
		status = http.StatusOK
	}
	s.render(w, r, status, "admin_security.html", map[string]any{
		"User": p.User, "CSRF": p.Session.CSRFToken, "Notice": res.Notice, "Error": res.Error,
		"Policy": policy, "PolicyLabel": store.TwoFactorLabel(reqLang(r), policy), "PolicySet": set,
		"Required": policy == store.TwoFactorRequired, "Registered": registered, "Unregistered": unregistered,
		"Changes": views, "NeedCode": p.User.TOTPEnabled,
	})
}

// adminSecuritySubmit は POST /im/admin/security（two_factor = required / optional。任意にするときは password と code）。
func (s *Server) adminSecuritySubmit(w http.ResponseWriter, r *http.Request, p *principal) {
	ctx := r.Context()
	value := r.PostFormValue("two_factor")
	fail := func(status int, msg string) {
		s.renderAdminSecurity(w, r, p, adminResult{Error: msg, status: status})
	}
	if !store.ValidTwoFactor(value) {
		fail(http.StatusBadRequest, i18n.T(reqLang(r), "server.web.security.err_choose"))
		return
	}
	current, _, err := store.TwoFactorPolicy(ctx, s.db)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if current == value {
		s.renderAdminSecurity(w, r, p, adminResult{Notice: i18n.T(reqLang(r), "server.web.security.notice_unchanged", "label", store.TwoFactorLabel(reqLang(r), value))})
		return
	}
	if value == store.TwoFactorOptional {
		// 必須を外すときは、操作する管理者の再認証（パスワード・登録済みなら TOTP）を求める
		status, msg, err := s.reauth(r, p.User, r.PostFormValue("password"), r.PostFormValue("code"))
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if status != 0 {
			fail(status, msg+i18n.T(reqLang(r), "server.web.security.err_not_changed"))
			return
		}
	}
	res, err := store.SetTwoFactorPolicy(ctx, s.db, value, store.SettingChange{
		ActorUserID: sql.NullInt64{Int64: p.User.ID, Valid: true}, Via: "web", IP: s.clientIP(r)})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.cfg.Logger.Info("admin", "action", "two_factor", "actor", p.User.Login, "from", res.Old, "to", value, "revoked_sessions", res.RevokedSessions)
	lang := reqLang(r)
	msg := i18n.T(lang, "server.web.security.notice_changed", "label", store.TwoFactorLabel(lang, value))
	if value == store.TwoFactorRequired {
		msg += i18n.T(lang, "server.web.security.notice_sessions_revoked")
		if !p.User.TOTPEnabled || !p.Session.TOTPVerified {
			msg += i18n.T(lang, "server.web.security.notice_own_session")
		}
	} else {
		msg += i18n.T(lang, "server.web.security.notice_still_required")
	}
	s.renderAdminSecurity(w, r, p, adminResult{Notice: msg})
}

// reauth は本人の再認証（パスワード。TOTP 登録済みなら確認コードも）。失敗は login_attempts に reauth で記録し、
// ログインと同じ回数制限をかける。成功なら status 0。
func (s *Server) reauth(r *http.Request, u store.User, password, code string) (int, string, error) {
	ok, locked, err := s.verifyCurrentPassword(r, u, password)
	if err != nil {
		return 0, "", err
	}
	if locked {
		return http.StatusTooManyRequests, msgLocked(reqLang(r)), nil
	}
	if !ok {
		return http.StatusUnauthorized, i18n.T(reqLang(r), "server.web.security.err_password"), nil
	}
	if !u.TOTPEnabled {
		return 0, "", nil
	}
	ok, err = s.verifyTOTPCode(r.Context(), u, code)
	if err != nil {
		return 0, "", err
	}
	if !ok {
		_ = store.RecordLoginAttempt(r.Context(), s.db, u.Login, s.clientIP(r), "reauth", false)
		return http.StatusUnauthorized, i18n.T(reqLang(r), "server.web.security.err_code"), nil
	}
	return 0, "", nil
}

// verifyTOTPCode は登録済みの TOTP で code を確かめ、使用済みのステップを進める（同じコードの再利用を拒否する）。
func (s *Server) verifyTOTPCode(ctx context.Context, u store.User, code string) (bool, error) {
	secret, err := s.cfg.Box.Open(u.TOTPSecret)
	if err != nil {
		return false, err
	}
	step, ok := auth.VerifyTOTP(secret, code, s.cfg.Now(), u.TOTPLastStep)
	if !ok {
		return false, nil
	}
	return store.ConsumeTOTPStep(ctx, s.db, u.ID, step)
}

// pendingSecret は登録途中の TOTP シークレットを返す（無ければ作って暗号化して保存する）。
func (s *Server) pendingSecret(ctx context.Context, u store.User) ([]byte, error) {
	if u.TOTPPending != nil {
		if plain, err := s.cfg.Box.Open(u.TOTPPending); err == nil {
			return plain, nil
		}
	}
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		return nil, err
	}
	sealed, err := s.cfg.Box.Seal(secret)
	if err != nil {
		return nil, err
	}
	if err := store.SetTOTPPending(ctx, s.db, u.ID, sealed); err != nil {
		return nil, err
	}
	return secret, nil
}

// totpEnrollData は QR コードと手入力用のシークレット（登録画面の共通部分）。
func (s *Server) totpEnrollData(ctx context.Context, u store.User) (map[string]any, error) {
	secret, err := s.pendingSecret(ctx, u)
	if err != nil {
		return nil, err
	}
	uri := auth.TOTPURI(s.cfg.Issuer, u.Login, secret)
	png, err := qrcode.Encode(uri, qrcode.Medium, 220)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"Login":  u.Login,
		"Secret": auth.TOTPSecretString(secret),
		"QR":     templateURL("data:image/png;base64," + base64.StdEncoding.EncodeToString(png)),
	}, nil
}

// ---- アカウント設定の TOTP 登録・解除（全利用者） ----

func (s *Server) accountTOTPPage(w http.ResponseWriter, r *http.Request, p *principal) {
	if p.User.TOTPEnabled {
		http.Redirect(w, r, s.cfg.BasePath+"/account", http.StatusSeeOther)
		return
	}
	s.renderAccountTOTP(w, r, p, http.StatusOK, "")
}

func (s *Server) renderAccountTOTP(w http.ResponseWriter, r *http.Request, p *principal, status int, msg string) {
	data, err := s.totpEnrollData(r.Context(), p.User)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	data["User"], data["CSRF"], data["Error"] = p.User, p.Session.CSRFToken, msg
	s.render(w, r, status, "account_totp.html", data)
}

// accountTOTPSubmit は POST /im/account/totp（登録途中のシークレットの確認コード）。
func (s *Server) accountTOTPSubmit(w http.ResponseWriter, r *http.Request, p *principal) {
	ctx := r.Context()
	if p.User.TOTPEnabled {
		http.Redirect(w, r, s.cfg.BasePath+"/account", http.StatusSeeOther)
		return
	}
	if p.User.TOTPPending == nil {
		http.Redirect(w, r, s.cfg.BasePath+"/account/totp", http.StatusSeeOther)
		return
	}
	ip := s.clientIP(r)
	byLogin, byIP, err := store.RecentFailures(ctx, s.db, p.User.Login, ip, s.cfg.Now().Add(-lockWindow))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if byLogin >= lockPerLogin || byIP >= lockPerIP {
		s.renderAccountTOTP(w, r, p, http.StatusTooManyRequests, msgLocked(reqLang(r)))
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
		s.renderAccountTOTP(w, r, p, http.StatusUnauthorized, i18n.T(reqLang(r), "server.web.totp.err_code_setup"))
		return
	}
	if err := store.EnableTOTP(ctx, s.db, p.User.ID, step); err != nil {
		s.internalError(w, r, err)
		return
	}
	// TOTP を経ていない他のセッションを残さない（登録後は、このセッションだけ TOTP 済みとして作り直す）
	if err := store.DeleteUserSessions(ctx, s.db, p.User.ID); err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := s.startSession(w, r, p.User, true, true); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.cfg.Logger.Info("account", "action", "totp_enable", "user", p.User.Login, "ip", ip)
	http.Redirect(w, r, s.cfg.BasePath+"/account?done=totp", http.StatusSeeOther)
}

// accountTOTPDisable は POST /im/account/totp/disable（二段階認証が任意のときだけ。パスワードと確認コードで再認証する）。
func (s *Server) accountTOTPDisable(w http.ResponseWriter, r *http.Request, p *principal) {
	ctx := r.Context()
	if !p.User.TOTPEnabled {
		s.renderAccount(w, r, p, accountResult{Error: i18n.T(reqLang(r), "server.web.totp.err_not_enrolled"), status: http.StatusBadRequest})
		return
	}
	required, err := s.twoFactorRequired(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if required {
		s.renderAccount(w, r, p, accountResult{Error: i18n.T(reqLang(r), "server.web.totp.err_required"), status: http.StatusForbidden})
		return
	}
	status, msg, err := s.reauth(r, p.User, r.PostFormValue("password"), r.PostFormValue("code"))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if status != 0 {
		s.renderAccount(w, r, p, accountResult{Error: msg + i18n.T(reqLang(r), "server.web.totp.err_not_disabled"), status: status})
		return
	}
	if err := store.DisableTOTP(ctx, s.db, p.User.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.internalError(w, r, err)
		return
	}
	if err := store.DeleteUserSessions(ctx, s.db, p.User.ID); err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := s.startSession(w, r, p.User, true, false); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.cfg.Logger.Info("account", "action", "totp_disable", "user", p.User.Login, "ip", s.clientIP(r))
	http.Redirect(w, r, s.cfg.BasePath+"/account?done=totp-off", http.StatusSeeOther)
}
