package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// 添付（エビデンスのファイル）。メタデータは DB（attachments・attachment_purges。どちらも追記専用）、本体はディスクの置き場に置く。
//
// 本体は <置き場>/<sha256 の先頭 2 字>/<次の 2 字>/<sha256> に置く。同じ内容を何度添付しても本体は 1 つで、行は添付の数だけできる。
// 書き込みは一時ファイル（<置き場>/.tmp）→ fsync → rename の順で、rename の後にメタデータを入れる。既にある本体は上書きしない。
// 一時ファイルは失敗したら消すので、置き場にもメタデータにも途中の状態を残さない。rename の後でメタデータの記録が失敗したときだけは
// 本体を消さずに残す（同じ本体を別の添付が使い始めているかもしれず、消すと「本体の欠け」になるため。残った本体は整合の検査が
// 「どこからも指されない本体」として報告し、--apply で消せる）。
//
// 権限は 1 か所（ここ）で判定する: 一覧・読むのは閲覧できる人、添付は editor 以上、消去は管理者（利用者の役割 admin）。
// 消去は本体のファイルを消し、attachment_purges に記録する。同じ本体を指す添付はすべて消去済みになる（秘密を消すための操作なので、
// 1 つだけ残すと消えない）。メタデータの行は残し、読むと「消去済み」を返す。
//
// issue_events には kind attach・attach_purge を残す。この 2 つは usage.Ops（トークン情報を付ける操作）に足さない。
// 足すと未付与の検知（トークン計測）の対象に入り、添付のたびに「トークン情報が付いていない操作」として数えられるため。

// AttachDirName は置き場を既定で決めるときのディレクトリ名（$STATE_DIRECTORY・SQLite の DB の隣・デスクトップの DataDir の下）。
const AttachDirName = "attachments"

// attachTmpDir は書き込み途中の一時ファイルを置く、置き場の中のディレクトリ（rename を同じファイルシステムの中で済ませるため）。
const attachTmpDir = ".tmp"

// OrphanGrace は、どこからも指されない本体でも消さずに残す新しさ。添付は rename の後にメタデータを入れるので、
// その間に整合の検査が走ると、記録の直前の本体が「どこからも指されない」に見える。書かれて（触れられて）から
// この時間が経っていない本体は、報告だけして --apply でも消さない。
const OrphanGrace = time.Hour

// AttachDirFromEnv は添付の置き場を環境変数から決める（looptrack serve と整合の検査が使う）。
// 順は控え（update-check.json。cmd/looptrack/serve_update.go の updateStatePath）と同じで、明示の LOOPTRACK_ATTACH_DIR →
// install.sh の unit の StateDirectory（$STATE_DIRECTORY/attachments。ProtectSystem=strict で書けるのはそこだけ）→
// SQLite の DB と同じディレクトリの attachments（compose の /data・ローカルモード）。どれも無ければ空（添付は使えない）。
// STATE_DIRECTORY は複数あれば最初を使い、OS のパスの並びの区切りで読む（: で切ると Windows のドライブ文字が壊れる）。
func AttachDirFromEnv(getenv func(string) string) string {
	if d := getenv("LOOPTRACK_ATTACH_DIR"); strings.TrimSpace(d) != "" {
		return d
	}
	if ds := filepath.SplitList(getenv("STATE_DIRECTORY")); len(ds) > 0 && strings.TrimSpace(ds[0]) != "" {
		return filepath.Join(ds[0], AttachDirName)
	}
	if p, ok := store.SQLitePath(getenv("LOOPTRACK_DSN")); ok && p != "" && p != ":memory:" {
		return filepath.Join(filepath.Dir(p), AttachDirName)
	}
	return ""
}

var sha256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

// AttachmentBodyPath は本体の置き場のパス（<dir>/ab/cd/<sha256>）。
func AttachmentBodyPath(dir, sum string) string {
	return filepath.Join(dir, sum[:2], sum[2:4], sum)
}

// errTooLarge は 1 ファイルの上限を超えた添付の拒否（REST は 413。本体もメタデータも残さない）。
func errTooLarge(max int64) *Error {
	return errm(TooLarge, "attachment_too_large", i18n.M("service.err.attach.too_large", "limit", max))
}

// inlineMediaTypes は、画面の中にそのまま表示してよい形式（本体の応答を inline にする）。
// ここに無い形式は、SVG・HTML を含めてすべてダウンロードさせる（ブラウザに中身を解釈させない）。
var inlineMediaTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// AttachmentServeType は添付の本体を返すときの Content-Type と、inline で返すかを決める（REST と画面で共通の規則）。
// inline にするのは、記録したメディアタイプ（添付した側の申告）が png・jpeg・gif・webp で、しかも本体の先頭（head。
// 先頭 512 バイトまでを見る）を http.DetectContentType で見た形式が申告と同じときだけ。食い違えば、申告が画像でも
// application/octet-stream の attachment にする（画像を名乗る HTML をそのまま表示させない）。
func AttachmentServeType(mediaType string, head []byte) (contentType string, inline bool) {
	mt, _, err := mime.ParseMediaType(mediaType)
	if err == nil && inlineMediaTypes[mt] {
		if sniffed, _, err := mime.ParseMediaType(http.DetectContentType(head)); err == nil && sniffed == mt {
			return mt, true
		}
	}
	return "application/octet-stream", false
}

// AttachmentServeTypeOf は本体 body の先頭を読んで AttachmentServeType で決める（読む位置は動かさない）。
// 先頭を読めないときは attachment にする。
func AttachmentServeTypeOf(mediaType string, body io.ReaderAt) (contentType string, inline bool) {
	head := make([]byte, 512)
	n, err := body.ReadAt(head, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return "application/octet-stream", false
	}
	return AttachmentServeType(mediaType, head[:n])
}

// MaxAttachmentRefs はコメント・verify の記録 1 件が指せる添付の数の上限（MCP の入力項目の説明にも埋め込む）。
const MaxAttachmentRefs = 100

// attachmentRefs はコメント・verify の記録に付ける添付の ID を確かめる（同じ ID は 1 つにまとめ、順は保つ）。
// 指せるのは、そのイシューの添付で消去していないものだけ。別のイシューの添付を指させると、閲覧の権限の無い
// プロジェクトの添付の存在を確かめる道になるので、見つからないものと同じ文面で拒む。
func attachmentRefs(ctx context.Context, q store.Queryer, issueID int64, ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	seen := map[int64]bool{}
	var out []int64
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	if len(out) > MaxAttachmentRefs {
		return nil, errm(Invalid, "invalid_argument", i18n.M("service.err.attach.too_many_refs", "limit", MaxAttachmentRefs, "given", len(out)))
	}
	for _, id := range out {
		at, err := store.AttachmentByID(ctx, q, id)
		if errors.Is(err, store.ErrNotFound) || (err == nil && at.IssueID != issueID) {
			return nil, errm(Invalid, "attachment_not_on_issue", i18n.M("service.err.attach.not_on_issue", "id", id))
		}
		if err != nil {
			return nil, err
		}
		if at.Purged {
			return nil, errm(Invalid, "attachment_purged", i18n.M("service.err.attach.purged", "id", at.ID, "filename", at.Filename))
		}
	}
	return out, nil
}

func errAttachUnavailable() *Error {
	return errm(Rejected, "attachments_unavailable", i18n.M("service.err.attach.unavailable"))
}

// AttachInput は添付の入力。
type AttachInput struct {
	Issue     string // イシューの表示用の ID（大文字小文字を区別しない）
	Project   string // slug（空なら閲覧できる全プロジェクトから探す）
	Filename  string // ディレクトリの部分は捨てる
	MediaType string // 空なら application/octet-stream
	Body      io.Reader
	// Declared は送る側が申告した大きさ（REST の Content-Length）。1 ファイルの上限を超えていれば、本文を読まずに拒む。
	// 分からない・申告が無いときは 0 以下にする（読みながら上限で止める）
	Declared int64
}

// actorUser は操作した利用者を読む（権限の判定に使う）。
func (s *Service) actorUser(ctx context.Context, a Actor) (store.User, error) {
	if a.UserID == 0 {
		return store.User{}, errm(Forbidden, "forbidden", i18n.M("service.err.attach.no_user"))
	}
	u, err := store.UserByID(ctx, s.DB, a.UserID)
	if errors.Is(err, store.ErrNotFound) {
		return u, errm(Forbidden, "forbidden", i18n.M("service.err.attach.no_user"))
	}
	return u, err
}

// attachIssue は利用者が扱えるイシューを引く（権限の無いプロジェクトのイシューは「見つからない」）。
// write なら editor 以上を求める。
func (s *Service) attachIssue(ctx context.Context, u store.User, displayID, slug string, write bool) (store.Project, store.IssueRow, error) {
	projects, roles, err := store.AccessibleProjects(ctx, s.DB, u)
	if err != nil {
		return store.Project{}, store.IssueRow{}, err
	}
	byID := map[int64]store.Project{}
	var scope int64
	for _, pr := range projects {
		byID[pr.ID] = pr
		if pr.Slug == slug {
			scope = pr.ID
		}
	}
	if slug != "" && scope == 0 {
		return store.Project{}, store.IssueRow{}, errm(NotFound, "not_found", i18n.M("service.err.not_found.project", "slug", slug))
	}
	row, err := s.Locate(ctx, displayID, scope)
	if err != nil {
		return store.Project{}, store.IssueRow{}, err
	}
	pr, ok := byID[row.ProjectID]
	if !ok {
		return store.Project{}, store.IssueRow{}, notFound(displayID)
	}
	if write && roles[pr.ID] != "editor" && roles[pr.ID] != "admin" {
		return store.Project{}, store.IssueRow{}, errm(Forbidden, "forbidden", i18n.M("service.err.attach.forbidden", "slug", pr.Slug))
	}
	return pr, row, nil
}

// readableAttachment は添付を引き、利用者がそのプロジェクトを閲覧できることを確かめる（できなければ「見つからない」）。
func (s *Service) readableAttachment(ctx context.Context, u store.User, id int64) (store.Attachment, error) {
	at, err := store.AttachmentByID(ctx, s.DB, id)
	if errors.Is(err, store.ErrNotFound) {
		return at, errm(NotFound, "not_found", i18n.M("service.err.attach.not_found", "id", id))
	}
	if err != nil {
		return at, err
	}
	projects, _, err := store.AccessibleProjects(ctx, s.DB, u)
	if err != nil {
		return at, err
	}
	for _, p := range projects {
		if p.ID == at.ProjectID {
			return at, nil
		}
	}
	return store.Attachment{}, errm(NotFound, "not_found", i18n.M("service.err.attach.not_found", "id", id))
}

// cleanFilename はファイル名からディレクトリの部分を捨て、表示に使える名前かを確かめる。
func cleanFilename(name string) (string, error) {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.TrimSpace(name)
	bad := name == "" || name == "." || name == ".." || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 255
	for _, r := range name {
		if unicode.IsControl(r) {
			bad = true
		}
	}
	if bad {
		return "", errm(Invalid, "invalid_argument", i18n.M("service.err.attach.filename"))
	}
	return name, nil
}

// cleanMediaType はメディアタイプを正規化する（空なら application/octet-stream）。
func cleanMediaType(v string) (string, error) {
	if strings.TrimSpace(v) == "" {
		return "application/octet-stream", nil
	}
	mt, params, err := mime.ParseMediaType(v)
	if err == nil {
		v = mime.FormatMediaType(mt, params)
	}
	if err != nil || v == "" || len(v) > 255 {
		return "", errm(Invalid, "invalid_argument", i18n.M("service.err.attach.media_type", "value", v))
	}
	return v, nil
}

// Attach はイシューにファイルを添付する。上限（1 ファイル・1 プロジェクト）はこの呼び出しのたびに DB から読む。
// 上限を超えたら本体もメタデータも残さずに拒む。
func (s *Service) Attach(ctx context.Context, a Actor, in AttachInput) (*store.Attachment, error) {
	u, err := s.actorUser(ctx, a)
	if err != nil {
		return nil, err
	}
	p, row, err := s.attachIssue(ctx, u, in.Issue, in.Project, true)
	if err != nil {
		return nil, err
	}
	name, err := cleanFilename(in.Filename)
	if err != nil {
		return nil, err
	}
	mt, err := cleanMediaType(in.MediaType)
	if err != nil {
		return nil, err
	}
	if s.AttachDir == "" {
		return nil, errAttachUnavailable()
	}
	limits, err := store.ReadAttachLimits(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	if in.Declared > limits.MaxFile {
		return nil, errTooLarge(limits.MaxFile)
	}
	tmp, sum, size, err := writeAttachTemp(s.AttachDir, in.Body, limits.MaxFile)
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp) // rename した後は無い（消すものが無いだけ）

	s.attachMu.Lock()
	defer s.attachMu.Unlock()
	now := s.Now()
	out := &store.Attachment{ProjectID: p.ID, IssueID: row.ID, SHA256: sum, Size: size, Filename: name, MediaType: mt,
		CreatedAt: now, AuthorUserID: a.UserID, TokenID: a.TokenID, Via: a.Via, ProjectSlug: p.Slug, IssueDisplayID: row.DisplayID}
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		// プロジェクトの行をロックして、同じプロジェクトへの添付を 1 つずつにする（上限の判定と記録の間に合計が変わらないように）
		var pid int64
		if err := tx.QueryRowContext(ctx, "SELECT id FROM projects WHERE id = ? FOR UPDATE", p.ID).Scan(&pid); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errm(NotFound, "not_found", i18n.M("service.err.not_found.project", "slug", p.Slug))
			}
			return err
		}
		if err := checkProjectActive(ctx, tx, p); err != nil {
			return err
		}
		used, err := store.ProjectAttachmentBytes(ctx, tx, p.ID)
		if err != nil {
			return err
		}
		if used+size > limits.MaxProject {
			return errm(Rejected, "attachment_quota", i18n.M("service.err.attach.project_quota",
				"slug", p.Slug, "limit", limits.MaxProject,
				"used", i18n.MN("service.err.attach.bytes", int(used), "n", used),
				"size", i18n.MN("service.err.attach.bytes", int(size), "n", size)))
		}
		if err := placeAttachBody(s.AttachDir, tmp, sum); err != nil {
			return err
		}
		id, err := store.InsertAttachment(ctx, tx, *out)
		if err != nil {
			return err
		}
		out.ID = id
		return store.InsertEvent(ctx, tx, store.Event{ProjectID: p.ID, IssueID: row.ID, Kind: "attach", SessionID: a.SessionID,
			Author: s.author(a, now), Detail: map[string]any{"attachment_id": id, "sha256": sum, "size": size, "filename": name, "media_type": mt}})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// writeAttachTemp は本体を置き場の一時ファイルに書き、fsync して閉じる。max を超えたら・途中で失敗したら一時ファイルを消して失敗を返す。
func writeAttachTemp(dir string, r io.Reader, max int64) (path, sum string, size int64, err error) {
	if r == nil {
		r = strings.NewReader("")
	}
	tmpDir := filepath.Join(dir, attachTmpDir)
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return "", "", 0, err
	}
	f, err := os.CreateTemp(tmpDir, "upload-*")
	if err != nil {
		return "", "", 0, err
	}
	path = f.Name()
	fail := func(e error) (string, string, int64, error) {
		f.Close()
		os.Remove(path)
		return "", "", 0, e
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(r, max+1))
	if err != nil {
		return fail(err)
	}
	if n > max {
		return fail(errTooLarge(max))
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", "", 0, err
	}
	return path, hex.EncodeToString(h.Sum(nil)), n, nil
}

// placeAttachBody は一時ファイルを本体の置き場へ rename する。本体が既にあれば上書きせず、一時ファイルを捨てて
// 本体の更新時刻だけを進める（整合の検査が、記録の直前の本体を「どこからも指されない」として消さないように。OrphanGrace）。
// 何度呼んでも結果は同じ（トランザクションの再試行で 2 回目に呼ばれても、本体があれば何もしない）。
func placeAttachBody(dir, tmp, sum string) error {
	final := AttachmentBodyPath(dir, sum)
	if _, err := os.Lstat(final); err == nil {
		os.Remove(tmp)
		now := time.Now()
		return os.Chtimes(final, now, now)
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		return err
	}
	syncDir(filepath.Dir(final))
	return nil
}

// syncDir は rename をディスクに残すため、ディレクトリを fsync する（できない OS・ファイルシステムでは何もしない）。
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
}

// Attachments はイシューの添付を古い順に返す（消去済みも含め、Purged で見分ける）。閲覧できる人なら誰でも読める。
// メタデータだけなので、置き場が設定されていなくても返す。
func (s *Service) Attachments(ctx context.Context, a Actor, issue, slug string) ([]store.Attachment, error) {
	u, err := s.actorUser(ctx, a)
	if err != nil {
		return nil, err
	}
	_, row, err := s.attachIssue(ctx, u, issue, slug, false)
	if err != nil {
		return nil, err
	}
	return store.IssueAttachments(ctx, s.DB, row.ID)
}

// OpenAttachment は添付の本体を開く（呼ぶ側が閉じる）。消去済みなら「消去済み」、本体が置き場に無ければ「本体の欠け」を返す。
func (s *Service) OpenAttachment(ctx context.Context, a Actor, id int64) (store.Attachment, *os.File, error) {
	u, err := s.actorUser(ctx, a)
	if err != nil {
		return store.Attachment{}, nil, err
	}
	at, err := s.readableAttachment(ctx, u, id)
	if err != nil {
		return store.Attachment{}, nil, err
	}
	if at.Purged {
		return at, nil, errm(Rejected, "attachment_purged", i18n.M("service.err.attach.purged", "id", at.ID, "filename", at.Filename))
	}
	if s.AttachDir == "" {
		return at, nil, errAttachUnavailable()
	}
	f, err := os.Open(AttachmentBodyPath(s.AttachDir, at.SHA256))
	if errors.Is(err, fs.ErrNotExist) {
		return at, nil, errm(NotFound, "attachment_body_missing", i18n.M("service.err.attach.body_missing", "id", at.ID))
	}
	if err != nil {
		return at, nil, err
	}
	return at, f, nil
}

// PurgeResult は消去の結果。
type PurgeResult struct {
	SHA256        string
	AttachmentIDs []int64 // この消去で消去済みになった添付（既に消去済みなら空）
	Changed       bool    // 記録を足したか（既に消去済みなら false）
}

// PurgeAttachment は添付の本体を消す（管理者だけ）。同じ本体を指す、まだ消去していない添付はすべて消去済みになる。
// 記録（attachment_purges と、関わったイシューごとの issue_events の attach_purge）を入れてから本体を消す。
// 本体を消せなかったときは記録を残したまま失敗を返す（その本体は「どこからも指されない」ので、整合の検査の --apply で消せる）。
// 既に消去済みでも、本体が残っていれば消す。
func (s *Service) PurgeAttachment(ctx context.Context, a Actor, id int64, reason string) (*PurgeResult, error) {
	u, err := s.actorUser(ctx, a)
	if err != nil {
		return nil, err
	}
	if u.Role != "admin" {
		return nil, errm(Forbidden, "forbidden", i18n.M("service.err.attach.purge_admin"))
	}
	if s.AttachDir == "" {
		return nil, errAttachUnavailable()
	}
	at, err := store.AttachmentByID(ctx, s.DB, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errm(NotFound, "not_found", i18n.M("service.err.attach.not_found", "id", id))
	}
	if err != nil {
		return nil, err
	}
	reason = strings.TrimSpace(reason)
	s.attachMu.Lock()
	defer s.attachMu.Unlock()
	now := s.Now()
	res := &PurgeResult{SHA256: at.SHA256}
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		res.AttachmentIDs, res.Changed = nil, false
		targets, err := store.AttachmentsToPurge(ctx, tx, at.SHA256)
		if err != nil || len(targets) == 0 {
			return err
		}
		through := targets[len(targets)-1].ID
		if err := store.InsertAttachmentPurge(ctx, tx, store.AttachmentPurge{SHA256: at.SHA256, AttachmentID: at.ID, ThroughAttachmentID: through,
			ActorUserID: a.UserID, Via: a.Via, Reason: reason, At: now}); err != nil {
			return err
		}
		byIssue := map[int64][]int64{}
		var order []store.Attachment
		for _, t := range targets {
			res.AttachmentIDs = append(res.AttachmentIDs, t.ID)
			if _, ok := byIssue[t.IssueID]; !ok {
				order = append(order, t)
			}
			byIssue[t.IssueID] = append(byIssue[t.IssueID], t.ID)
		}
		for _, t := range order {
			if err := store.InsertEvent(ctx, tx, store.Event{ProjectID: t.ProjectID, IssueID: t.IssueID, Kind: "attach_purge", SessionID: a.SessionID,
				Author: s.author(a, now), Detail: map[string]any{"sha256": at.SHA256, "attachment_ids": byIssue[t.IssueID], "requested": at.ID, "reason": reason}}); err != nil {
				return err
			}
		}
		res.Changed = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := os.Remove(AttachmentBodyPath(s.AttachDir, at.SHA256)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return res, i18n.Wrapf(err, "service.err.attach.purge_remove", "sha256", at.SHA256)
	}
	return res, nil
}

// OrphanBody はどこからも（消去していない添付から）指されない本体。
type OrphanBody struct {
	Path    string
	SHA256  string
	ModTime time.Time
	Recent  bool // OrphanGrace より新しい（--apply でも消さない）
}

// AttachmentCheck は DB と置き場の突き合わせの結果。
type AttachmentCheck struct {
	Missing []store.Attachment // 消去していないのに本体が置き場に無い添付
	Orphans []OrphanBody       // どこからも指されない本体
}

// CheckAttachments は DB と置き場を突き合わせる（何も変えない）。本体の欠けと、どこからも指されない本体を返す。
// 置き場の中で本体の形（<2 字>/<2 字>/<sha256>）をしていないもの（一時ファイルなど）は見ない。
func (s *Service) CheckAttachments(ctx context.Context) (*AttachmentCheck, error) {
	if s.AttachDir == "" {
		return nil, errAttachUnavailable()
	}
	live, err := store.LiveAttachments(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	res := &AttachmentCheck{}
	referenced := map[string]bool{}
	exists := map[string]bool{}
	for _, at := range live {
		if !referenced[at.SHA256] {
			referenced[at.SHA256] = true
			_, err := os.Stat(AttachmentBodyPath(s.AttachDir, at.SHA256))
			exists[at.SHA256] = err == nil
		}
		if !exists[at.SHA256] {
			res.Missing = append(res.Missing, at)
		}
	}
	now := time.Now()
	// WalkDir は根がシンボリックリンクだとその中へ降りない（黙って 0 件になる）ので、根だけは先に解いてから辿る。
	// 報告するパスは設定した置き場の下の形に戻す（途中のリンクは os.Remove も辿る）。
	root := s.AttachDir
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && path == root { // 置き場がまだ無い（1 度も添付していない）
				return filepath.SkipDir
			}
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			if rel == attachTmpDir {
				return filepath.SkipDir
			}
			return nil
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) != 3 || !sha256Re.MatchString(parts[2]) || parts[0] != parts[2][:2] || parts[1] != parts[2][2:4] || !d.Type().IsRegular() {
			return nil
		}
		if referenced[parts[2]] {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		res.Orphans = append(res.Orphans, OrphanBody{Path: filepath.Join(s.AttachDir, rel), SHA256: parts[2], ModTime: info.ModTime(), Recent: now.Sub(info.ModTime()) < OrphanGrace})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// RemoveOrphanBodies は CheckAttachments が見つけた、どこからも指されない本体を消す（Recent のものは消さない）。
// 消す直前に、その本体を指す消去していない添付が無いことを DB で確かめ直す（検査の後に同じ内容が添付されたときに消さないため）。
// 本体の欠けは直さない（直せるものが無い）。消したパスを返す。
func (s *Service) RemoveOrphanBodies(ctx context.Context, c *AttachmentCheck) ([]string, error) {
	s.attachMu.Lock()
	defer s.attachMu.Unlock()
	var removed []string
	for _, o := range c.Orphans {
		if o.Recent {
			continue
		}
		n, err := store.LiveAttachmentCount(ctx, s.DB, o.SHA256)
		if err != nil {
			return removed, err
		}
		if n > 0 {
			continue
		}
		if info, err := os.Stat(o.Path); err != nil || time.Since(info.ModTime()) < OrphanGrace {
			continue // 消えた・検査の後に触れられた（同じ内容が添付されようとしている）
		}
		if err := os.Remove(o.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return removed, err
		}
		removed = append(removed, o.Path)
	}
	return removed, nil
}
