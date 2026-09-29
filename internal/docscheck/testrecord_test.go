package docscheck

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestTestRecordParse は deploy/dev/test-record.sh の集計（--parse）と並走の数え方（--count-ps）を
// 合成のログと ps の出力で確かめる deploy/dev/test-record_test.sh を go test から回す。
// シェルのスクリプトだけだと手で走らせた人にしか結果が見えないので、go test から呼ぶ。
// go test も DB も使わない（全検査そのものは回さない）。
//
// Windows では skip する（bash のスクリプトなので、Windows のジョブに bash 依存を持ち込まない）。
func TestTestRecordParse(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test-record_test.sh は bash のスクリプト（Windows のジョブでは走らせない）")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skipf("bash がありません: %v", err)
	}

	cmd := exec.Command("bash", filepath.Join("deploy", "dev", "test-record_test.sh"))
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	t.Logf("bash deploy/dev/test-record_test.sh:\n%s", out)
	if err != nil {
		t.Fatalf("test-record.sh の集計の検査が落ちました（%v）", err)
	}
}
