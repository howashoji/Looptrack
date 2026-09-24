package loop

// kit/loop/verify/verify-handoff.sh の移植（69 ケース）。使い捨ての git リポジトリで、1 段目（積む）と 2 段目（鮮度）を確かめる。

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/hookio"
)

const okResp = "[main 061e38aeb] 作業\n 1 file changed, 1 insertion(+)"

// markKind は post-work-complete-handoff-mark の結果（OUT / QUIET / INVALID）。
func markKind(g got) string {
	switch {
	case g.quiet():
		return "QUIET"
	case g.hookEventName() == "PostToolUse" && g.ctx() != "":
		return "OUT"
	}
	return "INVALID"
}

func wantMark(k string) func(*testing.T, *sandbox, got) {
	return func(t *testing.T, _ *sandbox, g got) {
		t.Helper()
		if markKind(g) != k {
			t.Errorf("期待 %s / 実際 %s: %s", k, markKind(g), g.out.Stdout)
		}
	}
}

// stopKind は stop-handoff-freshness の結果（PASS / BLOCK / OUTPUT）。
func stopKind(g got) string {
	switch {
	case g.quiet():
		return "PASS"
	case g.json["decision"] == "block":
		return "BLOCK"
	}
	return "OUTPUT"
}

func wantStop(k string) func(*testing.T, *sandbox, got) {
	return func(t *testing.T, _ *sandbox, g got) {
		t.Helper()
		if stopKind(g) != k {
			t.Errorf("期待 %s / 実際 %s: %s", k, stopKind(g), g.out.Stdout)
		}
	}
}

func wantExists(path func(*sandbox) string, want bool) func(*testing.T, *sandbox, got) {
	return func(t *testing.T, s *sandbox, _ got) {
		t.Helper()
		if s.exists(path(s)) != want {
			t.Errorf("%s の有無: 期待 %v", path(s), want)
		}
	}
}

// wantRecordLines は記録ファイル（handoff-pending.d/<セッション>）の中身を、時刻の欄を除いてそのまま突き合わせる。
func wantRecordLines(path func(*sandbox) string, want ...string) func(*testing.T, *sandbox, got) {
	return func(t *testing.T, s *sandbox, _ got) {
		t.Helper()
		var body []string
		for _, l := range strings.Split(s.read(path(s)), "\n") {
			if trimSpace(l) == "" {
				continue
			}
			_, rest, _ := strings.Cut(l, "\t")
			body = append(body, rest)
		}
		if strings.Join(body, " | ") != strings.Join(want, " | ") {
			t.Errorf("記録: 期待 %q / 実際 %q", want, body)
		}
	}
}

// wantNoRawVar は、展開されていない変数（$ を含む語）がそのまま記録に残っていないことを見る。
func wantNoRawVar(path func(*sandbox) string) func(*testing.T, *sandbox, got) {
	return func(t *testing.T, s *sandbox, _ got) {
		t.Helper()
		if text := s.read(path(s)); strings.Contains(text, "$") {
			t.Errorf("未展開の変数がそのまま記録された: %q", text)
		}
	}
}

func wantLines(path func(*sandbox) string, n int) func(*testing.T, *sandbox, got) {
	return func(t *testing.T, s *sandbox, _ got) {
		t.Helper()
		if c := strings.Count(s.read(path(s)), "\n"); c != n {
			t.Errorf("%s の行数: 期待 %d / 実際 %d", path(s), n, c)
		}
	}
}

func TestHandoff(t *testing.T) {
	needGit(t)
	R := func(s *sandbox, parts ...string) string { return s.p(append([]string{"repo"}, parts...)...) }
	H := func(s *sandbox) string { return R(s, ".claude", "memories", "handoff.md") }
	pend := func(sid string) func(*sandbox) string {
		return func(s *sandbox) string { return R(s, ".claude", "handoff-pending.d", sid) }
	}
	setup := func(s *sandbox) {
		s.git("init", "-q", "-b", "main", R(s))
		s.mkdir("repo", ".claude", "memories")
		s.write(R(s, "src", "a.txt"), "a\n")
		s.git("-C", R(s), "add", "src/a.txt")
		s.git("-C", R(s), "commit", "-qm", "base")
	}
	// pj は PostToolUse の入力（Bash ならコマンド、それ以外は JSON の tool_input）
	pj := func(tool, sid, arg, resp string) string {
		var ti any
		if tool == "Bash" {
			ti = map[string]any{"command": arg}
		} else {
			_ = json.Unmarshal([]byte(arg), &ti)
		}
		d := map[string]any{"tool_name": tool, "tool_input": ti, "tool_response": resp}
		if sid != "" {
			d["session_id"] = sid
		}
		return jsonInput(d)
	}
	mark := func(tool, sid, arg, resp string) func(*sandbox) call {
		return func(s *sandbox) call {
			return call{hook: "post-work-complete-handoff-mark", env: map[string]string{"CLAUDE_PROJECT_DIR": R(s)}, input: pj(tool, sid, arg, resp)}
		}
	}
	clearPending := func(s *sandbox) {
		os.RemoveAll(R(s, ".claude", "handoff-pending.d"))
		os.RemoveAll(R(s, ".claude", "handoff-pending"))
	}
	stop := func(sid string, active bool, env ...string) func(*sandbox) call {
		return func(s *sandbox) call {
			m := map[string]string{"CLAUDE_PROJECT_DIR": R(s)}
			for i := 0; i+1 < len(env); i += 2 {
				m[env[i]] = env[i+1]
			}
			return call{hook: "stop-handoff-freshness", env: m, input: jsonInput(map[string]any{"session_id": sid, "stop_hook_active": active})}
		}
	}
	putMarker := func(sid string) func(*sandbox) {
		return func(s *sandbox) { s.write(pend(sid)(s), "2026-09-18 10:00:00\tイシュー Done: TST-0009\n") }
	}
	// writeWithMark は「そのセッションが引き継ぎを書いた」ことを、本文の印（handoff.go の writerMark）で表す。
	// 既定（LOOPTRACK_LOOP_HANDOFF_WRITER=session）では、更新時刻だけでは「誰が書いたか」が分からないので畳まない。
	writeWithMark := func(p func(*sandbox) string, sid string) func(*sandbox) {
		return func(s *sandbox) { s.write(p(s), "x\n"+writerMark(sid)+"\n") }
	}
	older := func(p func(*sandbox) string) func(*sandbox) { return func(s *sandbox) { s.touch(p(s), oldTime) } }
	newer := func(p func(*sandbox) string) func(*sandbox) { return func(s *sandbox) { s.touch(p(s), newTime) } }
	seq := func(fs ...func(*sandbox)) func(*sandbox) {
		return func(s *sandbox) {
			for _, f := range fs {
				f(s)
			}
		}
	}
	commit := func(msg string, files ...string) func(*sandbox) {
		return func(s *sandbox) {
			s.git(append([]string{"-C", R(s), "add"}, files...)...)
			s.git("-C", R(s), "commit", "-qm", msg)
		}
	}
	appendTo := func(p func(*sandbox) string, text string) func(*sandbox) {
		return func(s *sandbox) {
			f, err := os.OpenFile(p(s), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				s.t.Fatal(err)
			}
			f.WriteString(text)
			f.Close()
		}
	}
	srcA := func(s *sandbox) string { return R(s, "src", "a.txt") }

	scenario{name: "1 段目: 完了イベントを積む", setup: setup, steps: []step{
		{name: "コミットの成功で引き継ぎを促す・マーカーに 1 行積む", mk: mark("Bash", "s1", "git commit -m x", okResp),
			want: all(wantMark("OUT"), wantLines(pend("s1"), 1))},
		{name: "コミット以外は素通り", mk: mark("Bash", "s1", "git status --short", "M a.txt"), want: wantMark("QUIET")},
		{name: "失敗（nothing to commit）", mk: mark("Bash", "s1", "git commit -m x", "nothing to commit, working tree clean"), want: wantMark("QUIET")},
		{name: "--dry-run は対象外", mk: mark("Bash", "s1", "git commit --dry-run -m x", okResp), want: wantMark("QUIET")},
		{name: "複合コマンドでも検知する", mk: mark("Bash", "s1", "git add src/a.txt && git commit -m x", okResp), want: wantMark("OUT")},
		{name: "git -C <dir> commit も検知する", mk: mark("Bash", "s1", "git -C /tmp/x commit -m y", okResp), want: wantMark("OUT")},
		{name: "出力が無いとき（判定できない）は積む", mk: mark("Bash", "s1", "git commit -m x", ""), want: wantMark("OUT")},
		{name: "引用符の中の git commit（メッセージ）は拾わない", mk: mark("Bash", "s1", `echo "次は git commit する"`, "x"), want: wantMark("QUIET")},
		{name: "ヒアドキュメントの本文は拾わない", mk: mark("Bash", "s1", "cat <<'EOF' > n.txt\ngit commit -m x\nEOF", "x"), want: wantMark("QUIET")},
		{name: "grep の引数は拾わない", mk: mark("Bash", "s1", "grep -n 'git commit' a.sh", "x"), want: wantMark("QUIET")},
		{name: "CLI の close", mk: mark("Bash", "s1", "looptrack issue close TST-0001 --comment ok", "x"), want: wantMark("OUT")},
		{name: `CLI の status <ID> "Done"`, mk: mark("Bash", "s1", `looptrack issue status TST-0002 "Done"`, "x"), want: wantMark("OUT")},
		{name: "CLI の status <ID> Canceled", mk: mark("Bash", "s1", "looptrack issue status TST-0003 Canceled", "x"), want: wantMark("OUT")},
		{name: `CLI の status <ID> "In Progress" は対象外`, mk: mark("Bash", "s1", `looptrack issue status TST-0003 "In Progress"`, "x"), want: wantMark("QUIET")},
		{name: "CLI の comment の本文の close は拾わない", mk: mark("Bash", "s1", `looptrack issue comment TST-0003 "looptrack issue close は後で"`, "x"), want: wantMark("QUIET")},
		{name: "CLI の close --help は完了ではない（実際に起きた例）", mk: mark("Bash", "s1", "looptrack issue close --help", "usage: looptrack issue close ..."), want: wantMark("QUIET")},
		{name: "CLI の close <ID> -h は完了ではない", mk: mark("Bash", "s1", "looptrack issue close TST-0001 -h", "usage: looptrack issue close ..."), want: wantMark("QUIET")},
		{name: "CLI の status --help は完了ではない", mk: mark("Bash", "s1", "looptrack issue status --help", "usage: looptrack issue status ..."), want: wantMark("QUIET")},
		{name: "CLI の status <ID> Done --help は完了ではない", mk: mark("Bash", "s1", "looptrack issue status TST-0002 Done --help", "usage: looptrack issue status ..."), want: wantMark("QUIET")},
		{name: "CLI の --help close は完了ではない", mk: mark("Bash", "s1", "looptrack issue --help close", "usage: looptrack issue ..."), want: wantMark("QUIET")},
		{name: "--help の後の別コマンドの close は積む", mk: mark("Bash", "s1", "looptrack issue close --help; looptrack issue close TST-0001", "x"), want: wantMark("OUT")},
		{name: "コメント本文に --help を含む close は積む", mk: mark("Bash", "s1", `looptrack issue close TST-0001 --comment "--help の扱いを直した"`, "x"), want: wantMark("OUT")},
		{name: "MCP の set_status Done", mk: mark("mcp__im__set_status", "s1", `{"id":"TST-0004","status":"Done"}`, ""), want: wantMark("OUT")},
		{name: "MCP の set_status In Review は対象外", mk: mark("mcp__im__set_status", "s1", `{"id":"TST-0004","status":"In Review"}`, ""), want: wantMark("QUIET")},
		{name: "他の MCP は対象外", mk: mark("mcp__im__add_comment", "s1", `{"id":"TST-0004","body":"Done"}`, ""), want: wantMark("QUIET")},
		{name: "引き継ぎのファイルだけのコミットは積まない",
			do: seq(clearPending, appendTo(H, "b\n"), commit("handoff", ".claude/memories/handoff.md")),
			mk: mark("Bash", "s2", "git commit -m handoff", okResp), want: wantMark("QUIET")},
		{name: "引き継ぎ + 作業のコミットは積む",
			do: seq(appendTo(srcA, "c\n"), appendTo(H, "c\n"), commit("both", "src/a.txt", ".claude/memories/handoff.md")),
			mk: mark("Bash", "s2", "git commit -m both", okResp), want: wantMark("OUT")},
		{name: "session_id が無いときは共有のマーカーに積む", mk: mark("Bash", "", "git commit -m x", okResp),
			want: all(wantMark("OUT"), wantLines(func(s *sandbox) string { return R(s, ".claude", "handoff-pending") }, 1))},
	}}.run(t)

	scenario{name: "1 段目: looptrack issue（bash 版も同じに数える）", setup: setup,
		steps: []step{
			{name: "looptrack issue close --help は完了ではない", mk: mark("Bash", "s1", "looptrack issue close --help", "usage: looptrack issue close ..."), want: wantMark("QUIET")},
			{name: "looptrack issue close", mk: mark("Bash", "s1", "looptrack issue close TST-0001 --comment ok", "x"), want: wantMark("OUT")},
			{name: "looptrack issue status Done", do: clearPending, mk: mark("Bash", "s1", `LOOPTRACK_PROJECT=tst looptrack issue status TST-0002 Done --comment ok`, "x"), want: wantMark("OUT")},
			{name: "絶対パスの looptrack issue status Canceled", do: clearPending, mk: mark("Bash", "s1", `/usr/local/bin/looptrack issue status TST-0003 Canceled`, "x"), want: wantMark("OUT")},
			{name: "looptrack issue status In Progress は積まない", do: clearPending, mk: mark("Bash", "s1", `looptrack issue status TST-0004 "In Progress"`, "x"), want: wantMark("QUIET")},
		}}.run(t)

	scenario{name: "2 段目: 鮮度（file バックエンド・既定）", setup: func(s *sandbox) { setup(s); s.write(H(s), "x\n") }, steps: []step{
		{name: "マーカーが無ければ通す", mk: stop("f0", false), want: wantStop("PASS")},
		{name: "引き継ぎがマーカーより古い・差し戻してもマーカーは残す", do: seq(putMarker("f1"), older(H)), mk: stop("f1", false),
			want: all(wantStop("BLOCK"), wantExists(pend("f1"), true))},
		{name: "stop_hook_active は通す（無限ループ防止）", mk: stop("f1", true), want: wantStop("PASS")},
		{name: "引き継ぎを更新したら通す・マーカーを畳む", do: seq(writeWithMark(H, "f1"), newer(H)), mk: stop("f1", false),
			want: all(wantStop("PASS"), wantExists(pend("f1"), false))},
		{name: "引き継ぎが無い", do: seq(putMarker("f2"), func(s *sandbox) { os.Rename(H(s), H(s)+".bak") }), mk: stop("f2", false), want: wantStop("BLOCK")},
		{name: "別セッションのマーカーでは止めない", do: seq(func(s *sandbox) { os.Rename(H(s)+".bak", H(s)) }, putMarker("f3"), older(H)),
			mk: stop("f4", false), want: wantStop("PASS")},
		{name: "差し戻しの文に完了したイベントとマーカーのパスが出る",
			mk: func(s *sandbox) call {
				return call{hook: "stop-handoff-freshness", env: map[string]string{"CLAUDE_PROJECT_DIR": R(s)}, input: `{"session_id":"f3"}`}
			},
			want: func(t *testing.T, _ *sandbox, g got) {
				if !strings.Contains(g.res.Block, "TST-0009") || !strings.Contains(g.res.Block, ".claude/handoff-pending.d/f3") {
					t.Errorf("理由: %s", g.res.Block)
				}
			}},
		{name: "LOOPTRACK_LOOP_HANDOFF_FILE で引き継ぎのファイルを変えられる",
			do: func(s *sandbox) {
				s.write(s.p("alt-handoff.md"), writerMark("f3")+"\n")
				s.touch(s.p("alt-handoff.md"), newTime)
			},
			mk: func(s *sandbox) call {
				return call{hook: "stop-handoff-freshness", env: map[string]string{"CLAUDE_PROJECT_DIR": R(s), "LOOPTRACK_LOOP_HANDOFF_FILE": s.p("alt-handoff.md")},
					input: `{"session_id":"f3"}`}
			},
			want: wantQuiet},
		{name: "LOOPTRACK_LOOP_STATE_DIR のマーカーを見る（無ければ通す）", do: seq(putMarker("f5"), older(H)),
			mk: func(s *sandbox) call {
				return call{hook: "stop-handoff-freshness", env: map[string]string{"CLAUDE_PROJECT_DIR": R(s), "LOOPTRACK_LOOP_STATE_DIR": s.p("nostate")},
					input: `{"session_id":"f5"}`}
			},
			want: wantQuiet},
	}}.run(t)

	memMD := func(s *sandbox) string { return s.p("mem", "MEMORY.md") }
	autoStop := func(sid string) func(*sandbox) call {
		return func(s *sandbox) call {
			return call{hook: "stop-handoff-freshness", env: map[string]string{"CLAUDE_PROJECT_DIR": R(s), "LOOPTRACK_LOOP_HANDOFF_BACKEND": "auto-memory",
				"LOOPTRACK_LOOP_HANDOFF_MEMORY_DIR": s.p("mem")}, input: `{"session_id":"` + sid + `"}`}
		}
	}
	scenario{name: "2 段目: auto-memory バックエンド", setup: func(s *sandbox) { setup(s); s.write(memMD(s), "") }, steps: []step{
		{name: "MEMORY.md が古ければ差し戻す", do: seq(putMarker("a1"), older(memMD)), mk: autoStop("a1"), want: wantStop("BLOCK")},
		{name: "MEMORY.md を更新したら通してマーカーを畳む", do: newer(memMD), mk: autoStop("a1"),
			want: all(wantQuiet, wantExists(pend("a1"), false))},
		{name: "既定の auto-memory の場所をプロジェクトのパスから組み立てる",
			do: func(s *sandbox) {
				enc := regexp.MustCompile(`[^A-Za-z0-9]`).ReplaceAllString(R(s), "-")
				p := s.p("home", ".claude", "projects", enc, "memory", "MEMORY.md")
				s.write(p, "")
				s.touch(p, newTime)
				putMarker("a2")(s)
			},
			mk: func(s *sandbox) call {
				return call{hook: "stop-handoff-freshness", env: map[string]string{"HOME": s.p("home"), "CLAUDE_PROJECT_DIR": R(s),
					"LOOPTRACK_LOOP_HANDOFF_BACKEND": "auto-memory"}, input: `{"session_id":"a2"}`}
			},
			want: wantQuiet},
	}}.run(t)

	cmdStop := func(sid, cmd string, env ...string) func(*sandbox) call {
		return func(s *sandbox) call {
			m := map[string]string{"CLAUDE_PROJECT_DIR": R(s), "LOOPTRACK_LOOP_HANDOFF_BACKEND": "command", "LOOPTRACK_LOOP_HANDOFF_CHECK_CMD": cmd}
			for i := 0; i+1 < len(env); i += 2 {
				m[env[i]] = env[i+1]
			}
			return call{hook: "stop-handoff-freshness", env: m, input: `{"session_id":"` + sid + `"}`}
		}
	}
	ex := `grep -qF "master=$(git rev-parse --short=9 main)" "$HANDOFF_TEXT" && exit 0 || exit 1`
	sha := func(s *sandbox, rev string) string { return s.git("-C", R(s), "rev-parse", "--short=9", rev) }
	scenario{name: "2 段目: command バックエンド",
		setup: func(s *sandbox) { setup(s); s.write(srcA(s), "b\n"); commit("second", "src/a.txt")(s) },
		steps: []step{
			{name: "判定コマンドが 1（古い）なら差し戻す", do: putMarker("c1"), mk: cmdStop("c1", "exit 1"), want: wantStop("BLOCK")},
			{name: "判定コマンドが 3（判定できない）なら通す", mk: cmdStop("c1", "exit 3"), want: wantQuiet},
			{name: "マーカーのパスを LOOPTRACK_LOOP_HANDOFF_PENDING で渡す・0 なら通してマーカーを畳む",
				mk:   cmdStop("c1", `[ -f "$LOOPTRACK_LOOP_HANDOFF_PENDING" ] && exit 0 || exit 1`),
				want: all(wantQuiet, wantExists(pend("c1"), false))},
			{name: "判定コマンドが空なら通す", do: putMarker("c2"), mk: cmdStop("c2", ""), want: wantQuiet},
			{name: "例: 引き継ぎが 1 つ前の sha を指す → 差し戻す",
				do: func(s *sandbox) { s.write(s.p("ho.txt"), "作業完了。master="+sha(s, "HEAD~1")+"\n") },
				mk: func(s *sandbox) call { return cmdStop("c2", ex, "HANDOFF_TEXT", s.p("ho.txt"))(s) }, want: wantStop("BLOCK")},
			{name: "例: 引き継ぎが到達点の sha を指す → 通す",
				do: func(s *sandbox) { s.write(s.p("ho.txt"), "作業完了。master="+sha(s, "HEAD")+"・検証済\n") },
				mk: func(s *sandbox) call { return cmdStop("c2", ex, "HANDOFF_TEXT", s.p("ho.txt"))(s) }, want: wantQuiet},
			{name: "知らないバックエンドは判定しない（通す）", do: putMarker("u1"),
				mk: func(s *sandbox) call {
					return call{hook: "stop-handoff-freshness", env: map[string]string{"CLAUDE_PROJECT_DIR": R(s), "LOOPTRACK_LOOP_HANDOFF_BACKEND": "no-such-backend"},
						input: `{"session_id":"u1"}`}
				},
				want: wantQuiet},
		}}.run(t)

	codexPend := func(sid string) func(*sandbox) string {
		return func(s *sandbox) string { return R(s, ".codex", "handoff-pending.d", sid) }
	}
	scenario{name: "LOOPTRACK_LOOP_NO_BLOCK=1 と Codex の状態の置き場", setup: func(s *sandbox) { setup(s); s.write(H(s), "x\n") }, steps: []step{
		{name: "LOOPTRACK_LOOP_NO_BLOCK=1 は systemMessage で知らせ、decision を出さない・マーカーは残す",
			do: seq(putMarker("n1"), older(H)), mk: stop("n1", false, "LOOPTRACK_LOOP_NO_BLOCK", "1"),
			want: all(wantSystemMessage("TST-0009"), wantExists(pend("n1"), true))},
		{name: "Codex（CLAUDE_PROJECT_DIR 無し）は git のルートの .codex/ に積む", do: clearPending,
			mk: func(s *sandbox) call {
				return call{hook: "post-work-complete-handoff-mark", agent: hookio.Codex, env: map[string]string{"CODEX_THREAD_ID": "cx1"},
					cwd: R(s), input: pj("Bash", "cx1", "git commit -m x", okResp)}
			},
			want: wantExists(codexPend("cx1"), true)},
		{name: "Codex の Stop はサブディレクトリからでも .codex/ のマーカーを見る", do: older(H),
			mk: func(s *sandbox) call {
				return call{hook: "stop-handoff-freshness", agent: hookio.Codex, env: map[string]string{"CODEX_THREAD_ID": "cx1", "LOOPTRACK_LOOP_NO_BLOCK": "1"},
					cwd: R(s, "src"), input: `{"session_id":"cx1"}`}
			},
			want: wantSystemMessage("")},
	}}.run(t)

	cpPost := func(s *sandbox) string {
		return `{"hook_event_name":"PostToolUse","session_id":"cp1","timestamp":"2026-09-18T12:00:00.000Z","cwd":` + jstr(R(s)) +
			`,"tool_name":"Bash","tool_input":{"command":"git commit -m x"},"tool_result":{"result_type":"success","text_result_for_llm":"[main abc1234] x"}}`
	}
	cp := map[string]string{"LOOPTRACK_LOOP_AGENT": "copilot"}
	cpTool := func(tool, sid, ti string) func(*sandbox) call {
		return func(s *sandbox) call {
			return call{hook: "post-work-complete-handoff-mark", agent: hookio.Copilot, env: cp, cwd: R(s),
				input: `{"hook_event_name":"PostToolUse","session_id":"` + sid + `","tool_name":"` + tool + `","tool_input":` + ti +
					`,"tool_result":{"result_type":"success","text_result_for_llm":"[main abc1234] x"}}`}
		}
	}
	cpStop := func(sid string) func(*sandbox) call {
		return func(s *sandbox) call {
			return call{hook: "stop-handoff-freshness", agent: hookio.Copilot, env: cp, cwd: R(s, "src"),
				input: `{"hook_event_name":"Stop","session_id":"` + sid + `","stop_hook_active":false}`}
		}
	}
	scenario{name: "GitHub Copilot の入力の形", setup: func(s *sandbox) { setup(s); s.write(H(s), "x\n") }, steps: []step{
		{name: "Copilot（CLAUDE_PROJECT_DIR 無し）は git のルートの .claude/ に積む",
			mk: func(s *sandbox) call {
				return call{hook: "post-work-complete-handoff-mark", agent: hookio.Copilot, env: cp, cwd: R(s), input: cpPost(s)}
			},
			want: wantExists(pend("cp1"), true)},
		{name: "Copilot の Stop は知らせるだけ（systemMessage・decision を出さない）", do: older(H),
			mk: func(s *sandbox) call {
				return call{hook: "stop-handoff-freshness", agent: hookio.Copilot, env: map[string]string{"LOOPTRACK_LOOP_AGENT": "copilot", "LOOPTRACK_LOOP_NO_BLOCK": "1"}, cwd: R(s, "src"),
					input: `{"hook_event_name":"Stop","session_id":"cp1","timestamp":"2026-09-18T12:00:05.000Z","cwd":` + jstr(R(s, "src")) + `,"stop_hook_active":false}`}
			},
			want: wantSystemMessage("")},
		{name: "Copilot の PostToolUse は additionalContext をトップレベルと hookSpecificOutput の両方に置く", do: clearPending,
			mk: func(s *sandbox) call {
				return call{hook: "post-work-complete-handoff-mark", agent: hookio.Copilot, env: cp, cwd: R(s), input: cpPost(s)}
			},
			want: func(t *testing.T, _ *sandbox, g got) {
				h, _ := g.json["hookSpecificOutput"].(map[string]any)
				if top, _ := g.json["additionalContext"].(string); top == "" || top != h["additionalContext"] || h["hookEventName"] != "PostToolUse" {
					t.Errorf("出力の形: %s", g.out.Stdout)
				}
			}},
		{name: "Claude Code の出力は変えない（トップレベルに additionalContext を足さない）",
			mk: mark("Bash", "t9", "git commit -m x", okResp),
			want: func(t *testing.T, _ *sandbox, g got) {
				if _, ok := g.json["hookSpecificOutput"]; !ok || len(g.json) != 1 {
					t.Errorf("出力の形: %s", g.out.Stdout)
				}
			}},
		{name: "Copilot で差し戻すときは decision をトップレベル（CLI）と hookSpecificOutput（VS Code）の両方に置く",
			do: seq(clearPending, putMarker("cp1"), older(H)),
			mk: cpStop("cp1"),
			want: func(t *testing.T, _ *sandbox, g got) {
				h, _ := g.json["hookSpecificOutput"].(map[string]any)
				if g.json["decision"] != "block" || h["decision"] != "block" || h["hookEventName"] != "Stop" || h["reason"] != g.json["reason"] {
					t.Errorf("出力の形: %s", g.out.Stdout)
				}
				// 理由は次の利用者のメッセージとして渡るので、task-mode・scope が見分ける印を先頭に付ける
				if r, _ := g.json["reason"].(string); !strings.HasPrefix(r, hookio.CopilotStopMarker+" 引き継ぎが作業の完了に追いついていません") {
					t.Errorf("理由の先頭に印が無い: %s", g.out.Stdout)
				}
			}},
		// 同じ積み残しでは 1 回だけ差し戻す（Copilot は差し戻しの理由を利用者のメッセージとして渡して続けるため）
		{name: "Copilot: 同じ積み残しでは 2 回目は差し戻さない", mk: cpStop("cp1"),
			want: all(wantStop("PASS"), wantExists(func(s *sandbox) string { return R(s, ".claude", "handoff-blocked.d", "cp1") }, true))},
		{name: "Copilot: 新しい完了が積まれたら再び差し戻す", do: appendTo(pend("cp1"), "2026-09-18 10:05:00\tイシュー close: TST-0010\n"),
			mk: cpStop("cp1"), want: wantStop("BLOCK")},
		{name: "Copilot: 積み残しを消して作り直しても（中身が違えば）差し戻す", do: seq(clearPending, putMarker("cp1")),
			mk: cpStop("cp1"), want: wantStop("BLOCK")},
		{name: "Copilot: 引き継ぎを更新したら通し、差し戻しの記録も消す", do: seq(writeWithMark(H, "cp1"), newer(H)), mk: cpStop("cp1"),
			want: all(wantStop("PASS"), wantExists(pend("cp1"), false),
				wantExists(func(s *sandbox) string { return R(s, ".claude", "handoff-blocked.d", "cp1") }, false))},
		{name: "Claude Code は同じ積み残しでも毎回差し戻す（従来どおり）", do: seq(putMarker("cc1"), older(H)), mk: stop("cc1", false), want: wantStop("BLOCK")},
		{name: "Claude Code の 2 回目も差し戻す", mk: stop("cc1", false), want: wantStop("BLOCK")},
		{name: "Copilot CLI のシェル（bash）の git commit を積む", do: clearPending,
			mk: cpTool("bash", "cpa", `{"command":"git commit -m x"}`), want: wantExists(pend("cpa"), true)},
		{name: "VS Code のシェル（run_in_terminal）の git commit を積む",
			mk: cpTool("run_in_terminal", "cpb", `{"command":"git commit -m x","explanation":"commit","isBackground":false}`), want: wantExists(pend("cpb"), true)},
		{name: "Copilot CLI の MCP（im-set_status）の Done を積む",
			mk: cpTool("im-set_status", "cpc", `{"id":"TST-0001","status":"Done"}`), want: wantExists(pend("cpc"), true)},
		{name: "VS Code の MCP（mcp_im_set_status）の Canceled を積む",
			mk: cpTool("mcp_im_set_status", "cpd", `{"id":"TST-0001","status":"Canceled"}`), want: wantExists(pend("cpd"), true)},
		{name: "ほかのツール（matcher が効かず呼ばれた read_file）は積まない",
			mk: cpTool("read_file", "cpe", `{"filePath":"/x","command":"git commit -m x"}`), want: wantExists(pend("cpe"), false)},
		{name: "MCP の set_status でも Done / Canceled 以外は積まない",
			mk: cpTool("im-set_status", "cpf", `{"id":"TST-0001","status":"In Progress"}`), want: wantExists(pend("cpf"), false)},
	}}.run(t)

	scenario{name: "1 段目 → 2 段目の通し（引き継ぎだけのコミットでガードが回り続けた不具合の回帰）",
		setup: func(s *sandbox) {
			setup(s)
			s.write(H(s), "x\n")
			appendTo(srcA, "c\n")(s)
			commit("both", "src/a.txt", ".claude/memories/handoff.md")(s)
		},
		steps: []step{
			{do: older(H), mk: mark("Bash", "t1", "git commit -m both", okResp)},
			{name: "作業のコミットの後、引き継ぎが古ければ差し戻す", mk: stop("t1", false), want: wantStop("BLOCK")},
			{do: seq(func(*sandbox) { time.Sleep(1100 * time.Millisecond) }, appendTo(H, "d\n"+writerMark("t1")+"\n"), commit("handoff2", ".claude/memories/handoff.md")),
				mk: mark("Bash", "t1", "git commit -m handoff2", okResp)},
			{name: "引き継ぎを更新してコミットしても、ガードが回り続けない", mk: stop("t1", false), want: wantStop("PASS")},
		}}.run(t)
}

// TestHandoffFreshnessWriterMark は「誰が書いたか」で鮮度を判定することの回帰。
//
// 本体の作業ツリーの引き継ぎ 1 本を全セッションが共有するので、更新時刻だけで見ると
// **ほかのセッションの書き込みで自分のマーカーが畳まれ**、自分の引き継ぎは黙って書かれないまま終わる
// （実測で再現: セッション B が 1 文字も書いていないのに Stop が通り、B のマーカーが消えた）。
// handoff.go の判定を元の `fresh = !bestMT.Before(pendMT)` に戻すと、最初のケースが red になる。
func TestHandoffFreshnessWriterMark(t *testing.T) {
	needGit(t)
	R := func(s *sandbox, parts ...string) string { return s.p(append([]string{"repo"}, parts...)...) }
	H := func(s *sandbox) string { return R(s, ".claude", "memories", "handoff.md") }
	other := func(s *sandbox) string { return R(s, ".claude", "memories", "handoff-38-supervisor.md") }
	pend := func(sid string) func(*sandbox) string {
		return func(s *sandbox) string { return R(s, ".claude", "handoff-pending.d", sid) }
	}
	// 印を使い始めたプロジェクト（A が印つきで書いてある）。この形でだけ、書き手で厳密に判定する
	setup := func(s *sandbox) {
		s.git("init", "-q", "-b", "main", R(s))
		s.mkdir("repo", ".claude", "memories")
		s.write(H(s), "# 引き継ぎ\n\nセッション A の申し送り\n"+writerMark("A")+"\n")
		s.touch(H(s), oldTime)
		s.write(pend("B")(s), "2026-09-21 02:00:00\tcommit\n")
		s.touch(pend("B")(s), newTime)
	}
	stop := func(sid string, env ...string) func(*sandbox) call {
		return func(s *sandbox) call {
			m := map[string]string{"CLAUDE_PROJECT_DIR": R(s)}
			for i := 0; i+1 < len(env); i += 2 {
				m[env[i]] = env[i+1]
			}
			return call{hook: "stop-handoff-freshness", env: m, input: jsonInput(map[string]any{"session_id": sid})}
		}
	}
	// A の更新（B は 1 文字も書いていない）。印は A のものだけ
	writtenByOther := func(s *sandbox) {
		s.write(H(s), "# 引き継ぎ\n\nセッション A の申し送り（更新）\n"+writerMark("A")+"\n")
		s.touch(H(s), newTime.Add(time.Minute))
	}
	scenario{name: "ほかのセッションの書き込みでは畳まない", setup: setup, steps: []step{
		{name: "A が更新しただけでは B のマーカーは畳まれない（差し戻す）",
			do: writtenByOther, mk: stop("B"),
			want: all(wantStop("BLOCK"), wantExists(pend("B"), true))},
		{name: "差し戻しの文に、本文へ入れる印の行がそのまま出る", mk: stop("B"),
			want: func(t *testing.T, _ *sandbox, g got) {
				t.Helper()
				if !strings.Contains(g.res.Block, writerMark("B")) {
					t.Errorf("印の行が無い: %s", g.res.Block)
				}
			}},
		{name: "印はあるが完了より古い更新では畳まない",
			do: func(s *sandbox) {
				s.write(H(s), "# 引き継ぎ\n\n"+writerMark("B")+"\n")
				s.touch(H(s), oldTime)
			},
			mk: stop("B"), want: all(wantStop("BLOCK"), wantExists(pend("B"), true))},
		{name: "B が印つきで書けば通してマーカーを畳む",
			do: func(s *sandbox) {
				s.write(H(s), "# 引き継ぎ\n\n## セッション B の申し送り\n"+writerMark("B")+"\n")
				s.touch(H(s), newTime.Add(time.Minute))
			},
			mk: stop("B"), want: all(wantStop("PASS"), wantExists(pend("B"), false))},
	}}.run(t)

	scenario{name: "同じディレクトリの別のファイルに書いても更新済みと見る", setup: setup, steps: []step{
		{name: "別ファイルに印つきで書いたら通す（差し戻しの逆の誤判定）",
			do: func(s *sandbox) {
				writtenByOther(s)
				s.write(other(s), "# 監督の申し送り\n"+writerMark("B")+"\n")
				s.touch(other(s), newTime.Add(time.Minute))
			},
			mk: stop("B"), want: all(wantStop("PASS"), wantExists(pend("B"), false))},
	}}.run(t)

	scenario{name: "LOOPTRACK_LOOP_HANDOFF_WRITER=mtime は以前の挙動", setup: setup, steps: []step{
		{name: "更新時刻だけで畳む（印を見ない）", do: writtenByOther,
			mk:   stop("B", "LOOPTRACK_LOOP_HANDOFF_WRITER", "mtime"),
			want: all(wantStop("PASS"), wantExists(pend("B"), false))},
	}}.run(t)

	scenario{name: "1 段目（完了の検知）が印の行を渡す", setup: setup, steps: []step{
		{name: "作業完了の知らせに、本文へ入れる印の行が出る",
			mk: func(s *sandbox) call {
				return call{hook: "post-work-complete-handoff-mark", env: map[string]string{"CLAUDE_PROJECT_DIR": R(s)},
					input: jsonInput(map[string]any{"session_id": "B", "tool_name": "Bash",
						"tool_input": map[string]any{"command": "git commit -m x"}, "tool_response": okResp})}
			},
			want: func(t *testing.T, _ *sandbox, g got) {
				t.Helper()
				if !strings.Contains(g.ctx(), writerMark("B")) {
					t.Errorf("印の行が無い: %s%s", g.ctx(), g.why())
				}
			}},
	}}.run(t)
}

// TestHandoffFreshnessWriterMarkCompat は「印を知らない既存のプロジェクトを差し戻さない」ことの回帰。
//
// 引き継ぎに印が 1 つも無いファイル（＝印を知らない既存のプロジェクト）では、従来どおり更新時刻で判定する。
// 印が 1 つでも入っているファイル（＝印を使い始めたプロジェクト）だけが、書き手で厳密になる。
// handoff.go の分岐から handoffHasAnyWriterMark の条件を外すと、1 つ目のケースが red になる
// （配布物を更新しただけの既存のプロジェクトが、普通に更新しても差し戻される）。
func TestHandoffFreshnessWriterMarkCompat(t *testing.T) {
	needGit(t)
	R := func(s *sandbox, parts ...string) string { return s.p(append([]string{"repo"}, parts...)...) }
	H := func(s *sandbox) string { return R(s, ".claude", "memories", "handoff.md") }
	pend := func(sid string) func(*sandbox) string {
		return func(s *sandbox) string { return R(s, ".claude", "handoff-pending.d", sid) }
	}
	stop := func(sid string) func(*sandbox) call {
		return func(s *sandbox) call {
			return call{hook: "stop-handoff-freshness", env: map[string]string{"CLAUDE_PROJECT_DIR": R(s)},
				input: jsonInput(map[string]any{"session_id": sid})}
		}
	}
	// 印を 1 つも知らない引き継ぎ（既存のプロジェクトの姿）
	setupPlain := func(s *sandbox) {
		s.git("init", "-q", "-b", "main", R(s))
		s.mkdir("repo", ".claude", "memories")
		s.write(H(s), "# 引き継ぎ\n\n## セッション A の申し送り\n")
		s.touch(H(s), oldTime)
		s.write(pend("B")(s), "2026-09-21 02:00:00\tcommit\n")
		s.touch(pend("B")(s), newTime)
	}
	updatePlain := func(body string) func(*sandbox) {
		return func(s *sandbox) {
			s.write(H(s), "# 引き継ぎ\n\n"+body+"\n")
			s.touch(H(s), newTime.Add(time.Minute))
		}
	}

	scenario{name: "印なしのファイル: 自分の更新で通す（後方互換）", setup: setupPlain, steps: []step{
		{name: "印を知らないプロジェクトが普通に更新したら通す・マーカーを畳む",
			do: updatePlain("## セッション B の申し送り"), mk: stop("B"),
			want: all(wantStop("PASS"), wantExists(pend("B"), false))},
	}}.run(t)

	scenario{name: "印なしのファイル: ほかのセッションの更新でも通す（従来どおりの弱点を互換のため残す）", setup: setupPlain, steps: []step{
		{name: "B は 1 文字も書いていないが、印が無いファイルでは時刻だけで畳む",
			do: updatePlain("## セッション A の申し送り（更新）"), mk: stop("B"),
			want: all(wantStop("PASS"), wantExists(pend("B"), false))},
	}}.run(t)

	scenario{name: "印ありのファイル: 自分の印が無い更新は差し戻す", setup: setupPlain, steps: []step{
		{name: "誰か（A）が 1 度でも印を書けば、そのファイルは厳密になる",
			do: func(s *sandbox) {
				s.write(H(s), "# 引き継ぎ\n\n## セッション A の申し送り\n"+writerMark("A")+"\n")
				s.touch(H(s), newTime.Add(time.Minute))
			},
			mk: stop("B"), want: all(wantStop("BLOCK"), wantExists(pend("B"), true))},
		{name: "差し戻しの文に、本文へ入れる印の行がそのまま出る", mk: stop("B"),
			want: func(t *testing.T, _ *sandbox, g got) {
				t.Helper()
				if !strings.Contains(g.res.Block, writerMark("B")) {
					t.Errorf("印の行が無い: %s", g.res.Block)
				}
			}},
		{name: "印ありのファイルに自分の印つきで書けば通す",
			do: func(s *sandbox) {
				s.write(H(s), "# 引き継ぎ\n\n## セッション A の申し送り\n"+writerMark("A")+
					"\n\n## セッション B の申し送り\n"+writerMark("B")+"\n")
				s.touch(H(s), newTime.Add(2*time.Minute))
			},
			mk: stop("B"), want: all(wantStop("PASS"), wantExists(pend("B"), false))},
	}}.run(t)
}

// TestHandoffMarkAccumulates は「1 回のツールの呼び出しで起きた完了を、全部・展開できる形で記録する」ことの回帰。
//
// 実際に起きたこと: `for i in ABC-0123 ABC-0124 ABC-0125; do looptrack issue status $i Done; done` の後、
// 記録には `issue.status Done $i` の 1 行しか残らなかった（3 件のうち 1 件、しかも ID は未展開の変数名）。
// 原因は 2 つ。(1) この hook は Bash ツールに渡された**コマンドの文字列**を読むだけで、シェルの展開後の値は
// 原理的に追えない。(2) 一致を単一の変数に**上書き**していたので、複数件のうち最後の 1 件しか残らなかった。
// handoff.go の addEvent をやめて再び上書きに戻すと「1 回の呼び出しで 3 件」が red になる。
func TestHandoffMarkAccumulates(t *testing.T) {
	needGit(t)
	R := func(s *sandbox, parts ...string) string { return s.p(append([]string{"repo"}, parts...)...) }
	pend := func(s *sandbox) string { return R(s, ".claude", "handoff-pending.d", "s1") }
	setup := func(s *sandbox) {
		s.git("init", "-q", "-b", "main", R(s))
		s.mkdir("repo", ".claude", "memories")
		s.write(R(s, "src", "a.txt"), "a\n")
		s.git("-C", R(s), "add", "src/a.txt")
		s.git("-C", R(s), "commit", "-qm", "base")
	}
	mark := func(cmd, resp string) func(*sandbox) call {
		return func(s *sandbox) call {
			return call{hook: "post-work-complete-handoff-mark", env: map[string]string{"CLAUDE_PROJECT_DIR": R(s)},
				input: jsonInput(map[string]any{"session_id": "s1", "tool_name": "Bash",
					"tool_input": map[string]any{"command": cmd}, "tool_response": resp})}
		}
	}
	clear := func(s *sandbox) { os.RemoveAll(R(s, ".claude", "handoff-pending.d")) }
	// 記録の突き合わせと「未展開の変数を書かない」の確認は、パッケージ共通のヘルパに寄せてある。
	wantRecord := func(want ...string) func(*testing.T, *sandbox, got) { return wantRecordLines(pend, want...) }
	wantNoDollar := wantNoRawVar(pend)

	scenario{name: "1 回の呼び出しの完了を全部積む", setup: setup, steps: []step{
		{name: "ループの $i は ID 不明（?）として残す（変数名をそのまま書かない）", do: clear,
			mk:   mark("for i in TST-0001 TST-0002 TST-0003\ndo\n  looptrack issue status $i Done --comment ok\ndone", "x"),
			want: all(wantMark("OUT"), wantRecord("issue.status\tDone\t?"), wantNoDollar)},
		{name: "コマンド置換の ID も ID 不明にする", do: clear,
			mk:   mark("looptrack issue close $(cat id.txt)", "x"),
			want: all(wantMark("OUT"), wantRecord("issue.close\t?"), wantNoDollar)},
		{name: "1 回の呼び出しで 3 件を Done にしたら 3 行とも残る", do: clear,
			mk: mark("looptrack issue status TST-0001 Done\nlooptrack issue status TST-0002 Done\nlooptrack issue status TST-0003 Done", "x"),
			want: all(wantMark("OUT"), wantLines(pend, 3),
				wantRecord("issue.status\tDone\tTST-0001", "issue.status\tDone\tTST-0002", "issue.status\tDone\tTST-0003"))},
		{name: "close と status が混ざっても両方残る", do: clear,
			mk:   mark("looptrack issue close TST-0001 && looptrack issue status TST-0002 Canceled", "x"),
			want: all(wantMark("OUT"), wantRecord("issue.close\tTST-0001", "issue.status\tCanceled\tTST-0002"))},
		{name: "同じ呼び出しの中の重複は 1 件にまとめる", do: clear,
			mk:   mark("looptrack issue status TST-0001 Done; looptrack issue status TST-0001 Done", "x"),
			want: all(wantMark("OUT"), wantRecord("issue.status\tDone\tTST-0001"))},
		{name: "コミットと close が同じ呼び出しなら両方残る（以前はコミットが消えていた）", do: clear,
			mk:   mark("git commit -m x && looptrack issue close TST-0001", okResp),
			want: all(wantMark("OUT"), wantRecord("commit\t061e38aeb\tmain\t?", "issue.close\tTST-0001"))},
		{name: "失敗したコミットは落ちるが、同じ呼び出しの close は残る", do: clear,
			mk:   mark("git commit -m x; looptrack issue close TST-0001", "nothing to commit, working tree clean"),
			want: all(wantMark("OUT"), wantRecord("issue.close\tTST-0001"))},
		{name: "知らせの文面に全部の件が出る", do: clear,
			mk: mark("looptrack issue status TST-0001 Done; looptrack issue status TST-0002 Done", "x"),
			want: func(t *testing.T, _ *sandbox, g got) {
				t.Helper()
				if !strings.Contains(g.ctx(), "TST-0001") || !strings.Contains(g.ctx(), "TST-0002") {
					t.Errorf("両方の ID が出ていない: %s%s", g.ctx(), g.why())
				}
			}},
	}}.run(t)
}

// TestHandoffMarkShellKeywords は「シェルの予約語で始まる区間の中の完了も数える」ことの回帰。
//
// commandSegments は区切り（; & | ( )）でしか区間を切らないので、1 行のループ
// `for i in ABC-0123 ABC-0124; do looptrack issue status $i Done; done` は
// [for i in …] [do looptrack issue status $i Done] [done] に切れ、真ん中の区間の先頭の語は `do` になる。
// 予約語を剥がす前は、この区間が「looptrack でも git でもない」として丸ごと捨てられ、
// ループの中の完了が 1 件も残らなかった。判定が同じ先頭の語に乗っている git commit も、
// `for d in a b; do git commit -m x; done` の形では数えられていなかった（巻き添え）。
//
// handoff.go の shellKeywords の剥がしを外すと、下の 17 段（「予約語」の表と、ループ・波括弧の形）が red になる。
// 末尾の「誤検出」の段は、剥がす対象を予約語より広げていないこと（前置きの語 sudo・env・xargs や
// 入れ子のシェルは剥がさないこと）を固定する。広げると、実際には実行されない引数（xargs の雛形）まで
// 完了として積むので、ここを red にせずに広げることはできない。
func TestHandoffMarkShellKeywords(t *testing.T) {
	needGit(t)
	R := func(s *sandbox, parts ...string) string { return s.p(append([]string{"repo"}, parts...)...) }
	pend := func(s *sandbox) string { return R(s, ".claude", "handoff-pending.d", "s1") }
	setup := func(s *sandbox) {
		s.git("init", "-q", "-b", "main", R(s))
		s.mkdir("repo", ".claude", "memories")
		s.write(R(s, "src", "a.txt"), "a\n")
		s.git("-C", R(s), "add", "src/a.txt")
		s.git("-C", R(s), "commit", "-qm", "base")
	}
	mark := func(cmd, resp string) func(*sandbox) call {
		return func(s *sandbox) call {
			return call{hook: "post-work-complete-handoff-mark", env: map[string]string{"CLAUDE_PROJECT_DIR": R(s)},
				input: jsonInput(map[string]any{"session_id": "s1", "tool_name": "Bash",
					"tool_input": map[string]any{"command": cmd}, "tool_response": resp})}
		}
	}
	clear := func(s *sandbox) { os.RemoveAll(R(s, ".claude", "handoff-pending.d")) }
	wantRecord := func(want ...string) func(*testing.T, *sandbox, got) { return wantRecordLines(pend, want...) }
	// 積まれないこと（記録のファイルそのものができない）
	wantNothing := all(wantMark("QUIET"), wantExists(pend, false))

	steps := []step{
		{name: "1 行の for ループの中の完了を積む（ID は展開できないので ? にする）", do: clear,
			mk: mark("for i in TST-0001 TST-0002 TST-0003; do looptrack issue status $i Done; done", "x"),
			want: all(wantMark("OUT"), wantRecord("issue.status\tDone\t?"), wantNoRawVar(pend),
				wantLines(pend, 1))}, // 同じ区間は 1 つなので、重複を畳んだ 1 行になる
		{name: "ループの中の git commit を数える（先頭の語の判定の巻き添えだった）", do: clear,
			mk:   mark("for d in a b; do git commit -m x; done", okResp),
			want: all(wantMark("OUT"), wantRecord("commit\t061e38aeb\tmain\t?"))},
		{name: "入れ子のループの中の close も届く", do: clear,
			mk:   mark("for i in a; do for j in b; do looptrack issue close TST-0001; done; done", "x"),
			want: all(wantMark("OUT"), wantRecord("issue.close\tTST-0001"))},
		{name: "波括弧のまとまりの中の close も届く", do: clear,
			mk:   mark("{ looptrack issue close TST-0001; }", "x"),
			want: all(wantMark("OUT"), wantRecord("issue.close\tTST-0001"))},
		{name: "while read のループの中の close も届く", do: clear,
			mk:   mark("while read l; do looptrack issue close TST-0001; done < ids.txt", "x"),
			want: all(wantMark("OUT"), wantRecord("issue.close\tTST-0001"))},
		{name: "予約語のあとに環境変数の代入が挟まっても届く", do: clear,
			mk:   mark("for i in a; do LOOPTRACK_LANG=ja looptrack issue close TST-0001; done", "x"),
			want: all(wantMark("OUT"), wantRecord("issue.close\tTST-0001"))},
		// 届かないことが分かっている形（案を広げずに残した穴。別のイシューで扱う）
		{name: "function の宣言は名前が挟まるので届かない（既知の穴）", do: clear,
			mk: mark("function f { looptrack issue close TST-0001; }", "x"), want: wantNothing},
		// 誤検出が増えていないこと（通す側を広げていないことの確認）
		{name: "誤検出: echo の引数の looptrack は完了にしない", do: clear,
			mk: mark("echo looptrack issue close TST-0001", "x"), want: wantNothing},
		{name: "誤検出: echo の引数に予約語が挟まっても完了にしない", do: clear,
			mk: mark("echo do looptrack issue close TST-0001", "x"), want: wantNothing},
		{name: "誤検出: 予約語のあとが looptrack でなければ完了にしない", do: clear,
			mk: mark("do echo looptrack issue close TST-0001", "x"), want: wantNothing},
		{name: "誤検出: 引用符の中の do looptrack は完了にしない（コミットだけ残る）", do: clear,
			mk:   mark(`git commit -m "do looptrack issue close TST-0001"`, okResp),
			want: all(wantMark("OUT"), wantRecord("commit\t061e38aeb\tmain\t?"))},
		{name: "誤検出: 前置きの語（sudo・xargs・入れ子のシェル）は剥がさない", do: clear,
			mk: mark("sudo -u me looptrack issue close TST-0001", "x"), want: wantNothing},
		{name: "誤検出: xargs の雛形は実際には実行されないので完了にしない", do: clear,
			mk: mark("xargs -I{} looptrack issue close {}", "x"), want: wantNothing},
	}
	// 予約語それぞれについて、`<予約語> looptrack …` が届くことを表で押さえる。
	// 入れるのは「直後がコマンドの位置になる」語だけ（for / select / case は直後が変数名・被検査語なので入れない）。
	for _, kw := range []string{"do", "then", "else", "elif", "if", "while", "until", "{", "!", "time", "coproc"} {
		steps = append(steps, step{name: "予約語 " + kw + " の直後の close を積む", do: clear,
			mk:   mark(kw+" looptrack issue close TST-0001", "x"),
			want: all(wantMark("OUT"), wantRecord("issue.close\tTST-0001"))})
	}

	scenario{name: "予約語で始まる区間の中の完了", setup: setup, steps: steps}.run(t)
}

// TestHandoffFreshnessWriterMarkTime は「印そのものの新しさ」で判定することの回帰。
//
// 引き継ぎは全セッションで同じ 1 ファイルなので、ほかのセッションが追記するだけでファイルの更新時刻は進む。
// 「ファイルが完了より新しい」＋「自分の印がある」の 2 つだけで見ると、**過去に 1 度書いた印が以後ずっと
// 「今回も書いた証拠」として働く**。時刻つきの印（`<!-- looptrack:session <ID> <RFC3339> -->`）なら、
// 印の時刻を完了のイベントと比べられる。時刻の無い印（古い形）は従来どおり更新時刻で見る（後方互換）。
//
// handoff.go の判定を `strings.Contains(t, writerMark(sid))` に戻すと、時刻つきのケースが軒並み red になる
// （末尾が ` -->` でなくなるので、印を書いたセッションが全員差し戻される）。
func TestHandoffFreshnessWriterMarkTime(t *testing.T) {
	needGit(t)
	R := func(s *sandbox, parts ...string) string { return s.p(append([]string{"repo"}, parts...)...) }
	H := func(s *sandbox) string { return R(s, ".claude", "memories", "handoff.md") }
	pend := func(sid string) func(*sandbox) string {
		return func(s *sandbox) string { return R(s, ".claude", "handoff-pending.d", sid) }
	}
	// 完了のイベント（マーカー）は newTime。印の時刻はこれと比べる
	before := newTime.Add(-time.Hour) // 完了より前に書いた印（過去の自分の印）
	after := newTime.Add(time.Hour)   // 完了より後に書いた印（今回書いた印）
	setup := func(s *sandbox) {
		s.git("init", "-q", "-b", "main", R(s))
		s.mkdir("repo", ".claude", "memories")
		s.write(H(s), "# 引き継ぎ\n\n## セッション A の申し送り\n"+writerMark("A")+"\n")
		s.touch(H(s), oldTime)
		s.write(pend("B")(s), "2026-09-21 02:00:00\tcommit\n")
		s.touch(pend("B")(s), newTime)
	}
	stop := func(sid string) func(*sandbox) call {
		return func(s *sandbox) call {
			return call{hook: "stop-handoff-freshness", env: map[string]string{"CLAUDE_PROJECT_DIR": R(s)},
				input: jsonInput(map[string]any{"session_id": sid})}
		}
	}
	// ほかのセッションの追記でファイルの更新時刻だけが進んだ状態を作る（本文の body は B の節）
	touched := func(body string) func(*sandbox) {
		return func(s *sandbox) {
			s.write(H(s), "# 引き継ぎ\n\n## セッション A の申し送り\n"+writerMark("A")+"\n\n"+body+"\n")
			s.touch(H(s), newTime.Add(time.Minute)) // C の追記で mtime は完了より後
		}
	}

	scenario{name: "時刻つきの印は、印の時刻で判定する", setup: setup, steps: []step{
		{name: "完了より前に書いた自分の印は、mtime が進んでいても差し戻す",
			do: touched("## セッション B の申し送り（前の周）\n" + writerMarkAt("B", before)),
			mk: stop("B"), want: all(wantStop("BLOCK"), wantExists(pend("B"), true))},
		{name: "完了より後に書いた自分の印なら通して畳む",
			do: touched("## セッション B の申し送り\n" + writerMarkAt("B", after)),
			mk: stop("B"), want: all(wantStop("PASS"), wantExists(pend("B"), false))},
	}}.run(t)

	scenario{name: "時刻つきの印: 書き手を取り違えない", setup: setup, steps: []step{
		{name: "ほかのセッションの新しい印では畳まない",
			do: touched("## セッション C の申し送り\n" + writerMarkAt("C", after)),
			mk: stop("B"), want: all(wantStop("BLOCK"), wantExists(pend("B"), true))},
		{name: "ID の先頭が同じだけの印（B-2）では畳まない",
			do: touched("## セッション B-2 の申し送り\n" + writerMarkAt("B-2", after)),
			mk: stop("B"), want: all(wantStop("BLOCK"), wantExists(pend("B"), true))},
	}}.run(t)

	scenario{name: "時刻の無い印（古い形）は従来どおり更新時刻で見る", setup: setup, steps: []step{
		{name: "時刻の無い自分の印は、mtime が完了より後なら通す（後方互換）",
			do: touched("## セッション B の申し送り\n" + writerMark("B")),
			mk: stop("B"), want: all(wantStop("PASS"), wantExists(pend("B"), false))},
	}}.run(t)

	scenario{name: "時刻つきと時刻なしが混ざっても、新しい印があれば通す", setup: setup, steps: []step{
		{name: "古い時刻つきの印と、完了より後の時刻つきの印が両方あれば通す",
			do: touched("## セッション B の申し送り（前の周）\n" + writerMarkAt("B", before) +
				"\n\n## セッション B の申し送り（今回）\n" + writerMarkAt("B", after)),
			mk: stop("B"), want: all(wantStop("PASS"), wantExists(pend("B"), false))},
	}}.run(t)

	// 実物の `looptrack handoff append` が書いた印を、鮮度ガードがそのまま受け取れるかを見る。
	// 上の走査は印を手で組み立てているので、**書く側が時刻つきをやめても落ちない**。ここだけが両者を結ぶ。
	cliAppend := func(at time.Time) func(*sandbox) {
		return func(s *sandbox) {
			s.write(H(s), "# 引き継ぎ\n\n## セッション A の申し送り\n"+writerMark("A")+"\n")
			e := handoffTestEnv(R(s), map[string]string{"CLAUDE_CODE_SESSION_ID": "B"})
			e.Now = func() time.Time { return at }
			if code, out, errOut := runHandoff(e, "## セッション B の申し送り\n\n本文 B", "append", "--file", H(s)); code != 0 {
				s.t.Fatalf("handoff append: exit %d %s %s", code, out, errOut)
			}
			// ほかのセッションの追記で mtime だけが完了より後になった状態にそろえる
			s.touch(H(s), newTime.Add(time.Minute))
		}
	}
	// 通す段（PASS）は積み残しのマーカーを畳むので、同じ scenario では差し戻す段より後に置く。
	scenario{name: "append が書いた印を鮮度ガードが受け取る（書く側と読む側を結ぶ）", setup: setup, steps: []step{
		{name: "完了より前に append した印は、mtime が進んでいても差し戻す（時刻なしに戻すとここが緑になる）",
			do: cliAppend(before), mk: stop("B"), want: all(wantStop("BLOCK"), wantExists(pend("B"), true))},
		{name: "完了より後に append したら通して畳む",
			do: cliAppend(after), mk: stop("B"), want: all(wantStop("PASS"), wantExists(pend("B"), false))},
	}}.run(t)

	scenario{name: "append が書いた印: 完了と同じ秒でも通す", setup: setup, steps: []step{
		{name: "完了に秒未満があっても、同じ秒に append した印で通す（切り捨てで落とさない）",
			// 印は RFC3339（秒まで）なので、丸めずに比べると「完了の直後に書いたセッションだけが
			// 差し戻される」ことになる（再現しにくく、原因も見えない）
			do: func(s *sandbox) {
				s.touch(pend("B")(s), newTime.Add(500*time.Millisecond))
				cliAppend(newTime.Add(900 * time.Millisecond))(s)
			},
			mk: stop("B"), want: all(wantStop("PASS"), wantExists(pend("B"), false))},
	}}.run(t)
}
