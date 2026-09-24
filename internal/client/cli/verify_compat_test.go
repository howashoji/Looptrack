//go:build unix

package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// cached の欄を知らない版のサーバ（DisallowUnknownFields で 400 invalid_json）に当たっても、
// 検証の記録そのものは残す（注記だけを捨てて送り直す）。注記を外したことはイシューのコメントにも残す
// （残さないと、後から記録を読む人には注記が無い理由が分からない）。
// これを入れる前は、結果キャッシュを見つけた回に限って記録が丸ごと失われ、CLI は exit 1 で終わっていた
// （実物の本番サーバで再現: `json: unknown field "cached"`）。
//
// 実物のシェルを起こすので unix だけ（Windows は Git for Windows の bash が要る）。
func TestVerifyDropsCachedNoteForOldServer(t *testing.T) {
	// oldServer: cached の欄を知らない版か。false は対照（注記を受け取れる版）で、
	// 退避路が発動しないので、注記を外したことのコメントも付かない。
	for _, oldServer := range []bool{true, false} {
		name := "old_server"
		if !oldServer {
			name = "current_server"
		}
		t.Run(name, func(t *testing.T) {
			var posts []map[string]any
			var comments []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet {
					io.WriteString(w, `{"id":"DEMO-0001","commands":["echo 'ok  \tdemo/pkg\t(cached)'"],"body_sha256":"abc","current":true,"last":null}`)
					return
				}
				var body map[string]any
				raw, _ := io.ReadAll(r.Body)
				json.Unmarshal(raw, &body)
				if strings.HasSuffix(r.URL.Path, "/comments") {
					text, _ := body["text"].(string)
					comments = append(comments, text)
					io.WriteString(w, `{"message":"コメントを追加: DEMO-0001","issue":{"id":"DEMO-0001"}}`)
					return
				}
				posts = append(posts, body)
				// 古いサーバの振る舞い: results に知らない欄 cached があれば本文ごと拒む
				if oldServer && strings.Contains(string(raw), `"cached"`) {
					w.WriteHeader(http.StatusBadRequest)
					io.WriteString(w, `{"error":{"code":"invalid_json","message":"リクエストの JSON を解釈できません: json: unknown field \"cached\""}}`)
					return
				}
				io.WriteString(w, `{"message":"verify を記録: DEMO-0001"}`)
			}))
			defer srv.Close()

			code, stdout, stderr := run(t, map[string]string{"LOOPTRACK_API_URL": srv.URL + "/im", "LOOPTRACK_PROJECT": "demo",
				"LOOPTRACK_TOKEN": "imp_lt", "LOOPTRACK_LANG": "ja"}, "verify", "DEMO-0001")

			if code != 0 {
				t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
			}
			if !strings.Contains(stdout, "verify を記録: DEMO-0001") {
				t.Errorf("記録できたことを出していません:\n%s", stdout)
			}
			if len(posts) == 0 || !hasCached(t, posts[0]) {
				t.Fatalf("前提が崩れています: 1 回目の POST に cached がありません（結果キャッシュの印が検出されていない）: %v", posts)
			}
			const mark = "注記を外して記録しました"
			if !oldServer {
				// 対照: 退避路は発動しない。送り直しも、注記を外したことのコメントも無い
				if len(posts) != 1 {
					t.Errorf("POST が %d 回（受け取れる版では送り直さない）: %v", len(posts), posts)
				}
				if len(comments) != 0 {
					t.Errorf("退避路が発動していないのにコメントが付きました: %q", comments)
				}
				if strings.Contains(stdout, mark) {
					t.Errorf("退避路が発動していないのに注記を外した旨を出しています:\n%s", stdout)
				}
				return
			}
			if len(posts) != 2 {
				t.Fatalf("POST が %d 回（1 回目は cached つき・2 回目は外して送り直す）: %v", len(posts), posts)
			}
			if hasCached(t, posts[1]) {
				t.Errorf("2 回目の POST から cached が外れていない: %v", posts[1])
			}
			if !strings.Contains(stdout, mark) {
				t.Errorf("注記が落ちたことを手元に出していません:\n%s", stdout)
			}
			// 記録の側にも残る（イシューのコメントに 1 行）
			if len(comments) != 1 {
				t.Fatalf("注記を外したことのコメントが %d 件（1 件のはず）: %q", len(comments), comments)
			}
			if !strings.Contains(comments[0], mark) || !strings.Contains(comments[0], "1 件") || strings.Contains(comments[0], "\n") {
				t.Errorf("コメントの文面が想定と違います: %q", comments[0])
			}
			t.Logf("記録に残ったコメント: %s", comments[0])
		})
	}
}

func hasCached(t *testing.T, body map[string]any) bool {
	t.Helper()
	list, _ := body["results"].([]any)
	for _, x := range list {
		if o, ok := x.(map[string]any); ok {
			if _, ok := o["cached"]; ok {
				return true
			}
		}
	}
	return false
}
