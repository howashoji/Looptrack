# PDF の日本語フォント（埋め込み用の置き場）

`looptrack report pdf`（トークンレポートの PDF）は、このディレクトリの `*.ttf` を実行ファイルに埋め込んで使います。
埋め込むのは `//go:embed fonts` です。OS のフォントを探すと、列幅の規則が OS や言語設定で崩れるためです（DESIGN.md §5-11 Q4）。
ファイルが無くてもビルドは通ります。そのときは `TOKEN_REPORT_FONT`（TrueType のパス）→ 手元の既定の置き場の順に探します。

置いたものは次のとおりです。2026-09-19 に利用者の承認を得て、v1.051 のタグから取得しました。SHA-256 は下の表にあります。

| ファイル | 取得元 | ライセンス |
| -- | -- | -- |
| `BIZUDGothic-Regular.ttf` | BIZ UDGothic（Morisawa）の公式リポジトリ `googlefonts/morisawa-biz-ud-gothic` の Releases（`fonts/ttf/BIZUDGothic-Regular.ttf`）。Google Fonts（fonts.google.com/specimen/BIZ+UDGothic）の配布物も同じ | SIL Open Font License 1.1 |
| `OFL.txt` | 同じリポジトリの `OFL.txt`（著作権表示つきのライセンス文） | — |

- TrueType（glyf）のフォントを使ってください。gopdf は CFF（OpenType の OTTO）を読めません。TTC なら最初のフォントを取り出して使います。
- OFL は、フォントを同梱して配るときにライセンス文を添えるよう求めています。配布物にも `OFL.txt` を入れます。
- 使うのは Regular の 1 つだけです。以前の PDF の作り方と同じで、太字は使っていません。

| ファイル | 版 | SHA-256 |
| -- | -- | -- |
| `BIZUDGothic-Regular.ttf` | v1.051（4,667,380 バイト） | `7d2b48d84ef4e65c9f85bcf65fb1fb41c92b134be641fab6a7141d597c9a92b0` |
| `OFL.txt` | v1.051 | `e753d7155d53c747d037a445e584c8ecfca6dd79846db610417e282a736b28bc` |
