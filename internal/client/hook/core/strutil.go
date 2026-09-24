package core

// 以前の実装（1.0.0 より前）と同じ結果にするための小さな部品（真偽・文字列化・パスの扱い・正規表現の先読み / 後読みの手移植）。

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/howashoji/looptrack/internal/client/hook/hookcmd"
)

// truthy は JSON を解いた値の真偽（空でない文字列・0 でない数・true・空でない配列やオブジェクトが真）。
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	case json.Number:
		f, err := x.Float64()
		return err != nil || f != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

// toStr は JSON を解いた値の文字列化（数は整数なら整数の形。真偽は True / False、値なしは None ＝以前の CLI の出力と同じ形）。
// オブジェクト・配列は JSON の形で代える（使わない）。
func toStr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	case float64:
		if x == float64(int64(x)) && !strings.ContainsAny(strconv.FormatFloat(x, 'g', -1, 64), "e.") {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// rawString は入力の key の値（文字列のときだけ。無い・文字列でなければ ""）。
func rawString(raw map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := raw[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// basename はパスの最後の要素（POSIX は / だけ、Windows は \ も区切り）。
func basename(p string) string {
	i := strings.LastIndex(p, "/")
	if runtime.GOOS == "windows" {
		if j := strings.LastIndex(p, `\`); j > i {
			i = j
		}
	}
	return p[i+1:]
}

// abspath は絶対パス（作業ディレクトリ cwd からの絶対パスにして正規化する。シンボリックリンクは解かない）。
func abspath(p, cwd string) string {
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	return filepath.Clean(p)
}

// realpath は実体のパス（存在する部分のシンボリックリンクを解き、存在しない残りはそのまま付ける）。
func realpath(p, cwd string) string {
	p = abspath(p, cwd)
	rest := ""
	for {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Join(p, rest)
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

// under は path が base の下（base 自身を含む）か（両方を realpath にして比べる）。
func under(path, base, cwd string) bool {
	rel, err := filepath.Rel(realpath(base, cwd), realpath(path, cwd))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// isWordRune は語を作る文字（正規表現の \w。Unicode）の 1 文字版（英字・数字・_ と、Unicode の文字・数字）。
func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r)
}

// idMatcher はプロジェクトの ID だけに当たる正規表現 `(?<![A-Za-z0-9])<prefix>-[0-9]{width}(?![0-9])` の手移植
// （RE2 に先読み・後読みが無いため）。
type idMatcher struct {
	prefix string // "<prefix>-"
	width  int
}

func isASCIIAlnum(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z'
}
func isASCIIDigit(b byte) bool { return b >= '0' && b <= '9' }

// matchAt は s[i:] が ID の本体（前後の条件を除く）で始まれば、その終わりの位置。
func (m idMatcher) matchAt(s string, i int) (int, bool) {
	if !strings.HasPrefix(s[i:], m.prefix) {
		return 0, false
	}
	j := i + len(m.prefix)
	for k := 0; k < m.width; k++ {
		if j >= len(s) || !isASCIIDigit(s[j]) {
			return 0, false
		}
		j++
	}
	return j, true
}

// findAll は重ならない一致を左から集める。
func (m idMatcher) findAll(s string) []string {
	var out []string
	for i := 0; i <= len(s); {
		j, ok := m.matchAt(s, i)
		if ok && (i == 0 || !isASCIIAlnum(s[i-1])) && (j >= len(s) || !isASCIIDigit(s[j])) {
			out = append(out, s[i:j])
			if j == i {
				j++ // 空の一致（prefix が空になることは無いが念のため）
			}
			i = j
			continue
		}
		if i >= len(s) {
			break
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return out
}

// fullMatch は文字列全体が ID か。
func (m idMatcher) fullMatch(s string) bool {
	j, ok := m.matchAt(s, 0)
	return ok && j == len(s)
}

// gitWork は `\bgit\s+(?:subs)(?![\w-])` の最初の一致の位置（search。無ければ -1）。WORK_CMD・COMMIT_CMD の手移植。
func gitWork(s string, subs ...string) (start, end int) {
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], "git") && wordBoundaryBefore(s, i) {
			j := i + 3
			k := j
			for k < len(s) {
				r, size := utf8.DecodeRuneInString(s[k:])
				if !hookcmd.IsSpace(r) {
					break
				}
				k += size
			}
			if k > j {
				for _, sub := range subs {
					if strings.HasPrefix(s[k:], sub) {
						e := k + len(sub)
						if e >= len(s) {
							return i, e
						}
						r, _ := utf8.DecodeRuneInString(s[e:])
						if !isWordRune(r) && r != '-' {
							return i, e
						}
						break // 選択肢は先頭の文字が違うので、ほかは当たらない
					}
				}
			}
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return -1, -1
}

// wordBoundaryBefore は s[i]（語の文字）の前に \b があるか（前が語の文字でない・先頭）。
func wordBoundaryBefore(s string, i int) bool {
	if i == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return !isWordRune(r)
}

// msgOpt は `(?:^|\s)(?:-[a-zA-Z]*m|--message)(?:=|\s+)` を pos から search したときの一致の終わり（MSG_OPT。無ければ -1）。
// pos > 0 なので ^ は当たらない（位置を指定した検索でも ^ は文字列の本当の先頭だけに当たる）。
func msgOpt(s string, pos int) int {
	afterSep := func(k int) int { // (?:=|\s+) の終わり
		if k < len(s) && s[k] == '=' {
			return k + 1
		}
		e := k
		for e < len(s) {
			r, size := utf8.DecodeRuneInString(s[e:])
			if !hookcmd.IsSpace(r) {
				break
			}
			e += size
		}
		if e > k {
			return e
		}
		return -1
	}
	for j := pos; j < len(s); {
		r, size := utf8.DecodeRuneInString(s[j:])
		if hookcmd.IsSpace(r) {
			k := j + size
			if k < len(s) && s[k] == '-' {
				// -[a-zA-Z]*m: 英字の並びの最後が m（その後ろが = か空白）
				e := k + 1
				for e < len(s) && (s[e] >= 'a' && s[e] <= 'z' || s[e] >= 'A' && s[e] <= 'Z') {
					e++
				}
				for x := e; x > k+1; x-- {
					if s[x-1] == 'm' {
						if end := afterSep(x); end >= 0 {
							return end
						}
					}
				}
				if strings.HasPrefix(s[k:], "--message") {
					if end := afterSep(k + len("--message")); end >= 0 {
						return end
					}
				}
			}
		}
		j += size
	}
	return -1
}
