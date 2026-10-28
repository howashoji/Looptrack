package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/env"
)

// 前段のリバースプロキシがチャンクを本文の大きさで止めたとき（JSON の error を持たない HTML の 413）、
// looptrack session transcript push は「HTTP 413」ではなく前段の上限と直し方（client_max_body_size）を出す。
// 対照: サーバ自身の 413（JSON の error を持つ）はサーバの文面のまま出す。どちらも hook の分け方は 4xx の拒否で、
// 前段の上限だけ ProxyLimit が立つ（hook はこれで失敗の理由を分ける）。
func TestPushReportsProxyBodyLimit(t *testing.T) {
	for _, c := range []struct {
		name      string
		reply     func(w http.ResponseWriter)
		want      string
		notWant   string
		wantProxy bool
	}{
		{"前段の HTML の 413", func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(413)
			io.WriteString(w, "<html><head><title>413 Request Entity Too Large</title></head><body><center><h1>413 Request Entity Too Large</h1></center><hr><center>nginx</center></body></html>")
		}, "前段のリバースプロキシが本文の大きさの上限で止めました", "", true},
		{"サーバの JSON の 413", func(w http.ResponseWriter) {
			writeJSONResp(w, 413, map[string]any{"error": map[string]any{"code": "payload_too_large", "message": "本文が大きすぎます（サーバの文面）"}})
		}, "本文が大きすぎます（サーバの文面）", "前段のリバースプロキシ", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			chunks := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				switch {
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/projects/demo/transcripts"):
					writeJSONResp(w, 200, map[string]any{"transcript": map[string]any{"id": 1, "seq": 0, "files": []any{}, "transcripts_enabled": true}})
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/transcripts/1/chunks"):
					chunks++
					c.reply(w)
				default:
					writeJSONResp(w, 404, map[string]any{"error": map[string]any{"code": "unknown_api", "message": r.URL.Path}})
				}
			}))
			defer srv.Close()
			vars, home, _ := trEnv(t, srv.URL)
			writeClaudeSession(t, home, jsonlLine("hello"), "")
			code, stdout, stderr := runSession(t, vars, "transcript", "push", trTestSID, "--agent", "claude-code")
			if chunks == 0 {
				t.Fatalf("前提が崩れています（チャンクの要求が届いていない）: exit %d stdout %q stderr %q", code, stdout, stderr)
			}
			if code == 0 {
				t.Fatalf("413 なのに終了コード 0: stdout %q stderr %q", stdout, stderr)
			}
			if !strings.Contains(stderr, c.want) {
				t.Errorf("文面に %q がありません: %q", c.want, stderr)
			}
			if c.wantProxy && !strings.Contains(stderr, "client_max_body_size を 33m 以上") {
				t.Errorf("直し方がありません: %q", stderr)
			}
			if c.notWant != "" && strings.Contains(stderr, c.notWant) {
				t.Errorf("サーバの 413 に前段の文面を出しました: %q", stderr)
			}
			// hook と同じ本体で送ったときの分け方
			r := PushTranscriptForHook(env.FromMap(vars), "claude-code", trTestSID)
			if r.Outcome != HookPushRefused || r.Status != 413 || r.ProxyLimit != c.wantProxy {
				t.Errorf("hook の分け方: outcome %v status %d proxy %v（Refused・413・%v のはず）", r.Outcome, r.Status, r.ProxyLimit, c.wantProxy)
			}
		})
	}
	// 分け方そのもの（api.Error の形だけで決まる）
	if r := classifyHookPush(&api.Error{Status: 413, Code: "payload_too_large"}); r.ProxyLimit {
		t.Error("JSON の 413 に ProxyLimit を立てた")
	}
}
