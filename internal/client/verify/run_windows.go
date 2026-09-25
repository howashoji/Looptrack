//go:build windows

package verify

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/howashoji/looptrack/internal/i18n"
)

// Windows: 一時停止のまま起動し（CREATE_SUSPENDED）、Job Object に入れてから再開する。子孫は Job を引き継ぐので、
// 時間切れは TerminateJobObject で木ごと止まる（起動から Job に入るまでの間に子を作られることが無い）。
// 標準の syscall だけを使う（golang.org/x/sys は internal/server の検査が禁じる対象に含まれるため入れない）。
//
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE は付けない: 本体が普通に終わった後も残る背景のプロセスを止めない点を
// POSIX（プロセスグループは時間切れと出力の待ちすぎのときだけ止める）と揃える。

const (
	createSuspended       = 0x00000004
	createNewProcessGroup = 0x00000200 // 端末の Ctrl-C を受けない（POSIX の新しいプロセスグループと同じ）
)

var (
	kernel32                     = syscall.NewLazyDLL("kernel32.dll")
	procCreateJobObjectW         = kernel32.NewProc("CreateJobObjectW")
	procAssignProcessToJobObject = kernel32.NewProc("AssignProcessToJobObject")
	procTerminateJobObject       = kernel32.NewProc("TerminateJobObject")
	ntdll                        = syscall.NewLazyDLL("ntdll.dll")
	procNtResumeProcess          = ntdll.NewProc("NtResumeProcess")
)

func findShell() (string, error) {
	return WindowsShell(exec.LookPath, os.Getenv, func(p string) bool {
		fi, err := os.Stat(p)
		return err == nil && fi.Mode().IsRegular()
	})
}

func prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createSuspended | createNewProcessGroup}
}

// job は Job Object のハンドル。
type job struct{ h syscall.Handle }

// attach は一時停止中のプロセスを新しい Job に入れて再開する。
func attach(cmd *exec.Cmd) (tree, error) {
	h, _, e := procCreateJobObjectW.Call(0, 0)
	if h == 0 {
		return nil, errors.New(i18n.T(i18n.FromEnv(os.Getenv), "verify.run.job_create", "reason", e))
	}
	j := job{syscall.Handle(h)}
	var aerr error
	herr := cmd.Process.WithHandle(func(ph uintptr) {
		if r, _, e := procAssignProcessToJobObject.Call(uintptr(j.h), ph); r == 0 {
			aerr = errors.New(i18n.T(i18n.FromEnv(os.Getenv), "verify.run.job_assign", "reason", e))
			return
		}
		if st, _, _ := procNtResumeProcess.Call(ph); st != 0 {
			aerr = errors.New(i18n.T(i18n.FromEnv(os.Getenv), "verify.run.resume", "status", fmt.Sprintf("0x%08X", uint32(st))))
		}
	})
	if herr != nil {
		aerr = errors.New(i18n.T(i18n.FromEnv(os.Getenv), "verify.run.handle", "reason", herr))
	}
	if aerr != nil {
		j.kill()
		j.close()
		return nil, aerr
	}
	return j, nil
}

func (j job) terminate() { j.kill() } // Windows には木全体へ送れる穏やかな停止（SIGTERM）が無い
func (j job) kill()      { procTerminateJobObject.Call(uintptr(j.h), 1) }
func (j job) close()     { syscall.CloseHandle(j.h) }

func exitCode(st *os.ProcessState) int { return st.ExitCode() }
