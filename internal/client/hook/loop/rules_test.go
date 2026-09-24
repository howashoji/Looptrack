package loop

// kit/loop/verify/verify-rules-inject.sh の移植（16 ケース）。

import (
	"testing"

	"github.com/howashoji/looptrack/internal/hookio"
)

func TestRulesInject(t *testing.T) {
	none := func(s *sandbox) map[string]string { return map[string]string{"CLAUDE_PROJECT_DIR": s.p("none")} }
	cpIn := func(s *sandbox) string {
		return `{"hook_event_name":"SessionStart","session_id":"cp1","timestamp":"2026-09-18T12:00:00.000Z","cwd":` + jstr(s.root) + `,"source":"new"}`
	}
	sessionKit := []string{"毎セッションの要点", "全作業共通の規律", "ツール呼び出しは**メッセージの冒頭**", "事実認定"}
	scenario{name: "kit の rules そのもの", steps: []step{
		{name: "SessionStart: 出力規律と作業規律の節が入る",
			mk:   func(s *sandbox) call { return call{hook: "session-start-rules", env: none(s), input: "{}"} },
			want: wantContext("SessionStart", sessionKit, []string{"任意節", "毎ターンの要点", "looptrack:inject", "リモートとの同期"})},
		{name: "UserPromptSubmit: 毎ターンの要点だけが入る",
			mk:   func(s *sandbox) call { return call{hook: "user-prompt-rules", env: none(s), input: "{}"} },
			want: wantContext("UserPromptSubmit", []string{"[出力規律]", "[作業規律]"}, []string{"全作業共通の規律", "毎セッションの要点", "looptrack:inject"})},
	}}.run(t)

	scenario{name: "AI ごとの印", steps: []step{
		{name: "Claude Code では Codex 向けの要点を入れない",
			mk: func(s *sandbox) call { return call{hook: "session-start-rules", env: none(s), input: "{}"} },
			want: wantContext("SessionStart", []string{"毎セッションの要点"},
				[]string{"要点（iteration-discipline.md）", "要点（background-process.md）"})},
		{name: "Codex では Claude の書式の節を入れず Codex 向けの要点を入れる",
			mk: func(s *sandbox) call {
				return call{hook: "session-start-rules", agent: hookio.Codex, env: map[string]string{"CODEX_THREAD_ID": "t1"}, input: "{}"}
			},
			want: wantContext("SessionStart", []string{"全作業共通の規律", "要点（iteration-discipline.md）", "要点（background-process.md）"},
				[]string{"毎セッションの要点", "antml"})},
		{name: "Codex の毎ターンは作業規律だけ",
			mk: func(s *sandbox) call {
				return call{hook: "user-prompt-rules", agent: hookio.Codex, env: map[string]string{"CODEX_THREAD_ID": "t1"}, input: "{}"}
			},
			want: wantContext("UserPromptSubmit", []string{"[作業規律]"}, []string{"[出力規律]"})},
		{name: "LOOPTRACK_LOOP_AGENT（Go 版は --agent）で AI を指定できる",
			mk: func(s *sandbox) call {
				return call{hook: "user-prompt-rules", agent: hookio.Codex,
					env: map[string]string{"CLAUDE_PROJECT_DIR": s.p("none"), "LOOPTRACK_LOOP_AGENT": "codex"}, input: "{}"}
			},
			want: wantContext("UserPromptSubmit", []string{"[作業規律]"}, []string{"[出力規律]"})},
		{name: "Copilot では Claude の書式の節を入れず Copilot 向けの要点を入れる",
			mk: func(s *sandbox) call {
				return call{hook: "session-start-rules", agent: hookio.Copilot, env: map[string]string{"LOOPTRACK_LOOP_AGENT": "copilot"}, input: cpIn(s)}
			},
			want: wantContext("SessionStart", []string{"全作業共通の規律", "要点（iteration-discipline.md）", "要点（background-process.md）"},
				[]string{"毎セッションの要点", "antml"})},
		{name: "Copilot の毎ターンは作業規律だけ",
			mk: func(s *sandbox) call {
				return call{hook: "user-prompt-rules", agent: hookio.Copilot, env: map[string]string{"LOOPTRACK_LOOP_AGENT": "copilot"},
					input: `{"hook_event_name":"UserPromptSubmit","session_id":"cp1","prompt":"直して"}`}
			},
			want: wantContext("UserPromptSubmit", []string{"[作業規律]"}, []string{"[出力規律]"})},
		{name: "Copilot の SessionStart は additionalContext をトップレベルと hookSpecificOutput の両方に置く",
			mk: func(s *sandbox) call {
				return call{hook: "session-start-rules", agent: hookio.Copilot, env: map[string]string{"LOOPTRACK_LOOP_AGENT": "copilot"}, input: cpIn(s)}
			},
			want: func(t *testing.T, _ *sandbox, g got) {
				h, _ := g.json["hookSpecificOutput"].(map[string]any)
				if top, _ := g.json["additionalContext"].(string); top == "" || top != h["additionalContext"] {
					t.Errorf("出力の形: %s", g.out.Stdout)
				}
			}},
		{name: "Claude Code の SessionStart の出力は変えない（hookSpecificOutput だけ）",
			mk: func(s *sandbox) call { return call{hook: "session-start-rules", env: none(s), input: "{}"} },
			want: func(t *testing.T, _ *sandbox, g got) {
				if _, ok := g.json["hookSpecificOutput"]; !ok || len(g.json) != 1 {
					t.Errorf("出力の形: %s", g.out.Stdout)
				}
			}},
	}}.run(t)

	aMD := "# A\n\n## 注入する\n<!-- looptrack:inject session -->\n\n本文 A1\n### 下の階層も含む\n本文 A2\n```\n## フェンスの中の見出しで切らない\n```\n本文 A3\n\n" +
		"## 注入しない\n本文 X\n\n## 毎ターン\n<!-- looptrack:inject prompt -->\n一行目 P1\n\n二行目 P2\n"
	bMD := "## 印がフェンスの中\n```\n<!-- looptrack:inject session -->\n```\n本文 Y\n"
	scenario{name: "抜き出しの規則（一時の rules）",
		setup: func(s *sandbox) {
			s.write(s.p("rules", "a.md"), aMD)
			s.write(s.p("rules", "b.md"), bMD)
		},
		steps: []step{
			{name: "節は同じ階層の次の見出しまで（下の階層・フェンスの中を含む）",
				mk: func(s *sandbox) call {
					return call{hook: "session-start-rules", env: map[string]string{"LOOPTRACK_LOOP_RULES_DIR": s.p("rules")}, input: "{}"}
				},
				want: wantContext("SessionStart", []string{"本文 A1", "下の階層も含む", "本文 A2", "フェンスの中の見出しで切らない", "本文 A3"},
					[]string{"本文 X", "P1", "本文 Y"})},
			{name: "prompt は印の付いた節の空でない行だけ",
				mk: func(s *sandbox) call {
					return call{hook: "user-prompt-rules", env: map[string]string{"LOOPTRACK_LOOP_RULES_DIR": s.p("rules")}, input: "{}"}
				},
				want: wantContext("UserPromptSubmit", []string{"一行目 P1", "二行目 P2"}, []string{"本文 A1", "注入する"})},
			{name: "第 1 引数でディレクトリを渡せる",
				mk: func(s *sandbox) call {
					return call{hook: "session-start-rules", args: []string{s.p("rules")}, input: "{}"}
				},
				want: wantContext("SessionStart", []string{"本文 A1"}, nil)},
		}}.run(t)

	// 日本語が正本、英語は同じディレクトリの en/（kit/README.ja.md「本文の言語」）。
	// 訳の無いファイルは正本の日本語に戻る。導入では日英の両方を置くので、言語を変えれば注入も切り替わる。
	scenario{name: "言語で選ぶ（日本語が正本・英語は en/）",
		setup: func(s *sandbox) {
			s.write(s.p("i18n", "x.md"), "## 要点\n<!-- looptrack:inject session -->\n日本語の本文 X\n")
			s.write(s.p("i18n", "en", "x.md"), "## Key points\n<!-- looptrack:inject session -->\nEnglish body X\n")
			s.write(s.p("i18n", "y.md"), "## 訳なし\n<!-- looptrack:inject session -->\n訳の無い本文 Y\n")
		},
		steps: []step{
			{name: "ja では正本（日本語）を入れる",
				mk: func(s *sandbox) call {
					return call{hook: "session-start-rules", env: map[string]string{"LOOPTRACK_LOOP_RULES_DIR": s.p("i18n"), "LOOPTRACK_LANG": "ja"}, input: "{}"}
				},
				want: wantContext("SessionStart", []string{"日本語の本文 X", "訳の無い本文 Y"}, []string{"English body X", "Key points"})},
			{name: "en では en/ の訳を入れ、訳が無いものは日本語に戻る",
				mk: func(s *sandbox) call {
					return call{hook: "session-start-rules", env: map[string]string{"LOOPTRACK_LOOP_RULES_DIR": s.p("i18n"), "LOOPTRACK_LANG": "en"}, input: "{}"}
				},
				want: wantContext("SessionStart", []string{"English body X", "Key points", "訳の無い本文 Y"}, []string{"日本語の本文 X"})},
			{name: "kit に埋め込んだ rules も言語で切り替わる（Codex の要点）",
				mk: func(s *sandbox) call {
					return call{hook: "session-start-rules", agent: hookio.Codex,
						env: map[string]string{"CLAUDE_PROJECT_DIR": s.p("none"), "LOOPTRACK_LOOP_AGENT": "codex", "LOOPTRACK_LANG": "en"}, input: "{}"}
				},
				want: wantContext("SessionStart", []string{"Key points（background-process.md）", "Never write an unbounded"},
					[]string{"要点（background-process.md）", "無限の"})},
		}}.run(t)

	scenario{name: "置き場の優先順位・何も無いとき",
		setup: func(s *sandbox) {
			s.write(s.p("proj", ".claude", "rules", "looptrack-loop", "x.md"), "## 導入先\n<!-- looptrack:inject session -->\n導入先の本文\n")
			s.write(s.p("empty", "z.md"), "## 印なし\n本文\n")
		},
		steps: []step{
			{name: "<プロジェクト>/.claude/rules/looptrack-loop を kit より先に見る",
				mk: func(s *sandbox) call {
					return call{hook: "session-start-rules", env: map[string]string{"CLAUDE_PROJECT_DIR": s.p("proj")}, input: "{}"}
				},
				want: wantContext("SessionStart", []string{"導入先の本文"}, []string{"全作業共通の規律"})},
			{name: "印の付いた節が無ければ何も出さない",
				mk: func(s *sandbox) call {
					return call{hook: "session-start-rules", env: map[string]string{"LOOPTRACK_LOOP_RULES_DIR": s.p("empty")}, input: "{}"}
				},
				want: wantQuiet},
			{name: "ディレクトリが無ければ何も出さない",
				mk: func(s *sandbox) call {
					return call{hook: "session-start-rules", env: map[string]string{"LOOPTRACK_LOOP_RULES_DIR": s.p("no-such-dir")}, input: "{}"}
				},
				want: wantQuiet},
		}}.run(t)
}
