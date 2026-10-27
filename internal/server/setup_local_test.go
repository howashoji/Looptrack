package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpLocal はローカルモードのサーバへトークンなしで MCP に接続する（言語は Accept-Language で選ぶ）。
func (e *env) mcpLocal(lang string) *mcpClient {
	e.t.Helper()
	h := map[string]string{"X-Looptrack-Project": "loc", "Accept-Language": lang}
	client := mcp.NewClient(&mcp.Implementation{Name: "claude-code", Version: "1"}, nil)
	cs, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: e.srv.URL + "/im/mcp", HTTPClient: &http.Client{Transport: headerTransport{h}}, MaxRetries: -1, DisableStandaloneSSE: true,
	}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-06-18"})
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { cs.Close() })
	return &mcpClient{t: e.t, cs: cs}
}

// TestSetupLocalModeGuidesTrayCLI は、ローカルモード（デスクトップ版）の setup が、配布ディレクトリに looptrack が無いときに
// 「サーバの管理者に配布を依頼する」ではなくトレイのメニューの「CLI を使えるようにする」を案内し、login の手順を出さないことを
// 日英で確かめる。ローカルモードの CLI はトークンなしで通る（api.Client.LocalMode と localAPI。TestLocalModeCLIWithoutToken）。
// 対照: 同じ「配布ディレクトリが無い」通常モードのサーバでは、従来の文面（管理者への依頼・login の手順）のまま。
func TestSetupLocalModeGuidesTrayCLI(t *testing.T) {
	// トレイのメニューの項目名（internal/client/desktop/tray の miCLI）と同じ語であること
	menu := map[string]string{"ja": "CLI を使えるようにする", "en": "Make the CLI available"}
	for lang, want := range menu {
		l, _ := i18n.Parse(lang)
		if got := i18n.T(l, "desktop.menu.install_cli"); got != want {
			t.Fatalf("前提が崩れています: トレイの項目名（%s）= %q, want %q", lang, got, want)
		}
	}
	stepsText := func(out setupOut) string {
		var b strings.Builder
		for _, s := range out.Steps {
			b.WriteString(s.Title + "\n" + s.Command + "\n" + s.CommandWindows + "\n")
		}
		return b.String()
	}

	e := newLocalEnv(t)
	localSetup(t, e)
	base := e.srv.URL + "/im"
	initCmd := "`looptrack issue init --project loc --url " + base + " --agent claude-code --loop`"
	for _, c := range []struct {
		lang, cliLine, admin string
	}{
		{"ja", "CLI: looptrack（デスクトップ版のアプリに入っています。トレイのメニューの「CLI を使えるようにする」で使えるようになります。", "サーバの管理者"},
		{"en", "CLI: looptrack (bundled with the desktop app; the tray menu's \"Make the CLI available\" makes it available.", "administrator"},
	} {
		text, data := e.mcpLocal(c.lang).call("setup", map[string]any{"loop": "yes", "workspace": t.TempDir()}, false)
		out := setupOf(t, data)
		all := text + "\n" + stepsText(out)
		if !strings.Contains(text, c.cliLine) {
			t.Errorf("%s: CLI の行がトレイの案内でない:\n%s", c.lang, text)
		}
		if len(out.Steps) == 0 || out.Steps[0].Who != "human" || out.Steps[0].Command != "" ||
			!strings.Contains(out.Steps[0].Title, menu[c.lang]) || !strings.Contains(out.Steps[0].Title, initCmd) {
			t.Errorf("%s: 1 つ目の手順がトレイの案内 + init でない: %+v", c.lang, out.Steps)
		}
		for _, bad := range []string{c.admin, "LOOPTRACK_DIST_DIR", "issue login", "/account"} {
			if strings.Contains(all, bad) {
				t.Errorf("%s: ローカルモードの setup に %q が残っている:\n%s", c.lang, bad, all)
			}
		}
	}

	// 対照: 通常モードのサーバ（配布ディレクトリが無い）は従来の文面のまま
	n, _, ed := newAPIEnv(t)
	m := n.mcpAsClient(ed.token, map[string]string{"X-Looptrack-Project": "req"}, "claude-code", "1", "2025-06-18")
	text, data := m.call("setup", map[string]any{"loop": "yes", "workspace": t.TempDir()}, false)
	out := setupOf(t, data)
	all := text + "\n" + stepsText(out)
	for _, want := range []string{"CLI: looptrack（このサーバの配布ディレクトリに looptrack がありません。取得の手順を示せません）",
		"サーバの管理者に配布（LOOPTRACK_DIST_DIR への looptrack の配置）を依頼する", "looptrack issue login --browser --url " + n.srv.URL + "/im"} {
		if !strings.Contains(all, want) {
			t.Errorf("通常モード: %q が無い:\n%s", want, all)
		}
	}
	if strings.Contains(all, menu["ja"]) {
		t.Errorf("通常モードにトレイの案内が出ている:\n%s", all)
	}
}

// TestSetupLocalModeNoLoginStep は、導入済みでフックの通知だけが無い（no_hook）ときも、ローカルモードでは login の手順を
// 出さないことを確かめる。対照は同じ材料の通常モード（先頭が login）。
func TestSetupLocalModeNoLoginStep(t *testing.T) {
	st := installStateJSON{State: "no_hook"}
	for _, local := range []bool{false, true} {
		g := &goSetup{lang: i18n.JA, os: "darwin", missing: "x", local: local}
		steps := g.baseSteps(i18n.JA, agentClaudeCode, "loc", "http://127.0.0.1:18090/looptrack", "d", st)
		hasLogin := false
		for _, s := range steps {
			if strings.Contains(s.Command, "issue login") {
				hasLogin = true
			}
		}
		if hasLogin == local {
			t.Errorf("local=%v: login の手順の有無が逆: %+v", local, steps)
		}
		if local && (len(steps) == 0 || steps[0].Who != "human") {
			t.Errorf("ローカルモードの no_hook の先頭が承認でない: %+v", steps)
		}
	}
}
