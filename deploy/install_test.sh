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
# 取得元は --from <ローカルのディレクトリ>（dist.sh の出力そのまま。install.sh と grants.sql もその中に入る）。
# コンテナの /src はリポジトリの deploy/・/dist は dist.sh の出力（公開後の Releases の資産の並びと同じ）。
#
# 確かめること:
#   plain（systemd なし・各イメージ）: リリースの出力のディレクトリをそのまま取得元にする（install.sh と grants.sql が入り
#     SHA256SUMS で照合できる・配布物の中の install.sh で入る・--upgrade も同じ並びから）/ SHA-256 の不一致で何も入れない / SHA256SUMS の署名（minisign）: 合わなければ・--require-signature で
#     minisign か署名が無ければ何も入れない、確かめられないときは注意を出して進む（コンテナの minisign は呼び出しを確かめる偽物。
#     本物の署名と検証の形は手元の minisign で dist.sh sign-sums・verify が確かめる） / setup の失敗で何も残さない / 中断（TERM）で一時ファイルを残さない /
#     systemd --no-start で .env・unit・利用者・SQLite の置き場と権限・MCP の接続設定（setup と同じ形）/
#     2 回目は「設定済み」で .env を書き換えない /
#     手で serve を起動して /healthz と管理者 / --uninstall でデータを残し、入れ直すと同じデータで動く / --purge で消える /
#     compose --no-start で compose.yaml・.env・data の所有者 / 2 回目は「設定済み」
#   systemd（ubuntu:24.04 + systemd）: 実起動と /healthz・unit のサンドボックスの評価・2 回目・DB の権限（600・警告なし）/
#     ブラウザと同じ手順のログイン（パスワード → 二段階認証の登録 → 一覧の画面。curl と openssl で TOTP を計算）を 127.0.0.1 と、
#     install.sh の設定例のままの Caddy・nginx（https・公開 URL im.example.com）の後ろで / コンテナの再起動の後にサービスが上がる /
#     --upgrade（版が変わり、データと控えが残り、0644 の DB を 600 に直して警告が消え、同じ二段階認証でログインできる）・--uninstall /
#     アプリ用の利用者が保存先を読めない MySQL の構成では、起動せずに grants.sql の順番を案内して終わる
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
  PERM_WARN="本人以外も読めます" # looptrack serve が SQLite の DB の権限が広いときに出す警告

  # ---- ブラウザと同じ手順のログイン（curl と openssl がある場面だけ。受け入れ条件「公開 URL にログインできる」）
  # 二段階認証の状態は $TOTP_STATE（「<Base32 の秘密> <最後に使ったステップ>」）に持ち越す（同じステップの確認コードは再利用できないため）
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
    [ "$code" = 303 ] && [ -n "$sess" ] || { echo "確認コードの送信（$path）: $code"; return 1; }
    echo "$secret $step" >"$TOTP_STATE"
    code=$(curl -s "$@" -o $b -w '%{http_code}' -H "Cookie: looptrack_session=$sess" "$u/") || true
    [ "$code" = 200 ] || { echo "ログイン後の GET /: $code"; return 1; }
    code=$(curl -s "$@" -o /dev/null -w '%{http_code}' "$u/") || true
    [ "$code" = 303 ] || { echo "ログインしていない GET / が $code（ログイン画面に送らない）"; return 1; }
    [ $path = /login/totp/setup ] && echo "（二段階認証を登録してログイン）" || echo "（確認コードでログイン）"
  }

  case $scenario in
    plain)
      echo "== 取得元は --from だけ"
      out=$($I --from-server https://im.example.com --yes 2>&1) && ng "--from-server を受け付けた" || true
      contains "$out" "不明な引数です: --from-server" && ok "--from-server は受け付けない" || ng "--from-server の表示: $out"
      out=$($I --yes --method systemd --no-start 2>&1) && ng "取得元なしで通った" || true
      contains "$out" "取得元を --from" && ok "取得元なしは --from を案内" || ng "取得元なしの表示: $out"
      check "何も入れていない（取得元なし）" nothing_installed

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
      check "一時ファイルを残さない" sh -c '[ -z "$(ls /tmp/looptrack-install.* 2>/dev/null)" ]'

      echo "== 2 回目"
      before=$(sha256sum /etc/looptrack/.env /etc/systemd/system/looptrack.service)
      out=$($I --from /dist --yes 2>&1) || ng "2 回目が失敗: $out"
      contains "$out" "設定済みです" && ok "設定済みを示す" || ng "2 回目の表示: $out"
      [ "$before" = "$(sha256sum /etc/looptrack/.env /etc/systemd/system/looptrack.service)" ] && ok "書き換えない" || ng "2 回目で書き換わった"

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
      echo "== systemd で実起動（非対話）"
      out=$($I --from /dist --yes --method systemd -- "${SETUP[@]}" 2>&1) || ng "install が失敗: $out"
      contains "$out" "200 OK" && ok "/healthz を待った" || ng "起動の表示: $out"
      contains "$out" "読めました" && ok "起動の前に保存先を確かめる" || ng "保存先の確認の表示: $out"
      check "サービスが動いている" systemctl is-active looptrack
      check "サービスは有効" systemctl is-enabled looptrack
      check "looptrack の利用者で動く" sh -c '[ "$(ps -o user= -C looptrack | head -n1)" = looptrack ]'
      echo "  サンドボックスの評価: $(systemd-analyze security looptrack 2>/dev/null | tail -n 1)"
      out=$($I --from /dist --yes 2>&1) || ng "2 回目が失敗"
      contains "$out" "設定済みです" && ok "2 回目は設定済み" || ng "2 回目: $out"
      contains "$out" "サービス: active" && ok "2 回目にサービスの状態" || ng "2 回目の状態: $out"
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

      echo "== --upgrade（URL の接頭辞から取る: $UPGRADE_URL）"
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
      # 2 回目は配布物の中の install.sh で（リリースの資産の並びのまま更新の経路に入れる）
      cmp -s /src/install.sh /dist2/install.sh && ok "更新先の配布物の install.sh はリポジトリの写し" || ng "/dist2/install.sh が deploy/install.sh と違う"
      out=$(sh /dist2/install.sh --upgrade --from /dist2 2>&1) || ng "同じ版の upgrade が失敗"
      contains "$out" "すでに" && ok "同じ版の upgrade は何もしない" || ng "同じ版: $out"

      echo "== --uninstall"
      $I --uninstall >/dev/null 2>&1 || ng "uninstall が失敗"
      check "サービスを止めた" sh -c '! systemctl is-active looptrack'
      check "データは残る" test -f /var/lib/looptrack/im.db
      $I --uninstall --purge --yes >/dev/null 2>&1 || true

      echo "== MySQL の構成: アプリ用の利用者が読めないまま起動しない"
      # 実際の場面は「最小権限（grants.sql）を、表ができる前には流せない」。ここでは MySQL を立てる代わりに、
      # 表を作るところまで SQLite で済ませてから .env の LOOPTRACK_DSN だけを繋がらない MySQL に差し替え、
      # 「アプリ用の接続だけが保存先を読めない」状態を作る（install.sh から見た症状は同じ）
      out=$($I --from /dist --yes --method systemd --no-start -- "${SETUP[@]}" 2>&1) || ng "下準備の install が失敗: $out"
      rm -f /etc/looptrack/install.conf # 印を消して、2 回目の「設定済み」ではなく起動まで進ませる
      sed -i "s|^LOOPTRACK_DSN=.*|LOOPTRACK_DSN='im_app:pw@tcp(127.0.0.1:3306)/im?parseTime=true'|" /etc/looptrack/.env
      before=$(date +%s)
      out=$($I --from /dist --yes --method systemd -- "${SETUP[@]}" 2>&1) && ng "読めないまま起動した" || true
      took=$(($(date +%s) - before))
      contains "$out" "保存先の確認" && ok "起動の前に保存先を確かめる" || ng "確認の表示: $out"
      contains "$out" "grants.sql を流す" && ok "grants.sql を流す順番を案内する" || ng "案内の表示: $out"
      contains "$out" "サービスは起動していません" && ok "起動していないと伝える" || ng "終わりの表示: $out"
      contains "$out" "60 秒待っても" && ng "60 秒待つ方の失敗になった" || ok "60 秒待つ失敗にならない"
      [ "$took" -lt 30 ] && ok "待たずに終わる（$took 秒）" || ng "終わるまで $took 秒かかった"
      check "サービスを起動していない" sh -c '! systemctl is-active looptrack'
      check "設定は残る（grants.sql を流した後にやり直せる）" test -f /etc/looptrack/.env
      check "印は書かない（やり直しが設定済みで止まらない）" sh -c '[ ! -e /etc/looptrack/install.conf ]'
      rm -rf /etc/looptrack /var/lib/looptrack /usr/local/bin/looptrack # 印が無いので --uninstall は使えない
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
    echo "失敗: $fails 件（$scenario）" >&2
    exit 1
  fi
  echo "すべて通りました（$scenario）"
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
# ベースイメージ（中身を変えたらタグを上げる。同じ名前の古いイメージを使い回さないため）
SYSTEMD_BASE=im0151-systemd-base:v2 # + curl・openssl（ログインの確認）・Caddy・nginx（設定例の確認）
COMPOSE_BASE=im0151-compose-base:v2 # + curl・openssl（ログインの確認）
HTTP_PID=""
COMPOSE_RAN=0
cleanup() {
  if [ -n "$HTTP_PID" ]; then kill "$HTTP_PID" 2>/dev/null || true; fi
  for c in "${CONTAINERS[@]}"; do docker rm -f "$c" >/dev/null 2>&1 || true; done
  if [ "$COMPOSE_RAN" = 1 ]; then
    # compose の確認で作った looptrack（始める前に同じ名前のコンテナが無いことを確かめている）
    docker rm -f looptrack >/dev/null 2>&1 || true
  fi
  docker image ls --format '{{.Repository}}:{{.Tag}}' imtest0151 | xargs -r docker image rm >/dev/null 2>&1 || true
  if [ "${INSTALL_TEST_KEEP_BASE:-1}" = 0 ]; then docker image rm "$SYSTEMD_BASE" "$COMPOSE_BASE" >/dev/null 2>&1 || true; fi
  rm -rf "$WORK"
}
trap cleanup EXIT

echo "== 材料: dist.sh で looptrack（linux/$ARCH）を 2 版作る"
for n in 1 2; do
  RELEASE_CMDS=looptrack RELEASE_TARGETS="linux/$ARCH" bash deploy/release/dist.sh build "v0.0.0-installtest$n" "$WORK/dist$n" 2>/dev/null
  bash deploy/release/dist.sh sums "$WORK/dist$n" 2>/dev/null
done
# SHA256SUMS に使い捨ての鍵（パスワードなし）で署名する。手元に minisign が無ければ形だけの .minisig を置く
# （コンテナの中は偽の minisign で呼び出しを確かめるので、中身は問わない）
SIGN_ENV=()
if command -v minisign >/dev/null 2>&1; then
  minisign -G -W -p "$WORK/test.pub" -s "$WORK/test.key" >/dev/null
  for n in 1 2; do
    MINISIGN_SECRET_KEY_FILE="$WORK/test.key" MINISIGN_PUB_FILE="$WORK/test.pub" \
      bash deploy/release/dist.sh sign-sums "$WORK/dist$n" "install_test" >/dev/null
    MINISIGN_PUB_FILE="$WORK/test.pub" bash deploy/release/dist.sh verify "$WORK/dist$n" >/dev/null
  done
  SIGN_ENV=(-e "LOOPTRACK_INSTALL_MINISIGN_PUBKEY=$(sed -n 2p "$WORK/test.pub")")
else
  echo "（手元に minisign が無いため、形だけの SHA256SUMS.minisig を置きます）"
  for n in 1 2; do printf 'untrusted comment: placeholder\n' >"$WORK/dist$n/SHA256SUMS.minisig"; done
fi

run_plain() { # <イメージ>
  local name
  name="im0151-plain-$(echo "$1" | tr ':.' '--')"
  CONTAINERS+=("$name")
  echo
  echo "######## $1（systemd なし）"
  docker run --rm --name "$name" ${SIGN_ENV[@]+"${SIGN_ENV[@]}"} -v "$REPO/deploy:/src:ro" -v "$WORK/dist1:/dist:ro" "$1" \
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
    --tmpfs /run --tmpfs /run/lock -v "$REPO/deploy:/src:ro" -v "$WORK/dist1:/dist:ro" -v "$WORK/dist2:/dist2:ro" \
    "$SYSTEMD_BASE" >/dev/null
  wait_systemd || return 1
  docker exec im0151-systemd bash /src/install_test.sh --in-container systemd || return 1
  # OS の再起動の代わりにコンテナを起動し直し、サービスが自動で上がることを確かめてから --upgrade へ
  docker restart im0151-systemd >/dev/null
  wait_systemd || return 1
  docker exec -e UPGRADE_URL="http://host.docker.internal:$port/dist2" im0151-systemd bash /src/install_test.sh --in-container systemd-reboot
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
