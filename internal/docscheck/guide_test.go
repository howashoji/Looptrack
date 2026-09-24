package docscheck

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// 利用者ガイド（docs/guide/ と docs/guide/ja/）の日英の構成がそろっているかを確かめる（以前の検査と同じ規則）。
//  1. 英語版と日本語版で .md のファイル名がそろっている
//  2. 各ファイルの見出しの階層の並び（コードブロックの外）・コードブロックの数・表の数が同じ
//  3. 相対リンクの先のファイルがある
//  4. 社内固有の名前・内部の番号が無い（公開リポジトリ github.com/<組織>/looptrack は除く）

// 語は分けて書く（このファイル自身が公開物の検査に掛からないように）
var (
	org       = "howa" + "shoji"
	forbidden = regexp.MustCompile(strings.Join([]string{`IM` + `-[0-9]`, "HP" + "C-", "REQ" + "-", "RW" + "-", org, "宝" + "和", `dev\.` + org}, "|"))
	allowed   = "github.com/" + org + "/looptrack"
	link      = regexp.MustCompile(`\]\(([^)#\s]+)(#[^)]*)?\)`)
	fence     = regexp.MustCompile("^\\s*(```|~~~)")
	heading   = regexp.MustCompile(`^(#{1,6})\s`)
	tableRule = regexp.MustCompile(`^\|\s*-{2,}`)
	scheme    = regexp.MustCompile(`^[a-z]+:`)
)

const guideDir = "../../docs/guide"

type shape struct {
	headings       []int
	fences, tables int
}

func shapeOf(text string) shape {
	var s shape
	inFence := false
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if fence.MatchString(line) {
			if !inFence {
				s.fences++
			}
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if m := heading.FindStringSubmatch(line); m != nil {
			s.headings = append(s.headings, len(m[1]))
		}
		if tableRule.MatchString(line) {
			s.tables++
		}
	}
	return s
}

func mdNames(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestGuideStructure(t *testing.T) {
	ja := filepath.Join(guideDir, "ja")
	en, jn := mdNames(t, guideDir), mdNames(t, ja)
	if len(en) == 0 {
		t.Fatal("docs/guide に .md がありません")
	}
	for _, n := range en {
		if !slices.Contains(jn, n) {
			t.Errorf("ja/%s がありません", n)
		}
	}
	for _, n := range jn {
		if !slices.Contains(en, n) {
			t.Errorf("%s（英語版）がありません", n)
		}
	}
	for _, n := range en {
		if !slices.Contains(jn, n) {
			continue
		}
		e, j := shapeOf(read(t, filepath.Join(guideDir, n))), shapeOf(read(t, filepath.Join(ja, n)))
		if !slices.Equal(e.headings, j.headings) {
			t.Errorf("%s: 見出しの並びが違います（英 %v / 日 %v）", n, e.headings, j.headings)
		}
		if e.fences != j.fences {
			t.Errorf("%s: コードブロックの数が違います（英 %d / 日 %d）", n, e.fences, j.fences)
		}
		if e.tables != j.tables {
			t.Errorf("%s: 表の数が違います（英 %d / 日 %d）", n, e.tables, j.tables)
		}
	}
}

func TestGuideWordsAndLinks(t *testing.T) {
	for _, dir := range []string{guideDir, filepath.Join(guideDir, "ja")} {
		for _, n := range mdNames(t, dir) {
			p := filepath.Join(dir, n)
			rel, _ := filepath.Rel(guideDir, p)
			for i, line := range strings.Split(read(t, p), "\n") {
				if forbidden.MatchString(strings.ReplaceAll(line, allowed, "")) {
					t.Errorf("%s:%d: 社内固有の名前か内部の番号: %s", rel, i+1, strings.TrimSpace(line))
				}
				for _, m := range link.FindAllStringSubmatch(line, -1) {
					target := m[1]
					if scheme.MatchString(target) {
						continue
					}
					if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(target))); err != nil {
						t.Errorf("%s:%d: リンク先がありません: %s", rel, i+1, target)
					}
				}
			}
		}
	}
}

// 実行ファイルに埋め込む AI 向けの案内（internal/guide/common.md と同 en/common.md）の日英の構成がそろっているか。
// docs/guide/ と同じ 3 点（見出しの階層の並び・コードブロックの数・表の数）を同じ関数で見る。
// 訳がずれても実行時には何も起きないので、ここで落とす。
func TestEmbeddedGuideStructure(t *testing.T) {
	const dir = "../../internal/guide"
	ja, en := filepath.Join(dir, "common.md"), filepath.Join(dir, "en", "common.md")
	for _, p := range []string{ja, en} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s: %v（改名・移動したらこの検査も直してください）", p, err)
		}
	}
	j, e := shapeOf(read(t, ja)), shapeOf(read(t, en))
	if len(j.headings) == 0 {
		t.Fatalf("%s に見出しがありません（検査が空振りしています）", ja)
	}
	if !slices.Equal(e.headings, j.headings) {
		t.Errorf("internal/guide/common.md: 見出しの並びが違います（英 %v / 日 %v）", e.headings, j.headings)
	}
	if e.fences != j.fences {
		t.Errorf("internal/guide/common.md: コードブロックの数が違います（英 %d / 日 %d）", e.fences, j.fences)
	}
	if e.tables != j.tables {
		t.Errorf("internal/guide/common.md: 表の数が違います（英 %d / 日 %d）", e.tables, j.tables)
	}
}
