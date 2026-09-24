//go:build desktop

package main

import (
	"github.com/howashoji/looptrack/internal/client/desktop"
	"github.com/howashoji/looptrack/internal/client/desktop/tray"
)

// edition はビルドの種類（-tags desktop。トレイつき・引数なしの起動はデスクトップ版）。
const edition = "desktop"

// desktopUI はトレイ / メニューバー（fyne.io/systray。macOS は cgo）。
func desktopUI() desktop.UI { return tray.New() }
