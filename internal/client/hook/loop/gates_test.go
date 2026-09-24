package loop

// 以前の kit/loop/verify/verify-gates.sh（12 ケース）と verify-foreign-host.sh（67 ケース）の移植。

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/hookio"
)

func TestGates(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make がありません")
	}
	phony := ".PHONY: build lint test\n"
	makefiles := map[string]string{
		"ok":       phony + "test:\n\t@true\n",
		"fail":     phony + "test:\n\t@false\n",
		"norecipe": phony + "# test のレシピが無い\n",
		"missing":  "test:\n\t@true\n",
		"partial":  "build:\n\t@true\nlint:\n\t@true\n",
		"full":     "build:\n\t@true\nlint:\n\t@true\ntest:\n\t@true\n",
		"fastfail": "a:\n\t@true\nb:\n\t@false\nc:\n\t@echo ran-c > c.txt\n",
	}
	gates := func(repo string, env map[string]string, args ...string) func(*sandbox) call {
		return func(s *sandbox) call {
			m := map[string]string{"CLAUDE_PROJECT_DIR": s.p(repo)}
			for k, v := range env {
				m[k] = v
			}
			return call{hook: "gates", env: m, args: args}
		}
	}
	has := func(g got, w string) bool { return strings.Contains(g.out.Stdout, w) }
	check := func(f func(g got, s *sandbox) bool) func(*testing.T, *sandbox, got) {
		return func(t *testing.T, s *sandbox, g got) {
			t.Helper()
			if !f(g, s) {
				t.Errorf("exit %d:\n%s", g.code, g.out.Stdout)
			}
		}
	}
	scenario{name: "段の判定・引数化", asciiRoot: true,
		setup: func(s *sandbox) {
			for name, mk := range makefiles {
				s.write(s.p(name, "Makefile"), mk)
			}
			s.mkdir("empty")
			s.write(s.p("sub", "app", "Makefile"), "unit:\n\t@true\ne2e:\n\t@true\n")
		},
		steps: []step{
			{name: "レシピを持つ段は実行され PASS", mk: gates("ok", nil, "test"),
				want: check(func(g got, _ *sandbox) bool { return g.code == 0 && has(g, "✓ test: PASS") })},
			{name: "レシピが失敗すれば FAIL（実行されている証拠）", mk: gates("fail", nil, "test"),
				want: check(func(g got, _ *sandbox) bool { return g.code != 0 && has(g, "✘ test: FAIL") })},
			{name: ".PHONY に名前だけの段は失敗（偽緑の防止）", mk: gates("norecipe", nil, "test"),
				want: check(func(g got, _ *sandbox) bool { return g.code != 0 && has(g, "✘ test") && !has(g, "✓ test: PASS") })},
			{name: "Makefile に無い段は失敗", mk: gates("missing", nil, "integration"),
				want: check(func(g got, _ *sandbox) bool { return g.code != 0 && has(g, "✘ integration") })},
			{name: "Makefile が無ければ exit 2 で明示的に止まる", mk: gates("empty", nil, "test"),
				want: check(func(g got, _ *sandbox) bool { return g.code == 2 })},
			{name: "既定の段（build lint test）の 1 つが欠ければ全段グリーンと言わない", mk: gates("partial", nil),
				want: check(func(g got, _ *sandbox) bool {
					return g.code != 0 && !has(g, "全段グリーン") && has(g, "✘ test")
				})},
			{name: "既定の段が全部あれば全段グリーン", mk: gates("full", nil),
				want: check(func(g got, _ *sandbox) bool {
					return g.code == 0 && has(g, "✓ build: PASS") && has(g, "✓ lint: PASS") && has(g, "✓ test: PASS") && has(g, "全段グリーン")
				})},
			{name: "fail-fast（失敗した段より後は実行しない）・失敗した段のログを残して末尾を出す", mk: gates("fastfail", nil, "a", "b", "c"),
				want: check(func(g got, s *sandbox) bool {
					return g.code != 0 && !s.exists(s.p("fastfail", "c.txt")) && s.exists(s.p("fastfail", ".gates", "b.log")) && has(g, "b の失敗ログ")
				})},
			{name: "LOOPTRACK_LOOP_GATES_DIR と LOOPTRACK_LOOP_GATES_STAGES で段とディレクトリを決める",
				mk: gates("sub", map[string]string{"LOOPTRACK_LOOP_GATES_DIR": "app", "LOOPTRACK_LOOP_GATES_STAGES": "unit e2e"}),
				want: check(func(g got, _ *sandbox) bool {
					return g.code == 0 && has(g, "✓ unit: PASS") && has(g, "✓ e2e: PASS")
				})},
			{name: "-C <dir> で Makefile のディレクトリを指定できる", mk: gates("sub", nil, "-C", "app", "unit"),
				want: check(func(g got, _ *sandbox) bool { return g.code == 0 && has(g, "✓ unit: PASS") && !has(g, "e2e") })},
			{name: "ディレクトリを指定しなければプロジェクトの直下（Makefile が無ければ exit 2）", mk: gates("sub", nil, "unit"),
				want: check(func(g got, _ *sandbox) bool { return g.code == 2 })},
		}}.run(t)
}

func TestForeignHost(t *testing.T) {
	needGit(t)
	payload := `{"hook_event_name":"Stop","session_id":"s1","stop_hook_active":false,"prompt":"確認して",
"tool_name":"Bash","tool_input":{"command":"git commit -m x"},"tool_response":"[main 1234567] x\n 1 file changed"}`
	R := func(s *sandbox) string { return s.p("repo") }
	setup := func(s *sandbox) {
		s.git("init", "-q", "-b", "main", R(s))
		s.write(s.p("repo", ".claude", "memories", "handoff.md"), "old\n")
		s.touch(s.p("repo", ".claude", "memories", "handoff.md"), oldTime)
		s.mkdir("repo", ".claude", "handoff-pending.d")
	}
	prime := func(s *sandbox) {
		s.write(s.p("repo", ".claude", "handoff-pending.d", "s1"), "コミット: テスト\n")
	}
	run := func(hook string, agent hookio.Agent, trust bool, env ...string) func(*sandbox) call {
		return func(s *sandbox) call {
			m := map[string]string{}
			for i := 0; i+1 < len(env); i += 2 {
				m[env[i]] = env[i+1]
			}
			for k, v := range m {
				m[k] = strings.ReplaceAll(v, "{R}", R(s))
			}
			return call{hook: hook, agent: agent, bare: true, trust: trust, env: m, cwd: R(s), input: payload}
		}
	}
	silent := func(t *testing.T, _ *sandbox, g got) {
		t.Helper()
		if g.out.ExitCode != 0 || g.out.Stdout != "" || g.out.Stderr != "" {
			t.Errorf("exit 0・出力なしのはず: exit %d %q %q", g.out.ExitCode, g.out.Stdout, g.out.Stderr)
		}
	}
	var steps []step
	// 1. Copilot の印があり Claude Code の印が無い → 全 hook が exit 0・出力なし
	for _, mark := range [][2]string{{"COPILOT_AGENT", "1"}, {"COPILOT_CLI", "1"}, {"AI_AGENT", "github-copilot"}, {"VSCODE_PID", "4242"}, {"VSCODE_IPC_HOOK", "/tmp/x.sock"}} {
		for _, h := range Names() {
			steps = append(steps, step{name: h + "（" + mark[0] + "）", do: prime, mk: run(h, hookio.ClaudeCode, false, mark[0], mark[1]), want: silent})
		}
	}
	// Copilot CLI 1.0.86 が .claude/settings.json の hook に渡す環境（CLAUDE_PROJECT_DIR あり・CLAUDECODE なし）と、
	// Claude Code から起動した Copilot CLI（CLAUDECODE・AI_AGENT=claude-code_… を受け継ぐ）
	for _, h := range Names() {
		steps = append(steps,
			step{name: h + "（Copilot CLI: CLAUDE_PROJECT_DIR + COPILOT_CLI）", do: prime,
				mk: run(h, hookio.ClaudeCode, false, "CLAUDE_PROJECT_DIR", "{R}", "COPILOT_CLI", "1", "COPILOT_CLI_BINARY_VERSION", "1.0.86"), want: silent},
			step{name: h + "（Claude Code から起動した Copilot CLI: CLAUDECODE + COPILOT_PROJECT_DIR）", do: prime,
				mk: run(h, hookio.ClaudeCode, false, "CLAUDECODE", "1", "AI_AGENT", "claude-code_2-1-275_agent", "CLAUDE_PROJECT_DIR", "{R}",
					"COPILOT_CLI", "1", "COPILOT_PROJECT_DIR", "{R}"), want: silent})
	}
	steps = append(steps,
		step{name: "Copilot CLI のシェルから起動した Claude Code（CLAUDECODE + COPILOT_CLI・COPILOT_PROJECT_DIR なし）→ 差し戻す", do: prime,
			mk: run("stop-handoff-freshness", hookio.ClaudeCode, false, "CLAUDECODE", "1", "CLAUDE_PROJECT_DIR", "{R}", "COPILOT_CLI", "1",
				"COPILOT_AGENT_SESSION_ID", "c1"), want: wantBlock},
		step{name: "対照: 印が何も無ければ（git のルートで）Stop を差し戻す", do: prime, mk: run("stop-handoff-freshness", hookio.ClaudeCode, false), want: wantBlock},
		// 2. Claude Code では従来どおり（Copilot の印があっても）
		step{name: "CLAUDE_PROJECT_DIR あり + COPILOT_AGENT → 差し戻す", do: prime,
			mk: run("stop-handoff-freshness", hookio.ClaudeCode, false, "CLAUDE_PROJECT_DIR", "{R}", "COPILOT_AGENT", "1"), want: wantBlock},
		step{name: "CLAUDECODE あり + VSCODE_PID → 差し戻す", do: prime,
			mk: run("stop-handoff-freshness", hookio.ClaudeCode, false, "CLAUDECODE", "1", "VSCODE_PID", "4242"), want: wantBlock},
		step{name: "AI_AGENT=claude-code_…（Claude Code 自身が設定する値）は印に数えない → 差し戻す", do: prime,
			mk: run("stop-handoff-freshness", hookio.ClaudeCode, false, "AI_AGENT", "claude-code_2-1-275_agent"), want: wantBlock},
		// 3. 自分向けの配線（LOOPTRACK_LOOP_AGENT・Codex）では動く
		step{name: "LOOPTRACK_LOOP_AGENT=copilot + COPILOT_AGENT → 知らせる（systemMessage）", do: prime,
			mk:   run("stop-handoff-freshness", hookio.Copilot, false, "LOOPTRACK_LOOP_AGENT", "copilot", "LOOPTRACK_LOOP_NO_BLOCK", "1", "COPILOT_AGENT", "1"),
			want: wantSystemMessage("")},
		step{name: "CODEX_THREAD_ID + VSCODE_PID → 差し戻す（Codex の状態）",
			do: func(s *sandbox) {
				s.write(s.p("repo", ".codex", "handoff-pending.d", "s1"), "コミット: テスト\n")
			},
			// Codex の Stop の差し戻しは実物で未確認（hookio の capabilities）。確かめる試験の形（trust）で見る
			mk: run("stop-handoff-freshness", hookio.Codex, true, "CODEX_THREAD_ID", "t1", "VSCODE_PID", "4242"), want: wantBlock},
	)
	scenario{name: "Claude Code 以外が Claude Code 向けの配線を起動したとき", setup: setup, steps: steps}.run(t)

	// 4. 判定の環境変数（以前の hook（1.0.0 より前）の CLAUDE_ENV・OWN_WIRING_ENV・COPILOT_HOOK_ENV・FOREIGN_ENV を記録したもの。
	// 旧名 IM_LOOP_AGENT は LOOPTRACK_LOOP_AGENT に読み替えた。以前の実装は撤去済み）
	t.Run("判定の環境変数", func(t *testing.T) {
		tuples := map[string][]string{
			"CLAUDE_ENV":       {"CLAUDECODE", "CLAUDE_PROJECT_DIR"},
			"OWN_WIRING_ENV":   {"CODEX_THREAD_ID", "CODEX_SESSION_ID", "GEMINI_CLI", "GEMINI_PROJECT_DIR", "LOOPTRACK_LOOP_AGENT"},
			"COPILOT_HOOK_ENV": {"COPILOT_PROJECT_DIR"},
			"FOREIGN_ENV":      {"COPILOT_CLI", "COPILOT_AGENT", "AI_AGENT", "VSCODE_PID", "VSCODE_IPC_HOOK"},
		}
		tuple := func(name string) []string { return tuples[name] }
		raw := map[string]any{"hook_event_name": "Stop"}
		for _, v := range append(tuple("CLAUDE_ENV"), tuple("OWN_WIRING_ENV")...) {
			env := map[string]string{v: "1", "COPILOT_AGENT": "1"}
			if hookio.ForeignHost(func(k string) string { return env[k] }, raw) {
				t.Errorf("%s があれば Claude Code 以外と見なさないはず", v)
			}
		}
		for _, v := range tuple("COPILOT_HOOK_ENV") {
			env := map[string]string{v: "x", "CLAUDECODE": "1", "CLAUDE_PROJECT_DIR": "/p"}
			if !hookio.ForeignHost(func(k string) string { return env[k] }, raw) {
				t.Errorf("%s があれば CLAUDECODE があっても Claude Code 以外と見なすはず", v)
			}
		}
		for _, v := range tuple("FOREIGN_ENV") {
			env := map[string]string{v: "x"}
			if !hookio.ForeignHost(func(k string) string { return env[k] }, raw) {
				t.Errorf("%s があれば Claude Code 以外と見なすはず", v)
			}
		}
	})
}
