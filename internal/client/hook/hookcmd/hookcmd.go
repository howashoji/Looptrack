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

// StripQuotes は引用符で囲まれた中身を落とす（閉じていない引用符はそのまま残す）。先に ' '、次に " " を落とす。
func StripQuotes(cmd string) string {
	return dropPairs(dropPairs(cmd, '\''), '"')
}

// dropPairs は q で囲まれた組を順に落とす（正規表現 q[^q]*q を空文字に置き換えるのと同じ）。
func dropPairs(s string, q byte) string {
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
		b.WriteString(s[:i])
		s = s[i+1+j+1:]
	}
	b.WriteString(s)
	return b.String()
}

// CommandText は実際に実行される語だけを返す。quotes = false ならヒアドキュメントの本文だけ落とす。
func CommandText(cmd string, quotes bool) string {
	text := StripHeredocs(cmd)
	if quotes {
		text = StripQuotes(text)
	}
	return text
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
