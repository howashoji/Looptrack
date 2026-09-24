package core

// fail-open（panic・時間切れ・読めない入力・知らない名前でも、何も出さずに exit 0。鮮度ガードの check も含む）と、
// 登録表・切り離し（自分を子プロセスとして起動する）の確かめ。

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/hook/loop"
	"github.com/howashoji/looptrack/internal/hookio"
)

// jstr はパスを JSON の文字列にする（Windows のパスの \ をそのまま埋めると不正なエスケープで JSON として読めず、
// 読めない入力の扱いになって確かめたいことに届かない）。
func jstr(p string) string {
	b, _ := json.Marshal(p)
	return string(b)
}

// quietRun は Go 版を動かし、終了コード 0・出力なしを確かめる。
func quietRun(t *testing.T, name string, args []string, stdin string, e *Env, timeout time.Duration) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	start := time.Now()
	code := mainWith(context.Background(), name, args, strings.NewReader(stdin), &stdout, &stderr, e, func(o *hookio.RunOptions) {
		if timeout > 0 {
			o.Timeout = timeout
		}
	})
	if code != 0 || stdout.Len() > 0 || stderr.Len() > 0 {
		t.Errorf("%s %v: exit 0・出力なしのはず: exit %d stdout %q stderr %q", name, args, code, stdout.String(), stderr.String())
	}
	if timeout > 0 && time.Since(start) > timeout+2*time.Second {
		t.Errorf("%s: 打ち切り（%s）で終わっていない: %s", name, timeout, time.Since(start))
	}
}

func testEnv(s *sandbox, extra map[string]string) *Env {
	m := s.envMap(call{})
	for k, v := range extra {
		if v == "" {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	vars := env.FromMap(m)
	return &Env{Vars: &vars, Getwd: func() string { return s.proj }, Sleep: func(time.Duration) {},
		Spawn: func([]byte, string) error { panic("切り離しで panic") }}
}

func TestFailOpenUnreadableInput(t *testing.T) {
	s := newSandbox(t)
	freshSetup(s)
	for _, in := range []string{"not json", "[1, 2]", `"str"`, "\x00\xff\xfe", "{", "null", "1"} {
		for _, name := range Names() {
			quietRun(t, name, []string{"--agent", "claude-code"}, in, testEnv(s, nil), 0)
		}
	}
	// 以前の実装は読めない入力を空とみなして器（_nosession）を作ったが、Go 版は何も記録しない
	if exists(s.p(".claude", ".looptrack-freshness", "sessions")) {
		t.Error("読めない入力で記録を作った")
	}
	if len(s.api.requests()) != 0 {
		t.Errorf("読めない入力で API を呼んだ: %v", s.api.requests())
	}
}

func TestFailOpenArgs(t *testing.T) {
	s := newSandbox(t)
	quietRun(t, "no-such-hook", nil, "{}", testEnv(s, nil), 0)
	quietRun(t, "issue-freshness-check", []string{"--agent", "gemini"}, `{"hook_event_name":"Stop"}`, testEnv(s, nil), 0)
	quietRun(t, "issue-freshness-check", []string{"--agent"}, `{"hook_event_name":"Stop"}`, testEnv(s, nil), 0)
	quietRun(t, "summary", []string{"--agent", "claude-code", "--limit", "x"}, `{}`, testEnv(s, nil), 0)
}

func TestFailOpenPanic(t *testing.T) {
	s := newSandbox(t)
	freshSetup(s)
	boom := testEnv(s, map[string]string{"CLAUDE_PROJECT_DIR": ""})
	boom.Getwd = func() string { panic("Getwd で panic") }
	stop := `{"hook_event_name":"Stop","session_id":"s1"}`
	quietRun(t, "issue-freshness-mark", []string{"--agent", "claude-code"}, `{"hook_event_name":"PostToolUse","session_id":"s1","tool_name":"Bash","tool_input":{"command":"git commit -m x"}}`, boom, 0)
	quietRun(t, "issue-freshness-check", []string{"--agent", "claude-code"}, stop, boom, 0)
	// 切り離し（Spawn）で panic
	s.write(s.p("t.jsonl"), sampleTranscript())
	e := testEnv(s, map[string]string{"LOOPTRACK_USAGE_FOREGROUND": ""})
	quietRun(t, "usage", []string{"--agent", "claude-code"}, `{"hook_event_name":"SessionEnd","session_id":"s1","transcript_path":`+jstr(s.p("t.jsonl"))+`}`, e, 0)
	// 鮮度ガードの check が差し戻す状態で本体が panic しても止めない
	e = testEnv(s, nil)
	for _, in := range []string{`{"hook_event_name":"PostToolUse","session_id":"s1","tool_name":"Bash","tool_input":{"command":"looptrack issue show TST-0001"}}`,
		`{"hook_event_name":"PostToolUse","session_id":"s1","tool_name":"Edit","tool_input":{"file_path":` + jstr(s.p("a.go")) + `}}`} {
		quietRun(t, "issue-freshness-mark", []string{"--agent", "claude-code"}, in, e, 0)
	}
	var out bytes.Buffer
	Main(context.Background(), "issue-freshness-check", []string{"--agent", "claude-code"}, strings.NewReader(stop), &out, &out, e)
	if !strings.Contains(out.String(), `"decision":"block"`) {
		t.Fatalf("対照: 差し戻すはず: %q", out.String())
	}
	// 同じ記録で、本体がルートを求めるところ（Getwd の 2 回目）で panic させる
	e = testEnv(s, map[string]string{"CLAUDE_PROJECT_DIR": ""})
	calls := 0
	e.Getwd = func() string {
		if calls++; calls > 1 {
			panic("Getwd で panic")
		}
		return s.proj
	}
	quietRun(t, "issue-freshness-check", []string{"--agent", "claude-code"}, stop, e, 0)
	if calls < 2 {
		t.Errorf("本体まで届いていない（Getwd %d 回）", calls)
	}
	// CLI の逃げ道も止めない（理由を stderr に書いて 0）
	var so, se bytes.Buffer
	if code := FreshnessMain(context.Background(), []string{"reset"}, nil, &so, &se, boom); code != 0 || !strings.Contains(se.String(), "issue-freshness:") {
		t.Errorf("reset の panic: exit %d stderr %q", code, se.String())
	}
}

func TestFailOpenTimeout(t *testing.T) {
	s := newSandbox(t)
	freshSetup(s)
	e := testEnv(s, nil)
	for _, in := range []string{`{"hook_event_name":"PostToolUse","session_id":"s1","tool_name":"Bash","tool_input":{"command":"looptrack issue show TST-0001"}}`,
		`{"hook_event_name":"PostToolUse","session_id":"s1","tool_name":"Edit","tool_input":{"file_path":` + jstr(s.p("a.go")) + `}}`} {
		quietRun(t, "issue-freshness-mark", []string{"--agent", "claude-code"}, in, e, 0)
	}
	s.api.mu.Lock()
	s.api.delay = time.Second
	s.api.mu.Unlock()
	// 打ち切った本体の goroutine は止まらない（本番はすぐ os.Exit する）。一時ディレクトリを消す前に終わるのを待つ
	defer time.Sleep(2500 * time.Millisecond)
	// check: /activity が遅い → 打ち切りで何も出さずに 0（差し戻さない）
	quietRun(t, "issue-freshness-check", []string{"--agent", "claude-code"}, `{"hook_event_name":"Stop","session_id":"s1"}`, e, 200*time.Millisecond)
	// summary: サーバが遅い
	s.api.mu.Lock()
	s.api.summaryBody = summaryLayered
	s.api.mu.Unlock()
	quietRun(t, "summary", []string{"--agent", "claude-code"}, `{"hook_event_name":"SessionStart"}`, e, 200*time.Millisecond)
	// mark: 控えが無く /projects が遅い
	_ = os.Remove(s.p(".claude", ".looptrack-freshness", "project.json"))
	quietRun(t, "issue-freshness-mark", []string{"--agent", "claude-code"}, `{"hook_event_name":"UserPromptSubmit","session_id":"s1","prompt":"TST-0002"}`, e, 200*time.Millisecond)
}

// TestRegistry は core の名前が loop の名前と重ならないこと（cmd/looptrack は core を先に引く）。
func TestRegistry(t *testing.T) {
	want := []string{"issue-freshness-check", "issue-freshness-mark", "summary", "usage"}
	if got := Names(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Names = %v", got)
	}
	for _, n := range loop.Names() {
		if _, ok := Lookup(n); ok {
			t.Errorf("loop の hook と名前が重なる: %s", n)
		}
	}
	for _, n := range Names() {
		e, _ := Lookup(n)
		if e.Timeout <= 0 || e.Timeout >= 10*time.Second {
			t.Errorf("%s: 打ち切りは配線の timeout より短く: %s", n, e.Timeout)
		}
	}
}

// buildLooptrack は cmd/looptrack をビルドする（-short では飛ばす）。
func buildLooptrack(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("-short")
	}
	exe := filepath.Join(t.TempDir(), "looptrack")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", exe, "../../../../cmd/looptrack")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return exe
}

func environ(m map[string]string) []string {
	var out []string
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	if v := os.Getenv("SYSTEMROOT"); v != "" {
		out = append(out, "SYSTEMROOT="+v)
	}
	return out
}

// TestDetachEndToEnd は実行ファイルで、usage の送信が子プロセスに切り離される（親はすぐ 0 で終わり、子が送る）ことと、
// cmd/looptrack の振り分け（hook <core の名前>・issue-freshness）を確かめる。
func TestDetachEndToEnd(t *testing.T) {
	exe := buildLooptrack(t)
	s := newSandbox(t)
	freshSetup(s)
	usageSetup(s)
	m := s.envMap(call{})
	delete(m, "LOOPTRACK_USAGE_FOREGROUND")
	m["LOOPTRACK_USAGE_TIMEOUT"] = "15" // 下で止めている間に子の送信が打ち切られないように（既定は 5 秒）
	// 子の送信（POST …/usage）の応答を、親が終わるまで返さない。親が子の送信を待っているなら、親は応答を待ったまま
	// 終わらない（下の 60 秒で落ちる）。切り離しに失敗して親が自分で送っているなら、親は本体の打ち切り（9 秒）で
	// 送信を切って終わるので、止めている間に接続が切られたこと（usageAbandoned）で落ちる。
	//
	// 以前は子の送信を 5 秒遅らせ、親が 3 秒以内に終わるかを時間で見ていた。go test ./... を通常と -race の
	// 2 本同時に走らせた負荷の下では、親が 3〜6 秒かかって落ちた。親と子に時刻の記録を仮に入れて測ると、
	// 親の Go のコード（パッケージの初期化から usage の切り離し・終了まで）は 2〜10 ミリ秒で、子の送信は親が終わった
	// 時点でまだ届いてもいなかった。時間のほぼすべては、テストが親を起動してから親のパッケージの初期化が始まるまで
	// （実行ファイルの起動。macOS で、ビルドしたばかりの実行ファイルを初めて起動するときの検査・読み込みが、並行して
	// 作られ起動される多数のテストの実行ファイルと重なって遅れる）だった。親が子を待っていたのではなく、判定が
	// 「起動の速さ」を測っていた。そこで時間ではなく順序（子の送信が終わる前に親が終わる）で確かめる。
	hold := make(chan struct{})
	release := sync.OnceFunc(func() { close(hold) })
	defer release()
	s.api.mu.Lock()
	s.api.usageHold = hold
	s.api.mu.Unlock()

	cmd := exec.Command(exe, "hook", "usage", "--agent", "claude-code")
	cmd.Env = environ(m)
	cmd.Dir = s.proj
	cmd.Stdin = strings.NewReader(`{"hook_event_name":"SessionEnd","session_id":"sess-1","transcript_path":` + jstr(s.transcript()) + `,"cwd":` + jstr(s.proj) + `}`)
	type result struct {
		out []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := cmd.CombinedOutput()
		done <- result{out, err}
	}()
	var r result
	select {
	case r = <-done:
	case <-time.After(60 * time.Second): // 起動の遅れには十分に長く、待っているなら必ず超える
		release()
		<-done
		t.Fatal("親が子の送信（止めてある応答）を待っていて終わらない")
	}
	if r.err != nil || len(r.out) > 0 {
		t.Fatalf("親: exit 0・出力なしのはず: %v %q", r.err, r.out)
	}
	release() // 子の送信を通す
	deadline := time.Now().Add(30 * time.Second)
	for len(sent(s)) == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	ps := sent(s)
	if len(ps) != 1 || ps[0]["trigger"] != "session_end" || ps[0]["session_id"] != "sess-1" {
		t.Fatalf("子が 1 件送るはず: %v", ps)
	}
	// 子が送り終えたら usage-last を書く
	last := filepath.Join(s.appDir(), "usage-last", "sess-1")
	for !exists(last) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !exists(last) {
		t.Error("子が usage-last を書いていない")
	}
	s.api.mu.Lock()
	abandoned := s.api.usageAbandoned
	s.api.mu.Unlock()
	if abandoned > 0 {
		t.Errorf("送信が応答を待たずに切られた（親が切り離さずに自分で送り、打ち切りで終わった）: %d 件", abandoned)
	}

	// 振り分け: hook issue-freshness-mark → core、issue-freshness show → core、hook session-start-rules → loop（知らない名前も 0）
	run := func(stdin string, args ...string) string {
		t.Helper()
		c := exec.Command(exe, args...)
		c.Env = environ(m)
		c.Dir = s.proj
		c.Stdin = strings.NewReader(stdin)
		b, err := c.Output()
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return string(b)
	}
	run(`{"hook_event_name":"PostToolUse","session_id":"s9","tool_name":"Bash","tool_input":{"command":"looptrack issue show TST-0001"}}`,
		"hook", "issue-freshness-mark", "--agent", "claude-code")
	if got := run("", "issue-freshness", "show"); !strings.Contains(got, "engaged : TST-0001") {
		t.Errorf("issue-freshness show: %s", got)
	}
	run(`{"hook_event_name":"PostToolUse","session_id":"s9","tool_name":"Edit","tool_input":{"file_path":`+jstr(s.p("a.go"))+`}}`,
		"issue-freshness", "mark", "--agent", "claude-code")
	if got := run(`{"hook_event_name":"Stop","session_id":"s9"}`, "hook", "issue-freshness-check", "--agent", "claude-code"); !strings.Contains(got, `"decision":"block"`) {
		t.Errorf("check: %s", got)
	}
	if got := run("", "issue-freshness", "ack", "TST-0001"); !strings.Contains(got, "検査対象から外しました: TST-0001") {
		t.Errorf("ack: %s", got)
	}
	if got := run("{}", "hook", "no-such-hook"); got != "" {
		t.Errorf("知らない名前: %q", got)
	}
}
