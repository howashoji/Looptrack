package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/store"
)

// 参加を外す・viewer に下げるとき、その人が担当の未クローズのイシューがあれば代わりの担当者を
// 指定しないと変えられない。指定すると担当を付け替え、issue_events に assign（reason）を残す。

func TestMemberRemovalNeedsReplacement(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("aaa")
	root := e.adminIn("root", "root-password-12", "aaa")
	dave := e.user("dave", "dave-password-12", "member")
	erin := e.user("erin", "erin-password-12", "member")
	vic := e.user("vic", "vic-password-123", "member")
	store.SetMember(ctx, e.db, pr.ID, dave.ID, "editor")
	store.SetMember(ctx, e.db, pr.ID, erin.ID, "editor")
	store.SetMember(ctx, e.db, pr.ID, vic.ID, "viewer")
	d := e.apiAs(dave)
	for _, title := range []string{"dave の作業中", "dave が閉じた", "dave の 2 件目"} {
		d.json(201, "POST", "/projects/aaa/issues", map[string]any{"title": title}, nil)
	}
	d.json(200, "POST", "/issues/AAA-0001/status", map[string]any{"status": "In Progress"}, nil)
	d.json(200, "POST", "/issues/AAA-0002/status", map[string]any{"status": "In Progress"}, nil)
	d.json(200, "POST", "/issues/AAA-0002/status", map[string]any{"status": "Done"}, nil)
	d.json(200, "POST", "/issues/AAA-0003/status", map[string]any{"status": "In Review", "assignee": "me"}, nil)

	assignee := func(id string) string {
		t.Helper()
		row, err := store.FindIssue(ctx, e.db, id, pr.ID, false)
		if err != nil {
			t.Fatal(err)
		}
		a, err := store.IssueAssignee(ctx, e.db, row.ID)
		if err != nil {
			t.Fatal(err)
		}
		return a.Login
	}
	version := func(id string) int {
		row, _ := store.FindIssue(ctx, e.db, id, pr.ID, false)
		return row.Version
	}
	lastEvent := func(id string) (kind, via string, actor int64, detail map[string]any) {
		t.Helper()
		var raw string
		if err := e.db.QueryRow(`SELECT ev.kind, ev.via, COALESCE(ev.actor_user_id, 0), COALESCE(ev.detail, '{}') FROM issue_events ev
JOIN issues i ON i.id = ev.issue_id WHERE i.display_id = ? ORDER BY ev.id DESC LIMIT 1`, id).Scan(&kind, &via, &actor, &raw); err != nil {
			t.Fatal(err)
		}
		json.Unmarshal([]byte(raw), &detail)
		return
	}
	role := func(u store.User) string {
		r, _ := store.MemberRole(ctx, e.db, pr.ID, u.ID)
		return r
	}
	if a1, a2, a3 := assignee("AAA-0001"), assignee("AAA-0002"), assignee("AAA-0003"); a1 != "dave" || a2 != "dave" || a3 != "dave" {
		t.Fatalf("準備: %s %s %s", a1, a2, a3)
	}

	c := e.client()
	e.enroll(c, "root", "root-password-12")
	post := func(form url.Values, want int) string {
		t.Helper()
		res, body := e.formAt(c, "/im/admin/projects", "/im/admin/projects/aaa/member", form)
		if res.StatusCode != want {
			t.Errorf("%v: %d, want %d", form, res.StatusCode, want)
		}
		return body
	}

	// 代わりの担当者なしで外すと 409。件数（未クローズの 2 件。閉じた AAA-0002 は数えない）と選択欄が出て、何も変わらない
	v1 := version("AAA-0001")
	body := post(url.Values{"login": {"dave"}, "role": {""}}, http.StatusConflict)
	sec := body[strings.Index(body, `id="replacement"`):]
	sec = sec[:strings.Index(sec, "</section>")]
	for _, want := range []string{"<strong>2 件</strong>", "AAA-0001, AAA-0003", `<select name="replacement" required>`,
		`<option value="erin">`, `<option value="root">`, `<option value="-">`, `name="login" value="dave"`, `name="role" value=""`,
		`action="/im/admin/projects/aaa/member"`, `name="csrf"`} {
		if !strings.Contains(sec, want) {
			t.Errorf("選択欄に %q が無い: %s", want, sec)
		}
	}
	for _, not := range []string{`<option value="dave">`, `<option value="vic">`} { // 本人と viewer は候補にしない
		if strings.Contains(sec, not) {
			t.Errorf("選択欄に %q がある", not)
		}
	}
	if !strings.Contains(body, `class="error"`) || strings.Contains(body, "<script") {
		t.Errorf("エラー表示: %s", body)
	}
	if role(dave) != "editor" || assignee("AAA-0001") != "dave" || version("AAA-0001") != v1 {
		t.Errorf("拒否したのに変わった: role=%s assignee=%s", role(dave), assignee("AAA-0001"))
	}
	// 担当にできない人（viewer・本人・存在しない）は 400 で何も変えない
	for _, r := range []string{"vic", "dave", "nobody"} {
		post(url.Values{"login": {"dave"}, "role": {""}, "replacement": {r}}, http.StatusBadRequest)
	}
	if role(dave) != "editor" || assignee("AAA-0001") != "dave" {
		t.Errorf("不正な代わりの担当者で変わった")
	}
	// CSRF なしは 403 で何も変えない
	if res, _ := e.post(c, "/im/admin/projects/aaa/member", url.Values{"login": {"dave"}, "role": {""}, "replacement": {"erin"}}); res.StatusCode != http.StatusForbidden {
		t.Errorf("CSRF なし: %d", res.StatusCode)
	}

	// 指定すると外れ、未クローズの担当が付け替わる（閉じたものは残す）。assign に理由が残り、版が進む
	body = post(url.Values{"login": {"dave"}, "role": {""}, "replacement": {"erin"}}, http.StatusOK)
	if !strings.Contains(body, "担当していた 2 件（AAA-0001, AAA-0003）の担当を erin にしました") {
		t.Errorf("完了のメッセージ: %s", body)
	}
	if role(dave) != "" || assignee("AAA-0001") != "erin" || assignee("AAA-0003") != "erin" || assignee("AAA-0002") != "dave" {
		t.Errorf("付け替え: role=%q %s %s %s", role(dave), assignee("AAA-0001"), assignee("AAA-0003"), assignee("AAA-0002"))
	}
	if version("AAA-0001") != v1+1 {
		t.Errorf("版が進まない: %d → %d", v1, version("AAA-0001"))
	}
	kind, via, actor, detail := lastEvent("AAA-0001")
	if kind != "assign" || via != "web" || actor != root.ID || detail["from"] != "dave" || detail["to"] != "erin" ||
		detail["reason"] != "参加の解除" || detail["from_inactive"] != true || detail["op"] != "member" {
		t.Errorf("assign の記録: %s %s %d %v", kind, via, actor, detail)
	}
	if kind, _, _, _ := lastEvent("AAA-0002"); kind == "assign" {
		t.Error("閉じたイシューの担当を替えた")
	}

	// 利用者の画面から viewer に下げる場合も同じ（erin は 2 件の担当）。- で未設定にできる
	upost := func(form url.Values, want int) string {
		t.Helper()
		res, body := e.formAt(c, "/im/admin/users/erin", "/im/admin/users/erin/member", form)
		if res.StatusCode != want {
			t.Errorf("%v: %d, want %d", form, res.StatusCode, want)
		}
		return body
	}
	body = upost(url.Values{"project": {"aaa"}, "role": {"viewer"}}, http.StatusConflict)
	sec = body[strings.Index(body, `id="replacement"`):]
	sec = sec[:strings.Index(sec, "</section>")]
	for _, want := range []string{"<strong>2 件</strong>", "役割を viewer にする", `name="project" value="aaa"`, `name="role" value="viewer"`,
		`action="/im/admin/users/erin/member"`, `<option value="root">`, `<option value="-">`} {
		if !strings.Contains(sec, want) {
			t.Errorf("利用者の画面の選択欄に %q が無い: %s", want, sec)
		}
	}
	if role(erin) != "editor" {
		t.Errorf("拒否したのに役割が変わった: %s", role(erin))
	}
	// editor → admin は担当にできるままなので代わりの担当者は要らない
	upost(url.Values{"project": {"aaa"}, "role": {"admin"}}, http.StatusOK)
	body = upost(url.Values{"project": {"aaa"}, "role": {"viewer"}, "replacement": {"-"}}, http.StatusOK)
	if !strings.Contains(body, "担当を 未設定 にしました") {
		t.Errorf("未設定への付け替えのメッセージ: %s", body)
	}
	if role(erin) != "viewer" || assignee("AAA-0001") != "" || assignee("AAA-0003") != "" {
		t.Errorf("viewer への変更: role=%s %q %q", role(erin), assignee("AAA-0001"), assignee("AAA-0003"))
	}
	if kind, _, _, detail := lastEvent("AAA-0003"); kind != "assign" || detail["from"] != "erin" || detail["to"] != "" || detail["reason"] != "役割の変更（viewer）" {
		t.Errorf("viewer への変更の記録: %s %v", kind, detail)
	}
	// 担当が無ければ代わりの担当者なしで外せる
	upost(url.Values{"project": {"aaa"}, "role": {""}}, http.StatusOK)
	if role(erin) != "" {
		t.Errorf("担当なしの解除: %s", role(erin))
	}
}
