// Package deploy は配置用のファイルを持つ。grants.sql は統合テストから、
// rules/ はプロジェクト別ルールの設定（`looptrack project rules set`）とテストから使うため埋め込む。
package deploy

import (
	"embed"
	_ "embed"
)

// GrantsSQL はアプリ用 DB ユーザー im_app の権限定義。
//
//go:embed grants.sql
var GrantsSQL string

// Rules はプロジェクト別ルールの設定（rules/<slug>.json）。
//
//go:embed rules/*.json
var Rules embed.FS
