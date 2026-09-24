package clitest

import (
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// 最重要: テストが本番に書き込まないこと。利用者の環境（LOOPTRACK_API_URL・トークン・AI のセッション・HOME）が子プロセスに渡らない。

func userEnvPolluted(t *testing.T) {
	for k, v := range map[string]string{
		"IM_API_URL":               "https://looptrack.example.com/im",
		"IM_PROJECT":               "im",
		"IM_TOKEN":                 "imp_real_user_token",
		"LOOPTRACK_SESSION_ID":     "user-session",
		"CLAUDE_PROJECT_DIR":       "/Users/someone/project",
		"CLAUDE_CODE_SESSION_ID":   "cc-user",
		"CLAUDECODE":               "1",
		"CODEX_THREAD_ID":          "codex-user",
		"COPILOT_CLI":              "1",
		"COPILOT_AGENT_SESSION_ID": "copilot-user",
		"XDG_CONFIG_HOME":          "/Users/someone/.config",
		"HOME":                     "/Users/someone",
		"SSL_CERT_FILE":            "/etc/ssl/cert.pem",
		"LOOPTRACK_API_URL":        "https://looptrack.example.com/im",
		"LOOPTRACK_PROJECT":        "im",
		"LOOPTRACK_TOKEN":          "imp_real_user_token",
	} {
		t.Setenv(k, v)
	}
}

func TestChildEnvDoesNotInheritUserEnv(t *testing.T) {
	userEnvPolluted(t)
	root := t.TempDir()
	api := "http://127.0.0.1:1/im"
	env := ChildEnv(api, filepath.Join(root, "home"), filepath.Join(root, "tmp"), nil)
	got := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}
	if got["LOOPTRACK_API_URL"] != api || got["LOOPTRACK_TOKEN"] != Token || got["LOOPTRACK_PROJECT"] != Project {
		t.Errorf("LOOPTRACK_* が偽 API 向けの既定になっていない: %v", got)
	}
	for k := range got {
		for _, p := range []string{"CLAUDE", "CODEX_", "COPILOT_", "SSL_", "LOOPTRACK_SESSION", "IM_"} {
			if strings.HasPrefix(k, p) {
				t.Errorf("利用者の環境変数 %s が子プロセスに渡る", k)
			}
		}
	}
	if !strings.HasPrefix(got["HOME"], root) || !strings.HasPrefix(got["XDG_CONFIG_HOME"], root) {
		t.Errorf("HOME・XDG_CONFIG_HOME が一時ディレクトリの外: %s %s", got["HOME"], got["XDG_CONFIG_HOME"])
	}
	if err := CheckEnv(env, api, root, nil); err != nil {
		t.Errorf("CheckEnv: %v", err)
	}
	// 上書きで消す
	env = ChildEnv(api, filepath.Join(root, "home"), filepath.Join(root, "tmp"), map[string]string{"LOOPTRACK_TOKEN": ""})
	for _, kv := range env {
		if strings.HasPrefix(kv, "LOOPTRACK_TOKEN=") {
			t.Error("LOOPTRACK_TOKEN を消せない")
		}
	}
}

func TestCheckEnvRejects(t *testing.T) {
	root := t.TempDir()
	api := "http://127.0.0.1:1/im"
	base := ChildEnv(api, filepath.Join(root, "home"), filepath.Join(root, "tmp"), nil)
	with := func(extra ...string) []string { return append(append([]string(nil), base...), extra...) }
	bad := map[string][]string{
		"本番の LOOPTRACK_API_URL":        with("LOOPTRACK_API_URL=https://looptrack.example.com/im"),
		"実際の HOME":                     with("HOME=/Users/someone"),
		"一時ディレクトリの外の設定":                with("XDG_CONFIG_HOME=" + filepath.Dir(root)),
		"明示していない CLAUDE_*":             with("CLAUDE_CODE_SESSION_ID=x"),
		"明示していない CODEX_*":              with("CODEX_THREAD_ID=x"),
		"明示していない COPILOT_*":            with("COPILOT_CLI=1"),
		"明示していない LOOPTRACK_SESSION_ID": with("LOOPTRACK_SESSION_ID=x"),
		"LOOPTRACK_API_URL":            with("LOOPTRACK_API_URL=https://looptrack.example.com/im"),
	}
	for name, env := range bad {
		if err := CheckEnv(env, api, root, nil); err == nil {
			t.Errorf("%s を通してしまう", name)
		}
	}
	// ケースが明示したものは通す
	if err := CheckEnv(with("CODEX_THREAD_ID=x"), api, root, map[string]string{"CODEX_THREAD_ID": "x"}); err != nil {
		t.Errorf("明示した変数を拒否した: %v", err)
	}
}

// 利用者の LOOPTRACK_*・IM_* が汚れていても、CLI は偽 API と偽のトークンだけを使う。
func TestRunIgnoresUserEnvGo(t *testing.T) {
	abs := goBin(t)
	userEnvPolluted(t)
	res := Run(t, GoImpl(abs), Case{Name: "env", Args: []string{"config"},
		Routes: []Route{r("GET", pProj, ok(projectRes)), r("GET", "/api/v1/me", ok(meRes))}})
	if res.Code != 0 || len(res.Requests) != 2 {
		t.Fatalf("exit %d・要求 %d 件: %s", res.Code, len(res.Requests), res.Stderr)
	}
	for _, q := range res.Requests {
		if q.Header.Get("Authorization") != "Bearer "+Token || q.Header.Get("X-Looptrack-Session") != "" {
			t.Errorf("利用者のトークン・セッションが使われた: %v", q.Header)
		}
	}
}

func TestFakeAPI(t *testing.T) {
	f := NewFakeAPI([]Route{
		rq("GET", "/api/v1/x", map[string]string{"format": "md"}, text("md")),
		r("GET", "/api/v1/x", ok(`{"n":1}`), ok(`{"n":2}`)),
		r("POST", "/api/v1/drop", Response{Drop: true}),
	})
	defer f.Close()
	get := func(path string) (int, string, string) {
		res, err := http.Get(f.URL() + path)
		if err != nil {
			return 0, "", err.Error()
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, res.Header.Get("Content-Type"), string(b)
	}
	if c, ct, b := get("/api/v1/x?format=md"); c != 200 || b != "md" || !strings.HasPrefix(ct, "text/markdown") {
		t.Errorf("クエリで選ぶ Route: %d %s %q", c, ct, b)
	}
	if _, ct, b := get("/api/v1/x"); b != `{"n":1}` || ct != "application/json" {
		t.Errorf("1 回目: %s %q", ct, b)
	}
	if _, _, b := get("/api/v1/x"); b != `{"n":2}` {
		t.Errorf("2 回目は次の応答: %q", b)
	}
	if _, _, b := get("/api/v1/x"); b != `{"n":2}` {
		t.Errorf("使い切ったら最後の応答を繰り返す: %q", b)
	}
	if c, _, b := get("/api/v1/none"); c != 404 || !strings.Contains(b, "API が見つかりません") {
		t.Errorf("フィクスチャの無い要求: %d %q", c, b)
	}
	if _, err := http.Post(f.URL()+"/api/v1/drop", "application/json", strings.NewReader(`{"a":1}`)); err == nil {
		t.Error("Drop で接続が切れない")
	}
	reqs := f.Requests()
	if len(reqs) != 6 {
		t.Fatalf("記録 %d 件", len(reqs))
	}
	if reqs[0].Path != "/im/api/v1/x" || reqs[0].Query != "format=md" || !reqs[0].Matched || reqs[4].Matched {
		t.Errorf("記録の中身: %+v", reqs[0])
	}
	if string(reqs[5].Body) != `{"a":1}` || reqs[5].Method != "POST" {
		t.Errorf("本文の記録: %+v", reqs[5])
	}
}
