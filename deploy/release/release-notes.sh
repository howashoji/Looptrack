#!/bin/bash
# GitHub の Release の本文を、2 本の CHANGELOG の同じ版の節から作る。
# サーバ版の CHANGELOG.md とデスクトップ版の CHANGELOG-desktop.md は版番号とタグを共有するので、
# Release の本文は「Server」「Desktop app」の 2 節にし、それぞれ CHANGELOG の節（英語・`---`・日本語）をそのまま写す。
# 読み手が 1 行目で自分の版の節を探せるようにするため。
#
#   bash deploy/release/release-notes.sh <版> [<サーバ版の CHANGELOG> <デスクトップ版の CHANGELOG>] > notes.md
#
# <版> はタグの形（v1.0.0-rc.5）でも CHANGELOG の見出しの形（1.0.0-rc.5）でもよい。
# 片方の CHANGELOG にその版の節が無ければ、その節に「この版の変更はありません」を日英で出す。
# どちらにも無ければ何も出さずに終了コード 1 で止まる（版の打ち間違い・CHANGELOG の書き忘れを公開の前に止める）。
set -euo pipefail

usage() {
  echo "使い方: bash deploy/release/release-notes.sh <版> [<サーバ版の CHANGELOG> <デスクトップ版の CHANGELOG>]" >&2
  exit 2
}

[ $# -eq 1 ] || [ $# -eq 3 ] || usage
root="$(cd "$(dirname "$0")/../.." && pwd)"
ver="${1#v}"
server_md="${2:-$root/CHANGELOG.md}"
desktop_md="${3:-$root/CHANGELOG-desktop.md}"
[ -n "$ver" ] || usage
for f in "$server_md" "$desktop_md"; do
  [ -f "$f" ] || {
    echo "CHANGELOG がありません: $f" >&2
    exit 1
  }
done

# section は <ファイル> の「## [<版>]」の節の本文（見出しの行を除き、前後の空行を落とす）を出す。
# 節は次の「## [」か、末尾の比較リンク（[…]: …）の手前で終わる。見出しは「## [<版>]」の直後が行末か空白のものだけを
# その版とみなす（1.0.0 が 1.0.0-rc.5 に当たらないように、前方一致にしない）。
section() {
  awk -v want="## [$ver]" '
    !on && index($0, want) == 1 && (length($0) == length(want) || substr($0, length(want) + 1, 1) == " ") { on = 1; next }
    on && (/^## \[/ || /^\[[^]]+\]: /) { exit }
    on { print }
  ' "$1" | sed -e '/./,$!d'
}

server_body="$(section "$server_md")"
desktop_body="$(section "$desktop_md")"
if [ -z "$server_body" ] && [ -z "$desktop_body" ]; then
  echo "どちらの CHANGELOG にも版 ${ver} の節がありません（${server_md}・${desktop_md}）" >&2
  exit 1
fi

none_server=$'No changes for the server in this version.\n\n---\n\nこの版にサーバ版の変更はありません。'
none_desktop=$'No changes for the desktop app in this version.\n\n---\n\nこの版にデスクトップ版の変更はありません。'

printf '## Server（サーバ版）\n\n%s\n\n## Desktop app（デスクトップ版）\n\n%s\n' \
  "${server_body:-$none_server}" "${desktop_body:-$none_desktop}"
