package loop

// pre-tool-scope-guard（kit/loop/hooks/pre-tool-scope-guard.sh の Go 版）: 別のプロジェクトの変更を利用者に確認する（ask）。
//
//   - Edit / Write / NotebookEdit: file_path が別の git リポジトリの中（同じリポジトリの worktree は同じとみなす）
//   - Bash: cd / git -C で移った先が別の git リポジトリで、その区切り（&& ; || 改行）に変更の操作がある
//   - Bash: LOOPTRACK_PROJECT=<別> を付けて CLI の変更系サブコマンドを打つ
//   - MCP（im…）: 変更系ツールの project がセッションの LOOPTRACK_PROJECT と違う
//
// Bash の 2 つは、入れ子のシェル（bash -c '…'・sudo sh -c '…'・eval "…"）を hookcmd で 1 段ほどいてから見る（包んだ形も素の形と同じ）。
//
// 通すもの: 読むだけ・git でない場所・同じリポジトリの worktree・LOOPTRACK_LOOP_SCOPE_ALLOW_REPOS・LOOPTRACK_PROJECT が無いときのイシューの判定。
//
// bash 版との違い: ツールは種類（hookio.Tool.Kind）で見分ける（Copilot の edit・create・bash なども対象になる。bash 版は Claude Code の
// ツール名だけを見た）。MCP のサーバ名は looptrack か looptrack-<英数字>（Copilot の <サーバ>-<ツール>・mcp_<サーバ>_<ツール> の形も）。
// git の共通ディレクトリは .git を読んで求める（git を起動しない）。リダイレクトの判定（`(?<![0-9&<>])>>?\s*(?!&|/dev/null)…`）は
// RE2 に後読み・先読みが無いので手で書いた（結果は同じ）。

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/howashoji/looptrack/internal/client/hook/hookcmd"
	"github.com/howashoji/looptrack/internal/hookio"
	"github.com/howashoji/looptrack/internal/i18n"
)

var (
	// looptrack issue …（文書の書き方）を変更として数える
	issueWriteSrc = `looptrack(?:\.exe)?"?\s+issue\s+(new|comment|status|close|assign|edit|push|verify|init|login|usage\s+attach)\b`
	issueWriteRe  = compatRe(issueWriteSrc)
	helpFlagRe    = compatRe(`\s(--help|-h)(\s|$)`)
	imProjectRe   = compatRe(`(?:^|[\s;&|(])(?:export\s+)?LOOPTRACK_PROJECT=([^\s;&|]+)`)
	imServerRe    = regexp.MustCompile(`^looptrack(-[A-Za-z0-9]+)?$`)
	imWriteTools  = map[string]bool{"create_issue": true, "add_comment": true, "set_status": true, "update_issue": true,
		"assign_issue": true, "verify_issue": true, "report_verify": true, "add_usage_ledger": true}
	heredocTagRe = compatRe(`<<-?\s*["']?([A-Za-z_][A-Za-z0-9_]*)["']?`)
	quotedRe     = regexp.MustCompile(`"(\\\\.|[^"\\\\])*"|'[^']*'`)
	segSplitRe   = regexp.MustCompile(`&&|\|\||;|\n`)
	cdRe         = compatRe(`^(?:builtin\s+)?cd\s+(\S+)\s*$`)
	gitCRe       = compatRe(`(?:^|\s)git\s+-C\s+(\S+)`)
	writeRes     = []*regexp.Regexp{
		compatRe(`(^|\s)git(\s+-C\s+\S+|\s+-c\s+\S+)*\s+(commit|add|rm|mv|push|merge|reset|checkout|switch|stash|apply|am|cherry-pick|rebase|revert|tag|restore|worktree\s+(add|remove))\b`),
		compatRe(`(^|\s)sed\s+(-[A-Za-z]*i|--in-place)`),
		compatRe(`(^|[\s|])(cp|mv|rm|rmdir|touch|mkdir|tee|ln|patch|truncate|chmod|install)\s`),
		issueWriteRe,
	}
)

// hasRedirect は `(?<![0-9&<>])>>?\s*(?!&|/dev/null)[^\s|&;<>]` に当たる所があるか（ファイルへのリダイレクト）。
func hasRedirect(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '>' {
			continue
		}
		if i > 0 && strings.ContainsRune("0123456789&<>", rune(s[i-1])) {
			continue
		}
		j := i + 1
		if j < len(s) && s[j] == '>' {
			j++
		}
		for j < len(s) {
			r, n := utf8.DecodeRuneInString(s[j:])
			if !isSpaceRune(r) {
				break
			}
			j += n
		}
		rest := s[j:]
		if rest == "" || strings.HasPrefix(rest, "&") || strings.HasPrefix(rest, "/dev/null") {
			continue
		}
		r, _ := utf8.DecodeRuneInString(rest)
		if isSpaceRune(r) || strings.ContainsRune("|&;<>", r) {
			continue
		}
		return true
	}
	return false
}

func isWriteSeg(s string) bool {
	if hasRedirect(s) {
		return true
	}
	for _, w := range writeRes {
		if w.MatchString(s) {
			return true
		}
	}
	return false
}

// stripHeredocs はヒアドキュメントの本文を外す（本文の > や rm はコマンドではない）。
func stripHeredocs(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	i := 0
	for i < len(lines) {
		line := lines[i]
		out = append(out, line)
		tags := heredocTagRe.FindAllStringSubmatch(line, -1)
		i++
		for _, t := range tags {
			for i < len(lines) && trimSpace(lines[i]) != t[1] {
				i++
			}
			i++
		}
	}
	return strings.Join(out, "\n")
}

// PreToolScopeGuard は別のリポジトリ・別のプロジェクトのイシューの変更を利用者に確認する。
func PreToolScopeGuard(ctx context.Context, ev hookio.Event) (hookio.Result, error) {
	e := envFrom(ctx)
	if ev.Tool == nil {
		return hookio.Result{}, nil
	}
	if v := ev.Raw["tool_input"]; truthy(v) {
		if _, isMap := v.(map[string]any); !isMap {
			return hookio.Result{}, nil // bash 版と同じく、オブジェクトでない tool_input は見ない
		}
	}
	r := realpath(root(ev, e))
	ti := toolInput(ev)
	slug := trimSpace(e.env("LOOPTRACK_PROJECT"))
	cwd0 := toStr(ev.Raw["cwd"])
	if cwd0 == "" {
		cwd0 = r
	}

	cache := map[string]string{}
	repoOf := func(p string) string {
		d := realpath(p)
		for !isDir(d) {
			parent := filepath.Dir(d)
			if parent == d {
				return ""
			}
			d = parent
		}
		if v, ok := cache[d]; ok {
			return v
		}
		v := gitCommonDir(d)
		cache[d] = v
		return v
	}
	own := repoOf(r)
	allow := map[string]bool{}
	for _, p := range strings.Fields(e.env("LOOPTRACK_LOOP_SCOPE_ALLOW_REPOS")) {
		allow[repoOf(p)] = true
	}
	otherRepo := func(p string) string {
		rr := repoOf(p)
		if rr == "" || rr == own || allow[rr] {
			return ""
		}
		if filepath.Base(rr) == ".git" {
			return filepath.Dir(rr)
		}
		return rr
	}
	lang := e.lang()
	ask := func(reason string) (hookio.Result, error) {
		return hookio.Result{Ask: i18n.T(lang, "loop.pretool.ask", "reason", reason)}, nil
	}
	home := e.env("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	resolve := func(p, base string) string {
		p = strings.Trim(p, `"'`)
		p = strings.ReplaceAll(p, "$HOME", home)
		p = strings.ReplaceAll(p, "${HOME}", home)
		p = expandUser(p, home)
		p = strings.ReplaceAll(p, "$CLAUDE_PROJECT_DIR", r)
		p = strings.ReplaceAll(p, "${CLAUDE_PROJECT_DIR}", r)
		if !filepath.IsAbs(p) {
			p = filepath.Join(base, p)
		}
		return filepath.Clean(p)
	}

	switch ev.Tool.Kind {
	case hookio.KindEdit, hookio.KindWrite:
		p := toStr(ti["file_path"])
		if p == "" {
			p = toStr(ti["notebook_path"])
		}
		if p == "" {
			p = ev.Tool.FilePath()
		}
		if p != "" {
			if top := otherRepo(resolve(p, cwd0)); top != "" {
				return ask(i18n.T(lang, "loop.pretool.other_repo_file", "root", r, "repo", top, "path", p))
			}
		}
		return hookio.Result{}, nil
	case hookio.KindMCP:
		if !imServerRe.MatchString(ev.Tool.Server) || !imWriteTools[ev.Tool.Name] {
			return hookio.Result{}, nil
		}
		if p := trimSpace(toStr(ti["project"])); slug != "" && p != "" && p != slug {
			return ask(i18n.T(lang, "loop.pretool.other_project_mcp", "slug", slug, "other", p, "tool", ev.Tool.Name))
		}
		return hookio.Result{}, nil
	case hookio.KindBash:
	default:
		return hookio.Result{}, nil
	}

	cmd := toStr(ti["command"])
	if cmd == "" {
		return hookio.Result{}, nil
	}
	// 入れ子のシェル（bash -c '…'・sudo sh -c '…'・eval "…"）の中身は、下の引用符を空にする段で消えるので、
	// 先にほどいて単純コマンドとして並べる（秘密のガード・git ガード・待ちループのガードと同じ hookcmd の段）。
	// 位置は問わない（hookcmd.AnyPos）: `ask` は人がその場で通せるので、広く当てて取りこぼしを減らす。
	// 前置の語を落とす段（hookcmd.Normalize の後半）は呼ばない。この判定は前置の語に左右されない形
	// （`(^|\s)` で当てる書き込みの語・区切りの直後の cd）で見ていて、落とすと `env LOOPTRACK_PROJECT=<別> …` の
	// 指定そのものが空白に替わり、別プロジェクトの判定が消える。
	body := hookcmd.UnwrapNestedShell(stripHeredocs(cmd), hookcmd.AnyPos)
	bare := quotedRe.ReplaceAllString(body, `""`) // 引用符の中を空にしたもの（リダイレクト・区切りの判定用）

	// 別プロジェクトの LOOPTRACK_PROJECT でのイシューの変更
	if slug != "" && issueWriteRe.MatchString(bare) && !helpFlagRe.MatchString(bare) {
		for _, m := range imProjectRe.FindAllStringSubmatch(body, -1) {
			v := strings.Trim(m[1], `"'`)
			if v != "" && v != slug {
				return ask(i18n.T(lang, "loop.pretool.other_project_cli", "slug", slug, "other", v))
			}
		}
	}

	// 別リポジトリでの変更
	cwd := cwd0
	raws, segs := segSplitRe.Split(body, -1), segSplitRe.Split(bare, -1)
	if len(raws) != len(segs) { // 引用符の中に区切りがある: 区切りは引用符を空にした側でそろえる
		raws = segs
	}
	for i := range segs {
		segRaw, s := raws[i], trimSpace(segs[i])
		if m := cdRe.FindStringSubmatch(trimSpace(segRaw)); m != nil {
			cwd = resolve(m[1], cwd)
			continue
		}
		target := cwd
		if m := gitCRe.FindStringSubmatch(segRaw); m != nil {
			target = resolve(m[1], cwd)
		}
		if !isWriteSeg(s) || helpFlagRe.MatchString(s) {
			continue
		}
		if top := otherRepo(target); top != "" {
			return ask(i18n.T(lang, "loop.pretool.other_repo_cmd", "root", r, "repo", top, "cmd", headStr(s, 80)))
		}
	}
	return hookio.Result{}, nil
}

// expandUser は先頭の ~ か ~/ をホームに置き換える（~user は展開しない）。
func expandUser(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return home + p[1:]
	}
	return p
}

// headStr は先頭 n 文字（文字数で切る）。
func headStr(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n])
}
