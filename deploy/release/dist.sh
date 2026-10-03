#!/bin/bash
# リリース物（実行ファイル・SHA256SUMS・コンテナイメージの材料）を作る。
# CI（.github/workflows/ci.yml のクロスビルド）とリリース（.github/workflows/release.yml）と手元で同じものを使う。
# 手順の全体は docs/server/RELEASE.md。
#
#   bash deploy/release/dist.sh build <版> <出力先>
#       → <出力先>/<コマンド>_<版>_<os>_<arch>[.exe]（6 対象 × RELEASE_CMDS。素の実行ファイル）
#         looptrack を作るときは、第三者のライセンス文 <出力先>/NOTICE（依存モジュール・Go・フォント）と
#         埋め込んだフォント（BIZ UDGothic）のライセンス文 <出力先>/OFL-BIZUDGothic.txt も置く
#         インストーラ <出力先>/install.sh と、MySQL の最小権限 <出力先>/grants.sql も写す
#         （この出力はサーバの配布ディレクトリ（self-update の取得元）・インストーラの --from・コンテナイメージの材料・
#           macOS の署名の入力に使う。GitHub Releases には上げない。Releases に上げるのは下の archive の書庫）
#   bash deploy/release/dist.sh archive <版> <build の出力先> <書庫の置き場>
#       → <書庫の置き場>/looptrack_<版>_<os>_<arch>_server.tar.gz（linux・darwin）/ …_server.zip（windows）
#         中身は最上位のディレクトリ looptrack_<版>_<os>_<arch>_server/ の下に looptrack[.exe]・NOTICE・OFL-BIZUDGothic.txt・LICENSE だけ。
#         実行ファイルは build の出力先のもの（macOS は署名の後に作る。署名でバイト列が変わるため）
#   bash deploy/release/dist.sh check-archives <版> <書庫の置き場> <build の出力先>
#       → 6 つの書庫がそろい、中身が上の 4 つだけで（install.sh・grants.sql が無い）、NOTICE・OFL・LICENSE がリポジトリのものと、
#         実行ファイルが build の出力先のものとバイト列で一致することを確かめる
#   bash deploy/release/dist.sh check-release <版> <Releases に上げるディレクトリ>
#       → 上げるものを名前で確かめる: 書庫 6 つ・NOTICE・OFL-BIZUDGothic.txt がある／素の実行ファイル・install.sh・grants.sql が無い／
#         ほかはデスクトップ版（Looptrack_<版>_*）と SHA256SUMS・SHA256SUMS.minisig だけ／小文字にそろえても名前が重ならない／
#         SHA256SUMS があれば、載っているのは並んでいるファイルと書庫の中の実行ファイル（従来の名前）の行だけ
#   bash deploy/release/dist.sh sums <出力先>
#       → <出力先>/SHA256SUMS（署名で中身が変わるので、署名の後に作る。macOS は sign-macos.sh の後）
#         <出力先> に書庫（*_server.tar.gz・*_server.zip）があれば、書庫の中の実行ファイルを従来の名前
#         （looptrack_<版>_<os>_<arch>[.exe]）にした行も載せる。書庫から出して配布ディレクトリに置いた実行ファイルを、
#         公式の署名つきの SHA256SUMS のまま self-update が確かめられるようにするため
#   bash deploy/release/dist.sh sign-sums <出力先> [<trusted comment>]
#       → <出力先>/SHA256SUMS.minisig（minisign で署名し、公開鍵 deploy/release/minisign.pub で確かめる）
#         秘密鍵は MINISIGN_SECRET_KEY_FILE（既定 ~/.minisign/minisign.key）。パスワードは端末で問われる（CI は標準入力で渡す）
#   bash deploy/release/dist.sh verify <出力先>
#       → SHA256SUMS の照合と、SHA256SUMS.minisig があれば署名の確認（公開鍵は MINISIGN_PUB_FILE。既定 deploy/release/minisign.pub）
#         <出力先> に無い実行ファイルの行は、同じ名前の書庫（…_server.tar.gz・.zip）の中の実行ファイルで照合する
#   bash deploy/release/dist.sh image-context <版> <arch> <出力先> <作業ディレクトリ>
#       → <作業ディレクトリ>/Dockerfile と looptrack（<出力先> の linux/<arch> の実行ファイルを写す。サーバのイメージ）と NOTICE
#
# ビルドの形は deploy/build.sh と同じ（CGO_ENABLED=0・-trimpath・-s -w・-X main.version=<版>）。
# windows の実行ファイルには版の情報（VERSIONINFO。ProductName・ProductVersion）を入れ、作った直後に読み戻して確かめる
# （go run ./internal/tools/winversion。アイコンは入れない。linux・darwin のビルドは何も変えない）。
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
# 書庫に入れるライセンス
LICENSE_SRC="LICENSE"
# build の出力先に写すもの（配布ディレクトリ・--from の取得元として、実行ファイルと同じ場所に置く。Releases には上げない）
INSTALL_SRC="deploy/install.sh"
INSTALL_NAME="install.sh"
GRANTS_SRC="deploy/grants.sql"
GRANTS_NAME="grants.sql"
TARGETS="${RELEASE_TARGETS:-linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64}"
PUBKEY_VAR="github.com/howashoji/looptrack/internal/client/selfupdate.MinisignPublicKey"
# windows の exe に入れる版の情報の .syso を作り、できた exe から読み戻す道具
WINVERSION="./internal/tools/winversion"

# build の間だけ置く .syso（失敗しても消す）
SYSO=""
cleanup_syso() {
  [ -z "$SYSO" ] || rm -f "$SYSO"
}
trap cleanup_syso EXIT

usage() {
  sed -n '2,45p' "$0" >&2
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
      if [ "$os" = windows ]; then
        SYSO="cmd/$cmd/rsrc_windows_$arch.syso"
        go run "$WINVERSION" syso -version "$version" -arch "$arch" -original "$cmd.exe" -out "$SYSO"
      fi
      CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
        -ldflags "$ldflags" -o "$file" "./cmd/$cmd"
      if [ "$os" = windows ]; then
        rm -f "$SYSO"
        SYSO=""
        go run "$WINVERSION" check -version "$version" "$file" >&2
      fi
    done
    if [ "$cmd" = looptrack ]; then
      cp "$OFL_SRC" "$out/$OFL_NAME"
      cp "$NOTICE_SRC" "$out/NOTICE"
      # インストーラと MySQL の最小権限。取り出した配布物のディレクトリをそのまま --from に渡せるよう、
      # 実行ファイルと同じディレクトリに同じ名前で置く（SHA256SUMS に載るので、写した install.sh 自体を照合できる）
      cp "$INSTALL_SRC" "$out/$INSTALL_NAME"
      chmod 0755 "$out/$INSTALL_NAME"
      cp "$GRANTS_SRC" "$out/$GRANTS_NAME"
      chmod 0644 "$out/$GRANTS_NAME"
    fi
  done
}

# sha256_stdin は標準入力の SHA-256（16 進の小文字）を出す。
sha256_stdin() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum | cut -d' ' -f1
  else
    shasum -a 256 | cut -d' ' -f1
  fi
}

# server_base <版> <os> <arch> は書庫の最上位のディレクトリの名前（拡張子なしの書庫の名前）。
server_base() { echo "looptrack_${1}_${2}_${3}_server"; }

# archive_ext <os> は書庫の拡張子（windows は zip。ほかは tar.gz）。
archive_ext() { if [ "$1" = windows ]; then echo zip; else echo tar.gz; fi; }

# archive_inner <書庫> <中のパス> は書庫の中の 1 ファイルを標準出力に出す。
archive_inner() {
  case "$1" in
    *.zip) unzip -p "$1" "$2" ;;
    *) tar -xzOf "$1" "$2" ;;
  esac
}

# archive_list <書庫> は書庫の中のファイルの一覧（ディレクトリの項目を除く。名前順）。
archive_list() {
  case "$1" in
    *.zip) unzip -Z1 "$1" ;;
    *) tar -tzf "$1" ;;
  esac | grep -v '/$' | LC_ALL=C sort
}

archive() {
  local version="$1" in="$2" out="$3" target os arch ext exe base src work tarflags
  check_version "$version"
  command -v zip >/dev/null 2>&1 || { echo "zip がありません（windows の書庫に要る）" >&2; exit 1; }
  mkdir -p "$out"
  out=$(cd "$out" && pwd)
  work=$(mktemp -d "${TMPDIR:-/tmp}/looptrack-archive.XXXXXX")
  # 持ち主は root（0）にそろえる（作った人の名前を書庫に残さない）。macOS の tar（bsdtar）は拡張属性（._*）を入れない
  if tar --version 2>/dev/null | grep -q 'GNU tar'; then
    tarflags=(--owner=0 --group=0 --numeric-owner --sort=name)
  else
    tarflags=(--uid 0 --gid 0 --uname root --gname root --no-xattrs)
  fi
  for target in $TARGETS; do
    os="${target%/*}"
    arch="${target#*/}"
    exe=""
    [ "$os" = windows ] && exe=".exe"
    src="$in/looptrack_${version}_${os}_${arch}${exe}"
    [ -f "$src" ] || { rm -rf "$work"; echo "$src がありません（先に build を流す）" >&2; exit 1; }
    base=$(server_base "$version" "$os" "$arch")
    ext=$(archive_ext "$os")
    mkdir -p "$work/$base"
    cp "$src" "$work/$base/looptrack$exe"
    cp "$NOTICE_SRC" "$work/$base/NOTICE"
    cp "$OFL_SRC" "$work/$base/$OFL_NAME"
    cp "$LICENSE_SRC" "$work/$base/LICENSE"
    chmod 0755 "$work/$base" "$work/$base/looptrack$exe"
    chmod 0644 "$work/$base/NOTICE" "$work/$base/$OFL_NAME" "$work/$base/LICENSE"
    rm -f "$out/$base.$ext"
    if [ "$ext" = zip ]; then
      (cd "$work" && zip -qr -X "$out/$base.$ext" "$base")
    else
      COPYFILE_DISABLE=1 tar "${tarflags[@]}" -C "$work" -czf "$out/$base.$ext" "$base"
    fi
    echo "archive $out/$base.$ext" >&2
  done
  rm -rf "$work"
}

check_archives() {
  local version="$1" dir="$2" in="$3" target os arch exe base ext a want got f fails=0
  check_version "$version"
  for target in $TARGETS; do
    os="${target%/*}"
    arch="${target#*/}"
    exe=""
    [ "$os" = windows ] && exe=".exe"
    base=$(server_base "$version" "$os" "$arch")
    ext=$(archive_ext "$os")
    a="$dir/$base.$ext"
    if [ ! -f "$a" ]; then
      echo "書庫がありません: $a" >&2
      fails=$((fails + 1))
      continue
    fi
    # 中身は最上位のディレクトリ 1 つの下の 4 つだけ（install.sh・grants.sql は入れない）
    want=$(printf '%s\n' "$base/LICENSE" "$base/NOTICE" "$base/$OFL_NAME" "$base/looptrack$exe" | LC_ALL=C sort)
    got=$(archive_list "$a")
    if [ "$got" != "$want" ]; then
      echo "書庫の中身が違います: $(basename "$a")" >&2
      echo "  期待: $(echo "$want" | tr '\n' ' ')" >&2
      echo "  実際: $(echo "$got" | tr '\n' ' ')" >&2
      fails=$((fails + 1))
      continue
    fi
    for f in "NOTICE:$NOTICE_SRC" "$OFL_NAME:$OFL_SRC" "LICENSE:$LICENSE_SRC" "looptrack$exe:$in/looptrack_${version}_${os}_${arch}${exe}"; do
      if ! archive_inner "$a" "$base/${f%%:*}" | cmp -s - "${f#*:}"; then
        echo "書庫の $base/${f%%:*} が ${f#*:} と違います" >&2
        fails=$((fails + 1))
      fi
    done
    if [ "$os" != windows ] && [ "$ext" = tar.gz ]; then
      # 展開したら実行できること（実行権が書庫に入っている）
      if ! tar -tvzf "$a" "$base/looptrack" | grep -q '^-rwx'; then
        echo "書庫の $base/looptrack に実行権がありません" >&2
        fails=$((fails + 1))
      fi
    fi
    echo "ok $(basename "$a")" >&2
  done
  if [ "$fails" -gt 0 ]; then
    echo "書庫の確認: $fails 件の失敗" >&2
    exit 1
  fi
}

check_release() {
  local version="$1" dir="$2" target os arch exe f n lower dup fails=0 want_sums got_sums
  check_version "$version"
  [ -d "$dir" ] || { echo "$dir がありません" >&2; exit 1; }
  for target in $TARGETS; do
    os="${target%/*}"
    arch="${target#*/}"
    exe=""
    [ "$os" = windows ] && exe=".exe"
    f="$(server_base "$version" "$os" "$arch").$(archive_ext "$os")"
    [ -f "$dir/$f" ] || { echo "上げるものに書庫がありません: $f" >&2; fails=$((fails + 1)); }
    # 素の実行ファイルは上げない（書庫の中にだけある）
    f="looptrack_${version}_${os}_${arch}${exe}"
    [ ! -e "$dir/$f" ] || { echo "上げるものに素の実行ファイルがあります: $f" >&2; fails=$((fails + 1)); }
  done
  for f in "$NOTICE_SRC" "$OFL_NAME"; do
    [ -s "$dir/$(basename "$f")" ] || { echo "上げるものに $(basename "$f") がありません" >&2; fails=$((fails + 1)); }
  done
  for f in "$INSTALL_NAME" "$GRANTS_NAME"; do
    [ ! -e "$dir/$f" ] || { echo "上げるものに $f があります（Releases の資産から外した）" >&2; fails=$((fails + 1)); }
  done
  # ほかに並んでよいのはデスクトップ版と SHA256SUMS・その署名だけ
  for f in "$dir"/*; do
    n=$(basename "$f")
    case "$n" in
      looptrack_"${version}"_*_server.tar.gz | looptrack_"${version}"_*_server.zip | NOTICE | "$OFL_NAME" | SHA256SUMS | SHA256SUMS.minisig) ;;
      Looptrack_"${version}"_*) ;;
      *) echo "上げるものに想定していないファイルがあります: $n" >&2; fails=$((fails + 1)) ;;
    esac
  done
  # 大小の違いだけの名前の重なり（Windows・macOS では同じファイルになる）
  lower=$(cd "$dir" && find . -maxdepth 1 -type f | sed 's|^\./||' | tr '[:upper:]' '[:lower:]' | LC_ALL=C sort)
  dup=$(printf '%s\n' "$lower" | uniq -d)
  [ -z "$dup" ] || { echo "小文字にそろえると重なる名前があります: $dup" >&2; fails=$((fails + 1)); }
  if [ -f "$dir/SHA256SUMS" ]; then
    want_sums=$(
      cd "$dir"
      for f in *; do
        case "$f" in (SHA256SUMS | SHA256SUMS.minisig) continue ;; esac
        echo "$f"
        n=$(server_raw_name "$f")
        [ -z "$n" ] || echo "$n"
      done | LC_ALL=C sort
    )
    got_sums=$(awk '{ n = $2; sub(/^\*/, "", n); print n }' "$dir/SHA256SUMS" | LC_ALL=C sort)
    if [ "$want_sums" != "$got_sums" ]; then
      echo "SHA256SUMS の行が上げるものと合いません" >&2
      echo "  期待: $(echo "$want_sums" | tr '\n' ' ')" >&2
      echo "  実際: $(echo "$got_sums" | tr '\n' ' ')" >&2
      fails=$((fails + 1))
    fi
  fi
  if [ "$fails" -gt 0 ]; then
    echo "上げるものの確認: $fails 件の失敗" >&2
    exit 1
  fi
  echo "上げるもの: $(find "$dir" -maxdepth 1 -type f | wc -l | tr -d ' ') ファイル（${dir}）" >&2
}

# server_raw_name <書庫の名前> は書庫の中の実行ファイルの従来の名前（looptrack_<版>_<os>_<arch>[.exe]）。書庫でなければ空。
server_raw_name() {
  case "$1" in
    looptrack_*_windows_*_server.zip) echo "${1%_server.zip}.exe" ;;
    looptrack_*_server.tar.gz) echo "${1%_server.tar.gz}" ;;
  esac
}

# server_inner_path <書庫の名前> は書庫の中の実行ファイルのパス。
server_inner_path() {
  case "$1" in
    *.zip) echo "${1%.zip}/looptrack.exe" ;;
    *) echo "${1%.tar.gz}/looptrack" ;;
  esac
}

sums() {
  local out="$1"
  (
    cd "$out"
    rm -f SHA256SUMS
    # 配るファイルすべて（書庫・実行ファイル・NOTICE・OFL・install.sh・grants.sql・デスクトップ版）。
    # SHA256SUMS 自身と署名のファイルは含めない。名前順で固定する。
    local files a raw
    files=$(find . -maxdepth 1 -type f ! -name 'SHA256SUMS*' ! -name '*.sig' ! -name '*.pem' ! -name '*.bundle' | sed 's|^\./||' | LC_ALL=C sort)
    if [ -z "$files" ]; then
      echo "$out に実行ファイルがありません" >&2
      exit 1
    fi
    {
      if command -v sha256sum >/dev/null 2>&1; then
        # shellcheck disable=SC2086
        sha256sum $files
      else
        # shellcheck disable=SC2086
        shasum -a 256 $files
      fi
      # 書庫の中の実行ファイル（従来の名前の行。同じ名前のファイルが並んでいれば、そのファイル自身の行があるので足さない）
      for a in $files; do
        raw=$(server_raw_name "$a")
        [ -n "$raw" ] || continue
        [ -e "$raw" ] && continue
        printf '%s  %s\n' "$(archive_inner "$a" "$(server_inner_path "$a")" | sha256_stdin)" "$raw"
      done
    } | LC_ALL=C sort -k 2 >SHA256SUMS.tmp
    mv SHA256SUMS.tmp SHA256SUMS
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
  [ -f "$key" ] || { echo "minisign の秘密鍵がありません: ${key}（MINISIGN_SECRET_KEY_FILE で渡す）" >&2; exit 1; }
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
    local hash name a found fails=0
    # 並んでいるファイルの行は sha256sum -c で照合する
    while read -r hash name; do
      name="${name#\*}"
      [ -e "$name" ] && printf '%s  %s\n' "$hash" "$name"
    done <SHA256SUMS >"${TMPDIR:-/tmp}/looptrack-verify.$$"
    if command -v sha256sum >/dev/null 2>&1; then
      sha256sum -c "${TMPDIR:-/tmp}/looptrack-verify.$$" || fails=1
    else
      shasum -a 256 -c "${TMPDIR:-/tmp}/looptrack-verify.$$" || fails=1
    fi
    rm -f "${TMPDIR:-/tmp}/looptrack-verify.$$"
    # 並んでいない行は、書庫の中の実行ファイルの行だけを認め、その書庫の中身で照合する
    while read -r hash name; do
      name="${name#\*}"
      [ -e "$name" ] && continue
      found=""
      for a in "${name%.exe}_server.tar.gz" "${name%.exe}_server.zip"; do
        if [ -f "$a" ] && [ "$(server_raw_name "$a")" = "$name" ]; then found="$a"; fi
      done
      if [ -z "$found" ]; then
        echo "$name: ありません（並んでいるファイルにも書庫の中にも無い）" >&2
        fails=1
      elif [ "$(archive_inner "$found" "$(server_inner_path "$found")" | sha256_stdin)" = "$hash" ]; then
        echo "$name: OK（$found の中）"
      else
        echo "$name: FAILED（$found の中）" >&2
        fails=1
      fi
    done <SHA256SUMS
    [ "$fails" = 0 ] || exit 1
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
  archive) [ $# -eq 4 ] || usage; archive "$2" "$3" "$4" ;;
  check-archives) [ $# -eq 4 ] || usage; check_archives "$2" "$3" "$4" ;;
  check-release) [ $# -eq 3 ] || usage; check_release "$2" "$3" ;;
  sums) [ $# -eq 2 ] || usage; sums "$2" ;;
  sign-sums) [ $# -eq 2 ] || [ $# -eq 3 ] || usage; sign_sums "$2" "${3:-}" ;;
  verify) [ $# -eq 2 ] || usage; verify "$2" ;;
  image-context) [ $# -eq 5 ] || usage; image_context "$2" "$3" "$4" "$5" ;;
  *) usage ;;
esac
