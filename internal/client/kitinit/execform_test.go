package kitinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/client/jsonorder"
)

// Claude Code を絶対パスで配線するとき（PATH に looptrack が無い端末）の exec form（command に実行ファイル、args に引数）。
// Git Bash の無い Windows で Claude Code はフックを PowerShell で実行し、引用した絶対パスの後ろに語が続く shell form は
// 構文エラーになる。exec form はシェルを通さずに起動する。OS は問わない（判定は binRef.abs だけで決まる）ので、ここで確かめる。

// localHooks は settings.local.json の hooks の全エントリ（イベント → エントリ）。
func localHooks(t *testing.T, ws string) map[string][]*jsonorder.Object {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(ws, ".claude", "settings.local.json"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := jsonorder.DecodeObject(b)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]*jsonorder.Object{}
	hooks := data.Object("hooks")
	if hooks == nil {
		return out
	}
	for _, ev := range hooks.Keys() {
		out[ev] = groupedEntries(hooks, ev)
	}
	return out
}

// TestExecFormWrite: PATH に無い端末の init は、手元の設定の配線をすべて exec form で書き、shell form を残さない。
// 自己検査（verify）が通る（exit 0）ことは、exec form を init の配線と読めている証拠になる（読めなければ配線の欠けで落ちる）。
func TestExecFormWrite(t *testing.T) {
	stubs(t, false)
	root := newRoot(t)
	ws := filepath.Join(root, "ws")
	r := runInit(t, root, "", "--project", "demo", "--loop")
	if r.code != 0 {
		t.Fatalf("%d %s %s", r.code, r.stdout, r.stderr)
	}
	n := 0
	names := map[string]bool{}
	for ev, entries := range localHooks(t, ws) {
		for _, h := range entries {
			n++
			args, ok := h.Get("args")
			list, isList := args.([]any)
			if !ok || !isList || len(list) < 4 || h.String("command") != "/opt/lt/looptrack" {
				t.Errorf("%s: exec form でない: %s", ev, jsonorder.Compact(h))
				continue
			}
			if list[0] != "hook" || list[2] != "--agent" || list[3] != "claude-code" {
				t.Errorf("%s: args の形が違う: %v", ev, list)
			}
			names[list[1].(string)] = true
		}
	}
	for _, want := range []string{"summary", "issue-session-bind", "pre-tool-secrets-guard", "pre-tool-git-guard"} {
		if !names[want] {
			t.Errorf("%s が exec form で配線されていない（%d 件）: %v", want, n, names)
		}
	}
	local := read(t, filepath.Join(ws, ".claude", "settings.local.json"))
	if strings.Contains(local, `\"/opt/lt/looptrack\"`) {
		t.Errorf("shell form（引用した絶対パス）が残っている:\n%s", local)
	}
	// やり直しても変わらない（重ねて足さない）
	before := local
	r = runInit(t, root, "", "--project", "demo", "--loop")
	if r.code != 0 {
		t.Fatalf("やり直し: %d %s %s", r.code, r.stdout, r.stderr)
	}
	if after := read(t, filepath.Join(ws, ".claude", "settings.local.json")); after != before {
		t.Errorf("やり直しで配線が変わった:\nbefore=%s\nafter=%s", before, after)
	}
}

// TestExecFormReplacesShellForm: 以前の init が shell form（引用した絶対パス。別の場所の絶対パスも）で置いた配線は、
// init のやり直しで exec form に置き換わる（同じ hook を重ねて足さない）。手で変えた配線（init の形と違う文字列）は残す。
func TestExecFormReplacesShellForm(t *testing.T) {
	stubs(t, false)
	root := newRoot(t)
	ws := filepath.Join(root, "ws")
	writeFile(t, filepath.Join(ws, ".claude", "settings.local.json"), `{"hooks": {
	  "SessionStart": [{"hooks": [
	    {"type": "command", "command": "\"/opt/lt/looptrack\" hook summary --agent claude-code", "timeout": 10},
	    {"type": "command", "command": "\"/opt/lt/looptrack\" hook session-start-rules --agent claude-code", "timeout": 5}]}],
	  "PreToolUse": [{"matcher": "mcp__.*", "hooks": [
	    {"type": "command", "command": "\"/old/place/looptrack\" hook issue-session-bind --agent claude-code", "timeout": 5}]}],
	  "Stop": [{"hooks": [
	    {"type": "command", "command": "./scripts/own-hook.sh", "timeout": 5}]}]}}`)
	r := runInit(t, root, "", "--project", "demo", "--loop")
	if r.code != 0 {
		t.Fatalf("%d %s %s", r.code, r.stdout, r.stderr)
	}
	local := read(t, filepath.Join(ws, ".claude", "settings.local.json"))
	if strings.Contains(local, `\"/opt/lt/looptrack\"`) || strings.Contains(local, "/old/place") {
		t.Errorf("shell form の配線が置き換わっていない:\n%s", local)
	}
	if !strings.Contains(local, "./scripts/own-hook.sh") {
		t.Errorf("プロジェクトが自分で置いた hook を消した:\n%s", local)
	}
	count := map[string]int{}
	for ev, entries := range localHooks(t, ws) {
		for _, h := range entries {
			c := classify(cmdOf(h))
			if c.kind == kindOther {
				continue
			}
			count[ev+" "+c.id]++
			if _, ok := h.Get("args"); !ok {
				t.Errorf("%s %s が exec form でない: %s", ev, c.id, jsonorder.Compact(h))
			}
		}
	}
	for _, k := range []string{"SessionStart summary", "SessionStart session-start-rules", "PreToolUse issue-session-bind"} {
		if count[k] != 1 {
			t.Errorf("%s が %d 件（1 件のはず）: %v", k, count[k], count)
		}
	}
}

// TestExecFormDropsForeignAgent: 手元の設定に別の AI（--agent codex）の exec form の配線があれば外し、claude-code の配線を足す。
func TestExecFormDropsForeignAgent(t *testing.T) {
	stubs(t, false)
	root := newRoot(t)
	ws := filepath.Join(root, "ws")
	writeFile(t, filepath.Join(ws, ".claude", "settings.local.json"), `{"hooks": {"SessionStart": [{"hooks": [
	  {"type": "command", "command": "/opt/lt/looptrack", "args": ["hook", "summary", "--agent", "codex"], "timeout": 10}]}]}}`)
	r := runInit(t, root, "", "--project", "demo")
	if r.code != 0 {
		t.Fatalf("%d %s %s", r.code, r.stdout, r.stderr)
	}
	var agents []string
	for _, h := range localHooks(t, ws)["SessionStart"] {
		agents = append(agents, agentOf(cmdOf(h)))
	}
	if len(agents) != 1 || agents[0] != "claude-code" {
		t.Errorf("別の AI の exec form の配線を外していない: %v", agents)
	}
}

// TestExecFormRead: 読む側の小道具（cmdOf・classify・agentOf・stripManaged）が exec form を init の配線と読む。
// 対照として、args の無い同じ command（実行ファイルだけ）は init の配線ではないと読む。
func TestExecFormRead(t *testing.T) {
	h := jsonorder.NewObject().Set("type", "command").Set("command", `C:\Users\u\AppData\Local\Programs\Looptrack Desktop\cli\looptrack.exe`).
		Set("args", []any{"hook", "pre-tool-secrets-guard", "--agent", "claude-code"})
	cmd := cmdOf(h)
	if want := `"C:\Users\u\AppData\Local\Programs\Looptrack Desktop\cli\looptrack.exe" hook pre-tool-secrets-guard --agent claude-code`; cmd != want {
		t.Fatalf("cmdOf = %q, want %q", cmd, want)
	}
	if c := classify(cmd); c.kind != kindNew || c.id != "pre-tool-secrets-guard" || !c.loop {
		t.Errorf("classify = %+v", c)
	}
	if a := agentOf(cmd); a != "claude-code" {
		t.Errorf("agentOf = %q", a)
	}
	bare := jsonorder.NewObject().Set("type", "command").Set("command", "/opt/lt/looptrack")
	if c := classify(cmdOf(bare)); c.kind != kindOther {
		t.Errorf("args の無い実行ファイルだけを init の配線と読んだ: %+v", c)
	}
	hooks := jsonorder.NewObject().Set("Stop", []any{jsonorder.NewObject().Set("hooks", []any{h, bare})})
	if !stripManaged(hooks) || len(groupedEntries(hooks, "Stop")) != 1 {
		t.Errorf("stripManaged が exec form を外していない: %s", jsonorder.Compact(hooks))
	}
	// setHookCmd: exec form の want は command と args を書き、shell form の want は args を外す
	e := jsonorder.NewObject()
	setHookCmd(e, want{Cmd: `"/x/looptrack" hook summary --agent claude-code`, Exec: "/x/looptrack", Args: []string{"hook", "summary", "--agent", "claude-code"}})
	if e.String("command") != "/x/looptrack" || cmdOf(e) != `"/x/looptrack" hook summary --agent claude-code` {
		t.Errorf("exec form: %s", jsonorder.Compact(e))
	}
	setHookCmd(e, want{Cmd: "looptrack hook summary --agent claude-code"})
	if e.Has("args") || cmdOf(e) != "looptrack hook summary --agent claude-code" {
		t.Errorf("shell form に戻すと args が残る: %s", jsonorder.Compact(e))
	}
}

// TestExecFormDoctor: doctor は exec form の配線を数え、command の実行ファイルの有無を見る。
// 対照として、無い実行ファイルを指す exec form は NG にする（args を読まなければ配線 0 件になり、どちらも NG にならない）。
func TestExecFormDoctor(t *testing.T) {
	stubs(t, false)
	bin := filepath.Join(t.TempDir(), "looptrack")
	writeFile(t, bin, "#!/bin/sh\n")
	jbin := strings.ReplaceAll(bin, `\`, `\\`)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".claude", "settings.local.json"),
		`{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "`+jbin+`", "args": ["hook", "summary", "--agent", "claude-code"]}]}]}}`)
	code, out := doctor(t, dir, map[string]string{"LOOPTRACK_API_URL": "https://example.invalid/im"})
	if code != 0 || !strings.Contains(out, "OK    .claude/settings.local.json: looptrack hook の配線 1 件") {
		t.Errorf("exec form の配線（あるファイル）:\n%s", out)
	}
	missing := t.TempDir()
	writeFile(t, filepath.Join(missing, ".claude", "settings.local.json"),
		`{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "/nowhere/looptrack", "args": ["hook", "summary", "--agent", "claude-code"]}]}]}}`)
	code, out = doctor(t, missing, map[string]string{"LOOPTRACK_API_URL": "https://example.invalid/im"})
	if code != 1 || !strings.Contains(out, "実行ファイル /nowhere/looptrack がありません") {
		t.Errorf("exec form の配線（無いファイル）:\n%s", out)
	}
}

// 利用者が別の実行ファイル（名前が looptrack そのものでないもの）に向けて手で置いた配線は、exec form への置き換えの対象にしない。
// 対照として、同じファイルの init が置いた shell form（/opt/lt/looptrack）は置き換わることも確かめる。
func TestExecFormKeepsHandWiredWrapper(t *testing.T) {
	stubs(t, false)
	root := newRoot(t)
	ws := filepath.Join(root, "ws")
	writeFile(t, filepath.Join(ws, ".claude", "settings.local.json"), `{"hooks": {
	  "SessionStart": [{"hooks": [
	    {"type": "command", "command": "\"/home/me/dev/looptrack-wrapper\" hook summary --agent claude-code", "timeout": 10},
	    {"type": "command", "command": "\"/opt/lt/looptrack\" hook session-start-rules --agent claude-code", "timeout": 5}]}]}}`)
	r := runInit(t, root, "", "--project", "demo", "--loop")
	if r.code != 0 {
		t.Fatalf("%d %s %s", r.code, r.stdout, r.stderr)
	}
	local := read(t, filepath.Join(ws, ".claude", "settings.local.json"))
	if !strings.Contains(local, `\"/home/me/dev/looptrack-wrapper\" hook summary`) {
		t.Errorf("手で別の実行ファイルに向けた配線を書き換えた:\n%s", local)
	}
	if strings.Contains(local, `\"/opt/lt/looptrack\" hook session-start-rules`) {
		t.Errorf("前提が崩れています: init が置いた shell form の配線が exec form に置き換わっていない:\n%s", local)
	}
}

func TestIsLooptrackBin(t *testing.T) {
	for tok, want := range map[string]bool{
		"looptrack": true, `"/opt/lt/looptrack"`: true, `"C:\Users\a b\cli\looptrack.exe"`: true, `'C:\x\LOOPTRACK.EXE'`: true,
		`"/home/me/looptrack-wrapper"`: false, `"C:\x\looptrack-dev.exe"`: false,
	} {
		if got := isLooptrackBin(tok); got != want {
			t.Errorf("isLooptrackBin(%s) = %v, want %v", tok, got, want)
		}
	}
}
