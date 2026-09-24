package kitinit

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/howashoji/looptrack/internal/client/cli"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/kit"
)

// isKitLink は以前の init（link）が置いた kit/loop への symlink か。
func isKitLink(fp string) bool {
	if !isLink(fp) {
		return false
	}
	t, err := os.Readlink(fp)
	return err == nil && strings.Contains(filepath.ToSlash(t), "kit/loop")
}

// insideReal は rel が途中の symlink を経由せずにプロジェクトの中にあるか（kit への symlink の先に書かない・消さない）。
func insideReal(p *Plan, rel string) bool {
	d, err := filepath.EvalSymlinks(filepath.Dir(p.path(rel)))
	if err != nil {
		return false
	}
	root, err := filepath.EvalSymlinks(p.Root)
	if err != nil {
		return false
	}
	return d == filepath.Join(root, filepath.FromSlash(pathDir(rel)))
}

func pathDir(rel string) string {
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		return rel[:i]
	}
	return "."
}

// prefixes は rel の .claude/ より下の途中のパス（.claude/rules/looptrack-loop/x.md → .claude/rules・.claude/rules/looptrack-loop）と rel 自身。
func prefixes(rel string) []string {
	parts := strings.Split(rel, "/")
	var out []string
	for i := 2; i <= len(parts); i++ {
		out = append(out, strings.Join(parts[:i], "/"))
	}
	return out
}

// writable は rel に実体を書けるか（途中に消さない symlink が無い）。
func writable(p *Plan, rel string) bool {
	ps := prefixes(rel)
	for _, x := range ps[:len(ps)-1] {
		if isLink(p.path(x)) && !p.removing(x) {
			return false
		}
	}
	return true
}

// existsAfter は、予定の削除の後に rel が通常のファイルとして残るか。
func existsAfter(p *Plan, rel string) bool {
	for _, x := range prefixes(rel) {
		if p.removing(x) {
			return false
		}
	}
	return isRegular(p.path(rel))
}

// placedAsIs は置いたファイルの本文 cur が init の置いたまま（kit の今の版・前回の版を導入の形にしたもの）か。
func placedAsIs(cur, kitBody, prevSum string) bool {
	s := sha(cur)
	return s == sha(kitBody) || (prevSum != "" && s == prevSum)
}

// removeIfUnchanged は init が置いたときのハッシュ（recorded）と同じなら消す。手で変えたもの・symlink は残して kept に足す。
func removeIfUnchanged(p *Plan, rel, recorded string, kept *[]string) {
	fp := p.path(rel)
	if !lexists(fp) || !insideReal(p, rel) {
		return
	}
	if isRegular(fp) {
		if cur, ok := readFile(fp); ok && recorded != "" && sha(cur) == recorded {
			p.Remove(rel)
			return
		}
	}
	*kept = append(*kept, rel)
}

// removeIfUnchangedAny は removeIfUnchanged の、記録のハッシュが複数ありうる形（skill の正本の位置には
// 日英のどちらの本文も置かれうる）。どれかと同じなら消す。
func removeIfUnchangedAny(p *Plan, rel string, recorded []string, kept *[]string) {
	fp := p.path(rel)
	if !lexists(fp) || !insideReal(p, rel) {
		return
	}
	if isRegular(fp) {
		if cur, ok := readFile(fp); ok {
			s := sha(cur)
			for _, r := range recorded {
				if r != "" && s == r {
					p.Remove(rel)
					return
				}
			}
		}
	}
	*kept = append(*kept, rel)
}

// isLoopSkill は name が kit/loop の skill のファイルか。
func isLoopSkill(name string) bool { return strings.HasPrefix(name, "kit/loop/skills/") }

// skillSums は skill の正本の位置に init が置きうる本文のハッシュ（kit の今の日英・前回の記録の日英）。
// 導入時の言語を切り替えて打ち直したとき、前の言語の本文を「手で変えたもの」と取り違えないため。
func skillSums(files, prevFiles map[string]string, name string) []string {
	tr := kit.TranslatedName(name)
	out := []string{prevFiles[name], prevFiles[tr]}
	for _, n := range []string{name, tr} {
		if b, ok := files[n]; ok {
			out = append(out, sha(b))
		}
	}
	return out
}

// planLoopFiles は kit/loop の rules・skill を実体で置く。
// kit へのディレクトリ単位の symlink は消して実体に置き換える。
// 実体は、前回置いたときのハッシュ（prevFiles）から変わっていれば手で変えたものとして上書きしない。
// rules は日英の両方を置く（hook が実行時に選ぶ）。skill は AI のハーネスが固定のパスで読むので、
// 導入時の言語の本文 1 本だけを正本の位置に置き、以前の init が置いた訳（en/）は片付ける。
func (in *installer) planLoopFiles(files, prevFiles map[string]string) {
	p := in.plan
	roots := append([]string{loopRulesDir}, loopSkillDirs(files)...)
	for _, r := range roots {
		if isKitLink(p.path(r)) {
			p.Remove(r)
		}
	}
	blocked := map[string]bool{}
	for _, name := range sortedKeys(files) {
		dest := loopDest(name)
		if dest == "" {
			continue
		}
		parts := strings.Split(dest, "/")
		top := strings.Join(parts[:3], "/")
		if strings.HasPrefix(dest, ".claude/skills/") {
			top = strings.Join(parts[:min(4, len(parts))], "/")
		}
		if !writable(p, dest) {
			if !blocked[top] {
				blocked[top] = true
				p.Note(i18n.T(p.Lang, "kitinit.loop.note.symlink", "file", top))
			}
			continue
		}
		skill := isLoopSkill(name)
		if skill && kit.IsTranslation(name) {
			continue // 訳は置かない（下で片付ける）
		}
		body := files[name]
		asIs := func(cur string) bool { return placedAsIs(cur, files[name], prevFiles[name]) }
		if skill {
			body, _ = pickLang(files, name, p.Lang)
			sums := skillSums(files, prevFiles, name)
			asIs = func(cur string) bool {
				s := sha(cur)
				for _, x := range sums {
					if x != "" && s == x {
						return true
					}
				}
				return false
			}
		}
		if !existsAfter(p, dest) {
			p.Text(dest, body)
			continue
		}
		cur, _ := readFile(p.path(dest))
		if asIs(cur) {
			p.Text(dest, body)
		} else {
			p.Note(i18n.T(p.Lang, "kitinit.loop.note.modified", "file", dest))
		}
	}
	// 以前の init が置いた skill の訳（.claude/skills/<名前>/en/…）。init の置いたままなら消し、手で変えたものは残す
	var keptTr []string
	for _, name := range sortedKeys(files) {
		if !isLoopSkill(name) || !kit.IsTranslation(name) {
			continue
		}
		if d := loopDest(name); d != "" {
			removeIfUnchangedAny(p, d, []string{prevFiles[name], sha(files[name])}, &keptTr)
		}
	}
	for _, k := range keptTr {
		p.Note(i18n.T(p.Lang, "kitinit.loop.note.kept_translation", "file", k))
	}
	var kept []string
	for _, name := range sortedKeys(prevFiles) {
		if _, ok := files[name]; ok {
			continue // 今も kit にあるもの
		}
		d := loopDest(name)
		if d == "" {
			continue
		}
		removeIfUnchanged(p, d, prevFiles[name], &kept)
	}
	for _, k := range kept {
		p.Note(i18n.T(p.Lang, "kitinit.loop.note.kept", "file", k))
	}
}

// removeLoop は --remove-loop。loop の配線・置いたファイルを外す。手で変えたファイル・core・手で置いた hook は残す。
func removeLoop(c *cli.Ctx, o *Options, target string) error {
	p := &Plan{Root: target, Lang: c.Lang}
	prev, err := readKitJSON(target)
	if err != nil {
		return err
	}
	lp := prev.Object("loop")
	dry := ""
	if o.DryRun {
		dry = "（dry-run）"
	}
	c.Printf("init --remove-loop: %s%s\n", target, dry)
	var kept []string
	dests := []string{loopRulesDir}
	if ents, err := os.ReadDir(p.path(".claude/skills")); err == nil {
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		for _, n := range names {
			dests = append(dests, ".claude/skills/"+n)
		}
	}
	for _, d := range dests {
		if isKitLink(p.path(d)) {
			p.Remove(d)
		}
	}
	files := strMap(lp.Object("files"))
	for _, name := range sortedKeys(files) {
		d := loopDest(name)
		switch {
		case d == "":
		case isLoopSkill(name) && !kit.IsTranslation(name):
			// 正本の位置には導入時の言語の本文が置かれている（日英のどちらの記録とも照らす）
			removeIfUnchangedAny(p, d, []string{files[name], files[kit.TranslatedName(name)]}, &kept)
		default:
			removeIfUnchanged(p, d, files[name], &kept)
		}
	}
	for _, rel := range []string{".claude/settings.json", ".claude/settings.local.json", ".codex/hooks.json", copilotHooks, legacyCopilotHooks} {
		if !isFile(p.path(rel)) {
			continue
		}
		data, err := loadJSON(p, rel)
		if err != nil {
			return err
		}
		before, existed := beforeOf(data)
		if h, ok := data.Get("hooks"); ok {
			if hooks, ok := h.(*jsonorder.Object); ok {
				if rel == copilotHooks || rel == legacyCopilotHooks {
					splitCombinedSessionStart(hooks) // 合成の SessionStart の summary は残す
					syncFlatLoop(hooks, nil)
				} else {
					syncGroupedLoop(hooks, nil)
				}
				if len(hooks.Keys()) == 0 {
					data.Delete("hooks")
				}
			}
		}
		putJSON(p, rel, data, before, existed)
	}
	upsertLoopBlock(p, "AGENTS.md", nil)
	if !p.Changed() && !truthy(lp, "installed") {
		c.Println(i18n.T(c.Lang, "kitinit.loop.not_installed"))
		return nil
	}
	if len(prev.Keys()) > 0 {
		s := stamp()
		next := prev.Clone()
		next.Set("loop", jsonorder.NewObject().Set("installed", false).Set("removed_at", s).Set("declined_at", s))
		p.Text(kitJSON, dumpJSON(next))
	}
	if _, _, err := finishPlan(c, p, o.DryRun, target); err != nil {
		return err
	}
	for _, k := range kept {
		c.Println(i18n.T(c.Lang, "kitinit.loop.kept_file", "file", k))
	}
	if !o.DryRun {
		c.Println(removeLoopNext(c.Lang, prev))
	}
	return nil
}

// removeLoopNext は --remove-loop の後の案内。残る core は導入した AI で違う:
// 鮮度ガードと skill /issue は Claude Code の導入だけに置くので、Codex・Copilot だけの導入では要約・計測の hook と AGENTS.md の節が残る。
func removeLoopNext(lang i18n.Lang, prev *jsonorder.Object) string {
	agents := map[string]bool{}
	if v, ok := prev.Get("agent"); ok {
		for _, a := range objList(v) {
			if s, ok := a.(string); ok {
				agents[s] = true
			}
		}
	}
	if agents["claude-code"] || len(agents) == 0 {
		return i18n.T(lang, "kitinit.loop.next.claude")
	}
	var names []string
	for _, a := range []struct{ id, name string }{{"codex", "Codex"}, {"copilot", "Copilot"}} {
		if agents[a.id] {
			names = append(names, a.name)
		}
	}
	if len(names) == 0 {
		return i18n.T(lang, "kitinit.loop.next.plain")
	}
	return i18n.T(lang, "kitinit.loop.next.agents", "agents", strings.Join(names, i18n.T(lang, "kitinit.list_sep")))
}
