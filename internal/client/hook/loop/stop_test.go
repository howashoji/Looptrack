package loop

// kit/loop/verify/verify-tool-markup-guard.sh（12 ケース）と verify-runaway-background-process.sh（19 ケース）の移植。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/client/proc"
	"github.com/howashoji/looptrack/internal/hookio"
)

// lt は「<」（このファイルに素のタグの形を並べない。読んだ AI の出力に紛れないように）。
const lt = "<"

// allowKind は Stop の hook の結果（block / allow / output）。
func allowKind(g got) string {
	switch {
	case g.json["decision"] == "block":
		return "block"
	case g.quiet():
		return "allow"
	}
	return "output"
}

func wantAllow(k string) func(*testing.T, *sandbox, got) {
	return func(t *testing.T, _ *sandbox, g got) {
		t.Helper()
		if allowKind(g) != k {
			t.Errorf("期待 %s / 実際 %s: %s", k, allowKind(g), g.out.Stdout)
		}
	}
}

// transcript は user の行と、text と tool_use を持つ assistant の行の会話記録。
func transcript(text string, tools int) string {
	var content []any
	if text != "" {
		content = append(content, map[string]any{"type": "text", "text": text})
	}
	for i := 0; i < tools; i++ {
		content = append(content, map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{}})
	}
	if content == nil {
		content = []any{}
	}
	u, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"content": "作業して"}})
	a, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"content": content}})
	return string(u) + "\n" + string(a) + "\n"
}

func TestToolMarkupGuard(t *testing.T) {
	caseT := func(text string, tools int, active bool, env ...string) (func(*sandbox), func(*sandbox) call) {
		do := func(s *sandbox) { s.write(s.p("t.jsonl"), transcript(text, tools)) }
		mk := func(s *sandbox) call {
			m := map[string]string{}
			for i := 0; i+1 < len(env); i += 2 {
				m[env[i]] = env[i+1]
			}
			return call{hook: "stop-tool-markup-guard", env: m,
				input: `{"transcript_path":` + jstr(s.p("t.jsonl")) + `,"stop_hook_active":` + map[bool]string{true: "true", false: "false"}[active] + `}`}
		}
		return do, mk
	}
	mkStep := func(name, want, text string, tools int, active bool) step {
		do, mk := caseT(text, tools, active)
		return step{name: name, do: do, mk: mk, want: wantAllow(want)}
	}
	trFile := func(name string) func(*sandbox) call {
		return func(s *sandbox) call {
			return call{hook: "stop-tool-markup-guard", input: `{"transcript_path":` + jstr(s.p(name)) + `,"stop_hook_active":false}`}
		}
	}
	nbDo, nbMk := caseT(lt+`invoke name="Bash">`, 0, false, "LOOPTRACK_LOOP_NO_BLOCK", "1")
	scenario{name: "未パースの markup", steps: []step{
		// 差し戻すべき（素の markup が残り、tool_use が無い）
		mkStep("素の invoke 開始タグ", "block", "確認します。\n"+lt+`invoke name="Bash">`+"\n"+lt+`parameter name="command">ls`+lt+"/parameter>", 0, false),
		mkStep("素の parameter タグだけ", "block", lt+`parameter name="file_path">/tmp/x`+lt+"/parameter>", 0, false),
		mkStep("先頭の count の直後の invoke", "block", "count\n"+lt+`invoke name="Read">`, 0, false),
		mkStep("名前空間付きでもテキストに残った", "block", lt+"antml"+`:invoke name="Bash">`, 0, false),
		// 通すべき
		mkStep("tool_use がある（書式ミスではない）", "allow", lt+`invoke name="Bash">`, 1, false),
		mkStep("散文での言及（< が無い）", "allow", "invoke タグと parameter タグの書式を確認しました。", 0, false),
		mkStep("普通の返答", "allow", "完了しました。差分はありません。", 0, false),
		mkStep("stop_hook_active（無限ループ防止）", "allow", lt+`invoke name="Bash">`, 0, true),
		mkStep("テキストが空", "allow", "", 0, false),
		{name: "会話記録が無い", mk: trFile("no-such.jsonl"), want: wantAllow("allow")},
		{name: "assistant の行が無い", do: func(s *sandbox) { s.write(s.p("u.jsonl"), `{"type":"user","message":{"content":"x"}}`+"\n") },
			mk: trFile("u.jsonl"), want: wantAllow("allow")},
		// LOOPTRACK_LOOP_NO_BLOCK=1（差し戻さず知らせるだけ。Codex 向け）
		{name: "LOOPTRACK_LOOP_NO_BLOCK=1 は systemMessage で知らせ、decision を出さない", do: nbDo, mk: nbMk, want: wantSystemMessage("書式ミス")},
	}}.run(t)
}

func TestRunawayBackgroundProcess(t *testing.T) {
	const parent = "99999"
	psCase := func(lines []string, env ...string) (func(*sandbox), func(*sandbox) call) {
		do := func(s *sandbox) {
			text := strings.Join(lines, "\n")
			if len(lines) > 0 {
				text += "\n"
			}
			s.write(s.p("ps.txt"), text)
		}
		mk := func(s *sandbox) call {
			m := map[string]string{"LOOPTRACK_LOOP_RUNAWAY_PARENT_PID": parent, "LOOPTRACK_LOOP_RUNAWAY_PS_FILE": s.p("ps.txt")}
			for i := 0; i+1 < len(env); i += 2 {
				m[env[i]] = env[i+1]
			}
			return call{hook: "stop-runaway-background-process", env: m, input: "{}"}
		}
		return do, mk
	}
	st := func(name, want string, lines []string, env ...string) step {
		do, mk := psCase(lines, env...)
		return step{name: name, do: do, mk: mk, want: wantAllow(want)}
	}
	snap := func(s string) string {
		return "/bin/zsh -c source /tmp/x/.claude/shell-snapshots/snapshot-zsh-1.sh 2>/dev/null || true && " + s
	}
	long := " 22222 " + parent + " 03:00:00 " + snap("eval 'tail -f logs/app.log'")
	nbDo, nbMk := psCase([]string{long}, "LOOPTRACK_LOOP_NO_BLOCK", "1")
	scenario{name: "終わらない子プロセス", steps: []step{
		// 検知すべき
		st("実例: 無限ポーリング 15 時間", "block", []string{" 30451 " + parent + " 15:26:10 " + snap(`eval 'until [ "$(gh api repos/o/r/commits/9d95da5c/check-runs --jq .total_count)" != "0" ]; do sleep 15; done'`)}),
		st("実例: もう 1 本（sleep 20）", "block", []string{" 48962 " + parent + " 15:14:51 " + snap(`eval 'until [ "$(gh api .../check-runs --jq .total_count)" != "0" ]; do sleep 20; done'`)}),
		st("閾値をちょうど超えた（31 分）", "block", []string{" 12345 " + parent + " 00:31:00 " + snap("eval 'sleep 3000'")}),
		st("日をまたぐ表記（1-02:00:00）", "block", []string{" 12346 " + parent + " 1-02:00:00 " + snap("eval 'while true; do sleep 60; done'")}),
		st("bash -c のシェルも対象", "block", []string{" 12348 " + parent + " 02:00:00 /bin/bash -c while true; do sleep 60; done"}),
		// 見逃すべき（誤検知させない）
		st("閾値未満（29 分）", "allow", []string{" 12347 " + parent + " 00:29:00 " + snap("eval 'npx jest'")}),
		st("MCP サーバ（17 時間・正当）", "allow", []string{" 91504 " + parent + " 17:06:53 npm exec chrome-devtools-mcp@latest"}),
		st("別の MCP サーバ（17 時間・正当）", "allow", []string{" 91514 " + parent + " 17:06:53 npm exec some-mcp-server --port 9000"}),
		st("dev サーバ（親が別のプロセス）", "allow", []string{" 55555 1 20:00:00 node node_modules/.bin/next dev"}),
		st("他のプロセスの子（親が違う）", "allow", []string{" 55556 4444 20:00:00 " + snap("eval 'until false; do sleep 5; done'")}),
		st("docker（正当）", "allow", []string{" 55557 " + parent + " 20:00:00 docker compose -f docker/compose.yml logs -f"}),
		st("Bash ツールのシェルでない子", "allow", []string{" 55558 " + parent + " 20:00:00 /usr/bin/ruby -run -e httpd"}),
		st("プロセスが 1 件も無い", "allow", nil),
		{name: "stop_hook_active（無限ループ防止）", do: func(s *sandbox) { s.write(s.p("ps.txt"), long+"\n") },
			mk: func(s *sandbox) call {
				return call{hook: "stop-runaway-background-process", env: map[string]string{"LOOPTRACK_LOOP_RUNAWAY_PARENT_PID": parent,
					"LOOPTRACK_LOOP_RUNAWAY_PS_FILE": s.p("ps.txt")}, input: `{"stop_hook_active": true}`}
			},
			want: wantQuiet},
		// 環境変数での調整
		st("既定では長時間の tail -f を検知する", "block", []string{long}),
		st("LOOPTRACK_LOOP_RUNAWAY_ALLOW で除外を足せる", "allow", []string{long}, "LOOPTRACK_LOOP_RUNAWAY_ALLOW", "tail -f logs/"),
		st("LOOPTRACK_LOOP_RUNAWAY_THRESHOLD_MIN で閾値を上げられる", "allow", []string{long}, "LOOPTRACK_LOOP_RUNAWAY_THRESHOLD_MIN", "240"),
		st("旧名 RUNAWAY_THRESHOLD_MIN も読む", "allow", []string{long}, "RUNAWAY_THRESHOLD_MIN", "240"),
		// LOOPTRACK_LOOP_NO_BLOCK=1
		{name: "LOOPTRACK_LOOP_NO_BLOCK=1 は systemMessage で知らせ、decision を出さない", do: nbDo, mk: nbMk, want: wantSystemMessage("22222")},
	}}.run(t)
}

// TestRunawayLoopThreshold は形別の閾値（上限の無い待ちループの形だけ 10 分・ほかは 30 分のまま）。
// 2026-09-20 に作られた 3 本はいずれも 30 分に達する前に人が ps で見つけたもので、この hook では捕まらなかった。
func TestRunawayLoopThreshold(t *testing.T) {
	const parent = "99999"
	psCase := func(lines []string, env ...string) (func(*sandbox), func(*sandbox) call) {
		do := func(s *sandbox) { s.write(s.p("ps.txt"), strings.Join(lines, "\n")+"\n") }
		mk := func(s *sandbox) call {
			m := map[string]string{"LOOPTRACK_LOOP_RUNAWAY_PARENT_PID": parent, "LOOPTRACK_LOOP_RUNAWAY_PS_FILE": s.p("ps.txt")}
			for i := 0; i+1 < len(env); i += 2 {
				m[env[i]] = env[i+1]
			}
			return call{hook: "stop-runaway-background-process", env: m, input: "{}"}
		}
		return do, mk
	}
	st := func(name, want string, lines []string, env ...string) step {
		do, mk := psCase(lines, env...)
		return step{name: name, do: do, mk: mk, want: wantAllow(want)}
	}
	snap := func(s string) string {
		return "/bin/zsh -c source /tmp/x/.claude/shell-snapshots/snapshot-zsh-1.sh 2>/dev/null || true && " + s
	}
	// 12 分 = 既定の閾値 30 分には届かないが、形別の閾値 10 分は超えている
	loop12 := " 26081 " + parent + " 00:12:00 " + snap(`eval 'until test -f /tmp/claude-a902-cwd; do sleep 2; done'`)
	bounded12 := " 26082 " + parent + " 00:12:00 " + snap(`eval 'n=0; until [ -f /tmp/x ]; do n=$((n+1)); [ $n -ge 60 ] && break; sleep 5; done'`)
	scenario{name: "形別の閾値", steps: []step{
		// 捕まえるべき（30 分より前）
		st("上限の無い待ちループは 12 分で捕まえる", "block", []string{loop12}),
		st("実例: 待機対象のパスが存在しない待ち（7 分経過時点では鳴らない）", "allow",
			[]string{" 26081 " + parent + " 00:07:00 " + snap(`eval 'until test -f /tmp/claude-a902-cwd; do sleep 2; done'`)}),
		// 通すべき（形に当たらないものは 30 分のまま）
		st("前置の語で包んだ待ちループも 12 分で捕まえる（sudo sh -c）", "block",
			[]string{" 26086 " + parent + " 00:12:00 " + snap(`eval 'sudo sh -c "until false; do sleep 15; done"'`)}),
		st("前置の語で包んだ待ちループも 12 分で捕まえる（nohup sh -c）", "block",
			[]string{" 26087 " + parent + " 00:12:00 " + snap(`eval 'nohup sh -c "while true; do sleep 1; done"'`)}),
		st("上限のある待ちは 12 分では鳴らない", "allow", []string{bounded12}),
		st("前置の語で包んでも、上限のある待ちは 12 分では鳴らない", "allow",
			[]string{" 26088 " + parent + " 00:12:00 " + snap(`eval 'sudo sh -c "for i in \$(seq 1 60); do sleep 15; done"'`)}),
		st("ループでない長時間のコマンドは 30 分のまま（12 分）", "allow",
			[]string{" 26083 " + parent + " 00:12:00 " + snap("eval 'tail -f logs/app.log'")}),
		st("sleep の無いループは当たらない（12 分）", "allow",
			[]string{" 26084 " + parent + " 00:12:00 " + snap(`eval 'while read -r l; do echo "$l"; done < /tmp/list'`)}),
		st("除外の一覧は形より先に効く（docker・12 分）", "allow",
			[]string{" 26085 " + parent + " 00:12:00 " + snap(`eval 'docker compose logs -f; until false; do sleep 1; done'`)}),
		// 環境変数で変えられる
		st("LOOPTRACK_LOOP_RUNAWAY_LOOP_THRESHOLD_MIN で形別の閾値を上げられる", "allow", []string{loop12},
			"LOOPTRACK_LOOP_RUNAWAY_LOOP_THRESHOLD_MIN", "20"),
		st("全体の閾値を下げた指定を、形別の閾値が上書きしない", "block", []string{bounded12},
			"LOOPTRACK_LOOP_RUNAWAY_THRESHOLD_MIN", "5"),
	}}.run(t)
	// SubagentStop の配線（子が終わった直後に同じ本体が動く）
	scenario{name: "SubagentStop にも配線されている", steps: []step{
		{name: "subagent-stop-runaway-background-process",
			do: func(s *sandbox) { s.write(s.p("ps.txt"), loop12+"\n") },
			mk: func(s *sandbox) call {
				return call{hook: "subagent-stop-runaway-background-process",
					env:   map[string]string{"LOOPTRACK_LOOP_RUNAWAY_PARENT_PID": parent, "LOOPTRACK_LOOP_RUNAWAY_PS_FILE": s.p("ps.txt")},
					input: `{"hook_event_name":"SubagentStop"}`}
			},
			want: wantBlock},
	}}.run(t)
}

// TestRunawayProcessTree は ps のファイルを使わず、プロセスの一覧（Env.Procs）から自分の祖先の本体をたどる経路を確かめる
// （bash 版の verify に無いもの。Windows の claude.exe・bash.exe -c の形も）。
func TestRunawayProcessTree(t *testing.T) {
	run := func(procs []proc.Proc, self int) hookio.Result {
		e := &Env{Getenv: func(string) string { return "" }, SelfPID: self,
			Procs: func(context.Context) ([]proc.Proc, error) { return procs, nil }}
		r, err := StopRunawayBackgroundProcess(WithEnv(context.Background(), e), hookio.Event{Agent: hookio.ClaudeCode, Name: hookio.Stop})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	h := time.Hour
	cases := []struct {
		name  string
		procs []proc.Proc
		self  int
		block bool
	}{
		{"unix: 祖先の claude の子の長い bash -c", []proc.Proc{
			{PID: 10, PPID: 1, Command: "/usr/local/bin/claude --resume", Elapsed: 5 * h, ElapsedOK: true},
			{PID: 20, PPID: 10, Command: "/bin/zsh -c hook", Elapsed: time.Second, ElapsedOK: true},
			{PID: 21, PPID: 20, Command: "looptrack hook stop-runaway-background-process", Elapsed: time.Second, ElapsedOK: true},
			{PID: 30, PPID: 10, Command: "/bin/bash -c while true; do sleep 1; done", Elapsed: 2 * h, ElapsedOK: true},
		}, 21, true},
		{"unix: 本体が見つからなければ何もしない", []proc.Proc{
			{PID: 20, PPID: 1, Command: "/bin/zsh", Elapsed: h, ElapsedOK: true},
			{PID: 21, PPID: 20, Command: "looptrack", Elapsed: h, ElapsedOK: true},
			{PID: 30, PPID: 20, Command: "/bin/bash -c sleep 99999", Elapsed: 2 * h, ElapsedOK: true},
		}, 21, false},
		{"windows: claude.exe の子の bash.exe -c", []proc.Proc{
			{PID: 100, PPID: 4, Command: `"C:\Users\u\AppData\Local\claude\claude.exe" --resume`, Elapsed: 5 * h, ElapsedOK: true},
			{PID: 200, PPID: 100, Command: `looptrack.exe hook stop-runaway-background-process`, Elapsed: time.Second, ElapsedOK: true},
			{PID: 300, PPID: 100, Command: `"C:\Program Files\Git\bin\bash.exe" -c "until false; do sleep 5; done"`, Elapsed: 3 * h, ElapsedOK: true},
		}, 200, true},
		{"windows: 経過時間が取れないものは数えない", []proc.Proc{
			{PID: 100, PPID: 4, Command: `C:\claude\claude.exe`, Elapsed: 5 * h, ElapsedOK: true},
			{PID: 200, PPID: 100, Command: `looptrack.exe`, ElapsedOK: true},
			{PID: 300, PPID: 100, Command: `C:\Git\bin\bash.exe -c sleep 99999`},
		}, 200, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := run(c.procs, c.self)
			if (r.Block != "") != c.block {
				t.Errorf("block = %q", r.Block)
			}
		})
	}
}
