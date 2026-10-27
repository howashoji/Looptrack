package usagesnap

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/client/env"
)

// ルール session.transcripts の覚えた値はプロジェクトごと（API の URL + slug）で、無ければ ok が false。
// オプトインは LOOPTRACK_SESSION_TRANSCRIPTS が前後の空白を除いて "1" のときだけ。
func TestTranscriptsRuleRemembered(t *testing.T) {
	home := t.TempDir()
	vars := map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "APPDATA": filepath.Join(home, "AppData"),
		"LOOPTRACK_API_URL": "https://example.invalid/lt", "LOOPTRACK_PROJECT": "demo"}
	o := Options{Env: env.FromMap(vars)}
	if _, _, ok := TranscriptsRule(o); ok {
		t.Fatal("覚えていないのに値があります")
	}
	RememberTranscriptsRule(true, o)
	if on, at, ok := TranscriptsRule(o); !ok || !on || at.IsZero() {
		t.Fatalf("入を覚えていません: %v %v %v", on, at, ok)
	}
	RememberTranscriptsRule(false, o)
	if on, _, ok := TranscriptsRule(o); !ok || on {
		t.Fatalf("切を覚えていません: %v %v", on, ok)
	}
	vars["LOOPTRACK_PROJECT"] = "other"
	if _, _, ok := TranscriptsRule(Options{Env: env.FromMap(vars)}); ok {
		t.Fatal("別のプロジェクトの値を読んでいます")
	}
	for v, want := range map[string]bool{"1": true, " 1 ": true, "0": false, "": false, "true": false} {
		if got := TranscriptsOptIn(env.FromMap(map[string]string{"LOOPTRACK_SESSION_TRANSCRIPTS": v})); got != want {
			t.Errorf("LOOPTRACK_SESSION_TRANSCRIPTS=%q: %v（%v のはず）", v, got, want)
		}
	}
}

// 会話記録の失敗は使用量の失敗とは別のファイルに残り、片方を消してももう片方は残る。退避と印の置き場はプロジェクトごと。
func TestTranscriptFailureSeparateFromUsage(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	RecordTranscriptFailure(dir, at, FailTranscriptRejected, 0, 2)
	RecordTranscriptFailure(dir, at, FailTranscriptRejected, 0, 2)
	if f := ReadTranscriptFailure(dir); f == nil || f.Count != 2 || f.Files != 2 || f.Reason != FailTranscriptRejected {
		t.Fatalf("会話記録の失敗: %+v", f)
	}
	if f := ReadFailure(dir); f != nil {
		t.Fatalf("使用量の失敗に書いています: %+v", f)
	}
	RecordFailure(dir, at, FailUnreachable, 0)
	ClearTranscriptFailure(dir)
	if ReadTranscriptFailure(dir) != nil || ReadFailure(dir) == nil {
		t.Fatal("会話記録の失敗だけを消すはず")
	}
	a, b := TranscriptSpoolDir(dir, "https://example.invalid/lt", "a"), TranscriptSpoolDir(dir, "https://example.invalid/lt", "b")
	if a == "" || a == b || TranscriptRejectedDir(dir, "https://example.invalid/lt", "a") == a || TranscriptSpoolDir(dir, "", "a") != "" {
		t.Fatalf("置き場: %q %q", a, b)
	}
}
