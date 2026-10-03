package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/auth"
	"github.com/howashoji/looptrack/internal/store"
)

// ログイン画面の「ログインしたままにする」（remember=1）。付けたセッションは無操作では切れず、
// 最終アクセスから 400 日の期限を Cookie と DB の両方で延ばし続ける。付けないセッションは従来どおり。

// loginRemember はパスワード段階までを行う。remember が true ならチェックボックスを付けて送る。
func (e *env) loginRemember(c *http.Client, login, password string, remember bool) *http.Response {
	e.t.Helper()
	_, page := e.get(c, "/im/login")
	form := url.Values{"csrf": {csrfOf(e.t, page)}, "login": {login}, "password": {password}}
	if remember {
		form.Set("remember", "1")
	}
	res, _ := e.post(c, "/im/login", form)
	return res
}

// sessionCookieOf は応答の Set-Cookie からセッションの Cookie を取り出す（無ければ nil）。
func sessionCookieOf(res *http.Response) *http.Cookie {
	for _, c := range res.Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			return c
		}
	}
	return nil
}

// sessionRow はセッションの Cookie の値に対応する DB の行を引く。
func (e *env) sessionRow(value string) (persistent bool, expires time.Time) {
	e.t.Helper()
	if err := e.db.QueryRow("SELECT persistent, expires_at FROM web_sessions WHERE id_hash = ?", auth.HashToken(value)).Scan(&persistent, &expires); err != nil {
		e.t.Fatalf("セッションの行が引けない: %v", err)
	}
	return persistent, expires
}

func near(got, want time.Time) bool {
	d := got.Sub(want)
	return d > -2*time.Minute && d < 2*time.Minute
}

func TestRememberLoginCookieAndFlag(t *testing.T) {
	e := newEnv(t)
	e.user("bob", "bob-password-123", "member")
	e.setTwoFactor(store.TwoFactorOptional) // パスワードだけでログインを終える

	for _, tc := range []struct {
		name     string
		remember bool
		life     time.Duration
	}{
		{"remember=1", true, persistentLifetime},
		{"チェックなし（対照）", false, sessionLifetime},
	} {
		c := e.client()
		res := e.loginRemember(c, "bob", "bob-password-123", tc.remember)
		if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/im/" {
			t.Fatalf("%s: ログイン %d %s", tc.name, res.StatusCode, res.Header.Get("Location"))
		}
		ck := sessionCookieOf(res)
		if ck == nil {
			t.Fatalf("%s: セッションの Cookie が出ていない", tc.name)
		}
		want := e.clock.Now().Add(tc.life)
		if !near(ck.Expires, want) {
			t.Errorf("%s: Cookie の Expires = %v, want 約 %v", tc.name, ck.Expires, want)
		}
		persistent, expires := e.sessionRow(ck.Value)
		if persistent != tc.remember {
			t.Errorf("%s: persistent = %v, want %v", tc.name, persistent, tc.remember)
		}
		if !near(expires, want) {
			t.Errorf("%s: expires_at = %v, want 約 %v", tc.name, expires, want)
		}
	}
}

// 無操作 12 時間を超えても persistent は使え、そのとき（最終アクセスの更新と同じ時機に）期限が延びる。
// persistent でないセッションは同じ経過で破棄される（対照）。
func TestRememberLoginSurvivesIdleAndExtends(t *testing.T) {
	e := newEnv(t)
	e.user("bob", "bob-password-123", "member")
	e.setTwoFactor(store.TwoFactorOptional)
	keep, plain := e.client(), e.client()
	keepCookie := sessionCookieOf(e.loginRemember(keep, "bob", "bob-password-123", true))
	e.loginRemember(plain, "bob", "bob-password-123", false)
	if keepCookie == nil {
		t.Fatal("persistent のセッションの Cookie が出ていない")
	}
	_, firstExpires := e.sessionRow(keepCookie.Value)

	// 5 分以内の要求では最終アクセスを更新しないので、Cookie も出し直さない
	if res, _ := e.get(keep, "/im/"); res.StatusCode != 200 || sessionCookieOf(res) != nil {
		t.Errorf("直後の要求: %d, Set-Cookie = %v（出し直さないはず）", res.StatusCode, sessionCookieOf(res))
	}

	e.clock.Add(sessionIdleTimeout + time.Minute)
	res, _ := e.get(keep, "/im/")
	if res.StatusCode != 200 {
		t.Fatalf("persistent の無操作後: %d %s, want 200", res.StatusCode, res.Header.Get("Location"))
	}
	want := e.clock.Now().Add(persistentLifetime)
	ck := sessionCookieOf(res)
	if ck == nil || ck.Value != keepCookie.Value || !near(ck.Expires, want) {
		t.Errorf("出し直した Cookie = %v, want 同じ値で Expires 約 %v", ck, want)
	}
	if _, expires := e.sessionRow(keepCookie.Value); !near(expires, want) || !expires.After(firstExpires) {
		t.Errorf("expires_at = %v, want 約 %v（発行時の %v より後）", expires, want, firstExpires)
	}
	// API の入口でも同じく延ばす
	e.clock.Add(10 * time.Minute)
	res, _ = e.get(keep, "/im/api/v1/me")
	if ck := sessionCookieOf(res); res.StatusCode != 200 || ck == nil || !near(ck.Expires, e.clock.Now().Add(persistentLifetime)) {
		t.Errorf("API での延長: %d, Cookie = %v", res.StatusCode, ck)
	}

	if res, _ := e.get(plain, "/im/"); res.StatusCode != http.StatusSeeOther {
		t.Errorf("persistent でないセッションの無操作後: %d, want ログイン画面へ", res.StatusCode)
	}

	// 最終アクセスから 400 日を過ぎれば persistent でも使えない
	e.clock.Add(persistentLifetime + time.Minute)
	if res, _ := e.get(keep, "/im/"); res.StatusCode != http.StatusSeeOther {
		t.Errorf("400 日の無アクセス後: %d, want ログイン画面へ", res.StatusCode)
	}
}

// TOTP を求める段では仮のセッション（10 分）にフラグを持たせ、段階を終えたセッションへ引き継ぐ。
func TestRememberLoginThroughTOTP(t *testing.T) {
	e := newEnv(t) // 二段階認証は必須（既定）
	e.user("carol", "carol-password-1", "member")

	// 登録（TOTP 未登録 → 登録画面）を経る
	c := e.client()
	res := e.loginRemember(c, "carol", "carol-password-1", true)
	if res.Header.Get("Location") != "/im/login/totp/setup" {
		t.Fatalf("パスワード後の遷移先 = %q", res.Header.Get("Location"))
	}
	pend := sessionCookieOf(res)
	if pend == nil || !near(pend.Expires, e.clock.Now().Add(pendingLifetime)) {
		t.Errorf("仮のセッションの Cookie = %v, want Expires 約 10 分後", pend)
	}
	if persistent, expires := e.sessionRow(pend.Value); !persistent || !near(expires, e.clock.Now().Add(pendingLifetime)) {
		t.Errorf("仮のセッション: persistent = %v, expires_at = %v, want TRUE・約 10 分後", persistent, expires)
	}
	_, page := e.get(c, "/im/login/totp/setup")
	secret := secretFromPage(t, page)
	res, _ = e.post(c, "/im/login/totp/setup", url.Values{"csrf": {csrfOf(t, page)}, "code": {e.code(secret)}})
	final := sessionCookieOf(res)
	if res.StatusCode != http.StatusSeeOther || final == nil {
		t.Fatalf("登録後: %d %v", res.StatusCode, final)
	}
	if persistent, _ := e.sessionRow(final.Value); !persistent || !near(final.Expires, e.clock.Now().Add(persistentLifetime)) {
		t.Errorf("登録を経たセッション: persistent = %v, Expires = %v, want TRUE・約 400 日後", persistent, final.Expires)
	}

	// パスワードの変更（この画面のセッションだけ作り直す）でも引き継ぐ
	res, _ = e.formAt(c, "/im/account", "/im/account/password", url.Values{"current": {"carol-password-1"}, "new": {"carol-password-2"}, "confirm": {"carol-password-2"}})
	renewed := sessionCookieOf(res)
	if res.StatusCode != http.StatusSeeOther || renewed == nil {
		t.Fatalf("パスワードの変更: %d %v", res.StatusCode, renewed)
	}
	if persistent, _ := e.sessionRow(renewed.Value); !persistent || !near(renewed.Expires, e.clock.Now().Add(persistentLifetime)) {
		t.Errorf("作り直したセッション: persistent = %v, Expires = %v, want TRUE・約 400 日後", persistent, renewed.Expires)
	}

	// 登録済みの TOTP の入力を経る（付けた場合と、付けない場合の対照）
	for _, remember := range []bool{true, false} {
		e.clock.Add(time.Minute) // 使用済みのステップを避ける
		c := e.client()
		res := e.loginRemember(c, "carol", "carol-password-2", remember)
		if res.Header.Get("Location") != "/im/login/totp" {
			t.Fatalf("remember=%v: パスワード後の遷移先 = %q", remember, res.Header.Get("Location"))
		}
		_, page := e.get(c, "/im/login/totp")
		res, _ = e.post(c, "/im/login/totp", url.Values{"csrf": {csrfOf(t, page)}, "code": {e.code(secret)}})
		ck := sessionCookieOf(res)
		if res.StatusCode != http.StatusSeeOther || ck == nil {
			t.Fatalf("remember=%v: TOTP の後 %d %v", remember, res.StatusCode, ck)
		}
		life := sessionLifetime
		if remember {
			life = persistentLifetime
		}
		if persistent, _ := e.sessionRow(ck.Value); persistent != remember || !near(ck.Expires, e.clock.Now().Add(life)) {
			t.Errorf("remember=%v: persistent = %v, Expires = %v", remember, persistent, ck.Expires)
		}
	}
}

// housekeeping は無操作 12 時間を超えた persistent を残し、persistent でないものと期限切れの persistent を消す。
func TestHousekeepingKeepsPersistentSessions(t *testing.T) {
	e := newEnv(t)
	u := e.user("dave", "dave-password-1", "member")
	ctx := context.Background()
	now := time.Now().UTC() // housekeeping は DB の現在時刻（CURRENT_TIMESTAMP）で比べる
	rows := []struct {
		name       string
		persistent bool
		lastSeen   time.Time
		expires    time.Time
		keep       bool
	}{
		{"persistent・無操作 13 時間", true, now.Add(-13 * time.Hour), now.Add(300 * 24 * time.Hour), true},
		{"persistent でない・無操作 13 時間", false, now.Add(-13 * time.Hour), now.Add(6 * 24 * time.Hour), false},
		{"persistent・期限切れ", true, now.Add(-2 * time.Hour), now.Add(-time.Hour), false},
		{"persistent でない・最近のアクセス", false, now.Add(-time.Hour), now.Add(6 * 24 * time.Hour), true},
	}
	for i, r := range rows {
		hash := auth.HashToken(strings.Repeat(string(rune('a'+i)), 8))
		if err := store.CreateSession(ctx, e.db, store.Session{IDHash: hash, UserID: u.ID, CSRFToken: strings.Repeat("c", 64),
			MFAPassed: true, Persistent: r.persistent, ExpiresAt: r.expires}, "", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := e.db.Exec("UPDATE web_sessions SET last_seen_at = ? WHERE id_hash = ?", r.lastSeen, hash); err != nil {
			t.Fatal(err)
		}
	}
	housekeepOnce(ctx, e.app, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for i, r := range rows {
		var n int
		if err := e.db.QueryRow("SELECT COUNT(*) FROM web_sessions WHERE id_hash = ?", auth.HashToken(strings.Repeat(string(rune('a'+i)), 8))).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if (n == 1) != r.keep {
			t.Errorf("%s: 残った = %v, want %v", r.name, n == 1, r.keep)
		}
	}
}

// ログイン画面にチェックボックスが日英のラベルで出る（既定はチェックなし）。
func TestLoginPageRememberCheckbox(t *testing.T) {
	e := newEnv(t)
	for _, tc := range []struct {
		lang, label string
		c           *http.Client
	}{
		{"ja", "ログインしたままにする", e.client()},       // Accept-Language: ja
		{"en", "Keep me signed in", enClient()}, // Accept-Language なし＝英語
	} {
		_, page := e.get(tc.c, "/im/login")
		if !strings.Contains(page, `<input type="checkbox" name="remember" value="1">`+tc.label+`</label>`) {
			t.Errorf("%s: チェックボックスとラベルが出ていない:\n%s", tc.lang, page)
		}
		if strings.Contains(page, "checked") {
			t.Errorf("%s: 既定でチェックが入っている", tc.lang)
		}
	}
}

// TOTP を待つ persistent の仮セッションは、5 分を超えてアクセスしても期限を延ばさない（10 分のまま・Cookie も出し直さない）。
func TestRememberPendingSessionNotExtended(t *testing.T) {
	e := newEnv(t) // 二段階認証は必須（既定）
	e.user("erin", "erin-password-1", "member")
	c := e.client()
	res := e.loginRemember(c, "erin", "erin-password-1", true)
	pend := sessionCookieOf(res)
	if res.Header.Get("Location") != "/im/login/totp/setup" || pend == nil {
		t.Fatalf("前提が崩れています: 仮のセッションができていない（%s・%v）", res.Header.Get("Location"), pend)
	}
	persistent, firstExpires := e.sessionRow(pend.Value)
	if !persistent {
		t.Fatal("前提が崩れています: 仮のセッションが persistent を持っていない")
	}

	e.clock.Add(6 * time.Minute) // 最終アクセスの更新（5 分おき）の時機を越える
	for _, path := range []string{"/im/login", "/im/login/totp/setup"} {
		res, _ := e.get(c, path)
		if ck := sessionCookieOf(res); ck != nil {
			t.Errorf("GET %s: 仮のセッションの Cookie を出し直した: %v", path, ck)
		}
	}
	if _, expires := e.sessionRow(pend.Value); !expires.Equal(firstExpires) {
		t.Errorf("仮のセッションの expires_at = %v, want 発行時の %v のまま", expires, firstExpires)
	}

	e.clock.Add(5 * time.Minute) // 発行から 11 分: 仮のセッションの期限切れ
	if res, _ := e.get(c, "/im/login/totp/setup"); res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(res.Header.Get("Location"), "/im/login") {
		t.Errorf("10 分を過ぎた仮のセッション: %d %s, want ログイン画面へ", res.StatusCode, res.Header.Get("Location"))
	}
}

// アカウント画面の TOTP の登録と解除（その画面のセッションだけ作り直す）でも persistent を引き継ぐ。
func TestRememberKeptThroughAccountTOTP(t *testing.T) {
	e := newEnv(t)
	e.user("frank", "frank-password-1", "member")
	e.setTwoFactor(store.TwoFactorOptional)
	c := e.client()
	if res := e.loginRemember(c, "frank", "frank-password-1", true); res.Header.Get("Location") != "/im/" {
		t.Fatalf("ログイン: %s", res.Header.Get("Location"))
	}
	check := func(step string, res *http.Response) {
		t.Helper()
		ck := sessionCookieOf(res)
		if res.StatusCode != http.StatusSeeOther || ck == nil {
			t.Fatalf("%s: %d %v", step, res.StatusCode, ck)
		}
		if persistent, _ := e.sessionRow(ck.Value); !persistent || !near(ck.Expires, e.clock.Now().Add(persistentLifetime)) {
			t.Errorf("%s の後: persistent = %v, Expires = %v, want TRUE・約 400 日後", step, persistent, ck.Expires)
		}
	}
	_, page := e.get(c, "/im/account/totp")
	secret := secretFromPage(t, page)
	res, _ := e.post(c, "/im/account/totp", url.Values{"csrf": {csrfOf(t, page)}, "code": {e.code(secret)}})
	check("TOTP の登録", res)

	e.clock.Add(time.Minute) // 使用済みのステップを避ける
	res, _ = e.formAt(c, "/im/account", "/im/account/totp/disable", url.Values{"password": {"frank-password-1"}, "code": {e.code(secret)}})
	check("TOTP の解除", res)
}

// ExtendSession は行が無い（読んだ後にログアウト・破棄で消えた）とき ErrNotFound を返す。
// sessionRefresh はこれを見て、消えた ID の Cookie を出し直さずに未ログインとして扱う。
func TestExtendSessionMissingRow(t *testing.T) {
	e := newEnv(t)
	e.user("gina", "gina-password-1", "member")
	e.setTwoFactor(store.TwoFactorOptional)
	ck := sessionCookieOf(e.loginRemember(e.client(), "gina", "gina-password-1", true))
	if ck == nil {
		t.Fatal("前提が崩れています: セッションの Cookie が出ていない")
	}
	ctx := context.Background()
	hash := auth.HashToken(ck.Value)
	now := e.clock.Now()
	if err := store.ExtendSession(ctx, e.app, hash, now, now.Add(persistentLifetime)); err != nil {
		t.Fatalf("行があるときの延長（対照）: %v", err)
	}
	if err := store.DeleteSession(ctx, e.app, hash); err != nil {
		t.Fatal(err)
	}
	if err := store.ExtendSession(ctx, e.app, hash, now, now.Add(persistentLifetime)); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("消えたセッションの延長: %v, want ErrNotFound", err)
	}
}
