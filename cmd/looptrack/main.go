// looptrack は Looptrack の単一の実行ファイル（設計は docs/server/DESIGN.md §5-11。クライアントとサーバを 1 つにまとめてある）。
//
// クライアントの操作:
//
//	looptrack issue <サブコマンド> …   イシューの操作（読み取り・変更・init）
//	looptrack hook <名前> [--agent …] …  AI の hook。core の 4 本（issue-freshness-mark・issue-freshness-check・usage・summary。
//	                                   internal/client/hook/core）と loop の hook（internal/client/hook/loop）。
//	                                   名前は重ならない（core を先に引き、無ければ loop）
//	looptrack issue-freshness <mark|check|ack|reset|show> …
//	                                   鮮度ガード（issue-freshness.py の Go 版。mark・check は hook と同じ、ack・reset・show は逃げ道）
//	looptrack gates …                  検証ゲート（kit/loop/scripts/gates.sh の Go 版）
//	looptrack handoff append|compact … 引き継ぎのファイルへの書き込み（append は O_APPEND で足すだけ・
//	                                   compact は全文の書き直しを版照合とロックつきで行う。internal/client/hook/loop）
//	looptrack report pdf --report 集計.json --content 本文.json --out X.pdf
//	                                   トークンレポートの PDF（build_report_pdf.py の Go 版。internal/client/report/pdf）
//	looptrack self-update [--check]     サーバの配布（GET /api/v1/dist の binaries）から最新の looptrack に置き換える
//	                                   （internal/client/selfupdate）
//	looptrack doctor                   PATH・hook の配線・導入の記録・配布の版を確かめる（internal/client/kitinit）
//	looptrack worktree list|prune|mark 作業が終わった作業ツリーとブランチの後始末（internal/client/worktree）
//	looptrack desktop [--background] [--no-tray] [--status] [--quit]
//	                                   デスクトップ版（ローカルモードのサーバ・ブラウザ・トレイ。internal/client/desktop。§5-14）。
//	                                   desktop ビルド（-tags desktop）は引数なしの起動（ダブルクリック）もこれ。headless はトレイなし
//
// サーバの操作（setup・serve・user・member・token・settings・project・secret-key・healthcheck・migrate・import・verify・
// export・verify-files・repair-lists）は server.go の serverCommands。クライアントのサブコマンドと名前は重ならない。
//
//	looptrack version                  版とビルドの種類（headless / desktop）。サーバとクライアントで 1 つ
//
// 環境変数は LOOPTRACK_* だけを読む（internal/client/env。旧名 IM_* は読まない）。
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"slices"
	"strings"

	"github.com/howashoji/looptrack"
	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/cli"
	"github.com/howashoji/looptrack/internal/client/desktop"
	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/hook/core"
	"github.com/howashoji/looptrack/internal/client/hook/loop"
	"github.com/howashoji/looptrack/internal/client/kitinit"
	"github.com/howashoji/looptrack/internal/client/report/pdf"
	"github.com/howashoji/looptrack/internal/client/selfupdate"
	"github.com/howashoji/looptrack/internal/client/worktree"
	"github.com/howashoji/looptrack/internal/i18n"
)

var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], cli.IO{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}, env.OS()))
}

func run(args []string, stdio cli.IO, e env.Env) int {
	api.Version = version
	kitinit.Register() // looptrack issue init（cli から kitinit を import すると循環するため、ここで登録する）
	// 画面に出す文面の言語。渡された環境（env.Env）から決める（テストが指す環境と食い違わないように）
	lang := i18n.FromEnv(func(k string) string { return e.Get(k) })
	if len(args) == 0 || (edition == "desktop" && strings.HasPrefix(args[0], "-psn_")) {
		if edition == "desktop" {
			// ダブルクリック（.app・AppImage・.exe）は引数なし（古い macOS は -psn_…）で起動される
			return runDesktop(args, stdio, e)
		}
		usage(stdio.Stderr, lang)
		return 2
	}
	switch args[0] {
	case "issue":
		return cli.Main(args[1:], stdio, e)
	case "hook":
		// 名前が無い・知らない名前も loop.Main が何もせず 0 で返す（fail-open）
		name := ""
		if len(args) > 1 {
			name = args[1]
		}
		rest := []string{}
		if len(args) > 2 {
			rest = args[2:]
		}
		if _, ok := core.Lookup(name); ok {
			return core.Main(context.Background(), name, rest, stdio.Stdin, stdio.Stdout, stdio.Stderr, &core.Env{Vars: &e})
		}
		return loop.Main(context.Background(), name, rest, stdio.Stdin, stdio.Stdout, stdio.Stderr, nil)
	case "issue-freshness":
		return core.FreshnessMain(context.Background(), args[1:], stdio.Stdin, stdio.Stdout, stdio.Stderr, &core.Env{Vars: &e})
	case "gates":
		return loop.Gates(context.Background(), args[1:], stdio.Stdout, nil)
	case "handoff":
		return loop.Handoff(context.Background(), args[1:], stdio.Stdin, stdio.Stdout, stdio.Stderr, nil)
	case "report":
		return report(args[1:], stdio, e, lang)
	case "self-update":
		if edition == "desktop" && !slices.Contains(args[1:], "--check") {
			// デスクトップ版はアプリ（.app・AppImage・zip）ごと更新する。配布の looptrack は headless なので、置き換えると
			// トレイ・ダブルクリックの起動が無くなる（Windows は GUI の Looptrack.exe がコンソールの実行ファイルに替わる）
			fmt.Fprintln(stdio.Stderr, i18n.T(lang, "cmd.prefix.error", "msg", i18n.T(lang, "cmd.err.desktop_self_update")))
			return 1
		}
		return selfupdate.Main(args[1:], selfupdate.Options{Env: e, Stdout: stdio.Stdout, Stderr: stdio.Stderr, Version: version})
	case "desktop":
		return runDesktop(args[1:], stdio, e)
	case "worktree":
		return worktree.Main(args[1:], stdio.Stdout, stdio.Stderr, e)
	case "doctor":
		return kitinit.Doctor(args[1:], kitinit.DoctorOptions{Env: e, Stdout: stdio.Stdout, Stderr: stdio.Stderr, Version: version,
			Lang: i18n.FromEnv(func(k string) string { return e.Get(k) })})
	case "licenses":
		// 第三者のライセンス文（ルートの NOTICE。埋め込んだフォント BIZ UDGothic の SIL OFL 1.1・Go・依存モジュール）
		fmt.Fprint(stdio.Stdout, looptrack.Notice)
		return 0
	case "version", "--version":
		fmt.Fprintln(stdio.Stdout, i18n.T(lang, "cmd.version", "version", version, "edition", edition, "os", runtime.GOOS, "arch", runtime.GOARCH))
		return 0
	case "help", "-h", "--help":
		usage(stdio.Stdout, lang)
		return 0
	}
	if cmd, ok := serverCommands[args[0]]; ok {
		// サーバの操作は os の標準入出力と環境変数を直に使う（パスワードの入力は端末から読む）
		return cmd(args[1:])
	}
	fmt.Fprintln(stdio.Stderr, i18n.T(lang, "cmd.prefix.error", "msg", i18n.T(lang, "cmd.err.unknown_subcommand", "name", args[0])))
	usage(stdio.Stderr, lang)
	return 2
}

func usage(w io.Writer, lang i18n.Lang) {
	fmt.Fprint(w, i18n.T(lang, "cmd.usage.client"), serverUsage(lang), i18n.T(lang, "cmd.usage.common"))
}

// runDesktop は looptrack desktop（desktop ビルドはトレイつき。headless はトレイなし）。
func runDesktop(args []string, stdio cli.IO, e env.Env) int {
	return desktop.Main(args, desktop.Options{
		Version: version, Env: e, Stdout: stdio.Stdout, Stderr: stdio.Stderr, UI: desktopUI(),
	})
}

// report は looptrack report <種類>。今は pdf だけ。
func report(args []string, stdio cli.IO, e env.Env, lang i18n.Lang) int {
	if len(args) == 0 || args[0] != "pdf" {
		fmt.Fprintln(stdio.Stderr, i18n.T(lang, "cmd.usage.report_pdf"))
		return 2
	}
	home, _ := os.UserHomeDir()
	return pdf.Main(args[1:], stdio.Stdout, stdio.Stderr, pdf.Env{Getenv: e.Get, Home: home, Lang: lang})
}
