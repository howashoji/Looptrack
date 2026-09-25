package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
)

// ErrNotFound は対象が無いことを表す。
var ErrNotFound = i18n.Errorf("store.err.not_found")

// User はログインできる利用者。
type User struct {
	ID           int64
	Login        string
	DisplayName  string
	PasswordHash string
	TOTPSecret   []byte // 暗号化済み
	TOTPEnabled  bool
	TOTPLastStep int64
	TOTPPending  []byte // 暗号化済み
	Role         string // admin / member
	Disabled     bool
	LastLoginAt  sql.NullTime
	Lang         sql.NullString // 表示の言語（ja / en）。NULL は設定なし（要求の Accept-Language へ落ちる）
}

const userColumns = "id, login, display_name, password_hash, totp_secret, totp_enabled, totp_last_step, totp_pending, role, disabled_at IS NOT NULL, last_login_at, lang"

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Login, &u.DisplayName, &u.PasswordHash, &u.TOTPSecret, &u.TOTPEnabled, &u.TOTPLastStep, &u.TOTPPending, &u.Role, &u.Disabled, &u.LastLoginAt, &u.Lang)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// CreateUser は利用者を作る。
func CreateUser(ctx context.Context, q execQuerier, login, displayName, passwordHash, role string) (int64, error) {
	res, err := q.ExecContext(ctx, "INSERT INTO users (login, display_name, password_hash, role) VALUES (?, ?, ?, ?)", login, displayName, passwordHash, role)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SetUserLang は利用者の表示の言語を保存する。
//
// 空文字は NULL（設定なし）に戻す。**空文字のままは保存しない**（設定なしの表し方を NULL の 1 つに
// 絞る。空文字が残ると「設定なし」が 2 通りになり、読む側の判定が経路ごとに食い違う）。
//
// ja / en 以外は**ここで拒む**。規則は共通のドメイン操作に 1 か所だけ置く（経路ごとのハンドラに書くと、
// 経路によって効いたり効かなかったりする）。MySQL には CHECK (lang IN ('ja','en')) があるが、
// **SQLite は ALTER TABLE ADD COLUMN に CHECK を付けられない**ので、SQLite ではここが最後の砦になる。
// 2 つ目の書き込み経路が増えても自動的に守られる。読む側は読めない値を「設定なし」として無視するので、
// 変な値が入っても**表示は壊れず、設定が黙って効かなくなるだけ**で、気づける形にならない。
//
// 呼び出し側（Web の /account/lang）にも i18n.Parse があるが、役割が違う。あちらは**利用者の入力を
// 正規形に直し、直せなければ利用者に分かるエラー（400）を返す**ためのもの。ここは**正規形そのもの
// （ja / en）だけを通す関所**で、DB の CHECK と同じ集合にそろえる。
func SetUserLang(ctx context.Context, q execQuerier, userID int64, lang string) error {
	var v any // nil のまま渡すと NULL になる
	if lang != "" {
		// i18n.Parse は ja-JP・jpn・前後の空白・大文字も受けて正規形に直すが、ここで通すのは
		// 正規形と一致するものだけにする（DB へ入る値を ja / en / NULL の 3 通りに閉じるため）。
		parsed, ok := i18n.Parse(lang)
		if !ok || string(parsed) != lang {
			return i18n.Errorf("store.err.user_lang.invalid", "value", strconv.Quote(lang))
		}
		v = lang
	}
	_, err := q.ExecContext(ctx, "UPDATE users SET lang = ? WHERE id = ?", v, userID)
	return err
}

// UserByLogin はログイン名で利用者を引く。
func UserByLogin(ctx context.Context, q execQuerier, login string) (User, error) {
	return scanUser(q.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE login = ?", login))
}

// UserByID は ID で利用者を引く。
func UserByID(ctx context.Context, q execQuerier, id int64) (User, error) {
	return scanUser(q.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE id = ?", id))
}

// ListUsers は全利用者を返す。
func ListUsers(ctx context.Context, q execQuerier) ([]User, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+userColumns+" FROM users ORDER BY login")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func affected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetPassword はパスワードを変え、その利用者のセッションをすべて破棄する。
func SetPassword(ctx context.Context, q execQuerier, userID int64, hash string) error {
	if err := affected(q.ExecContext(ctx, "UPDATE users SET password_hash = ? WHERE id = ?", hash, userID)); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, "DELETE FROM web_sessions WHERE user_id = ?", userID)
	return err
}

// SetRole は利用者の役割（admin / member）を変える。
func SetRole(ctx context.Context, q execQuerier, userID int64, role string) error {
	_, err := q.ExecContext(ctx, "UPDATE users SET role = ? WHERE id = ?", role, userID)
	return err
}

// CountActiveAdmins は無効化されていない管理者の数を返す。
func CountActiveAdmins(ctx context.Context, q execQuerier) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE role = 'admin' AND disabled_at IS NULL").Scan(&n)
	return n, err
}

// SetDisabled は利用者を無効化（または再有効化）する。無効化時はセッションを破棄する。
func SetDisabled(ctx context.Context, q execQuerier, userID int64, disabled bool) error {
	if disabled {
		if err := affected(q.ExecContext(ctx, "UPDATE users SET disabled_at = CURRENT_TIMESTAMP(6) WHERE id = ?", userID)); err != nil {
			return err
		}
		_, err := q.ExecContext(ctx, "DELETE FROM web_sessions WHERE user_id = ?", userID)
		return err
	}
	return affected(q.ExecContext(ctx, "UPDATE users SET disabled_at = NULL WHERE id = ?", userID))
}

// ResetTOTP は TOTP の登録を消す（次回ログインで再登録）。セッションも破棄する。
func ResetTOTP(ctx context.Context, q execQuerier, userID int64) error {
	if err := affected(q.ExecContext(ctx, "UPDATE users SET totp_secret = NULL, totp_enabled = FALSE, totp_last_step = 0, totp_pending = NULL WHERE id = ?", userID)); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, "DELETE FROM web_sessions WHERE user_id = ?", userID)
	return err
}

// DisableTOTP は本人が TOTP の登録を解除する（二段階認証が任意のときのアカウント設定）。
// 登録途中のシークレットも消す。セッションは呼び出し側で作り直す。
func DisableTOTP(ctx context.Context, q execQuerier, userID int64) error {
	return affected(q.ExecContext(ctx, "UPDATE users SET totp_secret = NULL, totp_enabled = FALSE, totp_last_step = 0, totp_pending = NULL WHERE id = ? AND totp_enabled = TRUE", userID))
}

// DeleteUserSessions は利用者のセッションをすべて破棄する。
func DeleteUserSessions(ctx context.Context, q execQuerier, userID int64) error {
	_, err := q.ExecContext(ctx, "DELETE FROM web_sessions WHERE user_id = ?", userID)
	return err
}

// SetTOTPPending は登録途中のシークレット（暗号化済み）を保存する。
func SetTOTPPending(ctx context.Context, q execQuerier, userID int64, sealed []byte) error {
	return affected(q.ExecContext(ctx, "UPDATE users SET totp_pending = ? WHERE id = ? AND totp_enabled = FALSE", sealed, userID))
}

// EnableTOTP は登録途中のシークレットを有効にする。
func EnableTOTP(ctx context.Context, q execQuerier, userID int64, step int64) error {
	return affected(q.ExecContext(ctx, `UPDATE users SET totp_secret = totp_pending, totp_pending = NULL, totp_enabled = TRUE, totp_last_step = ?
WHERE id = ? AND totp_enabled = FALSE AND totp_pending IS NOT NULL`, step, userID))
}

// ConsumeTOTPStep は使用済みステップを進める。同時に同じコードが使われた場合は片方だけが成功する。
func ConsumeTOTPStep(ctx context.Context, q execQuerier, userID, step int64) (bool, error) {
	res, err := q.ExecContext(ctx, "UPDATE users SET totp_last_step = ? WHERE id = ? AND totp_last_step < ?", step, userID, step)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// TouchLogin は最終ログイン時刻を記録する。
func TouchLogin(ctx context.Context, q execQuerier, userID int64) error {
	_, err := q.ExecContext(ctx, "UPDATE users SET last_login_at = CURRENT_TIMESTAMP(6) WHERE id = ?", userID)
	return err
}

// RecordLoginAttempt はログインの試行を記録する。stage は password / totp（失敗）と login（ログイン完了）。
func RecordLoginAttempt(ctx context.Context, q execQuerier, login, ip, stage string, success bool) error {
	_, err := q.ExecContext(ctx, "INSERT INTO login_attempts (login, ip, stage, success) VALUES (?, ?, ?, ?)", login, ip, stage, success)
	return err
}

// RecentFailures は since 以降の失敗回数（ログイン名単位は最後の成功以降に限る）と IP 単位の失敗回数を返す。
func RecentFailures(ctx context.Context, q execQuerier, login, ip string, since time.Time) (byLogin, byIP int, err error) {
	err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM login_attempts WHERE login = ? AND success = FALSE AND at >= ?
  AND at > COALESCE((SELECT MAX(at) FROM login_attempts s WHERE s.login = ? AND s.success = TRUE AND s.stage = 'login'), '1970-01-01')`,
		login, since, login).Scan(&byLogin)
	if err != nil {
		return
	}
	err = q.QueryRowContext(ctx, "SELECT COUNT(*) FROM login_attempts WHERE ip = ? AND success = FALSE AND at >= ?", ip, since).Scan(&byIP)
	return
}

// Session は Web のログインセッション。
type Session struct {
	IDHash    []byte
	UserID    int64
	CSRFToken string
	MFAPassed bool // ログインの段階をすべて終えた（画面・API に使える）
	// TOTPVerified は TOTP を入力して発行したセッションか（マイグレーション 0010）。二段階認証が任意のとき、
	// 未登録の利用者はパスワードだけで MFAPassed になる（TOTPVerified は false）。必須に切り替えるとそのセッションは使えない。
	TOTPVerified bool
	CreatedAt    time.Time
	ExpiresAt    time.Time
	LastSeenAt   time.Time
}

// CreateSession はセッションを保存する。
func CreateSession(ctx context.Context, q execQuerier, s Session, ip, userAgent string) error {
	if len(userAgent) > 255 {
		userAgent = userAgent[:255]
	}
	_, err := q.ExecContext(ctx, `INSERT INTO web_sessions (id_hash, user_id, csrf_token, mfa_passed, totp_verified, expires_at, ip, user_agent)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, s.IDHash, s.UserID, s.CSRFToken, s.MFAPassed, s.TOTPVerified, s.ExpiresAt, ip, userAgent)
	return err
}

// SessionByHash は有効期限内のセッションを引く。
func SessionByHash(ctx context.Context, q execQuerier, hash []byte, now time.Time) (Session, error) {
	var s Session
	err := q.QueryRowContext(ctx, `SELECT id_hash, user_id, csrf_token, mfa_passed, totp_verified, created_at, expires_at, last_seen_at
FROM web_sessions WHERE id_hash = ? AND expires_at > ?`, hash, now).Scan(&s.IDHash, &s.UserID, &s.CSRFToken, &s.MFAPassed, &s.TOTPVerified, &s.CreatedAt, &s.ExpiresAt, &s.LastSeenAt)
	if errors.Is(err, sql.ErrNoRows) {
		return s, ErrNotFound
	}
	return s, err
}

// TouchSession は最終アクセス時刻を更新する。
func TouchSession(ctx context.Context, q execQuerier, hash []byte, now time.Time) error {
	_, err := q.ExecContext(ctx, "UPDATE web_sessions SET last_seen_at = ? WHERE id_hash = ?", now, hash)
	return err
}

// DeleteSession はセッションを破棄する。
func DeleteSession(ctx context.Context, q execQuerier, hash []byte) error {
	_, err := q.ExecContext(ctx, "DELETE FROM web_sessions WHERE id_hash = ?", hash)
	return err
}

// Token はアクセストークン。
type Token struct {
	ID         int64
	UserID     int64
	Kind       string
	Name       string
	Prefix     string
	CreatedAt  time.Time
	ExpiresAt  sql.NullTime
	LastUsedAt sql.NullTime
	RevokedAt  sql.NullTime
}

// CreateToken はトークンを保存する（平文は保存しない）。
// created_at は DB の既定値（CURRENT_TIMESTAMP）に委ねず、呼び出し側の時計（now）を書く。
// 委ねると発行日時だけがアプリの時計を通らず、同じ行の expires_at・last_used_at と食い違う。
func CreateToken(ctx context.Context, q execQuerier, userID int64, kind, name, prefix string, hash []byte, expiresAt *time.Time, now time.Time) (int64, error) {
	res, err := q.ExecContext(ctx, "INSERT INTO api_tokens (user_id, kind, name, token_prefix, token_hash, expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		userID, kind, name, prefix, hash, expiresAt, now.UTC())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

const tokenColumns = "id, user_id, kind, name, token_prefix, created_at, expires_at, last_used_at, revoked_at"

func scanToken(row interface{ Scan(...any) error }) (Token, error) {
	var t Token
	err := row.Scan(&t.ID, &t.UserID, &t.Kind, &t.Name, &t.Prefix, &t.CreatedAt, &t.ExpiresAt, &t.LastUsedAt, &t.RevokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

// TokenByHash はトークンを引く（失効・期限の判定は呼び出し側）。
func TokenByHash(ctx context.Context, q execQuerier, hash []byte) (Token, error) {
	return scanToken(q.QueryRowContext(ctx, "SELECT "+tokenColumns+" FROM api_tokens WHERE token_hash = ?", hash))
}

// ListTokens は利用者のトークンを返す。
func ListTokens(ctx context.Context, q execQuerier, userID int64) ([]Token, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+tokenColumns+" FROM api_tokens WHERE user_id = ? ORDER BY id", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Token
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TouchToken は最終利用時刻を記録する。
func TouchToken(ctx context.Context, q execQuerier, id int64, now time.Time) error {
	_, err := q.ExecContext(ctx, "UPDATE api_tokens SET last_used_at = ? WHERE id = ?", now, id)
	return err
}

// RevokeToken はトークンを失効させる。
func RevokeToken(ctx context.Context, q execQuerier, id int64) error {
	return affected(q.ExecContext(ctx, "UPDATE api_tokens SET revoked_at = CURRENT_TIMESTAMP(6) WHERE id = ? AND revoked_at IS NULL", id))
}

// RevokeUserToken は利用者本人のトークンだけを失効させる（他人の ID や失効済みは ErrNotFound）。
func RevokeUserToken(ctx context.Context, q execQuerier, userID, id int64) error {
	return affected(q.ExecContext(ctx, "UPDATE api_tokens SET revoked_at = CURRENT_TIMESTAMP(6) WHERE id = ? AND user_id = ? AND revoked_at IS NULL", id, userID))
}

// ProjectBySlug は slug でプロジェクトを引く。
func ProjectBySlug(ctx context.Context, q execQuerier, slug string) (Project, error) {
	return scanProject(q.QueryRowContext(ctx, "SELECT "+projectColumns+" FROM projects WHERE slug = ?", slug))
}

// ProjectByID は ID でプロジェクトを引く。
func ProjectByID(ctx context.Context, q execQuerier, id int64) (Project, error) {
	return scanProject(q.QueryRowContext(ctx, "SELECT "+projectColumns+" FROM projects WHERE id = ?", id))
}

// SetMember はプロジェクトの権限を付ける（既にあれば役割を更新）。
func SetMember(ctx context.Context, q execQuerier, projectID, userID int64, role string) error {
	_, err := q.ExecContext(ctx, "INSERT INTO project_members (project_id, user_id, role) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE role = VALUES(role)", projectID, userID, role)
	return err
}

// RemoveMember はプロジェクトの権限を外す。
func RemoveMember(ctx context.Context, q execQuerier, projectID, userID int64) error {
	return affected(q.ExecContext(ctx, "DELETE FROM project_members WHERE project_id = ? AND user_id = ?", projectID, userID))
}

// Member はプロジェクトの権限。
type Member struct {
	Login string
	Role  string
}

// ListMembers はプロジェクトの権限一覧。
func ListMembers(ctx context.Context, q execQuerier, projectID int64) ([]Member, error) {
	rows, err := q.QueryContext(ctx, "SELECT u.login, m.role FROM project_members m JOIN users u ON u.id = m.user_id WHERE m.project_id = ? ORDER BY u.login", projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.Login, &m.Role); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Membership は利用者から見たプロジェクトの権限。
type Membership struct {
	Project Project
	Role    string
}

// UserMemberships は利用者の project_members の行をプロジェクトの並び順で返す。
func UserMemberships(ctx context.Context, q execQuerier, userID int64) ([]Membership, error) {
	all, err := ListProjects(ctx, q)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, "SELECT project_id, role FROM project_members WHERE user_id = ?", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	roles := map[int64]string{}
	for rows.Next() {
		var id int64
		var role string
		if err := rows.Scan(&id, &role); err != nil {
			return nil, err
		}
		roles[id] = role
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []Membership
	for _, p := range all {
		if p.Archived { // アーカイブ済みは一覧にも操作の解決にも出さない（管理画面は ListProjects で全件を見る）
			continue
		}
		if role, ok := roles[p.ID]; ok {
			out = append(out, Membership{Project: p, Role: role})
		}
	}
	return out, nil
}

// NonMemberAdminRole は、管理者が参加していない（project_members に行が無い）プロジェクトでの役割。
const NonMemberAdminRole = "viewer"

// AccessibleProjects は slug・ID を指定した操作の解決用。利用者が扱えるプロジェクトと役割を返す。
// 画面・REST・MCP・CLI（LOOPTRACK_PROJECT）の権限の判定はすべてここの役割による（resolveProject・resolveIssue 経由）。
// admin の利用者は参加の有無にかかわらず全件を返すが（読める）、役割は project_members に行があればその値
// （viewer / editor / admin）、行が無いプロジェクトは viewer（閲覧のみ）とする。
// 以前は常に admin、その次は行が無いプロジェクトだけ admin だったが、それでは viewer で参加している
// 管理者が自分の参加を外すだけで書けてしまうため、書くには必ず参加させる（/im/admin/projects で自分の役割を変える）。
// /im/admin/* の管理操作はこの役割を見ない（利用者の役割 admin だけで判定）。一覧の表示には MemberProjects を使う。
// アーカイブ済みのプロジェクトは返さない（利用者の役割にかかわらず。UserMemberships も同じ）。そのため
// slug を指定した閲覧・書き込みも「存在しない」になる。書き込みは service の入口でも拒む（ProjectArchived）。
func AccessibleProjects(ctx context.Context, q execQuerier, u User) ([]Project, map[int64]string, error) {
	if u.Role == "admin" {
		all, err := ListProjects(ctx, q)
		if err != nil {
			return nil, nil, err
		}
		_, memberRoles, err := MemberProjects(ctx, q, u.ID)
		if err != nil {
			return nil, nil, err
		}
		roles := map[int64]string{}
		active := make([]Project, 0, len(all))
		for _, p := range all {
			if p.Archived { // 管理者にもアーカイブ済みは解決させない（戻すのは /admin/projects から）
				continue
			}
			active = append(active, p)
			roles[p.ID] = NonMemberAdminRole
			if r, ok := memberRoles[p.ID]; ok {
				roles[p.ID] = r
			}
		}
		return active, roles, nil
	}
	return MemberProjects(ctx, q, u.ID)
}

// MemberProjects は一覧用。利用者が参加している（project_members に行がある）プロジェクトを並び順で返し、
// 役割は project_members の値をそのまま返す（admin の利用者も同じ）。
func MemberProjects(ctx context.Context, q execQuerier, userID int64) ([]Project, map[int64]string, error) {
	ms, err := UserMemberships(ctx, q, userID)
	if err != nil {
		return nil, nil, err
	}
	out := make([]Project, 0, len(ms))
	roles := map[int64]string{}
	for _, m := range ms {
		out = append(out, m.Project)
		roles[m.Project.ID] = m.Role
	}
	return out, roles, nil
}

// MemberRole は利用者のプロジェクトでの project_members の役割を返す（参加していなければ ""）。
func MemberRole(ctx context.Context, q execQuerier, projectID, userID int64) (string, error) {
	var role string
	err := q.QueryRowContext(ctx, "SELECT role FROM project_members WHERE project_id = ? AND user_id = ?", projectID, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return role, err
}

// ProjectMember はプロジェクトの参加者 1 人分（利用者の状態つき）。
type ProjectMember struct {
	ProjectID int64
	Login     string
	Name      string
	Role      string
	Disabled  bool
}

// AllMembers は全プロジェクトの参加者をプロジェクト ID ごとに返す（管理画面用。ログイン名順）。
func AllMembers(ctx context.Context, q execQuerier) (map[int64][]ProjectMember, error) {
	rows, err := q.QueryContext(ctx, "SELECT m.project_id, u.login, u.display_name, m.role, u.disabled_at IS NOT NULL FROM project_members m JOIN users u ON u.id = m.user_id ORDER BY m.project_id, u.login")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]ProjectMember{}
	for rows.Next() {
		var m ProjectMember
		if err := rows.Scan(&m.ProjectID, &m.Login, &m.Name, &m.Role, &m.Disabled); err != nil {
			return nil, err
		}
		out[m.ProjectID] = append(out[m.ProjectID], m)
	}
	return out, rows.Err()
}

// OAuthClient は動的登録されたクライアント（公開クライアントのみ）。
type OAuthClient struct {
	ClientID     string
	Name         string
	RedirectURIs []string
	CreatedAt    time.Time
}

// CreateOAuthClient はクライアントを登録する。
func CreateOAuthClient(ctx context.Context, q execQuerier, c OAuthClient) error {
	uris, err := json.Marshal(c.RedirectURIs)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, "INSERT INTO oauth_clients (client_id, client_name, redirect_uris) VALUES (?, ?, ?)",
		c.ClientID, c.Name, string(uris))
	return err
}

// OAuthClientByID はクライアントを引く。
func OAuthClientByID(ctx context.Context, q execQuerier, id string) (OAuthClient, error) {
	var c OAuthClient
	var uris []byte
	err := q.QueryRowContext(ctx, "SELECT client_id, client_name, redirect_uris, created_at FROM oauth_clients WHERE client_id = ?", id).
		Scan(&c.ClientID, &c.Name, &uris, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(uris, &c.RedirectURIs)
}

// AuthCode は認可コード（平文は保存しない）。
type AuthCode struct {
	ClientID      string
	UserID        int64
	RedirectURI   string
	CodeChallenge string
	Scope         string
	Resource      string
	ExpiresAt     time.Time
}

// CreateAuthCode は認可コードを保存する。
func CreateAuthCode(ctx context.Context, q execQuerier, hash []byte, c AuthCode) error {
	_, err := q.ExecContext(ctx, `INSERT INTO oauth_codes (code_hash, client_id, user_id, redirect_uri, code_challenge, scope, resource, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, hash, c.ClientID, c.UserID, c.RedirectURI, c.CodeChallenge, c.Scope, c.Resource, c.ExpiresAt)
	return err
}

// UseAuthCode は認可コードを 1 回だけ引き換える（使用済み・期限切れは ErrNotFound）。
func UseAuthCode(ctx context.Context, q execQuerier, hash []byte, now time.Time) (AuthCode, error) {
	var c AuthCode
	res, err := q.ExecContext(ctx, "UPDATE oauth_codes SET used_at = ? WHERE code_hash = ? AND used_at IS NULL AND expires_at > ?", now, hash, now)
	if err != nil {
		return c, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return c, ErrNotFound
	}
	err = q.QueryRowContext(ctx, "SELECT client_id, user_id, redirect_uri, code_challenge, scope, resource, expires_at FROM oauth_codes WHERE code_hash = ?", hash).
		Scan(&c.ClientID, &c.UserID, &c.RedirectURI, &c.CodeChallenge, &c.Scope, &c.Resource, &c.ExpiresAt)
	return c, err
}

// CreateOAuthToken は OAuth のアクセストークンを保存する。created_at は CreateToken と同じく呼び出し側の時計を書く。
func CreateOAuthToken(ctx context.Context, q execQuerier, userID int64, clientID, name, prefix string, hash []byte, expiresAt, now time.Time) (int64, error) {
	res, err := q.ExecContext(ctx, `INSERT INTO api_tokens (user_id, kind, name, token_prefix, token_hash, expires_at, oauth_client_id, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, userID, "oauth", name, prefix, hash, expiresAt, clientID, now.UTC())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}
