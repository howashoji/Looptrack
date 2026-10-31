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

// 連絡（messages・message_reads。マイグレーション 0013。docs/server/DESIGN.md §9-9）。どちらも追記専用。
// 宛先の権限・本文の検査・保持期間の決定は service が持つ。ここは読み書きと、見える範囲の条件の SQL だけを置く。

// 連絡の宛先の種類（表の CHECK と同じ値）。
const (
	MessageToProject = "project"
	MessageToUser    = "user"
	MessageToSession = "session"
)

// 連絡の保持期間（system_settings）。
const (
	SettingMessageRetentionDays       = "message_retention_days" // この日数を過ぎた連絡を読みの経路に出さない（0 は無期限）
	DefaultMessageRetentionDays int64 = 30
)

// Message は連絡 1 件。
type Message struct {
	ID            int64
	ProjectID     int64
	ToKind        string
	ToUserID      int64 // project は 0
	ToLogin       string
	ToSession     string
	SenderUserID  int64
	SenderLogin   string
	SenderMachine string
	SenderSession string
	IssueID       int64 // 添えていなければ 0
	IssueDisplay  string
	Body          string
	TokenID       int64
	Via           string
	CreatedAt     time.Time
	// Read はこの読み手（利用者とセッション）が既読か
	Read bool
}

// MessageReader は連絡の読み手。
type MessageReader struct {
	UserID  int64
	Session string // 空はセッションの無い経路（Web・手で打つ CLI）
}

const messageSelect = `SELECT m.id, m.project_id, m.to_kind, COALESCE(m.to_user_id, 0), COALESCE(t.login, ''), m.to_session,
  m.sender_user_id, COALESCE(s.login, ''), m.sender_machine, m.sender_session, COALESCE(m.issue_id, 0), COALESCE(i.display_id, ''),
  m.body, COALESCE(m.token_id, 0), m.via, m.created_at,
  EXISTS (SELECT 1 FROM message_reads r WHERE r.message_id = m.id AND r.user_id = ? AND r.session_id = ?)
FROM messages m
LEFT JOIN users t ON t.id = m.to_user_id
LEFT JOIN users s ON s.id = m.sender_user_id
LEFT JOIN issues i ON i.id = m.issue_id`

func scanMessage(row interface{ Scan(...any) error }) (Message, error) {
	var m Message
	err := row.Scan(&m.ID, &m.ProjectID, &m.ToKind, &m.ToUserID, &m.ToLogin, &m.ToSession, &m.SenderUserID, &m.SenderLogin, &m.SenderMachine,
		&m.SenderSession, &m.IssueID, &m.IssueDisplay, &m.Body, &m.TokenID, &m.Via, &m.CreatedAt, &m.Read)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}

// InsertMessage は連絡を 1 行足し、その id を返す。
func InsertMessage(ctx context.Context, q execQuerier, m Message) (int64, error) {
	res, err := q.ExecContext(ctx, `INSERT INTO messages (project_id, to_kind, to_user_id, to_session, sender_user_id, sender_machine, sender_session,
  issue_id, body, token_id, via, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ProjectID, m.ToKind, nullID(m.ToUserID), m.ToSession, m.SenderUserID, m.SenderMachine, m.SenderSession,
		nullID(m.IssueID), m.Body, nullID(m.TokenID), m.Via, m.CreatedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// messageVisible は宛先として読める条件（プロジェクト宛てか、自分宛て）。since がゼロ値でなければ、それより古い連絡を外す。
func messageVisible(projectID int64, r MessageReader, since time.Time) (string, []any) {
	cond := "m.project_id = ? AND (m.to_kind = '" + MessageToProject + "' OR m.to_user_id = ?)"
	args := []any{projectID, r.UserID}
	if !since.IsZero() {
		cond += " AND m.created_at >= ?"
		args = append(args, since)
	}
	return cond, args
}

// messageUnread は読み手のセッションに届ける未読の条件。自分のセッションが送ったものと、同じ利用者の別のセッションに宛てたものは外す
// （セッションの無い読み手は、持ち主として別のセッション宛ても見る）。
func messageUnread(r MessageReader) (string, []any) {
	return `NOT (m.sender_user_id = ? AND m.sender_session = ?)
  AND (m.to_kind <> '` + MessageToSession + `' OR ? = '' OR m.to_session = ?)
  AND NOT EXISTS (SELECT 1 FROM message_reads x WHERE x.message_id = m.id AND x.user_id = ? AND x.session_id = ?)`,
		[]any{r.UserID, r.Session, r.Session, r.Session, r.UserID, r.Session}
}

// MessageByID は読み手が読める連絡 1 件を返す（宛先か送り手。読めないものは ErrNotFound）。
func MessageByID(ctx context.Context, q execQuerier, projectID, id int64, r MessageReader, since time.Time) (Message, error) {
	cond, args := messageVisible(projectID, r, since)
	all := append([]any{r.UserID, r.Session, id}, args...)
	all = append(all, projectID, r.UserID)
	return scanMessage(q.QueryRowContext(ctx, messageSelect+" WHERE m.id = ? AND (("+cond+") OR (m.project_id = ? AND m.sender_user_id = ?))", all...))
}

// ListInbox は読み手宛ての連絡を返す（新しい順）。unread なら読み手のセッションに届ける未読だけを、利用者とセッションに宛てたものを先にして返す。
func ListInbox(ctx context.Context, q execQuerier, projectID int64, r MessageReader, since time.Time, unread bool, limit int) ([]Message, error) {
	cond, args := messageVisible(projectID, r, since)
	order := " ORDER BY m.id DESC LIMIT ?"
	if unread {
		c, a := messageUnread(r)
		cond += " AND " + c
		args = append(args, a...)
		order = " ORDER BY CASE WHEN m.to_kind = '" + MessageToProject + "' THEN 1 ELSE 0 END, m.id DESC LIMIT ?"
	}
	all := append([]any{r.UserID, r.Session}, args...)
	return queryMessages(ctx, q, messageSelect+" WHERE "+cond+order, append(all, limit)...)
}

// ListSentMessages は利用者が送った連絡を新しい順に返す。
func ListSentMessages(ctx context.Context, q execQuerier, projectID int64, r MessageReader, since time.Time, limit int) ([]Message, error) {
	cond := "m.project_id = ? AND m.sender_user_id = ?"
	args := []any{r.UserID, r.Session, projectID, r.UserID}
	if !since.IsZero() {
		cond += " AND m.created_at >= ?"
		args = append(args, since)
	}
	return queryMessages(ctx, q, messageSelect+" WHERE "+cond+" ORDER BY m.id DESC LIMIT ?", append(args, limit)...)
}

func queryMessages(ctx context.Context, q execQuerier, query string, args ...any) ([]Message, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// UnreadMessageIDs は読み手のセッションに届ける未読の id を、上限つきで返す（並びは ListInbox の未読と同じ）。total は上限に関わらない件数。
func UnreadMessageIDs(ctx context.Context, q execQuerier, projectID int64, r MessageReader, since time.Time, limit int) (ids []int64, total int, err error) {
	cond, args := messageVisible(projectID, r, since)
	c, a := messageUnread(r)
	cond += " AND " + c
	args = append(args, a...)
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages m WHERE "+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := q.QueryContext(ctx, "SELECT m.id FROM messages m WHERE "+cond+
		" ORDER BY CASE WHEN m.to_kind = '"+MessageToProject+"' THEN 1 ELSE 0 END, m.id DESC LIMIT ?", append(args, limit)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	ids = []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, 0, err
		}
		ids = append(ids, id)
	}
	return ids, total, rows.Err()
}

// MarkMessagesRead は ids のうち読み手が読める連絡に既読の行を足し、足した件数を返す。同じ読み手の 2 度目は何も足さない。
func MarkMessagesRead(ctx context.Context, q execQuerier, projectID int64, r MessageReader, since time.Time, ids []int64, at time.Time) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	list := make([]string, len(ids))
	for i, id := range ids {
		list[i] = strconv.FormatInt(id, 10)
	}
	cond, args := messageVisible(projectID, r, since)
	rows, err := q.QueryContext(ctx, "SELECT m.id FROM messages m WHERE m.id IN ("+strings.Join(list, ",")+") AND "+cond, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var ok []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		ok = append(ok, id)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(ok) == 0 {
		return 0, nil
	}
	values := make([]string, len(ok))
	vals := make([]any, 0, len(ok)*4)
	for i, id := range ok {
		values[i] = "(?, ?, ?, ?)"
		vals = append(vals, id, r.UserID, r.Session, at)
	}
	res, err := q.ExecContext(ctx, "INSERT IGNORE INTO message_reads (message_id, user_id, session_id, read_at) VALUES "+strings.Join(values, ", "), vals...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ReadMessageRetentionDays は連絡の保持日数を読む。行が無い・整数として読めない・負の値は既定にする。要求ごとに呼ぶ。
func ReadMessageRetentionDays(ctx context.Context, q execQuerier) (int64, error) {
	raw, ok, err := GetSetting(ctx, q, SettingMessageRetentionDays)
	if err != nil || !ok {
		return DefaultMessageRetentionDays, err
	}
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil && validRetentionDays(n) {
		return n, nil
	}
	return DefaultMessageRetentionDays, nil
}

// SetMessageRetentionDays は連絡の保持日数を変え、変更を setting_changes に記録する（同じ値なら何もしない）。
func SetMessageRetentionDays(ctx context.Context, db *sql.DB, days int64, ch SettingChange) (old string, changed bool, err error) {
	if !validRetentionDays(days) {
		return "", false, i18n.Errorf("store.err.retention_days", "value", days, "max", MaxRetentionDays)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback() //nolint:errcheck
	old, changed, err = setSettingValueTx(ctx, tx, SettingMessageRetentionDays, strconv.FormatInt(days, 10), ch)
	if err != nil || !changed {
		return old, false, err
	}
	if err := tx.Commit(); err != nil {
		return old, false, err
	}
	return old, true, nil
}
