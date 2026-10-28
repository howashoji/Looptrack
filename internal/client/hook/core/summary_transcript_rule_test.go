package core

import (
	"bytes"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/client/cli"
	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/hookio"
)

// SessionStart の注入（looptrack hook summary）と手で打つ looptrack issue summary が、サーバの返したルール
// session_transcripts から同じ会話記録の行を出すこと。注入は同じ printSummary を通るので、片方だけに出ることは無いはず。
// 出る側（ルールが切・変数なし / あり、ルールが入・変数なし）と出ない側（session_transcripts の無い古いサーバ）を同じテストで見る。
func TestSummaryTranscriptRuleSameInHookAndCLI(t *testing.T) {
	// transcriptLines は出力のうち、会話記録の鍵の行（無ければ空）
	transcriptLines := func(text string) []string {
		var out []string
		for _, l := range strings.Split(text, "\n") {
			if strings.Contains(l, "session.transcripts") {
				out = append(out, strings.TrimSpace(l))
			}
		}
		return out
	}
	withRule := func(v string) string { // summaryPlain の末尾の } の前に session_transcripts を足す
		return strings.TrimSuffix(summaryPlain, "}") + `,"session_transcripts":` + v + `}`
	}
	for _, c := range []struct {
		name  string
		body  string
		optIn string
		want  string // 出るべき行の一部（空なら出ない）
	}{
		{"ルールが切・変数なし", withRule("false"), "", "LOOPTRACK_SESSION_TRANSCRIPTS=1 も設定します"},
		{"ルールが切・変数あり", withRule("false"), "1", "会話記録の保存を切にしています"},
		{"ルールが入・変数なし", withRule("true"), "", "LOOPTRACK_SESSION_TRANSCRIPTS=1 を設定します"},
		{"古いサーバ", summaryPlain, "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newSandbox(t)
			s.api.summaryBody, s.api.installBody = c.body, installCurrent
			extra := map[string]string{"LOOPTRACK_SESSION_TRANSCRIPTS": c.optIn}
			g := s.runGo(call{hook: "summary", agent: hookio.ClaudeCode, env: extra,
				input: map[string]any{"hook_event_name": "SessionStart", "session_id": "s1", "source": "startup"}})
			if g.code != 0 || !strings.Contains(g.res.Context, "TST-0002") {
				t.Fatalf("前提が崩れています（SessionStart の要約が出ていない）: exit %d %q", g.code, g.out.Stdout)
			}
			m := s.envMap(call{env: extra})
			delete(m, "TZ") // runGo と同じ（同じプロセスで time.Local を書き換えない）
			var out, errb bytes.Buffer
			if code := cli.Main([]string{"summary"}, cli.IO{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb}, env.FromMap(m)); code != 0 ||
				!strings.Contains(out.String(), "TST-0002") {
				t.Fatalf("前提が崩れています（手で打った要約が出ていない）: exit %d stdout %q stderr %q", code, out.String(), errb.String())
			}
			hook, manual := transcriptLines(g.res.Context), transcriptLines(out.String())
			if strings.Join(hook, "\n") != strings.Join(manual, "\n") {
				t.Fatalf("注入と手で打った要約で行が違います:\nhook   %q\nmanual %q", hook, manual)
			}
			if c.want == "" {
				if len(hook) != 0 {
					t.Fatalf("古いサーバで覚えた値も無ければ出さないはず: %q", hook)
				}
				return
			}
			if len(hook) != 1 || !strings.Contains(hook[0], c.want) {
				t.Fatalf("会話記録の行に %q がありません: %q", c.want, hook)
			}
		})
	}
}
