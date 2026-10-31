package server

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/client/cli"
	clienv "github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/hook/core"
	"github.com/howashoji/looptrack/internal/store"
)

// presenceTerm は 1 つのディレクトリで動く 1 つのセッション。hook は core を本物のまま動かし、切り離しだけを同じプロセスの呼び出しに置き換える。
type presenceTerm struct {
	t    *testing.T
	dir  string
	sess string
	vars clienv.Env
}

func (e *env) presenceTerm(token, slug, label, sess, branch string) *presenceTerm {
	e.t.Helper()
	home := e.t.TempDir()
	dir := filepath.Join(e.t.TempDir(), "repo-"+label)
	for _, args := range [][]string{{"init", "-q", "-b", branch, dir}, {"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "x"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			e.t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	vars := clienv.FromMap(map[string]string{"LOOPTRACK_API_URL": e.srv.URL + "/im", "LOOPTRACK_PROJECT": slug, "LOOPTRACK_TOKEN": token, "LOOPTRACK_LANG": "ja",
		"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "APPDATA": filepath.Join(home, "AppData"),
		"LOOPTRACK_MACHINE_LABEL": label, "CLAUDE_PROJECT_DIR": dir, "PATH": os.Getenv("PATH")})
	return &presenceTerm{t: e.t, dir: dir, sess: sess, vars: vars}
}

// hook は hook を 1 回動かす。切り離した子の仕事を同じプロセスで終わらせてから返す。
func (p *presenceTerm) hook(name, event string) {
	p.t.Helper()
	var jobs [][]byte
	ev := &core.Env{Vars: &p.vars, Getwd: func() string { return p.dir }, Spawn: func(job []byte, _ string) error {
		jobs = append(jobs, job)
		return nil
	}}
	in := `{"hook_event_name":"` + event + `","session_id":"` + p.sess + `","cwd":"` + filepath.ToSlash(p.dir) + `"}`
	var out bytes.Buffer
	if code := core.Main(context.Background(), name, []string{"--agent", "claude-code"}, strings.NewReader(in), &out, &out, ev); code != 0 {
		p.t.Fatalf("hook %s: %d %s", name, code, out.String())
	}
	for _, j := range jobs {
		core.Main(context.Background(), "usage", []string{"--usage-job"}, bytes.NewReader(j), io.Discard, io.Discard, ev)
	}
}

// summary は手で打つ summary（このセッションから）。
func (p *presenceTerm) summary() string {
	p.t.Helper()
	vars := clienv.Overlay(p.vars, map[string]string{"LOOPTRACK_SESSION_ID": p.sess})
	var out, errb bytes.Buffer
	if code := cli.Main([]string{"summary", "--limit", "5"}, cli.IO{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb}, vars); code != 0 {
		p.t.Fatalf("summary: %d %s", code, errb.String())
	}
	return out.String()
}

// 受け入れ条件: 別のディレクトリのセッションが送った後、もう一方の summary に一覧が出て、時刻が古くなると消える。
// 着手中のイシューは着手の記録から引き、SessionEnd で外れ、保持の 7 日を過ぎた行は定期の処理が消す。
func TestPresenceHookToSummary(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("pre")
	member := func(login string) *apiClient {
		u := e.user(login, login+"-password-1", "member")
		if err := store.SetMember(ctx, e.db, pr.ID, u.ID, "editor"); err != nil {
			t.Fatal(err)
		}
		return e.apiAs(u)
	}
	alice, bob := member("pre-alice"), member("pre-bob")
	a, b := e.presenceTerm(alice.token, "pre", "box-a", "session-a", "main"), e.presenceTerm(bob.token, "pre", "box-b", "session-b", "feature/x")

	start := time.Date(2026, 10, 31, 3, 0, 0, 0, time.UTC)
	e.setClock(start)
	a.hook("summary", "SessionStart")
	b.hook("summary", "SessionStart")

	var created struct {
		Issue struct {
			ID string `json:"id"`
		} `json:"issue"`
	}
	bob.json(201, "POST", "/projects/pre/issues", map[string]any{"title": "在席の確認"}, &created)
	vars := clienv.Overlay(b.vars, map[string]string{"LOOPTRACK_SESSION_ID": b.sess})
	var errb bytes.Buffer
	if code := cli.Main([]string{"status", created.Issue.ID, "In Progress"}, cli.IO{Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: &errb}, vars); code != 0 {
		t.Fatalf("着手: %d %s", code, errb.String())
	}

	got := a.summary()
	for _, want := range []string{"動いているほかのセッション 1 本", "参加者が名乗った", "pre-bob", "`box-b`", "claude-code", "`feature/x`", "`repo-box-b`", created.Issue.ID} {
		if !strings.Contains(got, want) {
			t.Errorf("A の summary に %q が無い:\n%s", want, got)
		}
	}
	if strings.Contains(got, "box-a") {
		t.Errorf("自分のセッションが一覧に出た:\n%s", got)
	}
	if strings.Contains(got, b.dir) {
		t.Errorf("作業ツリーの絶対パスが見えた:\n%s", got)
	}
	if got := b.summary(); !strings.Contains(got, "box-a") || !strings.Contains(got, "repo-box-a") {
		t.Errorf("B の summary に A が無い:\n%s", got)
	}

	e.setClock(start.Add(16 * time.Minute))
	if got := a.summary(); strings.Contains(got, "box-b") {
		t.Errorf("15 分を過ぎても出ている:\n%s", got)
	}
	b.hook("usage", "Stop") // Stop の送信で戻る
	if got := a.summary(); !strings.Contains(got, "box-b") {
		t.Errorf("Stop の後に出ない:\n%s", got)
	}
	b.hook("usage", "SessionEnd")
	if got := a.summary(); strings.Contains(got, "box-b") {
		t.Errorf("SessionEnd の後も出ている:\n%s", got)
	}

	b.hook("usage", "Stop")
	rows := func() (n int) {
		if err := e.db.QueryRow("SELECT COUNT(*) FROM session_presence").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	housekeepOnce(ctx, e.app, logger, e.s.Service())
	if n := rows(); n != 2 {
		t.Fatalf("対照: 7 日より新しい行が消えた: %d", n)
	}
	e.setClock(start.Add(8 * 24 * time.Hour))
	housekeepOnce(ctx, e.app, logger, e.s.Service())
	if n := rows(); n != 0 {
		t.Fatalf("7 日を過ぎた行が残った: %d", n)
	}
}

// 画面の一覧とボードからのリンク、summary の JSON の形。
func TestPresencePageAndMCP(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("prew")
	u := e.user("prew-ed", "editor-password-1", "member")
	if err := store.SetMember(ctx, e.db, pr.ID, u.ID, "editor"); err != nil {
		t.Fatal(err)
	}
	api := e.apiAs(u)
	if code, _, body := api.do("POST", "/projects/prew/presence", map[string]any{"branch": "x"}); code != http.StatusBadRequest {
		t.Fatalf("セッションの無い要求: %d %s", code, body)
	}
	p := e.presenceTerm(api.token, "prew", "box-w", "session-w", "feature/w")
	p.hook("summary", "SessionStart")
	c, board, _ := e.boardSession("prew-ed", "editor-password-1", "prew")
	if !strings.Contains(board, `href="/im/p/prew/presence"`) {
		t.Error("ボードに在席の画面へのリンクが無い")
	}
	res, page := e.get(c, "/im/p/prew/presence")
	if res.StatusCode != http.StatusOK || !strings.Contains(page, "box-w") || !strings.Contains(page, "feature/w") || !strings.Contains(page, "repo-box-w") {
		t.Errorf("一覧: %d %s", res.StatusCode, page)
	}
	if strings.Contains(page, p.dir) {
		t.Error("作業ツリーの絶対パスが画面に出た")
	}
	var s struct {
		Others []presenceJSON `json:"active_sessions"`
	}
	api.json(http.StatusOK, "GET", "/projects/prew/summary", nil, &s) // セッションの無い要求には全部がほかのセッション
	if len(s.Others) != 1 || s.Others[0].MachineLabel != "box-w" || s.Others[0].Issues == nil {
		t.Fatalf("summary の active_sessions: %+v", s.Others)
	}
}
