// Package auth は認証の部品（パスワード・TOTP・秘密情報の暗号化・アクセストークン・セッション ID）を持つ。
// DB や HTTP に依存しない。
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id のパラメータ（OWASP Password Storage Cheat Sheet の下限: m=19MiB, t=2, p=1）。
// メモリ 1GB のサーバのため、同時実行数は HTTP 側で制限する。
const (
	argonMemory  = 19 * 1024
	argonTime    = 2
	argonThreads = 1
	argonKeyLen  = 32
	saltLen      = 16
)

// MinPasswordLen はパスワードの最短文字数。
const MinPasswordLen = 12

// ErrPasswordPolicy はパスワードが規則を満たさないことを表す。
var ErrPasswordPolicy = fmt.Errorf("パスワードは %d 文字以上にしてください", MinPasswordLen)

// HashPassword は argon2id の PHC 形式文字列を返す。
func HashPassword(password string) (string, error) {
	if len([]rune(password)) < MinPasswordLen {
		return "", ErrPasswordPolicy
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword は PHC 形式のハッシュとパスワードを照合する（定数時間比較）。
func VerifyPassword(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("argon2id のハッシュではありません")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errors.New("argon2 のバージョンが対応外です")
	}
	var m uint32
	var t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, err
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

var dummyHash, _ = HashPassword("dummy-password-for-timing")

// DummyVerify は存在しないユーザーでも同程度の時間をかける（応答時間でユーザーの有無を漏らさない）。
func DummyVerify(password string) {
	_, _ = VerifyPassword(dummyHash, password)
}
