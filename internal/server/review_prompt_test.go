package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/howashoji/looptrack/internal/guide"
	"github.com/howashoji/looptrack/internal/i18n"
)

// 人の判断待ちと外からの反応の文面（設計 DESIGN.md §9-3-5）が
// guide（common.md）・MCP の instructions・prompt loop / review に入っていて、設計の全文と一致する。

// designSection58 は DESIGN.md の §9-3 の「5. 人の判断待ちの周」の節を返す。
func designSection58(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "server", "DESIGN.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	i := strings.Index(s, "#### 5. 人の判断待ちの周")
	if i < 0 {
		t.Fatal("DESIGN.md に §9-3-5 の見出しが無い")
	}
	s = s[i:]
	if j := strings.Index(s, "\n#### 6. "); j >= 0 {
		s = s[:j]
	}
	return s
}

// quoteBlocks は節の中の引用（> で始まる行の連なり）を、> を外した本文にして順に返す（空の > 行は段落の区切り）。
func quoteBlocks(sec string) []string {
	var out []string
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.Join(cur, "\n"))
			cur = nil
		}
	}
	for _, line := range strings.Split(sec, "\n") {
		switch {
		case line == ">":
			flush()
		case strings.HasPrefix(line, "> "):
			cur = append(cur, strings.TrimPrefix(line, "> "))
		default:
			flush()
		}
	}
	flush()
	return out
}

func TestReviewTextsMatchDesign(t *testing.T) {
	sec := designSection58(t)

	// prompt review の全文（コードブロック）が reviewPromptText と一致する
	const head = "prompt **`review`** の全文"
	i := strings.Index(sec, head)
	if i < 0 {
		t.Fatal("DESIGN.md に prompt review の全文が無い")
	}
	rest := sec[i:]
	a := strings.Index(rest, "```\n")
	b := strings.Index(rest[a+4:], "\n```")
	if a < 0 || b < 0 {
		t.Fatal("prompt review のコードブロックが見つからない")
	}
	if want := rest[a+4 : a+4+b]; want != reviewPromptText {
		t.Errorf("prompt review が DESIGN.md §9-3-5 と違う:\n--- DESIGN\n%s\n--- code\n%s", want, reviewPromptText)
	}

	q := quoteBlocks(sec)
	// 1〜2: common.md の 2 段落、3: instructions の 1 行、4: loop の 0'
	if len(q) < 4 {
		t.Fatalf("引用が %d 個しかない: %q", len(q), q)
	}
	ja := guide.Common(i18n.JA)
	for _, p := range q[:2] {
		if !strings.Contains(ja, p+"\n") {
			t.Errorf("common.md に DESIGN の段落が無い:\n%s", p)
		}
	}
	if !strings.Contains(mcpInstructions(i18n.JA), "\n"+q[2]+"\n") {
		t.Errorf("instructions に DESIGN の 1 行が無い:\n%s", q[2])
	}
	if !strings.Contains(loopPromptText, "\n"+q[3]+"\n") {
		t.Errorf("prompt loop に DESIGN の 0' が無い:\n%s", q[3])
	}
}

func TestReviewPromptViaMCP(t *testing.T) {
	e, _, ed := newAPIEnv(t)
	m := e.mcpAsClient(ed.token, map[string]string{"X-Looptrack-Project": "req"}, "claude-code", "1", "2025-06-18")
	ctx := context.Background()

	list, err := m.cs.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var review *mcp.Prompt
	for _, p := range list.Prompts {
		if p.Name == "review" {
			review = p
		}
	}
	if review == nil || review.Title != "人の判断待ちと外からの反応を利用者に持ちかける" || len(review.Arguments) != 1 || review.Arguments[0].Name != "project" {
		t.Fatalf("prompts/list に review が無い・形が違う: %+v", review)
	}
	res, err := m.cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: "review", Arguments: map[string]string{"project": "web"}})
	if err != nil {
		t.Fatal(err)
	}
	if text := res.Messages[0].Content.(*mcp.TextContent).Text; text != fmt.Sprintf(reviewPromptText, projectSuffix("web")) {
		t.Errorf("review prompt: %s", text)
	}

	// instructions と loop に人の判断待ち・フィードバックの指示
	ins := m.cs.InitializeResult().Instructions
	for _, want := range []string{"In Review（人の判断待ち）", "「フィードバック:」", "「判断:」「差し戻し:」", "prompt「review」"} {
		if !strings.Contains(ins, want) {
			t.Errorf("instructions に %q が無い", want)
		}
	}
	res, _ = m.cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: "loop"})
	loop := res.Messages[0].Content.(*mcp.TextContent).Text
	for _, want := range []string{"0'. project_summary の「人の判断待ち」「外からの反応」", "prompt「review」", "先頭「フィードバック: 」で add_comment",
		"Done にせず In Review にし、comment に判断してほしい点を書く"} {
		if !strings.Contains(loop, want) {
			t.Errorf("loop に %q が無い:\n%s", want, loop)
		}
	}

	// guide（MCP の guide ツール。REST・CLI と同じ Markdown）に 2 段落が入っている
	g := e.mcpAs(ed.token, map[string]string{"X-Looptrack-Project": "req"})
	text, _ := g.call("guide", map[string]any{}, false)
	for _, want := range []string{"**人の判断待ち**: ", "**外からの反応**: ", "先頭に `判断:`", "`差し戻し:`", "**先頭 `フィードバック:`**"} {
		if !strings.Contains(text, want) {
			t.Errorf("guide に %q が無い", want)
		}
	}
}

// guide（common.md）冒頭の 3 層の箇条書き（設計 DESIGN.md §9-3-8）が設計の文面と一字一句同じで、
// 「整備中」の文言が残っていない。
func TestMeritTextMatchesDesign(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "server", "DESIGN.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	i := strings.Index(s, "#### 8. 横断の文書")
	if i < 0 {
		t.Fatal("DESIGN.md に §9-3-8 の見出しが無い")
	}
	s = s[i:]
	if j := strings.Index(s, "\n### 9. "); j >= 0 {
		s = s[:j]
	}
	q := quoteBlocks(s)
	if len(q) != 1 || strings.Count(q[0], "\n") != 2 {
		t.Fatalf("§5-8-8 のメリットの文面（3 行の引用）が見つからない: %q", q)
	}
	if !strings.Contains(guide.Common(i18n.JA), "\n"+q[0]+"\n") {
		t.Errorf("common.md の 3 層の箇条書きが DESIGN §9-3-8 と違う:\n%s", q[0])
	}
	if strings.Contains(guide.Common(i18n.JA), "整備中") {
		t.Error("common.md に「整備中」が残っている")
	}
}
