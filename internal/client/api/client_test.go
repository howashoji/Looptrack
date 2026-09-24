package api

import (
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/i18n"
)

// テストは env.FromMap の環境だけを使い、OS の環境変数（LOOPTRACK_API_URL・トークン）を読まない（本番に書き込まない）。

type recorded struct {
	Method, Path, Query, Body string
	Header                    http.Header
}

type fake struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []recorded
}

func newFake(t *testing.T, h func(w http.ResponseWriter, r *http.Request, n int)) *fake {
	t.Helper()
	f := &fake{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, recorded{r.Method, r.URL.Path, r.URL.RawQuery, string(b), r.Header.Clone()})
		n := len(f.reqs)
		f.mu.Unlock()
		h(w, r, n)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fake) requests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.reqs...)
}

func jsonReply(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	io.WriteString(w, body)
}

func newClient(t *testing.T, m map[string]string) *Client {
	t.Helper()
	home := t.TempDir()
	base := map[string]string{"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "APPDATA": filepath.Join(home, "AppData")}
	for k, v := range m {
		base[k] = v
	}
	c, err := New(env.FromMap(base))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestHeadersAndDecode(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, n int) {
		jsonReply(w, 200, `{"z":1,"a":"\u003cb\u003e","n":1.0}`)
	})
	c := newClient(t, map[string]string{"LOOPTRACK_API_URL": f.URL + "/im/api/v1/", "LOOPTRACK_TOKEN": " imp_t ", "CLAUDE_CODE_SESSION_ID": "cc-1"})
	if c.BaseURL != f.URL+"/im" {
		t.Fatalf("URL の正規化: %s", c.BaseURL)
	}
	res, err := c.Do(Request{Method: "POST", Path: "/projects/demo/x?a=1", Body: jsonorder.NewObject().Set("title", "日本語")})
	if err != nil {
		t.Fatal(err)
	}
	if got := jsonorder.Compact(res.Value); got != `{"z": 1, "a": "<b>", "n": 1.0}` {
		t.Errorf("応答の順・形: %s", got)
	}
	q := f.requests()[0]
	if q.Path != "/im/api/v1/projects/demo/x" || q.Query != "a=1" || q.Body != `{"title": "日本語"}` {
		t.Errorf("要求: %+v", q)
	}
	for k, want := range map[string]string{"Authorization": "Bearer imp_t", "X-Looptrack-Client": "cli", "Accept": "application/json",
		"Content-Type": "application/json; charset=utf-8", "X-Looptrack-Session": "cc-1", "X-Looptrack-Agent": ""} {
		if got := q.Header.Get(k); got != want {
			t.Errorf("%s: %q（期待 %q）", k, got, want)
		}
	}
	if !strings.HasPrefix(q.Header.Get("User-Agent"), "looptrack/") {
		t.Errorf("User-Agent: %s", q.Header.Get("User-Agent"))
	}
}

// TestLooptrackEnvOnly は LOOPTRACK_* だけを読み、旧名 IM_* を読まないこと（旧名のフォールバックは製品に残さないと決めた）。
func TestLooptrackEnvOnly(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, n int) { jsonReply(w, 200, `{}`) })
	// 取得元のラベルを日本語で検査するので言語を固定する（指定が無いと端末の LANG に従う）
	c := newClient(t, map[string]string{"LOOPTRACK_API_URL": f.URL + "/im", "IM_API_URL": "https://example.com/im",
		"LOOPTRACK_TOKEN": "imp_new", "IM_TOKEN": "imp_old", "LOOPTRACK_LANG": "ja"})
	if _, err := c.Get("/me"); err != nil {
		t.Fatal(err)
	}
	if got := f.requests()[0].Header.Get("Authorization"); got != "Bearer imp_new" {
		t.Errorf("LOOPTRACK_TOKEN を使わない: %s", got)
	}
	_, src, _ := c.TokenSource(c.BaseURL)
	if src != "環境変数 LOOPTRACK_TOKEN" {
		t.Errorf("取得元: %s", src)
	}
}

func TestErrors(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, n int) {
		switch r.URL.Path {
		case "/im/api/v1/e404":
			jsonReply(w, 404, `{"error":{"code":"not_found","message":"イシューが見つかりません: X"}}`)
		case "/im/api/v1/text500":
			w.WriteHeader(502)
			io.WriteString(w, "bad gateway")
		case "/im/api/v1/text":
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
			io.WriteString(w, "# 見出し\n")
		case "/im/api/v1/broken":
			jsonReply(w, 200, `{"a":`)
		}
	})
	c := newClient(t, map[string]string{"LOOPTRACK_API_URL": f.URL + "/im", "LOOPTRACK_TOKEN": "t"})
	_, err := c.Get("/e404")
	var ae *Error
	if !errors.As(err, &ae) || ae.Status != 404 || ae.Code != "not_found" || ae.Message != "イシューが見つかりません: X" {
		t.Errorf("404: %#v", err)
	}
	_, err = c.Get("/text500")
	if !errors.As(err, &ae) || ae.Status != 502 || ae.Message != "HTTP 502" {
		t.Errorf("本文が JSON でない 502: %#v", err)
	}
	if v, err := c.Get("/text"); err != nil || v != "# 見出し\n" {
		t.Errorf("Markdown: %#v %v", v, err)
	}
	if v, err := c.Get("/broken"); err != nil || jsonorder.Compact(v) != "{}" {
		t.Errorf("壊れた JSON は空のオブジェクト: %#v %v", v, err)
	}
	f.Close()
	_, err = c.Get("/e404")
	var ce *ConnError
	if !errors.As(err, &ce) || !strings.HasPrefix(err.Error(), "サーバに接続できません（"+c.BaseURL+"）: ") {
		t.Errorf("接続できない: %v", err)
	}
}

func TestTimeout(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, n int) {
		time.Sleep(700 * time.Millisecond)
		jsonReply(w, 200, `{}`)
	})
	c := newClient(t, map[string]string{"LOOPTRACK_API_URL": f.URL, "LOOPTRACK_TOKEN": "t", "LOOPTRACK_TIMEOUT": "0.2"})
	start := time.Now()
	_, err := c.Get("/slow")
	if err == nil || !strings.HasSuffix(err.Error(), ": timed out") || time.Since(start) > 600*time.Millisecond {
		t.Errorf("時間切れ: %v（%s）", err, time.Since(start))
	}
	if _, err := New(env.FromMap(map[string]string{"LOOPTRACK_TIMEOUT": "abc", "HOME": t.TempDir()})); err == nil {
		t.Error("数でない LOOPTRACK_TIMEOUT を受け付けた")
	}
}

// TestNoToken は、ローカルモードでないサーバに資格情報なしで呼ぶと、従来どおり要求を送らずに案内で終えること
// （ループバックでない URL は名乗りも引かない。ループバックでも、サーバがいなければ・名乗らなければ同じ）。
func TestNoToken(t *testing.T) {
	setEntryRoot(t, t.TempDir())
	plain := newFake(t, func(w http.ResponseWriter, r *http.Request, n int) { // 通常モードのサーバ（名乗らない）
		if r.URL.Path == "/im/healthz" {
			io.WriteString(w, "ok\n")
			return
		}
		jsonReply(w, 401, `{"error":{"code":"unauthorized","message":"認証が必要です"}}`)
	})
	const want = "アクセストークンがありません。looptrack issue login --browser でログインしてください（%s）"
	for _, url := range []string{"http://127.0.0.1:1/im", "http://looptrack.invalid/im", "https://127.0.0.1:1/im", plain.URL + "/im"} {
		c := newClient(t, map[string]string{"LOOPTRACK_API_URL": url})
		_, err := c.Get("/me")
		var ne *NoTokenError
		if !errors.As(err, &ne) || err.Error() != fmt.Sprintf(want, url) {
			t.Errorf("トークンなし（%s）: %v", url, err)
		}
	}
	for _, q := range plain.requests() { // 名乗りを引くだけで、API は呼ばない
		if q.Path != "/im/healthz" {
			t.Errorf("通常モードのサーバに送った: %+v", q)
		}
	}
}

// TestLocalModeWithoutToken は、ローカルモードと名乗るサーバ（/healthz の X-Looptrack-Local-Mode: 1）には、
// 資格情報が無くても Authorization を付けずに呼べること（画面・MCP と同じ扱い）。
func TestLocalModeWithoutToken(t *testing.T) {
	setEntryRoot(t, t.TempDir())
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if r.URL.Path == "/looptrack/healthz" {
			w.Header().Set(LocalModeHeader, "1")
			io.WriteString(w, "ok\n")
			return
		}
		if r.Header.Get("Authorization") != "" { // ローカルモードのサーバは Authorization を見ない
			jsonReply(w, 400, `{"error":{"code":"bad","message":"Authorization が付いている"}}`)
			return
		}
		jsonReply(w, 200, `{"id":"MAIN-0001"}`)
	})
	c := newClient(t, map[string]string{"LOOPTRACK_API_URL": f.URL + "/looptrack"})
	for i := range 2 {
		res, err := c.Do(Request{Method: "POST", Path: "/projects/main/issues", Body: jsonorder.NewObject().Set("title", "起票")})
		if err != nil {
			t.Fatalf("%d 回目: ローカルモードで呼べない: %v", i+1, err)
		}
		if got := jsonorder.Compact(res.Value); got != `{"id": "MAIN-0001"}` {
			t.Errorf("応答: %s", got)
		}
	}
	reqs := f.requests()
	if len(reqs) != 3 || reqs[0].Path != "/looptrack/healthz" { // 名乗りは 1 回だけ引いて覚える
		t.Fatalf("要求: %+v", reqs)
	}
	for _, q := range reqs[1:] {
		if _, ok := q.Header["Authorization"]; ok {
			t.Errorf("Authorization を付けた: %q", q.Header.Get("Authorization"))
		}
		if q.Path != "/looptrack/api/v1/projects/main/issues" || q.Header.Get("X-Looptrack-Client") != "cli" || q.Body != `{"title": "起票"}` {
			t.Errorf("要求: %+v", q)
		}
	}
}

// TestLocalModeTeamServerStill401 は、ローカルモードと名乗ったサーバでも 401 を返せば従来の案内で終えること
// （認証が緩むのは、実際に認証を省いているサーバに対してだけ。401 を握りつぶして続けない）。
func TestLocalModeTeamServerStill401(t *testing.T) {
	setEntryRoot(t, t.TempDir())
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if r.URL.Path == "/im/healthz" {
			w.Header().Set(LocalModeHeader, "1")
			io.WriteString(w, "ok\n")
			return
		}
		jsonReply(w, 401, `{"error":{"code":"unauthorized","message":"認証が必要です（Authorization: Bearer <アクセストークン> を指定してください）"}}`)
	})
	c := newClient(t, map[string]string{"LOOPTRACK_API_URL": f.URL + "/im"})
	_, err := c.Do(Request{Method: "POST", Path: "/projects/im/issues", Body: jsonorder.NewObject().Set("title", "起票")})
	var ne *NoTokenError
	if !errors.As(err, &ne) || err.Error() != "アクセストークンがありません。looptrack issue login --browser でログインしてください（"+f.URL+"/im）" {
		t.Fatalf("401 を返すサーバ: %v", err)
	}
	if n := len(f.requests()); n != 2 { // 名乗り + 1 回だけ（401 でやり直さない）
		t.Errorf("要求の回数: %d", n)
	}
}

// TestLocalModeNotProbedWithToken は、トークンがあるときは名乗りを引かず、従来どおり Authorization を付けること。
func TestLocalModeNotProbedWithToken(t *testing.T) {
	setEntryRoot(t, t.TempDir())
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, n int) { jsonReply(w, 200, `{}`) })
	c := newClient(t, map[string]string{"LOOPTRACK_API_URL": f.URL + "/looptrack", "LOOPTRACK_TOKEN": "imp_t"})
	if _, err := c.Get("/me"); err != nil {
		t.Fatal(err)
	}
	reqs := f.requests()
	if len(reqs) != 1 || reqs[0].Path != "/looptrack/api/v1/me" || reqs[0].Header.Get("Authorization") != "Bearer imp_t" {
		t.Errorf("要求: %+v", reqs)
	}
}

// TestLocalURL は、名乗りを引く前の足切り（この機械のループバックの http だけ）。
func TestLocalURL(t *testing.T) {
	for _, u := range []string{"http://127.0.0.1:18090/looptrack", "http://localhost:8090/im", "http://LOCALHOST/im", "http://[::1]:18090/looptrack", "http://127.0.0.1"} {
		if !LocalURL(u) {
			t.Errorf("ループバックのはず: %s", u)
		}
	}
	for _, u := range []string{"https://127.0.0.1:18090/looptrack", "http://127.0.0.2:18090/looptrack", "http://192.168.0.2/im",
		"http://looptrack.example.com/im", "https://example.com/im", "", "://"} {
		if LocalURL(u) {
			t.Errorf("ループバックではないはず: %s", u)
		}
	}
}

func setEntryRoot(t *testing.T, root string) {
	t.Helper()
	SetRoot(root)
	t.Cleanup(func() { SetRoot("") })
}

// CLI の呼び方は looptrack issue だけ（以前の入口を廃止したので、入口の有無で変えない）。
func TestEntryCommand(t *testing.T) {
	root := t.TempDir()
	setEntryRoot(t, root)
	if got := EntryCommand(); got != "looptrack issue" {
		t.Errorf("EntryCommand: %q", got)
	}
	if got := Relogin(i18n.JA); got != "looptrack issue login --browser でログインし直してください" {
		t.Errorf("Relogin: %q", got)
	}
}

// 資格情報のファイルのトークン: 期限前の取り直し・401 での取り直し・更新トークンの失効。
func storeWith(t *testing.T, c *Client, url, entry string) {
	t.Helper()
	unlock, err := c.Creds.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	o, err := jsonorder.DecodeObject([]byte(entry))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Creds.SaveEntry(url, o); err != nil {
		t.Fatal(err)
	}
}

func oauthHandler(t *testing.T, tokenStatus int, tokenBody string, apiFirst401 bool) func(http.ResponseWriter, *http.Request, int) {
	var mu sync.Mutex
	api401 := apiFirst401
	return func(w http.ResponseWriter, r *http.Request, n int) {
		if r.URL.Path == "/im/oauth/token" {
			jsonReply(w, tokenStatus, tokenBody)
			return
		}
		mu.Lock()
		first := api401
		api401 = false
		mu.Unlock()
		if first {
			jsonReply(w, 401, `{"error":{"code":"unauthorized","message":"トークンが失効しているか期限切れです"}}`)
			return
		}
		jsonReply(w, 200, `{"login":"alice"}`)
	}
}

func TestRefreshBeforeExpiry(t *testing.T) {
	f := newFake(t, oauthHandler(t, 200, `{"access_token":"imp_new","refresh_token":"imr_2","expires_in":3600}`, false))
	c := newClient(t, map[string]string{"LOOPTRACK_API_URL": f.URL + "/im"})
	c.Now = func() time.Time { return time.Unix(2000, 0) }
	storeWith(t, c, c.BaseURL, `{"token":"imp_old","refresh_token":"imr_1","client_id":"cid","expires_at":1000,"login":"alice"}`)
	if _, err := c.Get("/me"); err != nil {
		t.Fatal(err)
	}
	reqs := f.requests()
	if len(reqs) != 2 || reqs[0].Path != "/im/oauth/token" || reqs[1].Header.Get("Authorization") != "Bearer imp_new" {
		t.Fatalf("期限前に取り直していない: %+v", reqs)
	}
	for _, want := range []string{"grant_type=refresh_token", "refresh_token=imr_1", "client_id=cid", "resource=" + strings.ReplaceAll(strings.ReplaceAll(f.URL, ":", "%3A"), "/", "%2F") + "%2Fim%2Fapi%2Fv1"} {
		if !strings.Contains(reqs[0].Body, want) {
			t.Errorf("取り直しの要求に %s が無い: %s", want, reqs[0].Body)
		}
	}
	e, _ := c.Creds.Entry(c.BaseURL)
	if got := jsonorder.Compact(e); got != `{"token": "imp_new", "refresh_token": "imr_2", "client_id": "cid", "login": "alice", "expires_at": 5600}` {
		t.Errorf("保存した値（以前の CLI と同じ順）: %s", got)
	}
}

func TestNoRefreshWhenFar(t *testing.T) {
	f := newFake(t, oauthHandler(t, 200, `{}`, false))
	c := newClient(t, map[string]string{"LOOPTRACK_API_URL": f.URL + "/im"})
	c.Now = func() time.Time { return time.Unix(1000, 0) }
	storeWith(t, c, c.BaseURL, fmt.Sprintf(`{"token":"imp_old","refresh_token":"imr_1","client_id":"cid","expires_at":%d}`, 1000+2*86400))
	if _, err := c.Get("/me"); err != nil {
		t.Fatal(err)
	}
	if reqs := f.requests(); len(reqs) != 1 || reqs[0].Header.Get("Authorization") != "Bearer imp_old" {
		t.Errorf("期限が遠いのに取り直した: %+v", reqs)
	}
}

func TestRefreshOn401(t *testing.T) {
	f := newFake(t, oauthHandler(t, 200, `{"access_token":"imp_new","expires_in":3600}`, true))
	c := newClient(t, map[string]string{"LOOPTRACK_API_URL": f.URL + "/im"})
	storeWith(t, c, c.BaseURL, `{"token":"imp_old","refresh_token":"imr_1","client_id":"cid"}`)
	v, err := c.Get("/me")
	if err != nil {
		t.Fatal(err)
	}
	if jsonorder.Compact(v) != `{"login": "alice"}` {
		t.Errorf("やり直しの結果: %s", jsonorder.Compact(v))
	}
	reqs := f.requests()
	if len(reqs) != 3 || reqs[2].Header.Get("Authorization") != "Bearer imp_new" {
		t.Fatalf("401 の後に取り直してやり直していない: %+v", reqs)
	}
	e, _ := c.Creds.Entry(c.BaseURL)
	if e.Has("refresh_token") {
		t.Error("応答に無い更新トークンを残した")
	}
}

func TestRefreshInvalid(t *testing.T) {
	f := newFake(t, oauthHandler(t, 400, `{"error":"invalid_grant","error_description":"更新トークンが無効です"}`, true))
	c := newClient(t, map[string]string{"LOOPTRACK_API_URL": f.URL + "/im"})
	storeWith(t, c, c.BaseURL, `{"token":"imp_old","refresh_token":"imr_1","client_id":"cid"}`)
	_, err := c.Get("/me")
	var ae *Error
	if !errors.As(err, &ae) || ae.Status != 401 || ae.Message != "ログインの有効期限が切れました（保存した更新トークンが期限切れか失効しています）" {
		t.Fatalf("失効: %v", err)
	}
	e, _ := c.Creds.Entry(c.BaseURL)
	if e.Has("refresh_token") || e.String("token") != "imp_old" {
		t.Errorf("失効した更新トークンを消していない: %s", jsonorder.Compact(e))
	}
}

func TestRefreshDownKeepsOriginalError(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if r.URL.Path == "/im/oauth/token" {
			hj, _ := w.(http.Hijacker)
			conn, _, _ := hj.Hijack()
			conn.Close()
			return
		}
		jsonReply(w, 401, `{"error":{"code":"unauthorized","message":"トークンが失効しているか期限切れです"}}`)
	})
	c := newClient(t, map[string]string{"LOOPTRACK_API_URL": f.URL + "/im"})
	storeWith(t, c, c.BaseURL, `{"token":"imp_old","refresh_token":"imr_1","client_id":"cid"}`)
	_, err := c.Get("/me")
	var ae *Error
	if !errors.As(err, &ae) || ae.Message != "トークンが失効しているか期限切れです" {
		t.Fatalf("取り直せないときは元の 401: %v", err)
	}
	e, _ := c.Creds.Entry(c.BaseURL)
	if e.String("refresh_token") != "imr_1" {
		t.Error("接続できないだけで更新トークンを消した")
	}
}

// 他のプロセスが先に取り直していれば、その値を使う（使用済みの更新トークンを出さない）。
func TestRefreshUsesOtherProcessResult(t *testing.T) {
	f := newFake(t, oauthHandler(t, 500, `{}`, true))
	c := newClient(t, map[string]string{"LOOPTRACK_API_URL": f.URL + "/im"})
	storeWith(t, c, c.BaseURL, `{"token":"imp_other","refresh_token":"imr_2","client_id":"cid"}`)
	fresh, why, err := c.refresh(c.BaseURL, "imp_used_by_me")
	if err != nil || fresh != "imp_other" || why != "" {
		t.Errorf("他のプロセスの結果を使わない: %q %q %v", fresh, why, err)
	}
}

func TestExplicitTokenIsNotManaged(t *testing.T) {
	f := newFake(t, oauthHandler(t, 200, `{"access_token":"imp_new"}`, true))
	c := newClient(t, map[string]string{"LOOPTRACK_API_URL": f.URL + "/im", "LOOPTRACK_TOKEN": "imp_env"})
	storeWith(t, c, c.BaseURL, `{"token":"imp_env","refresh_token":"imr_1","client_id":"cid","expires_at":0}`)
	_, err := c.Get("/me")
	var ae *Error
	if !errors.As(err, &ae) || ae.Status != 401 || len(f.requests()) != 1 {
		t.Errorf("LOOPTRACK_TOKEN のときに取り直した: %v %d", err, len(f.requests()))
	}
}

// SSL_CERT_FILE の CA で検証する（社内の CA・プロキシ）。無ければ検証に失敗する（検証を無効にしない）。
func TestTLSRespectsSSLCertFile(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { jsonReply(w, 200, `{"ok":true}`) }))
	defer srv.Close()
	c := newClient(t, map[string]string{"LOOPTRACK_API_URL": srv.URL, "LOOPTRACK_TOKEN": "t"})
	if _, err := c.Get("/x"); err == nil {
		t.Fatal("知らない CA の証明書を受け入れた")
	}
	dir := t.TempDir()
	pemFile := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(pemFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o644); err != nil {
		t.Fatal(err)
	}
	c = newClient(t, map[string]string{"LOOPTRACK_API_URL": srv.URL, "LOOPTRACK_TOKEN": "t", "SSL_CERT_FILE": pemFile})
	if _, err := c.Get("/x"); err != nil {
		t.Errorf("SSL_CERT_FILE の CA で検証しない: %v", err)
	}
	certDir := filepath.Join(dir, "certs")
	os.Mkdir(certDir, 0o755)
	os.Rename(pemFile, filepath.Join(certDir, "ca.pem"))
	c = newClient(t, map[string]string{"LOOPTRACK_API_URL": srv.URL, "LOOPTRACK_TOKEN": "t", "SSL_CERT_DIR": certDir})
	if _, err := c.Get("/x"); err != nil {
		t.Errorf("SSL_CERT_DIR の CA で検証しない: %v", err)
	}
}

func TestPathEscapeLikeQuote(t *testing.T) {
	if got := PathEscape("a b/c:d@é~_.-"); got != "a%20b%2Fc%3Ad%40%C3%A9~_.-" {
		t.Errorf("quote(safe=''): %s", got)
	}
	if NormalizeURL(" https://x/im/api/v1/ ") != "https://x/im" {
		t.Error("NormalizeURL")
	}
}

// 利用者の言語をサーバへ伝える。送らないとサーバは既定の英語を選ぶので、
// 日本語の環境なのに英語のエラーが返る。
func TestAcceptLanguage(t *testing.T) {
	ok := func(w http.ResponseWriter, r *http.Request, n int) { jsonReply(w, 200, `{}`) }
	for _, tc := range []struct{ name, lang, want string }{
		{name: "日本語の環境", lang: "ja", want: "ja"},
		{name: "英語の環境", lang: "en", want: "en"},
		{name: "指定が無ければ英語（サーバの既定と同じ）", lang: "", want: "en"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t, ok)
			m := map[string]string{"LOOPTRACK_API_URL": f.URL, "LOOPTRACK_TOKEN": "t"}
			if tc.lang != "" {
				m["LOOPTRACK_LANG"] = tc.lang
			}
			if _, err := newClient(t, m).Do(Request{Method: "GET", Path: "/x"}); err != nil {
				t.Fatal(err)
			}
			if got := f.requests()[0].Header.Get("Accept-Language"); got != tc.want {
				t.Errorf("Accept-Language が %q（期待 %q）", got, tc.want)
			}
		})
	}
	t.Run("端末の LANG からも決まる", func(t *testing.T) {
		f := newFake(t, ok)
		c := newClient(t, map[string]string{"LOOPTRACK_API_URL": f.URL, "LOOPTRACK_TOKEN": "t", "LANG": "ja_JP.UTF-8"})
		if _, err := c.Do(Request{Method: "GET", Path: "/x"}); err != nil {
			t.Fatal(err)
		}
		if got := f.requests()[0].Header.Get("Accept-Language"); got != "ja" {
			t.Errorf("Accept-Language が %q（期待 ja）", got)
		}
	})
	t.Run("呼び出し側が明示したヘッダは上書きしない", func(t *testing.T) {
		f := newFake(t, ok)
		c := newClient(t, map[string]string{"LOOPTRACK_API_URL": f.URL, "LOOPTRACK_TOKEN": "t", "LOOPTRACK_LANG": "ja"})
		r := Request{Method: "GET", Path: "/x", Headers: map[string]string{"Accept-Language": "en"}}
		if _, err := c.Do(r); err != nil {
			t.Fatal(err)
		}
		if got := f.requests()[0].Header.Get("Accept-Language"); got != "en" {
			t.Errorf("Accept-Language が %q（期待 en。呼び出し側の明示を尊重する）", got)
		}
	})
}

// TestLangHeaderOnlyWhenExplicit は、**LOOPTRACK_LANG が明示されているときだけ** X-Looptrack-Lang を
// 送ること。環境変数が無いのに送ると、サーバから見て「常に明示」になり、サーバに保存した利用者の
// 設定が CLI に一度も効かなくなる（Accept-Language は環境変数が無くても en を送るので、
// あちらでは明示かどうかを見分けられない）。
func TestLangHeaderOnlyWhenExplicit(t *testing.T) {
	ok := func(w http.ResponseWriter, r *http.Request, n int) { jsonReply(w, 200, `{}`) }
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string // 空文字はヘッダを送らないこと
		// accept は従来どおりの Accept-Language（この実装で変えていないことを同時に見る）
		accept string
	}{
		{name: "LOOPTRACK_LANG=ja なら送る", env: map[string]string{"LOOPTRACK_LANG": "ja"}, want: "ja", accept: "ja"},
		{name: "LOOPTRACK_LANG=en なら送る", env: map[string]string{"LOOPTRACK_LANG": "en"}, want: "en", accept: "en"},
		{name: "環境変数が無ければ送らない", env: nil, want: "", accept: "en"},
		{name: "LANG だけなら送らない（端末の既定は明示ではない）", env: map[string]string{"LANG": "ja_JP.UTF-8"}, want: "", accept: "ja"},
		{name: "LC_ALL だけなら送らない", env: map[string]string{"LC_ALL": "ja_JP.UTF-8"}, want: "", accept: "ja"},
		{name: "読めない LOOPTRACK_LANG は送らない", env: map[string]string{"LOOPTRACK_LANG": "zz"}, want: "", accept: "en"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t, ok)
			m := map[string]string{"LOOPTRACK_API_URL": f.URL, "LOOPTRACK_TOKEN": "t"}
			for k, v := range tc.env {
				m[k] = v
			}
			if _, err := newClient(t, m).Do(Request{Method: "GET", Path: "/x"}); err != nil {
				t.Fatal(err)
			}
			h := f.requests()[0].Header
			if got := h.Get("X-Looptrack-Lang"); got != tc.want {
				t.Errorf("X-Looptrack-Lang が %q（期待 %q）", got, tc.want)
			}
			if got := h.Get("Accept-Language"); got != tc.accept {
				t.Errorf("Accept-Language が %q（期待 %q。従来の送り方は変えない）", got, tc.accept)
			}
		})
	}
	t.Run("呼び出し側が明示したヘッダは上書きしない", func(t *testing.T) {
		f := newFake(t, ok)
		c := newClient(t, map[string]string{"LOOPTRACK_API_URL": f.URL, "LOOPTRACK_TOKEN": "t", "LOOPTRACK_LANG": "ja"})
		r := Request{Method: "GET", Path: "/x", Headers: map[string]string{"X-Looptrack-Lang": "en"}}
		if _, err := c.Do(r); err != nil {
			t.Fatal(err)
		}
		if got := f.requests()[0].Header.Get("X-Looptrack-Lang"); got != "en" {
			t.Errorf("X-Looptrack-Lang が %q（期待 en。呼び出し側の明示を尊重する）", got)
		}
	})
}

// こちら側で作るエラーは利用者の言語で出る。サーバ由来の文面（Error.Message）は
// サーバが Accept-Language で言語を決めるので、ここでは訳さない。
func TestClientErrorsAreTranslated(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		ja, en string
	}{
		{name: "接続できない", err: &ConnError{URL: "http://x/api", Reason: "connection refused"},
			ja: "サーバに接続できません（http://x/api）: connection refused",
			en: "Cannot reach the server (http://x/api): connection refused"},
		{name: "トークンが無い", err: &NoTokenError{URL: "http://x/api"},
			ja: "アクセストークンがありません。looptrack issue login --browser でログインしてください（http://x/api）",
			en: "No access token. Log in with looptrack issue login --browser (http://x/api)"},
		{name: "待ち時間が秒数でない", err: &TimeoutError{Var: "LOOPTRACK_TIMEOUT", Value: "abc"},
			ja: "LOOPTRACK_TIMEOUT は秒数で指定してください（指定値: abc）",
			en: "LOOPTRACK_TIMEOUT must be a number of seconds (given: abc)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := i18n.Text(i18n.JA, tc.err); got != tc.ja {
				t.Errorf("ja: %q（期待 %q）", got, tc.ja)
			}
			if got := i18n.Text(i18n.EN, tc.err); got != tc.en {
				t.Errorf("en: %q（期待 %q）", got, tc.en)
			}
		})
	}
	t.Run("更新トークンの期限切れ", func(t *testing.T) {
		e := &Error{Status: 401, Msg: i18n.M("api.err.refresh_expired")}
		if got := e.Text(i18n.JA); got != "ログインの有効期限が切れました（保存した更新トークンが期限切れか失効しています）" {
			t.Errorf("ja: %q", got)
		}
		if got := e.Text(i18n.EN); got != "Your login has expired (the saved refresh token has expired or was revoked)" {
			t.Errorf("en: %q", got)
		}
	})
	t.Run("サーバ由来の文面はそのまま出す", func(t *testing.T) {
		e := &Error{Status: 404, Message: "イシューが見つかりません: TST-0001"}
		if got := e.Text(i18n.EN); got != "イシューが見つかりません: TST-0001" {
			t.Errorf("サーバの文面を書き換えてはいけない: %q", got)
		}
	})
	t.Run("ログインし直す案内", func(t *testing.T) {
		if got := Relogin(i18n.JA); got != "looptrack issue login --browser でログインし直してください" {
			t.Errorf("ja: %q", got)
		}
		if got := Relogin(i18n.EN); got != "log in again with looptrack issue login --browser" {
			t.Errorf("en: %q", got)
		}
	})
}
