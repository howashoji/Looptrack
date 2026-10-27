package core

// 会話記録の自動送信の hook（transcript.go）。偽 API（会話記録の登録・追記・取得だけ）と一時の置き場
// （HOME・XDG_CONFIG_HOME・CLAUDE_CONFIG_DIR）で、合成の記録を送らせる。本物の記録は読まない。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/client/agentdirs"
	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/usagesnap"
)

const (
	trSID = "00000000-0000-4000-8000-0000000000f1"
	// trMarker は合成の記録の中身の目印（退避・失敗の記録・状態のファイルに現れてはいけない）。
	trMarker = "synthetic-transcript-body-7f3a"
)

// trFake は会話記録の API の偽物。
type trFake struct {
	srv *httptest.Server
	mu  sync.Mutex
	// ruleOff なら登録を 422 transcript_rule_off で拒む。status が 0 でなければ、すべての要求をその状態で返す。
	ruleOff bool
	status  int
	// hold が nil でなければ、登録の応答をそれが閉じられるまで返さない（記録は先にする）。
	hold    chan struct{}
	reqs    []string
	seq     int64
	files   map[string]int64
	chunked int
}

func newTrFake(t *testing.T) *trFake {
	f := &trFake{files: map[string]int64{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *trFake) url() string { return f.srv.URL + "/im" }

func (f *trFake) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reqs...)
}

func (f *trFake) set(fn func(f *trFake)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *trFake) state() map[string]any {
	files := []map[string]any{}
	for k, n := range f.files {
		files = append(files, map[string]any{"file_key": k, "length": n})
	}
	return map[string]any{"transcript": map[string]any{"id": 7, "project": "tst", "agent": "claude-code", "vendor_session_id": trSID,
		"seq": f.seq, "files": files, "transcripts_enabled": true}}
}

func (f *trFake) serve(w http.ResponseWriter, r *http.Request) {
	_, _ = io.ReadAll(r.Body)
	f.mu.Lock()
	f.reqs = append(f.reqs, r.Method+" "+r.URL.Path)
	hold := f.hold
	f.mu.Unlock()
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	p := strings.TrimPrefix(r.URL.Path, "/im/api/v1")
	if hold != nil && r.Method == "POST" && p == "/projects/tst/transcripts" {
		select {
		case <-hold:
		case <-r.Context().Done():
			return
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer test-token" {
		reply(401, map[string]any{"error": map[string]any{"code": "unauthorized", "message": "認証が必要です"}})
		return
	}
	if f.status != 0 {
		reply(f.status, map[string]any{"error": map[string]any{"code": "x", "message": fmt.Sprintf("HTTP %d のテスト", f.status)}})
		return
	}
	switch {
	case r.Method == "POST" && p == "/projects/tst/transcripts":
		if f.ruleOff {
			reply(422, map[string]any{"error": map[string]any{"code": "transcript_rule_off", "message": "このプロジェクトは会話記録を受け付けていません"}})
			return
		}
		reply(200, f.state())
	case r.Method == "POST" && p == "/transcripts/7/chunks":
		key, _ := url.PathUnescape(r.Header.Get("X-Looptrack-Transcript-File"))
		off, _ := strconv.ParseInt(r.Header.Get("X-Looptrack-Transcript-Offset"), 10, 64)
		n, _ := strconv.ParseInt(r.Header.Get("X-Looptrack-Transcript-Length"), 10, 64)
		if r.Header.Get("If-Match") != `"`+strconv.FormatInt(f.seq, 10)+`"` || off != f.files[key] {
			reply(409, map[string]any{"error": map[string]any{"code": "transcript_seq", "message": "seq"}})
			return
		}
		f.seq++
		f.chunked++
		f.files[key] = off + n
		reply(201, f.state())
	case r.Method == "GET" && p == "/transcripts/7":
		reply(200, f.state())
	default:
		reply(404, map[string]any{"error": map[string]any{"code": "not_found", "message": "ありません"}})
	}
}

// trBox は 1 つのテストの置き場と時計。
type trBox struct {
	t       *testing.T
	api     *trFake
	home    string
	proj    string
	claude  string
	vars    map[string]string
	now     time.Time
	spawned [][]byte
}

func newTrBox(t *testing.T) *trBox {
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b := &trBox{t: t, api: newTrFake(t), home: filepath.Join(d, "home"), proj: filepath.Join(d, "proj"), now: time.Now()}
	b.claude = filepath.Join(b.home, ".claude")
	for _, p := range []string{b.proj, filepath.Join(b.home, ".config")} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	b.vars = map[string]string{"PATH": os.Getenv("PATH"), "HOME": b.home, "USERPROFILE": b.home, "XDG_CONFIG_HOME": filepath.Join(b.home, ".config"),
		"APPDATA": filepath.Join(b.home, "AppData"), "CLAUDE_CONFIG_DIR": b.claude, "CLAUDE_PROJECT_DIR": b.proj, "CLAUDECODE": "1",
		"LANG": "C.UTF-8", "LOOPTRACK_LANG": "ja", "LOOPTRACK_API_URL": b.api.url(), "LOOPTRACK_PROJECT": "tst", "LOOPTRACK_TOKEN": "test-token"}
	t.Chdir(b.proj) // push は作業ディレクトリから書き換えの元（git のルート）を決める。テストのパッケージのディレクトリを読ませない
	return b
}

// writeTranscript は合成の記録を Claude Code の置き場に書く（lines は 1 行ずつ。改行は足す）。
func (b *trBox) writeTranscript(lines ...string) string {
	b.t.Helper()
	p := filepath.Join(b.claude, "projects", agentdirs.ClaudeSlug(b.proj), trSID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		b.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		b.t.Fatal(err)
	}
	return p
}

func (b *trBox) goodTranscript() string {
	return b.writeTranscript(
		fmt.Sprintf(`{"type":"user","sessionId":%q,"cwd":%q,"message":{"role":"user","content":%q}}`, trSID, b.proj, trMarker+" one"),
		fmt.Sprintf(`{"type":"assistant","sessionId":%q,"cwd":%q,"message":{"role":"assistant","content":%q}}`, trSID, b.proj, trMarker+" two"))
}

func (b *trBox) env() env.Env { return env.FromMap(b.vars) }

func (b *trBox) rememberRule(on bool) {
	usagesnap.RememberTranscriptsRule(on, usagesnap.Options{Env: b.env()})
}

// run は hook を 1 回呼ぶ（切り離しは記録してから同じプロセスで子の本体を動かす）。終了コードと出力を返す。
func (b *trBox) run(event string) (int, string) {
	b.t.Helper()
	vars := b.env()
	e := &Env{Vars: &vars, Getwd: func() string { return b.proj }, Now: func() time.Time { return b.now }}
	e.SpawnTranscript = func(job []byte, dir string) error {
		b.spawned = append(b.spawned, job)
		if dir != b.proj {
			b.t.Errorf("子の作業ディレクトリ %q（%q のはず）", dir, b.proj)
		}
		runTranscriptJob(context.Background(), &Call{Env: e.withDefaults(), Args: TranscriptChildArgs}, bytes.NewReader(job))
		return nil
	}
	in := fmt.Sprintf(`{"hook_event_name":%q,"session_id":%q,"transcript_path":"","cwd":%q}`, event, trSID, b.proj)
	var out, errb bytes.Buffer
	code := Main(context.Background(), "transcript", []string{"--agent", "claude-code"}, strings.NewReader(in), &out, &errb, e)
	return code, out.String() + errb.String()
}

func (b *trBox) stateDir() string { return usagesnap.StateDir(b.env()) }

func countPrefix(reqs []string, prefix string) int {
	n := 0
	for _, r := range reqs {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

// 鍵の 4 通り（端末のオプトイン 0/1 × 覚えたルール 切/入）のうち、両方が入のときだけ要求が出る。
// 対照: 両方が入のときは登録 1 本とチャンクの追記が出る（同じテストの中）。hook はどの組み合わせでも何も出さずに 0 で終わる。
func TestTranscriptGates(t *testing.T) {
	for _, c := range []struct {
		optIn, rule string
		want        bool
	}{
		{"0", "off", false},
		{"0", "on", false},
		{"1", "off", false},
		{"1", "on", true},
	} {
		t.Run("optin="+c.optIn+"/rule="+c.rule, func(t *testing.T) {
			b := newTrBox(t)
			b.goodTranscript()
			b.vars["LOOPTRACK_SESSION_TRANSCRIPTS"] = c.optIn
			b.rememberRule(c.rule == "on")
			code, out := b.run("SessionEnd")
			if code != 0 || out != "" {
				t.Fatalf("hook は何も出さずに 0 で終わるはず: %d %q", code, out)
			}
			reqs := b.api.requests()
			if !c.want {
				if len(reqs) != 0 || len(b.spawned) != 0 {
					t.Fatalf("鍵が揃っていないのに送りました: 要求 %v・子 %d", reqs, len(b.spawned))
				}
				return
			}
			if len(b.spawned) != 1 || countPrefix(reqs, "POST /im/api/v1/projects/tst/transcripts") != 1 ||
				countPrefix(reqs, "POST /im/api/v1/transcripts/7/chunks") < 1 {
				t.Fatalf("両方が入なら子を 1 つ起こし、登録 1 本と追記を送るはず: 子 %d・要求 %v", len(b.spawned), reqs)
			}
		})
	}
}

// 覚えたルールが無ければ子が登録で確かめる。切（422）なら切を覚え、次の hook は子を起こさない。
// 覚えた切が 1 日より古ければ、また確かめる。
func TestTranscriptRuleUnknownChecksByRegister(t *testing.T) {
	b := newTrBox(t)
	b.goodTranscript()
	b.vars["LOOPTRACK_SESSION_TRANSCRIPTS"] = "1"
	b.api.set(func(f *trFake) { f.ruleOff = true })
	b.run("SessionEnd")
	if len(b.spawned) != 1 || countPrefix(b.api.requests(), "POST /im/api/v1/projects/tst/transcripts") != 1 {
		t.Fatalf("覚えた値が無ければ登録で確かめるはず: 子 %d・要求 %v", len(b.spawned), b.api.requests())
	}
	if on, _, ok := usagesnap.TranscriptsRule(usagesnap.Options{Env: b.env()}); !ok || on {
		t.Fatalf("422 transcript_rule_off で切を覚えるはず: %v %v", on, ok)
	}
	if f := usagesnap.ReadTranscriptFailure(b.stateDir()); f != nil {
		t.Fatalf("ルールが切なのは失敗ではない: %+v", f)
	}
	b.run("SessionEnd")
	if len(b.spawned) != 1 {
		t.Fatalf("覚えた切が新しいうちは子を起こさないはず: 子 %d", len(b.spawned))
	}
	b.now = b.now.Add(25 * time.Hour)
	b.run("SessionEnd")
	if len(b.spawned) != 2 {
		t.Fatalf("覚えた切が 1 日より古ければ確かめ直すはず: 子 %d", len(b.spawned))
	}
}

// Stop の 2 回目（10 分以内）は要求を出さない。10 分を過ぎた Stop と、間を置かない SessionEnd は送る。
func TestTranscriptThrottle(t *testing.T) {
	b := newTrBox(t)
	b.goodTranscript()
	b.vars["LOOPTRACK_SESSION_TRANSCRIPTS"] = "1"
	b.rememberRule(true)
	b.run("Stop")
	first := len(b.api.requests())
	if len(b.spawned) != 1 || first == 0 {
		t.Fatalf("最初の Stop は送るはず: 子 %d・要求 %d", len(b.spawned), first)
	}
	b.now = b.now.Add(9 * time.Minute)
	b.run("Stop")
	if len(b.spawned) != 1 || len(b.api.requests()) != first {
		t.Fatalf("10 分以内の Stop は要求を出さないはず: 子 %d・要求 %d → %d", len(b.spawned), first, len(b.api.requests()))
	}
	b.run("SessionEnd")
	if len(b.spawned) != 2 || countPrefix(b.api.requests()[first:], "POST /im/api/v1/projects/tst/transcripts") != 1 {
		t.Fatalf("SessionEnd は間引かないはず: 子 %d・要求 %v", len(b.spawned), b.api.requests()[first:])
	}
	b.now = b.now.Add(11 * time.Minute)
	b.run("Stop")
	if len(b.spawned) != 3 {
		t.Fatalf("10 分を過ぎた Stop は送るはず: 子 %d", len(b.spawned))
	}
	b.vars["LOOPTRACK_TRANSCRIPT_THROTTLE_MIN"] = "0"
	b.run("Stop")
	if len(b.spawned) != 4 {
		t.Fatalf("LOOPTRACK_TRANSCRIPT_THROTTLE_MIN=0 なら間引かないはず: 子 %d", len(b.spawned))
	}
}

// 送れなかったときの退避には会話記録の中身が入らない（合成の行の文字列が 0 回）。対照: 元の記録には目印が 2 回ある。
// 次に送れたときに退避した記録を送り、退避と失敗の記録を消す。
func TestTranscriptSpoolHoldsNoContent(t *testing.T) {
	b := newTrBox(t)
	main := b.goodTranscript()
	b.vars["LOOPTRACK_SESSION_TRANSCRIPTS"] = "1"
	b.rememberRule(true)
	raw, _ := os.ReadFile(main)
	if n := strings.Count(string(raw), trMarker); n != 2 {
		t.Fatalf("前提が崩れています: 元の記録の目印が %d 回（2 回のはず）", n)
	}
	b.api.set(func(f *trFake) { f.status = 503 })
	b.run("SessionEnd")
	spool := usagesnap.SpoolFiles(usagesnap.TranscriptSpoolDir(b.stateDir(), b.api.url(), "tst"))
	if len(spool) != 1 {
		t.Fatalf("届かなければ 1 件退避するはず: %v", spool)
	}
	if f := usagesnap.ReadTranscriptFailure(b.stateDir()); f == nil || f.Reason != usagesnap.FailUnreachable {
		t.Fatalf("失敗を unreachable で記録するはず: %+v", f)
	}
	// looptrack の置き場（退避・失敗・間引き・送った長さの状態）のどこにも中身が無い
	hits := 0
	_ = filepath.WalkDir(b.stateDir(), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		bs, _ := os.ReadFile(p)
		hits += strings.Count(string(bs), trMarker)
		return nil
	})
	if hits != 0 {
		t.Fatalf("退避と状態のファイルに会話記録の中身が %d 回現れます（0 回のはず）", hits)
	}
	var job transcriptJob
	bs, _ := os.ReadFile(spool[0])
	if json.Unmarshal(bs, &job) != nil || job.SessionID != trSID || job.Agent != "claude-code" {
		t.Fatalf("退避は「どれを送るか」のはず: %s", bs)
	}
	b.api.set(func(f *trFake) { f.status = 0 })
	b.vars["LOOPTRACK_TRANSCRIPT_THROTTLE_MIN"] = "0"
	before := countPrefix(b.api.requests(), "POST /im/api/v1/transcripts/7/chunks")
	b.run("Stop")
	if n := countPrefix(b.api.requests(), "POST /im/api/v1/transcripts/7/chunks"); n <= before {
		t.Fatalf("送れるようになったら送るはず: 追記 %d → %d", before, n)
	}
	if left := usagesnap.SpoolFiles(usagesnap.TranscriptSpoolDir(b.stateDir(), b.api.url(), "tst")); len(left) != 0 {
		t.Fatalf("送った記録の退避を消すはず: %v", left)
	}
	if f := usagesnap.ReadTranscriptFailure(b.stateDir()); f != nil {
		t.Fatalf("送れたら失敗の記録を消すはず: %+v", f)
	}
	// 7 日より古い退避は捨てる
	b.api.set(func(f *trFake) { f.status = 503 })
	b.run("SessionEnd")
	spool = usagesnap.SpoolFiles(usagesnap.TranscriptSpoolDir(b.stateDir(), b.api.url(), "tst"))
	if len(spool) != 1 {
		t.Fatalf("前提が崩れています: 退避が %d 件", len(spool))
	}
	old := b.now.Add(-8 * 24 * time.Hour)
	_ = os.Chtimes(spool[0], old, old)
	expireTranscriptFiles(b.stateDir(), b.now)
	if exists(spool[0]) {
		t.Fatal("7 日より古い退避を捨てるはず")
	}
}

// 変換の止め（読めない行）は退避せず、失敗を 1 回だけ記録する（件数だけ）。同じ記録は自動では試し直さない。
// 対照: 1 回目は失敗が記録される（止めが本当に起きている）。
func TestTranscriptRejectRecordedOnce(t *testing.T) {
	b := newTrBox(t)
	b.writeTranscript(fmt.Sprintf(`{"type":"user","sessionId":%q,"message":{"content":%q}}`, trSID, trMarker), "{not json "+trMarker+"}")
	b.vars["LOOPTRACK_SESSION_TRANSCRIPTS"] = "1"
	b.vars["LOOPTRACK_TRANSCRIPT_THROTTLE_MIN"] = "0"
	b.rememberRule(true)
	b.run("SessionEnd")
	f := usagesnap.ReadTranscriptFailure(b.stateDir())
	if f == nil || f.Reason != usagesnap.FailTranscriptRejected || f.Count != 1 || f.Files != 1 {
		t.Fatalf("変換の止めを 1 回・1 ファイルで記録するはず: %+v", f)
	}
	if len(b.api.requests()) != 0 {
		t.Fatalf("止めがあれば要求を 1 本も送らないはず: %v", b.api.requests())
	}
	if s := usagesnap.SpoolFiles(usagesnap.TranscriptSpoolDir(b.stateDir(), b.api.url(), "tst")); len(s) != 0 {
		t.Fatalf("変換の止めは退避しないはず: %v", s)
	}
	b.run("Stop")
	b.run("SessionEnd")
	if f := usagesnap.ReadTranscriptFailure(b.stateDir()); f == nil || f.Count != 1 {
		t.Fatalf("同じ記録の止めは 1 回だけ記録するはず: %+v", f)
	}
	bs, _ := os.ReadFile(filepath.Join(b.stateDir(), "transcript-failure.json"))
	if strings.Contains(string(bs), trMarker) {
		t.Fatal("失敗の記録に中身が入っています")
	}
}

// hook 自身は送らない（子を起こしてすぐ返る）。子を起こせなければ同じプロセスで送る（失わない）。
func TestTranscriptHookDoesNotSendItself(t *testing.T) {
	b := newTrBox(t)
	b.goodTranscript()
	b.vars["LOOPTRACK_SESSION_TRANSCRIPTS"] = "1"
	b.rememberRule(true)
	vars := b.env()
	var jobs [][]byte
	e := &Env{Vars: &vars, Getwd: func() string { return b.proj }, SpawnTranscript: func(job []byte, dir string) error {
		jobs = append(jobs, job)
		return nil
	}}
	in := fmt.Sprintf(`{"hook_event_name":"Stop","session_id":%q,"cwd":%q}`, trSID, b.proj)
	var out bytes.Buffer
	if code := Main(context.Background(), "transcript", []string{"--agent", "claude-code"}, strings.NewReader(in), &out, &out, e); code != 0 || out.Len() != 0 {
		t.Fatalf("hook は何も出さずに 0: %d %q", code, out.String())
	}
	if len(jobs) != 1 || len(b.api.requests()) != 0 {
		t.Fatalf("hook は子に渡すだけで、自分では送らないはず: 子 %d・要求 %v", len(jobs), b.api.requests())
	}
	var job transcriptJob
	if json.Unmarshal(jobs[0], &job) != nil || job.Trigger != "stop" || job.SessionID != trSID || job.Agent != "claude-code" {
		t.Fatalf("子に渡す仕事: %s", jobs[0])
	}
	// SubagentStop・Copilot は対象外
	e.SpawnTranscript = func([]byte, string) error { t.Error("対象外のイベントで子を起こしました"); return nil }
	Main(context.Background(), "transcript", []string{"--agent", "claude-code"},
		strings.NewReader(fmt.Sprintf(`{"hook_event_name":"SubagentStop","session_id":%q}`, trSID)), io.Discard, io.Discard, e)
	Main(context.Background(), "transcript", []string{"--agent", "copilot", "--event", "Stop"},
		strings.NewReader(fmt.Sprintf(`{"sessionId":%q}`, trSID)), io.Discard, io.Discard, e)
}

// 実行ファイルで、hook が子プロセスに切り離して返る（9 秒の打ち切りの前に返る）ことを確かめる。
// 登録の応答を止めておき、親がそれを待たずに終わること（順序）で見る。親が自分で送っていれば、止めている間は終わらない。
func TestTranscriptDetachEndToEnd(t *testing.T) {
	exe := buildLooptrack(t)
	b := newTrBox(t)
	b.goodTranscript()
	b.vars["LOOPTRACK_SESSION_TRANSCRIPTS"] = "1"
	b.rememberRule(true)
	hold := make(chan struct{})
	release := sync.OnceFunc(func() { close(hold) })
	defer release()
	b.api.set(func(f *trFake) { f.hold = hold })
	cmd := exec.Command(exe, "hook", "transcript", "--agent", "claude-code")
	cmd.Env = environ(b.vars)
	cmd.Dir = b.proj
	cmd.Stdin = strings.NewReader(fmt.Sprintf(`{"hook_event_name":"SessionEnd","session_id":%q,"cwd":%q}`, trSID, b.proj))
	type result struct {
		out []byte
		err error
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		out, err := cmd.CombinedOutput()
		done <- result{out, err}
	}()
	var r result
	select {
	case r = <-done:
	case <-time.After(60 * time.Second):
		release()
		<-done
		t.Fatal("親が子の送信（止めてある登録の応答）を待っていて終わらない")
	}
	if r.err != nil || len(r.out) > 0 {
		t.Fatalf("親: exit 0・出力なしのはず: %v %q", r.err, r.out)
	}
	t.Logf("親が終わるまで %s（登録の応答は止めたまま）", time.Since(start).Round(time.Millisecond))
	release()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		b.api.set(func(f *trFake) { n = f.chunked })
		if n > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("子が追記を送っていない: %v", b.api.requests())
}
