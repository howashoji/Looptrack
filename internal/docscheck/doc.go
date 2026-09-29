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
//   - publicscan_python_test.go: 「Python の名残」検査（python_left）が旧実装に固有の語だけを狙い、
//     文書中の一般的な python への言及（語そのもの・URL・バージョン表記）には当たらないことを確かめる
//   - embedscan_test.go: 実行ファイルに埋め込むもの（//go:embed）に配るつもりのないものが混ざっていないこと
//   - removedids_test.go: 撤去した ID（ルールのキー・エラーコード・対訳キー・サブコマンド名）の残骸が無いこと
//   - mcptoolsets_test.go: 「イシューを変える MCP のツール」の一覧が 4 か所（サーバの登録・付与の hook・
//     loop の PreToolUse・DESIGN.md）でずれていないこと。実物を読み取って 1 つの宣言表と突き合わせる
//   - shellvar_test.go: シェルスクリプト（*.sh）で、波かっこの無い変数展開の直後に ASCII 以外の文字が無いこと
//     （macOS の bash はその先頭のバイトを変数名の続きとして読む。Linux では再現しないので、字面で見る）
//   - doclist_test.go: この一覧と、実在する *_test.go が一致すること
//   - i18n_docref_test.go: 対訳表（internal/i18n/{ja,en}.json）が案内する docs/guide/ の節が実物にあること
//   - designref_test.go: サーバの設計書（docs/server/DESIGN.md）の節番号の参照（「§」+ 番号。直後に「」の名前があればその名前も）が、
//     いまの DESIGN.md の見出しに実在すること。DESIGN.md の本文と、公開物の中の DESIGN を含む行を見る
//   - desktopversion_test.go: デスクトップ版の版の変換（deploy/release/desktop_version_test.sh。
//     plist_version・iss_version）を呼ぶ（Windows では skip）
//   - testrecord_test.go: 全検査の記録の 1 行を作るスクリプトの集計（deploy/dev/test-record_test.sh。
//     --parse と --count-ps を合成のログと ps の出力で確かめる）を呼ぶ（Windows では skip）
//
// 実行: go test ./internal/docscheck/
package docscheck
