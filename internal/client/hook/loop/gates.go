package loop

// gates（以前の kit/loop/scripts/gates.sh の Go 版）: 検証ゲートの実行ラッパ（rules の iteration-discipline.md §1）。
//
//	looptrack gates                      # 全段（LOOPTRACK_LOOP_GATES_STAGES。既定 build lint test）
//	looptrack gates test                 # 指定した段だけ
//	looptrack gates -C app build test    # Makefile のディレクトリ（既定 LOOPTRACK_LOOP_GATES_DIR か .）
//
// `make -C <ディレクトリ> <段>` を順に実行し、失敗したら以降の段は実行しない（fail-fast）。各段のログは <ディレクトリ>/.gates/<段>.log。
// 指定した段はすべて必須（Makefile に無い段・レシピの無い段は失敗。make は レシピの無い段に exit 0 を返す＝偽緑になるため）。
// 終了コード: 0 = 全段が緑 / 1 = 失敗あり / 2 = Makefile が無い。出力の文言は以前の bash 版と同じ。

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/howashoji/looptrack/internal/i18n"
)

// Gates は検証ゲートを実行して終了コードを返す。env が nil なら実際の環境。
func Gates(ctx context.Context, args []string, stdout io.Writer, env *Env) int {
	if env == nil {
		env = &Env{}
	}
	e := env.withDefaults()
	root := e.env("CLAUDE_PROJECT_DIR")
	if root == "" {
		root = gitTop(e.Getwd())
	}
	if root == "" {
		root = e.Getwd()
	}
	dir := e.env("LOOPTRACK_LOOP_GATES_DIR")
	if dir == "" {
		dir = "."
	}
	var stages []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-C":
			dir = "."
			if i+1 < len(args) && args[i+1] != "" {
				dir = args[i+1]
			}
			i++
		case strings.HasPrefix(a, "-C"):
			dir = a[2:]
		default:
			stages = append(stages, a)
		}
	}
	if !strings.HasPrefix(dir, "/") && !filepath.IsAbs(dir) {
		dir = root + "/" + dir
	}
	if len(stages) == 0 {
		st := e.env("LOOPTRACK_LOOP_GATES_STAGES")
		if st == "" {
			st = "build lint test"
		}
		stages = strings.Fields(st)
	}
	logDir := dir + "/.gates"
	shown := strings.TrimPrefix(dir, root+"/")
	lang := e.lang()
	// 文面は対訳表から来る（% を含むことがある）ので、書式ではなくそのまま書き出す
	out := func(s string) { io.WriteString(stdout, s) }

	if !isFile(dir + "/Makefile") {
		out(i18n.T(lang, "loop.gates.no_makefile", "dir", dir) + "\n")
		return 2
	}
	environ := e.Environ()
	// stageState は ok（レシピがある）/ norecipe（ターゲットはあるがレシピが無い）/ missing（ターゲットが無い）。
	// make はレシピの無いターゲットに「Nothing to be done」を出して exit 0 を返すため、LC_ALL=C で文言を固定して見分ける。
	stageState := func(st string) string {
		cmd := exec.CommandContext(ctx, "make", "-C", dir, "-n", st)
		cmd.Env = append(append([]string(nil), environ...), "LC_ALL=C")
		b, err := cmd.CombinedOutput()
		switch {
		case err != nil:
			return "missing"
		case bytes.Contains(b, []byte("Nothing to be done")):
			return "norecipe"
		}
		return "ok"
	}

	_ = os.MkdirAll(logDir, 0o777)
	fail := false
	var summary []string
	for _, st := range stages {
		switch stageState(st) {
		case "missing":
			summary = append(summary, i18n.T(lang, "loop.gates.no_target", "stage", st))
			fail = true
		case "norecipe":
			summary = append(summary, i18n.T(lang, "loop.gates.no_recipe", "stage", st))
			fail = true
		}
		if fail {
			break
		}
		logPath := logDir + "/" + st + ".log"
		rc := runToLog(ctx, environ, logPath, "make", "-C", dir, st)
		if rc == 0 {
			summary = append(summary, fmt.Sprintf("✓ %s: PASS", st))
			continue
		}
		summary = append(summary, i18n.T(lang, "loop.gates.fail",
			"stage", st, "code", rc, "log", fmt.Sprintf("%s/.gates/%s.log", shown, st)))
		out(i18n.T(lang, "loop.gates.fail_log", "stage", st) + "\n")
		if b, err := os.ReadFile(logPath); err == nil {
			stdout.Write(tailLines(b, 40))
		}
		out("────────────────────────────────\n")
		fail = true
		break
	}

	out("\n")
	out(i18n.T(lang, "loop.gates.summary",
		"time", e.Now().Local().Format("2006-01-02 15:04"), "stages", strings.Join(stages, " ")) + "\n")
	for _, l := range summary {
		out(l + "\n")
	}
	if fail {
		out("\n")
		out(i18n.T(lang, "loop.gates.fail_advice") + "\n")
		return 1
	}
	out(i18n.T(lang, "loop.gates.pass_advice") + "\n")
	return 0
}

// runToLog はコマンドの標準出力と標準エラーをログのファイルに書き、終了コードを返す（起動できなければ 127）。
func runToLog(ctx context.Context, environ []string, logPath, name string, args ...string) int {
	f, err := os.Create(logPath)
	if err != nil {
		return 1
	}
	defer f.Close()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = environ
	cmd.Stdout, cmd.Stderr = f, f
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() > 0 {
			return ee.ExitCode()
		}
		return 127
	}
	return 0
}

// tailLines は tail -n の出力（最後の n 行。最後の行に改行が無ければそのまま）。
func tailLines(b []byte, n int) []byte {
	end := len(b)
	if end > 0 && b[end-1] == '\n' {
		end--
	}
	count := 0
	for i := end - 1; i >= 0; i-- {
		if b[i] == '\n' {
			count++
			if count == n {
				return b[i+1:]
			}
		}
	}
	return b
}
