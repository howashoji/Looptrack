package hookcmd

// 包んだ形（前置の語・入れ子のシェル・eval）をほどいて、判定に掛ける文字列を作る段。**ここが唯一の置き場**で、
// 秘密のガード・git ガード・待ちループのガード（と、それを使う stop-runaway-background-process）・
// 別リポジトリの変更の確認（pre-tool-scope-guard。ほどく段だけ）が同じ関数を呼ぶ。
// 同じ規則を hook ごとに写すと、次に片方だけが直り、経路によって効いたり効かなかったりする状態が残る
// （実測: `bash -c '…'` は待ちループのガードでは止まり、秘密のガードでは素通りしていた。逆に前置の語は
// 秘密のガードだけが外していた。試した人は「どれかは止まった」と報告でき、止まらない組み合わせだけが残る）。
//
// いまある段は 2 つで、**どちらも単独で呼べる**。まとめて掛けるのが Normalize。
//
//   - StripCommandPrefixes: 前置の語（sudo・env・xargs …）を落とす（コマンドの位置だけ）
//   - UnwrapNestedShell:    入れ子のシェル（bash -c '…'・eval "…"）の中身を 1 段ほどく（位置は呼ぶ側が引数で選ぶ）
//
// 段を分けてあるのは、ガードごとに要る段だけを選んで呼べるようにするため。待ちループのガードは
// ほどく段だけを呼ぶ（前置の語の `timeout 600 …` はループの外からの上限なので、落とすと上限が見えなくなる）。
// 前置の語の並びを正規表現で当てたいときは PrefixRun を使う（語の一覧は PrefixWordsPattern の 1 か所）。

import (
	"bytes"
	"regexp"
	"strings"
)

// CmdAnyPos / CmdPath はコマンド名を当てるときの前置。ここに置くのは、同じ断片を
// 秘密のガードの正規表現とこのファイルの判定の両方が使うため（写すと片方だけずれる）。
//
//   - CmdAnyPos: 語の位置に依らない前置（行頭・区切りの直後・空白の直後）
//   - CmdPath:   コマンド名の前に付く実行ファイルのパス（/bin/cat・C:\Windows\System32\findstr.exe）
const (
	CmdAnyPos = `(?:^|[;&|(]\s*|\s)`
	CmdPath   = `(?:[^\s;&|()<>'"]*[/\\])?`
)

// PrefixWordsPattern は「後ろにコマンドが続く語」（前置の語の段が落とすもの）。
// 正規表現の選択肢としても語の集合としても使うので、並びはここだけに置く。
//
// `setsid`・`flock`・`script` も「後ろにコマンドが続く語」なので入れてある
// （`flock /tmp/l bash -c '…'`・`script -q /tmp/o bash -c '…'` を実測で確かめた）。
const PrefixWordsPattern = `sudo|doas|env|xargs|nohup|time|command|nice|stdbuf|timeout|setsid|flock|script`

// PrefixRun は前置の語とその選択肢の並びを当てる正規表現の部品（sudo ・ env FOO=1 ・ xargs -0 ・ timeout 30 ・
// nice -n 10 ）。0 回以上。選択肢は CmdOptWord（- で始まる語・変数の指定・数）だけを飲む。
// 前置の語の直後を「コマンドの位置」として正規表現で見たいガード（秘密のガードの大小を無視する照合・
// 待ちループのガードの until / while）が使う。
//
// **限界**: 値を別の語で取る選択肢（sudo -u deploy CAT …）は前置として読み切れないので、そこは拾えない。
// 「任意の語」に広げると、引数の位置の語（echo sh -c '…'・git grep while）までコマンドの位置になり誤発火する。
const PrefixRun = `(?:(?:` + PrefixWordsPattern + `)(?:\.exe)?(?:\s+(?:` + CmdOptWord + `))*\s+)*`

// OperandPrefixWords は、コマンドの前に作業対象を 1 語取る前置（`flock <ファイル> <コマンド>`・
// `script <ファイル> <コマンド>`）。ほかの前置の語は、選択肢でない語が来たらそこがコマンド。
const OperandPrefixWords = `flock|script`

var prefixWordSet = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Split(PrefixWordsPattern, "|") {
		m[w] = true
	}
	return m
}()

// Normalize は段をまとめて掛ける。**ほどく → 前置の語を落とす**の順。
//
// **前置の語を先に落としてはいけない。** 落とすのは語を空白に替える処理で、値を別の語で取る選択肢
// （`env -u FOO`・`flock /tmp/l`）はその値の語を残すので、残った語がコマンドの位置を塞ぐ。
// ほどく側は CmdHeadPos が前置の語とその後ろの語をまとめて飲むので、先に落とす必要がない。
// 落とすのを**後**にするのは、ほどいた中身の先頭にある前置の語（`bash -c 'sudo git …'`）のため。
//
// ほどく側を引数の位置でも当てると、`echo bash -c 'git reset --hard'`・`ls -l bash -c '…'`・
// `docker run img bash -c '…'` のように**何も実行されない形・別の場所で実行される形**まで止まる
// （git ガードは deny なので、そのガードについて書く作業そのものが止まる）。
func Normalize(cmd string, pos UnwrapPos) string {
	return StripCommandPrefixes(UnwrapNestedShell(cmd, pos))
}

// UnwrapPos は入れ子のシェルをどの位置で当てるか。**呼ぶ側が必ず明示する**（既定値に頼らない）。
//
// 分けるのは**誤発火の害が非対称**だから。`ask` は人がその場で通せるので、広く当てて取りこぼしを減らすほうがよい。
// `deny` は bypass permissions でも素通りしないので、何も実行されない形で止めると作業そのものが止まる。
// 処理は 1 つの関数に残り、分かれるのは引数だけ（規則を 2 か所に写さない）。
type UnwrapPos int

const (
	// HeadOnly はコマンドの位置だけで当てる（git ガード。deny なので狭く取る）。
	HeadOnly UnwrapPos = iota
	// AnyPos は位置を問わず当てる（秘密のガード。ask なので広く取る。基点もこの広さだった）。
	AnyPos
)

// ── 前置の語 ─────────────────────────────────────────────────────────────────

// StripCommandPrefixes はコマンドの位置にある前置の語とその選択肢を空白に替える。
//
// `sudo git reset --hard` の語を並べると先頭は `sudo` で、先頭が `git` かどうかしか見ないガードは
// 一度も中を検査しない。落としておけば、どの経路でも同じ語の並びとして見える。
//
// **落とすのはコマンドの位置だけ**（文字列の先頭か、引用符の外の区切りの直後）。引数の位置の語まで
// 落とすと `echo sudo git reset --hard` のような文で語の並びが変わる。引用符の中は読まない。
// 後ろに語が続かないとき（`env` 単体・`env | grep x`）は落とさない（それ自体がコマンドなので）。
//
// **限界**: 値を別の語で取る選択肢（`sudo -u deploy git …`・`env -u FOO git …`）は読み切れず、
// その語で止まる。止まった語が先頭になるだけなので、素通りはしても誤って止めることはない。
func StripCommandPrefixes(cmd string) string {
	b := []byte(cmd)
	atCmd := true
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c == '#' { // コメント（語の先頭の # から行末）。引用符の数え方は UnwrapNestedShell と同じ skipInert
			if n, ok := skipInert(b, i, true); ok {
				i = n - 1
				continue
			}
		}
		switch {
		case c == '\'' || c == '"':
			end := quoteEnd(b, i)
			if end < 0 {
				return string(b) // 閉じていない引用符。ここから先は区切りが信用できない
			}
			i = end
			atCmd = false
		case strings.IndexByte(Separators, c) >= 0:
			atCmd = true
		case c == ' ' || c == '\t' || c == '\r':
			// 語の間。atCmd は変えない
		default:
			if atCmd {
				i = stripPrefixRun(b, i) - 1
			} else {
				i = wordEnd(b, i) - 1
			}
			atCmd = false
		}
	}
	return string(b)
}

// stripPrefixRun は i から始まる語が前置の語の並びなら空白に替え、次に読む位置を返す。
func stripPrefixRun(b []byte, i int) int {
	for {
		we := wordEnd(b, i)
		if !prefixWordSet[prefixWordOf(string(b[i:we]))] {
			return we
		}
		// 選択肢の並び（-x・FOO=1・30）を飲む
		end := we
		for {
			os := wordStart(b, end)
			if os < 0 {
				break
			}
			oe := wordEnd(b, os)
			if !prefixOptRe.MatchString(string(b[os:oe])) {
				break
			}
			end = oe
		}
		next := wordStart(b, end)
		if next < 0 {
			return we // 後ろにコマンドが続かない。前置ではない
		}
		for k := i; k < end; k++ {
			if b[k] != '\n' {
				b[k] = ' '
			}
		}
		i = next
	}
}

// prefixWordOf は語を前置の語として読むための正規化（実行ファイルのパスと .exe を外し、小文字にする）。
func prefixWordOf(w string) string {
	if i := strings.LastIndexAny(w, `/\`); i >= 0 {
		w = w[i+1:]
	}
	return strings.TrimSuffix(strings.ToLower(w), ".exe")
}

// CmdOptWord は前置の語に続く選択肢の形（- で始まる語・変数の指定・数）。
// **ここ 1 か所**に置き、段 2 の語ごとの判定（prefixOptRe）と、ほどく側の位置の判定（cmdPrefixOne）の
// 両方が使う。2 か所に写すと、片方だけが緩んで引数の位置がコマンドの位置に見える。
const CmdOptWord = `-[^\s;&|()<>'"]*|[A-Za-z_][A-Za-z0-9_]*=[^\s;&|()<>'"]*|[0-9]+[smhd]?`

// prefixOptRe は CmdOptWord を 1 語に当てたもの。
var prefixOptRe = regexp.MustCompile(`^(?:` + CmdOptWord + `)$`)

// wordStart は p 以降で最初の語の先頭（空白を飛ばす）。区切り・引用符・終わりに当たったら -1。
func wordStart(b []byte, p int) int {
	for ; p < len(b); p++ {
		c := b[p]
		if c == ' ' || c == '\t' || c == '\r' {
			continue
		}
		if c == '\'' || c == '"' || strings.IndexByte(Separators, c) >= 0 {
			return -1
		}
		return p
	}
	return -1
}

// wordEnd は p から始まる語の終わり（空白・区切り・引用符の手前）。
func wordEnd(b []byte, p int) int {
	for ; p < len(b); p++ {
		c := b[p]
		if c == '\\' { // 打ち消した 1 文字は語の一部（`don\'t` の `'` で語を切らない）
			p++
			continue
		}
		if c == ' ' || c == '\t' || c == '\r' || c == '\'' || c == '"' || strings.IndexByte(Separators, c) >= 0 {
			return min(p, len(b))
		}
	}
	return len(b)
}

// ── 入れ子のシェル ───────────────────────────────────────────────────────────

// CmdHeadPos は**コマンドの位置**の前置。文字列の先頭・引用符の外の区切りの直後・`find … -exec` の直後から
// 始まり、そこから「後ろにコマンドが続くもの」の並びを飲む。先頭に空白を許すのは、前置の語を空白に
// 替えた後もコマンドの位置として読むため。
//
// 飲むのは 3 種類。**どれも「その次に来る語がコマンドになる」ものだけ**で、`echo`・`ls -l`・`ssh host`・
// `docker run img` のような**引数として渡すだけの語は入れない**（入れると、何も実行されない形まで当たる）。
//
//   - 複合コマンドの語: `if`・`then`・`else`・`elif`・`do`・`while`・`until`・`!`・`{`
//   - 変数の指定: `x=1`・`FOO=bar BAZ=1`
//   - 前置の語（PrefixWordsPattern）とその後ろの語: `env -u FOO`・`sudo -u deploy`・`xargs -a f`・
//     `flock /tmp/l`・`script -q /tmp/o`。**値を別の語で取る選択肢は読み切れない**ので、
//     前置の語の後ろは語の並びとして飲む（段 2 の空白への置き換えは語の位置を見るので、そちらでは飲めない）。
const (
	cmdKeywords  = `if|then|else|elif|do|while|until|!|\{`
	cmdAssign    = `[A-Za-z_][A-Za-z0-9_]*=[^\s;&|()<>'"]*`
	cmdHeadStart = `(?:^\s*|[;&|()\n]\s*|\s-exec(?:dir)?\s+)`
	cmdWord      = `[^\s;&|()<>'"]+`
	// cmdPrefixOne は前置の語 1 つぶん。**選択肢だけを飲み、選択肢でない語が来たら止まる**
	// （止まった語がコマンド）。値を別の語で取る短い選択肢だけ、値の 1 語を飲む。
	// **任意個の語を飲ませてはいけない**: `timeout 30 ssh host bash -c '…'` の `ssh`・`host` まで飲んで
	// `bash -c` がコマンドの位置に見え、遠隔で走る形が deny になる（実測で 24 形）。
	cmdPrefixOne = CmdPath + `(?i:` + PrefixWordsPattern + `)(?:\.exe)?` +
		`(?:\s+(?:` + CmdOptWord + `))*(?:\s+-[A-Za-z](?:` + cmdWord + `)?\s+` + cmdWord + `)?`
	// cmdOperandOne は、コマンドの前に作業対象を 1 語取る前置（flock <ファイル> …・script <ファイル> …）。
	cmdOperandOne = CmdPath + `(?i:` + OperandPrefixWords + `)(?:\s+(?:` + CmdOptWord + `))*(?:\s+` + cmdWord + `)?`

	cmdHeadRun = `(?:(?:` + cmdKeywords + `|` + cmdAssign +
		`|` + cmdPrefixOne + `|` + cmdOperandOne + `)\s+)*`

	CmdHeadPos = cmdHeadStart + cmdHeadRun
)

// nestedShellPrefixRe は、その直後の引用符の中身がそのまま「実行されるコマンド」になる前置
// （bash / sh / zsh / dash の -c と eval）。引用符の直前までの文字列に当てる。
//
// **当てるのはコマンドの位置だけ**（CmdHeadPos）。空白の直後まで当てると、`echo bash -c '…'` のように
// **何も実行されない形**や、`docker run img bash -c '…'`・`ssh host bash -c '…'` のように
// **守る対象の作業ツリーではない場所で実行される形**まで止まる。前置の語（sudo・xargs …）は
// Normalize が先に落とすので、`sudo bash -c '…'` はここに届く。
//
// **シェルの名前は大小を区別しない（`.exe` の部分も含めて）。** macOS と Windows のファイルシステムは
// 大小を区別しないので、`BASH -c 'git clean -fd'` は実際に実行される（実機で確かめた）。
// `.exe` だけを区別すると `Bash.exe` は拾えて `BASH.EXE` が落ちる、という半端な形になる。
// **`eval` だけは区別する。** シェルの組込みなので `EVAL` は実行されない（`command not found` を実測）。
//
// `-c` は**短い選択肢をまとめた形の最後**にも現れる（bash -lc・bash -ec・bash -xc）ので、`-[A-Za-z]*c` で見る。
// その前には、値を取らない選択肢（-x）と値を 1 語で取る選択肢（-o pipefail）の並びを許す。
var nestedShellPrefixHeadRe = regexp.MustCompile(CmdHeadPos + nestedShellBody)

// nestedShellPrefixAnyRe は位置を問わない版（秘密のガード）。CmdAnyPos は空白の直後にも当たる。
var nestedShellPrefixAnyRe = regexp.MustCompile(CmdAnyPos + nestedShellBody)

const nestedShellBody = CmdPath +
	`(?:(?i:(?:bash|sh|zsh|dash)(?:\.exe)?)` +
	`(?:\s+-[^\s;&|()<>'"]*(?:\s+[A-Za-z0-9_][^\s;&|()<>'"]*)?)*` +
	`\s+-[A-Za-z]*c|eval)\s+$`

// UnwrapNestedShell は `bash -c '…'`・`eval "…"` の引用符を `;` に替えて、中身を判定の対象に上げる。
//
// 判定は「引用符の中はデータ」という前提で組んであるが、入れ子のシェルではその前提が逆になり、
// **実際に実行されるコマンドだけが判定文字列から消える**（bash -c 'cat ~/.env' が素通りしていた）。
//
// 空白ではなく **`;`（単純コマンドの区切り）に替える**のが要点。空白にすると中身は `bash` のコマンドの
// 引数のままで、語の並びの先頭しか見ないガード（git ガード）には届かない。区切りにすれば、中身が
// それ自体で 1 つの単純コマンドになる。1 文字なのでほかの語の位置も動かない。
//
// 外すのは 1 段だけ（入れ子の入れ子は対象外）。閉じていない引用符に出会ったらそこで止める
// （区切りが信用できない）。
func UnwrapNestedShell(cmd string, pos UnwrapPos) string {
	re := nestedShellPrefixHeadRe
	if pos == AnyPos {
		re = nestedShellPrefixAnyRe
	}
	b := []byte(cmd)
	for i := 0; i < len(b); i++ {
		if n, ok := skipInert(b, i, pos == HeadOnly); ok {
			i = n - 1
			continue
		}
		q := b[i]
		if q != '\'' && q != '"' {
			continue
		}
		end := quoteEnd(b, i)
		if end < 0 {
			break
		}
		if re.Match(b[:i]) {
			b[i], b[end] = ';', ';'
		}
		i = end
	}
	return string(b)
}

// skipInert は、引用符の外で「引用符の数え方に効かない」ものを飛ばす。i がその先頭なら、次に読む位置と true を返す。
//
//   - バックスラッシュで打ち消した 1 文字（`don\'t` の `'` は引用符ではない）
//   - コメント（語の先頭の `#` から行末まで。`echo ok # don't` の `'` は引用符ではない）
//
// これを飛ばさないと、実シェルでは引用符でない `'` を開きとして数え、その後ろの本物の `bash -c '…'` を
// 引用符の中と取り違えてほどかない（実測: 待ちループのガードが、この段を共有に移したときに
// `echo don\'t ; bash -c 'while true; do sleep 1; done'` を deny から素通りへ緩めた）。
//
// コメントを飛ばすのは comments が真のときだけ。UnwrapNestedShell は HeadOnly（deny のガード）でだけ飛ばし、
// AnyPos（秘密のガード。ask なので広く取る）では飛ばさない。飛ばすとコメントの中の `# bash -c 'cat ~/.env'` が
// 確認から素通りへ緩む（実行はされないが、確認を減らす向きの変化は ask のガードでは採らない）。
func skipInert(b []byte, i int, comments bool) (int, bool) {
	switch c := b[i]; {
	case c == '\\':
		if i+1 < len(b) {
			return i + 2, true
		}
		return i + 1, true
	case comments && c == '#' && (i == 0 || isWordBreak(b[i-1])):
		j := bytes.IndexByte(b[i:], '\n')
		if j < 0 {
			return len(b), true
		}
		return i + j, true
	}
	return i, false
}

// isWordBreak は、その直後が語の先頭になる文字（空白と区切り）。
func isWordBreak(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || strings.IndexByte(Separators, c) >= 0
}

// quoteEnd は b[i]（' か "）で開いた引用符を閉じる位置。閉じていなければ -1。
// 二重引用符の中ではバックスラッシュが次の 1 文字を打ち消す（`"a\"b"` は 1 つの引用）。一重引用符の中は打ち消しが無い。
func quoteEnd(b []byte, i int) int {
	q := b[i]
	for j := i + 1; j < len(b); j++ {
		switch {
		case q == '"' && b[j] == '\\':
			j++
		case b[j] == q:
			return j
		}
	}
	return -1
}
