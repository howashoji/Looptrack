// Package docscheck は文書（docs/ と kit/）の形と、文書とコードの食い違いを確かめるテストだけを置く（コードは無い）。
//
//   - guide_test.go: 利用者ガイド（docs/guide/ と docs/guide/ja/）の日英の構成・禁止語・相対リンク
//     （以前は docs/guide/ の別のスクリプトで確かめていた。今は Go のテストで行う）
//   - readme_test.go: README.md と README.ja.md の日英の構成
//   - kit_test.go: 配る kit（kit/core・kit/loop）と kit 自体の説明の日英の構成（正本は日本語、訳は同じ
//     ディレクトリの en/。kit/README だけは例外で、kit/README.ja.md が正本・kit/README.md が訳）
//   - publicscan_test.go: 公開物の検査（deploy/public-scan.sh）を呼ぶ（Windows では skip）
//   - publicscan_scope_test.go: その検査が「公開物に入るものを調べ、入らないものは調べない」ことを
//     使い捨ての git リポジトリで確かめる。AI ツールの設定（.claude/・CLAUDE.md など）が公開物に入ったら落ちることも（Windows では skip）
//   - embedscan_test.go: 実行ファイルに埋め込むもの（//go:embed）に配るつもりのないものが混ざっていないこと
//   - removedids_test.go: 撤去した ID（ルールのキー・エラーコード・対訳キー・サブコマンド名）の残骸が無いこと
//   - mcptoolsets_test.go: 「イシューを変える MCP のツール」の一覧が 4 か所（サーバの登録・付与の hook・
//     loop の PreToolUse・DESIGN.md）でずれていないこと。実物を読み取って 1 つの宣言表と突き合わせる
//   - shellvar_test.go: シェルスクリプト（*.sh）で、波かっこの無い変数展開の直後に ASCII 以外の文字が無いこと
//     （macOS の bash はその先頭のバイトを変数名の続きとして読む。Linux では再現しないので、字面で見る）
//   - doclist_test.go: この一覧と、実在する *_test.go が一致すること
//
// 実行: go test ./internal/docscheck/
package docscheck
