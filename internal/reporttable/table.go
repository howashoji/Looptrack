// Package reporttable は帳票の表のモデル（列・行・注記）と、PDF の列幅の規則を持つ。
//
// 同じ表を xlsx（internal/xlsxreport。サーバの usage report の xlsx）と PDF（internal/client/report/pdf。
// トークンレポート）で組むので、表の形はここで 1 つにする。出力の形式ごとの見た目は各パッケージが持つ。
package reporttable

import (
	"fmt"
	"strconv"
)

// Column は表の 1 列。
type Column struct {
	Head   string
	Width  float64 // xlsx の列幅（文字数。0 は既定）。PDF は中身から決める（Fit）
	Number bool    // 数値の列: xlsx は数値のまま桁区切りで表示、PDF は右寄せで折り返さない
	NoWrap bool    // 折り返さない文字の列（ID・状態など。PDF の列幅を中身の最大幅に合わせる）
}

// Table は表 1 つ（xlsx は 1 シート）。Rows の値は string / int / int64 / float64。
type Table struct {
	Sheet   string // xlsx のシート名（31 文字まで・[]:*?/\ を含まない）
	Title   string
	Sub     string // 表題の下の 1 行（期間など）
	Columns []Column
	Rows    [][]any
	Notes   []string // 表の下の注記（PDF）
}

// Fixed は折り返さない列（数値か NoWrap）か。
func (t Table) Fixed(i int) bool {
	return i >= 0 && i < len(t.Columns) && (t.Columns[i].Number || t.Columns[i].NoWrap)
}

// Cell は i 行 j 列の表示の文字列（範囲外は ""）。
func (t Table) Cell(i, j int) string {
	if i < 0 || i >= len(t.Rows) || j < 0 || j >= len(t.Rows[i]) {
		return ""
	}
	return Text(t.Rows[i][j])
}

// Text はセルの値を表示の文字列にする。整数は桁区切り（1,234）。
func Text(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case int:
		return Comma(int64(x))
	case int64:
		return Comma(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return fmt.Sprint(x)
	}
}

// Comma は 3 桁ごとの桁区切り。
func Comma(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := ""
	if n < 0 {
		neg, s = "-", s[1:]
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return neg + s
}
