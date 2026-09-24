//go:build linux

package proc

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// clockTicks は /proc/<pid>/stat の starttime の単位（USER_HZ）。cgo なしでは sysconf(_SC_CLK_TCK) を引けないが、
// Linux の全アーキテクチャで 100 に固定されている（カーネルが USER_HZ に換算して出す）。
const clockTicks = 100

// list は /proc を読む（プロセスを起動しない）。
func list(ctx context.Context) ([]Proc, error) {
	return listProc(ctx, "/proc")
}

func listProc(ctx context.Context, root string) ([]Proc, error) {
	up, err := os.ReadFile(filepath.Join(root, "uptime"))
	if err != nil {
		return nil, err
	}
	f := strings.Fields(string(up))
	if len(f) == 0 {
		return nil, ErrUnsupported
	}
	uptime, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []Proc
	for _, e := range ents {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		p, ok := readProc(filepath.Join(root, e.Name()), pid, uptime)
		if ok {
			out = append(out, p)
		}
	}
	return out, nil
}

// readProc は 1 つのプロセスを読む（読んでいる間に終わったものは ok = false）。
func readProc(dir string, pid int, uptime float64) (Proc, bool) {
	stat, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		return Proc{}, false
	}
	cmd, _ := os.ReadFile(filepath.Join(dir, "cmdline"))
	return parseProcStat(pid, stat, cmd, uptime, clockTicks)
}
