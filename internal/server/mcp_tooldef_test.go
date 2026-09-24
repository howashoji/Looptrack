package server

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/howashoji/looptrack/internal/i18n"
)

// MCP のツール定義（ツールの説明・入力項目の説明）は、接続ごとに利用者の言語で出る（mcp_tooldef.go）。

// mcpConnectRaw は、header に書いたものだけを付けて /im/mcp に接続する（initialize まで）。
// mcpAs と違い Accept-Language を補わないので、「Accept-Language が無い接続」を作れる。
func (e *env) mcpConnectRaw(header map[string]string) *mcp.ClientSession {
	e.t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: e.srv.URL + "/im/mcp", HTTPClient: &http.Client{Transport: headerTransport{header}}, MaxRetries: -1, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { cs.Close() })
	return cs
}

// userWithLang は利用者を作り、表示の言語を保存して、その利用者のアクセストークンを返す（lang が空なら設定なし）。
func (e *env) userWithLang(login, lang string) string {
	e.t.Helper()
	u := e.user(login, login+"-password-1", "member")
	if lang != "" {
		setLang(e.t, e, u, lang)
	}
	return e.apiAs(u).token
}

// toolDefs は tools/list の応答から「位置 → 説明」を集める。位置はツール名（ツールの説明）か
// ツール名 + 入力項目の道筋（入れ子の項目・配列の要素を含む）。説明の無い入力項目も空文字で入れる（取りこぼしを見つけるため）。
func toolDefs(t *testing.T, cs *mcp.ClientSession) map[string]string {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.NextCursor != "" {
		t.Fatalf("tools/list が 1 ページに収まっていない（NextCursor=%q）。続きのページも読むように直す", res.NextCursor)
	}
	out := map[string]string{}
	for _, tool := range res.Tools {
		out[tool.Name] = tool.Description
		schema, _ := tool.InputSchema.(map[string]any)
		collectSchemaDescs(tool.Name, schema, out)
	}
	return out
}

func collectSchemaDescs(path string, schema map[string]any, out map[string]string) {
	props, _ := schema["properties"].(map[string]any)
	for name, v := range props {
		p, _ := v.(map[string]any)
		d, _ := p["description"].(string)
		out[path+"."+name] = d
		collectSchemaDescs(path+"."+name, p, out)
	}
	if items, ok := schema["items"].(map[string]any); ok {
		collectSchemaDescs(path+"[]", items, out)
	}
}

// mcpToolDefIDs は対訳表にあるツール定義の ID（ツールの説明と入力項目の説明）。
func mcpToolDefIDs() []string {
	var ids []string
	for _, id := range i18n.IDs(i18n.JA) {
		if strings.HasPrefix(id, "server.mcp.tool.") || strings.HasPrefix(id, "server.mcp.arg.") {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// TestMCPToolDefinitionsPerUserLang は、日本語の利用者 A と英語の利用者 B が同じプロセスに**交互に**繋いだとき、
// それぞれの tools/list が自分の言語のツール定義になることを確かめる。
func TestMCPToolDefinitionsPerUserLang(t *testing.T) {
	e := newEnv(t)
	tokA, tokB := e.userWithLang("td-ja", "ja"), e.userWithLang("td-en", "en")

	// 交互に: A の initialize → B の initialize → A の tools/list → B の tools/list（Accept-Language は付けない）
	a := e.mcpConnectRaw(map[string]string{"Authorization": "Bearer " + tokA})
	b := e.mcpConnectRaw(map[string]string{"Authorization": "Bearer " + tokB})
	defA := toolDefs(t, a)
	defB := toolDefs(t, b)

	// ① ツールの説明と入力項目の説明が、それぞれの言語の対訳表の値と完全に一致する
	for _, c := range []struct{ pos, id string }{
		{"list_issues", "server.mcp.tool.list_issues"},
		{"list_issues.status", "server.mcp.arg.list_issues.status"},
	} {
		if got, want := defA[c.pos], i18n.T(i18n.JA, c.id); got != want {
			t.Errorf("A（ja）の %s = %q, want ja.json の %s = %q", c.pos, got, c.id, want)
		}
		if got, want := defB[c.pos], i18n.T(i18n.EN, c.id); got != want {
			t.Errorf("B（en）の %s = %q, want en.json の %s = %q", c.pos, got, c.id, want)
		}
	}

	// サーバの表示名（serverInfo.title）も接続の言語
	if got, want := a.InitializeResult().ServerInfo.Title, i18n.T(i18n.JA, "server.mcp.title"); got != want {
		t.Errorf("A の serverInfo.title = %q, want %q", got, want)
	}
	if got, want := b.InitializeResult().ServerInfo.Title, i18n.T(i18n.EN, "server.mcp.title"); got != want {
		t.Errorf("B の serverInfo.title = %q, want %q", got, want)
	}

	// ② 全件: 位置の集合が同じで、どの位置も説明を持ち、A と B で同じ文字列になる位置が 0 件、B には日本語が 0 文字
	if len(defA) != len(defB) {
		t.Fatalf("A と B で説明の位置の数が違う: %d / %d", len(defA), len(defB))
	}
	same := 0
	for pos, da := range defA {
		db, ok := defB[pos]
		if !ok {
			t.Errorf("B に %s が無い", pos)
			continue
		}
		if da == "" || db == "" {
			t.Errorf("%s に説明が無い（ja %q / en %q）。入力項目の jsonschema タグに対訳表の ID を書く", pos, da, db)
		}
		if strings.HasPrefix(da, "server.mcp.") || strings.HasPrefix(db, "server.mcp.") {
			t.Errorf("%s の説明が ID のまま（ja %q / en %q）。対訳表に無い", pos, da, db)
		}
		if da == db {
			same++
			t.Errorf("%s の説明が A と B で同じ: %q", pos, da)
		}
		if hasJapanese(db) {
			t.Errorf("B（en）の %s に日本語が混ざっている: %q", pos, db)
		}
	}

	// ③ 取りこぼしが無い: 対訳表のツール定義の ID（ツール + 入力項目）がすべて、どこかの位置で
	// ja・en の両方の値として出ている。件数は対訳表の ID の数と一致する（実装の登録の実数）。
	covered := map[string]bool{}
	for _, id := range mcpToolDefIDs() {
		ja, en := i18n.T(i18n.JA, id), i18n.T(i18n.EN, id)
		for pos, da := range defA {
			if da == ja && defB[pos] == en {
				covered[id] = true
				break
			}
		}
		if !covered[id] {
			t.Errorf("%s が tools/list のどこにも出ていない（使われていないか、登録の言語が違う）", id)
		}
	}
	tools, args := 0, 0
	for id := range covered {
		if strings.HasPrefix(id, "server.mcp.tool.") {
			tools++
		} else {
			args++
		}
	}
	t.Logf("tools/list の説明: 位置 %d か所・ID %d 件（ツール %d + 入力項目 %d）・A と B で同じもの %d 件", len(defA), len(covered), tools, args, same)
	if len(covered) != len(mcpToolDefIDs()) {
		t.Errorf("tools/list に出た ID が %d 件、対訳表のツール定義の ID は %d 件", len(covered), len(mcpToolDefIDs()))
	}
	// 位置の側からも: どの位置の説明も、対訳表のどれかの ID の ja・en の組になっている
	for pos, da := range defA {
		found := false
		for _, id := range mcpToolDefIDs() {
			if i18n.T(i18n.JA, id) == da && i18n.T(i18n.EN, id) == defB[pos] {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s の説明が対訳表のどの ID の組とも一致しない（ja %q / en %q）", pos, da, defB[pos])
		}
	}
}

// TestMCPToolDefinitionsUserLangOverAcceptLanguage は、users.lang が Accept-Language より強いことを確かめる。
func TestMCPToolDefinitionsUserLangOverAcceptLanguage(t *testing.T) {
	e := newEnv(t)
	tok := e.userWithLang("td-pref", "ja")
	wantJA := i18n.T(i18n.JA, "server.mcp.tool.list_issues")
	for _, c := range []struct {
		name   string
		header map[string]string
	}{
		{"Accept-Language なし", map[string]string{"Authorization": "Bearer " + tok}},
		{"Accept-Language: en", map[string]string{"Authorization": "Bearer " + tok, "Accept-Language": "en"}},
	} {
		defs := toolDefs(t, e.mcpConnectRaw(c.header))
		if got := defs["list_issues"]; got != wantJA {
			t.Errorf("%s: users.lang=ja の接続の list_issues = %q, want 日本語 %q", c.name, got, wantJA)
		}
	}

	// 対照: 設定なしの利用者は Accept-Language で決まる（en を送れば英語）。これが日本語なら、上の検査は言語を見ていない
	tokUnset := e.userWithLang("td-unset", "")
	defs := toolDefs(t, e.mcpConnectRaw(map[string]string{"Authorization": "Bearer " + tokUnset, "Accept-Language": "en"}))
	if got, want := defs["list_issues"], i18n.T(i18n.EN, "server.mcp.tool.list_issues"); got != want {
		t.Errorf("対照（設定なし・Accept-Language: en）の list_issues = %q, want 英語 %q", got, want)
	}
}

// TestMCPToolDefinitionsBuiltOncePerLang は、tools/list を何度呼んでもツール定義を組み直さない
// （言語ごとに起動時の 1 回だけ）ことを数える。
func TestMCPToolDefinitionsBuiltOncePerLang(t *testing.T) {
	e := newEnv(t)
	before := e.s.mcpServersBuilt.Load()
	if before != 2 {
		t.Fatalf("起動時に組んだ MCP サーバが %d 個（ja・en の 2 個のはず）", before)
	}
	tok := e.userWithLang("td-once", "ja")
	for i := 0; i < 2; i++ {
		defs := toolDefs(t, e.mcpConnectRaw(map[string]string{"Authorization": "Bearer " + tok}))
		if defs["list_issues"] != i18n.T(i18n.JA, "server.mcp.tool.list_issues") {
			t.Fatalf("%d 回目の tools/list が日本語でない: %q", i+1, defs["list_issues"])
		}
	}
	if after := e.s.mcpServersBuilt.Load(); after != before {
		t.Errorf("tools/list を 2 回呼んだら MCP サーバを組み直した: %d → %d", before, after)
	}
}
