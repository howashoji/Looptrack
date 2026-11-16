package kitinit

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/client/jsonorder"
)

// Codex のアプリの「Claude Code の hooks を移す」は、.claude/settings.json の配線を --agent claude-code のまま
// .codex/hooks.json に写す。以前の init は core の配線を「同じ名前があれば足さない」ので、写された配線が残って
// verify が「--agent が codex ではありません」で落ち、導入を戻していた（Claude Code だけの導入先に codex を足したとき）。

// migratedHooks は Codex のアプリが写した形（--agent claude-code の core と loop・利用者が自分で置いた hook）。
const migratedHooks = `{
  "hooks": {
    "SessionStart": [
      {"hooks": [
        {"type": "command", "command": "looptrack hook summary --agent claude-code", "timeout": 10},
        {"type": "command", "command": "looptrack hook session-start-rules --agent claude-code", "timeout": 10},
        {"type": "command", "command": "echo mine", "timeout": 5}
      ]}
    ],
    "PostToolUse": [
      {"matcher": "mcp__.*", "hooks": [
        {"type": "command", "command": "looptrack hook usage --agent claude-code", "timeout": 10}
      ]}
    ]
  }
}
`

// TestInitReplacesForeignAgentWiring は、.codex/hooks.json に --agent claude-code の配線があっても init が
// それを外して codex の配線を入れ直し、verify を通ることを確かめる。利用者が自分で置いた hook は残す。
// 対照: 同じ配線が .claude/settings.json にあるとき（--agent が合っている）は外さず、注記も出さない。
func TestInitReplacesForeignAgentWiring(t *testing.T) {
	stubs(t, true)

	root := newRoot(t)
	codex := filepath.Join(root, "ws", ".codex", "hooks.json")
	write(t, codex, migratedHooks)
	r := runInit(t, root, "", "--project", "demo", "--agent", "codex", "--loop")
	if r.code != 0 {
		t.Fatalf("init が失敗しました（code %d）\nstdout:\n%s\nstderr:\n%s", r.code, r.stdout, r.stderr)
	}
	got := read(t, codex)
	if strings.Contains(got, "--agent claude-code") {
		t.Errorf("--agent claude-code の配線が .codex/hooks.json に残っています:\n%s", got)
	}
	for _, want := range []string{"hook summary --agent codex", "hook usage --agent codex", "hook session-start-rules --agent codex", `"echo mine"`} {
		if !strings.Contains(got, want) {
			t.Errorf(".codex/hooks.json に %q がありません:\n%s", want, got)
		}
	}
	if out := r.stdout + r.stderr; !strings.Contains(out, "--agent が codex ではない looptrack の配線 3 件を外し") {
		t.Errorf("外した旨の注記がありません:\n%s", out)
	}

	// 対照: --agent が合っている配線は外さない（Claude Code の導入先で同じ形の配線を持つ）
	root2 := newRoot(t)
	settings := filepath.Join(root2, "ws", ".claude", "settings.json")
	write(t, settings, migratedHooks)
	r2 := runInit(t, root2, "", "--project", "demo", "--agent", "claude-code", "--loop")
	if r2.code != 0 {
		t.Fatalf("対照の init が失敗しました（前提が崩れています。code %d）\nstdout:\n%s\nstderr:\n%s", r2.code, r2.stdout, r2.stderr)
	}
	if out := r2.stdout + r2.stderr; strings.Contains(out, "ではない looptrack の配線") {
		t.Errorf("--agent が合っている配線を外したと注記しています:\n%s", out)
	}
	if got2 := read(t, settings); !strings.Contains(got2, "hook summary --agent claude-code") || !strings.Contains(got2, `"echo mine"`) {
		t.Errorf("対照: .claude/settings.json の配線が変わっています:\n%s", got2)
	}
}

// TestDropForeignAgent は、Claude Code・Codex の形（matcher ごとのグループ）と Copilot の形（イベントごとの配列）の両方で、
// --agent の違う looptrack hook だけを外し、その数を返すことを確かめる。空になったグループとイベントは消す。
// 対照: --agent が合う配線・looptrack でない hook・--agent の無い looptrack のコマンドは残る。
func TestDropForeignAgent(t *testing.T) {
	grouped := decodeObj(t, migratedHooks).Object("hooks")
	if n := dropForeignAgent(grouped, "codex", false); n != 3 {
		t.Errorf("外した数 = %d、want 3", n)
	}
	if grouped.Has("PostToolUse") {
		t.Errorf("空になった PostToolUse が残っています: %s", jsonorder.Compact(grouped))
	}
	if s := jsonorder.Compact(grouped); !strings.Contains(s, "echo mine") || strings.Contains(s, "claude-code") {
		t.Errorf("グループの形の結果が違います: %s", s)
	}

	same := decodeObj(t, migratedHooks).Object("hooks")
	if n := dropForeignAgent(same, "claude-code", false); n != 0 {
		t.Errorf("対照: --agent が合う配線を %d 件外しました", n)
	}

	flat := decodeObj(t, `{"hooks": {
  "sessionStart": [
    {"type": "command", "command": "looptrack hook summary --agent claude-code"},
    {"type": "command", "command": "looptrack hook summary --agent copilot"},
    {"type": "command", "command": "looptrack version"}
  ],
  "postToolUse": [
    {"type": "command", "command": "looptrack hook usage --agent codex"}
  ]
}}`).Object("hooks")
	if n := dropForeignAgent(flat, "copilot", true); n != 2 {
		t.Errorf("Copilot の形で外した数 = %d、want 2", n)
	}
	if flat.Has("postToolUse") {
		t.Errorf("空になった postToolUse が残っています: %s", jsonorder.Compact(flat))
	}
	if s := jsonorder.Compact(flat); !strings.Contains(s, "--agent copilot") || !strings.Contains(s, "looptrack version") {
		t.Errorf("Copilot の形の結果が違います: %s", s)
	}
}

func decodeObj(t *testing.T, s string) *jsonorder.Object {
	t.Helper()
	o, err := jsonorder.DecodeObject([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return o
}
