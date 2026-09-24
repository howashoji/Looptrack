package kitinit

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/i18n"
)

// Copilot の配線の SessionStart の合成: loop ありなら SessionStart は 1 本（looptrack hook session-start --parts …）。

// copilotSessionStart は .github/hooks/looptrack.json の SessionStart のコマンドの並び。
func copilotSessionStart(t *testing.T, root string) []string {
	t.Helper()
	return copilotEvent(t, root, "SessionStart")
}

// copilotEvent は .github/hooks/looptrack.json の ev のコマンドの並び。
func copilotEvent(t *testing.T, root, ev string) []string {
	t.Helper()
	o, err := jsonorder.DecodeObject([]byte(read(t, filepath.Join(root, "ws", copilotHooks))))
	if err != nil {
		t.Fatal(err)
	}
	hooks := o.Object("hooks")
	if hooks == nil {
		return nil
	}
	var out []string
	for _, h := range flatEntries(hooks, ev) {
		out = append(out, cmdOf(h))
	}
	return out
}

const combinedCmd = "LOOPTRACK_API_URL=" + fakeURL + " LOOPTRACK_PROJECT=demo looptrack hook session-start --agent copilot " +
	"--parts summary,session-start-rules,session-start-memories,session-start-iteration,session-start-worktrees"

func TestCopilotSessionStartCombined(t *testing.T) {
	stubs(t, true)
	summaryCmd := "LOOPTRACK_API_URL=" + fakeURL + " LOOPTRACK_PROJECT=demo looptrack hook summary --agent copilot"

	t.Run("loop あり: 1 本にまとめ、2 回目は変えない", func(t *testing.T) {
		root := newRoot(t)
		if r := runInit(t, root, "", "--project", "demo", "--agent", "copilot", "--loop"); r.code != 0 {
			t.Fatalf("init: %s", r.stderr)
		}
		if got := copilotSessionStart(t, root); strings.Join(got, "\n") != combinedCmd {
			t.Errorf("SessionStart: %q", got)
		}
		before := read(t, filepath.Join(root, "ws", copilotHooks))
		if r := runInit(t, root, "", "--project", "demo", "--agent", "copilot", "--loop"); r.code != 0 {
			t.Fatalf("2 回目: %s", r.stderr)
		}
		if after := read(t, filepath.Join(root, "ws", copilotHooks)); after != before {
			t.Errorf("2 回目で変わった:\n%s", firstDiff(before, after))
		}
	})

	t.Run("loop なし: summary だけ（まとめない）", func(t *testing.T) {
		root := newRoot(t)
		if r := runInit(t, root, "", "--project", "demo", "--agent", "copilot", "--no-loop"); r.code != 0 {
			t.Fatalf("init: %s", r.stderr)
		}
		if got := copilotSessionStart(t, root); strings.Join(got, "\n") != summaryCmd {
			t.Errorf("SessionStart: %q", got)
		}
	})

	t.Run("旧名の置き場（im.json）から新しい置き場へ移す（手で置いた hook は残す）", func(t *testing.T) {
		root := newRoot(t)
		legacy := filepath.Join(root, "ws", legacyCopilotHooks)
		write(t, legacy, `{"version": 1, "hooks": {"SessionStart": [
  {"type": "command", "command": "`+summaryCmd+`", "timeoutSec": 10},
  {"type": "command", "command": "echo mine", "timeoutSec": 3}]}}`+"\n")
		if r := runInit(t, root, "", "--project", "demo", "--agent", "copilot", "--no-loop"); r.code != 0 {
			t.Fatalf("init: %s", r.stderr)
		}
		if isFile(legacy) {
			t.Errorf("旧名の置き場 %s が残っている（Copilot は両方を読み、同じ hook が 2 回動く）", legacyCopilotHooks)
		}
		if got := copilotSessionStart(t, root); strings.Join(got, "\n") != summaryCmd+"\necho mine" {
			t.Errorf("新しい置き場の SessionStart（手で置いた hook を引き継ぐ）: %q", got)
		}
		// 対照: 新しい置き場があるときは、旧名の置き場を読まない（中身を上書きしない）。
		write(t, legacy, `{"version": 1, "hooks": {"SessionStart": [{"type": "command", "command": "echo stale", "timeoutSec": 3}]}}`+"\n")
		if r := runInit(t, root, "", "--project", "demo", "--agent", "copilot", "--no-loop"); r.code != 0 {
			t.Fatalf("2 回目: %s", r.stderr)
		}
		if got := copilotSessionStart(t, root); strings.Join(got, "\n") != summaryCmd+"\necho mine" {
			t.Errorf("新しい置き場があるのに旧名の置き場を読んだ: %q", got)
		}
	})

	t.Run("以前の init の個別の配線から移る（手で置いた hook は残す）", func(t *testing.T) {
		root := newRoot(t)
		old := `{"version": 1, "hooks": {"SessionStart": [
  {"type": "command", "command": "` + summaryCmd + `", "timeoutSec": 10},
  {"type": "command", "command": "echo mine", "timeoutSec": 3},
  {"type": "command", "command": "looptrack hook session-start-rules --agent copilot", "timeoutSec": 5},
  {"type": "command", "command": "looptrack hook session-start-memories --agent copilot", "timeoutSec": 5},
  {"type": "command", "command": "looptrack hook session-start-iteration --agent copilot", "timeoutSec": 10}]}}` + "\n"
		write(t, filepath.Join(root, "ws", copilotHooks), old)
		if r := runInit(t, root, "", "--project", "demo", "--agent", "copilot", "--loop"); r.code != 0 {
			t.Fatalf("init: %s", r.stderr)
		}
		if got := copilotSessionStart(t, root); strings.Join(got, "\n") != "echo mine\n"+combinedCmd {
			t.Errorf("SessionStart: %q", got)
		}
	})

	t.Run("--remove-loop は summary の個別の配線に戻す", func(t *testing.T) {
		root := newRoot(t)
		if r := runInit(t, root, "", "--project", "demo", "--agent", "copilot", "--loop"); r.code != 0 {
			t.Fatalf("init: %s", r.stderr)
		}
		if r := runInit(t, root, "", "--remove-loop"); r.code != 0 {
			t.Fatalf("remove: %s", r.stderr)
		}
		if got := copilotSessionStart(t, root); strings.Join(got, "\n") != summaryCmd {
			t.Errorf("SessionStart: %q", got)
		}
		body := read(t, filepath.Join(root, "ws", copilotHooks))
		if !strings.Contains(body, `"powershell": "$env:LOOPTRACK_API_URL='`+fakeURL+`'; $env:LOOPTRACK_PROJECT='demo'; looptrack hook summary --agent copilot"`) ||
			strings.Contains(body, "session-start") {
			t.Errorf("PowerShell の配線も summary に戻すはず:\n%s", body)
		}
		// もう一度 loop を入れると、また 1 本にまとまる
		if r := runInit(t, root, "", "--project", "demo", "--agent", "copilot", "--loop"); r.code != 0 {
			t.Fatalf("再導入: %s", r.stderr)
		}
		if got := copilotSessionStart(t, root); strings.Join(got, "\n") != combinedCmd {
			t.Errorf("再導入の SessionStart: %q", got)
		}
	})

	t.Run("Claude Code・Codex の配線はまとめない", func(t *testing.T) {
		root := newRoot(t)
		if r := runInit(t, root, "", "--project", "demo", "--agent", "claude-code,codex,copilot", "--loop"); r.code != 0 {
			t.Fatalf("init: %s", r.stderr)
		}
		for _, rel := range []string{".claude/settings.json", ".codex/hooks.json"} {
			if body := read(t, filepath.Join(root, "ws", rel)); strings.Contains(body, "hook session-start ") ||
				!strings.Contains(body, "hook session-start-rules") {
				t.Errorf("%s は個別の配線のまま:\n%s", rel, body)
			}
		}
		if got := copilotSessionStart(t, root); strings.Join(got, "\n") != combinedCmd {
			t.Errorf("Copilot の SessionStart: %q", got)
		}
	})
}

// Copilot の配線の UserPromptSubmit の合成: loop ありなら UserPromptSubmit は 1 本（looptrack hook user-prompt --parts …）。
// 部分はすべて loop の hook なので env は前置しない（個別の配線と同じ）。
const combinedPromptCmd = "looptrack hook user-prompt --agent copilot --parts user-prompt-rules,user-prompt-task-mode,session-scope-guard,user-prompt-stale-base"

func TestCopilotUserPromptCombined(t *testing.T) {
	stubs(t, true)
	ups := func(t *testing.T, root string) string {
		t.Helper()
		return strings.Join(copilotEvent(t, root, "UserPromptSubmit"), "\n")
	}

	t.Run("loop あり: 1 本にまとめ、2 回目は変えない", func(t *testing.T) {
		root := newRoot(t)
		if r := runInit(t, root, "", "--project", "demo", "--agent", "copilot", "--loop"); r.code != 0 {
			t.Fatalf("init: %s", r.stderr)
		}
		if got := ups(t, root); got != combinedPromptCmd {
			t.Errorf("UserPromptSubmit: %q", got)
		}
		body := read(t, filepath.Join(root, "ws", copilotHooks))
		if !strings.Contains(body, `"powershell": "`+combinedPromptCmd+`"`) || !strings.Contains(body, `"timeoutSec": 15`) {
			t.Errorf("PowerShell の配線と timeoutSec 15:\n%s", body)
		}
		if r := runInit(t, root, "", "--project", "demo", "--agent", "copilot", "--loop"); r.code != 0 {
			t.Fatalf("2 回目: %s", r.stderr)
		}
		if after := read(t, filepath.Join(root, "ws", copilotHooks)); after != body {
			t.Errorf("2 回目で変わった:\n%s", firstDiff(body, after))
		}
	})

	t.Run("loop なし: UserPromptSubmit は配線しない", func(t *testing.T) {
		root := newRoot(t)
		if r := runInit(t, root, "", "--project", "demo", "--agent", "copilot", "--no-loop"); r.code != 0 {
			t.Fatalf("init: %s", r.stderr)
		}
		if got := ups(t, root); got != "" {
			t.Errorf("UserPromptSubmit: %q", got)
		}
	})

	t.Run("以前の init の個別の配線から移る（手で置いた hook は残す）", func(t *testing.T) {
		root := newRoot(t)
		old := `{"version": 1, "hooks": {"UserPromptSubmit": [
  {"type": "command", "command": "looptrack hook user-prompt-rules --agent copilot", "timeoutSec": 5},
  {"type": "command", "command": "echo mine", "timeoutSec": 3},
  {"type": "command", "command": "looptrack hook user-prompt-task-mode --agent copilot", "timeoutSec": 5},
  {"type": "command", "command": "looptrack hook session-scope-guard --agent copilot", "timeoutSec": 10}]}}` + "\n"
		write(t, filepath.Join(root, "ws", copilotHooks), old)
		if r := runInit(t, root, "", "--project", "demo", "--agent", "copilot", "--loop"); r.code != 0 {
			t.Fatalf("init: %s\n%s", r.stderr, r.stdout)
		}
		if got := ups(t, root); got != "echo mine\n"+combinedPromptCmd {
			t.Errorf("UserPromptSubmit: %q", got)
		}
	})

	t.Run("--remove-loop は合成の配線ごと外す（手で置いた hook は残す）・再導入でまた 1 本", func(t *testing.T) {
		root := newRoot(t)
		write(t, filepath.Join(root, "ws", copilotHooks), `{"version": 1, "hooks": {"UserPromptSubmit": [{"type": "command", "command": "echo mine", "timeoutSec": 3}]}}`+"\n")
		if r := runInit(t, root, "", "--project", "demo", "--agent", "copilot", "--loop"); r.code != 0 {
			t.Fatalf("init: %s", r.stderr)
		}
		if got := ups(t, root); got != "echo mine\n"+combinedPromptCmd {
			t.Errorf("UserPromptSubmit: %q", got)
		}
		if r := runInit(t, root, "", "--remove-loop"); r.code != 0 {
			t.Fatalf("remove: %s", r.stderr)
		}
		if got := ups(t, root); got != "echo mine" {
			t.Errorf("--remove-loop の後の UserPromptSubmit: %q", got)
		}
		if body := read(t, filepath.Join(root, "ws", copilotHooks)); strings.Contains(body, "user-prompt") || strings.Contains(body, "session-scope-guard") {
			t.Errorf("loop の UserPromptSubmit が残っている:\n%s", body)
		}
		if r := runInit(t, root, "", "--project", "demo", "--agent", "copilot", "--loop"); r.code != 0 {
			t.Fatalf("再導入: %s", r.stderr)
		}
		if got := ups(t, root); got != "echo mine\n"+combinedPromptCmd {
			t.Errorf("再導入の UserPromptSubmit: %q", got)
		}
	})

	t.Run("Claude Code・Codex の配線は変わらない", func(t *testing.T) {
		root := newRoot(t)
		if r := runInit(t, root, "", "--project", "demo", "--agent", "claude-code,codex,copilot", "--loop"); r.code != 0 {
			t.Fatalf("init: %s", r.stderr)
		}
		for _, rel := range []string{".claude/settings.json", ".codex/hooks.json"} {
			body := read(t, filepath.Join(root, "ws", rel))
			if strings.Contains(body, "hook user-prompt ") || strings.Contains(body, "--parts") {
				t.Errorf("%s に合成の配線がある:\n%s", rel, body)
			}
			for _, n := range []string{"user-prompt-rules", "user-prompt-task-mode", "session-scope-guard"} {
				if !strings.Contains(body, "hook "+n+" --agent ") {
					t.Errorf("%s に %s の個別の配線が無い", rel, n)
				}
			}
		}
		if got := ups(t, root); got != combinedPromptCmd {
			t.Errorf("Copilot の UserPromptSubmit: %q", got)
		}
	})
}

// TestRemoveLoopNext は --remove-loop の後の案内が導入した AI に合うこと。
// 鮮度ガードと skill /issue は Claude Code の導入にだけ置くので、Codex・Copilot だけの導入ではそれを「そのまま」と言わない。
func TestRemoveLoopNext(t *testing.T) {
	kit := func(agents ...string) *jsonorder.Object {
		o := jsonorder.NewObject()
		if agents != nil {
			l := make([]any, len(agents))
			for i, a := range agents {
				l[i] = a
			}
			o.Set("agent", l)
		}
		return o
	}
	claude := "次に: Claude Code を再起動する（Codex・Copilot は新しいセッションから反映）。core（鮮度ガード・skill /issue）はそのまま"
	for _, c := range []struct {
		name string
		prev *jsonorder.Object
		want string
	}{
		{"Claude Code と Copilot", kit("claude-code", "copilot"), claude},
		{"記録なし（以前の導入）", kit(), claude},
		{"Copilot だけ", kit("copilot"), "次に: Copilot は新しいセッションから反映する。core（SessionStart の要約・トークン計測の hook・AGENTS.md の課題管理の節）はそのまま"},
		{"Codex と Copilot", kit("codex", "copilot"), "次に: Codex・Copilot は新しいセッションから反映する。"},
		{"other だけ", kit("other"), "次に: 新しいセッションから反映する"},
	} {
		got := removeLoopNext(i18n.JA, c.prev)
		if !strings.HasPrefix(got, c.want) {
			t.Errorf("%s: %s", c.name, got)
		}
		if c.want != claude && strings.Contains(got, "Claude Code を再起動") {
			t.Errorf("%s: Claude Code の無い導入で再起動を案内している: %s", c.name, got)
		}
	}
}
