package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/usagesnap"
	"github.com/howashoji/looptrack/internal/i18n"
)

// TestSummaryTranscriptState は、会話記録の自動送信の鍵の状態と、この PC の hook の失敗・退避が要約に出ること。
// 鍵: 覚えたルールが入で端末のオプトインも入なら「入」の 1 行、オプトインが切なら環境変数の名前を挙げた 1 行。
// ルールが切・覚えていない・失敗も退避も無いときは何も出さない（対照として同じテストで見る）。
func TestSummaryTranscriptState(t *testing.T) {
	home := t.TempDir()
	vars := map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, "cfg"), "APPDATA": filepath.Join(home, "appdata"),
		"LOOPTRACK_API_URL": "https://issues.example.invalid/im"}
	jst := time.FixedZone("JST", 9*3600)
	lines := func() []string {
		return transcriptStateText(i18n.JA, localTranscriptState(env.FromMap(vars), "demo"), jst)
	}
	if got := lines(); len(got) != 0 {
		t.Fatalf("ルールを覚えていなければ何も出さない: %q", got)
	}
	ruleEnv := env.Overlay(env.FromMap(vars), map[string]string{"LOOPTRACK_PROJECT": "demo"})
	usagesnap.RememberTranscriptsRule(false, usagesnap.Options{Env: ruleEnv})
	vars["LOOPTRACK_SESSION_TRANSCRIPTS"] = "1"
	if got := lines(); len(got) != 0 {
		t.Fatalf("ルールが切なら何も出さない: %q", got)
	}
	usagesnap.RememberTranscriptsRule(true, usagesnap.Options{Env: ruleEnv})
	if got := lines(); len(got) != 1 || !strings.Contains(got[0], "会話記録の自動送信: 入") {
		t.Fatalf("両方が入なら「入」の 1 行: %q", got)
	}
	vars["LOOPTRACK_SESSION_TRANSCRIPTS"] = "0"
	if got := lines(); len(got) != 1 || !strings.Contains(got[0], "LOOPTRACK_SESSION_TRANSCRIPTS=1 を設定") {
		t.Fatalf("ルールが入でオプトインが切なら、環境変数の名前を挙げた 1 行: %q", got)
	}
	// 失敗（変換の止め）と退避
	dir := usagesnap.StateDir(env.FromMap(vars))
	usagesnap.RecordTranscriptFailure(dir, time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC), usagesnap.FailTranscriptRejected, 0, 2)
	spool := usagesnap.TranscriptSpoolDir(dir, vars["LOOPTRACK_API_URL"], "demo")
	if err := os.MkdirAll(spool, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spool, "claude-code-s1.json"), []byte(`{"agent":"claude-code","session_id":"s1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got := lines()
	if len(got) != 2 || !strings.Contains(got[1], "会話記録を送れていません") || !strings.Contains(got[1], "ファイルが 2 個") ||
		!strings.Contains(got[1], "未送信 1 件") || !strings.Contains(got[1], "2026-01-02 12:04") {
		t.Fatalf("失敗の 1 行（件数・時刻・理由）: %q", got)
	}
	usagesnap.ClearTranscriptFailure(dir)
	vars["LOOPTRACK_SESSION_TRANSCRIPTS"] = "1"
	got = lines()
	if len(got) != 2 || !strings.Contains(got[1], "未送信の会話記録が 1 件") {
		t.Fatalf("退避だけの 1 行: %q", got)
	}
	// 英語の文面も同じ数だけ出る
	if en := transcriptStateText(i18n.EN, localTranscriptState(env.FromMap(vars), "demo"), jst); len(en) != 2 || !strings.Contains(en[0], "Automatic transcript sending: on") {
		t.Fatalf("英語: %q", en)
	}
}
