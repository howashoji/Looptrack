package loop

// kit/loop/verify/verify-session-scope.sh と verify-pre-tool-scope.sh（33 ケース）の移植。
// session-scope-guard の「1 課題 = 1 セッション」の束縛は機能ごと削除したので、束縛のケースは無い。

import (
	"strings"
	"testing"
)

// scopeKind は session-scope-guard の結果（DENY / OUT / QUIET / INVALID）。
func scopeKind(g got) string {
	switch {
	case g.json["decision"] == "block":
		return "DENY"
	case g.quiet():
		return "QUIET"
	case g.hookEventName() == "UserPromptSubmit":
		return "OUT"
	}
	return "INVALID"
}

func wantScope(k string) func(*testing.T, *sandbox, got) {
	return func(t *testing.T, _ *sandbox, g got) {
		t.Helper()
		if scopeKind(g) != k {
			t.Errorf("期待 %s / 実際 %s: %s%s", k, scopeKind(g), g.out.Stdout, g.why())
		}
	}
}

func wantCtxContains(w string) func(*testing.T, *sandbox, got) {
	return func(t *testing.T, _ *sandbox, g got) {
		t.Helper()
		if !strings.Contains(g.out.Stdout, w) {
			t.Errorf("「%s」が無い: %s%s", w, g.out.Stdout, g.why())
		}
	}
}

const (
	bigJSONL = `{"type":"assistant","message":{"usage":{"input_tokens":100,"output_tokens":500,"cache_read_input_tokens":480000,"cache_creation_input_tokens":30000}}}
{"type":"assistant","isSidechain":true,"message":{"usage":{"input_tokens":1,"output_tokens":1,"cache_read_input_tokens":9999999,"cache_creation_input_tokens":0}}}
`
	smallJSONL = `{"type":"assistant","message":{"usage":{"input_tokens":10,"output_tokens":50,"cache_read_input_tokens":80000,"cache_creation_input_tokens":1000}}}
`
	midJSONL = `{"type":"assistant","message":{"usage":{"input_tokens":10,"output_tokens":50,"cache_read_input_tokens":150000,"cache_creation_input_tokens":0}}}
`
)

func TestSessionScope(t *testing.T) {
	// run <session> <prompt> [transcript] [プロジェクト]（環境変数はそのまま）。
	// 「1 課題 = 1 セッション」の束縛は機能ごと削除したので、この hook は文脈の大きさを警告するだけ。
	run := func(sid, prompt, tr, proj string, env ...string) func(*sandbox) call {
		return func(s *sandbox) call {
			p := s.p("proj")
			if proj != "" {
				p = s.p(proj)
			}
			trp := ""
			if tr != "" {
				trp = s.p(tr)
			}
			m := map[string]string{"CLAUDE_PROJECT_DIR": p}
			for i := 0; i+1 < len(env); i += 2 {
				m[env[i]] = env[i+1]
			}
			return call{hook: "session-scope-guard", env: m,
				input: jsonInput(map[string]any{"session_id": sid, "transcript_path": trp, "prompt": prompt})}
		}
	}
	setup := func(s *sandbox) {
		s.write(s.p("proj", ".claude", ".looptrack-freshness", "project.json"), `{"prefix": "TST", "width": 4, "project": "tst", "url": "http://example.invalid"}`+"\n")
		s.write(s.p("big.jsonl"), bigJSONL)
		s.write(s.p("small.jsonl"), smallJSONL)
		s.write(s.p("mid.jsonl"), midJSONL)
	}
	// 何も止めない（束縛は削除済み）。着手・別の課題への着手・一括の着手のいずれも素通りする。
	scenario{name: "着手の指示は止めない（束縛は無い）", setup: setup, steps: []step{
		{name: "イシュー ID への着手を止めない", mk: run("nb1", "TST-0015に着手して。", "", ""), want: wantScope("QUIET")},
		{name: "続けて別の課題への着手も止めない", mk: run("nb1", "TST-0009に着手して。", "", ""), want: wantScope("QUIET")},
		{name: "一括の着手も止めない", mk: run("nb2", "残件を連続で片付けて。", "small.jsonl", ""), want: wantScope("QUIET")},
		{name: "英語の着手も止めない", mk: run("nb2", "Start working on TST-0021, please.", "", ""), want: wantScope("QUIET")},
		{name: "膨らんだセッションでも着手は止めず、文脈の大きさだけ警告する",
			mk:   run("nb3", "TST-0009に着手して。", "big.jsonl", "", "LOOPTRACK_LANG", "ja"),
			want: all(wantScope("OUT"), wantCtxContains("[文脈の大きさ] 現在のコンテキストは約 51 万トークン / 応答"))},
		{name: "膨らんだセッションの一括の着手も止めない",
			mk: run("nb4", "残件を連続で片付けて。", "big.jsonl", "", "LOOPTRACK_LANG", "ja"), want: wantScope("OUT")},
		{name: "小さいセッションは黙る", mk: run("nb5", "進めて。", "small.jsonl", ""), want: wantScope("QUIET")},
		// 受け入れの実証: 束縛を入れる環境変数は無くなったので、設定しても何も起きない。
		{name: "LOOPTRACK_LOOP_SCOPE_BIND=1 を設定しても束縛しない",
			mk: run("nb6", "TST-0015に着手して。", "", "", "LOOPTRACK_LOOP_SCOPE_BIND", "1"), want: wantScope("QUIET")},
		{name: "LOOPTRACK_LOOP_SCOPE_BIND=1 でも別の課題への着手を止めない",
			mk: run("nb6", "TST-0009に着手して。", "", "", "LOOPTRACK_LOOP_SCOPE_BIND", "1"), want: wantScope("QUIET")},
	}}.run(t)

	// (B) 文脈の大きさの警告
	scenario{name: "文脈の大きさの警告", setup: setup, steps: []step{
		{name: "膨らんだセッションは警告", mk: run("sg4", "進めて。", "big.jsonl", ""), want: wantScope("OUT")},
		{name: "同じ大きさでの再警告は抑える", mk: run("sg4", "続けて。", "big.jsonl", ""), want: wantScope("QUIET")},
		{name: "小さいセッションは警告なし", mk: run("sg5", "進めて。", "small.jsonl", ""), want: wantScope("QUIET")},
		{name: "中くらいのセッションは既定では警告なし", mk: run("sg7", "進めて。", "mid.jsonl", ""), want: wantScope("QUIET")},
		{name: "LOOPTRACK_LOOP_SCOPE_WARN で閾値を変えられる", mk: run("sg8", "進めて。", "mid.jsonl", "", "LOOPTRACK_LOOP_SCOPE_WARN", "120000"), want: wantScope("OUT")},
		{name: "通知は対象外", mk: run("sg9", "<task-notification>進めて</task-notification>", "big.jsonl", ""), want: wantScope("QUIET")},
		{name: "Copilot の Stop の差し戻しの理由は対象外",
			mk: run("sg10", "[Stop hook の差し戻し] 引き継ぎが作業の完了に追いついていません。", "big.jsonl", ""), want: wantScope("QUIET")},
		// 他のセッションからの連絡。利用者の発話ではないので判定の対象にしない
		{name: "他セッションからの連絡は対象外",
			mk: run("sg11", "Another Claude session sent a message:\n"+
				`<cross-session-message from="local_89abcdef" name="2. 別の作業">ABC-0123 を live で確認してください。</cross-session-message>`,
				"big.jsonl", ""), want: wantScope("QUIET")},
	}}.run(t)
}

func TestPreToolScope(t *testing.T) {
	needGit(t)
	setup := func(s *sandbox) {
		for _, d := range []string{"proj", "other"} {
			s.git("init", "-q", s.p(d))
			s.git("-C", s.p(d), "commit", "-q", "--allow-empty", "-m", "init")
		}
		s.git("-C", s.p("proj"), "worktree", "add", "-q", s.p("proj-wt"))
		s.mkdir("plain")
	}
	// run <tool_name> <tool_input> [LOOPTRACK_PROJECT]
	run := func(tool string, ti func(*sandbox) map[string]any, slug ...string) func(*sandbox) call {
		return func(s *sandbox) call {
			sl := "tst"
			if len(slug) > 0 {
				sl = slug[0]
			}
			return call{hook: "pre-tool-scope-guard", env: map[string]string{"CLAUDE_PROJECT_DIR": s.p("proj"), "LOOPTRACK_PROJECT": sl},
				input: jsonInput(map[string]any{"session_id": "s1", "cwd": s.p("proj"), "hook_event_name": "PreToolUse", "tool_name": tool, "tool_input": ti(s)})}
		}
	}
	f := func(parts ...string) func(*sandbox) map[string]any {
		return func(s *sandbox) map[string]any { return map[string]any{"file_path": s.p(parts...), "content": "x"} }
	}
	fr := func(p string) func(*sandbox) map[string]any {
		return func(*sandbox) map[string]any { return map[string]any{"file_path": p, "content": "x"} }
	}
	// j はコマンド（{P}・{O}・{N}・{T} を sandbox のパスに置き換える）
	j := func(cmd string) func(*sandbox) map[string]any {
		return func(s *sandbox) map[string]any {
			c := strings.NewReplacer("{P}", s.p("proj"), "{O}", s.p("other"), "{N}", s.p("plain"), "{T}", s.root).Replace(cmd)
			return map[string]any{"command": c}
		}
	}
	raw := func(m map[string]any) func(*sandbox) map[string]any {
		return func(*sandbox) map[string]any { return m }
	}
	ask, quiet := wantAsk, wantQuiet

	scenario{name: "別リポジトリ・別プロジェクトの変更の確認", setup: setup, steps: []step{
		// ファイルの編集
		{name: "プロジェクトの中", mk: run("Write", f("proj", "a.txt")), want: quiet},
		{name: "同じリポジトリの worktree", mk: run("Write", f("proj-wt", "a.txt")), want: quiet},
		{name: "git でない場所（scratchpad・記憶 等）", mk: run("Write", f("plain", "a.txt")), want: quiet},
		{name: "まだ無いディレクトリ（git でない側）", mk: run("Write", f("plain", "new", "dir", "a.txt")), want: quiet},
		{name: "別のリポジトリ", mk: run("Edit", f("other", "a.txt")), want: ask},
		{name: "別のリポジトリのまだ無いファイル", mk: run("Write", f("other", "sub", "new.txt")), want: ask},
		{name: "相対パスで別のリポジトリ", mk: run("Write", fr("../other/a.txt")), want: ask},
		// Bash
		{name: "プロジェクトの中の変更", mk: run("Bash", j("git add a && git commit -m x")), want: quiet},
		{name: "別リポジトリを読むだけ（cd + grep）", mk: run("Bash", j("cd {O} && grep -rn foo . | head")), want: quiet},
		{name: "別リポジトリの状態を見るだけ", mk: run("Bash", j("cd ../other && git status --short && git log --oneline -3")), want: quiet},
		{name: "別リポジトリを読むだけ（2>/dev/null）", mk: run("Bash", j("cd {O} && ls 2>/dev/null; cat x 2>&1 | head")), want: quiet},
		{name: "別リポジトリのイシューを見るだけ", mk: run("Bash", j("cd {O} && looptrack issue show X-0001")), want: quiet},
		{name: "別リポジトリの CLI の使い方を見る", mk: run("Bash", j("cd ../other && looptrack issue new --help 2>&1 | head -30")), want: quiet},
		{name: "別リポジトリを最新にする（git pull）", mk: run("Bash", j("cd ../other && git pull --ff-only -q && git log --oneline -1")), want: quiet},
		{name: "別リポジトリでコミット", mk: run("Bash", j("cd {O} && git add a && git commit -m x")), want: ask},
		{name: "git -C で別リポジトリに push", mk: run("Bash", j("git -C {O} push origin master")), want: ask},
		{name: "別リポジトリで sed -i", mk: run("Bash", j("cd ../other && sed -i '' 's/a/b/' f.txt")), want: ask},
		{name: "別リポジトリへのリダイレクト", mk: run("Bash", j("cd {O} && echo x > f.txt")), want: ask},
		{name: "別リポジトリのファイルを消す", mk: run("Bash", j("cd {O} && rm -f f.txt")), want: ask},
		{name: "別リポジトリで起票（実例: 誤起票）", mk: run("Bash", j("cd ../other && looptrack issue new t --type bug")), want: ask},
		// 包んだ形（入れ子のシェル・前置の語・eval）。素の形「cd {O} && touch a.txt」と同じく確認する
		{name: "素の形（包んだ形の対照）", mk: run("Bash", j("cd {O} && touch a.txt")), want: ask},
		{name: "bash -c で包んだ別リポジトリの変更", mk: run("Bash", j("bash -c 'cd {O} && touch a.txt'")), want: ask},
		{name: "sudo sh -c で包んだ別リポジトリの変更", mk: run("Bash", j("sudo sh -c 'cd {O} && touch a.txt'")), want: ask},
		{name: "nohup bash -c … & で包んだ別リポジトリの変更", mk: run("Bash", j("nohup bash -c 'cd {O} && touch a.txt' &")), want: ask},
		{name: "setsid sh -c で包んだ別リポジトリの変更", mk: run("Bash", j("setsid sh -c 'cd {O} && touch a.txt'")), want: ask},
		{name: "eval で包んだ別リポジトリの変更", mk: run("Bash", j(`eval "cd {O} && touch a.txt"`)), want: ask},
		{name: "bash -c で包んだ別プロジェクトの起票", mk: run("Bash", j("bash -c 'LOOPTRACK_PROJECT=other looptrack issue comment X-0001 y'")), want: ask},
		{name: "env で前置した別プロジェクトの起票", mk: run("Bash", j("env LOOPTRACK_PROJECT=other looptrack issue comment X-0001 y")), want: ask},
		{name: "包んだ形で別リポジトリを読むだけ", mk: run("Bash", j("bash -c 'cd {O} && git status'")), want: quiet},
		{name: "sudo sh -c で別リポジトリを読むだけ", mk: run("Bash", j("sudo sh -c 'cd {O} && grep -rn foo .'")), want: quiet},
		{name: "包んだ形でプロジェクトの中を変更", mk: run("Bash", j("bash -c 'cd {P} && touch a.txt'")), want: quiet},
		{name: "eval でプロジェクトの中を変更", mk: run("Bash", j(`eval "touch a.txt"`)), want: quiet},
		{name: "git でない場所での変更", mk: run("Bash", j("cd {N} && echo x > f.txt")), want: quiet},
		{name: "同じリポジトリの worktree でコミット", mk: run("Bash", j("cd {T}/proj-wt && git commit -q --allow-empty -m x")), want: quiet},
		// 別プロジェクトのイシューの変更（LOOPTRACK_PROJECT）
		{name: "LOOPTRACK_PROJECT=別 で comment", mk: run("Bash", j("LOOPTRACK_PROJECT=other looptrack issue comment X-0001 y")), want: ask},
		{name: "export LOOPTRACK_PROJECT=別 のあとで new", mk: run("Bash", j("export LOOPTRACK_PROJECT=other && looptrack issue new t")), want: ask},
		{name: "LOOPTRACK_PROJECT=別 で読むだけ", mk: run("Bash", j("LOOPTRACK_PROJECT=other looptrack issue list --all")), want: quiet},
		{name: "LOOPTRACK_PROJECT=自分 で変更", mk: run("Bash", j("LOOPTRACK_PROJECT=tst looptrack issue close TST-0001 --comment c")), want: quiet},
		{name: "セッションの LOOPTRACK_PROJECT が無ければ見ない", mk: run("Bash", j("LOOPTRACK_PROJECT=other looptrack issue comment X-0001 y"), ""), want: quiet},
		{name: "MCP で別プロジェクトに起票", mk: run("mcp__looptrack__create_issue", raw(map[string]any{"project": "other", "title": "t"})), want: ask},
		{name: "MCP で自分のプロジェクトに起票", mk: run("mcp__looptrack__create_issue", raw(map[string]any{"project": "tst", "title": "t"})), want: quiet},
		{name: "MCP で project を省略", mk: run("mcp__looptrack__add_comment", raw(map[string]any{"id": "TST-0001", "body": "c"})), want: quiet},
		{name: "MCP で別プロジェクトを読むだけ", mk: run("mcp__looptrack__list_issues", raw(map[string]any{"project": "other"})), want: quiet},
		// 例外
		{name: "LOOPTRACK_LOOP_SCOPE_ALLOW_REPOS に挙げたリポジトリは通す",
			mk: func(s *sandbox) call {
				return call{hook: "pre-tool-scope-guard", env: map[string]string{"CLAUDE_PROJECT_DIR": s.p("proj"), "LOOPTRACK_LOOP_SCOPE_ALLOW_REPOS": s.p("other")},
					input: jsonInput(map[string]any{"session_id": "s1", "cwd": s.p("proj"), "tool_name": "Write", "tool_input": map[string]any{"file_path": s.p("other", "a.txt")}})}
			},
			want: quiet},
		{name: "入力が JSON でなければ何もしない",
			mk: func(s *sandbox) call {
				return call{hook: "pre-tool-scope-guard", env: map[string]string{"CLAUDE_PROJECT_DIR": s.p("proj")}, input: "not json"}
			},
			want: all(quiet, func(t *testing.T, _ *sandbox, g got) {
				if g.out.ExitCode != 0 {
					t.Errorf("終了コード %d", g.out.ExitCode)
				}
			})},
	}}.run(t)
}
