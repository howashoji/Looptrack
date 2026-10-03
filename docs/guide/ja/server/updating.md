# サーバと CLI の更新

[ガイドの目次](../README.md) · [サーバ版](README.md) · 関連: [サーバ版の始め方](getting-started.md) · [FAQ / トラブルシュート](../faq.md)

サーバと、手元の CLI（`looptrack`）の更新をまとめたページです。デスクトップ版は更新のしかたが違うので、[デスクトップ版の更新](../desktop/updating.md)を見てください。
**既定ではどちらも新しい版に自動で置き換わることはありません。**
サーバは管理者が設定で自動の置き換えを有効にできます（既定は無効）。それ以外の置き換えは、利用者か管理者の仕事です。
自動なのは新しい版の知らせと、置き換えた後の後始末のほうです。

## 何が自動で、何が手動か

| 対象 | 自動で行われること | 自分で行うこと |
| -- | -- | -- |
| CLI（`looptrack`） | 手元の版が古いと、AI のセッションの開始時と MCP のツールの結果に【配布スクリプトの更新】が付きます。サーバが `looptrack` を配っているか、対応する最低の版を決めているときだけです。CLI が GitHub のリリースを見るのは、`self-update` を実行したときだけです | AI に setup ツールを呼ばせ、返った手順（`--url` 付きの、取得と init をまとめた 1 つのコマンド）を実行させます。手で直すときも、setup ツールが返したその手順を使います |
| サーバ | 新しい版が出ると、管理者の画面の帯・`looptrack doctor`・サーバのログで知らせます（起動したときと 24 時間ごとに確かめます）。`install.sh` で入れた systemd のサーバは、設定で有効にしたときだけ（既定は無効）1 日 1 回自動で置き換えます | 管理者が `install.sh --upgrade` などで入れ替えます。利用者に配る `looptrack` は、`install.sh` で入れたサーバなら `install.sh` がそろえます。管理者が配布ディレクトリに置き直すのは、`install.sh` で入れていないサーバだけです（[利用者に配る looptrack](#利用者に配る-looptrack配布ディレクトリ)） |

CLI のログインの期限（アクセストークン）も自動で延びます。ただ、これは版の更新とは別の仕組みです。

## CLI（looptrack）

### 新しい版の知らせ

AI のセッションが始まると、hook がサーバに「この作業環境の `looptrack` の版」を知らせます。
サーバはそれを、配っている最新の版と比べます。古ければ、次の 2 か所に【配布スクリプトの更新】が付きます。

- セッションの開始時の要約（`looptrack hook summary`）の末尾
- MCP のツールの結果

案内に書かれているのは、古い理由と直し方（setup ツールを呼び、返った手順を実行する）。
理由は「配布中の最新より古い」か「サーバが対応する最低の版より古い」のどちらかです。
後者が出るのは、管理者が最低の版を決めたときだけ。この場合、更新するまで正しく動かない機能があります。

サーバが `looptrack` を配っておらず、最低の版も決めていなければ？ 比べる相手が無いので、この案内は出ません（[利用者に配る looptrack](#利用者に配る-looptrack配布ディレクトリ)）。
そのときはリリースのページで確かめるか、`looptrack self-update --from github --check` で GitHub のリリースと比べてください。

### self-update で置き換える

```bash
looptrack self-update --check --url <サーバの URL>   # 新しい版があるかを確かめるだけ（置き換えない）
looptrack self-update --url <サーバの URL>           # 置き換える
looptrack self-update                               # URL が無ければ GitHub のリリースから置き換える
```

取ってくる先は 2 つ。
`--url` か環境変数 `LOOPTRACK_API_URL` があればサーバの配布から、どちらも無ければ GitHub のリリースから取ります。
`--from server` か `--from github` で決めてもよい。片方で失敗しても、もう一方へは切り替えません。

**サーバの配布から取る**ときは、配布の一覧からこの OS と CPU に合う最新の `looptrack` を取ってきます。プロジェクトの中なら、`--url` は環境変数 `LOOPTRACK_API_URL` から取れます。
取ったファイルは、配布の一覧の SHA-256 と照らし合わせます。公式のビルドなら `SHA256SUMS` の minisign の署名も確かめます。
一覧の版が署名された版そのものかどうかも、確かめる対象。`SHA256SUMS` の中のファイル名（`looptrack_<版>_<os>_<arch>`）と、リリースなら署名の trusted comment に書かれた版。この 2 つが、どちらも一覧の版と同じでなければなりません。
合わなければ、何も置き換えません。
手元の版が配布の版と同じか新しければ、何もしません。古い版へ戻したいなら `--force` を付けます。
自分でビルドした版（`dev` など）は配布の版と比べられないので、置き換えるときも `--force` が要ります。

**GitHub のリリースから取る**ときの版の決め方は、[新しい版の確認](#新しい版の確認デスクトップ版とサーバ)と同じ。今の版が rc なら rc も追い、環境変数 `LOOPTRACK_UPDATE_CHANNEL` で変えられます。
取ってくるのはサーバ版の書庫（`looptrack_<版>_<os>_<arch>_server.tar.gz`、Windows は `.zip`）。
署名を確かめた `SHA256SUMS` で書庫を照合し、中の `looptrack` だけを取り出してから、同じ `SHA256SUMS` の `looptrack_<版>_<os>_<arch>` の行とも照らし合わせる。
置き換えるのは今の版より新しい版があるときだけで、`--force` は使えません。
署名を確かめる公開鍵を持たないビルドは、GitHub に問い合わせずに止まります。`LOOPTRACK_UPDATE_CHECK=off` のときも問い合わせない。

Windows では実行中のファイルを消せません。そこで元のファイルを `looptrack.exe.old` に改名してから、新しいものを置きます（どちらの取得元でも同じ）。`.old` を消すのは次の `self-update`。

### self-update が置き換えないもの

次の 2 つは `self-update` では置き換えず、エラーで止まります。`--check` なら使えます。

- **macOS と Linux のデスクトップ版の中の `looptrack`**: アプリごと置き換えます（[デスクトップ版の更新](../desktop/updating.md)）。
  配布の `looptrack` はトレイを持っていません。置き換えると、ダブルクリックで起動できなくなってしまいます。
  Windows の CLI はアプリ本体（`Looptrack.exe`）とは別のファイルなので、`self-update` で置き換えられます。置き換えた後に残るものは[CLI だけを self-update で新しくする](../desktop/updating.md#cli-だけを-self-update-で新しくする)にあります。
- **`install.sh` で入れたサーバの `looptrack`**: `install.sh --upgrade` で更新します（[サーバ](#サーバ)）。
  ファイルを置き換えるだけでは DB の移行（migrate）も再起動もされず、動いているサーバと版がずれるからです。

### プロジェクトの kit をそろえる

hook・規則文・skill（kit）は `looptrack` に埋め込まれています。
だから `self-update` の後は、各プロジェクトで一度 `init` を実行し直しておきましょう。kit と配線が新しい版にそろいます。

```bash
looptrack issue init --project <slug> --url <サーバの URL> --agent <AI>
```

【配布スクリプトの更新】の案内は、このコマンドを直接は示しません。AI に setup ツールを呼ばせると、`--url` 付きの取得 + init の 1 つのコマンドが返ります（置き場の `looptrack` が配布物と同じなら取得を省きます）。案内のコマンドを `--url` も `LOOPTRACK_API_URL` も無い端末で実行すると、手元のローカルモードに向いてしまうからです。
更新すると、次のセッションの開始時にサーバへ知らせ直され、案内は消えます。

## サーバ

サーバは既定では自動で更新されません。入れ替えるのは管理者です（新しい版は知らせます。下の「新しい版の知らせと自動の置き換え」）。
チームのサーバは、起動しても DB の移行（migrate）をしません。だから入れ替えと一緒に migrate を実行します。
下の手順はどれも同じ順番。止める → DB を控える → 実行ファイルを入れ替える → migrate → 起動、です。

### install.sh で入れたサーバ

```bash
curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh -s -- --upgrade
```

`--from` を付けなければ、新しい版は GitHub Releases の最新のリリースになります（`--version <版>` で選べます）。
ほかの場所から取るなら `--from <取得元>` を付けます（`deploy/release/dist.sh build` の出力のディレクトリか、同じ形の URL の接頭辞）。
1.0.0-rc.1・rc.2 の `install.sh` で入れたサーバも、同じ 1 行で上げられます。それらが置いた `/etc/looptrack/install.conf`・`.env`・unit（か `compose.yaml`）を、そのまま読むからです。
`--upgrade` の進み方は次のとおり。

1. 新しい版（GitHub Releases の `looptrack_<版>_linux_<arch>_server.tar.gz`）を取得し、展開する前に SHA-256 を確かめます。サーバに `minisign` があれば署名も確かめます。
   利用者に配る `looptrack`（6 つの OS・CPU の組）も取り、同じ `SHA256SUMS` で確かめます（下の「利用者に配る looptrack（配布ディレクトリ）」）。
2. サービスを止めます。
3. SQLite なら、止めた状態で DB を `backup-<日時>/` に写します。MySQL の控えは自分で取ってください（`mysqldump` など）。
   添付の本体はこの控えに入りません。添付の控えは別に取ります（[管理者の手引き](admin.md#置き場とバックアップ)）。
4. 実行ファイルを入れ替えます。前の版は `/usr/local/bin/looptrack.prev` に残ります。compose なら新しいイメージを作り、前の版のイメージも残ります。
5. migrate を実行します。
6. 利用者に配る新しい `looptrack` を配布ディレクトリに置いてから起動し、`/healthz` が応えるのを待ちます。systemd では、サービスを止めた後に同じポートの `/healthz` に別のプロセス（止め忘れた古いコンテナなど）が応えていれば、その応答を取り違えないよう、何も置き換えずにポート番号を示して止まります（無人の更新はサービスを起こし直してから、失敗として終わります）。起動した後も、ポートで待ち受けているのがサービス自身かを確かめます（`ss` が要ります。無ければ、サービスが動いているかだけを見ます）。

同じ版なら、サーバは置き換えません。利用者に配る `looptrack` だけをその版にそろえます（そろっていれば何も変えません）。
MySQL を最小権限で使っているときは、もう 1 段あります。表が増えた版では、migrate の後にアプリ用の DB 利用者に権限を与え直すまで読めません。そこで `--upgrade` は MySQL の管理用の資格情報を端末で尋ね（表示しません。保存しません）、権限を与え直してから起動します。
表が増える版（migrate がある版）は止めた後の migrate で表を作りますが、最小権限のアプリ用の利用者では作れません。表を作れる接続先を環境変数 `LOOPTRACK_SETUP_MIGRATE_DSN` で渡してください。渡していなければ、`--upgrade` は止める前にそれを示して続けるかを尋ね、既定では何も変えずに止まります。
詳しくは運用者向けの [DEPLOY.md の「更新（--upgrade）」](../../../server/DEPLOY.md)を見てください。

### 新しい版の知らせと自動の置き換え

サーバは起動したときと 24 時間ごとに GitHub Releases を確かめます。新しい版があれば、知らせる先は次の 3 か所。

- 管理者（role が admin の利用者）の画面の上部の帯。更新の 1 行も出ます。member には出ません
- 管理者のトークンで実行した `looptrack doctor` の注意の行
- サーバのログ（`journalctl -u looptrack`、compose なら `docker compose logs`）

確認を止めたいなら、サーバの `.env` に `LOOPTRACK_UPDATE_CHECK=off` を書いて起動し直します（ほかの設定は下の[新しい版の確認](#新しい版の確認デスクトップ版とサーバ)）。

`install.sh` で入れた systemd のサーバなら、設定で自動の置き換えを有効にできます。**既定は無効です。**

```bash
curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh -s -- --auto-upgrade on    # 有効にする（off で外す）
systemctl list-timers looptrack-upgrade.timer    # 次に動く時刻
journalctl -u looptrack-upgrade                  # 自動の置き換えの記録（置き換えなかった理由も出る）
```

有効にすると、1 日 1 回、上の `--upgrade` と同じ手順で新しい版に置き換えます。
動かすのは有効にしたときに手元に置いた `install.sh` の写しです。毎回取り直すことはしません（写しが新しくなるのは、管理者が `install.sh` を動かしたとき）。
取るのは新しい版の書庫だけ。署名は必ず確かめ（サーバに `minisign` が要ります）、版を下げることもしません。
途中で失敗したら前の版に戻して起動し直すので、サービスが止まったままにはなりません。
compose（コンテナのイメージ）は自動では置き換えません。知らせを見たら `--upgrade` を実行してください。
MySQL で、新しい版が DB の形を変える（migrate がある）ときも、自動では置き換えず今の版のまま動かします。そのときは表を作れる接続先（`LOOPTRACK_SETUP_MIGRATE_DSN`）を渡して、端末で `--upgrade` を実行します。
詳しくは [DEPLOY.md の「新しい版の知らせと自動の置き換え」](../../../server/DEPLOY.md)を見てください。

### looptrack setup だけで立ち上げたサーバ

`install.sh` を使わず `looptrack setup` で立ち上げたサーバには、`--upgrade` のような一括の手順がありません。
なので、上と同じ順番を手で進めます。compose（`setup` の既定）なら、`setup` を実行したディレクトリで次のとおり。

```bash
docker compose stop
# SQLite なら ./data を控える。MySQL は mysqldump などで控える
# 新しい linux 向けの looptrack を、このディレクトリの looptrack と置き換える
# NOTICE も新しい looptrack の出力で置き換える（./looptrack licenses > NOTICE）
docker compose build
docker compose run --rm --no-deps looptrack migrate
docker compose up -d
```

systemd（`--service systemd`）で動かしているときも、順番は同じです。
サービスを止め、実行ファイルを置き換え、`.env` の設定を読ませて `looptrack migrate` を実行してから起動します。

### 利用者に配る looptrack（配布ディレクトリ）

サーバを入れ替えただけでは、利用者の手元の `looptrack` は新しくなりません。
`self-update` と【配布スクリプトの更新】が使うのは、サーバの**配布ディレクトリ**に置いた `looptrack` です。
配布ディレクトリは `.env` の `LOOPTRACK_DIST_DIR` で指定します。指定が無ければ、サーバは `looptrack` を配りません。

**`install.sh` で入れたサーバでは、`install.sh` が受け持ちます。** 入れるときと `--upgrade`（自動の置き換えを含む）のたびに、取得したリリースから配布ディレクトリをそろえます。置くのは 6 つの OS・CPU の組の実行ファイル（どれも署名つきの `SHA256SUMS` で照合したもの）と、その `SHA256SUMS` と署名です。
置き場は systemd なら `/usr/local/share/looptrack/dist`、compose なら `<dir>/dist`（コンテナの `/dist` に読み取り専用で入れる）で、`.env` に `LOOPTRACK_DIST_DIR` が無ければ `install.sh` が足します。
以前の `install.sh` で入れたサーバは、新しい `install.sh` で 1 度 `--upgrade` を実行するとそろいます（同じ版でもそろえます。その後は案内に従ってサービスを起動し直します）。
`LOOPTRACK_DIST_DIR` が別の置き場を指していれば、`install.sh` はそこに触りません。以前の setup が書いた `compose.yaml` なら、先に `services.looptrack.volumes` に `- ./dist:/dist:ro` を足してください。Windows 向けの書庫（zip）を開くには、サーバに `unzip` か `python3` が要ります。
配る `looptrack` がサーバの版にそろっていない（置き場が無い・OS・CPU の組が足りない・古い）と、`looptrack serve` が起動時のログと管理者の画面の帯で知らせます。

それ以外（`install.sh` で入れていないサーバ・自分で用意する配布ディレクトリ）では、新しい版を配るときに管理者が配布ディレクトリの中身を置き換えます。

1. 各 OS 向けの `looptrack_<版>_<OS>_<CPU>`（Windows は末尾に `.exe`）を置きます。
   GitHub Releases に上がっているのは、素の実行ファイルではなく書庫です。各 `looptrack_<版>_<OS>_<CPU>_server.tar.gz`（Windows は `.zip`）から `looptrack` を取り出し、上の名前で置きます。
   書庫そのものは配布ディレクトリに置きません（サーバが配るのは素の実行ファイルだけ）。
2. 続けて `SHA256SUMS` を置きます。公式の配布物なら、リリースの `SHA256SUMS` と `SHA256SUMS.minisig` をそのまま置いてください。公式のビルドの `self-update` は、署名が無いと置き換えないからです。
   公式の `SHA256SUMS` には、書庫の中の実行ファイルを `looptrack_<版>_<OS>_<CPU>[.exe]` の名前にした行も載っています。つまり、取り出した実行ファイルは署名された一覧で照合されます。
3. 前の版のファイルを消します。

公式のリリースから置くときの例（サーバの Linux で）:

```bash
VER=v1.0.0
BASE="https://github.com/howashoji/looptrack/releases/download/$VER"
cd /path/to/dist            # LOOPTRACK_DIST_DIR
curl -fsSL -O "$BASE/SHA256SUMS" -O "$BASE/SHA256SUMS.minisig"
for t in linux_amd64 linux_arm64 darwin_amd64 darwin_arm64 windows_amd64 windows_arm64; do
  n="looptrack_${VER}_${t}_server"
  case $t in
    windows_*) curl -fsSL -O "$BASE/$n.zip" && grep -E " $n\.zip\$" SHA256SUMS | sha256sum -c - &&
                 unzip -p "$n.zip" "$n/looptrack.exe" >"looptrack_${VER}_${t}.exe" && rm "$n.zip" ;;
    *) curl -fsSL -O "$BASE/$n.tar.gz" && grep -E " $n\.tar\.gz\$" SHA256SUMS | sha256sum -c - &&
         tar -xzOf "$n.tar.gz" "$n/looptrack" >"looptrack_${VER}_${t}" && rm "$n.tar.gz" ;;
  esac || break
done
sha256sum -c --ignore-missing SHA256SUMS   # 取り出した実行ファイルの照合（書庫の行はここにファイルが無いので飛ばす）
```

サーバが配るのは、OS と CPU の組ごとの最新の版です。`SHA256SUMS` に載っていないファイルや、ハッシュが違うファイルは配りません。
`.env` に `LOOPTRACK_CLIENT_MIN_VERSION=v…` を書くと、それより古い `looptrack` に「対応する最低の版より古い」という案内が付きます。
置き方の詳細は [RELEASE.md の「サーバの配布ディレクトリから配る」](../../../server/RELEASE.md)にあります。

## 新しい版の確認（デスクトップ版とサーバ）

デスクトップ版とサーバ（`looptrack serve`）は、起動したときと 24 時間ごとに GitHub のリリースの一覧を確かめます。
CLI（`looptrack`）は自分からは確かめません。CLI に届く知らせは、サーバが付ける【配布スクリプトの更新】だけです。
ただし `self-update` を GitHub のリリースから実行したときは、下の環境変数がそのまま効きます。

- 知らせるのは、`SHA256SUMS` の署名を確かめられて、この OS と CPU 向けのファイル（デスクトップ版は dmg・AppImage・zip、サーバはサーバ版の書庫）がリリースにある版だけ。
- 追うのは今の版と同じ種類の版です。今の版が rc なら rc も知らせ、正式版なら正式版だけを知らせます。
- 確認に失敗した回（オフラインなど）は、前に知らせた版の知らせを残します。
- 自分でビルドした版（`dev` など、配布の版と比べられないもの）は確かめません。

次の環境変数で変えられます。デスクトップ版はアプリを起動する環境に渡し、サーバは `.env` に書いて起動し直します。
分からない値を書いたら？ 確かめません。取り違えた設定のまま通信しないためです。

| 環境変数 | 働き |
| -- | -- |
| `LOOPTRACK_UPDATE_CHECK=off` | 確かめません（GitHub へ通信しません）。デスクトップ版は、トレイの「新しい版を確認する」のチェックを外しても同じです。どちらかが off なら確かめません |
| `LOOPTRACK_UPDATE_CHANNEL` | 追う版を `stable`（正式版だけ）か `prerelease`（rc も）に決めます。書かなければ今の版で決まります |
| `LOOPTRACK_UPDATE_URL` | 確認先を差し替えます。GitHub の API と同じ形の JSON を返す `https://` の URL だけを受けます |

## 更新を確かめる

```bash
looptrack version                # 手元の版（headless か desktop か・OS/CPU も出る）
looptrack self-update --check    # 最新の版と比べる（サーバの配布か GitHub のリリース。置き換えない）
looptrack doctor                 # PATH・配線に加え、配布の最新の版と手元の版を比べる（admin のトークンならサーバの新しい版も出る）
```

デスクトップ版の版の確かめ方は、[デスクトップ版の更新](../desktop/updating.md#新しい版の知らせ)にあります。
サーバの版はサーバの上で `/usr/local/bin/looptrack version` を実行すれば分かります（`install.sh` で入れた場合）。

## うまくいかないとき

| 症状 | 対処 |
| -- | -- |
| `self-update` が「サーバに … 向けの looptrack がありません」で止まる | サーバが自分の OS と CPU の `looptrack` を配っていません。管理者に配布ディレクトリへ置いてもらうか、[始め方](getting-started.md)の手順 1 で新しい版を取り直します |
| `self-update` が「サーバの URL がありません」で止まる | `--from server` を付けたのに URL が無いときの表示。`--url <サーバの URL>` を付けるか、環境変数 `LOOPTRACK_API_URL` を設定します。GitHub のリリースから取るなら `--from github` にします |
| `self-update` が「公開鍵を持たないので、GitHub のリリースからは置き換えません」で止まる | 署名を確かめられないビルドです。`--url` でサーバの配布から取るか、公式のリリースの `looptrack` を入れ直します |
| `self-update` が「GitHub のリリースから取るときは --force を使えません」で止まる | GitHub のリリースからは新しい版しか入れません。古い版へ戻すなら、リリースのページから書庫を取って手で置き換えます |
| `self-update` が「LOOPTRACK_UPDATE_CHECK=off なので GitHub に問い合わせません」で止まる | `LOOPTRACK_UPDATE_CHECK` を外すか、`--url` でサーバの配布から取ります |
| `self-update` が「手元の版 … は配布の版 … と比べられません」と出す | 自分でビルドした版です。置き換えるなら `--force` を付けます |
| `self-update` が署名（`.minisig`）が無いと言って止まる | 配布ディレクトリに `SHA256SUMS.minisig` がありません。管理者に置いてもらいます |
| macOS・Linux のデスクトップ版で `self-update` がエラーになる | デスクトップ版はアプリごと置き換えます（[デスクトップ版の更新](../desktop/updating.md)） |
| サーバで `self-update` がエラーになり、インストーラの `--upgrade` を案内する | `install.sh` で入れたサーバです。`curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh \| sudo sh -s -- --upgrade` で更新します |
| 更新したのに【配布スクリプトの更新】が消えない | 次のセッションの開始時に知らせ直されるまで残ります。kit が古いと出ている場合は `looptrack issue init` も実行します |
| `install.sh --upgrade` の migrate が失敗した | systemd なら前の実行ファイルが `/usr/local/bin/looptrack.prev` にあり、compose なら前の版のイメージが残っています。戻し方はエラーの文に出ます。migrate が 1 本でも適用していれば（出力に「適用:」の行があれば）、DB も更新の前の控えに戻します |
| `install.sh --auto-upgrade on` が「何も変えていません」で止まる | 自動の置き換えを入れられないサーバです。compose のサーバ・`--no-start` で入れたサーバ・systemd が動いていないサーバでは使えません。`minisign`（Debian・Ubuntu は `apt-get install -y minisign`、AlmaLinux などは EPEL から `dnf install -y epel-release && dnf install -y minisign`）と、新しい版の書庫と署名を取るための `curl` か `wget` も要ります |
| 自動の置き換えを有効にしたのに新しい版にならない | `journalctl -u looptrack-upgrade` に理由が出ます。MySQL で新しい版が DB の形を変えるときは自動では置き換えないので、端末で `--upgrade` を実行します。途中で失敗した回は前の版に戻して動いています |
| 前の版に戻したら「新しい版の looptrack で migrate した DB」と出て起動しない | 新しい版が DB を新しい形に変えた後です。前の版はその DB を使いません（書き込みで壊さないため）。新しい版に戻すか、前の版で使うなら、DB（デスクトップ版はデータのフォルダ）を新しい版で migrate する前の控えに戻します（デスクトップ版はデータのフォルダの `backups` に控えがあります） |
