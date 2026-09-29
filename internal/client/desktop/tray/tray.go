//go:build desktop

// Package tray はデスクトップ版のトレイ / メニューバー（fyne.io/systray・Apache-2.0。DESIGN.md §5-4）。
//
// macOS は cgo（Cocoa）、Windows・Linux は cgo なし（Windows は Win32 API、Linux は D-Bus の StatusNotifierItem）。
// -tags desktop のときだけビルドする（headless のビルド・CI の cgo なしのクロスビルドには入らない）。
//
// どの OS でも通知領域のアイコンの**左クリックでメニューを出す**（systray の既定の動き。SetOnTapped は使わない）。
//
// メニュー:
//
//	新しい版 <版> に更新する（新しい版を知らせる間だけ。選ぶと取得・確認・置き換え・起動し直し。
//	                          置き換えられないときは「新しい版 <版> があります」で、選ぶとリリースのページを開く）
//	画面を開く
//	設定
//	AI の接続設定をコピー ▸ Claude Code（ターミナルで実行）… / 接続設定の画面を開く
//	CLI を使えるようにする
//	アプリ一覧に登録する（チェック。Linux の AppImage でだけ出す）
//	ログイン時に起動する（チェック）
//	新しい版を確認する（チェック。外すと確認を止める）
//	新しい版を自動で入れる（チェック。既定は外れている。置き換えられる環境でだけ出す）
//	終了
package tray

import (
	"runtime"
	"sync/atomic"

	"fyne.io/systray"

	"github.com/howashoji/looptrack/internal/client/desktop"
	"github.com/howashoji/looptrack/internal/client/desktop/icon"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/updatecheck"
)

// Tray は desktop.UI の実装。
type Tray struct{}

// New はトレイを作る。
func New() *Tray { return &Tray{} }

// Run はトレイを出し、a.Quit（「終了」・シグナル・サーバの異常）まで戻らない。macOS はメインスレッドで呼ぶ
// （systray の init が固定している）。Quit を見て systray.Quit するのは onReady の中（イベントループが動いてから）にする
// （ループの前に Quit すると Run が戻らない）。
func (t *Tray) Run(a *desktop.App) {
	if ok, why := available(); !ok {
		// トレイを出せない環境: サーバだけ動かす（止めるのは looptrack desktop --quit かシグナル）
		a.Logf("%s", i18n.T(a.Lang(), "desktop.tray.unavailable", "reason", why))
		<-a.Done()
		return
	}
	systray.Run(func() {
		onReady(a)
		go func() {
			<-a.Done()
			systray.Quit()
		}()
	}, nil)
}

func onReady(a *desktop.App) {
	switch runtime.GOOS {
	case "darwin":
		systray.SetTemplateIcon(icon.TrayTemplate, icon.TrayTemplate)
	case "windows":
		systray.SetIcon(icon.TrayICO)
	default:
		systray.SetIcon(icon.TrayPNG)
		systray.SetTitle(desktop.AppName)
	}
	systray.SetTooltip(desktop.AppName + " " + a.Version())
	a.MarkTrayShown()       // 画面の帯の止め方の案内をトレイのメニューにする
	installReopen(a.OpenUI) // macOS: 起動中の .app をもう一度開いたら（Dock・Finder・open）画面を開く

	// 項目と並びは menu.go（menuOrder がテストで固定されている）
	lang := a.Lang()
	// 先頭は新しい版の知らせ（知らせが無い間は隠す。systray は後から途中に項目を差し込めないので、先に作って出し入れする）
	update := systray.AddMenuItem(miUpdate(lang, "").Label, miUpdate(lang, "").Tip)
	var updateBusy atomic.Bool // 置き換えの途中（文面を「取得しています」にしたまま、知らせの変化で上書きしない）
	showUpdate := func(n *updatecheck.Notice) {
		if updateBusy.Load() {
			return
		}
		if n == nil {
			update.Hide()
			return
		}
		it := miUpdate(lang, n.Version)
		if a.UpdateReplaceable() {
			it = miUpdateInstall(lang, n.Version)
		}
		update.SetTitle(it.Label)
		update.SetTooltip(it.Tip)
		update.Enable()
		update.Show()
	}
	a.OnUpdateNotice(showUpdate) // 先に見始めてから今の知らせを当てる（間の変化を取りこぼさない）
	showUpdate(a.UpdateNotice())
	open := systray.AddMenuItem(miOpen(lang).Label, a.URL())
	se := miSettings(lang)
	settings := systray.AddMenuItem(se.Label, se.Tip)
	cp := miCopy(lang)
	copyMenu := systray.AddMenuItem(cp.Label, cp.Tip)
	var copies []*systray.MenuItem
	for _, label := range desktop.MCPLabels(lang) {
		copies = append(copies, copyMenu.AddSubMenuItem(label, ""))
	}
	copyMenu.AddSeparator()
	sm := miShowMCP(lang)
	showMCP := copyMenu.AddSubMenuItem(sm.Label, sm.Tip)
	ci := miCLI(lang)
	cli := systray.AddMenuItem(ci.Label, ci.Tip)
	am := miAppMenu(lang)
	appMenu := systray.AddMenuItemCheckbox(am.Label, am.Tip, a.AppMenuEnabled())
	if !a.AppMenuSupported() {
		appMenu.Hide() // Linux の AppImage でだけ（ほかの OS は .app・インストーラがアプリ一覧に出す）
	}
	au := miAuto(lang)
	auto := systray.AddMenuItemCheckbox(au.Label, au.Tip, a.AutostartEnabled())
	uc := miUpdateCheck(lang)
	updateCheck := systray.AddMenuItemCheckbox(uc.Label, uc.Tip, a.UpdateCheckEnabled())
	if a.UpdateCheckEnvOff() {
		// 環境変数で止めているときは、メニューでは入れられない（控えの切り替えより環境変数が勝つ）
		updateCheck.SetTooltip(updateCheckEnvOffTip(lang))
		updateCheck.Disable()
	}
	ua := miUpdateAuto(lang)
	updateAuto := systray.AddMenuItemCheckbox(ua.Label, ua.Tip, a.UpdateAutoEnabled())
	if !a.ReplaceSupported() {
		updateAuto.Hide() // Windows・.app / AppImage の外では置き換えない
	}
	// 版の表示（クリックできない）。rc を含む版をホバーのツールチップだけでなく、開かなくても
	// 見える項目としても出す（利用者の決定: 画面にも rc を含む版を出す）。
	ver := systray.AddMenuItem(miVersion(lang, a.Version()).Label, "")
	ver.Disable()
	systray.AddSeparator()
	quit := systray.AddMenuItem(miQuit(lang).Label, quitTip(lang))

	for i, it := range copies {
		go func(i int, it *systray.MenuItem) {
			for range it.ClickedCh {
				a.CopyMCP(i)
			}
		}(i, it)
	}
	go func() {
		for {
			select {
			case <-update.ClickedCh:
				n := a.UpdateNotice()
				if n == nil || !a.UpdateReplaceable() {
					a.OpenUpdate()
					continue
				}
				// 取得と確認は時間がかかるので、ほかの項目を止めないよう別の goroutine で行う（その間は押せなくする）
				updateBusy.Store(true)
				update.SetTitle(miUpdateBusy(lang, n.Version).Label)
				update.Disable()
				go func() {
					if !a.ApplyUpdate() { // true なら起動し直すので、このインスタンスは終わる
						updateBusy.Store(false)
						showUpdate(a.UpdateNotice())
					}
				}()
			case <-open.ClickedCh:
				a.OpenUI()
			case <-settings.ClickedCh:
				a.OpenSettings()
			case <-showMCP.ClickedCh:
				a.OpenPath("/first-run/done")
			case <-cli.ClickedCh:
				a.InstallCLI()
			case <-appMenu.ClickedCh:
				if a.SetAppMenu(!appMenu.Checked()) {
					appMenu.Check()
				} else {
					appMenu.Uncheck()
				}
			case <-auto.ClickedCh:
				if a.SetAutostart(!auto.Checked()) {
					auto.Check()
				} else {
					auto.Uncheck()
				}
			case <-updateCheck.ClickedCh:
				if a.SetUpdateCheck(!updateCheck.Checked()) {
					updateCheck.Check()
				} else {
					updateCheck.Uncheck()
				}
			case <-updateAuto.ClickedCh:
				if a.SetUpdateAuto(!updateAuto.Checked()) {
					updateAuto.Check()
				} else {
					updateAuto.Uncheck()
				}
			case <-quit.ClickedCh:
				a.Quit()
				return
			}
		}
	}()
}
