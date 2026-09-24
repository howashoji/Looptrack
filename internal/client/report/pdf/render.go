// Package pdf はトークンレポートのブロック（internal/client/report）を PDF に描く（gopdf・MIT）。
//
// 見た目は以前の PDF の作り方（1.0.0 より前）に合わせる: A4 横・余白 14mm・文字の大きさと行送り・色・表の罫線と
// 縞・見出し行の繰り返し・足もとの題名とページ番号。表の列幅は reporttable.Fit を実際のフォントの幅で決める。
package pdf

import (
	"bytes"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/signintech/gopdf"

	"github.com/howashoji/looptrack/internal/client/report"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/reporttable"
)

// 寸法（pt）。A4 横・外の余白 14mm・枠の内側の余白 6pt（以前の PDF の作り方と同じ）。
const (
	PageW    = 841.8897637795275
	PageH    = 595.2755905511812
	Margin   = 14 * reporttable.Mm
	FramePad = 6.0
	CellSize = 7.5 // 表のセルの文字の大きさ
	CellPad  = 4.0 // 表のセルの左右の余白（CELL_PAD）
	cellLead = 10.0
	cellVPad = 2.0
	gridLine = 0.4
)

// TableWidth は表の幅（ページの幅 - 28mm）。以前の PDF の作り方は枠より広い表を中央に置いたので、
// 表は左右の余白 14mm の内側いっぱいになる。
const TableWidth = PageW - 2*Margin

type rgb [3]uint8

var (
	navy     = rgb{0x1f, 0x3a, 0x68}
	gray55   = rgb{0x55, 0x55, 0x55}
	gray66   = rgb{0x66, 0x66, 0x66}
	gray77   = rgb{0x77, 0x77, 0x77}
	black    = rgb{0, 0, 0}
	white    = rgb{0xff, 0xff, 0xff}
	stripe   = rgb{0xf3, 0xf5, 0xf9}
	gridGray = rgb{0xb8, 0xbf, 0xcc}
)

// style は段落の書式。
type style struct {
	size, leading, before, after float64
	color                        rgb
}

var (
	stTitle   = style{17, 23, 0, 4 * reporttable.Mm, black}
	stMeta    = style{8.5, 12, 0, 0, gray55}
	stHeading = style{12.5, 17, 6 * reporttable.Mm, 2 * reporttable.Mm, navy}
	stBody    = style{9.5, 15, 0, 2 * reporttable.Mm, black}
	stNote    = style{7.5, 11, 0, 0, gray66}
)

const fontName = "ReportJP"

// Run は描いた文字列 1 つ（テストで中身と位置を確かめる）。Y は上端からのベースライン。
type Run struct {
	Page int
	X, Y float64
	Size float64
	Text string
}

// Doc は組版の途中の状態。
type Doc struct {
	gp     *gopdf.GoPdf
	m      *Metrics
	lang   i18n.Lang
	title  string
	page   int
	y      float64 // 次に置く位置（上端から）
	atTop  bool    // ページの頭（spaceBefore を捨てる）
	Runs   []Run
	Widths [][]float64 // 表ごとの列幅（テスト用）
}

const (
	top    = Margin + FramePad
	bottom = PageH - Margin - FramePad
	left   = Margin + FramePad
	width  = PageW - 2*Margin - 2*FramePad
)

// newMetrics と addTTFFontData は Render が内側のフォント読み込みに使う関数。既定は本物の実装
// （NewMetrics・gopdf.GoPdf.AddTTFFontData）で、テストだけがすり替える。すり替える理由は、
// gopdf 自身が返す素のエラーでは「内側のエラーが i18n のエラーである場合に文面が正しく展開される」
// という経路を通せない（gopdf のエラーは i18n のエラーではないので、i18n.Errorf に
// そのまま渡しても渡さなくても見た目の文面が変わらず、回避コードの要不要を区別できない）ため。
var (
	newMetrics     = NewMetrics
	addTTFFontData = func(gp *gopdf.GoPdf, name string, data []byte) error { return gp.AddTTFFontData(name, data) }
)

// Render はブロックを PDF にする。title は PDF の題名と足もとの文字列。
func Render(lang i18n.Lang, blocks []report.Block, title string, font Font) ([]byte, *Doc, error) {
	m, err := newMetrics(font.Data)
	if err != nil {
		return nil, nil, i18n.Errorf("report.render.err.font_metrics", "name", font.Name, "reason", err)
	}
	gp := &gopdf.GoPdf{}
	gp.Start(gopdf.Config{Unit: gopdf.UnitPT, PageSize: gopdf.Rect{W: PageW, H: PageH}})
	gp.SetInfo(gopdf.PdfInfo{Title: title, Author: "token-report", Creator: "looptrack report pdf", CreationDate: time.Now()})
	if err := addTTFFontData(gp, fontName, font.Data); err != nil {
		return nil, nil, i18n.Errorf("report.render.err.font_load", "name", font.Name, "reason", err)
	}
	d := &Doc{gp: gp, m: m, lang: lang, title: title}
	d.newPage()
	for _, b := range blocks {
		if err := d.block(b); err != nil {
			return nil, nil, err
		}
	}
	var buf bytes.Buffer
	if err := gp.Write(&buf); err != nil {
		return nil, nil, err
	}
	return buf.Bytes(), d, nil
}

func (d *Doc) newPage() {
	d.gp.AddPage()
	d.page++
	d.y, d.atTop = top, true
	// 足もと（題名とページ番号）
	d.text(Margin, PageH-8*reporttable.Mm, 7.5, gray77, d.title)
	num := fmt.Sprint(d.page)
	d.text(PageW-Margin-d.m.Width(num, 7.5), PageH-8*reporttable.Mm, 7.5, gray77, num)
}

// text は baseline（上端から）に文字列を描く。
func (d *Doc) text(x, baseline, size float64, c rgb, s string) {
	if s == "" {
		return
	}
	_ = d.gp.SetFont(fontName, "", size)
	d.gp.SetTextColor(c[0], c[1], c[2])
	d.gp.SetXY(x, baseline)
	_ = d.gp.Text(s)
	d.Runs = append(d.Runs, Run{Page: d.page, X: x, Y: baseline, Size: size, Text: s})
}

// space は次に置くものの spaceBefore を足す（ページの頭では捨てる）。
func (d *Doc) space(before float64) {
	if !d.atTop {
		d.y += before
	}
}

// ensure は高さ h が入らなければ改ページする。
func (d *Doc) ensure(h float64) bool {
	if d.y+h > bottom+1e-6 && !d.atTop {
		d.newPage()
		return true
	}
	return false
}

// paragraph は段落を描く（ページをまたぐときは行で分ける）。indent は左の字下げ。
func (d *Doc) paragraph(s string, st style, indent float64, bullet string) {
	lines := d.m.Wrap(s, st.size, width-indent)
	d.space(st.before)
	if d.ensure(st.leading) {
		d.space(st.before)
	}
	for i, line := range lines {
		if d.y+st.leading > bottom+1e-6 && !d.atTop {
			d.newPage()
		}
		if i == 0 && bullet != "" {
			d.text(left, d.y+st.size, st.size, st.color, bullet)
		}
		d.text(left+indent, d.y+st.size, st.size, st.color, line)
		d.y += st.leading
		d.atTop = false
	}
	d.y += st.after
}

func (d *Doc) block(b report.Block) error {
	switch b.Kind {
	case report.Title:
		d.paragraph(b.Text, stTitle, 0, "")
	case report.Meta:
		for _, l := range b.Lines {
			d.paragraph(l, stMeta, 0, "")
		}
	case report.Heading:
		// 見出しだけがページの末尾に残らないよう、見出しと本文 2 行分が入らなければ改ページする
		if !d.atTop && d.y+stHeading.before+stHeading.leading+2*stBody.leading > bottom+1e-6 {
			d.newPage()
		}
		d.paragraph(b.Text, stHeading, 0, "")
	case report.Paragraph:
		d.paragraph(b.Text, stBody, 0, "")
	case report.Note:
		d.paragraph(b.Text, stNote, 0, "")
	case report.Bullets:
		for _, item := range b.Lines {
			d.paragraph(item, stBody, 12, i18n.T(d.lang, "report.render.bullet"))
		}
	case report.TableKind:
		d.table(b.Table)
		for _, n := range b.Table.Notes {
			d.paragraph(i18n.T(d.lang, "report.render.note", "text", n), stNote, 0, "")
		}
		d.y += 2 * reporttable.Mm
	default:
		return i18n.Errorf("report.render.err.unknown_block", "kind", string(b.Kind))
	}
	return nil
}

// ColWidths は表の列幅（pt）。
func (m *Metrics) ColWidths(t *reporttable.Table) []float64 {
	return reporttable.Fit(*t, TableWidth, func(s string) float64 { return m.Width(s, CellSize) }, reporttable.PDFFit)
}

// cellLines はセルの行（幅は列幅から左右の余白を引いたもの）。
func (d *Doc) cellLines(s string, w float64) []string {
	return d.m.Wrap(s, CellSize, w-2*CellPad)
}

func (d *Doc) table(t *reporttable.Table) {
	widths := d.m.ColWidths(t)
	d.Widths = append(d.Widths, widths)
	x0 := Margin
	type row struct {
		lines  [][]string
		height float64
	}
	mk := func(cells []string) row {
		r := row{}
		n := 1
		for i, c := range cells {
			ls := d.cellLines(c, widths[i])
			r.lines = append(r.lines, ls)
			n = max(n, len(ls))
		}
		r.height = float64(n)*cellLead + 2*cellVPad
		return r
	}
	heads := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		heads[i] = c.Head
	}
	head := mk(heads)
	draw := func(r row, fill rgb, textColor rgb) {
		x := x0
		d.gp.SetLineWidth(gridLine)
		for i, w := range widths {
			// 文字の色も PDF の塗りの色なので、枠ごとに塗りの色を設定し直す
			d.gp.SetFillColor(fill[0], fill[1], fill[2])
			d.gp.SetStrokeColor(gridGray[0], gridGray[1], gridGray[2])
			d.gp.RectFromUpperLeftWithStyle(x, d.y, w, r.height, "FD")
			for j, line := range r.lines[i] {
				baseline := d.y + cellVPad + CellSize + float64(j)*cellLead
				tx := x + CellPad
				if t.Columns[i].Number {
					tx = x + w - CellPad - d.m.Width(line, CellSize)
				}
				d.text(tx, baseline, CellSize, textColor, line)
			}
			x += w
		}
		d.y += r.height
		d.atTop = false
	}
	drawHead := func() { draw(head, navy, white) }
	rows := make([]row, len(t.Rows))
	for i := range t.Rows {
		cells := make([]string, len(t.Columns))
		for j := range cells {
			cells[j] = t.Cell(i, j)
		}
		rows[i] = mk(cells)
	}
	// 見出しと 1 行目が入らなければ改ページ
	if len(rows) > 0 {
		d.ensure(head.height + rows[0].height)
	} else {
		d.ensure(head.height)
	}
	drawHead()
	for i, r := range rows {
		if d.y+r.height > bottom+1e-6 {
			d.newPage()
			drawHead()
		}
		fill := white
		if i%2 == 1 {
			fill = stripe
		}
		draw(r, fill, black)
	}
}

// Text は描いた文字列をページごとに上から、同じ高さは左から並べてつないだもの（テスト用。行は改行で分ける）。
func (d *Doc) Text() string {
	runs := append([]Run(nil), d.Runs...)
	sort.SliceStable(runs, func(i, j int) bool {
		a, b := runs[i], runs[j]
		if a.Page != b.Page {
			return a.Page < b.Page
		}
		if math.Abs(a.Y-b.Y) > 0.01 {
			return a.Y < b.Y
		}
		return a.X < b.X
	})
	var b strings.Builder
	for i, r := range runs {
		if i > 0 {
			if r.Page != runs[i-1].Page || math.Abs(r.Y-runs[i-1].Y) > 0.01 {
				b.WriteByte('\n')
			} else {
				b.WriteByte(' ')
			}
		}
		b.WriteString(r.Text)
	}
	return b.String()
}
