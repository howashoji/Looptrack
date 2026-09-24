// Package icon はデスクトップ版のアイコン（**仮のもの**）。
//
// このディレクトリのファイルが唯一の置き場: トレイ（ここで埋め込む）と、配布物の組み立て（deploy/release/desktop/ の
// スクリプトが .app の Looptrack.icns・AppImage の looptrack.png・Windows の .exe に埋め込む app.ico として使う）。
// 差し替えるときは同じ名前・同じ形式のファイルを置く（作り方は gen/main.go。今のものは go run ./internal/client/desktop/icon/gen で作った）。
package icon

import _ "embed"

// TrayTemplate は macOS のメニューバーのテンプレート画像（黒と透明。表示の明暗に合わせて OS が色を変える）。
//
//go:embed tray_template.png
var TrayTemplate []byte

// TrayPNG は Linux のトレイ（StatusNotifierItem）の画像。
//
//go:embed tray.png
var TrayPNG []byte

// TrayICO は Windows の通知領域の画像（.ico の中身が要る）。
//
//go:embed tray.ico
var TrayICO []byte
