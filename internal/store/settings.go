package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
)

// システム全体の設定（system_settings・マイグレーション 0010）。

// SettingTwoFactor は二段階認証（TOTP）を全利用者に必須にするかの設定名。
const SettingTwoFactor = "two_factor"

// 二段階認証の設定値。
const (
	TwoFactorRequired = "required" // 必須: TOTP 未登録の利用者はログイン時に登録を求める
	TwoFactorOptional = "optional" // 任意: 登録済みの利用者だけ TOTP を求める。未登録ならパスワードだけでセッションを発行する
)

// ValidTwoFactor は二段階認証の設定値として正しいかを返す。
func ValidTwoFactor(v string) bool { return v == TwoFactorRequired || v == TwoFactorOptional }

// TwoFactorLabel は画面・コマンドに出す名前。表示する側が決めた言語を受け取る
// （この層は言語を知らないが、値と表示名の対応は 1 か所に置く）。
func TwoFactorLabel(lang i18n.Lang, v string) string {
	switch v {
	case TwoFactorRequired:
		return i18n.T(lang, "store.two_factor.required")
	case TwoFactorOptional:
		return i18n.T(lang, "store.two_factor.optional")
	case "":
		return i18n.T(lang, "store.two_factor.unset")
	}
	return v
}

// GetSetting は設定値を返す。行が無ければ ok=false。
func GetSetting(ctx context.Context, q execQuerier, name string) (value string, ok bool, err error) {
	err = q.QueryRowContext(ctx, "SELECT value FROM system_settings WHERE name = ?", name).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return value, err == nil, err
}

// TwoFactorPolicy は二段階認証の設定を返す。未設定（set=false）のときは必須として扱う（安全側）。
func TwoFactorPolicy(ctx context.Context, q execQuerier) (policy string, set bool, err error) {
	v, ok, err := GetSetting(ctx, q, SettingTwoFactor)
	if err != nil {
		return TwoFactorRequired, false, err
	}
	if !ok || !ValidTwoFactor(v) {
		return TwoFactorRequired, ok, nil
	}
	return v, true, nil
}

// SettingChange は設定の変更記録 1 件（setting_changes）。
type SettingChange struct {
	At          time.Time
	Name        string
	OldValue    string // 空は未設定
	NewValue    string
	ActorUserID sql.NullInt64
	ActorLogin  string // 表示用（読み出し時に users から引く）
	Via         string // web / command / migration
	Note        string
	IP          string
}

// TwoFactorResult は SetTwoFactorPolicy の結果。
type TwoFactorResult struct {
	Old             string // 変更前（空は未設定）
	Changed         bool
	RevokedSessions int64 // 必須にしたとき無効にしたセッションの数
}

// SetTwoFactorPolicy は二段階認証の設定を変え、変更を setting_changes に記録する（同じ値なら何もしない）。
// 必須にしたときは、TOTP を経ずに発行されたセッションと TOTP 未登録の利用者のセッションを破棄する
// （再ログインで登録・入力を求める）。TOTP のシークレットには触れない。
// ch の OldValue / NewValue / Name は無視する（ここで決める）。
func SetTwoFactorPolicy(ctx context.Context, db *sql.DB, value string, ch SettingChange) (TwoFactorResult, error) {
	if !ValidTwoFactor(value) {
		return TwoFactorResult{}, i18n.Errorf("store.err.two_factor.invalid", "required", TwoFactorRequired, "optional", TwoFactorOptional, "value", strconv.Quote(value))
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return TwoFactorResult{}, err
	}
	defer tx.Rollback() //nolint:errcheck
	res, err := SetTwoFactorPolicyTx(ctx, tx, value, ch)
	if err != nil || !res.Changed {
		return res, err
	}
	if err := tx.Commit(); err != nil {
		res.Changed = false
		return res, err
	}
	return res, nil
}

// SetTwoFactorPolicyTx は SetTwoFactorPolicy を呼び出し元のトランザクションの中で行う（コミットは呼び出し元。
// 初回設定で利用者・プロジェクトと 1 つのトランザクションにまとめるため）。Changed はコミットすれば変わることを表す。
func SetTwoFactorPolicyTx(ctx context.Context, tx Queryer, value string, ch SettingChange) (TwoFactorResult, error) {
	var res TwoFactorResult
	if !ValidTwoFactor(value) {
		return res, i18n.Errorf("store.err.two_factor.invalid", "required", TwoFactorRequired, "optional", TwoFactorOptional, "value", strconv.Quote(value))
	}
	var old string
	err := tx.QueryRowContext(ctx, "SELECT value FROM system_settings WHERE name = ? FOR UPDATE", SettingTwoFactor).Scan(&old)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return res, err
	}
	res.Old = old
	if old == value {
		return res, nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO system_settings (name, value) VALUES (?, ?)
ON DUPLICATE KEY UPDATE value = VALUES(value)`, SettingTwoFactor, value); err != nil {
		return res, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO setting_changes (name, old_value, new_value, actor_user_id, via, note, ip)
VALUES (?, ?, ?, ?, ?, ?, ?)`, SettingTwoFactor, old, value, ch.ActorUserID, ch.Via, truncate(ch.Note, 255), ch.IP); err != nil {
		return res, err
	}
	if value == TwoFactorRequired {
		r, err := tx.ExecContext(ctx, `DELETE FROM web_sessions
WHERE totp_verified = FALSE OR user_id IN (SELECT id FROM users WHERE totp_enabled = FALSE)`)
		if err != nil {
			return res, err
		}
		res.RevokedSessions, _ = r.RowsAffected()
	}
	res.Changed = true
	return res, nil
}

// SettingChanges は設定の変更記録を新しい順に返す（limit 件まで）。
func SettingChanges(ctx context.Context, q execQuerier, name string, limit int) ([]SettingChange, error) {
	rows, err := q.QueryContext(ctx, `SELECT c.at, c.name, c.old_value, c.new_value, c.actor_user_id, COALESCE(u.login, ''), c.via, c.note, c.ip
FROM setting_changes c LEFT JOIN users u ON u.id = c.actor_user_id
WHERE c.name = ? ORDER BY c.id DESC LIMIT ?`, name, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SettingChange
	for rows.Next() {
		var c SettingChange
		if err := rows.Scan(&c.At, &c.Name, &c.OldValue, &c.NewValue, &c.ActorUserID, &c.ActorLogin, &c.Via, &c.Note, &c.IP); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CountUsers は利用者の数（無効化された利用者を含む）を返す。
func CountUsers(ctx context.Context, q execQuerier) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&n)
	return n, err
}

// UsageSendPromptsSetting は指示文の作業名を送るか（projects.rules の usage.send_prompts）の変更記録の名前。
// 値は GateOn / GateOff。
func UsageSendPromptsSetting(slug string) string { return "usage_send_prompts:" + slug }

// 設定の切り替えの記録の値。
const (
	GateOn  = "on"
	GateOff = "off"
)

// RecordSettingChange は設定の変更を 1 件記録する（追記のみ）。
func RecordSettingChange(ctx context.Context, q execQuerier, ch SettingChange) error {
	_, err := q.ExecContext(ctx, `INSERT INTO setting_changes (name, old_value, new_value, actor_user_id, via, note, ip)
VALUES (?, ?, ?, ?, ?, ?, ?)`, truncate(ch.Name, 64), ch.OldValue, ch.NewValue, ch.ActorUserID, truncate(ch.Via, 16), truncate(ch.Note, 255), truncate(ch.IP, 45))
	return err
}
