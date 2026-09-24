//go:build windows

package verify

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Windows の打ち切り（Job Object）の確かめ。macOS からはクロスビルドまで（GOOS=windows go test -c）。
// 実機・CI で go test ./internal/client/verify を流す。

const (
	synchronize         = 0x00100000
	waitObject0         = 0
	errInvalidParameter = syscall.Errno(87)
	bom                 = "\xef\xbb\xbf" // Set-Content が付けることがある
)

func shortGraces(t *testing.T) {
	t.Helper()
	tg, pg, kg := termGrace, pipeGrace, killGrace
	termGrace, pipeGrace, killGrace = 500*time.Millisecond, 1500*time.Millisecond, 1000*time.Millisecond
	t.Cleanup(func() { termGrace, pipeGrace, killGrace = tg, pg, kg })
}

// gone は Windows の PID のプロセスが within の間に終わるか。
func gone(t *testing.T, pid int, within time.Duration) bool {
	t.Helper()
	h, err := syscall.OpenProcess(synchronize, false, uint32(pid))
	if err != nil {
		return err == errInvalidParameter // もう無い
	}
	defer syscall.CloseHandle(h)
	ev, _ := syscall.WaitForSingleObject(h, uint32(within/time.Millisecond))
	return ev == waitObject0
}

func killPID(pid int) {
	if p, err := os.FindProcess(pid); err == nil {
		p.Kill()
	}
}

func readPID(t *testing.T, path string, within time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		if b, err := os.ReadFile(path); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(string(b), bom))); err == nil {
				return n
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("pid のファイルができない: %s", path)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func powershell(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("powershell.exe が無い")
	}
	return p
}

// シェルに依らない確かめ: PowerShell（-c は -Command）の Start-Process で作った孫も、時間切れで Job ごと止まる。
func TestJobTimeoutKillsDescendantsPowerShell(t *testing.T) {
	shortGraces(t)
	ps := powershell(t)
	dir := t.TempDir()
	cmd := `$p = Start-Process -PassThru -WindowStyle Hidden -FilePath ping.exe -ArgumentList '-n','120','127.0.0.1'; ` +
		`Set-Content -Path pid -Value $p.Id; Start-Sleep -Seconds 120`
	start := time.Now()
	// CI の runner では PowerShell の起動と Start-Process に 8 秒を超えることがある（2026-09-19 の run 35392739759）。
	// 時間切れが孫の起動より先に来ると確かめにならないので、起動に足りる長さにする
	r := runWith(ps, cmd, dir, os.Environ(), 30*time.Second)
	if r.Status != StatusTimeout || r.ExitCode != nil {
		t.Fatalf("%+v", r)
	}
	if el := time.Since(start); el > 40*time.Second {
		t.Errorf("時間切れから戻るまでが長い: %v", el)
	}
	pid := readPID(t, filepath.Join(dir, "pid"), time.Second)
	if !gone(t, pid, 3*time.Second) {
		t.Errorf("孫 %d（ping）が残っている", pid)
		killPID(pid)
	}
}

func TestJobExitCodePowerShell(t *testing.T) {
	r := runWith(powershell(t), "Write-Output ok; exit 3", t.TempDir(), os.Environ(), 60*time.Second)
	if r.Status != StatusFail || r.ExitCode == nil || *r.ExitCode != 3 || !strings.Contains(r.OutputTail, "ok") {
		t.Errorf("%+v", r)
	}
}

// Git for Windows の bash があれば、既定のシェルでも確かめる（MSYS の PID は /proc/<pid>/winpid で Windows の PID にする）。
func TestJobTimeoutKillsDescendantsBash(t *testing.T) {
	shortGraces(t)
	if _, err := Shell(); err != nil {
		t.Skipf("bash が無い: %v", err)
	}
	dir := t.TempDir()
	r := RunCommand(`sleep 120 & cat /proc/$!/winpid > pid; echo started; sleep 120`, dir, os.Environ(), 5*time.Second)
	if r.Status != StatusTimeout || !strings.Contains(r.OutputTail, "started") {
		t.Fatalf("%+v", r)
	}
	pid := readPID(t, filepath.Join(dir, "pid"), time.Second)
	if !gone(t, pid, 3*time.Second) {
		t.Errorf("背景の sleep %d が残っている", pid)
		killPID(pid)
	}
}

func TestRunCommandBasicsBash(t *testing.T) {
	if _, err := Shell(); err != nil {
		t.Skipf("bash が無い: %v", err)
	}
	r := RunCommand("echo ok; echo $LOOPTRACK_VERIFY_ID; exit 4", t.TempDir(), Env(os.Environ(), "TST-0001"), 60*time.Second)
	if r.Status != StatusFail || r.ExitCode == nil || *r.ExitCode != 4 || r.OutputTail != "ok\nTST-0001\n" {
		t.Errorf("%+v", r)
	}
}

func TestRunCommandLaunchFailure(t *testing.T) {
	r := runWith(filepath.Join(t.TempDir(), "no-such-shell.exe"), "echo x", t.TempDir(), os.Environ(), time.Second)
	if r.Status != StatusFail || r.ExitCode != nil || !strings.HasPrefix(r.OutputTail, "起動できません: ") {
		t.Errorf("%+v", r)
	}
}
