# 管理者の手引き

[ガイドの目次](../README.md) · [サーバ版](README.md) · 関連: [サーバ版の始め方](getting-started.md) · [サーバと CLI の更新](updating.md)

管理者は利用者の役割が `admin` の人です。
最初の管理者は `looptrack setup` が作ります。
管理の画面は `<サーバの URL>/admin/…` にあります。

`looptrack user …` や `looptrack project …` のような管理コマンドは保存先に直接つなぎます。だから、実行の前に `.env` の `LOOPTRACK_DSN` を環境変数に入れておいてください。手順は[始め方](getting-started.md)の手順 4 にあります。
チームのサーバなら `docker compose run --rm --no-deps looptrack …` の形で実行します。

## 利用者

画面: `<サーバの URL>/admin/users`

| 操作 | 画面でできること |
| -- | -- |
| 追加 | ログイン名（英数字と `. _ -`）・表示名・初期パスワード・役割（admin / member） |
| 変更 | 役割の変更・無効化 / 有効化・パスワードの再設定・二段階認証のリセット・アクセストークンの失効 |
| プロジェクトの権限 | プロジェクトごとに viewer / editor / admin を付ける・外す |

- 自分自身の役割の変更と無効化はできません。有効な管理者が 0 人になる変更も拒否されます。
- 無効化するとその人のセッションは破棄。トークンも使えなくなります。
- パスワードの変更とアクセストークンの発行・失効は、各自が `<サーバの URL>/account` で行います。

同じことは管理コマンドでもできます。

```bash
looptrack user list
looptrack user add alice --name "Alice"          # パスワードを 2 回入力する
looptrack user disable alice
looptrack user totp-reset alice
```

## 権限

| 役割 | できること |
| -- | -- |
| viewer | 読むだけ。コメントもフィードバックも書けません |
| editor | 起票・コメント・状態の変更・担当の変更 |
| admin（プロジェクト） | editor と同じく書けます |

- 権限はプロジェクトごと。
- システムの管理者でも参加していないプロジェクトは読むだけです。書きたければ、自分を editor か admin で参加させましょう。
- 画面 `<サーバの URL>/admin/projects` なら全プロジェクトの参加者と役割を一覧で見て、その場で変えられます。
- 参加を外す相手や viewer に下げる相手が未完了のイシューを担当していると、代わりの担当者を選ぶよう求められます。

```bash
looptrack member set demo alice --role editor
looptrack member list demo
looptrack member remove demo alice --reassign -    # 担当のイシューは未設定に戻す
```

## プロジェクト

```bash
looptrack project create demo --prefix DEMO --name "Demo" --description "1 行の説明" --order 100
looptrack project list
looptrack project rename demo "Demo（新しい名前）"   # 表示名だけを変える
looptrack project archive demo                      # アーカイブ（画面の「削除」）
looptrack project list --archived                   # アーカイブしたものだけを出す
looptrack project unarchive demo                    # 戻す
```

- slug に使えるのは英小文字・数字・ハイフン。
- `--prefix` と `--width` は後から変えられません。発番済みの ID が壊れるからです。slug も同じく変えられず、変えられるのは表示名だけ（`project rename`）。
- 画面 `<サーバの URL>/admin/projects` でも各プロジェクトの「表示名とアーカイブ」から表示名の変更とアーカイブができます。使えるのは管理者だけです。
- **アーカイブは消去ではありません。** ハブ・API・MCP・CLI の一覧から消え、起票・更新・コメントも受け付けなくなります。それでもイシュー・コメント・経緯はちゃんと残ります。
  画面ではアーカイブの前に確認として slug の入力を求めます。戻せば元どおり一覧に出ますし、参加者と役割もそのまま使えます。
  戻すには画面の下の「アーカイブしたプロジェクト」で「戻す」を押すか、`project unarchive` を使います。slug と接頭辞が使い回されることはありません。
- プロジェクトごとの運用の文書も登録できます。登録すると、CLI・MCP の `guide` が共通の規則と一緒に AI へ返します。

```bash
looptrack project guide set demo ./demo-rules.md --source demo-rules.md
looptrack project guide show demo
```

## 二段階認証の必須 / 任意

| 設定 | 動き |
| -- | -- |
| 必須 | 全員に認証アプリ（TOTP）の登録を求めます。初回のログインで登録画面へ案内します |
| 任意 | 登録した人にだけ、ログイン時に確認コードを求めます。登録と解除は各自が `<サーバの URL>/account` で行います |

- 最初の設定は `looptrack setup` の ⑤ で決めます。
- あとから変えるなら画面 `<サーバの URL>/admin/security` か次のコマンドで。必須を外すときはパスワードの再入力が要ります。登録済みなら確認コードも求められます。
- 任意から必須にすると、二段階認証を通っていないセッションは無効になる。
- 登録済みの TOTP はどちらに切り替えても消えません。
- CLI・MCP のアクセストークンには影響しません。

```bash
looptrack settings two-factor              # 今の設定と変更の記録
looptrack settings two-factor required
```

**`.env` の `LOOPTRACK_SECRET_KEY` を失うと全員の TOTP が使えなくなります。** DB のバックアップには入っていません。別に控えておいてください。

## 連絡の保持日数

連絡は既定で 30 日を過ぎると読めなくなります。0 は無期限です。行は DB に残るので、消すときは管理用の資格情報で行います。

```bash
looptrack settings message-retention       # 今の日数
looptrack settings message-retention 90
```

## 記憶の版の保持日数

プロジェクト記憶の古い版は既定で期限なく残ります。保持日数は 36500 までです。短くすると、期限を過ぎた古い版と消した記憶の本体をサーバが 1 時間ごとに消します。
今の版は消しません。0 は無期限です。

```bash
looptrack settings memory-retention       # 今の日数
looptrack settings memory-retention 180
```

本体を持つ版は 1 件 100 版までで、超えると古い版から本体が消えます。

秘密が混ざったと分かったら、指した記憶の版の本体を消します。

```bash
looptrack purge-memory --project <slug> --kind decision --name <名前> --all-versions      # 対象の数と大きさだけを示す
looptrack purge-memory --project <slug> --kind decision --name <名前> --all-versions --reason <理由> --yes
```

1 つの版だけなら `--all-versions` の代わりに `--version <版>` を指定します。`--user <login>` を付けるとその利用者の personal の記憶を指します。
追記した節は後の版に引き継がれるので、秘密を消すときは全部の版を消します。`--version` で指した版の本文が後の版に残っていると、何も消さずに拒みます。
消した記録は `memory_purges` に残ります。今の版を消すと版が進むので、消す前に読んだ版の置き換えは拒まれます。
バックアップの中の本体はバックアップの側で消してください。バイナリログを有効にした MySQL のログに本文が残るかは未確認です。

## プロジェクト別ルールの設定

### require_on_close と verify

ルールは JSON で書いて `looptrack project rules set` で登録します。
**このコマンドはルール全体を置き換えます。** ファイルに書かなかったルールは消えます。
なので、ファイルで管理するなら使うルールを全部そのファイルに書いてください。

```json
{
  "verify": { "require_on_close": true },
  "usage": { "require_on_close": true }
}
```

```bash
looptrack project rules set demo ./demo-rules.json
looptrack project rules show demo
looptrack project rules clear demo
```

| キー | 働き |
| -- | -- |
| `verify.require_on_close` | 「## 検証コマンド」節のあるイシューは、今の本文に対する直近の `verify` が全件成功でないと Done にできません |
| `usage.require_on_close` | AI が Done / Canceled にするとき、その会話のトークン情報が無いと拒否します。CLI は拒否されると自動で付けてやり直します |
| `verify.require_evidence` | 既定で入。「## 検証コマンド」節のあるイシューは、今の本文に対する直近の `verify` の記録に添付が無いと Done にできません。`false` にすると拒否の代わりに注意だけになる（[日々の使い方](../daily-use.md#添付エビデンス)） |
| `session.digest_required` | 既定は切。入にすると、ループ kit の Stop hook が、引き継ぎの backend に関わらず、このセッションが着手中のイシューに完了より新しいセッション要約（`looptrack session digest <ID> -` で送る）が無いと、セッションを終わらせません。サーバ自体はこのルールで何も拒否しません。値は summary の応答の `session_digest_required` で hook に届きます |
| `session.transcripts` | 既定は切。入にすると、利用者が AI との会話記録をこのプロジェクトのサーバに置けます（`looptrack session transcript push`）。切のときは記録の登録と追記を拒否します（送った記録の読み出しと消去はできます）。hook が自動で送るには、利用者の端末ごとの `LOOPTRACK_SESSION_TRANSCRIPTS=1` も要ります。置いた記録を読めるのは持ち主だけです。値は、会話記録の登録とチャンクの追記の応答の `transcripts_enabled` でクライアントに届きます |
| `comment.reject_secret` | 既定で入。コメント（状態の変更と同時のコメントを含む）の本文に秘密らしい文字列（`token=…`・`Bearer …` など）があると拒否します。示すのは行番号と規則の語だけで、値は出しません。`false` にすると拒否の代わりに、保存して注記を返します。セッション要約はこのルールに関わらず拒否します |

知らないキーや綴りの誤りは登録の時点で拒否されます。
`--override "理由"` で上書きした記録はサーバに残ります。

既定で切のルールは版を上げた後もどこでも切のまま気づかれずに残りがちです。
アーカイブしていないどのプロジェクトでも切のルールがあると、サーバはそのことを入れ方と一緒に知らせます。
出る場所はサーバの起動時のログ・`<サーバの URL>/admin/projects` の上・`looptrack features` の出力です。`looptrack features` はサーバの上で実行します。`install.sh --upgrade` も最後にこれを呼びます。
いま対象になるのは 1.1.0 で足した `session.transcripts` です。

```bash
looptrack features    # 既定で切の機能ごとに入のプロジェクトの数を出す（終了コードはいつも 0）
```

## 添付の上限と消去

管理者の画面の「添付の管理」（`<サーバの URL>/admin/attachments`）では、添付の上限の変更・プロジェクトごとの使用量の確認・本体の消去ができます。
使い方は[日々の使い方](../daily-use.md#添付エビデンス)を見てください。

- 上限は 1 ファイル（既定 20MiB）と 1 プロジェクト（既定 1GiB。消去していない添付の合計）の 2 つで、全プロジェクトに共通です。変えれば再起動しなくても次の添付から効き、変更は操作した人つきで記録に残ります。
- 1 ファイルの上限を上げたら、前段のプロキシの本文の上限（nginx の `client_max_body_size` など）も合わせて上げてください。プロキシで止まった要求はサーバに届く前に 413 になります。
- 消去は秘密を誤って添付したときの逃げ道です。本体のファイルを消して理由と操作した人を記録します。
  ファイル名や大きさの記録は残り、読もうとすると「消去済み」と出ます。同じ内容を指すほかの添付もまとめて消去済みになり、元には戻せません。
- 消去した本体がバックアップの控えにも残っていれば、そちらからも消してください。

### 置き場とバックアップ

本体はサーバのディスクに、記録は DB に置きます。**バックアップは DB・添付の置き場・会話記録の置き場の 3 系統**で、DB を先に取ります（会話記録の置き場は下の「会話記録の置き場」）。

| 動かし方 | 添付の置き場 |
| -- | -- |
| install.sh（systemd） | `/var/lib/looptrack/attachments` |
| `looptrack setup` の compose | `<dir>/data/attachments`（保存先が MySQL でも同じ） |

install.sh の `--upgrade` が取る DB の控えに添付は入りません。
`looptrack export` は添付の本体と目録も書き出し、`looptrack verify-files` で本体が目録と一致するかを確かめられます。それでも `looptrack import` は添付を運ばないので、控えは 3 系統で取ってください。
控えの取り方・戻し方と、DB と置き場の食い違いを調べる `looptrack repair-attachments` は運用者向けの [DEPLOY.md の「添付の置き場とバックアップ」](../../../server/DEPLOY.md)にあります。

## 会話記録の上限と使用量

利用者は、AI との会話記録を自分の別の端末へ運べます（[会話記録をそのまま運ぶ](../ai-agents.md#会話記録をそのまま運ぶ)）。使えるのは、プロジェクト別ルール `session.transcripts` が入のプロジェクトだけです。
管理者の画面の「会話記録の管理」（`<サーバの URL>/admin/transcripts`。ヘッダの右端の利用者メニューから開けます）では、上限と保持期間を変え、利用者ごとの使用量を確かめられます。

- 上限は 1 チャンク（伸ばした後の大きさ。既定 32MiB）と 1 利用者（消去していない分の合計。既定 2GiB）の 2 つです。保持期間は最後の送信からの日数（既定 90 日。0 は無期限）です。どれも全利用者に共通で、上限は MiB、保持期間は日で入力します。
- 変えた値は、再起動しなくても次の送信から効きます。保持期間は 1 時間ごとの定期の処理が見て、過ぎた会話の本体を消します。変更は、変更前後の値と操作した人つきで記録に残ります。
- 使用量の表に出るのは、利用者ごとの会話の数と使用量（上限に対する割合）だけです。会話の中身・題名・ファイル名・セッション ID は出しません。管理者でも、画面と API からは会話記録を読めません。
- 画面に消去の操作はありません。本体を消すのは、持ち主（`looptrack session transcript purge`）・保持期間の切れ・運用者（下の `looptrack purge-transcripts`）です。
- 1 チャンクの上限を上げたら、前段のプロキシの本文の上限も合わせて上げてください（運用者向けの [DEPLOY.md の「前段のプロキシの上限」](../../../server/DEPLOY.md)）。
- 前段のプロキシが既定の大きさ（32MiB）のチャンクを通すかは `looptrack check-proxy` で確かめられます。公開の URL の会話記録の経路へその大きさのデータを認証なしで送るだけで、何も保存しません。`install.sh` は入れた後と `--upgrade` の後に実行します。手で実行するときは `looptrack check-proxy --url https://example.com/looptrack` のように公開の URL を渡します。本文を読み切る前に応える前段（読んだ大きさで止める Caddy の `request_body` など）は、この方法では確かめきれません。
- 前段が止めたチャンクはサーバに届かないので、サーバのログにも残りません。気づくのは利用者の側です。`push` とセッションの冒頭の要約に「前段のリバースプロキシが本文の大きさの上限で止めた（HTTP 413）」と出て、直せるのはサーバの管理者だと案内します。

### 会話記録の置き場

本体はサーバのディスクに、記録は DB に置きます。置き場は添付とは別のディレクトリです。

| 動かし方 | 会話記録の置き場 |
| -- | -- |
| install.sh（systemd） | `/var/lib/looptrack/transcripts` |
| `looptrack setup` の compose | `<dir>/data/transcripts` |

- 置き場は環境変数 `LOOPTRACK_TRANSCRIPT_DIR` で変えられます。会話記録の置き場と添付の置き場の一方がもう一方の中にある（同じ場所を含む）と、会話記録だけが使えなくなり、この画面に注意が出ます。
- 本体はマスクとパスの書き換えの後の平文で、暗号化しません。置き場とその控えを読める人は、中身を読めます。
- 控えは DB を先に取り、添付と会話記録の置き場を後から写します。消去した本体は、控えからも消してください。
- DB と置き場の食い違いは、運用者が `looptrack repair-transcripts` で調べます（引数なしは報告だけ。`--apply` はどこからも指されない本体を消します）。控えの取り方と合わせて、運用者向けの [DEPLOY.md の「添付の置き場とバックアップ」](../../../server/DEPLOY.md)にあります。
- ほかの利用者の会話記録を運用者が消す方法は、`looptrack purge-transcripts --user <login> [--transcript <id>] --reason <理由> --yes` だけです（管理者の画面には消去の操作がありません）。`--yes` が無いときは、消す対象の数と大きさを示すだけで何も変えません。`--yes` を付けると本体を消し、消去を cause `operator` として記録に残します。

## トークンレポート

AI の会話のトークン消費はイシューごと・段階ごとにサーバへ貯まっていきます。
記録されるもの・集計の仕方・レポートの作り方は [トークンレポート](../token-report.md) に詳しくまとめました。
期間ごとに集計して PDF のレポートを作り、台帳に登録できます。

1. プロジェクトのボード（`<サーバの URL>/p/<slug>/`）の「レポート作成」で期間を指定して、作成を依頼します。
2. 依頼は `summary` の末尾に出ます。skill `token-report` の入ったプロジェクトなら、AI に「トークンレポートを作成して」と頼むだけ。集計・本文・PDF・台帳の登録まで進みます。この skill は `looptrack issue init` が Claude Code 向けに `.claude/skills/token-report/SKILL.md` として置きます。PDF を作るのは `looptrack report pdf` です。
3. 手で集計するなら次のコマンドを使います。

```bash
looptrack issue usage requests                             # 画面から登録された作成依頼
looptrack issue usage report --since-last --json > r.json  # 前回のレポート以降の集計（--from / --to で期間指定）
looptrack issue usage report --since-last --xlsx r.xlsx    # 同じ集計を数表で
looptrack issue usage ledger add "2026年9月" --from-report r.json --note report.pdf   # 台帳に登録（取り消せない）
looptrack issue usage ledger list
looptrack issue usage missing --all-users                  # トークン情報が付いていない AI の操作と充足率
```

- PDF は `looptrack report pdf --report 集計.json --content 本文.json --out レポート.pdf` で作ります。日本語フォントは内蔵済み。
- 指示文（作業名）は既定では送りません。送るかどうかは画面 `<サーバの URL>/admin/projects` で切り替えられます。
