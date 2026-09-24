package main

import (
	"context"
	"database/sql"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/auth"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/privfile"
	"github.com/howashoji/looptrack/internal/server"
	"github.com/howashoji/looptrack/internal/setupwiz"
	"github.com/howashoji/looptrack/internal/store"
)

// 未設定のデスクトップ版（looptrack serve に LOOPTRACK_LOCAL_MODE=1 と LOOPTRACK_DSN だけを与えた状態）の初回設定。

// startDesktop は serve と同じ手順（serveConfig → openDB → prepareDB → server.New）で、LOOPTRACK_LOCAL_MODE=1 と LOOPTRACK_DSN だけから
// サーバを動かす（待ち受けは httptest）。
func startDesktop(t *testing.T, dbPath string) (*httptest.Server, *sql.DB) {
	t.Helper()
	for _, k := range []string{"LOOPTRACK_SECRET_KEY", "LOOPTRACK_LISTEN", "LOOPTRACK_BASE_PATH", "LOOPTRACK_PUBLIC_URL", "LOOPTRACK_COOKIE_SECURE", "LOOPTRACK_TRUSTED_PROXIES"} {
		t.Setenv(k, "")
	}
	t.Setenv("LOOPTRACK_LOCAL_MODE", "1")
	t.Setenv("LOOPTRACK_DSN", "sqlite:"+dbPath)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg, _, err := serveConfig(logger)
	if err != nil {
		t.Fatalf("serveConfig: %v", err)
	}
	db, err := openDB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := prepareDB(context.Background(), cfg, db, logger); err != nil {
		t.Fatalf("prepareDB: %v", err)
	}
	h, err := server.New(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, db
}

// webFirstRun はブラウザで初回設定を答える（未設定の / を開く → 初回設定の画面 → 送信 → 最後の画面）。最後の画面を返す。
func webFirstRun(t *testing.T, b *browser, answers url.Values) string {
	t.Helper()
	res, _ := b.get("/looptrack/")
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/looptrack/first-run" {
		t.Fatalf("未設定の /looptrack/: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	res, page := b.get("/looptrack/first-run")
	if res.StatusCode != http.StatusOK || !strings.Contains(page, "初回設定") {
		t.Fatalf("初回設定の画面: %d\n%s", res.StatusCode, page)
	}
	answers.Set("csrf", csrfIn(t, page))
	res, body := b.postForm("/looptrack/first-run", answers)
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/looptrack/first-run/done" {
		t.Fatalf("送信: %d %s\n%s", res.StatusCode, res.Header.Get("Location"), body)
	}
	res, done := b.get("/looptrack/first-run/done")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("最後の画面: %d", res.StatusCode)
	}
	return done
}

// 受け入れ条件: 未設定で起動すると初回設定の画面が出て、答えるとそのままイシューの画面に入れる。
func TestDesktopFirstRun(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "data", "im.db") // ディレクトリも無い
	srv, db := startDesktop(t, dbPath)

	// 鍵は DB の隣の本人だけのファイルに作る
	keyPath := dbPath + secretKeySuffix
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("鍵のファイル: %v", err)
	}
	if err := privfile.Check(keyPath); err != nil {
		t.Errorf("鍵のファイルの権限: %v", err)
	}
	if _, err := auth.NewBox(strings.TrimSpace(string(raw))); err != nil {
		t.Errorf("鍵: %v", err)
	}

	b := newBrowser(t, srv.URL)
	done := webFirstRun(t, b, url.Values{"login": {"alice"}, "name": {""}, "password": {setupPW}, "confirm": {setupPW},
		"two_factor": {"optional"}, "project": {"main"}})
	if !strings.Contains(done, `href="/looptrack/p/main/"`) {
		t.Fatalf("最後の画面にイシューの画面への入口が無い:\n%s", done)
	}
	if code, body := b.createIssueFromBoard("main"); code != http.StatusCreated || !strings.Contains(body, "MAIN-0001") {
		t.Fatalf("起票: %d %s", code, body)
	}
	var n int
	db.QueryRow("SELECT COUNT(*) FROM issues").Scan(&n)
	if n != 1 {
		t.Errorf("イシューの数 = %d", n)
	}

	// 2 回目の起動は同じ鍵を使い、初回設定は出さない
	srv2, _ := startDesktop(t, dbPath)
	if raw2, _ := os.ReadFile(keyPath); string(raw2) != string(raw) {
		t.Error("2 回目の起動で鍵が変わった")
	}
	if res, _ := newBrowser(t, srv2.URL).get("/looptrack/first-run"); res.StatusCode != http.StatusNotFound {
		t.Errorf("設定済みの初回設定: %d, want 404", res.StatusCode)
	}
}

// 鍵を作るのはローカルモード + SQLite で LOOPTRACK_SECRET_KEY が無いときだけ。それ以外は従来どおりエラー。
func TestServeSecretKeyRequired(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := t.TempDir()
	t.Setenv("LOOPTRACK_SECRET_KEY", "")
	t.Setenv("LOOPTRACK_LISTEN", "")
	for _, tc := range []struct{ local, dsn string }{
		{"", "sqlite:" + filepath.Join(dir, "team.db")},
		{"1", "root:pw@tcp(127.0.0.1:3306)/im"},
		{"1", "sqlite::memory:"},
	} {
		t.Setenv("LOOPTRACK_LOCAL_MODE", tc.local)
		t.Setenv("LOOPTRACK_DSN", tc.dsn)
		if _, _, err := serveConfig(logger); err == nil || !strings.Contains(err.Error(), "LOOPTRACK_SECRET_KEY") {
			t.Errorf("local=%q dsn=%q: err = %v, want LOOPTRACK_SECRET_KEY のエラー", tc.local, tc.dsn, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "team.db"+secretKeySuffix)); err == nil {
		t.Error("通常モードで鍵のファイルを作った")
	}
	// 壊れた鍵のファイルは作り直さずにエラー
	bad := filepath.Join(dir, "bad.db")
	os.WriteFile(bad+secretKeySuffix, []byte("not-a-key\n"), 0o600)
	t.Setenv("LOOPTRACK_LOCAL_MODE", "1")
	t.Setenv("LOOPTRACK_DSN", "sqlite:"+bad)
	if _, _, err := serveConfig(logger); err == nil {
		t.Error("壊れた鍵のファイルで起動した")
	}
	if b, _ := os.ReadFile(bad + secretKeySuffix); string(b) != "not-a-key\n" {
		t.Error("壊れた鍵のファイルを書き換えた")
	}
}

// snapshot は初回設定で決まる設定（利用者・二段階認証・プロジェクト・参加）を比べられる形にする（パスワードは照合の結果）。
func snapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	ctx := context.Background()
	var out []string
	users, err := store.ListUsers(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range users {
		ok, _ := auth.VerifyPassword(u.PasswordHash, setupPW)
		out = append(out, fmt.Sprintf("user %s %q %s disabled=%v totp=%v password=%v", u.Login, u.DisplayName, u.Role, u.Disabled, u.TOTPEnabled, ok))
	}
	policy, set, err := store.TwoFactorPolicy(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	out = append(out, fmt.Sprintf("two-factor %s set=%v", policy, set))
	projects, err := store.ListProjects(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range projects {
		out = append(out, fmt.Sprintf("project %s %s width=%d %q counter=%d", p.Slug, p.Prefix, p.Width, p.Name, p.Counter))
		ms, err := store.ListMembers(ctx, db, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range ms {
			out = append(out, fmt.Sprintf("  member %s %s", m.Login, m.Role))
		}
	}
	return strings.Join(out, "\n")
}

// 受け入れ条件: ターミナル版と画面版で、同じ答えから同じ設定ができる。最後の MCP の接続設定の文字列も同じ（setupwiz.MCPConfigs）。
func TestTerminalAndWebSameSettings(t *testing.T) {
	cases := []struct {
		name string
		args []string
		web  url.Values
		slug string
	}{
		{"プロジェクトあり・任意",
			[]string{"--admin-login", "alice", "--admin-name", "Alice", "--two-factor", "optional", "--project", "main", "--project-prefix", "MAIN", "--project-name", "メイン"},
			url.Values{"login": {"alice"}, "name": {"Alice"}, "two_factor": {"optional"}, "project": {"main"}, "project_prefix": {"MAIN"}, "project_name": {"メイン"}},
			"main"},
		{"既定値・必須・プロジェクトなし",
			[]string{"--two-factor", "required"},
			url.Values{"login": {"admin"}, "two_factor": {"required"}, "project": {""}},
			""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// ターミナル版（looptrack setup --yes）
			dir := t.TempDir()
			r := doSetup(context.Background(), t, map[string]string{setupwiz.EnvAdminPassword: setupPW}, "", setupBackend{},
				append([]string{"--dir", dir, "--yes"}, tc.args...)...)
			if r.code != 0 {
				t.Fatalf("setup: %d\n%s", r.code, r.errOut)
			}
			termDB, err := store.Open(envValue(t, filepath.Join(dir, setupwiz.EnvFile), "LOOPTRACK_DSN"))
			if err != nil {
				t.Fatal(err)
			}
			defer termDB.Close()

			// 画面版（未設定のデスクトップ版）
			srv, webDB := startDesktop(t, filepath.Join(t.TempDir(), "im.db"))
			tc.web.Set("password", setupPW)
			tc.web.Set("confirm", setupPW)
			done := webFirstRun(t, newBrowser(t, srv.URL), tc.web)

			if a, b := snapshot(t, termDB), snapshot(t, webDB); a != b {
				t.Errorf("ターミナル版と画面版で設定が違う:\n%s\n---\n%s", a, b)
			}

			// MCP の接続設定: ターミナル版の最後の案内と画面版の最後の画面が、同じ setupwiz.MCPConfigs の文字列を出す
			termURL := "http://127.0.0.1:8090/looptrack"
			page := strings.ReplaceAll(html.UnescapeString(done), srv.URL, "http://127.0.0.1:8090")
			configs := setupwiz.MCPConfigs(i18n.JA, termURL, tc.slug)
			clients := map[string]bool{}
			for _, c := range configs {
				clients[c.Client] = true
				if !strings.Contains(page, c.Text) {
					t.Errorf("画面版に %s（%s）の接続設定が無い: %q", c.Client, c.Where, c.Text)
				}
				for _, l := range strings.Split(c.Text, "\n") {
					if !strings.Contains(r.stdout, "    "+l+"\n") {
						t.Errorf("ターミナル版に %s の接続設定の行が無い: %q\n%s", c.Client, l, r.stdout)
					}
				}
			}
			for _, want := range []string{"Claude Code", "Codex", "GitHub Copilot（VS Code）", "GitHub Copilot CLI"} {
				if !clients[want] {
					t.Errorf("%s の接続設定が無い", want)
				}
			}
		})
	}
}
