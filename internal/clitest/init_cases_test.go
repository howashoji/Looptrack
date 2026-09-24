package clitest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// init のケース。導入先は一時ディレクトリの作業ディレクトリ（ws）だけ（このリポジトリや実プロジェクトには init しない）。
// 配布物は偽 API の /dist（トークンあり）と /setup/<ticket>（--dist の期限つき URL。トークンなし）から取る小さな偽物。

// distFiles は配布物の偽物（kit/core の skill）。
var distFiles = map[string]string{
	"kit/core/skills/issue/SKILL.md": "---\nname: issue\ndescription: 偽の skill\n---\n\n# issue\n",
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// distListing は GET /dist の一覧（{"files":[{"name","sha256"}]}）。broken の名前だけハッシュを違える。
func distListing(broken string) string {
	names := make([]string, 0, len(distFiles))
	for n := range distFiles {
		names = append(names, n)
	}
	sort.Strings(names)
	var items []string
	for _, n := range names {
		sum := sha(distFiles[n])
		if n == broken {
			sum = sha("別物")
		}
		items = append(items, fmt.Sprintf(`{"name":%q,"sha256":%q,"size":%d}`, n, sum, len(distFiles[n])))
	}
	return `{"files":[` + strings.Join(items, ",") + `]}`
}

// distRoutes は prefix（/api/v1/dist か /setup/<ticket>）の下に一覧と各ファイルを置く。
func distRoutes(prefix, broken string) []Route {
	rs := []Route{r("GET", prefix, ok(distListing(broken))), r("GET", prefix+"/", ok(distListing(broken)))}
	names := make([]string, 0, len(distFiles))
	for n := range distFiles {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		rs = append(rs, r("GET", prefix+"/"+n, Response{Body: distFiles[n], ContentType: "text/plain; charset=utf-8"}))
	}
	return rs
}

func initCases() []Case {
	server := distRoutes("/api/v1/dist", "")
	// 導入後の「次に」の案内で、利用者の役割とプロジェクトを見に行くことがある
	withMe := append(append([]Route(nil), server...), r("GET", "/api/v1/me", ok(meRes)), r("GET", pProj, ok(projectRes)))
	existing := []Seed{
		{Path: "ws/.claude/settings.json", Content: `{"env": {"OTHER": "1"}, "permissions": {"allow": ["Bash(ls:*)"]}}` + "\n"},
		{Path: "ws/CLAUDE.md", Content: "# プロジェクト\n\n既存の説明。\n"},
		{Path: "ws/.gitignore", Content: "node_modules/\n"},
	}
	cases := []Case{
		{Name: "init/server-claude-code", Args: []string{"init", "--project", "demo", "--source", "server"}, Routes: withMe},
		{Name: "init/server-existing-files", Args: []string{"init", "--project", "demo", "--source", "server", "--mcp"}, Routes: withMe, Seeds: existing},
		{Name: "init/server-dry-run", Args: []string{"init", "--project", "demo", "--source", "server", "--dry-run"}, Routes: withMe, Seeds: existing},
		{Name: "init/server-codex-copilot", Args: []string{"init", "--project", "demo", "--source", "server", "--agent", "codex,copilot", "--no-loop"}, Routes: withMe},
		{Name: "init/server-minimal", Args: []string{"init", "--project", "demo", "--source", "server", "--no-freshness", "--no-usage", "--no-summary", "--no-skill"}, Routes: withMe},
		{Name: "init/dist-url", Args: []string{"init", "--project", "demo", "--dist", APIPlaceholder + "/setup/tkt_golden"}, Env: map[string]string{"LOOPTRACK_TOKEN": ""},
			Routes: distRoutes("/setup/tkt_golden", "")},
		{Name: "init/dist-url-expired", Args: []string{"init", "--project", "demo", "--dist", APIPlaceholder + "/setup/tkt_old"}, Env: map[string]string{"LOOPTRACK_TOKEN": ""},
			Routes: []Route{r("GET", "/setup/tkt_old/", ErrorResponse(404, "not_found", "期限切れか無効な取得 URL です。MCP の setup をやり直してください"))}},
		{Name: "init/sha-mismatch", Args: []string{"init", "--project", "demo", "--source", "server"}, Routes: distRoutes("/api/v1/dist", "kit/core/skills/issue/SKILL.md")},
		{Name: "init/server-down", Args: []string{"init", "--project", "demo", "--source", "server"}, Routes: []Route{r("GET", "/api/v1/dist", Response{Drop: true})}},
		{Name: "init/server-no-token", Args: []string{"init", "--project", "demo", "--source", "server"}, Env: map[string]string{"LOOPTRACK_TOKEN": ""}},
		{Name: "init/loop-without-kit", Args: []string{"init", "--project", "demo", "--source", "server", "--loop"}, Routes: withMe},
		{Name: "init/no-project", Args: []string{"init"}, Env: map[string]string{"LOOPTRACK_PROJECT": ""}},
		{Name: "init/bad-project", Args: []string{"init", "--project", "Demo_1"}},
		{Name: "init/bad-agent", Args: []string{"init", "--project", "demo", "--agent", "cursor"}},
		{Name: "init/loop-and-no-loop", Args: []string{"init", "--project", "demo", "--loop", "--no-loop"}},
		{Name: "init/dist-with-link", Args: []string{"init", "--project", "demo", "--source", "link", "--dist", APIPlaceholder + "/setup/x"}},
		{Name: "init/other", Args: []string{"init", "--project", "demo", "--agent", "other"}},
		{Name: "init/missing-dir", Args: []string{"init", "--project", "demo", "--dir", "no-such-dir"}},
	}
	return cases
}
