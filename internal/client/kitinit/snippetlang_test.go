package kitinit

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
)

// CLAUDE.md・AGENTS.md の案内節は init の言語で入る（日本語が正本、英語版は …EN の定数）。

// 英語で導入すると英語の節と英語の印が入り、日本語で導入すると日本語の節と日本語の印が入る
// （対照として両方の言語を同じテストで回し、言語で結果が変わることを確かめる）。
func TestSnippetPlacedInInstallLanguage(t *testing.T) {
	cases := []struct {
		agent, file string
		ja, en      string // 本文（{url}・{slug} を埋める前）
	}{
		{"claude-code", "CLAUDE.md", claudeSnippet, claudeSnippetEN},
		{"codex", "AGENTS.md", agentsSnippet, agentsSnippetEN},
		{"copilot", "AGENTS.md", agentsCopilotSnippet, agentsCopilotSnippetEN},
		{"codex,copilot", "AGENTS.md", agentsSnippet + agentsCopilotAddendum, agentsSnippetEN + agentsCopilotAddendumEN},
	}
	for _, c := range cases {
		for _, lang := range []string{"en", "ja"} {
			t.Run(c.agent+"/"+lang, func(t *testing.T) {
				stubs(t, true)
				root := newRoot(t)
				r := runInitIn(t, root, "", map[string]string{"LOOPTRACK_LANG": lang}, "--url", fakeURL, "--project", "demo", "--agent", c.agent)
				if r.code != 0 {
					t.Fatalf("init（%s）が失敗: %d\n%s%s", lang, r.code, r.stdout, r.stderr)
				}
				got := read(t, filepath.Join(root, "ws", c.file))
				want, wantMark, other, otherMark := c.ja, blockBegin, c.en, blockBeginEN
				if lang == "en" {
					want, wantMark, other, otherMark = c.en, blockBeginEN, c.ja, blockBegin
				}
				if block := wantMark + "\n" + fill(want, "demo", fakeURL) + blockEnd + "\n"; !strings.Contains(got, block) {
					t.Errorf("%s に %s の節が入っていない:\n%s", c.file, lang, got)
				}
				if strings.Contains(got, otherMark) || strings.Contains(got, firstLine(other)) {
					t.Errorf("%s に別の言語の印か見出しが入っている:\n%s", c.file, got)
				}
			})
		}
	}
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}

// 言語を変えて init を打ち直しても、節は 1 つのまま差し替わる（前の言語の印を同じ節と見る）。
// 節の前後に利用者が書いた本文は残る。
func TestSnippetLanguageSwitchKeepsOneSection(t *testing.T) {
	for _, c := range []struct{ agent, file string }{{"claude-code", "CLAUDE.md"}, {"codex,copilot", "AGENTS.md"}} {
		t.Run(c.agent, func(t *testing.T) {
			stubs(t, true)
			root := newRoot(t)
			p := filepath.Join(root, "ws", c.file)
			write(t, p, "# 利用者の見出し\n\n利用者が書いた本文。\n")
			for i, lang := range []string{"ja", "en", "ja", "en"} {
				r := runInitIn(t, root, "", map[string]string{"LOOPTRACK_LANG": lang}, "--url", fakeURL, "--project", "demo", "--agent", c.agent)
				if r.code != 0 {
					t.Fatalf("%d 回目の init（%s）が失敗: %d\n%s%s", i+1, lang, r.code, r.stdout, r.stderr)
				}
				got := read(t, p)
				mark := blockBegin
				if lang == "en" {
					mark = blockBeginEN
				}
				begins := strings.Count(got, blockBegin) + strings.Count(got, blockBeginEN)
				heads := len(regexp.MustCompile(`(?m)^## (課題管理（イシュー管理サーバ）|Issue tracking \(issue server\))$`).FindAllString(got, -1))
				if begins != 1 || strings.Count(got, blockEnd) != 1 || heads != 1 || !strings.Contains(got, mark) {
					t.Errorf("%d 回目（%s）: 節が 1 つになっていない（始まりの印 %d・終わりの印 %d・見出し %d）:\n%s",
						i+1, lang, begins, strings.Count(got, blockEnd), heads, got)
				}
				if !strings.HasPrefix(got, "# 利用者の見出し\n\n利用者が書いた本文。\n\n") {
					t.Errorf("%d 回目（%s）: 利用者の本文が残っていない:\n%s", i+1, lang, got)
				}
			}
		})
	}
}

// 英語版は日本語版と項目をそろえる: 見出し・箇条（入れ子を含む）の数、ASCII だけのコード片
// （コマンド・パス・ツール名）の出現数、{url}・{slug} の出現数が同じであること。
// 対照: 片方の箇条を 1 つ消すと食い違いとして見つかること（比べる仕組みが死んでいないこと）も確かめる。
func TestSnippetENPairsJA(t *testing.T) {
	pairs := []struct {
		name   string
		ja, en string
	}{
		{"claudeSnippet", claudeSnippet, claudeSnippetEN},
		{"agentsSnippet", agentsSnippet, agentsSnippetEN},
		{"agentsCopilotSnippet", agentsCopilotSnippet, agentsCopilotSnippetEN},
		{"agentsCopilotAddendum", agentsCopilotAddendum, agentsCopilotAddendumEN},
	}
	for _, p := range pairs {
		if d := snippetShapeDiff(p.ja, p.en); d != "" {
			t.Errorf("%s の英語版が日本語版と食い違う: %s", p.name, d)
		}
		if hasJapanese(p.en) {
			t.Errorf("%sEN に日本語が残っている", p.name)
		}
	}
	broken := strings.Replace(claudeSnippetEN, "- Browse:", "Browse:", 1)
	if snippetShapeDiff(claudeSnippet, broken) == "" {
		t.Error("前提が崩れています。箇条を 1 つ消した英語版との食い違いを見つけられません")
	}
	for _, l := range []i18n.Lang{i18n.JA, i18n.EN} {
		if !strings.HasPrefix(codexConfigNoteFor(l), "# looptrack issue init") && !strings.HasPrefix(codexConfigNoteFor(l), "# Added by looptrack issue init") {
			t.Errorf("%s の .codex/config.toml の注釈が TOML の注釈になっていない: %q", l, codexConfigNoteFor(l))
		}
	}
	if hasJapanese(codexConfigNoteFor(i18n.EN)) || hasJapanese(blockBeginFor(i18n.EN)) {
		t.Error("英語の注釈か印に日本語が残っている")
	}
}

var codeSpan = regexp.MustCompile("`[^`\n]*`")

func snippetShapeDiff(ja, en string) string {
	count := func(s string, re *regexp.Regexp) int { return len(re.FindAllString(s, -1)) }
	for _, k := range []struct {
		what string
		re   *regexp.Regexp
	}{
		{"見出し", regexp.MustCompile(`(?m)^#+ `)},
		{"箇条", regexp.MustCompile(`(?m)^- `)},
		{"入れ子の箇条", regexp.MustCompile(`(?m)^  - `)},
		{"{url}", regexp.MustCompile(`\{url\}`)},
		{"{slug}", regexp.MustCompile(`\{slug\}`)},
		{"太字", regexp.MustCompile(`\*\*[^*]+\*\*`)},
	} {
		if a, b := count(ja, k.re), count(en, k.re); a != b {
			return k.what + " の数が違う（日本語 " + strconv.Itoa(a) + "・英語 " + strconv.Itoa(b) + "）"
		}
	}
	spans := map[string]int{}
	for _, s := range codeSpan.FindAllString(ja, -1) {
		if !hasJapanese(s) {
			spans[s]++
		}
	}
	for _, s := range codeSpan.FindAllString(en, -1) {
		spans[s]--
	}
	for s, n := range spans {
		if n > 0 {
			return "コード片 " + s + " が英語版に足りない"
		}
	}
	return ""
}

func hasJapanese(s string) bool {
	for _, r := range s {
		if r >= 0x3000 && r <= 0x9fff || r >= 0xff00 && r <= 0xffef {
			return true
		}
	}
	return false
}
