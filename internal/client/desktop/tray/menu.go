//go:build desktop

package tray

import (
	"github.com/howashoji/looptrack/internal/client/desktop"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/updatecheck"
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

// miAppMenu は「アプリ一覧に登録する」（チェック。Linux の AppImage でだけ出す。App.SetAppMenu）。
func miAppMenu(lang i18n.Lang) menuItem {
	return menuItem{Label: i18n.T(lang, "desktop.menu.app_menu"), Tip: i18n.T(lang, "desktop.menu.app_menu.tip")}
}

func miAuto(lang i18n.Lang) menuItem { return menuItem{Label: i18n.T(lang, "desktop.menu.autostart")} }

// miUpdateCheck は「新しい版を確認する」（チェック。外すと確認を止める。App.SetUpdateCheck）。
func miUpdateCheck(lang i18n.Lang) menuItem {
	return menuItem{Label: i18n.T(lang, "desktop.menu.update_check"), Tip: i18n.T(lang, "desktop.menu.update_check.tip")}
}

// updateCheckEnvOffTip は環境変数で確認を止めているときの「新しい版を確認する」の説明（チェックは押せなくする）。
func updateCheckEnvOffTip(lang i18n.Lang) string {
	return i18n.T(lang, "desktop.menu.update_check.env_off", "name", updatecheck.EnvCheck)
}

// miUpdate はメニューの先頭の「新しい版 <版> があります」（新しい版を知らせる間だけ出す。選ぶとリリースのページを開く。
// App.OpenUpdate）。文面に版が入るので menuOrder には含めない（onReady が最初に足し、知らせが無い間は隠す）。
func miUpdate(lang i18n.Lang, version string) menuItem {
	return menuItem{Label: i18n.T(lang, "desktop.menu.update_available", "version", version), Tip: i18n.T(lang, "desktop.menu.update_available.tip")}
}

// miUpdateInstall はメニューの先頭の「新しい版 <版> に更新する」（1 クリックで置き換えられるときの miUpdate。選ぶと取得・確認・
// 置き換え・起動し直し。App.UpdateClicked）。miUpdateBusy はその間の文面（押せなくする）。
func miUpdateInstall(lang i18n.Lang, version string) menuItem {
	return menuItem{Label: i18n.T(lang, "desktop.menu.update_install", "version", version), Tip: i18n.T(lang, "desktop.menu.update_install.tip")}
}

func miUpdateBusy(lang i18n.Lang, version string) menuItem {
	return menuItem{Label: i18n.T(lang, "desktop.menu.update_busy", "version", version)}
}

// miUpdateAuto は「新しい版を自動で入れる」（チェック。既定は外れている。置き換えられる環境＝macOS の .app・Linux の AppImage
// でだけ出す。App.SetUpdateAuto）。
func miUpdateAuto(lang i18n.Lang) menuItem {
	return menuItem{Label: i18n.T(lang, "desktop.menu.update_auto"), Tip: i18n.T(lang, "desktop.menu.update_auto.tip")}
}

func miQuit(lang i18n.Lang) menuItem { return menuItem{Label: i18n.T(lang, "desktop.menu.quit")} }

// miVersion は版の表示（クリックできない・onReady が Disable する）。main.version をそのまま見せるので、
// rc のビルド（例 v1.0.0-rc.2）でも正式版と区別できる。ホバーのツールチップ（tray.go の SetTooltip）と
// 違い、開かなくても常に見える項目として出す。
func miVersion(lang i18n.Lang, version string) menuItem {
	return menuItem{Label: i18n.T(lang, "desktop.menu.version", "version", version)}
}

// menuOrder はトレイのメニューの上からの並び（onReady が足す順と同じ）。
// 新しい版を知らせる間だけ、この並びの上に miUpdate が出る（文面に版が入るのでここには含めない。TestUpdateItemLabel が固定する）。
// 「設定」は「画面を開く」の次（ブラウザで開く操作をひとまとめにする）。
// 「アプリ一覧に登録する」は「CLI を使えるようにする」の次で、Linux の AppImage でないときは隠す。
// 「新しい版を確認する」は「ログイン時に起動する」の次（どちらもチェックの切り替え）。「新しい版を自動で入れる」はその次で、
// 置き換えられない環境（Windows・.app / AppImage の外）では隠す。
// 「AI の接続設定をコピー」は下位のメニューを持ち、その中は MCPLabels の順 →（区切り）→「接続設定の画面を開く」。
// miVersion（版の表示）は文面に実行時の版の文字列が入るのでここには含めない。onReady では
// miAuto と miQuit（区切りの下）の間に置く。文面（rc を含む版がそのまま入ること）は
// TestVersionItemLabel が固定する。
func menuOrder(lang i18n.Lang) []menuItem {
	return []menuItem{miOpen(lang), miSettings(lang), miCopy(lang), miCLI(lang), miAppMenu(lang), miAuto(lang), miUpdateCheck(lang), miUpdateAuto(lang), miQuit(lang)}
}

// quitTip は「終了」の説明（アプリ名が入る）。
func quitTip(lang i18n.Lang) string {
	return i18n.T(lang, "desktop.menu.quit.tip", "app", desktop.AppName)
}
