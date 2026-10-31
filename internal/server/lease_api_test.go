package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/client/cli"
	clienv "github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

// 占有の REST（lease_api.go）・画面（lease_web.go）・CLI（client/cli/lease.go）。規則の本体は service（lease.go）にあり、
// ここでは HTTP の形（状態コード・code・current）と、summary に持ち主が出ることを確かめる。

func TestLeaseREST(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("lea")
	member := func(login, role string) store.User {
		u := e.user(login, login+"-password-1", "member")
		if role != "" {
			if err := store.SetMember(ctx, e.db, pr.ID, u.ID, role); err != nil {
				t.Fatal(err)
			}
		}
		return u
	}
	ed, ed2, vi := e.apiAs(member("lea-ed", "editor")), e.apiAs(member("lea-ed2", "editor")), e.apiAs(member("lea-vi", "viewer"))
	stray := e.apiAs(member("lea-stray", ""))

	var got struct {
		Lease leaseJSON `json:"lease"`
	}
	body := map[string]any{"purpose": "dev へ反映する", "ttl_seconds": 600, "machine_label": "box-a"}
	var taken struct {
		Lease leaseJSON `json:"lease"`
		Token string    `json:"lease_token"`
	}
	ed.json(http.StatusCreated, "POST", "/projects/lea/leases/deploy.dev", body, &taken)
	got.Lease = taken.Lease
	if taken.Token == "" {
		t.Fatal("取った者に鍵が返っていない")
	}
	if got.Lease.Holder != "lea-ed" || got.Lease.MachineLabel != "box-a" || got.Lease.ExpiresAt == "" || got.Lease.Expired {
		t.Fatalf("取る: %+v", got.Lease)
	}

	// もう一方は 409 lease_held で、current に取っている者と期限が返る
	code, _, b := ed2.do("POST", "/projects/lea/leases/deploy.dev", body)
	var eb memErrBody
	_ = json.Unmarshal(b, &eb)
	var cur leaseJSON
	_ = json.Unmarshal(eb.Error.Current, &cur)
	if code != http.StatusConflict || eb.Error.Code != "lease_held" || cur.Holder != "lea-ed" || cur.ExpiresAt != got.Lease.ExpiresAt || cur.Purpose != "dev へ反映する" {
		t.Fatalf("取り合い: %d %s", code, b)
	}

	// viewer は取れない・外せないが読める。参加していない利用者は 404
	if code, _, b := vi.do("POST", "/projects/lea/leases/other", body); code != http.StatusForbidden {
		t.Errorf("viewer の取得: %d %s", code, b)
	}
	if code, _, b := vi.do("POST", "/projects/lea/leases/deploy.dev/break", map[string]any{"reason": "外したい"}); code != http.StatusForbidden {
		t.Errorf("viewer の外す: %d %s", code, b)
	}
	if code, _, b := stray.do("GET", "/projects/lea/leases", nil); code != http.StatusNotFound {
		t.Errorf("参加していない利用者: %d %s", code, b)
	}
	var list struct {
		Leases []leaseJSON `json:"leases"`
		Count  int         `json:"count"`
	}
	vi.json(http.StatusOK, "GET", "/projects/lea/leases", nil, &list)
	if list.Count != 1 || list.Leases[0].Name != "deploy.dev" {
		t.Fatalf("一覧: %+v", list)
	}

	// summary に持ち主・端末・目的・期限が出る
	var sum struct {
		Leases []leaseJSON `json:"leases"`
	}
	vi.json(http.StatusOK, "GET", "/projects/lea/summary", nil, &sum)
	if len(sum.Leases) != 1 || sum.Leases[0].Holder != "lea-ed" || sum.Leases[0].MachineLabel != "box-a" || sum.Leases[0].Purpose == "" || sum.Leases[0].ExpiresAt == "" {
		t.Fatalf("summary: %+v", sum)
	}

	// 取っていない者の返却は 409。持ち主は延ばせる
	if code, _, b := ed2.do("DELETE", "/projects/lea/leases/deploy.dev", nil); code != http.StatusConflict || !strings.Contains(string(b), "lease_not_holder") {
		t.Errorf("他人の返却: %d %s", code, b)
	}
	ed.json(http.StatusOK, "POST", "/projects/lea/leases/deploy.dev/extend", map[string]any{"ttl_seconds": 1200, "lease_token": taken.Token}, &got)

	// 鍵は取った者の応答にだけ出す（取り合いの 409・一覧・1 件・summary のどれにも出ない）。対照は取ったときの応答
	if _, _, held := ed2.do("POST", "/projects/lea/leases/deploy.dev", body); strings.Contains(string(held), taken.Token) || !strings.Contains(string(held), "lease_held") {
		t.Errorf("409: %s", held)
	}
	for _, path := range []string{"/projects/lea/leases", "/projects/lea/leases/deploy.dev", "/projects/lea/summary"} {
		code, _, b := vi.do("GET", path, nil)
		if code != http.StatusOK || strings.Contains(string(b), taken.Token) || strings.Contains(string(b), "token") {
			t.Errorf("%s: %d に鍵が出た", path, code)
		}
	}

	// 理由なしの外すは 400、理由つきは通り、記録に残る
	if code, _, b := ed2.do("POST", "/projects/lea/leases/deploy.dev/break", map[string]any{}); code != http.StatusBadRequest {
		t.Errorf("理由なし: %d %s", code, b)
	}
	ed2.json(http.StatusOK, "POST", "/projects/lea/leases/deploy.dev/break", map[string]any{"reason": "端末が止まった", "machine_label": "box-b"}, nil)
	var detail struct {
		Lease  leaseJSON        `json:"lease"`
		Events []leaseEventJSON `json:"events"`
	}
	vi.json(http.StatusOK, "GET", "/projects/lea/leases/deploy.dev", nil, &detail)
	if detail.Lease.Holder != "" || len(detail.Events) != 3 || detail.Events[0].Action != "break" || detail.Events[0].Reason != "端末が止まった" || detail.Events[0].PrevUser != "lea-ed" {
		t.Fatalf("記録: %+v", detail)
	}
	vi.json(http.StatusOK, "GET", "/projects/lea/summary", nil, &sum)
	if len(sum.Leases) != 0 {
		t.Fatalf("外した後の summary: %+v", sum)
	}
	if code, _, b := ed.do("DELETE", "/projects/lea/leases/never", nil); code != http.StatusNotFound {
		t.Errorf("行の無い返却: %d %s", code, b)
	}
}

// MCP の 5 つのツール。取り合いの負けはツールの実行エラーで、文面に持ち主が出る。別のセッションは返せない。
func TestLeaseMCP(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("leam")
	u := e.user("leam-ed", "editor-password-1", "member")
	if err := store.SetMember(ctx, e.db, pr.ID, u.ID, "editor"); err != nil {
		t.Fatal(err)
	}
	token := e.apiAs(u).token
	a := e.mcpAs(token, map[string]string{"X-Looptrack-Project": "leam", "X-Looptrack-Session": "sess-a", "Accept-Language": "ja"})
	b := e.mcpAs(token, map[string]string{"X-Looptrack-Project": "leam", "X-Looptrack-Session": "sess-b", "Accept-Language": "ja"})
	a.call("acquire_lease", map[string]any{"name": "publish", "purpose": "公開の PR を作る", "machine_label": "box-a"}, false)
	if text, _ := b.call("acquire_lease", map[string]any{"name": "publish", "purpose": "別の作業"}, true); !strings.Contains(text, "leam-ed") || !strings.Contains(text, "公開の PR を作る") {
		t.Fatalf("取り合い: %q", text)
	}
	if text, _ := b.call("list_leases", map[string]any{}, false); !strings.Contains(text, "publish") || !strings.Contains(text, "box-a") {
		t.Fatalf("一覧: %q", text)
	}
	if text, _ := a.call("project_summary", map[string]any{}, false); !strings.Contains(text, "publish") || !strings.Contains(text, "公開の PR を作る") {
		t.Fatalf("summary: %q", text)
	}
	b.call("release_lease", map[string]any{"name": "publish"}, true)
	a.call("extend_lease", map[string]any{"name": "publish", "ttl_seconds": 1200}, false)
	a.call("release_lease", map[string]any{"name": "publish"}, false)
	// 接続が張り直されてセッションが変わっても、鍵で返せる。鍵なしは拒む（対照）
	text, _ := a.call("acquire_lease", map[string]any{"name": "again", "purpose": "鍵で返す"}, false)
	m := regexp.MustCompile(`[0-9a-f]{32}`).FindString(text)
	if m == "" {
		t.Fatalf("鍵が返っていない: %q", text)
	}
	b.call("release_lease", map[string]any{"name": "again"}, true)
	b.call("extend_lease", map[string]any{"name": "again", "lease_token": m}, false)
	b.call("release_lease", map[string]any{"name": "again", "lease_token": m}, false)
	b.call("acquire_lease", map[string]any{"name": "publish", "purpose": "別の作業"}, false)
	a.call("break_lease", map[string]any{"name": "publish"}, true) // 理由なしは拒む
	a.call("break_lease", map[string]any{"name": "publish", "reason": "止まっている"}, false)
}

// 期限切れの占有は list_leases の本文に印が付く。切れる前の行には付かない（対照）。
func TestLeaseLinesMarkExpired(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 10, 30, 3, 0, 0, 0, loc)
	st := func(expired bool) service.LeaseState {
		return service.LeaseState{Lease: store.Lease{Name: "a", HolderUserID: 1, ExpiresAt: now}, Expired: expired}
	}
	if got := leaseLines(i18n.JA, []service.LeaseState{st(true)}, loc); !strings.Contains(got, "期限切れ") {
		t.Errorf("期限切れ: %q", got)
	}
	if got := leaseLines(i18n.JA, []service.LeaseState{st(false)}, loc); strings.Contains(got, "期限切れ") {
		t.Errorf("切れる前: %q", got)
	}
}

func TestLeasePages(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("leaw")
	u := e.user("leaw-ed", "editor-password-1", "member")
	if err := store.SetMember(ctx, e.db, pr.ID, u.ID, "editor"); err != nil {
		t.Fatal(err)
	}
	e.apiAs(u).json(http.StatusCreated, "POST", "/projects/leaw/leases/publish", map[string]any{"purpose": "公開の PR を作る", "machine_label": "box-w"}, nil)
	c, board, _ := e.boardSession("leaw-ed", "editor-password-1", "leaw")
	if !strings.Contains(board, `href="/im/p/leaw/leases"`) {
		t.Error("ボードに占有の画面へのリンクが無い")
	}
	for path, want := range map[string]string{"/im/p/leaw/leases": "公開の PR を作る", "/im/p/leaw/leases/publish": "acquire"} {
		res, page := e.get(c, path)
		if res.StatusCode != http.StatusOK || !strings.Contains(page, want) || !strings.Contains(page, "leaw-ed") {
			t.Errorf("%s: %d %s", path, res.StatusCode, page)
		}
	}
	if res, _ := e.get(c, "/im/p/leaw/leases/none"); res.StatusCode != http.StatusNotFound {
		t.Errorf("無い名前: %d", res.StatusCode)
	}
}

// looptrack lease を 2 つの端末から動かす。負けた側は終了コード 1 で持ち主が出て、持ち主が返せばもう一方が取れる。
func TestLeaseCLI(t *testing.T) {
	e := newEnv(t)
	pr := e.project("leac")
	u := e.user("leac-ed", "editor-password-1", "member")
	if err := store.SetMember(context.Background(), e.db, pr.ID, u.ID, "editor"); err != nil {
		t.Fatal(err)
	}
	tok := e.apiAs(u).token
	machine := func(label, session string) func(args ...string) (int, string, string) {
		home := t.TempDir()
		vars := map[string]string{"LOOPTRACK_API_URL": e.srv.URL + "/im", "LOOPTRACK_PROJECT": "leac", "LOOPTRACK_TOKEN": tok, "LOOPTRACK_LANG": "ja",
			"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "APPDATA": filepath.Join(home, "AppData"),
			"CLAUDE_PROJECT_DIR": t.TempDir(), "LOOPTRACK_MACHINE_LABEL": label, "LOOPTRACK_SESSION_ID": session}
		return func(args ...string) (int, string, string) {
			var out, errb bytes.Buffer
			code := cli.LeaseMain(args, cli.IO{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb}, clienv.FromMap(vars))
			return code, out.String(), errb.String()
		}
	}
	a, b := machine("box-a", "cli-session-a"), machine("box-b", "cli-session-b")
	code, out, errs := a("acquire", "deploy.dev", "--purpose", "dev へ反映する", "--ttl", "30m")
	token := regexp.MustCompile(`(?m)^lease_token=([0-9a-f]{32})$`).FindStringSubmatch(out)
	if code != 0 || !strings.Contains(out, "deploy.dev") || token == nil || strings.Contains(out, "T") && strings.Contains(out, "Z）") {
		t.Fatalf("A の取得: %d %q %q", code, out, errs)
	}
	code, out, errs = b("acquire", "deploy.dev", "--purpose", "別の反映")
	if code != 1 || !strings.Contains(errs, "leac-ed") || !strings.Contains(errs, "box-a") || !strings.Contains(errs, "dev へ反映する") {
		t.Fatalf("B の取得: %d %q %q", code, out, errs)
	}
	if code, out, errs := b("list"); code != 0 || !strings.Contains(out, "deploy.dev") || !strings.Contains(out, "box-a") {
		t.Fatalf("一覧: %d %q %q", code, out, errs)
	}
	if code, out, errs := b("release", "deploy.dev"); code != 1 {
		t.Fatalf("他のセッションの返却: %d %q %q", code, out, errs)
	}
	if code, _, errs := b("extend", "deploy.dev", "--ttl", "1h", "--token", token[1]); code != 0 {
		t.Fatalf("鍵の延長: %d %q", code, errs)
	}
	if code, _, _ := b("extend", "deploy.dev", "--token", "stale"); code != 1 {
		t.Fatalf("古い鍵の延長: %d", code)
	}
	if code, _, errs := a("extend", "deploy.dev", "--ttl", "1h"); code != 0 {
		t.Fatalf("延長: %d %q", code, errs)
	}
	if code, _, errs := a("extend", "deploy.dev", "--ttl", "soon"); code == 0 {
		t.Fatalf("読めない --ttl: %d %q", code, errs)
	}
	if code, _, errs := a("release", "deploy.dev"); code != 0 {
		t.Fatalf("返却: %d %q", code, errs)
	}
	if code, _, errs := b("acquire", "deploy.dev", "--purpose", "別の反映"); code != 0 {
		t.Fatalf("返した後の取得: %d %q", code, errs)
	}
	if code, _, errs := a("break", "deploy.dev", "--reason", "端末が止まった"); code != 0 {
		t.Fatalf("外す: %d %q", code, errs)
	}
	code, out, errs = a("show", "deploy.dev")
	if code != 0 || !strings.Contains(out, "空いています") || !strings.Contains(out, "端末が止まった") {
		t.Fatalf("show: %d %q %q", code, out, errs)
	}
}
