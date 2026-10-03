package loop

import (
	"strings"
	"testing"
)

// TestGuardsJudgeLongCommandsWithinDeadline は、長いコマンド（60 KB 前後）の末尾に置いた危ない行を、
// 4 つのガードが打ち切り（4 秒）の内に判定することを確かめる。
//
// 打ち切りを越えたガードは判定を出さずに通す（fail-open）。入れ子のシェルをほどく段が入力の長さに対して 2 次だった
// ころは、20 KB 前後で待ちループと scope のガードが打ち切りに届き、末尾の危ない行を黙って通していた。
// 2 次に戻ると、ここでは判定が出ずに落ちる。いまは 60 KB で 0.1 秒前後なので、遅い CI の機械でも余裕がある。
func TestGuardsJudgeLongCommandsWithinDeadline(t *testing.T) {
	needGit(t)
	s := newSandbox(t)
	for _, d := range []string{"proj", "other"} {
		s.git("init", "-q", s.p(d))
		s.git("-C", s.p(d), "commit", "-q", "--allow-empty", "-m", "init")
	}
	guards := []struct {
		hook, line string
		want       func(*testing.T, *sandbox, got)
	}{
		{"pre-tool-git-guard", "git reset --hard", wantDeny},
		{"pre-tool-secrets-guard", "cat /tmp/x/.env", wantAsk},
		{"pre-tool-wait-loop-guard", "while true; do sleep 1; done", wantDeny},
		{"pre-tool-scope-guard", "git -C " + s.p("other") + " add x", wantAsk},
	}
	// 引用符・コメント・入れ子のシェルの多い行（打ち切りに届いていた形）
	units := []string{
		"echo $'a\\'b' # it's\n",
		"bash -c 'echo a; echo b'\n",
		"echo 'it'\"'\"'s here'\n",
		"echo \"v=$(date +%s) it's\"\n",
	}
	for _, u := range units {
		body := strings.Repeat(u, 60000/len(u))
		for _, g := range guards {
			cmd := body + g.line
			c := call{hook: g.hook, env: map[string]string{"CLAUDE_PROJECT_DIR": s.p("proj")},
				input: jsonInput(map[string]any{"session_id": "s1", "cwd": s.p("proj"), "hook_event_name": "PreToolUse",
					"tool_name": "Bash", "tool_input": map[string]any{"command": cmd}})}
			r := s.runGo(c)
			t.Logf("%s %q×%d（%d バイト）: %v", g.hook, u, len(body)/len(u), len(cmd), r.elapsed)
			// 判定が出ないときは、打ち切りを越えて通したか（elapsed が limit に届く）を添えて落とす
			if r.quiet() {
				t.Errorf("%s: %d バイトのコマンドの末尾の %q を判定しなかった（%v・打ち切り %v）。ほどく段が 2 次に戻っていないか",
					g.hook, len(cmd), g.line, r.elapsed, r.limit)
				continue
			}
			g.want(t, s, r)
		}
	}
}
