package server

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
)

// staleInitRe は init のコマンドを引数ごと拾う（引数は ASCII の語か一重引用符の値。日本語の文はそこで切れる）。
var staleInitRe = regexp.MustCompile(`issue init(?: (?:'[^']*'|[-\w./:$=@%+,]+))*`)

// staleBadCommands は、利用者の端末で実行すると PATH の looptrack や CLI の既定の接続先に頼ってしまうもの
// （--url の無い init・self-update）を s から拾う。
func staleBadCommands(s string) []string {
	var bad []string
	for _, m := range staleInitRe.FindAllString(s, -1) {
		if !strings.Contains(m, " --url ") {
			bad = append(bad, m)
		}
	}
	if strings.Contains(s, "self-update") {
		bad = append(bad, "self-update")
	}
	return bad
}

// TestSetupStaleSingleInit は導入が古いとき（実行ファイルの版・kit の控え・その両方・撤去した以前の CLI）に、setup の手順・
// 導入状態の案内（update_command）と文（message）・MCP の注記・導入済み通知（summary が出す）の応答のどこにも、--url の無い
// init と self-update が出ないこと、手順の init がちょうど 1 回であることを、loop の答え（yes / no / 未回答）と辞退済みの
// 組み合わせで確かめる。サーバ版の利用者の端末には PATH の looptrack も LOOPTRACK_API_URL も無いことがあり、--url の無い
// init は CLI の既定（手元のローカルモード）に向く。init が 2 回続く手順（更新 → loop の選択）にもしない。
func TestSetupStaleSingleInit(t *testing.T) {
	// 検出が起きる側の対照: 以前の案内と手順の形は拾い、--url 付きの取得 + init は拾わない
	for _, old := range []string{
		"looptrack issue init --project req --agent claude-code を再実行する",
		"looptrack self-update && looptrack issue init --project req --agent claude-code",
		"looptrack self-update",
		"run looptrack issue init --project req --agent codex again",
	} {
		if len(staleBadCommands(old)) == 0 {
			t.Fatalf("前提が崩れています: 検査が以前の形 %q を拾わない", old)
		}
	}
	good := `"$HOME/.local/bin/looptrack" issue init --project req --url http://127.0.0.1:1/im --agent claude-code --source server --dist 'http://127.0.0.1:1/im/setup/t' --loop`
	if bad := staleBadCommands(good); len(bad) != 0 || len(staleInitRe.FindAllString(good, -1)) != 1 {
		t.Fatalf("前提が崩れています: --url 付きの init を拾った（%v）か数えられない", bad)
	}

	withLoopKit(t, loopFixture())
	e, _, ed := newAPIEnv(t)
	e.s.cfg.DistDir = t.TempDir()
	writeDist(t, e.s.cfg.DistDir, map[string]string{
		"looptrack_v1.1.0_darwin_arm64": "da", "looptrack_v1.1.0_darwin_amd64": "dx", "looptrack_v1.1.0_linux_amd64": "lx",
		"looptrack_v1.1.0_linux_arm64": "la", "looptrack_v1.1.0_windows_amd64.exe": "wx",
	})
	latest, _ := latestDist()
	refetch := i18n.T(i18n.JA, "server.mcp.setup.update.refetch")
	oldCore := strings.Repeat("0", 64)

	kinds := []struct {
		label, version, core string
		legacy               bool
	}{
		{label: "実行ファイルだけ", version: "v1.0.0", core: latest.Core},
		{label: "kit だけ", version: "v1.1.0", core: oldCore},
		{label: "両方", version: "v1.0.0", core: oldCore},
		{label: "以前の CLI", legacy: true},
	}
	loops := []struct {
		label, state, answer string
	}{
		{"未選択・loop=yes", "none", "yes"},
		{"未選択・loop=no", "none", "no"},
		{"未選択・未回答", "none", ""},
		{"辞退済み", "declined", ""},
	}
	for _, agent := range []string{"claude-code", "codex"} {
		client := map[string]string{"claude-code": "claude-code", "codex": "codex-mcp-client"}[agent]
		m := e.mcpAsClient(ed.token, map[string]string{"X-Looptrack-Project": "req"}, client, "1.0.0", "2025-06-18")
		for _, k := range kinds {
			for _, lp := range loops {
				label := agent + "・" + k.label + "・" + lp.label
				loop := map[string]any{"installed": false, "declined": lp.state == "declined"}
				body := map[string]any{"agent": agent, "trigger": "hook", "source": "server", "loop": loop}
				if k.legacy {
					body["files"] = map[string]string{"kit.tar.gz": strings.Repeat("b", 64)}
				} else {
					body["files"] = map[string]string{}
					body["client"] = map[string]any{"version": k.version, "os": "darwin", "arch": "arm64"}
					body["core"] = map[string]any{"bundle_sha256": k.core}
				}
				// (c) 導入済み通知の応答（looptrack hook summary が出す message）
				var st installStateJSON
				ed.json(200, "POST", "/projects/req/install", body, &st)
				if st.State != "stale" || st.UpdateCommand != refetch || !strings.Contains(st.Message, refetch) ||
					!strings.Contains(st.Message, "【配布スクリプトの更新】") {
					t.Errorf("%s: 導入状態: %+v", label, st)
				}
				for name, s := range map[string]string{"update_command": st.UpdateCommand, "message": st.Message} {
					if bad := staleBadCommands(s); len(bad) != 0 {
						t.Errorf("%s: 通知の応答の %s に %q: %s", label, name, bad, s)
					}
				}
				// MCP のツール結果の注記（setupNotice）
				m.call("list_issues", map[string]any{}, false)
				if bad := staleBadCommands(m.notice); len(bad) != 0 || !strings.Contains(m.notice, refetch) {
					t.Errorf("%s: MCP の注記 %v: %q", label, bad, m.notice)
				}

				// (a) setup の結果
				args := map[string]any{}
				if lp.answer != "" {
					args["loop"] = lp.answer
				}
				_, data := m.call("setup", args, false)
				out := setupOf(t, data)
				var inst installStateJSON
				raw, _ := json.Marshal(data["install"])
				if err := json.Unmarshal(raw, &inst); err != nil {
					t.Fatal(err)
				}
				fields := map[string]string{"install.update_command": inst.UpdateCommand, "install.message": inst.Message}
				posixInits, winInits := 0, 0
				for i, s := range out.Steps {
					fields[fmt.Sprintf("steps[%d]", i)+".command"] = s.Command
					fields[fmt.Sprintf("steps[%d]", i)+".command_windows"] = s.CommandWindows
					fields[fmt.Sprintf("steps[%d]", i)+".title"] = s.Title
					posixInits += len(staleInitRe.FindAllString(s.Command, -1))
					winInits += len(staleInitRe.FindAllString(s.CommandWindows, -1))
				}
				for name, s := range fields {
					if bad := staleBadCommands(s); len(bad) != 0 {
						t.Errorf("%s: setup の %s に %q: %s", label, name, bad, s)
					}
				}
				if lp.state == "none" && lp.answer == "" {
					// 問いだけ（コマンドを返さない）
					if out.Ask != "loop" || posixInits != 0 || winInits != 0 {
						t.Errorf("%s: 問いだけの結果でない: ask=%q %+v", label, out.Ask, out.Steps)
					}
					continue
				}
				if out.Ask != "" || posixInits != 1 || winInits != 1 {
					t.Errorf("%s: init が 1 回でない（sh %d 回・PowerShell %d 回）: %+v", label, posixInits, winInits, out.Steps)
					continue
				}
				first := out.Steps[0]
				if !strings.Contains(first.Command, `curl -fsSL "$U"`) || !strings.Contains(first.CommandWindows, "Invoke-WebRequest") {
					t.Errorf("%s: 1 つ目が取得 + init でない: %+v", label, first)
				}
				want := map[string]string{"yes": " --loop", "no": " --no-loop"}[lp.answer]
				if want != "" && (!strings.HasSuffix(innerCmd(first.Command), want) || !strings.HasSuffix(innerCmd(first.CommandWindows), want)) {
					t.Errorf("%s: 取得 + init に答えの旗 %q が無い: %+v", label, want, first)
				}
				if want == "" && (strings.Contains(first.Command, "-loop") || strings.Contains(first.CommandWindows, "-loop")) {
					t.Errorf("%s: 答えの無い取得 + init に loop の旗がある: %+v", label, first)
				}
				// 確認の手順（以前の CLI 以外）は、置いた looptrack に URL とプロジェクトを明示して渡す（AI の環境変数に頼らない）。
				// sh は export の無い代入の前置、PowerShell は退避して finally で戻す形（どちらも値を呼び出し元のシェルに残さない）
				if !k.legacy {
					last := out.Steps[len(out.Steps)-1]
					base := e.srv.URL + "/im"
					if last.Command != "LOOPTRACK_API_URL="+base+" LOOPTRACK_PROJECT=req \"$HOME/.local/bin/looptrack\" issue installed --agent "+agent ||
						last.CommandWindows != envWinHead+"; $__ltPrevProject = $env:LOOPTRACK_PROJECT; $env:LOOPTRACK_API_URL = '"+base+"'; $env:LOOPTRACK_PROJECT = 'req'; "+
							"try { & (Join-Path $env:LOCALAPPDATA 'Programs\\looptrack\\looptrack.exe') issue installed --agent "+agent+" } "+
							"finally { $env:LOOPTRACK_API_URL = $__ltPrevApiUrl; $env:LOOPTRACK_PROJECT = $__ltPrevProject } }" {
						t.Errorf("%s: 確認の手順: %+v", label, last)
					}
				}
			}
		}
	}
}

// innerCmd は Copilot・Codex 向けの前置（sh のサブシェルの export・PowerShell の退避と復元）と、取得 + init を包む形
// （sh のサブシェル・PowerShell の & { }）を外した中身（どちらも無ければそのまま）。
func innerCmd(c string) string {
	c = innerEnvCmd(c)
	if strings.HasPrefix(c, "(D=") && strings.HasSuffix(c, ")") {
		return c[1 : len(c)-1]
	}
	if strings.HasPrefix(c, "& { $ErrorActionPreference") && strings.HasSuffix(c, " }") {
		return c[len("& { ") : len(c)-len(" }")]
	}
	return c
}

// innerEnvCmd は Copilot・Codex 向けの前置を外した中身（前置が無ければそのまま）。
func innerEnvCmd(c string) string {
	if strings.HasPrefix(c, "(export LOOPTRACK_API_URL=") && strings.HasSuffix(c, ")") {
		if i := strings.Index(c, " && "); i >= 0 {
			return c[i+4 : len(c)-1]
		}
	}
	if strings.HasPrefix(c, envWinHead) {
		if i, j := strings.Index(c, "try { "), strings.LastIndex(c, " } finally {"); i >= 0 && j > i {
			return c[i+6 : j]
		}
	}
	return c
}

// bareLooptrackRe はパスの付かない looptrack をコマンドとして使う形（行頭・空白・シェルの区切り・インラインコードの `・全角の括弧や句読点の直後に
// サブコマンドが続く）を拾う。置き場の絶対パス（"$HOME/.local/bin/looptrack" issue …・…\looptrack.exe') issue …）や、
// 説明文の中の語（「looptrack（Go 版の実行ファイル）」「looptrack 自身」）には当たらない。
var bareLooptrackRe = regexp.MustCompile("(?:^|[\\s;&|(`（「、。：])looptrack(?:\\.exe)? (?:issue|doctor|self-update|hook|login|config|gates|report)\\b")

// setupBadForms は staleBadCommands に、パスの付かない looptrack のコマンドを足したもの。
func setupBadForms(s string) []string {
	bad := staleBadCommands(s)
	for _, m := range bareLooptrackRe.FindAllString(s, -1) {
		bad = append(bad, strings.TrimSpace(m))
	}
	return bad
}

// allStrings は構造化データの全文字列を集める（キーの道のりつき）。
func allStrings(path string, v any, out map[string]string) {
	switch x := v.(type) {
	case string:
		out[path] = x
	case map[string]any:
		for k, c := range x {
			allStrings(path+"."+k, c, out)
		}
	case []any:
		for i, c := range x {
			allStrings(fmt.Sprintf("%s[%d]", path, i), c, out)
		}
	}
}

// TestSetupNoBareCommands は setup の結果の全テキスト（ツール結果の本文（Markdown の全文）・構造化データの全文字列）と
// MCP の注記に、--url の無い init・self-update・パスの付かない looptrack のコマンドが無いことを、導入状態（missing・no_hook・
// current・stale）× loop（yes / no / 未回答 / 辞退済み）× AI（Claude Code・Codex）で確かめる。サーバ版の利用者の端末には
// PATH の looptrack も LOOPTRACK_API_URL も無い前提で、表示されたコマンドをそのまま実行して手元のローカルモードに向く形を残さない。
// 検査の範囲はコマンドの形（--url の無い `issue init`・`self-update`・パスの付かない `looptrack <サブコマンド>`）に絞り、
// 説明文の中の語としての looptrack は見ない。配布ディレクトリにその OS 向けが無いとき（利用者が別の方法で PATH に置く手順）は対象外。
func TestSetupNoBareCommands(t *testing.T) {
	// 検出が起きる側の対照: 直す前の文面とコマンドには当たり、置き場の絶対パスと説明文には当たらない
	for _, old := range []string{
		"ループエンジニアリング一式（loop）: なし（辞退。入れるなら looptrack issue init --loop）",
		"PATH と配線は looptrack doctor で確かめられる",
		"トークン未登録ならフックは送れません（looptrack issue config で確認）。",
		"後から `looptrack issue init --loop` / `--remove-loop` で変えられる。",
		"利用者が望めば `looptrack issue init --project req --agent claude-code --url http://x/im --source server --dist 'u' --loop` で入る",
		"looptrack issue init --project req --agent claude-code --url http://x/im --source server --dist 'u' --no-loop",
	} {
		if len(setupBadForms(old)) == 0 {
			t.Fatalf("前提が崩れています: 検査が直す前の形 %q を拾わない", old)
		}
	}
	for _, ok := range []string{
		`"$HOME/.local/bin/looptrack" issue init --project req --url http://x/im --agent claude-code --source server --dist 'u' --loop`,
		`& (Join-Path $env:LOCALAPPDATA 'Programs\looptrack\looptrack.exe') issue init --project req --url http://x/im --agent codex --source server --dist 'u'`,
		"CLI: looptrack（Go 版の実行ファイル。置き場 ~/.local/bin）",
		"この作業ディレクトリは looptrack 自身（kit の正本）",
	} {
		if bad := setupBadForms(ok); len(bad) != 0 {
			t.Fatalf("前提が崩れています: 正しい形 %q を拾った: %v", ok, bad)
		}
	}

	withLoopKit(t, loopFixture())
	e, _, ed := newAPIEnv(t)
	e.s.cfg.DistDir = t.TempDir()
	writeDist(t, e.s.cfg.DistDir, map[string]string{
		"looptrack_v1.1.0_darwin_arm64": "da", "looptrack_v1.1.0_darwin_amd64": "dx", "looptrack_v1.1.0_linux_amd64": "lx",
		"looptrack_v1.1.0_linux_arm64": "la", "looptrack_v1.1.0_windows_amd64.exe": "wx",
	})
	latest, _ := latestDist()
	answers := []string{"yes", "no", ""}

	check := func(label string, m *mcpClient, answer string, wantAsk bool) {
		t.Helper()
		args := map[string]any{}
		if answer != "" {
			args["loop"] = answer
		}
		text, data := m.call("setup", args, false)
		fields := map[string]string{"text": text}
		allStrings("data", data, fields)
		inits := 0
		for name, s := range fields {
			if bad := setupBadForms(s); len(bad) != 0 {
				t.Errorf("%s: setup の %s に %q:\n%s", label, name, bad, s)
			}
		}
		inits = len(staleInitRe.FindAllString(text, -1))
		out := setupOf(t, data)
		if wantAsk != (out.Ask == "loop") {
			t.Errorf("%s: ask=%q（問いだけの結果か: %v のはず）", label, out.Ask, wantAsk)
		}
		// 検査が init のコマンドを実際に見ていることの対照（問いだけの結果以外は、--url 付きの init が本文にある）
		if !wantAsk && inits == 0 {
			t.Errorf("前提が崩れています: %s: 本文に init のコマンドが無い（検査が空振りしている）:\n%s", label, text)
		}
		m.call("list_issues", map[string]any{}, false)
		if bad := setupBadForms(m.notice); len(bad) != 0 {
			t.Errorf("%s: MCP の注記に %q: %q", label, bad, m.notice)
		}
	}
	post := func(body map[string]any) {
		t.Helper()
		var st installStateJSON
		ed.json(200, "POST", "/projects/req/install", body, &st)
		if bad := setupBadForms(st.Message + "\n" + st.UpdateCommand); len(bad) != 0 {
			t.Errorf("通知の応答に %q: %+v", bad, st)
		}
	}
	body := func(agent, trigger, version, core string, declined bool) map[string]any {
		return map[string]any{"agent": agent, "trigger": trigger, "source": "server", "files": map[string]string{},
			"client": map[string]any{"version": version, "os": "darwin", "arch": "arm64"},
			"core":   map[string]any{"bundle_sha256": core}, "loop": map[string]any{"installed": false, "declined": declined}}
	}
	for _, agent := range []string{"claude-code", "codex"} {
		client := map[string]string{"claude-code": "claude-code", "codex": "codex-mcp-client"}[agent]
		m := e.mcpAsClient(ed.token, map[string]string{"X-Looptrack-Project": "req"}, client, "1.0.0", "2025-06-18")
		// missing（通知が無い。loop は未選択なので辞退済みの組はない）
		for _, a := range answers {
			check(agent+"・missing・loop="+a, m, a, a == "")
		}
		for _, sc := range []struct {
			state, trigger, version, core string
		}{
			{"no_hook", "manual", "v1.1.0", latest.Core},
			{"current", "hook", "v1.1.0", latest.Core},
			{"stale", "hook", "v1.0.0", strings.Repeat("0", 64)},
		} {
			for _, a := range answers {
				post(body(agent, sc.trigger, sc.version, sc.core, false))
				check(agent+"・"+sc.state+"・未選択・loop="+a, m, a, a == "")
			}
			post(body(agent, sc.trigger, sc.version, sc.core, true))
			check(agent+"・"+sc.state+"・辞退済み", m, "", false)
		}
	}
}

// winInners は s の中の PowerShell の前置（envRestoreWin）で包んだ中身をすべて返す。
func winInners(s string) []string {
	var out []string
	for {
		i := strings.Index(s, envWinHead)
		if i < 0 {
			return out
		}
		s = s[i:]
		a, b := strings.Index(s, "try { "), strings.Index(s, " } finally {")
		if a < 0 || b < a {
			return out
		}
		out = append(out, s[a+6:b])
		s = s[b:]
	}
}

// winNameHit は中身に、退避の変数名のどれかが（PowerShell と同じく大小を区別せずに）変数として出てくるか。
func winNameHit(inner string, names []string) []string {
	var hit []string
	for _, n := range names {
		re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(n) + `\b`)
		if re.MatchString(inner) {
			hit = append(hit, n)
		}
	}
	return hit
}

// TestSetupWinRestoreNamesDoNotCollide は、setup が返す PowerShell のコマンド（手順の CommandWindows・本文や見出しの中の
// Windows のコマンド）のうち、環境変数を退避して戻す前置で包んだものについて、包んだ中身に退避の変数名が出てこないことを
// 確かめる。PowerShell の変数名は大小を区別しないので、退避を $u・$p にすると取得の手順の $U（取得 URL）・$P（.part のパス）に
// 上書きされ、finally が LOOPTRACK_API_URL・LOOPTRACK_PROJECT に取得 URL と .part のパスを入れてしまう。
// 対照として、退避の名前を $u・$p と仮定すると、この検査が取得の手順の $U・$P に当たることを示す。
func TestSetupWinRestoreNamesDoNotCollide(t *testing.T) {
	names := []string{envWinPrevURL, envWinPrevProj}
	withLoopKit(t, loopFixture())
	e, _, ed := newAPIEnv(t)
	withFakeDist(t, e)
	var inners []string
	collect := func(m *mcpClient, args map[string]any) {
		t.Helper()
		text, data := m.call("setup", args, false)
		fields := map[string]string{"text": text}
		allStrings("data", data, fields)
		for _, s := range fields {
			inners = append(inners, winInners(s)...)
		}
	}
	for _, c := range []struct{ agent, client string }{
		{"claude-code", "claude-code"}, {"codex", "codex-mcp-client"}, {"copilot", "github-copilot-developer"}, {"other", "cursor"},
	} {
		m := e.mcpAsClient(ed.token, map[string]string{"X-Looptrack-Project": "req"}, c.client, "1.0.0", "2025-06-18")
		for _, os := range []string{"", "windows"} {
			collect(m, map[string]any{"os": os, "loop": "yes"}) // missing
		}
		if c.agent == "other" {
			continue
		}
		for _, sc := range []struct{ trigger, version, core string }{
			{"manual", "v1.0.0", ""},                    // no_hook
			{"hook", "v0.9.0", strings.Repeat("0", 64)}, // stale
		} {
			body := installBody(c.agent, sc.trigger, "server", sc.core, map[string]any{"installed": false, "declined": true})
			body["client"] = map[string]any{"version": sc.version, "os": "darwin", "arch": "arm64"}
			var st installStateJSON
			ed.json(200, "POST", "/projects/req/install", body, &st)
			for _, os := range []string{"", "windows"} {
				collect(m, map[string]any{"os": os})
			}
		}
	}
	fetches := 0
	for _, in := range inners {
		if strings.Contains(in, "Invoke-WebRequest") {
			fetches++
		}
		if hit := winNameHit(in, names); len(hit) != 0 {
			t.Errorf("包んだ中身に退避の変数名 %v が出てくる（finally が別の値を戻す）: %s", hit, in)
		}
	}
	// 対照: 包んだ取得の手順を実際に見ていて、退避が $u・$p なら当たる
	if fetches == 0 || len(inners) < 10 {
		t.Fatalf("前提が崩れています: 包んだ PowerShell のコマンドを集められていない（%d 件・取得 %d 件）", len(inners), fetches)
	}
	old := 0
	for _, in := range inners {
		if strings.Contains(in, "Invoke-WebRequest") && len(winNameHit(in, []string{"$u", "$p"})) == 2 {
			old++
		}
	}
	if old != fetches {
		t.Fatalf("前提が崩れています: 退避を $u・$p とした検査が取得の手順の $U・$P に当たらない（%d / %d 件）", old, fetches)
	}
}
