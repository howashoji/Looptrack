package server

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/service"
)

// MCP の添付（mcp_attach.go）。report_verify と add_comment の attachments は REST と同じ service の入口を通り、
// get_issue が添付の一覧を返す。

// mcpAttachEnv は添付の REST の環境に、検証コマンドを持つイシューと MCP の接続を足したもの。
type mcpAttachEnv struct {
	*attachAPIEnv
	m, viewerM *mcpClient
	vi, vj     string // 検証コマンドを持つイシュー（同じプロジェクト）
	on, onJ    attachmentJSON
	far        attachmentJSON // 別のプロジェクトの添付
	purged     attachmentJSON // vi の添付で、管理者が消去したもの
}

func newMCPAttachEnv(t *testing.T) *mcpAttachEnv {
	t.Helper()
	a := newAttachAPIEnv(t)
	x := &mcpAttachEnv{attachAPIEnv: a}
	create := func(title string) string {
		var created struct {
			Issue issueDetailJSON `json:"issue"`
		}
		a.ed.json(201, "POST", "/projects/"+a.pr.Slug+"/issues", map[string]any{"title": title, "body": verifyBody}, &created)
		return created.Issue.ID
	}
	x.vi, x.vj = create("検証あり"), create("検証あり（隣）")
	x.on = a.mustUpload(x.vi, "out.txt", "text/plain", []byte("全文のログ"))
	x.onJ = a.mustUpload(x.vj, "shot.png", "image/png", []byte("\x89PNG"))
	x.purged = a.mustUpload(x.vi, "old.txt", "text/plain", []byte("消すログ"))
	code, _, b := a.raw(a.admin, "POST", "/attachments/"+strconv.FormatInt(x.purged.ID, 10)+"/purge", []byte(`{"reason":"誤って添付"}`), -1, "Content-Type", "application/json")
	if code != http.StatusOK {
		t.Fatalf("消去: %d %s", code, b)
	}
	// 別のプロジェクトの添付（outsider はそのプロジェクトの editor）
	code, _, b = a.upload(a.outsider, a.otherIssue, "far.txt", "text/plain", []byte("far"))
	if code != http.StatusCreated {
		t.Fatalf("別のプロジェクトへの添付: %d %s", code, b)
	}
	var far struct {
		Attachment attachmentJSON `json:"attachment"`
	}
	if err := json.Unmarshal(b, &far); err != nil {
		t.Fatal(err)
	}
	x.far = far.Attachment
	x.m = a.mcpAs(a.ed.token, map[string]string{"X-Looptrack-Project": a.pr.Slug})
	x.viewerM = a.mcpAs(a.viewer.token, map[string]string{"X-Looptrack-Project": a.pr.Slug})
	return x
}

func (x *mcpAttachEnv) plan(id string) verifyPlanJSON {
	x.t.Helper()
	var p verifyPlanJSON
	x.ed.json(200, "GET", "/issues/"+id+"/verify", nil, &p)
	return p
}

// events は、イシューの kind の記録の数と、いちばん新しい記録の detail（無ければ空）。
func (x *mcpAttachEnv) events(issue, kind string) (n int, detail map[string]any) {
	x.t.Helper()
	n = x.count("SELECT COUNT(*) FROM issue_events e JOIN issues i ON i.id = e.issue_id WHERE i.display_id = '" + issue + "' AND e.kind = '" + kind + "'")
	if n == 0 {
		return 0, nil
	}
	var raw []byte
	if err := x.db.QueryRow(`SELECT e.detail FROM issue_events e JOIN issues i ON i.id = e.issue_id
 WHERE i.display_id = ? AND e.kind = ? ORDER BY e.id DESC LIMIT 1`, issue, kind).Scan(&raw); err != nil {
		x.t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &detail); err != nil {
		x.t.Fatal(err)
	}
	return n, detail
}

func attachIDs(v any) []int64 {
	var out []int64
	list, _ := v.([]any)
	for _, e := range list {
		if f, ok := e.(float64); ok {
			out = append(out, int64(f))
		}
	}
	return out
}

// 受け入れ条件 1: report_verify に、そのイシューのものでない添付の ID（別のイシュー・消去済み・別のプロジェクト）を
// 渡すと拒否され、記録は残らない。対照として、そのイシューの添付なら記録の detail に付く。
// 拒否の文面は REST と同じ（service の 1 か所を通る）。add_comment も同じ。
func TestReportVerifyMCPAttachmentRefs(t *testing.T) {
	x := newMCPAttachEnv(t)
	p := x.plan(x.vi)
	res := results(p.Commands)
	rejects := []struct {
		name string
		id   int64
		code string
	}{
		{"別のイシューの添付", x.onJ.ID, "attachment_not_on_issue"},
		{"別のプロジェクトの添付", x.far.ID, "attachment_not_on_issue"},
		{"無い ID", x.far.ID + 1000, "attachment_not_on_issue"},
		{"消去済み", x.purged.ID, "attachment_purged"},
	}
	for _, c := range rejects {
		text, _ := x.m.call("report_verify", map[string]any{"id": x.vi, "body_sha256": p.BodySHA256, "results": res, "attachments": []int64{x.on.ID, c.id}}, true)
		e := x.ed.fail(400, "POST", "/issues/"+x.vi+"/verify", map[string]any{"body_sha256": p.BodySHA256, "results": res, "attachments": []int64{x.on.ID, c.id}})
		if e.Error.Code != c.code || text != e.Error.Message {
			t.Errorf("report_verify（%s）: MCP %q / REST %q（code %s、期待 %s）", c.name, text, e.Error.Message, e.Error.Code, c.code)
		}
		ctext, _ := x.m.call("add_comment", map[string]any{"id": x.vi, "text": "付けたい", "attachments": []int64{c.id}}, true)
		ce := x.ed.fail(400, "POST", "/issues/"+x.vi+"/comments", map[string]any{"text": "付けたい", "attachments": []int64{c.id}})
		if ce.Error.Code != c.code || ctext != ce.Error.Message {
			t.Errorf("add_comment（%s）: MCP %q / REST %q", c.name, ctext, ce.Error.Message)
		}
	}
	if n, _ := x.events(x.vi, "verify"); n != 0 {
		t.Fatalf("拒否した report_verify の記録が残った: %d 件", n)
	}
	if n, _ := x.events(x.vi, "comment"); n != 0 {
		t.Fatalf("拒否した add_comment の記録が残った: %d 件", n)
	}
	var d issueDetailJSON
	x.ed.json(200, "GET", "/issues/"+x.vi, nil, &d)
	if len(d.Comments) != 0 {
		t.Fatalf("拒否したのにコメントが残った: %+v", d.Comments)
	}

	// 対照: そのイシューの添付なら付く（同じ ID を 2 回渡しても 1 つ）
	_, data := x.m.call("report_verify", map[string]any{"id": x.vi, "body_sha256": p.BodySHA256, "results": res, "attachments": []int64{x.on.ID, x.on.ID}}, false)
	if got := attachIDs(data["attachments"]); !reflect.DeepEqual(got, []int64{x.on.ID}) {
		t.Errorf("report_verify の応答の attachments: %v", data["attachments"])
	}
	n, det := x.events(x.vi, "verify")
	if got := attachIDs(det["attachments"]); n != 1 || !reflect.DeepEqual(got, []int64{x.on.ID}) {
		t.Errorf("report_verify の記録: %d 件 detail.attachments=%v", n, det["attachments"])
	}
	x.m.call("add_comment", map[string]any{"id": x.vi, "text": "ログを付けた", "attachments": []int64{x.on.ID}}, false)
	n, det = x.events(x.vi, "comment")
	if got := attachIDs(det["attachments"]); n != 1 || !reflect.DeepEqual(got, []int64{x.on.ID}) {
		t.Errorf("add_comment の記録: %d 件 detail.attachments=%v", n, det["attachments"])
	}

	// viewer は添付を付けた記録を書けない（権限はこれまでどおり）
	x.viewerM.call("add_comment", map[string]any{"id": x.vi, "text": "x", "attachments": []int64{x.on.ID}}, true)
	if n, _ := x.events(x.vi, "comment"); n != 1 {
		t.Errorf("viewer の add_comment が記録された: %d 件", n)
	}
}

// 受け入れ条件 2: attachments を渡した MCP の記録は、同じ入力の REST の記録と同じ detail の形になる。
// 違うのは経路で決まる印（self_reported・agent）と、記録の順を表す comment_seq だけ。
func TestReportVerifyMCPDetailMatchesREST(t *testing.T) {
	x := newMCPAttachEnv(t)
	p := x.plan(x.vi)
	res := results(p.Commands, "ok", "fail")
	body := map[string]any{"body_sha256": p.BodySHA256, "results": res, "host": "mac.local", "workspace": "im-wt", "attachments": []int64{x.on.ID}}

	x.ed.json(201, "POST", "/issues/"+x.vi+"/verify", body, nil)
	_, rest := x.events(x.vi, "verify")
	args := map[string]any{"id": x.vi}
	for k, v := range body {
		args[k] = v
	}
	x.m.call("report_verify", args, false)
	n, viaMCP := x.events(x.vi, "verify")
	if n != 2 {
		t.Fatalf("記録の数: %d", n)
	}
	// 対照: 経路の印は違う（比べる前に外すものが、本当に違っていること）
	if rest["self_reported"] == viaMCP["self_reported"] || viaMCP["self_reported"] != true {
		t.Fatalf("前提が崩れています: self_reported が REST %v・MCP %v", rest["self_reported"], viaMCP["self_reported"])
	}
	if _, ok := viaMCP["attachments"]; !ok {
		t.Fatalf("前提が崩れています: MCP の記録に attachments が無い: %v", viaMCP)
	}
	for _, m := range []map[string]any{rest, viaMCP} {
		delete(m, "self_reported")
		delete(m, "agent") // どの AI の経路かを残す印（MCP の接続が付ける。記録の中身ではない）
		delete(m, "comment_seq")
	}
	if !reflect.DeepEqual(rest, viaMCP) {
		t.Errorf("detail の形が違う:\nREST %v\nMCP  %v", rest, viaMCP)
	}
}

// get_issue は添付の一覧（ID・名前・形式・大きさ・消去済みか）を返す。消去済みも載る。添付が無いイシューの応答は、これまでと同じ。
func TestGetIssueMCPAttachments(t *testing.T) {
	x := newMCPAttachEnv(t)
	text, data := x.m.call("get_issue", map[string]any{"id": x.vi}, false)
	list, _ := data["attachments"].([]any)
	if len(list) != 2 {
		t.Fatalf("attachments: %v", data["attachments"])
	}
	want := []map[string]any{
		{"id": float64(x.on.ID), "filename": "out.txt", "media_type": "text/plain", "size": float64(len([]byte("全文のログ"))), "purged": false},
		{"id": float64(x.purged.ID), "filename": "old.txt", "media_type": "text/plain", "size": float64(len([]byte("消すログ"))), "purged": true},
	}
	for i, w := range want {
		if !reflect.DeepEqual(list[i], any(w)) {
			t.Errorf("attachments[%d]: %v / 期待 %v", i, list[i], w)
		}
	}
	for _, s := range []string{"── 添付 2 件", "- " + strconv.FormatInt(x.on.ID, 10) + " out.txt（text/plain・", "old.txt（text/plain・", "消去済みで付けられない"} {
		if !strings.Contains(text, s) {
			t.Errorf("get_issue の本文に %q が無い:\n%s", s, text)
		}
	}
	// viewer も読める
	if _, vd := x.viewerM.call("get_issue", map[string]any{"id": x.vi}, false); len(vd["attachments"].([]any)) != 2 {
		t.Errorf("viewer の get_issue: %v", vd["attachments"])
	}
	// 対照: 添付の無いイシュー（a.issue）は、一覧も見出しも出ない
	text, data = x.m.call("get_issue", map[string]any{"id": x.issue}, false)
	if _, ok := data["attachments"]; ok || strings.Contains(text, "── 添付") {
		t.Errorf("添付の無いイシュー: attachments=%v\n%s", data["attachments"], text)
	}
	// 別のプロジェクトの添付は、このイシューの一覧に出ない
	_, fd := x.m.call("get_issue", map[string]any{"id": x.vj}, false)
	if got := fd["attachments"].([]any); len(got) != 1 || got[0].(map[string]any)["id"] != float64(x.onJ.ID) {
		t.Errorf("隣のイシュー: %v", fd["attachments"])
	}
}

// 受け入れ条件 3: ツールの説明（日英）に、添付は CLI で送って返った ID を attachments に渡す旨が入っている。
// 入力項目 attachments は整数の配列で、説明にも同じ案内がある。
func TestMCPAttachmentToolDefinitions(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct{ lang, listWord string }{{"ja", "添付の一覧"}, {"en", "list of attachments"}} {
		defs := toolDefs(t, e.mcpConnectRaw(map[string]string{"Authorization": "Bearer " + e.userWithLang("ad-"+c.lang, c.lang)}))
		for _, pos := range []string{"add_comment", "report_verify", "add_comment.attachments", "report_verify.attachments"} {
			d := defs[pos]
			if !strings.Contains(d, "looptrack issue attach") || !strings.Contains(d, "attachments") && !strings.HasSuffix(pos, ".attachments") {
				t.Errorf("%s の %s の説明に、CLI で送って ID を attachments に渡す旨が無い: %q", c.lang, pos, d)
			}
		}
		if d := defs["get_issue"]; !strings.Contains(d, c.listWord) {
			t.Errorf("%s の get_issue の説明に添付の一覧が無い: %q", c.lang, d)
		}
	}
	res, err := e.mcpConnectRaw(map[string]string{"Authorization": "Bearer " + e.userWithLang("ad-type", "ja")}).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, tool := range res.Tools {
		if tool.Name != "add_comment" && tool.Name != "report_verify" {
			continue
		}
		props := tool.InputSchema.(map[string]any)["properties"].(map[string]any)
		at, _ := props["attachments"].(map[string]any)
		items, _ := at["items"].(map[string]any)
		types, _ := at["type"].([]any) // []int64 は nil も許すので [null array] になる
		if !slices.Contains(types, any("array")) || items["type"] != "integer" {
			t.Errorf("%s.attachments の型: %v", tool.Name, at)
		}
		seen++
	}
	if seen != 2 {
		t.Fatalf("前提が崩れています: 型を見たツールが %d 個（2 個のはず）", seen)
	}
}

// get_issue の text をそのまま update_issue に渡したときの witness。
// 添付の一覧を本文の末尾に足すと、コメント節のあるイシューは comments_changed で原因の分からない拒否になり、
// コメント節の無いイシューは一覧が本文に入って黙って保存される。一覧は version の行と Markdown の間にあるので、
// 一覧を残したまま渡した全文は frontmatter で始まらず、Parse が必ず止める。
func TestGetIssueMCPListNeverReachesBody(t *testing.T) {
	x := newMCPAttachEnv(t)
	noSection := func() string {
		var created struct {
			Issue issueDetailJSON `json:"issue"`
		}
		x.ed.json(201, "POST", "/projects/"+x.pr.Slug+"/issues", map[string]any{"title": "コメント節なし", "body": "説明"}, &created)
		x.mustUpload(created.Issue.ID, "log.txt", "text/plain", []byte("log"))
		if _, err := x.db.Exec("UPDATE issues SET has_comment_section = 0 WHERE display_id = ?", created.Issue.ID); err != nil {
			t.Fatal(err)
		}
		return created.Issue.ID
	}()
	bodyOf := func(id string) string {
		var b string
		if err := x.db.QueryRow("SELECT body_main FROM issues WHERE display_id = ?", id).Scan(&b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	for _, c := range []struct {
		name, id string
		section  bool
	}{{"コメント節あり", x.vi, true}, {"コメント節なし", noSection, false}} {
		text, data := x.m.call("get_issue", map[string]any{"id": c.id}, false)
		md, _ := data["markdown"].(string)
		version := data["version"]
		// 前提: 一覧は text にあり、Markdown より前にある（Markdown は text の末尾）。コメント節の有無は想定どおり
		if !strings.Contains(text, "── 添付") || !strings.HasSuffix(text, md) || strings.Index(text, "── 添付") > strings.Index(text, md) ||
			strings.Contains(md, "## コメント") != c.section {
			t.Fatalf("前提が崩れています（%s）: 一覧の位置かコメント節の有無が違う:\n%s", c.name, text)
		}
		before := bodyOf(c.id)
		edited := func(s string) string { return strings.Replace(s, "説明", "説明を直した", 1) }
		// version の行だけを消して一覧を残した全文は、うるさく拒否される。本文は変わらない
		naive := edited(text[strings.Index(text, "\n\n")+2:])
		if got, _ := x.m.call("update_issue", map[string]any{"id": c.id, "version": version, "markdown": naive}, true); got == "" {
			t.Errorf("%s: 拒否の文面が空", c.name)
		}
		if got := bodyOf(c.id); got != before || strings.Contains(got, "── 添付") {
			t.Errorf("%s: 拒否したのに本文が変わった: %q（前は %q）", c.name, got, before)
		}
		// version の行ごと渡しても同じ
		x.m.call("update_issue", map[string]any{"id": c.id, "version": version, "markdown": edited(text)}, true)
		if got := bodyOf(c.id); got != before {
			t.Errorf("%s: version の行つきの全文で本文が変わった: %q", c.name, got)
		}
		// 対照: 一覧を消して（Markdown だけを）渡せば通り、本文に添付の一覧は入らない
		x.m.call("update_issue", map[string]any{"id": c.id, "version": version, "markdown": edited(md)}, false)
		if got := bodyOf(c.id); !strings.Contains(got, "説明を直した") || strings.Contains(got, "添付") {
			t.Errorf("%s: 対照の更新後の本文: %q", c.name, got)
		}
	}
}

// 入力項目 attachments の説明に、service の上限の値がそのまま出る（日英。値を変えても説明は古くならない）。
func TestMCPAttachmentLimitInDescriptions(t *testing.T) {
	e := newEnv(t)
	limit := strconv.Itoa(service.MaxAttachmentRefs)
	for _, lang := range []string{"ja", "en"} {
		defs := toolDefs(t, e.mcpConnectRaw(map[string]string{"Authorization": "Bearer " + e.userWithLang("lim-"+lang, lang)}))
		for _, pos := range []string{"add_comment.attachments", "report_verify.attachments"} {
			d := defs[pos]
			if !strings.Contains(d, limit) || strings.Contains(d, "{") {
				t.Errorf("%s の %s の説明に上限 %s が出ていない（または未展開の {…} が残っている）: %q", lang, pos, limit, d)
			}
		}
	}
}
