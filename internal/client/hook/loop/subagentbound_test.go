package loop

// pre-tool-subagent-bound の検査（実プロセスを立てず、PreToolUse の入力を表で与える）。

import (
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/hookio"
)

// wantUpdatedPrompt は updatedInput.prompt が元の指示文を残したまま印と上限の一文を足していること。
func wantUpdatedPrompt(orig string) func(*testing.T, *sandbox, got) {
	return func(t *testing.T, _ *sandbox, g got) {
		t.Helper()
		h, _ := g.json["hookSpecificOutput"].(map[string]any)
		u, _ := h["updatedInput"].(map[string]any)
		p, _ := u["prompt"].(string)
		if p == "" {
			t.Fatalf("updatedInput.prompt が無い: %q%s", g.out.Stdout, g.why())
		}
		if !strings.HasPrefix(p, orig) {
			t.Errorf("元の指示文が残っていない:\n%s", p)
		}
		for _, w := range []string{backgroundBoundMarker, "最大試行回数か締切", "存在を確かめる", "背景プロセス"} {
			if !strings.Contains(p, w) {
				t.Errorf("「%s」が無い:\n%s", w, p)
			}
		}
		if _, ok := g.json["decision"]; ok {
			t.Errorf("止めないはず: %q", g.out.Stdout)
		}
		if h["permissionDecision"] != nil {
			t.Errorf("permissionDecision を出さないはず: %q", g.out.Stdout)
		}
		if msg, _ := g.json["systemMessage"].(string); !strings.Contains(msg, backgroundBoundMarker) {
			t.Errorf("追記したことが親に見えない（systemMessage に印が無い）: %q", g.out.Stdout)
		}
		// updatedInput はほかの項目を落とさない
		if u["subagent_type"] != "Explore" {
			t.Errorf("ほかの項目が落ちた: %v", u)
		}
	}
}

// wantNoteWords は updatedInput.prompt の、印より後ろ（追記した注意文）に語が入っていること。
// 言語ごとの文面を確かめるために使う（既定は ja。call.env の LOOPTRACK_LANG で切り替える）。
func wantNoteWords(orig string, words ...string) func(*testing.T, *sandbox, got) {
	return func(t *testing.T, _ *sandbox, g got) {
		t.Helper()
		h, _ := g.json["hookSpecificOutput"].(map[string]any)
		u, _ := h["updatedInput"].(map[string]any)
		p, _ := u["prompt"].(string)
		if !strings.HasPrefix(p, orig) {
			t.Fatalf("元の指示文が残っていない: %q%s", p, g.why())
		}
		_, note, ok := strings.Cut(p, backgroundBoundMarker)
		if !ok {
			t.Fatalf("印が無い: %q%s", p, g.why())
		}
		for _, w := range words {
			if !strings.Contains(note, w) {
				t.Errorf("印より後ろに「%s」が無い:\n%s", w, note)
			}
		}
	}
}

func TestPreToolSubagentBound(t *testing.T) {
	mk := func(tool string, input map[string]any, agent hookio.Agent) func(*sandbox) call {
		return func(s *sandbox) call {
			return call{hook: "pre-tool-subagent-bound", agent: agent,
				input: jsonInput(map[string]any{"session_id": "s1", "cwd": s.root, "hook_event_name": "PreToolUse",
					"tool_name": tool, "tool_input": input})}
		}
	}
	withLang := func(lang string, f func(*sandbox) call) func(*sandbox) call {
		return func(s *sandbox) call {
			c := f(s)
			c.env = map[string]string{"LOOPTRACK_LANG": lang}
			return c
		}
	}
	const orig = "テストを走らせて結果を報告して"
	task := func(prompt string) map[string]any {
		return map[string]any{"description": "テスト", "prompt": prompt, "subagent_type": "Explore"}
	}

	scenario{name: "サブエージェントの指示に上限の規律を降ろす", steps: []step{
		// 追記すべき
		{name: "Agent の起動（印が無い）", mk: mk("Agent", task(orig), ""), want: wantUpdatedPrompt(orig)},
		{name: "Task も同じ（別名）", mk: mk("Task", task(orig), ""), want: wantUpdatedPrompt(orig)},

		// 印に続けて scratchpad の衝突しない命名の一文が入る（日英とも。scratchpad は親と全サブエージェントで共有される）
		{name: "衝突しない命名の一文（ja）", mk: withLang("ja", mk("Agent", task(orig), "")),
			want: wantNoteWords(orig, "scratchpad", "衝突しない名前", "計測が壊れた")},
		{name: "衝突しない命名の一文（en）", mk: withLang("en", mk("Agent", task(orig), "")),
			want: wantNoteWords(orig, "scratchpad", "cannot collide", "the measurement")},

		// 何もしないべき（誤って足さない・二重に足さない）
		{name: "既に印が入っている指示文には足さない",
			mk: mk("Agent", task(orig+"\n\n"+backgroundBoundMarker+" 背景で待つループを書かない。"), ""), want: wantQuiet},
		{name: "Bash（サブエージェントの起動ではない）",
			mk: mk("Bash", map[string]any{"command": "go test ./..."}, ""), want: wantQuiet},
		{name: "prompt が無い", mk: mk("Agent", map[string]any{"description": "x"}, ""), want: wantQuiet},
		{name: "prompt が空白だけ", mk: mk("Agent", task("   "), ""), want: wantQuiet},
		{name: "prompt が文字列でない", mk: mk("Agent", map[string]any{"prompt": 3}, ""), want: wantQuiet},
		{name: "書き換えを実物で確かめていない AI（Codex）では何もしない",
			mk: mk("Agent", task(orig), hookio.Codex), want: wantQuiet},
	}}.run(t)
}
