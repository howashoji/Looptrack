// Package pathhint は、セッション要約の本文の中の端末のパスをサーバに置き換えさせるための手がかり（path_hints）を集める
// （DESIGN.md §9-7）。サーバは端末のパスを知らないので、置き換える値はクライアントが要求に添える。
// 置き換えそのものはサーバの service（digest.go）の 1 か所で行い、ここでは値を集めるだけにする。
// CLI の issue comment と、セッション要約を送るほかの入口（サブコマンド・hook）は、すべてこの Collect を使う。
package pathhint

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/howashoji/looptrack/internal/client/agentdirs"
	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/hookio"
)

// Hints はサーバに送る手がかり（REST の path_hints・MCP の add_comment の path_hints と同じ形）。
type Hints struct {
	Roots        []string // git の作業ツリーのルート（literal と realpath と、macOS の /private/… の形）
	Home         string   // ホームディレクトリ
	MachineLocal []string // その端末にしか無い置き場（AI の scratchpad・会話記録のディレクトリ）
}

// Collect は手がかりを集める。
//   - Roots: dirs（作業ディレクトリと CLAUDE_PROJECT_DIR など）それぞれを含む git の作業ツリーのルート。
//     `git rev-parse --show-toplevel` と同じ場所を、git を起動せずに上へたどって求め（hookio.GitRoot）、
//     その文字列と realpath（シンボリックリンクを解いた形）の両方を入れる。macOS では /tmp・/var・/etc の実体が
//     /private/… なので、どちらの形で書かれても当たるよう、/private の有無を入れ替えた形も足す（ほかの OS では本文に現れないので害は無い）。
//   - Home: HOME（Windows は USERPROFILE）。
//   - MachineLocal: extra（hook の scratchpad_dir・transcript_path のディレクトリなど、呼ぶ側が知っている値）と、
//     LOOPTRACK_MACHINE_LOCAL（OS のパスの区切りで複数）と、AI の会話記録の置き場のうち実在するもの
//     （agentdirs の解決: ~/.claude/projects・CLAUDE_CONFIG_DIR/projects・~/.codex/sessions・CODEX_HOME/sessions）。
//
// 絶対パスでないもの・空のものは入れない。重複は除き、見つけた順に並べる。
func Collect(e env.Env, dirs []string, extra []string) Hints {
	var h Hints
	roots := newSet()
	for _, d := range dirs {
		top := hookio.GitRoot(d)
		if top == "" {
			continue
		}
		for _, p := range variants(top) {
			roots.add(p)
		}
	}
	h.Roots = roots.list
	home := e.Get("HOME")
	if home == "" {
		home = e.Get("USERPROFILE")
	}
	if (filepath.IsAbs(home) || strings.HasPrefix(home, "/")) && !isRoot(home) && len(home) <= maxBytes {
		h.Home = home
	}
	local := newSet()
	for _, p := range extra {
		local.add(p)
	}
	for _, p := range filepath.SplitList(e.Value(env.MachineLocal)) {
		local.add(strings.TrimSpace(p))
	}
	// 置き場の解決は agentdirs の 1 か所（CLAUDE_CONFIG_DIR・CODEX_HOME があればその下、無ければ ~/.claude・~/.codex）。
	// 既定の置き場と環境変数で変えた置き場の両方が実在すれば、両方入れる（変える前の記録が残っていることがある）。
	var known []string
	if home != "" {
		known = append(known, agentdirs.ClaudeProjectsDir(nil, home), agentdirs.CodexSessionsDir(nil, home))
	}
	known = append(known, agentdirs.ClaudeProjectsDir(e.Get, home), agentdirs.CodexSessionsDir(e.Get, home))
	for _, p := range known {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			local.add(p)
		}
	}
	h.MachineLocal = local.list
	return h
}

// Empty は手がかりが 1 つも無いか。
func (h Hints) Empty() bool {
	return len(h.Roots) == 0 && h.Home == "" && len(h.MachineLocal) == 0
}

// Object は要求の本文に入れる path_hints（空の欄は入れない）。
func (h Hints) Object() *jsonorder.Object {
	o := jsonorder.NewObject()
	if len(h.Roots) > 0 {
		o.Set("roots", h.Roots)
	}
	if h.Home != "" {
		o.Set("home", h.Home)
	}
	if len(h.MachineLocal) > 0 {
		o.Set("machine_local", h.MachineLocal)
	}
	return o
}

// variants は p と、その realpath と、それぞれの /private の有無を入れ替えた形。
func variants(p string) []string {
	s := newSet()
	s.add(p)
	if real, err := filepath.EvalSymlinks(p); err == nil {
		s.add(real)
	}
	for _, q := range append([]string(nil), s.list...) {
		s.add(privateToggle(q))
	}
	return s.list
}

// privateToggle は macOS の /private/{tmp,var,etc} と /{tmp,var,etc} を入れ替えた形（当たらなければ ""）。
func privateToggle(p string) string {
	for _, d := range []string{"tmp", "var", "etc"} {
		if rest, ok := strings.CutPrefix(p, "/private/"+d); ok && (rest == "" || rest[0] == '/') {
			return "/" + d + rest
		}
		if rest, ok := strings.CutPrefix(p, "/"+d); ok && (rest == "" || rest[0] == '/') {
			return "/private/" + d + rest
		}
	}
	return ""
}

// set は見つけた順を保つ重複なしの並び（絶対パスだけを入れる）。
type set struct {
	seen map[string]bool
	list []string
}

func newSet() *set { return &set{seen: map[string]bool{}} }

// サーバが受け付ける値に合わせる（ルートそのもの・長すぎる値は送らない。1 つの欄は maxEntries 件まで）。
const (
	maxEntries = 32
	maxBytes   = 4096
)

func (s *set) add(p string) {
	if p == "" || len(p) > maxBytes || isRoot(p) || !(filepath.IsAbs(p) || strings.HasPrefix(p, "/")) || s.seen[p] || len(s.list) >= maxEntries {
		return
	}
	s.seen[p] = true
	s.list = append(s.list, p)
}

// isRoot はルートそのもの（/・C:\ など。末尾の区切りを除くと空かドライブ名だけ）か。
func isRoot(p string) bool {
	c := strings.TrimRight(p, `/\`)
	return c == "" || (len(c) == 2 && c[1] == ':')
}
