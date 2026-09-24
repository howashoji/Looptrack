package store

import (
	"context"
	"testing"

	"github.com/howashoji/looptrack/migrations"
)

// 利用者 0 人の新しい DB では未設定のまま（最初の利用者を作るときに決める）。未設定の間は必須として扱う。
func TestFreshDBLeavesTwoFactorUnset(t *testing.T) {
	db, _, _ := testDB(t)
	ctx := context.Background()
	if _, err := Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	policy, set, err := TwoFactorPolicy(ctx, db)
	if err != nil || set || policy != TwoFactorRequired {
		t.Errorf("設定 = %q set=%v err=%v, want 未設定（必須として扱う）", policy, set, err)
	}
	if cs, _ := SettingChanges(ctx, db, SettingTwoFactor, 10); len(cs) != 0 {
		t.Errorf("記録 = %+v", cs)
	}
}

// SetTwoFactorPolicy: 同じ値なら記録しない。必須にしたときだけ TOTP を経ていないセッションを破棄する。シークレットは消さない。
func TestSetTwoFactorPolicy(t *testing.T) {
	db, _, _ := testDB(t)
	ctx := context.Background()
	if _, err := Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, "INSERT INTO users (login, password_hash, totp_secret, totp_enabled) VALUES ('alice', 'x', 'sealed', TRUE), ('bob', 'x', NULL, FALSE)")
	if _, err := SetTwoFactorPolicy(ctx, db, "maybe", SettingChange{Via: "command"}); err == nil {
		t.Error("不正な値を受け付けた")
	}
	res, err := SetTwoFactorPolicy(ctx, db, TwoFactorOptional, SettingChange{Via: "command"})
	if err != nil || !res.Changed || res.Old != "" {
		t.Fatalf("未設定 → 任意: %+v %v", res, err)
	}
	if res, _ := SetTwoFactorPolicy(ctx, db, TwoFactorOptional, SettingChange{Via: "command"}); res.Changed {
		t.Error("同じ値で変更扱いになった")
	}
	// alice: TOTP 済み / TOTP なし、bob: パスワードだけ、bob: 途中
	mustExec(t, db, "INSERT INTO web_sessions (id_hash, user_id, csrf_token, mfa_passed, totp_verified, expires_at) VALUES (UNHEX(SHA2('a1', 256)), 1, 'c', TRUE, TRUE, '2099-01-01')")
	mustExec(t, db, "INSERT INTO web_sessions (id_hash, user_id, csrf_token, mfa_passed, totp_verified, expires_at) VALUES (UNHEX(SHA2('a2', 256)), 1, 'c', TRUE, FALSE, '2099-01-01')")
	mustExec(t, db, "INSERT INTO web_sessions (id_hash, user_id, csrf_token, mfa_passed, totp_verified, expires_at) VALUES (UNHEX(SHA2('b1', 256)), 2, 'c', TRUE, FALSE, '2099-01-01')")
	res, err = SetTwoFactorPolicy(ctx, db, TwoFactorRequired, SettingChange{Via: "command"})
	if err != nil || !res.Changed || res.Old != TwoFactorOptional || res.RevokedSessions != 2 {
		t.Fatalf("任意 → 必須: %+v %v", res, err)
	}
	var n int
	db.QueryRow("SELECT COUNT(*) FROM web_sessions WHERE id_hash = UNHEX(SHA2('a1', 256))").Scan(&n)
	if n != 1 {
		t.Error("TOTP 済みのセッションまで破棄した")
	}
	var secret []byte
	db.QueryRow("SELECT totp_secret FROM users WHERE login = 'alice'").Scan(&secret)
	if string(secret) != "sealed" {
		t.Errorf("シークレット = %q", secret)
	}
	cs, _ := SettingChanges(ctx, db, SettingTwoFactor, 10)
	if len(cs) != 2 || cs[0].OldValue != TwoFactorOptional || cs[0].NewValue != TwoFactorRequired {
		t.Errorf("記録 = %+v", cs)
	}
}
