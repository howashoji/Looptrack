package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP の設定（RFC 6238 / Google Authenticator 互換: SHA-1・30 秒・6 桁）。
const (
	TOTPPeriod = 30
	TOTPDigits = 6
	// TOTPSkew は前後に許容するステップ数（時計のずれ対策）。
	TOTPSkew = 1
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret は 20 バイトのシークレットを返す。
func NewTOTPSecret() ([]byte, error) {
	s := make([]byte, 20)
	_, err := rand.Read(s)
	return s, err
}

// TOTPSecretString は認証アプリに手入力する Base32 文字列。
func TOTPSecretString(secret []byte) string { return b32.EncodeToString(secret) }

// TOTPURI は認証アプリに登録する otpauth URI。
func TOTPURI(issuer, account string, secret []byte) string {
	label := url.PathEscape(issuer) + ":" + url.PathEscape(account)
	q := url.Values{}
	q.Set("secret", TOTPSecretString(secret))
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(TOTPDigits))
	q.Set("period", fmt.Sprint(TOTPPeriod))
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// HOTP は RFC 4226 のワンタイムパスワード。
func HOTP(secret []byte, counter uint64, digits int) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	mac := hmac.New(sha1.New, secret)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	code := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, code%mod)
}

// TOTPStep は時刻に対応するステップ番号。
func TOTPStep(t time.Time) int64 { return t.Unix() / TOTPPeriod }

// VerifyTOTP は code が now の前後 TOTPSkew ステップのいずれかに一致すれば、一致したステップを返す。
// lastStep 以下のステップは使用済みとして拒否する（同じコードの再利用を防ぐ）。
func VerifyTOTP(secret []byte, code string, now time.Time, lastStep int64) (step int64, ok bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != TOTPDigits {
		return 0, false
	}
	cur := TOTPStep(now)
	for d := -int64(TOTPSkew); d <= TOTPSkew; d++ {
		s := cur + d
		if s <= lastStep || s < 0 {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(HOTP(secret, uint64(s), TOTPDigits)), []byte(code)) == 1 {
			return s, true
		}
	}
	return 0, false
}
