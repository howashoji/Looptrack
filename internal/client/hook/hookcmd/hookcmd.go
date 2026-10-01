// Package hookcmd は、hook が受け取る「コマンド文字列」から実際に実行される語だけを取り出す。
//
// ヒアドキュメントの本文と引用符の中はデータであってコマンドではない。これを区別せずに照合すると、コミットメッセージの本文に
// 書いた語でガードが誤発火したり、JSON の文字列の中の `git commit` を実作業と数えたりする（どちらも実際に起きた）。
// 規則は以前の hook（1.0.0 より前）と同じ（同じ入力に対する結果の記録と突き合わせる＝ hookcmd_test.go）。
//
//   - CommandText(cmd, true): 実行される語だけ（ヒアドキュメントの本文と引用符の中を落とす）
//   - CommandText(cmd, false): ヒアドキュメントの本文だけ落とす（引数の中身は残す）
//   - SimpleCommands(cmd): 引用符を外した語の並びを、区切り（; & | ( ) < > 改行）ごとに返す（シェルと同じ分け方）
//   - CommandWords(cmd): SimpleCommands と同じ分け方で、リダイレクト（記述子・演算子・行き先）を語から外したもの
//   - CommandTexts(cmd): CommandText(cmd, true) と、その Windows の読み方（CommandTextWin。`\` を打ち消しに使わない）の
//     両方。名前が Win で終わるもの（StripQuotesWin・SimpleSegmentsWin・CommandWordsWin など）は同じ処理の Windows の読み方
//
// 以前の CLI（1.0.0 より前）の文字列の規則（行の分け方・両端の空白の除き方・空白文字の類）に合わせるための部品も置く
// （SplitLines・IsSpace など）。起動した AI の判定は internal/hookio の ForeignHost にある。
package hookcmd

import (
	"strings"
	"unicode"
)

// StripHeredocs はヒアドキュメントの本文と終端行を落とす（開き行は残す＝そこはコマンドのため）。
func StripHeredocs(cmd string) string {
	var out []string
	delim := ""
	inBody := false
	for _, line := range SplitLines(cmd) {
		if inBody {
			// 終端行は行頭から始まる（`<<-` のときは前置の空白を許す）
			if TrimSpace(line) == delim {
				inBody = false
			}
			continue
		}
		out = append(out, line)
		if d, ok := HeredocOpen(line); ok {
			delim, inBody = d, true
		}
	}
	return strings.Join(out, "\n")
}

// HeredocOpen は行の中で最初に `<<-?\s*(['"]?)([A-Za-z_][A-Za-z0-9_]*)\1` に当たるものの終端の名前。
// 後方参照は RE2 に無いので手で照合する（この形では後戻りしても別の一致は生まれない）。
func HeredocOpen(line string) (string, bool) {
	rs := []rune(line)
	for i := 0; i+1 < len(rs); i++ {
		if rs[i] != '<' || rs[i+1] != '<' {
			continue
		}
		j := i + 2
		if j < len(rs) && rs[j] == '-' {
			j++
		}
		for j < len(rs) && IsSpace(rs[j]) {
			j++
		}
		var q rune
		if j < len(rs) && (rs[j] == '\'' || rs[j] == '"') {
			q = rs[j]
			j++
		}
		if j >= len(rs) || !isIdentStart(rs[j]) {
			continue
		}
		k := j + 1
		for k < len(rs) && isIdent(rs[k]) {
			k++
		}
		if q != 0 && (k >= len(rs) || rs[k] != q) {
			continue
		}
		return string(rs[j:k]), true
	}
	return "", false
}

func isIdentStart(r rune) bool { return r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' }
func isIdent(r rune) bool      { return isIdentStart(r) || r >= '0' && r <= '9' }

// ── 引用符の中を落とす（2 つの読み方）────────────────────────────────────────
//
// 同じ文字列でも、どのシェルに渡るかで引用符の組の取り方が違う。hook にはどのシェルが実行するかが分からない
// （Claude Code の Bash は Windows でも Git Bash だが、Copilot CLI の powershell ツールも同じ経路で届く）ので、
// **判定する側は 2 つの読み方の両方で読み、どちらかで当たれば当たりとする**（git ガードの posix / Windows 規則の
// 二重化と同じ考え方）。
//
//   - posix の読み方（StripQuotes・QuotedTexts・SimpleSegments）: 左から 1 回で数える。引用符の外と二重引用符の中では
//     バックスラッシュが次の 1 文字を打ち消す（`\"` は組を開きも閉じもしない）。一重引用符の中は打ち消さない。
//     sh では `git commit -m "x\" ; git reset --hard \""` は全体が 1 つの引数で、reset は実行されない。
//   - Windows の読み方（StripQuotesWin・QuotedTextsWin・SimpleSegmentsWin）: バックスラッシュを打ち消しに使わない。
//     PowerShell では `\` はただの文字なので、同じ文字列の `"x\"` で組が閉じ、後ろの reset が実行される。
//     組の取り方は以前の hook のまま（先に ' ' の組、次に残りへ " " の組）で、**1 ビットも変えない**。
//     両方で読んで和を取る限り、読み方を 1 つにしていたときより当たりが減らない（通す側が広がらない）のはこのため。
//
// 組の取り方は、落とす側（StripQuotes*）・中身を読む側（QuotedTexts*）・単純コマンドに分ける側（SimpleSegments*）で
// 読み方ごとに 1 か所にまとめてある。ずれると、落とす側だけが直って読む側が同じ組を見失う。

// StripQuotes は posix の読み方で、引用符で囲まれた中身を落とす（閉じていない引用符から後ろはそのまま残す）。
func StripQuotes(cmd string) string {
	out, _ := scanPosix(cmd)
	return out
}

// QuotedTexts は posix の読み方で、引用符の組の中身を順に返す（StripQuotes が落とすのと同じ組）。
func QuotedTexts(text string) []string {
	_, inner := scanPosix(text)
	return inner
}

// scanPosix は posix の読み方の本体。引用符の外の文字をつないだものと、組の中身の並びを返す。
//
// 左から 1 回で数えるので、種類の違う引用符が交ざっても組を取り違えない（`"it's" ; cat … ; echo 'x'` の `'` は
// 二重引用符の中の文字）。先に一重引用符の組だけを落とすと、`'s" ; cat … ; echo '` を 1 組と読み、
// 実行される cat が判定の文字列から消える。
func scanPosix(s string) (string, []string) {
	var b strings.Builder
	var inner []string
	for i := 0; i < len(s); {
		switch c := s[i]; c {
		case '\\':
			end := min(i+2, len(s))
			b.WriteString(s[i:end]) // 打ち消した 1 文字は引用符の外の文字として残す
			i = end
		case '\'', '"':
			j := indexUnescaped(s[i+1:], c, c == '"')
			if j < 0 {
				b.WriteString(s[i:]) // 閉じていない引用符。ここから後ろは区切りが信用できないので残す
				return b.String(), inner
			}
			inner = append(inner, s[i+1:i+1+j])
			i += j + 2
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), inner
}

// StripQuotesWin は Windows の読み方で、引用符で囲まれた中身を落とす（閉じていない引用符はそのまま残す）。
// 先に ' '、次に " " を落とす（以前の hook と同じ）。
func StripQuotesWin(cmd string) string {
	return dropPairs(dropPairs(cmd, '\'', nil), '"', nil)
}

// QuotedTextsWin は Windows の読み方で、引用符の組の中身を順に返す（StripQuotesWin が落とすのと同じ組）。
func QuotedTextsWin(text string) []string {
	var out []string
	for _, q := range []byte{'\'', '"'} {
		text = dropPairs(text, q, func(inner string) { out = append(out, inner) })
	}
	return out
}

// dropPairs は q で囲まれた組を順に落とす（正規表現 q[^q]*q を空文字に置き換えるのと同じ）。
// visit が nil でなければ、落とした組の中身を順に渡す。
func dropPairs(s string, q byte, visit func(string)) string {
	var b strings.Builder
	for {
		i := strings.IndexByte(s, q)
		if i < 0 {
			break
		}
		j := strings.IndexByte(s[i+1:], q)
		if j < 0 {
			break
		}
		if visit != nil {
			visit(s[i+1 : i+1+j])
		}
		b.WriteString(s[:i])
		s = s[i+1+j+1:]
	}
	b.WriteString(s)
	return b.String()
}

// indexUnescaped は s の中で最初に現れる q の位置（esc が真ならバックスラッシュの次の 1 文字は飛ばす）。
func indexUnescaped(s string, q byte, esc bool) int {
	for i := 0; i < len(s); i++ {
		if esc && s[i] == '\\' {
			i++
			continue
		}
		if s[i] == q {
			return i
		}
	}
	return -1
}

// SimpleSegments は posix の読み方で、引用符の外の区切り（; & | 改行 かっこ）で文字列を単純コマンドに分ける。
// 引用符の中の区切りでは切らない。閉じていない引用符があるときは ok = false（呼ぶ側は分けずに扱う）。
// 引用符の外と二重引用符の中ではバックスラッシュが次の 1 文字を打ち消す（scanPosix と同じ数え方）。
//
// 行末のコメント（語の先頭の # から行末まで）は、引用符の数えに入れない。`cp .env.example .env  # don't …`
// のようにコメントの中に ' があると、閉じていない引用符と誤認して単純コマンドに分けられなくなる
// （引用符の外で # が語の先頭に来たときだけコメントとして飛ばす。引用符の中の # はそのまま数える
// ので `echo '# don't'` のような形を誤って特別扱いしない）。
func SimpleSegments(raw string) ([]string, bool) { return segments(raw, true) }

// SimpleSegmentsWin は Windows の読み方で SimpleSegments と同じことをする（バックスラッシュを打ち消しに使わない。
// 以前の hook と同じ分け方）。
func SimpleSegmentsWin(raw string) ([]string, bool) { return segments(raw, false) }

// segments は SimpleSegments / SimpleSegmentsWin の本体。esc が真ならバックスラッシュが次の 1 文字を打ち消す
// （引用符の外と二重引用符の中）。
func segments(raw string, esc bool) ([]string, bool) {
	var out []string
	var q byte
	start := 0
	for i := 0; i < len(raw); {
		c := raw[i]
		if esc && (q == 0 || q == '"') && c == '\\' {
			i += 2 // 打ち消された 1 文字は読み飛ばす
			continue
		}
		if q == 0 && c == '#' && (i == 0 || isSegmentBreak(raw[i-1])) {
			nl := strings.IndexByte(raw[i:], '\n')
			if nl < 0 {
				break // コメントが文字列の終わりまで続く。その後ろに区切りは無い
			}
			i += nl // 次に読む位置は改行そのもの（改行は区切りとしてふだんどおり扱う）
			continue
		}
		if q != 0 {
			if c == q {
				q = 0
			}
			i++
			continue
		}
		switch c {
		case '\'', '"':
			q = c
		case ';', '&', '|', '\n', '(', ')':
			out = append(out, raw[start:i])
			start = i + 1
		}
		i++
	}
	if q != 0 {
		return []string{raw}, false
	}
	return append(out, raw[start:]), true
}

// isSegmentBreak は、その直後が語の先頭になる文字（空白と segments の区切り）。
func isSegmentBreak(c byte) bool {
	switch c {
	case ' ', '\t', '\r', '\n', ';', '&', '|', '(', ')':
		return true
	}
	return false
}

// CommandText は実際に実行される語だけを返す（posix の読み方）。quotes = false ならヒアドキュメントの本文だけ落とす。
// 判定に使うときは CommandTexts で両方の読み方を見る。
func CommandText(cmd string, quotes bool) string {
	text := StripHeredocs(cmd)
	if quotes {
		text = StripQuotes(text)
	}
	return text
}

// CommandTextWin は CommandText(cmd, true) の Windows の読み方（StripQuotesWin）。
func CommandTextWin(cmd string) string {
	return StripQuotesWin(StripHeredocs(cmd))
}

// CommandTexts は実際に実行される語を、posix の読み方・Windows の読み方の順に返す（同じなら 1 つだけ）。
// 判定する側はこの全部に掛け、どれかで当たれば当たりとする。
func CommandTexts(cmd string) []string {
	p, w := CommandText(cmd, true), CommandTextWin(cmd)
	if p == w {
		return []string{p}
	}
	return []string{p, w}
}

// Separators は単純コマンドの区切り。改行も区切りに含める。
const Separators = ";&|()<>\n"

// SimpleCommands はコマンド文字列を単純コマンドごとの語の並びに分ける（ヒアドキュメントの本文は先に落とす）。
// 引用符はシェルと同じく外して 1 語にする。引用符が閉じていないなど分解できないときは ok = false。
func SimpleCommands(cmd string) ([][]string, bool) {
	toks, err := shlex(StripHeredocs(cmd))
	if err != nil {
		return nil, false
	}
	return simpleCommandsFrom(toks), true
}

// SimpleCommandsWin は SimpleCommands と同じだが、`\` をエスケープとして扱わない（winEscape）。
// PowerShell / cmd に渡された引用符なしの Windows の絶対パス（`C:\tools\git.exe`）が、posix の規則
// （`\` は次の 1 文字を打ち消す）では区切りを失って 1 語のまま読めない（`C:\tools\git.exe` →
// `C:toolsgit.exe`）。git ガードが posix の判定と**両方**に掛けて二重に見るときだけ使う
// （利用者・監督の決定「案 1」）。
func SimpleCommandsWin(cmd string) ([][]string, bool) {
	toks, _, err := shlexTokens(StripHeredocs(cmd), winEscape)
	if err != nil {
		return nil, false
	}
	return simpleCommandsFrom(toks), true
}

// simpleCommandsFrom は SimpleCommands / SimpleCommandsWin の共通部分（語の並びを単純コマンドごとに分ける）。
func simpleCommandsFrom(toks []string) [][]string {
	out := [][]string{}
	var cur []string
	for _, t := range toks {
		if t != "" && allSeparators(t) {
			if len(cur) > 0 {
				out = append(out, cur)
			}
			cur = nil
			continue
		}
		cur = append(cur, t)
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// redirectOps はリダイレクトの演算子（区切りの文字だけでできた語のうち、単純コマンドを終わらせないもの）。
// ここに無い区切りの並び（`;`・`&&`・`|`・`<(` など）は SimpleCommands と同じく単純コマンドを終わらせる。
var redirectOps = map[string]bool{
	">": true, ">>": true, "<": true, "<>": true, ">&": true, "<&": true,
	"&>": true, "&>>": true, ">|": true, "<<": true, "<<<": true,
}

// CommandWords は SimpleCommands と同じ分け方で、**リダイレクトを語から外した**単純コマンドの語の並びを返す。
//
// SimpleCommands は `<` `>` も区切りとして数えるので、`git checkout main 2>&1` が `[git checkout main 2]` と `[1]` に
// 分かれ、記述子の `2` が引数に見える（パス指定の checkout と取り違えて deny になった）。ここでは
//
//   - 演算子の直前に空白を挟まずに付いた数字だけの語（`2>&1`・`2>/dev/null` の `2`）は記述子として落とす
//     （`git checkout main 2 >x` の `2` は空白で離れているので、シェルと同じく引数のまま残す。引用符付きの `'2'>x` も引数）
//   - 演算子の次の 1 語（`/dev/null`・`out.log`・`&1` の `1`・ヒアドキュメントの終端の名前）は行き先として落とす
//   - 演算子は単純コマンドを終わらせない（`git checkout >log main -- f` の `main -- f` は同じコマンドの引数）
//
// 引数の並びで判定する hook（git ガード）が使う。SimpleCommands の結果は以前の hook の記録と突き合わせてあるので変えない。
func CommandWords(cmd string) ([][]string, bool) {
	return commandWordsFrom(StripHeredocs(cmd), posixEscape)
}

// CommandWordsWin は CommandWords と同じだが、`\` をエスケープとして扱わない（winEscape）。
// SimpleCommandsWin と同じ理由で、git ガードが posix の判定と**両方**に掛けて二重に見るときだけ使う
// （利用者・監督の決定「案 1」）。
func CommandWordsWin(cmd string) ([][]string, bool) {
	return commandWordsFrom(StripHeredocs(cmd), winEscape)
}

// commandWordsFrom は CommandWords / CommandWordsWin の共通部分。
func commandWordsFrom(strippedCmd string, escapeChar rune) ([][]string, bool) {
	toks, infos, err := shlexTokens(strippedCmd, escapeChar)
	if err != nil {
		return nil, false
	}
	isSep := func(t string) bool { return t != "" && allSeparators(t) }
	out := [][]string{}
	var cur []string
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if isSep(t) {
			if redirectOps[t] {
				if i+1 < len(toks) && !isSep(toks[i+1]) {
					i++ // 行き先
				}
				continue
			}
			if len(cur) > 0 {
				out = append(out, cur)
			}
			cur = nil
			continue
		}
		if infos[i].glued && !infos[i].quoted && isDigits(t) && i+1 < len(toks) && redirectOps[toks[i+1]] {
			continue // 記述子（`2>&1` の `2`）
		}
		cur = append(cur, t)
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out, true
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func allSeparators(t string) bool {
	for _, r := range t {
		if !strings.ContainsRune(Separators, r) {
			return false
		}
	}
	return true
}

// IsSpace は空白文字の類（正規表現の \s。Unicode）の 1 文字版。Go の unicode.IsSpace に \x1c〜\x1f を足したもの。
func IsSpace(r rune) bool {
	return unicode.IsSpace(r) || r >= 0x1c && r <= 0x1f
}

// TrimSpace は両端から空白文字の類を除く。
func TrimSpace(s string) string { return strings.TrimFunc(s, IsSpace) }

// Fields は空白の並びで分ける（空の語を作らない）。
func Fields(s string) []string { return strings.FieldsFunc(s, IsSpace) }

// SplitLines は文字列を行に分ける（改行を含めない）。区切りは \n・\r\n・\r・\v・\f・\x1c〜\x1e・\x85・ ・ 。
// 末尾の区切りの後ろに空の行は作らない。
func SplitLines(s string) []string {
	var out []string
	rs := []rune(s)
	start := 0
	for i := 0; i < len(rs); i++ {
		switch rs[i] {
		case '\n', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			out = append(out, string(rs[start:i]))
			start = i + 1
		case '\r':
			out = append(out, string(rs[start:i]))
			if i+1 < len(rs) && rs[i+1] == '\n' {
				i++
			}
			start = i + 1
		}
	}
	if start < len(rs) {
		out = append(out, string(rs[start:]))
	}
	return out
}
