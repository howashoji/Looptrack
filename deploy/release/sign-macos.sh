#!/bin/bash
# macOS の配布物に Developer ID で署名し、Apple の公証（notarization）を通す。
# 手元の Mac（rc の試し）と release.yml の sign-macos ジョブ（CI）で同じものを使う。手順は docs/server/RELEASE.md「署名」。
#
#   bash deploy/release/sign-macos.sh [オプション] <ファイル…>
#
# 受け取るもの:
#   単体の実行ファイル（Mach-O。dist.sh の *_darwin_amd64 / *_darwin_arm64）
#       → codesign（hardened runtime・安全なタイムスタンプ）→ まとめて zip にして notarytool submit --wait。
#         単体の実行ファイルと zip には staple できない（初回の起動時に Gatekeeper がオンラインで公証を確かめる）
#   <名前>.app（デスクトップ版）→ codesign → zip で公証 → stapler staple（オフラインでも通る）
#   <名前>.dmg                                   → codesign → dmg のまま公証 → stapler staple
#   .app を dmg に入れて配るときは、先に .app を渡して staple し、dmg を作ってから dmg を渡す（2 回に分けて呼ぶ）。
#
# オプション（環境変数でも渡せる。オプションが優先）:
#   --identity <名前 | SHA-1>    署名の身元（MACOS_SIGN_IDENTITY）。"-" は ad-hoc 署名（手元の動作確認用。公証はできない）。
#                                同じ名前の証明書がキーチェーンに複数あると codesign が選べないので、SHA-1（40 桁）で渡す
#   --keychain <パス>            証明書を探すキーチェーン（MACOS_SIGN_KEYCHAIN。CI の一時キーチェーン）。既定は検索リスト
#   --team-id <ID>               署名の TeamIdentifier がこれであることを確かめる（APPLE_TEAM_ID）
#   --id-prefix <接頭辞>         単体の実行ファイルの識別子（codesign --identifier）の接頭辞（MACOS_SIGN_ID_PREFIX）。
#                                識別子は <接頭辞><コマンド名>（例 looptrack）。既定は接頭辞なし（Bundle ID が決まったらそれに合わせる）
#   --entitlements <plist>       .app / 実行ファイルに付ける entitlements（既定なし。Go は JIT を使わないので要らない）
#   公証の認証（どちらか 1 つ）:
#   --notary-profile <名前>      notarytool のキーチェーンプロファイル（NOTARY_PROFILE）
#   --api-key <p8 のパス> --api-key-id <ID> --api-issuer <ID>
#                                App Store Connect API キー（APPLE_API_KEY_PATH・APPLE_API_KEY_ID・APPLE_API_ISSUER_ID。CI）
#   --skip-notarize              署名だけ行い、公証しない（ad-hoc の動作確認用。配布物には使わない）
#
# 秘密（.p12・.p8 の中身・パスワード）はこのスクリプトでは扱わない（キーチェーンとファイルのパスを受け取るだけ）。
# 署名するとファイルのバイト列が変わるので、SHA256SUMS はこの後に作る（dist.sh sums → dist.sh sign-sums）。
set -euo pipefail

IDENTITY="${MACOS_SIGN_IDENTITY:-}"
KEYCHAIN="${MACOS_SIGN_KEYCHAIN:-}"
TEAM_ID="${APPLE_TEAM_ID:-}"
ID_PREFIX="${MACOS_SIGN_ID_PREFIX:-}"
ENTITLEMENTS=""
PROFILE="${NOTARY_PROFILE:-}"
API_KEY="${APPLE_API_KEY_PATH:-}"
API_KEY_ID="${APPLE_API_KEY_ID:-}"
API_ISSUER="${APPLE_API_ISSUER_ID:-}"
SKIP_NOTARIZE=0

die() {
  echo "sign-macos: エラー: $*" >&2
  exit 1
}
usage() {
  sed -n '2,30p' "$0" >&2
  exit 2
}

files=()
while [ $# -gt 0 ]; do
  case $1 in
    --identity) IDENTITY=${2:?--identity <名前 | SHA-1>}; shift 2 ;;
    --keychain) KEYCHAIN=${2:?--keychain <パス>}; shift 2 ;;
    --team-id) TEAM_ID=${2:?--team-id <ID>}; shift 2 ;;
    --id-prefix) ID_PREFIX=${2?--id-prefix <接頭辞>}; shift 2 ;;
    --entitlements) ENTITLEMENTS=${2:?--entitlements <plist>}; shift 2 ;;
    --notary-profile) PROFILE=${2:?--notary-profile <名前>}; shift 2 ;;
    --api-key) API_KEY=${2:?--api-key <p8 のパス>}; shift 2 ;;
    --api-key-id) API_KEY_ID=${2:?--api-key-id <ID>}; shift 2 ;;
    --api-issuer) API_ISSUER=${2:?--api-issuer <ID>}; shift 2 ;;
    --skip-notarize) SKIP_NOTARIZE=1; shift ;;
    -h | --help) usage ;;
    --) shift; files+=("$@"); break ;;
    -*) die "不明なオプションです: $1（--help）" ;;
    *) files+=("$1"); shift ;;
  esac
done

[ "$(uname -s)" = Darwin ] || die "macOS で実行してください（codesign・notarytool が要る）"
[ ${#files[@]} -gt 0 ] || die "署名するファイルを渡してください（--help）"
[ -n "$IDENTITY" ] || die "署名の身元がありません（--identity か MACOS_SIGN_IDENTITY。ad-hoc で試すなら \"-\"）"
[ -z "$ENTITLEMENTS" ] || [ -f "$ENTITLEMENTS" ] || die "entitlements がありません: $ENTITLEMENTS"

ADHOC=0
[ "$IDENTITY" = "-" ] && ADHOC=1

# 公証の認証を決める（ad-hoc は公証できない）
notary_auth=()
if [ "$SKIP_NOTARIZE" = 0 ]; then
  [ "$ADHOC" = 0 ] || die "ad-hoc 署名（--identity -）は公証できません。--skip-notarize を付けてください"
  if [ -n "$PROFILE" ]; then
    notary_auth=(--keychain-profile "$PROFILE")
    [ -z "$KEYCHAIN" ] || notary_auth+=(--keychain "$KEYCHAIN")
  elif [ -n "$API_KEY" ] || [ -n "$API_KEY_ID" ] || [ -n "$API_ISSUER" ]; then
    [ -n "$API_KEY" ] && [ -n "$API_KEY_ID" ] && [ -n "$API_ISSUER" ] ||
      die "API キーで公証するには --api-key・--api-key-id・--api-issuer の 3 つが要ります"
    [ -f "$API_KEY" ] || die "API キーのファイルがありません: $API_KEY"
    notary_auth=(--key "$API_KEY" --key-id "$API_KEY_ID" --issuer "$API_ISSUER")
  else
    die "公証の認証がありません（--notary-profile か --api-key 一式。署名だけなら --skip-notarize）"
  fi
fi

# 署名の身元が 1 つに決まることを確かめる（同じ名前の証明書が複数あると codesign が止まるので、先に分かりやすく止める）。
# 出力は数えるだけで表示しない。
if [ "$ADHOC" = 0 ] && [[ ! "$IDENTITY" =~ ^[0-9A-Fa-f]{40}$ ]]; then
  kc=()
  [ -z "$KEYCHAIN" ] || kc=("$KEYCHAIN")
  n=$(security find-identity -v -p codesigning ${kc[@]+"${kc[@]}"} | grep -cF "\"$IDENTITY" || true)
  [ "$n" -ge 1 ] || die "署名の身元がキーチェーンにありません: $IDENTITY"
  [ "$n" -eq 1 ] || die "「${IDENTITY}」に合う証明書が $n 個あります。使う証明書の SHA-1（40 桁）を --identity に渡してください（RELEASE.md「署名」）"
fi

sign_args=(--force --sign "$IDENTITY")
[ -z "$KEYCHAIN" ] || sign_args+=(--keychain "$KEYCHAIN")
if [ "$ADHOC" = 1 ]; then
  sign_args+=(--timestamp=none)
else
  sign_args+=(--timestamp)
fi

is_macho() {
  lipo -archs "$1" >/dev/null 2>&1
}

# check_sig <パス> — 署名を検証し、要点（識別子・署名者・Team・hardened runtime）を出す
check_sig() {
  codesign --verify --strict --verbose=2 "$1" 2>&1 | sed 's/^/    /'
  local info
  info=$(codesign -dvv "$1" 2>&1)
  grep -E '^(Identifier|Authority|TeamIdentifier|Timestamp)=|flags=' <<<"$info" | sed 's/^/    /' || true
  if [ -n "$TEAM_ID" ] && [ "$ADHOC" = 0 ]; then
    grep -qx "TeamIdentifier=$TEAM_ID" <<<"$info" || die "$1 の TeamIdentifier が $TEAM_ID ではありません"
  fi
}

tmpbase=${RUNNER_TEMP:-${TMPDIR:-/tmp}}
WORK=$(mktemp -d "${tmpbase%/}/sign-macos.XXXXXX")
trap 'rm -rf "$WORK"' EXIT

bins=() apps=() dmgs=()
for f in "${files[@]}"; do
  f=${f%/}
  [ -e "$f" ] || die "ありません: $f"
  case $f in
    *.app) [ -d "$f" ] || die ".app がディレクトリではありません: $f"; apps+=("$f") ;;
    *.dmg) dmgs+=("$f") ;;
    *)
      if [ ! -f "$f" ] || ! is_macho "$f"; then
        die "macOS の実行ファイル（Mach-O）・.app・.dmg ではありません: $f"
      fi
      bins+=("$f")
      ;;
  esac
done

# ---------------------------------------------------------------- 署名
for f in ${bins[@]+"${bins[@]}"}; do
  base=$(basename "$f")
  cmd=${base%%_*}
  echo "==> 署名: ${f}（識別子 ${ID_PREFIX}${cmd}）"
  args=("${sign_args[@]}" --options runtime --identifier "${ID_PREFIX}${cmd}")
  [ -z "$ENTITLEMENTS" ] || args+=(--entitlements "$ENTITLEMENTS")
  codesign "${args[@]}" "$f"
  check_sig "$f"
done
for f in ${apps[@]+"${apps[@]}"}; do
  echo "==> 署名: $f"
  # --deep は使わない。中に別の実行ファイルや framework を入れるときは、内側から順に署名してからここに来る
  args=("${sign_args[@]}" --options runtime)
  [ -z "$ENTITLEMENTS" ] || args+=(--entitlements "$ENTITLEMENTS")
  codesign "${args[@]}" "$f"
  check_sig "$f"
done
for f in ${dmgs[@]+"${dmgs[@]}"}; do
  echo "==> 署名: $f"
  codesign "${sign_args[@]}" "$f"
  check_sig "$f"
done

if [ "$SKIP_NOTARIZE" = 1 ]; then
  echo "公証は省きました（--skip-notarize）。配布物にはしないでください"
  exit 0
fi

# ---------------------------------------------------------------- 公証
# notarize <提出するファイル> <説明> — submit --wait し、Accepted でなければログを出して止める
notarize() {
  local sub=$1 what=$2 out status id
  echo "==> 公証: $what"
  out="$WORK/submit.plist"
  if ! xcrun notarytool submit "$sub" "${notary_auth[@]}" --wait --output-format plist >"$out"; then
    cat "$out" >&2 || true
    die "notarytool submit が失敗しました（${what}）"
  fi
  status=$(plutil -extract status raw -o - "$out" 2>/dev/null || echo "?")
  id=$(plutil -extract id raw -o - "$out" 2>/dev/null || echo "?")
  echo "    提出 ID: ${id}・結果: $status"
  if [ "$status" != Accepted ]; then
    # ログには秘密は含まれない（どのファイルのどの問題かが JSON で返る）
    xcrun notarytool log "$id" "${notary_auth[@]}" >&2 || true
    die "公証が通りませんでした（${what}・${status}）"
  fi
}

if [ ${#bins[@]} -gt 0 ]; then
  # 単体の実行ファイルはまとめて 1 つの zip で出す（staple はできない）
  mkdir "$WORK/bins"
  for f in ${bins[@]+"${bins[@]}"}; do
    ditto "$f" "$WORK/bins/$(basename "$f")"
  done
  ditto -c -k --sequesterRsrc "$WORK/bins" "$WORK/bins.zip"
  notarize "$WORK/bins.zip" "実行ファイル ${#bins[@]} 個"
fi
for f in ${apps[@]+"${apps[@]}"}; do
  z="$WORK/$(basename "$f").zip"
  ditto -c -k --keepParent "$f" "$z"
  notarize "$z" "$f"
  xcrun stapler staple "$f"
  xcrun stapler validate "$f"
  spctl --assess --type execute --verbose=2 "$f"
done
for f in ${dmgs[@]+"${dmgs[@]}"}; do
  notarize "$f" "$f"
  xcrun stapler staple "$f"
  xcrun stapler validate "$f"
  spctl --assess --type open --context context:primary-signature --verbose=2 "$f"
done
echo "署名と公証が終わりました（${#bins[@]} 個の実行ファイル・${#apps[@]} 個の .app・${#dmgs[@]} 個の dmg）"
