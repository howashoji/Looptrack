package clitest

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
)

// BasePath は偽 API の置き場（本番と同じ /im の下）。LOOPTRACK_API_URL は URL + BasePath。
const BasePath = "/im"

// Response はフィクスチャの 1 つの応答。
type Response struct {
	Status      int               // 既定 200
	ContentType string            // 既定は Body が { か [ で始まれば application/json、それ以外は text/plain; charset=utf-8
	Header      map[string]string // 追加のヘッダ（X-Looptrack-Rows など）
	Body        string
	Drop        bool // 応答せずに接続を切る（通信断）
	// Redirect があれば 302 でその URL へ送る（OAuth の authorize。要求から戻り先を組み立てる）
	Redirect func(r *http.Request) string
}

// Route は 1 つの API の応答。Method と Path（BasePath を除いた部分。例 /api/v1/issues/DEMO-0001）が一致し、
// Query の値がすべて一致する最初の Route を使う。Responses は呼ばれた順に使い、最後の 1 つを繰り返す。
type Route struct {
	Method    string
	Path      string
	Query     map[string]string
	Responses []Response
}

// Request は偽 API が受けた要求の記録。
type Request struct {
	Method  string
	Path    string // エスケープしたままのパス（BasePath を含む）
	Query   string // 生のクエリ文字列
	Header  http.Header
	Body    []byte
	Matched bool // フィクスチャの Route に当たったか（外れは 404 コード unknown_api「API が見つかりません」）
}

// FakeAPI はフィクスチャを返し、要求を記録する httptest のサーバ。
type FakeAPI struct {
	Server *httptest.Server
	mu     sync.Mutex
	routes []Route
	used   []int
	reqs   []Request
}

// NewFakeAPI は偽 API を起動する。止めるのは呼び出し側（Close）。
func NewFakeAPI(routes []Route) *FakeAPI {
	f := &FakeAPI{routes: routes, used: make([]int, len(routes))}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

// URL は LOOPTRACK_API_URL に入れる値（http://127.0.0.1:<port>/im）。
func (f *FakeAPI) URL() string { return f.Server.URL + BasePath }

// Close はサーバを止める。
func (f *FakeAPI) Close() { f.Server.Close() }

// Requests は受けた要求（受けた順）。
func (f *FakeAPI) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.reqs...)
}

func (f *FakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	idx := f.match(r)
	var res Response
	if idx >= 0 {
		rt := f.routes[idx]
		n := f.used[idx]
		if n >= len(rt.Responses) {
			n = len(rt.Responses) - 1
		}
		if n >= 0 {
			res = rt.Responses[n]
		}
		f.used[idx]++
	} else {
		// 実サーバの現在の既定（経路が無い 404）と合わせる。コードは unknown_api（資源が無い 404 の
		// not_found とは別。internal/server/server.go）。「古いサーバ」（not_found + 同じ文面を返す）を
		// 模す必要があるケースは、明示の Route で個別に与える。
		res = ErrorResponse(http.StatusNotFound, "unknown_api", "API が見つかりません")
	}
	f.reqs = append(f.reqs, Request{Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.RawQuery,
		Header: r.Header.Clone(), Body: body, Matched: idx >= 0})
	f.mu.Unlock()

	if res.Drop {
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				conn.Close()
				return
			}
		}
		panic(http.ErrAbortHandler)
	}
	if res.Redirect != nil {
		http.Redirect(w, r, res.Redirect(r), http.StatusFound)
		return
	}
	for k, v := range res.Header {
		w.Header().Set(k, v)
	}
	ct := res.ContentType
	if ct == "" {
		ct = "text/plain; charset=utf-8"
		if s := strings.TrimSpace(res.Body); strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[") {
			ct = "application/json"
		}
	}
	w.Header().Set("Content-Type", ct)
	status := res.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	io.WriteString(w, res.Body)
}

func (f *FakeAPI) match(r *http.Request) int {
	path, ok := strings.CutPrefix(r.URL.Path, BasePath)
	if !ok {
		return -1
	}
	q := r.URL.Query()
	for i, rt := range f.routes {
		if rt.Method != r.Method || rt.Path != path {
			continue
		}
		hit := true
		for k, v := range rt.Query {
			if q.Get(k) != v || (v == "" && q.Has(k)) {
				hit = false
				break
			}
		}
		if hit {
			return i
		}
	}
	return -1
}

// ErrorResponse はサーバの writeError と同じ形（{"error":{"code","message"}}）の応答。
func ErrorResponse(status int, code, message string) Response {
	return Response{Status: status, Body: `{"error":{"code":` + jsonString(code) + `,"message":` + jsonString(message) + `}}` + "\n"}
}

// OAuthRedirect は authorize の要求を redirect_uri へ認可コード code と同じ state を付けて戻す（ブラウザの代わり）。
func OAuthRedirect(code string) func(r *http.Request) string {
	return func(r *http.Request) string {
		q := r.URL.Query()
		v := url.Values{"code": {code}, "state": {q.Get("state")}}
		return q.Get("redirect_uri") + "?" + v.Encode()
	}
}
