package transfer

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

// 添付の書き出し。export は本体を <out>/<slug>/attachments/<sha256> に、目録を <out>/<slug>/attachments.json に書く。
// 往復で運ぶのは書き出しだけで、import は添付を運ばない（取り込むには作者の利用者・経路・コメントとの結び付きを
// 別の DB に作り直す必要があり、DB の形を変えずにはできないため）。添付を含めた控えと移し替えは DB と置き場の 2 系統で取る。
// verify-files は、書き出した本体のバイトが目録の SHA-256 と一致するかを確かめる。
//
// 添付が 1 件も無いプロジェクトには目録もディレクトリも書かない（添付の無い書き出しはこれまでと同じ形のまま）。
// 置き場が決まっていない・本体が欠けている・本体が SHA-256 と合わないときは、その添付を目録に missing として載せ、
// 書き出しは最後まで続けて問題として返す（黙って欠けた書き出しを完全に見せないため。export の終了コードは 1 になる）。

// AttachmentManifestName は書き出しの添付の目録のファイル名（<out>/<slug>/ の下）。
const AttachmentManifestName = "attachments.json"

// attachmentBodyDir は書き出しの添付の本体のディレクトリ（<out>/<slug>/ の下。中身は <sha256> の名前のファイル）。
const attachmentBodyDir = "attachments"

// attachmentManifestFormat は目録の形の版。形を変えたら上げる（verify-files は知らない版を拒む）。
const attachmentManifestFormat = 1

// AttachmentManifest は書き出しの添付の目録。
type AttachmentManifest struct {
	Format      int                  `json:"format"`
	Project     string               `json:"project"`
	Attachments []ManifestAttachment `json:"attachments"`
}

// ManifestAttachment は目録の添付 1 件（DB の attachments の 1 行と、それを指すコメント・verify の記録）。
type ManifestAttachment struct {
	ID        int64  `json:"id"`
	Issue     string `json:"issue"`
	Filename  string `json:"filename"`
	MediaType string `json:"media_type"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	CreatedAt string `json:"created_at"` // RFC 3339（UTC）
	Author    string `json:"author"`     // 作者の login（利用者が分からなければ空）
	Via       string `json:"via"`
	// Purged は管理者が本体を消去した添付（本体は書き出さない）
	Purged bool `json:"purged"`
	// Missing は消去していないのに本体を書き出せなかった添付（置き場が無い・本体が欠けている・SHA-256 と合わない）
	Missing bool `json:"missing"`
	// File は書き出した本体の、<out>/<slug>/ からの相対パス（attachments/<sha256>）。書き出さなかったものは空
	File string `json:"file"`
	// Refs はこの添付を付けたコメント・verify の記録（無ければ空。issue attach だけで付けたもの）
	Refs []ManifestRef `json:"refs"`
}

// ManifestRef は添付を付けた記録 1 件。
type ManifestRef struct {
	Kind    string `json:"kind"`    // comment か verify
	Comment int    `json:"comment"` // そのイシューの何番目のコメントか（1 から。書き出した Markdown のコメントの順）
	At      string `json:"at"`      // 記録の時刻（RFC 3339・UTC）
}

var manifestSHA256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

// exportAttachments はプロジェクト p の添付を <out>/<slug>/ に書き出し、目録の件数・書いた本体の数・問題を返す。
func exportAttachments(ctx context.Context, db *sql.DB, out string, p store.Project, attachDir string) (entries, bodies int, problems []i18n.Msg, err error) {
	atts, err := store.ProjectAttachments(ctx, db, p.ID)
	if err != nil || len(atts) == 0 {
		return 0, 0, nil, err
	}
	logins, err := userLogins(ctx, db)
	if err != nil {
		return 0, 0, nil, err
	}
	refs, err := attachmentRecordRefs(ctx, db, p.ID)
	if err != nil {
		return 0, 0, nil, err
	}
	base := filepath.Join(out, p.Slug)
	if err := os.MkdirAll(filepath.Join(base, attachmentBodyDir), 0o755); err != nil {
		return 0, 0, nil, err
	}
	m := AttachmentManifest{Format: attachmentManifestFormat, Project: p.Slug, Attachments: make([]ManifestAttachment, 0, len(atts))}
	written := map[string]bool{} // 同じ本体を指す添付は 1 つだけ書く
	noDir := 0
	for _, a := range atts {
		e := ManifestAttachment{ID: a.ID, Issue: a.IssueDisplayID, Filename: a.Filename, MediaType: a.MediaType, Size: a.Size,
			SHA256: a.SHA256, CreatedAt: a.CreatedAt.UTC().Format(time.RFC3339Nano), Author: logins[a.AuthorUserID], Via: a.Via,
			Purged: a.Purged, Refs: refs[a.ID]}
		if e.Refs == nil {
			e.Refs = []ManifestRef{}
		}
		rel := attachmentBodyDir + "/" + a.SHA256
		switch {
		case a.Purged:
		case written[a.SHA256]:
			e.File = rel
		case attachDir == "":
			e.Missing = true
			noDir++
		default:
			switch err := copyAttachmentBody(service.AttachmentBodyPath(attachDir, a.SHA256), filepath.Join(base, filepath.FromSlash(rel)), a.SHA256); {
			case err == nil:
				written[a.SHA256] = true
				e.File = rel
				bodies++
			case errors.Is(err, fs.ErrNotExist):
				e.Missing = true
				problems = append(problems, i18n.M("transfer.export.attach_missing", "slug", p.Slug, "id", a.ID, "filename", a.Filename, "sha256", a.SHA256))
			case errors.Is(err, errBodyDiffers):
				e.Missing = true
				problems = append(problems, i18n.M("transfer.export.attach_corrupt", "slug", p.Slug, "id", a.ID, "filename", a.Filename, "sha256", a.SHA256))
			default:
				return 0, 0, nil, err
			}
		}
		m.Attachments = append(m.Attachments, e)
	}
	if noDir > 0 {
		problems = append(problems, i18n.MN("transfer.export.attach_no_dir", noDir, "slug", p.Slug, "count", noDir))
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return 0, 0, nil, err
	}
	if err := os.WriteFile(filepath.Join(base, AttachmentManifestName), append(b, '\n'), 0o644); err != nil {
		return 0, 0, nil, err
	}
	return len(m.Attachments), bodies, problems, nil
}

var errBodyDiffers = errors.New("attachment body does not match its sha256")

// copyAttachmentBody は置き場の本体 src を dst に写す。写しながら SHA-256 を数え、sum と合わなければ dst を消して errBodyDiffers を返す。
func copyAttachmentBody(src, dst, sum string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(out, h), in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && hex.EncodeToString(h.Sum(nil)) != sum {
		err = errBodyDiffers
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// userLogins は利用者の id → login。
func userLogins(ctx context.Context, db *sql.DB) (map[int64]string, error) {
	rows, err := db.QueryContext(ctx, "SELECT id, login FROM users")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var login string
		if err := rows.Scan(&id, &login); err != nil {
			return nil, err
		}
		out[id] = login
	}
	return out, rows.Err()
}

// attachmentRecordRefs は、プロジェクトのコメント・verify の記録（issue_events の detail.attachments）から、
// 添付の id → それを付けた記録 を作る。コメントの番号は comment の detail.seq と verify の detail.comment_seq（どちらも
// その記録で足したコメントが、イシューの何番目か）。
func attachmentRecordRefs(ctx context.Context, db *sql.DB, projectID int64) (map[int64][]ManifestRef, error) {
	rows, err := db.QueryContext(ctx, `SELECT kind, detail, at FROM issue_events
 WHERE project_id = ? AND kind IN ('comment', 'verify') AND detail IS NOT NULL
 ORDER BY at, id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]ManifestRef{}
	for rows.Next() {
		var kind string
		var detail []byte
		var at time.Time
		if err := rows.Scan(&kind, &detail, &at); err != nil {
			return nil, err
		}
		var d struct {
			Seq         int     `json:"seq"`
			CommentSeq  int     `json:"comment_seq"`
			Attachments []int64 `json:"attachments"`
		}
		if json.Unmarshal(detail, &d) != nil || len(d.Attachments) == 0 {
			continue
		}
		n := d.Seq
		if kind == "verify" {
			n = d.CommentSeq
		}
		for _, id := range d.Attachments {
			out[id] = append(out[id], ManifestRef{Kind: kind, Comment: n, At: at.UTC().Format(time.RFC3339Nano)})
		}
	}
	return out, rows.Err()
}

// AttachmentFilesReport は書き出した添付の本体の確認（verify-files）の結果 1 プロジェクト分。
type AttachmentFilesReport struct {
	Slug     string
	Bodies   int // 目録が本体を指している添付の数
	OK       int // そのうち本体のバイトが目録の SHA-256 と一致したもの
	Problems []i18n.Msg
}

// VerifyAttachmentFiles は root 直下のプロジェクトのうち目録（attachments.json）を持つものについて、目録が指す本体の
// バイトが目録の SHA-256 と一致するかを確かめる。DB は使わない（書き出したディレクトリだけで確かめる）。
func VerifyAttachmentFiles(root string) ([]AttachmentFilesReport, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []AttachmentFilesReport
	for _, ent := range entries {
		name := ent.Name()
		if !ent.IsDir() || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "scripts" {
			continue
		}
		mpath := filepath.Join(root, name, AttachmentManifestName)
		fi, err := os.Lstat(mpath)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		// 目録も本体と同じく、通常のファイルだけを上限つきで読む（リンクの先やデバイスを読まない）
		if !fi.Mode().IsRegular() {
			out = append(out, AttachmentFilesReport{Slug: name, Problems: []i18n.Msg{
				i18n.M("transfer.verify.attach_not_regular_manifest", "file", name+"/"+AttachmentManifestName)}})
			continue
		}
		raw, err := readLimited(mpath, maxManifestBytes)
		if err != nil {
			return nil, err
		}
		out = append(out, verifyManifest(filepath.Join(root, name), name, raw))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out, nil
}

func verifyManifest(dir, slug string, raw []byte) AttachmentFilesReport {
	r := AttachmentFilesReport{Slug: slug}
	var m AttachmentManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		r.Problems = append(r.Problems, i18n.M("transfer.verify.attach_bad_manifest", "file", slug+"/"+AttachmentManifestName, "reason", err.Error()))
		return r
	}
	if m.Format != attachmentManifestFormat {
		r.Problems = append(r.Problems, i18n.M("transfer.verify.attach_bad_format", "file", slug+"/"+AttachmentManifestName, "format", m.Format, "want", attachmentManifestFormat))
		return r
	}
	// 本体のディレクトリがシンボリックリンクなら、その中のファイルは書き出しの外にある。読まずに全部 NG にする
	if fi, err := os.Lstat(filepath.Join(dir, attachmentBodyDir)); err == nil && !fi.IsDir() {
		r.Problems = append(r.Problems, i18n.M("transfer.verify.attach_dir_not_dir", "file", slug+"/"+attachmentBodyDir))
		return r
	}
	for _, a := range m.Attachments {
		if a.Missing {
			r.Problems = append(r.Problems, i18n.M("transfer.verify.attach_exported_missing", "id", a.ID, "filename", a.Filename))
			continue
		}
		if a.File == "" {
			continue // 消去済み（本体は書き出さない）
		}
		r.Bodies++
		// 目録が指すのは attachments/<sha256> だけ（ほかのパスを読ませない）
		if !manifestSHA256Re.MatchString(a.SHA256) || a.File != attachmentBodyDir+"/"+a.SHA256 {
			r.Problems = append(r.Problems, i18n.M("transfer.verify.attach_bad_path", "id", a.ID, "file", a.File))
			continue
		}
		path := filepath.Join(dir, filepath.FromSlash(a.File))
		// 読むのは通常のファイルだけで、目録の size まで（シンボリックリンク・ディレクトリ・デバイスはたどらずに NG。
		// リンクをたどると書き出しの外のファイルを「一致」と数え、/dev/zero のようなものでは終わらない）
		fi, err := os.Lstat(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			r.Problems = append(r.Problems, i18n.M("transfer.verify.attach_no_body", "id", a.ID, "filename", a.Filename, "file", slug+"/"+a.File))
			continue
		case err != nil:
			r.Problems = append(r.Problems, i18n.M("transfer.verify.attach_unreadable", "id", a.ID, "file", slug+"/"+a.File, "reason", err.Error()))
			continue
		case !fi.Mode().IsRegular():
			r.Problems = append(r.Problems, i18n.M("transfer.verify.attach_not_regular", "id", a.ID, "file", slug+"/"+a.File))
			continue
		case fi.Size() != a.Size:
			r.Problems = append(r.Problems, i18n.MN("transfer.verify.attach_size_differs", int(fi.Size()), "id", a.ID, "filename", a.Filename, "file", slug+"/"+a.File, "size", fi.Size(), "want", a.Size))
			continue
		}
		got, err := fileSHA256(path, a.Size)
		switch {
		case err != nil:
			r.Problems = append(r.Problems, i18n.M("transfer.verify.attach_unreadable", "id", a.ID, "file", slug+"/"+a.File, "reason", err.Error()))
		case got != a.SHA256:
			r.Problems = append(r.Problems, i18n.M("transfer.verify.attach_differs", "id", a.ID, "filename", a.Filename, "file", slug+"/"+a.File))
		default:
			r.OK++
		}
	}
	return r
}

// maxManifestBytes は verify-files が読む目録の大きさの上限（添付 1 件は数百バイトなので、数十万件でも収まる）。
const maxManifestBytes = 256 << 20

// readLimited は path を max バイトまで読む（超えていれば誤り）。
func readLimited(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, i18n.Errorf("transfer.err.manifest_too_large", "file", path, "limit", max)
	}
	return b, nil
}

// fileSHA256 は path の先頭 max バイトまでの SHA-256（それより長くは読まない。呼び出し側が Lstat で大きさを確かめた後に使う）。
func fileSHA256(path string, max int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, max)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// readManifestCount は dir の目録の添付の件数を返す（目録が無ければ ok=false。読めない・形が違えば count=-1）。
func readManifestCount(dir string) (count int, ok bool) {
	raw, err := os.ReadFile(filepath.Join(dir, AttachmentManifestName))
	if errors.Is(err, fs.ErrNotExist) {
		return 0, false
	}
	if err != nil {
		return -1, true
	}
	var m AttachmentManifest
	if json.Unmarshal(raw, &m) != nil {
		return -1, true
	}
	return len(m.Attachments), true
}
