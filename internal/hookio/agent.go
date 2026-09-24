package hookio

import (
	"os"
	"path/filepath"
	"strings"
)

// Getenv は環境変数の読み取り（テストで差し替える）。
type Getenv func(string) string

// 起動した AI の印（以前の hook（1.0.0 より前）の判定と同じ）。
var (
	// claudeEnv があれば Claude Code（CLAUDECODE は Claude Code の全子プロセス、CLAUDE_PROJECT_DIR は hook に渡る）。
	// ただし Copilot CLI も .claude/settings.json の hook に CLAUDE_PROJECT_DIR を渡し、Claude Code から起動された Copilot は
	// CLAUDECODE も受け継ぐ（1.0.86 で実測）ので、ForeignHost は Copilot の印を先に見る。
	claudeEnv = []string{"CLAUDECODE", "CLAUDE_PROJECT_DIR"}
	// codexEnv は Codex のシェル・hook に渡る。
	codexEnv = []string{"CODEX_THREAD_ID", "CODEX_SESSION_ID"}
	// geminiEnv は Gemini CLI のシェル・hook に渡る。
	geminiEnv = []string{"GEMINI_CLI", "GEMINI_PROJECT_DIR"}
	// copilotHookEnv は Copilot CLI が自分の起動する hook にだけ渡す（シェルのコマンドには渡らない。1.0.86 で実測）。
	// あれば hook を起動したのは Copilot（Claude Code から起動された Copilot で CLAUDECODE を受け継いでいても）。
	copilotHookEnv = "COPILOT_PROJECT_DIR"
	// copilotCLIEnv は Copilot CLI が hook とシェルのコマンドの両方に渡す（COPILOT_CLI=1）。Copilot のシェルから起動した
	// Claude Code の hook にも受け継がれるので、CLAUDECODE があるときは印に数えない。
	copilotCLIEnv = "COPILOT_CLI"
	// foreignEnv は Claude Code 以外の AI・エディタの印（Copilot CLI・Copilot のエージェント・VS Code の拡張機能ホスト）。
	// 以前の hook（1.0.0 より前）の FOREIGN_ENV と同じ並び（変えるときは記録したケースも見直す）。
	foreignEnv = []string{"COPILOT_CLI", "COPILOT_AGENT", "AI_AGENT", "VSCODE_PID", "VSCODE_IPC_HOOK"}
	// ownAIAgent: AI_AGENT は Claude Code 自身も設定する（claude-code_<版>_agent。2.1.275 で実測）→ この語で始まる値は印に数えない。
	ownAIAgent = []string{"claude", "codex", "gemini"}
)

func anyEnv(getenv Getenv, keys []string) bool {
	for _, k := range keys {
		if getenv(k) != "" {
			return true
		}
	}
	return false
}

func foreignMarked(getenv Getenv) bool {
	for _, k := range foreignEnv {
		v := getenv(k)
		if v == "" {
			continue
		}
		if k == "AI_AGENT" && hasAnyPrefix(strings.ToLower(v), ownAIAgent) {
			continue
		}
		return true
	}
	return false
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// ForeignHost は、Claude Code 向けの hook（.claude/settings.json の配線）を Claude Code 以外（Copilot など）が起動したなら true。
// 以前の hook（1.0.0 より前）と同じ判定。上から順に:
//
//  1. LOOPTRACK_LOOP_AGENT（init が Codex・Copilot 向けの配線に付ける）→ false（自分向けの配線で動いている）
//  2. COPILOT_PROJECT_DIR（Copilot CLI が自分の起動する hook にだけ渡す）→ true
//  3. CLAUDECODE・Codex・Gemini CLI の印 → false（Claude Code の挙動は変えない。Copilot のシェルから起動した Claude Code・Codex を含む）
//  4. COPILOT_CLI（Copilot CLI は .claude/settings.json の hook に CLAUDE_PROJECT_DIR も渡す）→ true
//  5. CLAUDE_PROJECT_DIR → false
//  6. それ以外で Copilot・VS Code の印があるか、入力が空でないのに hook_event_name が無い（Copilot CLI の camelCase の入力）→ true
//
// 判断できないときは false（従来どおり動く）。
func ForeignHost(getenv Getenv, raw map[string]any) bool {
	if getenv("LOOPTRACK_LOOP_AGENT") != "" {
		return false
	}
	if getenv(copilotHookEnv) != "" {
		return true
	}
	if getenv("CLAUDECODE") != "" || anyEnv(getenv, codexEnv) || anyEnv(getenv, geminiEnv) {
		return false
	}
	if getenv(copilotCLIEnv) != "" {
		return true
	}
	if anyEnv(getenv, claudeEnv) {
		return false
	}
	tp := str(raw["transcript_path"])
	if strings.Contains(tp, "/.codex/") || strings.Contains(tp, "/.gemini/") {
		return false
	}
	if foreignMarked(getenv) {
		return true
	}
	if len(raw) > 0 {
		_, ok := raw["hook_event_name"]
		return !ok
	}
	return false
}

// DetectAgent は --agent が無いときの予備の推測（配線で --agent を渡すのが原則）。分からなければ ok = false。
//
// 順序: LOOPTRACK_LOOP_AGENT（init が Codex・Copilot の配線に付ける）→ COPILOT_PROJECT_DIR（Copilot CLI が起動した hook）→ Claude Code の環境変数 → Codex の環境変数・会話記録のパス
// → Copilot の入力の形（camelCase）・会話記録のパス・環境変数 → Claude Code の会話記録のパス。
// Claude Code の環境変数を Copilot の印より先に見る（Claude Code を VS Code の端末から起動すると VSCODE_PID が付くため）。
func DetectAgent(getenv Getenv, raw map[string]any) (Agent, bool) {
	if a, ok := ParseAgent(getenv("LOOPTRACK_LOOP_AGENT")); ok {
		return a, true
	}
	if getenv(copilotHookEnv) != "" {
		return Copilot, true // Copilot CLI が起動した hook（CLAUDE_PROJECT_DIR・受け継いだ CLAUDECODE があっても）
	}
	if anyEnv(getenv, claudeEnv) {
		return ClaudeCode, true
	}
	tp := first(raw, "transcript_path", "transcriptPath")
	if anyEnv(getenv, codexEnv) || strings.Contains(tp, "/.codex/") {
		return Codex, true
	}
	// COPILOT_AGENT_SESSION_ID は Copilot CLI がシェルと stdio の MCP サーバに渡す（changelog 1.0.29）。hook に渡るかは未確認
	if isCamel(raw) || strings.Contains(tp, "/.copilot/") || foreignMarked(getenv) || getenv("COPILOT_AGENT_SESSION_ID") != "" {
		return Copilot, true
	}
	if strings.Contains(tp, "/.claude/") {
		return ClaudeCode, true
	}
	return "", false
}

// isCamel は Copilot CLI の camelCase の入力か（snake_case の hook_event_name・session_id・tool_name が無く、camelCase の項目がある）。
func isCamel(raw map[string]any) bool {
	for _, k := range []string{"hook_event_name", "session_id", "tool_name"} {
		if _, ok := raw[k]; ok {
			return false
		}
	}
	for _, k := range []string{"sessionId", "toolName", "toolArgs", "toolResult", "hookEventName", "initialPrompt"} {
		if _, ok := raw[k]; ok {
			return true
		}
	}
	return false
}

// GitRoot は dir を含む git の作業ツリーのルート（無ければ ""）。`git rev-parse --show-toplevel` の代わりに、
// 上へたどって最初に .git（ディレクトリ、または worktree・submodule の .git ファイル）がある場所を返す。
// git を起動しない（hook を速く保つ・git の無い Windows でも動く・internal/ はプロセスを起動しない＝ TestServerNeverExecutes）。
func GitRoot(dir string) string {
	if dir == "" {
		return ""
	}
	d, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}
