package main

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/auth"
	"github.com/howashoji/looptrack/internal/server"
	"github.com/howashoji/looptrack/internal/setupwiz"
	"github.com/howashoji/looptrack/internal/store"
)

// ローカル + SQLite の setup の直後に、ブラウザだけでイシューを 1 件作れる。

// browser はテスト用のブラウザ（Cookie を持ち、転送は追わない）。
type browser struct {
	t    *testing.T
	base string
	c    *http.Client
}

func newBrowser(t *testing.T, base string) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, base: base, c: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (b *browser) do(method, path, contentType, body string, header ...string) (*http.Response, string) {
	b.t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, b.base+path, r)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	// 表示の言語を日本語に固定する（既定は英語なので、付けないと日本語の文面を検査する
	// 表明が英語の画面と突き合わさる。ブラウザは必ずこのヘッダを送る）。
	req.Header.Set("Accept-Language", "ja")
	// ブラウザが同じオリジンへ送るときのヘッダ
	if method != http.MethodGet {
		req.Header.Set("Origin", b.base)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	res, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res, string(raw)
}

func (b *browser) get(path string) (*http.Response, string) { return b.do("GET", path, "", "") }

func (b *browser) postForm(path string, form url.Values) (*http.Response, string) {
	return b.do("POST", path, "application/x-www-form-urlencoded", form.Encode())
}

var csrfFieldRe = regexp.MustCompile(`name="csrf" value="([^"]+)"`)
var bodyCSRFRe = regexp.MustCompile(`data-csrf="([^"]+)"`)

func csrfIn(t *testing.T, html string) string {
	t.Helper()
	for _, re := range []*regexp.Regexp{csrfFieldRe, bodyCSRFRe} {
		if m := re.FindStringSubmatch(html); m != nil {
			return m[1]
		}
	}
	t.Fatalf("CSRF トークンが無い:\n%s", html)
	return ""
}

// createIssueFromBoard はボードの画面を開き、その画面のセッション（Cookie + X-CSRF-Token）で起票する（画面の JS と同じ要求）。
func (b *browser) createIssueFromBoard(slug string) (int, string) {
	b.t.Helper()
	res, page := b.get("/looptrack/p/" + slug + "/")
	if res.StatusCode != http.StatusOK {
		b.t.Fatalf("ボード %s: %d", slug, res.StatusCode)
	}
	// ボードに「起票」ボタンがある（押すと起票フォームが開き、render.js の formRequest("create") の要求を送る）
	if !strings.Contains(page, `id="newIssue"`) {
		b.t.Fatalf("ボード %s に起票ボタンが無い:\n%s", slug, page)
	}
	res, body := b.do("POST", "/looptrack/api/v1/projects/"+slug+"/issues", "application/json",
		`{"title":"ブラウザからの起票","type":"task","priority":"P2","body":"画面のフォームから\n\n## 受け入れ条件\n\n- [ ] 1 件できる"}`,
		"X-CSRF-Token", csrfIn(b.t, page))
	return res.StatusCode, body
}

// startServe は serve と同じ設定（ローカルモード・.env の DSN と鍵）でサーバを動かす。
func startServe(t *testing.T, envPath string, cfg server.Config) (*httptest.Server, *sql.DB) {
	t.Helper()
	db, err := store.Open(envValue(t, envPath, "LOOPTRACK_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	box, err := auth.NewBox(envValue(t, envPath, "LOOPTRACK_SECRET_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.BasePath, cfg.Box, cfg.LocalMode = "/looptrack", box, true
	cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	h, err := server.New(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, db
}

func TestSetupThenBrowserCreatesIssue(t *testing.T) {
	ctx := context.Background()
	env := map[string]string{setupwiz.EnvAdminPassword: setupPW}

	t.Run("プロジェクトなしの setup の後、画面でプロジェクトを作って起票", func(t *testing.T) {
		dir := t.TempDir()
		if r := doSetup(ctx, t, env, "", setupBackend{}, "--dir", dir, "--yes", "--two-factor", "optional"); r.code != 0 {
			t.Fatalf("setup: %d\n%s", r.code, r.errOut)
		}
		srv, db := startServe(t, filepath.Join(dir, setupwiz.EnvFile), server.Config{})
		b := newBrowser(t, srv.URL)
		res, page := b.get("/looptrack/admin/projects")
		if res.StatusCode != http.StatusOK || !strings.Contains(page, "プロジェクトを作る") {
			t.Fatalf("プロジェクト管理: %d\n%s", res.StatusCode, page)
		}
		res, body := b.postForm("/looptrack/admin/projects", url.Values{"csrf": {csrfIn(t, page)}, "slug": {"first"}, "name": {"最初"}})
		if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/looptrack/p/first/" {
			t.Fatalf("プロジェクトの作成: %d %s\n%s", res.StatusCode, res.Header.Get("Location"), body)
		}
		if code, body := b.createIssueFromBoard("first"); code != http.StatusCreated || !strings.Contains(body, "FIRST-0001") {
			t.Fatalf("起票: %d %s", code, body)
		}
		var n int
		db.QueryRow("SELECT COUNT(*) FROM issues").Scan(&n)
		if n != 1 {
			t.Errorf("イシューの数 = %d", n)
		}
	})

	t.Run("setup の⑥で作ったプロジェクトにすぐ起票", func(t *testing.T) {
		dir := t.TempDir()
		r := doSetup(ctx, t, env, "", setupBackend{}, "--dir", dir, "--yes", "--two-factor", "optional", "--project", "main")
		if r.code != 0 {
			t.Fatalf("setup: %d\n%s", r.code, r.errOut)
		}
		if !strings.Contains(r.stdout, "プロジェクト: main（MAIN-nnnn）") || !strings.Contains(r.stdout, "admin で参加させました") {
			t.Errorf("setup の表示:\n%s", r.stdout)
		}
		srv, _ := startServe(t, filepath.Join(dir, setupwiz.EnvFile), server.Config{})
		if code, body := newBrowser(t, srv.URL).createIssueFromBoard("main"); code != http.StatusCreated || !strings.Contains(body, "MAIN-0001") {
			t.Fatalf("起票: %d %s", code, body)
		}
	})

	t.Run("looptrack project create で作ったプロジェクト（参加者なし）にも起票できる", func(t *testing.T) {
		dir := t.TempDir()
		if r := doSetup(ctx, t, env, "", setupBackend{}, "--dir", dir, "--yes", "--two-factor", "optional"); r.code != 0 {
			t.Fatalf("setup: %d\n%s", r.code, r.errOut)
		}
		srv, db := startServe(t, filepath.Join(dir, setupwiz.EnvFile), server.Config{})
		if _, err := store.CreateProject(ctx, db, store.Project{Slug: "cli", Prefix: "CLI", Width: 4, Name: "cli"}); err != nil {
			t.Fatal(err)
		}
		if code, body := newBrowser(t, srv.URL).createIssueFromBoard("cli"); code != http.StatusCreated {
			t.Fatalf("起票: %d %s", code, body)
		}
	})
}
