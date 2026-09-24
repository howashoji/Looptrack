# CI とリリースの手順

GitHub Actions の CI（`.github/workflows/ci.yml`）とリリース物の作成（`.github/workflows/release.yml`）、版の付け方と公開の手順をまとめます。
実行ファイルの作り方は `deploy/release/dist.sh` 1 つに寄せてあり、CI・リリース・手元で同じものを使います。

サーバの配置（`looptrack setup`・`deploy/install.sh`）は [DEPLOY.md](DEPLOY.md) にあります。この文書の手順とは独立しています。

## 1. CI（`ci.yml`）

**CI は手動（`workflow_dispatch`）と週次（月曜の朝の `schedule`）でだけ走ります。** push や pull request では走りません（`on:` に `push` / `pull_request` を置いていません）。
手動で実行するのは次の 2 つの機会です。

- **本番の配置・配布物の更新・リリースのタグの直前**
- **Windows に関わる変更をまとめた区切り**（1 日 0〜2 回）

実行する SHA は、手元の全検査（CONTRIBUTING.md の「Running the tests」）が緑のものだけにしてください。
入力 `full` は既定で入っていて、重いジョブも走ります。軽いぶんだけなら `gh workflow run ci.yml -f full=false` を使います。

| ジョブ | いつ | 中身 |
| -- | -- | -- |
| `go-lint` | 毎回 | `gofmt -l`（差分があれば失敗）・`go vet ./...`・`go mod tidy -diff`・`govulncheck ./...` |
| `go-test-linux` | 毎回 | MySQL 8.4 の service コンテナに `LOOPTRACK_TEST_DSN` を向けて `go test -count=1 -v ./...`。**DB を使うテストが省略されたら失敗**にする（省略の文言 `LOOPTRACK_TEST_DSN が未設定` を探す）。失敗時は全文を artifact `go-test-log` に残す。Go 以外の処理系は入れない |
| `go-test-windows` | 毎回 | Windows で `go test -count=1 ./...`（DB なし。DB のテストは SQLite で走り、省略しない）。落ちたら全体を赤にする（2026-09-19 に continue-on-error を外した）。一時ディレクトリは checkout と同じドライブに置く |
| `go-test-macos` | 週次・手動の `full` | macOS で `go test -count=1 ./...`（DB なし）。重いので毎回は回さない |
| `desktop-build` | 毎回 | デスクトップ版（`-tags desktop`）の linux/amd64 のビルドと vet、`internal/client/desktop/...` のテスト（通常のビルドに入らないコードの回帰を拾う） |
| `desktop-cross` | 週次・手動の `full` | デスクトップ版を linux / windows × amd64 / arm64 でビルドと vet（cgo なし） |
| `desktop-macos` | 週次・手動の `full` | デスクトップ版を macOS の runner で arm64・amd64 ともビルド（cgo。`fyne.io/systray` の Cocoa） |
| `cross-build` | 週次・手動の `full` | `dist.sh build` で 6 対象（linux / darwin / windows × amd64 / arm64）を作り、`SHA256SUMS` を作り、linux/amd64 の `version` が埋め込んだ版を返すことを確かめる |
| `checks` | 毎回 | 1 分に満たない検査をまとめた 1 ジョブ: `shellcheck -S error`（`deploy/` の .sh）・`deploy/public-scan.sh`・`node --test render_test.mjs`・actionlint（workflow 自体の lint）・NOTICE を作り直して差分が無いこと（下の「2-2. NOTICE」）。起動の手間を 1 回で済ませるため、分けずに 1 つにする |

環境変数の方針: テストには `IM_*` を渡しません。過去に手元のテストが本番へ書き込んだことがあるからです。
Go には `LOOPTRACK_TEST_DSN` だけを渡し、各テストジョブの最初に「`LOOPTRACK_TEST_DSN` 以外の `IM_*` が無いこと」を確かめます。
CI には運用中のサーバの URL や秘密を置きません。

Go の版: ci.yml・release.yml の `GO_VERSION` で決めます。今は `1.27.x` で、手元と同じ系列の最新パッチです。go.mod の `go` 行は下限です。
上げるときは 2 つの workflow の `GO_VERSION` を同時に変えてください。Dependabot は Go 本体の版を上げません。


## 2. リリース物（`release.yml`）

| 物 | 中身 |
| -- | -- |
| 実行ファイル | `<コマンド>_<版>_<os>_<arch>[.exe]`（6 対象。darwin は署名・公証済み。Windows は署名なし）。`CGO_ENABLED=0`・`-trimpath`・`-s -w`・`-X main.version=<版>`（deploy/build.sh と同じ形）。対象のコマンドは `RELEASE_CMDS`（既定 `looptrack`。サーバは looptrack に統合した） |
| `NOTICE` | 第三者のライセンス文（依存モジュール・Go・BIZ UDGothic の OFL。リポジトリのルートの NOTICE の写し。下の「2-2. NOTICE」）。実行ファイルからも `looptrack licenses` で読める。デスクトップ版（.app の `Contents/Resources`・AppImage の `usr/share/doc/looptrack`・Windows の zip の `Looptrack\`・Windows のインストーラの `{app}\NOTICE.txt`）とコンテナイメージ（`/NOTICE`）にも入る |
| `OFL-BIZUDGothic.txt` | looptrack に埋め込んだ日本語フォント（BIZ UDGothic・SIL OFL 1.1）のライセンス文（`internal/client/report/pdf/fonts/OFL.txt` の写し）。OFL はフォントを配るときにライセンス文を添えることを求める（NOTICE にも入っている） |
| `install.sh` | まっさらな Linux サーバに入れるインストーラ（`deploy/install.sh` の写し）。**「1 行で取得して実行する」入口**なので、名前を変えない（下の「2-3. install.sh と grants.sql」）。Releases から取った後に `SHA256SUMS` で照合できる |
| `grants.sql` | MySQL のアプリ用の利用者に与える最小権限（`deploy/grants.sql` の写し）。install.sh が「setup（表を作る）→ grants.sql → 起動」の順番を案内するときに使う。DEPLOY.md「保存先に MySQL を選ぶとき」 |
| `SHA256SUMS` | 上の実行ファイル（と NOTICE・OFL-BIZUDGothic.txt・install.sh・grants.sql・デスクトップ版）の SHA-256（`sha256sum -c SHA256SUMS` で照合できる形）。**署名の後に作る** |
| `SHA256SUMS.minisig` | SHA256SUMS の minisign の署名（`RELEASE_SIGN` のとき。公開では必須）。下の「署名」 |
| コンテナイメージ | `deploy/Dockerfile`（scratch）に、同じリリースの linux/amd64・arm64 の `looptrack` を置いたもの（ENTRYPOINT `/looptrack`・CMD `serve`）（deploy/build.sh と同じ内容。中の実行ファイルは SHA256SUMS のものと同じバイト列）。名前は `ghcr.io/<owner>/looptrack:<版>`（arch 別に `<版>-amd64` / `<版>-arm64`、正式版は `latest` も） |

起動と公開の条件は次のとおりです。

| 起動 | 作るもの | 公開（GHCR への push・Releases の下書き） |
| -- | -- | -- |
| タグ `v*` の push | 実行ファイル・SHA256SUMS・イメージ（tar）を workflow の artifact に置く | リポジトリ変数 `RELEASE_PUBLISH` が `true` のときだけ |
| 手動・`version` 空 | 「日付-コミット ID」で試しに作る | しない |
| 手動・`version` 指定 | そのタグの内容から作る | `publish` にチェックしたとき |

公開する版は `vX.Y.Z` / `vX.Y.Z-<pre>` の形に限ります。それ以外は meta ジョブで止まります。
`-` を含む版はプレリリースとして扱い、`latest` を付けません。Releases でもプレリリースになります。
Releases は**下書き**で作ります。中身を確かめてから人が公開します。

### 署名

決定（2026-09-19）: 公式の配布物のうち **darwin の実行ファイルは HOWA SHOJI K.K. の Developer ID Application（CI 用の証明書）で署名し、Apple の公証を通します**。
**Windows は当面署名しません**（公開後は SignPath Foundation）。
**SHA256SUMS は署名の後の最終ファイルから作り、minisign で署名します**（`SHA256SUMS.minisig`）。
フォークして自分でビルドしたものには署名が付きません。Developer ID と minisign の秘密鍵は HOWA SHOJI K.K. だけが持っています。

| ジョブ | 中身 |
| -- | -- |
| `binaries`（ubuntu） | `dist.sh build` で 6 対象を作り、署名前のファイルを artifact `build-<版>` に置く（SHA256SUMS はまだ作らない） |
| `sign-macos`（macos-15・Environment `release`） | Secrets の有無を確かめる（無ければ名前を示してエラー）→ `$RUNNER_TEMP` に一時キーチェーンを作って .p12 を入れる → `deploy/release/sign-macos.sh` で darwin の 2 つ（looptrack × amd64・arm64）に署名（hardened runtime・安全なタイムスタンプ・TeamIdentifier の確認）→ まとめて zip で `notarytool submit --wait`（API キー）→ 署名後も `version` が動くことを確かめる → artifact `signed-macos-<版>`。最後に `if: always()` で一時キーチェーン・.p12・.p8 を消す |
| `sums`（ubuntu） | `build-<版>` に署名済みの darwin の 2 つを上書きし（数と名前を確かめる）、`dist.sh sums` で SHA256SUMS を作る → artifact `binaries-<版>`（最終ファイル） |
| `sign-sums`（ubuntu・Environment `release`） | `apt-get install minisign` → 秘密鍵を `$RUNNER_TEMP` に書き、パスワードを標準入力で渡して `dist.sh sign-sums` → `dist.sh verify`（リポジトリの公開鍵 `deploy/release/minisign.pub` で確かめる）→ artifact `sums-signature-<版>`（`SHA256SUMS.minisig`） |
| `image` | `binaries-<版>` の linux の looptrack から作る（署名とは独立。中の実行ファイルは SHA256SUMS に載っているものと同じバイト列） |
| `publish`（Environment `release`） | `binaries-<版>` と `SHA256SUMS.minisig` を取り、`dist.sh verify` で照合と署名を確かめてから GHCR・Releases の下書き |

- リポジトリ変数 `RELEASE_SIGN` が `true` でないと、`sign-macos`・`sign-sums` は飛ばされます。SHA256SUMS は署名なしで作られます（private の間の試しのビルド）。
  **公開（publish）には署名が必須です。** `RELEASE_SIGN` が `true` でないと meta ジョブで止まります。looptrack の self-update が署名の無い配布からは更新しないためです。
- darwin の単体の実行ファイルと zip には staple できません。初回の起動時に Gatekeeper がオンラインで公証を確かめます。
  デスクトップ版の `.app` / dmg は、sign-macos ジョブが同じ `sign-macos.sh` に渡します。署名・公証の後に `stapler staple` まで行います（.app → dmg の順に 2 回呼ぶ。下の「デスクトップ版」）。
- 秘密の扱い: `set -x` は使わず、秘密を echo しません。復号したファイルは `$RUNNER_TEMP` に置いて最後に消します。
  Secrets は Environment `release`（タグ `v*` だけ・承認つき）に置くので、`ci.yml`（手動・週次）からは読めません。
  サードパーティの action を足すときは、コミット SHA で固定してください。

#### 公開鍵（minisign）

`deploy/release/minisign.pub` が Looptrack 専用の公開鍵です（鍵 ID `29D707D7EBFF246B`・秘密ではない）。
同じ値を次の 3 か所に埋め込んであり、`TestEmbeddedPublicKey` が一致を確かめます。

| 場所 | 使い方 |
| -- | -- |
| `internal/client/selfupdate.MinisignPublicKey` | `looptrack self-update` が SHA256SUMS と `.minisig` を取り、署名と、SHA256SUMS の中のハッシュが一覧と同じことを確かめる。署名が無い・合わなければ置き換えない |
| `deploy/install.sh`（`MINISIGN_PUBKEY_DEFAULT`） | `minisign` コマンドがあれば確かめる（無ければ注意して進む。`--require-signature` で必須）。DEPLOY.md「install.sh」 |
| `deploy/release/dist.sh verify` | 手元・CI で SHA256SUMS の照合と署名の確認 |

鍵を持たないビルド: `dist.sh build` に `RELEASE_MINISIGN_PUBKEY=`（空）を渡すと、`-X` で公開鍵を空にします。この場合 self-update は署名を確かめず、ハッシュだけを見ます。
これを使うのは**署名しない配布（自分のサーバの配布ディレクトリに置く配布・§2-1）だけ**です。普通の `go build` とリリースは鍵を持ちます。
秘密鍵（`looptrack-minisign.key`・パスワード付き）は、利用者の手元の可搬媒体 2 部に保管しています。
鍵を替えるときは 3 か所と `minisign.pub` を同時に直してください。古い looptrack が新しい署名を拒む期間があるので、その案内も出します。

#### 署名・公証に要るもの（自分で署名するとき）

公式の配布物は配布元が署名・公証します。鍵・証明書・Secrets は配布元が管理するので、ここには書きません。
フォークで署名するときは、自分の証明書と鍵で次を用意してください。
**Apple への送信と秘密鍵を使う操作は、利用者の承認を得てから行います。**

- 手元で署名・公証する: `bash deploy/release/sign-macos.sh --identity <証明書の SHA-1> --team-id <Team ID> --notary-profile <notarytool のプロファイル> <darwin の実行ファイル…>`
  同じ名前の Developer ID がキーチェーンに複数あると codesign が選べないので、SHA-1 で渡します。動作確認だけなら `--identity - --skip-notarize` を使います。
  続けて `dist.sh sums` → `MINISIGN_SECRET_KEY_FILE=<秘密鍵> dist.sh sign-sums` → `dist.sh verify` の順に実行します。
- CI（`release.yml`）で署名する: Environment `release`（Required reviewers・タグ `v*` だけ）に次を置きます。`gh secret set` では値を引数に書かないでください。

| 種類 | 名前 |
| -- | -- |
| Secrets | `MACOS_CERT_P12_BASE64`・`MACOS_CERT_P12_PASSWORD`・`APPLE_API_KEY_P8_BASE64`・`APPLE_API_KEY_ID`・`APPLE_API_ISSUER_ID`・`MINISIGN_SECRET_KEY`・`MINISIGN_PASSWORD` |
| 変数（Environment） | `APPLE_TEAM_ID`・`MACOS_SIGN_IDENTITY`（CI の一時キーチェーンには証明書が 1 つだけなので名前でよい） |
| 変数（リポジトリ） | `RELEASE_SIGN=true`（meta ジョブが読む） |

確かめるには次の 2 つを使います。

- `codesign -dvvv <ファイル>`: Authority が `Developer ID Application: …` → `Developer ID Certification Authority` → `Apple Root CA` と並び、`flags=0x10000(runtime)`・`Timestamp=…`・`TeamIdentifier=…` が出ること
- `spctl -a -vvv -t install <ファイル>`: `accepted` と `source=Notarized Developer ID` が出ること

**単体の実行ファイルには `stapler` が使えません。** `Stapler is incapable of working with Document files.` と出ます。staple できるのは .app・dmg・pkg です。
`codesign -vv --test-requirement="=notarized" <ファイル>` も併せて使うときは、**`spctl` の後に**実行してください。
公証の控えが手元に無い初回は失敗し、`spctl` が Gatekeeper にオンラインで確かめた後なら成功するからです。
.app と dmg は `stapler staple` → `stapler validate` で確かめます。
ブラウザで取得した（検疫の付いた）実行ファイルで、Gatekeeper の警告が出ないことも確かめてください。

### デスクトップ版（DESIGN.md §5-14）

desktop ビルド（`-tags desktop`・トレイつき）は `dist.sh` では作りません。`deploy/release/desktop.sh` で OS ごとの配布物に組み立てます。

| ジョブ（release.yml） | runner | 作るもの |
| -- | -- | -- |
| `desktop-macos` | macos-15 | `Looptrack.app`（arm64 と amd64 を `CGO_ENABLED=1` でビルドして `lipo`。Bundle ID は `internal/client/desktop/app.go` の `BundleID`＝仮）→ 起動の確認（`desktop_smoke.sh`）→ 署名なしの `Looptrack_<版>_macos_universal.dmg`。.app は zip にして sign-macos へ渡す |
| `desktop-linux` | ubuntu（amd64・arm64 の matrix） | `Looptrack_<版>_linux_<x86_64|aarch64>.AppImage`（cgo なし。squashfs-tools と、版と SHA-256 で固定した type2-runtime）。x86_64 は展開して `desktop_smoke.sh` |
| `desktop-windows` | ubuntu（amd64・arm64 の matrix） | `Looptrack_<版>_windows_<arch>.zip`（`Looptrack.exe`＝GUI・アイコンつき、`cli/looptrack.exe`＝CLI） |
| `desktop-windows-installer` | windows-2025（amd64）・windows-11-arm（arm64） | 上の zip を展開し、**まずその場から `Looptrack.exe` を起動して確かめる**（`desktop_smoke.ps1`。zip はインストーラを通さない配り方なので別に確かめる）。そのあと同じ中身を Inno Setup 7 でインストーラにする（`Looptrack_<版>_windows_<arch>_setup.exe`。下の「Windows のインストーラ」） |
| `sign-macos`（署名するとき） | macos-15 | 上の .app に署名・公証・staple → dmg を作り直して署名・公証・staple（`sign-macos.sh` を 2 回）。署名済みの dmg は `signed-macos-<版>` に入り、sums で署名なしの dmg を置き換える |
| `sums` | ubuntu | 実行ファイル・デスクトップ版（`desktop-*-<版>` の artifact）・署名済みのものを合わせ、デスクトップ版の 7 つがそろっていることを確かめてから SHA256SUMS を作る（署名の後に作る順は変えない） |

手元（macOS）で試すなら次のとおりです。署名は ad-hoc までです。本物の証明書・公証は、CI か上の「署名」の手順で行います。

```
bash deploy/release/desktop.sh macos-app v0.0.0-test out          # out/Looptrack.app
bash deploy/release/desktop_smoke.sh out/Looptrack.app/Contents/MacOS/looptrack   # 一時ディレクトリで起動・二重起動・--quit
bash deploy/release/sign-macos.sh --identity - --skip-notarize out/Looptrack.app
bash deploy/release/desktop.sh macos-dmg v0.0.0-test out/Looptrack.app out
```

`open out/Looptrack.app` で試すときは、利用者の本物の置き場を使わないようにデータを一時ディレクトリに向けてください。
`open --env LOOPTRACK_DATA_DIR=/tmp/lt-data --env HOME=/tmp/lt-home out/Looptrack.app` のように実行します。HOME も変えるのは、自動起動と CLI の置き場も試すためです。

#### Windows のインストーラ

`deploy/release/windows/Looptrack.iss`（Inno Setup 7）で作ります。決定と中身は DESIGN.md §5-14「Windows のインストーラ」にあります。
ISCC は Windows でしか動かないので、手元（macOS・Linux）では組み立てられません。**CI で確かめます。**

```
# Windows で（Inno Setup 7 を入れてから。PATH に無ければ ISCC=<ISCC.exe のパス>）
bash deploy/release/desktop.sh windows-zip v0.0.0-test amd64 out
bash deploy/release/desktop.sh windows-installer v0.0.0-test amd64 out/Looptrack_v0.0.0-test_windows_amd64.zip out
```

`.iss` の必須の設定は `go test ./internal/client/desktop/`（`installer_test.go`）が確かめます。
CI の smoke test は次の順に確かめます。

1. サイレント導入（`/TASKS=startup,cli`）
2. ファイル・HKCU の Uninstall キー・スタートメニューの `.lnk`・`looptrack.exe version`・Run の値
3. 起動中の上書き
4. アンインストール（データが残り、Run の値と CLI の写しが消える）

#### Windows の zip

zip はインストーラを通さず、**展開した場所からそのまま起動する**配り方です。展開先は利用者が自由に決めます。
インストーラ版（`%LOCALAPPDATA%\Programs\Looptrack Desktop`）とは場所が違うので、インストーラ版が通っても zip で動くとは限りません。
そこで同じジョブ（Windows の runner）で、インストーラを作る前に zip の中身を直接起動して確かめます。

```
# Windows で（手元で試すとき）
pwsh -File deploy/release/desktop_smoke.ps1 -Exe <展開先>\Looptrack\Looptrack.exe
```

確かめることは macOS・Linux の `desktop_smoke.sh` と同じです。

1. `desktop --no-tray --background` で起動する
2. `--status` で URL が分かる
3. `healthz` が ok を返す
4. 画面が初回設定へ 303 で転送する
5. 2 つ目の起動が二重に立たない
6. `--quit` で穏やかに（終了コード 0 で）止まり、`--status` が 1 になる
7. データの置き場に DB・鍵・設定・ログができている

データは一時ディレクトリに向ける（`LOOPTRACK_DATA_DIR`）ので、runner の本物の置き場は使いません。
この確認が失敗するとそのジョブが止まり、リリースは進みません。

| 変えるもの | 場所 |
| -- | -- |
| アイコン | `internal/client/desktop/icon/`（同じ名前・形式のファイルを置く。今のものは仮で `go run ./internal/client/desktop/icon/gen` が作る） |
| Bundle ID・表示名 | `internal/client/desktop/app.go` の `BundleID`・`paths.go` の `AppName`（公開の準備で確定する） |
| AppImage の runtime | `desktop.sh` の `APPIMAGE_RUNTIME_TAG` と SHA-256（type2-runtime の Releases のファイルの SHA-256 を確かめて直す）。**部品の一覧（`deploy/release/licenses/runtime-components.json`）も一緒に取り直す**。手順は下の「2-2. NOTICE」→「AppImage の runtime の版を上げるとき」 |
| Windows のアイコンの埋め込みの道具 | `desktop.sh` の `RSRC`（`go run` で版を固定） |
| Inno Setup の版 | `release.yml` の `desktop-windows-installer` の `INNO_URL`・`INNO_SHA256`（GitHub Releases のファイルの SHA-256 を確かめて直す） |
| インストーラの `AppId` | **変えない**（`Looptrack.iss` と `release.yml` の `APP_ID`。変えると更新が上書きにならず、アンインストールの登録が二重になる） |

## 2-1. サーバの配布ディレクトリから配る（LOOPTRACK_DIST_DIR）

GitHub Releases を使わずに、looptrack を自分のサーバから配ることもできます（DESIGN.md §5-11「配布と更新」）。

| 物 | 中身 |
| -- | -- |
| 置き方 | `dist.sh build`（署名しないなら `RELEASE_MINISIGN_PUBKEY=` を付け、self-update はハッシュだけを確かめる）と `dist.sh sums` の出力を、サーバの配布ディレクトリに置く（実行ファイル → SHA256SUMS の順に置き、前の版を消す）。出力に入る `install.sh`・`grants.sql` は置いても置かなくてもよい（配布口が配るのは実行ファイルと SHA256SUMS・NOTICE・OFL-BIZUDGothic.txt だけで、ほかの名前は配らない） |
| 版 | 公開の版（`vX.Y.Z`）か、`v0.0.0-<UTC の年月日時分秒>-<コミット ID>`（Go の擬似版の形。semver のプレリリースとして時刻の順に並ぶ） |
| サーバ（looptrack serve） | `LOOPTRACK_DIST_DIR=<配布ディレクトリ>`（コンテナなら読み取り専用で入れる）。`GET /api/v1/dist`（と setup の券の一覧）の `binaries: [{name, os, arch, version, sha256, size, url}]` に (os, arch) ごとの最新を出し、`/api/v1/dist/bin/<名前>` で本体を返す。SHA256SUMS に載っていない・ハッシュが違うファイルは配らない。配布ディレクトリが無い・空なら `binaries` は空の一覧 |
| 最低の対応版 | `.env` に `LOOPTRACK_CLIENT_MIN_VERSION=v…` を書くと、それより古い looptrack の導入に【配布スクリプトの更新】が出る（空なら判定しない） |
| setup の手順 | looptrack だけ（以前の手順と `LOOPTRACK_SETUP_GO` は撤去した）。配布ディレクトリに looptrack が無ければ取得の手順を示せない旨を出す |
| 更新 | 利用者の手元で `looptrack self-update`（`--check` で確かめるだけ）。PATH・配線の確認は `looptrack doctor` |

確かめるには `curl -s -H "Authorization: Bearer $TOKEN" https://example.com/looptrack/api/v1/dist | jq .binaries` と、手元の looptrack での `looptrack self-update --check` を使います。

## 2-2. NOTICE（第三者のライセンス文）

配布物に添える第三者のライセンス文は、リポジトリのルートの `NOTICE` です。生成物ですがコミットします。手で直さないでください。

```
go run ./internal/tools/notice          # 作り直す（依存を足した・go.mod の版を上げたら流してコミットする）
go run ./internal/tools/notice -check   # 書き換えずに、今の依存と合っているかだけ確かめる
```

- 中身: `./cmd/looptrack` にリンクされるモジュール（標準ライブラリと本体を除く）ごとに、モジュールのパス・版・ライセンス文を並べます。
  ライセンス文は Module.Dir の直下の LICENSE・LICENCE・COPYING・NOTICE から取ります。大文字小文字・拡張子・`LICENSE-MIT` のような接尾辞の違いは許します。
  対象のモジュールは、リリースで作る全ての形で `go list -deps -json` を実行した和です（headless の 6 対象と desktop の 6 対象。cgo を使うのは darwin の desktop だけ）。
  先頭には BIZ UDGothic の OFL（`internal/client/report/pdf/fonts/OFL.txt`）と、Go の標準ライブラリ・ランタイムのライセンス（`$GOROOT/LICENSE`）を置きます。
- **入手先の URL**: 各節に `Source:` と `Source (this version):` を書きます。
  前者はリポジトリで、module path から `/v2` のような major 版の接尾辞を外したものです。
  後者はその版のソースの zip で、`https://proxy.golang.org/<エスケープした module path>/@v/<版>.zip` の形です（大文字は `!小文字`）。
  MPL-2.0 のモジュール（今は `github.com/go-sql-driver/mysql`）には、「ソースは上の URL から取れる」旨の案内を添えます。
  MPL-2.0 は、実行ファイルで配るときに受け取った人へソースの入手先を知らせることを求めるからです。
  判定はライセンス文に MPL-2.0 の見出しがあるかで行うので、MPL-2.0 の依存が増えても自動で付きます。
- **AppImage の runtime**: AppImage の先頭に付く type2-runtime（MIT）も節を持ちます。
  ライセンス文の写しは `deploy/release/licenses/AppImage-type2-runtime-LICENSE.txt` にあり、版は `deploy/release/desktop.sh` の `APPIMAGE_RUNTIME_TAG` から読みます。
- **runtime に静的リンクされた部品**（libfuse 3.15.0・musl libc・squashfuse・zstd・zlib・mimalloc）は、`deploy/release/licenses/runtime-components.json` を元データにして部品ごとの節を出します。
  節には版・著作権表示・ソースの URL と SHA-256・上流の改変・**ライセンス文の全文**が入ります。
  libfuse は LGPL-2.1 なので、全文（`LGPL-2.1.txt`）・対応ソースの置き場・作り直しの手順（`RELINKING.md`）も節に書きます（DESIGN.md §5-14）。
  マニフェストの `runtime.tag` が `desktop.sh` の `APPIMAGE_RUNTIME_TAG` と違えば、生成は失敗します。写しの SHA-256 も確かめます。
- モジュールのパスで並べ、日付は入れません。何度作り直しても同じ結果になります。
  ライセンス文が見つからないモジュールがあれば失敗するので、そのときは依存を見直してください。
- 実行ファイルは NOTICE を埋め込んでいて（ルートの `notice.go`）、`looptrack licenses` で全文を表示します。
- 配る経路は次のとおりです。
  - `dist.sh build`: 出力先に `NOTICE` を置き、SHA256SUMS に載ります。
  - `dist.sh image-context` と `deploy/build.sh`: イメージの `/NOTICE` に入ります。
  - `desktop.sh`: .app・AppImage・Windows の zip に入ります。AppImage には runtime のライセンス文も `usr/share/doc/looptrack/AppImage-type2-runtime-LICENSE.txt` として入ります。
    runtime に静的リンクされた部品の全文・`RELINKING.md`・`runtime-components.json` は `usr/share/doc/looptrack/licenses/` に入ります。
  - `deploy/install.sh`: 利用者の手元で作る compose のイメージの `/NOTICE` に入ります。
    取得元から取るのではなく、入れる実行ファイル自身の `looptrack licenses` の出力を書き出します。なので実行ファイルと必ず同じ版になります。

  サーバの配布ディレクトリに置くと、`/api/v1/dist` の `notice_url` に出ます。
- CI の `notice` ジョブが「作り直して差分が無いこと」を確かめます。
  `release` の `desktop-linux` は、AppImage の `usr/share/doc/looptrack/licenses/` に `LGPL-2.1.txt`・`RELINKING.md`・`runtime-components.json` と写しが入っていることを確かめます。中身がリポジトリと同じかも見ます。

### AppImage の runtime の版を上げるとき

`deploy/release/desktop.sh` の `APPIMAGE_RUNTIME_TAG` を上げるときは、**部品の一覧も一緒に取り直します**。
取り直しを忘れても、NOTICE の生成が失敗するので気づけます。手順は次のとおりです。

1. `APPIMAGE_RUNTIME_TAG` と `APPIMAGE_RUNTIME_SHA256_x86_64`・`APPIMAGE_RUNTIME_SHA256_aarch64` を、
   その tag の Releases のファイルの SHA-256 で直します。
2. 同じ tag の `LICENSE` を取り直し、`deploy/release/licenses/AppImage-type2-runtime-LICENSE.txt` と
   `APPIMAGE_RUNTIME_LICENSE_SHA256` を直します。
3. 部品の版を確かめ直します。見るのは上流の `scripts/common/install-dependencies.sh`（libfuse・squashfuse は版と SHA-256 が固定）・
   `scripts/docker/Dockerfile`（Alpine の版で、musl・zlib・zstd・mimalloc はその apk）・`src/runtime/Makefile`（リンクする部品）です。
   取った runtime の二進の中の文字列（`strings`）でも版を確かめられます。
4. 版が変わった部品は、ソースの tarball を取り直して SHA-256 を記録します。
   その中のライセンス文を `deploy/release/licenses/` の写しに入れ直してください。ファイル名に版が入っているので、名前も変わります。
5. `deploy/release/licenses/runtime-components.json` の `runtime.tag`・`runtime.commit`・`runtime.source_tarball` を直します。
   各部品の `version`・`source_tarball`・`license_files`（ファイル名と SHA-256）も直し、改変（`modification`）も確かめ直します。
6. 対応ソース一式（runtime と部品の tarball・`SHA256SUMS`）を作り直します。
   `runtime.corresponding_source.url` の置き場に、新しい tag 用のものを置きます（下の「対応ソースの置き場」）。
7. `go run ./internal/tools/notice` で NOTICE を作り直してコミットします。
8. AppImage を 1 つ組み立てて、`usr/share/doc/looptrack/licenses/` の中身を確かめます。
   `bash deploy/release/desktop.sh appimage v0.0.0-test <arch> out` → `--appimage-extract` の順です。
   macOS には mksquashfs が無いので、`golang` のコンテナに `squashfs-tools` を入れて実行してください。

### 対応ソースの置き場（LGPL-2.1 §6(d)）

libfuse（LGPL-2.1）の対応ソースは、**毎回のリリースには添付しません**。runtime の tag ごとに **1 回限りのリリース**を作ります。
そこに 7 本（runtime のソース + 6 部品のソース・約 11MB）と `SHA256SUMS` を置き、NOTICE と AppImage の中の案内から指します（`runtime-components.json` の `runtime.corresponding_source.url`）。
同じ runtime を使うリリースには同じ資料が当てはまるので、置き場は tag ごとに 1 つで足ります。

> **公開のときにやること**: 今の `corresponding_source` は `status: planned` で、URL は仮です。
> 公開のときに上の 7 本（URL と SHA-256 はマニフェストにあります）を取り直して照合し、リリースを作って `url` を確定してください。
> そのあと `status` を `published` に直し、NOTICE を作り直します。

## 2-3. install.sh と grants.sql（導入用の資産）

`dist.sh build` は、実行ファイルと同じディレクトリに `install.sh`（`deploy/install.sh` の写し）と `grants.sql`（`deploy/grants.sql` の写し）を置きます。
`dist.sh sums` がそれを `SHA256SUMS` に載せます。
名前は写す前と同じにします。取得の 1 行が指す名前であり、install.sh の「取得元の規約」の接頭辞の下にそのまま並ぶからです。
release.yml は `binaries` で「リポジトリの `deploy/` の写しと同じであること」を、`sums` で「`SHA256SUMS` に載っていること」を確かめます。
そのうえで `publish` が Releases の資産に入れます（`gh release create … dist/*`）。

**公開後**の取得の 1 行は次のとおりです。`<org>/<repo>` は公開先で、README・利用者ガイド・DEPLOY.md にも同じ形を書いています。

```bash
curl -fsSL https://github.com/<org>/<repo>/releases/latest/download/install.sh -o install.sh
less install.sh                                    # 実行する前に中身を読む
sudo sh install.sh --from https://github.com/<org>/<repo>/releases/latest/download
```

- `releases/latest/download/<名前>` は最新の**正式版**の資産を指します（プレリリースは指しません）。
  版を固定するなら `releases/download/<タグ>/<名前>` にします（README・DEPLOY.md はこちらの形）。
- `--from` に渡すのは「その版の資産が並ぶ接頭辞」です。
  install.sh はその下の `SHA256SUMS`・`SHA256SUMS.minisig`・`looptrack_<版>_linux_<arch>` を取り、SHA-256 と署名を確かめてから入れます。
  取った install.sh 自身も `SHA256SUMS` に載っているので、別に取り直して照合できます（`grep ' install.sh$' SHA256SUMS | sha256sum -c -`）。
- MySQL を最小権限で使うときは、同じ接頭辞から `grants.sql` も取れます（DEPLOY.md「保存先に MySQL を選ぶとき」）。
- 更新は `sudo sh install.sh --upgrade --from <同じ接頭辞>` です。資産の並びは入れるときと同じです。

**公開前**（Releases がまだ無い間）は、手元で作った配布物のディレクトリをそのまま `--from` に渡して確かめます。
install.sh 自身もその中にあるので、リポジトリではなく配布物から実行してください。

```bash
bash deploy/release/dist.sh build v0.0.0-local /tmp/dist
bash deploy/release/dist.sh sums /tmp/dist
sudo sh /tmp/dist/install.sh --from /tmp/dist
```

この経路は `deploy/install_test.sh` が Docker で確かめています。
リリースの出力のディレクトリをそのまま取得元にして、install.sh も grants.sql もそこから取れることを見ています。

## 3. 版の付け方

- [semver](https://semver.org/lang/ja/) に従います。形は `vMAJOR.MINOR.PATCH` です。
  候補版は `v1.0.0-rc.1` のように `-rc.N` を付けます（semver の比較で rc.10 が rc.9 より後になります）。
- 互換を壊す変更は MAJOR を上げます。CLI の出力・終了コード・REST / MCP の形・環境変数・マイグレーションで戻れない変更がこれに当たります。
  機能の追加は MINOR、修正だけなら PATCH です。
- タグを打つ前に、CHANGELOG.md に版ごとの「追加・変更・修正・移行の注意」を書きます。

## 4. リリースの手順

版を切る前に、公開候補をまっさらな環境で通しておきます（[QUICKSTART-CHECK.md](QUICKSTART-CHECK.md)）。
README・利用者ガイド・`install.sh`・`setup` のどれかを変えたときも同じです。

1. CI（`ci`）を手動で実行し、緑であることを確かめます（`gh workflow run ci.yml`）。
   push では走らないので、main に入れただけでは実行されていません。実行する SHA は、手元の全検査が緑のものにします。
2. CHANGELOG.md を更新して main にコミットします。
3. 注釈付きタグを打って push します。push は CI・release を動かすので、利用者の承認を得てから行ってください。
   ```
   git tag -a v1.0.0-rc.1 -m "v1.0.0-rc.1"
   git push origin v1.0.0-rc.1
   ```
4. Actions の `release` の実行を開き、artifact を取って確かめます。
   ```
   sha256sum -c SHA256SUMS                        # macOS は shasum -a 256 -c SHA256SUMS
   minisign -Vm SHA256SUMS -p deploy/release/minisign.pub   # 署名したとき（bash deploy/release/dist.sh verify <dir> でも同じ）
   codesign -dvv looptrack_v1.0.0-rc.1_darwin_arm64 # 署名したとき: Authority=Developer ID Application: HOWA SHOJI K.K.
   spctl -a -vvv -t install looptrack_v1.0.0-rc.1_darwin_arm64 # 署名したとき: accepted / source=Notarized Developer ID
   ./looptrack_v1.0.0-rc.1_darwin_arm64 version     # → looptrack v1.0.0-rc.1 (headless, darwin/arm64)（既定は英語。日本語は LOOPTRACK_LANG=ja）
   docker load -i image-amd64.tar && docker run --rm ghcr.io/<owner>/looptrack:v1.0.0-rc.1-amd64 version
   ```
5. 公開します。`release` を手動で起動し、`version` にタグ名を入れて `publish` にチェックします。
   リポジトリ変数 `RELEASE_PUBLISH=true` にしておけば、タグの push で公開まで進みます。
6. GHCR のイメージと Releases の下書きを確かめ、Releases の画面で公開します。
   イメージは `docker buildx imagetools inspect ghcr.io/<owner>/looptrack:<版>` で、amd64・arm64 の 2 つがあることを見ます。
   下書きには実行ファイル 6 個・デスクトップ版・NOTICE・OFL-BIZUDGothic.txt・**install.sh**・**grants.sql**・SHA256SUMS・SHA256SUMS.minisig がそろっているはずです。
   公開したら、取得の 1 行（上の「2-3. install.sh と grants.sql」）が実際に動くことを確かめます（`curl -fsSL …/releases/latest/download/install.sh -o install.sh` → `sudo sh install.sh --from …`）。
   初回は GHCR のパッケージの公開範囲（既定は private）と、リポジトリへの紐付けも確かめてください。

やり直すとき: 公開前ならタグを消して打ち直して構いません（`git push --delete origin <tag>`）。公開後の版は消さずに、次の PATCH / rc を出します。

## 5. 手元での確認

```
bash deploy/release/dist.sh build v0.0.0-local /tmp/dist   # 6 対象の looptrack と NOTICE・OFL-BIZUDGothic.txt・install.sh・grants.sql
bash deploy/release/dist.sh sums /tmp/dist
(cd /tmp/dist && shasum -a 256 -c SHA256SUMS)
bash deploy/install_test.sh                                # Docker で install.sh（この出力を --from に渡す経路も）を通す
bash deploy/release/sign-macos.sh --identity - --skip-notarize /tmp/dist/*_darwin_*   # 署名の手順だけ（ad-hoc。Apple に送らない）
bash deploy/release/dist.sh image-context v0.0.0-local arm64 /tmp/dist /tmp/ctx-arm64
docker buildx build --platform linux/arm64 -t im-check:local --load /tmp/ctx-arm64 && docker run --rm im-check:local version
```

workflow を直したら、actionlint（`go run github.com/rhysd/actionlint/cmd/actionlint@<版>`）で確かめます。CI の `checks` ジョブも同じものを実行します。

## 6. 用意するもの（リポジトリの設定）

| 種類 | 名前 | 用途 | 今 |
| -- | -- | -- | -- |
| 自動 | `GITHUB_TOKEN` | GHCR への push（`packages: write`）・Releases の下書き（`contents: write`）。権限はジョブごとに与えるので、Settings の既定（read）のままでよい | 追加の設定なし |
| 変数 | `RELEASE_PUBLISH` | `true` でタグの push から公開まで進める | 未設定（作るだけ） |
| 変数 | `RELEASE_SIGN` | `true` で macOS の署名・公証と SHA256SUMS の署名を行う（公開には必須）。meta が読むのでリポジトリ変数 | 未設定（private の間は手元で署名） |
| 変数（Environment `release`） | `APPLE_TEAM_ID`・`MACOS_SIGN_IDENTITY` | 署名の身元と TeamIdentifier の確認 | 公開用リポジトリを作るときに登録 |
| 秘密（Environment `release`） | `MACOS_CERT_P12_BASE64`・`MACOS_CERT_P12_PASSWORD`・`APPLE_API_KEY_P8_BASE64`・`APPLE_API_KEY_ID`・`APPLE_API_ISSUER_ID`・`MINISIGN_SECRET_KEY`・`MINISIGN_PASSWORD` | macOS の署名と公証・SHA256SUMS の署名。登録の手順は「署名」 | 可搬媒体に保管。公開用リポジトリを作るときに登録 |

Dependabot（`.github/dependabot.yml`）が、Go のモジュールと Actions の版の更新 PR を週 1 回まとめて出します。
**その PR では CI は走りません**（上の「1. CI」）。取り込む前に、手元の全検査（CONTRIBUTING.md の「Running the tests」）を通してください。
