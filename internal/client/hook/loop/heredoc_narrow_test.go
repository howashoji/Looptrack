package loop

import (
	"strings"
	"testing"
)

// TestHeredocNarrowReading は、シェルがヒアドキュメントの開きと読まない `<<`（ヒアストリング `<<<` の一部・
// 引用符の中・コメントの中）の次の行にある本物のコマンドを、4 つのガードが止めること。
// 以前の読み方はそこを開きと読み、次の行を本文として落として素通りさせていた。
//
// 同じ表に対照を置く。ふつうのヒアドキュメントの本文はこれまでどおりデータとして通すこと（落とす経路が
// 生きていること）と、同じ行を終端の後ろに置けば止まること（ガードがその行を見ていること）。
func TestHeredocNarrowReading(t *testing.T) {
	needGit(t)
	type guard struct {
		hook, line string
		want       func(*testing.T, *sandbox, got)
	}
	guards := []guard{
		{"pre-tool-git-guard", "git reset --hard", wantDeny},
		{"pre-tool-secrets-guard", "cat /tmp/x/.env", wantAsk},
		{"pre-tool-wait-loop-guard", "while true; do sleep 1; done", wantDeny},
		{"pre-tool-scope-guard", "git -C {O} add x", wantAsk},
	}
	setup := func(s *sandbox) {
		for _, d := range []string{"proj", "other"} {
			s.git("init", "-q", s.p(d))
			s.git("-C", s.p(d), "commit", "-q", "--allow-empty", "-m", "init")
		}
	}
	mk := func(g guard, tmpl string) func(*sandbox) call {
		return func(s *sandbox) call {
			line := strings.ReplaceAll(g.line, "{O}", s.p("other"))
			cmd := strings.ReplaceAll(tmpl, "{L}", line)
			return call{hook: g.hook, env: map[string]string{"CLAUDE_PROJECT_DIR": s.p("proj")},
				input: jsonInput(map[string]any{"session_id": "s1", "cwd": s.p("proj"), "hook_event_name": "PreToolUse",
					"tool_name": "Bash", "tool_input": map[string]any{"command": cmd}})}
		}
	}
	const stop, pass = true, false
	cases := []struct {
		name, tmpl string
		stop       bool
	}{
		// 開きと読まない `<<` の次の行（以前の読み方では素通りしていた）
		{"ヒアストリングの後の名前", "cat <<<EOF\n{L}", stop},
		{"ヒアストリングの後の名前（後ろに同じ名前の行）", "cat <<<EOF\n{L}\nEOF", stop},
		{"一重引用符の中の <<EOF", "echo 'a <<EOF'\n{L}", stop},
		{"二重引用符の中の <<EOF", "echo \"a <<EOF\"\n{L}", stop},
		{"コメントの中の <<EOF", "# cat <<EOF\n{L}", stop},
		{"行をまたぐ一重引用符の中の <<EOF", "echo 'a\ncat <<EOF'\n{L}", stop},
		{"ヒアストリングの後ろの本物の開きの終端の後", "cat <<<x <<EOF\nbody\nEOF\n{L}", stop},
		// 以前の読み方を残していることの witness: 狭い読み方だけなら 2 行目を開きと読み直して {L} を落とすが、
		// 以前の読み方を先に見るので止まる（判定は以前より通す側に動かない）
		{"以前の読み方を先に見る", "echo 'a <<EOF'\ncat <<ZZZ\nEOF\n{L}\nZZZ", stop},
		// 据え置き: <<\EOF の本文は以前から止まる（誤って止める形。文書の「誤って止める形」）
		{"据え置き: <<\\EOF の本文", "cat <<\\EOF\n{L}\nEOF", stop},

		// 対照
		{"対照: <<EOF の本文はデータ", "cat <<EOF\n{L}\nEOF", pass},
		{"対照: <<'EOF' の本文はデータ", "cat <<'EOF'\n{L}\nEOF", pass},
		{"対照: コミットメッセージの本文はデータ", "git commit -m \"$(cat <<'EOF'\n{L}\nEOF\n)\"", pass},
		{"対照: 終端の後ろの行は止める", "cat <<EOF\nbody\nEOF\n{L}", stop},
	}
	var steps []step
	for _, c := range cases {
		for _, g := range guards {
			want := wantQuiet
			if c.stop {
				want = g.want
			}
			steps = append(steps, step{name: c.name + " / " + g.hook, mk: mk(g, c.tmpl), want: want})
		}
	}
	scenario{name: "ヒアドキュメントの開きと読まない <<", setup: setup, steps: steps}.run(t)
}
