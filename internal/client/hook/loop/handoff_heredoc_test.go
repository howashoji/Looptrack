package loop

import (
	"os"
	"testing"
)

// TestHandoffMarkHeredocsAndComments は、ヒアドキュメントの本文とコメントを、シェルと同じ読み方で落とすことの回帰。
//
// 以前は行の中で最初の `<<名前` を開きと読み（引用符・コメント・算術の中の `<<` も）、改行を空白でつないでから
// 語に分けていた。そのため次の形で、実際に実行される close を 1 件も積まなかった（引き継ぎが黙って求められない）。
//
//   - コメントの後ろの行（`git add a.txt  # stage` + 改行 + close）: 1 行につないだ後でコメントが文字列の終わりまで続く
//   - 語の中の `#`（`echo a#b`）の後ろの行: 語の分け方がそこからをコメントと読み、同じく終わりまで続く
//   - `echo $'it\'s'` の後ろの行: 語の分け方が $'…' を知らず、引用符が閉じないと読んで全体を分解できない
//   - `$((1<<y))`・`echo "a <<EOF"` の次の行: シェルが開きと読まない `<<` で、次の行からを本文として落とす
//   - `cat <<EOF | sh` の本文: シェルが実行する本文をデータとして落とす
//
// CRLF の行は、字句解析が終端の名前を `EOF\r` と読んで後ろを全部落とさないよう、先に LF にそろえる。
// handoff.go の commandSegments で、改行の置き換えを `" ; "` に戻すと語の中の `#` の段が、hookcmd.ShlexFriendly を外すと
// $'…' の段が、hookcmd.StripHeredocsStrict を hookcmd.StripHeredocs に替えると行頭のコメント・算術・引用符・`| sh` の段が、
// CRLF をそろえる置き換えを外すと CRLF の段が red になる。
// 末尾の対照は、ふつうのヒアドキュメントの本文とコメントそのものの close を積まないこと（読み方を広げすぎていないこと）を固定する。
func TestHandoffMarkHeredocsAndComments(t *testing.T) {
	needGit(t)
	R := func(s *sandbox, parts ...string) string { return s.p(append([]string{"repo"}, parts...)...) }
	pend := func(s *sandbox) string { return R(s, ".claude", "handoff-pending.d", "s1") }
	setup := func(s *sandbox) {
		s.git("init", "-q", "-b", "main", R(s))
		s.mkdir("repo", ".claude", "memories")
	}
	mark := func(cmd string) func(*sandbox) call {
		return func(s *sandbox) call {
			return call{hook: "post-work-complete-handoff-mark", env: map[string]string{"CLAUDE_PROJECT_DIR": R(s)},
				input: jsonInput(map[string]any{"session_id": "s1", "tool_name": "Bash",
					"tool_input": map[string]any{"command": cmd}, "tool_response": "x"})}
		}
	}
	clear := func(s *sandbox) { os.RemoveAll(R(s, ".claude", "handoff-pending.d")) }
	closed := all(wantMark("OUT"), wantRecordLines(pend, "issue.close\tTST-0001"))
	wantNothing := all(wantMark("QUIET"), wantExists(pend, false))

	steps := []step{
		{name: "行末のコメントの後ろの行の close を積む", do: clear,
			mk: mark("git add a.txt  # stage\nlooptrack issue close TST-0001"), want: closed},
		{name: "行頭のコメント（中に <<EOF）の後ろの行の close を積む", do: clear,
			mk: mark("# cat <<EOF\nlooptrack issue close TST-0001"), want: closed},
		{name: "語の中の # の後ろの行の close を積む", do: clear,
			mk: mark("echo a#b\nlooptrack issue close TST-0001"), want: closed},
		{name: "$'…' の中の打ち消した引用符の後ろの行の close を積む", do: clear,
			mk: mark("echo $'it\\'s'\nlooptrack issue close TST-0001"), want: closed},
		{name: "算術の中の << の次の行の close を積む", do: clear,
			mk: mark("echo $((1<<y))\nlooptrack issue close TST-0001"), want: closed},
		{name: "引用符の中の <<EOF の次の行の close を積む", do: clear,
			mk: mark("echo \"a <<EOF\"\nlooptrack issue close TST-0001"), want: closed},
		{name: "シェルに渡すヒアドキュメント（cat <<EOF | sh）の本文の close を積む", do: clear,
			mk: mark("cat <<EOF | sh\nlooptrack issue close TST-0001\nEOF"), want: closed},
		{name: "CRLF の行のヒアドキュメントの後ろの close を積む", do: clear,
			mk: mark("cat <<EOF\r\nbody\r\nEOF\r\nlooptrack issue close TST-0001"), want: closed},
		// 対照: 積む側に広げすぎていないこと
		{name: "対照: ふつうのヒアドキュメントの本文の close は積まない", do: clear,
			mk: mark("cat <<EOF\nlooptrack issue close TST-0001\nEOF"), want: wantNothing},
		{name: "対照: CRLF の行のヒアドキュメントの本文の close は積まない", do: clear,
			mk: mark("cat <<EOF\r\nlooptrack issue close TST-0001\r\nEOF\r\n"), want: wantNothing},
		{name: "対照: コメントそのものの中の close は積まない", do: clear,
			mk: mark("# looptrack issue close TST-0001\necho done"), want: wantNothing},
	}
	scenario{name: "ヒアドキュメントの本文とコメント", setup: setup, steps: steps}.run(t)
}
