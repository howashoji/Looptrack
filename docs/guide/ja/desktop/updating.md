# デスクトップ版の更新

[ガイドの目次](../README.md) · [デスクトップ版](README.md) · 前: [デスクトップ版の使い方](using.md) · 次: [デスクトップ版のトラブルシュート](troubleshooting.md)

デスクトップ版が新しい版をどう知らせ、どう置き換わるかと、前の版への戻し方・アンインストールの手順をまとめたページです。
デスクトップ版はアプリごと置き換えて更新します。同梱の CLI もそれに合わせて新しくなります。アプリの更新に `looptrack self-update` は使いません。使えるのは Windows の CLI の写しだけです（[CLI だけを self-update で新しくする](#cli-だけを-self-update-で新しくする)）。

## 新しい版の知らせ

新しい版が出ると、トレイのメニューのいちばん上に「新しい版 <版> に更新する」（置き換えられないときは「新しい版 <版> があります」）が出て、画面の上部にも帯が出ます。確認をやめたいなら、トレイのメニューの「新しい版を確認する」のチェックを外します（[トレイ / メニューバー](using.md#トレイ--メニューバー)）。
確かめるのは起動したときと 24 時間ごと。確かめ方を変える環境変数はサーバと共通で、[新しい版の確認](../server/updating.md#新しい版の確認デスクトップ版とサーバ)にあります。
トレイから置き換えられないときは、知らせを選ぶか[リリースのページ](https://github.com/howashoji/looptrack/releases)を開き、OS に合うファイルを取得してください。
手元の版はアプリの中の `looptrack`（`Looptrack.app/Contents/MacOS/looptrack`・AppImage のファイル・Windows の `cli\looptrack.exe`）で `looptrack version` を実行すれば分かります。

## 更新

トレイのメニューのいちばん上にある「新しい版 <版> に更新する」を選ぶと、アプリが置き換わります。
対象は macOS の .app・Linux の AppImage・Windows です。Windows はインストーラで入れた人も、zip を展開した人も同じように使えます。
「新しい版を自動で入れる」にチェックを入れていれば、新しい版を見つけたときに同じことを自分でやってくれます。

1. 新しい版のファイルを取得し、署名を確かめた `SHA256SUMS` の SHA-256 と照らし合わせます。合わなければ置き換えません。
2. macOS では、dmg とその中の `Looptrack.app` の署名を `spctl` と `codesign` で確かめます。識別子と署名のチームが今のアプリと同じかも見ます。Windows のファイルにはコード署名が無いので、確かめるのは SHA-256 と実行ファイルの形まで。
3. 今のアプリを `.prev` の付いた名前に変え（`Looptrack.app.prev`・`<AppImage のファイル名>.prev`）、新しい版を元の名前で置きます。前の `.prev` は消えます。
   Windows の zip では、zip に入っている 4 つのファイルを 1 つずつ同じように置き換えます。Windows のインストーラで入れた人には、代わりに新しいインストーラが画面を出さずに動き、選択肢もそのまま残ります。
4. アプリを起動し直します。前の版が止まるのを待ってから、新しい版が起動します（ブラウザは開きません）。

途中で失敗しても、今の版のまま動き続けて理由を知らせます。新しい版を起動できなかったときの戻り先は、前の版。
Windows のインストーラは、終わったところでアプリを起動し直します。失敗して元に戻したときに起動するのは前の版。理由はデータのフォルダの `updates` にある `setup.log` に残ります。
アプリの置き場（`/Applications` など）に書き込めないときは？ 置き換えません。macOS は確かめた dmg を開くので、`Looptrack` を `アプリケーション` へドラッグしてください。Linux と Windows の zip はリリースのページを開きます。
置き換えた後で前の版へ戻したいなら、トレイで「終了」を選び、今のアプリを消して `.prev` の名前を元に戻します（DB の戻し方は下の手順と同じ）。

Windows の zip では、アプリの隣に `.prev` の付いたファイルが 4 つ残ります。`Looptrack.exe.prev`・`cli\looptrack.exe.prev`・`NOTICE.prev`・`OFL-BIZUDGothic.txt.prev` です。
どれも前の版のファイル。アプリを終了していれば消してかまいません。消さなくても、次の更新で入れ替わります。
手で前の版に戻すときは、アプリを終了して今の 4 ファイルを消し、`.prev` の付いたファイルをそれぞれ `.prev` を外した名前に戻します。
インストーラで入れた人には `.prev` は作りません。戻すときは前の版のインストーラを実行してください。
コード署名の無い更新をスマート アプリ コントロール・Microsoft Defender・SmartScreen が止めるかどうかは、まだ実機で確かめていません。更新が終わらないときは、次の手順で手で置き換えてください。

トレイから置き換えられないときは、次の手順で置き換えます。

1. トレイのメニューで「終了」を選びます。
2. アプリを新しい版に置き換えます。
   - macOS: 新しい dmg を開き、`Looptrack` を `アプリケーション` へドラッグします。確認が出たら「置き換える」を選びます。
   - Windows: 新しいインストーラを実行します。前の版に上書きで入り、選択肢もそのまま残ります。Looptrack が動いていれば、インストーラが先に止めます。zip なら、新しい zip を前と同じフォルダに上書きで展開します。
   - Linux: 新しい AppImage を前のものと置き換えます。ファイル名は前のままでもかまいません（トレイから置き換えたときもファイル名は変わりません）。
3. アプリをもう一度起動します。

データはデータのフォルダに残ります。新しい版を最初に起動したとき、自動で新しい形に変わります。
変わった後に前の版のアプリへ戻しても、「新しい版の looptrack で migrate した DB」と出て起動しません（この確かめが入る前の版は止まらずに動いてしまうので、なおさら戻さないでください）。
新しい版は形を変える前に `looptrack.db` をデータのフォルダの `backups` に控えます（[データの置き場](using.md#データの置き場)）。控えを作れなければ形を変えず、理由を示して起動を止めます。
前の版に戻す手順はこうです。

1. トレイのメニューで「終了」を選びます。
2. アプリを前の版に戻します。
3. データのフォルダの `looptrack.db` を、`backups` の中で最も新しい控え（名前の時刻が最も新しいもの）で置き換えます。`looptrack.db-wal`・`looptrack.db-shm` があれば消します。
4. アプリを起動します。

控えを取った後に変えた内容は、戻した DB には入っていません。
鍵のファイル（`looptrack.db.secret-key`）は、そのままにしておきます。
更新で形が変わらなかったときは、控えを取りません。前の版はその DB をそのまま使えます。

## 置き換えた後に自動で進むこと

データは残り、新しい版を最初に起動したときに新しい形に変わります。詳しい流れと戻し方は上の[更新](#更新)にあります。
ほかにも、次のものが新しいアプリに合わせて直ります。

- 「ログイン時に起動する」の登録は、次の起動で今のアプリの場所に合わせて直ります。Linux のアプリ一覧の登録（`~/.local/share/applications/looptrack.desktop`）とアイコンも同じ。
- CLI の置き場も直ります。macOS と Linux のリンクはそのままアプリを指し、Windows の CLI の写しは次の起動で新しくなります。
  ただし `self-update` で自分で新しくした写しには触りません（[CLI だけを self-update で新しくする](#cli-だけを-self-update-で新しくする)）。

## CLI だけを self-update で新しくする

`looptrack self-update` の動きは OS ごとに違う。CLI の実体が別のファイルだからです。

Windows では使えます。CLI（`cli\looptrack.exe` と、アプリが `%LOCALAPPDATA%\Programs\looptrack` に置く写し）はトレイを持たない普通の CLI のビルドなので、`self-update` はそのファイルを最新のリリースの `looptrack` に置き換えます（[self-update で置き換える](../server/updating.md#self-update-で置き換える)）。アプリ本体の `Looptrack.exe` には触りません。トレイもダブルクリックの起動もそのまま。

macOS と Linux ではエラーで止まり、何も置き換えません。CLI がアプリそのものだからです。アプリごと更新してください（[更新](#更新)）。新しいリリースがあるかどうかは、`looptrack self-update --check` で分かります。

Windows の写しをこの方法で置き換えると、次のことが変わります。

- アプリはその写しを新しくしなくなります。アプリを更新しても写しは置いた版のままで、CLI とアプリの版がずれることがあります。追いつかせるなら、もう一度 `looptrack self-update` を実行します。
- アプリをアンインストールしても消えません。アンインストーラが消すのは、アプリが置いて、まだ変わっていない写しだけです。フォルダは自分で消してください（アンインストールの [Windows](#windows)）。
- アプリ同梱の CLI に戻すなら、`%LOCALAPPDATA%\Programs\looptrack\looptrack.exe` を消してから、トレイのメニューで「CLI を使えるようにする」を選びます。そのファイルが残っているうちは、メニューの項目は何もしないので注意。

## アンインストール

先にトレイのメニューで「終了」を選び、「ログイン時に起動する」にチェックがあれば外しておきます。
そのうえでアプリ・CLI・CLI の資格情報を消します。データは要らなければ消してください。

### macOS

```bash
rm -rf /Applications/Looptrack.app /Applications/Looptrack.app.prev
rm -f ~/Library/LaunchAgents/*looptrack*.plist        # 「ログイン時に起動する」を外し忘れたときだけ
[ -L ~/.local/bin/looptrack ] && rm ~/.local/bin/looptrack
# CLI の資格情報（ログインしたサーバのアクセストークン。ほかのサーバで CLI を使い続けるなら残す）:
rm -rf ~/.config/looptrack
# データとログ — イシューがすべて消えます:
rm -rf ~/Library/Application\ Support/Looptrack ~/Library/Logs/Looptrack
```

### Windows

インストーラで入れたなら、「設定 → アプリ → インストールされているアプリ」で Looptrack を見つけて「アンインストール」を選びます。
消えるのはアプリ・スタートメニューの登録・「ログイン時に起動する」の登録・インストーラが置いた CLI の写し。
データはわざと残します。入れ直せば、イシューがそのまま戻ってきます。

`self-update` で置き換えた CLI の写しは残ります。そのフォルダは自分で消してください。

```powershell
Remove-Item -Recurse -Force "$env:LOCALAPPDATA\Programs\looptrack"
```

zip を使っているときは、次のコマンドで消します。

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
rm -f ~/Applications/Looptrack_*.AppImage ~/Applications/Looptrack_*.AppImage.prev   # 自分の置き場に合わせる
rm -f ~/.config/autostart/looptrack.desktop
rm -f ~/.local/share/applications/looptrack.desktop ~/.local/share/icons/hicolor/256x256/apps/looptrack.png   # アプリ一覧の登録
[ -L ~/.local/bin/looptrack ] && rm ~/.local/bin/looptrack
# CLI の資格情報（ログインしたサーバのアクセストークン。ほかのサーバで CLI を使い続けるなら残す）:
rm -rf ~/.config/looptrack
# データとログ — イシューがすべて消えます:
rm -rf ~/.local/share/looptrack ~/.local/state/looptrack
```
