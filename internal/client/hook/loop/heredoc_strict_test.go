package loop

import (
	"strings"
	"sync/atomic"
	"testing"
)

// TestStrictReadings は、以前の読み方と狭い読み方のどちらでも素通りしていた形の本物のコマンドを、追加の読み方
// （3 つ目のヒアドキュメントの読み方・$'…' とコメントを直す読み方・本文を落としてからほどく読み方・scope の広い区切り）で
// 4 つのガードが止めること。どの形も、bash と zsh で {L} の行が実行される。
//
// 同じ表に対照を置く。ふつうのヒアドキュメントの本文と、シェル以外・遠隔のシェルに渡す本文はデータとして通すこと
// （追加の読み方が誤って止める形を増やしていないこと）と、終端の後ろの行は止まること（ガードがその行を見ていること）。
func TestStrictReadings(t *testing.T) {
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
		// 空白を挟まないパイプ・背景・かっこの後ろ（scope のガードが素通りしていた）
		{"空白の無いパイプの後ろ", "echo a|{L}", stop},
		{"空白の無い & の後ろ", "echo a&{L}", stop},
		{"かっこの中", "({L})", stop},
		// $'…' とコメントの中の引用符（git ガードが全体を分解できずに通していた）
		{"$'…' の打ち消した ' の次の行", "echo $'it\\'s' ; echo x\n{L}", stop},
		{"$'…' の中の <<EOF の次の行", "echo $'it\\'s <<EOF'\n{L}", stop},
		{"コメントの中の ' の次の行", "echo x # it's\n{L}", stop},
		{"コメントの中の \" の次の行", "echo x # say \"hi\n{L}", stop},
		// 算術・展開の中の <<（以前の読み方も狭い読み方も開きと読んで次の行を落としていた）
		{"算術の中の <<", "echo $((1<<y))\n{L}\ny", stop},
		{"算術 (( )) の中の <<", "((y=1<<x))\n{L}\nx", stop},
		{"パラメータ展開の中の <<", "echo ${x#<<EOF}\n{L}\nEOF", stop},
		// 本文をシェルに渡す
		{"cat <<EOF | sh の本文", "cat <<EOF | sh\n{L}\nEOF", stop},
		{"bash <<'EOF' の本文", "bash <<'EOF'\n{L}\nEOF", stop},
		{"source /dev/stdin の本文", "source /dev/stdin <<EOF\n{L}\nEOF", stop},
		// 終端の名前をシェルと同じく読む
		{"<<E\"OF\" の終端の後ろ", "cat <<E\"OF\"\nbody\nEOF\n{L}", stop},
		{"終端の名前に \" を含む形の終端の後ろ", "cat <<\"E\\\"F\"\nb\nE\"F\n{L}", stop},
		// 本文やコメントの引用符が入れ子のシェルのほどき方を狂わせていた形と、$'…' で渡す入れ子のシェル
		{"本文の ' の後の bash -c", "cat <<\\EOF\nit's\nEOF\nbash -c '{L}'", stop},
		{"コメントの中の ' の後の bash -c", "git status # don't\nbash -c '{L}'", stop},
		{"bash -c $'…'", "bash -c $'{L}'", stop},
		// 対照（閉じない引用符の後ろはシェルが実行しない）
		{"対照: 閉じない ' の行の後ろ（算術の << の後）", "((y=1<<x))\nit's\nEOF\n{L}", pass},
		{"対照: $'…' の中身に閉じない '", "bash -c $'{L} it\\'s'", pass},

		// 対照
		{"対照: <<EOF の本文はデータ", "cat <<EOF\n{L}\nEOF", pass},
		{"対照: <<'EOF' の本文はデータ", "cat <<'EOF'\n{L}\nEOF", pass},
		{"対照: シェルでないコマンドに渡す本文", "cat <<'EOF' | grep sh\n{L}\nEOF", pass},
		{"対照: 遠隔のシェルに渡す本文", "ssh host 'bash -s' <<'EOF'\n{L}\nEOF", pass},
		{"対照: コメントの中の | sh", "cat <<EOF # | sh\n{L}\nEOF", pass},
		{"対照: コミットメッセージの本文", "git commit -m \"$(cat <<'EOF'\nit's {L}\nEOF\n)\"", pass},
		{"対照: 終端の後ろの行は止める", "cat <<EOF | sh\necho a\nEOF\n{L}", stop},
		{"入れ子のシェルの中で本文をシェルに渡す形の後ろ", "bash -c 'cat <<EOF | sh\n\"\nEOF'\n{L}", stop},
		// シェルは `EOF;` を終端と読まない（後ろの行も本文）
		{"対照: EOF; の後ろの行は本文", "cat <<EOF\nEOF;\n{L}\nEOF", pass},
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
	scenario{name: "追加の読み方で止める形", setup: setup, steps: steps}.run(t)
}

// TestExtraReadingsOnlyAfterBase は、以前の読み方で止まる形では追加の読み方（3 つ目のヒアドキュメントの読み方）を
// 1 度も計算しないこと。追加の読み方は遅いので、先に計算すると打ち切りの時間を越え、以前は止めていた大きな入力が
// 通ってしまう。呼ばれた回数を strictHeredocs の差し替えで数える。
// 対照: 以前の読み方では通り、追加の読み方でだけ止まる形では、追加の読み方が呼ばれて止まる（数える仕掛けが生きている）。
func TestExtraReadingsOnlyAfterBase(t *testing.T) {
	needGit(t)
	var n atomic.Int64
	orig := strictHeredocs
	strictHeredocs = func(s string) string { n.Add(1); return orig(s) }
	t.Cleanup(func() { strictHeredocs = orig })

	guards := []struct {
		hook, line string
		want       func(*testing.T, *sandbox, got)
	}{
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
	var steps []step
	for _, g := range guards {
		for _, c := range []struct {
			name, tmpl string
			extra      bool // 追加の読み方が呼ばれるか
		}{
			{"以前の読み方で止まる", "echo $'a\\'b' # it's\n{L}", false},
			{"対照: 追加の読み方でだけ止まる", "echo $((1<<y))\n{L}\ny", true},
		} {
			steps = append(steps, step{
				name: c.name + " / " + g.hook,
				mk: func(s *sandbox) call {
					n.Store(0)
					cmd := strings.ReplaceAll(c.tmpl, "{L}", strings.ReplaceAll(g.line, "{O}", s.p("other")))
					return call{hook: g.hook, env: map[string]string{"CLAUDE_PROJECT_DIR": s.p("proj")},
						input: jsonInput(map[string]any{"session_id": "s1", "cwd": s.p("proj"), "hook_event_name": "PreToolUse",
							"tool_name": "Bash", "tool_input": map[string]any{"command": cmd}})}
				},
				want: func(t *testing.T, s *sandbox, gt got) {
					g.want(t, s, gt)
					if called := n.Load() > 0; called != c.extra {
						t.Errorf("追加の読み方が呼ばれた: %v（%d 回）、期待 %v", called, n.Load(), c.extra)
					}
				},
			})
		}
	}
	scenario{name: "追加の読み方は以前の読み方の後", setup: setup, steps: steps}.run(t)
}

// TestGitGuardBackslashHeredocBodyIsData は、終端の名前をバックスラッシュで引用したヒアドキュメントの本文に
// コメントの ' があっても、追加の読み方が本文を語に分けて新しく deny にしないこと（以前の読み方はこの形を通す）。
// 対照: 終端の後ろの行は止める。
func TestGitGuardBackslashHeredocBodyIsData(t *testing.T) {
	mk := func(cmd string) func(*sandbox) call {
		return func(s *sandbox) call {
			return call{hook: "pre-tool-git-guard", input: jsonInput(map[string]any{"session_id": "s1", "cwd": s.root,
				"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": map[string]any{"command": cmd}})}
		}
	}
	scenario{name: "<<\\EOF の本文", steps: []step{
		{name: "本文の git とコメントの '", want: wantQuiet,
			mk: mk("cat <<\\EOF > /tmp/x/s.sh\ngit reset --hard origin/main\n# don't run this twice\nEOF")},
		{name: "<<'EOF' の本文", want: wantQuiet,
			mk: mk("cat <<'EOF' > /tmp/x/s.sh\ngit reset --hard origin/main\n# don't run this twice\nEOF")},
		{name: "対照: 終端の後ろの行", want: wantDeny,
			mk: mk("cat <<\\EOF > /tmp/x/s.sh\nbody # don't\nEOF\ngit reset --hard")},
	}}.run(t)
}
