package api

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"

	"github.com/howashoji/looptrack/internal/client/env"
)

// TLSConfig は HTTPS の検証に使う設定。検証そのものは無効にしない。
//
// SSL_CERT_FILE・SSL_CERT_DIR（OpenSSL と同じ環境変数。社内のプロキシの CA などを足す）を尊重する:
//   - Linux などでは Go の SystemCertPool が既にこの 2 つを読む（OpenSSL と同じく既定の置き場の代わりに使う）。
//   - macOS・Windows の SystemCertPool は OS の検証を使いこの 2 つを読まないので、ここで読んだ証明書を足す
//     （OS の検証で通らなかったときに、足した証明書で検証し直す）。
//
// SystemCertPool が使えない環境では、この 2 つの証明書だけで検証する。
func TLSConfig(e env.Env) *tls.Config {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	for _, pem := range extraCAs(e) {
		pool.AppendCertsFromPEM(pem)
	}
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
}

// extraCAs は SSL_CERT_FILE のファイルと SSL_CERT_DIR（: 区切り・Windows は ;）の中のファイルの中身。
func extraCAs(e env.Env) [][]byte {
	var out [][]byte
	if f := e.Get("SSL_CERT_FILE"); f != "" {
		if b, err := os.ReadFile(f); err == nil {
			out = append(out, b)
		}
	}
	if d := e.Get("SSL_CERT_DIR"); d != "" {
		for _, dir := range filepath.SplitList(d) {
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, ent := range entries {
				if ent.IsDir() {
					continue
				}
				if b, err := os.ReadFile(filepath.Join(dir, ent.Name())); err == nil {
					out = append(out, b)
				}
			}
		}
	}
	return out
}
