package hookcmd

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// 入れ子のシェルをほどく段は、引用符ごとに「その手前の末尾が前置に当たるか」を、前向きの正規表現を手前の全体に
// 掛けて調べていた（引用符の数 × 長さで 2 次）。いまは逆向きの正規表現で末尾から読む（revmatch.go）。
// ここでは、以前の実装をそのまま写した unwrapNestedShellForward と出力を 1 バイトずつ突き合わせる。
// 速くするための書き換えで出力が変わると、ガードの判定が黙って通す側に動くことがある。

var (
	nestedShellPrefixHeadFwd = regexp.MustCompile(nestedShellPrefixHead)
	nestedShellPrefixAnyFwd  = regexp.MustCompile(nestedShellPrefixAny)
)

// unwrapNestedShellForward は書き換える前の unwrapNestedShell（前向きの正規表現を b[:i] に掛ける）。
func unwrapNestedShellForward(cmd string, pos UnwrapPos, sep byte) string {
	re := nestedShellPrefixHeadFwd
	if pos == AnyPos {
		re = nestedShellPrefixAnyFwd
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
			b[i], b[end] = sep, sep
		}
		i = end
	}
	return string(b)
}

// diffUnwrap は 1 つの入力を、位置 2 通り × 区切り 2 通りで以前の実装と突き合わせ、食い違いの数を返す。
func diffUnwrap(t *testing.T, in string, report *int) int {
	t.Helper()
	n := 0
	for _, pos := range []UnwrapPos{HeadOnly, AnyPos} {
		for _, sep := range []byte{';', '\n'} {
			got, want := unwrapNestedShell(in, pos, sep), unwrapNestedShellForward(in, pos, sep)
			if got != want {
				n++
				if *report < 20 {
					*report++
					t.Errorf("出力が以前の実装と違う（pos=%d sep=%q）\n入力: %q\n以前: %q\nいま: %q", pos, sep, in, want, got)
				}
			}
		}
	}
	return n
}

// collectForms は突き合わせに使うこれまでの形を集める。hookcmd の golden（ガードの字句解析の記録）と、
// hook の各パッケージのテストに書いた文字列リテラル全部（ガードのテストの入力がそのまま入る）。
func collectForms(t *testing.T) []string {
	t.Helper()
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, f := range []string{"testdata/hookcmd_golden.json", "testdata/hookcmd_posix_text.json"} {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var rows []struct {
			In string `json:"in"`
		}
		if err := json.Unmarshal(raw, &rows); err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			add(r.In)
		}
	}
	fset := token.NewFileSet()
	err := filepath.WalkDir("..", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil {
					add(s)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestUnwrapNestedShellMatchesForward は、これまでの形と、それを 2 つずつつないだ形で出力が以前と同じことを確かめる。
func TestUnwrapNestedShellMatchesForward(t *testing.T) {
	forms := collectForms(t)
	// 前提の確認: 形が集まっていて、その中に実際にほどかれる形（出力が入力と違う）がある。
	// 集まらない・ほどかれる形が無いと、突き合わせは何も確かめずに通る。
	if len(forms) < 3000 {
		t.Fatalf("前提が崩れています: 突き合わせる形が %d 件しか集まらない（3000 件以上のはず）", len(forms))
	}
	unwrapped, report, bad := 0, 0, 0
	for _, f := range forms {
		if unwrapNestedShellForward(f, HeadOnly, ';') != f {
			unwrapped++
		}
		bad += diffUnwrap(t, f, &report)
	}
	if unwrapped < 50 {
		t.Fatalf("前提が崩れています: ほどかれる形が %d 件しかない", unwrapped)
	}
	// 2 つずつつなぐ（前の形の引用符・コメント・区切りが後ろの形の判定に効く組み合わせ）
	r := rand.New(rand.NewPCG(1, 2))
	pairs := 0
	for range 20000 {
		a, b := forms[r.IntN(len(forms))], forms[r.IntN(len(forms))]
		for _, glue := range []string{"", " ", "\n", "; "} {
			bad += diffUnwrap(t, a+glue+b, &report)
			pairs++
		}
	}
	t.Logf("形 %d 件（ほどかれる形 %d 件）・つないだ形 %d 件 × 位置 2 × 区切り 2 で食い違い %d 件", len(forms), unwrapped, pairs, bad)
}

// fuzzAlphabet は乱数の入力の部品。ほどく段の正規表現と引用符の数え方に効く語・記号を厚めに入れる。
var fuzzAlphabet = []string{
	"bash", "sh", "zsh", "dash", "BASH", "Sh", "baſh", "bas\u212a", "eval", "EVAL", ".exe", ".EXE", "/usr/bin/", `C:\x\`,
	"-c", "-lc", "-xc", "-o", "pipefail", "-ec", "-cx", "-", "-exec", "-execdir", "find", ".",
	"sudo", "env", "xargs", "nohup", "time", "command", "nice", "stdbuf", "timeout", "setsid", "flock", "script",
	"-u", "deploy", "FOO=1", "x=", "30", "10s", "/tmp/l", "-q", "-n",
	"if", "then", "else", "elif", "do", "while", "until", "!", "{", "}",
	"echo", "git", "reset", "--hard", "cat", "a", "b", "c", "EOF", "<<", "<<<", "$(", "$'", "${", "`",
	" ", " ", " ", "  ", "\t", "\n", "\r\n", "\f",
	";", "&", "&&", "|", "||", "(", ")", "<", ">", "'", "'", "\"", "\"", "\\", "\\'", "\\\"", "#", " #",
	"é", "日本", "\xe2\x82", "\xff", "\xc3", "\xf0\x9f\x98",
}

var (
	fuzzHeads  = []string{"sudo", "env", "env -u FOO", "sudo -u deploy", "xargs -0", "timeout 30", "nice -n 10", "flock /tmp/l", "script -q /tmp/o", "FOO=1", "x=", "if", "then", "!", "{", "do", "while", "/usr/bin/sudo", "SUDO", "time", "ssh host", "echo", "docker run img"}
	fuzzShells = []string{"bash", "sh", "zsh", "dash", "BASH", "Bash.exe", "BASH.EXE", "/bin/bash", `C:\x\bash.exe`, "baſh", "eval", "EVAL", "bas\u212a", "ksh"}
	fuzzOpts   = []string{"-c", "-lc", "-xc", "-ec", "-cx", "-o pipefail -c", "-x -c", "-c", "-l", "--", "-c -x"}
	fuzzSeps   = []string{" ", " ", " ", "  ", "\t", "\n", "", ";", "; ", " && ", " | ", "|", "(", ")", " & ", "\r\n", " -exec ", " -execdir "}
)

// fuzzInput は乱数の入力を 1 つ作る。部品をただ並べるだけだと語が空白でつながらず、ほどかれる形がほとんど出ないので、
// 「前置の並び + シェル + 選択肢 + 引用符」の形を厚めに混ぜる。
func fuzzInput(r *rand.Rand) string {
	pick := func(xs []string) string { return xs[r.IntN(len(xs))] }
	var sb strings.Builder
	for range 1 + r.IntN(8) {
		switch r.IntN(3) {
		case 0:
			for range r.IntN(3) {
				sb.WriteString(pick(fuzzHeads) + pick(fuzzSeps[:6]))
			}
			sb.WriteString(pick(fuzzShells))
			if pick(fuzzShells) != "eval" {
				sb.WriteString(" " + pick(fuzzOpts))
			}
			sb.WriteString(pick(fuzzSeps[:6]))
			q := pick([]string{"'", "\"", "$'", "\\'"})
			sb.WriteString(q)
			for range r.IntN(4) {
				sb.WriteString(pick(fuzzAlphabet))
			}
			if r.IntN(5) > 0 {
				sb.WriteString(q[len(q)-1:])
			}
		default:
			for range r.IntN(5) {
				sb.WriteString(pick(fuzzAlphabet))
			}
		}
		sb.WriteString(pick(fuzzSeps))
	}
	return sb.String()
}

// TestUnwrapNestedShellRandomMatchesForward は乱数の入力（10 万件）で出力が以前と同じことを確かめる。
func TestUnwrapNestedShellRandomMatchesForward(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 91))
	report, bad, unwrapped := 0, 0, 0
	const n = 100000
	for range n {
		in := fuzzInput(r)
		if unwrapNestedShellForward(in, AnyPos, ';') != in {
			unwrapped++
		}
		bad += diffUnwrap(t, in, &report)
	}
	if unwrapped < n/10 {
		t.Fatalf("前提が崩れています: 乱数の入力でほどかれる形が %d 件しかない", unwrapped)
	}
	t.Logf("乱数の入力 %d 件（ほどかれる形 %d 件）× 位置 2 × 区切り 2 で食い違い %d 件", n, unwrapped, bad)
}

// TestEndsWithMatchMatchesForward は、逆向きの式で末尾から読む判定が、前向きの式を接頭辞に掛けた判定と
// すべての位置で同じことを確かめる（unwrapNestedShell を通さず、部品そのものを見る）。
func TestEndsWithMatchMatchesForward(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 3))
	hits, total := 0, 0
	for range 5000 {
		b := []byte(fuzzInput(r))
		for i := 0; i <= len(b); i++ {
			for _, c := range []struct {
				fwd, rev *regexp.Regexp
			}{{nestedShellPrefixHeadFwd, nestedShellPrefixHeadRev}, {nestedShellPrefixAnyFwd, nestedShellPrefixAnyRev}} {
				want := c.fwd.Match(b[:i])
				if got := endsWithMatch(c.rev, b, i); got != want {
					t.Fatalf("末尾の判定が前向きと違う: %q got %v want %v", b[:i], got, want)
				}
				total++
				if want {
					hits++
				}
			}
		}
	}
	if hits < 1000 {
		t.Fatalf("前提が崩れています: 当たる位置が %d 件しかない", hits)
	}
	t.Logf("位置 %d 件（当たる位置 %d 件）で食い違い 0 件", total, hits)
}

// TestBackwardRunesSegmentation は、末尾から読んだ字の切れ目が前から読んだ切れ目と同じことを、壊れた UTF-8 を含む
// 乱数のバイト列で確かめる（違うと、(?i) で当たる ſ・K のような字の扱いが向きで変わる）。
func TestBackwardRunesSegmentation(t *testing.T) {
	r := rand.New(rand.NewPCG(11, 13))
	pieces := []string{"a", "'", " ", "é", "ſ", "\u212a", "日", "😀", "\xe2\x82", "\xff", "\x80", "\xc3", "\xf0\x9f\x98", "\xed\xa0\x80"}
	for range 50000 {
		var sb strings.Builder
		for range r.IntN(12) {
			sb.WriteString(pieces[r.IntN(len(pieces))])
		}
		b := []byte(sb.String())
		var fwd []rune
		for p := 0; p < len(b); {
			c, n := utf8.DecodeRune(b[p:])
			fwd = append(fwd, c)
			p += n
		}
		br := &backwardRunes{b: b, p: len(b)}
		var bwd []rune
		for {
			c, _, err := br.ReadRune()
			if err != nil {
				break
			}
			bwd = append([]rune{c}, bwd...)
		}
		if string(fwd) != string(bwd) || len(fwd) != len(bwd) {
			t.Fatalf("字の切れ目が向きで違う: %q 前から %q 末尾から %q", b, fwd, bwd)
		}
	}
}

// TestUnwrapNestedShellLongInputIsFast は所要時間の後退を捕まえる。2 次に戻ると、引用符が多い数百 KB の入力で
// 何十秒もかかる（書き換える前の実装は 20 KB 前後で数秒かかり、hook の打ち切りの 4 秒を越えていた）。
// いまは 300 KB 前後で数十ミリ秒なので、上限は遅い CI の機械でも揺れない 5 秒に置く。
// 対照として、同じ入力の短い版が実際にほどかれる（中身が判定に上がる）ことも確かめる。
func TestUnwrapNestedShellLongInputIsFast(t *testing.T) {
	units := []string{
		"echo $'a\\'b' # it's\n",
		"bash -c 'echo a; echo b'\n",
		"echo 'it'\"'\"'s here'\n",
		"echo \"v=$(date +%s) it's\"\n",
		"bash -c 'echo a'\n",
		"sudo env FOO=1 bash -lc \"x\" 'y' \"z\"\n",
	}
	for _, u := range units {
		in := strings.Repeat(u, 300000/len(u)) + "bash -c 'git reset --hard'"
		for _, pos := range []UnwrapPos{HeadOnly, AnyPos} {
			for _, sep := range []byte{';', '\n'} {
				// 2 次に戻ると何分も返らないので、終わるのを待たずに 5 秒で落とす
				done := make(chan string, 1)
				go func() { done <- unwrapNestedShell(in, pos, sep) }()
				var out string
				select {
				case out = <-done:
				case <-time.After(5 * time.Second):
					t.Fatalf("長い入力（%d バイト・%q の繰り返し・pos=%d sep=%q）が 5 秒で終わらない。2 次に戻っていないか", len(in), u, pos, sep)
				}
				if !strings.HasSuffix(out, string(sep)+"git reset --hard"+string(sep)) {
					t.Errorf("前提が崩れています: 末尾の bash -c '…' がほどかれていない（%q の繰り返し・pos=%d）: %q", u, pos, out[max(0, len(out)-60):])
				}
			}
		}
	}
}
