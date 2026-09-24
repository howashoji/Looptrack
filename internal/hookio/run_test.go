package hookio

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func envOf(kv ...string) Getenv {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return func(k string) string { return m[k] }
}

func runOpts(agent Agent, getenv Getenv) RunOptions {
	return RunOptions{Parse: ParseOptions{Agent: agent, Getenv: getenv, GitRoot: func(string) string { return "" }, Getwd: func() string { return "/w" }}}
}

// fail-open: 入力が読めない・本体のエラー・panic・時間切れ・Claude Code 以外からの起動は、何も出さずに exit 0。
func TestRunFailOpen(t *testing.T) {
	claude := envOf("CLAUDECODE", "1")
	stop := `{"hook_event_name":"Stop","session_id":"s1"}`
	block := func(Event) (Result, error) { return Result{Block: "止める"}, nil }
	cases := []struct {
		name    string
		input   string
		getenv  Getenv
		timeout time.Duration
		h       Handler
		called  bool
	}{
		{"JSON でない", "{not json", claude, 0, block, false},
		{"オブジェクトでない", `["a"]`, claude, 0, block, false},
		{"本体のエラー", stop, claude, 0, func(Event) (Result, error) { return Result{Block: "x"}, errors.New("失敗") }, true},
		{"panic", stop, claude, 0, func(Event) (Result, error) { panic("壊れた") }, true},
		{"時間切れ", stop, claude, 20 * time.Millisecond, func(Event) (Result, error) {
			time.Sleep(500 * time.Millisecond)
			return Result{Block: "遅すぎる"}, nil
		}, true},
		{"Copilot が .claude/settings.json の配線を起動", stop, envOf("VSCODE_PID", "1"), 0, block, false},
		{"camelCase の入力が Claude Code 向けの配線に来た", `{"sessionId":"s1","toolName":"bash"}`, envOf(), 0, block, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var calledFlag atomic.Bool // 時間切れのケースでは本体が Run の後も動き続ける
			h := func(ev Event) (Result, error) { calledFlag.Store(true); return c.h(ev) }
			var out, errw bytes.Buffer
			opts := runOpts(ClaudeCode, c.getenv)
			opts.Timeout = c.timeout
			code := Run(strings.NewReader(c.input), &out, &errw, opts, h)
			if code != 0 || out.Len() != 0 || errw.Len() != 0 {
				t.Errorf("exit %d stdout %q stderr %q, want 0 と出力なし", code, out.String(), errw.String())
			}
			if called := calledFlag.Load(); called != c.called {
				t.Errorf("本体を呼んだ = %v, want %v", called, c.called)
			}
		})
	}
}

func TestRunDebugWritesStderrOnly(t *testing.T) {
	var out, errw bytes.Buffer
	opts := runOpts(ClaudeCode, envOf("CLAUDECODE", "1"))
	opts.Debug = true
	code := Run(strings.NewReader(`{"hook_event_name":"Stop"}`), &out, &errw, opts, func(Event) (Result, error) { panic("壊れた") })
	if code != 0 || out.Len() != 0 || !strings.Contains(errw.String(), "panic: 壊れた") {
		t.Errorf("exit %d stdout %q stderr %q", code, out.String(), errw.String())
	}
}

func TestRunWritesResult(t *testing.T) {
	var out bytes.Buffer
	var got Event
	code := Run(strings.NewReader(`{"hook_event_name":"Stop","session_id":"s1","stop_hook_active":false}`), &out, nil,
		runOpts(ClaudeCode, envOf("CLAUDECODE", "1")), func(ev Event) (Result, error) {
			got = ev
			return Result{Block: "引き継ぎを更新して <b>&</b>"}, nil
		})
	if code != 0 || got.Name != Stop || got.SessionID != "s1" {
		t.Fatalf("exit %d event %+v", code, got)
	}
	// HTML のエスケープをしない（AI と人が読む）
	if want := `{"decision":"block","reason":"引き継ぎを更新して <b>&</b>"}` + "\n"; out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
	// 空の入力は {}（イベント名は --event から）
	opts := runOpts(Codex, envOf())
	opts.Parse.Event = "Stop"
	Run(strings.NewReader(""), &out, nil, opts, func(ev Event) (Result, error) { got = ev; return Result{}, nil })
	if got.Name != Stop || got.Agent != Codex {
		t.Errorf("空の入力: %+v", got)
	}
}

func TestRecover(t *testing.T) {
	code := 3
	var seen any
	func() {
		defer Recover(&code, func(p any, stack []byte) { seen = p })
		panic("x")
	}()
	if code != 0 || seen != "x" {
		t.Errorf("code %d seen %v", code, seen)
	}
	code = 3
	func() { defer Recover(&code, nil) }()
	if code != 3 {
		t.Errorf("panic が無ければ変えない: %d", code)
	}
}

func TestParseArgs(t *testing.T) {
	opts, rest, err := ParseArgs([]string{"--agent=copilot", "extra", "--event", "agentStop", "--no-block", "--limit", "3"}, envOf())
	if err != nil || opts.Parse.Agent != Copilot || opts.Parse.Event != "agentStop" || !opts.Render.NoBlock ||
		strings.Join(rest, " ") != "extra --limit 3" {
		t.Errorf("opts %+v rest %v err %v", opts, rest, err)
	}
	if opts, _, _ := ParseArgs(nil, envOf("LOOPTRACK_LOOP_NO_BLOCK", "1", "LOOPTRACK_HOOK_DEBUG", "1")); !opts.Render.NoBlock || !opts.Debug {
		t.Errorf("LOOPTRACK_LOOP_NO_BLOCK・LOOPTRACK_HOOK_DEBUG: %+v", opts)
	}
	for _, args := range [][]string{{"--agent", "cursor"}, {"--agent"}, {"--event"}} {
		if _, _, err := ParseArgs(args, envOf()); err == nil {
			t.Errorf("%v は error のはず", args)
		}
	}
}

// ForeignHost は以前の hook（1.0.0 より前）と同じ判定（以前のテストの「起動した AI の判定」と同じ観点）。
func TestForeignHost(t *testing.T) {
	snake := map[string]any{"hook_event_name": "Stop", "session_id": "s"}
	camel := map[string]any{"sessionId": "s", "toolName": "bash"}
	cases := []struct {
		name string
		env  Getenv
		raw  map[string]any
		want bool
	}{
		{"Claude Code", envOf("CLAUDECODE", "1", "VSCODE_PID", "1"), snake, false},
		{"Claude Code（CLAUDE_PROJECT_DIR だけ）", envOf("CLAUDE_PROJECT_DIR", "/p", "COPILOT_AGENT", "1"), camel, false},
		{"Codex", envOf("CODEX_THREAD_ID", "t", "VSCODE_PID", "1"), snake, false},
		{"Copilot 向けの配線（LOOPTRACK_LOOP_AGENT）", envOf("LOOPTRACK_LOOP_AGENT", "copilot", "COPILOT_AGENT", "1"), snake, false},
		{"Gemini CLI", envOf("GEMINI_CLI", "1", "VSCODE_PID", "1"), snake, false},
		{"Codex の会話記録", envOf("VSCODE_PID", "1"), map[string]any{"hook_event_name": "Stop", "transcript_path": "/h/.codex/s.jsonl"}, false},
		{"VS Code の Copilot", envOf("VSCODE_PID", "1"), snake, true},
		{"Copilot CLI", envOf("COPILOT_CLI", "1"), snake, true},
		{"AI_AGENT が Copilot", envOf("AI_AGENT", "github_copilot_vscode_agent"), snake, true},
		{"AI_AGENT が Claude Code 自身（2.1.275 で実測）", envOf("AI_AGENT", "claude-code_2.1.275_agent"), snake, false},
		{"camelCase の入力", envOf(), camel, true},
		{"印も入力も無い", envOf(), map[string]any{}, false},
		{"snake_case で印なし", envOf(), snake, false},
		// Copilot CLI 1.0.86 は .claude/settings.json の hook に CLAUDE_PROJECT_DIR・COPILOT_CLI=1・COPILOT_PROJECT_DIR を渡す
		// （CLAUDECODE は無い。Claude Code から起動された Copilot なら CLAUDECODE・AI_AGENT=claude-code_… も受け継ぐ）
		{"Copilot CLI が起動した .claude の hook（CLAUDE_PROJECT_DIR あり・CLAUDECODE なし）",
			envOf("CLAUDE_PROJECT_DIR", "/p", "COPILOT_CLI", "1", "COPILOT_CLI_BINARY_VERSION", "1.0.86"), snake, true},
		{"Copilot CLI が起動した .claude の hook（COPILOT_PROJECT_DIR あり）",
			envOf("CLAUDE_PROJECT_DIR", "/p", "COPILOT_CLI", "1", "COPILOT_PROJECT_DIR", "/p"), snake, true},
		{"Claude Code から起動された Copilot CLI（CLAUDECODE を受け継ぐ）",
			envOf("CLAUDECODE", "1", "AI_AGENT", "claude-code_2-1-275_agent", "CLAUDE_PROJECT_DIR", "/p", "COPILOT_CLI", "1", "COPILOT_PROJECT_DIR", "/p"), snake, true},
		{"Copilot CLI のシェルから起動した Claude Code（COPILOT_CLI を受け継ぐ・COPILOT_PROJECT_DIR は無い）",
			envOf("CLAUDECODE", "1", "CLAUDE_PROJECT_DIR", "/p", "COPILOT_CLI", "1", "COPILOT_AGENT_SESSION_ID", "c"), snake, false},
		{"Copilot CLI のシェルから起動した Codex", envOf("CODEX_THREAD_ID", "t", "COPILOT_CLI", "1"), snake, false},
		{"Copilot 向けの配線（LOOPTRACK_LOOP_AGENT）は COPILOT_PROJECT_DIR があっても自分の配線",
			envOf("LOOPTRACK_LOOP_AGENT", "copilot", "COPILOT_CLI", "1", "COPILOT_PROJECT_DIR", "/p", "CLAUDE_PROJECT_DIR", "/p"), snake, false},
	}
	for _, c := range cases {
		if got := ForeignHost(c.env, c.raw); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestDetectAgent(t *testing.T) {
	cases := []struct {
		name string
		env  Getenv
		raw  map[string]any
		want Agent
	}{
		{"LOOPTRACK_LOOP_AGENT が最優先", envOf("LOOPTRACK_LOOP_AGENT", "copilot", "CLAUDECODE", "1"), nil, Copilot},
		{"Claude Code を VS Code の端末から", envOf("CLAUDECODE", "1", "VSCODE_PID", "1"), nil, ClaudeCode},
		{"Codex の環境変数", envOf("CODEX_THREAD_ID", "t"), nil, Codex},
		{"Codex の会話記録", envOf(), map[string]any{"transcript_path": "/h/.codex/sessions/r.jsonl"}, Codex},
		{"camelCase の入力", envOf(), map[string]any{"sessionId": "s"}, Copilot},
		{"Copilot の会話記録", envOf(), map[string]any{"transcriptPath": "/h/.copilot/session-state/s/events.jsonl"}, Copilot},
		{"Copilot CLI のセッション ID", envOf("COPILOT_AGENT_SESSION_ID", "s"), nil, Copilot},
		{"Claude Code の会話記録", envOf(), map[string]any{"transcript_path": "/h/.claude/projects/p/s.jsonl"}, ClaudeCode},
		{"Copilot CLI が起動した hook（CLAUDE_PROJECT_DIR・受け継いだ CLAUDECODE があっても）",
			envOf("COPILOT_PROJECT_DIR", "/p", "CLAUDE_PROJECT_DIR", "/p", "CLAUDECODE", "1"), nil, Copilot},
		{"分からない", envOf(), map[string]any{"hook_event_name": "Stop"}, ""},
	}
	for _, c := range cases {
		got, ok := DetectAgent(c.env, c.raw)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("%s: %q %v, want %q", c.name, got, ok, c.want)
		}
	}
}

func TestToolMatchMCPAndAccessors(t *testing.T) {
	cases := []struct {
		agent Agent
		name  string
		kind  ToolKind
	}{
		{ClaudeCode, "mcp__im-oauth__set_status", KindMCP},
		{Codex, "mcp__im-oauth__set_status", KindMCP},
		{Copilot, "im-oauth-set_status", KindMCP},     // CLI
		{Copilot, "mcp_im-oauth_set_status", KindMCP}, // VS Code（推論）
		{Copilot, "mcp__im-oauth__set_status", KindMCP},
	}
	for _, c := range cases {
		tl := newTool(c.agent, c.name, map[string]any{})
		if tl.Kind != c.kind || !tl.MatchMCP("im-oauth", "set_status") || tl.MatchMCP("im", "set_status") {
			t.Errorf("%s %s: %+v", c.agent, c.name, tl)
		}
	}
	// Claude Code・Codex では <サーバ>-<ツール> を MCP とみなさない（Copilot CLI だけの形）
	if tl := newTool(ClaudeCode, "im-set_status", nil); tl.Kind != KindOther {
		t.Errorf("Claude Code の im-set_status: %+v", tl)
	}
	// 名前の推定がずれても MatchMCP は名前全体で当てる（VS Code でサーバ名に _ がある）
	if tl := newTool(Copilot, "mcp_my_srv_add_comment", nil); !tl.MatchMCP("my_srv", "add_comment") {
		t.Errorf("mcp_my_srv_add_comment: %+v", tl)
	}
	if tl := newTool(Copilot, "create_file", map[string]any{"filePath": "/w/a.go"}); tl.FilePath() != "/w/a.go" || tl.Kind != KindWrite {
		t.Errorf("filePath: %+v", tl)
	}
	if tl := newTool(Copilot, "bash", `{"command":"ls"}`); tl.Command() != "ls" {
		t.Errorf("toolArgs の文字列: %+v", tl)
	}
	if tl := newTool(Copilot, "bash", "not json"); tl.Input != nil || tl.InputText != "not json" {
		t.Errorf("JSON でない toolArgs: %+v", tl)
	}
	var nilTool *Tool
	if nilTool.Command() != "" || nilTool.FilePath() != "" || nilTool.MatchMCP("im", "x") {
		t.Error("nil の Tool")
	}
}

// ParseOutput は既存の hook の出力（exit 2 + stderr など）も読む（以前の hook（1.0.0 より前）の形）。
func TestParseOutputExitCodes(t *testing.T) {
	cases := []struct {
		name  string
		agent Agent
		event Name
		out   Output
		want  Result
	}{
		{"鮮度ガードの exit 2 + stderr", ClaudeCode, Stop, Output{Stderr: "⛔ 更新して\n", ExitCode: 2}, Result{Block: "⛔ 更新して"}},
		{"PreToolUse の exit 2 は拒否", ClaudeCode, PreToolUse, Output{Stderr: "だめ", ExitCode: 2}, Result{Deny: "だめ"}},
		{"Claude Code の exit 1 は止めないエラー", ClaudeCode, PreToolUse, Output{Stderr: "壊れた", ExitCode: 1}, Result{}},
		{"Copilot の preToolUse の exit 1 は拒否（fail closed）", Copilot, PreToolUse, Output{Stderr: "壊れた", ExitCode: 1}, Result{Deny: "壊れた"}},
		{"Copilot の postToolUse の exit 1 は続行", Copilot, PostToolUse, Output{ExitCode: 1}, Result{}},
		{"SessionStart の JSON でない stdout は文脈", ClaudeCode, SessionStart, Output{Stdout: "イシュー一覧\n"}, Result{Context: "イシュー一覧"}},
		{"Stop の JSON でない stdout は捨てる", ClaudeCode, Stop, Output{Stdout: "log\n"}, Result{}},
		{"以前の PreToolUse の decision: block", ClaudeCode, PreToolUse, Output{Stdout: `{"decision":"block","reason":"r"}`}, Result{Deny: "r"}},
		{"permissionDecision: allow は何もしない", ClaudeCode, PreToolUse, Output{Stdout: `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}`}, Result{}},
	}
	for _, c := range cases {
		if got := ParseOutput(c.agent, c.event, c.out); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

// 以前の kit の hook（Copilot 向けに整えた出力）を ParseOutput で読むと Go の Render と同じ Result になる。
func TestParseOutputReadsKitCopilotShape(t *testing.T) {
	// 以前の kit の stop-handoff-freshness が LOOPTRACK_LOOP_AGENT=copilot で出した形（JSON の区切りもそのまま）
	kit := `{"decision": "block", "reason": "古い", "hookSpecificOutput": {"hookEventName": "Stop", "decision": "block", "reason": "古い"}}`
	if got := ParseOutput(Copilot, Stop, Output{Stdout: kit + "\n"}); got != (Result{Block: "古い"}) {
		t.Errorf("Stop: %+v", got)
	}
	kit = `{"hookSpecificOutput": {"hookEventName": "SessionStart", "additionalContext": "規律"}, "additionalContext": "規律"}`
	if got := ParseOutput(Copilot, SessionStart, Output{Stdout: kit}); got != (Result{Context: "規律"}) {
		t.Errorf("SessionStart: %+v", got)
	}
}

func TestGitRoot(t *testing.T) {
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	wt := filepath.Join(tmp, "wt") // worktree・submodule は .git がファイル
	outside := filepath.Join(tmp, "plain", "sub")
	for _, d := range []string{filepath.Join(repo, ".git"), filepath.Join(repo, "a", "b"), wt, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: ../repo/.git/worktrees/wt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for dir, want := range map[string]string{
		filepath.Join(repo, "a", "b"): repo, repo: repo, wt: wt, outside: "", "": "",
	} {
		// t.TempDir の上（macOS の /var/folders…）が git の中でないことは前提にしない＝ tmp の外で見つかったら "" と同じ扱い
		got := GitRoot(dir)
		if want == "" && got != "" && strings.HasPrefix(got+string(filepath.Separator), tmp+string(filepath.Separator)) {
			t.Errorf("GitRoot(%q) = %q, want 外（tmp の中に .git は無い）", dir, got)
		} else if want != "" && got != want {
			t.Errorf("GitRoot(%q) = %q, want %q", dir, got, want)
		}
	}
	// ProjectDir: CLAUDE_PROJECT_DIR → cwd の git のルート → cwd
	ev, _ := Parse([]byte(`{"hook_event_name":"Stop","cwd":"`+filepath.ToSlash(filepath.Join(repo, "a"))+`"}`), ParseOptions{Agent: Codex, Getenv: envOf()})
	if ev.ProjectDir != repo {
		t.Errorf("ProjectDir = %q, want %q", ev.ProjectDir, repo)
	}
}

func TestNameOf(t *testing.T) {
	for raw, want := range map[string]Name{
		"userPromptSubmitted": UserPromptSubmit, "agentStop": Stop, "subagentStop": SubagentStop, "sessionEnd": SessionEnd,
		"PreToolUse": PreToolUse, "errorOccurred": Other, "Notification": Other,
	} {
		if got := NameOf(raw); got != want {
			t.Errorf("%s: %s, want %s", raw, got, want)
		}
	}
}
