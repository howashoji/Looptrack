package selfupdate

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"strings"

	"golang.org/x/crypto/blake2b"

	"github.com/howashoji/looptrack/internal/i18n"
)

// VerifyMinisign は minisign（https://jedisct1.github.io/minisign/）の署名を確かめる（SHA256SUMS を minisign で署名する）。
// pub は公開鍵の base64（minisign.pub の 2 行目。"Ed" + 鍵 ID 8 バイト + 公開鍵 32 バイト）、sig は .minisig の全文
// （untrusted comment・署名・trusted comment・全体の署名の 4 行）。署名の方式は Ed（本文そのもの）と ED（本文の BLAKE2b-512。既定）の両方を受ける。
// trusted comment も全体の署名で確かめる（コメントの差し替えを見逃さない）。
func VerifyMinisign(pub string, message, sig []byte) error {
	pk, err := base64.StdEncoding.DecodeString(strings.TrimSpace(pub))
	if err != nil || len(pk) != 42 || string(pk[:2]) != "Ed" {
		return i18n.Errorf("selfupdate.minisign.err.pubkey")
	}
	keyID, key := pk[2:10], ed25519.PublicKey(pk[10:])
	lines := strings.Split(strings.ReplaceAll(string(sig), "\r\n", "\n"), "\n")
	if len(lines) < 4 || !strings.HasPrefix(lines[0], "untrusted comment:") || !strings.HasPrefix(lines[2], "trusted comment: ") {
		return i18n.Errorf("selfupdate.minisign.err.sigfile")
	}
	s, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[1]))
	if err != nil || len(s) != 74 {
		return i18n.Errorf("selfupdate.minisign.err.sig")
	}
	alg, sigID, sigBytes := string(s[:2]), s[2:10], s[10:]
	if !bytes.Equal(sigID, keyID) {
		return i18n.Errorf("selfupdate.minisign.err.keyid")
	}
	signed := message
	switch alg {
	case "Ed":
	case "ED":
		h := blake2b.Sum512(message)
		signed = h[:]
	default:
		return i18n.Errorf("selfupdate.minisign.err.alg", "alg", alg)
	}
	if !ed25519.Verify(key, signed, sigBytes) {
		return i18n.Errorf("selfupdate.minisign.err.mismatch")
	}
	global, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[3]))
	if err != nil || len(global) != ed25519.SignatureSize {
		return i18n.Errorf("selfupdate.minisign.err.global_sig")
	}
	trusted := strings.TrimPrefix(lines[2], "trusted comment: ")
	if !ed25519.Verify(key, append(append([]byte{}, sigBytes...), trusted...), global) {
		return i18n.Errorf("selfupdate.minisign.err.trusted_comment")
	}
	return nil
}
