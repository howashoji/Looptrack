#!/bin/bash
# deploy/release/desktop.sh の plist_version（Info.plist の CFBundleShortVersionString）と
# iss_version（Inno Setup の VersionInfoVersion）を確かめる。looptrack 本体のビルドは伴わない。
# iss_version と同じ規則を Go の exe の版の情報の固定部（internal/tools/winversion の numeric）も使うので、
# 同じ入力で両者が一致することも確かめる（その道具だけは go build する）。
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

# iss_version と winversion numeric は同じ規則（片方だけ直すと、インストーラと exe で固定部の版が食い違う）
WV_DIR=$(mktemp -d "${TMPDIR:-/tmp}/looptrack-winversion.XXXXXX")
trap 'rm -rf "$WV_DIR"' EXIT
go build -o "$WV_DIR/winversion" ./internal/tools/winversion
for v in "20260925-ddc984df1eb4" "20261002-abc1234" "1.0.0-rc.2" "v1.0.0-rc.5" "v9.8.7-rc.1" "1.2.3" "v1.2.3" "1.2" "v1.2.3+meta" "v70000.0.0" "v1" "v1.2.3.4.5"; do
  want=$(iss_version "$v")
  got=$("$WV_DIR/winversion" numeric "$v")
  [ "$got" = "$want" ] && ok "winversion numeric $v -> $got" || ng "winversion numeric $v -> ${got}（iss_version は ${want}）"
done

if [ "$fails" -eq 0 ]; then
  echo "desktop_version_test: OK"
else
  echo "desktop_version_test: NG（${fails} 件）" >&2
  exit 1
fi
