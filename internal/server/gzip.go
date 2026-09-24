package server

import (
	"compress/gzip"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// API の JSON の応答を、要求が Accept-Encoding: gzip を持つときだけ圧縮する。
// 配置の前段（nginx）でも Go でも圧縮していなかったため、ボードの JSON がそのままの大きさで回線を通っていた。
// 前段の設定に頼らず、どの配置（ローカルモード・デスクトップ版を含む）でも効くようにサーバで行う。
// 対象は Content-Type が application/json の応答だけ（xlsx・配布物のようにもともと圧縮済みか、
// Range・Content-Length を自分で扱う応答には触らない）。ヘッダは元の Header() をそのまま使うので、
// CSP などの既存のヘッダはそのまま残る。

var gzipPool = sync.Pool{New: func() any { return gzip.NewWriter(nil) }}

// gzipJSON は next の JSON の応答を圧縮する。
func gzipJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 圧縮するかどうかで本文が変わるので、キャッシュには要求の Accept-Encoding で分けさせる（304 を含む全応答）
		w.Header().Add("Vary", "Accept-Encoding")
		if r.Method == http.MethodHead || !acceptsGzip(r.Header.Get("Accept-Encoding")) {
			next.ServeHTTP(w, r)
			return
		}
		g := &gzipJSONWriter{ResponseWriter: w}
		defer g.close()
		next.ServeHTTP(g, r)
	})
}

// acceptsGzip は Accept-Encoding に gzip（か *）が q=0 以外で入っているかを見る。
func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "gzip" && name != "*" {
			continue
		}
		for _, p := range strings.Split(params, ";") {
			k, v, _ := strings.Cut(strings.TrimSpace(p), "=")
			if strings.EqualFold(strings.TrimSpace(k), "q") {
				if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && f <= 0 {
					return false
				}
			}
		}
		return true
	}
	return false
}

type gzipJSONWriter struct {
	http.ResponseWriter
	gz      *gzip.Writer
	decided bool
}

// WriteHeader は、状態コードと Content-Type が決まった時点で圧縮するかを決める。
func (g *gzipJSONWriter) WriteHeader(code int) {
	if !g.decided {
		g.decided = true
		h := g.Header()
		if code >= 200 && code != http.StatusNoContent && code != http.StatusNotModified &&
			strings.HasPrefix(h.Get("Content-Type"), "application/json") && h.Get("Content-Encoding") == "" {
			h.Set("Content-Encoding", "gzip")
			h.Del("Content-Length")
			gz := gzipPool.Get().(*gzip.Writer)
			gz.Reset(g.ResponseWriter)
			g.gz = gz
		}
	}
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipJSONWriter) Write(b []byte) (int, error) {
	if !g.decided {
		g.WriteHeader(http.StatusOK)
	}
	if g.gz != nil {
		return g.gz.Write(b)
	}
	return g.ResponseWriter.Write(b)
}

// Flush は圧縮中の分を送ってから下の Flush を呼ぶ。
func (g *gzipJSONWriter) Flush() {
	if g.gz != nil {
		_ = g.gz.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap は http.ResponseController が下の ResponseWriter に届くようにする。
func (g *gzipJSONWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

func (g *gzipJSONWriter) close() {
	if g.gz == nil {
		return
	}
	_ = g.gz.Close()
	g.gz.Reset(nil)
	gzipPool.Put(g.gz)
	g.gz = nil
}
