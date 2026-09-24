package reporttable

import "testing"

func TestTextAndComma(t *testing.T) {
	for v, want := range map[any]string{nil: "", "IM-0058": "IM-0058", 0: "0", 999: "999", int64(4331803110): "4,331,803,110",
		-1234567: "-1,234,567", 1.5: "1.5"} {
		if got := Text(v); got != want {
			t.Errorf("Text(%v) = %q", v, got)
		}
	}
}

func TestApproxWidth(t *testing.T) {
	if got := ApproxWidth("IM-0058", 10); got != 42 {
		t.Errorf("半角 = %f", got)
	}
	if got := ApproxWidth("状態\nab", 10); got != 20 {
		t.Errorf("全角・改行 = %f", got)
	}
}

// 折り返さない列は中身の最大幅 + 余白 + 遊び、残りは文字の列。合計はページ幅。
func TestFitFixedColumns(t *testing.T) {
	tb := Table{Columns: []Column{{Head: "ID", NoWrap: true}, {Head: "タイトル"}, {Head: "合計", Number: true}},
		Rows: [][]any{{"IM-0058", "長いタイトル", int64(602209566)}}}
	m := func(s string) float64 { return ApproxWidth(s, 7.5) }
	w := Fit(tb, 500, m, PDFFit)
	pad := 2*PDFFit.Pad + PDFFit.Slack
	if w[0] != m("IM-0058")+pad || w[2] != m("602,209,566")+pad {
		t.Errorf("固定の列 = %v", w)
	}
	if s := w[0] + w[1] + w[2]; s < 500-1e-9 || s > 500+1e-9 {
		t.Errorf("合計 = %f", s)
	}
}
