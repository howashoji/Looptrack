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
#     自動の置き換え: 既定は AUTO_UPGRADE=off で timer を置かない / compose・--no-start・systemd なしの --auto-upgrade on は
#     何も変えずに止まる / 値の誤り・--upgrade なしの --only-newer は止まる
#   systemd（ubuntu:24.04 + systemd）: README の 1 行（curl … | sh）で書庫を取って実起動まで・/healthz・unit のサンドボックスの評価・2 回目・DB の権限（600・警告なし）/
#     ブラウザと同じ手順のログイン（パスワード → 二段階認証の登録 → 一覧の画面。curl と openssl で TOTP を計算）を 127.0.0.1 と、
#     install.sh の設定例のままの Caddy・nginx（https・公開 URL im.example.com）の後ろで / コンテナの再起動の後にサービスが上がる /
#     --upgrade（版が変わり、データと控えが残り、0644 の DB を 600 に直して警告が消え、同じ二段階認証でログインできる）・
#     同じ版の --upgrade --version（書庫の中身が入っているものと同じなら何もしない）・--uninstall
#   mysql（上の systemd のコンテナ + テスト専用の MySQL のコンテナ。共有の開発用 MySQL は使わない）: 最小権限の構成で、
#     管理用の資格情報が合わなければ権限を与えず起動せずに止まり、.env を残す / もう一度実行すると尋ねるところから続き、
#     アプリ用の利用者を作って権限を与え（DB 名・利用者名は im・im_app 以外）、起動と動作確認まで 1 回で進む /
#     尋ねた管理用のパスワードが /etc/looptrack・/var/lib/looptrack・/opt・インストーラの出力・シェルの履歴・ps の引数に残らない
#     （対照: そのパスワードで与えた権限が SHOW GRANTS に出る）/ 権限の欠けた表がある状態の --upgrade も尋ねて与え直して起動する /
#     DB が無い構成: setup が管理用の資格情報を 1 回だけ尋ね、DB を作るかを確かめてから DB・アプリ用の利用者を作り、migrate して起動まで進む・
#     「作らない」と答えると何も作らず（.env も書かず）、流す CREATE DATABASE を示して止まる / 対照: 既にある DB は作り直さない（表と行が残る）
#   rc2（同じコンテナ）: 1.0.0-rc.2 の install.sh（開発側のタグ）で入れたサーバを、新しいインストーラの --upgrade --version で上げる
#   auto-upgrade（同じコンテナ）: 自動の置き換え（--auto-upgrade。既定は off）。入れた直後は off で timer が無い / minisign が無ければ
#     on は止まり何も変えない / 1 行（curl … | sh）の on で looptrack-upgrade.timer が有効になり、install.sh の写し（root 755）・service・
#     timer が置かれる（service は写しを動かし、取り直さない）/ timer の service を動かすと新しい版（/gh2 の latest）に上がり、
#     設定は on のまま・写しは書き換えない / もう一度動かしても何もしない / 人が --upgrade を動かすと写しをそろえる /
#     古い版の取得元（/gh の latest）では版を下げない（--only-newer）/ off で timer を止めて 3 つのファイルを外す / on のまま --uninstall すると外れる
#   mysql-auto-*（MySQL の最小権限）: 無人の更新（--yes・端末なし。timer と同じ）で、新しい版に未適用の migrate があれば止めずに置き換えない /
#     止めた後に失敗（権限の欠けた表）すれば前の版に戻して起動し直し、0 でない終了コード / 対照: 権限がそろっていれば置き換わる
#     （コンテナの minisign は呼び出しを確かめる偽物。本物の署名と検証の形は手元の minisign で dist.sh sign-sums・verify が確かめる）
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
      admin user list 2>&1 | grep -q alice && ok "管理者 alice" || ng "管理者: $(admin user list 2>&1)"

      echo "== --uninstall（データを残す）→ 入れ直す"
      out=$($I --uninstall 2>&1) || ng "uninstall が失敗: $out"
      check "実行ファイル・unit・印を外した" sh -c '[ ! -e /usr/local/bin/looptrack ] && [ ! -e /etc/systemd/system/looptrack.service ] && [ ! -e /etc/looptrack/install.conf ]'
      check ".env と SQLite は残る" sh -c '[ -f /etc/looptrack/.env ] && [ -f /var/lib/looptrack/im.db ]'
      chmod 644 /var/lib/looptrack/im.db # 以前の版が作った DB（0644）を入れ直す場面
      key=$(grep ^LOOPTRACK_SECRET_KEY /etc/looptrack/.env)
      out=$($I --from /dist --yes --method systemd --no-start -- "${SETUP[@]}" 2>&1) || ng "入れ直しが失敗: $out"
      contains "$out" "作成済みです" && ok "setup を飛ばす" || ng "入れ直しの表示: $out"
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
      journalctl -u looptrack -o cat --no-pager | grep -q "$PERM_WARN" && ng "DB の権限の警告が出た" || ok "DB の権限の警告を出さない"

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
      journalctl -u looptrack -o cat --no-pager | grep -q "$PERM_WARN" && ok "権限の警告が出る（確認の仕組みが効いている）" || ng "644 でも警告が出ない"

      echo "== --upgrade（URL の接頭辞から取る: ${UPGRADE_URL}）"
      out=$($I --upgrade --from "$UPGRADE_URL" 2>&1) && ng "http:// を黙って使った" || true
      contains "$out" "https:// を使うか" && ok "http:// は断る" || ng "http:// の表示: $out"
      since=$(date +%s)
      out=$(LOOPTRACK_INSTALL_ALLOW_HTTP=1 $I --upgrade --from "$UPGRADE_URL" 2>&1) || ng "upgrade が失敗: $out"
      contains "$out" "更新しました" && ok "更新の表示" || ng "更新の表示: $out"
      case "$(/usr/local/bin/looptrack version)" in "looptrack v0.0.0-installtest2（"*) ok "版が変わった" ;; *) ng "版: $(/usr/local/bin/looptrack version)" ;; esac
      check "動いている" systemctl is-active looptrack
      check "印の版" grep -q ^VERSION=v0.0.0-installtest2 /etc/looptrack/install.conf
      check "SQLite の控え（控えの置き場は root 700）" sh -c 'ls -d /var/lib/looptrack/backup-*/im.db && [ "$(stat -c %U:%a /var/lib/looptrack/backup-*)" = root:700 ]'
      check "前の実行ファイル" test -x /usr/local/bin/looptrack.prev
      check "--upgrade で DB を 600 に直す" sh -c '[ "$(stat -c %U:%a /var/lib/looptrack/im.db)" = looptrack:600 ]'
      journalctl -u looptrack -o cat --no-pager --since "@$since" | grep -q "$PERM_WARN" && ng "更新の後に権限の警告が出た" || ok "更新の後は権限の警告を出さない"
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

    mysql-nodb-decline | mysql-nodb | mysql-bad | mysql-good | mysql-upgrade | mysql-auto-pending | mysql-auto-fail | mysql-auto-ok)
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
      before_unit=$(sha256sum /etc/looptrack/.env)
      out=$(LOOPTRACK_INSTALL_REPO="$GH_URL" LOOPTRACK_INSTALL_ALLOW_HTTP=1 $I --upgrade --version v0.0.0-installtest2 2>&1) || ng "新しいインストーラの --upgrade が失敗: $out"
      contains "$out" "更新しました: v0.0.0-installtest1 → v0.0.0-installtest2" && ok "rc.2 の install.conf を読んで上げる" || ng "更新の表示: $out"
      case "$(/usr/local/bin/looptrack version)" in "looptrack v0.0.0-installtest2（"*) ok "版が変わった" ;; *) ng "版: $(/usr/local/bin/looptrack version)" ;; esac
      check "動いている" systemctl is-active looptrack
      [ "$before_unit" = "$(sha256sum /etc/looptrack/.env)" ] && ok ".env はそのまま" || ng ".env が変わった"
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
      check "止まったときは何も置かない（印は off のまま）" sh -c 'grep -qx AUTO_UPGRADE=off /etc/looptrack/install.conf && [ ! -e /etc/systemd/system/looptrack-upgrade.timer ] && [ ! -e /usr/local/lib/looptrack/auto-upgrade ]'

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
      since=$(date +%s)
      run_timer_service || ng "古い取得元の timer の service が失敗"
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
      grep -q -- "-V -q -P .* -m .*/SHA256SUMS -x .*/SHA256SUMS.minisig" /root/minisign.args && ok "minisign で SHA256SUMS を確かめた" || ng "minisign の引数: $(cat /root/minisign.args 2>&1)"
      rm -f /usr/local/bin/minisign /root/minisign.args
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
SYSTEMD_BASE=im0151-systemd-base:v2 # + curl・openssl（ログインの確認）・Caddy・nginx（設定例の確認）
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

echo "== 材料: dist.sh で looptrack（linux/${ARCH}）を 2 版作る（素の形と、GitHub Releases の形の書庫）"
for n in 1 2; do
  RELEASE_CMDS=looptrack RELEASE_TARGETS="linux/$ARCH" bash deploy/release/dist.sh build "v0.0.0-installtest$n" "$WORK/dist$n" 2>/dev/null
  bash deploy/release/dist.sh sums "$WORK/dist$n" 2>/dev/null
  # Releases の並び: <リポジトリ>/releases/download/<版>/（書庫・NOTICE・OFL・SHA256SUMS）
  rel="$WORK/gh/releases/download/v0.0.0-installtest$n"
  RELEASE_TARGETS="linux/$ARCH" bash deploy/release/dist.sh archive "v0.0.0-installtest$n" "$WORK/dist$n" "$rel" 2>/dev/null
  RELEASE_TARGETS="linux/$ARCH" bash deploy/release/dist.sh check-archives "v0.0.0-installtest$n" "$rel" "$WORK/dist$n" 2>/dev/null
  cp "$WORK/dist$n/NOTICE" "$WORK/dist$n/OFL-BIZUDGothic.txt" "$rel/"
  bash deploy/release/dist.sh sums "$rel" 2>/dev/null
  RELEASE_TARGETS="linux/$ARCH" bash deploy/release/dist.sh check-release "v0.0.0-installtest$n" "$rel" 2>/dev/null
done
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
    printf 'FROM ubuntu:24.04\nRUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends systemd systemd-sysv dbus procps curl ca-certificates openssl caddy nginx && rm -rf /var/lib/apt/lists/*\nCMD ["/sbin/init"]\n' |
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
    --tmpfs /run --tmpfs /run/lock -v "$REPO/deploy:/src:ro" -v "$WORK/dist1:/dist:ro" -v "$WORK/dist2:/dist2:ro" -v "$WORK/old:/old:ro" \
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
    -e BASE_IMAGE="$COMPOSE_BASE" "$COMPOSE_BASE" bash /src/install_test.sh --in-container compose "$WORK/compose/opt"
}

status=0
for img in $IMAGES; do run_plain "$img" || status=1; done
if [ "${INSTALL_TEST_INTERACTIVE:-1}" = 1 ]; then run_interactive || status=1; fi
if [ "${INSTALL_TEST_SYSTEMD:-1}" = 1 ]; then run_systemd || status=1; fi
if [ "${INSTALL_TEST_COMPOSE:-0}" = 1 ]; then run_compose || status=1; fi
echo
if [ $status = 0 ]; then echo "install_test: すべて通りました"; else echo "install_test: 失敗があります" >&2; fi
exit $status
