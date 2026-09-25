# 公開候補を、まっさらな環境で通す（quickstart の再現）

公開したあと第三者が最初にやることを、ひととおり通す手順です。開発者の手元の設定・タグ・環境変数が無い環境で行います。
**目的は、README と `docs/guide`・`docs/server/DEPLOY.md` だけで最後まで行けるかを確かめることです。**
通らなかった箇所やそれ以外の知識が要った箇所を見つけたら、直しは別のイシューで行います。

リリースの前（版を切る前）と、README・ガイド・`install.sh`・`setup` のどれかを変えたときに実行します。

- 所要: 30〜45 分（イメージの構築を含む）
- 前提: Docker（Linux のコンテナが動くもの）と Go（配布物を作るため）
- 【CI 可】の印がある節は、コンテナの中だけで完結するので e2e に移せます。
  印の無い節には、ホストの Docker・実物の AI の CLI・人の目が要ります。

> **既存の環境に触らないでください。** 作るコンテナ・イメージ・ネットワークの名前は、すべて `ltcheck-` で始めます。
> ほかのコンテナを止めたり消したりしません。本番のサーバも使いません。終わったら「10. 片付け」を必ず実行してください。

## 0. 公開候補の tarball を作る 【CI 可】

`git archive` で作ります。`.gitattributes` の `export-ignore` が効くので、`private/` と開発用の設定は入りません。

```sh
S=$(mktemp -d)
git archive --format=tar --prefix=looptrack/ HEAD -o "$S/looptrack-src.tar"
mkdir "$S/src" && tar xf "$S/looptrack-src.tar" -C "$S/src"
```

期待する結果:

- `tar tf "$S/looptrack-src.tar" | grep -c '^looptrack/private/'` が `0`
- `.claude/`・`CLAUDE.md` が入っていない（`bash deploy/public-scan.sh --only aiconf` と同じ判定）
- 展開した `"$S/src/looptrack"` に `README.md`・`LICENSE`・`deploy/`・`docs/` がある

**以降は、展開したこの木の中だけを使います。** 作業ツリーや手元の設定は参照しません。

## 1. 取得元（GitHub Releases の代わり）を作る 【CI 可】

公開前なので Releases がありません。**代わりに `deploy/release/dist.sh` の出力のディレクトリを「取得元」として使います。**
`install.sh` の `--from` もガイドの `curl` の取得先も、この読み替えで通ります。

```sh
cd "$S/src/looptrack"
RELEASE_TARGETS="linux/amd64 linux/arm64" bash deploy/release/dist.sh build v1.0.0 "$S/dist"
bash deploy/release/dist.sh sums "$S/dist"
bash deploy/release/dist.sh verify "$S/dist"
```

期待する結果（`SHA256SUMS` に 6 行）:

```
NOTICE / OFL-BIZUDGothic.txt / grants.sql / install.sh
looptrack_v1.0.0_linux_amd64 / looptrack_v1.0.0_linux_arm64
```

`install.sh` は 0755、`grants.sql` は 0644 です。`verify` はすべて `OK` になります。
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

箱を起こしたら、まず素性を確かめます。**ここが汚れていると確認の意味がありません。**

```sh
docker run -d --name ltcheck-a --network ltcheck-net -v "$S/dist:/dist:ro" ltcheck-base:v1 sleep infinity
docker exec ltcheck-a sh -c 'env | grep -E "LOOPTRACK|^IM_" || echo "(none) OK"'
docker exec ltcheck-a grep PRETTY_NAME /etc/os-release
```

期待する結果: `(none) OK`・`PRETTY_NAME="Ubuntu 24.04…LTS"`（点リリースの番号は変わってよい）。

## 3. ローカル + SQLite（ウィザード） 【CI 可】

利用者ガイドの「1. Download the binaries」→「2. Set up the server」→「3. Start the server」をなぞります。
`curl` の取得先だけを `/dist` に読み替えてください。

```sh
docker exec -i ltcheck-a sudo -u dev bash -s <<'EOF'
set -eu
VER=v1.0.0; OS=linux; ARCH=arm64; BASE=/dist     # 読み替え: releases/download/$VER
mkdir -p ~/.local/bin; TMP=$(mktemp -d); cd "$TMP"
cp "$BASE/looptrack_${VER}_${OS}_${ARCH}" "$BASE/SHA256SUMS" .
grep -E " looptrack_${VER}_${OS}_${ARCH}\$" SHA256SUMS | sha256sum -c -
install -m 755 "looptrack_${VER}_${OS}_${ARCH}" ~/.local/bin/looptrack
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
- ウィザードは **6 つ**問う（① 使い方 ② 保存先 ③ 待ち受け ④ 最初の管理者 ⑤ 二段階認証 ⑥ 最初のプロジェクト）
- `~/looptrack-server/.env`（600）と `im.db`（600）ができ、「起動」「ブラウザで開く URL」
  「CLI のログイン」「MCP の接続設定」が表示される

起動して、ブラウザの代わりに `curl` で見ます。

```sh
docker exec -d ltcheck-a sudo -u dev bash -c \
  'export PATH="$HOME/.local/bin:$PATH"; cd ~/looptrack-server && exec looptrack serve --env-file ./.env >~/serve.log 2>&1'
docker exec ltcheck-a curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8090/looptrack/healthz
docker exec ltcheck-a curl -s http://127.0.0.1:8090/looptrack/api/v1/projects
```

期待する結果: `healthz` が 200、`/`・`/p/demo/`・`/account` が 200（**ローカル利用はログイン画面を出さない**）、
`api/v1/projects` に `"slug":"demo"`。

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
  `.mcp.json`・`.gitignore`・`.claude/.looptrack-kit.json` の 7 つを作り、`verify: 配線 7 件` で終わる
- `doctor` は「問題はありません」（`LOOPTRACK_API_URL` が無い注意は、素のシェルでは出てよい）
- `issue new` が `DEMO-0001` を採番し、`next` が `Todo → In Progress` にして受け入れ条件と検証コマンドを出す
- `summary` が 3 層（いまの周／人の判断待ち／外からの反応）で出る

## 5. サーバ + MySQL（install.sh・systemd・二段階認証は必須） 【CI 可】

`docs/server/DEPLOY.md`「保存先に MySQL を選ぶとき（grants.sql を流す順番）」のとおりに進めます。
**一度必ず止まる**ところまでが確認の対象です。

```sh
docker run -d --name ltcheck-mysql --network ltcheck-net \
  -e MYSQL_ROOT_PASSWORD=qscheckroot -e MYSQL_DATABASE=im mysql:8.4
docker run -d --name ltcheck-b --network ltcheck-net \
  --privileged --cgroupns=host -v /sys/fs/cgroup:/sys/fs/cgroup:rw --tmpfs /run --tmpfs /run/lock \
  -v "$S/dist:/dist:ro" ltcheck-systemd:v1
# 1. DB とアプリ用の利用者を作る（権限はまだ与えない）
docker exec -i ltcheck-mysql mysql -uroot -pqscheckroot <<'SQL'
CREATE DATABASE IF NOT EXISTS im CHARACTER SET utf8mb4;
CREATE USER IF NOT EXISTS 'im_app'@'%' IDENTIFIED BY 'qscheckapp';
SQL
# 2. install.sh（ここで止まるのが正しい）
docker exec -i ltcheck-b bash -s <<'EOF'
printf '%s\n' 'Quickstart-2026-pass' > /root/pw && chmod 600 /root/pw
export LOOPTRACK_SETUP_DSN='im_app:qscheckapp@tcp(ltcheck-mysql:3306)/im?parseTime=true'
export LOOPTRACK_SETUP_MIGRATE_DSN='root:qscheckroot@tcp(ltcheck-mysql:3306)/im?parseTime=true'
sh /dist/install.sh --from /dist --yes --method systemd -- \
  --store mysql --public-url https://im.example.test \
  --admin-login admin --admin-password-file /root/pw --two-factor required \
  --project demo --project-prefix DEMO --project-name Demo
EOF
```

期待する結果（**終了コード 1 で止まる**）:

```
==> 保存先の確認（サービスと同じ利用者で読めるか）
エラー: Error 1044 (42000): Access denied for user 'im_app'@'%' to database 'im'
install.sh: エラー: MySQL の保存先をアプリ用の利用者で読めません（…）。サービスは起動していません。
… (1) 表への権限がまだ無い（最小権限（grants.sql）の構成）。… 1. setup が表を作る 2. grants.sql を流す 3. もう一度
```

`.env` とテーブルはここまででできています（作り直しは要りません）。続けて 3・4 を実行します。

```sh
docker exec -i ltcheck-b bash -s <<'EOF'
cp /dist/grants.sql /root/          # 読み替え: curl -fsSL -O "<取得元>/grants.sql"
mysql -h ltcheck-mysql -u root -pqscheckroot im < /root/grants.sql
sh /dist/install.sh --from /dist --yes --method systemd
EOF
```

期待する結果:

- 2 回目は最後まで進み、`systemctl is-active looptrack` が `active`・`is-enabled` が `enabled`
- `/etc/looptrack/.env` が 600、`/looptrack/healthz` が 200
- **3 回目**を実行すると「設定済みです（/etc/looptrack/install.conf）。何も変えていません。」

### リバースプロキシ（設定例をそのまま使う）

`/etc/looptrack/proxy-examples.txt` の nginx の節を取り出し、**証明書の 2 行だけ**を自己署名のものに替えます。
公開の CA はコンテナでは使えないからで、ここが実機との差です。

```sh
docker exec ltcheck-b bash -c '
  openssl req -x509 -newkey rsa:2048 -nodes -days 30 -keyout /etc/ssl/ltcheck/key.pem \
    -out /etc/ssl/ltcheck/cert.pem -subj "/CN=im.example.test" -addext "subjectAltName=DNS:im.example.test"
  # proxy-examples.txt の nginx の節を sites-available/looptrack へ写し、ssl_certificate の 2 行を上に向ける
  nginx -t && systemctl restart nginx'
docker exec ltcheck-b curl -sk -o /dev/null -w '%{http_code}\n' https://im.example.test/looptrack/healthz
```

期待する結果: `nginx -t` が成功、`https` 越しの `healthz` が 200、`/looptrack/` が 303 で `/looptrack/login` へ。

## 6. ブラウザ相当（curl）のログインとプロジェクト作成

二段階認証が**必須**の構成（5 の続き）で確かめます。`oathtool` は確認する側の道具で、実際には認証アプリがこの役をします。

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

- パスワードだけの POST は **303 で `/login/totp/setup` へ**（必須の構成なので一覧には入れない）
- 登録の画面に QR（`data:image/png;base64` の `<img class="qr">`）と手入力用の 32 文字が出る
- 確認コードを入れると 303 で `/looptrack/`、以後 `/`・`/admin/users`・`/admin/projects` が 200

画面からプロジェクトを作ります（`/admin/projects` の「プロジェクトを作る」）。

```
POST /looptrack/admin/projects  csrf=… slug=shop prefix=SHOP name=Shop
→ 303 /looptrack/p/shop/、一覧に「Shop shop ・ SHOP-nnnn ・ 自分は admin で参加」
→ api/v1/projects に demo と shop の 2 件
```

> **秘密を出力に出さないでください。** TOTP のシークレット・アクセストークン・`LOOPTRACK_SECRET_KEY` は、
> 長さや有無だけを表示してファイル（600）に落とします。過去に使い捨てのトークンを出力に出したことがあります。

### 二段階認証が「任意」の構成

別の箱で `--two-factor optional` にして入れ、同じ `curl` の手順を踏みます。

期待する結果: パスワードだけの POST が **303 で `/looptrack/` へ**（確認コードを求めない）。
`/account` に「未登録です。このサーバでは二段階認証は任意です。」と「二段階認証を登録する」が出る。

## 7. 実物の AI（Claude Code）から MCP

ホスト側の本物の `claude` を使います。**`~/.claude.json`・`~/.claude/settings.json` は変えません。**
そのため `--strict-mcp-config` を必ず付けます。こうすると MCP サーバが利用者の設定に登録されません。

> **ホストの資格情報を読まない・表示しないでください。** キーチェーン（`security find-generic-password`）・
> `~/.claude/.credentials.json`・`~/.config/looptrack/credentials.json` など、手元の認証情報には触れません。
> この確認に要るのは、上でサーバに発行した使い捨てのトークンだけです。それは `$S/token.txt`（600）に置きます。
> **この節を人や AI に頼むときは、この禁止を指示にそのまま書いてください。** 「`claude` がどこに
> 認証情報を置くか」を調べる過程でその中身を出力に残す事故が、過去に 2 回起きています。

コンテナの中のサーバは `127.0.0.1` だけで待ち受けます。
ホストから届かせるには**文書どおり前段にリバースプロキシを置き、その口だけを公開します**（`-p 127.0.0.1:18090:80`）。

アクセストークンは `/account` の「アクセストークン」で発行します（`name` と `days` の 2 つが要ります）。

```sh
cat > "$S/mcp.json" <<JSON
{ "mcpServers": { "looptrack": { "type": "http",
    "url": "http://127.0.0.1:18090/looptrack/mcp",
    "headers": { "X-Looptrack-Project": "demo", "Authorization": "Bearer $(cat "$S/token.txt")" } } } }
JSON
chmod 600 "$S/mcp.json"
cd "$S/mcp-work" && claude -p --mcp-config "$S/mcp.json" --strict-mcp-config \
  --allowedTools "mcp__looptrack__setup,mcp__looptrack__guide,mcp__looptrack__next,mcp__looptrack__create_issue" \
  "setup を workspace=<この作業ディレクトリ> で 1 回呼び、guide、next を呼んで、要点だけをまとめてください"
```

期待する結果:

- 認証なしの `POST /looptrack/mcp` は 401 と
  `WWW-Authenticate: Bearer realm="im", resource_metadata="…/.well-known/oauth-protected-resource/looptrack/mcp"`
- トークンありの `initialize` が通り、`tools/list` に `setup`・`guide`・`next`・`create_issue` ほか 24 本
- `claude -p` が `setup`（プロジェクト `demo`・loop を入れるかの問い）→ `guide`（権限 admin・ID は `DEMO-0001` 形式）
  → `next`（着手できるイシューが無ければ `create_issue` の後に `Todo → In Progress`）を返す
- 実行の前後で `~/.claude/settings.json` の md5 が変わらず、`~/.claude.json` に `mcpServers` の追加も
  この作業ディレクトリの `projects` の項目も増えない

## 8. サーバ + compose（README の入口 3） 【CI 可】

README の入口 3「Docker があるところで — compose」を、**そのイメージが 1 つも無い状態から**なぞります。
`looptrack setup` が書く `compose.yaml` は隣の `Dockerfile` からイメージを作るので、レジストリからは何も取りません。
他人が同じ名前のイメージを公開していても引きません。

ここだけは**まっさらなコンテナの中ではなくホストで**行います。中で docker を動かす必要があるからです。
手元の `looptrack:latest` を上書きしないよう、`LOOPTRACK_IMAGE` と `-p` で名前を分けます。

```sh
D=$(mktemp -d); PW=$(mktemp); printf 'correcthorsebattery123\n' >"$PW"
# 1. ウィザード（チームのサーバ・SQLite・compose）
looptrack setup --dir "$D" --yes --mode team --store sqlite --port 18390 \
  --base-path /looptrack --public-url http://127.0.0.1:18390 \
  --admin-login admin --admin-name Admin --admin-password-file "$PW" \
  --two-factor optional --project demo
ls "$D"        # .env  Dockerfile  NOTICE  compose.yaml （linux なら looptrack も）

# 2. linux 以外で setup した場合は、配布物と同じ形の linux/amd64 を自分で置く
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o "$D/looptrack" ./cmd/looptrack
chmod +x "$D/looptrack"; chmod 777 "$D/data"

# 3. 起動（イメージはここで作られる）
cd "$D" && LOOPTRACK_IMAGE=ltcheck-compose:latest docker compose -p ltcheck-compose up -d
curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18390/looptrack/healthz
docker inspect looptrack --format '{{.State.Health.Status}} restarts={{.RestartCount}}'
```

期待する結果: 3 で `Built` と出てイメージが手元で作られる（`Pull` ではない）、`healthz` が 200。

`docker inspect` の健全性は **`healthy` になるのが正しい状態です**。`unhealthy` のまま `restarts` が増えるときは、
`compose.yaml` の `mem_limit` が足りていません（サーバと healthcheck の 2 プロセスが 1 つの枠に入らない）。

片付け: `docker compose -p ltcheck-compose down -v && docker rmi ltcheck-compose:latest && rm -rf "$D" "$PW"`

## 9. コンテナでは代えられないもの（限界）

この手順で確かめられないものは、実機での確認として別に行います。

| 代えたもの | 実物 |
| -- | -- |
| `--from /dist`（`dist.sh` の出力） | GitHub Releases の URL の接頭辞 |
| 自己署名の証明書 | 公開の CA の証明書（`certbot`・Caddy の自動取得） |
| `oathtool` で作った確認コード | 認証アプリ（QR の読み取り） |
| ホストの `claude -p` + トークン | 別の PC からの `claude mcp add` の OAuth・`issue login --browser` |
| 署名なしの `SHA256SUMS` | `SHA256SUMS.minisig`（`--require-signature` の経路） |
| Ubuntu のコンテナ | まっさらな Windows・macOS のデスクトップ版 |

## 10. 片付け

```sh
docker rm -f ltcheck-a ltcheck-b ltcheck-mysql
docker rmi ltcheck-base:v1 ltcheck-systemd:v1
docker network rm ltcheck-net
rm -rf "$S"          # tarball・配布物・トークンの控え
```

`docker ps -a` で `ltcheck-` のものが無いこと、**それ以外のコンテナが動いたままであること**を確かめます。
サーバに作った使い捨てのアクセストークンはコンテナごと消えるので、失効の操作は要りません。
本番のサーバに作った場合は `/account` で失効させてください。
