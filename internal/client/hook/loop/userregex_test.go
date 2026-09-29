package loop

// 利用者が環境変数に書いた正規表現（LOOPTRACK_LOOP_TASK_MODE_EXEC_RE・_INVEST_RE・LOOPTRACK_LOOP_RUNAWAY_ALLOW）が
// RE2 で読めないとき、黙って無視せずに変数名と誤りを systemMessage で知らせる（判定・検知は既定の語のまま続ける）。
// どの検査にも、同じ変数に読める正規表現を渡すと知らせが出ずに効く対照を置く。
// 文面を見るので、言語は呼び出しごとに LOOPTRACK_LANG で固定する。

import (
	"strings"
	"testing"
)

// 否定先読みは RE2 で読めない（以前の hook の正規表現で書けていた形）
const unreadableRE = `まとめて(?!(確認))`

func systemMessageOf(g got) string {
	msg, _ := g.json["systemMessage"].(string)
	return msg
}

// wantNotice は systemMessage に変数名と regexp の誤りの文面があること。
func wantNotice(name string) func(*testing.T, *sandbox, got) {
	return func(t *testing.T, _ *sandbox, g got) {
		t.Helper()
		msg := systemMessageOf(g)
		if !strings.Contains(msg, name) || !strings.Contains(msg, "error parsing regexp") {
			t.Errorf("systemMessage に変数名 %s と regexp の誤りが無い: %q%s", name, g.out.Stdout, g.why())
		}
		if !strings.Contains(msg, "(?!") {
			t.Errorf("誤りの箇所（(?!）が systemMessage に無い: %q", msg)
		}
	}
}

// wantMsgHas・wantMsgLacks は systemMessage が語を含む・含まないこと。
func wantMsgHas(w string) func(*testing.T, *sandbox, got) {
	return func(t *testing.T, _ *sandbox, g got) {
		t.Helper()
		if !strings.Contains(systemMessageOf(g), w) {
			t.Errorf("systemMessage に「%s」が無い: %q", w, g.out.Stdout)
		}
	}
}

func wantMsgLacks(w string) func(*testing.T, *sandbox, got) {
	return func(t *testing.T, _ *sandbox, g got) {
		t.Helper()
		if strings.Contains(systemMessageOf(g), w) {
			t.Errorf("systemMessage に「%s」があってはならない: %q", w, g.out.Stdout)
		}
	}
}

// wantNoNotice は知らせ（systemMessage）が出ないこと。
func wantNoNotice(t *testing.T, _ *sandbox, g got) {
	t.Helper()
	if msg := systemMessageOf(g); msg != "" {
		t.Errorf("知らせは出ないはず: %q%s", g.out.Stdout, g.why())
	}
}

func TestUserRegexNoticeTaskMode(t *testing.T) {
	say := func(prompt string, env ...string) func(*sandbox) call {
		return func(s *sandbox) call {
			m := map[string]string{"CLAUDE_PROJECT_DIR": s.p("proj"), "LOOPTRACK_LANG": "ja"}
			for i := 0; i+1 < len(env); i += 2 {
				m[env[i]] = env[i+1]
			}
			return call{hook: "user-prompt-task-mode", env: m, input: jsonInput(map[string]any{"prompt": prompt, "session_id": "R"})}
		}
	}
	const prompt = "論点を 1 枚にまとめて。"
	scenario{name: "読めない正規表現の知らせ（タスクモード）", setup: func(s *sandbox) { s.mkdir("proj", ".claude") }, steps: []step{
		// 対照: 読める正規表現は知らせが出ず、効く（実行モードになる）
		{name: "対照: 読める EXEC_RE は知らせず、効く", mk: say(prompt, "LOOPTRACK_LOOP_TASK_MODE_EXEC_RE", "まとめて"),
			want: all(wantKind("execute"), wantNoNotice)},
		// 読めない EXEC_RE: 知らせる。足した語は効かないので判定は既定のまま（何も決まらない）
		{name: "読めない EXEC_RE は変数名と誤りを知らせる", mk: say(prompt, "LOOPTRACK_LOOP_TASK_MODE_EXEC_RE", unreadableRE),
			want: all(wantNotice("LOOPTRACK_LOOP_TASK_MODE_EXEC_RE"), func(t *testing.T, _ *sandbox, g got) {
				if g.ctx() != "" {
					t.Errorf("読めない正規表現は効かないはず（既定の語で決まらない）: %q", g.out.Stdout)
				}
			})},
		{name: "読めない EXEC_RE でも既定の語の判定は続く（止めない）", mk: say("記憶を確認して", "LOOPTRACK_LOOP_TASK_MODE_EXEC_RE", unreadableRE),
			want: all(wantKind("investigate"), wantNotice("LOOPTRACK_LOOP_TASK_MODE_EXEC_RE"))},
		{name: "読めない EXEC_RE でも実行系の既定の語は効く", mk: say("A を実装して", "LOOPTRACK_LOOP_TASK_MODE_EXEC_RE", unreadableRE),
			want: all(wantKind("execute"), wantNotice("LOOPTRACK_LOOP_TASK_MODE_EXEC_RE"))},
		// INVEST_RE
		{name: "対照: 読める INVEST_RE は知らせず、効く", mk: say("仕様を精査しておいて", "LOOPTRACK_LOOP_TASK_MODE_INVEST_RE", "精査"),
			want: all(wantKind("investigate"), wantNoNotice)},
		{name: "読めない INVEST_RE は変数名と誤りを知らせる", mk: say("仕様を精査しておいて", "LOOPTRACK_LOOP_TASK_MODE_INVEST_RE", `精査(?!x)`),
			want: wantNotice("LOOPTRACK_LOOP_TASK_MODE_INVEST_RE")},
		// EXEC_RE が既定の語で決まる文でも、INVEST_RE の誤りは知らせる（判定に使われたかに関わらず読む）
		{name: "実行系の既定の語で決まる文でも、読めない INVEST_RE は知らせる", mk: say("A を実装して", "LOOPTRACK_LOOP_TASK_MODE_INVEST_RE", `精査(?!x)`),
			want: all(wantKind("execute"), wantNotice("LOOPTRACK_LOOP_TASK_MODE_INVEST_RE"))},
		// 言語（日英）
		{name: "英語の利用者には英語の文面", mk: say(prompt, "LOOPTRACK_LOOP_TASK_MODE_EXEC_RE", unreadableRE, "LOOPTRACK_LANG", "en"),
			want: func(t *testing.T, _ *sandbox, g got) {
				if msg := systemMessageOf(g); !strings.Contains(msg, "LOOPTRACK_LOOP_TASK_MODE_EXEC_RE") || !strings.Contains(msg, "Setting error") {
					t.Errorf("英語の知らせが無い: %q", g.out.Stdout)
				}
			}},
		{name: "日本語の利用者には日本語の文面", mk: say(prompt, "LOOPTRACK_LOOP_TASK_MODE_EXEC_RE", unreadableRE),
			want: func(t *testing.T, _ *sandbox, g got) {
				if msg := systemMessageOf(g); !strings.Contains(msg, "設定の誤り") || !strings.Contains(msg, "既定の語だけで判定を続けています") {
					t.Errorf("日本語の知らせが無い: %q", g.out.Stdout)
				}
			}},
	}}.run(t)
}

func TestUserRegexNoticeRunaway(t *testing.T) {
	const parent = "99999"
	snap := func(s string) string {
		return "/bin/zsh -c source /tmp/x/.claude/shell-snapshots/snapshot-zsh-1.sh 2>/dev/null || true && " + s
	}
	long := " 22222 " + parent + " 03:00:00 " + snap("eval 'tail -f logs/app.log'")
	mk := func(lines []string, env ...string) (func(*sandbox), func(*sandbox) call) {
		do := func(s *sandbox) {
			text := strings.Join(lines, "\n")
			if len(lines) > 0 {
				text += "\n"
			}
			s.write(s.p("ps.txt"), text)
		}
		c := func(s *sandbox) call {
			m := map[string]string{"LOOPTRACK_LOOP_RUNAWAY_PARENT_PID": parent, "LOOPTRACK_LOOP_RUNAWAY_PS_FILE": s.p("ps.txt"), "LOOPTRACK_LANG": "ja"}
			for i := 0; i+1 < len(env); i += 2 {
				m[env[i]] = env[i+1]
			}
			return call{hook: "stop-runaway-background-process", env: m, input: "{}"}
		}
		return do, c
	}
	st := func(name string, want func(*testing.T, *sandbox, got), lines []string, env ...string) step {
		do, c := mk(lines, env...)
		return step{name: name, do: do, mk: c, want: want}
	}
	scenario{name: "読めない正規表現の知らせ（終わらない子プロセス）", steps: []step{
		// 対照: 読める許可は知らせず、効く（許可されて何も止めない）
		st("対照: 読める RUNAWAY_ALLOW は知らせず、効く", all(wantAllow("allow"), wantNoNotice), []string{long},
			"LOOPTRACK_LOOP_RUNAWAY_ALLOW", "tail -f logs/"),
		// 読めない許可: 知らせる。許可は効かないので既定の検知が続く（差し戻す）
		st("読めない RUNAWAY_ALLOW は変数名と誤りを知らせ、検知は既定のまま続ける", all(wantBlock, wantNotice("LOOPTRACK_LOOP_RUNAWAY_ALLOW"),
			wantMsgHas("既定の許可だけで検知を続けています"), wantMsgLacks("既定の語だけで")),
			[]string{long}, "LOOPTRACK_LOOP_RUNAWAY_ALLOW", `tail -f (?!x)`),
		st("英語の利用者には、許可の文面（既定の exemptions）", wantMsgHas("only the default exemptions"),
			[]string{long}, "LOOPTRACK_LOOP_RUNAWAY_ALLOW", `tail -f (?!x)`, "LOOPTRACK_LANG", "en"),
		st("検知するものが無くても、読めない RUNAWAY_ALLOW は知らせる", all(wantNoBlock, wantNotice("LOOPTRACK_LOOP_RUNAWAY_ALLOW")),
			nil, "LOOPTRACK_LOOP_RUNAWAY_ALLOW", `tail -f (?!x)`),
		st("対照: 検知するものが無く許可が読めるなら、何も出さない", wantQuiet, nil, "LOOPTRACK_LOOP_RUNAWAY_ALLOW", "tail -f logs/"),
	}}.run(t)
}

// wantNoBlock は差し戻しの無いこと（知らせだけの出力を許す wantQuiet の代わり）。
func wantNoBlock(t *testing.T, _ *sandbox, g got) {
	t.Helper()
	if _, ok := g.json["decision"]; ok {
		t.Errorf("差し戻さないはず: %q", g.out.Stdout)
	}
}
