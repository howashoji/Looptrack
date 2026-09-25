# Looptrack サーバ設計

複数のプロジェクトのイシューを 1 つのサーバ（Go の単一バイナリ・MySQL または SQLite）で管理し、
コーディング AI に CLI・リモート MCP・hook で渡し、人がブラウザで読む。**この文書は現在の仕様を述べる。**
製品としての説明は [README.md](../../README.md)、AI からの使い方は [AI-GUIDE.md](../AI-GUIDE.md)。

## 1. 概要

### 1-1. 設計の前提

| 項目 | 決めていること | 理由 |
| -- | -- | -- |
| 配置 | 1 台のサーバのサブパスに置きます（例 `https://example.com/looptrack/`）。コンテナか systemd で 127.0.0.1:8090 に待ち受け、前段のリバースプロキシから受けます | 既存のサイトと 1 台を共有でき、TLS はプロキシに任せられるからです |
| DB | MySQL 8.4 か SQLite です（§3-3）。MySQL は共有のサーバに DB を 1 つ作る形でもかまいません | 共有の MySQL があれば DB を増やすだけで済みます。1 人で使うならファイル 1 つで足ります |
| 言語 | **Go**（単一バイナリ・静的リンク） | メモリ 1GB のサーバでも常駐のコストが小さく、MCP の公式 Go SDK もあるからです |
| Web 認証 | ID / パスワード（argon2id）+ TOTP（RFC 6238）です。TOTP を全員に**必須**にするか**任意**にするかは、システム全体の設定で決めます（§2-1「二段階認証の設定」） | 必須にできない事情のある現場でも、登録した人には確認を求められます |
| CLI / MCP 認証 | 利用者ごとのアクセストークンです。MCP は OAuth 2.1 にも対応します。CLI も `looptrack issue login --browser` で同じ OAuth を使い、更新トークンで自動更新します（§3-2） | Claude Code の `.mcp.json` のヘッダ（環境変数の展開）とブラウザでの認可の両方で使えるようにするためです |
| 正本 | **DB が正本です。Markdown への定期的な書き出しはしません**。`export` は一時的な出力にだけ使い、バックアップは DB のダンプで取ります | 2 つの正本を同期し続ける仕組みを持たずに済むからです |
| Web 画面の範囲 | 操作の主な入口は CLI / MCP で、画面は閲覧が中心です。書き込みは**担当者の変更**と、**起票・状態の変更・コメントの追記の最小限のフォーム**だけです。フォームはターミナルを使わない利用者の入口で、既存の REST API を呼びます。本文の編集は画面にありません。アカウントの操作（パスワード変更・アクセストークンの発行と失効・管理者の利用者管理）は Web でできます（§9-2・§7） | 本文の編集を画面にも持つと、同じ規則を 2 か所で守ることになるからです |
| クライアント側の言語 | CLI・hook・トークンの送信・レポートの PDF も **Go** です（実行ファイルは `looptrack` 1 つ・§5-1） | Windows とデスクトップ版で、手元の依存を実行ファイル 1 つにするためです |

### 1-2. 全体構成

```
Claude Code（各プロジェクト）                         example.com（ホスト Nginx・TLS）
  ├ looptrack issue <サブコマンド>       ──HTTPS + PAT──▶ /looptrack/api/v1/…   ┐
  ├ hooks（CLI --json / activity）                                     │ looptrack serve（Go・コンテナ）
  └ MCP（.mcp.json: /looptrack/mcp） ──HTTPS + PAT/OAuth──▶ /looptrack/mcp          │   127.0.0.1:8090
ブラウザ ──HTTPS + セッション Cookie（Path=/looptrack）────────▶ /looptrack/…        ┘      │ network: mysql
                                                                             ▼
                                                                     MySQL 8.4（DB im）
```

- **単一バイナリ `looptrack`**: サーバの機能は `serve` / `setup` / `migrate` / `import` / `export` / `verify` / `user` / `token` / `project` などのサブコマンドです。クライアントの `issue` / `hook` などと同じ実行ファイルに入っています。
- **ベースパス（URL の接頭辞）の下に作ります**。既定は `/looptrack` で、`LOOPTRACK_BASE_PATH` で変えられます。既存の配置では `/im` を設定しています。リバースプロキシでは接頭辞を剥がさないでください。本書の経路は既定の `/looptrack` で書きます。
- メモリの目標は常駐 30MB 以下です（`GOMEMLIMIT=64MiB`、コンテナは `mem_limit: 96m`）。
- イメージは手元で `linux/amd64` の静的リンクの実行ファイルから作ります（`deploy/build.sh`）。サーバではビルドしません。

### 1-3. Claude Code との親和性（設計の中心）

| 原則 | 具体策 |
| -- | -- |
| **手順を壊さない** | CLI の**サブコマンド・位置引数・フラグ・出力文字列・終了コード**は、版を上げても変えません。ゴールデンテストで全件を比べます。hook がコマンド文字列の形（`status <ID> "<状態>"`、`close <ID> --comment`）を正規表現で見ているからです |
| **規則はサーバが強制** | 採番・append-only・クローズ済みの不変・プロジェクト別ルール（状態の禁止など）はサーバで検証します。hook の文字列解析によるすり抜け（`new --status Done` など）と誤検知を無くすためです。拒否の理由は「次に何をすべきか」を含む日本語で返します（CLI は `エラー: …` / exit 1） |
| **機械可読** | `list` / `ready` / `show` に `--json` があります。bug の件数・コメントの件数・ラベルの検索を hook が読むためです |
| **本文編集は Read / Edit のまま** | `looptrack issue edit <ID>` が作業コピー（`.claude/.looptrack-work/<ID>.md`・gitignore 済み）を書き出します。Claude はそれを Edit で直し、`looptrack issue push <ID>` で版番号つきで更新します。競合したら 409 と差分を表示します |
| **git 操作を無くす** | pull / commit / push / counter / 一括 stage の注意とガードが要らなくなります |
| **鮮度ガードはイベントログで判定** | `looptrack issue activity <ID…> --since <epoch> --json` がサーバの更新イベント（秒精度）を返します。これで mtime による判定を置き換えます |
| **MCP** | 同じドメインロジックを MCP ツールとして公開します。hook は `mcp__looptrack__*` のツール名と構造化された入力で判定できます |
| **起動を止めない** | SessionStart 用の `looptrack hook summary` は 1 リクエストだけで 2 秒で打ち切り、失敗しても止めません。止めないのは**フックの経路だけ**です。CLI は `--agent` / `--hook-json` が付いたときに黙り、これを付けるのは init の配線です。人が手で打つ `looptrack issue summary` は、サーバの設定が無い・ログインしていない・届かないときに `list` / `ready` / `show` と同じ案内を出して exit 1 で終わります。無出力で exit 0 だと「該当なし」と区別がつかないからです |

**API モードの詳細**:

| 項目 | 内容 |
| -- | -- |
| 切替 | 環境変数 `LOOPTRACK_API_URL`（例 `https://example.com/looptrack`）があれば API モードになります。プロジェクト単位で切り替えるには、そのプロジェクトの `.claude/settings.json` の `env` に `LOOPTRACK_API_URL` と `LOOPTRACK_PROJECT` を書きます。Claude Code がこれを Bash ツールと hook に渡します |
| プロジェクト | `LOOPTRACK_PROJECT` です（MCP はヘッダ `X-Looptrack-Project` か引数） |
| トークン | `LOOPTRACK_TOKEN`、無ければ `~/.config/looptrack/credentials.json`（URL ごと）を使います。保存するのは `looptrack issue login --browser`（OAuth・§3-2）か `login --url`（貼る方式）で、`/me` で検証してから 0600 で書きます。グループや他者が読める権限なら拒否します。OAuth で保存したものは、期限の前と 401 のときに更新トークンから自動で取り直します。Keychain は使いません（`security` にトークンを引数で渡すとプロセスの一覧に出るため）。Go 版（looptrack）の置き場と Windows での保護は §5-1 の「資格情報の置き場」にあります |
| 送るヘッダ | `X-Looptrack-Client: cli` を送ります。`X-Looptrack-Session` の値は `LOOPTRACK_SESSION_ID` → `CLAUDE_CODE_SESSION_ID` → `CLAUDE_SESSION_ID` → `CLAUDE_CODE_HOST_SESSION_ID` → `CODEX_THREAD_ID` → `CODEX_SESSION_ID` → `COPILOT_AGENT_SESSION_ID`（`COPILOT_CLI=1` のときだけ）の順に探します。Claude Code の Bash に渡るのは `CLAUDE_CODE_SESSION_ID`、Codex のシェルに渡るのは `CODEX_THREAD_ID`、Copilot CLI は `COPILOT_AGENT_SESSION_ID` です。**`CLAUDE_CODE_HOST_SESSION_ID` はデスクトップ版のセッションの窓（器）が渡す ID です。`CLAUDE_CODE_SESSION_ID` が渡らない窓でも並行するセッションを見分けられるよう、最後に読みます。会話記録のファイル名とは一致しないので、トークン情報を引くのには使いません（`usagesnap.Detect` は読みません）。この ID を送るときは `X-Looptrack-Session-Kind: host` も送り、サーバが付与の対象から外せるようにします**（§9-5「器のセッション ID」）。`X-Looptrack-Session-Kind` は `host` のときだけ送ります（上記）。`X-Looptrack-Agent: copilot` は Copilot の下で動いているときだけ送ります。対象は VS Code のエージェント用ターミナル（`AI_AGENT=github_copilot_vscode_agent` / `COPILOT_AGENT=1`）と Copilot CLI で、既定では送りません |
| 出力を固定するもの | list / ready / show / comment / status / close と、入力の誤り・見つからない場合のエラー（stdout・stderr・終了コード）です。ゴールデンテストで全件を比べます（入力は合成フィクスチャ `internal/testdata/fixtures`） |
| 出力の決まり | `new` は `作成: <ID> <タイトル>` を出します（存在しないパスは出しません）。`index` / `matrix` はファイルを書かず、生成した Markdown を標準出力へ出します。`--labels "a, b,"` のように空白や空の要素を含む入力は、正規化して保存します（`[a, b]`） |
| 担当者（§9-2） | 担当が付いたイシューは出力が増えます。`show` の frontmatter の `assignee:` 行、`list` / `ready` / `summary` の `ASSIGNEE` 列（担当が 1 件でもある表だけ）、`status` の `担当: <ID>: <旧> → <新>` 行です。最後の行は明示の変更と引き継ぎのときだけ出し、In Progress で本人が担当になる R1 では出しません。担当の無いイシューだけの表には列を出しません |
| サブコマンド | `list` / `ready` / `show`（`--json`）、`edit` / `push [--rebase]`、`activity <ID…> [--since] [--json]`、`summary [--limit] [--json]`、`config [--json]`（利用者名も出す）、`login [--url] [--browser]`、`guide` / `next` / `init`（§5-2） |
| 鮮度ガード | Stop のときに `/activity`（`since` = セッション開始時刻）を 1 回呼びます。`events_since` が 0 のイシューは「参照したのに更新していない」とみなします。クローズ済みと存在しない ID は対象外です。ID の見分けに使う prefix / width は `.claude/.looptrack-freshness/project.json` に控えます（mark は毎回のツール実行で走るので、そこでは HTTP を出しません）。サーバに聞けないときは黙って通します（fail-open） |
| MCP | `/looptrack/mcp` です。公式 Go SDK の streamable HTTP を **Stateless・JSON 応答**で使い、セッションを持たないので常駐メモリが増えません。GET は 405 です。認証は Bearer のアクセストークンだけで、Cookie のセッションは使えません（401 には `WWW-Authenticate: Bearer`）。ツールは `list_projects` `create_project`（プロジェクトの作成。管理者だけ。「setup の直後にブラウザだけで起票できるようにする」の (c) と同じ `service.CreateProject`）`list_issues` `ready_issues` `get_issue` `create_issue` `add_comment` `set_status` `update_issue` `get_matrix` `project_summary` `issue_activity` `guide` `next`（guide / next は §5-2）`assign_issue`（担当者・§9-2・`create_issue` / `set_status` / `update_issue` / `next` にも `assignee` 引数）、トークンの `issue_usage` `usage_missing` `usage_report` `list_usage_ledger` `add_usage_ledger`（§9-5）`list_usage_requests` です。REST と同じ internal/service を呼ぶので、ルール・楽観ロック・権限・記録も同じです（`via=mcp`）。project は引数 → ヘッダ `X-Looptrack-Project` → 利用できるプロジェクトが 1 つならそれ、の順に決めます。エラーはツールの実行エラー（isError）で、文言は REST と同じです。`.mcp.json` の例: `{"mcpServers": {"looptrack": {"type": "http", "url": "https://example.com/looptrack/mcp", "headers": {"Authorization": "Bearer ${LOOPTRACK_TOKEN}", "X-Looptrack-Project": "<slug>"}}}}`。実際の Claude Code での確認は `deploy/dev/mcp-e2e.sh` で行います |
| edit / push | `edit` は `.claude/.looptrack-work/`（中に `.gitignore`）へ `<ID>.md`・`<ID>.base.md`・版番号を書きます。反映していない変更があれば、`--force` なしでは取り直しません。`push` は変更が無ければ何もせず、作業コピーを消します。409 のときは edit した時点からサーバの最新への差分を stderr に出し、最新版を `<ID>.server.md` に保存して exit 1 で終わります。差分を取り込んだら `push --rebase` で最新版に対して反映します。**作業コピーを取った後に付いたコメントはサーバ側が保持します**（全文更新のコメント節は、現在のコメントの先頭部分であればかまいません） |

### 1-4. リポジトリ構成

構成の一覧は [README.md](../../README.md) の「Repository layout」にあります。要点は次のとおりです。

- サーバのコードは `cmd/looptrack`（`server.go`・`serve.go`・`admin.go`・`setup.go` ほか）と `internal/` にあります。HTTP は `internal/server` に REST・MCP・OAuth・画面をまとめています。
- クライアント（CLI・hook・init・usage の収集・report pdf）は `internal/client/` にあり、サーバと同じ `looptrack` の実行ファイルに入ります（§5-1）。
  **導入先に置くスクリプトは 1 つもありません**。実行ファイル 1 つだけで動き、別の処理系も bash も要りません。
- 配布では `kit/embed.go` が kit をサーバに埋め込みます（`GET /api/v1/dist`・§5-2）。AI 向けの使い方は `internal/guide` が組み立てます（共通規則は `internal/guide/common.md`）。
- 全プロジェクト共通の skill は `kit/core/skills/<名前>/` にあります（`issue`・`token-report`）。init が各プロジェクトの `.claude/skills/<名前>/SKILL.md` に置きます。
- 各プロジェクトへ配る hook / rules / skill の実体は `kit/core/`・`kit/loop/` です（§8・一覧は `kit/README.ja.md`）。
- 文書は `docs/AI-GUIDE.md`（AI からの使い方）・`docs/ADD-PROJECT.md`（プロジェクトを載せる手順）・`docs/projects/<slug>.md`（プロジェクト別の運用ルール）です。
- 比較テスト（往復一致・取り込み・ゴールデン）の入力には、合成フィクスチャ `internal/testdata/fixtures`（架空の 3 プロジェクト demo・sample・mini）だけを使います。
  実データはテストに持ち込みません。`internal/domain/testdata/golden.json` の記録と対になっているので、片方だけ直さないでください。

## 2. データモデル

### 2-1. テーブル（MySQL 8.4 / utf8mb4）

Markdown で書いたイシューを取り込んでも、下のように分けて保存したものから元のバイト列に戻せます（往復一致・§2-2）。

| テーブル | 主な列 | 備考 |
| -- | -- | -- |
| `projects` | slug（一意）, prefix（一意）, width, name, description, sort_order, next_number, rules（JSON）, **archived_at**（NULL 可・`migrations/0004_projects_archived.sql`） | prefix / width は作成後に変更できません。表示名（name）だけは `looptrack project rename <slug> <表示名>` か管理画面（§3-1「プロジェクト管理」）で変えられます（管理者・`LOOPTRACK_DSN` の経路で `project create` と同じ。規則は `service.RenameProject` に置き、名前の検査は作成と同じ `store.ValidateProjectName`（空・空白だけ・255 文字超を拒否）。slug・prefix・width・採番には触れません）。`archived_at` はアーカイブ（画面の「削除」）の印で、**論理削除**です。行もイシュー・コメント・イベントも消さず（`deploy/grants.sql` は projects・issues に DELETE を与えず、comments・issue_events は追記だけのまま）、slug と prefix は一意のまま残るので使い回されません。値があると一覧と解決から外し（§3-1「一覧用と解決用」）、起票・更新・コメントを拒みます。管理者が NULL に戻せます（`looptrack project archive|unarchive <slug>`・管理画面。規則は `service.SetProjectArchived`） |
| `issues` | project_id, number, display_id（一意）, file_name（VARBINARY）, front_keys（JSON: 出現したキーの順序）, title, type, status, priority, parent（`''` 可・NULL と区別）, created / updated（CHAR(16)）, body_main（MEDIUMTEXT）, gap_nl, preamble（NULL 可）, trail_nl, version, closed_at, **assignee_user_id**（NULL 可・users への FK・マイグレーション 0008・§9-2） | 列挙値は CHECK 制約で縛ります。`version` は楽観ロックに使います。`assignee_user_id` は**サーバだけの項目**で、front_keys には入りません。取り込み・往復一致・ファイルモードには影響せず、表示のときだけ frontmatter に `assignee:` を差し込みます |
| `issue_values` | issue_id, field（labels / blocked_by / traces / refs）, pos, value | 順序を保ちます。**取り込みでは空白を含む値を分割しません**（書いてあるとおりに保つため）。キー自体が無いことは front_keys で表します。ID の項目（blocked_by / traces / refs）に空白区切りの値を新しく入れることはできません。取り込んだ既存の分は `looptrack repair-lists` で分割します（§2-3） |
| `issue_extra` | issue_id, key, value | `origin` など、モデルに無い frontmatter のキーです。位置は front_keys で持ちます |
| `comments` | issue_id, seq, ts（CHAR(16) 表示用）, created_at（DATETIME(6)・新規のみ実時刻）, author_user_id, via, content（MEDIUMTEXT・空可） | 並びは seq です。同じ分に 2 件以上あっても順序が決まります。**アプリ用の DB ユーザーには UPDATE / DELETE の権限を与えません**（append-only を DB の権限でも担保するため） |
| `issue_events` | issue_id, at（DATETIME(6)）, actor_user_id, token_id, via（cli/web/mcp/import）, session_id, kind, detail（JSON） | 全変更の監査ログで、鮮度ガードの判定元です |
| `users` | login, display_name, password_hash（argon2id）, totp_secret（暗号化）, totp_enabled, role（admin/member）, disabled_at | |
| `system_settings` | name（PK）, value, updated_at | システム全体の設定です。`two_factor` = `required` / `optional`（マイグレーション 0010） |
| `setting_changes` | at, name, old_value（空は未設定）, new_value, actor_user_id（NULL 可）, via（web / command / migration / cli / api / mcp）, note, ip | 設定の変更の記録です。**アプリ用の DB ユーザーは SELECT・INSERT のみ**です（追記専用）。撤去したゼロバグゲートの切り替えの記録（`zero_bug_gate:<slug>`）も、追記専用なのでそのまま残ります（§9-1「ゼロバグゲート（撤去済み・2026-09-20）」） |
| `project_members` | project_id, user_id, role（viewer / editor / admin） | 参加者だけが見られます（既定で非公開） |
| `api_tokens` | user_id, name, prefix, hash（SHA-256）, scopes, project_ids, expires_at, last_used_at, revoked_at | 平文は発行時に 1 回だけ表示します |
| `web_sessions` | id_hash, user_id, csrf_token, mfa_passed, **totp_verified**（0010）, created_at, expires_at, last_seen_at, ip, user_agent | Cookie `looptrack_session`（Path=/looptrack, Secure, HttpOnly, SameSite=Strict）です。mfa_passed はログインの段階を終えたか、totp_verified はそのとき TOTP を入力したかを表します |
| `oauth_*` | clients（動的登録）, auth_codes（PKCE）, refresh_tokens（更新トークン・系列と対のアクセストークン・§3-2）, 発行トークンは api_tokens に kind 付きで保存 | MCP・CLI（login --browser）用です |
| `project_guides` | project_id（PK）, content（MEDIUMTEXT）, source, updated_at | プロジェクトの運用文書です（§5-2・マイグレーション 0005） |
| `mcp_connections` | id（発行した Mcp-Session-Id）, user_id, token_id, client_name, client_version, agent, protocol_version, project, user_agent, created_at, last_seen_at, closed_at | MCP の接続ごとの clientInfo です（§6・マイグレーション 0007）。30 日使われなければ消します |
| `agent_installs` | user_id + project_id + agent（一意）, source, files（JSON）, bundle_sha256, host, workspace, self_repo, first_at, reported_at, hook_at | 導入済み通知です（§6・マイグレーション 0007・`self_repo` は 0002） |

照合順序は `utf8mb4_bin` です。大文字小文字と末尾の空白を区別し、4 バイト文字もそのまま保ちます。

#### 二段階認証の設定（マイグレーション 0010）

決めていることは次の 3 つです。

1. 任意のときも、TOTP を登録した利用者には確認コードを求めます。
2. 必須から任意への切り替えには、操作する管理者の再認証が要ります。
3. 通常モードでの初回の決め方は、管理コマンドの引数だけです。公開サーバに「最初の管理者になれる窓」を開けないためです（ローカルモードの初回設定は §3-3）。

| 項目 | 内容 |
| -- | -- |
| 値 | `system_settings.two_factor` = `required`（必須）/ `optional`（任意）です。**行が無い（未設定）ときは必須として動きます**（安全側） |
| 既定 | 利用者が 1 人以上いる DB にマイグレーション 0010 を当てると `required` が入り、`setting_changes` に via=migration で記録されます（安全側に倒します） |
| 初回 | 利用者 0 人の DB では未設定のままです。最初の利用者を作る `looptrack user add <login> --admin --two-factor required\|optional` は、`--two-factor` が無いか値が違うと**作らずに**指定方法を示して終了 1 で止まります（パスワードを聞く前に判定します）。2 人目以降に `--two-factor` を付けると拒否します（変更は下の経路で記録を残します） |
| 必須 | TOTP を登録していない利用者は、パスワードの後に登録画面へ送られます。登録するまで `mfa_passed` になりません |
| 任意 | TOTP を登録していない利用者は、パスワードだけで `mfa_passed`（`totp_verified = FALSE`）のセッションを得ます。画面・API・MCP の OAuth 認可画面のすべてで同じです。登録済みの利用者には確認コードを求めます。各自が `/looptrack/account` から登録（`/looptrack/account/totp`）と解除（`POST /looptrack/account/totp/disable`）ができます。解除はパスワードと確認コードで再認証し、必須のときは 403 です。登録・解除のたびに本人のセッションを全部破棄し、操作した画面のセッションだけ作り直します |
| 切替（画面） | `/looptrack/admin/security`（GET / POST）です。**管理者以外は 403** で、利用者管理の画面から辿ります。項目は `two_factor`・`password`・`code` です。**必須 → 任意**には操作者の再認証が要ります（パスワードと、TOTP 登録済みなら確認コードも）。失敗は `login_attempts` に `stage=reauth` で記録し、ログインと同じ回数制限をかけます。コードは使用済みのステップを進めます。任意 → 必須は再認証なしです |
| 切替（コマンド） | `looptrack settings two-factor` で表示と記録を、`looptrack settings two-factor required\|optional` で変更をします。サーバ上の操作なので再認証はありません。記録は via=command で、note に `SUDO_USER` を入れます |
| 任意 → 必須 | 切り替えのトランザクションで、`totp_verified = FALSE` のセッションと TOTP 未登録の利用者のセッションを DELETE します。加えて必須のときは、`sessionFrom` が `mfa_passed` でも `!totp_verified` か未登録の利用者のセッションを破棄して未ログインとして扱います。切り替えと同時に発行されたものや、DB を直接書き換えた場合もこれで止まります。再ログインで登録・入力を求めます |
| 変わらないもの | TOTP シークレットは変わりません（切り替えでは触らず、消えるのは本人の解除と管理者の TOTP リセットだけ）。アクセストークン（PAT・OAuth）の認証も変わりません（`bearer()` はセッションも設定も見ません） |
| 記録 | `setting_changes` に、誰が・いつ・どの値からどの値へ・経路・IP を残します。`/looptrack/admin/security` に新しい順に 50 件出します。サーバのログにも `admin` / `two_factor` を出します |
| 読まない設定 | 環境変数 `LOOPTRACK_REQUIRE_TOTP` は読みません。設定されていれば起動時に警告を出して無視します。二段階認証は DB の設定だけで決まります |

**Markdown への復元規則**（`internal/mdformat`）は次のとおりです。
`frontmatter（front_keys の順）` + `\n` + `body_main` + `"\n" × gap_nl` + `## コメント`
\+ （preamble があれば `\n\n` + preamble）+ 各コメント `\n\n### ` + ts（content があれば `\n\n` + content）
\+ 末尾改行を除去して `"\n" × trail_nl`。
区切りは「コードブロック外の最初の `## コメント`」と「コードブロック外の `^### \d{4}-\d{2}-\d{2} \d{2}:\d{2}$`」です。

実データの特徴（取り込むときに壊さないもの）:
- 本文とコメントの間の空行が 3 行 / ファイル末尾が `\n\n` / コメント欄冒頭の文章（preamble）/ コメント 0 件
- 日付だけのものや `##` の疑似コメントは、直前のコメントの content か preamble に含めたまま保ちます
- ファイル名が NFD のものや、title を変えて slug が合わなくなったもの。file_name は元のバイト列で保ちます
- 8 万バイトを超えるファイル（本文の列に MEDIUMTEXT が要ります）
- これらの形は合成フィクスチャ `internal/testdata/fixtures`（`generate.py` の出力）に意図して入れてあります。消えていないことは `internal/transfer` の `TestFixturesCoverage` が確かめます。フィクスチャ自体の検査を transfer に置くのは、`go test ./...` が testdata という名前のディレクトリを対象にしないからです。NFD のファイル名だけは git が NFC に揃えてしまうので、`internal/transfer` のテストの中で組み立てます

### 2-2. 取り込み・往復一致・書き出し

Markdown ファイルでイシューを管理してきたプロジェクトを、内容を失わずに載せ替えられるようにします。

- `looptrack import --root <repo>`: `config.json` → projects、`counter` → next_number、`open/` `closed/` → issues / values / extra / comments に取り込み、イベント `import` を残します。
- `looptrack verify --root <repo>`: 全ファイルを DB から Markdown に作り直し、**バイト一致**を確かめます（一致しなければ差分を出して非ゼロで終わります）。
  往復一致を確かめてから切り替えられるので、取り込みで情報が落ちていないことを機械で保証できます。
- 載せ替えはプロジェクト単位です。起票を止める → 最後の取り込み → verify → プロジェクト側の設定を API モードへ → hook・skill・案内の節を更新、の順に進めます。
- **載せ替えた後は DB が正本です**。Markdown への定期的な書き出しはしません。`looptrack export` でいつでも Markdown を書き出せるので、データは閉じ込められません。
  バックアップは DB のダンプで取ります。

### 2-3. ID の項目の空白区切り

**防ぎたいこと**: `--refs 'NFR-SEC-003 NFR-SEC-004'` のような空白区切りが 1 要素のまま保存されると困ります。
`list --ref NFR-SEC-003` の逆引き・`ready` の blocked_by の判定・matrix の traces の突き合わせに出てこないからです。
ID の項目（blocked_by / traces / refs）は、どの経路でも 1 要素 = 1 ID を守ります。

**入力の扱い（全経路）**:

| 経路 | 扱い |
| -- | -- |
| REST・MCP（`POST /projects/{slug}/issues`・`PATCH` の項目指定・`create_issue`・`update_issue`） | blocked_by / traces / refs の要素が途中に空白（全角空白を含む Unicode の空白）を含めば、**400 で拒否します**（`service.cleanList`） |
| 全文の更新（`PATCH {markdown}` = CLI の edit → push） | 同じ項目に**新しく入った**空白入りの値を 400 で拒否します（`service.checkSpacedIDs`）。現在の値に既にあるもの（補正前の旧データ）は通します。本文だけを直す更新を妨げないためです |
| CLI | `new` は `--blocked-by` / `--traces` / `--refs` をカンマと空白の両方で区切って送ります（人がシェルで打つときは空白区切りが自然なため）。`push` は送る前に同じ検査をします（検査の無い古いサーバでも保存させないため） |
| labels | 対象外です。ラベルは名前なので、空白を含むものも正しい値です（例 `保守リスク再監査 対応`） |

**分割でなく拒否にする理由**:

1. カンマ・改行は既に 400 で拒否しているので（「複数の値は配列で指定」）、同じ扱いに揃います。
2. API・MCP の呼び出し側は配列を組み立てているので、空白入りの要素は呼び出し側の誤りです。黙って直すと誤りに気づけません。
3. 人がシェルで打つ CLI だけは空白区切りが自然なので、CLI 側で分けて送ります（サーバの規則は 1 つのまま）。

ID は空白を含みません（採番 ID・文書 ID ともに英数字とハイフン）。そのため ID の項目に限れば、拒否しても正しい入力を失うことはありません。

**取り込んだデータの補正（`looptrack repair-lists`）**: クローズ済みの本文・項目は変更できないという規則（§4）の**例外**として、管理コマンドだけが補正します。
値の意味は変えず（`"A B"` → `["A", "B"]`）、逆引きから漏れるという保存形式の問題を直すだけだからです。経緯（本文・コメント・状態）は失われません。
新規に起票して参照し直しても、クローズ済みの refs が逆引きに出ない問題は直りません。

- 既定は **dry-run** です。`<slug> <ID> (<状態>) <項目>: [変更前] → [変更後]` とプロジェクト別・合計の件数を出すだけです。`--apply` を付けると書き込みます。
  対象の項目は `--fields` で選び、既定は `blocked_by,traces,refs` です。labels も指定できますが、既定では触りません。slug を並べるとプロジェクトを限れます（省略すると全プロジェクト）。
- 分割は Unicode の空白で行い、分けた結果の重複は先に出たほうを残します（例 `["A B", "B"]` → `["A", "B"]`）。空白を含む要素が無い項目は触りません。
- 処理は 1 イシューずつで、行ロック → 計算し直し → 保存（版番号を進める）→ `issue_events` への記録の順です。記録は **kind `repair_lists`・via `admin`** で、detail は
  `{fields, before, after, status, reason}` です（append-only の監査記録で、コメントは足しません）。**updated は変えません**。利用者の更新ではないので、
  一覧の更新日時の順を乱さないためです。
- アプリ用の DB 利用者の権限（issues の UPDATE・issue_values の DELETE / INSERT・issue_events の INSERT）で動きます。
  運用中のサーバでは `looptrack repair-lists`（dry-run）→ 中身を確かめる → `looptrack repair-lists --apply` の順に実行します。
- マイグレーションでなくコマンドにした理由は 3 つです。dry-run で対象と差分を見せてから書けること・監査記録の主体（via admin）と理由を残せること・
  labels のように判断の要る項目を外せることです。スキーマの変更はありません（issue_events.kind に CHECK は無いため）。

## 3. 認証と権限

### 3-1. アカウント設定・利用者管理

各利用者が CLI 用のトークンを自分で発行・失効できるようにして、サーバ上の管理コマンドを使わずに済ませます。イシューの画面は閲覧のみです。

| 項目 | 内容 |
| -- | -- |
| 画面 | `/looptrack/account`（全利用者）と、`/looptrack/admin/users`・`/looptrack/admin/users/<login>`（管理者のみ・ほかは 404）です。ハブとボードのヘッダから辿れます |
| 変更の方式 | すべて POST です。`web()` が TOTP 済みのセッションと CSRF を検査し、違えば 403 で何も変えません。結果は同じ画面を描き直し、メッセージで伝えます。スクリプトは使いません（CSP） |
| パスワード変更 | 現在のパスワードで再認証します。argon2 の同時実行枠を使います。失敗は `login_attempts` に `stage=reauth` で記録し、ログインと同じ回数制限をかけます。成功したら全セッションを破棄し、操作した画面のセッションだけ作り直します |
| トークン発行 | 用途（必須・255 文字まで）と有効日数（30 / 90 / 180 / 365）を指定します。平文は発行した POST の応答にだけ出し、DB にはハッシュと先頭 12 文字を置きます。無期限にできるのは管理コマンド（`looptrack token create --days 0`）だけです |
| 失効 | 一覧には PAT と OAuth の両方が出ます。`user_id` の条件つきの UPDATE で失効させるので、他人の ID や失効済みのものは 404 で何も変えません。OAuth のアクセストークンを失効させると、対の更新トークンも使えなくなります（§3-2） |
| 利用者管理 | 追加（ログイン名は英数字と `. _ -`・初期パスワード・役割 admin / member）・役割の変更・無効化 / 有効化・パスワードの再設定・TOTP のリセット・プロジェクト権限（viewer / editor / admin / 解除）・トークンの失効ができます。無効化するとセッションを破棄し、トークンも認証時に利用者の無効を見て 401 にします |
| 自己防衛 | 管理画面から自分自身の役割変更・無効化・TOTP リセット・パスワード再設定はできません（自分のパスワードはアカウント設定で変えます）。有効な管理者が 0 人になる変更も拒否します |
| 記録 | 管理操作とトークンの発行・失効は、サーバのログ（JSON）に操作者・対象・操作を出します。秘密は出しません |
| 初回 | 最初の管理者だけは、サーバ上で `looptrack user add … --admin --two-factor required\|optional` を実行して作ります。二段階認証を必須にするか任意にするかもここで決めます（§2-1「二段階認証の設定」・DEPLOY.md） |
| 二段階認証 | `/looptrack/account` に登録の状態と、任意のときの登録・解除があります。`/looptrack/admin/security`（管理者のみ・ほかは 403）で必須 / 任意の切り替えと変更の記録を扱います。詳しくは §2-1「二段階認証の設定」を見てください |
| プロジェクト管理 | `/looptrack/admin/projects` です（管理者のみ・ほかは 404・メニューの「プロジェクト管理」から開く）。全プロジェクトを並べ、各プロジェクトの参加者と役割・自分が参加しているか・「開く」（`/looptrack/p/<slug>/`）を出します。付与・変更・解除は `POST /looptrack/admin/projects/<slug>/member` で行います。項目は `login`・`role`（空なら解除）と、代わりの担当者を示す `replacement` です（§9-2「参加を外すときの担当」）。利用者の画面の「プロジェクト権限」（`POST /looptrack/admin/users/<login>/member`）と同じ関数（`setProjectMember`）を通るので、結果は同じです。表示名の変更は `POST …/projects/<slug>/rename`（`name`。`service.RenameProject`）、アーカイブ（画面の「削除」）は `POST …/projects/<slug>/archive`（確認のため `confirm` に slug をそのまま入れさせ、違えば 400）、戻すのは `POST …/projects/<slug>/unarchive` です（どれも `service.SetProjectArchived` / `RenameProject` を呼ぶだけで、規則は service に置きます。管理者以外は 403）。アーカイブ済みのプロジェクトは参加者の欄を出さず、下の「アーカイブしたプロジェクト」に名前・slug・接頭辞と「戻す」だけを並べます。画面はサーバ側で描きます（JS なし・CSP） |
| プロジェクト権限と自己防衛 | プロジェクト権限は、一覧に出るプロジェクトとそこでの役割を決めます。**管理者も、参加している（行がある）プロジェクトではその役割で動きます**（viewer なら読むだけ）。**行が無いプロジェクトは閲覧のみ**です（役割 viewer 扱い）。プロジェクトの役割によらず管理者に書かせると、viewer で参加している管理者が自分の参加を外すだけで書けてしまうからです。書きたいときは、自分をここで editor / admin として参加させます（自分自身の参加・役割も変えられます）。`/looptrack/admin/*` の管理操作はプロジェクト権限に左右されません（利用者の役割 admin だけで判定します）。利用者の役割（admin / member）と無効化には自己防衛があります（自分には使えず、有効な管理者を 0 人にしません） |

#### 一覧用と解決用

**一覧に出るもの**と、**slug を指定して解決できるもの**を分けます。管理者は全プロジェクトを**読めます**が、一覧に出すのは参加している分だけです。
一覧は AI の作業場所を選ぶためのものなので、参加していないものを並べても選べないからです。
解決したときの役割は、管理者でも参加しているプロジェクトでは `project_members` の値です。参加していないプロジェクトでは **viewer**（閲覧のみ）になります。

**読み取りは全件、書き込みは参加（editor / admin）を要します。** 参加なしで管理者に書かせると、viewer で参加している管理者が
自分の参加を外すだけで書けてしまうためです。管理の操作（`/looptrack/admin/*` の参加者・役割の変更）はプロジェクトの役割に左右されません。
書きたい管理者は、自分を参加させてください。

| 用途 | 関数 | admin の利用者 | member の利用者 |
| -- | -- | -- | -- |
| 一覧（ハブ・`GET /api/v1/projects`・MCP `list_projects`・MCP の既定プロジェクト） | `store.MemberProjects` | 参加分・役割は `project_members` の値 | 参加分 |
| 解決（slug・ID を指定した操作: `/looptrack/p/<slug>/`・`/api/v1/projects/<slug>/…`・`/api/v1/issues/<id>`・`activity`・CLI の `LOOPTRACK_PROJECT`・MCP の `project` 引数と ID） | `store.AccessibleProjects` | 全件です。役割は `project_members` に行があればその値、無ければ viewer です（`store.NonMemberAdminRole`） | 参加分 |

**アーカイブ済みのプロジェクト（`projects.archived_at` が NULL でない）は、どちらにも出しません**（`store.UserMemberships` と `store.AccessibleProjects` の 1 か所で外す。
利用者の役割にかかわらず、管理者の `?all=1` も同じ）。そのため一覧（ハブ・REST・MCP・ログイン済みの CLI）に出ず、slug・ID を指定した
閲覧・書き込みは「見つからない」（404）になります。書き込みは service の入口（起票の `Create` と、既存イシューを変える `mutate`。
コメント・状態・担当・編集・検証の記録・補正がここを通る）でも、DB から読み直した `archived_at` で拒みます（`Rejected`・`project_archived`）。
一覧の解決を通らない呼び出し（サーバ側の管理コマンド）にも効かせるためです。全件を見るのは管理画面（`store.ListProjects`）と、
書き出し・補正の全件指定だけです（補正の全件指定はアーカイブ済みを外します）。

- **`GET /api/v1/projects?all=1`**（`true` も可・`0` / `false` / 無しは既定）: 管理者だけが使えます。全プロジェクトを返し、参加していないものは
  `role: "viewer"`・`member: false` です（解決の役割と同じ）。**管理者以外が付けると 403 `forbidden`** になります。黙って参加分を返すと、呼び出し側が「全件」と取り違えるからです。
  それ以外の値は 400 です。プロジェクト管理の画面は、同じデータをサーバ側で組み立てます（`ListProjects` + `AllMembers`）。
- 一覧と `GET /api/v1/projects/<slug>` の各プロジェクトには `member`（参加しているか）を付けます。`<slug>` の `role` は解決の役割です。
  管理者が viewer で参加していれば viewer、参加していなければ viewer・`member: false` になります。
- **権限の判定は `store.AccessibleProjects` の 1 か所で行います**。画面（`/looptrack/p/<slug>/` の `can_edit`・担当の変更・レポート作成依頼）・REST
  （`resolveProject` / `resolveIssue` → `canWrite`）・MCP（同じ関数）・CLI（`LOOPTRACK_PROJECT` は REST に渡るだけ）が、すべてここの役割を使います。
  `/looptrack/admin/*` はプロジェクトの役割を見ません（利用者の役割 admin だけを見ます）。
- MCP: `project` 引数も `X-Looptrack-Project` も無いときは、参加しているプロジェクトが 1 つならそれを使います（管理者も参加分で数えます）。
  0 件か複数なら「project を指定してください（参加しているプロジェクト: …）」を返します。`list_projects` は参加分だけで、全件を出す引数は設けません。
  AI の作業場所を選ぶためのものだからです（全件は画面か `?all=1` で見ます）。
- CLI: `LOOPTRACK_PROJECT` はそのまま解決に使います。参加していない slug も管理者なら読め、参加していれば `project_members` の役割になります。`config` は参加していないときに
  「注意: このプロジェクトには参加していません（閲覧のみ。書くには…参加させてください）」を出し、`--json` に `member` を足します。
  文面はサーバが返す役割で分けるので、CLI だけを先に更新しても誤った案内にはなりません。

### 3-2. CLI のブラウザログインと更新トークン

CLI のログインに要る人手を 2 つだけにします。**AI が実行する `looptrack issue login --browser` と、利用者のブラウザでのログイン・承認**です。
`/account` で発行したトークンを CLI に貼り直す作業が無くなります。
MCP 用の OAuth 2.1（認可コード・PKCE・承認画面・`api_tokens` の kind=oauth）をそのまま使い、別の認可コードの発行経路は作りません。
トークンの保存は §1-3 の「API モードの詳細」の「トークン」の行のとおりです（`/me` で検証してから `credentials.json` に 0600 で書きます）。トークンは CLI のプロセスとサーバの間の HTTP の本文だけを通ります。

#### 流れ（`looptrack issue login --browser [--url URL]`）

1. 認可サーバのメタデータ（`<URL>/.well-known/oauth-authorization-server`）の `grant_types_supported` に `refresh_token` が無ければ、
   「サーバの更新が必要」と出して終えます。更新トークンを持たないサーバでは、この流れが成り立たないからです。
2. `127.0.0.1` の空きポートで待ち受けます（1 回だけ・5 分で打ち切り）。戻り先は `http://127.0.0.1:<port>/callback` です。
3. クライアントは初回だけ動的登録をします（`/oauth/register`・名前は「looptrack（<ホスト名>）」・grant は authorization_code と refresh_token）。
   `client_id`（秘密ではありません）は credentials.json の URL ごとの項目に控え、以後は使い回します。打ち切りになったときは控えを消します。
   サーバに無い client_id で承認画面が出ない場合に、次の実行で登録し直すためです。
4. ブラウザで `/oauth/authorize` を開きます（開けない環境のために URL も表示します）。state は 32 バイトの乱数、PKCE は S256 です。
   `resource` は REST API の基点（`<URL>/api/v1`）です。利用者は既存のログイン（ID / パスワード + TOTP）と承認画面を通ります。
5. コールバックは `GET /callback` だけを受けます（ほかのパスは 404）。state が違えば 400 で拒否して待ち続けます（比較は定数時間）。
   ブラウザには「閉じてよい」だけを返し、要求の記録（URL に認可コードを含む）は出しません。`error`（access_denied など）なら理由を出して終えます。
6. `/oauth/token` で code と code_verifier を引き換え、`/me` で確かめます。そのうえで credentials.json（0600・ディレクトリは 0700）に
   `{token, refresh_token, expires_at, client_id, login}` を保存します。出力は「ログイン: <利用者名>（URL）。…に保存しました」だけです。

`login --url`（トークンを貼る方式）は、ブラウザの無い環境のために残しています。保存の形は同じです。
ブラウザを使う方式は `--browser` を明示して選びます。既定にすると、既にある手順の意味が変わってしまうからです。`config` は利用者名（`/me`）も出します。

#### サーバの変更

| 項目 | 決定 |
| -- | -- |
| 戻り先の照合 | RFC 8252 §7.3 に合わせ、**ループバック（`127.0.0.1` / `::1` / `localhost`）の http だけはポート番号を問いません**。ホストの表記・パス・問い合わせは登録と同じでなければならず、利用者情報とフラグメントは認めません（`localhost` と `127.0.0.1` は別物として扱います）。https とループバック以外は完全一致のままです。承認画面の CSP（form-action）は、実際の戻り先（ポートつき）を許します。引き換えのときの redirect_uri は、認可のときの値（ポートつき）と完全に一致しなければなりません |
| resource | MCP の URL（`…/mcp`）に加えて、REST API の基点（`…/api/v1`）も受け付けます。省略もできます。認証はトークンの宛先を見ないので、どちらのトークンも REST・MCP の両方で通ります |
| 更新トークン | `refresh_token` grant を足します。認可コードの引き換えでも更新でも、アクセストークンと更新トークン（`imr_` + 乱数 30 バイト・平文は保存しない）の組を発行します。登録時の grant_types によらず発行します。メタデータの `grant_types_supported` に `refresh_token` を載せます |
| 入れ替え（rotation） | 更新のたびに、使った更新トークンを使用済みにして対のアクセストークンを失効させます。そして同じ系列（`family_id`）の新しい組を発行します（1 トランザクション・行ロック）。アカウント画面に出る有効なトークンは、系列ごとに常に 1 つです |
| 再利用の検知 | 使用済みの更新トークンがもう一度出されたら漏えいとみなします。**系列の更新トークンと系列で発行したアクセストークンをすべて失効**させ、invalid_grant を返します（失効はコミットして残します）。client_id が違うだけでは系列を止めません。ほかのクライアントの値の誤用で、利用者の系列を止めないためです |
| 失効との連動 | 更新トークンは対のアクセストークン（`access_token_id`）を持ちます。アカウント画面や管理画面でそのアクセストークンを失効させると、更新トークンも使えなくなります（出されたら系列ごと失効させます）。利用者を無効化しても使えません |
| 期限 | アクセストークンは 30 日です。**更新トークンは発行から 90 日で、使うたびに入れ替わって新しい値の 90 日が始まります（上限は設けません）** |
| テーブル | `oauth_refresh_tokens`（マイグレーション 0012）: `token_hash`（SHA-256・一意）・`family_id`・`user_id`・`client_id`・`access_token_id`・`scope`・`resource`・`expires_at`・`used_at`・`revoked_at`。アプリ用ユーザーは SELECT・INSERT・UPDATE を持ちます（行は消さない・件数は利用者 × 端末 × 30 日ごとに 1 行程度） |
| MCP クライアント | 同じ更新トークンを使えます。Claude Code は登録時に refresh_token を含めます。更新に対応したクライアントは、期限が切れると自動で取り直します |

**期限の理由**: 狙いは「利用者がトークン切れを意識しないで済むこと」です。CLI は期限の前と 401 のときに自動で更新するので、
アクセストークンの期限は利用者から見えません。更新トークンは使うたびに延びるので、**90 日のあいだに一度でも使う端末は切れません**。
上限（例: 1 年で必ず再ログイン）を設けないのは、定期的な再ログインが生まれるからです。代わりに止める手段を 3 つ持ちます。
アカウント画面での失効（一覧の「looptrack（ホスト名）」を失効させると、対の更新トークンも止まります）・利用者の無効化・再利用の検知です。
90 日は、使わなくなった端末（持ち出し・廃棄）に残った値が自然に無効になるまでの長さです。アクセストークンの既定の有効日数とも揃えてあります。
アクセストークンを 1 時間のように短くしても、ファイルが漏れた場合の防御にはなりません。credentials.json には更新トークンも並んで置かれるからです。

#### CLI の自動更新

| 項目 | 決定 |
| -- | -- |
| 対象 | credentials.json から取ったトークンのうち、`refresh_token` と `client_id` が控えてあるものです（`LOOPTRACK_TOKEN` と呼び出し側が明示したトークンは対象外） |
| 期限前 | 控えた `expires_at` まで 1 日を切っていたら、要求の前に取り直します（失敗しても今のトークンで続けます） |
| 401 | 保存した更新トークンで取り直し、credentials.json を書き換えて要求を 1 回だけやり直します。SessionStart の summary と導入済み通知もこの経路を通ります |
| 同時実行 | フックと Bash が同時に走ると、同じ更新トークンを 2 回出してしまいます。するとサーバが再利用とみなして系列を止めます。そこで `credentials.json.lock` の排他ロック（fcntl）の中で読み直し、**ほかのプロセスが先に取り直していればその値を使います**（fcntl の無い環境ではロックしません） |
| 失敗 | 更新トークンが期限切れか失効（invalid_grant）なら控えから消し、「ログインの有効期限が切れました（…）（… login --browser でログインし直してください）」で終えます。接続できないときは消しません |
| 互換 | 更新トークンの無い応答（古いサーバ）も、そのまま保存して使えます。新しいキーは credentials.json の中だけにあり、API に送る項目は増えません |

#### 安全（受け入れ条件との対応）

- 待ち受けは `127.0.0.1` だけです（`localhost` の名前解決や、全インタフェースでは待ちません）。state の検証もあります（`internal/client/cli/login_test.go`）。
- トークンはコマンドの引数・標準出力・ログ・例外のメッセージに出ません。コールバックの要求の記録も出しません（同上・`internal/server/cli_login_test.go`）。
- credentials.json は 0600 です（書き換えは一時ファイルからの置き換え）。グループや他者が読める権限なら、読み込みを拒否します。
- サーバ側では、ループバックのポートの照合・https の完全一致・resource・更新の入れ替え・再利用の検知・失効との連動・期限を確かめています（`internal/server/oauth_refresh_test.go`）。

#### 採らなかった案

| 案 | 理由 |
| -- | -- |
| `login` の既定をブラウザにし、貼る方式を `--token` にする | 既にある手順の意味が変わるからです（`--browser` を明示して選ぶ形にしました） |
| CLI 向けだけ無期限のトークン | 失効の手段が画面だけになり、使わなくなった端末の値が残り続けます。更新トークンの 90 日（使えば延びる）で同じ使い勝手になります |
| 更新トークンの絶対期限 | 定期的な再ログインが生まれます（上の「期限の理由」）。止める手段は失効・無効化・再利用の検知で足ります |
| 再利用の猶予時間（直前の値を数秒だけ許す） | 検知が弱くなります。CLI はロックで同時の更新を防ぎます |
| 待ち受けを `localhost` にする | 名前解決で IPv6 やほかのアドレスに向く環境があります。RFC 8252 §10.3 もループバックの IP リテラルを勧めています |

### 3-3. ローカルモードとセットアップ未完了

1 人で手元だけで使うときは、認証を省きます（127.0.0.1 に固定します）。ここではその実装と、管理者がいないサーバの扱いを説明します。

#### ローカルモード（`LOOPTRACK_LOCAL_MODE=1`）

| 項目 | 決定 |
| -- | -- |
| 有効化 | `looptrack serve` の環境変数 `LOOPTRACK_LOCAL_MODE=1`（`server.Config.LocalMode`）で有効になり、起動ログに `local_mode` が出ます。`looptrack serve --env-file <path>` で、`looptrack setup` が書く `.env` を読めます。形式は KEY=VALUE です。`#` の行・`export `・値の引用符を許し、展開はしません。既に決まっている環境変数が優先です |
| 待ち受け | `LOOPTRACK_LISTEN` の既定を `127.0.0.1:8090` にし、ホストは `127.0.0.1`・`::1`・`localhost` だけを許します（`server.CheckLocalListen`）。`0.0.0.0`・`:8090`（全アドレス）・LAN のアドレス・`127.0.0.2` などは**起動エラー**で、DB に触れる前に止まります |
| 既定の変更 | `LOOPTRACK_COOKIE_SECURE` の既定は false です（http で使うため）。`LOOPTRACK_TRUSTED_PROXIES` の既定は空です（前段のプロキシを置かない前提で、置くなら明示します）。HSTS は出しません |
| 利用者 | **無効化されていない管理者のうち ID が最小の人**です（`store.FirstActiveAdmin`）。要求ごとに引きます。通常モードのセッションやトークンの検証と同じく 1 回の問い合わせで、無効化や役割の変更がすぐ効きます。`looptrack setup` が作る最初の管理者が、ふつうはこれにあたります |
| Web | ログインを経ずに自動でログインします。有効なセッションが無い（または別の利用者のもの）ときは、その管理者のセッションを作って Cookie を返します（`MFAPassed`・`TOTPVerified=false`）。二段階認証の必須の検査は、ローカルモードでは見ません。`/login`・`/login/totp*` は `/looptrack/` へ転送し、ログアウトは何もせずに `/looptrack/` へ戻します（メニューにも出しません）。POST の CSRF トークンの検査は通常モードと同じです。アカウント設定・利用者管理などの画面もそのままです |
| REST API | `Authorization` を**見ません**（無効なトークンが付いていても通します）。画面のセッション Cookie があれば経路 `web` として扱い、通常モードと同じく CSRF（`X-CSRF-Token`）を検査します。無ければ経路 `api`（`X-Looptrack-Client: cli` なら `cli`）として通します。トークン ID は無し（NULL）です |
| MCP | 同じ利用者として通します。SDK の `RequireBearerToken` は Authorization が無いと検証を呼ばないので、ローカルモードでは内部で置き換えて渡します。記録の経路は `mcp` です |
| CLI | 画面・MCP と同じく、**トークンの発行もログインも要りません**。資格情報（`LOOPTRACK_TOKEN`・`credentials.json`）が無くサーバがローカルモードなら、`Authorization` を付けずに呼びます（`internal/client/api` の `Do`）。ローカルモードかどうかは、**認証の要らない `/healthz` の応答ヘッダ `X-Looptrack-Local-Mode: 1`** で確かめます。これを名乗るのはローカルモードのサーバだけで、結果はプロセスの中に覚えて 1 回しか引きません。引くのは URL がこの機械のループバックの http（待ち受け・Host の検査と同じ `127.0.0.1`・`::1`・`localhost` の 3 つ）のときだけです。それ以外の URL では名乗りを引かずに「アクセストークンがありません」と案内します。名乗らない・届かない・それでも 401 が返るサーバも、同じ案内で終えます。**認証が緩むのは、実際に認証を省いているサーバに対してだけです**。トークンがあるときも名乗りは引かず、`Authorization` を付けます。トークンの有無で分かれるほかの処理（`doctor`・SessionStart の summary・`init` の次の手順・kit の取得）も同じ判定を使います |
| 記録 | `issue_events` などの操作者はその管理者です（`actor_user_id`）。経路は上のとおりです |
| OAuth・配布・導入通知 | 認証の中間層（`api()`・`web()`・MCP の検証）で決まるので、ハンドラは同じです。`/api/v1/dist`（`bin/` を含む）・`installed`・MCP の setup ツールも管理者の名で通ります。`/setup/<券>/`（`bin/` を含む）は、通常モードと同じく券で通します |

#### ローカルモードのブラウザ経由の攻撃への備え

認証を省くと、利用者のブラウザで開いた**別のサイト**が `http://127.0.0.1:8090/looptrack/…` を叩けてしまいます。そこで認証の代わりに、次の対策で止めます（ServeHTTP の先頭・全経路）。

| 攻撃 | 対策 |
| -- | -- |
| DNS rebinding（攻撃者のドメインを 127.0.0.1 に向け、同一オリジンとして読む・書く） | **Host ヘッダ**のホスト名が `localhost`・`127.0.0.1`・`[::1]` 以外なら 421 を返します（読み取りと healthz も含みます）。ポートは見ません |
| 別サイトからの変更（フォームの POST・`fetch` の単純リクエスト・`text/plain` の JSON） | GET・HEAD・OPTIONS 以外について、`Sec-Fetch-Site` があり `same-origin` / `none` 以外（`cross-site`・`same-site`）なら 403 にします。`Origin` があり `scheme://Host` がこのサーバと一致しなければ、これも 403 です（`null` や同じ機械の別ポートも拒否します）。どちらも無い要求（CLI・MCP クライアントなどブラウザ以外）は通します。画面の POST は、これに加えて CSRF トークンを検査します |
| 別サイトからの読み取り | CORS のヘッダを出さないので、応答は読めません。`frame-ancestors 'none'`・`X-Frame-Options: DENY` も通常モードと同じく出します |
| 同じ機械の他の利用者・プロセス | **防ぎません**（認証を省く前提のため）。複数人で使う機械では通常モードを使ってください |

採らなかった案は次のとおりです。

- ローカル用の固定トークンをファイルに置き、CLI・MCP に渡す案。「認証を省く」ことにならず、設定の手間も増えます。デスクトップ版が起動時に CLI 用のトークンを発行して置く案も、同じ理由で採りません。失効や置き場の権限の面倒も増えます。
- 利用者ガイドに「最初に一度だけ `login --browser` をする」と書くだけにする案。画面と MCP には要らないのに CLI だけ要る、という食い違いが残ります。
- ループバックの URL なら名乗りを確かめずにトークンなしで送る案。通常モードのサーバを手元で試しているときの要求まで変わってしまいます。名乗りを 1 回引けば、影響はローカルモードのサーバだけに閉じます。
- Origin が無い変更系の要求を拒否する案。CLI・MCP クライアントが通らなくなります。ブラウザは変更系に Origin か Sec-Fetch-Site を付けるので、無いものはブラウザ以外とみなせます。

#### 管理者 0 人のとき（通常モード・ローカルモードとも）

| 経路 | 応答 |
| -- | -- |
| 画面（`/looptrack/` 配下・ログイン画面・POST `/login` を含む） | 503 の「セットアップ未完了（looptrack setup を実行してください）」を返し、ログインは受け付けません。**ローカルモードでは初回設定の画面**を出します（下の「画面版の初回設定」） |
| REST API（`/looptrack/api/…`・`dist` を含む）・MCP・OAuth（`/looptrack/oauth/…`）・`/looptrack/setup/<券>/…` | 503 の JSON `{"error":{"code":"setup_required","message":"…looptrack setup を実行してください"}}` と `Retry-After: 60` を返します |
| 除外 | `/looptrack/healthz`（監視）・`/looptrack/static/…`・OAuth のメタデータ（`/.well-known/…`） |

「管理者」は、無効化されていない `role = 'admin'` の利用者です（`store.CountActiveAdmins`）。一般の利用者や無効化された管理者しかいなければ、未完了のままです。
通常モードでは、一度いると分かったら以後は数えません。画面は管理者を 0 人にする変更を拒否しますが、管理コマンドで 0 人にしたときは再起動まで検査しません。0 人の間は要求ごとに数えるので、`looptrack setup` で作られればサーバを再起動せずに通ります。
ローカルモードは要求ごとに利用者を引くので、その結果で判定します。テストでは `Config.AllowNoAdmin` でこの検査を外せます（serve は常に検査します）。

#### setup の直後にブラウザだけで起票できるようにする

setup の直後はプロジェクトが 0 件です。プロジェクトを作っても、管理者は参加（`member set`）するまで閲覧のみになります。
ターミナルを開かずに 1 件目を起票できるよう、次の 3 つを組み合わせます。

| 手当て | 決定 |
| -- | -- |
| (a) setup の⑥「最初のプロジェクト」 | slug・接頭辞・表示名を聞き、作ったら最初の管理者を admin で参加させます。対話の既定は、ローカルなら `main`（接頭辞 `MAIN`・表示名 `main`）、チームなら `-`（作らない）です。`--yes` では、`--project <slug>`（`--project-prefix`・`--project-name`）があるときだけ作ります。後の 2 つの既定は、slug を英大文字にしたものと slug です |
| (b) ローカルモードの全プロジェクトへの参加 | 要求ごとに（利用者を引いた直後）、参加の行が無いすべてのプロジェクトへローカルの利用者を admin で参加させます（`store.ProjectsWithoutMember`・`AddMemberIfAbsent`）。役割を要求ごとに計算するのではなく、**行を作ります**。担当者の候補（`AssignableMembers`）と一覧（`MemberProjects`）も参加の行で決まるからです。**既存の行は変えません**。画面で自分を viewer・editor にしたものはそのままで、外した参加は次の要求で admin に戻ります。チームのサーバ（通常モード）では行いません |
| (c) 画面からプロジェクトを作る | `POST /looptrack/admin/projects`（slug・prefix・name）です。MCP の `create_project`（slug・name・description・prefix・width）も同じ処理（`service.CreateProject`。管理者でなければ Forbidden）を呼びます。setup がプロジェクトを見つけられないときは、管理者には `create_project` で作れること（prefix と width は後から変えられないので、AI が値を利用者に確かめる）を、管理者でない利用者には管理者に頼むことを、エラーの文面に添えます。管理者だけが使え、管理者以外には 404 を返します。CSRF の検査もあります。プロジェクトの作成と作った人の admin での参加を 1 つのトランザクションで行い、そのプロジェクトのボードへ移ります。プロジェクト管理の画面に「プロジェクトを作る」を置き、ハブの空の表示と案内からそこへリンクします。チームのサーバでも使えます。権限の規則は同じです。作った人は明示的に参加し、ほかの管理者は参加するまで閲覧のみです |

最初の管理者・二段階認証の設定・最初のプロジェクト・参加の行は、`setupwiz.Provision` が 1 つのトランザクションで作ります。`looptrack setup` と画面版の初回設定が同じ関数を呼び、失敗したら何も残しません。そのため `store.SetTwoFactorPolicy` とは別に、呼び出し元のトランザクションで行う `SetTwoFactorPolicyTx` があります。
`looptrack user add` はこの流れを通らず、単独で利用者を作ります（`addUser`）。

採らなかった案:

- (b) だけにする案。プロジェクトの作成に `looptrack project create` が要り、ターミナルなしでは終わりません。
- (b) を役割の計算で行う案。担当者の候補や一覧に出ず、画面ごとに扱いが分かれます。

#### 画面版の初回設定（デスクトップ版の前提）

「未設定のデスクトップ版」は、`looptrack serve` に `LOOPTRACK_LOCAL_MODE=1` と `LOOPTRACK_DSN` だけを与えた状態とみなします。

| 項目 | 決定 |
| -- | -- |
| 起動 | ローカルモードの serve は、起動時に `store.Migrate` でスキーマを最新にします。`LOOPTRACK_DSN=sqlite:…` だけではテーブルが無いからです。チームのサーバでは `looptrack migrate` を別に実行します |
| 秘密鍵 | ローカルモード + SQLite（ファイル）で `LOOPTRACK_SECRET_KEY` が無ければ DB の隣の `<db>.secret-key` を読み、無ければ作ります。権限は本人だけです（unix は 0600・Windows は本人だけの ACL・`internal/privfile`）。DB のディレクトリも、無ければ本人だけで作ります（`privfile.MkdirAll`）。壊れた・読めないファイルは作り直さず、起動エラーにします。鍵を失うと二段階認証の登録が使えなくなるからです。それ以外（通常モード・MySQL・`sqlite::memory:`）は、鍵が無ければエラーです |
| DB のファイルの権限 | SQLite の本体・`-wal`・`-shm` は本人だけにします（パスワードのハッシュ・暗号化した TOTP の秘密・トークンのハッシュ・本文が入るため）。本体は、`store.Open` が開く前に本人だけの空のファイルとして作ります（`privfile.CreateEmpty`）。SQLite に作らせると umask のまま 0644 になるからです。unix の `-wal`・`-shm` は、SQLite が本体の mode と所有者を引き継いで作ります（fchmod で umask の分も戻す・modernc.org/sqlite で実測）。Windows の SQLite は `-wal`・`-shm` をセキュリティ記述子なしで作るので、権限はディレクトリの継承する ACE で決まります。`-wal` は閉じると消えて作り直されるので、先に作っておいても続きません。そこで setup や鍵のファイルで DB のディレクトリを新しく作るときに、本人だけの継承する ACL にします（`privfile.ProtectDir`）。既にあるディレクトリは狭めません。serve は起動時に本体・`-wal`・`-shm` のどれかを本人以外も読めれば警告し、直し方（`chmod 600 …`・Windows は icacls）を添えます。自動では直しません。サービスの利用者とグループで共有する運用を壊さないためです |
| いつ出すか | ローカルモードで有効な管理者が 0 人の間だけ、画面の経路（API・MCP・OAuth 以外）を初回設定にします。`GET /looptrack/first-run` がフォームです。ほかの GET は `/looptrack/first-run` へ 303、ほかの POST は 503 です。API・MCP は 503 の `setup_required` を返します（案内は「ブラウザで初回設定をするか looptrack setup」）。通常モードは 503 の「セットアップ未完了」だけで、初回設定の画面は出しません。公開サーバに「最初の管理者になれる窓」を開けないためです（§10） |
| 問い | ④最初の管理者（ログイン名・表示名・パスワード 2 回）⑤二段階認証（必須 / 任意・既定値で決めない）⑥最初のプロジェクト（既定 `main`・空では作らない）を聞きます。①使い方 ②保存先 ③待ち受けは、起動の設定で決まっているので聞きません。検査は `setupwiz.PlanFirstRun`（ターミナル版と同じ検査の関数）、DB の処理は `setupwiz.Provision` です。`FirstRun` では、有効な管理者が 0 人であることと同じログイン名が無いこと（無効化された管理者を含む）を、トランザクションの中で確かめます。そうしないと初回設定が出続けるからです |
| 済んだ後 | `/looptrack/first-run/done`（画面の認証の下・ローカルモードだけ）へ移ります。イシューの画面（最初のプロジェクトのボード・無ければハブ）への入口と、Claude Code・Codex・Copilot（VS Code・CLI）の MCP の接続設定を出します。文字列は `setupwiz.MCPConfigs` で作ります。`looptrack setup` の最後の案内と同じ関数で、最初のプロジェクトがあれば `X-Looptrack-Project` を付けます。`/looptrack/first-run` は経路を持たず、gate が管理者 0 人のときだけ出します。そのため管理者ができたら 404 になり、二度と出ません |

守りは次のとおりです（§10）。

| 攻撃 | 対策 |
| -- | -- |
| 同じネットワークの他の機械からの初回設定 | 接続元（`RemoteAddr`）が loopback（127.0.0.1・::1）でなければ 403 にします。表示・送信・転送のどれも同じで、X-Real-IP は見ません。待ち受けも 127.0.0.1 に固定しています（上の表） |
| DNS rebinding・別サイトからの送信 | ローカルモードの Host・Origin・Sec-Fetch-Site の検査（上の表）が先に効きます |
| CSRF（ログイン前なのでセッションが無い） | Cookie に結びつけたトークン（double submit）を使います。`GET /looptrack/first-run` で 32 バイトの乱数を作り、`looptrack_first_run` の Cookie（HttpOnly・SameSite=Strict・Path=/looptrack/first-run）とフォームに入れます。送信のときに、両者の一致を定数時間で比べます |
| 同時の送信で管理者が 2 人できる | サーバ内の mutex で 1 つずつ処理し、`Provision` もトランザクションの中で有効な管理者が 0 人であることを確かめます |

## 4. API（`/looptrack/api/v1`・JSON・Bearer トークンまたはセッション + CSRF）

| メソッド・パス | 用途 | CLI 対応 |
| -- | -- | -- |
| `GET /projects` | 一覧 + 集計（未クローズ・進行中・着手可能・不具合） | ハブ画面 |
| `GET /projects/{slug}` | 設定（prefix / width / rules） | `config --json` |
| `GET /projects/{slug}/issues?status=&type=&label=&ref=&assignee=&has_feedback=&all=&sort=&reverse=` | 一覧（`assignee` は `me` / login / `-`（未設定）で §9-2、`has_feedback` は未応答のフィードバックがあるものだけで §9-3-6） | `list`（`--has-feedback`） |
| `GET /projects/{slug}/ready?assignee=&sort=&reverse=` | 着手可能 | `ready` |
| `GET /projects/{slug}/issues.xlsx?status=&type=&label=&ref=&all=&sort=&reverse=` | 課題管理表（xlsx）を絞り込みつきで書き出す | `export --xlsx` |
| `POST /projects/{slug}/issues.xlsx`（`ids=` カンマ区切り・`filter=` 説明文） | 画面が並べている順のまま書き出す | ボードの「エクスポート」 |
| `POST /projects/{slug}/issues` | 起票（採番はトランザクション内） | `new` |
| `GET /issues/{id}` | 詳細（`?format=md` で現行 `show` と同じ全文） | `show` / `edit` |
| `PATCH /issues/{id}` | 項目・本文の更新（`If-Match: <version>`） | `push` / `set` |
| `POST /issues/{id}/comments` | コメント追記 | `comment` |
| `POST /issues/{id}/status` | 状態変更（`comment` 同時指定可・ルール検証・`assignee` 同時指定可） | `status` / `close` |
| `POST /issues/{id}/assign` | 担当者の変更（`{assignee, override_reason}`・§9-2） | `assign` |
| `GET /issues/{id}/verify` | 検証コマンドの一覧・本文の版・直近の記録（§9-3-3・viewer 可） | `verify --list` / `--last`・MCP `verify_issue` |
| `POST /issues/{id}/verify` | 手元で実行した検証コマンドの結果の記録（コメント + `issue_events` kind `verify`・§9-3-3・editor 以上） | `verify`・MCP `report_verify`（同じ検査・自己申告の印が付く） |
| `GET /projects/{slug}/matrix` | トレース表 + 警告 2 種 | `matrix` / `index` |
| `GET /projects/{slug}/summary` | SessionStart 用（① 進行中・ready 上位 ② In Review と滞留 ③ 未応答のフィードバック の 3 層と、トークン情報の未付与の件数・§9-3-7） | `summary` |
| `GET /activity?ids=&since=` | ID ごとの最終更新イベント時刻 | `activity` |
| `GET /projects/{slug}/guide` | 使い方とルール（共通規則 + プロジェクト別ルール + 運用文書・`?format=md`・§5-2） | `guide` |
| `POST /projects/{slug}/next` | ループ運用の着手（§5-2） | `next` |
| `GET /dist` ・ `GET /dist/{name}` | 配布するスクリプトの一覧（SHA-256）と本体（§5-2） | `init --source server` |
| `POST /projects/{slug}/install` ・ `GET /projects/{slug}/install` | 導入済み通知（配布物のハッシュ）と自分の導入状態（§6） | `summary --agent`（SessionStart）・`installed` |

エラーは `{"error": {"code": "...", "message": "日本語（次の行動を含む）"}}` の形で返します。ルール違反は 422 です。

**実装の取り決め**:

| 項目 | 内容 |
| -- | -- |
| 共通処理 | `internal/service` です。REST・MCP・Web が同じ処理を通ります。変更は 1 トランザクションで、行ロック → Document に復元 → `internal/domain` の変更関数 → 保存（コメントは追記のみ）→ `issue_events` の順に進みます。デッドロックは再試行します |
| 採番 | `projects` の行を `FOR UPDATE` して `counter + 1` を取ります（counter が正）。番号が既存と衝突したら 409 です |
| 時刻 | created / updated / コメントの見出しは、**プロジェクトの現地時刻（`service.Service.Loc`・既定 Asia/Tokyo）の分単位**です（人が読む値）。イベントは UTC の DATETIME(6) です |
| 時間帯の運び方 | 時刻を描く時間帯は `service.Service.Loc` の**1 か所だけ**に置きます。画面・`?format=md`・xlsx・PDF・台帳・CLI で食い違わせないためです。時刻を含む応答は、**最上位に `timezone`（IANA 名）を 1 つ**持ちます（`/issues`・`/ready`・`/summary`・`/activity`・`/issues/{id}/verify`・`/usage/requests`・`/usage/ledger`・集計 JSON）。経路ごとに別の運び方は作りません。**CLI は、応答に `timezone` が無いとき（それを載せる前のサーバ）は Asia/Tokyo で描きます**。端末のローカル時間帯には倒しません。新しい CLI と古いサーバの組み合わせで、いまの利用者の表示が黙って変わる退行を出さないためです。CLI は tzdata を埋め込むので（`_ "time/tzdata"`）、tzdata の無い環境でも IANA の名前を読めます |
| 状態コード | 入力誤り 400・未認証 401・閲覧のみの権限で変更 403・見えない / 無い 404（権限が無い場合も 404）・版の不一致 409（`error.current` に現在の全文）・規則による拒否 422・版の指定なし 428 |
| 404 の使い分け | **経路が無い**（存在しない URL）404 はコード `unknown_api` です（メッセージ `server.api.err.api_not_found`・`internal/server/server.go` のフォールバックハンドラ）。**資源が無い**（プロジェクト・イシューなどが無い、または権限が無い）404 はコード `not_found` です。分けているのは、「経路そのものが無い（= サーバがその機能を持たない古い版）」ことをクライアントが**文面ではなくコードで**機械的に判定できるようにするためです。**いまこの判定をしているクライアントはありません**。唯一の利用者だった CLI の `zero-bug-gate` は、ゼロバグゲートの撤去（§9-1）で一緒に消えました。今後これが要るときは `unknown_api` を見てください。`unknown_api` を返すより前の古いサーバは、経路が無いときも `not_found` と同じメッセージを返します。そこまで遡って支える必要があるときだけ、文面の比較を足します |
| 権限 | project_members の `viewer` は参照のみです。`editor` / `admin`（と利用者の admin）は変更できます |
| 経路の記録 | Bearer は `via=api`（`X-Looptrack-Client: cli` なら `cli`）、セッションは `web` です。`X-Looptrack-Session`（Claude Code のセッション ID、128 文字まで）は `issue_events.session_id` に記録します。`X-Looptrack-Agent`（`claude-code` / `codex` / `copilot`・ほかの値は読み捨てる）は `issue_events.detail` の `agent` に記録します。セッション ID を渡さない AI の操作の目印です。`X-Looptrack-Session-Kind`（`host` だけ・ほかの値は読み捨てて会話のセッション ID として扱う）は `issue_events.detail` の `session_kind` に記録します。トークン情報を付けられないセッション ID の目印です（§9-5「器のセッション ID」） |
| `GET /issues/{id}` | ID は大文字小文字を区別しません。`?project=<slug>` でプロジェクトの中に限れます（ファイルモードの find と同じ範囲）。`?format=md` は `show` と同じ全文です（CLI は末尾に改行を 1 つ足して表示します）。`ETag: "<version>"` を返します |
| `PATCH /issues/{id}` | `If-Match: <version>` が必須です。本文は `{"markdown": 全文}`、項目は `{"title", "type", "priority", "parent", "labels", "blocked_by", "traces", "refs"}` で送ります（併用はできません）。**id・created・status・assignee・コメント節（preamble とコメント）は変えられません**。担当は `/assign` で変えます。担当が他人なら 422 で、`override_reason` を付ければ通します（担当は替えない・§9-2）。状態は `/status`、追記は `/comments` です。送ったコメント節が現在のコメントの先頭部分なら、後から付いたコメントを保持します。クローズ済みは 422 です。updated はサーバが設定します。ファイル名（title 由来）は変えません |
| `POST /projects/{slug}/issues` | `{"title", "type", "status", "priority", "parent", "labels"[], "blocked_by"[], "traces"[], "refs"[], "body"}` です。既定値は CLI の `new` と同じです。リストの値は前後の空白を除き、空のものを捨てます。カンマ・改行は 400 です。**blocked_by / traces / refs は途中に空白を含む値も 400** です（§2-3・labels は空白可）。201 で `{"issue": 詳細, "path": "open/<ファイル名>"}` を返します |
| `POST /issues/{id}/comments` | `{"text"}` です（空文字も可）。クローズ済みにも追記できます（経緯を足せるようにするため）。`message` は CLI の出力です（`コメント追記: <ID>`） |
| `POST /issues/{id}/status` | `{"status", "comment"}` です。`messages` は CLI の出力行です（`<ID>: <旧> → <新>` と、コメントがあれば `コメント追記: <ID>`） |
| 一覧 | `list` / `ready` は `{"items": [...], "count"}` を返します。項目は frontmatter の値と `closed` `version` `file_name`、未知のキー（`extra`）です。**In Progress の項目には `other_session`（要求のセッション ID と最後に着手したイベントのセッション ID が比べられて違う）・`cross_path_session`（経路が違って比べられない）と `started_ago`（着手からの経過）が付きます**。判定は summary と同じで、CLI と MCP は「別のセッションが着手しています」の 1 行を出します。セッション ID が分からない経路では付きません。`all` / `reverse` は `1` / `true` です。`has_feedback=1` は未応答のフィードバックを持つものだけに絞り、**既定でクローズ済みも含めます**（`all` と同じ）。項目には `feedback_pending`（未応答の件数）が付きます（§9-3-6） |
| `GET /projects/{slug}/matrix` | JSON（rows / untested / orphans）を返します。`?format=md` は matrix.md と同じ文字列です |
| `GET /projects/{slug}/summary` | counts（open / in_progress / in_review / ready / open_bugs / by_status）・進行中・In Review・ready 上位（`?limit=`、既定 5）を返します。`usage_missing` は呼び出した利用者の AI 操作のうち、直近 7 日でトークン情報が付いていないものです（count・issues・command・message・§9-5）。`usage_requests` は未完了のトークンレポート作成依頼です（§9-5）。3 層の追加キー（§9-3-7・既存のキーは変えない）は、`in_review[]` の各項目の `review_since`・`review_hours`・`review_stale`（48 時間超）・`review_age`・`verify_self_reported`、`feedback`（`count`・`issue_count`・`issues[]`: id・title・status・pending・first_at・excerpt）、`counts` の `in_review_stale`・`feedback_pending` です |
| 変更の応答 | 起票・コメント・状態変更・更新の応答には、AI からの操作なら `usage_notice` が付きます（トークン情報の付与の指示・§9-5 の経路 ③） |
| `GET /activity` | `?ids=A,B&since=<UNIX 秒・小数可>` で、`items[]`（id・project・last_at（RFC 3339）・last_epoch・last_kind・last_via・events_since）を返します。見つからない ID や権限の無い ID は含めません。`now_epoch` も返します |

## 5. CLI

### 5-1. クライアント（CLI・hook・レポート）の形

利用者の手元で動くもの（CLI・鮮度ガード・トークン情報の収集と送信・トークンレポートの PDF・kit の hook）は
すべて Go で書き、サーバと同じ実行ファイル `looptrack` に入れます。**手元の依存を実行ファイル 1 つにする**ためです。

- Windows に最初から入っている処理系は限られていて、bash の hook も動きません（権限の確認・プロセスグループの停止・起動名・symlink など）。
- デスクトップ版は CLI を同梱します。実行環境ごと同梱すると重くなります。

ブラウザの画面（素の JavaScript）は対象外です。

| # | 項目 | 決めていること | 理由 |
| -- | -- | -- | -- |
| Q1 | 実行ファイルの形 | **1 つ（`looptrack`）**を build tag で 2 種類にビルドします: desktop（トレイつき。macOS は cgo）と headless（cgo なし。サーバ・CI・Linux の端末） | デスクトップ版は 1 つで済みます。サーバとクライアントを分けても共通部品（`internal/usage`・`internal/xlsxreport`・kit の埋め込み）は同じです |
| Q2 | hook の配線での実行ファイルの指し方 | PATH の名前（`looptrack hook …`）。解決できない端末では**手元専用の設定**（`settings.local.json` 等）に絶対パスを書きます | 共有の設定に利用者ごとのパスを入れないためです。GUI から起動した AI の PATH は OS や起動のしかたで違います |
| Q3 | 配布物の置き場 | GitHub Releases（`/api/v1/dist` の経路でも配ります） | 導入する側が外部のサーバに依存せずに取れます |
| Q4 | PDF の日本語フォント | **BIZ UDGothic（SIL OFL）を埋め込む**（+約 7MB） | OS のフォントを探すと OS や言語設定で結果が変わります。すると列幅の規則が崩れます |
| Q5 | Windows のトークンの保護 | ファイル + ACL（本人だけ） | 他の OS と同じ「ファイル 1 つを本人だけ読める形で置く」形に揃います |

#### 実行ファイルの形とサブコマンド

| サブコマンド | 中身 |
| -- | -- |
| `issue …` | イシューの操作（サブコマンド・引数・`--json`・出力・終了コード・決まった文言をゴールデンテストで固定します） |
| `hook <名前> --agent <AI>` | core の hook（鮮度ガード・usage の送信と spool・summary）と loop の hook 14 本・gates |
| `report pdf` | トークンレポートの PDF |
| `serve`・`setup`・`user`・`project` … | サーバ |
| `desktop` | デスクトップ版です。ローカルモードのサーバを上げてブラウザで開きます（`--background`・`--no-tray`・`--status`・`--quit`）。desktop ビルド（`-tags desktop`）はトレイつきです。引数なしの起動（ダブルクリック）もこれになります。headless ビルドでもサブコマンドとしては動きます。トレイは出ず、止めるには `--quit` かシグナルを使います。引数なしなら使い方を出します。§5-4 |
| `self-update`・`doctor` | 版の更新・配線と PATH の確認 |

- 実行ファイルの大きさは 30〜45MB です（PDF 用のフォント込み）。
- **イシューをファイルで持つモードはありません**。元データは DB で、比較テストは合成フィクスチャで足ります。
- 資格情報の置き場は OS で違います。全プロジェクトで共有します。
  - Windows: `%APPDATA%\looptrack\credentials.json`（`APPDATA` が無ければ `%USERPROFILE%\AppData\Roaming`）
  - 他の OS: `$XDG_CONFIG_HOME/looptrack/credentials.json`（無ければ `~/.config`）
  - ロックは flock / LockFileEx（`credentials.json.lock`）で取ります。
- Windows の保護（Q7）は `internal/privfile` に一本化しています。サーバの `.env`・SQLite の DB と同じ実装です。
  - 書くとき: 同じフォルダに一時ファイルを作ります。中身を書く前に DACL を「本人に全権」の ACE 1 つだけにし、親からの継承を切ります
    （`PROTECTED_DACL`）。書いてから rename で置き換えます（rename しても DACL は保たれます）。フォルダ（`%APPDATA%\looptrack`）には
    作ったときだけ同じ DACL を中へ継承する形で付けます。中のロックのファイルも本人だけになります。既にあるフォルダは変えません。
  - 読むとき: 本人・SYSTEM・Administrators 以外に読み取り（`FILE_READ_DATA`・`GENERIC_READ`・`GENERIC_ALL`）か権限の書き換え
    （`WRITE_DAC`・`WRITE_OWNER`）を許す ACE があれば読まずに拒否します。そのときはログインし直すよう案内します（書き直すと本人だけに戻ります）。
    SYSTEM と Administrators は OS の既定で `%USERPROFILE%` の下を読めるので許します。
  - 確かめ方: `icacls "%APPDATA%\looptrack\credentials.json"` が本人の `(F)` の 1 行だけで、`(I)` の継承の印が無ければ正しい状態です。
    テストは `internal/client/cred/fs_windows_test.go` です。CI の Windows で、本人だけの ACL と Everyone に読める ACL の拒否を確かめます。
- Windows の端末と文字:
  - 引数と環境変数は UTF-16 の API で受けます。端末（コンソール）への出力は Go が UTF-16 で書くので、コードページ（cp932）に関係なく化けません。
  - パイプとファイルへは UTF-8・LF で書きます（PowerShell で受けるときは `[Console]::OutputEncoding` を UTF-8 にします。AI-GUIDE）。
  - 読む側では、作業コピーの BOM・CRLF を push の前に edit 時の形へ戻します。
  - `--from-report`・`report pdf` の入力の BOM・UTF-16（PowerShell 5.1 の `>`）は UTF-8 にします。
  - verify の出力が UTF-8 として不正なら ANSI コードページとして読み直します。
  - 表示するパスは OS の区切り（`\`）のままです。init は symlink を作りません（kit の md は copy）。

#### hook の入出力の正規化（`internal/hookio`）

- 入力は共通の **Event** に直します。Claude Code・Codex・Copilot の hook の入力（JSON の形・キー名）はそれぞれ違うからです。
  Event は Agent・Name・SessionID・TranscriptPath・CWD・Tool などを持ちます。hook の本体は Event だけを見ます。
  結果は共通の **Result**（Block・Deny・Context・SystemMessage）から AI ごとの出力の形と終了コードに直します。
- どの AI から呼ばれたかは**配線で `--agent` を必ず渡します**。入力からの推測は予備にとどめます（誤判定すると出力の形を間違えるためです）。
- **fail-open**: panic・タイムアウト・想定外の入力でも exit 0 で終わり、作業を止めません。送信の切り離しには POSIX で setsid を、Windows で DETACHED_PROCESS を使います。
- runaway の検知では OS ごとにプロセスの木を作ります（/proc・sysctl・ToolHelp32）。verify の打ち切りには POSIX でプロセスグループを、Windows で Job Object を使います。
- kit の manifest は `runner: looptrack` で、windows・copilot の欄を持ちます（§8）。hook のケースは Go の表駆動テストで確かめます。

#### 配布と更新

- ビルドは 6 対象（linux / darwin / windows × amd64 / arm64）と `SHA256SUMS` です。`/api/v1/dist` に binaries の一覧を足します。
- setup（MCP）は接続した AI の OS に合う取得コマンドと SHA-256 を返します。置き場は管理者権限の要らない場所です（`~/.local/bin`・`%LOCALAPPDATA%\Programs\looptrack`）。
  デスクトップ版はアプリの中の実体を使い、メニューからこの置き場にリンクを作ります。
- 導入済み通知（§6）に `client.version` を足し、**実行ファイルの版**で【配布スクリプトの更新】を出します。直すのは `looptrack self-update` です。
- macOS の CLI はブラウザ経由で取得しなければ検疫が付きません。公式の配布物の darwin の実行ファイルは配布元の Developer ID で署名・公証します。
  Windows は当面署名しません。`SHA256SUMS` は minisign で署名し、`self-update` は埋め込んだ公開鍵で確かめます（手順は [RELEASE.md](RELEASE.md)「署名」）。

#### トークンレポートの PDF

signintech/gopdf（MIT）と BIZ UDGothic（ライセンス文を NOTICE に同梱）を使います。表のモデルは `internal/reporttable` に置いて Excel と共通にします。
列幅は実フォントの寸法で決めます。unipdf は AGPL なので使いません。

#### 互換性の確かめ方

| 対象 | 方法 |
| -- | -- |
| CLI | httptest の偽 API がフィクスチャの応答を返し、受けた要求（method・path・本文・X-Looptrack-* ヘッダ）を記録します。それを golden と比べます。golden には stdout・stderr・終了コード・要求の列・書いたファイルを記録します（全サブコマンド × 正常・401・404・409・422・通信断 × `--json` の有無）。時刻・一時パス・版・ホスト名・乱数は正規化します |
| 変更系 | 上に加えて、実サーバで同じ台本を流した最終 DB 状態（イベントを含む）を比べます |
| usage_snapshot（最重要） | 合成の会話記録（Claude Code: message.id の重複・subagents / Codex: token_count）で payload の**完全一致**を見ます。実データはリポジトリに入れません |
| hook | AI ごとの匿名化した入力のフィクスチャで、Event と Result の往復・fail-open を確かめます |
| PDF | 抽出したテキストと列幅で比べます |
| CI | 3 OS で Go のテストを実行します（linux・Windows は毎回、macOS は週次と手動の `full`。契機は手動と週次だけ。RELEASE.md「CI」） |

### 5-2. 導入・使い方・着手（init / guide / next）

AI が 1 回の操作でこのシステムの使い方とルールを理解し、ループ運用に組み込めるようにします。
回数はどれも 1 回です。導入は `looptrack issue init`・ルールの読み込みは `guide`・着手は `next` で行います。

#### guide（使い方とルールを 1 回で返す）

| 項目 | 決定 |
| -- | -- |
| 返すもの | 1 つの Markdown です。中身は下の ① ② ③ です |
| 経路 | `GET /projects/{slug}/guide`（JSON: `markdown` `common` `rules[]` `doc` `doc_source`。`?format=md` で Markdown だけ）・`looptrack issue guide [--json]`・MCP の `guide` ツール。3 つとも同じ言語で同じ Markdown を返します（テストで一致を確かめます） |
| 共通規則の置き場 | **イメージに埋め込みます**（日本語が元の定義の `internal/guide/common.md`・英語は `internal/guide/en/common.md`）。ツール名やサブコマンドに依存し、サーバの版と一緒に変わるためです。**言語は組み立てる側が決めて渡します**（REST は `reqLang`・MCP は接続の言語。`guide.Compose(lang, …)`）。運用文書（`in.Doc`）は利用者が登録した自由文です。どちらの言語でも原文のまま返します |
| 運用文書の置き場 | **DB に持ちます**（`project_guides`・マイグレーション 0005）。登録は管理者が `looptrack project guide set <slug> <file\|-> [--source 名前]`（`show` / `clear`）で行います。元の文書は従来どおりファイル（例 `docs/projects/<slug>.md`） です。直したら登録し直します（`deploy/rules/<slug>.json` → `project rules set` と同じ運用）。**イメージに埋め込まない理由**: 埋め込むと新しいプロジェクトの追加や文書の直しのたびに配置（ビルド・停止）が要ります。別リポジトリのプロジェクトが自分の文書を載せることもできません。`projects` の列にしないのは、一覧や権限判定で毎回読む行を重くしないためです |
| 権限 | プロジェクトの閲覧権限があれば読めます（viewer も可）。見えないプロジェクトは 404 です |

guide が返す Markdown の中身:

1. 共通規則: `docs/AI-GUIDE.md` の要点です。次を含みます。
   - 冒頭の「このシステムで何が整うか」（3 層のループ。文面は §9-3-8）
   - ループの 1 周（人の判断待ち・外からの反応を含む。§9-3-5）
   - 起票と編集・変えられないもの・困ったとき
2. プロジェクト別ルール: `projects.rules` をルールごとの日本語に直したものです。知らないキーは隠さず JSON をそのまま出します。
3. 運用文書: `docs/projects/<slug>.md` 相当です。見出しを 2 段下げて ① ② と構造をそろえます。

#### next（ループ運用の着手）

経路は `POST /projects/{slug}/next`（`{dry_run, comment, override_reason, types[], assignee}`）・`looptrack issue next [--dry-run] [--comment] [--type] [--override] [--assignee] [--json]`・MCP の `next` ツールです。実装は `internal/service/next.go` にあります。

| 規則 | 内容 | 理由 |
| -- | -- | -- |
| 1. 着手中を優先 | **自分が担当の In Progress**（担当者で見ます）があれば、状態を変えずにそれを返します（`resumed`）。細かい決まりは表の下の「規則 1 の詳細」にあります | 周の途中で呼び直しても作業対象が変わりません。同じ利用者の AI を並行して動かしても、他のセッションの着手中を横取りしません。§9-2 は利用者単位なので、セッションの区別はここで残します。epic は子の束ねで、作業の単位ではありません。着手中の epic を返すとループが実作業に進めません。`types` を指定しても着手中が優先されて効きませんでした |
| 2. 候補 | 着手可能（`ready`）のうち、次をすべて満たすものです。状態が **Todo**（Backlog・In Review は対象外）で、型が `types`（既定は **epic 以外**）に入ること。**未クローズの子イシューが無い**こと（子から先に着手します）。**担当が自分か未設定**であること（§9-2。他人・印付きの担当は取りません）。並びは `ready` と同じで、優先度順・同順位は ID 昇順です。ただし規則 1 の `containers` があれば、その子孫（parent を辿って届くもの）を先に見ます | 要件や epic のように子で進めるものに誤って着手しないためです。他人に割り当てたものも取りません |
| 3. ルールで見送り | 候補ごとに In Progress への変更をプロジェクト別ルールで判定します。違反する候補は見送って次を見ます（`skipped[]` に理由）。たとえば `forbid_status` が In Progress を禁じていれば、その候補は見送ります。最初に通った候補を In Progress にします（`started`・`--comment` は同時コメント）。`dry_run` は何も変えずに返します（`would_start`） | プロジェクト固有の分岐をコードに入れずにルールの意図に沿えます |
| 4. 全部見送り | 候補はあったのに全部ルールで見送ったときは、最初の違反を 422 で返します。`next --comment "…"`（コメント必須のルール）や `--override` を案内します | 次に打つコマンドを示すためです |
| 5. 二重着手の防止 | 状態の変更は、ロックした時点の状態が判定時と同じ（Todo）ときだけ行います。違えば見送って次の候補へ進みます。担当の規則（§9-2 の R2）もロックの中で判定します。他人が担当になっていれば見送ります | 同時に呼んだ 2 つの AI が同じイシューに着手しないようにします |
| 6. 担当 | 着手（started）したときに担当が未設定なら自分が担当になります（§9-2 の R1）。`assignee`（CLI は `next --assignee`）を指定すると着手と同時にその人を担当にします（resumed・dry_run では使いません） | |
| 権限 | editor 以上が要ります（dry_run も）。viewer は 403 です | |

規則 1 の詳細:

- セッション ID があるときは、**自分の別のセッション（または端末）が In Progress にしたもの**を除きます。見るのは `issue_events` の最後の着手の主体とセッションです。他の利用者が着手して自分に割り当てたものは取ります。
- **除くのは、両方のセッション ID が分かっていて種類も同じときだけです**（`service.ComparableSessions`）。
- 着手のイベントにセッション ID が無いもの（画面・送らないクライアント・人の操作・取り込んだまま）は見分けられません。種類の違う ID（MCP の接続 ID）も同じです。これらは従来どおり自分の着手として扱います。
- **ただし種類が違って比べられないとき（どちらの ID も分かっているとき）は `cross_path_sessions[]` に並べます**。`text` には「別の経路（CLI / MCP）で着手されています。同じセッションかは判定できません」を出します（着手中としては返します）。
- セッションが無ければ担当が自分のものを全部見ます。担当が未設定の In Progress（取り込んだまま等）は従来どおり着手のイベントで判定します。
- 複数あれば優先度順の先頭を返し、残りを `others` に並べます。
- **作業の単位でないものは着手中として返しません**。未クローズの子を持つもの（束ね。epic・要件など）は `containers[]` に並べます。型が `types`（既定は epic 以外）に入らないものは `outside_types[]` に並べます。どちらも `text` に 1 行ずつ出ます。残りが無ければ規則 2 へ進みます。
- **同じ利用者の別のセッションが着手したものは `other_sessions[]` に並べ、`text` に「別のセッションが着手しています」の 1 行を出します**。担当者欄は同じ利用者なので、状態からはセッション ID でしか見分けられません。

応答は次の形です。

`{action, message, issue（全文つき）, from, acceptance（「## 受け入れ条件」節）, verify（「## 検証コマンド」節。無ければ null。§9-3-1）, related {parent, blocked_by[], traces[], children[]}（各 id・title・type・status。無い ID は missing）, others[], containers[], outside_types[], skipped[], next_steps[], text}`

- `text` には受け入れ条件の後に「検証コマンド（`looptrack issue verify <ID>` で実行して記録する）」の一覧が出ます（節があるときだけ）。
- `text` は人と AI が読む形です。CLI はこれをそのまま出します。MCP のツール結果の本文も同じです。
- 候補が無ければ `action: none` になります。`ready` / `summary` を見て利用者に相談するよう返します。
- 着手（started）したら CLI はトークンのスナップショットを送ります（§9-5 の経路 ①。op は status）。
- started の応答にも、他の状態変更と同じく `usage_notice`（§9-5 の経路 ③）が載ります。CLI は付与に失敗したときだけ表示します（`after_change`）。MCP はツール結果の本文の末尾に付けます。
- In Progress への判定も `set_status` と同じ関数（`checkStatus`）を通ります。そのため `usage.require_on_close` などトークン計測の規則も同じように効きます（In Progress は対象外）。

#### init（導入の一発化）

`looptrack issue init --project <slug> [--agent claude-code|codex|copilot|other] [--url] [--dir] [--source link|copy|server] [--dry-run] [--force] [--mcp] [--no-freshness] [--no-usage] [--no-summary] [--no-skill]`。
プロジェクトを載せるときの手作業（[ADD-PROJECT.md](../ADD-PROJECT.md) §4）を 1 回のコマンドにまとめます。

| AI | 書くもの |
| -- | -- |
| Claude Code（既定） | `.claude/settings.json` などに書きます。詳細は表の下の「Claude Code に書くもの」です |
| Codex | `.codex/hooks.json` などに書きます。詳細は表の下の「Codex に書くもの」です |
| GitHub Copilot | `.github/hooks/looptrack.json` などに書きます。詳細は表の下の「GitHub Copilot に書くもの」です |
| その他 | **案内文だけ**を出します（CLI の置き方・環境変数・指示ファイルに書くこと・MCP の URL・フックがあれば `looptrack hook usage`・`installed --agent other`）。`--source` / `--dist` を付けたときは CLI の配置だけ行います（setup ツールの手順が使います） |

Claude Code に書くもの:

- `.claude/settings.json` の `env`（`LOOPTRACK_API_URL`・`LOOPTRACK_PROJECT`）
- 同じファイルのフック
  - SessionStart の `summary --limit 12 --agent claude-code`（導入済み通知を兼ねます・§6）。`--agent` の無い古い配線には付け足します。
  - 鮮度ガード（UserPromptSubmit / PostToolUse の mark・Stop の check）
  - トークン計測（PostToolUse `mcp__.*`・Stop・SessionEnd の `usage`）
- コマンドのルートは `${CLAUDE_PROJECT_DIR:-$(git rev-parse --show-toplevel 2>/dev/null || pwd)}` にします。VS Code の Copilot や Copilot CLI もこの hooks を読みますが、`CLAUDE_PROJECT_DIR` を渡さないためです。hook は Claude Code 以外から起動されたと分かると何も出さずに exit 0 で終わります。
- `permissions.allow` の `Bash(looptrack issue:*)`
- **スクリプトは 1 つも置きません**。
- `.claude/skills/issue/SKILL.md`・`CLAUDE.md` の案内節・`.gitignore` の `.claude/.looptrack-freshness/`
- `--mcp` なら `.mcp.json` の `looptrack`

Codex に書くもの:

- `.codex/hooks.json` に SessionStart の `summary --agent codex` を書きます。
- 同じファイルに、PostToolUse（matcher `mcp__.*`）/ Stop / SessionEnd の `looptrack hook usage` を書きます。Codex は英数字・`_`・`|` だけの matcher を完全一致で比べます。`mcp` のような短い matcher は MCP のツール名に合いません。
- **コマンドには `LOOPTRACK_API_URL=… LOOPTRACK_PROJECT=…` を前置**します。パスは `git rev-parse --show-toplevel` から引きます（Codex の文書の推奨）。
- `AGENTS.md` の案内節も書きます。**操作は MCP のツールが主**で、CLI は MCP に無い操作だけに使います。
- スクリプトは Claude Code と同じ場所です。
- プロジェクトの `.codex/config.toml` には `[shell_environment_policy]` の `set`（`LOOPTRACK_API_URL`・`LOOPTRACK_PROJECT`。AI が打つ CLI 用）を書きます。無ければ末尾に足し、既にあれば書き換えずに案内します。サンドボックスのネットワークの許可は書きません。
- `~/.codex/config.toml` の `[mcp_servers.looptrack]` は**案内文だけ**出します。利用者の全体設定は書き換えません。
- 鮮度ガードは配線しません。Stop でのブロックの扱いを確かめていないためです。

GitHub Copilot に書くもの:

- `.github/hooks/looptrack.json` に SessionStart の `summary --agent copilot --hook-json` を書きます。このファイルは VS Code のエージェントモードと Copilot CLI の両方が読みます。
- 形は `{"version": 1, "hooks": {<イベント>: [{type, command, timeoutSec, matcher?}]}}` です。イベント名は PascalCase で書きます。CLI を VS Code 互換の入力（snake_case）にそろえます。
- VS Code は標準出力を JSON として読みます。そのため `hookSpecificOutput.additionalContext` に包みます。
- コマンドに `LOOPTRACK_API_URL=… LOOPTRACK_PROJECT=…` を前置します。パスは `git rev-parse --show-toplevel` から引きます。
- `AGENTS.md` の案内節も書きます（**操作は MCP のツールが主**）。Codex と一緒なら Codex の案内に「GitHub Copilot から使うとき」を足した 1 節にします。
- トークン計測は `looptrack hook usage --agent copilot --event <イベント>` を PostToolUse・Stop・SessionEnd に matcher なしで配線します。送るのは OpenTelemetry のファイル出力を有効にした利用者だけです。
- 鮮度ガードは配線しません。
- `--mcp` なら VS Code の `.vscode/mcp.json` の `servers.looptrack` と Copilot CLI の `.github/mcp.json` の `mcpServers.looptrack` を書きます。
  - `tools: ["*"]` は必須です。
  - CLI は 1.0.22 から `.vscode/mcp.json` を読みません。
  - `.mcp.json` は Claude Code と共用なので使いません。
  - `~/.copilot/mcp-config.json` は案内だけです。
- hook の 1 件は `command` と同じ `bash`（CLI）を持ちます。Windows 用の `powershell`（CLI・PowerShell 7）/ `windows`（VS Code・5.1）も持ちます。`powershell` / `windows` には `command` と同じ `looptrack hook …` を置きます。環境変数の前置は PowerShell の形です（Windows での実物は未確認です）。
- CLAUDE.md の管理節は Copilot も読みます。そこで冒頭に「Claude Code 向け。Copilot・Codex は AGENTS.md」と書きます。
- `.claude/settings.json` に hooks があれば「Copilot はそれも読む」と知らせます（kit/README.ja.md「Copilot での対応」）。

| 性質 | 内容 |
| -- | -- |
| 書くだけ | 承認は利用者が行います。Claude Code なら再起動とフックの確認です。Codex ならターミナルの `codex` の `/hooks` で信頼します（デスクトップ版にも効きます）。完全な無人化はしません |
| 冪等・マージ | 既存の設定は消しません。同じ hook がどこかのコマンドに既にあれば足しません。matcher が同じグループがあればそこへ足します。意味が変わらなければファイルを書き直しません（書式の差で差分を出しません）。再実行すると「変更はありません」と出ます |
| 案内節 | `<!-- looptrack:begin … -->` 〜 `<!-- looptrack:end -->` で囲み、再実行では節の中だけを差し替えます。手で入れた「## 課題管理（イシュー管理サーバ）」節があれば触らずに知らせます。skill は init が作ったもの（印つき）だけを上書きします |
| 衝突 | 既存の `LOOPTRACK_PROJECT` / `LOOPTRACK_API_URL` と違う値は `--force` が無ければ拒否します。壊れた JSON は直すよう案内して止まります。**Looptrack のリポジトリ自身には導入しません**（拒否します） |
| 控え | 変更するファイルとリンクは `.claude/.looptrack-init-backup/<時刻>/` に控えます（中に `.gitignore`）。`--dry-run` は差分だけを表示して何も書きません |
| kit の置き方 | `link`（手元に Looptrack のリポジトリがあるとき）は相対 symlink を張ります。`copy` は実体を置きます。`server`（手元に無いとき）は `GET /api/v1/dist` の一覧（SHA-256）と本体を取ります。**ハッシュが一致しなければ何も書かずに止まります**。取得元は `--dist <URL>`（setup ツールの期限つき URL。トークン不要）があればそこです。無ければ `/api/v1/dist`（トークンが要ります）です。copy / server は `.claude/.looptrack-kit.json` に取得元とハッシュを控えます（更新の判定用。§8） |
| 配布 | `kit/embed.go` が kit（`kit/core/…`・`kit/loop/…`）をバイナリに埋め込みます。`GET /api/v1/dist` は `{files: [{name, sha256, size}]}` を返します。`GET /api/v1/dist/{name}` は本体（`X-Looptrack-SHA256`）を返します。認証は他の API と同じでトークンが要ります。トークンの無い端末向けには、setup ツールが期限つきの取得 URL（`/looptrack/setup/<券>/`）を返します（§6） |

#### MCP

接続時の instructions で次を指示します（最後の 1 行の全文は §9-3-5）。

- 「最初に setup を 1 回呼ぶ」
- 「次に guide を 1 回呼ぶ」
- 「ループは next → 作業 → add_comment → 検証 → set_status Done → next（prompt `loop`）」
- 「In Review（人の判断待ち）や未応答のフィードバックがあれば次の next の前に利用者に示し、返答を先頭『判断:』『差し戻し:』のコメントに残して動かす（prompt `review`）。聞いた反応は先頭『フィードバック:』でコメントに残す」

検証コマンドのツールは 2 つあります。`verify_issue` は一覧と直近の記録を返すだけで、実行はしません（§9-3-2）。`report_verify` は AI が手元で実行した結果を記録し、「MCP の自己申告」の印が付きます（§9-3-2）。setup・導入済み通知・prompts は §6 で説明します。

### 5-3. 一覧の表の列の幅

`list` / `ready` / `summary` の表では、ID・BLOCKED・ASSIGNEE の幅を**その一覧の中身に合わせて広げます**。既定の幅（ID 9・BLOCKED 8・ASSIGNEE 12）より狭くはしません。
固定幅だと BLOCKED に ID が 2 つ入るだけで幅を超えます。するとその行だけ ASSIGNEE と TITLE が右にずれます。
BLOCKED は 26 文字（ID 3 つ弱）を上限とし、超える値は 25 文字 +「…」に切ります（全部は `show` で見られます）。
中身が既定の幅に収まる一覧の文字列は変わりません（ゴールデンテストの比較が保てます）。CLI と MCP の `rowsText` は同じ規則を使います。

### 5-4. デスクトップ版

ローカルで 1 人で使う人が、ターミナルを開かずに使い始められる形です。`looptrack` の desktop ビルド（`-tags desktop`・§5-1 の Q1）1 つに次を入れます。
ローカルモードのサーバ（§3-3）・ブラウザでの初回設定・トレイ / メニューバー・CLI です。コードは次の 3 か所にあります。

- `internal/client/desktop`: 起動・二重起動の防止・自動起動・CLI の置き場
- `internal/client/desktop/tray`: トレイ（desktop ビルドだけ）
- `internal/localserve`: 同じプロセスでローカルモードのサーバを動かします（`looptrack serve` と共用）

配布物の組み立ては `deploy/release/desktop.sh` です（RELEASE.md「デスクトップ版」）。

#### 起動

| 項目 | 決定 |
| -- | -- |
| 入口 | desktop ビルドでは**引数なしの起動**（.app・AppImage・.exe のダブルクリック。古い macOS の `-psn_…` も）が `looptrack desktop` になります。headless ビルドの引数なしは従来どおり使い方を出します。`looptrack desktop` は headless でも動きます（トレイなし） |
| 起動の手順 | データの置き場を作る → ロックを取る → 127.0.0.1 で待ち受け → `localserve.Start` → `desktop.json` に pid・URL・版を書く → 既定のブラウザで `http://127.0.0.1:<port>/looptrack/` を開く → トレイ、の順です。`localserve.Start` は鍵のファイル `<db>.secret-key`・`store.Open`・migrate・`server.New`（`LocalMode`・`CookieSecure=false`）を行います。管理者が 0 人なら画面は初回設定になります（同じ gate がそのまま効きます） |
| ポート | **固定**です（既定 18090。looptrack serve の既定 8090 と重ねません）。前回のポートを `desktop.json` に残して次も使います。AI の MCP の接続設定に URL が入るので、起動ごとに変えないためです。使えなければ OS に選ばせてそれを残します（ログに「接続設定をコピーし直す」と出します）。`LOOPTRACK_DESKTOP_PORT`（0 は OS 任せ）で指定もできます。そのときは使えなければエラーにします |
| オプション | `--background`（ブラウザを開きません。ログイン時の自動起動が使います）・`--no-tray`・`--status`（起動中なら URL を出して 0、無ければ 1）・`--quit`。`--quit` は起動中のものを止めます。トレイが出ない環境やインストーラの逃げ道です。unix では SIGTERM を送ります。Windows ではトレイの窓（`SystrayClass`）に `WM_CLOSE` を送り、止まらなければ TerminateProcess で止めます（SQLite は WAL なので壊れません） |
| インストーラ用 | `--enable-autostart`・`--install-cli`・`--unregister` の 3 つです。前の 2 つはインストール時の選択肢で、トレイのメニューと同じ処理を 1 回行って終わります。`--unregister` はアンインストールの後始末です。このアプリが登録した Run の値と、このアプリが置いた CLI の写しだけを消します。**データは消しません**。3 つともサーバを上げず、ロックもデータの置き場も作りません |
| 失敗の知らせ | ダブルクリックでは端末が無いので、起動の失敗やコピーの結果は OS の小さな知らせで出します。macOS では `osascript` の display alert を使います（文は引数で渡し AppleScript に埋め込みません）。Linux では zenity → kdialog → notify-send を使います。Windows では MessageBoxW を使います。詳細はログに出ます |
| self-update | desktop ビルドは `self-update` を拒否します（`--check` は可）。配布の looptrack は headless なので、置き換えるとトレイとダブルクリックの起動が無くなるためです（Windows では GUI の exe がコンソールの exe に替わります）。デスクトップ版はアプリごと置き換えます |

#### 二重起動の防止

| 項目 | 決定 |
| -- | -- |
| 仕組み | データの置き場の `desktop.lock` に **OS のファイルロック**を起動中ずっと持ちます。unix では `flock(LOCK_EX|LOCK_NB)` を使います。Windows では `LockFileEx(EXCLUSIVE|FAIL_IMMEDIATELY)` を使います。取れなければ 2 つ目です。2 つ目は `desktop.json` の URL の `/healthz` が 200 を返すまで待ちます（最大 30 秒）。それからブラウザでその URL を開いて 0 で終わります（`--background` なら開きません） |
| ロックにした理由 | プロセスが落ちると OS がロックを外します。pid ファイルのように「古い記録が残って起動できない」ことが起きません。同時のダブルクリック（2 つがほぼ同時に起動）でも取れるのは片方だけです |
| ポートの確認と組み合わせる理由 | ロックだけでは「起動の途中（まだ待ち受けていない）」と区別できません。2 つ目は healthz が通るまで待ってから開きます |
| `--status`・`--quit` との重なり | これらも起動していないことを確かめるために一瞬ロックを取ります。そのため、ふつうの起動は取れなければ 0.1 秒おきに 2 秒まで取り直します（`desktop.json` に pid が無い間） |
| macOS | 起動中の .app をもう一度開いても macOS は 2 つ目のプロセスを立てません。Apple Event の reopen（`kAEReopenApplication`）を送るだけです。そこでトレイの起動後に reopen の処理を差し替え（`tray/reopen_darwin.m`）、画面を開きます。`open -n` や端末から打った `looptrack desktop` の 2 つ目のプロセスはロックで止まります |
| 採らなかった案 | ポートだけで判定する案（別のアプリがポートを使っていると誤ります。起動の途中も見分けられません）。pid ファイル（異常終了で残り、pid の再利用で誤ります）。名前付きミューテックスやソケットによる OS ごとの仕組み（3 OS で別の実装になります） |

#### データ・ログの置き場

| OS | データ（DB `looptrack.db`・鍵 `looptrack.db.secret-key`・`desktop.json`・`desktop.lock`） | ログ（`looptrack.log`。5MB を超えたら起動時に `.1` へ回す） |
| -- | -- | -- |
| macOS | `~/Library/Application Support/Looptrack` | `~/Library/Logs/Looptrack` |
| Windows | `%LOCALAPPDATA%\Looptrack` | `%LOCALAPPDATA%\Looptrack\logs` |
| Linux | `$XDG_DATA_HOME/looptrack`（既定 `~/.local/share/looptrack`） | `$XDG_STATE_HOME/looptrack`（既定 `~/.local/state/looptrack`） |

`LOOPTRACK_DATA_DIR` があればデータをそこに置きます。ログはその下の `logs` です（テスト・持ち運び用）。データのディレクトリは本人だけが読めます（`privfile.MkdirAll`）。
アプリ（.app・AppImage・zip の中）とデータを分けているので、アプリを置き換えてもデータは残ります（更新）。

#### トレイ / メニューバー

| 項目 | 決定 |
| -- | -- |
| 部品 | **fyne.io/systray v1.12.2** です（Apache-2.0。getlantern/systray のフォークで保守が続いています）。OS ごとの仕組みは表の下にまとめました |
| cgo | macOS だけで要ります。macOS の desktop ビルドは macOS の runner で arm64・amd64 を cgo でビルドし、`lipo` でまとめます。Linux・Windows の desktop ビルドは cgo なしのクロスビルドです（ubuntu の runner）。headless は従来どおり cgo なしです |
| クリップボード | github.com/atotto/clipboard v0.1.4（BSD-3-Clause。macOS は pbcopy / Linux は xclip / xsel / wl-copy / Windows は Win32 API） |
| メニュー | 画面を開く / 設定（ブラウザでアカウント設定の画面 `/account` を開く。`App.OpenSettings`）/ AI の接続設定をコピー ▸（`setupwiz.MCPConfigs` の各項目＝画面の `/first-run/done` と同じ文字列。X-Looptrack-Project は最初のプロジェクト）・接続設定の画面を開く / CLI を使えるようにする / ログイン時に起動する（チェック）/ 終了 |
| クリック | **どの OS でも左クリックでメニューを出します**（systray の既定の動き。`SetOnTapped` は使いません。以前は Windows だけ左クリックで画面を開き、メニューは右クリックにしていましたが、どの OS でも同じ操作にする利用者の判断で改めました） |
| 終了 | 「終了」・シグナル・サーバの異常で `App.Quit` を呼びます → トレイを閉じ、サーバを `Shutdown`（15 秒）→ `desktop.json` にはポートだけを残します |
| トレイが出ない環境 | Linux でセッションの D-Bus に接続できなければトレイを出さずに動きます（systray は接続が無いまま終了の処理で止まるためです）。GNOME は拡張（AppIndicator）が無いとアイコンが見えません。どちらも `looptrack desktop --quit` で止めます（利用者ガイドに書きます） |
| アイコン | **仮のもの**です（角の丸い藍色の四角に白い輪と矢じり）。置き場は `internal/client/desktop/icon/`（`app.png`・`app_256.png`・`app.icns`・`app.ico`・`tray.png`・`tray.ico`・`tray_template.png`）で、`go run ./internal/client/desktop/icon/gen` が作ります。差し替えるときは同じ名前・形式のファイルを置くだけです（トレイは埋め込み、配布物は desktop.sh がここから取ります） |

systray の OS ごとの仕組み:

- macOS: cgo（Cocoa）
- Windows: Win32 API（cgo なし）
- Linux: D-Bus の StatusNotifierItem（godbus/dbus v5・BSD-2-Clause。cgo なし・GTK 不要）

#### CLI を使えるようにする

| OS | 置き方 |
| -- | -- |
| macOS・Linux | `~/.local/bin/looptrack` からアプリの中の実体（`.app/Contents/MacOS/looptrack`・AppImage は `$APPIMAGE`）への**シンボリックリンク**を張ります。一時名で作って rename します（置き換えは原子的です） |
| Windows | `%LOCALAPPDATA%\Programs\looptrack\looptrack.exe` に、zip に同梱した **CLI の exe（`cli\looptrack.exe`。headless・コンソール）をコピー**します。印のファイル `.looptrack-desktop` に置いたものの SHA-256 を書きます |

- 置いた CLI は**トークンの発行もログインもせずにそのまま使えます**。このアプリのサーバはローカルモードで、CLI もそれに合わせるためです（§3-3 の「CLI」の行）。
  URL は `looptrack desktop --status` が出すもの（末尾の `/` を除く）を `LOOPTRACK_API_URL` に渡します。
- 既にある普通のファイル（setup・self-update で入れたもの）と利用者が自分で作ったリンクは触りません（「そのまま使える」と知らせます）。
- 起動のたびに、このアプリが作ったものが古ければ直します。古いと見なすのは次の 2 つです。
  - リンクの先が別の場所の .app / AppImage のとき（動かした場合や AppImage のファイル名に入る版が変わった場合）
  - Windows のコピーが同梱のものと違い、かつ置いたときの SHA-256 のままのとき（self-update で新しくしたものは戻しません）
- PATH は変えません。置き場が PATH に無ければ知らせに足し方を書きます（`looptrack doctor` の案内と同じです。kit の入口は既定の置き場も探します）。
- **Windows で symlink にしない理由**: 開発者モードか管理者権限が要るためです。
- **.cmd のシムにしない理由**: hook がシェルを通さずに起動すると動きません。Ctrl+C で「バッチ ジョブを終了しますか」も出ます。
- **デスクトップ版の exe を CLI に使わない理由**: GUI の実行ファイル（`-H windowsgui`。ダブルクリックでコンソールの窓を出さないため）は標準出力が端末に出ません。
  そのため Windows の zip にだけ CLI の exe を別に同梱します（大きさは 2 倍になります）。

#### ログイン時の自動起動（管理者権限不要）

| OS | 登録 |
| -- | -- |
| macOS | `~/Library/LaunchAgents/<BundleID>.plist` に登録します（`ProgramArguments` = アプリの中の実体 `desktop --background`・`RunAtLoad`・`KeepAlive` なし＝「終了」で終わったら再起動しない・`AssociatedBundleIdentifiers` でログイン項目の画面にアプリ名で出す）。効くのは次のログインからです（登録のときに launchctl で読み込みません） |
| Linux | `$XDG_CONFIG_HOME/autostart/looptrack.desktop`（既定 `~/.config/autostart`。Exec は Desktop Entry の引用規則で書きます。AppImage は `$APPIMAGE`） |
| Windows | `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` の値 `Looptrack` = `"<Looptrack.exe>" desktop --background` |

メニューのチェックで登録と削除をします。起動のたびに登録を確かめ、中身が今のアプリの場所と違えば書き直します（.app や AppImage を動かしても続きます）。

#### Bundle ID（仮）

`net.howashoji.looptrack` です（配布元の識別子で、macOS の署名の識別子と合わせています）。置き場は `internal/client/desktop/app.go` の `BundleID` の 1 か所だけです。
`desktop.sh` はそこから読んで Info.plist の `CFBundleIdentifier` に入れ、LaunchAgent の Label にも使います。変えると既に登録した LaunchAgent は
古い名前のまま残ります（変えるときは移し替えを足してください）。sign-macos.sh の `--id-prefix`（単体の実行ファイルの識別子）とは別物です。

#### 配布物

| OS | 物 | 中身 |
| -- | -- | -- |
| macOS | `Looptrack_<版>_macos_universal.dmg` | `Looptrack.app`（universal・`LSUIElement`＝Dock に出さない・最低 macOS 13＝Go の要件・アイコン `Looptrack.icns`・`NOTICE`・`OFL-BIZUDGothic.txt`）と `/Applications` へのリンクです。署名は .app → staple → dmg → dmg の署名・公証・staple の順です（sign-macos.sh） |
| Linux | `Looptrack_<版>_linux_<x86_64|aarch64>.AppImage` | AppDir を squashfs にして type2-runtime の後ろにつなぎます。詳細は表の下の「AppImage の中身と起動の条件」です |
| Windows | `Looptrack_<版>_windows_<amd64|arm64>_setup.exe` | **インストーラ**です（Inno Setup 7。下の「Windows のインストーラ」）。中身は同じ版の zip と同じ実行ファイルです |
| Windows | `Looptrack_<版>_windows_<amd64|arm64>.zip` | 持ち運び用です（インストーラを使わない形も残します）。`Looptrack\Looptrack.exe`（desktop・GUI・アイコンは rsrc v0.10.2 で埋め込む）・`Looptrack\cli\looptrack.exe`（headless）・`NOTICE`・`OFL-BIZUDGothic.txt` |

AppImage の中身と起動の条件:

- AppDir の中身:
  - `AppRun` → `usr/bin/looptrack`
  - `looptrack.desktop`・256px のアイコン
  - `usr/share/doc/looptrack/` の `NOTICE`・`OFL-BIZUDGothic.txt`・`AppImage-type2-runtime-LICENSE.txt`
  - `usr/share/doc/looptrack/licenses/` の部品のライセンス文の全文と `RELINKING.md`・`runtime-components.json`
- type2-runtime は版 20251108 を SHA-256 で固定しています。libfuse3 3.15.0 を静的リンクした runtime なので libfuse2 は要りません。
- appimagetool は使いません（取るものを runtime 1 つにするためです）。
- **起動には `fusermount3`（FUSE 3 の実行ファイル。Ubuntu は `fuse3` パッケージ）が PATH に要ります**。runtime 自体は静的ですが、中身を mount するときに外の `fusermount3` / `fusermount` を起動するためです。
- 無いと `No suitable fusermount binary found on the $PATH` で止まります（実測 2026-09-20）。ふつうのデスクトップ導入には入っています。起きるのは最小構成のコンテナやサーバです。
- 入れられない環境では `APPIMAGE_EXTRACT_AND_RUN=1` を付けます。または `--appimage-extract` で展開して `squashfs-root/usr/bin/looptrack` を直に動かします。どちらも実測で動きます（利用者ガイドに書きます）。

SHA256SUMS は従来どおり署名の後の最終ファイルから作ります。デスクトップ版の 7 つも入ります（release.yml の sums）。
Windows の exe とインストーラは当面署名しません（SmartScreen の「詳細情報 → 実行」を利用者ガイドに書きます）。

#### Windows のインストーラ

`deploy/release/windows/Looptrack.iss` です（**Inno Setup 7**・7.1.0 に固定）。組み立ては `desktop.sh windows-installer` で行います
（zip の中身をそのまま入れる＝持ち運び用と同じ実行ファイル）。ISCC は Windows でしか動きません。そのため release.yml の
`desktop-windows-installer` ジョブで作ります（amd64 は windows-2025・arm64 は windows-11-arm）。

| 項目 | 決定（利用者・2026-09-20） |
| -- | -- |
| 道具 | Inno Setup 7.1.0 です（GitHub Releases から SHA-256 を照合して取ります）。商用ライセンスは当面買いません（ライセンス上は必須でないため）。公開のときに再検討します |
| 権限 | `PrivilegesRequired=lowest` です（管理者不要で、利用者ごとに導入します）。HKCU の `Uninstall\<AppId>_is1` に入るので「アプリ」の一覧から消せます |
| 置き場 | `%LOCALAPPDATA%\Programs\Looptrack Desktop`。CLI の置き場 `%LOCALAPPDATA%\Programs\looptrack` と**大小の違いだけの同名にはしません**（Windows のパスは大小を区別しないためです） |
| スタートメニュー | 必ず登録します（`DisableProgramGroupPage=yes`）。デスクトップのショートカットは出しません |
| 選択肢（Tasks） | 「ログイン時に起動する」「CLI を使えるようにする」の 2 つだけです。`[Run]` が `Looptrack.exe desktop --enable-autostart` / `--install-cli` を呼びます（トレイのメニューと同じ処理） |
| 更新 | 同じ `AppId` で上書きします。`[Code]` の `PrepareToInstall` が `desktop --quit` で止めてから置き換えます（`CloseApplications=yes` の Restart Manager との二重の備えです）。データは別の場所（`%LOCALAPPDATA%\Looptrack`）にあるので残ります |
| アンインストール | **データを消す選択肢は出しません**（データは残します。消し方は利用者ガイドにあります）。`[Code]` の `CurUninstallStepChanged(usUninstall)` は `desktop --quit` の後に `desktop --unregister` を呼びます。消すのはこのアプリが登録した Run の値と、このアプリが置いた CLI の写しだけです。CLI の資格情報（`%APPDATA%\looptrack`）も消しません。このアプリが作ったものではなく、チームのサーバへのログインも入っているためです（消し方は利用者ガイドのアンインストールに書きます） |
| arch | amd64 と arm64 で別のインストーラです（`ArchitecturesAllowed` は `x64compatible` / `arm64`）。`AppId` は共通なので入れ替えられます |
| 言語 | 日本語（`compiler:Languages\Japanese.isl`）と英語 |
| 採らなかった案 | WiX v6（Open Source Maintenance Fee と per-user の置き場の不具合の前歴があります）・NSIS（全部手書きになります）・MSIX（署名が必須で、今の署名の方針と両立しません） |

`.iss` の必須の設定は `internal/client/desktop/installer_test.go` が確かめます。確かめるのは次の 6 点です。

- AppId が固定
- 置き場
- Tasks が 2 つ
- デスクトップのショートカットが無い
- データを消す指示が無い
- 呼ぶフラグが `desktop.Main` にある

導入からアンインストールまでの通しの確認は Windows の runner でしかできません。release.yml の smoke test で行います。

#### 依存のライセンス（desktop ビルドで増えるもの）

| 部品 | ライセンス | 入るビルド |
| -- | -- | -- |
| fyne.io/systray v1.12.2 | Apache-2.0 | desktop |
| github.com/godbus/dbus/v5 v5.1.0 | BSD-2-Clause | desktop（Linux） |
| github.com/atotto/clipboard v0.1.4 | BSD-3-Clause | desktop・headless（`internal/client/desktop` は両方に入ります。headless では使いません） |
| AppImage type2-runtime 20251108 | MIT | Linux の AppImage（実行ファイルの先頭。中に静的リンクされた部品は下の表） |
| github.com/akavel/rsrc v0.10.2 | MIT | ビルドの道具だけ（成果物には入りません） |

第三者のライセンス文はリポジトリのルートの `NOTICE` にまとめて全ての配布物に添えます。`looptrack licenses` でも表示します（手順は RELEASE.md「2-2. NOTICE」）。
`NOTICE` は `go run ./internal/tools/notice` が作ります。材料はリリースの全ての形でリンクされるモジュールと、Go・BIZ UDGothic の OFL・AppImage の type2-runtime です。
各節にはソースの入手先の URL（リポジトリと module proxy の zip）を書きます。MPL-2.0（`github.com/go-sql-driver/mysql`）の「実行ファイルで配るときに受け取った人へソースの入手先を知らせる」義務に応えるためです。
AppImage の先頭の type2-runtime（MIT）は Go のモジュールではありません。そこでライセンス文の写しを `deploy/release/licenses/` に置き、NOTICE と AppImage の `usr/share/doc/looptrack/` の両方に入れます。

#### AppImage の runtime に静的リンクされた部品（LGPL-2.1 への対応）

AppImage の先頭に付く type2-runtime（`20251108`・コミット `dd6cebedcbddde9c82f89b011e8e1d40b6e43868`）は静的な実行ファイルです。上流（AppImage）が
Alpine Linux 3.21 + clang（`-static -static-pie`）で作り、次の 6 つを静的リンクしています。
一覧の元データは `deploy/release/licenses/runtime-components.json` です（版・ライセンス・著作権表示・ソースの URL と SHA-256・
ライセンス文の写しのファイル名とその SHA-256）。`go run ./internal/tools/notice` と `deploy/release/desktop.sh` が同じものを読みます。

| 部品 | 版 | ライセンス | 備考 |
| -- | -- | -- | -- |
| libfuse | 3.15.0 | **LGPL-2.1-only** | 静的リンクされるのは `include/`・`lib/`・`meson.build` だけです。上流が `patches/libfuse/mount.c.diff` で改変しています（`fusermount3` の場所を `$FUSERMOUNT_PROG` から読みます）。**libfuse2 ではなく fuse3** です |
| musl libc | 1.2.5 | MIT | ビルド環境（Alpine 3.21）の apk |
| squashfuse | 0.5.2 | BSD-2-Clause | 上流が版を固定しています（SHA-256 つき） |
| zstd（libzstd） | 1.5.6 | BSD-3-Clause | BSD-3-Clause と GPL-2.0 の二択のうち BSD-3-Clause を採ります |
| zlib | 1.3.1 | Zlib | ビルド環境の apk |
| mimalloc | 2.1.7 | MIT | runtime の `Makefile` の `-lmimalloc` です。**上流の `LICENSE` の列挙には載っていない**ので、こちらで補っています |

GPL-2.0 の `fusermount3` は同梱せず、利用者の OS に入っているものを実行します（GPL の義務は生じません）。
Looptrack 自身（`usr/bin/looptrack`）はこれらの部品と一切リンクしません（MIT の Go の実行ファイルです）。

libfuse が LGPL-2.1 なので、**LGPL-2.1 §6(a) + §6(d)** で対応します。

| やること | どこに |
| -- | -- |
| LGPL-2.1 の全文・libfuse の `LICENSE`・著作権表示（§2-2 の前段） | `NOTICE`（全配布物と `looptrack licenses`）と AppImage の `usr/share/doc/looptrack/licenses/` |
| 他の 5 部品の全文（同じ機会に足した） | 同上 |
| ソース（「著作物を作り直すために必要な情報」＝§6(a)） | 各部品の tarball の URL と SHA-256 をマニフェストと NOTICE に書きます。7 本（runtime + 6 部品・約 11MB）をまとめた **1 回限りのリリース**を作り、そこを指します（§6(d)＝配布物と同じ場所から同等のアクセスで取れる形）。毎回のリリースには添付しません |
| 再リンクの手段 | `deploy/release/licenses/RELINKING.md`（英日併記）です。`--appimage-offset` で payload を取り出します。自分で直した libfuse で runtime を作り直し、`cat` でつなぎます |
| 版がずれない担保 | マニフェストの `runtime.tag` が `desktop.sh` の `APPIMAGE_RUNTIME_TAG` と違えば NOTICE の生成が失敗します。写しの SHA-256 も生成時と AppImage の組み立て時に確かめます |

採らなかった案と、将来の選択肢:

| 案 | 判断 |
| -- | -- |
| (b) libfuse を動的リンクした runtime にする（§2-2(b)） | **不可**です。Ubuntu 24.04 などで libfuse3 が無いと起動しなくなります（AppImage が静的 runtime にした理由そのものです） |
| (c) AppImage を配るのをやめる | 採りません。「3 つの OS でダブルクリックで始められる」を満たせなくなります |
| libfuse を使わない runtime（uruntime など）に差し替える | 採りません。候補が埋め込む squashfuse が libfuse3 を静的リンクしている可能性が高いからです。義務が移るだけになりかねません |
| **(d1) runtime を自前でビルドする** | **将来の選択肢**です（今回はやりません）。上流の `scripts/docker/build-with-docker.sh` で作れば部品の版が実測でなく記録で確定します。対応ソースも自分のビルドと 1 対 1 になります。ただ取るもの（Alpine のイメージ・各ソース）が増えます。再現性のある二進を CI で作る手間も要ります |

## 6. MCP（接続設定だけで導入する・setup・導入済み通知・prompts）

リモートの MCP サーバは利用者の端末にファイルを書けません。そこで接続時の instructions で AI に setup ツールを呼ばせます。
setup が返す 1 コマンド（`looptrack` の取得 → SHA-256 の確認 → `looptrack issue init`）は、**利用者の承認を得て AI が実行します**。
フックの承認とトークンの登録は利用者が行います。フックの承認は Claude Code なら再起動と確認で、Codex ならターミナルの `codex` の `/hooks` です（AI-GUIDE §7-2）。完全な無人化はしません。

### 流れ

```
MCP 接続（OAuth・X-Looptrack-Project）── instructions: 最初に setup
  └ setup ツール → clientInfo から AI の種類を判定 → 手順（取得・init・login・承認）と配布物の SHA-256・期限つき取得 URL
      └ [AI] curl <券 URL>/looptrack_<OS>_<CPU> → SHA-256 を確認 → ~/.local/bin/looptrack（Windows は %LOCALAPPDATA%\Programs\looptrack）に置く
             → looptrack issue init --agent <種類> --source server --dist <券 URL>
      └ [AI] looptrack issue login --browser → [利用者] ブラウザでログインと承認（未ログイン時。§3-2）
      └ [利用者] 再起動とフックの承認
          └ SessionStart のフック: looptrack hook summary --agent <種類> → POST /projects/{slug}/install（実行ファイルの版）
              └ 以後、MCP のツール結果の【導入が未完了】が消える。実行ファイルが古くなると【配布スクリプトの更新】が付く
```

### 接続してきた AI の判定（clientInfo）

| 項目 | 決定 |
| -- | -- |
| 問題 | `/looptrack/mcp` は stateless（要求ごとに一時セッション）です。そのため SDK は initialize の `clientInfo` を以後の要求で覚えていません |
| セッションの見分け | `X-Looptrack-Session` があればそれを使います。無ければ接続 ID から作ります。詳細は表の下の「セッションの見分け方」です |
| 記録 | 認証の後・SDK の前のミドルウェアが記録します。詳細は表の下の「接続の記録」です |
| 名乗りの無い接続 | SEP-2575 の `clientInfo` は**任意**です（SDK の `validateRequestMeta`）。そのため名前の無い接続の記録も作られます。**`client_name` が空の記録は判定に使わず、下の「引き方」の ③ User-Agent へ進みます**。`other` で固定すると Copilot が `copilot` と判定されません。計測を有効にしていない利用者の操作まで付与の対象になってしまいます（§9-5） |
| 素の HTTP で要求を組むとき | 新しいプロトコル（2026-07-28 以降）の要求には、params の `_meta` に `io.modelcontextprotocol/protocolVersion` と `io.modelcontextprotocol/clientCapabilities` が要ります（`clientInfo` は任意）。さらに**ヘッダ `Mcp-Protocol-Version` と `Mcp-Method`** も要ります（`tools/call` はこれに加えて **`Mcp-Name`**）。欠けると SDK が `-32020`（例: `missing required Mcp-Method header`）で 400 を返します。SDK のクライアントは必ず `clientInfo` を載せます。**載せない要求を作るには素の HTTP で組みます**（テストの例は `TestMCPDiscoverWithoutClientInfo`） |
| 引き方 | ① 要求の `_meta` の clientInfo（2026-07-28 以降のプロトコルは要求ごとに付きます）→ ② `Mcp-Session-Id` の記録（**同じ利用者のものだけ**。他人の ID は使えません）→ ③ User-Agent → ④ 不明（`other` として扱い、setup の `agent` 引数で上書きできます） |
| 種類 | `claude-code`・`codex`・`copilot`・`other` のどれかです。名前との対応は表の下の「種類の見分け方」です |
| 終了 | DELETE（`Mcp-Session-Id` つき）は `closed_at` を記録して 204 を返します（stateless の SDK は 405 を返すので手前で受けます）。最終利用が 30 日前の記録は housekeeping が消します。最終利用時刻の更新は 1 分に 1 回までです |

セッションの見分け方:

- `X-Looptrack-Session` があればそれを使います。
  - `X-Looptrack-Session-Kind: host` が添えてあれば REST の `actor` と同じに読み、付与の対象から外します（§9-5「器のセッション ID」）。今の接続設定は送りません。
- **無ければ接続 ID（`Mcp-Session-Id` = `mcp_connections.id`）に印 `mcp-conn:` を付けてセッション ID として使います**。
  - このときは器の印を付けません（サーバが発行した値なので）。
  - これで MCP の接続設定にヘッダを書けない AI でも、同じ利用者の別の接続を区別できます。接続を張り直すと値が変わります。
- **印を付けるのは、クライアントが名乗るセッション ID（環境変数由来）と種類が違うためです**。
  - 同じ AI が CLI と MCP を併用すると、同じセッションでも値が違います。
  - そこで**種類の違う ID どうしは「同じか違うか」を比べません**（`service.ComparableSessions`）。比べると自分の着手を別のセッションの扱いにしてしまいます。
- **比べられないときも黙って自分のものとして扱わず、一覧と `next` に「別の経路（CLI / MCP）で着手されています。同じセッションかは判定できません」と出します**。
  - 使う名前は `cross_path_session` / `cross_path_sessions`・`server.api.issue.cross_path_note` です。
  - 判定できないことを伏せると見落としの側に倒れるからです。
- トークン情報が付いているかの判定（§9-5「その会話」）には使いません。接続 ID はスナップショットの会話と結び付かないからです。従来どおり利用者のスナップショット全体で見ます。

接続の記録:

- ミドルウェアは POST の本文から `initialize`（2026-07-28 より前のプロトコル）か **`server/discover`** を見つけます。
- `server/discover` は SEP-2575 の要求です。新しいプロトコルのクライアントは `initialize` を送りません。`clientInfo` とプロトコルの版を params の `_meta` に載せます。
- 見つけたら接続 ID（16 バイトの乱数の 16 進）を発行します。`mcp_connections` に `clientInfo`・プロトコルの版・`X-Looptrack-Project`・User-Agent を記録します。
- 接続 ID は**応答ヘッダ `Mcp-Session-Id` で渡します**。Streamable HTTP のクライアントは以後の要求にこのヘッダを付けます。SDK の stateless 処理はヘッダを無視するので動作は変わりません。
- DB に置くので、サーバを再起動しても複数台でも引けます。

種類の見分け方:

- 名前に `claude-code` / `claude code` を含めば `claude-code` です。
- `codex` を含めば `codex` です（Codex は `codex-mcp-client`）。
- 次のどれかなら `copilot` です。`copilot` を含む・`Visual Studio Code`（`Visual Studio Code - Insiders` 等）/ `Code - OSS` で**始まる**・`vscode` である。
  - Copilot CLI は `github-copilot-developer` を名乗ります。出典は [github/copilot-cli#432](https://github.com/github/copilot-cli/issues/432) の保守者のコメントで、文書にはありません。
  - VS Code は `productService.nameLong` を名乗ります。出典は [microsoft/vscode の mcpServerRequestHandler.ts](https://github.com/microsoft/vscode/blob/main/src/vs/workbench/contrib/mcp/common/mcpServerRequestHandler.ts) です。
    [mcpServer.ts](https://github.com/microsoft/vscode/blob/main/src/vs/workbench/contrib/mcp/common/mcpServer.ts) も同じです。
  - 名前の途中に含むだけ（`my Visual Studio Code tool` 等）は `other` です。
  - どちらも実物では未確認です。クラウド版の Copilot coding agent も CLI と同じ名前なので区別できません。
- それ以外は `other` です。

### setup ツール

入力は `{project?, agent?, cli?, os?, workspace?, loop?}` です。出力（テキストと構造化データ）は次の形です。

`{project, agent, workspace?, ask?, loop_answer?, client {name, version, agent, source}, install（下の導入状態）, steps [{who: ai|human, title, command?, command_windows?, text?}], files [{name, sha256, size, url}], dist_url, dist_url_expires_at, text}`

`ask` と `loop_answer` は下の「loop の問いの 2 段」で使います。答えごとに 2 通りのコマンドを返すことはしません（AI が自分で答えを決めてしまうためです）。

| 導入状態 | 手順 |
| -- | -- |
| missing（通知が無い）・stale で置き方が link 以外 | 表の下の「missing と stale（link 以外）の手順」です |
| no_hook（手動の通知だけ） | login の確認・フックの承認・確認 |
| stale で置き方が link | 手元の Looptrack のリポジトリを `git pull` します。確認は `installed` です |
| current | guide → next（prompt `loop`）から始めます |

missing と stale（link 以外）の手順:

- Claude Code / Codex:
  1. [AI] 取得 → SHA-256 確認 → 置き場に置いて `looptrack issue init --project <slug> --url <URL> --agent <種類> --source server --dist <券 URL>`。SHA-256 の確認は macOS・Linux で `shasum -a 256` / `sha256sum` を、Windows で `Get-FileHash` を使います。
     loop が未選択なら 1 回目の setup は問いだけを返します。答え（`loop`）を付けた 2 回目の ① は、取得 + init に `--loop` / `--no-loop` の一方を付けた 1 つです（下の「loop の問いの 2 段」・§8）。
  2. [利用者] トークン未登録なら `login`
  3. [利用者] 再起動・フックの承認
  4. [AI] setup を呼び直して確認
- other:
  1. 取得と `init --agent other`（CLI だけ置く）
  2. [AI] 環境変数と指示ファイル
  3. login
  4. `installed --agent other`

手元に Looptrack のリポジトリがある場合の代案（そこでビルドした `looptrack` で `looptrack issue init`）も本文に出します。

**loop の問いの 2 段**（利用者の判断 2026-09-19）: 1 つの結果に問いと答えごとの 2 通りのコマンドを並べていた時期がありました。すると AI が問いを示さずにコマンドを実行することがありました（Copilot CLI 1.0.86 の実物で 3 回中 2 回。モデルによる差があります）。問いの段階を飛ばせない形にするため、setup を 2 段にしています。Claude Code・Codex・Copilot で同じ形です。

| 呼び出し | 返すもの |
| -- | -- |
| loop の選択が要る（導入状態の `loop` が none・配布物に kit/loop がある・AI が other でない。§8）で、引数 `loop` が無い | **問いだけ**を返します。詳細は表の下の「問いだけの結果」です |
| loop の選択が要る状態で、`loop` が yes / no | 今までの手順を返します。詳細は表の下の「答えつきの結果」です |
| loop の選択が済んでいる（導入済み・辞退済み）・other・配布物に kit/loop が無い | 今までどおりの手順です。`loop` は使いません（本文に「引数 loop=… は使わない」の 1 行を出します） |
| `loop` が yes / no（y / n も受ける）以外 | エラー |

問いだけの結果:

- `ask: "loop"` です。`steps` は `[{who: human, title: "ループエンジニアリング一式（loop）を入れるかを利用者に問う（…）", text: <問いの文面>}]` の 1 つです。
- 導入のコマンド・`files`（空）・`binaries`（空）・`dist_url`（空。券を発行しません）は返しません。
- 本文の先頭は次の順です。
  1. 「AI への指示: 次の問いを利用者にそのまま示し、答え（入れる / 入れない）を得る。AI が答えを決めない。」
  2. 問いの全文
  3. 「答えを得たら、setup を loop=yes / no（と今回と同じ workspace）を付けて呼び直す（…利用者に問わずに loop を付けて呼ばない）」
- 続けて接続してきた AI と状態を置きます。missing は通知の文で、それ以外は状態の語だけです。更新の文は更新のコマンドを含むので出しません。
- 最後に「この結果には導入のコマンドが無い」を置きます。

答えつきの結果:

- loop の手順は答えに合うコマンド 1 つ（`who: ai`）です。
- missing・stale（link 以外）では、取得 + init に `--loop` / `--no-loop` の一方を付けた 1 つが最初の手順になります（承認 1 回）。
- current・no_hook・stale（link・Go 版）では、`init … --loop` / `--no-loop` の 1 つを §8 の位置に挟みます。
- `loop_answer` には使った答えが入ります。
- 本文の先頭は「AI への指示: 利用者の答え「loop を入れる」（入れない）に合わせた手順。…」です。問いの文面は繰り返しません。
- yes には hook の承認を添えます。no の手順の見出しには辞退の説明（作業ディレクトリごとに問う）を添えます。

作業ディレクトリ単位の判定とも組み合わさります。導入済みの別の作業ディレクトリしか無ければ、その作業ディレクトリは missing・loop 未選択として扱います。1 回目は問いを返し、答え（と同じ `workspace`）を付けた 2 回目で手順を返します。
AI が利用者に問わずに `loop` を付けて呼ぶことまでは防げません。そこで「利用者の答えを得てから loop を付けて呼ぶ」を次の 5 か所に書いています。setup の説明・引数 `loop` の説明・本文の先頭・接続時の instructions・prompt `setup` です。
既知の限界もあります（利用者の判断 2026-09-19 で制約として受け入れました）。
2 段にした後も Copilot CLI 1.0.86 の auto で選ばれた mai-code-1.1-flash は、答えを渡さない 8 回すべてで問いを利用者に示しませんでした。自分で `loop` を決めて呼び直しています（7 回は yes）。
答えを渡した回（gpt-5.6-luna）は、答えに合うコマンドを 1 つだけ実行しようとしました。サーバの結果の形では問いの段階を強制できません（モデル依存です）。loop が意図せず入ったときは `looptrack issue init --remove-loop` で外せます。
**古い呼び方**: `loop` も `workspace` も付けずに呼ばれたとき、問いが要る状態なら問いだけを返します。本文の先頭に呼び直し方があり、ツールの一覧（`tools/list`）の setup にも引数 `loop` が出ます。問いを示した後に呼び直せば導入できます。`commands` を読んで実行する作りの AI は、コマンドが無いので何も実行しません（誤って導入することはありません）。

**本文（text）の形と大きさ**: Copilot CLI は大きなツール結果を一時ファイルに逃がし、先頭のプレビュー（実物で 500 文字）だけを AI に渡します。MCP の instructions も既定では system prompt に入りません。そのため本文の**先頭**に見出しと AI への指示を置きます。見出しは `# イシュー管理の導入（<slug>・<AI>）: 未導入` のような状態の短い語です。問いだけの結果は、上の表のとおり指示と問いの全文を先頭 500 文字に収めます。手順の結果には「[AI] の手順は承認を得てから実行・[利用者] の手順は依頼」の 1 行を置きます（答えつきなら答えも）。
配布物の一覧は本文に出しません。出すのは取得コマンドが確かめる実行ファイルのハッシュの 1 行と、件数・一覧の URL だけです。全件の SHA-256 は構造化データの `files` / `binaries` と一覧 `/setup/<券>/` にあります。init は一覧の SHA-256 で確かめます。
大きさは AI に渡る本文で測り、テストで固定しています（`TestSetupTextSize`）。

- 未導入の手順: **8 KiB 以下**（Go 版で OS が分からず sh と PowerShell の両方を出すときは 16 KiB 以下）
- 問いだけの結果: **2 KiB 以下**
- 実測: 問いだけで約 1.7 KB（約 760 文字）・loop=yes で約 4.5〜4.9 KB・loop=no で約 3.7〜4.0 KB
- 根拠: Copilot CLI の閾値は公式文書で 20 KiB です（`COPILOT_LARGE_OUTPUT_THRESHOLD_BYTES`。「Managing context in GitHub Copilot CLI」）。ただ MCP の結果を約 10 KB で切り詰める不具合の報告（github/copilot-cli#1732）があるので、保守的に取っています。
- 構造化データ（`_meta`）は AI に渡らない前提なので測りません（変更前: 本文 10.6 KB・結果全体 37 KB → 変更後: 本文 6.6 KB）。

**作業ディレクトリの単位の判定**: 導入済み通知は利用者 × プロジェクト × AI で 1 行（最後の通知）です。そのままでは同じプロジェクトの 2 つ目の作業ディレクトリ（別のリポジトリ・clone・端末）でも「導入済み」を返していました。手順も出ませんでした（Codex の実物で発生）。
そこで setup の任意の引数 `workspace` の**最後の要素（ディレクトリ名）**を、通知の `workspace` と比べます。

- 引数は AI の作業ディレクトリの git のルートの絶対パスです。接続時の instructions・setup の説明・prompt `setup` で渡すよう案内しています。
- 通知の `workspace` は CLI が送る導入先ディレクトリの名前です（パスは送りません）。
- 比べるときは大文字小文字を区別せず、「/」「\」の区切りを受けます。引数は保存しません。

違えばその作業ディレクトリは missing として手順を返します。loop の選択もその作業ディレクトリで問い、本文に最後の通知の作業ディレクトリ・ホスト・時刻を出します。`workspace` を渡さない呼び方と、通知に `workspace` が無いとき（送らない古い CLI）は従来どおりです。
MCP のツール結果に付ける【導入が未完了】は従来の単位（利用者 × プロジェクト × AI）のままにします。要求に作業ディレクトリが無いためです。setup なら AI が引数で渡せますが、他のツールの呼び出しごとに渡させるのは重すぎます。

**Copilot 向けの手順のコマンド**: Copilot（CLI・VS Code）は `.claude/settings.json` の env を CLI に渡しません。そのため agent が copilot のときは、サーバの URL とプロジェクトの環境変数を手順のすべてのコマンドの前に置きます（login の手順の文中の `config` の確認も含みます）。置くのは `LOOPTRACK_API_URL` / `LOOPTRACK_PROJECT` です。`&&` でつないだ後ろのコマンドにも効く形にします。sh は `export A=… B=… && <コマンド>` です。PowerShell は `$env:A='…'; $env:B='…'; <コマンド>` です。Claude Code・Codex・other には付けません。
CLI（Go 版）の「サーバの URL がありません」の案内では URL を固定値（本番）で出しません。代わりに `<サーバの URL>`（MCP の接続設定の URL から末尾の `/mcp` を除いたもの）と書きます。ローカルのサーバでは固定値が誤りになるからです。

**期限つきの取得 URL（券）**: `/looptrack/setup/<券>/`（一覧。`GET /api/v1/dist` と同じ JSON）と `/looptrack/setup/<券>/<名前>`（本体・`X-Looptrack-SHA256`）の 2 つです。
券は「用途 dist・利用者 ID・期限」を `LOOPTRACK_SECRET_KEY` の AES-GCM で封じたものです（DB に置かず、改ざんできません）。**有効期間は 1 時間で、配布物の取得だけ**に使えます。
無効化された利用者の券は通しません。無効・期限切れのときは 403 で「setup ツールを呼び直す」と返します。
**理由**: MCP の接続（OAuth）はあっても CLI のトークンがまだ無い端末があります。そうした端末でも手作業なしで配布物を取れるようにするためです。`/api/v1/dist` は公開しません（従来の決定を保ちます）。券は AI の会話に載ります。ただ 1 時間で失効し、配布物の取得以外には使えません。

### 導入済み通知（`agent_installs`・マイグレーション 0007）

| 項目 | 決定 |
| -- | -- |
| 送る側 | **フック**: SessionStart の `looptrack hook summary --agent <種類>`（init が配線します）。要約の取得に続けて `POST /projects/{slug}/install` を送ります（`trigger: hook`・2 秒で打ち切り・失敗しても起動を妨げません）。古ければ要約の後に【配布スクリプトの更新】を出します。**手動**: `looptrack issue installed --agent <種類>`（`trigger: manual`・状態を表示します） |
| 送る内容 | `{agent, trigger, source（link / copy / server。symlink なら link、実体なら .looptrack-kit.json（無ければ旧 .im-dist.json）の値）, files {配布ファイル名: SHA-256}（実体＝symlink ならリンク先の内容から計算）, host（ホスト名）, workspace（導入先ディレクトリの名前。パスは送らない）, self_repo（looptrack 自身のリポジトリからの通知のときだけ true）, core {bundle_sha256}, loop {installed, declined, bundle_sha256, version}}`。`core` / `loop` は `.claude/.looptrack-kit.json` の控えから作ります（§8。無ければ送りません） |
| 保存 | 利用者 × プロジェクト × AI の種類で 1 行です（最後の通知で上書きします）。`hook_at` はフックからの通知でだけ進み、手動の通知では消えません。`bundle_sha256` は名前順の「名前 ハッシュ」行の SHA-256 です |
| 判定 | missing → **no_hook** → **stale** → current の順に見ます。missing は行が無い状態です。no_hook は claude-code / codex で `hook_at` が無い状態で、フックが承認されて動いた証拠がありません。stale は `scripts.Names` のどれかが現在の配布物と違うか、kit の core / loop の一式のハッシュが違う状態です（§8）。other はフックが無いので手動の通知で current になります |
| 自分自身 | `self_repo` が立っている導入では**kit（core / loop）を配布物と比べません**。looptrack 自身のリポジトリの kit は配布物より新しい元データなので、差が出るのが当たり前です。促された `issue init` の再実行はクライアントが「自身です」と拒否します。`--dir` で押し通すと古い配布物が元データを上書きしてしまいます。判定は配置（`kit/embed.go` と `cmd/looptrack`）を見るクライアントの `cli.IsSelfRepo` の 1 か所です。`init` の拒否も同じものを使います。比べなかったことは状態の文に出します（`server.mcp.setup.state.self_repo`）。実行ファイルの古さ（`self-update`）は抑えません |
| 権限 | プロジェクトの閲覧権限があれば送れます（viewer も CLI とフックを使うためです）。見えないプロジェクトは 404 です。入力の検査に外れると 400 です。検査するのは agent・trigger・source の値と、files が 1〜32 件・名前に `/` を含まない・ハッシュが 16 進 64 文字であることです |
| 読む | `GET /projects/{slug}/install` → `{project, installs: [claude-code, codex, other の状態], latest_bundle_sha256}`（自分の分だけ） |

**ツール結果への指示**: 状態が current でなければ、MCP の受信ミドルウェアが `tools/call` の結果（エラーの結果も）に指示を足します。1 つの text content として足し、`_meta` に `looptrack/notice` を付けます。
判定に使う AI は上の clientInfo です。プロジェクトは引数・`X-Looptrack-Project`・唯一のプロジェクトの順に決め、決まらない呼び出しには付けません。setup ツール自身にも付けません。
AI を判定できないときは、その利用者・プロジェクトの導入のうち最も進んだ状態で判定します。
変更系ツールの結果に 0059 の付与の指示（`usage_notice`）も付くときは次の順です。

1. 操作の文
2. 同じ text content の中で付与の指示
3. 別の text content で導入の指示

操作ごとの指示が先で、環境の指示が最後です。
CLI の `summary` の末尾には、トークン情報の未付与→ レポートの作成依頼→ 導入状態（current 以外のとき）の順に出します。

| 状態 | 付く文（抜粋） |
| -- | -- |
| missing | 【導入が未完了】…導入済み通知が届いていません。setup ツールを呼び、返った手順を利用者の承認を得て実行してください |
| no_hook | 【導入が未完了】…フックからの通知がまだ届いていません。利用者が Claude Code を再起動し…承認（Codex は /hooks で信頼・Copilot は新しいセッション。CLI はフォルダの信頼） |
| stale | 【配布スクリプトの更新】…更新: `looptrack issue init --project <slug> --agent <種類> --source server`（link は git pull） |

### prompts

| 名前 | 内容 |
| -- | -- |
| `loop` | ループの 1 周です。手順は表の下の「prompt loop の手順」です。loop が入っている作業環境では `/iterate` の手順に切り替わります（§8） |
| `review` | 人の判断待ちと外からの反応を利用者に持ちかけます（全文は §9-3-5）。手順は表の下の「prompt review の手順」です |
| `setup` | setup ツールを呼びます（workspace つき）。結果が loop の問いだけなら利用者に示して答えを得ます。それから `loop`（yes / no）と同じ workspace を付けて呼び直します（AI が答えを決めません）。[AI] の手順は利用者の承認を得て実行し、[利用者] の手順は依頼します。再起動後に setup で「導入済み」を確かめます |

prompt loop の手順:

1. 未読なら guide を呼びます。
2. 「人の判断待ち」「外からの反応」に項目があって利用者がいれば、先に `review` の手順を行います（§9-3-5 の 0'）。
3. next を呼びます。
4. 作業します。分かった時点で add_comment し、不具合は create_issue で起票します。聞いた反応は先頭「フィードバック: 」で add_comment します。
5. 受け入れ条件を 1 つずつ検証します。
6. set_status Done にします（comment に検証結果）。利用者の判断が要るものは Done にせず In Review にし、判断してほしい点を comment に書きます。
7. 続けるなら次の next を呼びます。そうでなければ報告して止まります。

prompt review の手順:

1. project_summary を呼びます。
2. In Review を滞留の長い順に示します（何を作ったか・検証結果・判断してほしい点）。
3. 返答を先頭「判断: 」「差し戻し: 」で add_comment し、Done / Todo へ動かします（決まらなければ In Review のまま）。
4. 未応答のフィードバックの対応を決め、先頭語の無いコメントで応答します。
5. 件数を報告して止まります（この prompt の中では next を呼びません）。

引数は `project` です（省略すると `X-Looptrack-Project`）。Claude Code では `/mcp__looptrack__loop`・`/mcp__looptrack__review` のようなスラッシュコマンドとして出ます。

### 限界

- 人の承認は最低 1 回要ります（コマンドの実行許可・フックの承認）。CLI のログインでは AI が `looptrack issue login --browser` を実行します。利用者はブラウザでログインと承認をします（§3-2。トークンは会話に出ません）。ブラウザの無い環境だけは、発行したトークンを `login --url` に貼る手作業が残ります。
- 同じ利用者が複数の端末に導入した場合、状態は最後に通知した端末のものになります。setup の `workspace` も最後の通知とだけ比べます。
  そのため 2 つの作業ディレクトリで同時にセッションを開くと、導入済みの側でも「未導入」になることがあります。後から通知した他方の名前と違う場合です。
  手順の init を再実行しても「変更はありません」で終わるので、壊れはしません。名前だけを比べるので、別のホストの同名のディレクトリは区別できません。
- 手で配線した既存のプロジェクト（`--agent` の無い SessionStart）は通知を送りません。`looptrack issue init` を再実行すると `--agent` が付きます。

## 7. Web（閲覧画面）

旧ビューア（`scripts/issue_web.py`。削除済み・タグ `archive/file-mode`）のハブ・ボード・一覧・トレース・詳細を `/looptrack` 配下へ移しました。閲覧が中心です。書き込みは担当者の変更（下の「担当の変更」）と、起票・状態の変更・コメントの追記のフォーム（下の「起票・状態・コメントのフォーム」）だけです。

| 項目 | 内容 |
| -- | -- |
| 画面 | `/looptrack/`（プロジェクト選択）と `/looptrack/p/<slug>/`（ボード / 一覧 / トレース + 詳細ドロワー）です。どちらもログイン必須で、権限の無いプロジェクトは 404 です |
| 一覧に出るプロジェクト | **管理者も含め、参加している（`project_members` に行がある）プロジェクトだけ**です。役割は `project_members` の値をそのまま出します。管理者にはハブにプロジェクト管理（§3-1）への導線を出します（`data-admin`・参加 0 件のときの案内）。参加していないプロジェクトも `/looptrack/p/<slug>/` を直接開けば見られますが、**閲覧のみ**です。アーカイブ済みのプロジェクトは一覧にも出ず、直接開いても 404 です（戻すのはプロジェクト管理から。§3-1）。**管理者もプロジェクトの中では `project_members` の役割で動きます**。viewer で参加しているときや参加していないときは、担当の変更・レポート作成依頼のフォームを出しません（§3-1「一覧用と解決用」） |
| データ | 画面は骨組みだけを返します。詳細は表の下の「ボードのデータの取り方」です |
| 本文を載せない理由 | 全件の本文を載せると数百件のプロジェクトで応答が数 MB になりました（大半はクローズ済みの本文）。4 秒ごとの見直しで遅い回線が埋まりました。画面が本文を使うのは詳細と検索だけなので、どちらも必要なときだけ取ります |
| 詳細の本文 | 詳細を開いたときに既存の 1 件取得 `GET …/issues/{id}?project=<slug>` を呼び、`markdown` から frontmatter を除いて描きます（ボードの `body` と同じ範囲）。ボードの各イシューの `version`（コメントや状態の変更でも進みます）ごとに覚え、版が同じなら取り直しません |
| 本文の検索 | 検索欄に語があるときだけ `GET …/projects/<slug>/board/search?q=<語>` を呼びます（打鍵から 250ms 待ちます）。受け取るのは本文（コメントを含む）に語を含むイシューの ID の列だけです。照合はサーバで行い、大文字小文字を区別しない部分一致です（画面の照合と同じ）。題名・ID・ラベル・担当・refs・traces の照合は従来どおり画面で即座に行います。どちらかに当たれば出します。**サーバで照合する理由**: 画面で照合するには全件の本文を一度は送ることになります（数 MB）。検索のたびにその転送を待たせてしまいます。サーバなら返すのは当たった ID だけで済みます。ボードの内容が変わった周期には検索も取り直します |
| 圧縮 | API の JSON の応答は、要求が `Accept-Encoding: gzip` を持つときにサーバが gzip で返します（`internal/server/gzip.go`）。前段の nginx の設定に頼らず、ローカルモードやデスクトップ版でも効くようにするためです。対象は `Content-Type: application/json` の応答だけで、304・xlsx・配布物には触りません。`Vary: Accept-Encoding` を付け、CSP などのヘッダは変えません |
| CSP | インラインスクリプトを禁じているので、JS は `static/*.js`（ES モジュール）に置きます。データもインラインに埋め込みません |
| XSS の修正 | `esc` が `"` と `'` も落とすようにし、属性値は `attr` を通します。Markdown のリンク先は `safeURL` で `http(s)` / `mailto` / 同一ページ内 / 相対パスだけに限ります（`javascript:` `data:` `//…` は無効なリンクにします）。ID・状態・型も属性値でエスケープします |
| 詳細の「ファイル」行 | ボード JSON の項目 `imported`（真偽）が true のときだけ出します。true は旧ファイルモードから取り込んだものを表します（`issue_events` に `kind='import'` があり、`path` の元ファイルがタグ `archive/file-mode` に実在します）。サーバで起票したものは `file_name` を合成しているだけなので出しません。判定はプロジェクトごとに 1 回のクエリです（`store.ImportedIssueIDs`）。API の `file_name` と作成応答の `path` は変えません |
| 検査 | 表示の変換は `static/render.js` に分け、`node --test static/render_test.mjs` で検査します（Go のテストからも呼びます） |
| 表示名・ヘッダ | ヘッダは全画面共通です。詳細は表の下の「ヘッダの組み立て」です |
| レポート作成 | ボードのヘッダの「レポート作成」から `/looptrack/p/<slug>/report-requests` へ進みます。期間（前回以降 / 開始日〜終了日）・対象・メモを指定して、トークンレポートの作成を**依頼**します（editor 以上・CSRF・JS なしのフォーム。閲覧者は一覧だけ）。サーバはレポートを作りません（§9-5「レポートの作成依頼」） |
| 担当の変更（**閲覧のみの例外**） | 詳細ドロワーに「担当」の行と変更フォームを出します（担当にできる人の選択・未設定・自分・引き継ぎの理由）。出すのは editor 以上だけで、クローズ済みには出しません。送信は **JS を使わない通常の POST** `/looptrack/p/<slug>/issues/<ID>/assign` です（`web()` の CSRF 検査・`via=web`）。`internal/service` の `Assign` を CLI / MCP と同じように通ります。成功すると 303 でボードの同じイシュー（`#<ID>`）へ戻ります。失敗したら同じ文言を `assign.html` に出し、400 / 403 / 422 をそのまま返します（viewer が直接 POST しても 403 です）。本文の編集は画面に足しません（状態の変更・起票・コメントは下のフォームで行います） |
| 起票・状態・コメントのフォーム | ターミナルを使わない利用者の入口です。詳細は表の下の「起票・状態・コメントのフォームの動き」です |
| 担当の表示と絞り込み | ボード JSON の各イシューに `assignee`・`assignee_inactive` を持たせます。全体には `me`（自分の login）・`can_edit`・`members`（担当にできる人）を持たせます。カードと一覧には担当を出します（印付きは「権限なし」）。フィルタには「担当: すべて / 自分 / 未設定 / 各担当者」を出します。課題管理表には「担当」列を足します（印付きは `login（権限なし）`） |
| 外からの反応の印と絞り込み（§9-3-6） | ボード JSON の各イシューに `feedback_pending`（未応答のフィードバックの件数）を持たせます。判定は `store.PendingFeedbacks` の SQL をプロジェクトごとに 1 回流すだけで、画面では数え直しません。カードのタグには「反応 N」を出します。フィルタには「未応答の反応」を出します（未応答のあるものだけです。クローズ済みも含み `list --has-feedback` と同じ集合です）。4 秒ごとの見直しで `feedback_pending` の変化も描き直します（ETag の内容に含まれます）。詳細ドロワーのコメントは特別扱いしません（先頭語がそのまま見えます）。**フォームは足しません**。フィードバックの登録は CLI / MCP のコメントで editor 以上が行い、viewer は登録できません |
| 課題管理表 | ボードの「エクスポート」で、**今出ている一覧（絞り込みと並び順のまま）**を xlsx にします。画面は ID の並びと絞り込みの説明を `POST …/issues.xlsx` に送るだけです。組み立ては `internal/xlsxreport`（CLI と共通）が行います。見出しの固定・フィルタ・状態の色分けを付け、印刷は横向きで幅に合わせます |

ボードのデータの取り方:

- 中身は `/looptrack/api/v1/projects`（ハブ）と `/looptrack/api/v1/projects/<slug>/board`（全件の項目。**本文は含みません**）から取ります。
- **4 秒ごとに見直します**（現行と同じ間隔）。見直しは**前の取得が終わってから次を予約します**（`static/board_data.js` の `makePoller`）。`setInterval` は前の完了を待たないので、遅い回線では要求が積み上がるためです。
- ボードの応答は `ETag`（弱い。`generated` を除いた内容のハッシュ）を持ちます。画面は `If-None-Match` を付けて見直します。
- 変化が無ければ本文の無い **304** が返り、転送も描き直しもしません（`generated` の分単位の変化は変化に数えません）。
- 304 でも「… 時点」を進められるよう、`generated` はヘッダ `X-Looptrack-Generated` でも返します。
- 取得に失敗したら console と画面の帯（`#loadError`・文面は対訳表の `server.web.board.load_failed` など）に出します。次の周期で取り直し、成功すると帯を消します。

ヘッダの組み立て:

- 部品は `templates/layout.html` の `nav` / `appbar_start` / `appbar_end` / `usermenu` と `static/appbar.css` です。
- ブランド「イシュー管理」と利用者メニューはここだけで組み立てます。
- 画面固有の要素は `appbar_start` と `appbar_end` の間（差し込み枠 `appbar-slot`）に差し込みます。ボードのプロジェクト名・検索・タブ・レポート作成と、レポート作成・担当の変更のパンくずがこれに当たります。
- ボードの絞り込みの段は `appbar_row_end` と `appbar_close` の間（`appbar-below`）です。
- 行はブランド・差し込み枠・利用者メニューの 3 列です。**どの幅でも利用者メニューは 1 行目の右端**に残ります。
  - 幅 1000px 以下では `appbar-wide` の要素（ボードの検索とタブ）を次の行へ全幅で回します。
  - 600px 以下ではレポート作成も 2 行目へ回します。
  - パンくずと表示名は省略記号で縮みます。
- 部品のクラス（`appbar-sep` / `appbar-crumb` / `appbar-title` / `appbar-action` / `appbar-wide` / `appbar-below`）のスタイルも appbar.css だけに置きます。画面のスタイルシートが持つのはボードのヘッダを上に留める指定だけです（`TestAppBarSource` が検査します）。
- 幅ごとの崩れは `deploy/dev/appbar-check.sh` で確かめます。使い捨て DB とローカルのサーバ・Playwright で、11 画面 × 幅 360〜1280 × ライト / ダークを測ります。
- 利用者の表示名をクリックするとアカウント設定・利用者管理・ログアウトのメニューが開きます（JS は使わず `<details>`）。
- 404・OAuth のエラー・接続の許可にも利用者メニューを出します。どれもログイン後にだけ出る画面です。どのアカウントで操作しているかの確認と切り替えの入口になります。
- ログイン前の画面（ログイン・確認コード・二段階認証の設定・セットアップ未完了）はヘッダを持ちません。

起票・状態・コメントのフォームの動き:

- ボードのヘッダに「起票」ボタンを出します（`board.html`。`canWrite(role)`＝editor 以上のときだけサーバが出します）。
- 押すと詳細ドロワーに起票フォームが開きます（題名・型・優先度・本文・受け入れ条件。既定は CLI の new と同じ task / P2 / Todo）。
- 詳細ドロワーには「状態の変更」（状態 + 任意のコメント）とコメントの追記のフォームも出します（ボード JSON の `can_edit` のときだけ）。
- 送信は JS の `fetch` で**既存の REST API** を呼びます（`POST …/projects/{slug}/issues`・`…/issues/{id}/status`・`…/issues/{id}/comments`）。認証は画面のセッション（Cookie）+ `X-CSRF-Token`（`body[data-csrf]`）です。
- サーバ側に書き込みの経路は足していません。権限・ルール・`via=web` は API と同じで、viewer が直接呼んでも 403 です。
- 本文は `render.js` の `buildIssueBody` が「本文 + 受け入れ条件の節」として組みます（1 行 1 項目に `- [ ] ` を補います）。
  - 見出しと「未記入」は雛形と同じ語を作成者の言語で書きます。英語なら `## Acceptance criteria` です。
  - `board.html` が `domain.template.acceptance_heading`・`domain.template.empty` を画面の文面として渡します。
  - それをサーバの `domain.NewDocument` が CLI の new と同じ雛形（背景 / 内容 / 受け入れ条件 / コメント）に差し込みます。本文が受け入れ条件を持てば、雛形の受け入れ条件は外れます。
- 失敗（403・400・ルールの 422）したら、サーバの `error.message` をフォームの下にそのまま出します。
- 上書きできる違反（`error.overridable`。`usage` / `verify` のクローズ時の必須など）なら「上書きの理由」の欄を出し、再送できるようにします。
- 4 秒ごとの見直しでは、ドロワーのフォームに書きかけがあれば詳細を開き直しません。
- 画面の操作は人の操作です。そのため `usage.require_on_close`（AI の操作だけが対象）には掛かりません。

## 8. kit（導入セット core / loop と選択導入）

init（§5-2）と setup（§6）が各プロジェクトへ入れるものを 2 層に分けます。内訳と判定理由の元の定義は [kit/README.ja.md](../../kit/README.ja.md) にあります。

| 層 | 内容 | 入れ方 |
| -- | -- | -- |
| **core** | CLI・鮮度ガード・トークン計測の hook・SessionStart の summary・skill `/issue`・案内節 | init / setup が無条件に入れます |
| **loop** | 出力規律・作業規律・確認モード・引き継ぎ鮮度・イテレーション規律（iterate）・文脈の大きさの警告・記憶注入・背景プロセス検知 | 利用者が AI を介して要 / 不要を選びます。後から追加も除去もできます |

### 配布

- 実体は `kit/core/`・`kit/loop/` にあります（rules・skill・`manifest.json`）。hook の実体は looptrack の中にあり、kit にファイルはありません。
- `kit/embed.go`（パッケージ `kit`）が `kit/` の下を埋め込みます。`GET /api/v1/dist` と `/looptrack/setup/<券>/` の一覧には `kit/core/skills/issue/SKILL.md` のような相対名で出します（SHA-256・size つき）。
- 配るのは `core/`・`loop/` の下だけです（`README.md`・`embed.go`・「.」「_」で始まるファイルは出しません）。`kit/loop` が無い版でもビルドは通ります。
- 本体は `GET /api/v1/dist/{name...}`・`/looptrack/setup/<券>/{name...}` で取れます。`/` を含む名前はそのままのパスでも、`%2F` で 1 セグメントにしても取れます。一覧に無い名前は 404 です。
- 導入状態の比較（§6 の stale）では、スクリプトをファイルごとに比べます（`distSums`）。kit は **core と loop を別々に一式のハッシュで**比べます（`latestDist`・下の「導入済み通知の core / loop」）。
- `kit/loop/manifest.json` が配線を持ちます。形は `{version: 1, layer: "loop", entries: [{name, kind: hook|rules|skill|script|verify, event, matcher, timeout, order, runner: "bash", codex: {event, matcher, block?} | null, verify}]}` です。
  - `name` は `kit/loop/` からの相対パスで、kit/loop のファイルと 1 対 1 です。event〜order は hook だけが持ちます。
  - init はこれを読んで settings.json / hooks.json / AGENTS.md を書きます。CLI 側に配線を書き込まないので、増減は manifest だけで済みます。
  - 形式の詳細と hook の環境変数（`LOOPTRACK_LOOP_*`）は kit/README.ja.md「loop の置き方」にあります。
  - init は配列だけの形（`[…]`）も読みます。hook の `args` はコマンドの後ろに付ける引数（注入元など）です。
  - manifest の name が kit/loop に無いときや event が未知のときは、何も書かずに止まります。
- 置き方は §5-2 と同じ 3 方式です。
  - `link` は `kit/…` への相対 symlink をディレクトリ単位で張ります（`.claude/rules/looptrack-loop/` → `kit/loop/rules`・`.claude/skills/<名前>` → `kit/loop/skills/<名前>`）。
  - `copy` / `server` は同じ場所に実体を置き、`.claude/.looptrack-kit.json` にハッシュを控えます。
  - `verify/` と `manifest.json` は置きません（配布の一覧には載ります）。
  - 同じ場所に kit への symlink でないものがあれば、変えずに知らせます。
- core に hook のファイルはありません（`looptrack hook <名前>` を配線します）。skill `/issue` は `kit/core/skills/issue/SKILL.md` をそのまま書きます。CLI に本文は埋め込まず、プロジェクト固有の決まりは guide に置きます。server は `/dist` に `kit/core/…` があるときだけ置きます（詳細は kit/README.ja.md「core の置き方」）。
- loop の hook も `looptrack hook <名前>`（manifest の `runner`）で配線します。init は manifest の名前で loop の配線を見分けます（再実行で manifest にそろえ、`--remove-loop` で外します）。
- Codex では `CLAUDE_PROJECT_DIR="$(git rev-parse --show-toplevel …)"` を前に付けます。`codex.block: false` なら `LOOPTRACK_LOOP_NO_BLOCK=1` も付けます。hook 側はこれがあると Stop でブロックせず、注入だけにします。

### `.claude/.looptrack-kit.json`（選択の記録）

```json
{"version": 1, "url": "https://example.com/looptrack", "project": "<slug>", "agent": ["claude-code"], "source": "link|copy|server",
 "core": {"bundle_sha256": "…", "files": {"kit/core/skills/issue/SKILL.md": "…"}},
 "loop": {"installed": true, "installed_at": "2026-09-18T03:00:00Z", "version": "…", "bundle_sha256": "…", "files": {…}}
       | {"installed": false, "declined_at": "…", "removed_at": "…"} | {"installed": false}}
```

- `files` は kit の層の全ファイルの SHA-256 です（verify・manifest を含みます。配布の一覧と同じ名前）。
- `bundle_sha256` はサーバの `bundleSHA256` と同じ計算です。「名前 ハッシュ」の行を名前順に並べた SHA-256 です。
- `files` は copy / server で「init が置いたときの内容」の判定にも使います（違えば手で変えたものです）。link でも選択の記録のために書きます。
- `.claude/scripts/.im-dist.json`（§5-2）はこの中に統合します（1 ファイルにします。旧ファイルがあれば読み替えて消します）。`scripts` が旧 `sha256` に当たり、`source` はそのままです（`installed` の通知は `.looptrack-kit.json` → 旧ファイルの順に読みます）。
- `installed --agent <種類>` の通知（`agent_installs`）に `core` と `loop` を含めます。**ハッシュの比較は core と loop を別々に行います**（loop を入れていないプロジェクトに loop の更新指示を出さないためです）。詳細は下の「導入済み通知の core / loop」にあります。
- `looptrack issue config` は「導入セット: core <版> / loop あり <版>（2026-09-18）」または「loop なし（未選択 | 辞退 2026-09-18）」を出します。
- `declined_at` があれば setup ツールは loop を再度は勧めません（`init --loop` で入れられる旨だけを出します）。

### init の挙動

| 指定 | 動き |
| -- | -- |
| `--loop` | core + loop を入れます。取得元（手元の Looptrack のリポジトリか `/dist` の一覧）に `kit/loop/manifest.json` が無ければ何も書かずに止まります。`declined_at` があっても入ります |
| `--no-loop` | core だけを入れ、`.looptrack-kit.json` に `declined_at` を書きます。loop が入っているときは「`--remove-loop` で外す」と案内して止まります |
| 指定なし・対話（stdin が tty） | loop が未選択（`installed` も `declined_at` も無い）で、取得元に kit/loop があるときだけ問います（`--dry-run` では問いません）。n（既定）なら `declined_at` を書きます。core を入れた後に次の問いを出し、y / n を待ちます（既定 n）。**問いの文面**: 「ループエンジニアリング一式（loop）を入れますか？ 入るもの: 出力規律・作業規律・確認モード（「確認して」で編集を止める）・引き継ぎ鮮度ガード・イテレーション規律（/iterate）・文脈の大きさの警告・背景プロセス検知（hook <N> 本・rules <M> 本・skill 2 本）。効果: 起票 → 着手 → 実装 → 検証 → クローズ → 次へ を AI が自走し、途中の逸脱（言葉だけの完了・確認中の編集・引き継ぎ漏れ）を hook が差し戻す。後から `looptrack issue init --loop` / `--remove-loop` で変えられる。[y/N]」 |
| 指定なし・非対話 | core だけを入れます。`declined_at` は書きません（次回の対話で問います）。「loop は入れていません（`--loop` で入る）」と出します |
| `--remove-loop` | loop の配線（settings.json / hooks.json のエントリ・AGENTS.md の loop 節）と symlink を外します。**利用者が手で変えたファイル（ハッシュ不一致）は消さず、名前を挙げて残します**。core と手で置いた hook は触りません。`.looptrack-kit.json` の `loop` は `installed: false` にします（`removed_at`・`declined_at` つき。以後は問いません）。`--project` は要りません |
| 再実行 | 入っている loop は指定なしでも更新します。manifest とハッシュを突き合わせ、差分のあるファイルだけを書きます。**copy / server で手で変えたファイル（`files` のハッシュと違うもの）は上書きせず、名前を挙げます**。kit から無くなったファイルは、手で変えていなければ消します。変更が無ければ「変更はありません」と出します（既存と同じ） |

マージ規則は §5-2 のとおりです（同じスクリプト名のコマンドがあれば足さない・matcher が同じグループへ足す・書式だけの差で書き直さない）。loop の hook は `manifest.json` の `order` の順に**既存の hook の後ろ**へ足します。各プロジェクトが手で置いた hook の順序を変えないためです。

### 導入済み通知の core / loop（§6 への追加・マイグレーション 0009）

| 項目 | 決定 |
| -- | -- |
| 送る側 | `install_kit()` が `.claude/.looptrack-kit.json` から `core: {bundle_sha256}`・`loop: {installed, declined（installed でなく declined_at がある。--remove-loop の後も true）, bundle_sha256, version}` を作ります。ファイルはそのディレクトリ → `CLAUDE_PROJECT_DIR` の順に探します。loop が入っていなければハッシュと版は空です。`.looptrack-kit.json` が無ければ送りません |
| 保存 | `agent_installs` に `core_bundle_sha256`・`loop_state`（installed / declined / none。空は送らない古い CLI）・`loop_bundle_sha256`・`loop_version` を持ちます（0009。0008 は担当者の追加が使っています）。検査ではハッシュが 16 進 64 文字で version が 64 文字までかを見て、外れると 400 です |
| 比較 | サーバの `latestDist` が配布の一覧から `kit/core/` と `kit/loop/` の一式の `bundleSHA256` を作ります（`.looptrack-kit.json` の bundle_sha256 と同じ計算）。**core**: 通知に core があって配布物に kit/core があり、両者が違えば `stale_kit: ["core"]` です。**loop**: `loop_state` が installed で配布物に kit/loop があり、両者が違えば `stale_kit: ["loop"]` です。未選択・辞退・古い CLI・kit/loop の無い配布物では loop を比べません |
| 状態の JSON | `installStateJSON` に `loop`（installed / declined / none。通知が無いときと古い CLI は none）・`loop_version`・`loop_bundle_sha256`・`latest_loop_bundle_sha256`・`core_bundle_sha256`・`latest_core_bundle_sha256`・`stale_kit` を足します。`GET /install` には `latest_core_bundle_sha256`・`latest_loop_bundle_sha256` を足します |
| 更新の指示 | 【配布スクリプトの更新】の古いものの一覧に「kit/core 一式」「kit/loop 一式」を並べます。更新コマンドは copy / server なら従来どおり `init --source server` です。link はスクリプトだけなら `git pull` です。kit が古ければ「`git pull` の後に `init` を再実行」です（`.looptrack-kit.json` の控えと skill `/issue` は init が書くためです） |
| 導入済みの文 | current の文の末尾に loop の状態（あり（版）・なし（辞退）・未選択（setup で利用者に問う））を足します（`looptrack issue installed` の表示にも出ます） |

### setup ツール（§6 への追加）

- 2 段で問います（§6「loop の問いの 2 段」）。
  - 1 回目は `steps` に `{who: human, title: "ループエンジニアリング一式（loop）を入れるかを利用者に問う（…）", text: <上の問いの文面（本数は配布物の kit/loop から数える）>}` だけを返します（`ask: "loop"`・コマンドなし）。
  - AI は**利用者に示して答えを得てから** setup を `loop`（yes / no）付きで呼び直します。AI が勝手に決めてはいけません（MCP の instructions にも 1 行あります）。
  - 2 回目は答えに合うコマンド（`… init --loop` / `--no-loop` の一方）を 1 つだけ返します。
- 出す条件は次の 3 つがそろうときです。導入状態の `loop` が `none`（未選択・通知が無い・古い CLI）**かつ** 配布物に `kit/loop/manifest.json` がある **かつ** AI が Claude Code / Codex / Copilot。
  - `declined` では問いを出さず、「辞退済みのため勧めない（`… --loop` で入る）」の 1 行だけを出します。
  - `installed` では何も出しません。
- 位置（答えを付けた 2 回目）:
  - missing・stale（link 以外）では**最初**に置き、取得 + init の手順をこの手順に置き換えます。取得 + init に答えの `--loop` / `--no-loop` を付けた 1 つで、旗の無い取得 + init は出しません。
    AI は先に問い、答えのコマンドを 1 回実行するだけで導入を終えられます（承認 1 回）。問いが取得と init の後にあると、答えを先に得ていても core の init と `init --loop` の 2 回になっていました。
  - no_hook では login の後（承認の前）に置きます。
  - stale（link）では git pull の後に置きます。
  - current では最初に置きます。
- コマンド（取得の無い位置）は `looptrack issue init --project <slug> --agent <種類>` です。置き方が link なら何も足しません。それ以外は `--url <URL> --source server --dist '<券 URL>'` を足します（トークン未登録でも券で取れます）。
- prompt `loop`（§6）の本文は loop の有無で変えます。
  - loop が入っているときは `/iterate` の手順です（next → 実装 → ゲート（`gates.sh`）→ 問題の起票 → close → next。`loopIteratePromptText`）。
  - 入っていないときは最小ループです（next → 作業 → comment → 検証 → close → next。`loopPromptText`）。
  - 判定には prompt を求めた AI（clientInfo）の `agent_installs.loop_state` を使います。AI を判定できなければ、どれかの AI の導入に loop があれば installed とみなします。
  - プロジェクトが決まらないときや見えないときは最小ループです。0・0'（guide と人の判断待ち）は両方に同じ文を置きます。

### Codex

`kit/loop/manifest.json` の `codex` に従い、hook は `.codex/hooks.json` へ同じイベントで配線します。**Stop でブロックできるかは実物で確かめていません**。確認できるまで Stop 系（引き継ぎ鮮度・runaway）は `codex.block: false` で「注入のみ」にします（manifest で切り替えます）。
rules は `AGENTS.md` に `<!-- looptrack:loop:begin -->` 〜 `<!-- looptrack:loop:end -->` の節として追記します。skills は AGENTS.md の同じ節に短い手順として入れます（Codex に skill はありません）。
何を入れるかは manifest の rules / skill の `codex.agents_md` に従います。

- `sections`: Codex 向けの `looptrack:inject session` の節と、全文の置き場への 1 行
- `description`: skill の description の 1 行と手順のパス
- null: 入れない

状態ファイル（`task-mode.d` 等）は `.codex/` 配下に置きます。

### GitHub Copilot

`kit/loop/manifest.json` の `copilot` に従い、hook は `.github/hooks/looptrack.json` へ配線します。`LOOPTRACK_LOOP_AGENT=copilot` を前置し、`block: false` は `LOOPTRACK_LOOP_NO_BLOCK=1` にします。

- rules / skill は Codex と同じく `AGENTS.md` の loop 節に入れます。Codex と両方のときは `codex` の列で作ります。印は `looptrack:inject session codex copilot` です。
- PreToolUse の 2 本・`stop-tool-markup-guard.sh`・`stop-runaway-background-process.sh` は null です（理由は kit/README.ja.md「Copilot での対応」）。
- hook の出力は `LOOPTRACK_LOOP_AGENT=copilot` のとき CLI（トップレベル）と VS Code（`hookSpecificOutput`）の両方の形に置きます。
- ツール名は matcher でなく hook の中で見ます。VS Code は matcher を無視し、CLI の MCP は `<サーバ>-<ツール>` の形だからです。
- 状態ファイルは `<ルート>/.claude/` に置きます（Copilot の判定に使える環境変数が無いためです）。
- 実物では未確認です。

### サーバ側

- `guide` の共通規則（`internal/guide/common.md`）の冒頭に「メリットの文面」（下）を入れます。
- 見出しの直後には「次に読むもの」を**AI ごとの行の表**（AI・loop の有無・回し方）で置きます。
  - 行は、呼び出した利用者のそのプロジェクトへの導入済み通知（`agent_installs`）の AI ごとに 1 行です（`agent` の名前順）。
  - loop が installed なら Claude Code には skill `/iterate` の手順と `.claude/rules/looptrack-loop/` を案内します。
  - Codex には `AGENTS.md` の loop 節（`<!-- looptrack:loop:begin -->`）の iterate の手順を案内します。
  - それ以外（未選択・辞退・古い CLI）は最小ループ（下の「作業の進め方」）です。
  - loop の列は「あり（版）」「なし（辞退）」「なし（未選択）」のどれかです。
  - **導入の無い AI の行は出さず**、表の下に「表に無い AI は未導入（setup ツールで導入する）。導入するまでは最小ループ」の 1 段落を置きます。
  - 導入済み通知が 1 件も無ければ「次に読むもの」自体を出しません。
- guide は CLI・REST・MCP で同じ Markdown を返すので、呼んだ AI では出し分けません。**読む AI が自分の行に従います**。「どれかの AI に loop があれば loop あり」にすると、loop の無い Codex にも `/iterate` を案内してしまうためです。prompt `loop` は prompt を求めた AI の導入状態で出し分けます（上の setup ツールの節）。

### メリットの文面（README・AI-GUIDE §1・guide の冒頭に置く）

> **このシステムを導入すると、ループエンジニアリングの基盤が整います。** イシューを中心に「起票 → 着手（next）→ 作業 → 検証 → クローズ → 次へ」を AI が回します。規則（採番・append-only・クローズ済み不変・プロジェクト別ルール）はサーバが強制し、各段階のトークン消費は記録されます。
>
> - **core だけ（最小ループ）**: セッションの冒頭に 3 層の要約（いまの周・人の判断待ち・外からの反応）が注入されます。`next` で着手し、参照したイシューを更新せずに終えようとするとやり直しを求められます。消費は段階別に貯まります。
> - **loop を足すと**: 「確認して」と頼まれたときは編集が止まり、ツール呼び出しの書式ミスにはやり直しが求められます。作業が終わるたびに引き継ぎ記憶の更新が求められ、文脈の大きさと放置された背景プロセスも見張られます。別のリポジトリ・プロジェクトへの変更は利用者に確認されます。逸脱の検知は hook に任せ、人は判断に集中できます。loop を入れるかどうかは利用者が決めます（AI が勝手に入れてはいけません）。

## 9. 運用ルール

### 9-1. プロジェクト別ルール（`projects.rules`、サーバで検証）

| ルール | 対象 | 内容 |
| -- | -- | -- |
| `forbid_status` | 例（`deploy/rules/example.json`） | 指定の状態（例 `In Review`・`Backlog`）への遷移・起票を拒否 |
| `require_comment_before` | 例 | Done / In Progress へ進めるとき、既存コメント 0 件かつ同時コメント無しなら拒否 |
| `done_requires_keyword` | 例 | Done には既存または同時コメントに指定の語（例「動作確認」）を含むこと |
| `forbid_checkbox_pattern` | 例 | 受け入れ条件のチェックボックス行に反映・マージ依頼（ENV × ACTION × ASK − EXEMPT）を書かせない |
| `usage` | 任意 | `{"require_on_close": true}` で、AI からの操作で Done / Canceled（`statuses` で変更可）にするとき、その会話のトークン情報がイシューに 1 件以上あることを求める（§9-5。理由付きで上書き可）。無い・`false` なら警告だけ（既定）。`case_pattern`（正規表現）はトークンレポートの案件別の規則（§9-5「案件ラベル別」）で、判定には使わない。例は `CASE-\d+`。`send_prompts: true`で指示文の作業名を送れるようにする（§9-5「指示文」。既定 false。Web のプロジェクト管理画面でも切り替えられる） |
| `verify` | 任意（§9-3-4） | `{"require_on_close": true}` で、「## 検証コマンド」節を持つイシューを Done（`statuses` で変更可。既定は Done だけ）にするとき、**現在の本文**に対する直近の `verify` の記録が全件成功であることを求める（記録なし・本文が変わった・失敗の 3 通りの文言で `looptrack issue verify <ID>` を案内。理由付きで上書き可）。節の無いイシューには効かない。AI の操作に限らず全ての経路に効く |
| `acceptance` | 任意 | `{"require_on_close": true}` で、「## 受け入れ条件」節を持つイシューを Done（`statuses` で変更可。既定は Done だけ。Canceled は既定で対象外）にするとき、節に**中身のある行が 1 行以上**あることを求める（起票の雛形のまま・空のチェックボックス・「（未記入）」だけなら拒否。`looptrack issue edit <ID>` を案内。理由付きで上書き可）。節の無いイシューには効かない。AI の操作に限らず全ての経路に効く |
| 共通 | 全体 | 採番アトミック、列挙値、コメント append-only、クローズ済みの本文・項目は編集不可 |

**実装の取り決め**:

| 項目 | 内容 |
| -- | -- |
| 置き場所 | 判定は `internal/domain/rules.go`（DB に依存しない純粋関数）、呼び出しは `internal/service`（REST・MCP・Web 共通）。**コードにプロジェクト固有の分岐を入れない**。各プロジェクトの設定は `deploy/rules/<slug>.json`（hook の文言をそのまま移した） |
| 設定 | `looptrack project rules set <slug> <file\|->` / `show` / `clear`。保存前に検査し、**未知のキー・存在しない状態や型・不正な正規表現は拒否**（綴り間違いでルールが黙って無効になるのを防ぐ）。取り込み（import）はルールを変えない。DB 上の設定が壊れていたら変更を 500 で失敗させる（黙って素通りさせない） |
| 適用箇所 | 起票（状態・本文）、状態変更（状態・同時コメント）、コメント（本文）、全文更新（本文）。項目の部分更新は状態を変えないため対象外 |
| 判定順 | hook と同じ: `forbid_status` → `done_requires_keyword` → `require_comment_before`。最初の 1 件で拒否。その後に `acceptance` → `verify` → `usage`（いずれも `internal/service` の `checkStatus` で判定し、状態変更のときだけ。`acceptance` は本文だけを見るので DB を引く 2 つより先。§9-3-4） |
| コメントの数え方 | 件数は `### 日時` のコメントだけ（前置きは数えない。hook と同じ）。空のコメントも 1 件。キーワードは前置き・既存コメント・同時コメントから探す |
| チェックボックス | 区間はチェックボックスの出現位置から次の改行または次のチェックボックスまで（hook と同じ。`--body` が 1 行で渡る場合に対応）。**変更前の本文に既にある区間は対象外**（過去の記述を理由に無関係な編集を止めない）。hook の実装と 15 例で判定が一致することをテストで確認 |
| メッセージ | ルールごとの `message`（起票時は `create_message`）で上書き。`{id}` `{status}` `{keyword}` `{hit}`（`usage` は `{command}`、`verify` は `{command}` `{state}` `{failed}`、`acceptance` は `{command}`）を置き換える。起票時の `{id}` は「新しいイシュー」 |
| 上書き | `usage`（`usage_required_on_close`）・`verify`（`verify_required_on_close`）・`acceptance`（`acceptance_required_on_close`）が上書き可能。API は `override_reason`、CLI は `--override "理由"`（new / status / close）。通した場合は `issue_events` に `rule_override`（rule・reason・セッション）を上書きした規則ごとに残す。違反が無ければ理由は無視して記録しない |
| 応答 | 422・`code: rule_violation`・`rule`・`overridable`。CLI は `エラー: <メッセージ>`・exit 1 |

#### ゼロバグゲート（撤去済み・2026-09-20）

プロジェクト別ルール `zero_bug_gate` は、未解決の bug が残っている間 bug / test 以外のイシューを In Progress にできなくするものでした。
このルールと切り替えの経路（Web の `/looptrack/admin/projects`・REST の `/projects/{slug}/zero-bug-gate`・MCP の `set_zero_bug_gate`・
CLI の `looptrack issue zero-bug-gate`）は、**2026-09-20 に製品から撤去しました**（利用者の決定）。

**撤去の理由**: 並行作業では「見つけたその場で起票する」規律のために bug が増え続けます。
そのため**真面目に働くセッションが多いほどゲートが閉じたままになります**。実際に bug ではない作業（翻訳・文書の整備）に着手できなくなりました。
残る道は `--override` を常用するか設定を切るかの二択でした。
ゲートが守ろうとしたのは「問題を抱えたまま次へ進まない」ことです。これは**イテレーション規律（kit の loop）とレビューで守る**ほうが実態に合います。
イテレーション規律の「1 イテレーション = 1 実装イシュー」「発見 = 起票」は撤去後も残ります。サーバが強制しなくなっただけです。

**`projects.rules` に残ったキーの扱い**: `domain.ParseRules` は未知のキーを拒否します（`DisallowUnknownFields`）。
キーを解釈する側を消すだけでは、設定が残っているプロジェクトでイシューの読み書きが全経路で失敗します。そこで撤去では次の 2 つを両方行いました。

| | 内容 |
| -- | -- |
| (A) 読み飛ばし | `Rules` に `zero_bug_gate` を**解釈しない無視用のフィールド**として残す。DB にキーが残っていても読み書きは失敗せず、判定にも使われない |
| (B) 配布物からの除去 | `deploy/rules/*.json` から `zero_bug_gate` のキーを消す。`looptrack project rules set` はルール全体を置き換えるので、ファイルを直さないと無効化した DB にキーが戻る |

マイグレーションは要りません。`projects.rules` は汎用の JSON 列で、専用の列やテーブルは無いからです。
`setting_changes` に残る `zero_bug_gate:<slug>` の行は追記専用なのでそのまま残ります。読めなくなることもありません。

### 9-2. 担当者（assignee）

1 つのプロジェクトを複数の利用者やセッションが使っても、同じイシューを別々に進めないようにします。
「誰が着手中か」を `issue_events` から推し量るのはやめました。イシューに**担当者を 1 人**持たせ、サーバが規則を強制します。
REST・MCP・画面は同じ `internal/service` を通り、判定は `service.assignee.go` にあります。

#### 保存と表示

| 項目 | 決定 | 理由 |
| -- | -- | -- |
| 保存 | `issues.assignee_user_id`（NULL = 未設定・`users.id` への FK。マイグレーション **0008**）。front_keys・`issue_extra` には入れない | 利用者の改名・権限の判定を ID で引ける。**サーバだけの項目**にして、取り込み・往復一致（681 件のバイト一致）とファイルモードを変えない |
| 表示（frontmatter） | `show` / `edit` / `get_issue` / `next` の全文 / 409 の `current` / `looptrack export` では、担当があるときだけ `status` の次の行に `assignee: <login>` を差し込む。未設定なら行を出さない | 取り込んだままのイシューの出力（ゴールデンテスト）を変えない |
| 本文での編集 | `edit` → `push`（PATCH の markdown）の `assignee:` 行は保存しない。値が現在と違えば 422 `immutable_field`（「assignee は本文では変えられません。assign を使ってください」）。行を消しただけなら無視 | 担当の変更を規則（下の R3）の外で通さない。`id`・`created`・`status` と同じ扱い |
| 一覧の JSON | `list` / `ready` / `summary` / MCP の項目に `assignee`（login）と `assignee_inactive`（下の印）。**どちらも未設定なら出さない**（omitempty） | ファイルモードの `--json` と同じキーのまま（担当の無い旧データの比較テストが変わらない） |
| CLI の表 | 表に担当が 1 件でもあれば `TITLE` の前に `ASSIGNEE` 列を足す（`%-12s`）。印は `login(!)`、表の下に「(!) は…」の注記 | 担当の無い表（ファイルモード・旧データ）は今の出力のまま |
| 版 | 担当の変更も `version` を 1 進め、`updated` を更新する | 作業コピー（edit）と画面の再描画が変更に気づける |

#### 担当にできる利用者・権限を外された担当の印

| 項目 | 決定 |
| -- | -- |
| 指定の形 | `me`（操作した本人）・login・`-`（解除）。空文字・省略は「変えない」（MCP の既定値の空文字で消さないため） |
| 担当にできる人 | **そのプロジェクトで変更できる参加者**: 無効化されていない利用者で、`project_members` の行があり、その役割が editor / admin（利用者の役割が admin でも viewer の参加は除く。管理者も参加の役割で動くため）。違えば 400 `invalid_argument`「担当にできるのはプロジェクト <slug> の参加者（editor 以上）です: <login>（担当にできる人: a, b）」。viewer は作業できないので外す |
| `me` | 操作した本人は常に指定できる（変更の権限を通った時点で作業できる。変更の権限を通るのは editor 以上の参加者だけ） |
| 印（`assignee_inactive`） | 担当が今そのプロジェクトで変更できないとき true: 無効化・参加の解除・viewer への変更（利用者の役割が admin でも同じ）。一覧・ボード・課題管理表に印を出す。**自動では外さない**（要件 6）。参加の解除・viewer への変更は、管理者が変更と同時に代わりの担当者を決める（下の「参加を外すときの担当」）ので、印が付くのは主に無効化と、それより前からの担当 |
| 印の付いた担当 | 作業できない人なので**重複作業の相手にならない**: R2 の拒否・R3 の override を求めない（引き継いだ記録は `assign` の `from_inactive: true`）。ただし `next` の候補には入れない（明示の `status` / `assign` で引き継ぐ） |

#### 規則（サーバが強制・全経路共通）

「他人」は、担当として設定済み・操作した本人でない・印が付いていない（作業できる）利用者を指します。

| # | 場面 | 規則 |
| -- | -- | -- |
| R1 | In Progress にする（`status`・`next`・`new --status "In Progress"`）で担当が未設定、かつ担当の指定が無い | 操作した本人を担当にする（`assign` の `auto: true`） |
| R2 | 担当が他人のイシューを **In Progress にする**・**本文 / 項目を更新する（PATCH）** | 422 `code: assigned_to_other`・`rule: assignee`・`overridable: true`。メッセージは操作ごとに、override を付けたときに起きることを示す: In Progress にする（`status`・`next`・`new`）は「<ID> の担当は <login> です（他の利用者が担当のイシューは、In Progress にすることができません）。引き継ぐなら --override "理由"（MCP は override_reason）を付けてください（担当が自分に替わります）。依頼や確認はコメント（comment / add_comment）で伝えてください」、本文 / 項目の更新は「（…本文や項目を編集することができません）。担当者に代わって直すなら --override "理由"（MCP は override_reason）を付けてください（担当は替わりません）。…」。理由付きで通すと、**In Progress にする場合は担当が操作した本人に替わる**（引き継ぎ）。**本文 / 項目の更新は担当を替えずに通し**、`assignee_override` を残す（編集のたびに担当が移ると、本来の担当者が自分の担当を失うため。引き継ぎは In Progress の override か R3 の assign で行う） |
| R3 | 担当を明示で変える（`assign`・`new` / `status` / `close` / `next` の `--assignee`・MCP の `assignee` 引数と `assign_issue`・画面） | 担当が未設定か本人（または印付き）なら誰にでも設定・解除できる。**他人の担当を替える・外すには override**（R2 と同じ 422。文面は「（…担当を替える・外すことができません）。引き継ぐなら --override "理由"（MCP は override_reason）を付けてください。…」）。同じ値なら何もしない（イベントも残さない）。クローズ済みは 422 `closed`（`close --assignee` は閉じる前に判定するので可） |
| R4 | コメント追記・閲覧・In Progress 以外への状態変更（In Review・Done・Todo 等） | 担当に関係なく従来どおり（要件 3 の範囲。閉じる前の確認は受け入れ条件と規則で行う） |
| R5 | viewer | 担当を変えられない（403。従来の変更権限の検査） |

判定は 権限（403）→ 担当の指定の検査（400）→ クローズ済み（422）→ R2 / R3（422）→ プロジェクト別ルール（`checkStatus`）の順です。
担当の検査は行ロックの中（`mutate`）で行うので、同時に 2 人が引き継いでも片方だけが通ります。
`override_reason` はプロジェクト別ルールの上書き（§9-1）と共用します。1 つの理由で両方を通し、記録は規則ごとに残します。

#### 記録（`issue_events`。kind に CHECK は無いのでマイグレーション不要・append-only のまま）

| kind | いつ | detail |
| -- | -- | -- |
| `assign` | 担当が変わったとき（R1・R2 の In Progress での引き継ぎ・R3・起票時の指定） | `from`（login か `""`）・`to`（同）・`auto`（R1 のとき true）・`reason`（override のとき）・`from_inactive`（印付きの担当から替えたとき true） |
| `assignee_takeover` | R2 の In Progress / R3 を override で通したとき（`rule_override` と同じ扱いの追加の記録） | `from`・`to`・`reason`・`op`（`status` / `assign`） |
| `assignee_override` | R2 の本文 / 項目の更新を override で通したとき（`update` の後に残す。担当は替わらない） | `assignee`（その時の担当の login）・`reason`・`op`（`update`）。直した人はイベントの主体（`actor_user_id`・セッション） |

状態変更と同時に担当が変わるときは `status` → `assign` →（`assignee_takeover`）の順に残します。鮮度ガード（`activity`）は最後のイベントを見るので、
担当の変更でも `last_at` が進みます（要件 4）。トークン情報の突き合わせ（§9-5）が数える kind（create / update / comment / status）には入れません。
単独の `assign` にはトークン情報の付与を求めません。

#### API・CLI・MCP

| 経路 | 形 |
| -- | -- |
| REST | `POST /issues/{id}/assign` `{assignee, override_reason}` → `{issue, from, to, changed, message}`（`message` は `担当: <ID>: <旧> → <新>`、変わらなければ `担当は変わりません: <ID>（<login>）`）。`POST /projects/{slug}/issues`・`POST /issues/{id}/status`・`POST /projects/{slug}/next` に `assignee`、`PATCH` に `override_reason`。一覧の `?assignee=me\|<login>\|-` |
| CLI | `new --assignee <login\|me>`・`status` / `close` / `next` の `--assignee`・`assign <ID> <login\|me\|-> [--override]`・`list` / `ready` / `export --xlsx` の `--assignee <me\|login\|->`・`push --override` |
| MCP | `create_issue` / `set_status` / `update_issue` / `next` の `assignee`（update_issue は担当だけの変更にも使える。本文と同時なら本文の後に担当を変える）・`update_issue` の `override_reason`・単独の `assign_issue {id, assignee, override_reason}`・`list_issues` / `ready_issues` の `assignee` |
| 画面 | §7「担当の変更」 |

エラーの文言は 3 経路とも `internal/service` の同じ文字列です。一致はテストで確かめています。

#### AI への案内（guide・MCP instructions）

サーバが拒否するだけだと、AI は 422 を受けてから考えることになります。そこで規則を先に案内します。guide（`internal/guide/common.md`）では
「起票・編集」の後に「担当者（重複作業の防止）」の節を置き、次の段落を載せます。一字一句の一致はテストで確かめます。

> **担当者**: 他の利用者が担当のイシューは In Progress にしない・本文や項目を直さない（サーバが 422 `assigned_to_other` で拒否する）。依頼や確認はコメントで伝える。引き継ぐとき・担当者に代わって直すときは、利用者に確認してから `--override "理由"`（MCP は `override_reason`）を付ける。In Progress にするときの override と `assign <ID> me --override "理由"`（MCP は `assign_issue`）は担当が自分に替わる（引き継ぎ）。本文の編集（`push` / `update_issue`）の override は担当を替えずに通り、誰が理由付きで直したかが記録に残る。`next` は自分が担当か未設定のイシューだけを取る。In Progress 以外への状態変更とコメントは担当に関係なくできる。

MCP の instructions では、本文の編集の行の次に下の 1 行を置きます。これも一致をテストで確かめます。

> 担当者: 他の利用者が担当のイシューは In Progress にしない・本文を直さない（サーバが 422 で拒否する。依頼や確認は add_comment）。引き継ぐとき・担当者に代わって本文を直すときは、利用者に確認してから override_reason を付ける（set_status の In Progress と assign_issue は担当が自分に替わる。update_issue は担当を替えずに通り、理由が記録に残る）。next は自分が担当か未設定のイシューだけを取る。

#### マイグレーション 0008

列・索引（`project_id, assignee_user_id`）・FK を足し、**現在 In Progress のイシューの担当を補います**。
補う値は、最後に In Progress にした状態変更か起票のイベント（§5-2 の旧判定と同じ）の `actor_user_id` です。
補った分のイベントは残しません。マイグレーションは管理用の資格情報で動きます。
`issue_events` の append-only はアプリ用ユーザーの権限で守っているので、そこは変えません。
取り込んだだけで該当するイベントが無いものは未設定のままです。

#### 参加を外すときの担当

参加を外したり役割を viewer に下げたりすると、その人はそのプロジェクトで担当にできなくなります。
担当のまま残すと作業できない人の未クローズのイシューが残り、誰も引き継がないまま止まってしまいます。
そこで変更の時点で代わりの担当者を決めてもらいます。

| 項目 | 決定 |
| -- | -- |
| 対象の操作 | プロジェクトの参加の**解除**と、役割の **viewer への変更**（どちらも担当にできなくなる。editor ⇔ admin・新規の付与は対象外）。経路は `/looptrack/admin/projects`（`POST …/projects/<slug>/member`）・`/looptrack/admin/users/<login>`（`POST …/users/<login>/member`）・サーバ上の `looptrack member remove|set`。REST・MCP には参加を変える操作が無い（増やすときも同じ関数を通す） |
| 条件 | 対象の利用者が**担当の未クローズ（Done / Canceled 以外）のイシュー**がそのプロジェクトに 1 件以上ある。無ければ従来どおりそのまま変わる |
| 代わりの担当者（`replacement`） | そのプロジェクトで担当にできる人（§9-2「担当にできる人」。対象の本人を除く・変更前の参加で判定）か、**`-`（未設定）**。未設定も選べる: 他に editor がいないプロジェクトで参加を外せなくなるのを避けるため。未設定は明示の選択で、黙って外すのではない（選ばないと拒否）。担当にできない人・存在しない login は 400（何も変えない） |
| 指定が無いとき | 何も変えずに拒否する。画面は **409** で同じ画面を描き直し、エラー文「<login> は <slug> で未クローズのイシュー N 件（ID, …）の担当です。…代わりの担当者…を指定してください」と、**件数・ID・選択欄**（候補 + 「未設定にする」・`required`）のフォームを出す（JS なし・CSRF・`login` / `project` と `role` を hidden で引き継ぐ）。管理コマンド（`looptrack member`）は同じ文と `--reassign <login\|->` の案内で終了コード 1 |
| 付け替え | 参加の変更と**同じトランザクション**で、対象のイシューを行ロック（`FOR UPDATE`）してから担当を替える。R2 / R3 の override は求めない（管理者の操作。元の担当はこの変更で作業できなくなる人）。版（`version`）を進め `updated` を更新する（§9-2「版」） |
| 記録 | イシューごとに `assign`: `from`（対象の login）・`to`（代わりの login か `""`）・`reason`（`参加の解除` / `役割の変更（viewer）`）・`from_inactive: true`・`op: "member"`。主体は操作した管理者（`via` は画面 `web`・管理コマンドは `admin` で `actor` なし） |
| 実装 | `service.SetMembership`（`internal/service/membership.go`）。画面の `setProjectMember` と `looptrack member` が通る。代わりが要るときは `*service.ReplacementError`（件数と候補）を返す |
| 無効化 | 対象外（利用者の無効化はプロジェクトをまたぐ操作で、戻せば作業を再開できる）。従来どおり印を付けて自動では外さない |

#### 採らなかった案

| 案 | 理由 |
| -- | -- |
| frontmatter（`issue_extra`）に `assignee` を持つ | `edit` / `push` で規則を通さずに変えられる。改名・権限の判定が文字列頼みになる |
| 担当を複数人にする | 重複作業の防止という目的に対し、「誰が止める側か」が曖昧になる |
| 権限を外された担当を自動で外す | 要件 6（自動で外さない）。経緯（誰が持っていたか）が一覧から消える。参加の解除・viewer への変更は、自動ではなく管理者が代わりの担当者を選ぶ |
| 参加の解除で代わりの担当者を必須にし、未設定を選べなくする | 他に editor がいないプロジェクトでは参加を外せなくなる。未設定も明示の選択にして、記録（`assign` の `to: ""`・理由）で追えるようにした |
| 閉じる（Done 等）も担当だけに限る | 要件 3 の範囲外。レビュー後に別の人が閉じる運用を止める |
| override 付きの本文 / 項目の編集でも担当を本人に替える | 編集のたびに担当が移り、本来の担当者が自分の担当を失う（422 の文面も操作ごとに分けてある） |

### 9-3. 3 層のループ（検証コマンド・人の判断待ち・外からの反応）

Andrew Ng の 3 層の入れ子ループ（① AI の作業ループ・② 人の判断のループ・③ 外からの反応のループ）をこのシステムで回します。
① には「## 検証コマンド」節と `verify`（機械が判定する検証器）を使います。
② は AI へのプロンプト指示（prompt `review`）と `summary` の滞留が受け持ちます。
③ はコメント先頭の `フィードバック:` の規約と `summary` の「外からの反応」で扱います。
**スキーマは変えません**。`verify` は `issue_events` の新しい kind で、フィードバックはコメントの先頭語なのでマイグレーションは要りません。

#### 1. 検証コマンド節の形式と抽出

| 項目 | 決定 |
| -- | -- |
| 見出し | コードブロック外の `^##[ \t]+検証コマンド[ \t]*$`。範囲は次の `## ` 見出しまで（`## 受け入れ条件` の抽出（`AcceptanceCriteria`）と同じ規則）。節が 2 つあれば最初のものだけ |
| コマンドの行 | ① 節の中の fenced code block（```` ``` ```` / `~~~`。言語名は問わない）の各行。空行と `#` で始まる行（コメント）は捨てる ② fenced の外では、**行全体が 1 つのインラインコード**の箇条書き（`- `…`` / `* `…`` / `1. `…``）だけをコマンドとして読む。それ以外の文（説明）は読まない |
| 1 行 1 コマンド | 行継続（末尾 `\`）・ヒアドキュメントは使えない（複数行が要るものはスクリプトにしてそれを呼ぶ）。行の前後の空白は除く |
| 上限 | 20 コマンド・1 コマンド 1,000 文字。超えたら verify を 400（`verify_commands_too_many` / `too_long`）で拒否し、本文を直すよう返す |
| テンプレート | 空の節は付けない（書いたイシューだけが持つ） |
| 置き場所 | Go は `internal/domain/verify.go` の `VerifyCommands(body) []string`（`next.go` の `AcceptanceCriteria` と同じ見出し検索を共有する）。例は `internal/domain/testdata/verify_commands.json` |

例（テストの最小集合）:

| # | 本文の節 | 抽出結果 |
| -- | -- | -- |
| 1. fenced のみ | ```` ## 検証コマンド ```` / ```` ```bash ```` / `go test ./internal/domain/...` / `# 遅いものは最後` / `go test ./internal/server/...` / ```` ``` ```` | `["go test ./internal/domain/...", "go test ./internal/server/..."]` |
| 2. 箇条書き | `## 検証コマンド` / `次の 2 つを通す。` / ``- `make lint` `` / ``- `make test`（全体）`` / ``- make build`` | `["make lint"]`（2 行目は行全体がインラインコードでない、3 行目はコードでない） |
| 3. 節なし | `## 受け入れ条件` / ``- [ ] `go test ./...` が通る`` | `[]`（受け入れ条件の中のコードは読まない）→ verify は「検証コマンドがありません」・exit 2 |
| 4. コードブロック内の見出し | ```` ```md ```` / `## 検証コマンド` / `echo x` / ```` ``` ````（節の外の fenced の中） | `[]`（コードブロック内の見出しは見出しでない） |
| 5. 次の節で終わる | `## 検証コマンド` / ```` ``` ```` / `true` / ```` ``` ```` / `## 関連` / ``- `false` `` | `["true"]` |

`next` の応答では `acceptance` の隣に `verify: {commands: [...], body_sha256, last: {at, ok, passed, failed, current}|null}` を返します（節が無ければ `verify: null`）。
`text` には受け入れ条件の後に「検証コマンド（`looptrack issue verify <ID>` で実行して記録する）」として一覧を出します。`show` の本文は変わりません。

#### 2. verify の実行（CLI・手元）

形は `looptrack issue verify <ID> [--timeout 秒] [--total-timeout 秒] [--list] [--last] [--json]` です。`--total-timeout` は全体の上限を決めます。

| 項目 | 決定 | 採らなかった案と理由 |
| -- | -- | -- |
| 実行場所 | `CLAUDE_PROJECT_DIR`（プロジェクトのルート）。無ければ `git rev-parse --show-toplevel`、それも無ければカレント | 常にカレント: AI の Bash のカレントは作業中に変わり、同じイシューの結果が場所で変わる |
| シェル | `bash -c`（無ければ `sh -c`）。標準入力は `/dev/null`、新しいプロセスグループで起動し、時間切れはグループごと止める | 引数分割して直接実行: パイプ・`&&`・環境変数の前置が書けない |
| 環境変数 | 親の環境から **`IM_` で始まるものを除く**（`LOOPTRACK_API_URL` / `LOOPTRACK_PROJECT` / `LOOPTRACK_TOKEN` 等）。`LOOPTRACK_VERIFY_ID=<ID>` と **`GOFLAGS=-count=1`** を足す（利用者の `GOFLAGS` は残して後ろに足し、`-count` が既にあればそのまま。同じ名前を 2 つ渡さない） | そのまま渡す: 検証コマンドのテストが CLI を起動すると本番に書き込む（2026-09 に実際に発生）。`GOFLAGS` を足さない: `go test` の結果キャッシュが返ると、**テストが走っていないのに「検証コマンド 1/1 成功」の記録が残る**（実測: 同じパッケージを含む 2 件の verify が 105.9 秒 → 5.0 秒）。`go build` / `go vet` / `go list` / `go mod tidy` / `go run` は `GOFLAGS` の中の「そのコマンドが知らないフラグ」を無視し（go1.27.0 で実測。すべて exit 0）、go 以外のコマンド（`bash` / `node` / `shellcheck` 等）は `GOFLAGS` を読まない |
| 時間 | 1 コマンド既定 600 秒（`--timeout`）・全体 1,800 秒。全体の上限を超えたら残りは実行せず `skipped` として送る | 無制限: 待ち続けるプロセスで AI のセッションが止まる |
| 失敗時 | **止めずに全部実行**して全結果を送る | fail-fast: 後段の失敗が見えず、直して再実行する回数が増える |
| 出力 | 標準出力と標準エラーを合わせ、**末尾 4,000 バイト**（UTF-8 の文字境界で切る）を保つ。手元の表示は失敗したコマンドの末尾 20 行 | 全文を送る: DB と会話を圧迫する。先頭を送る: 失敗の理由は末尾に出る |
| 結果キャッシュの注記 | 出力に `go test` の結果キャッシュの行（`^ok\s+<パッケージ>\s+\(cached\)`）があれば、その結果に `cached: true` を付けて送る（切る前の末尾 64KiB で見る）。**失敗にはしない**（注記として記録とコメントに残す） | 失敗にする: `\| grep -v cached` で回避でき、出力に偶然その語が混ざると誤検知する。語がどこかにあるだけで印にする: 同じ理由で誤検知する（行の形で見る）。`make` / turbo など印を出さない実行系は捕まえられない（この注記は `go test` の書式だけを見る） |
| 秘密のマスク | 送る前に次を置換する（既存のマスク規則は無いので新設。Go 側も同じ規則で再度かける。例は両方のテストで共有）: `imp_[A-Za-z0-9_-]+`（本システムの PAT）→ `imp_***`・`(?i)bearer\s+\S+` → `Bearer ***`・`(?i)(password\|passwd\|secret\|token\|api[_-]?key)(\s*[=:]\s*)\S+` → `\1\2***`・`sk-[A-Za-z0-9_-]{20,}`・`AKIA[0-9A-Z]{16}`・`-----BEGIN [A-Z ]*PRIVATE KEY-----` 以降（同じブロックの END まで） | マスクしない: テストの失敗出力に接続文字列やトークンが出ることがある |
| 終了コード | 全成功 0・失敗あり 1・節なし 2（何も記録しない）・記録の送信に失敗 1（手元の結果は表示する） | |
| `--list` | 実行せずにコマンドを表示する。`--last` はサーバの直近の記録（出力の末尾つき）を表示する | |

**MCP の `verify_issue` は実行も記録もしません。** 入力は `{id}` です。返すのはコマンドの一覧・直近の記録（現在の本文に対するものか）・
「手元のシェルで `looptrack issue verify <ID>` を実行する」という指示です。MCP の本文の末尾には `report_verify` の使い方（下）を足します。
節が無いときは CLI と同じ文言（`<ID> に検証コマンドがありません（本文に「## 検証コマンド」節を書くと verify で実行できる）`）を `isError` で返します。
文言と一覧は `internal/service` の 1 つの関数（`VerifyPlan`）で組み立てます。CLI（`GET /issues/{id}/verify`）と MCP は同じものを出し、違いは MCP の末尾に `report_verify` の使い方が付くことだけです。一致はテストで確かめます。

**MCP の `report_verify`**: CLI を置けない・使えない環境のためのものです。AI が手元のシェルで実行した結果を MCP から送れます。

| 項目 | 決定 |
| -- | -- |
| 入力 | `{id, project?, body_sha256, results: [{command, status, exit_code?, duration_ms?, output_tail?}], host?, workspace?}`（`POST /issues/{id}/verify` の要求と同じ形） |
| 検査 | `POST /issues/{id}/verify` と**同じ関数**（`service.RecordVerify`）を通す: 本文の版の不一致 `body_changed`（409 相当）・コマンドの順・件数の不一致 `verify_commands_mismatch`（400 相当）・節なし `no_verify_commands`・viewer は「閲覧のみ」（403 相当）・出力のマスクと切り詰め。拒否の文言は REST と同じ（テストで一致）。拒否したときは何も記録しない |
| 自己申告の印 | 経路（`issue_events.via`・コメントの `via`）が `mcp`。detail に `self_reported: true`。コメントの見出しは `検証コマンド（MCP の自己申告）: 2/2 成功（…）`（CLI の記録は従来どおり `検証コマンド: …`・印なし）。「直近の verify」の 1 行（`next` の text・`verify_issue`・`GET …/verify` の text）に `（現在の本文に対する記録・MCP の自己申告）`、JSON の `last.self_reported`。`summary` の ②（In Review）で直近の記録が自己申告のものは行末に `  （直近の verify は MCP の自己申告）`、JSON `in_review[].verify_self_reported`（CLI の `review_layer` と MCP の本文で同じ文言。古いサーバはキーを返さないので CLI は印を出さない）。`verify --last` も印を出す |
| 規則 | `verify.require_on_close` は自己申告も数える（直近の 1 件の経路を問わない。全件成功なら Done にできる）。人はレビュー（In Review・コメント）で印を見て判断する |
| 実行 | サーバはコマンドを実行しない（`TestServerNeverExecutes` のまま）。`report_verify` は結果を受け取るだけ |
| CLI | `looptrack issue verify` は従来どおり（印なし）。新しいサブコマンド・要求のキーは足さない（未デプロイのサーバでも CLI が動く） |

| MCP の方式 | 採否 | 理由 |
| -- | -- | -- |
| **CLI の実行を促す（採用。第一の経路）** | ○ | 実行と計測を CLI（コード）が行うので、結果が AI の書き写しにならない。core は全ての AI に CLI を置く（§5-2・§8。`other` も CLI は置く） |
| **2 段（`verify_issue` で一覧 → AI が手元で実行 → `report_verify` で結果を送る）（採用。自己申告の印つき）** | ○ | CLI の無い MCP だけの環境でも検証のループを回せる。「結果が AI の自己申告に戻る」という弱点は、記録に「MCP の自己申告」の印を付けて人がレビューで見分けることで受け止める。`verify.require_on_close` は自己申告も数える（どちらを信じるかは規則で分けず、人のレビューに任せる） |
| 自己申告を `verify.require_on_close` で数えない（CLI の記録だけを通す） | × | MCP だけの環境では閉じられなくなり、上書き（`--override`）が常態になる。印で見分けられれば足りる |
| サーバで実行する | × | サーバに各プロジェクトのチェックアウトが無い。任意コマンドの実行は RCE の入口になる |

#### 3. 記録（API・イベント・コメント）

| 項目 | 決定 |
| -- | -- |
| `GET /issues/{id}/verify` | `{id, commands[], body_sha256, last: 直近の記録（results の出力つき）\|null, current: last が現在の本文に対するものか}`。節が無ければ 404 ではなく `commands: []` と `message`（CLI は exit 2）。権限は閲覧（viewer 可） |
| `POST /issues/{id}/verify` | 要求 `{body_sha256, results: [{command, status: ok\|fail\|timeout\|skipped, exit_code: int\|null, duration_ms, output_tail, cached?}], host, workspace}`（`cached` は結果キャッシュの注記。付いたときだけ送る）（host・workspace は §6 の導入済み通知と同じくホスト名とディレクトリ名。パスは送らない）。editor 以上（viewer は 403） |
| 検査 | ① `body_sha256` が現在の本文と違えば 409 `body_changed`（「本文が変わりました。verify をやり直してください」）② `results[].command` が現在の節のコマンドと**同じ順で同じ**でなければ 400 ③ `output_tail` はサーバでも 4,096 バイトに切り、同じマスクをかける ④ クローズ済みにも記録できる（コメントの追記と同じ） |
| 保存 | 1 トランザクション（`mutate`）で、コメントの追記と `issue_events` kind **`verify`**（detail: `body_sha256`・`ok`（全件 ok のとき true）・`passed`・`failed`・`results[]`・`host`・`workspace`・`cached`（results のどれかに結果キャッシュの注記があれば true。無ければ省く））。kind に CHECK 制約は無いのでマイグレーション不要。コメント・イベントとも append-only のまま |
| 自己申告 | MCP の `report_verify` からの記録は経路 `mcp`・detail に `self_reported: true`・コメントの見出しが `検証コマンド（MCP の自己申告）: …`（§9-3-2） |
| 結果キャッシュの注記 | 出力に `go test` の結果キャッシュの行があった記録は detail に `cached: true`（該当の `results[]` にも `cached: true`）。コメントの見出しは `検証コマンド（結果キャッシュあり）: …`、該当の行は `- ok \`go test ./...\`（0.0 秒・結果キャッシュあり）`。自己申告と両方なら `検証コマンド（MCP の自己申告・結果キャッシュあり）: …`。「直近の verify」の 1 行と `verify --last` にも同じ注記が出る（JSON は `last.cached`）。**成否・件数・`verify.require_on_close` の判定は変えない**（注記だけ。環境変数の `GOFLAGS=-count=1` で塞いだ後の、上書きや go 以外の実行系に対する網） |
| 本文の版 | **`body_sha256` = 本文（frontmatter とコメント節を除いた `body_main`）の SHA-256**。`issues.version` を使わない理由: コメント・状態変更でも版が進むため、verify 自身のコメントや `close --comment` で記録が無効になる。検証コマンド節だけのハッシュにしない理由: 受け入れ条件を直したら検証もやり直すべき（要件 A-1-4「現在の本文の版に対する」） |
| コメントの文面 | 成功: `検証コマンド: 3/3 成功（12.4 秒・本文 1a2b3c4d）` + コマンドごとに `- ok \`go test ./...\`（8.1 秒）`（結果キャッシュの注記があれば `（8.1 秒・結果キャッシュあり）`）。失敗: `検証コマンド: 2/3 成功・1 失敗（…）` + `- fail \`make lint\`（exit 2・3.2 秒）` / `- timeout …` / `- skipped …`。**出力はコメントに入れない**（`verify --last` と `GET …/verify` で見る） |
| 他の規則との関係 | このコメントは通常のコメントとして数える（`require_comment_before`・`done_requires_keyword` の既存コメント、鮮度ガード（`activity`）の更新、③ の「応答」）。`CheckText`（チェックボックスの禁止）も通す |
| トークン | CLI は記録の後にスナップショットを送る（§9-5 の経路 ①。op は **`verify`**。`internal/usage` の `Ops` に足す。`usage_snapshots.op` は CHECK 無しの VARCHAR(16)）。応答には他の変更と同じく `usage_notice` が載る |

#### 4. 規則 `verify.require_on_close`

`deploy/rules/<slug>.json` に `"verify": {"require_on_close": true, "statuses": ["Done"], "message": "…"}` と書きます。`statuses` の既定は Done だけで、Canceled は検証しません。
枠組みは `usage.require_on_close` と同じです。判定は `internal/service` の `checkStatus` で行い、対象は節を持つイシューだけです。
上書きもできます（`--override "理由"`。`rule_override` に `rule: verify_required_on_close` で記録）。
AI の操作に限らず全ての経路に効きます。検証は誰が閉じても要るからです。起票で Done にする場合も同じ判定で、記録が無いので拒否します（上書きは可能）。

判定の順は `forbid_status` → `done_requires_keyword` → `require_comment_before` → **`verify`** → `usage` です。DB を引く 2 つを後ろに置き、そのうち検証を先にします。

判定の SQL です。`k_issue_events_issue_at` を使って 1 行だけ引きます。

```sql
SELECT JSON_UNQUOTE(JSON_EXTRACT(detail, '$.body_sha256')) AS body_sha256,
       JSON_EXTRACT(detail, '$.ok') = TRUE                  AS ok,
       JSON_EXTRACT(detail, '$.failed')                     AS failed
  FROM issue_events
 WHERE issue_id = ? AND kind = 'verify'
 ORDER BY at DESC, id DESC
 LIMIT 1;
```

通るのは、行がある **かつ** `body_sha256` が現在の本文の SHA-256 と一致する **かつ** `ok` のときです。**見るのは直近の 1 件だけです**。
同じ本文で前に成功していても、その後に失敗していれば拒否します。今の状態を表すのは最後の実行だからです。
経路は問いません（MCP の `report_verify` の自己申告も数えます）。

| 状態 | メッセージ（`{id}` `{command}` `{state}` を置換。`message` で上書き可） |
| -- | -- |
| 記録なし | `{id} は検証コマンドを持っていますが、verify の記録がありません。次を実行してから閉じてください: {command}` |
| 本文が変わった | `{id} の本文が最後の verify の後に変わりました。次を実行してから閉じてください: {command}` |
| 失敗 | `{id} の最後の verify で {failed} 件が失敗しています。直して次を実行してから閉じてください: {command}` |

`{command}` は `looptrack issue verify {id}` です。応答は他の規則と同じく 422・`code: rule_violation`・`rule: verify_required_on_close`・`overridable: true` を返します。
版の紐づけは次のように働きます。

1. verify が成功する
2. `edit` / `push` で受け入れ条件を 1 行直す
3. `close` は「本文が変わった」で拒否される
4. もう一度 verify する
5. `close` が通る

verify 自身のコメントと `close --comment` は本文を変えないので、記録は有効なままです。
設定は `looptrack project rules set <slug> deploy/rules/<slug>.json` で行います（§9-1 の運用。未知のキーや不正な `statuses` は拒否）。

##### 節ごとのハッシュと「検証コマンドが起票時のまま」の警告

`verify.require_on_close` が見るのは「記録があること」と「全件成功であること」だけです。そのため**起票時に書いた検証コマンドが、あとで更新された受け入れ条件を実証していなくても通ります**。
実際にこんなことがありました。受け入れ条件は「別の場所へ移す」に書き替わったのに、検証コマンドは起票時の `git ls-files <移す前の場所>` のままでした。
移設の前後で出力の形が同じだったので 1/1 成功が記録され、その記録で close が通りました。
そこで本文の**節ごとのハッシュ**を記録に残し、食い違いを警告します。成否は変えません。

| 項目 | 決定 |
| -- | -- |
| 記録 | `issue_events` kind **`create` / `update`** の detail に **`sections`**（`{"acceptance": <SHA-256>, "verify": <SHA-256>}`）を足す。値は節の中身（見出しの次の行から次の `## ` 見出しの前まで。CRLF をそろえ前後の空白を落とす）の SHA-256。その節が本文に無ければキーを省く。**detail は JSON の列なので `migrations/` の追加は要らない**（`kind` の CHECK 制約も無い。§9-3-3 の `verify` と同じ） |
| 基準 | 「`sections` を持つ**最初**の `create` / `update`」= 起票時（`store.BaselineSections`。`k_issue_events_issue_at` を使う 1 行） |
| 判定 | `domain.SectionDrift(base, now)`: **受け入れ条件のハッシュが変わった かつ 検証コマンドのハッシュが起票時と同じ** のときだけ true。規則はこの 1 か所に置く（REST・MCP・CLI で同じ `service.PlanVerify` を通る） |
| 判定しない場合 | `base` が空（**この仕組みより前に起票されたイシュー**。過去の分は判定も警告もしない。これから `create` / `update` されるものから効く）・どちらかの側に検証コマンドの節が無い（関門そのものが働かない）・どちらかの側に受け入れ条件の節が無い（更新されたかを比べられない）・`sections` の JSON が読めない（**古い記録で落とさない**。判定せずに通す） |
| 表示 | `service.PlanVerify` が `SectionDrift` を立て、注記（対訳 `domain.verify.section_drift`）を `text` に足す。JSON は `section_drift`（`GET /issues/{id}/verify`・`next` の `verify`。false なら省く）。CLI は `verify` を走らせる前に同じ注記を出す |
| **止めない** | 警告だけで、close も verify も拒否しない。ハッシュが言えるのは「更新されたか」までで、節の中身が受け入れ条件を実証しているかは人と AI が読むしかない。`verify.require_on_close` の判定は変えない |

#### 5. 人の判断待ちの周（B の文面）

`internal/guide/common.md` の「作業の進め方（ループの 1 周）」の 5. の前に、次の文を足します。

> **人の判断待ち**: 自分の作業で利用者の判断が要るもの（仕様の解釈・見た目・方針の選択・受け入れ条件を満たしたが確かめてほしいもの）は、Done にせず `looptrack issue status <ID> "In Review" --comment "判断してほしい点: …"` にする。
> In Review のイシューがあれば、**次の `next` の前に**利用者に示す: 何を作ったか・検証結果（verify の記録と受け入れ条件ごとの結果）・判断してほしい点。
> 利用者の返答はコメントの**先頭に `判断:`**（了承・指示）か **`差し戻し:`**（やり直し。方針も書く）を付けて残し、`判断:` なら Done（`close --comment "判断: …"`）、`差し戻し:` なら Todo に戻す（`status <ID> Todo --comment "差し戻し: …"`）。
> 利用者がいない・答えが無いときは In Review のまま次の `next` へ進む（催促しない。`summary` の「人の判断待ち」に滞留が出る）。
>
> **外からの反応**: 利用者との会話で、参加者・テスター・利用者の反応（使ってみた感想・不具合の報告・要望）を聞いたら、該当するイシューに**先頭 `フィードバック:`** を付けてコメントする（`フィードバック: 〈誰から・いつ・どの場面で〉 〈内容〉`）。該当が無ければ起票してからコメントする。
> `summary` の「外からの反応」に未応答のものがあれば、次の `next` の前に利用者に示して対応（方針のコメント・起票・状態変更）を決め、先頭語の無いコメントで応答を残す。

MCP の instructions（`internal/server/mcp.go`）では、ループの文の後に次の 1 行を足します。

> In Review（人の判断待ち）や未応答のフィードバック（コメント先頭が「フィードバック:」）があれば、次の next の前に利用者に示し、返答を先頭「判断:」「差し戻し:」のコメントに残して Done か Todo へ動かす（prompt「review」）。利用者から聞いた参加者・テスターの反応は先頭「フィードバック:」でコメントに残す。

prompt `loop` の本文（§6。loop の有無による切り替えは §8）では、1. の前に次の手順を足します。

> 0'. project_summary の「人の判断待ち」「外からの反応」に項目があり、利用者がこの会話にいるなら、先に prompt「review」の手順を行う（いなければ飛ばす）

同じく 4. を「set_status で Done にし …。利用者の判断が要るもの（仕様の解釈・見た目・方針）は Done にせず In Review にし、comment に判断してほしい点を書く」に改めます。

prompt **`review`** の全文です（`Title: 人の判断待ちと外からの反応を利用者に持ちかける`・引数は `project`）。

```
イシュー管理（looptrack）の「人の判断待ち」と「外からの反応」を利用者に持ちかけてください%s。

1. project_summary を呼び、「人の判断待ち（In Review）」と「外からの反応（未応答のフィードバック）」の一覧を得る。どちらも無ければ「判断待ちはありません」と伝えて止まる
2. In Review を滞留の長い順に 1 件ずつ、get_issue で本文・コメントを読み、利用者に次を示す:
   - 何を作ったか（変更の要点・場所）
   - 検証結果（verify の記録・受け入れ条件ごとの結果と確かめた方法）
   - 判断してほしい点（In Review にしたときのコメント）
3. 利用者の返答を add_comment で残す。了承・指示は先頭「判断: 」、やり直しは先頭「差し戻し: 」とし、利用者の言葉と次の方針を書く
4. 「判断:」なら set_status で Done（comment に検証結果）。「差し戻し:」なら set_status で Todo（comment に方針）。利用者が決めなかったものは In Review のまま次へ
5. 未応答のフィードバック（先頭「フィードバック:」のコメント）を 1 件ずつ利用者に示し、対応を決める: 既存イシューで直す（方針を add_comment）・新しく起票する（create_issue。元のイシューに起票した ID を add_comment）・対応しない（理由を add_comment）。どれも先頭語を付けないコメントで応答を残す（これで未応答から外れる）
6. 扱った件数（Done・Todo へ戻した・残した・フィードバックへの応答）を報告して止まる。この prompt の中では next を呼ばない
```

利用者がいないとき、`loop` は In Review とフィードバックを飛ばして `next` に進みます。In Review は `next` の候補にならないからです（§5-2 規則 2 のまま）。
`review` が利用者のいないときに呼ばれたら、一覧だけ報告して止まります。状態は変えません。
CLI 側は `looptrack issue list --status "In Review"`・`summary`・`show` で足りるので、新しいサブコマンドは足しません。`skills/issue/SKILL.md`（kit/core）にも上の 2 段落を短く入れます。

#### 6. 外からの反応の先頭語の判定（C）

**スキーマ・権限・画面には手を入れません**。`comments` に列を足さず、画面にもフォームを足しません。`deploy/grants.sql` の「comments / issue_events は SELECT・INSERT のみ」もそのまま保ちます。

**viewer はフィードバックを登録できません。** フィードバックは先頭に `フィードバック:` を付けた通常のコメントなので、**書けるのは editor 以上だけです**（`POST /issues/{id}/comments`・MCP `add_comment` の既存の権限。viewer は 403 のまま）。
viewer 向けの入口（専用のコマンド・ツール・画面のフォーム・viewer に限ったコメント権限）は作りません（2026-09-18 の利用者の追加判断）。
参加者やテスターの反応は、editor 以上の人（AI を使う開発者）が聞いてイシューに残します。

| 項目 | 決定 |
| -- | -- |
| 先頭語 | コメント本文の**先頭**が `フィードバック:` または `フィードバック：`（全角コロン）なら「フィードバック」。先頭の空白・改行は許さない（`LIKE` で引けるようにする。AI には先頭に書くよう指示する）。`判断:` / `差し戻し:`（B）も同じ規則だが、いまは判定に使わない（記録の形式だけ） |
| 置き場所 | Go は `internal/domain/leadword.go` の `LeadWord(content) string`（`"feedback"` / `"decision"` / `"sendback"` / `""`）。例は `internal/domain/testdata/leadword.json` |
| 応答 | フィードバック F（`created_at` がある＝サーバで書かれたもの）より後に、**先頭語が「フィードバック」でないコメント**（seq が大きい）か **状態変更**（`issue_events` kind `status`、`at` > F の `created_at`）があれば応答済み。コメントは editor 以上しか書けない（viewer はフィードバックも応答も書けない）ので、「editor 以上のコメント」の条件は役割を引かずに満たされる |
| 未応答のイシュー | 未応答の F を 1 件以上持つイシュー。**クローズ済みも含める**（反応は Done の後に来る）。期間では切らない（7 日で消えると放置が見えなくなる。応答 1 つで消えるので溜まらない） |
| 取り込み分 | `created_at` が NULL のコメント（取り込み）は対象外（先頭語の規約より前のもの） |

判定の SQL です。プロジェクト単位で 1 回引き、summary・list・board で共用します（`store.PendingFeedback(ctx, q, projectID)`）。

```sql
SELECT c.issue_id, c.seq, c.created_at, LEFT(c.content, 200) AS excerpt
  FROM comments c
  JOIN issues i ON i.id = c.issue_id
 WHERE i.project_id = ?
   AND c.created_at IS NOT NULL
   AND (c.content LIKE 'フィードバック:%' OR c.content LIKE 'フィードバック：%')
   AND NOT EXISTS (SELECT 1 FROM comments r
                    WHERE r.issue_id = c.issue_id AND r.seq > c.seq
                      AND r.content NOT LIKE 'フィードバック:%' AND r.content NOT LIKE 'フィードバック：%')
   AND NOT EXISTS (SELECT 1 FROM issue_events e
                    WHERE e.issue_id = c.issue_id AND e.kind = 'status' AND e.at > c.created_at)
 ORDER BY c.created_at;
```

`状態変更 + 同時コメント` は、コメントの側でも応答になります。`LIKE` の照合順序は `utf8mb4_bin` です。
`REGEXP_LIKE` で先頭の空白を許す案は 8 倍遅かったので採りません（下の実測）。

判定の例（テストの最小集合）:

| # | コメント・イベントの並び | 未応答 |
| -- | -- | -- |
| 1. 未応答 | c1 `作業メモ` → c2 `フィードバック: テスター A（9/18）: 保存ボタンが見つからない` | c2（1 件） |
| 2. コメントで応答 | c1 `フィードバック: …` → c2 `EX-0090 で起票。ボタンを右上に移す` | なし |
| 3. 状態変更で応答 | c1 `フィードバック: …` → 状態変更 Done → Todo（コメントなし） | なし |
| 4. 重ねても外れない | c1 `フィードバック: A` → c2 `フィードバック：B`（全角） | c1・c2（2 件） |
| 5. 先頭でない | c1 ` フィードバック: …`（先頭が空白）・c2 `テスターのフィードバック: …` | なし（フィードバックと数えない） |
| 6. 応答の後の新しい反応 | c1 `フィードバック: A` → c2 `対応方針…` → c3 `フィードバック: B` | c3（1 件） |

**`list --has-feedback` とボードの絞り込みは残します**。先頭語で判定できるからです。

| 経路 | 形 |
| -- | -- |
| API | `GET /projects/{slug}/issues?has_feedback=1`。`has_feedback` を付けたら**既定でクローズ済みも含める**（`all` を付けたのと同じ。理由は上の「未応答のイシュー」）。項目に `feedback_pending`（未応答の件数）を付ける |
| CLI | `looptrack issue list --has-feedback`（他の絞り込みと併用可）。ファイルモードでは「API モードだけ」とエラー（ファイルモードの挙動は変えない） |
| ボード | `/projects/{slug}/board` の各項目に `feedback_pending` を足し、カードに「反応 N」の印、絞り込みに「未応答の反応」を足す（`static/render.js`。4 秒ごとの見直しで同じ SQL を 1 回引く）。詳細ドロワーのコメントは特別扱いしない（本文の先頭語がそのまま見える） |
| MCP | `list_issues` に `has_feedback` 引数 |

#### 7. summary の 3 層表示

CLI（API モード）の表示です。**ファイルモードの表示は変えません**。見出しは従来のままで、イベントもフィードバックもありません。

```
══ ① いまの周（AI の作業） ══
── 進行中（In Progress）──
…（従来の行）
── 着手可能（ready・上位 12 / 全 30 件）──
…

══ ② 人の判断待ち（In Review 6 件・48 時間超 2 件） ══
EX-0062   task  In Review  P2  3日4時間 [48h超]  共通のトークンレポート skill と…
EX-0077   task  In Review  P2  5時間             メリットの文面…

══ ③ 外からの反応（未応答のフィードバック 3 件・2 イシュー） ══
EX-0081   2026-09-18 14:02 から 2 件  テスター A（9/18）: 保存ボタンが見つからない…

未クローズ 40 件（bug 3 件）
（以下、トークン情報の未付与 → レポートの作成依頼 → 導入状態。§6 の順のまま）
```

| 項目 | 決定 |
| -- | -- |
| 見出し | `① いまの周（AI の作業）`・`② 人の判断待ち（In Review N 件・48 時間超 M 件）`・`③ 外からの反応（未応答のフィードバック N 件・K イシュー）`。該当が無い層も見出しと「該当なし」を出す（層があることを毎回見せる） |
| 既存項目の割り当て | 進行中・着手可能 → ①。レビュー待ち → ②。未クローズ・トークン関連・作成依頼・導入状態 → 層の外（末尾。現状の順） |
| 滞留 | In Review のイシューごとに、**最後に In Review になった時刻**: `issue_events` の `kind = 'status' AND detail.to = 'In Review'` または `kind = 'create' AND detail.status = 'In Review'` の最大の `at`（1 クエリ・GROUP BY）。イベントが無い（取り込み分）ときは frontmatter の `updated`。表示は `N日M時間` / `M時間` / `M分`。**48 時間超**に `[48h超]` の印（絵文字は使わない）。並びは滞留の長い順 |
| `--limit` | 従来どおり着手可能にだけ効く。② は全件（In Review は多くない）。③ は上位 `--limit` イシュー（古い未応答から）と総数 |
| JSON（REST・MCP 共通） | `in_review[]` の各項目に `review_since`（RFC 3339）・`review_hours`（小数 1 桁）・`review_stale`（48 時間超）・`review_age`（表示用の `N日M時間` 等。CLI と MCP の本文がサーバの同じ時刻で同じ文字列になるように）。直近の verify が MCP の自己申告なら `verify_self_reported: true`（無ければ省く。本文は行末に `  （直近の verify は MCP の自己申告）`）。新しいキー `feedback: {count（未応答の件数）, issue_count, issues: [{id, title, status, pending, first_at, excerpt（最初の未応答の 80 文字）}]}`。既存キーは変えない（古い CLI が読めるまま）。`counts` に `in_review_stale`・`feedback_pending` |
| MCP `project_summary` | 構造化結果は上の JSON。本文（text）は CLI と同じ 3 層の見出し（組み立ては Go 側で CLI と同じ文言。CLI の表示との一致をテストで確かめる） |
| SessionStart | 変更なし（`summary --limit 12 --agent …`、2 秒で打ち切り） |

**2 秒の制限に収まる根拠**です。いずれも読み取りだけで実測しました。

| 対象 | 方法 | 結果 |
| -- | -- | -- |
| summary 全体 | リモートのサーバに対して `summary --limit 12 --json` の所要時間を 3 回ずつ | 84 件のプロジェクトで 0.19〜0.23 秒・203 件で 0.18〜0.21 秒 |
| 参考: 本文つき全件（board。測った当時。いまの board は本文を含まない（§7）） | 同じ端末から `GET /projects/<slug>/board` | 84 件で 0.40 秒・203 件で 3.41 秒。**summary で本文・コメントを全件読んではいけない**（2 秒を超える）→ 上の SQL でコメントだけを引く |
| 3 層のための 2 クエリ | ローカルの MySQL 8.4（`deploy/dev/compose.yaml`）に使い捨ての DB を作り、1,000 イシュー・コメント 10,000 件（各約 3 KB・37 件に 1 件がフィードバック）・イベント 30,000 件を入れて計測 | 未応答フィードバック（`LIKE`）**17 ms**・同（`REGEXP_LIKE` で先頭の空白を許す案）140 ms・In Review の滞留 **7 ms** |

合わせて 0.2 秒 + 0.03 秒未満なので、2 秒の制限には十分な余裕があります。

#### 8. 横断の文書

kit README（§8 の元の定義）の core / loop の表に「効くループ」列を足します（① AI の作業・② 人の判断・③ 外からの反応）。

| 名前 | 効くループ |
| -- | -- |
| core: CLI・フック本体 | ①②③（`verify`・`list --status "In Review"`・`list --has-feedback`） |
| core: SessionStart `summary --agent` | ①②③（3 層の見出しを毎セッション注入する） |
| core: 鮮度ガード | ① |
| core: usage_hook | ①（計測。どの層の消費も記録する） |
| core: `skills/issue/SKILL.md` | ①②③（B・C の手順を含める） |
| core: 案内節 | — |
| loop: 出力規律・作業規律・引き継ぎ鮮度・iterate（イテレーション規律）・文脈の大きさの警告・記憶注入・背景プロセス検知 | ① |
| loop: 確認モード（task-mode） | ②（「確認して」で止めて人の判断を待つ） |
| サーバの prompt `review` / `loop`・guide（配布物ではない） | ②③ |

メリットの文面は README・AI-GUIDE §1・guide 冒頭に置きます。common.md 冒頭「このシステムで何が整うか」の 3 層の箇条書きは、この設計を実装した後に次の文へ差し替えました。
③ の入口が未実装という注記はそのとき外しています。common.md の箇条書きはこの引用と一字一句同じです（`TestMeritTextMatchesDesign`）。

> - **① AI の作業ループ（数分）**: AI が上の 1 周を自走する。イシューに「## 検証コマンド」を書くと、`verify` が手元で実行して結果をイシューに残し、閉じる前に機械が判定する（プロジェクトによっては成功していないと閉じられない）。
> - **② 人の判断のループ（数十分〜数時間）**: 人の判断が要るものは In Review に集まり、`summary` に滞留（48 時間超に印）が出る。AI は次の `next` の前に判断を持ちかけ（prompt `review`）、返答を `判断:` / `差し戻し:` で残して動かす。人は判断だけをする。
> - **③ 外からの反応のループ（数時間〜数週）**: 利用者・テスターから聞いた反応を AI が `フィードバック:` 付きのコメントでイシューに戻し、`summary` の「外からの反応」に未応答が並ぶ。応答（方針のコメント・起票・状態変更）で消える。

README・AI-GUIDE §1 には同じ 3 行を短くして置きます。§1 は「30 秒」の節なので各 1 文です。
AI-GUIDE §2「ループ運用」には 5. の 2 段落（人の判断待ち・外からの反応）を足します。§3 の表と「閉じる」「探す」の例には `verify` と `list --has-feedback` を加えます。

#### 採らなかった案（まとめ）

| 案 | 理由 |
| -- | -- |
| `comments.kind`（`feedback` 種別）のマイグレーション・`feedback` コマンド・`add_feedback` | 反応を書くのは AI を使う開発者で、先頭語の規約で足りる。列を足すと append-only のテーブルに既定値の移行が要る |
| viewer にフィードバックだけ書かせる権限・画面のフィードバックフォーム・viewer 向けの入口 | 同上。「閲覧のみ」の原則（§1-1・§7）の例外を作らない。viewer はフィードバックを登録できない（2026-09-18 利用者の追加判断。入口は作らない） |
| サーバでの実行 | §1-3 の表 |
| 自己申告の verify を規則で数えない | §1-3 の表（`report_verify` は採用した。印を付けて規則では数える） |
| 版に `issues.version` を使う | §2-1（コメント・状態変更で進み、verify 自身で無効になる） |
| fail-fast・全文の出力・`IM_*` をそのまま渡す | §1-3 の表 |
| 先頭の空白を許す（`REGEXP_LIKE`） | 8 倍遅い。AI に先頭へ書かせれば足りる |
| フィードバックを 7 日で数えなくする | 放置が見えなくなる |
| `判断:` / `差し戻し:` を機械で判定して状態を自動で動かす | 状態は AI が `set_status` で明示的に動かす（誤判定で Done にしない）。「受け入れ条件のチェックと状態の連動」を採らなかったのと同じ理由 |

### 9-4. 下位がすべて完了した要件の検証と close

**症状**: ループの 1 周で閉じるのは着手したイシューだけです。そのため下位（設計・実装）が全部 Done になっても、traces 先の要件は誰にも
促されずに開いたまま残っていました。下位が全部 Done なのに要件が Todo のままという例が複数ありました。

**判定**は `domain.ClosableRequirements` / `ClosableFor` で行います。判定するのはサーバだけで、CLI と MCP は結果を表示するだけです。

| 項目 | 決定 |
| -- | -- |
| 下位 | traces でその要件を指すイシュー（型は問わない。大文字小文字を区別しない。`parent` は見ない） |
| 対象の要件 | `type: requirement` で状態が Backlog / Todo / In Progress。**In Review は除く**（人の判断待ちとして ② に出ている。二重に促さない）。閉じた要件も除く |
| 「完了」 | 下位が 1 件以上あり、**すべて閉じている（Done / Canceled）**うえで、**Done が 1 件以上** |
| Done と Canceled が混在 | 完了として扱う。Canceled は「作らないと決めた」下位で、残りの Done で受け入れ条件を満たしたかを要件側で検証すればよい（内訳 `Done 2 件・Canceled 1 件` を案内に出し、検証の手がかりにする） |
| 全部 Canceled | **対象外**。受け入れ条件を満たした根拠となる下位が 1 件も無く、「検証して close」を促すと誤って Done にされうる。要件を取り下げる（Canceled）か下位を起票し直すかは人の判断（guide に書く）。matrix の表には従来どおり `(Canceled)` で見える |

**出す場所**:

| 場所 | 内容 |
| -- | -- |
| close の応答（`POST /issues/{id}/status`・MCP `set_status`） | 開いていたイシューを閉じた（Done / Canceled）とき、そのイシューの traces 先で上の条件を満たした要件ごとに 1 行、`messages` の末尾に `要件 <ID> の下位がすべて完了しました（Done N 件…）。受け入れ条件を検証して close する: looptrack issue show <ID> で受け入れ条件を確かめ、looptrack issue close <ID> --comment "受け入れ条件の検証結果"（MCP は get_issue → set_status で Done と comment）`。構造化結果に `requirements_ready: [{requirement, children, done, canceled, command, message}]`（対象が無ければキーごと省く）。**`messages` に入れるので、この変更より前の CLI でも表示される**。閉じた状態どうしの変更（Done → Canceled）では出さない |
| matrix（API モードの `?format=md`・MCP `get_matrix`・`looptrack issue matrix`） | ファイルモードと同じ表・警告（`domain.BuildMatrixMarkdown`。比較テストの対象のまま）の**後に**節 `## ⚠️ 下位がすべて完了したのに開いている要件（N 件）` を足す（0 件なら「（なし）」。要件が 1 件も無いプロジェクトでは足さない）。行は `- <ID>(<状態>) <タイトル> — 下位 <内訳>: <下位の ID> → \`<close のコマンド>\``。JSON に `requirements_ready`。**ファイルモードの matrix.md は変えない**（CLI の比較テストはこの節を除いて突き合わせる） |
| summary の ①（REST・MCP `project_summary`・`looptrack hook summary`） | ① の末尾（着手可能の後）に `── 下位がすべて完了した要件（検証して close・N 件） ──` と上位 `limit` 件（要件の ID 順。行は `<ID> <タイトル>（<内訳>）→ <close のコマンド>`）、超えた分は `…ほか N 件（looptrack issue matrix）`。**対象が無ければ節ごと出さない**（SessionStart の注入を増やさない）。JSON に `requirements_ready`（上位 limit 件）と `requirements_ready_total`。CLI は `requirements_ready_total` が無い（古いサーバ）か 0 なら従来どおり |
| guide（`common.md`）・MCP instructions・prompt `loop` | ループの 1 周の「完了」に、応答の案内が出たら次の `next` の前に要件の受け入れ条件を検証して close する手順を足す |

**採らなかった案**:

- 下位の最後の close で要件を自動で Done にする。要件の受け入れ条件は下位の総和とは限りません。検証は AI が行って記録を残します。
- `next` が要件を優先して返す。着手の順序を変えると ready の意味が変わります。応答・summary・matrix で促せば足ります。
- CLI 側で判定する。CLI と MCP で結果が割れますし、古い CLI にも届きません。

### 9-5. トークン計測

コーディング AI のトークン消費を、イシュー操作をきっかけにサーバへ貯めます。
これまではプロジェクトごとの hook が手元のテキスト（CSV）に貯めていました。それを置き換えて全プロジェクトで使います。
**Claude Code 専用にはしません**。Codex などほかのコーディング AI でも使えるようにという利用者の要望（2026-09-18）があったからです。

#### 汎用性のための層分け

| 層 | 内容 | AI ごとの差 |
| -- | -- | -- |
| サーバ（テーブル・API・集計・ルール） | どの AI かを知らない。`client` の列で区別するだけ | なし |
| 送信の経路 ① **CLI 内蔵** | CLI の変更操作（`new` / `push` / `comment` / `status` / `close`）が成功したら、その場でスナップショットを送る。**フックの無い AI でも、CLI を使うだけで付く** | なし（下のアダプタがセッションを見つけられれば） |
| 送信の経路 ② フック | MCP 経由の操作と、ターン終了・セッション終了を拾う。AI ごとの仕組みに合わせる | 下の「各 AI のフック」。無い AI・トークンの記録を読めない AI は ① と ③ だけ |
| 送信の経路 ③ **応答での指示** | トークン情報の無い操作に、サーバが応答で `looptrack issue usage attach <ID>` を実行するよう返す（CLI・MCP のどちらの応答にも）。ツールの出力を読める AI ならどれでも回収できる | なし |
| 収集（アダプタ） | AI の会話記録を読んで共通の形にする。`internal/client/usagesnap` に AI ごとのアダプタを置く | Claude Code: `~/.claude/projects/<slug>/<session>.jsonl` の `usage`。Codex: `~/.codex/sessions/年/月/日/rollout-*.jsonl` の `token_count`（`total_token_usage` は累計。`session_meta` に `id`・`cwd`）。どちらも 2026-09-18 にこの Mac の実物で形を確認。GitHub Copilot: 利用者が有効にした OpenTelemetry のファイル出力（JSON Lines）の `chat` / `invoke_agent` スパンの `gen_ai.usage.*`（Copilot CLI 1.0.86 の実物で 2026-09-19 に確認。出力先は `$COPILOT_HOME/otel/` の下に限る。下の「Copilot のトークン」） |

各 AI のフックは 2026-09-18 に公式文書で確認しました。
フックで「きっかけ」を取れることと、会話記録から「トークン」を読めることは別の話です。両方がそろって初めて ② が使えます。

| AI | ツール実行後 | MCP の操作 | ターン終了 / セッション終了 | 入力にセッション ID・会話記録の場所 | トークンの記録 |
| -- | -- | -- | -- | -- | -- |
| Claude Code | PostToolUse | 拾える（`mcp__<server>__<tool>`） | Stop / SessionEnd | あり | 会話記録の `usage`（実物で確認） |
| Codex | PostToolUse（既定で有効。`.codex/hooks.json` か `config.toml` の `[hooks]`） | 拾える | Stop / SessionEnd | あり（`tool_use_id` も） | 会話記録の `token_count`（実物で確認。内訳は実装時に新しい記録で確かめる） |
| Gemini CLI | AfterTool（`.gemini/settings.json`） | 拾える（`mcp_<server>_<tool>`） | AfterAgent / SessionEnd | あり | 未確認（会話記録の形を実装時に確かめる） |
| Cursor | postToolUse・afterShellExecution・afterMCPExecution（`hooks.json`） | 拾える | stop / sessionEnd | `conversation_id`・`transcript_path`（有効時） | **文書に無い**（フックにも会話記録にも使用量の記載なし）→ きっかけは取れるがトークンは付けられない見込み |
| GitHub Copilot（VS Code のエージェントモード・Copilot CLI。2026-09-18 に文書で確認し、同日の再調査（ソース・changelog）で直した） | `.github/hooks/*.json`（両方が読む）。CLI は camelCase の `postToolUse`、PascalCase の `PostToolUse` なら VS Code 互換の形式。VS Code は PascalCase の 8 イベント。**出力の形が違う**: CLI はトップレベル（`additionalContext`・`permissionDecision`・`decision`）、VS Code は `hookSpecificOutput`（Stop の `decision` も）→ kit は両方に置く（kit/README.ja.md「Copilot での対応」） | 拾える見込み。**VS Code は matcher を無視する**（全ツールで呼ぶ）。CLI の MCP のツール名は `<サーバ>-<ツール>`（`mcp__.*` に合わない）→ hook の中でツール名を見る（`looptrack hook usage --agent copilot` も中で絞る） | CLI: `agentStop` / `sessionEnd`。VS Code: `Stop`（SessionEnd は無い） | CLI の camelCase は `sessionId`・`cwd`（`agentStop` に `transcriptPath`）、PascalCase と VS Code は `session_id`・`transcript_path`（任意）。CLI はシェルに `COPILOT_AGENT_SESSION_ID` を渡す。VS Code はシェルに ID を渡さない（下の「Copilot のセッション ID」） | **利用者が OpenTelemetry のファイル出力を有効にしたときだけ読める**（既定で無効）。CLI は `COPILOT_OTEL_FILE_EXPORTER_PATH`、VS Code は設定 `github.copilot.chat.otel.outfile`（`exporterType: "file"`）で JSON Lines に書く。**出力先は `$COPILOT_HOME/otel/`（無ければ `~/.copilot/otel/`）の下に限る**（CLI は出力先を hook にもシェルにも渡さない）。`gen_ai.usage.*` と `gen_ai.conversation.id`（＝ hook の入力のセッション ID。CLI 1.0.86 の実物で確認）。会話記録 `~/.copilot/session-state/<ID>/events.jsonl` は非公開の形式なので読まない。下の「Copilot のトークン」 |
| Cline・Windsurf（Cascade）・opencode | それぞれ PostToolUse 相当あり（opencode はプラグインの `tool.execute.after`） | — | — | — | 未確認 |

フックの入力の形は Claude Code・Codex・Gemini CLI でほぼ同じです（`session_id`・`transcript_path`・`tool_name`・`tool_input`・`tool_response`）。
そのためトークン情報の hook は 1 本にして、項目名の違いだけを吸収します。

アダプタが返せない項目は 0 / NULL にします（サブエージェントの別・読み書き量・関与の指標）。
AI を足すときに要るのはアダプタ 1 つと、あればフックの配線だけです。

#### 前提（実測で確かめた事実）

| 事実 | 設計への影響 |
| -- | -- |
| トークンの実測値は Claude Code の会話記録（`~/.claude/projects/<slug>/<session>.jsonl` の assistant 行の `usage`）にしか無い。サーバも MCP のツールも知り得ない | 送るのはクライアント側（フック） |
| ツールの実行中、**そのツールを呼んだ応答は使用量つきで既に会話記録に書かれている**（Bash から自分の呼び出しを検索して確認） | CLI の操作中・PostToolUse のどちらの時点の累計にも、その操作自体の消費が入る |
| Claude Code のフックの入力（stdin の JSON）に `session_id`・`transcript_path`・`tool_name`・`tool_input`・`tool_response` がある。Bash には `CLAUDE_CODE_SESSION_ID` が渡る | フックは会話記録の場所をそのまま取れる。CLI は環境変数のセッション ID から場所を引ける。MCP の操作はフックで拾う |
| Codex はシェルのコマンドに `CODEX_THREAD_ID`（会話記録のファイル名・`session_meta.id` と同じスレッド ID）を渡す。新しい版は `CODEX_SESSION_ID`（サブエージェントも共有する親の会話の ID）も渡す（ソースで確認。下の「Codex のセッション ID」） | CLI は環境変数で Codex の会話を特定できる。会話記録を推測で探す必要はない |
| Codex は会話記録に累計の `token_count` を書く。フック（PostToolUse・Stop・SessionEnd。MCP の操作も対象・既定で有効）は Claude Code とほぼ同じ形（公式文書） | Codex も Claude Code と同じ 3 経路が使える |
| 1 回の応答は内容ブロックごとに複数行（同じ `message.id`・同じ `usage`）。サブエージェントは `<session>/subagents/agent-*.jsonl` の別ファイル | **`message.id` 単位で 1 回だけ数え**、subagents/ も読む |
| モデルは応答ごとに記録され、セッション途中で変わる。キャッシュ作成は 1 時間 / 5 分の別がある | モデル別の内訳を持つ（費用換算を後から足せる） |
| 再開したセッションは新しいセッション ID になるが、前の履歴（使用量つき）を複製して持つ | 累計は会話の中で単調に増える。会話 ID で束ねれば二重に数えない |
| Codex デスクトップは中断のあと、同じスレッドを `rollout-<時刻>-<スレッド ID>_<別の ID>.jsonl` の新しいファイルで続け、`total_token_usage` を 0 から数え直す（`session_meta.id` は同じ） | Codex のアダプタは同じスレッドのファイルを全部古い順に読み、ファイルごとの最後の累計を足す（複製された同じ時刻・同じ合計の `token_count` は二度数えない）。応答数は合計が増えた `token_count` の数（新しい版は assistant の `message` を最終回答にしか書かない）。サブエージェント（guardian など）は別のスレッド ID で、本体に入れない |
| Codex の人の指示は、旧形式では `event_msg` の `user_message`（`payload.message` が本文）。新しい版（デスクトップ 26.915・codex-cli 0.155 で確認）は `user_message` を書かず、`event_msg` の `item_completed`（`payload.item.type` が `UserMessage`・本文は `item.content` の `type: text` の `text`・`payload.turn_id` がターン）に書く。同じ指示は `response_item` の `message`（`role: user`・`internal_chat_message_metadata_passthrough.content_item_kinds` が `user.text`）にもあるが、`role: user` には AGENTS.md・`environment_context`・`turn_aborted` などの差し込みも混ざる。ターンは `task_started` 〜 `task_complete` / `turn_aborted` | Codex のアダプタは両方の形を人の指示として読み（同じ指示が両方にあれば 1 回）、`role: user` の `message` は使わない。区間・関与の指標・「レポート対象外」の判定・会話 ID（最初の指示の時刻 + MD5）を Claude Code と同じ規則で作る。実行中のターンに届いた 2 件目の指示は割込。区間の終わりは最後の応答（合計が増えた `token_count` か assistant の `message`）。区間ごとの消費はその区間に増えた累計（和は会話の累計と一致。一致しなければ最後の区間にまとめる）。新しい版の会話は、それまで会話 ID がスレッド ID の先頭 8 桁だった（UUIDv7 のため約 65 秒以内に始めたスレッドどうしで重なる） |

#### 記録の単位と保存の方針

| 項目 | 決定 | 理由 |
| -- | -- | -- |
| 単位 | **スナップショット**＝ある時点の「会話の累計」1 件。イシュー操作ごと・ターン終了（間引き）・セッション終了で送る | 差分を送ると、1 件の欠落・重複がそのまま誤差になる。累計なら欠けても次の 1 件で追いつく |
| 差分と帰属 | **保存しない。問い合わせのときに計算する** | 帰属の規則を後から直しても、データを作り直さずに済む |
| 追記のみ | `usage_snapshots` は SELECT・INSERT のみ（`deploy/grants.sql`。comments / issue_events と同じ。台帳 `usage_reports` も同じ） | 実績の改ざん・取り違えを DB 権限でも防ぐ |
| 指示文 | **既定では送らない**。区間ごとの数値（開始時刻・種別・待ち分・所要分・トークン）だけ送る。先頭 44 文字の作業名（区間の `label`）を送るのは、`projects.rules` の `usage.send_prompts: true` と利用者の環境変数 `LOOPTRACK_USAGE_SEND_PROMPTS`（`0` で止める。`1`・未設定はルールに従う）の**両方が許すときだけ**。CLI・フックはルールを `POST /usage` の応答の `send_prompts` で知って手元に覚え（looptrack の置き場の `usage-send-prompts/`・呼び出しは増やさない）、次の送信から従う。覚えていない（最初の 1 件・古いサーバ）ときは送らない。サーバも、許さないプロジェクトに届いた `label` は保存しない | 顧客情報・未修正の脆弱性が指示文に混ざる |

#### テーブル `usage_snapshots`（マイグレーション 0003）

| 列 | 内容 |
| -- | -- |
| `id` / `project_id` / `user_id` / `token_id` / `received_at` | 受け取った側の情報 |
| `issue_status`（32） | 受け取った時点のイシューの状態（サーバが埋める）。stop / session_end の区間を「直前に操作した未クローズのイシュー」へ寄せる判定に使う |
| `client`（32）・`client_version` | `claude-code` / `codex` / `copilot` / `other`。集計は AI をまたいで合算でき、内訳も出せる |
| `session_id`（128）・`conversation_id`（64） | 会話 ID は「最初の人間の指示の時刻（現地・秒）+ 内容の MD5 先頭 6 桁」（導入前の hook と同じ。再開しても変わらない）。人間の指示が無ければセッション ID の先頭 8 桁 |
| `trigger_kind` | `issue_op` / `stop` / `session_end` / `manual` / `import` |
| `issue_id`・`op` | `issue_op` のときの対象と操作（`create` / `update` / `comment` / `status`。issue_events.kind と同じ語）。それ以外は NULL |
| `via` | その操作の経路（`cli` / `mcp`） |
| `at` | 会話記録の最後の時刻（クライアント側・UTC） |
| `main_input` `main_cache_create` `main_cache_read` `main_output` `sub_input` `sub_cache_create` `sub_cache_read` `sub_output` | 累計（BIGINT）。本体とサブエージェント。Codex は `input_tokens − cached_input_tokens` → input、`cached_input_tokens` → cache_read、`output_tokens` → output（推論分 `reasoning_output_tokens` は `by_model` に内訳で持つ）、cache_create とサブは 0 |
| `responses` `sub_responses` | 応答数（`message.id` の一意数） |
| `by_model`（JSON） | モデル → 4 種のトークン・応答数・キャッシュ作成の 1 時間 / 5 分の別 |
| `io`（JSON） | 読込回数・行数・KB、PDF / 画像の回数・KB、編集回数・追加行・削除行・書込 KB |
| `human`（JSON） | 純指示数・自動再開数・割込数・待ち中央値分・待ち 5 分以内率・長時間中断数・AI 作業分 |
| `segments`（JSON・NULL 可） | 区間（人間の指示ごと）の数値の一覧。**`stop` / `session_end` のときだけ**送る（毎回送ると重い。会話の最後の 1 件を使えば足りる） |
| `branch` `branches`（JSON）`cwd_name` | ブランチ（最終と観測した全部。記録に無い AI は送信時の `git rev-parse`）、作業ディレクトリ名 |
| `dedupe_key`（CHAR(64)・UNIQUE） | 下記 |

**冪等**: `dedupe_key` = SHA-256（`session_id` + 操作の識別）です。
操作の識別にはフックの入力の `tool_use_id` を使います。無ければ `trigger_kind`・`issue_id`・`op`・応答数・合計トークンです。
同じキーの再送は `INSERT IGNORE` で 200（`duplicate: true`）を返します。スプールからの再送や Stop の多重起動で件数は増えません。

#### 差分と帰属（問い合わせ時の計算）

1. 会話 ID ごとにスナップショットを累計の合計の昇順に並べます（同値なら `at`・`id` の順）。
2. 隣り合う 2 件の差を「区間の消費」とします（会話の最初の 1 件は 0 からの差）。差が負になる行は巻き戻った枝なので 0 として捨て、件数を「不整合」として返します。
3. **区間は、その区間を閉じたスナップショットのイシューに帰属させます**。起票の前の調査は起票したイシューへ、コメント・状態変更の前の作業はそのイシューへ入ります。
4. `stop` / `session_end` で閉じた区間は、その会話で直前に操作したイシューが未クローズならそこへ入れます。そうでなければ **未帰属** です。
5. 1 回の応答で複数のイシューを操作した場合（並列のツール呼び出し）は累計が同じなので、2 件目以降の差は 0 になります。つまり先に届いた 1 件に寄ります。

「作業中のイシューを In Progress で宣言させる」方式は採りません。宣言を忘れるとそのまま未帰属になります。しかも MCP の操作ではセッション ID が取れないので、宣言と区間を結べません。

#### イベントとの突き合わせ（付与漏れの検知）

`issue_events` は追記のみで、後から印を付けられません。そこで**結合で判定します**。
同じイシュー・同じ利用者のスナップショットのうち、`received_at` がイベントの `at` 以後 10 分以内のものがあれば「付与済み」です。
イベントの後に届いた `manual`（`usage attach` による回収）も、10 分を過ぎていても付与済みとします。回収すれば一覧と summary から消えます。
対象は変更操作です（`kind` が `create` / `update` / `comment` / `status`）。`rule_override` は同時の変更に付随するので数えません。
突き合わせに使うので、`usage_snapshots.received_at` には DB の既定値ではなく**サーバの時計**の値を入れます（`issue_events.at` と同じ）。

| イベント | 扱い |
| -- | -- |
| `via = mcp` | 対象（AI からの操作） |
| `via = mcp` かつ `detail.agent` が計測を利用者が有効にしたときだけ測れる AI（`copilot`） | その利用者のその AI（`client = copilot`）のスナップショットが、同じプロジェクトに操作の 7 日前から操作の 10 分後までに届いていれば**対象**、無ければ**対象外**（付けようがない。下の「Copilot のトークン」） |
| `via = cli` かつ `session_id` あり | 対象（Claude Code の Bash から入る） |
| `via = cli` かつ `detail.agent` が計測の任意な AI（`copilot`） | 計測を有効にしていない利用者（直近 7 日に `client = copilot` のスナップショットが無い）なら**対象外**、有効なら対象（Copilot CLI は `session_id` あり、VS Code は無し。`humans` には数えない） |
| `detail.session_kind = host`（経路は問わない） | **対象外**（器のセッション ID。会話記録と結び付かないので付けようがない。`humans` にも数えない。下の「器のセッション ID」） |
| `via = cli` かつ `session_id` なし・`detail.agent` なし | **対象外**（人がターミナルから打った操作。`humans` に数える） |
| `via = web` / `import` / `admin`、`kind = import` | 対象外 |

claude.ai のコネクタのようにフックを置けないクライアントからの MCP 操作は、必ず未付与になります。回収もできません。件数が問題になったら OAuth クライアント単位で対象外にします。
Codex のシェルから打った CLI は `CODEX_THREAD_ID` を `X-Looptrack-Session` に載せるので、AI の操作として対象になります。以前はこれを読んでおらず、「人の操作」として対象外でした。

既知の限界があります。同じ応答の中で同じイシューに同じ操作を 2 回すると（1 つの Bash で `comment` を 2 回など）、2 回目のスナップショットは累計が同じなので重複として捨てられます。
そのため 2 回目の操作は未付与に数えられます。これは `usage attach` で回収できます。

| 出し先 | 内容 |
| -- | -- |
| `GET /projects/{slug}/usage/coverage?days=&mine=` | 期間（既定 30 日・1〜366）の AI 操作の数（`target`）・付与済み（`attached`）・未付与（`missing_count`・`missing[]` に イシュー・操作・経路・時刻・利用者）・充足率（`rate`。対象 0 件は null）・対象外の人の操作の数（`humans`）・未付与のイシュー（`issues`）。`mine=1` で呼び出した利用者の操作だけ。閲覧権限で読める |
| CLI | `looptrack issue usage missing [--days N] [--all-users] [--json]`（既定は自分の操作だけ） |
| MCP | `usage_missing`（`days`・`all_users`） |
| SessionStart | `summary`（API と CLI）と MCP の `project_summary` の末尾に「トークン情報の未付与 N 件（直近 7 日・あなたの AI 操作・ID…）。回収: イシューごとに `looptrack issue usage attach <ID>`」。**呼び出した利用者の分だけ**（summary はセッション開始時に呼ぶので、前のセッションの分は会話では絞れない。利用者で絞る） |

#### 器のセッション ID

デスクトップ版の窓（器）によっては、Bash に `CLAUDE_CODE_SESSION_ID` が渡りません。渡るのは `CLAUDE_CODE_HOST_SESSION_ID`（その窓を通して一定の ID）だけです。
実測では、上位のセッションのプロセスはすべて器の ID だけを持っていました。両方を持つのは子のエージェントの Bash だけです。
この ID は**並行するセッションを見分けるには足ります**（同じ利用者の別の窓は別の値になります）。
ただし**会話記録（`~/.claude/projects/*/<セッション ID>.jsonl`）のファイル名とは一致しません**。実測では、器の ID は 42 文字で対応するファイルが無く、`CLAUDE_CODE_SESSION_ID` は 36 文字でファイルがありました。

| 論点 | 決定 |
| -- | -- |
| `X-Looptrack-Session` に載せるか | **載せる**。セッションを見分けるのが目的で、そのためには足りる（§5-4 の読み取り順の 3 番目） |
| トークン情報を引くのに使うか | **使わない**（`usagesnap.Detect` は読まない。引けるファイルが無い） |
| 付与の対象（付与の指示・`usage.require_on_close`・未付与の検知）にするか | **しない**。クライアントが `X-Looptrack-Session-Kind: host` を添えて「この ID は会話記録と結び付かない」と伝え、サーバは `service.UsageTarget` で対象から外し、`issue_events.detail.session_kind` に残して `store.UsageCoverage`（未付与の一覧・充足率・`summary`）からも外す。`humans`（人の操作）にも数えない（計測を有効にしていない利用者の Copilot の操作と同じ扱い） |
| 判定を経路（CLI / MCP）で絞るか | **絞らない**。印は「送ってきた経路」ではなく「そのセッション ID の種類」を表すので、判定の根拠は `session_kind` だけにする（`internal/server/mcp.go` の `mcpCallOf` も REST の `actor` と同じ判定でこのヘッダを読み、`service.UsageTarget` と `store.usageHostSessionCond` は経路で絞らない）。規則を 1 か所に置くため。**ただし `Mcp-Session-Id` の接続 ID を代用したとき（`mcp-conn:`）には印を付けない**（サーバが発行した値で、器かどうかとは関係が無い） |
| MCP 経路で実際に印は届くか | **今は届かない**。MCP の接続設定のヘッダは接続ごとの固定の文字列で、`setupwiz.MCPConfigs` が配る設定は `X-Looptrack-Project` しか入れない（実測: 手元の Claude Code の `looptrack` の接続には `headers` がそもそも無く、`project` 省略の呼び出しが「プロジェクトを指定してください」で返る）。**「器の窓かどうか」は窓ごとに変わる実行時の性質なので、同じ設定ファイルを共有する固定のヘッダでは正しく表せない**（同じ設定を普通のターミナルからも使うため）。したがって**器の窓からの MCP 操作は、今のところ付与の対象のまま残る**（未付与として積み上がる）。送る側の手当ては別に決める |
| 古い CLI・古いサーバ | **黙って悪くならない**。印を送らない古い CLI の操作は今までどおり対象になり、印を読まない古いサーバはヘッダを読み捨てるだけで 400 にはならない。サーバは知らない種類の値も読み捨てる（＝会話のセッション ID として扱う） |

**`CLAUDE_CODE_REMOTE_SESSION_ID` の判断（「クラウド版の AI」）との関係**: どちらも「会話記録と結び付かない ID」という同じ形の問題ですが、**採った手当ては違います**。
`CLAUDE_CODE_REMOTE_SESSION_ID` は**読みません**（`X-Looptrack-Session` に載せません）。クラウド版では `CLAUDE_CODE_SESSION_ID` が渡る見込みです。
載せてもセッションを見分ける利点は無く、付与の対象になるという害だけが残ります。
器の ID は**載せたうえで付与の対象から外します**。`CLAUDE_CODE_SESSION_ID` が渡らないことが実測で分かっている窓だからです。
これを載せないと**並行するセッションをまったく見分けられなくなります**。つまり載せる利点が実際にあります。
**判断の基準は同じ**で、トークン情報を付けられない ID を付与の対象にしません。違うのは「その ID を載せる利点があるか」だけです。

テストは次のとおりです。

- `internal/server/hostsession_test.go`: 環境変数 4 通り × 送るヘッダ・付与の対象・会話記録の表を、送る側 `session.Detect` と受け取る側 `actor` / `service.UsageTarget` と
  `usagesnap.Detect` でまとめて判定します。
- `internal/server/usage_coverage_test.go` の `TestUsageHostSessionNotTarget`: 指示・充足率・`summary`・`usage.require_on_close`・古い CLI。
- `internal/client/session/session_test.go`・golden `internal/clitest/testdata/golden/headers/claude-code-host.golden`。
- `internal/server/sessionkind_test.go`（**DB 無しで走ります**）: 印 `host` を表す 3 つの定数 `session.KindHost` / `service.SessionKindHost` / `store.SessionKindHost` が一致すること・
  `mcpCallOf` が REST の `actor` と同じにヘッダを読み、接続 ID の代用には印を付けないこと・`UsageTarget` が経路によらず印で外すこと。

**3 つの定数は `store` が `service` を参照できない都合で別々に定義されています**。そのため以前は値のずれが DB の要るテストでしか見つからず、DB 無しの検査はすべて緑のままでした。

#### Codex のセッション ID

CLI の操作を「AI の操作」と判定する根拠は `X-Looptrack-Session` です（上の表）。Codex では次の環境変数から得ます（2026-09-18 に確認）。

| 変数 | 中身 | 入った版 | 根拠 |
| -- | -- | -- | -- |
| `CODEX_THREAD_ID` | そのスレッド（会話）の ID。会話記録 `rollout-<時刻>-<ID>.jsonl` のファイル名と `session_meta.id` に一致 | 2026-02（openai/codex #10096） | `codex-rs/core/src/exec_env.rs`（shell ツール・unified exec・利用者の `!` コマンドに注入。`shell_environment_policy` の `include_only` でも消えない）。この Mac の Codex.app 同梱の codex-cli 0.135.0-alpha.1 のバイナリにも文字列がある。実物の rollout で `session_meta.id` がファイル名の ID と一致 |
| `CODEX_SESSION_ID` | 親の会話（root session）の ID。サブエージェントのスレッドも親と同じ値 | 2026-08（#37848） | 同上 `inject_session_env`。0.135 には無い |

**判断**: CLI は `LOOPTRACK_SESSION_ID` → `CLAUDE_CODE_SESSION_ID` → `CLAUDE_SESSION_ID` → `CODEX_THREAD_ID` → `CODEX_SESSION_ID` の順に読みます。
THREAD を先にするのは、会話記録とイベントの ID を揃えるためです（`usage_snapshot.detect` が同じ順でファイル名を引きます）。サブエージェントで SESSION を使うと親の記録を指してしまいます。
Claude Code の変数を先にするのは、以前からの挙動を変えないためです。
片方の中からもう片方を起動すると、両方の変数が子に残ります。内側がどちらかは環境変数からは分からないので、既存の順を保ちます。

**会話記録からの推測（cwd が一致する直近の rollout の `session_meta.id`）は `X-Looptrack-Session` には使いません**。
環境変数が 2026-02 から全経路で入るので要らないからです。
推測すると、同じディレクトリで Codex を開いたまま人が端末から打った操作まで AI の操作になります（未付与の検知とクローズ時の必須判定の対象になります）。
同じディレクトリで並行する別の Codex の会話に誤って帰属することもあります。
トークン情報の送信（経路 ①）での cwd による推測は従来どおり残します。送るのは累計なので、誤っても重複排除と差分で害は小さく済みます。

既知の限界として、Codex の TUI で利用者が `!` で打ったコマンドにも ID が入るので、AI の操作に数えます。
これは Claude Code の `!` と同じ扱いです。同じ会話の中の操作なので、トークン情報も付きます。
Codex のアプリサーバの単発のコマンド実行（スレッドに属さない）には入りません（`create_env(…, None)`）。2026-02 より前の Codex は ID を渡さないので、従来どおり人の操作として対象外になります。

#### Copilot のトークン

**経緯**: 最初の調査（2026-09-18 22:03）では「測れない」と判断し、Copilot の MCP の操作を未付与の判定から外しました。
再調査（同日 22:23）で、CLI・VS Code とも OpenTelemetry（OTel）をファイルに書き出せると分かりました。そこには使用量とセッション ID が入ります。
そこで利用者の判断（同日 22:27）により、扱いを広げました。OTel のファイル出力を有効にした利用者の Copilot は計測し、無いときは今の扱い（未付与に数えない）を残します。

**確かめたこと（Copilot CLI 1.0.86 の実物・2026-09-19。VS Code は公式文書だけで実物は未確認）**

公式文書で調べたうえで、Copilot CLI 1.0.86（macOS・`copilot -p` と対話モード）で確かめた範囲を書きます。見たのは実物の出力と、観察用の hook が受け取った環境です。
文書と実物が違った点は実物に合わせました（hook に出力先が渡らない・属性名が `cache_write`）。

| 項目 | 内容 | 確かめ方・出典 |
| -- | -- | -- |
| 有効にする方法（CLI） | 環境変数 `COPILOT_OTEL_FILE_EXPORTER_PATH=<ファイル>` を設定すると OTel が有効になり、全部のシグナル（span・metric）を JSON Lines で書く（`COPILOT_OTEL_EXPORTER_TYPE` は自動で `file`）。既定は無効。**出力先は `$COPILOT_HOME/otel/` の下（`COPILOT_HOME` が無ければ `~/.copilot/otel/`）にする**（次の行） | 実物（1.0.86）・[CLI の command reference「OpenTelemetry monitoring」](https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-command-reference) |
| hook・シェルに渡る環境 | **`COPILOT_OTEL_FILE_EXPORTER_PATH` は hook にもエージェントのシェルにも渡らない**。hook に渡る Copilot の変数は `COPILOT_CLI=1`・`COPILOT_CLI_BINARY_VERSION`・`COPILOT_HOME`・`COPILOT_PROJECT_DIR`・`COPILOT_TRACEPARENT` だけ（ほかに `COPILOT_CLI_RESOLVED_DIST_DIR` と、Claude Code 互換の `CLAUDE_PROJECT_DIR`）。シェルには `COPILOT_CLI=1` と `COPILOT_AGENT_SESSION_ID` が渡る。よって出力先を既定の外にすると、`usage attach` も Stop・SessionEnd の hook も記録を見つけられない（実物で「会話記録が見つかりません」・スナップショット 0 件を再現）。`$COPILOT_HOME/otel/` の下なら見つかる | 実物（観察用の hook が受け取った環境） |
| 有効にする方法（VS Code） | 設定 `github.copilot.chat.otel.enabled: true`・`github.copilot.chat.otel.exporterType: "file"`・`github.copilot.chat.otel.outfile: <ファイル>`（または同じ環境変数）。既定は無効。`outfile` も CLI と同じ置き場（`~/.copilot/otel/` の下）にする（VS Code の設定ファイルは読まない） | 文書だけ（実物は未確認）: [VS Code の Monitor agent usage with OpenTelemetry](https://code.visualstudio.com/docs/agents/guides/monitoring-agents) |
| 1 行の形（CLI） | OTLP の JSON を 1 行ずつ: `{"type": "span" \| "metric", "traceId", "spanId", "parentSpanId", "name", "kind", "startTime": [秒, ナノ秒], "endTime": [秒, ナノ秒], "attributes": {…}（辞書）, "status", "events", "resource": {"attributes": {"service.name": "github-copilot", "service.version": "1.0.86"}}, "instrumentationScope"}`。metric の行は `dataPoints` を持ち、読まない | 実物（1.0.86） |
| スパン | `invoke_agent`（根。子の `chat` の合計を持つ。実物では 1 つの根（同じ `traceId`）が 2 回の指示にまたがった例があり、指示 1 回とは限らない）・`chat`（LLM の呼び出し 1 回。名前は `chat <要求のモデル>`、例 `chat auto`）・`execute_tool`（名前は `execute_tool <ツール>`） | 実物（1.0.86） |
| 使用量の属性（`chat`） | `gen_ai.usage.input_tokens`（**キャッシュを含む**）・`gen_ai.usage.cache_read.input_tokens`・**`gen_ai.usage.cache_write.input_tokens`**（文書の `cache_creation` ではない）・`gen_ai.usage.output_tokens`・`gen_ai.usage.reasoning.output_tokens`。無い属性は出ない（最初の応答は `cache_read` が無く `cache_write` だけ、推論の無い応答は `reasoning` が無い）。実物の 10 回の `chat` で `input_tokens − cache_read − cache_write` はどれも 3（合計 30） | 実物（1.0.86）・[OTel GenAI の属性](https://opentelemetry.io/docs/specs/semconv/registry/attributes/gen-ai/) |
| 使用量の属性（`invoke_agent`） | `gen_ai.usage.input_tokens`（`chat` の `input_tokens` の和＝キャッシュを含む）と `gen_ai.usage.output_tokens` だけ。**cache の属性は無い**。ほかに `github.copilot.turn_count`（`chat` の回数） | 実物（1.0.86） |
| セッション | `gen_ai.conversation.id` は hook の入力の `session_id` / `sessionId`、シェルの `COPILOT_AGENT_SESSION_ID` と同じ値。モデルは `gen_ai.response.model`（`gen_ai.request.model` は `auto` のことがある）。版は資源属性 `service.version`。hook の入力の `traceparent` は OTel の `traceId` と同じ | 実物（1.0.86） |
| ツール名 | MCP のサーバ名・ツール名は OTel ではハッシュ（16 進）で出る（`github.copilot.context.mcp_server_names`・イベントの `github.copilot.mcp.server.name_hash` など）。hook の入力の `toolName` はハッシュされない（`im-guide` の形）。使用量の計算はツール名を使わない | 実物（1.0.86） |
| 書き出しの遅れ | 目立った遅れは無く、hook の 6 秒の待ち（`LOOPTRACK_USAGE_COPILOT_WAIT_SEC`）で MCP の操作ごとのスナップショットが付いた | 実物（1.0.86） |
| 既知の不具合 | 並列のサブエージェントで、成功した `chat` に使用量が入らないことがある（未解決） | [github/copilot-cli#4860](https://github.com/github/copilot-cli/issues/4860) |
| 記録しないもの | 指示文・応答の内容（`captureContent` / `OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT` を有効にしたときだけ入る。IM は読まない・有効にすることを求めない） | 文書 |

**アダプタ（`internal/client/usagesnap/copilot.go`）**

| 項目 | 決定 |
| -- | -- |
| 読むファイル | ① `LOOPTRACK_USAGE_COPILOT_OTEL`（ファイルかディレクトリ・`:` 区切りで複数）② `COPILOT_OTEL_FILE_EXPORTER_PATH`（その変数が見える環境だけ。**Copilot CLI は hook にもシェルにも渡さない**ので、実際には利用者が同じ値を自分のシェルで設定したときだけ効く）③ `${COPILOT_HOME:-~/.copilot}/otel/` の下（サブディレクトリも）の `*.jsonl`。③ は環境変数が無くても必ず見る。見つかったものを全部読む（同じファイルは 1 回）。**hook と `usage attach` が確実に見つけられるのは ③ だけ**なので、CLI の出力先も VS Code の `outfile` も ③ の下にしてもらう（AI-GUIDE §7-3-1。VS Code の設定ファイルは読まない）。① を hook に渡す配線はしない（置き場を 1 つに決める方が、利用者の設定と hook の配線の両方を合わせるより崩れにくい） |
| 1 行の読み方 | Copilot CLI 1.0.86 の形（トップレベルの ID・辞書の属性・`[秒, ナノ秒]`）を読む。VS Code の形は未確認なので、ほかの形も読む: ID は `spanContext`・`_spanContext`・`context`、属性は OTLP の `[{key, value}]`、時刻は数値（桁で秒〜ナノ秒）・ISO 8601。`gen_ai.operation.name`（無ければ名前の先頭語）が `chat` / `invoke_agent` の行だけ。読めない行・メトリクス・ログは捨てる |
| 会話 | `gen_ai.conversation.id` が hook の入力のセッション ID に一致するスパンだけ（ID の無い `chat` は同じトレースの他のスパンから引く）。**会話 ID（`conversation_id`）はセッション ID**（指示文が記録に無く「最初の指示の時刻 + 内容」を作れない。Copilot は再開しても同じ ID） |
| 重複 | 同じスパン（`traceId` + `spanId`、無ければ `gen_ai.response.id`、無ければ行の中身）は 1 回だけ数える |
| 合計 | トレース（`traceId`。ふつうは利用者の指示 1 回）ごとに「`chat` の和」と「`invoke_agent` の最大（根）」の**項目ごとの大きい方**。`chat` は呼び出しの完了ごと、根は指示の終わりに書かれる。#4860 で欠けた分は根が書かれた時点で埋まる。どちらも増える一方なので、累計は単調に増える。**cache の属性が無い根**（Copilot CLI 1.0.86 の実物: 根は `input_tokens`＝`chat` の和（キャッシュを含む）と `output_tokens` だけ）は、同じトレースの `chat` の入力から引いたキャッシュの和を根の `input_tokens` から引いてから比べる（引かないと入力にキャッシュが二重に入り、stop・session_end の累計が 2 倍近くになった）。`chat` が全部欠けた指示は引くものが無く、根の値（キャッシュを含む）のままになる（過大な推定） |
| 対応 | 入力 = `input_tokens − cache_read − cache_write`（キャッシュの作成は `cache_write`（CLI 1.0.86 の実物）、無ければ文書の名前 `cache_creation` を読む。`input_tokens` がその和より小さい出力元はキャッシュを含まないとみなして引かない）、`cache_read`・`cache_create`（＝`cache_write`）・`output` はそのまま。推論分は `by_model` の `reasoning`。サブエージェントの別は取らない（`sub` は 0）。応答数は使用量のある `chat` の数（根の `turn_count` が多ければそれ） |
| 区間 | トレースごと（種別 `human`・作業名は無い）。区間の時刻はスパンの開始・終了 |
| 送らないとき | そのセッションのスパンが 1 つも無い（OTel が無効・出力先が ③ の外）→ `None`。hook は何も送らない。Copilot CLI のシェルの `usage attach` は、探した結果（③ にファイルが無い／あるがそのセッションのスパンが無い）と「出力先を `$COPILOT_HOME/otel/` の下にする（CLI は出力先を hook にもシェルにも渡さない）」を示して止まる（`usagesnap.CopilotMissHint`。`LOOPTRACK_USAGE_DEBUG=1` なら変更操作の後の付与でも標準エラーに出す。hook のログへの表示は hook の担当範囲で未対応） |
| 版 | 資源属性 `service.version` を `client_version` に |

**hook（`looptrack hook usage`）**: 次の 2 つの形を Claude Code と同じ項目名にそろえます。
Copilot CLI の camelCase の入力（`sessionId`・`toolName`・`toolArgs`（JSON の文字列）・`toolResult.resultType`）と、VS Code 互換の形（`tool_result.result_type`）です。
camelCase には event 名が無いので、配線で `--event postToolUse` などを付けます。
VS Code は入力から Copilot と分からないので `--client copilot` を付けます。CLI の camelCase の入力と `~/.copilot/` の下の `transcript_path` は自動で Copilot と判定します。
Copilot の MCP のツール名は、CLI 1.0.86 の hook の入力では `im-guide` の形でした。
VS Code は未確認なので、Copilot のときだけ広く受けます。`mcp` の前置は任意で、サーバ名との区切りは `__` `_` `-` `/` `.` のどれでも構いません。
OTel のスパンはまとめて書き出されます。そのため切り離した後に `LOOPTRACK_USAGE_COPILOT_WAIT_SEC`（既定 6 秒）待ってから読みます。
**配線（`.github/hooks/*.json` に usage_hook を入れること）は init の担当です**。入るまでは、利用者が手で配線したときだけ動きます。

**サーバの判定**: `store.UsageOptInAgents`（今は `copilot`）の AI の MCP の操作を付与の対象（`service.UsageTarget`）にするのは、
**その利用者のその AI のスナップショット（`client = copilot`）が同じプロジェクトに直近 7 日（`store.UsageOptInWindow`）以内に届いているときだけ**です。
対象なら、次の 3 つが Claude Code と同じに効きます。

- 応答の付与の指示（経路 ③。フックの MCP の送信が 7 日以内にあれば従来どおり出ません）
- クローズ時の必須（`usage.require_on_close`）
- Done の警告

未付与の検知（`usage/coverage`・`usage missing`・MCP の `usage_missing`・summary）は操作ごとに判定します。
操作の 7 日前から 10 分後までに、その利用者のその AI のスナップショットがあれば数えます。操作自体のフックの送信も目印になります。
**有効にする前の操作は、後から有効にしても対象に戻りません**。どちらも無ければ従来どおり対象外です。
判定の材料は既存の列（`usage_snapshots.client`・`issue_events.detail.agent`）だけなので、マイグレーションはありません。

- MCP の変更系のツール（`create_issue`・`add_comment`・`set_status`・`update_issue`・`assign_issue`・`next`・`report_verify`）は、接続してきた AI（`clientInfo` から §6 の `agentOf`）を `Actor.Agent` に持ちます。
  それを `issue_events.detail` の `agent` に残します。detail は JSON なので列は足さず、マイグレーションも要りません。
- `store.UsageOptInAgents`（今は `copilot` だけ）の AI の操作を `service.UsageTarget` が対象外にするのは、その利用者が計測を有効にしていないときだけです。つまり同じプロジェクトに直近 7 日の `client = copilot` のスナップショットが無いときです。このとき応答の付与の指示（経路 ③）・クローズ時の必須（`usage.require_on_close`）・Done の警告は出ません。有効にしている利用者の操作は Claude Code と同じに数えます。
- 未付与の検知（`usage/coverage`・`usage missing`・MCP の `usage_missing`・summary の未付与の行）も同じ条件です。`via` が mcp / cli かつ `detail.agent` がその AI の操作のうち、計測を有効にしていない利用者の分は数えません（`target` にも `humans` にも入りません）。
- Copilot のシェルから打った CLI の操作は、`X-Looptrack-Agent: copilot` によって `detail.agent = copilot` になります。CLI は `COPILOT_AGENT_SESSION_ID` を session_id に載せます。`humans` には数えません。
- init は Copilot に `looptrack hook usage --agent copilot` を配線します。OTel のファイル出力が無ければ何も送りません。
- **Copilot CLI のシェルから打った CLI の操作**: Go 版の `usagesnap.Detect` は `COPILOT_CLI=1` と `COPILOT_AGENT_SESSION_ID` から Copilot のセッションを特定します。
  判定は `internal/client/session` と同じで、順番は Claude Code・Codex の変数の後、作業ディレクトリで Codex を推測する前です。
  `usage attach` と変更操作の後の自動の付与（経路 ①）は、OTel のファイル出力からスナップショットを作って付けます。OTel が無ければ何も送りません（`usage attach` は「会話記録が見つかりません」を返します）。
  VS Code のエージェント用ターミナル（`AI_AGENT=github_copilot_vscode_agent` / `COPILOT_AGENT=1`。セッション ID が無い）では、作業ディレクトリで Codex を推測しません。同じディレクトリの Codex の記録を Copilot の操作に付けないためです。

既知の限界:
- 判定は利用者単位で、Copilot の製品（CLI・VS Code）を区別しません。`clientInfo` の名前で区別はできますが、`agent` は同じ `copilot` です。
  そのため CLI だけ OTel を有効にした利用者の VS Code の操作は、7 日以内に CLI のスナップショットがあると対象になり、未付与に数えられます。VS Code も有効にするか、`override_reason` でクローズしてください。
- 計測している利用者がクローズ時の必須で拒否・警告されると、案内には `usage attach` が出ます。Copilot CLI では実行できますが、VS Code ではセッション ID が無いので付けられません。起票・コメントの hook の送信で付いていれば出ません。
- 判定は `clientInfo` の名前で行います。VS Code から Copilot 以外の拡張が同じ名前で接続すると、Copilot として扱います。`agent` を持たない記録は `other` と同じく対象のままです。
- 1 行の形・ツール名の形・書き出しの遅れは Copilot CLI 1.0.86 の実物で確かめました（2026-09-19）。VS Code の OTel の出力は実物では未確認です。Copilot の版の更新で形が変わったら、上の表と `internal/client/usagesnap/copilot_otel_test.go`（1.0.86 の形のテスト）を直してください。
- 出力先を `$COPILOT_HOME/otel/` の外にした会話は計測できません（Copilot CLI が出力先を hook・シェルに渡さないため）。`usage attach` の案内で気づけますが、hook（Stop・SessionEnd・MCP の操作）は何も言わずに送らないままになります。

#### Copilot のセッション ID

GitHub Copilot（VS Code のエージェントモード・Copilot CLI）について、2026-09-18 にソースと changelog で再調査しました。
最初の調査は公式文書だけで「渡らない」と結論しましたが、それを訂正しています。
Copilot CLI 1.0.86 の実物（2026-09-19）では、シェルと hook に `COPILOT_CLI=1` と `COPILOT_AGENT_SESSION_ID`（hook の session_id と同じ値）が渡ることを確かめました。VS Code は実物では未確認です。

| 事実 | 確度・出典 |
| -- | -- |
| **Copilot CLI** は、エージェントが実行するシェルコマンドと stdio の MCP サーバに `COPILOT_AGENT_SESSION_ID`（セッション ID）を渡す。`COPILOT_CLI=1`・`COPILOT_CLI_BINARY_VERSION` も付く。`--session-id <UUID>` で呼び出し側が ID を決めることもできる | [copilot-cli の changelog](https://github.com/github/copilot-cli/blob/main/changelog.md)（1.0.29）。公式の環境変数表（[command reference](https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-command-reference)）には無い |
| **VS Code** はセッション ID をターミナルに渡さない。ただし**エージェント用のターミナルにだけ** `AI_AGENT=github_copilot_vscode_agent` と `COPILOT_AGENT=1` が付く（人が開いた端末には付かない） | ソースで確認: [microsoft/vscode の toolTerminalCreator.ts](https://github.com/microsoft/vscode/blob/main/src/vs/workbench/contrib/terminalContrib/chatAgentTools/browser/toolTerminalCreator.ts)・aiAgentEnv.ts |
| VS Code の MCP の `tools/call` の `_meta` に `vscode.conversationId`（hook の `session_id` と同じ値）が付く | ソース: [mcpServer.ts](https://github.com/microsoft/vscode/blob/main/src/vs/workbench/contrib/mcp/common/mcpServer.ts)。サーバはまだ使っていない |
| hook の入力（stdin の JSON）: Copilot CLI の camelCase の形式は `sessionId`、PascalCase（VS Code 互換）の形式と VS Code は `session_id` | 文書: [Copilot の hooks configuration](https://docs.github.com/en/copilot/reference/hooks-configuration)・[VS Code の hooks reference](https://code.visualstudio.com/docs/agents/reference/hooks-reference) |

**判断（利用者・2026-09-18。最初の判断「人の操作として記録する」を変えた）**: CLI（`cli_agent_session`）は次のとおり送ります。

| 環境 | `X-Looptrack-Session` | `X-Looptrack-Agent` | 記録 |
| -- | -- | -- | -- |
| Copilot CLI（`COPILOT_CLI=1` かつ `COPILOT_AGENT_SESSION_ID`） | `COPILOT_AGENT_SESSION_ID` | `copilot` | AI の操作（セッション ID あり・`detail.agent = copilot`） |
| VS Code のエージェント用ターミナル（`AI_AGENT=github_copilot_vscode_agent` または `COPILOT_AGENT=1`） | なし | `copilot` | **セッション ID なしの AI の操作**（`detail.agent = copilot`） |
| 上のどちらかの下で `LOOPTRACK_SESSION_ID` を明示 | `LOOPTRACK_SESSION_ID` | `copilot` | AI の操作 |
| Claude Code・Codex の変数がある（Copilot の端末から Claude Code を起動した等） | その変数 | なし | 従来どおり（Claude Code・Codex の順を優先） |
| `TERM_PROGRAM=vscode` だけ（人が VS Code の端末で打った）・`COPILOT_CLI=1` だけ（ID を渡さない旧い版）・`COPILOT_AGENT_SESSION_ID` だけ | なし | なし | 人の操作 |

- `COPILOT_AGENT_SESSION_ID` は `COPILOT_CLI=1` と組み合わせたときだけ読みます。別のツールが同名の変数を使っても取り違えません。
- `X-Looptrack-Agent` は既定（Copilot でない）では送りません。反映前のサーバは知らないヘッダを読み捨てます。そのため VS Code の操作は人の操作として記録されるだけで、壊れはしません。
- サーバは `X-Looptrack-Agent` を `Actor.Agent` に入れ、`issue_events.detail` の `agent` に残します（MCP の clientInfo の判定と同じ欄で、列は足しません）。Copilot は計測が任意の AI（`store.UsageOptInAgents`）です。計測を有効にしていない利用者の操作は、付与の指示・クローズ時の必須・未付与の検知の対象にしません。`humans`（人の操作）にも数えません（上の「イベントとの突き合わせ」）。
- テスト: `TestCLISessionIDCopilot`（internal/server/cli_session_test.go）。

Copilot でも**イシューの操作は MCP のツールが主です**（利用者の方針・2026-09-18）。MCP の操作は `clientInfo` から `copilot` と判定します（下の §6「接続してきた AI の判定」）。

既知の限界: Copilot CLI のトークン情報（経路 ①）は `usage_snapshot` の Copilot のアダプタ（OpenTelemetry のファイル出力）が扱います。VS Code の CLI の操作はセッション ID が無いので、会話と結べません。

#### クラウド版の AI

Claude Code on the web・Codex cloud・GitHub Copilot cloud agent（旧称 coding agent）について、2026-09-19 に公式文書で確かめました（実物では未確認）。
利用者向けの設定手順は [docs/CLOUD-AGENTS.md](../CLOUD-AGENTS.md) にあります。
**どれもクラウドの VM・コンテナから出ていく通信なので、届くのはインターネットに公開したサーバだけです**。ローカル利用や `LOOPTRACK_LOCAL_MODE` の 127.0.0.1 のサーバには届きません。

| 項目 | Claude Code on the web | Codex cloud | Copilot cloud agent |
| -- | -- | -- | -- |
| 通信（CLI の HTTPS） | 環境の Network access を **Custom** にして Allowed domains にサーバのドメインを足す（None / Trusted / Custom / Full。既定は Trusted で、足さなければ届かない）。外向きはすべて HTTP/HTTPS のセキュリティプロキシを通る〔A2〕 | エージェントの段階は**既定で遮断**（セットアップスクリプトの段階は通る）。環境ごとに許可リスト（None＝空に足す / Common dependencies / All）と、**HTTP メソッドを GET・HEAD・OPTIONS に絞る設定**がある。絞ると POST / PATCH が止まり、イシューを変えられない（読むだけになる）〔B2〕 | ファイアウォールが既定で有効（推奨の許可リストつき）。リポジトリの Settings > Copilot > Internet access の Custom allowlist（組織は Organization custom allowlist）にドメインか URL を足す〔C1〕 |
| 通信（MCP） | リポジトリの `.mcp.json` のサーバは VM から接続する（許可リストが要る）。claude.ai で有効にした **MCP コネクタは Anthropic のサーバ経由で、許可リストに関係なく届く**〔A2〕 | クラウドの文書に MCP の記載なし（未確認） | ファイアウォールは**エージェントの Bash ツールが起動したプロセスにだけ掛かり、MCP サーバには直接掛からない**〔C1〕 |
| 認証（CLI） | 環境の Environment variables（`.env` 形式）に `LOOPTRACK_TOKEN`。**その環境を使う人は誰でも値を読める**（文書が「秘密を置かない」と注意）。Pro / Max は API credentials（Bearer。プロキシが指定したホストへの要求に後から付け、VM には入らない）があるが、CLI は自分の `Authorization` を付けるので、置き換わるかは未確認。Team / Enterprise には無い〔A2〕 | Environment variables はセットアップとエージェントの両方の段階で使える。**Secrets はセットアップスクリプトだけで、エージェントの段階の前に消える**。セットアップで書いたファイルは残る（`export` は残らない）〔B1〕→ `LOOPTRACK_TOKEN` を環境変数に置くか、セットアップスクリプトが Secret から `~/.config/looptrack/credentials.json` を書く | Settings > Security > Secrets and variables > **Agents** の secret / variable は、エージェントの環境に環境変数として渡る（値はログで伏せられる）。Actions・Codespaces・Dependabot の secret は渡らない〔C3〕→ `LOOPTRACK_TOKEN` を Agents の secret に |
| 認証（MCP） | コネクタは OAuth（claude.ai の画面で許可）。init が書く `.mcp.json` は OAuth（`Authorization` ヘッダなし）で、VM の中でブラウザの許可を通せるかは未確認。`headers` に `Bearer ${LOOPTRACK_TOKEN}`（環境変数の展開）を書けば PAT で繋がる見込みだが、共有の `.mcp.json` に書くと手元の利用者も `LOOPTRACK_TOKEN` が要るので勧めない（CLI かコネクタを使う） | 未確認 | リポジトリの Settings > Copilot > MCP servers に JSON（`type: "http"`・`url`・`headers`・**`tools` は必須**）。ヘッダの値は `COPILOT_MCP_` で始まる Agents の secret を `$名前` で参照する（`COPILOT_MCP_` の secret はエージェントのシェルには渡らない）。**OAuth のリモート MCP サーバは使えない**（PAT を使う）。ツールは承認なしで使われる〔C2・C3〕 |
| シェルに渡るセッション ID | 文書にあるのは `CLAUDE_CODE_REMOTE=true` と `CLAUDE_CODE_REMOTE_SESSION_ID`（`cse_…`。claude.ai の会話の URL 用）〔A1・A2〕。Bash に `CLAUDE_CODE_SESSION_ID` が渡るかはクラウドの文書に無い（本体が同じなので渡る見込み。未確認） | 文書に無い（`CODEX_THREAD_ID` は codex-rs の core が注入するので渡る見込み。未確認） | `COPILOT_CLI=1`・`COPILOT_AGENT_SESSION_ID`・`COPILOT_AGENT_ACTION=task`・`CI=true`・`GITHUB_ACTIONS=true` など（公式文書には無く、第三者の報告〔C5〕。未確認） |
| リポジトリの hook | `.claude/settings.json` の hook は**リポジトリ 1 つのセッションでだけ**動く（複数リポジトリ・projects のスレッドでは読まない）。利用者の `~/.claude/settings.json` は無い〔A2〕 | プロジェクトの `.codex/hooks.json` は `.codex/` を信頼したときだけ読み、hook の定義ごとに `/hooks` で信頼が要る〔B3〕。クラウドで信頼する手段は文書に無い → **動かない見込み**（未確認） | `.github/hooks/*.json` は cloud agent と Copilot CLI が読む〔C4〕 |
| 会話記録・`usage attach` | VM の中の Claude Code の会話記録を読めるはず（文書に無い。未確認）。VM は放置で回収されるので、後からの `usage attach` はできない〔A1〕 | 文書に無い（未確認） | 会話記録は非公開の形式で読まない。OpenTelemetry のファイル出力（`COPILOT_OTEL_FILE_EXPORTER_PATH`）を Agents の variable で有効にすれば読める見込み（未確認） |
| init の成果物をコミットすれば使えるか | 使える: `CLAUDE.md`・`.claude/settings.json`（hook と `env`）・`.claude/scripts/`・`.mcp.json`・`.claude/skills/` は clone に入る。`settings.local.json`・`~/.claude/` の分は入らない〔A2〕 | `AGENTS.md` は読む。`.codex/hooks.json` は上のとおり動かない見込み | `AGENTS.md`・`.github/hooks/looptrack.json` は使える見込み。`.github/mcp.json`・`.vscode/mcp.json` は cloud agent の文書に無く、MCP は Settings の JSON で設定する〔C2〕 |

出典（どれも 2026-09-19 に確認）:
〔A1〕[Use Claude Code in the cloud](https://code.claude.com/docs/en/claude-code-on-the-web)
〔A2〕[Configure cloud environments](https://code.claude.com/docs/en/cloud-environments)（Network access・Set environment variables・Add API credentials・What carries over from your setup・Link output back to the session・Setup scripts vs. SessionStart hooks）
〔B1〕[Codex: Cloud environments](https://learn.chatgpt.com/docs/environments/cloud-environment)
〔B2〕[Codex: Agent internet access](https://learn.chatgpt.com/docs/cloud/internet-access)
〔B3〕[Codex: Hooks](https://learn.chatgpt.com/docs/hooks)
〔C1〕[Customizing or disabling the firewall for Copilot cloud agent](https://docs.github.com/en/copilot/how-tos/use-copilot-agents/coding-agent/customize-the-agent-firewall)
〔C2〕[Extending Copilot cloud agent with MCP](https://docs.github.com/en/copilot/how-tos/use-copilot-agents/cloud-agent/extend-cloud-agent-with-mcp)
〔C3〕[Configure secrets and variables for Copilot cloud agent](https://docs.github.com/en/copilot/how-tos/copilot-on-github/customize-copilot/customize-cloud-agent/configure-secrets-and-variables)
〔C4〕[About hooks（Copilot）](https://docs.github.com/en/copilot/concepts/agents/coding-agent/about-hooks)
〔C5〕[watson/is-ci#28](https://github.com/watson/is-ci/issues/28)（2026-09-15。cloud agent の環境変数の一覧。第三者の報告）

**判断（コードの変更は無し。テストだけ足した）**

| 論点 | 判断と理由 |
| -- | -- |
| セッション ID の環境変数名の追加 | 足さない。Copilot cloud agent は Copilot CLI と同じ `COPILOT_CLI=1` と `COPILOT_AGENT_SESSION_ID` を持つので、今の判定（§9-5「Copilot のセッション ID」）で `X-Looptrack-Session` と `X-Looptrack-Agent: copilot` が付く。Claude Code on the web は `CLAUDE_CODE_SESSION_ID`（本体が Bash に渡す）で今と同じ。**`CLAUDE_CODE_REMOTE_SESSION_ID` は読まない**: 会話記録のファイル名（`usage_snapshot.detect` が引くセッション ID）と一致しないので、これで AI の操作にすると、トークンを付けられないのに付与の対象になり、`usage.require_on_close` のプロジェクトで Done が拒否される。`CLAUDE_CODE_SESSION_ID` が渡らないと実測で分かったら、そのとき読み方を決める（**デスクトップ版の器の窓ではそれが実測で分かったので、`CLAUDE_CODE_HOST_SESSION_ID` は載せたうえで付与の対象から外す形にした**。上の「器のセッション ID」に両者の関係を書いた） |
| クラウドでトークンを読めない AI を未付与の判定から外す | 外さない。Copilot は計測が任意の AI（`store.UsageOptInAgents`）で、cloud agent の操作も `detail.agent = copilot` になるので、計測を有効にしていない利用者の分は既に対象外。Claude Code on the web は hook が動き、VM の中の会話記録を読めるはずなので、ローカルと同じく対象にする（サーバを許可リストに足せば hook が送る）。Codex cloud は、会話記録がクラウドの中に残るかが未確認で、残らないと分かってもシェルの環境変数でクラウドを見分けられるかも未確認。判定を足すのは実測の結果を見てから |
| CLI がクラウドのプロキシを通れるか | 通れる。looptrack は `http.ProxyFromEnvironment` で `HTTPS_PROXY` 等に従い、プロキシの CA は `SSL_CERT_FILE` / `SSL_CERT_DIR` で足せる |
| init の案内 | 変えない。クラウド版は「init の成果物をコミットしておく」だけで足り、クラウド固有の配線は要らない（上の表の最終行）。手順は docs/CLOUD-AGENTS.md に書く |

テスト: `internal/client/session/session_test.go`（Copilot cloud agent・Claude Code on the web・`CLAUDE_CODE_REMOTE_SESSION_ID` だけ）と、golden `internal/clitest/testdata/golden/headers/copilot-cloud-agent.golden`・`claude-code-web.golden` です。

既知の限界:
- **PAT はプロジェクト単位に絞れません**。`api_tokens.scopes` は未使用で、PAT はその利用者の全プロジェクトの権限で動きます。クラウドに置く PAT は、そのプロジェクトだけに参加させた専用の利用者で、最短の有効日数（30 日）で発行してください（docs/CLOUD-AGENTS.md）。
- Copilot の計測を手元で有効にしている利用者（直近 7 日に `client = copilot` のスナップショットがある）の cloud agent の操作は、付与の対象になります。cloud agent で OpenTelemetry と hook を有効にしないと未付与に数えられます。`usage.require_on_close` のプロジェクトでは Done に `override_reason` が要ります（VS Code と CLI の片方だけ有効にしたときと同じ限界です）。
- Claude Code on the web で MCP コネクタだけを使い、サーバを許可リストに足さないと、hook も CLI も届きません。MCP の操作にはトークン情報が付かず（§9-5「イベントとの突き合わせ」の claude.ai のコネクタと同じ）、SessionStart の hook も失敗します。

#### API

| メソッド・パス | 用途 |
| -- | -- |
| `POST /projects/{slug}/usage` | スナップショットを 1 件受け取る（editor 以上・閲覧のみは 403）。応答は `201 {id, duplicate: false}`、再送は `200 {duplicate: true}`。検証に失敗した値は 400、無いイシューは 404 |
| `GET /issues/{id}/usage` | そのイシューに帰属する区間の一覧と合計。`{issue, stages: [{id, at, trigger, op, via, client, session_id, conversation_id, delta, delta_total, cumulative, inconsistent, excluded}], stage_count, total, total_tokens, excluded_total, inconsistent}`。対象外の会話の区間は一覧に出るが `total` に入らない。`stages` は時刻順（同時刻は id 順）。差分は会話ごとに計算してから並べ替える（MCP の `issue_usage`・`usage show` も同じ順） |
| `GET /projects/{slug}/usage/coverage?days=&mine=` | 付与漏れの一覧と充足率（上の「イベントとの突き合わせ」）。`summary` にも件数を載せる |
| `GET /projects/{slug}/usage/report?from=&to=` / `?since_last=1` | レポート用の集計（閲覧者も可）。`?format=md` で表示用の表（CLI・MCP と同じ文）。`&group=label`（案件別）/ `type` / `stage` / `client` でその切り口を `groups` に入れ、md はその表だけにする。下の「レポートの集計と台帳」 |
| `GET /projects/{slug}/usage/report.xlsx`（同じ引数） | 同じ集計の数表（概要・イシュー別・案件別・ラベル別・種類別・段階別・AI 別・会話別の 8 シート。`internal/xlsxreport.BuildTables`） |
| `GET /projects/{slug}/usage/ledger` | 台帳の一覧（データ終端の新しい順）と `next_from`（次の「前回以降」の起点） |
| `POST /projects/{slug}/usage/ledger` | 台帳に 1 行足す（editor 以上）。同じ名前は 409。`request_id` は同じプロジェクトの未完了の依頼だけ（無い依頼は 400・完了済みは 409） |
| `GET /projects/{slug}/usage/requests[?all=1]` | レポートの作成依頼の一覧（新しい順。既定は未完了だけ。閲覧者も可）。各行に次に打つコマンド（`command`） |
| `POST /projects/{slug}/usage/requests` | 依頼を登録（editor 以上。画面のフォームと同じ検証）。`{since_last | from, to?, target?, note?}` |

MCP: スナップショットを**書く**ツールはありません（MCP のツールはトークンを知り得ないため）。MCP にあるのは次のツールです。

- 読む側: `issue_usage`（`GET /issues/{id}/usage` と同じ内容）
- レポート: `usage_report`（集計。`request_id` で依頼の期間）・`list_usage_ledger`・`add_usage_ledger`（台帳の登録。トークンではなくレポートの記録なので MCP からも書けます）・
  `list_usage_requests`（作成依頼）
- 付与漏れ: `usage_missing`

`project_summary` にも未付与の件数と未完了の依頼を出します。

#### レポートの集計と台帳

**期間の規則**

| 項目 | 決定 |
| -- | -- |
| 区間を期間に入れる基準 | その区間を**閉じたスナップショットの `at`**（会話記録の最後の時刻）が `[from, to)` に入ること。`received_at` は使わない（スプールからの再送でも実際の時刻で入る） |
| 区間の差分 | 上の「差分と帰属」をそのまま使う。期間の前のスナップショットとの差も取る（会話の途中から期間が始まっても、期間内の消費だけ数える）。そのため期間に 1 件でもスナップショットがある会話は、会話の全スナップショットを読む |
| 時刻の指定 | RFC 3339、または `YYYY-MM-DD[ HH:MM[:SS]]`（プロジェクトの現地時刻 `service.Service.Loc`。既定 Asia/Tokyo）。**日付だけの `to` はその日を含む**（翌日 0:00 まで）。`to` の既定は今 |
| 時間帯の名乗り | 時刻はプロジェクトの現地時刻で描き、注記（対象期間・データ終端・会話の時刻・台帳・時刻の指定の誤り）は**実際に描いた時間帯を名乗る**。Asia/Tokyo のときは「日本時間」/ project local time、ほかは IANA 名。集計 JSON と台帳の一覧は `timezone`（IANA 名）を返し、PDF（`internal/client/report`）と CLI の台帳はそれで描く（無い古い応答は Asia/Tokyo とみなす） |
| 前回以降（`since_last`） | `from` = 台帳で**データ終端が最も新しい行**の `data_end`（登録順ではない。過去分を後から取り込んでも起点が戻らない）。台帳が空なら下限なし。`from` との併用は 400。登録直後で空の期間なら 0 件を返す |
| データ終端 | `data_end` = `to` と今の早い方。レポートの応答に入れ、それを台帳に登録する。次の「前回以降」は `[data_end, …)` なので、境目の区間は二重にも欠けにもならない |
| 取りこぼし | `at` が前回のデータ終端より前で、レポートの後に届いたスナップショット（長く送れなかったスプール）は、どちらのレポートにも入らない。件数が問題になったら期間指定で出し直す |
| 対象外 | `excluded` の会話の区間は合計・内訳に入れず、`excluded_total` / `excluded_tokens` / `excluded_conversations`（会話 ID）に別に返す |
| 未帰属 | 帰属先の無い区間（クローズ後の stop など）。`unattributed` に合計、会話別の `unattributed_tokens` にも出す |

**返す切り口**（応答の JSON。`internal/usage.BuildReport`）: `total`（4 種 × 本体・サブ・応答数）・`total_tokens`・`stage_count`・`unattributed`・
`excluded_*`・`inconsistent`・`first_at` / `last_at`・`by_issue`（ID・タイトル・種類・状態・ラベル）・`by_label`（**複数ラベルのイシューはそれぞれに数えます**。
ラベルなしは `key: ""`。合計は total と一致しません）・`by_type`・`by_stage`（段階＝区間を閉じた操作: `create` / `update` / `comment` / `status` / `stop` / `session_end` / `manual` / `import`）・
`by_client`（AI 別）・`by_case`（案件別。下）・`case_pattern`・`conversations`（会話 ID・AI・セッション ID・最初 / 最後・未帰属・触れたイシュー）です。イシューの種類・ラベルは**レポートを作った時点の値**を使います。
金額換算はしません（範囲外）。

**案件ラベル別（`by_case`・`group=label`）**: イシューの `labels` に案件（ブランチ名 `CASE-nnn` の形）を入れて運用するプロジェクト向けです。
イシューに帰属していない消費でも、ほとんどの区間にはブランチ名が付いています。そこでブランチ名を案件ラベルとみなし、同じ軸で集計できるようにします。

| 項目 | 決定 |
| -- | -- |
| 正規表現 | プロジェクト別ルール `usage.case_pattern`（`deploy/rules/<slug>.json`・`looptrack project rules set`）。保存時に検査し、不正な式と空文字列に一致する式は拒否。捕捉グループがあれば 1 番目、無ければ一致した部分を案件名にする。**コード（サーバ・CLI）にプロジェクト固有の分岐は入れない** |
| 決め方 | 区間ごとに 1 つ: ① 帰属先イシューのラベルを並び順に調べ、最初に当たったもの → ② 区間を閉じたスナップショットの `branch`（フックが送る `gitBranch`、取り込みは CSV の「ブランチ」）→ ③ どちらも当たらなければ「案件なし」（`key: ""`）。イシューがあればそちらを優先（ブランチで作業していても、イシューのラベルが正） |
| 合計 | 1 区間は 1 案件にだけ数えるので、`by_case` の和は `total_tokens` と一致する（`by_label` は複数ラベルをそれぞれに数え、一致しない）。対象外の会話は入れない |
| 未設定 | `case_pattern` の無いプロジェクトは全部「案件なし」1 行（`case_pattern: ""`） |
| 表示 | md・xlsx（「案件別」シート）・skill `token-report` の PDF（`{"auto": "by_case"}`）に表を出し、正規表現を注記する。閲覧画面に集計の表示は無い（画面は作成依頼の登録だけ。§「レポートの作成依頼」） |
| 保存 | 案件は保存しない（問い合わせ時に計算）。正規表現を直せば過去分も直した規則で出る |

**テーブル `usage_reports`（マイグレーション 0004・追記のみ）**

| 列 | 内容 |
| -- | -- |
| `name`（200・プロジェクト内で一意） | レポート名。同じ名前は 409（過去分の取り込みを流し直しても増えない） |
| `period_from`（NULL 可）・`period_to` | 対象期間 `[from, to)`（UTC）。NULL は最初のデータから |
| `data_end` | データ終端。`period_from` 〜 `period_to` の中。今より 24 時間以上先は 400 |
| `excluded_conversations`（JSON） | 除外した会話 ID |
| `total_tokens`（NULL 可） | レポートに載せた合計の控え |
| `note` | メモ（手元の保存先など。PDF・本文はサーバに置かない） |
| `request_id`（NULL 可・一意） | 画面からの作成依頼（`usage_report_requests.id`。外部キーと一意制約は 0006）。この行があることが依頼の完了（1 依頼に台帳 1 行まで） |
| `created_by`・`token_id`・`via` | 登録した利用者と経路（`cli` / `api` / `web` / `mcp`） |
| `created_at` | レポートを作った日時。**過去の日時を受け付ける**（過去の台帳の取り込み）。未来は 400 |
| `recorded_at` | 実際に登録した日時（サーバが入れる） |

アプリ用 DB ユーザーには SELECT・INSERT だけを与えます（`deploy/grants.sql`）。誤登録は取り消せません。同名が使えない点も含めて、CLI・MCP の説明に「取り消せない」と書きます。

**CLI**: `looptrack issue usage report (--from D [--to D] | --since-last) [--json | --xlsx PATH]`、
`looptrack issue usage ledger list [--json]`、`looptrack issue usage ledger add <名前> (--from-report <report --json の出力> | --to D [--from D] [--data-end D]) [--excluded c1,c2] [--total-tokens N] [--note …] [--created-at D] [--request-id N]`。

流れは次のとおりです。

1. `usage report --since-last --json > r.json` で集計します。
2. 本文・PDF を手元で作ります。
3. `usage ledger add "<名前>" --from-report r.json --note <PDF のファイル名>` で登録します。集計したときの期間とデータ終端がそのまま入ります。

この流れは全プロジェクト共通の skill `token-report`（下の「レポートの作成依頼」）にまとめてあります。

#### レポートの作成依頼

画面のボタンからレポートを「作る」ことはできません。本文を書くのは AI で、PDF は手元に保存するからです。ボタンは**依頼を登録するだけ**です。
次にコーディング AI のセッションを始めると、要約（SessionStart の `looptrack hook summary`・MCP の `project_summary`）に未完了の依頼が出ます。それを見た AI が skill で作ります。常駐ランナーは作りません。

| 項目 | 決定 |
| -- | -- |
| 登録 | 画面 `/looptrack/p/<slug>/report-requests`（ボードの「レポート作成」）・`POST …/usage/requests`。editor 以上。期間は「前回以降」か「開始日〜終了日（終了日を含む・空欄は作成するときまで）」。対象（1 行・500 文字）とメモ（2000 文字）は AI への指示として渡すだけで、サーバは対象で絞らない |
| 期間の解決 | **作成するときに決める**。前回以降は作成時点の台帳の最新のデータ終端から。終わりの指定が無ければ作成時点の今まで。`GET …/usage/report?request=N`（CLI `usage report --request N`・MCP `usage_report` の `request_id`）が依頼の期間で集計し、応答の `request` に依頼を入れる。他の期間指定との併用は 400 |
| 完了 | 依頼の行は**書き換えない**（追記のみ）。台帳に `request_id` つきの行があれば完了。`ledger add --from-report` は集計 JSON の `request.id` を自動で付ける。完了した依頼への 2 回目の登録は 409（同時の登録は `usage_reports.request_id` の一意制約で止まる） |
| 要約 | `GET …/summary` の `usage_requests`（未完了・新しい順）。CLI の `summary` は依頼があるときだけ末尾に「── トークンレポートの作成依頼（未完了 N 件） ──」と、各依頼の期間・対象・メモ、次のコマンド（最も古い依頼）を出す（無ければ従来と同じ出力） |
| 上限 | 未完了の依頼は 20 件まで（押し間違いの連打で要約が埋まらないように。21 件目は 409） |
| 取り消し | できない（追記のみ）。不要な依頼は、依頼の番号を付けて台帳に登録する以外に消す方法が無い。必要になったら取り下げの行を足す設計を別に起票する |

**テーブル `usage_report_requests`（マイグレーション 0006・追記のみ）**: `since_last`・`period_from`（前回以降のときは NULL）・`period_to`（NULL は作成するときまで）・
`target`・`note`・`requested_by`・`token_id`・`via`（`web` / `api`）・`created_at` を持ちます。アプリ用 DB ユーザーには SELECT・INSERT だけを与えます（`deploy/grants.sql`）。
同じマイグレーションで `usage_reports.request_id` に外部キーと一意制約を足します。

**skill `token-report`（`kit/core/skills/token-report/`）**: 全プロジェクト共通の skill です。中身は `SKILL.md`（手順・本文 JSON の形・執筆ルール・チェックリスト）です。
PDF は `looptrack report pdf` で作ります（集計 JSON + 本文 JSON → PDF）。数表は集計 JSON から機械的に作るので、AI は本文だけを書きます。
各プロジェクトへは、init が `.claude/skills/token-report/SKILL.md` を kit から実体で置きます。以前はリポジトリの `skills/token-report` への
ディレクトリの symlink で置いていましたが、init を再実行すると実体に置き換わります。
PDF の保存先は既定で `~/Documents/トークンレポート/<slug>/`（`TOKEN_REPORT_DIR`）で、リポジトリの外です。導入前に使っていた稼働レポートの章立てと執筆ルールを引き継いでいます。プロジェクト固有の内容（CSV・チェックリスト・案件名）は持ちません。

#### 送信の経路

**① CLI 内蔵（全 AI 共通・主経路）**: CLI は変更操作が成功した直後にスナップショットを集め、取れたら `POST …/usage` します。
セッションはまず環境変数で特定します（Claude Code は `CLAUDE_CODE_SESSION_ID`、Codex は `CODEX_THREAD_ID` → `CODEX_SESSION_ID` でファイル名を引きます）。
無ければ、作業ディレクトリが一致する直近 10 分以内の会話記録（Codex の `session_meta.cwd`）を使います。
どの AI の下でもない（人がターミナルから打った）ときは何も送りません。
送信は 2 秒で打ち切ります（`LOOPTRACK_USAGE_TIMEOUT`）。失敗しても操作の結果と終了コードは変えません（`LOOPTRACK_USAGE_DEBUG=1` で理由を標準エラーに出します）。
`LOOPTRACK_USAGE=0` で止められます。手動の付与は `looptrack issue usage attach <ID>`（trigger `manual` + issue）で、確認は `usage show <ID>` です。

**② フック（`looptrack hook` が行う）**

| AI | 起動 | 動作 |
| -- | -- | -- |
| Claude Code | PostToolUse（matcher `mcp__.*`） | **MCP でイシューを変更したときだけ**送る（Bash の CLI は ① が送るので見ない）。ツール名の末尾が `create_issue` / `update_issue` / `add_comment` / `set_status` で、サーバ名が `LOOPTRACK_MCP_SERVER`（既定 `looptrack`）に合うもの。起票の ID は応答の `id` から取る。`isError` の操作は送らない |
| Claude Code | Stop | 前回の送信から 10 分未満なら送らない（`LOOPTRACK_USAGE_THROTTLE_MIN`）。それ以外は `stop` で送る |
| Claude Code | SessionEnd | 常に `session_end` で送る（区間の一覧つき） |
| Codex | PostToolUse / Stop / SessionEnd | Claude Code と同じ（MCP のツール名の形だけ違う）。配線は `.codex/hooks.json` |
| Gemini CLI | AfterTool / AfterAgent / SessionEnd | 同上。トークンの記録を読めることを確かめてから足す |

**この表の 4 つは、上の「MCP の変更系のツール」の 7 つより狭くなっています**。`assign_issue`・`next`・`report_verify` は `issue_events` を書くのにフックが拾わないので、トークン情報が送られません。狭いのは今そうなっているだけで、意図したものではありません。
同じ「イシューを変える MCP のツール」の一覧は、`internal/client/hook/loop/pretool.go` の `imWriteTools`（8 つ。`verify_issue`・`add_usage_ledger` を含む）にもあります。
**4 か所（サーバの登録・この表・フックの正規表現・`imWriteTools`）のずれは `internal/docscheck/mcptoolsets_test.go` が突き合わせます**。どれか 1 本だけを動かすとテストが失敗します。

共通: **送信は子プロセスに切り離し、フック自体はすぐ 0 で終わります**。操作を遅らせず、失敗しても操作を妨げません。
送れなかった分は looptrack の置き場（資格情報と同じ場所）に置き、次の起動でまとめて送ります。置き場は `~/.config/looptrack/usage-spool/`、Windows は `%APPDATA%\looptrack\usage-spool\` で、7 日で捨てます。認証は CLI と同じ資格情報を使います。
人間の発話に「本セッションは…レポート対象外」があれば `excluded: true` を付けて送ります。サーバは会話 ID 単位で集計から外します（行は残します）。

**③ 応答での指示（全 AI 共通・回収）**: AI からの変更操作（上の対象）の応答に `usage_notice`
`トークン情報が未付与です。次を実行してください: looptrack issue usage attach <ID>` を付けます。
応答の時点では、その操作自体のスナップショットはまだありません（CLI は応答の後に ① で送り、MCP はフックが後から送ります）。そのためこの指示は「この後に付かなかったら」実行するものです。

| 経路 | 指示の扱い |
| -- | -- |
| CLI | 応答の JSON に載るが、**CLI は自分の付与（①）が送信に失敗したときだけ標準エラーに出す**。付いたときは出さない。会話記録が無い（送るものが無い）ときも出さない（`usage attach` でも付けられないため） |
| MCP | ツール結果の文の末尾に足す。ただし**その利用者のフック（`via = mcp` のスナップショット）が直近 7 日に届いていれば出さない**（数秒後に ② が付けるので、毎回 `usage attach` を実行させて二重に送らせない）。フックが止まっていれば summary の未付与で回収する |
| クローズ | Done / Canceled にしたとき、その会話のトークン情報がイシューにまだ 1 件も無ければ、既定（警告のみ）の警告として `警告: <ID> をクローズしましたが、この会話のトークン情報が <ID> にまだありません。次を実行してください: …` を返す（フックの有無に関わらず） |

`usage attach` は ① と同じ収集を手動で 1 回だけするコマンドです（trigger `manual` + issue）。

**クローズ時の必須化**はフックではなく、サーバのプロジェクト別ルール（`usage.require_on_close`）で行います（§9-1。どの経路でも同じ判定）。
状態を Done / Canceled にする時点では、PostToolUse がまだ走っていません。そこで判定は「**その会話のスナップショットがそのイシューに 1 件以上ある**」とします。起票・着手・コメントのどれかで付いていれば通ります。
無ければ 422（`rule: usage_required_on_close`・上書き可）を返します。メッセージには `looptrack issue usage attach <ID>`（手動で 1 件送るコマンド）と `--override "理由"` を出します。

| 項目 | 決定 | 理由 |
| -- | -- | -- |
| 「その会話」 | CLI は `X-Looptrack-Session` のセッション ID。そのセッションのスナップショットと同じ `conversation_id` のものも数える（再開でセッション ID が変わっても前の分が効く）。MCP は**その利用者のスナップショット**（会話を問わない）で判定する（`Mcp-Session-Id` を `session_id` に記録するようになっても、接続 ID は会話と結び付かないので判定には使わない。`service.Actor.usageSession`） | MCP の要求にはセッションを結ぶ情報が無い |
| 対象 | AI からの操作（MCP・セッション ID 付きの CLI）だけ。人がターミナルから閉じる操作・web・API 直接は判定しない | 人の操作には付けるトークン情報が無く、止めても付けようがない |
| CLI の順序 | **拒否されたときだけ**、先に `usage attach`（manual）で付けてから同じ状態変更を 1 回だけやり直す。付けられなければ（会話記録が無い等）拒否のメッセージのまま exit 1 | サーバの判定は操作の前、① の付与は操作の後なので、この会話で最初の操作がクローズだと必ず拒否される。常にクローズの前にも送る方式は、クローズのたびに会話記録を 2 回読み、行も増える。拒否された操作は何も変えていないのでやり直しは安全 |
| 既定 | ルールが無い・`require_on_close: false` は警告だけ（上の ③ のクローズの警告） | 利用者の決定（Q2: 通常は警告のみ、クローズ時だけ必須をプロジェクト別に） |
| 上書き | `--override "理由"`（API は `override_reason`）。`rule_override` イベントに rule `usage_required_on_close` と理由を残す | `verify_required_on_close` と同じ扱い |

### 9-6. 決まった語の英語の別名と、メッセージの言語（日英 2 言語）

英語で使う利用者のために、本システムが**意味を読み取る**日本語の決まった語には英語の別名を認めます。
あわせて v1.0.0 から、**利用者が読むメッセージを日本語と英語の 2 言語で出します**。
2 言語化の仕組みは「メッセージの 2 言語化」の項にまとめています。

#### 決めていること

| 項目 | 内容 |
| -- | -- |
| 別名の効き方 | **設定なしで常に**日本語と同じ扱い（プロジェクト・利用者ごとの切り替えは作らない）。1 つのイシューで日本語と英語が混ざってもよい。既存のデータは変えない |
| メッセージの言語 | **日本語と英語の 2 つ**（3 言語目を増やす作りにはしない）。利用者が読むものは 2 言語で出す。**AI しか読まないものも、案内の本文（MCP の `instructions` 本体・`guide` の本文・`kit/` に配る rules と skill の本文）は 2 言語化する**（v1.0.0 の公開前。下の「kit の rules と skill」）。**MCP のツールと入力項目の説明（`Tool.Description` と `jsonschema` タグ）も、接続ごとに利用者の言語で出す**。詳細は下の「メッセージの 2 言語化」 |
| 対象外 | 状態名（Todo / In Progress / In Review / Done / Canceled）・型（task / bug …）・ルールのキーは元から英語なので変えない。本文のその他の節（背景・内容・コメント）の見出しは意味を読まないので対象外 |

#### 別名の一覧

| 日本語（今の形） | 英語の別名 | 判定の規則 |
| -- | -- | -- |
| `## 受け入れ条件` | `## Acceptance criteria` | 見出しの語は英字の大小を問わない（`Acceptance Criteria` も可。大小の比較は ASCII の英字だけで、`ſ`・ケルビン記号を英字と読まない）。語の間の空白は 1 つ以上でよい。前後の空白の規則・コードブロック内を見出しとしないこと・次の `## ` までを範囲とすることは日本語と同じ（§9-3-1）。両方の見出しがあれば最初のものだけ |
| `## 検証コマンド` | `## Verify commands` | 同上 |
| `判断:` | `Decision:` | コメントの**先頭**（§9-3-6 と同じ。先頭の空白・改行は許さない）。英語は半角コロンだけ。英字の大小を問わない |
| `差し戻し:` | `Changes requested:` | 同上（GitHub のレビューの語に合わせる） |
| `フィードバック:` | `Feedback:` | 同上。未応答のフィードバックの問い合わせ（`internal/store/feedback.go` の `LIKE`）も英語の先頭語を含める。応答の判定（先頭語の無いコメント）も同じ |
| 確認モードの語（`確認して`・`調査して` … / `実装して`・`修正して` …） | 確認: check・investigate・look into・review・audit・analyze・explain・propose・plan / 実行: implement・fix・apply・create・write・add・remove・update・commit・merge・deploy・release・go ahead | 英語は単語の境界で、大小を問わずに判定する（`fixture` を `fix` と読まない）。日本語の語で決まればそれを使い、英語の語は日本語で決まらないときだけ見る（「deploy.sh を確認して」を英単語 deploy で実行モードにしない）。英語だけの依頼の中では、今の規則どおり実行系が勝つ |

#### 置き場所と実装の注意

- 見出し: 見出しの正規表現は `internal/domain/heading.go` の 1 か所にまとめ、そこに別名を持たせます。
  受け入れ条件の取り出しも、検証コマンドと同じくコードブロックを見ます。コードブロックの中の `## 受け入れ条件` を見出しと読まず、節の中のコードブロックの `## ` で節を終えません。
  `require_on_close`（§9-3-4）・`next`・`verify_issue` はこの関数を使うので、一緒に効きます。
- 先頭語: `internal/domain/leadword.go` の表に英語を足し、`internal/domain/testdata/leadword.json` に例を足します。
  SQL は英語の `Feedback:` を「先頭 9 文字の小文字化」と「大文字化」の両方で比べます。MySQL の小文字化はケルビン記号を `k` にし、SQLite は ASCII だけを変えるので、片方だけだと結果がずれるからです。MySQL と SQLite の両方のテストで確かめます。
- 確認モード: `internal/client/hook/loop/taskmode.go` の正規表現です。
- サーバ側の判定（`require_on_close`・next・フィードバック）は経路を問わず効きます。
- 雛形（`acceptanceTemplate`）はメッセージの扱いに合わせて、**起票した利用者の言語**で入れます（英語なら `## Acceptance criteria`）。
  英語の別名は常に認めるので、日本語の雛形と英語の雛形が同じプロジェクトに混ざっても、受け入れ条件は同じように取り出せます。

#### メッセージの 2 言語化

利用者が読む文面は `internal/i18n` の対訳表から取り出します。**言語は要求ごとに決まります**。
そのためグローバル変数には持たず、呼び出しの経路（CLI の環境・HTTP の要求）から `lang` を引き回します。

**言語の決め方（優先順）**

| 順 | CLI・hook・デスクトップ版（`i18n.FromEnv`） | サーバ（Web・REST・MCP。`langFor`） |
| -- | -- | -- |
| 1 | 環境変数 `LOOPTRACK_LANG`（`ja` / `en`） | 問い合わせの `?lang=`（その要求だけ。画面を両方の言語で見るため。HTTP だけ） |
| 2 | `LC_ALL` → `LC_MESSAGES` → `LANG` | `X-Looptrack-Lang`（利用者が明示した指定。CLI が送る） |
| 3 | 上のどれも日本語を示さなければ **英語**（既定） | **利用者の設定**（`users.lang`。`/account` で選ぶ。NULL は設定なし） |
| 4 | — | `Accept-Language`（`q` を比べ、同値なら先に現れたほう） |
| 5 | — | 上のどれも日本語を示さなければ **英語**（既定） |

**優先順を決める処理はサーバに 1 つだけ置きます**（`internal/server/lang.go` の `langFor`）。
`reqLang`（HTTP）・`mcpLang`（MCP のツール）・`setupMCPLang`（setup と prompt）は、材料を渡して呼ぶだけにします。
経路ごとに書き分けると、同じサーバなのに経路によって効いたり効かなかったりするからです。

**MCP ではヘッダに頼らず、サーバ側の状態（利用者の設定）から決めます。** MCP の接続設定はヘッダを持てないことが多く、
実際に `Accept-Language` を送ってくるクライアントはほとんどありません。
要求にヘッダが 1 つも無くても、`users.lang` があればその言語で返します（設定が無ければ英語）。
利用者が `/account` で選び直せば、**接続設定を貼り直さなくても**次の呼び出しから変わります。

**利用者ごとの設定（`users.lang`）**

- 置き場は `users` の列 `lang` です（`migrations/0003_users_lang.sql`）。認証のたびに引いている行に相乗りするので、
  要求あたりの追加の問い合わせはありません。**NULL は「設定なし」**で、`Accept-Language` の段へ進みます。
  空文字は保存しません。「設定なし」の表し方を NULL の 1 つに絞るためで、`store.SetUserLang` が空文字を NULL に直します。
- **値の制限（`ja` / `en` / NULL）は `store.SetUserLang` が持ちます。** MySQL には `CHECK (lang IN ('ja','en'))` が
  ありますが、**SQLite は `ALTER TABLE ADD COLUMN` に `CHECK` を付けられません**。そのため共通のドメイン操作の側が
  最後の砦になります（書き込み経路が増えても効きます）。画面の `/account/lang` にも `i18n.Parse` があります。
  ただしそちらは入力を正規形に直して 400 を返すためのもので、制限そのものではありません。
- 画面は `/account` の「表示の言語」です（選択肢は **設定なし / 日本語 / English**）。設定なしにも戻せます。
- **`LOOPTRACK_LANG` はこの設定より強く効きます。** CLI は `LOOPTRACK_LANG` が**実際に設定されているときだけ**
  `X-Looptrack-Lang` を送ります。`Accept-Language` は環境変数が 1 つも無くても `en` を送ります（`i18n.FromEnv` の既定）。
  そのためそちらでは「利用者が明示したのか、既定で英語になっただけか」を見分けられません。
- ログインを経ない要求（ログイン画面・認証の失敗）は利用者が分からないので、従来どおり `Accept-Language` で決まります。
- **CLI が手元で組み立てる文面（枠・表・案内・エラーの接頭辞）も、この設定に従います**（下の「応答の `Content-Language`」）。
- hook・PDF のレポート・デスクトップ版の文面はクライアント側で作るので、サーバの設定は効きません（`i18n.FromEnv` のまま）。
  CLI でも、要求を 1 本も出さずに終わる出力は `i18n.FromEnv` のままです。コマンド木・`--help`・引数の誤り・`issue init`・サーバの設定が無い案内がこれに当たります。
  宣言を受け取る応答がそもそも無いからです。

**応答の `Content-Language`**

サーバは `langFor` で決めた言語を、応答のヘッダ `Content-Language` で宣言します。
**クライアントはこれを、自分の手元で組み立てる文面の言語に使います**。サーバが返した本文の言語と、CLI が付ける枠・案内の言語が割れないようにするためです。

- **この宣言はクライアントが送った値の反響ではありません。** 優先順の 3 段目（`users.lang`）は**クライアントが知らない情報**です。
  `LOOPTRACK_LANG` を持たない利用者が `Accept-Language: en` を送っても、設定が `ja` なら `ja` が返ります。
  この割れ（サーバの文面は日本語・手元の文面は英語）を直すのがこのヘッダの目的です。
- **付ける場所は `withPrincipal` を通した後の 4 か所です**（`internal/server/authn.go` の `api` と `web`、
  `internal/server/localmode.go` の `localAPI` と `localWeb`）。**規則は 1 か所**（`setContentLanguage`）に置き、
  呼び出しだけを 4 か所に置きます。`ServeHTTP` のような認証より前の層で付けると、principal がまだありません。
  すると `users.lang` の段を飛ばし、送られてきた値を返すだけになります（直したい割れがそのまま残ります）。
- **REST だけでなく画面にも付けます。** 画面は実際に `reqLang(r)` の言語で描かれるので、正しい宣言になります。
  経路によって効いたり効かなかったりする状態も作りません。`/static/` はこの 4 か所を通らないので付きません。
  言語で変わるヘッダがキャッシュ可能な応答に付かないので、`Vary: Accept-Language` も要りません。
- **`/mcp` には付けません。** MCP のハンドラはこの 4 か所を通らず、言語は `mcpLang` / `setupMCPLang` が決めます。
  MCP のクライアント（AI）は手元で文面を組み立てないので、宣言を受け取る相手がいません。
- 認証を通らない応答（401 / 403・ログイン画面）は利用者が分からないので、`X-Looptrack-Lang` / `Accept-Language` の段で決まります。
- **クライアント側**: `internal/client/api` の `Client.OnLang` が、応答の `Content-Language` を `i18n.Parse` して
  呼び出し側へ渡します。**渡すのは読めたときだけです**。ヘッダを返さない古いサーバでも `i18n.Parse` は英語を返します。
  `ok` を捨てると、日本語の利用者の手元が知らないうちに英語になってしまいます。
  CLI は `useServerLang` で配線します（`api.Client` を作るすべての場所）。**`LOOPTRACK_LANG` が明示されていれば環境が勝ちます**。
  「明示したか」の判定は新しく作らず、`X-Looptrack-Lang` を送るかを決めている判定と同じ形を使います。
- **配る順序の縛りはありません。** 古いサーバはヘッダを返さないので、新しい CLI は環境の言語を使います。古い CLI は新しいサーバの
  ヘッダを読まないだけです。

**置き場と形**

- `internal/i18n/` に `ja.json` と `en.json` を置き、`go:embed` で実行ファイルに含めます。
- ID は `<パッケージ>.<場面>.<内容>` の形です（例 `setupwiz.ask.mode`・`cli.err.not_found`・`server.api.err.not_found`）。
- 置換は名前つきの `{count}` 形式です。語順が言語で変わるので、**`%s` の位置には頼りません**。
- 取り出しは `i18n.T(lang, "id", args)` です。文面を作る場所が相手の言語を知らないときは、`i18n.Errorf` / `i18n.Wrapf` で
  **ID を持つ error** を返します。出す側が `i18n.Text(lang, err)` で文面にします（`Error()` は ID を返すので、ログには ID が出ます）。
  もとの error を包む必要があるときは `Wrapf` を使ってください。`Errorf` は包まないので `errors.Is` が壊れます。

**抜けの検出**（`internal/i18n/lint_test.go` ほか）

- `ja.json` と `en.json` のキー集合が完全に一致することを確かめます（片方だけ足すと失敗します）。
- コード中の `i18n.T` / `M` / `Errorf` / `Wrapf` の ID を `go/ast` で集め、対訳表に無い ID があれば失敗させます。
  **ID は必ず文字列リテラルで渡してください**。変数で渡すと検査が追えず、抜けを見逃します。
- 対訳表にあってコードから参照されない ID も報告します（消し忘れの検出）。
- **Web のテンプレートに書く ID は Go のソースに現れません**。そのため `go/ast` の走査だけでは拾えません。
  これは `internal/i18n/lint_test.go` の `collectTemplateIDs` で**実装済み**です。**リポジトリ全体の `.html`** を歩きます
  （先頭が `.` のディレクトリ・`testdata`・`vendor`・`node_modules` の下は除きます）。`{{T .Lang "id"}}` の形から ID を集め
  （`{{template "head" (headData .Lang (T .Lang "id"))}}` のように括弧の中にあるものも含みます）、
  上の 2 つの検査（対訳表に無い ID・どこからも使われない ID）の対象に足します。
  **テンプレートでも ID は文字列リテラルで書いてください**（組み立てると拾えません）。
  集めた ID が 0 件だと、上の 2 つの検査は**何も見ないまま緑になります**。テンプレートが `{{T` を 1 つも使っていなかった
  間は実際にその状態でした。そこで下限を `TestTemplateScanIsNotEmpty` で押さえています。
  画面の JS が使う ID は **Go 側には現れません**。テンプレートが埋め込むデータブロック
  （`board.html`・`hub.html`・`first_run_done.html` の `<script type="application/json" id="i18n">`）の中に
  `{{T .Lang "id"}}` として書き、JS はそれを読むだけです。したがって拾うのは `go/ast` の走査ではなく、上の `.html` の走査
  （`collectTemplateIDs`）のほうです。`server.web.board.*` は Go に 1 件も無く、`board.html` のこのブロックにだけあります。
  `.js` のファイルそのものは走査しません。**`.html` にも Go 側にも現れない ID を、JS にだけ書かないでください**。

**日本語のラチェット**（2 本立て。どちらも**増えても減っても失敗します**。減少を通すと残高が実態より多いまま残り、
減ったぶんだけ新しい日本語を気づかれずに足せてしまうからです）

| 検査 | 走査の対象 | 数える単位 | 許可一覧 |
| -- | -- | -- | -- |
| `TestJapaneseLiteralsDoNotIncrease`（`internal/i18n/jalint_test.go`） | リポジトリ全体の `.go`（`_test.go` を除く）・`.html`・`.js` | `.go` は日本語を含む**文字列リテラルの件数**、`.html` と `.js` は**コメントを除いた日本語の行数**（`japaneseLines`） | `jalint_allow_test.go` |
| `TestJapaneseMarkdownDoesNotIncrease`（`internal/i18n/jamd_test.go`） | **`//go:embed` を実際にパースして展開した `.md`**（実行ファイルに入って配られるもの） | 日本語を含む**行数** | `jamd_allow_test.go` |

`.md` の側は**対象を列挙しません**。「どの `.md` が利用者に届くか」は `//go:embed` が決めます。
そこでリポジトリの `//go:embed` を読んで展開した結果を対象にします。列挙にすると、埋め込みのディレクトリに
`.md` を足した人が一覧も直さない限り、気づかれずに検査から漏れるからです。テストが失敗したときは、どの
`//go:embed` から何が来たかを失敗の文面に添えます。単位（件数と行数）が違うので、一覧も合計も分けてあります。

**この 2 つが見ないもの**（意図して対象外にしているもの）

- **埋め込まれない `.md`**（`docs/` の日英 2 本立ての文書・`private/`・リポジトリのルートの `README` 類）。
  構成の一致は `internal/docscheck/`（`TestGuideStructure`・`TestKitStructure`）が見るので、行数まで二重には管理しません。
- **`testdata/` の下と、先頭が `.` のディレクトリ**（合成データ・記録と、別の作業ツリーの複製）。
- **`_test.go`**。**公開物として配られます**が、テストの中の日本語は利用者に見せる文面ではないので、どちらのラチェットでも数えません。
  フィクスチャや注釈に実在の利用者名・内部の固有名を書かないことは、`deploy/public-scan.sh` が別に見ます。
- **`.html` と `.js` のコメントの中の日本語**（`{{/* … */}}`・`<!-- … -->`・`//`・`/* … */`）。開発者向けの注釈で
  画面には出ないので、`japaneseLines` がコメントを外してから数えます。
  **画面に出る文面のほうは穴ではありません**。`{{T …}}` の仕組みは入っています（`template.FuncMap` の `"T": i18n.TFunc`）。
  `collectTemplateIDs` は 20 個の `.html` から ID を集めます（実測 396 件。空振りは `TestTemplateScanIsNotEmpty` が止めます）。
  日本語そのものも `TestJapaneseLiteralsDoNotIncrease` が `.go` と一緒に数えるので、**`.html` も `.js` も残りは 0 件です**。
  起票フォームの本文の見出しと「未記入」は、`board.html` が雛形と同じ ID（`domain.template.acceptance_heading`・
  `domain.template.empty`）で画面の文面に入れて JS に渡します。

**対象**

| 対象 | 扱い |
| -- | -- |
| CLI の出力・ヘルプ・エラー、`doctor` / `issue init` の出力、ウィザード、Web の画面、HTTP のエラー、hook の差し戻しと ask の文面、デスクトップ版のトレイと通知、MCP の `setup` が返す導入の手順 | **2 言語化する** |
| `kit/` に配る rules と skill の本文 | **2 言語化する**（下の「kit の rules と skill」。対訳表ではなくファイルで持つ） |
| 書き出す成果物（課題管理表の xlsx・トークンレポートの PDF と xlsx） | **2 言語化する**。言語は**出力を要求した利用者**で決まる（サーバが組み立てるものは `reqLang`、手元で組み立てる PDF は `i18n.FromEnv`）。サーバが返す集計 JSON の**描画済みの文字列**（`period` など）をそのまま埋めない。出す側が機械可読な値（`from` / `to`）から描く（描画済みの文字列を渡すと、出す側の言語に関係なくサーバ側の言語で出る） |
| MCP の `instructions` 本体・`guide` の本文 | **2 言語化する**（2026-09-21 の利用者の決定。当初は「AI しか読まない」として日本語のままにしていた。下の「採らなかった案」）。`instructions` は**言語ごとに `*mcp.Server` を作り、接続（要求）の言語で選ぶ**（`mcp.ServerOptions.Instructions` は `mcp.NewServer` のときに固まり、要求ごとには差し替えられない。SDK の `NewStreamableHTTPHandler` は要求ごとに `getServer` を呼ぶので、そこで選ぶ）。`guide` は `Compose(lang, …)` が組み立て、**長い共通規則は対訳表ではなくファイルで持つ**（`internal/guide/common.md` ↔ `internal/guide/en/common.md`。`kit/` と同じ `en/` の規約。構成の一致は `internal/docscheck` が確かめる） |
| MCP の `Tool.Description` と入力項目の `jsonschema` タグ（ツール 23・入力項目 88 の計 111 か所）と、サーバの表示名（`serverInfo.title`） | **接続ごとに利用者の言語で出す**（2026-09-21 の利用者の決定。当初は日本語のままにしていた。下の「採らなかった案」）。`instructions` と同じく**言語ごとの `*mcp.Server` にその言語で登録**し、要求の言語で選ぶ（サーバは起動時に言語ごとに 1 つだけ作る）。言語の決め方は `instructions` と同じ（利用者の設定 `users.lang` が `Accept-Language` より強い）。ツールの説明は登録のときに `i18n.T` で引く。入力項目の説明は、構造体の `jsonschema` タグに**文面ではなく対訳表の ID**（`server.mcp.arg.…`）を書き、登録の前にスキーマを作って ID を文面に置き換えてから `Tool.InputSchema` に渡す（SDK は `InputSchema` があれば反射で作り直さない。`internal/server/mcp_tooldef.go`）。タグの ID は i18n の検査（`internal/i18n/lint_test.go`）が集めるので、表に無い ID・日本語を直に書いたタグは組み立ての時点で落ちる |
| hook の判定に使う日本語の語（確認モードの語など） | **訳さない**（語そのものが判定に使われる）。表示用の文面と定数を共有している場合は、定数を分ける |

原則は「**人の目に触れうるものは 2 言語化する。AI しか読まないものも、規則と手順を伝える案内の本文は 2 言語化する**。迷ったら 2 言語化する側に倒す」です。
対象外に残るのは、判定に使う語だけです。

**AI 向けの案内は、2 言語化された文面を目印にしない**

案内文（`guide` の共通規則・MCP の `instructions` と prompts・`init` が置く案内節・`docs/AI-GUIDE.md`）では、
**2 言語化された文面のリテラルを「『…』と出たら〜しなさい」の目印にしてはいけません**。
案内文そのものが 2 言語になっても同じです。**訳した先の語を目印にすると、その言語でしか効かない案内になります**。
通知は利用者の言語で出るので、英語で動く AI にはその語が現れず、促しが届きません。
しかも**壊れてもテストは緑のまま**です。英語環境の AI が、何のエラーも出さずに従わなくなるだけです。「日本語で判定している」箇所が 2 言語化で気づかれずに壊れるのと同じ構図です。

| 目印の作り方 | 例 |
| -- | -- |
| **① 通知が示す looptrack のコマンドを指す**（最優先。ASCII で、どの言語の文面にも `{command}` として入る） | 「結果に `looptrack issue usage attach <ID>` が示されたら、そのコマンドを実行する」 |
| **② コマンドを示さない通知は、条件を言葉で説明する** | 「ツールの結果とは別の行で、`setup` ツールか更新のコマンドを示す注記が付いていたら」 |
| ③ AI が**書く**側の取り決めの語は目印にしてよい | コメント先頭の `フィードバック:` / `判断:` / `差し戻し:`（利用者の言語で変わらず、サーバも同じ語で判定する） |

検査は `internal/i18n/aiguide_marker_test.go` です。対訳表の「日本語にだけある文面」を案内文（リポジトリ全体の Go の文字列リテラルと
上の Markdown）が含んでいたら失敗します。あわせて、AI に行動を促す通知がどの言語でも `{command}` を持つこと（= ① の目印が必ずあること）も確かめます。

**kit の rules と skill**

`kit/` に配る rules と skill の本文は、AI しか読みませんが **2 言語化します**。v1.0.0 の公開前の決定で、公開版を英語圏の利用者が
自分で読んで直せることを優先しました。対訳表（`ja.json` / `en.json`）ではなく**ファイルで持ちます**。

**経緯**: 当初は「AI しか読まないので `kit/` の rules は日本語のまま（2 言語化の対象外）」としていました。
これを **2026-09-20 に利用者の判断で撤回しました**（下の「採らなかった案」にも残しています）。kit を読んで直すのは英語圏の利用者も同じです。
日本語のままでは、OSS として公開しても手を入れられません。**v1.0.0 の公開前に rules と skill を全部英語にします。**

| 決めごと | 内容 |
| -- | -- |
| 範囲 | `kit/` に配る rules と skill の本文を**全部**。加えて、**`kit/README.md` も英訳する**（2026-09-20 の利用者の決定）。配り物ではないが、kit の構成を読んで直すのは英語圏の貢献者も同じなので範囲に含める |
| `kit/README.md` の置き場（例外） | **ルートと同じ 2 本立て**にする（`kit/README.md` が英語・`kit/README.ja.md` が日本語。互いへのリンクは H1 の直後に 1 行）。下の `en/` の規約を当てはめないのは、**`kit/README.md` が配布物ではない**ため（`kit/embed.go` が配るのは `core/` と `loop/` の下だけ）。`docscheck` の `kitMD` は、この 1 対だけを明示して登録する（下の「検査」）。実装は `kit/README.md` を英語に書き直し、日本語を `kit/README.ja.md` に移した |
| 置き場 | **日本語が正本で今の場所のまま**、英語は同じディレクトリの `en/` に同じファイル名（`kit/loop/rules/background-process.md` ↔ `kit/loop/rules/en/background-process.md`）。正本を動かさないのは、kit のパスを `manifest.json`・hook の配線・`internal/client/kitinit` が参照しているため（`README.md` / `docs/guide/` は英語が上位だが、あちらは配線に使われていない） |
| 配り方 | **rules は導入で日英の両方を置く**（`.claude/rules/looptrack-loop/` と同 `en/`）。rules では導入時に片方だけを選ぶ形にはしない。**skill は導入時の言語の 1 本だけを `.claude/skills/<名>/SKILL.md` に置く**（`en/SKILL.md` は併置しない。下の「読む側」） |
| 読む側 | rules は hook（`session-start-rules`・`user-prompt-rules`）が **実行時に** `i18n.FromEnv` と同じ順（`LOOPTRACK_LANG` → `LC_ALL` → `LC_MESSAGES` → `LANG`）で選ぶ。訳の無いファイルは正本の日本語に戻る。**skill は実行時に選べない**（AI のハーネスが固定のパス `.claude/skills/<名>/SKILL.md` を読み、looptrack が割り込む余地が無い）ので、init が**導入時の**言語（同じ順）で本文を選んで置く。以前の init が置いた `en/SKILL.md` は次の init と `--remove-loop` で片付ける（手で変えたものは残す） |
| 例外 | Codex・Copilot の `AGENTS.md` は生成物なので、写す本文は**導入時の**言語で決まる（言語を変えたら `looptrack issue init --loop` を打ち直す）。skill も同じく導入時の言語で決まる（言語を変えたら `looptrack issue init` を打ち直す） |
| 検査 | `go test ./internal/docscheck/` の `TestKitStructure` が、見出しの階層の並び・コードブロックの数・表の数・注入の印（`<!-- looptrack:inject … -->`）・相対リンクの一致を確かめる（`docs/guide/` と同じ関数を使い回す）。訳の無いファイルは落とさない（訳した分だけ検査するラチェット）。**`kit/README.ja.md` と `kit/README.md` もこの検査の対象**（`kit_test.go` の `kitMD` が、`en/` の規約の外にあるこの 1 対だけを明示して登録する。正本が `kit/README.ja.md`・訳が `kit/README.md` で向きが逆なので、`en/` の振り分けには任せられない） |

**Web の画面**

| 対象 | 方式 |
| -- | -- |
| HTML テンプレート | `template.FuncMap` に `T` を足し、`{{T .Lang "server.web.login.title"}}` と書く。**テンプレートは 1 本のまま**（日英 2 本にすると文面が離れ、片方だけ更新されたときに気づけない） |
| 画面の中の JS | 画面を返すときに、**その画面が使う文面だけ**を `<script type="application/json">` で埋め込み、JS がそれを引く。対訳表を API で丸ごと公開しない。埋め込むのはデータブロックなので、CSP のインラインスクリプト禁止（§10）には当たらない |

**テストの注意**: 既定の言語は英語です。そのため**日本語の文面を検査するテストでは言語を固定してください**
（HTTP は `Accept-Language: ja`、CLI と hook は `LOOPTRACK_LANG=ja`）。
**子プロセスで `looptrack` を起動するテスト**では、子プロセスの環境にも `LOOPTRACK_LANG=ja` を渡します
（渡さないと、実行する機械の `LANG` で結果が変わります）。

#### 採らなかった案

| 案 | 採らなかった理由 |
| -- | -- |
| プロジェクトの言語設定（`lang`）で片方だけを認める | 設定と移行の手間が増える。語が衝突しないので、両方を常に認めても誤判定が起きない |
| ~~v1.0.0 でメッセージをプロジェクト単位・利用者単位で切り替える~~（**2026-09-20 に覆した**） | 当初は「対象が広く公開を遅らせる。AI は日本語の指示を読めるので、別名だけで英語の利用者の書く側は困らない」として見送った。**利用者の判断で v1.0.0 の範囲に入れ直した。**読む側が日本語のままでは、OSS として公開しても英語の利用者が使えないため |
| ~~`kit/` に配る rules と skill は日本語のまま（2 言語化の対象外）~~（**2026-09-20 に撤回**） | 当初は「AI しか読まないので対象外。正本が 2 つに割れると AI の従う規則が言語で変わる」として日本語のままにしていた。**利用者の判断で撤回し、v1.0.0 の公開前に rules と skill を全部英語にすることにした**（`kit/README.md` も範囲に含める）。公開版を英語圏の利用者が自分で読んで直せることを優先する。置き場と配り方は上の「kit の rules と skill」 |
| ~~MCP の `instructions` 本体と `guide` の本文は日本語のまま（2 言語化の対象外）~~（**2026-09-21 に撤回**） | 当初は「AI しか読まない。正本が 2 つに割れると、AI の従う規則が言語で変わる」として日本語のままにしていた。**利用者の判断で撤回し、kit の rules・skill と同じく 2 言語化することにした**。kit で同じ理由を撤回したのと同じ判断で、`guide` は `looptrack issue guide` として利用者の端末にも印字されるため（英語で動く AI に日本語の規則だけを読ませる形が残る）。**MCP の `Tool.Description` と `jsonschema` タグはこの決定に含まなかった**（同じ日の別の決定で 2 言語化した。次の行） |
| ~~MCP の `Tool.Description` と `jsonschema` タグは日本語のまま（2 言語化の対象外）~~（**2026-09-21 に撤回**） | 当初は「AI が読むための文面で、利用者の画面には出ない。正本が 2 つに割れると AI の従う規則が言語で変わる」として日本語のままにしていた。**利用者の判断で撤回し、接続ごとに利用者の言語で出すことにした**（同じサーバに日英の利用者が同時に繋いでも、それぞれ自分の言語になる）。ツールの使い方を決めるのは AI なので、`instructions` を 2 言語化しても、ツール定義が日本語のままでは英語で動く AI に日本語の規則が残る |
| 入力項目の説明を `jsonschema` タグに日本語の正本のまま残し、英語だけを対訳表から引く | タグと `ja.json` の 2 か所に正本ができ、片方だけ直すと黙って食い違う。タグには ID を書き、文面は対訳表だけに置いた |
| 原文をキーにして英訳だけを表に持つ（gettext 的な方式） | 既存コードを `T("…")` で包むだけで済むが、原文を直すたびにキーが変わり、訳が静かに外れる。ID を付ける正攻法を採った |
| 3 言語目を増やせる作りにする | v1.0.0 では日英の 2 つに絞る。対訳表の形は増やせるが、増やす前提の抽象を先に作らない |

## 10. セキュリティ

- インターネットに公開され、未修正の脆弱性の情報も載りうる前提で作ります。HTTPS のみ・Cookie は `Secure; HttpOnly; SameSite=Strict; Path=/looptrack`・CSRF トークン・CSP（インラインスクリプト禁止・nonce）を使います。
- 画面は属性値・リンク・ID をすべてエスケープし、`javascript:` の URL を出しません。
- ログイン試行はアカウント・IP 単位で制限します。PAT は SHA-256 ハッシュで保存し、期限・失効・最終利用時刻を持ちます。OAuth の更新トークンもハッシュで保存し、入れ替えと再利用の検知をします（§3-2）。
- 二段階認証の必須 / 任意は管理者が切り替えます（§2-1「二段階認証の設定」）。既定（未設定・既存 DB）は必須です。任意にしても、登録済みの利用者には TOTP を求めます。
  必須を外す操作には管理者の再認証（パスワード + 登録済みなら TOTP）が要り、変更は追記専用の `setting_changes` に残ります。任意 → 必須にすると、TOTP を経ていないセッションは使えなくなります。
  最初の管理者を作る Web 画面は、通常モードでは持ちません（公開サーバに「最初の管理者になれる窓」を開けないためです）。
  管理者が 0 人のサーバは、画面・API・MCP とも「セットアップ未完了」（503）を返し、ログインも受け付けません（§3-3）。
  例外はローカルモードの初回設定の画面です（§3-3）。127.0.0.1 固定で待ち受け、接続元が loopback の要求だけを受けます。
  Cookie に結びつけた CSRF トークンと Host・Origin の検査で守り、管理者ができたら二度と出しません。
- ローカルモード（`LOOPTRACK_LOCAL_MODE=1`・§3-3）は認証を省く代わりに、127.0.0.1 / ::1 / localhost でだけ待ち受けます。Host ヘッダ（DNS rebinding）と
  変更系の Origin / Sec-Fetch-Site（別サイトからの変更）も検査します。同じ機械のほかの利用者は防ぎません。
- アプリ用の DB 利用者に与えるのは、その DB の表だけです（`deploy/grants.sql`）。`comments` / `issue_events` には UPDATE / DELETE を与えません
  （append-only を DB の権限でも担保するため）。
- 監査: すべての変更を `issue_events` に記録します（誰が・どの経路で・どのセッションから）。

