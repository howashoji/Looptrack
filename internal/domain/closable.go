package domain

import (
	"fmt"
	"sort"
	"strings"

	"github.com/howashoji/looptrack/internal/i18n"
)

// 下位がすべて完了した要件（DESIGN.md §5-10）。
//
// 下位 = traces でその要件を指すイシュー（型は問わない）。次をすべて満たす要件を「検証して close する」対象にする:
//   - 要件（type: requirement）が Backlog / Todo / In Progress（In Review は人の判断待ちとして ② に出るので除く。閉じていれば除く）
//   - 下位が 1 件以上あり、すべて閉じている（Done / Canceled）
//   - 下位のうち Done が 1 件以上ある（全部 Canceled は「作らないと決めた」だけで、受け入れ条件を満たした根拠が無いので除く）

// ClosableRequirement は下位がすべて完了した開いている要件 1 件。
type ClosableRequirement struct {
	Requirement Issue
	Children    []string // 下位の ID（ID 順）
	Done        int
	Canceled    int
}

// closableStatuses は対象にする要件の状態。
var closableStatuses = []string{"Backlog", "Todo", "In Progress"}

// children は traces で reqID を指すイシュー（ID 順）。
func (s *Set) children(reqID string) []Issue {
	var got []Issue
	for _, it := range s.Items {
		for _, t := range it.Traces {
			if strings.EqualFold(t, reqID) {
				got = append(got, it)
				break
			}
		}
	}
	sort.SliceStable(got, func(i, j int) bool { return got[i].ID < got[j].ID })
	return got
}

// closable は r が対象なら判定結果を返す。
func (s *Set) closable(r Issue) (ClosableRequirement, bool) {
	c := ClosableRequirement{Requirement: r}
	if r.Type != "requirement" || !contains(closableStatuses, r.Status) {
		return c, false
	}
	for _, ch := range s.children(r.ID) {
		switch ch.Status {
		case "Done":
			c.Done++
		case "Canceled":
			c.Canceled++
		default:
			return c, false
		}
		c.Children = append(c.Children, ch.ID)
	}
	return c, c.Done > 0
}

// ClosableRequirements はプロジェクト全体で下位がすべて完了した開いている要件（要件の ID 順）。
func (s *Set) ClosableRequirements() []ClosableRequirement {
	var out []ClosableRequirement
	for _, it := range s.Items {
		if c, ok := s.closable(it); ok {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Requirement.ID < out[j].Requirement.ID })
	return out
}

// ClosableFor は閉じたイシュー closed が traces で指す要件のうち、下位がすべて完了したもの（traces の順）。
// s は closed を閉じた後の状態で渡す。
func (s *Set) ClosableFor(closed Issue) []ClosableRequirement {
	var out []ClosableRequirement
	seen := map[string]bool{}
	for _, t := range closed.Traces {
		r, ok := s.Get(t)
		if !ok || seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		if c, ok := s.closable(r); ok {
			out = append(out, c)
		}
	}
	return out
}

// Breakdown は「Done 3 件」「Done 2 件・Canceled 1 件」の内訳（lang の文面）。
func (c ClosableRequirement) Breakdown(lang i18n.Lang) string {
	if c.Canceled > 0 {
		return i18n.T(lang, "domain.closable.breakdown_canceled", "done", c.Done, "canceled", c.Canceled)
	}
	return i18n.T(lang, "domain.closable.breakdown", "done", c.Done)
}

// CloseCommand は要件を検証して閉じるときに打つ CLI のコマンド（lang の文面。--comment の中身は利用者が打つ文なので訳す）。
func (c ClosableRequirement) CloseCommand(lang i18n.Lang) string {
	return i18n.T(lang, "domain.closable.close_command", "id", c.Requirement.ID)
}

// Notice は close の応答に付ける案内（CLI・MCP で同じ文言）。
func (c ClosableRequirement) Notice(lang i18n.Lang) string {
	return i18n.T(lang, "domain.closable.notice", "id", c.Requirement.ID,
		"breakdown", c.Breakdown(lang), "command", c.CloseCommand(lang))
}

// ClosableMarkdown は matrix（API モード）に足す警告節。ファイルモードの matrix.md には出さない（比較テストの前提を変えない）。
//
// **常に日本語で書く。** これは matrix.md という生成物の一部で、report.go の表と同じ文書に並ぶ。
// 生成した人の言語で成果物の中身が変わると、版管理に入れたときに中身の違わない差分が出る。
func ClosableMarkdown(list []ClosableRequirement) string {
	const lang = i18n.JA
	lines := []string{i18n.T(lang, "domain.closable.md.heading", "count", len(list)), ""}
	if len(list) == 0 {
		lines = append(lines, i18n.T(lang, "domain.closable.md.none"))
	} else {
		lines = append(lines, i18n.T(lang, "domain.closable.md.lead"), "")
		for _, c := range list {
			lines = append(lines, fmt.Sprintf("- %s(%s) %s — %s", c.Requirement.ID, c.Requirement.Status, c.Requirement.Title,
				i18n.T(lang, "domain.closable.md.row", "breakdown", c.Breakdown(lang),
					"children", strings.Join(c.Children, ", "), "command", c.CloseCommand(lang))))
		}
	}
	return strings.Join(lines, "\n") + "\n"
}
