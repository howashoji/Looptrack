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
| プロジェクトを作る（サーバの管理者だけ） | `create_project` |
| 検証 | `verify_issue`（一覧を返すだけで、実行は手元のシェル）・`report_verify`（手元で実行した結果を送る） |
| トークン | `issue_usage`・`usage_missing`・`usage_report`・`list_usage_ledger`・`add_usage_ledger`・`list_usage_requests` |

MCP だけの環境では、AI が検証コマンドを手元のシェルで順に実行して結果を `report_verify` で送ります。
この記録には「MCP の自己申告」の印が付くので、人は CLI の記録と見分けられます。

## MCP だけで導入する（setup ツール）

CLI も hook も入っていない PC でも、MCP の接続設定だけをした状態から導入できます。

1. AI に MCP の接続設定を入れ、ブラウザで許可します。AI に接続の追加から任せるなら、下の「プロンプトを貼るだけで導入する」のプロンプトを使います。
2. AI に「イシュー管理を使えるようにして」と頼むと、AI が `setup` ツールを呼びます。
3. setup は最初に「loop を入れるか」という問いだけを返します。AI はそれを利用者に尋ね、答えを付けて setup を呼び直します。返ってくるのは取得 → SHA-256 の確認 → init をまとめた 1 つのコマンドで、AI は利用者の承認を得てから実行します。
4. トークンが無ければ、AI が承認を得て `looptrack issue login --browser` を実行します。ブラウザでのログインと許可は利用者が行います。
5. 利用者が AI を起動し直し、hook を承認します。
6. 次のセッションの開始時に、hook が導入済みであることをサーバへ知らせます。これでツール結果の【導入が未完了】が消えます。

## プロンプトを貼るだけで導入する（外部のサーバ）

別の PC やクラウドに置いた Looptrack のサーバに、手元の AI をつなぐ手順です。
利用者は、下のプロンプトの `<サーバの URL>` と `<プロジェクト>` を埋めて AI に貼るだけです。
MCP の接続の追加・`setup` の呼び出し・導入のコマンドは AI が進めます。
人が手を動かすのは、ブラウザでの許可とログイン・コマンドの承認・AI の再起動と hook の信頼だけです。

**トークンを AI に渡さないでください。** プロンプトにも会話にも、トークン・パスワード・確認コードを書きません。
ログインは `looptrack issue login --browser` で行い、トークンは CLI とサーバの間だけを通ります。

Windows では、この手順を実機で確かめていません。
VS Code の Copilot も、実機では確かめていません。

### 事前の準備

| 誰が | すること |
| -- | -- |
| サーバの管理者 | 利用者を作ります（`<サーバの URL>/admin/users`）。使うプロジェクトに editor で参加させます（同じ画面か `<サーバの URL>/admin/projects`） |
| 利用者 | サーバの URL（例 `https://example.com/looptrack`。末尾に `/mcp` を付けない）とプロジェクトの slug を管理者から受け取ります |
| 利用者 | ブラウザでサーバに一度ログインできることを確かめます。二段階認証が必須なら、初回のログインで認証アプリを登録します |
| 利用者 | つなぎたいリポジトリの一番上のディレクトリで AI を起動します。Codex はそのディレクトリを信頼（trusted）しておきます |

プロジェクトがまだ無いときは、ふつうは管理者が先に作ります（`<サーバの URL>/admin/projects`）。
つなぐ利用者自身がサーバの管理者なら、先に作らなくてもかまいません。setup が「プロジェクトが見つかりません」を返すと、
管理者には MCP の `create_project` で作れることが案内に添えられ、AI はそれに従って作ってから setup を呼び直します。
prefix（ID の接頭辞）と width（番号の桁数）は後から変えられないので、AI は作る前に slug・prefix・width を利用者に見せて確かめます。
管理者でない利用者には、管理者に頼むよう案内が出ます。

`<サーバの URL>` と `<プロジェクト>` のほかに、プロンプトへ書き足すものはありません。
同じプロンプトを何度貼ってもかまいません。AI は導入の状態を `setup` で確かめて、残りの手順から続けます。

### Claude Code に貼るプロンプト

```text
このリポジトリを、イシュー管理サーバ Looptrack（<サーバの URL>）のプロジェクト <プロジェクト> につないでください。
コマンドの実行と設定の変更は、そのたびに内容を見せて私の承認を得てから行ってください。

1. looptrack の MCP のツール（setup）がこのセッションで使えるなら、2 と 3 を飛ばして 4 へ進む。
2. 次のコマンドで MCP の接続を足す。
   claude mcp add --transport http looptrack <サーバの URL>/mcp --header "X-Looptrack-Project: <プロジェクト>"
3. ここで止まり、私に次を頼む。「Claude Code を再起動し、/mcp で looptrack を選んでブラウザで許可してから、このプロンプトをもう一度貼ってください」
4. setup ツールを、引数 workspace（このリポジトリの git のルートの絶対パス）と project: "<プロジェクト>" を付けて呼ぶ。
5. loop（ループエンジニアリング一式）を入れるかを聞かれたら、決める前に私に聞く。私の答えを付けて、4 と同じ引数で setup を呼び直す。
6. setup が返した手順を上から順に行う。[AI] の手順のコマンドは、私の承認を得てからリポジトリのルートで実行する。SHA-256 が合わなければそこで止まる。
7. ログインは setup の手順どおり looptrack issue login --browser で行う。コマンドのタイムアウトは 5 分より長くする。ブラウザでのログインと許可は私がする。
8. トークンを読まない・表示しない。私にトークンを貼るよう頼まない。
9. [利用者] の手順（再起動とフックの承認）は私に頼んで止まる。私がこのプロンプトをもう一度貼ったら、setup が「導入済み」を返すことを確かめて終わる。
```

### Codex に貼るプロンプト

```text
このリポジトリを、イシュー管理サーバ Looptrack（<サーバの URL>）のプロジェクト <プロジェクト> につないでください。
コマンドの実行と設定の変更は、そのたびに内容を見せて私の承認を得てから行ってください。

1. looptrack の MCP のツール（setup）がこのセッションで使えるなら、2 と 3 を飛ばして 4 へ進む。
2. ~/.codex/config.toml の末尾に次の 3 行を足す。[mcp_servers.looptrack] が既にあれば、書き換えずに私に知らせる（別のプロジェクト向けでも、4 で project を渡すので書き換えなくてよい）。
   [mcp_servers.looptrack]
   url = "<サーバの URL>/mcp"
   http_headers = { "X-Looptrack-Project" = "<プロジェクト>" }
3. ここで止まり、私に次を頼む。「ターミナルで codex mcp login looptrack を実行してブラウザで許可し、Codex を起動し直してから、このプロンプトをもう一度貼ってください」
4. setup ツールを、引数 workspace（このリポジトリの git のルートの絶対パス）と project: "<プロジェクト>" を付けて呼ぶ。
5. loop（ループエンジニアリング一式）を入れるかを聞かれたら、決める前に私に聞く。私の答えを付けて、4 と同じ引数で setup を呼び直す。
6. setup が返した手順を上から順に行う。[AI] の手順のコマンドは、私の承認を得てからリポジトリのルートで実行する。サンドボックスに通信を止められたら、権限を上げて実行してよいかを私に聞く。SHA-256 が合わなければそこで止まる。
7. ログインは setup の手順どおり looptrack issue login --browser で行う。コマンドのタイムアウトは 5 分より長くする。ブラウザでのログインと許可は私がする。
8. トークンを読まない・表示しない。私にトークンを貼るよう頼まない。
9. [利用者] の手順（ターミナルの codex の /hooks でのフックの信頼）は私に頼んで止まる。私がこのプロンプトをもう一度貼ったら、setup が「導入済み」を返すことを確かめて終わる。
```

Codex の MCP の設定（`~/.codex/config.toml`）は PC 全体で 1 つです。
ほかのプロジェクト向けの looptrack が既にあると、1 で setup が使えるので 2 と 3 は飛ばされます。
そのとき引数 project が無いと、setup は接続のヘッダのプロジェクト（別のプロジェクト）向けの手順を返し、AI もそれに気づきません。
そのため 4 で project を渡しています（省略したときだけ、setup はヘッダのプロジェクトを使います）。
導入の後も、MCP のツールはヘッダのプロジェクトを使います。ヘッダが別のプロジェクトなら、MCP のツールを呼ぶたびに引数 project を渡します。
Codex を起動し直す前は、setup が示すトークンの確認（`looptrack issue config`）が「イシュー管理サーバの URL（環境変数 LOOPTRACK_API_URL）がありません」（英語では `Error: the issue server URL …`）で落ちることがあります。
init がリポジトリの `.codex/config.toml` に書く環境変数は、Codex を起動し直すまで効かないからです。
そのときは `LOOPTRACK_API_URL` と `LOOPTRACK_PROJECT` を付けて実行し直します（実地の確認では AI が自分で気づいて付けました）。

### GitHub Copilot（VS Code）に貼るプロンプト

エージェントモードのチャットに貼ります。

```text
このリポジトリを、イシュー管理サーバ Looptrack（<サーバの URL>）のプロジェクト <プロジェクト> につないでください。
コマンドの実行とファイルの変更は、そのたびに内容を見せて私の承認を得てから行ってください。

1. looptrack の MCP のツール（setup）がこのチャットで使えるなら、2 と 3 を飛ばして 4 へ進む。
2. .vscode/mcp.json の servers に次の looptrack を足す。ファイルが無ければ作る。looptrack が既にあれば、書き換えずに私に知らせる。
   "looptrack": { "type": "http", "url": "<サーバの URL>/mcp", "headers": { "X-Looptrack-Project": "<プロジェクト>" } }
3. ここで止まり、私に次を頼む。「VS Code が looptrack の許可を求めたらブラウザで許可し、新しいチャットでこのプロンプトをもう一度貼ってください」
4. setup ツールを、引数 workspace（このリポジトリの git のルートの絶対パス）と project: "<プロジェクト>" を付けて呼ぶ。
5. loop（ループエンジニアリング一式）を入れるかを聞かれたら、決める前に必ず私に聞く。自分で答えを決めない。私の答えを付けて、4 と同じ引数で setup を呼び直す。
6. setup が返した手順を上から順に行う。[AI] の手順のコマンドは、私の承認を得てからリポジトリのルートで実行する。SHA-256 が合わなければそこで止まる。
7. ログインは setup の手順どおり looptrack issue login --browser で行う。コマンドのタイムアウトは 5 分より長くする。ブラウザでのログインと許可は私がする。
8. トークンを読まない・表示しない。私にトークンを貼るよう頼まない。
9. [利用者] の手順（新しいチャットを始める）は私に頼んで止まる。私がこのプロンプトをもう一度貼ったら、setup が「導入済み」を返すことを確かめて終わる。
```

### GitHub Copilot CLI に貼るプロンプト

```text
このリポジトリを、イシュー管理サーバ Looptrack（<サーバの URL>）のプロジェクト <プロジェクト> につないでください。
コマンドの実行と設定の変更は、そのたびに内容を見せて私の承認を得てから行ってください。

1. looptrack の MCP のツール（setup）がこのセッションで使えるなら、2 と 3 を飛ばして 4 へ進む。
2. 次のコマンドで MCP の接続を足す。
   copilot mcp add --transport http --header "X-Looptrack-Project: <プロジェクト>" looptrack <サーバの URL>/mcp
3. ここで止まり、私に次を頼む。「copilot を起動し直し、開いたブラウザで許可してから（開かなかったときは /mcp auth looptrack で）、copilot をもう一度起動し直して、このプロンプトをもう一度貼ってください」
4. setup ツールを、引数 workspace（このリポジトリの git のルートの絶対パス）と project: "<プロジェクト>" を付けて呼ぶ。
5. loop（ループエンジニアリング一式）を入れるかを聞かれたら、決める前に必ず私に聞く。自分で答えを決めない。私の答えを付けて、4 と同じ引数で setup を呼び直す。
6. setup が返した手順を上から順に行う。[AI] の手順のコマンドは、私の承認を得てからリポジトリのルートで実行する。SHA-256 が合わなければそこで止まる。
7. ログインは setup の手順どおり looptrack issue login --browser で行う。コマンドのタイムアウトは 5 分より長くする。ブラウザでのログインと許可は私がする。
8. トークンを読まない・表示しない。私にトークンを貼るよう頼まない。
9. [利用者] の手順（copilot を起動し直し、フォルダを信頼する確認で「今後も信頼」を選ぶ）は私に頼んで止まる。私がこのプロンプトをもう一度貼ったら、setup が「導入済み」を返すことを確かめて終わる。
```

Copilot では、モデルによっては loop を入れるかを利用者に聞かずに決めてしまうことがあります。
そのため Copilot のプロンプトでは 5 を強めに書いています。答えをはじめからプロンプトに書き足してもかまいません（例「loop は入れない」）。

### 人が手を動かす所

| 段 | 人がすること | なぜ人か |
| -- | -- | -- |
| 事前 | 管理者が利用者を作り、プロジェクトに参加させます | 権限はプロジェクトごとに管理者が付けます |
| MCP の接続の追加 | AI が見せた変更を承認します | 手元の AI の設定を書き換えるからです |
| MCP の許可 | Claude Code は `/mcp`、Codex はターミナルの `codex mcp login looptrack`、Copilot CLI は起動し直したときに自動で開くブラウザ（開かなかったときは `/mcp auth looptrack`）、VS Code は求められたときに、ブラウザでログインして許可します | サーバが本人を確かめます。パスワードと確認コードは AI に渡しません |
| 再起動と貼り直し | AI を起動し直し（VS Code は新しいチャット）、同じプロンプトを貼ります。Copilot CLI は、ブラウザで許可した後にもう一度起動し直します | 足した接続は、起動し直した後に読み込まれます。Copilot CLI は起動と同時に許可を始めるので、許可を待つ間に接続が 10 秒の上限を過ぎ、`Failed to connect … timed out after 10000 ms` になることがあります |
| loop の問い | 入れるか入れないかを答えます | loop を入れるかを決めるのは利用者です |
| 導入のコマンド | 取得 → SHA-256 の確認 → init の 1 つのコマンドを承認します。Claude Code では、auto mode でもこのコマンドの許可を求められます（Claude Code が `Contains brace with quote character (expansion obfuscation)` と判定するため） | 実行ファイルを `~/.local/bin` に置き、リポジトリのファイルを変えるからです |
| ログイン | `looptrack issue login --browser` を承認し、開いたブラウザでログイン（と二段階認証）と許可をします。この PC にトークンが既にあれば、この段は飛ばされます | トークンは CLI とサーバの間だけを通り、会話には出ません |
| hook の信頼 | Claude Code は再起動して確認が出たら承認、Codex はターミナルの `codex` の `/hooks` で信頼、Copilot は新しいセッション（Copilot CLI はフォルダを「今後も信頼」）を始めます | hook は利用者が認めた後にだけ動きます |

Codex では、AI が実行するシェルのコマンドの外部通信をサンドボックスが止めます。
導入のコマンドとログインは、権限を上げて実行することの承認が要ります。
Copilot CLI では、MCP のツールを呼ぶたびに承認が要ります。

### うまくいかないとき

| 症状 | 確かめること |
| -- | -- |
| 許可のブラウザが開かない | 既定のブラウザを先に起動しておきます。Claude Code なら `/mcp` の画面を閉じて開き直します。`looptrack issue login --browser` は URL も表示するので、起動済みのブラウザに貼っても続けられます |
| 貼り直しても MCP のツールが使えない | 接続の追加の後に AI を起動し直したか・許可を済ませたかを確かめます。URL の末尾が `/mcp` であることも確かめます |
| ツール結果の【導入が未完了】が消えない | 再起動と hook の信頼を済ませたかを確かめます。Codex の未信頼の hook は知らせなしに飛ばされます。トークンが未登録だと hook は知らせを送れません（`looptrack issue config` で確かめます） |
| PATH や hook の配線が怪しい | `looptrack doctor` を実行します。PATH・hook の配線・導入の記録を確かめます（何も書きません） |
| setup が「プロジェクトが見つかりません」を返す | slug の綴りを確かめます。あなたがサーバの管理者なら、AI が `create_project` で作ってよいかを slug・prefix・width を示して尋ねるので、値を確かめて答えます（prefix と width は後から変えられません）。管理者でなければ、管理者にプロジェクトを作ってもらうか、参加させてもらいます |
| 聞かれないまま loop が入った | `looptrack issue init --remove-loop` で外せます |
| ブラウザが無い環境で使いたい | 利用者が `<サーバの URL>/account` でトークンを発行し、自分のターミナルで `looptrack issue login --url <サーバの URL>` に貼ります。AI には渡しません |
