#!/bin/bash
# deploy/release/sign-macos.sh の staple_validate（stapler の試し直し）を、偽の xcrun で確かめる。
# 本物の公証・署名は呼ばない。sign-macos.sh 本体は macOS 専用の処理を先に実行するので、
# 対象の関数だけを取り出して読み込む（desktop_version_test.sh と同じ作法）。
#
#   bash deploy/release/sign-macos_test.sh
set -euo pipefail
cd "$(dirname "$0")/../.."

SRC=deploy/release/sign-macos.sh
fails=0
ok() { echo "  ok: $*"; }
ng() {
  echo "  NG: $*" >&2
  fails=$((fails + 1))
}

die() {
  echo "sign-macos: エラー: $*" >&2
  exit 1
}
eval "$(sed -n '/^staple_validate() {/,/^}/p' "$SRC")"

export STAPLE_RETRY_WAIT=0
calls=$(mktemp "${TMPDIR:-/tmp}/sign-macos-test.XXXXXX")
trap 'rm -f "$calls"' EXIT

# 偽の xcrun: staple の呼び出しが FAIL_UNTIL 回目より前なら Error 68 で落とす（回数は $calls に数える）
xcrun() {
  [ "$1" = stapler ] || return 0
  if [ "$2" = staple ]; then
    echo x >>"$calls"
    if [ "$(wc -l <"$calls")" -lt "$FAIL_UNTIL" ]; then
      echo "The network connection was lost. Error 68." >&2
      return 68
    fi
  fi
  echo "The $2 action worked!"
}
count() { wc -l <"$calls" | tr -d ' '; }

# 2 回失敗して 3 回目に通る
: >"$calls"
FAIL_UNTIL=3 STAPLE_MAX_ATTEMPTS=5
export FAIL_UNTIL STAPLE_MAX_ATTEMPTS
if (staple_validate app.dmg) >/dev/null 2>&1 && [ "$(count)" = 3 ]; then
  ok "2 回失敗しても 3 回目で通る（staple は 3 回）"
else
  ng "3 回目で通るはずが、通らないか回数が違います（staple $(count) 回）"
fi

# 上限まで失敗して止まる（対照: 同じ関数が上の例では通る）
: >"$calls"
FAIL_UNTIL=99 STAPLE_MAX_ATTEMPTS=4
if err=$( (staple_validate app.dmg) 2>&1 >/dev/null); then
  ng "上限まで失敗したのに成功しました"
elif [ "$(count)" = 4 ] && grep -q "4 回続けて失敗" <<<"$err" && grep -q "Error 68" <<<"$err"; then
  ok "上限の 4 回で止まり、理由と最後の出力を出す"
else
  ng "上限での止まり方が違います（staple $(count) 回）: $err"
fi

[ "$fails" = 0 ] || exit 1
echo "sign-macos_test: 全部通りました"
