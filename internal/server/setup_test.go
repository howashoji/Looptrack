package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/kit"
)

// MCP の接続設定だけで導入が完了するための setup ツール・導入済み通知・prompts。

// mcpAsClient は clientInfo とプロトコルの版を指定して接続する（protocol が空なら SDK の既定＝新しいプロトコル）。
// mcpAsClient も mcpAs と同じく日本語の AI として接続する（Accept-Language: ja。header で上書きできる）。
func (e *env) mcpAsClient(token string, header map[string]string, name, version, protocol string) *mcpClient {
	e.t.Helper()
	h := map[string]string{"Authorization": "Bearer " + token, "Accept-Language": "ja"}
	for k, v := range header {
		h[k] = v
	}
	client := mcp.NewClient(&mcp.Implementation{Name: name, Version: version}, nil)
	var opts *mcp.ClientSessionOptions
	if protocol != "" {
		opts = &mcp.ClientSessionOptions{ProtocolVersion: protocol}
	}
	cs, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: e.srv.URL + "/im/mcp", HTTPClient: &http.Client{Transport: headerTransport{h}}, MaxRetries: -1, DisableStandaloneSSE: true,
	}, opts)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { cs.Close() })
	return &mcpClient{t: e.t, cs: cs}
}

func TestAgentOf(t *testing.T) {
	for name, want := range map[string]string{
		"claude-code": agentClaudeCode, "Claude Code": agentClaudeCode, "codex-mcp-client": agentCodex, "Codex": agentCodex,
		"github-copilot-developer": agentCopilot, "copilot-cli": agentCopilot, "GitHub Copilot": agentCopilot, "Visual Studio Code": agentCopilot,
		"Visual Studio Code - Insiders": agentCopilot, "Code - OSS": agentCopilot,
		"claude-ai": agentOther, "test": agentOther, "": agentOther, "vscode-extension-x": agentOther,
		"Visual Studio Code - Exploration": agentCopilot, "code - oss-dev": agentCopilot, "my Visual Studio Code tool": agentOther,
	} {
		if got := agentOf(name); got != want {
			t.Errorf("agentOf(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestMCPClientInfo は clientInfo の記録と判定を確かめる: 旧プロトコル（initialize）は接続の記録（Mcp-Session-Id）から、
// 新しいプロトコルは要求の _meta から。接続の終了（DELETE）も記録する。
func TestMCPClientInfo(t *testing.T) {
	e, pr, ed := newAPIEnv(t)
	hdr := map[string]string{"X-Looptrack-Project": "req"}

	legacy := e.mcpAsClient(ed.token, hdr, "claude-code", "2.1.0", "2025-06-18")
	_, data := legacy.call("setup", map[string]any{}, false)
	client := data["client"].(map[string]any)
	if data["agent"] != "claude-code" || client["source"] != "connection" || client["name"] != "claude-code" || client["version"] != "2.1.0" {
		t.Errorf("旧プロトコルの clientInfo: %v", data["client"])
	}
	var n int
	var agent, proto, project string
	e.db.QueryRow("SELECT COUNT(*), MAX(agent), MAX(protocol_version), MAX(project) FROM mcp_connections WHERE client_name = 'claude-code'").Scan(&n, &agent, &proto, &project)
	if n != 1 || agent != "claude-code" || proto != "2025-06-18" || project != "req" {
		t.Errorf("接続の記録: n=%d agent=%s proto=%s project=%s", n, agent, proto, project)
	}
	legacy.cs.Close()
	var closed int
	e.db.QueryRow("SELECT COUNT(*) FROM mcp_connections WHERE client_name = 'claude-code' AND closed_at IS NOT NULL").Scan(&closed)
	if closed != 1 {
		t.Errorf("接続の終了（DELETE）が記録されていない")
	}

	modern := e.mcpAsClient(ed.token, hdr, "codex-mcp-client", "0.40.0", "")
	_, data = modern.call("setup", map[string]any{}, false)
	if client := data["client"].(map[string]any); data["agent"] != "codex" || client["source"] != "request" {
		t.Errorf("新しいプロトコルの clientInfo: %v", data["client"])
	}

	// GitHub Copilot: Copilot CLI と VS Code の名前で接続すると、接続の記録も setup も copilot になる
	for _, name := range []string{"github-copilot-developer", "Visual Studio Code"} {
		cp := e.mcpAsClient(ed.token, hdr, name, "1.0.0", "2025-06-18")
		if _, data = cp.call("setup", map[string]any{}, false); data["agent"] != "copilot" || !strings.Contains(data["text"].(string), "GitHub Copilot") {
			t.Errorf("%s の setup: agent=%v", name, data["agent"])
		}
		var got string
		e.db.QueryRow("SELECT MAX(agent) FROM mcp_connections WHERE client_name = ?", name).Scan(&got)
		if got != "copilot" {
			t.Errorf("%s の接続の記録: agent=%q", name, got)
		}
	}

	// 他の利用者の接続 ID を名乗っても使えない（判定できない＝other として扱う）
	var sid string
	e.db.QueryRow("SELECT id FROM mcp_connections WHERE client_name = 'claude-code'").Scan(&sid)
	other := e.user("other", "other-password-1", "member")
	store.SetMember(context.Background(), e.db, pr.ID, other.ID, "editor")
	spoof := e.mcpAsClient(e.apiAs(other).token, map[string]string{"X-Looptrack-Project": "req", "Mcp-Session-Id": sid}, "", "", "2025-06-18")
	if _, data = spoof.call("setup", map[string]any{"project": "req"}, false); data["agent"] == "claude-code" {
		t.Errorf("他の利用者の接続記録を使った: %v", data["client"])
	}
}

// TestMCPConnectionBothProtocols は、接続の記録と Mcp-Session-Id の発行が
// 旧プロトコル（initialize）と既定のプロトコル（SEP-2575 の server/discover）の**両方**で働くことを確かめる。
// 実在するクライアントに両方あるので、どちらか一方だけを見張る形にしない
// （2026-09-21: server/discover を見ていなかったため、既定のプロトコルでは記録も Mcp-Session-Id も作られなかった）。
func TestMCPConnectionBothProtocols(t *testing.T) {
	e, _, ed := newAPIEnv(t)
	hdr := map[string]string{"X-Looptrack-Project": "req"}
	for _, c := range []struct {
		what     string
		client   string
		protocol string // 空なら SDK の既定（新しいプロトコル）
		wantVer  string // mcp_connections.protocol_version
	}{
		{"旧プロトコル（initialize）", "claude-code", "2025-06-18", "2025-06-18"},
		{"既定のプロトコル（server/discover）", "codex-mcp-client", "", "2026-07-28"},
	} {
		m := e.mcpAsClient(ed.token, hdr, c.client, "1.0.0", c.protocol)
		_, data := m.call("create_issue", map[string]any{"title": c.what}, false)
		id, _ := data["id"].(string)
		if id == "" {
			t.Fatalf("%s: create_issue: %v", c.what, data)
		}
		// ① 接続が記録される（clientInfo とプロトコルの版も入る。新しいプロトコルは params の _meta から読む）
		var connID, gotVer string
		if err := e.db.QueryRow(`SELECT id, protocol_version FROM mcp_connections WHERE client_name = ?`, c.client).
			Scan(&connID, &gotVer); err != nil {
			t.Fatalf("%s: 接続が記録されていない: %v", c.what, err)
		}
		if gotVer != c.wantVer {
			t.Errorf("%s: protocol_version=%q（%q のはず）", c.what, gotVer, c.wantVer)
		}
		// ② 発行した Mcp-Session-Id がクライアントから戻り、③ セッション ID として記録される
		var sid string
		if err := e.db.QueryRow(`SELECT COALESCE(e.session_id, '') FROM issue_events e JOIN issues i ON i.id = e.issue_id
WHERE i.display_id = ? AND e.kind = 'create'`, id).Scan(&sid); err != nil {
			t.Fatalf("%s: 起票のイベントが無い: %v", c.what, err)
		}
		if sid != service.MCPSessionPrefix+connID {
			t.Errorf("%s: session_id=%q（%q のはず。Mcp-Session-Id が発行・往復していない）", c.what, sid, service.MCPSessionPrefix+connID)
		}
	}
}

// TestMCPAgentThroughSetStatus は、接続してきた AI の判定（mcpClient）が **set_status（クローズ）の経路**でも
// 接続の記録どおりになることを、旧プロトコルと既定のプロトコル（SEP-2575）の両方で確かめる。
// agent は表示だけでなく service.UsageTarget → usage の規則（RequiresUsage / CheckUsage）に流れるので、
// 接続の記録が取れないと**クローズの可否が変わりうる**（表示に留まらない）。
// 値そのもの（イベントの detail の "agent"）を見るに留め、規則の可否までは固定しない
// （可否を固定すると usage の規則を有効にしたプロジェクトが前提になり、agent の判定とは別のものを一緒に見張ることになる）。
func TestMCPAgentThroughSetStatus(t *testing.T) {
	e, _, ed := newAPIEnv(t)
	hdr := map[string]string{"X-Looptrack-Project": "req"}
	for _, c := range []struct {
		what      string
		client    string
		protocol  string // 空なら SDK の既定（新しいプロトコル = server/discover）
		wantAgent string
	}{
		{"旧プロトコル（initialize）", "claude-code", "2025-06-18", agentClaudeCode},
		{"既定のプロトコル（server/discover）", "claude-code", "", agentClaudeCode},
	} {
		m := e.mcpAsClient(ed.token, hdr, c.client, "1.0.0", c.protocol)
		_, data := m.call("create_issue", map[string]any{"title": c.what}, false)
		id, _ := data["id"].(string)
		if id == "" {
			t.Fatalf("%s: create_issue: %v", c.what, data)
		}
		m.call("set_status", map[string]any{"id": id, "status": "Done", "comment": "検証: 経路の確認"}, false)
		// status のイベントの detail に、接続してきた AI が残る（actor.Agent がそのまま入る）
		rows, err := e.db.Query(`SELECT COALESCE(e.detail, '{}') FROM issue_events e JOIN issues i ON i.id = e.issue_id
WHERE i.display_id = ? AND e.kind = 'status' ORDER BY e.id`, id)
		if err != nil {
			t.Fatal(err)
		}
		var agents []string
		for rows.Next() {
			var detail string
			if err := rows.Scan(&detail); err != nil {
				t.Fatal(err)
			}
			mp := map[string]any{}
			json.Unmarshal([]byte(detail), &mp)
			agent, _ := mp["agent"].(string)
			agents = append(agents, agent)
		}
		rows.Close()
		if len(agents) == 0 {
			t.Fatalf("%s: status のイベントが無い", c.what)
		}
		for _, got := range agents {
			if got != c.wantAgent {
				t.Errorf("%s: set_status の agent=%q（%q のはず。接続の記録が取れていない）", c.what, got, c.wantAgent)
			}
		}
	}
}

// TestMCPDiscoverWithoutClientInfo は、clientInfo を載せない新しいプロトコル（SEP-2575。clientInfo は任意）の
// クライアントでも、① 接続が記録され ② Mcp-Session-Id が発行され ③ AI の判定が User-Agent へ倒れ
// ④ 要求がエラーにならないことを確かめる（そういうクライアントが実在するかは調べず、実在しても壊れない形に固定する）。
// 要求は SDK のクライアントが必ず clientInfo を載せるため、素の HTTP で組み立てる
// （新しいプロトコルの要求に要るヘッダは Mcp-Protocol-Version・Mcp-Method と、tools/call の Mcp-Name）。
func TestMCPDiscoverWithoutClientInfo(t *testing.T) {
	e, _, ed := newAPIEnv(t)
	const meta = `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}`
	cl := e.client()
	post := func(ua, method, name, sid, body string) (*http.Response, string) {
		t.Helper()
		req, err := http.NewRequest("POST", e.srv.URL+"/im/mcp", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer "+ed.token)
		req.Header.Set("X-Looptrack-Project", "req")
		req.Header.Set("Mcp-Protocol-Version", "2026-07-28")
		req.Header.Set("Mcp-Method", method)
		req.Header.Set("User-Agent", ua)
		if name != "" {
			req.Header.Set("Mcp-Name", name)
		}
		if sid != "" {
			req.Header.Set(mcpSessionHeader, sid)
		}
		res, err := cl.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return res, string(b)
	}

	const ua = "claude-code/2.1.0"
	res, body := post(ua, "server/discover", "", "", `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{`+meta+`}}`)
	// ④ エラーにならない
	if res.StatusCode != http.StatusOK || strings.Contains(body, `"error"`) {
		t.Fatalf("clientInfo 無しの server/discover: %d %s", res.StatusCode, body)
	}
	// ② Mcp-Session-Id が発行される
	sid := res.Header.Get(mcpSessionHeader)
	if sid == "" {
		t.Fatalf("Mcp-Session-Id が返らない: %v", res.Header)
	}
	// ① 接続が記録される（名乗りが無いので client_name は空・agent は other）
	var gotName, gotAgent string
	if err := e.db.QueryRow(`SELECT client_name, COALESCE(agent, '') FROM mcp_connections WHERE id = ?`, sid).
		Scan(&gotName, &gotAgent); err != nil {
		t.Fatalf("接続が記録されていない: %v", err)
	}
	if gotName != "" || gotAgent != agentOther {
		t.Errorf("名乗りの無い接続の記録: client_name=%q agent=%q（\"\" と %q のはず）", gotName, gotAgent, agentOther)
	}

	// ③ 名乗りが無いので判定は User-Agent へ倒れる。知らない User-Agent なら空になる
	ed.json(201, "POST", "/projects/req/issues", map[string]any{"title": "clientInfo 無しの経路"}, nil)
	for _, c := range []struct{ ua, want string }{
		{ua, agentClaudeCode},
		{"curl/8.0.1", ""}, // agentOf が other にするものは記録しない（人の操作と区別できない）
	} {
		res, body := post(c.ua, "tools/call", "add_comment",
			sid, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"add_comment","arguments":{"id":"REQ-0001","text":"`+c.ua+`"},`+meta+`}}`)
		if res.StatusCode != http.StatusOK || strings.Contains(body, `"error"`) {
			t.Fatalf("%s の add_comment: %d %s", c.ua, res.StatusCode, body)
		}
	}
	rows, err := e.db.Query(`SELECT COALESCE(e.detail, '{}') FROM issue_events e JOIN issues i ON i.id = e.issue_id
WHERE i.display_id = 'REQ-0001' AND e.kind = 'comment' ORDER BY e.id`)
	if err != nil {
		t.Fatal(err)
	}
	var agents []string
	for rows.Next() {
		var detail string
		if err := rows.Scan(&detail); err != nil {
			t.Fatal(err)
		}
		m := map[string]any{}
		json.Unmarshal([]byte(detail), &m)
		agent, _ := m["agent"].(string)
		agents = append(agents, agent)
	}
	rows.Close()
	if len(agents) != 2 || agents[0] != agentClaudeCode || agents[1] != "" {
		t.Errorf("User-Agent からの判定: %q（[%q, \"\"] のはず）", agents, agentClaudeCode)
	}
}

type setupOut struct {
	Project string `json:"project"`
	Agent   string `json:"agent"`
	Install struct {
		State string `json:"state"`
	} `json:"install"`
	Steps []struct {
		Who            string `json:"who"`
		Title          string `json:"title"`
		Command        string `json:"command"`
		CommandWindows string `json:"command_windows"`
		Text           string `json:"text"`
	} `json:"steps"`
	Files []struct {
		Name   string `json:"name"`
		SHA256 string `json:"sha256"`
		URL    string `json:"url"`
	} `json:"files"`
	DistURL    string `json:"dist_url"`
	Text       string `json:"text"`
	Ask        string `json:"ask"`
	LoopAnswer string `json:"loop_answer"`
}

// checkLoopAsk は setup の結果が loop の問いだけ（手順は問いの 1 つ・コマンド・配布物・取得 URL が無い）であることを確かめる。
func checkLoopAsk(t *testing.T, label, text string, out setupOut) {
	t.Helper()
	latest, _ := latestDist()
	if out.Ask != "loop" || len(out.Steps) != 1 || out.Steps[0].Who != "human" || out.Steps[0].Command != "" || out.Steps[0].Text != loopQuestion(i18n.JA, latest) ||
		len(out.Files) != 0 || out.DistURL != "" || out.LoopAnswer != "" {
		t.Errorf("%s: 問いだけの結果でない: ask=%q steps=%+v files=%d dist=%q", label, out.Ask, out.Steps, len(out.Files), out.DistURL)
	}
	// 問いの文面は「後から looptrack issue init --loop で変えられる」を含むので、実行できるコマンドの印で見る
	for _, bad := range []string{"curl", "--agent", "--source server", "--no-loop", "/setup/", "login --browser"} {
		if strings.Contains(text, bad) {
			t.Errorf("%s: 問いだけの本文に %q がある:\n%s", label, bad, text)
		}
	}
	for _, want := range []string{"AI への指示:", "loop=yes", "loop=no", "AI が答えを決めない", loopQuestion(i18n.JA, latest)} {
		if !strings.Contains(text, want) {
			t.Errorf("%s: 問いだけの本文に %q が無い:\n%s", label, want, text)
		}
	}
}

func setupOf(t *testing.T, data map[string]any) setupOut {
	t.Helper()
	var out setupOut
	b, _ := json.Marshal(data)
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestSetupTool は setup ツールが AI の種類ごとの手順と配布物の SHA-256 を返し、取得 URL が期限つきで使えることを確かめる。
func TestSetupTool(t *testing.T) {
	e, _, ed := newAPIEnv(t)
	withFakeDist(t, e)
	hdr := map[string]string{"X-Looptrack-Project": "req"}
	files, _ := distFiles()
	allSums := map[string]string{} // kit
	for _, f := range files {
		allSums[f.Name] = f.SHA256
	}

	for _, c := range []struct {
		client, want string
		inStep       []string
	}{
		{"claude-code", "claude-code", []string{"--agent claude-code --source server --dist", "Claude Code を再起動", "looptrack\" issue login --browser --url " + e.srv.URL + "/im"}},
		{"codex-mcp-client", "codex", []string{"--agent codex --source server --dist", "/hooks", "issue login --browser --url"}},
		{"github-copilot-developer", "copilot", []string{"--agent copilot --source server --dist", "Copilot CLI はプロジェクトで copilot を起動", "issue login --browser --url"}},
		{"cursor", "other", []string{"--agent other --source server --dist", "installed --agent other", "指示ファイル"}},
	} {
		m := e.mcpAsClient(ed.token, hdr, c.client, "1", "2025-06-18")
		text, data := m.call("setup", map[string]any{}, false)
		out := setupOf(t, data)
		if out.Agent != c.want || out.Install.State != "missing" || m.notice != "" {
			t.Errorf("%s: agent=%s state=%s notice=%q", c.client, out.Agent, out.Install.State, m.notice)
		}
		// loop を入れられる AI（配布物に kit/loop がある）は、1 回目は loop の問いだけ。答えを付けて呼び直すと手順が返る
		if c.want != "other" {
			checkLoopAsk(t, c.client, text, out)
			text, data = m.call("setup", map[string]any{"loop": "yes"}, false)
			out = setupOf(t, data)
			if out.Ask != "" || out.LoopAnswer != "yes" || out.Install.State != "missing" {
				t.Errorf("%s: loop=yes: ask=%q answer=%q", c.client, out.Ask, out.LoopAnswer)
			}
		}
		var all strings.Builder
		for _, s := range out.Steps {
			all.WriteString(s.Title + "\n" + s.Command + "\n")
		}
		for _, want := range c.inStep {
			if !strings.Contains(all.String(), want) {
				t.Errorf("%s: 手順に %q が無い:\n%s", c.client, want, all.String())
			}
		}
		// 配布物（kit）の SHA-256 は GET /api/v1/dist と同じ。手順の取得コマンドは looptrack のハッシュを確かめる
		if len(out.Files) != len(kit.Names()) {
			t.Fatalf("files: %+v", out.Files)
		}
		for _, f := range out.Files {
			if f.SHA256 != allSums[f.Name] || f.URL != out.DistURL+"/"+f.Name {
				t.Errorf("%s: %s のハッシュ・URL: %+v", c.client, f.Name, f)
			}
		}
		// 本文には配布物の一覧を出さない（一覧の URL だけ）
		if !strings.Contains(text, out.DistURL+"/") || strings.Contains(text, "kit/loop/rules/") {
			t.Errorf("%s: 本文の配布物:\n%s", c.client, text)
		}
		// 最初の手順は取得 + init の 1 つ。loop を入れられる AI は答えの旗（--loop）付き
		first := out.Steps[0].Command
		if c.want != "other" && (!strings.HasSuffix(first, ` --loop`) || strings.Contains(first, "--no-loop")) {
			t.Errorf("%s: loop=yes の取得 + init: %s", c.client, first)
		}
		if !strings.Contains(first, `curl -fsSL "$U"`) || !strings.Contains(first, out.DistURL+"/bin/looptrack_v1.0.0_") || !strings.HasPrefix(out.DistURL, e.srv.URL+"/im/setup/") {
			t.Errorf("%s: 取得コマンド: %s", c.client, first)
		}
	}

	// agent 引数で上書きできる。未知の値は拒否
	m := e.mcpAsClient(ed.token, hdr, "cursor", "1", "")
	if _, data := m.call("setup", map[string]any{"agent": "codex"}, false); data["agent"] != "codex" {
		t.Errorf("agent の上書き: %v", data["agent"])
	}
	m.call("setup", map[string]any{"agent": "gemini"}, true)

	// 取得 URL はトークンなしで使える（一覧と本体・ハッシュ）。改ざん・期限切れは 403
	_, data := m.call("setup", map[string]any{}, false)
	out := setupOf(t, data)
	c := e.client()
	res, body := e.get(c, strings.TrimPrefix(out.DistURL, e.srv.URL)+"/")
	var list struct {
		Files []distFileJSON `json:"files"`
	}
	if res.StatusCode != 200 || json.Unmarshal([]byte(body), &list) != nil || len(list.Files) != len(kit.Names()) {
		t.Fatalf("一覧: %d %s", res.StatusCode, body)
	}
	// kit の名前（「/」を含む）も券の URL で取れる（looptrack issue init --dist が skill /issue を置ける）
	for _, f := range out.Files {
		if !strings.HasPrefix(f.Name, "kit/") {
			continue
		}
		res, body := e.get(c, strings.TrimPrefix(f.URL, e.srv.URL))
		sum := sha256.Sum256([]byte(body))
		if res.StatusCode != 200 || hex.EncodeToString(sum[:]) != f.SHA256 {
			t.Errorf("券の URL で %s: %d", f.Name, res.StatusCode)
		}
	}
	res, body = e.get(c, strings.TrimPrefix(out.Files[0].URL, e.srv.URL))
	sum := sha256.Sum256([]byte(body))
	if res.StatusCode != 200 || hex.EncodeToString(sum[:]) != out.Files[0].SHA256 {
		t.Errorf("本体: %d", res.StatusCode)
	}
	if res, _ := e.get(c, "/im/setup/AAAA"+strings.TrimPrefix(out.DistURL, e.srv.URL+"/im/setup/")[4:]+"/bin/looptrack"); res.StatusCode != 403 {
		t.Errorf("改ざんした券: %d", res.StatusCode)
	}
	for _, name := range []string{"/NOTICE", "/deploy.sh"} { // kit と実行ファイル以外は配らない
		if res, _ := e.get(c, strings.TrimPrefix(out.DistURL, e.srv.URL)+name); res.StatusCode != 404 {
			t.Errorf("配布していないファイル %s: %d", name, res.StatusCode)
		}
	}
	e.clock.Add(61 * time.Minute)
	if res, body := e.get(c, strings.TrimPrefix(out.DistURL, e.srv.URL)+"/bin/looptrack"); res.StatusCode != 403 || !strings.Contains(body, "setup ツールを呼び直して") {
		t.Errorf("期限切れの券: %d %s", res.StatusCode, body)
	}
}

// TestInstallNotice は導入済み通知の前後で MCP のツール結果の指示が変わることを確かめる。
func TestInstallNotice(t *testing.T) {
	e, pr, ed := newAPIEnv(t)
	hdr := map[string]string{"X-Looptrack-Project": "req"}
	m := e.mcpAsClient(ed.token, hdr, "claude-code", "2.1.0", "2025-06-18")
	legacyFiles := map[string]string{"kit.tar.gz": strings.Repeat("a", 64)} // 撤去した 1.0.0 より前の CLI の通知の形（client なし）

	// 通知の前: どのツールの結果にも導入の指示が付く（setup 自身には付けない。エラーの結果にも付く）
	m.call("list_issues", map[string]any{}, false)
	if !strings.Contains(m.notice, "【導入が未完了】Claude Code") || !strings.Contains(m.notice, "setup ツール") {
		t.Errorf("通知前: %q", m.notice)
	}
	m.call("get_issue", map[string]any{"id": "REQ-9999"}, true)
	if m.notice == "" {
		t.Errorf("エラーの結果に指示が無い")
	}
	// 変更の結果: 操作の文のすぐ後に付与の指示（同じ文）、導入の指示は別の文として最後に付く
	if text, _ := m.call("create_issue", map[string]any{"title": "両方の指示"}, false); !strings.HasSuffix(text, "トークン情報が未付与です。次を実行してください: looptrack issue usage attach REQ-0001") ||
		!strings.Contains(m.notice, "【導入が未完了】") || strings.Contains(text, "【導入が未完了】") {
		t.Errorf("付与の指示と導入の指示: text=%q notice=%q", text, m.notice)
	}

	// 手動の通知だけではフックの AI は導入済みにならない（フックの承認を促す）
	post := func(body map[string]any) installStateJSON {
		var st installStateJSON
		ed.json(200, "POST", "/projects/req/install", body, &st)
		return st
	}
	if st := post(installBody("claude-code", "manual", "server", "", nil)); st.State != "no_hook" {
		t.Errorf("手動の通知: %+v", st)
	}
	m.call("list_issues", map[string]any{}, false)
	if !strings.Contains(m.notice, "フックからの通知がまだ") || !strings.Contains(m.notice, "Claude Code を再起動") {
		t.Errorf("フック未承認: %q", m.notice)
	}

	// フックからの通知（最新）の後は付かない
	hooked := installBody("claude-code", "hook", "server", "", nil)
	hooked["host"], hooked["workspace"] = "mac", "proj"
	if st := post(hooked); st.State != "current" || st.Host != "mac" || st.Workspace != "proj" {
		t.Errorf("フックの通知: %+v", st)
	}
	if text, _ := m.call("list_issues", map[string]any{}, false); m.notice != "" || strings.Contains(text, "【") {
		t.Errorf("通知後にも指示が付く: %q", m.notice)
	}
	if _, data := m.call("setup", map[string]any{}, false); setupOf(t, data).Install.State != "current" || !strings.Contains(setupOf(t, data).Text, "導入済み") {
		t.Errorf("setup: %v", data["install"])
	}

	// 撤去した 1.0.0 より前の CLI（client の無い通知）は、フックから届いても looptrack への置き換えを求める
	st := post(map[string]any{"agent": "claude-code", "trigger": "hook", "source": "server", "files": legacyFiles})
	if st.State != "stale" || strings.Join(st.StaleFiles, ",") != "looptrack" || !strings.Contains(st.UpdateCommand, "looptrack issue init --project req --agent claude-code") {
		t.Errorf("1.0.0 より前の CLI の通知: %+v", st)
	}
	m.call("list_issues", map[string]any{}, false)
	if !strings.Contains(m.notice, "【配布スクリプトの更新】") || !strings.Contains(m.notice, "1.0.0 より前の CLI で導入されています") || !strings.Contains(m.notice, "looptrack に置き換えて") {
		t.Errorf("更新の指示: %q", m.notice)
	}
	// 手動の通知でもフックの記録（hook_at）は消えない
	post(installBody("claude-code", "manual", "server", "", nil))
	m.call("list_issues", map[string]any{}, false)
	if m.notice != "" {
		t.Errorf("手動の再通知で未導入に戻った: %q", m.notice)
	}

	// AI ごとに別。Codex はまだ未導入。フックの無い AI（other）は手動の通知で導入済みになる
	cx := e.mcpAsClient(ed.token, hdr, "codex-mcp-client", "1", "")
	cx.call("list_issues", map[string]any{}, false)
	if !strings.Contains(cx.notice, "Codex") {
		t.Errorf("Codex: %q", cx.notice)
	}
	ot := e.mcpAsClient(ed.token, hdr, "cursor", "1", "")
	post(installBody("other", "manual", "", "", nil))
	ot.call("list_issues", map[string]any{}, false)
	if ot.notice != "" {
		t.Errorf("other の手動通知の後: %q", ot.notice)
	}

	// 一覧（GET）と入力の検査
	var got struct {
		Installs []installStateJSON `json:"installs"`
	}
	ed.json(200, "GET", "/projects/req/install", nil, &got)
	if len(got.Installs) != 4 || got.Installs[0].Agent != "claude-code" || got.Installs[0].State != "current" ||
		got.Installs[1].State != "missing" || got.Installs[2].Agent != "copilot" || got.Installs[2].State != "missing" || got.Installs[3].State != "current" {
		t.Errorf("GET install: %+v", got.Installs)
	}
	for _, bad := range []map[string]any{
		{"agent": "gemini", "trigger": "hook", "files": legacyFiles},
		{"agent": "codex", "trigger": "auto", "files": legacyFiles},
		{"agent": "codex", "trigger": "hook", "files": map[string]string{}},
		{"agent": "codex", "trigger": "hook", "files": map[string]string{"kit.tar.gz": "xyz"}},
		{"agent": "codex", "trigger": "hook", "files": map[string]string{"../x": legacyFiles["kit.tar.gz"]}},
		{"agent": "codex", "trigger": "hook", "source": "ftp", "files": legacyFiles},
	} {
		ed.fail(400, "POST", "/projects/req/install", bad)
	}
	e.project("secret")
	ed.fail(404, "POST", "/projects/secret/install", installBody("codex", "hook", "", "", nil))

	// 閲覧のみの利用者も通知できる（閲覧でもフックと CLI を使う）
	viewer := e.user("vi", "vi-password-1234", "member")
	store.SetMember(context.Background(), e.db, pr.ID, viewer.ID, "viewer")
	e.apiAs(viewer).json(200, "POST", "/projects/req/install", installBody("codex", "hook", "", "", nil), nil)
}

func TestMCPPrompts(t *testing.T) {
	e, _, ed := newAPIEnv(t)
	m := e.mcpAsClient(ed.token, map[string]string{"X-Looptrack-Project": "req"}, "claude-code", "1", "2025-06-18")
	ctx := context.Background()
	list, err := m.cs.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range list.Prompts {
		names = append(names, p.Name)
		if p.Description == "" || len(p.Arguments) != 1 || p.Arguments[0].Name != "project" {
			t.Errorf("%s: %+v", p.Name, p)
		}
	}
	if strings.Join(names, ",") != "loop,review,setup" {
		t.Errorf("prompts: %v", names)
	}
	res, err := m.cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: "loop"})
	if err != nil {
		t.Fatal(err)
	}
	text := res.Messages[0].Content.(*mcp.TextContent).Text
	for _, want := range []string{"（プロジェクト req。", "guide", "next ツールで着手", "add_comment", "受け入れ条件を 1 つずつ検証", "set_status で Done", "次の next"} {
		if !strings.Contains(text, want) {
			t.Errorf("loop に %q が無い:\n%s", want, text)
		}
	}
	res, _ = m.cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: "setup", Arguments: map[string]string{"project": "web"}})
	// loop の問いは 2 段: 問いを示して答えを得てから loop を付けて呼び直す、を prompt・instructions・ツールの説明に書く
	if text := res.Messages[0].Content.(*mcp.TextContent).Text; !strings.Contains(text, "setup ツールを呼ぶ") || !strings.Contains(text, "プロジェクト web") ||
		!strings.Contains(text, "引数 loop（yes / no）") || !strings.Contains(text, "AI が答えを決めない") {
		t.Errorf("setup prompt: %s", text)
	}
	// 接続時の指示は setup → guide → next（prompt loop）
	if ins := m.cs.InitializeResult().Instructions; !strings.Contains(ins, "最初に setup ツールを 1 回呼ぶ") || !strings.Contains(ins, "prompt「loop」") ||
		!strings.Contains(ins, "setup を引数 loop（yes / no）") || !strings.Contains(ins, "AI が勝手に決めず") {
		t.Errorf("instructions: %s", ins)
	}
	tools, err := m.cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools.Tools {
		if tl.Name != "setup" {
			continue
		}
		schema, _ := json.Marshal(tl.InputSchema)
		if !strings.Contains(tl.Description, "1 回目は問いだけを返し") || !strings.Contains(tl.Description, "AI が答えを決めて loop を付けない") ||
			!strings.Contains(string(schema), `"loop"`) {
			t.Errorf("setup の説明・引数: %s\n%s", tl.Description, schema)
		}
	}
}

// hookText は hook の出力（JSON。additionalContext・systemMessage など）の文字列をつなげたもの（JSON でなければそのまま）。
func hookText(out string) string {
	var v any
	if json.Unmarshal([]byte(out), &v) != nil {
		return out
	}
	var b strings.Builder
	var walk func(any)
	walk = func(x any) {
		switch x := x.(type) {
		case string:
			b.WriteString(x + "\n")
		case map[string]any:
			for _, y := range x {
				walk(y)
			}
		case []any:
			for _, y := range x {
				walk(y)
			}
		}
	}
	walk(v)
	return b.String()
}

// sessionStartHook は init が配線した SessionStart の summary の hook のコマンド（settings.json か、PATH に無い looptrack を
// 絶対パスで配線した settings.local.json）と、settings.json の env。
func sessionStartHook(t *testing.T, proj string) (string, map[string]string) {
	t.Helper()
	type hookFile struct {
		Env   map[string]string `json:"env"`
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	var settings hookFile
	if err := json.Unmarshal([]byte(mustRead(t, filepath.Join(proj, ".claude", "settings.json"))), &settings); err != nil {
		t.Fatal(err)
	}
	cmd := ""
	for _, f := range []string{"settings.json", "settings.local.json"} {
		var h hookFile
		if b, err := os.ReadFile(filepath.Join(proj, ".claude", f)); err != nil || json.Unmarshal(b, &h) != nil {
			continue
		}
		for _, g := range h.Hooks["SessionStart"] {
			for _, c := range g.Hooks {
				if strings.Contains(c.Command, "hook summary") {
					cmd = c.Command
				}
			}
		}
	}
	if !strings.Contains(cmd, "--agent claude-code") {
		t.Fatalf("SessionStart の summary の hook が無い: %q", cmd)
	}
	return cmd, settings.Env
}

// sessionStartInput は SessionStart の hook に渡す入力（Claude Code の形）。
const sessionStartInput = `echo '{"hook_event_name":"SessionStart","session_id":"s1","source":"startup"}' | `

// TestSetupEndToEnd は「MCP の接続だけ」の状態から、setup が返したコマンドをそのまま実行して導入し（配布ディレクトリの
// looptrack を券の URL から取って置き、init する）、利用者のトークン登録（ここでは LOOPTRACK_TOKEN）とフック（SessionStart を
// そのまま実行）で導入済みになるまでを通す。サーバの kit が変わったら SessionStart の出力と MCP に更新の指示が出て、init で解消する。
func TestSetupEndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("取得コマンドの sh を流すため Windows では省略（PowerShell の手順の形は TestSetupGo が確かめる）")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl が無い")
	}
	e, _, ed := newAPIEnv(t)
	bin, err := os.ReadFile(looptrackBin(t))
	if err != nil {
		t.Fatal(err)
	}
	e.s.cfg.DistDir = t.TempDir()
	writeDist(t, e.s.cfg.DistDir, map[string]string{"looptrack_v1.0.0_" + runtime.GOOS + "_" + runtime.GOARCH: string(bin)})
	m := e.mcpAsClient(ed.token, map[string]string{"X-Looptrack-Project": "req"}, "claude-code", "2.1.0", "2025-06-18")
	proj, home := t.TempDir(), t.TempDir()
	sh := func(env []string, script string) cliResult {
		t.Helper()
		cmd := exec.Command("/bin/sh", "-c", script)
		cmd.Env, cmd.Dir = append(cliHomeEnv(home), env...), proj
		var so, se bytes.Buffer
		cmd.Stdout, cmd.Stderr = &so, &se
		err := cmd.Run()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		return cliResult{so.String(), se.String(), code}
	}

	// 1. setup の [AI] の手順をそのまま実行（トークンなし）
	// 1 回目は loop の問いだけ。利用者の答え「入れない」を付けて呼び直すと、取得 + init（--no-loop）の 1 つが返る
	text, data := m.call("setup", map[string]any{"os": runtime.GOOS}, false)
	checkLoopAsk(t, "1 回目", text, setupOf(t, data))
	_, data = m.call("setup", map[string]any{"loop": "no", "os": runtime.GOOS}, false)
	out := setupOf(t, data)
	if !strings.HasSuffix(out.Steps[0].Command, " --no-loop") || !strings.Contains(out.Steps[0].Command, "curl -fsSL") {
		t.Fatalf("手順: %+v", out.Steps)
	}
	if res := sh(nil, out.Steps[0].Command); res.code != 0 {
		t.Fatalf("取得と init: %d\n%s\n%s", res.code, res.stdout, res.stderr)
	}
	if b, err := os.ReadFile(filepath.Join(home, ".local", "bin", "looptrack")); err != nil || !bytes.Equal(b, bin) {
		t.Fatalf("取得コマンドが looptrack を置いていない: %v", err)
	}
	sessionStart, settingsEnv := sessionStartHook(t, proj)
	if settingsEnv["LOOPTRACK_API_URL"] != e.srv.URL+"/im" || settingsEnv["LOOPTRACK_PROJECT"] != "req" {
		t.Fatalf("settings.json の env: %v", settingsEnv)
	}
	// 以前の CLI の入口・hook（.claude/scripts/*.py）は置かない
	if legacy, _ := filepath.Glob(filepath.Join(proj, ".claude", "scripts", "*.py")); len(legacy) != 0 {
		t.Errorf("以前の CLI のファイルを置いた: %v", legacy)
	}
	// kit/core: 券の URL から skill /issue を取って置く。言行一致の Stop hook は廃止したので
	// 置かず配線しない。取得元（.claude/.looptrack-kit.json）に core のハッシュを控える
	// （本文は init が CLI の呼び方を looptrack issue にそろえて置くので、配布物とバイト一致とは限らない）
	if b, err := os.ReadFile(filepath.Join(proj, ".claude/skills/issue/SKILL.md")); err != nil || !strings.Contains(string(b), "name: issue") {
		t.Errorf("サーバ経由の導入で skill /issue が置かれていない（%v）", err)
	}
	if _, err := os.Lstat(filepath.Join(proj, ".claude/hooks/im-core")); err == nil {
		t.Errorf("廃止した言行一致の hook の置き場が作られている")
	}
	var kitJSON struct {
		Source string `json:"source"`
		Core   struct {
			Files map[string]string `json:"files"`
		} `json:"core"`
	}
	if err := json.Unmarshal([]byte(mustRead(t, filepath.Join(proj, ".claude", ".looptrack-kit.json"))), &kitJSON); err != nil ||
		kitJSON.Source != "server" || kitJSON.Core.Files["kit/core/skills/issue/SKILL.md"] == "" {
		t.Errorf(".looptrack-kit.json: %+v %v", kitJSON, err)
	}
	// まだフックは動いていない（再起動前）ので、指示は出たまま
	m.call("list_issues", map[string]any{}, false)
	if !strings.Contains(m.notice, "【導入が未完了】") {
		t.Errorf("導入前: %q", m.notice)
	}

	// 2. 利用者がトークンを登録し、再起動（SessionStart のフックが動く）
	env := []string{"CLAUDE_PROJECT_DIR=" + proj, "LOOPTRACK_TOKEN=" + ed.token, "LOOPTRACK_USAGE=0"}
	for k, v := range settingsEnv {
		env = append(env, k+"="+v)
	}
	if res := sh(env, sessionStartInput+sessionStart); res.code != 0 || !strings.Contains(hookText(res.stdout), "未クローズ 0 件") ||
		strings.Contains(hookText(res.stdout), cliNoticeMissing) || strings.Contains(hookText(res.stdout), cliNoticeStale) {
		t.Fatalf("SessionStart: %d %s %s", res.code, hookText(res.stdout), res.stderr)
	}
	m.call("list_issues", map[string]any{}, false)
	if m.notice != "" {
		t.Errorf("導入後も指示が出る: %q", m.notice)
	}
	var got struct {
		Installs []installStateJSON `json:"installs"`
	}
	ed.json(200, "GET", "/projects/req/install", nil, &got)
	st := got.Installs[0]
	if st.Agent != "claude-code" || st.State != "current" || st.Source != "server" || st.Workspace != filepath.Base(proj) || st.HookAt == "" || st.ClientOS != runtime.GOOS {
		t.Errorf("導入状態: %+v", st)
	}

	// 3. サーバの kit/core が変わると（新しい版のデプロイ）、SessionStart と MCP に更新の指示が出る。init の再実行で解消する
	names, read := kitNames, kitRead
	t.Cleanup(func() { kitNames, kitRead = names, read })
	kitNames = func() []string { return append(names(), "kit/core/skills/issue/EXTRA.md") }
	kitRead = func(name string) ([]byte, error) {
		if name == "kit/core/skills/issue/EXTRA.md" {
			return []byte("# 新しい版で足したファイル\n"), nil
		}
		return read(name)
	}
	if res := sh(env, sessionStartInput+sessionStart); !strings.Contains(hookText(res.stdout), cliNoticeStale) ||
		!strings.Contains(hookText(res.stdout), "kit/core 一式") {
		t.Errorf("古いときの SessionStart: %s %s", hookText(res.stdout), res.stderr)
	}
	m.call("list_issues", map[string]any{}, false)
	if !strings.Contains(m.notice, "【配布スクリプトの更新】") || !strings.Contains(m.notice, "looptrack issue init --project req --agent claude-code") {
		t.Errorf("古いときの MCP: %q", m.notice)
	}
	out = setupOf(t, second(m.call("setup", map[string]any{"os": runtime.GOOS}, false)))
	if res := sh(env, `"$HOME/.local/bin/looptrack" issue init --project req --agent claude-code --url `+e.srv.URL+`/im --source server --dist '`+out.DistURL+`'`); res.code != 0 {
		t.Fatalf("更新: %s %s", res.stdout, res.stderr)
	}
	if res := sh(env, sessionStartInput+sessionStart); strings.Contains(hookText(res.stdout), cliNoticeMissing) ||
		strings.Contains(hookText(res.stdout), cliNoticeStale) {
		t.Errorf("更新後の SessionStart: %s", hookText(res.stdout))
	}
	m.call("list_issues", map[string]any{}, false)
	if m.notice != "" {
		t.Errorf("更新後も指示が出る: %q", m.notice)
	}
	// 手動の確認コマンド
	if res := sh(env, `"$HOME/.local/bin/looptrack" issue installed --agent claude-code`); res.code != 0 || !strings.HasPrefix(res.stdout, "導入済み（Claude Code・looptrack ") ||
		!strings.Contains(res.stdout, "配布物は最新") {
		t.Errorf("installed: %d %s %s", res.code, res.stdout, res.stderr)
	}
}

// setupTextMax は未導入の setup の本文の上限。Copilot CLI は大きなツール結果を一時ファイルに逃がし、先頭の
// プレビュー（実物で 500 文字）だけを AI に渡す。閾値は公式文書で 20 KiB（COPILOT_LARGE_OUTPUT_THRESHOLD_BYTES で変えられる。
// docs.github.com の「Managing context in GitHub Copilot CLI」）だが、MCP の結果を約 10 KB で切り詰める不具合の報告
// （github/copilot-cli#1732）もあるため、保守的に 8 KiB とする。大きさは AI に渡る本文（content の text）で測る
// （構造化データは _meta に置き、AI には渡さない）。
const setupTextMax = 8 << 10

// setupPreviewRunes は Copilot CLI がファイルに逃がしたときに AI に渡す先頭のプレビューの文字数（実測値）。
// CLI・フックは要求の言語をサーバへ送る（HTTP の Accept-Language。internal/client/api の client.go が
// i18n.FromEnv で決める）。テストの子プロセスは cliHomeEnv で LOOPTRACK_LANG=ja を持つので、サーバの文面は
// 日本語で返る。子プロセスの looptrack の出力を見る検査は、その日本語の文面で確かめる。
const (
	cliNoticeMissing = "【導入が未完了】"
	cliNoticeStale   = "【配布スクリプトの更新】"
)

const setupPreviewRunes = 500

// setupAskMax は loop の問いだけの setup の本文の上限（問いの全文と指示・状態だけなので小さく保つ）。
const setupAskMax = 2 << 10

// TestSetupTextSize は、未導入の setup の本文が上限に収まり、先頭 500 文字に AI への指示と loop の問いの全文があることを確かめる。
// loop を入れられる AI の 1 回目は問いだけ（2 KiB 以下）、答えを付けた 2 回目の手順も 8 KiB 以下。
func TestSetupTextSize(t *testing.T) {
	e, _, ed := newAPIEnv(t)
	hdr := map[string]string{"X-Looptrack-Project": "req"}
	latest, _ := latestDist()
	question := loopQuestion(i18n.JA, latest)
	head := func(text string) string {
		r := []rune(text)
		return string(r[:min(len(r), setupPreviewRunes)])
	}
	for _, c := range []string{"github-copilot-developer", "claude-code", "codex-mcp-client", "cursor"} {
		m := e.mcpAsClient(ed.token, hdr, c, "1", "2025-06-18")
		text, data := m.call("setup", map[string]any{"workspace": "/Users/a/tmp/proj"}, false)
		if setupOf(t, data).Install.State != "missing" {
			t.Fatalf("%s: 未導入でない", c)
		}
		if c == "cursor" { // other は loop を問わない。1 回目から手順で、指示だけ先頭に置く
			if len(text) > setupTextMax {
				t.Errorf("%s: 未導入の setup の本文が %d バイト（上限 %d）", c, len(text), setupTextMax)
			}
			if h := head(text); !strings.Contains(h, "AI への指示:") || !strings.Contains(h, "承認を得てから実行") {
				t.Errorf("%s: 先頭 %d 文字に指示が無い:\n%s", c, setupPreviewRunes, h)
			}
			continue
		}
		// 1 回目: 問いだけ。先頭 500 文字に指示と問いの全文
		checkLoopAsk(t, c, text, setupOf(t, data))
		meta, _ := json.Marshal(data)
		t.Logf("%s: 問いだけの本文 %d バイト（%d 文字）・構造化データ %d バイト", c, len(text), len([]rune(text)), len(meta))
		if len(text) > setupAskMax {
			t.Errorf("%s: 問いだけの本文が %d バイト（上限 %d）", c, len(text), setupAskMax)
		}
		h := head(text)
		for _, want := range []string{"AI への指示:", "loop=yes", "同じ workspace", "AI が答えを決めない", question} {
			if !strings.Contains(h, want) {
				t.Errorf("%s: 先頭 %d 文字に %q が無い:\n%s", c, setupPreviewRunes, want, h)
			}
		}
		// 2 回目（答えつき）: 手順。問いは繰り返さない
		for _, ans := range []string{"yes", "no"} {
			text, _ := m.call("setup", map[string]any{"workspace": "/Users/a/tmp/proj", "loop": ans}, false)
			t.Logf("%s: loop=%s の本文 %d バイト", c, ans, len(text))
			if len(text) > setupTextMax {
				t.Errorf("%s: loop=%s の setup の本文が %d バイト（上限 %d）", c, ans, len(text), setupTextMax)
			}
			if h := head(text); !strings.Contains(h, "AI への指示: 利用者の答え") || strings.Contains(text, question) {
				t.Errorf("%s: loop=%s の本文の先頭:\n%s", c, ans, h)
			}
		}
	}

	// Go 版の手順（OS が分からないと sh と PowerShell の両方を出すので大きい）も 20 KiB より十分小さい
	dir := t.TempDir()
	e.s.cfg.DistDir = dir
	writeDist(t, dir, map[string]string{
		"looptrack_v1.0.0_darwin_arm64": "da", "looptrack_v1.0.0_darwin_amd64": "dx", "looptrack_v1.0.0_linux_amd64": "lx",
		"looptrack_v1.0.0_linux_arm64": "la", "looptrack_v1.0.0_windows_amd64.exe": "wx",
	})
	m := e.mcpAsClient(ed.token, hdr, "github-copilot-developer", "1", "2025-06-18")
	for _, goos := range []string{"darwin", "windows", ""} {
		if text, _ := m.call("setup", map[string]any{"os": goos}, false); len(text) > setupAskMax || !strings.Contains(head(text), question) {
			t.Errorf("Go 版（os=%q）の問い: %d バイト・先頭に問いがあるか %v", goos, len(text), strings.Contains(head(text), question))
		}
		text, _ := m.call("setup", map[string]any{"os": goos, "loop": "yes"}, false)
		limit := setupTextMax
		if goos == "" {
			limit = 2 * setupTextMax
		}
		if len(text) > limit || !strings.Contains(head(text), "AI への指示: 利用者の答え「loop を入れる」") {
			t.Errorf("Go 版（os=%q）: %d バイト（上限 %d）:\n%s", goos, len(text), limit, head(text))
		}
	}
}

func TestWorkspaceName(t *testing.T) {
	for in, want := range map[string]string{
		"/Users/a/tmp/abc-0123-codex": "abc-0123-codex", "/Users/a/tmp/abc-0123-codex/": "abc-0123-codex", " proj ": "proj",
		`C:\work\proj`: "proj", `C:\work\proj\`: "proj", "/": "", "": "", `C:\`: "", ".": "",
	} {
		if got := workspaceName(in); got != want {
			t.Errorf("workspaceName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSetupWorkspace は、setup が引数 workspace（作業ディレクトリの git のルート）で導入済みかを作業ディレクトリの単位で
// 判定することを確かめる（以前は別のディレクトリの導入済み通知で、空のディレクトリにも「導入済み」を返していた）。
// workspace を渡さない呼び方は従来どおり（利用者・プロジェクト・AI の単位）。
func TestSetupWorkspace(t *testing.T) {
	e, _, ed := newAPIEnv(t)
	withFakeDist(t, e)
	hdr := map[string]string{"X-Looptrack-Project": "req"}
	for _, c := range []struct{ client, agent string }{{"codex-mcp-client", "codex"}, {"claude-code", "claude-code"}} {
		m := e.mcpAsClient(ed.token, hdr, c.client, "1", "2025-06-18")
		var st installStateJSON
		body := installBody(c.agent, "hook", "server", "", map[string]any{"installed": false, "declined": true})
		body["host"], body["workspace"] = "mac", "im-setup-"+c.agent
		ed.json(200, "POST", "/projects/req/install", body, &st)
		if st.State != "current" {
			t.Fatalf("%s: 通知: %+v", c.agent, st)
		}
		// 導入済みの作業ディレクトリ（パス・末尾の「/」・名前だけ・Windows の区切り）: 従来どおり「導入済み」
		for _, ws := range []string{"/Users/a/tmp/im-setup-" + c.agent, "/Users/a/tmp/im-setup-" + c.agent + "/", "im-setup-" + c.agent, `C:\tmp\im-setup-` + c.agent} {
			text, data := m.call("setup", map[string]any{"workspace": ws}, false)
			if out := setupOf(t, data); out.Install.State != "current" || !strings.Contains(text, "導入済み") || data["workspace"] != "im-setup-"+c.agent {
				t.Errorf("%s: 導入済みの作業ディレクトリ %q: state=%s workspace=%v", c.agent, ws, out.Install.State, data["workspace"])
			}
		}
		// workspace を渡さない古い呼び方: 従来どおり
		if _, data := m.call("setup", map[string]any{}, false); setupOf(t, data).Install.State != "current" || data["workspace"] != nil {
			t.Errorf("%s: workspace なし: %v", c.agent, data["install"])
		}
		// 別の（空の）作業ディレクトリ: 未導入。loop の選択もこのディレクトリで問うので、1 回目は問いだけ
		ws := "/Users/a/tmp/abc-0123-" + c.agent
		text, data := m.call("setup", map[string]any{"workspace": ws}, false)
		out := setupOf(t, data)
		if out.Install.State != "missing" {
			t.Errorf("%s: 別の作業ディレクトリ: state=%s", c.agent, out.Install.State)
		}
		checkLoopAsk(t, c.agent+" 別の作業ディレクトリ", text, out)
		for _, want := range []string{"この作業ディレクトリ（abc-0123-" + c.agent + "）", "別の作業ディレクトリ im-setup-" + c.agent + "・mac", "同じ workspace"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s: 本文に %q が無い:\n%s", c.agent, want, text)
			}
		}
		if strings.Contains(text, "辞退済み") { // loop の選択は作業ディレクトリごと（このディレクトリでは未選択）
			t.Errorf("%s: 別の作業ディレクトリで loop の辞退を引き継いでいる", c.agent)
		}
		// 2 回目（答え・同じ workspace）: 取得 + init（--no-loop）の手順
		text, data = m.call("setup", map[string]any{"workspace": ws, "loop": "no"}, false)
		out = setupOf(t, data)
		if out.Install.State != "missing" || out.Ask != "" || !strings.Contains(out.Steps[0].Command, "--agent "+c.agent+" --source server --dist") ||
			!strings.HasSuffix(out.Steps[0].Command, " --no-loop") || !strings.Contains(text, "この作業ディレクトリ（abc-0123-"+c.agent+"）") {
			t.Errorf("%s: 別の作業ディレクトリの 2 回目: state=%s steps=%+v", c.agent, out.Install.State, out.Steps)
		}
		// 導入済み（辞退済み）の作業ディレクトリでは、loop を付けても問いも loop の手順も出ない
		text, data = m.call("setup", map[string]any{"workspace": "/Users/a/tmp/im-setup-" + c.agent, "loop": "yes"}, false)
		if out := setupOf(t, data); out.Install.State != "current" || out.Ask != "" || out.LoopAnswer != "" || len(out.Steps) != 1 ||
			!strings.Contains(text, "引数 loop=yes は使わない") {
			t.Errorf("%s: 辞退済みの作業ディレクトリに loop=yes: %+v\n%s", c.agent, out, text)
		}
	}

	// 通知に workspace が無い（送らない古い CLI）ときは比べられないので従来どおり
	m := e.mcpAsClient(ed.token, hdr, "github-copilot-developer", "1", "2025-06-18")
	ed.json(200, "POST", "/projects/req/install", installBody("copilot", "hook", "server", "", nil), nil)
	if _, data := m.call("setup", map[string]any{"workspace": "/x/other"}, false); setupOf(t, data).Install.State != "current" {
		t.Errorf("workspace の無い通知: %v", data["install"])
	}
}

// TestSetupCopilotEnv は Copilot 向けの手順のコマンドが、サーバの URL とプロジェクトを環境変数で前置していて、
// .claude/settings.json の env を使わない Copilot でもそのまま動くことを確かめる。Claude Code 向けには付けない。
func TestSetupCopilotEnv(t *testing.T) {
	e, _, ed := newAPIEnv(t)
	hdr := map[string]string{"X-Looptrack-Project": "req"}
	base := e.srv.URL + "/im"
	commands := func(out setupOut) []string {
		var cmds []string
		for _, s := range out.Steps {
			for _, c := range []string{s.Command, s.CommandWindows} {
				if c != "" {
					cmds = append(cmds, c)
				}
			}
		}
		return cmds
	}
	// 配布ディレクトリ: この OS 向けは本物の looptrack（取得 + init を実際に流す）、ほかは偽物
	bin, err := os.ReadFile(looptrackBin(t))
	if err != nil {
		t.Fatal(err)
	}
	dists := map[string]string{"looptrack_v1.0.0_darwin_arm64": "da", "looptrack_v1.0.0_windows_amd64.exe": "wx"}
	if runtime.GOOS != "windows" {
		dists["looptrack_v1.0.0_"+runtime.GOOS+"_"+runtime.GOARCH] = string(bin)
	}
	e.s.cfg.DistDir = t.TempDir()
	writeDist(t, e.s.cfg.DistDir, dists)
	goos := runtime.GOOS
	if goos == "windows" {
		goos = "darwin"
	}

	// looptrack は LOOPTRACK_* を読む。1 回目は loop の問いだけなので、答え（入れない）を付けて呼ぶ
	cp := e.mcpAsClient(ed.token, hdr, "github-copilot-developer", "1.0.86", "2025-06-18")
	text, data := cp.call("setup", map[string]any{"os": goos}, false)
	checkLoopAsk(t, "Copilot", text, setupOf(t, data))
	_, data = cp.call("setup", map[string]any{"os": goos, "loop": "no"}, false)
	out := setupOf(t, data)
	goEnv := "export LOOPTRACK_API_URL=" + base + " LOOPTRACK_PROJECT=req && "
	cmds := commands(out)
	if len(cmds) < 2 {
		t.Fatalf("手順: %+v", out.Steps)
	}
	for _, c := range cmds {
		if !strings.HasPrefix(c, goEnv) {
			t.Errorf("Copilot の手順のコマンドに環境変数が前置されていない: %s", c)
		}
	}
	cfgRe := regexp.MustCompile(`（(export [^（）]*? config) で確認）`)
	var cfg string
	for _, s := range out.Steps {
		if m := cfgRe.FindStringSubmatch(s.Title); m != nil {
			cfg = m[1]
		}
	}
	if cfg != goEnv+`"$HOME/.local/bin/looptrack" issue config` {
		t.Errorf("config の確認のコマンド: %q", cfg)
	}

	// Claude Code 向けには付けない（init が settings.json の env に置く）
	cc := e.mcpAsClient(ed.token, hdr, "claude-code", "2.1.0", "2025-06-18")
	_, data = cc.call("setup", map[string]any{"loop": "yes"}, false)
	for _, c := range commands(setupOf(t, data)) {
		if strings.Contains(c, "IM_API_URL=") || strings.Contains(c, "LOOPTRACK_API_URL=") {
			t.Errorf("Claude Code の手順に環境変数が前置されている: %s", c)
		}
	}

	// そのまま動く: 取得 + init（入れない）の後、手順の config の確認を LOOPTRACK_API_URL の無いシェルで実行できる
	if _, err := exec.LookPath("curl"); err == nil && runtime.GOOS != "windows" {
		proj, home := t.TempDir(), t.TempDir()
		sh := func(script string) cliResult {
			cmd := exec.Command("/bin/sh", "-c", script)
			cmd.Env = append(cliHomeEnv(home), "LOOPTRACK_TOKEN="+ed.token, "LOOPTRACK_USAGE=0")
			cmd.Dir = proj
			var so, se bytes.Buffer
			cmd.Stdout, cmd.Stderr = &so, &se
			err := cmd.Run()
			code := 0
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			}
			return cliResult{so.String(), se.String(), code}
		}
		if res := sh(out.Steps[0].Command); res.code != 0 {
			t.Fatalf("取得と init: %d\n%s\n%s", res.code, res.stdout, res.stderr)
		}
		if res := sh(cfg); res.code != 0 || strings.Contains(res.stderr, "がありません") {
			t.Errorf("config の確認: %d\n%s\n%s", res.code, res.stdout, res.stderr)
		}
		if res := sh(`"$HOME/.local/bin/looptrack" issue config`); res.code == 0 {
			t.Logf("前置なしの config も通った（Copilot の init が env を置く形になった？）: %s", res.stdout)
		}
	}

	// PowerShell は $env: で置く
	_, data = cp.call("setup", map[string]any{"os": "darwin", "loop": "yes"}, false)
	for _, c := range commands(setupOf(t, data)) {
		if !strings.HasPrefix(c, goEnv) {
			t.Errorf("Copilot の手順（macOS）: %s", c)
		}
	}
	_, data = cp.call("setup", map[string]any{"os": "windows", "loop": "yes"}, false)
	for _, c := range commands(setupOf(t, data)) {
		if !strings.HasPrefix(c, "$env:LOOPTRACK_API_URL='"+base+"'; $env:LOOPTRACK_PROJECT='req'; ") {
			t.Errorf("Copilot の手順（Windows）: %s", c)
		}
	}
}

// TestInstallSelfRepo: looptrack 自身のリポジトリ（kit の正本）を模した作業ディレクトリからの通知では、
// kit が配布物と違っても【配布スクリプトの更新】を出さない。手元の kit のほうが新しいのは当たり前で、
// 促される init の再実行はクライアントが「自身です」と拒否するので、案内と実際にできることが食い違う。
// 実行ファイルの古さは印があっても抑制しない（self-update は自分自身のリポジトリでも実行できる）。
func TestInstallSelfRepo(t *testing.T) {
	withLoopKit(t, loopFixture())
	e, _, ed := newAPIEnv(t)
	m := e.mcpAsClient(ed.token, map[string]string{"X-Looptrack-Project": "req"}, "claude-code", "2.1.0", "2025-06-18")
	post := func(body map[string]any) installStateJSON {
		t.Helper()
		var st installStateJSON
		ed.json(200, "POST", "/projects/req/install", body, &st)
		return st
	}
	loop := map[string]any{"installed": true, "bundle_sha256": strings.Repeat("1", 64), "version": "local-1"}
	core := strings.Repeat("0", 64)

	// 普通のプロジェクト（印なし）: core も loop も配布物と違うので更新を求める
	st := post(installBody("claude-code", "hook", "link", core, loop))
	if st.State != "stale" || strings.Join(st.StaleKit, ",") != "core,loop" || st.SelfRepo {
		t.Fatalf("印なし: %+v", st)
	}
	m.call("list_issues", map[string]any{}, false)
	if !strings.Contains(m.notice, "【配布スクリプトの更新】") {
		t.Fatalf("印なしの指示: %q", m.notice)
	}

	// looptrack 自身のリポジトリからの通知（self_repo）: 同じ中身でも kit は比べない
	body := installBody("claude-code", "hook", "link", core, loop)
	body["self_repo"] = true
	st = post(body)
	if st.State != "current" || len(st.StaleKit) != 0 || !st.SelfRepo || st.UpdateCommand != "" {
		t.Fatalf("self_repo の通知: %+v", st)
	}
	if !strings.Contains(st.Message, "kit は配布物と比べません") || strings.Contains(st.Message, "【配布スクリプトの更新】") {
		t.Errorf("self_repo の文面: %q", st.Message)
	}
	// ツール結果にも指示が付かない（setupNotice は DB の記録だけを見るので、印が残っていることの確認でもある）
	if text, _ := m.call("list_issues", map[string]any{}, false); m.notice != "" || strings.Contains(text, "【") {
		t.Errorf("self_repo の後に指示が付いた: %q", m.notice)
	}

	// 実行ファイルの古さは印があっても抑制しない（kit の比較だけを止める）
	e.s.cfg.ClientMinVersion = "v1.1.0"
	st = post(body)
	if st.State != "stale" || strings.Join(st.StaleFiles, ",") != "looptrack" || len(st.StaleKit) != 0 ||
		!strings.Contains(st.Message, "kit は配布物と比べません") {
		t.Errorf("実行ファイルの更新: %+v", st)
	}
}
