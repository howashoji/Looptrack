package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/store"
)

// 鮮度ガード（looptrack issue-freshness）を、実際のサーバ・DB に対して通す。
// 判定の細かなケースは internal/client/hook/core の偽 API のテストが受け持つ。ここではサーバの更新イベントでの判定を見る。

func TestFreshnessGuardAPIMode(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("req")
	u := e.user("guard", "guard-password-1", "member")
	if err := store.SetMember(ctx, e.db, pr.ID, u.ID, "editor"); err != nil {
		t.Fatal(err)
	}
	a := e.apiAs(u)
	a.json(201, "POST", "/projects/req/issues", map[string]any{"title": "参照されるイシュー"}, nil)
	a.json(201, "POST", "/projects/req/issues", map[string]any{"title": "閉じたイシュー"}, nil)
	a.json(200, "POST", "/issues/REQ-0002/status", map[string]any{"status": "Done"}, nil)

	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".claude"), 0o755)
	env := append(cliAPIEnv(e.srv.URL+"/im", "req", a.token, t.TempDir()), "CLAUDE_PROJECT_DIR="+root)
	hook := func(args []string, payload map[string]any) (int, string) {
		t.Helper()
		stdin := ""
		if payload != nil {
			b, _ := json.Marshal(payload)
			stdin = string(b)
		}
		r := runCLI(t, root, env, stdin, append([]string{"issue-freshness"}, args...)...)
		return r.code, r.stdout + r.stderr
	}
	engage := func(id string) {
		hook([]string{"mark"}, map[string]any{"hook_event_name": "PostToolUse", "session_id": "s1", "tool_name": "Bash",
			"tool_input": map[string]any{"command": "looptrack issue show " + id}})
	}
	work := func() {
		hook([]string{"mark"}, map[string]any{"hook_event_name": "PostToolUse", "session_id": "s1", "tool_name": "Edit",
			"tool_input": map[string]any{"file_path": filepath.Join(root, "src/main.go")}})
	}
	// stop は Stop の check。差し戻し（decision: block）なら 2 と理由を返す（以前の CLI の exit 2 + stderr に当たる）
	stop := func() (int, string) {
		t.Helper()
		code, out := hook([]string{"check"}, map[string]any{"hook_event_name": "Stop", "session_id": "s1"})
		var res struct {
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
		}
		if code == 0 && strings.TrimSpace(out) != "" && json.Unmarshal([]byte(out), &res) == nil && res.Decision == "block" {
			return 2, res.Reason
		}
		return code, out
	}
	cli := func(args ...string) {
		t.Helper()
		e.clock.mu.Lock() // サーバの時計を実時刻に合わせる（イベントの時刻でセッション開始と比べるため）
		e.clock.t = time.Now().UTC()
		e.clock.mu.Unlock()
		if r := runCLI(t, root, env, "", append([]string{"issue"}, args...)...); r.code != 0 {
			t.Fatalf("looptrack issue %v: exit %d %s%s", args, r.code, r.stdout, r.stderr)
		}
	}

	// 参照しただけ・実作業なしなら止めない
	engage("REQ-0001")
	if code, out := stop(); code != 0 {
		t.Fatalf("参照のみ: %d %s", code, out)
	}
	// 参照して実作業をしたのに更新しないまま終えると止まる
	engage("REQ-0001")
	work()
	code, out := stop()
	if code != 2 || !strings.Contains(out, "REQ-0001 [Todo] 参照されるイシュー") {
		t.Fatalf("未更新: %d %s", code, out)
	}
	// サーバにコメントを残せば通る（判定はサーバの更新イベント）
	cli("comment", "REQ-0001", "調査した内容")
	if code, out := stop(); code != 0 {
		t.Fatalf("更新後: %d %s", code, out)
	}
	// クローズ済みは更新を求めない
	engage("REQ-0002")
	work()
	if code, out := stop(); code != 0 {
		t.Fatalf("クローズ済み: %d %s", code, out)
	}
	// 状態変更でも「更新した」とみなす
	engage("REQ-0001")
	work()
	cli("status", "REQ-0001", "In Progress")
	if code, out := stop(); code != 0 {
		t.Fatalf("状態変更後: %d %s", code, out)
	}
	// show の表示（モードと対象）
	if _, out := hook([]string{"show"}, nil); !strings.Contains(out, "mode    : API（"+e.srv.URL+"/im・req）") {
		t.Errorf("show: %s", out)
	}
}
