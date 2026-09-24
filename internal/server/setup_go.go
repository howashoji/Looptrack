package server

import (
	"fmt"
	"strings"

	"github.com/howashoji/looptrack/internal/i18n"
)

// setup ツールの手順（looptrack。DESIGN.md §5-11「配布と更新」）。
//
// 以前は 1.0.0 より前の CLI の手順が既定で、looptrack は引数 cli: "looptrack" かサーバの LOOPTRACK_SETUP_GO のときだけ
// だった。以前の CLI は撤去し、手順は looptrack だけにした（LOOPTRACK_SETUP_GO も廃止）。
// 配布ディレクトリ（LOOPTRACK_DIST_DIR）にその OS 向けの looptrack が無ければ、取得の手順は出せないので、その旨と
// looptrack を別の方法で入れた後の init のコマンドを利用者向けの手順として示す（以前の CLI の手順には戻さない）。
//
// 置き場は管理者権限の要らない場所: macOS・Linux は ~/.local/bin/looptrack、Windows は %LOCALAPPDATA%\Programs\looptrack\looptrack.exe。
// 取得は券の URL（トークン不要）から。CPU はコマンドの中で調べ（uname -m・PROCESSOR_ARCHITECTURE）、SHA-256 を確かめてから置く。
// macOS / Linux は curl + shasum -a 256（Linux は sha256sum）、Windows は Invoke-WebRequest + Get-FileHash。
// OS は引数 os → User-Agent の順に決め、分からなければ macOS・Linux と Windows の両方を返す（command と command_windows）。
// 置いた実行ファイルを PATH で解決できなくても、init が hook を絶対パスで配線する（Claude Code は .claude/settings.local.json）。

const (
	goPosixDir = `$HOME/.local/bin`
	goPosixBin = `"$HOME/.local/bin/looptrack"`
	goWinBin   = `& (Join-Path $env:LOCALAPPDATA 'Programs\looptrack\looptrack.exe')`
)

// goSetup は手順の材料。lang は手順の文面の言語（要求ごとに決めて持ち回る）。
type goSetup struct {
	bins    []distBinaryJSON // 券の URL の実行ファイル
	lang    i18n.Lang        // 手順の文面の言語
	os      string           // darwin / linux / windows / posix（OS 不明で Windows 向けが無い）/ ""（不明）
	missing string           // 取得の手順を出せない理由（その OS 向けの looptrack を配っていない）。空なら出せる
}

// goSetupOf は setup の引数（cli・os）と User-Agent から手順の材料を作る。引数の誤りは利用者向けのエラー。
func (s *Server) goSetupOf(lang i18n.Lang, in setupIn, ua string, bins []distBinaryJSON) (*goSetup, error) {
	switch strings.ToLower(strings.TrimSpace(in.CLI)) {
	case "", "looptrack", "go":
	default:
		return nil, &serviceErrorText{i18n.T(lang, "server.mcp.setup.err.cli")}
	}
	goos := strings.ToLower(strings.TrimSpace(in.OS))
	switch goos {
	case "", "darwin", "linux", "windows":
	case "macos", "mac":
		goos = "darwin"
	default:
		return nil, &serviceErrorText{i18n.T(lang, "server.mcp.setup.err.os")}
	}
	if goos == "" {
		goos = osOfUserAgent(ua)
	}
	g := &goSetup{bins: bins, lang: lang, os: goos}
	if !g.posixOK() && !g.winOK() {
		g.missing = i18n.T(lang, "server.mcp.setup.dist.missing")
		if goos != "" {
			g.missing = i18n.T(lang, "server.mcp.setup.dist.missing_os", "os", goos)
		}
		return g, nil
	}
	if goos == "" && !g.winOK() {
		g.os = "posix" // Windows 向けが無ければ sh の手順だけ
	}
	if goos == "" && !g.posixOK() {
		g.os = "windows"
	}
	return g, nil
}

// osOfUserAgent は User-Agent から OS を推し量る（分からなければ ""）。
func osOfUserAgent(ua string) string {
	u := strings.ToLower(ua)
	switch {
	case strings.Contains(u, "windows") || strings.Contains(u, "win32") || strings.Contains(u, "win64"):
		return "windows"
	case strings.Contains(u, "darwin") || strings.Contains(u, "mac os") || strings.Contains(u, "macos") || strings.Contains(u, "macintosh"):
		return "darwin"
	case strings.Contains(u, "linux"):
		return "linux"
	}
	return ""
}

func (g *goSetup) has(goos string) bool {
	for _, b := range g.bins {
		if b.OS == goos {
			return true
		}
	}
	return false
}

// posixOK は sh の手順を出すか（OS が windows 以外で、その OS 向けの実行ファイルがある）。
func (g *goSetup) posixOK() bool {
	switch g.os {
	case "windows":
		return false
	case "darwin", "linux":
		return g.has(g.os)
	}
	return g.has("darwin") || g.has("linux")
}

// winOK は PowerShell の手順を出すか。
func (g *goSetup) winOK() bool {
	return (g.os == "windows" || g.os == "") && g.has("windows")
}

// isWindows は主のコマンドが PowerShell か。
func (g *goSetup) isWindows() bool { return g.os == "windows" }

// fence は本文のコードブロックの言語。
func (g *goSetup) fence() string {
	if g.os == "windows" {
		return "powershell"
	}
	return "sh"
}

func (g *goSetup) placeText() string {
	switch {
	case g.os == "windows":
		return i18n.T(g.lang, "server.mcp.setup.place.windows")
	case g.posixOK() && g.winOK():
		return i18n.T(g.lang, "server.mcp.setup.place.both")
	}
	return i18n.T(g.lang, "server.mcp.setup.place.posix")
}

// pick は (os, arch) の実行ファイル（無ければ nil）。
func (g *goSetup) pick(goos, arch string) *distBinaryJSON {
	for i := range g.bins {
		if g.bins[i].OS == goos && g.bins[i].Arch == arch {
			return &g.bins[i]
		}
	}
	return nil
}

// posixFetch は sh の取得と init（OS・CPU は uname で調べる）。
func (g *goSetup) posixFetch(tail string) string {
	var cases []string
	for _, t := range []struct{ osName, goos, arch, pat, sum string }{
		{"Darwin", "darwin", "arm64", "Darwin/arm64", "shasum -a 256"},
		{"Darwin", "darwin", "amd64", "Darwin/x86_64", "shasum -a 256"},
		{"Linux", "linux", "amd64", "Linux/x86_64|Linux/amd64", "sha256sum"},
		{"Linux", "linux", "arm64", "Linux/aarch64|Linux/arm64", "sha256sum"},
	} {
		if g.os != "" && g.os != "posix" && g.os != t.goos {
			continue
		}
		if b := g.pick(t.goos, t.arch); b != nil {
			// ハッシュの確認は関数にする。変数に入れて $C のように使うと、zsh は値を単語に分けないので
			// 'shasum -a 256' 全体をコマンド名として探して失敗する。
			cases = append(cases, fmt.Sprintf(`%s) U='%s'; S='%s'; sum() { %s "$@"; };;`, t.pat, b.URL, b.SHA256, t.sum))
		}
	}
	cases = append(cases, fmt.Sprintf(`*) echo "%s: $(uname -s)/$(uname -m)" >&2; false;;`,
		i18n.T(g.lang, "server.mcp.setup.fetch.no_dist_platform")))
	return fmt.Sprintf(`D="%s" && mkdir -p "$D" && case "$(uname -s)/$(uname -m)" in %s esac && curl -fsSL "$U" -o "$D/looptrack.part" && `+
		`echo "$S  $D/looptrack.part" | sum -c - && chmod 755 "$D/looptrack.part" && mv -f "$D/looptrack.part" "$D/looptrack" && %s %s`,
		goPosixDir, strings.Join(cases, " "), goPosixBin, tail)
}

// winFetch は PowerShell の取得と init（CPU は PROCESSOR_ARCHITECTURE。ARM64 に arm64 が無ければ amd64 をエミュレーションで使う）。
func (g *goSetup) winFetch(tail string) string {
	amd, arm := g.pick("windows", "amd64"), g.pick("windows", "arm64")
	var cases []string
	if arm == nil {
		arm = amd
	}
	if arm != nil {
		cases = append(cases, fmt.Sprintf(`'ARM64' { $U = '%s'; $S = '%s' }`, arm.URL, arm.SHA256))
	}
	if amd != nil {
		cases = append(cases, fmt.Sprintf(`'AMD64' { $U = '%s'; $S = '%s' }`, amd.URL, amd.SHA256))
	}
	cases = append(cases, fmt.Sprintf(`default { throw "%s: $env:PROCESSOR_ARCHITECTURE" }`,
		i18n.T(g.lang, "server.mcp.setup.fetch.no_dist_cpu")))
	return fmt.Sprintf(`$ErrorActionPreference = 'Stop'; $ProgressPreference = 'SilentlyContinue'; `+
		`$D = Join-Path $env:LOCALAPPDATA 'Programs\looptrack'; New-Item -ItemType Directory -Force -Path $D | Out-Null; `+
		`switch ($env:PROCESSOR_ARCHITECTURE) { %s }; $P = Join-Path $D 'looptrack.part'; Invoke-WebRequest -UseBasicParsing -Uri $U -OutFile $P; `+
		`if ((Get-FileHash -Algorithm SHA256 -Path $P).Hash -ne $S) { Remove-Item $P; throw '%s' }; `+
		`Move-Item -Force $P (Join-Path $D 'looptrack.exe'); %s %s`,
		strings.Join(cases, " "), i18n.T(g.lang, "server.mcp.setup.fetch.sha_mismatch"), goWinBin, tail)
}

// fetch は取得 + init の (主のコマンド, Windows のコマンド)。OS が分かっていれば 2 つ目は空。
func (g *goSetup) fetch(slug, base, dist, initArgs, loopFlag string) (string, string) {
	tail := fmt.Sprintf("issue init --project %s --url %s %s --source server --dist '%s'%s", slug, base, initArgs, dist, loopFlag)
	return g.both(func(win bool) string {
		if win {
			return g.winFetch(tail)
		}
		return g.posixFetch(tail)
	})
}

// both は OS に合わせて (主, Windows) を作る。
func (g *goSetup) both(f func(win bool) string) (string, string) {
	switch {
	case g.os == "windows":
		return f(true), ""
	case g.winOK() && g.posixOK():
		return f(false), f(true)
	}
	return f(false), ""
}

// run は置いた looptrack のコマンド（主, Windows）。取得の手順を出せない（利用者が別の方法で入れる）ときは PATH の looptrack。
func (g *goSetup) run(args string) (string, string) {
	if g.missing != "" {
		return "looptrack " + args, ""
	}
	return g.both(func(win bool) string {
		if win {
			return goWinBin + " " + args
		}
		return goPosixBin + " " + args
	})
}

func (g *goSetup) step(who, title string, cmd, win string) setupStepJSON {
	return setupStepJSON{Who: who, Title: title, Command: cmd, CommandWindows: win}
}

// goInitCommand は取得の無い位置の init（loop の選択・辞退の案内）。bin は looptrack の呼び方（空なら「issue init …」から）。
func goInitCommand(bin, agent, slug, base, dist string) string {
	return strings.TrimPrefix(fmt.Sprintf("%s issue init --project %s --agent %s --url %s --source server --dist '%s'", bin, slug, agent, base, dist), " ")
}

// fetchStep は looptrack の取得 + init の手順（note は見出しに添える利用者の答え。空なら添えない）。その OS 向けの looptrack を
// 配っていなければ、取得の手順は出せないので、利用者が looptrack を用意した後に実行する init のコマンドを見出しに書いた
// 利用者の手順にする（AI が実行できるコマンドは付けない）。
func (g *goSetup) fetchStep(lang i18n.Lang, agent, slug, base, dist, loopFlag, note string) setupStepJSON {
	if g.missing != "" {
		return setupStepJSON{Who: "human", Title: i18n.T(lang, "server.mcp.setup.step.fetch_manual", "reason", g.missing,
			"command", fmt.Sprintf("looptrack issue init --project %s --url %s --agent %s%s", slug, base, agent, loopFlag))}
	}
	c, w := g.fetch(slug, base, dist, "--agent "+agent, loopFlag)
	title := i18n.T(lang, "server.mcp.setup.step.fetch")
	if note != "" {
		title = i18n.T(lang, "server.mcp.setup.step.fetch_note", "note", note)
	}
	return g.step("ai", title, c, w)
}

// goUpdateSteps は looptrack の導入が古いときの手順（self-update・kit が古ければ init の再実行）。
// 実行するコマンドは st.updateShell（言語に依らない）。st.UpdateCommand は利用者の言語の案内文なので見出しにだけ使う。
func goUpdateSteps(lang i18n.Lang, agent string, st installStateJSON) []setupStepJSON {
	return []setupStepJSON{
		{Who: "ai", Title: i18n.T(lang, "server.mcp.setup.step.update", "command", st.UpdateCommand),
			Command: st.updateShell},
		{Who: "ai", Title: i18n.T(lang, "server.mcp.setup.step.update_confirm"),
			Command: fmt.Sprintf("looptrack issue installed --agent %s", agent)},
	}
}

// baseSteps は導入状態に合わせた手順（loop の答えに合う手順は setupStepsOf が挟む）。
func (g *goSetup) baseSteps(lang i18n.Lang, agent, slug, base, dist string, st installStateJSON) []setupStepJSON {
	lc, lw := g.run("issue login --browser --url " + base)
	cc, cw := g.run("issue config")
	if agent == agentCopilot { // Copilot には環境変数を付けて渡す
		cc, cw = copilotEnv(cc, base, slug, g.os == "windows"), copilotEnv(cw, base, slug, true)
	}
	login := g.step("ai", i18n.T(lang, "server.mcp.setup.step.login", "check", joinOS(lang, cc, cw), "url", base), lc, lw)
	confirm := setupStepJSON{Who: "ai", Title: i18n.T(lang, "server.mcp.setup.step.confirm")}
	switch st.State {
	case "current":
		return []setupStepJSON{{Who: "ai", Title: i18n.T(lang, "server.mcp.setup.step.current")}}
	case "no_hook":
		return []setupStepJSON{login, {Who: "human", Title: approveStep(lang, agent)}, confirm}
	case "stale":
		if st.ClientOS != "" {
			return goUpdateSteps(lang, agent, st)
		}
		// 撤去した以前の CLI の導入を looptrack に置き換える: 取得 + init（core・loop の配線を looptrack hook で足す。以前の配線は置き換えずに残し、init が知らせる）
	}
	if agent == agentOther {
		ic, iw := g.run("issue installed --agent other")
		if iw != "" {
			iw = fmt.Sprintf("$env:LOOPTRACK_API_URL='%s'; $env:LOOPTRACK_PROJECT='%s'; %s", base, slug, iw)
		}
		if g.os == "windows" {
			ic = fmt.Sprintf("$env:LOOPTRACK_API_URL='%s'; $env:LOOPTRACK_PROJECT='%s'; %s", base, slug, ic)
		} else {
			ic = fmt.Sprintf("LOOPTRACK_API_URL=%s LOOPTRACK_PROJECT=%s %s", base, slug, ic)
		}
		return []setupStepJSON{
			g.fetchStep(lang, agent, slug, base, dist, "", ""),
			{Who: "ai", Title: i18n.T(lang, "server.mcp.setup.step.other_wire")},
			login,
			g.step("ai", i18n.T(lang, "server.mcp.setup.step.other_installed"), ic, iw),
		}
	}
	return []setupStepJSON{g.fetchStep(lang, agent, slug, base, dist, "", ""), login, {Who: "human", Title: approveStep(lang, agent)}, confirm}
}

func joinOS(lang i18n.Lang, posix, win string) string {
	if win == "" {
		return posix
	}
	return i18n.T(lang, "server.mcp.setup.text.or_windows", "posix", posix, "win", win)
}

// loopStep は利用者の答え（answer: yes / no）に合う loop の手順（who: ai・コマンド 1 つ）。問う状態でない・答えが無いときは nil。
// 取得の無い位置（current・no_hook・looptrack の更新）に挟む。looptrack の通知がある導入（入れ方が分からない）は PATH の
// looptrack で、それ以外は setup の手順で置いた looptrack で実行する。
func (g *goSetup) loopStep(lang i18n.Lang, agent, slug, base, dist string, st installStateJSON, l distLatest, answer string) *setupStepJSON {
	if answer == "" || !needLoopAsk(agent, st, l) {
		return nil
	}
	ls := &setupStepJSON{Who: "ai", Title: i18n.T(lang, "server.mcp.setup.step.loop_apply", "answer", loopAnswerPhrase(lang, answer))}
	if st.ClientOS != "" {
		ls.Command = goInitCommand("looptrack", agent, slug, base, dist) + loopFlag(answer)
	} else {
		ls.Command, ls.CommandWindows = g.run(goInitCommand("", agent, slug, base, dist) + loopFlag(answer))
	}
	return ls
}
