package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/guide"
	"github.com/howashoji/looptrack/internal/i18n"
)

// 担当者の規則（DESIGN.md §9-2「AI への案内」）が guide（common.md）と MCP の instructions に入っていて、
// 設計の文面と一字一句同じ。guide ツール・initialize の instructions からも読める。
func TestAssigneeGuideMatchesDesign(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "server", "DESIGN.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	i := strings.Index(s, "#### AI への案内（guide・MCP instructions）")
	if i < 0 {
		t.Fatal("DESIGN.md §9-2 に「AI への案内」の節が無い")
	}
	s = s[i:]
	if j := strings.Index(s[1:], "\n#### "); j >= 0 {
		s = s[:j+1]
	}
	q := quoteBlocks(s)
	if len(q) != 2 {
		t.Fatalf("§9-2「AI への案内」の引用が 2 つでない: %q", q)
	}
	if !strings.Contains(guide.Common(i18n.JA), "\n### 担当者（重複作業の防止）\n\n"+q[0]+"\n") {
		t.Errorf("common.md に担当者の段落が無い（DESIGN §9-2 と違う）:\n%s", q[0])
	}
	if !strings.Contains(mcpInstructions(i18n.JA), "\n"+q[1]+"\n") {
		t.Errorf("instructions に担当者の 1 行が無い（DESIGN §9-2 と違う）:\n%s", q[1])
	}
	for _, want := range []string{"In Progress にしない", "--override \"理由\"", "override_reason", "担当を替えずに", "自分が担当か未設定"} {
		if !strings.Contains(q[0], want) {
			t.Errorf("guide の段落に %q が無い", want)
		}
	}

	// 経路: guide ツール（REST・CLI と同じ Markdown）と MCP の initialize の instructions
	e, _, ed := newAPIEnv(t)
	g := e.mcpAs(ed.token, map[string]string{"X-Looptrack-Project": "req"})
	if text, _ := g.call("guide", map[string]any{}, false); !strings.Contains(text, q[0]) {
		t.Error("guide ツールの出力に担当者の段落が無い")
	}
	m := e.mcpAsClient(ed.token, map[string]string{"X-Looptrack-Project": "req"}, "claude-code", "1", "2025-06-18")
	if ins := m.cs.InitializeResult().Instructions; !strings.Contains(ins, q[1]) {
		t.Error("initialize の instructions に担当者の 1 行が無い")
	}
}
