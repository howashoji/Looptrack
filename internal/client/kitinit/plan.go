package kitinit

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/howashoji/looptrack/internal/client/cli"
	"github.com/howashoji/looptrack/internal/i18n"
)

// Plan は init が行う変更の一覧。Apply で書き、dry-run では Show で差分だけ出す。
// Go 版は symlink を作らない（kit の md は copy で置く。Windows 対応）。以前の init が置いた symlink は
// 消すか（removes）、実体のファイルで置き換える（files の link）。
type Plan struct {
	Root    string
	Lang    i18n.Lang // 変更の一覧・注意を出す言語（呼び出しの経路から渡す）
	files   []change
	removes []string
	Notes   []string
}

type change struct {
	rel    string
	before *string // 変更前の本文（無い・symlink なら nil）
	link   string  // 変更前が symlink ならリンク先（実体で置き換える）
	after  string
}

func (p *Plan) path(rel string) string { return filepath.Join(p.Root, filepath.FromSlash(rel)) }

// Text は rel を after にする予定を足す（同じファイルを 2 度書くときは後の内容）。
func (p *Plan) Text(rel, after string) {
	for i := range p.files {
		if p.files[i].rel == rel {
			p.files[i].after = after
			return
		}
	}
	c := change{rel: rel, after: after}
	fp := p.path(rel)
	if p.viaLink(rel) {
		// 途中が symlink（以前の init の kit への symlink。消してから実体を置く）: 今の中身は kit の実体なので「無い」とみなす
		p.files = append(p.files, c)
		return
	}
	if fi, err := os.Lstat(fp); err == nil {
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			c.link, _ = os.Readlink(fp)
		case fi.Mode().IsRegular():
			if b, err := os.ReadFile(fp); err == nil {
				s := string(b)
				c.before = &s
			}
		}
	}
	p.files = append(p.files, c)
}

// Current は書く予定の内容（無ければ今のファイル、それも無ければ ""）。
func (p *Plan) Current(rel string) string {
	for _, c := range p.files {
		if c.rel == rel {
			return c.after
		}
	}
	if s, ok := readFile(p.path(rel)); ok {
		return s
	}
	return ""
}

// Remove は rel（ファイル・symlink）を消す予定を足す。
func (p *Plan) Remove(rel string) {
	if !lexists(p.path(rel)) {
		return
	}
	for _, r := range p.removes {
		if r == rel {
			return
		}
	}
	p.removes = append(p.removes, rel)
}

// viaLink は rel の .claude/ より下の途中（rel 自身は除く）に symlink があるか。
func (p *Plan) viaLink(rel string) bool {
	parts := strings.Split(rel, "/")
	for i := 2; i < len(parts); i++ {
		if isLink(p.path(strings.Join(parts[:i], "/"))) {
			return true
		}
	}
	return false
}

func (p *Plan) removing(rel string) bool {
	for _, r := range p.removes {
		if r == rel {
			return true
		}
	}
	return false
}

// Note は利用者への案内を足す（文面は呼ぶ側が i18n.T で作る）。
func (p *Plan) Note(text string) { p.Notes = append(p.Notes, text) }

func (c change) changed() bool {
	return c.link != "" || c.before == nil || *c.before != c.after
}

// Changed は変更があるか。
func (p *Plan) Changed() bool {
	if len(p.removes) > 0 {
		return true
	}
	for _, c := range p.files {
		if c.changed() {
			return true
		}
	}
	return false
}

// Show は変更の一覧（dry-run は差分も）を出す（以前の CLI（1.0.0 より前）と同じ形）。
func (p *Plan) Show(w io.Writer, dryRun bool) bool {
	for _, r := range p.removes {
		fmt.Fprintf(w, "  %s %s\n", pad(i18n.T(p.Lang, "kitinit.plan.state.delete"), 10), r)
	}
	for _, c := range p.files {
		state := i18n.T(p.Lang, "kitinit.plan.state.same")
		switch {
		case c.link != "":
			state = i18n.T(p.Lang, "kitinit.plan.state.replace", "link", c.link)
		case c.before == nil:
			state = i18n.T(p.Lang, "kitinit.plan.state.create")
		case *c.before != c.after:
			state = i18n.T(p.Lang, "kitinit.plan.state.update")
		}
		fmt.Fprintf(w, "  %s %s\n", pad(state, 10), c.rel)
		if dryRun && c.changed() {
			from, before := "/dev/null", ""
			if c.before != nil {
				from, before = "a/"+c.rel, *c.before
			}
			for _, d := range cli.UnifiedDiffLines(cli.SplitLinesKeep(before), cli.SplitLinesKeep(c.after), from, "b/"+c.rel) {
				if !strings.HasSuffix(d, "\n") {
					d += "\n"
				}
				io.WriteString(w, "      "+d)
			}
		}
	}
	return p.Changed()
}

// pad は右に空白を足して n 文字にそろえる（表示幅ではなく文字数で数える）。
func pad(s string, n int) string {
	if k := len([]rune(s)); k < n {
		return s + strings.Repeat(" ", n-k)
	}
	return s
}

// ---------------------------------------------------------------- 書き込みと巻き戻し

// entry は書き込みの前の状態（巻き戻し用）。
type entry struct {
	rel    string
	exists bool
	link   string
	data   []byte
	mode   os.FileMode
}

// Applied は Apply が行った変更の記録（Rollback で元に戻す）。
type Applied struct {
	root      string
	journal   []entry
	madeDirs  []string // Apply が作ったディレクトリ（巻き戻しで空なら消す）
	BackupDir string   // 変更前のファイルの控え（無ければ ""）
	madeBase  bool     // .looptrack-init-backup を Apply が作った
}

func (a *Applied) snapshot(rel string) {
	for _, e := range a.journal {
		if e.rel == rel {
			return
		}
	}
	fp := filepath.Join(a.root, filepath.FromSlash(rel))
	e := entry{rel: rel}
	if fi, err := os.Lstat(fp); err == nil {
		e.exists = true
		e.mode = fi.Mode().Perm()
		if fi.Mode()&os.ModeSymlink != 0 {
			e.link, _ = os.Readlink(fp)
		} else {
			e.data, _ = os.ReadFile(fp)
		}
	}
	a.journal = append(a.journal, e)
}

func (a *Applied) mkdirs(dir string) error {
	var missing []string
	for d := dir; d != a.root && strings.HasPrefix(d, a.root); d = filepath.Dir(d) {
		if _, err := os.Lstat(d); err == nil {
			break
		}
		missing = append(missing, d)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	a.madeDirs = append(a.madeDirs, missing...)
	return nil
}

// Apply は変更を書く。変更するファイルは backupDir に控える。
// 順は、消すもの（以前の init の symlink を含む）→ 書くもの。symlink の先には書かない（実体で置き換える）。
func (p *Plan) Apply(backupDir string) (*Applied, error) {
	a := &Applied{root: p.Root}
	backed := false
	backup := func(rel string) error {
		src := p.path(rel)
		fi, err := os.Lstat(src)
		if err != nil {
			return nil
		}
		dst := filepath.Join(backupDir, filepath.FromSlash(rel))
		if !backed {
			base := filepath.Dir(backupDir)
			if _, err := os.Lstat(base); err != nil {
				a.madeBase = true
			}
			if err := os.MkdirAll(base, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(base, ".gitignore"), []byte("*\n"), 0o644); err != nil {
				return err
			}
			backed = true
			a.BackupDir = backupDir
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			t, _ := os.Readlink(src)
			if os.Symlink(t, dst) != nil { // symlink を作れない環境（Windows の権限）はリンク先を文字で控える
				return os.WriteFile(dst+".symlink", []byte(t+"\n"), 0o644)
			}
			return nil
		}
		if fi.IsDir() {
			return nil
		}
		b, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	}
	stop := filepath.Join(p.Root, ".claude")
	for _, rel := range p.removes {
		fp := p.path(rel)
		if err := backup(rel); err != nil {
			return a, err
		}
		a.snapshot(rel)
		if err := os.Remove(fp); err != nil {
			return a, err
		}
		// 空になったディレクトリを .claude/ の手前まで消す
		for d := filepath.Dir(fp); strings.HasPrefix(d, stop+string(filepath.Separator)); d = filepath.Dir(d) {
			fi, err := os.Lstat(d)
			if err != nil || !fi.IsDir() {
				break
			}
			ents, err := os.ReadDir(d)
			if err != nil || len(ents) > 0 {
				break
			}
			if os.Remove(d) != nil {
				break
			}
		}
	}
	for _, c := range p.files {
		if !c.changed() {
			continue
		}
		fp := p.path(c.rel)
		if c.before != nil || c.link != "" {
			if err := backup(c.rel); err != nil {
				return a, err
			}
		}
		a.snapshot(c.rel)
		if err := a.mkdirs(filepath.Dir(fp)); err != nil {
			return a, err
		}
		mode := os.FileMode(0o644)
		if fi, err := os.Lstat(fp); err == nil && fi.Mode().IsRegular() {
			mode = fi.Mode().Perm()
		}
		tmp := fp + ".im-init.tmp"
		if err := os.WriteFile(tmp, []byte(c.after), mode); err != nil {
			return a, err
		}
		os.Chmod(tmp, mode)
		if fi, err := os.Lstat(fp); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			os.Remove(fp) // symlink は消してから置く（リンク先には書かない）
		}
		if err := os.Rename(tmp, fp); err != nil {
			os.Remove(tmp)
			return a, err
		}
	}
	return a, nil
}

// Rollback は Apply の前の状態に戻す（書いたもの・消したものを戻し、作ったディレクトリと控えを消す）。
func (a *Applied) Rollback() error {
	var firstErr error
	keep := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	// 1. 書いたもの・置き換えたものを消す → 2. 作ったディレクトリ（空のもの）を消す → 3. 元の状態を置き直す
	// （以前の symlink のディレクトリを実体のディレクトリで置き換えたときも、先に実体を消してから symlink を戻せる）
	for i := len(a.journal) - 1; i >= 0; i-- {
		fp := filepath.Join(a.root, filepath.FromSlash(a.journal[i].rel))
		if fi, err := os.Lstat(fp); err == nil && !fi.IsDir() {
			keep(os.Remove(fp))
		}
	}
	dirs := append([]string(nil), a.madeDirs...)
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, d := range dirs {
		if ents, err := os.ReadDir(d); err == nil && len(ents) == 0 {
			os.Remove(d)
		}
	}
	for _, e := range a.journal {
		if !e.exists {
			continue
		}
		fp := filepath.Join(a.root, filepath.FromSlash(e.rel))
		keep(os.MkdirAll(filepath.Dir(fp), 0o755))
		if e.link != "" {
			keep(os.Symlink(e.link, fp))
			continue
		}
		keep(os.WriteFile(fp, e.data, e.mode))
		keep(os.Chmod(fp, e.mode))
	}
	if a.BackupDir != "" {
		keep(os.RemoveAll(a.BackupDir))
		base := filepath.Dir(a.BackupDir)
		if a.madeBase {
			keep(os.RemoveAll(base))
		}
	}
	return firstErr
}

// ---------------------------------------------------------------- ファイルの小道具

func lexists(p string) bool { _, err := os.Lstat(p); return err == nil }

func isLink(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

func isRegular(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode().IsRegular()
}

// isFile は symlink を辿って通常のファイルか。
func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func readFile(p string) (string, bool) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	return string(b), true
}
