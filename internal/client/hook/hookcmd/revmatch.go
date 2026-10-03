package hookcmd

import (
	"io"
	"regexp"
	"regexp/syntax"
	"slices"
	"unicode/utf8"
)

// 「接頭辞 b[:i] の末尾が正規表現に当たるか」を、接頭辞の長さに依らない手間で調べる部品。
//
// 末尾が `$` の正規表現を b[:i] にそのまま掛けると、正規表現の機械は b[:i] を先頭から最後まで読む。
// 引用符ごとに接頭辞へ掛ける段（unwrapNestedShell）では、引用符の数 × 長さで 2 次になり、
// 数十 KB のコマンドで hook の打ち切り（4 秒）を越えて、判定を出さずに通していた。
//
// そこで正規表現を**逆向き**にして（連結の順・文字列の字の並び・`^` と `$` を入れ替える）、b[:i] を
// 末尾から字ごとに読ませる。逆向きの式は先頭が `^` で固定されるので、機械は当たる見込みが無くなった時点で
// 止まる（末尾から数語で止まる）。当たるかどうかは向きに依らないので、結果は前向きに掛けたときと同じになる。
// 同じになることは、前向きに掛ける以前の実装をテストに写して、ためた形と乱数の入力で突き合わせて確かめている。

// mustCompileReversed は pattern を逆向きにした正規表現を作る。
// 扱うのは連結・選択・繰り返し・文字の集合・文字列・`^`・`$` だけで、それ以外の空の幅の表明が出てきたら
// 作らずに止める（向きで意味の変わる表明を黙って取り違えないため）。
func mustCompileReversed(pattern string) *regexp.Regexp {
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		panic(err)
	}
	return regexp.MustCompile(reverseSyntax(re).String())
}

func reverseSyntax(re *syntax.Regexp) *syntax.Regexp {
	out := *re
	out.Sub = make([]*syntax.Regexp, len(re.Sub))
	for k, s := range re.Sub {
		out.Sub[k] = reverseSyntax(s)
	}
	out.Sub0 = [1]*syntax.Regexp{}
	switch re.Op {
	case syntax.OpConcat:
		slices.Reverse(out.Sub)
	case syntax.OpLiteral:
		out.Rune = slices.Clone(re.Rune)
		slices.Reverse(out.Rune)
	case syntax.OpBeginText:
		out.Op, out.Flags = syntax.OpEndText, out.Flags&^syntax.WasDollar
	case syntax.OpEndText:
		out.Op, out.Flags = syntax.OpBeginText, out.Flags&^syntax.WasDollar
	case syntax.OpEmptyMatch, syntax.OpCharClass, syntax.OpAnyCharNotNL, syntax.OpAnyChar,
		syntax.OpCapture, syntax.OpStar, syntax.OpPlus, syntax.OpQuest, syntax.OpRepeat, syntax.OpAlternate:
		// 向きに依らない
	default:
		panic("hookcmd: cannot reverse regexp element: " + re.String())
	}
	return &out
}

// endsWithMatch は b[:i] の末尾が fwd に当たるか（fwd は末尾が `$` の正規表現、rev はそれを逆向きにしたもの）を、
// rev で末尾から読んで調べる。fwd.Match(b[:i]) と同じ結果を返す。
func endsWithMatch(rev *regexp.Regexp, b []byte, i int) bool {
	return rev.MatchReader(&backwardRunes{b: b, p: i})
}

// backwardRunes は b[:p] を末尾から 1 字ずつ返す。字の切れ目は前から読んだときと同じになる
// （utf8.DecodeLastRune は、壊れた並びも前から読んだときと同じく 1 バイトずつ RuneError として返す）。
type backwardRunes struct {
	b []byte
	p int
}

func (r *backwardRunes) ReadRune() (rune, int, error) {
	if r.p <= 0 {
		return 0, 0, io.EOF
	}
	c, n := utf8.DecodeLastRune(r.b[:r.p])
	r.p -= n
	return c, n, nil
}
