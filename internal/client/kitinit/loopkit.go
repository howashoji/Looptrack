package kitinit

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/howashoji/looptrack/internal/client/cli"
	"github.com/howashoji/looptrack/internal/client/hook/loop"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/kit"
)

// 導入セットの loop（kit/loop）。
//
// 置き方:
//   - 置くのは rules（.claude/rules/looptrack-loop/）と skills（.claude/skills/<名前>/）の md だけ。実体のファイル（copy）で置き、symlink は使わない。
//   - hook は置かない（kit/loop に実体が無い。manifest の hooks/<名前> を looptrack hook <名前> で配線する）。ゲートは looptrack gates。
//   - .looptrack-kit.json の loop.files・bundle_sha256 は kit/loop の全ファイルのハッシュ（サーバの配布物と比べるため）。

const (
	kitLoopManifest = "kit/loop/manifest.json"
	loopRulesDir    = ".claude/rules/looptrack-loop"
)

var hookEvents = []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop", "SubagentStop", "SessionEnd",
	"PreCompact", "Notification"}

var loopKinds = []string{"hook", "rules", "skill", "script", "verify"}

// loopEntry は manifest の 1 項目。
type loopEntry struct {
	Name    string
	Kind    string
	Event   string
	Matcher string // null・無しは ""
	Timeout int
	Order   float64
	idx     int
	raw     map[string]any
}

// col は AI の列（codex / copilot）。null・無しは nil。
func (e loopEntry) col(agent string) map[string]any {
	m, _ := e.raw[agent].(map[string]any)
	return m
}

// hookName は looptrack hook の名前（hooks/session-start-rules → session-start-rules。以前の版の manifest の .sh も外す）。
func (e loopEntry) hookName() string {
	n := path.Base(e.Name)
	for _, ext := range []string{".sh", ".py"} {
		n = strings.TrimSuffix(n, ext)
	}
	return n
}

// parseManifest は kit/loop/manifest.json を読んで検査する（以前の CLI（1.0.0 より前）と同じ規則・同じ文面）。
func parseManifest(files map[string]string) (version any, out []loopEntry, err error) {
	body, ok := files[kitLoopManifest]
	if !ok {
		return nil, nil, i18n.Errorf("kitinit.manifest.err.missing", "file", kitLoopManifest)
	}
	var data any
	if e := json.Unmarshal([]byte(body), &data); e != nil {
		return nil, nil, i18n.Errorf("kitinit.manifest.err.unreadable", "file", kitLoopManifest, "reason", e)
	}
	entries := data
	if m, ok := data.(map[string]any); ok {
		version, entries = m["version"], m["entries"]
	}
	list, ok := entries.([]any)
	if !ok {
		return nil, nil, i18n.Errorf("kitinit.manifest.err.shape", "file", kitLoopManifest)
	}
	for i, x := range list {
		m, ok := x.(map[string]any)
		name, _ := m["name"].(string)
		if !ok || strings.Trim(name, "/") == "" {
			return nil, nil, i18n.Errorf("kitinit.manifest.err.no_name", "file", kitLoopManifest, "index", i+1)
		}
		name = strings.TrimPrefix(strings.Trim(name, "/"), "kit/loop/")
		e := loopEntry{Name: name, idx: i, raw: m}
		kind, _ := m["kind"].(string)
		if kind == "" {
			if ev, _ := m["event"].(string); ev != "" {
				kind = "hook"
			}
		}
		if !has(loopKinds, kind) {
			return nil, nil, i18n.Errorf("kitinit.manifest.err.kind", "file", kitLoopManifest, "name", name, "kinds", strings.Join(loopKinds, " / "))
		}
		e.Kind = kind
		if kind == "hook" {
			ev, _ := m["event"].(string)
			if !has(hookEvents, ev) {
				return nil, nil, i18n.Errorf("kitinit.manifest.err.event", "file", kitLoopManifest, "name", name, "events", strings.Join(hookEvents, " / "))
			}
			if !strings.HasPrefix(name, "hooks/") {
				return nil, nil, i18n.Errorf("kitinit.manifest.err.hook_dir", "file", kitLoopManifest, "name", name)
			}
			e.Event = ev
		}
		for _, c := range []string{"codex", "copilot"} {
			if (kind == "rules" || kind == "skill") && m[c] != nil {
				want := "sections"
				if kind == "skill" {
					want = "description"
				}
				cm, ok := m[c].(map[string]any)
				if !ok || cm["agents_md"] != want {
					return nil, nil, i18n.Errorf("kitinit.manifest.err.agents_md", "file", kitLoopManifest, "name", name, "key", c, "want", want)
				}
			}
		}
		// hook の実体は looptrack の中（looptrack hook <名前>）で、kit/loop にファイルが無い（以前の版の kit は
		// hooks/*.sh を持っていた）。rules・skill などはファイルがあること
		if kind != "hook" {
			full := "kit/loop/" + name
			found := false
			if _, ok := files[full]; ok {
				found = true
			}
			for n := range files {
				if strings.HasPrefix(n, full+"/") {
					found = true
				}
			}
			if !found {
				return nil, nil, i18n.Errorf("kitinit.manifest.err.no_file", "file", kitLoopManifest, "name", name)
			}
		}
		e.Matcher, _ = m["matcher"].(string)
		e.Timeout = 10
		if t, ok := m["timeout"].(float64); ok && t != 0 {
			e.Timeout = int(t)
		}
		e.Order, _ = m["order"].(float64)
		out = append(out, e)
	}
	return version, out, nil
}

func sortedEntries(entries []loopEntry) []loopEntry {
	out := append([]loopEntry(nil), entries...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].idx < out[j].idx
	})
	return out
}

// want は配線の 1 件（イベント・matcher・hook の名前・コマンド・timeout）。
type want struct {
	Event, Matcher, ID, Cmd string
	Timeout                 int
	PS                      string // Copilot の powershell / windows のフィールド
	// Lead は、そのイベントの配列が無ければ hooks の先頭側に作る（Copilot の合成の SessionStart。core の summary が
	// 最初に作っていた位置を保つ）。
	Lead bool
}

// loopWants は manifest の hook の配線（order 順）。agent が claude-code 以外なら、その AI の列（null・無しは配線しない）に従う。
// Go 版に無い hook（登録表に無い名前）は配線せずに skipped に入れる。
func loopWants(entries []loopEntry, w wiring) (out []want, skipped []string) {
	for _, e := range sortedEntries(entries) {
		if e.Kind != "hook" {
			continue
		}
		name := e.hookName()
		ev, matcher, noBlock := e.Event, e.Matcher, false
		if w.agent != "claude-code" {
			c := e.col(w.agent)
			if c == nil {
				continue
			}
			if v, _ := c["event"].(string); v != "" {
				ev = v
			}
			if m, ok := c["matcher"]; ok {
				matcher, _ = m.(string)
			}
			if b, ok := c["block"].(bool); ok && !b {
				noBlock = true
			}
		}
		if _, ok := loop.Lookup(name); !ok {
			skipped = append(skipped, e.Name)
			continue
		}
		extra := ""
		if noBlock {
			extra = " --no-block"
		}
		out = append(out, want{Event: ev, Matcher: matcher, ID: name, Cmd: w.hook(name, extra, false), PS: w.hookPS(name, extra, false), Timeout: e.Timeout})
	}
	return out, skipped
}

// loopCounts は問いの文面の hook・rules・skill の本数（hook は manifest から数える）。
func loopCounts(files map[string]string) (hooks, rules, skills int) { return kit.LoopCounts(files) }

// loopDest は kit/loop のファイルを Go 版の init が置く場所（rules・skills だけ。置かないものは ""）。
func loopDest(kitName string) string {
	rest := strings.TrimPrefix(kitName, "kit/loop/")
	top, sub, _ := strings.Cut(rest, "/")
	switch {
	case top == "rules" && sub != "":
		return loopRulesDir + "/" + sub
	case top == "skills" && strings.Contains(sub, "/"):
		return ".claude/skills/" + sub
	}
	return ""
}

// loopSkillDirs は kit/loop の skill のディレクトリ（.claude/skills/<名前>）。
func loopSkillDirs(files map[string]string) []string {
	seen := map[string]bool{}
	var out []string
	for n := range files {
		rest := strings.TrimPrefix(n, "kit/loop/")
		if parts := strings.Split(rest, "/"); len(parts) >= 3 && parts[0] == "skills" && !seen[parts[1]] {
			seen[parts[1]] = true
			out = append(out, ".claude/skills/"+parts[1])
		}
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------- AGENTS.md の loop 節

var injectHeading = regexp.MustCompile(`^(#{1,6})` + spaceClass + `+(.*)$`)

// spaceClass は以前の CLI（1.0.0 より前）の正規表現の \s と同じ空白の集合。
const spaceClass = `[\t\n\v\f\r\x{1c}-\x{1f} \x{85}\p{Z}]`

var injectAny = regexp.MustCompile(`^` + spaceClass + `*<!--` + spaceClass + `*looptrack:inject\b`)

type injected struct {
	level   int
	heading string
	body    []string
}

// injectSections は rules の本文から、見出しの直後（空行は飛ばす）に `<!-- looptrack:inject <mode> [AI…] -->` の印がある節を返す
// （以前の CLI（1.0.0 より前）・kit/loop/hooks/session-start-rules.sh と同じ規則）。
func injectSections(text, mode, agent string) []injected {
	marker := regexp.MustCompile(`^` + spaceClass + `*<!--` + spaceClass + `*looptrack:inject` + spaceClass + `+` + regexp.QuoteMeta(mode) +
		`((?:` + spaceClass + `+[a-z][a-z0-9-]*)*)` + spaceClass + `*-->` + spaceClass + `*$`)
	lines := splitlines(text)
	var out []injected
	fence := false
	for i := 0; i < len(lines); {
		line := lines[i]
		if strings.HasPrefix(trimSpaceLeft(line), "```") {
			fence = !fence
		}
		var m []string
		if !fence {
			m = injectHeading.FindStringSubmatch(line)
		}
		j := i + 1
		for m != nil && j < len(lines) && trimSpace(lines[j]) == "" {
			j++
		}
		var mk []string
		if m != nil && j < len(lines) {
			mk = marker.FindStringSubmatch(lines[j])
		}
		if mk != nil {
			if agents := strings.Fields(mk[1]); len(agents) > 0 && !has(agents, agent) {
				mk = nil
			}
		}
		if mk == nil {
			i++
			continue
		}
		level := len(m[1])
		var body []string
		k, inner := j+1, false
		for ; k < len(lines); k++ {
			l := lines[k]
			if strings.HasPrefix(trimSpaceLeft(l), "```") {
				inner = !inner
			}
			if !inner {
				if h := injectHeading.FindStringSubmatch(l); h != nil && len(h[1]) <= level {
					break
				}
			}
			if !injectAny.MatchString(l) && trimSpace(l) != "---" {
				body = append(body, l)
			}
		}
		for len(body) > 0 && trimSpace(body[0]) == "" {
			body = body[1:]
		}
		for len(body) > 0 && trimSpace(body[len(body)-1]) == "" {
			body = body[:len(body)-1]
		}
		if len(body) > 0 {
			out = append(out, injected{level, trimSpace(m[2]), body})
		}
		i = k
	}
	return out
}

// shiftHeadings はフェンスの外の見出しの階層を delta ずらす（1〜6 に収める）。
func shiftHeadings(lines []string, delta int) []string {
	var out []string
	fence := false
	for _, l := range lines {
		if strings.HasPrefix(trimSpaceLeft(l), "```") {
			fence = !fence
		}
		if !fence {
			if h := injectHeading.FindStringSubmatch(l); h != nil {
				n := min(6, max(1, len(h[1])+delta))
				out = append(out, strings.Repeat("#", n)+" "+h[2])
				continue
			}
		}
		out = append(out, l)
	}
	return out
}

// frontmatterValue は先頭の YAML frontmatter の key の値を 1 行にして返す。
func frontmatterValue(text, key string) string {
	lines := splitlines(text)
	if len(lines) == 0 || trimSpace(lines[0]) != "---" {
		return ""
	}
	re := regexp.MustCompile(`^` + regexp.QuoteMeta(key) + `:` + spaceClass + `*(.*)$`)
	for i := 1; i < len(lines); i++ {
		if trimSpace(lines[i]) == "---" {
			break
		}
		m := re.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		v := trimSpace(m[1])
		if has([]string{"", ">", "|", ">-", "|-"}, v) {
			var more []string
			for _, l := range lines[i+1:] {
				if trimSpace(l) == "---" || (l != "" && !isSpaceRune([]rune(l)[0])) {
					break
				}
				if s := trimSpace(l); s != "" {
					more = append(more, s)
				}
			}
			v = strings.Join(more, " ")
		}
		if r := []rune(v); len(r) >= 2 && r[0] == r[len(r)-1] && (r[0] == '"' || r[0] == '\'') {
			v = string(r[1 : len(r)-1])
		}
		return v
	}
	return ""
}

// pickLang は kit の本文を lang で選ぶ（EN なら en/ の訳、無ければ正本の日本語）。
func pickLang(files map[string]string, name string, lang i18n.Lang) (string, bool) {
	if lang != i18n.JA {
		if b, ok := files[kit.TranslatedName(name)]; ok {
			return b, true
		}
	}
	b, ok := files[name]
	return b, ok
}

// loopAgentsText は AGENTS.md の loop 節（以前の CLI（1.0.0 より前）と同じ）。
//
// Codex・Copilot は rules を hook で注入せず、この節に写す。写す本文は導入時の言語で選ぶ
// （AGENTS.md は生成物なので、言語を変えたら `looptrack issue init --loop` を打ち直す。
// Claude Code の経路は hook が実行時に選ぶので打ち直しは要らない）。
func loopAgentsText(files map[string]string, entries []loopEntry, agent string, lang i18n.Lang) string {
	parts := []string{"## ループエンジニアリング（loop）\n\n`looptrack issue init --loop` が入れた節。外すときは `looptrack issue init --remove-loop`。" +
		"rules は要点だけを載せる（全文は「全文:」のファイル。作業の前に読む）。\n"}
	var skills []string
	for _, e := range sortedEntries(entries) {
		full := "kit/loop/" + e.Name
		mode := ""
		if c := e.col(agent); c != nil {
			mode, _ = c["agents_md"].(string)
		}
		text, ok := pickLang(files, full, lang)
		switch {
		case e.Kind == "rules" && mode == "sections" && ok:
			title := ""
			for _, l := range splitlines(text) {
				if h := injectHeading.FindStringSubmatch(l); h != nil && len(h[1]) == 1 {
					title = trimSpace(h[2])
					break
				}
			}
			if title == "" {
				title = path.Base(e.Name)
			}
			dest := loopDest(full)
			if dest == "" {
				dest = full
			}
			block := []string{"### " + title, "", "全文: `" + dest + "`"}
			for _, s := range injectSections(text, "session", agent) {
				block = append(block, "", "#### "+s.heading, "")
				block = append(block, shiftHeadings(s.body, 4-s.level)...)
			}
			parts = append(parts, strings.Join(block, "\n")+"\n")
		case e.Kind == "skill" && mode == "description":
			p := full
			if _, ok := files[full]; !ok {
				p = full + "/SKILL.md"
			}
			body, _ := pickLang(files, p, lang)
			desc := frontmatterValue(body, "description")
			name := e.Name
			if strings.Contains(name, "/") {
				name = strings.Split(name, "/")[1]
			}
			dest := loopDest(p)
			if dest == "" {
				dest = p
			}
			skills = append(skills, fmt.Sprintf("- %s（手順: `%s`）: %s", name, dest, desc))
		}
	}
	if len(skills) > 0 {
		parts = append(parts, "### 手順（skill）\n\n"+strings.Join(skills, "\n")+"\n")
	}
	return strings.Join(parts, "\n")
}

// upsertLoopBlock は AGENTS.md の loop 節を入れる・差し替える。body が nil なら外す。
func upsertLoopBlock(p *Plan, rel string, body *string) {
	cur := p.Current(rel)
	begin, end := strings.Index(cur, loopBegin), strings.Index(cur, loopEnd)
	var next string
	switch {
	case begin != -1 && end > begin:
		rest := strings.TrimPrefix(cur[end+len(loopEnd):], "\n")
		head := cur[:begin]
		if body != nil {
			next = head + loopBegin + "\n" + *body + loopEnd + "\n" + rest
		} else {
			next = head + rest
			if trimSpace(next) != "" {
				next = strings.TrimRight(next, "\n") + "\n"
			} else {
				next = ""
			}
		}
	case body == nil:
		return
	default:
		next = ""
		if trimSpace(cur) != "" {
			next = strings.TrimRight(cur, "\n") + "\n\n"
		}
		next += loopBegin + "\n" + *body + loopEnd + "\n"
	}
	p.Text(rel, next)
}

// ---------------------------------------------------------------- 文字列の小道具（以前の CLI（1.0.0 より前）と同じ規則）

func isSpaceRune(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', 0x1c, 0x1d, 0x1e, 0x1f, ' ', 0x85, 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

func trimSpace(s string) string     { return strings.TrimFunc(s, isSpaceRune) }
func trimSpaceLeft(s string) string { return strings.TrimLeftFunc(s, isSpaceRune) }

// splitlines は行に分ける（区切りは除く。\r\n のほか \n・\r・\v・\f・U+001C-001E・U+0085・U+2028・U+2029 も区切り）。
func splitlines(s string) []string {
	parts := cli.SplitLinesKeep(s)
	out := make([]string, len(parts))
	for i, l := range parts {
		switch {
		case strings.HasSuffix(l, "\r\n"):
			l = l[:len(l)-2]
		case l != "":
			r := []rune(l)
			switch r[len(r)-1] {
			case '\n', '\r', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
				l = string(r[:len(r)-1])
			}
		}
		out[i] = l
	}
	return out
}
