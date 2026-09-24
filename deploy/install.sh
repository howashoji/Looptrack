#!/bin/sh
# looptrack のインストーラ。まっさらな Linux サーバ（Ubuntu LTS・Debian）で
# 実行ファイルの取得 → 設定（looptrack setup）→ 起動 → 動作確認までを 1 回で行う。手順と判断は docs/server/DEPLOY.md「install.sh」。
#
#   sudo sh install.sh --from <ディレクトリ | URL の接頭辞>                      対話（動かし方と setup の問いに答える）
#   sudo sh install.sh --from <…> --yes --method systemd -- <setup の引数>       非対話（setup の引数は -- の後ろ）
#   sudo sh install.sh --upgrade --from <…>                                      実行ファイルを入れ替え、migrate して再起動（データは保つ）
#   sudo sh install.sh --uninstall [--purge]                                     外す（既定はデータを残す。--purge で消す）
#
# 取得元の規約（deploy/release/dist.sh の出力をそのまま読む）:
#   <接頭辞>/SHA256SUMS                          sha256sum の形（<hash>  <名前>）
#   <接頭辞>/looptrack_<版>_linux_<amd64|arm64>    実行ファイル
#   <接頭辞>/SHA256SUMS.minisig                  SHA256SUMS の minisign の署名（公式の配布物にはある）
#   <接頭辞>/install.sh・<接頭辞>/grants.sql      このスクリプト自身と MySQL の最小権限（人が curl で取るもの。
#     このスクリプトは取りに行かないので無くても入る。SHA256SUMS には載るので、取った install.sh を照合できる）
#     minisign コマンドがあれば、下の公開鍵で署名を確かめ、合わなければ何も入れない。minisign が無い・署名が無いときは注意を出して続ける
#     （--require-signature で止める）。公開鍵は looptrack（selfupdate.MinisignPublicKey）・deploy/release/minisign.pub と同じ
# 保存先に MySQL を選び、アプリ用の利用者を最小権限（deploy/grants.sql）にするときは、流す順番が決まっている:
#   DB とアプリ用の利用者を作る → install.sh（setup が表を作る）→ ここで一度止まる → 管理用の資格情報で grants.sql を流す
#   → install.sh をもう一度実行（起動と動作確認だけ）。表ごとの GRANT は表ができてからしか流せないため、この順番になる。
#   起動の前に「保存先の確認」でアプリ用の利用者が読めるかを試し、読めなければ grants.sql の案内を出して止まる
#   （黙って 60 秒待たない）。アプリ用の利用者に DB 単位の広い権限を与える構成では、この確認は素通りして最後まで進む。
# 動かし方:
#   systemd  専用ユーザー looptrack・/usr/local/bin/looptrack・/etc/looptrack/.env・/var/lib/looptrack・サンドボックス付きの unit
#   compose  <dir>（既定 /opt/looptrack）に setup が書く compose.yaml。取得した実行ファイルを scratch に載せたイメージ looptrack:<版> を作る
#            （第三者のライセンス文は looptrack licenses の出力をイメージの /NOTICE に入れる。公式のイメージと同じ）
# どちらも 127.0.0.1 だけで待ち受ける。TLS は前段のリバースプロキシ（nginx・Caddy の設定例を出す）が持つ。
set -eu

PROG=install.sh
CONF_DIR=/etc/looptrack                # systemd の設定（.env）と、どちらの動かし方でも install.conf を置く
STATE=$CONF_DIR/install.conf           # インストールが最後まで終わった印（2 回目は「設定済み」を示して終わる）
BIN=/usr/local/bin/looptrack
DATA_DIR=/var/lib/looptrack            # systemd のデータ（SQLite）
UNIT=/etc/systemd/system/looptrack.service
SVC_USER=looptrack
COMPOSE_DIR_DEFAULT=/opt/looptrack
# compose のイメージ名（setup の compose.yaml は ${LOOPTRACK_IMAGE:-looptrack:latest}）。LOOPTRACK_INSTALL_IMAGE はテスト用（手元の looptrack:latest を上書きしないため）
IMAGE=${LOOPTRACK_INSTALL_IMAGE:-looptrack}
# SHA256SUMS の署名を確かめる minisign の公開鍵（Looptrack 専用・鍵 ID 29D707D7EBFF246B。秘密ではない）。
# LOOPTRACK_INSTALL_MINISIGN_PUBKEY で差し替えられる（自分で署名した配布物・テスト用）
MINISIGN_PUBKEY_DEFAULT=RWRrJP/r1wfXKalGsxLnzFmmsExUd2azSJh4ccrYDJEBu8yE3N0ZlLJy
MINISIGN_PUBKEY=${LOOPTRACK_INSTALL_MINISIGN_PUBKEY:-$MINISIGN_PUBKEY_DEFAULT}
REQUIRE_SIG=${LOOPTRACK_INSTALL_REQUIRE_SIGNATURE:-0}

# 最初のプロジェクトの slug（MCP の接続設定の X-Looptrack-Project に使う）。
# setup の引数（--project）・起動前の確認（check_store_access）・印（install.conf）の順に決まる。
# 分からないときは PROJECT_UNKNOWN を出して、置き換え方を添える
PROJECT=""
PROJECT_UNKNOWN='<プロジェクトの slug>'

ACTION=install
METHOD=""
FROM="${LOOPTRACK_INSTALL_FROM:-}"
WANT_VERSION="${LOOPTRACK_INSTALL_VERSION:-}"
WANT_SHA=""
DIR=""
YES=0
NO_START=0
PURGE=0
TMP=""
LOCAL_MODE=""

# ---------------------------------------------------------------- 共通

say() { printf '%s\n' "$*"; }
step() { printf '\n==> %s\n' "$*"; }
warn() { printf '%s: 注意: %s\n' "$PROG" "$*" >&2; }
die() {
  printf '%s: エラー: %s\n' "$PROG" "$*" >&2
  exit 1
}

cleanup() {
  if [ -n "$TMP" ] && [ -d "$TMP" ]; then
    rm -rf "$TMP"
  fi
}
trap cleanup EXIT
trap 'cleanup; printf "\n%s: 中断しました（一時ファイルは片付けました。もう一度実行すると続きから進みます）\n" "$PROG" >&2; trap - EXIT; exit 130' INT TERM HUP

usage() {
  cat <<'EOF'
使い方: sudo sh install.sh [オプション] [-- <looptrack setup の引数>]

  --from <ディレクトリ | URL の接頭辞>   実行ファイルの取得元（環境変数 LOOPTRACK_INSTALL_FROM でも可）。
                                        <接頭辞>/SHA256SUMS と <接頭辞>/looptrack_<版>_linux_<arch>（deploy/release/dist.sh の出力）
  --version <版>                        取得元に複数の版があるときに選ぶ（LOOPTRACK_INSTALL_VERSION）
  --sha256 <hash>                       実行ファイルの SHA-256 を別経路で固定する（SHA256SUMS と両方で確かめる）
  --require-signature                   SHA256SUMS の署名（SHA256SUMS.minisig）を必ず確かめる（minisign か署名が無ければ止める。
                                        LOOPTRACK_INSTALL_REQUIRE_SIGNATURE=1）。指定しなくても、minisign があれば確かめる
  --method systemd|compose              動かし方（対話では問う。--yes の既定は systemd）
  --dir <ディレクトリ>                  compose の置き場（既定 /opt/looptrack）
  --yes                                 対話しない（setup の答えは -- の後ろに渡す。秘密は環境変数 LOOPTRACK_SETUP_*）
  --no-start                            設定まで行い、サービスを起動しない（compose ではイメージも作らない）
  --upgrade                             実行ファイルを入れ替え、migrate して再起動する（データは保つ）
  --uninstall [--purge]                 外す。既定では設定とデータを残す。--purge で設定・データ・利用者も消す
  -h, --help                            この表示

例（非対話・SQLite・systemd）:
  printf '%s\n' '<管理者のパスワード>' > /root/pw && chmod 600 /root/pw
  sudo sh install.sh --from https://example.com/looptrack/v1.0.0 --yes --method systemd -- \
    --store sqlite --public-url https://im.example.com --admin-login alice \
    --admin-password-file /root/pw --two-factor required
EOF
}

need_root() {
  [ "$(id -u)" -eq 0 ] || die "root で実行してください（sudo sh $PROG …）"
}

# ask <問い> <既定> — 端末から 1 行読む（curl | sh でも /dev/tty から読む）
ask() {
  printf '%s [%s]: ' "$1" "$2" >/dev/tty
  ans=""
  IFS= read -r ans </dev/tty || die "入力が途中で終わりました（何も変えていません）"
  [ -n "$ans" ] || ans=$2
}

# env_get <KEY> <file> — setup が書く .env（KEY='value'）から値を取る
env_get() {
  sed -n "s/^$1=//p" "$2" | tail -n 1 | sed "s/^'\(.*\)'\$/\1/; s/^\"\(.*\)\"\$/\1/"
}

# restrict_sqlite <SQLite のファイル> — 本体・-wal・-shm を本人だけ（0600）にする（looptrack は広い権限を起動時に警告する）。
# install.sh が置いた DB を使うのは looptrack（compose は uid 65534）だけなので、狭めて困る利用者はいない。
# looptrack は 0600 で作るが、以前の版が作った DB（0644）は入れ直し・--upgrade のときにここで直る
restrict_sqlite() {
  for f in "$1" "$1-wal" "$1-shm"; do
    if [ -f "$f" ]; then chmod 0600 "$f"; fi
  done
}

# sqlite_host_path — .env の LOOPTRACK_DSN が SQLite なら、ホストから見たファイルのパスを出す（MySQL なら何も出さない）。
# compose の DSN はコンテナの中のパス（sqlite:/data/<名前>）なので <dir>/data/<名前> に読み替える
sqlite_host_path() {
  d=$(env_get LOOPTRACK_DSN "$DIR/.env")
  case $METHOD:$d in
    compose:sqlite:/data/*) printf '%s\n' "$DIR/data/${d#sqlite:/data/}" ;;
    systemd:sqlite:*) printf '%s\n' "${d#sqlite:}" ;;
  esac
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

have_systemd() { [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; }

compose() {
  (
    cd "$DIR"
    if [ "$IMAGE" != looptrack ]; then
      export LOOPTRACK_IMAGE="$IMAGE:latest"
    fi
    docker compose "$@"
  )
}

# ---------------------------------------------------------------- 引数

parse_args() {
  while [ $# -gt 0 ]; do
    case $1 in
      --from) FROM=${2:?--from <ディレクトリ | URL>}; shift 2 ;;
      --from=*) FROM=${1#--from=}; shift ;;
      --version) WANT_VERSION=${2:?--version <版>}; shift 2 ;;
      --version=*) WANT_VERSION=${1#--version=}; shift ;;
      --sha256) WANT_SHA=${2:?--sha256 <hash>}; shift 2 ;;
      --sha256=*) WANT_SHA=${1#--sha256=}; shift ;;
      --method) METHOD=${2:?--method systemd|compose}; shift 2 ;;
      --method=*) METHOD=${1#--method=}; shift ;;
      --dir) DIR=${2:?--dir <ディレクトリ>}; shift 2 ;;
      --dir=*) DIR=${1#--dir=}; shift ;;
      --yes | -y) YES=1; shift ;;
      --no-start) NO_START=1; shift ;;
      --require-signature) REQUIRE_SIG=1; shift ;;
      --upgrade) ACTION=upgrade; shift ;;
      --uninstall) ACTION=uninstall; shift ;;
      --purge) PURGE=1; shift ;;
      -h | --help) usage; exit 0 ;;
      --) shift; break ;;
      *) die "不明な引数です: $1（setup の引数は -- の後ろに置く。--help）" ;;
    esac
  done
  SETUP_N=$#
  # setup の引数のうち、install.sh が決めるものは受け付けない。
  # --project は setup に渡したうえで控える（MCP の接続設定に出す既定のプロジェクト）
  take_project=0
  for a in "$@"; do
    if [ "$take_project" = 1 ]; then
      PROJECT=$a
      take_project=0
      continue
    fi
    case $a in
      --dir | --dir=* | --mode | --mode=* | --service | --service=* | --yes | --force | --yes=* | --force=*)
        die "setup の $a は install.sh が決めます（-- の後ろから外してください。作り直しは looptrack setup --force を直接使う）" ;;
      --project) take_project=1 ;;
      --project=*) PROJECT=${a#--project=} ;;
    esac
  done
  case $PROJECT in - | none) PROJECT="" ;; esac # setup の「作らない」の答え
  case $METHOD in "" | systemd | compose) ;; *) die "--method は systemd か compose です: $METHOD" ;; esac
  if [ "$PURGE" = 1 ] && [ "$ACTION" != uninstall ]; then
    die "--purge は --uninstall と一緒に使います"
  fi
  case $WANT_SHA in "" | [0-9a-f]*) ;; *) die "--sha256 は 16 進の小文字で指定してください" ;; esac
}

# ---------------------------------------------------------------- 取得

detect_platform() {
  os=$(uname -s)
  [ "$os" = Linux ] || die "Linux 専用です（この OS: $os）"
  case $(uname -m) in
    x86_64 | amd64) ARCH=amd64 ;;
    aarch64 | arm64) ARCH=arm64 ;;
    *) die "対応していない CPU です: $(uname -m)（amd64・arm64 のみ）" ;;
  esac
}

# download <url> <出力先>
download() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --retry 3 --proto '=https,http' -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O "$2" "$1"
  else
    die "curl も wget もありません（apt-get install -y curl）"
  fi
}

# fetch <名前> <出力先> — 取得元から 1 ファイル
fetch() {
  case $SRC in
    https://* | http://*) download "$SRC/$1" "$2" || die "取得できません: $SRC/$1" ;;
    *)
      [ -e "$SRC/$1" ] || die "ありません: $SRC/$1"
      cp "$SRC/$1" "$2"
      ;;
  esac
}

# try_fetch <名前> <出力先> — fetch と同じだが、無ければ 1 を返す（止めない）
try_fetch() {
  case $SRC in
    https://* | http://*) download "$SRC/$1" "$2" 2>/dev/null ;;
    *) [ -f "$SRC/$1" ] && cp "$SRC/$1" "$2" ;;
  esac
}

# verify_sums_signature — SHA256SUMS の minisign の署名を確かめる。合わなければ止める。
# 確かめられない（署名が無い・minisign が無い）ときは注意を出して続ける。--require-signature なら止める
verify_sums_signature() {
  if ! try_fetch SHA256SUMS.minisig "$TMP/SHA256SUMS.minisig"; then
    [ "$REQUIRE_SIG" = 1 ] && die "取得元に SHA256SUMS.minisig がありません（--require-signature）。何も入れ替えていません"
    warn "取得元に SHA256SUMS の署名（SHA256SUMS.minisig）がありません。SHA-256 の照合だけで進めます（公式の配布物には署名があります）"
    return 0
  fi
  if ! command -v minisign >/dev/null 2>&1; then
    [ "$REQUIRE_SIG" = 1 ] && die "minisign がありません（apt-get install -y minisign）。--require-signature のため止めました。何も入れ替えていません"
    warn "minisign が無いので SHA256SUMS の署名を確かめていません（apt-get install -y minisign を入れると確かめます。--require-signature で必須にできます）"
    return 0
  fi
  minisign -V -q -P "$MINISIGN_PUBKEY" -m "$TMP/SHA256SUMS" -x "$TMP/SHA256SUMS.minisig" >/dev/null 2>&1 ||
    die "SHA256SUMS の署名が合いません（改ざんされたか、別の鍵で署名されています）。何も入れ替えていません"
  say "  SHA256SUMS の署名を確かめました（minisign）"
}

# get_binary — 取得元から looptrack を取り、SHA-256 を確かめて $TMP/looptrack に置く。NEW_VERSION・NEW_SHA を決める
get_binary() {
  [ -n "$FROM" ] || die "取得元を --from <ディレクトリ | URL の接頭辞>（または LOOPTRACK_INSTALL_FROM）で指定してください"
  SRC=${FROM%/}
  case $SRC in
    http://*)
      case $SRC in
        http://127.0.0.1[:/]* | http://localhost[:/]*) ;;
        *) [ "${LOOPTRACK_INSTALL_ALLOW_HTTP:-}" = 1 ] || die "http:// の取得元は改ざんを防げません。https:// を使うか、承知の上なら LOOPTRACK_INSTALL_ALLOW_HTTP=1" ;;
      esac
      ;;
  esac
  detect_platform
  step "実行ファイルを取得します（$SRC・linux/$ARCH）"
  fetch SHA256SUMS "$TMP/SHA256SUMS"
  verify_sums_signature
  # SHA256SUMS から looptrack_<版>_linux_<arch> を選ぶ（名前の前の * は二進の印）
  awk -v arch="$ARCH" -v want="$WANT_VERSION" '
    NF == 2 {
      n = $2; sub(/^\*/, "", n)
      if (n !~ ("^looptrack_[0-9A-Za-z][0-9A-Za-z.+-]*_linux_" arch "$")) next
      v = n; sub(/^looptrack_/, "", v); sub("_linux_" arch "$", "", v)
      if (want != "" && v != want) next
      print tolower($1), n, v
    }' "$TMP/SHA256SUMS" >"$TMP/candidates"
  count=$(wc -l <"$TMP/candidates" | tr -d ' ')
  if [ "$count" -eq 0 ]; then
    if [ -n "$WANT_VERSION" ]; then
      die "取得元に looptrack_${WANT_VERSION}_linux_$ARCH がありません（SHA256SUMS を確かめてください）"
    fi
    die "取得元の SHA256SUMS に looptrack_<版>_linux_$ARCH がありません"
  fi
  if [ "$count" -gt 1 ]; then
    say "取得元に複数の版があります:" >&2
    awk '{print "  " $3}' "$TMP/candidates" >&2
    die "--version <版> で選んでください"
  fi
  read -r NEW_SHA name NEW_VERSION <"$TMP/candidates"
  if [ -n "$WANT_SHA" ] && [ "$WANT_SHA" != "$NEW_SHA" ]; then
    die "--sha256 と SHA256SUMS が合いません（指定 $WANT_SHA・SHA256SUMS $NEW_SHA）"
  fi
  fetch "$name" "$TMP/looptrack"
  got=$(sha256_of "$TMP/looptrack")
  [ "$got" = "$NEW_SHA" ] || die "SHA-256 が合いません: $name（期待 $NEW_SHA・実際 $got）。何も入れ替えていません"
  chmod 0755 "$TMP/looptrack"
  v=$("$TMP/looptrack" version 2>/dev/null) || die "取得した実行ファイルがこのサーバで動きません（$name）"
  # looptrack version は「looptrack <版>（headless・linux/<arch>）」（英語は "looptrack <版> (headless, linux/<arch>)"）
  case $v in
    "looptrack $NEW_VERSION（"* | "looptrack $NEW_VERSION "*) ;;
    *) warn "実行ファイルの版（$v）が名前の版（$NEW_VERSION）と違います" ;;
  esac
  say "  $name（SHA-256 一致: $NEW_SHA）"
}

# place_binary — $TMP/looptrack を $BIN に置く（同じ中身なら何もしない。置き換えは rename で一度に）
place_binary() {
  if [ -f "$BIN" ] && [ "$(sha256_of "$BIN")" = "$NEW_SHA" ]; then
    say "  $BIN は同じ版です（$NEW_VERSION）"
    return
  fi
  if [ -f "$BIN" ]; then
    cp -p "$BIN" "$BIN.prev"
  fi
  install -m 0755 "$TMP/looptrack" "$BIN.install-$$"
  mv -f "$BIN.install-$$" "$BIN"
  say "  置いた: $BIN（$NEW_VERSION）"
}

# ---------------------------------------------------------------- 状態

write_state() {
  mkdir -p "$CONF_DIR"
  chmod 0750 "$CONF_DIR"
  t="$STATE.install-$$"
  {
    say "# install.sh が作成（$(date '+%Y-%m-%d %H:%M')）。この印があると 2 回目の install.sh は「設定済み」を示して終わる"
    say "METHOD=$METHOD"
    say "DIR=$DIR"
    say "VERSION=$NEW_VERSION"
    say "PROJECT=$PROJECT"
    say "STARTED=$([ "$NO_START" = 1 ] && echo no || echo yes)"
  } >"$t"
  chmod 0644 "$t"
  mv -f "$t" "$STATE"
}

load_state() {
  [ -f "$STATE" ] || return 1
  S_METHOD=$(sed -n 's/^METHOD=//p' "$STATE")
  S_DIR=$(sed -n 's/^DIR=//p' "$STATE")
  S_VERSION=$(sed -n 's/^VERSION=//p' "$STATE")
  S_PROJECT=$(sed -n 's/^PROJECT=//p' "$STATE")
  S_STARTED=$(sed -n 's/^STARTED=//p' "$STATE")
  return 0
}

# 待ち受けと URL（.env から）
read_env_info() {
  envf="$DIR/.env"
  LISTEN=$(env_get LOOPTRACK_LISTEN "$envf")
  PORT=${LISTEN##*:}
  BASE=$(env_get LOOPTRACK_BASE_PATH "$envf")
  [ -n "$BASE" ] || BASE=/looptrack
  PUBLIC=$(env_get LOOPTRACK_PUBLIC_URL "$envf")
  PUBLIC=${PUBLIC%/}
  URL="$PUBLIC$BASE"
  LOCAL_MODE=$(env_get LOOPTRACK_LOCAL_MODE "$envf")
}

# ---------------------------------------------------------------- 表示

proxy_examples() {
  host=$(printf '%s' "$PUBLIC" | sed 's|^[a-z]*://||; s|[:/].*$||')
  cat <<EOF
# ---- nginx（TLS は nginx が持つ。証明書は certbot などで用意する）----
server {
    listen 443 ssl;
    server_name $host;
    # ssl_certificate     /etc/letsencrypt/live/$host/fullchain.pem;
    # ssl_certificate_key /etc/letsencrypt/live/$host/privkey.pem;

    location = $BASE { return 301 $BASE/; }
    location $BASE/ {
        proxy_pass http://127.0.0.1:$PORT;   # 末尾に / を付けない（接頭辞 $BASE を剥がさずに渡す）
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_buffering off;                 # MCP の応答を溜めない
        proxy_read_timeout 300s;
        client_max_body_size 20m;
    }
}

# ---- Caddy（証明書は Caddy が自動で取る）----
$host {
    redir $BASE $BASE/ 301
    handle $BASE/* {
        reverse_proxy 127.0.0.1:$PORT {
            header_up X-Real-IP {remote_host}
        }
    }
}
EOF
}

# mcp_config <プロジェクトの slug> — MCP の接続設定。looptrack setup が最後に出すものと同じ文字列・同じ並びにする。
# 正本は internal/setupwiz の MCPConfigs で、internal/setupwiz/install_sh_test.go がその出力とこの下の行を突き合わせる
# （直すときは両方を合わせる）。$URL は print_access が決める
mcp_config() {
  proj=$1
  cat <<EOF
  Claude Code（ターミナルで実行）:
    claude mcp add --transport http looptrack $URL/mcp --header "X-Looptrack-Project: $proj"
  Claude Code（またはプロジェクトの .mcp.json）:
    { "mcpServers": { "looptrack": { "type": "http", "url": "$URL/mcp", "headers": { "X-Looptrack-Project": "$proj" } } } }
  Codex（~/.codex/config.toml に足す）:
    [mcp_servers.looptrack]
    url = "$URL/mcp"
    http_headers = { "X-Looptrack-Project" = "$proj" }
  GitHub Copilot（VS Code）（プロジェクトの .vscode/mcp.json）:
    { "servers": { "looptrack": { "type": "http", "url": "$URL/mcp", "headers": { "X-Looptrack-Project": "$proj" } } } }
  GitHub Copilot CLI（~/.copilot/mcp-config.json（またはリポジトリの .github/mcp.json））:
    { "mcpServers": { "looptrack": { "type": "http", "url": "$URL/mcp", "headers": { "X-Looptrack-Project": "$proj" }, "tools": ["*"] } } }
    （tools は必須）
EOF
}

print_access() {
  say ""
  say "■ ブラウザで開く URL"
  say "  $URL/"
  say ""
  say "■ CLI のログイン"
  say "  looptrack issue login --browser --url $URL"
  say ""
  say "■ MCP の接続設定（AI から使う。コピーしてそのまま貼る）"
  if [ -n "$PROJECT" ]; then
    mcp_config "$PROJECT"
  else
    mcp_config "$PROJECT_UNKNOWN"
    say "  （$PROJECT_UNKNOWN は使うプロジェクトの slug に置き換えます。一覧は上の setup の案内か looptrack project list。"
    say "    プロジェクトを 1 つも作っていないときは --header と \"headers\" を外します）"
  fi
  if [ "$LOCAL_MODE" != 1 ]; then
    say ""
    say "■ リバースプロキシ（TLS はプロキシが持つ。looptrack は 127.0.0.1:$PORT だけで待ち受けます）"
    say "  設定例: $CONF_DIR/proxy-examples.txt（nginx と Caddy）"
  fi
  say ""
  say "■ 管理"
  if [ "$METHOD" = systemd ]; then
    say "  状態・ログ: systemctl status looptrack / journalctl -u looptrack -f"
    say "  管理コマンド: sudo sh -c 'set -a; . $CONF_DIR/.env; exec setpriv --reuid $SVC_USER --regid $SVC_USER --init-groups $BIN user list'"
  else
    say "  状態・ログ: cd $DIR && docker compose ps / docker compose logs -f"
    say "  管理コマンド: cd $DIR && docker compose run --rm --no-deps looptrack user list"
  fi
  say "  更新: sudo sh install.sh --upgrade --from <取得元>"
  say "  $DIR/.env の LOOPTRACK_SECRET_KEY を失うと全員の二段階認証が使えなくなります。別の場所に控えてください。"
}

show_configured() {
  METHOD=$S_METHOD
  DIR=$S_DIR
  [ -n "$PROJECT" ] || PROJECT=$S_PROJECT
  say "設定済みです（$STATE）。何も変えていません。"
  say "  動かし方: $S_METHOD・版: $S_VERSION・設定: $DIR/.env"
  if [ "$S_METHOD" = systemd ] && have_systemd; then
    say "  サービス: $(systemctl is-active looptrack 2>/dev/null || true)（systemctl status looptrack）"
  elif [ "$S_METHOD" = compose ] && command -v docker >/dev/null 2>&1; then
    say "  コンテナ: $(docker container inspect -f '{{.State.Status}}' looptrack 2>/dev/null || echo 'なし')"
  fi
  if [ "$S_STARTED" = no ]; then
    say "  --no-start で入れたため起動していません。起動: $(start_hint)"
  fi
  if [ -f "$DIR/.env" ]; then
    read_env_info
    print_access
  fi
  say ""
  say "更新は --upgrade、外すのは --uninstall（データを残す）です。"
}

start_hint() {
  if [ "$METHOD" = systemd ]; then
    echo "systemctl enable --now looptrack"
  else
    echo "cd $DIR && docker compose up -d（イメージは sudo sh install.sh --upgrade --from <取得元> で作る）"
  fi
}

# ---------------------------------------------------------------- 設定（looptrack setup）

run_setup() {
  if [ -f "$DIR/.env" ]; then
    say "  $DIR/.env は作成済みです（setup は飛ばします。作り直すときは looptrack setup --dir $DIR --force）"
    return
  fi
  # 起動の方法は setup の --service で伝える（systemd: compose.yaml を書かず 127.0.0.1 で待ち受け、SQLite はホストのパスのまま .env に書く。
  # compose: compose.yaml を書き、SQLite はコンテナの中の /data/im.db）
  if [ "$METHOD" = systemd ]; then
    set -- --service systemd --sqlite-path "$DATA_DIR/im.db" "$@"
  else
    set -- --service compose "$@"
  fi
  set -- --dir "$DIR" --mode team "$@"
  if [ "$YES" = 1 ]; then
    "$BIN" setup "$@" --yes || die "looptrack setup が失敗しました（上のメッセージ。何も残していません）"
  else
    say "  looptrack setup の問いに答えてください。① 使い方は「チームのサーバ」を選びます。"
    "$BIN" setup "$@" </dev/tty || die "looptrack setup を終えませんでした（何も残していません。もう一度実行すると続きから進みます）"
  fi
  [ -f "$DIR/.env" ] || die "setup が $DIR/.env を作りませんでした（中止したか、保存先にすでに利用者がいます。上のメッセージを見てください）"
}

# ---------------------------------------------------------------- systemd

write_unit() {
  t="$UNIT.install-$$"
  cat >"$t" <<EOF
# install.sh が作成。設定は $CONF_DIR/.env（root 600。systemd が読んで環境変数で渡す）。
[Unit]
Description=Looptrack server (issue management)
Documentation=https://github.com/howashoji/looptrack/blob/main/docs/server/DEPLOY.md
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$SVC_USER
Group=$SVC_USER
EnvironmentFile=$CONF_DIR/.env
Environment=GOMEMLIMIT=64MiB
ExecStart=$BIN serve
Restart=on-failure
RestartSec=5s
WorkingDirectory=$DATA_DIR
StateDirectory=looptrack
StateDirectoryMode=0750
UMask=0077

# サンドボックス: 書けるのは $DATA_DIR だけ。特権・デバイス・カーネルの設定に触れない
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
PrivateUsers=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
ProtectProc=invisible
ProcSubset=pid
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
RestrictNamespaces=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
RemoveIPC=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
CapabilityBoundingSet=
AmbientCapabilities=
SystemCallArchitectures=native
SystemCallFilter=@system-service
SystemCallFilter=~@privileged
SystemCallErrorNumber=EPERM
MemoryMax=512M

[Install]
WantedBy=multi-user.target
EOF
  chmod 0644 "$t"
  if [ -f "$UNIT" ] && cmp -s "$t" "$UNIT"; then
    rm -f "$t"
  else
    mv -f "$t" "$UNIT"
    say "  置いた: $UNIT"
  fi
}

ensure_user() {
  if id "$SVC_USER" >/dev/null 2>&1; then
    return
  fi
  nologin=/usr/sbin/nologin
  [ -x "$nologin" ] || nologin=/bin/false
  useradd --system --user-group --home-dir "$DATA_DIR" --no-create-home --shell "$nologin" "$SVC_USER"
  say "  作成: 利用者 $SVC_USER"
}

# systemd で動かすための所有者とパーミッション（何度行っても同じ結果。setup の後の中断でも次の実行で直る）
#   setup は root で動くので、作った SQLite を $SVC_USER に渡す。.env は root だけ（systemd の EnvironmentFile が読む）
fix_owner_systemd() {
  envf="$CONF_DIR/.env"
  chown root:root "$envf"
  chmod 0600 "$envf"
  chown -R "$SVC_USER:$SVC_USER" "$DATA_DIR"
  chmod 0750 "$DATA_DIR"
  db=$(sqlite_host_path)
  if [ -n "$db" ]; then restrict_sqlite "$db"; fi
}

install_systemd() {
  if [ "$NO_START" = 0 ]; then
    have_systemd || die "systemd が動いていません（--method compose を使うか、--no-start で設定だけ行う）"
  fi
  step "利用者とディレクトリ"
  ensure_user
  mkdir -p "$CONF_DIR" "$DATA_DIR"
  chmod 0750 "$CONF_DIR"
  chown "$SVC_USER:$SVC_USER" "$DATA_DIR"
  chmod 0750 "$DATA_DIR"
  step "実行ファイル"
  place_binary
  step "設定（looptrack setup）"
  run_setup "$@"
  fix_owner_systemd
  step "systemd の unit"
  write_unit
  read_env_info
  if [ "$NO_START" = 1 ]; then
    say "  --no-start: 起動していません（起動: systemctl enable --now looptrack）"
    return
  fi
  check_store_access
  db=$(sqlite_host_path) # 確認で SQLite を開いたときの -wal・-shm を 0600 に戻す
  if [ -n "$db" ]; then restrict_sqlite "$db"; fi
  systemctl daemon-reload
  systemctl enable looptrack >/dev/null 2>&1
  systemctl restart looptrack
  wait_health
}

# ---------------------------------------------------------------- compose

build_image() {
  step "イメージ $IMAGE:$NEW_VERSION（scratch に取得した実行ファイルを載せる）"
  mkdir -p "$TMP/ctx"
  cp "$TMP/looptrack" "$TMP/ctx/looptrack"
  # 第三者のライセンス文（/NOTICE）。取得元の NOTICE ではなく、入れる実行ファイル自身が埋め込んでいるものを書き出す
  # （looptrack licenses はルートの NOTICE をそのまま出す。実行ファイルと必ず同じ版になり、取得元に NOTICE が無くても入る）
  "$TMP/looptrack" licenses >"$TMP/ctx/NOTICE" || die "looptrack licenses が動きません（第三者のライセンス文を取り出せません）"
  [ -s "$TMP/ctx/NOTICE" ] || die "looptrack licenses の出力が空です（第三者のライセンス文を取り出せません）"
  # deploy/Dockerfile と同じ形（シェルも curl も無い。健全性確認は looptrack healthcheck）
  cat >"$TMP/ctx/Dockerfile" <<'EOF'
FROM scratch
COPY looptrack /looptrack
COPY NOTICE /NOTICE
USER 65534:65534
EXPOSE 8090
ENTRYPOINT ["/looptrack"]
CMD ["serve"]
EOF
  docker build -q -t "$IMAGE:$NEW_VERSION" "$TMP/ctx" >/dev/null
  docker tag "$IMAGE:$NEW_VERSION" "$IMAGE:latest"
  say "  作成: $IMAGE:$NEW_VERSION（$IMAGE:latest）"
}

check_docker() {
  command -v docker >/dev/null 2>&1 || die "docker がありません（Docker Engine と compose プラグインを入れてください: https://docs.docker.com/engine/install/）"
  docker compose version >/dev/null 2>&1 || die "docker compose（v2 のプラグイン）がありません（apt-get install -y docker-compose-plugin など）"
}

install_compose() {
  [ "$NO_START" = 1 ] || check_docker
  step "実行ファイル（setup と管理用に $BIN にも置く）"
  place_binary
  mkdir -p "$CONF_DIR"
  chmod 0750 "$CONF_DIR"
  step "設定（looptrack setup）"
  run_setup "$@"
  [ -f "$DIR/compose.yaml" ] || die "$DIR/compose.yaml がありません（compose では setup の ① で「チームのサーバ」を選びます。rm $DIR/.env してやり直してください）"
  if [ -d "$DIR/data" ]; then
    # コンテナは uid 65534 で動く（SQLite のファイルを書けるように。im.db は 0600 なので中のファイルごと渡す）
    chown -R 65534:65534 "$DIR/data"
  fi
  db=$(sqlite_host_path)
  if [ -n "$db" ]; then restrict_sqlite "$db"; fi
  read_env_info
  if [ "$NO_START" = 1 ]; then
    say "  --no-start: イメージを作らず、起動していません"
    return
  fi
  build_image
  check_store_access
  db=$(sqlite_host_path) # 確認で SQLite を開いたときの -wal・-shm を 0600 に戻す
  if [ -n "$db" ]; then restrict_sqlite "$db"; fi
  step "起動（docker compose up -d）"
  compose up -d
  wait_health
}

# ---------------------------------------------------------------- 保存先の確認（起動の前）

# is_mysql — .env の LOOPTRACK_DSN が MySQL なら真（sqlite:<パス> 以外は MySQL の DSN）
is_mysql() {
  case $(env_get LOOPTRACK_DSN "$DIR/.env") in
    sqlite:* | "") return 1 ;;
    *) return 0 ;;
  esac
}

# app_run <looptrack の引数…> — サービスと同じ利用者・同じ設定（.env の LOOPTRACK_DSN）で looptrack を動かす
app_run() {
  if [ "$METHOD" = systemd ]; then
    (
      set -a
      # shellcheck disable=SC1090,SC1091
      . "$DIR/.env"
      set +a
      exec setpriv --reuid "$SVC_USER" --regid "$SVC_USER" --init-groups "$BIN" "$@"
    )
  else
    compose run --rm --no-deps looptrack "$@"
  fi
}

# check_store_access — 起動の前に、サービスと同じ利用者で保存先を読めるかを確かめる。
#
# MySQL を最小権限（deploy/grants.sql）で使う構成では、権限を与える順番が決まっている: 表ごとの GRANT は
# 表ができてからしか流せないので、「setup（migrate）が表を作る → grants.sql を流す → 起動」になる。
# install.sh は setup から起動まで続けて行うため、grants.sql を流す隙が無く、アプリ用の利用者は
# まだ何も読めない。そのまま起動すると /healthz が上がらず、60 秒待ってから「ログを見てください」で終わる。
# ここで先に読んでみて、読めなければ原因（権限か接続先）を指すエラーにして止める。設定はそのまま残るので、
# grants.sql を流してからもう一度実行すれば、setup を飛ばして起動と動作確認だけになる。
# ついでに、読めたときは最初のプロジェクトの slug を控える（MCP の接続設定に出す）。
#
# 止めるのは MySQL のときだけにする。ここまで来ていれば setup（migrate）は繋がっているので、
# アプリ用の DSN だけが読めないのは「権限がまだ無い」か「接続先が違う」のどちらかで、どちらも起動しても直らない。
# SQLite と、確認そのものができなかったとき（setpriv が無いなど）は、注意だけ出して進む
check_store_access() {
  if [ "$METHOD" = systemd ] && ! command -v setpriv >/dev/null 2>&1; then
    warn "setpriv が無いので、起動の前の保存先の確認を飛ばします（util-linux）"
    return 0
  fi
  step "保存先の確認（サービスと同じ利用者で読めるか）"
  if out=$(app_run project list 2>&1); then
    say "  読めました"
    if [ -z "$PROJECT" ]; then
      # project list の 1 行目は見出し（SLUG PREFIX …）。2 行目の 1 列目が最初のプロジェクト
      PROJECT=$(printf '%s\n' "$out" | awk 'NR == 2 { print $1 }')
    fi
    return 0
  fi
  if ! is_mysql; then
    warn "保存先を確かめられませんでした（このまま起動します）: $out"
    return 0
  fi
  retry="sudo sh $PROG --from <取得元>"
  if [ "$ACTION" = upgrade ]; then retry="sudo sh $PROG --upgrade --from <取得元>"; fi
  printf '%s\n' "$out" >&2
  die "MySQL の保存先をアプリ用の利用者で読めません（上の出力）。サービスは起動していません。

考えられるのは次の 2 つです。

(1) 表への権限がまだ無い（最小権限（grants.sql）の構成）。
    表ごとの GRANT は表ができてからしか流せないので、順番が決まっています:
      1. looptrack setup が表を作る（ここまで終わりました）
      2. 管理用の資格情報（root など）で grants.sql を流す:
           mysql -u root -p <DB 名> < grants.sql
         grants.sql は取得元（--from の接頭辞）にも同じ名前で並んでいます:
           curl -fsSL -O \"<取得元>/grants.sql\"
         ソースから使うなら deploy/grants.sql です。DB 名・利用者名が im・im_app と違うときは、
         中の「im.」と「'im_app'@'%'」を自分の名前に置き換えてから流してください。
      3. もう一度 $retry を実行する
         （設定（$DIR/.env）は残っているので、setup は飛ばして起動と動作確認だけになります）
    表が増えた更新（--upgrade）でも同じことが起きます。grants.sql は表を足したときにも流し直してください。

(2) $DIR/.env の LOOPTRACK_DSN（アプリが使う接続先）が違う。
    表を作るときの接続先（LOOPTRACK_SETUP_DSN・LOOPTRACK_SETUP_MIGRATE_DSN）とは別に指定しています。"
}

# ---------------------------------------------------------------- 起動の確認

health_once() {
  if [ "$METHOD" = systemd ]; then
    LOOPTRACK_HEALTH_URL="http://127.0.0.1:$PORT$BASE/healthz" "$BIN" healthcheck >/dev/null 2>&1
  else
    compose exec -T looptrack /looptrack healthcheck >/dev/null 2>&1
  fi
}

wait_health() {
  step "動作確認（$BASE/healthz）"
  i=0
  while ! health_once; do
    i=$((i + 1))
    if [ "$i" -ge 60 ]; then
      if [ "$METHOD" = systemd ]; then
        journalctl -u looptrack -n 30 --no-pager >&2 || true
      else
        compose logs --tail 30 >&2 || true
      fi
      die "60 秒待っても /healthz が 200 を返しません（上のログを見てください）"
    fi
    sleep 1
  done
  say "  200 OK"
}

# ---------------------------------------------------------------- 更新

backup_sqlite() { # <SQLite のファイル>
  db=$1
  [ -f "$db" ] || return 0
  b="$(dirname "$db")/backup-$(date +%Y%m%d-%H%M%S)"
  mkdir -p "$b"
  chmod 0700 "$b" # 控えも DB と同じく本人（root）だけが読める場所に置く
  for f in "$db" "$db-wal" "$db-shm"; do
    if [ -f "$f" ]; then
      cp -p "$f" "$b/"
    fi
  done
  say "  控え: $b（止めた状態で写した SQLite）"
}

upgrade() {
  load_state || die "install.sh で入れた印（$STATE）がありません。先に install.sh で入れてください"
  METHOD=$S_METHOD
  DIR=$S_DIR
  PROJECT=$S_PROJECT # 印に書き戻すため（更新では変わらない）
  get_binary
  if [ "$NEW_VERSION" = "$S_VERSION" ] && [ -f "$BIN" ] && [ "$(sha256_of "$BIN")" = "$NEW_SHA" ]; then
    say "すでに $NEW_VERSION です。何も変えていません。"
    return
  fi
  read_env_info
  dsn=$(env_get LOOPTRACK_DSN "$DIR/.env")
  # MySQL で、テーブルを作れる利用者を別に使うときは LOOPTRACK_SETUP_MIGRATE_DSN（setup と同じ）
  mdsn=${LOOPTRACK_SETUP_MIGRATE_DSN:-$dsn}
  if [ "$METHOD" = systemd ]; then
    step "停止"
    systemctl stop looptrack
    db=$(sqlite_host_path)
    if [ -n "$db" ]; then
      backup_sqlite "$db"
      restrict_sqlite "$db"
    fi
    step "実行ファイル"
    place_binary
    step "マイグレーション（$SVC_USER として）"
    (
      set -a
      # shellcheck disable=SC1090,SC1091
      . "$DIR/.env"
      set +a
      LOOPTRACK_DSN=$mdsn exec setpriv --reuid "$SVC_USER" --regid "$SVC_USER" --init-groups "$BIN" migrate
    ) || die "マイグレーションに失敗しました。前の実行ファイルは $BIN.prev にあります（戻すときは mv $BIN.prev $BIN && systemctl start looptrack）"
    check_store_access # 表が増えた更新では grants.sql を流し直すまで読めない
    if [ -n "$db" ]; then restrict_sqlite "$db"; fi
    step "起動"
    systemctl daemon-reload
    systemctl start looptrack
  else
    check_docker
    step "実行ファイル"
    place_binary
    build_image
    step "停止"
    compose stop
    db=$(sqlite_host_path)
    if [ -n "$db" ]; then
      backup_sqlite "$db"
      restrict_sqlite "$db"
    fi
    step "マイグレーション"
    if [ "$mdsn" != "$dsn" ]; then
      (
        export LOOPTRACK_DSN="$mdsn"
        compose run --rm --no-deps -e LOOPTRACK_DSN looptrack migrate
      )
    else
      compose run --rm --no-deps looptrack migrate
    fi || die "マイグレーションに失敗しました（前のイメージは $IMAGE:$S_VERSION。compose.yaml の image を戻して up -d）"
    check_store_access # 表が増えた更新では grants.sql を流し直すまで読めない
    if [ -n "$db" ]; then restrict_sqlite "$db"; fi
    step "起動"
    compose up -d
  fi
  wait_health
  NO_START=0
  write_state
  say ""
  say "更新しました: $S_VERSION → $NEW_VERSION（データはそのままです）"
}

# ---------------------------------------------------------------- 外す

uninstall() {
  load_state || die "install.sh で入れた印（$STATE）がありません"
  METHOD=$S_METHOD
  DIR=$S_DIR
  if [ "$PURGE" = 1 ] && [ "$YES" = 0 ]; then
    say "--purge は設定（LOOPTRACK_SECRET_KEY を含む $DIR/.env）とデータ（SQLite）を消します。元に戻せません。"
    say "MySQL のデータベースは消しません（必要なら自分で DROP してください）。"
    ask "続けるなら purge と入力" ""
    [ "$ans" = purge ] || die "中止しました（何も変えていません）"
  fi
  if [ "$METHOD" = systemd ]; then
    if have_systemd; then
      systemctl disable --now looptrack >/dev/null 2>&1 || true
    fi
    rm -f "$UNIT"
    if have_systemd; then systemctl daemon-reload; fi
  else
    if command -v docker >/dev/null 2>&1 && [ -f "$DIR/compose.yaml" ]; then
      compose down || true
    fi
  fi
  rm -f "$BIN" "$BIN.prev" "$STATE" "$CONF_DIR/proxy-examples.txt"
  if [ "$PURGE" = 1 ]; then
    if [ "$METHOD" = systemd ]; then
      rm -rf "$DATA_DIR"
      if id "$SVC_USER" >/dev/null 2>&1; then userdel "$SVC_USER" || true; fi
    else
      rm -rf "$DIR"
      if command -v docker >/dev/null 2>&1; then
        docker image ls --format '{{.Repository}}:{{.Tag}}' "$IMAGE" | while read -r img; do docker image rm "$img" >/dev/null || true; done
      fi
    fi
    rm -rf "$CONF_DIR"
    say "外しました（設定とデータも消しました）。"
  else
    say "外しました。設定とデータは残しています:"
    if [ "$METHOD" = systemd ]; then
      say "  $CONF_DIR/.env・$DATA_DIR（利用者 $SVC_USER も残しています）"
    else
      say "  $DIR（.env・compose.yaml・data）とイメージ $IMAGE:*"
    fi
    say "  もう一度 install.sh を実行すると、残した設定で入れ直します。消すときは --uninstall --purge。"
  fi
}

# ---------------------------------------------------------------- 入れる

install_main() {
  if load_state; then
    [ -z "$METHOD" ] || [ "$METHOD" = "$S_METHOD" ] || die "すでに $S_METHOD で入っています（$STATE）。変えるときは --uninstall してから"
    show_configured
    return
  fi
  if [ -z "$METHOD" ]; then
    if [ "$YES" = 1 ]; then
      METHOD=systemd
    else
      def=1
      if ! have_systemd && command -v docker >/dev/null 2>&1; then def=2; fi
      say "動かし方を選んでください:"
      say "  1) systemd（実行ファイルを専用ユーザーで常駐させる）"
      say "  2) Docker compose（コンテナで動かす。Docker Engine と compose プラグインが要る）"
      ask "番号" "$def"
      case $ans in 1 | systemd) METHOD=systemd ;; 2 | compose) METHOD=compose ;; *) die "1 か 2 を選んでください" ;; esac
    fi
  fi
  if [ "$METHOD" = systemd ]; then
    [ -z "$DIR" ] || [ "$DIR" = "$CONF_DIR" ] || die "systemd では設定の置き場は $CONF_DIR 固定です（--dir は compose 用）"
    DIR=$CONF_DIR
  else
    [ -n "$DIR" ] || DIR=$COMPOSE_DIR_DEFAULT
    case $DIR in /*) ;; *) die "--dir は絶対パスで指定してください" ;; esac
  fi
  get_binary
  if [ "$METHOD" = systemd ]; then
    install_systemd "$@"
  else
    install_compose "$@"
  fi
  if [ "$LOCAL_MODE" != 1 ]; then
    t="$CONF_DIR/proxy-examples.txt.install-$$"
    proxy_examples >"$t"
    chmod 0644 "$t"
    mv -f "$t" "$CONF_DIR/proxy-examples.txt"
  fi
  write_state
  say ""
  if [ "$NO_START" = 1 ]; then
    say "設定まで終わりました（起動はしていません。起動: $(start_hint)）。"
  else
    say "インストールが終わりました（$METHOD・$NEW_VERSION）。"
  fi
  print_access
  if [ "$LOCAL_MODE" != 1 ]; then
    say ""
    proxy_examples
  fi
}

main() {
  parse_args "$@"
  # parse_args の後ろ（-- の後）を setup の引数として残す
  shift $(($# - SETUP_N))
  need_root
  umask 022
  TMP=$(mktemp -d "${TMPDIR:-/tmp}/looptrack-install.XXXXXX")
  case $ACTION in
    install) install_main "$@" ;;
    upgrade) upgrade ;;
    uninstall) uninstall ;;
  esac
}

main "$@"
