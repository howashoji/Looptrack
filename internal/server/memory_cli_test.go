package server

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/howashoji/looptrack/internal/client/cli"
	clienv "github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/store"
)

// looptrack memory（internal/client/cli/memory.go）を、テストのサーバに向けて動かす。2 つの端末（別のラベルと別のセッション ID）が
// 追記した節が両方残り、古い --version の replace は終了コード 1 で何も書かないことを確かめる（対照は今の版の replace）。
func TestMemoryCLI(t *testing.T) {
	e := newEnv(t)
	pr := e.project("memc")
	u := e.user("memc-ed", "editor-password-1", "member")
	if err := store.SetMember(context.Background(), e.db, pr.ID, u.ID, "editor"); err != nil {
		t.Fatal(err)
	}
	tok := e.apiAs(u).token
	machine := func(label, session string) func(stdin string, args ...string) (int, string, string) {
		home := t.TempDir()
		vars := map[string]string{"LOOPTRACK_API_URL": e.srv.URL + "/im", "LOOPTRACK_PROJECT": "memc", "LOOPTRACK_TOKEN": tok, "LOOPTRACK_LANG": "ja",
			"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "APPDATA": filepath.Join(home, "AppData"),
			"CLAUDE_PROJECT_DIR": t.TempDir(), "LOOPTRACK_MACHINE_LABEL": label, "LOOPTRACK_SESSION_ID": session}
		return func(stdin string, args ...string) (int, string, string) {
			var out, errb bytes.Buffer
			code := cli.MemoryMain(args, cli.IO{Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errb}, clienv.FromMap(vars))
			return code, out.String(), errb.String()
		}
	}
	a, b := machine("box-a", "cli-session-a"), machine("box-b", "cli-session-b")
	if code, out, errs := a("## A の節\n端末 A から", "append", "handoff"); code != 0 || !strings.Contains(out, "handoff/main") {
		t.Fatalf("A の追記: %d %q %q", code, out, errs)
	}
	if code, out, errs := b("## B の節\n端末 B から", "append", "handoff"); code != 0 {
		t.Fatalf("B の追記: %d %q %q", code, out, errs)
	}
	code, out, errs := a("", "show", "handoff")
	if code != 0 || !strings.Contains(out, "端末 A から") || !strings.Contains(out, "端末 B から") || !strings.Contains(errs, "2") {
		t.Fatalf("show: %d %q %q", code, out, errs)
	}
	code, out, _ = a("", "show", "handoff", "--history")
	if code != 0 || !strings.Contains(out, "box-a") || !strings.Contains(out, "box-b") {
		t.Fatalf("show --history: %d %q", code, out)
	}
	// 古い版の replace は 1 で、本文は変わらない
	if code, _, errs := a("書き直し", "replace", "handoff", "--version", "1"); code != 1 || !strings.Contains(errs, "版が違います") {
		t.Errorf("古い版の replace: %d %q", code, errs)
	}
	if _, out, _ := a("", "show", "handoff"); !strings.Contains(out, "端末 B から") {
		t.Errorf("拒まれた replace の後の本文: %q", out)
	}
	// 対照: 今の版なら通る
	if code, out, errs := a("書き直し", "replace", "handoff", "--version", "2"); code != 0 || !strings.Contains(out, "3") {
		t.Errorf("今の版の replace: %d %q %q", code, out, errs)
	}
	var sessions []string
	rows, err := e.db.Query("SELECT session_id FROM memory_versions ORDER BY version")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		sessions = append(sessions, s)
	}
	rows.Close()
	if strings.Join(sessions, ",") != "cli-session-a,cli-session-b,cli-session-a" {
		t.Errorf("履歴のセッション: %v", sessions)
	}
	if code, out, _ := a("", "list"); code != 0 || !strings.Contains(out, "handoff/main") {
		t.Errorf("list: %d %q", code, out)
	}
}

// 2 つの別々のディレクトリから同時に looptrack memory append を実行しても、両方の節が残る。
func TestMemoryCLIConcurrentDirectories(t *testing.T) {
	e := newEnv(t)
	pr := e.project("memd")
	u := e.user("memd-ed", "editor-password-1", "member")
	if err := store.SetMember(context.Background(), e.db, pr.ID, u.ID, "editor"); err != nil {
		t.Fatal(err)
	}
	tok := e.apiAs(u).token
	dirA, dirB := t.TempDir(), t.TempDir()
	if dirA == dirB {
		t.Fatal("前提が崩れています: ディレクトリが同じ")
	}
	run := func(dir, label, stdin string, args ...string) (int, string, string) {
		home := filepath.Join(dir, "home")
		vars := map[string]string{"LOOPTRACK_API_URL": e.srv.URL + "/im", "LOOPTRACK_PROJECT": "memd", "LOOPTRACK_TOKEN": tok, "LOOPTRACK_LANG": "ja",
			"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "APPDATA": filepath.Join(home, "AppData"),
			"CLAUDE_PROJECT_DIR": dir, "LOOPTRACK_MACHINE_LABEL": label, "LOOPTRACK_SESSION_ID": "session-" + label}
		var out, errb bytes.Buffer
		code := cli.MemoryMain(args, cli.IO{Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errb}, clienv.FromMap(vars))
		return code, out.String(), errb.String()
	}
	const rounds = 5
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, w := range []struct{ dir, label string }{{dirA, "box-a"}, {dirB, "box-b"}} {
		wg.Add(1)
		go func(dir, label string) {
			defer wg.Done()
			<-start
			for i := 0; i < rounds; i++ {
				if code, out, errs := run(dir, label, fmt.Sprintf("## %s %d\n節 %s-%d", label, i, label, i), "append", "handoff"); code != 0 {
					t.Errorf("%s %d: %d %q %q", label, i, code, out, errs)
				}
			}
		}(w.dir, w.label)
	}
	close(start)
	wg.Wait()
	code, out, errs := run(dirA, "box-a", "", "show", "handoff")
	if code != 0 {
		t.Fatalf("show: %d %q", code, errs)
	}
	for _, label := range []string{"box-a", "box-b"} {
		for i := 0; i < rounds; i++ {
			if want := fmt.Sprintf("節 %s-%d", label, i); !strings.Contains(out, want) {
				t.Errorf("本文に %q が残っていない", want)
			}
		}
	}
}
