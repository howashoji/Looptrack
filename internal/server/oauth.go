package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/auth"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// MCP の OAuth 2.1。MCP の認可仕様（2025-06-18）に合わせる:
// RFC 9728（保護リソースのメタデータ）・RFC 8414（認可サーバのメタデータ）・RFC 7591（動的クライアント登録）・
// PKCE（S256 必須）・RFC 8707（resource でトークンの宛先を限定）。
// 利用者の確認は既存のログイン（ID/パスワード + TOTP）を使い、承認画面で許可を取る。

const (
	authCodeLifetime = 10 * time.Minute
	oauthTokenLife   = 30 * 24 * time.Hour // アクセストークン（期限が来たら更新トークンで取り直す）
	// oauthRefreshLife は更新トークンの期限（DESIGN.md §5-9）。使うたびに入れ替え、新しい値の期限をここから数え直す
	// （90 日のうちに一度でも使えば切れない。上限は設けない。止めるのはアカウント画面の失効・利用者の無効化・再利用の検知）
	oauthRefreshLife = 90 * 24 * time.Hour
	oauthScope       = "im"
)

// publicBase は外から見た URL の基点（例 https://example.com）。
// LOOPTRACK_PUBLIC_URL があればそれを使い、無ければ要求のヘッダから組み立てる。
func (s *Server) publicBase(r *http.Request) string {
	if s.cfg.PublicURL != "" {
		return strings.TrimSuffix(s.cfg.PublicURL, "/")
	}
	scheme := "https"
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = p
	} else if r.TLS == nil {
		scheme = "http"
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = h
	}
	return scheme + "://" + host
}

func (s *Server) issuer(r *http.Request) string { return s.publicBase(r) + s.cfg.BasePath }
func (s *Server) mcpResource(r *http.Request) string {
	return s.publicBase(r) + s.cfg.BasePath + "/mcp"
}

// apiResource は REST API の基点（CLI の login --browser が resource に指定する）。
func (s *Server) apiResource(r *http.Request) string {
	return s.publicBase(r) + s.cfg.BasePath + "/api/v1"
}

// knownResource は resource（RFC 8707）として受け付ける宛先か（MCP か REST API の基点）。
func (s *Server) knownResource(r *http.Request, res string) bool {
	return sameResource(res, s.mcpResource(r)) || sameResource(res, s.apiResource(r))
}

// resourceMetadataURL は 401 の WWW-Authenticate と保護リソースのメタデータで使う URL。
func (s *Server) resourceMetadataURL(r *http.Request) string {
	return s.publicBase(r) + "/.well-known/oauth-protected-resource" + s.cfg.BasePath + "/mcp"
}

// metadataJSON は誰でも読める設定情報を返す（CORS 許可。ブラウザ内のクライアントも読む）。
func metadataJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) protectedResourceMetadata(w http.ResponseWriter, r *http.Request) {
	metadataJSON(w, map[string]any{
		"resource":                 s.mcpResource(r),
		"authorization_servers":    []string{s.issuer(r)},
		"scopes_supported":         []string{oauthScope},
		"bearer_methods_supported": []string{"header"},
		"resource_name":            i18n.T(reqLang(r), "server.api.oauth.resource_name"),
	})
}

func (s *Server) authorizationServerMetadata(w http.ResponseWriter, r *http.Request) {
	base := s.issuer(r)
	metadataJSON(w, map[string]any{
		"issuer":                                base,
		"authorization_endpoint":                base + "/oauth/authorize",
		"token_endpoint":                        base + "/oauth/token",
		"registration_endpoint":                 base + "/oauth/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{oauthScope},
		"service_documentation":                 base + "/",
	})
}

// oauthError は OAuth のエラー応答（RFC 6749 §5.2）。
func oauthError(w http.ResponseWriter, status int, code, desc string) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

// --- 動的クライアント登録（RFC 7591。公開クライアントのみ）

type registerRequest struct {
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	Scope                   string   `json:"scope"`
	ClientURI               string   `json:"client_uri"`
	LogoURI                 string   `json:"logo_uri"`
	SoftwareID              string   `json:"software_id"`
	SoftwareVersion         string   `json:"software_version"`
	ApplicationType         string   `json:"application_type"`
	Contacts                []string `json:"contacts"`
	PolicyURI               string   `json:"policy_uri"`
	TosURI                  string   `json:"tos_uri"`
	JwksURI                 string   `json:"jwks_uri"`
}

// validRedirectURI は戻り先として認める形か（https、または折り返し用の localhost）。
func validRedirectURI(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Fragment != "" || !u.IsAbs() {
		return false
	}
	switch u.Scheme {
	case "https":
		return u.Host != ""
	case "http":
		host := u.Hostname()
		return host == "localhost" || host == "127.0.0.1" || host == "::1"
	}
	return false
}

func (s *Server) oauthRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		corsPreflight(w)
		return
	}
	var req registerRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	if err := dec.Decode(&req); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", i18n.T(reqLang(r), "server.api.oauth.bad_register_json"))
		return
	}
	if len(req.RedirectURIs) == 0 || len(req.RedirectURIs) > 10 {
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", i18n.T(reqLang(r), "server.api.oauth.redirect_uris_count"))
		return
	}
	for _, u := range req.RedirectURIs {
		if !validRedirectURI(u) {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", i18n.T(reqLang(r), "server.api.oauth.redirect_uri_scheme", "uri", u))
			return
		}
	}
	if m := req.TokenEndpointAuthMethod; m != "" && m != "none" {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", i18n.T(reqLang(r), "server.api.oauth.public_client_only"))
		return
	}
	// authorization_code と refresh_token だけを受け付ける（Claude Code は登録時に必ず両方を含める）。
	// 更新トークンは登録内容に依らず発行する。それ以外の種類（password 等）は拒否する
	grants := []string{}
	for _, g := range req.GrantTypes {
		switch g {
		case "authorization_code", "refresh_token":
			grants = append(grants, g)
		default:
			oauthError(w, http.StatusBadRequest, "invalid_client_metadata", i18n.T(reqLang(r), "server.api.oauth.grant_types", "value", g))
			return
		}
	}
	if len(grants) == 0 {
		grants = []string{"authorization_code"}
	}
	id, err := auth.RandomToken(16)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	name := strings.TrimSpace(req.ClientName)
	if name == "" {
		name = i18n.T(reqLang(r), "server.api.oauth.unnamed_client")
	}
	if len([]rune(name)) > 80 {
		name = string([]rune(name)[:80])
	}
	c := store.OAuthClient{ClientID: id, Name: name, RedirectURIs: req.RedirectURIs}
	if err := store.CreateOAuthClient(r.Context(), s.db, c); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.cfg.Logger.Info("oauth client registered", "client_id", id, "name", name, "redirect_uris", req.RedirectURIs)
	w.Header().Set("Access-Control-Allow-Origin", "*")
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  id,
		"client_id_issued_at":        s.cfg.Now().Unix(),
		"client_name":                name,
		"redirect_uris":              req.RedirectURIs,
		"grant_types":                grants,
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
		"scope":                      oauthScope,
	})
}

func corsPreflight(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, MCP-Protocol-Version")
	h.Set("Access-Control-Max-Age", "600")
	w.WriteHeader(http.StatusNoContent)
}

// --- 認可（利用者の確認と承認）

// authorizeParams は検査済みの認可要求。
type authorizeParams struct {
	Client      store.OAuthClient
	RedirectURI string
	State       string
	Challenge   string
	Scope       string
	Resource    string
}

// parseAuthorize は認可要求を検査する。戻り先が確かなときだけ redirect でエラーを返せる。
func (s *Server) parseAuthorize(w http.ResponseWriter, r *http.Request) (*authorizeParams, bool) {
	q := r.URL.Query()
	if r.Method == http.MethodPost {
		q = r.Form
	}
	fail := func(msg string) {
		s.oauthErrorPage(w, r, msg)
	}
	client, err := store.OAuthClientByID(r.Context(), s.db, q.Get("client_id"))
	if err != nil {
		fail(i18n.T(reqLang(r), "server.web.oauth.client_unknown"))
		return nil, false
	}
	redirect := q.Get("redirect_uri")
	if redirect == "" && len(client.RedirectURIs) == 1 {
		redirect = client.RedirectURIs[0]
	}
	if !redirectAllowed(client.RedirectURIs, redirect) {
		fail(i18n.T(reqLang(r), "server.web.oauth.redirect_mismatch"))
		return nil, false
	}
	p := &authorizeParams{Client: client, RedirectURI: redirect, State: q.Get("state"),
		Challenge: q.Get("code_challenge"), Scope: q.Get("scope"), Resource: q.Get("resource")}
	// ここから先の誤りは、戻り先へ error を付けて返す（クライアントが理由を受け取れる）
	if q.Get("response_type") != "code" {
		s.redirectError(w, r, p, "unsupported_response_type", i18n.T(reqLang(r), "server.api.oauth.response_type"))
		return nil, false
	}
	if q.Get("code_challenge_method") != "S256" || len(p.Challenge) < 43 {
		s.redirectError(w, r, p, "invalid_request", i18n.T(reqLang(r), "server.api.oauth.pkce_required"))
		return nil, false
	}
	if p.Resource != "" && !s.knownResource(r, p.Resource) {
		s.redirectError(w, r, p, "invalid_target", i18n.T(reqLang(r), "server.api.oauth.resource_choice", "mcp", s.mcpResource(r), "api", s.apiResource(r)))
		return nil, false
	}
	return p, true
}

// redirectAllowed は認可要求の戻り先が登録内容と合うか。完全一致が原則で、ループバック（127.0.0.1 / ::1 / localhost）の
// http だけはポート番号を問わない（RFC 8252 §7.3。CLI は毎回空いているポートで待ち受ける）。
// ホストの表記・パス・問い合わせは登録と同じでなければならない。https と非ループバックは完全一致のまま。
func redirectAllowed(registered []string, got string) bool {
	if contains(registered, got) {
		return true
	}
	g, err := url.Parse(got)
	if err != nil || !isLoopbackHTTP(g) {
		return false
	}
	for _, raw := range registered {
		u, err := url.Parse(raw)
		if err != nil || !isLoopbackHTTP(u) {
			continue
		}
		if u.Hostname() == g.Hostname() && u.EscapedPath() == g.EscapedPath() && u.RawQuery == g.RawQuery && u.ForceQuery == g.ForceQuery {
			return true
		}
	}
	return false
}

// isLoopbackHTTP はループバックを指す http の戻り先か（利用者情報・フラグメントは認めない）。
func isLoopbackHTTP(u *url.URL) bool {
	if u.Scheme != "http" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	switch u.Hostname() {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	return false
}

// sameResource は RFC 8707 の resource の照合（末尾のスラッシュだけは無視する）。
func sameResource(got, want string) bool {
	return strings.TrimSuffix(got, "/") == strings.TrimSuffix(want, "/")
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v && v != "" {
			return true
		}
	}
	return false
}

func (s *Server) redirectError(w http.ResponseWriter, r *http.Request, p *authorizeParams, code, desc string) {
	u, err := url.Parse(p.RedirectURI)
	if err != nil {
		s.oauthErrorPage(w, r, desc)
		return
	}
	q := u.Query()
	q.Set("error", code)
	q.Set("error_description", desc)
	if p.State != "" {
		q.Set("state", p.State)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}

func (s *Server) oauthErrorPage(w http.ResponseWriter, r *http.Request, msg string) {
	data := map[string]any{"Base": s.cfg.BasePath, "Message": msg}
	if p := principalFrom(r.Context()); p != nil {
		data["User"] = p.User
		if p.Session != nil {
			data["CSRF"] = p.Session.CSRFToken // ヘッダのメニューのログアウト
		}
	}
	s.render(w, r, http.StatusBadRequest, "oauth_error.html", data)
}

// allowFormAction は承認画面の CSP に戻り先のオリジンを足す。
// 既定の form-action 'self' のままだと、ブラウザが**承認後のリダイレクト先**（クライアントの戻り先）まで止める。
func (s *Server) allowFormAction(w http.ResponseWriter, redirectURI string) {
	u, err := url.Parse(redirectURI)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return
	}
	origin := u.Scheme + "://" + u.Host
	h := w.Header()
	h.Set("Content-Security-Policy", strings.Replace(h.Get("Content-Security-Policy"),
		"form-action 'self'", "form-action 'self' "+origin, 1))
}

// authorizePage は承認画面（ログイン必須。web ミドルウェアが未ログインをログイン画面へ送る）。
func (s *Server) authorizePage(w http.ResponseWriter, r *http.Request, p *principal) {
	params, ok := s.parseAuthorize(w, r)
	if !ok {
		return
	}
	s.allowFormAction(w, params.RedirectURI)
	host := params.RedirectURI
	if u, err := url.Parse(params.RedirectURI); err == nil {
		host = u.Host
	}
	s.render(w, r, http.StatusOK, "authorize.html", map[string]any{
		"Base": s.cfg.BasePath, "User": p.User, "CSRF": p.Session.CSRFToken,
		"Client": params.Client.Name, "Host": host, "Query": r.URL.RawQuery,
	})
}

// authorizeSubmit は承認（または拒否）を受けて認可コードを返す。
func (s *Server) authorizeSubmit(w http.ResponseWriter, r *http.Request, p *principal) {
	if err := r.ParseForm(); err != nil {
		s.oauthErrorPage(w, r, i18n.T(reqLang(r), "server.web.oauth.bad_request"))
		return
	}
	// 画面から送られた元の問い合わせ文字列を使う（値はすべて検査し直す）
	if q := r.PostFormValue("query"); q != "" {
		values, err := url.ParseQuery(q)
		if err != nil {
			s.oauthErrorPage(w, r, i18n.T(reqLang(r), "server.web.oauth.bad_request"))
			return
		}
		for k, v := range values {
			r.Form[k] = v
		}
	}
	params, ok := s.parseAuthorize(w, r)
	if !ok {
		return
	}
	s.allowFormAction(w, params.RedirectURI)
	if r.PostFormValue("approve") != "1" {
		s.redirectError(w, r, params, "access_denied", i18n.T(reqLang(r), "server.api.oauth.denied"))
		return
	}
	code, err := auth.RandomToken(32)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	now := s.cfg.Now()
	err = store.CreateAuthCode(r.Context(), s.db, auth.HashToken(code), store.AuthCode{
		ClientID: params.Client.ClientID, UserID: p.User.ID, RedirectURI: params.RedirectURI,
		CodeChallenge: params.Challenge, Scope: oauthScope, Resource: params.Resource, ExpiresAt: now.Add(authCodeLifetime),
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	u, err := url.Parse(params.RedirectURI)
	if err != nil {
		s.oauthErrorPage(w, r, i18n.T(reqLang(r), "server.web.oauth.bad_redirect"))
		return
	}
	q := u.Query()
	q.Set("code", code)
	if params.State != "" {
		q.Set("state", params.State)
	}
	u.RawQuery = q.Encode()
	s.cfg.Logger.Info("oauth code issued", "client_id", params.Client.ClientID, "user", p.User.Login)
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}

// --- トークン発行

func (s *Server) oauthToken(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		corsPreflight(w)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", i18n.T(reqLang(r), "server.api.oauth.bad_request"))
		return
	}
	switch r.PostFormValue("grant_type") {
	case "authorization_code":
		s.oauthTokenFromCode(w, r)
	case "refresh_token":
		s.oauthTokenFromRefresh(w, r)
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", i18n.T(reqLang(r), "server.api.oauth.grant_type"))
	}
}

func (s *Server) oauthTokenFromCode(w http.ResponseWriter, r *http.Request) {
	code := r.PostFormValue("code")
	if code == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request", i18n.T(reqLang(r), "server.api.oauth.code_missing"))
		return
	}
	now := s.cfg.Now()
	ac, err := store.UseAuthCode(r.Context(), s.db, auth.HashToken(code), now)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			oauthError(w, http.StatusBadRequest, "invalid_grant", i18n.T(reqLang(r), "server.api.oauth.code_invalid"))
			return
		}
		s.internalError(w, r, err)
		return
	}
	if cid := r.PostFormValue("client_id"); cid != "" && cid != ac.ClientID {
		oauthError(w, http.StatusBadRequest, "invalid_grant", i18n.T(reqLang(r), "server.api.oauth.client_mismatch"))
		return
	}
	if ru := r.PostFormValue("redirect_uri"); ru != "" && ru != ac.RedirectURI {
		oauthError(w, http.StatusBadRequest, "invalid_grant", i18n.T(reqLang(r), "server.api.oauth.redirect_changed"))
		return
	}
	if res := r.PostFormValue("resource"); res != "" && !sameResource(res, ac.Resource) && !s.knownResource(r, res) {
		oauthError(w, http.StatusBadRequest, "invalid_target", i18n.T(reqLang(r), "server.api.oauth.resource_changed"))
		return
	}
	verifier := r.PostFormValue("code_verifier")
	if !verifyPKCE(ac.CodeChallenge, verifier) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", i18n.T(reqLang(r), "server.api.oauth.verifier_mismatch"))
		return
	}
	u, err := store.UserByID(r.Context(), s.db, ac.UserID)
	if err != nil || u.Disabled {
		oauthError(w, http.StatusBadRequest, "invalid_grant", i18n.T(reqLang(r), "server.api.oauth.user_disabled"))
		return
	}
	family, err := auth.RandomToken(16)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.issueOAuthTokens(w, r, u, ac.ClientID, ac.Scope, ac.Resource, family, nil)
}

// oauthTokenFromRefresh は更新トークンで取り直す（DESIGN.md §5-9）。使った更新トークンは使用済みにし、
// 対のアクセストークンを失効させて、同じ系列の新しい組を発行する（rotation）。
// 使用済みの値がもう一度出されたら漏えいとみなし、系列の更新トークンとアクセストークンをすべて失効させる。
func (s *Server) oauthTokenFromRefresh(w http.ResponseWriter, r *http.Request) {
	value := r.PostFormValue("refresh_token")
	clientID := r.PostFormValue("client_id")
	if value == "" || clientID == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request", i18n.T(reqLang(r), "server.api.oauth.refresh_params"))
		return
	}
	ctx := r.Context()
	now := s.cfg.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer tx.Rollback() //nolint:errcheck
	rt, err := store.RefreshTokenForUpdate(ctx, tx, auth.HashToken(value))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			oauthError(w, http.StatusBadRequest, "invalid_grant", i18n.T(reqLang(r), "server.api.oauth.refresh_invalid"))
			return
		}
		s.internalError(w, r, err)
		return
	}
	// 系列を失効させて理由を返す（再利用・失効済みのアクセストークン）。失効はコミットして残す
	revokeFamily := func(logMsg, desc string) {
		if err := store.RevokeRefreshFamily(ctx, tx, rt.FamilyID, now); err != nil {
			s.internalError(w, r, err)
			return
		}
		if err := tx.Commit(); err != nil {
			s.internalError(w, r, err)
			return
		}
		s.cfg.Logger.Warn(logMsg, "client_id", rt.ClientID, "user_id", rt.UserID, "family", rt.FamilyID)
		oauthError(w, http.StatusBadRequest, "invalid_grant", desc)
	}
	switch {
	case rt.ClientID != clientID:
		// 他のクライアントの更新トークン。系列は失効させない（client_id の誤りで利用者の系列を止めない）
		oauthError(w, http.StatusBadRequest, "invalid_grant", i18n.T(reqLang(r), "server.api.oauth.client_mismatch_issued"))
		return
	case rt.RevokedAt.Valid:
		oauthError(w, http.StatusBadRequest, "invalid_grant", i18n.T(reqLang(r), "server.api.oauth.refresh_revoked"))
		return
	case rt.UsedAt.Valid:
		revokeFamily("oauth refresh token reused; family revoked", i18n.T(reqLang(r), "server.api.oauth.refresh_reused"))
		return
	case rt.AccessRevoked:
		revokeFamily("oauth refresh token of revoked access token; family revoked", i18n.T(reqLang(r), "server.api.oauth.refresh_access_revoked"))
		return
	case !rt.ExpiresAt.After(now):
		oauthError(w, http.StatusBadRequest, "invalid_grant", i18n.T(reqLang(r), "server.api.oauth.refresh_expired"))
		return
	}
	if res := r.PostFormValue("resource"); res != "" && !sameResource(res, rt.Resource) && !s.knownResource(r, res) {
		oauthError(w, http.StatusBadRequest, "invalid_target", i18n.T(reqLang(r), "server.api.oauth.resource_changed_issued"))
		return
	}
	u, err := store.UserByID(ctx, tx, rt.UserID)
	if err != nil || u.Disabled {
		oauthError(w, http.StatusBadRequest, "invalid_grant", i18n.T(reqLang(r), "server.api.oauth.user_disabled"))
		return
	}
	if err := store.MarkRefreshTokenUsed(ctx, tx, rt.ID, now); err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := store.RevokeToken(ctx, tx, rt.AccessTokenID); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.internalError(w, r, err)
		return
	}
	s.issueOAuthTokens(w, r, u, rt.ClientID, rt.Scope, rt.Resource, rt.FamilyID, tx)
}

// issueOAuthTokens はアクセストークンと更新トークンの組を発行して応答する。tx があればその中で保存してコミットする。
func (s *Server) issueOAuthTokens(w http.ResponseWriter, r *http.Request, u store.User, clientID, scope, resource, family string, tx *sql.Tx) {
	ctx := r.Context()
	now := s.cfg.Now()
	if tx == nil {
		var err error
		if tx, err = s.db.BeginTx(ctx, nil); err != nil {
			s.internalError(w, r, err)
			return
		}
		defer tx.Rollback() //nolint:errcheck
	}
	token, prefix, err := auth.NewOAuthToken()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	refresh, err := auth.NewRefreshToken()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	client, _ := store.OAuthClientByID(ctx, tx, clientID)
	accessID, err := store.CreateOAuthToken(ctx, tx, u.ID, clientID, client.Name, prefix, auth.HashToken(token), now.Add(oauthTokenLife), now)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	err = store.CreateRefreshToken(ctx, tx, auth.HashToken(refresh), store.RefreshToken{FamilyID: family, UserID: u.ID, ClientID: clientID,
		AccessTokenID: accessID, Scope: scope, Resource: resource, ExpiresAt: now.Add(oauthRefreshLife)})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := tx.Commit(); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.cfg.Logger.Info("oauth token issued", "client_id", clientID, "user", u.Login, "grant", r.PostFormValue("grant_type"))
	w.Header().Set("Access-Control-Allow-Origin", "*")
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  token,
		"token_type":    "Bearer",
		"expires_in":    int(oauthTokenLife.Seconds()),
		"refresh_token": refresh,
		"scope":         scope,
	})
}

// verifyPKCE は S256 の検証（RFC 7636）。
func verifyPKCE(challenge, verifier string) bool {
	if len(verifier) < 43 || len(verifier) > 128 || challenge == "" {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(want), []byte(strings.TrimRight(challenge, "="))) == 1
}
