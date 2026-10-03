package server

import (
	"fmt"
	"strings"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/setuppath"
)

// setup ツールの手順（looptrack。DESIGN.md §5-1「配布と更新」）。
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
// 置いた後、置き場が PATH に無ければ利用者の PATH に足す（internal/setuppath。macOS・Linux はシェルの起動ファイル、Windows は
// 利用者の環境変数 Path）。kit・MCP・hook・CLI の文面は素の looptrack で書いてあるので、PATH で解決できれば AI と利用者が
// そのまま打てる。足せなくても、init が hook を絶対パスで配線する（Claude Code は .claude/settings.local.json）。

const (
	goPosixDir = setuppath.PosixDir
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

// posixFetch は sh の取得と init（OS・CPU は uname で調べる）。全体をサブシェルで包む: 手順は利用者の端末に貼られることが
// あり、包まないと変数 U・S・D と、/usr/bin/sum を覆い隠す関数 sum（PATH を足す断片の L・W・lt_rc も）が対話シェルに残る
// （どの AI 向けでも同じ形）。PATH を足すのは起動ファイルへの追記なので、包みの外（次に開く端末）に効く。
func (g *goSetup) posixFetch(tail string) string { return "(" + g.posixFetchBody(tail) + ")" }

// posixFetchBody は posixFetch の包む前の中身（テストの対照にも使う）。
func (g *goSetup) posixFetchBody(tail string) string {
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
	// 配布の無い OS・CPU（32 ビットの x86 など）では、置き場に同じ looptrack があってもここで止まる。理由を出してうるさく
	// 落ちるので、踏んだ人はその場で気づける（判定を後ろに回すと、比べる U と S が無くなる）
	cases = append(cases, fmt.Sprintf(`*) echo "%s: $(uname -s)/$(uname -m)" >&2; false;;`,
		i18n.T(g.lang, "server.mcp.setup.fetch.no_dist_platform")))
	// 置き場の looptrack が配布物と SHA-256 で同じときだけ取得を省き、違えば（無ければ）取得して確かめてから置き換える。
	// サーバ版の利用者の手元にあるのは setup が配布物から置いた looptrack だけという前提なので、版の新旧は比べず
	// サーバが配る版にそろえる（PATH の looptrack や手元の CLI の既定の接続先に頼らない）。比べるには U と S が要るので、
	// OS・CPU の判定を先に置く。置き換えは .part に落としてから mv -f で行い、symlink ならリンクの先ではなくリンクを置き換える。
	// 取得・確認・置き換えのどれかで失敗したら .part を消して失敗で終える（PowerShell の形と同じく、途中のファイルを残さない）。
	// 置いた後、置き場が PATH に無ければ起動ファイルに足す（setuppath.PosixBody。書けなくても init は続ける）。
	// tail が空なら、置いた looptrack を呼ばずに PATH の段で終える（kit の正本では init を使えないため）。
	body := fmt.Sprintf(`D="%s" && case "$(uname -s)/$(uname -m)" in %s esac && `+
		`if [ -x "$D/looptrack" ] && echo "$S  $D/looptrack" | sum -c - >/dev/null 2>&1; then echo "%s: $D/looptrack" >&2; `+
		`else mkdir -p "$D" && { curl -fsSL "$U" -o "$D/looptrack.part" && `+
		`echo "$S  $D/looptrack.part" | sum -c - && chmod 755 "$D/looptrack.part" && mv -f "$D/looptrack.part" "$D/looptrack" || `+
		`{ rm -f "$D/looptrack.part"; false; }; }; fi && { %s; }`,
		goPosixDir, strings.Join(cases, " "), i18n.T(g.lang, "server.mcp.setup.fetch.skip_same"), setuppath.PosixBody(g.lang))
	if tail == "" {
		return body
	}
	return body + " && " + goPosixBin + " " + tail
}

// winFetch は PowerShell の取得と init（CPU は PROCESSOR_ARCHITECTURE。ARM64 に arm64 が無ければ amd64 をエミュレーションで使う）。
// 全体を & { } で包む（posixFetch と同じ理由。包まないと $ErrorActionPreference・$ProgressPreference と $U・$S・$D・$B・$P、
// PATH を足す断片の $R・$O・$H・$K が呼び出し元のセッションに残る。スクリプトブロックの中の代入はその中のスコープにだけ効く）。
// 置いた後、置き場が利用者の環境変数 Path に無ければ足す（setuppath.WinBody。書けなくても init は続ける）。
func (g *goSetup) winFetch(tail string) string { return "& { " + g.winFetchBody(tail) + " }" }

// winFetchBody は winFetch の包む前の中身（テストの対照にも使う）。
func (g *goSetup) winFetchBody(tail string) string {
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
	// 配布の無い CPU（32 ビットの PowerShell の x86 など）は、置き場に同じ looptrack.exe があってもここで止まる（理由を出して
	// うるさく落ちるので、踏んだ人はその場で気づける）
	cases = append(cases, fmt.Sprintf(`default { throw "%s: $env:PROCESSOR_ARCHITECTURE" }`,
		i18n.T(g.lang, "server.mcp.setup.fetch.no_dist_cpu")))
	// 置き場の looptrack.exe が配布物と SHA-256 で同じときだけ取得を省く（posixFetch と同じ理由）。Get-FileHash の値は
	// 大文字で、-eq / -ne は大小を区別しないので、小文字の S とそのまま比べられる
	// tail が空なら、置いた looptrack.exe を呼ばずに PATH の段で終える（posixFetchBody と同じ）
	body := fmt.Sprintf(`$ErrorActionPreference = 'Stop'; $ProgressPreference = 'SilentlyContinue'; `+
		`$D = Join-Path $env:LOCALAPPDATA 'Programs\looptrack'; $B = Join-Path $D 'looptrack.exe'; `+
		`switch ($env:PROCESSOR_ARCHITECTURE) { %s }; `+
		`if ((Test-Path -PathType Leaf $B) -and ((Get-FileHash -Algorithm SHA256 -Path $B).Hash -eq $S)) { Write-Host "%s: $B" } `+
		`else { New-Item -ItemType Directory -Force -Path $D | Out-Null; `+
		`$P = Join-Path $D 'looptrack.part'; Invoke-WebRequest -UseBasicParsing -Uri $U -OutFile $P; `+
		`if ((Get-FileHash -Algorithm SHA256 -Path $P).Hash -ne $S) { Remove-Item $P; throw '%s' }; `+
		`Move-Item -Force $P $B }; %s`,
		strings.Join(cases, " "), i18n.T(g.lang, "server.mcp.setup.fetch.skip_same"),
		i18n.T(g.lang, "server.mcp.setup.fetch.sha_mismatch"), setuppath.WinBody(g.lang))
	if tail == "" {
		return body
	}
	return body + "; " + goWinBin + " " + tail
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
// setup の手順のほか、Web の画面（アカウント設定の login の案内）も同じ規則で組む。
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

// goInitCommand は取得の無い位置の init（loop の選択・辞退の案内）。bin は looptrack の呼び方（空なら「issue init …」から。
// 呼び出し側が goSetup.run で置き場の絶対パスを前に付ける）。
func goInitCommand(bin, agent, slug, base, dist string) string {
	return strings.TrimPrefix(fmt.Sprintf("%s issue init --project %s --agent %s --url %s --source server --dist '%s'", bin, slug, agent, base, dist), " ")
}

// fetchStep は looptrack の取得 + init の手順（note は見出しに添える利用者の答え。空なら添えない）。その OS 向けの looptrack を
// 配っていなければ、取得の手順は出せないので、利用者が looptrack を用意した後に実行する init のコマンドを見出しに書いた
// 利用者の手順にする（AI が実行できるコマンドは付けない）。
//
// self は looptrack 自身のリポジトリ（kit の正本）からの通知があった導入（installStateJSON.SelfRepo）。正本に init は要らない
// （kitinit の cli.IsSelfRepo）ので、取得・置き換え・PATH の段だけを返し、init を付けない。正本での init は、いまの
// クライアントでは何もせずに成功で終わるが、それより前のクライアントは拒否する（付けると looptrack は置き換わったのに
// コマンド全体が失敗で終わる）。loopFlag・note は使わない
// （正本には loop を問わない: needLoopAsk）。
func (g *goSetup) fetchStep(lang i18n.Lang, agent, slug, base, dist, loopFlag, note string, self bool) setupStepJSON {
	if self {
		if g.missing != "" {
			return setupStepJSON{Who: "human", Title: i18n.T(lang, "server.mcp.setup.step.fetch_manual_self_repo", "reason", g.missing)}
		}
		c, w := g.both(func(win bool) string {
			if win {
				return g.winFetch("")
			}
			return g.posixFetch("")
		})
		return g.step("ai", i18n.T(lang, "server.mcp.setup.step.fetch_self_repo"), c, w)
	}
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

// staleSteps は looptrack の導入が古いとき（実行ファイルの版か kit の控えが配布物と違う）の手順。
// 取得 + init（--url・--source server・--dist 付き）の 1 つで、実行ファイルを配布物にそろえ、kit と配線を init で直す。
// PATH の looptrack・手元の CLI の既定の接続先・AI の作業環境にだけある環境変数に頼る手順（self-update や --url の無い
// init）は返さない。利用者の端末にはそれらが無く、--url の無い init は CLI の既定（手元のローカルモード）に向くため。
// loop の答えがあれば、setupStepsOf が 1 つ目を答えの旗を付けた取得 + init に置き換える（init は 1 回だけ）。
// self（kit の正本）では取得に init を付けない（fetchStep）。正本の導入済み通知は init からは届かないので、確認の手順
// （issue installed。正本からも self_repo と実行ファイルの版を送る）が置き換えた版を知らせる経路になる。
func (g *goSetup) staleSteps(lang i18n.Lang, agent, slug, base, dist string, self bool) []setupStepJSON {
	return []setupStepJSON{g.fetchStep(lang, agent, slug, base, dist, "", "", self), g.updateConfirm(lang, agent, slug, base)}
}

// updateConfirm は更新の後の確認（任意）。置いた looptrack に、サーバの URL とプロジェクトを環境変数で明示して渡す
// （どの AI でも。値は呼び出し元のシェルに残さない: envPrefixSimple。setupStepsFor の前置は、前置済みのコマンドには重ねない）。
func (g *goSetup) updateConfirm(lang i18n.Lang, agent, slug, base string) setupStepJSON {
	c, w := g.run("issue installed --agent " + agent)
	c, w = envPrefixSimple(c, base, slug, g.isWindows()), envPrefixSimple(w, base, slug, true)
	return g.step("ai", i18n.T(lang, "server.mcp.setup.step.update_confirm"), c, w)
}

// baseSteps は導入状態に合わせた手順（loop の答えに合う手順は setupStepsOf が挟む）。
func (g *goSetup) baseSteps(lang i18n.Lang, agent, slug, base, dist string, st installStateJSON) []setupStepJSON {
	lc, lw := g.run("issue login --browser --url " + base)
	cc, cw := g.run("issue config")
	if needsEnvPrefix(agent) { // Copilot・Codex には環境変数を付けて渡す
		cc, cw = envPrefixSimple(cc, base, slug, g.os == "windows"), envPrefixSimple(cw, base, slug, true)
	}
	// 説明文の中のコマンドも置いた looptrack の絶対パスで書く（サーバ版の利用者の端末に PATH の looptrack は無い）
	pc, pw := g.run("issue login --url " + base)
	dc, dw := g.run("doctor")
	login := g.step("ai", i18n.T(lang, "server.mcp.setup.step.login", "check", joinOS(lang, cc, cw), "url", base,
		"paste", joinOS(lang, pc, pw)), lc, lw)
	confirm := setupStepJSON{Who: "ai", Title: i18n.T(lang, "server.mcp.setup.step.confirm", "doctor", joinOS(lang, dc, dw))}
	switch st.State {
	case "current":
		return []setupStepJSON{{Who: "ai", Title: i18n.T(lang, "server.mcp.setup.step.current")}}
	case "no_hook":
		return []setupStepJSON{login, {Who: "human", Title: approveStep(lang, agent)}, confirm}
	case "stale":
		if st.ClientOS != "" {
			return g.staleSteps(lang, agent, slug, base, dist, st.SelfRepo)
		}
		// 撤去した以前の CLI の導入を looptrack に置き換える: 取得 + init（core・loop の配線を looptrack hook で足す。以前の配線は置き換えずに残し、init が知らせる）
	}
	if agent == agentOther {
		ic, iw := g.run("issue installed --agent other")
		ic, iw = envPrefixSimple(ic, base, slug, g.os == "windows"), envPrefixSimple(iw, base, slug, true)
		return []setupStepJSON{
			g.fetchStep(lang, agent, slug, base, dist, "", "", st.SelfRepo),
			{Who: "ai", Title: i18n.T(lang, "server.mcp.setup.step.other_wire")},
			login,
			g.step("ai", i18n.T(lang, "server.mcp.setup.step.other_installed"), ic, iw),
		}
	}
	return []setupStepJSON{g.fetchStep(lang, agent, slug, base, dist, "", "", st.SelfRepo), login, {Who: "human", Title: approveStep(lang, agent)}, confirm}
}

func joinOS(lang i18n.Lang, posix, win string) string {
	if win == "" {
		return posix
	}
	return i18n.T(lang, "server.mcp.setup.text.or_windows", "posix", posix, "win", win)
}

// loopStep は利用者の答え（answer: yes / no）に合う loop の手順（who: ai・コマンド 1 つ）。問う状態でない・答えが無いときは nil。
// 取得の無い位置（current・no_hook）に挟む（missing・stale では setupStepsOf が取得 + init の旗にする）。
// 置き場の looptrack の絶対パスで実行する（サーバ版の利用者の端末に PATH の looptrack は無い。OS が分からなければ両方）。
func (g *goSetup) loopStep(lang i18n.Lang, agent, slug, base, dist string, st installStateJSON, l distLatest, answer string) *setupStepJSON {
	if answer == "" || !needLoopAsk(agent, st, l) {
		return nil
	}
	ls := &setupStepJSON{Who: "ai", Title: i18n.T(lang, "server.mcp.setup.step.loop_apply", "answer", loopAnswerPhrase(lang, answer))}
	ls.Command, ls.CommandWindows = g.run(goInitCommand("", agent, slug, base, dist) + loopFlag(answer))
	return ls
}
