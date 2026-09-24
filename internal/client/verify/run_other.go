//go:build !unix && !windows

package verify

import (
	"os"
	"os/exec"
)

// プロセスグループも Job Object も無い OS（js/wasm・plan9 など。配布の対象外）。起動したシェルだけを止める。

func findShell() (string, error) {
	if p, err := exec.LookPath("bash"); err == nil {
		return p, nil
	}
	return "sh", nil
}

func prepare(*exec.Cmd) {}

type single struct{ p *os.Process }

func attach(cmd *exec.Cmd) (tree, error) { return single{cmd.Process}, nil }

func (s single) terminate() { _ = s.p.Kill() }
func (s single) kill()      { _ = s.p.Kill() }
func (s single) close()     {}

func exitCode(st *os.ProcessState) int { return st.ExitCode() }
