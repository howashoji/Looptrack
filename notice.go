// Package looptrack はリポジトリのルートに置くファイルを埋め込む。
package looptrack

import _ "embed"

// Notice は配布物に添える第三者のライセンス文（ルートの NOTICE。go run ./internal/tools/notice が作る）。
// looptrack licenses が表示する。
//
//go:embed NOTICE
var Notice string
