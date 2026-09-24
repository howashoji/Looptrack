# デスクトップ版

[ガイドの目次](README.md) · 関連: [始め方](getting-started.md) · [FAQ / トラブルシュート](faq.md)

デスクトップ版は、1 台の PC で 1 人で使うための形です。ターミナルを開かずに使い始められます。
アイコンをダブルクリックすると手元のサーバが起動し、既定のブラウザで画面が開きます。サーバは 127.0.0.1 だけで待ち受け、データは SQLite のファイル 1 つに入ります。
初めて起動したときは、ブラウザに初回設定の画面が出ます。管理者・二段階認証・最初のプロジェクトをここで決めます。
タスクトレイのアイコンからは、画面を開く・AI の MCP の接続設定をコピーする・CLI を使えるようにする・ログイン時に起動する・終了するといった操作ができます。macOS ではメニューバーにアイコンが出ます。
Windows にはインストーラがあります。スタートメニューに登録され、「アプリ」の一覧からアンインストールできます。

どの OS でも管理者権限は要りません。
複数人で使うサーバは `looptrack setup` で立ち上げます。詳しくは[始め方](getting-started.md)を見てください。

## 取得

リリースのページ（https://github.com/howashoji/looptrack/releases ）から、OS に合うファイルと `SHA256SUMS` を落としてください。

| OS | ファイル | 中身 |
| -- | -- | -- |
| macOS 13 以降（Apple シリコン・Intel） | `Looptrack_<版>_macos_universal.dmg` | `Looptrack.app` |
| Windows 10 / 11 | `Looptrack_<版>_windows_amd64_setup.exe`（Arm の PC は `arm64`） | インストーラ（こちらがおすすめ） |
| Windows 10 / 11・入れずに使う | `Looptrack_<版>_windows_amd64.zip`（Arm の PC は `arm64`） | `Looptrack\Looptrack.exe`（アプリ）と `Looptrack\cli\looptrack.exe`（CLI） |
| Linux（x86_64 / aarch64） | `Looptrack_<版>_linux_x86_64.AppImage`（Arm は `aarch64`） | アプリ（ファイル 1 つ） |

取得したファイルは `SHA256SUMS` と照らし合わせます。使うコマンドは次のとおりです。

- macOS: `shasum -a 256 -c SHA256SUMS --ignore-missing`
- Linux: `sha256sum -c SHA256SUMS --ignore-missing`
- Windows: `Get-FileHash`

## 最初の起動

### macOS

dmg を開いて `Looptrack` を `アプリケーション` へドラッグし、`アプリケーション` の `Looptrack` をダブルクリックします。
署名と公証をしてあるので、macOS の警告は出ません。
Dock にはアイコンが出ないので、メニューバーの輪のアイコンを探してください。

### Windows

`Looptrack_<版>_windows_amd64_setup.exe` をダブルクリックします。
インストーラにはまだコード署名が無いので、SmartScreen が「Windows によって PC が保護されました」と出すことがあります。そのときは「詳細情報」→「実行」を選んでください。
管理者権限は要りません。自分だけの場所（`%LOCALAPPDATA%\Programs\Looptrack Desktop`）に入り、スタートメニューに Looptrack が登録されます。
途中で 2 つの選択肢が出ます。どちらも後からトレイのメニューで変えられます。

- ログイン時に起動する
- CLI を使えるようにする（[CLI を使う](#cli-を使う)）

最後の「Looptrack を起動する」にチェックを入れたまま終えるか、スタートメニューから起動します。
アイコンは通知領域に出ます。Windows 11 は新しいアイコンを時計の横の「^」（隠れているインジケーター）の中に入れます。
「^」を押して Looptrack のアイコンをタスクバーへドラッグすれば、いつも見えるようになります。「設定 → 個人用設定 → タスクバー → その他のシステム トレイ アイコン」でも切り替えられます。
アイコンを左クリックすると画面が開き、右クリックでメニューが出ます。

何もインストールしたくないときは zip を使います。`%USERPROFILE%\Apps` のようなユーザーのフォルダの中に展開し、`Looptrack.exe` をダブルクリックしてください。この例なら `%USERPROFILE%\Apps\Looptrack\Looptrack.exe` になります。
`%LOCALAPPDATA%\Programs\looptrack` は CLI を置くフォルダなので、そこには展開しないでください。

### Linux

AppImage を `~/Applications` のような動かさない場所に置き、実行できるようにしてからダブルクリックします。端末から実行してもかまいません。
AppImage に FUSE 2 は要りませんが、`fusermount3` が要ります。ふつうのデスクトップには既に入っていて、Ubuntu なら `sudo apt install fuse3` で入れられます。
入れられないときは `APPIMAGE_EXTRACT_AND_RUN=1 ./Looptrack_<版>_linux_x86_64.AppImage` で起動してください。`./Looptrack_<版>_linux_x86_64.AppImage --appimage-extract` で展開し、`squashfs-root/usr/bin/looptrack` を実行する方法もあります。
トレイのアイコンが出るのは、KDE・Xfce・Cinnamon のように StatusNotifierItem のアイコンを出せるデスクトップです。GNOME では拡張「AppIndicator and KStatusNotifierItem Support」を入れてください。
トレイが無くてもアプリは動きます。止めるときは `looptrack desktop --quit` を使います（[トラブルシュート](#トラブルシュート)）。

起動中にもう一度ダブルクリックしても 2 つ目は立ち上がらず、ブラウザで画面が開くだけです。

## トレイ / メニューバー

| 項目 | すること |
| -- | -- |
| 画面を開く | ブラウザで画面を開きます（既定は `http://127.0.0.1:18090/looptrack/`） |
| AI の接続設定をコピー | Claude Code・Codex・GitHub Copilot の MCP の接続設定を、そのまま貼れる形でクリップボードにコピーします。「接続設定の画面を開く」を選ぶと、ブラウザで全部を表示します |
| CLI を使えるようにする | 端末や AI から `looptrack` を呼べるようにします（[CLI を使う](#cli-を使う)） |
| ログイン時に起動する | チェックすると、ログインしたときに裏で起動します。このときはブラウザを開きません |
| 終了 | サーバとアプリを止めます |

Windows ではトレイのアイコンを左クリックすると画面が開き、右クリックでメニューが出ます。
macOS と Linux では、どちらのボタンでもメニューが出ます。

ポートは起動のたびに同じなので、コピーした MCP の接続設定はそのまま使えます。
ほかのプログラムがそのポートを使っていると、空いているポートに替えてそれを覚えます。そのときは接続設定をコピーし直してください。

## データの置き場

アプリとデータは別の場所にあるので、アプリを置き換えてもデータは残ります。

| OS | データ（DB・鍵・状態） | ログ |
| -- | -- | -- |
| macOS | `~/Library/Application Support/Looptrack` | `~/Library/Logs/Looptrack` |
| Windows | `%LOCALAPPDATA%\Looptrack` | `%LOCALAPPDATA%\Looptrack\logs` |
| Linux | `~/.local/share/looptrack`（`$XDG_DATA_HOME`） | `~/.local/state/looptrack`（`$XDG_STATE_HOME`） |

データのフォルダには `looptrack.db`（イシュー・利用者・設定のすべて）と `looptrack.db.secret-key`（二段階認証の秘密を暗号化する鍵）があります。
バックアップは 2 つを一緒に取ってください。**鍵が無いと二段階認証の登録が使えなくなります。**
どちらのファイルも本人しか読めません。

## CLI を使う

「CLI を使えるようにする」は、管理者権限の要らない場所に CLI を置きます。

| OS | 置き場 |
| -- | -- |
| macOS / Linux | `~/.local/bin/looptrack`（アプリの中の `looptrack` へのリンク） |
| Windows | `%LOCALAPPDATA%\Programs\looptrack\looptrack.exe`（アプリに同梱の `cli\looptrack.exe` の写し。アプリを更新すると写し直す） |

そのフォルダが `PATH` に無ければ、足し方を案内します。
別の方法で入れた `looptrack` が既にあれば、そちらはそのままにします。
サーバの URL には、`looptrack desktop --status` が出す URL から末尾の `/` を除いたもの（`http://127.0.0.1:18090/looptrack`）を使います。
このサーバは自分の PC の中だけのものです。画面や AI の MCP と同じく、ログインもトークンの発行も要りません。

```bash
LOOPTRACK_API_URL=http://127.0.0.1:18090/looptrack LOOPTRACK_PROJECT=main looptrack issue list
```

複数人で使うサーバに同じ CLI を向けるときだけ、`looptrack issue login --browser --url <そのサーバの URL>` で一度ログインしてください。

## ログイン時に起動する

トレイのメニューの「ログイン時に起動する」にチェックを入れます。
登録に管理者権限は要りません。登録先は OS ごとに次のとおりです。

- macOS: `~/Library/LaunchAgents` の LaunchAgent
- Linux: `~/.config/autostart` の `looptrack.desktop`
- Windows: `HKEY_CURRENT_USER\Software\Microsoft\Windows\CurrentVersion\Run` の値 `Looptrack`

チェックを外すと登録を消します。
アプリを別の場所に移したときは、次にアプリを起動したときに登録も新しい場所に合わせて直ります。

## 更新

1. トレイのメニューで「終了」を選びます。
2. アプリを新しい版に置き換えます。
   - macOS: 新しい dmg を開き、`Looptrack` を `アプリケーション` へドラッグします。確認が出たら「置き換える」を選びます。
   - Windows: 新しいインストーラを実行します。前の版に上書きで入り、選択肢もそのまま残ります。Looptrack が動いていれば、インストーラが先に止めます。zip を使っているときは、新しい zip を前と同じフォルダに上書きで展開します。
   - Linux: 新しい AppImage を前のものと置き換えます。ファイル名は前のままでもかまいません。
3. アプリをもう一度起動します。

データは上のデータのフォルダに残り、新しい版を最初に起動したときに自動で新しい形に変わります。
デスクトップ版では `looptrack self-update` を使いません。アプリごと置き換えてください。
macOS と Linux の CLI のリンクはそのままアプリを指します。Windows の CLI の写しは次の起動で新しくなります。

## アンインストール

先にトレイのメニューで「終了」を選び、「ログイン時に起動する」にチェックがあれば外します。
そのうえでアプリ・CLI・CLI の資格情報を消します。データは要らなければ消してください。

### macOS

```bash
rm -rf /Applications/Looptrack.app
rm -f ~/Library/LaunchAgents/*looptrack*.plist        # 「ログイン時に起動する」を外し忘れたときだけ
[ -L ~/.local/bin/looptrack ] && rm ~/.local/bin/looptrack
# CLI の資格情報（ログインしたサーバのアクセストークン。ほかのサーバで CLI を使い続けるなら残す）:
rm -rf ~/.config/looptrack
# データとログ — イシューがすべて消えます:
rm -rf ~/Library/Application\ Support/Looptrack ~/Library/Logs/Looptrack
```

### Windows

インストーラで入れたときは、「設定 → アプリ → インストールされているアプリ」で Looptrack を見つけて「アンインストール」を選びます。
アプリ・スタートメニューの登録・「ログイン時に起動する」の登録・インストーラが置いた CLI の写しが消えます。
データはわざと残します。入れ直せばイシューがそのまま戻ります。

zip を使っているときは次のコマンドで消します。

```powershell
Remove-Item -Recurse -Force "$env:USERPROFILE\Apps\Looptrack"          # 展開したアプリ（自分のフォルダに合わせる）
Remove-ItemProperty -Path HKCU:\Software\Microsoft\Windows\CurrentVersion\Run -Name Looptrack -ErrorAction SilentlyContinue
Remove-Item -Recurse -Force "$env:LOCALAPPDATA\Programs\looptrack"      # CLI の写し（「CLI を使えるようにする」で作ったときだけ）
```

CLI の資格情報とデータは、どちらの入れ方でも残ります。消すときは次のとおりです。

```powershell
Remove-Item -Recurse -Force "$env:APPDATA\looptrack"        # CLI の資格情報（ほかのサーバで CLI を使い続けるなら残す）
Remove-Item -Recurse -Force "$env:LOCALAPPDATA\Looptrack"   # データ — イシューがすべて消えます
```

### Linux

```bash
rm -f ~/Applications/Looptrack_*.AppImage                   # 自分の置き場に合わせる
rm -f ~/.config/autostart/looptrack.desktop
[ -L ~/.local/bin/looptrack ] && rm ~/.local/bin/looptrack
# CLI の資格情報（ログインしたサーバのアクセストークン。ほかのサーバで CLI を使い続けるなら残す）:
rm -rf ~/.config/looptrack
# データとログ — イシューがすべて消えます:
rm -rf ~/.local/share/looptrack ~/.local/state/looptrack
```

## トラブルシュート

| 症状 | 対処 |
| -- | -- |
| ダブルクリックしても何も起きないように見える | トレイのアイコンが見えないまま起動していることがあります。`http://127.0.0.1:18090/looptrack/` を開くか、上のログのフォルダにあるログを見てください |
| Windows 11 でトレイのアイコンが出ない | 時計の横の「^」（隠れているインジケーター）の中にあります。「^」を押して Looptrack のアイコンをタスクバーへドラッグするか、「設定 → 個人用設定 → タスクバー → その他のシステム トレイ アイコン」で出します |
| SmartScreen がインストーラを止める | インストーラにはまだコード署名がありません。`SHA256SUMS` と照らし合わせたうえで、「詳細情報」→「実行」を選びます |
| Linux で AppImage が `No suitable fusermount binary found on the $PATH` で起動しない | FUSE 3 がありません。`sudo apt install fuse3` で入れてください。ほかのディストリビューションでは fuse3 に当たるパッケージを入れます。入れられないときは `APPIMAGE_EXTRACT_AND_RUN=1` を付けて起動するか、`--appimage-extract` で展開して `squashfs-root/usr/bin/looptrack` を実行します |
| Linux でトレイのアイコンが出ない | StatusNotifierItem を出せるもの（GNOME なら AppIndicator の拡張）を入れます。止めるときは下のコマンドを端末で使います |
| 再起動したら AI の MCP の接続が切れた | ほかのプログラムがポートを使っていたので、空いているポートに替わりました。ログにも出ます。接続設定をコピーし直してください |
| ブラウザが開かない | `looptrack desktop --status` が出す URL を自分で開いてください |

端末からは次のコマンドを使います。ここでの `looptrack` は、AppImage のファイル・`Looptrack.app/Contents/MacOS/looptrack`・Windows の `cli\looptrack.exe` でもかまいません。

```bash
looptrack desktop --status   # 起動中なら URL を出す
looptrack desktop --quit     # 起動中のアプリを止める
```

`--quit` はまずアプリに終了を頼み、データを書き終えてから止めます。頼み方は macOS と Linux では SIGTERM、Windows では
終了を頼む印です。トレイを出していなくても同じです。頼んでも止まらないときは、Windows だけ強制終了に進んで
その理由を表示します。macOS と Linux に強制終了はありません。
