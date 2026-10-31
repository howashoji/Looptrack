package loop

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/hookio"
)

const remoteSyncFetchCall = "-c gc.auto=0 -c maintenance.auto=false fetch --quiet origin"

// remoteSyncGitOut は git の答えの表（上流 origin/main に対して ahead 先行・behind 遅れ）。
func remoteSyncGitOut(counts string) map[string]string {
	return map[string]string{
		"rev-parse --abbrev-ref HEAD":                             "work\n",
		remoteSyncFetchCall:                                       "",
		"rev-parse --abbrev-ref --symbolic-full-name @{upstream}": "origin/main\n",
		"rev-list --left-right --count HEAD...origin/main":        counts + "\n",
	}
}

func runRemoteSync(t *testing.T, e *Env, dir string) string {
	t.Helper()
	res, err := SessionStartRemoteSync(WithEnv(context.Background(), e), hookio.Event{Name: hookio.SessionStart, CWD: dir})
	if err != nil {
		t.Fatal(err)
	}
	return res.Context
}

func countCalls(calls []string, want string) int {
	n := 0
	for _, c := range calls {
		if c == "git "+want {
			n++
		}
	}
	return n
}

// TestRemoteSyncRealGit は本物の git で 4 つの状態を順に作り、同期のときだけ黙ることを同じテストの中で示す。
// 遅れの段は別の clone の push を fetch で初めて知る形なので、fetch が動いていなければ落ちる。
func TestRemoteSyncRealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git が無い")
	}
	base := t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	bare, a, b := filepath.Join(base, "o.git"), filepath.Join(base, "a"), filepath.Join(base, "b")
	git(base, "init", "--bare", "-b", "main", bare)
	git(base, "clone", bare, a)
	git(a, "checkout", "-B", "main")
	git(a, "commit", "--allow-empty", "-m", "first")
	git(a, "push", "-u", "origin", "main")
	git(base, "clone", bare, b)

	e := (&Env{Getenv: func(k string) string {
		switch k {
		case "LOOPTRACK_LANG":
			return "ja"
		case "LOOPTRACK_LOOP_REMOTE_SYNC_INTERVAL":
			return "1ns" // 毎回 fetch する
		}
		return ""
	}}).withDefaults()

	if s := runRemoteSync(t, e, a); s != "" {
		t.Fatalf("同期しているのに出た: %s", s)
	}
	git(b, "commit", "--allow-empty", "-m", "from b")
	git(b, "push", "origin", "main")
	if s := runRemoteSync(t, e, a); !strings.Contains(s, "1 コミット遅れて") {
		t.Fatalf("遅れが出ない: %q", s)
	}
	git(a, "commit", "--allow-empty", "-m", "from a")
	if s := runRemoteSync(t, e, a); !strings.Contains(s, "乖離") || !strings.Contains(s, "先行 1・遅れ 1") {
		t.Fatalf("乖離が出ない: %q", s)
	}
	git(a, "pull", "--rebase", "origin", "main")
	if s := runRemoteSync(t, e, a); !strings.Contains(s, "コミットが 1 件あります") {
		t.Fatalf("先行が出ない: %q", s)
	}
	git(a, "push", "origin", "main")
	if s := runRemoteSync(t, e, a); s != "" {
		t.Fatalf("同期に戻ったのに出た: %s", s)
	}
}

// TestRemoteSyncNoteOneLine は 3 つの状態の文面がどれも 1 行で、差が無ければ空であること（両方の言語）。
func TestRemoteSyncNoteOneLine(t *testing.T) {
	for _, tc := range []struct {
		name           string
		counts         string
		wantJa, wantEn string
	}{
		{"遅れ", "0\t3", "3 コミット遅れて", "3 commit(s) behind"},
		{"先行", "2\t0", "push していなければ", "Unless they are pushed"},
		{"乖離", "2\t3", "先行 2・遅れ 3", "2 ahead, 3 behind"},
		{"同期", "0\t0", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for lang, want := range map[string]string{"ja": tc.wantJa, "en": tc.wantEn} {
				e, _ := fakeGit(t, dir, time.Unix(1_000_000, 0), remoteSyncGitOut(tc.counts), map[string]string{"LOOPTRACK_LOOP_REMOTE_SYNC_INTERVAL": "1ns"})
				base := e.Getenv
				e.Getenv = func(k string) string {
					if k == "LOOPTRACK_LANG" {
						return lang
					}
					return base(k)
				}
				s := runRemoteSync(t, e, dir)
				if want == "" {
					if s != "" {
						t.Errorf("%s: 同期しているのに出た: %s", lang, s)
					}
					continue
				}
				if !strings.Contains(s, want) || !strings.Contains(s, "work") || !strings.Contains(s, "origin/main") || strings.Contains(s, "\n") {
					t.Errorf("%s: 文面が違う: %q", lang, s)
				}
			}
		})
	}
}

// TestRemoteSyncFetchFailureIsSilent は fetch の失敗のときに黙ることと、同じ差で fetch が通れば出ること（対照）。
func TestRemoteSyncFetchFailureIsSilent(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	env := map[string]string{"LOOPTRACK_LOOP_REMOTE_SYNC_INTERVAL": "1ns"}
	dir := t.TempDir()
	e, _ := fakeGit(t, dir, now, remoteSyncGitOut("0\t3"), env)
	if s := runRemoteSync(t, e, dir); s == "" {
		t.Fatal("前提が崩れています: fetch が通っても差が出ない")
	}
	out := remoteSyncGitOut("0\t3")
	delete(out, remoteSyncFetchCall) // 表に無い呼び出しは終了コード 1
	dir2 := t.TempDir()
	e, calls := fakeGit(t, dir2, now, out, env)
	if s := runRemoteSync(t, e, dir2); s != "" {
		t.Errorf("fetch が失敗したのに出た: %s", s)
	}
	if countCalls(*calls, remoteSyncFetchCall) != 1 {
		t.Errorf("fetch が 1 回試されていない: %v", *calls)
	}
}

// TestRemoteSyncFetchTimeoutDoesNotHang は fetch が ctx を無視して戻らなくても、hook が待ちの上限で抜けて黙ること。
// 打ち切りの後に孫のプロセスが標準エラーを握って起動の戻りが遅れる場合と同じ形。
func TestRemoteSyncFetchTimeoutDoesNotHang(t *testing.T) {
	dir := t.TempDir()
	e, _ := fakeGit(t, dir, time.Unix(1_000_000, 0), remoteSyncGitOut("0\t3"), map[string]string{"LOOPTRACK_LOOP_REMOTE_SYNC_FETCH_TIMEOUT": "50ms"})
	release := make(chan struct{})
	defer close(release)
	inner := e.Run
	gotEnv := make(chan []string, 1)
	gotGrace := make(chan time.Duration, 1)
	e.Run = func(ctx context.Context, c Command) ([]byte, int, error) {
		if strings.Contains(strings.Join(c.Args, " "), "fetch") {
			gotEnv <- c.Env
			gotGrace <- c.Grace
			<-release
			return nil, -1, context.DeadlineExceeded
		}
		return inner(ctx, c)
	}
	start := time.Now()
	s := runRemoteSync(t, e, dir)
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("待ちの上限で抜けていない: %s", d)
	}
	if s != "" {
		t.Errorf("時間切れなのに出た: %s", s)
	}
	if g := <-gotGrace; g != remoteSyncFetchGrace {
		t.Errorf("SIGKILL で止めると .lock が残る。Grace = %s", g)
	}
	joined := strings.Join(<-gotEnv, " ")
	if !strings.Contains(joined, "GIT_TERMINAL_PROMPT=0") {
		t.Errorf("認証の入力を止めていない: %q", joined)
	}
	if b, err := os.ReadFile(filepath.Join(dir, ".git", remoteSyncMarkerName("origin"))); err != nil || !strings.Contains(string(b), remoteSyncFailed) {
		t.Errorf("失敗が印に残っていない: %q %v", b, err)
	}
}

// TestRemoteSyncInterval は間隔の内側で fetch を重ねず、前回の結果に従うこと。
func TestRemoteSyncInterval(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	dir := t.TempDir()
	e, calls := fakeGit(t, dir, now, remoteSyncGitOut("0\t3"), nil)
	if s := runRemoteSync(t, e, dir); s == "" {
		t.Fatal("初回に出ない")
	}
	e.Now = func() time.Time { return now.Add(DefaultRemoteSyncInterval - time.Second) }
	if s := runRemoteSync(t, e, dir); s == "" {
		t.Error("間隔の内側は前回の参照で比べて出す")
	}
	if n := countCalls(*calls, remoteSyncFetchCall); n != 1 {
		t.Errorf("間隔の内側で fetch が重なった: %d", n)
	}
	e.Now = func() time.Time { return now.Add(DefaultRemoteSyncInterval) }
	runRemoteSync(t, e, dir)
	if n := countCalls(*calls, remoteSyncFetchCall); n != 2 {
		t.Errorf("間隔が過ぎたら fetch する: %d", n)
	}

	// 未来の時刻の印は間隔の内側と読まない（時計が戻った後に fetch が止まり続けない）
	e.Now = func() time.Time { return now.Add(-time.Hour) }
	before := countCalls(*calls, remoteSyncFetchCall)
	runRemoteSync(t, e, dir)
	if countCalls(*calls, remoteSyncFetchCall) != before+1 {
		t.Error("未来の時刻の印で fetch が止まった")
	}

	// 失敗の後は、間隔の内側では fetch せず黙る。fetch の途中で打ち切られた印も同じ
	for _, state := range []string{remoteSyncFailed, remoteSyncActive} {
		writeRemoteSyncMarker(filepath.Join(dir, ".git", remoteSyncMarkerName("origin")), now, state)
		e.Now = func() time.Time { return now.Add(time.Minute) }
		before := countCalls(*calls, remoteSyncFetchCall)
		if s := runRemoteSync(t, e, dir); s != "" {
			t.Errorf("%s の後の間隔の内側で出た: %s", state, s)
		}
		if countCalls(*calls, remoteSyncFetchCall) != before {
			t.Errorf("%s の後の間隔の内側で fetch した", state)
		}
	}
	// 共通の git のディレクトリに置くので、別の作業ツリーでも間隔は同じ
	wt := t.TempDir()
	gd := filepath.Join(dir, ".git", "worktrees", "w")
	if err := os.MkdirAll(gd, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gd, "commondir"), []byte("../..\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+gd+"\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	writeRemoteSyncMarker(filepath.Join(dir, ".git", remoteSyncMarkerName("origin")), now, remoteSyncOK)
	before = countCalls(*calls, remoteSyncFetchCall)
	if s := runRemoteSync(t, e, wt); s == "" {
		t.Error("別の作業ツリーで前回の結果を使っていない")
	}
	if countCalls(*calls, remoteSyncFetchCall) != before {
		t.Error("別の作業ツリーで fetch が重なった")
	}
}

// TestRemoteSyncNoUpstream は上流が無いと基準のブランチと比べ、どちらも無ければ黙ること。
func TestRemoteSyncNoUpstream(t *testing.T) {
	out := remoteSyncGitOut("0\t2")
	delete(out, "rev-parse --abbrev-ref --symbolic-full-name @{upstream}")
	out["rev-parse --verify --quiet origin/main^{commit}"] = "abc\n"
	dir := t.TempDir()
	e, _ := fakeGit(t, dir, time.Unix(1_000_000, 0), out, nil)
	if s := runRemoteSync(t, e, dir); !strings.Contains(s, "origin/main") {
		t.Errorf("基準と比べていない: %q", s)
	}
	// 基準の指定は stale-base と同じ環境変数に従う
	out2 := map[string]string{"rev-parse --verify --quiet other/base^{commit}": "abc\n", "rev-list --left-right --count HEAD...other/base": "0\t1\n", remoteSyncFetchCall: ""}
	dir = t.TempDir()
	e, _ = fakeGit(t, dir, time.Unix(1_000_000, 0), out2, map[string]string{"LOOPTRACK_LOOP_STALE_BASE_REF": "other/base"})
	if s := runRemoteSync(t, e, dir); !strings.Contains(s, "other/base") {
		t.Errorf("LOOPTRACK_LOOP_STALE_BASE_REF に従っていない: %q", s)
	}
	delete(out, "rev-parse --verify --quiet origin/main^{commit}")
	dir = t.TempDir()
	e, _ = fakeGit(t, dir, time.Unix(1_000_000, 0), out, nil)
	if s := runRemoteSync(t, e, dir); s != "" {
		t.Errorf("基準が無いのに出た: %s", s)
	}
}

// TestRemoteSyncBranchRemote はブランチの上流のリモートを fetch すること。
func TestRemoteSyncBranchRemote(t *testing.T) {
	out := remoteSyncGitOut("0\t1")
	out["config --get branch.work.remote"] = "fork\n"
	want := strings.Replace(remoteSyncFetchCall, " origin", " fork", 1)
	out[want] = ""
	delete(out, remoteSyncFetchCall)
	dir := t.TempDir()
	e, calls := fakeGit(t, dir, time.Unix(1_000_000, 0), out, nil)
	if s := runRemoteSync(t, e, dir); s == "" || countCalls(*calls, want) != 1 {
		t.Errorf("fork を fetch していない: %q %v", s, *calls)
	}
}

// TestRemoteSyncQuietCases は切ったとき・git でないときに git を 1 回も起動しないこと（対照は他のテストの出る場合）。
func TestRemoteSyncQuietCases(t *testing.T) {
	dir := t.TempDir()
	e, calls := fakeGit(t, dir, time.Unix(1_000_000, 0), remoteSyncGitOut("0\t3"), map[string]string{"LOOPTRACK_LOOP_REMOTE_SYNC_NOTICE": "off"})
	if s := runRemoteSync(t, e, dir); s != "" || len(*calls) != 0 {
		t.Errorf("切っても動いた: %q %v", s, *calls)
	}
	e, calls = fakeGit(t, dir, time.Unix(1_000_000, 0), remoteSyncGitOut("0\t3"), nil)
	nonGit := t.TempDir()
	if s := runRemoteSync(t, e, nonGit); s != "" || len(*calls) != 0 {
		t.Errorf("git でないのに動いた: %q %v", s, *calls)
	}
}

// TestRemoteSyncMarkerPerRemote は印がリモートごとで、別のリモートの fetch の結果を流用しないこと。
// 対照として、同じリモートの印があれば fetch しないこと。
func TestRemoteSyncMarkerPerRemote(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	dir := t.TempDir()
	e, calls := fakeGit(t, dir, now, remoteSyncGitOut("0\t1"), nil)
	writeRemoteSyncMarker(filepath.Join(dir, ".git", remoteSyncMarkerName("fork")), now, remoteSyncOK)
	runRemoteSync(t, e, dir)
	if n := countCalls(*calls, remoteSyncFetchCall); n != 1 {
		t.Errorf("fork の印で origin の fetch が省かれた: %d", n)
	}
	runRemoteSync(t, e, dir)
	if n := countCalls(*calls, remoteSyncFetchCall); n != 1 {
		t.Errorf("origin の印があるのに fetch した: %d", n)
	}
}

// TestRemoteSyncMarkerBeforeFetch は fetch の前に印を置くこと（同時に開いたセッションが fetch を重ねない）。
func TestRemoteSyncMarkerBeforeFetch(t *testing.T) {
	dir := t.TempDir()
	e, _ := fakeGit(t, dir, time.Unix(1_000_000, 0), remoteSyncGitOut("0\t1"), nil)
	inner := e.Run
	during := ""
	e.Run = func(ctx context.Context, c Command) ([]byte, int, error) {
		if strings.Contains(strings.Join(c.Args, " "), "fetch") {
			b, _ := os.ReadFile(filepath.Join(dir, ".git", remoteSyncMarkerName("origin")))
			during = string(b)
		}
		return inner(ctx, c)
	}
	runRemoteSync(t, e, dir)
	if !strings.Contains(during, remoteSyncActive) {
		t.Errorf("fetch の前に印が無い: %q", during)
	}
}

// TestRemoteSyncLocalUpstreamIsSilent は上流が手元のブランチのとき fetch も比較もしないこと（対照は他のテストの出る場合）。
func TestRemoteSyncLocalUpstreamIsSilent(t *testing.T) {
	out := remoteSyncGitOut("3\t0")
	out["config --get branch.work.remote"] = ".\n"
	dir := t.TempDir()
	e, calls := fakeGit(t, dir, time.Unix(1_000_000, 0), out, nil)
	if s := runRemoteSync(t, e, dir); s != "" || countCalls(*calls, "-c gc.auto=0 -c maintenance.auto=false fetch --quiet .") != 0 {
		t.Errorf("上流が手元なのに動いた: %q %v", s, *calls)
	}
}

// TestExecRunGraceSendsSIGTERM は Grace を付けた起動が打ち切りで SIGTERM を送ること。
// SIGKILL だと git は参照の .lock を消せない。対照として Grace なしではトラップが動かないこと。
func TestExecRunGraceSendsSIGTERM(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows に SIGTERM は無い")
	}
	for _, tc := range []struct {
		grace time.Duration
		want  bool
	}{{2 * time.Second, true}, {0, false}} {
		dir := t.TempDir()
		mark, ready := filepath.Join(dir, "term"), filepath.Join(dir, "ready")
		ctx, cancel := context.WithCancel(context.Background())
		script := `trap 'touch "$1"; exit 0' TERM; touch "$2"; while :; do sleep 0.02; done`
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _, _ = execRun(ctx, Command{Name: "sh", Args: []string{"-c", script, "sh", mark, ready}, Grace: tc.grace})
		}()
		for i := 0; i < 500 && !isFile(ready); i++ {
			time.Sleep(10 * time.Millisecond)
		}
		if !isFile(ready) {
			t.Fatal("前提が崩れています: シェルが起動しない")
		}
		cancel()
		<-done
		if got := isFile(mark); got != tc.want {
			t.Errorf("Grace %s: トラップの実行 = %v、期待 %v", tc.grace, got, tc.want)
		}
	}
}

// TestRemoteSyncWaitsForCleanup は時間切れの後も、git の後始末が終わるまで hook が抜けないこと。
func TestRemoteSyncWaitsForCleanup(t *testing.T) {
	dir := t.TempDir()
	e, _ := fakeGit(t, dir, time.Unix(1_000_000, 0), remoteSyncGitOut("0\t3"), map[string]string{"LOOPTRACK_LOOP_REMOTE_SYNC_FETCH_TIMEOUT": "20ms"})
	inner := e.Run
	var cleaned atomic.Bool
	e.Run = func(ctx context.Context, c Command) ([]byte, int, error) {
		if strings.Contains(strings.Join(c.Args, " "), "fetch") {
			<-ctx.Done()
			time.Sleep(100 * time.Millisecond)
			cleaned.Store(true)
			return nil, -1, ctx.Err()
		}
		return inner(ctx, c)
	}
	if s := runRemoteSync(t, e, dir); s != "" {
		t.Errorf("時間切れなのに出た: %s", s)
	}
	if !cleaned.Load() {
		t.Error("後始末を待たずに抜けた")
	}
}

// TestRemoteSyncMarkerFollowsBranchRemote は上流のリモートの印を見ること。URL と紛らわしい名前も別の印になる。
func TestRemoteSyncMarkerFollowsBranchRemote(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	fetchFork := strings.Replace(remoteSyncFetchCall, " origin", " fork", 1)
	out := remoteSyncGitOut("0\t1")
	out["config --get branch.work.remote"] = "fork\n"
	out[fetchFork] = ""
	dir := t.TempDir()
	e, calls := fakeGit(t, dir, now, out, nil)
	writeRemoteSyncMarker(filepath.Join(dir, ".git", remoteSyncMarkerName("origin")), now, remoteSyncOK)
	runRemoteSync(t, e, dir)
	if n := countCalls(*calls, fetchFork); n != 1 {
		t.Errorf("origin の印で fork の fetch が省かれた: %d", n)
	}
	if !isFile(filepath.Join(dir, ".git", remoteSyncMarkerName("fork"))) {
		t.Error("fork の印が無い")
	}
	names := map[string]bool{}
	for _, r := range []string{"a/b", "a_b", "https://example.invalid/x.git", strings.Repeat("r", 400)} {
		n := remoteSyncMarkerName(r)
		if names[n] || len(n) > 100 || strings.ContainsAny(n, `/\\:`) {
			t.Errorf("印の名前が使えない: %q → %q", r, n)
		}
		names[n] = true
	}
}

// TestExecRunGraceKillsIgnoringProcess は SIGTERM を無視するプロセスも猶予の後に止まること。
func TestExecRunGraceKillsIgnoringProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows に SIGTERM は無い")
	}
	ready := filepath.Join(t.TempDir(), "ready")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = execRun(ctx, Command{Name: "sh", Args: []string{"-c", `trap '' TERM; touch "$1"; while :; do sleep 0.02; done`, "sh", ready}, Grace: 100 * time.Millisecond})
	}()
	for i := 0; i < 500 && !isFile(ready); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("SIGTERM を無視するプロセスで戻らない")
	}
}

// TestExecRunGraceKeepsSuccess は本体が 0 で終われば、孫が出力を握っていても成功のままであること。
// 対照として本体が 5 で終わったら 5 を返すこと。
func TestExecRunGraceKeepsSuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh が無い")
	}
	for _, want := range []int{0, 5} {
		_, code, err := execRun(context.Background(), Command{Name: "sh", Args: []string{"-c", fmt.Sprintf("sleep 1 & exit %d", want)}, Grace: 50 * time.Millisecond})
		if err != nil || code != want {
			t.Errorf("code = %d err = %v、期待 %d", code, err, want)
		}
	}
}
