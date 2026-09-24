package i18n

import (
	"sort"
	"strings"
	"testing"
)

// MCP のツール定義（ツールの説明・入力項目の説明・サーバの表示名）は、接続の言語で出す
// （internal/server/mcp_tooldef.go）。AI はこの文面を読んでツールの使い方を決めるので、片方の言語だけ
// 抜けていると、その言語の接続では ID か日本語の正本がそのまま AI に渡る（i18n.T の戻し先）。
// ここでは、コードが使う MCP のツール定義の ID がすべて ja.json と en.json の両方にあり、どちらも空でないことを見る。

// mcpToolDefPrefixes はツール定義の ID の接頭辞。
var mcpToolDefPrefixes = []string{"server.mcp.tool.", "server.mcp.arg.", "server.mcp.title"}

func isMCPToolDefID(id string) bool {
	for _, p := range mcpToolDefPrefixes {
		if strings.HasPrefix(id, p) {
			return true
		}
	}
	return false
}

// mcpToolDefProblems は、ids の各 ID が JA・EN の両方の表に空でない文面として無ければ、その食い違いを返す。
func mcpToolDefProblems(ids []string, cats map[Lang]map[string]string) []string {
	var out []string
	for _, id := range ids {
		for _, lang := range []Lang{JA, EN} {
			if v, ok := cats[lang][id]; !ok {
				out = append(out, string(lang)+".json に "+id+" がありません")
			} else if strings.TrimSpace(v) == "" {
				out = append(out, string(lang)+".json の "+id+" が空です")
			}
		}
	}
	return out
}

func TestMCPToolDefinitionsInBothCatalogs(t *testing.T) {
	var ids []string
	tools, args := 0, 0
	for id := range collectUsedIDs(t) {
		if !isMCPToolDefID(id) {
			continue
		}
		ids = append(ids, id)
		switch {
		case strings.HasPrefix(id, "server.mcp.tool."):
			tools++
		case strings.HasPrefix(id, "server.mcp.arg."):
			args++
		}
	}
	sort.Strings(ids)
	// 前提: コードからツール定義の ID を集められていること（0 件なら集め方が壊れていて、下の検査は素通りする）
	if tools == 0 || args == 0 {
		t.Fatalf("前提が崩れています: コードから集めた MCP のツール定義の ID がツール %d 件・入力項目 %d 件（jsonschema タグか i18n.T の集め方が壊れている）", tools, args)
	}
	t.Logf("MCP のツール定義の ID: ツール %d 件・入力項目 %d 件・計 %d 件", tools, args, len(ids))
	for _, p := range mcpToolDefProblems(ids, catalogs) {
		t.Error(p)
	}

	// 対照: 片方の表から 1 件消す・空にすると、同じ検査が食い違いを返すこと（検査が死んでいないことの確認）
	for _, tc := range []struct {
		name string
		edit func(cats map[Lang]map[string]string)
	}{
		{"en から消す", func(c map[Lang]map[string]string) { delete(c[EN], ids[0]) }},
		{"ja から消す", func(c map[Lang]map[string]string) { delete(c[JA], ids[0]) }},
		{"en を空にする", func(c map[Lang]map[string]string) { c[EN][ids[0]] = "" }},
	} {
		cats := map[Lang]map[string]string{}
		for lang, m := range catalogs {
			cats[lang] = map[string]string{}
			for k, v := range m {
				cats[lang][k] = v
			}
		}
		tc.edit(cats)
		if got := mcpToolDefProblems(ids, cats); len(got) != 1 {
			t.Errorf("対照 %s: 食い違いが %d 件（1 件のはず）: %v", tc.name, len(got), got)
		}
	}
}
