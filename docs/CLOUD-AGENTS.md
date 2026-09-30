# クラウド版の AI から使う（Claude Code on the web・Codex cloud・Copilot cloud agent）

クラウドで動くコーディング AI から、イシュー管理（CLI・MCP・hook）を使うための設定手順です。
中身は 2026-09-19 に各サービスの公式文書で確かめたもの。**実物での確認はまだです。**「未確認」と書いたところは、確認の結果を見てこの文書を直します。
調べた結果の表と出典は [DESIGN.md §9-5「クラウド版の AI」](server/DESIGN.md) にあります。

コマンドは `looptrack issue …` の形で書きます。
クラウドの環境には、セットアップスクリプトなどで `looptrack` を入れておいてください。実行ファイル 1 つで動くので、ほかの処理系は要りません。

## 0. どのクラウド版でも共通の前提と注意

### 届くのは、インターネットに公開したサーバだけ

クラウド版の AI は、クラウドの VM やコンテナの中で動きます。

**だから、手元の PC で動かしているサーバ（ローカル利用・`LOOPTRACK_LOCAL_MODE=1`・`127.0.0.1`）には届きません。**

クラウド版から使うには、インターネットから HTTPS で届くサーバ（例: `https://example.com/looptrack`）が要ります。
以下の「サーバのホスト名」は、そのサーバの URL のホスト名（例: `example.com`）のことです。

### init の成果物をコミットしておく

クラウド版は、リポジトリを clone した状態から始まります。手元で `init` した成果物も、**コミットしてプッシュしておけばクラウドでそのまま使われます**。
対象は `CLAUDE.md` / `AGENTS.md` の管理節・`.claude/settings.json`・`.claude/scripts/`・`.github/hooks/looptrack.json` など。
一方、手元だけの設定（`~/.claude/`・`settings.local.json`・`~/.codex/`・`~/.copilot/`・`~/.config/looptrack/credentials.json`）はクラウドにはありません。

### トークン（PAT）の作り方と絞り方

クラウドでは、ブラウザのログイン（`login --browser`・MCP の OAuth）を通せないことがよくあります。そこで**アクセストークン（PAT）を環境変数かシークレットに置きます**。
置いた値は、そのクラウドの環境を使える人や AI なら読める。そう考えて扱ってください。

1. **クラウド専用の利用者を、管理者に作ってもらいます。** PAT はプロジェクト単位に絞れず、発行した利用者の**全プロジェクトの権限**で動くからです。
   自分の PAT を置いたらどうなるか。自分が参加している全プロジェクトに、クラウドから書けてしまいます。専用の利用者（例: `alice-cloud`）を作り、
   **使うプロジェクトだけに参加させてください**（書くなら editor、読むだけなら viewer）。操作は、その利用者の名前で記録されます。
2. その利用者でログインし、アカウント設定（`/looptrack/account`）でトークンを発行します。**有効日数は最短の 30 日**に。用途には置き場所を書いておきます（例: `Codex cloud: org/repo の環境`）。
3. 使い終わったときや漏れた疑いがあるときは、アカウント設定の一覧から**すぐ失効させてください**。期限が来たら、新しく発行して置き換えます。
4. PAT は**リポジトリにコミットしないこと**。`.claude/settings.json` の `env`・`.mcp.json`・`.github/hooks/` にも書きません。

### どう記録されるか

| クラウド版 | CLI の操作 | MCP の操作 |
| -- | -- | -- |
| Claude Code on the web | AI の操作（Bash に渡る `CLAUDE_CODE_SESSION_ID` を `X-Looptrack-Session` に載せる見込み。未確認） | AI の操作（コネクタの接続名で判定） |
| Codex cloud | AI の操作（`CODEX_THREAD_ID` が渡れば。未確認。渡らなければ人の操作として記録される） | 未確認（クラウドの文書に MCP の記載が無い） |
| Copilot cloud agent | AI の操作（`COPILOT_CLI=1` と `COPILOT_AGENT_SESSION_ID`。`detail.agent = copilot`） | AI の操作（`copilot`） |

## 1. Claude Code on the web（claude.ai/code・`claude --cloud`）

1. **許可リスト**: claude.ai/code で環境（cloud environment）の設定を開きます。Network access を **Custom** にして、Allowed domains にサーバのホスト名を 1 行足してください。
   パッケージのインストールも要るなら「Also include default list of common package managers」も選びます。既定の **Trusted** のままだと届きません。
2. **トークン**: 同じ設定の Environment variables（`.env` 形式）に `LOOPTRACK_TOKEN=<PAT>` を書きます。**この環境を使う人なら、誰でも値を読めます。** 上の「PAT の絞り方」の専用の利用者を使ってください。
   コミットしてあれば、`LOOPTRACK_API_URL`・`LOOPTRACK_PROJECT` は要りません。init が `.claude/settings.json` の `env` に書いているからです。
   - Pro / Max の API credentials は、プロキシが要求に後から付けるので、値が VM に入りません。ところが CLI も自分で `Authorization` を付けるため、置き換わるかは未確認。当面は環境変数を使ってください。
3. **MCP（任意）**: claude.ai のコネクタとしてイシュー管理の MCP を追加して有効にすると、許可リストに関係なく Anthropic のサーバ経由で届きます（認証は OAuth）。
   けど、hook と CLI は VM から直接通信します。だから**MCP を使うときも 1. の許可リストは必要です**。足さないと SessionStart の hook が失敗し、MCP の操作にトークン情報が付きません。
   リポジトリの `.mcp.json`（init が書くもの・OAuth）が VM の中で認証できるかは未確認。
4. **hook**: コミットした `.claude/settings.json` の hook（SessionStart の summary・トークン計測・loop）は、クラウドでも動きます。**ただし、リポジトリを 1 つだけ付けたセッションに限ります。**
   複数のリポジトリを付けたセッションや projects のスレッドは、リポジトリの設定を読みません。
5. **確かめる**: セッションで `looptrack issue summary` を実行させます。一覧が出れば通っています。

注意:
- VM は放置すると回収されます。トークン情報は、操作のたびに hook と CLI が送るもの。後から `usage attach` で回収することはできません。
- 組織が Claude の IP 許可リストを使っていると、クラウドのセッション自体が動きません（Claude Code の文書による）。

## 2. Codex cloud（chatgpt.com/codex）

1. **許可リスト**: 環境の設定で、エージェントのインターネットアクセスを有効にします。既定では、エージェントの段階で遮断されています。許可リストは **None**（空）か **Common dependencies** を選び、サーバのホスト名を足してください。
2. **HTTP メソッドは絞らないでください。** 「GET・HEAD・OPTIONS に絞る」を選ぶと、イシューの変更（POST・PATCH）がプロキシで止まります。**起票・コメント・状態の変更ができず**、読むだけの状態に。
   書くなら、すべてのメソッドを許すこと。サーバ以外のドメインも許可リストに入れているなら、そのドメインにも POST が通ると踏まえて選びましょう。
3. **トークン**: 置き方は次の 2 つです。
   - 環境の **Environment variables** に `LOOPTRACK_TOKEN=<PAT>` を書く方法。セットアップとエージェントの両方の段階で使えます。環境を使える人には値が見える、と考えて扱ってください。
   - 環境の **Secrets** に置き、セットアップスクリプトで資格情報のファイルに書く方法。**Secrets が読めるのはセットアップスクリプトの間だけで、エージェントの段階の前に消えます。**
     Secret のままでは CLI から読めません。`export` もエージェントの段階には残らない。残るのはファイルです。

     ```bash
     # セットアップスクリプト（Secret の名前を LOOPTRACK_TOKEN_SECRET とした例。URL は LOOPTRACK_API_URL と同じ値・末尾の / なし）
     install -d -m 700 ~/.config/looptrack
     ( umask 077
       printf '{"https://example.com/looptrack": {"token": "%s"}}\n' "$LOOPTRACK_TOKEN_SECRET" \
         > ~/.config/looptrack/credentials.json )
     ```

     書いたファイルはエージェントからも読めます。環境の設定画面に値が出ないだけ。エージェントから隠せるわけではありません。
   - どちらの場合も、`LOOPTRACK_API_URL`・`LOOPTRACK_PROJECT` は Environment variables に置きます。Codex は `.claude/settings.json` の `env` を読まないからです。
4. **MCP**: クラウドの文書に MCP の記載がありません（未確認）。**なので CLI を使ってください。** AGENTS.md の管理節の案内（MCP が主）は、手元の Codex 向けのものです。
5. **hook**: `.codex/hooks.json` が動くのは、プロジェクトの `.codex/` を信頼したうえで、hook の定義ごとに `/hooks` で信頼したときだけ。クラウドで信頼する手段は文書に無いので、**動かないものとして使ってください**。SessionStart の summary の代わりに、`looptrack issue summary` を自分で実行させます。
6. **確かめる**: タスクで `looptrack issue summary` を実行させます。

注意:
- シェルに `CODEX_THREAD_ID` が渡るかは未確認です。会話記録がコンテナの中に残り、CLI がトークン情報を付けられるかどうかも未確認。
  付けられなければ、`usage.require_on_close` のプロジェクトでは Done にするときに `--override "理由"` が要ります。
- セットアップスクリプトの結果はキャッシュされます。書いた資格情報のファイルも、キャッシュに残ったまま。PAT を失効させたら、Secret を変えてキャッシュを作り直してください。

## 3. GitHub Copilot cloud agent（旧称 coding agent。GitHub の Issue・PR から動く）

1. **許可リスト**: リポジトリの Settings > Copilot > Internet access の **Custom allowlist** に、サーバのホスト名（か URL）を足します。組織でまとめるなら、組織の Settings > Copilot > Internet access の Organization custom allowlist で。
   ファイアウォールが掛かるのは、エージェントの Bash が起動したプロセス（CLI や hook の中のコマンド）です。**MCP サーバには直接掛かりません。**
2. **トークン（CLI）**: リポジトリの Settings > Security > Secrets and variables > **Agents** に、secret `LOOPTRACK_TOKEN`（値は PAT）と variable `LOOPTRACK_API_URL`・`LOOPTRACK_PROJECT` を置きます。
   Agents の secret と variable は、エージェントの環境変数として渡ります（値はログで伏せられます）。**Actions の secret は渡りません。**
3. **MCP（任意）**: Agents に secret `COPILOT_MCP_IM_AUTHORIZATION`（値は `Bearer <PAT>`）を置いて、リポジトリの Settings > Copilot > MCP servers に次を書きます。
   `COPILOT_MCP_` で始まる secret は MCP の設定からしか読めず、エージェントのシェルには渡りません。だから CLI 用の `LOOPTRACK_TOKEN` とは別に置きます。

   ```json
   {
     "mcpServers": {
       "looptrack": {
         "type": "http",
         "url": "https://example.com/looptrack/mcp",
         "headers": {
           "Authorization": "$COPILOT_MCP_IM_AUTHORIZATION",
           "X-Looptrack-Project": "<プロジェクトの slug>"
         },
         "tools": ["*"]
       }
     }
   }
   ```

   - `tools` は必須。cloud agent は OAuth のリモート MCP サーバを使えないので、PAT でつなぎます。**ツールは承認なしで使われます。**
   - `"Authorization": "Bearer $COPILOT_MCP_IM_TOKEN"` のように、文字列の途中で展開できるか？ 文書に例がありません（未確認）。上のように、secret に `Bearer ` まで入れておいてください。
   - リポジトリの `.github/mcp.json`・`.vscode/mcp.json`（init が書くもの）は、cloud agent の文書に出てきません。使われるのは Settings の JSON のほう。
4. **hook**: コミットした `.github/hooks/looptrack.json`（`init --agent copilot` が書くもの）は、cloud agent も読みます。
5. **トークン計測（任意）**: Copilot で測れるのは、OpenTelemetry のファイル出力を有効にした利用者の操作だけです（DESIGN §9-5「Copilot のトークン」）。
   cloud agent で測るなら、Agents の variable に `COPILOT_OTEL_FILE_EXPORTER_PATH`（例: `/tmp/copilot-otel.jsonl`）を置きます（動くかは未確認）。
   測らないなら、何もしなくてかまいません。トークン情報の未付与には数えられません。
6. **確かめる**: Issue を Copilot に割り当て、`looptrack issue summary` の結果を PR の説明に書かせます。

注意:
- 手元の Copilot でトークン計測を有効にしている利用者（直近 7 日に計測の記録がある人）がいたとします。その人の PAT を cloud agent に置くと、cloud agent の操作も計測の対象になる。
  測れていなければ、未付与に数えられます。クラウド専用の利用者を使えば、これは起きません。

## 4. 実物で確かめる

試験用のプロジェクトで行います。使う PAT は、そのプロジェクトだけに参加させた専用の利用者の、30 日のもの（本番の im のイシューは変えません）。

1. 上の手順どおりに、許可リストとトークンを設定します。
2. CLI で `looptrack issue next` → `looptrack issue comment <ID> "…"` → `looptrack issue close <ID>` を実行します。使える経路なら、MCP でも同じことを。
3. サーバの `issue_events` で `via`・`session_id`・`detail.agent` を見ます。期待する値は §0「どう記録されるか」に。
4. hook（SessionStart の summary が導入済みを知らせるか）とトークン計測（`looptrack issue usage show <ID>`）が、DESIGN §9-5「クラウド版の AI」のとおりかを確かめます。
5. 最後に、試験に使った PAT を失効させます。

## 5. 困ったとき

| 症状 | 原因と対処 |
| -- | -- |
| `サーバに接続できません` | 許可リストにサーバのホスト名が無い（Claude Code は既定の Trusted、Codex は既定の遮断、Copilot は既定のファイアウォール）。サーバがローカル利用（127.0.0.1）なら、クラウドからは届かない |
| 読めるが、起票・コメント・状態の変更だけ失敗する（プロキシの 403 など） | Codex cloud の HTTP メソッドを GET 等に絞っている。すべてのメソッドを許す |
| `アクセストークンがありません` | `LOOPTRACK_TOKEN` がシェルに渡っていない。Codex の Secrets はエージェントの段階で消える（§2 の 3.）。Copilot は Actions ではなく Agents の secret に置く |
| `トークンが無効です` / `失効しているか期限切れです` | PAT の期限切れか失効。新しく発行して置き換える |
| 証明書の検証のエラー（`CERTIFICATE_VERIFY_FAILED`・`x509: certificate signed by unknown authority` など） | クラウドのプロキシの CA を信頼していない。`SSL_CERT_FILE` にその CA を含むファイルを指定する |
| 操作が人の操作として記録される | シェルにセッション ID の環境変数が渡っていない（Codex cloud は未確認）。確認できた状況をイシューに書く |
