//go:build !desktop

package main

import "github.com/howashoji/looptrack/internal/client/desktop"

// edition はビルドの種類。既定は headless（cgo なし。サーバ・CI・端末）。トレイつきの desktop は -tags desktop。
const edition = "headless"

// desktopUI は headless ではトレイを出さない（looptrack desktop はサーバとブラウザだけ。止めるのは --quit かシグナル）。
func desktopUI() desktop.UI { return nil }
