// Package relsig は配布物の SHA256SUMS の署名（minisign）と、署名で守られた版の読み方。
// self-update（internal/client/selfupdate）と新しい版の確認（internal/updatecheck）の両方が使うので、規則をここ 1 か所に置く
// （サーバからも使うので internal/client の外に置く）。
//
//   - 署名: SHA256SUMS を minisign で署名し、.minisig（untrusted comment・署名・trusted comment・全体の署名の 4 行）を添える
//   - 実行ファイルの名前: looptrack_<版>_<os>_<arch>（windows は .exe）。版は名前にも入るので、署名された SHA256SUMS に
//     その名前の行があれば、その版は署名で守られている
//   - trusted comment: リリース（release.yml）は "looptrack <版> SHA256SUMS" で署名する。版を持たない形
//     （dist.sh sign-sums の既定 "looptrack SHA256SUMS <ディレクトリ名>"・minisign の既定）もある
package relsig

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"strings"

	"golang.org/x/crypto/blake2b"

	"github.com/howashoji/looptrack/internal/i18n"
)

// Command は配布する実行ファイルの名前の頭（looptrack_<版>_<os>_<arch>[.exe]）。
const Command = "looptrack"

// Verify は minisign（https://jedisct1.github.io/minisign/）の署名を確かめ、確かめた trusted comment を返す。
// pub は公開鍵の base64（minisign.pub の 2 行目。"Ed" + 鍵 ID 8 バイト + 公開鍵 32 バイト）、sig は .minisig の全文。
// 署名の方式は Ed（本文そのもの）と ED（本文の BLAKE2b-512。既定）の両方を受ける。
// trusted comment も全体の署名で確かめる（コメントの差し替えを見逃さない）。
func Verify(pub string, message, sig []byte) (string, error) {
	pk, err := base64.StdEncoding.DecodeString(strings.TrimSpace(pub))
	if err != nil || len(pk) != 42 || string(pk[:2]) != "Ed" {
		return "", i18n.Errorf("selfupdate.minisign.err.pubkey")
	}
	keyID, key := pk[2:10], ed25519.PublicKey(pk[10:])
	lines := strings.Split(strings.ReplaceAll(string(sig), "\r\n", "\n"), "\n")
	if len(lines) < 4 || !strings.HasPrefix(lines[0], "untrusted comment:") || !strings.HasPrefix(lines[2], "trusted comment: ") {
		return "", i18n.Errorf("selfupdate.minisign.err.sigfile")
	}
	s, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[1]))
	if err != nil || len(s) != 74 {
		return "", i18n.Errorf("selfupdate.minisign.err.sig")
	}
	alg, sigID, sigBytes := string(s[:2]), s[2:10], s[10:]
	if !bytes.Equal(sigID, keyID) {
		return "", i18n.Errorf("selfupdate.minisign.err.keyid")
	}
	signed := message
	switch alg {
	case "Ed":
	case "ED":
		h := blake2b.Sum512(message)
		signed = h[:]
	default:
		return "", i18n.Errorf("selfupdate.minisign.err.alg", "alg", alg)
	}
	if !ed25519.Verify(key, signed, sigBytes) {
		return "", i18n.Errorf("selfupdate.minisign.err.mismatch")
	}
	global, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[3]))
	if err != nil || len(global) != ed25519.SignatureSize {
		return "", i18n.Errorf("selfupdate.minisign.err.global_sig")
	}
	trusted := strings.TrimPrefix(lines[2], "trusted comment: ")
	if !ed25519.Verify(key, append(append([]byte{}, sigBytes...), trusted...), global) {
		return "", i18n.Errorf("selfupdate.minisign.err.trusted_comment")
	}
	return trusted, nil
}

// TrustedVersion はリリースの trusted comment（"looptrack <版> SHA256SUMS"）から版を読む。
// その形でなければ ok = false（版を持たない形。呼ぶ側は名前で版を確かめる）。
func TrustedVersion(trusted string) (version string, ok bool) {
	f := strings.Fields(trusted)
	if len(f) == 3 && f[0] == Command && f[2] == "SHA256SUMS" {
		return f[1], true
	}
	return "", false
}

// BinaryName は配布する実行ファイルの名前（looptrack_<版>_<os>_<arch>。windows は .exe 付き。dist.sh build と同じ）。
func BinaryName(version, goos, arch string) string {
	name := Command + "_" + version + "_" + goos + "_" + arch
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

// ServerArchiveName は Releases に上げるサーバ版の書庫の名前（looptrack_<版>_<os>_<arch>_server.tar.gz。
// windows は .zip。deploy/release/dist.sh の server_base・archive_ext と同じ）。
func ServerArchiveName(version, goos, arch string) string {
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	return Command + "_" + version + "_" + goos + "_" + arch + "_server." + ext
}

// Lookup は SHA256SUMS（"<ハッシュ>  <名前>" の行。名前の前の * は二進の印）から name のハッシュを探す。
func Lookup(sums []byte, name string) (string, bool) {
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return f[0], true
		}
	}
	return "", false
}
