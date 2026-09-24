package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/howashoji/looptrack/internal/store"
)

// ローカルモード（認証なし・最初の管理者として通す・Host と Origin の検査）と、管理者 0 人のときの「セットアップ未完了」。

func newLocalEnv(t *testing.T) *env {
	return newEnvWith(t, func(cfg *Config) { cfg.LocalMode = true })
}

// do は任意のメソッド・ヘッダ・本文で要求し、状態と本文を返す。
func (e *env) do(c *http.Client, method, path, body string, header ...string) (*http.Response, string) {
	e.t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, r)
	for i := 0; i+1 < len(header); i += 2 {
		if header[i] == "Host" {
			req.Host = header[i+1]
			continue
		}
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

const mcpInitBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"x","version":"1"}}}`

// checkSetupRequired は画面が「セットアップ未完了」、API・MCP・OAuth が 503 の JSON、healthz・静的ファイルは通ることを確かめる。
func checkSetupRequired(t *testing.T, e *env, label string) {
	t.Helper()
	c := e.client()
	for _, p := range []string{"/im/", "/im/login", "/im/p/x/", "/im/account", "/im/admin/users"} {
		res, body := e.get(c, p)
		if res.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, "looptrack setup") || strings.Contains(body, `name="password"`) {
			t.Errorf("%s: GET %s: %d, want 503 のセットアップ未完了: %s", label, p, res.StatusCode, body)
		}
	}
	res, body := e.post(c, "/im/login", url.Values{"login": {"x"}, "password": {"y"}})
	if res.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, "looptrack setup") {
		t.Errorf("%s: POST /im/login: %d, want 503", label, res.StatusCode)
	}
	for _, p := range []string{"/im/api/v1/me", "/im/api/v1/projects", "/im/api/v1/dist", "/im/api/v1/dist/bin/looptrack-linux-amd64",
		"/im/api/v1/projects/x/install", "/im/setup/ticket/", "/im/setup/ticket/bin/looptrack-linux-amd64", "/im/oauth/token"} {
		res, body := e.get(c, p, "Authorization", "Bearer imp_whatever")
		var v apiError
		if res.StatusCode != http.StatusServiceUnavailable || json.Unmarshal([]byte(body), &v) != nil || v.Error.Code != "setup_required" ||
			!strings.Contains(v.Error.Message, "looptrack setup") {
			t.Errorf("%s: GET %s: %d %s, want 503 setup_required", label, p, res.StatusCode, body)
		}
	}
	res, body = e.do(c, "POST", "/im/mcp", mcpInitBody, "Content-Type", "application/json", "Accept", "application/json, text/event-stream")
	if res.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, `"setup_required"`) {
		t.Errorf("%s: MCP: %d %s, want 503", label, res.StatusCode, body)
	}
	if res, _ := e.get(c, "/im/healthz"); res.StatusCode != http.StatusOK {
		t.Errorf("%s: healthz: %d", label, res.StatusCode)
	}
	if res, _ := e.get(c, "/im/static/app.css"); res.StatusCode != http.StatusOK {
		t.Errorf("%s: 静的ファイル: %d", label, res.StatusCode)
	}
}

func TestSetupRequiredWithoutAdmin(t *testing.T) {
	e := newEnvWith(t, func(*Config) {}) // 本番と同じ（AllowNoAdmin なし）
	checkSetupRequired(t, e, "利用者 0 人")

	// 管理者でない利用者・無効化された管理者だけでも未完了のまま（そのパスワードでもログインできない）
	e.user("member1", "member-password-1", "member")
	old := e.user("old", "old-password-123", "admin")
	if err := store.SetDisabled(context.Background(), e.db, old.ID, true); err != nil {
		t.Fatal(err)
	}
	checkSetupRequired(t, e, "有効な管理者 0 人")

	// 管理者ができたら通常の認証に戻る（再起動は要らない）
	e.user("boss", "boss-password-12", "admin")
	c := e.client()
	if res, _ := e.get(c, "/im/"); res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(res.Header.Get("Location"), "/im/login") {
		t.Errorf("管理者の作成後の画面: %d %s, want ログイン画面へ", res.StatusCode, res.Header.Get("Location"))
	}
	if res, _ := e.get(c, "/im/login"); res.StatusCode != http.StatusOK {
		t.Errorf("管理者の作成後のログイン画面: %d", res.StatusCode)
	}
	if res, _ := e.get(c, "/im/api/v1/me"); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("管理者の作成後の API: %d, want 401", res.StatusCode)
	}
	res, _ := e.do(c, "POST", "/im/mcp", mcpInitBody, "Content-Type", "application/json")
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("管理者の作成後の MCP: %d, want 401", res.StatusCode)
	}
}

// ローカルモードで管理者が 0 人の間は、画面は初回設定、API・MCP は 503 の setup_required。
func TestSetupRequiredLocalMode(t *testing.T) {
	e := newLocalEnv(t)
	check := func(label string) {
		t.Helper()
		c := e.client()
		for _, p := range []string{"/im/", "/im/login", "/im/p/x/", "/im/account", "/im/admin/users", "/im/first-run/done"} {
			if res, _ := e.get(c, p); res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/im/first-run" {
				t.Errorf("%s: GET %s: %d %s, want 初回設定へ", label, p, res.StatusCode, res.Header.Get("Location"))
			}
		}
		if res, body := e.get(c, "/im/first-run"); res.StatusCode != http.StatusOK || !strings.Contains(body, `action="/im/first-run"`) {
			t.Errorf("%s: 初回設定の画面: %d %s", label, res.StatusCode, body)
		}
		if res, body := e.post(c, "/im/login", url.Values{"login": {"x"}, "password": {"y"}}); res.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s: POST /im/login: %d %s, want 503", label, res.StatusCode, body)
		}
		for _, p := range []string{"/im/api/v1/me", "/im/api/v1/projects", "/im/api/v1/dist", "/im/oauth/token"} {
			res, body := e.get(c, p)
			var v apiError
			if res.StatusCode != http.StatusServiceUnavailable || json.Unmarshal([]byte(body), &v) != nil || v.Error.Code != "setup_required" ||
				!strings.Contains(v.Error.Message, "初回設定") {
				t.Errorf("%s: GET %s: %d %s, want 503 setup_required", label, p, res.StatusCode, body)
			}
		}
		res, body := e.do(c, "POST", "/im/mcp", mcpInitBody, "Content-Type", "application/json", "Accept", "application/json, text/event-stream")
		if res.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, `"setup_required"`) {
			t.Errorf("%s: MCP: %d %s, want 503", label, res.StatusCode, body)
		}
		for _, p := range []string{"/im/healthz", "/im/static/app.css"} {
			if res, _ := e.get(c, p); res.StatusCode != http.StatusOK {
				t.Errorf("%s: %s: %d", label, p, res.StatusCode)
			}
		}
	}
	check("ローカルモード・利用者 0 人")
	e.user("member1", "member-password-1", "member")
	check("ローカルモード・管理者 0 人")
	e.user("boss", "boss-password-12", "admin")
	c := e.client()
	if res, body := e.get(c, "/im/api/v1/me"); res.StatusCode != http.StatusOK || !strings.Contains(body, `"boss"`) {
		t.Errorf("管理者の作成後: %d %s", res.StatusCode, body)
	}
	// 管理者ができたら初回設定は二度と出さない
	if res, _ := e.get(c, "/im/first-run"); res.StatusCode != http.StatusNotFound {
		t.Errorf("管理者の作成後の初回設定: %d, want 404", res.StatusCode)
	}
}

// localSetup は「無効化された管理者（ID 最小）・一般利用者・管理者 2 人」を作り、ローカルの利用者（boss）が editor のプロジェクトを返す。
func localSetup(t *testing.T, e *env) store.User {
	t.Helper()
	ctx := context.Background()
	old := e.user("old", "old-password-123", "admin")
	if err := store.SetDisabled(ctx, e.db, old.ID, true); err != nil {
		t.Fatal(err)
	}
	e.user("member1", "member-password-1", "member")
	boss := e.user("boss", "boss-password-12", "admin")
	e.user("boss2", "boss2-password-1", "admin")
	pr := e.project("loc")
	if err := store.SetMember(ctx, e.db, pr.ID, boss.ID, "editor"); err != nil {
		t.Fatal(err)
	}
	return boss
}

// lastEvent は直近のイベントの操作者と経路。
func (e *env) lastEvent(display string) (login, via string) {
	e.t.Helper()
	if err := e.db.QueryRow(`SELECT u.login, e.via FROM issue_events e JOIN issues i ON i.id = e.issue_id JOIN users u ON u.id = e.actor_user_id
WHERE i.display_id = ? ORDER BY e.id DESC LIMIT 1`, display).Scan(&login, &via); err != nil {
		e.t.Fatalf("%s のイベント: %v", display, err)
	}
	return login, via
}

func TestLocalModeAPI(t *testing.T) {
	e := newLocalEnv(t)
	localSetup(t, e)
	c := e.client()

	// トークンなし（無効なトークンを付けても見ない）で最初の有効な管理者として通る
	for _, h := range [][]string{nil, {"Authorization", "Bearer imp_invalid"}} {
		res, body := e.get(c, "/im/api/v1/me", h...)
		if res.StatusCode != http.StatusOK || !strings.Contains(body, `"login":"boss"`) || !strings.Contains(body, `"via":"api"`) {
			t.Errorf("me %v: %d %s", h, res.StatusCode, body)
		}
	}
	// 配布物の一覧・導入済み通知も同じ中間層で通る（ハンドラは変えない）
	for _, p := range []string{"/im/api/v1/dist", "/im/api/v1/projects/loc/install"} {
		if res, body := e.get(c, p); res.StatusCode != http.StatusOK {
			t.Errorf("GET %s: %d %s", p, res.StatusCode, body)
		}
	}
	res, body := e.do(c, "POST", "/im/api/v1/projects/loc/issues", `{"title":"ローカルの起票"}`,
		"Content-Type", "application/json", "X-Looptrack-Client", "cli")
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("起票: %d %s", res.StatusCode, body)
	}
	if login, via := e.lastEvent("LOC-0001"); login != "boss" || via != "cli" {
		t.Errorf("起票の記録 = %s / %s, want boss / cli", login, via)
	}
	// 同じオリジンの Origin は通る
	res, body = e.do(c, "POST", "/im/api/v1/issues/LOC-0001/comments", `{"text":"同じオリジンから"}`,
		"Content-Type", "application/json", "Origin", e.srv.URL, "Sec-Fetch-Site", "same-origin")
	if res.StatusCode >= 300 {
		t.Errorf("同じオリジンのコメント: %d %s", res.StatusCode, body)
	}
	if login, via := e.lastEvent("LOC-0001"); login != "boss" || via != "api" {
		t.Errorf("コメントの記録 = %s / %s, want boss / api", login, via)
	}
}

// TestLocalModeCLIWithoutToken は、同梱の CLI（looptrack issue …）が、ローカルモードのサーバに対しては
// トークンを 1 本も発行せずに使えること（画面・MCP と同じ扱い）。
// 同じループバックの URL でも、通常モード（チームのサーバ）では今までどおりトークンを求めて止まる
// ＝認証が緩むのはローカルモードのサーバだけ。
func TestLocalModeCLIWithoutToken(t *testing.T) {
	testutilDSNOrSkip(t)
	e := newLocalEnv(t)
	localSetup(t, e)
	// 認証を省いているサーバだけが、認証の要らない /healthz でそう名乗る（CLI はこれを見る）
	if res, _ := e.get(e.client(), "/im/healthz"); res.StatusCode != http.StatusOK || res.Header.Get("X-Looptrack-Local-Mode") != "1" {
		t.Fatalf("ローカルモードの healthz: %d %q", res.StatusCode, res.Header.Get("X-Looptrack-Local-Mode"))
	}
	run := issueCLI(t, e, "loc", "", "LOOPTRACK_USAGE=0") // トークンを渡さない・HOME も空（資格情報が無い）

	if res := run("list"); res.code != 0 {
		t.Fatalf("ローカルモードの list: %d %s %s", res.code, res.stdout, res.stderr)
	}
	if res := run("new", "トークンなしの起票"); res.code != 0 || !strings.Contains(res.stdout, "LOC-0001") {
		t.Fatalf("ローカルモードの new: %d %s %s", res.code, res.stdout, res.stderr)
	}
	if login, via := e.lastEvent("LOC-0001"); login != "boss" || via != "cli" {
		t.Errorf("起票の記録 = %s / %s, want boss / cli", login, via)
	}

	// 通常モード（チームのサーバ）: 同じ 127.0.0.1 でもトークンが要る
	n := newEnv(t)
	n.user("boss", "boss-password-12", "admin")
	n.project("loc")
	if res, _ := n.get(n.client(), "/im/healthz"); res.StatusCode != http.StatusOK || res.Header.Get("X-Looptrack-Local-Mode") != "" {
		t.Fatalf("通常モードの healthz: %d %q", res.StatusCode, res.Header.Get("X-Looptrack-Local-Mode"))
	}
	nrun := issueCLI(t, n, "loc", "", "LOOPTRACK_USAGE=0")
	for _, args := range [][]string{{"list"}, {"new", "通らない起票"}} {
		res := nrun(args...)
		if res.code == 0 || !strings.Contains(res.stderr, "アクセストークンがありません") {
			t.Errorf("通常モードの %v: %d %s %s", args, res.code, res.stdout, res.stderr)
		}
	}
	var n0 int
	if err := n.db.QueryRow(`SELECT COUNT(*) FROM issues`).Scan(&n0); err != nil {
		t.Fatal(err)
	}
	if n0 != 0 {
		t.Errorf("通常モードでイシューができた: %d 件", n0)
	}
}

func TestLocalModeWeb(t *testing.T) {
	e := newLocalEnv(t)
	localSetup(t, e)
	c := e.client()

	// ログインを経ずに画面が開く（管理者の名前で・ログアウトは出さない）
	res, body := e.get(c, "/im/")
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "boss") || strings.Contains(body, "/logout") {
		t.Fatalf("ハブ: %d %s", res.StatusCode, body)
	}
	for _, p := range []string{"/im/login", "/im/login/totp", "/im/login/totp/setup"} {
		if res, _ := e.get(c, p); res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/im/" {
			t.Errorf("GET %s: %d %s, want /im/ へ", p, res.StatusCode, res.Header.Get("Location"))
		}
	}
	if res, _ := e.get(c, "/im/admin/users"); res.StatusCode != http.StatusOK {
		t.Errorf("利用者管理: %d", res.StatusCode)
	}

	// 画面の変更は CSRF を検査する（トークンなしは 403・画面のトークンなら通る）
	_, page := e.get(c, "/im/account")
	if res, _ := e.post(c, "/im/account/tokens", url.Values{"name": {"x"}, "days": {"30"}}); res.StatusCode != http.StatusForbidden {
		t.Errorf("CSRF なしのトークン発行: %d, want 403", res.StatusCode)
	}
	res, body = e.post(c, "/im/account/tokens", url.Values{"csrf": {csrfOf(t, page)}, "name": {"ローカル"}, "days": {"30"}})
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "ローカル") {
		t.Errorf("トークン発行: %d", res.StatusCode)
	}
	// ログアウトは何もしない（次の画面で自動的に入り直すため）
	if res, _ := e.post(c, "/im/logout", url.Values{"csrf": {csrfOf(t, page)}}); res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/im/" {
		t.Errorf("ログアウト: %d %s", res.StatusCode, res.Header.Get("Location"))
	}

	// 画面の API 呼び出し（Cookie + X-CSRF-Token）は経路 web で記録する。Cookie があって CSRF が無ければ 403
	e.do(e.client(), "POST", "/im/api/v1/projects/loc/issues", `{"title":"画面から"}`, "Content-Type", "application/json")
	res, body = e.do(c, "POST", "/im/api/v1/issues/LOC-0001/comments", `{"text":"画面から"}`,
		"Content-Type", "application/json", "X-CSRF-Token", csrfOf(t, page), "Origin", e.srv.URL)
	if res.StatusCode >= 300 {
		t.Errorf("画面からのコメント: %d %s", res.StatusCode, body)
	} else if login, via := e.lastEvent("LOC-0001"); login != "boss" || via != "web" {
		t.Errorf("画面からの変更の記録 = %s / %s, want boss / web", login, via)
	}
	if res, _ := e.do(c, "POST", "/im/api/v1/issues/LOC-0001/comments", `{"text":"x"}`, "Content-Type", "application/json"); res.StatusCode != http.StatusForbidden {
		t.Errorf("Cookie があって CSRF なしの API: %d, want 403", res.StatusCode)
	}
}

func TestLocalModeMCP(t *testing.T) {
	e := newLocalEnv(t)
	localSetup(t, e)

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: e.srv.URL + "/im/mcp", HTTPClient: &http.Client{Transport: headerTransport{map[string]string{"X-Looptrack-Project": "loc"}}},
		MaxRetries: -1, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("トークンなしの MCP 接続: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	m := &mcpClient{t: t, cs: cs}
	m.call("create_issue", map[string]any{"title": "MCP からの起票"}, false)
	if login, via := e.lastEvent("LOC-0001"); login != "boss" || via != "mcp" {
		t.Errorf("MCP の記録 = %s / %s, want boss / mcp", login, via)
	}
}

// ローカルモードの Host・Origin の検査（DNS rebinding・別サイトからの CSRF）。
func TestLocalModeCrossSite(t *testing.T) {
	e := newLocalEnv(t)
	localSetup(t, e)
	c := e.client()
	issue := `{"title":"別サイトから"}`

	// Host が loopback の名前でなければ、読み取りも含めて拒否（DNS rebinding）
	for _, host := range []string{"evil.example", "evil.example:8090", "127.0.0.1.nip.io", "192.168.1.10:8090"} {
		for _, p := range []string{"/im/", "/im/api/v1/me", "/im/healthz"} {
			if res, _ := e.do(c, "GET", p, "", "Host", host); res.StatusCode != http.StatusMisdirectedRequest {
				t.Errorf("Host %s GET %s: %d, want 421", host, p, res.StatusCode)
			}
		}
	}
	for _, host := range []string{"localhost:1234", "[::1]:1234", "127.0.0.1", "LOCALHOST"} {
		if res, _ := e.do(c, "GET", "/im/api/v1/me", "", "Host", host); res.StatusCode != http.StatusOK {
			t.Errorf("Host %s: %d, want 200", host, res.StatusCode)
		}
	}

	// 変更系は別オリジンを拒否（API・画面・MCP）
	for _, h := range [][]string{
		{"Origin", "http://evil.example"},
		{"Origin", "null"},
		{"Origin", "http://localhost:1"}, // 同じ機械の別のポート（別のアプリ）
		{"Sec-Fetch-Site", "cross-site"},
		{"Sec-Fetch-Site", "same-site"},
	} {
		if res, _ := e.do(c, "POST", "/im/api/v1/projects/loc/issues", issue, append([]string{"Content-Type", "text/plain"}, h...)...); res.StatusCode != http.StatusForbidden {
			t.Errorf("API %v: %d, want 403", h, res.StatusCode)
		}
		if res, _ := e.do(c, "POST", "/im/mcp", mcpInitBody, append([]string{"Content-Type", "application/json"}, h...)...); res.StatusCode != http.StatusForbidden {
			t.Errorf("MCP %v: %d, want 403", h, res.StatusCode)
		}
		if res, _ := e.do(c, "POST", "/im/account/tokens", "name=x&days=30", append([]string{"Content-Type", "application/x-www-form-urlencoded"}, h...)...); res.StatusCode != http.StatusForbidden {
			t.Errorf("画面 %v: %d, want 403", h, res.StatusCode)
		}
	}
	var n int
	e.db.QueryRow("SELECT COUNT(*) FROM issues").Scan(&n)
	if n != 0 {
		t.Errorf("別サイトからの起票が %d 件通った", n)
	}
	// 読み取り（GET）は Origin があっても通す（応答は CORS が無いので別サイトからは読めない）
	if res, _ := e.get(c, "/im/api/v1/me", "Origin", "http://evil.example"); res.StatusCode != http.StatusOK {
		t.Errorf("別オリジンの GET: %d", res.StatusCode)
	}
	// 通常モードでは Host を見ない（前段の Nginx が Host を決める）
	n2 := newEnv(t)
	if res, _ := n2.do(n2.client(), "GET", "/im/healthz", "", "Host", "evil.example"); res.StatusCode != http.StatusOK {
		t.Errorf("通常モードの Host: %d", res.StatusCode)
	}
}
