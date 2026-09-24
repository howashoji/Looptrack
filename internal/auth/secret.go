package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Box は DB に置く秘密情報（TOTP シークレット）を AES-256-GCM で暗号化する。
// 鍵は環境変数 LOOPTRACK_SECRET_KEY（32 バイトを base64 で表したもの）から作る。DB が漏れても鍵が無ければ復号できない。
type Box struct{ aead cipher.AEAD }

// NewBox は base64 の 32 バイト鍵から Box を作る。
func NewBox(keyB64 string) (*Box, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(keyB64))
	if err != nil {
		return nil, fmt.Errorf("LOOPTRACK_SECRET_KEY を base64 として読めません: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("LOOPTRACK_SECRET_KEY は 32 バイトにしてください（現在 %d バイト）", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead}, nil
}

// NewSecretKey は LOOPTRACK_SECRET_KEY に設定する値を生成する。
func NewSecretKey() (string, error) {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(k), nil
}

// Seal は平文を暗号化する（nonce を先頭に付ける）。
func (b *Box) Seal(plain []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, plain, nil), nil
}

// Open は Seal したものを復号する。
func (b *Box) Open(sealed []byte) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return nil, errors.New("暗号文が短すぎます")
	}
	return b.aead.Open(nil, sealed[:n], sealed[n:], nil)
}

// RandomToken は URL で使える乱数文字列（bytes バイト分）。
func RandomToken(bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken はトークン・セッション ID を DB に保存する形（SHA-256）にする。
func HashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// PATPrefix は個人アクセストークンの接頭辞（漏洩検知ツールで見つけやすくする）。
const PATPrefix = "imp_"

// NewPAT は個人アクセストークンを作る。表示用の先頭 12 文字も返す。
func NewPAT() (token, displayPrefix string, err error) {
	b := make([]byte, 30)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	token = PATPrefix + hex.EncodeToString(b)
	return token, token[:12], nil
}

// OAuthPrefix は OAuth で発行するアクセストークンの接頭辞（PAT と見分けるため）。
const OAuthPrefix = "imo_"

// NewOAuthToken は OAuth のアクセストークンを作る。表示用の先頭 12 文字も返す。
func NewOAuthToken() (token, displayPrefix string, err error) {
	b := make([]byte, 30)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	token = OAuthPrefix + hex.EncodeToString(b)
	return token, token[:12], nil
}

// RefreshPrefix は OAuth の更新トークンの接頭辞（アクセストークンと見分けるため）。
const RefreshPrefix = "imr_"

// NewRefreshToken は OAuth の更新トークンを作る。
func NewRefreshToken() (string, error) {
	b := make([]byte, 30)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return RefreshPrefix + hex.EncodeToString(b), nil
}
