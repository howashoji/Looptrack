<!-- プロジェクト側の CLAUDE.md に入る節です（<slug> はプロジェクト名、<URL> はサーバの URL）。looptrack issue init が
     <!-- looptrack:begin … --> / <!-- looptrack:end --> で囲んで入れます。正本は internal/client/kitinit/texts.go の claudeSnippet。
     この写しと一致することを internal/client/kitinit の TestClaudeSnippetDoc が確かめます（-update で書き直します）。手で貼らないでください。 -->

## 課題管理（イシュー管理サーバ）

**作業はイシュー登録から始めます。会話だけで進めないでください。不具合は発覚と同時に起票します。**

この節は Claude Code 向けです。GitHub Copilot・Codex はこの節ではなく、AGENTS.md の「課題管理」節に従ってください。AGENTS.md に無ければ MCP の looptrack の `setup` ツールから始めます。
GitHub Copilot の CLI と VS Code は CLAUDE.md も読むので、この断りを置いています。

- 操作: `looptrack issue`（API モード。`.claude/settings.json` の `env` に `LOOPTRACK_API_URL` / `LOOPTRACK_PROJECT=<slug>`）。skill は `/issue`
- **使い方とルールは `looptrack issue guide` で読みます**。共通規則 + このプロジェクトのルール + 運用文書を 1 回で返します。MCP なら `guide` ツールです
- ループ運用の 1 周: `looptrack issue next`（着手）→ 作業 → `looptrack issue comment <ID> "…"`（分かった時点で記録）→ 受け入れ条件を検証 → `looptrack issue close <ID> --comment "検証結果"` → 次の `next`
- 閲覧: <URL>/p/<slug>/ （ログイン必須・閲覧専用）

イシューの正本はサーバ（DB）にあり、ローカルにファイルはありません。そのため、扱い方は次のとおり。

- **初回だけ** `looptrack issue login --browser --url <URL>` を実行します。開いたブラウザで利用者がログインと承認をします。トークンは会話に出ず、以後は自動で更新されます。ブラウザの無い環境では <URL>/account で発行して `login --url <URL>` に貼ってください
- 本文の編集: `looptrack issue edit <ID>` → `.claude/.looptrack-work/<ID>.md` を Read / Edit → `looptrack issue push <ID>`
- イシューについて git の pull / commit / push は不要
- 採番・クローズ済みの不変・プロジェクト別ルールはサーバが強制します。拒否されたらメッセージの指示に従ってください
- `--traces` はイシュー ID 専用、`--refs` は文書 ID（`FR-`/`NFR-`/`UC-`/`ISS-`/`DEC-`）専用
- 本文・コメントをシェル経由で渡すとき: クォート付きヒアドキュメント（`<<'EOF'`）で囲む
