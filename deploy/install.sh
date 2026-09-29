#!/bin/sh
# looptrack-install-sh（この行は印。自動の置き換えが手元に置く写しが本物の install.sh かを見分ける。消さない）
# looptrack のインストーラ。まっさらな Linux サーバ（Ubuntu LTS・Debian）で
# 書庫の取得 → 照合（SHA256SUMS と minisign の署名）→ 展開 → 設定（looptrack setup）→（MySQL の最小権限なら）権限 →
# 起動 → 動作確認までを 1 回で行う。手順と判断は docs/server/DEPLOY.md「install.sh」。
#
#   curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh
#                                                                                対話（最新のリリースを入れる）
#   … | sudo sh -s -- --version <版>                                              版を固定する
#   … | sudo sh -s -- --yes --method systemd -- <setup の引数>                     非対話（setup の引数は -- の後ろ）
#   … | sudo sh -s -- --upgrade                                                   実行ファイルを入れ替え、migrate して再起動（データは保つ）
#   … | sudo sh -s -- --auto-upgrade on|off                                       自動の置き換え（systemd timer。既定は off）を入れる / 外す
#   sudo sh install.sh --from <ディレクトリ | URL の接頭辞>                          手元の配布物・自分の取得元から入れる
#   sudo sh install.sh --uninstall [--purge]                                      外す（既定はデータを残す。--purge で消す）
#
# このスクリプト自身は HTTPS で取るだけで、照合できない（照合の鍵と手順をこのスクリプトが持つため）。
# 取った実行ファイルは、下の公開鍵で署名を確かめた SHA256SUMS で照合してから展開・配置する。
#
# 取得元（--from を付けないときは GitHub Releases の <リポジトリ>/releases/latest/download。--version なら …/releases/download/<版>）:
#   <接頭辞>/SHA256SUMS                                     sha256sum の形（<hash>  <名前>）
#   <接頭辞>/looptrack_<版>_linux_<amd64|arm64>_server.tar.gz  書庫（GitHub Releases の形。中の <書庫の名前>/looptrack を置く）
#   <接頭辞>/looptrack_<版>_linux_<amd64|arm64>               素の実行ファイル（deploy/release/dist.sh build の出力・サーバの配布ディレクトリ・
#                                                             1.0.0-rc.2 までの Releases の形）。同じ版に書庫があれば書庫を使う
#   <接頭辞>/SHA256SUMS.minisig                             SHA256SUMS の minisign の署名（公式の配布物にはある）
#     minisign コマンドがあれば、下の公開鍵で署名を確かめ、合わなければ何も入れない。minisign が無い・署名が無いときは注意を出して続ける
#     （--require-signature で止める）。公開鍵は looptrack（selfupdate.MinisignPublicKey）・deploy/release/minisign.pub と同じ
# 保存先に MySQL を選び、アプリ用の利用者を最小権限（deploy/grants.sql）にするときは、流す順番が決まっている:
#   setup が表を作る → 管理用の資格情報で権限を与える → 起動。表ごとの GRANT は表ができてからしか流せないため。
#   起動の前に「保存先の確認」でアプリ用の利用者が読めるかを試し、読めなければ管理用の資格情報を端末で尋ねて
#   looptrack grants apply（権限の中身は実行ファイルに埋め込んだ deploy/grants.sql）で与え、読めることを確かめてから起動する。
#   資格情報はファイル・.env・install.conf・ログに残さない。アプリ用の利用者に DB 単位の広い権限を与える構成では、この確認は素通りする。
#   DB がまだ無ければ、setup が接続の段（migrate の前）で気づき、同じ処理（looptrack grants apply）を先に動かす
#   （管理用の資格情報を端末で尋ね、作るかを確かめてから DB・アプリ用の利用者・表・権限を作る）。そのあと起動の前の確認は素通りする。
# 動かし方:
#   systemd  専用ユーザー looptrack・/usr/local/bin/looptrack・/etc/looptrack/.env・/var/lib/looptrack・サンドボックス付きの unit
#   compose  <dir>（既定 /opt/looptrack）に setup が書く compose.yaml。取得した実行ファイルを scratch に載せたイメージ looptrack:<版> を作る
#            （第三者のライセンス文は looptrack licenses の出力をイメージの /NOTICE に入れる。公式のイメージと同じ）
# どちらも 127.0.0.1 だけで待ち受ける。TLS は前段のリバースプロキシ（nginx・Caddy の設定例を出す）が持つ。
# 自動の置き換え（--auto-upgrade on。既定は off。systemd だけ）: 1 日 1 回の systemd timer（looptrack-upgrade.timer）が、
#   手元に置いたこのスクリプトの写し（/usr/local/lib/looptrack/install.sh。root だけが書ける）を
#   --upgrade --require-signature --yes --only-newer で動かす。取るのは書庫だけで、書庫は SHA256SUMS と minisign の署名で確かめる。
#   写しを新しくするのは、人がこのスクリプトを動かしたとき（1 行か手元のファイルで --upgrade・--auto-upgrade on・入れる）だけ。
#   無人の更新（--yes で端末が無い。timer）は、MySQL で新しい版に未適用の migrate があれば置き換えず、止めた後に失敗すれば
#   前の実行ファイル（SQLite は DB の控えも）に戻して起動し直し、0 でない終了コードで終わる（サービスを止めたままにしない）。
#   compose（コンテナのイメージ）は自動では置き換えない（新しい版は looptrack serve が管理画面の帯・doctor・起動時のログで知らせる）。
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
# --from を付けないときの取得元（GitHub のリポジトリ。LOOPTRACK_INSTALL_REPO はテスト・ミラー用）
REPO=${LOOPTRACK_INSTALL_REPO:-https://github.com/howashoji/looptrack}
# このスクリプトを取る 1 行（案内に出す）
ONE_LINER='curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh'
# 1 行（curl … | sh）で動かしたときに写しを取る URL（手元のファイルで動かしたときはそのファイルを写す。
# LOOPTRACK_INSTALL_SCRIPT_URL はテスト・ミラー用）
SCRIPT_URL=${LOOPTRACK_INSTALL_SCRIPT_URL:-https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh}
# 自動の置き換え（--auto-upgrade on）で置くもの
AUTO_LIB=/usr/local/lib/looptrack
AUTO_SH=$AUTO_LIB/install.sh
AUTO_SVC=/etc/systemd/system/looptrack-upgrade.service
AUTO_TIMER=/etc/systemd/system/looptrack-upgrade.timer

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
# 自動の置き換えの指定（on / off。空は「変えない」= 印の値、印に無ければ off）と、決まった値
AUTO_UPGRADE="${LOOPTRACK_INSTALL_AUTO_UPGRADE:-}"
AUTO=off
ONLY_NEWER=0
# 無人の更新で、止めた後の失敗を前の版に戻すための状態（rollback_upgrade）
UNATTENDED=0
ROLLBACK=0
RB_BIN=0
RB_BACKUP=""

# ---------------------------------------------------------------- 共通

say() { printf '%s\n' "$*"; }
step() { printf '\n==> %s\n' "$*"; }
warn() { printf '%s: 注意: %s\n' "$PROG" "$*" >&2; }
die() {
  printf '%s: エラー: %s\n' "$PROG" "$*" >&2
  if [ "$ROLLBACK" = 1 ]; then rollback_upgrade; fi
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
使い方: curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh -s -- [オプション] [-- <looptrack setup の引数>]
        sudo sh install.sh [オプション] [-- <looptrack setup の引数>]

  --from <ディレクトリ | URL の接頭辞>   取得元（環境変数 LOOPTRACK_INSTALL_FROM でも可）。省略すると GitHub Releases の最新
                                        （--version を付ければその版）。<接頭辞>/SHA256SUMS と、書庫
                                        <接頭辞>/looptrack_<版>_linux_<arch>_server.tar.gz か素の <接頭辞>/looptrack_<版>_linux_<arch>
  --version <版>                        入れる版（LOOPTRACK_INSTALL_VERSION）。--from があれば、取得元に複数の版があるときの選択
  --sha256 <hash>                       取得するファイル（書庫か素の実行ファイル）の SHA-256 を別経路で固定する（SHA256SUMS と両方で確かめる）
  --require-signature                   SHA256SUMS の署名（SHA256SUMS.minisig）を必ず確かめる（minisign か署名が無ければ止める。
                                        LOOPTRACK_INSTALL_REQUIRE_SIGNATURE=1）。指定しなくても、minisign があれば確かめる
  --method systemd|compose              動かし方（対話では問う。--yes の既定は systemd）
  --dir <ディレクトリ>                  compose の置き場（既定 /opt/looptrack）
  --yes                                 対話しない（setup の答えは -- の後ろに渡す。秘密は環境変数 LOOPTRACK_SETUP_*）。
                                        MySQL の権限を与える管理用の資格情報は、それでも端末で尋ねる（端末が無ければ
                                        LOOPTRACK_INSTALL_DB_ADMIN_USER と LOOPTRACK_INSTALL_DB_ADMIN_PASSWORD_FILE。systemd のみ）
  --no-start                            設定まで行い、サービスを起動しない（compose ではイメージも作らない）
  --upgrade                             実行ファイルを入れ替え、migrate して再起動する（データは保つ）
  --only-newer                          --upgrade で、取得した版が入っている版より新しいときだけ置き換える（同じ・古いなら何もしない）
  --auto-upgrade on|off                 自動の置き換え（LOOPTRACK_INSTALL_AUTO_UPGRADE。既定は off。systemd だけ）。on は 1 日 1 回の
                                        systemd timer が --upgrade --require-signature --only-newer を動かす（minisign が要る）。
                                        入れた後のサーバにだけ付けても効く（入れ直さない）
  --uninstall [--purge]                 外す。既定では設定とデータを残す。--purge で設定・データ・利用者も消す
  -h, --help                            この表示

例（非対話・SQLite・systemd）:
  printf '%s\n' '<管理者のパスワード>' > /root/pw && chmod 600 /root/pw
  sudo sh install.sh --version v1.0.0 --yes --method systemd -- \
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
      --only-newer) ONLY_NEWER=1; shift ;;
      --auto-upgrade) AUTO_UPGRADE=${2:?--auto-upgrade on|off}; shift 2 ;;
      --auto-upgrade=*) AUTO_UPGRADE=${1#--auto-upgrade=}; shift ;;
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
  case $AUTO_UPGRADE in "" | on | off) ;; *) die "--auto-upgrade は on か off です: $AUTO_UPGRADE" ;; esac
  if [ "$AUTO_UPGRADE" != "" ] && [ "$ACTION" = uninstall ]; then
    die "--auto-upgrade は --uninstall と一緒に使えません（--uninstall は自動の置き換えも外します）"
  fi
  if [ "$ONLY_NEWER" = 1 ] && [ "$ACTION" != upgrade ]; then
    die "--only-newer は --upgrade と一緒に使います"
  fi
}

# ---------------------------------------------------------------- 取得

detect_platform() {
  os=$(uname -s)
  [ "$os" = Linux ] || die "Linux 専用です（この OS: ${os}）。何も変えていません。macOS・Windows でサーバを動かすときは、デスクトップ版を使ってください"
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

# get_binary — 取得元から looptrack を取り、SHA-256 を確かめて $TMP/looptrack に置く。NEW_VERSION・NEW_SHA（置く実行ファイルの SHA-256）を決める。
# 書庫（…_server.tar.gz）は照合してから展開し、中の looptrack だけを取り出す。照合が合わなければ何も置かない
get_binary() {
  if [ -z "$FROM" ]; then
    if [ -n "$WANT_VERSION" ]; then
      FROM="${REPO%/}/releases/download/$WANT_VERSION"
    else
      FROM="${REPO%/}/releases/latest/download"
    fi
  fi
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
  step "実行ファイルを取得します（${SRC}・linux/${ARCH}）"
  fetch SHA256SUMS "$TMP/SHA256SUMS"
  verify_sums_signature
  # SHA256SUMS から、書庫 looptrack_<版>_linux_<arch>_server.tar.gz と素の looptrack_<版>_linux_<arch> を版ごとに選ぶ
  # （名前の前の * は二進の印）。同じ版に書庫があれば書庫を取る（公式の SHA256SUMS は書庫の中の実行ファイルの行も持つが、
  # その名前のファイルは並んでいない）。行: <取るものの hash> <名前> <版> <archive|raw> <素の実行ファイルの hash か ->
  awk -v arch="$ARCH" -v want="$WANT_VERSION" '
    NF == 2 {
      n = $2; sub(/^\*/, "", n)
      if (n ~ ("^looptrack_[0-9A-Za-z][0-9A-Za-z.+-]*_linux_" arch "_server[.]tar[.]gz$")) {
        v = n; sub(/^looptrack_/, "", v); sub("_linux_" arch "_server[.]tar[.]gz$", "", v)
        if (want != "" && v != want) next
        arc[v] = tolower($1) " " n
      } else if (n ~ ("^looptrack_[0-9A-Za-z][0-9A-Za-z.+-]*_linux_" arch "$")) {
        v = n; sub(/^looptrack_/, "", v); sub("_linux_" arch "$", "", v)
        if (want != "" && v != want) next
        raw[v] = tolower($1); rawn[v] = n
      } else next
      seen[v] = 1
    }
    END {
      for (v in seen) {
        if (v in arc) print arc[v], v, "archive", ((v in raw) ? raw[v] : "-")
        else print raw[v], rawn[v], v, "raw", "-"
      }
    }' "$TMP/SHA256SUMS" >"$TMP/candidates"
  count=$(wc -l <"$TMP/candidates" | tr -d ' ')
  if [ "$count" -eq 0 ]; then
    if [ -n "$WANT_VERSION" ]; then
      die "取得元に looptrack_${WANT_VERSION}_linux_${ARCH}_server.tar.gz（または looptrack_${WANT_VERSION}_linux_${ARCH}）がありません（SHA256SUMS を確かめてください）"
    fi
    die "取得元の SHA256SUMS に looptrack_<版>_linux_${ARCH}_server.tar.gz（または looptrack_<版>_linux_${ARCH}）がありません"
  fi
  if [ "$count" -gt 1 ]; then
    say "取得元に複数の版があります:" >&2
    awk '{print "  " $3}' "$TMP/candidates" >&2
    die "--version <版> で選んでください"
  fi
  read -r want_sha name NEW_VERSION kind raw_sha <"$TMP/candidates"
  if [ -n "$WANT_SHA" ] && [ "$WANT_SHA" != "$want_sha" ]; then
    die "--sha256 と SHA256SUMS が合いません（指定 ${WANT_SHA}・SHA256SUMS ${want_sha}）"
  fi
  fetch "$name" "$TMP/download"
  got=$(sha256_of "$TMP/download")
  [ "$got" = "$want_sha" ] || die "SHA-256 が合いません: ${name}（期待 ${want_sha}・実際 ${got}）。何も入れ替えていません"
  if [ "$kind" = archive ]; then
    # 照合の後で展開する。取り出すのは <書庫の名前>/looptrack の 1 つだけ（ほかの名前・パスは展開しない）
    base=${name%.tar.gz}
    mkdir -p "$TMP/archive"
    tar -xzf "$TMP/download" -C "$TMP/archive" "$base/looptrack" 2>/dev/null ||
      die "書庫 ${name} から ${base}/looptrack を取り出せません。何も入れ替えていません"
    mv "$TMP/archive/$base/looptrack" "$TMP/looptrack"
    rm -rf "$TMP/archive" "$TMP/download"
    NEW_SHA=$(sha256_of "$TMP/looptrack")
    if [ "$raw_sha" != - ] && [ "$raw_sha" != "$NEW_SHA" ]; then
      die "書庫の中の looptrack が SHA256SUMS の looptrack_${NEW_VERSION}_linux_$ARCH の行と合いません（期待 ${raw_sha}・実際 ${NEW_SHA}）。何も入れ替えていません"
    fi
  else
    mv "$TMP/download" "$TMP/looptrack"
    NEW_SHA=$want_sha
  fi
  chmod 0755 "$TMP/looptrack"
  v=$("$TMP/looptrack" version 2>/dev/null) || die "取得した実行ファイルがこのサーバで動きません（${name}）"
  # looptrack version は「looptrack <版>（headless・linux/<arch>）」（英語は "looptrack <版> (headless, linux/<arch>)"）
  case $v in
    "looptrack ${NEW_VERSION}（"* | "looptrack $NEW_VERSION "*) ;;
    *) warn "実行ファイルの版（${v}）が名前の版（${NEW_VERSION}）と違います" ;;
  esac
  say "  ${name}（SHA-256 一致: ${want_sha}）"
}

# place_binary — $TMP/looptrack を $BIN に置く（同じ中身なら何もしない。置き換えは rename で一度に）
place_binary() {
  if [ -f "$BIN" ] && [ "$(sha256_of "$BIN")" = "$NEW_SHA" ]; then
    say "  $BIN は同じ版です（${NEW_VERSION}）"
    return
  fi
  if [ -f "$BIN" ]; then
    cp -p "$BIN" "$BIN.prev"
  fi
  install -m 0755 "$TMP/looptrack" "$BIN.install-$$"
  mv -f "$BIN.install-$$" "$BIN"
  say "  置いた: ${BIN}（${NEW_VERSION}）"
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
    say "AUTO_UPGRADE=$AUTO"
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
  S_AUTO=$(sed -n 's/^AUTO_UPGRADE=//p' "$STATE")
  [ "$S_AUTO" = on ] || S_AUTO=off # 以前の版の印（この行が無い）は off
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
  say "  更新: $ONE_LINER -s -- --upgrade"
  if [ "$METHOD" = systemd ] && [ "$AUTO" = on ]; then
    say "  自動の置き換え: 有効（1 日 1 回・署名を確かめて新しい版だけ。止める: $ONE_LINER -s -- --auto-upgrade off）"
  elif [ "$METHOD" = systemd ]; then
    say "  自動の置き換え: 無効（既定。有効にする: $ONE_LINER -s -- --auto-upgrade on。minisign が要る）"
  else
    say "  コンテナのイメージは自動では置き換えません（新しい版は管理画面の帯・looptrack doctor・起動時のログで知らせます）"
  fi
  say "  $DIR/.env の LOOPTRACK_SECRET_KEY を失うと全員の二段階認証が使えなくなります。別の場所に控えてください。"
}

show_configured() {
  METHOD=$S_METHOD
  DIR=$S_DIR
  AUTO=$S_AUTO
  [ -n "$PROJECT" ] || PROJECT=$S_PROJECT
  say "設定済みです（${STATE}）。何も変えていません。"
  say "  動かし方: ${S_METHOD}・版: ${S_VERSION}・設定: $DIR/.env"
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
    echo "cd $DIR && docker compose up -d（イメージは $ONE_LINER -s -- --upgrade で作る）"
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
  step "イメージ $IMAGE:${NEW_VERSION}（scratch に取得した実行ファイルを載せる）"
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
  say "  作成: $IMAGE:${NEW_VERSION}（$IMAGE:latest）"
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
# 表ができてからしか流せないので、「setup（migrate）が表を作る → 権限を与える → 起動」になる。
# ここで先に読んでみて、読めなければ管理用の資格情報を端末で尋ね、looptrack grants apply で権限を与えてから
# もう一度読む（黙って起動して 60 秒待たない）。与えられなければ、起動せずに止める。設定（.env）は残るので、
# もう一度実行すると setup を飛ばし、尋ねるところから続く。
# ついでに、読めたときは最初のプロジェクトの slug を控える（MCP の接続設定に出す）。
#
# 権限を与えるのは MySQL のときだけ。ここまで来ていれば setup（migrate）は繋がっているので、
# アプリ用の DSN だけが読めないのは「権限がまだ無い」か「接続先が違う」のどちらか。
# SQLite と、確認そのものができなかったとき（setpriv が無いなど）は、注意だけ出して進む
check_store_access() {
  if [ "$METHOD" = systemd ] && ! command -v setpriv >/dev/null 2>&1; then
    warn "setpriv が無いので、起動の前の保存先の確認を飛ばします（util-linux）"
    return 0
  fi
  step "保存先の確認（サービスと同じ利用者で読めるか）"
  if store_readable; then
    return 0
  fi
  if ! is_mysql; then
    warn "保存先を確かめられませんでした（このまま起動します）: $out"
    return 0
  fi
  retry="$ONE_LINER"
  if [ "$ACTION" = upgrade ]; then retry="$ONE_LINER -s -- --upgrade"; fi
  printf '%s\n' "$out" >&2
  step "MySQL の権限（アプリ用の利用者はまだ表を読めません。管理用の資格情報で権限を与えます）"
  say "  表ごとの GRANT は表ができてからしか流せないので、setup（表を作る）の後のここで与えます。"
  say "  管理用の資格情報は接続にだけ使い、保存しません。"
  if ! grants_apply; then
    die "MySQL の権限を与えられませんでした（上の出力）。サービスは起動していません。
設定（$DIR/.env）は残っています。資格情報を確かめて、もう一度実行してください（setup は飛ばし、管理用の資格情報を尋ねるところから続きます）:
  $retry
（自分で与えるなら: sudo sh -c 'set -a; . $DIR/.env; exec $BIN grants print' で GRANT 文を出し、管理用の資格情報で流してから、もう一度実行します）"
  fi
  if store_readable; then
    return 0
  fi
  printf '%s\n' "$out" >&2
  die "権限を与えた後も、MySQL の保存先をアプリ用の利用者で読めません（上の出力）。サービスは起動していません。
$DIR/.env の LOOPTRACK_DSN（アプリが使う接続先）が、権限を与えた DB・利用者と合っているかを確かめてください。
表を作るときの接続先（LOOPTRACK_SETUP_DSN・LOOPTRACK_SETUP_MIGRATE_DSN）とは別に指定しています。"
}

# store_readable — サービスと同じ利用者・設定で保存先を読む（読めたら最初のプロジェクトの slug を控える）。出力は $out
store_readable() {
  if out=$(app_run project list 2>&1); then
    say "  読めました"
    if [ -z "$PROJECT" ]; then
      # project list の 1 行目は見出し（SLUG PREFIX …）。2 行目の 1 列目が最初のプロジェクト
      PROJECT=$(printf '%s\n' "$out" | awk 'NR == 2 { print $1 }')
    fi
    return 0
  fi
  return 1
}

# grants_apply — .env の接続先（LOOPTRACK_DSN）に、管理用の資格情報で最小権限を与える（looptrack grants apply）。
# 資格情報は looptrack が端末（/dev/tty）で尋ねる（表示しない・保存しない）。端末が無いときは、systemd に限り
# LOOPTRACK_INSTALL_DB_ADMIN_USER・LOOPTRACK_INSTALL_DB_ADMIN_PASSWORD_FILE（1 行目がパスワード）で渡せる
grants_apply() {
  set -- grants apply
  [ "$YES" = 1 ] && set -- "$@" --yes
  if [ "$METHOD" = systemd ]; then
    if [ -n "${LOOPTRACK_INSTALL_DB_ADMIN_PASSWORD_FILE:-}" ]; then
      set -- "$@" --admin-user "${LOOPTRACK_INSTALL_DB_ADMIN_USER:-root}" --admin-password-file "$LOOPTRACK_INSTALL_DB_ADMIN_PASSWORD_FILE"
    fi
    (
      set -a
      # shellcheck disable=SC1090,SC1091
      . "$DIR/.env"
      set +a
      exec "$BIN" "$@"
    )
  else
    # コンテナの中から繋ぐ（compose のネットワークの接続先でも届くように）。尋ねるので端末を渡す
    ( : </dev/tty ) 2>/dev/null || { warn "管理用の資格情報を尋ねる端末がありません"; return 1; }
    compose run --rm --no-deps looptrack "$@" </dev/tty
  fi
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

# ---------------------------------------------------------------- 自動の置き換え（--auto-upgrade。既定は off）

# version_gt <a> <b> — 版 a が b より新しければ真（semver の順。先頭の v と +build は見ない。
# プレリリース（-rc.1 など）は同じ版の正式版より古く、識別子は数なら数として、それ以外は文字列として比べる）
version_gt() {
  awk -v a="$1" -v b="$2" '
    function norm(s) { sub(/^v/, "", s); sub(/[+].*$/, "", s); return s }
    function cmpid(x, y,    xn, yn) {
      xn = (x ~ /^[0-9]+$/); yn = (y ~ /^[0-9]+$/)
      if (xn && yn) return (x + 0 > y + 0) - (x + 0 < y + 0)
      if (xn) return -1
      if (yn) return 1
      return ("" x > "" y) - ("" x < "" y)
    }
    function cmpv(a, b,    i, ca, cb, pa, pb, xa, xb, na, nb, n, r) {
      a = norm(a); b = norm(b)
      ca = a; pa = ""; i = index(a, "-"); if (i > 0) { ca = substr(a, 1, i - 1); pa = substr(a, i + 1) }
      cb = b; pb = ""; i = index(b, "-"); if (i > 0) { cb = substr(b, 1, i - 1); pb = substr(b, i + 1) }
      split(ca, xa, "."); split(cb, xb, ".")
      for (i = 1; i <= 3; i++) { r = cmpid(xa[i] + 0 "", xb[i] + 0 ""); if (r) return r }
      if (pa == "" && pb == "") return 0
      if (pa == "") return 1
      if (pb == "") return -1
      na = split(pa, xa, "."); nb = split(pb, xb, ".")
      n = (na < nb) ? na : nb
      for (i = 1; i <= n; i++) { r = cmpid(xa[i], xb[i]); if (r) return r }
      return (na > nb) - (na < nb)
    }
    BEGIN { exit (cmpv(a, b) > 0) ? 0 : 1 }'
}

# check_auto_upgrade — 自動の置き換えを入れられるかを、何かを変える前に確かめる（on のときだけ。合わなければ止める）
check_auto_upgrade() {
  [ "$AUTO" = on ] || return 0
  if [ "$METHOD" = compose ]; then
    die "compose ではコンテナのイメージを自動では置き換えません（何も変えていません）。新しい版は管理画面の帯・looptrack doctor・起動時のログで知らせます。更新: $ONE_LINER -s -- --upgrade"
  fi
  [ "$NO_START" = 0 ] || die "--no-start と --auto-upgrade on は一緒に使えません（起動していないサーバを置き換えないため）。何も変えていません"
  have_systemd || die "systemd が動いていないので、自動の置き換え（systemd timer）を入れられません。何も変えていません"
  command -v minisign >/dev/null 2>&1 ||
    die "自動の置き換えは SHA256SUMS の署名の確認を必須にします。minisign を入れてください（apt-get install -y minisign）。何も変えていません"
  if ! command -v curl >/dev/null 2>&1 && ! command -v wget >/dev/null 2>&1; then
    die "自動の置き換えはこのスクリプトを取り直すので、curl か wget が要ります（apt-get install -y curl）。何も変えていません"
  fi
}

# warn_auto_mysql — MySQL では、DB の形を変える版を自動では置き換えないことを知らせる（.env がある時点で呼ぶ）
warn_auto_mysql() {
  if [ "$AUTO" = on ] && [ -f "$DIR/.env" ] && is_mysql; then
    warn "MySQL では、新しい版が DB の形を変える（未適用の migrate がある）ときは自動では置き換えません（migrate の後に権限を与え直す管理用の資格情報を無人では尋ねられず、MySQL の DB は控えから戻せないため）。サービスは今の版のまま動き、知らせは続きます。そのときは $ONE_LINER -s -- --upgrade を端末で実行してください"
  fi
}

# write_if_changed <一時ファイル> <置き場> <権限> — 中身が同じなら置かない
write_if_changed() {
  chmod "$3" "$1"
  if [ -f "$2" ] && cmp -s "$1" "$2"; then
    rm -f "$1"
  else
    mv -f "$1" "$2"
  fi
}

# save_installer_copy — timer が動かすこのスクリプトの写しを置く（root 0755）。手元のファイルで動かしたときはそのファイルを、
# 1 行（curl … | sh）で動かしたとき（$0 がファイルでない）は同じ URL から取り直したものを写す。timer の回は写しそのものが
# 動いているので中身は変わらない（写しを新しくするのは人が動かしたときだけ）
save_installer_copy() {
  mkdir -p "$AUTO_LIB"
  chmod 0755 "$AUTO_LIB"
  t="$AUTO_SH.install-$$"
  if [ -f "$0" ] && sed -n 2p "$0" | grep -q '^# looptrack-install-sh'; then
    cp "$0" "$t"
  else
    download "$SCRIPT_URL" "$t" || die "自動の置き換えが動かす install.sh の写しを取れません: $SCRIPT_URL"
    if ! sed -n 2p "$t" | grep -q '^# looptrack-install-sh' || ! sh -n "$t"; then
      rm -f "$t"
      die "取った install.sh（${SCRIPT_URL}）が install.sh の形をしていません。写しを置いていません"
    fi
  fi
  write_if_changed "$t" "$AUTO_SH" 0755
}

# unit_env <名前> — timer の service に写す環境変数の行（値があるときだけ。systemd の % を %% にする）。
# 取得元を差し替えて有効にしたとき（ミラー・テスト）は、自動の置き換えも同じ取得元から取る
unit_env() {
  eval "val=\${$1:-}"
  [ -n "$val" ] || return 0
  case $val in *[\"\\[:space:]]*) die "$1 に空白・引用符・バックスラッシュがあるので、自動の置き換えに写せません" ;; esac
  printf 'Environment="%s=%s"\n' "$1" "$(printf '%s' "$val" | sed 's/%/%%/g')"
}

# write_auto_files — 自動の置き換えのスクリプトの写し・service・timer を置く（何度行っても同じ結果）
write_auto_files() {
  save_installer_copy
  t="$AUTO_SVC.install-$$"
  {
    cat <<AUTOEOF
# install.sh が作成（--auto-upgrade on）。looptrack-upgrade.timer から動く。止める: $ONE_LINER -s -- --auto-upgrade off
# 動かすのは手元の写し ${AUTO_SH}（人が install.sh を動かしたときだけ新しくなる）。取るのは書庫だけで、署名を必須にする
[Unit]
Description=Looptrack server automatic upgrade (install.sh --upgrade --require-signature --only-newer)
Documentation=https://github.com/howashoji/looptrack/blob/main/docs/server/DEPLOY.md
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
AUTOEOF
    unit_env LOOPTRACK_INSTALL_REPO
    unit_env LOOPTRACK_INSTALL_ALLOW_HTTP
    unit_env LOOPTRACK_INSTALL_MINISIGN_PUBKEY
    say "ExecStart=/bin/sh $AUTO_SH --upgrade --require-signature --yes --only-newer"
  } >"$t"
  write_if_changed "$t" "$AUTO_SVC" 0644
  t="$AUTO_TIMER.install-$$"
  cat >"$t" <<'AUTOEOF'
# install.sh が作成（--auto-upgrade on）。1 日 1 回（時刻は 1 時間の幅でずらす。止まっていた間の回は起動した後に 1 回）
[Unit]
Description=Daily automatic upgrade of the Looptrack server

[Timer]
OnCalendar=daily
RandomizedDelaySec=1h
Persistent=true

[Install]
WantedBy=timers.target
AUTOEOF
  write_if_changed "$t" "$AUTO_TIMER" 0644
}

# apply_auto_upgrade — 設定（${AUTO}）どおりに timer を入れる / 外す（入れるのは systemd だけ。compose には置いていない）
apply_auto_upgrade() {
  if [ "$AUTO" = on ]; then
    step "自動の置き換え（looptrack-upgrade.timer）"
    write_auto_files
    systemctl daemon-reload
    systemctl enable --now looptrack-upgrade.timer >/dev/null 2>&1 ||
      die "looptrack-upgrade.timer を有効にできません（systemctl status looptrack-upgrade.timer）"
    say "  有効: 1 日 1 回、署名を確かめて新しい版に置き換えます（${AUTO_TIMER}・写し ${AUTO_SH}）"
    return
  fi
  if [ -e "$AUTO_TIMER" ] || [ -e "$AUTO_SVC" ] || [ -e "$AUTO_SH" ]; then
    step "自動の置き換えを外します"
    if have_systemd; then systemctl disable --now looptrack-upgrade.timer >/dev/null 2>&1 || true; fi
    rm -f "$AUTO_TIMER" "$AUTO_SVC" "$AUTO_SH"
    rmdir "$AUTO_LIB" 2>/dev/null || true
    if have_systemd; then systemctl daemon-reload; fi
    say "  無効にしました（looptrack-upgrade.timer と $AUTO_SH を外しました）"
  fi
}

# auto_state_line — 印の AUTO_UPGRADE だけを書き換える（入れた後のサーバの設定だけを変えるとき）
auto_state_line() {
  t="$STATE.install-$$"
  {
    grep -v '^AUTO_UPGRADE=' "$STATE" || true
    say "AUTO_UPGRADE=$AUTO"
  } >"$t"
  chmod 0644 "$t"
  mv -f "$t" "$STATE"
}

# auto_upgrade_only — 入れた後のサーバの自動の置き換えだけを変える（入れ直さない）
auto_upgrade_only() {
  METHOD=$S_METHOD
  DIR=$S_DIR
  AUTO=$AUTO_UPGRADE
  if [ "$S_STARTED" = no ]; then NO_START=1; fi
  check_auto_upgrade
  warn_auto_mysql
  apply_auto_upgrade
  auto_state_line
  say ""
  if [ "$AUTO" = on ]; then
    say "自動の置き換えを有効にしました（既定は無効）。止める: $ONE_LINER -s -- --auto-upgrade off"
  else
    say "自動の置き換えは無効です（既定）。新しい版は管理画面の帯・looptrack doctor・起動時のログで知らせます。更新: $ONE_LINER -s -- --upgrade"
  fi
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
  say "  控え: ${b}（止めた状態で写した SQLite）"
  RB_BACKUP=$b
}

# unattended — 無人の実行か（--yes で、尋ねる端末が無い。自動の置き換えの timer はこれ）
unattended() { [ "$YES" = 1 ] && ! (: </dev/tty) 2>/dev/null; }

# precheck_unattended_mysql — 無人の更新で、止める前に確かめる（MySQL だけ）。新しい版に未適用の migrate があれば置き換えない。
# MySQL を最小権限で使うと、migrate の後に新しい表の権限を与え直すには管理用の資格情報が要り、無人では尋ねられないため。
# また MySQL の DB は控えから戻せないので、migrate が DB を変える更新は無人では行わない（止めた後に戻せるのは実行ファイルだけ）
precheck_unattended_mysql() {
  is_mysql || return 0
  step "無人の更新の確認（新しい版が DB の形を変えるか）"
  rc=0
  out=$(
    set -a
    # shellcheck disable=SC1090,SC1091
    . "$DIR/.env"
    set +a
    exec "$TMP/looptrack" migrate --check
  ) 2>&1 || rc=$?
  case $rc in
    0) say "  未適用の migrate はありません（DB の形は変わりません）" ;;
    3)
      printf '%s\n' "$out" >&2
      die "新しい版 $NEW_VERSION は DB の形を変えます（上の未適用の migrate）。MySQL では、無人の更新（自動の置き換え）は migrate の後に権限を与え直せないので置き換えません。サービスは今の版 $S_VERSION のまま動いています。端末で $ONE_LINER -s -- --upgrade を実行してください"
      ;;
    *)
      printf '%s\n' "$out" >&2
      die "新しい版で DB の適用記録を確かめられません（上の出力）。置き換えていません。サービスは今の版 $S_VERSION のまま動いています"
      ;;
  esac
}

# rollback_upgrade — 無人の更新で、止めた後に失敗したとき（die から呼ぶ）: 前の実行ファイルに戻し、SQLite なら DB を止めた直後の控えに戻し、
# 前の版で起動し直す。MySQL は置き換えの前に「DB の形を変えない」ことを確かめているので（precheck_unattended_mysql）、実行ファイルだけを戻す。
# 印（install.conf）は前の版のまま。呼んだ die が 0 でない終了コードで終わる（timer の service は失敗として journal に残る）
rollback_upgrade() {
  ROLLBACK=0
  printf '\n==> %s\n' "前の版 ${S_VERSION} に戻して起動し直します（無人の更新なので、サービスを止めたままにしない）" >&2
  systemctl stop looptrack >/dev/null 2>&1 || true
  # 置き換えは rename で一度に（実行中のプロセスが残っていても「Text file busy」にならない。place_binary と同じ）
  if [ "$RB_BIN" = 1 ] && [ -f "$BIN.prev" ]; then
    if ! { cp -p "$BIN.prev" "$BIN.rollback-$$" && mv -f "$BIN.rollback-$$" "$BIN"; }; then
      rm -f "$BIN.rollback-$$"
      printf '%s: エラー: 前の実行ファイル %s に戻せません（手で: mv %s %s && systemctl start looptrack）\n' "$PROG" "$BIN.prev" "$BIN.prev" "$BIN" >&2
      return
    fi
  fi
  rdb=$(sqlite_host_path)
  if [ -n "$rdb" ] && [ -n "$RB_BACKUP" ] && [ -f "$RB_BACKUP/$(basename "$rdb")" ]; then
    rm -f "$rdb" "$rdb-wal" "$rdb-shm"
    for f in "$RB_BACKUP/$(basename "$rdb")" "$RB_BACKUP/$(basename "$rdb")-wal" "$RB_BACKUP/$(basename "$rdb")-shm"; do
      if [ -f "$f" ]; then cp -p "$f" "$(dirname "$rdb")/"; fi
    done
    restrict_sqlite "$rdb"
    printf '  DB を控え %s に戻しました\n' "$RB_BACKUP" >&2
  fi
  systemctl daemon-reload >/dev/null 2>&1 || true
  if ! systemctl start looptrack; then
    printf '%s: エラー: 前の版 %s でも起動できません（journalctl -u looptrack）\n' "$PROG" "$S_VERSION" >&2
    return
  fi
  i=0
  while ! health_once; do
    i=$((i + 1))
    if [ "$i" -ge 60 ]; then
      printf '%s: エラー: 前の版 %s でも 60 秒待って /healthz が 200 を返しません（journalctl -u looptrack）\n' "$PROG" "$S_VERSION" >&2
      return
    fi
    sleep 1
  done
  printf '%s: 前の版 %s に戻して起動し直しました（新しい版 %s は入れていません）。上の理由を直すか、端末で %s -s -- --upgrade を実行してください\n' \
    "$PROG" "$S_VERSION" "$NEW_VERSION" "$ONE_LINER" >&2
}

upgrade() {
  load_state || die "install.sh で入れた印（${STATE}）がありません。先に install.sh で入れてください"
  METHOD=$S_METHOD
  DIR=$S_DIR
  PROJECT=$S_PROJECT # 印に書き戻すため（更新では変わらない）
  AUTO=${AUTO_UPGRADE:-$S_AUTO}
  check_auto_upgrade # 自動の置き換えを入れられない（compose・minisign なし）なら、何かを変える前に止める
  get_binary
  if [ "$NEW_VERSION" = "$S_VERSION" ] && [ -f "$BIN" ] && [ "$(sha256_of "$BIN")" = "$NEW_SHA" ]; then
    say "すでに $NEW_VERSION です。何も変えていません。"
    # 人が動かしたとき（無人でない）は、自動の置き換えの写しもこのスクリプトにそろえる
    if [ -n "$AUTO_UPGRADE" ] || { [ "$AUTO" = on ] && ! unattended; }; then
      warn_auto_mysql
      apply_auto_upgrade
      auto_state_line
    fi
    return
  fi
  if [ "$ONLY_NEWER" = 1 ] && ! version_gt "$NEW_VERSION" "$S_VERSION"; then
    say "取得した版 $NEW_VERSION は入っている版 $S_VERSION より新しくないので、置き換えません（--only-newer）。何も変えていません。"
    return
  fi
  read_env_info
  dsn=$(env_get LOOPTRACK_DSN "$DIR/.env")
  # MySQL で、テーブルを作れる利用者を別に使うときは LOOPTRACK_SETUP_MIGRATE_DSN（setup と同じ）
  mdsn=${LOOPTRACK_SETUP_MIGRATE_DSN:-$dsn}
  if [ "$METHOD" = systemd ]; then
    if unattended; then
      UNATTENDED=1
      precheck_unattended_mysql
    fi
    step "停止"
    systemctl stop looptrack
    # 無人の更新は、ここから後の失敗（die）で前の版に戻して起動し直す（rollback_upgrade）
    if [ "$UNATTENDED" = 1 ]; then ROLLBACK=1; fi
    db=$(sqlite_host_path)
    if [ -n "$db" ]; then
      backup_sqlite "$db"
      restrict_sqlite "$db"
    fi
    step "実行ファイル"
    place_binary
    RB_BIN=1
    step "マイグレーション（$SVC_USER として）"
    (
      set -a
      # shellcheck disable=SC1090,SC1091
      . "$DIR/.env"
      set +a
      LOOPTRACK_DSN=$mdsn exec setpriv --reuid "$SVC_USER" --regid "$SVC_USER" --init-groups "$BIN" migrate
    ) || die "マイグレーションに失敗しました。前の実行ファイルは $BIN.prev にあります（戻すときは mv $BIN.prev $BIN && systemctl start looptrack）。上に「適用:」（英語では Applied:）の行があれば、前の版はその DB で起動しないので、DB もこの更新の前の控え（SQLite は上の「控え:」、MySQL は自分で取った控え）に戻してください"
    check_store_access # 表が増えた更新では権限を与え直すまで読めない（ここで尋ねて与え直す）
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
    fi || die "マイグレーションに失敗しました（前のイメージは $IMAGE:${S_VERSION}。compose.yaml の image を戻して up -d）。上に「適用:」（英語では Applied:）の行があれば、前の版はその DB で起動しないので、DB もこの更新の前の控え（SQLite は上の「控え:」、MySQL は自分で取った控え）に戻してください"
    check_store_access # 表が増えた更新では権限を与え直すまで読めない（ここで尋ねて与え直す）
    if [ -n "$db" ]; then restrict_sqlite "$db"; fi
    step "起動"
    compose up -d
  fi
  wait_health
  ROLLBACK=0
  NO_START=0
  warn_auto_mysql
  apply_auto_upgrade # 設定どおりに timer を入れ直す / 外す（人が動かしたときは写しをこのスクリプトにそろえる）
  write_state
  say ""
  say "更新しました: $S_VERSION → ${NEW_VERSION}（データはそのままです）"
}

# ---------------------------------------------------------------- 外す

uninstall() {
  load_state || die "install.sh で入れた印（${STATE}）がありません"
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
  AUTO=off
  apply_auto_upgrade # 自動の置き換え（timer）も外す
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
      say "  $CONF_DIR/.env・${DATA_DIR}（利用者 $SVC_USER も残しています）"
    else
      say "  ${DIR}（.env・compose.yaml・data）とイメージ $IMAGE:*"
    fi
    say "  もう一度 install.sh を実行すると、残した設定で入れ直します。消すときは --uninstall --purge。"
  fi
}

# ---------------------------------------------------------------- 入れる

install_main() {
  if load_state; then
    [ -z "$METHOD" ] || [ "$METHOD" = "$S_METHOD" ] || die "すでに $S_METHOD で入っています（${STATE}）。変えるときは --uninstall してから"
    if [ -n "$AUTO_UPGRADE" ]; then
      auto_upgrade_only
      return
    fi
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
  AUTO=${AUTO_UPGRADE:-off} # 既定は off（利用者の決定）
  check_auto_upgrade
  get_binary
  if [ "$METHOD" = systemd ]; then
    install_systemd "$@"
  else
    install_compose "$@"
  fi
  if [ "$AUTO" = on ]; then
    warn_auto_mysql
    apply_auto_upgrade
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
    say "インストールが終わりました（${METHOD}・${NEW_VERSION}）。"
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
  # Linux 以外では、何かを尋ねたり変えたりする前に止める
  detect_platform
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
