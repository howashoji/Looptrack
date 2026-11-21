package kitinit

import (
	"path/filepath"
	"strings"

	"github.com/howashoji/looptrack/internal/i18n"
)

// desktopBundled は exe が Windows のデスクトップ版に同梱の CLI（<アプリ>\cli\looptrack.exe。cli の親に Looptrack.exe がある）か。
// 同梱の CLI は PATH に入っていない。PATH から呼べるようにするのはトレイメニューの「CLI を使えるようにする」で、
// %LOCALAPPDATA%\Programs\looptrack\looptrack.exe に写す（internal/client/desktop の CLIInstall）。そこで init と doctor の
// PATH の案内は、同梱の CLI から動いているときはそのメニューと写し先を示す。goos・exists はテストで差し替える。
// macOS・Linux のデスクトップ版は実行ファイルそのものが CLI で、メニューは ~/.local/bin にリンクを置く（binDirHint のまま）。
func desktopBundled(goos, exe string, exists func(string) bool) bool {
	if goos != "windows" || exe == "" {
		return false
	}
	dir := filepath.Dir(exe)
	if !strings.EqualFold(filepath.Base(dir), "cli") || !strings.EqualFold(filepath.Base(exe), "looptrack.exe") {
		return false
	}
	return exists(filepath.Join(filepath.Dir(dir), "Looptrack.exe"))
}

// desktopMenu はトレイメニューの「CLI を使えるようにする」の文言（デスクトップ版のメニューと同じ対訳表の語）。
func desktopMenu(lang i18n.Lang) string { return i18n.T(lang, "desktop.menu.install_cli") }

// wiredAbsNote は init が絶対パスで配線したときの注記（同梱の CLI ならメニューと写し先を示す）。
func wiredAbsNote(lang i18n.Lang, exe string, desktop bool) string {
	if desktop {
		return i18n.T(lang, "kitinit.init.note.wired_abs_desktop", "exe", exe, "bindir", winBinDir, "menu", desktopMenu(lang))
	}
	return i18n.T(lang, "kitinit.init.note.wired_abs", "exe", exe, "bindir", binDirHint())
}
