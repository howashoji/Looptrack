package kitinit

import (
	"strings"

	"github.com/howashoji/looptrack/internal/i18n"
)

// 文面（init が CLAUDE.md / AGENTS.md に入れる案内節と、その部品）。
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
	blockEnd   = "<!-- looptrack:end -->"
	skillMark  = "<!-- looptrack:init が作成（手で直した場合は looptrack issue init が上書きしない） -->"
	// skillMarkEN は英訳（en/SKILL.md）の同じ印。置き直してよいかは日英のどちらの印でも見る。
	skillMarkEN = "<!-- Created by looptrack:init (once you edit it by hand, looptrack issue init leaves it alone) -->"
	loopBegin   = "<!-- looptrack:loop:begin -->"
	loopEnd     = "<!-- looptrack:loop:end -->"
	legacyHead  = "## 課題管理（イシュー管理サーバ）"

	// 許可（.claude/settings.json の permissions.allow）。
	goPermission = "Bash(looptrack issue:*)"
)

const claudeSnippet = `## 課題管理（イシュー管理サーバ）

**作業はイシュー登録から始める。会話だけで進めない。不具合は発覚と同時に起票する。**

（この節は Claude Code 向け。GitHub Copilot・Codex はこの節ではなく AGENTS.md の「課題管理」節に従う。AGENTS.md に無ければ MCP の looptrack の ` + "`setup`" + ` ツールから始める。
GitHub Copilot の CLI と VS Code は CLAUDE.md も読むため）

- 操作: ` + "`looptrack issue`" + `（API モード。` + "`.claude/settings.json`" + ` の ` + "`env`" + ` に ` + "`LOOPTRACK_API_URL`" + ` / ` + "`LOOPTRACK_PROJECT={slug}`" + `）。skill は ` + "`/issue`" + `
- **使い方とルールは ` + "`looptrack issue guide`" + ` で読む**（共通規則 + このプロジェクトのルール + 運用文書を 1 回で返す。MCP なら ` + "`guide`" + ` ツール）
- ループ運用の 1 周: ` + "`looptrack issue next`" + `（着手）→ 作業 → ` + "`looptrack issue comment <ID> \"…\"`" + `（分かった時点で記録）→ 受け入れ条件を検証 → ` + "`looptrack issue close <ID> --comment \"検証結果\"`" + ` → 次の ` + "`next`" + `
- 閲覧: {url}/p/{slug}/ （ログイン必須・閲覧専用）

イシューはサーバ（DB）が正本で、ローカルにファイルは無い。したがって:

- **初回だけ** ` + "`looptrack issue login --browser --url {url}`" + `（開いたブラウザで利用者がログインと承認をする。トークンは会話に出ず、以後は自動で更新される。ブラウザの無い環境は {url}/account で発行して ` + "`login --url {url}`" + ` に貼る）
- 本文の編集は ` + "`looptrack issue edit <ID>`" + ` → ` + "`.claude/.looptrack-work/<ID>.md`" + ` を Read / Edit → ` + "`looptrack issue push <ID>`" + `
- イシューについて git の pull / commit / push は不要
- 採番・クローズ済みの不変・プロジェクト別ルールはサーバが強制する。拒否されたらメッセージの指示に従う
- ` + "`--traces`" + ` はイシュー ID 専用、` + "`--refs`" + ` は文書 ID（` + "`FR-`/`NFR-`/`UC-`/`ISS-`/`DEC-`" + `）専用
- 本文・コメントをシェル経由で渡すときはクォート付きヒアドキュメント（` + "`<<'EOF'`" + `）で囲む
`

const agentsSnippet = `## 課題管理（イシュー管理サーバ）

**作業はイシュー登録から始める。会話だけで進めない。不具合は発覚と同時に起票する。**

- **操作は MCP のツール（サーバ名 looptrack）で行う。** Codex のサンドボックスはシェルのコマンドの外部通信を止めるので、
  CLI（` + "`looptrack issue`" + `）は実行のたびに権限の昇格（利用者の承認）が要る。MCP のツールの通信はサンドボックスに止められない
- **使い方とルールは最初に ` + "`guide`" + ` ツールで読む**（共通規則 + このプロジェクトのルール + 運用文書を 1 回で返す）
- ループ運用の 1 周: ` + "`next`" + `（着手）→ 作業 → ` + "`add_comment`" + `（分かった時点で記録）→ 受け入れ条件を検証 → ` + "`set_status`" + ` で Done（` + "`comment`" + ` に検証結果）→ 次の ` + "`next`" + `。
  起票は ` + "`create_issue`" + `、本文の編集は ` + "`get_issue`" + ` で version と全文を取り ` + "`update_issue`" + ` に直した全文を渡す。拒否されたらメッセージの指示に従う
- 検証コマンド（本文の「## 検証コマンド」節）: ` + "`verify_issue`" + ` で一覧を取り、手元のシェルで順に全部実行して、結果を ` + "`report_verify`" + ` で送る
- トークン情報: MCP で起票・コメント・状態変更をすると、フック（` + "`.codex/hooks.json`" + ` の ` + "`looptrack hook usage`" + `）が数秒後にイシューへ付ける。
  Done の前に ` + "`add_comment`" + ` で経過を残しておけば、規則 usage（AI の Done にトークン情報を求める）に拒否されない
- CLI は MCP に無い操作だけに使う。権限を上げて実行し、利用者の承認を得る（環境変数 ` + "`LOOPTRACK_API_URL={url}`" + ` / ` + "`LOOPTRACK_PROJECT={slug}`" + ` は init が ` + "`.codex/config.toml`" + ` に書いた。
  ` + "`looptrack issue config`" + ` がサーバの URL が無いというエラーで止まるなら、コマンドの前に ` + "`LOOPTRACK_API_URL={url} LOOPTRACK_PROJECT={slug}`" + ` を付ける）:
  - ` + "`looptrack issue usage attach <ID>`" + `: ツールの結果にこのコマンドが示されたとき（目印は文面ではなくコマンド。サーバの文面は利用者の言語で変わる）。` + "`set_status`" + ` が規則 usage で拒否されたときは、
    数秒おいて 1 回やり直し（フックの送信が間に合っていないことがある）、それでも拒否されたら付けてからやり直す
  - ` + "`looptrack issue login --browser --url {url}`" + `: 初回だけ（フックが使うトークン。開いたブラウザで利用者がログインと承認をする。トークンは会話に出ない）
  - 導入・更新（` + "`init`・`installed`" + `）: ` + "`setup`" + ` ツールが返す手順どおり
- MCP の looptrack が接続されていないときは、CLI の ` + "`next` / `comment` / `verify` / `close --comment`" + ` で同じ 1 周を回す（毎回承認が要る。
  利用者に ` + "`~/.codex/config.toml`" + ` への ` + "`[mcp_servers.looptrack]`" + ` の追加を勧める）。本文・コメントはクォート付きヒアドキュメント（` + "`<<'EOF'`" + `）で渡す
- 閲覧: {url}/p/{slug}/ （ログイン必須・閲覧専用）
`

const agentsCopilotSnippet = `## 課題管理（イシュー管理サーバ）

**作業はイシュー登録から始める。会話だけで進めない。不具合は発覚と同時に起票する。**

- **操作は MCP のツール（サーバ名 looptrack）で行う**（GitHub Copilot。VS Code は ` + "`.vscode/mcp.json`" + `、Copilot CLI はリポジトリの ` + "`.github/mcp.json`" + ` か ` + "`~/.copilot/mcp-config.json`" + ` の looptrack。
  CLI の MCP のツールは呼ぶたびに承認が要る。利用者が ` + "`copilot --allow-tool='looptrack'`" + ` で起動すれば要らない）。
  CLI（` + "`looptrack issue`" + `）は MCP に無い操作だけに使う
- **使い方とルールは最初に ` + "`guide`" + ` ツールで読む**（共通規則 + このプロジェクトのルール + 運用文書を 1 回で返す）
- ループ運用の 1 周: ` + "`next`" + `（着手）→ 作業 → ` + "`add_comment`" + `（分かった時点で記録）→ 受け入れ条件を検証 → ` + "`set_status`" + ` で Done（` + "`comment`" + ` に検証結果）→ 次の ` + "`next`" + `。
  起票は ` + "`create_issue`" + `、本文の編集は ` + "`get_issue`" + ` で version と全文を取り ` + "`update_issue`" + ` に直した全文を渡す。拒否されたらメッセージの指示に従う
- 検証コマンド（本文の「## 検証コマンド」節）: ` + "`verify_issue`" + ` で一覧を取り、手元のシェルで順に全部実行して、結果を ` + "`report_verify`" + ` で送る
- トークン情報: 利用者が GitHub Copilot の OpenTelemetry のファイル出力を有効にすると、フック（` + "`.github/hooks/looptrack.json`" + ` の ` + "`looptrack hook usage`" + `）が
  MCP の操作・ターン終了のたびにイシューへ付ける（手順は AI-GUIDE §7-3-1）。有効にしていなければ付かず、未付与にも数えない。
  Copilot CLI のシェルでは ` + "`looptrack issue usage attach <ID>`" + ` で付けられる（` + "`COPILOT_AGENT_SESSION_ID`" + ` から会話を特定し、OTel のファイル出力から付ける）。
  **VS Code の Copilot では ` + "`looptrack issue usage attach`" + ` を実行しない**（シェルにセッション ID が渡らず付けられない）
- CLI が要るのは: ` + "`looptrack issue login --browser --url {url}`" + `（初回だけ。フックの SessionStart が使うトークン。開いたブラウザで利用者がログインと承認をする）・
  導入と更新（` + "`init`・`installed`" + `。` + "`setup`" + ` ツールが返す手順どおり）。CLI を打つときはコマンドの前に ` + "`LOOPTRACK_API_URL={url} LOOPTRACK_PROJECT={slug}`" + ` を付ける
- MCP の looptrack が接続されていないときは、CLI の ` + "`next` / `comment` / `verify` / `close --comment`" + ` で同じ 1 周を回す（` + "`LOOPTRACK_API_URL={url} LOOPTRACK_PROJECT={slug}`" + ` を前に付ける）。
  本文・コメントはクォート付きヒアドキュメント（` + "`<<'EOF'`" + `）で渡す。利用者に MCP の接続設定を勧める
- 閲覧: {url}/p/{slug}/ （ログイン必須・閲覧専用）
`

const agentsCopilotAddendum = `
### GitHub Copilot から使うとき

- 上と同じく MCP のツールが主（VS Code は ` + "`.vscode/mcp.json`" + `、Copilot CLI はリポジトリの ` + "`.github/mcp.json`" + ` か ` + "`~/.copilot/mcp-config.json`" + ` の looptrack）。Copilot にサンドボックスは無いが、
  CLI を打つときはコマンドの前に ` + "`LOOPTRACK_API_URL={url} LOOPTRACK_PROJECT={slug}`" + ` を付ける（Copilot は ` + "`.codex/config.toml`" + ` を読まない）
- トークン情報: 利用者が Copilot の OpenTelemetry のファイル出力を有効にすると、フック（` + "`.github/hooks/looptrack.json`" + ` の ` + "`looptrack hook usage`" + `）が付ける
  （手順は AI-GUIDE §7-3-1）。有効にしていなければ付かず、未付与にも数えない。
  Copilot CLI のシェルでは ` + "`looptrack issue usage attach <ID>`" + ` で付けられる（` + "`COPILOT_AGENT_SESSION_ID`" + ` から会話を特定する）。
  **VS Code の Copilot では ` + "`looptrack issue usage attach`" + ` を実行しない**（シェルにセッション ID が渡らず、同じディレクトリの Codex の記録を付けてしまうことがある）
`

// agentsMDBody は AGENTS.md の管理節の本文。
func agentsMDBody(agents []string, url, slug string) string {
	if has(agents, "codex") {
		body := fill(agentsSnippet, slug, url)
		if has(agents, "copilot") {
			body += fill(agentsCopilotAddendum, slug, url)
		}
		return body
	}
	return fill(agentsCopilotSnippet, slug, url)
}

const codexConfigNote = "# looptrack issue init が足した: AI が打つ looptrack issue にイシュー管理サーバの場所を渡す（サンドボックスのネットワークの許可ではない）"

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
