// Package kitinit は `looptrack issue init`（以前の CLI（1.0.0 より前）の init の Go 版。DESIGN.md §5-11）。
//
// 以前の CLI と同じにするもの: 引数・誤りの文面・.claude/.looptrack-kit.json の構造・CLAUDE.md / AGENTS.md の案内節・skill と rules の置き場・
// loop の問いの文面・dry-run の差分の形・--remove-loop・既存の設定のマージと控え（.claude/.looptrack-init-backup）。
//
// 意図して変えたもの:
//   - 配線は `looptrack hook <名前> --agent <AI>`（wiring.go）。以前の配線はその場で置き換え、手で変えたものは残す。
//   - スクリプトを 1 つも置かない。導入先に要るのは looptrack の実行ファイル 1 つだけ。
//   - kit は実行ファイルに埋め込んだもの（--source server / --dist ならサーバの配布物の kit/ だけ）。symlink は作らない（copy）。
//   - 導入の後に verify（verify.go）を実行し、失敗したら書いたものを元に戻して止める。
package kitinit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/cli"
	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/kit"
)

// Register は `looptrack issue init` の本体を cli に登録する（cmd/looptrack が呼ぶ）。
func Register() { cli.InitRunner = Run }

// テストで差し替える口。
var (
	lookPath    = exec.LookPath
	executable  = os.Executable
	now         = time.Now
	interactive = func(r io.Reader) bool {
		f, ok := r.(*os.File)
		return ok && term.IsTerminal(int(f.Fd()))
	}
	// embedded は実行ファイルに埋め込んだ kit（{"kit/…": 本文}）。
	embedded = func() map[string]string {
		out := map[string]string{}
		for _, n := range kit.Names() {
			if b, err := kit.ReadFile(n); err == nil {
				out[n] = string(b)
			}
		}
		return out
	}
)

var agentsAll = []string{"claude-code", "codex", "copilot", "other"}

const (
	kitJSON         = ".claude/.looptrack-kit.json"
	kitSkill        = "kit/core/skills/issue/SKILL.md"
	kitTokenReport  = "kit/core/skills/token-report/SKILL.md"
	codexMCPMatcher = "mcp__.*"
	codexConfig     = ".codex/config.toml"
	copilotHooks    = ".github/hooks/looptrack.json"
	// legacyCopilotHooks は以前の版が書いた置き場（製品の旧名）。init が中身を copilotHooks へ移して消す。
	// Copilot は .github/hooks/*.json をすべて読むので、両方が残ると同じ hook が 2 回動く。
	legacyCopilotHooks = ".github/hooks/im.json"
	copilotVSCMCP      = ".vscode/mcp.json"
	copilotCLIMCP      = ".github/mcp.json"
)

// Options は init の引数。
type Options struct {
	Project, URL, Agent, Dir, Source, Dist                       string
	DryRun, Force, MCP, NoFreshness, NoUsage, NoSummary, NoSkill bool
	Loop, NoLoop, RemoveLoop, NoVerify                           bool
	agents                                                       []string
}

func optionsFrom(v *cli.Values, e env.Env) *Options {
	o := &Options{
		Project: v.Str("project"), URL: v.Str("url"), Agent: v.Str("agent"), Dir: v.Str("dir"), Source: v.Str("source"), Dist: v.Str("dist"),
		DryRun: v.Bool("dry_run"), Force: v.Bool("force"), MCP: v.Bool("mcp"), NoFreshness: v.Bool("no_freshness"), NoUsage: v.Bool("no_usage"),
		NoSummary: v.Bool("no_summary"), NoSkill: v.Bool("no_skill"), Loop: v.Bool("loop"), NoLoop: v.Bool("no_loop"),
		RemoveLoop: v.Bool("remove_loop"), NoVerify: v.Bool("no_verify"),
	}
	if !v.IsSet("project") {
		o.Project = e.Value(env.Project) // 既定（環境変数 LOOPTRACK_PROJECT）
	}
	return o
}

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Run は init の本体。
func Run(c *cli.Ctx, v *cli.Values) error {
	o := optionsFrom(v, c.Env)
	if o.Loop && o.NoLoop {
		return i18n.Errorf("kitinit.init.err.loop_and_no_loop")
	}
	if o.RemoveLoop && (o.Loop || o.NoLoop) {
		return i18n.Errorf("kitinit.init.err.remove_loop_with_loop")
	}
	if !o.RemoveLoop && !slugRe.MatchString(o.Project) {
		return i18n.Errorf("kitinit.init.err.bad_project")
	}
	for _, a := range strings.Split(o.Agent, ",") {
		if a = strings.TrimSpace(a); a != "" {
			o.agents = append(o.agents, a)
		}
	}
	for _, a := range o.agents {
		if !has(agentsAll, a) {
			return i18n.Errorf("kitinit.init.err.bad_agent", "agents", strings.Join(agentsAll, " / "), "value", a)
		}
	}
	url := o.URL
	if url == "" {
		url = api.NormalizeURL(c.Env.Value(env.APIURL))
	}
	if url == "" {
		url = cli.DefaultURL
	}
	url = strings.TrimSuffix(strings.TrimRight(url, "/"), "/api/v1")
	dir := o.Dir
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		dir = wd
	}
	target, _ := filepath.Abs(dir)
	if r, err := filepath.EvalSymlinks(target); err == nil {
		target = r
	}
	if !isDir(target) {
		return i18n.Errorf("kitinit.init.err.no_target", "dir", target)
	}
	if cli.IsSelfRepo(target) { // 自分自身には導入しない（判定は cli.IsSelfRepo の 1 か所）
		return i18n.Errorf("kitinit.init.err.target_is_repo", "dir", target)
	}
	if o.RemoveLoop {
		return removeLoop(c, o, target)
	}
	dry := ""
	if o.DryRun {
		dry = i18n.T(c.Lang, "kitinit.init.header.dry")
	}
	c.Println(i18n.T(c.Lang, "kitinit.init.header", "dir", target, "project", o.Project, "url", url,
		"agents", strings.Join(o.agents, ", "), "dry", dry))
	// other だけなら案内を出すだけ
	if len(o.agents) == 1 && o.agents[0] == "other" {
		c.Println(otherGuidance(c.Lang, url, o.Project, false, GoCLI))
		return nil
	}
	in := &installer{c: c, o: o, url: url, target: target, plan: &Plan{Root: target, Lang: c.Lang}}
	return in.run()
}

// installer は 1 回の導入。
type installer struct {
	c      *cli.Ctx
	o      *Options
	url    string
	target string
	plan   *Plan
	files  map[string]string // kit の全ファイル（{"kit/…": 本文}）
	source string            // copy（埋め込み）/ server
	bin    binRef
	loop   []loopEntry // loop を配線する（nil は触らない）
	loopOn bool
}

// binRef は配線での looptrack の指し方。
type binRef struct {
	sh, ps string // シェル・PowerShell での呼び方
	abs    string // 絶対パスで配線するときの実行ファイル（PATH に無い端末）
	note   string
}

// resolveBin は looptrack の指し方を決める（PATH → 実行中の looptrack の絶対パス）。
func resolveBin(lang i18n.Lang) binRef {
	if p, err := lookPath("looptrack"); err == nil && p != "" {
		return binRef{sh: "looptrack", ps: "looptrack"}
	}
	if exe, err := executable(); err == nil && strings.HasPrefix(strings.ToLower(filepath.Base(exe)), "looptrack") {
		if r, err := filepath.EvalSymlinks(exe); err == nil {
			exe = r
		}
		return binRef{sh: `"` + exe + `"`, ps: "& '" + strings.ReplaceAll(exe, "'", "''") + "'", abs: exe,
			note: i18n.T(lang, "kitinit.init.note.wired_abs", "exe", exe, "bindir", binDirHint())}
	}
	return binRef{sh: "looptrack", ps: "looptrack",
		note: i18n.T(lang, "kitinit.init.note.not_in_path", "bindir", binDirHint())}
}

// binDirHint は looptrack を置く場所の例（setup の置き場。管理者権限が要らない）。
func binDirHint() string {
	if runtime.GOOS == "windows" {
		return `%LOCALAPPDATA%\Programs\looptrack`
	}
	return "~/.local/bin"
}

func (in *installer) wiring(agent string) wiring {
	return wiring{agent: agent, bin: in.bin.sh, psBin: in.bin.ps, url: in.url, slug: in.o.Project, env: agent != "claude-code"}
}

func (in *installer) run() error {
	c, o, p := in.c, in.o, in.plan
	if err := in.loadKit(); err != nil {
		return err
	}
	in.bin = resolveBin(c.Lang)
	prev, err := readKitJSON(in.target)
	if err != nil {
		return err
	}
	prevLoop := prev.Object("loop")
	if prevLoop == nil {
		prevLoop = jsonorder.NewObject()
	}
	var loopAgents []string
	for _, a := range o.agents {
		if a != "other" {
			loopAgents = append(loopAgents, a)
		}
	}
	loopFiles := map[string]string{}
	if len(loopAgents) > 0 {
		for n, b := range in.files {
			if strings.HasPrefix(n, "kit/loop/") {
				loopFiles[n] = b
			}
		}
	}
	where := i18n.T(c.Lang, "kitinit.init.source.embedded")
	if in.source == "server" {
		where = i18n.T(c.Lang, "kitinit.init.source.server")
	}
	_, hasManifest := loopFiles[kitLoopManifest]
	if o.Loop && len(loopAgents) == 0 {
		return i18n.Errorf("kitinit.init.err.loop_needs_agent")
	}
	if o.Loop && !hasManifest {
		return i18n.Errorf("kitinit.init.err.no_loop_kit", "where", where)
	}
	if o.NoLoop && truthy(prevLoop, "installed") {
		return i18n.Errorf("kitinit.init.err.loop_installed")
	}
	wantLoop, decline := in.decideLoop(prevLoop, loopFiles, hasManifest)
	loopState := prevLoop
	if len(prevLoop.Keys()) == 0 {
		loopState = jsonorder.NewObject().Set("installed", false)
	}
	switch {
	case wantLoop && !hasManifest:
		p.Note(i18n.T(c.Lang, "kitinit.init.note.loop_not_updated", "where", where))
	case wantLoop:
		version, entries, err := parseManifest(loopFiles)
		if err != nil {
			return err
		}
		in.loop, in.loopOn = entries, true
		var prevFiles map[string]string
		if truthy(prevLoop, "installed") {
			prevFiles = strMap(prevLoop.Object("files"))
		}
		in.planLoopFiles(loopFiles, prevFiles)
		sums := sums(loopFiles)
		at := any(stamp())
		if truthy(prevLoop, "installed") {
			at, _ = prevLoop.Get("installed_at")
		}
		loopState = jsonorder.NewObject().Set("installed", true).Set("installed_at", at).Set("bundle_sha256", bundleSHA(sums)).Set("files", walkObj(sums))
		if version != nil {
			loopState.Set("version", jsonValue(version))
		}
	case decline:
		loopState = jsonorder.NewObject().Set("installed", false).Set("declined_at", stamp())
	}

	if has(o.agents, "claude-code") {
		if err := in.planClaude(); err != nil {
			return err
		}
	}
	if has(o.agents, "codex") {
		if err := in.planCodex(); err != nil {
			return err
		}
	}
	if has(o.agents, "copilot") {
		if err := in.planCopilot(); err != nil {
			return err
		}
	}
	if has(o.agents, "codex") || has(o.agents, "copilot") {
		in.planAgentsMD()
	}
	if !o.NoFreshness && has(o.agents, "claude-code") {
		cur, _ := readFile(p.path(".gitignore"))
		if !has(strings.Split(cur, "\n"), ".claude/.looptrack-freshness/") {
			next := ""
			if trimSpace(cur) != "" {
				next = strings.TrimRight(cur, "\n") + "\n"
			}
			p.Text(".gitignore", next+".claude/.looptrack-freshness/\n")
		}
	}
	in.planKitJSON(prev, loopState)

	switch in.source {
	case "server":
		c.Println(i18n.T(c.Lang, "kitinit.init.kit_from.server", "url", in.url))
	default:
		c.Println(i18n.T(c.Lang, "kitinit.init.kit_from.embedded", "version", api.Version))
	}
	c.Println(i18n.T(c.Lang, "kitinit.init.wiring", "bin", in.bin.sh))
	if in.bin.note != "" {
		p.Note(in.bin.note)
	}
	if len(loopAgents) > 0 {
		switch {
		case wantLoop && in.loopOn:
			n := 0
			for _, e := range in.loop {
				if e.Kind == "hook" {
					n++
				}
			}
			c.Println(i18n.T(c.Lang, "kitinit.init.set.core_loop", "count", n))
		case wantLoop:
			c.Println(i18n.T(c.Lang, "kitinit.init.set.core_loop_nochange"))
		case truthy(loopState, "declined_at"):
			c.Println(i18n.T(c.Lang, "kitinit.init.set.core_declined"))
		default:
			c.Println(i18n.T(c.Lang, "kitinit.init.set.core_only"))
		}
	}
	changed, applied, err := in.finish()
	if err != nil {
		return err
	}
	if !o.DryRun && !o.NoVerify {
		rep := in.verify()
		if len(rep.fails) > 0 {
			msg := i18n.T(c.Lang, "kitinit.init.err.verify_failed")
			if applied != nil {
				if rerr := applied.Rollback(); rerr != nil {
					msg += i18n.T(c.Lang, "kitinit.init.err.rollback_failed", "reason", rerr, "backup", applied.BackupDir)
				} else {
					msg += i18n.T(c.Lang, "kitinit.init.err.rolled_back")
				}
			}
			return cli.Failf("%s:\n  - %s", msg, strings.Join(rep.fails, "\n  - "))
		}
		c.Println(i18n.T(c.Lang, "kitinit.init.verify.done", "summary", rep.summary()))
	} else if !o.DryRun {
		c.Println(i18n.T(c.Lang, "kitinit.init.verify.skipped"))
	}
	in.nextSteps(changed)
	return nil
}

// finish は変更を表示して書く（dry-run は表示だけ）。
func (in *installer) finish() (bool, *Applied, error) {
	return finishPlan(in.c, in.plan, in.o.DryRun, in.target)
}

func finishPlan(c *cli.Ctx, p *Plan, dryRun bool, target string) (bool, *Applied, error) {
	changed := p.Show(c.Stdout, dryRun)
	for _, n := range p.Notes {
		c.Println(i18n.T(c.Lang, "kitinit.init.note", "note", n))
	}
	switch {
	case dryRun && changed:
		c.Println(i18n.T(c.Lang, "kitinit.init.dry_run.changed"))
	case dryRun:
		c.Println(i18n.T(c.Lang, "kitinit.init.no_change"))
	case changed:
		backup := filepath.Join(target, ".claude", ".looptrack-init-backup", now().Format("20060102-150405"))
		a, err := p.Apply(backup)
		if err != nil {
			if a != nil {
				a.Rollback()
			}
			return changed, nil, i18n.Wrapf(err, "kitinit.init.err.write_rolled_back")
		}
		if a.BackupDir != "" {
			rel, _ := filepath.Rel(target, a.BackupDir)
			c.Println(i18n.T(c.Lang, "kitinit.init.backup", "dir", rel))
		}
		return changed, a, nil
	default:
		c.Println(i18n.T(c.Lang, "kitinit.init.no_change.installed"))
	}
	return changed, nil, nil
}

func (in *installer) nextSteps(changed bool) {
	c, o := in.c, in.o
	var steps []string
	if changed && !o.DryRun {
		if has(o.agents, "claude-code") {
			steps = append(steps, i18n.T(c.Lang, "kitinit.init.step.restart_claude"))
		}
		if has(o.agents, "codex") {
			steps = append(steps, i18n.T(c.Lang, "kitinit.init.step.trust_codex"))
		}
		if has(o.agents, "copilot") {
			steps = append(steps, i18n.T(c.Lang, "kitinit.init.step.restart_copilot", "file", copilotHooks))
		}
	}
	token := canCall(c.Env, in.url)
	cliCmd := GoCLI
	if !token {
		steps = append(steps, i18n.T(c.Lang, "kitinit.init.step.login", "cli", cliCmd, "url", in.url))
	}
	if has(o.agents, "claude-code") || has(o.agents, "other") {
		steps = append(steps, i18n.T(c.Lang, "kitinit.init.step.check_cli", "cli", cliCmd))
	}
	if has(o.agents, "codex") {
		steps = append(steps, i18n.T(c.Lang, "kitinit.init.step.check_mcp", "agent", "Codex"))
	}
	if has(o.agents, "copilot") {
		steps = append(steps, i18n.T(c.Lang, "kitinit.init.step.check_mcp", "agent", "Copilot"))
	}
	if has(o.agents, "other") {
		steps = append(steps, i18n.T(c.Lang, "kitinit.init.step.installed_other", "url", in.url, "project", o.Project, "cli", cliCmd))
	}
	c.Println(i18n.T(c.Lang, "kitinit.init.next"))
	for i, s := range steps {
		c.Printf("  %d. %s\n", i+1, s)
	}
	if has(o.agents, "codex") {
		c.Println(codexGuidance(c.Lang, in.url, o.Project))
	}
	if has(o.agents, "copilot") {
		c.Println(copilotGuidance(c.Lang, in.url, o.Project))
	}
	if has(o.agents, "other") {
		c.Println(otherGuidance(c.Lang, in.url, o.Project, true, cliCmd))
	}
}

// canCall は、この URL のサーバを今のまま呼べるか（トークンがある、またはローカルモードのサーバ）。
func canCall(e env.Env, url string) bool {
	cl, err := api.New(e)
	if err != nil {
		return false
	}
	tok, _, err := cl.TokenSource(url)
	return err == nil && (tok != "" || cl.LocalMode(url))
}

// decideLoop は loop を入れるか。返り値 (入れる, 辞退を記録する)。
func (in *installer) decideLoop(prevLoop *jsonorder.Object, loopFiles map[string]string, available bool) (bool, bool) {
	o := in.o
	switch {
	case o.Loop:
		return true, false
	case o.NoLoop:
		return false, true
	case truthy(prevLoop, "installed"):
		return true, false // 入っているものは再実行で更新する
	case truthy(prevLoop, "declined_at") || !available:
		return false, false
	case o.DryRun || !interactive(in.c.Stdin):
		return false, false // 非対話: 入れない。辞退は記録しない（次の対話で問う）
	}
	h, r, s := loopCounts(loopFiles)
	in.c.Printf("%s", loopQuestion(in.c.Lang, h, r, s))
	line, err := readLine(in.c.Stdin)
	if err != nil && line == "" {
		in.c.Println()
		return false, false
	}
	ans := strings.ToLower(trimSpace(line))
	yes := ans == "y" || ans == "yes" || ans == "はい"
	return yes, !yes
}

func readLine(r io.Reader) (string, error) {
	var b []byte
	one := make([]byte, 1)
	for {
		n, err := r.Read(one)
		if n > 0 {
			if one[0] == '\n' {
				return string(b), nil
			}
			b = append(b, one[0])
		}
		if err != nil {
			return string(b), err
		}
	}
}

// loadKit は kit のファイルを取る（埋め込み・サーバ）。
func (in *installer) loadKit() error {
	o := in.o
	if o.Dist != "" && o.Source != "" && o.Source != "server" {
		return i18n.Errorf("kitinit.init.err.dist_needs_server")
	}
	switch {
	case o.Dist != "" || o.Source == "server":
		files, err := fetchKit(in.c.Env, in.url, o.Dist)
		if err != nil {
			return err
		}
		in.files, in.source = files, "server"
	default:
		if o.Source == "link" {
			in.plan.Note(i18n.T(in.c.Lang, "kitinit.init.note.source_link"))
		}
		in.files, in.source = embedded(), "copy"
	}
	return nil
}

// ---------------------------------------------------------------- 各設定ファイル

// loadJSON は JSON のオブジェクトのファイルを読む（無ければ空）。
func loadJSON(p *Plan, rel string) (*jsonorder.Object, error) {
	fp := p.path(rel)
	if !isFile(fp) {
		return jsonorder.NewObject(), nil
	}
	b, _ := os.ReadFile(fp)
	v, err := jsonorder.Decode(b)
	if err != nil {
		return nil, i18n.Errorf("kitinit.init.err.bad_json", "file", rel, "reason", err)
	}
	o, ok := v.(*jsonorder.Object)
	if !ok {
		return nil, i18n.Errorf("kitinit.init.err.json_not_object", "file", rel)
	}
	return o, nil
}

func dumpJSON(o *jsonorder.Object) string { return jsonorder.Indent(o, 2) + "\n" }

// putJSON は data を rel に書く予定にする（意味が変わらなければ今のファイルのまま＝書式の違いで書き換えない）。
func putJSON(p *Plan, rel string, data *jsonorder.Object, before string, existed bool) {
	after := dumpJSON(data)
	if after == before && existed {
		cur, _ := readFile(p.path(rel))
		p.Text(rel, cur)
		return
	}
	p.Text(rel, after)
}

func beforeOf(data *jsonorder.Object) (string, bool) {
	if len(data.Keys()) == 0 {
		return "", false
	}
	return dumpJSON(data.Clone()), true
}

func ensureObj(parent *jsonorder.Object, key string) *jsonorder.Object {
	if o := parent.Object(key); o != nil {
		return o
	}
	o := jsonorder.NewObject()
	parent.Set(key, o)
	return o
}

func (in *installer) planClaude() error {
	p, o := in.plan, in.o
	rel := ".claude/settings.json"
	data, err := loadJSON(p, rel)
	if err != nil {
		return err
	}
	before, existed := beforeOf(data)
	if !data.Has("env") {
		data = &jsonorder.Object{Members: append([]jsonorder.Member{{Key: "env", Value: jsonorder.NewObject()}}, data.Members...)}
	}
	env := data.Object("env")
	if env == nil {
		return i18n.Errorf("kitinit.init.err.env_not_object", "file", rel)
	}
	if cur := env.String("LOOPTRACK_PROJECT"); cur != "" && cur != o.Project && !o.Force {
		return i18n.Errorf("kitinit.init.err.env_project", "file", rel, "current", cur, "next", o.Project)
	}
	if cur := env.String("LOOPTRACK_API_URL"); cur != "" && strings.TrimRight(cur, "/") != in.url && !o.Force {
		return i18n.Errorf("kitinit.init.err.env_url", "file", rel, "current", cur, "next", in.url)
	}
	if v, _ := env.Get("LOOPTRACK_TOKEN"); jsonorder.Truthy(v) {
		p.Note(i18n.T(p.Lang, "kitinit.init.note.env_token", "file", rel, "cli", GoCLI))
	}
	env.Set("LOOPTRACK_API_URL", in.url)
	env.Set("LOOPTRACK_PROJECT", o.Project)
	w := in.wiring("claude-code")
	var loopWant []want
	if in.loopOn {
		var skipped []string
		loopWant, skipped = loopWants(in.loop, w)
		for _, s := range skipped {
			p.Note(i18n.T(p.Lang, "kitinit.init.note.unknown_loop_hook", "name", s))
		}
	}
	hooks := ensureObj(data, "hooks")
	local := ".claude/settings.local.json"
	if in.bin.abs == "" {
		groupedSync(hooks, w, coreWants(w, o), loopWant, in.loopOn)
		if isFile(p.path(local)) { // 以前に絶対パスで配線していたら、手元の設定から外す
			ld, err := loadJSON(p, local)
			if err != nil {
				return err
			}
			lb, lex := beforeOf(ld)
			if lh := ld.Object("hooks"); lh != nil && stripManaged(lh) {
				if len(lh.Keys()) == 0 {
					ld.Delete("hooks")
				}
				putJSON(p, local, ld, lb, lex)
			}
		}
	} else {
		// PATH に looptrack が無い: 共有の設定から init の配線を外し、手元専用の設定に絶対パスで配線する
		groupedSync(hooks, w, nil, nil, false)
		stripManaged(hooks)
		ld, err := loadJSON(p, local)
		if err != nil {
			return err
		}
		lb, lex := beforeOf(ld)
		lh := ensureObj(ld, "hooks")
		groupedSync(lh, w, coreWants(w, o), loopWant, in.loopOn)
		if len(lh.Keys()) == 0 {
			ld.Delete("hooks")
		}
		putJSON(p, local, ld, lb, lex)
	}
	if len(hooks.Keys()) == 0 {
		data.Delete("hooks")
	}
	addPermission(data)
	putJSON(p, rel, data, before, existed)
	upsertBlock(p, "CLAUDE.md", fill(claudeSnippet, o.Project, in.url))
	if !o.NoSkill {
		in.planCoreSkill("issue", kitSkill, true)
		// token-report。以前の CLI（1.0.0 より前）の init はリポジトリの skills/token-report へのディレクトリの
		// symlink で置いていた（その撤去で先が無くなる）。symlink は消して実体を置く
		if isLink(p.path(".claude/skills/token-report")) {
			p.Remove(".claude/skills/token-report")
		}
		in.planCoreSkill("token-report", kitTokenReport, false)
	}
	if o.MCP {
		wantMCP := jsonorder.NewObject().Set("type", "http").Set("url", in.url+"/mcp").Set("headers", jsonorder.NewObject().Set("X-Looptrack-Project", o.Project))
		if err := in.putMCP(".mcp.json", "mcpServers", wantMCP); err != nil {
			return err
		}
	}
	return nil
}

// addPermission は permissions.allow に looptrack の許可が無ければ足す（settings.json だけ。
// 手元専用の settings.local.json には足さない）。返り値は変えたか。
func addPermission(data *jsonorder.Object) bool {
	perms := data.Object("permissions")
	if perms == nil {
		perms = ensureObj(data, "permissions")
	}
	av, ok := perms.Get("allow")
	allow, isList := av.([]any)
	if ok && !isList {
		return false // allow が配列でない（手で変えた設定）は触らない
	}
	if containsStr(allow, goPermission) {
		return false
	}
	ensureObj(data, "permissions").Set("allow", append(allow, goPermission))
	return true
}

func containsStr(l []any, s string) bool {
	for _, x := range l {
		if v, ok := x.(string); ok && v == s {
			return true
		}
	}
	return false
}

func (in *installer) putMCP(rel, key string, wantMCP *jsonorder.Object) error {
	p := in.plan
	m, err := loadJSON(p, rel)
	if err != nil {
		return err
	}
	before, existed := beforeOf(m)
	servers := ensureObj(m, key)
	if cur, ok := servers.Get("looptrack"); ok && cur != nil && jsonorder.Compact(cur) != jsonorder.Compact(wantMCP) && !in.o.Force {
		p.Note(i18n.T(p.Lang, "kitinit.init.note.mcp_other", "file", rel, "key", key))
		return nil
	}
	servers.Set("looptrack", wantMCP)
	putJSON(p, rel, m, before, existed)
	return nil
}

func (in *installer) planCodex() error {
	p, o := in.plan, in.o
	rel := ".codex/hooks.json"
	data, err := loadJSON(p, rel)
	if err != nil {
		return err
	}
	before, existed := beforeOf(data)
	hooks := ensureObj(data, "hooks")
	w := in.wiring("codex")
	if !o.NoUsage && fixCodexUsageMatcher(hooks) {
		p.Note(i18n.T(p.Lang, "kitinit.init.note.codex_matcher", "file", rel, "matcher", codexMCPMatcher))
	}
	var loopWant []want
	if in.loopOn {
		loopWant, _ = loopWants(in.loop, w)
	}
	groupedSync(hooks, w, coreWants(w, o), loopWant, in.loopOn)
	putJSON(p, rel, data, before, existed)
	in.planCodexEnv()
	return nil
}

// fixCodexUsageMatcher は以前の init が PostToolUse に matcher "mcp" で配線した usage を codexMCPMatcher へ移す。
func fixCodexUsageMatcher(hooks *jsonorder.Object) bool {
	fixed := false
	v, _ := hooks.Get("PostToolUse")
	for _, g := range objList(v) {
		gobj := asObj(g)
		if gobj == nil || matcherOf(gobj) != "mcp" {
			continue
		}
		hv, _ := gobj.Get("hooks")
		hs, ok := hv.([]any)
		if !ok {
			continue
		}
		var others []any
		for _, h := range hs {
			cmd := cmdOf(asObj(h))
			if classify(cmd).id == "usage" {
				continue
			}
			others = append(others, h)
		}
		if len(others) == len(hs) {
			continue
		}
		if len(others) > 0 {
			gobj.Set("hooks", others)
		} else {
			gobj.Set("matcher", codexMCPMatcher)
		}
		fixed = true
	}
	return fixed
}

var tomlTable = regexp.MustCompile(`(?m)^\s*(\[\s*shell_environment_policy\b|shell_environment_policy\s*[.=])`)

// planCodexEnv は .codex/config.toml の [shell_environment_policy] の set に LOOPTRACK_API_URL / LOOPTRACK_PROJECT を書く
// （TOML は読まず、以前の CLI（1.0.0 より前）と同じく文字列で確かめる）。
func (in *installer) planCodexEnv() {
	p, o := in.plan, in.o
	section := fmt.Sprintf("[shell_environment_policy]\nset = { LOOPTRACK_API_URL = \"%s\", LOOPTRACK_PROJECT = \"%s\" }\n", in.url, o.Project)
	cur, _ := readFile(p.path(codexConfig))
	if !tomlTable.MatchString(cur) {
		next := ""
		if trimSpace(cur) != "" {
			next = strings.TrimRight(cur, "\n") + "\n\n"
		}
		p.Text(codexConfig, next+codexConfigNote+"\n"+section)
		return
	}
	ok := true
	for k, v := range map[string]string{"LOOPTRACK_API_URL": in.url, "LOOPTRACK_PROJECT": o.Project} {
		if !regexp.MustCompile(k + `\s*=\s*"` + regexp.QuoteMeta(v) + `"`).MatchString(cur) {
			ok = false
		}
	}
	if !ok {
		p.Note(i18n.T(p.Lang, "kitinit.init.note.codex_shell_env", "file", codexConfig, "url", in.url, "project", o.Project))
	}
}

func (in *installer) planCopilot() error {
	p, o := in.plan, in.o
	src := copilotHooks
	if !isFile(p.path(copilotHooks)) && isFile(p.path(legacyCopilotHooks)) {
		src = legacyCopilotHooks // 旧名の置き場の中身（利用者が足した hook を含む）を引き継いで、新しい置き場へ移す
	}
	data, err := loadJSON(p, src)
	if err != nil {
		return err
	}
	before, existed := beforeOf(data)
	if src != copilotHooks {
		before, existed = "", false // 新しい置き場には無い（必ず書く）
		p.Remove(legacyCopilotHooks)
	}
	if !data.Has("version") {
		data = &jsonorder.Object{Members: append([]jsonorder.Member{{Key: "version", Value: jsonorder.Number("1")}}, data.Members...)}
	}
	if v, ok := data.Get("hooks"); ok {
		if _, isObj := v.(*jsonorder.Object); !isObj {
			return i18n.Errorf("kitinit.init.err.hooks_not_object", "file", copilotHooks)
		}
	}
	hooks := ensureObj(data, "hooks")
	w := in.wiring("copilot")
	var loopWant []want
	if in.loopOn {
		loopWant, _ = loopWants(in.loop, w)
	}
	coreWant, loopWant := mergeCopilotSessionStart(w, coreWants(w, o), loopWant) // SessionStart を 1 本に
	flatSync(hooks, w, coreWant, loopWant, in.loopOn)
	if len(hooks.Keys()) == 0 {
		data.Delete("hooks")
	}
	putJSON(p, copilotHooks, data, before, existed)
	if hasClaudeHooks(p) || has(o.agents, "claude-code") {
		p.Note(i18n.T(p.Lang, "kitinit.init.note.copilot_reads_claude"))
	}
	if o.MCP {
		base := func() *jsonorder.Object {
			return jsonorder.NewObject().Set("type", "http").Set("url", in.url+"/mcp").Set("headers", jsonorder.NewObject().Set("X-Looptrack-Project", o.Project))
		}
		if err := in.putMCP(copilotVSCMCP, "servers", base()); err != nil {
			return err
		}
		if err := in.putMCP(copilotCLIMCP, "mcpServers", base().Set("tools", []any{"*"})); err != nil {
			return err
		}
	}
	return nil
}

func hasClaudeHooks(p *Plan) bool {
	for _, rel := range []string{".claude/settings.json", ".claude/settings.local.json"} {
		b, err := os.ReadFile(p.path(rel))
		if err != nil {
			continue
		}
		if o, err := jsonorder.DecodeObject(b); err == nil {
			if v, ok := o.Get("hooks"); ok && jsonorder.Truthy(v) {
				return true
			}
		}
	}
	return false
}

func (in *installer) planAgentsMD() {
	p, o := in.plan, in.o
	upsertBlock(p, "AGENTS.md", agentsMDBody(o.agents, in.url, o.Project))
	if in.loopOn {
		agent := "copilot"
		if has(o.agents, "codex") {
			agent = "codex"
		}
		body := loopAgentsText(in.files, in.loop, agent, in.c.Lang)
		upsertLoopBlock(p, "AGENTS.md", &body)
	}
}

// upsertBlock は CLAUDE.md / AGENTS.md の管理節を入れる・差し替える。
func upsertBlock(p *Plan, rel, body string) {
	cur, _ := readFile(p.path(rel))
	block := blockBegin + "\n" + body + blockEnd + "\n"
	begin, end := strings.Index(cur, blockBegin), strings.Index(cur, blockEnd)
	var next string
	switch {
	case begin != -1 && end > begin:
		next = cur[:begin] + block + strings.TrimPrefix(cur[end+len(blockEnd):], "\n")
	case strings.Contains(cur, legacyHead):
		p.Note(i18n.T(p.Lang, "kitinit.init.note.manual_section", "file", rel, "section", strings.TrimLeft(legacyHead, "# ")))
		return
	default:
		if trimSpace(cur) != "" {
			next = strings.TrimRight(cur, "\n") + "\n\n"
		}
		next += block
	}
	p.Text(rel, next)
}

func (in *installer) planKitJSON(prev, loopState *jsonorder.Object) {
	var agents []any
	prevAgents := map[string]bool{}
	if v, ok := prev.Get("agent"); ok {
		for _, a := range objList(v) {
			if s, ok := a.(string); ok {
				prevAgents[s] = true
			}
		}
	}
	for _, a := range agentsAll {
		if prevAgents[a] || has(in.o.agents, a) {
			agents = append(agents, a)
		}
	}
	coreSums := map[string]string{}
	for n, b := range in.files {
		if strings.HasPrefix(n, "kit/core/") {
			coreSums[n] = sha(b)
		}
	}
	var bundle any
	if len(coreSums) > 0 {
		bundle = bundleSHA(coreSums)
	}
	data := jsonorder.NewObject().Set("version", jsonorder.Number("1")).Set("url", in.url).Set("project", in.o.Project).Set("agent", agents).
		Set("source", in.source).
		Set("core", jsonorder.NewObject().Set("bundle_sha256", bundle).Set("files", walkObj(coreSums))).Set("loop", loopState)
	in.plan.Text(kitJSON, dumpJSON(data))
}

// readKitJSON は .claude/.looptrack-kit.json（無ければ空）。
func readKitJSON(root string) (*jsonorder.Object, error) {
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(kitJSON)))
	if err == nil {
		v, err := jsonorder.Decode(b)
		if err != nil {
			return nil, i18n.Errorf("kitinit.init.err.bad_kit_json", "file", kitJSON, "reason", err)
		}
		if o, ok := v.(*jsonorder.Object); ok {
			return o, nil
		}
		return jsonorder.NewObject(), nil
	}
	return jsonorder.NewObject(), nil
}

// ---------------------------------------------------------------- 小道具

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func sums(files map[string]string) map[string]string {
	out := make(map[string]string, len(files))
	for n, b := range files {
		out[n] = sha(b)
	}
	return out
}

// bundleSHA はサーバの bundleSHA256 と同じ（「名前 ハッシュ」の行を名前順に並べたものの SHA-256）。
func bundleSHA(files map[string]string) string {
	var b strings.Builder
	for _, n := range sortedKeys(files) {
		b.WriteString(n + " " + files[n] + "\n")
	}
	return sha(b.String())
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedObj(m map[string]string) *jsonorder.Object {
	o := jsonorder.NewObject()
	for _, k := range sortedKeys(m) {
		o.Set(k, m[k])
	}
	return o
}

// walkOrder はディレクトリごとにファイルを名前順に出してから、下のディレクトリを名前順にたどる並び。
// 以前の CLI（1.0.0 より前）と同じ並びにする（.looptrack-kit.json の files のキーの順）。
func walkOrder(names []string) []string {
	out := append([]string(nil), names...)
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.Split(out[i], "/"), strings.Split(out[j], "/")
		for k := 0; k < len(a) && k < len(b); k++ {
			aLast, bLast := k == len(a)-1, k == len(b)-1
			if a[k] == b[k] && !aLast && !bLast {
				continue
			}
			if aLast != bLast {
				return aLast // ファイルがディレクトリより先
			}
			return a[k] < b[k]
		}
		return len(a) < len(b)
	})
	return out
}

func walkObj(m map[string]string) *jsonorder.Object {
	o := jsonorder.NewObject()
	for _, k := range walkOrder(sortedKeys(m)) {
		o.Set(k, m[k])
	}
	return o
}

func strMap(o *jsonorder.Object) map[string]string {
	out := map[string]string{}
	if o == nil {
		return out
	}
	for _, k := range o.Keys() {
		out[k] = o.String(k)
	}
	return out
}

func truthy(o *jsonorder.Object, k string) bool {
	if o == nil {
		return false
	}
	v, _ := o.Get(k)
	return jsonorder.Truthy(v)
}

// jsonValue は encoding/json の値を jsonorder の値にする（manifest の version）。
func jsonValue(v any) any {
	switch x := v.(type) {
	case float64:
		if x == float64(int64(x)) {
			return jsonorder.Number(strconv.FormatInt(int64(x), 10))
		}
		return jsonorder.Number(jsonorder.FormatFloat(x))
	}
	return v
}

func stamp() string { return now().UTC().Format("2006-01-02T15:04:05Z") }

// planCoreSkill は kit/core の skill を .claude/skills/<名前>/SKILL.md に 1 本だけ置く。本文は導入時の言語で選ぶ
// （pickLang。EN なら en/ の訳、無ければ正本の日本語）。skill は AI のハーネスが固定のパスで読み、実行時に言語を
// 選ぶ主体がいないため、rules と違って日英の両方は置かない（言語を変えたら looptrack issue init を打ち直す）。
// 以前の init が併置した en/SKILL.md は、init の印があれば片付ける。
func (in *installer) planCoreSkill(name, kitName string, required bool) {
	body, ok := pickLang(in.files, kitName, in.plan.Lang)
	in.planCoreSkillFile(".claude/skills/"+name+"/SKILL.md", name, kitName, body, ok, required)
	in.cleanupCoreSkillTranslation(".claude/skills/" + name + "/" + kit.LangDir + "/SKILL.md")
}

// cleanupCoreSkillTranslation は以前の init が置いた訳（en/SKILL.md）を消す。印の無いもの（手で置いたもの）は残す。
func (in *installer) cleanupCoreSkillTranslation(rel string) {
	p := in.plan
	fp := p.path(rel)
	if !lexists(fp) || !insideReal(p, rel) {
		return
	}
	if cur, ok := readFile(fp); ok && isRegular(fp) && hasSkillMark(cur) {
		p.Remove(rel)
		return
	}
	p.Note(i18n.T(p.Lang, "kitinit.init.note.not_ours", "file", rel))
}

// planCoreSkillFile は skill の本文 1 本（skill。ok が false なら配布物に無い）を rel に置く。init が作ったもの
// （skillMark がある）だけを置き直し、手で置いたものは変えない。required でない skill は、サーバの配布物に無ければ
// 黙って置かない（古いサーバ）。
func (in *installer) planCoreSkillFile(rel, name, kitName, skill string, ok, required bool) {
	p := in.plan
	cur, isF := readFile(p.path(rel))
	switch {
	case !ok:
		if required {
			p.Note(i18n.T(p.Lang, "kitinit.init.note.skill_missing", "name", name, "kit", kitName))
		}
	case isF && !hasSkillMark(cur):
		p.Note(i18n.T(p.Lang, "kitinit.init.note.not_ours", "file", rel))
	case !writable(p, rel):
		p.Note(i18n.T(p.Lang, "kitinit.init.note.symlink_target", "file", rel))
	default:
		p.Text(rel, skill)
	}
}
