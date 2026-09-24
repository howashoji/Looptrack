package loop

// 本体以外の作業ツリーに取り残された引き継ぎの検知（引き継ぎ鮮度ガードに添える知らせ）。
//
// 引き継ぎのパスを本体で解決するようにしても（memoriesRoot）、**それ以前に取り残されたもの**は残る。
// 取り残しは git status の「??」1 行にまとまり、go test にも public-scan にもかからない。
// public-scan にかからないのは「未追跡だから」ではない（未追跡も調べる）。取り残しはほかの作業ツリーの中にあり、
// その置き場（.claude/worktrees/）は .gitignore で無視されるので --exclude-standard で外れる。
// 作業ツリーを本体の外に置けば元々走査の対象外で、その中で回しても private/ は export-ignore で外れる。
// どの検査にもかからないまま looptrack worktree prune で作業ツリーごと消えるので、
// 消える前に気づけるように、Stop で 1 回だけ知らせる（止めない）。

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/howashoji/looptrack/internal/hookio"
	"github.com/howashoji/looptrack/internal/i18n"
)

// strayHandoffs は、本体以外の作業ツリーに取り残された引き継ぎ（<本体>/.git/worktrees/<名前>/gitdir から場所を読む。git は起動しない）。
//
// 引き継ぎのパスを本体で解決するようにしても（memoriesRoot）、**以前に取り残されたもの**は残る。
// 取り残しはほかの作業ツリーの中にあり、その置き場は .gitignore で無視されるので公開物の検査にもかからず、
// looptrack worktree prune で作業ツリーごと消える（詳しい理由はファイル冒頭）。
func strayHandoffs(ev hookio.Event, e *Env) []string {
	r := root(ev, e)
	main := mainWorktreeRoot(r)
	if main == "" {
		return nil
	}
	hf := handoffFile(ev, e)
	rel := relpath(hf, main)
	if rel == "" || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return nil // 引き継ぎを本体の外に置いている。作業ツリーごとの取り残しは起きない
	}
	wts := filepath.Join(gitCommonDir(r), "worktrees")
	ents, err := os.ReadDir(wts)
	if err != nil {
		return nil
	}
	mainReal := realpath(hf)
	var out []string
	for _, ent := range ents {
		b, err := os.ReadFile(filepath.Join(wts, ent.Name(), "gitdir"))
		if err != nil {
			continue
		}
		g := trimSpace(string(b))
		if g == "" {
			continue
		}
		p := filepath.Join(filepath.Dir(g), rel)
		if !isFile(p) || realpath(p) == mainReal {
			continue // 消えた作業ツリー・symlink で本体を指しているもの
		}
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// strayHandoffNotice は取り残しの知らせ（無ければ ""）。同じ顔ぶれでは 1 回だけ出す（毎回出ると読まれなくなる）。
func strayHandoffNotice(ev hookio.Event, e *Env) string {
	seenPath := filepath.Join(stateDir(ev, e), ".looptrack-freshness", "stray-handoff-seen")
	list := strayHandoffs(ev, e)
	if len(list) == 0 {
		_ = os.Remove(seenPath)
		return ""
	}
	key := strings.Join(list, "\n")
	if b, err := os.ReadFile(seenPath); err == nil && string(b) == key {
		return ""
	}
	if os.MkdirAll(filepath.Dir(seenPath), 0o777) == nil {
		_ = os.WriteFile(seenPath, []byte(key), 0o666)
	}
	shown := list
	if len(shown) > strayListMax {
		shown = shown[:strayListMax]
	}
	items := make([]string, 0, len(shown)+1)
	for _, p := range shown {
		items = append(items, "  - "+filepath.ToSlash(p))
	}
	if len(list) > len(shown) {
		items = append(items, "  - …")
	}
	return i18n.T(e.lang(), "loop.handoff.stray.notice",
		"n", len(list), "list", strings.Join(items, "\n"), "main", filepath.ToSlash(handoffFile(ev, e)))
}

// strayListMax は知らせに並べる取り残しの数の上限。
const strayListMax = 5
