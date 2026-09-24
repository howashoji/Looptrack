package loop

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/howashoji/looptrack/internal/hookio"
)

// execRun は os/exec でコマンドを起動する（Runner の既定）。
func execRun(ctx context.Context, c Command) ([]byte, int, error) {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir = c.Dir
	if c.Env != nil {
		cmd.Env = c.Env
	}
	out, err := cmd.Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ctx.Err() == nil {
		return out, ee.ExitCode(), nil
	}
	if err != nil {
		return out, -1, err
	}
	return out, 0, nil
}

// run は e.Environ に足した環境でコマンドを起動する。
func (e *Env) run(ctx context.Context, dir string, extra []string, name string, args ...string) ([]byte, int, error) {
	env := append(append([]string(nil), e.Environ()...), extra...)
	return e.Run(ctx, Command{Dir: dir, Env: env, Name: name, Args: args})
}

// selfExe は実行中の実行ファイル（テストで差し替える）。
var selfExe = os.Executable

// issueCLI は issue の CLI の起動のしかた（名前と前に付ける引数）。
// LOOPTRACK_LOOP_ISSUE_CLI（実行ファイルのパス）→ 実行中の looptrack（looptrack issue）。どちらも無ければ ok = false。
// 以前は、間に「<ルート>/.claude/scripts の下の以前の入口を起動する」段があった（入口の廃止で外した）。
func issueCLI(e *Env, r string) (name string, pre []string, ok bool) {
	if cli := e.env("LOOPTRACK_LOOP_ISSUE_CLI"); cli != "" {
		if cli = e.abs(cli); isFile(cli) {
			return cli, nil, true
		}
		return "", nil, false
	}
	if exe, err := selfExe(); err == nil && strings.HasPrefix(strings.ToLower(filepath.Base(exe)), "looptrack") {
		return exe, []string{"issue"}, true
	}
	return "", nil, false
}

// root はプロジェクトのルート（CLAUDE_PROJECT_DIR → cwd の git のルート → cwd。hookio が求めたもの）。
func root(ev hookio.Event, e *Env) string {
	if ev.ProjectDir != "" {
		return ev.ProjectDir
	}
	return e.Getwd()
}

// stateDir は状態の置き場（LOOPTRACK_LOOP_STATE_DIR → Codex（CLAUDE_PROJECT_DIR が無い）なら <ルート>/.codex → <ルート>/.claude）。
// bash 版は CODEX_THREAD_ID の有無で Codex を見分けた。Go 版は配線の --agent codex でも見分ける。
func stateDir(ev hookio.Event, e *Env) string {
	if d := e.env("LOOPTRACK_LOOP_STATE_DIR"); d != "" {
		return e.abs(d)
	}
	r := root(ev, e)
	if e.env("CLAUDE_PROJECT_DIR") == "" && (e.env("CODEX_THREAD_ID") != "" || (ev.Agent == hookio.Codex && !ev.AgentGuessed)) {
		return filepath.Join(r, ".codex")
	}
	return filepath.Join(r, ".claude")
}

// abs は相対パスを作業ディレクトリ（Env.Getwd）からのパスにする（bash 版は hook のプロセスの cwd から読んだ）。
func (e *Env) abs(p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(e.Getwd(), p)
}

var nonIDChar = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// sessionFile は session_id から記号を除いたもの（記録先がフォルダの外へ出ないように）。
// bash 版は session_id だけを読んだ。Go 版は Copilot CLI の camelCase の sessionId も読む。
func sessionFile(ev hookio.Event) string {
	v := toStr(ev.Raw["session_id"])
	if v == "" {
		v = toStr(ev.Raw["sessionId"])
	}
	return nonIDChar.ReplaceAllString(v, "")
}

// toolInput は tool_input（Copilot CLI の toolArgs を解いたもの）。ツール名の無い入力（手で動かしたとき）も tool_input を読む。
func toolInput(ev hookio.Event) map[string]any {
	if ev.Tool != nil && ev.Tool.Input != nil {
		return ev.Tool.Input
	}
	if m, ok := ev.Raw["tool_input"].(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

// toStr は値の文字列化（文字列・数・真偽値だけ。それ以外は ""）。
func toStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		if x == 0 {
			return ""
		}
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	case bool:
		if x {
			return "True"
		}
	}
	return ""
}

// realpath は実体のパス（存在する部分だけシンボリックリンクを解き、残りはそのまま足す）。
func realpath(p string) string {
	if p == "" {
		return p
	}
	a, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	rest := ""
	d := a
	for {
		if r, err := filepath.EvalSymlinks(d); err == nil {
			if rest == "" {
				return r
			}
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(d)
		if parent == d {
			return a
		}
		if rest == "" {
			rest = filepath.Base(d)
		} else {
			rest = filepath.Join(filepath.Base(d), rest)
		}
		d = parent
	}
}

// under は p が d かその下か（どちらも正規化済みの前提）。
func under(p, d string) bool {
	d = filepath.Clean(d)
	return p == d || strings.HasPrefix(p, strings.TrimSuffix(d, string(filepath.Separator))+string(filepath.Separator))
}

// relpath は base から見た相対パス（失敗したら元のパス）。区切りは / にそろえる: 結果は AI に示すコマンド
// （rm・echo … > ・bash …）に埋めるか、git の出力（常に /）と比べるため。Windows の Claude Code の Bash は Git Bash で、
// \ の区切りはエスケープとして消える（PowerShell・cmd も / の区切りを受け付ける）。
func relpath(p, base string) string {
	r, err := filepath.Rel(absClean(base), absClean(p))
	if err != nil {
		return filepath.ToSlash(p)
	}
	return filepath.ToSlash(r)
}

func absClean(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return a
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// readText はファイルを UTF-8 として読む（読めないバイトは U+FFFD に置き換える）。
func readText(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return decodeReplace(b), nil
}

func decodeReplace(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var sb strings.Builder
	for len(b) > 0 {
		r, n := utf8.DecodeRune(b)
		if r == utf8.RuneError && n <= 1 {
			sb.WriteRune(utf8.RuneError)
			b = b[1:]
			continue
		}
		sb.Write(b[:n])
		b = b[n:]
	}
	return sb.String()
}

// splitlines は文字列を行に分ける（\n・\r\n・\r・\v・\f・\x1c〜\x1e・\x85・U+2028・U+2029 で分け、区切りは残さない）。
func splitlines(s string) []string {
	var out []string
	rs := []rune(s)
	cur := []rune{}
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch r {
		case '\r':
			out = append(out, string(cur))
			cur = cur[:0]
			if i+1 < len(rs) && rs[i+1] == '\n' {
				i++
			}
		case '\n', '\v', '\f', '\x1c', '\x1d', '\x1e', '\u0085', '\u2028', '\u2029':
			out = append(out, string(cur))
			cur = cur[:0]
		default:
			cur = append(cur, r)
		}
	}
	if len(cur) > 0 {
		out = append(out, string(cur))
	}
	return out
}

// textLines はテキストとして開いたファイルの行（\r\n と \r を \n に揃え、行末の \n は残さない）。
func textLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.SplitAfter(s, "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if l == "" {
			continue
		}
		out = append(out, strings.TrimSuffix(l, "\n"))
	}
	return out
}

// trimSpace は両端から空白文字の類（Unicode の空白）を除く。
func trimSpace(s string) string { return strings.TrimFunc(s, isSpaceRune) }

func trimSpaceRight(s string) string { return strings.TrimRightFunc(s, isSpaceRune) }

func trimSpaceLeft(s string) string { return strings.TrimLeftFunc(s, isSpaceRune) }

func isSpaceRune(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// globMD は dir の *.md（名前順・「.」で始まるものとディレクトリを除く）。
func globMD(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		n := e.Name()
		if strings.HasPrefix(n, ".") || !strings.HasSuffix(n, ".md") {
			continue
		}
		out = append(out, filepath.Join(dir, n))
	}
	return out // os.ReadDir は名前順
}

// compileUser は利用者が環境変数で渡した正規表現（RE2 で読めなければ nil＝以前の hook と同じく無視）。
func compileUser(s string) *regexp.Regexp {
	if s == "" {
		return nil
	}
	rx, err := regexp.Compile(s)
	if err != nil {
		return nil
	}
	return rx
}
