package proxycheck

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testSize = 1 << 20 // 1MiB（判定に要るのは閾値を超えるかどうかだけ。32MiB を流さない）

// looptrackLike は認証の無い要求に JSON の 401 を返す後段（本文は読み切ってから応える。届いた大きさを数える）。
func looptrackLike(t *testing.T, got *atomic.Int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		got.Store(n)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"code":"unauthorized","message":"auth required"}}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// frontWithLimit は nginx の client_max_body_size の真似: Content-Length が limit を超えれば本文を読まずに HTML の 413、
// 超えなければ後段へ渡す。
func frontWithLimit(t *testing.T, backend *httptest.Server, limit int64) *httptest.Server {
	t.Helper()
	u, _ := url.Parse(backend.URL)
	rp := httputil.NewSingleHostReverseProxy(u)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > limit {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			io.WriteString(w, "<html><head><title>413 Request Entity Too Large</title></head><body>nginx</body></html>")
			return
		}
		rp.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func client() *http.Client { return NewClient(10*time.Second, 2*time.Second) }

// 3 つの判定（足りている・足りない・確かめられない）を同じテストで見る。足りないときは後段に何も届かない
// （前段で止まる）ことと、足りているときは本文が全部後段に届いたことも確かめる（判定の経路が死んでいないことの対照）。
func TestCheckVerdicts(t *testing.T) {
	var got atomic.Int64
	backend := looptrackLike(t, &got)

	t.Run("前段の上限が足りない", func(t *testing.T) {
		got.Store(-1)
		front := frontWithLimit(t, backend, testSize/2)
		r := Check(context.Background(), client(), front.URL+"/looptrack"+ChunkPath, testSize)
		if r.Verdict != TooSmall || r.Status != http.StatusRequestEntityTooLarge {
			t.Fatalf("足りないはず: %+v", r)
		}
		if got.Load() != -1 {
			t.Fatalf("前段で止まったのに後段に届いた: %d バイト", got.Load())
		}
	})
	t.Run("前段の上限が足りている", func(t *testing.T) {
		got.Store(-1)
		front := frontWithLimit(t, backend, testSize*2)
		r := Check(context.Background(), client(), front.URL+"/looptrack"+ChunkPath, testSize)
		if r.Verdict != Enough || r.Status != http.StatusUnauthorized {
			t.Fatalf("足りているはず: %+v", r)
		}
		if got.Load() != testSize {
			t.Fatalf("後段に届いた本文 %d バイト（%d のはず）", got.Load(), testSize)
		}
	})
	t.Run("前段なし（後段が直接 JSON で応える）", func(t *testing.T) {
		r := Check(context.Background(), client(), backend.URL+ChunkPath, testSize)
		if r.Verdict != Enough {
			t.Fatalf("JSON の応答なら足りているはず: %+v", r)
		}
	})
	t.Run("サーバ自身の JSON の 413（error の鍵を持つ）", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			io.WriteString(w, `{"error":{"code":"too_large","message":"x"}}`)
		}))
		defer srv.Close()
		if r := Check(context.Background(), client(), srv.URL+ChunkPath, testSize); r.Verdict != Enough {
			t.Fatalf("error の鍵を持つ JSON の 413 はサーバ自身のもので、前段を通っているので足りているはず: %+v", r)
		}
	})
	t.Run("error の鍵を持たない JSON の 413", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			io.WriteString(w, `{"message":"Request Entity Too Large"}`)
		}))
		defer srv.Close()
		if r := Check(context.Background(), client(), srv.URL+ChunkPath, testSize); r.Verdict != TooSmall || r.Status != http.StatusRequestEntityTooLarge {
			t.Fatalf("error の鍵を持たない JSON の 413 は前段のものなので足りないはず: %+v", r)
		}
	})
	t.Run("error の鍵を持たない JSON のほかの応答", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			io.WriteString(w, `{"message":"bad gateway"}`)
		}))
		defer srv.Close()
		if r := Check(context.Background(), client(), srv.URL+ChunkPath, testSize); r.Verdict != Unknown {
			t.Fatalf("looptrack の形でない JSON は確かめられないはず: %+v", r)
		}
	})
	t.Run("JSON でないほかの応答", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusBadGateway)
			io.WriteString(w, "<html>bad gateway</html>")
		}))
		defer srv.Close()
		if r := Check(context.Background(), client(), srv.URL+ChunkPath, testSize); r.Verdict != Unknown || r.Status != http.StatusBadGateway {
			t.Fatalf("確かめられないはず: %+v", r)
		}
	})
	t.Run("つながらない", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		addr := srv.URL
		srv.Close()
		if r := Check(context.Background(), client(), addr+ChunkPath, testSize); r.Verdict != Unknown || r.Err == nil || r.Status != 0 {
			t.Fatalf("確かめられないはず: %+v", r)
		}
	})
}

func TestTarget(t *testing.T) {
	for _, c := range []struct{ pub, base, want string }{
		{"https://example.com", "/looptrack", "https://example.com/looptrack" + ChunkPath},
		{"https://example.com/", "looptrack/", "https://example.com/looptrack" + ChunkPath},
		{"http://127.0.0.1:8090", "", "http://127.0.0.1:8090" + ChunkPath},
		{"https://example.com/im", "/", "https://example.com/im" + ChunkPath},
	} {
		if got, err := Target(c.pub, c.base); err != nil || got != c.want {
			t.Errorf("Target(%q, %q) = %q, %v（%q のはず）", c.pub, c.base, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "example.com", "ftp://example.com", "https://"} {
		if got, err := Target(bad, "/looptrack"); err == nil {
			t.Errorf("Target(%q) は誤りのはず: %q", bad, got)
		}
	}
	if !strings.HasPrefix(ChunkPath, "/api/v1/transcripts/") {
		t.Fatalf("送り先は会話記録の追記の経路のはず: %s", ChunkPath)
	}
}
