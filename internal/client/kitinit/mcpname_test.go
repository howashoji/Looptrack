package kitinit

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/setupwiz"
)

// TestGuidanceMCPServerName は、Copilot・Codex 向けの案内（AGENTS.md の管理節・init の表示）が示す MCP のサーバ名が、
// 実際の接続設定（サーバの初回設定の最後の画面が出すもの・init --mcp が書くもの）のサーバ名と一致することを確かめる。
func TestGuidanceMCPServerName(t *testing.T) {
	// サーバが示す接続設定のサーバ名（Copilot CLI の JSON のキー・Codex の [mcp_servers.<名前>]）
	var copilotName, codexName string
	for _, c := range setupwiz.MCPConfigs(i18n.JA, "http://127.0.0.1:9/looptrack", "demo") {
		switch c.Client {
		case "GitHub Copilot CLI":
			var m struct {
				MCPServers map[string]any `json:"mcpServers"`
			}
			if err := json.Unmarshal([]byte(c.Text), &m); err != nil || len(m.MCPServers) != 1 {
				t.Fatalf("Copilot CLI の接続設定を読めない: %q %v", c.Text, err)
			}
			for k := range m.MCPServers {
				copilotName = k
			}
		case "Codex":
			if m := regexp.MustCompile(`^\[mcp_servers\.([^\]]+)\]`).FindStringSubmatch(c.Text); m != nil {
				codexName = m[1]
			}
		}
	}
	if copilotName == "" || codexName == "" {
		t.Fatalf("接続設定のサーバ名が見つからない（copilot %q・codex %q）", copilotName, codexName)
	}

	// init --mcp が書く接続設定も同じ名前
	stubs(t, true)
	root := newRoot(t)
	if r := runInit(t, root, "", "--project", "demo", "--agent", "copilot", "--no-loop", "--mcp"); r.code != 0 {
		t.Fatalf("init: %s", r.stderr)
	}
	var written struct {
		MCPServers map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(read(t, filepath.Join(root, "ws", copilotCLIMCP))), &written); err != nil {
		t.Fatal(err)
	}
	if _, ok := written.MCPServers[copilotName]; !ok || len(written.MCPServers) != 1 {
		t.Errorf("%s のサーバ名が %s ではない: %v", copilotCLIMCP, copilotName, written.MCPServers)
	}

	stale := regexp.MustCompile(`サーバ名 im\b|allow-tool='im'|の im[）。]|login im\b|auth im\b`)
	check := func(label, text, name string, wants ...string) {
		t.Helper()
		if s := stale.FindString(text); s != "" {
			t.Errorf("%s に改名前のサーバ名が残っている: %q", label, s)
		}
		for _, w := range wants {
			if w = strings.ReplaceAll(w, "{name}", name); !strings.Contains(text, w) {
				t.Errorf("%s に %q が無い", label, w)
			}
		}
	}
	url, slug := "http://127.0.0.1:9/looptrack", "demo"
	check("AGENTS.md（Copilot だけ）", agentsMDBody([]string{"copilot"}, url, slug), copilotName,
		"（サーバ名 {name}）", "`~/.copilot/mcp-config.json` の {name}。", "`copilot --allow-tool='{name}'`")
	check("AGENTS.md（Codex・Copilot）", agentsMDBody([]string{"codex", "copilot"}, url, slug), codexName,
		"（サーバ名 {name}）", "`~/.copilot/mcp-config.json` の {name}）")
	check("init の Copilot の案内", copilotGuidance(i18n.JA, url, slug), copilotName,
		"copilot --allow-tool='{name}'", "/mcp auth {name}）", `{"mcpServers": {"{name}": {`)
	check("init の Codex の案内", codexGuidance(i18n.JA, url, slug), codexName, "[mcp_servers.{name}]", "codex mcp login {name}）")
	// 実際に置いた AGENTS.md（cliText を通した後）も同じ
	check("置いた AGENTS.md", read(t, filepath.Join(root, "ws", "AGENTS.md")), copilotName,
		"（サーバ名 {name}）", "`copilot --allow-tool='{name}'`")
}

// TestPutMCPKeepsOtherSetting は、.mcp.json の mcpServers.looptrack が別の設定なら置き換えず、実際のキーの名前で注記することを
// 確かめる（--force なら置き換える）。
func TestPutMCPKeepsOtherSetting(t *testing.T) {
	stubs(t, true)
	root := newRoot(t)
	other := `{"mcpServers": {"looptrack": {"type": "http", "url": "https://other.example/mcp"}}}` + "\n"
	write(t, filepath.Join(root, "ws", ".mcp.json"), other)
	r := runInit(t, root, "", "--project", "demo", "--no-loop", "--mcp")
	if r.code != 0 {
		t.Fatalf("init: %s", r.stderr)
	}
	out := r.stdout + r.stderr
	if !strings.Contains(out, ".mcp.json の mcpServers.looptrack は既に別の設定です（置き換えるなら --force）") {
		t.Errorf("注記が無い・キーの名前が違う:\n%s", out)
	}
	if strings.Contains(out, "mcpServers.im ") {
		t.Errorf("改名前のキーの名前が出ている:\n%s", out)
	}
	if got := read(t, filepath.Join(root, "ws", ".mcp.json")); got != other {
		t.Errorf("別の設定を置き換えた:\n%s", got)
	}
	if r := runInit(t, root, "", "--project", "demo", "--no-loop", "--mcp", "--force"); r.code != 0 {
		t.Fatalf("init --force: %s", r.stderr)
	}
	if got := read(t, filepath.Join(root, "ws", ".mcp.json")); strings.Contains(got, "other.example") {
		t.Errorf("--force で置き換えていない:\n%s", got)
	}
}
