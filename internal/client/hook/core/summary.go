package core

// SessionStart の summary（CLI の `summary --limit 12 --agent <AI>`、Copilot は `--hook-json` 付き）。
// 本文は `looptrack issue summary`（internal/client/cli）が作る。ここは読み取り専用で呼び、出力を additionalContext にする
// （AI ごとの形は hookio.Render。Copilot は --hook-json と同じくトップレベルと hookSpecificOutput の両方に置く）。
// SessionStart を止めない: CLI が失敗したら何も出さない（summary は 1 要求 2 秒で打ち切る）。

import (
	"bytes"
	"context"
	"io"
	"strconv"
	"strings"

	"github.com/howashoji/looptrack/internal/client/cli"
	"github.com/howashoji/looptrack/internal/hookio"
)

// summaryLimit は着手可能の表示件数の既定（init が書く配線の --limit 12）。
const summaryLimit = 12

// Summary は `looptrack hook summary --agent <AI> [--limit N]`。
func Summary(ctx context.Context, c *Call, ev hookio.Event) (hookio.Result, error) {
	limit := summaryLimit
	if v, ok := c.arg("--limit"); ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			return hookio.Result{}, err
		}
		limit = n
	}
	args := []string{"summary", "--limit", strconv.Itoa(limit)}
	if !ev.AgentGuessed {
		// 導入済みをサーバへ知らせる。配線に --agent が無い（推測した）ときは知らせない（CLI の --agent なしと同じ）
		args = append(args, "--agent", string(ev.Agent))
	}
	var out bytes.Buffer
	if code := cli.Main(args, cli.IO{Stdin: strings.NewReader(""), Stdout: &out, Stderr: io.Discard}, *c.Vars); code != 0 {
		return hookio.Result{}, nil // 以前の CLI も失敗したときは（--hook-json では）出力を捨てた
	}
	return hookio.Result{Context: strings.TrimSpace(out.String())}, nil
}
