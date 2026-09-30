# AI 向けガイド — イシュー管理サーバを使う

**対象読者**: Looptrack のサーバに載せたプロジェクトのどれかで作業している AI（Claude Code など）。
イシュー管理を回すのに要ることは、このページ 1 枚にまとめてあります。プロジェクト固有の追加ルールは
[docs/projects/](projects/)`<slug>.md` のほうです。

---

## 0. プロジェクト

どのプロジェクトが載っているかは、画面のプロジェクト選択（§6）か MCP の `list_projects` を見れば分かります
（出るのは自分が参加しているものだけ）。プロジェクトごとに slug・ID の接頭辞・運用文書を持っています。

| slug | プロジェクト | ID | 運用文書 |
| -- | -- | -- | -- |
| `example` | 架空の業務アプリ（プロジェクト別ルールの全種類の例） | `EX-0001` | [projects/example.md](projects/example.md) |

運用文書の置き場と書き方は [projects/](projects/) と [templates/project-rules.md](templates/project-rules.md) を見てください。
いまどこにつながっている？ それは `looptrack issue config` が答えます。

---

## 1. 仕組み（30 秒）

**Looptrack は、AI コーディングエージェントの外部記憶となり、ループエンジニアリングを実現するイシュー管理ツールです。** コンテキストに収まらない作業計画・判断・経緯はイシューに置いておき、次のセッションはそこから再開する。これが基本の形。ここのイシューはプロジェクト本来の課題一覧とは別物で、AI が分解した作業単位です。
**このシステムを導入すると、ループエンジニアリングの基盤が整います。** イシューを軸に、「起票 → 着手（next）→ 作業 → 検証 → クローズ → 次へ」を AI が回していきます。規則（採番・append-only・クローズ済み不変・プロジェクト別ルール）を強制するのはサーバの役目で、各段階のトークン消費も記録され、分析レポートにまとめられます。
入れ子の 3 つのループ（Andrew Ng の整理）に当てはめると、こうなります。

| ループ | 周期 | 回す人 | このシステムでの形 |
| -- | -- | -- | -- |
| ① AI の作業ループ | 数分 | AI | `next` → 作業 → `comment` → 検証 → `close` → 次の `next` を自走し、本文の「## 検証コマンド」を `verify` が手元で実行して記録し、閉じる前に機械が判定する（§2） |
| ② 人の判断のループ | 数十分〜数時間 | 人 | 判断が要るものは In Review に集まり（`summary` に滞留・48 時間超に印）、AI が次の `next` の前に持ちかけて返答を `判断:` / `差し戻し:` のコメントで残す（§2） |
| ③ 外からの反応のループ | 数時間〜数週 | 利用者・テスター | 聞いた反応を AI が `フィードバック:` 付きのコメントでイシューに戻し、未応答のものが `summary` の「外からの反応」と `list --has-feedback` に並ぶ（§2） |

- **core だけ（最小ループ）**（`looptrack issue init` が必ず入れます。§9）: セッションの冒頭に 3 層の要約（いまの周・人の判断待ち・外からの反応）が入ります。着手は `next`。参照したイシューを更新しないまま終えようとすると、やり直しを求められます。消費は段階別に貯まっていきます。
- **loop を足すと**（`init --loop`）: 見張りが増えます。「確認して」と頼まれたら編集が止まり、ツール呼び出しの書式ミスにはやり直しがかかる。作業が終わるたびに引き継ぎ記憶の更新を求められますし、文脈の大きさや放置された背景プロセスも見張りの対象です。別のリポジトリ・プロジェクトへの変更は利用者に確認が行きます。逸脱の検知を hook に任せるぶん、人は判断に集中できるわけです。
  loop を入れるかどうかは利用者が決めること。AI が勝手に入れてはいけません。
  内訳と、各項目がどのループ（① / ② / ③）に効くかは、[kit/README.ja.md](../kit/README.ja.md) の「効くループ」列にまとめてあります。

- イシューは **サーバ（https://example.com/looptrack/ ・MySQL）に保存**されます。元データは DB。
- 操作は `looptrack issue <サブコマンド>`（Go の CLI）で。skill は `/issue` です。
- API モードにするには、プロジェクトの `.claude/settings.json` の `env` にサーバの URL とプロジェクトを書きます。

  ```json
  "env": { "LOOPTRACK_API_URL": "https://example.com/looptrack", "LOOPTRACK_PROJECT": "<slug>" }
  ```

  この値は Claude Code が Bash ツールと hook に渡します。**書いていないと API モードにならない**ので、ここは必ず。
- **採番・append-only・クローズ済みの不変・プロジェクト別ルールはサーバが強制します。** 違反したら
  `エラー: <次に何をすべきか>` と exit 1 が返ってくる。hook の文字列解析には頼っていません。
- **git の操作は要りません。** pull / commit / push も、`counter` の衝突も、気にしなくて大丈夫です。
- **導入先に置くものは `looptrack` の実行ファイル 1 つだけです**（別の処理系も bash も要りません）。
  置き場は `~/.local/bin`（Windows は `%LOCALAPPDATA%\Programs\looptrack`）。PATH に無ければ、`looptrack doctor` の案内どおりに進めてください。

### 初回だけ: ログインする

1. 端末で次を実行します。AI が打ってもかまいません。ただし、その前に利用者の承認を。

   ```bash
   looptrack issue login --browser --url https://example.com/looptrack
   ```

   ブラウザが開いたら、ログイン（ID・パスワード + 二段階認証）と承認（「looptrack（ホスト名）」に許可）をするのは**利用者**です。
   開かなければ、表示された URL を開いてください。待ち受けは最長 5 分。AI が実行するなら、コマンドのタイムアウトを 5 分より長くしておきます。
   トークンの保存先は `~/.config/looptrack/credentials.json`（600。`XDG_CONFIG_HOME` があればその下）。**Windows では `%APPDATA%\looptrack\credentials.json`** です。
   保存したトークンは全プロジェクトで共有され、**以後は期限が近づくと自動で更新されます**。
   ログインのやり直しが要るのは 2 つの場合だけ。90 日のあいだ一度も使わなかったときと、アカウント画面で失効させたときです。
   Windows には 600 が無いので、かわりにファイルとフォルダの ACL を「本人だけ（親からの継承なし）」にして書きます。本人・SYSTEM・Administrators
   以外に読み取りを許す ACL が付いていたら、読み込みは拒否。本人の行だけになっているかは `icacls "%APPDATA%\looptrack\credentials.json"` で確かめられます。
2. 確認: `looptrack issue config` を実行します（モード・利用者・プロジェクトと自分の権限・トークンの取得元が出ます）。

**Windows で looptrack を使うとき**:

- Claude Code の Bash ツールと hook は、Git Bash から looptrack を起動します。出力は UTF-8、改行は LF。
- 気をつけたいのは PowerShell。出力を変数やファイルに受ける（`$x = looptrack issue list`・`looptrack … > out.txt`）と文字化けします。PowerShell が既定の
  コードページ（日本語版は cp932）で読んでしまうからです。先に `[Console]::OutputEncoding = [Text.Encoding]::UTF8` を実行しておいてください
  （画面に出すだけなら要りません）。PowerShell 5.1 の `>` は UTF-16 で書くものの、`usage ledger add --from-report` と `report pdf` はそれも読めます。
- 作業コピー（`edit` → `.claude/.looptrack-work/<ID>.md`）をメモ帳などで保存して CRLF や BOM が付いた？ 大丈夫、`push` が元の形（LF・BOM なし）に戻します。
  ただし ANSI（Shift_JIS）で保存したものは拒否されるので、UTF-8 で保存し直してください。
- `verify` の検証コマンドが動くのは Git Bash の `bash -c` です（WSL の bash や PowerShell では動かしません）。cmd.exe の組み込みコマンドなどが
  cp932 で出した出力は、UTF-8 に読み直してから送ります。時間切れになったら、Job Object で子孫のプロセスまで止めます。

ブラウザの無い環境（ssh 先など）ならどうするか。https://example.com/looptrack/account で用途と有効日数を選んでトークンを発行し、
それを `looptrack issue login --url https://example.com/looptrack` に貼り付けます。これは**利用者本人が行います**。トークンがその画面に表示されるのは 1 回きりです。

**AI はトークンを扱いません。** `login --browser` でも、トークンが通るのは CLI とサーバの間だけ。会話に出るのは「ログイン: <利用者名>」の 1 行です。
`エラー: …（… login --browser でログインし直してください）` が出たら、利用者の承認を得てから `login --browser` をやり直してください。
アカウントの作成とプロジェクトの権限付与は管理者の仕事です。場所は https://example.com/looptrack/admin/users （利用者側から）か https://example.com/looptrack/admin/projects （プロジェクト側から）。ちなみに管理者でも、既定の一覧に出るのは参加しているプロジェクトだけです（§6）。

---

## 2. 作業を始める前に

```bash
looptrack issue guide       # 使い方とルール（共通規則 + このプロジェクトのルール + 運用文書）を 1 回で読む
looptrack issue summary     # 3 層の要約: ① いまの周（進行中・着手可能）② 人の判断待ち（In Review と滞留）③ 外からの反応（未応答のフィードバック）
looptrack issue list --status "In Progress"
looptrack issue list --status "In Review"   # 人の判断待ち
looptrack issue list --has-feedback         # 未応答のフィードバックがあるもの（クローズ済みも含む）
looptrack issue ready
```

`summary` はセッション開始の hook でも使っています。2 秒で打ち切り、失敗しても何も出さずに通す作り。

**進行中の一覧には「※ … は別のセッションが着手しています」という注記が付くことがあります。** 同じ利用者の別のセッション（別の端末・別のウィンドウ・並行して動く AI）が In Progress にしたイシューのことです。
**これは取りに行かないでください。**
引き取りたいなら、先に一声かけて、相手が降りてから着手します。添えてある経過時間が長いものは、渡したまま放置されている可能性もある。そのときも、まず確かめてから。`next` も同じ判断をするので、別のセッションが着手したものは選びません。なお、セッション ID を送らない経路（画面・Copilot の VS Code）では、この注記は出ません。
`guide` の中身は MCP の `guide` ツールと同じです（設計は [server/DESIGN.md](server/DESIGN.md)）。

### ループ運用（next → 作業 → 検証 → close）

```bash
looptrack issue next --dry-run   # 次に着手するもの（状態は変えない）
looptrack issue next             # 着手: 全文・受け入れ条件・関連（parent / blocked_by / traces / 子）を表示
#   … 作業。分かった時点で comment …
looptrack issue verify <ID>      # 本文に「## 検証コマンド」節があれば、手元で実行して結果を記録する
looptrack issue close <ID> --comment "受け入れ条件の検証結果"
looptrack issue next             # 次の周
```

| `next` の規則 | 内容 |
| -- | -- |
| 着手中を優先 | **担当が自分**の In Progress があれば、状態を変えずにそれを返す。セッションがあれば、自分の別のセッションが着手したものは除く（並行して動く別の AI の着手中は取らない）。**束ね（epic・要件など未クローズの子があるもの）と `--type` の対象外（既定は epic）は着手中として扱わず**、候補へ進む。束ねが In Progress なら、その子孫の候補を先に選ぶ |
| 候補 | 着手可能のうち、状態が Todo・epic 以外（`--type` で指定可）・未クローズの子が無い・**担当が自分か未設定**のもの。優先度順（同順位は ID 昇順） |
| 担当 | 着手すると担当が自分になる（未設定のとき）。`--assignee <login>` で着手と同時に別の人を担当にできる |
| ルール | In Progress にできない候補（プロジェクト別ルールに触れるもの）は見送って次を見る。着手前のコメントが要るプロジェクト（ルール `require_comment_before`）は `next --comment "着手。方針: …"` |
| 候補なし | 「着手可能なイシューはありません」。`ready` / `summary` を見て利用者に相談する |

**要件の検証と close**（設計は [server/DESIGN.md](server/DESIGN.md)）: 閉じたイシューが traces で指している要件で、その下位
（その要件を traces で指すイシュー）が全部閉じた（Done / Canceled で、Done が 1 件以上）とします。そのとき、`close` / `set_status` の応答の末尾に、
その要件の検証と close を促す行が要件ごとに 1 行出ます。中身は `looptrack issue show <要件ID>` と close のコマンド。**目印は文面ではなくこのコマンドです**（文面は利用者の言語で変わります）。
次の `next` の前に、要件の受け入れ条件を確かめる。満たしていれば `close <要件ID> --comment "受け入れ条件の検証結果"` で閉じてください。取りこぼすと、`matrix` の警告節と `summary` の ① に並びます。
下位が全部 Canceled の要件は対象外。取り下げるか、下位を起票し直すか――それは利用者と決めます。In Review の要件も対象外です（こちらは ② に出ます）。

**検証コマンド**（設計は [server/DESIGN.md](server/DESIGN.md)）: 本文の `## 検証コマンド` 節に書いたコマンドは、`verify <ID>` が実行します。
対象になるのは、fenced code block の各行か、行全体がインラインコードの箇条書き。git のルート（`CLAUDE_PROJECT_DIR`）で順に `bash -c` で実行し、途中で失敗しても最後まで走らせます。
結果はまとめて 1 回でサーバに記録（コメントとイベント。出力はマスクして末尾 4,000 バイト）。
実は、検証コマンドには `LOOPTRACK_` で始まる環境変数を渡していません。渡すのは `LOOPTRACK_VERIFY_ID` だけ。検証コマンドから CLI を起動して、別のサーバへ書き込んでしまう事故を防ぐためです。
上限は 1 コマンド 600 秒（`--timeout`）、全体 1,800 秒（`--total-timeout`。超えた残りは skipped）。
終了コードは全成功 0・失敗あり 1・節なし 2（何も記録しない）。`--list` なら実行せずに一覧を、`--last` なら直近の記録を出力つきで出します。
本文を直すと、記録は無効になります（`verify.require_on_close` のプロジェクトでは、閉じる前にやり直しを）。MCP の `verify_issue` は一覧を返すだけで、実行まではしません。
CLI が使えない環境なら、手元で実行した結果を MCP の `report_verify` で送れます（§7）。記録には「MCP の自己申告」の印が付くものの、規則の判定ではちゃんと数えます。

**人の判断待ち**（② のループ。全文は `looptrack issue guide` の「作業の進め方」）: 仕様の解釈・見た目・方針の選択・確かめてほしいもの、
こうした利用者の判断が要るものは Done にしないでください。`status <ID> "In Review" --comment "判断してほしい点: …"` にします。
In Review があったら、次の `next` の前に利用者に示す（何を作ったか・検証結果・判断してほしい点）。
返答はコメントで残します。先頭が `判断:` なら `close <ID> --comment "判断: …"`、
`差し戻し:` なら `status <ID> Todo --comment "差し戻し: …"`。利用者がいないときは、In Review のまま次の `next` へ進んでかまいません。
In Review のものは `summary` の「② 人の判断待ち」に滞留として出て、48 時間を超えると `[48h超]` の印が付きます。MCP なら prompt `review` です。

**外からの反応**（③ のループ）: 参加者・テスター・利用者の反応を利用者から聞いたら、該当するイシューに先頭 `フィードバック:` でコメントします。
形は `フィードバック: 〈誰から・いつ・どの場面で〉 〈内容〉`。該当するイシューが無い？ なら起票してから書きます。
「未応答」とは、そのコメントより後に、先頭が `フィードバック:` でないコメントも状態変更も無いもののこと。`summary` の「③ 外からの反応」・`list --has-feedback`・ボードの絞り込み「未応答の反応」に出てきます。
未応答のものについては、次の `next` の前に利用者と対応（方針のコメント・起票・状態変更）を決めてください。応答は先頭語なしのコメントで書きます。
フィードバックもふつうのコメントなので、書けるのは editor 以上だけ（viewer は登録できません）。

**英語の別名**（設計は [server/DESIGN.md](server/DESIGN.md)）: 意味を読み取る決まった語は、英語でも書けます。設定は不要で、日本語と混ぜても平気です。
見出しの `## Acceptance criteria`（= `## 受け入れ条件`）と `## Verify commands`（= `## 検証コマンド`）は、英字の大小を問いません。
コメントの先頭語 `Feedback:`・`Decision:`・`Changes requested:`（= `フィードバック:`・`判断:`・`差し戻し:`）も大小は問わない。けど、コロンは半角だけです。
判定はサーバ（next・verify の一覧・`verify.require_on_close`・未応答のフィードバック）がするので、どの経路でも結果は同じ。
loop の確認モードも、英語の依頼（`check`・`investigate`・`review` … / `implement`・`fix`・`apply` …。単語単位）で切り替わります。
日本語の依頼語があれば、そちらが優先。雛形は、起票した利用者の言語で入ります。

### 担当者（複数の利用者・セッションで同じイシューを進めない）

イシューには担当者が 1 人付きます（`show` の frontmatter に `assignee: <login>`。担当があると一覧に `ASSIGNEE` 列が出ます）。**サーバが強制する規則**はこれ。

| 場面 | 結果 |
| -- | -- |
| In Progress にする（`status` / `next` / In Progress で `new`）で担当が未設定 | 自分が担当になる |
| **他の人が担当**のイシューを In Progress にする・本文を直す（`push`）・担当を替える / 外す | **拒否（422）**。メッセージに担当者が出る。**勝手に `--override` を付けない**。まず担当者に確認する（`comment` で依頼）。利用者が指示したときだけ `--override "理由"`（MCP は `override_reason`）。In Progress にする・`assign` は通すと担当が自分に替わる（引き継ぎ）。本文の編集（`push` / `update_issue`）は担当を替えずに通る。どちらも理由が記録に残る |
| コメント・閲覧・In Review / Done などへの状態変更 | 担当に関係なくできる |
| 権限を外された担当（一覧で `login(!)`・画面で「権限なし」。無効化された利用者など。参加の解除・viewer への変更では管理者が代わりの担当者を決める） | 自動では外れない。`status <ID> "In Progress"` や `assign <ID> me` で理由なしに引き継げる |

```bash
looptrack issue list --assignee me              # 自分の担当だけ（- で未設定、login で他の人）
looptrack issue ready --assignee me
looptrack issue assign EX-0142 me             # 担当を自分に（login で他の人・- で解除）
looptrack issue assign EX-0142 tanaka --override "田中さんに引き継ぐ（利用者指示）"   # 他の人の担当を替える
looptrack issue new "…" --assignee me          # 起票と同時に担当を決める
looptrack issue status EX-0142 "In Review" --assignee sato   # 状態と同時に担当を替える（close / next も --assignee）
```

担当は画面（詳細の「担当」）からも変えられます（editor 以上）。変更は記録に残り、鮮度ガード（§9）の「更新」にも数えます。

`summary` の最後に「── トークンレポートの作成依頼（未完了 N 件） ──」が出ることがあります。利用者が画面の「レポート作成」で依頼したものです。
作り方は skill `token-report`（`.claude/skills/token-report/SKILL.md`）の手順どおり。流れは `usage report --request N --json` → 本文・PDF（手元に保存）→
`usage ledger add … --from-report`。最後の登録で依頼の番号が自動で付き、依頼は一覧から消えます。

---

## 3. 最小フロー

### 起票する（作業はここから始める。会話だけで進めない）

```bash
looptrack issue new "受注登録の入力項目を確定する" \
  --type requirement --priority P1 --labels "受注,クライアント確認待ち" --body "$(cat <<'EOF'
## 背景
…
EOF
)"
# → 作成: EX-0142 受注登録の入力項目を確定する
```

| オプション | 値 |
| -- | -- |
| `--type` | `requirement` / `design` / `task` / `bug` / `test` / `epic`（既定 `task`） |
| `--status` | `Backlog` / `Todo` / `In Progress` / `In Review` / `Done` / `Canceled`（既定 `Todo`） |
| `--priority` | `P0`（即対応）/ `P1` / `P2` / `P3`（既定 `P2`） |
| `--labels` | カンマ区切り |
| `--parent` | 親イシュー ID（epic 等） |
| `--blocked-by` | カンマ区切りのイシュー ID。**これが Done/Canceled になるまで `ready` に出ない** |
| `--traces` | **イシュー ID 専用**（後方トレース。§4） |
| `--refs` | **文書 ID 専用**（`FR-` / `NFR-` / `UC-` / `ISS-` / `DEC-`。§4） |

`--blocked-by` / `--traces` / `--refs` の区切りはカンマでも空白でも OK（ID は空白を含みません）。`--labels` はカンマだけ（ラベルは空白を含んでもかまわないので）。
MCP と API は違います。この 3 項目では、配列の 1 要素に ID を 1 つずつ入れる（`["NFR-SEC-003", "NFR-SEC-004"]`）。`"NFR-SEC-003 NFR-SEC-004"` のような空白区切りの要素は 400 で拒否されます。
`edit` の作業コピーでも `refs: [A, B]` とカンマで区切ります（`refs: [A B]` は push で止まります）。
| `--body` | 「内容」節に入れる本文。行頭の見出し `## 受け入れ条件` を含めると、テンプレートの受け入れ条件節は付かない（本文の節がそのまま受け入れ条件になる） |
| `--override` | 上書きできるプロジェクト別ルール（`usage` / `verify` のクローズ時の必須）を理由付きで通す。理由はサーバに記録される |
| `--assignee` | 担当者（`me` で自分・login）。省略すると未設定（`--status "In Progress"` なら自分）。§2「担当者」 |

### 本文を直す（背景 / 内容 / 受け入れ条件を埋める）

**イシューのファイルはローカルにはありません。** だから作業コピーを取り、Read / Edit で直して、それを反映します。

```bash
looptrack issue edit EX-0142   # → .claude/.looptrack-work/EX-0142.md（版番号つき）
#   Read / Edit で .claude/.looptrack-work/EX-0142.md を直す（frontmatter も本文も直せる）
looptrack issue push EX-0142   # 版番号つきで反映。作業コピーは消える
```

- **コメント節は編集できません**（append-only）。追記なら `comment` で。
- frontmatter の `assignee:` は本文からは変えられません（`assign` を使う）。**他の人が担当のイシューは push が拒否されます**（§2「担当者」）。
- 作業コピーを取った後で誰かが更新していたら？ `push` は競合で止まり、差分を表示して、最新版を
  `.claude/.looptrack-work/EX-0142.server.md` に保存します。差分を作業コピーへ取り込んでから、`push --rebase` してください。
- 作業コピーを取った後に付いたコメントは、サーバ側に残ります。消えません。
- `.claude/.looptrack-work/` は自動で git の対象外です（中に `.gitignore` を置くので）。

**受け入れ条件はテスト可能な形で書いてください。** 「速い」「使いやすい」のような曖昧な語は使わない。
ここがそのまま test イシューの根拠になるからです。

### 進める

```bash
looptrack issue status EX-0142 "In Progress"
looptrack issue comment EX-0142 "原因: 項目定義が旧様式のまま。対応方針: 2026 年版へ差し替え。"
looptrack issue status EX-0142 "In Progress" --comment "着手。方針: …"   # 状態変更と同時に記録
```

- **原因が分かった時点でコメントしてください。** 結論だけを後から書くのはやめてください。
- コメントは **append-only**。過去のコメントは書き換えません。
- **参照したイシューを更新しないままセッションを終えようとすると、Stop hook が止めます**（§9）。

### 閉じる

```bash
looptrack issue verify EX-0142 --list   # 本文の「## 検証コマンド」を実行せずに表示（直近の記録つき）
looptrack issue verify EX-0142          # 手元で実行して結果をイシューに記録（全成功 0・失敗あり 1・節なし 2）
looptrack issue close EX-0142 --comment "受け入れ条件を全て検証。証跡: docs/minutes/2026-08-24.md"
looptrack issue status EX-0142 "In Review" --comment "判断してほしい点: …"   # 利用者の判断が要るときは Done にしない（§2）
```

**受け入れ条件の検証結果をコメントに残してから閉じてください。** 本文に「## 検証コマンド」節があるなら、閉じる前に `verify` を通しておく。
プロジェクト別ルール `verify.require_on_close` のあるプロジェクトはもっと厳しくて、現在の本文に対する直近の `verify` が全件成功でない限り、close は拒否されます。
本文を直したら？ `verify` をやり直しです。

### 探す

```bash
looptrack issue show EX-0142
looptrack issue list --status Done          # クローズ済みも含めて探す
looptrack issue list --all --label "受注"      # ラベルで絞る
looptrack issue list --ref FR-STK-003       # 文書 ID から逆引き
looptrack issue list --sort updated         # 最近更新された順（--reverse で反転）
looptrack issue ready                       # 着手可能なものだけ
looptrack issue list --assignee me          # 自分が担当のものだけ（ready / export も --assignee）
looptrack issue list --has-feedback         # 未応答のフィードバック（先頭「フィードバック:」のコメント）があるもの。クローズ済みも含む
looptrack issue verify EX-0142 --last      # 直近の verify の記録（コマンドごとの出力の末尾つき）
looptrack issue index                       # ステータス別一覧（標準出力）
looptrack issue matrix                      # 要件→設計→実装→テストの対応表と警告（標準出力）
looptrack issue list --type bug --json      # hook・スクリプトからは --json（list / ready / show）
looptrack issue activity EX-0142 --since 1726500000 --json   # 指定時刻以降の更新の有無
looptrack issue export --xlsx 課題管理表.xlsx --all   # 課題管理表（Excel）に書き出す（絞り込みは list と同じ引数）
looptrack issue usage show EX-0142              # このイシューに使ったトークン（段階ごと）
looptrack issue usage report --since-last --json > r.json   # 前回のレポート以降の集計（--from 2026-09-01 --to 2026-09-30 で期間指定・--xlsx で数表）
looptrack issue usage ledger add "2026年9月" --from-report r.json --note report.pdf   # 作ったレポートを台帳に登録（取り消せない）
looptrack issue usage ledger list                # 台帳（次の --since-last の起点）
looptrack issue usage missing                    # トークン情報が付いていない自分の AI 操作と充足率（--all-users / --days / --json）
looptrack issue usage attach EX-0142            # 今の会話の累計をこのイシューに手動で付ける（回収）
looptrack issue usage requests                   # 画面から登録されたレポートの作成依頼（未完了。--all で完了も）
looptrack issue usage report --request 3 --json > r.json    # 依頼 #3 の期間で集計（ledger add --from-report r.json で依頼が完了）
```

`list` / `ready` の並びは、既定だと優先度順（同順位は ID 昇順）。`--sort` を使えば
`priority` / `id` / `updated` / `created` / `status` / `type` / `title` から選べます。

### 覚えておく挙動

| 項目 | 内容 |
| -- | -- |
| `new` の出力 | `作成: <ID> <タイトル>`（ファイルのパスは出ない） |
| `index` / `matrix` | ファイルを書かず、Markdown を標準出力に出す |
| 本文の編集 | `edit` → Read / Edit → `push`（ファイルを直接編集しない） |
| `--labels "a, b,"` | 最初から `[a, b]` に正規化される |
| 待ち時間 | 通常のコマンドは最大 30 秒（`LOOPTRACK_TIMEOUT` で変更）。`summary` は 2 秒 |
| `export --xlsx` | API モードだけ（サーバが整形する）。シェルに `LOOPTRACK_API_URL` が無いと、その環境変数を設定するよう促して **exit 1**（対訳表 `cli.err.export_api_only`） |
| `verify` | API モードだけ。本文の「## 検証コマンド」を手元（`CLAUDE_PROJECT_DIR`）で実行し、結果をコメントとイベントでサーバに記録する。シェルに `LOOPTRACK_API_URL` / `LOOPTRACK_PROJECT` が無いと、**何も実行・記録せず exit 2**（対訳表 `cli.err.verify_api_only`）。MCP の `verify_issue` は一覧と直近の記録を返すだけで実行しない（MCP の `report_verify` で送った記録には「MCP の自己申告」の印が付く。CLI の記録には付かない） |
| `list --has-feedback` | API モードだけ。未応答のフィードバックがあるイシューだけを出す（既定でクローズ済みも含む。末尾に件数の行）。判定はサーバのコメントの時刻と状態変更の記録を使う。シェルに `LOOPTRACK_API_URL` が無いと、`--has-feedback` 専用のエラーではなく `list` 自体がサーバの URL が無いという案内で **exit 1**（対訳表 `cli.no_api.missing_url`） |
| `summary` の見出し | API モードでは 3 層（`══ ① いまの周（AI の作業） ══`・`══ ② 人の判断待ち（…） ══`・`══ ③ 外からの反応（…） ══`）。該当が無い層も見出しと「該当なし」を出す（3 層に対応していない古いサーバでは従来の「進行中・レビュー待ち・着手可能」） |
| トークン計測 | `new` / `push` / `comment` / `status` / `close` / `next`（着手したとき）/ `verify`（と担当が変わった `assign`）の直後に、コーディング AI の会話記録から「会話の累計」をサーバへ送る。AI の下でない（人がターミナルから打った）ときは送らない。失敗しても操作は成功する。`LOOPTRACK_USAGE=0` で切れる。指示文（作業名）は既定で送らず、プロジェクト別ルール `usage.send_prompts: true` のプロジェクトだけ送る（利用者は `LOOPTRACK_USAGE_SEND_PROMPTS=0` で止められる）。手動で付けるときは `usage attach <ID>`。レポートの集計は `usage report`、作ったレポートの記録は `usage ledger add`。設計は [server/DESIGN.md](server/DESIGN.md) |
| トークン情報の付与漏れ | 送れなかったときだけ標準エラーに `looptrack issue usage attach <ID>` を示す 1 行が出る → **そのコマンドを実行する**。`summary`（SessionStart）の末尾にも同じコマンドが並ぶので、示されたイシューごとに実行して回収する（自分の AI 操作・直近 7 日。人がターミナルから打った操作は数えない）。**目印は文面ではなくこのコマンド**（文面は利用者の言語で変わるので、特定の言い回しで探さない） |
| クローズ時の必須化（プロジェクト別ルール `usage.require_on_close`） | その会話のトークン情報がイシューに 1 件も無いまま Done / Canceled にすると拒否される。CLI は拒否されたら自動で `usage attach` してから 1 回だけやり直すので、通常は意識しなくてよい。会話記録が無く付けられないときは拒否のメッセージが出る（利用者の指示があるときだけ `--override "理由"`）。ルールが無いプロジェクトでは警告だけ |

上の表の「対訳表 `…`」は、その文面の出どころ（Looptrack 本体の `internal/i18n/{ja,en}.json` の ID）を示しています。
**文面は利用者の言語で変わるので、特定の言い回しで探さないでください。** 目印は 2 つ。**終了コード**と、どの言語の文面にも
同じ形で入る `LOOPTRACK_API_URL` / `LOOPTRACK_PROJECT` という語です。

---

## 4. `traces` と `refs`（最頻出の誤り）

| フィールド | 入れるもの | 用途 |
| -- | -- | -- |
| `traces` | **実在するイシュー ID のみ**（`EX-0031` など） | `matrix` が要件 ↔ 設計/実装/テストを突合する |
| `refs` | **文書 ID**（`FR-` / `NFR-` / `UC-` / `ISS-` / `DEC-`） | `docs/` 側の要件・決定への参照。`list --ref` で逆引き |

> **やりがちな誤り**: `--traces FR-STK-003` のように、**文書 ID を traces に入れてしまう**こと。
> これは `matrix` で「存在しないイシューを traces に指しているイシュー」の警告になります。

- **design / task / test / bug には `--traces <要件イシューID>` を必ず付けてください。** 付け忘れると、トレーサビリティがそこで切れます。
- 文書 ID 体系を持たないプロジェクトなら **`refs` は使いません**。案件名や管理番号は `labels` へ（例 [projects/example.md](projects/example.md)）。

`matrix` の警告は 3 種類。**テスト未紐づけの要件** / **存在しないイシューを traces に指しているイシュー** / **下位がすべて完了したのに開いている要件**（API モードだけ。§2 の「要件の検証と close」）です。

---

## 5. やってはいけないこと

- **イシューを立てずに作業を始めること。** 経緯が残りません。不具合は**見つけたらすぐ**起票を。
- **待ちをコメントだけに書くこと。** `blocked_by` に入れておかないと、着手できるかどうかの判定が働かない。
- **受け入れ条件を検証せずに Done にすること。**
- **クローズ済みのイシューを編集すること。** どのみちサーバが拒否します。蒸し返したいなら新しく起票して、本文で参照してください。
- **ルールで拒否されたのを別の経路で回避すること**（MCP や `edit` で状態を書き換えるなど）。拒否されたら、メッセージの指示どおりに。
- **イシューの本文に本番データを貼ること。** マスクするか、ID だけにしてください。
- **アクセストークンを会話・コミット・イシュー・ログに書くこと。** `LOOPTRACK_TOKEN` を settings.json に書くのも同じくダメです。
- **コミットメッセージやソースのコメントにイシュー ID を書くこと。** 成果物に管理番号を残さないため。追跡はブランチ名でやります。

### シェル経由で本文・コメントを渡すときの必須作法

**クォート付きのヒアドキュメントで囲む。これは必須です。**

```bash
looptrack issue comment EX-0142 "$(cat <<'EOF'
`id` のようなバッククォート付き識別子もそのまま残る。
EOF
)"
```

なぜか。二重引用符の中では、**バッククォートと `$( )` をシェルが実行します**。識別子がコマンドの実行結果に
すり替わり、本文が壊れる（実際に起きました）。長い本文なら `edit` / `push` のほうが安全です。

### プロジェクト別ルール（サーバが強制）

| プロジェクト | ルール |
| -- | -- |
| `forbid_status`・`require_comment_before`・`done_requires_keyword`・`forbid_checkbox_pattern` を持つプロジェクト（例 [projects/example.md](projects/example.md)） | 指定の状態（例 `Backlog`）は使わない / コメント 0 件のまま指定の状態にしない（`--comment` を同時に付ければ通る） / `Done` にはコメントに指定の語（例「動作確認」）の記録（対象外なら「動作確認: 対象外（理由）」） / 受け入れ条件のチェックボックスに環境反映・マージの依頼を書かない |
| 任意（`verify.require_on_close`） | 「## 検証コマンド」節を持つイシューを Done にするとき、現在の本文に対する直近の `verify` が全件成功でなければ拒否（メッセージの `looptrack issue verify <ID>` を実行してから閉じる。利用者の明示指示があるときだけ `--override "理由"`）。節の無いイシューには効かない |

拒否されたら、**メッセージに書かれた次の行動**をとってください。ルールの定義は `deploy/rules/<slug>.json` にあります。

---

## 6. 閲覧（ユーザーに見せる）

| URL | 内容 |
| -- | -- |
| https://example.com/looptrack/ | プロジェクト選択画面 |
| https://example.com/looptrack/p/<slug>/ | ボード / 一覧 / トレース + 詳細 |
| https://example.com/looptrack/account | アカウント設定（トークン発行・失効・パスワード変更） |
| https://example.com/looptrack/admin/users | 利用者管理（管理者のみ） |
| https://example.com/looptrack/admin/projects | プロジェクト管理（管理者のみ。全プロジェクトの参加者と役割の付与・変更・解除、参加していないプロジェクトを開く） |

- ログインが要ります（ID・パスワード + 二段階認証）。権限の無いプロジェクトは見えません。
- **管理者でも、一覧（プロジェクト選択画面・`GET /api/v1/projects`・MCP の `list_projects`）に出るのは参加している（権限を付けた）
  プロジェクトだけです**。役割は、付けた権限の値。全プロジェクトを見たいときはプロジェクト管理の画面へ（API は `GET /api/v1/projects?all=1`。
  管理者以外は 403）。
  **管理者でも、書ける（起票・コメント・状態変更・担当の変更）のは editor / admin で参加しているプロジェクトだけです**。
  参加していないプロジェクトは、slug を指定すれば**読めます**。けど書けません（`/looptrack/p/<slug>/`・`LOOPTRACK_PROJECT=<slug> looptrack issue …`・MCP の `project` 引数）。
  このときの役割は viewer 扱い。書こうとすると 403 か「閲覧のみ」になり、`looptrack issue config` も「参加していません（閲覧のみ…）」と出します。viewer で参加している
  プロジェクトも同じ扱いです。書きたいなら、プロジェクト管理の画面で自分を editor / admin で参加させてください。
  なぜ参加を必要にしているのか。管理者権限だけで書けるようにすると、viewer の参加を外すだけで書けてしまう抜け道になるからです。管理画面（`/looptrack/admin/*`）の操作は、参加に関係なく管理者ができます。
- **担当の未クローズのイシューを持つ人の参加を外したり viewer に下げたりするときは、代わりの担当者を選ばないと変えられません。**
  プロジェクト管理と利用者管理、どちらの画面でも件数と選択欄が出ます。選べるのは、担当にできる参加者か「未設定」。付け替えは記録 `assign`
  に、理由「参加の解除」/「役割の変更（viewer）」として残ります。サーバ上の `looptrack member remove|set` なら `--reassign <login|->` を使います。
- **イシューの画面は閲覧専用です。** 起票や更新は `looptrack issue` か MCP で。
- 更新は 4 秒間隔で画面に反映されます。

---

## 7. MCP（任意）

Claude Code・Codex・GitHub Copilot などからは、MCP のツールとしても使えます。ツールは `setup` `guide` `next` `list_issues` `get_issue` `create_issue` `add_comment`
`set_status` `update_issue` `assign_issue` `ready_issues` `project_summary` `get_matrix` `issue_activity` `list_projects` `create_project`（管理者だけ） `verify_issue` `report_verify`、
トークンの `issue_usage` `usage_missing` `usage_report` `list_usage_ledger` `add_usage_ledger` `list_usage_requests`。
接続すると、「最初に `setup` で導入状態を確かめ、`guide` を読み、`next` からループを回す」という指示が AI に届きます。
prompt も 3 つ。ループの定型は `loop`、人の判断待ちと外からの反応を利用者に持ちかけるのは `review`、導入は `setup` です（Claude Code では `/mcp__looptrack__loop`・`/mcp__looptrack__review` など）。
`verify_issue` が返すのは、検証コマンドの一覧と直近の記録だけ。実行は手元のシェルの `looptrack issue verify <ID>` の役目です（サーバはコマンドを実行しません）。
CLI が使えない（MCP だけの）環境では、**`report_verify`** の出番です。一覧のコマンドを手元のシェルで同じ順に全部実行し、途中で失敗しても止めない。
そのうえで `id`・`body_sha256`（`verify_issue` の値）・`results` を送ります。結果にはコマンドごとに `command`（一覧の文字列そのまま）・`status`（ok / fail / timeout / skipped）・
`exit_code`・`duration_ms`・`output_tail` を入れる。検査の中身は CLI の記録と同じで、本文が変わっていれば `body_changed`、コマンドの順や件数が違えば拒否。viewer と節なしも受け付けません。
この記録は AI が書き写したもの。だから**「MCP の自己申告」の印**が付きます。印が出る場所は、コメントの見出し `検証コマンド（MCP の自己申告）: …`・`next` / `verify_issue` の「直近の verify」・`summary` の ② の行末。
人はレビューのとき、これで CLI の記録と見分けられます。`verify.require_on_close` では自己申告も数えます（全件成功なら閉じられる）。とはいえ、CLI が使えるなら CLI を使ってください。
`project_summary` は CLI の `summary` と同じ 3 層を返し、`list_issues` の `has_feedback` は `list --has-feedback` と同じ働きです。
ルール・楽観ロック・権限・記録も、CLI と変わりません。
違いの 1 つはトークンです。MCP はトークンを知り得ないので、変更の結果に
`looptrack issue usage attach <ID>` が示されたら、そのコマンドをシェルで実行してください（目印は文面ではなくこのコマンドです）。
トークン計測のフックが働いている利用者には、これは出ません。フックが数秒後に付けてくれるからです。Codex でのやり方は §7-1。GitHub Copilot では、OpenTelemetry のファイル出力を有効にした利用者の操作にだけ出ます（§7-3）。
担当者（§2「担当者」）を決めるのは `assign_issue` か、`create_issue` / `set_status` / `update_issue` / `next` の `assignee` 引数。他の人の担当を引き継ぐなら `override_reason` を使います（利用者の指示があるときだけ）。`list_issues` / `ready_issues` は `assignee` で絞れます。

- `setup` は、接続してきた AI（MCP の clientInfo）に合わせた導入コマンドと配布物の SHA-256 を返します。**MCP の接続設定だけの端末でも、この手順で CLI・フック・案内文が入ります**（[ADD-PROJECT.md](ADD-PROJECT.md) §4-2）。コマンドを実行する前には、利用者の承認を。トークンの登録・再起動・フックの承認は利用者がやります。
- 導入済みかどうかをサーバへ知らせるのは、SessionStart のフック（`looptrack issue summary --agent <種類>`）です。それが届くまでは、導入が未完了であることを知らせる注記がツール結果に付きます。配布スクリプトが古くなったときも、古いことを知らせる注記が付く（更新コマンドつき）。
  どちらも利用者の言語で出るので、文面で見分けてはいけません。見分けどころは「ツールの結果とは別の行で、`setup` ツールか更新のコマンドを示している」こと。手元の状態なら `looptrack issue installed --agent <種類>` で確かめられます。

```json
{ "mcpServers": { "looptrack": { "type": "http", "url": "https://example.com/looptrack/mcp",
  "headers": { "X-Looptrack-Project": "<slug>" } } } }
```

`project` 引数も `X-Looptrack-Project` も無い？ そのときは、参加しているプロジェクトが 1 つならそれを使います（管理者も参加している分で数えます。§6）。

認証はブラウザでの許可（OAuth。Claude Code の `/mcp` から）。許可したトークンはアカウント設定の一覧に出るので、
失効もそこから。**hook（SessionStart・鮮度ガード）は CLI を使う**ので、MCP だけで運用しないでください。
ちなみに鮮度ガードは、MCP での参照を「参照した」とは記録しません（更新のほうはサーバのイベントとして数えます）。

### 7-1. Codex では MCP のツールを主に使う

Codex の既定のサンドボックス（workspace-write・`network_access = false`）は、**AI が打つシェルのコマンドの外部通信を止めます**。
CLI（`looptrack issue`）はサーバと通信します。つまり Codex の AI が打つと、毎回「権限を上げて再実行 → 利用者の承認」になる（実物で確認済み）。
だから Codex では**イシューの操作を MCP のツールで行い**、CLI は MCP に無い操作だけに使います。`init` はサンドボックスのネットワークの許可を書きません（そこは利用者が自分で決めること）。
一方、MCP のツールの通信とフック（`.codex/hooks.json`）のコマンドの通信は、サンドボックスに止められません。フックの SessionStart の `summary` も、Stop のトークン計測（`looptrack hook usage`）も、承認なしにサーバへ届きます。

| ループの操作 | Codex でのやり方 |
| -- | -- |
| 規則を読む・着手・起票・コメント・状態変更・本文の編集 | MCP の `guide`・`next`・`create_issue`・`add_comment`・`set_status`・`get_issue` → `update_issue` |
| 検証コマンドの実行 | `verify_issue` で一覧 → 手元のシェルで順に全部実行（コマンド自体が通信しなければ承認は要らない）→ `report_verify`（「MCP の自己申告」の印が付く。規則では数える） |
| トークン情報（規則 usage: AI の Done に必要） | MCP で起票・コメント・状態変更をすると、PostToolUse のフック（`looptrack hook usage`・matcher `mcp__.*`）が数秒後に付ける。Done の前に `add_comment` があれば通る。拒否されたら数秒おいて 1 回やり直し、それでも拒否なら次の行 |
| **CLI が要る操作（権限を上げて実行・利用者の承認）** | `looptrack issue usage attach <ID>`（フックで付かなかったとき）・`looptrack issue login --browser`（初回だけ。フックが使うトークン）・`init` / `installed`（導入・更新。`setup` の手順） |
| MCP を接続していないとき | CLI の `next` / `comment` / `verify` / `close --comment` で回す（毎回承認）。`~/.codex/config.toml` への `[mcp_servers.looptrack]` の追加を勧める |

- **CLI の環境変数**: `init --agent codex` が、プロジェクトの `.codex/config.toml` に `[shell_environment_policy]` の `set = { LOOPTRACK_API_URL = …, LOOPTRACK_PROJECT = … }` を書きます。
  Codex が AI のシェルのコマンドに渡す環境変数で、読まれるのはプロジェクトを信頼したときだけ。既に `[shell_environment_policy]` があれば、書き換えずに案内だけ出します。
  `looptrack issue config` が**サーバの URL（`LOOPTRACK_API_URL`）が無いというエラーで止まったら**（exit 1。対訳表 `cli.no_api.missing_url`）、
  コマンドの前に `LOOPTRACK_API_URL=… LOOPTRACK_PROJECT=…` を付けます。ちゃんと渡っていれば、`モード: API（<サーバの URL>）` から始まる一覧を出して exit 0 で終わります。
- PostToolUse の matcher は `mcp__.*` に。Codex は、英数字・`_`・`|` だけの matcher を**完全一致**で比べるからです
  （[codex-rs/hooks/src/events/common.rs](https://github.com/openai/codex/blob/main/codex-rs/hooks/src/events/common.rs) の `matches_matcher`）。
  `mcp` のような短い matcher だと MCP のツール名（`mcp__looptrack__set_status`）に合わず、トークン情報が付きません。
  配線を直したら、そのフックを信頼し直すのを忘れずに。

### 7-2. Codex のフックの信頼

| | 確かめたこと |
| -- | -- |
| 条件 | プロジェクトが信頼済み（`~/.codex/config.toml` の `[projects."<パス>"] trust_level = "trusted"`）で、**かつフックごとに信頼**したものだけが動く。信頼はフックの定義のハッシュで記録され、未信頼・変わったフックは飛ばされる（[公式の Hooks の文書](https://learn.chatgpt.com/docs/hooks)） |
| 信頼のしかた | **ターミナルの Codex CLI（`codex`）をプロジェクトで起動し `/hooks`** で確認して信頼する。記録は `~/.codex/config.toml` の `[hooks.state."<hooks.json のパス>:<イベント>:<i>:<j>"] trusted_hash`（実物: codex-cli 0.155.0 の `/hooks` で 13 本を信頼 → 記録された） |
| デスクトップ版 | 信頼の記録は `~/.codex/config.toml` を共有するので、**CLI で信頼したフックはデスクトップ版でも動く**（実物: Codex デスクトップ 26.915 で、CLI の信頼の後に始めたセッションで SessionStart が動き、導入済みが通知された）。デスクトップ版のチャット欄に `/hooks` と打ってもコマンドにならない（AI への文として送られた）。未信頼のプロジェクトのフックは**知らせなしに飛ばされる**（[openai/codex#35306](https://github.com/openai/codex/issues/35306)。同じ issue に「設定 → Hooks」で信頼できるとあるが、26.915 では**未確認**） |
| 反映 | 信頼の後に始めたセッションから動く。始まっていたセッションの SessionStart はやり直されない |

### 7-3. GitHub Copilot（VS Code のエージェントモード・Copilot CLI）

Copilot でも、**イシューの操作は MCP のツールが主です**（Codex の §7-1 と同じ）。CLI の出番は、MCP に無い操作だけ。
ここから先は、公式文書と製品のソースで確かめた範囲です。Copilot CLI は 1.0.86 の実物でも確かめました（MCP の clientInfo は `copilot-cli`）。**VS Code は実物では未確認です**。

| 項目 | Copilot でのやり方 |
| -- | -- |
| MCP の接続 | VS Code: `.vscode/mcp.json` に `{"servers": {"looptrack": {"type": "http", "url": "https://example.com/looptrack/mcp", "headers": {"X-Looptrack-Project": "<slug>"}}}}`（`init --agent copilot --mcp` が書く。初回の接続でブラウザの許可）。Copilot CLI: リポジトリの `.github/mcp.json`（`init --agent copilot --mcp` が書く）か `.mcp.json`、または `~/.copilot/mcp-config.json` の `mcpServers.looptrack`（同じ `type`・`url`・`headers` と**必須の** `"tools": ["*"]`。CLI は 1.0.22 から `.vscode/mcp.json` を読まない）、認証は `/mcp auth looptrack`。CLI の MCP のツール呼び出しは毎回承認が要る（`copilot --allow-tool='looptrack'` で起動すれば要らない）（[CLI の command reference](https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-command-reference)・[VS Code の MCP の設定](https://code.visualstudio.com/docs/copilot/reference/mcp-configuration)） |
| AI の判定 | MCP の `clientInfo` が `copilot-cli`（CLI。1.0.86 の実物）/ `github-copilot-developer`（CLI。[github/copilot-cli#432](https://github.com/github/copilot-cli/issues/432) の保守者のコメント）・`Visual Studio Code` / `Visual Studio Code - Insiders` / `Code - OSS`（VS Code）なら `copilot`（`mcp_connections.agent`） |
| 案内文 | `AGENTS.md` の管理節（Copilot CLI・VS Code とも AGENTS.md を読む。`.github/copilot-instructions.md` は使わない）。VS Code で読まれないときは設定 `chat.useAgentsMdFile` を確かめる。**CLAUDE.md も読まれる**（CLI は AGENTS.md・CLAUDE.md・copilot-instructions.md を合成、VS Code も既定で読む）ので、CLAUDE.md の管理節の冒頭に「Claude Code 向け。Copilot・Codex は AGENTS.md に従う」と書いてある |
| ループの操作 | Codex と同じ（§7-1 の表）: `guide`・`next`・`create_issue`・`add_comment`・`set_status`・`get_issue` → `update_issue`、検証は `verify_issue` → 手元のシェル → `report_verify` |
| フック | `init --agent copilot` が `.github/hooks/looptrack.json` に SessionStart の `summary`（導入済みの通知）と、loop を入れたなら loop の hook を配線する。VS Code は既定で読む（Preview の機能・組織の設定で無効のことがある）。Copilot CLI は起動時にフォルダを信頼したときだけ読む。どちらも新しいセッションから動く。出力は CLI（トップレベル）と VS Code（`hookSpecificOutput`）の両方の形で出し、VS Code は matcher を無視するので hook の中でツール名を見る（kit/README.ja.md「Copilot での対応」）。**Windows**: hook は `looptrack`（`powershell` / `windows` のフィールドにも `command` と同じ `looptrack hook …` を置く。環境変数の前置は PowerShell の形。Windows での実物は未確認。導入済みにならなければ `looptrack issue installed --agent copilot`） |
| トークン情報 | **利用者が OpenTelemetry（OTel）のファイル出力を有効にしたときだけ測る**（下の §7-3-1。既定では無効）。有効にしていなければ、Copilot の MCP の操作は未付与に数えず、`usage attach` の指示も出ない。有効にした利用者（直近 7 日に Copilot のトークン情報が届いている利用者）の操作は Claude Code と同じに数える。Copilot CLI のシェルでは、`looptrack issue` の変更操作の後と `looptrack issue usage attach` が `COPILOT_AGENT_SESSION_ID` から会話を特定し、OTel のファイル出力から付ける。**VS Code の Copilot では `usage attach` を実行しない**（シェルにセッション ID が渡らず付けられない） |
| CLI のセッション ID | Copilot CLI はエージェントが打つコマンドに `COPILOT_AGENT_SESSION_ID`（と `COPILOT_CLI=1`）を渡すので、CLI はそれを `X-Looptrack-Session` に載せて AI の操作として記録する（changelog 1.0.29）。VS Code はセッション ID を渡さないが、エージェント用のターミナル（`AI_AGENT=github_copilot_vscode_agent` / `COPILOT_AGENT=1`）から打った操作は**セッション ID なしの AI の操作**として記録する。人が VS Code の端末で打った操作は人の操作のまま。CLI を打つときは `LOOPTRACK_API_URL=… LOOPTRACK_PROJECT=…` を前に付ける（Copilot に環境変数の設定が無い） |
| CLI が要る操作 | `looptrack issue login --browser`（初回だけ。フックの SessionStart が使うトークン）・`init` / `installed`（導入・更新。`setup` の手順） |

- Copilot は `.claude/settings.json` の hooks も読みます（VS Code・CLI とも）。ところが `CLAUDE_PROJECT_DIR` は渡さない。そこで init が書く Claude Code 向けのフックは、ルートを `${CLAUDE_PROJECT_DIR:-$(git rev-parse --show-toplevel 2>/dev/null || pwd)}` で引いています。
  Claude Code 以外から起動されたと分かれば、何も出さずに exit 0 で終わる（やり直しも拒否もしません）。判定の材料は、`CLAUDECODE`・`CLAUDE_PROJECT_DIR` が無く、`COPILOT_CLI`・`COPILOT_AGENT`・`AI_AGENT`（claude・codex・gemini で始まる値を除く）・`VSCODE_PID` などがあること。VS Code なら、設定 `chat.hookFilesLocations` で外す手もあります（kit/README.ja.md「Copilot での対応」）。
- 導入時の loop の問い: setup は 1 回目に問いだけを返します。
  ところがモデルによっては（Copilot CLI の auto で選ばれる mai-code-1.1-flash など）、問いを利用者に示さず、自分で答えを決めて導入を進めてしまうことがある。実物で確認したもので、サーバからは防げません。
  Copilot で導入するなら、最初の依頼に「loop を入れるか聞かれたら先に私に聞いて」と添えてください。答え（入れる / 入れない）を依頼文に含めてしまっても OK です。意図せず入ったら `looptrack issue init --remove-loop` で外せます。
- クラウド版（Copilot cloud agent）は §7-4 へ。CLI と同じ `clientInfo` の名前なので、サーバからは区別がつきません。

#### 7-3-1. Copilot のトークンを測る（OpenTelemetry のファイル出力）

Copilot は、会話記録に公開の形式で使用量を残しません。
そこで OTel です。利用者が OTel のファイル出力を有効にすると、LLM の呼び出しごとの使用量（`gen_ai.usage.*`）と
セッション ID（`gen_ai.conversation.id`）が JSON Lines で書かれる。それを Go 版の hook（`looptrack hook usage --agent copilot`）と `looptrack issue usage attach` が読み、`client = copilot` で送ります。
確認したのは Copilot CLI 1.0.86 の実物です（VS Code は未確認）。形は [server/DESIGN.md](server/DESIGN.md) の「Copilot のトークン」を見てください。
指示文や応答の内容は記録されません（内容の記録 `captureContent` は有効にしないので）。

**出力先は `$COPILOT_HOME/otel/` の下に限ります**（`COPILOT_HOME` を設定していなければ `~/.copilot/otel/` の下）。
理由はこうです。Copilot CLI は、OTel の出力先（`COPILOT_OTEL_FILE_EXPORTER_PATH`）を hook にもエージェントのシェルにも渡しません（渡るのは `COPILOT_HOME` など。1.0.86 で確認）。
ほかの場所に書かせたら、hook（MCP の操作・ターン終了・セッション終了）も `usage attach` も記録を見つけられず、トークン情報は付かない。
逆に、この置き場の下の `*.jsonl`（サブディレクトリも含む）なら、環境変数が無くても必ず読みます。

1. **Copilot CLI**: シェルの設定（`~/.zshrc` など）で出力先を決めます。
   ```sh
   mkdir -p "${COPILOT_HOME:-$HOME/.copilot}/otel"
   export COPILOT_OTEL_FILE_EXPORTER_PATH="${COPILOT_HOME:-$HOME/.copilot}/otel/copilot-cli.jsonl"   # これだけで OTel が有効になる（出力は file）
   ```
   （[CLI の command reference「OpenTelemetry monitoring」](https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-command-reference)）
2. **VS Code**: ユーザー設定（settings.json）に次を足します。`outfile` は絶対パスで。向ける先は CLI と同じ置き場（`~/.copilot/otel/` の下。`COPILOT_HOME` を設定しているならその `otel/` の下）です。
   ```json
   "github.copilot.chat.otel.enabled": true,
   "github.copilot.chat.otel.exporterType": "file",
   "github.copilot.chat.otel.outfile": "/Users/<you>/.copilot/otel/vscode.jsonl"
   ```
   （[VS Code の Monitor agent usage with OpenTelemetry](https://code.visualstudio.com/docs/agents/guides/monitoring-agents)。組織の管理設定が優先される）
3. 置き場の外に書いてしまったとき: ファイルを置き場の下へ移すか、次からの出力先を置き場の下に直します。`LOOPTRACK_USAGE_COPILOT_OTEL=<ファイルかディレクトリ>` が効くのは、自分のシェルで打つ
   `usage attach` だけ。hook の環境は Copilot が作るので hook には渡らず、これには頼れません。
   Copilot CLI のシェルで `looptrack issue usage attach <ID>` が記録を見つけられなければ、探した結果と置き場を示して止まります。
4. **hook の配線**: `looptrack init --agent copilot` が、`.github/hooks/looptrack.json` に `looptrack hook usage --agent copilot --event <PostToolUse|Stop|SessionEnd>` を入れます。
   PascalCase の名前なので、VS Code と CLI の両方が読む（`SessionEnd` は CLI だけ）。手で入れるときも、同じコマンドにしてください。
   hook がそのセッションの累計を送るのは、MCP でのイシューの変更（起票・コメント・状態変更・更新）の後と、ターン終了・セッション終了のとき。書き出しを待つので、数秒遅れます。
5. 確かめる: Copilot で MCP から 1 件コメントし、1 分ほど待って `looptrack issue usage show <ID>` に `copilot` の段階が出るかを見ます。出ない？ なら
   `ls "${COPILOT_HOME:-$HOME/.copilot}/otel"` に今日の `*.jsonl` があるかを確認（無ければ、出力先が置き場の外です）。Copilot CLI のシェルなら、
   `looptrack issue usage attach <ID>` を打って、案内（探した場所とその結果）を見てください。

既知の限界もあります。並列のサブエージェントでは、使用量が欠けることがある（[github/copilot-cli#4860](https://github.com/github/copilot-cli/issues/4860)）。欠けた分は、指示の終わりに書かれる合計で埋めます。
判定は利用者単位。CLI だけ有効にして VS Code を有効にしていないと、VS Code の操作が未付与に数えられてしまいます。両方を有効にしておきましょう。
OTel のファイルは追記され続けます。大きくなったら古いものは消してかまいません。送った値はサーバに残るので。ただし、続きのあるセッションの分を消すと累計が減り、その区間は「不整合」として捨てられます。

---

### 7-4. クラウド版の AI（Claude Code on the web・Codex cloud・Copilot cloud agent）

設定手順は [CLOUD-AGENTS.md](CLOUD-AGENTS.md) にあります（調べた結果の表と出典は [server/DESIGN.md](server/DESIGN.md) の「クラウド版の AI」。実物では未確認）。先に知っておいてほしいのは、次の 4 点。

- **手元の PC で動かしているサーバには届きません。** クラウド版から使えるのは、インターネットに HTTPS で公開したサーバだけ。クラウド側の許可リストに、サーバのホスト名を足してください（Claude Code on the web は環境を Custom に。Copilot cloud agent はリポジトリか組織の allowlist です）。
- 認証は **PAT を環境変数かシークレットに置きます**（クラウドではブラウザのログインを通せないことがよくあるので）。PAT はプロジェクト単位に絞れません。発行した利用者の全プロジェクトの権限で動きます。
  だから**クラウド専用の利用者を作って使うプロジェクトだけに参加させ**、有効日数 30 日で発行する。PAT はリポジトリにコミットしないでください。
- **Codex cloud のネットワークの許可を「GET・HEAD・OPTIONS に絞る」にすると書き込めません。** 起票・コメント・状態の変更は POST / PATCH だからです。書き込むなら、許可するメソッドは絞らずに。
- `init` の成果物（案内文・`.claude/settings.json`・`.github/hooks/`）をコミットしておけば、クラウドでもそのまま使われます。手元だけの設定（`~/.claude/`・`~/.codex/`・`~/.copilot/`・資格情報）は、クラウドにはありません。

## 8. 作業後

**イシューについて git でやることはありません。** サーバにすぐ保存されるからです。
残すのは 2 つだけ。プロジェクト側の引き継ぎ（§8-2）と、ソースのコミット。

### 8-2. イシューと引き継ぎメモの使い分け

| | 置き場所 | 内容 |
| -- | -- | -- |
| イシュー | サーバ（`looptrack issue`） | **これからやること・やっていること**。1 課題 1 件・状態遷移あり |
| 引き継ぎ | `.claude/memories/handoff.md` / `MEMORY.md` | **セッションをまたぐ現在地**。どのイシューがどこまで進んだかの要約 |
| 決定・学び | `.claude/memories/decision-*` / `caveat-*` | 決定理由・再発防止 |

イシュー本文に引き継ぎは書かない。引き継ぎにも、イシューの細かい経過は書かない。この線引きです。

### 8-3. 作業ツリーとブランチの後始末（`looptrack worktree`）

課題ごとに git の作業ツリー（worktree）を切って進めると、取り込み済みのものがどんどん溜まっていきます。
で、そのうち「どれが生きている作業か」が分からなくなる。判断の材料は毎回同じなので、それを `looptrack worktree` が集めます。

```bash
looptrack worktree list          # 一覧（片付けられるもの・残す理由・最後に動いていた時刻）
looptrack worktree prune         # 消す対象を示すだけ（既定。何も消さない）
looptrack worktree prune --yes   # 実際に片付ける
looptrack worktree mark          # いまのセッションがこの作業ツリーを使っていることを記録する
looptrack worktree mark --yes    # 別のセッションの印があっても上書きする
```

- **既定では何も消しません。** `prune` も、`--yes` が無ければ一覧を出すだけ。
  ほかのセッションが使っているものを消したら取り返しがつきません。だから、**消す前に一覧を利用者に見せてください**。
- 消すのは、次を**すべて**満たすものに限ります。本体の作業ツリーでない・取り込み済み・未コミットの変更が無い・
  locked でない・リモートに公開されているブランチでない・最終更新から `--min-age`（既定 30 分）以上たっている。
  1 つでも欠けたら、理由を付けて残す。
- **リモートを追うブランチ（追い先がまだあるもの）の作業ツリーは片付けません。** 配置や切り替えに使う常設の
  作業ツリーは、取り込み済みで中身がきれいでも消してはダメです。追い先が消えたもの（`gone`。PR がマージされて
  リモートのブランチが消えた後など）は、ふつうの一時のブランチとして扱います。
- **本体の作業ツリーは対象外です。** 複数のセッションが同時に使うので、印も付けないし、消しもしない。
- 判断の物差しは「**最後に動いていた時刻**」。セッションの ID だけだと、終わったセッションの印と動いている
  セッションの印を見分けられないんです。そこで `mark` は ID と時刻の両方を書きます。印が無いものは、git の記録の更新時刻で代用。
  作業ツリーを切ったら `mark` を打っておきましょう。ほかのセッションから「いま使っている」ことが見えるようになります。
- **`mark` は別のセッションの印を上書きしません。既に印があると失敗します（終了コード 1）。**
  「別のセッション <セッション>（<時刻>）の印があります。上書きしません（--yes で上書き）。」が出たら、
  **その作業ツリーはほかのセッションが使っているものとして扱ってください**（そのまま作業を始めないこと）。引き取るなら、相手に
  一声かけてから `looptrack worktree mark --yes` で上書きします。印が読めない・壊れているときも、誰のものか
  分からないので同じく失敗です（「既存の印（…）を読めません（…）。上書きしません（--yes で上書き）。」）。
  例外は 2 つ。自分のセッションの印を付け直す場合と、本体の作業ツリー（上の項目）は失敗になりません（終了コード 0）。
- `node_modules`・`.DS_Store`・処理系のキャッシュのような生成物だけの残骸は、「未コミットの変更」に数えません。
- 日をまたいだ未コミットの変更があるものには、**中身が失われかけています** と出ます。消さずに残すものの、
  本当の問題は、そのまま忘れられること。見つけたら、コミットか別ブランチへの退避を考えてください。
- 作業ツリーが無くなった後に残るブランチも、一覧に出ます（`worktree remove` してもブランチは残るので）。

---

## 9. イシュー鮮度ガード（Stop hook で止まる）

**参照したイシューを一度も更新しないまま作業を終えようとすると、停止がブロックされます。**
なぜか。次のセッションが、**古い状態のイシューを正しいものと思って**読み始めるのを防ぐためです。

| 記録するもの | いつ |
| -- | -- |
| 参照したイシュー ID | 利用者の発話に ID が出たとき（**harness が発話に混ぜたブロックの中は数えない**。下記） / `looptrack issue` に ID を渡したとき（`show EX-0142`・`comment EX-0142 …` の対象と `--parent`・`--blocked-by` の値。**コメント・`--comment`・`--body`・タイトルの本文に書いた ID は数えない**） / `git commit` の件名に ID が出たとき / `.claude/issues` のファイルを直接読んだとき |
| 実作業 | プロジェクトのルート内で `.claude/` 配下**以外**のファイルを変更したとき（別 worktree など**ルートの外は数えない**） / `git commit` `git merge` `git push` を実行したとき（`git merge-base` `git merge-tree` などの別サブコマンドは数えない） |

記録はセッション（フック入力の `session_id`）ごとに分かれます。同じ作業ツリーで並行する別のセッションの参照・作業・`ack` とは
混ざらない。**サブエージェントのツール呼び出しは記録しません**（親の Stop に混ぜないため）。
`issue-freshness ack` / `reset`（`looptrack issue-freshness`）を含む Bash コマンドからも、参照は記録しません。同じコマンドの `comment` で、ack した ID が戻ってこないようにするためです。
`session_id` を渡さない AI（Codex など）だと、プロジェクトで 1 つの記録になります。

**harness が発話に混ぜたブロックは、利用者の発話として数えません。** 他のセッションからの連絡・system の注意書き・
タスクの通知。こういうものには、そのセッションが読んでもいないイシューの ID が並びます。これを数えたらどうなるか。触っていないイシューを毎ターン
`ack` で外すはめになり、本当のやり直しの要求が埋もれてしまう。既定で除くのは
`<cross-session-message>`・`<system-reminder>`・`<task-notification>`・`<ci-monitor-event>` の 4 つ。
`LOOPTRACK_FRESHNESS_IGNORE_TAGS`（空白かカンマ区切り）で置き換えられます。閉じタグが見つからないもの（途中で切れたもの）は、
取りすぎないよう、取り除きません。

Stop の時点で **実作業があり、参照したイシューにセッション開始以降の更新（サーバのイベント）が 1 件も無ければ**
ブロック。誰の更新かは問いません。サーバにつながらないときは、止めずに通します。

止まらない条件: 実作業が無い / 参照先がクローズ済み / 参照した ID が実在しない / `.claude/` 配下だけの編集。

止まったら、やることはこれです。

```bash
looptrack issue comment <ID> "調べたこと・やったこと・確かめたこと"
looptrack issue close   <ID> --comment "受け入れ条件の検証結果"
```

**「作業したけれどイシューに書くことが無い」ときも、たいてい書くことはあります。** 本当に更新が要らないときに限って、
**理由を会話に残したうえで**外してください。

```bash
looptrack issue-freshness ack <ID>   # この ID だけ対象外（このセッションの間ずっと）
looptrack issue-freshness reset      # このセッションの記録を全て対象外
looptrack issue-freshness show       # いまの記録（モード・対象・ack した ID）を確認
```

`looptrack issue-freshness` は、`looptrack` の実行ファイルのサブコマンドです。
**`ack` で外した ID は控えに残り、同じセッションの間は二度と対象に戻りません。**
戻したらどうなるか。やり直しを求める文に並んだ ID が次のターンの入力に混ざったとき、また記録されてしまいます。結果、同じイシューを毎ターン外すことに。`reset` は控えごと消すので、その後の参照は改めて数えます。
実装は `internal/client/hook/core/freshness.go`（テストは `go test ./internal/client/hook/core/`）。

---

## 10. 困ったとき

| 症状 | 対処 |
| -- | -- |
| `エラー: アクセストークンがありません` / `トークンが無効です` / `失効しているか期限切れです` / `ログインの有効期限が切れました` | 利用者の承認を得て `looptrack issue login --browser` を実行し、ブラウザでのログインを利用者に頼む（ブラウザの無い環境は発行と `login --url` を利用者に依頼） |
| `エラー: プロジェクトが見つかりません: <slug>` | `LOOPTRACK_PROJECT` の綴り、または権限が無い（管理者に付与を依頼） |
| `エラー: サーバに接続できません（…）` | 時間をおいて再実行。続くなら利用者に伝える（https://example.com/looptrack/healthz） |
| `push` が競合で止まった | 表示された差分を作業コピーへ取り込み `push --rebase` |
| ルールで拒否された | メッセージの指示どおりに進める（コメントを付ける・状態を選び直す・`usage attach <ID>` を実行する 等） |
| `config` がサーバの URL（`LOOPTRACK_API_URL`）が無いというエラーで止まる（exit 1。対訳表 `cli.no_api.missing_url`） | `.claude/settings.json` の `env` に `LOOPTRACK_API_URL` / `LOOPTRACK_PROJECT` があるか。変更後は Claude Code の再起動が要る。未導入なら利用者に `looptrack issue init --project <slug>` を依頼する（[ADD-PROJECT.md](ADD-PROJECT.md) §4） |
| `next` がルールで拒否された | メッセージの指示どおり（`--comment` を付ける等）。見送られた候補と理由は `skipped` に出る |
