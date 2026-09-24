package core

// SessionStart の summary のケース（CLI の summary --limit 12 --agent <AI>、Copilot は --hook-json と突き合わせる）。

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/client/cli"
	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/hookio"
)

func sumItem(id, typ, status, prio, title, extra string) string {
	s := `{"id":"` + id + `","project":"tst","title":"` + title + `","type":"` + typ + `","status":"` + status + `","priority":"` + prio +
		`","parent":"","labels":[],"blocked_by":[],"traces":[],"refs":[],"created":"2024-05-01 10:00","updated":"2024-05-02 11:30","closed":false,"version":3`
	if extra != "" {
		s += "," + extra
	}
	return s + "}"
}

var (
	summaryLayered = `{"counts":{"open":3,"open_bugs":1,"ready":1,"in_progress":1,"in_review":1},` +
		`"feedback":{"count":2,"issue_count":1,"issues":[{"id":"TST-0003","first_at":"2024-05-02T01:00:00Z","pending":2,"excerpt":"ボタンが小さい"}]},` +
		`"in_progress":[` + sumItem("TST-0001", "task", "In Progress", "P1", "ログイン画面を作る", `"assignee":"alice"`) + `],` +
		`"in_review":[` + sumItem("TST-0003", "requirement", "In Review", "P2", "認証の要件", `"review_age":"3 日","review_stale":true`) + `],` +
		`"ready":[` + sumItem("TST-0002", "bug", "Todo", "P0", "保存でエラーになる", "") + `],"ready_total":1,` +
		`"usage_missing":{"count":1,"message":"トークン情報の未付与 1 件（TST-0001）"},` +
		`"usage_requests":[{"id":7,"created_at":"2024-05-03T00:00:00Z","requested_by":"carol","period":"2024-04-01〜2024-04-30","command":"looptrack issue usage report --request 7 --json"}]}`
	summaryPlain   = `{"counts":{"open":2,"open_bugs":0},"in_progress":[],"in_review":[],"ready":[` + sumItem("TST-0002", "bug", "Todo", "P0", "保存でエラーになる", "") + `],"ready_total":5}`
	installOld     = `{"state":"outdated","message":"【配布スクリプトの更新】looptrack が古い版です"}`
	installCurrent = `{"state":"current","message":"導入済み（最新）"}`
)

func summaryStep(name string, agent hookio.Agent, c func(*sandbox) call, want func(*testing.T, *sandbox, got)) step {
	return step{name: name, mk: func(s *sandbox) call {
		cl := call{hook: "summary", agent: agent, input: map[string]any{"hook_event_name": "SessionStart", "session_id": "s1", "source": "startup"}}
		if c != nil {
			cl = c(s)
			cl.hook, cl.agent = "summary", agent
		}
		return cl
	}, want: want}
}

func wantContext(needles ...string) func(*testing.T, *sandbox, got) {
	return func(t *testing.T, _ *sandbox, g got) {
		t.Helper()
		for _, n := range needles {
			if !strings.Contains(g.res.Context, n) {
				t.Errorf("additionalContext に「%s」が無い: %q", n, g.out.Stdout)
			}
		}
	}
}

func TestSummary(t *testing.T) {
	setup := func(body, install string) func(*sandbox) {
		return func(s *sandbox) { s.api.summaryBody, s.api.installBody = body, install }
	}
	in := map[string]any{"hook_event_name": "SessionStart", "session_id": "s1", "source": "startup"}
	cases := []scenario{
		{name: "3 層の要約（Claude Code・Codex・Copilot）", setup: setup(summaryLayered, installCurrent), steps: []step{
			summaryStep("claude-code", hookio.ClaudeCode, nil, wantContext("══ ① いまの周", "TST-0001", "トークン情報の未付与", "#7")),
			summaryStep("codex", hookio.Codex, nil, wantContext("TST-0002")),
			summaryStep("copilot", hookio.Copilot, func(*sandbox) call { return call{bare: true, input: in} }, func(t *testing.T, s *sandbox, g got) {
				wantContext("TST-0003")(t, s, g)
				if !strings.Contains(g.out.Stdout, `"additionalContext":`) || !strings.Contains(g.out.Stdout, `"hookSpecificOutput":{"hookEventName":"SessionStart"`) {
					t.Errorf("トップレベルと hookSpecificOutput の両方に置くはず: %s", g.out.Stdout)
				}
			}),
		}},
		{name: "古いサーバの要約・導入が古い", setup: setup(summaryPlain, installOld), steps: []step{
			summaryStep("claude-code", hookio.ClaudeCode, nil, wantContext("レビュー待ち（In Review）", "上位 5 / 全 5 件", "【配布スクリプトの更新】")),
			summaryStep("--limit 3", hookio.ClaudeCode, func(*sandbox) call { return call{args: []string{"--limit", "3"}, input: in} }, wantContext("上位 3 / 全 5 件")),
		}},
		{name: "何も出さない条件", setup: setup(summaryLayered, installCurrent), steps: []step{
			summaryStep("トークンなし", hookio.ClaudeCode, func(*sandbox) call { return call{noToken: true, input: in} }, wantQuiet),
			summaryStep("繋がらない", hookio.ClaudeCode, func(*sandbox) call { return call{downAPI: true, input: in} }, wantQuiet),
			summaryStep("API なし", hookio.ClaudeCode, func(*sandbox) call { return call{noAPI: true, input: in} }, wantQuiet),
			summaryStep("Copilot が Claude Code の配線を起動", hookio.ClaudeCode, func(*sandbox) call {
				return call{bare: true, env: map[string]string{"COPILOT_CLI": "1"}, input: in}
			}, wantQuiet),
		}},
		{name: "Copilot 向けの配線は Copilot から起動されても出す", setup: setup(summaryLayered, installCurrent), steps: []step{
			summaryStep("copilot", hookio.Copilot, func(*sandbox) call { return call{bare: true, env: map[string]string{"COPILOT_CLI": "1"}, input: in} },
				wantContext("TST-0001")),
		}},
	}
	for _, sc := range cases {
		sc.run(t)
	}
}

// TestSummaryWithoutServerDoesNotBlockSessionStart は、サーバの設定が無いときに hook 側が SessionStart を止めないことの回帰。
// サーバの設定が無いとき、CLI（`looptrack issue summary`）は list / ready / show と同じ案内を出して非 0 で終えるが、
// hook（`looptrack hook summary`）は SessionStart を止めない（何も出さずに exit 0）。
// 配線に --agent が無い（推測した）ときは CLI が非 0 で終えるので、その終了コードを捨てる経路も一緒に確かめる。
func TestSummaryWithoutServerDoesNotBlockSessionStart(t *testing.T) {
	s := newSandbox(t)
	vars := env.FromMap(s.envMap(call{noAPI: true}))
	e := &Env{Vars: &vars, Getwd: func() string { return s.proj }, Sleep: func(time.Duration) {}}
	in := `{"hook_event_name":"SessionStart","session_id":"s1","source":"startup"}`
	quietRun(t, "summary", []string{"--agent", "claude-code"}, in, e, 0) // 配線どおり（CLI 自身が黙る）
	quietRun(t, "summary", nil, in, e, 0)                                // --agent が無い配線（CLI は非 0・hook が捨てる）

	// 手で打った経路は案内を出して非 0（hook が捨てているだけで、CLI が黙っているのではない）
	var out, errOut bytes.Buffer
	code := cli.Main([]string{"summary"}, cli.IO{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errOut}, vars)
	if code == 0 || !strings.Contains(errOut.String(), "イシュー管理サーバの URL") {
		t.Errorf("手で打った summary は案内を出して非 0 で終えるはず: exit %d stdout %q stderr %q", code, out.String(), errOut.String())
	}
}
