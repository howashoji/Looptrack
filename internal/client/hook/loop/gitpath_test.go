package loop

import (
	"runtime"
	"testing"
)

// これらは純粋な関数の検査。**どの OS で走らせても同じ結果**になるように、Windows の形のパスを入力に入れる
// （GOOS で分岐して skip しない。Windows だけで落ちる欠陥を手元の `go test` で捕まえるための検査なので、
// skip すると検査そのものが無意味になる）。

func TestWinStylePath(t *testing.T) {
	for _, c := range []struct {
		in   string
		want bool
	}{
		{`C:\Users\foo`, true},
		{`C:/Users/foo`, true}, // ドライブ名があれば区切りが `/` でも Windows の形
		{`c:\users\foo`, true},
		{`C:foo`, true}, // ドライブからの相対
		{`\\server\share\a`, true},
		{`internal\client`, true}, // `\` を区切りに使っている
		{`/Users/foo`, false},
		{`internal/client`, false},
		{``, false},
	} {
		if got := winStylePath(c.in); got != c.want {
			t.Errorf("winStylePath(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestNormPath(t *testing.T) {
	for _, c := range []struct {
		in   string
		fold bool
		want string
	}{
		// Windows の形（大文字小文字をそろえる）
		{`C:\Users\foo`, true, "c:/users/foo"},
		{`C:/Users\foo`, true, "c:/users/foo"},   // 区切りの混在
		{`c:\users\foo\`, true, "c:/users/foo"},  // 末尾の区切り
		{`C:\Users\\foo`, true, "c:/users/foo"},  // 区切りの重なり
		{`C:\Users\foo\\`, true, "c:/users/foo"}, // 末尾の区切りが 2 つ
		{`C:\`, true, "c:/"},                     // ドライブ直下は残す
		{`C:`, true, "c:"},                       // ドライブからの相対
		{`\\server\share\a`, true, "//server/share/a"},
		{`\\Server\Share\A\`, true, "//server/share/a"}, // UNC・大文字小文字・末尾の区切り
		// POSIX の形（大文字小文字は区別する）
		{`/a/b/`, false, "/a/b"},
		{`/a//b`, false, "/a/b"},
		{`/`, false, "/"},
		{`/a/B`, false, "/a/B"},
		{``, false, ""},
	} {
		if got := normPath(c.in, c.fold); got != c.want {
			t.Errorf("normPath(%q, %v) = %q, want %q", c.in, c.fold, got, c.want)
		}
	}
}

func TestSamePath(t *testing.T) {
	for _, c := range []struct {
		a, b string
		fold bool
		want bool
	}{
		{`C:\Users\foo`, `c:/users/foo/`, true, true}, // 区切り・大文字小文字・末尾
		{`C:\Users\foo`, `C:\Users\bar`, true, false},
		{`C:\Users\foo`, `C:\Users\foobar`, true, false},
		{`\\server\share\a`, `\\SERVER\Share\A`, true, true},
		{`/a/b`, `/a/b/`, false, true},
		{`/a/B`, `/a/b`, false, false}, // fold しないときは区別する
		{``, `/a`, false, false},
		{`/a`, ``, false, false},
	} {
		if got := samePath(c.a, c.b, c.fold); got != c.want {
			t.Errorf("samePath(%q, %q, %v) = %v, want %v", c.a, c.b, c.fold, got, c.want)
		}
	}
}

func TestUnderPath(t *testing.T) {
	for _, c := range []struct {
		parent, child string
		fold          bool
		want          bool
	}{
		{`C:\proj`, `C:\proj\.claude\worktrees\other`, true, true},
		{`C:\proj`, `c:/PROJ/internal`, true, true},
		{`C:\proj`, `C:\project`, true, false}, // 接頭辞の取り違え
		{`C:\proj`, `C:\proj`, true, true},
		{`C:\proj\`, `C:\proj\a`, true, true},
		{`C:\proj`, `D:\proj\a`, true, false}, // ドライブが違う
		{`/a`, `/a/b`, false, true},
		{`/a`, `/ab`, false, false},
		{`/`, `/a`, false, true},
		{``, `/a`, false, false},
	} {
		if got := underPath(c.parent, c.child, c.fold); got != c.want {
			t.Errorf("underPath(%q, %q, %v) = %v, want %v", c.parent, c.child, c.fold, got, c.want)
		}
	}
}

func TestIsAbsPath(t *testing.T) {
	for _, c := range []struct {
		in   string
		want bool
	}{
		{`C:\Users\foo`, true},
		{`C:/Users/foo`, true},
		{`c:\users`, true},
		{`C:foo`, false}, // ドライブからの相対
		{`C:`, false},
		{`\\server\share`, true},
		{`/Users/foo`, true},
		{`internal/client`, false},
		{``, false},
	} {
		if got := isAbsPath(c.in); got != c.want {
			t.Errorf("isAbsPath(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestLostSeparators は「引用符で囲まない Windows のパスが、シェルの語の分け方で区切りを失った形」の見分け。
// 実例: `git -C C:\Users\x\other add -A` は語に分けると `C:Usersxother` になる（`\` がエスケープとして食われる）。
func TestLostSeparators(t *testing.T) {
	for _, c := range []struct {
		in   string
		want bool
	}{
		{`C:Usersrunneradminprojotherx`, true},
		{`c:x`, true},
		{`C:\Users\x`, false}, // 区切りが残っている（絶対パス）
		{`C:/Users/x`, false},
		{`C:`, false}, // ドライブ名だけ
		{`internal`, false},
		{`/Users/x`, false},
		{``, false},
	} {
		if got := lostSeparators(c.in); got != c.want {
			t.Errorf("lostSeparators(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestCaseFold(t *testing.T) {
	// Windows の形が混ざれば、どの OS でも大文字小文字をそろえて比べる。
	for _, c := range [][2]string{
		{`C:\proj`, `/tmp/x`},
		{`/tmp/x`, `C:\proj`},
		{`/tmp/x`, `C:/proj`},
	} {
		if !caseFold(c[0], c[1]) {
			t.Errorf("caseFold(%q, %q) = false, want true", c[0], c[1])
		}
	}
	// どちらも POSIX の形なら、動いている OS のファイルシステムに合わせる。
	if got, want := caseFold("/tmp/a", "/tmp/b"), runtime.GOOS == "windows"; got != want {
		t.Errorf("caseFold(POSIX, POSIX) = %v, want %v", got, want)
	}
}

// TestDirOutsideWorktreeWindows は、Windows の形のパスを指す `-C` / `--work-tree` / `--git-dir` が
// 「外」と判定されること。実在しないパスなので、どの OS で走らせても結果は同じ。
func TestDirOutsideWorktreeWindows(t *testing.T) {
	const proj = `C:\Users\x\proj`
	for _, c := range []struct {
		cwd, d string
		want   bool
		why    string
	}{
		{"/proj", `C:\Users\x\proj\.claude\worktrees\other`, true, "Windows の絶対パス（引用符で囲んだ形）"},
		{"/proj", `C:Usersxprojclaudeworktreesother`, true, "引用符で囲まず区切りを失った形"},
		{"/proj", `C:/Users/x/other`, true, "区切りが `/` の Windows の絶対パス"},
		{"/proj", "", false, "空（指定なし）"},

		// ほかの作業ツリー（deny する側）。書き方が違っても外だと分かること。
		{proj, `C:/Users/x/proj\.claude\worktrees\other`, true, "区切りが混在したほかの作業ツリー"},
		{proj, `C:\Users\x\proj\.claude\worktrees\other\`, true, "末尾に区切りが付いたほかの作業ツリー"},
		{proj, `\\localhost\share\proj\.claude\worktrees\other`, true, "UNC のほかの作業ツリー"},

		// 自分の作業ツリー（deny しない側＝偽陽性の検査）。ここが退行すると、Windows の利用者が
		// 自分の作業ツリーを触れなくなる（テストは緑のまま静かに悪くなる類）。
		{proj, `c:\users\x\proj`, false, "大文字小文字だけ違う自分の作業ツリー"},
		{proj, `C:/Users/X/PROJ/`, false, "大文字小文字・区切り・末尾の区切りだけ違う自分の作業ツリー"},
		// 上の 2 つは、直す前の実装でも macOS では false（＝同じ結果）になるため、macOS では退行を
		// 検出しない（直す前の実装は `filepath.IsAbs` が false になる `C:\…` を cwd の下へ継ぎ、
		// hookio.GitRoot が祖先の `.git` を拾って「同じ」と答えるため）。同じ主張を **macOS でも
		// 検出できる形**が次の 1 行（`/` で始まるので直す前の実装でも絶対パスとして扱われ、
		// `\` を含むので大文字小文字をそろえて比べる対象になる）。
		{`/no-such-root/proj\a`, `/no-such-root/PROJ\A`, false, "大文字小文字だけ違う同じ場所（絶対パス）"},
	} {
		if got := dirOutsideWorktree(c.cwd, c.d); got != c.want {
			t.Errorf("dirOutsideWorktree(%q, %q) = %v, want %v（%s）", c.cwd, c.d, got, c.want, c.why)
		}
	}
	if dirOutsideWorktree("", `C:\Users\x`) {
		t.Error(`dirOutsideWorktree("", …) は false のはず（cwd が分からないときは判定しない）`)
	}
}
