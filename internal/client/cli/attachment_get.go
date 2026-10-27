package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/i18n"
)

// attachmentSHAHeader は本体の応答がメタデータの sha256 を載せるヘッダ（サーバは GET /attachments/{id} が付ける）。
const attachmentSHAHeader = "X-Looptrack-SHA256"

// cmdAttachmentGet は issue attachment get <添付ID> [--out <パス>]。添付の本体を手元に保存する。
//
// 添付の ID だけで引く（GET /attachments/{id}）。保存名と sha256 は本体の応答のヘッダ
// （Content-Disposition の filename と X-Looptrack-SHA256）から取る。一覧（GET /issues/{id}/attachments）は
// イシューの ID が要るので使わない。ヘッダの値はサーバが一覧のメタデータと同じ値で付けている。
//
// 保存の前に、受け取ったバイト列の sha256 をヘッダの値と照らし、違えば何も書かずに止まる。
// ヘッダが無い古いサーバでは照合できないので、照合しなかったことを出して保存する。
// 既にあるファイルは上書きしない（O_EXCL。確かめてから書くまでの間に作られても上書きしない）。
// 消去済みはサーバが 422 attachment_purged を返すので、その文面をそのまま出して終了コード 1。
func cmdAttachmentGet(c *Ctx, v *Values) error {
	id, err := strconv.ParseInt(v.Str("id"), 10, 64)
	if err != nil || id <= 0 {
		return i18n.Errorf("cli.err.attachment_id", "id", v.Str("id"))
	}
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	res, err := cl.Do(api.Request{Method: http.MethodGet, Path: "/attachments/" + strconv.FormatInt(id, 10), Raw: true,
		Headers: map[string]string{"Accept": "*/*"}})
	if err != nil {
		var ae *api.Error
		if errors.As(err, &ae) && ae.Status == http.StatusNotFound && ae.Code == "unknown_api" {
			return i18n.Errorf("cli.err.attachment_get_unsupported")
		}
		return err
	}
	name := attachmentFilename(res.Header.Get("Content-Disposition"))

	dest, err := attachmentDest(c, v.Str("out"), name)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(res.Raw)
	got := hex.EncodeToString(sum[:])
	want := strings.ToLower(strings.TrimSpace(res.Header.Get(attachmentSHAHeader)))
	if want != "" && want != got {
		return i18n.Errorf("cli.err.attachment_get_sha_mismatch", "id", id, "want", want, "got", got)
	}

	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return i18n.Errorf("cli.err.attachment_get_exists", "path", dest)
	}
	if err != nil {
		return i18n.Errorf("cli.err.attachment_get_write", "path", dest, "reason", errReason(err))
	}
	_, werr := f.Write(res.Raw)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(dest) // 書きかけを残さない（自分が O_EXCL で作ったファイルだけ）
		return i18n.Errorf("cli.err.attachment_get_write", "path", dest, "reason", errReason(werr))
	}
	if want == "" {
		c.Println(i18n.T(c.Lang, "cli.attachment_get.saved_unverified", "id", id, "path", dest, "size", len(res.Raw), "sha256", got))
		return nil
	}
	c.Println(i18n.T(c.Lang, "cli.attachment_get.saved", "id", id, "path", dest, "size", len(res.Raw), "sha256", got))
	return nil
}

// attachmentFilename は Content-Disposition の filename（無い・読めなければ ""）。ディレクトリの部分は捨てる。
// 「.」「..」は名前として使わない。先頭のドライブ接頭辞（`C:`）も外し、それでも「:」が残る名前と空の名前は使わない
// （Windows では `C:evil.txt` がドライブ C の現在のディレクトリを指して作業ディレクトリの外へ出る）。
// 判定はサーバの値についてなので、CLI が動く OS によらず同じ。
func attachmentFilename(disposition string) string {
	_, params, err := mime.ParseMediaType(disposition)
	if err != nil {
		return ""
	}
	name := strings.ReplaceAll(params["filename"], `\`, "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	if len(name) >= 2 && name[1] == ':' && (name[0]|0x20 >= 'a' && name[0]|0x20 <= 'z') {
		name = name[2:]
	}
	if name == "" || name == "." || name == ".." || strings.Contains(name, ":") {
		return ""
	}
	return name
}

// attachmentDest は保存先。--out があればそれ（既存のディレクトリなら、その中に添付の名前で）、無ければ作業ディレクトリに添付の名前で。
func attachmentDest(c *Ctx, out, name string) (string, error) {
	if out == "" {
		if name == "" {
			return "", i18n.Errorf("cli.err.attachment_get_noname")
		}
		return filepath.Abs(name)
	}
	dest, err := absUserPath(c, out)
	if err != nil {
		return "", err
	}
	if fi, err := os.Stat(dest); err == nil && fi.IsDir() {
		if name == "" {
			return "", i18n.Errorf("cli.err.attachment_get_noname")
		}
		return filepath.Join(dest, name), nil
	}
	return dest, nil
}
