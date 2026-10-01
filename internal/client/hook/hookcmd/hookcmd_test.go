package hookcmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// 以前の hook（1.0.0 より前）のテストのケース（「データは落とす」「コマンドの語は残す」の両方）。
func TestCommandText(t *testing.T) {
	has := func(name, text, needle string, want bool) {
		t.Helper()
		if strings.Contains(text, needle) != want {
			t.Errorf("%s: %q に %q が含まれる=%v のはず", name, text, needle, want)
		}
	}
	cmd := "cat <<'EOT'\nSECRET\nEOT\necho done"
	has("ヒアドキュメント本文は落ちる", CommandText(cmd, true), "SECRET", false)
	has("ヒアドキュメントの後ろのコマンドは残る", CommandText(cmd, true), "echo done", true)

	cmd = "git commit -m \"$(cat <<'EOF'\nMYREPO のことを書いた本文\nEOF\n)\""
	has("コミットメッセージ本文の語は落ちる", CommandText(cmd, false), "MYREPO", false)
	has("git commit という語は残る", CommandText(cmd, true), "git commit", true)

	cmd = `echo '{"command":"git commit -m x"}'`
	has("引用符の中の git commit は落ちる", CommandText(cmd, true), "git commit", false)
	has("--keep-quotes なら引用符の中は残る", CommandText(cmd, false), "git commit", true)

	has("実行される git commit は残る", CommandText("git commit -m 'x'", true), "git commit", true)
	has("実行される git add -A は残る", CommandText("git add -A && git commit -m 'x'", true), "git add -A", true)
	has("--keep-quotes は引数の中身を残す", CommandText(`git -C "/path/to/MYREPO" add -A`, false), "MYREPO", true)

	has("<<- の本文も落ちる", CommandText("cat <<-EOF\n本文\nEOF\necho tail", true), "本文", false)
	has("引用符なしの開きでも落ちる", CommandText("cat <<EOF\n本文2\nEOF\necho tail", true), "本文2", false)

	if got := CommandText("cat <<'EOF'\n途中で終わる", true); got != "cat <<" {
		t.Errorf("終端が無くても例外にならない（既定）: %q", got)
	}
	if got := CommandText("cat <<'EOF'\n途中で終わる", false); got != "cat <<'EOF'" {
		t.Errorf("終端が無くても開き行は残る: %q", got)
	}
	if CommandText("", true) != "" {
		t.Error("空文字は空")
	}
}

func TestSimpleCommands(t *testing.T) {
	got, ok := SimpleCommands("cd x && looptrack issue comment IM-1 \"本文 IM-2\n次の行\"; git status\necho ok | cat")
	want := [][]string{{"cd", "x"}, {"looptrack", "issue", "comment", "IM-1", "本文 IM-2\n次の行"}, {"git", "status"}, {"echo", "ok"}, {"cat"}}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("区切りで分ける: %q", got)
	}
	got, ok = SimpleCommands("looptrack issue comment IM-1 \"$(cat <<'EOF'\nIM-2\nEOF\n)\"")
	want = [][]string{{"looptrack", "issue", "comment", "IM-1", "$(cat <<'EOF'\n)"}}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("ヒアドキュメント本文を落とす: %q", got)
	}
	if _, ok := SimpleCommands(`echo "abc`); ok {
		t.Error("閉じていない引用符で失敗するはず")
	}
}

// TestCommandWords はリダイレクト（記述子・演算子・行き先）を語から外すことを確かめる。
// 対照として、同じ入力を SimpleCommands に掛けると記述子の `2` が引数に残ることも見る（前提が崩れたら分かるように）。
func TestCommandWords(t *testing.T) {
	cases := []struct {
		in   string
		want [][]string
	}{
		{"git checkout main 2>&1", [][]string{{"git", "checkout", "main"}}},
		{"git checkout main 2>&1 | tail -20", [][]string{{"git", "checkout", "main"}, {"tail", "-20"}}},
		{"git checkout main 2>/dev/null", [][]string{{"git", "checkout", "main"}}},
		{"git checkout main 2> /dev/null", [][]string{{"git", "checkout", "main"}}},
		{"git checkout main >out.log", [][]string{{"git", "checkout", "main"}}},
		{"git checkout main > out.log 2>&1", [][]string{{"git", "checkout", "main"}}},
		{"git checkout main &>/dev/null", [][]string{{"git", "checkout", "main"}}},
		{"git checkout main >>out.log 2>>err.log", [][]string{{"git", "checkout", "main"}}},
		{"git checkout main 2>&1 && git status", [][]string{{"git", "checkout", "main"}, {"git", "status"}}},
		{"git checkout main 1>&2; echo ok", [][]string{{"git", "checkout", "main"}, {"echo", "ok"}}},
		{"sort < /tmp/list.txt", [][]string{{"sort"}}},
		{"cat <<EOF\n本文\nEOF\necho tail", [][]string{{"cat"}, {"echo", "tail"}}},
		// 演算子は単純コマンドを終わらせない（行き先の後ろの語は同じコマンドの引数）
		{"git checkout >log main -- f", [][]string{{"git", "checkout", "main", "--", "f"}}},
		// 空白で離れた数字・引用符付きの数字は、シェルでも引数（記述子ではない）
		{"git checkout main 2 >x", [][]string{{"git", "checkout", "main", "2"}}},
		{"git checkout main '2'>x", [][]string{{"git", "checkout", "main", "2"}}},
		// リダイレクトでない区切りは従来どおり分ける
		{"a && b; c | d", [][]string{{"a"}, {"b"}, {"c"}, {"d"}}},
		{"diff <(a) <(b)", [][]string{{"diff"}, {"a"}, {"b"}}},
	}
	for _, c := range cases {
		got, ok := CommandWords(c.in)
		if !ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("CommandWords(%q) = %q (ok=%v), want %q", c.in, got, ok, c.want)
		}
	}
	if s, _ := SimpleCommands("git checkout main 2>&1"); !reflect.DeepEqual(s, [][]string{{"git", "checkout", "main", "2"}, {"1"}}) {
		t.Errorf("前提が崩れている: SimpleCommands は記述子の 2 を引数に残す形のはず（それを外すのが CommandWords）: %q", s)
	}
	if _, ok := CommandWords(`echo "abc`); ok {
		t.Error("閉じていない引用符で失敗するはず")
	}
}

// TestCommandWordsMatchesSimpleCommands は、リダイレクトの無い入力では CommandWords が SimpleCommands と
// 同じ結果になることを、以前の hook の記録（testdata/hookcmd_golden.json）の全入力で確かめる。
func TestCommandWordsMatchesSimpleCommands(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "hookcmd_golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var golden []struct {
		In string `json:"in"`
	}
	if err := json.Unmarshal(b, &golden); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, w := range golden {
		if strings.ContainsAny(StripHeredocs(w.In), "<>") {
			continue
		}
		n++
		s, ok1 := SimpleCommands(w.In)
		c, ok2 := CommandWords(w.In)
		if ok1 != ok2 || !reflect.DeepEqual(s, c) {
			t.Errorf("入力 %q: SimpleCommands %q / CommandWords %q", w.In, s, c)
		}
	}
	if n == 0 {
		t.Fatal("リダイレクトの無い入力が 1 件も無い（前提が崩れている）")
	}
	t.Logf("突き合わせた入力: %d 件", n)
}

func TestSplitLines(t *testing.T) {
	for in, want := range map[string][]string{
		"":           nil,
		"a":          {"a"},
		"a\n":        {"a"},
		"a\n\nb":     {"a", "", "b"},
		"a\r\nb\rc":  {"a", "b", "c"},
		"a\vb\x1cc":  {"a", "b", "c"},
		"a\u2028b\n": {"a", "b"},
	} {
		if got := SplitLines(in); !reflect.DeepEqual(got, want) {
			t.Errorf("SplitLines(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestHookcmdGolden は以前の hook（1.0.0 より前）の 3 つの取り出し方（実行される語だけ・引用符の中は残す・単純コマンドごとの語）の
// 結果と同じことを確かめる。手で選んだ入力と乱数で組み立てたシェルらしい入力（区切り・引用符・\・ヒアドキュメント・改行を多めに。
// 種は固定）を以前の実装に通して記録したもの（testdata/hookcmd_golden.json。以前の実装は撤去し、記録の仕掛けも消した）。
//
// 実行される語（text）は読み方が 2 つある。**Windows の読み方（CommandTextWin）は以前の実装と同じ組の取り方で、
// 記録と全件一致する**。posix の読み方（CommandText(cmd, true)）は、引用符を左から 1 回で数え、引用符の外と二重引用符の
// 中でバックスラッシュを打ち消しに数えるので、記録とわざと違う入力がある。その入力と posix の読み方の結果を別表
// （testdata/hookcmd_posix_text.json）に置き、**別表に無い入力が記録と食い違ったら失敗する**ようにしてある。
// 別表の値は、入力の引用符・バックスラッシュ・改行以外を無害な文字に替え、接頭辞ごとに `/bin/sh -n`（構文だけ読む）が
// 引用符の中で終わるかを調べて得た「引用符の外の文字」と、全件一致することを確かめた（実行はしていない）。
func TestHookcmdGolden(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "hookcmd_golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var golden []struct {
		In         string      `json:"in"`
		Text       string      `json:"text"`
		KeepQuotes string      `json:"keep_quotes"`
		Simple     *[][]string `json:"simple"`
	}
	if err := json.Unmarshal(b, &golden); err != nil {
		t.Fatal(err)
	}
	if len(golden) == 0 {
		t.Fatal("記録が空")
	}
	d, err := os.ReadFile(filepath.Join("testdata", "hookcmd_posix_text.json"))
	if err != nil {
		t.Fatal(err)
	}
	var posix []struct {
		In   string `json:"in"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(d, &posix); err != nil {
		t.Fatal(err)
	}
	if len(posix) == 0 {
		t.Fatal("別表が空（前提が崩れています）")
	}
	posixText := map[string]string{}
	for _, r := range posix {
		posixText[r.In] = r.Text
	}
	fails, differ := 0, 0
	for _, w := range golden {
		wantPosix, ok := posixText[w.In]
		if ok {
			if wantPosix == w.Text {
				t.Errorf("別表の %q は記録と同じ値（別表に置く必要が無い）", w.In)
			}
			delete(posixText, w.In)
			differ++
		} else {
			wantPosix = w.Text
		}
		g3, ok := SimpleCommands(w.In)
		var have3 *[][]string
		if ok {
			have3 = &g3
		}
		g1, gw, g2 := CommandText(w.In, true), CommandTextWin(w.In), CommandText(w.In, false)
		if g1 != wantPosix || gw != w.Text || g2 != w.KeepQuotes || !reflect.DeepEqual(have3, w.Simple) {
			fails++
			if fails <= 10 {
				t.Errorf("入力 %q:\n  実行される語（posix）   Go %q / 期待 %q\n  実行される語（Windows） Go %q / 旧 %q\n  引用符は残す   Go %q / 旧 %q\n  単純コマンド   Go %q / 旧 %q",
					w.In, g1, wantPosix, gw, w.Text, g2, w.KeepQuotes, deref(have3), deref(w.Simple))
			}
		}
	}
	for in := range posixText {
		t.Errorf("別表にあるのに記録に無い入力: %q（別表が古い）", in)
	}
	t.Logf("以前の実装の記録と比べた入力: %d 件（うち posix の読み方が記録とわざと違う %d 件・不一致 %d）", len(golden), differ, fails)
}

// TestStripQuotesReadings は引用符の組の 2 つの読み方を固定する。
//   - posix（StripQuotes）: 左から 1 回で数える。引用符の外と二重引用符の中ではバックスラッシュが次の 1 文字を打ち消す。
//     期待値は /bin/sh -x で実際に実行される語と照らした。
//   - Windows（StripQuotesWin）: バックスラッシュを打ち消しに使わない（PowerShell では `\` はただの文字）。以前の hook と同じ組の取り方。
func TestStripQuotesReadings(t *testing.T) {
	cases := []struct{ name, in, posix, win string }{
		// sh では全体が 1 つの引数（後ろのコマンドは実行されない）。PowerShell では `"x\"` で閉じ、後ろのコマンドが実行される
		{"引数の中の reset --hard", `git commit -m "x\" ; git reset --hard \""`, `git commit -m `, `git commit -m  ; git reset --hard \`},
		{"引数の中の clean -fd", `echo "a\" ; git clean -fd \""`, `echo `, `echo  ; git clean -fd \`},
		{"引数の中の秘密の読み出し", `cat "x\" ; cat /tmp/x/.ssh/id_rsa \""`, `cat `, `cat  ; cat /tmp/x/.ssh/id_rsa \`},
		{"打ち消した引用符 1 つ", `echo "a\"b"`, `echo `, `echo b"`},
		{"打ち消した引用符の後ろに打ち消したバックスラッシュ", `echo "a\\\" ; cat /tmp/x/.env "`, `echo `, `echo  ; cat /tmp/x/.env "`},
		// バックスラッシュが偶数本なら打ち消されたのはバックスラッシュで、引用符は閉じる（どちらの読み方でも後ろは残る）
		{"打ち消したバックスラッシュの後ろで閉じる", `echo "a\\" ; git reset --hard "b"`, `echo  ; git reset --hard `, `echo  ; git reset --hard `},
		{"打ち消したバックスラッシュ 2 組の後ろで閉じる", `echo "a\\\\" ; git reset --hard`, `echo  ; git reset --hard`, `echo  ; git reset --hard`},
		// 引用符の外の `\"` は sh ではただの文字（組を開かない）。Windows の読み方では組になり、間のコマンドが落ちる
		{"引用符の外の打ち消しで挟む", `cp .env.example x\" ; cat /tmp/x/id_rsa \"`, `cp .env.example x\" ; cat /tmp/x/id_rsa \"`, `cp .env.example x\`},
		// 種類の違う引用符が交ざる形（左から数えれば `'` は二重引用符の中の文字）
		{"二重引用符の中の一重引用符", `echo "it's" ; cat /tmp/x/.env ; echo 'x'`, `echo  ; cat /tmp/x/.env ; echo `, `echo "itx'`},
		{"一重引用符の中の二重引用符", `echo 'say "hi' ; cat /tmp/x/.env ; echo "x"`, `echo  ; cat /tmp/x/.env ; echo `, `echo  ; cat /tmp/x/.env ; echo `},
		// PowerShell の末尾が `\` のパス（sh では `\"` が閉じないので、2 つ目の `"` までが 1 組）
		{"末尾が \\ の Windows のパス", `Get-ChildItem "C:\proj\" ; Get-Content "C:\proj\.env"`, `Get-ChildItem C:\proj\.env"`, `Get-ChildItem  ; Get-Content `},
		// 従来どおりのもの
		{"ふつうの組", `echo "abc" def`, `echo  def`, `echo  def`},
		{"一重引用符は打ち消しを数えない", `echo 'a\' ; git reset --hard`, `echo  ; git reset --hard`, `echo  ; git reset --hard`},
		{"閉じていない引用符はそのまま", `echo "abc`, `echo "abc`, `echo "abc`},
		{"引用符が無い", `git reset --hard`, `git reset --hard`, `git reset --hard`},
	}
	for _, c := range cases {
		eqs(t, c.name+"（posix）", StripQuotes(c.in), c.posix)
		eqs(t, c.name+"（Windows）", StripQuotesWin(c.in), c.win)
	}
	// 読む側（QuotedTexts*）も、それぞれ落とす側と同じ組を取る
	if got := QuotedTexts(`cat "\"" "/tmp/x/.env"`); !reflect.DeepEqual(got, []string{`\"`, `/tmp/x/.env`}) {
		t.Errorf("QuotedTexts: got %q", got)
	}
	if got := QuotedTextsWin(`Get-ChildItem "C:\proj\" ; Get-Content "C:\proj\.env"`); !reflect.DeepEqual(got, []string{`C:\proj\`, `C:\proj\.env`}) {
		t.Errorf("QuotedTextsWin: got %q", got)
	}
	// CommandTexts は 2 つの読み方が違うときだけ 2 つ返す
	if got := CommandTexts(`echo "abc" def`); len(got) != 1 {
		t.Errorf("CommandTexts（同じ）: got %q", got)
	}
	if got := CommandTexts(`echo "a\"b"`); !reflect.DeepEqual(got, []string{`echo `, `echo b"`}) {
		t.Errorf("CommandTexts（違う）: got %q", got)
	}
}

// TestSimpleSegmentsReadings は単純コマンドへの分け方も、読み方ごとに落とす側と同じ数え方をすることを固定する。
func TestSimpleSegmentsReadings(t *testing.T) {
	cases := []struct {
		name, in string
		want     []string
		ok       bool
		win      []string
		winOK    bool
	}{
		{"引数の中の区切り", `cat "x\" ; cat /tmp/x/id_rsa \""`,
			[]string{`cat "x\" ; cat /tmp/x/id_rsa \""`}, true,
			[]string{`cat "x\" `, ` cat /tmp/x/id_rsa \""`}, true},
		{"打ち消したバックスラッシュの後ろの区切りで切る", `echo "a\\" ; cat /tmp/x/.env`,
			[]string{`echo "a\\" `, ` cat /tmp/x/.env`}, true,
			[]string{`echo "a\\" `, ` cat /tmp/x/.env`}, true},
		{"引用符の外の打ち消しは組を開かない", `cp .env.example x\" ; cat /tmp/x/id_rsa \"`,
			[]string{`cp .env.example x\" `, ` cat /tmp/x/id_rsa \"`}, true,
			[]string{`cp .env.example x\" ; cat /tmp/x/id_rsa \"`}, true},
		{"引用符の外の打ち消した一重引用符", `cp .env.example .env ; echo don\'t`,
			[]string{`cp .env.example .env `, ` echo don\'t`}, true,
			[]string{`cp .env.example .env ; echo don\'t`}, false},
		{"コメントの中の引用符は数えない", "cp .env.example .env  # don't\necho ok",
			[]string{"cp .env.example .env  # don't", "echo ok"}, true,
			[]string{"cp .env.example .env  # don't", "echo ok"}, true},
	}
	for _, c := range cases {
		if got, ok := SimpleSegments(c.in); ok != c.ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s（posix）: got %q %v, want %q %v", c.name, got, ok, c.want, c.ok)
		}
		if got, ok := SimpleSegmentsWin(c.in); ok != c.winOK || !reflect.DeepEqual(got, c.win) {
			t.Errorf("%s（Windows）: got %q %v, want %q %v", c.name, got, ok, c.win, c.winOK)
		}
	}
}

func deref(p *[][]string) any {
	if p == nil {
		return nil
	}
	return *p
}
