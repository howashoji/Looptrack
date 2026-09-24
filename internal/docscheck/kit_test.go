package docscheck

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// 配る kit（kit/core・kit/loop）と kit 自体の説明（kit/README.ja.md と kit/README.md）の日英の構成を確かめる。
//
// 日本語が正本で今の場所のまま、英語は同じディレクトリの en/ に同じファイル名で置く（kit/README.ja.md の「本文の言語」）。
// 例外は kit 自体の説明の 1 対で、配らないのでルートと同じ 2 本立て（日本語が kit/README.ja.md・英語が kit/README.md）。
// kitMD がこの 1 対だけを明示して登録するので、ほかの .md と同じ検査に掛かる。
// 検査の規則は利用者ガイド（guide_test.go）と同じものを使い回す（shapeOf・forbidden・link）。ただし翻訳は途中なので、
// 「英語が無い正本」は落とさない（ラチェット。訳した分だけが検査の対象になる）。落とすのは次の 4 つ:
//
//  1. 正本の無い訳（en/ に孤児のファイルがある）
//  2. 見出しの階層の並び・コードブロックの数・表の数の食い違い
//  3. 注入の印（<!-- looptrack:inject … -->）の並びの食い違い（注入される節が言語でずれると hook の出す文面が変わる）
//  4. 社内固有の名前・内部の番号・切れた相対リンク（正本と訳の両方）

const kitDir = "../../kit"

// kitInject は注入の印（モードと AI の指定まで含めて比べる）。
var kitInject = regexp.MustCompile(`<!--\s*looptrack:inject\s+([a-z0-9 -]*?)\s*-->`)

// kitMarkers は本文の注入の印を現れた順に返す。
func kitMarkers(text string) []string {
	var out []string
	for _, m := range kitInject.FindAllStringSubmatch(text, -1) {
		out = append(out, strings.Join(strings.Fields(m[1]), " "))
	}
	return out
}

// kitMD は kit/ の下の .md を、正本（sources）と訳（translations。en/ を除いた相対名 → 実際の相対名）に分けて返す。
func kitMD(t *testing.T) (sources []string, translations map[string]string) {
	t.Helper()
	translations = map[string]string{}
	err := filepath.WalkDir(kitDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return err
		}
		rel, _ := filepath.Rel(kitDir, p)
		rel = filepath.ToSlash(rel)
		switch rel {
		case "README.ja.md":
			// kit 自体の説明は配らないので en/ の規約の外にある（上の説明の例外）。
			// 正本と訳の向きも逆（README.ja.md が日本語の正本・README.md が英語の訳）なので、
			// en/ の振り分けに任せず、この 1 対だけを明示して登録する。
			sources = append(sources, rel)
			return nil
		case "README.md":
			translations["README.ja.md"] = rel
			return nil
		}
		parts := strings.Split(rel, "/")
		if i := slices.Index(parts, "en"); i >= 0 {
			translations[strings.Join(slices.Delete(slices.Clone(parts), i, i+1), "/")] = rel
			return nil
		}
		sources = append(sources, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(sources)
	if len(sources) == 0 {
		t.Fatal("kit に .md がありません")
	}
	return sources, translations
}

// kitPairMismatch は正本と訳の 1 対を読み、構成の食い違いを人の読める文で返す（そろっていれば nil）。
// TestKitStructure と、合成した対で規則そのものを確かめる TestKitPairMismatchDetectsBrokenHeading の
// 両方から呼ぶので、kit/ の実ファイルの場所に縛らずパスを引数で受け取る。
func kitPairMismatch(t *testing.T, jaPath, enPath string) []string {
	t.Helper()
	js, es := read(t, jaPath), read(t, enPath)
	j, e := shapeOf(js), shapeOf(es)
	var out []string
	if !slices.Equal(e.headings, j.headings) {
		out = append(out, fmt.Sprintf("見出しの並びが違います（日 %v / 英 %v）", j.headings, e.headings))
	}
	if e.fences != j.fences {
		out = append(out, fmt.Sprintf("コードブロックの数が違います（日 %d / 英 %d）", j.fences, e.fences))
	}
	if e.tables != j.tables {
		out = append(out, fmt.Sprintf("表の数が違います（日 %d / 英 %d）", j.tables, e.tables))
	}
	if jm, em := kitMarkers(js), kitMarkers(es); !slices.Equal(jm, em) {
		out = append(out, fmt.Sprintf("注入の印が違います（日 %v / 英 %v）", jm, em))
	}
	return out
}

// TestKitStructure は訳のある分だけ、正本と訳の構成がそろっているかを確かめる。
func TestKitStructure(t *testing.T) {
	sources, translations := kitMD(t)
	for want, got := range translations {
		if !slices.Contains(sources, want) {
			t.Errorf("%s: 正本（%s）がありません", got, want)
		}
	}
	n := 0
	for _, src := range sources {
		tr, ok := translations[src]
		if !ok {
			continue // まだ訳していない（正本の日本語が使われる）
		}
		n++
		for _, msg := range kitPairMismatch(t, filepath.Join(kitDir, src), filepath.Join(kitDir, tr)) {
			t.Errorf("%s: %s", tr, msg)
		}
	}
	t.Logf("kit の .md: 正本 %d 本・訳あり %d 本", len(sources), n)
}

// TestKitWordsAndLinks は kit の .md（正本・訳の両方）に社内固有の名前・内部の番号・切れた相対リンクが無いかを確かめる。
// kit は公開物なので deploy/public-scan.sh も見る（publicscan_test.go から呼ぶ）。
// そちらは公開物の全体を見て語と番号を数える。ここは kit の .md を行ごとに見て、どの行かを名指しする。
func TestKitWordsAndLinks(t *testing.T) {
	sources, translations := kitMD(t)
	rels := append([]string(nil), sources...)
	for _, tr := range translations {
		rels = append(rels, tr)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		p := filepath.Join(kitDir, rel)
		for i, line := range strings.Split(read(t, p), "\n") {
			if forbidden.MatchString(strings.ReplaceAll(line, allowed, "")) {
				t.Errorf("%s:%d: 社内固有の名前か内部の番号: %s", rel, i+1, strings.TrimSpace(line))
			}
			for _, m := range link.FindAllStringSubmatch(line, -1) {
				target := m[1]
				if scheme.MatchString(target) {
					continue
				}
				if _, err := os.Stat(filepath.Join(filepath.Dir(p), filepath.FromSlash(target))); err != nil {
					t.Errorf("%s:%d: リンク先がありません: %s", rel, i+1, target)
				}
			}
		}
	}
}

// TestKitPairMismatchDetectsBrokenHeading は、TestKitStructure が使うのと同じ比較（kitPairMismatch）が、
// 構成を崩した対で本当に不一致を返すことを確かめる。
//
// 実ファイルの対が今そろっていること（検査が緑であること）は、比較が効いていることの根拠にならない。
// 崩れた対を一度も通していないので、比較を外しても緑のままになる。そこで一時ディレクトリに日英の対を
// 合成し、見出しを 1 つだけ崩して不一致が返ることを見る（kit/ の実ファイルは読むだけで、触らない）。
func TestKitPairMismatchDetectsBrokenHeading(t *testing.T) {
	const ja = "# 見出し\n\n## 節\n\n| 列 | 列 |\n|---|---|\n| あ | い |\n\n### 小節\n\n```\ncode\n```\n\n<!-- looptrack:inject session -->\n"
	const en = "# Title\n\n## Section\n\n| col | col |\n|---|---|\n| a | b |\n\n### Subsection\n\n```\ncode\n```\n\n<!-- looptrack:inject session -->\n"

	dir := t.TempDir()
	jaPath, enPath := filepath.Join(dir, "sample.ja.md"), filepath.Join(dir, "sample.md")
	write := func(p, text string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(jaPath, ja)
	write(enPath, en)
	if got := kitPairMismatch(t, jaPath, enPath); len(got) != 0 {
		t.Fatalf("そろった対で不一致が返った: %v", got)
	}

	// 見出しを 1 つだけ崩す（### → ####）。コードブロック・表・注入の印は変えない。
	write(enPath, strings.Replace(en, "### Subsection", "#### Subsection", 1))
	got := kitPairMismatch(t, jaPath, enPath)
	if len(got) != 1 || !strings.HasPrefix(got[0], "見出しの並びが違います") {
		t.Fatalf("見出しを崩した対で、見出しの不一致だけが返らなかった: %v", got)
	}
}
