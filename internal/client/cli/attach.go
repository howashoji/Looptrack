package cli

import (
	"bytes"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/client/textenc"
	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
)

// 添付（エビデンスのファイル）。issue attach・comment --attach・verify --attach / --attach-output が使う。
//
// ファイルは全部読んで確かめてから 1 つ目を送る。テキストのファイル（BOM を外し UTF-16 を直した後に NUL を含まないもの）には
// 検証の出力と同じ秘密の検査（domain.FindSecret。マスクの規則と同じ並び）を当て、当たれば 1 件も送らずに止める。
// サーバは中身をマスクできないので、ここが送る前の最後の関所になる。止めた理由には行と規則の語だけを出し、値は出さない。
//
// 古いサーバ（添付の経路が無い）とは、添付を使わない限りこれまでどおりに話す。comment・verify の本文の attachments は
// 添付を付けたときだけ送る（古いサーバは知らないキーのある本文を拒むため）。

// attachFile は送る前に読んで確かめた添付のファイル。
type attachFile struct {
	Path      string // 指定されたままのパス（表示用）
	Name      string // 送るファイル名（ディレクトリの部分を除く）
	MediaType string
	Data      []byte
}

// attachMediaTypes は拡張子からメディアタイプを決める表（OS の表は機械ごとに違うので使わない）。
// 無い拡張子は中身から推測する（http.DetectContentType）。
var attachMediaTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp",
	".svg": "image/svg+xml", ".html": "text/html; charset=utf-8", ".htm": "text/html; charset=utf-8",
	".txt": "text/plain; charset=utf-8", ".log": "text/plain; charset=utf-8", ".md": "text/markdown; charset=utf-8",
	".csv": "text/csv; charset=utf-8", ".json": "application/json", ".xml": "application/xml", ".pdf": "application/pdf",
	".zip": "application/zip", ".gz": "application/gzip", ".tar": "application/x-tar", ".mp4": "video/mp4", ".webm": "video/webm",
}

func attachMediaType(name string, data []byte) string {
	if mt, ok := attachMediaTypes[strings.ToLower(filepath.Ext(name))]; ok {
		return mt
	}
	return http.DetectContentType(data)
}

// checkAttachText はテキストのファイルに秘密の検査を当てる（バイナリは見ない）。
func checkAttachText(path string, data []byte) error {
	text := textenc.Decode(data)
	if bytes.IndexByte(text, 0) >= 0 {
		return nil
	}
	if f, ok := domain.FindSecret(string(text)); ok {
		return i18n.Errorf("cli.err.attach_secret", "path", path, "line", f.Line, "label", f.Label)
	}
	return nil
}

// readAttachments は添付するファイルを全部読み、秘密の検査を当てる（1 つでも止まれば何も返さない）。
func readAttachments(paths []string) ([]attachFile, error) {
	out := make([]attachFile, 0, len(paths))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if errors.Is(err, fs.ErrNotExist) { // OS ごとに文面が違うので、無いときは自分の文面で出す
			return nil, i18n.Errorf("cli.err.attach_not_found", "path", p)
		}
		if err != nil {
			return nil, i18n.Errorf("cli.err.attach_read", "path", p, "reason", errReason(err))
		}
		if data == nil {
			data = []byte{}
		}
		if err := checkAttachText(p, data); err != nil {
			return nil, err
		}
		name := filepath.Base(p)
		out = append(out, attachFile{Path: p, Name: name, MediaType: attachMediaType(name, data), Data: data})
	}
	return out, nil
}

// errReason は OS の誤りから、パスを除いた理由だけを取り出す（パスは文面の側で出す）。
func errReason(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

// uploadAttachment は 1 ファイルを添付し、応答の attachment を返す。
func (c *Ctx) uploadAttachment(cl *api.Client, issueID string, f attachFile) (*jsonorder.Object, string, error) {
	path, err := c.issuePath(issueID, "/attachments")
	if err != nil {
		return nil, "", err
	}
	res, err := cl.Do(api.Request{Method: http.MethodPost, Path: path, RawBody: f.Data,
		Headers: map[string]string{"Content-Type": f.MediaType, "X-Looptrack-Filename": url.PathEscape(f.Name)}})
	if err != nil {
		var ae *api.Error
		if errors.As(err, &ae) && ae.Status == http.StatusNotFound && ae.Code == "unknown_api" {
			return nil, "", i18n.Errorf("cli.err.attach_unsupported")
		}
		return nil, "", err
	}
	o := asObject(res.Value)
	return o.Object("attachment"), o.String("message"), nil
}

// uploadAttachments は files を順に添付し、添付の ID を返す（quiet でなければ 1 件ごとにサーバの文面を出す）。
// 途中で失敗したら、それまでに添付できたものはそのまま残る（添付は追記専用）。
func (c *Ctx) uploadAttachments(cl *api.Client, issueID string, files []attachFile, quiet bool) ([]any, []any, error) {
	ids := make([]any, 0, len(files))
	objs := make([]any, 0, len(files))
	for _, f := range files {
		at, msg, err := c.uploadAttachment(cl, issueID, f)
		if err != nil {
			return ids, objs, err
		}
		if at == nil {
			return ids, objs, i18n.Errorf("cli.err.missing_key", "key", "attachment")
		}
		id, _ := at.Get("id")
		ids = append(ids, id)
		objs = append(objs, at)
		if !quiet {
			c.Println(msg)
		}
	}
	return ids, objs, nil
}

// cmdAttach は issue attach <ID> <ファイル>...。添付の ID を出す。
func cmdAttach(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	files, err := readAttachments(v.List("files"))
	if err != nil {
		return err
	}
	asJSON := v.Bool("json")
	_, objs, err := c.uploadAttachments(cl, v.Str("id"), files, asJSON)
	if asJSON && (err == nil || len(objs) > 0) {
		c.PrintJSON(jsonorder.NewObject().Set("attachments", objs))
	}
	return err
}
