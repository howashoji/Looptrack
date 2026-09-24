package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/store"
)

// CLI 内蔵の付与（looptrack issue の変更操作の直後）と、フック（looptrack hook usage）からの
// 送信が実際にサーバへ届く。会話記録は Claude Code の形で合成する。

func claudeTranscript(t *testing.T, home, session string, extra ...string) string {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", "-Users-x-proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := func(kind, ts, body string) string {
		return fmt.Sprintf(`{"type":%q,"timestamp":%q,"gitBranch":"main","version":"2.1.271",%s}`, kind, ts, body)
	}
	lines := []string{
		line("user", "2026-09-18T01:00:00.000Z", `"origin":{"kind":"human"},"message":{"role":"user","content":"最初の指示"}`),
		line("assistant", "2026-09-18T01:00:10.000Z", `"message":{"id":"m1","model":"claude-opus-5","usage":{"input_tokens":10,"cache_creation_input_tokens":100,"cache_read_input_tokens":1000,"output_tokens":50}}`),
	}
	lines = append(lines, extra...)
	p := filepath.Join(dir, session+".jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestUsageFromCLIAndHook(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("req")
	u := e.user("editor", "editor-password-1", "member")
	if err := store.SetMember(ctx, e.db, pr.ID, u.ID, "editor"); err != nil {
		t.Fatal(err)
	}
	token := e.apiAs(u).token
	home := t.TempDir()
	transcript := claudeTranscript(t, home, "sess-cli")
	// usage-last・usage-spool の置き場は資格情報と同じ looptrack の置き場（internal/client/cred）: Windows は %APPDATA%\looptrack
	// （cliHomeEnv の APPDATA）、他は $XDG_CONFIG_HOME/looptrack（下の run の XDG_CONFIG_HOME）。
	state := filepath.Join(home, "cfg", "looptrack")
	if runtime.GOOS == "windows" {
		state = filepath.Join(home, "AppData", "Roaming", "looptrack")
	}

	// run は looptrack を起動する。script は以前の CLI のファイル名に当たるもの（"issue" → looptrack issue、
	// "usage-hook" → looptrack hook usage --agent claude-code）
	run := func(stdin string, env []string, script string, args ...string) (int, string) {
		t.Helper()
		argv := append([]string{"issue"}, args...)
		if script == "usage-hook" {
			argv = append([]string{"hook", "usage", "--agent", "claude-code"}, args...)
		}
		base := append(cliAPIEnv(e.srv.URL+"/im", "req", token, home), "XDG_CONFIG_HOME="+filepath.Join(home, "cfg"), "CLAUDE_PROJECT_DIR="+home,
			"LOOPTRACK_USAGE_FOREGROUND=1", "LOOPTRACK_USAGE_DEBUG=1")
		r := runCLI(t, home, append(base, env...), stdin, argv...)
		return r.code, r.stdout + r.stderr
	}
	count := func(where string, args ...any) int {
		t.Helper()
		var n int
		if err := e.db.QueryRow("SELECT COUNT(*) FROM usage_snapshots WHERE "+where, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// ① CLI 内蔵: Claude Code のセッションの中（CLAUDE_CODE_SESSION_ID）で new → comment → status → push
	inClaude := []string{"CLAUDE_CODE_SESSION_ID=sess-cli", "CLAUDECODE=1"}
	if code, out := run("", inClaude, "issue", "new", "付与される起票"); code != 0 || !strings.Contains(out, "REQ-0001") {
		t.Fatalf("new: %d %s", code, out)
	}
	if n := count("trigger_kind = 'issue_op' AND op = 'create' AND via = 'cli' AND client = 'claude-code' AND session_id = 'sess-cli'"); n != 1 {
		t.Errorf("create のスナップショット = %d", n)
	}
	// 累計が増えたことを会話記録に足す（同じ累計だと重複扱いになる）
	claudeTranscript(t, home, "sess-cli",
		`{"type":"assistant","timestamp":"2026-09-18T01:01:00.000Z","message":{"id":"m2","model":"claude-opus-5","usage":{"input_tokens":20,"cache_creation_input_tokens":0,"cache_read_input_tokens":2000,"output_tokens":80}}}`)
	run("", inClaude, "issue", "comment", "REQ-0001", "コメント")
	run("", inClaude, "issue", "status", "REQ-0001", "In Progress")
	if n := count("issue_id IS NOT NULL AND op IN ('comment', 'status')"); n != 2 {
		t.Errorf("comment / status のスナップショット = %d", n)
	}
	// 人がターミナルから打った操作（セッションの変数なし）は送らない
	before := count("1=1")
	if code, out := run("", nil, "issue", "comment", "REQ-0001", "端末から"); code != 0 {
		t.Fatalf("comment（端末）: %d %s", code, out)
	}
	if n := count("1=1"); n != before {
		t.Errorf("端末からの操作で増えた: %d → %d", before, n)
	}

	// 変数があっても会話記録が無ければ送らない（操作は成功する）
	if code, out := run("", []string{"CLAUDE_CODE_SESSION_ID=no-such-session", "CLAUDECODE=1"}, "issue", "comment", "REQ-0001", "記録なし"); code != 0 || !strings.Contains(out, "会話記録が見つからない") {
		t.Errorf("記録なし: %d %s", code, out)
	}

	// usage show / attach
	if code, out := run("", inClaude, "issue", "usage", "show", "REQ-0001"); code != 0 || !strings.Contains(out, "REQ-0001 トークン消費: 合計") {
		t.Errorf("usage show: %d %s", code, out)
	}
	var shown struct {
		StageCount int `json:"stage_count"`
	}
	_, out := run("", inClaude, "issue", "usage", "show", "REQ-0001", "--json")
	if err := json.Unmarshal([]byte(out), &shown); err != nil || shown.StageCount < 3 {
		t.Errorf("usage show --json: %v %s", err, out)
	}
	claudeTranscript(t, home, "sess-cli",
		`{"type":"assistant","timestamp":"2026-09-18T01:02:00.000Z","message":{"id":"m3","model":"claude-opus-5","usage":{"input_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":1,"output_tokens":1}}}`)
	if code, out := run("", inClaude, "issue", "usage", "attach", "REQ-0001"); code != 0 || !strings.Contains(out, "トークン情報: 付与") {
		t.Errorf("usage attach: %d %s", code, out)
	}
	if n := count("trigger_kind = 'manual' AND issue_id IS NOT NULL"); n != 1 {
		t.Errorf("manual のスナップショット = %d", n)
	}
	if code, out := run("", nil, "issue", "usage", "attach", "REQ-0001"); code == 0 || !strings.Contains(out, "会話記録が見つかりません") {
		t.Errorf("端末からの attach: %d %s", code, out)
	}

	// ② フック: MCP の操作（PostToolUse）と Stop / SessionEnd。stdin にフックの JSON
	hookIn := func(event, tool, input, response string) string {
		return fmt.Sprintf(`{"hook_event_name":%q,"session_id":"sess-cli","transcript_path":%q,"cwd":%q,"tool_name":%q,"tool_input":%s,"tool_response":%s,"tool_use_id":"toolu_9"}`,
			event, transcript, home, tool, input, response)
	}
	if code, out := run(hookIn("PostToolUse", "mcp__looptrack__add_comment", `{"id":"REQ-0001","text":"x"}`, `{"content":[{"type":"text","text":"ok"}]}`), nil, "usage-hook"); code != 0 {
		t.Fatalf("hook PostToolUse: %d %s", code, out)
	}
	if n := count("via = 'mcp' AND op = 'comment' AND issue_id IS NOT NULL"); n != 1 {
		t.Errorf("MCP の操作のスナップショット = %d", n)
	}
	// 同じ tool_use_id の再送は増えない
	run(hookIn("PostToolUse", "mcp__looptrack__add_comment", `{"id":"REQ-0001","text":"x"}`, `{}`), nil, "usage-hook")
	if n := count("via = 'mcp'"); n != 1 {
		t.Errorf("再送で増えた: %d", n)
	}
	// 起票は応答の文から ID を取る
	claudeTranscript(t, home, "sess-cli",
		`{"type":"assistant","timestamp":"2026-09-18T01:03:00.000Z","message":{"id":"m4","model":"claude-opus-5","usage":{"input_tokens":2,"cache_creation_input_tokens":0,"cache_read_input_tokens":2,"output_tokens":2}}}`)
	e.apiAs(u).json(201, "POST", "/projects/req/issues", map[string]any{"title": "MCP で起票"}, nil)
	in := strings.Replace(hookIn("PostToolUse", "mcp__looptrack__create_issue", `{"title":"MCP で起票"}`, `{"content":[{"type":"text","text":"作成: REQ-0002 MCP で起票（version 1）"}]}`), `"toolu_9"`, `"toolu_10"`, 1)
	run(in, nil, "usage-hook")
	if n := count("via = 'mcp' AND op = 'create'"); n != 1 {
		t.Errorf("MCP の起票のスナップショット = %d", n)
	}
	// 読むだけのツール・別サーバ・Bash は送らない
	before = count("1=1")
	run(hookIn("PostToolUse", "mcp__looptrack__get_issue", `{"id":"REQ-0001"}`, `{}`), nil, "usage-hook")
	run(hookIn("PostToolUse", "mcp__linear__save_issue", `{"id":"REQ-0001"}`, `{}`), nil, "usage-hook")
	run(hookIn("PostToolUse", "Bash", `{"command":"looptrack issue comment REQ-0001 x"}`, `{}`), nil, "usage-hook")
	if n := count("1=1"); n != before {
		t.Errorf("送らないはずのフックで増えた: %d → %d", before, n)
	}
	// Stop は区間つきで送り、10 分以内の 2 回目は間引く。SessionEnd は間引かない
	// （直前の MCP の送信で「送った時刻」が付いているので、いったん消してから）
	os.Remove(filepath.Join(state, "usage-last", "sess-cli"))
	if code, out := run(hookIn("Stop", "", `{}`, `{}`), nil, "usage-hook"); code != 0 || count("trigger_kind = 'stop' AND segments IS NOT NULL") != 1 {
		t.Errorf("stop のスナップショット: %d %s", code, out)
	}
	run(hookIn("Stop", "", `{}`, `{}`), nil, "usage-hook")
	if n := count("trigger_kind = 'stop'"); n != 1 {
		t.Errorf("間引かれていない: %d", n)
	}
	claudeTranscript(t, home, "sess-cli",
		`{"type":"assistant","timestamp":"2026-09-18T01:04:00.000Z","message":{"id":"m5","model":"claude-opus-5","usage":{"input_tokens":3,"cache_creation_input_tokens":0,"cache_read_input_tokens":3,"output_tokens":3}}}`)
	run(hookIn("SessionEnd", "", `{}`, `{}`), nil, "usage-hook")
	if n := count("trigger_kind = 'session_end'"); n != 1 {
		t.Errorf("session_end のスナップショット = %d", n)
	}

	// ③ 送れないときは溜め、次に送れたときに届く（サーバの URL を間違えて 1 回、正しい URL で 1 回）
	claudeTranscript(t, home, "sess-cli",
		`{"type":"assistant","timestamp":"2026-09-18T01:05:00.000Z","message":{"id":"m6","model":"claude-opus-5","usage":{"input_tokens":4,"cache_creation_input_tokens":0,"cache_read_input_tokens":4,"output_tokens":4}}}`)
	run(hookIn("SessionEnd", "", `{}`, `{}`), []string{"LOOPTRACK_API_URL=http://127.0.0.1:1/im", "LOOPTRACK_USAGE_TIMEOUT=1"}, "usage-hook")
	spool, _ := filepath.Glob(filepath.Join(state, "usage-spool", "*.json"))
	if len(spool) != 1 {
		t.Fatalf("スプール = %v", spool)
	}
	claudeTranscript(t, home, "sess-cli",
		`{"type":"assistant","timestamp":"2026-09-18T01:06:00.000Z","message":{"id":"m7","model":"claude-opus-5","usage":{"input_tokens":5,"cache_creation_input_tokens":0,"cache_read_input_tokens":5,"output_tokens":5}}}`)
	run(hookIn("SessionEnd", "", `{}`, `{}`), nil, "usage-hook")
	if n := count("trigger_kind = 'session_end'"); n != 3 {
		t.Errorf("スプールの再送後の session_end = %d, want 3", n)
	}
	if spool, _ = filepath.Glob(filepath.Join(state, "usage-spool", "*.json")); len(spool) != 0 {
		t.Errorf("スプールが残っている: %v", spool)
	}

	// LOOPTRACK_USAGE=0 で切れる
	before = count("1=1")
	run("", append(inClaude, "LOOPTRACK_USAGE=0"), "issue", "comment", "REQ-0001", "切った")
	if n := count("1=1"); n != before {
		t.Errorf("LOOPTRACK_USAGE=0 でも増えた")
	}
}
