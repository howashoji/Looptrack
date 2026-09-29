package docscheck

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestDesktopVersionHelpers は deploy/release/desktop.sh の plist_version・iss_version を
// go test から回す。シェルのスクリプトだけだと手で走らせた人にしか結果が見えないので、
// go test から呼ぶ。これで手元の go test ./... と CI の linux のテストの両方で落ちる。
//
// Windows では skip する（bash のスクリプトなので、Windows のジョブに bash 依存を持ち込まない）。
func TestDesktopVersionHelpers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("desktop_version_test.sh は bash のスクリプト（Windows のジョブでは走らせない）")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skipf("bash がありません: %v", err)
	}

	cmd := exec.Command("bash", filepath.Join("deploy", "release", "desktop_version_test.sh"))
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	t.Logf("bash deploy/release/desktop_version_test.sh:\n%s", out)
	if err != nil {
		t.Fatalf("desktop.sh の plist_version / iss_version の検査が落ちました（%v）", err)
	}
}
