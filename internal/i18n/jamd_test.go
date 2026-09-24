package i18n

import (
	"fmt"
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// .go のラチェット（jalint_test.go）は .go の非テストファイルしか走査しないので、
// **.md に書いた日本語は、いくら増えてもどの検査にも掛からない**。
// 「ラチェットが緑」を「利用者に届く日本語が増えていない」の証拠として使えるように、
// **実行ファイルに埋め込まれて配られる .md** を別立てのラチェットで数える。
//
// jalint にまとめず別ファイルにした理由:
//   - **単位が違う**。.go は「日本語を含む文字列リテラルの件数」、.md は「日本語を含む行数」。
//     同じ表に混ぜると count の意味が 2 種類になる。
//   - **桁が違う**。.md は 1 ファイルで数百行になる。jalint の節ごとの合計に混ぜると、
//     .go の残高（減らしていくべき数）が .md に埋もれて読めなくなる。
//   - jalint の TestAllowListTotals は固定のファイル名 jalint_allow_test.go を読むので、
//     別ファイルなら互いを巻き込まない。
//
// 対象を**列挙しない**のがこの検査の肝。「どの .md が利用者に届くか」は //go:embed が決めるので、
// リポジトリの //go:embed を実際に読んで展開する。列挙にすると、埋め込みのディレクトリに .md を
// 足した人が一覧も直さない限り黙って対象外になり、いま直している欠陥と同じ形に戻る。
//
// この検査が見ないもの:
//   - 埋め込まれない .md（docs/ の日英 2 本立ての文書・private/・ルートの README 類）。
//     docs/ と README は internal/docscheck が日英の構成で見ている。
//   - testdata/・先頭が . のディレクトリ（合成データと、別の作業ツリーの複製）。
//
// 近い検査が internal/docscheck/embedscan_test.go にもある（埋め込みに配るつもりのないものが
// 混ざっていないかを見る）。**//go:embed の解釈を変えるときは両方を直す**。

// jaMDRatchetMark は促しの見出しに必ず付ける固定の目印（jalint と同じ考え方。
// 促しを探す側は文面を知らないので、文面に依らない目印が要る）。
const jaMDRatchetMark = "[i18n md ラチェット]"

// jaMDAllow は許可一覧の 1 行（リポジトリからの相対パス・日本語を含む行数・理由）。
// 単位名を lines にしてあるのは、.go 側の count（文字列リテラルの件数）と混同させないため。
type jaMDAllow struct {
	path   string
	lines  int
	reason jaReason
	note   string
}

func TestJapaneseMarkdownDoesNotIncrease(t *testing.T) {
	allow := map[string]jaMDAllow{}
	for _, a := range jaMDAllowed {
		if _, dup := allow[a.path]; dup {
			t.Errorf("許可一覧に %s が 2 回あります（1 行にまとめてください）", a.path)
		}
		if a.lines <= 0 {
			t.Errorf("許可一覧の %s は lines が %d です（0 になったら行ごと消してください）", a.path, a.lines)
		}
		if a.note == "" {
			t.Errorf("許可一覧の %s に note がありません（なぜその日本語が配布物に入るのかをその場に書いてください）", a.path)
		}
		if _, err := os.Stat(filepath.Join(repoRoot, a.path)); err != nil {
			// 打ち間違いのパスは走査でも黙って 0 件を返す。0 件は下限を下回らないのでラチェットは鳴らない。
			t.Errorf("許可一覧の %s: パスが存在しません。打ち間違いか、ファイルが移動・改名された可能性があります（%v）", a.path, err)
		}
		allow[a.path] = a
	}

	got, provenance := embeddedMarkdownJapanese(t)
	if msg := mdRatchetReport(allow, got); msg != "" {
		// 走査の中身（どの //go:embed から何が来たか）を必ず添える。添えないと、
		// 読む人は「なぜこのファイルが対象なのか」を自分で追い直すことになる。
		t.Error(msg + "\n\n" + provenance)
	}
}

// mdRatchetReport は許可一覧と実測の食い違いを 1 つの文面にして返す（食い違いが無ければ空）。
// 増えたときだけでなく、減ったとき・無くなったときも空にしない（一覧と実測をずらしたまま
// 放置させない。残高が実態より多いと、減ったぶんだけ黙って新しい日本語を足せてしまう）。
func mdRatchetReport(allow map[string]jaMDAllow, got map[string]int) string {
	var over, missing, under []string
	for path, n := range got {
		a, ok := allow[path]
		if !ok {
			missing = append(missing, fmt.Sprintf("  %s: %d 行（許可一覧に無いファイル）", path, n))
			continue
		}
		switch {
		case n > a.lines:
			over = append(over, fmt.Sprintf("  %s: %d 行 → %d 行（+%d）", path, a.lines, n, n-a.lines))
		case n < a.lines:
			under = append(under, fmt.Sprintf("  %s: lines: %d → %d", path, a.lines, n))
		}
	}
	for path := range allow {
		if _, ok := got[path]; !ok {
			under = append(under, fmt.Sprintf("  %s: 日本語の行が無くなりました（埋め込みから外れたか、訳されました。許可一覧から行を消してください）", path))
		}
	}
	if len(over) == 0 && len(missing) == 0 && len(under) == 0 {
		return ""
	}
	sort.Strings(over)
	sort.Strings(missing)
	sort.Strings(under)

	var b strings.Builder
	b.WriteString(jaMDRatchetMark + " 埋め込まれる .md の日本語の行数が、internal/i18n/jamd_allow_test.go の許可一覧と一致しません。\n")
	if len(over) > 0 {
		b.WriteString("\n" + jaMDRatchetMark + " 増えたファイル\n" + strings.Join(over, "\n") + "\n")
	}
	if len(missing) > 0 {
		b.WriteString("\n" + jaMDRatchetMark + " 許可一覧に無いファイル\n" + strings.Join(missing, "\n") + "\n")
	}
	if len(under) > 0 {
		b.WriteString("\n" + jaMDRatchetMark + " 減ったファイル（許可一覧を実測に合わせてください）\n" + strings.Join(under, "\n") + "\n")
	}
	if len(over) > 0 || len(missing) > 0 {
		b.WriteString(`
増えたぶんは、どれかにしてください。

 1. 対訳のある文書なら、対になる英語の側も足す（日英 2 本立てを崩さない）。
 2. 配るつもりが無いものなら、//go:embed のパターンを配るものだけに絞る。
 3. 配るものとして残すなら、internal/i18n/jamd_allow_test.go の jaMDAllowed を実測に合わせる:
    {path: "` + "…" + `", lines: N, reason: jaAIOnly, note: "なぜこの日本語が配布物に入るのか"},`)
	}
	if len(under) > 0 {
		b.WriteString(`
減ったぶんは、許可一覧の lines を上の実測値に直す（0 になった行は消す）。
減少を通すと残高が実態より多いまま残り、同じファイルで減った数だけ新しい日本語を足せてしまいます。`)
	}
	return b.String()
}

// TestMDRatchetReport は、増加だけでなく減少・消失・一覧漏れでも文面が返ることを確かめる。
func TestMDRatchetReport(t *testing.T) {
	allow := map[string]jaMDAllow{
		"a.md": {path: "a.md", lines: 10, reason: jaAIOnly, note: "検査用"},
		"b.md": {path: "b.md", lines: 3, reason: jaAIOnly, note: "検査用"},
	}
	for _, tc := range []struct {
		name string
		got  map[string]int
		want string // 文面に必ず含まれる語（空なら「食い違い無し」）
	}{
		{"一致", map[string]int{"a.md": 10, "b.md": 3}, ""},
		{"増えた", map[string]int{"a.md": 11, "b.md": 3}, jaMDRatchetMark + " 増えたファイル"},
		{"減った", map[string]int{"a.md": 4, "b.md": 3}, "lines: 10 → 4"},
		{"無くなった", map[string]int{"a.md": 10}, "無くなりました"},
		{"一覧に無い", map[string]int{"a.md": 10, "b.md": 3, "c.md": 1}, jaMDRatchetMark + " 許可一覧に無いファイル"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := mdRatchetReport(allow, tc.got)
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

// TestMDRatchetMarkOnEveryHeading は、促しの見出しの行すべてに固定の目印が付くことを確かめる。
func TestMDRatchetMarkOnEveryHeading(t *testing.T) {
	allow := map[string]jaMDAllow{
		"a.md": {path: "a.md", lines: 10, reason: jaAIOnly, note: "検査用"},
		"b.md": {path: "b.md", lines: 3, reason: jaAIOnly, note: "検査用"},
	}
	// 増えた・消えた・一覧に無いを一度に起こす
	msg := mdRatchetReport(allow, map[string]int{"a.md": 11, "c.md": 1})
	if msg == "" {
		t.Fatal("食い違いがあるのに文面が返りませんでした")
	}
	marks := 0
	for _, line := range strings.Split(msg, "\n") {
		if strings.HasPrefix(line, jaMDRatchetMark) {
			marks++
		}
	}
	if marks < 4 { // 先頭の 1 行 + 増えた・許可一覧に無い・減った の 3 見出し
		t.Errorf("促しの見出しに目印 %s が %d 行しかありません（grep で絞ると消えます）:\n%s", jaMDRatchetMark, marks, msg)
	}
}

// mdEmbedDirective は 1 つの //go:embed と、そこから展開された .md。
type mdEmbedDirective struct {
	where string   // "kit/embed.go:24"
	text  string   // ディレクティブに書かれたパターンの並び
	files []string // そこから埋め込まれる .md（リポジトリからの相対パス・整列済み）
}

// embeddedMarkdownJapanese は、埋め込まれる .md のうち日本語を含むものの行数を数えて返す
// （リポジトリからの相対パス → 日本語を含む行数。0 行のファイルは入れない）。
// 2 つ目の戻り値は、走査したディレクティブと展開結果を人が読める形にしたもの
// （テストが落ちたときに、なぜそのファイルが対象なのかをその場で読めるようにする）。
func embeddedMarkdownJapanese(t *testing.T) (map[string]int, string) {
	t.Helper()
	directives := scanEmbedDirectivesForMarkdown(t)

	var b strings.Builder
	b.WriteString(jaMDRatchetMark + " 走査した //go:embed と、そこから埋め込まれる .md\n")
	counts := map[string]int{}
	seen := map[string]bool{}
	mdTotal := 0
	for _, d := range directives {
		fmt.Fprintf(&b, "  %s  //go:embed %s → .md %d 件\n", d.where, d.text, len(d.files))
		for _, rel := range d.files {
			mdTotal++
			n := countJapaneseLines(t, rel)
			mark := ""
			if !seen[rel] {
				seen[rel] = true
				if n > 0 {
					counts[rel] = n
				}
			} else {
				mark = "（別のディレクティブでも拾われています）"
			}
			fmt.Fprintf(&b, "    %s: 日本語 %d 行%s\n", rel, n, mark)
		}
	}
	fmt.Fprintf(&b, "  ディレクティブ %d 件 → .md %d 件（重複を除くと %d 件・うち日本語を含むもの %d 件）\n",
		len(directives), mdTotal, len(seen), len(counts))
	return counts, b.String()
}

// scanEmbedDirectivesForMarkdown は、リポジトリの .go にある //go:embed を全部読み、
// そこから埋め込まれる .md を書かれた順に返す（.md を 1 つも含まないディレクティブは返さない）。
//
// Go の規則に合わせる:
//   - パターンは、そのディレクティブを書いた .go のあるディレクトリからの相対
//   - ディレクトリを指したときは中身を全部（「.」「_」で始まる名前を除く）
//   - all: を付けたときは「.」「_」で始まるものも含む
func scanEmbedDirectivesForMarkdown(t *testing.T) []mdEmbedDirective {
	t.Helper()
	root := mustAbs(t, repoRoot)
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("走査の起点 %s: パスが存在しません（%v）", repoRoot, err)
	}
	var out []mdEmbedDirective
	total := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && scanSkipName(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		dir := filepath.Dir(p)
		relGo := filepath.ToSlash(mustRel(t, p))
		for _, dir2 := range embedDirectiveLines(p, string(b)) {
			total++
			var md []string
			for _, pat := range strings.Fields(dir2.text) {
				pat = strings.Trim(pat, "\"`")
				all := false
				if s, ok := strings.CutPrefix(pat, "all:"); ok {
					pat, all = s, true
				}
				matches, err := filepath.Glob(filepath.Join(dir, filepath.FromSlash(pat)))
				if err != nil {
					t.Errorf("%s: パターン %q が壊れています（%v）", relGo, pat, err)
					continue
				}
				if len(matches) == 0 {
					// 当たらないパターンはビルドが通らないはず。走査の側の取りこぼしを疑う。
					t.Errorf("%s: パターン %q が 1 つも当たりません（ビルドが通らないはずです）", relGo, pat)
					continue
				}
				for _, m := range matches {
					md = append(md, markdownUnder(t, root, m, all)...)
				}
			}
			if len(md) == 0 {
				continue
			}
			sort.Strings(md)
			out = append(out, mdEmbedDirective{
				where: fmt.Sprintf("%s:%d", relGo, dir2.line),
				text:  dir2.text,
				files: md,
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if total == 0 {
		t.Fatal("//go:embed のディレクティブが 1 つも見つかりません（走査が壊れています）")
	}
	return out
}

// markdownUnder は m（ファイルかディレクトリ）から埋め込まれる .md を集める。
func markdownUnder(t *testing.T, root, m string, all bool) []string {
	t.Helper()
	rel := func(p string) string {
		r, err := filepath.Rel(root, p)
		if err != nil {
			t.Errorf("%s: %v", p, err)
			return ""
		}
		return filepath.ToSlash(r)
	}
	fi, err := os.Stat(m)
	if err != nil {
		t.Errorf("%s: %v", m, err)
		return nil
	}
	var out []string
	if !fi.IsDir() {
		if strings.HasSuffix(m, ".md") {
			if r := rel(m); r != "" {
				out = append(out, r)
			}
		}
		return out
	}
	err = filepath.WalkDir(m, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if n := d.Name(); p != m && !all && (strings.HasPrefix(n, ".") || strings.HasPrefix(n, "_")) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() && strings.HasSuffix(p, ".md") {
			if r := rel(p); r != "" {
				out = append(out, r)
			}
		}
		return nil
	})
	if err != nil {
		t.Errorf("%s: %v", m, err)
	}
	return out
}

// embedLine は 1 つの //go:embed（行番号と、先頭の "//go:embed " を除いた残り）。
type embedLine struct {
	line int
	text string
}

// embedDirectiveLines は Go のソース src から、directive として実際に効く //go:embed を書かれた順に返す。
//
// どこまで拾うかは、コンパイラ（cmd/compile/internal/syntax の scanner と noder）の規則に合わせた:
//   - 行コメントは、**字下げされていても効く**（grouped var 宣言の中に字下げして書いたものは実際に埋め込む）。
//     だから行頭に限ってはいけない。
//   - ブロックコメント（/* … */）の中は効かない。
//   - 同じ行にコードが先にあると効かない（directive は行に単独で置かれていること）。
//   - 文字列リテラル（生文字列を含む）の中は、そもそもコメントではないので効かない。
//
// 自前で行を切ると後ろの 3 つを取りこぼすので、go/scanner にコメントだけを拾わせる
// （go/build の readGoInfo が //go:embed を集めるのと同じやり方）。
func embedDirectiveLines(name, src string) []embedLine {
	fset := token.NewFileSet()
	f := fset.AddFile(name, -1, len(src))
	var sc scanner.Scanner
	sc.Init(f, []byte(src), nil, scanner.ScanComments)
	var out []embedLine
	for {
		pos, tok, lit := sc.Scan()
		if tok == token.EOF {
			break
		}
		if tok != token.COMMENT {
			continue
		}
		rest, ok := strings.CutPrefix(strings.TrimRight(lit, "\r"), "//go:embed ")
		if !ok {
			continue
		}
		// 行に単独で置かれているか（前にコードがあると directive にならない）。
		p := fset.Position(pos)
		if strings.TrimSpace(src[strings.LastIndexByte(src[:p.Offset], '\n')+1:p.Offset]) != "" {
			continue
		}
		out = append(out, embedLine{line: p.Line, text: rest})
	}
	return out
}

// countJapaneseLines は、リポジトリからの相対パス rel のファイルで、日本語（ひらがな・カタカナ・漢字）を
// 含む行の数を返す。.md は文と印の混ざったプレーンテキストなので、.go のように構文で切らず行で数える。
func countJapaneseLines(t *testing.T, rel string) int {
	t.Helper()
	return countJapaneseLinesIn(t, filepath.Join(repoRoot, filepath.FromSlash(rel)))
}

// countJapaneseLinesIn は path のファイルで日本語を含む行の数を返す。
func countJapaneseLinesIn(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, line := range strings.Split(string(b), "\n") {
		if containsJapanese(line) {
			n++
		}
	}
	return n
}

// TestEmbeddedMarkdownScanIsNotEmpty は、走査が空振りしていないことと、分かっている入り口・
// 対象外を実際に判定できていることを確かめる。走査が 0 件に落ちると、ラチェットは何も見ずに緑になる。
func TestEmbeddedMarkdownScanIsNotEmpty(t *testing.T) {
	got, provenance := embeddedMarkdownJapanese(t)
	if len(got) < 10 {
		t.Fatalf("埋め込まれる .md のうち日本語を含むものが %d 件しかありません（走査が壊れている可能性）:\n%s", len(got), provenance)
	}
	// 入っていること（それぞれ別のディレクティブから来る。パターンの解決が効いている証拠）。
	for _, want := range []string{
		"internal/guide/common.md",             // //go:embed common.md（ファイル名を直に指す）
		"kit/loop/rules/working-discipline.md", // //go:embed *（ディレクトリを下まで歩く）
	} {
		if _, ok := got[want]; !ok {
			t.Errorf("%s が走査に入っていません（パターンの解決が壊れています）:\n%s", want, provenance)
		}
	}
	// 入っていないこと（埋め込まれない .md まで数えると、日英 2 本立ての文書を見ている
	// internal/docscheck と二重管理になる）。
	for _, ng := range []string{
		"docs/AI-GUIDE.md",
		"README.ja.md",
		"internal/client/report/pdf/fonts/README.md", // 埋め込みを .ttf と .txt に絞ってある
	} {
		if _, err := os.Stat(filepath.Join(repoRoot, ng)); err != nil {
			t.Errorf("%s: パスが存在しません。移動・改名した可能性があります（%v）", ng, err)
			continue
		}
		if _, ok := got[ng]; ok {
			t.Errorf("%s が走査に入っています（埋め込まれない .md まで数えています）:\n%s", ng, provenance)
		}
	}
}

// TestEmbedDirectiveLinesPosition は、//go:embed の拾い方が Go の規則どおりであることを確かめる。
// 拾い漏らすと配るものが検査の外に落ち、拾いすぎると実在しない対象を咎めて信用されなくなる。
func TestEmbedDirectiveLinesPosition(t *testing.T) {
	const src = "package p\n" + // 1
		"\n" + // 2
		"import _ \"embed\"\n" + // 3
		"\n" + // 4
		"var (\n" + // 5
		"\t//go:embed indented.md\n" + // 6: 字下げされていても効く
		"\tA string\n" + // 7
		")\n" + // 8
		"\n" + // 9
		"/*\n" + // 10
		"//go:embed inblock.md\n" + // 11: ブロックコメントの中は効かない
		"*/\n" + // 12
		"\n" + // 13
		"var B = \"x\" //go:embed trailing.md\n" + // 14: 行にコードが先にあると効かない
		"\n" + // 15
		"var C = `//go:embed inrawstring.md`\n" // 16: 文字列の中は効かない
	got := embedDirectiveLines("probe.go", src)
	want := []embedLine{{line: 6, text: "indented.md"}}
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("走査した directive が %+v でした（期待 %+v）", got, want)
	}
}

// TestCountJapaneseLines は、行数の数え方（1 行に何文字あっても 1 行・日本語の無い行は数えない）を固定する。
func TestCountJapaneseLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "probe.md")
	body := "# Title\n" + // 日本語なし
		"これは日本語の行です。\n" +
		"ASCII only line\n" +
		"漢字とカタカナとひらがな\n" +
		"| a | b |\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if n := countJapaneseLinesIn(t, path); n != 2 {
		t.Errorf("日本語を含む行を %d 行と数えました（期待 2 行）", n)
	}
}

// 節と末尾の合計の行は、書き換えを忘れると黙って実態とずれる（許可一覧の行数そのものと同じ問題）。
// jamd_allow_test.go を読んで、コメントの合計が jaMDAllowed と一致することを確かめる。
var (
	mdSectionTotalRe = regexp.MustCompile(`// ── (.+?) ── (\d+) ファイル・合計 (\d+) 行`)
	mdGrandTotalRe   = regexp.MustCompile(`// 合計 (\d+) ファイル・(\d+) 行`)
	mdAllowRowRe     = regexp.MustCompile(`\{path: "([^"]+)",\s*lines:\s*(\d+),\s*reason:\s*(\w+)`)
)

func TestMDAllowListTotals(t *testing.T) {
	const src = "jamd_allow_test.go"
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}

	type section struct {
		name         string
		files, lines int
	}
	var sections []*section
	var cur *section
	for _, line := range strings.Split(string(b), "\n") {
		if m := mdSectionTotalRe.FindStringSubmatch(line); m != nil {
			cur = &section{name: m[1]}
			sections = append(sections, cur)
			wantF, _ := strconv.Atoi(m[2])
			wantL, _ := strconv.Atoi(m[3])
			cur.files, cur.lines = -wantF, -wantL // いったん期待値を負で持ち、下で実測を足す
			continue
		}
		if m := mdAllowRowRe.FindStringSubmatch(line); m != nil && cur != nil {
			n, _ := strconv.Atoi(m[2])
			cur.files++
			cur.lines += n
		}
	}
	if len(sections) == 0 {
		t.Fatalf("%s に節の合計の行（// ── … ── N ファイル・合計 M 行）がありません", src)
	}
	for _, s := range sections {
		if s.files != 0 || s.lines != 0 {
			t.Errorf("%s の節「%s」: 合計の行が実測とずれています（ファイル数の差 %+d・行数の差 %+d。行を実測に直してください）",
				src, s.name, s.files, s.lines)
		}
	}

	rows := mdAllowRowRe.FindAllStringSubmatch(string(b), -1)
	files, lines := len(rows), 0
	for _, m := range rows {
		n, _ := strconv.Atoi(m[2])
		lines += n
	}
	if files != len(jaMDAllowed) {
		// 正規表現が拾えない書き方（改行して lines を次の行に書く等）をされると、合計の検査が
		// 黙って一部の行を見なくなる。宣言そのものの数と突き合わせて、その取りこぼしを捕まえる。
		t.Errorf("%s から読めた行が %d 件で、jaMDAllowed の %d 件と合いません（1 行に {path: …, lines: …, reason: …} を書いてください）",
			src, files, len(jaMDAllowed))
	}
	m := mdGrandTotalRe.FindStringSubmatch(string(b))
	if m == nil {
		t.Fatalf("%s に末尾の合計の行（// 合計 N ファイル・M 行）がありません", src)
	}
	wantFiles, _ := strconv.Atoi(m[1])
	wantLines, _ := strconv.Atoi(m[2])
	if wantFiles != files || wantLines != lines {
		t.Errorf("%s の末尾の合計の行が実測とずれています: 記載 %d ファイル・%d 行 → 実測 %d ファイル・%d 行",
			src, wantFiles, wantLines, files, lines)
	}
}
