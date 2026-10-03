package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/store"
)

// 添付の REST（attachments_api.go）。権限・応答の形式と安全のヘッダ・本文の上限。

type attachAPIEnv struct {
	*env
	dir                         string
	pr, other                   store.Project
	ed, viewer, outsider, admin *apiClient
	issue, otherIssue, sibling  string
	noAuth                      *http.Client
}

func newAttachAPIEnv(t *testing.T) *attachAPIEnv {
	t.Helper()
	dir := t.TempDir()
	e := newEnvWith(t, func(c *Config) { c.AllowNoAdmin = true; c.AttachDir = dir })
	ctx := context.Background()
	a := &attachAPIEnv{env: e, dir: dir, pr: e.project("att"), other: e.project("far")}
	member := func(login, role string, p store.Project) *apiClient {
		u := e.user(login, login+"-password-1", "member")
		if err := store.SetMember(ctx, e.db, p.ID, u.ID, role); err != nil {
			t.Fatal(err)
		}
		return e.apiAs(u)
	}
	a.ed, a.viewer, a.outsider = member("att-ed", "editor", a.pr), member("att-vi", "viewer", a.pr), member("far-ed", "editor", a.other)
	a.admin = e.apiAs(e.user("att-root", "root-password-1", "admin"))
	create := func(c *apiClient, slug, title string) string {
		var created struct {
			Issue issueDetailJSON `json:"issue"`
		}
		c.json(201, "POST", "/projects/"+slug+"/issues", map[string]any{"title": title}, &created)
		return created.Issue.ID
	}
	a.issue, a.sibling = create(a.ed, a.pr.Slug, "添付先"), create(a.ed, a.pr.Slug, "隣")
	a.otherIssue = create(a.outsider, a.other.Slug, "別のプロジェクト")
	a.noAuth = &http.Client{}
	return a
}

// raw は本文をそのまま送る要求（ヘッダは名前と値の組）。length が負なら Content-Length を付けない（chunked）。
func (a *attachAPIEnv) raw(c *apiClient, method, path string, body []byte, length int64, header ...string) (int, http.Header, []byte) {
	a.t.Helper()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
		if length < 0 {
			rd = io.MultiReader(rd) // 長さを隠す
		}
	}
	req, err := http.NewRequest(method, a.srv.URL+"/im/api/v1"+path, rd)
	if err != nil {
		a.t.Fatal(err)
	}
	if length < 0 {
		req.ContentLength = -1
	}
	if c != nil {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept-Language", "ja")
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	hc := a.noAuth
	if c != nil {
		hc = c.c
	}
	res, err := hc.Do(req)
	if err != nil {
		a.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, b
}

// upload は添付する（name は送る前にパーセントで符号化する）。
func (a *attachAPIEnv) upload(c *apiClient, issue, name, mediaType string, body []byte) (int, http.Header, []byte) {
	a.t.Helper()
	return a.raw(c, "POST", "/issues/"+issue+"/attachments", body, int64(len(body)),
		"Content-Type", mediaType, "X-Looptrack-Filename", url.PathEscape(name))
}

func (a *attachAPIEnv) mustUpload(issue, name, mediaType string, body []byte) attachmentJSON {
	a.t.Helper()
	code, _, b := a.upload(a.ed, issue, name, mediaType, body)
	if code != http.StatusCreated {
		a.t.Fatalf("添付 %s: %d %s", name, code, b)
	}
	var out struct {
		Attachment attachmentJSON `json:"attachment"`
		Message    string         `json:"message"`
	}
	if err := json.Unmarshal(b, &out); err != nil || out.Attachment.ID == 0 || !strings.Contains(out.Message, strconv.FormatInt(out.Attachment.ID, 10)) {
		a.t.Fatalf("添付の応答: %v %s", err, b)
	}
	return out.Attachment
}

func (a *attachAPIEnv) count(q string) int {
	a.t.Helper()
	var n int
	if err := a.db.QueryRow(q).Scan(&n); err != nil {
		a.t.Fatal(err)
	}
	return n
}

// stored は置き場の本体と一時ファイルの数。
func (a *attachAPIEnv) stored() (bodies, temps int) {
	a.t.Helper()
	_ = filepath.WalkDir(a.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(a.dir, p)
		if strings.HasPrefix(filepath.ToSlash(rel), ".tmp/") {
			temps++
		} else {
			bodies++
		}
		return nil
	})
	return bodies, temps
}

// safeHeaders は添付の経路の応答に安全のヘッダがあることを確かめる。
func safeHeaders(t *testing.T, label string, h http.Header) {
	t.Helper()
	if h.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(h.Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("%s: 安全のヘッダが無い（nosniff %q・CSP %q）", label, h.Get("X-Content-Type-Options"), h.Get("Content-Security-Policy"))
	}
}

type errBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func decodeErr(t *testing.T, b []byte) errBody {
	t.Helper()
	var e errBody
	if err := json.Unmarshal(b, &e); err != nil {
		t.Fatalf("誤りの本文が JSON でない: %v %s", err, b)
	}
	return e
}

// 受け入れ条件 1: 添付・一覧・取得・消去の各経路で、権限の無い利用者は拒否される。閲覧の権限が無いプロジェクトは
// 「存在しない」と同じ応答（状態コード・code・文面の形が、無い ID を指したときと同じ）。対照: 権限のある利用者は通る。
func TestAttachmentAPIPermissions(t *testing.T) {
	a := newAttachAPIEnv(t)
	at := a.mustUpload(a.issue, "evidence.txt", "text/plain", []byte("evidence"))
	rows := a.count("SELECT COUNT(*) FROM attachments")

	// 添付: viewer は 403、参加していない人は 404（無いイシューと同じ）、トークンなしは 401。どれも何も残さない
	code, h, b := a.upload(a.viewer, a.issue, "v.txt", "text/plain", []byte("v"))
	safeHeaders(t, "viewer の添付", h)
	if code != http.StatusForbidden || decodeErr(t, b).Error.Code != "forbidden" {
		t.Errorf("viewer の添付: %d %s", code, b)
	}
	sameAsMissing := func(label string, method, path, missingPath string, c *apiClient, body []byte, header ...string) {
		t.Helper()
		code, h, b := a.raw(c, method, path, body, int64(len(body)), header...)
		mcode, mh, mb := a.raw(c, method, missingPath, body, int64(len(body)), header...)
		safeHeaders(t, label, h)
		safeHeaders(t, label+"（無い ID）", mh)
		got, want := decodeErr(t, b), decodeErr(t, mb)
		id := func(p string) string { s := strings.Split(strings.TrimPrefix(p, "/"), "/"); return s[1] }
		if code != http.StatusNotFound || code != mcode || got.Error.Code != want.Error.Code ||
			got.Error.Message != strings.ReplaceAll(want.Error.Message, strings.ToUpper(id(missingPath)), strings.ToUpper(id(path))) {
			t.Errorf("%s: %d %s, 無い ID は %d %s", label, code, b, mcode, mb)
		}
	}
	missingIssue := strings.SplitN(a.issue, "-", 2)[0] + "-9999"
	sameAsMissing("参加していない人の添付", "POST", "/issues/"+a.issue+"/attachments", "/issues/"+missingIssue+"/attachments", a.outsider, []byte("o"),
		"Content-Type", "text/plain", "X-Looptrack-Filename", "o.txt")
	sameAsMissing("参加していない人の一覧", "GET", "/issues/"+a.issue+"/attachments", "/issues/"+missingIssue+"/attachments", a.outsider, nil)
	sameAsMissing("参加していない人の取得", "GET", "/attachments/"+strconv.FormatInt(at.ID, 10), "/attachments/"+strconv.FormatInt(at.ID+1000, 10), a.outsider, nil)
	// ほかの経路（イシューの取得）と同じ状態コードと code
	if code, _, b := a.outsider.do("GET", "/issues/"+a.issue, nil); code != http.StatusNotFound || decodeErr(t, b).Error.Code != "not_found" {
		t.Errorf("前提が崩れています: ほかの経路の「存在しない」の応答が 404 not_found でない: %d %s", code, b)
	}
	if code, h, _ := a.raw(nil, "POST", "/issues/"+a.issue+"/attachments", []byte("n"), 1, "X-Looptrack-Filename", "n.txt"); code != http.StatusUnauthorized {
		t.Errorf("トークンなしの添付: %d", code)
	} else {
		safeHeaders(t, "トークンなし", h)
	}
	if n := a.count("SELECT COUNT(*) FROM attachments"); n != rows {
		t.Errorf("拒んだ添付が残った: %d → %d 行", rows, n)
	}

	// 対照: 閲覧できる人は一覧と本体を読める
	var list struct {
		Attachments []attachmentJSON `json:"attachments"`
		Count       int              `json:"count"`
	}
	h = a.viewer.json(200, "GET", "/issues/"+a.issue+"/attachments", nil, &list)
	safeHeaders(t, "一覧", h)
	if list.Count != 1 || list.Attachments[0].ID != at.ID || list.Attachments[0].Filename != "evidence.txt" || list.Attachments[0].Purged {
		t.Errorf("一覧: %+v", list)
	}
	if code, _, b := a.raw(a.viewer, "GET", "/attachments/"+strconv.FormatInt(at.ID, 10), nil, 0); code != 200 || string(b) != "evidence" {
		t.Errorf("viewer の取得: %d %q", code, b)
	}

	// 消去: 管理者でない人は 403。参加していない人には、ある添付も無い添付も同じ応答（存在を漏らさない）
	purge := func(c *apiClient, id int64) (int, http.Header, []byte) {
		return a.raw(c, "POST", "/attachments/"+strconv.FormatInt(id, 10)+"/purge", []byte(`{"reason":"誤って添付"}`), -1, "Content-Type", "application/json")
	}
	for _, c := range []struct {
		name string
		c    *apiClient
	}{{"editor", a.ed}, {"viewer", a.viewer}, {"参加していない人", a.outsider}} {
		code, h, b := purge(c.c, at.ID)
		mcode, _, mb := purge(c.c, at.ID+1000)
		safeHeaders(t, c.name+"の消去", h)
		if code != http.StatusForbidden || code != mcode || string(b) != string(mb) {
			t.Errorf("%s の消去: %d %s / 無い添付 %d %s", c.name, code, b, mcode, mb)
		}
	}
	if n := a.count("SELECT COUNT(*) FROM attachment_purges"); n != 0 {
		t.Errorf("拒んだ消去が %d 件記録された", n)
	}
	// 対照: 管理者は消去できる。消去の後は読むと拒否、一覧では purged
	code, h, b = purge(a.admin, at.ID)
	safeHeaders(t, "管理者の消去", h)
	if code != 200 || !strings.Contains(string(b), `"changed":true`) {
		t.Fatalf("管理者の消去: %d %s", code, b)
	}
	if code, h, b := a.raw(a.viewer, "GET", "/attachments/"+strconv.FormatInt(at.ID, 10), nil, 0); code != http.StatusUnprocessableEntity || decodeErr(t, b).Error.Code != "attachment_purged" {
		t.Errorf("消去の後の取得: %d %s", code, b)
	} else {
		safeHeaders(t, "消去済みの取得", h)
	}
}

// 受け入れ条件 2: SVG と HTML は application/octet-stream と attachment、png・jpeg・gif・webp はその形式で inline
// （申告と中身の先頭が同じ形式のときだけ。申告が png でも中身が HTML なら attachment）。
// どの応答にも nosniff と sandbox がある。
func TestAttachmentAPIServeType(t *testing.T) {
	a := newAttachAPIEnv(t)
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	cases := []struct {
		name, mediaType, wantType string
		body                      []byte
		inline                    bool
	}{
		{"画面.png", "image/png", "image/png", png, true},
		{"photo.jpg", "image/jpeg", "image/jpeg", []byte("\xff\xd8\xff\xe0jpeg"), true},
		{"anim.gif", "image/gif", "image/gif", []byte("GIF89a"), true},
		{"shot.webp", "image/webp", "image/webp", []byte("RIFF\x00\x00\x00\x00WEBPVP8 "), true},
		// 申告は png でも中身が HTML なら attachment（対照は上の 画面.png。申告と中身が同じ png なら inline）
		{"fake.png", "image/png", "application/octet-stream", []byte(`<!DOCTYPE html><html><script>alert(1)</script></html>`), false},
		{"evil.svg", "image/svg+xml", "application/octet-stream", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), false},
		{"evil.html", "text/html; charset=utf-8", "application/octet-stream", []byte(`<html><script>alert(1)</script></html>`), false},
		{"log.txt", "text/plain; charset=utf-8", "application/octet-stream", []byte("PASS\n"), false},
	}
	for _, c := range cases {
		at := a.mustUpload(a.issue, c.name, c.mediaType, c.body)
		code, h, b := a.raw(a.viewer, "GET", "/attachments/"+strconv.FormatInt(at.ID, 10), nil, 0)
		if code != 200 || !bytes.Equal(b, c.body) {
			t.Errorf("%s: %d %q", c.name, code, b)
			continue
		}
		safeHeaders(t, c.name, h)
		disp, params, err := mime.ParseMediaType(h.Get("Content-Disposition"))
		wantDisp := "attachment"
		if c.inline {
			wantDisp = "inline"
		}
		if h.Get("Content-Type") != c.wantType || err != nil || disp != wantDisp || params["filename"] != c.name {
			t.Errorf("%s: Content-Type %q・Content-Disposition %q (%v), want %q %s", c.name, h.Get("Content-Type"), h.Get("Content-Disposition"), err, c.wantType, wantDisp)
		}
	}
	// 添付・一覧・誤りの応答にも付く
	code, h, _ := a.upload(a.ed, a.issue, "x.txt", "text/plain", []byte("x"))
	safeHeaders(t, "添付の応答", h)
	if code != http.StatusCreated {
		t.Errorf("添付: %d", code)
	}
	_, h, _ = a.raw(a.viewer, "GET", "/issues/"+a.issue+"/attachments", nil, 0)
	safeHeaders(t, "一覧の応答", h)
	code, h, _ = a.raw(a.viewer, "GET", "/attachments/abc", nil, 0)
	safeHeaders(t, "読めない ID", h)
	if code != http.StatusNotFound {
		t.Errorf("読めない ID: %d", code)
	}
	// 対照: 添付でない経路の CSP は画面用のまま（sandbox を全体に広げていない）
	if _, h, _ := a.raw(a.viewer, "GET", "/issues/"+a.issue, nil, 0); strings.Contains(h.Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("前提が崩れています: 添付でない経路の CSP に sandbox がある: %q", h.Get("Content-Security-Policy"))
	}
}

// 受け入れ条件 3: 2MiB（ほかの経路の本文の上限）を超え 1 ファイルの上限以下の添付は通る。上限を超える添付は 413 で拒まれ、
// 本体もメタデータも一時ファイルも残らない（Content-Length で申告したときも、申告しないときも）。ほかの経路の上限は 2MiB のまま。
func TestAttachmentAPIBodyLimit(t *testing.T) {
	a := newAttachAPIEnv(t)
	const limit = 3 << 20
	if _, _, err := store.SetAttachLimit(context.Background(), a.db, store.SettingAttachMaxFile, limit, store.SettingChange{Via: "command"}); err != nil {
		t.Fatal(err)
	}
	big := bytes.Repeat([]byte("a"), maxRequestBody+1)
	at := a.mustUpload(a.issue, "big.log", "text/plain", big)
	if at.Size != int64(len(big)) {
		t.Errorf("2MiB を超える添付の大きさ = %d", at.Size)
	}
	if code, _, b := a.raw(a.viewer, "GET", "/attachments/"+strconv.FormatInt(at.ID, 10), nil, 0); code != 200 || !bytes.Equal(b, big) {
		t.Errorf("2MiB を超える添付を読み戻せない: %d（%d バイト）", code, len(b))
	}
	exact := bytes.Repeat([]byte("b"), limit)
	a.mustUpload(a.issue, "exact.log", "text/plain", exact) // 上限ちょうどは通る

	rows, events := a.count("SELECT COUNT(*) FROM attachments"), a.count("SELECT COUNT(*) FROM issue_events")
	bodies, _ := a.stored()
	over := bytes.Repeat([]byte("c"), limit+1)
	for _, c := range []struct {
		name   string
		length int64
	}{{"Content-Length あり", int64(len(over))}, {"Content-Length なし", -1}} {
		code, h, b := a.raw(a.ed, "POST", "/issues/"+a.issue+"/attachments", over, c.length,
			"Content-Type", "text/plain", "X-Looptrack-Filename", "over.log")
		safeHeaders(t, c.name, h)
		if code != http.StatusRequestEntityTooLarge || decodeErr(t, b).Error.Code != "attachment_too_large" {
			t.Errorf("%s: %d %s", c.name, code, b)
		}
		nb, temps := a.stored()
		if nb != bodies || temps != 0 || a.count("SELECT COUNT(*) FROM attachments") != rows || a.count("SELECT COUNT(*) FROM issue_events") != events {
			t.Errorf("%s: 拒んだ添付が残った（本体 %d → %d・一時ファイル %d）", c.name, bodies, nb, temps)
		}
	}
	// ほかの経路の本文の上限は 2MiB のまま
	text := strings.Repeat("d", maxRequestBody)
	if code, _, b := a.ed.do("POST", "/issues/"+a.issue+"/comments", map[string]any{"text": text}); code != http.StatusBadRequest || decodeErr(t, b).Error.Code != "invalid_json" {
		t.Errorf("コメントの本文が 2MiB を超えても通った: %d %.200s", code, b)
	}
}

// コメントと verify の記録に添付の ID を付ける（REST の形）。付けない要求の応答はこれまでと同じ。
func TestAttachmentAPICommentRefs(t *testing.T) {
	a := newAttachAPIEnv(t)
	at := a.mustUpload(a.issue, "shot.png", "image/png", []byte("\x89PNG"))
	far := a.mustUpload(a.sibling, "far.txt", "text/plain", []byte("far"))
	var out map[string]any
	a.ed.json(201, "POST", "/issues/"+a.issue+"/comments", map[string]any{"text": "画面を付けた", "attachments": []int64{at.ID}}, &out)
	var raw []byte
	if err := a.db.QueryRow(`SELECT e.detail FROM issue_events e JOIN issues i ON i.id = e.issue_id
 WHERE i.display_id = ? AND e.kind = 'comment' ORDER BY e.id DESC LIMIT 1`, a.issue).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"attachments": [`+strconv.FormatInt(at.ID, 10)+`]`) && !strings.Contains(string(raw), `"attachments":[`+strconv.FormatInt(at.ID, 10)+`]`) {
		t.Errorf("コメントの記録に添付の ID が無い: %s", raw)
	}
	e := a.ed.fail(400, "POST", "/issues/"+a.issue+"/comments", map[string]any{"text": "x", "attachments": []int64{far.ID}})
	if e.Error.Code != "attachment_not_on_issue" {
		t.Errorf("別のイシューの添付: %+v", e)
	}
}
