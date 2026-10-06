# 公開候補を、まっさらな環境で通す（quickstart の再現）

公開したあと第三者が最初にやることをひととおり通す手順です。開発者の手元の設定・タグ・環境変数が無い環境で行います。
目的は README と `docs/guide`・`docs/server/DEPLOY.md` だけで最後まで行けるかを確かめることです。
通らなかった箇所やそれ以外の知識が要った箇所を見つけたら、直しは別のイシューで行います。

リリースの前（版を切る前）と、README・ガイド・`install.sh`・`setup` のどれかを変えたときに実行します。

- 所要: 30〜45 分（イメージの構築を含む）
- 前提: Docker（Linux のコンテナが動くもの）と Go（配布物を作るため）
- 【CI 可】の印がある節はコンテナの中だけで完結するので e2e に移せます。
  印の無い節にはホストの Docker・実物の AI の CLI・人の目が要ります。

> **既存の環境に触らないでください。** 作るコンテナ・イメージ・ネットワークの名前は、すべて `ltcheck-` で始めます。
> ほかのコンテナを止めたり消したりしません。本番のサーバも使いません。終わったら「10. 片付け」を必ず実行してください。

## 0. 公開候補の tarball を作る 【CI 可】

`git archive` で作ります。`.gitattributes` の `export-ignore` が効くので `private/` と開発用の設定は入りません。

```sh
S=$(mktemp -d)
git archive --format=tar --prefix=looptrack/ HEAD -o "$S/looptrack-src.tar"
mkdir "$S/src" && tar xf "$S/looptrack-src.tar" -C "$S/src"
```

期待する結果:

- `tar tf "$S/looptrack-src.tar" | grep -c '^looptrack/private/'` が `0`
- `.claude/`・`CLAUDE.md` が入っていない（`bash deploy/public-scan.sh --only aiconf` と同じ判定）
- 展開した `"$S/src/looptrack"` に `README.md`・`LICENSE`・`deploy/`・`docs/` がある

**以降は展開したこの木の中だけを使います。** 作業ツリーや手元の設定は参照しません。

## 1. 取得元（GitHub Releases の代わり）を作る 【CI 可】

公開前なので Releases がありません。**代わりに `deploy/release/dist.sh` の出力のディレクトリを「取得元」として使います。**
取得元は 2 つ作ります。`$S/release` は GitHub Releases と同じ形（書庫）で、ガイドの `curl` の取得先はこれに読み替えます。
`$S/dist` は `dist.sh build` の出力（素の実行ファイル）で、サーバの配布ディレクトリと同じ形です。

```sh
cd "$S/src/looptrack"
export RELEASE_TARGETS="linux/amd64 linux/arm64"
bash deploy/release/dist.sh build v1.0.0 "$S/dist"
bash deploy/release/dist.sh sums "$S/dist"
bash deploy/release/dist.sh verify "$S/dist"
# GitHub Releases と同じ形（書庫・NOTICE・OFL-BIZUDGothic.txt・SHA256SUMS）
bash deploy/release/dist.sh archive v1.0.0 "$S/dist" "$S/release"
bash deploy/release/dist.sh check-archives v1.0.0 "$S/release" "$S/dist"
cp "$S/dist/NOTICE" "$S/dist/OFL-BIZUDGothic.txt" "$S/release/"
bash deploy/release/dist.sh sums "$S/release"
bash deploy/release/dist.sh check-release v1.0.0 "$S/release"
bash deploy/release/dist.sh verify "$S/release"
unset RELEASE_TARGETS
```

期待する結果（`$S/dist/SHA256SUMS` に 6 行・`$S/release/SHA256SUMS` に 6 行）:

```
$S/dist:    NOTICE / OFL-BIZUDGothic.txt / grants.sql / install.sh
            looptrack_v1.0.0_linux_amd64 / looptrack_v1.0.0_linux_arm64
$S/release: NOTICE / OFL-BIZUDGothic.txt
            looptrack_v1.0.0_linux_amd64_server.tar.gz / looptrack_v1.0.0_linux_arm64_server.tar.gz
            looptrack_v1.0.0_linux_amd64 / looptrack_v1.0.0_linux_arm64（書庫の中の実行ファイル。$S/release には並ばない）
```

`install.sh` は 0755、`grants.sql` は 0644 です。`$S/release` に `install.sh`・`grants.sql`・素の実行ファイルはありません。
`verify` はすべて `OK` になります。書庫の中の実行ファイルの行は「OK（<書庫> の中）」と出ます。
手元では署名しないので、`SHA256SUMS.minisig` が無いという注意は出て構いません。

## 2. まっさらなコンテナを用意する 【CI 可】

**開発者の手元のタグ・`LOOPTRACK_*`・`IM_*`（旧名）の環境変数は渡しません。** 入れるのは git と curl だけです。
`less`・`jq`・`mysql-client`・`nginx`・`openssl`・`oathtool` は、確認する側が使う道具です。

```sh
docker network create ltcheck-net
# 素の箱（ローカル利用の確認）
docker build -t ltcheck-base:v1 -f - . <<'EOF'
FROM ubuntu:24.04
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      git curl ca-certificates less sudo jq && rm -rf /var/lib/apt/lists/*
RUN useradd -m -s /bin/bash dev && echo 'dev ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/dev
EOF
# systemd 入りの箱（install.sh の systemd の経路）
docker build -t ltcheck-systemd:v1 -f - . <<'EOF'
FROM ubuntu:24.04
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      systemd systemd-sysv git curl ca-certificates less sudo jq mysql-client nginx openssl oathtool \
 && rm -rf /var/lib/apt/lists/* \
 && rm -f /lib/systemd/system/multi-user.target.wants/* /etc/systemd/system/*.wants/* \
          /lib/systemd/system/local-fs.target.wants/* /lib/systemd/system/sockets.target.wants/*udev* \
          /lib/systemd/system/basic.target.wants/*
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
EOF
```

箱を起こしたらまず素性を確かめます。ここが汚れていると確認の意味がありません。

```sh
docker run -d --name ltcheck-a --network ltcheck-net -v "$S/dist:/dist:ro" -v "$S/release:/release:ro" ltcheck-base:v1 sleep infinity
docker exec ltcheck-a sh -c 'env | grep -E "LOOPTRACK|^IM_" || echo "(none) OK"'
docker exec ltcheck-a grep PRETTY_NAME /etc/os-release
```

期待する結果: `(none) OK` と `PRETTY_NAME="Ubuntu 24.04…LTS"` が出ます。点リリースの番号は変わってかまいません。

## 3. ローカル + SQLite（ウィザード） 【CI 可】

利用者ガイドのサーバ版の始め方（`docs/guide/server/getting-started.md`）の「1. Download the binaries」→「2. Set up the server」→「3. Start the server」をなぞります。
`curl` の取得先だけを `/release` に読み替えてください。

```sh
docker exec -i ltcheck-a sudo -u dev bash -s <<'EOF'
set -eu
VER=v1.0.0; OS=linux; ARCH=arm64; BASE=/release     # 読み替え: releases/download/$VER
NAME="looptrack_${VER}_${OS}_${ARCH}_server"
mkdir -p ~/.local/bin; TMP=$(mktemp -d); cd "$TMP"
cp "$BASE/$NAME.tar.gz" "$BASE/SHA256SUMS" .
grep -E " $NAME\.tar\.gz\$" SHA256SUMS | sha256sum -c -
tar -xzf "$NAME.tar.gz"
install -m 755 "$NAME/looptrack" ~/.local/bin/looptrack
export PATH="$HOME/.local/bin:$PATH"; looptrack version
mkdir -p ~/looptrack-server && cd ~/looptrack-server
# 対話の答えの順: ①使い方 ②保存先 SQLite の場所 ③ポート 接頭辞
#                 ④ログイン名 表示名 パスワード×2 ⑤二段階認証 ⑥slug prefix 表示名 確認
printf '%s\n' '' '' '' '' '' 'admin' 'Admin' 'Quickstart-2026-pass' 'Quickstart-2026-pass' \
  '2' 'demo' 'DEMO' 'Demo' 'y' | looptrack setup
EOF
```

期待する結果:

- チェックサムが `OK`、`looptrack version` が `v1.0.0 (headless, linux/<arch>)`（既定は英語で、日本語の形で確かめるときは `LOOPTRACK_LANG=ja looptrack version`）
- ウィザードは 6 つ問う（① 使い方 ② 保存先 ③ 待ち受け ④ 最初の管理者 ⑤ 二段階認証 ⑥ 最初のプロジェクト）
- `~/looptrack-server/.env`（600）と `im.db`（600）ができ、「起動」「ブラウザで開く URL」
  「CLI のログイン」「MCP の接続設定」が表示される

起動してブラウザの代わりに `curl` で見ます。

```sh
docker exec -d ltcheck-a sudo -u dev bash -c \
  'export PATH="$HOME/.local/bin:$PATH"; cd ~/looptrack-server && exec looptrack serve --env-file ./.env >~/serve.log 2>&1'
# 待ち受けを始めるまで待つ（最大 30 秒）
for i in $(seq 1 30); do
  docker exec ltcheck-a curl -fs -o /dev/null http://127.0.0.1:8090/looptrack/healthz && break
  sleep 1
done
docker exec ltcheck-a curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8090/looptrack/healthz
docker exec ltcheck-a curl -s http://127.0.0.1:8090/looptrack/api/v1/projects
```

期待する結果: `healthz` が 200 を返し、`/`・`/p/demo/`・`/account` も 200 を返します。ローカル利用ではログイン画面を出しません。
`api/v1/projects` に `"slug":"demo"` が出ます。

## 4. 空のリポジトリへの導入と最初の 1 周 【CI 可】

```sh
docker exec -i ltcheck-a sudo -u dev bash -s <<'EOF'
set -eu
export PATH="$HOME/.local/bin:$PATH"
B=http://127.0.0.1:8090/looptrack
mkdir -p ~/work/demo-app && cd ~/work/demo-app && git init -q
looptrack issue init --project demo --url "$B" --agent claude-code --mcp --dry-run
looptrack issue init --project demo --url "$B" --agent claude-code --mcp --no-loop
looptrack doctor
export LOOPTRACK_API_URL="$B" LOOPTRACK_PROJECT=demo   # 素のシェルでは自分で入れる
looptrack issue config
looptrack issue new "Write an overview in README" --type task
looptrack issue next
looptrack issue summary
EOF
```

期待する結果:

- `--dry-run` は差分だけを出して何も書かない
- 本番は `.claude/settings.json`・`CLAUDE.md`・`.claude/skills/issue/`・`.claude/skills/token-report/`・
  `.mcp.json`・`.gitignore`・`.claude/.looptrack-kit.json` の 7 つを作って `verify: checked <n> wiring(s)` で終わる。
  `<n>` は `grep -c 'looptrack hook' .claude/settings.json` の数と同じ。`doctor` の配線の件数とも一致する
- `doctor` は「問題はありません」（`LOOPTRACK_API_URL` が無い注意は、素のシェルでは出てよい）
- `issue new` が `DEMO-0001` を採番し、`next` が `Todo → In Progress` にして受け入れ条件と検証コマンドを出す
- `summary` が 3 層（いまの周／人の判断待ち／外からの反応）で出る

## 5. サーバ + MySQL（install.sh・systemd・二段階認証は必須） 【CI 可】

`docs/server/DEPLOY.md`「保存先に MySQL を選ぶとき（権限を与える順番）」のとおりに、**README の 1 行を 1 回実行するだけで**起動まで進むことを確かめます。
読み替えは 2 つです。1 行が取る `raw.githubusercontent.com/…/main/deploy/install.sh` は展開した木の `deploy/install.sh` に、
インストーラが既定で取る `https://github.com/howashoji/looptrack/releases/latest/download` は 1 章の `$S/release` にします
（`LOOPTRACK_INSTALL_REPO=/gh` と、`$S/release` を `/gh/releases/latest/download` に入れる）。

```sh
docker run -d --name ltcheck-mysql --network ltcheck-net \
  -e MYSQL_ROOT_PASSWORD=qscheckroot mysql:8.4
docker run -d --name ltcheck-b --network ltcheck-net -p 127.0.0.1:18391:80 \
  --privileged --cgroupns=host -v /sys/fs/cgroup:/sys/fs/cgroup:rw --tmpfs /run --tmpfs /run/lock \
  -v "$S/src/looptrack/deploy/install.sh:/raw/install.sh:ro" -v "$S/release:/gh/releases/latest/download:ro" \
  ltcheck-systemd:v1
# -p は 7 章で使う口（前段の nginx の 80 番）。デスクトップ版の既定の 18090 とはぶつからない番号にする
# MySQL がネットワーク越しに受け付けるまで待つ（最大 2 分。起動の途中は一時的なサーバが動いている）
for i in $(seq 1 60); do
  docker exec ltcheck-b mysql -h ltcheck-mysql -uroot -pqscheckroot -e 'SELECT 1' >/dev/null 2>&1 && break
  sleep 2
done
# 1. 人が先に用意するのは、表を作る利用者だけ（DB im とアプリ用の利用者 im_app はインストーラが作る。
#    GRANT ALL ON im.* は DB を作る前に流せる）
docker exec -i ltcheck-mysql mysql -uroot -pqscheckroot <<'SQL'
CREATE USER IF NOT EXISTS 'im_migrate'@'%' IDENTIFIED BY 'qscheckmig';
GRANT ALL ON im.* TO 'im_migrate'@'%';
SQL
# 2. README の 1 行（読み替え: curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh -s -- …）。
#    端末で尋ねられたら、管理用の利用者に root、パスワードに qscheckroot を答え（表示されない）、
#    DB im とアプリ用の利用者 im_app を作るかの確認には y を答える
docker exec -it ltcheck-b bash -c '
export LOOPTRACK_LANG=ja      # looptrack の文面を下の期待する結果と同じ日本語にする（既定は英語）
printf "%s\n" Quickstart-2026-pass > /root/pw && chmod 600 /root/pw
export LOOPTRACK_SETUP_DSN="im_app:qscheckapp@tcp(ltcheck-mysql:3306)/im?parseTime=true"
export LOOPTRACK_SETUP_MIGRATE_DSN="im_migrate:qscheckmig@tcp(ltcheck-mysql:3306)/im?parseTime=true"
cat /raw/install.sh | LOOPTRACK_INSTALL_REPO=/gh sh -s -- --yes --method systemd -- \
  --store mysql --public-url https://im.example.test \
  --admin-login admin --admin-password-file /root/pw --two-factor required \
  --project demo --project-prefix DEMO --project-name Demo'
```

期待する結果（1 回の実行で最後まで進む）:

```
==> 実行ファイルを取得します（/gh/releases/latest/download・linux/<arch>）
  looptrack_v1.0.0_linux_<arch>_server.tar.gz（SHA-256 一致: …）
==> 設定（looptrack setup）
保存先に接続しています…
  DB がまだありません（Error 1049 (42000): Unknown database 'im'）。管理用の資格情報を尋ね、確かめてから DB とアプリ用の利用者を作り、表を作って権限を与えます（looptrack grants apply と同じ）
MySQL の権限を与えます（接続先 ltcheck-mysql:3306・DB im・アプリ用の利用者 im_app）
管理用の利用者（DB と利用者を作り、権限を与えられるもの） [root]: root
root のパスワード（表示しません。保存しません）:
DB im がありません。作りますか (y/n) [y]: y
  作成: DB im
アプリ用の利用者 im_app がありません。LOOPTRACK_DSN のパスワードで作りますか (y/n) [y]: y
  作成: 利用者 im_app
  表がまだありません。管理用の資格情報で作ります（migrate）
適用: 0001_init.sql
…
  権限を与えました（GRANT <n> 件。deploy/grants.sql と同じ）
  アプリ用の利用者 im_app で読めました
スキーマを最新にしています（マイグレーション）…
  最新です
最初の管理者を作っています…
…
==> 保存先の確認（サービスと同じ利用者で読めるか）
  読めました
==> 動作確認（/looptrack/healthz）
  200 OK
インストールが終わりました（systemd・v1.0.0）。
```

- `systemctl is-active looptrack` が `active`・`is-enabled` が `enabled`
- `/etc/looptrack/.env` が 600 で `/looptrack/healthz` が 200
- `GRANT <n> 件` の `<n>` が展開した木で数えた `grep -c '^GRANT' deploy/grants.sql` と同じ
- `docker exec ltcheck-mysql mysql -uroot -pqscheckroot -e "SHOW GRANTS FOR 'im_app'@'%'"` に `deploy/grants.sql` と同じ表ごとの権限が出る
- `grep -rF qscheckroot /etc/looptrack /var/lib/looptrack` が何も出さない（管理用のパスワードを残さない）
- 2 回目を実行すると「設定済みです（/etc/looptrack/install.conf）。何も変えていません。」
- 管理用の資格情報を尋ねるのは 1 回だけ（「保存先の確認」では尋ねない）
- 管理用のパスワードを間違えると `管理用の資格情報で ltcheck-mysql:3306 に接続できません（利用者 root）` と出て起動せずに止まる。
  `.env` は書かない。もう一度実行すると setup から進む
- この手順は DB が無い経路を通ります。「DB があり、アプリ用の利用者だけが無い」経路は `deploy/install_test.sh` の `mysql-bad`・`mysql-good` が通しています
- DB を作るかの確認に `n` と答えると `DB im を作らずに止めました` と自分で流す `CREATE DATABASE im CHARACTER SET utf8mb4 COLLATE utf8mb4_bin` が出る。
  DB も `im_app` も作らずに止まる

### リバースプロキシ（設定例をそのまま使う）

`/etc/looptrack/proxy-examples.txt` の nginx の節を取り出し、証明書の 2 行だけを自己署名のものに替えます。
公開の CA はコンテナでは使えないからで、ここが実機との差です。

```sh
docker exec ltcheck-b bash -c '
  set -e
  mkdir -p /etc/ssl/ltcheck
  openssl req -x509 -newkey rsa:2048 -nodes -days 30 -keyout /etc/ssl/ltcheck/key.pem \
    -out /etc/ssl/ltcheck/cert.pem -subj "/CN=im.example.test" -addext "subjectAltName=DNS:im.example.test"
  # proxy-examples.txt の nginx の節を sites-available/looptrack へ写し、ssl_certificate の 2 行を上に向ける
  sed -n "/^# ---- nginx/,/^# ---- Caddy/p" /etc/looptrack/proxy-examples.txt | sed "\$d" \
    | sed -E -e "s|^ *# ssl_certificate +.*|    ssl_certificate     /etc/ssl/ltcheck/cert.pem;|" \
             -e "s|^ *# ssl_certificate_key .*|    ssl_certificate_key /etc/ssl/ltcheck/key.pem;|" \
    > /etc/nginx/sites-available/looptrack
  ln -sf /etc/nginx/sites-available/looptrack /etc/nginx/sites-enabled/looptrack
  nginx -t && systemctl restart nginx
  # 公開 URL の名前（im.example.test）を、この箱の中でだけ引けるようにする
  echo "127.0.0.1 im.example.test" >> /etc/hosts'
docker exec ltcheck-b curl -sk -o /dev/null -w '%{http_code}\n' https://im.example.test/looptrack/healthz
```

期待する結果: `nginx -t` が成功して `https` 越しの `healthz` が 200 を返します。`/looptrack/` には 303 が返り、行き先は `/looptrack/login` です。

## 6. ブラウザ相当（curl）のログインとプロジェクト作成

二段階認証が必須の構成（5 の続き）で確かめます。`oathtool` は確認する側の道具で、実際には認証アプリがこの役をします。

```sh
docker exec -i ltcheck-b bash -s <<'EOF'
B=https://im.example.test/looptrack; J=/tmp/cj.txt; rm -f "$J"
csrf() { grep -oE 'name="csrf" value="[^"]+"' "$1" | head -1 | sed -E 's/.*value="([^"]+)".*/\1/'; }
curl -sk -c "$J" -b "$J" -o /tmp/1.html "$B/login"
curl -sk -c "$J" -b "$J" -o /dev/null -w '%{redirect_url}\n' \
  -d "csrf=$(csrf /tmp/1.html)" -d 'login=admin' --data-urlencode 'password=Quickstart-2026-pass' "$B/login"
curl -sk -c "$J" -b "$J" -o /tmp/3.html "$B/login/totp/setup"
SEC=$(sed -E 's/<[^>]*>//g' /tmp/3.html | grep -oE '[A-Z2-7]{32}' | head -1)
curl -sk -c "$J" -b "$J" -o /dev/null -w '%{redirect_url}\n' \
  -d "csrf=$(csrf /tmp/3.html)" -d "code=$(oathtool --totp -b "$SEC")" "$B/login/totp/setup"
curl -sk -b "$J" -c "$J" -o /dev/null -w '%{http_code}\n' "$B/"
EOF
```

期待する結果:

- パスワードだけの POST は 303 で `/login/totp/setup` へ（必須の構成なので一覧には入れない）
- 登録の画面に QR（`data:image/png;base64` の `<img class="qr">`）と手入力用の 32 文字が出る
- 確認コードを入れると 303 で `/looptrack/` へ。以後 `/`・`/admin/users`・`/admin/projects` が 200

画面からプロジェクトを作ります（`/admin/projects` の「プロジェクトを作る」）。

```
POST /looptrack/admin/projects  csrf=… slug=shop prefix=SHOP name=Shop
→ 303 /looptrack/p/shop/、一覧に「Shop shop ・ SHOP-nnnn ・ 自分は admin で参加」
→ api/v1/projects に demo と shop の 2 件
```

> **秘密を出力に出さないでください。** TOTP のシークレット・アクセストークン・`LOOPTRACK_SECRET_KEY` は、
> 長さや有無だけを表示してファイル（600）に落とします。過去に使い捨てのトークンを出力に出したことがあります。

### 二段階認証が「任意」の構成

別の箱で `--two-factor optional` にして入れます。同じ `curl` の手順を踏みます。

期待する結果: 確認コードは求めません。パスワードだけの POST が 303 で `/looptrack/` へ移ります。
`/account` に「未登録です。このサーバでは二段階認証は任意です。」と「二段階認証を登録する」が出ます。

## 7. 実物の AI（Claude Code）から MCP

ホスト側の本物の `claude` を使います。`~/.claude.json`・`~/.claude/settings.json` は一切変えないので `--strict-mcp-config` を必ず付けます。こうすると MCP サーバが利用者の設定に登録されません。

> **ホストの資格情報を読まない・表示しないでください。** キーチェーン（`security find-generic-password`）・
> `~/.claude/.credentials.json`・`~/.config/looptrack/credentials.json` など、手元の認証情報には触れません。
> この確認に要るのは上でサーバに発行した使い捨てのトークンだけです。それは `$S/token.txt`（600）に置きます。
> この節を人や AI に頼むときはこの禁止を必ず指示にそのまま書いてください。「`claude` がどこに
> 認証情報を置くか」を調べる過程でその中身を出力に残す事故が、過去に 2 回起きています。

コンテナの中のサーバが待ち受けるのは `127.0.0.1` だけです。
ホストから届かせるには文書どおり前段にリバースプロキシを置き、その口だけを公開します。
5 章の `docker run` で付けた `-p 127.0.0.1:18391:80` がその口です。
デスクトップ版は既定で `127.0.0.1:18090` を使うので、同じ番号にするとデスクトップ版が動いている端末では始められません。
設定例の nginx が待ち受けるのは 443 番だけです。80 番の口を足し、80 番を先に取っている default の site を外します。

```sh
docker exec ltcheck-b bash -c '
  sed -i "s/^    listen 443 ssl;/&\n    listen 80;/" /etc/nginx/sites-available/looptrack
  rm -f /etc/nginx/sites-enabled/default
  nginx -t && systemctl restart nginx'
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18391/looptrack/healthz
```

アクセストークンは `/account` の「アクセストークン」で発行します（`name` と `days` の 2 つが要ります）。

```sh
cat > "$S/mcp.json" <<JSON
{ "mcpServers": { "looptrack": { "type": "http",
    "url": "http://127.0.0.1:18391/looptrack/mcp",
    "headers": { "X-Looptrack-Project": "demo", "Authorization": "Bearer $(cat "$S/token.txt")" } } } }
JSON
chmod 600 "$S/mcp.json"
mkdir -p "$S/mcp-work" && cd "$S/mcp-work" && claude -p "setup を workspace=$S/mcp-work で 1 回呼び、guide、next を呼んで、要点だけをまとめてください。next で着手できるイシューが無ければ、create_issue で 1 件起票してから next をもう一度呼んでください" \
  --mcp-config "$S/mcp.json" --strict-mcp-config \
  --allowedTools "mcp__looptrack__setup,mcp__looptrack__guide,mcp__looptrack__next,mcp__looptrack__create_issue" \
  </dev/null
```

プロンプトは `-p` の直後に置きます。`--mcp-config` と `--allowedTools` は値をいくつでも取るので、後ろに置くとプロンプトまで値として読まれます。
そのときの出力は `Error: Input must be provided either through stdin or as a prompt argument when using --print` の 1 行だけです。

期待する結果:

- 認証なしの `POST /looptrack/mcp` は 401 と
  `WWW-Authenticate: Bearer realm="im", resource_metadata="…/.well-known/oauth-protected-resource/looptrack/mcp"`
- トークンありの `initialize` が通る。`tools/list` に `setup`・`guide`・`next`・`create_issue` ほか 24 本
- `claude -p` が `setup`（プロジェクト `demo`・loop を入れるかの問い）→ `guide`（権限 admin・ID は `DEMO-0001` 形式）
  → `next`（着手できるイシューが無ければ `create_issue` の後に `Todo → In Progress`）を返す
- 実行の前後で `~/.claude/settings.json` の md5 が変わらず、`~/.claude.json` に `mcpServers` の追加も
  この作業ディレクトリの `projects` の項目も増えない

## 8. サーバ + compose（README の入口 3） 【CI 可】

README の入口 3「Docker があるところで」の compose の手順を、そのイメージが 1 つも無い状態からなぞります。
`looptrack setup` が書く `compose.yaml` は `pull_policy: build` を持ちます。隣の `Dockerfile` からイメージを作ります。
レジストリには取りに行きません。他人が同じ名前のイメージを公開していても引きません。

ここだけは**まっさらなコンテナの中ではなくホストで**行います。中で docker を動かす必要があるからです。
手元の `looptrack:latest` を上書きしないよう `LOOPTRACK_IMAGE` と `-p` で名前を分けます。
ウィザードは展開した木から作った候補の `looptrack` で動かします。手元に入っている別の版の `looptrack` を使うと、確かめたい `compose.yaml` になりません。

```sh
cd "$S/src/looptrack" && go build -o "$S/bin/looptrack" ./cmd/looptrack
D=$(mktemp -d); PW=$(mktemp); printf 'correcthorsebattery123\n' >"$PW"
# 1. ウィザード（チームのサーバ・SQLite・compose）
"$S/bin/looptrack" setup --dir "$D" --yes --mode team --store sqlite --port 18390 \
  --base-path /looptrack --public-url http://127.0.0.1:18390 \
  --admin-login admin --admin-name Admin --admin-password-file "$PW" \
  --two-factor optional --project demo
ls "$D"        # .env  Dockerfile  NOTICE  compose.yaml （linux なら looptrack も）
grep -n 'pull_policy' "$D/compose.yaml"

# 2. linux 以外で setup した場合は、配布物と同じ形の linux/amd64 を自分で置く
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o "$D/looptrack" ./cmd/looptrack
chmod +x "$D/looptrack"; chmod 777 "$D/data"

# 3. 起動（イメージはここで作られる）
cd "$D" && LOOPTRACK_IMAGE=ltcheck-compose:latest docker compose -p ltcheck-compose up -d
# 待ち受けを始めるまで待つ（最大 30 秒）。健全性は最初の確かめが終わるまで starting のまま（最大 2 分）
for i in $(seq 1 30); do curl -fs -o /dev/null http://127.0.0.1:18390/looptrack/healthz && break; sleep 1; done
curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18390/looptrack/healthz
for i in $(seq 1 60); do [ "$(docker inspect looptrack --format '{{.State.Health.Status}}')" != starting ] && break; sleep 2; done
docker inspect looptrack --format '{{.State.Health.Status}} restarts={{.RestartCount}}'
```

期待する結果:

- `compose.yaml` の `build:` の直後に `pull_policy: build` がある
- 3 で `Building` → `Built` と出てイメージが手元で作られ、`Pulling` の行が 1 つも出ない
- `healthz` が 200
- `pull_policy: build` なので `up -d` は `--build` を付けなくても毎回ビルドを走らせる。変わっていない層は使い回す

`docker inspect` の健全性は `healthy` になるのが正しい状態です。`unhealthy` のまま `restarts` が増えるときは、
`compose.yaml` の `mem_limit` が足りていません（サーバと healthcheck の 2 プロセスが 1 つの枠に入らない）。

`docker compose -p ltcheck-compose down -v && docker rmi ltcheck-compose:latest && rm -rf "$D" "$PW"` で片付けます。

## 9. コンテナでは代えられないもの（限界）

この手順で確かめられないものは実機での確認として別に行います。

| 代えたもの | 実物 |
| -- | -- |
| `--from /dist`（`dist.sh build` の出力） | サーバの配布ディレクトリ・手元の配布物 |
| `/release`・`/gh/releases/latest/download`（`dist.sh archive` の出力と NOTICE・OFL・SHA256SUMS） | GitHub Releases の `…/releases/latest/download` |
| `cat /raw/install.sh \| sh -s -- …` | `curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh \| sudo sh -s -- …` |
| 自己署名の証明書 | 公開の CA の証明書（`certbot`・Caddy の自動取得） |
| `oathtool` で作った確認コード | 認証アプリ（QR の読み取り） |
| ホストの `claude -p` + トークン | 別の PC からの `claude mcp add` の OAuth・`issue login --browser` |
| 署名なしの `SHA256SUMS` | `SHA256SUMS.minisig`（`--require-signature` の経路） |
| Ubuntu のコンテナ | まっさらな Windows・macOS のデスクトップ版 |

## 10. 片付け

```sh
docker rm -f -v ltcheck-a ltcheck-b ltcheck-mysql   # -v: MySQL の名前の無いボリュームも消す
docker rmi ltcheck-base:v1 ltcheck-systemd:v1
docker network rm ltcheck-net
rm -rf "$S"          # tarball・配布物・トークンの控え
```

`docker ps -a` で `ltcheck-` のものが無いこと、それ以外のコンテナが動いたままであることを確かめます。
サーバに作った使い捨てのアクセストークンはコンテナごと消えるので、失効の操作は要りません。
本番のサーバに作った場合は `/account` で失効させてください。
