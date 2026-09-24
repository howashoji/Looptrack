// Package report はトークンレポートの PDF の中身（集計 JSON + 本文 JSON → 描く順のブロック）を組み立てる。
//
// 以前のレポート生成（1.0.0 より前）の移植。入力の検査・数表・注記・文言は同じにする
// （比較のテストは report_compare_test.go）。PDF に描くのは internal/client/report/pdf。
package report

import (
	"fmt"
	"strings"
	"time"
	_ "time/tzdata" // 集計 JSON の時間帯（IANA 名）を、tzdata の無い環境でも読めるようにする

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/reporttable"
)

// AutoTables は本文の {"auto": …} に書ける数表の名前（並びは既定で入れる順）。
var AutoTables = []string{"summary", "by_issue", "by_case", "by_label", "by_type", "by_stage", "by_client", "conversations"}

var defaultLimits = map[string]int{"by_issue": 20, "conversations": 20}

// stageName は段階（区間を閉じた操作）の表示名。ID は文字列リテラルで書く（変数で渡すと lint が抜けを拾えない）。
func stageName(lang i18n.Lang, key string) (string, bool) {
	switch key {
	case "create":
		return i18n.T(lang, "report.stage.create"), true
	case "update":
		return i18n.T(lang, "report.stage.update"), true
	case "comment":
		return i18n.T(lang, "report.stage.comment"), true
	case "status":
		return i18n.T(lang, "report.stage.status"), true
	case "stop":
		return i18n.T(lang, "report.stage.stop"), true
	case "session_end":
		return i18n.T(lang, "report.stage.session_end"), true
	case "manual":
		return i18n.T(lang, "report.stage.manual"), true
	case "import":
		return i18n.T(lang, "report.stage.import"), true
	}
	return key, false
}

// LimitsHeadings は必ず設ける章の見出し（の一部）。**判定に使う語なので訳さない。**
// 本文 JSON を書くのは skill token-report の SKILL.md（導入時の言語の 1 本。日英のどちらか）を読んだ AI なので、
// 英語の本文で書かれた見出しも通るように、英語の言い回しも受ける（増やす向きの変更なので、日本語の判定は変わらない）。
var LimitsHeadings = []string{"データの限界", "Data limitations"}

func tokenColumns(lang i18n.Lang) []string {
	return []string{i18n.T(lang, "report.col.input"), i18n.T(lang, "report.col.cache_create"),
		i18n.T(lang, "report.col.cache_read"), i18n.T(lang, "report.col.output"), i18n.T(lang, "report.col.total")}
}

// Kind はブロックの種類。
type Kind string

const (
	Title     Kind = "title"
	Meta      Kind = "meta"
	Heading   Kind = "heading"
	Paragraph Kind = "paragraph"
	Bullets   Kind = "bullets"
	TableKind Kind = "table"
	Note      Kind = "note"
)

// Block は PDF に描く 1 まとまり。Text（title・heading・paragraph・note）か Lines（meta・bullets）か Table。
type Block struct {
	Kind  Kind
	Text  string
	Lines []string
	Table *reporttable.Table
}

// JST は日本時間。集計 JSON に時間帯が無い（時間帯を載せる前のサーバの）ときの既定。
var JST = time.FixedZone("JST", 9*3600)

// DefaultTZ は時間帯を載せていない古い集計 JSON を読んだときの時間帯。
// 以前の実装は時刻を日本時間で固定して描いていたので、後方互換のためその時間帯に倒す。
const DefaultTZ = "Asia/Tokyo"

// Num は int(n or 0) の桁区切り。
func Num(v any) string { return reporttable.Comma(toInt(v)) }

// tz は時刻を描く時間帯。集計 JSON の timezone（サーバのローカル時刻の IANA 名）から決める。
// サーバと同じ時間帯で描くためのもので、描く側の手元の時刻とは関係しない。
type tz struct {
	loc  *time.Location
	name string // 決まった時間帯の IANA 名（注記に出す）
}

// tzOf は集計 JSON の時間帯を読む。無い・読めない名前は Asia/Tokyo（以前の実装と同じ日本時間）。
func tzOf(report any) tz {
	if name := strings.TrimSpace(sget(report, "timezone")); name != "" && name != DefaultTZ {
		if loc, err := time.LoadLocation(name); err == nil {
			return tz{loc: loc, name: name}
		}
	}
	return tz{loc: JST, name: DefaultTZ}
}

// label は行の中に差し込む時間帯の短い呼び名（対象期間・データ終端）。
// 既定の日本時間だけは訳のある呼び名、ほかは IANA 名をそのまま出す。
func (z tz) label(lang i18n.Lang) string {
	if z.name == DefaultTZ {
		return i18n.T(lang, "report.tz.jst")
	}
	return z.name
}

// note は表の下に置く脚注。**既定の日本時間は以前の文をそのまま使う**（英語は略語ではなく
// "Japan Standard Time (UTC+9)" と説明する。短い呼び名を流用すると説明が略語に退化する）。
// ほかの時間帯も、IANA 名だけを置かず「IANA の時間帯名」と断って説明の形にそろえる。
func (z tz) note(lang i18n.Lang) string {
	if z.name == DefaultTZ {
		return i18n.T(lang, "report.note.jst")
	}
	return i18n.T(lang, "report.note.tz_named", "tz", z.name)
}

// at は RFC 3339（UTC）をこの時間帯の「YYYY-MM-DD HH:MM」にする。読めなければそのまま。
func (z tz) at(v any) string {
	if !truthy(v) {
		return ""
	}
	s := str(v)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999", "2006-01-02T15:04Z07:00", "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"} {
		loc := time.UTC
		if !strings.ContainsAny(layout, "Z") {
			loc = time.Local // 時差の無い時刻は手元の時刻とみなす（以前の実装と同じ）
		}
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t.In(z.loc).Format("2006-01-02 15:04")
		}
	}
	return s
}

// tokenCells は Counters（main / sub）を 4 種と合計のセルにする。
func tokenCells(counters any) []any {
	m, s := get(counters, "main"), get(counters, "sub")
	var cells []any
	var total int64
	for _, k := range []string{"input", "cache_create", "cache_read", "output"} {
		n := toInt(get(m, k)) + toInt(get(s, k))
		total += n
		cells = append(cells, reporttable.Comma(n))
	}
	return append(cells, reporttable.Comma(total))
}

func checkReport(report any) error {
	if _, ok := report.(obj); !ok || !has(report, "total_tokens") || !has(report, "by_issue") {
		return i18n.Errorf("report.check.err.report_json")
	}
	return nil
}

func checkContent(content any) error {
	if _, ok := content.(obj); !ok {
		return i18n.Errorf("report.check.err.content_object")
	}
	if strings.TrimSpace(str(or(get(content, "title"), ""))) == "" {
		return i18n.Errorf("report.check.err.no_title")
	}
	sections, ok := get(content, "sections").([]any)
	if !ok || len(sections) == 0 {
		return i18n.Errorf("report.check.err.no_sections")
	}
	for n, sec := range sections {
		where := fmt.Sprintf("sections[%d]", n+1)
		if _, ok := sec.(obj); !ok {
			return i18n.Errorf("report.check.err.section_object", "where", where)
		}
		if has(sec, "auto") {
			name, _ := get(sec, "auto").(string)
			if !known(name) {
				return i18n.Errorf("report.check.err.unknown_auto", "where", where,
					"names", strings.Join(AutoTables, " / "), "given", str(get(sec, "auto")))
			}
			continue
		}
		if strings.TrimSpace(str(or(get(sec, "heading"), ""))) == "" {
			return i18n.Errorf("report.check.err.no_heading", "where", where)
		}
		for _, key := range []string{"paragraphs", "bullets"} {
			if has(sec, key) {
				if _, ok := strList(get(sec, key)); !ok {
					return i18n.Errorf("report.check.err.not_string_list", "where", where, "key", key)
				}
			}
		}
		table := get(sec, "table")
		if table == nil {
			continue
		}
		cols, cok := get(table, "columns").([]any)
		rows, rok := get(table, "rows").([]any)
		if !cok || len(cols) == 0 || !rok {
			return i18n.Errorf("report.check.err.table_shape", "where", where)
		}
		for j, row := range rows {
			if r, ok := row.([]any); !ok || len(r) != len(cols) {
				return i18n.Errorf("report.check.err.row_columns", "where", where, "row", j+1, "cols", len(cols))
			}
		}
		for _, key := range []string{"numeric", "nowrap"} {
			idx := get(table, key)
			if idx == nil {
				continue
			}
			if _, ok := columnIndexes(idx, len(cols)); !ok {
				return i18n.Errorf("report.check.err.column_indexes", "where", where, "key", key, "cols", len(cols))
			}
		}
	}
	for _, sec := range sections {
		heading := str(or(get(sec, "heading"), ""))
		for _, want := range LimitsHeadings {
			if strings.Contains(heading, want) {
				return nil
			}
		}
	}
	return i18n.Errorf("report.check.err.no_limits_section")
}

func known(name string) bool {
	for _, a := range AutoTables {
		if a == name {
			return true
		}
	}
	return false
}

func columnIndexes(v any, n int) ([]int, bool) {
	l, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]int, 0, len(l))
	for _, x := range l {
		i, ok := isInt(x)
		if !ok || i < 0 || i >= n {
			return nil, false
		}
		out = append(out, i)
	}
	return out, true
}

// table は列の見出しと、数値（右寄せ）の列の範囲・折り返さない列から表を作る。
func table(columns []string, rows [][]any, numFrom, numTo int, nowrap []int, notes []string) *reporttable.Table {
	t := &reporttable.Table{Rows: rows}
	for i, c := range columns {
		t.Columns = append(t.Columns, reporttable.Column{Head: c, Number: i >= numFrom && i <= numTo})
	}
	for _, i := range nowrap {
		t.Columns[i].NoWrap = true
	}
	for _, n := range notes {
		if n != "" {
			t.Notes = append(t.Notes, n)
		}
	}
	return t
}

// limited は上位 n 件に絞る（n は本文の limit、無ければ既定）。
func limited(lang i18n.Lang, items []any, name string, limit any) ([]any, string, error) {
	n, ok := 0, false
	if limit != nil {
		if n, ok = isInt(limit); !ok {
			return nil, "", i18n.Errorf("report.check.err.limit_not_int", "value", str(limit))
		}
	} else {
		n, ok = defaultLimits[name]
	}
	if ok && len(items) > n {
		k := n
		if k < 0 { // 負の上限は末尾から数える
			k = max(len(items)+k, 0)
		}
		return items[:k], i18n.T(lang, "report.note.limited", "limit", n, "total", len(items)), nil
	}
	return items, "", nil
}

// AutoTable はサーバの集計から数表を 1 つ作る。heading は既定の見出し。
func AutoTable(lang i18n.Lang, report any, name string, limit any) (heading string, t *reporttable.Table, err error) {
	if name == "summary" {
		un := or(get(report, "unattributed"), obj{})
		row := func(label string, counters any, stages any) []any {
			return append(append([]any{label}, tokenCells(counters)...), Num(stages))
		}
		rows := [][]any{
			row(i18n.T(lang, "report.row.total"), get(report, "total"), get(report, "stage_count")),
			row(i18n.T(lang, "report.row.unattributed"), get(un, "total"), get(un, "stages")),
			row(i18n.T(lang, "report.row.excluded"), get(report, "excluded_total"), get(report, "excluded_stage_count")),
		}
		var notes []string
		if ex := get(report, "excluded_conversations"); truthy(ex) {
			parts := []string{}
			for _, c := range list(ex) {
				parts = append(parts, str(c))
			}
			notes = append(notes, i18n.T(lang, "report.note.excluded_conversations", "list", strings.Join(parts, ", ")))
		}
		if inc := get(report, "inconsistent"); truthy(inc) {
			notes = append(notes, i18n.T(lang, "report.note.inconsistent", "count", str(inc)))
		}
		cols := append(append([]string{i18n.T(lang, "report.col.item")}, tokenColumns(lang)...), i18n.T(lang, "report.col.stages"))
		return i18n.T(lang, "report.heading.summary"), table(cols, rows, 1, len(cols)-1, []int{0}, notes), nil
	}
	items, note, err := limited(lang, list(get(report, name)), name, limit)
	if err != nil {
		return "", nil, err
	}
	switch name {
	case "by_issue":
		var rows [][]any
		for _, it := range items {
			rows = append(rows, append(append([]any{str(get(it, "id")), sget(it, "type"), sget(it, "status"), sget(it, "title")},
				tokenCells(get(it, "total"))...), Num(get(it, "stages"))))
		}
		cols := append(append([]string{"ID", i18n.T(lang, "report.col.type"), i18n.T(lang, "report.col.status"),
			i18n.T(lang, "report.col.title")}, tokenColumns(lang)...), i18n.T(lang, "report.col.stages"))
		return i18n.T(lang, "report.heading.by_issue"), table(cols, rows, 4, len(cols)-1, []int{0, 1, 2}, []string{note}), nil
	case "conversations":
		z := tzOf(report)
		var rows [][]any
		for _, c := range items {
			id := []rune(sget(c, "conversation_id"))
			if len(id) > 12 {
				id = id[:12]
			}
			var issues []string
			for _, x := range list(get(c, "issues")) {
				issues = append(issues, str(x))
			}
			rows = append(rows, []any{string(id), sget(c, "client"), z.at(get(c, "first_at")), z.at(get(c, "last_at")),
				Num(get(c, "total_tokens")), Num(get(c, "unattributed_tokens")), strings.Join(issues, ", ")})
		}
		cols := []string{i18n.T(lang, "report.col.conversation_id"), i18n.T(lang, "report.col.client"),
			i18n.T(lang, "report.col.first"), i18n.T(lang, "report.col.last"), i18n.T(lang, "report.col.tokens"),
			i18n.T(lang, "report.col.unattributed"), i18n.T(lang, "report.col.issue_list")}
		return i18n.T(lang, "report.heading.conversations"),
			table(cols, rows, 4, 5, []int{0, 1, 2, 3}, []string{note, z.note(lang)}), nil
	}
	var h [2]string
	switch name {
	case "by_case":
		h = [2]string{i18n.T(lang, "report.heading.by_case"), i18n.T(lang, "report.col.case")}
	case "by_label":
		h = [2]string{i18n.T(lang, "report.heading.by_label"), i18n.T(lang, "report.col.label")}
	case "by_type":
		h = [2]string{i18n.T(lang, "report.heading.by_type"), i18n.T(lang, "report.col.type")}
	case "by_stage":
		h = [2]string{i18n.T(lang, "report.heading.by_stage"), i18n.T(lang, "report.col.stage")}
	case "by_client":
		h = [2]string{i18n.T(lang, "report.heading.by_client"), i18n.T(lang, "report.col.client")}
	default:
		return "", nil, i18n.Errorf("report.check.err.unknown_table", "name", name)
	}
	var rows [][]any
	for _, g := range items {
		key := str(or(get(g, "key"), i18n.T(lang, "report.value.none")))
		if name == "by_stage" {
			label, _ := stageName(lang, key)
			key = i18n.T(lang, "report.value.stage", "label", label, "key", key)
		}
		rows = append(rows, append(append([]any{key}, tokenCells(get(g, "total"))...), Num(get(g, "stages")), Num(get(g, "issues"))))
	}
	notes := []string{note}
	if name == "by_label" {
		notes = append(notes, i18n.T(lang, "report.note.by_label"))
	}
	if name == "by_case" {
		switch {
		case !has(report, "by_case"):
			notes = append(notes, i18n.T(lang, "report.note.by_case_missing"))
		case truthy(get(report, "case_pattern")):
			notes = append(notes, i18n.T(lang, "report.note.by_case_pattern", "pattern", str(get(report, "case_pattern"))))
		default:
			notes = append(notes, i18n.T(lang, "report.note.by_case_unset", "none", i18n.T(lang, "report.value.none")))
		}
	}
	cols := append(append([]string{h[1]}, tokenColumns(lang)...), i18n.T(lang, "report.col.stages"), i18n.T(lang, "report.col.issues"))
	return h[0], table(cols, rows, 1, len(cols)-1, nil, notes), nil
}

// periodText は対象期間を、機械可読な from / to（RFC 3339）から出す側の言語で描く。
// 集計 JSON の period はサーバが描いた文字列（サーバ側の言語）なので使わない。
// 使うと、英語で出す PDF の対象期間だけが日本語になる。
// to が無い古い集計 JSON のときだけ、サーバの描画済みの文をそのまま出す。
func periodText(lang i18n.Lang, report any) string {
	z := tzOf(report)
	if to := z.at(get(report, "to")); to != "" {
		from := z.at(get(report, "from"))
		if from == "" {
			from = i18n.T(lang, "report.meta.period_start_all")
		}
		return i18n.T(lang, "report.meta.period_range", "from", from, "to", to, "tz", z.label(lang))
	}
	if p := get(report, "period"); truthy(p) {
		return str(p)
	}
	return ""
}

func metaLines(lang i18n.Lang, report, content any) []string {
	z := tzOf(report)
	var lines []string
	if truthy(get(content, "subtitle")) {
		lines = append(lines, str(get(content, "subtitle")))
	}
	lines = append(lines, i18n.T(lang, "report.meta.period", "period", periodText(lang, report)))
	lines = append(lines, i18n.T(lang, "report.meta.data_end", "time", z.at(get(report, "data_end")), "tz", z.label(lang)))
	if truthy(get(report, "since_last")) {
		if last := get(report, "last_report"); truthy(last) {
			lines = append(lines, i18n.T(lang, "report.meta.since_last", "name", str(get(last, "name"))))
		} else {
			lines = append(lines, i18n.T(lang, "report.meta.since_empty"))
		}
	}
	if req := get(report, "request"); truthy(req) {
		text := i18n.T(lang, "report.meta.request", "id", str(get(req, "id")), "created", z.at(get(req, "created_at")),
			"by", str(or(get(req, "requested_by"), "-")))
		if truthy(get(req, "target")) {
			text += i18n.T(lang, "report.meta.request_target", "target", str(get(req, "target")))
		}
		lines = append(lines, text)
	}
	lines = append(lines, i18n.T(lang, "report.meta.source", "project", sget(report, "project"), "generated", sget(report, "generated")))
	if truthy(get(content, "author")) {
		lines = append(lines, str(get(content, "author")))
	}
	return lines
}

// Check は入力を検査する。
func Check(report, content any) error {
	if err := checkReport(report); err != nil {
		return err
	}
	return checkContent(content)
}

// Build は PDF に描く順のブロックを返す（build_blocks）。数値（右寄せ）の列と nowrap の列は折り返さない（列幅は reporttable.Fit）。
func Build(lang i18n.Lang, report, content any) ([]Block, error) {
	if err := Check(report, content); err != nil {
		return nil, err
	}
	sections := list(get(content, "sections"))
	hasAuto := false
	for _, s := range sections {
		hasAuto = hasAuto || has(s, "auto")
	}
	if !hasAuto { // 数表の位置の指定が無ければ最初の章の後に全部入れる
		all := append([]any{}, sections[:1]...)
		for _, a := range AutoTables {
			all = append(all, obj{"auto": a})
		}
		sections = append(all, sections[1:]...)
	}
	blocks := []Block{{Kind: Title, Text: strings.TrimSpace(str(get(content, "title")))}, {Kind: Meta, Lines: metaLines(lang, report, content)}}
	for _, sec := range sections {
		if has(sec, "auto") {
			heading, t, err := AutoTable(lang, report, get(sec, "auto").(string), get(sec, "limit"))
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, Block{Kind: Heading, Text: str(or(get(sec, "heading"), heading))})
			if len(t.Rows) == 0 {
				blocks = append(blocks, Block{Kind: Paragraph, Text: i18n.T(lang, "report.body.no_rows")})
				continue
			}
			blocks = append(blocks, Block{Kind: TableKind, Table: t})
			continue
		}
		blocks = append(blocks, Block{Kind: Heading, Text: strings.TrimSpace(str(get(sec, "heading")))})
		paragraphs, _ := strList(get(sec, "paragraphs"))
		for _, p := range paragraphs {
			blocks = append(blocks, Block{Kind: Paragraph, Text: p})
		}
		if bullets, _ := strList(get(sec, "bullets")); len(bullets) > 0 {
			blocks = append(blocks, Block{Kind: Bullets, Lines: bullets})
		}
		if tb := get(sec, "table"); truthy(tb) {
			cols := list(get(tb, "columns"))
			heads := make([]string, len(cols))
			for i, c := range cols {
				heads[i] = str(c)
			}
			var rows [][]any
			for _, r := range list(get(tb, "rows")) {
				row := []any{}
				for _, c := range list(r) {
					row = append(row, str(c))
				}
				rows = append(rows, row)
			}
			t := &reporttable.Table{Rows: rows}
			for _, h := range heads {
				t.Columns = append(t.Columns, reporttable.Column{Head: h})
			}
			numeric, _ := columnIndexes(or(get(tb, "numeric"), []any{}), len(cols))
			for _, i := range numeric {
				t.Columns[i].Number = true
			}
			nowrap, _ := columnIndexes(or(get(tb, "nowrap"), []any{}), len(cols))
			for _, i := range nowrap {
				t.Columns[i].NoWrap = true
			}
			for _, n := range list(get(tb, "notes")) {
				t.Notes = append(t.Notes, str(n))
			}
			blocks = append(blocks, Block{Kind: TableKind, Table: t})
		}
		if truthy(get(sec, "note")) {
			blocks = append(blocks, Block{Kind: Note, Text: str(get(sec, "note"))})
		}
	}
	return blocks, nil
}

// Tables はブロックの表だけ。
func Tables(blocks []Block) []*reporttable.Table {
	var out []*reporttable.Table
	for _, b := range blocks {
		if b.Kind == TableKind {
			out = append(out, b.Table)
		}
	}
	return out
}
