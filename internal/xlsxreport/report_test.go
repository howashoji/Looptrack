package xlsxreport

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"github.com/howashoji/looptrack/internal/i18n"
)

// SheetName は日本語のシート名（このテストは日本語の出力を見る）。
var SheetName = SheetNameIn(i18n.JA)

// 課題管理表（xlsx）の組み立て。
func sample() Report {
	return Report{
		Project: "要件", Prefix: "REQ", Generated: "2026-09-18 10:30", Filter: "不具合のみ / クローズを隠す",
		Rows: []Row{
			{ID: "REQ-0002", Type: "bug", Status: "In Progress", Priority: "P0", Assignee: "alice", Title: "落ちる",
				Labels: []string{"web", "api"}, Parent: "REQ-0001", BlockedBy: []string{"REQ-0003"},
				Traces: []string{"REQ-0001"}, Created: "2026-09-17 10:00", Updated: "2026-09-18 09:00"},
			{ID: "REQ-0004", Type: "task", Status: "Done", Priority: "P2", Title: "片付ける",
				Created: "2026-09-17 11:00", Updated: "2026-09-17 12:00"},
		},
	}
}

func open(t *testing.T, rep Report) *excelize.File {
	t.Helper()
	b, err := Build(rep)
	if err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("xlsx として開けない: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestBuildContents(t *testing.T) {
	rep := sample()
	f := open(t, rep)
	if names := f.GetSheetList(); len(names) != 1 || names[0] != SheetName {
		t.Fatalf("シート = %v", names)
	}
	rows, err := f.GetRows(SheetName)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != headerRow+len(rep.Rows) {
		t.Fatalf("行数 = %d", len(rows))
	}
	if got := rows[0][0]; got != "課題管理表 — 要件（REQ-nnnn）" {
		t.Errorf("表題 = %q", got)
	}
	if got := rows[1][0]; !strings.Contains(got, "2026-09-18 10:30") || !strings.Contains(got, "不具合のみ / クローズを隠す") || !strings.Contains(got, "2 件") {
		t.Errorf("出力日時と対象 = %q", got)
	}
	want := []string{"ID", "種類", "状態", "優先度", "担当", "タイトル", "ラベル", "親", "待ち", "トレース先", "作成", "更新"}
	if got := rows[headerRow-1]; len(got) != len(want) {
		t.Fatalf("見出し = %v", got)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("見出し %d = %q, want %q", i, got[i], want[i])
			}
		}
	}
	// 1 行目のイシュー: 渡した順に並び、一覧はカンマ区切りで入る
	first := rows[headerRow]
	for i, want := range []string{"REQ-0002", "bug", "In Progress", "P0", "alice", "落ちる", "web, api", "REQ-0001", "REQ-0003", "REQ-0001",
		"2026-09-17 10:00", "2026-09-18 09:00"} {
		if first[i] != want {
			t.Errorf("1 行目 %d 列 = %q, want %q", i+1, first[i], want)
		}
	}
	if second := rows[headerRow+1]; second[0] != "REQ-0004" || second[4] != "" || second[6] != "" {
		t.Errorf("2 行目 = %v", second)
	}
}

func TestBuildFormatting(t *testing.T) {
	rep := sample()
	f := open(t, rep)

	// 状態の色分け
	for cell, status := range map[string]string{"C5": "In Progress", "C6": "Done"} {
		id, err := f.GetCellStyle(SheetName, cell)
		if err != nil {
			t.Fatal(err)
		}
		st, err := f.GetStyle(id)
		if err != nil {
			t.Fatal(err)
		}
		want := statusFill[status]
		if len(st.Fill.Color) == 0 || !strings.EqualFold(strings.TrimPrefix(st.Fill.Color[0], "FF"), want) {
			t.Errorf("%s（%s）の塗り = %v, want %s", cell, status, st.Fill.Color, want)
		}
	}
	// P0 は目立たせる
	if id, err := f.GetCellStyle(SheetName, "D5"); err != nil {
		t.Fatal(err)
	} else if st, err := f.GetStyle(id); err != nil {
		t.Fatal(err)
	} else if st.Font == nil || !st.Font.Bold || !strings.EqualFold(strings.TrimPrefix(st.Font.Color, "FF"), priorityColor["P0"]) {
		t.Errorf("P0 の書式 = %+v", st.Font)
	}
	// 見出しの固定・フィルタ・列幅
	panes, err := f.GetPanes(SheetName)
	if err != nil {
		t.Fatal(err)
	}
	if !panes.Freeze || panes.YSplit != headerRow {
		t.Errorf("固定 = %+v", panes)
	}
	if w, err := f.GetColWidth(SheetName, "F"); err != nil { // E は担当
		t.Fatal(err)
	} else if w < 50 {
		t.Errorf("タイトル列の幅 = %v", w)
	}
}

// フィルタは excelize に取得の関数が無いのでシートの XML を直接見る
func TestBuildAutoFilter(t *testing.T) {
	rep := sample()
	b, err := Build(rep)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	var sheet string
	for _, f := range zr.File {
		if f.Name != "xl/worksheets/sheet1.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		sheet = string(raw)
	}
	if sheet == "" {
		t.Fatal("sheet1.xml が無い")
	}
	if want := `<autoFilter ref="$A$4:$L$6"`; !strings.Contains(sheet, want) {
		t.Errorf("フィルタの範囲に %s が無い", want)
	}
}

// 印刷では横に割れず、見出しが各ページに出る
func TestBuildPrintSetup(t *testing.T) {
	f := open(t, sample())
	layout, err := f.GetPageLayout(SheetName)
	if err != nil {
		t.Fatal(err)
	}
	if layout.Orientation == nil || *layout.Orientation != "landscape" {
		t.Errorf("向き = %v", layout.Orientation)
	}
	if layout.FitToWidth == nil || *layout.FitToWidth != 1 || layout.FitToHeight == nil || *layout.FitToHeight != 0 {
		t.Errorf("幅に合わせる設定 = %v / %v", layout.FitToWidth, layout.FitToHeight)
	}
	props, err := f.GetSheetProps(SheetName)
	if err != nil {
		t.Fatal(err)
	}
	if props.FitToPage == nil || !*props.FitToPage {
		t.Errorf("FitToPage = %v", props.FitToPage)
	}
	names := f.GetDefinedName()
	found := false
	for _, n := range names {
		if n.Name == "Print_Titles" && strings.Contains(n.RefersTo, "$4:$4") {
			found = true
		}
	}
	if !found {
		t.Errorf("見出し行の繰り返しが無い: %+v", names)
	}
}

func TestBuildEmptyAndDefaults(t *testing.T) {
	f := open(t, Report{Project: "要件", Generated: "2026-09-18 10:30"})
	rows, err := f.GetRows(SheetName)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != headerRow {
		t.Fatalf("行数 = %d（見出しだけのはず）", len(rows))
	}
	if got := rows[0][0]; got != "課題管理表 — 要件" {
		t.Errorf("接頭辞が無いときの表題 = %q", got)
	}
	if got := rows[1][0]; !strings.Contains(got, "対象: 全件") || !strings.Contains(got, "0 件") {
		t.Errorf("絞り込みが空のとき = %q", got)
	}
}

func TestFileName(t *testing.T) {
	for _, c := range []struct{ generated, want string }{
		{"2026-09-18 10:30", "issues-req-20260918.xlsx"},
		{"2026-09-18", "issues-req-20260918.xlsx"},
		{"", "issues-req-.xlsx"},
	} {
		if got := FileName("req", c.generated); got != c.want {
			t.Errorf("FileName(%q) = %q, want %q", c.generated, got, c.want)
		}
	}
}
