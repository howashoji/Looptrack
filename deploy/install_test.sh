#!/bin/bash
# deploy/install.sh のテスト。手元（Mac・Linux）の Docker で、まっさらな Ubuntu LTS・Debian のコンテナに入れて確かめる。
#
#   bash deploy/install_test.sh                 systemd なしの確認（ubuntu:24.04・ubuntu:22.04・debian:12）+ systemd 入りのコンテナで実起動
#   INSTALL_TEST_SYSTEMD=0 bash deploy/install_test.sh    systemd の実起動を省く（apt で systemd を入れるイメージを作るため数分かかる）
#   INSTALL_TEST_COMPOSE=1 bash deploy/install_test.sh    compose の実起動も試す（Docker のソケットをコンテナに渡す。下の注意）
#   INSTALL_TEST_IMAGES="debian:12" bash deploy/install_test.sh
#   INSTALL_TEST_INTERACTIVE=0 …                           対話の確認（script の疑似端末から setup の問いに答える）を省く
#
# 材料の looptrack は deploy/release/dist.sh で作る（版 v0.0.0-installtest1 と …2。Docker の CPU に合わせた linux/<arch> だけ）。
# 取得元は 2 つの形: /dist・/dist2 は dist.sh build の出力（素の実行ファイル。install.sh と grants.sql もその中に入る。
# サーバの配布ディレクトリと同じ形）、/gh は GitHub Releases と同じ並び（<リポジトリ>/releases/{latest/download,download/<版>}/ に
# 書庫 looptrack_<版>_linux_<arch>_server.tar.gz・NOTICE・OFL・SHA256SUMS。latest は …1）。README の 1 行は、
# 手元の HTTP サーバの raw/deploy/install.sh と gh/ に LOOPTRACK_INSTALL_REPO で読み替えて通す。
# コンテナの /src はリポジトリの deploy/。
#
# 確かめること:
#   plain（systemd なし・各イメージ）: --from なしは <リポジトリ>/releases/latest/download から取る / Linux 以外では何も変えずに止まる /
#     書庫（Releases の形）: 1 バイト変えた書庫・署名の不一致・中の実行ファイルが SHA256SUMS の行と違う書庫では何も入れない、
#     照合してから展開した looptrack を置く（素の実行ファイルは取らない）・--version でその版 /
#     dist.sh build の出力のディレクトリをそのまま取得元にする（install.sh と grants.sql が入り
#     SHA256SUMS で照合できる・配布物の中の install.sh で入る・--upgrade も同じ並びから）/ SHA-256 の不一致で何も入れない / SHA256SUMS の署名（minisign）: 合わなければ・--require-signature で
#     minisign か署名が無ければ何も入れない、確かめられないときは注意を出して進む（コンテナの minisign は呼び出しを確かめる偽物。
#     本物の署名と検証の形は手元の minisign で dist.sh sign-sums・verify が確かめる） / setup の失敗で何も残さない / 中断（TERM）で一時ファイルを残さない /
#     systemd --no-start で .env・unit・利用者・SQLite の置き場と権限・MCP の接続設定（setup と同じ形）/
#     2 回目は「設定済み」で .env を書き換えない /
#     手で serve を起動して /healthz と管理者 / --uninstall でデータを残し、入れ直すと同じデータで動く / --purge で消える /
#     compose --no-start で compose.yaml・.env・data の所有者 / 2 回目は「設定済み」/
#     クライアントに配る looptrack（配布ディレクトリ）: 入れると .env に LOOPTRACK_DIST_DIR を足し（compose は compose.yaml の ./dist:/dist:ro）、
#     取得元の SHA256SUMS にある対象（…1 は linux/<arch> だけ）を照合して置き、無い対象は注意を出す・手で起動した serve は不足をログに出す /
#     自動の置き換え: 既定は AUTO_UPGRADE=off で timer を置かない / compose・--no-start・systemd なしの --auto-upgrade on は
#     何も変えずに止まる / 値の誤り・--upgrade なしの --only-newer は止まる
#   systemd（ubuntu:24.04 + systemd）: README の 1 行（curl … | sh）で書庫を取って実起動まで・/healthz・unit のサンドボックスの評価・2 回目・DB の権限（600・警告なし）/
#     ブラウザと同じ手順のログイン（パスワード → 二段階認証の登録 → 一覧の画面。curl と openssl で TOTP を計算）を 127.0.0.1 と、
#     install.sh の設定例のままの Caddy・nginx（https・公開 URL im.example.com）の後ろで / コンテナの再起動の後にサービスが上がる /
#     .env を sh で読めないとき（値に括弧を含む引用なし、または X=a b のように構文としては通るが別のコマンドとして実行される形。
#     新規の入れ方・--upgrade の両方。許した形（KEY='…'・$ ` \ " を含まない KEY="…"・安全な引用なしの値）以外の行はすべて止める:
#     二重引用符の中の変数の展開・キーの形でない行（行番号を示す）も。setup が書く単引用符の .env は必ず通る）: 値を出さず（目印の文字列が出力に 1 回も出ない）、
#     「.env を sh で読めません」とキーの名前を示し、権限の段（MySQL の権限）にも進まず、--upgrade ではサービスを止める前に止まる /
#     対照: 同じ値を単引用符で囲むと検査を通る（新規は権限の段まで進み、--upgrade は最後まで更新される）/
#     --upgrade（版が変わり、データと控えが残り、0644 の DB を 600 に直して警告が消え、同じ二段階認証でログインできる）・
#     同じ版の --upgrade --version（書庫の中身が入っているものと同じなら何もしない）・--uninstall /
#     --upgrade の後の配布ディレクトリが新しい版の 6 対象（SHA256SUMS と一致）になり、GET /api/v1/dist の binaries も同じ 6 対象・同じ SHA-256、
#     古い looptrack の導入済み通知（POST /install）の応答に【配布スクリプトの更新】が出る（対照: 同じ版の looptrack には出ない・
#     更新の前の起動は配布物の不足をログに出し、更新の後の起動は出さない）
#   mysql（上の systemd のコンテナ + テスト専用の MySQL のコンテナ。共有の開発用 MySQL は使わない）: 最小権限の構成で、
#     管理用の資格情報が合わなければ権限を与えず起動せずに止まり、.env を残す / もう一度実行すると尋ねるところから続き、
#     アプリ用の利用者を作って権限を与え（DB 名・利用者名は im・im_app 以外）、起動と動作確認まで 1 回で進む /
#     尋ねた管理用のパスワードが /etc/looptrack・/var/lib/looptrack・/opt・インストーラの出力・シェルの履歴・ps の引数に残らない
#     （対照: そのパスワードで与えた権限が SHOW GRANTS に出る）/ 権限の欠けた表がある状態の --upgrade も尋ねて与え直して起動する /
#     DB が無い構成: setup が管理用の資格情報を 1 回だけ尋ね、DB を作るかを確かめてから DB・アプリ用の利用者を作り、migrate して起動まで進む・
#     「作らない」と答えると何も作らず（.env も書かず）、流す CREATE DATABASE を示して止まる / 対照: 既にある DB は作り直さない（表と行が残る）
#   rc2（同じコンテナ）: 1.0.0-rc.2 の install.sh（開発側のタグ）で入れたサーバを、新しいインストーラの --upgrade --version で上げる
#   auto-upgrade（同じコンテナ）: 自動の置き換え（--auto-upgrade。既定は off）。入れた直後は off で timer が無い / minisign が無ければ
#     on は止まり何も変えない / curl も wget も無ければ on は止まり、理由は新しい版の書庫・署名を取るため（取り直すとは書かない）/ 1 行（curl … | sh）の on で looptrack-upgrade.timer が有効になり、install.sh の写し（root 755）・service・
#     timer が置かれる（service は写しを動かし、取り直さない）/ timer の service を動かすと新しい版（/gh2 の latest）に上がり、
#     設定は on のまま・写しは書き換えない / もう一度動かしても何もしない / 人が --upgrade を動かすと写しをそろえる /
#     timer の更新（書庫・署名必須）でも配布ディレクトリが 6 対象（windows は zip から取り出す）になり署名も置く /
#     古い版の取得元（/gh の latest）では版を下げない（--only-newer。配布ディレクトリも変えない）/ off で timer を止めて 3 つのファイルを外す / on のまま --uninstall すると外れる
#   mysql-auto-*（MySQL の最小権限）: 無人の更新（--yes・端末なし。timer と同じ）で、新しい版に未適用の migrate があれば止めずに置き換えない /
#     止めた後に失敗（権限の欠けた表）すれば前の版に戻して起動し直し、0 でない終了コード / 対照: 権限がそろっていれば置き換わる /
#     表を作る利用者（LOOPTRACK_SETUP_MIGRATE_DSN）を渡さず、アプリ用の利用者（schema_migrations に SELECT だけ）で migrate する形でも、
#     未適用が 0 件なら置き換わる（手動の --upgrade と、timer の service を起動する無人の更新。service の環境にはこの変数が無い）
#   mysql-fakemig-*（MySQL の最小権限）: DB の形を変える新しい版（…3。偽の migrate 9001 を足したソースの写しから作る /dist3）を
#     端末（script の疑似端末）の --upgrade で入れる。(a) 表を作る接続先（LOOPTRACK_SETUP_MIGRATE_DSN）が無い → 止める前に未適用の一覧と
#     理由を示して続けるかを尋ね、既定の答えで 0 でない終了。サービスは起動し直さず、実行ファイル・印・版・DB は変わらない。--yes でも
#     尋ねずに同じく止まる / (b) 対照: 同じ版・同じ DB の状態から、root だけが読む .env から sh -c の中で組んだ接続先と、umask 077 の
#     一時ファイルの管理用のパスワード（LOOPTRACK_INSTALL_DB_ADMIN_PASSWORD_FILE）を渡すと、尋ねずに上がる（偽の migrate が適用され、
#     欠けた表の権限を与え直し、アプリ用の利用者で読め、/healthz が 200。一時ファイルは消え、パスワードは出力・/tmp・設定に残らない）
#     （コンテナの minisign は呼び出しを確かめる偽物。本物の署名と検証の形は手元の minisign で dist.sh sign-sums・verify が確かめる）
#   auto-rollback（同じコンテナ・SQLite）: 無人の更新で止めた後に、新しい版が起動しない（unit の追加設定で …2 の起動だけを失敗させる）・
#     daemon-reload の失敗・die を通らない裸のコマンドの失敗・中断（TERM）のどれでも、前の版に 1 回だけ戻して起動し直し、
#     理由を 1 行残して 0 でない終了コードで終わる（配布ディレクトリも前の版の配布に戻す）/ 対照: 端末のある手動の --upgrade は戻さない（サービスは止まったまま）
#   port-taken（同じコンテナ）: looptrack を止めた状態の 127.0.0.1:8090/looptrack/healthz に別のプロセス（nginx の偽の応答者）が 200 を返すと、
#     初回・入れ直しの install と手動の --upgrade は looptrack を起こす前に 0 でない終了コードで止まり、「別のプロセス」とポートを示し、
#     完了（「インストールが終わりました」「更新しました」）と 200 OK を出さない / 無人の --upgrade は戻しても「戻して起動し直しました」を出さず、
#     起動を確かめられないと示して 0 でない終了コード / 対照: 偽の応答者を止めると同じ install・--upgrade が完了まで進む /
#     起動の確認は 200 に加えて、ポートの待ち受けを持つ PID とサービスの MainPID を突き合わせる（unit の追加設定の ExecStartPre で
#     起動の直前に偽の応答者を起こすと、起こす前の確認は通り、200 でも「別のプロセス」と示して完了を出さない。対照: 起こさなければ完了）
#   compose-mysql（任意。compose と一緒に）: compose + テスト専用の MySQL（ネットワーク im0151-cnet。compose のコンテナは
#     テストが置く compose.override.yaml で同じネットワークに入る）で入れ、端末の --upgrade で DB の形を変える新しい版（…3）を入れようとすると、
#     止める前の確認が新しい版のイメージで動き（新しい版にしか無い偽の migrate の名前が出る）、既定の答えで止まる。
#     コンテナ（ID・起動時刻・イメージ）・latest のタグ・実行ファイル・印・版は変わらない
#   compose（任意）: イメージ作成・イメージの /NOTICE（looptrack licenses と同じ）・docker compose up・/healthz・DB の権限・
#     ログイン・--upgrade（0644 の DB を直す・更新の後のログイン）・--uninstall --purge
#     compose.yaml の ./data を Docker が解決できるよう、ホストの一時ディレクトリを同じパスでコンテナに入れる。
#     container_name が looptrack に固定のため、手元に looptrack という名前のコンテナがあると走らない。イメージ名は imtest0151（im:latest は触らない）。
#
# 作ったコンテナ・イメージは終わると消す（ベースイメージ im0151-systemd-base・im0151-compose-base は残す。INSTALL_TEST_KEEP_BASE=0 で消す）。
# 確認の書き方（A && ok || ng）と sh -c の単引用符は意図どおり
# shellcheck disable=SC2015,SC2016,SC2329
set -euo pipefail

# ---------------------------------------------------------------- コンテナの中（--in-container <場面>）

if [ "${1:-}" = --in-container ]; then
  scenario=$2
  I="sh /src/install.sh"
  # looptrack の文面の言語を固定する（既定は英語。下の確認は日本語の文面で見る）。
  # systemd のサービスはこの環境を受け取らないので、サービスのログは両方の言語の文面で見る（PERM_WARN）
  export LOOPTRACK_LANG=ja
  fails=0
  ok() { echo "  ok: $*"; }
  ng() {
    echo "  NG: $*" >&2
    fails=$((fails + 1))
  }
  check() { # <説明> <コマンド…>
    local d=$1
    shift
    if "$@" >/dev/null 2>&1; then ok "$d"; else ng "$d"; fi
  }
  contains() { grep -q -- "$2" <<<"$1"; }
  # wait_for <ファイル> <文字列> <秒> — ファイルに文字列が出るまで待つ（上限つき）
  wait_for() {
    local i=0
    while [ "$i" -lt "$3" ]; do
      grep -q -- "$2" "$1" 2>/dev/null && return 0
      sleep 1
      i=$((i + 1))
    done
    return 1
  }
  # tty_run <出力> <コマンド> [<問いの文字列> <答え>]… — script の疑似端末でコマンドを動かし、問いが出たら順に答える
  # （答えの空文字は既定の答え。管理用の資格情報を尋ねない場面用。全体は timeout で上限を持たせる）
  tty_run() {
    local log=$1 cmd=$2 pid rc=0
    shift 2
    rm -f /tmp/tty.in "$log"
    mkfifo /tmp/tty.in
    timeout 600 script -qfec "$cmd" "$log" </tmp/tty.in >/dev/null 2>&1 &
    pid=$!
    exec 7>/tmp/tty.in
    while [ $# -ge 2 ]; do
      wait_for "$log" "$1" 300 || break
      printf '%s\r' "$2" >&7
      shift 2
    done
    wait "$pid" || rc=$?
    exec 7>&-
    rm -f /tmp/tty.in
    return "$rc"
  }
  printf '%s\n' 'correct-horse-battery' >/root/pw
  chmod 600 /root/pw
  SETUP_BASE=(--store sqlite --public-url https://im.example.com --admin-login alice --admin-name Alice --admin-password-file /root/pw --two-factor required)
  SETUP=("${SETUP_BASE[@]}" --project web --project-name 'Web サイト')
  MCP_URL=https://im.example.com/looptrack/mcp # MCP の接続設定に出る URL
  admin() { # 管理コマンドを looptrack の利用者で（install.sh が案内する形）
    sh -c 'set -a; . /etc/looptrack/.env; exec setpriv --reuid looptrack --regid looptrack --init-groups /usr/local/bin/looptrack "$@"' _ "$@"
  }
  nothing_installed() {
    [ ! -e /usr/local/bin/looptrack ] && [ ! -e /etc/looptrack/install.conf ] && [ ! -e /etc/looptrack/.env ] &&
      [ -z "$(ls /tmp/looptrack-install.* 2>/dev/null)" ]
  }
  PERM_WARN='本人以外も読めます\|can be read by users other than you' # looptrack serve が SQLite の DB の権限が広いときに出す警告（ja・en）
  # journal の検索は、出力を受け取ってから grep する（journalctl … | grep -q は、pipefail の下で grep が先に終わると journalctl が
  # SIGPIPE で 0 でない終了になり、一致したのに「無い」と読まれる。「出ない」側の確認では、一致しても ok になる）
  # クライアントに配る looptrack（配布ディレクトリ）。serve が配布物の遅れを知らせるログの文面（ja・en）
  DISTD=/usr/local/share/looptrack/dist
  DIST_LAG='クライアントに配る looptrack\|looptrack distributed to clients'
  case $(uname -m) in x86_64 | amd64) CARCH=amd64 ;; *) CARCH=arm64 ;; esac
  # dist_matches <版> <取得元の SHA256SUMS> <対象の数> — 配布ディレクトリの SHA256SUMS が取得元のものと同じで、実行ファイルは
  # その版のものだけが <対象の数> あり、どれも SHA256SUMS の行と SHA-256 が一致する。合わなければ理由を出して 1
  dist_matches() {
    local v=$1 sums=$2 n=$3 f name want got cnt=0
    cmp -s "$DISTD/SHA256SUMS" "$sums" || { echo "SHA256SUMS が取得元のものと違う"; return 1; }
    for f in "$DISTD"/looptrack_*; do
      [ -e "$f" ] || continue
      name=${f##*/}
      case $name in "looptrack_${v}_"*) ;; *) echo "ほかの版のファイル: $name"; return 1 ;; esac
      want=$(awk -v n="$name" '{ m = $2; sub(/^\*/, "", m); if (m == n) print $1 }' "$sums")
      got=$(sha256sum "$f" | cut -d' ' -f1)
      [ -n "$want" ] && [ "$got" = "$want" ] || { echo "$name: 期待 ${want:-（行が無い）}・実際 $got"; return 1; }
      cnt=$((cnt + 1))
    done
    [ "$cnt" = "$n" ] || { echo "実行ファイルが $cnt 個（$n 個のはず）"; return 1; }
  }
  # dist_expected — 配布ディレクトリの実行ファイルを「名前 SHA-256」の行で（名前順）
  dist_expected() { (cd "$DISTD" && sha256sum looptrack_* | awk '{ print $2, $1 }' | sort); }
  # api_binaries <トークン> — GET /api/v1/dist の binaries を「名前 SHA-256」の行で（名前順。値はトークンを出さずに使う）
  api_binaries() {
    curl -fsS -H "Authorization: Bearer $1" http://127.0.0.1:8090/looptrack/api/v1/dist |
      grep -o '"name":"looptrack_[^"]*","os":"[^"]*","arch":"[^"]*","version":"[^"]*","sha256":"[0-9a-f]*"' |
      sed 's/^"name":"\([^"]*\)".*"sha256":"\([0-9a-f]*\)"$/\1 \2/' | sort
  }
  # post_install <トークン> <looptrack の版> — 古い / 新しい looptrack のクライアントの導入済み通知（hook）。応答（hook summary）を出す
  post_install() {
    curl -fsS -H "Authorization: Bearer $1" -H 'Content-Type: application/json' -H 'Accept-Language: ja' -X POST \
      -d "{\"agent\":\"claude-code\",\"trigger\":\"hook\",\"files\":{},\"host\":\"h\",\"workspace\":\"w\",\"client\":{\"version\":\"$2\",\"os\":\"linux\",\"arch\":\"$CARCH\"}}" \
      http://127.0.0.1:8090/looptrack/api/v1/projects/web/install
  }

  # ---- ブラウザと同じ手順のログイン（curl と openssl がある場面だけ。受け入れ条件「公開 URL にログインできる」）
  # 二段階認証の状態は ${TOTP_STATE}（「<Base32 の秘密> <最後に使ったステップ>」）に持ち越す（同じステップの確認コードは再利用できないため）
  TOTP_STATE=${TOTP_STATE:-/root/totp.state}
  totp() { # <Base32 の秘密> <ステップ> — RFC 6238（SHA-1・30 秒・6 桁。internal/auth/totp.go と同じ）
    local key mac off
    key=$(printf '%s' "$1" | base32 -d | od -An -v -tx1 | tr -d ' \n')
    mac=$(printf '%b' "$(printf '%016x' "$2" | sed 's/../\\x&/g')" |
      openssl dgst -sha1 -mac HMAC -macopt "hexkey:$key" -binary | od -An -v -tx1 | tr -d ' \n')
    off=$((16#${mac:39:1}))
    printf '%06d' $(((16#${mac:$((off * 2)):8} & 0x7fffffff) % 1000000))
  }
  cookie_of() { sed -n "s/^[Ss]et-[Cc]ookie: $1=\\([^;]*\\).*/\\1/p" "$2" | tr -d '\r' | tail -n 1; }
  header_of() { sed -n "s/^$1: //Ip" "$2" | tr -d '\r' | tail -n 1; }
  # web_login <URL の接頭辞（…/looptrack）> [curl の引数…] — パスワード → 二段階認証（初回は登録・2 回目からは確認コード）→ 一覧の画面が 200。
  # 初回は違うパスワードが 401 になることも確かめる。失敗の理由を標準出力に出して 1 を返す
  web_login() {
    local u=$1 h=/tmp/login.h b=/tmp/login.b code lc csrf sess loc secret last step path
    shift
    code=$(curl -s "$@" -D $h -o $b -w '%{http_code}' "$u/login") || true
    [ "$code" = 200 ] || { echo "GET /login: $code"; return 1; }
    lc=$(cookie_of looptrack_login_csrf $h)
    csrf=$(sed -n 's/.*name="csrf" value="\([^"]*\)".*/\1/p' $b | head -n 1)
    [ -n "$lc" ] && [ -n "$csrf" ] || { echo "ログイン画面に CSRF がない"; return 1; }
    if [ ! -f "$TOTP_STATE" ]; then # 初回だけ（失敗は 15 分に 5 回で締め出されるため、何度も試さない）
      code=$(curl -s "$@" -o /dev/null -w '%{http_code}' -H "Cookie: looptrack_login_csrf=$lc" --data-urlencode login=alice \
        --data-urlencode password=wrong-password --data-urlencode "csrf=$csrf" "$u/login") || true
      [ "$code" = 401 ] || { echo "違うパスワードが $code"; return 1; }
    fi
    code=$(curl -s "$@" -D $h -o /dev/null -w '%{http_code}' -H "Cookie: looptrack_login_csrf=$lc" --data-urlencode login=alice \
      --data-urlencode password=correct-horse-battery --data-urlencode "csrf=$csrf" "$u/login") || true
    sess=$(cookie_of looptrack_session $h)
    loc=$(header_of location $h)
    [ "$code" = 303 ] && [ -n "$sess" ] || { echo "パスワードの送信: $code"; return 1; }
    secret="" last=0
    if [ -f "$TOTP_STATE" ]; then read -r secret last <"$TOTP_STATE"; fi
    case $loc in
      */login/totp/setup*) path=/login/totp/setup ;;
      */login/totp*) path=/login/totp ;;
      *) echo "パスワードの後の行き先: $loc"; return 1 ;;
    esac
    code=$(curl -s "$@" -o $b -w '%{http_code}' -H "Cookie: looptrack_session=$sess" "$u$path") || true
    [ "$code" = 200 ] || { echo "GET $path: $code"; return 1; }
    csrf=$(sed -n 's/.*name="csrf" value="\([^"]*\)".*/\1/p' $b | head -n 1)
    if [ $path = /login/totp/setup ]; then
      secret=$(sed -n 's/.*<code data-secret>\([A-Z2-7]*\)<\/code>.*/\1/p' $b)
      [ -n "$secret" ] || { echo "登録画面に秘密がない"; return 1; }
      step=$(($(date +%s) / 30))
    else
      [ -n "$secret" ] || { echo "二段階認証の秘密を持ち越していない"; return 1; }
      # 使用済みのステップより後のコードを使う（前後 1 ステップまで受け付けるので、次のステップのコードを先に使える）
      while step=$(($(date +%s) / 30 + 1)) && [ "$step" -le "$last" ]; do sleep 1; done
    fi
    code=$(curl -s "$@" -D $h -o /dev/null -w '%{http_code}' -H "Cookie: looptrack_session=$sess" --data-urlencode "code=$(totp "$secret" "$step")" \
      --data-urlencode "csrf=$csrf" "$u$path") || true
    sess=$(cookie_of looptrack_session $h)
    [ "$code" = 303 ] && [ -n "$sess" ] || { echo "確認コードの送信（${path}）: $code"; return 1; }
    echo "$secret $step" >"$TOTP_STATE"
    code=$(curl -s "$@" -o $b -w '%{http_code}' -H "Cookie: looptrack_session=$sess" "$u/") || true
    [ "$code" = 200 ] || { echo "ログイン後の GET /: $code"; return 1; }
    code=$(curl -s "$@" -o /dev/null -w '%{http_code}' "$u/") || true
    [ "$code" = 303 ] || { echo "ログインしていない GET / が ${code}（ログイン画面に送らない）"; return 1; }
    [ $path = /login/totp/setup ] && echo "（二段階認証を登録してログイン）" || echo "（確認コードでログイン）"
  }

  case $scenario in
    plain)
      echo "== 取得元（--from なしは <リポジトリ>/releases/latest/download）"
      out=$($I --from-server https://im.example.com --yes 2>&1) && ng "--from-server を受け付けた" || true
      contains "$out" "不明な引数です: --from-server" && ok "--from-server は受け付けない" || ng "--from-server の表示: $out"
      out=$(LOOPTRACK_INSTALL_REPO=/nonexistent $I --yes --method systemd --no-start 2>&1) && ng "取得元が無いのに通った" || true
      contains "$out" "/nonexistent/releases/latest/download/SHA256SUMS" && ok "--from なしは <リポジトリ>/releases/latest/download から取る" || ng "取得元なしの表示: $out"
      out=$(LOOPTRACK_INSTALL_REPO=/nonexistent $I --version v9.9.9 --yes --method systemd --no-start 2>&1) && ng "取得元が無いのに通った（--version）" || true
      contains "$out" "/nonexistent/releases/download/v9.9.9/SHA256SUMS" && ok "--version は <リポジトリ>/releases/download/<版> から取る" || ng "--version の表示: $out"
      check "何も入れていない（取得元なし）" nothing_installed

      echo "== Linux 以外では何も変えずに止まる"
      mkdir -p /tmp/fakeuname
      printf '#!/bin/sh\nif [ "$1" = -s ]; then echo Darwin; else exec /bin/uname "$@"; fi\n' >/tmp/fakeuname/uname
      chmod 755 /tmp/fakeuname/uname
      out=$(PATH=/tmp/fakeuname:$PATH $I --yes --method systemd --no-start -- "${SETUP[@]}" </dev/null 2>&1) && ng "Linux 以外で通った" || true
      contains "$out" "Linux 専用です（この OS: Darwin）" && ok "対象の OS を示して止まる" || ng "Linux 以外の表示: $out"
      contains "$out" "デスクトップ版" && ok "デスクトップ版を案内する" || ng "デスクトップ版の案内: $out"
      check "何も入れていない（Linux 以外）" nothing_installed

      echo "== 書庫（GitHub Releases の形）"
      V1=v0.0.0-installtest1
      A=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
      ARC="looptrack_${V1}_linux_${A}_server"
      cp -r "/gh/releases/download/$V1" /tmp/badarc
      printf 'x' >>"/tmp/badarc/$ARC.tar.gz"
      out=$($I --from /tmp/badarc --yes --method systemd --no-start -- "${SETUP[@]}" 2>&1) && ng "1 バイト変えた書庫で通った" || true
      contains "$out" "SHA-256 が合いません: $ARC.tar.gz" && ok "1 バイト変えた書庫を検知" || ng "書庫の不一致の表示: $out"
      check "何も入れていない（書庫の不一致）" nothing_installed
      # 書庫の行は合うが、中の looptrack が SHA256SUMS の実行ファイルの行（looptrack_<版>_linux_<arch>）と違う
      mkdir -p /tmp/swap/src/"$ARC" /tmp/swap/rel
      tar -xzf "/gh/releases/download/$V1/$ARC.tar.gz" -C /tmp/swap/src
      cp /dist2/looptrack_v0.0.0-installtest2_linux_"$A" /tmp/swap/src/"$ARC"/looptrack
      tar -C /tmp/swap/src -czf "/tmp/swap/rel/$ARC.tar.gz" "$ARC"
      { grep -v " $ARC.tar.gz\$" "/gh/releases/download/$V1/SHA256SUMS"; (cd /tmp/swap/rel && sha256sum "$ARC.tar.gz"); } >/tmp/swap/rel/SHA256SUMS
      out=$($I --from /tmp/swap/rel --yes --method systemd --no-start -- "${SETUP[@]}" 2>&1) && ng "中身を差し替えた書庫で通った" || true
      contains "$out" "書庫の中の looptrack が SHA256SUMS の looptrack_${V1}_linux_$A の行と合いません" && ok "書庫の中身と SHA256SUMS の実行ファイルの行の食い違いを検知" || ng "中身の差し替えの表示: $out"
      check "何も入れていない（書庫の中身の食い違い）" nothing_installed
      # 本物の書庫: --from なし（/gh の latest）で入る。置くのは書庫の中の looptrack
      out=$($I --yes --method systemd --no-start -- "${SETUP[@]}" 2>&1) || ng "書庫から入らない: $out"
      contains "$out" "$ARC.tar.gz（SHA-256 一致" && ok "書庫を取って照合した" || ng "書庫の取得の表示: $out"
      contains "$out" "設定まで終わりました" && ok "書庫から入る" || ng "書庫からの終わりの表示: $out"
      tar -xzOf "/gh/releases/download/$V1/$ARC.tar.gz" "$ARC/looptrack" | cmp -s - /usr/local/bin/looptrack &&
        ok "置いたのは書庫の中の looptrack" || ng "置いた looptrack が書庫の中のものと違う"
      check "印（VERSION=${V1}）" grep -qx "VERSION=$V1" /etc/looptrack/install.conf
      # 同じ版の --upgrade --version（systemd が無いので、入れ替えない経路だけを見る。版の違う更新は systemd の場面）
      out=$($I --upgrade --version "$V1" 2>&1) || ng "--upgrade --version が失敗: $out"
      contains "$out" "$ARC.tar.gz（SHA-256 一致" && ok "--upgrade --version はその版の書庫を取る" || ng "--upgrade --version の表示: $out"
      contains "$out" "すでに" && ok "書庫の中身が入っているものと同じなら何もしない" || ng "同じ版の表示: $out"
      $I --uninstall --purge --yes >/dev/null 2>&1 || ng "書庫から入れた分の purge が失敗"
      check "片付いた（書庫）" nothing_installed

      echo "== SHA-256 の不一致"
      cp -r /dist /tmp/bad
      for f in /tmp/bad/looptrack_*; do printf 'x' >>"$f"; done
      out=$($I --from /tmp/bad --yes --method systemd --no-start -- "${SETUP[@]}" 2>&1) && ng "不一致で成功した" || true
      contains "$out" "SHA-256 が合いません" && ok "不一致を検知" || ng "不一致の表示: $out"
      check "何も入れていない" nothing_installed

      echo "== SHA256SUMS の署名"
      # 止まる場面（何も入れない）を先に、進む場面（setup の失敗で止める。実行ファイルは置かれる）を後に確かめる
      out=$($I --from /dist --yes --require-signature --method systemd --no-start -- "${SETUP[@]}" 2>&1) && ng "--require-signature で minisign なしが通った" || true
      contains "$out" "minisign がありません" && ok "--require-signature は minisign なしで止まる" || ng "--require-signature の表示: $out"
      check "何も入れていない（minisign なし）" nothing_installed
      cp -r /dist /tmp/nosig
      rm -f /tmp/nosig/SHA256SUMS.minisig
      out=$(LOOPTRACK_INSTALL_REQUIRE_SIGNATURE=1 $I --from /tmp/nosig --yes --method systemd --no-start -- "${SETUP[@]}" 2>&1) && ng "署名なしで --require-signature が通った" || true
      contains "$out" "SHA256SUMS.minisig がありません" && ok "LOOPTRACK_INSTALL_REQUIRE_SIGNATURE=1 は署名なしで止まる" || ng "署名なし・必須の表示: $out"
      check "何も入れていない（署名なし）" nothing_installed
      # 偽の minisign: 引数（-V -q -P <公開鍵> -m SHA256SUMS -x SHA256SUMS.minisig）を控え、MINISIGN_FAKE の結果を返す
      mkdir -p /tmp/fakebin
      printf '#!/bin/sh\necho "$*" >/tmp/minisign.args\n[ "$MINISIGN_FAKE" = ok ]\n' >/tmp/fakebin/minisign
      chmod 755 /tmp/fakebin/minisign
      out=$(PATH=/tmp/fakebin:$PATH MINISIGN_FAKE=ng $I --from /dist --yes --method systemd --no-start -- "${SETUP[@]}" 2>&1) && ng "署名の不一致で通った" || true
      contains "$out" "SHA256SUMS の署名が合いません" && ok "署名の不一致を検知" || ng "署名の不一致の表示: $out"
      check "何も入れていない（署名の不一致）" nothing_installed
      out=$(PATH=/tmp/fakebin:$PATH MINISIGN_FAKE=ng $I --yes --method systemd --no-start -- "${SETUP[@]}" 2>&1) && ng "書庫で署名の不一致が通った" || true
      contains "$out" "SHA256SUMS の署名が合いません" && ok "書庫の取得元でも署名の不一致を検知" || ng "書庫の署名の不一致の表示: $out"
      check "何も入れていない（書庫・署名の不一致）" nothing_installed
      BADSETUP=(--store sqlite --public-url https://im.example.com --admin-password-file /root/pw) # setup で止まる（署名の表示だけを見る）
      out=$(PATH=/tmp/fakebin:$PATH MINISIGN_FAKE=ok $I --from /dist --yes --require-signature --method systemd --no-start -- "${BADSETUP[@]}" 2>&1) || true
      contains "$out" "SHA256SUMS の署名を確かめました" && ok "署名が合えば進む" || ng "署名の一致の表示: $out"
      grep -q -- "-V -q -P ${LOOPTRACK_INSTALL_MINISIGN_PUBKEY:-RWRrJP/r1wfXKalGsxLnzFmmsExUd2azSJh4ccrYDJEBu8yE3N0ZlLJy} -m .*/SHA256SUMS -x .*/SHA256SUMS.minisig" /tmp/minisign.args &&
        ok "minisign に公開鍵と SHA256SUMS を渡す" || ng "minisign の引数: $(cat /tmp/minisign.args)"
      out=$($I --from /dist --yes --method systemd --no-start -- "${BADSETUP[@]}" 2>&1) || true
      contains "$out" "minisign が無いので" && ok "minisign が無ければ注意して進む" || ng "minisign なしの表示: $out"
      out=$($I --from /tmp/nosig --yes --method systemd --no-start -- "${BADSETUP[@]}" 2>&1) || true
      contains "$out" "SHA256SUMS.minisig）がありません" && ok "署名が無ければ注意して進む" || ng "署名なしの表示: $out"
      check ".env・印を残さない（setup の失敗）" sh -c '[ ! -e /etc/looptrack/install.conf ] && [ ! -e /etc/looptrack/.env ]'

      echo "== setup の失敗（--two-factor なし）"
      out=$($I --from /dist --yes --method systemd --no-start -- --store sqlite --public-url https://im.example.com --admin-password-file /root/pw 2>&1) && ng "setup の失敗で成功した" || true
      contains "$out" "二段階認証" && ok "setup の案内が出る" || ng "setup の表示: $out"
      check ".env・印・一時ファイルを残さない" sh -c '[ ! -e /etc/looptrack/.env ] && [ ! -e /etc/looptrack/install.conf ] && [ -z "$(ls /tmp/looptrack-install.* 2>/dev/null)" ]'
      check "SQLite のファイルを残さない" sh -c '[ ! -e /var/lib/looptrack/im.db ]'

      echo "== 中断（取得中に TERM）"
      mkdir /tmp/slow
      mkfifo /tmp/slow/SHA256SUMS
      $I --from /tmp/slow --yes --method systemd --no-start -- "${SETUP[@]}" >/tmp/int.out 2>&1 &
      pid=$!
      sleep 1
      kill -TERM "$pid" || ng "取得中に止まらずに終わった: $(cat /tmp/int.out)"
      echo x >/tmp/slow/SHA256SUMS # 読みかけの cp を終わらせる（sh は子の終了後に trap を動かす）
      rc=0
      wait "$pid" || rc=$?
      [ "$rc" = 130 ] && ok "中断は 130 で終わる" || ng "中断の終了コード $rc: $(cat /tmp/int.out)"
      grep -q 中断しました /tmp/int.out && ok "中断の表示" || ng "中断の表示: $(cat /tmp/int.out)"
      check "中断で一時ファイルを残さない" sh -c '[ -z "$(ls /tmp/looptrack-install.* 2>/dev/null)" ]'

      echo "== 自動の置き換え（--auto-upgrade）: 入れられない場面は何も変えずに止まる"
      # 置き場の一覧と中身（前の場面の setup の失敗が置いた実行ファイルは残っているので、前後で比べる）
      # （ディレクトリは名前だけ。一時ディレクトリの作成と片付けでディレクトリの時刻は変わるため）
      fs_snapshot() {
        find /usr/local /etc/looptrack /etc/systemd/system /var/lib/looptrack /tmp -xdev \( -type d -printf '%p d\n' \) -o -printf '%p %y %s %T@\n' 2>/dev/null |
          sort | sha256sum
      }
      snap=$(fs_snapshot)
      out=$($I --from /dist --yes --method systemd --auto-upgrade maybe -- "${SETUP[@]}" 2>&1) && ng "--auto-upgrade maybe が通った" || true
      contains "$out" "--auto-upgrade は on か off です: maybe" && ok "値の誤りで止まる" || ng "値の誤りの表示: $out"
      out=$($I --from /dist --yes --only-newer 2>&1) && ng "--upgrade なしの --only-newer が通った" || true
      contains "$out" "--only-newer は --upgrade と一緒に使います" && ok "--only-newer は --upgrade と一緒に" || ng "--only-newer の表示: $out"
      out=$($I --from /dist --yes --method compose --auto-upgrade on -- "${SETUP[@]}" 2>&1) && ng "compose の --auto-upgrade on が通った" || true
      contains "$out" "compose ではコンテナのイメージを自動では置き換えません" && ok "compose の on は止まる（イメージは自動では置き換えない）" || ng "compose の on の表示: $out"
      out=$($I --from /dist --yes --method systemd --no-start --auto-upgrade on -- "${SETUP[@]}" 2>&1) && ng "--no-start の --auto-upgrade on が通った" || true
      contains "$out" "--no-start と --auto-upgrade on は一緒に使えません" && ok "--no-start の on は止まる" || ng "--no-start の on の表示: $out"
      out=$($I --from /dist --yes --method systemd --auto-upgrade on -- "${SETUP[@]}" 2>&1) && ng "systemd なしの --auto-upgrade on が通った" || true
      contains "$out" "systemd が動いていないので、自動の置き換え" && ok "systemd なしの on は止まる" || ng "systemd なしの on の表示: $out"
      [ "$snap" = "$(fs_snapshot)" ] && ok "何も変えていない（--auto-upgrade on が止まった）" || ng "--auto-upgrade on が止まったのに置き場が変わった"
      touch /tmp/snapshot-probe
      [ "$snap" != "$(fs_snapshot)" ] && ok "対照: ファイルを 1 つ足すと比べた結果が変わる" || ng "前提: 置き場の比べ方が変化を拾わない"
      rm -f /tmp/snapshot-probe
      check "timer・スクリプトを置いていない" sh -c '[ ! -e /etc/systemd/system/looptrack-upgrade.timer ] && [ ! -e /etc/systemd/system/looptrack-upgrade.service ] && [ ! -e /usr/local/lib/looptrack ]'

      echo "== systemd --no-start（非対話）"
      out=$($I --from /dist --yes --method systemd --no-start -- "${SETUP[@]}" 2>&1) || ng "install が失敗: $out"
      contains "$out" "設定まで終わりました" && ok "終わりの表示" || ng "終わりの表示: $out"
      contains "$out" "https://im.example.com/looptrack/" && ok "ブラウザの URL" || ng "URL の表示"
      contains "$out" "looptrack issue login --browser --url https://im.example.com/looptrack" && ok "CLI の login" || ng "CLI の表示"
      # MCP の接続設定は looptrack setup の最後の案内と同じ形（X-Looptrack-Project を落とさない）。
      # 文字列の正本は internal/setupwiz.MCPConfigs で、install.sh との一致は internal/setupwiz/install_sh_test.go が見る
      contains "$out" "claude mcp add --transport http looptrack $MCP_URL --header \"X-Looptrack-Project: web\"" &&
        ok "MCP の設定（Claude Code・既定のプロジェクト付き）" || ng "MCP の表示: $out"
      contains "$out" "{ \"mcpServers\": { \"looptrack\": { \"type\": \"http\", \"url\": \"$MCP_URL\", \"headers\": { \"X-Looptrack-Project\": \"web\" } } } }" &&
        ok "MCP の設定（.mcp.json）" || ng ".mcp.json の表示: $out"
      contains "$out" "http_headers = { \"X-Looptrack-Project\" = \"web\" }" && ok "MCP の設定（Codex）" || ng "Codex の表示: $out"
      contains "$out" "GitHub Copilot CLI" && ok "MCP の設定（Copilot）" || ng "Copilot の表示: $out"
      contains "$out" "<プロジェクトの slug>" && ng "slug が分かるのに差し込み文が出た" || ok "slug が分かるときは差し込み文を出さない"
      check "印（PROJECT=web）" grep -qx PROJECT=web /etc/looptrack/install.conf
      contains "$out" "proxy_pass http://127.0.0.1:8090;" && ok "nginx の設定例" || ng "nginx の設定例"
      contains "$out" "reverse_proxy 127.0.0.1:8090" && ok "Caddy の設定例" || ng "Caddy の設定例"
      check "実行ファイル" test -x /usr/local/bin/looptrack
      check "利用者 looptrack（ログインできないシェル）" sh -c 'getent passwd looptrack | grep -q nologin'
      check ".env は root 600" sh -c '[ "$(stat -c %U:%a /etc/looptrack/.env)" = root:600 ]'
      check "LOOPTRACK_DSN は /var/lib/looptrack" grep -qx "LOOPTRACK_DSN='sqlite:/var/lib/looptrack/im.db'" /etc/looptrack/.env
      check "LOOPTRACK_LISTEN は 127.0.0.1" grep -qx "LOOPTRACK_LISTEN='127.0.0.1:8090'" /etc/looptrack/.env
      check "SQLite は looptrack の所有・600" sh -c '[ "$(stat -c %U:%a /var/lib/looptrack/im.db)" = looptrack:600 ]'
      check "systemd では data も compose.yaml も作らない" sh -c '[ ! -e /etc/looptrack/data ] && [ ! -e /etc/looptrack/compose.yaml ]'
      contains "$out" "systemctl enable --now looptrack" && ok "setup の起動の案内は systemd" || ng "起動の案内: $out"
      contains "$out" "docker compose up" && ng "systemd なのに compose の案内" || ok "compose の案内を出さない"
      admin project list 2>&1 | grep -q '^web ' && ok "setup の直後にプロジェクト web（--project）" || ng "プロジェクト: $(admin project list 2>&1)"
      admin member list web 2>&1 | grep -q alice && ok "管理者 alice が web に参加" || ng "参加: $(admin member list web 2>&1)"
      check "unit のサンドボックス" grep -q '^ProtectSystem=strict' /etc/systemd/system/looptrack.service
      check "unit は looptrack で動く" grep -q '^User=looptrack' /etc/systemd/system/looptrack.service
      check "印（METHOD=systemd・STARTED=no）" sh -c 'grep -q ^METHOD=systemd /etc/looptrack/install.conf && grep -q ^STARTED=no /etc/looptrack/install.conf'
      check "自動の置き換えは既定で off（印・timer なし）" sh -c 'grep -qx AUTO_UPGRADE=off /etc/looptrack/install.conf && [ ! -e /etc/systemd/system/looptrack-upgrade.timer ] && [ ! -e /usr/local/lib/looptrack ]'
      contains "$out" "自動の置き換え: 無効（既定。有効にする:" && ok "自動の置き換えは無効と案内する" || ng "自動の置き換えの案内: $out"
      check "一時ファイルを残さない" sh -c '[ -z "$(ls /tmp/looptrack-install.* 2>/dev/null)" ]'
      # クライアントに配る looptrack: 取得元（…1）の SHA256SUMS にあるのは linux/<arch> だけなので、その 1 つと SHA256SUMS を置き、
      # ほかの 5 対象は置かずに注意を出す（対照: …2 の 6 対象は systemd の場面の --upgrade で確かめる）
      check ".env に LOOPTRACK_DIST_DIR を足す（単引用符）" grep -qx "LOOPTRACK_DIST_DIR='$DISTD'" /etc/looptrack/.env
      r=$(dist_matches v0.0.0-installtest1 /dist/SHA256SUMS 1) && ok "配布ディレクトリにこの版の linux/${CARCH}（SHA256SUMS と一致）" || ng "配布ディレクトリ: $r"
      check "配布ディレクトリは root 755・ファイルは 644" sh -c '[ "$(stat -c %U:%a '"$DISTD"')" = root:755 ] && [ "$(stat -c %a '"$DISTD"'/SHA256SUMS)" = 644 ]'
      contains "$out" "次の対象は置きません" && contains "$out" "windows_arm64（SHA256SUMS に無い）" && ok "SHA256SUMS に無い対象は置かずに知らせる" || ng "足りない対象の表示: $out"

      echo "== 2 回目"
      before=$(sha256sum /etc/looptrack/.env /etc/systemd/system/looptrack.service)
      out=$($I --from /dist --yes 2>&1) || ng "2 回目が失敗: $out"
      contains "$out" "設定済みです" && ok "設定済みを示す" || ng "2 回目の表示: $out"
      [ "$before" = "$(sha256sum /etc/looptrack/.env /etc/systemd/system/looptrack.service)" ] && ok "書き換えない" || ng "2 回目で書き換わった"
      before=$(sha256sum /etc/looptrack/install.conf)
      out=$($I --auto-upgrade on 2>&1) && ng "--no-start で入れたサーバの --auto-upgrade on が通った" || true
      contains "$out" "--no-start と --auto-upgrade on は一緒に使えません" && ok "入れた後の on も --no-start のサーバでは止まる" || ng "入れた後の on の表示: $out"
      [ "$before" = "$(sha256sum /etc/looptrack/install.conf)" ] && ok "止まったときは印を書き換えない" || ng "止まったのに印が変わった"
      out=$($I --auto-upgrade off 2>&1) || ng "入れた後の --auto-upgrade off が失敗: $out"
      contains "$out" "自動の置き換えは無効です（既定）" && ok "入れた後の off（入れ直さない）" || ng "入れた後の off の表示: $out"
      check "off の後も印は AUTO_UPGRADE=off・設定の他の行はそのまま" sh -c 'grep -qx AUTO_UPGRADE=off /etc/looptrack/install.conf && grep -q ^METHOD=systemd /etc/looptrack/install.conf && grep -q ^STARTED=no /etc/looptrack/install.conf'

      echo "== 手で起動（unit と同じ利用者・環境変数）"
      admin serve >/tmp/serve.log 2>&1 &
      spid=$!
      up=0
      for _ in $(seq 1 30); do
        if LOOPTRACK_HEALTH_URL=http://127.0.0.1:8090/looptrack/healthz /usr/local/bin/looptrack healthcheck >/dev/null 2>&1; then
          up=1
          break
        fi
        sleep 0.5
      done
      [ $up = 1 ] && ok "/healthz が 200" || ng "/healthz: $(cat /tmp/serve.log)"
      check "ログイン画面が 200" env LOOPTRACK_HEALTH_URL=http://127.0.0.1:8090/looptrack/login /usr/local/bin/looptrack healthcheck
      kill "$spid" 2>/dev/null || true
      wait "$spid" 2>/dev/null || true
      grep -q "$PERM_WARN" /tmp/serve.log && ng "DB の権限の警告が出た: $(grep "$PERM_WARN" /tmp/serve.log)" || ok "DB の権限の警告を出さない"
      # 配布物が足りない（linux/<arch> だけ）ので、起動時のログに知らせる（対照: そろった後に出ないことは systemd の場面の --upgrade で）
      grep "$DIST_LAG" /tmp/serve.log | grep -q 'windows/arm64' && ok "起動時のログに配布物の不足（windows/arm64 など）を出す" || ng "配布物の不足のログ: $(cat /tmp/serve.log)"
      admin user list 2>&1 | grep -q alice && ok "管理者 alice" || ng "管理者: $(admin user list 2>&1)"

      echo "== --uninstall（データを残す）→ 入れ直す"
      out=$($I --uninstall 2>&1) || ng "uninstall が失敗: $out"
      check "実行ファイル・unit・印を外した" sh -c '[ ! -e /usr/local/bin/looptrack ] && [ ! -e /etc/systemd/system/looptrack.service ] && [ ! -e /etc/looptrack/install.conf ]'
      check ".env と SQLite は残る" sh -c '[ -f /etc/looptrack/.env ] && [ -f /var/lib/looptrack/im.db ]'
      check "配布ディレクトリを外した" sh -c '[ ! -e /usr/local/share/looptrack ]'
      chmod 644 /var/lib/looptrack/im.db # 以前の版が作った DB（0644）を入れ直す場面
      key=$(grep ^LOOPTRACK_SECRET_KEY /etc/looptrack/.env)
      out=$($I --from /dist --yes --method systemd --no-start -- "${SETUP[@]}" 2>&1) || ng "入れ直しが失敗: $out"
      contains "$out" "作成済みです" && ok "setup を飛ばす" || ng "入れ直しの表示: $out"
      [ "$(grep -c '^LOOPTRACK_DIST_DIR=' /etc/looptrack/.env)" = 1 ] && ok "入れ直しても LOOPTRACK_DIST_DIR は 1 行のまま" || ng "LOOPTRACK_DIST_DIR の行: $(grep -c '^LOOPTRACK_DIST_DIR=' /etc/looptrack/.env)"
      r=$(dist_matches v0.0.0-installtest1 /dist/SHA256SUMS 1) && ok "入れ直すと配布ディレクトリも置き直す" || ng "入れ直しの配布ディレクトリ: $r"
      [ "$key" = "$(grep ^LOOPTRACK_SECRET_KEY /etc/looptrack/.env)" ] && ok "LOOPTRACK_SECRET_KEY は同じ" || ng "LOOPTRACK_SECRET_KEY が変わった"
      check "入れ直しで広い DB（644）を 600 に直す" sh -c '[ "$(stat -c %U:%a /var/lib/looptrack/im.db)" = looptrack:600 ]'
      admin user list 2>&1 | grep -q alice && ok "データ（管理者）が残る" || ng "データが残らない"

      echo "== --uninstall --purge"
      out=$($I --uninstall --purge --yes 2>&1) || ng "purge が失敗: $out"
      check "設定・データ・利用者を消した" sh -c '[ ! -e /etc/looptrack ] && [ ! -e /var/lib/looptrack ] && ! id looptrack'

      echo "== MCP の接続設定（プロジェクトを作らなかったとき）"
      out=$($I --from /dist --yes --method systemd --no-start -- "${SETUP_BASE[@]}" 2>&1) || ng "install が失敗: $out"
      contains "$out" "claude mcp add --transport http looptrack $MCP_URL --header \"X-Looptrack-Project: <プロジェクトの slug>\"" &&
        ok "slug が分からないときは差し込みの slug を出す" || ng "差し込みの表示: $out"
      contains "$out" "使うプロジェクトの slug に置き換えます" && ok "置き換え方を添える" || ng "置き換え方の表示: $out"
      check "印（PROJECT は空）" grep -qx PROJECT= /etc/looptrack/install.conf
      $I --uninstall --purge --yes >/dev/null 2>&1 || ng "purge が失敗"

      echo "== compose --no-start"
      out=$($I --from /dist --yes --method compose --no-start -- "${SETUP[@]}" 2>&1) || ng "compose の install が失敗: $out"
      check "compose.yaml" grep -q 'image: ${LOOPTRACK_IMAGE:-looptrack:latest}' /opt/looptrack/compose.yaml
      check "compose.yaml は 127.0.0.1 だけで公開" grep -q '"127.0.0.1:8090:8090"' /opt/looptrack/compose.yaml
      check ".env（コンテナの /data/im.db）" grep -qx "LOOPTRACK_DSN='sqlite:/data/im.db'" /opt/looptrack/.env
      contains "$out" "docker compose up -d" && ok "setup の起動の案内は compose" || ng "compose の起動の案内: $out"
      check ".env は 600" sh -c '[ "$(stat -c %a /opt/looptrack/.env)" = 600 ]'
      check "data と im.db は uid 65534・im.db は 600" sh -c '[ "$(stat -c %u /opt/looptrack/data)" = 65534 ] && [ "$(stat -c %u:%a /opt/looptrack/data/im.db)" = 65534:600 ]'
      check "印（METHOD=compose・DIR）" sh -c 'grep -q ^METHOD=compose /etc/looptrack/install.conf && grep -q ^DIR=/opt/looptrack /etc/looptrack/install.conf'
      # クライアントに配る looptrack: compose.yaml が ./dist をコンテナの /dist に読み取り専用で入れ、.env は /dist を指し、ホストの ./dist に置く
      check "compose.yaml は ./dist を /dist に読み取り専用で入れる" grep -qE '^ +- \./dist:/dist:ro$' /opt/looptrack/compose.yaml
      check ".env の LOOPTRACK_DIST_DIR はコンテナの中の /dist" grep -qx "LOOPTRACK_DIST_DIR='/dist'" /opt/looptrack/.env
      r=$(DISTD=/opt/looptrack/dist dist_matches v0.0.0-installtest1 /dist/SHA256SUMS 1) && ok "compose の配布ディレクトリ（/opt/looptrack/dist）" || ng "compose の配布ディレクトリ: $r"
      out=$($I --from /dist --yes 2>&1) || ng "compose の 2 回目が失敗"
      contains "$out" "設定済みです" && ok "compose の 2 回目は設定済み" || ng "compose の 2 回目: $out"
      out=$($I --from /dist --yes --method systemd 2>&1) && ng "動かし方の違う 2 回目が通った" || ok "動かし方の違う 2 回目は断る"
      out=$($I --auto-upgrade on 2>&1) && ng "compose の入れた後の --auto-upgrade on が通った" || true
      contains "$out" "compose ではコンテナのイメージを自動では置き換えません" && ok "compose の入れた後の on も止まる" || ng "compose の on の表示: $out"
      check "compose の印は AUTO_UPGRADE=off のまま" grep -qx AUTO_UPGRADE=off /etc/looptrack/install.conf
      out=$($I --uninstall --purge --yes 2>&1) || ng "compose の purge が失敗: $out"
      check "compose の置き場を消した" sh -c '[ ! -e /opt/looptrack ] && [ ! -e /etc/looptrack ]'

      # 公開後の「Releases から install.sh を取って、同じ接頭辞を --from に渡す」と同じ形を、手元の配布物で確かめる。
      # /dist は dist.sh build + sums の出力そのままなので、install.sh も grants.sql もその中にある。
      echo "== リリースの出力をそのまま取得元にする"
      check "配布物に install.sh がある" test -s /dist/install.sh
      check "配布物に grants.sql がある" test -s /dist/grants.sql
      check "install.sh に実行権がある" test -x /dist/install.sh
      cmp -s /src/install.sh /dist/install.sh && ok "配布物の install.sh はリポジトリの写し" || ng "配布物の install.sh が deploy/install.sh と違う"
      cmp -s /src/grants.sql /dist/grants.sql && ok "配布物の grants.sql はリポジトリの写し" || ng "配布物の grants.sql が deploy/grants.sql と違う"
      for f in install.sh grants.sql; do
        grep -qE "^[0-9a-f]{64} [ *]?$f\$" /dist/SHA256SUMS && ok "SHA256SUMS に $f が載る" || ng "SHA256SUMS に $f が無い: $(cat /dist/SHA256SUMS)"
      done
      (cd /dist && grep -E ' [*]?(install\.sh|grants\.sql)$' SHA256SUMS | sha256sum -c - >/dev/null 2>&1) &&
        ok "取得した install.sh・grants.sql を SHA256SUMS で照合できる" || ng "SHA256SUMS の照合が合わない"
      # 配布物の中の install.sh を、同じディレクトリを --from にして実行する（リポジトリの /src は使わない）
      out=$(sh /dist/install.sh --from /dist --yes --method systemd --no-start -- "${SETUP[@]}" 2>&1) || ng "配布物の install.sh で入らない: $out"
      contains "$out" "設定まで終わりました" && ok "配布物の install.sh で入る" || ng "終わりの表示: $out"
      check "実行ファイルが入った" test -x /usr/local/bin/looptrack
      check "印（METHOD=systemd・VERSION）" sh -c 'grep -q ^METHOD=systemd /etc/looptrack/install.conf && grep -q ^VERSION=v0.0.0-installtest1 /etc/looptrack/install.conf'
      # 更新の経路も同じ並びから（同じ版なので入れ替えはしないが、SHA256SUMS の読みと版の判定はここまでで通る）
      out=$(sh /dist/install.sh --upgrade --from /dist 2>&1) || ng "配布物の install.sh の --upgrade が失敗: $out"
      contains "$out" "すでに" && ok "同じ版の --upgrade は何もしない（資産が増えても版を取り違えない）" || ng "--upgrade の表示: $out"
      $I --uninstall --purge --yes >/dev/null 2>&1 || ng "配布物から入れた分の purge が失敗"
      ;;

    interactive)
      # 端末（script の疑似端末）から答える。答えの順は install.sh の「動かし方」と looptrack setup の ①〜⑥・確認
      # （① チーム ② SQLite ③ ポート・接頭辞・公開 URL ④ ログイン名・表示名・パスワード 2 回 ⑤ 二段階認証 ⑥ slug［- = 作らない］→ y）
      # （setup の問いを変えたらここも直す）
      echo "== 対話（systemd --no-start）"
      (
        sleep 1
        for a in 1 2 1 "" "" https://im.example.com "" "" correct-horse-battery correct-horse-battery "" "" y; do
          printf '%s\r' "$a"
          sleep 0.7
        done
        sleep 3
      ) | script -qec "sh /src/install.sh --from /dist --no-start" /dev/null >/tmp/tty.out 2>&1 || true
      grep -q 設定まで終わりました /tmp/tty.out && ok "対話で最後まで進む" || ng "対話: $(tr -d '\r' </tmp/tty.out | tail -n 30)"
      check "対話の結果（systemd・SQLite・127.0.0.1）" sh -c "grep -q ^METHOD=systemd /etc/looptrack/install.conf && grep -qx \"LOOPTRACK_DSN='sqlite:/var/lib/looptrack/im.db'\" /etc/looptrack/.env && grep -qx \"LOOPTRACK_LISTEN='127.0.0.1:8090'\" /etc/looptrack/.env"
      grep -q correct-horse-battery /tmp/tty.out && ng "パスワードが端末に出た" || ok "パスワードを表示しない"
      admin user list 2>&1 | grep -q admin && ok "管理者 admin" || ng "管理者: $(admin user list 2>&1)"
      ;;

    systemd)
      # .env を sh で読めないとき（値に括弧を含み、単引用符で囲んでいない。docker compose の env_file や systemd の EnvironmentFile はそのまま受け付ける）:
      # sh は構文の誤りの行（DSN のパスワードを含む）をそのまま出力するので、読む前に構文を確かめ、値を出さずに止める。
      # 目印の SECRETMARK123 はどの出力にも出てはいけない。まだ何も入れていない状態で（次の本番の入れ方に影響しないよう、終わりに片付ける）
      echo "== .env を sh で読めないとき（引用なし・括弧つきの DSN）: 値を出さず、権限の段に進まずに止まる"
      mkdir -p /etc/looptrack
      (umask 077 && printf '%s\n' "LOOPTRACK_SECRET_KEY='0123456789abcdef'" 'LOOPTRACK_DSN=lt_app:SECRETMARK123@tcp(127.0.0.1:3306)/ltdb?parseTime=true' >/etc/looptrack/.env)
      rc=0
      out=$(timeout 300 $I --from /dist --yes --method systemd 2>&1) || rc=$?
      [ "$rc" != 0 ] && ok "0 でない終了コードで止まる（${rc}）" || ng "sh で読めない .env で成功した: $out"
      contains "$out" ".env を sh で読めません" && ok "「.env を sh で読めません」と示す" || ng "止まる文面: $out"
      contains "$out" "LOOPTRACK_DSN" && ok "疑わしい行のキーの名前を示す" || ng "キーの名前: $out"
      contains "$out" "単引用符で囲んでください" && ok "単引用符で囲むよう案内する" || ng "案内: $out"
      contains "$out" "SECRETMARK123" && ng "値が出力に出た: $out" || ok "値を出力しない（標準出力・標準エラーとも）"
      contains "$out" "MySQL の権限" && ng "権限の段に進んだ: $out" || ok "権限の段に進まない（原因を権限と取り違えない）"
      check "起動していない・印を書いていない" sh -c '! systemctl is-active looptrack && [ ! -e /etc/looptrack/install.conf ]'
      # 構文の誤りにならない形（X=a b は b を別のコマンドとして実行し、「b: not found」と値の後ろ側を出す）も、値を出さずに止める
      (umask 077 && printf '%s\n' "LOOPTRACK_SECRET_KEY='0123456789abcdef'" 'LOOPTRACK_EXTRA=front SECRETMARK123 back' >/etc/looptrack/.env)
      rc=0
      out=$(timeout 300 $I --from /dist --yes --method systemd 2>&1) || rc=$?
      [ "$rc" != 0 ] && ok "空白を含む引用なしの値も 0 でない終了コードで止まる（${rc}）" || ng "空白を含む引用なしの値で成功した: $out"
      contains "$out" ".env を sh で読めません" && contains "$out" "LOOPTRACK_EXTRA" && ok "文面とキーの名前を示す" || ng "空白の値の文面: $out"
      contains "$out" "SECRETMARK123\|not found" && ng "値（の一部）が出力に出た: $out" || ok "値を出力しない・別のコマンドとして実行しない（not found が出ない）"
      # 許した形（KEY='…'・KEY="…"（$ ` \ " なし）・引用なしの安全な値）以外はすべて止める。二重引用符の中の $name は sh が
      # 「name: parameter not set」と出す・キーの形でない行（値だけの行）は「…: not found」と行ごと出す、のを出さずに止める
      (umask 077 && printf '%s\n' "LOOPTRACK_SECRET_KEY='0123456789abcdef'" 'LOOPTRACK_EXTRA="pa$SECRETMARK123"' >/etc/looptrack/.env)
      rc=0
      out=$(timeout 300 $I --from /dist --yes --method systemd 2>&1) || rc=$?
      [ "$rc" != 0 ] && contains "$out" ".env を sh で読めません" && contains "$out" "LOOPTRACK_EXTRA" && ok "二重引用符の中の \$name も止める（キーの名前を示す）" || ng "二重引用符の \$name: rc=${rc} $out"
      contains "$out" "SECRETMARK123\|parameter not set\|not found" && ng "値（の一部）が出力に出た: $out" || ok "値も「parameter not set」も出さない"
      (umask 077 && printf '%s\n' "LOOPTRACK_SECRET_KEY='0123456789abcdef'" 'SECRETMARK123 back' >/etc/looptrack/.env)
      rc=0
      out=$(timeout 300 $I --from /dist --yes --method systemd 2>&1) || rc=$?
      [ "$rc" != 0 ] && contains "$out" ".env を sh で読めません" && contains "$out" "行 2" && ok "キーの形でない行も止める（行番号を示す）" || ng "キーの形でない行: rc=${rc} $out"
      contains "$out" "SECRETMARK123\|not found" && ng "値（の一部）が出力に出た: $out" || ok "値も「not found」も出さない"
      # 対照: 同じ値を単引用符で囲むと、この検査を通って先（保存先の確認 → MySQL には繋がらないので権限の段）へ進む
      (umask 077 && printf '%s\n' "LOOPTRACK_SECRET_KEY='0123456789abcdef'" "LOOPTRACK_DSN='lt_app:SECRETMARK123@tcp(127.0.0.1:3306)/ltdb?parseTime=true'" >/etc/looptrack/.env)
      out=$(timeout 300 $I --from /dist --yes --method systemd 2>&1) && ng "MySQL に繋がらないのに成功した" || true
      contains "$out" ".env を sh で読めません" && ng "単引用符で囲んだ値を sh で読めないと判定した: $out" || ok "対照: 単引用符で囲んだ値は検査を通る"
      contains "$out" "MySQL の権限" && ok "対照: 検査の先（保存先の確認・権限の段）へ進む" || ng "対照が先へ進まない: $out"
      rm -rf /etc/looptrack /var/lib/looptrack /etc/systemd/system/looptrack.service /usr/local/bin/looptrack
      systemctl daemon-reload >/dev/null 2>&1 || true

      echo "== systemd で実起動（README の 1 行と同じ形: curl … | sh。取得元は手元の HTTP の raw/・gh/ に読み替える）"
      A=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
      out=$(curl -fsSL "$RAW_URL" | LOOPTRACK_INSTALL_REPO="$GH_URL" LOOPTRACK_INSTALL_ALLOW_HTTP=1 sh -s -- --yes --method systemd -- "${SETUP[@]}" 2>&1) || ng "install が失敗: $out"
      contains "$out" "インストールが終わりました" && ok "1 行で最後まで進む" || ng "終わりの表示: $out"
      contains "$out" "looptrack_v0.0.0-installtest1_linux_${A}_server.tar.gz（SHA-256 一致" && ok "既定の取得元（releases/latest）の書庫を取った" || ng "書庫の取得の表示: $out"
      contains "$out" "200 OK" && ok "/healthz を待った" || ng "起動の表示: $out"
      contains "$out" "読めました" && ok "起動の前に保存先を確かめる" || ng "保存先の確認の表示: $out"
      check "サービスが動いている" systemctl is-active looptrack
      check "サービスは有効" systemctl is-enabled looptrack
      check "looptrack の利用者で動く" sh -c '[ "$(ps -o user= -C looptrack | head -n1)" = looptrack ]'
      echo "  サンドボックスの評価: $(systemd-analyze security looptrack 2>/dev/null | tail -n 1)"
      out=$($I --from /dist --yes 2>&1) || ng "2 回目が失敗"
      contains "$out" "設定済みです" && ok "2 回目は設定済み" || ng "2 回目: $out"
      contains "$out" "サービス: active" && ok "2 回目にサービスの状態" || ng "2 回目の状態: $out"
      contains "$out" "更新: curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh -s -- --upgrade" &&
        ok "更新の案内は 1 行の --upgrade" || ng "更新の案内: $out"
      check "SQLite は looptrack の所有・600" sh -c '[ "$(stat -c %U:%a /var/lib/looptrack/im.db)" = looptrack:600 ]'
      grep -q "$PERM_WARN" <<<"$(journalctl -u looptrack -o cat --no-pager)" && ng "DB の権限の警告が出た" || ok "DB の権限の警告を出さない"

      echo "== ログイン（127.0.0.1 の待ち受けに直接）"
      r=$(web_login http://127.0.0.1:8090/looptrack) && ok "ログインできる $r" || ng "ログイン: $r"

      echo "== 公開 URL（Caddy・nginx を install.sh の設定例のまま前段に置き、https で）"
      # Caddy: 設定例の im.example.com のブロックに、手元の CA（local_certs）の指定だけを足す。80 番は nginx に譲る
      { printf '{\n\tlocal_certs\n\tskip_install_trust\n\thttp_port 8081\n}\n'; sed -n '/^# ---- Caddy/,$p' /etc/looptrack/proxy-examples.txt; } >/etc/caddy/Caddyfile
      systemctl restart caddy || ng "Caddy が起動しない: $(journalctl -u caddy -n 20 --no-pager)"
      PUB=(-k --resolve im.example.com:443:127.0.0.1)
      for _ in $(seq 1 30); do curl -sf "${PUB[@]}" -o /dev/null https://im.example.com/looptrack/healthz && break; sleep 1; done
      check "Caddy 経由で /healthz" curl -sf "${PUB[@]}" https://im.example.com/looptrack/healthz
      [ "$(curl -s "${PUB[@]}" -o /dev/null -w '%{http_code} %{redirect_url}' https://im.example.com/looptrack)" = "301 https://im.example.com/looptrack/" ] &&
        ok "Caddy: /looptrack は /looptrack/ へ" || ng "Caddy の /looptrack: $(curl -s "${PUB[@]}" -o /dev/null -w '%{http_code} %{redirect_url}' https://im.example.com/looptrack)"
      r=$(web_login https://im.example.com/looptrack "${PUB[@]}") && ok "公開 URL（Caddy）でログインできる $r" || ng "公開 URL のログイン: $r"
      # nginx: 設定例の server ブロックの証明書の行を自己署名の証明書で埋め、443 は Caddy が使うので 8443 で受ける
      openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=im.example.com -keyout /etc/nginx/im.key -out /etc/nginx/im.crt >/dev/null 2>&1
      sed -n '/^# ---- nginx/,/^# ---- Caddy/p' /etc/looptrack/proxy-examples.txt |
        sed 's/listen 443 ssl;/listen 8443 ssl;/; s|# ssl_certificate_key .*|ssl_certificate_key /etc/nginx/im.key;|; s|# ssl_certificate .*|ssl_certificate /etc/nginx/im.crt;|' \
        >/etc/nginx/conf.d/looptrack.conf
      nginx -t >/dev/null 2>&1 && systemctl reload nginx && ok "nginx の設定例が通る（nginx -t）" || ng "nginx の設定例: $(nginx -t 2>&1)"
      NGX=(-k --resolve im.example.com:8443:127.0.0.1)
      for _ in $(seq 1 10); do curl -sf "${NGX[@]}" -o /dev/null https://im.example.com:8443/looptrack/healthz && break; sleep 1; done
      check "nginx 経由で /healthz" curl -sf "${NGX[@]}" https://im.example.com:8443/looptrack/healthz
      r=$(web_login https://im.example.com:8443/looptrack "${NGX[@]}") && ok "公開 URL（nginx）でログインできる $r" || ng "nginx 経由のログイン: $r"
      ;;

    systemd-reboot)
      echo "== 再起動の後（docker restart で OS を起動し直した）"
      up=0
      for _ in $(seq 1 30); do
        if LOOPTRACK_HEALTH_URL=http://127.0.0.1:8090/looptrack/healthz /usr/local/bin/looptrack healthcheck >/dev/null 2>&1; then up=1; break; fi
        sleep 1
      done
      check "サービスが上がっている" systemctl is-active looptrack
      [ $up = 1 ] && ok "再起動の後も /healthz が 200" || ng "再起動の後の /healthz"

      echo "== 広い権限の DB（以前の版が作った 0644）で起動すると警告が出る"
      chmod 644 /var/lib/looptrack/im.db
      systemctl restart looptrack
      sleep 2
      grep -q "$PERM_WARN" <<<"$(journalctl -u looptrack -o cat --no-pager)" && ok "権限の警告が出る（確認の仕組みが効いている）" || ng "644 でも警告が出ない"

      echo "== --upgrade: サーバ用でない対象（windows/amd64）の実行ファイルが SHA256SUMS と合わない取得元では、何も置き換えずに止まる"
      # /dist2bad は /dist2 の写しで、windows/amd64 の実行ファイルだけを 1 バイト変えたもの（SHA256SUMS と署名はそのまま）。
      # サーバに置く linux/<arch> は合うので、止まるのはクライアントに配る looptrack の照合（stage_dist）だけ
      dsnap() { (cd "$DISTD" && ls -la --time-style=full-iso . && sha256sum ./*); }
      before_dist=$(dsnap)
      before_bin=$(sha256sum /usr/local/bin/looptrack | cut -d' ' -f1)
      rc=0
      out=$($I --upgrade --from /dist2bad 2>&1) || rc=$?
      [ "$rc" != 0 ] && ok "0 でない終了コードで止まる（${rc}）" || ng "合わない配布物で --upgrade が成功した: $out"
      contains "$out" "SHA-256 が合いません: looptrack_v0.0.0-installtest2_windows_amd64.exe" && ok "合わない対象の名前を示す" || ng "止まる文面: $out"
      contains "$out" "==> 停止" && ng "サービスを止める段に進んだ: $out" || ok "サービスを止める前に止まる"
      [ "$before_dist" = "$(dsnap)" ] && ok "配布ディレクトリ（ファイルも SHA256SUMS も）は前のまま" || ng "配布ディレクトリが変わった"
      check "実行ファイルは変わっていない" sh -c '[ "$(sha256sum /usr/local/bin/looptrack | cut -d" " -f1)" = "$1" ]' _ "$before_bin"
      check "印の版は変わっていない・サービスは動いたまま" sh -c 'grep -q ^VERSION=v0.0.0-installtest1 /etc/looptrack/install.conf && systemctl is-active looptrack'

      echo "== --upgrade（URL の接頭辞から取る: ${UPGRADE_URL}）"
      out=$($I --upgrade --from "$UPGRADE_URL" 2>&1) && ng "http:// を黙って使った" || true
      contains "$out" "https:// を使うか" && ok "http:// は断る" || ng "http:// の表示: $out"
      # .env を sh で読めないとき（引用なしの括弧つきの値）: 止める前に値を出さず止まり、サービスも実行ファイルも印も変えない
      echo "== --upgrade: .env を sh で読めないとき、サービスを止める前に値を出さず止まる"
      cp -p /etc/looptrack/.env /tmp/env.keep
      BEFORE=$(sha256sum /usr/local/bin/looptrack | cut -d' ' -f1)
      printf '%s\n' 'LOOPTRACK_EXTRA_DSN=lt_app:SECRETMARK123@tcp(127.0.0.1:3306)/ltdb?parseTime=true' >>/etc/looptrack/.env
      rc=0
      out=$($I --upgrade --from /dist2 2>&1) || rc=$?
      [ "$rc" != 0 ] && ok "0 でない終了コードで止まる（${rc}）" || ng "sh で読めない .env の --upgrade が成功した: $out"
      contains "$out" ".env を sh で読めません" && ok "「.env を sh で読めません」と示す" || ng "止まる文面: $out"
      contains "$out" "LOOPTRACK_EXTRA_DSN" && ok "疑わしい行のキーの名前を示す" || ng "キーの名前: $out"
      contains "$out" "SECRETMARK123" && ng "値が出力に出た: $out" || ok "値を出力しない（標準出力・標準エラーとも）"
      contains "$out" "MySQL の権限" && ng "権限の段に進んだ: $out" || ok "権限の段に進まない"
      contains "$out" "==> 停止" && ng "サービスを止める段に進んだ: $out" || ok "サービスを止める前に止まる"
      check "サービスは動いたまま" systemctl is-active looptrack
      check "実行ファイルは変わっていない" sh -c '[ "$(sha256sum /usr/local/bin/looptrack | cut -d" " -f1)" = "$1" ]' _ "$BEFORE"
      check "印の版は変わっていない" grep -q ^VERSION=v0.0.0-installtest1 /etc/looptrack/install.conf
      # 対照: 同じ値を単引用符で囲んだ .env では、この検査を通って更新が最後まで進む（下の --upgrade がそれ）
      cp -p /tmp/env.keep /etc/looptrack/.env
      printf '%s\n' "LOOPTRACK_EXTRA_DSN='lt_app:SECRETMARK123@tcp(127.0.0.1:3306)/ltdb?parseTime=true'" >>/etc/looptrack/.env
      since=$(date +%s)
      out=$(LOOPTRACK_INSTALL_ALLOW_HTTP=1 $I --upgrade --from "$UPGRADE_URL" 2>&1) || ng "upgrade が失敗: $out"
      contains "$out" ".env を sh で読めません" && ng "対照: 単引用符で囲んだ値を sh で読めないと判定した" || ok "対照: 単引用符で囲んだ値は検査を通り、更新が進む"
      contains "$out" "SECRETMARK123" && ng "対照の更新の出力に値が出た" || true
      contains "$out" "更新しました" && ok "更新の表示" || ng "更新の表示: $out"
      case "$(/usr/local/bin/looptrack version)" in "looptrack v0.0.0-installtest2（"*) ok "版が変わった" ;; *) ng "版: $(/usr/local/bin/looptrack version)" ;; esac
      check "動いている" systemctl is-active looptrack
      check "印の版" grep -q ^VERSION=v0.0.0-installtest2 /etc/looptrack/install.conf
      echo "== --upgrade の後のクライアントに配る looptrack（6 対象・署名つきの SHA256SUMS と一致）と、古い looptrack への【配布スクリプトの更新】"
      r=$(dist_matches v0.0.0-installtest2 /dist2/SHA256SUMS 6) && ok "配布ディレクトリはこの版の 6 対象だけ（SHA256SUMS は取得元のまま・どれも行と一致・前の版は片付けた）" || ng "配布ディレクトリ: $r"
      check "配布ディレクトリに .prev・一時ファイルを残さない" sh -c '[ -z "$(ls -A '"$DISTD"' | grep -v "^looptrack_\|^SHA256SUMS\|^NOTICE$\|^OFL-BIZUDGothic.txt$")" ] && [ ! -e '"$DISTD"'/SHA256SUMS.prev ]'
      TOKEN=$(admin token create --name dist-check alice 2>/dev/null) # 値は出さない（テストのコンテナの中だけで使う）
      got=$(api_binaries "$TOKEN")
      [ -n "$got" ] && [ "$got" = "$(dist_expected)" ] && [ "$(printf '%s\n' "$got" | grep -c installtest2)" = 6 ] &&
        ok "GET /api/v1/dist の binaries はこの版の 6 対象で、SHA-256 は SHA256SUMS と一致" || ng "binaries: $got"
      resp=$(post_install "$TOKEN" v0.0.0-installtest1) || ng "POST /install（古い looptrack）が失敗"
      contains "$resp" "【配布スクリプトの更新】" && contains "$resp" "配布中の最新 v0.0.0-installtest2 より古い" &&
        ok "古い looptrack の hook summary に【配布スクリプトの更新】が出る" || ng "古い looptrack の応答: $resp"
      resp=$(post_install "$TOKEN" v0.0.0-installtest2) || ng "POST /install（新しい looptrack）が失敗"
      contains "$resp" '"state":"current"' && ! contains "$resp" "【配布スクリプトの更新】" &&
        ok "対照: 配布中と同じ版の looptrack には出ない" || ng "新しい looptrack の応答: $resp"
      grep -q "$DIST_LAG" <<<"$(journalctl -u looptrack -o cat --no-pager --until "@$since")" &&
        ok "対照: 更新の前（配布物が linux/$CARCH だけ）は起動時のログに配布物の不足が出ていた" ||
        ng "前提が崩れています（更新の前に配布物の不足のログが無い）: $(journalctl -u looptrack -o cat --no-pager --until "@$since" | tail -n 15)"
      grep -q "$DIST_LAG" <<<"$(journalctl -u looptrack -o cat --no-pager --since "@$since")" &&
        ng "更新の後の起動で配布物の遅れのログが出た: $(journalctl -u looptrack -o cat --no-pager --since "@$since" | grep "$DIST_LAG")" ||
        ok "更新の後の起動では配布物の遅れを知らせない（起動の前に置いた）"
      check "SQLite の控え（控えの置き場は root 700）" sh -c 'ls -d /var/lib/looptrack/backup-*/im.db && [ "$(stat -c %U:%a /var/lib/looptrack/backup-*)" = root:700 ]'
      check "前の実行ファイル" test -x /usr/local/bin/looptrack.prev
      check "--upgrade で DB を 600 に直す" sh -c '[ "$(stat -c %U:%a /var/lib/looptrack/im.db)" = looptrack:600 ]'
      grep -q "$PERM_WARN" <<<"$(journalctl -u looptrack -o cat --no-pager --since "@$since")" && ng "更新の後に権限の警告が出た" || ok "更新の後は権限の警告を出さない"
      admin user list 2>&1 | grep -q alice && ok "データ（管理者）が残る" || ng "データが残らない"
      r=$(web_login http://127.0.0.1:8090/looptrack) && ok "更新の後も同じパスワードと二段階認証でログインできる $r" || ng "更新の後のログイン: $r"
      # 2 回目は配布物の中の install.sh で（dist.sh build の出力の並びのまま更新の経路に入れる）
      cmp -s /src/install.sh /dist2/install.sh && ok "更新先の配布物の install.sh はリポジトリの写し" || ng "/dist2/install.sh が deploy/install.sh と違う"
      out=$(sh /dist2/install.sh --upgrade --from /dist2 2>&1) || ng "同じ版の upgrade が失敗"
      contains "$out" "すでに" && ok "同じ版の upgrade は何もしない" || ng "同じ版: $out"
      # 3 回目は Releases の形（書庫）から、版を指定して（中身は同じなので何もしない）
      out=$(LOOPTRACK_INSTALL_REPO="$GH_URL" LOOPTRACK_INSTALL_ALLOW_HTTP=1 $I --upgrade --version v0.0.0-installtest2 2>&1) || ng "書庫からの同じ版の upgrade が失敗: $out"
      contains "$out" "_server.tar.gz（SHA-256 一致" && contains "$out" "すでに" && ok "書庫の中身が入っているものと同じなら何もしない" || ng "書庫の同じ版: $out"

      echo "== --uninstall"
      $I --uninstall >/dev/null 2>&1 || ng "uninstall が失敗"
      check "サービスを止めた" sh -c '! systemctl is-active looptrack'
      check "データは残る" test -f /var/lib/looptrack/im.db
      # 印を外したので --purge は使えない。次の場面（MySQL・rc.2）のために手で片付ける
      rm -rf /etc/looptrack /var/lib/looptrack /usr/local/bin/looptrack /usr/local/bin/looptrack.prev
      ;;

    mysql-nodb-decline | mysql-nodb | mysql-bad | mysql-good | mysql-upgrade | mysql-auto-pending | mysql-auto-fail | mysql-auto-ok | mysql-manual-nomig | mysql-auto-timer | mysql-fakemig-decline | mysql-fakemig-dsn | mysql-fakemig-back)
      # MySQL を最小権限で使う構成（テスト専用の MySQL のコンテナ im0151-mysql。DB ltdb と表を作る利用者 lt_migrate は
      # 手元の側で先に作ってある。アプリ用の利用者 lt_app は無い → インストーラが尋ねたついでに作る）。
      # mysql-nodb*: DB ltnew は無い（表を作る利用者 lt_newmig と、その権限だけが手元の側で先にある）→ DB もインストーラが作る。
      # 管理用のパスワード（ADMIN_PW。テスト用の値）は疑似端末から答えるだけで、引数・ファイルには書かない
      export LOOPTRACK_SETUP_DSN="lt_app:${APP_PW}@tcp(im0151-mysql:3306)/ltdb?parseTime=true"
      export LOOPTRACK_SETUP_MIGRATE_DSN="lt_migrate:${MIG_PW}@tcp(im0151-mysql:3306)/ltdb?parseTime=true"
      case $scenario in mysql-nodb*)
        export LOOPTRACK_SETUP_DSN="lt_new:${APP_PW}@tcp(im0151-mysql:3306)/ltnew?parseTime=true"
        export LOOPTRACK_SETUP_MIGRATE_DSN="lt_newmig:${MIG_PW}@tcp(im0151-mysql:3306)/ltnew?parseTime=true"
        ;;
      esac
      SETUPM=(--store mysql --public-url https://im.example.com --admin-login alice --admin-name Alice --admin-password-file /root/pw --two-factor required --project web)
      # with_tty <出力> <管理用の利用者> <パスワード> <コマンド> [<問いの文字列> <答え>]… — script の疑似端末でコマンドを動かし、
      # 「管理用の利用者」「のパスワード」の問いが出たら答え、続く問い（DB・利用者を作るかの確認）にも順に答える。
      # 答えは組込みの printf で FIFO に書く（ps に出ない）。全体は timeout で上限を持たせる
      with_tty() {
        local log=$1 user=$2 pw=$3 cmd=$4 pid rc=0
        shift 4
        rm -f /tmp/tty.in "$log"
        mkfifo /tmp/tty.in
        timeout 600 script -qfec "$cmd" "$log" </tmp/tty.in >/dev/null 2>&1 &
        pid=$!
        exec 7>/tmp/tty.in
        if wait_for "$log" "管理用の利用者" 300; then
          printf '%s\r' "$user" >&7
          if wait_for "$log" "のパスワード（表示しません" 30; then
            printf '%s\r' "$pw" >&7
            sleep 1
            ps -eo args >/tmp/ps.snap1 2>/dev/null || true
            while [ $# -ge 2 ]; do
              wait_for "$log" "$1" 60 || break
              printf '%s\r' "$2" >&7
              shift 2
            done
          fi
        fi
        wait "$pid" || rc=$?
        ps -eo args >/tmp/ps.snap2 2>/dev/null || true
        exec 7>&-
        rm -f /tmp/tty.in
        return "$rc"
      }
      # snapshot — サービスの起動の時刻・実行ファイルと印の SHA-256・.prev の時刻（前後で比べ、何も変えていないことを確かめる）
      snapshot() {
        systemctl show -p ActiveEnterTimestampMonotonic looptrack
        sha256sum /usr/local/bin/looptrack /etc/looptrack/install.conf
        ls -l --time-style=+%s /usr/local/bin/looptrack.prev 2>/dev/null || echo "（.prev なし）"
      }
      CMD="sh /src/install.sh --from /dist --yes --method systemd -- ${SETUPM[*]}"
      case $scenario in
        mysql-nodb-decline)
          echo "== MySQL（DB が無い）: DB を作るかの確認で「作らない」→ 何も作らず、何をすればよいかを示して止まる"
          with_tty /tmp/mysql-nodb-decline.log root "$ADMIN_PW" "$CMD" "DB ltnew がありません。作りますか" n &&
            ng "作らないと答えたのに最後まで進んだ" || true
          out=$(tr -d '\r' </tmp/mysql-nodb-decline.log)
          contains "$out" "DB ltnew がありません。作りますか" && ok "DB を作る前に確認を取る" || ng "確認の表示: $out"
          contains "$out" "DB ltnew を作らずに止めました" && ok "作らなかったと伝える" || ng "止まったときの表示: $out"
          contains "$out" "CREATE DATABASE ltnew CHARACTER SET utf8mb4 COLLATE utf8mb4_bin" && ok "何をすればよいか（DB の作り方）を示す" || ng "案内の表示: $out"
          contains "$out" "作成: DB\|作成: 利用者 lt_new" && ng "作らないと答えたのに作った: $out" || ok "DB も利用者も作らない"
          check "設定（.env）を書かない" sh -c '[ ! -e /etc/looptrack/.env ]'
          check "サービスを起動していない" sh -c '! systemctl is-active looptrack'
          grep -qF -- "$ADMIN_PW" /tmp/mysql-nodb-decline.log && ng "管理用のパスワードがインストーラの出力に出た" || ok "インストーラの出力に管理用のパスワードが無い"
          ;;
        mysql-nodb)
          echo "== MySQL（DB が無い）: 管理用の資格情報を尋ねたついでに、確かめてから DB とアプリ用の利用者を作り、migrate して起動まで 1 回で"
          with_tty /tmp/mysql-nodb.log root "$ADMIN_PW" "$CMD" "DB ltnew がありません。作りますか" y "アプリ用の利用者 lt_new がありません" y ||
            ng "install が失敗: $(tr -d '\r' </tmp/mysql-nodb.log | tail -n 30)"
          out=$(tr -d '\r' </tmp/mysql-nodb.log)
          contains "$out" "作成: DB ltnew" && ok "確かめてから DB を作った" || ng "DB の作成の表示: $out"
          contains "$out" "作成: 利用者 lt_new" && ok "アプリ用の利用者を作った" || ng "利用者の作成の表示: $out"
          contains "$out" "権限を与えました" && ok "権限を与えた" || ng "権限の表示: $out"
          contains "$out" "最初の管理者を作っています" && ok "setup が続きから管理者を作った（migrate が通った）" || ng "setup の続きの表示: $out"
          contains "$out" "インストールが終わりました" && ok "1 回の実行で最後まで進む" || ng "終わりの表示: $out"
          [ "$(grep -c '管理用の利用者（DB と利用者を作り' <<<"$out")" = 1 ] && ok "管理用の資格情報は 1 回だけ尋ねる" ||
            ng "管理用の資格情報を尋ねた回数: $(grep -c '管理用の利用者（DB と利用者を作り' <<<"$out")"
          check "サービスが動いている" systemctl is-active looptrack
          grep -rqF -- "$ADMIN_PW" /etc/looptrack /var/lib/looptrack /opt 2>/dev/null && ng "管理用のパスワードが設定・データの置き場に残った" || ok "設定・データの置き場に管理用のパスワードが無い"
          grep -qF -- "$ADMIN_PW" /tmp/mysql-nodb.log && ng "管理用のパスワードがインストーラの出力に出た" || ok "インストーラの出力に管理用のパスワードが無い"
          cat /root/.bash_history /root/.sh_history /root/.ash_history 2>/dev/null | grep -qF -- "$ADMIN_PW" && ng "シェルの履歴に残った" || ok "シェルの履歴に管理用のパスワードが無い"
          [ -s /tmp/ps.snap1 ] && ok "パスワードを答えた直後の ps を取った" || ng "ps を取れていない"
          grep -qF -- "$ADMIN_PW" /tmp/ps.snap1 /tmp/ps.snap2 && ng "ps の引数に管理用のパスワードが出た" || ok "ps の引数に管理用のパスワードが無い"
          grep -q "lt_new:${APP_PW}@" /etc/looptrack/.env && ok "対照: .env にはアプリ用の接続先（LOOPTRACK_DSN）がある" || ng ".env の LOOPTRACK_DSN"
          # 次の場面（DB ltdb の構成）のために外す（MySQL の DB は消さない）
          $I --uninstall --purge --yes >/dev/null 2>&1 || ng "DB が無かった場面の purge が失敗"
          ;;
        mysql-bad)
          echo "== MySQL（最小権限）: 管理用の資格情報が合わない → 権限を与えず・起動せずに止まる"
          with_tty /tmp/mysql-bad.log root "wrong-${ADMIN_PW}" "$CMD" && ng "合わない資格情報で通った" || true
          out=$(tr -d '\r' </tmp/mysql-bad.log)
          contains "$out" "保存先の確認" && ok "起動の前に保存先を確かめる" || ng "確認の表示: $out"
          contains "$out" "管理用の資格情報で im0151-mysql:3306 に接続できません（利用者 root）" && ok "何が合わなかったかを示す" || ng "合わない資格情報の表示: $out"
          contains "$out" "DB ltdb がありません" && ng "既にある DB を作るかを尋ねた: $out" || ok "対照: DB が既にあれば、作るかを尋ねない"
          contains "$out" "サービスは起動していません" && ok "起動していないと伝える" || ng "終わりの表示: $out"
          check "サービスを起動していない" sh -c '! systemctl is-active looptrack'
          check "設定は残る（もう一度実行すると続きから）" test -f /etc/looptrack/.env
          check "印は書かない（やり直しが設定済みで止まらない）" sh -c '[ ! -e /etc/looptrack/install.conf ]'
          ;;
        mysql-good)
          echo "== MySQL（最小権限）: もう一度実行 → 尋ねて権限を与え、起動と動作確認まで 1 回で"
          with_tty /tmp/mysql-good.log root "$ADMIN_PW" "$CMD" || ng "install が失敗: $(tr -d '\r' </tmp/mysql-good.log | tail -n 30)"
          out=$(tr -d '\r' </tmp/mysql-good.log)
          contains "$out" "作成済みです" && ok "setup は飛ばす（.env を使う）" || ng "setup の表示: $out"
          contains "$out" "作成: DB" && ng "既にある DB を作ろうとした: $out" || ok "対照: DB が既にあれば作らない"
          contains "$out" "作成: 利用者 lt_app" && ok "アプリ用の利用者を作った（im_app 以外の名前）" || ng "利用者の作成の表示: $out"
          contains "$out" "権限を与えました" && ok "権限を与えた" || ng "権限の表示: $out"
          contains "$out" "アプリ用の利用者 lt_app で読めました" && ok "アプリ用の利用者で読めることを確かめた" || ng "読めたの表示: $out"
          contains "$out" "200 OK" && ok "/healthz を待った" || ng "起動の表示: $out"
          contains "$out" "インストールが終わりました" && ok "1 回の実行で最後まで進む" || ng "終わりの表示: $out"
          check "サービスが動いている" systemctl is-active looptrack
          check "印（METHOD=systemd）" grep -q ^METHOD=systemd /etc/looptrack/install.conf
          # 尋ねた管理用のパスワードが残っていない（対照: SHOW GRANTS は手元の側で確かめる）
          grep -rqF -- "$ADMIN_PW" /etc/looptrack /var/lib/looptrack /opt 2>/dev/null && ng "管理用のパスワードが設定・データの置き場に残った" || ok "設定・データの置き場に管理用のパスワードが無い"
          grep -qF -- "$ADMIN_PW" /tmp/mysql-good.log /tmp/mysql-bad.log && ng "管理用のパスワードがインストーラの出力に出た" || ok "インストーラの出力に管理用のパスワードが無い"
          cat /root/.bash_history /root/.sh_history /root/.ash_history 2>/dev/null | grep -qF -- "$ADMIN_PW" && ng "シェルの履歴に残った" || ok "シェルの履歴に管理用のパスワードが無い"
          [ -s /tmp/ps.snap1 ] && ok "パスワードを答えた直後の ps を取った" || ng "ps を取れていない"
          grep -qF -- "$ADMIN_PW" /tmp/ps.snap1 /tmp/ps.snap2 && ng "ps の引数に管理用のパスワードが出た" || ok "ps の引数に管理用のパスワードが無い"
          grep -q "lt_app:${APP_PW}@" /etc/looptrack/.env && ok "対照: .env にはアプリ用の接続先（LOOPTRACK_DSN）がある" || ng ".env の LOOPTRACK_DSN"
          ;;
        mysql-auto-pending)
          echo "== MySQL: 無人の更新で、新しい版に未適用の migrate があれば置き換えない（止めない）"
          before=$(systemctl show -p ActiveEnterTimestampMonotonic looptrack)
          rc=0
          out=$($I --upgrade --from /dist2 --yes 2>&1 </dev/null) || rc=$?
          [ "$rc" != 0 ] && ok "0 でない終了コード（${rc}）" || ng "未適用の migrate があるのに 0 で終わった: $out"
          contains "$out" "未適用: " && ok "未適用の migrate を示す" || ng "未適用の表示: $out"
          contains "$out" "は DB の形を変えます" && contains "$out" "サービスは今の版 v0.0.0-installtest1 のまま動いています" && ok "置き換えない理由を示す" || ng "理由の表示: $out"
          contains "$out" "==> 停止" && ng "止めてから確かめた" || ok "止める前に確かめる"
          [ "$before" = "$(systemctl show -p ActiveEnterTimestampMonotonic looptrack)" ] && ok "サービスを止めていない（起動し直していない）" || ng "サービスが起動し直された"
          check "動いている" systemctl is-active looptrack
          check "版はそのまま（印も）" sh -c '/usr/local/bin/looptrack version | grep -q installtest1 && grep -qx VERSION=v0.0.0-installtest1 /etc/looptrack/install.conf'
          ;;
        mysql-auto-fail)
          echo "== MySQL: 無人の更新で、止めた後に失敗（権限の欠けた表）→ 前の版に戻して起動し直す"
          rc=0
          out=$($I --upgrade --from /dist2 --yes 2>&1 </dev/null) || rc=$?
          [ "$rc" != 0 ] && ok "0 でない終了コード（${rc}）" || ng "失敗したのに 0 で終わった: $out"
          contains "$out" "未適用の migrate はありません" && ok "対照: 事前の確かめは通った（DB の形は変わらない）" || ng "事前の確かめの表示: $out"
          contains "$out" "==> 停止" && ok "止めて置き換えを始めた" || ng "置き換えを始めていない: $out"
          contains "$out" "前の版 v0.0.0-installtest1 に戻して起動し直しました（新しい版 v0.0.0-installtest2 は入れていません）" && ok "前の版に戻したと示す" || ng "戻しの表示: $out"
          check "前の版で動いている" sh -c 'systemctl is-active looptrack && /usr/local/bin/looptrack version | grep -q installtest1'
          check "/healthz が 200" env LOOPTRACK_HEALTH_URL=http://127.0.0.1:8090/looptrack/healthz /usr/local/bin/looptrack healthcheck
          check "印は前の版のまま" grep -qx VERSION=v0.0.0-installtest1 /etc/looptrack/install.conf
          ;;
        mysql-auto-ok)
          echo "== MySQL: 対照: 権限がそろっていれば、無人の更新は置き換わる（取得元は …1）"
          rc=0
          out=$($I --upgrade --from /dist --yes 2>&1 </dev/null) || rc=$?
          [ "$rc" = 0 ] && ok "0 で終わる" || ng "無人の更新が失敗（${rc}）: $out"
          contains "$out" "更新しました: v0.0.0-installtest2 → v0.0.0-installtest1" && ok "置き換わった" || ng "更新の表示: $out"
          contains "$out" "前の版" && ng "戻しが走った: $out" || ok "戻しは走らない"
          check "動いている" systemctl is-active looptrack
          $I --uninstall --purge --yes >/dev/null 2>&1 || ng "MySQL の分の purge が失敗"
          ;;
        mysql-manual-nomig)
          echo "== MySQL（最小権限）: 表を作る利用者を渡さない手動の --upgrade（未適用 0 件）→ アプリ用の利用者で migrate して置き換わる（取得元は …1）"
          unset LOOPTRACK_SETUP_MIGRATE_DSN
          # 更新の場面を続けて流すと、looptrack.service の起動が systemd の既定の上限（10 秒に 5 回）に掛かって
          # start-limit-hit で起動できなくなる（1 日 1 回の timer では起きない）。数えを戻してから始める
          systemctl reset-failed looptrack
          rc=0
          out=$($I --upgrade --from /dist 2>&1 </dev/null) || rc=$?
          [ "$rc" = 0 ] && ok "0 で終わる" || ng "手動の --upgrade が失敗（${rc}）: $out"
          contains "$out" "最新です（適用するマイグレーションはありません）" && ok "migrate は未適用 0 件で通った" || ng "migrate の表示: $out"
          contains "$out" "CREATE command denied" && ng "migrate が CREATE の権限を求めた: $out" || ok "migrate が CREATE の権限を求めない"
          contains "$out" "更新しました: v0.0.0-installtest2 → v0.0.0-installtest1" && ok "置き換わった" || ng "更新の表示: $out"
          check "…1 で動いている（印も）" sh -c 'systemctl is-active looptrack && /usr/local/bin/looptrack version | grep -q installtest1 && grep -qx VERSION=v0.0.0-installtest1 /etc/looptrack/install.conf'
          ;;
        mysql-auto-timer)
          echo "== MySQL（最小権限）: timer の service（LOOPTRACK_SETUP_MIGRATE_DSN の無い環境）の無人の更新で、DB の形を変えない新しい版に置き換わる（取得元は latest が …2 の /gh2）"
          unset LOOPTRACK_SETUP_MIGRATE_DSN
          # 更新の場面を続けて流すと、looptrack.service の起動が systemd の既定の上限（10 秒に 5 回）に掛かって
          # start-limit-hit で起動できなくなる（1 日 1 回の timer では起きない）。数えを戻してから始める（終わりにも戻す）
          systemctl reset-failed looptrack
          # 偽の minisign（auto-upgrade の場面と同じ。本物の検証は dist.sh verify が確かめる）
          printf '#!/bin/sh\necho "$*" >>/root/minisign.args\nexit 0\n' >/usr/local/bin/minisign
          chmod 755 /usr/local/bin/minisign
          out=$(LOOPTRACK_INSTALL_REPO="$GH2_URL" LOOPTRACK_INSTALL_ALLOW_HTTP=1 LOOPTRACK_INSTALL_SCRIPT_URL="$RAW_URL" $I --auto-upgrade on 2>&1) || ng "on が失敗: $out"
          check "timer が有効" systemctl is-enabled looptrack-upgrade.timer
          check "service の環境に LOOPTRACK_SETUP_MIGRATE_DSN が無い（アプリ用の DSN で migrate する）" sh -c '! grep -q LOOPTRACK_SETUP_MIGRATE_DSN /etc/systemd/system/looptrack-upgrade.service'
          since=$(date +%s)
          # timer を待たずに、timer が動かす service を 1 回動かす（oneshot は終わるまで戻らない。上限 300 秒）
          timeout 300 systemctl start looptrack-upgrade.service && ok "timer の service が 0 で終わる" || ng "timer の service が失敗"
          j=$(journalctl -u looptrack-upgrade -o cat --no-pager --since "@$since" 2>/dev/null)
          contains "$j" "未適用の migrate はありません" && ok "止める前の確かめ（未適用 0 件）を通った" || ng "事前の確かめの表示: $(tail -n 30 <<<"$j")"
          contains "$j" "最新です（適用するマイグレーションはありません）\|Up to date (there is no migration to apply)" && ok "migrate は未適用 0 件で通った" || ng "migrate の表示: $(tail -n 30 <<<"$j")"
          contains "$j" "CREATE command denied" && ng "migrate が CREATE の権限を求めた: $(tail -n 30 <<<"$j")" || ok "migrate が CREATE の権限を求めない"
          contains "$j" "更新しました: v0.0.0-installtest1 → v0.0.0-installtest2" && ok "置き換わった" || ng "更新の表示: $(tail -n 30 <<<"$j")"
          contains "$j" "前の版" && ng "戻しが走った: $(tail -n 30 <<<"$j")" || ok "戻しは走らない"
          check "…2 で動いている（印も。設定は on のまま）" sh -c 'systemctl is-active looptrack && /usr/local/bin/looptrack version | grep -q installtest2 && grep -qx VERSION=v0.0.0-installtest2 /etc/looptrack/install.conf && grep -qx AUTO_UPGRADE=on /etc/looptrack/install.conf'
          # 次の場面（mysql-auto-ok）のために外し、起動の数えを戻す
          out=$($I --auto-upgrade off 2>&1) || ng "off が失敗: $out"
          rm -f /usr/local/bin/minisign /root/minisign.args
          systemctl reset-failed looptrack
          ;;
        mysql-fakemig-decline)
          # 新しい版（…3）は偽の migrate（9001_installtest_fake.sql）を足した版。DB には未適用（手元の側で確かめてから流す）。
          # アプリ用の利用者 lt_app は表を作れない（最小権限）。表を作る接続先（LOOPTRACK_SETUP_MIGRATE_DSN）は渡さない
          echo "== MySQL（最小権限）: 端末の --upgrade で、新しい版が DB の形を変え、表を作る接続先が無い → 止める前に尋ね、既定の答え（続けない）で何も変えずに止まる"
          unset LOOPTRACK_SETUP_MIGRATE_DSN
          systemctl reset-failed looptrack
          snap=$(snapshot)
          vbefore=$(/usr/local/bin/looptrack version)
          rc=0
          tty_run /tmp/fakemig-a.log "sh /src/install.sh --upgrade --from /dist3" "それでもサービスを止めて続けますか" "" || rc=$?
          out=$(tr -d '\r' </tmp/fakemig-a.log)
          [ "$rc" != 0 ] && ok "0 でない終了コード（${rc}）" || ng "続けないと答えたのに 0 で終わった: $out"
          contains "$out" "止める前の確認" && ok "止める前に確かめる" || ng "確認の表示: $out"
          contains "$out" "未適用: 9001_installtest_fake.sql" && ok "未適用の migrate の一覧を示す" || ng "未適用の表示: $out"
          contains "$out" "サービスを止めた後に migrate が失敗し、止まったまま残ります" && ok "止めた後に失敗する理由を示す" || ng "理由の表示: $out"
          contains "$out" "それでもサービスを止めて続けますか" && ok "続けるかを尋ねる" || ng "問いの表示: $out"
          contains "$out" "止めずに終わりました" && contains "$out" "サービスは今の版 v0.0.0-installtest2 のまま動いています" && ok "止めなかったと伝える" || ng "終わりの表示: $out"
          contains "$out" "LOOPTRACK_SETUP_MIGRATE_DSN で渡して" && contains "$out" "docs/server/DEPLOY.md の「更新（--upgrade）」" && ok "渡し方（DEPLOY.md の節）を案内する" || ng "案内の表示: $out"
          contains "$out" "==> 停止" && ng "止めてから確かめた: $out" || ok "止めていない（停止の段に進まない）"
          grep -qx '==> 実行ファイル' <<<"$out" && ng "実行ファイルの段に進んだ: $out" || ok "実行ファイルの段（置き換え）に進まない"
          [ "$snap" = "$(snapshot)" ] && ok "サービスは起動し直しておらず、実行ファイル・印・.prev は変わっていない" || ng "変わった: 前 $snap / 後 $(snapshot)"
          check "動いている" systemctl is-active looptrack
          check "/healthz が 200" env LOOPTRACK_HEALTH_URL=http://127.0.0.1:8090/looptrack/healthz /usr/local/bin/looptrack healthcheck
          [ "$(/usr/local/bin/looptrack version)" = "$vbefore" ] && contains "$vbefore" "installtest2" && ok "looptrack version は前の版（${vbefore}）" || ng "版: $vbefore → $(/usr/local/bin/looptrack version)"
          check "印の版も前の版" grep -qx VERSION=v0.0.0-installtest2 /etc/looptrack/install.conf

          echo "== --yes（端末あり）: 尋ねずに既定の答え（続けない）で、何も変えずに止まる"
          rc=0
          tty_run /tmp/fakemig-a2.log "sh /src/install.sh --upgrade --from /dist3 --yes" || rc=$?
          out=$(tr -d '\r' </tmp/fakemig-a2.log)
          [ "$rc" != 0 ] && ok "0 でない終了コード（${rc}）" || ng "--yes で 0 で終わった: $out"
          contains "$out" "未適用: 9001_installtest_fake.sql" && ok "未適用の migrate の一覧を示す" || ng "未適用の表示: $out"
          contains "$out" "--yes なので尋ねず、既定の答え（続けない）にします" && ok "尋ねずに既定の答えにすると示す" || ng "--yes の表示: $out"
          contains "$out" "それでもサービスを止めて続けますか" && ng "--yes なのに尋ねた: $out" || ok "--yes では尋ねない"
          contains "$out" "==> 停止" && ng "止めた: $out" || ok "止めていない"
          [ "$snap" = "$(snapshot)" ] && ok "サービス・実行ファイル・印・.prev は変わっていない" || ng "変わった: 前 $snap / 後 $(snapshot)"
          ;;
        mysql-fakemig-dsn)
          # 対照: 上と同じ版・同じ DB の状態から。表を作る接続先と管理用のパスワードは、dev の手順書（DEPLOY-internal.md の
          # 「更新（公開版だけ）」）と同じ組み方で渡す: root だけが読む .env から sh -c の中で組み、管理用のパスワードは
          # umask 077 の一時ファイルで渡して trap で消す。dev の /opt/mysql/.env の代わりを置く（値は組込みの printf で書く）
          echo "== 対照: 同じ版に、root だけが読む .env から組んだ LOOPTRACK_SETUP_MIGRATE_DSN と管理用のパスワードのファイルを渡すと、尋ねずに上がる"
          unset LOOPTRACK_SETUP_MIGRATE_DSN
          systemctl reset-failed looptrack
          mkdir -p /opt/mysql
          (umask 077 && printf 'MYSQL_ROOT_PASSWORD=%s\n' "$ADMIN_PW" >/opt/mysql/.env)
          # 手順書の 1 行の、curl の代わり（cat）と取得元（--from /dist3）だけを変えたもの。sh -c の中身は手順書と同じ
          cat >/root/upgrade-with-root.sh <<'UPEOF'
cat /src/install.sh | sh -c 'umask 077; pwf=$(mktemp) || exit 1; trap "rm -f \"\$pwf\"" EXIT; trap "exit 130" INT TERM HUP
q=$(printf "\047")
p=$(sed -n "s/^MYSQL_ROOT_PASSWORD=//p" /opt/mysql/.env | tail -n 1); case $p in "$q"*"$q" | \"*\") p=${p#?}; p=${p%?} ;; esac
d=$(sed -n "s/^LOOPTRACK_DSN=//p" /etc/looptrack/.env | tail -n 1); case $d in "$q"*"$q" | \"*\") d=${d#?}; d=${d%?} ;; esac
[ -n "$p" ] && [ -n "$d" ] || { echo "MYSQL_ROOT_PASSWORD か LOOPTRACK_DSN を読めません" >&2; exit 1; }
printf "%s\n" "$p" >"$pwf"
LOOPTRACK_SETUP_MIGRATE_DSN="root:$p@${d##*@}" LOOPTRACK_INSTALL_DB_ADMIN_USER=root LOOPTRACK_INSTALL_DB_ADMIN_PASSWORD_FILE=$pwf sh -s -- --upgrade --from /dist3'
UPEOF
          ls /tmp/tmp.* >/dev/null 2>&1 && ng "前提: /tmp/tmp.* が既にある" || ok "前提: /tmp/tmp.* は無い"
          rc=0
          tty_run /tmp/fakemig-b.log "sh /root/upgrade-with-root.sh" || rc=$?
          out=$(tr -d '\r' </tmp/fakemig-b.log)
          [ "$rc" = 0 ] && ok "0 で終わる" || ng "上がらない（${rc}）: $(tail -n 30 <<<"$out")"
          contains "$out" "未適用: 9001_installtest_fake.sql" && ok "止める前に未適用の migrate を示す" || ng "未適用の表示: $out"
          contains "$out" "止めた後に表を作れる接続先（LOOPTRACK_SETUP_MIGRATE_DSN）で流します" && ok "表を作る接続先があるので進むと示す" || ng "進む表示: $out"
          contains "$out" "それでもサービスを止めて続けますか" && ng "表を作る接続先があるのに尋ねた: $out" || ok "尋ねない"
          grep -q '^適用: 9001_installtest_fake.sql' <<<"$out" && ok "偽の migrate を適用した（表を作る接続先で）" || ng "migrate の表示: $out"
          contains "$out" "権限を与えました" && ok "管理用のパスワードのファイルで権限を与え直した（尋ねない）" || ng "権限の表示: $out"
          contains "$out" "管理用の利用者（" && ng "管理用の資格情報を尋ねた: $out" || ok "管理用の資格情報を尋ねない"
          contains "$out" "読めました" && ok "アプリ用の利用者で読めることを確かめた" || ng "読めたの表示: $out"
          contains "$out" "更新しました: v0.0.0-installtest2 → v0.0.0-installtest3" && ok "上がった" || ng "更新の表示: $out"
          check "…3 で動いている（印も）" sh -c 'systemctl is-active looptrack && /usr/local/bin/looptrack version | grep -q installtest3 && grep -qx VERSION=v0.0.0-installtest3 /etc/looptrack/install.conf'
          check "/healthz が 200" env LOOPTRACK_HEALTH_URL=http://127.0.0.1:8090/looptrack/healthz /usr/local/bin/looptrack healthcheck
          check "アプリ用の利用者（.env の LOOPTRACK_DSN）で読める" admin project list
          ls /tmp/tmp.* >/dev/null 2>&1 && ng "管理用のパスワードの一時ファイルが残った" || ok "管理用のパスワードの一時ファイルは消えた"
          grep -rqF -D skip -- "$ADMIN_PW" /etc/looptrack /var/lib/looptrack /tmp /root 2>/dev/null && ng "管理用のパスワードが /opt/mysql/.env の外（設定・データ・/tmp・/root・出力）に残った" ||
            ok "管理用のパスワードは /opt/mysql/.env の外（設定・データ・/tmp・/root・出力）に無い"
          grep -qF -- "$ADMIN_PW" /opt/mysql/.env && ok "対照: /opt/mysql/.env には管理用のパスワードがある（検索は働いている）" || ng "前提: /opt/mysql/.env"
          cat /root/.bash_history /root/.sh_history 2>/dev/null | grep -qF -- "$ADMIN_PW" && ng "シェルの履歴に残った" || ok "シェルの履歴に管理用のパスワードが無い"
          rm -rf /opt/mysql /root/upgrade-with-root.sh
          ;;
        mysql-fakemig-back)
          # 手元の側で偽の migrate の記録と表を外した後に、…2 へ戻す（次の場面は …2 から始まる）
          echo "== 片付け: 偽の migrate を外した DB で、…2 に戻す（無人の更新）"
          systemctl reset-failed looptrack
          rc=0
          out=$($I --upgrade --from /dist2 --yes 2>&1 </dev/null) || rc=$?
          [ "$rc" = 0 ] && ok "0 で終わる" || ng "…2 に戻せない（${rc}）: $out"
          contains "$out" "更新しました: v0.0.0-installtest3 → v0.0.0-installtest2" && ok "…2 に戻った" || ng "更新の表示: $out"
          check "…2 で動いている" sh -c 'systemctl is-active looptrack && /usr/local/bin/looptrack version | grep -q installtest2'
          systemctl reset-failed looptrack
          ;;
        mysql-upgrade)
          echo "== MySQL（最小権限）: 権限の欠けた表がある状態の --upgrade（表が増えた更新と同じ症状）"
          with_tty /tmp/mysql-up.log root "$ADMIN_PW" "sh /src/install.sh --upgrade --from /dist2" || ng "upgrade が失敗: $(tr -d '\r' </tmp/mysql-up.log | tail -n 30)"
          out=$(tr -d '\r' </tmp/mysql-up.log)
          contains "$out" "権限を与えました" && ok "更新でも尋ねて権限を与え直す" || ng "更新の権限の表示: $out"
          contains "$out" "更新しました" && ok "更新が終わる" || ng "更新の表示: $out"
          check "サービスが動いている" systemctl is-active looptrack
          grep -qF -- "$ADMIN_PW" /tmp/mysql-up.log && ng "更新の出力に管理用のパスワードが出た" || ok "更新の出力に管理用のパスワードが無い"
          ;;
      esac
      ;;

    rc2)
      echo "== 1.0.0-rc.2 の install.sh で入れたサーバを、新しいインストーラの --upgrade で上げる"
      out=$(sh /old/install.sh --from /dist --yes --method systemd -- "${SETUP[@]}" 2>&1) || ng "rc.2 の install.sh で入らない: $out"
      contains "$out" "インストールが終わりました" && ok "rc.2 の install.sh で入る" || ng "rc.2 の表示: $out"
      cp -p /etc/looptrack/.env /tmp/env.rc2
      out=$(LOOPTRACK_INSTALL_REPO="$GH_URL" LOOPTRACK_INSTALL_ALLOW_HTTP=1 $I --upgrade --version v0.0.0-installtest2 2>&1) || ng "新しいインストーラの --upgrade が失敗: $out"
      contains "$out" "更新しました: v0.0.0-installtest1 → v0.0.0-installtest2" && ok "rc.2 の install.conf を読んで上げる" || ng "更新の表示: $out"
      case "$(/usr/local/bin/looptrack version)" in "looptrack v0.0.0-installtest2（"*) ok "版が変わった" ;; *) ng "版: $(/usr/local/bin/looptrack version)" ;; esac
      check "動いている" systemctl is-active looptrack
      # .env は元の行をそのまま残し、足すのはクライアントに配る looptrack の置き場（LOOPTRACK_DIST_DIR）の 1 行と注釈だけ
      n=$(wc -l </tmp/env.rc2)
      [ "$(head -n "$n" /etc/looptrack/.env)" = "$(cat /tmp/env.rc2)" ] && ok ".env の元の行はそのまま" || ng ".env の元の行が変わった"
      [ "$(tail -n +"$((n + 1))" /etc/looptrack/.env | grep -v '^#')" = "LOOPTRACK_DIST_DIR='$DISTD'" ] &&
        ok ".env に足したのは LOOPTRACK_DIST_DIR の 1 行だけ" || ng ".env に足した行: $(tail -n +"$((n + 1))" /etc/looptrack/.env)"
      curl -fsS "$GH_URL/releases/download/v0.0.0-installtest2/SHA256SUMS" >/tmp/gh-v2.sums
      r=$(dist_matches v0.0.0-installtest2 /tmp/gh-v2.sums 6 2>&1) || true
      [ -z "$r" ] && ok "rc.2 で入れたサーバも --upgrade で配布ディレクトリが 6 対象になる" || ng "rc.2 の後の配布ディレクトリ: $r"
      admin user list 2>&1 | grep -q alice && ok "データ（管理者）が残る" || ng "データが残らない"
      $I --uninstall --purge --yes >/dev/null 2>&1 || ng "rc.2 の分の purge が失敗"
      ;;

    auto-upgrade)
      echo "== 自動の置き換え（--auto-upgrade。既定は off・systemd timer）"
      upgrade_journal() { journalctl -u looptrack-upgrade -o cat --no-pager --since "@$1" 2>/dev/null; }
      run_timer_service() { # timer を待たずに、timer が動かす service を 1 回動かす（oneshot は終わるまで戻らない。上限 300 秒）
        timeout 300 systemctl start looptrack-upgrade.service
      }
      version_is() { case "$(/usr/local/bin/looptrack version)" in "looptrack $1（"*) return 0 ;; esac; return 1; }
      out=$($I --from /dist --yes --method systemd -- "${SETUP[@]}" 2>&1) || ng "install が失敗: $out"
      check "既定は off（印）" grep -qx AUTO_UPGRADE=off /etc/looptrack/install.conf
      check "既定は timer を置かない" sh -c '[ ! -e /etc/systemd/system/looptrack-upgrade.timer ] && ! systemctl is-enabled looptrack-upgrade.timer'
      contains "$out" "自動の置き換え: 無効（既定。" && ok "入れた直後は無効と案内する" || ng "無効の案内: $out"

      echo "== minisign が無ければ on は止まる"
      command -v minisign >/dev/null 2>&1 && ng "前提: このコンテナに minisign がある" || true
      out=$($I --auto-upgrade on 2>&1) && ng "minisign なしで on が通った" || true
      contains "$out" "minisign を入れてください" && ok "署名を確かめられないので止まる" || ng "minisign なしの表示: $out"
      contains "$out" "dnf install -y epel-release && dnf install -y minisign" && ok "minisign の入れ方に dnf（EPEL）も並べる" || ng "minisign の dnf の案内: $out"
      check "止まったときは何も置かない（印は off のまま）" sh -c 'grep -qx AUTO_UPGRADE=off /etc/looptrack/install.conf && [ ! -e /etc/systemd/system/looptrack-upgrade.timer ] && [ ! -e /usr/local/lib/looptrack/auto-upgrade ]'

      echo "== curl も wget も無ければ on は止まる（理由は新しい版の書庫・署名を取るため。timer は install.sh を取り直さない）"
      # 偽の minisign（下の on と同じ）を置き、curl を一時的に外す（このコンテナに wget は無い）
      printf '#!/bin/sh\nexit 0\n' >/usr/local/bin/minisign
      chmod 755 /usr/local/bin/minisign
      command -v wget >/dev/null 2>&1 && ng "前提が崩れています（このコンテナに wget がある）" || true
      CURL=$(command -v curl)
      mv "$CURL" "$CURL.hidden"
      out=$($I --auto-upgrade on 2>&1) && ng "curl も wget も無いのに on が通った" || true
      mv "$CURL.hidden" "$CURL"
      contains "$out" "curl か wget が要ります" && ok "curl も wget も無ければ止まる" || ng "curl・wget なしの表示: $out"
      contains "$out" "新しい版の書庫・SHA256SUMS・その署名を取るので" && ok "理由は書庫・SHA256SUMS・署名を取るため" || ng "理由の表示: $out"
      contains "$out" "取り直す" && ng "スクリプトを取り直すと読める理由が残っている: $out" || ok "スクリプトを取り直すとは書かない"
      contains "$out" "dnf install -y curl" && ok "dnf の入れ方も並べる" || ng "dnf の案内: $out"
      check "止まったときは何も置かない（curl・wget なし）" sh -c 'grep -qx AUTO_UPGRADE=off /etc/looptrack/install.conf && [ ! -e /etc/systemd/system/looptrack-upgrade.timer ] && [ ! -e /usr/local/lib/looptrack/auto-upgrade ]'
      rm -f /usr/local/bin/minisign

      echo "== on（入れた後のサーバに、1 行（curl … | sh）で。取得元は新しい版が latest の /gh2）"
      # 偽の minisign: 呼ばれた引数を控えて、合ったことにする（本物の検証は dist.sh verify が確かめる）
      printf '#!/bin/sh\necho "$*" >>/root/minisign.args\nexit 0\n' >/usr/local/bin/minisign
      chmod 755 /usr/local/bin/minisign
      out=$(curl -fsSL "$RAW_URL" | LOOPTRACK_INSTALL_REPO="$GH2_URL" LOOPTRACK_INSTALL_ALLOW_HTTP=1 LOOPTRACK_INSTALL_SCRIPT_URL="$RAW_URL" sh -s -- --auto-upgrade on 2>&1) || ng "on が失敗: $out"
      contains "$out" "自動の置き換えを有効にしました" && ok "on の表示" || ng "on の表示: $out"
      check "印は AUTO_UPGRADE=on" grep -qx AUTO_UPGRADE=on /etc/looptrack/install.conf
      check "timer が有効で動いている" sh -c 'systemctl is-enabled looptrack-upgrade.timer && systemctl is-active looptrack-upgrade.timer'
      check "timer は 1 日 1 回・ずらす・止まっていた分を補う" sh -c 'grep -qx OnCalendar=daily /etc/systemd/system/looptrack-upgrade.timer && grep -q ^RandomizedDelaySec= /etc/systemd/system/looptrack-upgrade.timer && grep -qx Persistent=true /etc/systemd/system/looptrack-upgrade.timer'
      COPY=/usr/local/lib/looptrack/install.sh
      check "service は oneshot で手元の写しを署名必須・新しい版だけで動かす（取り直さない）" sh -c "grep -qx Type=oneshot /etc/systemd/system/looptrack-upgrade.service && grep -qx 'ExecStart=/bin/sh $COPY --upgrade --require-signature --yes --only-newer' /etc/systemd/system/looptrack-upgrade.service"
      check "service は取得元を書き写す" grep -qxF "Environment=\"LOOPTRACK_INSTALL_REPO=$GH2_URL\"" /etc/systemd/system/looptrack-upgrade.service
      check "写しは root 755（ディレクトリも root 755）" sh -c "[ \"\$(stat -c %U:%a $COPY)\" = root:755 ] && [ \"\$(stat -c %U:%a /usr/local/lib/looptrack)\" = root:755 ]"
      cmp -s /src/install.sh "$COPY" && ok "1 行で動かしたときの写しは、同じ URL の install.sh" || ng "写しが deploy/install.sh と違う"
      out=$($I --from /dist --yes 2>&1) || ng "2 回目が失敗"
      contains "$out" "自動の置き換え: 有効（1 日 1 回" && ok "2 回目（設定済み）は有効と案内する" || ng "2 回目の案内: $out"

      echo "== timer の service を動かす → 新しい版に上がる（写しは新しくしない）"
      echo '# probe: timer の回は写しを書き換えない' >>"$COPY"
      since=$(date +%s)
      run_timer_service && ok "timer の service が 0 で終わる" || ng "timer の service が失敗: $(upgrade_journal "$since" | tail -n 30)"
      version_is v0.0.0-installtest2 && ok "新しい版に上がった" || ng "版: $(/usr/local/bin/looptrack version)"
      check "サービスが動いている" systemctl is-active looptrack
      upgrade_journal "$since" | grep -q "SHA256SUMS の署名を確かめました" && ok "署名を確かめてから置き換えた" || ng "署名の表示: $(upgrade_journal "$since" | tail -n 30)"
      upgrade_journal "$since" | grep -q "更新しました: v0.0.0-installtest1 → v0.0.0-installtest2" && ok "更新の記録が journal に残る" || ng "更新の表示: $(upgrade_journal "$since" | tail -n 30)"
      check "印は新しい版で、設定は on のまま" sh -c 'grep -qx VERSION=v0.0.0-installtest2 /etc/looptrack/install.conf && grep -qx AUTO_UPGRADE=on /etc/looptrack/install.conf'
      # 無人の更新（書庫・署名必須）でも、クライアントに配る looptrack が 6 対象になる（windows は zip から取り出して照合）
      curl -fsS "$GH2_URL/releases/latest/download/SHA256SUMS" >/tmp/gh2.sums
      curl -fsS "$GH2_URL/releases/latest/download/SHA256SUMS.minisig" >/tmp/gh2.minisig
      r=$(dist_matches v0.0.0-installtest2 /tmp/gh2.sums 6) && ok "timer の更新で配布ディレクトリが新しい版の 6 対象（書庫から取り出し、署名つきの SHA256SUMS と一致）" || ng "timer の更新の配布ディレクトリ: $r"
      check "署名（SHA256SUMS.minisig）も取得元のまま置く" cmp -s "$DISTD/SHA256SUMS.minisig" /tmp/gh2.minisig
      check "更新の後も timer は有効" systemctl is-enabled looptrack-upgrade.timer
      admin user list 2>&1 | grep -q alice && ok "データ（管理者）が残る" || ng "データが残らない"
      since=$(date +%s)
      run_timer_service || ng "2 回目の timer の service が失敗"
      upgrade_journal "$since" | grep -q "すでに v0.0.0-installtest2 です" && ok "同じ版なら何もしない" || ng "同じ版の表示: $(upgrade_journal "$since" | tail -n 20)"
      grep -q '^# probe: timer の回は写しを書き換えない' "$COPY" && ok "timer の回は写しを新しくしない" || ng "timer の回で写しが書き換わった"
      out=$(LOOPTRACK_INSTALL_REPO="$GH2_URL" LOOPTRACK_INSTALL_ALLOW_HTTP=1 $I --upgrade 2>&1) || ng "人の --upgrade が失敗: $out"
      cmp -s /src/install.sh "$COPY" && ok "人が --upgrade を動かすと、写しをそのスクリプトにそろえる" || ng "人の --upgrade の後も写しが古い"

      echo "== 古い版の取得元では版を下げない（--only-newer）"
      out=$(LOOPTRACK_INSTALL_REPO="$GH_URL" LOOPTRACK_INSTALL_ALLOW_HTTP=1 LOOPTRACK_INSTALL_SCRIPT_URL="$RAW_URL" $I --auto-upgrade on 2>&1) || ng "取得元の差し替えが失敗: $out"
      dist_snap() { (cd "$DISTD" && ls -la --time-style=full-iso . && sha256sum ./*); }
      dsnap=$(dist_snap)
      since=$(date +%s)
      run_timer_service || ng "古い取得元の timer の service が失敗"
      # 対照: 上の timer の更新（…1 → …2）では配布ディレクトリが置き換わっている（dist_matches の確認）
      [ "$dsnap" = "$(dist_snap)" ] && ok "置き換えないとき（--only-newer）は配布ディレクトリも変えない" || ng "--only-newer で配布ディレクトリが変わった"
      upgrade_journal "$since" | grep -q "取得した版 v0.0.0-installtest1 は入っている版 v0.0.0-installtest2 より新しくないので、置き換えません" &&
        ok "古い版には置き換えない" || ng "--only-newer の表示: $(upgrade_journal "$since" | tail -n 20)"
      version_is v0.0.0-installtest2 && ok "版はそのまま" || ng "版が変わった: $(/usr/local/bin/looptrack version)"
      check "サービスは動いたまま" systemctl is-active looptrack

      echo "== off（timer を止めて外す）"
      out=$($I --auto-upgrade off 2>&1) || ng "off が失敗: $out"
      contains "$out" "無効にしました" && ok "off の表示" || ng "off の表示: $out"
      check "印は AUTO_UPGRADE=off" grep -qx AUTO_UPGRADE=off /etc/looptrack/install.conf
      check "timer・service・写しを外した" sh -c '! systemctl is-enabled looptrack-upgrade.timer && [ ! -e /etc/systemd/system/looptrack-upgrade.timer ] && [ ! -e /etc/systemd/system/looptrack-upgrade.service ] && [ ! -e /usr/local/lib/looptrack ]'
      check "looptrack のサービスはそのまま" systemctl is-active looptrack

      echo "== on のまま --uninstall すると外れる"
      out=$(LOOPTRACK_INSTALL_REPO="$GH2_URL" LOOPTRACK_INSTALL_ALLOW_HTTP=1 LOOPTRACK_INSTALL_SCRIPT_URL="$RAW_URL" $I --auto-upgrade on 2>&1) || ng "on（2 回目）が失敗: $out"
      $I --uninstall --purge --yes >/dev/null 2>&1 || ng "purge が失敗"
      check "--uninstall で timer も外れる" sh -c '! systemctl is-enabled looptrack-upgrade.timer && [ ! -e /etc/systemd/system/looptrack-upgrade.timer ] && [ ! -e /usr/local/lib/looptrack ]'
      check "--uninstall で配布ディレクトリも外れる" sh -c '[ ! -e /usr/local/share/looptrack ]'
      grep -q -- "-V -q -P .* -m .*/SHA256SUMS -x .*/SHA256SUMS.minisig" /root/minisign.args && ok "minisign で SHA256SUMS を確かめた" || ng "minisign の引数: $(cat /root/minisign.args 2>&1)"
      rm -f /usr/local/bin/minisign /root/minisign.args
      ;;

    auto-rollback)
      echo "== 無人の更新（--yes・端末なし。timer と同じ）: 止めた後にどの形で終わっても、前の版に 1 回だけ戻して起動し直す"
      version_is() { case "$(/usr/local/bin/looptrack version)" in "looptrack $1（"*) return 0 ;; esac; return 1; }
      HEAD_LINE='前の版 v0.0.0-installtest1 に戻して起動し直します（無人の更新なので'
      DONE_LINE='前の版 v0.0.0-installtest1 に戻して起動し直しました（新しい版 v0.0.0-installtest2 は入れていません）'
      rolled_back() { # <場面の名前> <出力> <終了コード> — 戻しが 1 回だけ走り、前の版で動き、0 でない終了コードで終わった
        local name=$1 o=$2 code=$3
        [ "$code" != 0 ] && ok "$name: 0 でない終了コード（${code}）" || ng "$name: 失敗したのに 0 で終わった: $o"
        contains "$o" "==> 停止" && ok "$name: 止めて置き換えを始めた" || ng "$name: 置き換えを始めていない: $o"
        [ "$(grep -c -- "$HEAD_LINE" <<<"$o")" = 1 ] && ok "$name: 戻しは 1 回だけ走る" || ng "$name: 戻しの回数が 1 でない（$(grep -c -- "$HEAD_LINE" <<<"$o")）: $o"
        contains "$o" "$DONE_LINE" && ok "$name: 前の版に戻したと示す" || ng "$name: 戻しの表示: $o"
        contains "$o" "でも起動できません" && ng "$name: 前の版でも起動できなかった: $o" || true
        check "$name: 前の版で動いている" sh -c 'systemctl is-active looptrack && /usr/local/bin/looptrack version | grep -q installtest1'
        check "$name: /healthz が 200" env LOOPTRACK_HEALTH_URL=http://127.0.0.1:8090/looptrack/healthz /usr/local/bin/looptrack healthcheck
        check "$name: 印は前の版のまま" grep -qx VERSION=v0.0.0-installtest1 /etc/looptrack/install.conf
        r=$(dist_matches v0.0.0-installtest1 /dist/SHA256SUMS 1) && ok "$name: 配布ディレクトリも前の版の配布のまま（戻した）" || ng "$name: 配布ディレクトリ: $r"
      }
      unattended_upgrade() { # 出力を out・終了コードを rc に。起動を短い間に重ねると systemd の上限（10 秒に 5 回）に掛かるので、数えを戻してから
        systemctl reset-failed looptrack
        rc=0
        out=$($I --upgrade --from /dist2 --yes 2>&1 </dev/null) || rc=$?
      }
      out=$($I --from /dist --yes --method systemd -- "${SETUP[@]}" 2>&1) || ng "install が失敗: $out"
      check "前提: …1 で動いている" sh -c 'systemctl is-active looptrack && /usr/local/bin/looptrack version | grep -q installtest1'

      echo "== 新しい版が起動しない（systemctl start looptrack の失敗）"
      # 新しい版（…2）だけを起動させない unit の追加設定（ExecStartPre が失敗すると systemctl start が失敗する。前の版は起動できる）
      REFUSE=/etc/systemd/system/looptrack.service.d/zz-test-refuse-new.conf
      mkdir -p "${REFUSE%/*}"
      printf '[Service]\nExecStartPre=+/bin/sh -c "! /usr/local/bin/looptrack version | grep -q installtest2"\n' >"$REFUSE"
      systemctl daemon-reload
      unattended_upgrade
      contains "$out" "エラー: 新しい版 v0.0.0-installtest2 を起動できません（systemctl start looptrack が失敗しました" &&
        ok "起動の失敗を戻しの理由として 1 行残す" || ng "起動の失敗の理由: $out"
      contains "$out" "DB を控え " && ok "SQLite の DB を止めた直後の控えに戻す" || ng "DB の戻しの表示: $out"
      rolled_back "起動の失敗" "$out" "$rc"

      # 1 回だけ失敗させる偽物（/usr/local/sbin・/usr/local/bin は PATH で /usr/bin より前）。印のファイルがあるときだけ失敗し、印を消す
      REAL_SYSTEMCTL=$(command -v systemctl)
      printf '#!/bin/sh\nif [ "$1" = "$(cat /root/systemctl.fail-once 2>/dev/null)" ]; then rm -f /root/systemctl.fail-once; echo "偽の systemctl: $1 を失敗させました" >&2; exit 1; fi\nexec %s "$@"\n' "$REAL_SYSTEMCTL" >/usr/local/sbin/systemctl
      printf '#!/bin/sh\nif [ -e /root/install.fail-once ]; then m=$(cat /root/install.fail-once); rm -f /root/install.fail-once\n  if [ "$m" = term ]; then kill -TERM "$PPID"; sleep 1; fi\n  echo "偽の install: 失敗させました" >&2; exit 1\nfi\nexec /usr/bin/install "$@"\n' >/usr/local/bin/install
      chmod 755 /usr/local/sbin/systemctl /usr/local/bin/install

      echo "== systemctl daemon-reload の失敗（die）"
      echo daemon-reload >/root/systemctl.fail-once
      unattended_upgrade
      contains "$out" "偽の systemctl: daemon-reload を失敗させました" && ok "前提: daemon-reload が失敗した" || ng "前提が崩れています（daemon-reload が失敗していない）: $out"
      contains "$out" "エラー: systemctl daemon-reload に失敗しました" && ok "daemon-reload の失敗を理由として残す" || ng "daemon-reload の理由: $out"
      rolled_back "daemon-reload の失敗" "$out" "$rc"

      echo "== die を通らない裸のコマンドの失敗（set -e で止まる。実行ファイルを置く install の失敗）"
      echo fail >/root/install.fail-once
      unattended_upgrade
      contains "$out" "偽の install: 失敗させました" && ok "前提: install が失敗した" || ng "前提が崩れています（install が失敗していない）: $out"
      contains "$out" "エラー: 止めた後の手順が途中で失敗しました（終了コード " && ok "裸の失敗も理由として 1 行残す" || ng "裸の失敗の理由: $out"
      rolled_back "裸のコマンドの失敗" "$out" "$rc"

      echo "== 止めた後の中断（TERM）"
      echo term >/root/install.fail-once
      unattended_upgrade
      contains "$out" "中断しました" && ok "前提: 中断した" || ng "前提が崩れています（中断していない）: $out"
      rolled_back "中断" "$out" "$rc"
      rm -f /usr/local/sbin/systemctl /usr/local/bin/install /root/systemctl.fail-once /root/install.fail-once

      echo "== 対照: 端末のある手動の --upgrade（--yes なし）は戻さない（今の振る舞い）"
      systemctl reset-failed looptrack
      rc=0
      out=$($I --upgrade --from /dist2 2>&1 </dev/null) || rc=$?
      [ "$rc" != 0 ] && ok "手動: 0 でない終了コード（${rc}）" || ng "手動: 起動できないのに 0 で終わった: $out"
      contains "$out" "エラー: 新しい版 v0.0.0-installtest2 を起動できません" && ok "手動: 起動の失敗を示す" || ng "手動: 起動の失敗の表示: $out"
      contains "$out" "前の版" && ng "手動なのに戻した: $out" || ok "手動: 戻さない"
      systemctl is-active looptrack >/dev/null 2>&1 && ng "手動: サービスが動いている（起動の失敗を作れていない）" || ok "手動: サービスは止まったまま"
      version_is v0.0.0-installtest2 && ok "手動: 実行ファイルは新しい版のまま" || ng "手動: 版: $(/usr/local/bin/looptrack version)"
      rm -f "$REFUSE"
      rmdir "${REFUSE%/*}" 2>/dev/null || true
      systemctl daemon-reload
      systemctl reset-failed looptrack
      $I --uninstall --purge --yes >/dev/null 2>&1 || ng "purge が失敗"
      ;;

    port-taken)
      echo "== 同じポートの /healthz に別のプロセスが 200 を返す: looptrack を起こす前に止まり、完了を示さない"
      version_is() { case "$(/usr/local/bin/looptrack version)" in "looptrack $1（"*) return 0 ;; esac; return 1; }
      # 偽の応答者は、systemd の外で手で起こした nginx（止め忘れた古いコンテナの代わり）。127.0.0.1:8090 の /looptrack/healthz に 200 を返す。
      # サービスの nginx（上の systemd の場面の設定例）とは別の設定・pid で起こし、そちらには触らない
      FAKE=/root/fake-nginx.conf
      printf 'pid /root/fake-nginx.pid;\nerror_log /root/fake-nginx.err;\nevents {}\nhttp {\n    access_log off;\n    server {\n        listen 127.0.0.1:8090;\n        location = /looptrack/healthz { return 200 fake; }\n    }\n}\n' >"$FAKE"
      fake_on() {
        nginx -c "$FAKE" || ng "偽の応答者を起こせない: $(tail -n 5 /root/fake-nginx.err 2>&1)"
      }
      fake_off() {
        nginx -c "$FAKE" -s quit || ng "偽の応答者を止められない: $(tail -n 5 /root/fake-nginx.err 2>&1)"
        for _ in $(seq 1 20); do [ -e /root/fake-nginx.pid ] || return 0; sleep 0.5; done
        ng "偽の応答者が 10 秒で止まらない"
      }
      fake_answers() { # 127.0.0.1:8090/looptrack/healthz の 200 を返しているのが nginx
        curl -s -D - -o /dev/null http://127.0.0.1:8090/looptrack/healthz | tr -d '\r' |
          awk 'NR == 1 && $2 == 200 { s = 1 } tolower($1) == "server:" && $2 ~ /^nginx/ { n = 1 } END { exit !(s && n) }'
      }
      run_i() { # <install.sh の引数…> — 端末なしで。出力を out・終了コードを rc に（起動の上限（10 秒に 5 回）の数えを戻してから）
        systemctl reset-failed looptrack >/dev/null 2>&1 || true
        rc=0
        out=$($I "$@" 2>&1 </dev/null) || rc=$?
      }
      blocked() { # <場面> <出してはいけない完了の文面> — 0 でない終了コード・別のプロセスとポートを示す・完了と 200 OK を出さない
        local name=$1 done=$2
        [ "$rc" != 0 ] && ok "$name: 0 でない終了コード（${rc}）" || ng "$name: 別のプロセスが応えているのに 0 で終わった: $out"
        contains "$out" "127.0.0.1:8090/looptrack/healthz に別のプロセスが応答しています" && ok "$name: 「別のプロセス」とポートを示す" || ng "$name: 別のプロセスの表示: $out"
        contains "$out" "$done" && ng "$name: 完了の文面が出た: $out" || ok "$name: 「${done}」を出さない"
        contains "$out" "200 OK" && ng "$name: 200 OK を出した: $out" || ok "$name: 200 OK を出さない"
      }
      completed() { # <場面> <完了の文面> — 対照: 偽の応答者を止めると、同じ操作が完了まで進み、応えているのは looptrack
        local name=$1 done=$2
        [ "$rc" = 0 ] && ok "$name: 終了コード 0" || ng "$name: 失敗した（${rc}）: $out"
        contains "$out" "$done" && ok "$name: 「${done}」" || ng "$name: 完了の表示: $out"
        contains "$out" "200 OK" && ok "$name: 200 OK" || ng "$name: 起動の表示: $out"
        check "$name: サービスが動いている" systemctl is-active looptrack
        fake_answers && ng "$name: 応えているのが nginx のまま" || ok "$name: 応えているのは偽の応答者ではない"
      }
      precondition() {
        fake_answers && ok "前提: 偽の応答者（nginx）が 127.0.0.1:8090/looptrack/healthz に 200 を返す" ||
          ng "前提が崩れています（偽の応答者が応えていない: $(curl -s -D - -o /dev/null http://127.0.0.1:8090/looptrack/healthz 2>&1)）"
      }

      echo "== 初回の install"
      fake_on
      precondition
      run_i --from /dist --yes --method systemd -- "${SETUP[@]}"
      blocked "初回" "インストールが終わりました"
      contains "$out" "looptrack のサービスは起動していません" && ok "初回: 起動していないと示す" || ng "初回: 起動の表示: $out"
      systemctl is-active looptrack >/dev/null 2>&1 && ng "初回: サービスを起こした" || ok "初回: サービスを起こしていない"
      check "初回: 印（install.conf）を置いていない" test ! -e /etc/looptrack/install.conf
      echo "== 対照: 偽の応答者を止めてもう一度（続きから）"
      fake_off
      run_i --from /dist --yes --method systemd -- "${SETUP[@]}"
      completed "初回の対照" "インストールが終わりました"

      echo "== 入れ直し（--uninstall でデータを残した後の install）"
      run_i --uninstall
      [ "$rc" = 0 ] || ng "uninstall が失敗: $out"
      fake_on
      precondition
      run_i --from /dist --yes --method systemd -- "${SETUP[@]}"
      blocked "入れ直し" "インストールが終わりました"
      systemctl is-active looptrack >/dev/null 2>&1 && ng "入れ直し: サービスを起こした" || ok "入れ直し: サービスを起こしていない"
      check "入れ直し: 印（install.conf）を置いていない" test ! -e /etc/looptrack/install.conf
      fake_off
      run_i --from /dist --yes --method systemd -- "${SETUP[@]}"
      completed "入れ直しの対照" "インストールが終わりました"

      echo "== --upgrade（looptrack が止まっている間に、別のプロセスがポートを取った）"
      systemctl stop looptrack
      fake_on
      precondition
      echo "-- 端末のある手動の --upgrade（--yes なし）"
      run_i --upgrade --from /dist2
      blocked "手動の --upgrade" "更新しました"
      contains "$out" "新しい版 v0.0.0-installtest2 は入れていません。looptrack のサービスは止まっています" && ok "手動: 入れていない・止まっていると示す" || ng "手動: 表示: $out"
      contains "$out" "前の版" && ng "手動なのに戻した: $out" || ok "手動: 戻さない"
      version_is v0.0.0-installtest1 && ok "手動: 実行ファイルは置き換えていない" || ng "手動: 版: $(/usr/local/bin/looptrack version)"
      check "手動: 印は前の版のまま" grep -qx VERSION=v0.0.0-installtest1 /etc/looptrack/install.conf
      echo "-- 無人の --upgrade（--yes・端末なし。timer と同じ）"
      run_i --upgrade --from /dist2 --yes
      blocked "無人の --upgrade" "更新しました"
      contains "$out" "前の版 v0.0.0-installtest1 に戻して起動し直します（無人の更新なので" && ok "無人: 戻しの手順に入った" || ng "無人: 戻しに入っていない: $out"
      contains "$out" "に戻して起動し直しました" && ng "無人: 「戻して起動し直しました」を出した: $out" || ok "無人: 「戻して起動し直しました」を出さない"
      contains "$out" "looptrack が起動したかを確かめられません" && ok "無人: 起動を確かめられないと示す" || ng "無人: 表示: $out"
      version_is v0.0.0-installtest1 && ok "無人: 実行ファイルは置き換えていない" || ng "無人: 版: $(/usr/local/bin/looptrack version)"
      check "無人: 印は前の版のまま" grep -qx VERSION=v0.0.0-installtest1 /etc/looptrack/install.conf
      echo "== 対照: 偽の応答者を止めると、同じ手動の --upgrade・無人の --upgrade が完了まで進む"
      fake_off
      run_i --upgrade --from /dist2
      completed "手動の --upgrade の対照" "更新しました"
      version_is v0.0.0-installtest2 && ok "手動の対照: 新しい版" || ng "手動の対照: 版: $(/usr/local/bin/looptrack version)"
      # 無人の対照は …1 から（版を下げずに同じ …1 → …2 を流す）
      run_i --uninstall --purge --yes
      run_i --from /dist --yes --method systemd -- "${SETUP[@]}"
      [ "$rc" = 0 ] || ng "無人の対照の前の install が失敗: $out"
      run_i --upgrade --from /dist2 --yes
      completed "無人の --upgrade の対照" "更新しました"
      version_is v0.0.0-installtest2 && ok "無人の対照: 新しい版" || ng "無人の対照: 版: $(/usr/local/bin/looptrack version)"

      echo "== 起こした後に別のプロセスがポートを取る（unit の追加設定の ExecStartPre で、起動の直前に偽の応答者を起こす）"
      # 起こす前の確認（ポートは空いている）は通り、looptrack は待ち受けに失敗し、/healthz の 200 は偽の応答者が返す。
      # unit は Type=simple で start の直後から active なので、is-active だけではこの形を見分けられない（ポートの PID と MainPID の突き合わせで捕まえる）
      run_i --uninstall --purge --yes
      command -v ss >/dev/null 2>&1 && ok "前提: ss がある（PID の突き合わせを通る）" || ng "前提が崩れています（ss が無いので is-active だけに落ちる）"
      LATE=/etc/systemd/system/looptrack.service.d/zz-test-fake-late.conf
      printf '#!/bin/sh\n# looptrack の unit の外（別の unit。止め忘れた古いコンテナの代わり）で偽の応答者を起こし、応えるまで待つ（最大 5 秒）。Restart のたびに呼ばれるが、2 回目からは起動済み\nsystemd-run --quiet --unit=im-fake-late /usr/sbin/nginx -c %s -g "daemon off;" 2>/dev/null || true\nfor _ in $(seq 1 50); do curl -sf -o /dev/null http://127.0.0.1:8090/looptrack/healthz && exit 0; sleep 0.1; done\nexit 1\n' "$FAKE" >/root/fake-late.sh
      chmod 755 /root/fake-late.sh
      mkdir -p "${LATE%/*}"
      printf '[Service]\nExecStartPre=+/root/fake-late.sh\n' >"$LATE"
      run_i --from /dist --yes --method systemd -- "${SETUP[@]}"
      systemctl is-active --quiet im-fake-late && fake_answers && ok "前提: 起こした後に偽の応答者（別の unit の nginx）が応えている" ||
        ng "前提が崩れています（偽の応答者が起きていない: $(systemctl status im-fake-late --no-pager 2>&1 | tail -n 5)）"
      contains "$out" "==> 動作確認" && ok "起動の後: 起こす前の確認は通った（起動まで進んだ）" || ng "起動の後: 起動まで進んでいない: $out"
      contains "$out" "に別のプロセスが応答しています（止め忘れた" && ng "起動の後: 起こす前の確認で止まった（この場面を作れていない）: $out" || true
      [ "$rc" != 0 ] && ok "起動の後: 0 でない終了コード（${rc}）" || ng "起動の後: 別のプロセスの 200 で 0 で終わった: $out"
      contains "$out" "/healthz は 200 を返しましたが、" && contains "$out" "別のプロセス" &&
        ok "起動の後: 200 でも「別のプロセス」と示す" || ng "起動の後: 別のプロセスの表示: $out"
      contains "$out" "インストールが終わりました" && ng "起動の後: 完了の文面が出た: $out" || ok "起動の後: 完了の文面を出さない"
      contains "$out" "200 OK" && ng "起動の後: 200 OK を出した: $out" || ok "起動の後: 200 OK を出さない"
      rm -f "$LATE" /root/fake-late.sh
      rmdir "${LATE%/*}" 2>/dev/null || true
      systemctl daemon-reload
      systemctl stop im-fake-late >/dev/null 2>&1 || true
      systemctl reset-failed im-fake-late >/dev/null 2>&1 || true
      # 対照: 偽の応答者を起こさなければ、同じ install が完了まで進む（印も置くので、下の --purge で片付けられる）
      run_i --from /dist --yes --method systemd -- "${SETUP[@]}"
      completed "起動の後の対照" "インストールが終わりました"
      $I --uninstall --purge --yes >/dev/null 2>&1 || ng "purge が失敗"
      rm -f "$FAKE" /root/fake-nginx.err
      ;;

    compose)
      d=$3
      export LOOPTRACK_INSTALL_IMAGE=imtest0151
      SETUPC=("${SETUP[@]}" --port 18090)
      echo "== compose で実起動（非対話）"
      out=$($I --from /dist --yes --method compose --dir "$d" -- "${SETUPC[@]}" 2>&1) || ng "install が失敗: $out"
      contains "$out" "200 OK" && ok "/healthz を待った" || ng "起動の表示: $out"
      check "コンテナが動いている" sh -c '[ "$(docker container inspect -f {{.State.Status}} looptrack)" = running ]'
      check "イメージ imtest0151:v0.0.0-installtest1" docker image inspect imtest0151:v0.0.0-installtest1
      check "data/im.db は uid 65534・600" sh -c "[ \"\$(stat -c %u:%a $d/data/im.db)\" = 65534:600 ]"
      # 第三者のライセンス文。イメージは scratch なのでシェルが無く、docker cp で取り出して looptrack licenses と突き合わせる
      docker cp looptrack:/NOTICE - 2>/dev/null | tar -xO >/tmp/notice.image || true
      docker exec looptrack /looptrack licenses >/tmp/notice.bin 2>/dev/null || true
      check "イメージに /NOTICE がある" sh -c 'grep -q "third-party notices" /tmp/notice.image'
      check "/NOTICE が looptrack licenses と同じ" cmp -s /tmp/notice.image /tmp/notice.bin
      docker logs looptrack 2>&1 | grep -q "$PERM_WARN" && ng "DB の権限の警告が出た" || ok "DB の権限の警告を出さない"
      # ログインはコンテナ looptrack のネットワークに入った使い捨てのコンテナから（looptrack は scratch でシェルも curl も無い）。
      # 待ち受けは .env の LOOPTRACK_LISTEN のポート（compose.yaml はホストの 127.0.0.1 の同じ番号に公開する）
      login_in_im() {
        local port
        port=$(sed -n "s/^LOOPTRACK_LISTEN='.*:\([0-9]*\)'$/\1/p" "$d/.env")
        docker run -i --rm --network container:looptrack -e "TOTP_IN=$(cat "$TOTP_STATE" 2>/dev/null)" "$BASE_IMAGE" \
          bash -s -- --in-container login "http://127.0.0.1:$port/looptrack" </src/install_test.sh
      }
      r=$(login_in_im) && ok "ログインできる $(grep -v '^totp-state:' <<<"$r")" || ng "ログイン: $r"
      sed -n 's/^totp-state: //p' <<<"$r" >"$TOTP_STATE"
      out=$($I --from /dist --yes 2>&1) || ng "2 回目が失敗"
      contains "$out" "設定済みです" && ok "2 回目は設定済み" || ng "2 回目: $out"
      echo "== --upgrade（広い権限の DB で。以前の版が作った 0644）"
      chmod 644 "$d/data/im.db"
      out=$($I --upgrade --from /dist2 2>&1) || ng "upgrade が失敗: $out"
      contains "$out" "更新しました" && ok "更新の表示" || ng "更新の表示: $out"
      check "新しいイメージで動く" sh -c 'docker exec looptrack /looptrack version | grep -q installtest2'
      check "--upgrade で DB を 600 に直す" sh -c "[ \"\$(stat -c %u:%a $d/data/im.db)\" = 65534:600 ]"
      docker logs looptrack 2>&1 | grep -q "$PERM_WARN" && ng "更新の後に権限の警告が出た" || ok "更新の後は権限の警告を出さない"
      (cd "$d" && LOOPTRACK_IMAGE=imtest0151:latest docker compose run --rm --no-deps looptrack user list 2>&1) | grep -q alice && ok "データ（管理者）が残る" || ng "データが残らない"
      r=$(login_in_im) && ok "更新の後も同じパスワードと二段階認証でログインできる $(grep -v '^totp-state:' <<<"$r")" || ng "更新の後のログイン: $r"
      check "SQLite の控え" sh -c "ls -d $d/data/backup-*/im.db"
      echo "== --uninstall --purge"
      $I --uninstall --purge --yes >/dev/null 2>&1 || ng "purge が失敗"
      check "コンテナを消した" sh -c '! docker container inspect looptrack'
      check "イメージを消した" sh -c '! docker image inspect imtest0151:latest'
      ;;

    compose-mysql)
      # compose + MySQL（テスト専用の MySQL のコンテナ im0151-cmysql。ネットワーク im0151-cnet）。止める前の確認の compose の側
      # （新しい版のイメージを docker compose run で動かす）を通る場面。アプリ用の利用者 lt_capp は DB ltc に広い権限を持つ（権限の段は通らない）。
      # compose のコンテナを im0151-cnet に入れるのは、テストが置く compose.override.yaml（docker compose が compose.yaml に重ねて読む）。
      # install.sh と setup が書く compose.yaml には手を入れない
      d=$3
      export LOOPTRACK_INSTALL_IMAGE=imtest0151
      export LOOPTRACK_SETUP_DSN="lt_capp:${APP_PW}@tcp(im0151-cmysql:3306)/ltc?parseTime=true"
      unset LOOPTRACK_SETUP_MIGRATE_DSN
      mkdir -p "$d"
      printf 'services:\n  looptrack:\n    networks: [default, cnet]\nnetworks:\n  cnet:\n    external: true\n    name: im0151-cnet\n' >"$d/compose.override.yaml"
      SETUPCM=(--store mysql --public-url https://im.example.com --admin-login alice --admin-name Alice --admin-password-file /root/pw --two-factor required --project web --port 18090)
      echo "== compose + MySQL で実起動（非対話・…1）"
      out=$($I --from /dist --yes --method compose --dir "$d" -- "${SETUPCM[@]}" 2>&1) || ng "install が失敗: $out"
      contains "$out" "インストールが終わりました" && ok "入った" || ng "install の表示: $out"
      check "コンテナが動いている" sh -c '[ "$(docker container inspect -f {{.State.Status}} looptrack)" = running ]'
      echo "== compose + MySQL: 端末の --upgrade で、新しい版（…3・偽の migrate 9001）が DB の形を変え、表を作る接続先が無い → 止める前に止まる"
      csnap() {
        docker container inspect -f '{{.Id}} {{.State.StartedAt}} {{.Image}}' looptrack
        docker image inspect -f '{{.Id}}' imtest0151:latest
        sha256sum /usr/local/bin/looptrack /etc/looptrack/install.conf
      }
      snap=$(csnap)
      rc=0
      tty_run /tmp/cm-a.log "sh /src/install.sh --upgrade --from /dist3" "それでもサービスを止めて続けますか" "" || rc=$?
      out=$(tr -d '\r' </tmp/cm-a.log)
      [ "$rc" != 0 ] && ok "0 でない終了コード（${rc}）" || ng "続けないと答えたのに 0 で終わった: $out"
      # コンテナの中の looptrack には LOOPTRACK_LANG を渡さないので、行の頭は英語（Pending:）になる。見るのは新しい版にしか無い名前
      contains "$out" "\(未適用\|Pending\): 9001_installtest_fake.sql" && ok "新しい版のイメージで確かめた（新しい版にしか無い偽の migrate の名前が出る）" || ng "未適用の表示（新しい版のイメージで確かめていない）: $out"
      contains "$out" "それでもサービスを止めて続けますか" && ok "続けるかを尋ねる" || ng "問いの表示: $out"
      contains "$out" "止めずに終わりました" && ok "止めなかったと伝える" || ng "終わりの表示: $out"
      contains "$out" "==> 停止" && ng "止めた: $out" || ok "止めていない（停止の段に進まない）"
      grep -qx '==> 実行ファイル' <<<"$out" && ng "実行ファイルの段に進んだ: $out" || ok "実行ファイルの段（置き換え）に進まない"
      [ "$snap" = "$(csnap)" ] && ok "コンテナ（ID・起動時刻・イメージ）・latest のタグ・実行ファイル・印は変わっていない" || ng "変わった: 前 $snap / 後 $(csnap)"
      check "コンテナが動いている" sh -c '[ "$(docker container inspect -f {{.State.Status}} looptrack)" = running ]'
      check "コンテナは前の版（…1）" sh -c 'docker exec looptrack /looptrack version | grep -q installtest1'
      check "印の版も前の版" grep -qx VERSION=v0.0.0-installtest1 /etc/looptrack/install.conf
      echo "== 片付け"
      $I --uninstall --purge --yes >/dev/null 2>&1 || ng "purge が失敗"
      docker image ls --format '{{.Repository}}:{{.Tag}}' imtest0151 | xargs -r docker image rm >/dev/null 2>&1 || true
      check "コンテナを消した" sh -c '! docker container inspect looptrack'
      ;;

    login)
      # compose の確認から呼ぶ（コンテナ looptrack のネットワークの中。二段階認証の状態は TOTP_IN で受け取り、totp-state: で返す）
      if [ -n "${TOTP_IN:-}" ]; then echo "$TOTP_IN" >"$TOTP_STATE"; fi
      r=$(web_login "$3") || { echo "$r"; exit 1; }
      echo "$r"
      echo "totp-state: $(cat "$TOTP_STATE")"
      exit 0
      ;;
  esac
  if [ "$fails" -gt 0 ]; then
    echo "失敗: $fails 件（${scenario}）" >&2
    exit 1
  fi
  echo "すべて通りました（${scenario}）"
  exit 0
fi

# ---------------------------------------------------------------- 手元（Docker を動かす側）

cd "$(dirname "$0")/.."
REPO=$(pwd)
IMAGES="${INSTALL_TEST_IMAGES-ubuntu:24.04 ubuntu:22.04 debian:12}"
case $(docker info --format '{{.Architecture}}') in
  x86_64 | amd64) ARCH=amd64 ;;
  aarch64 | arm64) ARCH=arm64 ;;
  *) echo "Docker の CPU に対応していません" >&2; exit 1 ;;
esac
WORK=$(mktemp -d "${TMPDIR:-/tmp}/im0151-test.XXXXXX")
WORK=$(cd "$WORK" && pwd -P)
CONTAINERS=()
NETWORKS=()
# ベースイメージ（中身を変えたらタグを上げる。同じ名前の古いイメージを使い回さないため）
SYSTEMD_BASE=im0151-systemd-base:v4 # + curl・openssl（ログインの確認）・Caddy・nginx（設定例の確認）・iproute2（install.sh の ss）・unzip（windows の書庫）
COMPOSE_BASE=im0151-compose-base:v2 # + curl・openssl（ログインの確認）
HTTP_PID=""
COMPOSE_RAN=0
cleanup() {
  if [ -n "$HTTP_PID" ]; then kill "$HTTP_PID" 2>/dev/null || true; fi
  for c in "${CONTAINERS[@]}"; do docker rm -f "$c" >/dev/null 2>&1 || true; done
  for n in ${NETWORKS[@]+"${NETWORKS[@]}"}; do docker network rm "$n" >/dev/null 2>&1 || true; done
  if [ "$COMPOSE_RAN" = 1 ]; then
    # compose の確認で作った looptrack（始める前に同じ名前のコンテナが無いことを確かめている）
    docker rm -f looptrack >/dev/null 2>&1 || true
  fi
  docker image ls --format '{{.Repository}}:{{.Tag}}' imtest0151 | xargs -r docker image rm >/dev/null 2>&1 || true
  if [ "${INSTALL_TEST_KEEP_BASE:-1}" = 0 ]; then docker image rm "$SYSTEMD_BASE" "$COMPOSE_BASE" >/dev/null 2>&1 || true; fi
  rm -rf "$WORK"
}
trap cleanup EXIT

echo "== 材料: dist.sh で looptrack を 2 版作る（素の形と、GitHub Releases の形の書庫。…1 は linux/${ARCH} だけ、…2 は 6 対象）"
# …2 は 6 対象（クライアントに配る looptrack の確認: --upgrade の後に配布ディレクトリが 6 対象になる）。
# …1 は linux/<arch> だけ（対照: SHA256SUMS に無い対象は置かずに注意を出し、serve が足りないと知らせる）
for n in 1 2; do
  targets="linux/$ARCH"
  if [ "$n" = 2 ]; then targets="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64"; fi
  RELEASE_CMDS=looptrack RELEASE_TARGETS="$targets" bash deploy/release/dist.sh build "v0.0.0-installtest$n" "$WORK/dist$n" 2>/dev/null
  bash deploy/release/dist.sh sums "$WORK/dist$n" 2>/dev/null
  # Releases の並び: <リポジトリ>/releases/download/<版>/（書庫・NOTICE・OFL・SHA256SUMS）
  rel="$WORK/gh/releases/download/v0.0.0-installtest$n"
  RELEASE_TARGETS="$targets" bash deploy/release/dist.sh archive "v0.0.0-installtest$n" "$WORK/dist$n" "$rel" 2>/dev/null
  RELEASE_TARGETS="$targets" bash deploy/release/dist.sh check-archives "v0.0.0-installtest$n" "$rel" "$WORK/dist$n" 2>/dev/null
  cp "$WORK/dist$n/NOTICE" "$WORK/dist$n/OFL-BIZUDGothic.txt" "$rel/"
  bash deploy/release/dist.sh sums "$rel" 2>/dev/null
  RELEASE_TARGETS="$targets" bash deploy/release/dist.sh check-release "v0.0.0-installtest$n" "$rel" 2>/dev/null
done
# サーバ用でない対象の実行ファイルだけが SHA256SUMS と合わない取得元（クライアントに配る looptrack の照合の確認。SHA256SUMS・署名は …2 のまま）
cp -R "$WORK/dist2" "$WORK/dist2bad"
printf x >>"$WORK/dist2bad/looptrack_v0.0.0-installtest2_windows_amd64.exe"
# DB の形を変える新しい版（…3）: 偽の migrate（9001_installtest_fake.sql。MySQL と SQLite の対）を足したソースの写しから作る
# （MySQL の場面（systemd の最小権限・compose）だけが使う。素の形 /dist3 だけ。migrations は実行ファイルに埋め込むので、写しに足してビルドする）
if [ "${INSTALL_TEST_MYSQL:-1}" = 1 ] && { [ "${INSTALL_TEST_SYSTEMD:-1}" = 1 ] || [ "${INSTALL_TEST_COMPOSE:-0}" = 1 ]; }; then
  mkdir -p "$WORK/src3"
  git ls-files -z | xargs -0 tar cf - | tar xf - -C "$WORK/src3"
  printf -- '-- install_test.sh の偽の migrate（DB の形を変える新しい版の代わり）\nCREATE TABLE installtest_fake (id INT NOT NULL PRIMARY KEY) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;\n' \
    >"$WORK/src3/migrations/9001_installtest_fake.sql"
  printf -- '-- install_test.sh の偽の migrate（DB の形を変える新しい版の代わり）\nCREATE TABLE installtest_fake (id INTEGER NOT NULL PRIMARY KEY);\n' \
    >"$WORK/src3/migrations/sqlite/9001_installtest_fake.sql"
  (cd "$WORK/src3" && RELEASE_CMDS=looptrack RELEASE_TARGETS="linux/$ARCH" bash deploy/release/dist.sh build v0.0.0-installtest3 "$WORK/dist3" 2>/dev/null &&
    bash deploy/release/dist.sh sums "$WORK/dist3" 2>/dev/null)
  rm -rf "$WORK/src3"
else
  mkdir -p "$WORK/dist3"
fi
# README の 1 行が取るもの（raw.githubusercontent.com の main の deploy/install.sh の読み替え）
mkdir -p "$WORK/raw/deploy"
cp deploy/install.sh "$WORK/raw/deploy/install.sh"
# 1.0.0-rc.2 の install.sh（開発側のタグ。新しいインストーラの --upgrade で上げる経路の確認に使う）
mkdir -p "$WORK/old"
git show v1.0.0-rc.2:deploy/install.sh >"$WORK/old/install.sh" 2>/dev/null || rm -f "$WORK/old/install.sh"
# SHA256SUMS に使い捨ての鍵（パスワードなし）で署名する。手元に minisign が無ければ形だけの .minisig を置く
# （コンテナの中は偽の minisign で呼び出しを確かめるので、中身は問わない）
SIGN_ENV=()
if command -v minisign >/dev/null 2>&1; then
  minisign -G -W -p "$WORK/test.pub" -s "$WORK/test.key" >/dev/null
  for d in "$WORK/dist1" "$WORK/dist2" "$WORK"/gh/releases/download/*; do
    MINISIGN_SECRET_KEY_FILE="$WORK/test.key" MINISIGN_PUB_FILE="$WORK/test.pub" \
      bash deploy/release/dist.sh sign-sums "$d" "install_test" >/dev/null 2>&1
    MINISIGN_PUB_FILE="$WORK/test.pub" bash deploy/release/dist.sh verify "$d" >/dev/null 2>&1
  done
  SIGN_ENV=(-e "LOOPTRACK_INSTALL_MINISIGN_PUBKEY=$(sed -n 2p "$WORK/test.pub")")
else
  echo "（手元に minisign が無いため、形だけの SHA256SUMS.minisig を置きます）"
  for d in "$WORK/dist1" "$WORK/dist2" "$WORK"/gh/releases/download/*; do printf 'untrusted comment: placeholder\n' >"$d/SHA256SUMS.minisig"; done
fi
# releases/latest/download は …1（署名まで済んだものを写す）
mkdir -p "$WORK/gh/releases/latest"
cp -R "$WORK/gh/releases/download/v0.0.0-installtest1" "$WORK/gh/releases/latest/download"
# 自動の置き換えの確認用: latest が …2 の取得元（gh2）
mkdir -p "$WORK/gh2/releases/latest"
cp -R "$WORK/gh/releases/download/v0.0.0-installtest2" "$WORK/gh2/releases/latest/download"

run_plain() { # <イメージ>
  local name
  name="im0151-plain-$(echo "$1" | tr ':.' '--')"
  CONTAINERS+=("$name")
  echo
  echo "######## $1（systemd なし）"
  docker run --rm --name "$name" ${SIGN_ENV[@]+"${SIGN_ENV[@]}"} -v "$REPO/deploy:/src:ro" -v "$WORK/dist1:/dist:ro" -v "$WORK/dist2:/dist2:ro" \
    -v "$WORK/gh:/gh:ro" -e LOOPTRACK_INSTALL_REPO=/gh "$1" \
    bash /src/install_test.sh --in-container plain
}

run_interactive() {
  CONTAINERS+=(im0151-interactive)
  echo
  echo "######## ubuntu:24.04（対話・疑似端末）"
  docker run --rm --name im0151-interactive -v "$REPO/deploy:/src:ro" -v "$WORK/dist1:/dist:ro" ubuntu:24.04 \
    bash /src/install_test.sh --in-container interactive
}

run_systemd() {
  echo
  echo "######## ubuntu:24.04 + systemd（実起動）"
  if ! docker image inspect "$SYSTEMD_BASE" >/dev/null 2>&1; then
    printf 'FROM ubuntu:24.04\nRUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends systemd systemd-sysv dbus procps iproute2 curl ca-certificates openssl caddy nginx unzip && rm -rf /var/lib/apt/lists/*\nCMD ["/sbin/init"]\n' |
      docker build -q -t "$SYSTEMD_BASE" - >/dev/null
  fi
  CONTAINERS+=(im0151-systemd)
  # --upgrade は URL から取る（手元の一時 HTTP サーバ。コンテナからは host.docker.internal）
  local port
  port=$(node -e 'const s=require("net").createServer();s.listen(0,"0.0.0.0",()=>{console.log(s.address().port);s.close()})')
  node -e 'const http=require("http"),fs=require("fs"),path=require("path");const dir=process.argv[1];
    http.createServer((q,r)=>{const f=path.join(dir,decodeURIComponent(q.url.split("?")[0]));
      fs.readFile(f,(e,b)=>{if(e){r.statusCode=404;r.end();return}r.end(b)})}).listen(+process.argv[2],"0.0.0.0")' \
    "$WORK" "$port" >/dev/null 2>&1 &
  HTTP_PID=$!
  disown "$HTTP_PID"
  docker run -d --name im0151-systemd --add-host=host.docker.internal:host-gateway --privileged --cgroupns=host -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
    --tmpfs /run --tmpfs /run/lock -v "$REPO/deploy:/src:ro" -v "$WORK/dist1:/dist:ro" -v "$WORK/dist2:/dist2:ro" -v "$WORK/dist2bad:/dist2bad:ro" -v "$WORK/dist3:/dist3:ro" -v "$WORK/old:/old:ro" \
    "$SYSTEMD_BASE" >/dev/null
  wait_systemd || return 1
  local web="-e GH_URL=http://host.docker.internal:$port/gh -e RAW_URL=http://host.docker.internal:$port/raw/deploy/install.sh"
  # shellcheck disable=SC2086
  docker exec $web im0151-systemd bash /src/install_test.sh --in-container systemd || return 1
  # OS の再起動の代わりにコンテナを起動し直し、サービスが自動で上がることを確かめてから --upgrade へ
  docker restart im0151-systemd >/dev/null
  wait_systemd || return 1
  # shellcheck disable=SC2086
  local status=0
  docker exec $web -e UPGRADE_URL="http://host.docker.internal:$port/dist2" im0151-systemd bash /src/install_test.sh --in-container systemd-reboot || status=1
  if [ "${INSTALL_TEST_MYSQL:-1}" = 1 ]; then run_mysql || status=1; fi
  if [ -s "$WORK/old/install.sh" ]; then
    echo
    echo "######## 1.0.0-rc.2 の install.sh で入れたサーバを上げる"
    # shellcheck disable=SC2086
    docker exec $web im0151-systemd bash /src/install_test.sh --in-container rc2 || status=1
  else
    echo "タグ v1.0.0-rc.2 が無いため、rc.2 の install.sh からの更新の確認を省きます" >&2
    status=1
  fi
  echo
  echo "######## 自動の置き換え（--auto-upgrade・systemd timer）"
  # shellcheck disable=SC2086
  docker exec $web -e GH2_URL="http://host.docker.internal:$port/gh2" im0151-systemd bash /src/install_test.sh --in-container auto-upgrade || status=1
  echo
  echo "######## 無人の更新の戻し（止めた後の失敗: 起動・daemon-reload・裸のコマンド・中断）"
  docker exec im0151-systemd bash /src/install_test.sh --in-container auto-rollback || status=1
  echo
  echo "######## 同じポートを別のプロセスが握っている（起こす前に止まる・無人の更新は戻したと言わない・is-active を見る）"
  docker exec im0151-systemd bash /src/install_test.sh --in-container port-taken || status=1
  return $status
}

# mysql_root <SQL> — テスト専用の MySQL に root で流す（パスワードは環境変数で渡し、引数に書かない）
mysql_root() {
  MYSQL_PWD="$ADMIN_PW" docker exec -i -e MYSQL_PWD im0151-mysql mysql -uroot -N -B -e "$1"
}

run_mysql() {
  echo
  echo "######## MySQL（最小権限）: テスト専用の MySQL のコンテナ + 上の systemd のコンテナ"
  # テスト用の値（このテストの間だけ使う。管理用のパスワードは疑似端末から答えるだけで、引数に書かない）
  ADMIN_PW="ltroot-$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n')"
  APP_PW="ltapp-$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n')"
  MIG_PW="ltmig-$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n')"
  export ADMIN_PW APP_PW MIG_PW
  NETWORKS+=(im0151-net)
  docker network create im0151-net >/dev/null
  CONTAINERS+=(im0151-mysql)
  MYSQL_ROOT_PASSWORD="$ADMIN_PW" docker run -d --name im0151-mysql --network im0151-net -e MYSQL_ROOT_PASSWORD \
    "${INSTALL_TEST_MYSQL_IMAGE:-mysql:8.4}" >/dev/null
  local up=0
  for _ in $(seq 1 120); do
    if mysql_root "SELECT 1" >/dev/null 2>&1; then up=1; break; fi
    sleep 1
  done
  [ $up = 1 ] || { echo "MySQL が 120 秒で立ち上がりません" >&2; return 1; }
  # 人が先に用意する分: DB と表を作る利用者（アプリ用の利用者 lt_app はインストーラが作る）
  mysql_root "CREATE DATABASE ltdb CHARACTER SET utf8mb4 COLLATE utf8mb4_bin; CREATE USER 'lt_migrate'@'%' IDENTIFIED BY '$MIG_PW'; GRANT ALL ON ltdb.* TO 'lt_migrate'@'%';" ||
    { echo "DB の用意に失敗" >&2; return 1; }
  docker network connect im0151-net im0151-systemd
  local status=0 n
  # DB が無い構成（DB ltnew は作らず、表を作る利用者 lt_newmig と、その DB への権限だけを先に用意する）
  mysql_root "CREATE USER 'lt_newmig'@'%' IDENTIFIED BY '$MIG_PW'; GRANT ALL ON ltnew.* TO 'lt_newmig'@'%';" ||
    { echo "表を作る利用者の用意に失敗" >&2; return 1; }
  docker exec -e ADMIN_PW -e APP_PW -e MIG_PW im0151-systemd bash /src/install_test.sh --in-container mysql-nodb-decline || status=1
  n=$(mysql_root "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME='ltnew'")
  [ "$n" = 0 ] && echo "  ok: 作らないと答えたら DB ltnew は無いまま" || { echo "  NG: 作らないと答えたのに DB ltnew ができた" >&2; status=1; }
  n=$(mysql_root "SELECT COUNT(*) FROM mysql.user WHERE User='lt_new'")
  [ "$n" = 0 ] && echo "  ok: 作らないと答えたらアプリ用の利用者 lt_new も無いまま" || { echo "  NG: 作らないと答えたのに lt_new ができた" >&2; status=1; }
  docker exec -e ADMIN_PW -e APP_PW -e MIG_PW im0151-systemd bash /src/install_test.sh --in-container mysql-nodb || status=1
  n=$(mysql_root "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME='ltnew'")
  [ "$n" = 1 ] && echo "  ok: DB ltnew をインストーラが作った" || { echo "  NG: DB ltnew が無い" >&2; status=1; }
  n=$(mysql_root "SELECT COUNT(*) FROM ltnew.schema_migrations" 2>&1)
  [ "$n" -gt 0 ] 2>/dev/null && echo "  ok: DB ltnew に migrate が通った（適用 $n 件）" || { echo "  NG: DB ltnew の schema_migrations: $n" >&2; status=1; }
  grep -q 'GRANT SELECT, INSERT ON `ltnew`.`comments` TO `lt_new`@`%`' <<<"$(mysql_root "SHOW GRANTS FOR 'lt_new'@'%'" 2>&1)" &&
    echo "  ok: 作った利用者 lt_new に表ごとの権限を与えた" || { echo "  NG: lt_new の SHOW GRANTS" >&2; status=1; }
  # 対照: DB が既にあれば作り直さない（先に置いた表と行が、インストールの後も残る）
  mysql_root "CREATE TABLE ltdb.keep_check (v VARCHAR(16)); INSERT INTO ltdb.keep_check VALUES ('kept');" || status=1
  docker exec -e ADMIN_PW -e APP_PW -e MIG_PW im0151-systemd bash /src/install_test.sh --in-container mysql-bad || status=1
  n=$(mysql_root "SELECT COUNT(*) FROM mysql.user WHERE User='lt_app'")
  [ "$n" = 0 ] && echo "  ok: 合わない資格情報では利用者を作らない（権限も流さない）" || { echo "  NG: 合わない資格情報で lt_app ができた" >&2; status=1; }
  docker exec -e ADMIN_PW -e APP_PW -e MIG_PW im0151-systemd bash /src/install_test.sh --in-container mysql-good || status=1
  # 対照: 尋ねた管理用のパスワードで繋いで、grants.sql と同じ権限が DB ltdb・利用者 lt_app に流れた
  local g
  g=$(mysql_root "SHOW GRANTS FOR 'lt_app'@'%'" 2>&1)
  if grep -q 'GRANT SELECT, INSERT ON `ltdb`.`comments` TO `lt_app`@`%`' <<<"$g" && grep -q '`ltdb`.`projects`' <<<"$g" &&
    [ "$(grep -c '^GRANT' <<<"$g")" -eq $(($(grep -c '^GRANT' deploy/grants.sql) + 1)) ]; then
    echo "  ok: SHOW GRANTS に grants.sql と同じ表ごとの権限（USAGE を足して $(grep -c '^GRANT' <<<"$g") 行）"
  else
    echo "  NG: SHOW GRANTS: $g" >&2
    status=1
  fi
  [ "$(mysql_root "SELECT v FROM ltdb.keep_check" 2>&1)" = kept ] && echo "  ok: 対照: 既にあった DB ltdb の表と行が残る（作り直さない）" ||
    { echo "  NG: 既にあった DB ltdb の表・行が残っていない" >&2; status=1; }
  mysql_root "DROP TABLE ltdb.keep_check" || status=1
  # 表が増えた更新と同じ症状（権限の欠けた表がある）を作ってから --upgrade
  mysql_root "REVOKE ALL PRIVILEGES ON ltdb.projects FROM 'lt_app'@'%'" || status=1
  # 無人の更新（自動の置き換え）: 新しい版に未適用の migrate がある DB（最後の適用記録を一時的に外す）では置き換えない
  mysql_root "CREATE TABLE ltdb.sm_hold AS SELECT * FROM ltdb.schema_migrations ORDER BY version DESC LIMIT 1; DELETE FROM ltdb.schema_migrations WHERE version = (SELECT version FROM ltdb.sm_hold)" || status=1
  docker exec -e ADMIN_PW -e APP_PW -e MIG_PW im0151-systemd bash /src/install_test.sh --in-container mysql-auto-pending || status=1
  mysql_root "INSERT INTO ltdb.schema_migrations SELECT * FROM ltdb.sm_hold; DROP TABLE ltdb.sm_hold" || status=1
  # 止めた後の失敗（権限の欠けた表）では前の版に戻す
  docker exec -e ADMIN_PW -e APP_PW -e MIG_PW im0151-systemd bash /src/install_test.sh --in-container mysql-auto-fail || status=1
  docker exec -e ADMIN_PW -e APP_PW -e MIG_PW im0151-systemd bash /src/install_test.sh --in-container mysql-upgrade || status=1
  # 表を作る利用者（LOOPTRACK_SETUP_MIGRATE_DSN）を渡さない更新: アプリ用の利用者は schema_migrations に SELECT だけ
  grep -q 'GRANT SELECT ON `ltdb`.`schema_migrations` TO `lt_app`@`%`' <<<"$(mysql_root "SHOW GRANTS FOR 'lt_app'@'%'" 2>&1)" &&
    echo "  ok: 前提: lt_app は schema_migrations に SELECT だけ（grants.sql の最小権限）" || { echo "  NG: lt_app の schema_migrations の権限" >&2; status=1; }
  # 手動の --upgrade（…2 → …1）と、timer の service の無人の更新（…1 → …2。変数 web と port は呼び出し元の run_systemd のもの）
  docker exec -e ADMIN_PW -e APP_PW -e MIG_PW im0151-systemd bash /src/install_test.sh --in-container mysql-manual-nomig || status=1
  # shellcheck disable=SC2086
  docker exec $web -e GH2_URL="http://host.docker.internal:$port/gh2" -e ADMIN_PW -e APP_PW -e MIG_PW im0151-systemd bash /src/install_test.sh --in-container mysql-auto-timer || status=1
  # 端末の --upgrade で、DB の形を変える新しい版（…3。偽の migrate 9001）: (a) 表を作る接続先が無ければ止める前に止まる /
  # (b) 対照: 同じ版・同じ DB の状態から、表を作る接続先と管理用のパスワードのファイルを渡すと上がる。
  # 表が増えた更新と同じ症状（権限の欠けた表）も作っておき、(b) で管理用のパスワードのファイルが使われることを確かめる
  n=$(mysql_root "SELECT COUNT(*) FROM ltdb.schema_migrations WHERE version = 9001" 2>&1)
  [ "$n" = 0 ] && echo "  ok: 前提: 偽の migrate（9001）は未適用" || { echo "  NG: 前提: 9001 の記録: $n" >&2; status=1; }
  mysql_root "REVOKE ALL PRIVILEGES ON ltdb.projects FROM 'lt_app'@'%'" || status=1
  docker exec -e ADMIN_PW -e APP_PW -e MIG_PW im0151-systemd bash /src/install_test.sh --in-container mysql-fakemig-decline || status=1
  n=$(mysql_root "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = 'ltdb' AND TABLE_NAME = 'installtest_fake'" 2>&1)
  [ "$n" = 0 ] && echo "  ok: (a) 偽の migrate の表は作られていない（DB を変えていない）" || { echo "  NG: (a) で installtest_fake ができた: $n" >&2; status=1; }
  docker exec -e ADMIN_PW -e APP_PW -e MIG_PW im0151-systemd bash /src/install_test.sh --in-container mysql-fakemig-dsn || status=1
  n=$(mysql_root "SELECT COUNT(*) FROM ltdb.schema_migrations WHERE version = 9001" 2>&1)
  [ "$n" = 1 ] && echo "  ok: (b) 偽の migrate（9001）の適用記録がある" || { echo "  NG: (b) 9001 の記録: $n" >&2; status=1; }
  n=$(mysql_root "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = 'ltdb' AND TABLE_NAME = 'installtest_fake'" 2>&1)
  [ "$n" = 1 ] && echo "  ok: (b) 偽の migrate の表ができた" || { echo "  NG: (b) installtest_fake: $n" >&2; status=1; }
  grep -q '`ltdb`.`projects`' <<<"$(mysql_root "SHOW GRANTS FOR 'lt_app'@'%'" 2>&1)" &&
    echo "  ok: (b) 欠けた表の権限を与え直した（管理用のパスワードのファイルで）" || { echo "  NG: (b) の後も projects の権限が無い" >&2; status=1; }
  # 片付け: 偽の migrate の記録と表を外し（…1・…2 はこの番号を持たないので、残すと新しい版で migrate した DB として拒む）、…2 に戻す
  mysql_root "DELETE FROM ltdb.schema_migrations WHERE version = 9001; DROP TABLE ltdb.installtest_fake" || status=1
  docker exec -e ADMIN_PW -e APP_PW -e MIG_PW im0151-systemd bash /src/install_test.sh --in-container mysql-fakemig-back || status=1
  # 対照: 権限をそろえた後（上の --upgrade が与え直した）の無人の更新は置き換わる
  docker exec -e ADMIN_PW -e APP_PW -e MIG_PW im0151-systemd bash /src/install_test.sh --in-container mysql-auto-ok || status=1
  grep -q '`ltdb`.`projects`' <<<"$(mysql_root "SHOW GRANTS FOR 'lt_app'@'%'" 2>&1)" &&
    echo "  ok: --upgrade で欠けた表の権限を与え直した" || { echo "  NG: --upgrade の後も projects の権限が無い" >&2; status=1; }
  docker network disconnect im0151-net im0151-systemd >/dev/null 2>&1 || true
  return $status
}

wait_systemd() {
  docker exec im0151-systemd sh -c 'for i in $(seq 1 60); do s=$(systemctl is-system-running 2>/dev/null); case $s in running|degraded) exit 0;; esac; sleep 1; done; exit 1' ||
    { echo "systemd が立ち上がりません" >&2; return 1; }
}

run_compose() {
  echo
  echo "######## ubuntu:24.04 + docker compose（ホストの Docker を使う）"
  if docker container inspect looptrack >/dev/null 2>&1; then
    echo "looptrack という名前のコンテナがあるため compose の確認を省きます" >&2
    return 1
  fi
  if ! docker image inspect "$COMPOSE_BASE" >/dev/null 2>&1; then
    printf 'FROM ubuntu:24.04\nRUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends docker.io docker-compose-v2 curl ca-certificates openssl && rm -rf /var/lib/apt/lists/*\n' |
      docker build -q -t "$COMPOSE_BASE" - >/dev/null
  fi
  mkdir -p "$WORK/compose"
  COMPOSE_RAN=1
  CONTAINERS+=(im0151-compose)
  docker run --rm --name im0151-compose -v /var/run/docker.sock:/var/run/docker.sock \
    -v "$WORK/compose:$WORK/compose" -v "$REPO/deploy:/src:ro" -v "$WORK/dist1:/dist:ro" -v "$WORK/dist2:/dist2:ro" \
    -e BASE_IMAGE="$COMPOSE_BASE" "$COMPOSE_BASE" bash /src/install_test.sh --in-container compose "$WORK/compose/opt" || return 1
  [ "${INSTALL_TEST_MYSQL:-1}" = 1 ] || return 0
  echo
  echo "######## ubuntu:24.04 + docker compose + MySQL（止める前の確認を、新しい版のイメージで）"
  # テスト用の値（このテストの間だけ使う）
  local cpw rpw up=0
  cpw="ltcapp-$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n')"
  rpw="ltcroot-$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n')"
  NETWORKS+=(im0151-cnet)
  docker network create im0151-cnet >/dev/null
  CONTAINERS+=(im0151-cmysql im0151-compose-mysql)
  MYSQL_ROOT_PASSWORD="$rpw" docker run -d --name im0151-cmysql --network im0151-cnet -e MYSQL_ROOT_PASSWORD \
    "${INSTALL_TEST_MYSQL_IMAGE:-mysql:8.4}" >/dev/null
  for _ in $(seq 1 120); do
    if MYSQL_PWD="$rpw" docker exec -e MYSQL_PWD im0151-cmysql mysql -uroot -N -B -e "SELECT 1" >/dev/null 2>&1; then up=1; break; fi
    sleep 1
  done
  [ $up = 1 ] || { echo "MySQL（compose 用）が 120 秒で立ち上がりません" >&2; return 1; }
  MYSQL_PWD="$rpw" docker exec -i -e MYSQL_PWD im0151-cmysql mysql -uroot -N -B -e \
    "CREATE DATABASE ltc CHARACTER SET utf8mb4 COLLATE utf8mb4_bin; CREATE USER 'lt_capp'@'%' IDENTIFIED BY '$cpw'; GRANT ALL ON ltc.* TO 'lt_capp'@'%';" ||
    { echo "DB の用意に失敗（compose 用）" >&2; return 1; }
  APP_PW="$cpw" docker run --rm --name im0151-compose-mysql --network im0151-cnet -v /var/run/docker.sock:/var/run/docker.sock \
    -v "$WORK/compose:$WORK/compose" -v "$REPO/deploy:/src:ro" -v "$WORK/dist1:/dist:ro" -v "$WORK/dist3:/dist3:ro" \
    -e APP_PW -e BASE_IMAGE="$COMPOSE_BASE" "$COMPOSE_BASE" bash /src/install_test.sh --in-container compose-mysql "$WORK/compose/optm"
}

status=0
for img in $IMAGES; do run_plain "$img" || status=1; done
if [ "${INSTALL_TEST_INTERACTIVE:-1}" = 1 ]; then run_interactive || status=1; fi
if [ "${INSTALL_TEST_SYSTEMD:-1}" = 1 ]; then run_systemd || status=1; fi
if [ "${INSTALL_TEST_COMPOSE:-0}" = 1 ]; then run_compose || status=1; fi
echo
if [ $status = 0 ]; then echo "install_test: すべて通りました"; else echo "install_test: 失敗があります" >&2; fi
exit $status
