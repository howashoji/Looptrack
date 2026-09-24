package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/mdformat"
)

// Queryer は *sql.DB と *sql.Tx の共通部分。
type Queryer = execQuerier

// DB と Tx の共通部分。
type execQuerier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// 既知のスカラー項目と issues の列の対応。
var scalarColumns = map[string]string{
	"id": "display_id", "title": "title", "type": "type", "status": "status",
	"priority": "priority", "parent": "parent", "created": "created", "updated": "updated",
}

var listFields = map[string]bool{"labels": true, "blocked_by": true, "traces": true, "refs": true}

// Project はプロジェクトの設定（旧 config.json と counter）。
type Project struct {
	ID          int64
	Slug        string
	Prefix      string
	Width       int
	Name        string
	Description string
	SortOrder   int
	Counter     int
	Rules       json.RawMessage // プロジェクト別ルール（NULL は nil）
}

// StoredIssue は DB から復元した 1 イシュー。
type StoredIssue struct {
	ID        int64
	ProjectID int64
	Number    int
	FileName  string // 元のファイル名（バイト列そのまま）
	Version   int
	Doc       *mdformat.Document
	Assignee  Assignee // 担当者（サーバだけの項目。Doc の frontmatter には入らない）
}

// Assignee はイシューの担当者（DESIGN.md §5-1）。UserID が 0 なら未設定。
type Assignee struct {
	UserID int64
	Login  string
	// Inactive は、担当が今そのプロジェクトで変更できない（無効化・参加の解除・viewer への変更）こと。
	// 利用者の役割が admin でも同じ（参加していないプロジェクトは閲覧のみのため）。一覧に印を出す（自動では外さない）
	Inactive bool
}

// assigneeColumns は issues（別名なし）の行から担当者を引く列（loadWhere と IssueAssignee が使う）。
const assigneeColumns = `COALESCE(issues.assignee_user_id, 0),
  COALESCE((SELECT u.login FROM users u WHERE u.id = issues.assignee_user_id), ''),
  COALESCE((SELECT NOT (u.disabled_at IS NULL AND EXISTS (SELECT 1 FROM project_members m
      WHERE m.project_id = issues.project_id AND m.user_id = u.id AND m.role IN ('editor', 'admin')))
    FROM users u WHERE u.id = issues.assignee_user_id), FALSE)`

// IssueAssignee は 1 イシューの担当者を返す（トランザクション内では行ロックの後に使う）。
func IssueAssignee(ctx context.Context, q execQuerier, issueID int64) (Assignee, error) {
	var a Assignee
	err := q.QueryRowContext(ctx, "SELECT "+assigneeColumns+" FROM issues WHERE id = ?", issueID).Scan(&a.UserID, &a.Login, &a.Inactive)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

// SetAssignee は担当者を設定する（userID が 0 なら解除）。版番号・updated は呼び出し側（SaveDocument）で進める。
func SetAssignee(ctx context.Context, q execQuerier, issueID, userID int64) error {
	_, err := q.ExecContext(ctx, "UPDATE issues SET assignee_user_id = ? WHERE id = ?", nullID(userID), issueID)
	return err
}

// Assignable はプロジェクトで担当にできる利用者（無効化されていない参加者で、役割が editor / admin）。
// 利用者の役割が admin でも viewer で参加していれば含めない（書けないため）。
type Assignable struct {
	UserID int64
	Login  string
	Name   string
}

// AssignableMembers はプロジェクトで担当にできる利用者を login 順に返す。
func AssignableMembers(ctx context.Context, q execQuerier, projectID int64) ([]Assignable, error) {
	rows, err := q.QueryContext(ctx, `SELECT u.id, u.login, u.display_name FROM project_members m JOIN users u ON u.id = m.user_id
WHERE m.project_id = ? AND u.disabled_at IS NULL AND m.role IN ('editor', 'admin') ORDER BY u.login`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Assignable
	for rows.Next() {
		var a Assignable
		if err := rows.Scan(&a.UserID, &a.Login, &a.Name); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// decomposed は Document を DB の行に分けたもの。
type decomposed struct {
	keysJSON string
	cols     map[string]string
	values   []fieldValue
	extras   []extraValue
}

type fieldValue struct {
	field string
	pos   int
	value string
}

type extraValue struct {
	key, value string
	isList     bool
}

// decompose は保存規則（DESIGN.md §3）で Document を分ける: front_keys に出現順のキーを持ち、既知のスカラーは列、
// 既知のリストは issue_values、それ以外（未知のキーや、既知のキーでも型が想定と違う値）は issue_extra に入れる。
func decompose(doc *mdformat.Document) (decomposed, error) {
	d := decomposed{cols: map[string]string{}}
	keys := make([]string, 0, len(doc.Front))
	seen := map[string]bool{}
	for _, f := range doc.Front {
		if seen[f.Key] {
			return d, i18n.Errorf("store.err.document.duplicate_key", "key", fmt.Sprintf("%q", f.Key))
		}
		seen[f.Key] = true
		keys = append(keys, f.Key)
		switch col, isScalar := scalarColumns[f.Key]; {
		case isScalar && !f.IsList:
			d.cols[col] = f.Value
		case listFields[f.Key] && f.IsList:
			for i, v := range f.List {
				d.values = append(d.values, fieldValue{f.Key, i, v})
			}
		default:
			v := f.Value
			if f.IsList {
				b, _ := json.Marshal(f.List)
				v = string(b)
			}
			d.extras = append(d.extras, extraValue{f.Key, v, f.IsList})
		}
	}
	b, _ := json.Marshal(keys)
	d.keysJSON = string(b)
	if d.cols["display_id"] == "" {
		return d, i18n.Errorf("store.err.document.no_id")
	}
	return d, nil
}

func (d decomposed) insertChildren(ctx context.Context, q execQuerier, issueID int64) error {
	for _, v := range d.values {
		if _, err := q.ExecContext(ctx, "INSERT INTO issue_values (issue_id, field, pos, value) VALUES (?, ?, ?, ?)", issueID, v.field, v.pos, v.value); err != nil {
			return err
		}
	}
	for _, e := range d.extras {
		if _, err := q.ExecContext(ctx, "INSERT INTO issue_extra (issue_id, `key`, is_list, value) VALUES (?, ?, ?, ?)", issueID, e.key, e.isList, e.value); err != nil {
			return err
		}
	}
	return nil
}

// InsertDocument は Document を issues / issue_values / issue_extra / comments に保存し、issues.id を返す。
func InsertDocument(ctx context.Context, q execQuerier, projectID int64, number int, fileName string, doc *mdformat.Document, via string) (int64, error) {
	d, err := decompose(doc)
	if err != nil {
		return 0, err
	}
	cols := d.cols
	displayID := cols["display_id"]
	res, err := q.ExecContext(ctx, `INSERT INTO issues
  (project_id, number, display_id, file_name, front_keys, title, type, status, priority, parent, created, updated,
   body_main, gap_nl, has_comment_section, preamble, trail_nl)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		projectID, number, displayID, []byte(fileName), d.keysJSON, cols["title"], cols["type"], cols["status"],
		cols["priority"], cols["parent"], cols["created"], cols["updated"],
		doc.BodyMain, doc.GapNL, doc.HasCommentSection, doc.Preamble, doc.TrailNL)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", displayID, err)
	}
	issueID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := d.insertChildren(ctx, q, issueID); err != nil {
		return 0, fmt.Errorf("%s: %w", displayID, err)
	}
	for i, c := range doc.Comments {
		if _, err := q.ExecContext(ctx, "INSERT INTO comments (issue_id, seq, ts, content, via) VALUES (?, ?, ?, ?, ?)", issueID, i+1, c.TS, c.Content, via); err != nil {
			return 0, fmt.Errorf("%s: %w", displayID, err)
		}
	}
	return issueID, nil
}

// ErrConflict は楽観ロックの版が一致しないことを表す。
var ErrConflict = i18n.Errorf("store.err.document.conflict")

// Author は追記するコメント・イベントの記録者。
type Author struct {
	UserID  int64
	TokenID int64
	Via     string // cli / web / mcp / api
	At      time.Time
	Agent   string // MCP の接続してきた AI（空なら記録しない）。issue_events.detail の "agent" に残す
	// SessionKind は session_id の種類（空なら記録しない）。issue_events.detail の "session_kind" に残す。
	// 今は "host"（器のセッション ID。会話記録と結び付かないので未付与の検知から外す）だけ。
	SessionKind string
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// SaveDocument は既存イシューを doc の内容に書き直し、新しい版番号を返す。
// display_id とファイル名は変えない。コメントは doc.Comments[existing:] だけを追記する
// （comments は追記専用。既存のコメントは書き換えない）。version が一致しなければ ErrConflict。
func SaveDocument(ctx context.Context, q execQuerier, issueID int64, version int, doc *mdformat.Document, existing int, a Author) (int, error) {
	d, err := decompose(doc)
	if err != nil {
		return 0, err
	}
	if existing > len(doc.Comments) {
		return 0, i18n.Errorf("store.err.document.comments_decrease", "count", existing)
	}
	cols := d.cols
	res, err := q.ExecContext(ctx, `UPDATE issues SET front_keys = ?, title = ?, type = ?, status = ?, priority = ?, parent = ?,
  created = ?, updated = ?, body_main = ?, gap_nl = ?, has_comment_section = ?, preamble = ?, trail_nl = ?, version = version + 1
WHERE id = ? AND version = ? AND display_id = ?`,
		d.keysJSON, cols["title"], cols["type"], cols["status"], cols["priority"], cols["parent"], cols["created"], cols["updated"],
		doc.BodyMain, doc.GapNL, doc.HasCommentSection, doc.Preamble, doc.TrailNL, issueID, version, cols["display_id"])
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrConflict
	}
	for _, stmt := range []string{"DELETE FROM issue_values WHERE issue_id = ?", "DELETE FROM issue_extra WHERE issue_id = ?"} {
		if _, err := q.ExecContext(ctx, stmt, issueID); err != nil {
			return 0, err
		}
	}
	if err := d.insertChildren(ctx, q, issueID); err != nil {
		return 0, err
	}
	for i := existing; i < len(doc.Comments); i++ {
		c := doc.Comments[i]
		if _, err := q.ExecContext(ctx, `INSERT INTO comments (issue_id, seq, ts, content, created_at, author_user_id, token_id, via)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, issueID, i+1, c.TS, c.Content, a.At, nullID(a.UserID), nullID(a.TokenID), a.Via); err != nil {
			return 0, err
		}
	}
	return version + 1, nil
}

// IssueRow は ID から引いたイシューの識別情報。
type IssueRow struct {
	ID        int64
	ProjectID int64
	DisplayID string
	Version   int
}

// FindIssue は display_id（大文字小文字を区別しない。以前の CLI と同じ）でイシューを引く。
// projectID が 0 でなければそのプロジェクトに限る。forUpdate なら行をロックする（トランザクション内で使う）。
func FindIssue(ctx context.Context, q execQuerier, displayID string, projectID int64, forUpdate bool) (IssueRow, error) {
	query := "SELECT id, project_id, display_id, version FROM issues WHERE UPPER(display_id) = ?"
	args := []any{strings.ToUpper(displayID)}
	if projectID != 0 {
		query += " AND project_id = ?"
		args = append(args, projectID)
	}
	query += " ORDER BY id LIMIT 1"
	if forUpdate {
		query += " FOR UPDATE"
	}
	var r IssueRow
	err := q.QueryRowContext(ctx, query, args...).Scan(&r.ID, &r.ProjectID, &r.DisplayID, &r.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// NextNumber はプロジェクトの counter を 1 進めて返す（トランザクション内で使う。projects 行をロックする）。
func NextNumber(ctx context.Context, q execQuerier, projectID int64) (int, error) {
	var n int
	if err := q.QueryRowContext(ctx, "SELECT counter FROM projects WHERE id = ? FOR UPDATE", projectID).Scan(&n); err != nil {
		return 0, err
	}
	n++
	if _, err := q.ExecContext(ctx, "UPDATE projects SET counter = ? WHERE id = ?", n, projectID); err != nil {
		return 0, err
	}
	return n, nil
}

// Event は issue_events の 1 行。
type Event struct {
	ProjectID int64
	IssueID   int64
	Kind      string
	Author    Author
	SessionID string
	Detail    any
}

// InsertEvent は変更を監査ログに記録する。
func InsertEvent(ctx context.Context, q execQuerier, e Event) error {
	var detail any
	if e.Author.Agent != "" || e.Author.SessionKind != "" {
		// 接続してきた AI を detail の "agent"、セッション ID の種類を "session_kind" に残す
		// （列を足さずに済ませる。未付与の検知が Copilot の操作・器のセッション ID の操作を外すのに使う）
		m := map[string]any{}
		if e.Detail != nil {
			b, err := json.Marshal(e.Detail)
			if err != nil {
				return err
			}
			if err := json.Unmarshal(b, &m); err != nil || m == nil {
				m = map[string]any{"detail": e.Detail}
			}
		}
		if e.Author.Agent != "" {
			m["agent"] = e.Author.Agent
		}
		if e.Author.SessionKind != "" {
			m["session_kind"] = e.Author.SessionKind
		}
		e.Detail = m
	}
	if e.Detail != nil {
		b, err := json.Marshal(e.Detail)
		if err != nil {
			return err
		}
		detail = string(b)
	}
	var session any
	if e.SessionID != "" {
		session = e.SessionID
	}
	_, err := q.ExecContext(ctx, `INSERT INTO issue_events (project_id, issue_id, at, kind, via, actor_user_id, token_id, session_id, detail)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, e.ProjectID, nullID(e.IssueID), e.Author.At, e.Kind, e.Author.Via,
		nullID(e.Author.UserID), nullID(e.Author.TokenID), session, detail)
	return err
}

// Activity は 1 イシューの最終更新イベント。
type Activity struct {
	DisplayID string
	ProjectID int64
	Title     string
	Status    string
	LastAt    time.Time
	LastKind  string
	LastVia   string
	Since     int // since より後のイベント数
}

// IssueActivity は display_id ごとの最終イベント（取り込みを除く。無ければ取り込み）を返す。見つからない ID は含めない。
func IssueActivity(ctx context.Context, q execQuerier, displayIDs []string, since time.Time) ([]Activity, error) {
	if len(displayIDs) == 0 {
		return nil, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(displayIDs)), ", ")
	args := []any{since}
	for _, id := range displayIDs {
		args = append(args, strings.ToUpper(id))
	}
	rows, err := q.QueryContext(ctx, `SELECT i.display_id, i.project_id, i.title, i.status, e.at, e.kind, e.via,
  (SELECT COUNT(*) FROM issue_events s WHERE s.issue_id = i.id AND s.at > ?)
FROM issues i
JOIN issue_events e ON e.id = (SELECT MAX(x.id) FROM issue_events x WHERE x.issue_id = i.id)
WHERE UPPER(i.display_id) IN (`+marks+`) ORDER BY i.display_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Activity
	for rows.Next() {
		var a Activity
		if err := rows.Scan(&a.DisplayID, &a.ProjectID, &a.Title, &a.Status, &a.LastAt, &a.LastKind, &a.LastVia, &a.Since); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// LoadDocuments はプロジェクトの全イシューを Document に復元する（display_id 順）。
func LoadDocuments(ctx context.Context, q execQuerier, projectID int64) ([]StoredIssue, error) {
	return loadWhere(ctx, q, "project_id = ?", projectID, true)
}

// ImportedIssueIDs はプロジェクトのイシューのうち旧ファイルモードから取り込んだもの（issue_events に kind='import' がある）の
// issues.id を返す（file_name が実在のファイル名なのはこれだけ。サーバで起票したものは名前を合成しただけ）。
func ImportedIssueIDs(ctx context.Context, q execQuerier, projectID int64) (map[int64]bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT i.id FROM issues i WHERE i.project_id = ?
  AND EXISTS (SELECT 1 FROM issue_events e WHERE e.issue_id = i.id AND e.kind = 'import')`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// LoadFronts はプロジェクトの全イシューの frontmatter だけを復元する（本文・コメントは空。一覧・集計用）。
func LoadFronts(ctx context.Context, q execQuerier, projectID int64) ([]StoredIssue, error) {
	return loadWhere(ctx, q, "project_id = ?", projectID, false)
}

// LoadDocument は 1 イシューを Document に復元する。
func LoadDocument(ctx context.Context, q execQuerier, issueID int64) (StoredIssue, error) {
	out, err := loadWhere(ctx, q, "id = ?", issueID, true)
	if err != nil {
		return StoredIssue{}, err
	}
	if len(out) == 0 {
		return StoredIssue{}, ErrNotFound
	}
	return out[0], nil
}

// loadWhere は issues の条件 cond（列名は issues のもの・プレースホルダ 1 つ）に合うイシューを復元する。
// full でなければ本文・コメントを読まない。
func loadWhere(ctx context.Context, q execQuerier, cond string, arg any, full bool) ([]StoredIssue, error) {
	body := "'', gap_nl, has_comment_section, NULL, trail_nl"
	if full {
		body = "body_main, gap_nl, has_comment_section, preamble, trail_nl"
	}
	rows, err := q.QueryContext(ctx, `SELECT id, project_id, number, file_name, version, front_keys, display_id, title, type, status, priority,
  parent, created, updated, `+body+`, `+assigneeColumns+`
FROM issues WHERE `+cond+` ORDER BY display_id`, arg)
	if err != nil {
		return nil, err
	}
	var out []StoredIssue
	cols := map[int64]map[string]string{}
	keysOf := map[int64][]string{}
	index := map[int64]int{}
	for rows.Next() {
		var (
			s                             StoredIssue
			fileName                      []byte
			keysJSON                      string
			c                             = map[string]string{}
			id, t, ty, st, pr, pa, cr, up string
			preamble                      sql.NullString
			doc                           mdformat.Document
		)
		if err := rows.Scan(&s.ID, &s.ProjectID, &s.Number, &fileName, &s.Version, &keysJSON, &id, &t, &ty, &st, &pr, &pa, &cr, &up,
			&doc.BodyMain, &doc.GapNL, &doc.HasCommentSection, &preamble, &doc.TrailNL,
			&s.Assignee.UserID, &s.Assignee.Login, &s.Assignee.Inactive); err != nil {
			rows.Close()
			return nil, err
		}
		c["display_id"], c["title"], c["type"], c["status"], c["priority"], c["parent"], c["created"], c["updated"] = id, t, ty, st, pr, pa, cr, up
		if preamble.Valid {
			p := preamble.String
			doc.Preamble = &p
		}
		var keys []string
		if err := json.Unmarshal([]byte(keysJSON), &keys); err != nil {
			rows.Close()
			return nil, i18n.Wrapf(err, "store.err.document.front_keys", "id", id)
		}
		s.FileName = string(fileName)
		s.Doc = &doc
		cols[s.ID], keysOf[s.ID], index[s.ID] = c, keys, len(out)
		out = append(out, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	join := func(table, columns, order string) string {
		return "SELECT " + columns + " FROM " + table + " JOIN issues i ON i.id = t.issue_id WHERE i." + cond + order
	}

	values := map[int64]map[string][]string{}
	rows, err = q.QueryContext(ctx, join("issue_values t", "t.issue_id, t.field, t.value", " ORDER BY t.issue_id, t.field, t.pos"), arg)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var field, v string
		if err := rows.Scan(&id, &field, &v); err != nil {
			rows.Close()
			return nil, err
		}
		if values[id] == nil {
			values[id] = map[string][]string{}
		}
		values[id][field] = append(values[id][field], v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	type extraVal struct {
		value  string
		isList bool
	}
	extras := map[int64]map[string]extraVal{}
	rows, err = q.QueryContext(ctx, join("issue_extra t", "t.issue_id, t.`key`, t.is_list, t.value", ""), arg)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var ev extraVal
		var key string
		if err := rows.Scan(&id, &key, &ev.isList, &ev.value); err != nil {
			rows.Close()
			return nil, err
		}
		if extras[id] == nil {
			extras[id] = map[string]extraVal{}
		}
		extras[id][key] = ev
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if full {
		rows, err = q.QueryContext(ctx, join("comments t", "t.issue_id, t.ts, t.content", " ORDER BY t.issue_id, t.seq"), arg)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var cm mdformat.Comment
			if err := rows.Scan(&id, &cm.TS, &cm.Content); err != nil {
				rows.Close()
				return nil, err
			}
			d := out[index[id]].Doc
			d.Comments = append(d.Comments, cm)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	for _, s := range out {
		for _, key := range keysOf[s.ID] {
			if ev, ok := extras[s.ID][key]; ok {
				f := mdformat.Field{Key: key, IsList: ev.isList, Value: ev.value}
				if ev.isList {
					f.Value = ""
					if err := json.Unmarshal([]byte(ev.value), &f.List); err != nil {
						return nil, i18n.Wrapf(err, "store.err.document.extra_value", "id", cols[s.ID]["display_id"], "key", key)
					}
				}
				s.Doc.Front = append(s.Doc.Front, f)
				continue
			}
			if listFields[key] {
				l := values[s.ID][key]
				if l == nil {
					l = []string{}
				}
				s.Doc.Front = append(s.Doc.Front, mdformat.Field{Key: key, IsList: true, List: l})
				continue
			}
			col, ok := scalarColumns[key]
			if !ok {
				return nil, i18n.Errorf("store.err.document.extra_missing", "id", cols[s.ID]["display_id"], "key", fmt.Sprintf("%q", key))
			}
			s.Doc.Front = append(s.Doc.Front, mdformat.Field{Key: key, Value: cols[s.ID][col]})
		}
	}
	return out, nil
}

// UpsertProject は slug でプロジェクトを作成または更新する（prefix / width は作成後に変えられない）。
func UpsertProject(ctx context.Context, q execQuerier, p Project) (int64, error) {
	var id int64
	var prefix string
	var width int
	err := q.QueryRowContext(ctx, "SELECT id, prefix, width FROM projects WHERE slug = ?", p.Slug).Scan(&id, &prefix, &width)
	switch {
	case err == sql.ErrNoRows:
		res, err := q.ExecContext(ctx, `INSERT INTO projects (slug, prefix, width, name, description, sort_order, counter)
VALUES (?, ?, ?, ?, ?, ?, ?)`, p.Slug, p.Prefix, p.Width, p.Name, p.Description, p.SortOrder, p.Counter)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	case err != nil:
		return 0, err
	}
	if prefix != p.Prefix || width != p.Width {
		return 0, i18n.Errorf("store.err.project.immutable", "slug", p.Slug, "db_prefix", prefix, "db_width", width, "prefix", p.Prefix, "width", p.Width)
	}
	_, err = q.ExecContext(ctx, "UPDATE projects SET name = ?, description = ?, sort_order = ?, counter = ? WHERE id = ?",
		p.Name, p.Description, p.SortOrder, p.Counter, id)
	return id, err
}

var (
	projectSlugRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	projectPrefixRe = regexp.MustCompile(`^[A-Z][A-Z0-9]*(-[A-Z0-9]+)*$`)
)

// ValidateNewProject は新しく作るプロジェクトの slug・prefix・width・表示名を検査する。
// 返すエラーは i18n.Error（表示する側が i18n.Text で利用者の言語に訳す）。
func ValidateNewProject(p Project) error {
	switch {
	case !projectSlugRe.MatchString(p.Slug):
		return i18n.Errorf("store.err.project.slug", "slug", fmt.Sprintf("%q", p.Slug))
	case len(p.Prefix) > 24 || !projectPrefixRe.MatchString(p.Prefix):
		return i18n.Errorf("store.err.project.prefix", "prefix", fmt.Sprintf("%q", p.Prefix))
	case p.Width < 1 || p.Width > 9:
		return i18n.Errorf("store.err.project.width", "width", p.Width)
	case strings.TrimSpace(p.Name) == "" || len([]rune(p.Name)) > 255:
		return i18n.Errorf("store.err.project.name")
	case len([]rune(p.Description)) > 1024:
		return i18n.Errorf("store.err.project.description")
	}
	return nil
}

// ErrProjectExists は slug か prefix がすでに使われていることを表す。
var ErrProjectExists = i18n.Errorf("store.err.project.exists")

// CreateProject は空のプロジェクト（採番 0）を作る。既存の slug・prefix とは重複させない。
func CreateProject(ctx context.Context, q execQuerier, p Project) (int64, error) {
	if err := ValidateNewProject(p); err != nil {
		return 0, err
	}
	var n int
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM projects WHERE slug = ? OR prefix = ?", p.Slug, p.Prefix).Scan(&n); err != nil {
		return 0, err
	}
	if n > 0 {
		return 0, ErrProjectExists
	}
	res, err := q.ExecContext(ctx, "INSERT INTO projects (slug, prefix, width, name, description, sort_order, counter) VALUES (?, ?, ?, ?, ?, ?, 0)",
		p.Slug, p.Prefix, p.Width, p.Name, p.Description, p.SortOrder)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

const projectColumns = "id, slug, prefix, width, name, description, sort_order, counter, rules"

func scanProject(row interface{ Scan(...any) error }) (Project, error) {
	var p Project
	var rules []byte
	err := row.Scan(&p.ID, &p.Slug, &p.Prefix, &p.Width, &p.Name, &p.Description, &p.SortOrder, &p.Counter, &rules)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if rules != nil {
		p.Rules = json.RawMessage(rules)
	}
	return p, err
}

// ListProjects は全プロジェクトを並び順（sort_order, slug）で返す。
func ListProjects(ctx context.Context, q execQuerier) ([]Project, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+projectColumns+" FROM projects ORDER BY sort_order, slug")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ParseNumber は display_id から番号を取り出す（prefix-NNNN）。
func ParseNumber(prefix, displayID string) (int, error) {
	rest, ok := strings.CutPrefix(displayID, prefix+"-")
	if !ok || rest == "" {
		return 0, i18n.Errorf("store.err.document.id_prefix", "id", fmt.Sprintf("%q", displayID), "prefix", fmt.Sprintf("%q", prefix))
	}
	n := 0
	for _, c := range rest {
		if c < '0' || c > '9' {
			return 0, i18n.Errorf("store.err.document.id_number", "id", fmt.Sprintf("%q", displayID))
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

// LoadDocumentForUpdate は 1 イシューの行をロックして Document に復元する（トランザクション内で使う）。
func LoadDocumentForUpdate(ctx context.Context, q execQuerier, issueID int64) (StoredIssue, error) {
	var id int64
	if err := q.QueryRowContext(ctx, "SELECT id FROM issues WHERE id = ? FOR UPDATE", issueID).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return StoredIssue{}, ErrNotFound
		}
		return StoredIssue{}, err
	}
	return LoadDocument(ctx, q, issueID)
}

// OpenBugs はプロジェクトの未クローズの bug の ID を返す（exclude の issues.id は除く）。
func OpenBugs(ctx context.Context, q execQuerier, projectID, exclude int64) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT display_id FROM issues WHERE project_id = ? AND id <> ? AND type = 'bug'
  AND status NOT IN ('Done', 'Canceled') ORDER BY display_id`, projectID, exclude)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SetRules はプロジェクトのルール（検査済みの JSON。nil で解除）を保存する。
func SetRules(ctx context.Context, q execQuerier, projectID int64, rules []byte) error {
	var v any
	if rules != nil {
		v = string(rules)
	}
	_, err := q.ExecContext(ctx, "UPDATE projects SET rules = ? WHERE id = ?", v, projectID)
	return err
}

// ProjectGuide はプロジェクトの運用文書（guide が返す）。
type ProjectGuide struct {
	Content   string
	Source    string
	UpdatedAt time.Time
}

// GetProjectGuide は運用文書を返す。未登録なら ErrNotFound。
func GetProjectGuide(ctx context.Context, q execQuerier, projectID int64) (ProjectGuide, error) {
	var g ProjectGuide
	err := q.QueryRowContext(ctx, "SELECT content, source, updated_at FROM project_guides WHERE project_id = ?", projectID).
		Scan(&g.Content, &g.Source, &g.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return g, ErrNotFound
	}
	return g, err
}

// SetProjectGuide は運用文書を登録・置き換える。content が空なら登録を消す。
func SetProjectGuide(ctx context.Context, q execQuerier, projectID int64, content, source string) error {
	if content == "" {
		_, err := q.ExecContext(ctx, "DELETE FROM project_guides WHERE project_id = ?", projectID)
		return err
	}
	_, err := q.ExecContext(ctx, `INSERT INTO project_guides (project_id, content, source) VALUES (?, ?, ?)
ON DUPLICATE KEY UPDATE content = VALUES(content), source = VALUES(source), updated_at = CURRENT_TIMESTAMP(6)`, projectID, content, source)
	return err
}

// Starter は In Progress のイシューを最後に In Progress にした操作（next の「自分が着手中」の判定）。
type Starter struct {
	DisplayID string
	UserID    int64
	SessionID string
	At        time.Time
}

// InProgressStarters は、プロジェクトの In Progress のイシューごとに、最後に In Progress にしたイベント
// （状態変更の to、または起票時の status）の主体を返す。取り込んだだけで該当イベントが無いイシューは含めない。
func InProgressStarters(ctx context.Context, q execQuerier, projectID int64) ([]Starter, error) {
	rows, err := q.QueryContext(ctx, `SELECT i.display_id, COALESCE(e.actor_user_id, 0), COALESCE(e.session_id, ''), e.at
FROM issues i
JOIN issue_events e ON e.id = (
  SELECT MAX(x.id) FROM issue_events x WHERE x.issue_id = i.id AND (
    (x.kind = 'status' AND JSON_UNQUOTE(JSON_EXTRACT(x.detail, '$.to')) = 'In Progress') OR
    (x.kind = 'create' AND JSON_UNQUOTE(JSON_EXTRACT(x.detail, '$.status')) = 'In Progress')))
WHERE i.project_id = ? AND i.status = 'In Progress' ORDER BY i.display_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Starter
	for rows.Next() {
		var s Starter
		if err := rows.Scan(&s.DisplayID, &s.UserID, &s.SessionID, &s.At); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
