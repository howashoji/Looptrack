package hookcmd

import "strings"

// ── ヒアドキュメントの狭い読み方 ────────────────────────────────────────────
//
// StripHeredocs（以前の hook と同じ読み方）は、行の中で最初の `<<名前` をヒアドキュメントの開きと読む。そのため
// シェルが開きと読まない `<<`（ヒアストリング `<<<` の一部・引用符の中・コメントの中・打ち消した `\<<`）でも
// 次の行からを本文として落とし、そこにある本物のコマンドが判定から消える（黙って素通りする）。
//
// StripHeredocsNarrow は、シェルが演算子として読む `<<` だけを開きと読む。判定する側はこれを StripHeredocs の
// **代わりではなく、追加の読み方として**使い、どちらかで当たれば当たりとする（引用符の posix / Windows の
// 二重化と同じ考え方）。狭い読み方だけに替えると、以前の読み方で本文だった行が開きとして読み直されたり、
// 残った行の引用符が後ろの行と組になったりして、以前は止めていた形が通る側に動きうる。
// 以前の読み方を先に見て、当たらなかったときだけ狭い読み方を見る限り、判定は以前より通す側に動かない。
//
// 開きの形（終端の名前の書き方）は以前の読み方と同じ（heredocDelimAt）。`<<\EOF` のように以前の読み方が
// 開きと読まない形は、こちらでも開きと読まない。

// StripHeredocsNarrow は StripHeredocs の狭い読み方（演算子として読める `<<` だけを開きと読む）。
func StripHeredocsNarrow(cmd string) string {
	var out []string
	var lx HeredocLexer
	delim := ""
	inBody := false
	for _, line := range SplitLines(cmd) {
		if inBody {
			if TrimSpace(line) == delim {
				inBody = false
			}
			continue
		}
		out = append(out, line)
		for _, p := range lx.Ops(line) {
			if d, ok := heredocDelimAt([]rune(line[p:]), 0); ok {
				delim, inBody = d, true
				break
			}
		}
	}
	return strings.Join(out, "\n")
}

// HeredocLexer は、行をまたいで引用符とコマンド置換の入れ子を数え、`<<` が演算子として現れる位置を見分ける。
// 行を先頭から順に Ops に渡す。ヒアドキュメントの本文と終端の行は渡さない（本文の引用符は数えない）。
// 数え方は posix の読み方（引用符の外と二重引用符の中ではバックスラッシュが次の 1 文字を打ち消す）。
type HeredocLexer struct {
	stack []byte
	// Exprs が真なら、算術（$(( … ))・(( … ))・$[ … ]）・パラメータ展開（${ … }）・添字（${a[ … ]}・代入の a[ … ]=）の
	// 組も数え、その中の `<<` を演算子と読まない（シェルはそこをシフトや文字列として読み、開きとは読まない）。
	// 偽（ゼロ値）なら以前の数え方のまま（StripHeredocsNarrow はこちら）。
	Exprs bool

	line   int   // Ops に渡した行の数（次に渡す行の番号）
	openAt []int // stack と同じ深さで、その組が開いた行の番号
}

// inExpr は、t が算術・パラメータ展開・添字の組か（その中の `<<` と `#` は演算子でもコメントでもない）。
func inExpr(t byte) bool { return t == 'A' || t == '{' || t == '[' }

func (l *HeredocLexer) top() byte {
	if len(l.stack) == 0 {
		return 0
	}
	return l.stack[len(l.stack)-1]
}

func (l *HeredocLexer) push(c byte) {
	l.stack = append(l.stack, c)
	l.openAt = append(l.openAt, l.line)
}

func (l *HeredocLexer) pop() {
	l.stack = l.stack[:len(l.stack)-1]
	l.openAt = l.openAt[:len(l.openAt)-1]
}

// UnclosedLine は、ここまでに渡した行で閉じていない引用符（一重・二重・$'…'・バッククォート）か $( の組のうち、
// 一番外のものが開いた行の番号（Ops に渡した順に 0 から数える）。無ければ -1。
func (l *HeredocLexer) UnclosedLine() int {
	for k, c := range l.stack {
		switch c {
		case '\'', '"', 'e', '`', '(':
			return l.openAt[k]
		}
	}
	return -1
}

// Ops は line の中で、ヒアドキュメントの演算子として読める `<<` の位置（バイト）を返し、状態を行の終わりまで進める。
// 次の `<<` は演算子と読まない: 一重・二重引用符と `$'…'` の中・語の先頭の `#` から後ろ（コメント）・
// 3 つ以上並んだ `<`（ヒアストリング `<<<` とその続き）・バックスラッシュで打ち消した `<`。
// `$( … )` とバッククォートの組の中は、二重引用符の中にあっても演算子と読む（コマンド置換の中はコマンド）。
func (l *HeredocLexer) Ops(line string) []int {
	defer func() { l.line++ }()
	var out []int
	at := func(i int) byte {
		if i < len(line) {
			return line[i]
		}
		return 0
	}
	next := func(i int) byte { return at(i + 1) }
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch l.top() {
		case '\'':
			if c == '\'' {
				l.pop()
			}
			continue
		case 'e': // $'…'
			switch c {
			case '\\':
				i++
			case '\'':
				l.pop()
			}
			continue
		case '"':
			switch {
			case c == '\\':
				i++
			case c == '"':
				l.pop()
			case l.Exprs && c == '$' && next(i) == '(' && at(i+2) == '(':
				l.push('A') // "$(( … ))"
				i += 2
			case c == '$' && next(i) == '(':
				l.push('(')
				i++
			case c == '`':
				l.push('`')
			}
			continue
		}
		// 引用符の外（一番外・$( … )・` … `・( … ) の中）
		if l.Exprs && l.exprStep(line, &i) {
			continue
		}
		switch {
		case c == '\\':
			i++
		case c == '$' && next(i) == '\'':
			l.push('e')
			i++
		case c == '\'' || c == '"':
			l.push(c)
		case c == '`':
			if l.top() == '`' {
				l.pop()
			} else {
				l.push('`')
			}
		case c == '$' && next(i) == '(':
			l.push('(')
			i++
		case c == '(':
			l.push('p')
		case c == ')':
			if t := l.top(); t == '(' || t == 'p' {
				l.pop()
			}
		case c == '#' && (i == 0 || isSegmentBreak(line[i-1])):
			return out // 行の終わりまでコメント
		case c == '<' && next(i) == '<':
			n := 2
			for i+n < len(line) && line[i+n] == '<' {
				n++
			}
			if n == 2 {
				out = append(out, i)
			}
			i += n - 1
		}
	}
	return out
}

// exprStep は Exprs のときだけ見る組（算術・パラメータ展開・添字）を 1 文字ぶん進める。扱ったら true を返す
// （*i は扱った最後の文字を指す）。扱わない文字は Ops の以前の数え方に任せる。
func (l *HeredocLexer) exprStep(line string, i *int) bool {
	at := func(k int) byte {
		if k < len(line) {
			return line[k]
		}
		return 0
	}
	c, nx := line[*i], at(*i+1)
	switch {
	case c == '$' && nx == '(' && at(*i+2) == '(':
		l.push('A') // $(( … ))
		*i += 2
	case c == '(' && nx == '(':
		l.push('A') // (( … ))・for (( … ))
		*i++
	case c == '$' && nx == '{':
		l.push('{')
		*i++
	case c == '$' && nx == '[':
		l.push('[') // $[ … ]（古い算術）
		*i++
	case c == '[' && *i > 0 && isIdent(rune(line[*i-1])) && (l.top() == '{' || assignSubscript(line, *i)):
		l.push('[') // 添字（${a[1<<2]}・代入の a[1<<2]=3）。ふつうの語の [ はシェルにとってただの文字
	case c == '}' && l.top() == '{', c == ']' && l.top() == '[':
		l.pop()
	case c == ')' && l.top() == 'A':
		if nx == ')' {
			*i++
		}
		l.pop()
	case c == '<' && nx == '<' && inExpr(l.top()):
		for at(*i+1) == '<' {
			*i++ // シフトや文字列の一部。演算子と読まない
		}
	case c == '#' && inExpr(l.top()):
		// ${#x}・$((16#ff)) の # はコメントではない
	default:
		return false
	}
	return true
}

// assignSubscript は line[i] の `[` が代入の添字（`a[…]=`・`a[…]+=`）の開きか（同じ行で閉じの `]` の直後が = か +=）。
func assignSubscript(line string, i int) bool {
	j := strings.IndexByte(line[i:], ']')
	if j < 0 {
		return false
	}
	rest := line[i+j+1:]
	return strings.HasPrefix(rest, "=") || strings.HasPrefix(rest, "+=")
}

// CommandWordsNarrow は CommandWords のヒアドキュメントを狭い読み方（StripHeredocsNarrow）で落としたもの。
// 判定する側は CommandWords と両方に掛ける（CommandWords を先に見る）。
func CommandWordsNarrow(cmd string) ([][]string, bool) {
	return commandWordsFrom(StripHeredocsNarrow(cmd), posixEscape)
}

// CommandWordsWinNarrow は CommandWordsWin のヒアドキュメントを狭い読み方で落としたもの。
func CommandWordsWinNarrow(cmd string) ([][]string, bool) {
	return commandWordsFrom(StripHeredocsNarrow(cmd), winEscape)
}
