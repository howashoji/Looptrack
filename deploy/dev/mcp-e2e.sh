#!/bin/bash
# 実際の Claude Code から /im/mcp の全ツールを呼ぶ動作確認。
#
#   LOOPTRACK_TOKEN=imp_… deploy/dev/mcp-e2e.sh [MCP の URL] [既定プロジェクト]
#
# 既定: http://127.0.0.1:18091/im/mcp・example（deploy/rules/example.json のルールを入れたプロジェクト）。一時ディレクトリに .mcp.json（Authorization: Bearer ${LOOPTRACK_TOKEN}）を書き、
# claude -p でツールを順に呼ばせて結果の一覧を表示する。**書き込みを伴う**ので、使い捨ての DB を指すサーバで実行する。
# 事前に Claude Code にログインしていること（claude を対話で起動して /login）。
set -euo pipefail
URL="${1:-http://127.0.0.1:18091/im/mcp}"
PROJECT="${2:-example}"
: "${LOOPTRACK_TOKEN:?環境変数 LOOPTRACK_TOKEN にアクセストークンを設定してください}"
DIR=$(mktemp -d)
trap 'rm -rf "$DIR"' EXIT
cat > "$DIR/.mcp.json" <<JSON
{"mcpServers": {"looptrack": {"type": "http", "url": "$URL",
  "headers": {"Authorization": "Bearer \${LOOPTRACK_TOKEN}", "X-Looptrack-Project": "$PROJECT"}}}}
JSON
PROMPT='これは MCP サーバ looptrack の動作確認です（使い捨てのテスト用 DB）。mcp__looptrack__ のツールだけを使い、次を順に実行してください。各手順で失敗しても止めずに次へ進み、最後に「手順番号: 成功/失敗（ツールの返答の要点 1 行）」の一覧だけを出力してください。
1. list_projects
2. list_issues（引数なし）
3. ready_issues（sort=id）
4. create_issue: title「MCP 動作確認」、type task、priority P3、body「Claude Code から起票」
5. get_issue: 4 で作成した ID
6. add_comment: 4 の ID に「MCP からのコメント」
7. update_issue: get_issue し直して最新の version と markdown を取り、「## 背景\n\n（未記入）」を「## 背景\n\nMCP から編集」に置き換えた全文を渡す
8. set_status: 4 の ID を "In Progress"、comment「着手」（プロジェクト別ルールで拒否されたら、そのメッセージの要点を記録し、override_reason「E2E テストのため（利用者指示）」を付けて再実行）
9. get_matrix（format md）
10. project_summary（limit 3）
11. issue_activity: ids に 4 の ID
12. set_status: 4 の ID を "Backlog"（プロジェクト別ルール forbid_status で拒否されるはず。拒否メッセージの 1 行目を記録）'
cd "$DIR"
claude -p "$PROMPT" --mcp-config .mcp.json --strict-mcp-config --allowedTools "mcp__looptrack" --model sonnet
