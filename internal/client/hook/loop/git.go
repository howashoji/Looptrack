package loop

import (
	"os"
	"path/filepath"
	"strings"
)

// gitDirOf は d から上へたどって最初に見つかる .git（ディレクトリ、または worktree・submodule の .git ファイル）の
// 作業ツリーのルートと git のディレクトリを返す（無ければ ""）。git を起動しない。
func gitDirOf(d string) (top, gitDir string) {
	d = absClean(d)
	for {
		p := filepath.Join(d, ".git")
		if st, err := os.Stat(p); err == nil {
			if st.IsDir() {
				return d, p
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return "", ""
			}
			line := strings.TrimSpace(strings.SplitN(string(b), "\n", 2)[0])
			rest, ok := strings.CutPrefix(line, "gitdir:")
			if !ok {
				return "", ""
			}
			g := strings.TrimSpace(rest)
			if !filepath.IsAbs(g) {
				g = filepath.Join(d, g)
			}
			return d, filepath.Clean(g)
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", ""
		}
		d = parent
	}
}

// gitCommonDir は `git -C d rev-parse --git-common-dir` を実パスにしたもの（worktree でも本体と同じ値。git でなければ ""）。
// d が無ければ存在する親から探す（bash 版と同じ）。
func gitCommonDir(d string) string {
	d = existingDir(d)
	if d == "" {
		return ""
	}
	_, g := gitDirOf(d)
	if g == "" {
		return ""
	}
	// worktree の git のディレクトリ（.git/worktrees/<名前>）は commondir に本体への相対パスを持つ
	if b, err := os.ReadFile(filepath.Join(g, "commondir")); err == nil {
		c := strings.TrimSpace(string(b))
		if !filepath.IsAbs(c) {
			c = filepath.Join(g, c)
		}
		g = c
	}
	if !isDir(g) {
		return ""
	}
	return realpath(g)
}

// gitTop は `git -C d rev-parse --show-toplevel`（git でなければ ""）。
func gitTop(d string) string {
	d = existingDir(d)
	if d == "" {
		return ""
	}
	top, _ := gitDirOf(d)
	return top
}

// existingDir は d か、存在する最も近い親のディレクトリ（無ければ ""）。
func existingDir(d string) string {
	if d == "" {
		return ""
	}
	d = absClean(d)
	for !isDir(d) {
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
	return d
}

// mainWorktreeRoot は d を含むリポジトリの**本体（メイン）の作業ツリー**のルート。
// git worktree add で作った作業ツリーの中から呼んでも本体を返す（gitCommonDir の親）。
// git でない・本体のルートが決められない（bare・--separate-git-dir）ときは "" を返し、呼び出し側は今までの値に落とす。
//
// 記憶・引き継ぎの置き場をこれで解決する。作業ツリーの側で解決すると、
// 書いた引き継ぎは worktree prune で消え、読む側は .gitignore された symlink が無いために何も読めない。
func mainWorktreeRoot(d string) string {
	g := gitCommonDir(d)
	if g == "" {
		return ""
	}
	if filepath.Base(g) != ".git" {
		return "" // bare・--separate-git-dir。親が作業ツリーとは限らない
	}
	top := filepath.Dir(g)
	if !isDir(top) {
		return ""
	}
	return top
}
