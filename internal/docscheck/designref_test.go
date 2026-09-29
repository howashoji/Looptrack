package docscheck

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// サーバの設計書（docs/server/DESIGN.md）の節番号を指す参照（「§<数>」「§<数>-<数>」「§<数>-<数>-<数>」）が、
// いまの DESIGN.md の見出しに実在することを確かめる。
//
// きっかけ: DESIGN.md の章立てを直して節番号を振り直したとき、コード・文書の側の参照は旧番号のまま残った。
// 旧番号の多くは新しい番号としても実在する（旧のトークン計測と新のデスクトップ版が同じ番号になる、など）ので、
// 読む人は黙って別の節へ案内される。番号を振り直しても検査が落ちないので、誰も気づけなかった。
//
// 調べる参照:
//   - DESIGN.md の中: 本文のすべての § の参照（見出しの行も含む）。
//   - それ以外の公開物: 「DESIGN」または「設計 §」を含む行の § の参照。
//     公開物の範囲は .gitattributes の export-ignore（git archive が外すもの）を除いた全ファイル。
//
// 外の文書の節は外す: 直前が RFC・LGPL・AI-GUIDE・ADD-PROJECT・RELEASE.md・DEPLOY.md・README・
// iteration-discipline・CONTRIBUTING のもの（例: 「RFC 8252 の §8.3」の形）と、番号の後ろが「.<数>」「(<英字>)」の
// もの（RFC・LGPL の条項の書き方）。
//
// 実在の判定:
//   - §N: 「## N. …」の見出し。§N-M: 「### N-M. …」。§N-M-K: 「### N-M.」の下の「#### K. …」。
//   - 参照の直後（間に「の」を挟んでもよい）に「…」で名前が書いてあれば、その名前がその節の範囲
//     （見出しから、同じか上の階層の次の見出しの前まで）の文字列に含まれることも確かめる。
//     節の中の小見出し・表の行・用語を名前で指す書き方があるので、見出しの完全一致ではなく包含で見る。

const designDocPath = "docs/server/DESIGN.md"

// designRefPattern は § と番号。番号の後ろの「.<数>」「(<英字>)」は呼ぶ側で見る（RE2 に先読みが無いため）。
var designRefPattern = regexp.MustCompile(`§([0-9]+(?:-[0-9]+)*)`)

// designForeignBefore は、参照の直前が外の文書の名前なら当たる（その § は DESIGN.md の節ではない）。
var designForeignBefore = regexp.MustCompile(`(RFC\s*[0-9]+|LGPL-[0-9.]+|AI-GUIDE(\.md)?|ADD-PROJECT\.md\)?|RELEASE\.md|DEPLOY\.md|README(\.md)?|iteration-discipline(\.md)?|CONTRIBUTING\.md)\s*(の\s*)?$`)

// designLinePattern は、DESIGN.md の外で参照を拾う行の印。
var designLinePattern = regexp.MustCompile(`DESIGN|設計\s*§`)

var (
	designH2 = regexp.MustCompile(`^## ([0-9]+)\. `)
	designH3 = regexp.MustCompile(`^### ([0-9]+-[0-9]+)\. `)
	designH4 = regexp.MustCompile(`^#### ([0-9]+)\. `)
)

type designRef struct {
	path  string
	line  int
	id    string
	names []string
}

// designSections は DESIGN.md の本文から、節の番号 → その節の範囲の文字列を作る（コードブロックの中は見出しとしない）。
func designSections(text string) map[string]string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	type head struct {
		at, level int
		id        string
	}
	var heads []head
	cur3 := ""
	fence := false
	for i, l := range lines {
		if strings.HasPrefix(l, "```") {
			fence = !fence
			continue
		}
		if fence {
			continue
		}
		switch {
		case strings.HasPrefix(l, "## "):
			id := ""
			if m := designH2.FindStringSubmatch(l); m != nil {
				id = m[1]
			}
			cur3 = ""
			heads = append(heads, head{i, 2, id})
		case strings.HasPrefix(l, "### "):
			id := ""
			if m := designH3.FindStringSubmatch(l); m != nil {
				id = m[1]
			}
			cur3 = id
			heads = append(heads, head{i, 3, id})
		case strings.HasPrefix(l, "#### "):
			id := ""
			if m := designH4.FindStringSubmatch(l); m != nil && cur3 != "" {
				id = cur3 + "-" + m[1]
			}
			heads = append(heads, head{i, 4, id})
		}
	}
	secs := map[string]string{}
	for k, h := range heads {
		if h.id == "" {
			continue
		}
		end := len(lines)
		for _, n := range heads[k+1:] {
			if n.level <= h.level {
				end = n.at
				break
			}
		}
		secs[h.id] = strings.Join(lines[h.at:end], "\n")
	}
	return secs
}

// designQuotedNames は s の先頭（空白と「の」を飛ばした後）に続く「…」を順に返す（入れ子の「」も 1 つの名前に含める）。
func designQuotedNames(s string) []string {
	s = strings.TrimLeft(s, " 　")
	if rest, ok := strings.CutPrefix(s, "の"); ok && strings.HasPrefix(strings.TrimLeft(rest, " 　"), "「") {
		s = strings.TrimLeft(rest, " 　")
	}
	var names []string
	for strings.HasPrefix(s, "「") {
		depth := 0
		end := -1
		for i, r := range s {
			switch r {
			case '「':
				depth++
			case '」':
				depth--
			}
			if depth == 0 {
				end = i
				break
			}
		}
		if end < 0 {
			break // 閉じていない（行をまたぐ名前）。名前の照合はしない
		}
		names = append(names, s[len("「"):end])
		s = s[end+len("」"):]
	}
	return names
}

// scanDesignRefs は 1 ファイル分の本文から DESIGN.md の節への参照を拾う。
func scanDesignRefs(rel, text string) []designRef {
	inDesign := rel == designDocPath
	var refs []designRef
	for i, l := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if !strings.Contains(l, "§") {
			continue
		}
		if !inDesign && !designLinePattern.MatchString(l) {
			continue
		}
		for _, m := range designRefPattern.FindAllStringSubmatchIndex(l, -1) {
			after := l[m[1]:]
			if len(after) >= 2 && after[0] == '.' && after[1] >= '0' && after[1] <= '9' {
				continue // RFC の節（§8.3）
			}
			if len(after) >= 2 && after[0] == '(' && after[1] >= 'a' && after[1] <= 'z' {
				continue // LGPL の条項（§6(a)）
			}
			if designForeignBefore.MatchString(l[:m[0]]) {
				continue
			}
			refs = append(refs, designRef{path: rel, line: i + 1, id: l[m[2]:m[3]], names: designQuotedNames(after)})
		}
	}
	return refs
}

// checkDesignRefs は、実在しない節と、その節に無い名前を返す。
func checkDesignRefs(secs map[string]string, refs []designRef) []string {
	var bad []string
	for _, r := range refs {
		sec, ok := secs[r.id]
		if !ok {
			bad = append(bad, fmt.Sprintf("%s:%d: §%s は %s の見出しにありません", r.path, r.line, r.id, designDocPath))
			continue
		}
		for _, n := range r.names {
			if !strings.Contains(sec, n) {
				bad = append(bad, fmt.Sprintf("%s:%d: §%s の範囲に「%s」がありません", r.path, r.line, r.id, n))
			}
		}
	}
	return bad
}

// exportIgnored は .gitattributes の export-ignore（公開物に入らないもの）を repoRoot からの相対で返す。
// 「/<パス>/」（ディレクトリ）と「/<パス>」（ファイル）の形だけを読む。ほかの形が来たら落とす
// （読めない形を黙って無視すると、公開物に入らないものまで調べて偽の赤になるか、その逆になる）。
func exportIgnored(t *testing.T) (dirs, files []string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, ".gitattributes"))
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) < 2 || strings.HasPrefix(f[0], "#") || f[len(f)-1] != "export-ignore" {
			continue
		}
		p := f[0]
		if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, "*?[") {
			t.Fatalf(".gitattributes の export-ignore の形を読めません: %q（この検査は /<パス>/ と /<パス> だけを読む）", p)
		}
		if strings.HasSuffix(p, "/") {
			dirs = append(dirs, strings.TrimSuffix(strings.TrimPrefix(p, "/"), "/"))
		} else {
			files = append(files, strings.TrimPrefix(p, "/"))
		}
	}
	if len(dirs) == 0 {
		t.Fatal(".gitattributes から export-ignore を 1 つも読めません（private/ などが公開物の範囲に入ってしまう）")
	}
	return dirs, files
}

// designRefFiles は公開物の範囲のファイルを repoRoot からの相対で返す（.git と export-ignore を除く）。
func designRefFiles(t *testing.T) []string {
	t.Helper()
	dirs, files := exportIgnored(t)
	var out []string
	err := filepath.WalkDir(repoRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(repoRoot, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if d.Name() == ".git" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			for _, x := range dirs {
				if rel == x {
					return filepath.SkipDir
				}
			}
			return nil
		}
		for _, x := range files {
			if rel == x {
				return nil
			}
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func TestDesignSectionRefsExist(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(designDocPath)))
	if err != nil {
		t.Fatal(err)
	}
	secs := designSections(string(b))
	// 見出しの読み取りが空振りすると、すべての参照が「無い」になるか（赤）、読み方を緩めて緑にしたくなる。
	// 章（§N）・節（§N-M）・小節（§N-M-K）がそれぞれ読めていることを先に確かめる（2026-09 に 10・20・8）。
	var n1, n2, n3 int
	for id := range secs {
		switch strings.Count(id, "-") {
		case 0:
			n1++
		case 1:
			n2++
		default:
			n3++
		}
	}
	if n1 < 5 || n2 < 10 || n3 < 1 {
		t.Fatalf("%s の見出しを読めていません（章 %d・節 %d・小節 %d）", designDocPath, n1, n2, n3)
	}

	var refs []designRef
	files := designRefFiles(t)
	for _, rel := range files {
		b, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.IndexByte(b, 0) >= 0 {
			continue // 本文ではない
		}
		refs = append(refs, scanDesignRefs(rel, string(b))...)
	}
	// 拾った参照が 0 件でも緑になるので下限を置く。DESIGN.md の中と外の両方で見る
	// （片方の拾い方だけが壊れても、全体の件数では気づけない）。2026-09 に中 157 件・外 222 件。
	var inner, outer int
	for _, r := range refs {
		if r.path == designDocPath {
			inner++
		} else {
			outer++
		}
	}
	if inner < 50 || outer < 50 {
		t.Fatalf("拾った参照が少なすぎます（%s の中 %d 件・外 %d 件。%d ファイルを調べた）。拾い方が空振りしています", designDocPath, inner, outer, len(files))
	}
	for _, msg := range checkDesignRefs(secs, refs) {
		t.Error(msg)
	}
	if t.Failed() {
		t.Logf("%s の節番号か見出しの名前が変わったなら、参照を今の番号と名前に直してください（旧番号のままだと、読む人は別の節へ案内されます）", designDocPath)
	}
	t.Logf("%d ファイルから参照 %d 件（%s の中 %d・外 %d）を %d 節と照らしました", len(files), len(refs), designDocPath, inner, outer, len(secs))
}

// TestDesignSectionRefsDetect は、検査が実在しない節・範囲に無い名前を実際に拾うことを確かめる
// （拾い方が壊れると、上のテストは参照を 1 つも見ないまま緑になりうる）。
// ソースに節記号を直接書くと上のテストがこのファイルを拾うので、ここでは @ で書いて実行時に置き換える。
func TestDesignSectionRefsDetect(t *testing.T) {
	doc := "## 1. 概要\n\n### 1-1. 前提\n\n本文。\n\n## 9. 運用ルール\n\n### 9-3. ループ\n\n#### 2. verify の実行\n\n秘密のマスク\n\n```\n## 7. コードブロックの中\n```\n"
	secs := designSections(doc)
	for _, id := range []string{"1", "1-1", "9", "9-3", "9-3-2"} {
		if _, ok := secs[id]; !ok {
			t.Errorf("見出し %s を読めていません（前提が崩れています）: %v", id, secs)
		}
	}
	if _, ok := secs["7"]; ok {
		t.Error("コードブロックの中の見出しを節として読んでいます")
	}
	src := strings.Join([]string{
		"// 設計は DESIGN.md @9-3-2「秘密のマスク」",                // 1: 実在する（名前も範囲にある）
		"// 設計は DESIGN.md @5-11",                         // 2: 実在しない
		"// DESIGN.md @9-3 の「前提」",                        // 3: 名前が範囲に無い（前提は 1-1 にある）
		"// RFC 8252 @8.3・LGPL-2.1 @6(a)・DESIGN.md @1-1", // 4: 外の文書は外し、DESIGN の 1 件だけ拾う
		"// AI-GUIDE @7-3-1 と DESIGN.md @9-3-2",          // 5: AI-GUIDE の節は外す
		"// @99-1（設計書の名前の無い行は拾わない）",
	}, "\n")
	src = strings.ReplaceAll(src, "@", string(rune(0xa7)))
	refs := scanDesignRefs("x.go", src)
	var got []string
	for _, r := range refs {
		got = append(got, fmt.Sprintf("%d:%s%v", r.line, r.id, r.names))
	}
	want := []string{"1:9-3-2[秘密のマスク]", "2:5-11[]", "3:9-3[前提]", "4:1-1[]", "5:9-3-2[]"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("拾った参照が違います:\n got  %v\n want %v", got, want)
	}
	bad := checkDesignRefs(secs, refs)
	if len(bad) != 2 || !strings.Contains(bad[0], "x.go:2:") || !strings.Contains(bad[1], "x.go:3:") {
		t.Errorf("実在しない節（2 行目）と範囲に無い名前（3 行目）の 2 件を拾うはず: %v", bad)
	}
	// 入れ子の「」は 1 つの名前として読む
	if n := designQuotedNames(" の「節ごとのハッシュと「起票時のまま」の警告」"); len(n) != 1 || n[0] != "節ごとのハッシュと「起票時のまま」の警告" {
		t.Errorf("入れ子の名前を読めていません: %q", n)
	}
}
