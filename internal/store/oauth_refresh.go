package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// RefreshToken は OAuth の更新トークン（平文は保存しない）。
type RefreshToken struct {
	ID            int64
	FamilyID      string
	UserID        int64
	ClientID      string
	AccessTokenID int64
	Scope         string
	Resource      string
	ExpiresAt     time.Time
	UsedAt        sql.NullTime
	RevokedAt     sql.NullTime
	// AccessRevoked は対になるアクセストークンが失効済みか（アカウント画面の失効）。
	AccessRevoked bool
}

// CreateRefreshToken は更新トークンを保存する。
func CreateRefreshToken(ctx context.Context, q execQuerier, hash []byte, t RefreshToken) error {
	_, err := q.ExecContext(ctx, `INSERT INTO oauth_refresh_tokens
(token_hash, family_id, user_id, client_id, access_token_id, scope, resource, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		hash, t.FamilyID, t.UserID, t.ClientID, t.AccessTokenID, t.Scope, t.Resource, t.ExpiresAt)
	return err
}

// RefreshTokenForUpdate は更新トークンを行ロックつきで引く（トランザクションの中で使う。同じ値の同時使用を直列化する）。
// 使用済み・失効・期限の判定は呼び出し側。
func RefreshTokenForUpdate(ctx context.Context, q execQuerier, hash []byte) (RefreshToken, error) {
	var t RefreshToken
	var accessRevoked sql.NullTime
	err := q.QueryRowContext(ctx, `SELECT r.id, r.family_id, r.user_id, r.client_id, r.access_token_id, r.scope, r.resource,
       r.expires_at, r.used_at, r.revoked_at, a.revoked_at
  FROM oauth_refresh_tokens r JOIN api_tokens a ON a.id = r.access_token_id
 WHERE r.token_hash = ? FOR UPDATE`, hash).
		Scan(&t.ID, &t.FamilyID, &t.UserID, &t.ClientID, &t.AccessTokenID, &t.Scope, &t.Resource,
			&t.ExpiresAt, &t.UsedAt, &t.RevokedAt, &accessRevoked)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	t.AccessRevoked = accessRevoked.Valid
	return t, err
}

// MarkRefreshTokenUsed は入れ替えに使った印を付ける（以後この値の提示は再利用として扱う）。
func MarkRefreshTokenUsed(ctx context.Context, q execQuerier, id int64, now time.Time) error {
	_, err := q.ExecContext(ctx, "UPDATE oauth_refresh_tokens SET used_at = ? WHERE id = ? AND used_at IS NULL", now, id)
	return err
}

// RevokeRefreshFamily は系列の更新トークンと、それと対で発行したアクセストークンをすべて失効させる。
func RevokeRefreshFamily(ctx context.Context, q execQuerier, familyID string, now time.Time) error {
	if _, err := q.ExecContext(ctx, `UPDATE api_tokens SET revoked_at = ?
 WHERE revoked_at IS NULL AND id IN (SELECT access_token_id FROM oauth_refresh_tokens WHERE family_id = ?)`, now, familyID); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, "UPDATE oauth_refresh_tokens SET revoked_at = ? WHERE family_id = ? AND revoked_at IS NULL", now, familyID)
	return err
}
