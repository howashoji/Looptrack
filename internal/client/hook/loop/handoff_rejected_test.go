package loop

import (
	"os"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
)

// TestHandoffMarkRejectedClose は「サーバに拒否された close / status 変更を完了として積まない」ことの回帰。
//
// 以前は打ったコマンド（ツールへの入力）だけを見て積んでいたので、受け入れ条件が雛形のままで拒否された close も
// 「イシュー Done」として積まれ、していない作業について引き継ぎを要求していた（commit だけが応答を見ていた）。
// 拒否の段と同じシナリオに、成功した close / status・空の応答・知らない形の応答が従来どおり積まれる対照を置く
// （対照が積まれなければ、拒否の段の「積まれない」は経路が死んでいても通ってしまう）。
func TestHandoffMarkRejectedClose(t *testing.T) {
	needGit(t)
	R := func(s *sandbox, parts ...string) string { return s.p(append([]string{"repo"}, parts...)...) }
	pend := func(s *sandbox) string { return R(s, ".claude", "handoff-pending.d", "s1") }
	setup := func(s *sandbox) {
		s.git("init", "-q", "-b", "main", R(s))
		s.mkdir("repo", ".claude", "memories")
	}
	// ev は PostToolUse の入力（応答はどの形でも渡せるよう any で受ける）
	ev := func(tool string, ti map[string]any, respKey string, resp any) func(*sandbox) call {
		return func(s *sandbox) call {
			d := map[string]any{"session_id": "s1", "tool_name": tool, "tool_input": ti}
			if respKey != "" {
				d[respKey] = resp
			}
			return call{hook: "post-work-complete-handoff-mark", env: map[string]string{"CLAUDE_PROJECT_DIR": R(s)}, input: jsonInput(d)}
		}
	}
	bash := func(cmd string, resp any) func(*sandbox) call {
		return ev("Bash", map[string]any{"command": cmd}, "tool_response", resp)
	}
	mcp := func(id, status string, respKey string, resp any) func(*sandbox) call {
		return ev("mcp__im__set_status", map[string]any{"id": id, "status": status}, respKey, resp)
	}
	clear := func(s *sandbox) { os.RemoveAll(R(s, ".claude", "handoff-pending.d")) }
	wantRecord := func(want ...string) func(*testing.T, *sandbox, got) { return wantRecordLines(pend, want...) }
	wantNothing := all(wantMark("QUIET"), wantExists(pend, false))

	// サーバの拒否の文面は実際の対訳表から作る（手で写すと、文面が変わったときに黙って別物を試すことになる）
	reject := func(lang i18n.Lang, id string) string {
		return i18n.T(lang, "domain.rules.acceptance_required_on_close", "id", id, "status", "Done",
			"command", "looptrack issue edit "+id)
	}
	cliErr := func(lang i18n.Lang, id string) string {
		return i18n.T(lang, "cli.err.prefix", "message", reject(lang, id))
	}
	ok := func(id, to string) string { return id + ": In Progress → " + to }
	mcpText := func(text string) []any { return []any{map[string]any{"type": "text", "text": text}} }

	scenario{name: "拒否された完了は積まず、成功した完了は積む", setup: setup, steps: []step{
		// 拒否（積まない）
		{name: "拒否された CLI の close（受け入れ条件が雛形のまま）は積まない", do: clear,
			mk: bash("looptrack issue close TST-0410 --comment ok", cliErr(i18n.JA, "TST-0410")), want: wantNothing},
		{name: `拒否された CLI の status <ID> "Done" は積まない`, do: clear,
			mk: bash(`looptrack issue status TST-0410 "Done"`, cliErr(i18n.JA, "TST-0410")), want: wantNothing},
		{name: "拒否された CLI の close（英語の Error: ）は積まない", do: clear,
			mk: bash("looptrack issue close TST-0410", cliErr(i18n.EN, "TST-0410")), want: wantNothing},
		{name: "Claude Code の Bash の形（stderr に拒否）も積まない", do: clear,
			mk: bash("looptrack issue close TST-0410", map[string]any{"stdout": "", "stderr": cliErr(i18n.JA, "TST-0410"),
				"interrupted": false, "isImage": false}), want: wantNothing},
		{name: "非 0 の終了を文字列で渡す形（Exit code 1）も積まない", do: clear,
			mk: bash("looptrack issue close TST-0410", "Exit code 1\n"+cliErr(i18n.JA, "TST-0410")), want: wantNothing},
		{name: "構造で失敗を示す応答（isError）は積まない", do: clear,
			mk:   bash("looptrack issue close TST-0410", map[string]any{"content": mcpText(reject(i18n.JA, "TST-0410")), "isError": true}),
			want: wantNothing},
		{name: "拒否された MCP の set_status（Claude Code の content の配列）は積まない", do: clear,
			mk: mcp("TST-0410", "Done", "tool_response", mcpText(reject(i18n.JA, "TST-0410"))), want: wantNothing},
		{name: "拒否された MCP の set_status（isError: true）は積まない", do: clear,
			mk:   mcp("TST-0410", "Done", "tool_response", map[string]any{"content": mcpText(reject(i18n.JA, "TST-0410")), "isError": true}),
			want: wantNothing},
		{name: "拒否された MCP の set_status（Copilot の result_type: failure）は積まない", do: clear,
			mk:   mcp("TST-0410", "Canceled", "tool_result", map[string]any{"result_type": "failure", "text_result_for_llm": reject(i18n.JA, "TST-0410")}),
			want: wantNothing},
		// 対照（従来どおり積む）
		{name: "対照: 成功した CLI の close は積む", do: clear,
			mk: bash("looptrack issue close TST-0410 --comment ok", ok("TST-0410", "Done")), want: all(wantMark("OUT"), wantRecord("issue.close\tTST-0410"))},
		{name: "対照: 成功した CLI の status Canceled は積む", do: clear,
			mk:   bash("looptrack issue status TST-0410 Canceled", ok("TST-0410", "Canceled")+"\nTST-0410 にコメントを追加しました"),
			want: all(wantMark("OUT"), wantRecord("issue.status\tCanceled\tTST-0410"))},
		{name: "対照: 成功した MCP の set_status は積む", do: clear,
			mk: mcp("TST-0410", "Done", "tool_response", mcpText(ok("TST-0410", "Done"))), want: all(wantMark("OUT"), wantRecord("issue.status\tDone\tTST-0410"))},
		{name: "対照: 応答が空の CLI の close は積む（判定できないので従来どおり）", do: clear,
			mk: bash("looptrack issue close TST-0410", ""), want: all(wantMark("OUT"), wantRecord("issue.close\tTST-0410"))},
		{name: "対照: 応答の無い CLI の close は積む", do: clear,
			mk: ev("Bash", map[string]any{"command": "looptrack issue close TST-0410"}, "", nil), want: all(wantMark("OUT"), wantRecord("issue.close\tTST-0410"))},
		{name: "対照: 応答が空の MCP の set_status は積む", do: clear,
			mk: mcp("TST-0410", "Done", "tool_response", ""), want: all(wantMark("OUT"), wantRecord("issue.status\tDone\tTST-0410"))},
		{name: "対照: 失敗の印の無い出力（リダイレクトで捨てた等）の CLI の close は積む", do: clear,
			mk: bash("looptrack issue close TST-0410 > /dev/null && echo ok", "ok"), want: all(wantMark("OUT"), wantRecord("issue.close\tTST-0410"))},
		// 1 回の呼び出しに成功と拒否が混ざる
		{name: "拒否された方だけを落とし、同じ呼び出しの成功した方は積む", do: clear,
			mk:   bash("looptrack issue close TST-0410; looptrack issue close TST-0411", cliErr(i18n.JA, "TST-0410")+"\n"+ok("TST-0411", "Done")),
			want: all(wantMark("OUT"), wantRecord("issue.close\tTST-0411"))},
		{name: "後ろの別のコマンドの失敗は、成功した close を落とさない", do: clear,
			mk:   bash("looptrack issue close TST-0410 && go build ./...", ok("TST-0410", "Done")+"\nError: build failed"),
			want: all(wantMark("OUT"), wantRecord("issue.close\tTST-0410"))},
		{name: "ID の分からない close（$i）は、成功の行があれば積む", do: clear,
			mk:   bash("for i in TST-0410; do looptrack issue close $i; done", ok("TST-0410", "Done")),
			want: all(wantMark("OUT"), wantRecord("issue.close\t?"))},
		{name: "ID の分からない close（$i）も、拒否だけなら積まない", do: clear,
			mk: bash("for i in TST-0410; do looptrack issue close $i; done", cliErr(i18n.JA, "TST-0410")), want: wantNothing},
	}}.run(t)
}
