package loop

import (
	"regexp"
	"strings"
)

// spaceClass は空白文字の類（正規表現の \s。Unicode の空白）に当たる文字の並び（文字クラスの中身）。
// Go の \s は ASCII の空白だけなので、全角空白（U+3000）などを以前の hook（1.0.0 より前）と同じに扱うために置き換える。
const spaceClass = `\t\n\v\f\r \x{1c}-\x{1f}\x{85}\p{Z}`

// compatRe は \s・\S を Unicode の空白の意味のまま RE2 でコンパイルする（それ以外はそのまま）。
func compatRe(pattern string) *regexp.Regexp {
	return regexp.MustCompile(compatPattern(pattern))
}

func compatPattern(p string) string {
	var sb strings.Builder
	inClass := false
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c == '\\' && i+1 < len(p) {
			n := p[i+1]
			switch {
			case n == 's' && inClass:
				sb.WriteString(spaceClass)
			case n == 's':
				sb.WriteString("[" + spaceClass + "]")
			case n == 'S' && !inClass:
				sb.WriteString("[^" + spaceClass + "]")
			default:
				sb.WriteByte(c)
				sb.WriteByte(n)
			}
			i++
			continue
		}
		switch {
		case c == '[' && !inClass:
			inClass = true
			sb.WriteByte(c)
			// 先頭の ^ と ] はクラスの一部
			if i+1 < len(p) && p[i+1] == '^' {
				sb.WriteByte('^')
				i++
			}
			if i+1 < len(p) && p[i+1] == ']' {
				sb.WriteByte(']')
				i++
			}
			continue
		case c == ']' && inClass:
			inClass = false
		}
		sb.WriteByte(c)
	}
	return sb.String()
}
