package worktree

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/session"
	"github.com/howashoji/looptrack/internal/i18n"
)

// Main は looptrack worktree の入口。
func Main(args []string, stdout, stderr io.Writer, e env.Env) int {
	lang := i18n.FromEnv(e.Get)
	usage := i18n.T(lang, "worktree.usage")
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, usage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	sub := args[0]
	o := Options{}
	yes, sessionID := false, ""
	rest := args[1:]
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		val := func() (string, bool) {
			if i+1 < len(rest) {
				i++
				return rest[i], true
			}
			fmt.Fprintln(stderr, errLine(lang, i18n.T(lang, "worktree.err.need_value", "arg", a)))
			return "", false
		}
		var ok bool
		switch {
		case a == "--dir":
			if o.Dir, ok = val(); !ok {
				return 2
			}
		case strings.HasPrefix(a, "--dir="):
			o.Dir = strings.TrimPrefix(a, "--dir=")
		case a == "--base":
			if o.Base, ok = val(); !ok {
				return 2
			}
		case strings.HasPrefix(a, "--base="):
			o.Base = strings.TrimPrefix(a, "--base=")
		case a == "--min-age" || strings.HasPrefix(a, "--min-age="):
			v := strings.TrimPrefix(a, "--min-age=")
			if v == a {
				if v, ok = val(); !ok {
					return 2
				}
			}
			d, err := time.ParseDuration(v)
			if err != nil || d < 0 {
				fmt.Fprintln(stderr, errLine(lang, i18n.T(lang, "worktree.err.min_age", "value", v)))
				return 2
			}
			o.MinAge = d
		case a == "--session" || strings.HasPrefix(a, "--session="):
			v := strings.TrimPrefix(a, "--session=")
			if v == a {
				if v, ok = val(); !ok {
					return 2
				}
			}
			sessionID = v
		case a == "--yes" || a == "-y":
			yes = true
		case a == "-h" || a == "--help":
			fmt.Fprint(stdout, usage)
			return 0
		default:
			fmt.Fprintf(stderr, "%s\n%s", errLine(lang, i18n.T(lang, "worktree.err.unknown_arg", "arg", a)), usage)
			return 2
		}
	}

	switch sub {
	case "list":
		return list(lang, o, stdout, stderr)
	case "prune":
		return prune(lang, o, yes, stdout, stderr)
	case "mark":
		return mark(lang, o, sessionID, yes, e, stdout, stderr)
	}
	fmt.Fprintf(stderr, "%s\n%s", errLine(lang, i18n.T(lang, "worktree.err.unknown_sub", "sub", sub)), usage)
	return 2
}

// errLine は「エラー: …」の 1 行（接頭辞は cmd と共通）。
func errLine(lang i18n.Lang, msg string) string {
	return i18n.T(lang, "cmd.prefix.error", "msg", msg)
}

// sep は語を並べるときの区切り。
func sep(lang i18n.Lang) string {
	if lang == i18n.EN {
		return ", "
	}
	return "・"
}

func list(lang i18n.Lang, o Options, stdout, stderr io.Writer) int {
	rep, err := List(o)
	if err != nil {
		fmt.Fprintln(stderr, errLine(lang, i18n.Text(lang, err)))
		return 1
	}
	minAge := o.MinAge
	if minAge == 0 {
		minAge = DefaultMinAge
	}
	base := rep.Base
	if base == "" {
		base = i18n.T(lang, "worktree.list.base_missing")
	}
	fmt.Fprintf(stdout, "%s\n\n", i18n.T(lang, "worktree.list.base", "name", base))

	var can, keep []Entry
	for _, e := range rep.Entries {
		if e.Main {
			fmt.Fprintf(stdout, "%s\n\n", i18n.T(lang, "worktree.list.main", "path", e.Path, "branch", orDash(e.Branch)))
			continue
		}
		if ok, _ := e.Removable(lang, minAge); ok {
			can = append(can, e)
		} else {
			keep = append(keep, e)
		}
	}
	fmt.Fprintln(stdout, i18n.T(lang, "worktree.list.count", "n", len(can)+len(keep)))
	if len(can) > 0 {
		fmt.Fprintln(stdout, i18n.T(lang, "worktree.list.removable", "n", len(can)))
		for _, e := range can {
			fmt.Fprintf(stdout, "    %s\n", line(lang, e, minAge, rep.Root))
		}
	}
	if len(keep) > 0 {
		fmt.Fprintln(stdout, i18n.T(lang, "worktree.list.keep", "n", len(keep)))
		for _, e := range keep {
			fmt.Fprintf(stdout, "    %s\n", line(lang, e, minAge, rep.Root))
		}
	}
	printBranches(lang, stdout, rep)
	if len(can) > 0 || countMergedBranches(lang, rep) > 0 {
		fmt.Fprintf(stdout, "\n%s\n", i18n.T(lang, "worktree.list.how"))
	}
	return 0
}

func prune(lang i18n.Lang, o Options, yes bool, stdout, stderr io.Writer) int {
	o.fill()
	rep, err := List(o)
	if err != nil {
		fmt.Fprintln(stderr, errLine(lang, i18n.Text(lang, err)))
		return 1
	}
	var can, keep []Entry
	for _, e := range rep.Entries {
		if e.Main {
			continue
		}
		if ok, _ := e.Removable(lang, o.MinAge); ok {
			can = append(can, e)
		} else {
			keep = append(keep, e)
		}
	}
	var brs []Branch
	for _, b := range rep.Branches {
		if ok, _ := b.Removable(lang); ok {
			brs = append(brs, b)
		}
	}
	if len(can) == 0 && len(brs) == 0 {
		fmt.Fprintln(stdout, i18n.T(lang, "worktree.prune.nothing"))
		printKeep(lang, stdout, keep, o.MinAge, rep.Root)
		return 0
	}
	if !yes {
		fmt.Fprintln(stdout, i18n.T(lang, "worktree.prune.dryrun"))
		if len(can) > 0 {
			fmt.Fprintln(stdout, i18n.T(lang, "worktree.prune.worktrees", "n", len(can)))
			for _, e := range can {
				fmt.Fprintf(stdout, "    %s\n", line(lang, e, o.MinAge, rep.Root))
			}
		}
		if len(brs) > 0 {
			fmt.Fprintln(stdout, i18n.T(lang, "worktree.prune.branches", "n", len(brs)))
			for _, b := range brs {
				fmt.Fprintf(stdout, "    %s\n", i18n.T(lang, "worktree.prune.branch_item", "name", b.Name, "when", ago(lang, b.Age)))
			}
		}
		printKeep(lang, stdout, keep, o.MinAge, rep.Root)
		fmt.Fprintf(stdout, "\n%s\n", i18n.T(lang, "worktree.prune.warn"))
		return 0
	}

	root := repoRoot(o, rep)
	removed, failed := 0, 0
	for _, e := range can {
		args := []string{"worktree", "remove", e.Path}
		if e.Missing {
			args = []string{"worktree", "prune"}
		}
		if _, err := o.Git(root, args...); err != nil {
			fmt.Fprintln(stderr, i18n.T(lang, "worktree.prune.fail", "path", e.Path, "reason", err))
			failed++
			continue
		}
		fmt.Fprintln(stdout, i18n.T(lang, "worktree.prune.removed", "path", e.Path))
		removed++
		if e.Branch != "" && e.Merged {
			if _, err := o.Git(root, "branch", "-d", e.Branch); err == nil {
				fmt.Fprintln(stdout, i18n.T(lang, "worktree.prune.removed_branch", "name", e.Branch))
			}
		}
	}
	for _, b := range brs {
		if _, err := o.Git(root, "branch", "-d", b.Name); err != nil {
			fmt.Fprintln(stderr, i18n.T(lang, "worktree.prune.fail_branch", "name", b.Name, "reason", err))
			failed++
			continue
		}
		fmt.Fprintln(stdout, i18n.T(lang, "worktree.prune.removed_branch", "name", b.Name))
		removed++
	}
	fmt.Fprintln(stdout, i18n.T(lang, "worktree.prune.done", "n", removed, "failed", failed))
	printKeep(lang, stdout, keep, o.MinAge, rep.Root)
	if failed > 0 {
		return 1
	}
	return 0
}

func mark(lang i18n.Lang, o Options, sessionID string, force bool, e env.Env, stdout, stderr io.Writer) int {
	o.fill()
	if sessionID == "" {
		sessionID = session.Detect(e).Session
	}
	p, err := Mark(o, sessionID, force)
	if err != nil {
		fmt.Fprintln(stderr, errLine(lang, i18n.Text(lang, err)))
		return 1
	}
	if p == "" {
		fmt.Fprintln(stdout, i18n.T(lang, "worktree.mark.main"))
		return 0
	}
	fmt.Fprintln(stdout, i18n.T(lang, "worktree.mark.done", "path", p, "session", orDash(sessionID)))
	return 0
}

func printKeep(lang i18n.Lang, w io.Writer, keep []Entry, minAge time.Duration, root string) {
	if len(keep) == 0 {
		return
	}
	fmt.Fprintln(w, i18n.T(lang, "worktree.list.keep", "n", len(keep)))
	for _, e := range keep {
		fmt.Fprintf(w, "    %s\n", line(lang, e, minAge, root))
	}
}

func printBranches(lang i18n.Lang, w io.Writer, rep *Report) {
	if len(rep.Branches) == 0 {
		return
	}
	var merged, open, pub []Branch
	for _, b := range rep.Branches {
		switch {
		case b.Published:
			pub = append(pub, b)
		case b.Merged:
			merged = append(merged, b)
		default:
			open = append(open, b)
		}
	}
	fmt.Fprintf(w, "\n%s\n", i18n.T(lang, "worktree.branches.count", "n", len(rep.Branches)))
	if len(merged) > 0 {
		fmt.Fprintln(w, i18n.T(lang, "worktree.branches.merged", "n", len(merged), "names", names(lang, merged)))
	}
	if len(open) > 0 {
		fmt.Fprintln(w, i18n.T(lang, "worktree.branches.open", "n", len(open), "names", names(lang, open)))
	}
	if len(pub) > 0 {
		fmt.Fprintln(w, i18n.T(lang, "worktree.branches.published", "n", len(pub), "names", names(lang, pub)))
	}
}

func countMergedBranches(lang i18n.Lang, rep *Report) int {
	n := 0
	for _, b := range rep.Branches {
		if ok, _ := b.Removable(lang); ok {
			n++
		}
	}
	return n
}

func names(lang i18n.Lang, bs []Branch) string {
	out := make([]string, 0, len(bs))
	for _, b := range bs {
		out = append(out, b.Name)
	}
	if len(out) > 12 {
		return strings.Join(out[:12], " ") + i18n.T(lang, "worktree.names.more", "n", len(out)-12)
	}
	return strings.Join(out, " ")
}

// line は一覧の 1 行（パス・ブランチ・状態・最後に動いていた時刻）。
func line(lang i18n.Lang, e Entry, minAge time.Duration, root string) string {
	var st []string
	if ok, why := e.Removable(lang, minAge); !ok {
		st = append(st, why)
	}
	if e.Missing {
		st = append(st, i18n.T(lang, "worktree.state.missing"))
	}
	if e.JunkOnly {
		st = append(st, i18n.T(lang, "worktree.state.junk", "n", e.Changes))
	}
	if e.Losing() {
		st = append(st, i18n.T(lang, "worktree.state.losing"))
	}
	if len(st) == 0 {
		st = append(st, i18n.T(lang, "worktree.state.clean"))
	}
	when := ago(lang, e.Age)
	if e.TouchFrom != "" && e.Session != "" {
		when += i18n.T(lang, "worktree.line.session", "id", short(e.Session))
	}
	return fmt.Sprintf("%-44s %-24s %s", shortPath(e.Path, root), orDash(e.Branch),
		i18n.T(lang, "worktree.line.state", "state", strings.Join(st, sep(lang)), "when", when))
}

// shortPath は本体の作業ツリーからの相対パス（外にあるものは絶対のまま。長ければ末尾を残して詰める）。
func shortPath(p, root string) string {
	if root != "" {
		if rel, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(rel, "..") {
			p = rel
		}
	}
	if len(p) <= 44 {
		return p
	}
	return "…" + p[len(p)-43:]
}

func short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// ago は「いつ動いていたか」を人の言葉にする。
func ago(lang i18n.Lang, d time.Duration) string {
	switch {
	case d <= 0:
		return i18n.T(lang, "worktree.ago.unknown")
	case d < time.Minute:
		return i18n.T(lang, "worktree.ago.now")
	case d < time.Hour:
		return i18n.T(lang, "worktree.ago.minutes", "n", int(d.Minutes()))
	case d < 24*time.Hour:
		return i18n.T(lang, "worktree.ago.hours", "n", int(d.Hours()))
	default:
		return i18n.T(lang, "worktree.ago.days", "n", int(d.Hours()/24))
	}
}

// repoRoot は git を実行する場所（本体の作業ツリー）。
func repoRoot(o Options, rep *Report) string {
	for _, e := range rep.Entries {
		if e.Main {
			return e.Path
		}
	}
	if o.Dir != "" {
		return filepath.Clean(o.Dir)
	}
	return ""
}
