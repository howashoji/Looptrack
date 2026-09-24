package reporttable

import (
	"sort"
	"strings"

	"golang.org/x/text/width"
)

// 列幅の規則（以前のレポート生成（1.0.0 より前）の列幅と同じ）。
//
// 数値（Number）と NoWrap の列は中身の最大幅に合わせて折り返さない。残りを文字の列（タイトル・説明）で分ける。
// 以前は文字数 × 固定の係数で見積もり、ID・状態の列を文字の列として比例配分していたため、「ABC-0058」「In Progress」
// 「602,209,566」が列の途中で折り返した。いまは実際のフォントで測る（Measure。無ければ ApproxWidth）。
// ページに収まらないときは、まず文字の列を縮め（各列の最低幅までは残す）、次に見出しの折り返しを許し、
// それでも足りなければ折り返さない列も比率で縮める（その場合だけ折り返す）。

// Mm は 1mm の pt。
const Mm = 72 / 25.4

// FitOptions は列幅の決め方の定数。
type FitOptions struct {
	Pad     float64 // セルの左右の余白（片側・pt）
	Slack   float64 // 折り返さない列の幅の遊び（pt。測った幅ちょうどだと丸めの誤差で折り返すことがある）
	MinText float64 // 文字の列に最低限残す幅（pt。中身が短ければその幅）
	MaxRows int     // 測る行の数の上限（大きい表で時間をかけない）
}

// PDFFit はトークンレポートの PDF の定数（セルの余白・遊び・25mm・200 行）。
var PDFFit = FitOptions{Pad: 4, Slack: 1.5, MinText: 25 * 2.834645669, MaxRows: 200}

// ApproxWidth はフォント無しで見積もる文字列の幅（pt）。全角は 1 文字 = size、半角は 0.6 × size
// （実際の等幅の半角 0.5 より広めに見積もる）。改行を含むなら最も長い行。
func ApproxWidth(s string, size float64) float64 {
	return maxLine(s, func(line string) float64 {
		w := 0.0
		for _, r := range line {
			switch width.LookupRune(r).Kind() {
			case width.EastAsianWide, width.EastAsianFullwidth:
				w += size
			default:
				w += size * 0.6
			}
		}
		return w
	})
}

// maxLine は改行で分けた各行の幅の最大。
func maxLine(s string, measure func(string) float64) float64 {
	best := 0.0
	for i, line := range strings.Split(s, "\n") {
		if w := measure(line); i == 0 || w > best {
			best = w
		}
	}
	return best
}

// Fit は表の列幅（pt）を返す。合計は total。measure は 1 行の文字列の幅（pt）。
func Fit(t Table, total float64, measure func(string) float64, o FitOptions) []float64 {
	cols := len(t.Columns)
	rows := t.Rows
	if o.MaxRows > 0 && len(rows) > o.MaxRows {
		rows = rows[:o.MaxRows]
	}
	cell := func(r []any, i int) string {
		if i < len(r) {
			return Text(r[i])
		}
		return ""
	}
	m := func(s string) float64 { return maxLine(s, measure) }
	pad := 2*o.Pad + o.Slack
	var fit, text []int
	for i := 0; i < cols; i++ {
		if t.Fixed(i) {
			fit = append(fit, i)
		} else {
			text = append(text, i)
		}
	}
	valueW, headW, natural := map[int]float64{}, map[int]float64{}, map[int]float64{}
	for _, i := range fit {
		w := 0.0
		for _, r := range rows {
			w = max(w, m(cell(r, i)))
		}
		valueW[i] = w + pad
		headW[i] = max(valueW[i], m(t.Columns[i].Head)+pad)
	}
	reserve := 0.0
	for _, i := range text {
		w := m(t.Columns[i].Head)
		for _, r := range rows {
			w = max(w, m(cell(r, i)))
		}
		natural[i] = w + pad
		reserve += min(natural[i], o.MinText)
	}
	sum := func(ws map[int]float64) float64 {
		s := 0.0
		for _, i := range fit { // 足す順を決めて、合計の丸めを毎回同じにする
			s += ws[i]
		}
		return s
	}
	fixed := map[int]float64{}
	switch {
	case sum(headW) <= total-reserve:
		for _, i := range fit {
			fixed[i] = headW[i]
		}
	case sum(valueW) <= total-reserve:
		// 見出しの折り返しを許す。余りは見出しとの差の小さい列から順に足して、なるべく見出しも 1 行にする
		for _, i := range fit {
			fixed[i] = valueW[i]
		}
		spare := total - reserve - sum(valueW)
		order := append([]int(nil), fit...)
		sort.SliceStable(order, func(a, b int) bool {
			return headW[order[a]]-valueW[order[a]] < headW[order[b]]-valueW[order[b]]
		})
		for _, i := range order {
			add := min(headW[i]-valueW[i], spare)
			fixed[i] += add
			spare -= add
		}
	default:
		if s := sum(valueW); s > 0 {
			k := max(total-reserve, total*0.5) / s
			for _, i := range fit {
				fixed[i] = valueW[i] * k
			}
		}
	}
	out := make([]float64, cols)
	if len(text) == 0 {
		extra := (total - sum(fixed)) / float64(max(cols, 1))
		for i := range out {
			out[i] = fixed[i] + extra
		}
		return out
	}
	rest := total - sum(fixed)
	// 文字の列: 中身の幅に比例（最低 1 割）
	all := 0.0
	for _, i := range text {
		all += natural[i]
	}
	if all == 0 {
		all = 1
	}
	shares, scale := map[int]float64{}, 0.0
	for _, i := range text {
		shares[i] = max(natural[i]/all, 0.1)
		scale += shares[i]
	}
	for i := range out {
		if t.Fixed(i) {
			out[i] = fixed[i]
		} else {
			out[i] = rest * shares[i] / scale
		}
	}
	return out
}
