package cli

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/client/env"
)

// サーバが宣言した言語（Content-Language）を手元の描画に使うこと。
//
// 手元で組み立てる文面で見る（「エラー: 」/ 「Error: 」の接頭辞と「サーバエラー（HTTP 500）」の枠は
// CLI が対訳表から作る。サーバが返した本文はそのまま出るので、言語の証拠にならない）。

// langServer は、応答に Content-Language を付ける（contentLang が空なら付けない）サーバ。
// 本文は 500 のエラーにして、CLI が手元で組み立てる文面を出させる。
func langServer(t *testing.T, contentLang string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if contentLang != "" {
			w.Header().Set("Content-Language", contentLang)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":{"code":"boom","message":"body"}}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// runWithEnv は、与えた環境だけで looptrack issue を動かす（cli_test.go の run と違い、言語を勝手に足さない）。
func runWithEnv(t *testing.T, m map[string]string, args ...string) (int, string) {
	t.Helper()
	var out, errb bytes.Buffer
	m["HOME"] = t.TempDir()
	m["USERPROFILE"] = m["HOME"]
	m["CLAUDE_PROJECT_DIR"] = t.TempDir()
	code := Main(args, IO{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb}, env.FromMap(m))
	return code, errb.String()
}

func TestServerLangDrivesLocalOutput(t *testing.T) {
	for _, c := range []struct {
		name         string
		contentLang  string // サーバの宣言（空ならヘッダを返さない＝古いサーバ）
		looptrackEnv string // LOOPTRACK_LANG（空なら明示なし）
		osLang       string // LANG（端末の設定）
		wantPrefix   string
	}{
		{name: "明示なしならサーバの宣言が勝つ", contentLang: "ja", wantPrefix: "エラー: "},
		{name: "明示なしならサーバの宣言（英語）も勝つ", contentLang: "en", osLang: "ja_JP.UTF-8", wantPrefix: "Error: "},
		{name: "LOOPTRACK_LANG の明示は宣言より強い（en）", contentLang: "ja", looptrackEnv: "en", wantPrefix: "Error: "},
		{name: "LOOPTRACK_LANG の明示は宣言より強い（ja）", contentLang: "en", looptrackEnv: "ja", wantPrefix: "エラー: "},
		// 古いサーバ（ヘッダなし）。i18n.Parse の ok を捨てると、ここが英語に化ける
		{name: "宣言が無ければ端末の設定に倒れる", contentLang: "", osLang: "ja_JP.UTF-8", wantPrefix: "エラー: "},
		{name: "宣言が無く端末も英語なら英語", contentLang: "", osLang: "en_US.UTF-8", wantPrefix: "Error: "},
		// 読めない宣言も「宣言なし」と同じ扱い
		{name: "読めない宣言は無視して端末の設定へ", contentLang: "fr", osLang: "ja_JP.UTF-8", wantPrefix: "エラー: "},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := langServer(t, c.contentLang)
			m := map[string]string{"LOOPTRACK_API_URL": srv.URL + "/im", "LOOPTRACK_TOKEN": "imp_t", "LOOPTRACK_PROJECT": "demo"}
			if c.looptrackEnv != "" {
				m["LOOPTRACK_LANG"] = c.looptrackEnv
			}
			if c.osLang != "" {
				m["LANG"] = c.osLang
			}
			code, stderr := runWithEnv(t, m, "list")
			if code != 1 {
				t.Fatalf("終了コード %d（1 のはず）: %q", code, stderr)
			}
			if !strings.HasPrefix(stderr, c.wantPrefix) {
				t.Errorf("標準エラー %q（%q で始まるはず）", stderr, c.wantPrefix)
			}
		})
	}
}
