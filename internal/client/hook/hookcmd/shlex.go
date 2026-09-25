package hookcmd

import (
	"errors"
	"strings"
)

// shlex は SimpleCommands が使う、シェルと同じ規則（posix・区切りは Separators）の語の分け方。
// 空白は " \t\r"・語は空白で分ける・コメント記号は無し、で全部読んだときの語の並び
// （以前の CLI（1.0.0 より前）の分け方を 1 文字ずつ移したもの）。
//
// 改行は空白から外して区切りにしている。空の引用（""）は空の語になる。
// 引用符が閉じていない・末尾が \ のときは error。
func shlex(s string) ([]string, error) {
	toks, _, err := shlexTokens(s, posixEscape)
	return toks, err
}

// tokInfo は shlexTokens が語ごとに添える情報（語の並びそのものは shlex と同じ）。
type tokInfo struct {
	glued  bool // 空白を挟まずに次の語が続く（`2>&1` の `2` の直後の `>&`）
	quoted bool // 引用符を含む（`'2'>x` の `2` は記述子ではなく引数）
}

// posixEscape / winEscape は shlexTokens に渡すエスケープ文字（0 なら「エスケープ無し」）。
//
//   - posixEscape（\）: posix のシェル（Git Bash も含む）と同じ規則。`\` は次の 1 文字を打ち消す
//     （`C:\Users\x` は区切りを失って `C:Usersx` になる。引用符で囲まない Windows の絶対パスで
//     git ガードが素通りしていた原因そのもの）。
//   - winEscape（0）: PowerShell / cmd に渡された Windows の絶対パス（`C:\tools\git.exe`）を
//     1 語のまま保つための第 2 の規則（利用者・監督の決定「案 1」）。`\` を特別扱いせず、
//     ふつうの語の文字として読む。posix 側の判定は 1 ビットも変えず、この規則は git ガードが
//     posix の判定と**両方**に掛けて二重に見るときだけ使う（SimpleCommandsWin・CommandWordsWin）。
const (
	posixEscape rune = '\\'
	winEscape   rune = 0
)

// shlexTokens は shlex と同じ語の並びに、語ごとの tokInfo を添えて返す。
// escapeChar が 0 なら `\` をエスケープとして扱わない（winEscape。呼ぶ側は posixEscape / winEscape を渡す）。
func shlexTokens(s string, escapeChar rune) ([]string, []tokInfo, error) {
	escape := ""
	if escapeChar != 0 {
		escape = string(escapeChar)
	}
	const (
		whitespace = " \t\r"
		quotes     = `'"`
		escQuotes  = `"`
	)
	in := func(r rune, set string) bool { return r != 0 && strings.ContainsRune(set, r) }
	// 語に使える文字（区切りがあるときは ~-./*?= を足し、区切りの文字を除く）。語は空白で分けるので
	// 区切り・空白・引用符・\ 以外はすべて語に入る＝この判定は結果を変えない。

	rs := []rune(s)
	pos := 0
	var pushback []rune
	eof := false
	read := func() (rune, bool) {
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

	state := ' ' // ' '・'a'・'c'・引用符・'\\'。eof で終わり
	var toks []string
	var infos []tokInfo
	for !eof {
		var token strings.Builder
		hasToken := false // token が空でない
		quoted := false
		escapedState := ' '
		glued := false // 押し戻し（空白を挟まない次の語）で語が終わった
		add := func(r rune) { token.WriteRune(r); hasToken = true }
	read:
		for {
			r, ok := read()
			switch {
			case state == ' ':
				switch {
				case !ok:
					eof = true
					break read
				case in(r, whitespace):
					if hasToken || quoted {
						break read
					}
				case in(r, escape):
					escapedState = 'a'
					state = r
				case in(r, Separators):
					add(r)
					state = 'c'
				case in(r, quotes):
					state = r
				default: // whitespace_split
					add(r)
					state = 'a'
				}
			case in(state, quotes):
				quoted = true
				switch {
				case !ok:
					return nil, nil, errors.New("No closing quotation")
				case r == state:
					state = 'a'
				case in(r, escape) && in(state, escQuotes):
					escapedState = state
					state = r
				default:
					add(r)
				}
			case in(state, escape):
				if !ok {
					return nil, nil, errors.New("No escaped character")
				}
				// posix のシェルと同じく、引用符の中では引用符自身と \ だけを外せる
				if in(escapedState, quotes) && r != state && r != escapedState {
					add(state)
				}
				add(r)
				state = escapedState
			case state == 'a' || state == 'c':
				switch {
				case !ok:
					eof = true
					break read
				case in(r, whitespace):
					state = ' '
					if hasToken || quoted {
						break read
					}
				case state == 'c':
					if in(r, Separators) {
						add(r)
					} else {
						if !in(r, whitespace) {
							pushback = append(pushback, r)
							glued = true
						}
						state = ' '
						break read
					}
				case in(r, quotes):
					state = r
				case in(r, escape):
					escapedState = 'a'
					state = r
				case !in(r, Separators): // whitespace_split
					add(r)
				default:
					pushback = append(pushback, r)
					state = ' '
					if hasToken || quoted {
						glued = true
						break read
					}
				}
			}
		}
		tok := token.String()
		if !quoted && tok == "" && !hasToken {
			// posix で引用の無い空の語は None（終わり）。途中で None になるのは入力の終わりだけ
			if eof {
				break
			}
			continue
		}
		toks = append(toks, tok)
		infos = append(infos, tokInfo{glued: glued, quoted: quoted})
	}
	return toks, infos, nil
}
