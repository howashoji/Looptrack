package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// 導入セットの loop（設計 DESIGN.md §5-7）: setup ツールの loop の問い・導入済み通知の core / loop 別の比較・
// prompt loop の切り替え・guide の loop 対応。kit/loop の中身に依らないよう fixture の kit/loop に差し替えて試す。

// loopFixture は fixture の kit/loop（hook 2 本・rules 1 本・skill 1 本）。looptrack issue init --loop が受け付ける manifest を持つ
// （hook は looptrack の中にあるので、kit/loop に実体のファイルは無い。名前は looptrack hook の登録名）。
func loopFixture() map[string]string {
	return map[string]string{
		"kit/loop/manifest.json": `{"version": "fixture-1", "entries": [
  {"name": "hooks/session-start-rules", "kind": "hook", "runner": "looptrack", "event": "SessionStart", "matcher": null, "timeout": 5, "order": 10, "codex": null},
  {"name": "hooks/stop-tool-markup-guard", "kind": "hook", "runner": "looptrack", "event": "Stop", "matcher": null, "timeout": 5, "order": 20, "codex": null},
  {"name": "rules/fixture-rules.md", "kind": "rules"},
  {"name": "skills/iterate/SKILL.md", "kind": "skill"}
]}
`,
		"kit/loop/rules/fixture-rules.md":  "# fixture の規律\n",
		"kit/loop/skills/iterate/SKILL.md": "---\nname: iterate\ndescription: fixture\n---\n\n# iterate\n",
	}
}

// withFakeDist は配布ディレクトリに looptrack の偽物（macOS・Linux・Windows の 5 つ）を置く（setup が取得の手順を出せるように）。
func withFakeDist(t *testing.T, e *env) {
	t.Helper()
	e.s.cfg.DistDir = t.TempDir()
	writeDist(t, e.s.cfg.DistDir, map[string]string{
		"looptrack_v1.0.0_darwin_arm64": "da", "looptrack_v1.0.0_darwin_amd64": "dx", "looptrack_v1.0.0_linux_amd64": "lx",
		"looptrack_v1.0.0_linux_arm64": "la", "looptrack_v1.0.0_windows_amd64.exe": "wx",
	})
}

// withLoopKit は配布物の kit/loop を fix に差し替える（nil なら kit/loop の無い配布物）。kit/core はそのまま。
// fix の中身を後から書き換えると配布物の更新になる。
func withLoopKit(t *testing.T, fix map[string]string) {
	t.Helper()
	names, read := kitNames, kitRead
	t.Cleanup(func() { kitNames, kitRead = names, read })
	kitNames = func() []string {
		var out []string
		for _, n := range names() {
			if !strings.HasPrefix(n, "kit/loop/") {
				out = append(out, n)
			}
		}
		for n := range fix {
			out = append(out, n)
		}
		sort.Strings(out)
		return out
	}
	kitRead = func(name string) ([]byte, error) {
		if b, ok := fix[name]; ok {
			return []byte(b), nil
		}
		if strings.HasPrefix(name, "kit/loop/") {
			return nil, fs.ErrNotExist
		}
		return read(name)
	}
}

type loopSetupOut struct {
	Install    installStateJSON `json:"install"`
	Steps      []setupStepJSON  `json:"steps"`
	Text       string           `json:"text"`
	DistURL    string           `json:"dist_url"`
	Ask        string           `json:"ask"`
	LoopAnswer string           `json:"loop_answer"`
}

func loopSetupOf(t *testing.T, data map[string]any) loopSetupOut {
	t.Helper()
	var out loopSetupOut
	b, _ := json.Marshal(data)
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// loopStepOf は loop の手順（問いだけの結果の問い、または答えに合う --loop / --no-loop のコマンド）の位置。無ければ -1。
func loopStepOf(out loopSetupOut) (int, *setupStepJSON) {
	for i := range out.Steps {
		s := &out.Steps[i]
		if s.Text != "" || strings.Contains(s.Command, " --loop") || strings.Contains(s.Command, " --no-loop") {
			return i, s
		}
	}
	return -1, nil
}

// installBody は looptrack の導入済み通知の本文（版は配布ディレクトリが無ければ比べない）。loop が nil なら loop を送らない（古い CLI）。
func installBody(agent, trigger, source string, core string, loop map[string]any) map[string]any {
	b := map[string]any{"agent": agent, "trigger": trigger, "source": source, "files": map[string]string{},
		"client": map[string]any{"version": "v1.0.0", "os": "darwin", "arch": "arm64"}}
	if core != "" {
		b["core"] = map[string]any{"bundle_sha256": core}
	}
	if loop != nil {
		b["loop"] = loop
	}
	return b
}

// TestSetupLoopStep: loop の選択が要るとき、1 回目の setup は問いだけを返し（コマンドなし）、利用者の答え（loop=yes / no）を
// 付けた 2 回目に答えに合うコマンドを 1 つだけ返す。Claude Code・Codex・Copilot で同じ形。declined / installed では問わない。
func TestSetupLoopStep(t *testing.T) {
	fix := loopFixture()
	withLoopKit(t, fix)
	e, _, ed := newAPIEnv(t)
	withFakeDist(t, e)
	hdr := map[string]string{"X-Looptrack-Project": "req"}
	latest, _ := latestDist()
	if !latest.HasLoop || latest.LoopHooks != 2 || latest.LoopRules != 1 || latest.LoopSkills != 1 || latest.Loop == "" || latest.Core == "" {
		t.Fatalf("latestDist: %+v", latest)
	}
	post := func(body map[string]any) installStateJSON {
		t.Helper()
		var st installStateJSON
		ed.json(200, "POST", "/projects/req/install", body, &st)
		return st
	}
	// askThenAnswer は 1 回目（問いだけ）を確かめ、yes / no を付けた 2 回目の結果を返す
	askThenAnswer := func(label string, m *mcpClient, args map[string]any) (yes, no loopSetupOut) {
		t.Helper()
		text, data := m.call("setup", args, false)
		ask := loopSetupOf(t, data)
		checkLoopAsk(t, label, text, setupOf(t, data))
		if ask.Steps[0].Title == "" || !strings.HasPrefix(ask.Steps[0].Title, "ループエンジニアリング一式（loop）") || !strings.Contains(ask.Steps[0].Title, "AI は答えを決めない") {
			t.Errorf("%s: 問いの手順の見出し: %q", label, ask.Steps[0].Title)
		}
		with := func(ans string) loopSetupOut {
			a := map[string]any{"loop": ans}
			for k, v := range args {
				a[k] = v
			}
			out := loopSetupOf(t, second(m.call("setup", a, false)))
			if out.Ask != "" || out.LoopAnswer != ans {
				t.Errorf("%s: loop=%s: ask=%q answer=%q", label, ans, out.Ask, out.LoopAnswer)
			}
			// 答えに合うコマンドが 1 つだけ。他方の旗・答えごとの 2 通り・問いの文面は無い
			other := map[string]string{"yes": "--no-loop", "no": " --loop"}[ans]
			n := 0
			for _, s := range out.Steps {
				if s.Text != "" || strings.Contains(s.Command, other) {
					t.Errorf("%s: loop=%s の手順に問い・他方の答えがある: %+v", label, ans, s)
				}
				if strings.Contains(s.Command, loopFlag(ans)) {
					n++
				}
			}
			if n != 1 || strings.Contains(out.Text, "を入れますか？") {
				t.Errorf("%s: loop=%s の答えに合うコマンドが %d 個:\n%s", label, ans, n, out.Text)
			}
			return out
		}
		return with("yes"), with("no")
	}

	// 未導入（missing）: 1 回目は問いだけ。答えを付けると、最初の手順が答えの旗を付けた取得 + init の 1 つ
	m := e.mcpAsClient(ed.token, hdr, "claude-code", "2.1.0", "2025-06-18")
	yes, no := askThenAnswer("claude-code・missing", m, map[string]any{})
	fetchWith := func(out loopSetupOut, agent, flag string) string {
		return `"$HOME/.local/bin/looptrack" issue init --project req --url ` + e.srv.URL + "/im --agent " + agent + " --source server --dist '" + out.DistURL + "' " + flag
	}
	if yes.Install.State != "missing" || yes.Steps[0].Who != "ai" || !strings.HasSuffix(yes.Steps[0].Command, fetchWith(yes, "claude-code", "--loop")) ||
		!strings.HasSuffix(no.Steps[0].Command, fetchWith(no, "claude-code", "--no-loop")) || !strings.Contains(yes.Steps[0].Command, "curl -fsSL") {
		t.Errorf("missing の取得 + init: yes=%+v no=%+v", yes.Steps[0], no.Steps[0])
	}
	// 取得 + init の手順は置き換わるので、ほかは login・再起動・確認の 3 つ
	if len(yes.Steps) != 4 || !strings.Contains(yes.Steps[1].Title, "トークン") {
		t.Errorf("missing の手順: %+v", yes.Steps)
	}
	// 入れる答えには hook の承認、入れない答えには辞退の説明（作業ディレクトリごとに問う）
	if !strings.Contains(yes.Text, "loop の hook を承認する") || !strings.Contains(yes.Text, "利用者の答え「loop を入れる」") {
		t.Errorf("yes の本文:\n%s", yes.Text)
	}
	if strings.Contains(no.Text, "以後は問わない") || !strings.Contains(no.Text, "別の作業ディレクトリでは改めて問う") || strings.Contains(no.Text, "loop の hook を承認する") {
		t.Errorf("no の本文（辞退の説明）:\n%s", no.Text)
	}

	// Codex・Copilot も同じ形（--agent codex / copilot）。フックの無い AI（other）には問わない（loop を入れられない）
	cx := e.mcpAsClient(ed.token, hdr, "codex-mcp-client", "1", "")
	if yes, _ := askThenAnswer("codex・missing", cx, map[string]any{}); !strings.Contains(yes.Steps[0].Command, "--agent codex") {
		t.Errorf("Codex: %+v", yes.Steps[0])
	}
	cp := e.mcpAsClient(ed.token, hdr, "github-copilot-developer", "1.0.86", "2025-06-18")
	if yes, no := askThenAnswer("copilot・missing", cp, map[string]any{}); !strings.Contains(yes.Steps[0].Command, "--agent copilot") ||
		!strings.HasPrefix(no.Steps[0].Command, "export LOOPTRACK_API_URL=") {
		t.Errorf("Copilot: %+v", yes.Steps[0])
	}
	ot := e.mcpAsClient(ed.token, hdr, "cursor", "1", "")
	if out := loopSetupOf(t, second(ot.call("setup", map[string]any{}, false))); out.Ask != "" || len(out.Steps) < 2 {
		t.Errorf("other に loop の問いが出た: %+v", out)
	}
	if i, _ := loopStepOf(loopSetupOf(t, second(ot.call("setup", map[string]any{"loop": "yes"}, false)))); i >= 0 {
		t.Errorf("other に loop の手順が出た")
	}
	// 答えの値の検査
	m.call("setup", map[string]any{"loop": "maybe"}, true)

	// フックの通知（loop 未選択・core 最新）→ current でも 1 回目は問いだけ。答えると init の 1 つ（入れ方の分からない looptrack
	// の導入なので PATH の looptrack で）
	post(installBody("claude-code", "hook", "server", latest.Core, map[string]any{"installed": false}))
	yes, _ = askThenAnswer("claude-code・current・未選択", m, map[string]any{})
	if i, step := loopStepOf(yes); yes.Install.State != "current" || i != 0 || step.Who != "ai" ||
		step.Command != "looptrack issue init --project req --agent claude-code --url "+e.srv.URL+"/im --source server --dist '"+yes.DistURL+"' --loop" {
		t.Errorf("current・未選択: state=%s i=%d %+v", yes.Install.State, i, yes.Steps)
	}
	if !strings.Contains(yes.Install.Message, "loop）: 未選択") {
		t.Errorf("導入済みの文に loop の状態が無い: %s", yes.Install.Message)
	}
	// 古い CLI（loop を送らない）も未選択として問う
	post(installBody("claude-code", "hook", "server", "", nil))
	askThenAnswer("claude-code・古い CLI", m, map[string]any{})

	// 辞退（declined）: 問わない。init --loop で入れられる旨だけ出す。loop を付けても使わない
	post(installBody("claude-code", "hook", "server", latest.Core, map[string]any{"installed": false, "declined": true}))
	for _, args := range []map[string]any{{}, {"loop": "yes"}} {
		out := loopSetupOf(t, second(m.call("setup", args, false)))
		if i, _ := loopStepOf(out); i >= 0 || out.Ask != "" || out.Install.Loop != "declined" || strings.Contains(out.Text, "を入れますか？") ||
			!strings.Contains(out.Text, "辞退済みのため勧めない") || !strings.Contains(out.Text, "--loop` で入る") {
			t.Errorf("declined（%v）: loop=%s steps=%+v\n%s", args, out.Install.Loop, out.Steps, out.Text)
		}
	}

	// 導入済み（installed）: 問わない
	post(installBody("claude-code", "hook", "server", latest.Core, map[string]any{"installed": true, "bundle_sha256": latest.Loop, "version": "fixture-1"}))
	out := loopSetupOf(t, second(m.call("setup", map[string]any{}, false)))
	if i, _ := loopStepOf(out); i >= 0 || out.Ask != "" || out.Install.State != "current" || out.Install.Loop != "installed" || strings.Contains(out.Text, "を入れますか？") ||
		!strings.Contains(out.Install.Message, "loop）: あり（fixture-1）") {
		t.Errorf("installed: %+v\n%s", out.Install, out.Text)
	}

	// フック未承認（no_hook）で未選択: 1 回目は問いだけ。答えると login の後・承認の前に init の 1 つ
	post(installBody("codex", "manual", "server", "", map[string]any{"installed": false}))
	_, no = askThenAnswer("codex・no_hook", cx, map[string]any{})
	if i, step := loopStepOf(no); no.Install.State != "no_hook" || i != 1 || !strings.Contains(no.Steps[0].Title, "トークン") ||
		!strings.HasSuffix(step.Command, "--agent codex --url "+e.srv.URL+"/im --source server --dist '"+no.DistURL+"' --no-loop") {
		t.Errorf("no_hook: state=%s i=%d %+v", no.Install.State, i, no.Steps)
	}

	// 撤去した 1.0.0 より前の CLI の導入（client の無い通知・stale）で未選択: looptrack の取得 + init にも答えの旗を付けた 1 つ
	post(map[string]any{"agent": "claude-code", "trigger": "hook", "source": "server", "files": map[string]string{"kit.tar.gz": strings.Repeat("0", 64)},
		"loop": map[string]any{"installed": false}})
	_, no = askThenAnswer("claude-code・stale", m, map[string]any{})
	if i, step := loopStepOf(no); no.Install.State != "stale" || i != 0 ||
		!strings.Contains(step.Command, "curl -fsSL") || !strings.HasSuffix(step.Command, fetchWith(no, "claude-code", "--no-loop")) {
		t.Errorf("stale・未選択: state=%s i=%d %+v", no.Install.State, i, no.Steps)
	}

	// 配布物に kit/loop が無ければ問わない（init --loop が入れられない）
	withLoopKit(t, nil)
	post(installBody("claude-code", "hook", "server", "", map[string]any{"installed": false}))
	if out := loopSetupOf(t, second(m.call("setup", map[string]any{}, false))); out.Ask != "" {
		t.Errorf("kit/loop の無い配布物で loop の問いが出た")
	}

	// 入力の検査
	for _, bad := range []map[string]any{
		installBody("codex", "hook", "", "xyz", nil),
		installBody("codex", "hook", "", "", map[string]any{"installed": true, "bundle_sha256": "xyz"}),
		installBody("codex", "hook", "", "", map[string]any{"installed": true, "version": strings.Repeat("v", 65)}),
	} {
		ed.fail(400, "POST", "/projects/req/install", bad)
	}
}

func second(_ string, data map[string]any) map[string]any { return data }

// TestInstallKitStale: core と loop のハッシュは別々に比べる。loop 未導入のプロジェクトに loop の更新指示は出ない。
func TestInstallKitStale(t *testing.T) {
	fix := loopFixture()
	withLoopKit(t, fix)
	e, _, ed := newAPIEnv(t)
	m := e.mcpAsClient(ed.token, map[string]string{"X-Looptrack-Project": "req"}, "claude-code", "2.1.0", "2025-06-18")
	before, _ := latestDist()
	post := func(body map[string]any) installStateJSON {
		t.Helper()
		var st installStateJSON
		ed.json(200, "POST", "/projects/req/install", body, &st)
		return st
	}
	none := map[string]any{"installed": false}
	declined := map[string]any{"installed": false, "declined": true}
	installed := map[string]any{"installed": true, "bundle_sha256": before.Loop, "version": "fixture-1"}

	// サーバの kit/loop が変わる（更新をデプロイした）
	fix["kit/loop/rules/fixture-rules.md"] = "# fixture の規律（新しい版）\n"
	after, _ := latestDist()
	if after.Loop == before.Loop || after.Core != before.Core {
		t.Fatalf("fixture の更新: %+v → %+v", before, after)
	}
	// loop を入れていない（未選択・辞退・古い CLI）なら current のまま。ツール結果にも指示が付かない
	for name, loop := range map[string]map[string]any{"未選択": none, "辞退": declined, "古い CLI": nil} {
		if st := post(installBody("claude-code", "hook", "server", after.Core, loop)); st.State != "current" || len(st.StaleKit) != 0 {
			t.Errorf("%s: loop の更新を求めた: %+v", name, st)
		}
		if m.call("list_issues", map[string]any{}, false); m.notice != "" {
			t.Errorf("%s: 指示が付いた: %q", name, m.notice)
		}
	}
	// loop を入れている導入だけ loop の更新を求める
	st := post(installBody("claude-code", "hook", "server", after.Core, installed))
	if st.State != "stale" || strings.Join(st.StaleKit, ",") != "loop" || len(st.StaleFiles) != 0 || st.LoopBundle != before.Loop || st.LatestLoopBundle != after.Loop ||
		!strings.Contains(st.Message, "kit/loop 一式") || strings.Contains(st.Message, "kit/core") || st.UpdateCommand != "looptrack issue init --project req --agent claude-code を再実行する" {
		t.Errorf("loop の更新: %+v", st)
	}
	m.call("list_issues", map[string]any{}, false)
	if !strings.Contains(m.notice, "【配布スクリプトの更新】") || !strings.Contains(m.notice, "kit/loop 一式") {
		t.Errorf("loop の更新の指示: %q", m.notice)
	}
	// 新しい loop を入れ直せば current
	if st := post(installBody("claude-code", "hook", "server", after.Core, map[string]any{"installed": true, "bundle_sha256": after.Loop})); st.State != "current" {
		t.Errorf("更新後: %+v", st)
	}
	// core は loop と別に比べる（loop 未導入でも core が古ければ core だけ）。init を再実行する
	st = post(installBody("claude-code", "hook", "copy", strings.Repeat("0", 64), none))
	if st.State != "stale" || strings.Join(st.StaleKit, ",") != "core" || !strings.Contains(st.Message, "kit/core 一式") ||
		st.UpdateCommand != "looptrack issue init --project req --agent claude-code を再実行する" {
		t.Errorf("core の更新: %+v", st)
	}
	// 配布物に kit/loop が無くなった版（比べられない）では、入っている loop の更新を求めない
	withLoopKit(t, nil)
	if st := post(installBody("claude-code", "hook", "server", after.Core, installed)); st.State != "current" {
		t.Errorf("kit/loop の無い配布物: %+v", st)
	}
	var got struct {
		Installs   []installStateJSON `json:"installs"`
		LatestCore string             `json:"latest_core_bundle_sha256"`
	}
	ed.json(200, "GET", "/projects/req/install", nil, &got)
	if got.LatestCore != after.Core || got.Installs[0].Loop != "installed" || got.Installs[0].LoopVersion != "fixture-1" {
		t.Errorf("GET install: %+v", got)
	}
}

// TestPromptLoopSwitch: prompt loop の本文が導入状態（loop の有無）で変わる。
func TestPromptLoopSwitch(t *testing.T) {
	withLoopKit(t, loopFixture())
	e, _, ed := newAPIEnv(t)
	m := e.mcpAsClient(ed.token, map[string]string{"X-Looptrack-Project": "req"}, "claude-code", "2.1.0", "2025-06-18")
	ctx := context.Background()
	get := func(args map[string]string) string {
		t.Helper()
		res, err := m.cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: "loop", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return res.Messages[0].Content.(*mcp.TextContent).Text
	}
	list, err := m.cs.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range list.Prompts {
		if p.Name == "loop" && !strings.Contains(p.Description, "/iterate") {
			t.Errorf("prompts/list の loop の説明に切り替えが無い: %s", p.Description)
		}
	}
	minimal := get(nil)
	if minimal != fmt.Sprintf(loopPromptText, projectSuffix(i18n.JA, "req")) {
		t.Errorf("未導入の loop は最小ループ:\n%s", minimal)
	}
	latest, _ := latestDist()
	post := func(agent string, loop map[string]any) {
		ed.json(200, "POST", "/projects/req/install", installBody(agent, "hook", "server", latest.Core, loop), nil)
	}
	post("claude-code", map[string]any{"installed": false, "declined": true})
	if get(nil) != minimal {
		t.Errorf("辞退でも最小ループのまま")
	}
	post("claude-code", map[string]any{"installed": true, "bundle_sha256": latest.Loop})
	iterate := get(nil)
	for _, want := range []string{"skill /iterate", "1. 未解決の bug の確認", "looptrack gates", "0'. project_summary の「人の判断待ち」「外からの反応」", "prompt「review」",
		"Done にせず In Review にし、comment に判断してほしい点を書く", "（プロジェクト req。"} {
		if !strings.Contains(iterate, want) {
			t.Errorf("loop あり の本文に %q が無い:\n%s", want, iterate)
		}
	}
	if iterate == minimal {
		t.Fatal("loop の有無で本文が変わらない")
	}
	// AI ごと: Codex の導入には loop が無い → Codex からは最小ループ
	post("codex", map[string]any{"installed": false})
	cx := e.mcpAsClient(ed.token, map[string]string{"X-Looptrack-Project": "req"}, "codex-mcp-client", "1", "")
	res, err := cx.cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: "loop"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Messages[0].Content.(*mcp.TextContent).Text != minimal {
		t.Errorf("Codex（loop なし）に /iterate の手順が出た")
	}
	// 見えないプロジェクトを指定したら最小ループ（エラーにしない）
	e.project("secret")
	if !strings.Contains(get(map[string]string{"project": "secret"}), "4. set_status で Done") {
		t.Errorf("見えないプロジェクト")
	}
}

// TestGuideLoop: guide は「次に読むもの」を AI ごとの行で並べる（loop の有無は AI ごと）。CLI（REST）と MCP で同じ Markdown。
func TestGuideLoop(t *testing.T) {
	withLoopKit(t, loopFixture())
	e, pr, ed := newAPIEnv(t)
	m := e.mcpAs(ed.token, map[string]string{"X-Looptrack-Project": "req"})
	md := func() string {
		t.Helper()
		_, body := e.get(ed.c, "/im/api/v1/projects/req/guide?format=md", "Authorization", "Bearer "+ed.token)
		if text, _ := m.call("guide", map[string]any{}, false); text != body {
			t.Errorf("MCP と REST の guide が違う")
		}
		return body
	}
	// row は「次に読むもの」の表から AI の行を返す（無ければ空）
	row := func(g, label string) string {
		for _, l := range strings.Split(g, "\n") {
			if strings.HasPrefix(l, "| "+label+" |") {
				return l
			}
		}
		return ""
	}
	g := md()
	if strings.Contains(g, "次に読むもの") {
		t.Errorf("導入済み通知が無いのに loop の案内が出た")
	}
	latest, _ := latestDist()
	ed.json(200, "POST", "/projects/req/install", installBody("claude-code", "hook", "server", latest.Core, map[string]any{"installed": false}), nil)
	g = md()
	if cc := row(g, "Claude Code"); !strings.Contains(g, "**次に読むもの（AI ごと）**") || !strings.Contains(cc, "| なし（未選択） | 最小ループ") ||
		strings.Contains(g, "/iterate") || row(g, "Codex") != "" || !strings.Contains(g, "表に無い AI はこのプロジェクトに未導入") {
		t.Errorf("loop なし:\n%s", g)
	}
	// Claude Code に loop あり・Codex に loop なし（辞退）: AI ごとの行が両方出て、Codex に /iterate を案内しない
	ed.json(200, "POST", "/projects/req/install", installBody("claude-code", "hook", "server", latest.Core, map[string]any{"installed": true, "bundle_sha256": latest.Loop, "version": "fixture-1"}), nil)
	ed.json(200, "POST", "/projects/req/install", installBody("codex", "hook", "server", latest.Core, map[string]any{"installed": false, "declined": true}), nil)
	g = md()
	cc, cx := row(g, "Claude Code"), row(g, "Codex")
	if !strings.Contains(cc, "| あり（fixture-1） | skill `/iterate` の手順") || !strings.Contains(cc, "`.claude/rules/looptrack-loop/`") {
		t.Errorf("Claude Code の行: %q\n%s", cc, g)
	}
	if !strings.Contains(cx, "| なし（辞退） | 最小ループ") || strings.Contains(cx, "iterate") {
		t.Errorf("Codex の行: %q\n%s", cx, g)
	}
	if strings.Index(g, cc) > strings.Index(g, cx) || row(g, "その他の AI") != "" {
		t.Errorf("行の順・導入の無い AI:\n%s", g)
	}
	// Codex にも loop を入れたら AGENTS.md の loop 節を案内する
	ed.json(200, "POST", "/projects/req/install", installBody("codex", "hook", "server", latest.Core, map[string]any{"installed": true, "bundle_sha256": latest.Loop}), nil)
	if cx = row(md(), "Codex"); !strings.Contains(cx, "| あり | `AGENTS.md` の loop 節") {
		t.Errorf("Codex（loop あり）の行: %q", cx)
	}
	// 撤去したゼロバグゲートのキーが残っていても、guide は落ちずにそのまま出る（後方互換）
	if err := store.SetRules(context.Background(), e.db, pr.ID, []byte(`{"zero_bug_gate":{}}`)); err != nil {
		t.Fatal(err)
	}
	if g = md(); !strings.Contains(g, "**次に読むもの（AI ごと）**") {
		t.Errorf("撤去したキーが残るプロジェクト:\n%s", g)
	}
}

// TestSetupLoopEndToEnd は setup の手順（loop の問いの yes の取得 + init を 1 回）をそのまま実行し、SessionStart のフックの通知で
// loop が installed（ハッシュはサーバの kit/loop 一式と同じ）になって、問いが消え prompt loop が /iterate に切り替わるまでを通す。
// 配布ディレクトリにはこのテストで作った looptrack を置き、取得コマンドが券の URL から取って SHA-256 を確かめて置く。
func TestSetupLoopEndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("取得コマンドの sh を流すため Windows では省略（PowerShell の手順の形は TestSetupGo が確かめる）")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl が無い")
	}
	withLoopKit(t, loopFixture())
	e, _, ed := newAPIEnv(t)
	bin, err := os.ReadFile(looptrackBin(t))
	if err != nil {
		t.Fatal(err)
	}
	e.s.cfg.DistDir = t.TempDir()
	writeDist(t, e.s.cfg.DistDir, map[string]string{"looptrack_v1.0.0_" + runtime.GOOS + "_" + runtime.GOARCH: string(bin)})
	m := e.mcpAsClient(ed.token, map[string]string{"X-Looptrack-Project": "req"}, "claude-code", "2.1.0", "2025-06-18")
	proj, home := t.TempDir(), t.TempDir()
	shIn := func(dir string, env []string, script string) cliResult {
		t.Helper()
		cmd := exec.Command("/bin/sh", "-c", script)
		cmd.Env, cmd.Dir = append(cliHomeEnv(home), env...), dir
		var so, se bytes.Buffer
		cmd.Stdout, cmd.Stderr = &so, &se
		err := cmd.Run()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		return cliResult{so.String(), se.String(), code}
	}
	sh := func(env []string, script string) cliResult { t.Helper(); return shIn(proj, env, script) }

	// 1 回目は問いだけ。答えを付けて呼び直すと、答えに合う取得 + init が最初の手順に 1 つ返る
	text, data := m.call("setup", map[string]any{"os": runtime.GOOS}, false)
	checkLoopAsk(t, "1 回目", text, setupOf(t, data))
	answer := func(ans string) string {
		t.Helper()
		out := loopSetupOf(t, second(m.call("setup", map[string]any{"loop": ans, "os": runtime.GOOS}, false)))
		if i, step := loopStepOf(out); i != 0 || !strings.HasSuffix(step.Command, loopFlag(ans)) || !strings.Contains(step.Command, "curl -fsSL") {
			t.Fatalf("loop=%s の最初の手順が答えに合う取得 + init でない: %+v", ans, out.Steps)
		}
		return out.Steps[0].Command
	}
	// 利用者が「入れない」と答えた → no のコマンド 1 回で looptrack が置かれ、core が入り、辞退が記録される（別の空ディレクトリ）
	other := t.TempDir()
	if res := shIn(other, nil, answer("no")); res.code != 0 || !strings.Contains(res.stdout, "core のみ（loop は辞退を記録") {
		t.Fatalf("no: %d\n%s\n%s", res.code, res.stdout, res.stderr)
	}
	placed := filepath.Join(home, ".local", "bin", "looptrack")
	if b, err := os.ReadFile(placed); err != nil || !bytes.Equal(b, bin) {
		t.Fatalf("取得コマンドが looptrack を置いていない: %v", err)
	}
	var kitJSON struct {
		Loop map[string]any `json:"loop"`
	}
	if err := json.Unmarshal([]byte(mustRead(t, filepath.Join(other, ".claude", ".looptrack-kit.json"))), &kitJSON); err != nil ||
		kitJSON.Loop["installed"] != false || kitJSON.Loop["declined_at"] == nil {
		t.Errorf("no の .looptrack-kit.json: %+v %v", kitJSON, err)
	}
	if _, err := os.Stat(filepath.Join(other, ".claude", "skills", "issue", "SKILL.md")); err != nil {
		t.Errorf("no で core の skill /issue が置かれていない: %v", err)
	}
	if _, err := os.Stat(filepath.Join(other, ".claude", "skills", "iterate")); err == nil {
		t.Errorf("no で loop の skill が置かれた")
	}
	// 利用者が「入れる」と答えた → yes のコマンド 1 回で core + loop（トークンなし・券の URL から取る）
	if res := sh(nil, answer("yes")); res.code != 0 || !strings.Contains(res.stdout, "core + loop") {
		t.Fatalf("yes: %d\n%s\n%s", res.code, res.stdout, res.stderr)
	}
	if b, err := os.ReadFile(filepath.Join(proj, ".claude", "skills", "iterate", "SKILL.md")); err != nil || !strings.Contains(string(b), "name: iterate") {
		t.Errorf("loop の skill が置かれていない: %v", err)
	}
	type hookFile struct {
		Env   map[string]string `json:"env"`
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	var settings hookFile
	if err := json.Unmarshal([]byte(mustRead(t, filepath.Join(proj, ".claude", "settings.json"))), &settings); err != nil {
		t.Fatal(err)
	}
	// SessionStart の summary の hook（looptrack hook summary）。PATH に無い looptrack は settings.local.json に絶対パスで配線される
	sessionStart := ""
	for _, f := range []string{"settings.json", "settings.local.json"} {
		var h hookFile
		if b, err := os.ReadFile(filepath.Join(proj, ".claude", f)); err != nil || json.Unmarshal(b, &h) != nil {
			continue
		}
		for _, g := range h.Hooks["SessionStart"] {
			for _, c := range g.Hooks {
				if strings.Contains(c.Command, "hook summary") {
					sessionStart = c.Command
				}
			}
		}
	}
	if !strings.Contains(sessionStart, "--agent claude-code") {
		t.Fatalf("SessionStart の summary の hook が無い: %q", sessionStart)
	}
	env := []string{"CLAUDE_PROJECT_DIR=" + proj, "LOOPTRACK_TOKEN=" + ed.token, "LOOPTRACK_USAGE=0"}
	for k, v := range settings.Env {
		env = append(env, k+"="+v)
	}
	if res := sh(env, "echo '{\"hook_event_name\":\"SessionStart\",\"session_id\":\"s1\",\"source\":\"startup\"}' | "+sessionStart); res.code != 0 || strings.Contains(res.stdout, "【") {
		t.Fatalf("SessionStart: %d %s %s", res.code, res.stdout, res.stderr)
	}
	latest, _ := latestDist()
	out := loopSetupOf(t, second(m.call("setup", map[string]any{}, false)))
	if st := out.Install; st.State != "current" || st.Loop != "installed" || st.LoopBundle != latest.Loop || st.CoreBundle != latest.Core || st.LoopVersion != "fixture-1" {
		t.Errorf("導入状態: %+v（latest core=%s loop=%s）", st, latest.Core, latest.Loop)
	}
	if i, _ := loopStepOf(out); i >= 0 {
		t.Errorf("導入後も loop の問いが出る")
	}
	res, err := m.cs.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "loop"})
	if err != nil || !strings.Contains(res.Messages[0].Content.(*mcp.TextContent).Text, "skill /iterate") {
		t.Errorf("prompt loop が /iterate にならない: %v", err)
	}
	// 手動の確認（looptrack issue installed）にも loop の状態が出る
	// CLI は言語を送るのでサーバの文面は日本語（setup_test.go の cliNotice* の注記と同じ理由）
	if res := sh(env, `"$HOME/.local/bin/looptrack" issue installed --agent claude-code`); res.code != 0 || !strings.Contains(res.stdout, "loop）: あり（fixture-1）") {
		t.Errorf("installed: %d %s %s", res.code, res.stdout, res.stderr)
	}
}
