package loop

// stop-runaway-background-process（kit/loop/hooks/stop-runaway-background-process.sh の Go 版）: セッションが起こした
// 「終わらない子プロセス」を検知して差し戻す。
//
// コーディング AI の本体（自分の祖先で claude / codex を名乗るもの。LOOPTRACK_LOOP_RUNAWAY_PARENT_PID で指定もできる）の子のうち、
// Bash ツールのシェル（`bash -c` / `zsh -c`・shell-snapshots を読むもの）で、閾値（LOOPTRACK_LOOP_RUNAWAY_THRESHOLD_MIN。旧名
// RUNAWAY_THRESHOLD_MIN。既定 30 分）より長く生きているものを最大 5 件示す。正当に長時間動くもの（MCP・npm exec・docker・
// エディタ・dev サーバ・--watch 等。LOOPTRACK_LOOP_RUNAWAY_ALLOW で足す）は除く。本体が見つからない・一覧が取れないときは何もしない。
//
// 形別の閾値: コマンドが**上限の無い待ちループ**の形（waitloop.go の unboundedWaitLoops。until / while + sleep で上限の式が無い）
// なら、閾値は 30 分ではなく短い方（LOOPTRACK_LOOP_RUNAWAY_LOOP_THRESHOLD_MIN。既定 10 分）を使う。形に当たらないものは今までどおり。
// 2026-09-20 に作られた 3 本はいずれも 30 分に達する前に人が ps で見つけたもので、この hook では捕まらなかった。
// pre-tool-wait-loop-guard が起動前に止めるが、変数展開などで形を読めなかったものの受け皿がここになる。
// 同じ本体を SubagentStop にも配線してある（subagent-stop-runaway-background-process。子が終わった直後に見る）。
//
// bash 版との違い: プロセスの一覧は internal/client/proc が OS ごとに取る（Linux は /proc、macOS などは ps、Windows は
// ToolHelp32）。LOOPTRACK_LOOP_RUNAWAY_PS_FILE（ps の出力の差し替え）はテスト用に残す。Windows のために、本体とシェルの名前は
// 「\」区切りのパス・引用符で囲んだパス・.exe 付き（claude.exe・bash.exe -c）も認める（unix の判定は変えない）。

import (
	"context"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/howashoji/looptrack/internal/client/proc"
	"github.com/howashoji/looptrack/internal/hookio"
	"github.com/howashoji/looptrack/internal/i18n"
)

var (
	agentHeadRe  = regexp.MustCompile(`(?i)(^|/)(claude|codex)$`)
	agentAppRe   = regexp.MustCompile(`/(Claude|Codex)(\.app/|$)`)
	agentCmdRe   = regexp.MustCompile(`(^|[/ ])(claude|codex)(\.js|\.mjs)?( |$)`)
	agentExeRe   = regexp.MustCompile(`(?i)(^|[/\\])(claude|codex)\.exe$`)
	shellRe      = regexp.MustCompile(`^(/bin/|/usr/bin/|/usr/local/bin/|/opt/homebrew/bin/)?(ba|z)sh -c`)
	shellExeRe   = regexp.MustCompile(`(?i)(^|[/\\])(ba|z)sh\.exe$`)
	runawayAllow = regexp.MustCompile(`(mcp|npm exec|docker|dockerd|Visual Studio Code|Cursor|next dev|vite|webpack serve|nodemon|--watch|` +
		`(npm|pnpm|yarn) (run )?(dev|start|serve))`)
	evalInnerRe = regexp.MustCompile(`eval '(.*)'`)
)

// cmdHead はコマンド行の最初の語（"…" で囲まれていればその中身。Windows のパスは空白を含む）。
func cmdHead(cmd string) (head, rest string) {
	if strings.HasPrefix(cmd, `"`) {
		if i := strings.Index(cmd[1:], `"`); i >= 0 {
			return cmd[1 : i+1], strings.TrimLeft(cmd[i+2:], " \t")
		}
	}
	f := strings.Fields(cmd)
	if len(f) == 0 {
		return "", ""
	}
	i := strings.Index(cmd, f[0]) + len(f[0])
	return f[0], strings.TrimLeft(cmd[i:], " \t")
}

// isAgentProc はコーディング AI の本体か。
func isAgentProc(cmd string) bool {
	f := strings.Fields(cmd)
	head := ""
	if len(f) > 0 {
		head = f[0]
	}
	if agentHeadRe.MatchString(head) || agentAppRe.MatchString(cmd) || agentCmdRe.MatchString(cmd) {
		return true
	}
	h, _ := cmdHead(cmd)
	return agentExeRe.MatchString(h)
}

// isToolShell は Bash ツールのために起こしたシェルか。
func isToolShell(cmd string) bool {
	if strings.Contains(cmd, "shell-snapshots") || shellRe.MatchString(cmd) {
		return true
	}
	h, rest := cmdHead(cmd)
	return shellExeRe.MatchString(h) && (rest == "-c" || strings.HasPrefix(rest, "-c "))
}

// StopRunawayBackgroundProcess は終わらない子プロセスを検知して差し戻す。
func StopRunawayBackgroundProcess(ctx context.Context, ev hookio.Event) (hookio.Result, error) {
	e := envFrom(ctx)
	if ev.StopHookActive {
		return hookio.Result{}, nil
	}
	lang := e.lang()
	threshold := 30
	if v := e.env("LOOPTRACK_LOOP_RUNAWAY_THRESHOLD_MIN"); v != "" {
		threshold = atoiDefault(v, 30)
	} else if v := e.env("RUNAWAY_THRESHOLD_MIN"); v != "" {
		threshold = atoiDefault(v, 30)
	}
	loopThreshold := 10
	if v := e.env("LOOPTRACK_LOOP_RUNAWAY_LOOP_THRESHOLD_MIN"); v != "" {
		loopThreshold = atoiDefault(v, 10)
	}
	if loopThreshold > threshold {
		loopThreshold = threshold // 閾値を下げた指定を、形別の閾値が上書きしない
	}

	var procs []proc.Proc
	if f := e.env("LOOPTRACK_LOOP_RUNAWAY_PS_FILE"); f != "" && isFile(e.abs(f)) {
		b, err := os.ReadFile(e.abs(f))
		if err != nil {
			return hookio.Result{}, nil
		}
		procs = proc.ParsePS(string(b))
	} else {
		ps, err := e.Procs(ctx)
		if err != nil {
			return hookio.Result{}, nil
		}
		procs = append([]proc.Proc(nil), ps...)
		sort.Slice(procs, func(i, j int) bool { return procs[i].PID < procs[j].PID }) // ps と同じ PID の順
	}
	if len(procs) == 0 {
		return hookio.Result{}, nil
	}
	byPID := map[int]proc.Proc{}
	for _, p := range procs {
		byPID[p.PID] = p
	}

	parent, havParent := 0, false
	if v := trimSpace(e.env("LOOPTRACK_LOOP_RUNAWAY_PARENT_PID")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return hookio.Result{}, nil // bash 版は数でない値に一致する行が無く、何も出さない
		}
		parent, havParent = n, true
	} else {
		// 自分の祖先をたどり、コーディング AI の本体（claude / codex を名乗るもの）を探す
		pid, seen := e.SelfPID, map[int]bool{}
		for {
			p, ok := byPID[pid]
			if !ok || seen[pid] {
				break
			}
			seen[pid] = true
			if isAgentProc(p.Command) {
				parent, havParent = pid, true
				break
			}
			pid = p.PPID
		}
	}
	if !havParent {
		return hookio.Result{}, nil
	}

	extra := compileUser(e.env("LOOPTRACK_LOOP_RUNAWAY_ALLOW"))
	type hit struct {
		pid, mins int
		cmd       string
		loop      bool // 上限の無い待ちループの形（短い閾値で拾ったもの）
	}
	var hits []hit
	for _, p := range procs {
		if p.PPID != parent || !isToolShell(p.Command) {
			continue
		}
		if runawayAllow.MatchString(p.Command) || (extra != nil && extra.MatchString(p.Command)) {
			continue
		}
		if !p.ElapsedOK {
			continue
		}
		shown := p.Command
		if m := evalInnerRe.FindStringSubmatch(p.Command); m != nil {
			shown = m[1]
		}
		loopShape := len(unboundedWaitLoops(shown)) > 0
		limit := threshold
		if loopShape {
			limit = loopThreshold
		}
		mins := p.Minutes()
		if mins < limit {
			continue
		}
		hits = append(hits, hit{p.PID, mins, headStr(shown, 160), loopShape})
	}
	if len(hits) == 0 {
		return hookio.Result{}, nil
	}
	var lines []string
	for i, h := range hits {
		if i >= 5 {
			break
		}
		if h.loop {
			lines = append(lines, i18n.T(lang, "loop.runaway.item_loop", "pid", h.pid, "mins", h.mins, "cmd", h.cmd, "limit", loopThreshold))
		} else {
			lines = append(lines, i18n.T(lang, "loop.runaway.item", "pid", h.pid, "mins", h.mins, "cmd", h.cmd))
		}
	}
	reason := i18n.T(lang, "loop.runaway.reason", "threshold", threshold, "list", strings.Join(lines, "\n"))
	// LOOPTRACK_LOOP_NO_BLOCK=1（--no-block）のときは hookio.Render が systemMessage に回す
	return hookio.Result{Block: reason}, nil
}

func atoiDefault(s string, def int) int {
	n, err := strconv.Atoi(trimSpace(s))
	if err != nil {
		return def
	}
	return n
}
