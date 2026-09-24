#!/bin/bash
# デスクトップ版の起動を確かめる（release.yml の desktop-* ジョブと手元で使う）。
#
#   bash deploy/release/desktop_smoke.sh <looptrack の desktop ビルド（.app の中・AppImage の AppRun など）>
#
# データ・HOME は一時ディレクトリ（利用者の本物の置き場を使わない）。トレイは出さない（--no-tray）。ブラウザは開かない（BROWSER=true）。
#   1. looptrack desktop --no-tray --background で起動 → --status で URL が分かり、healthz が ok、画面は初回設定へ転送（303）
#   2. 2 つ目の起動（looptrack desktop --background）は二重に立たず「既に起動しています」で 0 で終わる
#   3. looptrack desktop --quit で止まり、--status が 1 になる
set -euo pipefail
bin=${1:?使い方: desktop_smoke.sh <looptrack>}
work=$(mktemp -d "${TMPDIR:-/tmp}/looptrack-smoke.XXXXXX")
pid=""
cleanup() {
  if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
    kill "$pid" 2>/dev/null || true
  fi
  rm -rf "$work"
}
trap cleanup EXIT
export LOOPTRACK_DATA_DIR="$work/data" LOOPTRACK_DESKTOP_PORT=0 HOME="$work/home" BROWSER=true XDG_CONFIG_HOME="$work/home/.config"
mkdir -p "$HOME"

fail() {
  echo "desktop_smoke: NG: $*" >&2
  [ -f "$work/data/logs/looptrack.log" ] && tail -20 "$work/data/logs/looptrack.log" >&2
  exit 1
}

"$bin" desktop --no-tray --background >"$work/out.txt" 2>&1 &
pid=$!
url=""
for _ in $(seq 1 150); do
  if url=$("$bin" desktop --status 2>/dev/null); then
    break
  fi
  kill -0 "$pid" 2>/dev/null || fail "起動しないで終わった: $(cat "$work/out.txt")"
  url=""
  sleep 0.2
done
[ -n "$url" ] || fail "30 秒で起動しない"
echo "起動: $url"
[ "$(curl -fsS "${url}healthz")" = ok ] || fail "healthz"
code=$(curl -s -o /dev/null -w '%{http_code} %{redirect_url}' "$url")
case "$code" in "303 "*/looptrack/first-run) ;; *) fail "画面が初回設定へ転送しない: $code" ;; esac
out=$("$bin" desktop --background) || fail "2 つ目の起動が失敗した"
case "$out" in *既に起動しています*) ;; *) fail "2 つ目の起動が二重に立った？: $out" ;; esac
"$bin" desktop --quit >/dev/null || fail "--quit"
for _ in $(seq 1 100); do
  kill -0 "$pid" 2>/dev/null || break
  sleep 0.2
done
kill -0 "$pid" 2>/dev/null && fail "--quit の後も動いている"
wait "$pid" || fail "終了コードが 0 でない"
pid=""
if "$bin" desktop --status >/dev/null 2>&1; then
  fail "止めた後も --status が 0"
fi
for f in looptrack.db looptrack.db.secret-key desktop.json logs/looptrack.log; do
  [ -e "$work/data/$f" ] || fail "$f が無い"
done
echo "desktop_smoke: OK（起動・画面・二重起動の防止・--quit）"
