# FAQ / トラブルシュート

[ガイドの目次](README.md) · 前: [管理者の手引き](admin.md)

## 【導入が未完了】と出る

MCP のツールの結果に【導入が未完了】が付く。原因は 2 つのうちのどちらかです。
この AI の作業環境に hook・CLI・案内文がまだ入っていないか、入ったことがサーバに届いていないか。
導入済みの知らせを送るのは、セッション開始の hook です。

1. AI に「setup ツールの手順で導入して」と頼みます。AI が手順を示すので、中身を見て承認してください。
2. CLI が手元にあるなら、プロジェクトで `looptrack issue init --project <slug> --url <サーバの URL> --agent <AI>` を実行します。
3. AI を起動し直して hook を承認。Codex なら、ターミナルの `codex` の `/hooks` で信頼します。
4. 新しいセッションを始めます。それでも消えない？ 手元の状態を次のコマンドで確かめましょう。

```bash
looptrack issue installed --agent claude-code   # codex / copilot / other
looptrack doctor
```

## 【配布スクリプトの更新】と出る

手元の `looptrack` が、サーバの求める版より古いときの表示です。

```bash
looptrack self-update --check --url <サーバの URL>   # 新しい版があるかだけ確かめる
looptrack self-update --url <サーバの URL>           # 置き換える（SHA-256 を確かめる）
looptrack issue init --project <slug> --url <サーバの URL> --agent <AI>   # 規則文・skill・配線を新しくする
```

`self-update` は、サーバが配っている `looptrack` を取ってきて今の実行ファイルと入れ替えます。
プロジェクトの中で実行するなら、`--url` は環境変数 `LOOPTRACK_API_URL` から取れます。
ところが、配布のディレクトリを設定していないサーバは、そもそも `looptrack` を配っていません。そのときは[始め方](getting-started.md)の手順 1 で新しい版を取り直してください。
案内が出る仕組みや、デスクトップ版・サーバの更新は[更新](updating.md)にまとめました。

## トークン情報が未付与と出る

変更の操作の後に、こんな表示が出ることがあります。

```
トークン情報が未付与です。次を実行してください: … usage attach <ID>
```

出たら、表示されたコマンドを実行するだけ。

```bash
looptrack issue usage attach DEMO-0004
```

- `summary` の末尾に「トークン情報の未付与 N 件」と出たら、並んだイシューごとに `usage attach <ID>` を実行して回収します。対象は自分の AI の操作で、直近 7 日分。
- 付いていない操作と充足率は `looptrack issue usage missing` で見られます。
- 人がターミナルから打った操作は数えません。
- Copilot CLI なら、OpenTelemetry のファイル出力を有効にしておけば `usage attach` で付けられます。変更操作の後は自動で付きます。
- VS Code の Copilot では `usage attach` を実行しないでください。シェルに会話の ID が渡らないので、付けようがありません。
- 送信を止めたいときは、環境変数 `LOOPTRACK_USAGE=0` を設定します。

## ログインできない

| 症状 | 対処 |
| -- | -- |
| `アクセストークンがありません` / `トークンが無効です` / `ログインの有効期限が切れました` | `looptrack issue login --browser --url <サーバの URL>` をもう一度実行します |
| ブラウザが開かない | 表示された URL を起動済みのブラウザに貼ります。待ち受けは最長 5 分です |
| ブラウザの無い環境（ssh の先など） | `<サーバの URL>/account` でトークンを発行し、`looptrack issue login --url <サーバの URL>` に貼ります。トークンは画面に 1 回しか出ません |
| 画面でパスワードを忘れた | 管理者に `<サーバの URL>/admin/users` で再設定してもらいます |
| 認証アプリを失くした | 管理者に二段階認証をリセットしてもらいます（画面か `looptrack user totp-reset <login>`） |
| `プロジェクトが見つかりません` | slug の綴りが違うか、権限がありません。管理者に権限を付けてもらいます |
| 書くと 403 /「閲覧のみ」 | そのプロジェクトに viewer で参加しているか、参加していません。editor 以上にしてもらいます |
| `サーバに接続できません` | サーバが動いているか確かめます。`<サーバの URL>/healthz` が 200 を返せば動いています。ローカルなら `looptrack serve` のターミナルが開いているかを見てください |

古いプロジェクトだと、以前の導入が `.claude/` の下に置いた入口のスクリプトが残っていることがあります。
もう使いません。
`looptrack issue init` をもう一度実行すれば、これらを消したうえで、配線・許可・案内を `looptrack …` の形に直してくれます。

## 「セットアップ未完了」と出る

有効な管理者が 1 人もいないサーバは、画面を「セットアップ未完了」にして、API と MCP には 503（`setup_required`）を返します。
例外は監視用の `/healthz` だけ。

最初の管理者を作れば、サーバを起動し直さなくても通るようになります。

```bash
cd ~/looptrack-server
looptrack setup            # .env が無いとき
looptrack setup --force    # .env はあるが利用者が 0 人のとき（LOOPTRACK_SECRET_KEY は引き継ぐ）
```

`.env` を残したまま管理者だけを作るなら、`LOOPTRACK_DSN` を読み込んでから次を実行します。最初の利用者には `--two-factor` が欠かせません。

```bash
set -a; . ./.env; set +a
looptrack user add admin --name "Admin" --admin --two-factor optional
```

管理者を無効にして 0 人にしたときも同じ。サーバを起動し直すと「セットアップ未完了」になります。

## 作業を終えようとすると止められる

鮮度ガードの働きです。
イシューを参照して作業したのに、そのイシューを一度も更新していない。そうすると作業を終えられず、やり直しを求められます。
狙いは、次のセッションが古い状態を正しいと思い込んで読み始めるのを防ぐこと。

```bash
looptrack issue comment DEMO-0004 "調べたこと・やったこと・確かめたこと"
looptrack issue close DEMO-0004 --comment "受け入れ条件の検証結果"
```

本当に更新が要らないときに限って、理由を会話に残してから対象外にします。

```bash
looptrack issue-freshness ack DEMO-0004   # この ID だけ対象外
looptrack issue-freshness reset           # このセッションの記録をすべて対象外
looptrack issue-freshness show            # 今の記録
```

## ルールで拒否された

次にすることは、メッセージに書いてあります。コメントを付ける・`verify` を実行する・`usage attach` を実行する、といった中身です。
それに従ってください。
MCP や edit のような別の経路で回避しないこと。
上書きの `--override "理由"` を使うのは、利用者がはっきり求めたときだけです。

## push が競合で止まった

作業コピーを取った後で、誰かがイシューを更新したからです。
表示された差分を作業コピーに取り込んで、`looptrack issue push <ID> --rebase` で反映します。

## Windows でつまずきやすい点

| 症状 | 対処 |
| -- | -- |
| `looptrack` が見つからない | PATH の変更は新しく開いたターミナルとアプリにしか効きません。PowerShell と AI を起動し直してください |
| AI の hook だけが `looptrack` を見つけられない | スタートメニューなどから起動した AI は PATH が違うことがあります。`init` は PATH に無いと絶対パスで配線し、`.claude/settings.local.json` に書きます。`looptrack doctor` で確かめてください |
| 実行ファイルに警告が出る（「Windows によって PC が保護されました」） | Windows 版はまだ署名していないので、初回の起動で SmartScreen が警告を出すことがあります。SHA-256 が `SHA256SUMS` と一致するのを確かめてから、「詳細情報」→「実行」で進めてください（[始め方](getting-started.md)の「署名と OS の警告」）。macOS 版は署名・公証済みです |
| ヒアドキュメント（`<<'EOF'`）が使えない | PowerShell にはありません。本文をファイルに書き、`--body (Get-Content -Raw body.md)` で渡します。Git Bash なら使えます |
| `. ./.env` が使えない | PowerShell では[始め方](getting-started.md)の手順 4 にある 1 行で読み込みます |
| ゲート（`looptrack gates`）の make が失敗する | Windows の make は、日本語などを含むディレクトリで動かないことがあります。英数字だけのパスで作業してください |
| Codex の hook が動かない | ターミナルの `codex` をプロジェクトで起動し、`/hooks` で信頼します。デスクトップ版のチャット欄に `/hooks` と打ってもコマンドになりません |
| ポート 8090 が使われている | `looptrack setup --force` でポートを変えます。変えたら、init や MCP など URL を使う設定も直してください |

Windows のまっさらな環境での確認は、まだ続けています。
うまくいかない点があれば、リポジトリの issue で知らせてもらえると助かります。
