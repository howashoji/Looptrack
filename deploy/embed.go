// Package deploy は配置用のファイルを持つ。grants.sql は looptrack grants（internal/dbgrants。インストーラが MySQL の
// 最小権限を与えるのに使う）と統合テストから、rules/ はプロジェクト別ルールの設定（`looptrack project rules set`）とテストから使うため埋め込む。
package deploy

import (
	"embed"
	_ "embed"
)

// GrantsSQL はアプリ用 DB ユーザー im_app の権限定義（DB 名・利用者名は internal/dbgrants が接続先のものに置き換える）。
//
//go:embed grants.sql
var GrantsSQL string

// Rules はプロジェクト別ルールの設定（rules/<slug>.json）。
//
//go:embed rules/*.json
var Rules embed.FS
