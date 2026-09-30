# 配置手順（Looptrack のサーバ）

サーバ（`looptrack serve`）の立ち上げ・更新・利用者の登録の手順をまとめます。
新しく立ち上げるときは、対話のウィザード `looptrack setup` を使います。
まっさらな Linux サーバなら、1 行で走るインストーラ（`deploy/install.sh`）が手軽です。取得 → 照合 → 展開 → setup →（MySQL の権限）→ 起動 → 確認を 1 回で行います。
URL の例では公開 URL を `https://example.com` とし、接頭辞（`LOOPTRACK_BASE_PATH`）は既定の `/looptrack` で書いています。

## コンテナ（setup が書く compose.yaml）

| 項目 | 値 |
| -- | -- |
| イメージ | `scratch` + 静的リンクの `looptrack` 1 本（約 28MB。ENTRYPOINT `/looptrack`・CMD `serve`。クライアントの機能と PDF のフォントも入る）。シェルも curl も無い（`deploy/Dockerfile`。手元で作るときは `deploy/build.sh`） |
| 公開 | `127.0.0.1:<ポート>` のみ（外はリバースプロキシで受ける） |
| メモリ | `mem_limit: 96m`・`GOMEMLIMIT=64MiB` |
| 権限 | `read_only`・`no-new-privileges`・`cap_drop: ALL`・非 root（65534） |
| ログ | json-file・10MB × 3 |
| 健全性確認 | `looptrack healthcheck`（自分自身の `/healthz` を叩く）を 60 秒ごと |

MySQL を使うときは、アプリ用の DB 利用者にテーブル単位の権限だけを与えます。
例は `deploy/grants.sql` にあります（DB 名 `im`・利用者 `im_app`）。
スキーマの変更（migrate）は管理用の資格情報で行い、アプリ用の利用者には DDL 権限を与えません。
**テーブル単位の `GRANT` はテーブルができてからしか流せません。** そのため流す順番は「migrate → 権限 → 起動」になります。
権限の中身は `looptrack` に埋め込んであり、`looptrack grants print` で DB 名・利用者名に合わせた GRANT 文を出し、`looptrack grants apply` で管理用の資格情報を尋ねて流せます。
install.sh での順番は下の「保存先に MySQL を選ぶとき」を見てください。

## 秘密情報

- `.env`（600・git 管理外）に `LOOPTRACK_DSN` と `LOOPTRACK_SECRET_KEY` を置きます。
  前者は DB のパスワードを含む接続先です。後者は TOTP の暗号化鍵で、`looptrack secret-key` で作ります。
- **`LOOPTRACK_SECRET_KEY` を失うと全利用者の TOTP が使えなくなります。** 全員が `looptrack user totp-reset` で登録し直すことになります。
  この鍵は DB のダンプに含まれないので、`.env` を別に控えておいてください。

## 設定（環境変数）と既定値

サーバは `.env` か環境変数で設定します。一覧と既定値の元の定義は `cmd/looptrack/serve.go` の冒頭のコメントです。
既定値はどれも設定で上書きできます。

| 設定 | 既定 | 用途 |
| -- | -- | -- |
| `LOOPTRACK_BASE_PATH` | `/looptrack` | URL の接頭辞。既存の URL・Cookie の Path を保つときは以前の接頭辞を設定する |
| `LOOPTRACK_PUBLIC_URL` | （なし） | 外から見た URL の基点（例 `https://example.com`）。OAuth のメタデータに使う |
| `LOOPTRACK_LISTEN` | `:8090`（ローカルモードは `127.0.0.1:8090`） | 待ち受け |
| `LOOPTRACK_TOTP_ISSUER` | `Looptrack` | TOTP の発行者名（認証アプリに表示される名前）。登録済みの認証アプリの表示を保つときは以前の名前を設定する |
| `LOOPTRACK_COOKIE_SECURE`・`LOOPTRACK_TRUSTED_PROXIES` | `true`・`127.0.0.1/32,::1/128,172.16.0.0/12` | https 前提の Cookie・`X-Real-IP` を信用する接続元 |

CLI（`looptrack issue`）の接続先は `LOOPTRACK_API_URL` か `init --url` / `login --url` で決まります。
どれも無いときは手元のローカルモードのアドレス `http://127.0.0.1:8090/looptrack` が既定です。
これは `looptrack setup` でローカル利用を選んだときの既定のポートと接頭辞です。

## 新しく立ち上げる（looptrack setup）

`.env` や `compose.yaml` を手で書く代わりに `looptrack setup` を使います。
問いに順に答えると、設定ファイル・スキーマ・最初の管理者がそろいます。

```bash
looptrack setup --dir /opt/looptrack     # --dir を省くと今のディレクトリ
```

| 問い | 選択肢（[ ] は既定。Enter で既定を採る） |
| -- | -- |
| ① 使い方 | [ローカルの 1 人利用]（127.0.0.1 固定・ログインを省く）/ チームのサーバ |
| ② 保存先 | SQLite（ファイル 1 つ。ローカルの既定）/ MySQL（チームの既定。DSN と、テーブルを作るときの接続先（root など。Enter で同じ）） |
| ③ 待ち受け | ポート [8090]・URL の接頭辞 [/looptrack]・公開 URL（チームのみ。パスなし。例 `https://im.example.com`） |
| ④ 最初の管理者 | ログイン名 [admin]・表示名・パスワード（12 文字以上。2 回入力。端末では表示しない） |
| ⑤ 二段階認証 | 必須 / 任意（**必ず聞く**。既定はチームなら必須・ローカルなら任意。あとから `settings two-factor` や管理画面で変えられる） |
| ⑥ 最初のプロジェクト | slug（ローカルの既定 [main]・チームの既定 [-]＝作らない）・接頭辞 [slug の英大文字]・表示名 [slug]。作ると最初の管理者を admin で参加させる |

最後に内容を確認してから書き込みます。作られるものは次のとおりです。

- `<dir>/.env`（600）: `LOOPTRACK_DSN`・`LOOPTRACK_SECRET_KEY`（安全な乱数で生成）・`LOOPTRACK_LISTEN`・`LOOPTRACK_BASE_PATH`・`LOOPTRACK_PUBLIC_URL`・`LOOPTRACK_COOKIE_SECURE`。ローカルなら `LOOPTRACK_LOCAL_MODE=1` も入ります。
- チームのサーバ（`--service compose`。既定）なら `<dir>/compose.yaml`。
  公開は `127.0.0.1:<ポート>` だけで、外はリバースプロキシで受けます。SQLite なら `./data` を `/data` に置きます。
- DB: 接続を確かめ、マイグレーションを流します。
  続けて最初の管理者・二段階認証の設定（変更の記録も残ります）・最初のプロジェクトと管理者の参加を、1 つのトランザクションで作ります（`setupwiz.Provision`）。

終わると「起動」「ブラウザで開く URL」「CLI のログイン（`looptrack issue login --browser --url …`）」「MCP の接続設定（Claude Code・Codex・Copilot）」が表示されます。

起動の方法は `--service` で選びます。チームのサーバだけのオプションで、対話では聞かれず引数でだけ指定します。

| `--service` | 動き |
| -- | -- |
| `compose`（既定） | `compose.yaml` と、その `build` が使う `Dockerfile`・`NOTICE` を書く（Linux では実行ファイル自身も `looptrack` として複製する。ほかの OS では `linux/amd64` の実行ファイルを自分で置く）。`LOOPTRACK_LISTEN=:<ポート>`（コンテナの中）。SQLite は `.env` にコンテナの中の `/data/im.db`。「起動」は `docker compose up -d`（イメージは隣の `Dockerfile` から作る。レジストリからは取らない） |
| `systemd` | `compose.yaml` を書かない。`LOOPTRACK_LISTEN=127.0.0.1:<ポート>`。SQLite は `--sqlite-path`（既定 `<dir>/data/im.db`）をそのまま `.env` に書く。「起動」は `systemctl enable --now looptrack` |
| `none` | ファイルは `systemd` と同じ。「起動」を出さない（呼び出し元が案内する） |

非対話でも同じ結果になります。秘密はプロセスの一覧（`ps`）に出てしまうので、引数には取りません。

- MySQL の接続先は `LOOPTRACK_SETUP_DSN` で渡します。テーブル作成用に別の利用者を使うなら `LOOPTRACK_SETUP_MIGRATE_DSN` も渡します。
  DB がまだ無ければ、端末で MySQL の管理用の資格情報を尋ね、確かめてから DB とアプリ用の利用者を作ります（`--yes` でも確かめます。下の「保存先に MySQL を選ぶとき」）。
  表を作る利用者と、その DB への `GRANT` は先に用意しておきます。
- パスワードは `--admin-password-file <file>`（`-` で標準入力の 1 行）か `LOOPTRACK_SETUP_ADMIN_PASSWORD` で渡します。
- `--yes` では `--two-factor` の指定が必須です。既定では決めません。

```bash
LOOPTRACK_SETUP_DSN='im_app:<pw>@tcp(mysql:3306)/im?parseTime=true' LOOPTRACK_SETUP_MIGRATE_DSN='root:<pw>@tcp(127.0.0.1:3306)/im?parseTime=true' \
  looptrack setup --dir /opt/looptrack --yes --mode team --store mysql --port 8090 --base-path /looptrack \
  --public-url https://im.example.com --admin-login alice --admin-name "Alice" --admin-password-file ./pw.txt --two-factor required \
  [--project web --project-prefix WEB --project-name "Web サイト"] [--service compose|systemd|none]
```

`--yes` では、最初のプロジェクトは `--project` を付けたときだけ作ります。

- **途中でやめても壊れません。** Ctrl-C・入力の終わり・接続やマイグレーションの失敗では、`.env` も利用者も残りません。
  ファイルは一時ファイルに書いて最後に置き換えます。管理者の作成は最後の段で、失敗したら置いたファイルを戻します。
  マイグレーションは適用済みのまま残りますが、やり直せば続きから通ります。
- **2 回目は何も書き換えません。** `<dir>/.env` があるか保存先にすでに利用者がいれば、「設定済み」として今の設定と利用者の数を示して終わります。
  作り直すときは `--force` を付けます。`LOOPTRACK_SECRET_KEY` は引き継ぎ、前のファイルは `.env.bak-<日時>` に退避します。
  既存の利用者は消さず、同じログイン名がいればパスワードも変えません。
- ローカル利用（SQLite・`LOOPTRACK_LOCAL_MODE=1`）は `looptrack serve --env-file <dir>/.env` で起動します（DESIGN.md §3-3）。
- ローカル利用はターミナルなしでも始められます。`LOOPTRACK_LOCAL_MODE=1 LOOPTRACK_DSN=sqlite:<ファイル> looptrack serve` だけで起動すると、スキーマができます。
  鍵は `<ファイル>.secret-key`（本人だけが読める）に作られます。127.0.0.1 から開いたブラウザには初回設定の画面（④〜⑥）が出ます。

## まっさらな Linux サーバに入れる（deploy/install.sh）

Ubuntu の LTS や Debian のサーバなら、`deploy/install.sh`（POSIX sh）1 つで取得 → 照合 → 展開 → 設定（`looptrack setup`）→（MySQL の権限）→ 起動 → 動作確認まで進みます。
問いに答えるだけで、公開 URL（リバースプロキシの後ろ）からブラウザでログインできるようになります。MCP と CLI の接続設定も表示されます。

インストーラは raw.githubusercontent.com の main から 1 行で取って走らせます（Homebrew の初期インストールと同じ形）。
README の 1 行は版を含まないので、リリースのたびに変わりません。インストーラは既定で GitHub Releases の最新（`releases/latest`）の書庫を取ります。

```bash
curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh

# 先に中身を読むなら
curl -fsSL -o install.sh https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh
less install.sh
sudo sh install.sh

# 手元で作った配布物（deploy/release/dist.sh build の出力）から入れるとき
sudo sh deploy/install.sh --from /path/to/dist
```

**インストーラのスクリプト自身は HTTPS で取るだけで、照合できません**（照合の鍵と手順をそのスクリプトが持つため）。
照合するのは、スクリプトが取る書庫です（下の「取得元の規約」）。スクリプトを信用できないときは、取って読んでから実行してください。

```bash
# 対話（動かし方 → setup の ①〜⑥。① は「チームのサーバ」を選ぶ）
curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh

# 非対話（同じ結果になる。setup の引数は -- の後ろ。秘密は引数に取らない）
printf '%s\n' '<管理者のパスワード>' > /root/pw && chmod 600 /root/pw
curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh -s -- --yes --method systemd -- \
  --store sqlite --public-url https://im.example.com --admin-login alice --admin-password-file /root/pw --two-factor required \
  [--project web --project-name "Web サイト"]      # 最初のプロジェクト（--yes では指定したときだけ作る）
#   MySQL なら --store mysql と環境変数 LOOPTRACK_SETUP_DSN（テーブル作成用に別の利用者なら LOOPTRACK_SETUP_MIGRATE_DSN）。
#   sudo は環境変数を落とすので、sudo -E か sudo env LOOPTRACK_SETUP_DSN=… sh -s -- … で渡す

… | sudo sh -s -- --upgrade                      # 実行ファイルを入れ替え、migrate して再起動（データは保つ）
… | sudo sh -s -- --auto-upgrade on             # 自動の置き換え（1 日 1 回の systemd timer）を入れる。既定は off。外すのは off
… | sudo sh -s -- --uninstall                    # 外す（設定とデータは残す）。--purge で設定・データも消す
```

| オプション | 意味 |
| -- | -- |
| `--from <ディレクトリ \| URL の接頭辞>` | 取得元（環境変数 `LOOPTRACK_INSTALL_FROM`）。省略すると GitHub Releases の `…/releases/latest/download`。下の規約 |
| `--version <版>` | 入れる版（`…/releases/download/<版>` から取る）。`--from` を付けたときは、取得元に複数の版があるときの選択 |
| `--sha256 <hash>` | 取るファイル（書庫か素の実行ファイル）の SHA-256 を別経路で知った値で固定する |
| `--require-signature` | SHA256SUMS の署名を必須にする（`LOOPTRACK_INSTALL_REQUIRE_SIGNATURE=1`） |
| `--method systemd\|compose` | 動かし方（対話では問う。`--yes` の既定は systemd） |
| `--dir <dir>` | compose の置き場（既定 `/opt/looptrack`） |
| `--no-start` | 設定まで行い、起動しない（compose ではイメージも作らない） |
| `--auto-upgrade on\|off` | 自動の置き換え（`LOOPTRACK_INSTALL_AUTO_UPGRADE`）。**既定は off**。入れた後のサーバにだけ付けても効きます（入れ直しません）。下の「新しい版の知らせと自動の置き換え」 |
| `--only-newer` | `--upgrade` で、取得した版が入っている版より新しいときだけ置き換える（自動の置き換えが使う） |
| `--yes`（`--upgrade` で端末が無いとき） | 無人の更新。MySQL で新しい版に未適用の migrate があれば置き換えず、止めた後に失敗すれば前の版に戻して起動し直す（下の「新しい版の知らせと自動の置き換え」） |

### 取得元の規約

`--from` を付けなければ、取得元は GitHub Releases の `https://github.com/howashoji/looptrack/releases/latest/download` です（`--version <版>` なら `…/releases/download/<版>`）。
Releases の最新は、公開側でプレリリースを Latest にしてあれば候補版も指します。
`--from` には、次の並びのディレクトリか URL の接頭辞を渡します。

```
<接頭辞>/SHA256SUMS                                        sha256sum の形（<hash>  <名前>。dist.sh sums が作る）
<接頭辞>/looptrack_<版>_linux_<amd64|arm64>_server.tar.gz  書庫（GitHub Releases の形。dist.sh archive が作る）
<接頭辞>/looptrack_<版>_linux_<amd64|arm64>                素の実行ファイル（dist.sh build の出力・サーバの配布ディレクトリ・1.0.0-rc.2 までの Releases）
<接頭辞>/SHA256SUMS.minisig                                SHA256SUMS の minisign の署名（公式の配布物にある。dist.sh sign-sums が作る）
```

- `uname -m` で amd64 / arm64 を選び、SHA256SUMS から `looptrack_*_linux_<arch>_server.tar.gz` と `looptrack_*_linux_<arch>` の行を取ります。
  **同じ版に書庫があれば書庫を取ります**（公式の SHA256SUMS は書庫の中の実行ファイルを従来の名前にした行も持ちますが、その名前のファイルは Releases に並んでいません）。
  書庫が無ければ素の実行ファイルを取ります（手元の配布物・古いリリースを入れられるように）。複数の版があれば `--version` の指定を求めます。
- 取ったファイルの SHA-256 が合わなければ何も入れ替えません。**書庫は照合してから展開し**、中の `<書庫の名前>/looptrack` だけを取り出します。
  取り出した looptrack が SHA256SUMS の `looptrack_<版>_linux_<arch>` の行と違うときも何も入れ替えません。`looptrack version` が名前の版と違えば注意を出します。
- 接頭辞にはローカルのディレクトリか `https://` の URL を使えます。
  `http://` では改ざんを防げません。そのため `127.0.0.1`・`localhost` 以外では `LOOPTRACK_INSTALL_ALLOW_HTTP=1` を求めます。
- SHA256SUMS の署名: 取得元に `SHA256SUMS.minisig` があり、サーバに `minisign` コマンドがあれば署名を確かめます（Debian・Ubuntu は `apt-get install -y minisign`、AlmaLinux などは EPEL から `dnf install -y epel-release && dnf install -y minisign` で入ります）。
  使う鍵は install.sh に埋め込んだ Looptrack の公開鍵で、鍵 ID は 29D707D7EBFF246B です。`deploy/release/minisign.pub` と同じもので、looptrack 本体とも同じです。
  署名が合わなければ何も入れ替えません。
  署名や minisign が無いときは、注意を出して SHA-256 の照合だけで進みます。止めたいときは `--require-signature` か `LOOPTRACK_INSTALL_REQUIRE_SIGNATURE=1` を指定してください。
  自分で署名した配布物なら `LOOPTRACK_INSTALL_MINISIGN_PUBKEY=<公開鍵>` で鍵を差し替えます。
  既定をこうした理由: minisign は Ubuntu・Debian の標準のパッケージにありますが、既定では入っていません。
  必須にすると「まっさらなサーバで 1 回で入る」が崩れるので、既定は「あれば確かめる」にしました。必須にするかは利用者が選べます。
- 署名の無い取得元（自分で `dist.sh build` した配布物など）では、SHA256SUMS も取得元と同じ経路から来ます。防げるのは壊れたファイルまでです。
  取得元を信用できないときは、`--sha256` で別経路の値を固定してください。
- 自分でビルドするときは、手元で `dist.sh build` と `dist.sh sums` を実行します。その出力のディレクトリをサーバに送って `--from` に渡してください。
  出力には install.sh 自身も入っているので、そのディレクトリから実行できます（`sudo sh /path/to/dist/install.sh --from /path/to/dist`）。
  動いているサーバの配布口（`/api/v1/dist`）は認証が要るので、取得元には使いません。
- install.sh が取りに行くのは `SHA256SUMS`・書庫か実行ファイル・`SHA256SUMS.minisig` だけです。GitHub Releases には install.sh も grants.sql も置いていません。
- Linux 専用です。ほかの OS では、何かを尋ねたり変えたりする前に「Linux 専用です（この OS: …）」で止まります。macOS・Windows でサーバを動かすときはデスクトップ版を使ってください。

### 動かし方（systemd と compose）

どちらも待ち受けは **127.0.0.1 だけ**で、外からは前段のリバースプロキシで受けます。setup の問い・`.env` の中身・案内も同じです。

| | systemd | Docker compose |
| -- | -- | -- |
| 実行ファイル | `/usr/local/bin/looptrack` | 同じ（setup と管理用）。コンテナは取得した実行ファイルを scratch に載せたイメージ `looptrack:<版>`（`looptrack:latest`）をその場で作る |
| 設定 | `/etc/looptrack/.env`（root 600。systemd の `EnvironmentFile` が読む。サービスの利用者はファイルを読めない） | `<dir>/.env`（600）と setup が書く `<dir>/compose.yaml` |
| データ（SQLite） | `/var/lib/looptrack/im.db`（利用者 `looptrack`・ディレクトリ 0750・ファイル 0600） | `<dir>/data/im.db`（uid 65534・0600。コンテナの `/data`） |
| 動く利用者 | 専用のシステム利用者 `looptrack`（ログイン不可） | 65534（`read_only`・`cap_drop: ALL`・`no-new-privileges`。上の「コンテナ」と同じ） |
| 前提 | systemd | Docker Engine と compose プラグイン |
| 管理コマンド | `sudo sh -c 'set -a; . /etc/looptrack/.env; exec setpriv --reuid looptrack --regid looptrack --init-groups looptrack user list'` | `cd <dir> && docker compose run --rm --no-deps looptrack user list` |

- **root で動くのはインストーラだけです。** インストーラは利用者・ディレクトリ・実行ファイル・unit を配置し、`looptrack setup` を実行します。
  setup は `/etc/looptrack` に書くので root で動かし、作った SQLite は `looptrack` に渡します。
  常駐の `serve` と `--upgrade` の `migrate` は `looptrack`（compose では 65534）で動きます。
- **SQLite のファイルは本人だけが読めるようにします（0600）。** looptrack serve は DB を 0600 で作ります。本人以外も読めると起動時に警告しますが、自動では直しません。
  install.sh の置いた DB を使うのは `looptrack`（compose は 65534）だけです。そこで install.sh は入れ直しと `--upgrade` のたびに、本体・`-wal`・`-shm` を 0600 に直します。
  以前の版が 0644 で作った DB も、次の `--upgrade` で警告が消えます。`--upgrade` の控えの置き場 `backup-<日時>/` は root 700 です。
- systemd の unit は強めのサンドボックスで動かします。付けているのは次のようなものです。
  `ProtectSystem=strict`（書けるのは `StateDirectory` の `/var/lib/looptrack` だけ）・`ProtectHome`・`PrivateTmp`・
  `PrivateDevices`・`PrivateUsers`・`NoNewPrivileges`・`CapabilityBoundingSet=`（空）・`RestrictAddressFamilies`・`SystemCallFilter=@system-service ~@privileged`・
  `MemoryDenyWriteExecute`。`systemd-analyze security looptrack` の評価は 1.2（OK）です。
  1024 未満のポートは使えませんが、プロキシの後ろなので使う理由もありません。
- setup への渡し方: install.sh は `looptrack setup --dir <設定の置き場> --mode team` に、動かし方に応じた引数を足して呼びます。
  systemd なら `--service systemd --sqlite-path /var/lib/looptrack/im.db`、compose なら `--service compose` です（`--service` の違いは上の「新しく立ち上げる」の表）。
  `.env` は setup が書いたまま使い、install.sh は書き換えません。
  `--dir`・`--mode`・`--service`・`--yes`・`--force` は install.sh が決めるので、`--` の後ろには置けません。setup の最後の「■ 起動」の案内は、install.sh が実行済みです。
- compose のイメージ: ビルド済みのイメージは配っていません。取得して SHA-256 を確かめた実行ファイルから、その場で作ります（`FROM scratch` の数行で deploy/Dockerfile と同じ形）。
  第三者のライセンス文はイメージの `/NOTICE` に入ります。取得元から取るのではなく、入れる実行ファイル自身の `looptrack licenses` の出力を書き出します。なので実行ファイルと必ず同じ版になります。
  ホストの実行ファイルをマウントする形にはしていません。コンテナを読み取り専用・単一ファイルのまま保ち、`--upgrade` の前の版のイメージ `looptrack:<前の版>` を残して戻せるようにするためです。

### 保存先に MySQL を選ぶとき（権限を与える順番）

アプリ用の DB 利用者を最小権限（`deploy/grants.sql`）にする構成では、権限を与える順番が決まっています。
**テーブル単位の `GRANT` は、そのテーブルができてからしか流せません。** DB 単位で広く与えてから取り消す形が取れないので、テーブル単位にしています。
install.sh はこの順番を 1 回の実行の中で進めます。

1. `LOOPTRACK_SETUP_DSN` はアプリ用の利用者（`.env` の `LOOPTRACK_DSN` になる）にします。
   テーブルを作る接続先（`LOOPTRACK_SETUP_MIGRATE_DSN`）は、テーブルを作れる利用者にします（`GRANT ALL ON <DB 名>.*` は DB を作る前に与えておけます）。
   DB とアプリ用の利用者は先に作らなくてかまいません。
2. install.sh を実行します。setup がテーブルを作り、起動の前の「保存先の確認」でアプリ用の利用者が読めるかを試します。
   - **DB がまだ無ければ**、setup は接続の段（migrate の前）でそれに気づき、下の 3 と同じ処理（`looptrack grants apply`）を先に動かします。
     MySQL の管理用の資格情報を端末で尋ね、**DB を作ってよいかを確かめてから**（`--yes` でも確かめます）DB とアプリ用の利用者を作り、テーブルを作って権限を与えます。
     そのあと setup の続き（最初の管理者）に進み、「保存先の確認」はそのまま通ります。管理用の資格情報を尋ねるのはこの 1 回だけです。
     DB が既にあれば、作り直しません（中の表と行はそのまま）。
     **前提は、表を作る利用者（`LOOPTRACK_SETUP_MIGRATE_DSN`）と、その DB 名への `GRANT` を先に用意しておくことです。**
     setup が「DB が無い」と分かるのは、その利用者が DB 名への権限を持っていて `Error 1049`（Unknown database）を受けたときだけです。
     利用者が無ければ `Error 1045`、権限が無ければ `Error 1044` で、従来どおり接続の段で止まります。
     管理用の接続と、アプリ用の利用者で読めることの確認は、この接続先（`LOOPTRACK_SETUP_MIGRATE_DSN`。無ければ `LOOPTRACK_SETUP_DSN`）の宛先で行います。
   - 作らないと答えると、DB もアプリ用の利用者も作らず、`.env` も書かずに止まり、自分で流す文（`CREATE DATABASE <DB 名> CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`）を示します。
     流してからもう一度実行すると、setup から進みます。端末が無い（自動化の）ときは尋ねられないので、同じ文を示して止まります。DB を先に作っておいてください。
3. 読めなければ、install.sh が `looptrack grants apply` を動かし、**MySQL の管理用の資格情報（既定の利用者 root）を端末で尋ねます**。
   パスワードは表示せず、接続にだけ使い、ファイル・`.env`・`install.conf`・ログに残しません（`curl … | sh` でも `/dev/tty` から読みます）。
   - アプリ用の利用者が無ければ、確かめてから `LOOPTRACK_DSN` のパスワードで作ります（`--yes` なら確かめずに作ります）。DB が無ければ DB も同じように作ります。
   - 権限の中身は looptrack の実行ファイルに埋め込んだ `deploy/grants.sql` です。DB 名と利用者名は `LOOPTRACK_DSN` のものに置き換えます（`im`・`im_app` でなくてよい）。
     照合済みの実行ファイルから出るので、取得の鎖の外にある別のファイルを信用しなくて済みます。流す GRANT 文は `looptrack grants print` で見られます。
   - 流した後で、アプリ用の利用者で読めることを確かめてから起動と動作確認に進みます。
4. 管理用の資格情報が合わないときは、権限を与えず、サービスを起動せずに、何が合わなかったか（接続先・利用者・MySQL のエラー）を出して止まります。
   `.env` とテーブルは残るので、もう一度実行すると setup を飛ばし、尋ねるところから続きます。
   DB が無いときの 2 で合わなかったときは `.env` を書かずに止まるので、もう一度実行すると setup から進みます。

端末が無い（自動化の）ときは、systemd の構成に限り `LOOPTRACK_INSTALL_DB_ADMIN_USER` と `LOOPTRACK_INSTALL_DB_ADMIN_PASSWORD_FILE`（1 行目がパスワードのファイル。使った後は自分で消す）で渡せます。
自分で流すなら、`sudo sh -c 'set -a; . /etc/looptrack/.env; exec looptrack grants print'` で GRANT 文を出し、管理用の資格情報で流してから install.sh をもう一度実行します。
アプリ用の利用者に DB 単位の広い権限を与える構成なら、2 の確認では止まらずに最後まで進みます。
**テーブルが増えた更新でも同じです。** `--upgrade` の `migrate` の後に読めなければ、同じように尋ねて権限を与え直してから起動します。
この確認を入れる前は、権限が無いまま起動していました。`/healthz` が上がらず、60 秒待ってから「ログを見てください」で終わっていたのです。

### TLS はリバースプロキシが持つ

サーバ（looptrack serve）は TLS を持たず、`127.0.0.1:<port>` だけで待ち受けます。
install.sh は nginx と Caddy の設定例を表示し、`/etc/looptrack/proxy-examples.txt` にも置きます。
設定例では接頭辞（`LOOPTRACK_BASE_PATH`）を剥がさずに渡し、`X-Real-IP` を付けます。サーバは既定で 127.0.0.1 と Docker のネットワークからの `X-Real-IP` を信用します。
nginx の設定を手で書くときも `proxy_buffering off;` を入れてください。MCP の購読は SSE で接続を開いたまま通知を流すので、nginx が溜めるとクライアントに届かず、クライアントが待ちきれずに切ります。
サーバは `/mcp` の応答に `X-Accel-Buffering: no` を付けるので、nginx はこの見出しを無視する設定（`proxy_ignore_headers X-Accel-Buffering`）でない限り、`/mcp` の応答を溜めません。
TLS をプロキシに任せる理由は次の 3 つです。

- 証明書の取得・更新（ACME）はプロキシの役目です（Caddy は自動、nginx は certbot）。
  サーバに持たせると、更新の失敗・鍵の置き場・443 番を開くための特権（`CAP_NET_BIND_SERVICE`）を抱えることになります。unit のサンドボックスも弱まります。
- 1 台のサーバで他のサービスと 443 番を分け合うのが普通です。サーバは `LOOPTRACK_BASE_PATH` で接頭辞の下に置けるようにしてあります。
- 平文の待ち受けを外に出さないためです。Cookie は `Secure` で、https の公開 URL を前提にしています。

### 途中で止めた・2 回目

- 一時ファイルは `mktemp -d` の中に置き、`trap` で片付けます（Ctrl-C・TERM は終了コード 130）。
  実行ファイル・unit・`.env` の手直し・印は、一時ファイルに書いてから rename します。
  setup は途中で止めても `.env` も利用者も残しません（上の「新しく立ち上げる」）。**もう一度実行すると続きから進みます。** `.env` があれば setup は飛ばします。
- 最後まで終わると、印として `/etc/looptrack/install.conf`（動かし方・置き場・版）を置きます。
  印があれば、2 回目は「設定済み」と状態・URL・接続設定を示すだけで何も変えません。
  動かし方を変えるときは、先に `--uninstall` してください。
- `--uninstall` は印・実行ファイル・unit（compose はコンテナ）を外します。`.env`（LOOPTRACK_SECRET_KEY）とデータは残るので、そのまま入れ直せば同じ設定とデータで動きます。
  **`--purge` は設定・データ・利用者 `looptrack`（compose は置き場とイメージ）も消します。** 対話では `purge` の入力を求めます。MySQL のデータベースは消しません。

### 更新（--upgrade）

`curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh -s -- --upgrade` で、次の順に進みます
（取得元は入れるときと同じで、`--from` なしなら GitHub Releases の最新・`--version <版>` でその版）。

1. 新しい版を取得して確かめます（書庫は照合してから展開します）。
2. 停止します。
3. SQLite なら、止めた状態で `im.db`（と `-wal`・`-shm`）を `backup-<日時>/` に写します。
4. 実行ファイルを入れ替えます。前の版は `looptrack.prev` に残ります（compose は新しいイメージを作ります）。
5. `migrate` を流します。MySQL でテーブル作成用の利用者を別にするときは `LOOPTRACK_SETUP_MIGRATE_DSN` を使います。
6. 起動して `/healthz` を待ちます。MySQL を最小権限で使っていて、5 の後にアプリ用の利用者が読めなければ、起動の前に管理用の資格情報を尋ねて権限を与え直します（上の「保存先に MySQL を選ぶとき」）。

同じ版なら何もしません。MySQL のバックアップは各自で取ってください（`mysqldump` など）。

**前の版に戻すときは、実行ファイル（`looptrack.prev`・compose の前の版のイメージ）だけでは戻りません。** 手順 5 の migrate が 1 本でも適用していれば、前の版は「新しい版の looptrack で migrate した DB」と出して、migrate も serve も止まります（前の版がこの確かめを持つ版の場合）。古い版が新しい形の DB に書き込んで壊さないためです。前の版で動かすには、DB も手順 3 の控え（SQLite）か各自の控え（MySQL）に戻します。控えの後に書かれたデータは失われるので、戻すより新しい版のまま直すほうを先に考えてください。

### 新しい版の知らせと自動の置き換え（--auto-upgrade）

`looptrack serve` は起動時と 24 時間ごとに GitHub Releases を確かめ、新しい版（署名を確かめ、この OS・CPU のサーバ版の書庫があるもの）を次の 3 か所で知らせます。

- **管理画面の帯**: role が admin の利用者の画面の上部（member には出しません）。新しい版・リリースのページ・更新の 1 行（`curl … | sudo sh -s -- --upgrade`）を出します
- **`looptrack doctor`**: admin のトークンで実行すると、注意の行にサーバの新しい版と更新の 1 行が出ます
- **起動時のログ**: `journalctl -u looptrack`（compose は `docker compose logs`）に、確認の結果の 1 行と、新しい版があれば更新の 1 行が出ます

確認の控えは `/var/lib/looptrack/update-check.json`（compose の SQLite は `data/update-check.json`）です。確認を止めるには `.env` に `LOOPTRACK_UPDATE_CHECK=off` を書いて起動し直します（GitHub へ通信しなくなります）。

**既定では自動で置き換えません。** 知らせを見て、上の `--upgrade` を実行します。systemd で動かしているサーバは、設定で自動の置き換えを有効にできます。

```bash
curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh -s -- --auto-upgrade on    # 有効にする
curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh -s -- --auto-upgrade off   # 外す
systemctl list-timers looptrack-upgrade.timer          # 次に動く時刻
journalctl -u looptrack-upgrade                         # 自動の置き換えの記録
```

- on にすると、1 日 1 回（時刻は 1 時間の幅でずらします。止まっていた間の回は起動した後に 1 回）`looptrack-upgrade.timer` が、
  手元に置いた install.sh の写し `/usr/local/lib/looptrack/install.sh`（root だけが書ける・0755）を `--upgrade --require-signature --yes --only-newer` で動かします。
  手順は上の「更新」と同じです（控え・migrate・再起動・動作確認）。
- **timer は install.sh を取り直しません。** 写しを新しくするのは、人が install.sh を動かしたとき（1 行か手元のファイルで、入れる・`--upgrade`・`--auto-upgrade on`）だけです。
  1 行で動かしたときは同じ URL から取り直したものを、手元のファイルで動かしたときはそのファイルを写します。
- **取るのは書庫だけで、署名を必須にします。** 書庫は SHA256SUMS と minisign の署名で確かめます。有効にするときに `minisign`（Debian・Ubuntu は `apt-get install -y minisign`、AlmaLinux などは EPEL から `dnf install -y epel-release && dnf install -y minisign`）と、書庫・SHA256SUMS・署名を取るための `curl` か `wget` が要ります。
- **版を下げません。** `--only-newer` は、取得した版が入っている版より新しいときだけ置き換えます。
- **サービスを止めたままにしません。** 止めた後に失敗すれば（migrate・権限・起動・`/healthz`・途中のコマンドの失敗・中断）、前の実行ファイル（SQLite は DB も止めた直後の控え）に戻して前の版で起動し直し、
  理由を `journalctl -u looptrack-upgrade` に残して失敗として終わります。次の日の回も同じ理由なら同じく戻ります。
- 取得元は GitHub Releases の最新です（`--from` で入れたサーバでも、自動の置き換えは GitHub Releases から取ります）。
- 設定は `install.conf` の `AUTO_UPGRADE` に残り、`--upgrade` のたびに設定どおりに入れ直します。`--uninstall` で外れます。
- **compose では有効にできません。** コンテナのイメージは自動では置き換えず、知らせるだけです（更新は `--upgrade`）。`--no-start` で入れたサーバも有効にできません。
- **MySQL では、DB の形を変える版を自動では置き換えません。** 止める前に新しい版の `looptrack migrate --check` で未適用の migrate を確かめ、あれば置き換えずに失敗として終わります
  （migrate の後に新しい表の権限を与え直す管理用の資格情報を無人では尋ねられず、MySQL の DB は控えから戻せないため）。サービスは今の版のまま動き、知らせは続きます。
  `journalctl -u looptrack-upgrade` を見て、端末で `--upgrade` を実行してください（有効にするときにも注意を出します）。
- 端末で動かす手動の `--upgrade` は変わりません（権限が足りなければ尋ねて与え直します）。

install.sh で入れたサーバでは `looptrack self-update` は置き換えずにエラーで止まり、インストーラの 1 行の `--upgrade`（`curl … | sudo sh -s -- --upgrade`）を案内します。
1.0.0-rc.1・rc.2 の install.sh で入れたサーバも同じ 1 行で上げられます（置いた `install.conf`・`.env`・unit / `compose.yaml` をそのまま読みます）。
手元に残した古い install.sh を新しいリリースの Releases に `--from` で向けると、素の実行ファイルが Releases に無いので、何も入れ替えずに止まります。
置き換えると migrate も再起動もされず、動いているサーバと版がずれるからです。更新があるかを見る `self-update --check` はそのまま使えます。
ここでいう「install.sh で入れたサーバ」は、linux で `/usr/local/bin/looptrack` から動いていて次のどれかがあるものです。
`/etc/looptrack/install.conf`・`/etc/looptrack/.env`・`/etc/systemd/system/looptrack.service`。

### テスト（deploy/install_test.sh）

```bash
bash deploy/install_test.sh                            # Docker で ubuntu:24.04・ubuntu:22.04・debian:12 + 対話 + systemd の実起動（数分）
INSTALL_TEST_COMPOSE=1 bash deploy/install_test.sh     # compose の実起動も（ホストの Docker のソケットを渡す。im という名前のコンテナがあると省く）
INSTALL_TEST_SYSTEMD=0 INSTALL_TEST_IMAGES=debian:12 bash deploy/install_test.sh
```

材料は `deploy/release/dist.sh` で作った 2 つの版（Docker の CPU の linux/<arch>）です。素の形（`dist.sh build` の出力）は `--from <ディレクトリ>` で、
GitHub Releases と同じ並び（書庫）は `LOOPTRACK_INSTALL_REPO`（`--from` なしのときの取得元のリポジトリ。テスト・ミラー用）で渡します。
README の 1 行は、一時 HTTP サーバに置いた `raw/deploy/install.sh` と `gh/releases/…` に読み替えて通します（`--upgrade` の一部も一時 HTTP サーバの URL から）。
確かめるのは次のことです。

- SHA-256 の不一致・署名・setup の失敗・中断で何も残さない
- 権限と置き場（DB は 0600・警告なし）
- 2 回目は設定済みになる
- 手で serve して `/healthz` が応える
- 外して入れ直すとデータが残る
- `--purge`
- compose の生成
- 疑似端末からの対話
- systemd 入りのコンテナで起動し、サンドボックスを評価する
- ブラウザと同じ手順のログイン（curl でパスワード → 二段階認証の登録（TOTP は openssl で計算）→ 一覧の画面が 200）。
  127.0.0.1 と、公開 URL `https://im.example.com/looptrack` の両方で確かめます。
  公開 URL の前には、install.sh の設定例をそのまま使った Caddy（`local_certs`）と nginx（自己署名の証明書）を置きます。
- コンテナの再起動の後にサービスが自動で上がる
- `--upgrade`（版が変わり、データと控えが残り、0644 の DB を 0600 に直し、同じ二段階認証でログインできる）
- compose の実起動・ログイン・`--upgrade`・`--purge`
- MCP の接続設定が `looptrack setup` の案内と同じ形になる（`X-Looptrack-Project` 付き）。
  文字列の一致は、`go test ./internal/setupwiz/` が install.sh と `setupwiz.MCPConfigs` を突き合わせて確かめます。
- README の 1 行（`curl … | sh`。`--from` なし）で最新のリリースの書庫を取り、起動まで進む
- 書庫: 1 バイト変えた書庫・署名の不一致・中の実行ファイルが SHA256SUMS の行と違う書庫では何も入れない。Linux 以外では何も変えずに止まる
- MySQL（テスト専用の MySQL のコンテナ。最小権限・DB 名 `ltdb`・利用者 `lt_app`）: 管理用の資格情報が合わなければ権限を与えず起動せずに止まり、
  もう一度実行すると疑似端末で尋ねられた資格情報でアプリ用の利用者を作って権限を与え、起動まで 1 回で進む。尋ねたパスワードが設定・データの置き場・
  インストーラの出力・シェルの履歴・`ps` の引数に残らない（対照: `SHOW GRANTS` に grants.sql と同じ権限が出る）。権限の欠けた表がある状態の `--upgrade` も与え直して起動する
- 1.0.0-rc.2 の install.sh（開発側のタグ `v1.0.0-rc.2`）で入れたサーバを、新しいインストーラの `--upgrade --version` で上げる
- 自動の置き換え（`--auto-upgrade`）: 既定は off で timer が無い。compose・`--no-start`・systemd なし・minisign なし・curl も wget も無いときの on は何も変えずに止まる（curl・wget は新しい版の書庫と署名を取るため）。
  1 行の on で timer が有効になり、install.sh の写し（root 755）を置く（service は写しを動かす）。timer の service を動かすと新しい版に上がる
  （写しは書き換えない・もう一度動かしても何もしない・古い版の取得元では版を下げない）。人が `--upgrade` を動かすと写しをそろえる。
  off と `--uninstall` で timer と写しが外れる（コンテナの minisign は呼び出しを確かめる偽物）
- 無人の更新（MySQL の最小権限）: 新しい版に未適用の migrate があれば止めずに置き換えない。止めた後に失敗（権限の欠けた表）すれば前の版に戻して起動し直し、
  0 でない終了コードで終わる。対照: 権限がそろっていれば置き換わる
- 無人の更新の戻し（SQLite）: 止めた後に、新しい版が起動しない・`daemon-reload` の失敗・`die` を通らない裸のコマンドの失敗・中断（TERM）の
  どれでも、前の版に 1 回だけ戻して起動し直し、理由を 1 行残して 0 でない終了コードで終わる。対照: 端末のある手動の `--upgrade` は戻さない

コンテナでは代えられないので、次は手で確かめます。
実機（VM）の Ubuntu LTS・Debian で、本物の証明書（ACME）を使うリバースプロキシの後ろから通してください。
認証アプリでの二段階認証の登録 → `looptrack issue login --browser` → `claude mcp add` までを 15 分以内に終えるのが目安です。

## 利用者の登録

**最初の管理者だけ**はサーバ上で作ります。`looptrack setup` と install.sh なら問いの ④ で作られます。
手で作るときは、パスワードを標準入力から渡してください。
このとき、二段階認証（TOTP）を全利用者に必須にするか任意にするかを `--two-factor` で決めます。指定しないと作らずに、指定方法を示して終わります。

```bash
cd /opt/looptrack      # compose の置き場
sudo docker compose run --rm --no-deps looptrack user add <ログイン名> --name "<表示名>" --admin --two-factor required   # パスワードを 2 回入力
#   required: 全員に TOTP の登録を求める（ログイン時に登録画面へ送る）
#   optional: TOTP を登録した人だけログイン時に確認コードを求める。未登録の人は ID とパスワードだけでログインする
```

- 必須のとき: 初回ログインで TOTP（認証アプリ）の登録を求められます。
- 任意のとき: 各自がアカウント設定（`https://example.com/looptrack/account`）から登録・解除できます。登録した人には、ログイン時に確認コードを求めます。
- あとから変えるときは、管理画面 `https://example.com/looptrack/admin/security`（管理者）を使います。必須を外すときはパスワードの再入力が要ります。登録済みなら確認コードも入れ直します。
  サーバ上なら `sudo docker compose run --rm --no-deps looptrack settings two-factor required|optional` でも変えられます（引数なしで現在の設定と変更の記録を表示）。
  任意から必須にすると、TOTP を経ずにログインしているセッションは無効になります。再ログインのときに登録や入力を求めます。
  登録済みの TOTP は、どちらに切り替えても消えません。
  アクセストークン（PAT・OAuth）での CLI / MCP には影響しません。
- 環境変数 `LOOPTRACK_REQUIRE_TOTP` はもう使いません。設定されていれば、起動時に警告を出して無視します。

以後の利用者管理は Web で行います。

- 利用者の追加・役割・無効化・パスワード再設定・TOTP リセット・プロジェクト権限: `https://example.com/looptrack/admin/users`（管理者）
- アクセストークンの発行・失効とパスワード変更: `https://example.com/looptrack/account`（各自）。
  CLI は `looptrack issue login --browser --url https://example.com/looptrack` でブラウザからログインすれば、発行は要りません。
  ブラウザの無い環境では、発行したトークンを `looptrack issue login --url https://example.com/looptrack` で保存します。

管理コマンド（`looptrack user|member|token …`）も引き続き使えます。無期限トークンを発行できるのはこちらだけです。
systemd で入れたときの管理コマンドの呼び方は、上の「動かし方」の表にあります。

## プロジェクトの運用文書とルール（guide が返すもの）

各プロジェクトの運用文書をサーバに登録します（DESIGN §5-2）。
運用文書は `docs/projects/<slug>.md` のような Markdown で、雛形は [../templates/project-rules.md](../templates/project-rules.md) にあります。
直したときも同じコマンドで置き換えます。プロジェクト別ルール（例 `deploy/rules/example.json`）も同じ形で入れます。

```bash
cd /opt/looptrack
sudo docker compose exec -T looptrack /looptrack project guide set <slug> - --source docs/projects/<slug>.md < <slug>.md
sudo docker compose exec -T looptrack /looptrack project rules set <slug> - < <slug>.json
```

## MCP の setup・導入済み通知

取得 URL の券は `LOOPTRACK_SECRET_KEY` で封じます。別の秘密は要りません。
`<接頭辞>/setup/<券>/…` はトークンなしで配布物を返します（券の検査はサーバが行います）。
リバースプロキシが接頭辞の配下をそのまま渡していれば、設定を変える必要はありません。
実物（Claude Code・Codex）での確認手順は [../ADD-PROJECT.md](../ADD-PROJECT.md) §4-2 にあります。

## CLI のブラウザログインと更新トークン

- OAuth のトークン（MCP・CLI とも）には更新トークンが付きます。30 日の期限が来る前に自動で更新します。
- 認可サーバのメタデータの `grant_types_supported` に `refresh_token` が載ります。
  CLI の `login --browser` はこれを見て、載っていない古い版のサーバには「サーバの更新が必要」と出して終えます。
- 確認: `curl -sS https://example.com/looptrack/.well-known/oauth-authorization-server | jq .grant_types_supported`

## 管理者 0 人のときの「セットアップ未完了」

有効な管理者が 1 人もいないサーバは、通常モードでも画面を「セットアップ未完了」にします。API・MCP には 503 を返します（DESIGN.md §3-3）。
**更新の前に、有効な管理者がいることを確かめてください。** `ROLE` が `admin` で `STATE` が `active` の行が 1 つ以上あれば大丈夫です。

```bash
cd /opt/looptrack && sudo docker compose run --rm --no-deps looptrack user list
```

0 人なら `user add … --admin` で管理者を作ります。
`user disable` などで管理者を 0 人にしたときは、サーバを再起動するまで 503 を返す状態には戻りません。

## 確認

```bash
curl -sS -o /dev/null -w '%{http_code}\n' https://example.com/looptrack/healthz                 # 200
curl -sS -o /dev/null -w '%{http_code} %{redirect_url}\n' https://example.com/looptrack/     # 303 → /looptrack/login
```
