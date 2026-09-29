package selfupdate

import "github.com/howashoji/looptrack/internal/relsig"

// VerifyMinisign は SHA256SUMS の minisign の署名を確かめる（規則は internal/relsig.Verify。新しい版の確認と共通）。
// pub は公開鍵の base64（minisign.pub の 2 行目）、sig は .minisig の全文。
func VerifyMinisign(pub string, message, sig []byte) error {
	_, err := relsig.Verify(pub, message, sig)
	return err
}
