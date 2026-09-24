package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

// 下位がすべて完了した要件の案内（DESIGN.md §5-10）。判定は domain.ClosableRequirements / ClosableFor。
// close の応答（REST の messages と requirements_ready・MCP set_status）、matrix（md / json・MCP get_matrix）、
// summary の ①（REST・MCP project_summary・CLI の summary）に同じ結果を出す。

// 案内の文面（command・message・内訳）は要求の言語（REST は reqLang(r)・MCP は mcpLang）で作る。
// 言語は呼ぶ側から引数で受け取る（この層は要求ごとの言語をグローバルに置かない。lang.go の langFor）。
// matrix.md の警告節（domain.ClosableMarkdown）だけは生成物なので常に日本語で書く（理由はそちらの注釈）。

// closableJSON は requirements_ready[] の 1 件。
type closableJSON struct {
	Requirement issueRefJSON `json:"requirement"`
	Children    []string     `json:"children"`
	Done        int          `json:"done"`
	Canceled    int          `json:"canceled"`
	Command     string       `json:"command"` // 要件を閉じるコマンド（検証の後に打つ）
	Message     string       `json:"message"` // close の応答に出す案内（CLI・MCP 共通）
}

func closablesJSON(lang i18n.Lang, list []domain.ClosableRequirement) []closableJSON {
	out := make([]closableJSON, 0, len(list))
	for _, c := range list {
		out = append(out, closableJSON{Requirement: refs([]domain.Issue{c.Requirement})[0], Children: c.Children,
			Done: c.Done, Canceled: c.Canceled, Command: c.CloseCommand(lang), Message: c.Notice(lang)})
	}
	return out
}

// closedRequirements は状態変更で閉じたイシューが traces で指す要件のうち、それで下位がすべて完了したもの。
// 閉じる変更でなければ（開いたまま・閉じた状態どうしの変更）nil。状態変更は済んでいるので、読み込みの失敗は案内を省くだけにする。
func (s *Server) closedRequirements(ctx context.Context, pr store.Project, res *service.StatusResult) []domain.ClosableRequirement {
	it := res.Issue
	if !it.Closed() || domain.Valid(domain.ClosedStatuses, res.From) || len(it.Item.Traces) == 0 {
		return nil
	}
	set, _, err := s.svc.ProjectIssues(ctx, pr)
	if err != nil {
		s.cfg.Logger.Error("closable requirements", "issue", it.Item.ID, "err", err)
		return nil
	}
	cur, ok := set.Get(it.Item.ID)
	if !ok {
		return nil
	}
	return set.ClosableFor(cur)
}

// withClosable は状態変更の応答の messages に案内を足し、構造化結果に requirements_ready を足す（対象が無ければ足さない）。
func withClosable(lang i18n.Lang, messages []string, body map[string]any, list []domain.ClosableRequirement) []string {
	if len(list) == 0 {
		return messages
	}
	for _, c := range list {
		messages = append(messages, c.Notice(lang))
	}
	body["requirements_ready"] = closablesJSON(lang, list)
	return messages
}

// closableSummaryText は summary の ① に出す節（以前の CLI と同じ文言）。対象が無ければ空。
func closableSummaryText(lang i18n.Lang, list []closableJSON, total int) string {
	if total == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(i18n.T(lang, "server.api.summary.closable_heading", "count", total) + "\n")
	for _, c := range list {
		fmt.Fprintf(&b, "%-9s %s（%s）→ %s\n", c.Requirement.ID, c.Requirement.Title, breakdown(lang, c.Done, c.Canceled), c.Command)
	}
	if rest := total - len(list); rest > 0 {
		b.WriteString(i18n.T(lang, "server.api.summary.closable_more", "count", rest) + "\n")
	}
	return b.String()
}

func breakdown(lang i18n.Lang, done, canceled int) string {
	return domain.ClosableRequirement{Done: done, Canceled: canceled}.Breakdown(lang)
}

// matrixMarkdown は API モードの matrix.md: ファイルモードと同じ表（domain.BuildMatrixMarkdown。比較テストの対象）の後に、
// 下位がすべて完了したのに開いている要件の警告節を足す（要件が 1 件も無ければ足さない）。
func (s *Server) matrixMarkdown(set *domain.Set) string {
	md := set.BuildMatrixMarkdown(s.stamp())
	if len(set.BuildMatrixData().Rows) == 0 {
		return md
	}
	return md + domain.ClosableMarkdown(set.ClosableRequirements()) + "\n"
}
