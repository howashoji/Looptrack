package service

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// 添付の ID をコメント・verify の記録に付ける（REST・CLI の土台）。指せるのはそのイシューの消去していない添付だけ。

// lastEventDetail はイシューの kind の直近の記録の detail を読む（無ければ nil）。
func (e *attachEnv) lastEventDetail(issueID int64, kind string) map[string]any {
	e.t.Helper()
	var raw []byte
	err := e.admin.QueryRowContext(e.ctx, "SELECT detail FROM issue_events WHERE issue_id = ? AND kind = ? ORDER BY id DESC LIMIT 1", issueID, kind).Scan(&raw)
	if err != nil {
		e.t.Fatal(err)
	}
	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		e.t.Fatal(err)
	}
	return d
}

func idsOf(v any) []int64 {
	list, _ := v.([]any)
	var out []int64
	for _, x := range list {
		f, _ := x.(float64)
		out = append(out, int64(f))
	}
	return out
}

func TestCommentAttachmentRefs(t *testing.T) {
	e := newAttachEnv(t)
	own := e.mustAttach(e.editor, e.issue, "own.txt", "own")
	sibling := e.mustAttach(e.editor, e.issue2, "sibling.txt", "sibling") // 同じプロジェクトの別のイシュー
	foreign := e.mustAttach(e.editor, e.otherIssue, "foreign.txt", "far") // 別のプロジェクト
	purged := e.mustAttach(e.editor, e.issue, "gone.txt", "gone")
	if _, err := e.s.PurgeAttachment(e.ctx, e.root, purged.ID, ""); err != nil {
		t.Fatal(err)
	}

	// 対照: 添付を付けないコメントの記録には attachments が無い（これまでと同じ形）
	if _, err := e.s.Comment(e.ctx, e.editor, e.demo, e.issue.Row.ID, "添付なし"); err != nil {
		t.Fatal(err)
	}
	if d := e.lastEventDetail(e.issue.Row.ID, "comment"); d["attachments"] != nil {
		t.Errorf("添付なしのコメントに attachments が付いた: %v", d)
	}
	// 付けたコメント: 同じ ID は 1 つにまとめる。本文には書かない
	it, err := e.s.CommentAttach(e.ctx, e.editor, e.demo, e.issue.Row.ID, "スクリーンショットを付けた", []int64{own.ID, own.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got := idsOf(e.lastEventDetail(e.issue.Row.ID, "comment")["attachments"]); !slices.Equal(got, []int64{own.ID}) {
		t.Errorf("記録の attachments = %v, want [%d]", got, own.ID)
	}
	if c := it.Row.Doc.Comments[len(it.Row.Doc.Comments)-1].Content; c != "スクリーンショットを付けた" {
		t.Errorf("コメントの本文が変わった: %q", c)
	}

	// 拒否: 別のイシュー・別のプロジェクト・無い ID・消去済み。どれも何も記録しない
	comments, events := len(it.Row.Doc.Comments), e.count("SELECT COUNT(*) FROM issue_events")
	for _, c := range []struct {
		name string
		id   int64
		code string
	}{
		{"同じプロジェクトの別のイシュー", sibling.ID, "attachment_not_on_issue"},
		{"別のプロジェクト", foreign.ID, "attachment_not_on_issue"},
		{"無い ID", foreign.ID + 1000, "attachment_not_on_issue"},
		{"消去済み", purged.ID, "attachment_purged"},
	} {
		_, err := e.s.CommentAttach(e.ctx, e.editor, e.demo, e.issue.Row.ID, "x", []int64{own.ID, c.id})
		if errCode(err) != c.code || errKind(err) != Invalid {
			t.Errorf("%s: err = %v (code %q), want %s", c.name, err, errCode(err), c.code)
		}
	}
	cur, err := e.s.Detail(e.ctx, e.demo, e.issue.Row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cur.Row.Doc.Comments) != comments || e.count("SELECT COUNT(*) FROM issue_events") != events {
		t.Errorf("拒んだコメントが残った（コメント %d → %d）", comments, len(cur.Row.Doc.Comments))
	}
	// 上限: 指せる数を超えたら拒む（重ねた同じ ID は 1 つに数える）
	many := make([]int64, 0, MaxAttachmentRefs+1)
	for i := range MaxAttachmentRefs + 1 {
		many = append(many, own.ID+int64(i))
	}
	if _, err := e.s.CommentAttach(e.ctx, e.editor, e.demo, e.issue.Row.ID, "x", many); errCode(err) != "invalid_argument" {
		t.Errorf("上限を超える数: %v", err)
	}
}

const refsVerifyBody = "説明\n\n## 検証コマンド\n\n```bash\ngo test ./...\n```\n"

func TestVerifyAttachmentRefs(t *testing.T) {
	e := newAttachEnv(t)
	it, err := e.s.Create(e.ctx, e.editor, e.demo, CreateInput{Title: "検証あり", Body: refsVerifyBody}, i18n.JA)
	if err != nil {
		t.Fatal(err)
	}
	full := e.mustAttach(e.editor, it, "verify-output.txt", strings.Repeat("line\n", 2000))
	foreign := e.mustAttach(e.editor, e.issue, "other.txt", "other")
	sum := func() string {
		cur, err := e.s.Detail(e.ctx, e.demo, it.Row.ID)
		if err != nil {
			t.Fatal(err)
		}
		return domain.BodySHA256(cur.Row.Doc.BodyMain)
	}
	in := func(ids ...int64) VerifyInput {
		return VerifyInput{BodySHA256: sum(), Host: "h", Workspace: "w", Attachments: ids,
			Results: []store.VerifyResult{{Command: "go test ./...", Status: "ok", ExitCode: new(int), DurationMS: 1500, OutputTail: strings.Repeat("x", 5000)}}}
	}

	// 対照: 添付なしの記録
	plain, err := e.s.RecordVerify(e.ctx, e.editor, e.demo, it.Row.ID, in(), i18n.JA)
	if err != nil {
		t.Fatal(err)
	}
	if plain.Detail.Attachments != nil {
		t.Errorf("添付なしの記録に attachments: %v", plain.Detail.Attachments)
	}
	with, err := e.s.RecordVerify(e.ctx, e.editor, e.demo, it.Row.ID, in(full.ID), i18n.JA)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(with.Detail.Attachments, []int64{full.ID}) {
		t.Errorf("記録の attachments = %v", with.Detail.Attachments)
	}
	// コメントの本文と output_tail は添付の有無で変わらない
	comment := func(r *VerifyRecord) string { return r.Issue.Row.Doc.Comments[r.Detail.CommentSeq-1].Content }
	if comment(with) != comment(plain) {
		t.Errorf("コメントの本文が変わった:\n%s\n---\n%s", comment(with), comment(plain))
	}
	if with.Detail.Results[0].OutputTail != plain.Detail.Results[0].OutputTail || len(with.Detail.Results[0].OutputTail) > 4096 {
		t.Errorf("output_tail が変わった（%d バイト）", len(with.Detail.Results[0].OutputTail))
	}
	// 後続（エビデンスの無い Done の拒否）が読む形: 直近の記録の本文のハッシュと添付
	last, err := store.LastVerify(e.ctx, e.admin, it.Row.ID)
	if err != nil || last == nil || last.BodySHA256 != sum() || !slices.Equal(last.Attachments, []int64{full.ID}) {
		t.Errorf("LastVerify = %+v, %v", last, err)
	}
	// 別のイシューの添付は指せない（何も記録しない）
	before := e.count("SELECT COUNT(*) FROM issue_events WHERE kind = 'verify'")
	if _, err := e.s.RecordVerify(e.ctx, e.editor, e.demo, it.Row.ID, in(foreign.ID), i18n.JA); errCode(err) != "attachment_not_on_issue" {
		t.Errorf("別のイシューの添付: %v", err)
	}
	if e.count("SELECT COUNT(*) FROM issue_events WHERE kind = 'verify'") != before {
		t.Error("拒んだ記録が残った")
	}
}

// failReader は読まれたら失敗させる（本文を読まずに拒むことを確かめる）。
type failReader struct{ t *testing.T }

func (f failReader) Read([]byte) (int, error) {
	f.t.Error("申告が上限を超えているのに本文を読んだ")
	return 0, io.EOF
}

func TestAttachDeclaredTooLarge(t *testing.T) {
	e := newAttachEnv(t)
	if _, _, err := store.SetAttachLimit(e.ctx, e.admin, store.SettingAttachMaxFile, 8, store.SettingChange{Via: "command"}); err != nil {
		t.Fatal(err)
	}
	rows := e.count("SELECT COUNT(*) FROM attachments")
	_, err := e.s.Attach(e.ctx, e.editor, AttachInput{Issue: e.issue.Item.ID, Filename: "big.bin", Body: failReader{t}, Declared: 9})
	if errCode(err) != "attachment_too_large" || errKind(err) != TooLarge {
		t.Errorf("申告が上限を超える: %v", err)
	}
	bodies, temps := e.files()
	if len(bodies) != 0 || len(temps) != 0 || e.count("SELECT COUNT(*) FROM attachments") != rows {
		t.Errorf("拒んだ添付が残った: %v %v", bodies, temps)
	}
	// 対照: 申告がちょうど上限なら読んで通す
	if _, err := e.s.Attach(e.ctx, e.editor, AttachInput{Issue: e.issue.Item.ID, Filename: "ok.bin", Body: strings.NewReader("12345678"), Declared: 8}); err != nil {
		t.Errorf("上限ちょうど: %v", err)
	}
}

// 本体を返す形式は、申告が png・jpeg・gif・webp で、しかも中身の先頭がその形式のときだけ inline。
// 申告が画像でも中身が HTML なら attachment（対照: 申告と中身が同じ画像なら inline）。
func TestAttachmentServeType(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	jpeg := []byte("\xff\xd8\xff\xe0\x00\x10JFIF")
	gif := []byte("GIF89a\x01\x00")
	webp := []byte("RIFF\x00\x00\x00\x00WEBPVP8 ")
	html := []byte("<!DOCTYPE html><html><script>alert(1)</script></html>")
	for _, c := range []struct {
		name, in string
		head     []byte
		ct       string
		inline   bool
	}{
		{"申告 png・中身 png（対照）", "image/png", png, "image/png", true},
		{"申告 png・中身 HTML", "image/png", html, "application/octet-stream", false},
		{"申告 png・中身が空", "image/png", nil, "application/octet-stream", false},
		{"申告 png・中身 gif", "image/png", gif, "application/octet-stream", false},
		{"申告の大文字と引数", "IMAGE/PNG; name=x", png, "image/png", true},
		{"jpeg", "image/jpeg", jpeg, "image/jpeg", true},
		{"gif", "image/gif", gif, "image/gif", true},
		{"webp", "image/webp", webp, "image/webp", true},
		{"svg", "image/svg+xml", []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>"), "application/octet-stream", false},
		{"html", "text/html; charset=utf-8", html, "application/octet-stream", false},
		{"申告 HTML・中身 png", "text/html", png, "application/octet-stream", false},
		{"text", "text/plain", []byte("PASS\n"), "application/octet-stream", false},
		{"申告なし", "", png, "application/octet-stream", false},
		{"壊れた申告", "image/png garbage;;", png, "application/octet-stream", false},
	} {
		ct, inline := AttachmentServeType(c.in, c.head)
		if ct != c.ct || inline != c.inline {
			t.Errorf("%s: %q → %q %v, want %q %v", c.name, c.in, ct, inline, c.ct, c.inline)
		}
		// 本体から読む形も同じ結果
		ct2, inline2 := AttachmentServeTypeOf(c.in, bytes.NewReader(c.head))
		if ct2 != ct || inline2 != inline {
			t.Errorf("%s: AttachmentServeTypeOf → %q %v", c.name, ct2, inline2)
		}
	}
}
