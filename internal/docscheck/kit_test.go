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
//
// kit 自体の説明（kit/README.ja.md と kit/README.md）は kitMD が明示して登録する 1 対で、en/ の規約に乗らない
// 唯一の例外なので、ここで名指しして確かめる。この登録が外れる（kitMD の switch が「配らないので対象外」の
// 除外に戻る）と、以降のループは sources・translations が縮んだことに気づけずに緑を返す
// （len(sources) や n は下限を検査していないため）。
func TestKitStructure(t *testing.T) {
	sources, translations := kitMD(t)

	// kit/README.ja.md と kit/README.md の対が登録されていること（検出できる側の対照）。
	// kitMD の除外を戻すとどちらも成り立たなくなるので、この 2 行が範囲の回帰を検出する。
	if !slices.Contains(sources, "README.ja.md") {
		t.Errorf("README.ja.md: kit の説明の正本として登録されていません（kitMD の除外が戻っていないか確認する）")
	}
	if got, want := translations["README.ja.md"], "README.md"; got != want {
		t.Errorf("README.ja.md の訳が %q として登録されていません（実際の登録: %q）", want, got)
	}

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
	// 訳のある対を 1 つも比べずに緑になるのを塞ぐ（en/ の振り分けが崩れると translations が縮み、上のループは素通りする）。
	// 下限は実物（2026-09 に正本 10 本・訳あり 10 本）より少し下。
	if len(sources) < 8 || n < 8 {
		t.Fatalf("kit の .md の正本が %d 本・訳のある対が %d 本しかありません（kitMD の振り分けが空振りしています）", len(sources), n)
	}
	if _, ok := translations["loop/rules/working-discipline.md"]; !ok {
		t.Errorf("loop/rules/working-discipline.md の訳（en/）が対として登録されていません（kitMD の en/ の振り分けを確かめる）")
	}
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
	checked := map[string]bool{} // 調べた相対リンク（"<相対パス> -> <リンク先>"）
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
				checked[rel+" -> "+target] = true
				if _, err := os.Stat(filepath.Join(filepath.Dir(p), filepath.FromSlash(target))); err != nil {
					t.Errorf("%s:%d: リンク先がありません: %s", rel, i+1, target)
				}
			}
		}
	}
	// 相対リンクを 1 本も見ずに緑になるのを塞ぐ。kit の相対リンクは少ない（2026-09 に README の日英で、重複を除いて 6 本）ので、
	// 本数の下限に加えて、分かっているリンクを実際に調べたことを確かめる。
	t.Logf("kit の .md %d 本・相対リンク %d 本を調べました", len(rels), len(checked))
	if len(rels) < 10 || len(checked) < 4 {
		t.Fatalf("調べた .md が %d 本・相対リンクが %d 本しかありません（走査か link の式が空振りしています）", len(rels), len(checked))
	}
	if want := "README.ja.md -> README.md"; !checked[want] {
		t.Errorf("%s を調べていません（link の式が kit の説明の日英の相互リンクに当たっていません）", want)
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

// kitSectionHeading・kitSectionMarker・kitSectionAny は、internal/client/kitinit/loopkit.go の
// injectHeading・injectSections 内のマーカー正規表現・injectAny と同じ定義（節を見出し＋直後のマーカーで
// 切り出す規則そのもの）。違うのは、特定のモード・AI に絞らずすべてを拾う点だけ（ここでは節の本文の「量」を
// 比べたいだけで、`looptrack issue init` が実際にどの節をどの AI に配るかは関係ない）。
var (
	kitSectionHeading = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	kitSectionMarker  = regexp.MustCompile(`^\s*<!--\s*looptrack:inject\s+([a-z0-9-]+)((?:\s+[a-z][a-z0-9-]*)*)\s*-->\s*$`)
	kitSectionAny     = regexp.MustCompile(`^\s*<!--\s*looptrack:inject\b`)
	kitBulletLine     = regexp.MustCompile(`^\s*[-*]\s+`)
	kitInlineCode     = regexp.MustCompile("`[^`]+`")
)

// injectedSection は 1 つの注入節の印（モード・対象 AI）と、訳で揺れない量だけを持つ。
type injectedSection struct {
	heading string
	mode    string
	agents  []string
	bullets int // 行頭が "-" か "*" の項目数
	fences  int // コードフェンス（```）の行数
	inline  int // インラインコード（`…`）の出現数
}

// kitInjectSections は本文から、見出しの直後（空行は飛ばす）に <!-- looptrack:inject … --> がある節を、
// internal/client/kitinit/loopkit.go の injectSections と同じ抽出規則（見出しの検出・フェンスの内外判定・
// 入れ子の見出しで節を閉じる・印と "---" の行は本文から除く・前後の空行を削る）で切り出し、出現順に返す。
//
// 本文の行数そのものは比べない。日本語は文を折り返さず、英語は同じ内容でも折り返しの位置で行数が増減するため、
// 実際にそろっている節でも行数だけはずれる（実測: kit/loop/rules/working-discipline.md の「全作業共通の規律」で
// 日本語 139 行・英語 141 行。箇条書きの項目数・コードフェンスの数・インラインコードの出現数はこの節も含め全節で
// 実際に一致している）。代わりにこの 3 つの、訳で揺れない量を数える。
func kitInjectSections(text string) []injectedSection {
	lines := strings.Split(text, "\n")
	var out []injectedSection
	fence := false
	for i := 0; i < len(lines); {
		line := lines[i]
		if strings.HasPrefix(strings.TrimLeft(line, " \t"), "```") {
			fence = !fence
		}
		var m []string
		if !fence {
			m = kitSectionHeading.FindStringSubmatch(line)
		}
		j := i + 1
		for m != nil && j < len(lines) && strings.TrimSpace(lines[j]) == "" {
			j++
		}
		var mk []string
		if m != nil && j < len(lines) {
			mk = kitSectionMarker.FindStringSubmatch(lines[j])
		}
		if mk == nil {
			i++
			continue
		}
		level := len(m[1])
		heading := strings.TrimSpace(m[2])
		mode := mk[1]
		agents := strings.Fields(mk[2])
		var body []string
		k, inner := j+1, false
		for ; k < len(lines); k++ {
			l := lines[k]
			if strings.HasPrefix(strings.TrimLeft(l, " \t"), "```") {
				inner = !inner
			}
			if !inner {
				if h := kitSectionHeading.FindStringSubmatch(l); h != nil && len(h[1]) <= level {
					break
				}
			}
			if !kitSectionAny.MatchString(l) && strings.TrimSpace(l) != "---" {
				body = append(body, l)
			}
		}
		for len(body) > 0 && strings.TrimSpace(body[0]) == "" {
			body = body[1:]
		}
		for len(body) > 0 && strings.TrimSpace(body[len(body)-1]) == "" {
			body = body[:len(body)-1]
		}
		if len(body) > 0 {
			bullets, fences, inline := 0, 0, 0
			for _, l := range body {
				if kitBulletLine.MatchString(l) {
					bullets++
				}
				if strings.HasPrefix(strings.TrimLeft(l, " \t"), "```") {
					fences++
				}
				inline += len(kitInlineCode.FindAllString(l, -1))
			}
			out = append(out, injectedSection{heading, mode, agents, bullets, fences, inline})
		}
		i = k
	}
	return out
}

// kitSectionAnyLines は、フェンスの外で kitSectionAny（印そのものの緩い判定。モード・AI の綴りを問わない）に
// 当たる行数を数える。
//
// kitInjectSections が返す節の数とこの行数がずれるのは、印はあるのに見出しの直後に無い・kitSectionMarker の
// 書式（モード・AI の綴り）に合っていないなど、**印はあるのに節として取れていない**ということ。
// kitSectionMarker と kitSectionAny の定義がどちらも kitinit 側の書式変更で同時に当たらなくなると、
// この行数自体が 0 になって節の数（0）と一致してしまい、この検査だけでは拾えない
// （その場合の下支えは TestKitRulesInjectSectionsMatch の合計 0 件チェック）。
func kitSectionAnyLines(text string) int {
	n := 0
	fence := false
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimLeft(l, " \t"), "```") {
			fence = !fence
			continue
		}
		if !fence && kitSectionAny.MatchString(l) {
			n++
		}
	}
	return n
}

// TestKitRulesInjectSectionsMatch は、kit/ の日英の対で注入節（<!-- looptrack:inject … --> を持つ節）ごとに、
// 訳で揺れない量（箇条書きの項目数・コードフェンスの数・インラインコードの出現数）がそろっているかを確かめる。
//
// TestKitStructure（kitPairMismatch）が見る見出しの階層・コードブロックの数・表の数・注入の印の並びという
// 「構造」だけでは、これらを増減させない本文の変更（既存の注入節に文を 1 行足す・箇条書きを 1 つ増やす等）を
// 検出できない。訳のあるファイルに限る点・「英語が無い正本は落とさない」ラチェットは TestKitStructure と同じ。
//
// 抽出そのものが壊れて日英とも 0 節になると、比較のループは len(ja)==len(en)==0 のまま黙って緑になる
// （kitSectionHeading・kitSectionMarker は kitinit 側の定義の写しなので、あちらの印の書式が変わると
// 両方 0 になり得る）。それを防ぐために、ファイルごとに「印の行数」と「節として取れた数」が一致することを確かめ
// （印はあるのに節が取れていない、を検出する）、比べたファイル全体の節の合計が 1 件以上であることも assert する。
func TestKitRulesInjectSectionsMatch(t *testing.T) {
	sources, translations := kitMD(t)

	rels := append([]string(nil), sources...)
	for _, tr := range translations {
		rels = append(rels, tr)
	}
	sort.Strings(rels)
	total := 0
	for _, rel := range rels {
		text := read(t, filepath.Join(kitDir, rel))
		secs := kitInjectSections(text)
		if raw := kitSectionAnyLines(text); raw != len(secs) {
			t.Errorf("%s: 印のある行数（%d）と抽出できた節の数（%d）が違います（節として取れていない印がある）", rel, raw, len(secs))
		}
		total += len(secs)
	}
	if total == 0 {
		t.Fatal("kit/ の .md から注入節を 1 つも抽出できませんでした（kitInjectSections の定義が kitinit 側の書式とずれていないか確認する）")
	}
	t.Logf("kit の .md: 比べたファイル %d 本・注入節の合計 %d 節", len(rels), total)

	for _, src := range sources {
		tr, ok := translations[src]
		if !ok {
			continue // まだ訳していない
		}
		ja := kitInjectSections(read(t, filepath.Join(kitDir, src)))
		en := kitInjectSections(read(t, filepath.Join(kitDir, tr)))
		if len(ja) != len(en) {
			t.Errorf("%s: 注入節の数が違います（日 %d / 英 %d）", tr, len(ja), len(en))
			continue
		}
		for i := range ja {
			j, e := ja[i], en[i]
			if j.mode != e.mode || !slices.Equal(j.agents, e.agents) {
				t.Errorf("%s: 節 %d の印が違います（日 %s %v / 英 %s %v）", tr, i, j.mode, j.agents, e.mode, e.agents)
			}
			if j.bullets != e.bullets {
				t.Errorf("%s: %q: 箇条書きの項目数が違います（日 %d / 英 %d）", tr, e.heading, j.bullets, e.bullets)
			}
			if j.fences != e.fences {
				t.Errorf("%s: %q: コードフェンスの数が違います（日 %d / 英 %d）", tr, e.heading, j.fences, e.fences)
			}
			if j.inline != e.inline {
				t.Errorf("%s: %q: インラインコードの出現数が違います（日 %d / 英 %d）", tr, e.heading, j.inline, e.inline)
			}
		}
	}
}

// TestKitInjectSectionsDetectsAddedBullet は、TestKitRulesInjectSectionsMatch が使う比較（kitInjectSections の
// 数え上げ）が、見出し・フェンス・注入の印の構造を保ったまま箇条書きを 1 つ増やしただけの変更を本当に検出できる
// ことを確かめる（対照）。
//
// 実ファイルの対が今そろっていること（検査が緑であること）は、比較が効いていることの根拠にならない。
// 崩れた対を一度も通していないので、数え上げの実装を外しても緑のままになる。そこで見出し・フェンス・印を
// 変えないまま本文の箇条書きだけを 1 つ増やした対を合成し、bullets の差として検出されることを見る。
func TestKitInjectSectionsDetectsAddedBullet(t *testing.T) {
	const base = "## 節\n\n<!-- looptrack:inject session -->\n\n- 項目1\n- 項目2\n"
	const mutated = "## 節\n\n<!-- looptrack:inject session -->\n\n- 項目1\n- 項目2\n- 項目3\n"

	before := kitInjectSections(base)
	if len(before) != 1 || before[0].bullets != 2 {
		t.Fatalf("前提の抽出が想定どおりでない: %+v", before)
	}
	after := kitInjectSections(mutated)
	if len(after) != 1 || after[0].bullets != 3 {
		t.Fatalf("変異後の抽出が想定どおりでない: %+v", after)
	}
	if before[0].bullets == after[0].bullets {
		t.Fatalf("箇条書きを 1 つ増やした変更が検出されなかった（対照が効いていない）")
	}
}
