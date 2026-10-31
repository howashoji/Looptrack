package docscheck

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestSignMacosStapleRetry は deploy/release/sign-macos.sh の staple_validate（stapler の試し直し）を
// 偽の xcrun で確かめる検査を go test から回す。本物の公証は呼ばない。
// Windows では skip する（bash のスクリプトなので、Windows のジョブに bash 依存を持ち込まない）。
func TestSignMacosStapleRetry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sign-macos_test.sh は bash のスクリプト（Windows のジョブでは走らせない）")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skipf("bash がありません: %v", err)
	}

	cmd := exec.Command("bash", filepath.Join("deploy", "release", "sign-macos_test.sh"))
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	t.Logf("bash deploy/release/sign-macos_test.sh:\n%s", out)
	if err != nil {
		t.Fatalf("sign-macos.sh の staple_validate の検査が落ちました（%v）", err)
	}
}
