package xlsxreport

import (
	"bytes"
	"testing"

	"github.com/xuri/excelize/v2"

	"github.com/howashoji/looptrack/internal/reporttable"
)

// 数表だけの帳票（シートごとの表・数値は数値のまま）。
func TestBuildTables(t *testing.T) {
	b, err := BuildTables([]reporttable.Table{
		{Sheet: "概要", Title: "トークンレポート", Sub: "期間 2026-09-01 〜 2026-09-18",
			Columns: []reporttable.Column{{Head: "項目", Width: 20}, {Head: "トークン", Number: true}},
			Rows:    [][]any{{"合計", int64(123456)}, {"未帰属", int64(0)}}},
		{Sheet: "イシュー別", Title: "イシュー別", Columns: []reporttable.Column{{Head: "ID"}, {Head: "合計", Number: true}},
			Rows: [][]any{{"REQ-0001", int64(99)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("xlsx として開けない: %v", err)
	}
	defer f.Close()
	if got := f.GetSheetList(); len(got) != 2 || got[0] != "概要" || got[1] != "イシュー別" {
		t.Errorf("シート = %v", got)
	}
	if v, _ := f.GetCellValue("概要", "A1"); v != "トークンレポート" {
		t.Errorf("表題 = %q", v)
	}
	if v, _ := f.GetCellValue("概要", "B5", excelize.Options{RawCellValue: true}); v != "123456" {
		t.Errorf("数値のセル = %q", v)
	}
	if v, _ := f.GetCellValue("概要", "B5"); v != "123,456" {
		t.Errorf("桁区切りの表示 = %q", v)
	}
	if v, _ := f.GetCellValue("イシュー別", "A5"); v != "REQ-0001" {
		t.Errorf("2 枚目の行 = %q", v)
	}
}
