package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/i18n"
)

// cmdIndex は全イシューから index.md と同じ一覧を作って標準出力に出す（書き込みはしない）。
func cmdIndex(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	path, err := c.ProjectPath("/issues?all=1&sort=id")
	if err != nil {
		return err
	}
	res, err := c.getObject(cl, path)
	if err != nil {
		return err
	}
	list, err := mustObjects(res, "items")
	if err != nil {
		return err
	}
	// load_all と同じ形（未クローズ → クローズ済み、それぞれ ID 順。同じ ID は後のものが勝つが位置は最初のまま）
	sort.SliceStable(list, func(i, j int) bool {
		ci, cj := truthy(list[i], "closed"), truthy(list[j], "closed")
		if ci != cj {
			return !ci
		}
		return getStr(list[i], "id", "") < getStr(list[j], "id", "")
	})
	items := newIssueMap()
	for _, it := range list {
		items.set(getStr(it, "id", ""), it)
	}
	loc, _ := serverTZ(c.Lang, res) // 作成日時も応答の timezone（サーバのローカル時刻）で描く
	io.WriteString(c.Stdout, buildIndex(c.Lang, items, time.Now().In(loc)))
	return nil
}

// cmdMatrix はサーバが作ったトレーサビリティ表をそのまま出す。
func cmdMatrix(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	path, err := c.ProjectPath("/matrix?format=md")
	if err != nil {
		return err
	}
	res, err := cl.Get(path)
	if err != nil {
		return err
	}
	io.WriteString(c.Stdout, jsonorder.Str(res))
	return nil
}

// issueMap は ID → イシュー（挿入順を保つ）。
type issueMap struct {
	order []string
	m     map[string]*jsonorder.Object
}

func newIssueMap() *issueMap { return &issueMap{m: map[string]*jsonorder.Object{}} }

func (im *issueMap) set(id string, it *jsonorder.Object) {
	if _, ok := im.m[id]; !ok {
		im.order = append(im.order, id)
	}
	im.m[id] = it
}

func (im *issueMap) values() []*jsonorder.Object {
	out := make([]*jsonorder.Object, len(im.order))
	for i, id := range im.order {
		out[i] = im.m[id]
	}
	return out
}

func isClosed(it *jsonorder.Object) bool {
	s := getStr(it, "status", "")
	return s == "Done" || s == "Canceled"
}

func rank(value string, order []string) int {
	for i, o := range order {
		if o == value {
			return i
		}
	}
	return len(order)
}

// sortByPriority は優先度順に並べる（同順位は ID 昇順）。
func sortByPriority(items []*jsonorder.Object) {
	sort.SliceStable(items, func(i, j int) bool { return getStr(items[i], "id", "") < getStr(items[j], "id", "") })
	sort.SliceStable(items, func(i, j int) bool {
		return rank(getStr(items[i], "priority", ""), priorities) < rank(getStr(items[j], "priority", ""), priorities)
	})
}

// readyItems は着手できるイシュー（未クローズ・進行中でない・blocked_by がすべてクローズ済み）。
func readyItems(items *issueMap) []*jsonorder.Object {
	var out []*jsonorder.Object
	for _, it := range items.values() {
		if isClosed(it) || getStr(it, "status", "") == "In Progress" {
			continue
		}
		ok := true
		for _, d := range strList(it, "blocked_by") {
			dep := items.m[strings.ToUpper(d)]
			if dep == nil {
				dep = items.m[d]
			}
			if dep == nil || !isClosed(dep) {
				ok = false
			}
		}
		if ok {
			out = append(out, it)
		}
	}
	sortByPriority(out)
	return out
}

// buildIndex は一覧の Markdown を組み立てる。
func buildIndex(lang i18n.Lang, items *issueMap, now time.Time) string {
	lines := []string{i18n.T(lang, "cli.index.title"), "",
		i18n.T(lang, "cli.index.generated"),
		i18n.T(lang, "cli.index.generated_at", "time", now.Format("2006-01-02 15:04")), ""}
	ready := readyItems(items)
	lines = append(lines, i18n.T(lang, "cli.index.ready_heading"), "", i18n.T(lang, "cli.index.ready_desc"), "")
	if len(ready) > 0 {
		lines = append(lines, i18n.T(lang, "cli.index.ready_header"), "| -- | -- | -- | -- |")
		for _, i := range ready {
			lines = append(lines, fmt.Sprintf("| %s | %s | %s | %s |",
				getStr(i, "id", ""), getStr(i, "type", ""), getStr(i, "priority", ""), getStr(i, "title", "")))
		}
	} else {
		lines = append(lines, i18n.T(lang, "cli.index.empty"))
	}
	lines = append(lines, "")
	for _, st := range statuses {
		var group []*jsonorder.Object
		for _, i := range items.values() {
			if getStr(i, "status", "") == st {
				group = append(group, i)
			}
		}
		sort.SliceStable(group, func(a, b int) bool { return getStr(group[a], "id", "") < getStr(group[b], "id", "") })
		lines = append(lines, i18n.T(lang, "cli.index.status_heading", "status", st, "count", len(group)), "")
		if len(group) > 0 {
			lines = append(lines, i18n.T(lang, "cli.index.group_header"), "| -- | -- | -- | -- | -- |")
			for _, i := range group {
				blocked := strings.Join(strList(i, "blocked_by"), ", ")
				if blocked == "" {
					blocked = "-"
				}
				lines = append(lines, fmt.Sprintf("| %s | %s | %s | %s | %s |",
					getStr(i, "id", ""), getStr(i, "type", ""), getStr(i, "priority", ""), blocked, getStr(i, "title", "")))
			}
		} else {
			lines = append(lines, i18n.T(lang, "cli.index.empty"))
		}
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n") + "\n"
}
