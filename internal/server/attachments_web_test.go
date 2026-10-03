package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

// 添付の画面（ボードのコメントのフォーム・ドロワーの一覧・管理者の画面 /admin/attachments）。
// フォームとドロワーの描画は render_test.mjs が確かめ、ここではそれらが呼ぶサーバの経路と管理者の画面を確かめる。

var webPNG = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01")

// sessionUpload は board.js の submitComment と同じ要求（本文はファイル・名前はパーセント符号化・画面の Cookie と X-CSRF-Token）を送る。
func (e *env) sessionUpload(c *http.Client, csrf, issue, slug, name, mediaType string, body []byte) (int, map[string]any) {
	e.t.Helper()
	res, out := e.do(c, "POST", "/im/api/v1/issues/"+issue+"/attachments?project="+slug, string(body),
		"Content-Type", mediaType, "X-Looptrack-Filename", url.PathEscape(name), "X-CSRF-Token", csrf,
		"Origin", e.srv.URL, "Sec-Fetch-Site", "same-origin")
	var v map[string]any
	json.Unmarshal([]byte(out), &v)
	return res.StatusCode, v
}

func (e *env) attachmentRows() int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRow("SELECT COUNT(*) FROM attachments").Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// 受け入れ条件 1: editor は画面のセッションで添付でき（選択・ドラッグ&ドロップ・貼り付けのどれも、この同じ要求になる）、
// 返った ID をコメントに付けられる。viewer の添付は拒まれ、何も残らない（同じテストの editor が対照）。CSRF が違えば拒む。
func TestAttachmentWebSessionUpload(t *testing.T) {
	dir := t.TempDir()
	e := newEnvWith(t, func(c *Config) { c.AllowNoAdmin = true; c.AttachDir = dir })
	ctx := context.Background()
	pr := e.project("web")
	ed := e.user("web-ed", "editor-password-1", "member")
	vw := e.user("web-vi", "viewer-password-1", "member")
	store.SetMember(ctx, e.db, pr.ID, ed.ID, "editor")
	store.SetMember(ctx, e.db, pr.ID, vw.ID, "viewer")
	e.apiAs(ed).json(201, "POST", "/projects/web/issues", map[string]any{"title": "添付先"}, nil)

	// viewer: 添付は 403 で、行も本体も残らない
	vc, _, vcsrf := e.boardSession("web-vi", "viewer-password-1", "web")
	if code, v := e.sessionUpload(vc, vcsrf, "WEB-0001", "web", "v.png", "image/png", webPNG); code != http.StatusForbidden || errMessage(v) == "" {
		t.Errorf("viewer の添付: %d %v, want 403 と文言", code, v)
	}
	if n := e.attachmentRows(); n != 0 {
		t.Errorf("viewer の添付で行が %d 件残った", n)
	}

	// editor: CSRF が違えば 403（行は残らない）
	c, _, csrf := e.boardSession("web-ed", "editor-password-1", "web")
	if code, _ := e.sessionUpload(c, "wrong", "WEB-0001", "web", "x.png", "image/png", webPNG); code != http.StatusForbidden {
		t.Errorf("CSRF 違いの添付: %d, want 403", code)
	}
	if n := e.attachmentRows(); n != 0 {
		t.Errorf("CSRF 違いの添付で行が %d 件残った", n)
	}

	// editor（対照）: 日本語の名前もパーセント符号化で届き、経路は web で記録される
	code, v := e.sessionUpload(c, csrf, "WEB-0001", "web", "画面 1.png", "image/png", webPNG)
	if code != http.StatusCreated {
		t.Fatalf("editor の添付: %d %v", code, v)
	}
	at, _ := v["attachment"].(map[string]any)
	if at["filename"] != "画面 1.png" || at["via"] != "web" || at["inline"] != true {
		t.Errorf("添付の応答: %v", at)
	}
	id := int64(at["id"].(float64))

	// 返った ID をコメントに付ける（render.js の formRequest("comment") が作る本文）
	if code, v := e.formPost(c, csrf, "/api/v1/issues/WEB-0001/comments", map[string]any{"text": "画面の記録", "attachments": []int64{id}}); code != http.StatusCreated {
		t.Fatalf("添付つきのコメント: %d %v", code, v)
	}
	var detail string
	if err := e.db.QueryRow("SELECT CAST(detail AS CHAR) FROM issue_events WHERE kind = 'comment' ORDER BY id DESC LIMIT 1").Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, `"attachments"`) || !strings.Contains(detail, strconv.FormatInt(id, 10)) {
		t.Errorf("コメントの記録に添付の ID が無い: %s", detail)
	}
}

// 受け入れ条件 3（サーバの側）: 一覧の inline は本体の GET の Content-Disposition と一致する。SVG・画像を名乗る HTML・消去済みは
// inline にならない（ドロワーはこの値が true のものだけを img にする）。対照: 中身も png のものは inline。
func TestAttachmentListInlineFollowsServe(t *testing.T) {
	a := newAttachAPIEnv(t)
	cases := []struct {
		name, mediaType string
		body            []byte
		inline          bool
	}{
		{"shot.png", "image/png", webPNG, true},
		{"evil.svg", "image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), false},
		{"fake.png", "image/png", []byte("<html><script>alert(1)</script></html>"), false},
		{"notes.txt", "text/plain", []byte("PASS\n"), false},
	}
	for _, c := range cases {
		a.mustUpload(a.issue, c.name, c.mediaType, c.body)
	}
	purgedAt := a.mustUpload(a.issue, "gone.png", "image/png", append(append([]byte{}, webPNG...), 'x'))
	if code, _, b := a.raw(a.admin, "POST", "/attachments/"+strconv.FormatInt(purgedAt.ID, 10)+"/purge", []byte(`{"reason":"誤り"}`), -1, "Content-Type", "application/json"); code != http.StatusOK {
		t.Fatalf("消去: %d %s", code, b)
	}

	_, _, b := a.raw(a.viewer, "GET", "/issues/"+a.issue+"/attachments", nil, 0)
	var list struct {
		Attachments []attachmentJSON `json:"attachments"`
	}
	if err := json.Unmarshal(b, &list); err != nil || len(list.Attachments) != len(cases)+1 {
		t.Fatalf("一覧: %v %s", err, b)
	}
	for i, got := range list.Attachments {
		if i == len(cases) {
			if !got.Purged || got.Inline {
				t.Errorf("消去済み: purged=%v inline=%v, want purged で inline でない", got.Purged, got.Inline)
			}
			continue
		}
		c := cases[i]
		if got.Inline != c.inline {
			t.Errorf("%s: inline = %v, want %v", c.name, got.Inline, c.inline)
		}
		code, h, _ := a.raw(a.viewer, "GET", "/attachments/"+strconv.FormatInt(got.ID, 10), nil, 0)
		served := strings.HasPrefix(h.Get("Content-Disposition"), "inline")
		if code != http.StatusOK || served != got.Inline {
			t.Errorf("%s: GET %d %q と一覧の inline=%v が食い違う", c.name, code, h.Get("Content-Disposition"), got.Inline)
		}
	}
}

var adminCSRFRe = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

// adminPage は利用者でログインして /admin/attachments を開き、状態・本文・CSRF を返す（403 のときは CSRF を返さない）。
func (e *env) adminPage(c *http.Client, query string) (int, string, string) {
	e.t.Helper()
	res, page := e.get(c, "/im/admin/attachments"+query)
	m := adminCSRFRe.FindStringSubmatch(page)
	if m == nil {
		return res.StatusCode, page, ""
	}
	return res.StatusCode, page, m[1]
}

// 受け入れ条件 2: 上限を変えると setting_changes に変更前後の値と操作した人が残り、次の添付から効く。管理者でない利用者は
// 画面にも POST（上限・消去）にも入れず、何も変わらない。規則は service の 1 か所にあり、画面の入口を通らない呼び出しも拒む。
// 対照: 同じテストの管理者は入れて、変えられる。
func TestAdminAttachmentsLimitsAndPurge(t *testing.T) {
	dir := t.TempDir()
	e := newEnvWith(t, func(c *Config) { c.AllowNoAdmin = true; c.AttachDir = dir })
	ctx := context.Background()
	pr := e.project("adm")
	root := e.user("adm-root", "root-password-1", "admin")
	ed := e.user("adm-ed", "editor-password-1", "member")
	store.SetMember(ctx, e.db, pr.ID, ed.ID, "editor")
	edAPI := e.apiAs(ed)
	edAPI.json(201, "POST", "/projects/adm/issues", map[string]any{"title": "添付先"}, nil)

	// 添付を 1 つ置く（消去の対象）
	ec, _, ecsrf := e.boardSession("adm-ed", "editor-password-1", "adm")
	code, v := e.sessionUpload(ec, ecsrf, "ADM-0001", "adm", "secret.png", "image/png", webPNG)
	if code != http.StatusCreated {
		t.Fatalf("添付: %d %v", code, v)
	}
	attID := int64(v["attachment"].(map[string]any)["id"].(float64))
	sha := v["attachment"].(map[string]any)["sha256"].(string)

	limits := func() store.AttachLimits {
		l, err := store.ReadAttachLimits(ctx, e.db)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	changes := func() int {
		var n int
		e.db.QueryRow("SELECT COUNT(*) FROM setting_changes WHERE name IN (?, ?)", store.SettingAttachMaxFile, store.SettingAttachMaxProject).Scan(&n)
		return n
	}
	purged := func() bool {
		var n int
		e.db.QueryRow("SELECT COUNT(*) FROM attachment_purges WHERE sha256 = ?", sha).Scan(&n)
		return n > 0
	}
	before := limits()

	// 管理者でない利用者: 画面は 403、POST（CSRF は正しい）も 403 で、上限・記録・消去のどれも変わらない
	if code, page, _ := e.adminPage(ec, ""); code != http.StatusForbidden || strings.Contains(page, `data-limit="file"`) {
		t.Errorf("editor の画面: %d, want 403 で中身を出さない", code)
	}
	res, _ := e.post(ec, "/im/admin/attachments/limits", url.Values{"csrf": {ecsrf}, "max_file_mib": {"1"}, "max_project_mib": {"2"}})
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("editor の上限の POST: %d, want 403", res.StatusCode)
	}
	res, _ = e.post(ec, "/im/admin/attachments/"+strconv.FormatInt(attID, 10)+"/purge", url.Values{"csrf": {ecsrf}, "reason": {"消したい"}})
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("editor の消去の POST: %d, want 403", res.StatusCode)
	}
	if limits() != before || changes() != 0 || purged() {
		t.Errorf("editor の POST で変わった: 上限 %+v（前 %+v）・記録 %d 件・消去 %v", limits(), before, changes(), purged())
	}
	// service の規則（画面の入口を通らない呼び出し）も管理者以外を拒む
	svc := service.New(e.db, nil)
	_, err := svc.SetAttachLimits(ctx, service.Actor{UserID: ed.ID, Via: "web"}, store.AttachLimits{MaxFile: 1, MaxProject: 2}, "")
	var se *service.Error
	if !errors.As(err, &se) || se.Kind != service.Forbidden {
		t.Errorf("service.SetAttachLimits（editor）: %v, want Forbidden", err)
	}
	if _, err := svc.AttachmentOverview(ctx, service.Actor{UserID: ed.ID, Via: "web"}, service.AttachmentAdminFilter{}); !errors.As(err, &se) || se.Kind != service.Forbidden {
		t.Errorf("service.AttachmentOverview（editor）: %v, want Forbidden", err)
	}

	// 管理者（対照）: 画面に上限と使用量が出る
	ac := e.client()
	e.enroll(ac, "adm-root", "root-password-1")
	code, page, csrf := e.adminPage(ac, "")
	if code != http.StatusOK || csrf == "" || !strings.Contains(page, `<strong data-limit="file">20 MiB</strong>`) ||
		!strings.Contains(page, `data-usage="adm"`) || !strings.Contains(page, `data-attachment="`+strconv.FormatInt(attID, 10)+`"`) {
		t.Fatalf("管理者の画面: %d\n%s", code, page)
	}
	// CSRF が違えば、管理者でも上限と消去の POST は 403 で何も変わらない（対照は下の正しい CSRF の POST）
	for _, path := range []string{"/im/admin/attachments/limits", "/im/admin/attachments/" + strconv.FormatInt(attID, 10) + "/purge"} {
		res, _ := e.post(ac, path, url.Values{"csrf": {"wrong"}, "max_file_mib": {"1"}, "max_project_mib": {"2"}, "reason": {"消したい"}})
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("CSRF 違いの %s: %d, want 403", path, res.StatusCode)
		}
	}
	if limits() != before || changes() != 0 || purged() {
		t.Errorf("CSRF 違いの POST で変わった: 上限 %+v（前 %+v）・記録 %d 件・消去 %v", limits(), before, changes(), purged())
	}
	// 入力が整数でなければ変えない
	res, body := e.post(ac, "/im/admin/attachments/limits", url.Values{"csrf": {csrf}, "max_file_mib": {"0"}, "max_project_mib": {"10"}})
	if res.StatusCode != http.StatusBadRequest || limits() != before || changes() != 0 {
		t.Errorf("0 MiB: %d・上限 %+v・記録 %d 件\n%s", res.StatusCode, limits(), changes(), body)
	}
	res, body = e.post(ac, "/im/admin/attachments/limits", url.Values{"csrf": {csrf}, "max_file_mib": {"1"}, "max_project_mib": {"300"}})
	if res.StatusCode != http.StatusOK || !strings.Contains(body, `class="notice"`) {
		t.Fatalf("上限の変更: %d\n%s", res.StatusCode, body)
	}
	if got := limits(); got.MaxFile != 1<<20 || got.MaxProject != 300<<20 {
		t.Errorf("変えた後の上限: %+v", got)
	}
	rows, err := e.db.Query(`SELECT name, old_value, new_value, COALESCE(actor_user_id, 0), via FROM setting_changes WHERE name IN (?, ?) ORDER BY id`,
		store.SettingAttachMaxFile, store.SettingAttachMaxProject)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var name, old, nv, via string
		var actor int64
		rows.Scan(&name, &old, &nv, &actor, &via)
		if actor != root.ID || via != "web" {
			t.Errorf("%s の記録: 操作した人 %d（want %d）・経路 %q", name, actor, root.ID, via)
		}
		got = append(got, name+":"+old+"->"+nv)
	}
	rows.Close()
	want := []string{store.SettingAttachMaxFile + ":->1048576", store.SettingAttachMaxProject + ":->314572800"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("setting_changes = %v, want %v", got, want)
	}
	if strings.Count(body, "data-setting-change") != 2 || !strings.Contains(body, "adm-root") {
		t.Errorf("画面に変更の記録（2 件・操作した人）が出ない:\n%s", body)
	}
	// 同じ値をもう一度送っても記録は増えない
	e.post(ac, "/im/admin/attachments/limits", url.Values{"csrf": {csrf}, "max_file_mib": {"1"}, "max_project_mib": {"300"}})
	if n := changes(); n != 2 {
		t.Errorf("同じ値の再送で記録が %d 件になった", n)
	}
	// 値がある状態から変えると、変更前の値も残る（変えていない 1 プロジェクトの上限は記録しない）
	if res, _ := e.post(ac, "/im/admin/attachments/limits", url.Values{"csrf": {csrf}, "max_file_mib": {"2"}, "max_project_mib": {"300"}}); res.StatusCode != http.StatusOK {
		t.Fatalf("2 回目の上限の変更: %d", res.StatusCode)
	}
	var name, old, nv string
	var actor int64
	e.db.QueryRow(`SELECT name, old_value, new_value, COALESCE(actor_user_id, 0) FROM setting_changes WHERE name IN (?, ?) ORDER BY id DESC LIMIT 1`,
		store.SettingAttachMaxFile, store.SettingAttachMaxProject).Scan(&name, &old, &nv, &actor)
	if name != store.SettingAttachMaxFile || old != "1048576" || nv != "2097152" || actor != root.ID || changes() != 3 {
		t.Errorf("2 回目の記録: %s %q → %q（操作した人 %d）・記録 %d 件, want 1 ファイルの上限 1048576 → 2097152・3 件", name, old, nv, actor, changes())
	}
	// 次の添付から効く（2 MiB を超える添付は拒む）
	if code, _ := e.sessionUpload(ec, ecsrf, "ADM-0001", "adm", "big.bin", "application/octet-stream", make([]byte, 2<<20+1)); code != http.StatusRequestEntityTooLarge {
		t.Errorf("上限を超える添付: %d, want 413", code)
	}

	// 消去: 理由が無ければ消さない。理由つきなら消去済みになり、本体が消える
	path := "/im/admin/attachments/" + strconv.FormatInt(attID, 10) + "/purge"
	if res, _ := e.post(ac, path, url.Values{"csrf": {csrf}, "reason": {"  "}}); res.StatusCode != http.StatusBadRequest || purged() {
		t.Errorf("理由なしの消去: %d・消去 %v", res.StatusCode, purged())
	}
	res, body = e.post(ac, path, url.Values{"csrf": {csrf}, "reason": {"秘密を誤って添付した"}, "project": {"adm"}})
	if res.StatusCode != http.StatusOK || !purged() || !strings.Contains(body, "data-purged") {
		t.Fatalf("消去: %d・消去 %v\n%s", res.StatusCode, purged(), body)
	}
	var reason, via string
	e.db.QueryRow("SELECT reason, via FROM attachment_purges WHERE sha256 = ?", sha).Scan(&reason, &via)
	if reason != "秘密を誤って添付した" || via != "web" {
		t.Errorf("消去の記録: 理由 %q・経路 %q", reason, via)
	}
	if _, err := os.Stat(service.AttachmentBodyPath(dir, sha)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("消去の後も本体が残っている: %v", err)
	}
	var events int
	e.db.QueryRow("SELECT COUNT(*) FROM issue_events WHERE kind = 'attach_purge'").Scan(&events)
	if events != 1 {
		t.Errorf("attach_purge の記録 = %d, want 1", events)
	}
}
