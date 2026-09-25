package loop

// pre-tool-subagent-model の検査（実プロセスを立てず、PreToolUse の入力を表で与える）。
// 「止めない」側も「止める」側と同数以上そろえる（誤検知で deny が常時バイパスされるのを避ける）。
//
// HOME は毎回 sandbox の中の空ディレクトリを明示して渡す。開発者の実在のホームにある
// ~/.claude/agents/*.md に判定が左右されないようにするため。

import (
	"strings"
	"testing"
)

func TestPreToolSubagentModel(t *testing.T) {
	mk := func(tool string, input map[string]any, env map[string]string) func(*sandbox) call {
		return func(s *sandbox) call {
			e := map[string]string{"HOME": s.p("home")}
			for k, v := range env {
				e[k] = v
			}
			return call{hook: "pre-tool-subagent-model", env: e,
				input: jsonInput(map[string]any{"session_id": "s1", "cwd": s.root, "hook_event_name": "PreToolUse",
					"tool_name": tool, "tool_input": input})}
		}
	}
	task := func(extra map[string]any) map[string]any {
		m := map[string]any{"description": "テスト", "prompt": "テストを走らせて結果を報告して"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	deny, quiet := wantDeny, wantQuiet

	scenario{name: "model 未指定のサブエージェントの起動を deny で止める",
		setup: func(s *sandbox) { s.mkdir("home") },
		steps: []step{
			// ── 止めるべき ──────────────────────────────────────────
			{name: "model が無い（Agent）", mk: mk("Agent", task(nil), nil), want: deny},
			{name: "model が無い（Task も同じ別名）", mk: mk("Task", task(nil), nil), want: deny},
			{name: "model が空文字", mk: mk("Agent", task(map[string]any{"model": ""}), nil), want: deny},
			{name: "model が空白だけ", mk: mk("Agent", task(map[string]any{"model": "   "}), nil), want: deny},
			{name: "subagent_type があっても model が無ければ止める",
				mk: mk("Agent", task(map[string]any{"subagent_type": "general-purpose"}), nil), want: deny},

			// ── 通すべき ────────────────────────────────────────────
			{name: "model が指定されている", mk: mk("Agent", task(map[string]any{"model": "sonnet"}), nil), want: quiet},
			{name: "subagent_type が fork（モデルは親を継ぐ）",
				mk: mk("Agent", task(map[string]any{"subagent_type": "fork"}), nil), want: quiet},
			{name: "サブエージェントの起動でないツール（Bash）は見ない",
				mk: mk("Bash", map[string]any{"command": "go test ./..."}, nil), want: quiet},
			{name: "サブエージェントの起動でないツール（Edit）は見ない",
				mk: mk("Edit", map[string]any{"file_path": "x.go"}, nil), want: quiet},
			{name: "LOOPTRACK_LOOP_SUBAGENT_MODEL_ALLOW に subagent_type を入れれば通る",
				mk: mk("Agent", task(map[string]any{"subagent_type": "Explore"}),
					map[string]string{"LOOPTRACK_LOOP_SUBAGENT_MODEL_ALLOW": "Explore"}), want: quiet},
			{name: "LOOPTRACK_LOOP_SUBAGENT_MODEL_ALLOW の * は全部を外す",
				mk: mk("Agent", task(map[string]any{"subagent_type": "general-purpose"}),
					map[string]string{"LOOPTRACK_LOOP_SUBAGENT_MODEL_ALLOW": "*"}), want: quiet},
			{name: "LOOPTRACK_LOOP_SUBAGENT_MODEL_ALLOW に別の名しか無ければ止める（対照）",
				mk: mk("Agent", task(map[string]any{"subagent_type": "general-purpose"}),
					map[string]string{"LOOPTRACK_LOOP_SUBAGENT_MODEL_ALLOW": "Explore"}), want: deny},
		}}.run(t)
}

// TestPreToolSubagentModelDefinitionFile は、subagent_type の定義ファイル（.claude/agents/<名>.md）の
// frontmatter に model: があるときは止めないこと（作業ディレクトリの置き場）。
func TestPreToolSubagentModelDefinitionFile(t *testing.T) {
	mk := func(subagentType string) func(*sandbox) call {
		return func(s *sandbox) call {
			return call{hook: "pre-tool-subagent-model", env: map[string]string{"HOME": s.p("home")},
				input: jsonInput(map[string]any{"session_id": "s1", "cwd": s.root, "hook_event_name": "PreToolUse",
					"tool_name": "Agent", "tool_input": map[string]any{"description": "テスト", "prompt": "調べて",
						"subagent_type": subagentType}})}
		}
	}
	deny, quiet := wantDeny, wantQuiet

	scenario{name: "作業ディレクトリの .claude/agents/<名>.md の frontmatter を見る",
		setup: func(s *sandbox) { s.mkdir("home") },
		steps: []step{
			{name: "model: がある",
				do: func(s *sandbox) {
					s.write(s.p(".claude", "agents", "reviewer.md"), "---\nname: reviewer\nmodel: opus\n---\n\n本文\n")
				},
				mk: mk("reviewer"), want: quiet},
			{name: "同じ回でも別の subagent_type なら止める（対照）",
				mk: mk("other-type"), want: deny},
			{name: "frontmatter があっても model: が無ければ止める（対照）",
				do: func(s *sandbox) {
					s.write(s.p(".claude", "agents", "no-model.md"), "---\nname: no-model\ndescription: x\n---\n\n本文\n")
				},
				mk: mk("no-model"), want: deny},
		}}.run(t)

	scenario{name: "~/.claude/agents/<名>.md（HOME）の frontmatter も見る",
		setup: func(s *sandbox) { s.mkdir("home") },
		steps: []step{
			{name: "model: がある",
				do: func(s *sandbox) {
					s.write(s.p("home", ".claude", "agents", "writer.md"), "---\nmodel: haiku\n---\n\n本文\n")
				},
				mk: mk("writer"), want: quiet},
			{name: "cwd にも HOME にも定義ファイルが無ければ止める（対照）",
				mk: mk("ghost-type"), want: deny},
		}}.run(t)
}

// TestPreToolSubagentModelWorksOutsideProject は、looptrack を導入していないディレクトリ
// （プロジェクトの設定・LOOPTRACK_API_URL・LOOPTRACK_PROJECT・サーバへの接続が無い場所）でも
// 同じ判定で動くこと。判定はツールの入力・環境変数・ローカルのファイルだけを見て、サーバへは出ない。
func TestPreToolSubagentModelWorksOutsideProject(t *testing.T) {
	mk := func(input map[string]any) func(*sandbox) call {
		return func(s *sandbox) call {
			return call{hook: "pre-tool-subagent-model",
				env: map[string]string{"HOME": s.p("outside-home"), "LOOPTRACK_API_URL": "", "LOOPTRACK_PROJECT": ""},
				input: jsonInput(map[string]any{"session_id": "s1", "cwd": s.root, "hook_event_name": "PreToolUse",
					"tool_name": "Agent", "tool_input": input})}
		}
	}
	scenario{name: "プロジェクトの設定・サーバ接続が無い一時ディレクトリでも動く",
		setup: func(s *sandbox) { s.mkdir("outside-home") },
		steps: []step{
			{name: "model 未指定は deny（対照: 通す側と対にする）",
				mk: mk(map[string]any{"description": "x", "prompt": "y"}), want: wantDeny},
			{name: "model 指定は通す",
				mk: mk(map[string]any{"description": "x", "prompt": "y", "model": "haiku"}), want: wantQuiet},
		}}.run(t)
}

// TestSubagentModelDenyReason は deny の文面に 3 段の選び方と呼び直し方が入っていること（ja/en 両方）。
func TestSubagentModelDenyReason(t *testing.T) {
	mk := func(lang string) func(*sandbox) call {
		return func(s *sandbox) call {
			return call{hook: "pre-tool-subagent-model",
				env: map[string]string{"HOME": s.p("home"), "LOOPTRACK_LANG": lang},
				input: jsonInput(map[string]any{"session_id": "s1", "cwd": s.root, "hook_event_name": "PreToolUse",
					"tool_name": "Agent", "tool_input": map[string]any{"description": "x", "prompt": "y"}})}
		}
	}
	wantWords := func(words ...string) func(*testing.T, *sandbox, got) {
		return func(t *testing.T, _ *sandbox, g got) {
			t.Helper()
			if g.res.Deny == "" {
				t.Fatalf("deny のはず: %q%s", g.out.Stdout, g.why())
			}
			for _, w := range words {
				if !strings.Contains(g.res.Deny, w) {
					t.Errorf("「%s」が無い:\n%s", w, g.res.Deny)
				}
			}
		}
	}
	scenario{name: "deny の文面（ja/en）",
		setup: func(s *sandbox) { s.mkdir("home") },
		steps: []step{
			{name: "ja", mk: mk("ja"), want: wantWords("haiku", "sonnet", "opus", "model", "LOOPTRACK_LOOP_SUBAGENT_MODEL_ALLOW")},
			{name: "en", mk: mk("en"), want: wantWords("haiku", "sonnet", "opus", "model", "LOOPTRACK_LOOP_SUBAGENT_MODEL_ALLOW")},
		}}.run(t)
}
