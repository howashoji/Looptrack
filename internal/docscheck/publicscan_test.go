package docscheck

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestPublicScan は公開物の検査（deploy/public-scan.sh）を go test から回す。
//
// この検査は長いあいだ「手で走らせた人にしか見えない」状態だった。開発者向けの手順には
// 並んでいたが、go test にも CI にも入っていなかったので、公開物に社内固有の語や内部の番号が入っても
// 誰にも気づかれずに残った（実際に、0 件で閉じた直後に 7 行入った）。ここから呼ぶことで、
// 手元の go test ./... と CI の linux のテストの両方で落ちる（CI では checks のジョブからも直接走る）。
//
// Windows では skip する（bash のスクリプトなので、Windows のジョブに bash 依存を持ち込まない）。
// 版管理の外（tar で展開しただけの木）でも skip する。
func TestPublicScan(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("public-scan.sh は bash のスクリプト（Windows のジョブでは走らせない）")
	}
	for _, bin := range []string{"bash", "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s がありません: %v", bin, err)
		}
	}
	if err := exec.Command("git", "-C", repoRoot, "rev-parse", "--show-toplevel").Run(); err != nil {
		t.Skipf("git の作業ツリーではありません: %v", err)
	}

	cmd := exec.Command("bash", filepath.Join("deploy", "public-scan.sh"))
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	t.Logf("bash deploy/public-scan.sh:\n%s", out)
	if err != nil {
		t.Fatalf("公開物の検査が落ちました（%v）。上の一覧の区分と行を直す"+
			"（イシューの ID なら、経緯はイシューに書き、コードには理由だけを残す）", err)
	}
}
