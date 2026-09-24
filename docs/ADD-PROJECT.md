# 新しいプロジェクトをイシュー管理サーバに載せる

所要時間は 10 分ほどです。**上から順に実行し、最後の検証まで通してください。** プロジェクト側の設定は `looptrack issue init` を 1 回実行すれば入ります（§4）。

用語: `<slug>` はサーバ上のプロジェクト名です（英小文字・数字・ハイフン。例 `web-app` / `infra`）。
`<PREFIX>` は ID の接頭辞（例 `MYP` → `MYP-0001`）、`<PROJECT>` はプロジェクトのソースリポジトリの絶対パスです。

前提として、自分のアカウントがあり `looptrack issue login --browser` を済ませている必要があります（[AI-GUIDE.md](AI-GUIDE.md) §1）。
サーバ側の手順（§1・§2）は管理者が行います。サーバの管理コマンド（`looptrack …`）はサーバ上で実行してください。
compose なら `docker compose exec -T looptrack /looptrack …` の形で、systemd なら [server/DEPLOY.md](server/DEPLOY.md) の「動かし方」の表の形で実行します。

> Markdown で書いたイシューを持つプロジェクトを移す場合は、先に取り込み（`looptrack import` / `verify`）を済ませてください。

---

## 0. 事前確認

```bash
looptrack project list
```

- **`prefix` は既存のものと重複させないでください**（サーバも拒否します）。
- **`prefix` と `width` は後から変えられません**（発番済みの ID が壊れるためです）。

---

## 1. サーバにプロジェクトを作る（管理者）

```bash
looptrack project create <slug> --prefix <PREFIX> --width 4 \
  --name "表示名" --description "プロジェクト選択画面に出す 1 行の説明" --order 100
```

| オプション | 用途 | 変更 |
| -- | -- | -- |
| `--prefix` | 採番の接頭辞（英大文字で始まる英大文字・数字。ハイフン区切り可） | **後から変更しない** |
| `--width` | ゼロ埋め桁数（既定 4） | **後から変更しない** |
| `--name` | プロジェクト選択画面の表示名 | — |
| `--description` | 同・1 行説明 | — |
| `--order` | 同・並び順（小さいほど先。既定 100） | — |

コメント必須や状態の禁止といったプロジェクト別ルールが要るなら、`deploy/rules/<slug>.json` を作って
`looptrack project rules set <slug> - < deploy/rules/<slug>.json` を実行します。
書式は [server/DESIGN.md](server/DESIGN.md) の「プロジェクト別ルール」にあります。例は `deploy/rules/example.json` です（全種類のルールを使った架空のプロジェクト）。

## 2. 権限を付ける（管理者）

https://example.com/looptrack/admin/users で利用者を開き、「プロジェクトの権限」で `<slug>` に権限を付けます。
`editor`（起票・コメント・状態変更）か `viewer`（閲覧のみ）のどちらかです。admin の利用者は付けなくても全プロジェクトを扱えます。

## 3. 運用ルールの文書を置き、サーバに登録する

テンプレート [templates/project-rules.md](templates/project-rules.md) をコピーし、プロジェクト固有の部分を埋めます。
型の使い分け・ラベルの使い方・プロジェクト側の規約などです。**この文書がそのプロジェクトの元の定義になります。**
サーバに登録すると、`looptrack issue guide`（MCP は `guide`）が共通規則・プロジェクト別ルールと一緒に AI へ返します。

ファイルの置き場はどこでもかまいません。そのプロジェクトのリポジトリでも、運用文書をまとめたリポジトリでも使えます。
下は `docs/projects/<slug>.md` に置いた場合の例です。

```bash
cp docs/templates/project-rules.md docs/projects/<slug>.md
# 埋めたら登録する（直したときも同じコマンドで置き換える）
looptrack project guide set <slug> - --source docs/projects/<slug>.md < docs/projects/<slug>.md
```

見本は [projects/example.md](projects/example.md) です（ラベルの使い方・プロジェクト別ルールの全種類）。

---

## 4. プロジェクト側に導入する（`looptrack issue init` の 1 回）

プロジェクトのディレクトリで実行します。**まず `--dry-run` で差分を見てから**反映してください。

```bash
cd <PROJECT>
looptrack issue init --project <slug> --dry-run
looptrack issue init --project <slug>
```

looptrack は MCP の setup ツールの手順（`cli: "looptrack"`）か `looptrack self-update` で `~/.local/bin` に置きます。
init はスクリプトを 1 つも置きません。フックは `looptrack hook <名前>`、許可は `Bash(looptrack issue:*)`、案内の節と skill の CLI は `looptrack issue` になります。
入るものは次のとおりです。

| 入るもの（Claude Code・既定） | 内容 |
| -- | -- |
| `.claude/settings.json` の `env` | `LOOPTRACK_API_URL` / `LOOPTRACK_PROJECT`。**トークン（`LOOPTRACK_TOKEN`）は書かない**（各自の `looptrack issue login --browser`） |
| フック | `looptrack hook …`: SessionStart の `summary`・鮮度ガード（[AI-GUIDE.md](AI-GUIDE.md) §9）・トークン計測（MCP の操作・ターン終了・セッション終了） |
| 許可 | `permissions.allow` に `Bash(looptrack issue:*)` |
| skill | `.claude/skills/issue/SKILL.md`（`/issue`）と `.claude/skills/token-report/SKILL.md`（kit/core から実体で置く。「トークンレポートを作成して」・画面の「レポート作成」の依頼（summary に出る）から、集計 → 本文 → PDF（`looptrack report pdf`・手元）→ 台帳登録まで。`--no-skill` では入れない） |
| 案内 | `CLAUDE.md` に `<!-- looptrack:begin -->`〜`<!-- looptrack:end -->` の節（内容は [templates/CLAUDE-snippet.md](templates/CLAUDE-snippet.md)） |
| `.gitignore` | `.claude/.looptrack-freshness/` |

- **既存の設定は消さずにマージします。** 変更したファイルは `.claude/.looptrack-init-backup/<時刻>/` に控えます。何度実行しても壊れません（2 回目は「変更はありません」と出ます）。
- 既存の `LOOPTRACK_PROJECT` と違う slug を指定すると、`--force` を付けない限り拒否します。
- Codex も使うなら `--agent claude-code,codex` を付けます。
  - 入るのは `.codex/hooks.json`・`AGENTS.md`・`.codex/config.toml` の `[shell_environment_policy]` です。最後のものは CLI 用の `LOOPTRACK_API_URL` / `LOOPTRACK_PROJECT` で、サンドボックスのネットワークの許可は書きません。
  - `~/.codex/config.toml` に足す MCP の接続は、実行時に案内が出ます。
  - Codex の AI は**イシューの操作を MCP のツールで行います**。CLI を使うのは MCP に無い操作だけで、権限の昇格と利用者の承認が要ります（[AI-GUIDE.md](AI-GUIDE.md) §7-1）。
  - フックは**ターミナルの `codex` の `/hooks` で信頼**してください（§7-2）。
- GitHub Copilot（VS Code のエージェントモード・Copilot CLI）も使うなら `--agent copilot` を付けます。
  - 入るのは `.github/hooks/looptrack.json` の SessionStart と `AGENTS.md`（MCP が主）です。
  - `--mcp` を付けると VS Code の `.vscode/mcp.json` と Copilot CLI の `.github/mcp.json`（`tools: ["*"]`）も入ります。
  - トークン計測のフック（`looptrack hook usage --agent copilot`）も入ります。送るのは、利用者が OpenTelemetry のファイル出力を有効にしたときだけです（手順は [AI-GUIDE.md](AI-GUIDE.md) §7-3-1）。有効にしていない利用者の Copilot の操作は、未付与に数えません。
  - Windows では `powershell` / `windows` のフィールドに `command` と同じ `looptrack hook …` が入ります（実物は未確認）。詳しくは [AI-GUIDE.md](AI-GUIDE.md) §7-3 にあります。
- 組み合わせるなら `--agent claude-code,codex,copilot` です。それ以外の AI は `--agent other`（案内文だけ）を使います。
- MCP の接続設定も入れるなら `--mcp` を付けます（`.mcp.json` の `looptrack`）。
- 入れたくないものがあれば `--no-freshness` / `--no-usage` / `--no-summary` / `--no-skill` で外せます。
- Looptrack のリポジトリが手元に無い端末では、トークンを `login` してから `--source server` を付けます。サーバの `/api/v1/dist` から取得し、SHA-256 を確かめて置きます。MCP だけを接続した端末は §4-2（setup ツール）を見てください。
- **手で配線したプロジェクト**も一度 `init` を通してください。SessionStart の `summary` に `--agent claude-code` が付き、導入済みであることがサーバへ通知されます。付かないと、MCP のツール結果に【導入が未完了】が出続けます。
- **ルールの強制を hook で作らないでください。** サーバのプロジェクト別ルール（§1）に置けば、CLI・MCP のどの経路でも同じ判定になります。

#### ループエンジニアリング一式（loop）を入れるか（利用者が決める）

上の表は **core** で、必ず入る最小のループです。その上に **loop** を足すかどうかは**利用者が決めます**（AI が勝手に決めてはいけません）。
loop には出力規律・作業規律・確認モード・引き継ぎ鮮度・イテレーション規律（`/iterate`）・文脈の大きさの警告・背景プロセス検知が入っています。
内訳は [kit/README.ja.md](../kit/README.ja.md)、効果は [AI-GUIDE.md](AI-GUIDE.md) §1 にあります。

| 指定 | 動き |
| -- | -- |
| `init --loop` | core + loop を入れる |
| `init --no-loop` | core だけ。辞退を `.claude/.looptrack-kit.json` に記録し、setup ツールは以後 loop を勧めない |
| 指定なし | 対話なら core を入れた後に要 / 不要を問う（既定は不要）。非対話なら入れない（次の対話で問う） |
| `init --remove-loop` | loop の配線・rules・skills だけを外す。core と、利用者が手で変えたファイルは残す |

### 4-2. MCP の接続設定だけから導入する（setup ツール）

Looptrack のリポジトリもトークンも無い端末で、AI に MCP の接続設定だけをした状態から導入する手順です（設計は [server/DESIGN.md](server/DESIGN.md)）。

1. 利用者が AI に MCP の接続設定を入れます。初回の接続では、ブラウザで許可（OAuth）をします。設定の置き場は AI ごとに違います（[AI-GUIDE.md](AI-GUIDE.md) §7）。
   - Claude Code は `.mcp.json` か `claude mcp add`、Codex は `~/.codex/config.toml` の `[mcp_servers.looptrack]` です。
   - GitHub Copilot は VS Code の `.vscode/mcp.json` です。Copilot CLI はリポジトリの `.github/mcp.json` か `~/.copilot/mcp-config.json` を使います。`tools: ["*"]` が必須で、`.vscode/mcp.json` は読みません（AI-GUIDE §7-3）。
   - ブラウザが開かないときは、既定のブラウザを先に起動しておいてください。そのうえで Claude Code の `/mcp` のモーダルを閉じて開き直します。失敗した後は「再連携」のまま押しても開かないことがありますが、開き直すと「連携」に戻ります。
     たとえば Firefox が「起動時にプロファイルを選ぶ」設定だと、起動していない状態では渡された URL が捨てられます。`looptrack issue login --browser` は URL も表示するので、起動済みのブラウザに貼れば続けられます。
2. AI に最初の依頼をします。内容は何でもかまいません（例「イシュー管理を使えるようにして」「/mcp__looptrack__setup」）。接続時の指示に従って、AI が `setup` ツールを呼びます。
   - loop を入れるかがまだ決まっていなければ、setup は loop の問いだけを返します（コマンドは返しません）。
   - AI は問いを利用者に示し、答えを得てから `loop=yes` / `loop=no`（と同じ `workspace`）を付けて setup を呼び直します。
   - 答えに合うコマンドが 1 つだけ返ります。配布物の取得 → SHA-256 の確認 → `looptrack issue init --source server --dist … --loop`（入れない答えなら `--no-loop`）です。AI は利用者の承認を得てこれを実行します。
3. トークンが未登録なら、AI が利用者の承認を得て `looptrack issue login --browser --url https://example.com/looptrack` を実行します。開いたブラウザで、利用者がログインと承認をします。
   トークンは会話に出ません。ブラウザの無い環境では /account で発行して `login --url` に貼ります。
4. 利用者が AI を再起動して、フックを承認します。
   - Claude Code は再起動時に確認が出ます（デスクトップ版は確認を出さずに読み込みます）。
   - Copilot は新しいセッションを始めます。VS Code なら新しいチャットです。Copilot CLI はフォルダを信頼する確認で「今後も信頼」を選びます。
   - Codex はプロジェクトが信頼済み（trusted）であることが前提です。そのうえで**ターミナルの Codex CLI（`codex`）をプロジェクトで起動し、`/hooks` でフックを信頼**してください。
     信頼は `~/.codex/config.toml` の `[hooks.state]` に記録され、デスクトップ版にも効きます。デスクトップ版のチャット欄の `/hooks` はコマンドにならず、未信頼のフックは知らせなしに飛ばされます。フックが動くのは、その後に始めたセッションからです（[AI-GUIDE.md](AI-GUIDE.md) §7-2）。
5. 次のセッションの開始時に、SessionStart のフックが導入済みを通知します。`setup` は「導入済み」を返し、MCP のツール結果の【導入が未完了】が消えます。

---

### 4-3. クラウド版の AI からも使う

Claude Code on the web・Codex cloud・Copilot cloud agent から使う場合は、4 の成果物をコミットしてプッシュしておきます。そのうえで [CLOUD-AGENTS.md](CLOUD-AGENTS.md) の手順でクラウド側を設定してください。
注意点は次の 3 つです（詳しくは [AI-GUIDE.md](AI-GUIDE.md) §7-4）。

- 手元の PC のサーバには届きません。インターネットに公開したサーバのホスト名を、クラウド側の許可リストに足してください。
- PAT はプロジェクト単位に絞れません。管理者がクラウド専用の利用者を作り、このプロジェクトだけに参加させます（書き込むなら editor）。PAT は有効日数 30 日で発行し、シークレットに置きます。コミットはしないでください。
- Codex cloud のネットワークの許可を「GET 等に絞る」にすると、書き込めなくなります。

## 5. 検証（ここまで通して完了）

Claude Code を起動し直してから実行します。`env` とフックを反映させるためです。フックの確認を求められたら、内容を見て承認してください。

```bash
cd <PROJECT>
looptrack issue config           # モード: API・プロジェクト <slug>・権限が出る
looptrack issue guide            # 共通規則・ルール・運用文書（§3 で登録したもの）が出る

# 1 周を通す（テスト起票 → 着手 → 閉じる）
looptrack issue new "接続確認" --type task
looptrack issue next --dry-run    # 次に着手するもの: <PREFIX>-0001
looptrack issue next              # 着手: <PREFIX>-0001: Todo → In Progress
looptrack issue status <PREFIX>-0001 Canceled --comment "接続確認のため起票。破棄する。"
```

https://example.com/looptrack/ に `<slug>` が出れば完了です。

## 6. commit する

- **運用文書を置いたリポジトリ**: `<slug>.md` をコミットします。サーバが強制するルールを JSON で持つなら、そのファイルもです。
  元の定義はサーバへの登録（§3）なので、ファイルを直したら登録し直してください。
- **`<PROJECT>`**: init が作ったり変えたりしたファイルをコミットします。`.claude/settings.json`・`.claude/skills/issue/`・`.gitignore`・`CLAUDE.md` です。
  Codex なら `.codex/hooks.json`・`.codex/config.toml`・`AGENTS.md`、Copilot なら `.github/hooks/looptrack.json`・`AGENTS.md` も入ります。
  `.claude/.looptrack-init-backup/` は版管理に入れません。

---

## 完了チェックリスト

- [ ] サーバに `<slug>` がある（`looptrack project list`）。`prefix` が既存と重複していない
- [ ] 使う人に権限がある（admin 以外）
- [ ] `docs/projects/<slug>.md` があり、サーバに登録した（`looptrack project guide show <slug>`）
- [ ] `looptrack issue init --project <slug>` を実行し、2 回目が「変更はありません」になる
- [ ] loop（ループエンジニアリング一式）の要 / 不要を利用者に確認し、`--loop` か `--no-loop` で反映した
- [ ] `looptrack issue config` が API モードを示し、`guide` にルールと運用文書が出る
- [ ] `next` で着手でき、`status` / `close` が動く
- [ ] https://example.com/looptrack/ にプロジェクトが出る
