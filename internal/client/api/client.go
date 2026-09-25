// Package api は looptrack（クライアント側）からイシュー管理サーバの REST API を呼ぶ
// （以前の CLI（1.0.0 より前）と同じ振る舞い）。
//
//   - URL は LOOPTRACK_API_URL（末尾の / と /api/v1 を除く）。待ち時間は LOOPTRACK_TIMEOUT（秒・既定 30）。
//   - トークンは引数 → 環境変数（LOOPTRACK_TOKEN）→ 資格情報のファイル（cred）の順。ファイルから取ったときだけ、
//     期限が近ければ先に、401 なら後で、保存した更新トークンで取り直して 1 回だけやり直す。
//   - どれも無いときは、サーバがローカルモード（/healthz が X-Looptrack-Local-Mode: 1 を返す。LocalMode）なら
//     Authorization を付けずに呼ぶ（ローカルモードは認証を省くため）。そうでなければ呼ばずに NoTokenError。
//   - ヘッダは Authorization（ローカルモードで資格情報が無ければ付けない）・X-Looptrack-Client: cli・Accept と、
//     AI の下で動いていれば X-Looptrack-Session・X-Looptrack-Agent（session）。
//   - 1 要求ごとに接続する（keep-alive を使わない。切れた接続を黙って再送しない）。
//   - 2xx 以外は *Error（サーバの {"error":{code,message}}）、接続できなければ *ConnError。
package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/howashoji/looptrack/internal/client/cred"
	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/client/session"
	"github.com/howashoji/looptrack/internal/i18n"
)

// GoCommand は案内の文面に出す CLI の呼び方。
// 以前は、移行したプロジェクトに以前の CLI の入口があればそちらの形で案内していたが、
// 以前の入口を廃止したので 1 つだけにした。
const GoCommand = "looptrack issue"

// SetRoot は以前、案内の形を決めるためにプロジェクトのディレクトリを覚えていた（入口の廃止で不要になった）。
func SetRoot(string) {}

// EntryCommand は案内の文面に出す CLI の呼び方。
func EntryCommand() string { return GoCommand }

// Relogin は 401 のときに添える案内。
func Relogin(lang i18n.Lang) string {
	return i18n.T(lang, "api.err.relogin", "cmd", EntryCommand())
}

// DefaultTimeout は API の待ち時間の既定。
const DefaultTimeout = 30 * time.Second

// RefreshMargin は保存したトークンの残りがこれを切ったら期限前に取り直す。
const RefreshMargin = 24 * time.Hour

// Version は User-Agent に入れる版（cmd/looptrack が設定する）。
var Version = "dev"

// Error はサーバが 2xx 以外を返した。
//
// Message はサーバが返した文面（サーバが利用者の言語で作る。要求の Accept-Language で決まる）。
// Msg は、サーバではなくこちら側で作った文面のときだけ入る（ID を持つので表示する側が訳せる）。
type Error struct {
	Status  int
	Code    string
	Message string
	Msg     i18n.Msg          // こちら側で作った文面（ID）。空なら Message を使う
	Body    *jsonorder.Object // 応答の error オブジェクト（無ければ空）
}

func (e *Error) Error() string { return e.Message }

// Text は利用者の言語の文面。ID を持たない（サーバ由来の）ものは Message をそのまま返す。
func (e *Error) Text(lang i18n.Lang) string {
	if e.Msg.ID == "" {
		return e.Message
	}
	return e.Msg.In(lang)
}

// ConnError はサーバに接続できない（通信断・名前解決・TLS・時間切れ）。
type ConnError struct {
	URL    string
	Reason string
}

func (e *ConnError) Error() string {
	return i18n.Text(i18n.JA, e.Unwrap())
}

// Unwrap は ID を持つ文面を返す（i18n.Text が errors.As でこれを見つけて訳す）。
// Error() はログと、まだ i18n を通していない表示のために、同じ ID の日本語（対訳表の正本）を返す。
func (e *ConnError) Unwrap() error {
	return i18n.Errorf("api.err.connect", "url", e.URL, "reason", e.Reason)
}

// NoTokenError はトークンが無い。
type NoTokenError struct{ URL string }

func (e *NoTokenError) Error() string {
	return i18n.Text(i18n.JA, e.Unwrap())
}

// Unwrap は ID を持つ文面を返す（ConnError と同じ）。
func (e *NoTokenError) Unwrap() error {
	return i18n.Errorf("api.err.no_token", "cmd", EntryCommand(), "url", e.URL)
}

// TimeoutError は待ち時間の環境変数が数でない。
type TimeoutError struct{ Var, Value string }

func (e *TimeoutError) Error() string {
	return i18n.Text(i18n.JA, e.Unwrap())
}

// Unwrap は ID を持つ文面を返す（ConnError と同じ）。
func (e *TimeoutError) Unwrap() error {
	return i18n.Errorf("api.err.timeout_not_number", "var", e.Var, "value", e.Value)
}

// LocalModeHeader は、サーバがローカルモード（DESIGN §5-12）であることを告げる /healthz の応答ヘッダ
// （サーバ側は internal/server の healthz。値は "1"）。
const LocalModeHeader = "X-Looptrack-Local-Mode"

// localProbePath は LocalMode が引く、認証の要らない口。
const localProbePath = "/healthz"

// LocalURL は、URL がこの機械のループバックの http か（ホストは 127.0.0.1・::1・localhost。
// ローカルモードのサーバの待ち受けと Host の検査と同じ 3 つ）。LocalMode を確かめる前の足切り。
func LocalURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "http" {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	return false
}

// NormalizeURL は URL の末尾の / と /api/v1 を除く。
func NormalizeURL(raw string) string {
	u := strings.TrimRight(strings.TrimSpace(raw), "/")
	return strings.TrimSuffix(u, "/api/v1")
}

// Client は API の呼び出し。
type Client struct {
	Env       env.Env
	BaseURL   string // 正規化した URL（空なら API の設定が無い）
	Timeout   time.Duration
	Creds     *cred.Store
	Mark      session.Mark
	UserAgent string
	Now       func() time.Time
	// OnLang は、サーバが応答で宣言した言語（Content-Language）を受け取る（nil なら何もしない）。
	// サーバは利用者の設定（users.lang。クライアントは知らない）まで見て決めるので、送った値の反響ではない。
	// 呼ばれるのは応答のヘッダを読めた要求ごとに 1 回。要求を出す側と同じ goroutine で呼ぶ。
	OnLang    func(i18n.Lang)
	transport http.RoundTripper
	mu        sync.Mutex
	localMode map[string]bool // URL ごとの「サーバがローカルモードか」（LocalMode が 1 回だけ引いて覚える）
}

// New は環境変数から Client を作る。
func New(e env.Env) (*Client, error) {
	timeout := DefaultTimeout
	if v, name := e.Setting(env.Timeout); v != "" {
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || f <= 0 {
			return nil, &TimeoutError{name, v}
		}
		timeout = time.Duration(f * float64(time.Second))
	}
	return NewWithTimeout(e, timeout)
}

// NewWithTimeout は待ち時間を指定して Client を作る（環境変数の待ち時間は読まない。summary の 2 秒など）。
func NewWithTimeout(e env.Env, timeout time.Duration) (*Client, error) {
	c := &Client{
		Env:       e,
		BaseURL:   NormalizeURL(e.Value(env.APIURL)),
		Timeout:   timeout,
		Mark:      session.Detect(e),
		UserAgent: "looptrack/" + Version,
		Now:       time.Now,
	}
	store, err := cred.Open(e)
	if err != nil {
		return nil, err
	}
	c.Creds = store
	c.transport = &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		TLSClientConfig:       TLSConfig(e),
		DisableKeepAlives:     true,
		TLSHandshakeTimeout:   c.Timeout,
		ResponseHeaderTimeout: c.Timeout,
		DialContext:           (&net.Dialer{Timeout: c.Timeout}).DialContext,
	}
	return c, nil
}

// Request は 1 回の呼び出し。
type Request struct {
	Method  string
	Path    string            // /api/v1 の後ろ（クエリを含めてよい）
	Body    any               // JSON で送る本文（nil なら送らない）
	Headers map[string]string // 追加・上書きのヘッダ
	Token   string            // 明示のトークン（空なら環境変数か資格情報）
	URL     string            // 明示の URL（空なら BaseURL）
	Raw     bool              // true なら本文を解釈しない（xlsx など）
}

// Response は 2xx の応答。
type Response struct {
	Status int
	Header http.Header
	Raw    []byte
	Value  any // JSON なら jsonorder の値（壊れていれば空の Object）、それ以外は文字列。Raw のときは nil
}

// TokenSource は (トークン, 取得元) を返す。無ければ ("", "")。
func (c *Client) TokenSource(url string) (string, string, error) {
	if v, name := c.Env.Setting(env.Token); v != "" {
		// 取得元のラベル。表示する側が言語を知っているので、ここでは日本語のままにせず ID から作る
		return strings.TrimSpace(v), i18n.T(i18n.FromEnv(c.Env.Get), "api.token_source.env", "name", name), nil
	}
	entry, err := c.Creds.Entry(url)
	if err != nil {
		return "", "", err
	}
	if tok := entry.String("token"); tok != "" {
		return tok, c.Creds.Path(), nil
	}
	return "", "", nil
}

// Do は API を呼ぶ（期限前・401 の取り直しを含む）。
func (c *Client) Do(r Request) (*Response, error) {
	base := r.URL
	if base == "" {
		base = c.BaseURL
	}
	managed := r.Token == "" && c.Env.Value(env.Token) == ""
	token := r.Token
	if token == "" {
		tok, _, err := c.TokenSource(base)
		if err != nil {
			return nil, err
		}
		token = tok
	}
	if token == "" {
		return c.withoutToken(r, base)
	}
	if managed {
		t, err := c.refreshIfExpiring(base, token)
		if err != nil {
			return nil, err
		}
		token = t
	}
	res, err := c.once(r, base, token)
	var ae *Error
	if err == nil || !managed || !errors.As(err, &ae) || ae.Status != http.StatusUnauthorized {
		return res, err
	}
	fresh, why, rerr := c.refresh(base, token)
	if rerr != nil {
		return nil, rerr
	}
	if fresh == "" {
		if why == "invalid" {
			// サーバではなくこちら側で作る文面なので、ID を持たせて表示する側に訳させる
			m := i18n.M("api.err.refresh_expired")
			return nil, &Error{Status: 401, Code: ae.Code, Message: m.In(i18n.JA), Msg: m, Body: ae.Body}
		}
		return nil, err
	}
	return c.once(r, base, fresh)
}

// LocalMode は、その URL のサーバがローカルモード（DESIGN §5-12）か。ローカルモードのサーバは
// 「同じ機械の本人だけが使う」前提で画面・MCP・REST API の認証を省くので、CLI もトークンなしで呼べる。
// 確かめ方は、認証の要らない /healthz の応答ヘッダ X-Looptrack-Local-Mode（サーバが名乗らなければ通常モード）。
// 引くのはこの機械のループバックの http（LocalURL）のときだけで、結果は Client に覚えて 1 回しか引かない。
// チームのサーバ（名乗らない・そもそも引かない）の扱いは変わらない。
func (c *Client) LocalMode(base string) bool {
	if !LocalURL(base) {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok := c.localMode[base]; ok {
		return v
	}
	status, header, _, err := c.send(http.MethodGet, base+localProbePath, nil,
		map[string]string{"X-Looptrack-Client": "cli", "User-Agent": c.UserAgent})
	v := err == nil && status == http.StatusOK && header.Get(LocalModeHeader) == "1"
	if c.localMode == nil {
		c.localMode = map[string]bool{}
	}
	c.localMode[base] = v
	return v
}

// withoutToken は資格情報が無いときの呼び出し。ローカルモードのサーバには、画面・MCP と同じく
// Authorization を付けずに送る（サーバは見ないので、トークンを発行させる手間を掛けない）。
// ローカルモードでなければ、従来どおり呼ばずに「アクセストークンがありません」で終える。
// 名乗っていても 401 が返れば（別のサーバに入れ替わったなど）同じ案内にする＝認証が緩むのはローカルモードのサーバだけ。
func (c *Client) withoutToken(r Request, base string) (*Response, error) {
	if !c.LocalMode(base) {
		return nil, &NoTokenError{base}
	}
	res, err := c.once(r, base, "")
	var ae *Error
	if errors.As(err, &ae) && ae.Status == http.StatusUnauthorized {
		return nil, &NoTokenError{base}
	}
	return res, err
}

// Get は GET して値を返す。
func (c *Client) Get(path string) (any, error) {
	res, err := c.Do(Request{Method: http.MethodGet, Path: path})
	if err != nil {
		return nil, err
	}
	return res.Value, nil
}

// once は 1 回だけ呼ぶ（取り直しのやり直しは呼び元が行う）。
func (c *Client) once(r Request, base, token string) (*Response, error) {
	var body io.Reader
	if r.Body != nil {
		body = bytes.NewReader(jsonorder.Marshal(r.Body))
	}
	headers := map[string]string{
		"X-Looptrack-Client": "cli",
		"Accept":             "application/json",
		"User-Agent":         c.UserAgent,
	}
	if token != "" { // 空はローカルモード（withoutToken）。Authorization を付けない
		headers["Authorization"] = "Bearer " + token
	}
	for k, v := range c.Mark.Headers() {
		headers[k] = v
	}
	if r.Body != nil {
		headers["Content-Type"] = "application/json; charset=utf-8"
	}
	for k, v := range r.Headers {
		headers[k] = v
	}
	status, header, raw, err := c.send(r.Method, base+"/api/v1"+r.Path, body, headers)
	if err != nil {
		return nil, &ConnError{URL: base, Reason: err.Error()}
	}
	if status < 200 || status >= 300 {
		e := &Error{Status: status, Body: jsonorder.NewObject()}
		if obj, ok := decodeBody(header, raw).(*jsonorder.Object); ok {
			if eo := obj.Object("error"); eo != nil {
				e.Body = eo
				e.Code = eo.String("code")
				e.Message = eo.String("message")
			}
		}
		if e.Message == "" {
			e.Message = fmt.Sprintf("HTTP %d", status)
		}
		return nil, e
	}
	res := &Response{Status: status, Header: header, Raw: raw}
	if !r.Raw {
		res.Value = decodeBody(header, raw)
	}
	return res, nil
}

// langHeader は、利用者が言語を明示したときにサーバへ送るヘッダ（サーバ側は internal/server/lang.go）。
const langHeader = "X-Looptrack-Lang"

// contentLanguageHeader は、サーバがその応答をどの言語で作ったかの宣言（サーバ側は internal/server/lang.go）。
const contentLanguageHeader = "Content-Language"

// send は HTTP の要求を 1 回送り、応答の全体を読む。接続の失敗は理由の文にして返す。
func (c *Client) send(method, target string, body io.Reader, headers map[string]string) (int, http.Header, []byte, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return 0, nil, nil, errors.New(reason(err))
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	// 利用者の言語をサーバへ伝える（サーバはこれでエラー・案内の言語を決める）。呼び出し側が
	// 明示していればそれを尊重する。決め方は CLI の表示と同じ i18n.FromEnv（LOOPTRACK_LANG →
	// LC_ALL → LC_MESSAGES → LANG）。送らないとサーバは既定の英語を選ぶ。
	if req.Header.Get("Accept-Language") == "" {
		req.Header.Set("Accept-Language", string(i18n.FromEnv(c.Env.Get)))
	}
	// 利用者が言語を**明示した**ときだけ、明示の指定として X-Looptrack-Lang も送る。
	// サーバはこれを利用者の設定（サーバに保存した言語）より強く扱う。
	//
	// 上の Accept-Language は、環境変数が 1 つも無くても i18n.FromEnv の既定で en を送るので、
	// 「利用者が明示したのか、既定で en になっただけか」を見分けられない。明示でないのに送ると
	// 常に明示扱いになり、サーバに保存した設定が CLI に一度も効かなくなる。
	//
	// 見るのは LOOPTRACK_LANG だけにする（この製品の指定。LC_ALL / LC_MESSAGES / LANG は
	// 端末の既定であって「この製品の表示をこの言語にする」という明示ではないので含めない）。
	if req.Header.Get(langHeader) == "" {
		if lang, ok := i18n.Parse(c.Env.Get("LOOPTRACK_LANG")); ok {
			req.Header.Set(langHeader, string(lang))
		}
	}
	hc := &http.Client{Transport: c.transport}
	res, err := hc.Do(req)
	if err != nil {
		return 0, nil, nil, errors.New(reason(err))
	}
	defer res.Body.Close()
	// サーバが宣言した言語を呼び出し側へ渡す。**読めたときだけ**渡す（ok を捨てると、ヘッダを返さない
	// 古いサーバで i18n.Parse の既定の英語に倒れ、日本語の利用者に英語が出る）。
	if c.OnLang != nil {
		if lang, ok := i18n.Parse(res.Header.Get(contentLanguageHeader)); ok {
			c.OnLang(lang)
		}
	}
	raw, err := readIdle(res.Body, c.Timeout, cancel)
	if err != nil {
		return 0, nil, nil, errors.New(reason(err))
	}
	return res.StatusCode, res.Header, raw, nil
}

// readIdle は本文を読む。timeout の間なにも届かなければ打ち切る（以前の CLI と同じく、全体ではなく無通信の時間）。
func readIdle(r io.Reader, timeout time.Duration, cancel func()) ([]byte, error) {
	timer := time.AfterFunc(timeout, cancel)
	defer timer.Stop()
	var buf bytes.Buffer
	chunk := make([]byte, 32*1024)
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			buf.Write(chunk[:n])
			timer.Reset(timeout)
		}
		if err == io.EOF {
			return buf.Bytes(), nil
		}
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil, context.DeadlineExceeded
			}
			return nil, err
		}
	}
}

// reason は接続の失敗の理由（短い文）。
func reason(err error) string {
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return "timed out"
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	var oe *net.OpError
	if errors.As(err, &oe) && oe.Err != nil {
		return oe.Err.Error()
	}
	return err.Error()
}

// decodeBody は Content-Type が JSON なら jsonorder の値（壊れていれば空の Object）、それ以外は文字列。
func decodeBody(h http.Header, raw []byte) any {
	if strings.HasPrefix(h.Get("Content-Type"), "application/json") {
		v, err := jsonorder.Decode(raw)
		if err != nil {
			return jsonorder.NewObject()
		}
		return v
	}
	return string(raw)
}

// PathEscape はパスの 1 区間を逃がす（英数字と _.-~ 以外をすべて %XX にする）。
func PathEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || strings.IndexByte("_.-~", ch) >= 0 {
			b.WriteByte(ch)
		} else {
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}
