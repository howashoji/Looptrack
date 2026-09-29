package core

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/hookio"
)

// bindAPI は合鍵の口（POST …/session-binds）だけを持つ偽のサーバ。
type bindAPI struct {
	srv    *httptest.Server
	mu     sync.Mutex
	reqs   []map[string]any // 受け取った本文
	paths  []string
	status int           // 0 なら 204
	body   string        // 204 以外のときの本文
	delay  time.Duration // 応答を返すまでの待ち
}

func newBindAPI(t *testing.T) *bindAPI {
	b := &bindAPI{}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var v map[string]any
		_ = json.Unmarshal(raw, &v)
		b.mu.Lock()
		b.reqs = append(b.reqs, v)
		b.paths = append(b.paths, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		st, body, delay := b.status, b.body, b.delay
		b.mu.Unlock()
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		if st == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(st)
		io.WriteString(w, body)
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *bindAPI) received() ([]map[string]any, []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]map[string]any(nil), b.reqs...), append([]string(nil), b.paths...)
}

func preToolUse(tool, useID, session string) string {
	m := map[string]any{"hook_event_name": "PreToolUse", "tool_name": tool, "tool_input": map[string]any{}}
	if useID != "" {
		m["tool_use_id"] = useID
	}
	if session != "" {
		m["session_id"] = session
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// TestSessionBindSendsOnlyForLooptrackMCP は、looptrack の MCP のツールの PreToolUse でだけ、同期で合鍵を送ることを確かめる。
// 送らない場合の検査が空振りにならないよう、同じテストの中で送る場合（対照）を先に通す。
func TestSessionBindSendsOnlyForLooptrackMCP(t *testing.T) {
	s := newSandbox(t)
	api := newBindAPI(t)
	envWith := func(extra map[string]string) *Env {
		m := map[string]string{"LOOPTRACK_API_URL": api.srv.URL + "/im"}
		for k, v := range extra {
			m[k] = v
		}
		return testEnv(s, m)
	}
	args := []string{"--agent", "claude-code"}

	// 対照: looptrack の MCP のツール。hook が返った時点で届いている（同期）
	quietRun(t, "issue-session-bind", args, preToolUse("mcp__looptrack__next", "toolu_01", "S-1"), envWith(nil), 0)
	reqs, paths := api.received()
	if len(reqs) != 1 {
		t.Fatalf("対照: 1 件送るはずが %d 件: %v", len(reqs), paths)
	}
	if paths[0] != "POST /im/api/v1/projects/tst/session-binds Bearer test-token" {
		t.Errorf("送り先: %q", paths[0])
	}
	if reqs[0]["tool_use_id"] != "toolu_01" || reqs[0]["session_id"] != "S-1" || reqs[0]["kind"] != nil {
		t.Errorf("本文: %v", reqs[0])
	}

	// 送らない: 他の MCP サーバ・MCP でないツール・PreToolUse でない・tool_use_id が無い・session_id も環境変数も無い
	for _, c := range []struct{ name, in string }{
		{"他の MCP サーバ", preToolUse("mcp__other__next", "toolu_02", "S-1")},
		{"MCP でないツール", preToolUse("Bash", "toolu_03", "S-1")},
		{"PostToolUse", `{"hook_event_name":"PostToolUse","tool_name":"mcp__looptrack__next","tool_use_id":"toolu_04","session_id":"S-1","tool_input":{},"tool_response":{}}`},
		{"tool_use_id が無い", preToolUse("mcp__looptrack__next", "", "S-1")},
		{"セッション ID が分からない", preToolUse("mcp__looptrack__next", "toolu_05", "")},
	} {
		quietRun(t, "issue-session-bind", args, c.in, envWith(nil), 0)
		if reqs, paths := api.received(); len(reqs) != 1 {
			t.Errorf("%s: 送った: %v", c.name, paths[1:])
			api.mu.Lock()
			api.reqs, api.paths = api.reqs[:1], api.paths[:1]
			api.mu.Unlock()
		}
	}

	// サーバ名の絞り込みは LOOPTRACK_MCP_SERVER（usage の plan と同じ）。変えると looptrack は外れ、指定した名前が当たる
	quietRun(t, "issue-session-bind", args, preToolUse("mcp__looptrack__next", "toolu_06", "S-1"),
		envWith(map[string]string{"LOOPTRACK_MCP_SERVER": "^issues$"}), 0)
	if reqs, _ := api.received(); len(reqs) != 1 {
		t.Errorf("LOOPTRACK_MCP_SERVER に合わないサーバで送った: %d 件", len(reqs))
	}
	quietRun(t, "issue-session-bind", args, preToolUse("mcp__issues__list_issues", "toolu_07", "S-1"),
		envWith(map[string]string{"LOOPTRACK_MCP_SERVER": "^issues$"}), 0)
	if reqs, _ := api.received(); len(reqs) != 2 || reqs[1]["tool_use_id"] != "toolu_07" {
		t.Errorf("LOOPTRACK_MCP_SERVER に合うサーバで送らない: %v", reqs)
	}

	// 入力に session_id が無ければ、環境変数（CLI の判定）があっても送らない（器の ID を MCP の操作に付けないため）
	quietRun(t, "issue-session-bind", args, preToolUse("mcp__looptrack__next", "toolu_08", ""),
		envWith(map[string]string{"CLAUDE_CODE_HOST_SESSION_ID": "H-1", "CLAUDE_CODE_SESSION_ID": "S-env"}), 0)
	if reqs, _ := api.received(); len(reqs) != 2 {
		t.Errorf("入力に session_id が無いのに送った: %v", reqs)
	}
}

// TestSessionBindFailOpen は、送れないどの場合でも何も出さずに呼び出しを通すことを確かめる
// （URL が無い・古いサーバの 404 unknown_api・拒否・サーバの誤り・時間切れ）。
func TestSessionBindFailOpen(t *testing.T) {
	s := newSandbox(t)
	api := newBindAPI(t)
	in := preToolUse("mcp__looptrack__next", "toolu_01", "S-1")
	args := []string{"--agent", "claude-code"}
	withURL := func() *Env { return testEnv(s, map[string]string{"LOOPTRACK_API_URL": api.srv.URL + "/im"}) }

	// URL が無い: 送らない
	quietRun(t, "issue-session-bind", args, in, testEnv(s, map[string]string{"LOOPTRACK_API_URL": ""}), 0)
	if reqs, _ := api.received(); len(reqs) != 0 {
		t.Errorf("URL が無いのに送った: %d 件", len(reqs))
	}
	// 届かない
	quietRun(t, "issue-session-bind", args, in, testEnv(s, map[string]string{"LOOPTRACK_API_URL": "http://127.0.0.1:1/im"}), 0)

	for _, c := range []struct {
		name   string
		status int
		body   string
	}{
		{"古いサーバ（404 unknown_api）", 404, `{"error":{"code":"unknown_api","message":"no such API"}}`},
		{"拒否（403）", 403, `{"error":{"code":"forbidden","message":"x"}}`},
		{"形の誤り（400）", 400, `{"error":{"code":"invalid_argument","message":"x"}}`},
		{"サーバの誤り（500）", 500, `{"error":{"code":"internal","message":"x"}}`},
	} {
		api.mu.Lock()
		api.status, api.body = c.status, c.body
		api.mu.Unlock()
		before, _ := api.received()
		quietRun(t, "issue-session-bind", args, in, withURL(), 0)
		// 対照: 実際に送って、その応答を受けたうえで黙っている（送らずに黙ったのではない）
		if after, _ := api.received(); len(after) != len(before)+1 {
			t.Errorf("%s: 前提が崩れています（送っていない）: %d → %d 件", c.name, len(before), len(after))
		}
	}

	// 時間切れ: サーバが応答を返さなくても、待ち時間（1.5 秒）で打ち切って黙って通す
	api.mu.Lock()
	api.status, api.body, api.delay = 0, "", 10*time.Second
	api.mu.Unlock()
	// 上限 2.5 秒: 1.5 秒の打ち切りが外れると、登録表の打ち切り（3 秒）かサーバの応答（10 秒）まで待つので越える。
	// 下限 1 秒: 実際に時間切れの経路を通った（待たずに返ったのではない）ことを確かめる。
	start := time.Now()
	quietRun(t, "issue-session-bind", args, in, withURL(), 0)
	if d := time.Since(start); d > 2500*time.Millisecond {
		t.Errorf("1.5 秒の打ち切りが効いていない: %s 待った（2.5 秒以内のはず）", d)
	} else if d < time.Second {
		t.Errorf("前提が崩れています: 時間切れの経路を通っていない（%s で返った。1 秒以上待つはず）", d)
	}
}

// hookOut は hook を 1 回動かし、終了コードと標準出力の JSON から systemMessage を取り出す。
func hookOut(t *testing.T, name string, args []string, stdin string, e *Env) (code int, system, raw string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code = Main(context.Background(), name, args, strings.NewReader(stdin), &stdout, &stderr, e)
	if stderr.Len() > 0 {
		t.Errorf("%s: 標準エラーに出た: %q", name, stderr.String())
	}
	raw = stdout.String()
	if raw != "" {
		var v struct {
			SystemMessage string `json:"systemMessage"`
		}
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			t.Fatalf("%s: 出力が JSON でない: %q: %v", name, raw, err)
		}
		system = v.SystemMessage
	}
	return code, system, raw
}

// TestMCPServerInvalidRegexpNotice は、LOOPTRACK_MCP_SERVER が RE2 で読めないとき、MCP のツールの呼び出しでだけ
// 変数名と誤りを 1 行で知らせ（結び付けは既定に戻さず「一致しない」のまま）、MCP でないツールでは何も出さないことを確かめる。
// 対照として、読める正規表現なら知らせが出ずに結び付くことを同じテストの中で先に通す。
// PostToolUse の usage も同じ知らせを返す（PostToolUse の hook の出力は systemMessage で利用者に届く）。
func TestMCPServerInvalidRegexpNotice(t *testing.T) {
	s := newSandbox(t)
	api := newBindAPI(t)
	// 先読みは RE2 で読めない
	const bad = "(?=looptrack)looptrack"
	envWith := func(server string) *Env {
		// 文面を見るので言語を固定する（既定は英語なので、指定が無いと実行する機械の LANG で文面が変わる）
		return testEnv(s, map[string]string{"LOOPTRACK_API_URL": api.srv.URL + "/im", "LOOPTRACK_LANG": "ja", "LOOPTRACK_MCP_SERVER": server})
	}
	args := []string{"--agent", "claude-code"}
	sent := func() int { r, _ := api.received(); return len(r) }

	// 対照: 読める正規表現。知らせは出ず、当たるサーバ名で結び付く
	code, _, raw := hookOut(t, "issue-session-bind", args, preToolUse("mcp__lt__next", "toolu_01", "S-1"), envWith("looptrack|lt"))
	if code != 0 || raw != "" || sent() != 1 {
		t.Fatalf("対照: 知らせなしで結び付くはず: exit %d 出力 %q 送信 %d 件", code, raw, sent())
	}

	// 読めない正規表現 × MCP のツール: 知らせが 1 行出る。結び付けはしない（looptrack のサーバでも、既定に戻さない）
	for _, tool := range []string{"mcp__looptrack__next", "mcp__other__next"} {
		var msg string
		code, msg, raw = hookOut(t, "issue-session-bind", args, preToolUse(tool, "toolu_02", "S-1"), envWith(bad))
		if code != 0 {
			t.Errorf("%s: exit %d", tool, code)
		}
		if !strings.Contains(msg, "LOOPTRACK_MCP_SERVER") || !strings.Contains(msg, "RE2") || !strings.Contains(msg, "invalid or unsupported Perl syntax") {
			t.Errorf("%s: 変数名かコンパイルの誤りが知らせに無い: %q", tool, msg)
		}
		if strings.Contains(msg, "\n") || strings.Contains(strings.TrimSpace(msg), "\n") || strings.Count(strings.TrimSpace(msg), "\n") > 0 {
			t.Errorf("%s: 1 行のはず: %q", tool, msg)
		}
		if sent() != 1 {
			t.Errorf("%s: 読めない正規表現なのに結び付けた: %d 件", tool, sent())
		}
	}

	// 読めない正規表現 × MCP でないツール（Bash・名前が mcp__ で始まらないだけのツール）: 何も出さない
	for _, c := range []struct{ name, in string }{
		{"Bash", preToolUse("Bash", "toolu_03", "S-1")},
		{"mcp を名乗るだけのツール名", preToolUse("mcp_looptrack_next", "toolu_04", "S-1")},
	} {
		if code, _, raw := hookOut(t, "issue-session-bind", args, c.in, envWith(bad)); code != 0 || raw != "" {
			t.Errorf("%s: 何も出さないはず: exit %d 出力 %q", c.name, code, raw)
		}
	}

	// usage（PostToolUse）: 同じ条件で同じ知らせ。Bash では出さない。読める正規表現なら plan が返り、知らせは無い
	post := func(tool string) string {
		return `{"hook_event_name":"PostToolUse","session_id":"S-1","tool_name":"` + tool + `","tool_input":{"id":"TST-0001"},"tool_response":{}}`
	}
	_, msg, _ := hookOut(t, "usage", args, post("mcp__looptrack__add_comment"), envWith(bad))
	if !strings.Contains(msg, "LOOPTRACK_MCP_SERVER") || !strings.Contains(msg, "invalid or unsupported Perl syntax") {
		t.Errorf("usage: 変数名かコンパイルの誤りが知らせに無い: %q", msg)
	}
	if code, _, raw := hookOut(t, "usage", args, post("Bash"), envWith(bad)); code != 0 || raw != "" {
		t.Errorf("usage の Bash: 何も出さないはず: exit %d 出力 %q", code, raw)
	}
	c := &Call{Env: envWith("looptrack|lt").withDefaults()}
	ev := hookio.FromMap(map[string]any{"hook_event_name": "PostToolUse", "session_id": "S-1", "tool_name": "mcp__lt__add_comment",
		"tool_input": map[string]any{"id": "TST-0001"}, "tool_response": map[string]any{}},
		hookio.ParseOptions{Agent: hookio.ClaudeCode, Getenv: func(string) string { return "" }})
	if p, notice := c.planNotice(ev, "claude-code"); p == nil || p.IssueID != "TST-0001" || notice != "" {
		t.Errorf("usage の対照: 読める正規表現なら plan が返り知らせは無いはず: %+v %q", p, notice)
	}
}
