#!/bin/bash
# リリース物（実行ファイル・SHA256SUMS・コンテナイメージの材料）を作る。
# CI（.github/workflows/ci.yml のクロスビルド）とリリース（.github/workflows/release.yml）と手元で同じものを使う。
# 手順の全体は docs/server/RELEASE.md。
#
#   bash deploy/release/dist.sh build <版> <出力先>
#       → <出力先>/<コマンド>_<版>_<os>_<arch>[.exe]（6 対象 × RELEASE_CMDS）
#         looptrack を作るときは、第三者のライセンス文 <出力先>/NOTICE（依存モジュール・Go・フォント）と
#         埋め込んだフォント（BIZ UDGothic）のライセンス文 <出力先>/OFL-BIZUDGothic.txt も置く（どちらも SHA256SUMS に載る）
#         インストーラ <出力先>/install.sh と、MySQL の最小権限 <出力先>/grants.sql も同じように置く
#         （どちらも SHA256SUMS に載る。install.sh は「取得して実行する 1 行」の入口で、取り出した配布物の
#           ディレクトリをそのまま --from に渡せる。grants.sql は install.sh が案内する順番で流す）
#   bash deploy/release/dist.sh sums <出力先>
#       → <出力先>/SHA256SUMS（署名で中身が変わるので、署名の後に作る。macOS は sign-macos.sh の後）
#   bash deploy/release/dist.sh sign-sums <出力先> [<trusted comment>]
#       → <出力先>/SHA256SUMS.minisig（minisign で署名し、公開鍵 deploy/release/minisign.pub で確かめる）
#         秘密鍵は MINISIGN_SECRET_KEY_FILE（既定 ~/.minisign/minisign.key）。パスワードは端末で問われる（CI は標準入力で渡す）
#   bash deploy/release/dist.sh verify <出力先>
#       → SHA256SUMS の照合と、SHA256SUMS.minisig があれば署名の確認（公開鍵は MINISIGN_PUB_FILE。既定 deploy/release/minisign.pub）
#   bash deploy/release/dist.sh image-context <版> <arch> <出力先> <作業ディレクトリ>
#       → <作業ディレクトリ>/Dockerfile と looptrack（<出力先> の linux/<arch> の実行ファイルを写す。サーバのイメージ）と NOTICE
#
# ビルドの形は deploy/build.sh と同じ（CGO_ENABLED=0・-trimpath・-s -w・-X main.version=<版>）。
# 対象のコマンドは RELEASE_CMDS（既定 "looptrack"。サーバは looptrack に統合した）、
# 対象の OS/arch は RELEASE_TARGETS（既定は linux / darwin / windows × amd64 / arm64）。
# macOS の desktop ビルド（cgo・トレイ）はここでは作らない（macOS の runner が要る。RELEASE.md「desktop ビルド」）。
# looptrack は SHA256SUMS の署名を確かめる公開鍵（selfupdate.MinisignPublicKey）をソースに持つ。RELEASE_MINISIGN_PUBKEY を
# 設定すると（空も含む）-X で差し替える。空は「署名を確かめない」ビルドで、署名しない配布（サーバの配布ディレクトリに置く配布。docs/server/RELEASE.md §2-1）だけが使う。
set -euo pipefail
cd "$(dirname "$0")/../.."

CMDS="${RELEASE_CMDS:-looptrack}"
# looptrack に埋め込むフォントのライセンス文（SIL OFL 1.1 はフォントを配るときにライセンス文を添えることを求める）
OFL_SRC="internal/client/report/pdf/fonts/OFL.txt"
OFL_NAME="OFL-BIZUDGothic.txt"
# 第三者のライセンス文（go run ./internal/tools/notice が作ってコミットしたもの）
NOTICE_SRC="NOTICE"
# リリースの資産として配るもの（取得の 1 行で install.sh だけを取れるよう、名前は元のまま）
INSTALL_SRC="deploy/install.sh"
INSTALL_NAME="install.sh"
GRANTS_SRC="deploy/grants.sql"
GRANTS_NAME="grants.sql"
TARGETS="${RELEASE_TARGETS:-linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64}"
PUBKEY_VAR="github.com/howashoji/looptrack/internal/client/selfupdate.MinisignPublicKey"

usage() {
  sed -n '2,27p' "$0" >&2
  exit 2
}

# 版はファイル名と -X に入るので、使える文字を絞る（タグ v1.2.3・v1.0.0-rc.1・日付-コミット ID）。
check_version() {
  if [[ ! "$1" =~ ^[0-9A-Za-z][0-9A-Za-z._+-]*$ ]]; then
    echo "版の形が不正です: $1" >&2
    exit 2
  fi
}

build() {
  local version="$1" out="$2" cmd target os arch ext file ldflags
  check_version "$version"
  mkdir -p "$out"
  ldflags="-s -w -X main.version=$version"
  if [ -n "${RELEASE_MINISIGN_PUBKEY+x}" ]; then
    if [[ ! "$RELEASE_MINISIGN_PUBKEY" =~ ^[A-Za-z0-9+/=]*$ ]]; then
      echo "RELEASE_MINISIGN_PUBKEY の形が不正です（minisign.pub の 2 行目の base64）" >&2
      exit 2
    fi
    ldflags="$ldflags -X $PUBKEY_VAR=$RELEASE_MINISIGN_PUBKEY"
  fi
  for cmd in $CMDS; do
    if [ ! -d "cmd/$cmd" ]; then
      echo "cmd/$cmd がありません（RELEASE_CMDS を確かめる）" >&2
      exit 1
    fi
    for target in $TARGETS; do
      os="${target%/*}"
      arch="${target#*/}"
      ext=""
      [ "$os" = windows ] && ext=".exe"
      file="$out/${cmd}_${version}_${os}_${arch}${ext}"
      echo "build $file" >&2
      CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
        -ldflags "$ldflags" -o "$file" "./cmd/$cmd"
    done
    if [ "$cmd" = looptrack ]; then
      cp "$OFL_SRC" "$out/$OFL_NAME"
      cp "$NOTICE_SRC" "$out/NOTICE"
      # インストーラと MySQL の最小権限。取り出した配布物のディレクトリをそのまま --from に渡せるよう、
      # 実行ファイルと同じディレクトリに同じ名前で置く（SHA256SUMS に載るので、取得した install.sh 自体を照合できる）
      cp "$INSTALL_SRC" "$out/$INSTALL_NAME"
      chmod 0755 "$out/$INSTALL_NAME"
      cp "$GRANTS_SRC" "$out/$GRANTS_NAME"
      chmod 0644 "$out/$GRANTS_NAME"
    fi
  done
}

sums() {
  local out="$1"
  (
    cd "$out"
    rm -f SHA256SUMS
    # 配るファイルすべて（実行ファイル・NOTICE・OFL・install.sh・grants.sql・デスクトップ版）。
    # SHA256SUMS 自身と署名のファイルは含めない。名前順で固定する。
    local files
    files=$(find . -maxdepth 1 -type f ! -name 'SHA256SUMS*' ! -name '*.sig' ! -name '*.pem' ! -name '*.bundle' | sed 's|^\./||' | LC_ALL=C sort)
    if [ -z "$files" ]; then
      echo "$out に実行ファイルがありません" >&2
      exit 1
    fi
    if command -v sha256sum >/dev/null 2>&1; then
      # shellcheck disable=SC2086
      sha256sum $files >SHA256SUMS
    else
      # shellcheck disable=SC2086
      shasum -a 256 $files >SHA256SUMS
    fi
    cat SHA256SUMS >&2
  )
}

# minisign の公開鍵のファイル（確かめるのに使う。テストでは使い捨ての鍵に差し替える）
pub_file() {
  local f="${MINISIGN_PUB_FILE:-deploy/release/minisign.pub}"
  [ -f "$f" ] || { echo "公開鍵がありません: $f" >&2; exit 1; }
  (cd "$(dirname "$f")" && echo "$PWD/$(basename "$f")")
}

sign_sums() {
  local out="$1" comment="${2:-}" key pub
  command -v minisign >/dev/null 2>&1 || { echo "minisign がありません（brew install minisign / apt-get install minisign）" >&2; exit 1; }
  [ -f "$out/SHA256SUMS" ] || { echo "$out/SHA256SUMS がありません（先に sums を流す）" >&2; exit 1; }
  key="${MINISIGN_SECRET_KEY_FILE:-$HOME/.minisign/minisign.key}"
  [ -f "$key" ] || { echo "minisign の秘密鍵がありません: $key（MINISIGN_SECRET_KEY_FILE で渡す）" >&2; exit 1; }
  pub=$(pub_file)
  [ -n "$comment" ] || comment="looptrack SHA256SUMS $(basename "$(cd "$out" && pwd)")"
  rm -f "$out/SHA256SUMS.minisig"
  # パスワードは minisign が問う（端末が無ければ標準入力の 1 行を読む）。-t は署名に含まれる trusted comment
  minisign -S -s "$key" -m "$out/SHA256SUMS" -x "$out/SHA256SUMS.minisig" -t "$comment"
  minisign -V -p "$pub" -m "$out/SHA256SUMS" -x "$out/SHA256SUMS.minisig"
}

verify() {
  local out="$1" pub
  [ -f "$out/SHA256SUMS" ] || { echo "$out/SHA256SUMS がありません" >&2; exit 1; }
  (
    cd "$out"
    if command -v sha256sum >/dev/null 2>&1; then
      sha256sum -c SHA256SUMS
    else
      shasum -a 256 -c SHA256SUMS
    fi
  )
  if [ -f "$out/SHA256SUMS.minisig" ]; then
    command -v minisign >/dev/null 2>&1 || { echo "SHA256SUMS.minisig がありますが minisign がありません" >&2; exit 1; }
    pub=$(pub_file)
    minisign -V -p "$pub" -m "$out/SHA256SUMS" -x "$out/SHA256SUMS.minisig"
  else
    echo "注意: SHA256SUMS.minisig がありません（署名していない）" >&2
  fi
}

image_context() {
  local version="$1" arch="$2" out="$3" ctx="$4" bin
  check_version "$version"
  bin="$out/looptrack_${version}_linux_${arch}"
  if [ ! -f "$bin" ]; then
    echo "$bin がありません（先に build を流す）" >&2
    exit 1
  fi
  mkdir -p "$ctx"
  cp "$bin" "$ctx/looptrack"
  chmod 0755 "$ctx/looptrack"
  cp deploy/Dockerfile "$ctx/Dockerfile"
  cp "$NOTICE_SRC" "$ctx/NOTICE"
}

case "${1:-}" in
  build) [ $# -eq 3 ] || usage; build "$2" "$3" ;;
  sums) [ $# -eq 2 ] || usage; sums "$2" ;;
  sign-sums) [ $# -eq 2 ] || [ $# -eq 3 ] || usage; sign_sums "$2" "${3:-}" ;;
  verify) [ $# -eq 2 ] || usage; verify "$2" ;;
  image-context) [ $# -eq 5 ] || usage; image_context "$2" "$3" "$4" "$5" ;;
  *) usage ;;
esac
