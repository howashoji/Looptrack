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

// TestSummaryUsageFailure は、この PC のフックの送信の失敗（usage-failure.json）と再送待ち（usage-spool）が
// 要約の 1 行に出ること。対照として、どちらも無ければ何も出さないことを同じテストで見る。
func TestSummaryUsageFailure(t *testing.T) {
	home := t.TempDir()
	e := env.FromMap(map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, "cfg"), "APPDATA": filepath.Join(home, "appdata"),
		"LOOPTRACK_API_URL": "https://issues.example.invalid/im"})
	dir := usagesnap.StateDir(e)
	if dir == "" {
		t.Fatal("置き場が分かりません（前提が崩れています）")
	}
	jst := time.FixedZone("JST", 9*3600)
	text := func() string { return usageFailureText(i18n.JA, localUsageFailure(e, "demo"), jst) }

	if got := localUsageFailure(e, "demo"); got != nil {
		t.Fatalf("何も無ければ nil: %v", got)
	}
	if got := text(); got != "" {
		t.Errorf("何も無ければ出さない: %q", got)
	}

	// 退避だけ（失敗の記録は無い）。数えるのはこのプロジェクトの置き場だけ（ほかのプロジェクト・鍵の無い以前の置き場は
	// ここでは再送しないので数えない）
	spool := usagesnap.SpoolDir(dir, e.Value(env.APIURL), "demo")
	other := usagesnap.SpoolDir(dir, e.Value(env.APIURL), "other")
	legacy := filepath.Join(dir, usagesnap.SpoolDirName)
	if spool == "" || other == "" || spool == other {
		t.Fatalf("プロジェクトごとの置き場が分かりません（前提が崩れています）: %q %q", spool, other)
	}
	for _, d := range []string{spool, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{"1700000000-00000001.json", "1700000100-00000002.json", ".hidden.json"} {
		if err := os.WriteFile(filepath.Join(spool, n), []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{filepath.Join(other, "1700000200-00000003.json"), filepath.Join(legacy, "1700000300-00000004.json")} {
		if err := os.WriteFile(p, []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := text(); got != "この PC に未送信のトークン情報が 2 件あります（次に送れたときに再送します）" {
		t.Errorf("退避だけ: %q", got)
	}

	// 届かない失敗が 2 回続いた
	at := time.Date(2026, 9, 26, 1, 2, 3, 0, time.UTC)
	usagesnap.RecordFailure(dir, at.Add(-time.Minute), usagesnap.FailUnreachable, 0)
	usagesnap.RecordFailure(dir, at, usagesnap.FailUnreachable, 0)
	want := "この PC のフックがトークン情報を送れていません（最後の失敗 2026-09-26 10:02・続けて 2 回・サーバに届かない）。未送信 2 件は、次に送れたときに再送します"
	if got := text(); got != want {
		t.Errorf("届かない:\n got  %q\n want %q", got, want)
	}

	// 拒否（再送しない。付ける手段を案内する）
	usagesnap.RecordFailure(dir, at, usagesnap.FailRejected, 403)
	if got := text(); !strings.Contains(got, "サーバが拒否（HTTP 403）") || !strings.Contains(got, "looptrack issue usage attach <ID>") ||
		!strings.Contains(got, "続けて 3 回") {
		t.Errorf("拒否: %q", got)
	}
	// 知らないキー（新しい版が書いたもの）はそのまま出す
	usagesnap.RecordFailure(dir, at, "future_reason", 0)
	if got := text(); !strings.Contains(got, "future_reason") {
		t.Errorf("知らないキー: %q", got)
	}

	// 送信に成功したら記録は消え、退避も無くなれば何も出さない
	usagesnap.ClearFailure(dir)
	if err := os.RemoveAll(spool); err != nil { // ほかのプロジェクト・以前の置き場の分は残したまま
		t.Fatal(err)
	}
	if got := text(); got != "" {
		t.Errorf("成功の後: %q", got)
	}
}
