#!/bin/bash
# 利用者ガイドと README のスクリーンショットを、合成データだけを入れた使い捨てのサーバから撮り直す。
#
#   deploy/dev/screenshots.sh [保存先]        （既定 docs/guide/images。その下の ja/ と en/ に書く）
#
# 作業ツリーの looptrack を一時ディレクトリにビルドし、言語（ja・en）ごとに、まっさらな SQLite のローカルモードの
# サーバを 127.0.0.1:${SHOT_PORT:-8090} で立てる。deploy/dev/screenshots.mjs が初回設定の画面から進め、架空の
# プロジェクト demo と課題を API で作って、ヘッドレスのブラウザでページの中だけを撮る。終わったらサーバを止め、
# 一時ディレクトリ（DB・設定・HOME）を消す。HOME と接続先の環境変数は使い捨てのものに差し替えるので、
# 手元の設定・本番のサーバ・開発用 MySQL・起動中のデスクトップ版には触れない。
# 画面に出る URL（初回設定を終えた画面の URL と MCP の接続設定）は、デスクトップ版の既定のポート 18090 の形にする。
# 待ち受けは SHOT_PORT のまま、公開 URL（LOOPTRACK_PUBLIC_URL）を http://127.0.0.1:${SHOT_URL_PORT:-18090} にして、
# サーバ自身にその URL で文面を組ませる。ブラウザは 18090 には一度もつながない（起動中のデスクトップ版に触れない）。
# 前提: Google Chrome と npm の global の @playwright/test（npm i -g @playwright/test）。ポートは空いていること。
# 撮り直したら、PNG を 1 枚ずつ目で見て、個人の情報や本物のプロジェクトが写っていないことを確かめてからコミットする。
set -euo pipefail
cd "$(dirname "$0")/../.."
OUT="${1:-docs/guide/images}"
PORT="${SHOT_PORT:-8090}"
URL_PORT="${SHOT_URL_PORT:-18090}"
BASE=/looptrack
WORK=$(mktemp -d)
PID=
cleanup() {
  [ -n "$PID" ] && kill "$PID" 2>/dev/null && wait "$PID" 2>/dev/null || true
  rm -rf "$WORK"
}
trap cleanup EXIT

if curl -s -o /dev/null "http://127.0.0.1:$PORT/"; then
  echo "127.0.0.1:$PORT は使用中です。SHOT_PORT で空いているポートを指定してください" >&2
  exit 1
fi
go build -o "$WORK/looptrack" ./cmd/looptrack

for lang in ja en; do
  dir="$WORK/$lang"
  mkdir -p "$dir/home"
  # 環境は空から組み立てる（LOOPTRACK_API_URL・LOOPTRACK_PROJECT・利用者の HOME を引き継がない）
  env -i PATH="$PATH" HOME="$dir/home" TZ=UTC \
    LOOPTRACK_DSN="sqlite:$dir/im.db" LOOPTRACK_LOCAL_MODE=1 LOOPTRACK_LISTEN="127.0.0.1:$PORT" \
    LOOPTRACK_UPDATE_CHECK=off LOOPTRACK_DIST_DIR= LOOPTRACK_PUBLIC_URL="http://127.0.0.1:$URL_PORT" \
    "$WORK/looptrack" serve >"$dir/serve.log" 2>&1 &
  PID=$!
  ok=
  for _ in $(seq 50); do
    if curl -sf "http://127.0.0.1:$PORT$BASE/healthz" >/dev/null; then ok=1; break; fi
    sleep 0.2
  done
  if [ -z "$ok" ]; then
    echo "サーバが 10 秒で起動しませんでした（${lang}）" >&2
    tail -20 "$dir/serve.log" >&2
    exit 1
  fi
  SHOT_BASE="http://127.0.0.1:$PORT$BASE" SHOT_LANG="$lang" SHOT_OUT="$OUT/$lang" \
    node deploy/dev/screenshots.mjs
  kill "$PID"
  wait "$PID" 2>/dev/null || true
  PID=
done
du -ck "$OUT"/ja/*.png "$OUT"/en/*.png | tail -1 | awk '{print "合計 " $1 " KB"}'
