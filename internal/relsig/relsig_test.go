package relsig

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"

	"golang.org/x/crypto/blake2b"
)

// TestTrustedVersion はリリースの trusted comment（"looptrack <版> SHA256SUMS"）だけから版を読むことを確かめる。
func TestTrustedVersion(t *testing.T) {
	for _, c := range []struct {
		in, want string
		ok       bool
	}{
		{"looptrack v1.0.0-rc.2 SHA256SUMS", "v1.0.0-rc.2", true}, // rc.2 の実物の trusted comment
		{"looptrack SHA256SUMS dist", "", false},                  // dist.sh sign-sums の既定
		{"timestamp:1 file:SHA256SUMS hashed", "", false},         // minisign の既定
		{"other v1.0.0 SHA256SUMS", "", false},
	} {
		if v, ok := TrustedVersion(c.in); v != c.want || ok != c.ok {
			t.Errorf("%q: %q %v", c.in, v, ok)
		}
	}
}

// TestBinaryNameAndLookup は実行ファイルの名前（windows は .exe）と SHA256SUMS の引き方を確かめる。
func TestBinaryNameAndLookup(t *testing.T) {
	if got := BinaryName("v1.0.0-rc.2", "windows", "arm64"); got != "looptrack_v1.0.0-rc.2_windows_arm64.exe" {
		t.Errorf("windows: %s", got)
	}
	if got := BinaryName("v1.0.0-rc.2", "linux", "amd64"); got != "looptrack_v1.0.0-rc.2_linux_amd64" {
		t.Errorf("linux: %s", got)
	}
	sums := []byte("aaa  looptrack_v1_linux_amd64\nbbb *looptrack_v1_darwin_arm64\nccc  x y\n")
	for name, want := range map[string]string{"looptrack_v1_linux_amd64": "aaa", "looptrack_v1_darwin_arm64": "bbb"} {
		if h, ok := Lookup(sums, name); !ok || h != want {
			t.Errorf("%s: %q %v", name, h, ok)
		}
	}
	if _, ok := Lookup(sums, "looptrack_v1_linux"); ok {
		t.Error("名前の前方一致で当てた")
	}
}

// TestServerArchiveName は書庫の名前（windows は .zip、それ以外は .tar.gz）が
// deploy/release/dist.sh の server_base・archive_ext と同じ形になることを確かめる。
func TestServerArchiveName(t *testing.T) {
	if got := ServerArchiveName("v1.0.0-rc.2", "windows", "arm64"); got != "looptrack_v1.0.0-rc.2_windows_arm64_server.zip" {
		t.Errorf("windows: %s", got)
	}
	if got := ServerArchiveName("v1.0.0-rc.2", "linux", "amd64"); got != "looptrack_v1.0.0-rc.2_linux_amd64_server.tar.gz" {
		t.Errorf("linux: %s", got)
	}
	if got := ServerArchiveName("v1.0.0-rc.2", "darwin", "arm64"); got != "looptrack_v1.0.0-rc.2_darwin_arm64_server.tar.gz" {
		t.Errorf("darwin: %s", got)
	}
}

// TestVerify は署名と trusted comment を確かめて返すこと、改ざんを通さないことを確かめる。
func TestVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keyID := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	pubStr := base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), keyID...), pub...))
	msg := []byte("abc  looptrack_v1.1.0_linux_arm64\n")
	h := blake2b.Sum512(msg)
	sig := ed25519.Sign(priv, h[:])
	trusted := "looptrack v1.1.0 SHA256SUMS"
	global := ed25519.Sign(priv, append(append([]byte{}, sig...), trusted...))
	file := []byte("untrusted comment: x\n" + base64.StdEncoding.EncodeToString(append(append([]byte("ED"), keyID...), sig...)) + "\n" +
		"trusted comment: " + trusted + "\n" + base64.StdEncoding.EncodeToString(global) + "\n")
	got, err := Verify(pubStr, msg, file)
	if err != nil || got != trusted {
		t.Fatalf("正しい署名を通さない: %q %v", got, err)
	}
	if _, err := Verify(pubStr, append(msg, 'x'), file); err == nil {
		t.Error("改ざんした本文を通した")
	}
}
