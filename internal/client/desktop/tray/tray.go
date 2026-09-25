//go:build desktop

// Package tray はデスクトップ版のトレイ / メニューバー（fyne.io/systray・Apache-2.0。DESIGN.md §5-14）。
//
// macOS は cgo（Cocoa）、Windows・Linux は cgo なし（Windows は Win32 API、Linux は D-Bus の StatusNotifierItem）。
// -tags desktop のときだけビルドする（headless のビルド・CI の cgo なしのクロスビルドには入らない）。
//
// どの OS でも通知領域のアイコンの**左クリックでメニューを出す**（systray の既定の動き。SetOnTapped は使わない）。
//
// メニュー:
//
//	画面を開く
//	設定
//	AI の接続設定をコピー ▸ Claude Code（ターミナルで実行）… / 接続設定の画面を開く
//	CLI を使えるようにする
//	ログイン時に起動する（チェック）
//	終了
package tray

import (
	"runtime"

	"fyne.io/systray"

	"github.com/howashoji/looptrack/internal/client/desktop"
	"github.com/howashoji/looptrack/internal/client/desktop/icon"
	"github.com/howashoji/looptrack/internal/i18n"
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
	installReopen(a.OpenUI) // macOS: 起動中の .app をもう一度開いたら（Dock・Finder・open）画面を開く

	// 項目と並びは menu.go（menuOrder がテストで固定されている）
	lang := a.Lang()
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
	au := miAuto(lang)
	auto := systray.AddMenuItemCheckbox(au.Label, au.Tip, a.AutostartEnabled())
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
			case <-open.ClickedCh:
				a.OpenUI()
			case <-settings.ClickedCh:
				a.OpenSettings()
			case <-showMCP.ClickedCh:
				a.OpenPath("/first-run/done")
			case <-cli.ClickedCh:
				a.InstallCLI()
			case <-auto.ClickedCh:
				if a.SetAutostart(!auto.Checked()) {
					auto.Check()
				} else {
					auto.Uncheck()
				}
			case <-quit.ClickedCh:
				a.Quit()
				return
			}
		}
	}()
}
