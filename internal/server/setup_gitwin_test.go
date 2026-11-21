package server

import (
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
)

// TestSetupGitForWindowsStep: Windows の Claude Code への導入の手順（未導入・古い・承認待ち）に、Git for Windows が
// looptrack issue verify に要ること（フックは無くても動くこと）の 1 段が付く。対照として、macOS・Codex・導入済みには付かない
// （付く側だけを見ると、条件が常に真でも通るため）。日英の両方の文面を見る。
func TestSetupGitForWindowsStep(t *testing.T) {
	win := &goSetup{lang: i18n.JA, os: "windows", missing: "x", local: true}
	mac := &goSetup{lang: i18n.JA, os: "darwin", missing: "x", local: true}
	has := func(steps []setupStepJSON, word string) bool {
		for _, s := range steps {
			if strings.Contains(s.Title, word) {
				return true
			}
		}
		return false
	}
	for _, state := range []string{"missing", "stale", "no_hook"} {
		steps := setupStepsFor(i18n.JA, agentClaudeCode, "demo", "http://127.0.0.1:18090/looptrack", "", installStateJSON{State: state}, distLatest{}, win, "no")
		if !has(steps, "Git for Windows") || !has(steps, "フックは無くても動く") {
			t.Errorf("%s: Windows の Claude Code の手順に Git for Windows の段が無い: %+v", state, steps)
		}
		en := setupStepsFor(i18n.EN, agentClaudeCode, "demo", "http://127.0.0.1:18090/looptrack", "", installStateJSON{State: state}, distLatest{}, win, "no")
		if !has(en, "Git for Windows") || !has(en, "Hooks work without it") {
			t.Errorf("%s: 英語の手順に Git for Windows の段が無い: %+v", state, en)
		}
		if steps := setupStepsFor(i18n.JA, agentClaudeCode, "demo", "http://127.0.0.1:18090/looptrack", "", installStateJSON{State: state}, distLatest{}, mac, "no"); has(steps, "Git for Windows") {
			t.Errorf("%s: macOS の手順に Git for Windows の段を付けた", state)
		}
		if steps := setupStepsFor(i18n.JA, agentCodex, "demo", "http://127.0.0.1:18090/looptrack", "", installStateJSON{State: state}, distLatest{}, win, "no"); has(steps, "Git for Windows") {
			t.Errorf("%s: Codex の手順に Git for Windows の段を付けた", state)
		}
	}
	if steps := setupStepsFor(i18n.JA, agentClaudeCode, "demo", "http://127.0.0.1:18090/looptrack", "", installStateJSON{State: "current"}, distLatest{}, win, "no"); has(steps, "Git for Windows") {
		t.Errorf("導入済みに Git for Windows の段を付けた")
	}
}
