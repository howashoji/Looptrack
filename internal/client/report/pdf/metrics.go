package pdf

import (
	"strings"

	"github.com/signintech/gopdf/fontmaker/core"

	"github.com/howashoji/looptrack/internal/i18n"
)

// Metrics はフォントの文字幅（gopdf が PDF に書く幅と同じ。1000 分の 1 em の整数）。
type Metrics struct {
	chars  map[int]uint
	widths []uint
	upem   uint
	cache  map[rune]float64
}

// NewMetrics はフォントの幅を読む。日本語（あ・漢字）と数字を描けないフォントは誤り。
func NewMetrics(ttf []byte) (*Metrics, error) {
	var p core.TTFParser
	if err := p.ParseFontData(ttf); err != nil {
		return nil, err
	}
	m := &Metrics{chars: p.Chars(), widths: p.Widths(), upem: p.UnitsPerEm(), cache: map[rune]float64{}}
	if m.upem == 0 || len(m.widths) == 0 {
		return nil, i18n.Errorf("report.metrics.err.no_widths")
	}
	// 描けることを確かめる文字。**判定に使う語なので訳さない**（英語のレポートでも、日本語の題名・イシュー名は描ける必要がある）。
	for _, r := range "あ漢0,ー" {
		if _, ok := m.chars[int(r)]; !ok {
			return nil, i18n.Errorf("report.metrics.err.missing_glyph", "char", string(r))
		}
	}
	return m, nil
}

// advance は 1 文字の幅（1pt の文字の大きさあたり）。無い文字は 0（gopdf も描かない）。
func (m *Metrics) advance(r rune) float64 {
	if w, ok := m.cache[r]; ok {
		return w
	}
	w := 0.0
	if g, ok := m.chars[int(r)]; ok {
		if g >= uint(len(m.widths)) {
			g = uint(len(m.widths)) - 1
		}
		u := m.widths[g]
		if m.upem != 1000 {
			u = u * 1000 / m.upem
		}
		w = float64(u) / 1000
	}
	m.cache[r] = w
	return w
}

// Width は 1 行の文字列の幅（pt）。
func (m *Metrics) Width(s string, size float64) float64 {
	w := 0.0
	for _, r := range s {
		w += m.advance(r)
	}
	return w * size
}

// CellWidth は表のセルの文字列の幅（pt。改行を含むなら最も長い行）。
func (m *Metrics) CellWidth(s string) float64 {
	best := 0.0
	for _, line := range strings.Split(s, "\n") {
		best = max(best, m.Width(line, CellSize))
	}
	return best
}

// 行頭に置かない文字（句読点・閉じ括弧・長音など）と行末に置かない文字（開き括弧）。
// **判定に使う語なので訳さない**（日本語の組版の禁則そのもの）。
const (
	noStart = "、。，．,.）)」』】〕〉》］]｝}・ー～！？!?：；:;ぁぃぅぇぉっゃゅょゎァィゥェォッャュョヮヵヶ％%"
	noEnd   = "（(「『【〔〈《［[｛{"
)

// latin は半角の語を作る文字か（空白以外の ASCII）。
func latin(r rune) bool { return r > ' ' && r < 0x7f }

// Wrap は文字列を幅 width（pt）に収まる行に分ける（改行は保つ）。日本語の組版と同じく文字の間で折り返し
// （以前の PDF の作り方の折り返しに近い）、半角の語はなるべく切らず、行頭・行末の禁則を守る。1 文字も入らない幅でも 1 行に 1 文字は置く。
func (m *Metrics) Wrap(s string, size, width float64) []string {
	var out []string
	for _, para := range strings.Split(s, "\n") {
		rs := []rune(para)
		if len(rs) == 0 {
			out = append(out, "")
			continue
		}
		start := 0
		for start < len(rs) {
			w, end := 0.0, start
			for end < len(rs) {
				a := m.advance(rs[end]) * size
				if end > start && w+a > width+1e-6 {
					break
				}
				w += a
				end++
			}
			if end < len(rs) {
				// 禁則: 次の行の頭が行頭禁則なら 1 文字戻す。行末が開き括弧なら次の行へ送る
				brk := end
				// 半角の語（ID・英単語・数値）の途中なら語の頭で折り返す（語が 1 行に収まらないときだけ途中で切る）
				for brk > start && latin(rs[brk]) && latin(rs[brk-1]) {
					brk--
				}
				if brk == start {
					brk = end
				}
				for brk-1 > start && strings.ContainsRune(noStart, rs[brk]) {
					brk--
				}
				for brk-1 > start && strings.ContainsRune(noEnd, rs[brk-1]) {
					brk--
				}
				if brk > start {
					end = brk
				}
			}
			out = append(out, string(rs[start:end]))
			start = end
			for start < len(rs) && start > 0 && rs[start] == ' ' { // 折り返した行の頭の空白は捨てる
				start++
			}
		}
	}
	return out
}
