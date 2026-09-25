# 管理者の手引き

[ガイドの目次](README.md) · 前: [AI ごとの手引き](ai-agents.md) · 次: [FAQ / トラブルシュート](faq.md)

管理者とは、利用者の役割が `admin` の人のことです。
最初の管理者は `looptrack setup` が作ります。
管理の画面は `<サーバの URL>/admin/…` にあります。

`looptrack user …` や `looptrack project …` といった管理コマンドは、保存先に直接つなぎます。
`.env` の `LOOPTRACK_DSN` を環境変数に入れてから実行してください。手順は[始め方](getting-started.md)の手順 4 にあります。
チームのサーバでは `docker compose run --rm --no-deps looptrack …` の形で実行します。

## 利用者

画面: `<サーバの URL>/admin/users`

| 操作 | 画面でできること |
| -- | -- |
| 追加 | ログイン名（英数字と `. _ -`）・表示名・初期パスワード・役割（admin / member） |
| 変更 | 役割の変更・無効化 / 有効化・パスワードの再設定・二段階認証のリセット・アクセストークンの失効 |
| プロジェクトの権限 | プロジェクトごとに viewer / editor / admin を付ける・外す |

- 自分自身の役割の変更・無効化はできません。有効な管理者が 0 人になる変更も拒否されます。
- 無効化するとその人のセッションは破棄され、トークンも使えなくなります。
- パスワードの変更とアクセストークンの発行・失効は、各自が `<サーバの URL>/account` で行います。

管理コマンドでも同じことができます。

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

- 権限はプロジェクトごとです。
- システムの管理者でも、参加していないプロジェクトは読むだけです。書くには自分を editor か admin で参加させてください。
- 画面 `<サーバの URL>/admin/projects` では、全プロジェクトの参加者と役割を一覧で見て変えられます。
- 参加を外す相手や viewer に下げる相手が未完了のイシューの担当なら、代わりの担当者を選ぶよう求められます。

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

- slug は英小文字・数字・ハイフンです。
- `--prefix` と `--width` は後から変えられません。発番済みの ID が壊れるからです。slug も変えられません。変えられるのは表示名だけです（`project rename`）。
- 画面 `<サーバの URL>/admin/projects` でも、各プロジェクトの「表示名とアーカイブ」から表示名を変え、アーカイブできます。管理者だけが使えます。
- **アーカイブは消去ではありません。** ハブ・API・MCP・CLI の一覧から消え、起票・更新・コメントを受け付けなくなりますが、イシュー・コメント・経緯は残ります。
  画面ではアーカイブの前に確認のため slug の入力を求めます。戻すと元どおり一覧に出て、参加者と役割もそのまま使えます。
  画面の下の「アーカイブしたプロジェクト」から「戻す」を押すか、`project unarchive` を使ってください。slug と接頭辞は使い回されません。
- プロジェクトごとの運用の文書を登録しておくと、CLI・MCP の `guide` が共通の規則と一緒に AI へ返します。

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
- あとから変えるときは画面 `<サーバの URL>/admin/security` か、次のコマンドを使います。必須を外すときはパスワードの再入力が要り、登録済みなら確認コードも求められます。
- 任意から必須にすると、二段階認証を通っていないセッションは無効になります。
- 登録済みの TOTP はどちらに切り替えても消えません。
- CLI・MCP のアクセストークンには影響しません。

```bash
looptrack settings two-factor              # 今の設定と変更の記録
looptrack settings two-factor required
```

**`.env` の `LOOPTRACK_SECRET_KEY` を失うと、全員の TOTP が使えなくなります。** DB のバックアップには含まれないので、別に控えてください。

## プロジェクト別ルールの設定

### require_on_close と verify

ルールは JSON で書き、`looptrack project rules set` で登録します。
**このコマンドはルール全体を置き換えます。** ファイルに書かなかったルールは消えます。
ファイルで管理するなら、使うルールをすべてそのファイルに書いてください。

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

知らないキーや綴りの誤りは登録のときに拒否されます。
`--override "理由"` で上書きした記録はサーバに残ります。

## トークンレポート

AI の会話のトークン消費は、イシューごと・段階ごとにサーバに貯まります。
期間ごとに集計して PDF のレポートを作り、台帳に登録できます。

1. プロジェクトのボード（`<サーバの URL>/p/<slug>/`）の「レポート作成」で期間を指定し、作成を依頼します。
2. 依頼は `summary` の末尾に出ます。skill `token-report` の入ったプロジェクトなら、AI に「トークンレポートを作成して」と頼むだけで集計・本文・PDF・台帳の登録まで進みます。この skill は `looptrack issue init` が Claude Code 向けに `.claude/skills/token-report/SKILL.md` として置き、PDF は `looptrack report pdf` で作ります。
3. 手で集計するときは次のコマンドを使います。

```bash
looptrack issue usage requests                             # 画面から登録された作成依頼
looptrack issue usage report --since-last --json > r.json  # 前回のレポート以降の集計（--from / --to で期間指定）
looptrack issue usage report --since-last --xlsx r.xlsx    # 同じ集計を数表で
looptrack issue usage ledger add "2026年9月" --from-report r.json --note report.pdf   # 台帳に登録（取り消せない）
looptrack issue usage ledger list
looptrack issue usage missing --all-users                  # トークン情報が付いていない AI の操作と充足率
```

- PDF は `looptrack report pdf --report 集計.json --content 本文.json --out レポート.pdf` で作ります。日本語フォントは内蔵しています。
- 指示文（作業名）は既定では送りません。送るかどうかは画面 `<サーバの URL>/admin/projects` で切り替えられます。
