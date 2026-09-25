# 始め方

[ガイドの目次](README.md) · 前: [本システムの位置](where-it-fits.md) · 次: [日々の使い方](daily-use.md)

この節では、何も入っていない PC から最初の 1 周までをコマンドを省かずに説明します。
上から順に実行してください。
例では次の値を使うので、自分の環境に合わせて読み替えてください。

| 項目 | 例の値 |
| -- | -- |
| 版 | `v1.0.0` |
| サーバの置き場 | `~/looptrack-server`（Windows は `%USERPROFILE%\looptrack-server`） |
| サーバの URL（ローカル） | `http://127.0.0.1:8090/looptrack` |
| 管理者のログイン名 | `admin` |
| プロジェクトの slug / ID の接頭辞 | `demo` / `DEMO`（最初のイシューは `DEMO-0001`） |
| 作業するリポジトリ | `~/work/demo-app` |

## 0. 使い方を選ぶ

| 使い方 | 向いている場面 | 保存先 | ログイン |
| -- | -- | -- | -- |
| ローカルの 1 人利用 | 自分の PC だけで使う | SQLite（ファイル 1 つ） | 省く（127.0.0.1 だけで待ち受ける） |
| チームのサーバ | 複数の人で使う | MySQL か SQLite | ID・パスワード（+ 二段階認証） |

この節はローカルの 1 人利用を前提に書いています。
チームのサーバとの違いは、手順 3 の後の「チームのサーバの場合」にまとめました。
チームのサーバがまっさらな Linux のマシンなら、手順 1〜3 はインストーラに任せて飛ばせます。手順 3 の後の「まっさらな Linux サーバの場合」を見てください。

Claude Code・Codex・GitHub Copilot など、使う AI は先に入れておいてください。
git も使います。
ほかに要るものはありません。Looptrack は実行ファイル 1 つで、サーバも CLI も hook もそこから動きます。
別の処理系（ランタイム）は要りません。`looptrack issue init` もプロジェクトにスクリプトを 1 つも置きません。
Windows でも同じです。

## 1. 実行ファイルを取得する

リリースのページ（https://github.com/howashoji/looptrack/releases ）から次の 2 つを落とします。

- `looptrack_<版>_<OS>_<CPU>`（サーバ・CLI・hook をまとめた実行ファイル）
- `SHA256SUMS`（ハッシュの一覧）

OS は `darwin`（macOS）・`linux`・`windows` のどれかで、CPU は `amd64`・`arm64` のどちらかです。
Windows のファイルには `.exe` が付きます。
置き場は管理者権限の要らない場所にしてください。

### macOS / Linux

```bash
VER=v1.0.0
OS=darwin      # Linux は linux
ARCH=arm64     # Intel / AMD の CPU は amd64
BASE="https://github.com/howashoji/looptrack/releases/download/$VER"
mkdir -p ~/.local/bin
TMP="$(mktemp -d)" && cd "$TMP"
curl -fsSL -O "$BASE/looptrack_${VER}_${OS}_${ARCH}" -O "$BASE/SHA256SUMS"
grep -E " looptrack_${VER}_${OS}_${ARCH}\$" SHA256SUMS | shasum -a 256 -c -   # Linux は sha256sum -c -
install -m 755 "looptrack_${VER}_${OS}_${ARCH}" ~/.local/bin/looptrack
```

`shasum` の結果が `OK` であることを確かめてください。
`~/.local/bin` が PATH に無いときは、`~/.zshrc` や `~/.bashrc` といったシェルの設定に次の 1 行を足します。足したらターミナルを開き直してください。

```bash
export PATH="$HOME/.local/bin:$PATH"
```

### Windows（PowerShell）

管理者権限は要りません。
置き場は `%LOCALAPPDATA%\Programs\looptrack` です。

```powershell
$Ver = 'v1.0.0'
$Arch = 'amd64'   # ARM の PC は arm64
$D = Join-Path $env:LOCALAPPDATA 'Programs\looptrack'
New-Item -ItemType Directory -Force -Path $D | Out-Null
$Base = "https://github.com/howashoji/looptrack/releases/download/$Ver"
Invoke-WebRequest -UseBasicParsing -Uri "$Base/looptrack_${Ver}_windows_$Arch.exe" -OutFile (Join-Path $D 'looptrack.exe')
Invoke-WebRequest -UseBasicParsing -Uri "$Base/SHA256SUMS" -OutFile (Join-Path $D 'SHA256SUMS')
Get-Content (Join-Path $D 'SHA256SUMS') | ForEach-Object {
  $h, $n = $_ -split '\s+', 2
  if ($n -match "^looptrack_${Ver}_windows_$Arch\.exe$") {
    $f = Join-Path $D 'looptrack.exe'
    if ((Get-FileHash -Algorithm SHA256 -Path $f).Hash -ne $h) { throw "SHA-256 が一致しません: $n" } else { "OK $n" }
  }
}
$UserPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (($UserPath -split ';') -notcontains $D) { [Environment]::SetEnvironmentVariable('Path', "$UserPath;$D", 'User') }
```

`OK` が出ることを確かめてください。
PATH の変更が効くのは、新しく開いたターミナルと新しく起動したアプリだけです。
PowerShell を開き直してから次に進んでください。

> **Windows の `looptrack issue verify` には Git Bash が要ります。** 検証コマンドは `bash -c` で実行し、
> `cmd.exe` や PowerShell では代わりに実行しません。POSIX の書き方のコマンドが別の意味になってしまうからです。`verify` を使うなら
> Git for Windows（`winget install --id Git.Git -e`）を入れてください。無いと `verify.require_on_close` の
> プロジェクトで、「## 検証コマンド」節のあるイシューを閉じられません。見つかるかどうかは `looptrack doctor` で確かめられます。

### Windows（Git Bash）

Git Bash でも使えます。
取得は macOS / Linux の手順と同じで、`OS=windows` にしてファイル名の末尾に `.exe` を付けます。

```bash
VER=v1.0.0
OS=windows
ARCH=amd64     # ARM の PC は arm64
BASE="https://github.com/howashoji/looptrack/releases/download/$VER"
mkdir -p ~/.local/bin
TMP="$(mktemp -d)" && cd "$TMP"
curl -fsSL -O "$BASE/looptrack_${VER}_${OS}_${ARCH}.exe" -O "$BASE/SHA256SUMS"
grep -E " looptrack_${VER}_${OS}_${ARCH}\.exe\$" SHA256SUMS | sha256sum -c -
cp "looptrack_${VER}_${OS}_${ARCH}.exe" ~/.local/bin/looptrack.exe
```

Git Bash の `~/.local/bin` は PowerShell や AI から見えないことがあります。
AI から使うなら、PowerShell の手順で `%LOCALAPPDATA%\Programs\looptrack` に置くほうが確実です。

### 確かめる

```bash
looptrack version
```

### 署名と OS の警告

- macOS: 公式の macOS 版は配布元の Apple Developer ID で署名し、Apple の公証（notarization）も通しています。
  ブラウザで取得しても Gatekeeper に止められることはありません。
  フォークなど、自分でビルドしたものには署名が付きません。
- Windows: **Windows 版はまだ署名していません。**
  ブラウザで取得したものを起動すると、Microsoft Defender SmartScreen の「Windows によって PC が保護されました」が出ることがあります。
  上の手順で SHA-256 が `SHA256SUMS` と一致するのを先に確かめ、「詳細情報」→「実行」で進めてください。
  `Invoke-WebRequest` で取得して PowerShell から起動する場合、ふつうこの画面は出ません。
- `SHA256SUMS` には minisign の署名を付けています。リリースのページの `SHA256SUMS.minisig` がそれです。
  `looptrack self-update` はこの署名を確かめてから置き換えます。
  手で確かめるときは [minisign](https://jedisct1.github.io/minisign/) を入れ、`SHA256SUMS.minisig` を `SHA256SUMS` と同じ場所に落として次を実行します。

```bash
minisign -Vm SHA256SUMS -P RWRrJP/r1wfXKalGsxLnzFmmsExUd2azSJh4ccrYDJEBu8yE3N0ZlLJy
```

`Signature and comment signature verified` と出れば一致しています。

## 2. サーバを立ち上げる（`looptrack setup`）

サーバの置き場を作り、対話式のウィザードを実行します。

```bash
mkdir -p ~/looptrack-server
cd ~/looptrack-server
looptrack setup
```

PowerShell では次のとおりです。

```powershell
New-Item -ItemType Directory -Force -Path "$HOME\looptrack-server" | Out-Null
Set-Location "$HOME\looptrack-server"
looptrack setup
```

問いには次のように答えます。`[ ]` の中は既定値で、Enter だけ押せばそれが選ばれます。

| 問い | ローカルの 1 人利用での答え |
| -- | -- |
| ① 使い方 | ローカルの 1 人利用（既定） |
| ② 保存先 | SQLite（既定。ファイルは `<置き場>/im.db`） |
| ③ 待ち受け | ポート `8090`・URL の接頭辞 `/looptrack`（既定） |
| ④ 最初の管理者 | ログイン名 `admin`・表示名・パスワード（12 文字以上。2 回入力） |
| ⑤ 二段階認証 | 任意（ローカルの既定）。ローカルではログインを省くので影響しません |
| ⑥ 最初のプロジェクト | `-`。ここでは作らず、手順 4 で `demo` を作ります。slug を入れるとここで作られるので、手順 4 は飛ばせます |

最後に内容を確かめて `y` を入れると、次のものができます。

- `<置き場>/.env`: 設定です。秘密を含むので、他人に見せたり git に入れたりしないでください
- `<置き場>/im.db`: SQLite のデータです
- 最初の管理者

終わると起動のコマンド・ブラウザの URL・CLI のログイン方法・MCP の接続設定が表示されます。
**`.env` の `LOOPTRACK_SECRET_KEY` を失うと、全員の二段階認証が使えなくなります。** `.env` は別の場所にも控えてください。

途中で Ctrl-C を押してやめても、ファイルも利用者も残りません。
2 回目の `looptrack setup` は何も書き換えず、今の設定を表示するだけです。
作り直すときは `looptrack setup --force` を使います。

## 3. サーバを起動する

```bash
cd ~/looptrack-server
looptrack serve --env-file ./.env
```

PowerShell では `looptrack serve --env-file .\.env` です。
閉じるとサーバが止まるので、このターミナルは開いたままにしておきます。
ブラウザで http://127.0.0.1:8090/looptrack/ を開き、画面が出ることを確かめてください。
ローカルの 1 人利用ではログインの画面は出ません。

ローカルモードは `127.0.0.1` だけで待ち受けます。
**複数の人が使う PC では使わないでください。** 同じ PC のほかの利用者が認証なしで操作できてしまいます。

### チームのサーバの場合

- ① で「チームのサーバ」を選びます。保存先の既定は MySQL で、接続先は環境変数 `LOOPTRACK_SETUP_DSN` で渡します。
- ③ で公開 URL（例 `https://im.example.com`）を入れます。二段階認証は既定で必須です。
- `.env` と一緒に `compose.yaml` と、それが使う `Dockerfile`・`NOTICE` ができます。
  起動は `docker compose up -d` です。イメージは手元で作り、レジストリからは取ってきません。
  Linux ではウィザードが自分自身を `looptrack` として複製します。ほかの OS では `linux/amd64` の実行ファイルを先に置いてください。
- 外からは Nginx などのリバースプロキシで `/looptrack/` を受け、`127.0.0.1:8090` へ渡します。接頭辞 `/looptrack` は外さずに渡します。
- 手順 4 の管理コマンドは、`looptrack …` の代わりに `docker compose run --rm --no-deps looptrack …` で実行します（例 `docker compose run --rm --no-deps looptrack user list`）。
- `--yes` による非対話の実行や MySQL の権限の分け方は、[docs/server/DEPLOY.md](../../server/DEPLOY.md) の「新しく立ち上げる」にあります。

### まっさらな Linux サーバの場合（`install.sh`）

まっさらな Ubuntu LTS や Debian のサーバなら、手順 1〜3 を手で進める必要はありません。
スクリプト 1 つで、実行ファイルの取得・`looptrack setup`・systemd の unit か `compose.yaml` の作成・起動・動作確認まで進みます。

```bash
VER=v1.0.0
curl -fsSL -O "https://github.com/howashoji/looptrack/releases/download/$VER/install.sh"
less install.sh   # 実行する前に中身を読む
sudo sh install.sh --from "https://github.com/howashoji/looptrack/releases/download/$VER"
```

`--from` は実行ファイルの取得元です。インストーラは `SHA256SUMS` と署名を確かめてから入れます。
手元で作った配布物から入れるときは、そのディレクトリを渡してください（`sudo sh deploy/install.sh --from /path/to/dist`）。

インストーラはまず systemd と Docker compose のどちらで動かすかを聞き、続けて `looptrack setup` と同じことを聞きます。
終わると Nginx と Caddy の設定例・ブラウザの URL・CLI のログイン方法・MCP の接続設定が表示されます。設定例は `/etc/looptrack/proxy-examples.txt` にも書き出されます。
リバースプロキシを前に置き、公開 URL でログインしてから手順 4 に進みましょう。

保存先に MySQL を選び、`deploy/grants.sql` でアプリ用の DB 利用者に必要な権限だけを与える場合は順番に気をつけてください。表ごとの `GRANT` は表ができてからでないと流せません。

1. インストーラを実行します。ここで setup が表を作ります。
2. 管理用の資格情報で権限のファイルを流します。
3. インストーラをもう一度実行します。今度は起動と動作確認だけです。

インストーラは起動の前に、サービスと同じ利用者で保存先を読めるかを試します。読めなければ、起動しないサーバを待たずにこの順番を案内して止まります。

更新は `sudo sh install.sh --upgrade --from <取得元>` です（詳しくは[更新](updating.md)）。アンインストールは `--uninstall` で、`--purge` を付けると設定とデータも消えます。

## 4. プロジェクトを作り、自分を参加させる

ウィザードの質問 ⑥ でプロジェクトを作った場合は、この手順を飛ばしてください。
同じ slug をもう一度作ろうとすると失敗します。
ブラウザの画面（`/admin/projects` の「プロジェクトを作る」）から後で作ることもできます。

プロジェクトは管理コマンドで作ります。
管理コマンドは `.env` の `LOOPTRACK_DSN` を使って保存先につなぎます。
サーバを起動したターミナルとは別のターミナルで実行してください。

macOS / Linux / Git Bash:

```bash
cd ~/looptrack-server
set -a; . ./.env; set +a
looptrack project create demo --prefix DEMO --name "Demo" --description "Looptrack を試すプロジェクト"
looptrack member set demo admin --role admin
looptrack project list
```

PowerShell:

```powershell
Set-Location "$HOME\looptrack-server"
Get-Content .\.env | ForEach-Object { if ($_ -match "^([A-Z_]+)='(.*)'$") { Set-Item -Path "Env:$($Matches[1])" -Value $Matches[2] } }
looptrack project create demo --prefix DEMO --name "Demo" --description "Looptrack を試すプロジェクト"
looptrack member set demo admin --role admin
looptrack project list
```

- `--prefix`（ID の接頭辞）と `--width`（連番の桁数。既定 4）は**後から変えられません**。
- 管理者でも、参加していないプロジェクトは読むだけです。書くには `member set` で editor か admin として参加させます。
- ブラウザの http://127.0.0.1:8090/looptrack/ に `demo` が出れば成功です。

## 5. 利用者と権限（チームのサーバの場合）

ローカルの 1 人利用ではこの手順は要りません。
チームのサーバでは、管理者がブラウザで利用者を足します。

1. `<サーバの URL>/admin/users` を開き、ログイン名・表示名・初期パスワード・役割（admin / member）を入れて追加します。
2. 同じ画面の「プロジェクトの権限」で、プロジェクトに `editor`（起票・コメント・状態の変更）か `viewer`（閲覧のみ）を付けます。
3. 利用者に URL・ログイン名・初期パスワードを伝えます。利用者はログインし、二段階認証が必須なら認証アプリを登録します。パスワードは `<サーバの URL>/account` で変えられます。

詳しくは[管理者の手引き](admin.md)にあります。

## 6. CLI にログインする（`looptrack issue login`）

ローカルの 1 人利用ではこの手順は要りません。画面や AI の MCP と同じく、CLI もトークンなしでそのまま使えます。
ログインが要るのは、チームのサーバに向けるときです。

```bash
looptrack issue login --browser --url http://127.0.0.1:8090/looptrack
```

ブラウザが開くので、ログインして「許可」を押します。ローカルの 1 人利用ではログインの画面は出ません。
ブラウザが開かないときは、表示された URL を開いてください。
トークンは手元の設定ディレクトリに本人だけが読める形で保存されます。macOS と Linux では `~/.config/looptrack/credentials.json`、Windows では `%APPDATA%\looptrack\credentials.json` です。
それ以降は、期限が近づくと自動で更新されます。

ssh の先のようなブラウザの無い環境では、`<サーバの URL>/account` でアクセストークンを発行して次のコマンドに貼り付けます。

```bash
looptrack issue login --url http://127.0.0.1:8090/looptrack
```

## 7. プロジェクトに導入する（`looptrack issue init`）

作業するリポジトリで実行します。
まず `--dry-run` で何が書かれるかを見ておきましょう。

```bash
mkdir -p ~/work/demo-app
cd ~/work/demo-app
git init
looptrack issue init --project demo --url http://127.0.0.1:8090/looptrack --agent claude-code --mcp --dry-run
looptrack issue init --project demo --url http://127.0.0.1:8090/looptrack --agent claude-code --mcp --no-loop
```

- `--agent` には使う AI を指定します。`claude-code`・`codex`・`copilot`・`other` から選び、複数ならカンマで区切ります（例 `claude-code,codex`）。
- `--mcp` を付けると MCP の接続設定も書きます。Claude Code なら `.mcp.json` です。
- `--no-loop` は core だけを入れます。loop も入れるなら `--loop` にします。どちらも付けないと対話で尋ねられます。選び方は [AI ごとの手引き](ai-agents.md) にあります。
- 既存の設定は消さずにマージします。変えたファイルは `.claude/.looptrack-init-backup/<時刻>/` に控えます。
- 導入の後に自己診断が走り、失敗したら書いたものを元に戻します。
- 再実行しても壊れません。2 回目は「変更はありません」と出ます。

Claude Code の場合、主に次のものが入ります。

| もの | 中身 |
| -- | -- |
| `.claude/settings.json` の `env` | `LOOPTRACK_API_URL` と `LOOPTRACK_PROJECT`。トークンは書きません |
| `.claude/settings.json` の hooks | セッション開始の要約・鮮度ガード・トークン計測（`looptrack hook …`） |
| `.claude/skills/issue/` | skill `/issue` |
| `CLAUDE.md` | `<!-- looptrack:begin -->` から `<!-- looptrack:end -->` までの案内の節 |
| `.mcp.json` | MCP の接続設定（`--mcp` のとき） |

PATH で `looptrack` が見つからないときは hook を絶対パスで配線し、その配線を手元専用の `.claude/settings.local.json` に書きます。
配線と PATH は次のコマンドで確かめられます。

```bash
looptrack doctor
```

## 8. MCP を接続する

`init --mcp` を使った場合は、Claude Code の接続設定はもうできています。
手で足すときは次のとおりです。

```bash
claude mcp add --transport http looptrack http://127.0.0.1:8090/looptrack/mcp --header "X-Looptrack-Project: demo"
```

`.mcp.json` に書く場合は次の形です。

```json
{ "mcpServers": { "looptrack": { "type": "http", "url": "http://127.0.0.1:8090/looptrack/mcp",
  "headers": { "X-Looptrack-Project": "demo" } } } }
```

Claude Code を起動し直します。
hook の確認を求められたら、内容を見て承認してください。
チームのサーバでは Claude Code の `/mcp` で `looptrack` を選び、ブラウザで許可します。
Codex・Copilot の接続は [AI ごとの手引き](ai-agents.md) にあります。

## 9. 最初の 1 周

まず CLI でつながっていることを確かめます。

```bash
cd ~/work/demo-app
export LOOPTRACK_API_URL=http://127.0.0.1:8090/looptrack LOOPTRACK_PROJECT=demo
looptrack issue config
looptrack issue guide
```

2 つの環境変数を自分で設定するのは、`init` がこれを `.claude/settings.json` に書くからです。
そこに書いた値は AI のセッションにしか渡らず、ふつうのターミナルからは見えません。
`config` にプロジェクト `demo` と自分の権限が出れば成功です。

次に最初のイシューを起票し、1 周を手で回してみます。

```bash
looptrack issue new "README に概要を書く" --type task --body "$(cat <<'EOF'
README.md にこのリポジトリの概要を 3 行で書く。

## 受け入れ条件

- [ ] README.md がある
- [ ] 概要が 3 行ある

## 検証コマンド

- `test -f README.md`
- `test "$(grep -c . README.md)" -ge 3`
EOF
)"
looptrack issue next
```

`next` が `DEMO-0001` を In Progress にして、本文と受け入れ条件を表示します。
ここからは AI に任せましょう。
Claude Code を開いて、次のように頼みます。

> イシューの next から 1 周回して。受け入れ条件を検証してから close して。

AI は作業を進め、`verify` で検証コマンドを実行して結果を残し、`close` します。
最後に手元でも結果を確かめます。

```bash
looptrack issue show DEMO-0001
looptrack issue verify DEMO-0001 --last
looptrack issue summary
```

`show` にコメントと Done の状態が出ていれば、最初の 1 周は完了です。
ブラウザの http://127.0.0.1:8090/looptrack/p/demo/ でも同じイシューを見られます。

PowerShell ではヒアドキュメントが使えません。本文をファイルに書いてから `--body (Get-Content -Raw body.md)` で渡してください。

## 10. 表示の言語（日本語 / 英語）

文面は日本語か英語で表示されます。`LOOPTRACK_LANG` で指定するか、端末とブラウザの設定に任せます。

```bash
LOOPTRACK_LANG=en looptrack issue list   # このコマンドだけ
export LOOPTRACK_LANG=ja                 # このシェルの間
```

| 順 | コマンドライン | 画面・MCP |
| -- | -- | -- |
| 1 | `LOOPTRACK_LANG` | URL の `?lang=ja` / `?lang=en`（その要求だけ） |
| 2 | `LC_ALL` → `LC_MESSAGES` → `LANG` | `LOOPTRACK_LANG`（CLI が明示の指定として送る） |
| 3 | 英語 | `/account` で選ぶ「表示の言語」（設定なしにもできる） |
| 4 | — | `Accept-Language` |
| 5 | — | 英語 |

`/account` の「表示の言語」を選んでおくと、ヘッダを送れない MCP の接続でもその言語で返ります。
「設定なし」に戻せば、これまでどおりブラウザや端末の設定に従います。

MCP の `instructions`・`guide` の本文・ツールの説明といった AI が読む文も、接続ごとに同じ順で言語が決まります。
プロジェクトに配る rules は日英の両方を置き、hook が実行時に上と同じ順で選びます。
skill は `looptrack issue init` が導入時の言語の 1 本だけを置きます。言語を変えたら `looptrack issue init` をもう一度実行してください。
