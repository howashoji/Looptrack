package docscheck

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
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
	// 0 件で緑になる形を塞ぐ: 書き出した木のファイルと、調べた相対リンクの本数に下限を置く
	// （下限は実物（2026-09 にファイル 1,400 件ほど・相対リンク 268 本）の半分より下）。
	for _, c := range []struct {
		label string
		re    *regexp.Regexp
		min   int
	}{
		{"書き出した公開物のファイル", regexp.MustCompile(`(?m)^== 書き出した公開物のファイル: (\d+) 件$`), 500},
		{"調べた相対リンク", regexp.MustCompile(`(?m)^== 調べた相対リンク: (\d+) 本$`), 100},
	} {
		m := c.re.FindStringSubmatch(string(out))
		if m == nil {
			t.Errorf("出力に「%s」の行がありません", c.label)
			continue
		}
		if n, _ := strconv.Atoi(m[1]); n < c.min {
			t.Errorf("%s が %d しかありません（%d 以上のはず。走査か式が空振りしています）", c.label, n, c.min)
		}
	}
}

// TestPublicScanEmptyTreeAndLinks は、public-scan.sh が「何も調べずに緑」にならないことを使い捨てのリポジトリで確かめる。
//
//   - 書き出した木が空（全部を export-ignore にした）なら、語の検査に入る前に落ちる。
//   - リンクの検査は、壊れたリンクを見つける（検出できる側の対照）と同時に、調べた本数を出す。
func TestPublicScanEmptyTreeAndLinks(t *testing.T) {
	requirePublicScanTools(t)

	t.Run("木が空なら落ちる", func(t *testing.T) {
		dir, env := publicScanRepo(t, map[string]string{
			".gitattributes": "* export-ignore\n",
			"README.md":      "# 見本\n",
		})
		out, ok := runPublicScan(t, dir, env, "--head", "--only", "company")
		if ok {
			t.Errorf("書き出した木が空なのに緑になりました:\n%s", out)
		}
		if !strings.Contains(out, "== 書き出した公開物のファイル: 0 件") {
			t.Errorf("書き出したファイルの件数（0 件）が出ていません:\n%s", out)
		}
	})

	t.Run("壊れたリンクを見つけ、調べた本数を出す", func(t *testing.T) {
		dir, env := publicScanRepo(t, map[string]string{
			".gitattributes": "* text=auto eol=lf\n",
			"README.md":      "# 見本\n\n[ある](docs/a.md) と [無い](docs/missing.md) と [外](https://example.invalid/x)。\n",
			"docs/a.md":      "# a\n",
		})
		out, ok := runPublicScan(t, dir, env, "--head", "--only", "links")
		if ok {
			t.Errorf("壊れたリンクを見落としました:\n%s", out)
		}
		if !strings.Contains(out, "README.md:3: リンク先が公開物にありません: docs/missing.md") {
			t.Errorf("壊れたリンクの行が出ていません:\n%s", out)
		}
		if !strings.Contains(out, "== 調べた相対リンク: 2 本") {
			t.Errorf("調べた相対リンクの本数（2 本。外部の URL は数えない）が出ていません:\n%s", out)
		}
	})
}
