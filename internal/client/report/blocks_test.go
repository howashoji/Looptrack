package report

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/reporttable"
)

// 以前のレポート生成（1.0.0 より前）のテストの移植。データは同じ合成データ（testdata/*.json は
// そのテストの REPORT・BIG・CONTENT を書き出したもの）。

func load(t *testing.T, name string) any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// clone は JSON の値を深く写す。
func clone(v any) any {
	switch x := v.(type) {
	case obj:
		m := obj{}
		for k, e := range x {
			m[k] = clone(e)
		}
		return m
	case []any:
		l := make([]any, len(x))
		for i, e := range x {
			l[i] = clone(e)
		}
		return l
	}
	return v
}

func headings(blocks []Block) []string {
	var out []string
	for _, b := range blocks {
		if b.Kind == Heading {
			out = append(out, b.Text)
		}
	}
	return out
}

func cells(t *reporttable.Table, i int) []string {
	out := make([]string, len(t.Columns))
	for j := range out {
		out[j] = t.Cell(i, j)
	}
	return out
}

func TestBlocksInOrderWithAutoTables(t *testing.T) {
	blocks, err := Build(i18n.JA, load(t, "report"), load(t, "content"))
	if err != nil {
		t.Fatal(err)
	}
	if blocks[0].Kind != Title || blocks[1].Kind != Meta {
		t.Fatalf("先頭 = %v %v", blocks[0].Kind, blocks[1].Kind)
	}
	want := []string{"1. 全体サマリー", "集計の概要", "イシュー別", "段階別（区間を閉じた操作）", "2. 関与と AI の挙動への影響", "3. 提案", "4. データの限界"}
	if got := headings(blocks); !reflect.DeepEqual(got, want) {
		t.Errorf("見出し = %v", got)
	}
	meta := blocks[1].Lines
	for _, line := range []string{"データ終端: 2026-09-18 10:00（日本時間。次回の「前回以降」はここから）", "起点: 前回のレポート「2026年8月」のデータ終端"} {
		if !slices.Contains(meta, line) {
			t.Errorf("meta に %q が無い: %v", line, meta)
		}
	}
	if !slices.ContainsFunc(meta, func(l string) bool {
		return strings.HasPrefix(l, "作成依頼: #3（2026-09-18 08:00 登録・alice） 対象: ラベル api")
	}) {
		t.Errorf("作成依頼の行: %v", meta)
	}
}

func TestSummaryTableNumbersComeFromReport(t *testing.T) {
	_, tb, err := AutoTable(i18n.JA, load(t, "report"), "summary", nil)
	if err != nil {
		t.Fatal(err)
	}
	// 入力は本体 + サブ（1000 + 100）。合計は 4 種の和
	if got := cells(tb, 0); !reflect.DeepEqual(got, []string{"合計（対象）", "1,100", "200", "5,000", "300", "6,600", "4"}) {
		t.Errorf("合計の行 = %v", got)
	}
	if tb.Cell(1, 0) != "うち未帰属" || tb.Cell(2, 5) != "700" {
		t.Errorf("行 = %v %v", cells(tb, 1), cells(tb, 2))
	}
	if !slices.Contains(tb.Notes, "対象外の会話: conv-x") || !slices.ContainsFunc(tb.Notes, func(n string) bool { return strings.Contains(n, "不整合") }) {
		t.Errorf("注記 = %v", tb.Notes)
	}
}

func TestLimitsAndNotes(t *testing.T) {
	rep := load(t, "report")
	_, tb, _ := AutoTable(i18n.JA, rep, "by_issue", jsonInt(10))
	if len(tb.Rows) != 10 || tb.Cell(0, 0) != "DEMO-0025" || !reflect.DeepEqual(tb.Notes, []string{"上位 10 件（全 25 件）"}) {
		t.Errorf("上位 10 件: %d %s %v", len(tb.Rows), tb.Cell(0, 0), tb.Notes)
	}
	if _, tb, _ = AutoTable(i18n.JA, rep, "by_issue", nil); len(tb.Rows) != 20 { // 既定は 20 件
		t.Errorf("既定の件数 = %d", len(tb.Rows))
	}
	_, tb, _ = AutoTable(i18n.JA, rep, "by_label", nil)
	if tb.Cell(1, 0) != "（なし）" || !slices.ContainsFunc(tb.Notes, func(n string) bool { return strings.Contains(n, "一致しない") }) {
		t.Errorf("ラベル別: %v %v", cells(tb, 1), tb.Notes)
	}
	if _, tb, _ = AutoTable(i18n.JA, rep, "by_stage", nil); tb.Cell(0, 0) != "コメント（comment）" {
		t.Errorf("段階別 = %s", tb.Cell(0, 0))
	}
	_, tb, _ = AutoTable(i18n.JA, rep, "conversations", nil)
	if got := cells(tb, 0)[:4]; !reflect.DeepEqual(got, []string{"0123456789ab", "claude-code", "2026-09-01 09:00", "2026-09-02 12:04"}) {
		t.Errorf("会話別 = %v", got)
	}
}

func TestDefaultAutoTablesAfterFirstSection(t *testing.T) {
	content := clone(load(t, "content")).(obj)
	var secs []any
	for _, s := range content["sections"].([]any) {
		if !has(s, "auto") {
			secs = append(secs, s)
		}
	}
	content["sections"] = secs
	blocks, err := Build(i18n.JA, load(t, "report"), content)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"1. 全体サマリー", "集計の概要", "イシュー別", "案件別", "ラベル別", "種類別", "段階別（区間を閉じた操作）", "AI 別", "会話別", "2. 関与と AI の挙動への影響"}
	if got := headings(blocks)[:10]; !reflect.DeepEqual(got, want) {
		t.Errorf("見出し = %v", got)
	}
}

func TestByCase(t *testing.T) {
	// 案件別。案件なしは（なし）、正規表現と「合計は全体と一致する」を注記に出す
	rep := clone(load(t, "report")).(obj)
	_, tb, _ := AutoTable(i18n.JA, rep, "by_case", nil)
	if tb.Columns[0].Head != "案件" || !reflect.DeepEqual(cells(tb, 0)[:6], []string{"CASE-101", "800", "0", "0", "0", "800"}) || tb.Cell(1, 0) != "（なし）" {
		t.Errorf("案件別 = %v %v", cells(tb, 0), cells(tb, 1))
	}
	if !slices.ContainsFunc(tb.Notes, func(n string) bool { return strings.Contains(n, `/CASE-\d+/`) && strings.Contains(n, "一致する") }) {
		t.Errorf("注記 = %v", tb.Notes)
	}
	rep["case_pattern"] = ""
	if _, tb, _ = AutoTable(i18n.JA, rep, "by_case", nil); !slices.ContainsFunc(tb.Notes, func(n string) bool { return strings.Contains(n, "未設定") }) {
		t.Errorf("未設定の注記 = %v", tb.Notes)
	}
	delete(rep, "by_case") // 案件別に対応する前のサーバの集計 JSON でも止まらない
	_, tb, _ = AutoTable(i18n.JA, rep, "by_case", nil)
	if len(tb.Rows) != 0 || !slices.ContainsFunc(tb.Notes, func(n string) bool { return strings.Contains(n, "by_case") }) {
		t.Errorf("by_case なし = %v %v", tb.Rows, tb.Notes)
	}
}

func TestEmptyReport(t *testing.T) {
	rep := clone(load(t, "report")).(obj)
	for _, k := range []string{"by_issue", "by_label", "by_type", "by_stage", "by_client", "conversations"} {
		rep[k] = []any{}
	}
	blocks, err := Build(i18n.JA, rep, load(t, "content"))
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(blocks, func(b Block) bool { return b.Kind == Heading && b.Text == "イシュー別" })
	if i < 0 || blocks[i+1].Kind != Paragraph || blocks[i+1].Text != "該当なし" {
		t.Errorf("該当なし が無い: %+v", blocks[i+1])
	}
}

func TestRejectsBadInput(t *testing.T) {
	appendSec := func(s any) func(obj) {
		return func(c obj) { c["sections"] = append(c["sections"].([]any), s) }
	}
	table := func(cols []any, rows []any, extra obj) obj {
		tb := obj{"columns": cols, "rows": rows}
		for k, v := range extra {
			tb[k] = v
		}
		return obj{"heading": "x", "table": tb}
	}
	cases := map[string]func(obj){
		"題名なし":            func(c obj) { delete(c, "title") },
		"章なし":             func(c obj) { c["sections"] = []any{} },
		"見出しなし":           appendSec(obj{"paragraphs": []any{"x"}}),
		"知らない数表":          appendSec(obj{"auto": "by_money"}),
		"列数違い":            appendSec(table([]any{"a", "b"}, []any{[]any{"1"}}, nil)),
		"段落が文字列でない":       appendSec(obj{"heading": "x", "paragraphs": "本文"}),
		"nowrap の列番号が範囲外": appendSec(table([]any{"a"}, []any{[]any{"1"}}, obj{"nowrap": []any{jsonInt(1)}})),
		"データの限界なし": func(c obj) {
			var secs []any
			for _, s := range c["sections"].([]any) {
				if !strings.Contains(sget(s, "heading"), "限界") {
					secs = append(secs, s)
				}
			}
			c["sections"] = secs
		},
	}
	for name, mutate := range cases {
		content := clone(load(t, "content")).(obj)
		mutate(content)
		var ie *i18n.Error
		if _, err := Build(i18n.JA, load(t, "report"), content); !errors.As(err, &ie) {
			t.Errorf("%s: 拒否しない（%v）", name, err)
		}
	}
	var ie *i18n.Error
	if _, err := Build(i18n.JA, obj{"project": "x"}, load(t, "content")); !errors.As(err, &ie) { // usage report の出力でない
		t.Errorf("集計でない JSON を拒否しない: %v", err)
	}
}

// ID・種類・状態・数値の列は中身の最大幅以上（折り返さない）。タイトルの列が残りを取る（フォント無しの見積もり）
func TestNowrapColumnsFitContent(t *testing.T) {
	content := clone(load(t, "content")).(obj)
	secs := []any{}
	for _, a := range AutoTables {
		secs = append(secs, obj{"auto": a})
	}
	all := content["sections"].([]any)
	content["sections"] = append(secs, all[len(all)-1])
	blocks, err := Build(i18n.JA, load(t, "big"), content)
	if err != nil {
		t.Fatal(err)
	}
	tables := Tables(blocks)
	if len(tables) != 8 {
		t.Fatalf("表の数 = %d", len(tables))
	}
	approx := func(s string) float64 { return reporttable.ApproxWidth(s, 7.5) }
	for _, tb := range tables {
		widths := reporttable.Fit(*tb, 763, approx, reporttable.PDFFit)
		if sum := sumOf(widths); sum < 763-1e-3 || sum > 763+1e-3 {
			t.Errorf("%s: 合計 %f", tb.Columns[0].Head, sum)
		}
		for i := range tb.Columns {
			if !tb.Fixed(i) {
				continue
			}
			need := 0.0
			for r := range tb.Rows {
				need = max(need, approx(tb.Cell(r, i)))
			}
			if widths[i] < need+2*reporttable.PDFFit.Pad {
				t.Errorf("%s の %d 列: 幅 %f < 中身 %f", tb.Columns[0].Head, i, widths[i], need)
			}
		}
	}
	byIssue := tables[1]
	if byIssue.Columns[3].Head != "タイトル" || !byIssue.Columns[0].NoWrap || !byIssue.Columns[1].NoWrap || !byIssue.Columns[2].NoWrap ||
		!slices.Contains(cells(byIssue, 0), "4,331,803,110") {
		t.Errorf("イシュー別 = %+v %v", byIssue.Columns, cells(byIssue, 0))
	}
	if w := reporttable.Fit(*byIssue, 763, approx, reporttable.PDFFit); w[3] <= 150 {
		t.Errorf("タイトルの列が狭い: %f", w[3]) // タイトルの列は残りを取る
	}
	conv := tables[7]
	var numeric []int
	for i, c := range conv.Columns {
		if c.Number {
			numeric = append(numeric, i)
		}
	}
	if !reflect.DeepEqual(numeric, []int{4, 5}) { // イシューの列（ID の並び）は数値の列でない
		t.Errorf("会話別の数値の列 = %v", numeric)
	}
}

func TestColWidthsShrinkWhenTooManyColumns(t *testing.T) {
	// 収まらないときは見出しの折り返しを許し、最後は比率で縮める（ページ幅は超えない）
	tb := reporttable.Table{}
	row := []any{}
	for i := 0; i < 6; i++ {
		tb.Columns = append(tb.Columns, reporttable.Column{Head: "キャッシュ読取の長い見出し", Number: true})
		row = append(row, "1,234,567,890")
	}
	tb.Columns = append(tb.Columns, reporttable.Column{Head: "説明"})
	tb.Rows = [][]any{append(row, strings.Repeat("x", 40))}
	approx := func(s string) float64 { return reporttable.ApproxWidth(s, 7.5) }
	w := reporttable.Fit(tb, 500, approx, reporttable.PDFFit)
	if s := sumOf(w); s < 500-1e-3 || s > 500+1e-3 || w[0] < approx("1,234,567,890")+2*reporttable.PDFFit.Pad {
		t.Errorf("500pt: %v", w)
	}
	if s := sumOf(reporttable.Fit(tb, 200, approx, reporttable.PDFFit)); s < 200-1e-3 || s > 200+1e-3 {
		t.Errorf("200pt: 合計 %f", s)
	}
}

func TestColWidthsFillThePage(t *testing.T) {
	_, tb, _ := AutoTable(i18n.JA, load(t, "report"), "by_issue", nil)
	for i := range tb.Columns {
		tb.Columns[i].NoWrap = false
	}
	w := reporttable.Fit(*tb, 700, func(s string) float64 { return reporttable.ApproxWidth(s, 7.5) }, reporttable.PDFFit)
	if s := sumOf(w); s < 700-1e-3 || s > 700+1e-3 || w[3] <= w[1] {
		t.Errorf("列幅 = %v", w) // タイトルの列は種類の列より広い
	}
}

func sumOf(ws []float64) float64 {
	s := 0.0
	for _, w := range ws {
		s += w
	}
	return s
}

func jsonInt(n int) any { return json.Number(strconv.Itoa(n)) }

// 対象期間は、集計 JSON の period（サーバが描いた文字列）ではなく from / to から出す側の言語で描く。
// testdata の period は日本語なので、これを使っていると英語の PDF でもこの行だけ日本語になる。
func TestPeriodIsRenderedInTheOutputLanguage(t *testing.T) {
	content := load(t, "content")
	for _, c := range []struct{ lang, want string }{
		{"ja", "対象期間: 2026-09-01 00:00 〜 2026-10-01 00:00（日本時間・終わりを含まない）"},
		{"en", "Period: 2026-09-01 00:00 — 2026-10-01 00:00 (JST, end exclusive)"},
	} {
		lang, _ := i18n.Parse(c.lang)
		blocks, err := Build(lang, load(t, "report"), content)
		if err != nil {
			t.Fatal(err)
		}
		if meta := blocks[1].Lines; !slices.Contains(meta, c.want) {
			t.Errorf("%s: meta に %q が無い: %v", c.lang, c.want, meta)
		}
	}

	// from が無い（最初のデータから）ときも、出す側の言語で描く
	rep := clone(load(t, "report")).(obj)
	delete(rep, "from")
	blocks, err := Build(i18n.EN, rep, content)
	if err != nil {
		t.Fatal(err)
	}
	want := "Period: the first data point — 2026-10-01 00:00 (JST, end exclusive)"
	if meta := blocks[1].Lines; !slices.Contains(meta, want) {
		t.Errorf("from 無し: meta に %q が無い: %v", want, meta)
	}

	// to が無い古い集計 JSON は、サーバが描いた period をそのまま出す（描き直す材料が無い）
	delete(rep, "to")
	if blocks, err = Build(i18n.EN, rep, content); err != nil {
		t.Fatal(err)
	}
	want = "Period: 2026-09-01 00:00 〜 2026-10-01 00:00（日本時間・終わりを含まない）"
	if meta := blocks[1].Lines; !slices.Contains(meta, want) {
		t.Errorf("to 無し: meta に %q が無い: %v", want, meta)
	}
}

// 時刻は集計 JSON の timezone（サーバのローカル時刻）で描く。
// 時間帯を載せていない古い集計 JSON は Asia/Tokyo（以前の実装と同じ日本時間）に倒す。
func TestTimesAreRenderedInTheReportTimezone(t *testing.T) {
	content := load(t, "content")
	meta := func(rep any, lang i18n.Lang) []string {
		t.Helper()
		blocks, err := Build(lang, rep, content)
		if err != nil {
			t.Fatal(err)
		}
		return blocks[1].Lines
	}

	// timezone が無い古い集計 JSON: 日本時間のまま（後方互換）
	old := load(t, "report")
	if lines := meta(old, i18n.JA); !slices.Contains(lines, "対象期間: 2026-09-01 00:00 〜 2026-10-01 00:00（日本時間・終わりを含まない）") {
		t.Errorf("timezone 無し（既定は日本時間）: %v", lines)
	}

	// timezone が Asia/Tokyo: 同じ（名指ししても表記は変わらない）
	tokyo := clone(old).(obj)
	tokyo["timezone"] = "Asia/Tokyo"
	if !slices.Equal(meta(tokyo, i18n.JA), meta(old, i18n.JA)) {
		t.Errorf("Asia/Tokyo を名指ししたら表記が変わった: %v", meta(tokyo, i18n.JA))
	}

	// timezone が Asia/Tokyo 以外: その時間帯の時刻で描き、注記もその時間帯を名乗る
	la := clone(old).(obj)
	la["timezone"] = "America/Los_Angeles"
	for _, c := range []struct {
		lang i18n.Lang
		want string
	}{
		{i18n.JA, "対象期間: 2026-08-31 08:00 〜 2026-09-30 08:00（America/Los_Angeles・終わりを含まない）"},
		{i18n.EN, "Period: 2026-08-31 08:00 — 2026-09-30 08:00 (America/Los_Angeles, end exclusive)"},
	} {
		lines := meta(la, c.lang)
		if !slices.Contains(lines, c.want) {
			t.Errorf("%v: %q が無い: %v", c.lang, c.want, lines)
		}
		for _, ng := range []string{"日本時間", "JST"} {
			if strings.Contains(strings.Join(lines, "\n"), ng) {
				t.Errorf("%v: 日本時間でないのに %q と注記している: %v", c.lang, ng, lines)
			}
		}
	}

	// 読めない時間帯の名前は日本時間に倒す（描けないより、以前と同じ表記のほうが安全）
	bad := clone(old).(obj)
	bad["timezone"] = "Mars/Olympus"
	if !slices.Equal(meta(bad, i18n.JA), meta(old, i18n.JA)) {
		t.Errorf("読めない時間帯: %v", meta(bad, i18n.JA))
	}
}

// 会話別の表の脚注（時刻の時間帯）の文面。ja / en × Asia/Tokyo / それ以外の 4 通りを固定する。
// 既定（Asia/Tokyo）の文は 1.0.0 より前からのもので、**英語は略語ではなく説明の形**。
// 時間帯を持ち回るようにしたとき、短い呼び名（report.tz.jst = "JST"）を脚注にも流用してしまい、
// "Times are Japan Standard Time (UTC+9)" が "Times are JST" に退化した。テストが無かったので気づけなかった。
// 言語は Build の引数で決まる（環境変数・Accept-Language を読まない）ので、ここで固定できている。
func TestConversationTimezoneNote(t *testing.T) {
	rep := clone(load(t, "report")).(obj)
	note := func(lang i18n.Lang) string {
		t.Helper()
		_, table, err := AutoTable(lang, rep, "conversations", nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(table.Notes) == 0 {
			t.Fatal("脚注が無い")
		}
		return table.Notes[len(table.Notes)-1]
	}

	for _, c := range []struct {
		name, timezone string
		lang           i18n.Lang
		want           string
	}{
		{"既定（時間帯なし）・日本語", "", i18n.JA, "時刻は日本時間"},
		{"既定（時間帯なし）・英語", "", i18n.EN, "Times are Japan Standard Time (UTC+9)"},
		{"Asia/Tokyo・日本語", "Asia/Tokyo", i18n.JA, "時刻は日本時間"},
		{"Asia/Tokyo・英語", "Asia/Tokyo", i18n.EN, "Times are Japan Standard Time (UTC+9)"},
		{"Asia/Tokyo 以外・日本語", "America/Los_Angeles", i18n.JA, "時刻は America/Los_Angeles（IANA の時間帯名）"},
		{"Asia/Tokyo 以外・英語", "America/Los_Angeles", i18n.EN, "Times are in the America/Los_Angeles time zone (IANA name)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.timezone == "" {
				delete(rep, "timezone")
			} else {
				rep["timezone"] = c.timezone
			}
			if got := note(c.lang); got != c.want {
				t.Errorf("脚注 = %q, want %q", got, c.want)
			}
		})
	}
}
