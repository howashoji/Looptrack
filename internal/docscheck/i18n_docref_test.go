package docscheck

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// 対訳表（internal/i18n/ja.json・en.json）の文面が案内する docs/guide/ の節が、実物に本当に
// あることを確かめる。
//
// きっかけ: cmd.err.desktop_self_update の en.json が `see "Updating" in docs/guide/desktop.md`
// と案内していたが、実物の docs/guide/desktop.md の見出しは `## Update`（"Updating" という見出しは
// 存在しない）。ja.json 側の対応する案内（docs/guide/desktop.md「更新」）は docs/guide/ja/desktop.md の
// `## 更新` と一致していて正しく、片方の言語だけが実物から取り残されていた。
//
// このテストは対訳表の全値を走査し、次の 2 つの形の「文書への案内」をすべて拾う:
//   - ja の形: docs/guide/<file>.md「<節名>」                    → docs/guide/ja/<file>.md を見る
//   - en の形: see "<節名>" in docs/guide/<file>.md               → docs/guide/<file>.md を見る
//
// 拾った案内それぞれについて、対応する文書に「# が 1〜6 個 + 空白 + 節名」が完全一致する行があるかを確かめる。
// 見出しの綴りが変わって案内文だけが取り残されても、この検査までは何も落ちない（対訳表と文書は別々に
// 編集されるので、確認する経路が他に無い）。

// jaDocRefPattern は ja.json の値の中の「docs/guide/<file>.md「<節名>」」を拾う。
var jaDocRefPattern = regexp.MustCompile(`docs/guide/([A-Za-z0-9_./-]+\.md)「([^」]+)」`)

// enDocRefPattern は en.json の値の中の「see "<節名>" in docs/guide/<file>.md」を拾う。
var enDocRefPattern = regexp.MustCompile(`see "([^"]+)" in docs/guide/([A-Za-z0-9_./-]+\.md)`)

// docRef は 1 件の「文書への案内」。
type docRef struct {
	key     string // 対訳表の ID（失敗の報告用）
	docPath string // 見に行く文書（repoRoot からの相対）
	heading string // その文書にあるはずの節名
}

// loadI18nJSON は対訳表の json を素朴な map[string]string として読む。
func loadI18nJSON(t *testing.T, rel string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, rel))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: json として読めません（%v）", rel, err)
	}
	return m
}

// scanDocRefs は対訳表 catalog の全値から docs/guide/ への案内を拾う。
//   - jaSide が true なら ja の形（docs/guide/<file>.md「<節名>」）を拾い、docPath は
//     docs/guide/ja/<file>.md にする。
//   - false なら en の形（see "<節名>" in docs/guide/<file>.md）を拾い、docPath は
//     docs/guide/<file>.md にする。
func scanDocRefs(catalog map[string]string, jaSide bool) []docRef {
	var out []docRef
	for key, val := range catalog {
		if jaSide {
			for _, m := range jaDocRefPattern.FindAllStringSubmatch(val, -1) {
				out = append(out, docRef{key: key, docPath: "docs/guide/ja/" + m[1], heading: m[2]})
			}
			continue
		}
		for _, m := range enDocRefPattern.FindAllStringSubmatch(val, -1) {
			out = append(out, docRef{key: key, docPath: "docs/guide/" + m[2], heading: m[1]})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].key != out[j].key {
			return out[i].key < out[j].key
		}
		return out[i].heading < out[j].heading
	})
	return out
}

// headingLineExists は、文書 docPath（repoRoot からの相対）に「# が 1〜6 個 + 空白 + heading」が
// 完全一致する行があるかを確かめる。
func headingLineExists(t *testing.T, docPath, heading string) (bool, error) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(docPath)))
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		for n := 1; n <= 6; n++ {
			if line == strings.Repeat("#", n)+" "+heading {
				return true, nil
			}
		}
	}
	return false, nil
}

// TestI18nDocRefsPointToRealHeadings は、対訳表が docs/guide/ の節を案内している箇所すべてについて、
// その節が実物に存在することを確かめる（詳しくはファイル冒頭のコメント）。
func TestI18nDocRefsPointToRealHeadings(t *testing.T) {
	ja := loadI18nJSON(t, "internal/i18n/ja.json")
	en := loadI18nJSON(t, "internal/i18n/en.json")

	refs := scanDocRefs(ja, true)
	refs = append(refs, scanDocRefs(en, false)...)

	// witness: パターンが 1 件も当たらなければ、以下の検査は何も見ずに緑になる。
	if len(refs) == 0 {
		t.Fatal("対訳表から docs/guide/ への案内を 1 件も拾えません" +
			"（パターン docs/guide/<file>.md「<節名>」・see \"<節名>\" in docs/guide/<file>.md が変わっていないか確かめてください）")
	}

	for _, r := range refs {
		ok, err := headingLineExists(t, r.docPath, r.heading)
		if err != nil {
			t.Errorf("%s: %s の実在を確かめられません（%v）", r.key, r.docPath, err)
			continue
		}
		if !ok {
			t.Errorf("%s: %s に見出し %q がありません（対訳表の案内が実物の見出しとずれています）", r.key, r.docPath, r.heading)
		}
	}
	t.Logf("対訳表から docs/guide/ への案内を %d 件確かめました", len(refs))
}

// TestScanDocRefsSubdirectory は、版ごとのサブディレクトリ（docs/guide/desktop/・docs/guide/server/）への案内を
// scanDocRefs が日英それぞれの実物のパスに直すことを確かめる。日本語版は docs/guide/ja/ の下に同じ階層で置くので、
// ja の側は docs/guide/ja/desktop/… になる（docs/guide/desktop/ja/… ではない）。
func TestScanDocRefsSubdirectory(t *testing.T) {
	ja := scanDocRefs(map[string]string{"k": "アプリごと置き換えてください（docs/guide/desktop/updating.md「更新」）"}, true)
	en := scanDocRefs(map[string]string{"k": `replace the whole application (see "Update" in docs/guide/desktop/updating.md)`}, false)
	if len(ja) != 1 || ja[0].docPath != "docs/guide/ja/desktop/updating.md" || ja[0].heading != "更新" {
		t.Errorf("ja の案内の読み取りが違います: %+v", ja)
	}
	if len(en) != 1 || en[0].docPath != "docs/guide/desktop/updating.md" || en[0].heading != "Update" {
		t.Errorf("en の案内の読み取りが違います: %+v", en)
	}
}
