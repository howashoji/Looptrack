package core

// イシュー鮮度ガード — 参照したイシューを更新しないままセッションを終えるのを防ぐ。
//
//	mark   UserPromptSubmit・PostToolUse: どのイシューに触れたかと、実作業をしたかをセッション（session_id）ごとに記録する。
//	check  Stop: 実作業があり、触れたイシューがセッション中に一度も更新されていなければ差し戻す（以前の exit 2 と同じ意味）。
//	ack    更新不要と判断したイシューを検査対象から外す（逃げ道）。
//	reset  このセッションの記録をすべて捨てる（逃げ道）。
//	show   いまの記録を表示する（デバッグ用）。
//
// 判定の粒度・記録の単位・サブエージェントを記録しないことなどは以前の hook（1.0.0 より前）と同じ。
// 「更新されたか」はサーバの更新イベント（GET /activity?ids=…&since=<セッションの開始>）で見る（ファイルモードは持たない）。
// サーバに聞けないときは黙って通す（fail-open）。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/hook/hookcmd"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/hookio"
	"github.com/howashoji/looptrack/internal/i18n"
)

const (
	noSession     = "_nosession" // session_id が取れない環境（Codex 等）の器
	staleScopeSec = 7 * 24 * 3600
	// freshnessTimeout は API の 1 要求の待ち時間（以前の hook と同じ 5 秒）。
	freshnessTimeout = 5 * time.Second
)

// guard は 1 回の呼び出しの鮮度ガードの状態。
type guard struct {
	c        *Call
	root     string
	issues   string // <ルート>/.claude/issues（直接の読み取りを参照に数える）
	state    string // <ルート>/.claude/.looptrack-freshness
	sessions string
	scope    string // いま扱っている器
}

func newGuard(c *Call) *guard {
	root := c.root()
	g := &guard{c: c, root: root, issues: filepath.Join(root, ".claude", "issues"), state: filepath.Join(root, ".claude", ".looptrack-freshness")}
	g.sessions = filepath.Join(g.state, "sessions")
	g.scope = filepath.Join(g.sessions, noSession)
	return g
}

func (g *guard) projectFile() string       { return filepath.Join(g.state, "project.json") }
func (g *guard) scopeFile(n string) string { return filepath.Join(g.scope, n) }

var scopeUnsafe = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

// scopeKey は session_id を器のディレクトリ名にする（パスとして安全な文字だけにする）。
func scopeKey(sid string) string {
	rs := []rune(scopeUnsafe.ReplaceAllString(sid, "_"))
	if len(rs) > 128 {
		rs = rs[:128]
	}
	key := strings.Trim(string(rs), ".")
	if key == "" {
		return noSession
	}
	return key
}

func (g *guard) useScope(sid string) { g.scope = filepath.Join(g.sessions, scopeKey(sid)) }

// cliScope はフック入力の無い CLI（ack・reset・show）が扱う器を選ぶ（CLAUDE_CODE_SESSION_ID の器、無ければ最後に記録された器）。
func (g *guard) cliScope() {
	sid := g.c.getenv("CLAUDE_CODE_SESSION_ID")
	if sid != "" && isDir(filepath.Join(g.sessions, scopeKey(sid))) {
		g.useScope(sid)
		return
	}
	latest, best := "", -1.0
	if ents, err := os.ReadDir(g.sessions); err == nil {
		for _, d := range ents {
			p := filepath.Join(g.sessions, d.Name())
			if t := mtime(p); isDir(p) && t > best {
				latest, best = d.Name(), t
			}
		}
	}
	if latest != "" {
		g.useScope(latest)
		return
	}
	g.useScope(sid)
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func mtime(p string) float64 {
	fi, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return float64(fi.ModTime().UnixNano()) / 1e9
}

// ------------------------------------------------------------------ API

// apiBase は LOOPTRACK_API_URLの末尾の / と /api/v1 を除いたもの。
func (c *Call) apiBase() string { return api.NormalizeURL(c.Vars.Value(env.APIURL)) }

// project はプロジェクトの slug（無ければ error ＝以前の CLI がここで異常終了した失敗）。
func (c *Call) project() (string, error) {
	if v := strings.TrimSpace(c.Vars.Value(env.Project)); v != "" {
		return v, nil
	}
	issues := filepath.Join(c.root(), ".claude", "issues")
	if fi, err := os.Lstat(issues); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		if t, err := os.Readlink(issues); err == nil {
			return filepath.Base(strings.TrimRight(t, `/\`)), nil
		}
	}
	return "", i18n.Errorf("core.freshness.err.no_project")
}

// get は API を GET する（待ち時間 d）。
func (c *Call) get(path string, d time.Duration) (any, error) {
	cl, err := api.NewWithTimeout(*c.Vars, d)
	if err != nil {
		return nil, err
	}
	return cl.Get(path)
}

// errAbort は以前の CLI がここで異常終了した場合。hook は何もせずに終える（fail-open）。
// errors.Is で見分ける印なので、同じ値を使い回す（i18n.Errorf が返す値は 1 つだけ作って共有する）。
var errAbort = i18n.Errorf("core.err.abort")

// abortish はトークンが無いなど、以前の CLI がここで異常終了した失敗か（fail-open の扱いは同じだが、握りつぶして先へ進まない）。
func abortish(err error) bool {
	var nt *api.NoTokenError
	var te *api.TimeoutError
	return errors.As(err, &nt) || errors.As(err, &te) || errors.Is(err, errAbort)
}

// projectConfig は {prefix, width, project, url}。API にプロジェクトを 1 度だけ聞いて控え（project.json）に残す（project_config）。
// 取れないときは nil（ガードを黙って無効にする）。プロジェクトが分からない・トークンが無いときは error（以前の CLI は異常終了した）。
func (g *guard) projectConfig() (*jsonorder.Object, error) {
	base := g.c.apiBase()
	if b, err := os.ReadFile(g.projectFile()); err == nil {
		if cached, err := jsonorder.DecodeObject(b); err == nil {
			if u, ok := cached.Get("url"); ok && u == any(base) {
				slug, err := g.c.project()
				if err != nil {
					return nil, err
				}
				if p, ok := cached.Get("project"); ok && p == any(slug) {
					return cached, nil
				}
			}
		}
	}
	slug, err := g.c.project()
	if err != nil {
		return nil, err
	}
	v, err := g.c.get("/projects/"+api.PathEscape(slug), freshnessTimeout)
	if err != nil {
		if abortish(err) {
			return nil, err
		}
		return nil, nil
	}
	pr, ok := v.(*jsonorder.Object)
	if !ok {
		return nil, nil
	}
	prefix, ok1 := pr.Get("prefix")
	width, ok2 := pr.Get("width")
	pslug, ok3 := pr.Get("slug")
	if !ok1 || !ok2 || !ok3 {
		return nil, nil // 必要なキーが無い
	}
	cfg := jsonorder.NewObject().Set("prefix", prefix).Set("width", width).Set("project", pslug).Set("url", base)
	if err := os.MkdirAll(g.state, 0o755); err == nil {
		_ = os.WriteFile(g.projectFile(), []byte(jsonASCII(cfg)), 0o644)
	}
	return cfg, nil
}

// jsonASCII は ASCII 以外を \uXXXX に逃がした JSON の形。
func jsonASCII(v any) string {
	s := jsonorder.Compact(v)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 0x80:
			b.WriteRune(r)
		case r > 0xFFFF:
			r -= 0x10000
			fmt.Fprintf(&b, `\u%04x\u%04x`, 0xD800+(r>>10), 0xDC00+(r&0x3FF))
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
	}
	return b.String()
}

// issueIDRe は prefix / width から、そのプロジェクトの ID だけに当たる照合を作る（issue_id_re）。作れなければ nil。
func (g *guard) issueIDRe() (*idMatcher, error) {
	cfg, err := g.projectConfig()
	if err != nil || cfg == nil {
		return nil, err
	}
	prefix, _ := cfg.Get("prefix")
	w := any(jsonorder.Number("4"))
	if x, ok := cfg.Get("width"); ok {
		w = x
	}
	width, ok := toInt(w)
	if !ok {
		return nil, errAbort // 整数にできない（以前の CLI はここで異常終了した）
	}
	if !jsonorder.Truthy(prefix) {
		return nil, nil
	}
	return &idMatcher{prefix: jsonorder.Str(prefix) + "-", width: int(width)}, nil
}

// toInt は値を整数にする（数と整数の文字列）。
func toInt(v any) (int64, bool) {
	if s, ok := v.(string); ok {
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		return n, err == nil
	}
	if b, ok := v.(bool); ok {
		if b {
			return 1, true
		}
		return 0, true
	}
	return jsonorder.Int(v)
}

// ------------------------------------------------------------------ 記録の器

// loadSession はいまの器の (session_id, started_epoch)。未記録なら ("", 0)。
func (g *guard) loadSession() (string, float64) {
	b, err := os.ReadFile(g.scopeFile("session"))
	if err != nil {
		return "", 0
	}
	sid, started, _ := strings.Cut(hookcmd.TrimSpace(string(b)), "\t")
	if started == "" {
		return sid, 0
	}
	f, err := strconv.ParseFloat(hookcmd.TrimSpace(started), 64)
	if err != nil {
		return "", 0
	}
	return sid, f
}

func dropScope(d string) {
	for _, n := range []string{"session", "engaged", "work", "acked"} {
		_ = os.Remove(filepath.Join(d, n))
	}
	_ = os.Remove(d) // 空でなければ残る（rmdir と同じ）
}

func (g *guard) sweepStaleScopes() {
	now := float64(g.c.Now().UnixNano()) / 1e9
	ents, err := os.ReadDir(g.sessions)
	if err != nil {
		return
	}
	for _, e := range ents {
		d := filepath.Join(g.sessions, e.Name())
		if d == g.scope || !isDir(d) || now-mtime(d) < staleScopeSec {
			continue
		}
		dropScope(d)
	}
}

// ensureSession は器を用意して started を返す（他セッションの記録は消さない）。
func (g *guard) ensureSession(sid string) (float64, error) {
	g.useScope(sid)
	if _, started := g.loadSession(); started != 0 {
		now := g.c.Now()
		_ = os.Chtimes(g.scope, now, now) // 最後に記録された器（cliScope の選択・掃除の基準）
		return started, nil
	}
	if err := os.MkdirAll(g.scope, 0o755); err != nil {
		return 0, err
	}
	started := float64(g.c.Now().UnixNano()) / 1e9
	label := sid
	if label == "" {
		label = "-"
	}
	if err := os.WriteFile(g.scopeFile("session"), []byte(fmt.Sprintf("%s\t%f\n", label, started)), 0o644); err != nil {
		return 0, err
	}
	g.sweepStaleScopes()
	return started, nil
}

// readLines はファイルの行（universal newlines）。
func readLines(p string) ([]string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	s := strings.ReplaceAll(strings.ReplaceAll(string(b), "\r\n", "\n"), "\r", "\n")
	lines := strings.SplitAfter(s, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines, nil
}

// engagedIDs は記録済みの参照 ID（読み出し側でも重複を潰す）。
func (g *guard) engagedIDs() []string {
	lines, err := readLines(g.scopeFile("engaged"))
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, ln := range lines {
		if i := hookcmd.TrimSpace(ln); i != "" && !seen[i] {
			seen[i] = true
			out = append(out, i)
		}
	}
	return out
}

// ackedIDs は ack で対象から外した ID（大文字に揃えたもの）。
// 外したものが次のターンで戻らないように、mark 側がこれを見て再登録を避ける。
func (g *guard) ackedIDs() map[string]bool {
	out := map[string]bool{}
	lines, err := readLines(g.scopeFile("acked"))
	if err != nil {
		return out
	}
	for _, ln := range lines {
		if i := hookcmd.TrimSpace(ln); i != "" {
			out[strings.ToUpper(i)] = true
		}
	}
	return out
}

func appendFile(p, text string) error {
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = f.WriteString(text)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func (g *guard) addEngaged(ids []string) error {
	known := map[string]bool{}
	for _, i := range g.engagedIDs() {
		known[i] = true
	}
	acked := g.ackedIDs() // 一度外したものは、同じセッションでは戻さない
	var add strings.Builder
	for _, i := range ids {
		if known[i] || acked[strings.ToUpper(i)] {
			continue
		}
		known[i] = true
		add.WriteString(i + "\n")
	}
	if add.Len() == 0 {
		return nil
	}
	if err := os.MkdirAll(g.scope, 0o755); err != nil {
		return err
	}
	return appendFile(g.scopeFile("engaged"), add.String())
}

func (g *guard) workEvents() []string {
	lines, err := readLines(g.scopeFile("work"))
	if err != nil {
		return nil
	}
	var out []string
	for _, ln := range lines {
		if hookcmd.TrimSpace(ln) != "" {
			out = append(out, strings.TrimRight(ln, "\n"))
		}
	}
	return out
}

func (g *guard) addWork(label string) error {
	for _, e := range g.workEvents() {
		if e == label {
			return nil
		}
	}
	if err := os.MkdirAll(g.scope, 0o755); err != nil {
		return err
	}
	return appendFile(g.scopeFile("work"), label+"\n")
}

// reset はいまの器を消す（他セッションの器は残す）。旧形式の記録と控えも消す。
func (g *guard) reset() {
	dropScope(g.scope)
	for _, n := range []string{"session", "engaged", "work", "acked", "project.json"} {
		_ = os.Remove(filepath.Join(g.state, n))
	}
}

// ------------------------------------------------------------------ mark

// escapeSubcmds は逃げ道のサブコマンド。
var escapeSubcmds = map[string]bool{"ack": true, "reset": true}

func isLooptrack(w string) bool {
	b := basename(w)
	return b == "looptrack" || b == "looptrack.exe"
}

// isEscapeCmd は `looptrack issue-freshness ack|reset` を実行するコマンドか
// （引用符の中に書いただけのものは数えない）。
func isEscapeCmd(cmd string) bool {
	cmds, ok := hookcmd.SimpleCommands(cmd)
	if !ok {
		text := hookcmd.CommandText(cmd, true)
		return strings.Contains(text, "looptrack issue-freshness ack") || strings.Contains(text, "looptrack issue-freshness reset")
	}
	for _, words := range cmds {
		for i := 0; i+1 < len(words); i++ {
			if isLooptrack(words[i]) && words[i+1] == "issue-freshness" && i+2 < len(words) && escapeSubcmds[words[i+2]] {
				return true
			}
		}
	}
	return false
}

// CLI の本文（自由記述）のオプション・ID のオプション・位置引数のうち本文のもの。
var (
	textOpts       = map[string]bool{"--comment": true, "--body": true, "--override": true, "--note": true, "--title": true, "-m": true, "--message": true}
	idOpts         = map[string]bool{"--parent": true, "--blocked-by": true, "--traces": true}
	textPositional = map[string]int{"comment": 1, "new": 0}
)

// bashEngagedIDs は Bash のコマンドから「参照した」イシューの ID を取り出す（bash_engaged_ids）。
func bashEngagedIDs(cmd string, pat *idMatcher) []string {
	cmds, ok := hookcmd.SimpleCommands(cmd)
	if !ok {
		text := hookcmd.CommandText(cmd, true)
		if strings.Contains(text, ".claude/issues") || strings.Contains(text, "looptrack issue") {
			return pat.findAll(text)
		}
		return nil
	}
	var ids []string
	for _, words := range cmds {
		direct := false
		for _, w := range words {
			if strings.Contains(w, ".claude/issues") {
				direct = true
				break
			}
		}
		if direct {
			for _, w := range words {
				ids = append(ids, pat.findAll(w)...)
			}
			continue
		}
		start := -1
		for i, w := range words {
			if isLooptrack(w) && i+1 < len(words) && words[i+1] == "issue" {
				start = i + 1
				break
			}
		}
		if start < 0 {
			continue
		}
		skip, idValue, sub, pos := false, false, "", 0
		hasSub := false
		for _, w := range words[start+1:] {
			if skip {
				skip = false
				continue
			}
			if idValue {
				idValue = false
				ids = append(ids, fullIDs(w, pat)...)
				continue
			}
			if textOpts[w] {
				skip = true
				continue
			}
			if idOpts[w] {
				idValue = true
				continue
			}
			if strings.HasPrefix(w, "-") {
				continue // `--x=値` の形の値は数えない
			}
			if !hasSub {
				sub, hasSub = w, true
				continue
			}
			pos++
			limit, ok := textPositional[sub]
			if ok && pos > limit {
				continue // `comment <ID> <本文>` の本文・`new <タイトル>` のタイトル
			}
			ids = append(ids, fullIDs(w, pat)...)
		}
	}
	return ids
}

func fullIDs(w string, pat *idMatcher) []string {
	var out []string
	for _, p := range strings.Split(w, ",") {
		if pat.fullMatch(p) {
			out = append(out, p)
		}
	}
	return out
}

var msgHeredocOpen = regexp.MustCompile(`^["']?\$\(` + spaceClass + `*cat` + spaceClass + `+<<-?` + spaceClass + `*["']?[A-Za-z_][A-Za-z0-9_]*["']?` + spaceClass + `*\)?\r?\n`)

// spaceClass は空白文字の類（正規表現の \s。Unicode）。
const spaceClass = `[\t\n\v\f\r \x{1c}-\x{1f}\x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}]`

// commitMessageHead は `git commit` のコミットメッセージの先頭行（無ければ ""。commit_message_head）。
func commitMessageHead(cmd string) string {
	if s, _ := gitWork(hookcmd.CommandText(cmd, true), "commit"); s < 0 {
		return ""
	}
	_, end := gitWork(cmd, "commit")
	if end < 0 {
		return ""
	}
	o := msgOpt(cmd, end)
	if o < 0 {
		return ""
	}
	rest := cmd[o:]
	if loc := msgHeredocOpen.FindStringIndex(rest); loc != nil {
		rest = rest[loc[1]:]
	}
	rest = strings.TrimLeft(rest, `"'`)
	lines := hookcmd.SplitLines(rest)
	if len(lines) == 0 {
		return ""
	}
	return hookcmd.TrimSpace(strings.TrimRight(lines[0], `"'`))
}

// workLabel は記録に残す実作業のラベル（実際に実行された git コマンド。work_label）。
func workLabel(text string, start int) string {
	seg := text[start:]
	if i := strings.IndexAny(seg, "\n;&|"); i >= 0 {
		seg = seg[:i]
	}
	for _, cut := range []string{`"`, "'", "$("} {
		seg, _, _ = strings.Cut(seg, cut)
	}
	rs := []rune(strings.Join(hookcmd.Fields(seg), " "))
	if len(rs) > 120 {
		rs = rs[:120]
	}
	return string(rs)
}

// isWorkEdit はイシュー本体・記録用マーカー・引き継ぎメモ（.claude の下）の編集を実作業に数えない。
func isWorkEdit(fp, cwd string) bool {
	if fp == "" {
		return false
	}
	sep := string(filepath.Separator)
	return !strings.Contains(abspath(fp, cwd)+sep, sep+".claude"+sep)
}

// ready はガードを動かせるか（API モード）。Go 版はファイルモードを持たない。
func (c *Call) ready() bool { return c.apiBase() != "" }

// subagent はサブエージェントのツール呼び出し（agent_id がある）か。
func subagent(ev hookio.Event) bool {
	return truthy(ev.Raw["agent_id"]) || truthy(ev.Raw["agentId"])
}

// harnessTags は、AI の harness が利用者のプロンプトに混ぜるブロックのタグ。
// 他のセッションからの連絡・system の注意書き・タスクの通知がこの形で入る。ここに名前が出ただけの
// イシューは「利用者が言及した」とは数えない（数えると、読んでもいないイシューを毎ターン外すことになる）。
// 当たるタグが無い AI では何も変わらない。
var harnessTags = []string{"cross-session-message", "system-reminder", "task-notification", "ci-monitor-event"}

// userPrompt は UserPromptSubmit の本文のうち、利用者が書いた部分。
// 取り除くタグは LOOPTRACK_FRESHNESS_IGNORE_TAGS（空白かカンマ区切り）で置き換えられる。
func userPrompt(s string, getenv func(string) string) string {
	tags := harnessTags
	if v := getenv("LOOPTRACK_FRESHNESS_IGNORE_TAGS"); strings.TrimSpace(v) != "" {
		tags = strings.FieldsFunc(v, func(r rune) bool {
			return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
		})
	}
	for _, t := range tags {
		s = stripTagged(s, t)
	}
	return s
}

// stripTagged は <tag …> から </tag> までを取り除く（入れ子は扱わない）。
// 閉じタグが見つからないとき（途中で切れているとき）はその先を残す（取りすぎない側に倒す）。
func stripTagged(s, tag string) string {
	open, closing := "<"+tag, "</"+tag+">"
	for {
		i := indexTagOpen(s, open)
		if i < 0 {
			return s
		}
		j := strings.Index(s[i:], closing)
		if j < 0 {
			return s
		}
		s = s[:i] + s[i+j+len(closing):]
	}
}

// indexTagOpen は open（"<tag"）で始まり、その次が「>」か空白である位置（無ければ -1）。
// 「<system-reminderX」のような別の名前のタグには当てない。
func indexTagOpen(s, open string) int {
	for from := 0; from < len(s); {
		i := strings.Index(s[from:], open)
		if i < 0 {
			return -1
		}
		i += from
		n := i + len(open)
		if n >= len(s) {
			return -1
		}
		switch s[n] {
		case '>', ' ', '\t', '\n', '\r':
			return i
		}
		from = n
	}
	return -1
}

// FreshnessMark は `looptrack hook issue-freshness-mark`（以前の hook の mark）。出力は無い。
func FreshnessMark(ctx context.Context, c *Call, ev hookio.Event) (hookio.Result, error) {
	if !c.ready() {
		return hookio.Result{}, nil
	}
	g := newGuard(c)
	pat, err := g.issueIDRe()
	if err != nil || pat == nil {
		return hookio.Result{}, err
	}
	if subagent(ev) {
		return hookio.Result{}, nil // サブエージェントの操作は親の記録に混ぜない
	}
	if _, err := g.ensureSession(rawString(ev.Raw, "session_id", "sessionId")); err != nil {
		return hookio.Result{}, err
	}
	if ev.Name == hookio.UserPromptSubmit {
		// 利用者が書いた部分だけを見る（他セッションからの連絡や system の注意書きに名前が出ただけの ID は参照に数えない）。
		return hookio.Result{}, g.addEngaged(pat.findAll(userPrompt(rawString(ev.Raw, "prompt"), c.getenv)))
	}
	t := ev.Tool
	if t == nil {
		return hookio.Result{}, nil
	}
	switch t.Kind {
	case hookio.KindBash:
		cmd := t.Command()
		// ack / reset を含むコマンドは参照を登録しない（同じコマンドの comment で ack した ID が戻るため）
		if !isEscapeCmd(cmd) {
			if err := g.addEngaged(bashEngagedIDs(cmd, pat)); err != nil {
				return hookio.Result{}, err
			}
			// コミットメッセージの先頭行の ID も参照とみなす
			if err := g.addEngaged(pat.findAll(commitMessageHead(cmd))); err != nil {
				return hookio.Result{}, err
			}
		}
		// 実作業の判定は「実際に実行される語」だけを見る
		run := hookcmd.CommandText(cmd, true)
		if s, _ := gitWork(run, "commit", "merge", "push"); s >= 0 {
			// 記録（work）はファイルに残って後から別の言語で読まれるので、文面ではなくキーと引数で書く（表示は workEvent が訳す）
			return hookio.Result{}, g.addWork("git\t" + workLabel(run, s))
		}
	case hookio.KindRead, hookio.KindEdit, hookio.KindWrite:
		fp := t.FilePath()
		cwd := c.Getwd()
		switch {
		case under(fp, g.issues, cwd):
			return hookio.Result{}, g.addEngaged(pat.findAll(basename(fp)))
		case t.Kind != hookio.KindRead && isWorkEdit(fp, cwd) && under(fp, g.root, cwd):
			// ルートの外（別 worktree 等）の変更は、このプロジェクトの作業として数えない
			rel, err := filepath.Rel(realpath(g.root, cwd), realpath(fp, cwd))
			if err != nil {
				return hookio.Result{}, err
			}
			return hookio.Result{}, g.addWork("file\t" + filepath.ToSlash(rel))
		}
	}
	return hookio.Result{}, nil
}

// staleIssue は参照したのに更新されていないイシュー。
type staleIssue struct{ id, status, title string }

// staleIssues は参照したのに、このセッション中に一度も更新されていないもの（stale_issues。サーバの更新イベントで見る）。
// サーバに聞けないときは空（止めない）。応答の形がおかしいときは error（以前の CLI はここで異常終了した）。
func (g *guard) staleIssues(started float64) ([]staleIssue, error) {
	ids := g.engagedIDs()
	if len(ids) == 0 {
		return nil, nil
	}
	q := make([]string, len(ids))
	for i, id := range ids {
		q[i] = api.PathEscape(id)
	}
	v, err := g.c.get("/activity?ids="+strings.Join(q, ",")+"&since="+jsonorder.FormatFloat(started), freshnessTimeout)
	if err != nil {
		if abortish(err) {
			return nil, err
		}
		return nil, nil // サーバに聞けないときは止めない（fail-open）
	}
	res, ok := v.(*jsonorder.Object)
	if !ok {
		return nil, errAbort // res.get が無い（AttributeError）
	}
	found := map[string]*jsonorder.Object{}
	if items, ok := res.Get("items"); ok {
		list, ok := items.([]any)
		if !ok {
			return nil, errAbort
		}
		for _, it := range list {
			a, ok := it.(*jsonorder.Object)
			if !ok {
				return nil, errAbort
			}
			id, ok := a.Get("id")
			s, isStr := id.(string)
			if !ok || !isStr {
				return nil, errAbort
			}
			found[strings.ToUpper(s)] = a
		}
	}
	var stale []staleIssue
	for _, iid := range ids {
		a := found[strings.ToUpper(iid)]
		if a == nil {
			continue // 存在しない ID
		}
		if x, _ := a.Get("closed"); jsonorder.Truthy(x) {
			continue // クローズ済み
		}
		n := any(jsonorder.Number("0"))
		if x, ok := a.Get("events_since"); ok {
			n = x
		}
		f, ok := jsonorder.Float(n)
		if !ok {
			return nil, errAbort // 数でないものとの比較（以前の CLI はここで異常終了した）
		}
		if f > 0 {
			continue // 更新済み
		}
		id, _ := a.Get("id")
		status, _ := a.Get("status")
		title, _ := a.Get("title")
		st := "?"
		if jsonorder.Truthy(status) {
			st = jsonorder.Str(status)
		}
		tt := ""
		if jsonorder.Truthy(title) {
			tt = jsonorder.Str(title)
		}
		stale = append(stale, staleIssue{jsonorder.Str(id), st, tt})
	}
	return stale, nil
}

// FreshnessCheck は `looptrack hook issue-freshness-check`（以前の hook の check）。
// 実作業があり、参照したイシューが更新されていなければ差し戻す（Result.Block）。
func FreshnessCheck(ctx context.Context, c *Call, ev hookio.Event) (hookio.Result, error) {
	if truthy(ev.Raw["stop_hook_active"]) || truthy(ev.Raw["stopHookActive"]) {
		return hookio.Result{}, nil
	}
	if !c.ready() || subagent(ev) {
		return hookio.Result{}, nil // サブエージェントの停止は検査しない（記録もしていない）
	}
	g := newGuard(c)
	g.useScope(rawString(ev.Raw, "session_id", "sessionId"))
	_, started := g.loadSession()
	if started == 0 {
		return hookio.Result{}, nil
	}
	events := g.workEvents()
	if len(events) == 0 {
		return hookio.Result{}, nil // 閲覧・相談だけのセッションでは何も言わない
	}
	stale, err := g.staleIssues(started)
	if err != nil {
		return hookio.Result{}, err
	}
	if len(stale) == 0 {
		g.reset()
		return hookio.Result{}, nil
	}
	cli, ack := escapeCommands(c.root())
	return hookio.Result{Block: staleMessage(c.lang(), events, stale, cli, ack)}, nil
}

// escapeCommands は差し戻しの文に出す CLI と逃げ道の呼び方。
// 以前の入口（1.0.0 より前の .claude/scripts の下のもの）は廃止したので、どの導入でも looptrack の呼び方で案内する。
func escapeCommands(string) (cli, ack string) {
	return "looptrack issue", "looptrack issue-freshness"
}

// workEvent は記録した実作業（キーと引数）を利用者の言語の 1 行にする。
//
// 知らないキー（以前の版が書いた「git 操作: …」のような文面そのもの）は、そのまま出す。
func workEvent(lang i18n.Lang, line string) string {
	kind, rest, found := strings.Cut(line, "\t")
	if !found {
		return line
	}
	switch kind {
	case "git":
		return i18n.T(lang, "core.freshness.work.git", "cmd", rest)
	case "file":
		return i18n.T(lang, "core.freshness.work.file", "path", rest)
	}
	return line
}

// staleMessage は差し戻しの理由（CLI と逃げ道の呼び方は escapeCommands）。
func staleMessage(lang i18n.Lang, events []string, stale []staleIssue, cli, ack string) string {
	rule := strings.Repeat("=", 60)
	var work []string
	for i, e := range events {
		if i == 8 {
			break
		}
		work = append(work, "   • "+workEvent(lang, e))
	}
	if len(events) > 8 {
		work = append(work, "   • "+i18n.T(lang, "core.freshness.stop.more", "n", len(events)-8))
	}
	var list []string
	for _, s := range stale {
		list = append(list, fmt.Sprintf("   • %s [%s] %s", s.id, s.status, s.title))
	}
	body := i18n.T(lang, "core.freshness.stop.reason", "rule", rule,
		"work", strings.Join(work, "\n"), "stale", strings.Join(list, "\n"), "cli", cli, "ack", ack)
	return rule + "\n" + body + "\n" + rule
}

// ------------------------------------------------------------------ CLI（looptrack issue-freshness …）

// FreshnessMain は `looptrack issue-freshness <mark|check|ack|reset|show> …`（以前の hook の入口に当たる）。戻り値は終了コード。
// mark・check は hook と同じ（Main に渡す）。ack・reset・show は以前の hook と同じ振る舞い・終了コード（文面は looptrack の呼び方に直した）。
func FreshnessMain(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, e *Env) (code int) {
	e = e.withDefaults()
	lang := e.lang()
	if len(args) == 0 {
		io.WriteString(stderr, i18n.T(lang, "core.freshness.usage"))
		return 1
	}
	switch args[0] {
	case "mark":
		return Main(ctx, "issue-freshness-mark", args[1:], stdin, stdout, stderr, e)
	case "check":
		return Main(ctx, "issue-freshness-check", args[1:], stdin, stdout, stderr, e)
	case "ack", "reset", "show":
	default:
		io.WriteString(stderr, i18n.T(lang, "core.freshness.usage"))
		return 1
	}
	defer func() {
		// 不具合でも作業を止めない（以前の hook と同じく、理由を stderr に書いて 0）
		if p := recover(); p != nil {
			fmt.Fprintf(stderr, "issue-freshness: %v\n", p)
			code = 0
		}
	}()
	c := &Call{Env: e, Stderr: stderr}
	g := newGuard(c)
	var err error
	switch args[0] {
	case "ack":
		if len(args) == 1 {
			io.WriteString(stderr, i18n.T(lang, "core.freshness.usage.ack")+"\n")
			return 1
		}
		err = g.ack(args[1:], stdout)
	case "reset":
		g.cliScope()
		g.reset()
		io.WriteString(stdout, i18n.T(lang, "core.freshness.reset.done")+"\n")
	case "show":
		err = g.show(stdout)
	}
	if err != nil {
		// i18n.Error の Error() は ID を返すので、表示は必ず i18n.Text を通す
		fmt.Fprintf(stderr, "issue-freshness: %s\n", i18n.Text(lang, err))
	}
	return 0
}

func (g *guard) ack(ids []string, w io.Writer) error {
	g.cliScope()
	drop := map[string]bool{}
	for _, a := range ids {
		drop[strings.ToUpper(a)] = true
	}
	var keep []string
	for _, i := range g.engagedIDs() {
		if !drop[strings.ToUpper(i)] {
			keep = append(keep, i)
		}
	}
	if len(keep) == 0 {
		_ = os.Remove(g.scopeFile("engaged"))
	} else if err := os.WriteFile(g.scopeFile("engaged"), []byte(strings.Join(keep, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	sorted := make([]string, 0, len(drop))
	for d := range drop {
		sorted = append(sorted, d)
	}
	sort.Strings(sorted)
	// 外した ID を控える。控えないと、次のターンで同じ ID がプロンプトやコマンドに現れたとき、
	// mark が何も知らずに再登録し、利用者の「更新不要」の判断が毎ターン覆る。
	// 同じ ID を二度 ack しても控えは増やさない（外した記録が並ぶだけで意味が無い）。
	was := g.ackedIDs()
	var add []string
	for _, d := range sorted {
		if !was[d] {
			add = append(add, d)
		}
	}
	if len(add) > 0 {
		if err := appendFile(g.scopeFile("acked"), strings.Join(add, "\n")+"\n"); err != nil {
			return err
		}
	}
	fmt.Fprintf(w, "%s\n", i18n.T(g.c.lang(), "core.freshness.ack.done", "ids", strings.Join(sorted, ", ")))
	return nil
}

func (g *guard) show(w io.Writer) error {
	g.cliScope()
	lang := g.c.lang()
	sid, started := g.loadSession()
	if g.c.ready() {
		cfg, err := g.projectConfig()
		if err != nil {
			return err
		}
		project := "?"
		if cfg != nil {
			if p, ok := cfg.Get("project"); ok {
				project = jsonorder.Str(p)
			}
		}
		fmt.Fprintf(w, "%s\n", i18n.T(lang, "core.freshness.show.mode.api", "url", g.c.apiBase(), "project", project))
	} else {
		fmt.Fprintf(w, "%s\n", i18n.T(lang, "core.freshness.show.mode.none", "name", env.Name(env.APIURL)))
	}
	if sid == "" {
		sid = i18n.T(lang, "core.freshness.show.none")
	}
	fmt.Fprintf(w, "session : %s\n", sid)
	fmt.Fprintf(w, "scope   : %s\n", g.scope)
	st := "-"
	if started != 0 {
		st = time.Unix(0, int64(started*1e9)).Local().Format("2006-01-02 15:04:05")
	}
	fmt.Fprintf(w, "started : %s\n", st)
	engaged := strings.Join(g.engagedIDs(), ", ")
	if engaged == "" {
		engaged = i18n.T(lang, "core.freshness.show.none")
	}
	fmt.Fprintf(w, "engaged : %s\n", engaged)
	if a := g.ackedIDs(); len(a) > 0 {
		ids := make([]string, 0, len(a))
		for i := range a {
			ids = append(ids, i)
		}
		sort.Strings(ids)
		fmt.Fprintf(w, "acked   : %s\n", strings.Join(ids, ", "))
	}
	events := g.workEvents()
	fmt.Fprintf(w, "%s\n", i18n.T(lang, "core.freshness.show.work", "n", len(events)))
	for _, e := range events {
		fmt.Fprintf(w, "          • %s\n", workEvent(lang, e))
	}
	if started != 0 {
		var st []staleIssue
		if g.c.ready() {
			var err error
			if st, err = g.staleIssues(started); err != nil {
				return err
			}
		}
		ids := make([]string, len(st))
		for i, s := range st {
			ids[i] = s.id
		}
		s := strings.Join(ids, ", ")
		if s == "" {
			s = i18n.T(lang, "core.freshness.show.none")
		}
		fmt.Fprintf(w, "stale   : %s\n", s)
	}
	return nil
}
