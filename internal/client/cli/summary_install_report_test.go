package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/usagesnap"
)

// installReportServer は要約と導入済み通知だけに答えるサーバ。trigger=hook の通知だけを delay だけ遅らせ、status で答える。
type installReportServer struct {
	mu       sync.Mutex
	summary  time.Duration // 要約の GET を遅らせる時間
	delay    time.Duration
	status   int
	triggers []string // 受け取って答えた通知の trigger（遅らせた要求は答え終えてから数える）
}

func (s *installReportServer) set(delay time.Duration, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.delay, s.status = delay, status
}

func (s *installReportServer) got() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.triggers...)
}

func (s *installReportServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/projects/demo/summary"):
		s.mu.Lock()
		wait := s.summary
		s.mu.Unlock()
		if wait > 0 {
			select {
			case <-time.After(wait):
			case <-r.Context().Done():
				return // 打ち切られた
			}
		}
		_, _ = io.WriteString(w, `{"in_progress":[],"in_review":[],"ready":[],"ready_total":0,"counts":{"open":0,"open_bugs":0}}`)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/projects/demo/install"):
		var body struct{ Trigger string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		delay, status := s.delay, s.status
		s.mu.Unlock()
		if body.Trigger != "hook" {
			delay, status = 0, http.StatusOK // 手で送る通知（installed）はいつも受け取る
		}
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return // 打ち切られた（届いていない）
			}
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"error":{"code":"x","message":"boom"}}`)
			return
		}
		s.mu.Lock()
		s.triggers = append(s.triggers, body.Trigger)
		s.mu.Unlock()
		_, _ = io.WriteString(w, `{"state":"no_hook","message":"導入状態のメッセージ"}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func installReportEnv(t *testing.T, url string) map[string]string {
	home := t.TempDir()
	return map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, "cfg"), "APPDATA": filepath.Join(home, "appdata"),
		"LOOPTRACK_API_URL": url, "LOOPTRACK_PROJECT": "demo", "LOOPTRACK_TOKEN": "imp_test", "LOOPTRACK_LANG": "ja",
		"CLAUDE_PROJECT_DIR": t.TempDir()}
}

// TestHookInstallReportSurvivesSlowServer は、要約の 2 秒を超えて答えるサーバにも trigger=hook の通知が届くこと。
// 対照として、要約の打ち切り（summaryTimeout）の client で同じ通知を送ると届かないことを同じテストで見る
// （対照が届いてしまうなら、遅延がこの修正を試せていない）。
func TestHookInstallReportSurvivesSlowServer(t *testing.T) {
	srv := &installReportServer{}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	delay := summaryTimeout + 700*time.Millisecond
	if delay >= installReportTimeout {
		t.Fatalf("遅延 %v が通知の打ち切り %v に届いています（前提が崩れています）", delay, installReportTimeout)
	}
	srv.set(delay, http.StatusOK)
	m := installReportEnv(t, ts.URL)

	// 対照: 要約の client のまま送ると時間切れになる
	c := &Ctx{Env: env.FromMap(m), Root: m["CLAUDE_PROJECT_DIR"]}
	old, err := api.NewWithTimeout(c.Env, summaryTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.reportInstall(old, "claude-code", "hook"); err == nil {
		t.Fatal("要約の 2 秒の client で遅いサーバに届いてしまいました（前提が崩れています）")
	}
	if got := srv.got(); len(got) != 0 {
		t.Fatalf("対照の通知が届いています（前提が崩れています）: %v", got)
	}

	code, stdout, stderr := run(t, m, "summary", "--agent", "claude-code", "--json")
	if code != 0 {
		t.Fatalf("summary: %d %q", code, stderr)
	}
	if got := srv.got(); len(got) != 1 || got[0] != "hook" {
		t.Errorf("trigger=hook の通知が届いていません: %v", got)
	}
	if !strings.Contains(stdout, `"install"`) || strings.Contains(stdout, "install_report_failure") {
		t.Errorf("要約に導入状態が載り、失敗は載らないこと: %s", stdout)
	}
}

// TestHookInstallReportFailureIsShown は、届かなかった通知が手元に記録され、要約（JSON と文面）と installed（文面と --json）に
// 出ること。届いたら記録が消えて出なくなることも同じテストで見る。
func TestHookInstallReportFailureIsShown(t *testing.T) {
	srv := &installReportServer{}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	m := installReportEnv(t, ts.URL)
	e := env.FromMap(m)
	state := usagesnap.StateDir(e)
	if state == "" {
		t.Fatal("置き場が分かりません（前提が崩れています）")
	}
	const line = "フックからの導入済み通知がサーバに届いていません"

	// 拒否（403）が 2 回続く
	srv.set(0, http.StatusForbidden)
	for i := 0; i < 2; i++ {
		if code, _, stderr := run(t, m, "summary", "--agent", "claude-code"); code != 0 {
			t.Fatalf("summary: %d %q", code, stderr)
		}
	}
	f := usagesnap.ReadInstallFailure(state, m["LOOPTRACK_API_URL"], "demo", "claude-code")
	if f == nil || f.Reason != usagesnap.FailRejected || f.Status != 403 || f.Count != 2 {
		t.Fatalf("失敗の記録: %+v", f)
	}
	// 別の AI の分は別の記録（この組の失敗を混ぜない）
	if g := usagesnap.ReadInstallFailure(state, m["LOOPTRACK_API_URL"], "demo", "codex"); g != nil {
		t.Errorf("codex の記録は無いこと: %+v", g)
	}
	_, stdout, _ := run(t, m, "summary", "--agent", "claude-code")
	if !strings.Contains(stdout, line) || !strings.Contains(stdout, "サーバが拒否（HTTP 403）") || !strings.Contains(stdout, "続けて 3 回") {
		t.Errorf("要約の文面に失敗が出ていません:\n%s", stdout)
	}
	_, stdout, _ = run(t, m, "summary", "--agent", "claude-code", "--json")
	if !strings.Contains(stdout, `"install_report_failure"`) || !strings.Contains(stdout, `"reason":"rejected"`) && !strings.Contains(stdout, `"reason": "rejected"`) {
		t.Errorf("要約の JSON に失敗が出ていません:\n%s", stdout)
	}
	// installed は手で送る通知なので届くが、フックからの通知の失敗は添えて、記録は消さない
	code, stdout, stderr := run(t, m, "installed", "--agent", "claude-code")
	if code != 0 || !strings.Contains(stdout, "導入状態のメッセージ") || !strings.Contains(stdout, line) {
		t.Errorf("installed の文面: %d %q %q", code, stdout, stderr)
	}
	code, stdout, _ = run(t, m, "installed", "--agent", "claude-code", "--json")
	if code != 0 || !strings.Contains(stdout, `"hook_report_failure"`) {
		t.Errorf("installed --json: %d %s", code, stdout)
	}
	if usagesnap.ReadInstallFailure(state, m["LOOPTRACK_API_URL"], "demo", "claude-code") == nil {
		t.Error("手で送った通知で、フックの失敗の記録を消さないこと")
	}

	// 届いたら消えて、どちらにも出ない
	srv.set(0, http.StatusOK)
	_, stdout, _ = run(t, m, "summary", "--agent", "claude-code")
	if strings.Contains(stdout, line) {
		t.Errorf("届いた後も失敗が出ています:\n%s", stdout)
	}
	if f := usagesnap.ReadInstallFailure(state, m["LOOPTRACK_API_URL"], "demo", "claude-code"); f != nil {
		t.Errorf("届いた後も記録が残っています: %+v", f)
	}
	_, stdout, _ = run(t, m, "installed", "--agent", "claude-code", "--json")
	if strings.Contains(stdout, "hook_report_failure") {
		t.Errorf("届いた後の installed --json: %s", stdout)
	}
}

// TestInstallFailReason は失敗の種類の振り分け（5xx と接続の失敗は届かない、4xx は拒否）。
func TestInstallFailReason(t *testing.T) {
	cases := []struct {
		err    error
		reason string
		status int
	}{
		{&api.Error{Status: 403}, usagesnap.FailRejected, 403},
		{&api.Error{Status: 503}, usagesnap.FailUnreachable, 503},
		{&api.ConnError{URL: "http://x", Reason: "timeout"}, usagesnap.FailUnreachable, 0},
		{&api.NoTokenError{URL: "http://127.0.0.1:1"}, usagesnap.FailUnreachable, 0},
		{io.ErrUnexpectedEOF, usagesnap.FailClient, 0},
	}
	for _, tc := range cases {
		if r, s := installFailReason(tc.err); r != tc.reason || s != tc.status {
			t.Errorf("%T: %s %d（期待 %s %d）", tc.err, r, s, tc.reason, tc.status)
		}
	}
}

// TestHookInstallReportWhenSummaryTimesOut は、要約の GET が遅くて切れても trigger=hook の通知が届くこと、
// 届かなければ失敗が手元に残ること。対照として、要約が本当に切れた（出力が空）ことを同じテストで見る。
func TestHookInstallReportWhenSummaryTimesOut(t *testing.T) {
	srv := &installReportServer{}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	srv.mu.Lock()
	srv.summary = summaryTimeout + 700*time.Millisecond
	srv.mu.Unlock()
	srv.set(0, http.StatusOK)
	m := installReportEnv(t, ts.URL)
	state := usagesnap.StateDir(env.FromMap(m))

	// 届くとき
	code, stdout, stderr := run(t, m, "summary", "--agent", "claude-code")
	if code != 0 || strings.TrimSpace(stdout) != "" {
		t.Fatalf("要約が切れていません（前提が崩れています）: %d %q %q", code, stdout, stderr)
	}
	if got := srv.got(); len(got) != 1 || got[0] != "hook" {
		t.Errorf("要約が切れたときも trigger=hook の通知が届くこと: %v", got)
	}
	if f := usagesnap.ReadInstallFailure(state, m["LOOPTRACK_API_URL"], "demo", "claude-code"); f != nil {
		t.Errorf("届いたのに失敗が残っています: %+v", f)
	}

	// 届かないとき（500）は記録が残る
	srv.set(0, http.StatusInternalServerError)
	if code, _, stderr := run(t, m, "summary", "--agent", "claude-code"); code != 0 {
		t.Fatalf("summary: %d %q", code, stderr)
	}
	f := usagesnap.ReadInstallFailure(state, m["LOOPTRACK_API_URL"], "demo", "claude-code")
	if f == nil || f.Reason != usagesnap.FailUnreachable || f.Status != 500 || f.Count != 1 {
		t.Errorf("要約が切れて通知も届かないときの記録: %+v", f)
	}
	// 手で打った要約（--agent なし）は通知を送らない
	before := len(srv.got())
	run(t, m, "summary")
	if got := srv.got(); len(got) != before {
		t.Errorf("--agent なしでは通知を送らないこと: %v", got)
	}
}
