# AI ごとの手引き

[ガイドの目次](README.md) · 前: [日々の使い方](daily-use.md) · 次: [FAQ / トラブルシュート](faq.md)

## 共通の考え方

| 経路 | 使う場面 |
| -- | -- |
| CLI（`looptrack issue …`） | Claude Code の主な経路です。hook も CLI を使います |
| MCP のツール | Codex・Copilot の主な経路です。hook の無い AI ではこれだけで回します |

どの AI でもサーバの規則・権限・記録は変わりません。
導入は `looptrack issue init --agent <AI>` で行います。
MCP だけを接続した AI なら、MCP の `setup` ツールが導入の手順を返します。下の「MCP だけで導入する」を見てください。

## kit の core と loop の選び方

| 状況 | おすすめ |
| -- | -- |
| まず試したい。hook に止められるのに慣れていない | core だけ（`--no-loop`） |
| AI に長く自分で回させたい。人は判断だけしたい | core + loop（`--loop`） |
| 1 つのリポジトリで複数の AI が並行して動く | core + loop。文脈の大きさの警告と別リポジトリへの変更の確認が役に立ちます |
| loop の一部が合わない | いったん `--remove-loop` で外します。hook の細かい調整は `LOOPTRACK_LOOP_*` の環境変数で行います |

loop に入っているのは次のものです。

| 項目 | 何をするか |
| -- | -- |
| 出力の規律・作業の規律 | 規則文を毎セッション AI に見せます |
| 確認モード | 「確認して」と頼むと調査と計画で止まり、編集を拒否します。「実装して」で解除されます |
| 引き継ぎの鮮度 | commit や close の後は、引き継ぎのメモを更新するまでターンを終えられません（skill `session-handoff`） |
| イテレーションの規律（skill `/iterate`） | 1 回の実装を 1 つのイシューで進め、見つけた問題はその場で起票します。ゲート（build → lint → test）は `looptrack gates` で実行します |
| 文脈の大きさの警告 | 1 応答あたりのコンテキストが閾値を超えたら、区切りで引き継ぎを書いてセッションを分けるよう勧めます。止めはしません |
| 背景プロセスの検知 | セッションが残した終わらない子プロセスを見つけて知らせます |
| 別リポジトリ・別プロジェクトへの変更 | 変更する前に利用者の確認を求めます |

規則文は日本語と英語の両方が同梱されていて、hook が実行時にどちらかを選びます。
確認モードが反応するのは英語の依頼（check、investigate、review など / implement、fix、add など）と日本語の依頼（「確認して」 / 「実装して」）の両方です。1 つの依頼に両方の語が入っていたら、日本語の語で決まります。
独自の言い回しを足したいときはエージェントの設定の `env` で `LOOPTRACK_LOOP_TASK_MODE_INVEST_RE` と `LOOPTRACK_LOOP_TASK_MODE_EXEC_RE` にパターンを足します。
注意点が 1 つあります。これらは Go の正規表現（RE2）なので、先読み・後読み（`(?!…)` など）は使えません。`LOOPTRACK_LOOP_RUNAWAY_ALLOW`（背景プロセスの検知から外すプロセス）も同じです。読めないパターンは無視して既定の語だけで動きます。そのときは hook が変数名と誤りをメッセージで知らせます。

loop を入れるかどうかは AI ではなく利用者が決めます。

```bash
looptrack issue init --project demo --url http://127.0.0.1:8090/looptrack --loop          # loop を足す
looptrack issue init --project demo --url http://127.0.0.1:8090/looptrack --remove-loop   # loop を外す
```

## Claude Code

```bash
looptrack issue init --project demo --url http://127.0.0.1:8090/looptrack --agent claude-code --mcp
```

- hook: `.claude/settings.json` に配線されます。起動し直して hook の確認が出たら、中身を見て承認してください。
- skill `/issue`: 起票・着手・コメント・close の手順。`/issue` と打つか「イシューを起票して」と頼むだけです。
- skill `/iterate`（loop）: 1 回の実装を「着手 → 実装 → ゲート → 問題の起票 → close → 次へ」の順で進めます。
- MCP の prompt: `/mcp__looptrack__loop`（ループの定型）・`/mcp__looptrack__review`（人の判断待ちと反応の相談）・`/mcp__looptrack__setup`（導入）。
- AI に CLI の使い方を伝えるのは `CLAUDE.md` の管理の節です。

頼み方の例:

> イシューの next から回して。判断が要るものは In Review にして止まらずに次へ。

## Codex

```bash
looptrack issue init --project demo --url http://127.0.0.1:8090/looptrack --agent codex
```

- 案内文: `AGENTS.md` に管理の節が入ります。Codex には rules も skill も無いので、要点はここに入れました。
- 主に使うのは MCP。なぜか？ Codex の既定のサンドボックスは、AI が実行するシェルのコマンドの外部通信を止めます。すると CLI を実行するたびに承認が要るので、イシューの操作は MCP のツールで行います。
- MCP の接続: `~/.codex/config.toml` に次を足して `codex mcp login looptrack` で許可します。

```toml
[mcp_servers.looptrack]
url = "http://127.0.0.1:8090/looptrack/mcp"
http_headers = { "X-Looptrack-Project" = "demo" }
```

- hook: `.codex/hooks.json` に配線されます。プロジェクトを信頼したら、**ターミナルの `codex` をプロジェクトで起動し `/hooks` で hook を信頼**してください。この信頼はデスクトップ版にも効きます。信頼していない hook は知らせもなく飛ばされます。
- CLI の環境変数: init が `.codex/config.toml` の `[shell_environment_policy]` に `LOOPTRACK_API_URL` と `LOOPTRACK_PROJECT` を書き込みます。

## GitHub Copilot（VS Code のエージェントモード・Copilot CLI）

```bash
looptrack issue init --project demo --url http://127.0.0.1:8090/looptrack --agent copilot --mcp
```

- Codex と同じく主に MCP を使います。
- MCP の接続: `--mcp` を付けると、VS Code の `.vscode/mcp.json` と Copilot CLI の `.github/mcp.json` が入ります。Copilot CLI では `/mcp auth looptrack` で許可します。
- 案内文: `AGENTS.md` の管理の節を読みます。
- hook: `.github/hooks/looptrack.json` に配線されます。動き出すのは VS Code なら新しいチャットから、Copilot CLI ならフォルダを信頼したときから。
- トークン計測: Copilot で測れるのは、利用者が OpenTelemetry のファイル出力を有効にしたときだけです。有効にしていなければトークン情報が無くても咎めません。

Copilot の hook にはまだ実機で確かめていない部分があります。
動かなければ MCP のツールだけで回してください。

## その他の AI（MCP だけ）

hook の仕組みが無い AI でも大丈夫です。MCP のツールだけでループを回せます。

1. MCP の接続を足します。URL は `<サーバの URL>/mcp`・ヘッダは `X-Looptrack-Project: <slug>` で、認証はブラウザで許可します。
2. `looptrack issue init --project demo --url http://127.0.0.1:8090/looptrack --agent other` を実行して表示された案内を AI の指示ファイルに入れます。
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

MCP だけの環境では AI が検証コマンドを手元のシェルで順に実行し、結果を `report_verify` で送ります。
この記録には「MCP の自己申告」の印が付くので、人は CLI の記録と見分けられます。

## 複数の端末で使う

1 つのプロジェクトを、2 つの端末（自宅と職場など）で AI と進められます。
ただし、**1 つの会話**を 2 つの端末で同時に進めることは、どのベンダーでもできません。できるのは交代するか、fork するかです。
だから作業はイシューとブランチで分け、会話では分けません。

### 規約

1. **1 端末 1 イシュー 1 ブランチにします。** 1 つのイシューを両方の端末で進めたいなら、子イシューに分けます。
2. **端末を切り替える前に push します。** 未コミットの変更と手元のメモ（引き継ぎのファイル・AI の記憶）は、書いた端末にしか残りません。
3. **受け取った端末は `looptrack issue status <ID> "In Progress"` で着手を宣言し直します。** 渡す端末は先にイシューを Todo に戻します。着手は最後に宣言したセッションへ確認なしで移ります。以後、もう一方のセッションの `next` はそのイシューを外し、別のセッションが着手していると注記します。
4. **人が手で打つシェルから `looptrack issue next` を実行しません。** AI のセッション ID を持たないシェルは、自分の In Progress を全部自分のものと見ます。
5. **同じ会話を 2 か所で同時に再開しません。別の場所で続けるときは fork します。**

```bash
claude --resume <セッション ID> --fork-session   # Claude Code
codex fork <セッション ID>                        # Codex
```

fork しない `claude --resume <セッション ID>` は、別のディレクトリから実行しても元の記録のファイルに追記するので、2 か所の発言が 1 つの記録に混ざります。
公式の文書にも同じことが書いてあります（"If you resume the same session in two terminals without forking, messages from both interleave into one transcript"。<https://code.claude.com/docs/en/sessions>）。
fork は新しいセッション ID を作り、元の記録はそのまま残します（`--fork-session`: <https://code.claude.com/docs/en/cli-reference>、`codex fork`: <https://developers.openai.com/codex/cli/reference>）。
fork しても、会話の中に出てきた絶対パスは元のまま使われます。実測では、別の作業コピーで続けた最初の編集が元の作業コピーを書き換えました。
だから最初の発言で、続ける場所の作業コピーのパスを AI に伝えてください。

3〜5 の挙動は 2026-10-25 に Claude Code 2.1.310 と使い捨ての Looptrack のサーバで実測しました。

### ベンダーの仕組み

次の仕組みを使うと、1 つの会話を複数の機器から見て操作できます。どれも、作業を実行する場所は **1 か所**です。

| AI | 仕組み | 作業を実行する場所 | 公式の文書 |
| -- | -- | -- | -- |
| Claude Code | `claude --cloud "<タスク>"` でクラウドのセッションを始める | クラウドの VM。GitHub からブランチを clone するので、先に push します | <https://code.claude.com/docs/en/claude-code-on-the-web> |
| Claude Code | `claude --teleport` でクラウドのセッションをターミナルに引き取る | 手元のターミナル（自分の写し）。そこでの作業はクラウドのセッションには出ません | <https://code.claude.com/docs/en/claude-code-on-the-web> |
| Claude Code | Remote Control: `claude --remote-control`（`--rc`）か、実行中のセッションで `/remote-control` | 手元の機械。電話やブラウザは操作するだけです | <https://code.claude.com/docs/en/remote-control> |
| Codex | cloud tasks（`codex cloud`） | Codex のクラウドの環境。結果は手元のリポジトリに適用します | <https://developers.openai.com/codex/cli/reference> |
| Codex | Remote connections | Codex の app server を動かしている機械 | <https://developers.openai.com/codex/remote-connections> |

手元の会話の記録を端末の間で同期する手段は、どちらのベンダーの文書にもありません（2026-10-25 に確認）。
Claude Code は、記録の形式が「Claude Code の内部のもので、版ごとに変わる」と書いています（<https://code.claude.com/docs/en/sessions>）。
記録を手で別の端末へ写しても、会話を続ける正式な方法にはなりません。

### 近道: 同じ絶対パスに置く

2 つの端末が同じ OS で、ユーザ名も同じなら、作業コピーを両方で同じ絶対パス（たとえば `/Users/<名前>/src/<プロジェクト>`）に置きます。
引き継ぎのメモ・イシューのコメント・会話に書いたパスが、どちらの端末でもそのまま通ります。
OS かユーザ名が違うとホームディレクトリが違うので、この近道は使えません。
そのパスには実体のディレクトリを置いてください。シンボリックリンクを置いても役に立ちません。Claude Code はプロジェクトのディレクトリの名前と記録する作業ディレクトリを、実体のパスから決めます（Claude Code 2.1.310 で実測）。
リンク（シンボリックリンク・Windows の junction）は OS ごとに振る舞いも違うので、頼らないでください。

### セッション要約

端末を交代するときは、AI が書く**セッション要約**をイシューに残します。会話の記録そのものも運べます（[会話記録をそのまま運ぶ](#会話記録をそのまま運ぶ)）が、ふだんは要約で足ります。
要約は先頭に印の付いたコメントとしてイシューに残り、別の端末のセッションの SessionStart と `next` に出ます。
要約はサーバに残り、プロジェクトの参加者が読みます。だから資格情報の値は書きません。
**資格情報の中身を読まない・表示しない・別の場所へ写さないでください。**

#### 書くこと

- 利用者の指示の要旨
- 決めたことと、その理由
- 試して捨てたこと
- どこで止まったか（現在地）
- 次の一手

ブランチと HEAD の SHA は見出しに入るので、本文で繰り返さなくてかまいません。
本文の上限は 16 KiB（16,384 バイト）です。経緯の全文ではなく、次のセッションが再開に要る要点に絞ってください。

#### 送る

`looptrack session digest <ID> -` で送ります。本文は標準入力から読みます（`-` を省いても同じです。ファイルのパスを渡せば、そのファイルを読みます）。

```bash
looptrack session digest DEMO-0004 - <<'EOF'
指示の要旨: パスワードの表示切替を付ける。文言は既存に合わせる
決定: アイコンは入力欄の右端に置く（検索欄と同じ位置のため）
捨てた案: 入力欄の下のチェックボックス（e2e のセレクタが壊れる）
現在地: アイコンの表示まで。e2e は未実行
次の一手: e2e を通してから close する
EOF
```

本文が印で始まらなければ、CLI が次の見出しの 4 行と空行を本文の前に足します。

```text
セッション要約: home-mac
branch: feat/login-toggle
head: 3f2c1a9d0b7e4c6a8f1e2d3c4b5a69788796a5b4
ai: claude-code
```

- 印は利用者の言語のものです（日本語は `セッション要約:`、英語は `Session digest:`）。後ろに端末のラベルが続きます。
- 端末のラベルは `LOOPTRACK_MACHINE_LABEL` の値です。無ければホスト名の最初の `.` より前を使います。
- `branch:` は作業ディレクトリでの `git rev-parse --abbrev-ref HEAD`（ブランチから外れていれば `HEAD`）、`head:` は `git rev-parse HEAD` です。
  git の作業ツリーの外などで決められなければ、送らずに止まります。そのときは見出しに手で書いて送り直してください。
- `ai:` は環境変数から決めた AI の種類です（`claude-code`・`codex`・`copilot`、分からなければ `other`）。
- 見出しを自分で書くなら、1 行目に印と端末のラベルを、8 行目までに `branch:`・`head:`・`ai:` の行を 1 つずつ置きます。CLI は欠けた行だけを足し、書いてある行は書き換えません。
- 本文の中の端末のパスは置き換えて保存されます。git の作業ツリーのルートは `./`、ホームディレクトリは `~`、AI の会話記録の置き場のようにその端末にしか無い場所は `<machine-local>` になります。会話記録の置き場は `CLAUDE_CONFIG_DIR` や `CODEX_HOME` があればそちらに従います。ほかにも置き換えたい場所があれば `LOOPTRACK_MACHINE_LOCAL` に並べます。区切りは OS の `PATH` と同じです。
- 要約は `close --comment` や `status --comment` には入れられません（拒否され、状態も変わりません）。先に `session digest` で送り、状態はその後で変えます。

#### 秘密に当たったとき

値の形（`token: …`・`Bearer …`・秘密鍵など）に当たる本文は、サーバが拒みます。コメントは残りません。
理由に出るのは行番号と規則の語だけで、値は出ません。

```text
エラー: DEMO-0004: セッション要約の 3 行目に秘密らしい文字列（token: …）があるので、保存しませんでした。値を *** に置き換えて送り直してください（例: token=***）。規律の文章の引用のような散文でも当たるので、その語を言い換えるか *** に置き換えてください
```

- 示された行の値を `***` に置き換えて、送り直してください（`token: ***`）。
- 規律の文章の引用のような散文でも当たります。そのときはその語を言い換えます。

#### 別の端末で読む

別のセッションが書いた要約は、次の形で出ます。印の行を除いた、空でない先頭の 8 行（400 文字まで）が字下げして並びます。

```text
※ DEMO-0004 の最新のセッション要約は別のセッションが書いたものです（home-mac・2時間前）:
  branch: feat/login-toggle
  head: 3f2c1a9d0b7e4c6a8f1e2d3c4b5a69788796a5b4
  ai: claude-code
  指示の要旨: パスワードの表示切替を付ける。文言は既存に合わせる
  決定: アイコンは入力欄の右端に置く（検索欄と同じ位置のため）
  捨てた案: 入力欄の下のチェックボックス（e2e のセレクタが壊れる）
  現在地: アイコンの表示まで。e2e は未実行
  次の一手: e2e を通してから close する
```

- SessionStart（`looptrack issue summary`）は、In Progress のイシューの最新の要約を出します。
- `next` は、着手したイシューの最新の要約を出します。渡す端末が Todo に戻したイシューの要約は、受け取る端末の `next` で目に入ります。
- 出すのは別のセッションが書いた要約だけです。自分のセッションが書いた要約は出ません。
- 全文は `looptrack session pull <ID>` で取り出します。新しい順に 3 件（`--limit N` で変えられます）を `.claude/.looptrack-work/<ID>-digest.md` に書き、標準出力にも出します。ファイルは毎回上書きします。要約が無ければその旨を出し、ファイルは書きません。

#### 送り忘れを止める（loop）

loop を入れていれば、引き継ぎの鮮度ガード（Stop）の判定先を要約にできます。環境変数 `LOOPTRACK_LOOP_HANDOFF_BACKEND=issue` を設定してください。

- 作業の完了（commit・close など）の後、このセッションが着手中のイシューに完了より新しい自分の要約が無ければ、Stop を 1 回差し戻します。送るコマンド `looptrack session digest <ID> -` をイシューごとに示します。
- サーバに届かないときは止めません。注意を 1 つ出して通し、つながった後の Stop でもう一度確かめます。
- このセッションが着手したと言えないイシュー（Web や手で打ったシェルで着手したものなど）は判定せず、注意で知らせます。

プロジェクトで要約を必須にするなら、プロジェクト別ルール `session.digest_required` を入にします。
入のプロジェクトでは、引き継ぎの判定先（`LOOPTRACK_LOOP_HANDOFF_BACKEND`）に関わらず同じ判定が加わり、両方が通ったときだけ Stop を通します。
サーバは状態の変更を拒みません。`looptrack project rules set` はルール全体を置き換えるので、ほかのルールも同じファイルに書きます（[管理者の手引き](server/admin.md#プロジェクト別ルールの設定)）。

```json
{
  "session": { "digest_required": true }
}
```

### 会話記録をそのまま運ぶ

前の会話を別の端末で丸ごと読み返したいときや、AI に前の文脈をそのまま持たせたいときは、会話記録そのものを自分の別の端末へ運べます。
ふだんは上のセッション要約で足ります。要約はプロジェクトの参加者が読むイシューのコメントで、こちらは本人にしか見えない生の記録です。
運んだ記録は、取り込んだ端末で必ず fork して続けます。同じ会話を 2 か所で続けることはしません。

#### 使える条件

- プロジェクト別ルール `session.transcripts` が入であること（既定は切。[管理者の手引き](server/admin.md#プロジェクト別ルールの設定)）。切のプロジェクトでは、記録の登録も追記も拒否されます。
- 送る端末と取り込む端末が、同じ OS の系統であること（Windows どうしか、macOS と Linux）。系統が違う記録は取り込めません。
- 対象の AI は Claude Code と Codex です。Codex の記録の取り込み（置く場所と `codex fork` が見つけるか）は、まだ実機で確かめていません。
- 取り込む端末でも、送ったときと同じ利用者でログインしていること。記録を読めるのは送った本人だけです。

#### 送る

```bash
looptrack session transcript push --dry-run   # 送らずに件数だけを見る
looptrack session transcript push             # 今のセッションの記録を送る（2 回目からは前回の続きだけ）
```

- AI のセッションの中（AI の Bash ツールなど）で実行すると、今のセッションの記録を送ります。手で打つシェルからは、セッション ID を引数で渡します（`push <セッション ID> --agent claude-code`）。
- 送り先は今のプロジェクトです。送った位置は送った端末に覚えておき、次は続きだけを送ります。
- 手で打つ `push` は `LOOPTRACK_SESSION_TRANSCRIPTS` を見ません。打つこと自体が本人の同意だからです。ルールが入なら送れます。
- Claude Code は本体・サブエージェントの記録・ツールの結果のファイルを運び、Codex は同じスレッドの rollout を運びます。そのほかのファイル（Claude Code の `workflows/`・Codex の圧縮された `.jsonl.zst` など）は運びません。

`--dry-run` の出力は件数だけです。値と行の中身は出しません。

```text
dry-run（送りません）: claude-code のセッション 5f0c2a9e-7b1d-4c3e-9a8f-2d6b1e0c4a7f
ファイル: 送る 31・運ばない 7・書き換えられた 0
行: 2104・チャンク: 31・書きかけの行（次の回に送る）: 0 バイト
書き換えたパス: 4120
マスクした値: 0
書き換えなかった絶対パス: 構造の欄 0・本文 41・キー 0
運ばない: 5f0c2a9e-7b1d-4c3e-9a8f-2d6b1e0c4a7f/workflows（workflows・7）
```

#### 書き換えるもの・書き換えないもの

- **構造の欄のパス**（`cwd`、ツールの入力の `file_path`・`path`、Bash の `command`、hook のコマンド行の先頭にある実行ファイルのパスなど）は書き換えます。この端末の git の作業ツリーのルート・AI の記録と設定の置き場・ホームディレクトリを記号（プレースホルダ）に置き換えて送り、取り込む端末でその端末のパスに戻します。元の絶対パスはサーバに送りません。
- **資格情報らしい値**（`token=…`・`Bearer …`・秘密鍵など）は、文字列の値ごとに `***` にマスクします。
- **散文は書き換えません**。AI の返答・ツールの出力・読んだファイルの中身・system-reminder の本文です。書き換えるのは、キーの名前で選んだ構造の欄だけです。
  散文に残ったパスは送った端末のままなので、`pull` はその数を注意として出します。fork した後の最初の発言で、続ける作業コピーのパスを AI に伝えてください（上の規約 5 と同じです）。
- 記録の形式は AI の内部のもので、版ごとに変わります。`push` が「構造の欄に書き換えなかった絶対パス」の注意を出したら、形式が変わった兆しです。fork の後に元のルートを指すおそれがあります。

#### 送れないとき

次のファイルがあると、`push` は何も送らずに、ファイルごとの理由を件数だけで出して終了コード 1 にします。値と行の中身は出しません。

- JSON として読めない行がある（書き換えとマスクの後に読めなくなった行も含みます）
- キー（JSON の名前）に秘密らしい文字列がある
- 1 チャンクの上限（既定 32MiB）を超える 1 行がある

送った後に元のファイルが短くなった・書き換わったときは、そのファイルの続きを送らず、件数で知らせて終了コード 1 にします。
`purge` してから送り直すと、最初から送れます。

#### 取り込む

続ける場所の git の作業ツリーの中で実行します。記録の ID は `list` で見ます（既定は今のプロジェクトの記録だけ。`--all-projects` ですべて）。

```bash
looptrack session transcript list
looptrack session transcript pull 42
```

```text
会話記録 42 を置きました（ファイル 31）: /home/u/.claude/projects/-home-u-proj/c81d4e2a-3f6b-4a9c-8e1d-7b2f0a5c9d3e.jsonl
注意: 本文（AI の返答・ツールの出力など）に送った端末の絶対パスが 41 個残っています（書き換えない欄）
再開（fork）: claude --resume /home/u/.claude/projects/-home-u-proj/c81d4e2a-3f6b-4a9c-8e1d-7b2f0a5c9d3e.jsonl --fork-session
plain の claude --resume <id>（Claude Code）や codex resume（Codex）では開かないでください。取り込んだ写しに追記されます。必ず fork のコマンドを使ってください
```

- **`pull` は新しいセッション ID で置きます。** 写しは元の会話と同じ ID を名乗りません。表示された fork のコマンドで開いてください。CLI は AI を起動しません。
- **元の ID の会話を 2 つの端末で再開しないでください。** plain の `claude --resume`（`codex resume`）で開くと、取り込んだ写しに追記されます。
- 同じ記録をもう一度 `pull` すると、同じ場所の同じ写しを見つけて何も変えません。「置き済みです（何も変えていません）」の行と再開のコマンドを出します。
- Codex は `codex fork <新しい ID>` を表示します。作業ツリーのルートで実行してください。
- `--print-resume` は、標準出力に再開のコマンドの 1 行だけを出します（置くのは同じです）。`--out <ディレクトリ>` は、AI の置き場ではなくそのディレクトリに書き出します（中身を見るためのもので、再開のコマンドは出しません）。

次のときは置かずに止まります。

- 元のセッション ID の記録がこの端末にある（送った端末自身か、同じ会話が既にある）
- 送った端末と OS の系統が違う（系統が同じで OS だけが違う Linux と macOS は、注意を出してから置きます）
- 記録が消去済み
- 写しが別の作業ツリーの置き場にある、または同じ場所に中身の違う写しがある（場所だけを出します）
- git の作業ツリーの外で実行した

#### 消す

```bash
looptrack session transcript purge 42 --reason "不要になった"
```

本体を消します。取り消せません。消した後に同じ会話を `push` すると、最初から送り直せます。

#### 見える人と見えない人

- API・CLI・Web から読めるのは**送った本人だけ**です。プロジェクトの管理者にも、サーバの管理者（利用者の役割 admin）にも見えません（無いものと同じ扱いです）。
- **暗号化はしません。サーバの運用者は中身を読めます**（サーバのディスク・DB・バックアップを読める人です）。置き場の本体は、マスクとパスの書き換えの後の平文の JSONL です。
- 会話記録には、AI が読んだファイルの中身（ソースコード・`CLAUDE.md`・コマンドの出力）がそのまま入っています。送ることは、それらをサーバに置くことです。
- 秘密のマスクは、決まった規則に当たるものだけです。規則に当たらない秘密（形の決まっていない値・顧客の情報）はそのまま残ります。
- 消去は本体のファイルを消します。メタデータ（ファイルの相対パス・大きさ・sha256・件数・時刻）は残ります。バックアップに入った本体は、バックアップの側で消すまで残ります。
- 本体は、最後に送ってから 90 日（既定）で自動で消えます。1 利用者の合計は既定で 2GiB までで、超える送信は拒否されます。どちらも管理者が変えられます。

会話に資格情報の値を出さないことが、いちばんの守りです。
**資格情報の中身を読まない・表示しない・別の場所へ写さないでください。**

#### 自動で送る（hook）

`looptrack issue init` は、Claude Code と Codex の Stop と SessionEnd に、会話記録を送る hook を配線します（Copilot には配線しません）。入れないなら `looptrack issue init --no-transcript` で外せます。

- 送るのは、プロジェクト別ルール `session.transcripts` と、端末ごとの環境変数 `LOOPTRACK_SESSION_TRANSCRIPTS=1`（既定は切）が、どちらも入のときだけです。
- Stop では、前回送ろうとしてから 10 分たつまで送りません（間隔は `LOOPTRACK_TRANSCRIPT_THROTTLE_MIN` で変えられます）。SessionEnd では間引きません。
- 送るのは切り離した子プロセスで、hook はすぐ返ります。ターンを止めることはありません。
- セッションの冒頭（SessionStart の要約。これも init が配線します）に、次の行が出ます。ルールが切か、まだ分からないときは、はじめの 2 つは出ません。
  - 両方が入: 「会話記録の自動送信: 入」で始まる 1 行
  - ルールは入で、`LOOPTRACK_SESSION_TRANSCRIPTS=1` が無い: その設定を案内する 1 行
  - 送れていない: 最後の失敗の 1 行か、未送信の件数の 1 行。送れるまで、セッションを始めるたびに出ます
- 読めない行やキーの秘密で送れなかった記録は、失敗として 1 回だけ記録し、自動では送り直しません。手で打つ `looptrack session transcript push` はこれまでどおり使えます。

## MCP だけで導入する（setup ツール）

CLI も hook も入っていない PC でも導入はできます。出発点は MCP の接続設定だけをした状態です。

1. AI に MCP の接続設定を入れてブラウザで許可します。AI に接続の追加から任せるなら、下の「プロンプトを貼るだけで導入する」のプロンプトを使います。
2. AI に「イシュー管理を使えるようにして」と頼むと、AI が `setup` ツールを呼びます。
3. setup は最初に「loop を入れるか」という問いだけを返します。AI はそれを利用者に尋ね、答えを付けて setup を呼び直す。返ってくるのは取得 → SHA-256 の確認 → init をまとめた 1 つのコマンドです。AI は利用者の承認を得てから実行します。`~/.local/bin/looptrack`（Windows は `%LOCALAPPDATA%\Programs\looptrack\looptrack.exe`）が既にあり、配布物と SHA-256 が同じなら取得を省きます。違えば（無ければ）取得して確かめてから置き換え、その looptrack で init を行います。置き場が PATH に無ければ、同じコマンドが利用者の PATH に足します。macOS・Linux は既定のシェル（`$SHELL`）の起動ファイルに 1 行（zsh は `~/.zshenv`（環境に `ZDOTDIR` があればその下にも）、bash は `~/.bashrc` とログインのシェルが読むファイル、fish は `conf.d/looptrack.fish`、それ以外は `~/.profile`）、Windows は利用者の環境変数 `Path` です。既にあれば何も書きません。足したら、AI のアプリと端末を開き直すまで `looptrack` を名前では呼べません（hook は絶対パスで動きます）。手元の版が古いと【配布スクリプトの更新】が出るので、同じ setup の手順で取得し直します。
4. トークンが無ければ、AI が承認を得て setup の手順のログインのコマンド `~/.local/bin/looptrack issue login --browser --url <サーバの URL>`（Windows は PowerShell で `& (Join-Path $env:LOCALAPPDATA 'Programs\looptrack\looptrack.exe') issue login --browser --url <サーバの URL>`）を実行します。ブラウザでのログインと許可は利用者が行います。
5. 利用者が AI を起動し直して hook を承認します。
6. 次のセッションの開始時に、hook が導入済みであることをサーバへ知らせます。これでツール結果の【導入が未完了】が消えます。

## プロンプトを貼るだけで導入する（外部のサーバ）

別の PC やクラウドに置いた Looptrack のサーバに、手元の AI をつなぐ手順です。
利用者は下のプロンプトの `<サーバの URL>` と `<プロジェクト>` を埋めて AI に貼るだけです。
MCP の接続の追加・`setup` の呼び出し・導入のコマンドは、AI が進めます。
人が手を動かすのはブラウザでの許可とログイン・コマンドの承認・AI の再起動と hook の信頼だけです。

**トークンを AI に渡さないでください。** プロンプトにも会話にもトークン・パスワード・確認コードは書きません。
ログインは setup の手順の `issue login --browser --url <サーバの URL>` で行います（setup が置いた `looptrack` を絶対パスで実行します）。トークンは CLI とサーバの間だけを通ります。

Windows ではこの手順を実機で確かめていません。
VS Code の Copilot も実機では未確認です。

### 事前の準備

| 誰が | すること |
| -- | -- |
| サーバの管理者 | 利用者を作ります（`<サーバの URL>/admin/users`）。使うプロジェクトに editor で参加させます（同じ画面か `<サーバの URL>/admin/projects`） |
| 利用者 | サーバの URL（例 `https://example.com/looptrack`。末尾に `/mcp` を付けない）とプロジェクトの slug を管理者から受け取ります |
| 利用者 | ブラウザでサーバに一度ログインできることを確かめます。二段階認証が必須なら、初回のログインで認証アプリを登録します |
| 利用者 | つなぎたいリポジトリの一番上のディレクトリで AI を起動します。Codex はそのディレクトリを信頼（trusted）しておきます |

プロジェクトがまだ無いときはふつう管理者が先に作ります（`<サーバの URL>/admin/projects`）。
つなぐ利用者自身がサーバの管理者なら話は別です。先に作らなくてもかまいません。setup が「プロジェクトが見つかりません」を返すと、
管理者には MCP の `create_project` で作れることが案内に添えられます。AI はそれに従って作ってから setup を呼び直します。
prefix（ID の接頭辞）と width（番号の桁数）は後から変えられません。だから AI は作る前に slug・prefix・width を利用者に見せて確かめます。
管理者でない利用者には管理者に頼むよう案内が出ます。

プロンプトへ書き足すものは `<サーバの URL>` と `<プロジェクト>` のほかにありません。
同じプロンプトは何度貼っても大丈夫です。AI が導入の状態を `setup` で確かめて残りの手順から続けます。

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
7. ログインは setup の手順どおり issue login --browser のコマンド（置いた looptrack の絶対パスと --url 付き）で行う。コマンドのタイムアウトは 5 分より長くする。ブラウザでのログインと許可は私がする。
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
7. ログインは setup の手順どおり issue login --browser のコマンド（置いた looptrack の絶対パスと --url 付き）で行う。コマンドのタイムアウトは 5 分より長くする。ブラウザでのログインと許可は私がする。
8. トークンを読まない・表示しない。私にトークンを貼るよう頼まない。
9. [利用者] の手順（ターミナルの codex の /hooks でのフックの信頼）は私に頼んで止まる。私がこのプロンプトをもう一度貼ったら、setup が「導入済み」を返すことを確かめて終わる。
```

Codex の MCP の設定（`~/.codex/config.toml`）は PC 全体で 1 つしかありません。
ここに落とし穴があります。ほかのプロジェクト向けの looptrack が既にあると、1 で setup が使えるので 2 と 3 は飛ばされます。
そのとき引数 project が無いと、setup は接続のヘッダのプロジェクト（別のプロジェクト）向けの手順を返します。AI もそれに気づきません。
4 で project を渡しているのはこのためです（省略したときだけ、setup はヘッダのプロジェクトを使います）。
導入の後も MCP のツールはヘッダのプロジェクトを使います。ヘッダが別のプロジェクトなら、MCP のツールを呼ぶたびに引数 project を渡します。
もう 1 つ注意点があります。init がリポジトリの `.codex/config.toml` に書く環境変数は、Codex を起動し直すまで効きません。そこで setup は
Codex を起動し直す前でも動くよう、手順のコマンド（トークンの確認 `looptrack issue config` を含む）にサーバの URL と
プロジェクトをあらかじめ付けて返します。

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
7. ログインは setup の手順どおり issue login --browser のコマンド（置いた looptrack の絶対パスと --url 付き）で行う。コマンドのタイムアウトは 5 分より長くする。ブラウザでのログインと許可は私がする。
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
7. ログインは setup の手順どおり issue login --browser のコマンド（置いた looptrack の絶対パスと --url 付き）で行う。コマンドのタイムアウトは 5 分より長くする。ブラウザでのログインと許可は私がする。
8. トークンを読まない・表示しない。私にトークンを貼るよう頼まない。
9. [利用者] の手順（copilot を起動し直し、フォルダを信頼する確認で「今後も信頼」を選ぶ）は私に頼んで止まる。私がこのプロンプトをもう一度貼ったら、setup が「導入済み」を返すことを確かめて終わる。
```

Copilot だと、モデルによっては loop を入れるかを利用者に聞かずに決めてしまうことがあります。
だから Copilot のプロンプトでは 5 を強めに書いてあります。答えを最初からプロンプトに書き足しておくのも手です（例「loop は入れない」）。

### 人が手を動かす所

| 段 | 人がすること | なぜ人か |
| -- | -- | -- |
| 事前 | 管理者が利用者を作り、プロジェクトに参加させます | 権限はプロジェクトごとに管理者が付けます |
| MCP の接続の追加 | AI が見せた変更を承認します | 手元の AI の設定を書き換えるからです |
| MCP の許可 | Claude Code は `/mcp`、Codex はターミナルの `codex mcp login looptrack`、Copilot CLI は起動し直したときに自動で開くブラウザ（開かなかったときは `/mcp auth looptrack`）、VS Code は求められたときに、ブラウザでログインして許可します | サーバが本人を確かめます。パスワードと確認コードは AI に渡しません |
| 再起動と貼り直し | AI を起動し直し（VS Code は新しいチャット）、同じプロンプトを貼ります。Copilot CLI は、ブラウザで許可した後にもう一度起動し直します | 足した接続は、起動し直した後に読み込まれます。Copilot CLI は起動と同時に許可を始めるので、許可を待つ間に接続が 10 秒の上限を過ぎ、`Failed to connect … timed out after 10000 ms` になることがあります |
| loop の問い | 入れるか入れないかを答えます | loop を入れるかを決めるのは利用者です |
| 導入のコマンド | 取得 → SHA-256 の確認 → init の 1 つのコマンドを承認します。Claude Code では、auto mode でもこのコマンドの許可を求められます（Claude Code が `Contains brace with quote character (expansion obfuscation)` と判定するため） | 実行ファイルを `~/.local/bin` に置き、リポジトリのファイルを変えるからです |
| ログイン | setup の手順のログインのコマンド（setup が置いた `looptrack` の `issue login --browser --url <サーバの URL>`）を承認し、開いたブラウザでログイン（と二段階認証）と許可をします。この PC にトークンが既にあれば、この段は飛ばされます | トークンは CLI とサーバの間だけを通り、会話には出ません |
| hook の信頼 | Claude Code は再起動して確認が出たら承認、Codex はターミナルの `codex` の `/hooks` で信頼、Copilot は新しいセッション（Copilot CLI はフォルダを「今後も信頼」）を始めます | hook は利用者が認めた後にだけ動きます |

Codex では、AI が実行するシェルのコマンドの外部通信をサンドボックスが止めます。
そのため導入のコマンドとログインには、権限を上げて実行する承認が要ります。
Copilot CLI では MCP のツールを呼ぶたびに承認が要ります。

### うまくいかないとき

| 症状 | 確かめること |
| -- | -- |
| 許可のブラウザが開かない | 既定のブラウザを先に起動しておきます。Claude Code なら `/mcp` の画面を閉じて開き直します。ログインのコマンド（`issue login --browser`）は URL も表示するので、起動済みのブラウザに貼っても続けられます |
| 貼り直しても MCP のツールが使えない | 接続の追加の後に AI を起動し直したか・許可を済ませたかを確かめます。URL の末尾が `/mcp` であることも確かめます |
| ツール結果の【導入が未完了】が消えない | 再起動と hook の信頼を済ませたかを確かめます。Codex の未信頼の hook は知らせなしに飛ばされます。トークンが未登録だと hook は知らせを送れません（`looptrack issue config` で確かめます） |
| PATH や hook の配線が怪しい | `looptrack doctor` を実行します。PATH・hook の配線・導入の記録を確かめます（何も書きません） |
| setup で入れた `looptrack` が名前で見つからない | AI のアプリと端末を開き直したかを確かめます（PATH の変更は開き直した後に効きます）。それでも見つからなければ、setup の手順を実行し直すと置き場を PATH に足します。導入済みで手順が返らないときは、置き場の `~/.local/bin/looptrack doctor`（Windows は PowerShell で `& (Join-Path $env:LOCALAPPDATA 'Programs\looptrack\looptrack.exe') doctor`）が PATH に足すコマンドを示します |
| setup が「プロジェクトが見つかりません」を返す | slug の綴りを確かめます。あなたがサーバの管理者なら、AI が `create_project` で作ってよいかを slug・prefix・width を示して尋ねるので、値を確かめて答えます（prefix と width は後から変えられません）。管理者でなければ、管理者にプロジェクトを作ってもらうか、参加させてもらいます |
| 聞かれないまま loop が入った | `looptrack issue init --remove-loop` で外せます |
| ブラウザが無い環境で使いたい | 利用者が `<サーバの URL>/account` でトークンを発行し、自分のターミナルで `~/.local/bin/looptrack issue login --url <サーバの URL>`（Windows は PowerShell で `& (Join-Path $env:LOCALAPPDATA 'Programs\looptrack\looptrack.exe') issue login --url <サーバの URL>`。`/account` の画面に両方が出ます）に貼ります。AI には渡しません |
