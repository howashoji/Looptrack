#!/bin/bash
# デスクトップ版の配布物を組み立てる（DESIGN.md §5-14）。release.yml の desktop-* ジョブと手元で同じものを使う。
# 手順の全体は docs/server/RELEASE.md「デスクトップ版」。
#
#   bash deploy/release/desktop.sh macos-app <版> <出力先>
#       → <出力先>/Looptrack.app（universal: arm64 と amd64 を cgo でビルドして lipo。macOS で実行する。署名はしない）
#         署名・公証は sign-macos.sh に .app を渡し（staple まで）、その後に macos-dmg で dmg を作って、dmg をもう一度渡す
#   bash deploy/release/desktop.sh macos-dmg <版> <.app> <出力先>
#       → <出力先>/Looptrack_<版>_macos_universal.dmg（.app と /Applications へのリンク。hdiutil）
#   bash deploy/release/desktop.sh appimage <版> <amd64|arm64> <出力先>
#       → <出力先>/Looptrack_<版>_linux_<x86_64|aarch64>.AppImage（cgo なしのビルド・AppDir・type2-runtime + squashfs）
#         要るもの: mksquashfs（squashfs-tools）・curl・sha256sum か shasum。runtime は下の版と SHA-256 で固定して取る
#         runtime（MIT）のライセンス文も同じ版から取り、AppImage の usr/share/doc/looptrack/ に入れる
#         runtime に静的リンクされた部品（libfuse は LGPL-2.1）のライセンス文の全文・マニフェスト・作り直しの手順は
#         usr/share/doc/looptrack/licenses/ に入れる（deploy/release/licenses/。DESIGN.md §5-14「AppImage の runtime の部品」）
#   bash deploy/release/desktop.sh windows-zip <版> <amd64|arm64> <出力先>
#       → <出力先>/Looptrack_<版>_windows_<arch>.zip（Looptrack\Looptrack.exe＝GUI・アイコンつき、Looptrack\cli\looptrack.exe＝CLI）
#         アイコンの埋め込みは rsrc（go run で版を固定して取る。MIT）。zip が要る
#   bash deploy/release/desktop.sh windows-installer <版> <amd64|arm64> <zip か展開したフォルダ> <出力先>
#       → <出力先>/Looptrack_<版>_windows_<arch>_setup.exe（上の zip の中身をそのまま入れる）
#         **Windows でだけ動く**（Inno Setup 7 の ISCC が要る。無ければ ISCC=<パス> で渡す）
#         zip を渡すときは unzip が要る（Git Bash に無ければ、先に展開してフォルダを渡す）
#         インストーラの設定は deploy/release/windows/Looptrack.iss
#
# ビルドの形は dist.sh と同じ（-trimpath・-s -w・-X main.version=<版>・RELEASE_MINISIGN_PUBKEY があれば -X で公開鍵）。
# desktop ビルドは -tags desktop（トレイつき）。cgo が要るのは macOS だけ（fyne.io/systray の Cocoa）。
# Bundle ID は internal/client/desktop/app.go の BundleID（仮の値。公開の準備で確定する）から読む（ここには書かない）。
# アイコンは internal/client/desktop/icon/ のファイル（差し替えの場所。internal/client/desktop/icon/icon.go）。
set -euo pipefail
cd "$(dirname "$0")/../.."

APP_NAME="Looptrack"
ICON_DIR="internal/client/desktop/icon"
OFL_SRC="internal/client/report/pdf/fonts/OFL.txt"
# 第三者のライセンス文（go run ./internal/tools/notice が作ってコミットしたもの）
NOTICE_SRC="NOTICE"
PUBKEY_VAR="github.com/howashoji/looptrack/internal/client/selfupdate.MinisignPublicKey"
# AppImage の runtime（https://github.com/AppImage/type2-runtime の版 20251108）。上流が作った静的な実行ファイルで、
# libfuse3 3.15.0（LGPL-2.1。上流が patches/libfuse/mount.c.diff で改変）・musl libc・squashfuse・zstd・zlib・mimalloc を
# 静的リンクしている（libfuse2 は使わない。fusermount3 は同梱せず利用者の OS のものを実行する）。
# 版を上げるときは、Releases の各ファイルの SHA-256 と、下のライセンス文（tag の LICENSE）に加えて、
# 部品の一覧（APPIMAGE_COMPONENTS。版・ソース・全文の写し）も取り直す。手順は docs/server/RELEASE.md「2-2. NOTICE」。
# APPIMAGE_RUNTIME_TAG は go run ./internal/tools/notice も読む（NOTICE の runtime の節の版。マニフェストの
# runtime.tag と違えば NOTICE の生成が失敗する）。
APPIMAGE_RUNTIME_TAG="20251108"
APPIMAGE_RUNTIME_SHA256_x86_64="2fca8b443c92510f1483a883f60061ad09b46b978b2631c807cd873a47ec260d"
APPIMAGE_RUNTIME_SHA256_aarch64="00cbdfcf917cc6c0ff6d3347d59e0ca1f7f45a6df1a428a0d6d8a78664d87444"
# runtime（MIT）のライセンス文。同じ tag から取り、SHA-256 で確かめる。取れないとき（ネットワークが無い環境）は
# リポジトリの写しを使う（同じ tag の中身。NOTICE の runtime の節もこの写しから作る）。
APPIMAGE_RUNTIME_LICENSE_SHA256="aa154fc9070614bbe7921f89db11efd1dba7a1f3a41685958110e2230f9c0ca1"
APPIMAGE_RUNTIME_LICENSE_SRC="deploy/release/licenses/AppImage-type2-runtime-LICENSE.txt"
APPIMAGE_RUNTIME_LICENSE_NAME="AppImage-type2-runtime-LICENSE.txt"
# runtime に静的リンクされた部品の一覧（正本。go run ./internal/tools/notice も同じものを読む）と、
# 作り直しの手順（LGPL-2.1 §6(a) の「作り直すために必要な情報」）。写しは同じディレクトリに置く。
APPIMAGE_LICENSES_DIR="deploy/release/licenses"
APPIMAGE_COMPONENTS="$APPIMAGE_LICENSES_DIR/runtime-components.json"
APPIMAGE_RELINKING="RELINKING.md"
# Windows のアイコンの埋め込み（.syso を作る）
RSRC="github.com/akavel/rsrc@v0.10.2"
# Windows のインストーラの設定（Inno Setup 7）
ISS="deploy/release/windows/Looptrack.iss"

usage() {
  sed -n '2,24p' "$0" >&2
  exit 2
}

die() {
  echo "desktop.sh: エラー: $*" >&2
  exit 1
}

# 作業ディレクトリ（終わったら消す）
CLEANUP=()
cleanup() {
  [ ${#CLEANUP[@]} -eq 0 ] || rm -rf "${CLEANUP[@]}"
}
trap cleanup EXIT
WORK_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/looptrack-desktop.XXXXXX")
CLEANUP+=("$WORK_ROOT")
mkwork() {
  mktemp -d "$WORK_ROOT/w.XXXXXX"
}

check_version() {
  [[ "$1" =~ ^[0-9A-Za-z][0-9A-Za-z._+-]*$ ]] || die "版の形が不正です: $1"
}

# ldflags <版> [追加] — dist.sh と同じ埋め込み
ldflags() {
  local f="-s -w -X main.version=$1"
  if [ -n "${RELEASE_MINISIGN_PUBKEY+x}" ]; then
    [[ "$RELEASE_MINISIGN_PUBKEY" =~ ^[A-Za-z0-9+/=]*$ ]] || die "RELEASE_MINISIGN_PUBKEY の形が不正です"
    f="$f -X $PUBKEY_VAR=$RELEASE_MINISIGN_PUBKEY"
  fi
  echo "$f${2:+ $2}"
}

bundle_id() {
  local id
  id=$(sed -n 's/^var BundleID = "\([^"]*\)"$/\1/p' internal/client/desktop/app.go)
  [[ "$id" =~ ^[A-Za-z0-9.-]+$ ]] || die "internal/client/desktop/app.go の BundleID を読めません"
  echo "$id"
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

# manifest_tag は部品の一覧が書いている runtime の版（runtime.tag）。
manifest_tag() {
  sed -n 's/.*"tag"[ 	]*:[ 	]*"\([^"]*\)".*/\1/p' "$APPIMAGE_COMPONENTS" | head -1
}

# manifest_license_files は一覧が挙げるライセンス文の写しを「ファイル名 SHA-256」で出す（現れた順）。
# license_files の各要素は "file" と "sha256" を同じ行に持つ。source_tarball の sha256 は file が無いので拾わない。
manifest_license_files() {
  awk '
    match($0, /"file"[ \t]*:[ \t]*"[^"]+"/) {
      s = substr($0, RSTART, RLENGTH); sub(/^"file"[ \t]*:[ \t]*"/, "", s); sub(/"$/, "", s); f = s
    }
    match($0, /"sha256"[ \t]*:[ \t]*"[0-9a-f]+"/) {
      s = substr($0, RSTART, RLENGTH); sub(/^"sha256"[ \t]*:[ \t]*"/, "", s); sub(/"$/, "", s)
      if (f != "") { print f, s; f = "" }
    }
  ' "$APPIMAGE_COMPONENTS"
}

# appimage_licenses は runtime に静的リンクされた部品のライセンス文の全文・一覧・作り直しの手順を
# AppImage の中（usr/share/doc/looptrack/licenses/）に入れる（LGPL-2.1 §6 の全文の同梱と再リンクの手段）。
appimage_licenses() {
  local licdir="$1" mtag n=0 lf lsum
  [ -f "$APPIMAGE_COMPONENTS" ] || die "部品の一覧がありません: $APPIMAGE_COMPONENTS"
  mtag=$(manifest_tag)
  [ "$mtag" = "$APPIMAGE_RUNTIME_TAG" ] ||
    die "$APPIMAGE_COMPONENTS の runtime.tag（$mtag）が APPIMAGE_RUNTIME_TAG（$APPIMAGE_RUNTIME_TAG）と違います。runtime の版を上げたら部品の一覧も取り直してください（docs/server/RELEASE.md「2-2. NOTICE」）"
  mkdir -p "$licdir"
  cp "$APPIMAGE_COMPONENTS" "$licdir/runtime-components.json"
  [ -f "$APPIMAGE_LICENSES_DIR/$APPIMAGE_RELINKING" ] || die "作り直しの手順がありません: $APPIMAGE_LICENSES_DIR/$APPIMAGE_RELINKING"
  cp "$APPIMAGE_LICENSES_DIR/$APPIMAGE_RELINKING" "$licdir/$APPIMAGE_RELINKING"
  while read -r lf lsum; do
    [ -n "$lf" ] || continue
    [ -f "$APPIMAGE_LICENSES_DIR/$lf" ] || die "ライセンス文の写しがありません: $APPIMAGE_LICENSES_DIR/$lf"
    [ "$(sha256_of "$APPIMAGE_LICENSES_DIR/$lf")" = "$lsum" ] ||
      die "$lf の SHA-256 が $APPIMAGE_COMPONENTS の記録と違います（取り直したなら sha256 も直す）"
    cp "$APPIMAGE_LICENSES_DIR/$lf" "$licdir/$lf"
    n=$((n + 1))
  done < <(manifest_license_files)
  [ "$n" -gt 0 ] || die "$APPIMAGE_COMPONENTS にライセンス文の写しが 1 つもありません"
  # LGPL の部品（libfuse）があるので、全文が入っていることを必ず確かめる
  [ -f "$licdir/LGPL-2.1.txt" ] || die "LGPL-2.1 の全文（LGPL-2.1.txt）が AppImage に入っていません"
  echo "licenses: 写し $n 本 + $APPIMAGE_RELINKING + runtime-components.json" >&2
}

# Info.plist の CFBundleShortVersionString は数字と点だけ（v1.2.3-rc.1 → 1.2.3。日付-コミットの試しの版は 0.0.0）
plist_version() {
  local v=${1#v}
  v=${v%%-*}
  v=${v%%+*}
  [[ "$v" =~ ^[0-9]+(\.[0-9]+){0,2}$ ]] || v="0.0.0"
  echo "$v"
}

# iss_version は Inno Setup の VersionInfoVersion（0〜65535 の数字を点で 2〜4 つつないだもの）。
# vX.Y.Z（と vX.Y.Z-rc.1 のようなプレリリース）は X.Y.Z、それ以外（日付-コミット ID の試しの版）は 0.0.0。
iss_version() {
  local v=${1#v} part
  v=${v%%-*}
  v=${v%%+*}
  [[ "$v" =~ ^[0-9]+(\.[0-9]+){1,3}$ ]] || {
    echo "0.0.0"
    return
  }
  # shellcheck disable=SC2086 # 点で分けるための意図した単語分割
  for part in ${v//./ }; do
    [ "$part" -le 65535 ] || {
      echo "0.0.0"
      return
    }
  done
  echo "$v"
}

macos_app() {
  local version="$1" out="$2" work app id pv
  check_version "$version"
  [ "$(uname -s)" = Darwin ] || die "macos-app は macOS で実行してください（cgo・lipo）"
  id=$(bundle_id)
  pv=$(plist_version "$version")
  work=$(mkwork)
  for arch in arm64 amd64; do
    echo "build darwin/${arch}（desktop・cgo）" >&2
    CGO_ENABLED=1 GOOS=darwin GOARCH="$arch" MACOSX_DEPLOYMENT_TARGET=13.0 \
      CGO_CFLAGS="-mmacosx-version-min=13.0" CGO_LDFLAGS="-mmacosx-version-min=13.0" \
      go build -trimpath -tags desktop -ldflags "$(ldflags "$version")" -o "$work/looptrack_$arch" ./cmd/looptrack
  done
  mkdir -p "$out"
  app="$out/$APP_NAME.app"
  rm -rf "$app"
  mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
  lipo -create -output "$app/Contents/MacOS/looptrack" "$work/looptrack_arm64" "$work/looptrack_amd64"
  lipo -archs "$app/Contents/MacOS/looptrack" >&2
  cp "$ICON_DIR/app.icns" "$app/Contents/Resources/$APP_NAME.icns"
  cp "$OFL_SRC" "$app/Contents/Resources/OFL-BIZUDGothic.txt"
  cp "$NOTICE_SRC" "$app/Contents/Resources/NOTICE"
  cat >"$app/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleName</key>
	<string>$APP_NAME</string>
	<key>CFBundleDisplayName</key>
	<string>$APP_NAME</string>
	<key>CFBundleIdentifier</key>
	<string>$id</string>
	<key>CFBundleExecutable</key>
	<string>looptrack</string>
	<key>CFBundleIconFile</key>
	<string>$APP_NAME</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleShortVersionString</key>
	<string>$pv</string>
	<key>CFBundleVersion</key>
	<string>$pv</string>
	<key>CFBundleInfoDictionaryVersion</key>
	<string>6.0</string>
	<key>LSMinimumSystemVersion</key>
	<string>13.0</string>
	<key>LSUIElement</key>
	<true/>
	<key>NSHighResolutionCapable</key>
	<true/>
	<key>LSApplicationCategoryType</key>
	<string>public.app-category.developer-tools</string>
	<key>NSHumanReadableCopyright</key>
	<string>$APP_NAME $version</string>
</dict>
</plist>
EOF
  plutil -lint "$app/Contents/Info.plist" >&2
  echo "${app}（${id}・${pv}）" >&2
}

macos_dmg() {
  local version="$1" app="${2%/}" out="$3" stage dmg
  check_version "$version"
  [ "$(uname -s)" = Darwin ] || die "macos-dmg は macOS で実行してください（hdiutil）"
  [ -d "$app" ] || die ".app がありません: $app"
  mkdir -p "$out"
  dmg="$out/${APP_NAME}_${version}_macos_universal.dmg"
  stage=$(mkwork)
  # ditto は署名・staple の拡張属性を保つ
  ditto "$app" "$stage/$APP_NAME.app"
  ln -s /Applications "$stage/Applications"
  rm -f "$dmg"
  hdiutil create -volname "$APP_NAME" -srcfolder "$stage" -fs HFS+ -format UDZO -ov "$dmg" >&2
  echo "$dmg" >&2
}

appimage() {
  local version="$1" goarch="$2" out="$3" arch want work appdir rt rtlic file
  check_version "$version"
  case "$goarch" in
    amd64) arch=x86_64; want=$APPIMAGE_RUNTIME_SHA256_x86_64 ;;
    arm64) arch=aarch64; want=$APPIMAGE_RUNTIME_SHA256_aarch64 ;;
    *) die "arch は amd64 か arm64: $goarch" ;;
  esac
  command -v mksquashfs >/dev/null 2>&1 || die "mksquashfs がありません（apt-get install squashfs-tools）"
  work=$(mkwork)
  appdir="$work/$APP_NAME.AppDir"
  mkdir -p "$appdir/usr/bin" "$appdir/usr/share/applications" "$appdir/usr/share/icons/hicolor/256x256/apps" "$appdir/usr/share/doc/looptrack"
  echo "build linux/$goarch（desktop・cgo なし）" >&2
  CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" go build -trimpath -tags desktop \
    -ldflags "$(ldflags "$version")" -o "$appdir/usr/bin/looptrack" ./cmd/looptrack
  # AppRun は中の実行ファイルへのリンク（引数なし＝ダブルクリックはデスクトップ版。引数ありは CLI）
  ln -s usr/bin/looptrack "$appdir/AppRun"
  cp "$ICON_DIR/app_256.png" "$appdir/usr/share/icons/hicolor/256x256/apps/looptrack.png"
  cp "$ICON_DIR/app_256.png" "$appdir/looptrack.png"
  ln -s looptrack.png "$appdir/.DirIcon"
  cp "$OFL_SRC" "$appdir/usr/share/doc/looptrack/OFL-BIZUDGothic.txt"
  cp "$NOTICE_SRC" "$appdir/usr/share/doc/looptrack/NOTICE"
  cat >"$appdir/looptrack.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=$APP_NAME
Comment=Issue tracking for coding agents and people
Exec=looptrack
Icon=looptrack
Terminal=false
Categories=Development;ProjectManagement;
EOF
  cp "$appdir/looptrack.desktop" "$appdir/usr/share/applications/looptrack.desktop"
  if command -v desktop-file-validate >/dev/null 2>&1; then
    desktop-file-validate "$appdir/looptrack.desktop"
  fi
  # runtime は版と SHA-256 で固定する（違えば止める）
  rt="$work/runtime-$arch"
  curl -fsSL --retry 3 -o "$rt" "https://github.com/AppImage/type2-runtime/releases/download/$APPIMAGE_RUNTIME_TAG/runtime-$arch"
  [ "$(sha256_of "$rt")" = "$want" ] || die "runtime-$arch の SHA-256 が違います（$APPIMAGE_RUNTIME_TAG）"
  # runtime（MIT）のライセンス文も同じ版から取る。取れなければリポジトリの写しを使う（どちらも SHA-256 で確かめる）
  rtlic="$work/runtime-LICENSE"
  if ! curl -fsSL --retry 3 -o "$rtlic" \
    "https://raw.githubusercontent.com/AppImage/type2-runtime/$APPIMAGE_RUNTIME_TAG/LICENSE" 2>/dev/null; then
    echo "desktop.sh: runtime のライセンス文を取れないので、リポジトリの写しを使います（$APPIMAGE_RUNTIME_LICENSE_SRC）" >&2
    cp "$APPIMAGE_RUNTIME_LICENSE_SRC" "$rtlic"
  fi
  [ "$(sha256_of "$rtlic")" = "$APPIMAGE_RUNTIME_LICENSE_SHA256" ] ||
    die "runtime のライセンス文の SHA-256 が違います（$APPIMAGE_RUNTIME_TAG）。版を上げたなら $APPIMAGE_RUNTIME_LICENSE_SRC と APPIMAGE_RUNTIME_LICENSE_SHA256 を取り直し、go run ./internal/tools/notice で NOTICE を作り直してください"
  cp "$rtlic" "$appdir/usr/share/doc/looptrack/$APPIMAGE_RUNTIME_LICENSE_NAME"
  # runtime に静的リンクされた部品（libfuse は LGPL-2.1）の全文・一覧・作り直しの手順
  appimage_licenses "$appdir/usr/share/doc/looptrack/licenses"
  mksquashfs "$appdir" "$work/app.squashfs" -root-owned -noappend -comp gzip -quiet >&2
  mkdir -p "$out"
  file="$out/${APP_NAME}_${version}_linux_${arch}.AppImage"
  cat "$rt" "$work/app.squashfs" >"$file"
  chmod 0755 "$file"
  echo "$file" >&2
}

windows_zip() {
  local version="$1" goarch="$2" out="$3" work top syso zipf
  check_version "$version"
  case "$goarch" in amd64 | arm64) ;; *) die "arch は amd64 か arm64: $goarch" ;; esac
  command -v zip >/dev/null 2>&1 || die "zip がありません"
  work=$(mkwork)
  syso="cmd/looptrack/rsrc_windows_$goarch.syso"
  CLEANUP+=("$syso")
  top="$work/$APP_NAME"
  mkdir -p "$top/cli"
  # アイコンを .exe に埋め込む（.syso はこのビルドの間だけ置く。headless の dist.sh のビルドには入れない）
  go run "$RSRC" -arch "$goarch" -ico "$ICON_DIR/app.ico" -o "$syso"
  echo "build windows/$goarch（desktop・GUI）" >&2
  CGO_ENABLED=0 GOOS=windows GOARCH="$goarch" go build -trimpath -tags desktop \
    -ldflags "$(ldflags "$version" "-H=windowsgui")" -o "$top/$APP_NAME.exe" ./cmd/looptrack
  echo "build windows/$goarch（CLI・headless）" >&2
  CGO_ENABLED=0 GOOS=windows GOARCH="$goarch" go build -trimpath \
    -ldflags "$(ldflags "$version")" -o "$top/cli/looptrack.exe" ./cmd/looptrack
  rm -f "$syso"
  cp "$OFL_SRC" "$top/OFL-BIZUDGothic.txt"
  cp "$NOTICE_SRC" "$top/NOTICE"
  mkdir -p "$out"
  zipf="$(cd "$out" && pwd)/${APP_NAME}_${version}_windows_${goarch}.zip"
  rm -f "$zipf"
  (cd "$work" && zip -qr -X "$zipf" "$APP_NAME")
  echo "$zipf" >&2
}

# winpath は Windows のプログラムに渡すパス（Git Bash / MSYS の /c/… を C:\… にする）。
winpath() {
  if command -v cygpath >/dev/null 2>&1; then
    cygpath -w "$1"
  else
    echo "$1"
  fi
}

# windows_installer は windows-zip で作った zip の中身を、管理者権限の要らないインストーラにする。
# ISCC（Inno Setup 7）は Windows でしか動かないので、ここでは「取って来て入れる」ことはせず、PATH か ISCC= で渡す。
windows_installer() {
  local version="$1" goarch="$2" from="$3" out="$4" work src iscc vi base outdir setup
  check_version "$version"
  case "$goarch" in amd64 | arm64) ;; *) die "arch は amd64 か arm64: $goarch" ;; esac
  iscc=${ISCC:-}
  if [ -z "$iscc" ]; then
    for c in ISCC iscc ISCC.exe; do
      if command -v "$c" >/dev/null 2>&1; then
        iscc=$c
        break
      fi
    done
  fi
  [ -n "$iscc" ] || die "ISCC（Inno Setup 7 のコンパイラ）がありません。Windows で実行し、場所は ISCC=<パス> で渡してください"
  if [ -d "$from" ]; then
    # 展開したフォルダ（Windows の Expand-Archive など。中に Looptrack\ があればその中）
    src="$from"
    [ -d "$src/$APP_NAME" ] && src="$src/$APP_NAME"
  else
    [ -f "$from" ] || die "zip も展開したフォルダもありません: $from"
    command -v unzip >/dev/null 2>&1 || die "unzip がありません（先に展開してフォルダを渡してください）"
    work=$(mkwork)
    unzip -q "$from" -d "$work"
    src="$work/$APP_NAME"
  fi
  for f in "$APP_NAME.exe" "cli/looptrack.exe" NOTICE OFL-BIZUDGothic.txt; do
    [ -f "$src/$f" ] || die "配布物に $APP_NAME/$f がありません: $from"
  done
  # ISCC は相対パスを .iss の場所から解決するので、絶対パスにしてから渡す
  src=$(cd "$src" && pwd)
  mkdir -p "$out"
  outdir=$(cd "$out" && pwd)
  vi=$(iss_version "$version")
  base="${APP_NAME}_${version}_windows_${goarch}_setup"
  setup="$outdir/$base.exe"
  rm -f "$setup"
  # MSYS の引数の自動変換を止める（/D… をパスとみなして壊すため）。パスは winpath で Windows の形にして渡す
  MSYS2_ARG_CONV_EXCL='*' MSYS_NO_PATHCONV=1 "$iscc" \
    "/DMyAppVersion=$version" \
    "/DMyVersionInfo=$vi" \
    "/DMyArch=$goarch" \
    "/DMySourceDir=$(winpath "$src")" \
    "/DMyOutputDir=$(winpath "$outdir")" \
    "/DMyOutputBase=$base" \
    "/DMyIconFile=$(winpath "$PWD/$ICON_DIR/app.ico")" \
    "$(winpath "$PWD/$ISS")" >&2
  [ -f "$setup" ] || die "インストーラができていません: $setup"
  echo "$setup" >&2
}

case "${1:-}" in
  macos-app) [ $# -eq 3 ] || usage; macos_app "$2" "$3" ;;
  macos-dmg) [ $# -eq 4 ] || usage; macos_dmg "$2" "$3" "$4" ;;
  appimage) [ $# -eq 4 ] || usage; appimage "$2" "$3" "$4" ;;
  windows-zip) [ $# -eq 4 ] || usage; windows_zip "$2" "$3" "$4" ;;
  windows-installer) [ $# -eq 5 ] || usage; windows_installer "$2" "$3" "$4" "$5" ;;
  *) usage ;;
esac
