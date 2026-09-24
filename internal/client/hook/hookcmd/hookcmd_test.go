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
	fails := 0
	for _, w := range golden {
		g3, ok := SimpleCommands(w.In)
		var have3 *[][]string
		if ok {
			have3 = &g3
		}
		if g1, g2 := CommandText(w.In, true), CommandText(w.In, false); g1 != w.Text || g2 != w.KeepQuotes || !reflect.DeepEqual(have3, w.Simple) {
			fails++
			if fails <= 10 {
				t.Errorf("入力 %q:\n  実行される語   Go %q / 旧 %q\n  引用符は残す   Go %q / 旧 %q\n  単純コマンド   Go %q / 旧 %q",
					w.In, g1, w.Text, g2, w.KeepQuotes, deref(have3), deref(w.Simple))
			}
		}
	}
	t.Logf("以前の実装の記録と比べた入力: %d 件（不一致 %d）", len(golden), fails)
}

func deref(p *[][]string) any {
	if p == nil {
		return nil
	}
	return *p
}
