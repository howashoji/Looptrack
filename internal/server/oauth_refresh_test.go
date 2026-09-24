package server

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/store"
)

// CLI のブラウザログイン向けのサーバ側（ループバックのポート照合・resource に API の基点・更新トークン）。

type oauthCase struct {
	e        *env
	c        *http.Client // TOTP 済みのブラウザ
	clientID string
	verifier string
}

func (e *env) oauthSetup(t *testing.T, login, password string, redirects ...string) *oauthCase {
	t.Helper()
	c := e.client()
	e.enroll(c, login, password)
	return e.oauthRegisterWith(t, c, redirects...)
}

// oauthRegisterWith は TOTP 済みのブラウザ c のまま、別のクライアントを登録する。
func (e *env) oauthRegisterWith(t *testing.T, c *http.Client, redirects ...string) *oauthCase {
	t.Helper()
	_, reg := e.postJSON("/im/oauth/register", map[string]any{"client_name": "looptrack（テスト）", "redirect_uris": redirects,
		"grant_types": []string{"authorization_code", "refresh_token"}})
	id, _ := reg["client_id"].(string)
	if id == "" {
		t.Fatalf("登録: %v", reg)
	}
	return &oauthCase{e: e, c: c, clientID: id, verifier: "verifier-" + strings.Repeat("r", 40)}
}

func (o *oauthCase) query(redirect, resource string) url.Values {
	q := url.Values{"response_type": {"code"}, "client_id": {o.clientID}, "redirect_uri": {redirect},
		"code_challenge": {challengeOf(o.verifier)}, "code_challenge_method": {"S256"}, "state": {"st-r"}}
	if resource != "" {
		q.Set("resource", resource)
	}
	return q
}

// authorize は承認画面を開いて許可し、戻り先の Location を返す。画面が出なければ (状態コード, 本文) を返す。
func (o *oauthCase) authorize(redirect, resource string) (loc *url.URL, status int, body string) {
	q := o.query(redirect, resource)
	res, page := o.e.get(o.c, "/im/oauth/authorize?"+q.Encode())
	if res.StatusCode == http.StatusSeeOther { // 画面を出す前に戻り先へ error を付けて返した
		l, _ := url.Parse(res.Header.Get("Location"))
		return l, res.StatusCode, ""
	}
	if res.StatusCode != 200 {
		return nil, res.StatusCode, res.Header.Get("Location") + page
	}
	res, _ = o.e.postForm(o.c, "/im/oauth/authorize", url.Values{"csrf": {csrfOf(o.e.t, page)}, "query": {q.Encode()}, "approve": {"1"}})
	l, _ := url.Parse(res.Header.Get("Location"))
	return l, res.StatusCode, ""
}

// token は認可からトークン発行までを通し、トークンの応答を返す。
func (o *oauthCase) token(t *testing.T, redirect, resource string) map[string]any {
	t.Helper()
	loc, status, body := o.authorize(redirect, resource)
	if loc == nil || loc.Query().Get("code") == "" {
		t.Fatalf("認可: %d %v %s", status, loc, body)
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")}, "redirect_uri": {redirect},
		"client_id": {o.clientID}, "code_verifier": {o.verifier}}
	if resource != "" {
		form.Set("resource", resource)
	}
	res, tok := o.e.postJSON2("/im/oauth/token", form)
	if res.StatusCode != 200 || tok["access_token"] == nil {
		t.Fatalf("トークン: %d %v", res.StatusCode, tok)
	}
	return tok
}

func (o *oauthCase) refresh(refresh string, extra ...string) (*http.Response, map[string]any) {
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {o.clientID}}
	for i := 0; i+1 < len(extra); i += 2 {
		form.Set(extra[i], extra[i+1])
	}
	return o.e.postJSON2("/im/oauth/token", form)
}

func TestOAuthLoopbackAnyPort(t *testing.T) {
	e := newEnv(t)
	e.user("cli", "cli-password-123", "member")
	o := e.oauthSetup(t, "cli", "cli-password-123", "http://127.0.0.1:51000/callback", "http://[::1]:51000/callback", "http://localhost:51000/callback")

	// RFC 8252 §7.3: ループバックの http はポートを問わない（CLI は毎回空きポートで待ち受ける）
	for _, redirect := range []string{"http://127.0.0.1:62345/callback", "http://[::1]:40000/callback", "http://localhost:1234/callback", "http://127.0.0.1/callback"} {
		loc, status, body := o.authorize(redirect, "")
		if loc == nil || !strings.HasPrefix(loc.String(), redirect+"?") || loc.Query().Get("code") == "" || loc.Query().Get("state") != "st-r" {
			t.Errorf("%s: %d %v %s", redirect, status, loc, body)
			continue
		}
		// トークン発行でも、認可のときの戻り先（ポートつき）と一致すれば通る。違うポートは拒否
		form := url.Values{"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")}, "redirect_uri": {redirect + "x"},
			"client_id": {o.clientID}, "code_verifier": {o.verifier}}
		if res, body := e.postJSON2("/im/oauth/token", form); res.StatusCode != 400 || body["error"] != "invalid_grant" {
			t.Errorf("%s: 認可と違う戻り先で引き換えた: %d %v", redirect, res.StatusCode, body)
		}
	}
	if tok := o.token(t, "http://127.0.0.1:62346/callback", ""); tok["access_token"] == nil {
		t.Error("ポート違いの戻り先でトークンが出ない")
	}
	// 承認画面の CSP は実際の戻り先（ポートつき）を許す
	res, _ := e.get(o.c, "/im/oauth/authorize?"+o.query("http://127.0.0.1:62347/callback", "").Encode())
	if csp := res.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'self' http://127.0.0.1:62347") {
		t.Errorf("CSP: %s", csp)
	}

	// ポート以外が違うものは拒否（パス・ホストの表記・スキーム・問い合わせ・利用者情報）
	for _, redirect := range []string{
		"http://127.0.0.1:62345/other", "http://127.0.0.2:51000/callback", "https://127.0.0.1:51000/callback",
		"http://127.0.0.1:62345/callback?x=1", "http://u@127.0.0.1:62345/callback", "http://127.0.0.1:62345/callback/",
		"http://evil.example:51000/callback",
	} {
		if loc, status, body := o.authorize(redirect, ""); loc != nil || status != 400 || !strings.Contains(body, "戻り先の URL") {
			t.Errorf("%s を受け付けた: %d %v", redirect, status, loc)
		}
	}
	// localhost と 127.0.0.1 は別のものとして照合する（登録が 127.0.0.1 だけなら localhost は拒否）
	o2 := e.oauthRegisterWith(t, o.c, "http://127.0.0.1:51000/callback")
	if loc, status, _ := o2.authorize("http://localhost:51000/callback", ""); loc != nil || status != 400 {
		t.Errorf("登録に無い localhost を受け付けた: %d", status)
	}

	// https と非ループバックは完全一致のまま（ポートも区別する）
	o3 := e.oauthRegisterWith(t, o.c, "https://client.example/cb")
	for _, redirect := range []string{"https://client.example:8443/cb", "https://client.example/cb2", "http://client.example/cb"} {
		if loc, status, _ := o3.authorize(redirect, ""); loc != nil || status != 400 {
			t.Errorf("https の %s を受け付けた: %d", redirect, status)
		}
	}
	if loc, _, _ := o3.authorize("https://client.example/cb", ""); loc == nil || loc.Query().Get("code") == "" {
		t.Error("https の完全一致が通らない")
	}
}

func TestOAuthResourceAPIBase(t *testing.T) {
	e := newEnv(t)
	e.project("req")
	e.user("cli", "cli-password-123", "member")
	o := e.oauthSetup(t, "cli", "cli-password-123", "http://127.0.0.1:51000/callback")
	for _, res := range []string{e.srv.URL + "/im/api/v1", e.srv.URL + "/im/api/v1/", e.srv.URL + "/im/mcp"} {
		tok := o.token(t, "http://127.0.0.1:51001/callback", res)
		if code := e.meStatus(tok["access_token"].(string)); code != 200 {
			t.Errorf("resource=%s のトークンで /me: %d", res, code)
		}
	}
	for _, bad := range []string{e.srv.URL + "/im/api", e.srv.URL + "/im", "https://evil.example/im/api/v1"} {
		loc, status, _ := o.authorize("http://127.0.0.1:51001/callback", bad)
		if loc == nil || loc.Query().Get("error") != "invalid_target" {
			t.Errorf("resource=%s: %d %v", bad, status, loc)
		}
	}
	// 引き換えのときの resource も同じ規則（認可のときと同じもの・MCP・API の基点のどれか）
	loc, _, _ := o.authorize("http://127.0.0.1:51001/callback", e.srv.URL+"/im/api/v1")
	form := url.Values{"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")}, "redirect_uri": {"http://127.0.0.1:51001/callback"},
		"client_id": {o.clientID}, "code_verifier": {o.verifier}, "resource": {"https://evil.example/im/api/v1"}}
	if res, body := e.postJSON2("/im/oauth/token", form); res.StatusCode != 400 || body["error"] != "invalid_target" {
		t.Errorf("引き換えの resource 違い: %d %v", res.StatusCode, body)
	}
}

func TestOAuthRefreshRotation(t *testing.T) {
	e := newEnv(t)
	e.project("req")
	e.user("cli", "cli-password-123", "member")

	_, as := e.getJSON("/.well-known/oauth-authorization-server/im")
	if g, _ := as["grant_types_supported"].([]any); len(g) != 2 || g[1] != "refresh_token" {
		t.Errorf("grant_types_supported: %v", as["grant_types_supported"])
	}

	o := e.oauthSetup(t, "cli", "cli-password-123", "http://127.0.0.1:51000/callback")
	tok := o.token(t, "http://127.0.0.1:51000/callback", e.srv.URL+"/im/api/v1")
	access1, _ := tok["access_token"].(string)
	refresh1, _ := tok["refresh_token"].(string)
	if !strings.HasPrefix(refresh1, "imr_") || refresh1 == access1 {
		t.Fatalf("更新トークンが無い: %v", tok)
	}
	var plain int
	e.db.QueryRow("SELECT COUNT(*) FROM oauth_refresh_tokens WHERE CAST(token_hash AS CHAR) LIKE ?", "%"+refresh1+"%").Scan(&plain)
	if plain != 0 {
		t.Error("更新トークンの平文が DB にある")
	}

	// 入れ替え: 新しいアクセストークンと更新トークン。古いアクセストークンは失効（アカウント画面に有効なものが 1 つだけ残る）
	e.clock.Add(time.Hour)
	res, tok2 := o.refresh(refresh1, "resource", e.srv.URL+"/im/api/v1")
	access2, _ := tok2["access_token"].(string)
	refresh2, _ := tok2["refresh_token"].(string)
	if res.StatusCode != 200 || access2 == "" || refresh2 == "" || refresh2 == refresh1 || access2 == access1 || tok2["token_type"] != "Bearer" {
		t.Fatalf("更新: %d %v", res.StatusCode, tok2)
	}
	if res.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control: %q", res.Header.Get("Cache-Control"))
	}
	if code := e.meStatus(access2); code != 200 {
		t.Errorf("新しいアクセストークン: %d", code)
	}
	if code := e.meStatus(access1); code != http.StatusUnauthorized {
		t.Errorf("入れ替え前のアクセストークンが使える: %d", code)
	}
	var name, client string
	e.db.QueryRow("SELECT name, oauth_client_id FROM api_tokens WHERE revoked_at IS NULL AND kind = 'oauth'").Scan(&name, &client)
	if name != "looptrack（テスト）" || client != o.clientID {
		t.Errorf("新しいアクセストークンの記録: %s %s", name, client)
	}

	// 再利用の検知: 使用済みの更新トークンが出されたら、系列ごと失効（新しいアクセストークンも更新トークンも使えない）
	res, body := o.refresh(refresh1)
	if res.StatusCode != 400 || body["error"] != "invalid_grant" {
		t.Errorf("再利用: %d %v", res.StatusCode, body)
	}
	if code := e.meStatus(access2); code != http.StatusUnauthorized {
		t.Errorf("再利用の検知の後もアクセストークンが使える: %d", code)
	}
	if res, body := o.refresh(refresh2); res.StatusCode != 400 || body["error"] != "invalid_grant" {
		t.Errorf("再利用の検知の後も系列の更新トークンが使える: %d %v", res.StatusCode, body)
	}
	// 別の系列（別のログイン）には影響しない
	tok3 := o.token(t, "http://127.0.0.1:51000/callback", "")
	if res, _ := o.refresh(tok3["refresh_token"].(string)); res.StatusCode != 200 {
		t.Errorf("別の系列: %d", res.StatusCode)
	}
}

func TestOAuthRefreshRejects(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.project("req")
	u := e.user("cli", "cli-password-123", "member")
	o := e.oauthSetup(t, "cli", "cli-password-123", "http://127.0.0.1:51000/callback")
	fresh := func() (string, string) {
		tok := o.token(t, "http://127.0.0.1:51000/callback", "")
		return tok["access_token"].(string), tok["refresh_token"].(string)
	}

	// 対のアクセストークンをアカウント画面で失効させると、更新トークンも使えない
	access, refresh := fresh()
	var id int64
	e.db.QueryRow("SELECT id FROM api_tokens WHERE token_prefix = ?", access[:12]).Scan(&id)
	if err := store.RevokeUserToken(ctx, e.db, u.ID, id); err != nil {
		t.Fatal(err)
	}
	if res, body := o.refresh(refresh); res.StatusCode != 400 || body["error"] != "invalid_grant" {
		t.Errorf("失効したアクセストークンの更新トークン: %d %v", res.StatusCode, body)
	}

	// client_id が違う・無い・resource が違う・値が不正
	_, refresh = fresh()
	for _, c := range []struct {
		name  string
		extra []string
		want  string
	}{
		{"client_id が違う", []string{"client_id", "other"}, "invalid_grant"},
		{"client_id が無い", []string{"client_id", ""}, "invalid_request"},
		{"resource が違う", []string{"resource", "https://evil.example/im/api/v1"}, "invalid_target"},
	} {
		if res, body := o.refresh(refresh, c.extra...); res.StatusCode != 400 || body["error"] != c.want {
			t.Errorf("%s: %d %v", c.name, res.StatusCode, body)
		}
	}
	for _, bad := range []string{"", "imr_unknown"} {
		if res, body := o.refresh(bad); res.StatusCode != 400 || (body["error"] != "invalid_grant" && body["error"] != "invalid_request") {
			t.Errorf("値 %q: %d %v", bad, res.StatusCode, body)
		}
	}
	// 拒否した後も、正しい要求なら使える（client_id 違いで系列を失効させない）
	if res, body := o.refresh(refresh); res.StatusCode != 200 {
		t.Errorf("正しい更新: %d %v", res.StatusCode, body)
	}

	// 無効化された利用者
	_, refresh = fresh()
	e.db.Exec("UPDATE users SET disabled_at = CURRENT_TIMESTAMP(6) WHERE id = ?", u.ID)
	if res, body := o.refresh(refresh); res.StatusCode != 400 || body["error"] != "invalid_grant" {
		t.Errorf("無効化された利用者: %d %v", res.StatusCode, body)
	}
}

func TestOAuthRefreshSlidingExpiry(t *testing.T) {
	e := newEnv(t)
	e.project("req")
	e.user("cli", "cli-password-123", "member")
	o := e.oauthSetup(t, "cli", "cli-password-123", "http://127.0.0.1:51000/callback")
	tok := o.token(t, "http://127.0.0.1:51000/callback", "")
	refresh := tok["refresh_token"].(string)
	if tok["expires_in"] != float64(oauthTokenLife.Seconds()) {
		t.Errorf("expires_in: %v", tok["expires_in"])
	}
	// 使うたびに期限が延びる（90 日以内に一度でも使えば切れない）
	for i := 0; i < 3; i++ {
		e.clock.Add(oauthRefreshLife - 24*time.Hour)
		res, body := o.refresh(refresh)
		if res.StatusCode != 200 {
			t.Fatalf("%d 回目の更新: %d %v", i+1, res.StatusCode, body)
		}
		refresh = body["refresh_token"].(string)
		if code := e.meStatus(body["access_token"].(string)); code != 200 {
			t.Errorf("%d 回目のアクセストークン: %d", i+1, code)
		}
	}
	// 90 日使わなければ切れる（ログインのやり直し）
	e.clock.Add(oauthRefreshLife + time.Minute)
	if res, body := o.refresh(refresh); res.StatusCode != 400 || body["error"] != "invalid_grant" {
		t.Errorf("期限切れ: %d %v", res.StatusCode, body)
	}
}
