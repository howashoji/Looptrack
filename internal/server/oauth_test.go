package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/store"
)

// MCP の OAuth 2.1（メタデータ・動的登録・PKCE・認可画面・トークン発行）。

func (e *env) postForm(c *http.Client, path string, form url.Values) (*http.Response, string) {
	e.t.Helper()
	res, err := c.PostForm(e.srv.URL+path, form)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

func (e *env) postJSON(path string, body any) (*http.Response, map[string]any) {
	e.t.Helper()
	b, _ := json.Marshal(body)
	res, err := http.Post(e.srv.URL+path, "application/json", strings.NewReader(string(b)))
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := map[string]any{}
	json.Unmarshal(raw, &out)
	return res, out
}

func (e *env) getJSON(path string) (*http.Response, map[string]any) {
	e.t.Helper()
	res, body := e.get(e.client(), path)
	out := map[string]any{}
	json.Unmarshal([]byte(body), &out)
	return res, out
}

func challengeOf(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func TestOAuthMetadata(t *testing.T) {
	e := newEnv(t)
	base := e.srv.URL
	res, prm := e.getJSON("/.well-known/oauth-protected-resource/im/mcp")
	if res.StatusCode != 200 || prm["resource"] != base+"/im/mcp" || res.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("保護リソースのメタデータ: %d %v", res.StatusCode, prm)
	}
	if prm["resource_name"] != "Looptrack イシュー管理（MCP）" {
		t.Errorf("resource_name: %v", prm["resource_name"])
	}
	if e.s.cfg.Issuer != "Looptrack" { // TOTP の発行者名の既定
		t.Errorf("TOTP の発行者名の既定: %q", e.s.cfg.Issuer)
	}
	if servers, _ := prm["authorization_servers"].([]any); len(servers) != 1 || servers[0] != base+"/im" {
		t.Errorf("authorization_servers: %v", prm["authorization_servers"])
	}
	res, as := e.getJSON("/.well-known/oauth-authorization-server/im")
	if res.StatusCode != 200 || as["issuer"] != base+"/im" || as["authorization_endpoint"] != base+"/im/oauth/authorize" ||
		as["token_endpoint"] != base+"/im/oauth/token" || as["registration_endpoint"] != base+"/im/oauth/register" {
		t.Fatalf("認可サーバのメタデータ: %v", as)
	}
	if m, _ := as["code_challenge_methods_supported"].([]any); len(m) != 1 || m[0] != "S256" {
		t.Errorf("PKCE: %v", as["code_challenge_methods_supported"])
	}
	// /im 配下でも同じものが読める（クライアントによって探す場所が違う）
	for _, p := range []string{"/im/.well-known/oauth-protected-resource", "/im/.well-known/oauth-authorization-server", "/.well-known/oauth-protected-resource/im"} {
		if res, _ := e.get(e.client(), p); res.StatusCode != 200 {
			t.Errorf("%s: %d", p, res.StatusCode)
		}
	}
	// MCP の 401 が入口を案内する
	req, _ := http.NewRequest("POST", base+"/im/mcp", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	r2, err := e.client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	want := `resource_metadata="` + base + `/.well-known/oauth-protected-resource/im/mcp"`
	if r2.StatusCode != 401 || !strings.Contains(r2.Header.Get("WWW-Authenticate"), want) {
		t.Errorf("401 の案内: %d %q", r2.StatusCode, r2.Header.Get("WWW-Authenticate"))
	}
}

func TestOAuthRegister(t *testing.T) {
	e := newEnv(t)
	res, body := e.postJSON("/im/oauth/register", map[string]any{
		"client_name": "Claude Code", "redirect_uris": []string{"http://localhost:51000/callback", "https://claude.ai/api/mcp/auth_callback"},
		"grant_types": []string{"authorization_code"}, "response_types": []string{"code"}, "token_endpoint_auth_method": "none",
	})
	if res.StatusCode != 201 || body["client_id"] == "" || body["token_endpoint_auth_method"] != "none" {
		t.Fatalf("登録: %d %v", res.StatusCode, body)
	}
	// Claude Code の登録要求（grant_types に refresh_token を含む）を受け付ける（実機で拒否していた不具合の回帰防止）
	if res2, body2 := e.postJSON("/im/oauth/register", map[string]any{
		"client_name": "Claude Code (im-oauth)", "redirect_uris": []string{"http://localhost:61234/callback"},
		"grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"},
		"token_endpoint_auth_method": "none",
	}); res2.StatusCode != 201 || body2["client_id"] == "" {
		t.Errorf("Claude Code 形式の登録: %d %v", res2.StatusCode, body2)
	}
	if c, err := store.OAuthClientByID(context.Background(), e.db, body["client_id"].(string)); err != nil || c.Name != "Claude Code" || len(c.RedirectURIs) != 2 {
		t.Errorf("保存: %+v %v", c, err)
	}
	for _, bad := range []map[string]any{
		{"redirect_uris": []string{}},
		{"redirect_uris": []string{"http://evil.example/cb"}},
		{"redirect_uris": []string{"ftp://x/cb"}},
		{"redirect_uris": []string{"https://ok.example/cb#frag"}},
		{"redirect_uris": []string{"https://ok.example/cb"}, "token_endpoint_auth_method": "client_secret_basic"},
		{"redirect_uris": []string{"https://ok.example/cb"}, "grant_types": []string{"password"}},
	} {
		if res, body := e.postJSON("/im/oauth/register", bad); res.StatusCode != 400 || body["error"] == nil {
			t.Errorf("%v: %d %v", bad, res.StatusCode, body)
		}
	}
}

// oauthFlow は登録から認可・トークン発行までを行い、アクセストークンを返す。
func (e *env) oauthFlow(t *testing.T, login, password string) (token, clientID string) {
	t.Helper()
	_, reg := e.postJSON("/im/oauth/register", map[string]any{
		"client_name": "テスト用クライアント", "redirect_uris": []string{"http://localhost:51000/callback"},
	})
	clientID = reg["client_id"].(string)
	verifier := "verifier-" + strings.Repeat("a", 40)
	q := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {"http://localhost:51000/callback"},
		"code_challenge": {challengeOf(verifier)}, "code_challenge_method": {"S256"}, "state": {"st-1"},
		"scope": {"im"}, "resource": {e.srv.URL + "/im/mcp"}}

	c := e.client()
	// 未ログインならログイン画面へ送られ、ログイン後に認可画面へ戻る
	res, _ := e.get(c, "/im/oauth/authorize?"+q.Encode())
	if res.StatusCode != http.StatusSeeOther || !strings.Contains(res.Header.Get("Location"), "/im/login?next=") {
		t.Fatalf("未ログインの認可: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	e.enroll(c, login, password)
	res, page := e.get(c, "/im/oauth/authorize?"+q.Encode())
	if res.StatusCode != 200 || !strings.Contains(page, "テスト用クライアント") || !strings.Contains(page, "localhost:51000") {
		t.Fatalf("承認画面: %d %s", res.StatusCode, page)
	}
	// 承認後の戻り先（別オリジン）をブラウザが止めないよう、この画面だけ form-action を広げる
	if csp := res.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'self' http://localhost:51000") {
		t.Errorf("承認画面の CSP: %s", csp)
	}
	res, _ = e.postForm(c, "/im/oauth/authorize", url.Values{"csrf": {csrfOf(t, page)}, "query": {q.Encode()}, "approve": {"1"}})
	loc, err := url.Parse(res.Header.Get("Location"))
	if err != nil || res.StatusCode != http.StatusSeeOther || loc.Query().Get("state") != "st-1" || loc.Query().Get("code") == "" {
		t.Fatalf("承認後: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	res2, tok := e.postJSON2("/im/oauth/token", url.Values{"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")},
		"redirect_uri": {"http://localhost:51000/callback"}, "client_id": {clientID}, "code_verifier": {verifier},
		"resource": {e.srv.URL + "/im/mcp"}})
	if res2.StatusCode != 200 || tok["token_type"] != "Bearer" || tok["access_token"] == nil {
		t.Fatalf("トークン: %d %v", res2.StatusCode, tok)
	}
	return tok["access_token"].(string), clientID
}

func (e *env) postJSON2(path string, form url.Values) (*http.Response, map[string]any) {
	e.t.Helper()
	res, body := e.postForm(e.client(), path, form)
	out := map[string]any{}
	json.Unmarshal([]byte(body), &out)
	return res, out
}

func TestOAuthFlow(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("req")
	u := e.user("oauth", "oauth-password-123", "member")
	store.SetMember(ctx, e.db, pr.ID, u.ID, "editor")

	token, clientID := e.oauthFlow(t, "oauth", "oauth-password-123")

	// 発行したトークンで MCP が使える（PAT と同じ扱い）
	m := e.mcpAs(token, map[string]string{"X-Looptrack-Project": "req"})
	if text, _ := m.call("list_projects", nil, false); !strings.Contains(text, "req（") {
		t.Errorf("MCP: %s", text)
	}
	m.call("create_issue", map[string]any{"title": "OAuth 経由の起票"}, false)

	// REST でも使える。記録はクライアント名つき
	a := &apiClient{e: e, c: e.client(), token: token, header: map[string]string{}}
	a.json(200, "GET", "/me", nil, nil)
	var kind, name, client string
	var expires time.Time
	e.db.QueryRow("SELECT kind, name, oauth_client_id, expires_at FROM api_tokens WHERE token_prefix LIKE 'imo_%'").Scan(&kind, &name, &client, &expires)
	if kind != "oauth" || name != "テスト用クライアント" || client != clientID || time.Until(expires) < 29*24*time.Hour {
		t.Errorf("トークンの記録: kind=%s name=%s client=%s expires=%s", kind, name, client, expires)
	}
}

func TestOAuthRejects(t *testing.T) {
	e := newEnv(t)
	e.user("oauth", "oauth-password-123", "member")
	c := e.client()
	e.enroll(c, "oauth", "oauth-password-123")
	_, reg := e.postJSON("/im/oauth/register", map[string]any{"client_name": "c", "redirect_uris": []string{"http://localhost:51000/callback"}})
	clientID := reg["client_id"].(string)
	verifier := "verifier-" + strings.Repeat("b", 40)
	base := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {"http://localhost:51000/callback"},
		"code_challenge": {challengeOf(verifier)}, "code_challenge_method": {"S256"}, "state": {"st"}}

	// 画面を出す前に弾くもの（戻り先が確かめられない・PKCE 無し・resource 違い）
	bad := func(q url.Values) (int, string) {
		res, body := e.get(c, "/im/oauth/authorize?"+q.Encode())
		return res.StatusCode, res.Header.Get("Location") + body
	}
	q := url.Values{"response_type": {"code"}, "client_id": {"unknown"}, "redirect_uri": {"http://localhost:51000/callback"}}
	if code, body := bad(q); code != 400 || !strings.Contains(body, "クライアントが登録されていません") {
		t.Errorf("未登録のクライアント: %d %s", code, body)
	}
	q = url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {"http://localhost:9999/other"}}
	if code, body := bad(q); code != 400 || !strings.Contains(body, "戻り先の URL") {
		t.Errorf("未登録の戻り先: %d %s", code, body)
	}
	for _, c2 := range []struct{ key, val, want string }{
		{"code_challenge_method", "plain", "invalid_request"},
		{"response_type", "token", "unsupported_response_type"},
		{"resource", "https://evil.example/im/mcp", "invalid_target"},
	} {
		q := url.Values{}
		for k, v := range base {
			q[k] = v
		}
		q.Set(c2.key, c2.val)
		res, _ := e.get(c, "/im/oauth/authorize?"+q.Encode())
		loc := res.Header.Get("Location")
		if res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "http://localhost:51000/callback?") || !strings.Contains(loc, "error="+c2.want) {
			t.Errorf("%s=%s: %d %s", c2.key, c2.val, res.StatusCode, loc)
		}
	}

	// 許可しないを選ぶと access_denied で戻る
	_, page := e.get(c, "/im/oauth/authorize?"+base.Encode())
	res, _ := e.postForm(c, "/im/oauth/authorize", url.Values{"csrf": {csrfOf(t, page)}, "query": {base.Encode()}, "approve": {"0"}})
	if !strings.Contains(res.Header.Get("Location"), "error=access_denied") {
		t.Errorf("拒否: %s", res.Header.Get("Location"))
	}
	// CSRF なしの承認は 403
	if res, _ := e.postForm(c, "/im/oauth/authorize", url.Values{"query": {base.Encode()}, "approve": {"1"}}); res.StatusCode != http.StatusForbidden {
		t.Errorf("CSRF なし: %d", res.StatusCode)
	}

	// 認可コードの引き換えの検査
	code := func() string {
		_, page := e.get(c, "/im/oauth/authorize?"+base.Encode())
		res, _ := e.postForm(c, "/im/oauth/authorize", url.Values{"csrf": {csrfOf(t, page)}, "query": {base.Encode()}, "approve": {"1"}})
		loc, _ := url.Parse(res.Header.Get("Location"))
		return loc.Query().Get("code")
	}
	good := url.Values{"grant_type": {"authorization_code"}, "redirect_uri": {"http://localhost:51000/callback"},
		"client_id": {clientID}, "code_verifier": {verifier}}
	for _, c2 := range []struct {
		name string
		mod  func(v url.Values)
		want string
	}{
		{"code_verifier が違う", func(v url.Values) { v.Set("code_verifier", "wrong-"+strings.Repeat("c", 40)) }, "invalid_grant"},
		{"client_id が違う", func(v url.Values) { v.Set("client_id", "other") }, "invalid_grant"},
		{"redirect_uri が違う", func(v url.Values) { v.Set("redirect_uri", "http://localhost:51000/x") }, "invalid_grant"},
		{"grant_type が違う", func(v url.Values) { v.Set("grant_type", "password") }, "unsupported_grant_type"},
		{"resource が違う", func(v url.Values) { v.Set("resource", "https://evil.example/im/mcp") }, "invalid_target"},
	} {
		v := url.Values{}
		for k, val := range good {
			v[k] = val
		}
		v.Set("code", code())
		c2.mod(v)
		if res, body := e.postJSON2("/im/oauth/token", v); res.StatusCode != 400 || body["error"] != c2.want {
			t.Errorf("%s: %d %v", c2.name, res.StatusCode, body)
		}
	}
	// 同じコードは 1 回だけ
	v := url.Values{}
	for k, val := range good {
		v[k] = val
	}
	v.Set("code", code())
	if res, _ := e.postJSON2("/im/oauth/token", v); res.StatusCode != 200 {
		t.Fatalf("1 回目: %d", res.StatusCode)
	}
	e.clock.Add(time.Second) // 実運用と同じく時刻が進んでから再利用を試す
	if res, body := e.postJSON2("/im/oauth/token", v); res.StatusCode != 400 || body["error"] != "invalid_grant" {
		t.Errorf("再利用: %d %v", res.StatusCode, body)
	}
	// 期限切れのコード
	v.Set("code", code())
	e.clock.Add(authCodeLifetime + time.Minute)
	if res, body := e.postJSON2("/im/oauth/token", v); res.StatusCode != 400 || body["error"] != "invalid_grant" {
		t.Errorf("期限切れ: %d %v", res.StatusCode, body)
	}
}

func TestOAuthTokenExpiry(t *testing.T) {
	e := newEnv(t)
	e.project("req")
	e.user("oauth", "oauth-password-123", "member")
	token, _ := e.oauthFlow(t, "oauth", "oauth-password-123")
	a := &apiClient{e: e, c: e.client(), token: token, header: map[string]string{}}
	a.json(200, "GET", "/me", nil, nil)
	e.clock.Add(oauthTokenLife + time.Hour)
	a.fail(401, "GET", "/me", nil)
}
