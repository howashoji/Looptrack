package server

import (
	"strings"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// MCP の添付の扱い。MCP のツールはバイナリを受けない（引数の型は文字列・数・真偽・それらの配列だけで、
// base64 で渡すと文脈を食う）。ファイルは CLI の looptrack issue attach で送り、返った ID を
// add_comment・report_verify の attachments に渡す。ID が属するイシューかどうか・消去済みかどうか・個数の上限は
// REST と同じ service の入口（attachmentRefs）が決めるので、ここでは何も判定しない。
// 一覧は専用のツールにせず get_issue に載せる（ツールを増やすと名前の一覧が 4 か所に波及し、
// 文脈にも毎回その説明が載る。添付を付けるのは、そのイシューを読んだ後）。
//
// 一覧を置く場所は、version の行と Markdown の間。update_issue は全文を mdformat.Parse で読み、
// 「## コメント」より後ろをコメント節として扱う。本文の末尾に足すと、コメント節のあるイシューは comments_changed で
// 原因の分からない拒否になり、コメント節の無いイシュー（移行で取り込んだもの）は一覧が本文に入って黙って保存される。
// 先頭に置けば、version の行だけ消して一覧を残した全文は、frontmatter で始まらないので Parse が必ず止める。

// mcpAttachment は get_issue が返す添付 1 件（本体の取り方・ハッシュ・作成の日時は返さない）。
type mcpAttachment struct {
	ID        int64  `json:"id"`
	Filename  string `json:"filename"`
	MediaType string `json:"media_type"`
	Size      int64  `json:"size"`
	Purged    bool   `json:"purged"`
}

// mcpIssueDetail は get_issue の構造化データ。添付が無いイシューは、これまでと同じ形になる。
type mcpIssueDetail struct {
	issueDetailJSON
	Attachments []mcpAttachment `json:"attachments,omitempty"`
}

func toMCPAttachments(list []store.Attachment) []mcpAttachment {
	var out []mcpAttachment
	for _, a := range list {
		out = append(out, mcpAttachment{ID: a.ID, Filename: a.Filename, MediaType: a.MediaType, Size: a.Size, Purged: a.Purged})
	}
	return out
}

// mcpAttachmentsText は get_issue の本文の末尾に足す添付の一覧（添付が無ければ空）。
func mcpAttachmentsText(lang i18n.Lang, list []store.Attachment) string {
	if len(list) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(i18n.TN(lang, "server.mcp.issue.attachments_heading", len(list), "count", len(list)))
	for _, a := range list {
		if a.Purged {
			b.WriteString("\n" + i18n.TN(lang, "server.mcp.issue.attachment_line_purged", int(a.Size), "id", a.ID, "filename", a.Filename, "media_type", a.MediaType, "size", a.Size))
			continue
		}
		b.WriteString("\n" + i18n.TN(lang, "server.mcp.issue.attachment_line", int(a.Size), "id", a.ID, "filename", a.Filename, "media_type", a.MediaType, "size", a.Size))
	}
	return b.String()
}
