//go:build unix

package verify

import (
	"os"
	"os/exec"
	"syscall"
)

// findShell は PATH の bash、無ければ sh（sh も無ければ起動で失敗する）。
func findShell() (string, error) {
	if p, err := exec.LookPath("bash"); err == nil {
		return p, nil
	}
	return "sh", nil
}

// prepare は新しいプロセスグループで起動させる（グループごと止められる）。
func prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// group はプロセスグループ（番号は起動したシェルの PID）。
type group struct{ pgid int }

func attach(cmd *exec.Cmd) (tree, error) { return group{cmd.Process.Pid}, nil }

// signal はグループ全体に送る。既に誰もいない（ESRCH）・権限が無い（EPERM）ときは何もしない。
func (g group) signal(sig syscall.Signal) { _ = syscall.Kill(-g.pgid, sig) }

func (g group) terminate() { g.signal(syscall.SIGTERM) }
func (g group) kill()      { g.signal(syscall.SIGKILL) }
func (g group) close()     {}

// exitCode は終了コード。シグナルで終わったら 128 + 番号（シェルと同じ）。
func exitCode(st *os.ProcessState) int {
	if ws, ok := st.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return st.ExitCode()
}
