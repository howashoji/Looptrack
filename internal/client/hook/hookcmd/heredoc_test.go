package hookcmd

import "testing"

// TestStripHeredocsNarrow は狭い読み方が、シェルが開きと読まない `<<` の次の行を残すこと。
// 同じ表に、ふつうのヒアドキュメントの本文をこれまでどおり落とす対照を置く（落とす経路が死んでいないことの確かめ）。
func TestStripHeredocsNarrow(t *testing.T) {
	cases := []struct {
		name, in    string
		base, narrw string // 以前の読み方（StripHeredocs）と狭い読み方（StripHeredocsNarrow）の結果
	}{
		// 開きと読まない形: 以前の読み方は次の行を落とし、狭い読み方は残す
		{"ヒアストリングの後の名前", "cat <<<EOF\ngit reset --hard",
			"cat <<<EOF", "cat <<<EOF\ngit reset --hard"},
		{"一重引用符の中", "echo 'a <<EOF'\ngit reset --hard",
			"echo 'a <<EOF'", "echo 'a <<EOF'\ngit reset --hard"},
		{"二重引用符の中", "echo \"a <<EOF\"\ngit reset --hard",
			"echo \"a <<EOF\"", "echo \"a <<EOF\"\ngit reset --hard"},
		{"$'…' の中", "echo $'it\\'s <<EOF'\ngit reset --hard",
			"echo $'it\\'s <<EOF'", "echo $'it\\'s <<EOF'\ngit reset --hard"},
		{"コメントの中", "# cat <<EOF\ngit reset --hard",
			"# cat <<EOF", "# cat <<EOF\ngit reset --hard"},
		{"行の途中のコメントの中", "echo x # <<EOF\ngit reset --hard",
			"echo x # <<EOF", "echo x # <<EOF\ngit reset --hard"},
		{"打ち消した <", "echo \\<<EOF\ngit reset --hard",
			"echo \\<<EOF", "echo \\<<EOF\ngit reset --hard"},
		{"行をまたぐ一重引用符の中", "echo 'a\ncat <<EOF'\ngit reset --hard",
			"echo 'a\ncat <<EOF'", "echo 'a\ncat <<EOF'\ngit reset --hard"},
		// ヒアストリングの後ろの本物の開きは、狭い読み方でも開きと読む
		{"ヒアストリングの後ろの開き", "cat <<<x <<EOF\nbody\nEOF\ngit reset --hard",
			"cat <<<x <<EOF", "cat <<<x <<EOF\ngit reset --hard"},

		// 対照: ふつうのヒアドキュメントの本文は、どちらの読み方でも落とす
		{"対照: <<EOF の本文", "cat <<EOF\ngit reset --hard\nEOF\necho after",
			"cat <<EOF\necho after", "cat <<EOF\necho after"},
		{"対照: <<'EOF' の本文", "cat <<'EOF'\ngit reset --hard\nEOF",
			"cat <<'EOF'", "cat <<'EOF'"},
		{"対照: <<-EOF の本文", "cat <<-EOF\n\tgit reset --hard\n\tEOF",
			"cat <<-EOF", "cat <<-EOF"},
		{"対照: コマンド置換の中（二重引用符の中の $( … )）",
			"git commit -m \"$(cat <<'EOF'\ngit reset --hard\nEOF\n)\"",
			"git commit -m \"$(cat <<'EOF'\n)\"", "git commit -m \"$(cat <<'EOF'\n)\""},
		{"対照: バッククォートの中", "x=`cat <<EOF\ngit reset --hard\nEOF\n`",
			"x=`cat <<EOF\n`", "x=`cat <<EOF\n`"},
		{"対照: 本文の引用符は数えない", "cat <<EOF\nit's\nEOF\ncat <<EOF\ngit reset --hard\nEOF",
			"cat <<EOF\ncat <<EOF", "cat <<EOF\ncat <<EOF"},
		// <<\EOF は以前の読み方が開きと読まない。狭い読み方も同じ（本文を新たに落とさない）
		{"据え置き: <<\\EOF", "cat <<\\EOF\ngit reset --hard\nEOF",
			"cat <<\\EOF\ngit reset --hard\nEOF", "cat <<\\EOF\ngit reset --hard\nEOF"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := StripHeredocs(c.in); got != c.base {
				t.Errorf("前提が崩れています（以前の読み方の結果が変わった）: StripHeredocs(%q) = %q、期待 %q", c.in, got, c.base)
			}
			if got := StripHeredocsNarrow(c.in); got != c.narrw {
				t.Errorf("StripHeredocsNarrow(%q) = %q、期待 %q", c.in, got, c.narrw)
			}
		})
	}
}

// TestHeredocLexerOps は `<<` を演算子と読む位置。<<< の並びと引用符の中を外し、ふつうの開きは拾う（対照）。
func TestHeredocLexerOps(t *testing.T) {
	cases := []struct {
		line string
		want []int
	}{
		{"cat <<EOF", []int{4}},           // 対照
		{"cat <<-EOF", []int{4}},          // 対照
		{"cat <<<EOF", nil},               // ヒアストリング
		{"cat <<<<EOF", nil},              // <<< の続き
		{"echo '<<EOF'", nil},             // 一重引用符の中
		{"echo \"<<EOF\"", nil},           // 二重引用符の中
		{"echo \"$(cat <<EOF", []int{12}}, // 二重引用符の中のコマンド置換
		{"# <<EOF", nil},                  // コメント
		{"a#<<EOF", []int{2}},             // 語の途中の # はコメントではない
		{"echo \\<<EOF", nil},             // 打ち消した <
	}
	for _, c := range cases {
		var lx HeredocLexer
		got := lx.Ops(c.line)
		if len(got) != len(c.want) {
			t.Errorf("Ops(%q) = %v、期待 %v", c.line, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("Ops(%q) = %v、期待 %v", c.line, got, c.want)
			}
		}
	}
}
