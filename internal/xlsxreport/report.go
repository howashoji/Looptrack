// Package xlsxreport はイシュー一覧を課題管理表（xlsx）に組み立てる。
// 画面のボタンと CLI（looptrack issue export）はどちらもこの 1 か所を使う。
package xlsxreport

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/xuri/excelize/v2"

	"github.com/howashoji/looptrack/internal/i18n"
)

// SheetNameIn は書き出すシートの名前（言語ごと）。
func SheetNameIn(lang i18n.Lang) string { return i18n.T(lang, "report.xlsx.sheet") }

// headerRow は見出しの行番号（1: 表題、2: 出力日時と対象、3: 空行）。
const headerRow = 4

// Row は表の 1 行。値は画面と同じ表記の文字列で渡す。
type Row struct {
	ID        string
	Type      string
	Status    string
	Priority  string
	Assignee  string // 担当者（権限を外された担当は「login（権限なし）」で渡す）
	Title     string
	Labels    []string
	Parent    string
	BlockedBy []string
	Traces    []string
	Created   string
	Updated   string
}

// Report は 1 ファイル分の内容。
type Report struct {
	Project   string    // プロジェクト名
	Prefix    string    // イシュー ID の接頭辞
	Generated string    // 出力日時 YYYY-MM-DD HH:MM
	Filter    string    // 絞り込みの説明（空なら「全件」）
	Lang      i18n.Lang // 見出し・表題の言語（空なら日本語）
	Rows      []Row
}

// columnWidths は列の幅（見出しの並びと同じ順）。
var columnWidths = []float64{12, 13, 13, 9, 16, 60, 22, 12, 20, 20, 18, 18}

// columnHeads は見出し。ID は文字列リテラルで渡す（変数で渡すと lint が抜けを拾えない）。
func columnHeads(lang i18n.Lang) []string {
	return []string{"ID", i18n.T(lang, "report.col.type"), i18n.T(lang, "report.col.status"),
		i18n.T(lang, "report.xlsx.col.priority"), i18n.T(lang, "report.xlsx.col.assignee"), i18n.T(lang, "report.col.title"),
		i18n.T(lang, "report.xlsx.col.labels"), i18n.T(lang, "report.xlsx.col.parent"), i18n.T(lang, "report.xlsx.col.blocked_by"),
		i18n.T(lang, "report.xlsx.col.traces"), i18n.T(lang, "report.xlsx.col.created"), i18n.T(lang, "report.xlsx.col.updated")}
}

// statusFill は状態の色（閲覧画面と同じ系統の淡い色）。
var statusFill = map[string]string{
	"Backlog":     "EDEEF0",
	"Todo":        "DCEAF7",
	"In Progress": "FBEFD3",
	"In Review":   "EEE2F7",
	"Done":        "DDF0E4",
	"Canceled":    "E7E8EA",
}

// priorityColor は優先度の文字色（P0 / P1 だけ目立たせる）。
var priorityColor = map[string]string{"P0": "B42318", "P1": "B45309"}

type styleSet struct {
	title, sub, header, cell, id int
	status                       map[string]int
	priority                     map[string]int
}

// Build は xlsx のバイト列を返す。
func Build(rep Report) ([]byte, error) {
	lang := rep.Lang
	sheet := SheetNameIn(lang)
	f := excelize.NewFile()
	defer f.Close()
	if err := f.SetSheetName("Sheet1", sheet); err != nil {
		return nil, err
	}
	st, err := newStyles(f)
	if err != nil {
		return nil, err
	}

	title := i18n.T(lang, "report.xlsx.title", "project", rep.Project)
	if rep.Prefix != "" {
		title += i18n.T(lang, "report.xlsx.title_prefix", "prefix", rep.Prefix)
	}
	filter := strings.TrimSpace(rep.Filter)
	if filter == "" {
		filter = i18n.T(lang, "report.xlsx.filter_all")
	}
	head := []struct {
		cell, value string
		style       int
	}{
		{"A1", title, st.title},
		{"A2", i18n.T(lang, "report.xlsx.sub", "generated", rep.Generated, "filter", filter, "count", len(rep.Rows)), st.sub},
	}
	for _, h := range head {
		if err := f.SetCellStr(sheet, h.cell, h.value); err != nil {
			return nil, err
		}
		if err := f.SetCellStyle(sheet, h.cell, h.cell, h.style); err != nil {
			return nil, err
		}
	}

	for i, h := range columnHeads(lang) {
		col, err := excelize.ColumnNumberToName(i + 1)
		if err != nil {
			return nil, err
		}
		if err := f.SetColWidth(sheet, col, col, columnWidths[i]); err != nil {
			return nil, err
		}
		if err := setCell(f, sheet, col, headerRow, h, st.header); err != nil {
			return nil, err
		}
	}

	for i, row := range rep.Rows {
		n := headerRow + 1 + i
		values := []string{row.ID, row.Type, row.Status, row.Priority, row.Assignee, row.Title,
			strings.Join(row.Labels, ", "), row.Parent, strings.Join(row.BlockedBy, ", "),
			strings.Join(row.Traces, ", "), row.Created, row.Updated}
		for j, v := range values {
			col, err := excelize.ColumnNumberToName(j + 1)
			if err != nil {
				return nil, err
			}
			if err := setCell(f, sheet, col, n, v, st.styleFor(j, row)); err != nil {
				return nil, err
			}
		}
	}

	last, err := excelize.ColumnNumberToName(len(columnWidths))
	if err != nil {
		return nil, err
	}
	if err := f.AutoFilter(sheet, fmt.Sprintf("A%d:%s%d", headerRow, last, headerRow+len(rep.Rows)), nil); err != nil {
		return nil, err
	}
	// 見出しまでを固定して、下にスクロールしても列名が見えるようにする
	first := fmt.Sprintf("A%d", headerRow+1)
	if err := f.SetPanes(sheet, &excelize.Panes{Freeze: true, YSplit: headerRow, TopLeftCell: first,
		ActivePane: "bottomLeft", Selection: []excelize.Selection{{SQRef: first, ActiveCell: first, Pane: "bottomLeft"}}}); err != nil {
		return nil, err
	}

	if err := setupPrint(f, sheet); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// FileName は保存するときのファイル名（ASCII だけ）。
func FileName(slug, generated string) string {
	day := generated
	if fields := strings.Fields(generated); len(fields) > 0 {
		day = fields[0]
	}
	return fmt.Sprintf("issues-%s-%s.xlsx", slug, strings.ReplaceAll(day, "-", ""))
}

func setCell(f *excelize.File, sheet, col string, row int, value string, style int) error {
	cell := fmt.Sprintf("%s%d", col, row)
	if err := f.SetCellStr(sheet, cell, value); err != nil {
		return err
	}
	return f.SetCellStyle(sheet, cell, cell, style)
}

func (st styleSet) styleFor(col int, row Row) int {
	switch col {
	case 0:
		return st.id
	case 2:
		if s, ok := st.status[row.Status]; ok {
			return s
		}
	case 3:
		if s, ok := st.priority[row.Priority]; ok {
			return s
		}
	}
	return st.cell
}

func newStyles(f *excelize.File) (styleSet, error) {
	var st styleSet
	var err error
	mk := func(s *excelize.Style) int {
		if err != nil {
			return 0
		}
		var id int
		id, err = f.NewStyle(s)
		return id
	}
	border := []excelize.Border{
		{Type: "left", Color: "D9DDE5", Style: 1}, {Type: "right", Color: "D9DDE5", Style: 1},
		{Type: "top", Color: "D9DDE5", Style: 1}, {Type: "bottom", Color: "D9DDE5", Style: 1},
	}
	st.title = mk(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 14}})
	st.sub = mk(&excelize.Style{Font: &excelize.Font{Size: 10, Color: "667085"}})
	st.header = mk(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "FFFFFF"},
		Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"44546A"}},
		Alignment: &excelize.Alignment{Vertical: "center"},
		Border:    border,
	})
	st.cell = mk(&excelize.Style{Alignment: &excelize.Alignment{Vertical: "center"}, Border: border})
	st.id = mk(&excelize.Style{Font: &excelize.Font{Bold: true}, Alignment: &excelize.Alignment{Vertical: "center"}, Border: border})
	st.status = map[string]int{}
	for status, color := range statusFill {
		st.status[status] = mk(&excelize.Style{
			Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{color}},
			Alignment: &excelize.Alignment{Vertical: "center", Horizontal: "center"},
			Border:    border,
		})
	}
	st.priority = map[string]int{}
	for priority, color := range priorityColor {
		st.priority[priority] = mk(&excelize.Style{
			Font:      &excelize.Font{Bold: true, Color: color},
			Alignment: &excelize.Alignment{Vertical: "center", Horizontal: "center"},
			Border:    border,
		})
	}
	return st, err
}

// setupPrint は印刷（PDF 書き出し）で表が横に割れないようにする。
func setupPrint(f *excelize.File, sheet string) error {
	fit, landscape := true, "landscape"
	if err := f.SetSheetProps(sheet, &excelize.SheetPropsOptions{FitToPage: &fit}); err != nil {
		return err
	}
	if err := f.SetPageLayout(sheet, &excelize.PageLayoutOptions{Orientation: &landscape,
		FitToWidth: intPtr(1), FitToHeight: intPtr(0)}); err != nil {
		return err
	}
	// 見出しの行を各ページの先頭で繰り返す
	return f.SetDefinedName(&excelize.DefinedName{Name: "Print_Titles",
		RefersTo: fmt.Sprintf("'%s'!$%d:$%d", sheet, headerRow, headerRow), Scope: sheet})
}

func intPtr(n int) *int { return &n }
