package mdformat

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/howashoji/looptrack/internal/testdata"
)

// TestFixturesRoundTrip は合成フィクスチャ全件の往復一致を検査する（実データは合成フィクスチャに置き換えてある）。
//
// 読み込み元は internal/testdata/fixtures（旧データで見つかった形を含む。何が入っているかは internal/transfer の
// TestFixturesCoverage が確かめる）。
func TestFixturesRoundTrip(t *testing.T) {
	root := testdata.Root(t)
	files, err := CollectIssueFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("イシューファイルがありません: %s", root)
	}
	var stats Stats
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := Parse(string(raw))
		if err != nil {
			rel, _ := filepath.Rel(root, path)
			t.Errorf("%s: %v", rel, err)
			continue
		}
		stats.Add(doc)
	}
	t.Logf("files=%d %s", len(files), stats)
}
