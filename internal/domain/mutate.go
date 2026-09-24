package domain

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/mdformat"
)

// NewIssueInput は起票の入力（issue new の引数）。
type NewIssueInput struct {
	Title     string
	Type      string
	Status    string
	Priority  string
	Labels    []string
	Parent    string
	BlockedBy []string
	Traces    []string
	Refs      []string
	Body      string // 「## 内容」節に入れる本文
	// OverrideReason は上書き可能なルール違反（usage・verify の require_on_close）を通す理由。文書には書かず、イベントに残す
	OverrideReason string
	// Assignee は担当者の指定（me / login。サーバだけの項目で文書には書かない）
	Assignee string
}

// Defaults は未指定の値を起票の既定値で埋める。
func (in *NewIssueInput) Defaults() {
	if in.Type == "" {
		in.Type = "task"
	}
	if in.Status == "" {
		in.Status = "Todo"
	}
	if in.Priority == "" {
		in.Priority = "P2"
	}
}

// Validate は列挙値を検査する（エラー文は以前の CLI と同じ）。
func (in NewIssueInput) Validate() error {
	if err := ValidateValue("--type", in.Type, Types); err != nil {
		return err
	}
	if err := ValidateValue("--priority", in.Priority, Priorities); err != nil {
		return err
	}
	return ValidateValue("--status", in.Status, Statuses)
}

// ValidateValue は列挙値の誤りを ID を持つ error で返す（文面は表示側が i18n.Text(lang, err) で作る）。
func ValidateValue(field, value string, allowed []string) error {
	if !contains(allowed, value) {
		return i18n.Errorf("domain.err.invalid_value", "field", field, "allowed", strings.Join(allowed, " / "), "value", value)
	}
	return nil
}

// FormatID は prefix と番号から ID を作る（例: REQ + 4 桁）。
func FormatID(prefix string, width, n int) string {
	return fmt.Sprintf("%s-%0*d", prefix, width, n)
}

// 起票の雛形（DESIGN.md §9-6）。**起票した利用者の言語**で入れる（英語なら ## Acceptance criteria）。
// 英語の別名を常に認める（heading.go）ので、日本語の雛形と英語の雛形が混ざっても受け入れ条件の取り出しは同じように効く。
//
// コメント節の見出しだけは訳さない。mdformat.CommentSection（"## コメント"）は本文とコメントを分ける
// **構造の目印**で、訳すと mdformat.Parse がコメント節を見つけられなくなる。ここでも定数から組み立て、
// 対訳表に写さない（写すと片方だけ直されて黙って壊れる）。

// NewDocument は起票の frontmatter・本文の Document を作る。now は "YYYY-MM-DD HH:MM"。
// lang は起票した利用者の言語（雛形の見出しがこれで決まる。domain は言語を決めず、呼ぶ側から受け取る）。
func NewDocument(id string, in NewIssueInput, now string, lang i18n.Lang) *mdformat.Document {
	nz := func(v []string) []string {
		if v == nil {
			return []string{}
		}
		return v
	}
	content := i18n.T(lang, "domain.template.empty")
	if in.Body != "" {
		content = in.Body
	}
	acceptance := i18n.T(lang, "domain.template.acceptance")
	if in.Body != "" && HasAcceptanceSection(in.Body) {
		// 本文が受け入れ条件を持つなら雛形の節は付けない（英語の見出しも同じ・§9-6）
		acceptance = ""
	}
	body := i18n.T(lang, "domain.template.body", "id", id, "title", in.Title,
		"background", i18n.T(lang, "domain.template.empty"), "content", content, "acceptance", acceptance) +
		mdformat.CommentSection + "\n"
	front := []mdformat.Field{
		{Key: "id", Value: id},
		{Key: "title", Value: in.Title},
		{Key: "type", Value: in.Type},
		{Key: "status", Value: in.Status},
		{Key: "priority", Value: in.Priority},
		{Key: "labels", IsList: true, List: nz(in.Labels)},
		{Key: "parent", Value: in.Parent},
		{Key: "blocked_by", IsList: true, List: nz(in.BlockedBy)},
		{Key: "traces", IsList: true, List: nz(in.Traces)},
		{Key: "refs", IsList: true, List: nz(in.Refs)},
		{Key: "created", Value: now},
		{Key: "updated", Value: now},
	}
	text := mdformat.Render(&mdformat.Document{Front: front}) + body
	doc, err := mdformat.Parse(text)
	if err != nil {
		// 本文に「## コメント」の見出しの形が混ざる等で分割規則に合わない場合でも、内容は失わない
		doc = &mdformat.Document{Front: front, BodyMain: strings.TrimRight(body, "\n"), TrailNL: 1}
	}
	return doc
}

// SplitList はカンマ区切り引数（"a,b"）を分ける。空文字は空リスト。
func SplitList(s string) []string {
	if s == "" {
		return []string{}
	}
	return strings.Split(s, ",")
}

// IDListFields はイシュー ID・文書 ID を入れるリスト項目。ID は空白を含まないので、空白を含む値は
// 「空白区切りで複数の ID を 1 要素に入れた」誤りとみなす。labels は名前なので空白を許す
// （移行時のラベル「CASE-101 保守リスク再監査 対応」など）。
var IDListFields = []string{"blocked_by", "traces", "refs"}

// IsIDListField は key が ID を入れるリスト項目か。
func IsIDListField(key string) bool { return contains(IDListFields, key) }

// HasSpace は値が空白（Unicode の空白。全角空白を含む）を含むか。
func HasSpace(v string) bool { return strings.IndexFunc(v, unicode.IsSpace) >= 0 }

// SplitSpaced は空白を含む要素を空白で分け、分けた結果の重複を除く（既存データの補正用）。
// 空白を含む要素が無ければ values をそのまま返し、changed は false。
func SplitSpaced(values []string) (out []string, changed bool) {
	for _, v := range values {
		if HasSpace(v) {
			changed = true
			break
		}
	}
	if !changed {
		return values, false
	}
	out = []string{}
	seen := map[string]bool{}
	for _, v := range values {
		for _, p := range strings.Fields(v) {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out, true
}

// Slugify は以前の CLI と同じファイル名部分を作る（最大 40 文字）。
func Slugify(title string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.TrimSpace(title) {
		if isSlugSep(r) {
			if !prevDash {
				b.WriteRune('-')
				prevDash = true
			}
			continue
		}
		if r == '-' {
			if !prevDash {
				b.WriteRune('-')
				prevDash = true
			}
			continue
		}
		b.WriteRune(r)
		prevDash = false
	}
	s := strings.Trim(b.String(), "-")
	if rs := []rune(s); len(rs) > 40 {
		s = string(rs[:40])
	}
	if s == "" {
		return "issue"
	}
	return s
}

// isSlugSep は区切りとして扱う文字（Unicode の空白と 0x1c–0x1f、/ \ : * ? " < > |）。
func isSlugSep(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) || strings.ContainsRune(`/\:*?"<>|`, r)
}

// FileName は起票が作るファイル名。
func FileName(id, title string) string { return id + "-" + Slugify(title) + ".md" }

// SetField はスカラー項目を更新する（無ければ末尾に足す）。
func SetField(d *mdformat.Document, key, value string) {
	if f := d.Field(key); f != nil {
		f.IsList, f.List, f.Value = false, nil, value
		return
	}
	d.Front = append(d.Front, mdformat.Field{Key: key, Value: value})
}

// AppendComment は以前の CLI と同じくコメントを追記し updated を now にする。
// 本文の前後の改行は取り除く（DB では前後の改行を持たない）。
func AppendComment(d *mdformat.Document, now, text string) {
	if !d.HasCommentSection {
		d.HasCommentSection, d.GapNL = true, 2
	}
	d.Comments = append(d.Comments, mdformat.Comment{TS: now, Content: strings.Trim(text, "\n")})
	d.TrailNL = 1
	SetField(d, "updated", now)
}

// SetStatus は状態と updated を更新し、変更前の状態を返す（列挙値の検査は呼び出し側）。
func SetStatus(d *mdformat.Document, status, now string) (old string) {
	if f := d.Field("status"); f != nil {
		old = f.Value
	}
	SetField(d, "status", status)
	SetField(d, "updated", now)
	return old
}

// SetList はリスト項目を更新する（無ければ末尾に足す）。
func SetList(d *mdformat.Document, key string, values []string) {
	if values == nil {
		values = []string{}
	}
	if f := d.Field(key); f != nil {
		f.IsList, f.List, f.Value = true, values, ""
		return
	}
	d.Front = append(d.Front, mdformat.Field{Key: key, IsList: true, List: values})
}
