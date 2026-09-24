package server

import (
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/auth"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/setupwiz"
	"github.com/howashoji/looptrack/internal/store"
)

// 画面版の初回設定（ローカルモードで有効な管理者が 0 人のとき）。

func firstRunForm(csrf string) url.Values {
	return url.Values{"csrf": {csrf}, "login": {"alice"}, "name": {"Alice"}, "password": {"correct-horse-battery"},
		"confirm": {"correct-horse-battery"}, "two_factor": {"optional"}, "project": {"main"}, "project_prefix": {""}, "project_name": {"メイン"}}
}

func countUsers(t *testing.T, e *env) int {
	t.Helper()
	n, err := store.CountUsers(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestFirstRunWeb(t *testing.T) {
	e := newLocalEnv(t)
	ctx := context.Background()
	c := e.client()
	res, _ := e.get(c, "/im/")
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/im/first-run" {
		t.Fatalf("未設定の /im/: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	res, page := e.get(c, "/im/first-run")
	if res.StatusCode != http.StatusOK || !strings.Contains(page, `name="two_factor"`) || !strings.Contains(page, `value="main"`) || strings.Contains(page, "<script") {
		t.Fatalf("初回設定の画面: %d\n%s", res.StatusCode, page)
	}
	var cookie *http.Cookie
	for _, ck := range res.Cookies() {
		if ck.Name == firstRunCookie {
			cookie = ck
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("CSRF の Cookie: %+v", cookie)
	}
	csrf := csrfOf(t, page)
	if csrf != cookie.Value {
		t.Errorf("フォームのトークンが Cookie と違う")
	}

	// CSRF: トークンなし・違うトークン・Cookie なし・別サイトからの送信は拒否（何も作らない）
	form := firstRunForm(csrf)
	form.Del("csrf")
	if res, _ := e.post(c, "/im/first-run", form); res.StatusCode != http.StatusForbidden {
		t.Errorf("トークンなし: %d", res.StatusCode)
	}
	if res, _ := e.post(c, "/im/first-run", firstRunForm(strings.Repeat("0", 64))); res.StatusCode != http.StatusForbidden {
		t.Errorf("違うトークン: %d", res.StatusCode)
	}
	if res, _ := e.post(e.client(), "/im/first-run", firstRunForm(csrf)); res.StatusCode != http.StatusForbidden {
		t.Errorf("Cookie なし: %d", res.StatusCode)
	}
	for _, h := range [][]string{{"Origin", "http://evil.example"}, {"Sec-Fetch-Site", "cross-site"}, {"Host", "evil.example"}} {
		res, _ := e.do(c, "POST", "/im/first-run", firstRunForm(csrf).Encode(), append([]string{"Content-Type", "application/x-www-form-urlencoded"}, h...)...)
		if res.StatusCode != http.StatusForbidden && res.StatusCode != http.StatusMisdirectedRequest {
			t.Errorf("%v: %d, want 403 / 421", h, res.StatusCode)
		}
	}
	// 入力の誤りは理由を示して入力を戻す（パスワードは戻さない）
	bad := firstRunForm(csrf)
	bad.Set("confirm", "different-password")
	if res, body := e.post(c, "/im/first-run", bad); res.StatusCode != http.StatusBadRequest || !strings.Contains(body, "一致しません") ||
		!strings.Contains(body, `value="Alice"`) || strings.Contains(body, "correct-horse-battery") {
		t.Errorf("確認の不一致: %d\n%s", res.StatusCode, body)
	}
	bad = firstRunForm(csrf)
	bad.Del("two_factor")
	if res, body := e.post(c, "/im/first-run", bad); res.StatusCode != http.StatusBadRequest || !strings.Contains(body, "二段階認証") {
		t.Errorf("二段階認証の未選択: %d", res.StatusCode)
	}
	if n := countUsers(t, e); n != 0 {
		t.Fatalf("拒否した送信で利用者ができた: %d", n)
	}

	// 答えるとそのままイシューの画面に入れる
	res, body := e.post(c, "/im/first-run", firstRunForm(csrf))
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/im/first-run/done" {
		t.Fatalf("送信: %d %s\n%s", res.StatusCode, res.Header.Get("Location"), body)
	}
	res, done := e.get(c, "/im/first-run/done")
	if res.StatusCode != http.StatusOK || !strings.Contains(done, `href="/im/p/main/"`) {
		t.Fatalf("最後の画面: %d\n%s", res.StatusCode, done)
	}
	for _, m := range setupwiz.MCPConfigs(i18n.JA, e.srv.URL+"/im", "main") {
		if !strings.Contains(html.UnescapeString(done), m.Text) {
			t.Errorf("最後の画面に %s の接続設定が無い: %q", m.Client, m.Text)
		}
	}
	if res, _ := e.get(c, "/im/p/main/"); res.StatusCode != http.StatusOK {
		t.Errorf("ボード: %d", res.StatusCode)
	}
	_, account := e.get(c, "/im/account")
	if code, body := e.webCreateIssue(c, "main", csrfOf(t, account)); code != http.StatusCreated || !strings.Contains(body, "MAIN-0001") {
		t.Errorf("起票: %d %s", code, body)
	}

	// 設定の中身（looptrack setup と同じ Provision）
	u, err := store.UserByLogin(ctx, e.db, "alice")
	if err != nil || u.Role != "admin" || u.DisplayName != "Alice" {
		t.Fatalf("管理者 = %+v %v", u, err)
	}
	if ok, _ := auth.VerifyPassword(u.PasswordHash, "correct-horse-battery"); !ok {
		t.Error("パスワードが合わない")
	}
	if policy, set, _ := store.TwoFactorPolicy(ctx, e.db); !set || policy != "optional" {
		t.Errorf("二段階認証 = %s %v", policy, set)
	}
	if cs, _ := store.SettingChanges(ctx, e.db, store.SettingTwoFactor, 5); len(cs) != 1 || cs[0].Via != "web" || cs[0].ActorLogin != "alice" {
		t.Errorf("記録 = %+v", cs)
	}
	pr, _ := store.ProjectBySlug(ctx, e.db, "main")
	if pr.Prefix != "MAIN" || pr.Name != "メイン" {
		t.Errorf("プロジェクト = %+v", pr)
	}
	if role, _ := store.MemberRole(ctx, e.db, pr.ID, u.ID); role != "admin" {
		t.Errorf("参加 = %q", role)
	}

	// 済んだら二度と出さない（再送信も受け付けない）
	if res, _ := e.get(c, "/im/first-run"); res.StatusCode != http.StatusNotFound {
		t.Errorf("済んだ後の初回設定: %d", res.StatusCode)
	}
	form = firstRunForm(csrf)
	form.Set("login", "mallory")
	e.post(c, "/im/first-run", form)
	if n := countUsers(t, e); n != 1 {
		t.Errorf("済んだ後の送信で利用者が増えた: %d", n)
	}
}

// 127.0.0.1 以外からの初回設定の要求は拒否する（画面の表示・送信・他の画面の転送とも）。
// 画面版の初回設定が残す二段階認証の変更記録の備考（setting_changes.note）は、設定した人の言語で書く。
// 備考は書き込んだときの文面で DB に残るので、英語の利用者の管理画面（/admin/security）に日本語が残らないことを見る。
// 日本語の利用者は従来どおり日本語の備考になる（同じテストで確かめる）。
func TestFirstRunRecordFollowsLang(t *testing.T) {
	for _, tc := range []struct {
		lang        string
		client      func(e *env) *http.Client
		note, other string
	}{
		{"en", func(*env) *http.Client { return enClient() }, "Set when the first user alice was created (the first-time setup page)", "初回設定"},
		{"ja", func(e *env) *http.Client { return e.client() }, "最初の利用者 alice の作成時に指定（初回設定の画面）", "Set when"},
	} {
		e := newLocalEnv(t)
		c := tc.client(e)
		_, page := e.get(c, "/im/first-run")
		if res, body := e.post(c, "/im/first-run", firstRunForm(csrfOf(t, page))); res.StatusCode != http.StatusSeeOther {
			t.Fatalf("%s: 送信 %d\n%s", tc.lang, res.StatusCode, body)
		}
		cs, err := store.SettingChanges(context.Background(), e.db, store.SettingTwoFactor, 5)
		if err != nil || len(cs) != 1 {
			t.Fatalf("%s: 記録 = %+v %v", tc.lang, cs, err)
		}
		if cs[0].Note != tc.note {
			t.Errorf("%s: 記録の備考 = %q, want %q", tc.lang, cs[0].Note, tc.note)
		}
		res, sec := e.get(c, "/im/admin/security")
		if res.StatusCode != http.StatusOK || !strings.Contains(sec, tc.note) || strings.Contains(sec, tc.other) {
			t.Errorf("%s: 管理画面の記録の行が %q でない（%d）:\n%s", tc.lang, tc.note, res.StatusCode, sec)
		}
		if tc.lang == "en" && jaRe.MatchString(sec) {
			t.Errorf("en: 管理画面に日本語が残る: %q", jaRe.FindAllString(sec, 20))
		}
	}
}

func TestFirstRunRejectsRemote(t *testing.T) {
	e := newLocalEnv(t)
	for _, remote := range []string{"192.0.2.10:5555", "[2001:db8::1]:5555", "10.0.0.2:1"} {
		for _, tc := range []struct{ method, path, body string }{
			{"GET", "/im/first-run", ""},
			{"GET", "/im/", ""},
			{"POST", "/im/first-run", firstRunForm("x").Encode()},
		} {
			req := httptest.NewRequest(tc.method, "http://127.0.0.1:8090"+tc.path, strings.NewReader(tc.body))
			req.RemoteAddr = remote
			if tc.body != "" {
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				req.AddCookie(&http.Cookie{Name: firstRunCookie, Value: "x"})
			}
			w := httptest.NewRecorder()
			e.s.ServeHTTP(w, req)
			if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), `name="password"`) {
				t.Errorf("%s %s %s: %d, want 403", remote, tc.method, tc.path, w.Code)
			}
		}
	}
	// loopback（127.0.0.1・::1）からは出す
	for _, remote := range []string{"127.0.0.1:5555", "[::1]:5555"} {
		req := httptest.NewRequest("GET", "http://localhost:8090/im/first-run", nil)
		req.RemoteAddr = remote
		w := httptest.NewRecorder()
		e.s.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("%s: %d, want 200", remote, w.Code)
		}
	}
	if n := countUsers(t, e); n != 0 {
		t.Errorf("利用者 = %d", n)
	}
	// 通常モード（チームのサーバ）には初回設定の画面が無い（従来どおり 503 のセットアップ未完了）
	n := newEnvWith(t, func(*Config) {})
	if res, body := n.get(n.client(), "/im/first-run"); res.StatusCode != http.StatusServiceUnavailable || strings.Contains(body, `name="password"`) {
		t.Errorf("通常モードの初回設定: %d", res.StatusCode)
	}
}

// 同じログイン名の利用者（無効化された管理者など）がいれば拒否する（そのままでは初回設定が出続けるため）。
func TestFirstRunLoginTaken(t *testing.T) {
	e := newLocalEnv(t)
	old := e.user("alice", "old-password-123", "admin")
	if err := store.SetDisabled(context.Background(), e.db, old.ID, true); err != nil {
		t.Fatal(err)
	}
	c := e.client()
	_, page := e.get(c, "/im/first-run")
	if res, body := e.post(c, "/im/first-run", firstRunForm(csrfOf(t, page))); res.StatusCode != http.StatusBadRequest || !strings.Contains(body, "すでにいます") {
		t.Fatalf("同じログイン名: %d\n%s", res.StatusCode, body)
	}
	form := firstRunForm(csrfOf(t, page))
	form.Set("login", "alice2")
	form.Set("project", "")
	if res, body := e.post(c, "/im/first-run", form); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("別のログイン名: %d\n%s", res.StatusCode, body)
	}
	// プロジェクトを作らなかったときは、最後の画面からハブへ
	if _, done := e.get(c, "/im/first-run/done"); !strings.Contains(done, `href="/im/"`) || strings.Contains(done, "X-Looptrack-Project") {
		t.Errorf("最後の画面:\n%s", done)
	}
}
