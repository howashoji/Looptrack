package core

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/client/hook/loop"
	"github.com/howashoji/looptrack/internal/hookio"
)

// TestHookLogCoreSamePlace は、core の hook も LOOPTRACK_LOOP_HOOK_LOG=1 のときだけ、loop の hook と同じ置き場
// （loop.HookLogPath）に 1 行を書くこと（置き場の解決を 2 か所に写さない）。設定しないときは何も作らない（対照）。
func TestHookLogCoreSamePlace(t *testing.T) {
	s := newSandbox(t)
	freshSetup(s)
	in := `{"hook_event_name":"Stop","session_id":"sess-core"}`
	var gotPath string
	run := func(extra map[string]string) {
		t.Helper()
		e := testEnv(s, extra)
		var stdout, stderr bytes.Buffer
		mainWith(context.Background(), "issue-freshness-check", []string{"--agent", "claude-code"}, strings.NewReader(in), &stdout, &stderr, e,
			func(o *hookio.RunOptions) {
				ev, _ := hookio.Parse([]byte(in), o.Parse)
				if o.LogPath != nil {
					gotPath = o.LogPath(ev)
				}
			})
	}
	want := filepath.Join(s.proj, ".claude", ".looptrack-freshness", "hook-log.jsonl")

	run(nil)
	if _, err := os.Stat(want); err == nil {
		t.Fatal("設定しないのに記録ができた")
	}
	run(map[string]string{hookio.HookLogEnv: "1"})
	if gotPath != want {
		t.Errorf("置き場 %q, want %q", gotPath, want)
	}
	b, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("設定したのに記録が無い: %v", err)
	}
	line := string(b)
	for _, w := range []string{`"hook":"issue-freshness-check"`, `"event":"Stop"`, `"session":"sess-core"`} {
		if !strings.Contains(line, w) {
			t.Errorf("記録に %s が無い: %s", w, line)
		}
	}
	ev, _ := hookio.Parse([]byte(in), hookio.ParseOptions{Getenv: testEnv(s, nil).Vars.Get, Getwd: func() string { return s.proj }})
	if p := loop.HookLogPath(ev, testEnv(s, nil).Vars.Get, func() string { return s.proj }); p != want {
		t.Errorf("loop.HookLogPath %q, want %q", p, want)
	}
}
