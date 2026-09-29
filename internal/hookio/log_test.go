package hookio

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// logRunOpts は記録つきの RunOptions（置き場は dir/hook-log.jsonl）。
func logRunOpts(t *testing.T, enabled bool, path string) RunOptions {
	t.Helper()
	kv := []string{"CLAUDECODE", "1"}
	if enabled {
		kv = append(kv, HookLogEnv, "1")
	}
	opts, _, err := ParseArgs([]string{"--agent", "claude-code"}, envOf(kv...))
	if err != nil {
		t.Fatal(err)
	}
	opts.Parse.GitRoot = func(string) string { return "" }
	opts.Parse.Getwd = func() string { return "/w" }
	opts.HookName = "pre-tool-git-guard"
	opts.LogPath = func(Event) string { return path }
	opts.Now = func() time.Time { return time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC) }
	return opts
}

func readLogLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("記録が読めない: %v", err)
	}
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		m := map[string]any{}
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("JSONL の 1 行として読めない: %q: %v", l, err)
		}
		out = append(out, m)
	}
	return out
}

const logPreToolInput = `{"hook_event_name":"PreToolUse","session_id":"sess-1","agent_id":"sub-9","tool_name":"Bash","tool_input":{"command":"git reset --hard"}}`

func denyWithKind(Event) (Result, error) {
	return Result{Deny: "止める理由の文面", Kind: "git-guard: reset_hard"}, nil
}

// TestHookLogOnlyWhenEnabled は、LOOPTRACK_LOOP_HOOK_LOG=1 のときだけ 1 行を追記し、無いときはファイル（置き場の
// ディレクトリも）作らないこと。両方を同じテストで確かめる（記録する側が死んでいたら「作らない」の確認も意味が無い）。
func TestHookLogOnlyWhenEnabled(t *testing.T) {
	// 設定しないとき: 出力は出るが、記録のファイルも置き場も作らない
	offDir := filepath.Join(t.TempDir(), "state", ".looptrack-freshness")
	offPath := filepath.Join(offDir, "hook-log.jsonl")
	var offOut bytes.Buffer
	offCode := Run(strings.NewReader(logPreToolInput), &offOut, nil, logRunOpts(t, false, offPath), denyWithKind)
	if _, err := os.Stat(offDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("設定しないのに置き場ができた: %v", err)
	}

	// 設定したとき（対照）: 1 行だけ書き、出力と終了コードは設定しないときと同じ
	onPath := filepath.Join(t.TempDir(), "state", ".looptrack-freshness", "hook-log.jsonl")
	var onOut bytes.Buffer
	onCode := Run(strings.NewReader(logPreToolInput), &onOut, nil, logRunOpts(t, true, onPath), denyWithKind)
	if onCode != offCode || onOut.String() != offOut.String() {
		t.Errorf("記録の有無で出力が変わった: %d %q / %d %q", offCode, offOut.String(), onCode, onOut.String())
	}
	if !strings.Contains(onOut.String(), `"permissionDecision":"deny"`) {
		t.Fatalf("前提が崩れています（本体の deny が出ていない）: %q", onOut.String())
	}
	lines := readLogLines(t, onPath)
	if len(lines) != 1 {
		t.Fatalf("行数 %d, want 1", len(lines))
	}
	want := map[string]any{
		"ts": "2026-09-29T12:00:00Z", "hook": "pre-tool-git-guard", "event": "PreToolUse", "decision": "deny",
		"session": "sess-1", "subagent": "sub-9", "kind": "git-guard: reset_hard",
	}
	for k, v := range want {
		if lines[0][k] != v {
			t.Errorf("%s = %v, want %v", k, lines[0][k], v)
		}
	}
	if len(lines[0]) != len(want) {
		t.Errorf("項目が想定の外にある: %v", lines[0])
	}
	b, _ := os.ReadFile(onPath)
	for _, leak := range []string{"git reset", "止める理由", "tool_input", "command"} {
		if strings.Contains(string(b), leak) {
			t.Errorf("記録に %q が入った: %s", leak, b)
		}
	}
}

// TestHookLogDecisions は、判定の語が実際に出す形（Normalize の後）と、エラー・時間切れ・panic から決まること。
func TestHookLogDecisions(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		noBlock bool
		timeout time.Duration
		h       Handler
		want    string
		kind    string
	}{
		{"deny", logPreToolInput, false, 0, denyWithKind, DecisionDeny, "git-guard: reset_hard"},
		{"NoBlock なら system（理由の種類は残す）", logPreToolInput, true, 0, denyWithKind, DecisionSystem, "git-guard: reset_hard"},
		{"ask", logPreToolInput, false, 0, func(Event) (Result, error) { return Result{Ask: "a", Kind: "k"}, nil }, DecisionAsk, "k"},
		{"通過（Kind だけでは pass・kind は書かない）", logPreToolInput, false, 0, func(Event) (Result, error) { return Result{Kind: "k"}, nil }, DecisionPass, ""},
		{"Stop の block", `{"hook_event_name":"Stop","session_id":"s"}`, false, 0, func(Event) (Result, error) { return Result{Block: "b", Kind: "markup"}, nil }, DecisionBlock, "markup"},
		{"文脈", `{"hook_event_name":"SessionStart","session_id":"s"}`, false, 0, func(Event) (Result, error) { return Result{Context: "c"}, nil }, DecisionContext, ""},
		{"本体のエラー", logPreToolInput, false, 0, func(Event) (Result, error) { return Result{Deny: "x", Kind: "k"}, errors.New("失敗") }, DecisionError, ""},
		{"panic", logPreToolInput, false, 0, func(Event) (Result, error) { panic("壊れた") }, DecisionPanic, ""},
		{"時間切れ", logPreToolInput, false, 20 * time.Millisecond, func(Event) (Result, error) {
			time.Sleep(300 * time.Millisecond)
			return Result{Deny: "遅い"}, nil
		}, DecisionTimeout, ""},
		{"読めない入力", "{not json", false, 0, denyWithKind, DecisionError, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "hook-log.jsonl")
			opts := logRunOpts(t, true, path)
			opts.Render.NoBlock = c.noBlock
			opts.Timeout = c.timeout
			if code := Run(strings.NewReader(c.input), &bytes.Buffer{}, &bytes.Buffer{}, opts, c.h); code != 0 {
				t.Errorf("exit %d", code)
			}
			lines := readLogLines(t, path)
			if len(lines) != 1 {
				t.Fatalf("行数 %d", len(lines))
			}
			if lines[0]["decision"] != c.want {
				t.Errorf("decision = %v, want %s", lines[0]["decision"], c.want)
			}
			if got, _ := lines[0]["kind"].(string); got != c.kind {
				t.Errorf("kind = %q, want %q", got, c.kind)
			}
		})
	}
}

// TestHookLogFailOpen は、置き場に書けない（置き場のパスの途中がふつうのファイル）ときも、出力と終了コードが
// 記録なしのときと同じであること。
func TestHookLogFailOpen(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "state")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(blocker, ".looptrack-freshness", "hook-log.jsonl")
	for _, c := range []struct {
		name  string
		input string
		h     Handler
	}{
		{"deny", logPreToolInput, denyWithKind},
		{"通過", logPreToolInput, func(Event) (Result, error) { return Result{}, nil }},
		{"panic", logPreToolInput, func(Event) (Result, error) { panic("壊れた") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			var wantOut, wantErr, gotOut, gotErr bytes.Buffer
			wantCode := Run(strings.NewReader(c.input), &wantOut, &wantErr, logRunOpts(t, false, bad), c.h)
			gotCode := Run(strings.NewReader(c.input), &gotOut, &gotErr, logRunOpts(t, true, bad), c.h)
			if gotCode != wantCode || gotOut.String() != wantOut.String() || gotErr.String() != wantErr.String() {
				t.Errorf("書けない置き場で出力が変わった: %d %q %q / want %d %q %q",
					gotCode, gotOut.String(), gotErr.String(), wantCode, wantOut.String(), wantErr.String())
			}
			if _, err := os.Stat(bad); err == nil {
				t.Error("書けないはずの置き場に書けた（前提が崩れています）")
			}
		})
	}
	// 対照: 同じ入力で、書ける置き場なら記録される（書き込みの経路が生きている）
	good := filepath.Join(dir, "ok", "hook-log.jsonl")
	Run(strings.NewReader(logPreToolInput), &bytes.Buffer{}, nil, logRunOpts(t, true, good), denyWithKind)
	if len(readLogLines(t, good)) != 1 {
		t.Error("書ける置き場で記録されない（前提が崩れています）")
	}
}

// TestHookLogRotate は、追記すると HookLogMax を超えるときに 1 世代だけ .1 へ回すこと（前の .1 は消える）。
func TestHookLogRotate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hook-log.jsonl")
	old := bytes.Repeat([]byte("o"), HookLogMax-10)
	if err := os.WriteFile(path, old, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".1", []byte("older"), 0o644); err != nil {
		t.Fatal(err)
	}
	Run(strings.NewReader(logPreToolInput), &bytes.Buffer{}, nil, logRunOpts(t, true, path), denyWithKind)
	b1, err := os.ReadFile(path + ".1")
	if err != nil || !bytes.Equal(b1, old) {
		t.Errorf(".1 が回す前のファイルでない（%d バイト・%v）", len(b1), err)
	}
	if lines := readLogLines(t, path); len(lines) != 1 || lines[0]["decision"] != DecisionDeny {
		t.Errorf("回した後の記録: %v", lines)
	}
	// 上限に届かないうちは回さない（対照）
	Run(strings.NewReader(logPreToolInput), &bytes.Buffer{}, nil, logRunOpts(t, true, path), denyWithKind)
	if lines := readLogLines(t, path); len(lines) != 2 {
		t.Errorf("上限の前で回した: %d 行", len(lines))
	}
	if b, _ := os.ReadFile(path + ".1"); !bytes.Equal(b, old) {
		t.Error("上限の前に .1 が変わった")
	}
}

// TestResultKindNotRendered は、Kind が出力の JSON に出ず、Normalize・IsZero に影響しないこと。
func TestResultKindNotRendered(t *testing.T) {
	ev := Event{Agent: ClaudeCode, Name: PreToolUse}
	with := Render(ev, Result{Deny: "d", Kind: "secrets: show .env"}, RenderOptions{})
	without := Render(ev, Result{Deny: "d"}, RenderOptions{})
	if with != without {
		t.Errorf("Kind で出力が変わった: %q / %q", with.Stdout, without.Stdout)
	}
	if strings.Contains(with.Stdout, "secrets") || strings.Contains(with.Stdout, "kind") {
		t.Errorf("出力に Kind が出た: %s", with.Stdout)
	}
	if n := Normalize(ClaudeCode, PreToolUse, Result{Ask: "a", Kind: "k"}, RenderOptions{}); n.Kind != "" || n != (Result{Ask: "a"}) {
		t.Errorf("Normalize が Kind を持ち越した: %+v", n)
	}
	if !(Result{Kind: "k"}).IsZero() {
		t.Error("Kind だけの Result は何もしない Result")
	}
	if (Result{Ask: "a", Kind: "k"}).IsZero() {
		t.Error("前提が崩れています（Ask のある Result が空と見なされた）")
	}
	if out := Render(ev, Result{Kind: "k"}, RenderOptions{}); out != (Output{}) {
		t.Errorf("Kind だけの Result で何か出た: %+v", out)
	}
	b, _ := json.Marshal(Result{Deny: "d", Kind: "k"})
	if strings.Contains(string(b), `"k"`) {
		t.Errorf("Result の JSON に Kind が出た: %s", b)
	}
}
