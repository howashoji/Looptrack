package cli

import (
	"fmt"
	"strings"

	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/i18n"
)

// 一覧の表（列の幅・行・見出しの組み立て。API モードの幅の規則。DESIGN.md §6-2）。

var (
	dateKeys      = []string{"updated", "created"}
	defaultWidths = map[string]int{"id": 9, "blocked": 8, "assignee": 12}
)

const blockedMax = 26

func runeLen(s string) int { return len([]rune(s)) }

func blockedText(it *jsonorder.Object) string {
	if s := strings.Join(strList(it, "blocked_by"), ","); s != "" {
		return s
	}
	return "-"
}

func assigneeLabel(it *jsonorder.Object) string {
	a := orStr(it, "assignee", "")
	if a == "" {
		return "-"
	}
	if truthy(it, "assignee_inactive") {
		return a + "(!)"
	}
	return a
}

func columnWidths(items []*jsonorder.Object, withAssignee bool) map[string]int {
	w := map[string]int{}
	for k, v := range defaultWidths {
		w[k] = v
	}
	for _, it := range items {
		w["id"] = max(w["id"], runeLen(getStr(it, "id", "?")))
		w["blocked"] = max(w["blocked"], runeLen(blockedText(it)))
		if withAssignee {
			w["assignee"] = max(w["assignee"], runeLen(assigneeLabel(it)))
		}
	}
	w["blocked"] = min(w["blocked"], blockedMax)
	return w
}

func rowText(it *jsonorder.Object, dateKey string, withAssignee bool, w map[string]int) string {
	blocked := blockedText(it)
	if r := []rune(blocked); len(r) > w["blocked"] {
		blocked = string(r[:w["blocked"]-1]) + "…"
	}
	date := ""
	if dateKey != "" {
		date = fmt.Sprintf("%-16s ", orStr(it, dateKey, "-"))
	}
	if withAssignee {
		date += fmt.Sprintf("%-*s ", w["assignee"], assigneeLabel(it))
	}
	return fmt.Sprintf("%-*s %-11s %-11s %-3s %-*s %s%s",
		w["id"], getStr(it, "id", "?"), getStr(it, "type", "?"), getStr(it, "status", "?"),
		getStr(it, "priority", "?"), w["blocked"], blocked, date, getStr(it, "title", ""))
}

func headerText(dateKey string, withAssignee bool, w map[string]int) string {
	date := ""
	if dateKey != "" {
		date = fmt.Sprintf("%-16s ", strings.ToUpper(dateKey))
	}
	if withAssignee {
		date += fmt.Sprintf("%-*s ", w["assignee"], "ASSIGNEE")
	}
	return fmt.Sprintf("%-*s %-11s %-11s %-3s %-*s %s%s", w["id"], "ID", "TYPE", "STATUS", "PRI", w["blocked"], "BLOCKED", date, "TITLE")
}

// printRows は表の行を出す（担当の列は担当が 1 件でもあるときだけ）。
func (c *Ctx) printRows(items []*jsonorder.Object, sortKey string) {
	dateKey := ""
	for _, k := range dateKeys {
		if k == sortKey {
			dateKey = k
		}
	}
	withAssignee, inactive := false, false
	for _, it := range items {
		withAssignee = withAssignee || truthy(it, "assignee")
		inactive = inactive || truthy(it, "assignee_inactive")
	}
	w := columnWidths(items, withAssignee)
	c.Println(headerText(dateKey, withAssignee, w))
	for _, it := range items {
		c.Println(rowText(it, dateKey, withAssignee, w))
	}
	if inactive {
		// 担当が権限を外されたときの注
		c.Println(i18n.T(c.Lang, "cli.list.inactive_note"))
	}
}

// cmdList はイシューの一覧を出す。
func cmdList(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	if s := v.Str("status"); s != "" {
		if err := validate("--status", s, statuses); err != nil {
			return err
		}
	}
	path, err := c.ProjectPath("/issues?" + listQuery(v,
		[2]string{"status", v.Str("status")}, [2]string{"type", v.Str("type")}, [2]string{"label", v.Str("label")},
		[2]string{"ref", v.Str("ref")}, [2]string{"all", flag1(v.Bool("all"))}, [2]string{"assignee", v.Str("assignee")},
		[2]string{"has_feedback", flag1(v.Bool("has_feedback"))}))
	if err != nil {
		return err
	}
	res, err := c.getObject(cl, path)
	if err != nil {
		return err
	}
	if v.Bool("json") {
		c.PrintJSON(res)
		return nil
	}
	items, err := mustObjects(res, "items")
	if err != nil {
		return err
	}
	if len(items) == 0 {
		c.Println(i18n.T(c.Lang, "cli.none"))
		return nil
	}
	c.printRows(items, v.Str("sort"))
	c.Println("\n" + i18n.T(c.Lang, "cli.list.count", "count", len(items)))
	// 別のセッションが着手中のものを注記する（横取りの防止。summary と同じ文面）
	if note := otherSessionNote(c.Lang, items); note != "" {
		c.Println(note)
	}
	if note := crossPathNote(c.Lang, items); note != "" {
		c.Println(note)
	}
	if v.Bool("has_feedback") {
		var parts []string
		for _, it := range items {
			if truthy(it, "feedback_pending") {
				parts = append(parts, i18n.T(c.Lang, "cli.list.feedback_item", "id", getStr(it, "id", ""), "count", intOf(get(it, "feedback_pending", nil))))
			}
		}
		if len(parts) > 0 {
			c.Println(i18n.T(c.Lang, "cli.list.feedback_pending", "items", strings.Join(parts, i18n.T(c.Lang, "cli.sep.list"))))
		}
	}
	return nil
}

// cmdReady は着手できるイシューの一覧を出す。
func cmdReady(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	path, err := c.ProjectPath("/ready?" + listQuery(v, [2]string{"assignee", v.Str("assignee")}))
	if err != nil {
		return err
	}
	res, err := c.getObject(cl, path)
	if err != nil {
		return err
	}
	if v.Bool("json") {
		c.PrintJSON(res)
		return nil
	}
	items, err := mustObjects(res, "items")
	if err != nil {
		return err
	}
	if len(items) == 0 {
		c.Println(i18n.T(c.Lang, "cli.ready.none"))
		return nil
	}
	c.printRows(items, v.Str("sort"))
	c.Println("\n" + i18n.T(c.Lang, "cli.ready.count", "count", len(items)))
	return nil
}
