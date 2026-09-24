package server

import (
	"bytes"
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/howashoji/looptrack/internal/store"
)

// 担当者（assignee）。設計は DESIGN.md §5-1。受け入れ条件ごとに確かめる。

type assigneeEnv struct {
	e                         *env
	pr                        store.Project
	alice, bob, carol, viewer *apiClient
	users                     map[string]store.User
}

func newAssigneeEnv(t *testing.T) *assigneeEnv {
	e := newEnv(t)
	e.clock.t = time.Now().UTC().Truncate(time.Minute).Add(30 * time.Second)
	ctx := context.Background()
	pr := e.project("req")
	a := &assigneeEnv{e: e, pr: pr, users: map[string]store.User{}}
	for _, u := range []struct{ login, role string }{{"alice", "editor"}, {"bob", "editor"}, {"carol", "editor"}, {"vic", "viewer"}} {
		us := e.user(u.login, u.login+"-password-1", "member")
		if err := store.SetMember(ctx, e.db, pr.ID, us.ID, u.role); err != nil {
			t.Fatal(err)
		}
		a.users[u.login] = us
	}
	a.alice, a.bob, a.carol, a.viewer = e.apiAs(a.users["alice"]), e.apiAs(a.users["bob"]), e.apiAs(a.users["carol"]), e.apiAs(a.users["vic"])
	return a
}

// assigneeOf は DB 上の担当の login（未設定は ""）。
func (a *assigneeEnv) assigneeOf(id string) string {
	var login string
	a.e.db.QueryRow("SELECT COALESCE(u.login, '') FROM issues i LEFT JOIN users u ON u.id = i.assignee_user_id WHERE i.display_id = ?", id).Scan(&login)
	return login
}

// versionOf は DB 上の version。
func (a *assigneeEnv) versionOf(id string) int {
	var v int
	a.e.db.QueryRow("SELECT version FROM issues WHERE display_id = ?", id).Scan(&v)
	return v
}

// events は issue_events の kind と detail（JSON）を古い順に返す（kind で絞る）。
func (a *assigneeEnv) events(id string, kinds ...string) []map[string]any {
	rows, err := a.e.db.Query(`SELECT e.kind, COALESCE(e.detail, '{}') FROM issue_events e JOIN issues i ON i.id = e.issue_id
WHERE i.display_id = ? ORDER BY e.id`, id)
	if err != nil {
		a.e.t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var kind, detail string
		rows.Scan(&kind, &detail)
		want := len(kinds) == 0
		for _, k := range kinds {
			want = want || k == kind
		}
		if !want {
			continue
		}
		m := map[string]any{}
		json.Unmarshal([]byte(detail), &m)
		m["kind"] = kind
		out = append(out, m)
	}
	return out
}

func TestAssigneeCreateAndShow(t *testing.T) {
	a := newAssigneeEnv(t)
	var created struct {
		Issue issueDetailJSON `json:"issue"`
	}
	// new --assignee me: show の frontmatter の status の次に assignee が出る
	a.alice.json(201, "POST", "/projects/req/issues", map[string]any{"title": "自分の担当", "assignee": "me"}, &created)
	if created.Issue.Assignee != "alice" || !strings.Contains(created.Issue.Markdown, "\nstatus: Todo\nassignee: alice\npriority: P2\n") {
		t.Fatalf("起票の担当: %+v\n%s", created.Issue.issueJSON, created.Issue.Markdown)
	}
	if _, _, md := a.alice.do("GET", "/issues/REQ-0001?format=md", nil); !strings.Contains(string(md), "\nassignee: alice\n") {
		t.Errorf("show に assignee が無い:\n%s", md)
	}
	// 参加者でない login・viewer は 400（担当にできる人を示す）
	a.e.user("stranger", "stranger-password-1", "member")
	for _, who := range []string{"stranger", "vic", "nobody"} {
		e := a.alice.fail(400, "POST", "/projects/req/issues", map[string]any{"title": "x", "assignee": who})
		if !strings.Contains(e.Error.Message, "担当にできるのはプロジェクト req の参加者（editor 以上）です: "+who) || !strings.Contains(e.Error.Message, "alice, bob, carol") {
			t.Errorf("%s: %s", who, e.Error.Message)
		}
	}
	// 担当の無いイシューは frontmatter に行を出さず、一覧の JSON にもキーを出さない（ファイルモードと同じ）
	a.alice.json(201, "POST", "/projects/req/issues", map[string]any{"title": "担当なし"}, &created)
	if strings.Contains(created.Issue.Markdown, "assignee") {
		t.Errorf("未設定なのに assignee 行がある:\n%s", created.Issue.Markdown)
	}
	_, _, raw := a.alice.do("GET", "/projects/req/issues?sort=id", nil)
	if n := strings.Count(string(raw), `"assignee"`); n != 1 {
		t.Errorf("一覧の assignee キー = %d 件（担当のある 1 件だけのはず）: %s", n, raw)
	}
	// In Progress で起票すると本人が担当になる（R1）
	a.bob.json(201, "POST", "/projects/req/issues", map[string]any{"title": "着手済みで起票", "status": "In Progress"}, &created)
	if created.Issue.Assignee != "bob" {
		t.Errorf("In Progress の起票: %+v", created.Issue.issueJSON)
	}
	if ev := a.events(created.Issue.ID, "assign"); len(ev) != 1 || ev[0]["to"] != "bob" || ev[0]["auto"] != true {
		t.Errorf("起票時の assign: %v", ev)
	}
}

func TestAssigneeNextAndTakeover(t *testing.T) {
	a := newAssigneeEnv(t)
	for _, c := range []map[string]any{
		{"title": "X", "priority": "P0"}, {"title": "Y", "priority": "P1"},
		{"title": "Z", "priority": "P1", "assignee": "alice"}, {"title": "W", "priority": "P2"},
	} {
		a.carol.json(201, "POST", "/projects/req/issues", c, nil)
	}
	var r nextResp
	// A（セッション a1）が next → X を In Progress にし、担当が A になる
	a.alice.header["X-Looptrack-Session"] = "a1"
	a.alice.json(200, "POST", "/projects/req/next", map[string]any{}, &r)
	if r.Action != "started" || r.Issue.ID != "REQ-0001" || r.Issue.Assignee != "alice" || a.assigneeOf("REQ-0001") != "alice" {
		t.Fatalf("A の next: %+v", r)
	}
	if !strings.Contains(r.Text, "\nstatus: In Progress\nassignee: alice\n") {
		t.Errorf("next の全文に担当が無い:\n%s", r.Text)
	}
	if ev := a.events("REQ-0001", "assign"); len(ev) != 1 || ev[0]["from"] != "" || ev[0]["to"] != "alice" || ev[0]["auto"] != true {
		t.Errorf("assign イベント: %v", ev)
	}
	// 同じ A の別のセッション（a2）は X を横取りせず、次の候補（Y）を取る
	a.alice.header["X-Looptrack-Session"] = "a2"
	a.alice.json(200, "POST", "/projects/req/next", map[string]any{}, &r)
	if r.Action != "started" || r.Issue.ID != "REQ-0002" {
		t.Errorf("A の別セッション: %+v", r)
	}
	// a1 に戻ると X を続ける（resumed）
	a.alice.header["X-Looptrack-Session"] = "a1"
	a.alice.json(200, "POST", "/projects/req/next", map[string]any{}, &r)
	if r.Action != "resumed" || r.Issue.ID != "REQ-0001" {
		t.Errorf("A の a1: %+v", r)
	}
	// B の next は A の担当（X・Z）を候補にしない → W
	a.bob.header["X-Looptrack-Session"] = "b1"
	a.bob.json(200, "POST", "/projects/req/next", map[string]any{"dry_run": true}, &r)
	if r.Action != "would_start" || r.Issue.ID != "REQ-0004" {
		t.Errorf("B の next: %+v", r)
	}
	// B が X を In Progress にするのは 422。担当者 A と引き継ぎ方を示す
	e := a.bob.fail(422, "POST", "/issues/REQ-0001/status", map[string]any{"status": "In Progress"})
	if e.Error.Code != "assigned_to_other" || e.Error.Rule != "assignee" || !e.Error.Overridable ||
		e.Error.Message != `REQ-0001 の担当は alice です（他の利用者が担当のイシューは、In Progress にすることができません）。引き継ぐなら --override "理由"（MCP は override_reason）を付けてください（担当が自分に替わります）。依頼や確認はコメント（comment / add_comment）で伝えてください` {
		t.Errorf("横取りの拒否: %+v", e.Error)
	}
	// --override "理由" なら通り、担当が B に替わり、assignee_takeover が残る
	var st struct {
		Issue    issueJSON `json:"issue"`
		Messages []string  `json:"messages"`
	}
	a.bob.json(200, "POST", "/issues/REQ-0001/status", map[string]any{"status": "In Progress", "override_reason": "A から引き継ぐ（利用者指示）"}, &st)
	if st.Issue.Assignee != "bob" || strings.Join(st.Messages, "|") != "REQ-0001: In Progress → In Progress|担当: REQ-0001: alice → bob" {
		t.Errorf("引き継ぎ: %+v", st)
	}
	ev := a.events("REQ-0001", "assign", "assignee_takeover")
	if len(ev) != 3 || ev[2]["kind"] != "assignee_takeover" || ev[2]["from"] != "alice" || ev[2]["to"] != "bob" ||
		ev[2]["reason"] != "A から引き継ぐ（利用者指示）" || ev[2]["op"] != "status" || ev[1]["reason"] != "A から引き継ぐ（利用者指示）" {
		t.Errorf("引き継ぎの記録: %v", ev)
	}
	// 引き継がれた後、A（a1）の next は X を返さない（担当が B）。Z（A の担当・Todo）に着手する
	a.alice.json(200, "POST", "/projects/req/next", map[string]any{}, &r)
	if r.Action != "started" || r.Issue.ID != "REQ-0003" {
		t.Errorf("引き継がれた後の A: %+v", r)
	}
	// コメントは担当に関係なく書ける（R4）。In Progress 以外への状態変更も従来どおり
	a.carol.json(201, "POST", "/issues/REQ-0001/comments", map[string]any{"text": "見ました"}, nil)
	a.carol.json(200, "POST", "/issues/REQ-0002/status", map[string]any{"status": "In Review"}, nil)
	if a.assigneeOf("REQ-0002") != "alice" {
		t.Errorf("In Review にしても担当は変わらない: %s", a.assigneeOf("REQ-0002"))
	}
	// next --assignee: 着手と同時に指定の人を担当にする
	a.carol.json(200, "POST", "/projects/req/next", map[string]any{"assignee": "bob"}, &r)
	if r.Action != "started" || r.Issue.ID != "REQ-0004" || a.assigneeOf("REQ-0004") != "bob" {
		t.Errorf("next --assignee: %+v", r)
	}
}

func TestAssigneeEditAssignAndActivity(t *testing.T) {
	a := newAssigneeEnv(t)
	for _, c := range []map[string]any{{"title": "A の作業", "assignee": "alice"}, {"title": "未設定"}, {"title": "閉じる"}} {
		a.carol.json(201, "POST", "/projects/req/issues", c, nil)
	}
	// 本文 / 項目の更新: 担当本人は可、他人は 422、override なら通すが担当は替えない
	var d issueDetailJSON
	a.alice.json(200, "GET", "/issues/REQ-0001", nil, &d)
	a.alice.json(200, "PATCH", "/issues/REQ-0001", map[string]any{"priority": "P1"}, &d, "If-Match", `"1"`)
	e := a.bob.fail(422, "PATCH", "/issues/REQ-0001", map[string]any{"markdown": strings.Replace(d.Markdown, "（未記入）", "B が書く", 1)}, "If-Match", `"2"`)
	// 文面は編集用（override でも担当は替わらない）
	if e.Error.Code != "assigned_to_other" || e.Error.Message != `REQ-0001 の担当は alice です（他の利用者が担当のイシューは、本文や項目を編集することができません）。担当者に代わって直すなら --override "理由"（MCP は override_reason）を付けてください（担当は替わりません）。依頼や確認はコメント（comment / add_comment）で伝えてください` {
		t.Errorf("他人の本文の編集: %+v", e.Error)
	}
	a.bob.json(200, "PATCH", "/issues/REQ-0001", map[string]any{"markdown": strings.Replace(d.Markdown, "（未記入）", "B が書く", 1),
		"override_reason": "A が休み"}, &d, "If-Match", `"2"`)
	if d.Assignee != "alice" || a.assigneeOf("REQ-0001") != "alice" || !strings.Contains(d.Body, "B が書く") {
		t.Errorf("override 付きの編集で担当が替わった: %+v", d.issueJSON)
	}
	if ev := a.events("REQ-0001", "assignee_override", "assignee_takeover", "assign"); len(ev) != 2 || ev[0]["kind"] != "assign" ||
		ev[1]["kind"] != "assignee_override" || ev[1]["assignee"] != "alice" || ev[1]["reason"] != "A が休み" || ev[1]["op"] != "update" {
		t.Errorf("override 付きの編集の記録（起票時の assign と assignee_override だけ）: %v", ev)
	}
	var by string
	if err := a.e.db.QueryRow(`SELECT u.login FROM issue_events e JOIN issues i ON i.id = e.issue_id JOIN users u ON u.id = e.actor_user_id
WHERE i.display_id = 'REQ-0001' AND e.kind = 'assignee_override'`).Scan(&by); err != nil || by != "bob" {
		t.Errorf("assignee_override の主体: %q %v", by, err)
	}
	// 項目の更新も同じ（override なしは 422、ありなら担当は替わらない）
	a.bob.fail(422, "PATCH", "/issues/REQ-0001", map[string]any{"priority": "P0"}, "If-Match", `"`+itoa(int64(d.Version))+`"`)
	a.bob.json(200, "PATCH", "/issues/REQ-0001", map[string]any{"priority": "P0", "override_reason": "優先度だけ直す"}, &d,
		"If-Match", `"`+itoa(int64(d.Version))+`"`)
	if d.Assignee != "alice" || len(a.events("REQ-0001", "assignee_override")) != 2 {
		t.Errorf("override 付きの項目の更新: %+v", d.issueJSON)
	}
	// 全文の assignee 行は保存しない。値を変えると 422（担当は assign で変える）。そのままなら通る
	e = a.alice.fail(422, "PATCH", "/issues/REQ-0001", map[string]any{"markdown": strings.Replace(d.Markdown, "assignee: alice", "assignee: carol", 1)},
		"If-Match", `"`+itoa(int64(d.Version))+`"`)
	if e.Error.Code != "immutable_field" || !strings.Contains(e.Error.Message, "assign") {
		t.Errorf("本文での担当の変更: %+v", e.Error)
	}
	a.alice.json(200, "PATCH", "/issues/REQ-0001", map[string]any{"markdown": strings.Replace(d.Markdown, "B が書く", "B が直す", 1)}, &d,
		"If-Match", `"`+itoa(int64(d.Version))+`"`)
	if d.Assignee != "alice" || strings.Count(d.Markdown, "assignee:") != 1 {
		t.Errorf("担当行つきの全文の更新: %s", d.Markdown)
	}
	// 引き継ぎは assign の override で行う（従来どおり担当が替わり assignee_takeover が残る）
	a.bob.json(200, "POST", "/issues/REQ-0001/assign", map[string]any{"assignee": "me", "override_reason": "A から引き継ぐ"}, nil)
	if ev := a.events("REQ-0001", "assignee_takeover"); a.assigneeOf("REQ-0001") != "bob" || len(ev) != 1 || ev[0]["op"] != "assign" {
		t.Errorf("assign での引き継ぎ: %s %v", a.assigneeOf("REQ-0001"), ev)
	}

	// assign: 未設定のイシューは editor なら誰でも設定できる
	var as struct {
		Issue   issueJSON `json:"issue"`
		From    string    `json:"from"`
		To      string    `json:"to"`
		Changed bool      `json:"changed"`
		Message string    `json:"message"`
	}
	// activity の last_at が担当の変更で進む（鮮度ガード）
	var act struct {
		Items []activityJSON `json:"items"`
	}
	a.carol.json(200, "GET", "/activity?ids=REQ-0002", nil, &act)
	before := act.Items[0].LastEpoch
	a.e.clock.Add(3 * time.Second)
	a.carol.json(200, "POST", "/issues/REQ-0002/assign", map[string]any{"assignee": "alice"}, &as)
	if !as.Changed || as.To != "alice" || as.Message != "担当: REQ-0002: - → alice" || a.assigneeOf("REQ-0002") != "alice" {
		t.Errorf("未設定への設定: %+v", as)
	}
	a.carol.json(200, "GET", "/activity?ids=REQ-0002", nil, &act)
	if act.Items[0].LastEpoch <= before || act.Items[0].LastKind != "assign" || act.Items[0].EventsSince < 1 {
		t.Errorf("activity が進まない: %+v（前 %v）", act.Items[0], before)
	}
	if ev := a.events("REQ-0002", "assign"); len(ev) != 1 || ev[0]["from"] != "" || ev[0]["to"] != "alice" {
		t.Errorf("assign の記録: %v", ev)
	}
	// 同じ値なら何もしない（イベントも残さない）
	a.alice.json(200, "POST", "/issues/REQ-0002/assign", map[string]any{"assignee": "me"}, &as)
	if as.Changed || as.Message != "担当は変わりません: REQ-0002（alice）" || len(a.events("REQ-0002", "assign")) != 1 {
		t.Errorf("同じ値: %+v", as)
	}
	// 他人の担当を替える・外すには override
	e = a.carol.fail(422, "POST", "/issues/REQ-0002/assign", map[string]any{"assignee": "-"})
	if e.Error.Code != "assigned_to_other" || e.Error.Message != `REQ-0002 の担当は alice です（他の利用者が担当のイシューは、担当を替える・外すことができません）。引き継ぐなら --override "理由"（MCP は override_reason）を付けてください。依頼や確認はコメント（comment / add_comment）で伝えてください` {
		t.Errorf("他人の担当を外す: %+v", e.Error)
	}
	a.carol.json(200, "POST", "/issues/REQ-0002/assign", map[string]any{"assignee": "-", "override_reason": "再割り当て"}, &as)
	if !as.Changed || as.To != "" || a.assigneeOf("REQ-0002") != "" || len(a.events("REQ-0002", "assignee_takeover")) != 1 {
		t.Errorf("override で外す: %+v", as)
	}
	// 本人の担当は自分で外せる。viewer は変えられない（403）。空の指定は 400。クローズ済みは 422
	a.viewer.fail(403, "POST", "/issues/REQ-0002/assign", map[string]any{"assignee": "me"})
	a.carol.fail(400, "POST", "/issues/REQ-0002/assign", map[string]any{"assignee": ""})
	a.carol.fail(400, "POST", "/issues/REQ-0002/assign", map[string]any{"assignee": "vic"})
	a.carol.json(200, "POST", "/issues/REQ-0003/status", map[string]any{"status": "Done", "assignee": "me"}, nil)
	if a.assigneeOf("REQ-0003") != "carol" {
		t.Errorf("close --assignee: %s", a.assigneeOf("REQ-0003"))
	}
	if e := a.carol.fail(422, "POST", "/issues/REQ-0003/assign", map[string]any{"assignee": "-"}); e.Error.Code != "closed" {
		t.Errorf("クローズ済みの担当: %+v", e.Error)
	}

	// 一覧の絞り込み: me・login・-（未設定）
	var l listJSON
	a.bob.json(200, "GET", "/projects/req/issues?assignee=me&all=1", nil, &l)
	if ids(l.Items) != "REQ-0001" {
		t.Errorf("assignee=me: %s", ids(l.Items))
	}
	a.bob.json(200, "GET", "/projects/req/issues?assignee=-", nil, &l)
	if ids(l.Items) != "REQ-0002" {
		t.Errorf("assignee=-: %s", ids(l.Items))
	}
	a.bob.json(200, "GET", "/projects/req/ready?assignee=carol", nil, &l)
	if ids(l.Items) != "" {
		t.Errorf("ready assignee=carol: %s", ids(l.Items))
	}

	// 権限を外された担当: 印が付き（自動では外さない）、重複作業の相手にならない
	store.RemoveMember(context.Background(), a.e.db, a.pr.ID, a.users["bob"].ID)
	a.alice.json(200, "GET", "/projects/req/issues?assignee=bob", nil, &l)
	if ids(l.Items) != "REQ-0001" || !l.Items[0].AssigneeInactive {
		t.Errorf("権限を外された担当の印: %+v", l.Items)
	}
	a.alice.json(200, "POST", "/issues/REQ-0001/status", map[string]any{"status": "In Progress"}, nil)
	if ev := a.events("REQ-0001", "assign"); a.assigneeOf("REQ-0001") != "alice" || ev[len(ev)-1]["from_inactive"] != true {
		t.Errorf("印付きの担当からの引き継ぎ: %s %v", a.assigneeOf("REQ-0001"), ev)
	}
}

// TestAssigneeSurfaces は CLI / MCP / 画面の担当の変更が REST と同じ結果・同じエラー文言になること（受け入れ条件 4）と、
// CLI の list --assignee・画面のデータ・課題管理表の担当の列（受け入れ条件 5）を確かめる。
func TestAssigneeSurfaces(t *testing.T) {
	a := newAssigneeEnv(t)
	a.carol.json(201, "POST", "/projects/req/issues", map[string]any{"title": "A の担当", "assignee": "alice"}, nil) // REQ-0001
	for i := 0; i < 4; i++ {
		a.carol.json(201, "POST", "/projects/req/issues", map[string]any{"title": "未設定"}, nil) // REQ-0002..5
	}
	want := a.bob.fail(422, "POST", "/issues/REQ-0001/assign", map[string]any{"assignee": "me"}).Error.Message

	// CLI（利用者の LOOPTRACK_* は持ち込まない）
	cli := func(tok string, args ...string) cliResult {
		t.Helper()
		return issueCLI(t, a.e, "req", tok, "LOOPTRACK_USAGE=0")(args...)
	}
	if r := cli(a.bob.token, "assign", "REQ-0001", "me"); r.code != 1 || r.stderr != "エラー: "+want+"\n" {
		t.Errorf("CLI assign の拒否: %d %q, want %q", r.code, r.stderr, want)
	}
	if r := cli(a.bob.token, "status", "REQ-0001", "In Progress"); r.code != 1 || !strings.Contains(r.stderr, "REQ-0001 の担当は alice です") {
		t.Errorf("CLI status の拒否: %d %q", r.code, r.stderr)
	}
	if r := cli(a.bob.token, "new", "CLI で起票", "--assignee", "me"); r.code != 0 || !strings.HasPrefix(r.stdout, "作成: REQ-0006") {
		t.Fatalf("CLI new --assignee: %d %s %s", r.code, r.stdout, r.stderr)
	}
	if r := cli(a.bob.token, "show", "REQ-0006"); !strings.Contains(r.stdout, "\nstatus: Todo\nassignee: bob\n") {
		t.Errorf("CLI show の担当:\n%s", r.stdout)
	}
	if r := cli(a.bob.token, "new", "x", "--assignee", "nobody"); r.code != 1 || !strings.Contains(r.stderr, "担当にできるのはプロジェクト req の参加者") {
		t.Errorf("CLI new --assignee nobody: %d %s", r.code, r.stderr)
	}
	if r := cli(a.bob.token, "assign", "REQ-0002", "me"); r.code != 0 || r.stdout != "担当: REQ-0002: - → bob\n" {
		t.Errorf("CLI assign: %d %q %s", r.code, r.stdout, r.stderr)
	}
	if r := cli(a.bob.token, "status", "REQ-0005", "Todo", "--assignee", "carol"); r.code != 0 || r.stdout != "REQ-0005: Todo → Todo\n担当: REQ-0005: - → carol\n" {
		t.Errorf("CLI status --assignee: %d %q %s", r.code, r.stdout, r.stderr)
	}
	r := cli(a.bob.token, "list", "--assignee", "me")
	lines := strings.Split(r.stdout, "\n")
	if r.code != 0 || !regexp.MustCompile(`^ID +TYPE +STATUS +PRI +BLOCKED +ASSIGNEE +TITLE$`).MatchString(lines[0]) ||
		!strings.Contains(r.stdout, "REQ-0002") || !strings.Contains(r.stdout, "REQ-0006") || strings.Contains(r.stdout, "REQ-0001") ||
		!strings.Contains(r.stdout, "\n2 件") {
		t.Errorf("CLI list --assignee me: %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	// 担当の無い一覧は従来の列のまま（ファイルモードと同じ表）
	if r := cli(a.bob.token, "list", "--assignee", "-"); !strings.HasPrefix(r.stdout, "ID        TYPE        STATUS      PRI BLOCKED  TITLE\n") {
		t.Errorf("担当の無い一覧の見出し:\n%s", r.stdout)
	}

	// MCP: assign_issue / update_issue(assignee) は同じ文言
	m := a.e.mcpAs(a.bob.token, map[string]string{"X-Looptrack-Project": "req"})
	if text, _ := m.call("assign_issue", map[string]any{"id": "REQ-0001", "assignee": "me"}, true); text != want {
		t.Errorf("MCP assign_issue: %q, want %q", text, want)
	}
	if text, _ := m.call("update_issue", map[string]any{"id": "REQ-0001", "assignee": "me"}, true); text != want {
		t.Errorf("MCP update_issue(assignee): %q, want %q", text, want)
	}
	if text, data := m.call("update_issue", map[string]any{"id": "REQ-0003", "assignee": "me"}, false); text != "担当: REQ-0003: - → bob" || data["to"] != "bob" {
		t.Errorf("MCP update_issue(assignee) の成功: %q %v", text, data)
	}
	if text, _ := m.call("assign_issue", map[string]any{"id": "REQ-0001", "assignee": "me", "override_reason": "MCP で引き継ぐ"}, false); text != "担当: REQ-0001: alice → bob" {
		t.Errorf("MCP assign_issue の引き継ぎ: %q", text)
	}
	if text, _ := m.call("list_issues", map[string]any{"assignee": "me"}, false); !strings.Contains(text, "ASSIGNEE") || strings.Count(text, " bob ") != 4 {
		t.Errorf("MCP list_issues assignee=me:\n%s", text)
	}
	if text, _ := m.call("set_status", map[string]any{"id": "REQ-0004", "status": "In Progress"}, false); strings.Split(text, "\n")[0] != "REQ-0004: Todo → In Progress" ||
		strings.Contains(text, "担当:") {
		t.Errorf("MCP set_status（R1 は出力行を増やさない）: %q", text)
	}
	mc := a.e.mcpAs(a.carol.token, map[string]string{"X-Looptrack-Project": "req"})
	wantStart, _ := mc.call("set_status", map[string]any{"id": "REQ-0004", "status": "In Progress"}, true)
	if !strings.Contains(wantStart, "REQ-0004 の担当は bob です") {
		t.Errorf("MCP set_status の拒否: %q", wantStart)
	}
	// 本文 / 項目の編集は編集用の文面（担当は替わらない）で、REST と同じ文字列
	wantEdit := a.carol.fail(422, "PATCH", "/issues/REQ-0004", map[string]any{"priority": "P1"}, "If-Match", `"`+itoa(int64(a.versionOf("REQ-0004")))+`"`).Error.Message
	if text, _ := mc.call("update_issue", map[string]any{"id": "REQ-0004", "version": a.versionOf("REQ-0004"), "priority": "P1"}, true); text != wantEdit ||
		!strings.Contains(text, "担当者に代わって直すなら") || !strings.Contains(text, "担当は替わりません") {
		t.Errorf("MCP update_issue の拒否: %q, want %q", text, wantEdit)
	}

	// 画面: 詳細ドロワーのフォーム（JS なしの POST・CSRF）
	c := a.e.client()
	a.e.enroll(c, "carol", "carol-password-1")
	_, page := a.e.get(c, "/im/p/req/")
	csrf := regexp.MustCompile(`data-csrf="([^"]+)"`).FindStringSubmatch(page)
	if csrf == nil {
		t.Fatalf("ボードに CSRF が無い: %s", page)
	}
	want = a.carol.fail(422, "POST", "/issues/REQ-0001/assign", map[string]any{"assignee": "me"}).Error.Message
	res, body := a.e.post(c, "/im/p/req/issues/REQ-0001/assign", url.Values{"csrf": {csrf[1]}, "assignee": {"me"}})
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(html.UnescapeString(body), want) || !strings.Contains(body, `name="override_reason"`) {
		t.Errorf("画面の拒否: %d\n%s\nwant %s", res.StatusCode, body, want)
	}
	res, _ = a.e.post(c, "/im/p/req/issues/REQ-0001/assign", url.Values{"csrf": {csrf[1]}, "assignee": {"me"}, "override_reason": {"画面から引き継ぐ"}})
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/im/p/req/#REQ-0001" || a.assigneeOf("REQ-0001") != "carol" {
		t.Errorf("画面の引き継ぎ: %d %s %s", res.StatusCode, res.Header.Get("Location"), a.assigneeOf("REQ-0001"))
	}
	if ev := a.events("REQ-0001", "assignee_takeover"); len(ev) != 2 || ev[1]["reason"] != "画面から引き継ぐ" || ev[1]["op"] != "assign" {
		t.Errorf("画面の引き継ぎの記録: %v", ev)
	}
	var via string
	a.e.db.QueryRow("SELECT e.via FROM issue_events e JOIN issues i ON i.id = e.issue_id WHERE i.display_id = 'REQ-0001' ORDER BY e.id DESC LIMIT 1").Scan(&via)
	if via != "web" {
		t.Errorf("画面の変更の経路 = %s", via)
	}
	if res, _ := a.e.post(c, "/im/p/req/issues/REQ-0001/assign", url.Values{"assignee": {"-"}}); res.StatusCode != http.StatusForbidden {
		t.Errorf("CSRF なし: %d", res.StatusCode)
	}
	vc := a.e.client()
	a.e.enroll(vc, "vic", "vic-password-1")
	_, vpage := a.e.get(vc, "/im/p/req/")
	vcsrf := regexp.MustCompile(`data-csrf="([^"]+)"`).FindStringSubmatch(vpage)
	if res, _ := a.e.post(vc, "/im/p/req/issues/REQ-0002/assign", url.Values{"csrf": {vcsrf[1]}, "assignee": {"me"}}); res.StatusCode != http.StatusForbidden {
		t.Errorf("viewer の画面からの変更: %d", res.StatusCode)
	}

	// ボードのデータ: 担当・自分・変更できるか・担当にできる人
	var board struct {
		Me      string            `json:"me"`
		CanEdit bool              `json:"can_edit"`
		Members []boardMemberJSON `json:"members"`
		Issues  []boardIssueJSON  `json:"issues"`
	}
	a.carol.json(200, "GET", "/projects/req/board", nil, &board)
	if board.Me != "carol" || !board.CanEdit || len(board.Members) != 3 || board.Issues[0].Assignee != "carol" {
		t.Errorf("ボード: %+v", board)
	}
	a.viewer.json(200, "GET", "/projects/req/board", nil, &board)
	if board.CanEdit {
		t.Errorf("viewer に変更フォームを出す")
	}

	// 課題管理表（xlsx）に担当の列（権限を外された担当は印）
	store.RemoveMember(context.Background(), a.e.db, a.pr.ID, a.users["bob"].ID)
	_, _, raw := a.carol.do("GET", "/projects/req/issues.xlsx?sort=id", nil)
	f, err := excelize.OpenReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, _ := f.GetRows("課題管理表")
	if rows[3][4] != "担当" || rows[4][0] != "REQ-0001" || rows[4][4] != "carol" || rows[5][4] != "bob（権限なし）" {
		t.Errorf("課題管理表の担当: %v / %v / %v", rows[3], rows[4], rows[5])
	}
	if r := cli(a.carol.token, "list", "--assignee", "bob"); !strings.Contains(r.stdout, "bob(!)") || !strings.Contains(r.stdout, "(!) は担当がこのプロジェクトで変更できなくなった") {
		t.Errorf("CLI の権限を外された担当の印:\n%s", r.stdout)
	}
}
