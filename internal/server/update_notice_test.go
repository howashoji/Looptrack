package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/updatecheck"
)

// 新しい版の帯（layout.html の update_notice）。デスクトップ版が Config.UpdateNotice で確認の結果を渡したときだけ、
// 共通ヘッダを使う画面（プロジェクトの一覧・ボード・アカウント設定）の行の下に出る。

// noticeSource は差し替えられる知らせ（デスクトップ版の確認の結果の代わり）。
type noticeSource struct {
	mu sync.Mutex
	n  *updatecheck.Notice
}

func (s *noticeSource) get() *updatecheck.Notice {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

func (s *noticeSource) set(n *updatecheck.Notice) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n = n
}

func TestUpdateNoticeBanner(t *testing.T) {
	src := &noticeSource{}
	var tray atomic.Bool
	tray.Store(true)
	e := newEnvWith(t, func(cfg *Config) { cfg.LocalMode = true; cfg.UpdateNotice = src.get; cfg.UpdateStopInTray = tray.Load })
	e.user("boss", "boss-password-12", "admin")
	e.project("demo")
	c := e.client()
	pages := []string{"/im/", "/im/p/demo/", "/im/account"}
	page := func(p string, header ...string) string {
		t.Helper()
		res, body := e.get(c, p, header...)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: %d %s", p, res.StatusCode, body)
		}
		return body
	}

	// 知らせが無い間は帯の要素そのものを出さない
	for _, p := range pages {
		if body := page(p); strings.Contains(body, "appbar-update") {
			t.Errorf("知らせが無いのに %s に帯がある", p)
		}
	}

	src.set(&updatecheck.Notice{Version: "v1.0.0-rc.3", Current: "v1.0.0-rc.2", URL: "https://github.com/howashoji/looptrack/releases/tag/v1.0.0-rc.3"})
	for _, p := range pages {
		body := page(p)
		for _, want := range []string{
			`class="appbar-update" role="status"`,
			"新しい版 v1.0.0-rc.3 があります（いまの版は v1.0.0-rc.2）。",
			`<a href="https://github.com/howashoji/looptrack/releases/tag/v1.0.0-rc.3" target="_blank" rel="noopener noreferrer">リリースのページを開く</a>`,
			"トレイのメニューの「新しい版を確認する」で止められます。",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s の帯に %q が無い", p, want)
			}
		}
		if strings.Contains(body, "LOOPTRACK_UPDATE_CHECK") {
			t.Errorf("%s: トレイを出しているのに環境変数を案内した", p)
		}
		if strings.Count(body, `class="appbar-update"`) != 1 {
			t.Errorf("%s の帯が 1 つでない（%d）", p, strings.Count(body, `class="appbar-update"`))
		}
	}
	en := page("/im/account", "Accept-Language", "en")
	for _, want := range []string{"A new version, v1.0.0-rc.3, is available (you have v1.0.0-rc.2).", ">Open the release page</a>",
		"To stop checking, clear &#34;Check for updates&#34; in the tray menu."} {
		if !strings.Contains(en, want) {
			t.Errorf("英語の帯に %q が無い", want)
		}
	}

	// トレイを出していない（headless・--no-tray）ときは、止め方を環境変数で案内する（トレイのメニューを指さない）
	tray.Store(false)
	if body := page("/im/account"); !strings.Contains(body, "確認を止めるには、環境変数 LOOPTRACK_UPDATE_CHECK=off を付けて起動し直します。") ||
		strings.Contains(body, "トレイのメニュー") {
		t.Error("トレイなし（ja）: 環境変数の案内が無いか、トレイを案内した")
	}
	if body := page("/im/account", "Accept-Language", "en"); !strings.Contains(body, "To stop checking, restart with the environment variable LOOPTRACK_UPDATE_CHECK=off.") ||
		strings.Contains(body, "tray menu") {
		t.Error("トレイなし（en）: 環境変数の案内が無いか、トレイを案内した")
	}
	tray.Store(true)

	// URL の無い知らせ（https でないリリースのページは updatecheck が落とす）はリンクを出さない
	src.set(&updatecheck.Notice{Version: "v1.0.0-rc.3", Current: "v1.0.0-rc.2"})
	if body := page("/im/account"); !strings.Contains(body, "新しい版 v1.0.0-rc.3 があります") || strings.Contains(body, "リリースのページを開く") {
		t.Error("URL の無い知らせ: 帯が無いか、リンクを出した")
	}

	// 知らせが消えたら（確認を止めた・最新になった）帯も消える
	src.set(nil)
	if body := page("/im/account"); strings.Contains(body, "appbar-update") {
		t.Error("知らせが消えた後も帯がある")
	}
}

// チームのサーバ（UpdateNotice を渡さない）には帯が出ない。
func TestUpdateNoticeBannerNotOnTeamServer(t *testing.T) {
	e := newEnv(t)
	e.user("alice", "alice-password-1", "member")
	c := e.client()
	e.enroll(c, "alice", "alice-password-1")
	res, body := e.get(c, "/im/account")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("アカウント設定: %d", res.StatusCode)
	}
	if !strings.Contains(body, `class="appbar"`) {
		t.Fatal("前提: 共通ヘッダが無い（帯の有無を見る意味が無い）")
	}
	if strings.Contains(body, "appbar-update") {
		t.Error("チームのサーバに新しい版の帯が出た")
	}
}

// サーバ版（looptrack serve が UpdateServer を立てる）: 帯は role が admin の利用者にだけ出し、更新の 1 行（install.sh --upgrade）と
// 止め方（.env の LOOPTRACK_UPDATE_CHECK=off）を案内する。member には出さない。デスクトップ版のトレイ・環境変数の案内は出さない。
func TestUpdateNoticeBannerServer(t *testing.T) {
	src := &noticeSource{}
	e := newEnvWith(t, func(cfg *Config) { cfg.AllowNoAdmin = true; cfg.UpdateNotice = src.get; cfg.UpdateServer = true })
	e.user("boss", "boss-password-12", "admin")
	e.user("alice", "alice-password-1", "member")
	admin, member := e.client(), e.client()
	e.enroll(admin, "boss", "boss-password-12")
	e.enroll(member, "alice", "alice-password-1")
	page := func(c *http.Client, p string, header ...string) string {
		t.Helper()
		res, body := e.get(c, p, header...)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: %d %s", p, res.StatusCode, body)
		}
		if !strings.Contains(body, `class="appbar"`) {
			t.Fatalf("前提: %s に共通ヘッダが無い（帯の有無を見る意味が無い）", p)
		}
		return body
	}

	// 知らせが無い間は admin にも出さない
	if body := page(admin, "/im/account"); strings.Contains(body, "appbar-update") {
		t.Error("知らせが無いのに帯がある")
	}

	src.set(&updatecheck.Notice{Version: "v1.0.0", Current: "v1.0.0-rc.3", URL: "https://github.com/howashoji/looptrack/releases/tag/v1.0.0"})
	for _, p := range []string{"/im/", "/im/account", "/im/admin/users"} {
		body := page(admin, p)
		for _, want := range []string{
			`class="appbar-update" role="status"`,
			"新しい版 v1.0.0 があります（いまの版は v1.0.0-rc.3）。",
			`<a href="https://github.com/howashoji/looptrack/releases/tag/v1.0.0" target="_blank" rel="noopener noreferrer">リリースのページを開く</a>`,
			"install.sh で入れたサーバは、サーバで次の 1 行を実行すると更新できます:",
			"<code>curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh -s -- --upgrade</code>",
			"コンテナのイメージは自動では置き換わりません。確認を止めるには、.env に LOOPTRACK_UPDATE_CHECK=off を書いて起動し直します。",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("admin の %s の帯に %q が無い", p, want)
			}
		}
		if strings.Contains(body, "トレイのメニュー") || strings.Contains(body, "を付けて起動し直します") {
			t.Errorf("admin の %s: サーバ版なのにデスクトップ版の止め方を案内した", p)
		}
	}
	en := page(admin, "/im/account", "Accept-Language", "en")
	for _, want := range []string{"A new version, v1.0.0, is available (you have v1.0.0-rc.3).",
		"On a server installed with install.sh, update it by running this on the server:",
		"Container images are not replaced automatically. To stop checking, set LOOPTRACK_UPDATE_CHECK=off in .env and restart."} {
		if !strings.Contains(en, want) {
			t.Errorf("英語の帯に %q が無い", want)
		}
	}
	// member には出さない（対照: 同じ時点の admin には出ている）
	for _, p := range []string{"/im/", "/im/account"} {
		if body := page(member, p); strings.Contains(body, "appbar-update") {
			t.Errorf("member の %s に帯が出た", p)
		}
	}
	if body := page(admin, "/im/account"); !strings.Contains(body, "appbar-update") {
		t.Error("対照: admin の帯が無い")
	}
}

// GET /api/v1/dist の server_update（looptrack doctor が注意として出す）は admin のトークンにだけ付く。
// 更新の 1 行はサーバ版（UpdateServer）だけ。券の一覧（/setup/<券>/）と member・知らせが無いときには付かない。
func TestDistServerUpdate(t *testing.T) {
	src := &noticeSource{}
	e := newEnvWith(t, func(cfg *Config) { cfg.AllowNoAdmin = true; cfg.UpdateNotice = src.get; cfg.UpdateServer = true })
	boss := e.apiAs(e.user("boss", "boss-password-12", "admin"))
	alice := e.apiAs(e.user("alice", "alice-password-1", "member"))
	get := func(a *apiClient) map[string]any {
		t.Helper()
		var out map[string]any
		a.json(http.StatusOK, "GET", "/dist", nil, &out)
		return out
	}
	if _, ok := get(boss)["server_update"]; ok {
		t.Error("知らせが無いのに server_update がある")
	}
	src.set(&updatecheck.Notice{Version: "v1.0.0", Current: "v1.0.0-rc.3", URL: "https://example.invalid/releases/tag/v1.0.0"})
	su, _ := get(boss)["server_update"].(map[string]any)
	want := map[string]any{"version": "v1.0.0", "current": "v1.0.0-rc.3", "url": "https://example.invalid/releases/tag/v1.0.0",
		"command": updatecheck.ServerUpgradeCommand}
	if fmt.Sprint(su) != fmt.Sprint(want) {
		t.Errorf("admin の server_update = %v, want %v", su, want)
	}
	if _, ok := get(alice)["server_update"]; ok {
		t.Error("member に server_update が付いた")
	}

	// 券の一覧（/setup/<券>/ は apiDist を呼ぶ。利用者が admin でも付けない）
	req := httptest.NewRequest("GET", "/im/setup/x/", nil)
	req.SetPathValue("ticket", "x")
	req = req.WithContext(context.WithValue(req.Context(), principalKey{}, &principal{User: store.User{Role: "admin"}}))
	if su := e.s.serverUpdate(req); su != nil {
		t.Errorf("券の一覧に server_update: %v", su)
	}
	req.SetPathValue("ticket", "")
	if su := e.s.serverUpdate(req); su == nil {
		t.Error("対照: 券なし・admin で server_update が無い")
	}

	// デスクトップ版（UpdateServer なし）は更新の 1 行を付けない
	d := newEnvWith(t, func(cfg *Config) { cfg.AllowNoAdmin = true; cfg.UpdateNotice = src.get })
	su, _ = get(d.apiAs(d.user("boss", "boss-password-12", "admin")))["server_update"].(map[string]any)
	if su == nil || su["version"] != "v1.0.0" || su["command"] != nil {
		t.Errorf("デスクトップ版の server_update: %v", su)
	}
}
