<!-- プロジェクト側の CLAUDE.md に入る節（<slug> はプロジェクト名、<URL> はサーバの URL）。looptrack issue init が
     <!-- looptrack:begin … --> / <!-- looptrack:end --> で囲んで入れる。正本は internal/client/kitinit/texts.go の claudeSnippet。
     この写しと一致することを internal/client/kitinit の TestClaudeSnippetDoc が確かめる（-update で書き直す）。手で貼らない。 -->

## 課題管理（イシュー管理サーバ）

**作業はイシュー登録から始める。会話だけで進めない。不具合は発覚と同時に起票する。**

（この節は Claude Code 向け。GitHub Copilot・Codex はこの節ではなく AGENTS.md の「課題管理」節に従う。AGENTS.md に無ければ MCP の looptrack の `setup` ツールから始める。
GitHub Copilot の CLI と VS Code は CLAUDE.md も読むため）

- 操作: `looptrack issue`（API モード。`.claude/settings.json` の `env` に `LOOPTRACK_API_URL` / `LOOPTRACK_PROJECT=<slug>`）。skill は `/issue`
- **使い方とルールは `looptrack issue guide` で読む**（共通規則 + このプロジェクトのルール + 運用文書を 1 回で返す。MCP なら `guide` ツール）
- ループ運用の 1 周: `looptrack issue next`（着手）→ 作業 → `looptrack issue comment <ID> "…"`（分かった時点で記録）→ 受け入れ条件を検証 → `looptrack issue close <ID> --comment "検証結果"` → 次の `next`
- 閲覧: <URL>/p/<slug>/ （ログイン必須・閲覧専用）

イシューはサーバ（DB）が正本で、ローカルにファイルは無い。したがって:

- **初回だけ** `looptrack issue login --browser --url <URL>`（開いたブラウザで利用者がログインと承認をする。トークンは会話に出ず、以後は自動で更新される。ブラウザの無い環境は <URL>/account で発行して `login --url <URL>` に貼る）
- 本文の編集は `looptrack issue edit <ID>` → `.claude/.looptrack-work/<ID>.md` を Read / Edit → `looptrack issue push <ID>`
- イシューについて git の pull / commit / push は不要
- 採番・クローズ済みの不変・プロジェクト別ルールはサーバが強制する。拒否されたらメッセージの指示に従う
- `--traces` はイシュー ID 専用、`--refs` は文書 ID（`FR-`/`NFR-`/`UC-`/`ISS-`/`DEC-`）専用
- 本文・コメントをシェル経由で渡すときはクォート付きヒアドキュメント（`<<'EOF'`）で囲む
