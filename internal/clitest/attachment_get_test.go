package clitest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// issue attachment get <添付ID>: 添付の本体を手元に保存する。
//
// 保存名と sha256 は本体の応答のヘッダ（Content-Disposition・X-Looptrack-SHA256）から取る。
// 一覧（GET /issues/{id}/attachments）のメタデータの sha256 と、保存したファイルの sha256 が一致することをここで確かめる。

const (
	pAtt11       = "/api/v1/attachments/11"
	attNotesBody = "PASS\nok  pkg\n"
)

func hexSHA(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// attBodyRes は本体の応答（サーバの apiGetAttachment と同じヘッダ）。
func attBodyRes(filename, contentType string, body []byte, sha string) Response {
	return Response{ContentType: contentType, Body: string(body), Header: map[string]string{
		"Content-Disposition": fmt.Sprintf("attachment; filename=%q", filename),
		"X-Looptrack-SHA256":  sha,
	}}
}

// attListRes は一覧（GET /issues/{id}/attachments）の応答。sha256 は本体から計算した値。
func attListRes(id int, filename, mediaType string, body []byte) string {
	return fmt.Sprintf(`{"issue":"DEMO-0001","attachments":[{"id":%d,"issue":"DEMO-0001","project":"demo","filename":%q,"media_type":%q,"size":%d,`+
		`"sha256":%q,"created_at":"2024-05-02T02:40:00Z","via":"cli","purged":false,"url":"/im/api/v1/attachments/%d","inline":false}],"count":1}`,
		id, filename, mediaType, len(body), hexSHA(body), id)
}

var attNotesRes = attBodyRes("notes.txt", "text/plain; charset=utf-8", []byte(attNotesBody), hexSHA([]byte(attNotesBody)))

// attPurgedRes はサーバの消去済みの応答（422 attachment_purged。service.OpenAttachment）。
var attPurgedRes = ErrorResponse(422, "attachment_purged", "添付 11（notes.txt）の本体は管理者が消去しました")

func attachmentGetCases() []Case {
	existing := Seed{Path: "ws/notes.txt", Content: "手元で書いた内容\n"}
	return []Case{
		{Name: "attachment-get/out", Args: []string{"attachment", "get", "11", "--out", "saved/copy.txt"},
			Seeds: []Seed{{Path: "ws/saved/.keep", Content: ""}}, Routes: []Route{r("GET", pAtt11, attNotesRes)}},
		{Name: "attachment-get/into-dir", Args: []string{"attachment", "get", "11", "--out", "saved"},
			Seeds: []Seed{{Path: "ws/saved/.keep", Content: ""}}, Routes: []Route{r("GET", pAtt11, attNotesRes)}},
		{Name: "attachment-get/purged", Args: []string{"attachment", "get", "11"}, Routes: []Route{r("GET", pAtt11, attPurgedRes)}},
		{Name: "attachment-get/exists", Args: []string{"attachment", "get", "11"}, Seeds: []Seed{existing}, Routes: []Route{r("GET", pAtt11, attNotesRes)}},
		{Name: "attachment-get/exists-out", Args: []string{"attachment", "get", "11", "--out", "notes.txt"}, Seeds: []Seed{existing},
			Routes: []Route{r("GET", pAtt11, attNotesRes)}},
		{Name: "attachment-get/sha-mismatch", Args: []string{"attachment", "get", "11"},
			Routes: []Route{r("GET", pAtt11, attBodyRes("notes.txt", "text/plain; charset=utf-8", []byte(attNotesBody), strings.Repeat("0", 64)))}},
		{Name: "attachment-get/no-sha-header", Args: []string{"attachment", "get", "11"},
			Routes: []Route{r("GET", pAtt11, attBodyRes("notes.txt", "text/plain; charset=utf-8", []byte(attNotesBody), ""))}},
		{Name: "attachment-get/no-filename", Args: []string{"attachment", "get", "11"},
			Routes: []Route{r("GET", pAtt11, Response{Body: attNotesBody})}},
		{Name: "attachment-get/path-in-filename", Args: []string{"attachment", "get", "11"},
			Routes: []Route{r("GET", pAtt11, attBodyRes("../../etc/evil.txt", "text/plain; charset=utf-8", []byte(attNotesBody), hexSHA([]byte(attNotesBody))))}},
		{Name: "attachment-get/drive-in-filename", Args: []string{"attachment", "get", "11"},
			Routes: []Route{r("GET", pAtt11, attBodyRes("C:evil.txt", "text/plain; charset=utf-8", []byte(attNotesBody), hexSHA([]byte(attNotesBody))))}},
		{Name: "attachment-get/drive-only-filename", Args: []string{"attachment", "get", "11"},
			Routes: []Route{r("GET", pAtt11, attBodyRes("C:", "text/plain; charset=utf-8", []byte(attNotesBody), hexSHA([]byte(attNotesBody))))}},
		{Name: "attachment-get/bad-id", Args: []string{"attachment", "get", "abc"}},
		{Name: "attachment-get/old-server", Args: []string{"attachment", "get", "11"}},
	}
}

func fileContent(res Result, path string) ([]byte, bool) {
	for _, f := range res.Files {
		if f.Path == path {
			return f.Content, true
		}
	}
	return nil, false
}

// 受け入れ条件 1: 保存したファイルの SHA-256 が一覧の sha256 と一致する（テキストと、NUL を含むバイナリの両方）。
func TestAttachmentGetSavesBytesMatchingListSHA(t *testing.T) {
	impl := GoImpl(goBin(t))
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x00\xff\xfe")
	for _, c := range []struct {
		name, filename, mediaType string
		body                      []byte
	}{
		{"テキスト", "notes.txt", "text/plain; charset=utf-8", []byte(attNotesBody)},
		{"バイナリ", "shot.png", "image/png", png},
	} {
		var listed struct {
			Attachments []struct {
				SHA256 string `json:"sha256"`
			} `json:"attachments"`
		}
		if err := json.Unmarshal([]byte(attListRes(11, c.filename, c.mediaType, c.body)), &listed); err != nil || len(listed.Attachments) != 1 {
			t.Fatalf("%s: 一覧の fixture を読めません: %v", c.name, err)
		}
		wantSHA := listed.Attachments[0].SHA256 // 一覧のメタデータの sha256
		res := Run(t, impl, Case{Name: "attachment-get-sha/" + c.name, Args: []string{"attachment", "get", "11"},
			Routes: []Route{r("GET", pAtt11, attBodyRes(c.filename, c.mediaType, c.body, wantSHA))}})
		got, ok := fileContent(res, "ws/"+c.filename)
		if !ok || res.Code != 0 {
			t.Fatalf("%s: 保存されていません: exit %d・stderr %q・files %d", c.name, res.Code, res.Stderr, len(res.Files))
		}
		if wantSHA == "" || hexSHA(got) != wantSHA || !bytes.Equal(got, c.body) {
			t.Errorf("%s: 保存したファイルの sha256 %s が一覧の sha256 %q と一致しません", c.name, hexSHA(got), wantSHA)
		}
		if !strings.Contains(res.Stdout, wantSHA) {
			t.Errorf("%s: 出力に sha256 が出ていません: %q", c.name, res.Stdout)
		}
	}
}

// 受け入れ条件 2: 消去済みの添付は、理由を出して終了コード 1。何も保存しない。
func TestAttachmentGetPurgedExits1(t *testing.T) {
	impl := GoImpl(goBin(t))
	res := Run(t, impl, Case{Name: "attachment-get-purged/exit", Args: []string{"attachment", "get", "11"}, Routes: []Route{r("GET", pAtt11, attPurgedRes)}})
	if res.Code != 1 || !strings.Contains(res.Stderr, "消去しました") || len(res.Files) != 0 || res.Stdout != "" {
		t.Errorf("exit %d・stderr %q・stdout %q・files %d", res.Code, res.Stderr, res.Stdout, len(res.Files))
	}
}

// 既にあるファイルは上書きしない（終了コード 1・中身は元のまま）。--out で指しても同じ。
// 対照: 既存のファイルが無ければ保存する（経路が生きている）。
func TestAttachmentGetNeverOverwrites(t *testing.T) {
	impl := GoImpl(goBin(t))
	mine := "手元で書いた内容\n"
	for _, args := range [][]string{{"attachment", "get", "11"}, {"attachment", "get", "11", "--out", "notes.txt"}} {
		res := Run(t, impl, Case{Name: "attachment-get-exists/" + strings.Join(args[3:], ""), Args: args,
			Seeds: []Seed{{Path: "ws/notes.txt", Content: mine}}, Routes: []Route{r("GET", pAtt11, attNotesRes)}})
		got, _ := fileContent(res, "ws/notes.txt")
		if res.Code != 1 || string(got) != mine || !strings.Contains(res.Stderr, "上書きしません") {
			t.Errorf("%v: exit %d・中身 %q・stderr %q", args, res.Code, got, res.Stderr)
		}
	}
	res := Run(t, impl, Case{Name: "attachment-get-exists/control", Args: []string{"attachment", "get", "11"}, Routes: []Route{r("GET", pAtt11, attNotesRes)}})
	if got, ok := fileContent(res, "ws/notes.txt"); res.Code != 0 || !ok || string(got) != attNotesBody {
		t.Errorf("前提が崩れています（既存のファイルが無いのに保存されない）: exit %d・stderr %q", res.Code, res.Stderr)
	}
}

// 本体の sha256 がサーバの記録と違えば、何も保存せず終了コード 1。
func TestAttachmentGetShaMismatchSavesNothing(t *testing.T) {
	impl := GoImpl(goBin(t))
	bad := attBodyRes("notes.txt", "text/plain; charset=utf-8", []byte(attNotesBody), strings.Repeat("0", 64))
	res := Run(t, impl, Case{Name: "attachment-get-mismatch/exit", Args: []string{"attachment", "get", "11"}, Routes: []Route{r("GET", pAtt11, bad)}})
	if res.Code != 1 || len(res.Files) != 0 || !strings.Contains(res.Stderr, "一致しません") {
		t.Errorf("exit %d・files %d・stderr %q", res.Code, len(res.Files), res.Stderr)
	}
}
