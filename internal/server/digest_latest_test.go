package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// 最新のセッション要約（DESIGN.md §9-7）が summary の in_progress と next の応答に latest_digest として載り、
// 文面（CLI の summary・next・MCP の project_summary）には別のセッションが書いたものだけが出る。
func TestSummaryAndNextLatestDigest(t *testing.T) {
	l := newLoopsEnv(t)
	l.ed.header["X-Looptrack-Session"] = "s-B"
	a := l.create("引き継ぐ作業", map[string]any{"priority": "P0"})
	l.status(a, "In Progress")
	b := l.create("要約の無い作業", map[string]any{"priority": "P1"})
	l.status(b, "In Progress")
	// 別の端末（セッション s-A）が a に要約を書いた
	l.ed.header["X-Looptrack-Session"] = "s-A"
	l.comment(a, "セッション要約: home-mac\nbranch: feature/x\nhead: ce768dc\nai: claude-code\n\n次の一手: テストを足す")

	summary := func(sess string) map[string]issueJSON {
		t.Helper()
		if sess == "" {
			delete(l.ed.header, "X-Looptrack-Session")
		} else {
			l.ed.header["X-Looptrack-Session"] = sess
		}
		var res summaryRes
		l.ed.json(200, "GET", "/projects/"+l.pr.Slug+"/summary", nil, &res)
		out := map[string]issueJSON{}
		for _, it := range res.InProgress {
			out[it.ID] = it
		}
		return out
	}
	got := summary("s-B")
	d := got[a].LatestDigest
	if d == nil || d.OtherSession == nil || !*d.OtherSession || d.MachineLabel != "home-mac" || d.SessionID != "s-A" ||
		!strings.Contains(d.Excerpt, "次の一手: テストを足す") || d.Age == "" {
		t.Fatalf("summary（s-B）の latest_digest: %+v", d)
	}
	if _, err := time.Parse(time.RFC3339, d.CreatedAt); err != nil {
		t.Errorf("created_at: %q", d.CreatedAt)
	}
	if got[b].LatestDigest != nil {
		t.Errorf("要約の無いイシューに latest_digest: %+v", got[b].LatestDigest)
	}
	// own_session は着手したセッション（s-B）にだけ付き、other_session と同時には付かない（引き継ぎの鮮度ガードが使う）
	if !got[a].OwnSession || got[a].OtherSession || !got[b].OwnSession {
		t.Errorf("summary（s-B）の own_session: a=%+v b=%+v", got[a], got[b])
	}
	if it := summary("s-A")[a]; it.OwnSession || !it.OtherSession {
		t.Errorf("summary（s-A）は着手していないので own_session を付けず other_session を付けるはず: %+v", it)
	}
	if it := summary("")[a]; it.OwnSession || it.OtherSession {
		t.Errorf("セッション ID の無い要求は own_session も other_session も付けないはず: %+v", it)
	}
	if d := summary("s-A")[a].LatestDigest; d == nil || d.OtherSession == nil || *d.OtherSession {
		t.Errorf("同じセッション（s-A）は other_session = false のはず: %+v", d)
	}
	if d := summary("")[a].LatestDigest; d == nil || d.OtherSession != nil {
		t.Errorf("セッション ID の無い要求は other_session を出さないはず: %+v", d)
	}

	// next（s-B）: 着手中の a を返し、別のセッションの要約を応答と文面に載せる
	l.ed.header["X-Looptrack-Session"] = "s-B"
	var r struct {
		Action       string            `json:"action"`
		Issue        *issueJSON        `json:"issue"`
		LatestDigest *latestDigestJSON `json:"latest_digest"`
		Text         string            `json:"text"`
	}
	l.ed.json(200, "POST", "/projects/"+l.pr.Slug+"/next", map[string]any{}, &r)
	if r.Action != "resumed" || r.Issue == nil || r.Issue.ID != a || r.LatestDigest == nil || r.LatestDigest.OtherSession == nil || !*r.LatestDigest.OtherSession {
		t.Fatalf("next（s-B）: %+v / %+v", r, r.LatestDigest)
	}
	note := "※ " + a + " の最新のセッション要約は別のセッションが書いたものです（home-mac・"
	if !strings.Contains(r.Text, note) || !strings.Contains(r.Text, "\n  次の一手: テストを足す\n") {
		t.Errorf("next の文面に要約が無い:\n%s", r.Text)
	}

	// CLI の summary（s-B）: 別のセッションの要約を出す。s-A からは出さない（対照の組）
	cli := newCLIEnv(t, l.e, l.pr.Slug, l.ed.token)
	cli.session = "s-B"
	out := mustCLI(t, cli.run("", "summary"), 0, "summary（s-B）").stdout
	// 抜粋は見出し（branch・head・ai）を除いた本文（見出しは数えず、本文の最後の行まで残る）
	if !strings.Contains(out, note) || !strings.Contains(out, "\n  次の一手: テストを足す\n") || strings.Contains(out, "branch: feature/x") {
		t.Errorf("CLI の summary（s-B）に要約が無い:\n%s", out)
	}
	cli.session = "s-A"
	if out := mustCLI(t, cli.run("", "summary"), 0, "summary（s-A）").stdout; strings.Contains(out, "最新のセッション要約") {
		t.Errorf("同じセッションの要約を出した:\n%s", out)
	}
	// 引き継ぎの鮮度ガード（backend issue）が読む形: `summary --limit 0 --json` の in_progress に own_session と latest_digest が素通しで載る
	cli.session = "s-B"
	var js struct {
		InProgress []issueJSON `json:"in_progress"`
	}
	if out := mustCLI(t, cli.run("", "summary", "--limit", "0", "--json"), 0, "summary --json（s-B）").stdout; json.Unmarshal([]byte(out), &js) != nil {
		t.Fatalf("summary --json を読めない:\n%s", out)
	}
	var seen bool
	for _, it := range js.InProgress {
		if it.ID == a {
			seen = true
			if !it.OwnSession || it.LatestDigest == nil || it.LatestDigest.OtherSession == nil || !*it.LatestDigest.OtherSession {
				t.Errorf("summary --json（s-B）の %s: %+v / %+v", a, it, it.LatestDigest)
			}
		}
	}
	if !seen {
		t.Errorf("summary --json（s-B）に %s が無い: %+v", a, js.InProgress)
	}

	// MCP の project_summary: 要求のセッションを名乗れば同じ判定（名乗らなければ接続 ID で、比べられないので出さない）
	m := l.e.mcpAs(l.ed.token, map[string]string{"X-Looptrack-Project": l.pr.Slug, "X-Looptrack-Session": "s-B"})
	if text, _ := m.call("project_summary", map[string]any{}, false); !strings.Contains(text, note) {
		t.Errorf("MCP の project_summary（s-B）に要約が無い:\n%s", text)
	}
	m = l.e.mcpAs(l.ed.token, map[string]string{"X-Looptrack-Project": l.pr.Slug})
	if text, _ := m.call("project_summary", map[string]any{}, false); strings.Contains(text, "最新のセッション要約") {
		t.Errorf("比べられないセッションで要約を出した:\n%s", text)
	}
}
