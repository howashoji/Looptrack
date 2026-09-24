# AI ごとの手引き

[ガイドの目次](README.md) · 前: [日々の使い方](daily-use.md) · 次: [管理者の手引き](admin.md)

## 共通の考え方

| 経路 | 使う場面 |
| -- | -- |
| CLI（`looptrack issue …`） | Claude Code の主な経路です。hook も CLI を使います |
| MCP のツール | Codex・Copilot の主な経路です。hook の無い AI ではこれだけで回します |

どの AI でも、サーバの規則・権限・記録は同じです。
導入は `looptrack issue init --agent <AI>` で行います。
MCP だけを接続した AI には、MCP の `setup` ツールが導入の手順を返します。下の「MCP だけで導入する」を見てください。

## kit の core と loop の選び方

| 状況 | おすすめ |
| -- | -- |
| まず試したい。hook に止められるのに慣れていない | core だけ（`--no-loop`） |
| AI に長く自分で回させたい。人は判断だけしたい | core + loop（`--loop`） |
| 1 つのリポジトリで複数の AI が並行して動く | core + loop。文脈の大きさの警告と別リポジトリへの変更の確認が役に立ちます |
| loop の一部が合わない | いったん `--remove-loop` で外します。hook の細かい調整は `LOOPTRACK_LOOP_*` の環境変数で行います |

loop には次のものが入っています。

| 項目 | 何をするか |
| -- | -- |
| 出力の規律・作業の規律 | 規則文を毎セッション AI に見せます |
| 確認モード | 「確認して」と頼むと調査と計画で止まり、編集を拒否します。「実装して」で解除されます |
| 引き継ぎの鮮度 | commit や close の後は、引き継ぎのメモを更新するまでターンを終えられません（skill `session-handoff`） |
| イテレーションの規律（skill `/iterate`） | 1 回の実装を 1 つのイシューで進め、見つけた問題はその場で起票します。ゲート（build → lint → test）は `looptrack gates` で実行します |
| 文脈の大きさの警告 | 1 応答あたりのコンテキストが閾値を超えたら、区切りで引き継ぎを書いてセッションを分けるよう勧めます。止めはしません |
| 背景プロセスの検知 | セッションが残した終わらない子プロセスを見つけて知らせます |
| 別リポジトリ・別プロジェクトへの変更 | 変更する前に利用者の確認を求めます |

loop を入れるかどうかを決めるのは AI ではなく利用者です。

```bash
looptrack issue init --project demo --url http://127.0.0.1:8090/looptrack --loop          # loop を足す
looptrack issue init --project demo --url http://127.0.0.1:8090/looptrack --remove-loop   # loop を外す
```

## Claude Code

```bash
looptrack issue init --project demo --url http://127.0.0.1:8090/looptrack --agent claude-code --mcp
```

- hook: `.claude/settings.json` に配線されます。起動し直して hook の確認が出たら、内容を見て承認してください。
- skill `/issue`: 起票・着手・コメント・close の手順です。`/issue` と打つか、「イシューを起票して」と頼みます。
- skill `/iterate`（loop）: 1 回の実装を「着手 → 実装 → ゲート → 問題の起票 → close → 次へ」の順で進めます。
- MCP の prompt: `/mcp__looptrack__loop`（ループの定型）・`/mcp__looptrack__review`（人の判断待ちと反応の相談）・`/mcp__looptrack__setup`（導入）があります。
- `CLAUDE.md` の管理の節が、AI に CLI の使い方を伝えます。

頼み方の例:

> イシューの next から回して。判断が要るものは In Review にして止まらずに次へ。

## Codex

```bash
looptrack issue init --project demo --url http://127.0.0.1:8090/looptrack --agent codex
```

- 案内文: `AGENTS.md` に管理の節が入ります。Codex は rules や skill を持たないので、要点をここに入れています。
- MCP を主に使います。Codex の既定のサンドボックスは、AI が実行するシェルのコマンドの外部通信を止めます。CLI を実行するたびに承認が要るので、イシューの操作は MCP のツールで行います。
- MCP の接続: `~/.codex/config.toml` に次を足し、`codex mcp login looptrack` で許可します。

```toml
[mcp_servers.looptrack]
url = "http://127.0.0.1:8090/looptrack/mcp"
http_headers = { "X-Looptrack-Project" = "demo" }
```

- hook: `.codex/hooks.json` に配線されます。プロジェクトを信頼したら、**ターミナルの `codex` をプロジェクトで起動し `/hooks` で hook を信頼**してください。この信頼はデスクトップ版にも効きます。信頼していない hook は知らせなしに飛ばされます。
- CLI の環境変数: init が `.codex/config.toml` の `[shell_environment_policy]` に `LOOPTRACK_API_URL` と `LOOPTRACK_PROJECT` を書きます。

## GitHub Copilot（VS Code のエージェントモード・Copilot CLI）

```bash
looptrack issue init --project demo --url http://127.0.0.1:8090/looptrack --agent copilot --mcp
```

- Codex と同じく MCP を主に使います。
- MCP の接続: `--mcp` で VS Code の `.vscode/mcp.json` と Copilot CLI の `.github/mcp.json` が入ります。Copilot CLI では `/mcp auth looptrack` で許可します。
- 案内文: `AGENTS.md` の管理の節を読みます。
- hook: `.github/hooks/looptrack.json` に配線されます。VS Code では新しいチャットから、Copilot CLI ではフォルダを信頼したときから動きます。
- トークン計測: Copilot では、利用者が OpenTelemetry のファイル出力を有効にしたときだけ測れます。有効にしていなければ、トークン情報が無くても咎めません。

Copilot の hook には、まだ実機で確かめていない部分があります。
動かないときは MCP のツールだけで回してください。

## その他の AI（MCP だけ）

hook の仕組みが無い AI でも、MCP のツールだけでループを回せます。

1. MCP の接続を足します。URL は `<サーバの URL>/mcp`・ヘッダは `X-Looptrack-Project: <slug>` で、認証はブラウザで許可します。
2. `looptrack issue init --project demo --url http://127.0.0.1:8090/looptrack --agent other` を実行し、表示された案内を AI の指示ファイルに入れます。
3. AI に「最初に guide ツールを読み、next から回して」と頼みます。
4. 導入が済んだら `looptrack issue installed --agent other` でサーバに知らせます。

MCP のツールは次のとおりです。

| 用途 | ツール |
| -- | -- |
| 規則を読む・導入 | `guide`・`setup` |
| ループ | `next`・`create_issue`・`add_comment`・`set_status`・`get_issue`・`update_issue`・`assign_issue` |
| 一覧 | `list_issues`・`ready_issues`・`project_summary`・`get_matrix`・`issue_activity`・`list_projects` |
| 検証 | `verify_issue`（一覧を返すだけで、実行は手元のシェル）・`report_verify`（手元で実行した結果を送る） |
| トークン | `issue_usage`・`usage_missing`・`usage_report`・`list_usage_ledger`・`add_usage_ledger`・`list_usage_requests` |

MCP だけの環境では、AI が検証コマンドを手元のシェルで順に実行して結果を `report_verify` で送ります。
この記録には「MCP の自己申告」の印が付くので、人は CLI の記録と見分けられます。

## MCP だけで導入する（setup ツール）

CLI も hook も入っていない PC でも、MCP の接続設定だけをした状態から導入できます。

1. AI に MCP の接続設定を入れ、ブラウザで許可します。
2. AI に「イシュー管理を使えるようにして」と頼むと、AI が `setup` ツールを呼びます。
3. setup は最初に「loop を入れるか」という問いだけを返します。AI はそれを利用者に尋ね、答えを付けて setup を呼び直します。返ってくるのは取得 → SHA-256 の確認 → init をまとめた 1 つのコマンドで、AI は利用者の承認を得てから実行します。
4. トークンが無ければ、AI が承認を得て `looptrack issue login --browser` を実行します。ブラウザでのログインと許可は利用者が行います。
5. 利用者が AI を起動し直し、hook を承認します。
6. 次のセッションの開始時に、hook が導入済みであることをサーバへ知らせます。これでツール結果の【導入が未完了】が消えます。
