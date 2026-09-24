package server

import (
	"bytes"
	"context"
	"encoding/base32"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/auth"
	"github.com/howashoji/looptrack/internal/store"
)

// 二段階認証（TOTP）の必須 / 任意。受け入れ条件ごとに HTTP 経由で確かめる。

// setTwoFactor は管理コマンドと同じ経路（store.SetTwoFactorPolicy）で設定を変える。
func (e *env) setTwoFactor(value string) store.TwoFactorResult {
	e.t.Helper()
	res, err := store.SetTwoFactorPolicy(context.Background(), e.db, value, store.SettingChange{Via: "command", Note: "test"})
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

func (e *env) twoFactor() string {
	e.t.Helper()
	v, _, err := store.TwoFactorPolicy(context.Background(), e.db)
	if err != nil {
		e.t.Fatal(err)
	}
	return v
}

func (e *env) totpState(login string) (enabled bool, secret []byte) {
	e.t.Helper()
	if err := e.db.QueryRow("SELECT totp_enabled, totp_secret FROM users WHERE login = ?", login).Scan(&enabled, &secret); err != nil {
		e.t.Fatal(err)
	}
	return enabled, secret
}

// loginPasswordOnly はパスワードだけでログインが完了する（任意・未登録）ことを確かめる。
func (e *env) loginPasswordOnly(c *http.Client, login, password string) {
	e.t.Helper()
	res, _ := e.login(c, login, password)
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/im/" {
		e.t.Fatalf("任意・未登録のログイン: %d %s, want /im/ へ", res.StatusCode, res.Header.Get("Location"))
	}
}

func secretFromPage(t *testing.T, page string) []byte {
	t.Helper()
	m := secretRe.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("シークレットが表示されない: %s", page)
	}
	secret, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(m[1])
	return secret
}

// 条件 2: 任意のとき、TOTP 未登録の利用者は ID / パスワードだけでセッションが発行される（MCP の OAuth 認可画面を含む）。
func TestTwoFactorOptionalPasswordOnly(t *testing.T) {
	e := newEnv(t)
	e.user("bob", "bob-password-123", "member")
	e.setTwoFactor(store.TwoFactorOptional)
	c := e.client()
	e.loginPasswordOnly(c, "bob", "bob-password-123")
	if res, _ := e.get(c, "/im/"); res.StatusCode != 200 {
		t.Errorf("画面: %d", res.StatusCode)
	}
	if res, _ := e.get(c, "/im/api/v1/me"); res.StatusCode != 200 {
		t.Errorf("セッションでの API: %d", res.StatusCode)
	}
	var mfa, verified bool
	e.db.QueryRow("SELECT mfa_passed, totp_verified FROM web_sessions WHERE user_id = (SELECT id FROM users WHERE login = 'bob')").Scan(&mfa, &verified)
	if !mfa || verified {
		t.Errorf("セッション: mfa_passed=%v totp_verified=%v, want true / false", mfa, verified)
	}

	// MCP の OAuth 認可画面もパスワードだけで開ける
	_, reg := e.postJSON("/im/oauth/register", map[string]any{"client_name": "任意のクライアント", "redirect_uris": []string{"http://localhost:51000/callback"}})
	q := url.Values{"response_type": {"code"}, "client_id": {reg["client_id"].(string)}, "redirect_uri": {"http://localhost:51000/callback"},
		"code_challenge": {challengeOf("verifier-" + strings.Repeat("b", 40))}, "code_challenge_method": {"S256"}, "state": {"s"},
		"scope": {"im"}, "resource": {e.srv.URL + "/im/mcp"}}
	c2 := e.client()
	res, _ := e.get(c2, "/im/oauth/authorize?"+q.Encode())
	next, _ := url.Parse(res.Header.Get("Location"))
	_, page := e.get(c2, "/im/login")
	res, _ = e.post(c2, "/im/login", url.Values{"csrf": {csrfOf(t, page)}, "login": {"bob"}, "password": {"bob-password-123"}, "next": {next.Query().Get("next")}})
	if res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(res.Header.Get("Location"), "/im/oauth/authorize?") {
		t.Fatalf("ログイン後: %d %s, want 認可画面へ", res.StatusCode, res.Header.Get("Location"))
	}
	res, page = e.get(c2, res.Header.Get("Location"))
	if res.StatusCode != 200 || !strings.Contains(page, "任意のクライアント") {
		t.Errorf("認可画面: %d %s", res.StatusCode, page)
	}
}

// 条件 3: 任意のとき、TOTP 登録済みの利用者は TOTP を入力するまでセッションが発行されない。
func TestTwoFactorOptionalEnrolledNeedsCode(t *testing.T) {
	e := newEnv(t)
	e.user("alice", "alice-password-1", "member")
	secret := e.enroll(e.client(), "alice", "alice-password-1") // 必須（未設定）のうちに登録
	e.setTwoFactor(store.TwoFactorOptional)

	c := e.client()
	e.clock.Add(30 * time.Second)
	res, _ := e.login(c, "alice", "alice-password-1")
	if res.Header.Get("Location") != "/im/login/totp" {
		t.Fatalf("任意・登録済みのパスワード後: %s, want /im/login/totp", res.Header.Get("Location"))
	}
	if res, _ := e.get(c, "/im/api/v1/me"); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("TOTP 前の API: %d, want 401", res.StatusCode)
	}
	if res, _ := e.get(c, "/im/"); res.Header.Get("Location") != "/im/login/totp" {
		t.Errorf("TOTP 前の画面: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	_, page := e.get(c, "/im/login/totp")
	if res, _ := e.post(c, "/im/login/totp", url.Values{"csrf": {csrfOf(t, page)}, "code": {"000000"}}); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("誤ったコード: %d", res.StatusCode)
	}
	res, _ = e.post(c, "/im/login/totp", url.Values{"csrf": {csrfOf(t, page)}, "code": {e.code(secret)}})
	if res.Header.Get("Location") != "/im/" {
		t.Fatalf("TOTP 後: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if res, _ := e.get(c, "/im/api/v1/me"); res.StatusCode != 200 {
		t.Errorf("TOTP 後の API: %d", res.StatusCode)
	}
}

// 条件 4: 任意のとき、利用者はアカウント設定から自分で TOTP を登録・解除できる（必須のときは解除できない）。
func TestTwoFactorAccountEnrollAndDisable(t *testing.T) {
	e := newEnv(t)
	e.user("bob", "bob-password-123", "member")
	e.setTwoFactor(store.TwoFactorOptional)
	c := e.client()
	e.loginPasswordOnly(c, "bob", "bob-password-123")
	other := e.client() // 別端末のセッション（登録で破棄される）
	e.loginPasswordOnly(other, "bob", "bob-password-123")

	_, acct := e.get(c, "/im/account")
	if !strings.Contains(acct, "二段階認証を登録する") {
		t.Fatalf("アカウント設定に登録の導線が無い: %s", acct)
	}
	res, page := e.get(c, "/im/account/totp")
	if res.StatusCode != 200 {
		t.Fatalf("登録画面: %d", res.StatusCode)
	}
	secret := secretFromPage(t, page)
	if res, _ := e.post(c, "/im/account/totp", url.Values{"csrf": {csrfOf(t, page)}, "code": {"000000"}}); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("誤ったコード: %d", res.StatusCode)
	}
	if on, _ := e.totpState("bob"); on {
		t.Fatal("誤ったコードで登録された")
	}
	res, _ = e.post(c, "/im/account/totp", url.Values{"csrf": {csrfOf(t, page)}, "code": {e.code(secret)}})
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/im/account?done=totp" {
		t.Fatalf("登録: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if on, _ := e.totpState("bob"); !on {
		t.Fatal("登録されていない")
	}
	if res, _ := e.get(c, "/im/account"); res.StatusCode != 200 {
		t.Errorf("登録した画面のセッションが残らない: %d", res.StatusCode)
	}
	if res, _ := e.get(other, "/im/"); res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(res.Header.Get("Location"), "/im/login") {
		t.Errorf("TOTP を経ていない別のセッションが残った: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	// 登録後は任意でもログインに TOTP が要る
	e.relogin(e.client(), "bob", "bob-password-123", secret)

	// 解除: パスワード・コードの誤りでは解除しない
	e.clock.Add(30 * time.Second)
	if res, _ := e.formAt(c, "/im/account", "/im/account/totp/disable", url.Values{"password": {"wrong-password-1"}, "code": {e.code(secret)}}); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("誤ったパスワードで解除: %d", res.StatusCode)
	}
	if res, _ := e.formAt(c, "/im/account", "/im/account/totp/disable", url.Values{"password": {"bob-password-123"}, "code": {"000000"}}); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("誤ったコードで解除: %d", res.StatusCode)
	}
	if on, _ := e.totpState("bob"); !on {
		t.Fatal("誤りで解除された")
	}
	// 必須のときは解除できない
	e.setTwoFactor(store.TwoFactorRequired) // 登録済み・TOTP 済みのセッション（c）は必須でも使い続けられる
	e.clock.Add(30 * time.Second)
	if res, _ := e.formAt(c, "/im/account", "/im/account/totp/disable", url.Values{"password": {"bob-password-123"}, "code": {e.code(secret)}}); res.StatusCode != http.StatusForbidden {
		t.Errorf("必須のときの解除: %d, want 403", res.StatusCode)
	}
	if on, _ := e.totpState("bob"); !on {
		t.Fatal("必須なのに解除された")
	}
	e.setTwoFactor(store.TwoFactorOptional)
	e.clock.Add(30 * time.Second)
	res, _ = e.formAt(c, "/im/account", "/im/account/totp/disable", url.Values{"password": {"bob-password-123"}, "code": {e.code(secret)}})
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/im/account?done=totp-off" {
		t.Fatalf("解除: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if on, sec := e.totpState("bob"); on || sec != nil {
		t.Errorf("解除後: enabled=%v secret=%v", on, sec != nil)
	}
	if res, _ := e.get(c, "/im/account"); res.StatusCode != 200 {
		t.Errorf("解除した画面のセッションが残らない: %d", res.StatusCode)
	}
	e.loginPasswordOnly(e.client(), "bob", "bob-password-123")
}

// 条件 5: 必須のとき、TOTP 未登録の利用者は次のログインで登録を求められ、登録するまで TOTP 済みのセッションにならない。
func TestTwoFactorRequiredCurrentBehavior(t *testing.T) {
	e := newEnv(t)
	e.user("bob", "bob-password-123", "member")
	e.setTwoFactor(store.TwoFactorRequired)
	c := e.client()
	res, _ := e.login(c, "bob", "bob-password-123")
	if res.Header.Get("Location") != "/im/login/totp/setup" {
		t.Fatalf("必須・未登録のパスワード後: %s", res.Header.Get("Location"))
	}
	if res, _ := e.get(c, "/im/api/v1/me"); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("登録前の API: %d", res.StatusCode)
	}
	if res, _ := e.get(c, "/im/account"); res.Header.Get("Location") != "/im/login/totp/setup" {
		t.Errorf("登録前の画面: %s", res.Header.Get("Location"))
	}
	e.enroll(e.client(), "bob", "bob-password-123")
}

// adminWithTOTP は TOTP 登録済みの管理者と、そのログイン済みクライアント・シークレットを返す。
func (e *env) adminWithTOTP(login, password string) (*http.Client, []byte) {
	e.t.Helper()
	e.user(login, password, "admin")
	c := e.client()
	return c, e.enroll(c, login, password)
}

// 条件 6・7・9: 管理画面での切り替え（管理者以外は 403・必須 → 任意は再認証・記録と表示）。
func TestTwoFactorAdminSwitch(t *testing.T) {
	e := newEnv(t)
	e.setTwoFactor(store.TwoFactorRequired)
	admin, secret := e.adminWithTOTP("alice", "alice-password-1")
	e.user("bob", "bob-password-123", "member")
	member := e.client()
	e.enroll(member, "bob", "bob-password-123")

	// 管理者以外は見ることも変えることもできない（403）
	if res, _ := e.get(member, "/im/admin/security"); res.StatusCode != http.StatusForbidden {
		t.Errorf("member の GET: %d, want 403", res.StatusCode)
	}
	_, acct := e.get(member, "/im/account")
	if res, _ := e.post(member, "/im/admin/security", url.Values{"csrf": {csrfOf(t, acct)}, "two_factor": {"optional"}, "password": {"bob-password-123"}}); res.StatusCode != http.StatusForbidden {
		t.Errorf("member の POST: %d, want 403", res.StatusCode)
	}
	if e.twoFactor() != store.TwoFactorRequired {
		t.Fatal("member が設定を変えた")
	}

	res, page := e.get(admin, "/im/admin/security")
	if res.StatusCode != 200 || !strings.Contains(page, `data-two-factor="required"`) || !strings.Contains(page, "確認コード") {
		t.Fatalf("管理画面: %d %s", res.StatusCode, page)
	}
	// 再認証が無い・誤りなら変更しない
	for _, form := range []url.Values{
		{"two_factor": {"optional"}},
		{"two_factor": {"optional"}, "password": {"alice-password-1"}},
		{"two_factor": {"optional"}, "password": {"wrong-password-1"}, "code": {e.code(secret)}},
		{"two_factor": {"optional"}, "password": {"alice-password-1"}, "code": {"000000"}},
	} {
		res, _ := e.formAt(admin, "/im/admin/security", "/im/admin/security", form)
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%v: %d, want 401", form, res.StatusCode)
		}
		if e.twoFactor() != store.TwoFactorRequired {
			t.Fatalf("%v で変更された", form)
		}
	}
	if res, _ := e.formAt(admin, "/im/admin/security", "/im/admin/security", url.Values{"two_factor": {"bogus"}}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("不正な値: %d", res.StatusCode)
	}
	e.clock.Add(30 * time.Second)
	res, page = e.formAt(admin, "/im/admin/security", "/im/admin/security", url.Values{"two_factor": {"optional"}, "password": {"alice-password-1"}, "code": {e.code(secret)}})
	if res.StatusCode != 200 || e.twoFactor() != store.TwoFactorOptional {
		t.Fatalf("必須 → 任意: %d %s（設定 %s）", res.StatusCode, page, e.twoFactor())
	}
	// 同じコードの再利用はできない（再認証も TOTP の使用済みステップを進める）
	var last int64
	e.db.QueryRow("SELECT totp_last_step FROM users WHERE login = 'alice'").Scan(&last)
	if last != auth.TOTPStep(e.clock.Now()) {
		t.Errorf("使用済みステップ = %d", last)
	}
	// 任意 → 必須は再認証なし
	res, page = e.formAt(admin, "/im/admin/security", "/im/admin/security", url.Values{"two_factor": {"required"}})
	if res.StatusCode != 200 || e.twoFactor() != store.TwoFactorRequired {
		t.Fatalf("任意 → 必須: %d（設定 %s）", res.StatusCode, e.twoFactor())
	}
	// 記録: 誰が・いつ・どの値からどの値へ。管理画面で見られる
	var n int
	e.db.QueryRow("SELECT COUNT(*) FROM setting_changes WHERE name = 'two_factor' AND via = 'web' AND actor_user_id = (SELECT id FROM users WHERE login = 'alice')").Scan(&n)
	if n != 2 {
		t.Errorf("管理画面の変更の記録 = %d 件, want 2", n)
	}
	_, page = e.get(admin, "/im/admin/security")
	if strings.Count(page, "data-setting-change") != 3 || !strings.Contains(page, "必須 → 任意") || !strings.Contains(page, "任意 → 必須") ||
		!strings.Contains(page, "alice") || !strings.Contains(page, "管理画面") || !strings.Contains(page, "管理コマンド") {
		t.Errorf("変更の記録の表示: %s", page)
	}
	// 利用者管理の画面から辿れる
	if _, users := e.get(admin, "/im/admin/users"); !strings.Contains(users, "/im/admin/security") {
		t.Error("利用者管理から二段階認証の設定へのリンクが無い")
	}
}

// 任意のとき TOTP 未登録の管理者が画面から必須にすると、自分のセッションも無効になる（必須のまま TOTP 無しでは使えない）。
func TestTwoFactorAdminSwitchOwnSession(t *testing.T) {
	e := newEnv(t)
	e.setTwoFactor(store.TwoFactorOptional)
	e.user("alice", "alice-password-1", "admin")
	c := e.client()
	e.loginPasswordOnly(c, "alice", "alice-password-1")
	e.setTwoFactor(store.TwoFactorRequired) // 管理コマンドで必須に → alice のセッションは無効
	if res, _ := e.get(c, "/im/admin/security"); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("必須にした後の TOTP 無しセッション: %d", res.StatusCode)
	}
	e.setTwoFactor(store.TwoFactorOptional)
	e.loginPasswordOnly(c, "alice", "alice-password-1")
	// 画面から必須にする（自分のセッションも無効になる）
	res, page := e.formAt(c, "/im/admin/security", "/im/admin/security", url.Values{"two_factor": {"required"}})
	if res.StatusCode != 200 || !strings.Contains(page, "あなたのセッションも対象です") {
		t.Fatalf("任意 → 必須: %d %s", res.StatusCode, page)
	}
	if res, _ := e.get(c, "/im/admin/security"); res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(res.Header.Get("Location"), "/im/login") {
		t.Errorf("必須にした本人の TOTP 無しセッション: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
}

// 条件 8: 任意 → 必須に切り替えたとき、TOTP を経ずに発行された既存のセッションは使えなくなる。
func TestTwoFactorSwitchToRequiredRevokesSessions(t *testing.T) {
	e := newEnv(t)
	e.setTwoFactor(store.TwoFactorRequired)
	alice, _ := e.adminWithTOTP("alice", "alice-password-1")
	e.user("bob", "bob-password-123", "member")
	e.setTwoFactor(store.TwoFactorOptional)
	bob := e.client()
	e.loginPasswordOnly(bob, "bob", "bob-password-123")
	if res, _ := e.get(bob, "/im/api/v1/me"); res.StatusCode != 200 {
		t.Fatalf("任意の間の bob: %d", res.StatusCode)
	}

	res := e.setTwoFactor(store.TwoFactorRequired)
	if res.RevokedSessions != 1 {
		t.Errorf("無効にしたセッション = %d, want 1（bob のだけ）", res.RevokedSessions)
	}
	if r, _ := e.get(bob, "/im/api/v1/me"); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("必須にした後の bob の API: %d, want 401", r.StatusCode)
	}
	if r, _ := e.get(bob, "/im/"); r.StatusCode != http.StatusSeeOther || !strings.HasPrefix(r.Header.Get("Location"), "/im/login") {
		t.Errorf("必須にした後の bob の画面: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	if r, _ := e.get(alice, "/im/api/v1/me"); r.StatusCode != 200 {
		t.Errorf("TOTP 済みの alice のセッション: %d, want 200（影響しない）", r.StatusCode)
	}
	// 再ログインで登録を求める
	if r, _ := e.login(bob, "bob", "bob-password-123"); r.Header.Get("Location") != "/im/login/totp/setup" {
		t.Errorf("再ログイン: %s", r.Header.Get("Location"))
	}

	// 切替の破棄をすり抜けたセッション（設定を直接書き換えた場合）も、リクエストの時点で拒否する
	e.setTwoFactor(store.TwoFactorOptional)
	carol := e.client()
	e.user("carol", "carol-password-1", "member")
	e.loginPasswordOnly(carol, "carol", "carol-password-1")
	if _, err := e.db.Exec("UPDATE system_settings SET value = 'required' WHERE name = 'two_factor'"); err != nil {
		t.Fatal(err)
	}
	if r, _ := e.get(carol, "/im/api/v1/me"); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("直接必須にした後の carol: %d, want 401", r.StatusCode)
	}
}

// 条件 10: 設定の変更で登録済みの TOTP シークレットは消えない。
func TestTwoFactorSwitchKeepsSecrets(t *testing.T) {
	e := newEnv(t)
	e.setTwoFactor(store.TwoFactorRequired)
	admin, secret := e.adminWithTOTP("alice", "alice-password-1")
	_, before := e.totpState("alice")
	e.clock.Add(30 * time.Second)
	e.formAt(admin, "/im/admin/security", "/im/admin/security", url.Values{"two_factor": {"optional"}, "password": {"alice-password-1"}, "code": {e.code(secret)}})
	e.formAt(admin, "/im/admin/security", "/im/admin/security", url.Values{"two_factor": {"required"}})
	e.setTwoFactor(store.TwoFactorOptional)
	e.setTwoFactor(store.TwoFactorRequired)
	if e.twoFactor() != store.TwoFactorRequired {
		t.Fatal("切り替わっていない")
	}
	on, after := e.totpState("alice")
	if !on || !bytes.Equal(before, after) {
		t.Errorf("シークレットが変わった: enabled=%v", on)
	}
	e.relogin(e.client(), "alice", "alice-password-1", secret)
}

// 条件 11: アクセストークン（PAT・OAuth）の認証は設定に影響されない。
func TestTwoFactorTokensUnaffected(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user("bob", "bob-password-123", "member") // TOTP 未登録
	pr := e.project("req")
	store.SetMember(ctx, e.db, pr.ID, u.ID, "viewer")
	pat, prefix, _ := auth.NewPAT()
	if _, err := store.CreateToken(ctx, e.db, u.ID, "pat", "t", prefix, auth.HashToken(pat), nil, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	e.setTwoFactor(store.TwoFactorOptional)
	oauthTok, _ := e.oauthFlowOptional(t, "bob", "bob-password-123")
	for _, v := range []string{store.TwoFactorRequired, store.TwoFactorOptional, store.TwoFactorRequired} {
		e.setTwoFactor(v)
		if got := e.meStatus(pat); got != 200 {
			t.Errorf("%s: PAT %d", v, got)
		}
		if got := e.meStatus(oauthTok); got != 200 {
			t.Errorf("%s: OAuth %d", v, got)
		}
		m := e.mcpAs(oauthTok, map[string]string{})
		if text, _ := m.call("list_projects", nil, false); !strings.Contains(text, "req") {
			t.Errorf("%s: MCP が応答しない", v)
		}
	}
}

// oauthFlowOptional は任意のときに、TOTP 未登録の利用者がパスワードだけで認可してトークンを得る。
func (e *env) oauthFlowOptional(t *testing.T, login, password string) (string, string) {
	t.Helper()
	_, reg := e.postJSON("/im/oauth/register", map[string]any{"client_name": "c", "redirect_uris": []string{"http://localhost:51000/callback"}})
	clientID := reg["client_id"].(string)
	verifier := "verifier-" + strings.Repeat("c", 40)
	q := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {"http://localhost:51000/callback"},
		"code_challenge": {challengeOf(verifier)}, "code_challenge_method": {"S256"}, "state": {"s"},
		"scope": {"im"}, "resource": {e.srv.URL + "/im/mcp"}}
	c := e.client()
	e.loginPasswordOnly(c, login, password)
	_, page := e.get(c, "/im/oauth/authorize?"+q.Encode())
	res, _ := e.postForm(c, "/im/oauth/authorize", url.Values{"csrf": {csrfOf(t, page)}, "query": {q.Encode()}, "approve": {"1"}})
	loc, _ := url.Parse(res.Header.Get("Location"))
	res2, tok := e.postJSON2("/im/oauth/token", url.Values{"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")},
		"redirect_uri": {"http://localhost:51000/callback"}, "client_id": {clientID}, "code_verifier": {verifier},
		"resource": {e.srv.URL + "/im/mcp"}})
	if res2.StatusCode != 200 || tok["access_token"] == nil {
		t.Fatalf("トークン: %d %v", res2.StatusCode, tok)
	}
	return tok["access_token"].(string), clientID
}

// 条件 12 の画面側: 設定が無い（未設定の）ときは必須として動く。
func TestTwoFactorUnsetBehavesRequired(t *testing.T) {
	e := newEnv(t)
	if _, set, _ := store.TwoFactorPolicy(context.Background(), e.db); set {
		t.Fatal("新しい DB で設定がある")
	}
	e.user("bob", "bob-password-123", "member")
	if res, _ := e.login(e.client(), "bob", "bob-password-123"); res.Header.Get("Location") != "/im/login/totp/setup" {
		t.Errorf("未設定のログイン: %s, want 登録画面", res.Header.Get("Location"))
	}
	var n int
	e.db.QueryRow("SELECT COUNT(*) FROM setting_changes").Scan(&n)
	if n != 0 {
		t.Errorf("記録 = %d", n)
	}
}
