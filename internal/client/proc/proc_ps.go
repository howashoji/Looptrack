//go:build unix && !linux

package proc

import (
	"context"
	"os/exec"
	"time"
)

// list は ps を起動して読む（macOS・BSD など）。-ww でコマンド行を切り詰めない。
// -ww を知らない ps（一部の商用 unix）には bash 版と同じ引数で試し直す。
func list(ctx context.Context) ([]Proc, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-ww", "-axo", "pid,ppid,etime,command").Output()
	if err != nil || len(out) == 0 {
		out, err = exec.CommandContext(ctx, "ps", "-eo", "pid,ppid,etime,command").Output()
		if err != nil {
			return nil, err
		}
	}
	return ParsePS(string(out)), nil
}
