// Package browser は URL を利用者のブラウザで開く（login --browser）。
//
// 順は以前の CLI（1.0.0 より前）に合わせる:
//
//  1. 環境変数 BROWSER（OS のパス区切りで複数。前から試す）。値に %s があれば空白で区切った引数の %s を URL に置き換え、
//     無ければ値全体を実行ファイルの名前として URL を最後の引数に渡す。
//     コマンドの終了を待ち、終了コード 0 なら開けたとみなす。テストはここにブラウザの代わりを入れる。
//  2. OS の既定: macOS は open、Windows は rundll32 url.dll,FileProtocolHandler（シェルを通さないので URL の & を解釈しない）、
//     それ以外は xdg-open などの最初に見つかったもの。Linux などで表示の無い環境（DISPLAY・WAYLAND_DISPLAY が無く WSL でもない）は
//     開かない（端末の中でテキストのブラウザが立ち上がるのを避ける）。こちらは起動できたら開けたとみなし、終了は待たない。
//
// 開けなくても誤りにはしない（呼び出し側は先に URL を表示し、利用者がそれを開けば続けられる）。
// クライアント側のパッケージなので、コマンドを起動してよい（internal/ のサーバ側からは import しない。TestServerNeverExecutes）。
package browser

import (
	"os/exec"
	"runtime"
	"strings"

	"github.com/howashoji/looptrack/internal/client/env"
)

// Command は起動するコマンド（実行ファイルと引数）。Wait なら終了を待って終了コードで判断する。
type Command struct {
	Argv []string
	Wait bool
}

// Plan は url を開くために順に試すコマンド（BROWSER の各項目 → OS の既定）。見つからないものは含めない。
func Plan(e env.Env, goos, url string) []Command {
	var out []Command
	sep := ":" // os.PathListSeparator（他の OS の分もテストで確かめられるよう goos で決める）
	if goos == "windows" {
		sep = ";"
	}
	for _, item := range strings.Split(e.Get("BROWSER"), sep) {
		if strings.TrimSpace(item) == "" {
			continue
		}
		var argv []string
		if strings.Contains(item, "%s") {
			for _, f := range strings.Fields(item) {
				argv = append(argv, strings.ReplaceAll(f, "%s", url))
			}
		} else {
			argv = []string{item, url}
		}
		out = append(out, Command{Argv: argv, Wait: true})
	}
	switch goos {
	case "darwin":
		out = append(out, Command{Argv: []string{"open", url}})
	case "windows":
		out = append(out, Command{Argv: []string{"rundll32", "url.dll,FileProtocolHandler", url}})
	default:
		if e.Get("DISPLAY") == "" && e.Get("WAYLAND_DISPLAY") == "" && e.Get("WSL_DISTRO_NAME") == "" {
			break
		}
		for _, name := range []string{"xdg-open", "wslview", "sensible-browser", "x-www-browser"} {
			out = append(out, Command{Argv: []string{name, url}})
		}
	}
	return out
}

// Open は url をブラウザで開く。開けたら true。
func Open(e env.Env, url string) bool {
	for _, c := range Plan(e, runtime.GOOS, url) {
		if run(c) {
			return true
		}
	}
	return false
}

func run(c Command) bool {
	path, err := exec.LookPath(c.Argv[0])
	if err != nil {
		return false
	}
	cmd := exec.Command(path, c.Argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if c.Wait {
		return cmd.Run() == nil
	}
	if err := cmd.Start(); err != nil {
		return false
	}
	go cmd.Wait() // 終了を待たずに刈り取る
	return true
}
