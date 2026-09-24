// Package mdformat は、イシュー Markdown（frontmatter + 本文）と構造化データを相互変換する。
//
// DB に保存した値から元ファイルをバイト単位で復元できることが要件。
// 分割規則は実データ 681 件で往復一致を確認したもの（docs/server/DESIGN.md §3）:
//
//	---\n<frontmatter>\n---\n\n<BodyMain>{"\n"×GapNL}## コメント
//	[\n\n<Preamble>]{\n\n### <TS>[\n\n<Content>]}…  → 末尾の改行を除き "\n"×TrailNL
//
// Parse は変換後に必ず Render と突き合わせ、一致しなければ *RoundTripError を返す。
// 規則に合わないファイルを黙って正規化（＝内容の欠落）しないため。
package mdformat

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// CommentSection はコメント節の見出し行。
const CommentSection = "## コメント"

var tsHeading = regexp.MustCompile(`^### (\d{4}-\d{2}-\d{2} \d{2}:\d{2})$`)

// Field は frontmatter の 1 行。出現順を保つためスライスで持つ。
type Field struct {
	Key    string
	IsList bool
	Value  string   // IsList == false のとき
	List   []string // IsList == true のとき
}

// Comment は `### YYYY-MM-DD HH:MM` 見出しで始まる 1 件のコメント。
type Comment struct {
	TS      string // "YYYY-MM-DD HH:MM"
	Content string // 前後の改行を除いた本文。空文字もありうる
}

// Document は 1 イシュー分の Markdown を分解したもの。
type Document struct {
	Front             []Field
	BodyMain          string  // コメント節より前の本文（末尾の改行を除く）
	GapNL             int     // BodyMain とコメント節見出しの間の改行数
	HasCommentSection bool    // "## コメント" 行があるか
	Preamble          *string // コメント節見出しと最初のコメントの間の文章（無ければ nil）
	Comments          []Comment
	TrailNL           int // ファイル末尾の改行数
}

// RoundTripError は、分解した結果から元のテキストを復元できないことを表す。
type RoundTripError struct {
	Offset   int    // 最初に異なるバイト位置
	Original string // 差分付近の元テキスト
	Rendered string // 差分付近の復元テキスト
}

func (e *RoundTripError) Error() string {
	return fmt.Sprintf("往復一致しません（%d バイト目）: 元=%q 復元=%q", e.Offset, e.Original, e.Rendered)
}

// ErrFormat はイシューの Markdown 形式として解釈できないことを表す。
var ErrFormat = errors.New("イシューの Markdown 形式ではありません")

// Field を名前で引く（無ければ nil）。
func (d *Document) Field(key string) *Field {
	for i := range d.Front {
		if d.Front[i].Key == key {
			return &d.Front[i]
		}
	}
	return nil
}

// Parse は Markdown を分解する。復元結果が元と一致しない場合は *RoundTripError を返す。
func Parse(text string) (*Document, error) {
	doc, err := split(text)
	if err != nil {
		return nil, err
	}
	if rendered := Render(doc); rendered != text {
		return nil, diff(text, rendered)
	}
	return doc, nil
}

func split(text string) (*Document, error) {
	if !strings.HasPrefix(text, "---\n") {
		return nil, fmt.Errorf("%w: 先頭が frontmatter（---）ではありません", ErrFormat)
	}
	end := strings.Index(text[3:], "\n---")
	if end < 0 {
		return nil, fmt.Errorf("%w: frontmatter の終わり（---）がありません", ErrFormat)
	}
	end += 3
	doc := &Document{}
	if end > 3 {
		for _, line := range strings.Split(text[4:end], "\n") {
			doc.Front = append(doc.Front, parseField(line))
		}
	}
	rest := text[end+len("\n---"):]
	if !strings.HasPrefix(rest, "\n\n") {
		return nil, fmt.Errorf("%w: frontmatter の後に空行 1 行がありません", ErrFormat)
	}
	body := rest[2:]
	doc.TrailNL = len(body) - len(strings.TrimRight(body, "\n"))

	lines := strings.Split(body, "\n")
	fence := fenceMap(lines)
	idx := -1
	for i, l := range lines {
		if l == CommentSection && !fence[i] {
			idx = i
			break
		}
	}
	if idx < 0 {
		doc.BodyMain = strings.TrimRight(body, "\n")
		return doc, nil
	}
	doc.HasCommentSection = true
	pre := strings.Join(lines[:idx], "\n")
	doc.BodyMain = strings.TrimRight(pre, "\n")
	if idx > 0 {
		doc.GapNL = len(pre) - len(doc.BodyMain) + 1
	}

	after := lines[idx+1:]
	afterFence := fence[idx+1:]
	var starts []int
	for j, l := range after {
		if !afterFence[j] && tsHeading.MatchString(l) {
			starts = append(starts, j)
		}
	}
	first := len(after)
	if len(starts) > 0 {
		first = starts[0]
	}
	if p := strings.Trim(strings.Join(after[:first], "\n"), "\n"); p != "" {
		doc.Preamble = &p
	}
	for n, s := range starts {
		e := len(after)
		if n+1 < len(starts) {
			e = starts[n+1]
		}
		doc.Comments = append(doc.Comments, Comment{
			TS:      after[s][len("### "):],
			Content: strings.Trim(strings.Join(after[s+1:e], "\n"), "\n"),
		})
	}
	return doc, nil
}

// parseField は以前の CLI（1.0.0 より前）と同じ規則で 1 行を解釈する。
func parseField(line string) Field {
	key, val, _ := strings.Cut(line, ":")
	key, val = strings.TrimSpace(key), strings.TrimSpace(val)
	if strings.HasPrefix(val, "[") && strings.HasSuffix(val, "]") && len(val) >= 2 {
		f := Field{Key: key, IsList: true, List: []string{}}
		for _, item := range strings.Split(val[1:len(val)-1], ",") {
			if item = strings.TrimSpace(item); item != "" {
				f.List = append(f.List, item)
			}
		}
		return f
	}
	return Field{Key: key, Value: val}
}

// fenceMap は各行がコードブロック内（開始・終了行を含む）かを返す。
// 実データ検証スクリプトと同じ規則: 行頭の空白に続く ``` または ~~~ で開き、
// 同じ文字 3 つで始まる行で閉じる。
func fenceMap(lines []string) []bool {
	out := make([]bool, len(lines))
	in := false
	var marker string
	for i, l := range lines {
		t := strings.TrimLeftFunc(l, unicode.IsSpace)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			if !in {
				in, marker = true, t[:3]
			} else if strings.HasPrefix(strings.TrimRightFunc(t, unicode.IsSpace), marker) {
				in = false
			}
			out[i] = true
			continue
		}
		out[i] = in
	}
	return out
}

// Render は Document を Markdown に戻す。
func Render(d *Document) string {
	var b strings.Builder
	b.WriteString("---\n")
	for _, f := range d.Front {
		b.WriteString(f.Key)
		b.WriteString(": ")
		if f.IsList {
			b.WriteString("[" + strings.Join(f.List, ", ") + "]")
		} else {
			b.WriteString(f.Value)
		}
		b.WriteString("\n")
	}
	b.WriteString("---\n\n")

	var body strings.Builder
	body.WriteString(d.BodyMain)
	if d.HasCommentSection {
		body.WriteString(strings.Repeat("\n", d.GapNL))
		body.WriteString(CommentSection)
		if d.Preamble != nil {
			body.WriteString("\n\n" + *d.Preamble)
		}
		for _, c := range d.Comments {
			body.WriteString("\n\n### " + c.TS)
			if c.Content != "" {
				body.WriteString("\n\n" + c.Content)
			}
		}
	}
	b.WriteString(strings.TrimRight(body.String(), "\n"))
	b.WriteString(strings.Repeat("\n", d.TrailNL))
	return b.String()
}

func diff(orig, rendered string) *RoundTripError {
	i := 0
	for i < len(orig) && i < len(rendered) && orig[i] == rendered[i] {
		i++
	}
	clip := func(s string) string {
		lo, hi := max(0, i-30), min(len(s), i+30)
		return s[lo:hi]
	}
	return &RoundTripError{Offset: i, Original: clip(orig), Rendered: clip(rendered)}
}

// RenderBody は frontmatter を除いた本文（以前の CLI と同じ範囲）を返す。
func RenderBody(d *Document) string {
	full := Render(d)
	if i := strings.Index(full, "\n---\n\n"); i >= 0 {
		return full[i+len("\n---\n\n"):]
	}
	return full
}
