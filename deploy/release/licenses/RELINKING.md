# Rebuilding the AppImage runtime (LGPL-2.1) / AppImage の runtime を作り直す（LGPL-2.1）

**EN** — The Linux distribution of Looptrack is an AppImage: an ELF *runtime* followed by a squashfs
*payload*. The payload holds Looptrack itself (`usr/bin/looptrack`, licensed under the MIT license;
the Go binary links no LGPL code). The runtime is the prebuilt
[AppImage type2-runtime](https://github.com/AppImage/type2-runtime) (MIT), and it statically links
**libfuse 3.15.0, which is licensed under the GNU Lesser General Public License, version 2.1**, plus
musl libc, squashfuse, zstd, zlib and mimalloc. This document is the "information necessary to
recreate the work" that LGPL-2.1 §6(a) asks for: it tells you where every source is and how to
replace libfuse with your own modified version. The full text of the LGPL is in `LGPL-2.1.txt` next
to this file; the exact versions, source URLs and SHA-256 sums are in `runtime-components.json`.

**JA** — Looptrack の Linux の配布物は AppImage です。**ELF の runtime** の後ろに **squashfs の payload**
をつないだ形をしています。payload には Looptrack 本体（`usr/bin/looptrack`・MIT）が入っています。Go の実行ファイルは
LGPL のコードとリンクしていません。先頭の runtime は
[AppImage type2-runtime](https://github.com/AppImage/type2-runtime)（MIT）です。その中に
**libfuse 3.15.0（LGPL-2.1）**・musl libc・squashfuse・zstd・zlib・mimalloc が静的リンクされています。
この文書は LGPL-2.1 §6(a) がいう「著作物を作り直すために必要な情報」です。ソースの入手先と、
libfuse を自分で直したものに差し替えて runtime を作り直す手順を書いています。LGPL の全文は
同じディレクトリの `LGPL-2.1.txt` に、各部品の版・ソースの URL・SHA-256 は `runtime-components.json` にあります。

## 1. Getting the sources / ソースを取る

**EN** — Every source tarball is listed in [`runtime-components.json`](runtime-components.json) with
its upstream URL and SHA-256. The same seven tarballs (the runtime plus its six components, about
11 MB in total) are kept together, with a `SHA256SUMS`, in the one-off release named in that file
under `runtime.corresponding_source.url`, so that they stay available even if an upstream URL moves.
They are deliberately *not* attached to every Looptrack release — the same archive applies to every
release that ships the same runtime tag.

**JA** — ソースの tarball は [`runtime-components.json`](runtime-components.json) に URL と SHA-256 が
並んでいます。同じ 7 本（runtime とその 6 部品で、合わせて約 11MB）を `SHA256SUMS` つきでまとめた置き場もあります。
場所はその JSON の `runtime.corresponding_source.url` です。上流の URL が動いても取れるようにするためです。
毎回のリリースには添付しません。同じ runtime を使うリリースには同じ資料が当てはまるからです。

```
# upstream, with the pinned checksums / 上流から取り、SHA-256 を照合する
curl -fL -O https://github.com/libfuse/libfuse/releases/download/fuse-3.15.0/fuse-3.15.0.tar.xz
sha256sum -c <<<"70589cfd5e1cff7ccd6ac91c86c01be340b227285c5e200baa284e401eea2ca0  fuse-3.15.0.tar.xz"
```

## 2. Taking the payload out of the AppImage / AppImage から payload を取り出す

**EN** — The runtime prints the offset at which the squashfs payload begins:

**JA** — squashfs の payload が始まる位置は runtime が教えてくれます。

```
chmod +x Looptrack_<version>_linux_x86_64.AppImage
offset=$(./Looptrack_<version>_linux_x86_64.AppImage --appimage-offset)
tail -c +$((offset + 1)) Looptrack_<version>_linux_x86_64.AppImage > payload.squashfs
```

**EN** — `--appimage-offset` needs no FUSE. If you would rather work with the files, unpack them with
`unsquashfs -o "$offset" Looptrack_<version>_linux_x86_64.AppImage` (or run the AppImage with
`--appimage-extract`) and repack later with
`mksquashfs squashfs-root payload.squashfs -root-owned -noappend -comp gzip` — the same options
`deploy/release/desktop.sh` uses.

**JA** — `--appimage-offset` は FUSE が無くても動きます。中身をファイルとして触りたいときは
`unsquashfs -o "$offset" Looptrack_<版>_linux_x86_64.AppImage` で展開してください。AppImage を `--appimage-extract` で実行しても展開できます。
あとで `mksquashfs squashfs-root payload.squashfs -root-owned -noappend -comp gzip` で詰め直します。
これは `deploy/release/desktop.sh` と同じ指定です。

## 3. Building a runtime with your own libfuse / 自分の libfuse で runtime を作る

**EN** — The runtime is built exactly as upstream documents it in
[`BUILD.md`](https://github.com/AppImage/type2-runtime/blob/20251108/BUILD.md) of the tag named in
`runtime-components.json` (`runtime.tag`). The short version:

1. Unpack the runtime source tarball and enter it.
2. Edit libfuse as you like. Upstream builds it from `scripts/common/install-dependencies.sh`, which
   downloads `fuse-3.15.0.tar.xz` and applies `patches/libfuse/mount.c.diff` (that patch makes the
   library take the path of `fusermount3` from `$FUSERMOUNT_PROG`). Point the script at your own
   source tree, or install your `libfuse3.a` into the build environment before the last step.
3. Build inside the same environment (Alpine Linux 3.21, clang, `-static -static-pie`):
   `bash scripts/docker/build-with-docker.sh`, or `sudo env ALPINE_ARCH=x86_64 scripts/chroot/chroot_build.sh`.
   `src/runtime/Makefile` links `-lsquashfuse -lsquashfuse_ll -lzstd -lz -lfuse3 -lmimalloc`.
4. The result is `out/runtime-x86_64` (or `-aarch64`).

**JA** — runtime は、`runtime-components.json` の `runtime.tag` の
[`BUILD.md`](https://github.com/AppImage/type2-runtime/blob/20251108/BUILD.md) のとおりに作ります。要点は次のとおりです。

1. runtime のソースの tarball を展開して中へ入ります。
2. libfuse を好きなように直します。上流は `scripts/common/install-dependencies.sh` で
   `fuse-3.15.0.tar.xz` を取り、`patches/libfuse/mount.c.diff` を当てています。これは `fusermount3` の場所を `$FUSERMOUNT_PROG` から
   読むようにする改変です。このスクリプトの取得先を自分のソースに差し替えてください。
   または最後の手順の前に、自分の `libfuse3.a` をビルド環境に入れておきます。
3. 同じ環境（Alpine Linux 3.21・clang・`-static -static-pie`）でビルドします。
   `bash scripts/docker/build-with-docker.sh` か `sudo env ALPINE_ARCH=x86_64 scripts/chroot/chroot_build.sh` を実行してください。
   `src/runtime/Makefile` が `-lsquashfuse -lsquashfuse_ll -lzstd -lz -lfuse3 -lmimalloc` をリンクします。
4. できあがるのは `out/runtime-x86_64`（または `-aarch64`）です。

## 4. Putting the AppImage back together / AppImage をつなぎ直す

```
cat out/runtime-x86_64 payload.squashfs > Looptrack-relinked.AppImage
chmod +x Looptrack-relinked.AppImage
./Looptrack-relinked.AppImage --appimage-offset   # sanity check / 動くかの確認
./Looptrack-relinked.AppImage version
```

**EN** — That is the whole build: `cat runtime payload > file`, then make it executable. Nothing in
the payload depends on the runtime, so the Looptrack binary inside it is untouched.

**JA** — 作り方はこれだけです。`cat runtime payload > ファイル` でつなぎ、実行できるようにします。
payload は runtime に依存しないので、中の Looptrack の実行ファイルはそのままです。

## 5. Things to know / 注意

**EN**

- **The checksums and the signature will no longer match.** A rebuilt AppImage is a different file,
  so it will not match the `SHA256SUMS` of the release, nor the minisign signature over it, and
  `looptrack self-update` will refuse it. That is expected: you are running your own build.
- **We ship the upstream prebuilt runtime**, pinned by tag and SHA-256 in `deploy/release/desktop.sh`
  (`APPIMAGE_RUNTIME_TAG`, `APPIMAGE_RUNTIME_SHA256_*`). We do not build it ourselves, so a rebuild
  of your own is not expected to be byte-for-byte identical to the one we ship.
- **`fusermount3` is not included.** The runtime executes the one installed on your system
  (`$FUSERMOUNT_PROG`), so its GPL-2.0 code is not part of this distribution.
- If you do not want FUSE at all, the AppImage can also be run with `--appimage-extract-and-run`, or
  you can use the plain `looptrack` binary from the same release instead.

**JA**

- **SHA256SUMS と署名は合わなくなります。** 作り直した AppImage は別のファイルです。リリースの
  `SHA256SUMS` にも minisign の署名にも合わず、`looptrack self-update` も受け付けません。
  自分のビルドを動かしているのだから、それで正しい動きです。
- **配っている runtime は上流がビルドしたバイナリそのもの**です。`deploy/release/desktop.sh` の `APPIMAGE_RUNTIME_TAG` と
  `APPIMAGE_RUNTIME_SHA256_*` で版と SHA-256 を固定しています。こちらではビルドしていないので、
  自分で作り直したものが 1 バイトまで同じになるとは限りません。
- **`fusermount3` は同梱していません。** runtime は利用者の OS に入っているもの（`$FUSERMOUNT_PROG`）を実行します。
  そのため、その GPL-2.0 のコードはこの配布物には入りません。
- FUSE を使いたくないときは、AppImage を `--appimage-extract-and-run` で実行してください。同じリリースの
  素の `looptrack` の実行ファイルを使う手もあります。
