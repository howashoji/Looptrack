package cli

// login（以前の CLI（1.0.0 より前）と互換。DESIGN.md §5-9）。
//
//   - login: 標準入力（端末ならエコーなしの入力）のアクセストークンを /me で確かめて資格情報に保存する（控えた client_id は残す）。
//   - login --browser: サーバの OAuth（MCP と同じ認可コード + PKCE）をブラウザで通し、トークンと更新トークンを保存する。
//     127.0.0.1 の空きポートで 1 回だけ待ち受け（5 分で打ち切り）、動的登録の client_id は URL ごとに控えて再利用する。
//
// トークンは CLI とサーバの間の HTTP の本文だけを通り、引数・出力・ログには出さない（AI はトークンを扱わない）。
// 保存は cred（新しい置き場と、あれば旧い置き場の両方。ロックも以前の CLI と同じファイルを先に取る）。期限前・401 の
// 取り直しは api（refresh.go）が行う。

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/browser"
	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/i18n"
)

// loginTimeout はブラウザでのログインを待つ時間。
const loginTimeout = 5 * time.Minute

// oauthTimeout は認可サーバへの要求の待ち時間（LOOPTRACK_TIMEOUT は読まない）。
const oauthTimeout = 30 * time.Second

// テストで差し替えるもの。
var (
	openBrowser = browser.Open // URL をブラウザで開く
	loginWait   = loginTimeout // 待ち受けの打ち切り
)

func cmdLogin(c *Ctx, v *Values) error {
	raw := v.Str("url")
	if raw == "" {
		raw = c.Env.Value(env.APIURL)
	}
	base := api.NormalizeURL(raw)
	if base == "" {
		return i18n.Errorf("cli.login.err.no_url", "default", DefaultURL, "env", env.Name(env.APIURL))
	}
	if v.Bool("browser") {
		return loginBrowser(c, base)
	}
	tok, err := readToken(c)
	if err != nil {
		return err
	}
	if tok == "" {
		return i18n.Errorf("cli.login.err.empty_token")
	}
	cl, err := c.Client()
	if err != nil {
		return err
	}
	me, err := fetchMe(cl, base, tok)
	if err != nil {
		return loginCheckError(i18n.T(c.Lang, "cli.login.err.check_token"), err)
	}
	unlock, err := cl.Creds.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	old, err := cl.Creds.Entry(base)
	if err != nil {
		return err
	}
	entry := jsonorder.NewObject().Set("token", tok).Set("login", loginOf(me, ""))
	if cid, _ := old.Get("client_id"); jsonorder.Truthy(cid) { // login --browser 用に控えた client_id は残す
		entry.Set("client_id", cid)
	}
	if err := cl.Creds.SaveEntry(base, entry); err != nil {
		return err
	}
	c.Println(i18n.T(c.Lang, "cli.login.done", "login", loginOf(me, "?"), "url", base, "path", cl.Creds.Path()))
	return nil
}

// readToken は貼られたトークンを読む。端末ならエコーなしで促し（getpass）、そうでなければ 1 行を読む。
func readToken(c *Ctx) (string, error) {
	if f, ok := c.Stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(c.Stderr, i18n.T(c.Lang, "cli.login.prompt_token"))
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(c.Stderr)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	line, err := bufio.NewReader(c.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// fetchMe は tok で /me を 1 回だけ呼ぶ（明示のトークンなので取り直さない）。
func fetchMe(cl *api.Client, base, tok string) (*jsonorder.Object, error) {
	res, err := cl.Do(api.Request{Method: http.MethodGet, Path: "/me", Token: tok, URL: base})
	if err != nil {
		return nil, err
	}
	if o, ok := res.Value.(*jsonorder.Object); ok {
		return o, nil
	}
	return jsonorder.NewObject(), nil
}

// loginCheckError はサーバの誤りに前置きを付ける（接続できないなどはそのまま）。
// loginCheckError は「確認できません」系の理由に、サーバからの文言を足す。
// 文面は呼ぶ側が作る（ID を変数で渡すと、訳の抜けを見つけるテストが拾えないため）。
func loginCheckError(prefix string, err error) error {
	var ae *api.Error
	if errors.As(err, &ae) {
		return &Fail{prefix + ae.Message}
	}
	return err
}

func loginOf(me *jsonorder.Object, def string) string {
	if v, ok := me.Get("login"); ok {
		return jsonorder.Str(v)
	}
	return def
}

// callbackResult はコールバックで受けたもの（code か error）。
type callbackResult struct {
	code, err, desc string
}

// loginBrowser はブラウザで OAuth を通してトークンを保存する。
func loginBrowser(c *Ctx, base string) error {
	cl, err := api.NewWithTimeout(c.Env, oauthTimeout)
	if err != nil {
		return err
	}
	c.useServerLang(cl)
	status, meta, err := cl.OAuthRequest(base+"/.well-known/oauth-authorization-server", nil, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK || !hasString(mustGet(meta, "grant_types_supported"), "refresh_token") {
		return i18n.Errorf("cli.login.err.browser_unsupported", "url", base, "cmd", api.EntryCommand())
	}
	state := randomURLSafe(32)
	// 待ち受けは 127.0.0.1 だけ（localhost の名前解決・全インタフェースでは待たない）。逆引きもしない
	// （以前の CLI は待ち受けの開始時に逆引きし、遅い環境でブラウザを開く前に止まっていた）
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return i18n.Errorf("cli.login.err.listen", "reason", err)
	}
	redirect := "http://127.0.0.1:" + strconv.Itoa(ln.Addr().(*net.TCPAddr).Port) + "/callback"
	results := make(chan callbackResult, 1)
	srv := &http.Server{
		Handler:           callbackHandler(c.Lang, state, results),
		ReadHeaderTimeout: 10 * time.Second, // 繋いだまま何も送らない接続で待ち受けを止めない
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		ErrorLog:          log.New(io.Discard, "", 0), // 要求の記録（URL に認可コードを含む）は出さない
	}
	go srv.Serve(ln)
	defer srv.Close()
	// 受けた後は、ブラウザへの応答（「閉じてよい」）を書き終えてから閉じる
	shutdown := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}

	entry, err := lockedEntry(cl, base)
	if err != nil {
		return err
	}
	clientID := ""
	if v, _ := entry.Get("client_id"); jsonorder.Truthy(v) {
		clientID = jsonorder.Str(v)
	}
	reused := clientID != ""
	if !reused {
		host, _ := os.Hostname()
		reg := jsonorder.NewObject().Set("client_name", "looptrack（"+host+"）").Set("redirect_uris", []any{redirect}).
			Set("grant_types", []any{"authorization_code", "refresh_token"}).Set("response_types", []any{"code"}).
			Set("token_endpoint_auth_method", "none")
		status, res, err := cl.OAuthRequest(base+"/oauth/register", nil, reg)
		if err != nil {
			return err
		}
		if (status != http.StatusOK && status != http.StatusCreated) || res.String("client_id") == "" {
			return i18n.Errorf("cli.login.err.register", "reason", orHTTP(res.String("error_description"), status))
		}
		clientID = res.String("client_id")
		// 控えて次回から再利用する（client_id は秘密ではない）
		if err := updateEntry(cl, base, func(e *jsonorder.Object) bool { e.Set("client_id", clientID); return true }); err != nil {
			return err
		}
	}
	verifier, challenge := pkcePair()
	params := [][2]string{{"response_type", "code"}, {"client_id", clientID}, {"redirect_uri", redirect}, {"code_challenge", challenge},
		{"code_challenge_method", "S256"}, {"state", state}, {"scope", "im"}, {"resource", base + "/api/v1"}}
	authURL := base + "/oauth/authorize?" + encodeOrdered(params)
	c.Println(i18n.T(c.Lang, "cli.login.open_browser", "minutes", int(loginTimeout/time.Minute), "url", authURL))
	openBrowser(c.Env, authURL) // 開けない環境でも、表示した URL を開けば続けられる

	sig, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	timer := time.NewTimer(loginWait)
	defer timer.Stop()
	var got callbackResult
	select {
	case got = <-results:
	case <-sig.Done():
		return i18n.Errorf("cli.login.err.interrupted")
	case <-timer.C:
		hint := ""
		if reused {
			// 控えた client_id がサーバに無い（ブラウザに「クライアントが登録されていません」）場合に備え、次回は登録し直す
			if err := updateEntry(cl, base, func(e *jsonorder.Object) bool {
				v, _ := e.Get("client_id")
				e.Delete("client_id")
				return jsonorder.Truthy(v)
			}); err != nil {
				return err
			}
			hint = i18n.T(c.Lang, "cli.login.hint.reregister")
		}
		return i18n.Errorf("cli.login.err.timeout", "minutes", int(loginTimeout/time.Minute), "hint", hint)
	}
	shutdown()
	if got.err != "" {
		switch got.err {
		case "access_denied":
			d := got.desc
			if d == "" {
				d = got.err
			}
			return i18n.Errorf("cli.login.err.denied", "reason", d)
		case "invalid_target":
			return i18n.Errorf("cli.login.err.resource", "url", base, "reason", got.desc)
		}
		return i18n.Errorf("cli.login.err.failed", "error", got.err, "reason", got.desc)
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {got.code}, "redirect_uri": {redirect}, "client_id": {clientID},
		"code_verifier": {verifier}, "resource": {base + "/api/v1"}}
	status, res, err := cl.OAuthRequest(base+"/oauth/token", form, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK || res.String("access_token") == "" {
		msg := res.String("error_description")
		if msg == "" {
			msg = res.String("error")
		}
		return i18n.Errorf("cli.login.err.get_token", "reason", orHTTP(msg, status))
	}
	fields := jsonorder.NewObject()
	cl.TokenFields(fields, res)
	me, err := fetchMe(cl, base, fields.String("token"))
	if err != nil {
		return loginCheckError(i18n.T(c.Lang, "cli.login.err.check_new_token"), err)
	}
	fields.Set("login", loginOf(me, "")).Set("client_id", clientID)
	unlock, err := cl.Creds.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	if err := cl.Creds.SaveEntry(base, fields); err != nil {
		return err
	}
	c.Println(i18n.T(c.Lang, "cli.login.done_browser", "login", loginOf(me, "?"), "url", base, "path", cl.Creds.Path()))
	return nil
}

// callbackHandler はコールバック（GET /callback）を受ける。state が違えば 400 で拒否して待ち続ける（定数時間で比較）。
// ブラウザには「閉じてよい」だけを返す。
func callbackHandler(lang i18n.Lang, state string, results chan<- callbackResult) http.Handler {
	deliver := func(r callbackResult) {
		select {
		case results <- r:
		default: // 先に受けたものを使う
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			callbackReply(w, http.StatusNotImplemented, i18n.T(lang, "cli.login.web.unsupported"))
			return
		}
		if r.URL.Path != "/callback" {
			callbackReply(w, http.StatusNotFound, i18n.T(lang, "cli.login.web.not_found"))
			return
		}
		q := r.URL.Query()
		if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
			callbackReply(w, http.StatusBadRequest, i18n.T(lang, "cli.login.web.state_mismatch"))
			return
		}
		if e := q.Get("error"); e != "" {
			deliver(callbackResult{err: e, desc: q.Get("error_description")})
			callbackReply(w, http.StatusOK, i18n.T(lang, "cli.login.web.failed"))
			return
		}
		code := q.Get("code")
		if code == "" {
			callbackReply(w, http.StatusBadRequest, i18n.T(lang, "cli.login.web.no_code"))
			return
		}
		deliver(callbackResult{code: code})
		callbackReply(w, http.StatusOK, i18n.T(lang, "cli.login.web.ok"))
	})
}

func callbackReply(w http.ResponseWriter, status int, text string) {
	body := `<!doctype html><meta charset="utf-8"><title>looptrack login</title><p>` + text + `</p>`
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	io.WriteString(w, body)
}

// lockedEntry はロックの中で URL の項目を読む。
func lockedEntry(cl *api.Client, base string) (*jsonorder.Object, error) {
	unlock, err := cl.Creds.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	return cl.Creds.Entry(base)
}

// updateEntry はロックの中で URL の項目を読み直して f で変え、f が true を返したら保存する。
func updateEntry(cl *api.Client, base string, f func(*jsonorder.Object) bool) error {
	unlock, err := cl.Creds.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	e, err := cl.Creds.Entry(base)
	if err != nil {
		return err
	}
	if !f(e) {
		return nil
	}
	return cl.Creds.SaveEntry(base, e)
}

// randomURLSafe は n バイトの乱数の base64url（パディングなし）。
func randomURLSafe(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand は失敗しない（Go 1.24 以降）
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// pkcePair は PKCE の code_verifier と S256 の code_challenge。
func pkcePair() (string, string) {
	verifier := randomURLSafe(64)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// encodeOrdered はクエリ文字列にする（渡した順のまま。キーと値は %XX に逃がす）。
func encodeOrdered(params [][2]string) string {
	parts := make([]string, len(params))
	for i, p := range params {
		parts[i] = url.QueryEscape(p[0]) + "=" + url.QueryEscape(p[1])
	}
	return strings.Join(parts, "&")
}

func hasString(v any, want string) bool {
	switch l := v.(type) {
	case []any:
		for _, x := range l {
			if s, ok := x.(string); ok && s == want {
				return true
			}
		}
	case []string:
		for _, s := range l {
			if s == want {
				return true
			}
		}
	}
	return false
}

func mustGet(o *jsonorder.Object, key string) any {
	v, _ := o.Get(key)
	return v
}

func orHTTP(msg string, status int) string {
	if msg != "" {
		return msg
	}
	return fmt.Sprintf("HTTP %d", status)
}
