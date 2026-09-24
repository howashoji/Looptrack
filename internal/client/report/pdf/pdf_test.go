package pdf

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/signintech/gopdf"

	"github.com/howashoji/looptrack/internal/client/report"
	"github.com/howashoji/looptrack/internal/i18n"
)

// テスト用のフォント。TOKEN_REPORT_FONT → 埋め込み → 既定の置き場 → 手元の OS にある日本語の TrueType の順。
// 無ければ skip。以前の実装と突き合わせるため、埋め込みのフォントは一時ファイルに書き出してパスを返す。
var testExtraFonts = []string{
	"/Library/Fonts/Arial Unicode.ttf", "/System/Library/Fonts/Supplemental/Arial Unicode.ttf",
	"/usr/share/fonts/truetype/fonts-japanese-gothic.ttf", "/usr/share/fonts/truetype/vlgothic/VL-Gothic-Regular.ttf",
	"C:/Windows/Fonts/BIZ-UDGothicR.ttc", "C:/Windows/Fonts/msgothic.ttc",
}

func testFont(t *testing.T) (Font, string) {
	t.Helper()
	home, _ := os.UserHomeDir()
	check := func(b []byte) error { _, err := NewMetrics(b); return err }
	f, err := FindFont(i18n.JA, os.Getenv, home, check, nil)
	if err == nil {
		if strings.HasPrefix(f.Name, "embedded:") {
			p := filepath.Join(t.TempDir(), "font.ttf")
			if err := os.WriteFile(p, f.Data, 0o600); err != nil {
				t.Fatal(err)
			}
			return f, p
		}
		return f, f.Name
	}
	for _, p := range testExtraFonts {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		ttf, err := trueType(data)
		if err == nil && check(ttf) == nil {
			return Font{Name: p, Data: ttf}, p
		}
	}
	t.Skip("日本語の TrueType フォントが無い（TOKEN_REPORT_FONT で指定できる）")
	return Font{}, ""
}

func loadJSON(t *testing.T, name string) any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := report.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// allAutoContent は数表 8 つとデータの限界の章だけの本文（以前のテストの列幅のケースと同じ）。
func allAutoContent(t *testing.T) map[string]any {
	c := loadJSON(t, "content").(map[string]any)
	secs := []any{}
	for _, a := range report.AutoTables {
		secs = append(secs, map[string]any{"auto": a})
	}
	all := c["sections"].([]any)
	out := map[string]any{}
	for k, v := range c {
		out[k] = v
	}
	out["sections"] = append(secs, all[len(all)-1])
	return out
}

func TestTrueTypeFormats(t *testing.T) {
	if _, err := trueType([]byte("OTTO\x00\x01\x00\x00\x00\x00\x00\x00")); err == nil || !strings.Contains(i18n.Text(i18n.JA, err), "CFF") {
		t.Errorf("CFF を拒否しない: %v", err)
	}
	if _, err := trueType([]byte("not a font file")); err == nil {
		t.Error("フォントでないものを受け付けた")
	}
	// TTC: 最初のフォントを単独の TrueType に組み直す（合成: 表 2 つ）
	sfnt := func(tables map[string][]byte, order []string) (dir []byte, body [][]byte) {
		dir = make([]byte, 12+16*len(order))
		binary.BigEndian.PutUint32(dir, 0x00010000)
		binary.BigEndian.PutUint16(dir[4:], uint16(len(order)))
		for i, tag := range order {
			copy(dir[12+16*i:], tag)
			binary.BigEndian.PutUint32(dir[12+16*i+12:], uint32(len(tables[tag])))
			body = append(body, tables[tag])
		}
		return dir, body
	}
	tables := map[string][]byte{"glyf": []byte("GLYFDATA"), "head": []byte("HEAD")}
	dir, body := sfnt(tables, []string{"glyf", "head"})
	ttc := make([]byte, 16)
	copy(ttc, "ttcf")
	binary.BigEndian.PutUint32(ttc[4:], 0x00010000)
	binary.BigEndian.PutUint32(ttc[8:], 1)
	binary.BigEndian.PutUint32(ttc[12:], 16)
	off := 16 + len(dir)
	for i, b := range body {
		binary.BigEndian.PutUint32(dir[12+16*i+8:], uint32(off))
		off += len(b)
	}
	ttc = append(ttc, dir...)
	for _, b := range body {
		ttc = append(ttc, b...)
	}
	out, err := trueType(ttc)
	if err != nil {
		t.Fatal(err)
	}
	if binary.BigEndian.Uint32(out) != 0x00010000 || binary.BigEndian.Uint16(out[4:]) != 2 {
		t.Fatalf("目録 = %x", out[:12])
	}
	for i, tag := range []string{"glyf", "head"} {
		rec := out[12+16*i:]
		start, n := binary.BigEndian.Uint32(rec[8:]), binary.BigEndian.Uint32(rec[12:])
		if string(rec[:4]) != tag || !bytes.Equal(out[start:start+n], tables[tag]) || start%4 != 0 {
			t.Errorf("表 %s: %d %d %q", tag, start, n, out[start:start+n])
		}
	}
}

func TestFindFontOrder(t *testing.T) {
	font, path := testFont(t)
	var warns []string
	getenv := func(k string) string {
		if k == FontEnv {
			return path
		}
		return ""
	}
	f, err := FindFont(i18n.JA, getenv, "", nil, func(s string) { warns = append(warns, s) })
	if err != nil || f.Name != path || !bytes.Equal(f.Data, font.Data) {
		t.Fatalf("%s を先に使わない: %s %v", FontEnv, f.Name, err)
	}
	// 読めない指定は注意を出して次を探す
	bad := filepath.Join(t.TempDir(), "bad.ttf")
	if err := os.WriteFile(bad, []byte("OTTOxxxxxxxxxxxx"), 0o600); err != nil {
		t.Fatal(err)
	}
	warns = nil
	f, err = FindFont(i18n.JA, func(k string) string {
		if k == FontEnv {
			return bad
		}
		return ""
	}, "", nil, func(s string) { warns = append(warns, s) })
	if len(warns) != 1 || !strings.Contains(warns[0], "CFF") {
		t.Errorf("注意 = %v", warns)
	}
	if err == nil && f.Name == bad {
		t.Error("読めないフォントを使った")
	}
}

func TestWrap(t *testing.T) {
	font, _ := testFont(t)
	m, err := NewMetrics(font.Data)
	if err != nil {
		t.Fatal(err)
	}
	w := m.Width("あ", 10)
	if w <= 0 {
		t.Fatal("幅が 0")
	}
	if got := m.Wrap("あいうえお", 10, w*3+0.01); strings.Join(got, "|") != "あいう|えお" {
		t.Errorf("折り返し = %v", got)
	}
	// 行頭禁則: 句点は次の行の頭に置かない
	if got := m.Wrap("あいう。えお", 10, w*3+0.01); strings.Join(got, "|") != "あい|う。えお" && strings.Join(got, "|") != "あい|う。え|お" {
		t.Errorf("禁則 = %v", got)
	}
	// 半角の語は途中で切らない（語が 1 行に収まらないときだけ切る）
	if got := m.Wrap("あいう（REQ-0058）", 10, m.Width("あいう（REQ-00", 10)+0.01); strings.Join(got, "|") != "あいう|（REQ-0058）" {
		t.Errorf("半角の語 = %v", got)
	}
	if got := m.Wrap("ABCDEFGH", 10, m.Width("ABCD", 10)+0.01); strings.Join(got, "|") != "ABCD|EFGH" {
		t.Errorf("長い語 = %v", got)
	}
	if got := m.Wrap("一\n二", 10, 100); strings.Join(got, "|") != "一|二" {
		t.Errorf("改行 = %v", got)
	}
	if got := m.Wrap("あい", 10, 1); len(got) != 2 {
		t.Errorf("1 文字も入らない幅 = %v", got)
	}
}

// TestRenderFontMetricsError は、フォントが読めないときの Render のエラー文面が
// 「フォント {name}: {reason}」の形になり、ID がそのまま出ないことを確かめる。
// report.render.err.font_metrics は内側のエラーを i18n.Errorf に直接渡すだけで文面へ
// 展開される（先に i18n.Text で組み立てる回避は不要）。
//
// 素のエラー（gopdf 自身が返す *errors.errorString 相当）と、i18n のエラー
// （NewMetrics が実際に返しうる report.metrics.err.* 相当）の両方の経路を確かめる。
// 素のエラーの経路だけでは、回避コード（"reason" に err ではなく err.Error() /
// i18n.Text(lang, err) を渡す退行）を入れても文面が変わらず検査にならない
// （どちらも Text(lang, err) が err.Error() を素通しするだけなので）。i18n のエラーの
// 経路は、内側の NewMetrics を差し替えて人工的に作る（合成フォントで
// AddTTFFontData だけを失敗させるのは実務上難しいため）。
func TestRenderFontMetricsError(t *testing.T) {
	t.Run("素のエラー", func(t *testing.T) {
		_, _, err := Render(i18n.JA, nil, "テスト", Font{Name: "bad-font", Data: []byte("not a real ttf")})
		if err == nil {
			t.Fatal("読めないフォントでエラーにならない")
		}
		got := i18n.Text(i18n.JA, err)
		if !strings.HasPrefix(got, "フォント bad-font: ") {
			t.Errorf("文面 = %q（フォント bad-font: … の形のはず）", got)
		}
		if strings.Contains(got, "report.render.err.font_metrics") {
			t.Errorf("文面に ID が漏れている: %q", got)
		}
	})
	t.Run("i18n のエラー", func(t *testing.T) {
		orig := newMetrics
		newMetrics = func([]byte) (*Metrics, error) { return nil, i18n.Errorf("report.metrics.err.no_widths") }
		defer func() { newMetrics = orig }()
		_, _, err := Render(i18n.JA, nil, "テスト", Font{Name: "bad-font", Data: []byte("x")})
		if err == nil {
			t.Fatal("読めないフォントでエラーにならない")
		}
		got := i18n.Text(i18n.JA, err)
		wantReason := i18n.T(i18n.JA, "report.metrics.err.no_widths")
		if !strings.Contains(got, wantReason) {
			t.Errorf("文面 = %q（内側の i18n エラーの文面 %q を含むはず）", got, wantReason)
		}
		if strings.Contains(got, "report.metrics.err.no_widths") {
			t.Errorf("文面に内側のエラーの ID が漏れている: %q", got)
		}
	})
}

// TestRenderFontLoadError は、AddTTFFontData が失敗したときの Render のエラー文面
// （report.render.err.font_load）でも、内側が i18n のエラーである経路が正しく展開されることを
// 確かめる。NewMetrics を通過しつつ AddTTFFontData だけ失敗する合成フォントを
// 作るのは実務上困難なので、addTTFFontData を差し替えて人工的に作る。
func TestRenderFontLoadError(t *testing.T) {
	font, _ := testFont(t) // NewMetrics を通す必要があるので、実在するフォントを使う
	orig := addTTFFontData
	addTTFFontData = func(*gopdf.GoPdf, string, []byte) error { return i18n.Errorf("report.font.err.cff") }
	defer func() { addTTFFontData = orig }()
	_, _, err := Render(i18n.JA, nil, "テスト", font)
	if err == nil {
		t.Fatal("フォントの登録に失敗してもエラーにならない")
	}
	got := i18n.Text(i18n.JA, err)
	wantReason := i18n.T(i18n.JA, "report.font.err.cff")
	if !strings.Contains(got, wantReason) {
		t.Errorf("文面 = %q（内側の i18n エラーの文面 %q を含むはず）", got, wantReason)
	}
	if strings.Contains(got, "report.font.err.cff") {
		t.Errorf("文面に内側のエラーの ID が漏れている: %q", got)
	}
	if strings.Contains(got, "report.render.err.font_load") {
		t.Errorf("文面に ID が漏れている: %q", got)
	}
}

// 実際のフォントで組んで、折り返さない列のセル（見出しを除く）が 1 行に収まる。
// 日英の両方で見る（英語の見出し・注記は幅が変わるので、列幅の配分が壊れていないかは言語ごとに確かめる）。
func TestNowrapCellsAreOneLineInPDF(t *testing.T) {
	font, _ := testFont(t)
	for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
		t.Run(string(lang), func(t *testing.T) {
			blocks, err := report.Build(lang, loadJSON(t, "big"), allAutoContent(t))
			if err != nil {
				t.Fatal(err)
			}
			data, doc, err := Render(lang, blocks, "テスト", font)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.HasPrefix(data, []byte("%PDF")) || len(data) < 2000 {
				t.Fatalf("PDF でない: %d バイト", len(data))
			}
			tables := report.Tables(blocks)
			if len(doc.Widths) != len(tables) {
				t.Fatalf("表の数 %d / %d", len(doc.Widths), len(tables))
			}
			for k, tb := range tables {
				widths := doc.Widths[k]
				sum := 0.0
				for _, w := range widths {
					sum += w
				}
				if sum < TableWidth-1e-3 || sum > TableWidth+1e-3 {
					t.Errorf("%s: 合計 %f", tb.Columns[0].Head, sum)
				}
				for i := range tb.Columns {
					if !tb.Fixed(i) {
						continue
					}
					for r := range tb.Rows {
						if lines := doc.cellLines(tb.Cell(r, i), widths[i]); len(lines) != 1 {
							t.Errorf("%s %q: %d 行（幅 %f）", tb.Columns[i].Head, tb.Cell(r, i), len(lines), widths[i])
						}
					}
				}
			}
			text := doc.Text()
			for _, s := range []string{"IM-0058", "In Progress", "4,331,803,110", "602,209,566", "6,022,095,660", "ABCD-0141"} {
				if !strings.Contains(text, s) {
					t.Errorf("描いた文字列に %s が無い", s)
				}
			}
		})
	}
}

func TestCommand(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, v []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, v, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	rb, _ := os.ReadFile(filepath.Join("..", "testdata", "report.json"))
	cb, _ := os.ReadFile(filepath.Join("..", "testdata", "content.json"))
	rp, cp := write("r.json", rb), write("c.json", cb)
	run := func(env Env, args ...string) (int, string) {
		var out, errb bytes.Buffer
		code := Main(args, &out, &errb, env)
		return code, out.String() + errb.String()
	}
	none := Env{Getenv: func(string) string { return "" }, Lang: i18n.JA}
	code, out := run(none, "--report", rp, "--content", cp, "--check")
	if code != 0 || !strings.Contains(out, "検査 OK: 章 7・表 4（合計 6,600 トークン）") {
		t.Errorf("--check: %d %s", code, out)
	}
	bad := write("bad.json", bytes.Replace(cb, []byte("データの限界"), []byte("まとめ"), 1))
	if code, out = run(none, "--report", rp, "--content="+bad, "--check"); code != 1 || !strings.Contains(out, "データの限界") {
		t.Errorf("データの限界なし: %d %s", code, out)
	}
	if code, out = run(none, "--report", rp); code != 2 {
		t.Errorf("--content なし: %d %s", code, out)
	}
	if code, out = run(none, "--report", filepath.Join(dir, "nothing.json"), "--content", cp, "--check"); code != 1 || !strings.Contains(out, "入力を読めません") {
		t.Errorf("無いファイル: %d %s", code, out)
	}
	if code, out = run(none, "--report", rp, "--content", cp); code != 1 || !strings.Contains(out, "--out") {
		t.Errorf("--out なし: %d %s", code, out)
	}
	_, path := testFont(t)
	withFont := Env{Lang: i18n.JA, Getenv: func(k string) string {
		if k == FontEnv {
			return path
		}
		return ""
	}}
	pdfPath := filepath.Join(dir, "sub", "レポート.pdf")
	code, out = run(withFont, "--report", rp, "--content", cp, "--out", pdfPath)
	if code != 0 || !strings.Contains(out, "PDF: "+pdfPath) {
		t.Fatalf("PDF: %d %s", code, out)
	}
	data, err := os.ReadFile(pdfPath)
	if err != nil || !bytes.HasPrefix(data, []byte("%PDF")) || len(data) < 2000 {
		t.Errorf("PDF の中身: %d %v", len(data), err)
	}
}
