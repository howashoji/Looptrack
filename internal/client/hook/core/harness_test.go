package core

// core の hook の表駆動テストの仕掛け。
//
// 1 つのケース（scenario）は、一時ディレクトリ（sandbox: プロジェクト・HOME・XDG_CONFIG_HOME）と偽 API の準備と、順に行う手順（step）の並び。
// 手順は「偽 API のイシューを更新する・時刻を変える」などの操作か、hook / CLI の呼び出し（call）。Go 版の結果は want で確かめる。
// 以前は同じケースを前の実装（1.0.0 より前の hook と CLI の summary）にも与えて突き合わせていたが、
// 前の実装は撤去した。
//
// 環境は PATH・LANG・TZ と sandbox の HOME・XDG_CONFIG_HOME と、ケースが指定したもの（偽 API の URL・プロジェクト・トークン）だけ
// （本番に書き込ませない）。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/client/cred"
	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/hookio"
)

// testTZ は子プロセス（実行ファイル）に渡す TZ、testLocal は同じ時差の Go 側の現地時刻（+9 固定・夏時間なし）。
const testTZ = "JST-9"

var testLocal = time.FixedZone("JST", 9*3600)

// 現地時刻は +9 固定（テストを動かす機械の時差で結果を変えない）。
func TestMain(m *testing.M) {
	time.Local = testLocal
	os.Exit(m.Run())
}

// ------------------------------------------------------------------ 偽 API

type fakeIssue struct {
	status, title string
	updated       float64
}

type recorded struct {
	Method  string
	Path    string
	Body    any
	Headers map[string]string
}

// fakeAPI はイシュー管理サーバの代わり（/projects/<slug>・/activity・/projects/<slug>/usage・summary・install だけ）。
type fakeAPI struct {
	srv *httptest.Server
	mu  sync.Mutex
	// issues は {ID: 状態・タイトル・更新時刻}。activity は since より後に更新されていれば events_since=1 を返す。
	issues map[string]*fakeIssue
	reqs   []recorded
	// usageStatus は POST /usage の応答の状態（0 なら 201）。sendPrompts は応答の send_prompts（nil なら載せない）。
	usageStatus int
	sendPrompts *bool
	// projectStatus は GET /projects/<slug> の状態（0 なら 200）。activityBody があれば /activity はそれを返す。
	projectStatus int
	activityBody  string
	summaryBody   string
	installBody   string
	delay         time.Duration
	// usageHold が nil でなければ、POST …/usage の応答をそれが閉じられるまで返さない（記録は先にする）。
	// 送信の途中で止めた状態を時間に頼らず作る（TestDetachEndToEnd）。止めている間に送り手が
	// 接続を切った（送り手のプロセスが終わった・打ち切った）数を usageAbandoned に数える
	usageHold      chan struct{}
	usageAbandoned int
}

const basePath = "/im/api/v1"

func newFakeAPI(t *testing.T) *fakeAPI {
	f := &fakeAPI{issues: map[string]*fakeIssue{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) url() string { return f.srv.URL + "/im" }

func (f *fakeAPI) requests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.reqs...)
}

func (f *fakeAPI) setUpdated(id string, t float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.issues[id].updated = t
}

func (f *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var v any
	if len(body) > 0 {
		if json.Unmarshal(body, &v) != nil {
			v = string(body)
		}
	}
	hs := map[string]string{"Authorization": r.Header.Get("Authorization")}
	for k := range r.Header {
		if strings.HasPrefix(strings.ToLower(k), "X-Looptrack-") {
			hs[http.CanonicalHeaderKey(k)] = r.Header.Get(k)
		}
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, recorded{Method: r.Method, Path: r.URL.RequestURI(), Body: v, Headers: hs})
	delay := f.delay
	hold := f.usageHold
	f.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	if hold != nil && r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/usage") {
		select {
		case <-hold:
		case <-r.Context().Done():
			f.mu.Lock()
			f.usageAbandoned++
			f.mu.Unlock()
		}
	}
	reply := func(status int, s string) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		io.WriteString(w, s)
	}
	if r.Header.Get("Authorization") != "Bearer test-token" {
		reply(401, `{"error":{"code":"unauthorized","message":"認証が必要です"}}`)
		return
	}
	p := strings.TrimPrefix(r.URL.Path, basePath)
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == "GET" && p == "/activity":
		if f.activityBody != "" {
			reply(200, f.activityBody)
			return
		}
		since, _ := strconv.ParseFloat(r.URL.Query().Get("since"), 64)
		items := []map[string]any{}
		for _, id := range strings.Split(r.URL.Query().Get("ids"), ",") {
			it := f.issues[strings.ToUpper(id)]
			if it == nil {
				continue
			}
			ev := 0
			if it.updated > since {
				ev = 1
			}
			items = append(items, map[string]any{"id": strings.ToUpper(id), "project": "tst", "title": it.title, "status": it.status,
				"closed": it.status == "Done" || it.status == "Canceled", "last_at": "", "last_epoch": it.updated,
				"last_kind": "comment", "last_via": "cli", "events_since": ev})
		}
		b, _ := json.Marshal(map[string]any{"items": items, "now_epoch": 0})
		reply(200, string(b))
	case r.Method == "POST" && strings.HasSuffix(p, "/usage"):
		st := f.usageStatus
		if st == 0 {
			st = 201
		}
		if st >= 300 {
			reply(st, fmt.Sprintf(`{"error":{"code":"x","message":"HTTP %d のテスト"}}`, st))
			return
		}
		res := map[string]any{"id": 1, "duplicate": false}
		if f.sendPrompts != nil {
			res["send_prompts"] = *f.sendPrompts
		}
		b, _ := json.Marshal(res)
		reply(st, string(b))
	case r.Method == "GET" && strings.HasSuffix(p, "/summary"):
		reply(200, f.summaryBody)
	case r.Method == "POST" && strings.HasSuffix(p, "/install"):
		reply(200, f.installBody)
	case r.Method == "GET" && strings.HasPrefix(p, "/projects/"):
		if f.projectStatus != 0 {
			reply(f.projectStatus, `{"error":{"code":"not_found","message":"プロジェクトがありません"}}`)
			return
		}
		slug := strings.TrimPrefix(p, "/projects/")
		reply(200, fmt.Sprintf(`{"slug":%q,"name":%q,"description":"","prefix":"TST","width":4,"role":"editor","counter":9,"rules":null}`, slug, slug))
	default:
		reply(404, `{"error":{"code":"not_found","message":"ありません"}}`)
	}
}

// ------------------------------------------------------------------ sandbox

type sandbox struct {
	t    *testing.T
	dir  string // 実パス
	proj string
	home string
	xdg  string
	api  *fakeAPI
}

func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := &sandbox{t: t, dir: d, proj: filepath.Join(d, "proj"), home: filepath.Join(d, "home"), api: newFakeAPI(t)}
	s.xdg = filepath.Join(s.home, ".config")
	for _, p := range []string{filepath.Join(s.proj, ".claude"), s.xdg} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func (s *sandbox) p(parts ...string) string {
	return filepath.Join(append([]string{s.proj}, parts...)...)
}

// appDir は looptrack の置き場（資格情報と同じ。usage-spool・usage-last を置く）。
func (s *sandbox) appDir() string {
	s.t.Helper()
	p, err := cred.DefaultPaths(env.FromMap(s.envMap(call{})))
	if err != nil {
		s.t.Fatal(err)
	}
	return filepath.Dir(p.Primary)
}

func (s *sandbox) scope(sid string) string {
	return s.p(".claude", ".looptrack-freshness", "sessions", sid)
}

func (s *sandbox) write(path, text string) {
	s.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		s.t.Fatal(err)
	}
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// ------------------------------------------------------------------ 呼び出し

// call は hook か CLI の 1 回の呼び出し。
type call struct {
	hook  string // 登録表の名前（issue-freshness-mark など）。空なら cli
	cli   []string
	agent hookio.Agent
	args  []string // hook ごとの引数（--client・--event・--limit）
	input any      // stdin（map なら JSON にする。string ならそのまま）
	env   map[string]string
	// noAPI は LOOPTRACK_API_URL などを渡さない。downAPI は繋がらない URL を渡す。noToken はトークンを渡さない。
	noAPI, downAPI, noToken bool
	bare                    bool // CLAUDE_PROJECT_DIR・CLAUDECODE を付けない（Claude Code 以外が起動した状況）
	timeout                 time.Duration
}

type got struct {
	out  hookio.Output
	res  hookio.Result
	code int
}

func (c call) agentOrDefault() hookio.Agent {
	if c.agent == "" {
		return hookio.ClaudeCode
	}
	return c.agent
}

func (c call) stdin() string {
	switch v := c.input.(type) {
	case nil:
		return "{}"
	case string:
		return v
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(c.input)
	return b.String()
}

// eventName は出力を読むときのイベント（入力の hook_event_name → --event → 登録表）。
func (c call) eventName() hookio.Name {
	if m, ok := c.input.(map[string]any); ok {
		if s, _ := m["hook_event_name"].(string); s != "" {
			return hookio.NameOf(s)
		}
	}
	for i, a := range c.args {
		if a == "--event" && i+1 < len(c.args) {
			return hookio.NameOf(c.args[i+1])
		}
	}
	e, _ := Lookup(c.hook)
	return e.Event
}

// envMap は子プロセス・Go 版に渡す環境。
func (s *sandbox) envMap(c call) map[string]string {
	// USERPROFILE: Windows の ~
	// LOOPTRACK_LANG: 下の検査は日本語の文面を見る（LANG が C.UTF-8 なので、指定が無ければ英語が出る）
	m := map[string]string{"PATH": os.Getenv("PATH"), "HOME": s.home, "USERPROFILE": s.home, "XDG_CONFIG_HOME": s.xdg, "TZ": testTZ,
		"LANG": "C.UTF-8", "LOOPTRACK_LANG": "ja", "LOOPTRACK_USAGE_FOREGROUND": "1"}
	if !c.bare {
		m["CLAUDE_PROJECT_DIR"] = s.proj
		if c.agentOrDefault() == hookio.ClaudeCode {
			m["CLAUDECODE"] = "1"
		}
	}
	if !c.noAPI {
		m["LOOPTRACK_API_URL"] = s.api.url()
		if c.downAPI {
			m["LOOPTRACK_API_URL"] = "http://127.0.0.1:1/im"
		}
		m["LOOPTRACK_PROJECT"] = "tst"
		if !c.noToken {
			m["LOOPTRACK_TOKEN"] = "test-token"
		}
	}
	for k, v := range c.env {
		if v == "" {
			delete(m, k)
			continue
		}
		m[k] = v
	}
	return m
}

func (s *sandbox) runGo(c call) got {
	m := s.envMap(c)
	// 同じプロセスで動かす Go 版には TZ を渡さない。TZ が POSIX 形式（testTZ の JST-9）だと、summary が呼ぶ cli.Main
	// （internal/client/cli の fixLocalZone）が time.Local を書き換え、偽 API（httptest）の goroutine の time.Now の
	// 読みと競合する（-race の DATA RACE）。現地時刻は TestMain が time.Local = testLocal（TZ と同じ +9 固定）に
	// してあるので、書き換えが無くても出力は変わらない。実行ファイル（子プロセス）には TZ を渡したまま。
	delete(m, "TZ")
	vars := env.FromMap(m)
	e := &Env{Vars: &vars, Getwd: func() string { return s.proj }, Sleep: func(time.Duration) {},
		Spawn: func([]byte, string) error { return fmt.Errorf("テストでは切り離さない") }}
	var stdout, stderr bytes.Buffer
	if c.hook == "" {
		code := FreshnessMain(context.Background(), c.cli, strings.NewReader(c.stdin()), &stdout, &stderr, e)
		return got{out: hookio.Output{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: code}, code: code}
	}
	args := append([]string{"--agent", string(c.agentOrDefault())}, c.args...)
	code := mainWith(context.Background(), c.hook, args, strings.NewReader(c.stdin()), &stdout, &stderr, e, func(o *hookio.RunOptions) {
		if c.timeout > 0 {
			o.Timeout = c.timeout
		}
	})
	o := hookio.Output{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: code}
	return got{out: o, res: hookio.ParseOutput(c.agentOrDefault(), c.eventName(), o), code: code}
}

// ------------------------------------------------------------------ ケース

type step struct {
	name string
	do   func(s *sandbox)
	mk   func(s *sandbox) call
	want func(t *testing.T, s *sandbox, g got)
}

type scenario struct {
	name  string
	setup func(s *sandbox)
	steps []step
}

func (sc scenario) run(t *testing.T) {
	t.Helper()
	t.Run(sc.name, func(t *testing.T) {
		a := newSandbox(t)
		if sc.setup != nil {
			sc.setup(a)
		}
		for i, st := range sc.steps {
			label := strings.ReplaceAll(st.name, " ", "_")
			if label == "" {
				label = fmt.Sprintf("step%d", i)
			}
			if st.do != nil {
				st.do(a)
			}
			if st.mk == nil {
				continue
			}
			ca := st.mk(a)
			ga := a.runGo(ca)
			if ca.hook != "" && ga.code != 0 {
				t.Errorf("手順 %d（%s）: hook の終了コードが 0 でない: %d", i, label, ga.code)
			}
			if st.want != nil {
				t.Run(label, func(t *testing.T) { st.want(t, a, ga) })
			}
		}
	})
}

// ------------------------------------------------------------------ よく使う確かめ方

func wantQuiet(t *testing.T, _ *sandbox, g got) {
	t.Helper()
	if g.out.Stdout != "" || g.out.Stderr != "" {
		t.Errorf("何も出さないはず: %q %q", g.out.Stdout, g.out.Stderr)
	}
}

func wantBlock(needles ...string) func(*testing.T, *sandbox, got) {
	return func(t *testing.T, _ *sandbox, g got) {
		t.Helper()
		if g.res.Block == "" {
			t.Fatalf("差し戻すはず: %q", g.out.Stdout)
		}
		for _, n := range needles {
			if !strings.Contains(g.res.Block, n) {
				t.Errorf("理由に「%s」が無い:\n%s", n, g.res.Block)
			}
		}
	}
}

func wantStdout(needle string, want bool) func(*testing.T, *sandbox, got) {
	return func(t *testing.T, _ *sandbox, g got) {
		t.Helper()
		if strings.Contains(g.out.Stdout, needle) != want {
			t.Errorf("標準出力に「%s」がある=%v のはず:\n%s", needle, want, g.out.Stdout)
		}
	}
}
