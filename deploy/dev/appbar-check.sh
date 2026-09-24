#!/bin/bash
# Web 画面の共通ヘッダの崩れを、使い捨ての DB とローカルのサーバで測る。
#
#   deploy/dev/appbar-check.sh [スクリーンショットの保存先]
#
# 作業ツリーの looptrack をビルドし、ローカル MySQL（deploy/dev/compose.yaml・127.0.0.1:13306）に使い捨ての DB を作って
# 127.0.0.1:18111 で起動する。管理者・プロジェクト・イシュー 2 件・OAuth クライアントを作り、deploy/dev/appbar-check.mjs で
# 各画面を幅 360・375・601・605・600〜1280（40px 刻み）× ライト / ダークで測る。終わったらサーバを止めて DB を消す。
# 長い名前での崩れは APPBAR_USER_NAME / APPBAR_PROJECT_NAME で表示名・プロジェクト名を変えて確かめる。
# 前提: Google Chrome と npm の global の @playwright/test（npm i -g @playwright/test）。本番には触らない。
set -euo pipefail
cd "$(dirname "$0")/../.."
OUT="${1:-}"
PORT="${APPBAR_PORT:-18111}"
DB="im_appbar_$$"
WORK=$(mktemp -d)
unset LOOPTRACK_API_URL LOOPTRACK_PROJECT LOOPTRACK_TOKEN
export LOOPTRACK_DSN="root:imdev@tcp(127.0.0.1:13306)/$DB?parseTime=true"
mysqlx() { mysql -h127.0.0.1 -P13306 -uroot -pimdev "$@" 2>/dev/null; }
cleanup() {
  [ -n "${PID:-}" ] && kill "$PID" 2>/dev/null || true
  mysqlx -e "DROP DATABASE IF EXISTS $DB" || true
  rm -rf "$WORK"
}
trap cleanup EXIT

go build -o "$WORK/looptrack" ./cmd/looptrack
mysqlx -e "CREATE DATABASE $DB CHARACTER SET utf8mb4"
"$WORK/looptrack" migrate >/dev/null
echo "root-password-12" | "$WORK/looptrack" user add --admin --name "${APPBAR_USER_NAME:-管理 太郎}" --two-factor optional root >/dev/null
"$WORK/looptrack" project create --name "${APPBAR_PROJECT_NAME:-要件定義システム}" --prefix REQ req >/dev/null
"$WORK/looptrack" member set req root --role admin >/dev/null
TOKEN=$("$WORK/looptrack" token create root --name appbar --days 30 2>/dev/null | grep -o 'imp_[A-Za-z0-9_-]*' | head -1)

BASE=/looptrack # base path の既定
LOOPTRACK_SECRET_KEY=$("$WORK/looptrack" secret-key) LOOPTRACK_LISTEN="127.0.0.1:$PORT" LOOPTRACK_COOKIE_SECURE=false \
  "$WORK/looptrack" serve >"$WORK/serve.log" 2>&1 &
PID=$!
for _ in $(seq 50); do curl -sf "http://127.0.0.1:$PORT$BASE/healthz" >/dev/null && break; sleep 0.2; done

api() { curl -sf -X POST "http://127.0.0.1:$PORT$BASE$1" -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d "$2" >/dev/null; }
api /api/v1/projects/req/issues '{"title":"ヘッダの確認用イシュー","body":"本文"}'
api /api/v1/projects/req/issues '{"title":"二件目","body":"本文","status":"In Progress"}'
CLIENT=$(curl -sf -X POST "http://127.0.0.1:$PORT$BASE/oauth/register" -H 'Content-Type: application/json' \
  -d '{"client_name":"ヘッダ確認用","redirect_uris":["http://127.0.0.1:9999/cb"]}' |
  node -e 'let s="";process.stdin.on("data",d=>{s+=d}).on("end",()=>{console.log(JSON.parse(s).client_id)})')

APPBAR_BASE="http://127.0.0.1:$PORT$BASE" APPBAR_SLUG=req APPBAR_CLIENT_ID="$CLIENT" APPBAR_OUT="$OUT" \
  node deploy/dev/appbar-check.mjs
