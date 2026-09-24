package server

import (
	"context"
	"database/sql"
	"encoding/base32"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/auth"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/testutil"
)

// R3（読み取り API）の受け入れ条件を HTTP 経由で検査する。

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

type env struct {
	t     *testing.T
	db    *sql.DB // 管理者の資格情報（テストの準備・検証用）
	app   *sql.DB // 本番と同じ権限のアプリ用ユーザー（権限の検査用）
	srv   *httptest.Server
	clock *clock
	box   *auth.Box
	s     *Server // 設定（配布ディレクトリなど）をテストで変える
}

// newEnv は通常モードのサーバ。管理者 0 人の「セットアップ未完了」は外す（AllowNoAdmin。既存のテストは管理者を作らないものが多い）。
func newEnv(t *testing.T) *env {
	t.Helper()
	return newEnvWith(t, func(cfg *Config) { cfg.AllowNoAdmin = true })
}

// newEnvWith は設定を変えたサーバ（ローカルモード・セットアップ未完了の検査）。
func newEnvWith(t *testing.T, configure func(*Config)) *env {
	t.Helper()
	db, app := testutil.AppDB(t)
	key, _ := auth.NewSecretKey()
	box, _ := auth.NewBox(key)
	c := &clock{t: time.Now().UTC()}
	cfg := Config{BasePath: "/im", Box: box, Now: c.Now, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	configure(&cfg)
	s, err := New(cfg, app) // サーバは本番と同じ権限のユーザーで動かす
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return &env{t: t, db: db, app: app, srv: srv, clock: c, box: box, s: s}
}

// client は画面をたどるクライアント。**日本語の利用者として要求する**（Accept-Language: ja）。
// サーバの既定は英語なので、これを付けないと画面の文面が英語になり、日本語で書いた検査が当たらない。
// 英語で出ることは TestWebInEnglish が別に確かめる。
func (e *env) client() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, Transport: headerTransport{map[string]string{"Accept-Language": "ja"}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (e *env) user(login, password, role string) store.User {
	e.t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := store.CreateUser(context.Background(), e.db, login, login, hash, role); err != nil {
		e.t.Fatal(err)
	}
	u, _ := store.UserByLogin(context.Background(), e.db, login)
	return u
}

func (e *env) project(slug string) store.Project {
	e.t.Helper()
	id, err := store.UpsertProject(context.Background(), e.db, store.Project{Slug: slug, Prefix: strings.ToUpper(slug), Width: 4, Name: slug})
	if err != nil {
		e.t.Fatal(err)
	}
	return store.Project{ID: id, Slug: slug}
}

func (e *env) get(c *http.Client, path string, header ...string) (*http.Response, string) {
	e.t.Helper()
	req, _ := http.NewRequest("GET", e.srv.URL+path, nil)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	res, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

func (e *env) post(c *http.Client, path string, form url.Values) (*http.Response, string) {
	e.t.Helper()
	res, err := c.PostForm(e.srv.URL+path, form)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

var csrfRe = regexp.MustCompile(`name="csrf" value="([^"]+)"`)
var secretRe = regexp.MustCompile(`data-secret>([A-Z2-7]+)<`)

func csrfOf(t *testing.T, html string) string {
	m := csrfRe.FindStringSubmatch(html)
	if m == nil {
		t.Fatalf("CSRF トークンが見つからない: %s", html)
	}
	return m[1]
}

// login はパスワード段階までを行い、応答を返す。
func (e *env) login(c *http.Client, login, password string) (*http.Response, string) {
	_, page := e.get(c, "/im/login")
	return e.post(c, "/im/login", url.Values{"csrf": {csrfOf(e.t, page)}, "login": {login}, "password": {password}})
}

func (e *env) code(secret []byte) string {
	return auth.HOTP(secret, uint64(auth.TOTPStep(e.clock.Now())), auth.TOTPDigits)
}

// enroll は TOTP 未登録の利用者を登録まで進め、シークレットを返す。
func (e *env) enroll(c *http.Client, login, password string) []byte {
	e.t.Helper()
	res, _ := e.login(c, login, password)
	if loc := res.Header.Get("Location"); loc != "/im/login/totp/setup" {
		e.t.Fatalf("パスワード後の遷移先 = %q, want 登録画面", loc)
	}
	_, page := e.get(c, "/im/login/totp/setup")
	m := secretRe.FindStringSubmatch(page)
	if m == nil {
		e.t.Fatalf("シークレットが表示されない: %s", page)
	}
	secret, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(m[1])
	res, _ = e.post(c, "/im/login/totp/setup", url.Values{"csrf": {csrfOf(e.t, page)}, "code": {e.code(secret)}})
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/im/" {
		e.t.Fatalf("登録後: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	return secret
}

func TestUnauthenticated(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	for _, p := range []string{"/im/", "/im/p/web/", "/im/anything"} {
		res, _ := e.get(c, p)
		if res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(res.Header.Get("Location"), "/im/login") {
			t.Errorf("GET %s: %d %s, want ログイン画面へ", p, res.StatusCode, res.Header.Get("Location"))
		}
	}
	for _, p := range []string{"/im/api/v1/me", "/im/api/v1/projects", "/im/api/v1/nope"} {
		res, body := e.get(c, p)
		if res.StatusCode != http.StatusUnauthorized || !strings.Contains(body, `"unauthorized"`) {
			t.Errorf("GET %s: %d %s, want 401", p, res.StatusCode, body)
		}
	}
	if res, _ := e.get(c, "/im/healthz"); res.StatusCode != 200 {
		t.Errorf("healthz: %d", res.StatusCode)
	}
	res, _ := e.get(c, "/im/login")
	for _, h := range []string{"Content-Security-Policy", "X-Frame-Options", "X-Content-Type-Options"} {
		if res.Header.Get(h) == "" {
			t.Errorf("ヘッダ %s が無い", h)
		}
	}
}

func TestTOTPRequiredAndReplay(t *testing.T) {
	e := newEnv(t)
	e.user("alice", "alice-password-1", "admin")
	c := e.client()

	// パスワードだけでは API も画面も使えない
	res, _ := e.login(c, "alice", "alice-password-1")
	if res.Header.Get("Location") != "/im/login/totp/setup" {
		t.Fatalf("Location = %s", res.Header.Get("Location"))
	}
	if res, _ := e.get(c, "/im/api/v1/me"); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("TOTP 前の API: %d, want 401", res.StatusCode)
	}
	if res, _ := e.get(c, "/im/"); res.Header.Get("Location") != "/im/login/totp/setup" {
		t.Errorf("TOTP 前の画面: %s", res.Header.Get("Location"))
	}
	for _, ck := range res.Cookies() {
		if ck.Name == sessionCookie && (ck.Path != "/im" || !ck.HttpOnly || ck.SameSite != http.SameSiteLaxMode) {
			t.Errorf("セッション Cookie の属性: %+v", ck)
		}
	}

	// 登録すると使える
	c = e.client()
	secret := e.enroll(c, "alice", "alice-password-1")
	if res, body := e.get(c, "/im/api/v1/me"); res.StatusCode != 200 || !strings.Contains(body, `"alice"`) {
		t.Fatalf("登録後の API: %d %s", res.StatusCode, body)
	}

	// 次回ログインは TOTP 入力。誤りは拒否、正しいコードは通る、同じコードの再利用は拒否
	e.clock.Add(auth.TOTPPeriod * time.Second)
	c2 := e.client()
	res, _ = e.login(c2, "alice", "alice-password-1")
	if res.Header.Get("Location") != "/im/login/totp" {
		t.Fatalf("Location = %s", res.Header.Get("Location"))
	}
	_, page := e.get(c2, "/im/login/totp")
	if res, _ := e.post(c2, "/im/login/totp", url.Values{"csrf": {csrfOf(t, page)}, "code": {"000000"}}); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("誤ったコード: %d", res.StatusCode)
	}
	code := e.code(secret)
	if res, _ := e.post(c2, "/im/login/totp", url.Values{"csrf": {csrfOf(t, page)}, "code": {code}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("正しいコード: %d", res.StatusCode)
	}
	c3 := e.client()
	e.login(c3, "alice", "alice-password-1")
	_, page = e.get(c3, "/im/login/totp")
	if res, _ := e.post(c3, "/im/login/totp", url.Values{"csrf": {csrfOf(t, page)}, "code": {code}}); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("使用済みコードの再利用: %d, want 401", res.StatusCode)
	}

	// ログアウトは CSRF 必須
	if res, _ := e.post(c2, "/im/logout", url.Values{}); res.StatusCode != http.StatusForbidden {
		t.Errorf("CSRF 無しのログアウト: %d, want 403", res.StatusCode)
	}
}

func TestLockout(t *testing.T) {
	e := newEnv(t)
	e.user("bob", "bob-password-12", "member")
	c := e.client()
	for i := 0; i < lockPerLogin; i++ {
		if res, body := e.login(c, "bob", "wrong-password"); res.StatusCode != http.StatusUnauthorized || !strings.Contains(body, msgBadCredentials(i18n.JA)) {
			t.Fatalf("%d 回目: %d", i+1, res.StatusCode)
		}
	}
	res, body := e.login(c, "bob", "bob-password-12")
	if res.StatusCode != http.StatusTooManyRequests || !strings.Contains(body, "一時的に制限") {
		t.Fatalf("連続失敗後の正しいパスワード: %d, want 429", res.StatusCode)
	}
	// 存在しない利用者も同じ文言（利用者の有無を漏らさない）
	if _, body := e.login(e.client(), "nobody", "whatever-password"); !strings.Contains(body, msgBadCredentials(i18n.JA)) {
		t.Error("存在しない利用者の文言が違う")
	}
}

func TestTokens(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user("carol", "carol-password-1", "member")
	c := e.client()
	bearer := func(tok string) (*http.Response, string) {
		return e.get(c, "/im/api/v1/me", "Authorization", "Bearer "+tok)
	}
	create := func(exp *time.Time) (string, int64) {
		tok, prefix, _ := auth.NewPAT()
		id, err := store.CreateToken(ctx, e.db, u.ID, "pat", "test", prefix, auth.HashToken(tok), exp, e.clock.Now())
		if err != nil {
			t.Fatal(err)
		}
		return tok, id
	}

	tok, id := create(nil)
	if res, body := bearer(tok); res.StatusCode != 200 || !strings.Contains(body, `"via":"api"`) {
		t.Fatalf("有効なトークン: %d %s", res.StatusCode, body)
	}
	var last sql.NullTime
	e.db.QueryRow("SELECT last_used_at FROM api_tokens WHERE id = ?", id).Scan(&last)
	if !last.Valid {
		t.Error("最終利用時刻が記録されていない")
	}
	if res, _ := bearer(tok + "x"); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("誤ったトークン: %d", res.StatusCode)
	}
	if err := store.RevokeToken(ctx, e.db, id); err != nil {
		t.Fatal(err)
	}
	if res, _ := bearer(tok); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("失効したトークン: %d", res.StatusCode)
	}
	past := e.clock.Now().Add(-time.Minute)
	expired, _ := create(&past)
	if res, _ := bearer(expired); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("期限切れのトークン: %d", res.StatusCode)
	}
	live, _ := create(nil)
	if err := store.SetDisabled(ctx, e.db, u.ID, true); err != nil {
		t.Fatal(err)
	}
	if res, _ := bearer(live); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("無効化された利用者のトークン: %d", res.StatusCode)
	}
}

func TestProjectMembership(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, b := e.project("aaa"), e.project("bbb")
	m := e.user("dave", "dave-password-1", "member")
	adm := e.user("erin", "erin-password-1", "admin")
	if err := store.SetMember(ctx, e.db, a.ID, m.ID, "viewer"); err != nil {
		t.Fatal(err)
	}
	_ = b
	token := func(u store.User) string {
		tok, prefix, _ := auth.NewPAT()
		store.CreateToken(ctx, e.db, u.ID, "pat", "t", prefix, auth.HashToken(tok), nil, e.clock.Now())
		return "Bearer " + tok
	}
	c := e.client()
	mt, at := token(m), token(adm)

	_, body := e.get(c, "/im/api/v1/projects", "Authorization", mt)
	var got struct {
		Projects []projectJSON `json:"projects"`
	}
	json.Unmarshal([]byte(body), &got)
	if len(got.Projects) != 1 || got.Projects[0].Slug != "aaa" || got.Projects[0].Role != "viewer" {
		t.Errorf("メンバーの一覧: %s", body)
	}
	if res, _ := e.get(c, "/im/api/v1/projects/aaa", "Authorization", mt); res.StatusCode != 200 {
		t.Errorf("権限のあるプロジェクト: %d", res.StatusCode)
	}
	if res, _ := e.get(c, "/im/api/v1/projects/bbb", "Authorization", mt); res.StatusCode != http.StatusNotFound {
		t.Errorf("権限の無いプロジェクト: %d, want 404", res.StatusCode)
	}
	if res, _ := e.get(c, "/im/api/v1/projects/zzz", "Authorization", mt); res.StatusCode != http.StatusNotFound {
		t.Errorf("存在しないプロジェクト: %d, want 404", res.StatusCode)
	}
	if res, _ := e.get(c, "/im/api/v1/projects/bbb", "Authorization", at); res.StatusCode != 200 {
		t.Errorf("admin: %d", res.StatusCode)
	}
}

func TestSafeNextAndIdle(t *testing.T) {
	e := newEnv(t)
	s := &Server{cfg: Config{BasePath: "/im"}}
	for in, want := range map[string]string{
		"/im/p/web/": "/im/p/web/", "https://evil.example/": "/im/", "//evil.example/im/": "/im/", "/im/\\evil": "/im/", "/other": "/im/",
	} {
		if got := s.safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}

	// 無操作 12 時間でセッションが切れる
	e.user("frank", "frank-password-1", "member")
	c := e.client()
	e.enroll(c, "frank", "frank-password-1")
	if res, _ := e.get(c, "/im/"); res.StatusCode != 200 {
		t.Fatalf("ログイン直後: %d", res.StatusCode)
	}
	e.clock.Add(sessionIdleTimeout + time.Minute)
	if res, _ := e.get(c, "/im/"); res.StatusCode != http.StatusSeeOther {
		t.Errorf("無操作後: %d, want ログイン画面へ", res.StatusCode)
	}
}
