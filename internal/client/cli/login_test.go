package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/howashoji/looptrack/internal/i18n"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/cred"
	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
)

// login を、OAuth を持つ偽のサーバ（認可コード + PKCE・動的登録・更新トークンの入れ替えと再利用の検知）に対して
// 通しで動かす。ブラウザの代わりは openBrowser の差し替え（authorize を開き、戻り先へ届ける）。
// OS に依らず動く（CI の 3 OS で実行される）。本物のサーバでの確認は internal/server の cli_login_go_test.go。

// fakeOAuth は偽のサーバ。
type fakeOAuth struct {
	t   *testing.T
	srv *httptest.Server

	mu         sync.Mutex
	clients    map[string]string  // client_id → 登録した戻り先
	codes      map[string]codeReq // 認可コード → 要求
	access     map[string]bool    // 有効なアクセストークン
	refresh    map[string]*rtok   // 更新トークン
	seq        int
	expiresIn  int
	registers  int
	refreshes  int
	reused     int  // 使用済みの更新トークンが出された回数（系列の失効）
	deny       bool // authorize で access_denied を返す
	authorizes int
}

type codeReq struct{ clientID, redirect, challenge string }

type rtok struct {
	family, access string
	used, revoked  bool
}

func newFakeOAuth(t *testing.T) *fakeOAuth {
	f := &fakeOAuth{t: t, clients: map[string]string{}, codes: map[string]codeReq{}, access: map[string]bool{},
		refresh: map[string]*rtok{}, expiresIn: 30 * 24 * 3600}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOAuth) base() string { return f.srv.URL + "/im" }

func (f *fakeOAuth) issue(family string) map[string]any {
	f.seq++
	at, rt := fmt.Sprintf("imo_access_%d", f.seq), fmt.Sprintf("imr_refresh_%d", f.seq)
	f.access[at] = true
	f.refresh[rt] = &rtok{family: family, access: at}
	return map[string]any{"access_token": at, "token_type": "Bearer", "expires_in": f.expiresIn, "refresh_token": rt}
}

// allow はアクセストークンを有効にする（貼る方式の値など）。
func (f *fakeOAuth) allow(tok string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.access[tok] = true
}

// counts は登録・更新・再利用の回数。
func (f *fakeOAuth) counts() (registers, refreshes, reused int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.registers, f.refreshes, f.reused
}

// revokeAccess はアカウント画面での失効の代わり（アクセストークンだけを無効にする。更新トークンは生きている）。
func (f *fakeOAuth) revokeAccess() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k := range f.access {
		f.access[k] = false
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (f *fakeOAuth) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := strings.TrimPrefix(r.URL.Path, "/im")
	switch {
	case p == "/.well-known/oauth-authorization-server":
		writeJSON(w, 200, map[string]any{"issuer": f.base(), "grant_types_supported": []string{"authorization_code", "refresh_token"}})
	case p == "/oauth/register" && r.Method == http.MethodPost:
		var reg struct {
			ClientName   string   `json:"client_name"`
			RedirectURIs []string `json:"redirect_uris"`
		}
		json.NewDecoder(r.Body).Decode(&reg)
		f.registers++
		id := fmt.Sprintf("cid_%d", f.registers)
		f.clients[id] = reg.RedirectURIs[0]
		writeJSON(w, 201, map[string]any{"client_id": id, "client_name": reg.ClientName})
	case p == "/oauth/authorize":
		q := r.URL.Query()
		f.authorizes++
		reg, ok := f.clients[q.Get("client_id")]
		if !ok {
			http.Error(w, "クライアントが登録されていません", 400)
			return
		}
		// ループバックはポートを問わない（RFC 8252 §7.3）
		ru, _ := url.Parse(q.Get("redirect_uri"))
		rr, _ := url.Parse(reg)
		if ru.Hostname() != "127.0.0.1" || ru.Path != rr.Path || q.Get("code_challenge_method") != "S256" ||
			q.Get("resource") != f.base()+"/api/v1" {
			http.Error(w, "不正な要求", 400)
			return
		}
		back := url.Values{"state": {q.Get("state")}}
		if f.deny {
			back.Set("error", "access_denied")
			back.Set("error_description", "利用者が拒否しました")
		} else {
			code := fmt.Sprintf("code_%d", len(f.codes)+1)
			f.codes[code] = codeReq{q.Get("client_id"), q.Get("redirect_uri"), q.Get("code_challenge")}
			back.Set("code", code)
		}
		http.Redirect(w, r, q.Get("redirect_uri")+"?"+back.Encode(), http.StatusFound)
	case p == "/oauth/token" && r.Method == http.MethodPost:
		r.ParseForm()
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			c, ok := f.codes[r.PostForm.Get("code")]
			delete(f.codes, r.PostForm.Get("code"))
			sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
			if !ok || c.clientID != r.PostForm.Get("client_id") || c.redirect != r.PostForm.Get("redirect_uri") ||
				base64.RawURLEncoding.EncodeToString(sum[:]) != c.challenge {
				writeJSON(w, 400, map[string]any{"error": "invalid_grant", "error_description": "認可コードが無効です"})
				return
			}
			writeJSON(w, 200, f.issue(fmt.Sprintf("fam_%d", f.seq+1)))
		case "refresh_token":
			f.refreshes++
			rt := f.refresh[r.PostForm.Get("refresh_token")]
			if rt == nil || rt.revoked {
				writeJSON(w, 400, map[string]any{"error": "invalid_grant", "error_description": "更新トークンが無効です"})
				return
			}
			if rt.used { // 再利用の検知: 系列ごと失効
				f.reused++
				for _, x := range f.refresh {
					if x.family == rt.family {
						x.revoked = true
						f.access[x.access] = false
					}
				}
				writeJSON(w, 400, map[string]any{"error": "invalid_grant", "error_description": "更新トークンが再利用されました"})
				return
			}
			rt.used = true
			f.access[rt.access] = false
			writeJSON(w, 200, f.issue(rt.family))
		default:
			writeJSON(w, 400, map[string]any{"error": "unsupported_grant_type"})
		}
	case strings.HasPrefix(p, "/api/v1/"):
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !f.access[tok] {
			writeJSON(w, 401, map[string]any{"error": map[string]any{"code": "unauthorized", "message": "トークンが無効です"}})
			return
		}
		if p == "/api/v1/me" {
			writeJSON(w, 200, map[string]any{"login": "alice", "name": "Alice", "role": "admin"})
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		io.WriteString(w, "# DEMO-0001 本文\n")
	default:
		http.NotFound(w, r)
	}
}

// browserVia は openBrowser の代わり（ログイン済みのブラウザ: authorize を開き、302 に従って戻り先へ届ける）。
func browserVia(t *testing.T, opened *[]string) func(env.Env, string) bool {
	return func(_ env.Env, u string) bool {
		*opened = append(*opened, u)
		go func() {
			if res, err := http.Get(u); err == nil { // 失敗はログインの打ち切り・誤りとして表に出る
				res.Body.Close()
			}
		}()
		return true
	}
}

// loginEnv は一時ディレクトリだけを指す環境（OS の IM_*・LOOPTRACK_*・HOME は読まない）。
func loginEnv(t *testing.T, base string) (map[string]string, string) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	os.MkdirAll(home, 0o755)
	m := map[string]string{
		"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"APPDATA": filepath.Join(home, "AppData", "Roaming"), "CLAUDE_PROJECT_DIR": dir,
		"LOOPTRACK_API_URL": base, "LOOPTRACK_PROJECT": "demo",
	}
	return m, dir
}

func runIn(t *testing.T, m map[string]string, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	// 下の検査は日本語の文面を見るので、指定が無ければ日本語にする。
	// 指定が無いときに英語が出るのが本来の振る舞い（端末の設定に従い、日本語でなければ英語）。
	if _, ok := m["LOOPTRACK_LANG"]; !ok {
		m["LOOPTRACK_LANG"] = "ja"
	}
	code := Main(args, IO{Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errb}, env.FromMap(m))
	return code, out.String(), errb.String()
}

func storeOf(t *testing.T, m map[string]string) *cred.Store {
	t.Helper()
	s, err := cred.Open(env.FromMap(m))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func entryOf(t *testing.T, path, base string) *jsonorder.Object {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	o, err := jsonorder.DecodeObject(b)
	if err != nil {
		t.Fatal(err)
	}
	if e := o.Object(base); e != nil {
		return e
	}
	return jsonorder.NewObject()
}

func swapBrowser(t *testing.T, f func(env.Env, string) bool, wait time.Duration) {
	savedOpen, savedWait := openBrowser, loginWait
	openBrowser, loginWait = f, wait
	t.Cleanup(func() { openBrowser, loginWait = savedOpen, savedWait })
}

// TestLoginBrowserThenRefresh: login --browser → 期限前の取り直し → 401 での取り直し。
func TestLoginBrowserThenRefresh(t *testing.T) {
	f := newFakeOAuth(t)
	f.expiresIn = 3600 // 1 日を切っているので、次の要求の前に取り直す
	var opened []string
	swapBrowser(t, browserVia(t, &opened), 30*time.Second)
	m, _ := loginEnv(t, f.base())
	store := storeOf(t, m)

	code, out, errOut := runIn(t, m, "", "login", "--browser")
	if code != 0 {
		t.Fatalf("login --browser: %d %s %s", code, out, errOut)
	}
	if len(opened) != 1 || !strings.HasPrefix(opened[0], f.base()+"/oauth/authorize?response_type=code&client_id=cid_1&redirect_uri=http%3A%2F%2F127.0.0.1%3A") {
		t.Fatalf("開いた URL: %v", opened)
	}
	if !strings.Contains(out, "ブラウザが開かないときは次の URL を開いてください:\n  "+opened[0]+"\n") {
		t.Errorf("URL を表示していない: %s", out)
	}
	wantTail := fmt.Sprintf("ログイン: alice（%s）。トークンを %s に保存しました（期限が近づくと自動で更新します）\n", f.base(), store.Paths.Primary)
	if !strings.HasSuffix(out, wantTail) {
		t.Errorf("出力: %q", out)
	}
	if strings.Contains(out+errOut, "imo_access") || strings.Contains(out+errOut, "imr_refresh") || strings.Contains(out+errOut, "code_1") {
		t.Errorf("出力にトークン・認可コードが出た: %s %s", out, errOut)
	}
	e := entryOf(t, store.Paths.Primary, f.base())
	if e.String("token") != "imo_access_1" || e.String("refresh_token") != "imr_refresh_1" || e.String("client_id") != "cid_1" ||
		e.String("login") != "alice" || strings.Join(e.Keys(), ",") != "token,refresh_token,expires_at,login,client_id" {
		t.Errorf("保存内容: %s", jsonorder.Compact(e))
	}
	if err := cred.CheckPrivate(store.Paths.Primary); err != nil {
		t.Errorf("資格情報が本人だけの形でない: %v", err)
	}

	// 期限前の取り直し（残りが 1 日を切っている）
	if code, out, errOut := runIn(t, m, "", "show", "DEMO-0001"); code != 0 || out != "# DEMO-0001 本文\n\n" {
		t.Fatalf("期限前の show: %d %q %s", code, out, errOut)
	}
	if e := entryOf(t, store.Paths.Primary, f.base()); e.String("token") != "imo_access_2" || e.String("refresh_token") != "imr_refresh_2" || f.refreshes != 1 {
		t.Errorf("期限前に取り直していない: %s（更新 %d 回）", jsonorder.Compact(e), f.refreshes)
	}

	// 401 での取り直し（期限は先でも、アクセストークンが無効）
	f.expiresIn = 30 * 24 * 3600
	store.SaveEntry(f.base(), entryOf(t, store.Paths.Primary, f.base()).Set("expires_at", jsonorder.Number(fmt.Sprint(time.Now().Unix()+30*24*3600))))
	f.revokeAccess()
	if code, out, errOut := runIn(t, m, "", "show", "DEMO-0001"); code != 0 || out != "# DEMO-0001 本文\n\n" {
		t.Fatalf("401 の後の show: %d %q %s", code, out, errOut)
	}
	if e := entryOf(t, store.Paths.Primary, f.base()); e.String("token") != "imo_access_3" || f.refreshes != 2 || f.reused != 0 {
		t.Errorf("401 で取り直していない: %s（更新 %d 回・再利用 %d 回）", jsonorder.Compact(e), f.refreshes, f.reused)
	}

	// 2 回目の login --browser は控えた client_id を再利用する
	opened = nil
	if code, _, errOut := runIn(t, m, "", "login", "--browser"); code != 0 || f.registers != 1 || !strings.Contains(opened[0], "client_id=cid_1&") {
		t.Errorf("2 回目: %d %s 登録 %d 回 %v", code, errOut, f.registers, opened)
	}
}

// TestRefreshConcurrent: 同時に走る複数の要求（フックと Bash）が期限切れで取り直しても、更新トークンは 1 回しか使わない。
func TestRefreshConcurrent(t *testing.T) {
	f := newFakeOAuth(t)
	f.expiresIn = 30 * 24 * 3600
	var opened []string
	swapBrowser(t, browserVia(t, &opened), 30*time.Second)
	m, _ := loginEnv(t, f.base())
	if code, _, errOut := runIn(t, m, "", "login", "--browser"); code != 0 {
		t.Fatal(errOut)
	}
	f.revokeAccess()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cl, err := api.New(env.FromMap(m))
			if err != nil {
				errs <- err
				return
			}
			if _, err := cl.Get("/issues/DEMO-0001?format=md&project=demo"); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("同時の要求: %v", err)
	}
	if _, refreshes, reused := f.counts(); refreshes != 1 || reused != 0 {
		t.Errorf("更新 %d 回・再利用 %d 回（1 回・0 回のはず）", refreshes, reused)
	}
}

func TestLoginBrowserErrors(t *testing.T) {
	f := newFakeOAuth(t)
	m, _ := loginEnv(t, f.base())
	store := storeOf(t, m)

	// 拒否
	f.deny = true
	var opened []string
	swapBrowser(t, browserVia(t, &opened), 30*time.Second)
	if code, _, errOut := runIn(t, m, "", "login", "--browser"); code != 1 || errOut != "エラー: ブラウザで許可されませんでした（利用者が拒否しました）\n" {
		t.Errorf("拒否: %d %q", code, errOut)
	}
	f.deny = false

	// 打ち切り: 開けないブラウザ（URL だけ表示）。控えた client_id は消して次回は登録し直す
	swapBrowser(t, func(env.Env, string) bool { return false }, 300*time.Millisecond)
	code, out, errOut := runIn(t, m, "", "login", "--browser")
	if code != 1 || !strings.Contains(out, "/oauth/authorize?") ||
		errOut != "エラー: 5 分以内にブラウザでのログインが終わらなかったため打ち切りました。もう一度 login --browser を実行してください"+
			"（ブラウザに「クライアントが登録されていません」と出ていた場合も、もう一度実行すれば登録し直します）\n" {
		t.Errorf("打ち切り: %d %q %q", code, out, errOut)
	}
	if e := entryOf(t, store.Paths.Primary, f.base()); e.Has("client_id") {
		t.Errorf("client_id を消していない: %s", jsonorder.Compact(e))
	}

	// URL が無い
	delete(m, "LOOPTRACK_API_URL")
	if code, _, errOut := runIn(t, m, "imp_x\n", "login"); code != 1 ||
		errOut != "エラー: サーバの URL が分かりません。--url http://127.0.0.1:8090/looptrack を指定するか、環境変数 LOOPTRACK_API_URL を設定してください\n" {
		t.Errorf("URL なし: %d %q", code, errOut)
	}
	// --url は /api/v1 と末尾の / を除く
	f.allow("imp_x")
	if code, out, errOut := runIn(t, m, "imp_x\n", "login", "--url", f.base()+"/api/v1/"); code != 0 || !strings.HasPrefix(out, "ログイン: alice（"+f.base()+"）") {
		t.Errorf("--url: %d %q %q", code, out, errOut)
	}
}

// TestCallbackHandler: 戻り先は GET /callback だけを受け、state が違えば 400 で拒否して待ち続ける。
func TestCallbackHandler(t *testing.T) {
	results := make(chan callbackResult, 1)
	srv := httptest.NewServer(callbackHandler(i18n.JA, "the-state", results))
	defer srv.Close()
	get := func(path string) (int, string) {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.Header.Get("Cache-Control") != "no-store" || res.Header.Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%s: ヘッダ %v", path, res.Header)
		}
		return res.StatusCode, string(b)
	}
	if code, _ := get("/other?state=the-state&code=x"); code != 404 {
		t.Errorf("他のパス: %d", code)
	}
	if code, body := get("/callback?state=wrong&code=x"); code != 400 || !strings.Contains(body, "state が一致しません") {
		t.Errorf("state 違い: %d %s", code, body)
	}
	if code, _ := get("/callback?state=the-state"); code != 400 {
		t.Errorf("code なし: %d", code)
	}
	select {
	case r := <-results:
		t.Fatalf("拒否した要求で結果を渡した: %+v", r)
	default:
	}
	if code, body := get("/callback?state=the-state&code=abc"); code != 200 || !strings.Contains(body, "閉じてかまいません") || strings.Contains(body, "abc") {
		t.Errorf("受け付け: %d %s", code, body)
	}
	if r := <-results; r.code != "abc" {
		t.Errorf("結果: %+v", r)
	}
	res, err := http.Post(srv.URL+"/callback?state=the-state&code=x", "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotImplemented {
		t.Errorf("POST: %d", res.StatusCode)
	}
}

func TestPKCEAndState(t *testing.T) {
	v, c := pkcePair()
	sum := sha256.Sum256([]byte(v))
	if len(v) != 86 || c != base64.RawURLEncoding.EncodeToString(sum[:]) || len(randomURLSafe(32)) != 43 {
		t.Errorf("verifier %d 文字・challenge %s", len(v), c)
	}
	if got := encodeOrdered([][2]string{{"b", "http://127.0.0.1:1/cb"}, {"a", "x y"}}); got != "b=http%3A%2F%2F127.0.0.1%3A1%2Fcb&a=x+y" {
		t.Errorf("encodeOrdered: %s", got)
	}
}
