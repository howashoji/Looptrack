package kitinit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/client/hook/core"
	"github.com/howashoji/looptrack/internal/client/hook/loop"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/i18n"
)

// 導入の後の自己診断（kit/loop/verify の bash の検査は Go のテスト＝internal/client/hook/loop に移ったので、
// 導入先ではその「導入先で確かめられる部分」を実行する）。1 つでも失敗したら init は書いたものを元に戻して止める。
//
//  1. 配線（verify-manifest 相当）: 書いた設定ファイルごとに、core と manifest の hook が 1 件ずつ、manifest どおりのイベント・
//     matcher・timeout・コマンド（looptrack hook <名前> --agent <AI>）で配線されている。looptrack hook の名前がすべて looptrack にあり、
//     --agent がその設定ファイルの AI と同じ。loop の配線に余分が無い。
//  2. 実行ファイル: 配線の looptrack が PATH（か絶対パス）で見つかり、`looptrack version` が動く。
//  3. hook の自己診断（verify-rules-inject 相当）: 置いた rules を looptrack の session-start-rules が読み、印の付いた節を
//     すべて文脈に入れる（loop を入れた AI ごと。読み取りだけで、導入先に状態を書かない）。
//
// 子プロセスと hook には利用者の IM_*・LOOPTRACK_*・CLAUDE_*・CODEX_*・COPILOT_* を渡さない（サーバに何も送らない）。
// ただし LOOPTRACK_LANG（表示の言語）だけは、doctor / verify が選んだ言語（lang）で渡し直す。表示の言語を
// 落とすのは cleanEnv の目的（利用者の接続設定と AI の印を落とすこと）に入っていない。落とすと、子（looptrack
// version）の出力が機械の LANG に従って親と食い違う。

// runCmd は子プロセスを起動する（テストで差し替える）。返り値は標準出力と終了コード。
var runCmd = func(ctx context.Context, dir string, env []string, name string, args ...string) (string, int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Env = dir, env
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out.String(), ee.ExitCode(), nil
	}
	if err != nil {
		return out.String(), -1, err
	}
	return out.String(), 0, nil
}

// cleanEnv は検査の子プロセスの環境（利用者の設定・AI の印を除く）。lang は親（doctor / verify）が
// 選んだ表示の言語で、LOOPTRACK_LANG として渡し直す（子の版の行が親と同じ言語で出るように）。
func cleanEnv(lang i18n.Lang, extra ...string) []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		u := strings.ToUpper(k)
		if strings.HasPrefix(u, "IM_") || strings.HasPrefix(u, "LOOPTRACK_") || strings.HasPrefix(u, "CLAUDE") ||
			strings.HasPrefix(u, "CODEX_") || strings.HasPrefix(u, "COPILOT_") {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "LOOPTRACK_LANG="+string(lang))
	return append(out, extra...)
}

type verifyReport struct {
	lang         i18n.Lang
	wires, rules int
	version      string
	fails        []string
}

// fail は失敗を 1 件足す（文面は呼ぶ側が i18n.T で作る）。
func (r *verifyReport) fail(text string) {
	r.fails = append(r.fails, text)
}

func (r *verifyReport) summary() string {
	if r.rules > 0 {
		return i18n.T(r.lang, "kitinit.verify.summary.rules", "wires", r.wires, "version", r.version, "rules", r.rules)
	}
	return i18n.T(r.lang, "kitinit.verify.summary", "wires", r.wires, "version", r.version)
}

// hookFile は AI の配線を書いた設定ファイル。
func (in *installer) hookFile(agent string) string {
	switch agent {
	case "codex":
		return ".codex/hooks.json"
	case "copilot":
		return copilotHooks
	}
	if in.bin.abs != "" {
		return ".claude/settings.local.json"
	}
	return ".claude/settings.json"
}

// placedEntry は設定ファイルの 1 件の hook（イベント・matcher・コマンド・timeout）。
type placedEntry struct {
	event, matcher, cmd string
	timeout             string
}

func readEntries(fp string, flat bool) ([]placedEntry, error) {
	b, err := os.ReadFile(fp)
	if err != nil {
		return nil, err
	}
	data, err := jsonorder.DecodeObject(b)
	if err != nil {
		return nil, err
	}
	hooks := data.Object("hooks")
	if hooks == nil {
		return nil, nil
	}
	var out []placedEntry
	for _, ev := range hooks.Keys() {
		v, _ := hooks.Get(ev)
		if flat {
			for _, h := range flatEntries(hooks, ev) {
				t, _ := h.Get("timeoutSec")
				out = append(out, placedEntry{ev, matcherOf(h), cmdOf(h), jsonorder.Str(t)})
			}
			continue
		}
		for _, g := range objList(v) {
			gobj := asObj(g)
			hv, _ := gobj.Get("hooks")
			for _, h := range objList(hv) {
				ho := asObj(h)
				t, _ := ho.Get("timeout")
				out = append(out, placedEntry{ev, matcherOf(gobj), cmdOf(ho), jsonorder.Str(t)})
			}
		}
	}
	return out, nil
}

func (in *installer) verify() *verifyReport {
	r := &verifyReport{lang: in.c.Lang}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// 1. 配線
	for _, agent := range in.o.agents {
		if agent == "other" {
			continue
		}
		w := in.wiring(agent)
		rel := in.hookFile(agent)
		entries, err := readEntries(in.plan.path(rel), agent == "copilot")
		if err != nil {
			r.fail(i18n.T(r.lang, "kitinit.verify.err.read", "file", rel, "reason", err))
			continue
		}
		coreW := coreWants(w, in.o)
		var loopW []want
		if in.loopOn {
			loopW, _ = loopWants(in.loop, w)
		}
		coreW, loopW = mergeCopilotSessionStart(w, coreW, loopW) // Copilot の SessionStart は 1 本
		for _, d := range coreW {
			ok := false
			for _, e := range entries {
				if e.event != d.Event || e.matcher != d.Matcher {
					continue
				}
				// 手で変えた配線（同じ hook の別の書き方）は残す約束なので、同じ hook があれば通す
				if c := classify(e.cmd); e.cmd == d.Cmd || (c.kind != kindOther && c.id == d.ID) {
					ok = true
				}
			}
			if !ok {
				r.fail(i18n.T(r.lang, "kitinit.verify.err.missing_core", "file", rel, "event", d.Event, "matcher", fmt.Sprintf("%q", d.Matcher), "cmd", d.Cmd))
			}
			r.wires++
		}
		for _, d := range loopW {
			n := 0
			for _, e := range entries {
				if e.event == d.Event && e.matcher == d.Matcher && e.cmd == d.Cmd && e.timeout == fmt.Sprint(d.Timeout) {
					n++
				}
			}
			if n != 1 {
				r.fail(i18n.T(r.lang, "kitinit.verify.err.loop_count", "file", rel, "id", d.ID, "event", d.Event,
					"matcher", fmt.Sprintf("%q", d.Matcher), "timeout", d.Timeout, "count", n))
			}
			r.wires++
		}
		nloop := 0
		for _, e := range entries {
			c := classify(e.cmd)
			if c.kind != kindNew {
				continue
			}
			m := newCmd.FindStringSubmatch(strings.TrimSpace(e.cmd))
			if _, ok := core.Lookup(c.id); !ok {
				if _, ok := loop.Lookup(c.id); !ok {
					r.fail(i18n.T(r.lang, "kitinit.verify.err.unknown_hook", "file", rel, "cmd", e.cmd))
				}
			}
			if m != nil && m[3] != agent {
				r.fail(i18n.T(r.lang, "kitinit.verify.err.agent_mismatch", "file", rel, "agent", agent, "cmd", e.cmd))
			}
			if c.loop {
				nloop++
			}
		}
		if in.loopOn && nloop != len(loopW) {
			r.fail(i18n.T(r.lang, "kitinit.verify.err.loop_extra", "file", rel, "count", nloop, "want", len(loopW)))
		}
		if !in.loopOn && nloop > 0 && !truthyLoop(in) {
			r.fail(i18n.T(r.lang, "kitinit.verify.err.loop_unwanted", "file", rel, "count", nloop))
		}
	}
	// 2. 実行ファイル
	bin := in.bin.abs
	if bin == "" {
		p, err := lookPath("looptrack")
		if err != nil {
			r.fail(i18n.T(r.lang, "kitinit.verify.err.not_in_path", "bindir", binDirHint()))
		}
		bin = p
	}
	if bin != "" {
		out, code, err := runCmd(ctx, in.target, cleanEnv(r.lang), bin, "version")
		switch {
		case err != nil:
			r.fail(i18n.T(r.lang, "kitinit.verify.err.version_run", "bin", bin, "reason", err))
		case code != 0 || !strings.HasPrefix(out, "looptrack "):
			r.fail(i18n.T(r.lang, "kitinit.verify.err.version_output", "bin", bin, "code", code, "out", fmt.Sprintf("%q", strings.TrimSpace(out))))
		default:
			r.version = strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
		}
	}
	// 3. rules の注入（置いた rules を hook が読む）
	if in.loopOn {
		for _, agent := range in.o.agents {
			if agent == "other" {
				continue
			}
			wants, _ := loopWants(in.loop, in.wiring(agent))
			wired := false
			for _, d := range wants {
				if d.ID == "session-start-rules" {
					wired = true
				}
			}
			if !wired {
				continue
			}
			in.verifyRules(ctx, agent, r)
		}
	}
	return r
}

// truthyLoop は以前から loop が入っているか（更新なしで残した配線を余分と数えない）。
func truthyLoop(in *installer) bool {
	prev, _ := readKitJSON(in.target)
	return truthy(prev.Object("loop"), "installed")
}

// verifyRules は session-start-rules を導入先で動かし、置いた rules の印の付いた節がすべて文脈に入るかを確かめる。
func (in *installer) verifyRules(ctx context.Context, agent string, r *verifyReport) {
	dir := in.plan.path(loopRulesDir)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return // 置けなかった（symlink を残した等）ときは hook が kit の rules を使う
	}
	var names []string
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".md") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	vars := map[string]string{}
	for _, kv := range cleanEnv(r.lang, "CLAUDE_PROJECT_DIR="+in.target) {
		k, v, _ := strings.Cut(kv, "=")
		vars[k] = v
	}
	// hook が読むのと同じ本文（EN なら en/ の訳）から見出しを取る。言語の判定も hook と同じ環境・同じ関数で行う
	// （cleanEnv は LOOPTRACK_* を落とすので、LC_ALL / LC_MESSAGES / LANG で決まる）。
	lang := i18n.FromEnv(func(k string) string { return vars[k] })
	var headings []string
	for _, n := range names {
		for _, c := range loop.RuleFilePaths(dir, n, lang) {
			b, err := os.ReadFile(c)
			if err != nil {
				continue
			}
			for _, s := range injectSections(string(b), "session", agent) {
				headings = append(headings, s.heading)
			}
			break
		}
	}
	if len(headings) == 0 {
		return
	}
	input, _ := json.Marshal(map[string]string{"session_id": "im-init-verify", "hook_event_name": "SessionStart", "cwd": in.target, "source": "startup"})
	var out, errb bytes.Buffer
	code := loop.Main(ctx, "session-start-rules", []string{"--agent", agent}, bytes.NewReader(input), &out, &errb,
		&loop.Env{Getenv: func(k string) string { return vars[k] }, Getwd: func() string { return in.target }})
	text := collectStrings(out.Bytes())
	var missing []string
	for _, h := range headings {
		if !strings.Contains(text, h) {
			missing = append(missing, h)
		}
	}
	if code != 0 || len(missing) > 0 {
		r.fail(i18n.T(r.lang, "kitinit.verify.err.rules_inject", "agent", agent, "code", code,
			"sections", strings.Join(missing, i18n.T(r.lang, "kitinit.list_sep"))))
		return
	}
	r.rules += len(headings)
}

// collectStrings は hook の出力（JSON）の文字列の値をつなげる（JSON でなければそのまま）。
func collectStrings(b []byte) string {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return string(b)
	}
	var sb strings.Builder
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case string:
			sb.WriteString(t + "\n")
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(v)
	return sb.String()
}
