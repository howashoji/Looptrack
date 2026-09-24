package loop_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/howashoji/looptrack/internal/client/hook/core" // summary を合成の部分に登録する
	"github.com/howashoji/looptrack/internal/client/hook/loop"
	"github.com/howashoji/looptrack/internal/hookio"
)

// TestSessionStartCombined は Copilot 向けの合成の SessionStart: summary と loop の SessionStart の文脈を 1 回の出力にまとめる。
func TestSessionStartCombined(t *testing.T) {
	summaryBody := `{"counts":{"open":1,"open_bugs":1},"in_progress":[],"in_review":[],"ready":[{"id":"TST-0002","project":"tst","title":"保存でエラーになる",` +
		`"type":"bug","status":"Todo","priority":"P0","parent":"","labels":[],"blocked_by":[],"traces":[],"refs":[],"created":"2024-05-01 10:00",` +
		`"updated":"2024-05-02 11:30","closed":false,"version":3}],"ready_total":1}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/summary"):
			io.WriteString(w, summaryBody)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/install"):
			io.WriteString(w, `{"state":"current","message":"導入済み（最新）"}`)
		default:
			w.WriteHeader(404)
			io.WriteString(w, `{"error":{"code":"not_found","message":"ありません"}}`)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	proj := filepath.Join(dir, "proj")
	vars := map[string]string{
		"HOME": dir, "USERPROFILE": dir, "XDG_CONFIG_HOME": filepath.Join(dir, "xdg"),
		"LOOPTRACK_API_URL": srv.URL + "/looptrack", "LOOPTRACK_PROJECT": "tst", "LOOPTRACK_TOKEN": "test-token",
	}
	env := &loop.Env{Getenv: func(k string) string { return vars[k] }, Environ: func() []string { return nil }, Getwd: func() string { return proj }}
	// cwd は JSON の文字列として埋める（Windows のパスの「\」を連結で埋めると不正な JSON になり、hook が何もしない）
	inb, _ := json.Marshal(map[string]any{"hook_event_name": "SessionStart", "session_id": "s1", "cwd": proj, "source": "new"})
	input := string(inb)
	run := func(name string, args ...string) (map[string]any, string) {
		t.Helper()
		var out bytes.Buffer
		if code := loop.Main(context.Background(), name, args, strings.NewReader(input), &out, io.Discard, env); code != 0 {
			t.Fatalf("%s: 終了コード %d", name, code)
		}
		if out.Len() == 0 {
			return nil, ""
		}
		var m map[string]any
		if err := json.Unmarshal(out.Bytes(), &m); err != nil {
			t.Fatalf("%s: JSON ではない: %q", name, out.String())
		}
		ctx, _ := m["additionalContext"].(string)
		return m, ctx
	}

	_, rules := run("session-start-rules", "--agent", "copilot")
	if rules == "" {
		t.Fatal("対照: session-start-rules が文脈を出さない")
	}
	m, got := run(loop.CombinedSessionStart, "--agent", "copilot", "--parts", "summary,session-start-rules,session-start-memories,no-such-hook,session-start")
	if !strings.Contains(got, "TST-0002") {
		t.Errorf("summary の文脈が無い: %q", got)
	}
	if !strings.Contains(got, strings.TrimSpace(rules)) {
		t.Errorf("rules の文脈が無い: %q", got)
	}
	if i, j := strings.Index(got, "TST-0002"), strings.Index(got, strings.TrimSpace(rules)); i < 0 || j < 0 || i > j {
		t.Errorf("--parts の順（summary → rules）でつなぐはず: %q", got)
	}
	hs, _ := m["hookSpecificOutput"].(map[string]any)
	if hs == nil || hs["additionalContext"] != got || hs["hookEventName"] != "SessionStart" {
		t.Errorf("Copilot の形（トップレベルと hookSpecificOutput の両方）で出すはず: %v", m)
	}

	// summary が失敗しても（API に繋がらない）残りは出す（fail-open）
	vars["LOOPTRACK_API_URL"] = "http://127.0.0.1:1/looptrack"
	_, got = run(loop.CombinedSessionStart, "--agent", "copilot", "--parts", "summary,session-start-rules")
	if got == "" || strings.Contains(got, "TST-0002") || !strings.Contains(got, strings.TrimSpace(rules)) {
		t.Errorf("summary が失敗したら rules だけ: %q", got)
	}

	// 部分が無ければ何も出さない
	if m, _ := run(loop.CombinedSessionStart, "--agent", "copilot"); m != nil {
		t.Errorf("--parts なしは何も出さないはず: %v", m)
	}

	// Claude Code 向けの配線を Copilot が起動したときは、合成の hook も何もしない
	vars["COPILOT_PROJECT_DIR"] = proj
	if m, _ := run(loop.CombinedSessionStart, "--agent", "claude-code", "--parts", "session-start-rules"); m != nil {
		t.Errorf("Copilot から起動された claude-code の配線は何も出さないはず: %v", m)
	}
}

func TestCombinedParts(t *testing.T) {
	parts, rest := loop.CombinedParts([]string{"--limit", "5", "--parts", "summary, session-start-rules,", "--parts=session-start-iteration", "x"})
	if strings.Join(parts, ",") != "summary,session-start-rules,session-start-iteration" || strings.Join(rest, " ") != "--limit 5 x" {
		t.Errorf("parts %v rest %v", parts, rest)
	}
	if _, ok := loop.Lookup(loop.CombinedSessionStart); !ok {
		t.Error("合成の hook は Lookup で引ける")
	}
	for _, n := range loop.Names() {
		if n == loop.CombinedSessionStart {
			t.Error("合成の hook は Names（manifest の 12 本）に出さない")
		}
	}
}

// TestUserPromptCombined は Copilot 向けの合成の UserPromptSubmit: user-prompt-rules と user-prompt-task-mode の文脈を
// 1 回の出力にまとめる。Copilot の Stop の差し戻し（先頭が「[Stop hook の差し戻し]」）では task-mode が判定しない。
func TestUserPromptCombined(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "proj")
	// 日本語の文面を検査するので言語を固定する（指定が無いと端末の LANG に従い、日本語でなければ英語が出る）
	vars := map[string]string{"HOME": dir, "USERPROFILE": dir, "XDG_CONFIG_HOME": filepath.Join(dir, "xdg"),
		"LOOPTRACK_LOOP_STATE_DIR": filepath.Join(dir, "state"), "LOOPTRACK_LANG": "ja"}
	env := &loop.Env{Getenv: func(k string) string { return vars[k] }, Environ: func() []string { return nil }, Getwd: func() string { return proj }}
	run := func(prompt, name string, args ...string) (map[string]any, string) {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "s1", "cwd": proj, "prompt": prompt})
		var out bytes.Buffer
		if code := loop.Main(context.Background(), name, args, bytes.NewReader(b), &out, io.Discard, env); code != 0 {
			t.Fatalf("%s: 終了コード %d", name, code)
		}
		if out.Len() == 0 {
			return nil, ""
		}
		var m map[string]any
		if err := json.Unmarshal(out.Bytes(), &m); err != nil {
			t.Fatalf("%s: JSON ではない: %q", name, out.String())
		}
		ctx, _ := m["additionalContext"].(string)
		return m, ctx
	}
	const parts = "user-prompt-rules,user-prompt-task-mode,session-scope-guard"

	_, rules := run("x", "user-prompt-rules", "--agent", "copilot")
	rules = strings.TrimSpace(rules)
	if rules == "" {
		t.Fatal("対照: user-prompt-rules が文脈を出さない")
	}

	// 個別の配線では Copilot CLI が最後の 1 つ（task-mode）しか渡さない。合成すると両方が 1 つの additionalContext に入る
	m, got := run("この不具合を確認して", loop.CombinedUserPrompt, "--agent", "copilot", "--parts", parts)
	if !strings.Contains(got, rules) || !strings.Contains(got, "[タスクモード] 確認モードに設定") {
		t.Errorf("rules と task-mode の両方が入るはず: %q", got)
	}
	if i, j := strings.Index(got, rules), strings.Index(got, "[タスクモード]"); i < 0 || j < 0 || i > j {
		t.Errorf("--parts の順（rules → task-mode）でつなぐはず: %q", got)
	}
	hs, _ := m["hookSpecificOutput"].(map[string]any)
	if hs == nil || hs["additionalContext"] != got || hs["hookEventName"] != "UserPromptSubmit" {
		t.Errorf("Copilot の形（トップレベルと hookSpecificOutput の両方）で出すはず: %v", m)
	}

	// Copilot の Stop の差し戻し（次の利用者のメッセージとして渡る）: task-mode は判定も注入もしない＝ rules だけ
	_, got = run(hookio.CopilotStopMarker+" 引き継ぎを更新してください", loop.CombinedUserPrompt, "--agent", "copilot", "--parts", parts)
	if got != rules {
		t.Errorf("Stop の差し戻しでは rules だけ（task-mode は何も出さない）のはず: %q", got)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "state", "task-mode.d", "s1")); err != nil || strings.TrimSpace(string(b)) != "investigate" {
		t.Errorf("記録は確認モードのまま（「更新して」で実行モードに切り替えない）: %q %v", b, err)
	}

	// Claude Code 向けの配線を Copilot が起動したときは何もしない
	vars["COPILOT_PROJECT_DIR"] = proj
	if m, _ := run("確認して", loop.CombinedUserPrompt, "--agent", "claude-code", "--parts", parts); m != nil {
		t.Errorf("Copilot から起動された claude-code の配線は何も出さないはず: %v", m)
	}

	if _, ok := loop.Lookup(loop.CombinedUserPrompt); !ok || !loop.IsCombined(loop.CombinedUserPrompt) {
		t.Error("user-prompt は Lookup で引ける合成の hook")
	}
	for _, n := range loop.Names() {
		if loop.IsCombined(n) {
			t.Errorf("合成の hook %s は Names（manifest の 12 本）に出さない", n)
		}
	}
}
