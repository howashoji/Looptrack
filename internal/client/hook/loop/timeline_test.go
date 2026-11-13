package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/hookio"
)

// fakeTimeline は未読を 1 行返し、読んだ位置の送信を数えるサーバ。
func fakeTimeline(t *testing.T) (*httptest.Server, *atomic.Int32) {
	return fakeTimelineSlowSeen(t, 0, 0)
}

// fakeTimelineSlowSeen は、/seen を受け取った時点で数え（サーバは位置を進めた）、応答を seenDelay だけ遅らせる。
// 連絡と未読の応答は readDelay だけ遅らせる（位置の送信を hook の締切の近くまで押し出す）。
func fakeTimelineSlowSeen(t *testing.T, seenDelay, readDelay time.Duration) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var seen atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.HasSuffix(r.URL.Path, "/timeline/seen") {
			time.Sleep(readDelay)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/timeline/unread"):
			_ = json.NewEncoder(w).Encode(map[string]any{"text": "タイムライン: 試験の行", "head_seq": 5, "read_seq": 1, "has_position": true,
				"advance_to": 5, "rows": []map[string]any{{"seq": 5}}, "unacked_holds": []any{}, "unacked_requests": []any{}, "undelivered": []any{}})
		case strings.HasSuffix(r.URL.Path, "/timeline/seen"):
			seen.Add(1)
			select {
			case <-time.After(seenDelay):
			case <-r.Context().Done():
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"seq": 5, "advanced": true})
		case strings.HasSuffix(r.URL.Path, "/inbox"):
			_ = json.NewEncoder(w).Encode(map[string]any{"messages": []any{}, "unread": 0, "unread_ids": []any{}})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "unknown_api", "message": "x"}})
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func runTimelinePrompt(t *testing.T, srvURL, dir string) string {
	t.Helper()
	home := t.TempDir()
	vars := map[string]string{"LOOPTRACK_API_URL": srvURL, "LOOPTRACK_PROJECT": "p", "LOOPTRACK_TOKEN": "tok", "LOOPTRACK_LANG": "ja",
		"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": home, "APPDATA": home, "CLAUDE_PROJECT_DIR": dir}
	env := &Env{Getenv: func(k string) string { return vars[k] }, Environ: func() []string { return nil }, Getwd: func() string { return dir }}
	in, _ := json.Marshal(map[string]any{"session_id": "sess-t", "cwd": dir, "hook_event_name": "UserPromptSubmit", "prompt": "x"})
	var out, errb bytes.Buffer
	if code := Main(context.Background(), "user-prompt-messages", []string{"--agent", "claude-code"}, bytes.NewReader(in), &out, &errb, env); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	return out.String()
}

// 受け入れ条件: hook が時間切れで出力できなかったとき、読んだ位置が進まない（出力の前に止まる形を作る）。
// (a) 位置を送る直前で締切まで 100 ms を切ったら、位置を送らず文も出さない（まだ打ち切られていない ctx で確かめる）。
// 対照は、時間切れにならなければ同じサーバで文が出て位置を 1 回送ること。
func TestTimelineTimeoutDoesNotAdvance(t *testing.T) {
	srv, seen := fakeTimeline(t)
	dir := t.TempDir()
	if out := runTimelinePrompt(t, srv.URL, dir); !strings.Contains(out, "試験の行") || seen.Load() != 1 {
		t.Fatalf("前提が崩れています: 時間切れでないのに出ない・位置を送らない: %q seen=%d", out, seen.Load())
	}
	defer func(prev func(context.Context)) { timelineBeforeSeen = prev }(timelineBeforeSeen)
	timelineBeforeSeen = func(ctx context.Context) { // 締切の 100 ms 手前まで使う（ctx はまだ生きている）
		if d, ok := ctx.Deadline(); ok {
			time.Sleep(time.Until(d) - 100*time.Millisecond)
		}
	}
	out := runTimelinePrompt(t, srv.URL, dir)
	time.Sleep(300 * time.Millisecond) // 打ち切られた本体が後から送らないことも見る
	if out != "" {
		t.Errorf("締切に余裕が無いのに出した: %q", out)
	}
	if n := seen.Load(); n != 1 {
		t.Errorf("締切に余裕が無いのに位置を送った: %d 回（want 1 = 対照の分だけ）", n)
	}
}

// (b) 位置の送信をサーバが受け取った（位置は進んだ）のに応答が締切まで返らなくても、文は出す（位置だけ進んで文が消えることが無い）。
func TestTimelineSlowSeenStillOutputs(t *testing.T) {
	srv, seen := fakeTimelineSlowSeen(t, 5*time.Second, 1200*time.Millisecond)
	start := time.Now()
	out := runTimelinePrompt(t, srv.URL, t.TempDir())
	if seen.Load() != 1 {
		t.Fatalf("前提が崩れています: 位置の送信がサーバに届かない: %d", seen.Load())
	}
	if !strings.Contains(out, "試験の行") {
		t.Errorf("位置は進んだのに文を出さなかった: %q", out)
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Errorf("hook の持ち時間を越えた: %v", d)
	}
}

// 場面と AI ごとの位置を進めるかの表は、既定を設計の推定どおりに持つ（実機で確かめたらここを直す）。
func TestTimelineAdvanceDefaults(t *testing.T) {
	cases := []struct {
		scene timelineScene
		agent string
		want  bool
	}{
		{sceneSessionStart, "codex", true}, {scenePrompt, "codex", true}, {sceneBoundary, "claude-code", true}, {sceneBoundary, "codex", true},
		{sceneBoundary, "copilot", false}, {sceneStop, "claude-code", true}, {sceneStop, "codex", false},
	}
	for _, c := range cases {
		if got := timelineAdvances[c.scene][hookio.Agent(c.agent)]; got != c.want {
			t.Errorf("%s %s: %v want %v", c.scene, c.agent, got, c.want)
		}
	}
}

// タイムラインの経路を持たない古いサーバ（404 unknown_api）には何も出さない（届かないときの知らせも出さない）。
// 対照は、同じ手順で届かないサーバには知らせが出ること。
func TestTimelineOldServerSilent(t *testing.T) {
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "unknown_api", "message": "x"}})
	}))
	defer old.Close()
	if out := runTimelinePrompt(t, old.URL, t.TempDir()); out != "" {
		t.Errorf("古いサーバで出した: %q", out)
	}
	if out := runTimelinePrompt(t, "http://127.0.0.1:1", t.TempDir()); !strings.Contains(out, "確かめられていません") {
		t.Errorf("前提が崩れています: 届かないサーバで知らせが出ない: %q", out)
	}
}

// 位置を進める表は配線と食い違わない: true の組み合わせは manifest でその AI に配線がある（null でない）。
// 対照として、配線の無い組み合わせ（pre-tool-scope-guard の codex）を数えたことも確かめる。
func TestTimelineAdvancesMatchWiring(t *testing.T) {
	_, entries := loadManifest(t)
	wired := map[string]map[string]bool{}
	for _, en := range entries {
		name := strings.TrimPrefix(en.Name, "hooks/")
		wired[name] = map[string]bool{"claude-code": en.Event != ""}
		for _, ag := range []string{"codex", "copilot"} {
			r, ok := en.raw[ag]
			wired[name][ag] = ok && string(r) != "null"
		}
	}
	unwired := 0
	for scene, hook := range timelineSceneHooks {
		w, ok := wired[hook]
		if !ok {
			t.Fatalf("前提が崩れています: manifest に %s が無い", hook)
		}
		for ag, adv := range timelineAdvances[scene] {
			if adv && !w[string(ag)] {
				t.Errorf("%s の %s は配線が無いのに位置を進める表になっている", scene, ag)
			}
		}
		for _, ag := range []string{"codex", "copilot"} {
			if !w[ag] {
				unwired++
			}
		}
	}
	if len(timelineSceneHooks) != len(timelineAdvances) || unwired == 0 {
		t.Errorf("前提が崩れています: 場面の数 %d / %d・配線の無い組み合わせ %d", len(timelineSceneHooks), len(timelineAdvances), unwired)
	}
}

// 書き込みの前の確認で、締切に間に合わず依頼を出せなかったときは確かめた通し番号を進めない（次の確認で同じ依頼を取りに行く）。
// 対照は、間に合ったときに依頼が出て、次の確認では取りに行かないこと。
func TestTimelinePreToolKeepsSeqWhenNotShown(t *testing.T) {
	var unread atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/timeline/check"):
			_ = json.NewEncoder(w).Encode(map[string]any{"seq": 5, "holds": []int64{}, "read_seq": 1, "has_position": true, "behind": true})
		case strings.HasSuffix(r.URL.Path, "/timeline/unread"):
			unread.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"head_seq": 5, "advance_to": 5, "rows": []any{}, "unacked_holds": []any{}, "undelivered": []any{},
				"unacked_requests": []map[string]any{{"id": 9, "kind": "request", "version": 1, "last_version": 1, "seq": 5, "body": "依頼の本文",
					"from": "bob", "to": "user", "to_user": "alice", "state": "active", "undelivered": true, "unacked": true}}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"recorded": 1})
		}
	}))
	defer srv.Close()
	dir, home := t.TempDir(), t.TempDir()
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	vars := map[string]string{"LOOPTRACK_API_URL": srv.URL, "LOOPTRACK_PROJECT": "p", "LOOPTRACK_TOKEN": "tok", "LOOPTRACK_LANG": "ja",
		"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": home, "APPDATA": home, "CLAUDE_PROJECT_DIR": dir}
	env := &Env{Getenv: func(k string) string { return vars[k] }, Environ: func() []string { return nil }, Getwd: func() string { return dir },
		Now: func() time.Time { return now }}
	pre := func() string {
		in, _ := json.Marshal(map[string]any{"session_id": "sess-p", "cwd": dir, "hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}})
		var out, errb bytes.Buffer
		if code := Main(context.Background(), "pre-tool-scope-guard", []string{"--agent", "claude-code"}, bytes.NewReader(in), &out, &errb, env); code != 0 {
			t.Fatalf("exit %d: %s", code, errb.String())
		}
		return out.String()
	}
	prev := timelineBeforeSeen
	timelineBeforeSeen = func(ctx context.Context) {
		if d, ok := ctx.Deadline(); ok {
			time.Sleep(time.Until(d) - 100*time.Millisecond)
		}
	}
	if out := pre(); out != "" {
		t.Fatalf("前提が崩れています: 締切に間に合わないのに出した: %q", out)
	}
	timelineBeforeSeen = prev
	now = now.Add(61 * time.Second)
	if out := pre(); !strings.Contains(out, "依頼の本文") {
		t.Errorf("出せなかった依頼を次の確認で出さない: %q（unread %d 回）", out, unread.Load())
	}
	before := unread.Load()
	now = now.Add(61 * time.Second)
	pre()
	if unread.Load() != before {
		t.Errorf("対照: 出した後の確認でまた取りに行った")
	}
}

// 書き込みの前の確認で、締切に間に合わず並びから外れた待機を知らせられなかったときは並びを戻し、次の確認で知らせる。
// 対照は、知らせた後の確認では重ねて出さないこと。
func TestTimelinePreToolLiftedAfterDeadline(t *testing.T) {
	var checks atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		first := checks.Load() <= 1
		switch {
		case strings.HasSuffix(r.URL.Path, "/timeline/check"):
			checks.Add(1)
			holds := []int64{}
			if checks.Load() == 1 {
				holds = []int64{7}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"seq": 5, "holds": holds, "read_seq": 5, "has_position": true})
		case strings.HasSuffix(r.URL.Path, "/timeline/unread"):
			held := []map[string]any{}
			if first {
				held = append(held, map[string]any{"id": 7, "kind": "hold", "version": 1, "last_version": 1, "seq": 5, "body": "待機の本文",
					"from": "bob", "to": "user", "to_user": "alice", "state": "active", "undelivered": true, "unacked": true})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"head_seq": 5, "advance_to": 5, "rows": []any{}, "unacked_holds": held, "undelivered": []any{}, "unacked_requests": []any{}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"recorded": 1})
		}
	}))
	defer srv.Close()
	dir, home := t.TempDir(), t.TempDir()
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	vars := map[string]string{"LOOPTRACK_API_URL": srv.URL, "LOOPTRACK_PROJECT": "p", "LOOPTRACK_TOKEN": "tok", "LOOPTRACK_LANG": "ja",
		"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": home, "APPDATA": home, "CLAUDE_PROJECT_DIR": dir}
	env := &Env{Getenv: func(k string) string { return vars[k] }, Environ: func() []string { return nil }, Getwd: func() string { return dir },
		Now: func() time.Time { return now }}
	pre := func() string {
		in, _ := json.Marshal(map[string]any{"session_id": "sess-l", "cwd": dir, "hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}})
		var out, errb bytes.Buffer
		if code := Main(context.Background(), "pre-tool-scope-guard", []string{"--agent", "claude-code"}, bytes.NewReader(in), &out, &errb, env); code != 0 {
			t.Fatalf("exit %d: %s", code, errb.String())
		}
		return out.String()
	}
	const lifted = "待機 #7 はこのセッションには効かなくなりました"
	if out := pre(); !strings.Contains(out, "待機の本文") {
		t.Fatalf("前提が崩れています: 待機が出ない: %q", out)
	}
	prev := timelineBeforeSeen
	timelineBeforeSeen = func(ctx context.Context) {
		if d, ok := ctx.Deadline(); ok {
			time.Sleep(time.Until(d) - 100*time.Millisecond)
		}
	}
	now = now.Add(61 * time.Second)
	if out := pre(); out != "" {
		t.Fatalf("前提が崩れています: 締切に間に合わないのに出した: %q", out)
	}
	timelineBeforeSeen = prev
	now = now.Add(61 * time.Second)
	if out := pre(); !strings.Contains(out, lifted) {
		t.Errorf("締切で出せなかった待機の知らせを次の確認で出さない: %q", out)
	}
	now = now.Add(61 * time.Second)
	if out := pre(); strings.Contains(out, lifted) {
		t.Errorf("同じ待機の知らせを 2 度出した: %q", out)
	}
}
