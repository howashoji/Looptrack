package session

import (
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/client/env"
)

// ケースは internal/clitest の headers/* の golden（以前の CLI の出力）と同じ。
func TestDetectLikeLegacyCLI(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		session string
		agent   string
	}{
		{"人", nil, "", ""},
		{"明示", map[string]string{"LOOPTRACK_SESSION_ID": "sess-explicit"}, "sess-explicit", ""},
		{"旧名の明示は読まない", map[string]string{"IM_SESSION_ID": "old"}, "", ""},
		{"Claude Code", map[string]string{"CLAUDE_CODE_SESSION_ID": "cc-session-1"}, "cc-session-1", ""},
		{"Claude の旧い名前", map[string]string{"CLAUDE_SESSION_ID": "cc-old"}, "cc-old", ""},
		{"Codex は THREAD を先に", map[string]string{"CODEX_THREAD_ID": "codex-thread-1", "CODEX_SESSION_ID": "codex-parent"}, "codex-thread-1", ""},
		{"Codex の親だけ", map[string]string{"CODEX_SESSION_ID": "codex-parent"}, "codex-parent", ""},
		{"明示が AI より先", map[string]string{"LOOPTRACK_SESSION_ID": "e", "CLAUDE_CODE_SESSION_ID": "c"}, "e", ""},
		{"Copilot CLI", map[string]string{"COPILOT_CLI": "1", "COPILOT_AGENT_SESSION_ID": "copilot-1"}, "copilot-1", "copilot"},
		{"COPILOT_CLI なしの ID は読まない", map[string]string{"COPILOT_AGENT_SESSION_ID": "copilot-1"}, "", ""},
		{"VS Code の Copilot", map[string]string{"AI_AGENT": "github_copilot_vscode_agent"}, "", "copilot"},
		{"COPILOT_AGENT=1", map[string]string{"COPILOT_AGENT": "1"}, "", "copilot"},
		{"Copilot の下の明示", map[string]string{"LOOPTRACK_SESSION_ID": "sess-explicit", "COPILOT_AGENT": "1"}, "sess-explicit", "copilot"},
		{"Claude の下の明示と Copilot の印", map[string]string{"LOOPTRACK_SESSION_ID": "e", "CLAUDE_CODE_SESSION_ID": "c", "COPILOT_AGENT": "1"}, "e", ""},
		{"TERM_PROGRAM だけは AI でない", map[string]string{"TERM_PROGRAM": "vscode"}, "", ""},
		// クラウド版（DESIGN.md §5-4「クラウド版の AI」）。クラウド固有の変数を足しても判定はローカルと同じ
		{"Copilot cloud agent", map[string]string{"COPILOT_CLI": "1", "COPILOT_AGENT_SESSION_ID": "cloud-1", "COPILOT_AGENT_ACTION": "task",
			"CI": "true", "GITHUB_ACTIONS": "true", "CLOUD_SESSION_STORE": "true"}, "cloud-1", "copilot"},
		{"Claude Code on the web", map[string]string{"CLAUDE_CODE_SESSION_ID": "cc-web-1", "CLAUDE_CODE_REMOTE": "true",
			"CLAUDE_CODE_REMOTE_SESSION_ID": "cse_web"}, "cc-web-1", ""},
		{"CLAUDE_CODE_REMOTE_SESSION_ID だけは読まない", map[string]string{"CLAUDE_CODE_REMOTE": "true", "CLAUDE_CODE_REMOTE_SESSION_ID": "cse_web"}, "", ""},
		// 器（デスクトップ版の窓）が CLAUDE_CODE_SESSION_ID を渡さず HOST だけを渡す場合。並行するセッションを見分けるのに使う
		{"器のセッション ID だけ", map[string]string{"CLAUDE_CODE_HOST_SESSION_ID": "host-1"}, "host-1", ""},
		{"CLAUDE_CODE_SESSION_ID が器より先", map[string]string{"CLAUDE_CODE_SESSION_ID": "cc-1", "CLAUDE_CODE_HOST_SESSION_ID": "host-1"}, "cc-1", ""},
		{"CLAUDE_SESSION_ID も器より先", map[string]string{"CLAUDE_SESSION_ID": "cc-old", "CLAUDE_CODE_HOST_SESSION_ID": "host-1"}, "cc-old", ""},
		{"明示が器より先", map[string]string{"LOOPTRACK_SESSION_ID": "e", "CLAUDE_CODE_HOST_SESSION_ID": "host-1"}, "e", ""},
	}
	for _, c := range cases {
		m := Detect(env.FromMap(c.env))
		if m.Session != c.session || m.Agent != c.agent {
			t.Errorf("%s: %+v（期待 session=%q agent=%q）", c.name, m, c.session, c.agent)
		}
	}
}

func TestHeaders(t *testing.T) {
	h := Mark{Session: strings.Repeat("あ", 200), Agent: "copilot"}.Headers()
	if got := []rune(h["X-Looptrack-Session"]); len(got) != MaxSessionLen {
		t.Errorf("X-Looptrack-Session を 128 文字で切らない: %d", len(got))
	}
	if h["X-Looptrack-Agent"] != "copilot" {
		t.Error("X-Looptrack-Agent")
	}
	if _, ok := h["X-Looptrack-Session-Kind"]; ok {
		t.Error("会話のセッション ID に種類のヘッダを付けた")
	}
	if len((Mark{}).Headers()) != 0 {
		t.Error("人の操作にヘッダを付けた")
	}
}

// 器のセッション ID（CLAUDE_CODE_HOST_SESSION_ID）だけの窓は、種類の印も送る。
// サーバはこれでトークン情報の付与の対象から外す（付けようがないため。DESIGN.md §9-5）。
// 判定の表（環境変数 4 通り × 送るヘッダ・付与の対象・会話記録）は internal/server の TestHostSessionKindTable。
func TestDetectHostSessionKind(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		kind string
	}{
		{"器のセッション ID だけ", map[string]string{"CLAUDE_CODE_HOST_SESSION_ID": "host-1"}, KindHost},
		{"会話のセッション ID", map[string]string{"CLAUDE_CODE_SESSION_ID": "cc-1"}, ""},
		{"両方あれば会話のほうを使うので印は付けない", map[string]string{"CLAUDE_CODE_SESSION_ID": "cc-1", "CLAUDE_CODE_HOST_SESSION_ID": "host-1"}, ""},
		{"明示の LOOPTRACK_SESSION_ID が勝つときは付けない", map[string]string{"LOOPTRACK_SESSION_ID": "e", "CLAUDE_CODE_HOST_SESSION_ID": "host-1"}, ""},
		{"どちらも無し", map[string]string{}, ""},
	}
	for _, c := range cases {
		m := Detect(env.FromMap(c.env))
		if m.Kind != c.kind {
			t.Errorf("%s: Kind = %q（期待 %q）", c.name, m.Kind, c.kind)
		}
		h := m.Headers()
		if h["X-Looptrack-Session-Kind"] != c.kind {
			t.Errorf("%s: X-Looptrack-Session-Kind = %q（期待 %q）", c.name, h["X-Looptrack-Session-Kind"], c.kind)
		}
	}
}
