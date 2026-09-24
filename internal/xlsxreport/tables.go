package xlsxreport

import (
	"bytes"
	"fmt"

	"github.com/xuri/excelize/v2"

	"github.com/howashoji/looptrack/internal/reporttable"
)

// 数表だけの帳票（トークンレポートの集計など）。シートごとに表題・説明・見出し・行を持つ。
// 見た目（見出しの色・罫線・固定・オートフィルタ・印刷の設定）は課題管理表にそろえる。
// 表のモデル（reporttable.Table）は PDF のトークンレポートと共通。Notes は xlsx には出さない。

// BuildTables は表をシートに並べた xlsx を返す。
func BuildTables(tables []reporttable.Table) ([]byte, error) {
	f := excelize.NewFile()
	defer f.Close()
	st, err := newStyles(f)
	if err != nil {
		return nil, err
	}
	num, err := f.NewStyle(&excelize.Style{NumFmt: 3, Alignment: &excelize.Alignment{Vertical: "center"}, Border: []excelize.Border{
		{Type: "left", Color: "D9DDE5", Style: 1}, {Type: "right", Color: "D9DDE5", Style: 1},
		{Type: "top", Color: "D9DDE5", Style: 1}, {Type: "bottom", Color: "D9DDE5", Style: 1},
	}})
	if err != nil {
		return nil, err
	}
	for i, t := range tables {
		if i == 0 {
			if err := f.SetSheetName("Sheet1", t.Sheet); err != nil {
				return nil, err
			}
		} else if _, err := f.NewSheet(t.Sheet); err != nil {
			return nil, err
		}
		if err := writeTable(f, t, st, num); err != nil {
			return nil, fmt.Errorf("%s: %w", t.Sheet, err)
		}
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeTable(f *excelize.File, t reporttable.Table, st styleSet, num int) error {
	sheet := t.Sheet
	for _, h := range []struct {
		cell, value string
		style       int
	}{{"A1", t.Title, st.title}, {"A2", t.Sub, st.sub}} {
		if err := f.SetCellStr(sheet, h.cell, h.value); err != nil {
			return err
		}
		if err := f.SetCellStyle(sheet, h.cell, h.cell, h.style); err != nil {
			return err
		}
	}
	for i, c := range t.Columns {
		col, err := excelize.ColumnNumberToName(i + 1)
		if err != nil {
			return err
		}
		width := c.Width
		if width == 0 {
			width = 14
		}
		if err := f.SetColWidth(sheet, col, col, width); err != nil {
			return err
		}
		cell := fmt.Sprintf("%s%d", col, headerRow)
		if err := f.SetCellStr(sheet, cell, c.Head); err != nil {
			return err
		}
		if err := f.SetCellStyle(sheet, cell, cell, st.header); err != nil {
			return err
		}
	}
	for r, row := range t.Rows {
		for i, v := range row {
			if i >= len(t.Columns) {
				break
			}
			col, err := excelize.ColumnNumberToName(i + 1)
			if err != nil {
				return err
			}
			cell := fmt.Sprintf("%s%d", col, headerRow+1+r)
			style := st.cell
			if s, ok := v.(string); ok {
				err = f.SetCellStr(sheet, cell, s)
			} else {
				err = f.SetCellValue(sheet, cell, v)
				if t.Columns[i].Number {
					style = num
				}
			}
			if err != nil {
				return err
			}
			if err := f.SetCellStyle(sheet, cell, cell, style); err != nil {
				return err
			}
		}
	}
	if len(t.Columns) == 0 {
		return nil
	}
	last, err := excelize.ColumnNumberToName(len(t.Columns))
	if err != nil {
		return err
	}
	if err := f.AutoFilter(sheet, fmt.Sprintf("A%d:%s%d", headerRow, last, headerRow+len(t.Rows)), nil); err != nil {
		return err
	}
	first := fmt.Sprintf("A%d", headerRow+1)
	if err := f.SetPanes(sheet, &excelize.Panes{Freeze: true, YSplit: headerRow, TopLeftCell: first,
		ActivePane: "bottomLeft", Selection: []excelize.Selection{{SQRef: first, ActiveCell: first, Pane: "bottomLeft"}}}); err != nil {
		return err
	}
	fit, landscape := true, "landscape"
	if err := f.SetSheetProps(sheet, &excelize.SheetPropsOptions{FitToPage: &fit}); err != nil {
		return err
	}
	return f.SetPageLayout(sheet, &excelize.PageLayoutOptions{Orientation: &landscape, FitToWidth: intPtr(1), FitToHeight: intPtr(0)})
}
