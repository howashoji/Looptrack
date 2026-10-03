package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/store"
)

// CLI（issue attach・comment --attach・verify --attach-output）とサーバの往復。

var reDecimal = regexp.MustCompile(`[0-9]+\.[0-9]+`)

// 受け入れ条件 4: verify --attach-output の記録に、出力の全文の添付の ID が付く。記録のコメントの本文と output_tail（4,096 バイト）は、
// 付けないとき（対照）と変わらない。全文は切らずにマスクして残る。
func TestVerifyAttachOutputCLI(t *testing.T) {
	a := newAttachAPIEnv(t)
	body := "説明\n\n## 検証コマンド\n\n```bash\n" +
		"i=1; while [ $i -le 1500 ]; do echo \"line $i\"; i=$((i+1)); done\n" +
		"echo token=s3cr3t-value\n" +
		"```\n"
	var created struct {
		Issue issueDetailJSON `json:"issue"`
	}
	a.ed.json(201, "POST", "/projects/"+a.pr.Slug+"/issues", map[string]any{"title": "長い出力", "body": body}, &created)
	id := created.Issue.ID
	run := issueCLI(t, a.env, a.pr.Slug, a.ed.token, "LOOPTRACK_USAGE=0", "LOOPTRACK_LANG=ja")
	last := func() (store.VerifyDetail, string) {
		t.Helper()
		var raw []byte
		if err := a.db.QueryRow(`SELECT e.detail FROM issue_events e JOIN issues i ON i.id = e.issue_id
 WHERE i.display_id = ? AND e.kind = 'verify' ORDER BY e.id DESC LIMIT 1`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var d store.VerifyDetail
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatal(err)
		}
		var it issueDetailJSON
		a.ed.json(200, "GET", "/issues/"+id, nil, &it)
		return d, it.Comments[d.CommentSeq-1].Content
	}

	if r := run("verify", id); r.code != 0 {
		t.Fatalf("対照の verify: exit %d %s%s", r.code, r.stdout, r.stderr)
	}
	plain, plainComment := last()
	r := run("verify", id, "--attach-output")
	if r.code != 0 {
		t.Fatalf("verify --attach-output: exit %d %s%s", r.code, r.stdout, r.stderr)
	}
	with, withComment := last()
	if plain.Attachments != nil || len(with.Attachments) != 1 {
		t.Fatalf("記録の attachments: 対照 %v・--attach-output %v", plain.Attachments, with.Attachments)
	}
	if !strings.Contains(r.stdout, "添付 ID "+strconv.FormatInt(with.Attachments[0], 10)) {
		t.Errorf("添付の ID を出していない: %s", r.stdout)
	}
	if reDecimal.ReplaceAllString(withComment, "N") != reDecimal.ReplaceAllString(plainComment, "N") {
		t.Errorf("記録のコメントの本文が変わった:\n%s\n---\n%s", withComment, plainComment)
	}
	for i := range with.Results {
		if with.Results[i].OutputTail != plain.Results[i].OutputTail || len(with.Results[i].OutputTail) > 4096 {
			t.Errorf("output_tail[%d] が変わった（%d バイト・対照 %d バイト）", i, len(with.Results[i].OutputTail), len(plain.Results[i].OutputTail))
		}
	}
	if !strings.Contains(plain.Results[0].OutputTail, "line 1500") || strings.Contains(plain.Results[0].OutputTail, "line 1\n") {
		t.Errorf("前提が崩れています: 対照の output_tail が切られていない（%d バイト）", len(plain.Results[0].OutputTail))
	}
	code, _, full := a.raw(a.viewer, "GET", "/attachments/"+strconv.FormatInt(with.Attachments[0], 10), nil, 0)
	text := string(full)
	if code != 200 || !strings.Contains(text, "line 1\n") || !strings.Contains(text, "line 1500\n") || len(full) <= 4096 {
		t.Errorf("全文の添付: %d（%d バイト）", code, len(full))
	}
	if strings.Contains(text, "s3cr3t") || !strings.Contains(text, "token=***") {
		t.Errorf("全文の添付がマスクされていない")
	}
	var list struct {
		Attachments []attachmentJSON `json:"attachments"`
	}
	a.viewer.json(200, "GET", "/issues/"+id+"/attachments", nil, &list)
	if len(list.Attachments) != 1 || list.Attachments[0].Filename != "verify-output-"+id+".txt" || list.Attachments[0].Via != "cli" {
		t.Errorf("一覧: %+v", list.Attachments)
	}
}

// issue attach と comment --attach の往復。添付の ID を出し、コメントの記録に付く。
func TestAttachCommentCLI(t *testing.T) {
	a := newAttachAPIEnv(t)
	dir := t.TempDir()
	png := filepath.Join(dir, "画面.png")
	if err := os.WriteFile(png, []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := issueCLI(t, a.env, a.pr.Slug, a.ed.token, "LOOPTRACK_USAGE=0", "LOOPTRACK_LANG=ja")
	r := run("attach", a.issue, png, "--json")
	var out struct {
		Attachments []attachmentJSON `json:"attachments"`
	}
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &out) != nil || len(out.Attachments) != 1 {
		t.Fatalf("attach: exit %d %s%s", r.code, r.stdout, r.stderr)
	}
	at := out.Attachments[0]
	if at.Filename != "画面.png" || at.MediaType != "image/png" {
		t.Errorf("添付: %+v", at)
	}
	code, h, _ := a.raw(a.viewer, "GET", "/attachments/"+strconv.FormatInt(at.ID, 10), nil, 0)
	if code != 200 || h.Get("Content-Type") != "image/png" || !strings.HasPrefix(h.Get("Content-Disposition"), "inline") {
		t.Errorf("本体: %d %v", code, h)
	}

	r = run("comment", a.issue, "画面を付けた", "--attach", png)
	if r.code != 0 {
		t.Fatalf("comment --attach: exit %d %s%s", r.code, r.stdout, r.stderr)
	}
	var raw []byte
	if err := a.db.QueryRow(`SELECT e.detail FROM issue_events e JOIN issues i ON i.id = e.issue_id
 WHERE i.display_id = ? AND e.kind = 'comment' ORDER BY e.id DESC LIMIT 1`, a.issue).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var d struct {
		Attachments []int64 `json:"attachments"`
	}
	if err := json.Unmarshal(raw, &d); err != nil || len(d.Attachments) != 1 || d.Attachments[0] == at.ID {
		t.Errorf("コメントの記録の attachments = %v（%s）", d.Attachments, raw)
	}
	var list struct {
		Attachments []attachmentJSON `json:"attachments"`
	}
	a.viewer.json(200, "GET", "/issues/"+a.issue+"/attachments", nil, &list)
	var ids []int64
	for _, x := range list.Attachments {
		ids = append(ids, x.ID)
	}
	if !slices.Contains(ids, d.Attachments[0]) || len(ids) != 2 {
		t.Errorf("一覧 %v にコメントの添付 %v が無い", ids, d.Attachments)
	}
	// 対照: 添付なしのコメントは attachments を送らず、記録にも付かない
	if r := run("comment", a.issue, "添付なし"); r.code != 0 {
		t.Fatalf("comment: exit %d %s", r.code, r.stderr)
	}
	if err := a.db.QueryRow(`SELECT e.detail FROM issue_events e JOIN issues i ON i.id = e.issue_id
 WHERE i.display_id = ? AND e.kind = 'comment' ORDER BY e.id DESC LIMIT 1`, a.issue).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "attachments") {
		t.Errorf("添付なしのコメントに attachments: %s", raw)
	}
}
