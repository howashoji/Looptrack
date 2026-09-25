# 更新

[ガイドの目次](README.md) · 関連: [始め方](getting-started.md) · [デスクトップ版](desktop.md) · [FAQ / トラブルシュート](faq.md)

Looptrack の更新は、手元の CLI・デスクトップ版・サーバの 3 つで手順が違います。
どれも**新しい版に自動で置き換わることはありません。** 置き換えは利用者か管理者が行います。
自動で行われるのは、新しい版の知らせ（CLI だけ）と、置き換えた後の後始末です。

## 何が自動で、何が手動か

| 対象 | 自動で行われること | 自分で行うこと |
| -- | -- | -- |
| CLI（`looptrack`） | 手元の版が古いと、AI のセッションの開始時と MCP のツールの結果に【配布スクリプトの更新】が付きます。サーバが `looptrack` を配っているか、対応する最低の版を決めているときだけです | `looptrack self-update` で置き換えます。続けて各プロジェクトで `looptrack issue init` を実行し直します |
| デスクトップ版 | 新しい版を最初に起動したとき、データが新しい形に変わります。ログイン時の起動の登録と CLI の置き場も、今のアプリに合わせて直ります | 新しい版に気づくことと、アプリごとの置き換えです。新しい版を知らせる仕組みはありません |
| サーバ | ありません | 管理者が `install.sh --upgrade` などで入れ替えます。利用者に配る `looptrack` も、管理者が配布ディレクトリに置き直します |

CLI のログインの期限（アクセストークン）も自動で延びますが、これは版の更新とは別の仕組みです。

## CLI（looptrack）

### 新しい版の知らせ

AI のセッションが始まると、hook がサーバに「この作業環境の `looptrack` の版」を知らせます。
サーバはそれを配っている最新の版と比べ、古ければ次の 2 か所に【配布スクリプトの更新】を付けます。

- セッションの開始時の要約（`looptrack hook summary`）の末尾
- MCP のツールの結果

案内には古い理由と、実行するコマンドが書かれています。
理由は「配布中の最新より古い」か「サーバが対応する最低の版より古い」のどちらかです。
後者は管理者が最低の版を決めたときだけ出ます。そのときは更新するまで正しく動かない機能があります。

サーバが `looptrack` を配っておらず、最低の版も決めていなければ、比べる相手が無いのでこの案内は出ません（[利用者に配る looptrack](#利用者に配る-looptrack配布ディレクトリ)）。
その場合はリリースのページで新しい版を確かめてください。

### self-update で置き換える

```bash
looptrack self-update --check --url <サーバの URL>   # 新しい版があるかを確かめるだけ（置き換えない）
looptrack self-update --url <サーバの URL>           # 置き換える
```

プロジェクトの中なら、`--url` は環境変数 `LOOPTRACK_API_URL` から取れます。
`self-update` はサーバの配布の一覧から、この OS と CPU に合う最新の `looptrack` を取ります。
取ったファイルは配布の一覧の SHA-256 と照らし合わせます。公式のビルドは `SHA256SUMS` の minisign の署名も確かめます。
合わなければ何も置き換えません。
Windows では実行中のファイルを消せないので、元のファイルを `looptrack.exe.old` に改名してから新しいものを置きます。`.old` は次の `self-update` で消えます。

手元の版が配布の版と同じか新しければ、何もしません。
自分でビルドした版（`dev` など）は配布の版と比べられないので、置き換えるには `--force` を付けます。

### self-update が置き換えないもの

次の 2 つは `self-update` では置き換えず、エラーで止まります。`--check` は使えます。

- **デスクトップ版の中の `looptrack`**: アプリごと置き換えます（[デスクトップ版](#デスクトップ版)）。
  配布の `looptrack` はトレイを持たないので、置き換えるとダブルクリックで起動できなくなるからです。
- **`install.sh` で入れたサーバの `looptrack`**: `install.sh --upgrade` で更新します（[サーバ](#サーバ)）。
  置き換えるだけでは DB の移行（migrate）も再起動もされず、動いているサーバと版がずれるからです。

### プロジェクトの kit をそろえる

hook・規則文・skill（kit）は `looptrack` に埋め込まれています。
`self-update` の後、各プロジェクトで一度 `init` を実行し直すと、kit と配線が新しい版にそろいます。

```bash
looptrack issue init --project <slug> --url <サーバの URL> --agent <AI>
```

kit が古いときも、【配布スクリプトの更新】の案内にこのコマンドが出ます。
更新した後、次のセッションの開始時にサーバへ知らせ直され、案内が消えます。

## デスクトップ版

デスクトップ版は `self-update` を使わず、アプリごと置き換えます。新しい版を知らせる仕組みはありません。
[リリースのページ](https://github.com/howashoji/looptrack/releases)で新しい版を確かめ、OS に合うファイルを取得してください。
手元の版は、アプリの中の `looptrack` で `looptrack version` を実行すると分かります（下の[更新を確かめる](#更新を確かめる)）。

先にトレイのメニューで「終了」を選び、次の手順で置き換えてから起動し直します。

| OS | 置き換え方 |
| -- | -- |
| macOS | 新しい dmg を開き、`Looptrack` を `アプリケーション` へドラッグして「置き換える」を選びます |
| Windows（インストーラ） | 新しいインストーラを実行します。前の版に上書きで入り、選択肢も残ります。Looptrack が動いていれば、インストーラが先に止めます |
| Windows（zip） | 新しい zip を、前と同じフォルダに上書きで展開します |
| Linux | 新しい AppImage を前のものと置き換えます。ファイル名は前のままでもかまいません |

置き換えた後に自動で行われることは次のとおりです。

- **データは残ります。** データはアプリとは別の場所にあり、新しい版を最初に起動したときに新しい形に変わります。
- 「ログイン時に起動する」の登録は、次の起動で今のアプリの場所に合わせて直ります。
- CLI の置き場も直ります。macOS と Linux のリンクはそのままアプリを指し、Windows の CLI の写しは次の起動で新しくなります。
  ただし `self-update` などで自分で新しくした写しには触りません。

データの置き場と手順の詳細は[デスクトップ版の「更新」](desktop.md#更新)にあります。

## サーバ

サーバは自動では更新されません。管理者が入れ替えます。
チームのサーバは起動しても DB の移行（migrate）を行わないので、入れ替えと一緒に migrate を実行します。
下の手順はどれも、止める → DB を控える → 実行ファイルを入れ替える → migrate → 起動、の順です。

### install.sh で入れたサーバ

```bash
sudo sh install.sh --upgrade --from <取得元>
```

`--from` は新しい版の取得元で、入れたときと同じ形です（GitHub Releases の `…/releases/download/<版>` か、手元の配布物のディレクトリ）。
`--upgrade` は次の順に進めます。

1. 新しい版を取得し、SHA-256 を確かめます。サーバに `minisign` があれば署名も確かめます。
2. サービスを止めます。
3. SQLite なら、止めた状態で DB を `backup-<日時>/` に写します。MySQL の控えは自分で取ってください（`mysqldump` など）。
4. 実行ファイルを入れ替えます。前の版は `/usr/local/bin/looptrack.prev` に残ります。compose なら新しいイメージを作り、前の版のイメージも残ります。
5. migrate を実行します。
6. 起動し、`/healthz` が応えるのを待ちます。

同じ版なら何も変えません。
MySQL で表が増えた版では、migrate の後にアプリ用の DB 利用者の権限（`grants.sql`）を流し直すまで読めないので、`--upgrade` はそこで止まって順番を案内します。
詳しくは運用者向けの [DEPLOY.md の「更新（--upgrade）」](../../server/DEPLOY.md)を見てください。

### looptrack setup だけで立ち上げたサーバ

`install.sh` を使わずに `looptrack setup` で立ち上げたサーバには、`--upgrade` のような一括の手順がありません。
上と同じ順番を手で進めます。compose（`setup` の既定）なら、`setup` を実行したディレクトリで次のとおりです。

```bash
docker compose stop
# SQLite なら ./data を控える。MySQL は mysqldump などで控える
# 新しい linux 向けの looptrack を、このディレクトリの looptrack と置き換える
# NOTICE も新しい looptrack の出力で置き換える（./looptrack licenses > NOTICE）
docker compose build
docker compose run --rm --no-deps looptrack migrate
docker compose up -d
```

systemd（`--service systemd`）で動かしているときも同じ順番です。
サービスを止め、実行ファイルを置き換え、`.env` の設定を読ませて `looptrack migrate` を実行してから起動します。

### 利用者に配る looptrack（配布ディレクトリ）

サーバの入れ替えだけでは、利用者の手元の `looptrack` は新しくなりません。
`self-update` と【配布スクリプトの更新】が使うのは、サーバの**配布ディレクトリ**に置いた `looptrack` です。
配布ディレクトリは `.env` の `LOOPTRACK_DIST_DIR` で指定します。指定が無ければ、サーバは `looptrack` を配りません。

新しい版を配るときは、管理者が配布ディレクトリの中身を置き換えます。

1. 各 OS 向けの `looptrack_<版>_<OS>_<CPU>`（Windows は末尾に `.exe`）を置きます。
2. 続けて `SHA256SUMS` を置きます。公式の配布物なら `SHA256SUMS.minisig` も置きます。公式のビルドの `self-update` は、署名が無いと置き換えないからです。
3. 前の版のファイルを消します。

サーバは OS と CPU の組ごとに最新の版を配ります。`SHA256SUMS` に載っていないファイルやハッシュが違うファイルは配りません。
`.env` に `LOOPTRACK_CLIENT_MIN_VERSION=v…` を書くと、それより古い `looptrack` に「対応する最低の版より古い」という案内が付きます。
置き方の詳細は [RELEASE.md の「サーバの配布ディレクトリから配る」](../../server/RELEASE.md)にあります。

## 更新を確かめる

```bash
looptrack version                # 手元の版（headless か desktop か・OS/CPU も出る）
looptrack self-update --check    # サーバが配っている最新の版と比べる（置き換えない）
looptrack doctor                 # PATH・配線に加え、配布の最新の版と手元の版を比べる
```

デスクトップ版の版は、アプリの中の `looptrack`（`Looptrack.app/Contents/MacOS/looptrack`・AppImage のファイル・Windows の `cli\looptrack.exe`）で `version` を実行して確かめます。
サーバの版は、サーバの上で `/usr/local/bin/looptrack version` を実行して確かめます（`install.sh` で入れた場合）。

## うまくいかないとき

| 症状 | 対処 |
| -- | -- |
| `self-update` が「サーバに … 向けの looptrack がありません」で止まる | サーバが自分の OS と CPU の `looptrack` を配っていません。管理者に配布ディレクトリへ置いてもらうか、[始め方](getting-started.md)の手順 1 で新しい版を取り直します |
| `self-update` が「サーバの URL がありません」で止まる | `--url <サーバの URL>` を付けるか、環境変数 `LOOPTRACK_API_URL` を設定します |
| `self-update` が「手元の版 … は配布の版 … と比べられません」と出す | 自分でビルドした版です。置き換えるなら `--force` を付けます |
| `self-update` が署名（`.minisig`）が無いと言って止まる | 配布ディレクトリに `SHA256SUMS.minisig` がありません。管理者に置いてもらいます |
| デスクトップ版で `self-update` がエラーになる | デスクトップ版はアプリごと置き換えます（[デスクトップ版](#デスクトップ版)） |
| サーバで `self-update` がエラーになり `install.sh --upgrade` を案内する | `install.sh` で入れたサーバです。`sudo sh install.sh --upgrade --from <取得元>` で更新します |
| 更新したのに【配布スクリプトの更新】が消えない | 次のセッションの開始時に知らせ直されるまで残ります。kit が古いと出ている場合は `looptrack issue init` も実行します |
| `install.sh --upgrade` の migrate が失敗した | systemd なら前の実行ファイルが `/usr/local/bin/looptrack.prev` にあり、compose なら前の版のイメージが残っています。戻し方はエラーの文に出ます |
