package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/client/cli"
	"github.com/howashoji/looptrack/internal/client/env"
)

// looptrack check-proxy は判定を 1 行で出し、どの判定でも終了コード 0（install.sh を止めない）。
// 送り先は LOOPTRACK_PUBLIC_URL + LOOPTRACK_BASE_PATH の会話記録の追記の経路。
// 対照に、前段が足りない（HTML の 413）・足りている（JSON の 401）・公開 URL が無い の 3 つを同じテストで見る。
func TestCheckProxyCmd(t *testing.T) {
	old := checkProxyContinueWait
	checkProxyContinueWait = time.Second
	t.Cleanup(func() { checkProxyContinueWait = old })
	var paths []string
	limit := int64(-1) // 前段の上限（-1 なら上限なし）
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if limit >= 0 && r.ContentLength > limit {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			io.WriteString(w, "<html>413 Request Entity Too Large</html>")
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"code":"unauthorized","message":"x"}}`)
	}))
	defer srv.Close()
	run := func(vars map[string]string, args ...string) (int, string) {
		var out, errb bytes.Buffer
		vars["LOOPTRACK_LANG"] = "ja"
		code := checkProxyCmd(args, cli.IO{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb}, env.FromMap(vars))
		return code, out.String() + errb.String()
	}
	vars := map[string]string{"LOOPTRACK_PUBLIC_URL": srv.URL + "/", "LOOPTRACK_BASE_PATH": "/im"}

	limit = 1 << 20
	code, out := run(vars)
	if code != 0 || !strings.Contains(out, "前段が HTTP 413 で止めました") || !strings.Contains(out, "client_max_body_size 33m;") ||
		!strings.Contains(out, "32 MiB") {
		t.Fatalf("足りない: exit %d %q", code, out)
	}
	if len(paths) != 1 || paths[0] != "POST /im/api/v1/transcripts/0/chunks" {
		t.Fatalf("送り先の経路: %v", paths)
	}
	limit = -1
	if code, out := run(vars); code != 0 || !strings.Contains(out, "HTTP 401 で応えました") || !strings.Contains(out, "足りています") {
		t.Fatalf("足りている: exit %d %q", code, out)
	}
	if code, out := run(map[string]string{}); code != 0 || !strings.Contains(out, "LOOPTRACK_PUBLIC_URL が無いか") {
		t.Fatalf("公開 URL が無い: exit %d %q", code, out)
	}
	if code, _ := run(map[string]string{}, "extra"); code != 2 {
		t.Fatalf("余分な引数は 2: %d", code)
	}
}
