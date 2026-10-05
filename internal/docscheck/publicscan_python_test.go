package docscheck

import (
	"strings"
	"testing"
)

// pythonRemnantProbeFile・pythonRemnantProbeCache は「旧実装に固有の語」の見本。
//
// この _test.go も公開物として配られ、python_skip は _test.go を除外しない（ids_skip と違う点。実測で確かめた）。
// そのため文字列リテラルのまま書くと、この見本そのものが検査に当たって常時赤になる。
// publicScanProbeID と同じ理由で、1 つの文字列としては書かず組み立てる。
var (
	pythonRemnantProbeFile  = "issue" + ".py"
	pythonRemnantProbeCache = "__pycache" + "__"
)

// TestPublicScanPythonRemnant は「Python の名残」検査（deploy/public-scan.sh の python_left）が
// 旧 Python 実装に固有の語（ファイル名・環境変数名）だけを狙い、文書中の一般的な python への言及
// （語そのもの・python を含む URL・バージョン表記）には当たらないことを確かめる。
//
// 対照として、旧実装に固有の語（上の見本）は引き続き検出されることも同じテストで確かめる。
// 対照が無いと、検査そのものが死んでいても「一般的な言及は通る」側だけが緑になってしまう。
func TestPublicScanPythonRemnant(t *testing.T) {
	requirePublicScanTools(t)

	base := map[string]string{
		".gitattributes": "* text=auto eol=lf\n/private/ export-ignore\n",
		"README.md":      "# 見本\n\n公開物に入る文書。\n",
	}

	generic := []struct {
		name string
		line string
	}{
		{"一般的な言及", "外部の Python 製ツールと連携する場合がある。\n"},
		{"python を含む URL", "参考資料 https://python.org/downloads を見よ。\n"},
		{"バージョン表記", "Python3.12 の互換性に触れる。\n"},
	}
	for _, g := range generic {
		t.Run(g.name+"は通る", func(t *testing.T) {
			dir, env := publicScanRepo(t, base)
			appendFile(t, dir, "README.md", g.line)

			out, ok := runPublicScan(t, dir, env, "--only", "python")
			if !ok || !strings.Contains(out, "Python の名残: 0 行") {
				t.Errorf("一般的な python への言及で落ちました:\n%s", out)
			}
		})
	}

	t.Run("旧実装に固有の語（対照）は引き続き検出する", func(t *testing.T) {
		dir, env := publicScanRepo(t, base)
		appendFile(t, dir, "README.md", "対照: 旧実装の"+pythonRemnantProbeFile+"と"+pythonRemnantProbeCache+"はここに残っている。\n")

		out, ok := runPublicScan(t, dir, env, "--only", "python")
		if ok {
			t.Errorf("旧実装に固有の語（%s・%s）を見落としました:\n%s", pythonRemnantProbeFile, pythonRemnantProbeCache, out)
		}
		if !strings.Contains(out, "README.md:") {
			t.Errorf("落ちた行に README.md の該当行が出ていません:\n%s", out)
		}
	})
}
