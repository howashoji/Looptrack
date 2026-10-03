package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
)

// 添付のメタデータ（attachments・attachment_purges。マイグレーション 0007）。どちらも追記専用で、UPDATE・DELETE をしない。
// 本体のファイルの置き場と書き込みは service（attachments.go）が持つ。

// Attachment は添付 1 件。Purged と表示用の ProjectSlug・IssueDisplayID は読み出しのときだけ埋まる。
type Attachment struct {
	ID             int64
	ProjectID      int64
	IssueID        int64
	SHA256         string
	Size           int64
	Filename       string
	MediaType      string
	CreatedAt      time.Time
	AuthorUserID   int64
	TokenID        int64
	Via            string
	Purged         bool
	ProjectSlug    string
	IssueDisplayID string
}

// purgedExpr は添付 a が消去済みかを表す式。消去はその時点でこの本体を指していた添付（id が through_attachment_id 以下）にだけ効く。
const purgedExpr = `EXISTS (SELECT 1 FROM attachment_purges p WHERE p.sha256 = a.sha256 AND p.through_attachment_id >= a.id)`

const attachmentColumns = `a.id, a.project_id, a.issue_id, a.sha256, a.size, a.filename, a.media_type, a.created_at,
  COALESCE(a.author_user_id, 0), COALESCE(a.token_id, 0), a.via, ` + purgedExpr + `, pr.slug, i.display_id`

const attachmentFrom = ` FROM attachments a JOIN projects pr ON pr.id = a.project_id JOIN issues i ON i.id = a.issue_id`

func scanAttachment(row interface{ Scan(...any) error }) (Attachment, error) {
	var a Attachment
	err := row.Scan(&a.ID, &a.ProjectID, &a.IssueID, &a.SHA256, &a.Size, &a.Filename, &a.MediaType, &a.CreatedAt,
		&a.AuthorUserID, &a.TokenID, &a.Via, &a.Purged, &a.ProjectSlug, &a.IssueDisplayID)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

func scanAttachments(rows *sql.Rows, err error) ([]Attachment, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Attachment
	for rows.Next() {
		a, err := scanAttachment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// InsertAttachment は添付のメタデータを 1 行足し、その id を返す。CreatedAt が零なら DB の現在時刻にする。
func InsertAttachment(ctx context.Context, q execQuerier, a Attachment) (int64, error) {
	var at any
	if !a.CreatedAt.IsZero() {
		at = a.CreatedAt
	}
	res, err := q.ExecContext(ctx, `INSERT INTO attachments (project_id, issue_id, sha256, size, filename, media_type, created_at, author_user_id, token_id, via)
VALUES (?, ?, ?, ?, ?, ?, COALESCE(?, CURRENT_TIMESTAMP(6)), ?, ?, ?)`,
		a.ProjectID, a.IssueID, a.SHA256, a.Size, a.Filename, a.MediaType, at, nullID(a.AuthorUserID), nullID(a.TokenID), a.Via)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// AttachmentByID は添付を 1 件返す（無ければ ErrNotFound）。
func AttachmentByID(ctx context.Context, q execQuerier, id int64) (Attachment, error) {
	return scanAttachment(q.QueryRowContext(ctx, "SELECT "+attachmentColumns+attachmentFrom+" WHERE a.id = ?", id))
}

// IssueAttachments はイシューの添付を古い順に返す（消去済みも含む）。
func IssueAttachments(ctx context.Context, q execQuerier, issueID int64) ([]Attachment, error) {
	return scanAttachments(q.QueryContext(ctx, "SELECT "+attachmentColumns+attachmentFrom+" WHERE a.issue_id = ? ORDER BY a.id", issueID))
}

// ProjectAttachments はプロジェクトの添付を id の順に返す（消去済みも含む。transfer の書き出しが使う）。
func ProjectAttachments(ctx context.Context, q execQuerier, projectID int64) ([]Attachment, error) {
	return scanAttachments(q.QueryContext(ctx, "SELECT "+attachmentColumns+attachmentFrom+" WHERE a.project_id = ? ORDER BY a.id", projectID))
}

// LiveAttachments は消去していない添付を全プロジェクトについて返す（整合の検査用。sha256・id の順）。
func LiveAttachments(ctx context.Context, q execQuerier) ([]Attachment, error) {
	return scanAttachments(q.QueryContext(ctx, "SELECT "+attachmentColumns+attachmentFrom+" WHERE NOT "+purgedExpr+" ORDER BY a.sha256, a.id"))
}

// LiveAttachmentCount は本体 sha256 を指す、消去していない添付の数を返す。
func LiveAttachmentCount(ctx context.Context, q execQuerier, sha256 string) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM attachments a WHERE a.sha256 = ? AND NOT "+purgedExpr, sha256).Scan(&n)
	return n, err
}

// ProjectAttachmentBytes はプロジェクトの、消去していない添付の size の合計を返す（プロジェクトの上限の判定に使う）。
func ProjectAttachmentBytes(ctx context.Context, q execQuerier, projectID int64) (int64, error) {
	var n int64
	err := q.QueryRowContext(ctx, "SELECT COALESCE(SUM(a.size), 0) FROM attachments a WHERE a.project_id = ? AND NOT "+purgedExpr, projectID).Scan(&n)
	return n, err
}

// AttachmentPurge は消去の記録 1 件（attachment_purges）。
type AttachmentPurge struct {
	SHA256              string
	AttachmentID        int64 // 消去を指示した添付
	ThroughAttachmentID int64 // この消去が効く添付の id の上限
	ActorUserID         int64
	Via                 string
	Reason              string
	At                  time.Time // 零なら DB の現在時刻
}

// AttachmentsToPurge は本体 sha256 を指し、まだ消去していない添付を id の順に返す（消去の対象。トランザクションの中で使う）。
func AttachmentsToPurge(ctx context.Context, q execQuerier, sha256 string) ([]Attachment, error) {
	return scanAttachments(q.QueryContext(ctx, "SELECT "+attachmentColumns+attachmentFrom+" WHERE a.sha256 = ? AND NOT "+purgedExpr+" ORDER BY a.id", sha256))
}

// InsertAttachmentPurge は消去の記録を 1 行足す（追記のみ）。
func InsertAttachmentPurge(ctx context.Context, q execQuerier, p AttachmentPurge) error {
	var at any
	if !p.At.IsZero() {
		at = p.At
	}
	_, err := q.ExecContext(ctx, `INSERT INTO attachment_purges (sha256, attachment_id, through_attachment_id, at, actor_user_id, via, reason)
VALUES (?, ?, ?, COALESCE(?, CURRENT_TIMESTAMP(6)), ?, ?, ?)`,
		p.SHA256, p.AttachmentID, p.ThroughAttachmentID, at, nullID(p.ActorUserID), p.Via, truncate(p.Reason, 255))
	return err
}

// 添付の上限（system_settings）。全プロジェクト共通の 1 値で、値はバイト数の 10 進。
const (
	SettingAttachMaxFile    = "attach_max_file_bytes"    // 1 ファイルの上限
	SettingAttachMaxProject = "attach_max_project_bytes" // 1 プロジェクトの上限（消去していない添付の size の合計）
)

// 上限の既定（行が無い・値が壊れているとき）。
const (
	DefaultAttachMaxFile    int64 = 20 << 20 // 20MiB
	DefaultAttachMaxProject int64 = 1 << 30  // 1GiB
)

// AttachLimits は添付の上限。
type AttachLimits struct {
	MaxFile    int64
	MaxProject int64
}

// ValidAttachLimitName は添付の上限の設定名かを返す。
func ValidAttachLimitName(name string) bool {
	return name == SettingAttachMaxFile || name == SettingAttachMaxProject
}

// ReadAttachLimits は添付の上限を読む。行が無い・正の整数として読めない値は既定にする。
// 添付のたびに呼ぶ（設定を変えたら、再起動せずに次の添付から効く）。
func ReadAttachLimits(ctx context.Context, q execQuerier) (AttachLimits, error) {
	l := AttachLimits{MaxFile: DefaultAttachMaxFile, MaxProject: DefaultAttachMaxProject}
	for _, s := range []struct {
		name string
		v    *int64
	}{{SettingAttachMaxFile, &l.MaxFile}, {SettingAttachMaxProject, &l.MaxProject}} {
		raw, ok, err := GetSetting(ctx, q, s.name)
		if err != nil {
			return l, err
		}
		if !ok {
			continue
		}
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
			*s.v = n
		}
	}
	return l, nil
}

// SetAttachLimit は添付の上限を変え、変更を setting_changes に記録する（同じ値なら何もしない。SetTwoFactorPolicy と同じ形）。
// ch の OldValue / NewValue / Name は無視する（ここで決める）。old は変更前の値（空は未設定）。
func SetAttachLimit(ctx context.Context, db *sql.DB, name string, value int64, ch SettingChange) (old string, changed bool, err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback() //nolint:errcheck
	old, changed, err = SetAttachLimitTx(ctx, tx, name, value, ch)
	if err != nil || !changed {
		return old, false, err
	}
	if err := tx.Commit(); err != nil {
		return old, false, err
	}
	return old, true, nil
}

// SetAttachLimitTx は SetAttachLimit を呼び出し元のトランザクションの中で行う（コミットは呼び出し元）。
func SetAttachLimitTx(ctx context.Context, tx Queryer, name string, value int64, ch SettingChange) (old string, changed bool, err error) {
	if !ValidAttachLimitName(name) {
		return "", false, i18n.Errorf("store.err.attach_limit.name", "name", strconv.Quote(name))
	}
	if value <= 0 {
		return "", false, i18n.Errorf("store.err.attach_limit.invalid", "value", value)
	}
	v := strconv.FormatInt(value, 10)
	err = tx.QueryRowContext(ctx, "SELECT value FROM system_settings WHERE name = ? FOR UPDATE", name).Scan(&old)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}
	if old == v {
		return old, false, nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO system_settings (name, value) VALUES (?, ?)
ON DUPLICATE KEY UPDATE value = VALUES(value)`, name, v); err != nil {
		return old, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO setting_changes (name, old_value, new_value, actor_user_id, via, note, ip)
VALUES (?, ?, ?, ?, ?, ?, ?)`, name, old, v, ch.ActorUserID, ch.Via, truncate(ch.Note, 255), ch.IP); err != nil {
		return old, false, err
	}
	return old, true, nil
}

// AttachmentUsage はプロジェクト 1 つの添付の使用量。Count・Bytes は消去していない添付だけを数える（プロジェクトの上限と同じ数え方）。
type AttachmentUsage struct {
	Count  int64
	Bytes  int64
	Purged int64 // 消去済みの添付の数
}

// AttachmentUsageByProject は添付の使用量をプロジェクトの id ごとに返す（添付が 1 つも無いプロジェクトは入らない）。
func AttachmentUsageByProject(ctx context.Context, q execQuerier) (map[int64]AttachmentUsage, error) {
	rows, err := q.QueryContext(ctx, `SELECT a.project_id,
  COALESCE(SUM(CASE WHEN `+purgedExpr+` THEN 0 ELSE 1 END), 0),
  COALESCE(SUM(CASE WHEN `+purgedExpr+` THEN 0 ELSE a.size END), 0),
  COALESCE(SUM(CASE WHEN `+purgedExpr+` THEN 1 ELSE 0 END), 0)
FROM attachments a GROUP BY a.project_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]AttachmentUsage{}
	for rows.Next() {
		var id int64
		var u AttachmentUsage
		if err := rows.Scan(&id, &u.Count, &u.Bytes, &u.Purged); err != nil {
			return nil, err
		}
		out[id] = u
	}
	return out, rows.Err()
}

// AttachmentFilter は管理者の画面の添付の一覧の絞り込み。零値の項目は絞らない。
type AttachmentFilter struct {
	ProjectID int64
	IssueID   string // イシューの表示用の ID（大文字にそろえて比べる）
	Limit     int    // 0 以下なら 100
}

// ListAttachments は添付を新しい順に返す（消去済みも含む。管理者の画面の一覧）。
func ListAttachments(ctx context.Context, q execQuerier, f AttachmentFilter) ([]Attachment, error) {
	where, args := []string{}, []any{}
	if f.ProjectID != 0 {
		where, args = append(where, "a.project_id = ?"), append(args, f.ProjectID)
	}
	if f.IssueID != "" {
		where, args = append(where, "i.display_id = ?"), append(args, strings.ToUpper(f.IssueID))
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	query := "SELECT " + attachmentColumns + attachmentFrom
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	return scanAttachments(q.QueryContext(ctx, query+" ORDER BY a.id DESC LIMIT ?", append(args, limit)...))
}
