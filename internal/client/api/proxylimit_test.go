package api

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
)

// JSON の error を持たない 413 は前段のリバースプロキシが返したものとして、こちら側の文面（ID つき）にする。
// 対照: JSON の error を持つ 413（サーバ自身の拒否）はサーバの文面のまま。413 でない HTML の誤りは今までどおり「HTTP <状態>」。
func TestProxyBodyLimitError(t *testing.T) {
	for _, c := range []struct {
		name      string
		status    int
		ctype     string
		body      string
		wantProxy bool
		wantJA    string
	}{
		{"前段の HTML の 413", 413, "text/html", "<html><body><h1>413 Request Entity Too Large</h1></body></html>", true, "前段のリバースプロキシが本文の大きさの上限で止めました"},
		{"本文の無い 413", 413, "", "", true, "client_max_body_size を 33m 以上"},
		{"サーバの JSON の 413", 413, "application/json", `{"error":{"code":"transcript_too_large","message":"サーバの文面"}}`, false, "サーバの文面"},
		{"前段の HTML の 502", 502, "text/html", "<html>bad gateway</html>", false, "HTTP 502"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFake(t, func(w http.ResponseWriter, r *http.Request, n int) {
				if c.ctype != "" {
					w.Header().Set("Content-Type", c.ctype)
				}
				w.WriteHeader(c.status)
				io.WriteString(w, c.body)
			})
			cl := newClient(t, map[string]string{"LOOPTRACK_API_URL": f.URL + "/im", "LOOPTRACK_TOKEN": "imp_t"})
			_, err := cl.Do(Request{Method: "POST", Path: "/transcripts/1/chunks", RawBody: []byte("x")})
			var ae *Error
			if !errors.As(err, &ae) {
				t.Fatalf("api.Error のはず: %v", err)
			}
			if ae.Status != c.status || ae.ProxyBodyLimit() != c.wantProxy {
				t.Errorf("状態 %d・前段 %v（%d・%v のはず）", ae.Status, ae.ProxyBodyLimit(), c.status, c.wantProxy)
			}
			if got := ae.Text(i18n.JA); !strings.Contains(got, c.wantJA) {
				t.Errorf("日本語の文面 %q に %q がありません", got, c.wantJA)
			}
			if c.wantProxy && !strings.Contains(ae.Text(i18n.EN), "client_max_body_size") {
				t.Errorf("英語の文面に直し方がありません: %q", ae.Text(i18n.EN))
			}
		})
	}
}
