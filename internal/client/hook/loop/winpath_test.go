package loop

import (
	"reflect"
	"testing"
)

// 引用符で囲まない Windows の絶対パス（`cd D:\a\b`）を、posix の規則で読んで `\` を落とすと `D:ab` になり、
// コミットした作業ツリーが分からなくなる。区切りの混ざった形・ドライブ文字の小文字も、同じ場所として読めること。
// OS に依らない文字列の処理なので、どの OS でも走る。
func TestCommandSegmentsWindowsPath(t *testing.T) {
	cases := []struct {
		cmd  string
		want [][]string
	}{
		{`cd D:\imtest-tmp\x\wt && git commit -m a`, [][]string{{"cd", "D:/imtest-tmp/x/wt"}, {"git", "commit", "-m", "a"}}},
		{`cd D:/imtest-tmp\x/wt; git commit -m a`, [][]string{{"cd", "D:/imtest-tmp/x/wt"}, {"git", "commit", "-m", "a"}}},
		{`cd d:\a\b && git -C e:\c\d commit -m x`, [][]string{{"cd", "d:/a/b"}, {"git", "-C", "e:/c/d", "commit", "-m", "x"}}},
		// 引用符の中は今までどおり（posix でも二重引用符の中の `\a` は `\` を残す）
		{`cd "D:\a\b" && git commit -m a`, [][]string{{"cd", `D:\a\b`}, {"git", "commit", "-m", "a"}}},
		// 対照: ドライブ文字で始まらない語の `\` は posix の規則のまま（打ち消し）
		{`echo a\ b && git commit -m a`, [][]string{{"echo", "a b"}, {"git", "commit", "-m", "a"}}},
		{`git commit -m D:x`, [][]string{{"git", "commit", "-m", "D:x"}}},
		// 直後が空白・区切りの `\` は打ち消しのまま（Git Bash の `D:/My\ Docs`）
		{`cd D:/My\ Docs/wt && git commit -m a`, [][]string{{"cd", "D:/My Docs/wt"}, {"git", "commit", "-m", "a"}}},
		{`echo C:\foo\;git commit -m x`, [][]string{{"echo", "C:/foo;git", "commit", "-m", "x"}}},
		// コメント・ヒアドキュメント・$'…' の中の `'` で引用符の読みがずれない（前処理の後に読む）
		{"git status # don't\ncd D:\\a\\wt && git commit -m a", [][]string{{"git", "status"}, {"cd", "D:/a/wt"}, {"git", "commit", "-m", "a"}}},
		{"cat <<EOF\ndon't\nEOF\ncd D:\\a\\wt && git commit -m a", [][]string{{"cat"}, {"cd", "D:/a/wt"}, {"git", "commit", "-m", "a"}}},
		{`echo $'it\'s' ; cd D:\a\wt && git commit -m a`, [][]string{{"echo", "it's"}, {"cd", "D:/a/wt"}, {"git", "commit", "-m", "a"}}},
	}
	for _, c := range cases {
		if got := commandSegments(c.cmd); !reflect.DeepEqual(got, c.want) {
			t.Errorf("commandSegments(%q) = %q, want %q", c.cmd, got, c.want)
		}
	}
}
