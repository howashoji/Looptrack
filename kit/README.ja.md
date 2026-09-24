# kit — 各プロジェクトへ配る hook / rules / skill（core / loop）

*English: [README.md](README.md)*

設計は [docs/server/DESIGN.md](../docs/server/DESIGN.md) にあります。

ここに置くのは、**どのプロジェクトでも同じように効くもの**だけです。`looptrack issue init` と MCP の setup ツールが、
ここから各プロジェクトに配ります。プロジェクト固有の分岐は入れません（汎用でないものはここに置きません）。
固有の運用はサーバのプロジェクト別ルール（`looptrack project rules set`）か、そのプロジェクトの運用文書に置いてください。

| 層 | 何が入るか | 入れ方 |
| -- | -- | -- |
| **core** | 汎用で、かつイシュー管理サーバの活用に不可欠 | init / setup が無条件に入れる |
| **loop** | 汎用だがサーバ無しでも成り立つ規律。core と組み合わせると「起票 → 着手 → 実装 → 検証 → クローズ → 次へ」を AI が自走する基盤になる | 利用者が AI を介して要 / 不要を選ぶ（`init --loop` / `--no-loop` / `--remove-loop`） |
| 対象外 | 特定のプロジェクト・スタック・外部ツールに依存するもの | 配らない |

「効くループ」列は、各項目が 3 層のループのどれを回すかを示します（① AI の作業・② 人の判断・③ 外からの反応。
[DESIGN.md](../docs/server/DESIGN.md)）。
② の「人の判断待ち」と ③ の「外からの反応」を AI に持ちかけさせる指示と `summary` の 3 層表示は、
このシステム無しでは成り立たないので core に入れています。
サーバの prompt `review` / `loop` と `guide`（共通規則）も ②③ に効きます。これらは配布物ではなく、サーバから直接 AI に届きます。

## core — 無条件に配るもの

| 何を配るか | 種類 | 何をする | なぜ汎用か | 検証 | 効くループ |
| -- | -- | -- | -- | -- | -- |
| `looptrack issue`・`looptrack hook` | CLI・フック本体（配布物の実行ファイル） | イシュー操作・鮮度ガード・トークン付与 | サーバの API を叩くだけで、プロジェクトの中身を知らない | `internal/clitest`・`internal/client/hook/core` | ①②③（`next`・`verify`・`list --status "In Review"`・`list --has-feedback`・`summary`） |
| SessionStart の `summary --agent` | hook | 3 層の要約（① いまの周・② 人の判断待ち・③ 外からの反応）と通知（未付与・依頼・導入状態）を冒頭に注入し、導入済みを通知する | サーバが返す summary をそのまま出す。既定の slug と URL は `LOOPTRACK_PROJECT` / `LOOPTRACK_API_URL` から取る | `internal/client/hook/core` | ①②③（3 層の見出しを毎セッション注入する） |
| 鮮度ガード（UserPromptSubmit / PostToolUse の mark・Stop の check） | hook | 参照したのに更新していないイシューがあれば Stop を差し戻す | 判定はサーバの更新イベントだけを見る。言語もスタックも問わない | `internal/client/hook/core` | ① |
| トークン計測（`looptrack hook usage`。PostToolUse `mcp__.*`・Stop・SessionEnd） | hook | MCP 操作・ターン終了・セッション終了のトークン消費を送る | 会話記録の読み取りは AI ごとのアダプタに閉じていて、プロジェクトには依存しない | `internal/client/usagesnap` | ①（計測。どの層の消費も記録する） |
| `core/skills/issue/SKILL.md` | skill | 起票 → 進める → 閉じる → 次を選ぶ の手順と禁止事項 | どのプロジェクトも同じ CLI・同じ規則を使う。固有の運用（ラベル規約・文書 ID 体系など）は書かず、`looptrack issue guide` が返すプロジェクトの運用文書へ送る | — | ①②③（人の判断待ち・外からの反応の手順を含む） |
| `core/skills/token-report/SKILL.md` | skill | トークン消費のレポート（PDF）を作って渡す手順 | 集計はサーバが持つ。skill は依頼の仕方だけを書く | — | ①（計測） |
| CLAUDE.md / AGENTS.md の案内節 | 文書 | サーバの URL・CLI の呼び方・guide の読み方 | 書く内容はサーバの設定から決まる | `internal/client/kitinit` | — |

## loop — 利用者が選んで配るもの

| 何を配るか | 種類 | 何をする | なぜ汎用か | 調整 | 効くループ |
| -- | -- | -- | -- | -- | -- |
| `rules/output-discipline.md` + `hooks/session-start-rules` + `hooks/user-prompt-rules` + `hooks/stop-tool-markup-guard` | rules・SessionStart・UserPromptSubmit・Stop | ツール呼び出しの書式規律（冒頭に単独・malformed なら即再送）を毎セッション / 毎ターン注入し、素の markup が残った返答を差し戻す | 書式ミスは harness の性質で起きる。プロジェクトの中身とは無関係 | 注入するのは rules の印を付けた節だけなので、文面はファイルを直せば変わる | ① |
| `rules/working-discipline.md`（注入は上の 2 本） | rules | 応答・事実認定・検証・並行セッション・サブエージェントの共通規律を毎セッション注入 | 「緑を鵜呑みにしない」「確かめてから断定する」は、どのスタックでも同じ失敗を防ぐ | 文書成果物向けなど一部は「任意節」に分けてあり、注入するかを選べる | ① |
| `hooks/user-prompt-task-mode` + `hooks/pre-edit-task-mode-guard` | UserPromptSubmit・PreToolUse（`Edit\|Write\|NotebookEdit`） | 「確認して」は調査報告と計画で止め（investigate）、編集を deny する。「実装して」で解除（execute） | 「確認」と言われて編集を始める失敗は、どのプロジェクトでも起きる | 語の追加は `LOOPTRACK_LOOP_TASK_MODE_EXEC_RE` / `_INVEST_RE`、実行モードの文言は `_EXEC_NOTE`、例外ディレクトリは `_ALLOW_DIRS` | ②（「確認して」で止めて人の判断を待つ） |
| `hooks/post-work-complete-handoff-mark` + `hooks/stop-handoff-freshness` + `hooks/session-start-memories` + `skills/session-handoff/SKILL.md` | PostToolUse（`Bash\|mcp__.*`）・Stop・SessionStart・skill | 作業完了イベント（`git commit`・`looptrack issue close`・状態を Done / Canceled に変更）を積み、引き継ぎ記憶が更新されるまで Stop を差し戻す。ファイル版の記憶は冒頭に注入する | 「セッション終了時にだけ引き継ぎを書く」設計は、終了できないときに必ず失われる。これはどのプロジェクトも同じ | 鮮度判定のバックエンドは `LOOPTRACK_LOOP_HANDOFF_BACKEND`: `file`（既定）/ `auto-memory` / `command`（`_CHECK_CMD` が 0 / 1 / 他で答える） | ① |
| `rules/iteration-discipline.md` + `hooks/session-start-iteration` + `skills/iterate/SKILL.md` + `looptrack gates` | rules・SessionStart・skill・コマンド | 1 イテレーション = 1 実装イシュー。見つけた問題は必ず起票し、bug を抱えたまま新しい実装を積み上げない。未解決の bug をセッション冒頭に出し、ゲート（build → lint → test）を fail-fast で回す | 「実装 → 検証 → クローズ → 次へ」の自走に必要な規律で、スタックに依らない | ゲートの段は `LOOPTRACK_LOOP_GATES_STAGES`（既定 `build lint test`）、ディレクトリは `_GATES_DIR` / `-C`。実コマンドはプロジェクト側の Makefile が持つ | ① |
| `hooks/session-scope-guard` | UserPromptSubmit | 直近の応答のコンテキストが警告の閾値を超えたら知らせる（区切りで引き継ぎを書いてセッションを分けることを勧めるだけで、**何も止めない**） | 1 セッションに複数の課題を詰めるとコンテキストが膨らみ、キャッシュ読取が総消費の大半を占める構造は AI 側の性質で、プロジェクトに依らない。勧めるだけで、セッションを 1 つの課題に束縛はしない。**束縛は並行作業を妨げる**（`rules/working-discipline.md` は並行セッションとサブエージェントへの委譲を前提にしていて、取りまとめ役のセッションは多数の課題にまたがる）ので、その部分は削除した | 閾値は `_WARN`（既定 25 万） | ① |
| `hooks/pre-tool-scope-guard` | PreToolUse（`Edit\|Write\|NotebookEdit\|Bash\|mcp__.*`） | 別の git リポジトリへの変更と、別プロジェクトのイシューの変更を、`permissionDecision: ask` で利用者に確認する。読むだけ・`git pull`・同じリポジトリの worktree・git でない場所は通す | 作業ツリーの取り合い・全プロジェクトに効くファイルの変更・トークンの計上先の取り違えは、どのプロジェクトでも起きる | 例外は `LOOPTRACK_LOOP_SCOPE_ALLOW_REPOS`（空白区切りの絶対パス）。deny でなく ask（利用者が頼んだ横断の作業は人の判断で通す）。入れ子のシェルと `eval` で包んだ変更（`bash -c 'cd <別> && touch a'`・`sudo sh -c '…'`・`nohup bash -c '…' &`・`setsid sh -c '…'`・`eval "…"`）も、秘密のガードと同じ処理で 1 段ほどいて素の形と同じに確認する（ask なので位置を問わない）。包んだ形でも読むだけなら通す。2 段以上の入れ子・コマンド置換は素通りする | ① |
| `rules/secrets-discipline.md` + `hooks/pre-tool-secrets-guard` | rules・PreToolUse（`Edit\|Write\|NotebookEdit\|Read\|Bash`） | 資格情報の中身を読む・出力に出す・版管理に入れる操作を `permissionDecision: ask` で確認する（キーチェーンの読み出し、`.env`・`credentials.json`・鍵を `cat`／`jq`／`git add` する、Read / Write の対象がそれら）。雛形（`.env.example`）と公開鍵（`.pub`）は通す | AI に実機の確認を任せると、認証情報の在りかを調べる過程で中身が出力に残る。出力は取り消せない。どの製品も秘密を持ち、名前の付け方も共通している | 例外は `LOOPTRACK_LOOP_SECRETS_ALLOW`（空白区切り。パスの一部に当たれば通す）。deny でなく ask（秘密のファイルを直す正当な作業はある）。名前と実行される語で見分けるので**「うっかり」を捕まえるだけ**で、捕まえない形（コマンド置換の中身・エスケープした引用符で挟む形など）は `secrets-discipline.md` の「捕まえないもの」 | ① |
| `hooks/pre-tool-git-guard`（rules は `working-discipline.md` の並行セッションの節） | PreToolUse（`Bash`） | 巻き込み（`git add -A` / `git add .` / `git commit -a`）は `ask`。取り返しのつかないもの（`git push --force`、`+` で始まる refspec（`git push origin +main`・`+HEAD:main`）、`git reset --hard`、`git clean -f…`、`git switch --discard-changes`、パス指定の `git checkout` / `git restore`、作業ツリー外を指す `-C` / `--work-tree` / `--git-dir` の書き込み）は `deny`。`git add <パス>`・`git add -u`・`--force-with-lease`・ブランチ切替・読み取り専用の操作は通す | 「触ったパスだけを stage する」は rules にも各プロジェクトの文書にも書かれているのに破られる。作業ツリーを共有するのはどのプロジェクトでも起きる（worktree・複数のセッション・人と AI の同居） | 例外は `LOOPTRACK_LOOP_GIT_GUARD_ALLOW`（空白区切り。コマンドの文字列に含まれれば通す）。取り返しのつくものは ask、つかないものは deny（bypass permissions モードでは `ask` が素通りすると実測で判明したため）。入れ子のシェル（`bash -c 'git reset --hard'`・`eval "git push --force"`）と前置の語（`sudo git push --force`・`env git clean -fd`）は、コマンドの位置にあるときだけ 1 段ほどいて判定する（`echo bash -c '…'`・`ssh host bash -c '…'` のような引数の位置は通す）。リダイレクト（`2>&1`・`>/dev/null`・`&>/dev/null`）は記述子と行き先を語から外して判定する（`git checkout main 2>&1` は通り、`git checkout -- <パス> 2>&1` は `deny`）。**それでも「うっかり」を捕まえるだけ**で、次は素通りする（実測）: コマンド置換（`echo "$(git reset --hard)"`）・行継続（`git push \` + 改行 + `--force`）・引用符で囲まない `\` 区切りの Windows のパス（`C:\tools\git.exe push --force`）・一覧に無いラッパ（`caffeinate bash -c 'git reset --hard'`）・2 段の入れ子。一覧は `working-discipline.md` の「git ガードが止めるもの・止めないもの」。`C:/tools/git.exe`・`/usr/bin/git` は判定する | ① |
| `hooks/session-start-worktrees` + `looptrack worktree` | SessionStart・コマンド | 作業ツリー（worktree）の残骸をセッションの冒頭に知らせる。片付けられるもの・中身が失われかけているもの（日をまたいだ未コミット）があるときだけ出し、無ければ黙る。**何も消さない**（消すのは `looptrack worktree prune --yes` を打ったときだけ） | 課題ごとに worktree を切る運用だと取り込み済みのものが溜まる。道具があっても打つきっかけが無ければ同じことが起きる。git の性質だけを見るのでプロジェクトに依らない | 切るときは `LOOPTRACK_LOOP_WORKTREE_NOTICE=0`。「使っているかもしれない」とみなす長さは `LOOPTRACK_LOOP_WORKTREE_MIN_AGE`（既定 30m）。本体の作業ツリーは対象外 | ① |
| `hooks/user-prompt-stale-base` | UserPromptSubmit | いま居る作業ツリーの枝分かれの基点が古いまま長く走っていることを毎ターン知らせる（基準 `origin/main` からの分岐点の経過時間が閾値を超えている間だけ 1 行。作業ツリー名・基点の SHA・遅れのコミット数を出す。閾値内なら**何も出さない**し、**何も止めない**） | 長く走ったセッションが古い基点のまま判断を続ける失敗は AI の走らせ方の性質で、プロジェクトに依らない。SessionStart の 1 回では走り出しにしか出ず（そのときの基点はいつでも新しい）、`git merge` の直前では判断がもう終わっている。判定を**経過時間だけ**にしてあるのは、遅れのコミット数がプロジェクトの速度で意味を変えるため（半日で 200 コミット進むリポジトリもあれば、200 コミットが数か月分のリポジトリもある） | 閾値は `LOOPTRACK_LOOP_STALE_BASE_MAX_AGE`（既定 `4h`）、基準は `_STALE_BASE_REF`（既定は `origin/main` → `origin/master`）、切るときは `_STALE_BASE_NOTICE=0`。**hook の中で `git fetch` は打たない**（利用者の入力ごとにネットワークの待ちを乗せないため）。その代償として、誰も fetch していない間は `origin/main` 自体が古く、遅れを実際より小さく見積もる＝**何も出ないこと＝基点が新しいこと、ではない** | ① |
| `hooks/stop-runaway-background-process` + `hooks/subagent-stop-runaway-background-process` + `rules/background-process.md` | Stop・SubagentStop・rules | セッションが起こした終わらない子プロセス（長時間のポーリング等）を検知して差し戻す。**上限の無い待ちループの形**（`until` / `while` + `sleep` で上限の式が無い）のシェルだけは短い閾値（既定 10 分）で見る。子が終わった直後にも見る（SubagentStop） | 背景プロセスの放置はコーディング AI 共通の失敗 | 許可する正規表現は `LOOPTRACK_LOOP_RUNAWAY_ALLOW`（既定は MCP・docker・エディタ・dev サーバ・`--watch`）。閾値は `_THRESHOLD_MIN`（既定 30 分）、形別の閾値は `_LOOP_THRESHOLD_MIN`（既定 10 分） | ① |
| `hooks/pre-tool-wait-loop-guard`（rules は `background-process.md`） | PreToolUse（`Bash`） | 上限の無い待ちループ（`until` / `while` のループ本体に `sleep` があり、上限の式が無い）を**起動前に** `deny` で止める。`break`・`timeout`・`seq`・`SECONDS`・`date +%s`・数の比較・`--max` 等があれば通す。上限のある待ちで、待つ先の**親ディレクトリ**が無いときは止めずに注意する | Stop の検知は経過時間でしか見られず、閾値より前に作られたものは原理的に見えない（実測で 1 日に作られた 3 本は、いずれも閾値の前に人が `ps` で見つけた）。形は起動前に分かる | 例外は `LOOPTRACK_LOOP_WAITLOOP_ALLOW`（空白区切り。コマンドの文字列に含まれれば通す）。`ask` ではなく `deny`（bypass permissions では `ask` が素通りする）で、そのかわり**文面に上限つきへの書き直し方**を必ず入れる。前置の語や `eval` で包んだ待ちループ（`sudo sh -c '…'`・`nohup bash -c '…' &`・`setsid sh -c '…'`・`eval "while true; …"`）も止める（前置の語は秘密のガードと同じ一覧）。入れ子のシェルは git ガード・秘密のガードと同じ処理でほどく（コマンドの位置だけ。`sudo -u deploy sh -c '…'`・`/bin/bash -c '…'` も止める）。**それでも「うっかり」を捕まえるだけ**で、一覧に無い前置の語（`caffeinate bash -c '…'`）と引数の位置（`ssh host bash -c '…'`・`docker run img bash -c '…'`）は素通りする（残るのは Stop の経過時間による検知） | ① |
| `hooks/pre-tool-subagent-bound`（rules は `background-process.md`） | PreToolUse（`Task`\|`Agent`） | サブエージェントの起動の指示文に、背景で待つループの上限の規律を**そのまま追記する**（`updatedInput`）。**止めない**（追記だけ）。既に固定の印 `[looptrack:background-bound]` が入っていれば足さない。追記したことは `systemMessage` で利用者にも見せる | rules は親セッションにしか注入されないので、親が毎回書き写さないと子へ降りない。降りないまま同じ違反が繰り返し作られるのは、どのプロジェクトでも起きる | **ツールの入力を書き換えられると実物で確かめた AI（Claude Code）にだけ配線する**。Codex・Copilot に `updatedInput` 相当があるかは未確認なので、そこでは何も起きない（下の「Codex での対応」「Copilot での対応」） | ① |

## 対象外（配らない）

汎用かどうかは、**そのプロジェクトを知らなくても同じ判断ができるか**で決めます。次のものは配りません。

| 対象 | 理由 |
| -- | -- |
| 特定の DB・スキーマ・納品物の規約に依存するガード | プロジェクトごとに正解が違う。プロジェクト別ルールか、そのプロジェクトの hook に置く |
| 特定の外部サービス（別のイシュートラッカー・ブラウザ自動化・決済など）に依存するもの | そのサービスを使わないプロジェクトでは無意味か有害 |
| 特定のフレームワーク・言語のテストやデプロイの手順 | 原則（偽緑を信用しない・skip でなく fail・内部用語を外に出さない）だけを `iteration-discipline.md`・`working-discipline.md` に一般化して入れてある |
| 特定のブランチ名・環境名・リリース手順を前提にするもの | 名前を設定で外に出せるなら loop に入れる余地があるが、前提ごと持ち込まない |

## 配置（このディレクトリ）

```
kit/
  README.md                 ← この文書の英語版
  README.ja.md              ← この文書（何を配るかの正本）
  embed.go                  ← kit/ を looptrack に埋め込む（サーバが GET /api/v1/dist で配る）
  core/
    skills/issue/SKILL.md          ← skill issue（init が .claude/skills/issue/SKILL.md に置く）
    skills/token-report/SKILL.md   ← skill token-report（同 .claude/skills/token-report/SKILL.md）
  loop/
    rules/*.md              ← output / working / iteration / secrets-discipline・background-process（5 本）
    rules/en/*.md           ← 上の英訳（同じファイル名。訳のあるものだけ）
    skills/iterate/SKILL.md  skills/session-handoff/SKILL.md
    manifest.json           ← hook の配線（イベント・matcher・timeout・順序）と Codex・Copilot での対応
```

- `kit/embed.go` が `kit/` を埋め込みます（go:embed はパッケージより上を指せないので、別のパッケージにしています）。
  `GET /api/v1/dist` の一覧には `kit/core/…` `kit/loop/…` の名前で、SHA-256 つきで出ます。
- hook の本体は Go で書いてあり（`internal/client/hook/core`・`internal/client/hook/loop`）、kit にファイルはありません。
  出力の形は `internal/hookio` が AI ごとに直します。

### 本文の言語（日英 2 言語）

**日本語が元の定義で、いまの場所に置いたままにします。英語は同じディレクトリの `en/` に同じファイル名で置きます**
（`loop/rules/background-process.md` ↔ `loop/rules/en/background-process.md`）。

- ルートの `README.md` / `README.ja.md`・この文書（`kit/README.md` / `kit/README.ja.md`）・`docs/guide/` は
  英語が上位（`ja/` が下）です。ただし**これらは配線には使われていません**。
  kit のパスは `manifest.json`・hook の配線・`internal/client/kitinit` が参照しているので、元の定義を動かすと配線が全部動きます。
  `internal/i18n` の方針（日本語が元の定義で、英語がそこに追いつく）ともそろいます。
- **rules は、導入のときに日英の両方を配ります。** どちらを読むかは実行時に決まります（hook の rules の注入は `i18n.FromEnv` と同じ
  `LOOPTRACK_LANG` → `LC_ALL` → `LC_MESSAGES` → `LANG` の順。訳の無いファイルは日本語の元の定義に戻る）。
  rules では、導入時に片方だけを選ぶ形にはしません。`LOOPTRACK_LANG` を切り替えたときについていけなくなるからです。
  例外は Codex・Copilot の `AGENTS.md` です。これは生成物なので、導入時の言語で写します（言語を変えたら
  `looptrack issue init --loop` をもう一度実行してください）。
- **skill は、導入時の言語の 1 本だけを置きます。** skill は AI のハーネスが `.claude/skills/<名前>/SKILL.md` という
  固定のパスで読みます。実行時に言語を選ぶ仕組みが無いので、`en/SKILL.md` を並べて置いても読まれません。そこで init が
  導入時の言語（上と同じ順）で本文を選び、その 1 本を `SKILL.md` として置きます（`description` も導入時の言語になります）。
  以前の init が置いた `en/SKILL.md` は、次の init で片付けます（手で変えたものは残します）。
  **skill の言語を変えたいときは、`looptrack issue init` をやり直してください**（`AGENTS.md` の例外と同じです）。
- 構成の一致（見出しの階層の並び・コードブロックの数・表の数・注入の印・相対リンク）は、
  `go test ./internal/docscheck/` の `TestKitStructure` が確かめます。訳が無いファイルは失敗にせず、訳した分だけを検査します。

### core の置き方

| もの | 置き場（Claude Code） |
| -- | -- |
| `skills/issue/SKILL.md`・`skills/issue/en/SKILL.md` | `.claude/skills/issue/SKILL.md`。**置かれるのは導入時の言語の 1 本だけ**（英語なら `en/SKILL.md` の本文を `SKILL.md` として置く。`en/SKILL.md` は置かない）。init の印が kit の SKILL.md 自体に入っている。印の無い手で置いたものは変えない |
| `skills/token-report/SKILL.md` | `.claude/skills/token-report/SKILL.md`（同上） |

- core の hook（summary・鮮度ガード・トークン計測）は `looptrack hook <名前>` で配線します。
- **導入先にスクリプトは 1 つも置きません。** 本文に出てくる CLI の呼び方は、置くときに必ず `looptrack issue` にします
  （`internal/client/kitinit` の cliText）。
- サーバの一覧に `kit/core/…` が無ければ（古いサーバ）、skill を置かずに「配布物に kit が無い」と知らせます。

### loop の置き方

**配線の元の定義は `kit/loop/manifest.json` です。** init はこれを読んで `settings.json` / `.codex/hooks.json` /
`.github/hooks/looptrack.json` / `AGENTS.md` を書きます。

```json
{"version": 1, "layer": "loop",
 "entries": [
   {"name": "hooks/session-start-rules", "kind": "hook", "runner": "looptrack", "event": "SessionStart", "matcher": null,
    "timeout": 5, "order": 10, "codex": {"event": "SessionStart", "matcher": null}, "copilot": {"event": "SessionStart", "matcher": null}},
   {"name": "hooks/stop-handoff-freshness", "kind": "hook", …, "event": "Stop", "codex": {"event": "Stop", "matcher": null, "block": false}, …},
   {"name": "rules/output-discipline.md", "kind": "rules", "codex": null, "copilot": null},
   {"name": "skills/iterate/SKILL.md", "kind": "skill", "codex": {"agents_md": "description"}, "copilot": {"agents_md": "description"}}, …]}
```

- `name` は `kit/loop/` からの相対パスです。rules・skill は **kit/loop のファイルと 1 対 1** です（manifest.json 自身と `en/` の訳を除く）。
  hook は `hooks/<名前>` で、ファイルはありません（`looptrack hook <名前>` の名前。Go の登録表と 1 対 1）。
  確かめるのは `internal/client/hook/loop` の `TestManifest` です。
- `kind`: `hook` / `rules` / `skill`。
- `event`・`matcher`（無ければ null）・`timeout`（秒）・`order` を持つのは hook だけです。順は同じ event の中の順で、
  既存の hook の後ろにこの順で足します。`runner: "looptrack"` は、配線が
  `looptrack hook <名前> --agent <AI>`（PATH に無い端末では絶対パス。Claude Code は `.claude/settings.local.json`）になることを示します。
- hook の `codex`: `{event, matcher, block?}` = `.codex/hooks.json` に配線する / null = 配線しない。
  null なのは PreToolUse の編集ガード（Codex の編集ツールが PreToolUse で捕まるかを確かめていない）と、
  `stop-tool-markup-guard`（書式ミスは Claude に固有）です。Stop の 2 本は `block: false` です。配線に `--no-block` を付け、
  hook はやり直しを求めずに `{"systemMessage": …}`（exit 0）で知らせるだけにします。
- rules / skill の `codex`（`AGENTS.md` の `<!-- looptrack:loop:begin -->` 節に何を入れるか）: null = **入れない** /
  rules の `{"agents_md": "sections"}` = その rules の `<!-- looptrack:inject session -->` の節と、全文の置き場
  （`.claude/rules/looptrack-loop/<名前>`）への 1 行 / skill の `{"agents_md": "description"}` =
  frontmatter の description の 1 行と手順のパス。`output-discipline.md` は Claude の書式に固有なので null です。
- hook の `copilot`: `{event, matcher, block?}` = `.github/hooks/looptrack.json` に配線する / null = 配線しない
  （理由は下の「Copilot での対応」）。rules / skill の `copilot` は `codex` と同じです。
  **全項目に `codex` と `copilot` の列を置きます**（null でもかまいません）。
- 注入の印には AI を指定できます。`<!-- looptrack:inject session claude-code -->` は、その AI のときだけ入ります。

| kind | 置き場（実体のファイル。symlink は使わない＝Windows 対応） |
| -- | -- |
| hook | 置かない（`looptrack hook <名前>` を配線する） |
| rules | `.claude/rules/looptrack-loop/`。session-start-rules / user-prompt-rules はここを読む（`LOOPTRACK_LOOP_RULES_DIR` で変更可） |
| skill | `.claude/skills/iterate/`・`.claude/skills/session-handoff/`（SKILL.md に init の印が入っている） |

**hook の約束**: プロジェクトのルートは `CLAUDE_PROJECT_DIR` → `git rev-parse --show-toplevel` → `pwd` の順に決めます。
状態ファイル（`task-mode.d/`・`handoff-pending.d/`・`session-scope/`）は `<ルート>/.claude/` に置きます
（`LOOPTRACK_LOOP_STATE_DIR` で変更可。`CLAUDE_PROJECT_DIR` が無く `CODEX_THREAD_ID` があれば `.codex/`）。
判定できないときは通します（fail-open）。
**Claude Code 以外から `.claude/settings.json` の配線で起動されたら、何もしません**（`hookio.ForeignHost`）。
プロジェクトごとの調整は、すべて `LOOPTRACK_LOOP_*` の環境変数（settings.json の `env`）で行います。

| 環境変数 | 使う hook | 既定 |
| -- | -- | -- |
| `LOOPTRACK_LOOP_STATE_DIR` | task-mode 2 本・handoff 2 本・scope | `<ルート>/.claude` |
| `LOOPTRACK_LOOP_RULES_DIR` / `LOOPTRACK_LOOP_AGENT` | session-start-rules / user-prompt-rules | `.claude/rules/looptrack-loop` / 配線の `--agent` |
| `LOOPTRACK_LOOP_TASK_MODE_EXEC_RE` / `_INVEST_RE` / `_EXEC_NOTE` / `_ALLOW_DIRS` | task-mode 2 本 | なし |
| `LOOPTRACK_LOOP_HANDOFF_BACKEND` / `_FILE` / `_MEMORY_DIR` / `_CHECK_CMD` | handoff 2 本・memories | `file` / `.claude/memories/handoff.md` |
| `LOOPTRACK_LOOP_HANDOFF_WRITER` | handoff 2 本 | `session`（本文に印があればその書き手で判定。印が無いファイルと `mtime` は更新時刻だけで判定） |
| `LOOPTRACK_LOOP_MEMORIES_DIR` / `_MAX_CHARS` | memories | `.claude/memories` / `6000`（0 以下なら全文を注入） |
| `LOOPTRACK_LOOP_GATES_DIR` / `_STAGES` | gates・iteration | `.` / `build lint test` |
| `LOOPTRACK_LOOP_ISSUE_CLI` | iteration | 実行中の `looptrack`（`looptrack issue`） |
| `LOOPTRACK_LOOP_SCOPE_WARN` | scope | 250000 |
| `LOOPTRACK_LOOP_RUNAWAY_ALLOW` / `_THRESHOLD_MIN` / `_LOOP_THRESHOLD_MIN` | runaway | なし・30・10 |
| `LOOPTRACK_LOOP_WAITLOOP_ALLOW` | wait-loop-guard | なし |

**検証**: `go test ./internal/client/hook/loop/`（表駆動のテスト）。
導入の後の自己診断（init の verify）は、次の 4 つを確かめます。配線が manifest どおりか・looptrack が起動できるか・
置いた rules の印の節を session-start-rules が注入するか・入口が looptrack を呼べるか。
失敗したら書いたものを元に戻して止まります（`--no-verify` で省けます）。

## Codex での対応

| kit の項目 | Codex |
| -- | -- |
| hook（SessionStart・UserPromptSubmit・PreToolUse・PostToolUse・Stop・SessionEnd） | `.codex/hooks.json` に同じイベントで配線する。**Stop のブロックの挙動は実物で未確認**なので、Stop 系（引き継ぎ鮮度・runaway）は「注入のみ」で入れる |
| `pre-tool-subagent-bound`（PreToolUse） | **配線しない**: ツールの入力を書き換える `updatedInput` 相当が Codex にあるかは**未確認**。したがって **Codex では上限の規律はサブエージェントへ自動では降りない**。親が `background-process.md` の 3 行を指示文に書き写すこと |
| rules | Codex に rules の仕組みは無い → `AGENTS.md` に `<!-- looptrack:loop:begin -->` 〜 `<!-- looptrack:loop:end -->` で節として追記（init が管理。手で直した部分は触らない） |
| skills | 無い → 案内文だけ（`/iterate` の手順は AGENTS.md の節に短く入れる） |
| `task-mode.d` 等の状態ファイル | `.claude/` ではなく `.codex/` 配下に置く（`CODEX_THREAD_ID` をセッション ID に使う） |

## Copilot での対応

対象は GitHub Copilot（VS Code のエージェントモード・Copilot CLI）です。
「確度」の意味は、文書 = 公式文書に記載・ソース = 製品のソースで確認・実物 = Copilot CLI 1.0.86 で実測です。VS Code は未確認です。

| 前提 | 確度・出典 |
| -- | -- |
| hook は `.github/hooks/*.json` を VS Code と Copilot CLI の両方が読む（CLI は信頼したフォルダだけ。VS Code は Preview の機能）。形は `{"version": 1, "hooks": {<イベント>: [{"type": "command", "command", "timeoutSec", "matcher"?}]}}`（matcher ごとのグループは無い） | 文書: [Copilot の hooks configuration](https://docs.github.com/en/copilot/reference/hooks-configuration)・[VS Code の hooks](https://code.visualstudio.com/docs/copilot/customization/hooks) |
| イベント名を PascalCase（`SessionStart` `UserPromptSubmit` `PreToolUse` `PostToolUse` `Stop`）で書くと、CLI は「VS Code 互換の形式」になり、入力は snake_case（`session_id`・`hook_event_name`・`tool_name`・`tool_input`・`transcript_path`）。VS Code も同じ形 → kit の hook は入力の読み方を変えずに動く。CLI の PostToolUse の結果は `tool_result`（`text_result_for_llm`）で渡る | 文書: 同上 |
| **出力の形が製品で違う。** Copilot CLI はトップレベル（SessionStart・PostToolUse の `additionalContext`、PreToolUse の `permissionDecision` / `permissionDecisionReason`、Stop の `decision` / `reason`）。VS Code は `hookSpecificOutput` の中（SessionStart の `additionalContext`、PreToolUse の `permissionDecision`、**Stop の `decision` / `reason` も**。PostToolUse の `decision` だけトップレベル）。VS Code の Stop はトップレベルの `decision` を読まない | CLI: 文書（[Copilot の hooks configuration](https://docs.github.com/en/copilot/reference/hooks-configuration)）。VS Code: 文書（[VS Code の hooks reference](https://code.visualstudio.com/docs/agents/reference/hooks-reference)）とソース |
| → kit の hook は `LOOPTRACK_LOOP_AGENT=copilot`（init が前置）のとき、**両方の場所に同じ値を置く**（`additionalContext`・`permissionDecision(Reason)` をトップレベルにも写し、`decision` を `hookSpecificOutput` にも写す）。`looptrack issue summary --hook-json` も両方に置く。Claude Code・Codex の出力は変えない | 実装（テストで確かめる）。実物で両方が読むかは未確認 |
| 終了コード: CLI の SessionStart・PostToolUse・Stop は 0 以外でも続行（fail-open）。**CLI の PreToolUse は 2 で拒否、それ以外の 0 以外も拒否（fail closed）**。VS Code は 2 を「ブロックするエラー」として扱う | 文書: [Copilot の hooks configuration](https://docs.github.com/en/copilot/reference/hooks-configuration) |
| UserPromptSubmit: **CLI はトップレベルの `additionalContext` を `<system_reminder>` として利用者の指示に付ける**（公式文書の記述と違う）。同じイベントの複数の hook の `additionalContext` は最後の 1 つしか渡らないので、init は合成の hook（`looptrack hook user-prompt --parts …`）1 本で配線する | CLI: 実物。VS Code: 文書のみ |
| **VS Code は matcher を無視する**（全ツールで呼ぶ）。ツール名は VS Code 独自（シェルは `run_in_terminal`、編集は `create_file`・`replace_string_in_file` 等、入力は camelCase の `filePath`）。CLI の matcher は `toolName` に `^(?:…)$` で当て、**MCP のツールは `<サーバ>-<ツール>`**（`mcp__.*` に合わない） | 文書: 同上。VS Code の MCP のツール名は推論 |
| → hook は matcher に頼らず、**中でツール名を見て絞る**: `post-work-complete-handoff-mark` はシェル（`Bash`・`bash`・`run_in_terminal`）と MCP の `set_status`（3 通りの綴り）だけを見る。トークン計測の hook（`--client copilot`）も中で MCP のツール名を見る | 実装（テストで確かめる） |
| **Windows**: CLI は `powershell` のフィールド（PowerShell 7 以上）、VS Code は `windows` のフィールド（Windows PowerShell 5.1）で実行する。init は `powershell` / `windows` にも `command` と同じ `looptrack hook …` を置く（環境変数の前置は PowerShell の形。looptrack は Windows でも動く） | CLI: 文書。VS Code の 5.1: ソース。Windows での実物は未確認 |
| VS Code に SessionEnd は無い。Copilot CLI はシェルに `COPILOT_AGENT_SESSION_ID` を渡し、VS Code のエージェント用ターミナルには `AI_AGENT=github_copilot_vscode_agent` / `COPILOT_AGENT=1` が付く | 文書・ソース |

init が書く 1 件の形（`.github/hooks/looptrack.json`）は次のとおりです:
`{"type": "command", "command": <bash>, "bash": <bash>, "powershell": <Windows 用>, "windows": <Windows 用>, "timeoutSec": …}`。
`command` と `bash` は同じものです（VS Code は `command` を使い、CLI はどちらでも同じものを実行します）。

| kit の項目 | Copilot |
| -- | -- |
| SessionStart の 4 本（rules・memories・iteration・worktrees） | 配線（`SessionStart`）。状態の置き場は `<ルート>/.claude/`（セッションごとのファイルは `session_id` で分かれる）。**CLI は同じイベントの複数の hook の `additionalContext` のうち最後の 1 つしか AI に渡さない**ので、core の summary と合わせて `looptrack hook session-start --parts summary,session-start-rules,session-start-memories,session-start-iteration,session-start-worktrees` の 1 本にまとめて配線する |
| UserPromptSubmit の 3 本（rules・task-mode・scope） | 配線。同じ理由で `looptrack hook user-prompt --parts user-prompt-rules,user-prompt-task-mode,session-scope-guard` の 1 本にまとめる。Stop の差し戻しの理由（次の利用者のメッセージとして渡る）は先頭の「[Stop hook の差し戻し]」で見分け、task-mode・scope は判定しない |
| `pre-edit-task-mode-guard`（PreToolUse） | 配線（matcher なし）。ガードはツールの種類で読み取り・シェル・MCP を除き、`path`・`filePath`・apply_patch の本文のファイルを見て、確認モード中のプロジェクト内の編集を `permissionDecision: deny` で止める（実物で拒否を確認）。looptrack は常に exit 0 なので CLI の fail closed に当たらない |
| `pre-tool-scope-guard`（PreToolUse） | **null**: VS Code は matcher を無視し、MCP のツール名の形が違う。確かめていない |
| `post-work-complete-handoff-mark`（PostToolUse） | 配線（matcher なし）。hook の中でツール名を見る。CLI の結果は `tool_result.text_result_for_llm` からコミットの成否を見る。VS Code の `run_in_terminal` の結果の形は未確認（無ければ成否を見ずに積む） |
| `stop-handoff-freshness`（Stop） | 配線・**差し戻す**。CLI は Stop の `systemMessage` を捨てるが `decision: block` は効く（`reason` を次の利用者のメッセージとして渡して続け、2 回目は `stop_hook_active: true`）。理由の先頭に「[Stop hook の差し戻し]」を付け、同じ積み残しでは 1 回だけ差し戻す |
| `stop-tool-markup-guard` | **null**: 書式ミスは Claude に固有 |
| `stop-runaway-background-process` | **null**: 本体の特定が「祖先で claude / codex を名乗るプロセス」。Copilot の祖先の形は未確認で、見つからなければ何もしない |
| `subagent-stop-runaway-background-process`（SubagentStop） | **null**: 上と同じ理由 |
| `pre-tool-wait-loop-guard`（PreToolUse） | 配線（matcher なし）。判定はコマンドの構文だけなので AI に依らない。CLI の PreToolUse は fail closed だが looptrack は常に exit 0 |
| `pre-tool-subagent-bound`（PreToolUse） | **null**: ツールの入力を書き換える `updatedInput` 相当が Copilot にあるかは**未確認**。確かめられるまで配線しない（配線しても何も起きないのではなく、何が起きるか分からないため）。したがって **Copilot では上限の規律はサブエージェントへ自動では降りない**。親が `background-process.md` の 3 行を指示文に書き写すこと |
| rules | `.claude/rules` の仕組みは無い → Codex と同じく AGENTS.md の loop 節（Copilot CLI・VS Code とも AGENTS.md を読む）。`.github/instructions/*.instructions.md` は使わない（AGENTS.md に一本化） |
| skills | 無い → 案内文だけ（AGENTS.md の loop 節に description の 1 行と手順のパス） |
| トークン計測（core） | 配線（`PostToolUse`・`Stop`・`SessionEnd`（CLI だけ）に `looptrack hook usage --agent copilot --event <イベント>`。matcher なし＝hook の中で MCP のツール名を絞る）。OTel の出力先は `$COPILOT_HOME/otel/` の下に限る（Copilot CLI は出力先を hook に渡さない）。OpenTelemetry のファイル出力を有効にした利用者だけ送る（手順は [docs/AI-GUIDE.md](../docs/AI-GUIDE.md)） |
| 鮮度ガード（core） | 配線しない（Claude Code だけ） |

**案内文**: Copilot CLI は AGENTS.md・CLAUDE.md・`.github/copilot-instructions.md` をすべて読んで合成します。
VS Code も AGENTS.md と CLAUDE.md を既定で読みます（文書）。CLAUDE.md の管理節は Claude Code 向け
（`.claude/settings.json` の `env` を前提にした CLI の案内）なので、冒頭に
「Claude Code 向け。GitHub Copilot・Codex は AGENTS.md の節に従う」と書きます。

**`.claude/settings.json` も読まれます**: VS Code と Copilot CLI は、`.claude/settings.json`・
`.claude/settings.local.json` の hooks も読みます（両方の文書に記載）。
Copilot CLI はその hook に `CLAUDE_PROJECT_DIR`・`COPILOT_CLI=1`・`COPILOT_PROJECT_DIR` を渡します
（`CLAUDECODE` は無く、settings の `env` も渡りません。実物で確認）。
そのため hook の判定では、`COPILOT_PROJECT_DIR`・`COPILOT_CLI` を `CLAUDE_PROJECT_DIR` より先に見ます（上の「hook の約束」）。
init が書くルートは `${CLAUDE_PROJECT_DIR:-$(git rev-parse --show-toplevel 2>/dev/null || pwd)}` です。
Claude Code 以外から起動された hook は、何も出さずに exit 0 で終わります。
init は、Claude Code と一緒に入れたときと `.claude/settings.json` に hooks があるときに知らせます。
VS Code では設定 `chat.hookFilesLocations` で外せます。VS Code の実物での動きは未確認です。
