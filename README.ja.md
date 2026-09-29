# Looptrack

*English: [README.md](README.md)*

**Looptrack は、AI コーディングエージェントの外部記憶となり、ループエンジニアリングを実現するイシュー管理ツールです。**
プロジェクト本来のイシューとは別に、AI が分解した作業単位の課題を記録します。
AI の作業計画と意思決定を可視化し、各作業のトークンも記録して分析レポートを出力できます。

これが大事な理由は 4 つあります。

- **AI コーディングエージェントの外部記憶**: バイブコーディングで AI が覚えていられるのは、コンテキストウィンドウに収まる範囲の作業内容だけです。
  それを超える量は覚えておけません。
  そのためセッションをまたぐと、同じ作業を繰り返したりやるべき作業を飛ばしたりすることがあります。
  だから AI の外に記憶を置く必要があります。Looptrack は AI 専用のタスクの記憶装置として生まれました。
  どのセッションもどの AI も同じイシューを読むので、前のセッションで決めたことが次のセッションに残ります。
- **ループエンジニアリングの実現**: ここでいうループエンジニアリングとは、イシューを起点に「着手 → 作業 → 検証 → クローズ」を AI と人が同じ記録の上で回していくやり方です。
  いちばんの利点は、個人のバイブコーディングから一歩進んで**チーム開発も見据えた**形でこれを回せることです。
  人も同じイシューをブラウザで見て判断するので、チームで 1 つのループを共有できます。
- **プロジェクト本来のイシューとは別に、AI が分解した作業単位を記録**: 人が話し合う要望や不具合の報告は、プロジェクトがふだん使っている置き場に残します。
  Looptrack が持つのは、AI がその仕事を分解した作業単位です（要件・設計・タスク・作業中に見つけた不具合・テスト）。
  親子と traces でつなぎ、1 件ずつ着手して根拠を添えて閉じられる大きさにします。
- **作業計画・意思決定・トークンの可視化**: 作業計画はイシューそのものです（本文・受け入れ条件・下位のタスク）。
  見つけた原因・下した判断・検証の結果は、あとから直したり消したりできないコメントとして積み上がります。
  人の判断が要るものは In Review に集まり、人はそのすべてをブラウザで読めます。
  各作業で消費したトークンも記録し、分析レポート（PDF）として出力できます。

具体的には、AI と人のループを回すためのイシュー管理ツールです。
複数のプロジェクトのイシューを 1 つのサーバで管理します。
Claude Code・Codex・GitHub Copilot といったコーディング AI には CLI・リモート MCP・hook でイシューを渡します。

サーバは Go の単一バイナリで、データは MySQL か SQLite に入ります。
イシューを管理対象のリポジトリにコミットすることはないので、成果物に管理番号が残りません。

## なぜ別のトラッカーなのか

プロジェクト本来のトラッカーには、人が求めることが入っています。
Looptrack は、AI にとってのもう 1 つの置き場です。その仕事を 1 件ずつ取れる単位に分けて置き、ルールを守らせる仕組みを備えています。
ルールを守らせる仕組みさえあれば、AI はループを自分で回せるからです。

Looptrack では AI が「起票 → 着手（`next`）→ 作業 → 検証 → クローズ → 次へ」を自分で進めます。
外れてはいけないところはサーバが守ります。採番・コメントとイベントの append-only・クローズしたイシューの不変・プロジェクトごとのルールです。
各段階のトークン消費も記録します。

ループは速さの違う 3 つが入れ子になっています。
この整理は Andrew Ng によるもので、設計は [DESIGN.md](docs/server/DESIGN.md) にまとめています。

- ① AI の作業ループ（数分）: AI が 1 周を自分で回します。`verify` がイシューの検証コマンドを実行して結果を残すので、閉じてよいかは機械が判定します。
- ② 人の判断のループ（数十分〜数時間）: 判断が要るものは In Review に集まり、`summary` に出続けます。AI は次に進む前にそれを人に尋ねます。返答はイシューに残ります。
- ③ 外からの反応のループ（数時間〜数週）: 利用者やテスターの声を印付きのコメントでイシューに戻します。応答するまで summary に残ります。

導入すると次のものが使えます。

- core: 最小のループ
- loop: 作業の規律（入れるかどうかは選べます）
- Claude Code との連携
- トークン計測

core だけでもセッションの冒頭に要約が入ります。中身はいまの周回・人の判断待ち・外からの反応の 3 つです。
`next` でイシューに着手し、見ていたイシューを更新しないまま終えようとすると差し戻されます。トークン消費は段階ごとに貯まります。

loop を入れると、「確認して」と頼んだときは編集を止めます。ツール呼び出しの書式の誤りも差し戻します。
作業が終わるたびに引き継ぎメモの更新を求め、コンテキストの大きさや放置された背景プロセスも見張ります。
別のリポジトリを変えるときは先に確認します。一覧は [kit/README.ja.md](kit/README.ja.md) にあります。

ただしガードが捕まえるのはうっかりミスだけです。
入れ子のシェル・`eval`・`sudo` などの前置は 1 段だけほどいて判定します。
コマンド置換や一覧にないラッパで包んだコマンドは素通りすることがあります。
既知の限界は一覧の表と [secrets-discipline.md](kit/loop/rules/secrets-discipline.md) にまとめています。

Claude Code ならプロジェクトの `.claude/settings.json` に 2 行足すだけでサーバにつながります。
Codex と Copilot の設定も同じ manifest から作ります。
トークンの消費はセッション・AI・段階・イシューごとに集計し、PDF のレポートにできます（[トークンレポート](#トークンレポート)）。

## 始め方

入口は 3 つあります。使い方に合うものを 1 つ選んでください。どれを選んでもサーバ・ブラウザ・AI 用の CLI がそろいます。

### 1. 手元の PC で使う — デスクトップ版

1 台の PC で 1 人で使うならデスクトップ版がいちばん手軽です。ターミナルは要りません。
[リリースのページ](https://github.com/howashoji/looptrack/releases) から OS に合うファイルを落とし、`SHA256SUMS` と照らし合わせてからダブルクリックしてください。

| OS | ファイル |
| -- | -- |
| macOS 13 以降（Apple シリコン・Intel） | `Looptrack_<版>_macos_universal.dmg` |
| Windows 10 / 11 | `Looptrack_<版>_windows_amd64_setup.exe`（インストーラ）か、入れずに使う `.zip` |
| Linux（x86_64 / aarch64） | `Looptrack_<版>_linux_x86_64.AppImage` |

起動すると既定のブラウザに初回設定の画面が開きます。管理者・二段階認証・最初のプロジェクトをここで決めます。
サーバは 127.0.0.1 だけで待ち受け、データは SQLite のファイル 1 つに入ります。管理者権限はどの OS でも要りません。

タスクトレイのアイコンからは、画面を開く・MCP の接続設定をコピーする・CLI を使えるようにする・ログイン時に起動するといった操作ができます。
macOS ではメニューバーにアイコンが出ます。

OS ごとの初回起動は [デスクトップ版](docs/guide/ja/desktop.md) で説明しています。

### 2. Linux サーバに入れる — `install.sh`

Ubuntu LTS や Debian なら、1 行でインストーラ（POSIX sh のスクリプト）が走り、
取得 → 照合 → 展開 → `looptrack setup` →（MySQL の権限）→ 起動 → 動作確認まで進みます。
systemd の unit か compose.yaml も作り、リバースプロキシの設定例も表示します。

```sh
curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh
```

インストーラは GitHub Releases の最新のリリースから `looptrack_<版>_linux_<arch>_server.tar.gz` を取り、
`SHA256SUMS` で照合してから展開し、中の `looptrack` を置きます。サーバに `minisign` があれば `SHA256SUMS` の署名も確かめます
（`--require-signature` で必須にできます）。
スクリプト自身は HTTPS で取るだけで、自分を照合できません（照合の鍵と手順をスクリプトが持っているためです）。
実行する前に中身を読むなら、次のようにします。

```sh
curl -fsSL -o install.sh https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh
less install.sh
sudo sh install.sh                      # 版を固定するなら sudo sh install.sh --version v1.0.0
```

1 行の形でオプションを付けるときは `sh -s --` の後ろに置きます（例: `… | sudo sh -s -- --version v1.0.0`）。
`deploy/release/dist.sh build` で手元に作った配布物から入れるときは、`--from` にその出力先のディレクトリを渡します。

```sh
sudo sh deploy/install.sh --from /path/to/dist
```

スクリプトはまず systemd と Docker compose のどちらで動かすかを聞きます。そのあとは `looptrack setup` と同じ質問です。
終わると nginx と Caddy の設定例・ブラウザの URL・CLI のログイン方法・MCP の接続設定が表示されます。
設定例は `/etc/looptrack/proxy-examples.txt` にも保存されます。あとはプロキシを立てて公開 URL からログインしましょう。

MySQL を使い、アプリ用の DB 利用者を [deploy/grants.sql](deploy/grants.sql) で最小権限にする場合も、同じ 1 回の実行で進みます。
表ごとの `GRANT` は表ができてからでないと流せないので、setup が表を作った後で、インストーラが MySQL の管理用の資格情報を端末で尋ねます
（表示しません。保存しません）。アプリ用の利用者が無ければ作り、`looptrack` に埋め込んだ権限を与え（中身は `looptrack grants print` で見られます）、
アプリ用の利用者で読めることを確かめてから起動します。資格情報が合わなければ、何も起動せずに止まります。もう一度実行すると、尋ねるところから続きます。
DB がまだ無ければ、setup が同じ管理用の資格情報を先に尋ね、作ってよいかを確かめてから DB も作ります（尋ねるのは 1 回だけです）。
ただし、表を作る利用者（`LOOPTRACK_SETUP_MIGRATE_DSN`）と、その DB への `GRANT` は先に用意しておきます（無ければ従来どおり接続で止まります）。
作らないと答えると何も作らずに止まり、自分で流す `CREATE DATABASE` の文を示します。

インストーラは Linux 専用です。macOS や Windows でサーバを動かすときは、上のデスクトップ版を使ってください。

更新は `curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh -s -- --upgrade` です。
1.0.0-rc.1・rc.2 の `install.sh` で入れたサーバも同じ 1 行で上げられます。
アンインストールは `--uninstall` で、`--purge` を付けると設定とデータも消えます。
オプション・取得元の決まり・確かめ方は [DEPLOY.md](docs/server/DEPLOY.md) を見てください。

### 3. Docker があるところで — compose

`looptrack setup` で「チームのサーバ」を選ぶと、`compose.yaml` とそれが使う `Dockerfile`・`NOTICE` が書き出されます。
ウィザードは同じで、行き先がコンテナになるだけです。

```sh
looptrack setup            # 「チームのサーバ」を選び、MySQL か SQLite を選ぶ
docker compose up -d       # 隣に書かれた Dockerfile からイメージを作って起動する
```

イメージは `scratch` に静的リンクの実行ファイルを 1 本置いただけで、約 28MB です。シェルも curl も入っていません。
127.0.0.1 にだけ公開し、read_only・非 root で動かします。権限は落とし、メモリにも上限を付けます。

レジストリからは何も取ってきません。`compose.yaml` の隣に置いた実行ファイルからイメージを作ります。
Linux ではウィザードがそこへ自分を複製します。macOS と Windows では `linux/amd64` の `looptrack` を先にそこへ置いておいてください。
ウィザードも最後にそう案内します。詳しくは [DEPLOY.md](docs/server/DEPLOY.md) を見てください。

### ウィザードそのもの

どの入口でもサーバの設定は `looptrack setup` で行います。

```bash
looptrack setup
```

聞かれるのは次の 6 つです。

1. 使い方: ローカルで 1 人で使うか、チームのサーバか
2. 保存先: SQLite か MySQL
3. 待ち受け
4. 最初の管理者
5. 二段階認証を必須にするか任意にするか
6. 最初のプロジェクト: slug・ID の接頭辞・表示名。`-` と答えれば作らずに進み、あとから画面で作れます

答え終わると `.env` と最初の管理者ができます。チームのサーバなら `compose.yaml`・`Dockerfile`・`NOTICE` も書き出されます。
最後にブラウザの URL・CLI のログイン方法・MCP の接続設定が表示されます。

ローカルで 1 人で使う場合は `looptrack serve --env-file ./.env` で起動してください。127.0.0.1 だけで待ち受け、ログインは省かれます。
`--yes` での非対話の実行や中断・2 回目の実行の扱いは [DEPLOY.md](docs/server/DEPLOY.md) にあります。

### 次に読むもの

利用者ガイドは[日本語](docs/guide/ja/README.md)と[英語](docs/guide/README.md)があります。
まずは [概念](docs/guide/ja/concepts.md) → [始め方](docs/guide/ja/getting-started.md) → [日々の使い方](docs/guide/ja/daily-use.md) の順に読んでみてください。

### 表示の言語

CLI・ウィザード・画面・エラー・hook の文面・デスクトップ版のトレイは、日本語か英語で表示されます。
言語は次の順で決まり、どれにも当たらなければ英語です。

| 順 | コマンドライン | 画面・MCP |
| -- | -- | -- |
| 1 | `LOOPTRACK_LANG`（`ja` / `en`） | URL の `?lang=ja` / `?lang=en`（その要求だけ） |
| 2 | `LC_ALL` → `LC_MESSAGES` → `LANG` | `LOOPTRACK_LANG`（CLI が明示の指定として送る） |
| 3 | 英語 | `/account` で選ぶ「表示の言語」（設定なしにもできる） |
| 4 | — | `Accept-Language` |
| 5 | — | 英語 |

`/account` で言語を選んでおけば、ヘッダを送れない MCP の接続でもその言語で返ります。
端末で `LOOPTRACK_LANG` を指定した場合はそちらが優先です。

AI だけが読む文も、接続ごとに同じ順で言語が決まります。
MCP の `instructions` や `guide` の本文・ツールと入力項目の説明がこれに当たります。
プロジェクトに配る rules は日英の両方を置き、hook が実行時に選びます。
skill は実行時に選べないので、`looptrack issue init` が導入時の言語の 1 本だけを置きます。
言語を変えたら `looptrack issue init` をもう一度実行してください。

## サーバが提供するもの

| URL | 内容 |
| -- | -- |
| `https://example.com/looptrack/` | プロジェクト選択（ログイン必須） |
| `https://example.com/looptrack/p/<slug>/` | ボード / 一覧 / トレース + 詳細（起票・状態の変更・コメント・担当の変更の最小限のフォーム） |
| `https://example.com/looptrack/account` | アカウント設定（アクセストークンの発行・失効、パスワード変更） |
| `https://example.com/looptrack/admin/users` | 利用者管理（管理者） |
| `https://example.com/looptrack/api/v1/` | REST API（Bearer トークン） |
| `https://example.com/looptrack/mcp` | リモート MCP（OAuth 2.1 またはトークン） |

公開 URL も接頭辞の `/looptrack` も設定で変えられます。

サーバは Go の単一バイナリで、127.0.0.1 で待ち受けます。外からは前段のリバースプロキシが接頭辞の下を転送します。
動かし方はコンテナか systemd、DB は MySQL 8.4 か SQLite です。

Web のログインは ID・パスワードと TOTP です。TOTP を全員に必須にするかどうかは管理者が決めます。
CLI と MCP は利用者ごとのアクセストークンを使います。トークンには期限と失効があり、最後に使った時刻も記録されます。

データの正本は DB です。Markdown への定期的な書き出しはしないので、バックアップは DB のダンプで取ってください。
`looptrack export` を使えばいつでも Markdown に書き出せます。データが閉じ込められることはありません。

### 画面の説明

プロジェクト選択の画面には権限のあるプロジェクトが 1 行の説明つきで並びます。

プロジェクトを開くと、1 本のバーで同じイシューの見方を 3 つに切り替えられます。
状態ごとの列に並べるボード・絞り込みと並べ替えができる一覧・イシュー同士のつながりをたどるトレースです。
イシューを選ぶと横に詳細が開きます。本文・時系列のコメント・イベント・検証コマンドとその最後の結果が見られます。

ブラウザは閲覧が中心です。書き込めるのは担当者の変更と、起票・状態の変更・コメントの追記の最小限のフォームだけです。
フォームはターミナルを使わない人の入口で、CLI・MCP・API と同じサーバの操作を通るので、どこから変えても同じルールが効きます。
本文の編集は画面にはありません。それを含むほかの変更は CLI・MCP・API から行います。

## CLI

各プロジェクトでは `looptrack issue <サブコマンド>` の形で使います。
導入は `looptrack issue init` を 1 回実行するだけです。詳しくは [ADD-PROJECT.md](docs/ADD-PROJECT.md) を見てください。
つなぐサーバとプロジェクトは `LOOPTRACK_API_URL` と `LOOPTRACK_PROJECT` で決まります。ふつうはプロジェクトの `.claude/settings.json` の `env` に書きます。

`looptrack issue login --browser` でブラウザからログインすれば、あとはトークンが自動で更新されます。
トークンは `~/.config/looptrack/credentials.json` に権限 600 で保存されます。Windows では `%APPDATA%\looptrack\credentials.json` です。

CLI も hook も同じ 1 つの実行ファイルで、導入先にはそれ以外に何も置きません。別の処理系も bash も要らず、どの OS でも同じです。
AI からの使い方は [AI-GUIDE.md](docs/AI-GUIDE.md) で説明しています。

Windows では 1 つだけ注意してください。`looptrack issue verify` は検証コマンドを `bash -c` で実行し、`cmd.exe` や PowerShell では代わりに実行しません。
そのため Git Bash が必要です。`winget install --id Git.Git -e` で入れられます。見つかるかどうかは `looptrack doctor` で確かめられます。

## イシュー鮮度ガード

イシューを見ながら作業したのに、一度も更新しないままセッションを終えてしまう。鮮度ガードはこれを止めます。
次のセッションが古いままのイシューを信じて読み始めないようにするためです。

| フック | 呼び方 | 役割 |
|---|---|---|
| `UserPromptSubmit` | `looptrack hook issue-freshness-mark` | ユーザー発話に出たイシュー ID を記録 |
| `PostToolUse`（`Bash\|Read\|Edit\|Write\|NotebookEdit`） | `looptrack hook issue-freshness-mark` | 参照したイシュー ID と、実作業（ファイル変更・`git commit`）を記録 |
| `Stop` | `looptrack hook issue-freshness-check` | 実作業があるのに未更新のイシューが残っていれば **停止を差し戻す** |

判定の基準は、このセッション中に一度でも更新したかどうかです。サーバの更新イベントを見て判定します。サーバにつながらないときは止めません。
抜け道の `looptrack issue-freshness ack` と `reset` は [AI-GUIDE.md](docs/AI-GUIDE.md) で説明しています。

フックが受け取るのはコマンドの文字列です。書かれた語がそのまま実行されるとは限りません。
ヒアドキュメントの本文や引用符の中はデータとして扱います。実際に実行される語を取り出す共通部品は `internal/client/hook/hookcmd` にあります。

## トークンレポート

**AI がイシューごとに使ったトークンを、人が手で記録しなくても測れます。**
イシューへの変更操作（CLI・MCP）の直後と、ターン・セッションの終わりに、`looptrack` が手元にある AI の会話記録を読み、
トークンの累計をサーバへ送ります。人が打った指示文は既定では送りません。`LOOPTRACK_USAGE=0` で送信を止められます。

サーバは会話の区間を、その区間を閉じた操作のイシューに帰属させ、好きな期間でイシュー・ラベル・段階・AI・会話ごとに集計します。
プロジェクトのボードから AI にレポートを依頼すると、skill `token-report` が本文を書き、手元で `looptrack report pdf` を使って PDF を作り、
追記のみの台帳に登録します。次の「前回以降」はこの台帳から始まります。PDF をサーバに置くことはありません。

何が記録され、どう帰属させ、レポートをどう作って登録するかは [トークンレポート](docs/guide/ja/token-report.md) にあります。

## リポジトリの構成

```
looptrack/
  cmd/looptrack/       単一の実行ファイル（クライアント: issue・hook・report・desktop …
                       サーバ: serve・setup・migrate・user/member/token/project …）
  internal/
    domain/            イシュー・状態・ready・matrix・並び順・プロジェクト別ルール
    mdformat/          Markdown ⇔ モデル（往復一致）
    store/             DB の層
    service/           REST・MCP・Web が共通で通るドメイン操作（next の着手規則を含む）
    guide/             AI 向けの使い方（共通規則 + プロジェクト別ルール + 運用文書）の組み立て
    server/            HTTP（REST・MCP・OAuth・ログイン・閲覧画面・アカウント設定・利用者管理）
    auth/              パスワード（argon2id）・TOTP・トークン・暗号化
    transfer/          取り込み・書き出し・照合
    client/            CLI・hook・usage の収集・report pdf・self-update・desktop
  migrations/          SQL（配布済みのファイルは変更しない）
  deploy/              イメージ（Dockerfile・build.sh）・install.sh・grants.sql・
                       rules/（プロジェクト別ルールの例）・release/・dev/（ローカル MySQL）
  kit/                 各プロジェクトへ配る rules・skill と hook の配線
  docs/                guide・AI-GUIDE・ADD-PROJECT・projects/・templates/・server/
  NOTICE               配布物に添える第三者のライセンス文（生成物。go run ./internal/tools/notice が作る）
```

## ドキュメント

| 目的 | ファイル |
| -- | -- |
| 日々の使い方 | [利用者ガイド](docs/guide/ja/README.md)（[English](docs/guide/README.md)） |
| AI のトークン消費を測り、レポートを作る | [トークンレポート](docs/guide/ja/token-report.md) |
| **別プロジェクトからイシューを操作する**（AI はまずこれ） | [docs/AI-GUIDE.md](docs/AI-GUIDE.md) |
| **新しいプロジェクトを載せる** | [docs/ADD-PROJECT.md](docs/ADD-PROJECT.md) |
| プロジェクトの運用ルールを書く | [docs/projects/](docs/projects/) と [docs/templates/](docs/templates/) のひな形 |
| サーバの仕組みを知る | [docs/server/DESIGN.md](docs/server/DESIGN.md) |
| サーバを配置・更新・運用する | [docs/server/DEPLOY.md](docs/server/DEPLOY.md) |
| 各プロジェクトへ何を配るか・なぜ汎用か | [kit/README.ja.md](kit/README.ja.md) |
| リリース物を作る | [docs/server/RELEASE.md](docs/server/RELEASE.md) |
| コードで貢献する | [CONTRIBUTING.md](CONTRIBUTING.md) |
| 脆弱性を報告する | [SECURITY.md](SECURITY.md) |
| 変更履歴を見る | [CHANGELOG.md](CHANGELOG.md) |

## 配布物と署名

- 公式の macOS 版は Developer ID で署名し、Apple の公証も通しています。Gatekeeper の「開発元を確認できない」は出ません。
  `codesign -dvv <ファイル>` で見ると、Authority は `Developer ID Application: HOWA SHOJI K.K.` です。
- **Windows 版はまだ署名していません。** 初回の起動で SmartScreen の「Windows によって PC が保護されました」が出ることがあります。
  SHA-256 が `SHA256SUMS` と一致するのを確かめてから、「詳細情報」→「実行」で進めてください。
- `SHA256SUMS` には minisign の署名 `SHA256SUMS.minisig` を付けています。公開鍵は
  [deploy/release/minisign.pub](deploy/release/minisign.pub) で、鍵 ID は 29D707D7EBFF246B です。
  `looptrack self-update` はこの署名を確かめてから置き換えます。手で確かめるなら
  `minisign -Vm SHA256SUMS -P RWRrJP/r1wfXKalGsxLnzFmmsExUd2azSJh4ccrYDJEBu8yE3N0ZlLJy` を実行してください。
- 配布物には、依存モジュール・Go・同梱フォントのライセンス文をまとめた `NOTICE` を添えています。
  実行ファイル・.app・dmg・AppImage・Windows の zip・コンテナイメージの `/NOTICE` です。`looptrack licenses` でも表示できます。
- 自分でビルドしたものには署名が付きません。署名の鍵は配布元だけが持っています。ビルドの手順は [RELEASE.md](docs/server/RELEASE.md) にあります。

## 貢献

不具合の報告や要望は GitHub Issues へどうぞ。脆弱性だけは Issues に書かず、[SECURITY.md](SECURITY.md) の手順で知らせてください。
開発環境やテストの回し方・コミットと PR の作法は [CONTRIBUTING.md](CONTRIBUTING.md) にあります。

## ライセンス

MIT ライセンスです。本文は [LICENSE](LICENSE) にあります。
Copyright (c) 2026 HOWA SHOJI K.K. で、公式の配布物もここが配布しています。
第三者の部品はそれぞれのライセンスに従います。文面は [NOTICE](NOTICE) にまとめています。
