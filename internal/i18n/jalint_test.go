package i18n

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// 受け入れ条件「訳の抜けを検出するテストがある」の 1 つ目。
//
// lint_test.go は「対訳表と i18n.T の呼び出しの過不足」しか見ない。つまり i18n.T に移していない
// 日本語のリテラルが残っていても緑のままになる（実際に約 300 件が初期の一覧から漏れた）。
// ここでは **.go の中の日本語を含む文字列リテラルの件数**をファイルごとに数え、許可一覧（jalint_allow_test.go）と
// 突き合わせる。
//
// ラチェット式にしてある。**許可一覧と実測がずれていたら、増減のどちらでも失敗させる**:
//   - 許可一覧より増えたら失敗（新しい未訳を入れさせない）
//   - 許可一覧より減っても失敗（一覧の更新を強制する）
//   - 一覧に無いファイルに日本語のリテラルがあれば失敗
//   - 一覧にあるのに日本語のリテラルが無くなっていれば失敗（行ごと消す）
//
// 減少を t.Log と stderr の通知だけにしていたときは、非 verbose（CI・既定）で誰も見ず、残高が実態より
// 多いまま残った。そうなると、同じファイルで減った数だけ新しい未訳を足しても一覧の上限内として素通りする
// （実例: 一覧が 1,182 件のまま実測 1,063 件になり、119 件ぶんの余裕が黙って空いた）。
//
// 促しを「見えるようにする」対策には限界がある。促しを探す側は**促しの文面を知らない**ので、
// 文面を目立たせても grep の語に当たらなければ消える（実例: `-v` の出力を
// `grep -iE "ファイル|件|PASS|FAIL"` で絞った結果、「…無くなりました」の行がどの語にも当たらず、
// 「促しは出ていない＝一致している」と誤って報告された）。だから決め手は**文面に依らない合図**にすること:
//   - 失敗させる（t.Error）。go test が `--- FAIL` と終了コード 1 を返すので、出力をどう絞っても、
//     また非 verbose でも、緑と赤を取り違えない。
//   - 併せて、促しの見出しの行には固定の目印 jaRatchetMark（"[i18n ラチェット]"）を必ず置く。
//     文面を知らなくても `[i18n` で絞れば拾える。

// jaRatchetMark は促しの見出しに必ず付ける固定の目印（文面を知らなくても grep で拾えるように）。
const jaRatchetMark = "[i18n ラチェット]"

//
// いまの状態で落ちる作りにはしない。約 1,400 件が残っている間ずっと赤だと、赤が常態化して誰も見なくなる。
//
// 数える対象は .go・.html・.js（配布する実行ファイルに埋め込まれるもの）。
//   - .go は go/ast で文字列リテラルだけを取る（1 リテラル = 1 件）。
//   - .html（テンプレート）と .js（画面のスクリプト）は素の走査で、日本語を含む行を 1 件として数える
//     （コメントは .go と同じく数えない。japaneseLines を見ること）。
//
// この検査が最初から見ないもの:
//   - Go のコメント（そもそも訳の対象ではない）。go/ast で文字列リテラルだけを取るので、
//     `"foo" // 日本語のコメント` のような行を拾わない。
//   - _test.go・_test.mjs（テストの中の日本語は配布物の文面ではない）
//   - testdata/（記録・合成データ。Go として読めないものが混ざる）
//   - 先頭が . のディレクトリ（.git・.claude/worktrees には別の作業ツリーの複製が入る）
//
// 走査の起点はリポジトリ全体にしてある（lint_test.go の scanDirs のような「見る場所の一覧」を持たない）。
// 一覧に挙げ忘れたパッケージが黙って検査の外に落ちるのを防ぐため。実際 scanDirs は
// deploy/embed.go・kit/embed.go・migrations/embed.go・notice.go を見ていない。

// jaReason は、その日本語が残っている理由の種別。許可一覧にその場で書く（別紙にしない）。
type jaReason string

const (
	// jaTodo は未訳。i18n.T へ移す対象で、これが減っていくのが正しい方向。
	jaTodo jaReason = "未訳（i18n.T へ移す対象）"
	// jaJudge は判定に使う語。訳すと分岐・正規表現が壊れる。
	jaJudge jaReason = "判定に使う語（訳すと壊れる）"
	// jaAIOnly は AI しか読まない文（kit の rules・skill の本文を埋め込んだもの）。
	jaAIOnly jaReason = "AI しか読まない文"
	// jaDevTool は生成ツール・開発用。配布物に入らないか、i18n を使えない場所。
	jaDevTool jaReason = "生成ツール・開発用（配布物に入らない / 訳せない）"
	// jaRecord は DB や書き出すファイルに残る記録。動かした人の言語で記録が変わらないようにする。
	jaRecord jaReason = "DB や書き出すファイルに残る記録"
)

// jaAllow は許可一覧の 1 行（リポジトリからの相対パス・現在の件数・理由）。
type jaAllow struct {
	path   string
	count  int
	reason jaReason
	note   string // その理由をこのファイルに当てる根拠（jaTodo は空）
}

func TestJapaneseLiteralsDoNotIncrease(t *testing.T) {
	allow := map[string]jaAllow{}
	// 公開物（git archive）には private/ が無い（export-ignore）。そこで回したときは private/ の行を使わない
	// （使うと「パスが存在しません」と「減ったファイル」で必ず落ちる）。開発側では private/ があるので従来どおり見る。
	_, privErr := os.Stat(filepath.Join(repoRoot, "private"))
	publicTree := errors.Is(privErr, fs.ErrNotExist)
	for _, a := range jaAllowed {
		if publicTree && strings.HasPrefix(a.path, "private/") {
			continue
		}
		if _, dup := allow[a.path]; dup {
			t.Errorf("許可一覧に %s が 2 回あります（1 行にまとめてください）", a.path)
		}
		if a.count <= 0 {
			t.Errorf("許可一覧の %s は count が %d です（0 になったら行ごと消してください）", a.path, a.count)
		}
		if a.reason != jaTodo && a.note == "" {
			t.Errorf("許可一覧の %s は reason が %q なのに note がありません（なぜ訳さないのかをその場に書いてください）", a.path, a.reason)
		}
		if _, err := os.Stat(filepath.Join(repoRoot, a.path)); err != nil {
			// 打ち間違いのパスは grep でも走査でも黙って 0 件を返す。0 件は下限を下回らないので
			// ラチェットは鳴らない。実在を必ず確かめる。
			t.Errorf("許可一覧の %s: パスが存在しません。打ち間違いか、パッケージが移動・改名された可能性があります（%v）", a.path, err)
		}
		allow[a.path] = a
	}

	if msg := ratchetReport(allow, countJapaneseLiterals(t)); msg != "" {
		t.Error(msg)
	}
}

// ratchetReport は許可一覧と実測の食い違いを 1 つの文面にして返す（食い違いが無ければ空）。
// 増えたときだけでなく、減ったとき・無くなったときも空にしない。
func ratchetReport(allow map[string]jaAllow, got map[string]int) string {
	var over, missing, under []string
	for path, n := range got {
		a, ok := allow[path]
		if !ok {
			missing = append(missing, fmt.Sprintf("  %s: %d 件（許可一覧に無いファイル）", path, n))
			continue
		}
		switch {
		case n > a.count:
			over = append(over, fmt.Sprintf("  %s: %d 件 → %d 件（+%d）", path, a.count, n, n-a.count))
		case n < a.count:
			under = append(under, fmt.Sprintf("  %s: count: %d → %d", path, a.count, n))
		}
	}
	for path := range allow {
		if _, ok := got[path]; !ok {
			under = append(under, fmt.Sprintf("  %s: 日本語のリテラルが無くなりました（許可一覧から行を消してください）", path))
		}
	}
	if len(over) == 0 && len(missing) == 0 && len(under) == 0 {
		return ""
	}
	sort.Strings(over)
	sort.Strings(missing)
	sort.Strings(under)

	var b strings.Builder
	b.WriteString(jaRatchetMark + " 日本語の文字列リテラルの件数が、internal/i18n/jalint_allow_test.go の許可一覧と一致しません。\n")
	if len(over) > 0 {
		b.WriteString("\n" + jaRatchetMark + " 増えたファイル\n" + strings.Join(over, "\n") + "\n")
	}
	if len(missing) > 0 {
		b.WriteString("\n" + jaRatchetMark + " 許可一覧に無いファイル\n" + strings.Join(missing, "\n") + "\n")
	}
	if len(under) > 0 {
		b.WriteString("\n" + jaRatchetMark + " 減ったファイル（許可一覧を実測に合わせてください）\n" + strings.Join(under, "\n") + "\n")
	}
	if len(over) > 0 || len(missing) > 0 {
		b.WriteString(`
増えたぶんは、どちらかにしてください。

 1. 利用者が読む文面なら訳す:
    ja.json と en.json に ID を足し、リテラルを i18n.T(lang, "id", …)（または i18n.M / i18n.Errorf /
    i18n.Wrapf）に置き換える。件数が減るので、許可一覧の count も減らすか、行ごと消す。

 2. 訳さないものなら、internal/i18n/jalint_allow_test.go の jaAllowed に理由つきで足す:
    {path: "` + "…" + `", count: N, reason: jaJudge, note: "なぜ訳さないか"},
    理由は jaJudge（判定に使う語）/ jaAIOnly（AI しか読まない文）/ jaDevTool（生成ツール・開発用）/
    jaRecord（DB や書き出すファイルに残る記録）から選ぶ。jaTodo（未訳）は既存の残高だけに使う。`)
	}
	if len(under) > 0 {
		b.WriteString(`
減ったぶんは、許可一覧の count を上の実測値に直す（0 になった行は消す）。
減少を通すと残高が実態より多いまま残り、同じファイルで減った数だけ新しい未訳を足せてしまいます。`)
	}
	return b.String()
}

// TestRatchetReport は、増加だけでなく減少・消失でも文面が返ることを確かめる（再発防止）。
func TestRatchetReport(t *testing.T) {
	allow := map[string]jaAllow{
		"a.go": {path: "a.go", count: 10, reason: jaTodo},
		"b.go": {path: "b.go", count: 3, reason: jaTodo},
	}
	for _, tc := range []struct {
		name string
		got  map[string]int
		want string // 文面に必ず含まれる語（空なら「食い違い無し」）
	}{
		{"一致", map[string]int{"a.go": 10, "b.go": 3}, ""},
		{"増えた", map[string]int{"a.go": 11, "b.go": 3}, jaRatchetMark + " 増えたファイル"},
		{"減った", map[string]int{"a.go": 4, "b.go": 3}, "count: 10 → 4"},
		{"無くなった", map[string]int{"a.go": 10}, "無くなりました"},
		{"一覧に無い", map[string]int{"a.go": 10, "b.go": 3, "c.go": 1}, jaRatchetMark + " 許可一覧に無いファイル"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := ratchetReport(allow, tc.got)
			if tc.want == "" {
				if msg != "" {
					t.Errorf("食い違いが無いのに文面が返りました:\n%s", msg)
				}
				return
			}
			if msg == "" {
				t.Fatalf("%s: 文面が返りませんでした（検査が素通りします）", tc.name)
			}
			if !strings.Contains(msg, tc.want) {
				t.Errorf("%s: 文面に %q がありません:\n%s", tc.name, tc.want, msg)
			}
		})
	}
}

// countJapaneseLiterals は、日本語を含む文字列リテラルの数をファイルごとに数える
// （リポジトリからの相対パス → 件数。0 件のファイルは入れない）。
func countJapaneseLiterals(t *testing.T) map[string]int {
	t.Helper()
	counts := map[string]int{}
	root := mustAbs(t, repoRoot)
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("走査の起点 %s: パスが存在しません。打ち間違いか、パッケージが移動・改名された可能性があります（%v）", repoRoot, err)
	}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			// .git・.claude/worktrees（別の作業ツリーの複製）・記録・合成データは見ない。
			if path != root && (strings.HasPrefix(name, ".") || name == "testdata" || name == "vendor" || name == "node_modules") {
				return fs.SkipDir
			}
			return nil
		}
		rel0 := filepath.ToSlash(mustRel(t, path))
		// テンプレートと画面のスクリプトは Go ではないので、素の走査で日本語の行を数える。
		if isHTML, isJS := strings.HasSuffix(path, ".html"), strings.HasSuffix(path, ".js"); isHTML || isJS {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if n := japaneseLines(string(b), isHTML); n > 0 {
				counts[rel0] = n
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// コメントは読み飛ばす（parser.ParseFile の既定）。go/ast で文字列リテラルだけを取るので、
		// `"foo" // 日本語のコメント` のような行を拾わない。
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		rel := rel0
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, err := strconv.Unquote(lit.Value)
			if err != nil {
				// 生文字列（`…`）の Unquote は失敗しないが、念のため素の綴りで見る。
				v = lit.Value
			}
			if containsJapanese(v) {
				counts[rel]++
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return counts
}

// japaneseLines は、コメントを除いた部分に日本語を含む行の数を返す（.html・.js 用）。
//
// Go のように構文解析はしない。コメント（{{/* … */}}・<!-- … -->・/* … */・//）だけを落とし、
// 残りに日本語があれば 1 行を 1 件として数える。文字列とコードは区別しない（どちらも訳の対象）。
// 引用符の状態は行をまたがない（この 2 種類のファイルに複数行の文字列が無いことを前提にする。
// 複数行の文字列を入れると、その中のコメントらしき綴りで数え方が変わる）。
func japaneseLines(src string, html bool) int {
	n := 0
	inBlock, inHTMLComment := false, false
	for _, line := range strings.Split(src, "\n") {
		var code strings.Builder
		var quote rune
		rs := []rune(line)
		at := func(i int) rune {
			if i < len(rs) {
				return rs[i]
			}
			return 0
		}
		for i := 0; i < len(rs); i++ {
			c := rs[i]
			switch {
			case inBlock:
				if c == '*' && at(i+1) == '/' {
					inBlock = false
					i++
				}
			case inHTMLComment:
				if c == '-' && at(i+1) == '-' && at(i+2) == '>' {
					inHTMLComment = false
					i += 2
				}
			case quote != 0:
				if c == '\\' {
					i++
					continue
				}
				if c == quote {
					quote = 0
				}
				code.WriteRune(c)
			case c == '<' && at(i+1) == '!' && at(i+2) == '-' && at(i+3) == '-':
				inHTMLComment = true
				i += 3
			case !html && c == '/' && at(i+1) == '/':
				i = len(rs) // 行末まで読み飛ばす
			case c == '/' && at(i+1) == '*':
				inBlock = true
				i++
			case c == '"' || c == '\'' || c == '`':
				quote = c
				code.WriteRune(c)
			default:
				code.WriteRune(c)
			}
		}
		if containsJapanese(code.String()) {
			n++
		}
	}
	return n
}

// containsJapanese は、ひらがな・カタカナ・漢字のいずれかを含むかを返す。
// 全角の記号（「」・…）だけの文字列は訳の対象ではないので数えない。
func containsJapanese(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func mustAbs(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// 節と末尾の合計の行は、書き換えを忘れると黙って実態とずれる（許可一覧の件数そのものと同じ問題）。
// jalint_allow_test.go を読んで、コメントの合計が jaAllowed と一致することを確かめる。
var (
	sectionTotalRe = regexp.MustCompile(`// ── (.+?) ── (\d+) ファイル・合計 (\d+) 件`)
	grandTotalRe   = regexp.MustCompile(`// 合計 (\d+) ファイル・(\d+) 件（うち未訳 (\d+) 件）`)
	allowRowRe     = regexp.MustCompile(`\{path: "([^"]+)",\s*count:\s*(\d+),\s*reason:\s*(\w+)`)
)

func TestAllowListTotals(t *testing.T) {
	const src = "jalint_allow_test.go"
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}

	type section struct {
		name         string
		files, count int
	}
	var sections []*section
	var cur *section
	for _, line := range strings.Split(string(b), "\n") {
		if m := sectionTotalRe.FindStringSubmatch(line); m != nil {
			cur = &section{name: m[1]}
			sections = append(sections, cur)
			want, _ := strconv.Atoi(m[2])
			wantN, _ := strconv.Atoi(m[3])
			cur.files, cur.count = -want, -wantN // いったん期待値を負で持ち、下で実測を足す
			continue
		}
		if m := allowRowRe.FindStringSubmatch(line); m != nil && cur != nil {
			n, _ := strconv.Atoi(m[2])
			cur.files++
			cur.count += n
		}
	}
	if len(sections) == 0 {
		t.Fatalf("%s に節の合計の行（// ── … ── N ファイル・合計 M 件）がありません", src)
	}
	for _, s := range sections {
		if s.files != 0 || s.count != 0 {
			t.Errorf("%s の節「%s」: 合計の行が実測とずれています（ファイル数の差 %+d・件数の差 %+d。行を実測に直してください）",
				src, s.name, s.files, s.count)
		}
	}

	rows := allowRowRe.FindAllStringSubmatch(string(b), -1)
	files, count, todo := len(rows), 0, 0
	for _, m := range rows {
		n, _ := strconv.Atoi(m[2])
		count += n
		if m[3] == "jaTodo" {
			todo += n
		}
	}
	m := grandTotalRe.FindStringSubmatch(string(b))
	if m == nil {
		t.Fatalf("%s に末尾の合計の行（// 合計 N ファイル・M 件（うち未訳 K 件））がありません", src)
	}
	wantFiles, _ := strconv.Atoi(m[1])
	wantCount, _ := strconv.Atoi(m[2])
	wantTodo, _ := strconv.Atoi(m[3])
	if wantFiles != files || wantCount != count || wantTodo != todo {
		t.Errorf("%s の末尾の合計の行が実測とずれています: 記載 %d ファイル・%d 件（うち未訳 %d 件）→ 実測 %d ファイル・%d 件（うち未訳 %d 件）",
			src, wantFiles, wantCount, wantTodo, files, count, todo)
	}
}

// TestRatchetMarkOnEveryHeading は、促しの見出しの行すべてに固定の目印が付くことを確かめる。
// 促しを探す側は促しの文面を知らないので、文面に依らない目印が無いと grep の絞り込みで消える。
func TestRatchetMarkOnEveryHeading(t *testing.T) {
	allow := map[string]jaAllow{
		"a.go": {path: "a.go", count: 10, reason: jaTodo},
		"b.go": {path: "b.go", count: 3, reason: jaTodo},
	}
	// 増えた・減った・消えた・一覧に無いを一度に起こす
	msg := ratchetReport(allow, map[string]int{"a.go": 11, "c.go": 1})
	if msg == "" {
		t.Fatal("食い違いがあるのに文面が返りませんでした")
	}
	marks := 0
	for _, line := range strings.Split(msg, "\n") {
		if strings.HasPrefix(line, jaRatchetMark) {
			marks++
		}
	}
	if marks < 4 { // 先頭の 1 行 + 増えた・減った・一覧に無い の 3 見出し
		t.Errorf("促しの見出しに目印 %s が %d 行しかありません（grep で絞ると消えます）:\n%s", jaRatchetMark, marks, msg)
	}
}
