// Package agentdirs は、コーディング AI が自分の設定と会話記録を置くディレクトリの解決を 1 か所に持つ
// （DESIGN.md §9-5）。Claude Code は CLAUDE_CONFIG_DIR、Codex は CODEX_HOME で置き場を変えられる。
// 会話記録の探索（usagesnap）・auto-memory の引き継ぎ（hook/loop）・セッション要約のパスの手がかり（pathhint）は、
// すべてここを通す。置き場の既定値（~/.claude・~/.codex）を呼ぶ側に直書きしない。
package agentdirs

import (
	"path/filepath"
	"regexp"
	"strings"
)

// 置き場の既定の名前（ホームの直下）。呼ぶ側はこの名前を直接書かず、下の関数を通す。
const (
	ClaudeDirName = ".claude"
	CodexDirName  = ".codex"
)

// Getenv は環境変数の読み取り（env.Env の Get・hookio.Getenv と同じ形。無ければ ""）。
type Getenv = func(string) string

// ClaudeConfigDir は Claude Code の設定のディレクトリ。CLAUDE_CONFIG_DIR があればそれ、無ければ <home>/.claude。
// 会話記録は <これ>/projects/<スラッグ>/<セッション ID>.jsonl、auto-memory は <これ>/projects/<スラッグ>/memory/MEMORY.md にある。
// 決められない（CLAUDE_CONFIG_DIR も home も空）ときは ""。
func ClaudeConfigDir(get Getenv, home string) string {
	return resolve(get, "CLAUDE_CONFIG_DIR", home, ClaudeDirName)
}

// ClaudeProjectsDir は Claude Code の会話記録の置き場（<ClaudeConfigDir>/projects）。決められなければ ""。
func ClaudeProjectsDir(get Getenv, home string) string {
	return sub(ClaudeConfigDir(get, home), "projects")
}

// CodexHome は Codex の設定のディレクトリ。CODEX_HOME があればそれ、無ければ <home>/.codex。決められなければ ""。
func CodexHome(get Getenv, home string) string {
	return resolve(get, "CODEX_HOME", home, CodexDirName)
}

// CodexSessionsDir は Codex の会話記録の置き場（<CodexHome>/sessions。下に 年/月/日/rollout-*.jsonl）。決められなければ ""。
func CodexSessionsDir(get Getenv, home string) string {
	return sub(CodexHome(get, home), "sessions")
}

// nonAlnum は Claude Code の記録のディレクトリの名前（slug）で "-" に置き換える文字。
var nonAlnum = regexp.MustCompile(`[^A-Za-z0-9]`)

// ClaudeSlug は Claude Code が作業ディレクトリから作る記録のディレクトリの名前（<ClaudeProjectsDir>/<これ>）。
// 作業ディレクトリの realpath の英数字以外を "-" にしたもの（実測で realpath から作られる）。realpath を解くのは呼ぶ側。
// auto-memory の引き継ぎ（hook/loop の handoffTargets）と会話記録の取り込み（session transcript pull）が共有する。
func ClaudeSlug(realpath string) string {
	return nonAlnum.ReplaceAllString(realpath, "-")
}

// Home は HOME（無ければ Windows の USERPROFILE）。どちらも空なら ""。
func Home(get Getenv) string {
	if get == nil {
		return ""
	}
	if h := get("HOME"); h != "" {
		return h
	}
	return get("USERPROFILE")
}

func resolve(get Getenv, name, home, def string) string {
	if get != nil {
		if d := strings.TrimSpace(get(name)); d != "" {
			return d
		}
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, def)
}

func sub(dir, name string) string {
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, name)
}
