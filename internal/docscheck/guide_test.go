package docscheck

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// 利用者ガイド（docs/guide/ と docs/guide/ja/）の日英の構成がそろっているかを確かめる（以前の検査と同じ規則）。
// 版ごとの節（server/・desktop/）のようなサブディレクトリにも降り、ファイルは guide からの相対パスで突き合わせる。
//  1. 英語版と日本語版で .md の相対パスがそろっている
//  2. 各ファイルの見出しの階層の並び（コードブロックの外）・コードブロックの数・表の数が同じ
//  3. 相対リンクの先のファイルがある
//  4. 社内固有の名前・内部の番号が無い（公開リポジトリ github.com/<組織>/looptrack と、そのインストーラを取る
//     raw.githubusercontent.com/<組織>/looptrack と、利用者ガイドのサイト <組織>.github.io/Looptrack/ は除く。正本は deploy/public-names.txt）

// 語は分けて書く（このファイル自身が公開物の検査に掛からないように）
var (
	org       = "howa" + "shoji"
	forbidden = regexp.MustCompile(strings.Join([]string{`IM` + `-[0-9]`, "HP" + "C-", "REQ" + "-", "RW" + "-", org, "宝" + "和", `dev\.` + org}, "|"))
	allowed   = "github.com/" + org + "/looptrack"
	allowRaw  = "raw.githubusercontent.com/" + org + "/looptrack"
	// 公開名の例外は deploy/public-names.txt が正本（public-scan.sh と publish-public.sh も同じ一覧を読む）。
	// 公開名の直後が英数字・_・- のときは別の名前の一部なので許さない（looptrack-internal の社名まで消さない）。
	allowNames, allowSite = loadPublicNames()
	link                  = regexp.MustCompile(`\]\(([^)#\s]+)(#[^)]*)?\)`)
	fence                 = regexp.MustCompile("^\\s*(```|~~~)")
	heading               = regexp.MustCompile(`^(#{1,6})\s`)
	tableRule             = regexp.MustCompile(`^\|\s*-{2,}`)
	scheme                = regexp.MustCompile(`^[a-z]+:`)
)

const guideDir = "../../docs/guide"

// publicNames は deploy/public-names.txt の中身（種別ごとの正規表現）。
type publicNames struct{ names, sites, ends []string }

func readPublicNames() publicNames {
	data, err := os.ReadFile("../../deploy/public-names.txt")
	if err != nil {
		panic(err)
	}
	var p publicNames
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		switch f[0] {
		case "name":
			p.names = append(p.names, f[1])
		case "site":
			p.sites = append(p.sites, f[1])
		case "end":
			p.ends = append(p.ends, f[1])
		}
	}
	if len(p.names) == 0 || len(p.sites) == 0 || len(p.ends) != 1 {
		panic("deploy/public-names.txt に name・site が無いか、end が 1 行ではありません")
	}
	return p
}

// loadPublicNames は、利用者ガイドで許す公開名（name の直後の文字つき）とサイト（site）を作る。
// ガイドに出てくるのは公開リポジトリとインストーラの取得元だけなので、ghcr・net・api は許さない。
func loadPublicNames() (names, site *regexp.Regexp) {
	p := readPublicNames()
	var ns []string
	for _, n := range p.names {
		if strings.HasPrefix(n, `github\.com/`) || strings.HasPrefix(n, `raw\.githubusercontent\.com/`) {
			ns = append(ns, n)
		}
	}
	return regexp.MustCompile(`(?:` + strings.Join(ns, "|") + `)(?P<end>` + p.ends[0] + `)`), regexp.MustCompile(strings.Join(p.sites, "|"))
}

type shape struct {
	headings       []int
	fences, tables int
}

// guideLineForbidden は、1 行に社内固有の名前か内部の番号が残っているかを返す（許す公開名を除いてから判定する）。
func guideLineForbidden(line string) bool {
	rest := allowNames.ReplaceAllString(line, "${end}")
	return forbidden.MatchString(allowSite.ReplaceAllString(rest, ""))
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

// mdNames は dir の下の .md を、dir からの相対パス（区切りは /）で返す。サブディレクトリにも降りる。
// skip に挙げたディレクトリ（dir からの相対パス。英語版から見た ja）には降りない。
func mdNames(t *testing.T, dir string, skip ...string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if slices.Contains(skip, rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".md") {
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// guideTreeProblems は英語版 en（ja には降りない）と日本語版 ja の .md を相対パスで突き合わせ、
// 片方にしか無いファイルと、構成（見出しの並び・コードブロックの数・表の数）の違いを返す。
func guideTreeProblems(t *testing.T, en, ja string) []string {
	t.Helper()
	enNames, jaNames := mdNames(t, en, "ja"), mdNames(t, ja)
	var out []string
	for _, n := range enNames {
		if !slices.Contains(jaNames, n) {
			out = append(out, "ja/"+n+" がありません")
		}
	}
	for _, n := range jaNames {
		if !slices.Contains(enNames, n) {
			out = append(out, n+"（英語版）がありません")
		}
	}
	for _, n := range enNames {
		if !slices.Contains(jaNames, n) {
			continue
		}
		e := shapeOf(read(t, filepath.Join(en, filepath.FromSlash(n))))
		j := shapeOf(read(t, filepath.Join(ja, filepath.FromSlash(n))))
		if !slices.Equal(e.headings, j.headings) {
			out = append(out, n+": 見出しの並びが違います（英 "+fmtInts(e.headings)+" / 日 "+fmtInts(j.headings)+"）")
		}
		if e.fences != j.fences {
			out = append(out, n+": コードブロックの数が違います（英 "+strconv.Itoa(e.fences)+" / 日 "+strconv.Itoa(j.fences)+"）")
		}
		if e.tables != j.tables {
			out = append(out, n+": 表の数が違います（英 "+strconv.Itoa(e.tables)+" / 日 "+strconv.Itoa(j.tables)+"）")
		}
	}
	return out
}

func fmtInts(v []int) string {
	s := make([]string, len(v))
	for i, n := range v {
		s[i] = strconv.Itoa(n)
	}
	return "[" + strings.Join(s, " ") + "]"
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
	en := mdNames(t, guideDir, "ja")
	if len(en) == 0 {
		t.Fatal("docs/guide に .md がありません")
	}
	// 検出できる側の対照 1: サブディレクトリ（版ごとの節）まで降りていること。降りなければ、版ごとの節は
	// 日英のどちらかが欠けても何も言わずに通る。
	for _, want := range []string{"server/README.md", "desktop/README.md"} {
		if !slices.Contains(en, want) {
			t.Errorf("%s を走査していません（mdNames がサブディレクトリに降りていません）", want)
		}
	}
	for _, p := range guideTreeProblems(t, guideDir, filepath.Join(guideDir, "ja")) {
		t.Error(p)
	}

	// 検出できる側の対照 2: 日本語版のサブディレクトリのファイルが 1 本欠けた写しでは、欠けを報告すること。
	// 一時ディレクトリに組むので、実物は変えない。
	tmp := t.TempDir()
	for _, f := range []string{"README.md", "server/README.md", "ja/README.md"} {
		p := filepath.Join(tmp, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("# x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := guideTreeProblems(t, tmp, filepath.Join(tmp, "ja"))
	if !slices.Equal(got, []string{"ja/server/README.md がありません"}) {
		t.Errorf("前提が崩れています: 日本語版の server/README.md を欠いた写しで、欠けの報告が 1 件だけ出ることを期待しましたが %q でした", got)
	}
}

// 許す公開名は、名前そのものだけに効く。続きの名前（looptrack-internal など）は社名ごと検出する。
func TestGuideAllowedNameBoundary(t *testing.T) {
	detected := []string{
		"https://" + allowed + "-internal",
		"https://" + allowRaw + "-internal/main/install.sh",
		allowed + "ops",
		allowed + "_old",
		allowed + ".internal",
		"https://" + allowed + ".git",
		"https://ghcr.io/" + org + "/looptrack",
		"net." + org + ".looptrack",
	}
	passed := []string{
		"https://" + allowed + "/",
		"https://" + allowed + ".",
		allowed + ":1",
		"https://" + allowed,
		"`" + allowed + "`",
		"https://" + allowRaw + "/main/install.sh",
		"https://" + org + ".github.io/Looptrack/guide/",
	}
	// 対照: 続きの名前が検出されること（ここが通らなければ、通る側の確認は何も言えない）。
	for _, l := range detected {
		if !guideLineForbidden(l) {
			t.Errorf("公開名の続きの名前が検出されていません: %s", l)
		}
	}
	for _, l := range passed {
		if guideLineForbidden(l) {
			t.Errorf("公開名そのものが検出されています: %s", l)
		}
	}
	// 前提: 許す語を取り除く前は、通る側の行も社名で当たる（取り除きが働いている証拠）。
	if !forbidden.MatchString(passed[0]) {
		t.Fatalf("前提が崩れています。公開名を含む行は、取り除く前なら社名に当たるはずです: %s", passed[0])
	}
}

func TestGuideWordsAndLinks(t *testing.T) {
	// 相対リンクを 1 本も見ずに緑になるのを塞ぐ（link の式か読み方が崩れると、リンク切れの検査は何も見ずに通る）。
	checked := map[string]bool{} // 調べた相対リンク（"<guide からの相対パス> -> <リンク先>"）
	files := 0
	for _, side := range []struct {
		dir  string
		skip []string
	}{{guideDir, []string{"ja"}}, {filepath.Join(guideDir, "ja"), nil}} {
		for _, n := range mdNames(t, side.dir, side.skip...) {
			files++
			p := filepath.Join(side.dir, filepath.FromSlash(n))
			rel, _ := filepath.Rel(guideDir, p)
			for i, line := range strings.Split(read(t, p), "\n") {
				if guideLineForbidden(line) {
					t.Errorf("%s:%d: 社内固有の名前か内部の番号: %s", rel, i+1, strings.TrimSpace(line))
				}
				for _, m := range link.FindAllStringSubmatch(line, -1) {
					target := m[1]
					if scheme.MatchString(target) {
						continue
					}
					checked[filepath.ToSlash(rel)+" -> "+target] = true
					// リンク先はそのファイルのあるディレクトリから辿る（server/・desktop/ の中の ../ を含む）。
					if _, err := os.Stat(filepath.Join(filepath.Dir(p), filepath.FromSlash(target))); err != nil {
						t.Errorf("%s:%d: リンク先がありません: %s", rel, i+1, target)
					}
				}
			}
		}
	}
	t.Logf("docs/guide の .md %d 本・相対リンク %d 本を調べました", files, len(checked))
	// 下限は実物のおよそ半分。サブディレクトリに分ける前（85ddcc00）は .md 22 本・相対リンク 118 本、
	// 分けた後は .md 40 本・相対リンク 212 本。
	if files < 20 || len(checked) < 100 {
		t.Fatalf("調べた .md が %d 本・相対リンクが %d 本しかありません（走査か link の式が空振りしています）", files, len(checked))
	}
	// 検出できる側の対照: 目次から各章へのリンクと、サブディレクトリのページのリンクを実際に調べていること。
	for _, want := range []string{"README.md -> concepts.md", "server/README.md -> getting-started.md", "ja/desktop/README.md -> ../daily-use.md"} {
		if !checked[want] {
			t.Errorf("%s を調べていません（走査か link の式がこのリンクに当たっていません）", want)
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

// 正本に足した語がそのまま全体の許可にならないよう、各行の形を確かめる。
func TestPublicNamesFileShape(t *testing.T) {
	p := readPublicNames()
	for _, n := range p.names {
		if !strings.HasSuffix(n, "looptrack") || strings.ContainsAny(n, "()") {
			t.Errorf("name の行は looptrack で終わり、かっこを含めません: %s", n)
		}
	}
	for _, n := range p.sites {
		if !strings.HasSuffix(n, "[Ll]ooptrack/") {
			t.Errorf("site の行は [Ll]ooptrack/ で終わる必要があります: %s", n)
		}
	}
}

// 続きの名前は、どの公開名でも例外にしない（ガイドに出ない ghcr・net も含め、正本の語は name の形で見る）。
func TestPublicNamesEndBoundary(t *testing.T) {
	p := readPublicNames()
	all := regexp.MustCompile(`(?:` + strings.Join(p.names, "|") + `)(?P<end>` + p.ends[0] + `)`)
	for _, c := range []struct {
		in   string
		pass bool
	}{
		{"net." + org + ".looptrack.internal", false},
		{"github.com/" + org + "/looptrack.internal", false},
		{"github.com/" + org + "/looptrack-internal", false},
		{"ghcr.io/" + org + "/looptrack:1", true},
		{"github.com/" + org + "/looptrack/", true},
		{"github.com/" + org + "/looptrack.", true},
		{"net." + org + ".looptrack", true},
	} {
		got := !forbidden.MatchString(all.ReplaceAllString(c.in, "${end}"))
		if got != c.pass {
			t.Errorf("%s: 通る=%v を期待しましたが %v でした", c.in, c.pass, got)
		}
	}
}
