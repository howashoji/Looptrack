package usagesnap

// 以前の CLI（1.0.0 より前・撤去した）の結果の golden。
//
// その撤去の前に、差分テスト（snap・diffMany）が以前の CLI に流した呼び出しと、その結果を
// testdata/golden/<テスト名>.json に記録した。今は Go 版の結果をこの記録と文字列で比べる（キーの順・数の形まで）。
// 合成の会話記録は各テストが固定の値・固定の種の乱数で作り直すので、記録には呼び出し（正規化したもの）と結果だけを置く。
//
// 正規化: 一時ディレクトリ（t.TempDir）の場所を <TMP> にする。Windows はパスの区切り（JSON の \\）を / にそろえる
// （記録は macOS で取った）。記録に無い呼び出しは失敗にする（テストを直したら、以前の CLI はもう無いので、
// 期待値を Go の側のテストに書く）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
)

type goldenEntry struct {
	Call string `json:"call"` // 正規化した call の JSON
	Out  string `json:"out"`
	used bool
}

var golden struct {
	sync.Mutex
	loaded map[string][]*goldenEntry // テスト名 → 記録
}

// tmpPatterns は一時ディレクトリの場所（JSON の文字列の中の形）を <TMP> にする正規表現（長いものから）。
var tmpPatterns = func() []*regexp.Regexp {
	roots := map[string]bool{}
	base := strings.TrimRight(os.TempDir(), `/\`)
	roots[base] = true
	if p, err := filepath.EvalSymlinks(base); err == nil {
		roots[p] = true
	}
	var list []string
	for r := range roots {
		list = append(list, r)
		b, _ := json.Marshal(r)
		list = append(list, strings.Trim(string(b), `"`))
	}
	sort.Slice(list, func(i, j int) bool { return len(list[i]) > len(list[j]) })
	var out []*regexp.Regexp
	for _, r := range list {
		out = append(out, regexp.MustCompile(regexp.QuoteMeta(r)+`(?:/|\\\\|\\)+[^/\\"]+`))
	}
	return out
}()

// normalize は記録・比較のための文字列（call の JSON・結果の JSON）の正規化。
func normalize(s string) string {
	for _, re := range tmpPatterns {
		s = re.ReplaceAllString(s, "<TMP>")
	}
	if runtime.GOOS == "windows" {
		s = strings.ReplaceAll(s, `\\`, "/")
	}
	return s
}

func callKey(c call) string {
	b, _ := json.Marshal(c)
	return normalize(string(b))
}

func goldenPath(name string) string {
	return filepath.Join("testdata", "golden", strings.NewReplacer("/", "__", " ", "_").Replace(name)+".json")
}

// goldenWant は記録した以前の CLI の結果（正規化済み）。無ければ ok = false。
func goldenWant(t testing.TB, c call) (string, bool) {
	t.Helper()
	golden.Lock()
	defer golden.Unlock()
	if golden.loaded == nil {
		golden.loaded = map[string][]*goldenEntry{}
	}
	name := t.Name()
	list, ok := golden.loaded[name]
	if !ok {
		b, err := os.ReadFile(goldenPath(name))
		if err == nil {
			if err := json.Unmarshal(b, &list); err != nil {
				t.Fatalf("%s を読めない: %v", goldenPath(name), err)
			}
		}
		golden.loaded[name] = list
	}
	key := callKey(c)
	for _, e := range list {
		if !e.used && e.Call == key {
			e.used = true
			return e.Out, true
		}
	}
	return "", false
}
