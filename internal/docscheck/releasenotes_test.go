package docscheck

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestReleaseNotesScript は deploy/release/release-notes_test.sh（2 本の CHANGELOG から GitHub の Release の本文を
// 作るスクリプトの検査）を go test から回す。release.yml の公開の段でしか動かないスクリプトなので、手元の go test ./... と
// CI の linux のテストで落ちるようにする。実物の CHANGELOG.md・CHANGELOG-desktop.md のどの版からも本文を作れることも見る。
//
// Windows では skip する（bash のスクリプトなので、Windows のジョブに bash 依存を持ち込まない）。
func TestReleaseNotesScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("release-notes_test.sh は bash のスクリプト（Windows のジョブでは走らせない）")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skipf("bash がありません: %v", err)
	}

	cmd := exec.Command("bash", filepath.Join("deploy", "release", "release-notes_test.sh"))
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	t.Logf("bash deploy/release/release-notes_test.sh:\n%s", out)
	if err != nil {
		t.Fatalf("Release の本文を作るスクリプトの検査が落ちました（%v）", err)
	}
}
