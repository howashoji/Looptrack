//go:build desktop

package tray

import (
	"github.com/howashoji/looptrack/internal/client/desktop"
	"github.com/howashoji/looptrack/internal/i18n"
)

// メニューの項目（onReady がこの値を systray に渡す。並びは menuOrder）。
//
// 定義をここに 1 か所にまとめてあるので、文面を変えるとテスト（menu_test.go）も同じ値を見る。
// 並びだけはテストが別に固定している（項目が増減したときに気づくため）。
type menuItem struct {
	Label string
	Tip   string // 空なら説明なし。URL・アプリ名が入るものは onReady が組み立てる
}

// 各項目の文面（利用者の言語で作る。ID は文字列リテラルで渡す）。
func miOpen(lang i18n.Lang) menuItem { return menuItem{Label: i18n.T(lang, "desktop.menu.open")} }

// miSettings は「設定」（ブラウザでアカウント設定の画面 /account を開く。App.OpenSettings）。
func miSettings(lang i18n.Lang) menuItem {
	return menuItem{Label: i18n.T(lang, "desktop.menu.settings"), Tip: i18n.T(lang, "desktop.menu.settings.tip")}
}

func miCopy(lang i18n.Lang) menuItem {
	return menuItem{Label: i18n.T(lang, "desktop.menu.copy_mcp"), Tip: i18n.T(lang, "desktop.menu.copy_mcp.tip")}
}

func miShowMCP(lang i18n.Lang) menuItem {
	return menuItem{Label: i18n.T(lang, "desktop.menu.show_mcp")}
}

func miCLI(lang i18n.Lang) menuItem {
	return menuItem{Label: i18n.T(lang, "desktop.menu.install_cli"), Tip: i18n.T(lang, "desktop.menu.install_cli.tip")}
}

func miAuto(lang i18n.Lang) menuItem { return menuItem{Label: i18n.T(lang, "desktop.menu.autostart")} }

func miQuit(lang i18n.Lang) menuItem { return menuItem{Label: i18n.T(lang, "desktop.menu.quit")} }

// miVersion は版の表示（クリックできない・onReady が Disable する）。main.version をそのまま見せるので、
// rc のビルド（例 v1.0.0-rc.2）でも正式版と区別できる。ホバーのツールチップ（tray.go の SetTooltip）と
// 違い、開かなくても常に見える項目として出す。
func miVersion(lang i18n.Lang, version string) menuItem {
	return menuItem{Label: i18n.T(lang, "desktop.menu.version", "version", version)}
}

// menuOrder はトレイのメニューの上からの並び（onReady が足す順と同じ）。
// 「設定」は「画面を開く」の次（ブラウザで開く操作をひとまとめにする）。
// 「AI の接続設定をコピー」は下位のメニューを持ち、その中は MCPLabels の順 →（区切り）→「接続設定の画面を開く」。
// miVersion（版の表示）は文面に実行時の版の文字列が入るのでここには含めない。onReady では
// miAuto と miQuit（区切りの下）の間に置く。文面（rc を含む版がそのまま入ること）は
// TestVersionItemLabel が固定する。
func menuOrder(lang i18n.Lang) []menuItem {
	return []menuItem{miOpen(lang), miSettings(lang), miCopy(lang), miCLI(lang), miAuto(lang), miQuit(lang)}
}

// quitTip は「終了」の説明（アプリ名が入る）。
func quitTip(lang i18n.Lang) string {
	return i18n.T(lang, "desktop.menu.quit.tip", "app", desktop.AppName)
}
