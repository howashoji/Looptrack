package loop

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/hookio"
)

func TestRegistry(t *testing.T) {
	if n := len(Names()); n != 19 {
		t.Errorf("hook は 19 本: %d %v", n, Names())
	}
	reg := Registry()
	for _, n := range Names() {
		if reg[n] == nil {
			t.Errorf("%s の本体が無い", n)
		}
	}
	delete(reg, "session-start-rules")
	if _, ok := Lookup("session-start-rules"); !ok {
		t.Error("Registry の写しを書き換えても登録表は変わらない")
	}
}

// TestMainFailOpen は、知らない名前・知らない --agent・読めない入力・時間切れ・panic で何も出さずに 0 を返すこと（fail-open）。
func TestMainFailOpen(t *testing.T) {
	env := &Env{Getenv: func(k string) string {
		if k == "CLAUDECODE" {
			return "1"
		}
		return ""
	}, Getwd: func() string { return t.TempDir() }}
	run := func(name string, args []string, in string) (int, string) {
		var out, errb bytes.Buffer
		code := Main(context.Background(), name, args, strings.NewReader(in), &out, &errb, env)
		return code, out.String() + errb.String()
	}
	for _, c := range []struct {
		name, in string
		args     []string
	}{
		{"no-such-hook", "{}", nil},
		{"session-start-rules", "{}", []string{"--agent", "gemini"}},
		{"pre-tool-scope-guard", "not json", []string{"--agent", "claude-code"}},
		{"stop-handoff-freshness", "[1,2]", []string{"--agent", "claude-code"}},
	} {
		if code, out := run(c.name, c.args, c.in); code != 0 || out != "" {
			t.Errorf("%s %v %q: exit %d %q", c.name, c.args, c.in, code, out)
		}
	}

	register(Entry{Name: "test-panic", Event: hookio.Stop, Timeout: time.Second, Hook: func(context.Context, hookio.Event) (hookio.Result, error) {
		panic("boom")
	}})
	register(Entry{Name: "test-slow", Event: hookio.Stop, Timeout: 50 * time.Millisecond, Hook: func(ctx context.Context, _ hookio.Event) (hookio.Result, error) {
		<-ctx.Done() // 打ち切りの ctx が本体にも届く
		return hookio.Result{Block: "遅すぎた"}, nil
	}})
	defer delete(registry, "test-panic")
	defer delete(registry, "test-slow")
	for _, n := range []string{"test-panic", "test-slow"} {
		if code, out := run(n, []string{"--agent", "claude-code"}, `{"hook_event_name":"Stop"}`); code != 0 || out != "" {
			t.Errorf("%s: exit %d %q", n, code, out)
		}
	}
}

// TestMainEventFallback は、入力に hook_event_name が無いときに登録表のイベントで出力すること。
func TestMainEventFallback(t *testing.T) {
	dir := t.TempDir()
	env := &Env{Getenv: func(k string) string {
		switch k {
		case "CLAUDE_PROJECT_DIR":
			return dir
		case "CLAUDECODE":
			return "1"
		}
		return ""
	}, Getwd: func() string { return dir }}
	var out bytes.Buffer
	Main(context.Background(), "user-prompt-task-mode", []string{"--agent", "claude-code"}, strings.NewReader(`{"prompt":"確認して","session_id":"x"}`), &out, nil, env)
	if !strings.Contains(out.String(), `"hookEventName":"UserPromptSubmit"`) {
		t.Errorf("出力: %s", out.String())
	}
}

// TestHarnessTimeoutDiagnosis は、打ち切りに達した呼び出しを検査の失敗の文面から見分けられること。
//
// hook は打ち切りに達すると何も出さずに終える（fail-open）ので、素のままでは「期待した語が無い」としか言えない。
// 機械の負荷で子プロセスの起動が間に合わなかったのか本当の不具合なのかを、ここで分ける。
func TestHarnessTimeoutDiagnosis(t *testing.T) {
	// callLimit は entry.Timeout と call.timeout の短いほう（mainWith が本体の ctx に必ず entry.Timeout を入れるので、
	// call.timeout を長くしても entry.Timeout は越えられない）。
	for _, c := range []struct {
		name string
		call call
		want time.Duration
	}{
		{"entry の打ち切り", call{hook: "session-scope-guard"}, 9 * time.Second},
		{"call のほうが短ければ call", call{hook: "session-scope-guard", timeout: 2 * time.Second}, 2 * time.Second},
		{"call を長くしても entry は越えられない", call{hook: "session-scope-guard", timeout: time.Minute}, 9 * time.Second},
		{"登録に無いもの（gates）は打ち切り無し", call{hook: "gates"}, 0},
	} {
		if d := callLimit(c.call); d != c.want {
			t.Errorf("%s: 期待 %s / 実際 %s", c.name, c.want, d)
		}
	}

	// why は打ち切りに達したときだけ言う。
	if s := (got{elapsed: time.Second, limit: 9 * time.Second}).why(); s != "" {
		t.Errorf("打ち切りに達していなければ何も言わない: %s", s)
	}
	if s := (got{elapsed: time.Hour}).why(); s != "" {
		t.Errorf("打ち切りが無ければ何も言わない: %s", s)
	}
	s := (got{elapsed: 9200 * time.Millisecond, limit: 9 * time.Second}).why()
	for _, w := range []string{"打ち切り 9s", "経過 9.2s", "何も出していない"} {
		if !strings.Contains(s, w) {
			t.Errorf("「%s」が無い: %s", w, s)
		}
	}

	// 実際の呼び出しでも経過と打ち切りが記録される（CLI も Makefile も無いので黙る経路）。
	//
	// 経過に「0 より大きいこと」は求めない。Go の単調時計は Windows では KUSER_SHARED_DATA の割り込み時刻を読むので、
	// 分解能はシステムのタイマの刻み（既定で約 15.6ms）しかなく、子プロセスを起こさない呼び出しは 1 刻みの中で終わって
	// 経過が 0 に丸まる（Windows の CI で実際に elapsed=0s になった）。0 は「測っていない」ではなく「刻みより速かった」で、
	// why の判定（経過 < 打ち切りなら黙る）は 0 でも正しく働く。
	// そこで確かめるのは、①打ち切りが登録表から入ること ②経過がこの呼び出しの実時間に収まっていること
	// （同じ時計で外側から測る。外側は内側を含むので、分解能に関係なく 経過 <= 実時間）③打ち切りに達していないこと。
	// ②③は経過が 0 でも通るので、「経過が代入されていること」はここでは確かめられない。それは runGo で経過と打ち切りを
	// 隣り合わせて代入していることに寄りかかっている（runGo の該当箇所の注釈を見よ）。
	sb := newSandbox(t)
	outer := time.Now()
	g := sb.runGo(call{hook: "session-start-iteration", input: "{}", env: map[string]string{"CLAUDE_PROJECT_DIR": sb.p("none")}})
	spent := time.Since(outer)
	if !g.quiet() {
		t.Errorf("何も出さないはず: %q", g.out.Stdout)
	}
	if g.limit != 9*time.Second {
		t.Errorf("打ち切りが記録されていない: limit=%s", g.limit)
	}
	if g.elapsed < 0 || g.elapsed > spent {
		t.Errorf("経過がこの呼び出しの実時間（%s）に収まっていない: elapsed=%s", spent, g.elapsed)
	}
	if g.elapsed >= g.limit {
		t.Errorf("打ち切りに達していないはず: elapsed=%s limit=%s", g.elapsed, g.limit)
	}
	if s := g.why(); s != "" {
		t.Errorf("打ち切りに達していないのに言った: %s", s)
	}
}
