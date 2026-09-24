package loop

// kit/loop/verify/verify-task-mode.sh の移植（42 ケース）。同じフォルダで 2 つのセッションが動いている状況を、session_id 付きの入力で再現する。

import (
	"fmt"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/hookio"
)

// kindOf は user-prompt-task-mode の出力を execute / investigate / continue / quiet / invalid / other に分類する（verify の kind）。
func kindOf(g got) string {
	if g.quiet() {
		return "quiet"
	}
	c := g.ctx()
	switch {
	case c == "":
		return "invalid"
	case strings.Contains(c, "確認モードに設定"):
		return "investigate"
	case strings.Contains(c, "実行モードに設定"):
		return "execute"
	case strings.Contains(c, "確認モードが続いて"):
		return "continue"
	}
	return "other"
}

func wantKind(k string) func(*testing.T, *sandbox, got) {
	return func(t *testing.T, _ *sandbox, g got) {
		t.Helper()
		if kindOf(g) != k {
			t.Errorf("期待 %s / 実際 %s: %s", k, kindOf(g), g.out.Stdout)
		}
	}
}

// wantEdit は pre-edit-task-mode-guard の結果（block = deny / pass = 何も出さない）。
func wantEdit(want string) func(*testing.T, *sandbox, got) {
	return func(t *testing.T, _ *sandbox, g got) {
		t.Helper()
		have := "pass"
		if g.res.Deny != "" && g.json != nil {
			if h, _ := g.json["hookSpecificOutput"].(map[string]any); h["permissionDecision"] == "deny" {
				have = "block"
			}
		}
		if have == "pass" && !g.quiet() {
			have = "output"
		}
		if have != want {
			t.Errorf("期待 %s / 実際 %s: %s", want, have, g.out.Stdout)
		}
	}
}

func wantFileFirstLine(path func(*sandbox) string, want string) func(*testing.T, *sandbox, got) {
	return func(t *testing.T, s *sandbox, _ got) {
		t.Helper()
		if l := strings.SplitN(s.read(path(s)), "\n", 2)[0]; l != want {
			t.Errorf("%s の 1 行目: 期待 %q / 実際 %q", path(s), want, l)
		}
	}
}

func TestTaskMode(t *testing.T) {
	proj := func(s *sandbox) string { return s.p("proj") }
	say := func(sid, prompt string, env ...string) func(*sandbox) call {
		return func(s *sandbox) call {
			in := map[string]any{"prompt": prompt}
			if sid != "" {
				in["session_id"] = sid
			}
			m := map[string]string{"CLAUDE_PROJECT_DIR": proj(s)}
			for i := 0; i+1 < len(env); i += 2 {
				m[env[i]] = env[i+1]
			}
			return call{hook: "user-prompt-task-mode", env: m, input: jsonInput(in)}
		}
	}
	edit := func(sid string, file func(*sandbox) string, env ...string) func(*sandbox) call {
		return func(s *sandbox) call {
			in := map[string]any{"tool_name": "Edit", "tool_input": map[string]any{"file_path": file(s)}}
			if sid != "" {
				in["session_id"] = sid
			}
			m := map[string]string{"CLAUDE_PROJECT_DIR": proj(s)}
			for i := 0; i+1 < len(env); i += 2 {
				m[env[i]] = env[i+1]
			}
			return call{hook: "pre-edit-task-mode-guard", env: m, input: jsonInput(in)}
		}
	}
	doc := func(s *sandbox) string { return s.p("proj", "docs", "x.md") }
	path := func(parts ...string) func(*sandbox) string { return func(s *sandbox) string { return s.p(parts...) } }
	rel := func(p string) func(*sandbox) string { return func(*sandbox) string { return p } }
	setup := func(s *sandbox) {
		s.mkdir("proj", ".claude", "prompts")
		s.mkdir("proj", "docs")
		s.mkdir("proj", "references")
	}

	scenario{name: "判定", setup: setup, steps: []step{
		{name: "「確認して」→ 確認モード", mk: say("A0", "記憶を確認して"), want: wantKind("investigate")},
		{name: "「実装して」→ 実行モード", mk: say("A0", "A と B を実装して"), want: wantKind("execute")},
		{name: "通知は判定しない", mk: say("A0", "<task-notification>実装して</task-notification>"), want: wantKind("quiet")},
		{name: "「洗い出して」→ 確認", mk: say("A1", "影響範囲を洗い出して"), want: wantKind("investigate")},
		{name: "「選別して」→ 確認", mk: say("A1", "候補を選別して"), want: wantKind("investigate")},
		{name: "「移植して」→ 実行", mk: say("A1", "この hook を移植して"), want: wantKind("execute")},
		{name: "「デプロイして」→ 実行", mk: say("A1", "検証環境にデプロイして"), want: wantKind("execute")},
		{name: "「執筆して」→ 実行", mk: say("A1", "第 3 章を執筆して"), want: wantKind("execute")},
		{name: "実行系と確認系が両方なら実行が勝つ", mk: say("A1", "確認して、問題なければ修正して"), want: wantKind("execute")},
		{name: "どちらも無ければ何も出さない（実行中）", mk: say("A1", "ありがとう"), want: wantKind("quiet")},
		{name: "JSON の形（hookEventName）", mk: say("A1", "確認して"), want: func(t *testing.T, _ *sandbox, g got) {
			if g.hookEventName() != "UserPromptSubmit" {
				t.Errorf("hookEventName: %s", g.out.Stdout)
			}
		}},
	}}.run(t)

	// 英語の別名（DESIGN.md §5-13）。単語の境界・大小を問わない
	scenario{name: "英語の依頼", setup: setup, steps: []step{
		{name: "please check → 確認", mk: say("E1", "Please check the memory files."), want: wantKind("investigate")},
		{name: "implement → 実行", mk: say("E1", "Implement A and B"), want: wantKind("execute")},
		{name: "look into（空白 2 つ）→ 確認", mk: say("E1", "Could you look  into the failing test?"), want: wantKind("investigate")},
		{name: "INVESTIGATE（大文字）→ 確認", mk: say("E1", "INVESTIGATE the crash"), want: wantKind("investigate")},
		{name: "go ahead → 実行", mk: say("E1", "OK, go ahead."), want: wantKind("execute")},
		{name: "analyse / analyze → 確認", mk: say("E1", "analyse the logs"), want: wantKind("investigate")},
		{name: "英語でも実行系と確認系が両方なら実行が勝つ", mk: say("E1", "Review it, and if it looks fine, fix it"), want: wantKind("execute")},
		{name: "語の一部では判定しない（fixture・prefix・checkout・reviewer・planet・addition）",
			mk: say("E2", "fixture prefix checkout reviewer planet addition updated committed"), want: wantKind("quiet")},
		{name: "語の一部の実行系の語で確認を上書きしない", mk: say("E2", "check the fixtures"), want: wantKind("investigate")},
		{name: "日本語で決まれば英語の語は見ない（コマンド名を含む確認の依頼）", mk: say("E3", "deploy.sh と git の merge の手順を確認して"), want: wantKind("investigate")},
		{name: "日本語の実行系は英語の確認系に勝つ", mk: say("E3", "review の指摘を修正して"), want: wantKind("execute")},
		{name: "日本語の依頼が無ければ英語の語で決める", mk: say("E3", "この PR を review してほしい"), want: wantKind("investigate")},
		{mk: say("E4", "Explain why it fails")},
		{name: "英語の確認の依頼で編集が止まる", mk: edit("E4", doc), want: wantEdit("block")},
		{mk: say("E4", "Now apply the patch")},
		{name: "英語の実行の依頼で編集が通る", mk: edit("E4", doc), want: wantEdit("pass")},
	}}.run(t)

	// 他のセッションからの連絡は利用者の依頼ではないので判定しない。
	// 形は実物に合わせた（値は架空。Claude Code の UserPromptSubmit に、harness の定型文で挟んだこのタグが利用者の発話として渡る）。
	crossJa := "Another Claude session sent a message:\n" +
		`<cross-session-message from="uds:/tmp/cc-socks/12345.sock" from-session="local_0123abcd" from-name="2. 別の作業のセッション" from-mode="prompting">` + "\n" +
		"【別のセッションより】`.claude/task-mode.d/…` の live 値を確認してください。ABC-0123 を割り当てます。\n" +
		"</cross-session-message>\n" +
		"This came from another Claude session — not typed by your user, but very likely working on their behalf.\n"
	crossShort := "Another Claude session sent a message:\n" +
		`<cross-session-message from="local_89abcdef" name="3. 引き継ぎ">取り込みの前に確認してください。</cross-session-message>`
	crossTrunc := `<cross-session-message from="local_89abcdef" name="36">途中で切れた連絡。ABC-0123 を確認して`
	crossEn := "Another Claude session sent a message:\n" +
		`<cross-session-message from="local_89abcdef" name="35">Please review the merge before you push.</cross-session-message>`

	scenario{name: "他セッションからの連絡は判定しない", setup: setup, steps: []step{
		{name: "利用者の「確認して」は従来どおり確認モード", mk: say("X", "記憶を確認して"), want: wantKind("investigate")},
		{name: "確認モード中の編集は止まる", mk: edit("X", doc), want: wantEdit("block")},
		{name: "利用者の「実装して」で実行モードへ", mk: say("X", "では実装して"), want: wantKind("execute")},
		{name: "他セッションからの連絡（「確認」を含む）では確認モードに落ちない", mk: say("X", crossJa), want: wantKind("quiet")},
		{name: "連絡の後も編集は止まらない", mk: edit("X", doc), want: wantEdit("pass")},
		{name: "属性の短い形（from / name）の連絡も判定しない", mk: say("X", crossShort), want: wantKind("quiet")},
		{name: "閉じタグが無い（途中で切れた）連絡も判定しない", mk: say("X", crossTrunc), want: wantKind("quiet")},
		{name: "英語の連絡（review を含む）も判定しない", mk: say("X", crossEn), want: wantKind("quiet")},
		{name: "連絡の後でも利用者の依頼は効く", mk: say("X", "push の前に確認して"), want: wantKind("investigate")},
		{name: "利用者の依頼なので編集は止まる", mk: edit("X", doc), want: wantEdit("block")},
	}}.run(t)

	scenario{name: "2 セッションが同じフォルダにいる・通知・例外・24 時間・環境変数・session_id が無いとき・記録先", setup: setup, steps: []step{
		{mk: say("A", "調査結果を確認して。")},
		{name: "A に『確認して』→ A の編集は止まる", mk: edit("A", doc), want: wantEdit("block")},
		{name: "A の『確認して』は B の編集を止めない", mk: edit("B", doc), want: wantEdit("pass")},
		{mk: say("B", "実行して。")},
		{name: "B に『実行して』→ A の確認モードは解けない", mk: edit("A", doc), want: wantEdit("block")},
		{name: "B は実行モード", mk: edit("B", doc), want: wantEdit("pass")},

		{mk: say("A", "<task-notification><summary>完了</summary>実行して</task-notification>")},
		{name: "通知の中の『実行して』では A のモードは変わらない", mk: edit("A", doc), want: wantEdit("block")},
		// Copilot CLI は Stop の差し戻しの理由を次の利用者のメッセージとして渡す（「更新してください」で実行モードにしない）
		{name: "Copilot の Stop の差し戻しの理由は判定しない", mk: say("A", "[Stop hook の差し戻し] 引き継ぎが作業の完了に追いついていません。対応: skill session-handoff の手順で引き継ぎを更新してください。TST-0009 に着手して"), want: wantKind("quiet")},
		{name: "差し戻しの理由の『更新して』では A のモードは変わらない", mk: edit("A", doc), want: wantEdit("block")},
		{name: "確認モードでも自セッションの記録の書き換えは通す", mk: edit("A", path("proj", ".claude", "task-mode.d", "A")), want: wantEdit("pass")},
		{name: "確認モードでも別セッションの記録は書き換えさせない", mk: edit("A", path("proj", ".claude", "task-mode.d", "B")), want: wantEdit("block")},
		{name: "確認モードでもプロジェクトの外は通す", mk: edit("A", path("scratch.md")), want: wantEdit("pass")},
		{name: "例外ディレクトリの指定が無ければ .claude/prompts も止める", mk: edit("A", path("proj", ".claude", "prompts", "p.md")), want: wantEdit("block")},
		{name: "LOOPTRACK_LOOP_TASK_MODE_ALLOW_DIRS の場所は通す（prompts）",
			mk:   edit("A", path("proj", ".claude", "prompts", "p.md"), "LOOPTRACK_LOOP_TASK_MODE_ALLOW_DIRS", "references .claude/prompts/"),
			want: wantEdit("pass")},
		{name: "LOOPTRACK_LOOP_TASK_MODE_ALLOW_DIRS の場所は通す（references）",
			mk:   edit("A", path("proj", "references", "memo.md"), "LOOPTRACK_LOOP_TASK_MODE_ALLOW_DIRS", "references .claude/prompts/"),
			want: wantEdit("pass")},
		{name: "例外の場所の外は止める",
			mk:   edit("A", path("proj", "referencesX", "memo.md"), "LOOPTRACK_LOOP_TASK_MODE_ALLOW_DIRS", "references"),
			want: wantEdit("block")},
		{name: "継続中の案内は自セッションの記録の場所を示す", mk: say("A", "続きをお願いします。"), want: func(t *testing.T, _ *sandbox, g got) {
			if !strings.Contains(g.ctx(), "echo execute > .claude/task-mode.d/A") {
				t.Errorf("案内: %s", g.ctx())
			}
		}},
		{name: "deny の案内も自セッションの記録の場所を示す",
			mk: func(s *sandbox) call {
				return call{hook: "pre-edit-task-mode-guard", env: map[string]string{"CLAUDE_PROJECT_DIR": proj(s)},
					input: jsonInput(map[string]any{"tool_input": map[string]any{"file_path": doc(s)}, "session_id": "A"})}
			},
			want: func(t *testing.T, _ *sandbox, g got) {
				if !strings.Contains(g.res.Deny, "echo execute > .claude/task-mode.d/A") {
					t.Errorf("案内: %s", g.out.Stdout)
				}
			}},
		{name: "相対パスの指定もプロジェクト内として止める", mk: edit("A", rel("docs/x.md")), want: wantEdit("block")},
		{mk: say("A", "では実行して。")},
		{name: "A 自身に『実行して』→ A の編集が通る", mk: edit("A", doc), want: wantEdit("pass")},

		// 24 時間より古い記録は無効
		{mk: say("S", "確認して")},
		{name: "古い確認モードの記録ではガードしない", do: func(s *sandbox) { s.touch(s.p("proj", ".claude", "task-mode.d", "S"), oldTime) },
			mk: edit("S", doc), want: wantEdit("pass")},

		// 環境変数での調整
		{name: "LOOPTRACK_LOOP_TASK_MODE_INVEST_RE で確認系の語を足せる", mk: say("E", "仕様を精査しておいて", "LOOPTRACK_LOOP_TASK_MODE_INVEST_RE", "精査"),
			want: wantKind("investigate")},
		{name: "LOOPTRACK_LOOP_TASK_MODE_EXEC_RE で実行系の語を足せる・EXEC_NOTE が足される",
			mk: say("E", "本番に出しておいて", "LOOPTRACK_LOOP_TASK_MODE_EXEC_RE", "出しておいて", "LOOPTRACK_LOOP_TASK_MODE_EXEC_NOTE", "（push は別途の指示が要る）"),
			want: all(wantKind("execute"), func(t *testing.T, _ *sandbox, g got) {
				if !strings.Contains(g.ctx(), "push は別途の指示が要る") {
					t.Errorf("EXEC_NOTE: %s", g.ctx())
				}
			})},
		{name: "LOOPTRACK_LOOP_STATE_DIR に記録する", mk: func(s *sandbox) call { return say("F", "確認して", "LOOPTRACK_LOOP_STATE_DIR", s.p("state"))(s) },
			want: wantFileFirstLine(path("state", "task-mode.d", "F"), "investigate")},

		// session_id が無いとき（手動実行など）は共有の記録
		{name: "共有の記録に書く", mk: say("", "確認して。"), want: wantFileFirstLine(path("proj", ".claude", "task-mode"), "investigate")},
		{name: "session_id の無い編集は止まる", mk: edit("", doc), want: wantEdit("block")},
		{name: "共有の記録は session_id のあるセッションに効かない", mk: edit("C", doc), want: wantEdit("pass")},

		// 記録先がフォルダの外へ出ない
		{name: "『../』を含む ID でもフォルダの外に書かない・記号を除いた ID で記録する", mk: say("../../escape", "確認して。"),
			want: func(t *testing.T, s *sandbox, _ got) {
				if s.exists(s.p("proj", "escape")) || s.exists(s.p("proj", ".claude", "escape")) {
					t.Error("フォルダの外に書いた")
				}
				wantFileFirstLine(path("proj", ".claude", "task-mode.d", "escape"), "investigate")(t, s, got{})
			}},
	}}.run(t)

	t.Run("git", func(t *testing.T) {
		needGit(t)
		gitSetup := func(s *sandbox) {
			setup(s)
			s.git("init", "-q", "-b", "main", s.p("repo"))
			s.git("-C", s.p("repo"), "commit", "-q", "--allow-empty", "-m", "base")
			s.git("-C", s.p("repo"), "worktree", "add", "-q", s.p("repo-wt"), "-b", "wt")
			s.mkdir("repo", "sub")
		}
		wt := func(file func(*sandbox) string, env ...string) func(*sandbox) call {
			return func(s *sandbox) call {
				m := map[string]string{"CLAUDE_PROJECT_DIR": s.p("repo")}
				for i := 0; i+1 < len(env); i += 2 {
					m[env[i]] = env[i+1]
				}
				return call{hook: "pre-edit-task-mode-guard", env: m,
					input: jsonInput(map[string]any{"tool_input": map[string]any{"file_path": file(s)}, "session_id": "W"})}
			}
		}
		scenario{name: "worktree も「プロジェクト内」・Codex の記録", setup: gitSetup, steps: []step{
			{mk: func(s *sandbox) call {
				return call{hook: "user-prompt-task-mode", env: map[string]string{"CLAUDE_PROJECT_DIR": s.p("repo")},
					input: `{"prompt":"確認して","session_id":"W"}`}
			}},
			{name: "同じリポジトリの worktree の編集も止める", mk: wt(path("repo-wt", "src", "a.go")), want: wantEdit("block")},
			{name: "別のリポジトリは止めない", mk: wt(doc), want: wantEdit("pass")},
			{name: "worktree の中の例外ディレクトリは通す", do: func(s *sandbox) { s.mkdir("repo-wt", "references") },
				mk: wt(path("repo-wt", "references", "m.md"), "LOOPTRACK_LOOP_TASK_MODE_ALLOW_DIRS", "references"), want: wantEdit("pass")},

			// Codex（CLAUDE_PROJECT_DIR を渡さない）は git のルートの .codex/ に記録する
			{name: "記録は <git のルート>/.codex/task-mode.d/<session_id>",
				mk: func(s *sandbox) call {
					return call{hook: "user-prompt-task-mode", agent: hookio.Codex, env: map[string]string{"CODEX_THREAD_ID": "CX"},
						cwd: s.p("repo", "sub"), input: `{"prompt":"確認して","session_id":"CX"}`}
				},
				want: wantFileFirstLine(path("repo", ".codex", "task-mode.d", "CX"), "investigate")},
			{name: "Codex の記録でも編集を止める",
				mk: func(s *sandbox) call {
					// Codex の PreToolUse の deny は実物で未確認（hookio の capabilities）なので、確かめる試験の形（trust）で見る
					return call{hook: "pre-edit-task-mode-guard", agent: hookio.Codex, env: map[string]string{"CODEX_THREAD_ID": "CX"}, trust: true,
						cwd: s.p("repo", "sub"), input: jsonInput(map[string]any{"tool_input": map[string]any{"file_path": s.p("repo", "src", "b.go")}, "session_id": "CX"})}
				},
				want: wantEdit("block")},
		}}.run(t)

		// Copilot CLI（1.0.86 で実測）の PascalCase の配線の PreToolUse は tool_name が Claude Code と同じ（Edit・Write・Read）で、
		// 入力は path（Edit は old_str・new_str、Write は file_text）。GPT 系のモデルの Edit は tool_input が apply_patch の本文の文字列。
		// VS Code は create_file・replace_string_in_file（filePath）。どれでも確認モードの編集を止める。
		cp := func(tool string, input func(*sandbox) any) func(*sandbox) call {
			return func(s *sandbox) call {
				return call{hook: "pre-edit-task-mode-guard", agent: hookio.Copilot, env: map[string]string{"LOOPTRACK_LOOP_AGENT": "copilot"},
					cwd: s.p("repo", "sub"), input: jsonInput(map[string]any{"hook_event_name": "PreToolUse", "session_id": "CP",
						"tool_name": tool, "tool_input": input(s)})}
			}
		}
		patch := func(files ...string) func(*sandbox) any {
			return func(s *sandbox) any {
				b := "*** Begin Patch\n"
				for _, f := range files {
					b += "*** Update File: " + s.p(f) + "\n@@\n-a\n+b\n"
				}
				return b + "*** End Patch\n"
			}
		}
		scenario{name: "GitHub Copilot の編集ツール", setup: gitSetup,
			steps: []step{
				{mk: func(s *sandbox) call {
					return call{hook: "user-prompt-task-mode", agent: hookio.Copilot, env: map[string]string{"LOOPTRACK_LOOP_AGENT": "copilot"},
						cwd: s.p("repo", "sub"), input: `{"hook_event_name":"UserPromptSubmit","prompt":"確認して","session_id":"CP"}`}
				}},
				{name: "Copilot CLI の Edit（path）を止める", mk: cp("Edit", func(s *sandbox) any {
					return map[string]any{"path": s.p("repo", "src", "a.go"), "old_str": "a", "new_str": "b"}
				}), want: wantEdit("block")},
				{name: "Copilot CLI の Write（path）を止める", mk: cp("Write", func(s *sandbox) any {
					return map[string]any{"path": s.p("repo", "new.txt"), "file_text": "x"}
				}), want: wantEdit("block")},
				{name: "Copilot CLI の Edit（apply_patch の本文の文字列）を止める", mk: cp("Edit", patch("repo/src/a.go")), want: wantEdit("block")},
				{name: "apply_patch の 2 つ目のファイルがプロジェクト内でも止める", mk: cp("Edit", patch("outside.txt", "repo/src/a.go")), want: wantEdit("block")},
				{name: "apply_patch がプロジェクトの外だけなら通す", mk: cp("Edit", patch("outside.txt")), want: wantEdit("pass")},
				{name: "Copilot CLI の Read（path）は止めない", mk: cp("Read", func(s *sandbox) any {
					return map[string]any{"path": s.p("repo", "src", "a.go")}
				}), want: wantEdit("pass")},
				{name: "Copilot CLI の Bash は止めない", mk: cp("Bash", func(s *sandbox) any { return map[string]any{"command": "echo x > src/a.go"} }), want: wantEdit("pass")},
				{name: "VS Code の replace_string_in_file（filePath）を止める", mk: cp("replace_string_in_file", func(s *sandbox) any {
					return map[string]any{"filePath": s.p("repo", "src", "a.go"), "oldString": "a", "newString": "b"}
				}), want: wantEdit("block")},
				{name: "Copilot の拒否は permissionDecision をトップレベル（CLI）にも置く", mk: cp("Edit", func(s *sandbox) any {
					return map[string]any{"path": s.p("repo", "src", "a.go")}
				}), want: func(t *testing.T, _ *sandbox, g got) {
					if g.json["permissionDecision"] != "deny" || !strings.Contains(fmt.Sprint(g.json["permissionDecisionReason"]), "確認モード中") {
						t.Errorf("出力の形: %s", g.out.Stdout)
					}
				}},
			}}.run(t)
	})
}
