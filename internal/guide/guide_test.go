package guide

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
)

// verify の規則が JSON のままでなく文で出ること（guide がこの規則を知らずに JSON のまま出していた）。
func TestDescribeRulesVerify(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want []string
	}{
		{`{"verify":{"require_on_close":true}}`, []string{"Done にするとき", "looptrack issue verify <ID>", "--override"}},
		{`{"verify":{"require_on_close":true,"statuses":["Done","Canceled"]}}`, []string{"Done / Canceled にするとき"}},
		{`{"verify":{"require_on_close":false}}`, []string{"設定: "}},
	} {
		rules := DescribeRules(i18n.JA, json.RawMessage(tc.raw))
		if len(rules) != 1 || rules[0].Name != "verify" {
			t.Fatalf("%s: 規則が 1 件でない: %+v", tc.raw, rules)
		}
		for _, w := range tc.want {
			if !strings.Contains(rules[0].Description, w) {
				t.Errorf("%s: %q を含まない: %s", tc.raw, w, rules[0].Description)
			}
		}
	}
}

// acceptance の規則が JSON のままでなく文で出ること。deploy/rules/example.json の全種が説明を持つことも
// ここで見る（この検査は DB を要らないので、規則を足したときに必ず回る）。
func TestDescribeRulesAcceptance(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want []string
	}{
		{`{"acceptance":{"require_on_close":true}}`, []string{"「## 受け入れ条件」節", "Done にするとき", "looptrack issue edit <ID>", "--override"}},
		{`{"acceptance":{"require_on_close":true,"statuses":["Done","Canceled"]}}`, []string{"Done / Canceled にするとき"}},
		{`{"acceptance":{"require_on_close":false}}`, []string{"設定: "}},
	} {
		rules := DescribeRules(i18n.JA, json.RawMessage(tc.raw))
		if len(rules) != 1 || rules[0].Name != "acceptance" {
			t.Fatalf("%s: 規則が 1 件でない: %+v", tc.raw, rules)
		}
		for _, w := range tc.want {
			if !strings.Contains(rules[0].Description, w) {
				t.Errorf("%s: %q を含まない: %s", tc.raw, w, rules[0].Description)
			}
		}
	}
}

// 配布する例のルールは全種が説明文を持つ（生の JSON のまま利用者に出さない）。
// 規則を足して switch を更新し忘れると、ここが落ちる。
func TestDescribeRulesExampleAllDescribed(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "rules", "example.json"))
	if err != nil {
		t.Fatal(err)
	}
	rules := DescribeRules(i18n.JA, json.RawMessage(raw))
	if len(rules) == 0 {
		t.Fatal("example.json の規則が読めない")
	}
	for _, r := range rules {
		if strings.HasPrefix(r.Description, "設定: ") {
			t.Errorf("%s の説明が生の JSON のまま: %s", r.Name, r.Description)
		}
	}
}

// usage.send_prompts が JSON のままでなく文で出ること。
func TestDescribeRulesSendPrompts(t *testing.T) {
	rules := DescribeRules(i18n.JA, json.RawMessage(`{"usage":{"send_prompts":true}}`))
	if len(rules) != 1 || !strings.Contains(rules[0].Description, "先頭 44 文字") || !strings.Contains(rules[0].Description, "LOOPTRACK_USAGE_SEND_PROMPTS=0") ||
		strings.Contains(rules[0].Description, "設定: ") {
		t.Errorf("send_prompts: %+v", rules)
	}
	rules = DescribeRules(i18n.JA, json.RawMessage(`{"usage":{"send_prompts":false}}`))
	if len(rules) != 1 || strings.Contains(rules[0].Description, "先頭 44 文字") {
		t.Errorf("send_prompts=false: %+v", rules)
	}
}

// ループの 1 周に、下位がすべて完了した要件の検証と close の手順がある。
func TestCommonRequirementClose(t *testing.T) {
	common := Common(i18n.JA)
	i, j := strings.Index(common, "### 作業の進め方（ループの 1 周）"), strings.Index(common, "### 起票・編集")
	if i < 0 || j < i {
		t.Fatal("ループの 1 周の節が見つからない")
	}
	loop := common[i:j]
	for _, w := range []string{"下位がすべて完了", "受け入れ条件を 1 つずつ", "looptrack issue close <要件ID>", "looptrack issue matrix", "summary"} {
		if !strings.Contains(loop, w) {
			t.Errorf("ループの 1 周に %q が無い:\n%s", w, loop)
		}
	}
}

// 英語版の共通規則が**実行ファイルに埋め込まれている**ことを、埋め込んだ変数の中身を歩いて確かめる。
// ファイルが在ること（ls・git ls-files）では足りない。//go:embed の指定から en/common.md を外すと
// ここが落ちる（「en/common.md が埋め込まれていません」）。
func TestCommonEmbedsBothLanguages(t *testing.T) {
	var got []string
	if err := fs.WalkDir(commonFS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			got = append(got, p)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Logf("commonFS の中身: %v", got)
	for _, want := range []string{"common.md", "en/common.md"} {
		if !slices.Contains(got, want) {
			t.Errorf("%s が埋め込まれていません（//go:embed の指定を確かめてください）。中身: %v", want, got)
		}
	}
	for _, c := range []struct {
		lang i18n.Lang
		head string
	}{
		{i18n.JA, "### このシステムで何が整うか"},
		{i18n.EN, "### What this system puts in place"},
	} {
		body := Common(c.lang)
		t.Logf("Common(%s): len=%d head=%q", c.lang, len(body), firstLine(body))
		if len(body) == 0 {
			t.Fatalf("Common(%s) が空です", c.lang)
		}
		if !strings.HasPrefix(body, c.head) {
			t.Errorf("Common(%s) の先頭が %q ではありません: %q", c.lang, c.head, firstLine(body))
		}
	}
	if Common(i18n.JA) == Common(i18n.EN) {
		t.Error("Common(JA) と Common(EN) が同じです（英語版が埋め込まれていないか、日本語に戻っています）")
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// Compose が lang で出し分ける（配線を外して lang を無視すると落ちる）。
func TestComposeLanguage(t *testing.T) {
	in := Input{Slug: "ex", Name: "ex", Prefix: "EX", Width: 4, Role: "editor",
		Rules:  json.RawMessage(`{"verify":{"require_on_close":true}}`),
		Agents: []AgentLoop{{Agent: "claude-code", Label: "Claude Code", Loop: "installed", Version: "1"}}}
	ja, en := Compose(i18n.JA, in).Markdown, Compose(i18n.EN, in).Markdown
	if ja == en {
		t.Fatal("日本語と英語の Markdown が同じです（lang が使われていません）")
	}
	for _, w := range []string{"# イシュー管理の使い方 — ex（ex）", "ID は `EX-0001` 形式", "## 1. 共通規則",
		"## 2. このプロジェクトのルール（サーバが強制する）", "## 3. このプロジェクトの運用文書",
		"### このシステムで何が整うか", "「## 検証コマンド」節を持つイシューを Done にするとき", "| Claude Code | あり（1） |"} {
		if !strings.Contains(ja, w) {
			t.Errorf("日本語の guide に %q が無い:\n%s", w, ja)
		}
	}
	for _, w := range []string{"# How to use issue management — ex (ex)", "IDs take the form `EX-0001`", "## 1. Common rules",
		"## 2. This project's rules (enforced by the server)", "## 3. This project's operating document",
		"### What this system puts in place", `Setting an issue that has a "## Verify commands" section to Done`,
		"| Claude Code | yes (1) |"} {
		if !strings.Contains(en, w) {
			t.Errorf("英語の guide に %q が無い:\n%s", w, en)
		}
	}
	// 英語を頼んだのに日本語の見出しが混ざらない（common.md が日本語のまま返っていないこと）
	for _, ng := range []string{"共通規則", "このプロジェクトのルール", "### 前提"} {
		if strings.Contains(en, ng) {
			t.Errorf("英語の guide に日本語 %q が混ざっています", ng)
		}
	}
}
