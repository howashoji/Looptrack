package auth

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestPassword(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Errorf("hash = %s", h)
	}
	if ok, err := VerifyPassword(h, "correct horse battery"); !ok || err != nil {
		t.Errorf("正しいパスワードで失敗: %v", err)
	}
	if ok, _ := VerifyPassword(h, "correct horse batterY"); ok {
		t.Error("誤ったパスワードで成功した")
	}
	h2, _ := HashPassword("correct horse battery")
	if h == h2 {
		t.Error("ソルトが毎回変わっていない")
	}
	if _, err := HashPassword("short"); err != ErrPasswordPolicy {
		t.Errorf("短いパスワード: err = %v", err)
	}
}

// RFC 6238 付録 B のテストベクタ（SHA-1、8 桁）。
func TestTOTPVectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	for unix, want := range map[int64]string{
		59: "94287082", 1111111109: "07081804", 1111111111: "14050471", 1234567890: "89005924", 2000000000: "69279037", 20000000000: "65353130",
	} {
		if got := HOTP(secret, uint64(unix/TOTPPeriod), 8); got != want {
			t.Errorf("T=%d: got %s want %s", unix, got, want)
		}
	}
}

func TestVerifyTOTP(t *testing.T) {
	secret, _ := NewTOTPSecret()
	now := time.Unix(1_800_000_000, 0)
	cur := TOTPStep(now)
	code := HOTP(secret, uint64(cur), TOTPDigits)

	step, ok := VerifyTOTP(secret, code, now, 0)
	if !ok || step != cur {
		t.Fatalf("現在のコードで失敗: ok=%v step=%d", ok, step)
	}
	if _, ok := VerifyTOTP(secret, code, now, step); ok {
		t.Error("使用済みのステップを再利用できてしまった")
	}
	prev := HOTP(secret, uint64(cur-1), TOTPDigits)
	if _, ok := VerifyTOTP(secret, prev, now, 0); !ok {
		t.Error("1 ステップ前のコードを許容しなかった")
	}
	old := HOTP(secret, uint64(cur-2), TOTPDigits)
	if _, ok := VerifyTOTP(secret, old, now, 0); ok {
		t.Error("2 ステップ前のコードを受け入れた")
	}
	if _, ok := VerifyTOTP(secret, "12345", now, 0); ok {
		t.Error("桁数違いを受け入れた")
	}
	if !strings.Contains(TOTPURI("Looptrack", "alice", secret), "secret="+TOTPSecretString(secret)) {
		t.Error("URI にシークレットが無い")
	}
}

func TestBox(t *testing.T) {
	key, _ := NewSecretKey()
	b, err := NewBox(key)
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := b.Seal([]byte("secret"))
	if bytes.Contains(sealed, []byte("secret")) {
		t.Error("平文が残っている")
	}
	plain, err := b.Open(sealed)
	if err != nil || string(plain) != "secret" {
		t.Fatalf("復号: %q %v", plain, err)
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := b.Open(sealed); err == nil {
		t.Error("改ざんを検出しなかった")
	}
	other, _ := NewSecretKey()
	b2, _ := NewBox(other)
	sealed2, _ := b.Seal([]byte("x"))
	if _, err := b2.Open(sealed2); err == nil {
		t.Error("別の鍵で復号できた")
	}
	if _, err := NewBox("c2hvcnQ="); err == nil {
		t.Error("短い鍵を受け入れた")
	}
}

func TestPAT(t *testing.T) {
	tok, prefix, err := NewPAT()
	if err != nil || !strings.HasPrefix(tok, PATPrefix) || len(tok) != 64 || prefix != tok[:12] {
		t.Fatalf("tok=%s prefix=%s err=%v", tok, prefix, err)
	}
	tok2, _, _ := NewPAT()
	if tok == tok2 || bytes.Equal(HashToken(tok), HashToken(tok2)) {
		t.Error("トークンが一意でない")
	}
}
