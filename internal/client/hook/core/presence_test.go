package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/client/env"
)

func presenceEnv(t *testing.T, url string, spawn func([]byte, string) error) *Env {
	t.Helper()
	home := t.TempDir()
	vars := env.FromMap(map[string]string{"LOOPTRACK_API_URL": url, "LOOPTRACK_PROJECT": "tst", "LOOPTRACK_TOKEN": "tok", "LOOPTRACK_USAGE": "0", "LOOPTRACK_LANG": "ja",
		"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "APPDATA": filepath.Join(home, "AppData"), "LOOPTRACK_MACHINE_LABEL": "box"})
	return &Env{Vars: &vars, Getwd: func() string { return home }, Spawn: spawn}
}

// 送る時・送らない時。Stop・SessionEnd は usage の hook が、LOOPTRACK_USAGE=0 でも送る。セッションの無い入力では子を起こさない。
func TestPresenceSpawnsOnLifecycleEvents(t *testing.T) {
	var jobs [][]byte
	e := presenceEnv(t, "http://127.0.0.1:1", func(job []byte, _ string) error { jobs = append(jobs, job); return nil })
	run := func(in string) { quietRun(t, "usage", []string{"--agent", "codex"}, in, e, 0) }
	run(`{"hook_event_name":"Stop"}`)
	run(`{"hook_event_name":"PreToolUse","session_id":"s1"}`)
	if len(jobs) != 0 {
		t.Fatalf("子を起こさないはず: %s", jobs)
	}
	run(`{"hook_event_name":"Stop","session_id":"s1"}`)
	run(`{"hook_event_name":"SessionEnd","session_id":"s1"}`)
	if len(jobs) != 2 {
		t.Fatalf("Stop と SessionEnd で 1 回ずつ: %s", jobs)
	}
	var stop, end usageJob
	if json.Unmarshal(jobs[0], &stop) != nil || json.Unmarshal(jobs[1], &end) != nil || stop.Presence == nil || end.Presence == nil {
		t.Fatalf("仕事の形: %s", jobs)
	}
	if stop.Presence.Ended || !end.Presence.Ended || stop.Presence.Agent != "codex" || stop.Presence.SessionID != "s1" {
		t.Fatalf("仕事の中身: %+v %+v", stop.Presence, end.Presence)
	}
}

// 長いターンの間は PostToolUse と UserPromptSubmit で 5 分に 1 回まで送る。Claude Code の配線にある mark と usage のどちらからでも同じ間引きが効く。
func TestPresenceBeatDuringTurn(t *testing.T) {
	spawned := 0
	e := presenceEnv(t, "http://127.0.0.1:1", func([]byte, string) error { spawned++; return nil })
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	e.Now = func() time.Time { return now }
	tool := `{"hook_event_name":"PostToolUse","session_id":"s1","tool_name":"Bash","tool_input":{"command":"ls"}}`
	mark := func() { quietRun(t, "issue-freshness-mark", []string{"--agent", "claude-code"}, tool, e, 0) }
	mark()
	if spawned != 1 {
		t.Fatalf("最初の呼び出しで送るはず: %d", spawned)
	}
	now = now.Add(presenceBeat - time.Second)
	mark()
	quietRun(t, "usage", []string{"--agent", "codex"}, `{"hook_event_name":"PostToolUse","session_id":"s1"}`, e, 0)
	if spawned != 1 {
		t.Fatalf("5 分の間は送らないはず: %d", spawned)
	}
	now = now.Add(2 * time.Second)
	quietRun(t, "issue-freshness-mark", []string{"--agent", "claude-code"}, `{"hook_event_name":"UserPromptSubmit","session_id":"s1","prompt":"x"}`, e, 0)
	if spawned != 2 {
		t.Fatalf("5 分を過ぎたら送るはず: %d", spawned)
	}
	quietRun(t, "usage", []string{"--agent", "codex"}, `{"hook_event_name":"Stop","session_id":"s1"}`, e, 0)
	if spawned != 3 {
		t.Fatalf("Stop は間引かない: %d", spawned)
	}
}

// 受け入れ条件: サーバに届かないとき hook を止めない。hook は子を起こすだけで返り、子も打ち切りで終わり、何も出さない。
// 切り離しの失敗と panic でも hook の本体は続く。対照として、届くサーバには要求が着くことを見る。
func TestPresenceUnreachableDoesNotBlock(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := "http://" + ln.Addr().String()
	ln.Close() // 待ち受けのない番地

	var job []byte
	e := presenceEnv(t, dead, func(j []byte, _ string) error { job = j; return nil })
	in := `{"hook_event_name":"Stop","session_id":"s1"}`
	quietRun(t, "usage", []string{"--agent", "claude-code"}, in, e, 2*time.Second)
	if job == nil {
		t.Fatal("前提が崩れています: 子を起こしていない")
	}
	begin := time.Now()
	var out, errb bytes.Buffer
	if code := Main(context.Background(), "usage", []string{usageJobFlag}, bytes.NewReader(job), &out, &errb, e); code != 0 || out.Len() > 0 || errb.Len() > 0 {
		t.Fatalf("子: exit %d %q %q", code, out.String(), errb.String())
	}
	if d := time.Since(begin); d > presenceTimeout+2*time.Second {
		t.Fatalf("子が打ち切りで終わらない: %s", d)
	}

	for name, spawn := range map[string]func([]byte, string) error{
		"失敗":    func([]byte, string) error { return errors.New("起動できない") },
		"panic": func([]byte, string) error { panic("切り離しで panic") },
	} {
		t.Run(name, func(t *testing.T) {
			quietRun(t, "usage", []string{"--agent", "claude-code"}, in, presenceEnv(t, dead, spawn), 0)
		})
	}
}

// 送る中身。作業ツリーの絶対パスは本文にも URL にも載らず、ディレクトリの名前だけが載る。サーバが受け取れない形の値は空にして送る。
func TestPresenceSendsDirectoryNameOnly(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	var sessions, methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		bodies, sessions, methods = append(bodies, r.URL.Path+" "+string(b)), append(sessions, r.Header.Get("X-Looptrack-Session")), append(methods, r.Method)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	e := presenceEnv(t, srv.URL, nil).withDefaults()
	dir := filepath.Join(t.TempDir(), "my-repo")
	gitInit(t, dir, "feature/x")
	spaced := filepath.Join(t.TempDir(), "my repo")
	gitInit(t, spaced, "main")
	c := &Call{Env: e}
	c.sendPresence(presenceJob{Agent: "claude-code", SessionID: "s1", CWD: dir})
	c.sendPresence(presenceJob{SessionID: "s1", CWD: dir, Ended: true})
	c.sendPresence(presenceJob{SessionID: "s1", CWD: spaced})
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 3 || methods[0] != http.MethodPost || methods[1] != http.MethodDelete || sessions[0] != "s1" || sessions[1] != "s1" {
		t.Fatalf("要求: %v %v %v", methods, sessions, bodies)
	}
	for _, want := range []string{`"directory": "my-repo"`, `"machine_label": "box"`, `"branch": "feature/x"`, "/presence"} {
		if !strings.Contains(bodies[0], want) {
			t.Fatalf("本文に %s が無い: %s", want, bodies[0])
		}
	}
	if strings.Contains(bodies[0], filepath.ToSlash(filepath.Dir(dir))) || strings.Contains(bodies[0], filepath.Dir(dir)) {
		t.Fatalf("作業ツリーの絶対パスを送った: %s", bodies[0])
	}
	if !strings.Contains(bodies[2], `"directory": ""`) || !strings.Contains(bodies[2], `"branch": "main"`) {
		t.Fatalf("空白を含む名前は空にして、ほかの欄は送るはず: %s", bodies[2])
	}
}

// git の外と、作業ツリーがホームのときはディレクトリの名前を返さない。対照はふつうのリポジトリ。
func TestPresenceGitHidesHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "alice")
	gitInit(t, home, "main")
	if _, _, dir := presenceGit(home, []string{home}); dir != "" {
		t.Errorf("ホームの名前を返した: %q", dir)
	}
	if _, _, dir := presenceGit(filepath.Join(home, "."), []string{"", home}); dir != "" {
		t.Errorf("ホームの名前を返した: %q", dir)
	}
	sub := filepath.Join(home, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, dir := presenceGit(sub, []string{home}); dir != "" {
		t.Errorf("ホームの下でも作業ツリーはホーム: %q", dir)
	}
	outside := t.TempDir()
	if b, h, dir := presenceGit(outside, nil); b != "" || h != "" || dir != "" {
		t.Errorf("git の外: %q %q %q", b, h, dir)
	}
	if _, _, dir := presenceGit(home, nil); dir != "alice" {
		t.Errorf("対照: ホームでなければ名前を返す: %q", dir)
	}
}

func gitInit(t *testing.T, dir, branch string) {
	t.Helper()
	for _, args := range [][]string{{"init", "-q", "-b", branch, dir}, {"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "x"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
}
