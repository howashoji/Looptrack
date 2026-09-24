package kitinit

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/howashoji/looptrack/internal/client/hook/core"
	"github.com/howashoji/looptrack/internal/client/hook/loop"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/hookio"
)

// hook の配線。
//
// 新しい形: `looptrack hook <名前> --agent <AI>`（core・loop とも。DESIGN.md §5-11 の Q2）。
//   - プロジェクトのルートはコマンドに書かない。looptrack hook が CLAUDE_PROJECT_DIR → 入力の cwd の git のルート → cwd の順に
//     決める（hookio。以前の配線の ${CLAUDE_PROJECT_DIR:-$(git rev-parse …)} と同じ順）ので、Copilot が .claude/settings.json を
//     CLAUDE_PROJECT_DIR なしで起動しても失敗しない。Claude Code 以外からの起動は --agent claude-code の hook が何もせず終わる（hookio.ForeignHost）。
//   - looptrack は PATH の名前で指す。PATH に無い端末では実行ファイルの絶対パスで配線し、Claude Code は手元専用の
//     .claude/settings.local.json に書く（共有の設定に利用者ごとのパスを入れない）。
//   - Codex・Copilot は設定に env が無いので LOOPTRACK_API_URL / LOOPTRACK_PROJECT を前置する（以前の CLI と同じ）。
//
// 以前の CLI（1.0.0 より前）の init が書いた配線を新しい形に置き換える処理は、その撤去で
// 消した（既存のプロジェクトは移し終えた）。残っている以前の形の配線は手で変えた配線と同じに扱い、
// 同じ hook を重ねて足さず、置き換えなかった旨を知らせる。プロジェクトが自分で置いた hook は残す。

// wiring は 1 つの設定ファイル（1 つの AI）の配線の作り方。
type wiring struct {
	agent string // claude-code / codex / copilot
	bin   string // looptrack（PATH）か引用した絶対パス
	psBin string // PowerShell での呼び方（Copilot の powershell / windows）
	url   string
	slug  string
	env   bool // LOOPTRACK_API_URL / LOOPTRACK_PROJECT を前置する（Codex・Copilot）
}

func (w wiring) prefix() string {
	if !w.env {
		return ""
	}
	return "LOOPTRACK_API_URL=" + w.url + " LOOPTRACK_PROJECT=" + w.slug + " "
}

// hook は `looptrack hook <名前> --agent <AI><extra>`（core は env を前置する）。
func (w wiring) hook(name, extra string, withEnv bool) string {
	p := ""
	if withEnv {
		p = w.prefix()
	}
	return p + w.bin + " hook " + name + " --agent " + w.agent + extra
}

// hookPS は Copilot の Windows（PowerShell）の同じ配線。
func (w wiring) hookPS(name, extra string, withEnv bool) string {
	p := ""
	if withEnv && w.env {
		p = "$env:LOOPTRACK_API_URL='" + w.url + "'; $env:LOOPTRACK_PROJECT='" + w.slug + "'; "
	}
	return p + w.psBin + " hook " + name + " --agent " + w.agent + extra
}

// coreWants は core の配線（以前の CLI（1.0.0 より前）と同じイベント・matcher・timeout）。
func coreWants(w wiring, o *Options) []want {
	var out []want
	add := func(ev, matcher, name, extra string, timeout int) {
		out = append(out, want{Event: ev, Matcher: matcher, ID: name, Cmd: w.hook(name, extra, true), PS: w.hookPS(name, extra, true), Timeout: timeout})
	}
	if !o.NoSummary {
		add("SessionStart", "", "summary", "", 10)
	}
	switch w.agent {
	case "claude-code":
		if !o.NoFreshness {
			add("UserPromptSubmit", "", "issue-freshness-mark", "", 5)
			add("PostToolUse", "Bash|Read|Edit|Write|NotebookEdit", "issue-freshness-mark", "", 5)
			add("Stop", "", "issue-freshness-check", "", 10)
		}
		if !o.NoUsage {
			add("PostToolUse", "mcp__.*", "usage", "", 10)
			add("Stop", "", "usage", "", 10)
			add("SessionEnd", "", "usage", "", 10)
		}
	case "codex":
		if !o.NoUsage {
			add("PostToolUse", codexMCPMatcher, "usage", "", 10)
			add("Stop", "", "usage", "", 10)
			add("SessionEnd", "", "usage", "", 10)
		}
	case "copilot":
		// matcher は付けない（VS Code は無視し、CLI の MCP のツール名は <サーバ>-<ツール>。hook が中で絞る）。SessionEnd は CLI だけ
		if !o.NoUsage {
			for _, ev := range []string{"PostToolUse", "Stop", "SessionEnd"} {
				add(ev, "", "usage", " --event "+ev, 10)
			}
		}
	}
	return out
}

// ---------------------------------------------------------------- 配線の見分け

const (
	kindOther = iota
	kindNew   // looptrack hook の形
)

type wired struct {
	kind int
	id   string // hook の名前（summary・issue-freshness-mark・usage・loop の名前）
	loop bool
}

var newCmd = regexp.MustCompile(`^(?:LOOPTRACK_API_URL=\S+ LOOPTRACK_PROJECT=\S+ )?(looptrack|"[^"]*looptrack[^"/\\]*"|'[^']*looptrack[^'/\\]*') hook ([a-z0-9-]+) --agent ([a-z-]+)(.*)$`)

func classify(cmd string) wired {
	if m := newCmd.FindStringSubmatch(cmd); m != nil {
		name := m[2]
		if _, ok := core.Lookup(name); ok {
			return wired{kind: kindNew, id: name}
		}
		if _, ok := loop.Lookup(name); ok {
			return wired{kind: kindNew, id: name, loop: true}
		}
		return wired{kind: kindNew, id: name, loop: !strings.HasPrefix(name, "issue-") && name != "summary" && name != "usage"}
	}
	return wired{}
}

// ---------------------------------------------------------------- JSON の小道具

func objList(v any) []any {
	l, _ := v.([]any)
	return l
}

func asObj(v any) *jsonorder.Object {
	o, _ := v.(*jsonorder.Object)
	return o
}

func cmdOf(h *jsonorder.Object) string {
	if h == nil {
		return ""
	}
	return h.String("command")
}

func matcherOf(o *jsonorder.Object) string {
	if o == nil {
		return ""
	}
	return o.String("matcher")
}

func num(n int) jsonorder.Number { return jsonorder.Number(strconv.Itoa(n)) }

// ---------------------------------------------------------------- Claude Code・Codex（matcher ごとのグループ）

// groupedSync は Claude Code・Codex の hooks（{イベント: [{matcher?, hooks: [{type, command, timeout}]}]}）を直す。
//   - core の足りない配線を足す（同じイベントに同じ hook があれば足さない＝手で変えた配線を尊重）
//   - loop（loopWant が nil でなければ）: 以前の形・新しい形の loop の配線を loopWant にそろえ、足りないものを後ろに足す
func groupedSync(hooks *jsonorder.Object, w wiring, coreWant []want, loopWant []want, touchLoop bool) {
	// 2. core の足りない配線を足す
	for _, d := range coreWant {
		addGrouped(hooks, d, false)
	}
	// 3. loop
	if touchLoop {
		syncGroupedLoop(hooks, loopWant)
	}
}

// hasID は hooks[ev] に id の hook（新しい形・id を部分に含む合成の配線）があるか。
func hasID(entries []*jsonorder.Object, id string) bool {
	for _, h := range entries {
		cmd := cmdOf(h)
		if c := classify(cmd); c.kind != kindOther && c.id == id {
			return true
		}
		if loop.IsCombined(classify(cmd).id) && has(combinedParts(cmd), id) {
			return true
		}
	}
	return false
}

func groupedEntries(hooks *jsonorder.Object, ev string) []*jsonorder.Object {
	v, _ := hooks.Get(ev)
	var out []*jsonorder.Object
	for _, g := range objList(v) {
		hv, _ := asObj(g).Get("hooks")
		for _, h := range objList(hv) {
			if o := asObj(h); o != nil {
				out = append(out, o)
			}
		}
	}
	return out
}

// addGrouped は d を足す（同じイベントに同じ hook があれば足さない）。last なら同じ matcher の最後のグループ、
// でなければ最初のグループに足す（以前の CLI（1.0.0 より前）の loop と core の違いをそのまま）。
func addGrouped(hooks *jsonorder.Object, d want, last bool) {
	if hasID(groupedEntries(hooks, d.Event), d.ID) {
		return
	}
	v, _ := hooks.Get(d.Event)
	groups, ok := v.([]any)
	if v != nil && !ok {
		return
	}
	entry := jsonorder.NewObject().Set("type", "command").Set("command", d.Cmd).Set("timeout", num(d.Timeout))
	var target *jsonorder.Object
	for _, g := range groups {
		gobj := asObj(g)
		if gobj == nil || matcherOf(gobj) != d.Matcher {
			continue
		}
		if hv, _ := gobj.Get("hooks"); hv != nil {
			if _, ok := hv.([]any); !ok {
				continue
			}
		} else {
			continue
		}
		target = gobj
		if !last {
			break
		}
	}
	if target != nil {
		hv, _ := target.Get("hooks")
		target.Set("hooks", append(objList(hv), entry))
		hooks.Set(d.Event, groups)
		return
	}
	g := jsonorder.NewObject()
	if d.Matcher != "" {
		g.Set("matcher", d.Matcher)
	}
	g.Set("hooks", []any{entry})
	hooks.Set(d.Event, append(groups, g))
}

// syncGroupedLoop は loop の配線を desired にそろえる。
// 同じイベント・matcher・名前のものはその場でコマンドと timeout を直し、desired に無いものを外し、足りないものを後ろに足す。
func syncGroupedLoop(hooks *jsonorder.Object, desired []want) {
	matched := make([]bool, len(desired))
	for _, ev := range hooks.Keys() {
		v, _ := hooks.Get(ev)
		groups, ok := v.([]any)
		if !ok {
			continue
		}
		var outGroups []any
		changed := false
		for _, g := range groups {
			gobj := asObj(g)
			hv, _ := gobj.Get("hooks")
			hs, ok := hv.([]any)
			if gobj == nil || !ok {
				outGroups = append(outGroups, g)
				continue
			}
			var kept []any
			for _, h := range hs {
				ho := asObj(h)
				c := classify(cmdOf(ho))
				if c.kind == kindOther || !c.loop {
					kept = append(kept, h)
					continue
				}
				hit := -1
				for i, d := range desired {
					if !matched[i] && d.Event == ev && d.Matcher == matcherOf(gobj) && d.ID == c.id {
						hit = i
						break
					}
				}
				if hit < 0 {
					changed = true
					continue
				}
				matched[hit] = true
				ho.Set("command", desired[hit].Cmd)
				ho.Set("timeout", num(desired[hit].Timeout))
				kept = append(kept, h)
			}
			if len(kept) != len(hs) {
				changed = true
				if len(kept) == 0 {
					continue // loop の hook だけだったグループは消す
				}
				gobj.Set("hooks", kept)
			}
			outGroups = append(outGroups, g)
		}
		if changed {
			if len(outGroups) > 0 {
				hooks.Set(ev, outGroups)
			} else {
				hooks.Delete(ev)
			}
		}
	}
	for i, d := range desired {
		if !matched[i] {
			addGrouped(hooks, d, true)
		}
	}
}

// stripManaged は init が書いた配線（core と loop）をすべて外す（配線の置き場を変えたとき）。
func stripManaged(hooks *jsonorder.Object) bool {
	removed := false
	for _, ev := range hooks.Keys() {
		v, _ := hooks.Get(ev)
		groups, ok := v.([]any)
		if !ok {
			continue
		}
		var outGroups []any
		for _, g := range groups {
			gobj := asObj(g)
			hv, _ := gobj.Get("hooks")
			hs, ok := hv.([]any)
			if gobj == nil || !ok {
				outGroups = append(outGroups, g)
				continue
			}
			var kept []any
			for _, h := range hs {
				if classify(cmdOf(asObj(h))).kind == kindOther {
					kept = append(kept, h)
				}
			}
			if len(kept) != len(hs) {
				removed = true
				if len(kept) == 0 {
					continue
				}
				gobj.Set("hooks", kept)
			}
			outGroups = append(outGroups, g)
		}
		if len(outGroups) == 0 {
			hooks.Delete(ev)
		} else {
			hooks.Set(ev, outGroups)
		}
	}
	return removed
}

// ---------------------------------------------------------------- Copilot（イベントごとの配列）

func copilotEntry(d want) *jsonorder.Object {
	e := jsonorder.NewObject().Set("type", "command").Set("command", d.Cmd).Set("bash", d.Cmd).
		Set("powershell", d.PS).Set("windows", d.PS).Set("timeoutSec", num(d.Timeout))
	if d.Matcher != "" {
		e.Set("matcher", d.Matcher)
	}
	return e
}

// setCopilotCmd は init が置いたままの hook のコマンドを新しい形にする（OS 別のフィールドは無ければ足す）。
func setCopilotCmd(h *jsonorder.Object, old, cmd, ps string) {
	h.Set("command", cmd)
	if b := h.String("bash"); b == "" || b == old {
		h.Set("bash", cmd)
	}
	for _, k := range []string{"powershell", "windows"} {
		if _, ok := h.Get(k); !ok {
			h.Set(k, ps)
		}
	}
}

func flatEntries(hooks *jsonorder.Object, ev string) []*jsonorder.Object {
	v, _ := hooks.Get(ev)
	var out []*jsonorder.Object
	for _, h := range objList(v) {
		if o := asObj(h); o != nil {
			out = append(out, o)
		}
	}
	return out
}

// flatSync は Copilot の hooks（{イベント: [{type, command, bash, powershell, windows, timeoutSec, matcher?}]}）を直す（groupedSync の Copilot 版）。
func flatSync(hooks *jsonorder.Object, w wiring, coreWant []want, loopWant []want, touchLoop bool) {
	if touchLoop {
		dropSplitSessionStart(hooks, loopWant)
		for _, d := range loopWant {
			if d.Lead && !hooks.Has(d.Event) {
				hooks.Set(d.Event, []any{}) // syncFlatLoop が足す（core の summary が先頭に作っていた位置を保つ）
			}
		}
	}
	for _, d := range coreWant {
		entries := flatEntries(hooks, d.Event)
		if hasID(entries, d.ID) {
			// init が置いたままなら OS 別のフィールドだけ補う
			for _, h := range entries {
				if cmdOf(h) == d.Cmd {
					fillVariants(h, d)
				}
			}
			continue
		}
		v, _ := hooks.Get(d.Event)
		if v != nil {
			if _, ok := v.([]any); !ok {
				continue
			}
		}
		hooks.Set(d.Event, append(objList(v), copilotEntry(d)))
	}
	if touchLoop {
		syncFlatLoop(hooks, loopWant)
	}
}

func fillVariants(h *jsonorder.Object, d want) {
	for k, val := range map[string]string{"bash": d.Cmd, "powershell": d.PS, "windows": d.PS} {
		if !h.Has(k) {
			h.Set(k, val)
		}
	}
}

// syncFlatLoop は Copilot の loop の配線を desired にそろえる。
func syncFlatLoop(hooks *jsonorder.Object, desired []want) {
	matched := make([]bool, len(desired))
	for _, ev := range hooks.Keys() {
		v, _ := hooks.Get(ev)
		entries, ok := v.([]any)
		if !ok {
			continue
		}
		var kept []any
		for _, h := range entries {
			ho := asObj(h)
			cmd := cmdOf(ho)
			c := classify(cmd)
			if ho == nil || c.kind == kindOther || !c.loop {
				kept = append(kept, h)
				continue
			}
			hit := -1
			for i, d := range desired {
				if !matched[i] && d.Event == ev && d.Matcher == matcherOf(ho) && d.ID == c.id {
					hit = i
					break
				}
			}
			if hit < 0 {
				continue
			}
			matched[hit] = true
			setCopilotCmd(ho, cmd, desired[hit].Cmd, desired[hit].PS)
			ho.Set("timeoutSec", num(desired[hit].Timeout))
			kept = append(kept, h)
		}
		if len(kept) != len(entries) {
			if len(kept) > 0 {
				hooks.Set(ev, kept)
			} else {
				hooks.Delete(ev)
			}
		}
	}
	for i, d := range desired {
		if matched[i] {
			continue
		}
		v, _ := hooks.Get(d.Event)
		hooks.Set(d.Event, append(objList(v), copilotEntry(d)))
	}
}

// stripManagedFlat は Copilot の init の配線をすべて外す。
func stripManagedFlat(hooks *jsonorder.Object) {
	for _, ev := range hooks.Keys() {
		v, _ := hooks.Get(ev)
		entries, ok := v.([]any)
		if !ok {
			continue
		}
		var kept []any
		for _, h := range entries {
			if classify(cmdOf(asObj(h))).kind == kindOther {
				kept = append(kept, h)
			}
		}
		if len(kept) == 0 {
			hooks.Delete(ev)
		} else {
			hooks.Set(ev, kept)
		}
	}
}

// ---------------------------------------------------------------- Copilot の SessionStart の合成

// Copilot CLI（1.0.86 で実測）は、同じイベントの複数の hook が additionalContext を出すと最後の 1 つしか AI に渡さない。
// loop を入れると SessionStart に summary（core）と session-start-rules・memories・iteration が並び、summary が消えていた。
// Copilot の配線では SessionStart の hook を `looptrack hook session-start --parts <名前,…>`（loop.SessionStartCombined）の
// 1 本にまとめる。まとめた配線は loop の配線として扱う（classify が loop と見る。syncFlatLoop がその場で直す・外す）。
// UserPromptSubmit も同じ（user-prompt-rules・user-prompt-task-mode・session-scope-guard を `looptrack hook user-prompt
// --parts …` の 1 本に。部分はすべて loop の hook なので、個別の配線は syncFlatLoop が外し、--remove-loop では合成の配線ごと外れる）。

var combinedPartsRe = regexp.MustCompile(`(?:^|\s)--parts[= ](\S+)`)

// combinedParts は合成の SessionStart の配線の --parts の名前。
func combinedParts(cmd string) []string {
	m := combinedPartsRe.FindStringSubmatch(cmd)
	if m == nil {
		return nil
	}
	var out []string
	for _, p := range strings.Split(strings.Trim(m[1], `"'`), ",") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// mergeCopilotSessionStart は Copilot の配線（coreW・loopW）の SessionStart・UserPromptSubmit がそれぞれ 2 本以上なら、
// 1 本の合成の配線（looptrack hook session-start / user-prompt --parts …）にまとめる。合成の配線は loop の配線として扱い、
// loopW のそのイベントの最初の配線の位置（loopW に無ければ先頭）に置き、coreW・loopW からそのイベントの個別の配線を外す。
// 1 本以下のイベントはそのまま。Claude Code・Codex（複数の hook の文脈をすべて渡す）は変えない。
func mergeCopilotSessionStart(w wiring, coreW, loopW []want) ([]want, []want) {
	if w.agent != "copilot" {
		return coreW, loopW
	}
	for _, ev := range []string{"SessionStart", "UserPromptSubmit"} {
		coreW, loopW = mergeCopilotEvent(w, ev, coreW, loopW)
	}
	return coreW, loopW
}

// mergeCopilotEvent は ev の配線を 1 本の合成の配線にまとめる（mergeCopilotSessionStart の 1 イベント分）。
func mergeCopilotEvent(w wiring, ev string, coreW, loopW []want) ([]want, []want) {
	name := loop.CombinedFor(hookio.Name(ev))
	var parts []string
	lead := false
	for i, l := range [][]want{coreW, loopW} {
		for _, d := range l {
			if d.Event == ev && d.ID != name {
				parts = append(parts, d.ID)
				lead = lead || i == 0
			}
		}
	}
	if name == "" || len(parts) < 2 {
		return coreW, loopW
	}
	extra := " --parts " + strings.Join(parts, ",")
	// env の前置は core の部分（summary）があるときだけ（loop の hook だけなら個別の配線と同じく前置しない）
	merged := want{Event: ev, ID: name, Timeout: combinedWiringTimeout, Lead: lead,
		Cmd: w.hook(name, extra, lead), PS: w.hookPS(name, extra, lead)}
	var outCore, outLoop []want
	for _, d := range coreW {
		if d.Event != ev {
			outCore = append(outCore, d)
		}
	}
	placed := false
	for _, d := range loopW {
		if d.Event != ev {
			outLoop = append(outLoop, d)
		} else if !placed {
			outLoop, placed = append(outLoop, merged), true
		}
	}
	if !placed {
		outLoop = append([]want{merged}, outLoop...)
	}
	return outCore, outLoop
}

// combinedWiringTimeout は合成の配線（SessionStart・UserPromptSubmit）の timeoutSec（部分の打ち切りの最大 9 秒と、hook 全体の 12 秒より長く）。
const combinedWiringTimeout = 15

// dropSplitSessionStart は、合成の SessionStart を配線するとき、init が置いた個別の SessionStart（summary など core の配線。
// loop の個別の配線は syncFlatLoop が外す）を外す（以前の init の Copilot の配線から移るとき）。手で変えた配線は残す。
func dropSplitSessionStart(hooks *jsonorder.Object, loopW []want) {
	var parts []string
	for _, d := range loopW {
		if d.ID == loop.CombinedSessionStart {
			parts = combinedParts(d.Cmd)
		}
	}
	if len(parts) == 0 {
		return
	}
	v, _ := hooks.Get("SessionStart")
	entries, ok := v.([]any)
	if !ok {
		return
	}
	var kept []any
	for _, h := range entries {
		c := classify(cmdOf(asObj(h)))
		if c.kind != kindOther && !c.loop && has(parts, c.id) {
			continue
		}
		kept = append(kept, h)
	}
	if len(kept) == len(entries) {
		return
	}
	if len(kept) == 0 {
		hooks.Delete("SessionStart")
	} else {
		hooks.Set("SessionStart", kept)
	}
}

// splitCombinedSessionStart は --remove-loop で、合成の SessionStart のうち core の部分（summary）を個別の配線に戻す
// （合成の配線は loop の配線として外されるため。戻さないと loop を外した後に summary が動かない）。
func splitCombinedSessionStart(hooks *jsonorder.Object) {
	for _, h := range flatEntries(hooks, "SessionStart") {
		cmd := cmdOf(h)
		m := newCmd.FindStringSubmatch(cmd)
		if m == nil || m[2] != loop.CombinedSessionStart {
			continue
		}
		var coreParts []string
		for _, p := range combinedParts(cmd) {
			if _, ok := core.Lookup(p); ok {
				coreParts = append(coreParts, p)
			}
		}
		if len(coreParts) != 1 {
			continue // core の部分が無い（か、想定外の形）なら合成の配線ごと外す
		}
		rest := strings.TrimSpace(combinedPartsRe.ReplaceAllString(m[4], " "))
		if rest != "" {
			rest = " " + rest
		}
		repl := func(s string) string {
			i := strings.Index(s, " hook "+loop.CombinedSessionStart+" ")
			if i < 0 {
				return s
			}
			return s[:i] + " hook " + coreParts[0] + " --agent " + m[3] + rest
		}
		for _, k := range []string{"command", "bash", "powershell", "windows"} {
			if v, ok := h.Get(k); ok {
				h.Set(k, repl(jsonorder.Str(v)))
			}
		}
		h.Set("timeoutSec", num(10))
	}
}
