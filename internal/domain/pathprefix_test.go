package domain

import "testing"

func TestReplacePathPrefixes(t *testing.T) {
	// 置き換え先は呼ぶ側が決める。印の形（{{LT:…}}）でも境界の規則は変わらない。
	ps := []PathPrefix{
		{"/home/u/proj/sub", "{{LT:SUB}}"}, // 長い値が先
		{"/home/u/proj", "{{LT:ROOT0}}"},
		{`C:\Users\u\proj`, "{{LT:ROOT1}}"},
		{"/home/u", "{{LT:HOME}}"},
	}
	for _, c := range []struct{ name, in, want string }{
		{"区切りは残す", "cat /home/u/proj/a.go", "cat {{LT:ROOT0}}/a.go"},
		{"長い値が先", "/home/u/proj/sub/x と /home/u/y", "{{LT:SUB}}/x と {{LT:HOME}}/y"},
		{"Windows の区切り", `C:\Users\u\proj\cmd\x.go`, `{{LT:ROOT1}}\cmd\x.go`},
		{"値そのもの（終わり）", "HOME=/home/u", "HOME={{LT:HOME}}"},
		{"文末の句点", "場所は /home/u/proj.", "場所は {{LT:ROOT0}}."},
		{"かっこ・引用符", "(`/home/u/proj/x`)", "(`{{LT:ROOT0}}/x`)"},
		{"日本語の直後", "パス/home/u/proj/x", "パス{{LT:ROOT0}}/x"},
		{"1 行に複数", "/home/u/proj/a /home/u/proj/b", "{{LT:ROOT0}}/a {{LT:ROOT0}}/b"},
		{"置き換えた結果は読まない", "/home/u/proj/a", "{{LT:ROOT0}}/a"},
		// 当たらない形
		{"続きのある名前", "/home/u/proj2/a", "{{LT:HOME}}/proj2/a"},
		{"拡張子の付いた名前", "/home/u/proj.bak/a", "{{LT:HOME}}/proj.bak/a"},
		{"別のパスの途中", "/x/home/u/proj/a", "/x/home/u/proj/a"},
		{"相対パスの途中", "a/home/u/proj", "a/home/u/proj"},
		{"~ の直後", "~/home/u/proj", "~/home/u/proj"},
		{"大小は区別する", "/Home/u/proj/a", "/Home/u/proj/a"},
		{"置き換え先に似た文字列は触らない", "{{LT:ROOT0}}/a", "{{LT:ROOT0}}/a"},
	} {
		if got := ReplacePathPrefixes(c.in, ps); got != c.want {
			t.Errorf("%s: %q → %q, want %q", c.name, c.in, got, c.want)
		}
	}
	// 値が無ければ何も置き換えない
	if got := ReplacePathPrefixes("/home/u/proj/a", nil); got != "/home/u/proj/a" {
		t.Errorf("値なしで置き換わった: %q", got)
	}
	// 並びの先頭が優先される（長い順に並べるのは呼ぶ側）。対照: 並びを逆にすると短い値が先に当たる
	rev := []PathPrefix{{"/home/u", "{{LT:HOME}}"}, {"/home/u/proj", "{{LT:ROOT0}}"}}
	if got := ReplacePathPrefixes("/home/u/proj/a", rev); got != "{{LT:HOME}}/proj/a" {
		t.Errorf("並びの先頭が優先されない（前提が崩れています）: %q", got)
	}
}
