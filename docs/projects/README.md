# プロジェクト別の運用ルール

ここには、各プロジェクト固有の運用ルールの元の定義を置きます。型の使い分け・ラベルの使い方・プロジェクト側の規約など。
共通のルールは [../AI-GUIDE.md](../AI-GUIDE.md) に。新しく作るなら、テンプレート [../templates/project-rules.md](../templates/project-rules.md) から始めてください。

置き場はどこでもかまいません。プロジェクトのリポジトリでも、運用文書をまとめたリポジトリでも使えます。
ただ、ここに置くのは公開の例だけ。実際のプロジェクトの運用文書は、そのプロジェクトの側に置いてください。

**直したら、サーバに登録し直すこと。** `looptrack issue guide` と MCP の `guide` が AI へ返すのは、登録した内容のほうです。
`looptrack project guide set <slug> - --source docs/projects/<slug>.md < docs/projects/<slug>.md`
サーバ上での実行の仕方は [../server/DEPLOY.md](../server/DEPLOY.md) の「プロジェクトの運用文書とルール」にあります。

**例**: [example.md](example.md) と [`deploy/rules/example.json`](../../deploy/rules/example.json) は、架空のプロジェクト（slug `example`）の例です。
サーバのプロジェクト別ルールを、全種類使っています。使わない状態・完了の記録のキーワード・コメント必須・反映依頼の受け入れ条件・
クローズ時のトークン情報と案件の正規表現・検証コマンド。新しいプロジェクトのルールは、ここから要るものだけを写して作りましょう。
Go のテスト（`internal/domain/rules_test.go`・`internal/server/rules_api_test.go` ほか）も、この例のルールでサーバの判定を確かめています。

| slug | ファイル | サーバが強制するルール |
| -- | -- | -- |
| `example`（例） | [example.md](example.md) | 全種類（`deploy/rules/example.json`） |
