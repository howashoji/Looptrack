package loop

// pre-tool-wait-loop-guard の検査（実プロセスを立てず、Bash の入力を表で与える）。
// 「当たらないこと」のケースは、当たるべきケースと同数以上そろえる（誤検知で deny が常時バイパスされるのを避ける）。

import (
	"strings"
	"testing"
)

func TestPreToolWaitLoopGuard(t *testing.T) {
	run := func(cmd string, env map[string]string) func(*sandbox) call {
		return func(s *sandbox) call {
			return call{hook: "pre-tool-wait-loop-guard", env: env,
				input: jsonInput(map[string]any{"session_id": "s1", "cwd": s.root, "hook_event_name": "PreToolUse",
					"tool_name": "Bash", "tool_input": map[string]any{"command": cmd}})}
		}
	}
	b := func(cmd string) func(*sandbox) call { return run(cmd, nil) }
	deny, quiet := wantDeny, wantQuiet

	scenario{name: "上限の無い待ちループを起動前に止める", steps: []step{
		// ── 止めるべき（until / while + sleep + 上限なし）──────────────────────
		{name: "実例 1: 完了マーカーを待つ（sleep 10）",
			mk: b("until [ -s /tmp/tasks/byfhd467t.output ]; do sleep 10; done"), want: deny},
		{name: "実例 2: 存在しないパスを待つ（sleep 2）",
			mk: b("until test -f /tmp/claude-a902-cwd; do sleep 2; done && cat /tmp/claude-a902-cwd | tail -50"), want: deny},
		{name: "実例 3: CI を待つ（15 時間・外部 API 約 6,400 回）",
			mk: b(`until [ "$(gh api repos/o/r/commits/9d95da5c/check-runs --jq .total_count)" != "0" ]; do sleep 15; done`), want: deny},
		{name: "while true の待ち", mk: b("while true; do sleep 60; done"), want: deny},
		{name: "bash -c の引用符の中も見る", mk: b(`bash -c 'until false; do sleep 5; done'`), want: deny},
		{name: "背景実行でも同じ",
			mk: func(s *sandbox) call {
				return call{hook: "pre-tool-wait-loop-guard",
					input: jsonInput(map[string]any{"session_id": "s1", "cwd": s.root, "hook_event_name": "PreToolUse",
						"tool_name": "Bash", "tool_input": map[string]any{"command": "until [ -f /tmp/x ]; do sleep 3; done", "run_in_background": true}})}
			}, want: deny},

		// ── 通すべき（誤検知させない）────────────────────────────────────────
		// (a) 正当に長く動くもの（ループではない）
		{name: "(a) docker compose up（数時間）", mk: b("docker compose -f deploy/dev/compose.yaml up -d"), want: quiet},
		{name: "(a) dev サーバ", mk: b("npm run dev"), want: quiet},
		{name: "(a) go test ./...（1 分半）", mk: b("go test ./..."), want: quiet},
		// (b) 上限のある待ち
		{name: "(b) 最大回数（seq + break）",
			mk: b("for i in $(seq 1 60); do curl -sf http://127.0.0.1:9/ && break; sleep 10; done"), want: quiet},
		{name: "(b) 数の比較で諦める",
			mk: b("n=0; until curl -sf http://127.0.0.1:9/; do n=$((n+1)); [ $n -ge 60 ] && break; sleep 5; done"), want: quiet},
		{name: "(b) 締切（SECONDS）",
			mk: b("deadline=$((SECONDS+2700)); until [ -f /tmp/x ]; do [ $SECONDS -ge $deadline ] && break; sleep 5; done"), want: quiet},
		{name: "(b) 丸ごと打ち切る（timeout）",
			mk: b(`timeout 600 bash -c 'until test -f /tmp/x; do sleep 5; done'`), want: quiet},
		// (c) 一瞬で終わるループ（sleep が無い）
		{name: "(c) while read", mk: b(`while read -r line; do echo "$line"; done < /tmp/list`), want: quiet},
		{name: "(c) for f in *.go", mk: b("for f in *.go; do gofmt -l \"$f\"; done"), want: quiet},
		// その他
		{name: "引用符の中の文字列（コマンドではない）",
			mk: b(`echo 'until x; do sleep 1; done は禁止'`), want: quiet},
		{name: "単なる sleep（ループではない）", mk: b("sleep 30 && echo done"), want: quiet},
		{name: "Bash 以外のツールは見ない",
			mk: func(s *sandbox) call {
				return call{hook: "pre-tool-wait-loop-guard",
					input: jsonInput(map[string]any{"session_id": "s1", "cwd": s.root, "hook_event_name": "PreToolUse",
						"tool_name": "Write", "tool_input": map[string]any{"file_path": s.p("x.sh"), "content": "until false; do sleep 1; done"}})}
			}, want: quiet},
		{name: "LOOPTRACK_LOOP_WAITLOOP_ALLOW で例外にできる",
			mk: run("until [ -f /tmp/x ]; do sleep 3; done", map[string]string{"LOOPTRACK_LOOP_WAITLOOP_ALLOW": "/tmp/x"}), want: quiet},
	}}.run(t)
}

// TestWaitLoopWrapped は、前置の語（sudo・env・nohup・setsid・time）と eval で包んだ待ちループ。
// 観点ごとに「止める」と「通す」を対にして置く（ほどく処理を広げすぎたときに、通す側が落ちるように）。
func TestWaitLoopWrapped(t *testing.T) {
	b := func(cmd string) func(*sandbox) call {
		return func(s *sandbox) call {
			return call{hook: "pre-tool-wait-loop-guard",
				input: jsonInput(map[string]any{"session_id": "s1", "cwd": s.root, "hook_event_name": "PreToolUse",
					"tool_name": "Bash", "tool_input": map[string]any{"command": cmd}})}
		}
	}
	deny, quiet := wantDeny, wantQuiet
	scenario{name: "包んだ待ちループ", steps: []step{
		// ── 前置の語 + 入れ子のシェル ──
		{name: "止める: sudo sh -c", mk: b(`sudo sh -c 'until false; do sleep 5; done'`), want: deny},
		{name: "止める: env sh -c", mk: b(`env sh -c 'while true; do sleep 1; done'`), want: deny},
		{name: "止める: env FOO=1 sh -c", mk: b(`env FOO=1 sh -c 'while true; do sleep 1; done'`), want: deny},
		{name: "止める: nohup sh -c", mk: b(`nohup sh -c 'until false; do sleep 5; done'`), want: deny},
		{name: "止める: nohup bash -c … &", mk: b(`nohup bash -c 'while true; do sleep 10; done' &`), want: deny},
		{name: "止める: setsid sh -c", mk: b(`setsid sh -c 'until false; do sleep 5; done'`), want: deny},
		{name: "止める: sudo nice -n 10 bash -lc", mk: b(`sudo nice -n 10 bash -lc 'until false; do sleep 5; done'`), want: deny},
		{name: "通す: timeout 60 sh -c（外から打ち切る）", mk: b(`timeout 60 sh -c 'while true; do sleep 1; done'`), want: quiet},
		{name: "通す: sudo sh -c + seq", mk: b(`sudo sh -c 'for i in $(seq 1 10); do sleep 1; done'`), want: quiet},
		{name: "通す: nohup bash -c + 数の比較 &",
			mk: b(`nohup bash -c 'n=0; while [ $n -lt 10 ]; do sleep 1; n=$((n+1)); done' &`), want: quiet},
		{name: "通す: setsid sh -c + break", mk: b(`setsid sh -c 'until test -f /tmp/x; do [ -f /tmp/y ] && break; sleep 5; done'`), want: quiet},
		// ── 前置の語の直後の until / while（time はループをそのまま測れる）──
		{name: "止める: time while", mk: b(`time while true; do sleep 1; done`), want: deny},
		{name: "通す: time for + seq", mk: b(`time for i in $(seq 1 10); do sleep 1; done`), want: quiet},
		// ── eval ──
		{name: "止める: eval \"…\"", mk: b(`eval "while true; do sleep 1; done"`), want: deny},
		{name: "止める: eval '…'", mk: b(`eval 'until false; do sleep 5; done'`), want: deny},
		{name: "止める: x=1; eval \"…\"", mk: b(`x=1; eval "until false; do sleep 5; done"`), want: deny},
		{name: "通す: eval + 数の比較", mk: b(`eval "while [ \$n -lt 10 ]; do sleep 1; n=\$((n+1)); done"`), want: quiet},
		{name: "通す: eval + SECONDS", mk: b(`eval 'end=$((SECONDS+60)); until [ $SECONDS -ge $end ]; do sleep 1; done'`), want: quiet},
		{name: "通す: echo の引数の eval は外さない", mk: b(`echo "eval 'until false; do sleep 5; done' は禁止"`), want: quiet},
		// ── 前置の語でない語の直後は、コマンドの位置ではない（一覧を広げすぎない）──
		{name: "通す: echo sh -c '…'（echo の引数）", mk: b(`echo sh -c 'while true; do sleep 1; done'`), want: quiet},
		{name: "通す: git grep while && sleep", mk: b(`git grep -n while . && sleep 1`), want: quiet},
		{name: "通す: printf until && sleep", mk: b(`printf until && sleep 1 && echo done`), want: quiet},
		// ── ほどく段は秘密のガード・git ガードと同じ hookcmd.UnwrapNestedShell（コマンドの位置だけ）──
		{name: "止める: /bin/bash -c（実行ファイルのパス）", mk: b(`/bin/bash -c 'while true; do sleep 1; done'`), want: deny},
		{name: "止める: sudo -u deploy bash -c", mk: b(`sudo -u deploy bash -c 'while true; do sleep 1; done'`), want: deny},
		{name: "止める: 2 段の入れ子", mk: b(`bash -c "bash -c 'while true; do sleep 1; done'"`), want: deny},
		{name: "止める: 打ち消した ' の後ろ", mk: b(`echo don\'t ; bash -c 'while true; do sleep 1; done'`), want: deny},
		{name: "止める: コメントの中の ' の後ろ", mk: b("echo ok # don't\nbash -c 'while true; do sleep 1; done'"), want: deny},
		{name: "通す: コメントの中の bash -c", mk: b(`echo x # bash -c 'while true; do sleep 1; done'`), want: quiet},
		{name: "通す: docker run img bash -c（コンテナで走る）", mk: b(`docker run img bash -c 'while true; do sleep 1; done'`), want: quiet},
		{name: "通す: nohup ssh host bash -c（遠隔で走る）", mk: b(`nohup ssh host bash -c 'while true; do sleep 1; done'`), want: quiet},
		// ── 素の形（退行が無いこと）──
		{name: "止める: 素の while true", mk: b(`while true; do sleep 1; done`), want: deny},
		{name: "止める: 素の until false", mk: b(`until false; do sleep 5; done`), want: deny},
		{name: "通す: 素の for + seq", mk: b(`for i in $(seq 1 10); do sleep 1; done`), want: quiet},
		{name: "通す: 素の数の比較", mk: b(`while [ $n -lt 10 ]; do sleep 1; n=$((n+1)); done`), want: quiet},
	}}.run(t)
}

// TestWaitLoopMissingDir は「待つ先の親ディレクトリが無い」ときの注意（**止めない**）。
// まだ無いファイルを待つこと自体は完了マーカー待ちの正しい使い方なので、deny にはしない。
func TestWaitLoopMissingDir(t *testing.T) {
	mk := func(cmd string) func(*sandbox) call {
		return func(s *sandbox) call {
			return call{hook: "pre-tool-wait-loop-guard",
				input: jsonInput(map[string]any{"session_id": "s1", "cwd": s.p("proj"), "hook_event_name": "PreToolUse",
					"tool_name": "Bash", "tool_input": map[string]any{"command": cmd}})}
		}
	}
	bounded := func(target string) string {
		return "n=0; until [ -s " + target + " ]; do n=$((n+1)); [ $n -ge 60 ] && break; sleep 5; done"
	}
	scenario{
		name:  "待つ先の置き場が無いときは、止めずに注意する",
		setup: func(s *sandbox) { s.mkdir("proj", "tasks") },
		steps: []step{
			{name: "親ディレクトリが無い（絶対パス）",
				mk:   mk("n=0; until test -f /tmp/looptrack-no-such-dir-0302/marker; do n=$((n+1)); [ $n -ge 60 ] && break; sleep 5; done"),
				want: wantContext("PreToolUse", []string{"looptrack-no-such-dir-0302", "親ディレクトリ"}, nil)},
			{name: "親ディレクトリが無い（相対パス・cwd から解く）",
				mk:   mk(bounded("out/tasks/x.output")),
				want: wantContext("PreToolUse", []string{"out/tasks/x.output"}, nil)},
			// 注意しないもの
			{name: "親ディレクトリがある（まだ無いファイルを待つのは正しい使い方）",
				mk: mk(bounded("tasks/x.output")), want: wantQuiet},
			{name: "パスが変数で、実在を確かめられない", mk: mk(bounded("$OUT/x.output")), want: wantQuiet},
			{name: "待ちループではない（sleep が無い）",
				mk: mk("test -f /tmp/looptrack-no-such-dir-0302/marker && echo yes"), want: wantQuiet},
			{name: "上限が無いほうが先（注意ではなく deny）",
				mk: mk("until test -f /tmp/looptrack-no-such-dir-0302/marker; do sleep 5; done"), want: wantDeny},
		}}.run(t)
}

// TestUnboundedWaitLoopReason は deny の文面に、通る形への書き直し方が入っていること（誤検知しても作業が止まらない出口）。
func TestUnboundedWaitLoopReason(t *testing.T) {
	scenario{name: "deny の文面に書き直し方がある", steps: []step{
		{name: "最大回数・締切・timeout・例外の環境変数",
			mk: func(s *sandbox) call {
				return call{hook: "pre-tool-wait-loop-guard",
					input: jsonInput(map[string]any{"session_id": "s1", "cwd": s.root, "hook_event_name": "PreToolUse",
						"tool_name": "Bash", "tool_input": map[string]any{"command": "until [ -f /tmp/x ]; do sleep 3; done"}})}
			},
			want: func(t *testing.T, _ *sandbox, g got) {
				t.Helper()
				if g.res.Deny == "" {
					t.Fatalf("deny のはず: %q%s", g.out.Stdout, g.why())
				}
				for _, w := range []string{"seq 1 60", "SECONDS", "timeout 600", "LOOPTRACK_LOOP_WAITLOOP_ALLOW"} {
					if !strings.Contains(g.res.Deny, w) {
						t.Errorf("「%s」が無い:\n%s", w, g.res.Deny)
					}
				}
			}},
	}}.run(t)
}
