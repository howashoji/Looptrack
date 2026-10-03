package server

import (
	"bytes"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

// 配る絵を、サーバが埋め込んだものから読む（埋め込みの取りこぼしもここで分かる）。
func iconBytes(t *testing.T, name string) []byte {
	t.Helper()
	b, err := assets.ReadFile("static/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// 画面のタブに出す favicon。全ページの head に rel="icon" があること、head が指す URL と /favicon.ico が
// 画像として返ること（BASE_PATH の下でも）、CSP を緩めずに済んでいること。

// wantCSP は favicon を足す前の CSP の文字列。img-src 'self' のまま同じ origin のアイコンを読めるので、変えない。
const wantCSP = "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

var iconLinkRe = regexp.MustCompile(`<link rel="icon"[^>]*href="([^"]+)"`)

// pageIconHref は HTML の head にある rel="icon" の href を返す。無ければ空。
func pageIconHref(page string) string {
	m := iconLinkRe.FindStringSubmatch(page)
	if m == nil {
		return ""
	}
	return m[1]
}

// 全ページのテンプレートが、共通の head（layout.html）を使うか、自前の head に rel="icon" を持つこと。
// 画面を 1 枚ずつ開いて確かめる検査（下）の取りこぼしを、ソースの側から塞ぐ。
func TestEveryPageTemplateCarriesIcon(t *testing.T) {
	files, err := fs.Glob(assets, "templates/*.html")
	if err != nil || len(files) == 0 {
		t.Fatalf("テンプレートが見つからない: %v", err)
	}
	var own, shared int
	for _, f := range files {
		raw, err := fs.ReadFile(assets, f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(raw)
		if strings.HasSuffix(f, "/layout.html") {
			if !strings.Contains(src, `rel="icon"`) {
				t.Errorf("%s の head に rel=\"icon\" が無い", f)
			}
			continue
		}
		switch {
		case strings.Contains(src, "<head>"):
			own++
			if !strings.Contains(src, `rel="icon"`) {
				t.Errorf("%s は自前の head を持つのに rel=\"icon\" が無い", f)
			}
		case strings.Contains(src, `{{template "head"`):
			shared++
		case strings.Contains(src, `{{define "`) && !strings.Contains(src, "<html"):
			// 画面の一部品（nav など）。
		default:
			t.Errorf("%s は共通の head も自前の head も使っていない（favicon が付かない）", f)
		}
	}
	// 対照: 自前の head を持つ画面（hub・board）と、共通の head を使う画面の両方を実際に通ったこと。
	if own < 2 || shared < 10 {
		t.Errorf("検査の前提が崩れています: 自前の head %d 枚・共通の head %d 枚（hub・board を含む 2 枚以上と 10 枚以上のはず）", own, shared)
	}
}

// 画面のログイン前（ログイン・初回設定）とログイン後（ハブ・ボード・管理者の画面・アカウント）。
func TestPagesCarryIconLink(t *testing.T) {
	check := func(t *testing.T, name, page string) {
		t.Helper()
		if got := pageIconHref(page); got != "/im/static/icon.png" {
			t.Errorf("%s の rel=\"icon\" の href = %q, want /im/static/icon.png", name, got)
		}
	}

	e := newEnv(t)
	c := e.client()
	res, page := e.get(c, "/im/login")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("/im/login: %d", res.StatusCode)
	}
	check(t, "ログイン", page)

	e.user("admin", "correct-horse-battery", "admin")
	e.project("web")
	e.enroll(c, "admin", "correct-horse-battery")
	for _, p := range []string{"/im/", "/im/p/web/", "/im/admin/users", "/im/account", "/im/no-such-page"} {
		_, page := e.get(c, p)
		check(t, "ログイン後 "+p, page)
	}

	l := newLocalEnv(t)
	lc := l.client()
	res, _ = l.get(lc, "/im/")
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/im/first-run" {
		t.Fatalf("未設定の /im/: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	res, page = l.get(lc, "/im/first-run")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("/im/first-run: %d", res.StatusCode)
	}
	check(t, "初回設定", page)
}

// head の href は BASE_PATH に追従する（空の BASE_PATH は設定できない。envOr が既定値に戻す）。
func TestIconLinkFollowsBasePath(t *testing.T) {
	for base, want := range map[string]string{"/lt/sub": "/lt/sub/static/icon.png", "/x": "/x/static/icon.png"} {
		e := newEnvWith(t, func(cfg *Config) { cfg.BasePath = base; cfg.AllowNoAdmin = true })
		res, err := http.Get(e.srv.URL + base + "/login")
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("BASE_PATH=%q のログイン画面: %d", base, res.StatusCode)
		}
		if got := pageIconHref(string(raw)); got != want {
			t.Errorf("BASE_PATH=%q の rel=\"icon\" の href = %q, want %q", base, got, want)
		}
	}
}

// head が指す URL と /favicon.ico が、画像として 200 で返る。BASE_PATH の下でも、未ログインでも返る。
// CSP の文字列は favicon の応答でも画面と同じで、変わっていない。
func TestIconRoutes(t *testing.T) {
	for _, base := range []string{"/im", "/lt/sub"} {
		s, err := New(Config{BasePath: base}, nil)
		if err != nil {
			t.Fatal(err)
		}
		get := func(path string) *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
			return rec
		}
		cases := []struct {
			path, ctype string
			body        []byte
		}{
			{base + "/static/icon.png", "image/png", iconBytes(t, "icon.png")},
			{base + "/favicon.ico", "image/x-icon", iconBytes(t, "favicon.ico")},
		}
		for _, tc := range cases {
			rec := get(tc.path)
			if rec.Code != http.StatusOK {
				t.Errorf("BASE_PATH=%q GET %s: %d, want 200", base, tc.path, rec.Code)
				continue
			}
			if ct := rec.Header().Get("Content-Type"); ct != tc.ctype {
				t.Errorf("BASE_PATH=%q GET %s の Content-Type = %q, want %q", base, tc.path, ct, tc.ctype)
			}
			if !bytes.Equal(rec.Body.Bytes(), tc.body) {
				t.Errorf("BASE_PATH=%q GET %s の本文が、デスクトップ版のアイコン（internal/client/desktop/icon）と違う", base, tc.path)
			}
			if csp := rec.Header().Get("Content-Security-Policy"); csp != wantCSP {
				t.Errorf("BASE_PATH=%q GET %s の CSP = %q, want 変えていない値", base, tc.path, csp)
			}
		}
		// 対照: 別のパスの favicon.ico は 200 にならない（経路が何にでも 200 を返しているのではない）。
		if rec := get("/favicon.ico"); rec.Code == http.StatusOK {
			t.Errorf("BASE_PATH=%q の外の /favicon.ico が 200 を返している", base)
		}
	}
}

// Web の絵は、デスクトップ版のアイコンと同じバイト列の複製（片方だけ差し替えると絵がずれる）。
func TestIconMatchesDesktopApp(t *testing.T) {
	for web, desktop := range map[string]string{"icon.png": "app_256.png", "favicon.ico": "app.ico"} {
		want, err := os.ReadFile("../client/desktop/icon/" + desktop)
		if err != nil || len(want) == 0 {
			t.Fatalf("デスクトップ版のアイコン %s を読めない: %v", desktop, err)
		}
		if !bytes.Equal(iconBytes(t, web), want) {
			t.Errorf("internal/server/static/%s が internal/client/desktop/icon/%s と違う。"+
				"差し替えるときは両方を同じ内容にする", web, desktop)
		}
	}
}

// 絵の中身が PNG と ICO の形になっている（Content-Type だけ画像で、本文が空・別物という状態を拾う）。
func TestIconBytesAreImages(t *testing.T) {
	if !bytes.HasPrefix(iconBytes(t, "icon.png"), []byte("\x89PNG\r\n\x1a\n")) {
		t.Error("static/icon.png が PNG ではない")
	}
	if !bytes.HasPrefix(iconBytes(t, "favicon.ico"), []byte{0, 0, 1, 0}) {
		t.Error("static/favicon.ico が ICO ではない")
	}
}

// セットアップ未完了でも /favicon.ico はリダイレクトも 503 も返さず、そのまま画像を返す。
// 対照: 同じ状態で画面（/im/）は初回設定へ飛ばされる。
func TestFaviconBeforeSetup(t *testing.T) {
	e := newLocalEnv(t)
	c := e.client()
	if res, _ := e.get(c, "/im/"); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("対照: 未設定の /im/ = %d, want 303（前提が崩れています）", res.StatusCode)
	}
	for _, p := range []string{"/im/favicon.ico", "/im/static/icon.png"} {
		res, _ := e.get(c, p)
		if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "image/") {
			t.Errorf("未設定の GET %s: %d %q, want 200 の画像", p, res.StatusCode, res.Header.Get("Content-Type"))
		}
	}
}
