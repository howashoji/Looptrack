#!/bin/bash
# deploy/release/desktop.sh の plist_version（Info.plist の CFBundleShortVersionString）と
# iss_version（Inno Setup の VersionInfoVersion）を確かめる。ビルドは伴わない。
# desktop.sh 本体には cd や実ビルドの副作用・末尾の case による exit があるため丸ごとの source はせず、
# 対象の関数だけを取り出して読み込む。
#
#   bash deploy/release/desktop_version_test.sh
set -euo pipefail
cd "$(dirname "$0")/../.."

SRC=deploy/release/desktop.sh
fails=0
ok() { echo "  ok: $*"; }
ng() {
  echo "  NG: $*" >&2
  fails=$((fails + 1))
}

extract() { sed -n "/^$1() {/,/^}/p" "$SRC"; }
eval "$(extract plist_version)"
eval "$(extract iss_version)"

check_plist() { # <入力> <期待>
  local got
  got=$(plist_version "$1")
  [ "$got" = "$2" ] && ok "plist_version $1 -> $got" || ng "plist_version $1 -> ${got}（期待 $2）"
}
check_iss() { # <入力> <期待>
  local got
  got=$(iss_version "$1")
  [ "$got" = "$2" ] && ok "iss_version $1 -> $got" || ng "iss_version $1 -> ${got}（期待 $2）"
}

# 日付-コミットの試しの版（点が無い）は 0.0.0 にする
check_plist "20260925-ddc984df1eb4" "0.0.0"
check_iss "20260925-ddc984df1eb4" "0.0.0"

# 対照: 通常のタグ・プレリリース・2 分割の版は従来どおり通す
check_plist "1.0.0-rc.2" "1.0.0"
check_plist "1.2.3" "1.2.3"
check_plist "v1.2.3" "1.2.3"
check_plist "1.2" "1.2"

check_iss "1.0.0-rc.2" "1.0.0"
check_iss "1.2.3" "1.2.3"

if [ "$fails" -eq 0 ]; then
  echo "desktop_version_test: OK"
else
  echo "desktop_version_test: NG（${fails} 件）" >&2
  exit 1
fi
