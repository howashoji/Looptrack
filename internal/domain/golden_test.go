package domain

import (
	"encoding/json"
	"os"
	"sync"
	"testing"
)

// goldenValue は以前の CLI（1.0.0 より前・ファイルモード。撤去済み）の出力の記録（testdata/golden.json）を返す。
// キーはテスト名と呼び出し（引数・読んだファイル）。記録に無ければ失敗にする。
func goldenValue(t *testing.T, key string) string {
	t.Helper()
	k := t.Name() + " | " + key
	goldenRec.Lock()
	defer goldenRec.Unlock()
	if goldenRec.m == nil {
		b, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, &goldenRec.m); err != nil {
			t.Fatal(err)
		}
	}
	v, ok := goldenRec.m[k]
	if !ok {
		t.Fatalf("以前の CLI の記録（%s）に無い: %s", goldenPath, k)
	}
	return v
}

const goldenPath = "testdata/golden.json"

var goldenRec struct {
	sync.Mutex
	m map[string]string
}
