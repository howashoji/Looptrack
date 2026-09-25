package server

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/howashoji/looptrack/internal/i18n"
)

// prompt（loop・review・setup）の本文は日本語の正本と英語版を定数の組で持つ。
// 片方だけ手順を足すと、言語によって AI の従う手順がずれる。番号つきの項目の並びが同じことを確かめる。
func TestPromptTextsHaveEnglishPairs(t *testing.T) {
	item := regexp.MustCompile(`(?m)^(\d+'?)\. `)
	numbers := func(s string) string {
		var out []string
		for _, m := range item.FindAllStringSubmatch(s, -1) {
			out = append(out, m[1])
		}
		return strings.Join(out, ",")
	}
	for _, c := range []struct{ name, ja, en string }{
		{"loop", loopPromptText, loopPromptTextEN},
		{"loop(iterate)", loopIteratePromptText, loopIteratePromptTextEN},
		{"review", reviewPromptText, reviewPromptTextEN},
		{"setup", setupPromptText, setupPromptTextEN},
	} {
		if numbers(c.ja) == "" {
			t.Fatalf("%s: 日本語の本文から番号つきの項目を 1 つも拾えません（検査の前提が崩れています）", c.name)
		}
		if numbers(c.ja) != numbers(c.en) {
			t.Errorf("%s: 項目の並びが日英で違います: ja=%s en=%s", c.name, numbers(c.ja), numbers(c.en))
		}
		if strings.Count(c.ja, "%s") != 1 || strings.Count(c.en, "%s") != 1 {
			t.Errorf("%s: プロジェクトの指定を入れる %%s は日英とも 1 つだけ（ja=%d en=%d）", c.name, strings.Count(c.ja, "%s"), strings.Count(c.en, "%s"))
		}
		if promptText(i18n.JA, c.ja, c.en) != c.ja || promptText(i18n.EN, c.ja, c.en) != c.en {
			t.Errorf("%s: promptText が言語で選んでいません", c.name)
		}
	}
}

// 英語の接続には prompt の題・説明・本文が英語で出る（日本語の接続は TestReviewPromptViaMCP が見る）。
func TestPromptsInEnglishViaMCP(t *testing.T) {
	e, _, ed := newAPIEnv(t)
	m := e.mcpAs(ed.token, map[string]string{"X-Looptrack-Project": "req", "Accept-Language": "en"})
	ctx := context.Background()

	list, err := m.cs.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	titles := map[string]string{}
	for _, p := range list.Prompts {
		titles[p.Name] = p.Title
		if len(p.Arguments) != 1 || p.Arguments[0].Title != "Project" {
			t.Errorf("prompt %s の引数の題が英語でない: %+v", p.Name, p.Arguments)
		}
	}
	if titles["review"] != i18n.T(i18n.EN, "server.mcp.prompt.review.title") || titles["loop"] != i18n.T(i18n.EN, "server.mcp.prompt.loop.title") ||
		titles["setup"] != i18n.T(i18n.EN, "server.mcp.prompt.setup.title") {
		t.Errorf("prompts/list の題が英語でない: %v", titles)
	}
	// 対照: 日本語の題とは違う（英語の対訳が日本語と同じ文面だと、上の比較は何も確かめない）
	if titles["review"] == i18n.T(i18n.JA, "server.mcp.prompt.review.title") {
		t.Fatalf("英語と日本語の題が同じ（検査の前提が崩れています）: %q", titles["review"])
	}
	res, err := m.cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: "review", Arguments: map[string]string{"project": "web"}})
	if err != nil {
		t.Fatal(err)
	}
	if text := res.Messages[0].Content.(*mcp.TextContent).Text; text != fmt.Sprintf(reviewPromptTextEN, projectSuffix(i18n.EN, "web")) {
		t.Errorf("英語の review prompt:\n%s", text)
	}
}
