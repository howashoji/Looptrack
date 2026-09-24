package server

import (
	"strings"
	"testing"
)

// 一覧の表で BLOCKED が長くても ASSIGNEE・TITLE の列がそろう（API モードの CLI と MCP の rowsText が同じ幅の規則）。
func TestRowsColumnsAligned(t *testing.T) {
	l := newLoopsEnv(t)
	a := l.create("一つ目", map[string]any{"assignee": "me"})
	b := l.create("二つ目", nil)
	c := l.create("待っているもの", map[string]any{"blocked_by": []string{a, b}, "assignee": "me"})
	l.create("たくさん待っているもの", map[string]any{"blocked_by": []string{a, b, c, a + "X"}})

	m := l.e.mcpAs(l.ed.token, map[string]string{"X-Looptrack-Project": l.pr.Slug})
	text, _ := m.call("list_issues", map[string]any{"sort": "id"}, false)
	lines := strings.Split(strings.SplitN(text, "\n\n", 2)[0], "\n")
	if len(lines) != 5 {
		t.Fatalf("MCP list_issues: %q", text)
	}
	head := lines[0] // 見出しは ASCII だけなので、バイトの位置 = 文字の位置
	ta, tt := strings.Index(head, "ASSIGNEE"), strings.Index(head, "TITLE")
	for i, want := range []string{"一つ目", "二つ目", "待っているもの", "たくさん待っているもの"} {
		row := []rune(lines[i+1]) // 行は「…」を含みうるので文字の位置で見る
		if len(row) <= tt || !strings.HasPrefix(string(row[tt:]), want) || row[ta-1] != ' ' || row[tt-1] != ' ' {
			t.Errorf("MCP の %d 行目の列がずれる:\n%s", i+1, strings.Join(lines, "\n"))
		}
	}
	if !strings.Contains(lines[3], " "+a+","+b+" ") {
		t.Errorf("26 文字以内の BLOCKED はそのまま出す: %q", lines[3])
	}
	if long := a + "," + b + "," + c; !strings.Contains(lines[4], " "+long[:25]+"… ") {
		t.Errorf("26 文字を超える BLOCKED は 26 文字に切って … を付ける: %q", lines[4])
	}

	// CLI の list は MCP と同じ表
	cli := newCLIEnv(t, l.e, l.pr.Slug, l.ed.token)
	r := mustCLI(t, cli.run("", "list", "--sort", "id"), 0, "list")
	if strings.TrimRight(r.stdout, "\n") != text {
		t.Errorf("CLI と MCP の表が違う:\n--- CLI\n%s\n--- MCP\n%s", r.stdout, text)
	}
}
