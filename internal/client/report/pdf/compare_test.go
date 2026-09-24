package pdf

import (
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/client/report"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/reporttable"
)

// 以前の PDF の作り方（1.0.0 より前。撤去済み）との突き合わせ。
// 以前の結果は撤去の前に testdata/goldenref/<normal|big>.json に記録した（ブロック・列幅・以前の PDF から
// pdftotext で抜き出した桁区切りの数値の並び。フォントは埋め込みの BIZ UDGothic）。
// バイト単位では比べない: ブロック（見出し・段落・表のセル・注記）が同じ、列幅が ±0.5pt、PDF から抜き出した
// 見出しの並びと桁区切りの数値の並びが同じ、を確かめる。実フォントの列幅は埋め込みのフォントのときだけ比べる。
// PDF のテキストの比較は pdftotext が無ければ skip。

type goldenRef struct {
	Font    string               `json:"font"`
	Blocks  [][2]json.RawMessage `json:"blocks"`
	Width   float64              `json:"width"`
	Widths  [][]float64          `json:"widths"`
	Approx  [][]float64          `json:"approx"`
	Numbers []string             `json:"numbers"`
}

type goldenTable struct {
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
	Numeric []int      `json:"numeric"`
	Nowrap  []int      `json:"nowrap"`
	Notes   []string   `json:"notes"`
}

func loadGoldenRef(t *testing.T, name string) goldenRef {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "testdata", "goldenref", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var ref goldenRef
	if err := json.Unmarshal(b, &ref); err != nil {
		t.Fatal(err)
	}
	return ref
}

// goBlock は Go のブロックを記録の (kind, value) の形にする。
func goBlock(b report.Block) (string, any) {
	switch b.Kind {
	case report.Meta, report.Bullets:
		return string(b.Kind), b.Lines
	case report.TableKind:
		t := goldenTable{Numeric: []int{}, Nowrap: []int{}, Notes: append([]string{}, b.Table.Notes...)}
		for i, c := range b.Table.Columns {
			t.Columns = append(t.Columns, c.Head)
			if c.Number {
				t.Numeric = append(t.Numeric, i)
			}
			if c.NoWrap {
				t.Nowrap = append(t.Nowrap, i)
			}
		}
		for r := range b.Table.Rows {
			row := []string{}
			for i := range b.Table.Columns {
				row = append(row, b.Table.Cell(r, i))
			}
			t.Rows = append(t.Rows, row)
		}
		return "table", t
	}
	return string(b.Kind), b.Text
}

func sortedCopy(s []string) []string {
	c := append([]string(nil), s...)
	sort.Strings(c)
	return c
}

var commaNumber = regexp.MustCompile(`\d{1,3}(?:,\d{3})+`)

func pdfText(t *testing.T, path string) string {
	t.Helper()
	tool, err := exec.LookPath("pdftotext")
	if err != nil {
		t.Skip("pdftotext が無い（PDF のテキストの比較を飛ばす）")
	}
	out, err := exec.Command(tool, "-layout", "-enc", "UTF-8", path, "-").Output()
	if err != nil {
		t.Fatalf("pdftotext %s: %v", path, err)
	}
	return string(out)
}

func TestCompareWithGolden(t *testing.T) {
	font, _ := testFont(t)
	t.Logf("フォント: %s", font.Name)
	dir := t.TempDir()
	m, err := NewMetrics(font.Data)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, ref       string
		report, content any
	}{
		{"通常", "normal", loadJSON(t, "report"), loadJSON(t, "content")},
		{"大きい数と長い ID", "big", loadJSON(t, "big"), allAutoContent(t)},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ref := loadGoldenRef(t, c.ref)
			sameFont := ref.Font == font.Name
			if math.Abs(ref.Width-TableWidth) > 1e-6 {
				t.Errorf("表の幅: 記録 %f / Go %f", ref.Width, TableWidth)
			}
			blocks, err := report.Build(i18n.JA, c.report, c.content)
			if err != nil {
				t.Fatal(err)
			}
			// 1) ブロック（見出し・段落・表のセル・注記）が同じ
			if len(blocks) != len(ref.Blocks) {
				t.Fatalf("ブロックの数: Go %d / 記録 %d", len(blocks), len(ref.Blocks))
			}
			for k, b := range blocks {
				var kind string
				_ = json.Unmarshal(ref.Blocks[k][0], &kind)
				gk, gv := goBlock(b)
				var want, got any
				if gk == "table" {
					var pt goldenTable
					_ = json.Unmarshal(ref.Blocks[k][1], &pt)
					if pt.Numeric == nil {
						pt.Numeric = []int{}
					}
					if pt.Nowrap == nil {
						pt.Nowrap = []int{}
					}
					if pt.Notes == nil {
						pt.Notes = []string{}
					}
					want, got = pt, gv
				} else {
					_ = json.Unmarshal(ref.Blocks[k][1], &want)
					gb, _ := json.Marshal(gv)
					_ = json.Unmarshal(gb, &got)
				}
				if kind != gk || !reflect.DeepEqual(want, got) {
					t.Errorf("ブロック %d: 記録 %s %v / Go %s %v", k, kind, want, gk, got)
				}
			}
			// 2) 列幅: 実フォントで測ったもの・フォント無しの見積もりとも ±0.5pt
			tables := report.Tables(blocks)
			if len(tables) != len(ref.Widths) {
				t.Fatalf("表の数: Go %d / 記録 %d", len(tables), len(ref.Widths))
			}
			approx := func(s string) float64 { return reporttable.ApproxWidth(s, CellSize) }
			worst := 0.0
			for k, tb := range tables {
				pairs := map[string][2][]float64{"見積もり": {reporttable.Fit(*tb, 763, approx, reporttable.PDFFit), ref.Approx[k]}}
				if sameFont {
					pairs["実フォント"] = [2][]float64{m.ColWidths(tb), ref.Widths[k]}
				}
				for kind, pair := range pairs {
					g, p := pair[0], pair[1]
					if len(g) != len(p) {
						t.Errorf("%s %s: 列の数 %d / %d", tb.Columns[0].Head, kind, len(g), len(p))
						continue
					}
					for i := range g {
						worst = max(worst, math.Abs(g[i]-p[i]))
						if math.Abs(g[i]-p[i]) > 0.5 {
							t.Errorf("%s %s の %d 列（%s）: Go %.2f / 記録 %.2f", tb.Columns[0].Head, kind, i, tb.Columns[i].Head, g[i], p[i])
						}
					}
				}
			}
			t.Logf("表 %d 個・列幅の差の最大 %.4fpt", len(tables), worst)
			// 3) PDF から抜き出した見出しの並びと桁区切りの数値の並び
			data, _, err := Render(i18n.JA, blocks, strings.TrimSpace(c.content.(map[string]any)["title"].(string)), font)
			if err != nil {
				t.Fatal(err)
			}
			goPDF := filepath.Join(dir, "go"+string(rune('0'+i))+".pdf")
			if err := os.WriteFile(goPDF, data, 0o600); err != nil {
				t.Fatal(err)
			}
			gt := pdfText(t, goPDF)
			// 並びは pdftotext -layout の段組みの解釈に左右される。Windows の runner の pdftotext（Git for Windows 同梱の
			// poppler。版が違う）は折り返したセルの行の並びが記録と違うので、Windows では個数と値（多重集合）だけ比べる
			g, p := commaNumber.FindAllString(gt, -1), ref.Numbers
			switch {
			case len(g) == 0 || !reflect.DeepEqual(sortedCopy(g), sortedCopy(p)):
				t.Errorf("数値（多重集合）が違う:\nGo   %v\n記録 %v", g, p)
			case !reflect.DeepEqual(g, p) && runtime.GOOS != "windows":
				t.Errorf("数値の並びが違う:\nGo   %v\n記録 %v", g, p)
			case !reflect.DeepEqual(g, p):
				t.Logf("桁区切りの数値 %d 個は同じ（並びは pdftotext の版の違いで異なる）", len(g))
			default:
				t.Logf("桁区切りの数値 %d 個が同じ並び", len(g))
			}
			for _, text := range []struct{ who, s string }{{"Go", gt}} {
				pos := 0
				for _, b := range blocks {
					if b.Kind != report.Heading && b.Kind != report.Title {
						continue
					}
					j := strings.Index(text.s[pos:], b.Text)
					if j < 0 {
						t.Errorf("%s の PDF に見出し %q が（順に）無い", text.who, b.Text)
						break
					}
					pos += j + len(b.Text)
				}
			}
		})
	}
}
