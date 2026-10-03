package kitinit

import (
	"strings"

	"github.com/howashoji/looptrack/internal/i18n"
)

// 文面（init が CLAUDE.md / AGENTS.md に入れる案内節と、その部品）。
//
// 案内節は日本語が正本で、英語版を同じ並びの定数（…EN）で持つ。どちらを入れるかは init の言語
// （端末に出す案内と同じ cli.Ctx.Lang）で決める。skill と同じく導入時の言語で決まるので、言語を変えたら
// init を打ち直す（節は印で見分けて丸ごと差し替えるので、言語が変わっても二重にはならない）。
//
// 案内節・skill・rules は kit の本文をそのまま置く（kit 自身が looptrack の呼び方で書かれている。
// 同梱の kit がそうであることは TestKitUsesLooptrackCLI が確かめる）。

const (
	// GoCLI は looptrack の CLI の呼び方（置いた本文・許可・案内はすべてこの形）。
	GoCLI = "looptrack issue"
)

func fill(s, slug, url string) string {
	return strings.NewReplacer("{slug}", slug, "{url}", url).Replace(s)
}

const (
	blockBegin = "<!-- looptrack:begin（looptrack issue init が管理する節。直すときは looptrack issue init を再実行する） -->"
	// blockBeginEN は英語の節の同じ印。節の始まりは日英のどちらの印でも見つける（blockBeginAt）。
	blockBeginEN = "<!-- looptrack:begin (managed by looptrack issue init; to change it, run looptrack issue init again) -->"
	blockEnd     = "<!-- looptrack:end -->"
	skillMark    = "<!-- looptrack:init が作成（手で直した場合は looptrack issue init が上書きしない） -->"
	// skillMarkEN は英訳（en/SKILL.md）の同じ印。置き直してよいかは日英のどちらの印でも見る。
	skillMarkEN = "<!-- Created by looptrack:init (once you edit it by hand, looptrack issue init leaves it alone) -->"
	loopBegin   = "<!-- looptrack:loop:begin -->"
	loopEnd     = "<!-- looptrack:loop:end -->"
	legacyHead  = "## 課題管理（イシュー管理サーバ）"

	// 許可（.claude/settings.json の permissions.allow）。
	goPermission = "Bash(looptrack issue:*)"
)

const claudeSnippet = `## 課題管理（イシュー管理サーバ）

**作業はイシュー登録から始めます。会話だけで進めないでください。不具合は発覚と同時に起票します。**

この節は Claude Code 向けです。GitHub Copilot・Codex はこの節ではなく、AGENTS.md の「課題管理」節に従ってください。AGENTS.md に無ければ MCP の looptrack の ` + "`setup`" + ` ツールから始めます。
GitHub Copilot の CLI と VS Code は CLAUDE.md も読むので、この断りを置いています。

- 操作: ` + "`looptrack issue`" + `（API モード。` + "`.claude/settings.json`" + ` の ` + "`env`" + ` に ` + "`LOOPTRACK_API_URL`" + ` / ` + "`LOOPTRACK_PROJECT={slug}`" + `）。skill は ` + "`/issue`" + `
- **使い方とルールは ` + "`looptrack issue guide`" + ` で読みます**。共通規則 + このプロジェクトのルール + 運用文書を 1 回で返します。MCP なら ` + "`guide`" + ` ツールです
- ループ運用の 1 周: ` + "`looptrack issue next`" + `（着手）→ 作業 → ` + "`looptrack issue comment <ID> \"…\"`" + `（分かった時点で記録）→ 受け入れ条件を検証 → ` + "`looptrack issue close <ID> --comment \"検証結果\"`" + ` → 次の ` + "`next`" + `
- 閲覧: {url}/p/{slug}/ （ログイン必須・閲覧専用）

イシューの正本はサーバ（DB）にあり、ローカルにファイルはありません。そのため、扱い方は次のとおり。

- **初回だけ** ` + "`looptrack issue login --browser --url {url}`" + ` を実行します。開いたブラウザで利用者がログインと承認をします。トークンは会話に出ず、以後は自動で更新されます。ブラウザの無い環境では {url}/account で発行して ` + "`login --url {url}`" + ` に貼ってください
- 本文の編集: ` + "`looptrack issue edit <ID>`" + ` → ` + "`.claude/.looptrack-work/<ID>.md`" + ` を Read / Edit → ` + "`looptrack issue push <ID>`" + `
- イシューについて git の pull / commit / push は不要
- 採番・クローズ済みの不変・プロジェクト別ルールはサーバが強制します。拒否されたらメッセージの指示に従ってください
- ` + "`--traces`" + ` はイシュー ID 専用、` + "`--refs`" + ` は文書 ID（` + "`FR-`/`NFR-`/`UC-`/`ISS-`/`DEC-`" + `）専用
- 本文・コメントをシェル経由で渡すとき: クォート付きヒアドキュメント（` + "`<<'EOF'`" + `）で囲む
`

const agentsSnippet = `## 課題管理（イシュー管理サーバ）

**作業はイシュー登録から始めます。会話だけで進めないでください。不具合は発覚と同時に起票します。**

- **操作は MCP のツール（サーバ名 looptrack）で行います。** Codex のサンドボックスはシェルのコマンドの外部通信を止めます。
  だから CLI（` + "`looptrack issue`" + `）は、実行のたびに権限の昇格（利用者の承認）が要ります。MCP のツールの通信はサンドボックスに止められません
- **使い方とルールは、最初に ` + "`guide`" + ` ツールで読みます**。共通規則 + このプロジェクトのルール + 運用文書を 1 回で返します
- ループ運用の 1 周: ` + "`next`" + `（着手）→ 作業 → ` + "`add_comment`" + `（分かった時点で記録）→ 受け入れ条件を検証 → ` + "`set_status`" + ` で Done（` + "`comment`" + ` に検証結果）→ 次の ` + "`next`" + `。
  起票は ` + "`create_issue`" + ` です。本文の編集では ` + "`get_issue`" + ` で version と全文を取り、` + "`update_issue`" + ` に直した全文を渡します。拒否されたらメッセージの指示に従ってください
- 検証コマンド（本文の「## 検証コマンド」節）: ` + "`verify_issue`" + ` で一覧を取り、手元のシェルで順に全部実行して、結果を ` + "`report_verify`" + ` で送る
- トークン情報: MCP で起票・コメント・状態変更をすると、フック（` + "`.codex/hooks.json`" + ` の ` + "`looptrack hook usage`" + `）が数秒後にイシューへ付けます。
  Done の前に ` + "`add_comment`" + ` で経過を残しておけば、規則 usage（AI の Done にトークン情報を求める）に拒否されません
- CLI は MCP に無い操作だけに使います。権限を上げて実行し、利用者の承認を得てください。環境変数 ` + "`LOOPTRACK_API_URL={url}`" + ` / ` + "`LOOPTRACK_PROJECT={slug}`" + ` は init が ` + "`.codex/config.toml`" + ` に書きました。
  ` + "`looptrack issue config`" + ` がサーバの URL が無いというエラーで止まるなら、コマンドの前に ` + "`LOOPTRACK_API_URL={url} LOOPTRACK_PROJECT={slug}`" + ` を付けます。CLI を使う場面は次のとおりです:
  - ` + "`looptrack issue usage attach <ID>`" + `: ツールの結果にこのコマンドが示されたとき。目印は文面ではなくコマンドです（サーバの文面は利用者の言語で変わります）。` + "`set_status`" + ` が規則 usage で拒否されたときは、
    数秒おいて 1 回やり直します。フックの送信が間に合っていないことがあるからです。それでも拒否されたら、付けてからやり直してください
  - ` + "`looptrack issue login --browser --url {url}`" + `: 初回だけ。取るのはフックが使うトークンです。開いたブラウザで利用者がログインと承認をし、トークンは会話に出ません
  - 導入・更新（` + "`init`・`installed`" + `）: ` + "`setup`" + ` ツールが返す手順どおり
- MCP の looptrack が接続されていないときは、CLI の ` + "`next` / `comment` / `verify` / `close --comment`" + ` で同じ 1 周を回します。毎回承認が要るので、
  利用者に ` + "`~/.codex/config.toml`" + ` への ` + "`[mcp_servers.looptrack]`" + ` の追加を勧めてください。本文・コメントはクォート付きヒアドキュメント（` + "`<<'EOF'`" + `）で渡します
- 閲覧: {url}/p/{slug}/ （ログイン必須・閲覧専用）
`

const agentsCopilotSnippet = `## 課題管理（イシュー管理サーバ）

**作業はイシュー登録から始めます。会話だけで進めないでください。不具合は発覚と同時に起票します。**

- **操作は MCP のツール（サーバ名 looptrack）で行います。** GitHub Copilot の接続先は、VS Code が ` + "`.vscode/mcp.json`" + `、Copilot CLI がリポジトリの ` + "`.github/mcp.json`" + ` か ` + "`~/.copilot/mcp-config.json`" + ` の looptrack。
  CLI の MCP のツールは呼ぶたびに承認が要ります。利用者が ` + "`copilot --allow-tool='looptrack'`" + ` で起動すれば要りません。
  CLI（` + "`looptrack issue`" + `）は MCP に無い操作だけに使います
- **使い方とルールは、最初に ` + "`guide`" + ` ツールで読みます**。共通規則 + このプロジェクトのルール + 運用文書を 1 回で返します
- ループ運用の 1 周: ` + "`next`" + `（着手）→ 作業 → ` + "`add_comment`" + `（分かった時点で記録）→ 受け入れ条件を検証 → ` + "`set_status`" + ` で Done（` + "`comment`" + ` に検証結果）→ 次の ` + "`next`" + `。
  起票は ` + "`create_issue`" + ` です。本文の編集では ` + "`get_issue`" + ` で version と全文を取り、` + "`update_issue`" + ` に直した全文を渡します。拒否されたらメッセージの指示に従ってください
- 検証コマンド（本文の「## 検証コマンド」節）: ` + "`verify_issue`" + ` で一覧を取り、手元のシェルで順に全部実行して、結果を ` + "`report_verify`" + ` で送る
- トークン情報: 利用者が GitHub Copilot の OpenTelemetry のファイル出力を有効にすると、フック（` + "`.github/hooks/looptrack.json`" + ` の ` + "`looptrack hook usage`" + `）が
  MCP の操作・ターン終了のたびにイシューへ付けます（手順は AI-GUIDE §7-3-1）。有効にしていなければ付かず、未付与にも数えません。
  Copilot CLI のシェルでは ` + "`looptrack issue usage attach <ID>`" + ` で付けられます。` + "`COPILOT_AGENT_SESSION_ID`" + ` から会話を特定し、OTel のファイル出力から付ける仕組みです。
  **VS Code の Copilot では ` + "`looptrack issue usage attach`" + ` を実行しないでください**。シェルにセッション ID が渡らず、付けられません
- CLI が要る場面: ` + "`looptrack issue login --browser --url {url}`" + `（初回だけ）と導入・更新（` + "`init`・`installed`" + `）。login で取るのはフックの SessionStart が使うトークンで、開いたブラウザで利用者がログインと承認をします。
  導入・更新は ` + "`setup`" + ` ツールが返す手順どおりに行います。CLI を打つときはコマンドの前に ` + "`LOOPTRACK_API_URL={url} LOOPTRACK_PROJECT={slug}`" + ` を付けてください
- MCP の looptrack が接続されていないときは、CLI の ` + "`next` / `comment` / `verify` / `close --comment`" + ` で同じ 1 周を回します。` + "`LOOPTRACK_API_URL={url} LOOPTRACK_PROJECT={slug}`" + ` は前に付けます。
  本文・コメントはクォート付きヒアドキュメント（` + "`<<'EOF'`" + `）で渡し、利用者には MCP の接続設定を勧めてください
- 閲覧: {url}/p/{slug}/ （ログイン必須・閲覧専用）
`

const agentsCopilotAddendum = `
### GitHub Copilot から使うとき

- 操作は上と同じく MCP のツールが主です（VS Code は ` + "`.vscode/mcp.json`" + `、Copilot CLI はリポジトリの ` + "`.github/mcp.json`" + ` か ` + "`~/.copilot/mcp-config.json`" + ` の looptrack）。Copilot にサンドボックスはありません。
  それでも CLI を打つときは、コマンドの前に ` + "`LOOPTRACK_API_URL={url} LOOPTRACK_PROJECT={slug}`" + ` を付けてください。Copilot は ` + "`.codex/config.toml`" + ` を読まないからです
- トークン情報: 利用者が Copilot の OpenTelemetry のファイル出力を有効にすると、フック（` + "`.github/hooks/looptrack.json`" + ` の ` + "`looptrack hook usage`" + `）が付けます
  （手順は AI-GUIDE §7-3-1）。有効にしていなければ付かず、未付与にも数えません。
  Copilot CLI のシェルでは ` + "`looptrack issue usage attach <ID>`" + ` で付けられます。会話は ` + "`COPILOT_AGENT_SESSION_ID`" + ` から特定します。
  **VS Code の Copilot では ` + "`looptrack issue usage attach`" + ` を実行しないでください**。シェルにセッション ID が渡らず、同じディレクトリの Codex の記録を付けてしまうことがあります
`

// claudeSnippetEN は claudeSnippet の英語版（項目・コマンド・パス・条件は日本語版とそろえる）。
const claudeSnippetEN = `## Issue tracking (issue server)

**Start every piece of work by filing an issue, not in conversation alone. File a bug as soon as you find one.**

This section is for Claude Code. GitHub Copilot and Codex follow the "Issue tracking" section in AGENTS.md instead; if AGENTS.md has none, they start from the ` + "`setup`" + ` tool of the looptrack MCP server.
The note is here because GitHub Copilot's CLI and VS Code read CLAUDE.md too.

- Operation: ` + "`looptrack issue`" + ` (API mode, with ` + "`LOOPTRACK_API_URL`" + ` / ` + "`LOOPTRACK_PROJECT={slug}`" + ` in the ` + "`env`" + ` of ` + "`.claude/settings.json`" + `). The skill is ` + "`/issue`" + `
- **Read how to use it and the rules with ` + "`looptrack issue guide`" + `**. It returns the common rules, this project's rules and the operating documents in one go. Over MCP, use the ` + "`guide`" + ` tool
- One round of the loop: ` + "`looptrack issue next`" + ` (start) → work → ` + "`looptrack issue comment <ID> \"…\"`" + ` (record what you learn as you learn it) → verify the acceptance criteria → ` + "`looptrack issue close <ID> --comment \"verification results\"`" + ` → the next ` + "`next`" + `
- Browse: {url}/p/{slug}/ (login required, read-only)

The authoritative copy of each issue lives on the server (in its database), and there are no local files. Handle them as follows.

- **The first time only**, run ` + "`looptrack issue login --browser --url {url}`" + `. The user logs in and approves in the browser that opens; the token never shows up in the conversation and refreshes itself from then on. Without a browser, issue a token at {url}/account and paste it into ` + "`login --url {url}`" + `
- Editing a body: ` + "`looptrack issue edit <ID>`" + ` → Read / Edit ` + "`.claude/.looptrack-work/<ID>.md`" + ` → ` + "`looptrack issue push <ID>`" + `
- Issues need no git pull / commit / push
- The server enforces numbering, the immutability of closed issues and the per-project rules. When it rejects something, follow the instructions in the message
- ` + "`--traces`" + ` is for issue IDs only, and ` + "`--refs`" + ` for document IDs (` + "`FR-`" + `/` + "`NFR-`" + `/` + "`UC-`" + `/` + "`ISS-`" + `/` + "`DEC-`" + `) only
- To pass a body or a comment through the shell, wrap it in a quoted heredoc (` + "`<<'EOF'`" + `)
`

// agentsSnippetEN は agentsSnippet の英語版。
const agentsSnippetEN = `## Issue tracking (issue server)

**Start every piece of work by filing an issue, not in conversation alone. File a bug as soon as you find one.**

- **Work through the MCP tools (server name looptrack).** Codex's sandbox blocks network access from shell commands,
  so the CLI (` + "`looptrack issue`" + `) needs elevated permissions (the user's approval) every time it runs. The sandbox does not block the MCP tools' traffic
- **Before anything else, read how to use it and the rules with the ` + "`guide`" + ` tool**. It returns the common rules, this project's rules and the operating documents in one go
- One round of the loop: ` + "`next`" + ` (start) → work → ` + "`add_comment`" + ` (record what you learn as you learn it) → verify the acceptance criteria → ` + "`set_status`" + ` to Done (with the verification results in ` + "`comment`" + `) → the next ` + "`next`" + `.
  File an issue with ` + "`create_issue`" + `. To edit a body, take the version and the full text with ` + "`get_issue`" + `, then pass the corrected full text to ` + "`update_issue`" + `. When something is rejected, follow the instructions in the message
- Verify commands (the "## Verify commands" section of the body): get the list with ` + "`verify_issue`" + `, run every one of them in order in your local shell, and send the results with ` + "`report_verify`" + `
- Token usage: when you file, comment or change a status over MCP, a hook (` + "`looptrack hook usage`" + ` in ` + "`.codex/hooks.json`" + `) attaches it to the issue a few seconds later.
  Leave a note of your progress with ` + "`add_comment`" + ` before Done, and the usage rule (which asks for token usage on an AI's Done) will not reject it
- Use the CLI only for operations that MCP lacks, with elevated permissions and the user's approval. init wrote the environment variables ` + "`LOOPTRACK_API_URL={url}`" + ` / ` + "`LOOPTRACK_PROJECT={slug}`" + ` into ` + "`.codex/config.toml`" + `.
  If ` + "`looptrack issue config`" + ` stops with an error saying there is no server URL, put ` + "`LOOPTRACK_API_URL={url} LOOPTRACK_PROJECT={slug}`" + ` in front of the command. The CLI is for these cases:
  - ` + "`looptrack issue usage attach <ID>`" + `: when a tool result shows this command. The cue is the command, not the wording (the server's wording follows the user's language). If ` + "`set_status`" + ` is rejected by the usage rule,
    wait a few seconds and retry once, since the hook may not have finished sending yet. If it is still rejected, attach the usage and then retry
  - ` + "`looptrack issue login --browser --url {url}`" + `: the first time only. It gets the token the hooks use. The user logs in and approves in the browser that opens, and the token never shows up in the conversation
  - Installing and updating (` + "`init`" + `, ` + "`installed`" + `): follow the steps the ` + "`setup`" + ` tool returns
- When the looptrack MCP server is not connected, go round the same loop with the CLI's ` + "`next`" + ` / ` + "`comment`" + ` / ` + "`verify`" + ` / ` + "`close --comment`" + `. Each call then needs approval,
  so suggest that the user add ` + "`[mcp_servers.looptrack]`" + ` to ` + "`~/.codex/config.toml`" + `. Pass bodies and comments in a quoted heredoc (` + "`<<'EOF'`" + `)
- Browse: {url}/p/{slug}/ (login required, read-only)
`

// agentsCopilotSnippetEN は agentsCopilotSnippet の英語版。
const agentsCopilotSnippetEN = `## Issue tracking (issue server)

**Start every piece of work by filing an issue, not in conversation alone. File a bug as soon as you find one.**

- **Work through the MCP tools (server name looptrack).** GitHub Copilot connects to looptrack in ` + "`.vscode/mcp.json`" + ` for VS Code, and in the repository's ` + "`.github/mcp.json`" + ` or ` + "`~/.copilot/mcp-config.json`" + ` for Copilot CLI.
  In the CLI, each call to an MCP tool needs approval, unless the user starts it with ` + "`copilot --allow-tool='looptrack'`" + `.
  Use the CLI (` + "`looptrack issue`" + `) only for operations that MCP lacks
- **Before anything else, read how to use it and the rules with the ` + "`guide`" + ` tool**. It returns the common rules, this project's rules and the operating documents in one go
- One round of the loop: ` + "`next`" + ` (start) → work → ` + "`add_comment`" + ` (record what you learn as you learn it) → verify the acceptance criteria → ` + "`set_status`" + ` to Done (with the verification results in ` + "`comment`" + `) → the next ` + "`next`" + `.
  File an issue with ` + "`create_issue`" + `. To edit a body, take the version and the full text with ` + "`get_issue`" + `, then pass the corrected full text to ` + "`update_issue`" + `. When something is rejected, follow the instructions in the message
- Verify commands (the "## Verify commands" section of the body): get the list with ` + "`verify_issue`" + `, run every one of them in order in your local shell, and send the results with ` + "`report_verify`" + `
- Token usage: once the user turns on GitHub Copilot's OpenTelemetry file output, a hook (` + "`looptrack hook usage`" + ` in ` + "`.github/hooks/looptrack.json`" + `)
  attaches it to the issue after every MCP operation and at the end of every turn (see AI-GUIDE §7-3-1 for the steps). Without that output nothing is attached, and nothing counts as missing either.
  In a Copilot CLI shell you can attach it with ` + "`looptrack issue usage attach <ID>`" + `, which identifies the conversation from ` + "`COPILOT_AGENT_SESSION_ID`" + ` and reads the OTel file output.
  **Do not run ` + "`looptrack issue usage attach`" + ` from Copilot in VS Code**. The shell never receives the session ID, so it cannot attach anything
- When you need the CLI: ` + "`looptrack issue login --browser --url {url}`" + ` (the first time only) and installing or updating (` + "`init`" + `, ` + "`installed`" + `). login gets the token the hooks' SessionStart uses; the user logs in and approves in the browser that opens.
  For installing and updating, follow the steps the ` + "`setup`" + ` tool returns. Whenever you type a CLI command, put ` + "`LOOPTRACK_API_URL={url} LOOPTRACK_PROJECT={slug}`" + ` in front of it
- When the looptrack MCP server is not connected, go round the same loop with the CLI's ` + "`next`" + ` / ` + "`comment`" + ` / ` + "`verify`" + ` / ` + "`close --comment`" + `, with ` + "`LOOPTRACK_API_URL={url} LOOPTRACK_PROJECT={slug}`" + ` in front.
  Pass bodies and comments in a quoted heredoc (` + "`<<'EOF'`" + `), and suggest that the user set up the MCP connection
- Browse: {url}/p/{slug}/ (login required, read-only)
`

// agentsCopilotAddendumEN は agentsCopilotAddendum の英語版。
const agentsCopilotAddendumEN = `
### When you use it from GitHub Copilot

- As above, the MCP tools are the main way in (looptrack in ` + "`.vscode/mcp.json`" + ` for VS Code, and in the repository's ` + "`.github/mcp.json`" + ` or ` + "`~/.copilot/mcp-config.json`" + ` for Copilot CLI). Copilot has no sandbox.
  If you do type a CLI command, put ` + "`LOOPTRACK_API_URL={url} LOOPTRACK_PROJECT={slug}`" + ` in front of it, because Copilot does not read ` + "`.codex/config.toml`" + `
- Token usage: once the user turns on Copilot's OpenTelemetry file output, a hook (` + "`looptrack hook usage`" + ` in ` + "`.github/hooks/looptrack.json`" + `) attaches it
  (see AI-GUIDE §7-3-1 for the steps). Without that output nothing is attached, and nothing counts as missing either.
  In a Copilot CLI shell you can attach it with ` + "`looptrack issue usage attach <ID>`" + `. The conversation is identified from ` + "`COPILOT_AGENT_SESSION_ID`" + `.
  **Do not run ` + "`looptrack issue usage attach`" + ` from Copilot in VS Code**. The shell never receives the session ID, and it may attach the record of a Codex session in the same directory instead
`

// claudeMDBody は CLAUDE.md の管理節の本文（init の言語で選ぶ）。
func claudeMDBody(lang i18n.Lang, url, slug string) string {
	if lang == i18n.JA {
		return fill(claudeSnippet, slug, url)
	}
	return fill(claudeSnippetEN, slug, url)
}

// agentsMDBody は AGENTS.md の管理節の本文（init の言語で選ぶ）。
func agentsMDBody(lang i18n.Lang, agents []string, url, slug string) string {
	snippet, codex, addendum := agentsCopilotSnippet, agentsSnippet, agentsCopilotAddendum
	if lang != i18n.JA {
		snippet, codex, addendum = agentsCopilotSnippetEN, agentsSnippetEN, agentsCopilotAddendumEN
	}
	if has(agents, "codex") {
		body := fill(codex, slug, url)
		if has(agents, "copilot") {
			body += fill(addendum, slug, url)
		}
		return body
	}
	return fill(snippet, slug, url)
}

// blockBeginFor は lang の節の始まりの印。
func blockBeginFor(lang i18n.Lang) string {
	if lang == i18n.JA {
		return blockBegin
	}
	return blockBeginEN
}

// blockBeginAt は cur の中で最初に現れる節の始まりの印（日英のどちらか）の位置。無ければ -1。
// 言語を変えて init を打ち直しても、前の言語の印を同じ節の始まりと見て差し替えるため、両方を探す。
func blockBeginAt(cur string) int {
	at := -1
	for _, m := range []string{blockBegin, blockBeginEN} {
		if i := strings.Index(cur, m); i != -1 && (at == -1 || i < at) {
			at = i
		}
	}
	return at
}

const (
	codexConfigNote   = "# looptrack issue init が足した: AI が打つ looptrack issue にイシュー管理サーバの場所を渡す（サンドボックスのネットワークの許可ではない）"
	codexConfigNoteEN = "# Added by looptrack issue init: tells the looptrack issue commands the AI runs where the issue server is (it does not open the sandbox to the network)"
)

// codexConfigNoteFor は .codex/config.toml に足す注釈（init の言語で選ぶ）。
func codexConfigNoteFor(lang i18n.Lang) string {
	if lang == i18n.JA {
		return codexConfigNote
	}
	return codexConfigNoteEN
}

// codexGuidance は init が端末に出す Codex の設定の案内。
func codexGuidance(lang i18n.Lang, url, slug string) string {
	return i18n.T(lang, "kitinit.guide.codex", "url", url, "slug", slug)
}

// copilotGuidance は init が端末に出す GitHub Copilot の設定の案内。
func copilotGuidance(lang i18n.Lang, url, slug string) string {
	return i18n.T(lang, "kitinit.guide.copilot", "url", url, "slug", slug, "hooks", copilotHooks)
}

// otherGuidance は --agent other の案内（init は CLI の配置のほかは何も書かない）。
func otherGuidance(lang i18n.Lang, url, slug string, placed bool, cli string) string {
	c := i18n.T(lang, "kitinit.guide.other.cli_todo")
	if placed {
		c = i18n.T(lang, "kitinit.guide.other.cli_done", "cli", cli)
	}
	return i18n.T(lang, "kitinit.guide.other", "cli_line", c, "cli", cli, "url", url, "slug", slug)
}

// loopQuestion は loop を入れるかの問い（サーバの setup の loopQuestion と同じ文面）。
func loopQuestion(lang i18n.Lang, hooks, rules, skills int) string {
	return i18n.T(lang, "kitinit.init.loop_question", "hooks", hooks, "rules", rules, "skills", skills)
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// hasSkillMark は skill の本文が init の置いた印（日英のどちらか）を持つか。
func hasSkillMark(body string) bool {
	return strings.Contains(body, skillMark) || strings.Contains(body, skillMarkEN)
}
