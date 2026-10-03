#!/bin/bash
# deploy/release/release-notes.sh（2 本の CHANGELOG から Release の本文を作る）を確かめる。
# 合成の CHANGELOG で、両方に節がある版・片方にしか無い版・どちらにも無い版（エラー）を同じテストで見る。
# 片方にしか無い版は、もう片方の節に「変更はありません」が出ることを対照にする。
# 最後に、実物の CHANGELOG.md・CHANGELOG-desktop.md のどの版からも本文を作れることを確かめる。
#
#   bash deploy/release/release-notes_test.sh
set -euo pipefail
cd "$(dirname "$0")/../.."

SRC=deploy/release/release-notes.sh
fails=0
ok() { echo "  ok: $*"; }
ng() {
  echo "  NG: $*" >&2
  fails=$((fails + 1))
}

WORK=$(mktemp -d "${TMPDIR:-/tmp}/looptrack-release-notes.XXXXXX")
trap 'rm -rf "$WORK"' EXIT

cat >"$WORK/server.md" <<'EOF'
# Changelog: server

Intro of the server file.

## [Unreleased]

### Added

- SERVER-UNRELEASED

## [1.0.0-rc.2] - 2026-09-25

### Fixed

- SERVER-RC2-EN

---

### 修正

- SERVER-RC2-JA

## [1.0.0-rc.1] - 2026-09-20

- SERVER-RC1

## [1.0.0] - unreleased

- SERVER-100

[Unreleased]: https://example.invalid/compare/v1.0.0-rc.2...HEAD
[1.0.0-rc.2]: https://example.invalid/releases/tag/v1.0.0-rc.2
EOF

cat >"$WORK/desktop.md" <<'EOF'
# Changelog: desktop app

Intro of the desktop file.

## [Unreleased]

- DESKTOP-UNRELEASED

## [1.0.0-rc.2] - 2026-09-25

### Added

- DESKTOP-RC2-EN

---

### 追加

- DESKTOP-RC2-JA

## [1.0.0-rc.0] - 2026-09-10

- DESKTOP-RC0

[Unreleased]: https://example.invalid/compare/v1.0.0-rc.2...HEAD
EOF

NONE_SERVER="この版にサーバ版の変更はありません。"
NONE_DESKTOP="この版にデスクトップ版の変更はありません。"

# run <版> は本文を $WORK/out に、終了コードを rc に入れる
run() {
  rc=0
  bash "$SRC" "$1" "$WORK/server.md" "$WORK/desktop.md" >"$WORK/out" 2>"$WORK/err" || rc=$?
}
has() { grep -qF -- "$1" "$WORK/out"; }
# order <前> <後> は本文の中で <前> が <後> より前の行にあること
order() {
  local a b
  a=$(grep -nF -- "$1" "$WORK/out" | head -1 | cut -d: -f1)
  b=$(grep -nF -- "$2" "$WORK/out" | head -1 | cut -d: -f1)
  [ -n "$a" ] && [ -n "$b" ] && [ "$a" -lt "$b" ]
}

echo "両方に節がある版（タグの形 v1.0.0-rc.2）"
run v1.0.0-rc.2
[ "$rc" -eq 0 ] && ok "終了コード 0" || ng "終了コード ${rc}（$(cat "$WORK/err")）"
[ "$(head -1 "$WORK/out")" = "## Server（サーバ版）" ] && ok "1 行目がサーバ版の見出し" || ng "1 行目: $(head -1 "$WORK/out")"
order "## Server（サーバ版）" "SERVER-RC2-EN" && ok "サーバ版の見出しの後にサーバ版の英語" || ng "サーバ版の英語の位置"
order "SERVER-RC2-EN" "SERVER-RC2-JA" && ok "サーバ版は英語の後に日本語" || ng "サーバ版の英日の順"
order "SERVER-RC2-JA" "## Desktop app（デスクトップ版）" && ok "サーバ版の節の後にデスクトップ版の見出し" || ng "デスクトップ版の見出しの位置"
order "## Desktop app（デスクトップ版）" "DESKTOP-RC2-EN" && ok "デスクトップ版の見出しの後にデスクトップ版の英語" || ng "デスクトップ版の英語の位置"
order "DESKTOP-RC2-EN" "DESKTOP-RC2-JA" && ok "デスクトップ版は英語の後に日本語" || ng "デスクトップ版の英日の順"
if has "$NONE_SERVER" || has "$NONE_DESKTOP"; then ng "両方に節があるのに「変更はありません」が出た"; else ok "「変更はありません」は出ない"; fi
for x in SERVER-UNRELEASED SERVER-RC1 SERVER-100 DESKTOP-UNRELEASED DESKTOP-RC0 "example.invalid" "## [1.0.0-rc.2]" "Intro of"; do
  if has "$x"; then ng "ほかの節・比較リンク・見出しの行が混ざった: $x"; else ok "混ざらない: $x"; fi
done

echo "サーバ版にしか無い版（1.0.0-rc.1）"
run 1.0.0-rc.1
[ "$rc" -eq 0 ] && ok "終了コード 0" || ng "終了コード ${rc}"
has "SERVER-RC1" && ok "サーバ版の節がある" || ng "サーバ版の節が無い"
order "## Desktop app（デスクトップ版）" "$NONE_DESKTOP" && ok "デスクトップ版の節に「変更はありません」" || ng "デスクトップ版の節に「変更はありません」が無い"
has "No changes for the desktop app in this version." && ok "英語の「変更はありません」もある" || ng "英語の「変更はありません」が無い"
if has "$NONE_SERVER"; then ng "サーバ版の節に「変更はありません」が出た"; else ok "サーバ版の節は中身のまま"; fi

echo "デスクトップ版にしか無い版（1.0.0-rc.0）"
run 1.0.0-rc.0
[ "$rc" -eq 0 ] && ok "終了コード 0" || ng "終了コード ${rc}"
order "## Server（サーバ版）" "$NONE_SERVER" && ok "サーバ版の節に「変更はありません」" || ng "サーバ版の節に「変更はありません」が無い"
order "$NONE_SERVER" "DESKTOP-RC0" && ok "デスクトップ版の節は中身のまま" || ng "デスクトップ版の節の中身が無い"

echo "前方一致にしない（1.0.0 は 1.0.0-rc.* に当たらない）"
run 1.0.0
[ "$rc" -eq 0 ] && ok "終了コード 0" || ng "終了コード ${rc}"
has "SERVER-100" && ok "1.0.0 の節がある" || ng "1.0.0 の節が無い"
if has "SERVER-RC1" || has "SERVER-RC2-EN"; then ng "1.0.0 が rc の節に当たった"; else ok "rc の節は混ざらない"; fi
has "$NONE_DESKTOP" && ok "デスクトップ版に 1.0.0 は無い" || ng "デスクトップ版の「変更はありません」が無い"

echo "どちらにも無い版はエラー（9.9.9）"
run v9.9.9
[ "$rc" -eq 1 ] && ok "終了コード 1" || ng "終了コード ${rc}（1 のはず）"
[ ! -s "$WORK/out" ] && ok "標準出力は空" || ng "標準出力に何か出た"
grep -qF "9.9.9" "$WORK/err" && ok "エラーに版を示す" || ng "エラーに版が無い: $(cat "$WORK/err")"

echo "実物の CHANGELOG のどの版からも本文を作れる"
versions=$(sed -n 's/^## \[\([^]]*\)\].*/\1/p' CHANGELOG.md CHANGELOG-desktop.md | sort -u)
[ -n "$versions" ] && ok "版の見出しがある: $(echo "$versions" | tr '\n' ' ')" || ng "実物の CHANGELOG に版の見出しが無い"
for v in $versions; do
  rc=0
  bash "$SRC" "$v" >"$WORK/out" 2>"$WORK/err" || rc=$?
  if [ "$rc" -eq 0 ] && has "## Server（サーバ版）" && has "## Desktop app（デスクトップ版）"; then
    ok "$v"
  else
    ng "${v}（終了コード ${rc}: $(cat "$WORK/err")）"
  fi
done

if [ "$fails" -eq 0 ]; then
  echo "release-notes_test: OK"
else
  echo "release-notes_test: NG（${fails} 件）" >&2
  exit 1
fi
