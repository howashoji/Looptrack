package domain

import "github.com/howashoji/looptrack/internal/mdformat"

// AssigneeKey は表示用の frontmatter に差し込む担当者のキー（DESIGN.md §5-1）。
// 担当者はサーバだけの項目で、保存する Document（front_keys）には入らない。
const AssigneeKey = "assignee"

// WithAssignee は、担当者 login を status の次（status が無ければ末尾）に差し込んだ表示用の写しを返す。
// login が空なら doc をそのまま返す（担当の無いイシューの出力は変わらない）。doc 自体は変えない。
func WithAssignee(doc *mdformat.Document, login string) *mdformat.Document {
	if login == "" || doc == nil {
		return doc
	}
	out := *doc
	out.Front = make([]mdformat.Field, 0, len(doc.Front)+1)
	field := mdformat.Field{Key: AssigneeKey, Value: login}
	inserted := false
	for _, f := range doc.Front {
		if f.Key == AssigneeKey {
			continue
		}
		out.Front = append(out.Front, f)
		if f.Key == "status" && !inserted {
			out.Front = append(out.Front, field)
			inserted = true
		}
	}
	if !inserted {
		out.Front = append(out.Front, field)
	}
	return &out
}

// StripAssignee は Document の frontmatter から担当者の行を取り除き、その値と有無を返す（edit / push で送られた全文用）。
func StripAssignee(doc *mdformat.Document) (string, bool) {
	for i, f := range doc.Front {
		if f.Key == AssigneeKey {
			doc.Front = append(doc.Front[:i:i], doc.Front[i+1:]...)
			if f.IsList {
				return "", true
			}
			return f.Value, true
		}
	}
	return "", false
}
