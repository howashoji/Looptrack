package hookcmd

import (
	"slices"
	"strings"
	"testing"
)

// TestStripHeredocsStrict は 3 つ目の読み方が、以前の読み方と狭い読み方の両方で落とされていた本物のコマンドの行を残すこと。
// 同じ表に、ふつうのヒアドキュメントの本文をこれまでどおり落とす対照を置く（落とす経路が死んでいないことの確かめ）。
func TestStripHeredocsStrict(t *testing.T) {
	const L = "git reset --hard"
	cases := []struct {
		name, in string
		keep     bool // L の行が残るか
	}{
		// 算術・パラメータ展開・代入の添字の中の <<（シェルは開きと読まない）
		{"算術 $(( ))", "echo $((1<<y))\n" + L + "\ny", true},
		{"二重引用符の中の算術", "echo \"$((1<<y))\"\n" + L + "\ny", true},
		{"算術 (( ))", "((y=1<<x))\n" + L + "\nx", true},
		{"パラメータ展開 ${x#<<EOF}", "echo ${x#<<EOF}\n" + L + "\nEOF", true},
		{"パラメータ展開 ${x:-<<EOF}", "echo ${x:-<<EOF}\n" + L + "\nEOF", true},
		{"代入の添字", "a[1<<2]=3\n" + L + "\nEOF", true},
		// 本文をシェルに渡す（本文は実行される）
		{"cat <<EOF | sh", "cat <<EOF | sh\n" + L + "\nEOF", true},
		{"cat <<'EOF'|bash -s", "cat <<'EOF'|bash -s\n" + L + "\nEOF", true},
		{"bash <<EOF", "bash <<EOF\n" + L + "\nEOF", true},
		{"/bin/sh <<EOF", "/bin/sh <<EOF\n" + L + "\nEOF", true},
		{"env -u FOO sh", "cat <<EOF | env -u FOO sh\n" + L + "\nEOF", true},
		{"| { sh; }", "cat <<EOF | { sh; }\n" + L + "\nEOF", true},
		{"\"$SHELL\" <<EOF", "\"$SHELL\" <<EOF\n" + L + "\nEOF", true},
		{"source /dev/stdin", "source /dev/stdin <<EOF\n" + L + "\nEOF", true},
		{"シェルに渡す本文の中のヒアドキュメント", "cat <<'A' | sh\ncat <<X\nA\ncat <<'B' | sh\n" + L + "\nX\nB", true},
		// 終端の名前をシェルと同じく読む（以前の読み方は別の名前を探して後ろを全部落としていた）
		{"<<E\"OF\" の終端の後ろ", "cat <<E\"OF\"\nbody\nEOF\n" + L, true},
		{"<<END-OF の終端の後ろ", "cat <<END-OF\nbody\nEND-OF\n" + L, true},
		{"<<'E'OF の終端の後ろ", "cat <<'E'OF\nbody\nEOF\n" + L, true},

		// 対照
		{"対照: <<EOF の本文", "cat <<EOF\n" + L + "\nEOF", false},
		{"対照: EOF; は終端ではない", "cat <<EOF\nEOF;\n" + L + "\nEOF", false},
		{"対照: <<'EOF' の本文", "cat <<'EOF'\n" + L + "\nEOF", false},
		{"対照: <<\\EOF の本文", "cat <<\\EOF\n" + L + "\nEOF", false},
		{"対照: 2 つの開きの 2 つ目の本文", "cat <<A <<B\nx\nA\n" + L + "\nB", false},
		{"対照: シェルでないコマンドに渡す", "cat <<EOF | grep sh\n" + L + "\nEOF", false},
		{"対照: 遠隔のシェル", "ssh host 'bash -s' <<'EOF'\n" + L + "\nEOF", false},
		{"対照: -c のシェル", "sh -c 'cat >/tmp/o' <<'EOF'\n" + L + "\nEOF", false},
		{"対照: スクリプトのファイルを読むシェル", "bash /tmp/s.sh <<'EOF'\n" + L + "\nEOF", false},
		{"対照: コメントの中の | sh", "cat <<EOF # | sh\n" + L + "\nEOF", false},
		{"対照: ふつうの語の [ の後ろの開き", "echo a[<<EOF]\n" + L + "\nEOF]", false},
		{"対照: コミットメッセージの本文", "git commit -m \"$(cat <<'EOF'\n" + L + "\nEOF\n)\"", false},
		{"対照: 終端の名前の引用が閉じない（シェルは何も実行しない）", "cat <<'EOF\n" + L, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := StripHeredocsStrict(c.in)
			if kept := slices.Contains(strings.Split(got, "\n"), L); kept != c.keep {
				t.Errorf("StripHeredocsStrict(%q) = %q、%q の行が残る: %v（期待 %v）", c.in, got, L, kept, c.keep)
			}
		})
	}
}

// TestStripHeredocsStrictIdempotent は、開きを行から外すので、もう一度掛けても結果が変わらないこと
// （入れ子のシェルをほどいてから掛け直す読み方が、本文の無い開きの後ろを全部本文と読まないため）。
func TestStripHeredocsStrictIdempotent(t *testing.T) {
	in := "cat <<'EOF'\nit's\nEOF\nbash -c 'git reset --hard'"
	once := StripHeredocsStrict(in)
	if twice := StripHeredocsStrict(once); twice != once {
		t.Errorf("2 回目で変わった: 1 回目 %q、2 回目 %q", once, twice)
	}
	if !strings.Contains(once, "bash -c 'git reset --hard'") {
		t.Errorf("前提が崩れています（終端の後ろの行が落ちた）: %q", once)
	}
	if strings.Contains(once, "<<") {
		t.Errorf("開きが行に残っている: %q", once)
	}
}

// TestHeredocLexerExprs は Exprs のときだけ、算術・展開・代入の添字の中の << を演算子と読まないこと。
// ゼロ値（狭い読み方）の数え方は変わらないことを同じ表で確かめる。
func TestHeredocLexerExprs(t *testing.T) {
	cases := []struct {
		line        string
		zero, exprs []int
	}{
		{"echo $((1<<y))", []int{9}, nil},
		{"((y=1<<x))", []int{5}, nil},
		{"echo ${x#<<EOF}", []int{9}, nil},
		{"a[1<<2]=3", []int{3}, nil},
		{"echo $[1<<2]", []int{8}, nil},
		{"echo \"$((1<<y))\"", []int{10}, nil},
		// 対照: 演算子として読むもの
		{"cat <<EOF", []int{4}, []int{4}},
		{"echo ${x:-$(cat <<EOF)}", []int{16}, []int{16}},
		{"echo a[<<EOF]", []int{7}, []int{7}},
	}
	for _, c := range cases {
		var zero HeredocLexer
		if got := zero.Ops(c.line); !slices.Equal(got, c.zero) {
			t.Errorf("前提が崩れています（ゼロ値の数え方が変わった）: Ops(%q) = %v、期待 %v", c.line, got, c.zero)
		}
		ex := HeredocLexer{Exprs: true}
		if got := ex.Ops(c.line); !slices.Equal(got, c.exprs) {
			t.Errorf("Exprs の Ops(%q) = %v、期待 %v", c.line, got, c.exprs)
		}
	}
}

// TestCommandWordsFriendly は、$'…' とコメントの中の引用符で分解できなかった形を、追加の読み方で分けられること。
// git ガードと同じく 3 つのヒアドキュメントの読み方のどれかで見えれば見えたとする。
// 以前の読み方（CommandWords）が分解できないことも同じ表で確かめる（前提）。対照は分解できていた形と、
// 閉じない引用符の行より後ろ（シェルも実行しない）。
func TestCommandWordsFriendly(t *testing.T) {
	const L = "git reset --hard"
	has := func(cmds [][]string) bool {
		for _, w := range cmds {
			if strings.Join(w, " ") == L {
				return true
			}
		}
		return false
	}
	cases := []struct {
		name, in   string
		baseSplits bool // 以前の読み方が分解できるか
		found      bool // 追加の読み方で L が単純コマンドとして見えるか
	}{
		{"$'…' の打ち消した '", "echo $'it\\'s' ; echo x\n" + L, false, true},
		{"$'…' の後ろの同じ行", "echo $'it\\'s' ; " + L, false, true},
		{"$'…' の中の <<EOF", "echo $'it\\'s <<EOF'\n" + L, false, true},
		{"コメントの中の '", "echo x # it's\n" + L, false, true},
		{"コメントの中の \"", "echo x # say \"hi\n" + L, false, true},
		{"行頭のコメントの中の '", "# it's\n" + L, false, true},
		{"閉じない引用符の行より前", L + "\necho \"", false, true},
		// 対照
		{"対照: ふつうの形", "echo a ; " + L, true, true},
		{"対照: コメントの中の語", "echo x # " + L, true, false},
		{"対照: 閉じない引用符の行より後ろ", "echo it's\n" + L, false, false},
		{"対照: 引用符の中の #", "echo 'a # b'\n" + L, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, ok := CommandWords(c.in); ok != c.baseSplits {
				t.Errorf("前提が崩れています（以前の読み方の分解の可否が変わった）: CommandWords(%q) ok = %v", c.in, ok)
			}
			var seen [][][]string
			got := false
			for _, strip := range []func(string) string{StripHeredocs, StripHeredocsNarrow, StripHeredocsStrict} {
				cmds, _ := CommandWordsFriendly(c.in, strip)
				seen = append(seen, cmds)
				got = got || has(cmds)
			}
			if got != c.found {
				t.Errorf("CommandWordsFriendly(%q) = %q、%q が見える: %v（期待 %v）", c.in, seen, L, got, c.found)
			}
		})
	}
}

// TestShlexFriendly は直した文字列そのもの（$'…' を引用符の組に、コメントを落とす）。
func TestShlexFriendly(t *testing.T) {
	cases := []struct{ in, want string }{
		{"echo $'it\\'s'", "echo \"it's\""},   // ' を含むなら二重引用符
		{"echo $'a\\'$b'", "echo \"a'\\$b\""}, // 二重引用符の中で意味を持つ $ は打ち消す
		{"echo $'a\\tb'", "echo 'a\\tb'"},
		{"echo x # it's\necho y", "echo x \necho y"},
		{"echo \"$'x'\"", "echo \"$'x'\""}, // 二重引用符の中の $' はただの文字
		{"echo 'a # b'", "echo 'a # b'"},
		{"echo a#b", "echo a#b"},
		{"x=$(echo $'q\\'') # c", "x=$(echo \"q'\") "},
	}
	for _, c := range cases {
		if got := ShlexFriendly(c.in); got != c.want {
			t.Errorf("ShlexFriendly(%q) = %q、期待 %q", c.in, got, c.want)
		}
	}
}

// TestStripHeredocsStrictUnclosed は、閉じない引用符か $( が開いた行から後ろを落とすこと（シェルはその行から後ろを
// 実行しない）。対照は閉じている形（何も落とさない）。
func TestStripHeredocsStrictUnclosed(t *testing.T) {
	cases := []struct{ in, want string }{
		{"echo a\necho it's\ngit reset --hard", "echo a"},
		{"echo a\necho \"x\ngit reset --hard", "echo a"},
		{"echo a\necho $(cat <<'EOF'\ngit reset --hard", "echo a"},
		{"git reset --hard\necho `x", "git reset --hard"},
		// 対照
		{"echo 'a'\ngit reset --hard", "echo 'a'\ngit reset --hard"},
		{"echo x # it's\ngit reset --hard", "echo x # it's\ngit reset --hard"},
		{"echo $'it\\'s'\ngit reset --hard", "echo $'it\\'s'\ngit reset --hard"},
		{"echo 'a\nb'\ngit reset --hard", "echo 'a\nb'\ngit reset --hard"},
	}
	for _, c := range cases {
		if got := StripHeredocsStrict(c.in); got != c.want {
			t.Errorf("StripHeredocsStrict(%q) = %q、期待 %q", c.in, got, c.want)
		}
	}
}
