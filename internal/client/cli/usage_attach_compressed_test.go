package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/i18n"
)

// 圧縮済みの Codex の rollout の件数は、手で打つ usage attach では出し、変更操作の後の自動の付与（quiet）では
// LOOPTRACK_USAGE_DEBUG のときだけ出す（操作のたびに標準エラーを汚さない）。対照は同じ環境の非 quiet（件数が出る）。
// 親の会話の ID（CODEX_SESSION_ID）だけがあるときは、親のスレッドの圧縮済みを今の会話のものとして数えない。
func TestUsageAttachCompressedNoticeQuietBranch(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, "codex")
	const tid = "0199aaaa-1111-7222-8333-444455556666"
	day := filepath.Join(codexHome, "sessions", "2026", "09", "18")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(day, "rollout-2026-09-18T01-00-00-"+tid+".jsonl.zst"), []byte("not-really-zstd"), 0o644); err != nil {
		t.Fatal(err)
	}
	wantNotice := i18n.TN(i18n.JA, "cli.usage.codex_compressed", 1, "count", 1)
	if !strings.Contains(wantNotice, "圧縮") {
		t.Fatalf("前提が崩れています: 通知の文面を取れません: %q", wantNotice)
	}
	attach := func(vars map[string]string, quiet bool) string {
		t.Helper()
		m := map[string]string{"HOME": home, "USERPROFILE": home, "CODEX_HOME": codexHome, "LOOPTRACK_LANG": "ja",
			"LOOPTRACK_API_URL": "https://issues.example.invalid/im", "LOOPTRACK_PROJECT": "demo"}
		for k, v := range vars {
			m[k] = v
		}
		var errb bytes.Buffer
		c := &Ctx{IO: IO{Stdout: &bytes.Buffer{}, Stderr: &errb}, Env: env.FromMap(m), Root: t.TempDir(), Lang: i18n.JA}
		if _, _, err := c.usageAttach("DEMO-0001", "", "manual", quiet); err != nil {
			t.Fatalf("usageAttach: %v", err)
		}
		return errb.String()
	}
	thread := map[string]string{"CODEX_THREAD_ID": tid}

	if got := attach(thread, false); !strings.Contains(got, wantNotice) {
		t.Fatalf("前提が崩れています: 非 quiet でも件数が出ません: %q", got)
	}
	if got := attach(thread, true); got != "" {
		t.Errorf("quiet で debug でないのに出ています: %q", got)
	}
	debug := map[string]string{"CODEX_THREAD_ID": tid, "LOOPTRACK_USAGE_DEBUG": "1"}
	if got := attach(debug, true); !strings.Contains(got, wantNotice) {
		t.Errorf("quiet でも debug なら件数が出るはず: %q", got)
	}
	parentOnly := map[string]string{"CODEX_SESSION_ID": tid}
	if got := attach(parentOnly, false); strings.Contains(got, wantNotice) {
		t.Errorf("親の会話の ID だけなのに、そのスレッドの圧縮済みを数えています: %q", got)
	}
}
