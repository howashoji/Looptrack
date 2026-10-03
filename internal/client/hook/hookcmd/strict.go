package hookcmd

import (
	"path"
	"strings"
)

// ── 3 つ目の読み方（止める側にだけ足す）──────────────────────────────────────────
//
// 以前の読み方（StripHeredocs）と狭い読み方（StripHeredocsNarrow）のどちらでも、次の形では本物のコマンドが
// 判定から消える（黙って素通りする）。
//
//   - 算術・パラメータ展開・添字の中の `<<`（`$((1<<y))`・`${x#<<EOF}`）を開きと読み、次の行を本文として落とす。
//   - 本文をシェルに渡すヒアドキュメント（`cat <<EOF | sh`・`bash <<EOF`）の本文を、データとして落とす。
//   - 語の分け方（shlex）が `$'…'` とコメントを知らないので、`echo $'it\'s'`・`echo x # it's` の後ろで
//     引用符が閉じないと読み、全体を分解できずに通す（git ガード）。
//
// ここに置く読み方は、判定する側が以前の読み方・狭い読み方の**後に追加で**掛けるためのもの。どれかで当たれば
// 当たりとするので、ここで何を読み違えても判定は以前より通す側に動かない（動くのは止める側だけ）。
// その代わり、ここで読み違えると誤って止める形が増えるので、止める形はシェルが実際に実行するものに絞る。

// StripHeredocsStrict は、ヒアドキュメントの本文を落とす 3 つ目の読み方。
//
//   - 行はシェルと同じく改行（\n）だけで分ける（SplitLines の \r・\v・U+2028 などは、シェルにとって行の区切りではない）。
//   - 開きと読むのは、狭い読み方と同じく引用符・$'…'・コメントの外の `<<` で、さらに算術（`$(( ))`・`(( ))`・`$[ ]`）・
//     パラメータ展開（`${ }`）・代入の添字（`a[ ]=`）の中でもないもの（HeredocLexer の Exprs）。
//   - 終端の名前はシェルと同じく 1 語として読み、引用を外す（shellDelimAt。`<<E"OF"`・`<<'E'OF`・`<<END-OF`・`<<\EOF`）。
//     名前の引用がその行で閉じなければ、シェルは後ろを全部 1 語の中と読んで何も実行しないので、その行から後ろを落とす。
//   - 1 行に開きが 2 つ以上あれば、本文を順に落とす（`cat <<A <<B`）。開き（`<<` と終端の名前）は行から外し、
//     終端の行も落とす（入れ子のシェルをほどいてからもう一度この読み方に掛けても、同じ結果になる）。
//   - 閉じない引用符か $( が残れば、それが開いた行から後ろを落とす（シェルはそこから後ろを実行しない）。
//   - 開きのある単純コマンドか、そこからパイプでつながる後ろのコマンドが本文をコマンドとして読むシェル
//     （runsStdin）なら、本文を落とさない（本文は実行される）。本文はそれだけで 1 つのスクリプトとして同じ読み方に
//     掛けてから残す（本文の中のヒアドキュメントの本文は落とし、本文の引用符は外のコマンドと組にしない）。
func StripHeredocsStrict(cmd string) string {
	var out, body []string
	var opsOut []int // Ops に渡した行ごとの、out の中の位置
	lx := HeredocLexer{Exprs: true}
	var pending []string
	exec := false
lines:
	for _, line := range strings.Split(cmd, "\n") {
		if len(pending) > 0 {
			if TrimSpace(line) != pending[0] {
				if exec {
					body = append(body, line)
				}
				continue
			}
			pending = pending[1:]
			if exec && len(body) > 0 {
				out = append(out, StripHeredocsStrict(strings.Join(body, "\n")))
			}
			body = nil
			continue
		}
		opsOut = append(opsOut, len(out))
		var delims []string
		var spans [][2]int
		for _, p := range lx.Ops(line) {
			d, end, ok, unclosed := shellDelimAt(line, p)
			if unclosed {
				break lines // 名前の引用が閉じない。下で、この行から後ろを落とす
			}
			if ok {
				delims = append(delims, d)
				spans = append(spans, [2]int{p, end})
			}
		}
		if len(delims) > 0 {
			pending, exec = delims, feedsShell(line)
		}
		// 開き（`<<` と終端の名前）は行から外す。もう一度この読み方に掛けても、本文の無い開きと読まないため
		for k := len(spans) - 1; k >= 0; k-- {
			line = line[:spans[k][0]] + line[spans[k][1]:]
		}
		out = append(out, line)
	}
	if exec && len(body) > 0 { // 終端の無い本文もシェルは実行する
		out = append(out, StripHeredocsStrict(strings.Join(body, "\n")))
	}
	// 閉じない引用符か $( があれば、その一番外のものが開いた行から後ろを落とす。シェルは行ごとに読み、閉じない組を
	// 含む行から後ろは構文の誤りで何も実行しない（その前の行は実行する）。
	if k := lx.UnclosedLine(); k >= 0 {
		out = out[:opsOut[k]]
	}
	return strings.Join(out, "\n")
}

// shellDelimAt は line[p:] が `<<` で始まるとき、終端の名前をシェルと同じく読む（`-` と空白を飛ばし、次の 1 語の
// 引用を外す）。end は語の終わりの位置。語が無ければ ok = false。語の中の引用がその行で閉じなければ unclosed = true。
func shellDelimAt(line string, p int) (delim string, end int, ok, unclosed bool) {
	i := p + 2
	if i < len(line) && line[i] == '-' {
		i++
	}
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	var b strings.Builder
	start := i
	for i < len(line) {
		c := line[i]
		switch {
		case c == ' ' || c == '\t' || strings.IndexByte(";&|()<>", c) >= 0:
			return b.String(), i, i > start, false
		case c == '\\':
			if i+1 < len(line) {
				b.WriteByte(line[i+1])
			}
			i += 2
		case c == '\'':
			j := strings.IndexByte(line[i+1:], '\'')
			if j < 0 {
				return "", 0, false, true
			}
			b.WriteString(line[i+1 : i+1+j])
			i += j + 2
		case c == '"':
			i++
			for {
				if i >= len(line) {
					return "", 0, false, true
				}
				if line[i] == '"' {
					i++
					break
				}
				if line[i] == '\\' && i+1 < len(line) && strings.IndexByte("$`\"\\", line[i+1]) >= 0 {
					i++
				}
				b.WriteByte(line[i])
				i++
			}
		case c == '$' && i+1 < len(line) && line[i+1] == '\'':
			end, body, closed := ansiCBody(line, i+2)
			if !closed {
				return "", 0, false, true
			}
			b.WriteString(body)
			i = end + 1
		case c == '$' && i+1 < len(line) && (line[i+1] == '(' || line[i+1] == '{'):
			// $( … ) と ${ … } は展開されずにそのまま名前の一部になる（かっこの組までを 1 語に含める）
			open, closeCh := line[i+1], byte(')')
			if open == '{' {
				closeCh = '}'
			}
			depth := 0
			k := i + 1
			for ; k < len(line); k++ {
				if line[k] == open {
					depth++
				} else if line[k] == closeCh {
					depth--
					if depth == 0 {
						break
					}
				}
			}
			if k >= len(line) {
				return "", 0, false, true
			}
			b.WriteString(line[i : k+1])
			i = k + 1
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), i, i > start, false
}

// feedsShell は、開きのある行で、ヒアドキュメントを受け取る単純コマンドか、そこからパイプでつながる後ろの
// コマンドが、標準入力をコマンドとして読むか（runsStdin）。行を語に分けられない（閉じない引用符がある）ときは
// 見ない（false。以前の読み方と同じく本文を落とす）。コメントの中の `| sh` は数えない（ShlexFriendly で落とす）。
func feedsShell(line string) bool {
	toks, infos, err := shlexTokens(ShlexFriendly(line), posixEscape)
	if err != nil {
		return false
	}
	sep := func(k int) bool { return toks[k] != "" && !infos[k].quoted && allSeparators(toks[k]) }
	type elem struct {
		words   []string
		heredoc bool
	}
	var pipe []elem
	var cur elem
	runs := func() bool {
		seen := false
		for _, e := range append(pipe, cur) {
			seen = seen || e.heredoc
			if seen && runsStdin(e.words) {
				return true
			}
		}
		return false
	}
	for k := 0; k < len(toks); k++ {
		t := toks[k]
		if !sep(k) {
			if infos[k].glued && !infos[k].quoted && isDigits(t) && k+1 < len(toks) && redirectOps[toks[k+1]] {
				continue // 記述子（`2>&1` の `2`）
			}
			cur.words = append(cur.words, t)
			continue
		}
		switch {
		case redirectOps[t]:
			if t == "<<" {
				cur.heredoc = true
			}
			if k+1 < len(toks) && !sep(k+1) {
				k++ // 行き先・終端の名前
			}
		case t == "|" || t == "|&":
			pipe, cur = append(pipe, cur), elem{}
		case t == "(" || t == ")":
			// サブシェルのかっこはパイプラインを切らない（`(cat <<EOF) | sh`・`| (sh)`）
		default: // ; & && || ( ) など: パイプラインの終わり
			if runs() {
				return true
			}
			pipe, cur = nil, elem{}
		}
	}
	return runs()
}

// stdinShells は、引数にスクリプトも -c も無ければ標準入力をコマンドとして読むシェル。
// シェルを指す変数（"$SHELL" など）も同じに扱う。
var stdinShells = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "mksh": true,
	"yash": true, "ash": true, "posh": true, "fish": true, "csh": true, "tcsh": true,
	"$shell": true, "${shell}": true, "$bash": true, "${bash}": true}

// stdinPrefixes は、後ろのコマンドをそのまま実行する前置の語（選択肢と数・変数の指定は読み飛ばす）。
// `{`・`!` も、後ろのコマンドが同じ標準入力を読むので同じに扱う（`| { sh; }`）。
var stdinPrefixes = map[string]bool{"sudo": true, "doas": true, "env": true, "nohup": true, "exec": true,
	"command": true, "builtin": true, "time": true, "nice": true, "stdbuf": true, "setsid": true, "timeout": true,
	"caffeinate": true, "{": true, "!": true}

// stdinPrefixValueOpts は、前置の語の選択肢のうち値を次の語で取るもの（`env -u FOO sh` の FOO を飛ばす）。
var stdinPrefixValueOpts = map[string]map[string]bool{
	"env":     {"-u": true, "-C": true, "-S": true},
	"sudo":    {"-u": true, "-g": true, "-C": true, "-D": true, "-h": true, "-p": true, "-r": true, "-t": true, "-U": true},
	"doas":    {"-u": true, "-C": true},
	"timeout": {"-s": true, "-k": true},
	"nice":    {"-n": true},
}

// runsStdin は、単純コマンドの語の並びが、標準入力をコマンドとして読むか。
// シェル（stdinShells）でスクリプトの引数も -c も無いもの（-s と - は読む）と、`source` / `.` で
// /dev/stdin・/dev/fd/0・- を読むもの。前置の語（stdinPrefixes）と変数の指定は飛ばす。
func runsStdin(words []string) bool {
	i := 0
	for i < len(words) {
		if isAssignment(words[i]) {
			i++
			continue
		}
		pre := cmdName(words[i])
		if !stdinPrefixes[pre] {
			break
		}
		i++
		for i < len(words) && (strings.HasPrefix(words[i], "-") || isAssignment(words[i]) || isDigits(words[i])) {
			if stdinPrefixValueOpts[pre][words[i]] {
				i++ // 値
			}
			i++
		}
	}
	if i >= len(words) {
		return false
	}
	name, args := cmdName(words[i]), words[i+1:]
	if name == "source" || name == "." {
		return len(args) > 0 && (args[0] == "/dev/stdin" || args[0] == "/dev/fd/0" || args[0] == "-")
	}
	if !stdinShells[name] {
		return false
	}
	for k := 0; k < len(args); k++ {
		a := args[k]
		switch {
		case a == "-" || a == "-s":
			return true
		case a == "--":
			// 選択肢の終わり。後ろにスクリプトのファイルが無ければ標準入力を読む
		case a == "-o" || a == "+o":
			k++ // 値（-o pipefail）
		case strings.HasPrefix(a, "--"):
			// 長い選択肢（--norc など）
		case strings.HasPrefix(a, "-") || strings.HasPrefix(a, "+"):
			if strings.ContainsRune(a[1:], 'c') {
				return false // 文字列のスクリプト。本文はそのスクリプトの入力
			}
			if strings.ContainsRune(a[1:], 's') {
				return true
			}
		default:
			return false // スクリプトのファイル。本文はそのスクリプトの入力
		}
	}
	return true
}

// cmdName はコマンドの名前（パスと .exe を外し、小文字にしたもの）。
func cmdName(w string) string {
	n := strings.ToLower(path.Base(strings.ReplaceAll(w, `\`, "/")))
	return strings.TrimSuffix(n, ".exe")
}

// isAssignment は `名前=値` の形の語（コマンドの前の変数の指定）。
func isAssignment(w string) bool {
	k, _, ok := strings.Cut(w, "=")
	if !ok || k == "" || !isIdentStart(rune(k[0])) {
		return false
	}
	for _, r := range k {
		if !isIdent(r) {
			return false
		}
	}
	return true
}

// ShlexFriendly は、語の分け方（shlex）が知らない posix のシェルの書き方のうち 2 つを、意味の近い形に直す
// （posix の読み方。引用符の外と二重引用符の中ではバックスラッシュが次の 1 文字を打ち消す）。
//
//   - コメント（引用符と $'…' の外で、語の先頭の # から行末まで）を落とす。shlex はコメントを知らないので、
//     `echo x # it's` の ' を開きと数え、後ろの行を引用の中と読んで全体を分解できなくなる。
//   - $'…'（ANSI-C の引用）を一重引用符の組（中身に ' があれば二重引用符の組）に直す。shlex は $ の後ろの ' を一重引用符の開きと読み、中の \' で
//     組を閉じてしまう（`$'it\'s'` の後ろの s' を新しい開きと読む）。\' \" \\ \? は 1 文字に、ほかの \x はそのまま残す。
//
// $'…' が閉じていなければ、そこから後ろはそのまま残す（呼ぶ側の shlex が分解できずに通す。以前と同じ）。
// 判定する側は、以前の読み方の後に追加の読み方として使う（CommandWordsFriendly）。
func ShlexFriendly(s string) string {
	var b strings.Builder
	var stack []byte
	push := func(c byte) { stack = append(stack, c) }
	top := func() byte {
		if len(stack) == 0 {
			return 0
		}
		return stack[len(stack)-1]
	}
	pop := func() { stack = stack[:len(stack)-1] }
	next := func(i int) byte {
		if i+1 < len(s) {
			return s[i+1]
		}
		return 0
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch top() {
		case '\'':
			b.WriteByte(c)
			if c == '\'' {
				pop()
			}
			continue
		case '"':
			switch {
			case c == '\\' && i+1 < len(s):
				b.WriteString(s[i : i+2])
				i++
			case c == '"':
				pop()
				b.WriteByte(c)
			case c == '$' && next(i) == '(':
				push('(')
				b.WriteString("$(")
				i++
			case c == '`':
				push('`')
				b.WriteByte(c)
			default:
				b.WriteByte(c)
			}
			continue
		}
		// 引用符の外（一番外・$( … )・` … `・( … ) の中）
		switch {
		case c == '\\' && i+1 < len(s):
			b.WriteString(s[i : i+2])
			i++
		case c == '$' && next(i) == '\'':
			end, body, ok := ansiCBody(s, i+2)
			if !ok {
				b.WriteString(s[i:])
				return b.String()
			}
			b.WriteString(quoteWord(body))
			i = end
		case c == '\'' || c == '"':
			push(c)
			b.WriteByte(c)
		case c == '`':
			if top() == '`' {
				pop()
			} else {
				push('`')
			}
			b.WriteByte(c)
		case c == '$' && next(i) == '(':
			push('(')
			b.WriteString("$(")
			i++
		case c == '(':
			push('p')
			b.WriteByte(c)
		case c == ')':
			if t := top(); t == '(' || t == 'p' {
				pop()
			}
			b.WriteByte(c)
		case c == '#' && (i == 0 || isSegmentBreak(s[i-1])):
			nl := strings.IndexByte(s[i:], '\n')
			if nl < 0 {
				return b.String() // コメントが文字列の終わりまで続く
			}
			i += nl - 1 // 次に読むのは改行そのもの
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// quoteWord は body を 1 語として引用した形。' を含まなければ一重引用符、含めば二重引用符（\ " $ ` を打ち消す）で
// 囲む（一重引用符を閉じて打ち消した ' でつなぐ形にすると、入れ子のシェルをほどく段が最初の ' の組だけを中身と読み違える）。
func quoteWord(body string) string {
	if !strings.Contains(body, "'") {
		return "'" + body + "'"
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(body); i++ {
		if strings.IndexByte("\\\"$`", body[i]) >= 0 {
			b.WriteByte('\\')
		}
		b.WriteByte(body[i])
	}
	b.WriteByte('"')
	return b.String()
}

// ansiCBody は s[start:] を $'…' の中身として読み、閉じの ' の位置と中身を返す（閉じていなければ ok = false）。
func ansiCBody(s string, start int) (int, string, bool) {
	var body strings.Builder
	for k := start; k < len(s); k++ {
		switch c := s[k]; {
		case c == '\\' && k+1 < len(s):
			switch d := s[k+1]; d {
			case '\'', '"', '\\', '?':
				body.WriteByte(d)
			default:
				body.WriteString(s[k : k+2])
			}
			k++
		case c == '\'':
			return k, body.String(), true
		default:
			body.WriteByte(c)
		}
	}
	return 0, "", false
}

// CommandWordsWith は CommandWords のヒアドキュメントを strip（3 つ目の読み方など）で落としたもの。
func CommandWordsWith(cmd string, strip func(string) string) ([][]string, bool) {
	return commandWordsFrom(strip(cmd), posixEscape)
}

// CommandWordsWinWith は CommandWordsWin のヒアドキュメントを strip で落としたもの。
func CommandWordsWinWith(cmd string, strip func(string) string) ([][]string, bool) {
	return commandWordsFrom(strip(cmd), winEscape)
}

// CommandWordsFriendly は、strip でヒアドキュメントの本文を落とした後に ShlexFriendly で直してから、posix の
// 読み方で語に分ける（$'…' とコメントのある形を分解できるようにする追加の読み方）。strip には StripHeredocsStrict を
// 渡す（閉じない引用符の行から後ろを落とし、`<<\EOF` の本文もデータとして落とすので、新しく誤って止める形を作らない）。
func CommandWordsFriendly(cmd string, strip func(string) string) ([][]string, bool) {
	return commandWordsFrom(ShlexFriendly(strip(cmd)), posixEscape)
}
