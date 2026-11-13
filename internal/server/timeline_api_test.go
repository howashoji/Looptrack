package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/client/cli"
	clienv "github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/store"
)

// タイムラインの REST・MCP・CLI（timeline_api.go・client/cli/timeline.go）。規則の本体は service（timeline.go）にあり、
// ここでは 3 つの経路が同じ規則で通り、同じ理由で拒むことと、summary の件数・着手の知らせが経路に出ることを確かめる。
// 文面を見る検査は日本語に固定する（REST と MCP は Accept-Language、CLI は LOOPTRACK_LANG）。

// timelinePostMember はプロジェクトに role で参加させた利用者の REST の口（セッションを名乗り、日本語で受け取る）。
func timelinePostMember(t *testing.T, e *env, pr store.Project, login, role, session string) *apiClient {
	t.Helper()
	u := e.user(login, login+"-password-1", "member")
	if role != "" {
		if err := store.SetMember(context.Background(), e.db, pr.ID, u.ID, role); err != nil {
			t.Fatal(err)
		}
	}
	c := e.apiAs(u)
	c.header["X-Looptrack-Session"] = session
	c.header["Accept-Language"] = "ja"
	return c
}

// errCode は応答の error.code を取る。
func errCode(b []byte) string {
	var eb memErrBody
	_ = json.Unmarshal(b, &eb)
	return eb.Error.Code
}

func TestTimelinePostREST(t *testing.T) {
	e := newEnv(t)
	pr := e.project("bul")
	ed := timelinePostMember(t, e, pr, "bul-ed", "editor", "s-ed")
	ed2 := timelinePostMember(t, e, pr, "bul-ed2", "editor", "s-ed2")
	vi := timelinePostMember(t, e, pr, "bul-vi", "viewer", "s-vi")
	stray := timelinePostMember(t, e, pr, "bul-stray", "", "s-stray")
	base := "/projects/bul/timeline"
	type counts struct {
		Timeline map[string]int `json:"timeline"`
	}
	summary := func(c *apiClient) map[string]int {
		var s counts
		c.json(http.StatusOK, "GET", "/projects/bul/summary", nil, &s)
		return s.Timeline
	}
	// 何も無いときは 0 件（対照）
	if got := summary(vi); got["active"] != 0 || got["unacked"] != 0 || got["holds"] != 0 {
		t.Fatalf("書き込みの無い summary: %v", got)
	}

	hold := map[string]any{"kind": "hold", "to": "project", "body": "main への push を待ってください", "machine_label": "box-a"}
	// 投稿は editor 以上。viewer は 403、参加していない利用者は 404、editor は通る（対照）
	if code, _, b := vi.do("POST", base, hold); code != http.StatusForbidden || errCode(b) != "forbidden" {
		t.Errorf("viewer の投稿: %d %s", code, b)
	}
	if code, _, b := stray.do("POST", base, hold); code != http.StatusNotFound {
		t.Errorf("参加していない利用者の投稿: %d %s", code, b)
	}
	if code, _, b := ed.do("POST", base, map[string]any{"kind": "stop", "to": "project", "body": "x"}); code != http.StatusBadRequest {
		t.Errorf("知らない種類: %d %s", code, b)
	}
	var one struct {
		TimelinePost timelinePostJSON `json:"post"`
	}
	ed.json(http.StatusCreated, "POST", base, hold, &one)
	if b := one.TimelinePost; b.Kind != "hold" || b.Version != 1 || b.ExpiresAt == "" || b.State != "active" || b.AppliesToMe || !b.SameUser || b.FromMachine != "box-a" {
		t.Fatalf("投稿: %+v", b)
	}
	id := one.TimelinePost.ID
	p := base + "/" + strconv.FormatInt(id, 10)

	// 改訂は書いた本人だけ。ほかの editor は 403 timeline_not_author、本人は版が上がる（対照）
	if code, _, b := ed2.do("POST", p+"/revise", map[string]any{"body": "横から"}); code != http.StatusForbidden || errCode(b) != "timeline_not_author" {
		t.Errorf("本人以外の改訂: %d %s", code, b)
	}
	ed.json(http.StatusOK, "POST", p+"/revise", map[string]any{"body": "main への push とマージを待ってください"}, &one)
	if one.TimelinePost.Version != 2 || !strings.Contains(one.TimelinePost.Body, "マージ") {
		t.Fatalf("改訂: %+v", one.TimelinePost)
	}

	// 一覧は読み手のセッションに効くものだけ。出したセッション自身には効かず、sent=1 で読める
	var list struct {
		TimelinePosts []timelinePostJSON `json:"posts"`
		Count         int                `json:"count"`
	}
	vi.json(http.StatusOK, "GET", base, nil, &list)
	if list.Count != 1 || !list.TimelinePosts[0].Unacked || !list.TimelinePosts[0].Undelivered || list.TimelinePosts[0].SameUser || !list.TimelinePosts[0].AppliesToMe {
		t.Fatalf("viewer の一覧: %+v", list)
	}
	ed.json(http.StatusOK, "GET", base, nil, &list)
	if list.Count != 0 {
		t.Fatalf("出したセッションの一覧: %+v", list)
	}
	ed.json(http.StatusOK, "GET", base+"?sent=1", nil, &list)
	if list.Count != 1 {
		t.Fatalf("出した一覧: %+v", list)
	}
	if got := summary(vi); got["active"] != 1 || got["unacked"] != 1 || got["holds"] != 1 {
		t.Fatalf("summary: %v", got)
	}
	if got := summary(ed); got["active"] != 0 {
		t.Fatalf("出したセッションの summary: %v", got)
	}

	// 了解は最新の版だけ。古い版は 409 timeline_version、最新の版は通り、受け取りも付く（対照）。info は了解できない
	if code, _, b := vi.do("POST", p+"/ack", map[string]any{"version": 1}); code != http.StatusConflict || errCode(b) != "timeline_version" {
		t.Errorf("古い版の了解: %d %s", code, b)
	}
	vi.json(http.StatusOK, "POST", p+"/ack", map[string]any{"version": 2}, &one)
	if b := one.TimelinePost; b.Acked != 2 || b.Received != 2 || b.Unacked || b.Undelivered {
		t.Fatalf("了解: %+v", b)
	}
	if code, _, b := ed.do("POST", p+"/ack", map[string]any{"version": 2}); code != http.StatusForbidden || errCode(b) != "timeline_not_recipient" {
		t.Errorf("出したセッションの了解: %d %s", code, b)
	}

	// 未達と未了解・受け取りの記録・軽い確認
	var pend struct {
		UndeliveredTotal int `json:"undelivered_total"`
		UnackedTotal     int `json:"unacked_total"`
	}
	ed2.json(http.StatusOK, "GET", base+"/pending", nil, &pend)
	if pend.UndeliveredTotal != 1 || pend.UnackedTotal != 1 {
		t.Fatalf("ed2 の未達と未了解: %+v", pend)
	}
	var rec struct {
		Recorded int64 `json:"recorded"`
	}
	ed2.json(http.StatusOK, "POST", base+"/receipts", map[string]any{"items": []map[string]any{{"id": id, "version": 2}}}, &rec)
	ed2.json(http.StatusOK, "GET", base+"/pending", nil, &pend)
	if rec.Recorded != 1 || pend.UndeliveredTotal != 0 || pend.UnackedTotal != 1 {
		t.Fatalf("受け取りの後: %+v %+v", rec, pend)
	}
	var chk struct {
		Seq   int64   `json:"seq"`
		Holds []int64 `json:"holds"`
	}
	ed2.json(http.StatusOK, "GET", base+"/check", nil, &chk)
	if chk.Seq != 2 || len(chk.Holds) != 1 || chk.Holds[0] != id {
		t.Fatalf("軽い確認: %+v", chk)
	}

	// 到達の状況: 了解した viewer と受け取った ed2 が並ぶ
	var st struct {
		Rows []timelinePostDeliveryJSON `json:"rows"`
	}
	ed.json(http.StatusOK, "GET", p+"/status", nil, &st)
	got := map[string][2]int64{}
	for _, r := range st.Rows {
		got[r.User] = [2]int64{r.Received, r.Acked}
	}
	if got["bul-vi"] != [2]int64{2, 2} || got["bul-ed2"] != [2]int64{2, 0} {
		t.Fatalf("到達の状況: %+v", st.Rows)
	}
	if code, _, b := stray.do("GET", p+"/status", nil); code != http.StatusNotFound {
		t.Errorf("参加していない利用者の到達の状況: %d %s", code, b)
	}
	var detail struct {
		Events []timelinePostEventJSON `json:"events"`
	}
	vi.json(http.StatusOK, "GET", p, nil, &detail)
	if len(detail.Events) != 2 || detail.Events[0].Action != "post" || detail.Events[1].Action != "revise" {
		t.Fatalf("出来事: %+v", detail.Events)
	}
	if code, _, b := vi.do("GET", base+"/x", nil); code != http.StatusBadRequest {
		t.Errorf("読めない ID: %d %s", code, b)
	}

	// 取り下げ: ほかの editor は理由が要り（400）、理由つきで通る（対照）。取り下げ済みは改訂できない
	if code, _, b := ed2.do("POST", p+"/withdraw", map[string]any{}); code != http.StatusBadRequest {
		t.Errorf("理由なしの取り下げ: %d %s", code, b)
	}
	if code, _, b := vi.do("POST", p+"/withdraw", map[string]any{"reason": "止めたい"}); code != http.StatusForbidden {
		t.Errorf("viewer の取り下げ: %d %s", code, b)
	}
	ed2.json(http.StatusOK, "POST", p+"/withdraw", map[string]any{"reason": "push は終わった"}, &one)
	if one.TimelinePost.State != "withdrawn" || one.TimelinePost.WithdrawReason != "push は終わった" || one.TimelinePost.WithdrawnBy != "bul-ed2" {
		t.Fatalf("取り下げ: %+v", one.TimelinePost)
	}
	if code, _, b := ed.do("POST", p+"/revise", map[string]any{"body": "やり直し"}); code != http.StatusConflict || errCode(b) != "timeline_withdrawn" {
		t.Errorf("取り下げ済みの改訂: %d %s", code, b)
	}
	if got := summary(vi); got["active"] != 0 || got["holds"] != 0 {
		t.Fatalf("取り下げの後の summary: %v", got)
	}
}

// 着手（next と In Progress への変更）の結果に、自分に効く有効な待機の知らせが付く。拒まない。
// 待機が無いとき・出したセッション自身・In Progress 以外への変更には付かない（対照）。
func TestTimelinePostHoldNoticeOnStart(t *testing.T) {
	e := newEnv(t)
	pr := e.project("buls")
	ed := timelinePostMember(t, e, pr, "buls-ed", "editor", "s-ed")
	ed2 := timelinePostMember(t, e, pr, "buls-ed2", "editor", "s-ed2")
	for i := 1; i <= 5; i++ {
		ed.json(http.StatusCreated, "POST", "/projects/buls/issues", map[string]any{"title": fmt.Sprintf("課題 %d", i), "status": "Todo"}, nil)
	}
	type statusRes struct {
		To       string   `json:"to"`
		Messages []string `json:"messages"`
		Holds    []int64  `json:"timeline_holds"`
	}
	setStatus := func(c *apiClient, id, status string) statusRes {
		var r statusRes
		c.json(http.StatusOK, "POST", "/issues/"+id+"/status", map[string]any{"status": status}, &r)
		return r
	}
	type nextRes struct {
		Text  string  `json:"text"`
		Holds []int64 `json:"timeline_holds"`
	}
	// 待機が無いときは知らせが無い（対照）
	if r := setStatus(ed2, "BULS-0001", "In Progress"); strings.Contains(strings.Join(r.Messages, "\n"), "タイムライン") || len(r.Holds) != 0 {
		t.Fatalf("待機の無い着手: %+v", r)
	}
	var n nextRes
	ed2.json(http.StatusOK, "POST", "/projects/buls/next", map[string]any{}, &n)
	if strings.Contains(n.Text, "タイムライン") || len(n.Holds) != 0 {
		t.Fatalf("待機の無い next: %+v", n)
	}

	var one struct {
		TimelinePost timelinePostJSON `json:"post"`
	}
	ed.json(http.StatusCreated, "POST", "/projects/buls/timeline", map[string]any{"kind": "hold", "to": "project", "body": "反映が終わるまで待ってください"}, &one)
	want := "#" + strconv.FormatInt(one.TimelinePost.ID, 10) + " buls-ed（ほかの利用者）"

	// 待機があっても着手は通り、知らせと ID が付く
	r := setStatus(ed2, "BULS-0002", "In Progress")
	if r.To != "In Progress" || !strings.Contains(strings.Join(r.Messages, "\n"), want) || len(r.Holds) != 1 || r.Holds[0] != one.TimelinePost.ID {
		t.Fatalf("待機のある着手: %+v", r)
	}
	ed2.json(http.StatusOK, "POST", "/projects/buls/next", map[string]any{}, &n)
	if !strings.Contains(n.Text, want) || len(n.Holds) != 1 {
		t.Fatalf("待機のある next: %+v", n)
	}
	// In Progress 以外への変更には付かない
	if r := setStatus(ed2, "BULS-0002", "In Review"); strings.Contains(strings.Join(r.Messages, "\n"), "タイムライン") || len(r.Holds) != 0 {
		t.Fatalf("In Review への変更: %+v", r)
	}
	// 出したセッション自身には効かない
	if r := setStatus(ed, "BULS-0003", "In Progress"); strings.Contains(strings.Join(r.Messages, "\n"), "タイムライン") {
		t.Fatalf("出したセッションの着手: %+v", r)
	}

	// MCP の set_status と next にも同じ知らせが出る
	m := e.mcpAs(ed2.token, map[string]string{"X-Looptrack-Project": "buls", "X-Looptrack-Session": "m-ed2"})
	if text, data := m.call("set_status", map[string]any{"id": "BULS-0004", "status": "In Progress"}, false); !strings.Contains(text, want) || data["timeline_holds"] == nil {
		t.Fatalf("MCP の set_status: %q %v", text, data)
	}
	if text, _ := m.call("next", map[string]any{}, false); !strings.Contains(text, want) {
		t.Fatalf("MCP の next: %q", text)
	}
	// 取り下げた後は出ない（対照）
	ed.json(http.StatusOK, "POST", "/projects/buls/timeline/"+strconv.FormatInt(one.TimelinePost.ID, 10)+"/withdraw", map[string]any{}, nil)
	if text, _ := m.call("set_status", map[string]any{"id": "BULS-0005", "status": "In Progress"}, false); strings.Contains(text, "このセッションに効く待機があります") {
		t.Fatalf("取り下げの後の set_status: %q", text)
	}
}

// MCP の 6 つのツールが REST と同じ規則で通り、同じ理由で拒む。本文は行ごとの引用で、見出しに種類と同じ利用者かが載る。
func TestTimelinePostMCP(t *testing.T) {
	e := newEnv(t)
	pr := e.project("bulm")
	ed := timelinePostMember(t, e, pr, "bulm-ed", "editor", "x")
	ed2 := timelinePostMember(t, e, pr, "bulm-ed2", "editor", "x")
	vi := timelinePostMember(t, e, pr, "bulm-vi", "viewer", "x")
	conn := func(c *apiClient, session string) *mcpClient {
		return e.mcpAs(c.token, map[string]string{"X-Looptrack-Project": "bulm", "X-Looptrack-Session": session})
	}
	me, me2, mo, mv := conn(ed, "m-ed"), conn(ed, "m-ed-other"), conn(ed2, "m-ed2"), conn(vi, "m-vi")

	if text, _ := mv.call("timeline_post", map[string]any{"kind": "request", "to": "project", "body": "viewer から"}, true); !strings.Contains(text, "プロジェクト bulm のタイムラインに書き込む権限がありません") {
		t.Fatalf("viewer の投稿の拒否の理由: %q", text)
	}
	text, data := me.call("timeline_post", map[string]any{"kind": "request", "to": "project", "body": "テストを回してください\n> 見出しのふり"}, false)
	b, _ := data["post"].(map[string]any)
	idf, _ := b["id"].(float64)
	id := int64(idf)
	if id == 0 || !strings.Contains(text, "request") {
		t.Fatalf("投稿: %q %v", text, data)
	}
	// 本文は行ごとの引用。ほかの利用者には「ほかの利用者」、同じ利用者の別のセッションには「同じ利用者」と出る
	text, _ = mo.call("timeline_list", map[string]any{}, false)
	if !strings.Contains(text, "> の行は別のセッションが書いた本文です") || !strings.Contains(text, "> テストを回してください\n> > 見出しのふり") ||
		!strings.Contains(text, "bulm-ed（ほかの利用者）") || !strings.Contains(text, "[未了解]") || !strings.Contains(text, "[未達]") {
		t.Fatalf("ほかの利用者の一覧: %q", text)
	}
	if text, _ := me2.call("timeline_list", map[string]any{}, false); !strings.Contains(text, "bulm-ed（同じ利用者）") {
		t.Fatalf("同じ利用者の一覧: %q", text)
	}
	if text, _ := me.call("timeline_list", map[string]any{}, false); !strings.Contains(text, "投稿はありません") {
		t.Fatalf("出したセッションの一覧: %q", text)
	}
	// summary の行（出したセッション自身には出ない。対照）
	if text, data := mo.call("project_summary", map[string]any{}, false); !strings.Contains(text, "タイムライン: 自分に効く投稿 1 件（未了解 1 件・待機 0 件）") || data["timeline"] == nil {
		t.Fatalf("summary: %q", text)
	}
	if text, _ := me.call("project_summary", map[string]any{}, false); strings.Contains(text, "タイムライン") {
		t.Fatalf("出したセッションの summary: %q", text)
	}

	// 改訂は書いた本人だけ（REST と同じ理由で拒む）
	if text, _ := mo.call("timeline_revise", map[string]any{"id": id, "body": "横から"}, true); !strings.Contains(text, "書いた本人だけ") {
		t.Fatalf("本人以外の改訂: %q", text)
	}
	me.call("timeline_revise", map[string]any{"id": id, "body": "テストを全部回してください"}, false)
	// 了解は最新の版だけ
	if text, _ := mo.call("timeline_ack", map[string]any{"id": id, "version": 1}, true); !strings.Contains(text, "最新の版は 2") {
		t.Fatalf("古い版の了解: %q", text)
	}
	mo.call("timeline_ack", map[string]any{"id": id, "version": 2}, false)
	if text, _ := mo.call("timeline_list", map[string]any{"mark_received": true}, false); strings.Contains(text, "[未了解]") || strings.Contains(text, "[未達]") {
		t.Fatalf("了解の後の一覧: %q", text)
	}
	if text, _ := me.call("timeline_status", map[string]any{"id": id}, false); !strings.Contains(text, "bulm-ed2  - ") || !strings.Contains(text, "`m-ed2`  2  2") {
		t.Fatalf("到達の状況: %q", text)
	}
	// 名乗ったセッションにバッククォートが入っていると、囲みを抜けないよう値を載せない（対照: 上の `m-ed2` は囲んで載る）
	conn(ed2, "m`x`y").call("timeline_ack", map[string]any{"id": id, "version": 2}, false)
	if text, _ := me.call("timeline_status", map[string]any{"id": id}, false); strings.Contains(text, "m`x`y") || !strings.Contains(text, "bulm-ed2  -  ?  2  2") {
		t.Fatalf("バッククォートを含むセッションの到達の状況: %q", text)
	}
	// 取り下げ: viewer は拒み、ほかの editor は理由が要り、書いた本人は理由なしで通る（対照）
	if text, _ := mv.call("timeline_withdraw", map[string]any{"id": id, "reason": "止めたい"}, true); !strings.Contains(text, "取り下げられるのは書いた本人か editor 以上です") {
		t.Fatalf("viewer の取り下げの拒否の理由: %q", text)
	}
	if text, _ := mo.call("timeline_withdraw", map[string]any{"id": id}, true); !strings.Contains(text, "理由") {
		t.Fatalf("理由なしの取り下げ: %q", text)
	}
	me.call("timeline_withdraw", map[string]any{"id": id}, false)
	// 前の版を受け取っていたセッションには、取り下げを 1 度知らせる
	if text, _ := mo.call("timeline_list", map[string]any{}, false); !strings.Contains(text, "bulm-ed が取り下げました") {
		t.Fatalf("取り下げの知らせ: %q", text)
	}
}

// looptrack timeline を 2 つの端末から動かす。REST・MCP と同じ理由で拒み、拒んだときは終了コード 1。
func TestTimelinePostCLI(t *testing.T) {
	e := newEnv(t)
	pr := e.project("bulc")
	ed := timelinePostMember(t, e, pr, "bulc-ed", "editor", "x")
	ed2 := timelinePostMember(t, e, pr, "bulc-ed2", "editor", "x")
	machine := func(tok, label, session string) (func(args ...string) (int, string, string), map[string]string) {
		home := t.TempDir()
		vars := map[string]string{"LOOPTRACK_API_URL": e.srv.URL + "/im", "LOOPTRACK_PROJECT": "bulc", "LOOPTRACK_TOKEN": tok, "LOOPTRACK_LANG": "ja",
			"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "APPDATA": filepath.Join(home, "AppData"),
			"CLAUDE_PROJECT_DIR": t.TempDir(), "LOOPTRACK_MACHINE_LABEL": label, "LOOPTRACK_SESSION_ID": session}
		return func(args ...string) (int, string, string) {
			var out, errb bytes.Buffer
			code := cli.TimelineMain(args, cli.IO{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb}, clienv.FromMap(vars))
			return code, out.String(), errb.String()
		}, vars
	}
	a, _ := machine(ed.token, "box-a", "cli-a")
	b, bvars := machine(ed2.token, "box-b", "cli-b")
	vi := timelinePostMember(t, e, pr, "bulc-vi", "viewer", "x")
	v, _ := machine(vi.token, "box-v", "cli-v")

	// viewer の投稿は拒まれ、終了コード 1（対照: 下の editor の投稿は通る）
	if code, out, errs := v("post", "info", "viewer から", "--all"); code != 1 || !strings.Contains(errs, "プロジェクト bulc のタイムラインに書き込む権限がありません") {
		t.Fatalf("viewer の投稿: %d %q %q", code, out, errs)
	}

	code, out, errs := a("post", "hold", "反映が終わるまで待ってください", "--all", "--ttl", "30m")
	m := regexp.MustCompile(`#(\d+)`).FindStringSubmatch(out)
	if code != 0 || m == nil || !strings.Contains(out, "プロジェクト全員") {
		t.Fatalf("投稿: %d %q %q", code, out, errs)
	}
	id := m[1]
	if code, out, errs := b("list"); code != 0 || !strings.Contains(out, "> 反映が終わるまで待ってください") || !strings.Contains(out, "bulc-ed（ほかの利用者）") ||
		!strings.Contains(out, "`box-a`") || !strings.Contains(out, "[未了解]") {
		t.Fatalf("一覧: %d %q %q", code, out, errs)
	}
	// 改訂は書いた本人だけ。本文を省いて期限だけを延ばせる（標準入力を待たない）
	if code, _, errs := b("revise", id, "横から"); code != 1 || !strings.Contains(errs, "書いた本人だけ") {
		t.Fatalf("本人以外の改訂: %d %q", code, errs)
	}
	if code, out, errs := a("revise", id, "--ttl", "1h"); code != 0 || !strings.Contains(out, "版 2") {
		t.Fatalf("期限の改訂: %d %q %q", code, out, errs)
	}
	// 了解は最新の版だけ。--version を省くと最新の版を出してから了解する
	if code, _, errs := b("ack", id, "--version", "1"); code != 1 || !strings.Contains(errs, "最新の版は 2") {
		t.Fatalf("古い版の了解: %d %q", code, errs)
	}
	if code, out, errs := b("ack", id); code != 0 || !strings.Contains(out, "> 反映が終わるまで待ってください") || !strings.Contains(out, "版 2") {
		t.Fatalf("了解: %d %q %q", code, out, errs)
	}
	if code, out, _ := b("check"); code != 0 || !regexp.MustCompile(`^seq=\d+ holds=`+id+` read=- behind=1\n$`).MatchString(out) {
		t.Fatalf("軽い確認: %d %q", code, out)
	}
	if code, out, errs := a("status", id); code != 0 || !strings.Contains(out, "bulc-ed2  -  `cli-b`  2  2") {
		t.Fatalf("到達の状況: %d %q %q", code, out, errs)
	}
	// summary に 1 行（了解は済んでいるので未了解は 0）
	var sum, sumErr bytes.Buffer
	if code := cli.Main([]string{"summary"}, cli.IO{Stdin: strings.NewReader(""), Stdout: &sum, Stderr: &sumErr}, clienv.FromMap(bvars)); code != 0 ||
		!strings.Contains(sum.String(), "タイムライン: 自分に効く投稿 1 件（未了解 0 件・待機 1 件）") {
		t.Fatalf("summary: %d %q %q", code, sum.String(), sumErr.String())
	}
	// 取り下げ: ほかの人は理由が要り、理由つきで通る（対照）
	if code, _, errs := b("withdraw", id); code != 1 || !strings.Contains(errs, "理由") {
		t.Fatalf("理由なしの取り下げ: %d %q", code, errs)
	}
	if code, _, errs := b("withdraw", id, "--reason", "反映は終わった"); code != 0 {
		t.Fatalf("取り下げ: %d %q", code, errs)
	}
	if code, out, errs := a("show", id); code != 0 || !strings.Contains(out, "[withdrawn]") || !strings.Contains(out, "withdraw") {
		t.Fatalf("show: %d %q %q", code, out, errs)
	} else if !regexp.MustCompile(`(?m)  withdraw  bulc-ed2  `+"`box-b`"+`\n> 反映は終わった$`).MatchString(out) || strings.Contains(out, "`box-b`  反映は終わった") {
		// 出来事の行の取り下げの理由も、本文と同じく次の行に引用で出る（地の文に混ぜない）
		t.Fatalf("show の出来事の行の理由: %q", out)
	}
	if code, out, _ := a("list", "--sent"); code != 0 || !strings.Contains(out, "投稿はありません") {
		t.Fatalf("取り下げの後の一覧: %d %q", code, out)
	}
	sum.Reset()
	if code := cli.Main([]string{"summary"}, cli.IO{Stdin: strings.NewReader(""), Stdout: &sum, Stderr: &sumErr}, clienv.FromMap(bvars)); code != 0 ||
		strings.Contains(sum.String(), "タイムライン") {
		t.Fatalf("取り下げの後の summary: %d %q", code, sum.String())
	}
}

// 読む時点で解決する宛先が REST と MCP で通る。イシュー宛ての待機は、置いた後でそのイシューに着手したセッションの一覧に出て、
// 到達の状況に出どころ（record）付きで並ぶ。別のイシューに着手したセッション（対照）には出ない。知らせはイシュー宛てにできない。
func TestTimelinePostResolvedTargetsREST(t *testing.T) {
	e := newEnv(t)
	pr := e.project("bult")
	ed := timelinePostMember(t, e, pr, "bult-ed", "editor", "s-ed")
	ed2 := timelinePostMember(t, e, pr, "bult-ed2", "editor", "s-ed2")
	ed3 := timelinePostMember(t, e, pr, "bult-ed3", "editor", "s-ed3")
	for i := 1; i <= 2; i++ {
		ed.json(http.StatusCreated, "POST", "/projects/bult/issues", map[string]any{"title": fmt.Sprintf("課題 %d", i), "status": "Todo"}, nil)
	}
	var one struct {
		TimelinePost timelinePostJSON `json:"post"`
	}
	ed.json(http.StatusCreated, "POST", "/projects/bult/timeline", map[string]any{"kind": "hold", "to": "issue", "to_issue": "BULT-0001", "body": "待って"}, &one)
	if one.TimelinePost.To != "issue" || one.TimelinePost.ToIssue != "BULT-0001" {
		t.Fatalf("宛先の応答: %+v", one.TimelinePost)
	}
	ed2.json(http.StatusOK, "POST", "/issues/BULT-0001/status", map[string]any{"status": "In Progress"}, nil)
	ed3.json(http.StatusOK, "POST", "/issues/BULT-0002/status", map[string]any{"status": "In Progress"}, nil)
	type list struct {
		Posts []timelinePostJSON `json:"posts"`
	}
	var l2, l3 list
	ed2.json(http.StatusOK, "GET", "/projects/bult/timeline", nil, &l2)
	ed3.json(http.StatusOK, "GET", "/projects/bult/timeline", nil, &l3)
	if len(l2.Posts) != 1 || l2.Posts[0].ID != one.TimelinePost.ID || !l2.Posts[0].AppliesToMe {
		t.Errorf("着手したセッションの一覧: %+v", l2.Posts)
	}
	if len(l3.Posts) != 0 {
		t.Errorf("別のイシューのセッションの一覧: %+v", l3.Posts)
	}
	var st struct {
		Rows []timelinePostDeliveryJSON `json:"rows"`
	}
	ed.json(http.StatusOK, "GET", "/projects/bult/timeline/"+strconv.FormatInt(one.TimelinePost.ID, 10)+"/status", nil, &st)
	if len(st.Rows) != 1 || st.Rows[0].User != "bult-ed2" || st.Rows[0].Session != "s-ed2" || st.Rows[0].ResolvedBy != "record" {
		t.Errorf("到達の状況: %+v", st.Rows)
	}
	if code, _, b := ed.do("POST", "/projects/bult/timeline", map[string]any{"kind": "info", "to": "issue", "to_issue": "BULT-0001", "body": "x"}); code != http.StatusBadRequest || errCode(b) != "invalid_argument" {
		t.Errorf("知らせのイシュー宛て: %d %s", code, b)
	}
	m := e.mcpAs(ed.token, map[string]string{"X-Looptrack-Project": "bult", "X-Looptrack-Session": "m-ed"})
	text, data := m.call("timeline_post", map[string]any{"kind": "request", "to": "branch", "to_branch": "feat-x", "body": "feat-x で検査を"}, false)
	p, _ := data["post"].(map[string]any)
	if p["to_branch"] != "feat-x" || !strings.Contains(text, "ブランチ `feat-x` のセッション") {
		t.Errorf("MCP のブランチ宛て: %q %v", text, data)
	}
}
