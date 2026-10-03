# デスクトップ版のトラブルシュート

[ガイドの目次](../README.md) · [デスクトップ版](README.md) · 前: [デスクトップ版の更新](updating.md) · 関連: [FAQ / トラブルシュート](../faq.md)

デスクトップ版が起動しない・トレイのアイコンが出ない・AI の接続が切れた、といったときの対処をまとめたページです。
【導入が未完了】のように、どちらの版でも出る表示は共通の [FAQ / トラブルシュート](../faq.md) で扱います。

## 症状と対処

| 症状 | 対処 |
| -- | -- |
| ダブルクリックしても何も起きないように見える | トレイのアイコンが見えないまま起動していることがあります。`http://127.0.0.1:18090/looptrack/` を開くか、ログのフォルダ（[データの置き場](using.md#データの置き場)）にあるログを見てください |
| Windows 11 でトレイのアイコンが出ない | 時計の横の「^」（隠れているインジケーター）の中にあります。「^」を押して Looptrack のアイコンをタスクバーへドラッグするか、「設定 → 個人用設定 → タスクバー → その他のシステム トレイ アイコン」で出します |
| SmartScreen がインストーラを止める | インストーラにはまだコード署名がありません。`SHA256SUMS` と照らし合わせたうえで、「詳細情報」→「実行」を選びます |
| インストーラやアプリが止められて「実行」が出ない | Windows 11 のスマート アプリ コントロールが「オン」だと、署名の無いアプリは止められます。会社の管理下の PC では、管理者の方針で止まることもある。打てる手は[サーバ版の始め方](../server/getting-started.md#署名と-os-の警告)の「署名と OS の警告」にまとめています。デスクトップ版にもそのまま当てはまります |
| Linux で AppImage が `No suitable fusermount binary found on the $PATH` で起動しない | FUSE 3 がありません。`sudo apt install fuse3` で入れてください。ほかのディストリビューションでは fuse3 に当たるパッケージを入れます。入れられないときは `APPIMAGE_EXTRACT_AND_RUN=1` を付けて起動するか、`--appimage-extract` で展開して `squashfs-root/usr/bin/looptrack` を実行します |
| Linux でトレイのアイコンが出ない | StatusNotifierItem を出せるもの（GNOME なら AppIndicator の拡張）を入れます。止めるときは下のコマンドを端末で使います |
| 再起動したら AI の MCP の接続が切れた | ほかのプログラムがポートを使っていたので、空いているポートに替わりました。ログにも出ます。接続設定をコピーし直してください |
| ブラウザが開かない | `looptrack desktop --status` が出す URL を自分で開いてください |

端末からは次のコマンドを使います。ここでの `looptrack` は、AppImage のファイル・`Looptrack.app/Contents/MacOS/looptrack`・Windows の `cli\looptrack.exe` でもかまいません。

```bash
looptrack desktop --status   # 起動中なら URL を出す
looptrack desktop --quit     # 起動中のアプリを止める
```

`--quit` は、まずアプリに終了を頼みます。データを書き終えてから止まります。
頼み方は macOS と Linux では SIGTERM、Windows では終了を頼む印。トレイを出していなくても同じです。
頼んでも止まらなかったら？ Windows だけは強制終了に進み、その理由を表示します。macOS と Linux に強制終了はありません。

CLI を Windows で使うときのつまずき（PATH・hook・ヒアドキュメント）は、FAQ の [Windows でつまずきやすい点](../faq.md#windows-でつまずきやすい点)にあります。
