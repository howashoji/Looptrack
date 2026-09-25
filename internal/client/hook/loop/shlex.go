package loop

import (
	"errors"
	"strings"
)

// shlexSplit は、シェルと同じ規則（posix・区切りは ";&|()"・語は空白で分ける）で全部読んだときの語の並び
// （以前の hook（1.0.0 より前）がコマンドを区切りに分けるのに使ったもの）。
// 引用符が閉じていない・末尾が \ のときは error。空の引用（""）は空の語になる。
//
// 語の中の # からは行末までを注釈として捨てる（bash とは違うが以前の hook と同じにする）。
func shlexSplit(s string) ([]string, error) {
	const (
		punct = ";&|()"
		space = " \t\r\n"
	)
	var (
		toks     []string
		pushback []rune
		rs       = []rune(s)
		pos      = 0
	)
	next := func() (rune, bool) {
		if n := len(pushback); n > 0 {
			r := pushback[n-1]
			pushback = pushback[:n-1]
			return r, true
		}
		if pos >= len(rs) {
			return 0, false
		}
		r := rs[pos]
		pos++
		return r, true
	}
	skipLine := func() {
		for pos < len(rs) {
			r := rs[pos]
			pos++
			if r == '\n' {
				return
			}
		}
	}
	isPunct := func(r rune) bool { return strings.ContainsRune(punct, r) }
	isSpace := func(r rune) bool { return strings.ContainsRune(space, r) }
	isQuote := func(r rune) bool { return r == '\'' || r == '"' }

	state := ' ' // ' '・'a'（語）・'c'（区切り）・引用符・'\\'・0（終わり）
	for state != 0 {
		var tok strings.Builder
		quoted := false
		escaped := ' '
		emit := false
		for !emit {
			r, ok := next()
			switch {
			case state == 0:
				emit = true
			case state == ' ':
				switch {
				case !ok:
					state = 0
					emit = true
				case isSpace(r):
					if tok.Len() > 0 || quoted {
						emit = true
					}
				case r == '#':
					skipLine()
				case r == '\\':
					escaped, state = 'a', '\\'
				case isPunct(r):
					tok.WriteRune(r)
					state = 'c'
				case isQuote(r):
					state = r
				default:
					tok.WriteRune(r)
					state = 'a'
				}
			case isQuote(state):
				quoted = true
				switch {
				case !ok:
					return nil, errors.New("shlex: unclosed quote") // 呼び出し元が握りつぶす内部のエラー。利用者に出ないので対訳にしない
				case r == state:
					state = 'a'
				case r == '\\' && state == '"':
					escaped, state = state, '\\'
				default:
					tok.WriteRune(r)
				}
			case state == '\\':
				if !ok {
					return nil, errors.New("shlex: trailing backslash") // 同上（内部のエラー）
				}
				if isQuote(escaped) && r != '\\' && r != escaped {
					tok.WriteRune('\\')
				}
				tok.WriteRune(r)
				state = escaped
			case state == 'a' || state == 'c':
				switch {
				case !ok:
					state = 0
					emit = true
				case isSpace(r):
					state = ' '
					if tok.Len() > 0 || quoted {
						emit = true
					}
				case r == '#':
					skipLine()
					state = ' '
					if tok.Len() > 0 || quoted {
						emit = true
					}
				case state == 'c':
					if isPunct(r) {
						tok.WriteRune(r)
					} else {
						if !isSpace(r) {
							pushback = append(pushback, r)
						}
						state = ' '
						emit = true
					}
				case isQuote(r):
					state = r
				case r == '\\':
					escaped, state = 'a', '\\'
				case !isPunct(r):
					tok.WriteRune(r)
				default:
					pushback = append(pushback, r)
					state = ' '
					if tok.Len() > 0 || quoted {
						emit = true
					}
				}
			}
		}
		t := tok.String()
		if t == "" && !quoted {
			if state == 0 {
				break
			}
			continue
		}
		toks = append(toks, t)
	}
	return toks, nil
}
