package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/i18n"
	"golang.org/x/crypto/blake2b"
)

// fakeDist は GET /api/v1/dist と本体を返す偽のサーバ。
type fakeDist struct {
	bins    map[string][]byte // 名前 → 本体
	listing Listing
	sums    []byte
	sig     []byte
	gets    []string
}

func (f *fakeDist) serve(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":{"code":"unauthorized","message":"認証が必要です"}}`))
			return
		}
		f.gets = append(f.gets, r.URL.Path)
		switch p := strings.TrimPrefix(r.URL.Path, "/im/api/v1"); {
		case p == "/dist":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(f.listing)
		case p == "/dist/bin/SHA256SUMS" && f.sums != nil:
			w.Write(f.sums)
		case p == "/dist/bin/SHA256SUMS.minisig" && f.sig != nil:
			w.Write(f.sig)
		case strings.HasPrefix(p, "/dist/bin/") && f.bins[strings.TrimPrefix(p, "/dist/bin/")] != nil:
			w.Write(f.bins[strings.TrimPrefix(p, "/dist/bin/")])
		default:
			w.WriteHeader(404)
			w.Write([]byte(`{"error":{"code":"not_found","message":"無い"}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (f *fakeDist) add(base, name, version, goos, arch string, body []byte) {
	if f.bins == nil {
		f.bins = map[string][]byte{}
	}
	f.bins[name] = body
	f.listing.Binaries = append(f.listing.Binaries, Binary{Name: name, OS: goos, Arch: arch, Version: version, SHA256: sum(body),
		Size: int64(len(body)), URL: base + "/api/v1/dist/bin/" + name})
}

type result struct {
	code           int
	stdout, stderr string
}

func selfUpdate(t *testing.T, srvURL, exe, version string, args ...string) result {
	t.Helper()
	home := t.TempDir()
	var out, errb bytes.Buffer
	code := Main(args, Options{
		Env:    env.FromMap(map[string]string{"LOOPTRACK_API_URL": srvURL + "/im", "LOOPTRACK_TOKEN": "tok", "LOOPTRACK_LANG": "ja", "HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config")}),
		Stdout: &out, Stderr: &errb, Version: version, GOOS: "linux", GOARCH: "arm64", Exe: exe,
	})
	return result{code, out.String(), errb.String()}
}

func placeExe(t *testing.T, body string) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "looptrack")
	if err := os.WriteFile(exe, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

func content(t *testing.T, p string) string {
	t.Helper()
	b, _ := os.ReadFile(p)
	return string(b)
}

func onlyFile(t *testing.T, dir string) []string {
	t.Helper()
	es, _ := os.ReadDir(dir)
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

// withoutKey は公開鍵を空にしたビルド（署名しない並行期間の配布。dist.sh の RELEASE_MINISIGN_PUBKEY=）としてテストを流す。
func withoutKey(t *testing.T) {
	t.Helper()
	orig := MinisignPublicKey
	MinisignPublicKey = ""
	t.Cleanup(func() { MinisignPublicKey = orig })
}

func TestSelfUpdate(t *testing.T) {
	withoutKey(t)
	f := &fakeDist{}
	srv := f.serve(t)
	base := srv.URL + "/im"
	f.add(base, "looptrack_v1.1.0_linux_arm64", "v1.1.0", "linux", "arm64", []byte("new-binary"))
	f.add(base, "looptrack_v1.1.0_linux_amd64", "v1.1.0", "linux", "amd64", []byte("other"))

	// 古い版 → 置き換える（一時ファイルは残らない）
	exe := placeExe(t, "old-binary")
	r := selfUpdate(t, srv.URL, exe, "v1.0.0")
	if r.code != 0 || content(t, exe) != "new-binary" || !strings.Contains(r.stdout, "v1.0.0 → v1.1.0") {
		t.Fatalf("更新: %+v %q", r, content(t, exe))
	}
	// Windows には実行権限のビットが無い（拡張子で決まる）
	if fi, _ := os.Stat(exe); runtime.GOOS != "windows" && fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("実行権限が無い: %v", fi.Mode())
	}
	if names := onlyFile(t, filepath.Dir(exe)); len(names) != 1 {
		t.Errorf("一時ファイルが残った: %v", names)
	}
	// 最新（同じ・新しい）は何もしない。--check は示すだけ
	for _, v := range []string{"v1.1.0", "v1.2.0"} {
		exe := placeExe(t, "keep")
		if r := selfUpdate(t, srv.URL, exe, v); r.code != 0 || content(t, exe) != "keep" || !strings.Contains(r.stdout, "最新です") {
			t.Errorf("%s: %+v", v, r)
		}
	}
	exe = placeExe(t, "keep")
	if r := selfUpdate(t, srv.URL, exe, "v1.0.0", "--check"); r.code != 0 || content(t, exe) != "keep" || !strings.Contains(r.stdout, "更新があります") {
		t.Errorf("--check: %+v", r)
	}
	// 比べられない版（dev）は --force のときだけ
	if r := selfUpdate(t, srv.URL, exe, "dev"); r.code != 0 || content(t, exe) != "keep" || !strings.Contains(r.stdout, "--force") {
		t.Errorf("dev: %+v", r)
	}
	if r := selfUpdate(t, srv.URL, exe, "dev", "--force"); r.code != 0 || content(t, exe) != "new-binary" {
		t.Errorf("dev --force: %+v", r)
	}

	// SHA-256 が一覧と違えば置き換えない
	f.bins["looptrack_v1.1.0_linux_arm64"] = []byte("tampered")
	exe = placeExe(t, "old-binary")
	if r := selfUpdate(t, srv.URL, exe, "v1.0.0"); r.code != 1 || content(t, exe) != "old-binary" || !strings.Contains(r.stderr, "SHA-256 が配布の一覧と一致しません") {
		t.Errorf("改ざん: %+v", r)
	}
	if names := onlyFile(t, filepath.Dir(exe)); len(names) != 1 {
		t.Errorf("失敗で一時ファイルが残った: %v", names)
	}
	f.bins["looptrack_v1.1.0_linux_arm64"] = []byte("new-binary")

	// 配っていない OS・CPU・認証の失敗・アプリの中
	f.listing.Binaries = f.listing.Binaries[1:]
	if r := selfUpdate(t, srv.URL, exe, "v1.0.0"); r.code != 1 || !strings.Contains(r.stderr, "linux/arm64 向けの looptrack がありません") {
		t.Errorf("配っていない: %+v", r)
	}
	f.add(base, "looptrack_v1.1.0_linux_arm64", "v1.1.0", "linux", "arm64", []byte("new-binary"))
	app := filepath.Join(t.TempDir(), "Looptrack.app", "Contents", "MacOS")
	os.MkdirAll(app, 0o755)
	if r := selfUpdate(t, srv.URL, filepath.Join(app, "looptrack"), "v1.0.0"); r.code != 1 || !strings.Contains(r.stderr, "アプリ") {
		t.Errorf("アプリの中: %+v", r)
	}
	var out, errb bytes.Buffer
	if code := Main(nil, Options{Env: env.FromMap(map[string]string{"LOOPTRACK_API_URL": base, "LOOPTRACK_TOKEN": "wrong", "HOME": t.TempDir()}), Stdout: &out, Stderr: &errb,
		Version: "v1.0.0", GOOS: "linux", GOARCH: "arm64", Exe: exe}); code != 1 || !strings.Contains(errb.String(), "401") || !strings.Contains(errb.String(), "login --browser") {
		t.Errorf("401: %d %s", code, errb.String())
	}
	// --url は環境変数より優先
	exe = placeExe(t, "old-binary")
	home := t.TempDir()
	if code := Main([]string{"--url", base}, Options{Env: env.FromMap(map[string]string{"LOOPTRACK_API_URL": "https://example.invalid/im", "LOOPTRACK_TOKEN": "tok", "HOME": home}),
		Stdout: &out, Stderr: &errb, Version: "v1.0.0", GOOS: "linux", GOARCH: "arm64", Exe: exe}); code != 0 || content(t, exe) != "new-binary" {
		t.Errorf("--url: %d %s", code, errb.String())
	}
}

// TestReplaceWindows は Windows の置き換え（実行中の exe を .old に改名してから置く・次回に .old を消す）を POSIX でなぞる。
func TestReplaceWindows(t *testing.T) {
	exe := placeExe(t, "running")
	if err := replace(exe, []byte("next"), true); err != nil {
		t.Fatal(err)
	}
	if content(t, exe) != "next" || content(t, exe+".old") != "running" {
		t.Errorf("置き換え: %q %q", content(t, exe), content(t, exe+".old"))
	}
	// .old が残っている（消せなかった）ときは別の名前に退ける
	if err := replace(exe, []byte("third"), true); err != nil {
		t.Fatal(err)
	}
	if content(t, exe) != "third" || len(onlyFile(t, filepath.Dir(exe))) != 3 {
		t.Errorf("2 回目: %v", onlyFile(t, filepath.Dir(exe)))
	}
	cleanupOld(exe)
	if _, err := os.Stat(exe + ".old"); err == nil {
		t.Error(".old が消えない")
	}
}

// minisignSign はテスト用に minisign と同じ形の署名（ED＝BLAKE2b-512 の prehash）を作る。
func minisignSign(priv ed25519.PrivateKey, keyID []byte, msg []byte, trusted string) []byte {
	h := blake2b.Sum512(msg)
	sig := ed25519.Sign(priv, h[:])
	blob := append(append([]byte("ED"), keyID...), sig...)
	global := ed25519.Sign(priv, append(append([]byte{}, sig...), trusted...))
	return []byte("untrusted comment: signature from minisign secret key\n" + base64.StdEncoding.EncodeToString(blob) + "\n" +
		"trusted comment: " + trusted + "\n" + base64.StdEncoding.EncodeToString(global) + "\n")
}

func TestMinisign(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keyID := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	pubStr := base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), keyID...), pub...))
	msg := []byte("abc  looptrack_v1.1.0_linux_arm64\n")
	sig := minisignSign(priv, keyID, msg, "timestamp:1 file:SHA256SUMS")
	if err := VerifyMinisign(pubStr, msg, sig); err != nil {
		t.Fatal(err)
	}
	if err := VerifyMinisign(pubStr, append(msg, 'x'), sig); err == nil {
		t.Error("改ざんした本文を通した")
	}
	bad := bytes.Replace(sig, []byte("file:SHA256SUMS"), []byte("file:OTHER"), 1)
	if err := VerifyMinisign(pubStr, msg, bad); err == nil {
		t.Error("trusted comment の差し替えを通した")
	}
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	if err := VerifyMinisign(pubStr, msg, minisignSign(other, keyID, msg, "t")); err == nil {
		t.Error("別の鍵の署名を通した")
	}
	if err := VerifyMinisign(pubStr, msg, minisignSign(priv, []byte{9, 9, 9, 9, 9, 9, 9, 9}, msg, "t")); err == nil || !strings.Contains(i18n.Text(i18n.JA, err), "鍵 ID") {
		t.Errorf("鍵 ID の違い: %v", err)
	}

	// 鍵を埋め込んだビルドの self-update は署名と SHA256SUMS の中身を確かめる
	f := &fakeDist{}
	srv := f.serve(t)
	base := srv.URL + "/im"
	body := []byte("signed-binary")
	f.add(base, "looptrack_v1.1.0_linux_arm64", "v1.1.0", "linux", "arm64", body)
	f.sums = []byte(sum(body) + "  looptrack_v1.1.0_linux_arm64\n")
	orig := MinisignPublicKey
	MinisignPublicKey = pubStr
	t.Cleanup(func() { MinisignPublicKey = orig })
	exe := placeExe(t, "old")
	if r := selfUpdate(t, srv.URL, exe, "v1.0.0"); r.code != 1 || !strings.Contains(r.stderr, "署名（.minisig）がありません") || content(t, exe) != "old" {
		t.Errorf("署名なし: %+v", r)
	}
	f.listing.SumsURL = base + "/api/v1/dist/bin/SHA256SUMS"
	f.listing.MinisigURL = base + "/api/v1/dist/bin/SHA256SUMS.minisig"
	f.sig = minisignSign(priv, keyID, f.sums, "t")
	if r := selfUpdate(t, srv.URL, exe, "v1.0.0"); r.code != 0 || content(t, exe) != "signed-binary" || strings.Contains(r.stdout, "署名の確認は未設定") {
		t.Errorf("署名あり: %+v", r)
	}
	// 署名された SHA256SUMS と一覧のハッシュが違う
	f.sums = []byte(strings.Repeat("0", 64) + "  looptrack_v1.1.0_linux_arm64\n")
	f.sig = minisignSign(priv, keyID, f.sums, "t")
	exe = placeExe(t, "old")
	if r := selfUpdate(t, srv.URL, exe, "v1.0.0"); r.code != 1 || !strings.Contains(r.stderr, "ハッシュが一覧と違います") || content(t, exe) != "old" {
		t.Errorf("SHA256SUMS の不一致: %+v", r)
	}
}

// TestEmbeddedPublicKey は埋め込んだ公開鍵（Looptrack 専用・鍵 ID 29D707D7EBFF246B）が、配布の公開鍵のファイル
// deploy/release/minisign.pub と install.sh の値と同じで、鍵を持つ既定のビルドは署名の無い配布から置き換えないことを確かめる。
func TestEmbeddedPublicKey(t *testing.T) {
	pk, err := base64.StdEncoding.DecodeString(MinisignPublicKey)
	if err != nil || len(pk) != 42 || string(pk[:2]) != "Ed" {
		t.Fatalf("公開鍵の形: %q %v", MinisignPublicKey, err)
	}
	id := make([]byte, 8)
	for i := range id {
		id[i] = pk[9-i] // minisign の鍵 ID はリトルエンディアンで表示される
	}
	if got := strings.ToUpper(hex.EncodeToString(id)); got != "29D707D7EBFF246B" {
		t.Errorf("鍵 ID: %s", got)
	}
	root := filepath.Join("..", "..", "..")
	pub, err := os.ReadFile(filepath.Join(root, "deploy", "release", "minisign.pub"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(pub)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "29D707D7EBFF246B") || strings.TrimSpace(lines[1]) != MinisignPublicKey {
		t.Errorf("deploy/release/minisign.pub と違う: %q", pub)
	}
	inst, err := os.ReadFile(filepath.Join(root, "deploy", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(inst), "MINISIGN_PUBKEY_DEFAULT="+MinisignPublicKey+"\n") {
		t.Error("deploy/install.sh の MINISIGN_PUBKEY_DEFAULT と違う")
	}

	// 既定のビルド（鍵あり）は、署名の無い配布（今の dev サーバの配布の形）から置き換えない
	f := &fakeDist{}
	srv := f.serve(t)
	base := srv.URL + "/im"
	f.add(base, "looptrack_v1.1.0_linux_arm64", "v1.1.0", "linux", "arm64", []byte("unsigned"))
	exe := placeExe(t, "old")
	if r := selfUpdate(t, srv.URL, exe, "v1.0.0"); r.code != 1 || !strings.Contains(r.stderr, "署名（.minisig）がありません") || content(t, exe) != "old" {
		t.Errorf("鍵あり・署名なし: %+v", r)
	}
	// 署名はあるが別の鍵（使い捨て）で作られたものも置き換えない
	body := []byte("unsigned")
	f.sums = []byte(sum(body) + "  looptrack_v1.1.0_linux_arm64\n")
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	f.sig = minisignSign(priv, pk[2:10], f.sums, "t")
	f.listing.SumsURL = base + "/api/v1/dist/bin/SHA256SUMS"
	f.listing.MinisigURL = base + "/api/v1/dist/bin/SHA256SUMS.minisig"
	if r := selfUpdate(t, srv.URL, exe, "v1.0.0"); r.code != 1 || !strings.Contains(r.stderr, "署名が一致しません") || content(t, exe) != "old" {
		t.Errorf("鍵あり・別の鍵の署名: %+v", r)
	}
}

// TestMinisignRealTool は本物の minisign（0.12）が作った署名（testdata。使い捨ての鍵で作り、秘密鍵は捨てた）を
// VerifyMinisign が通し、1 バイトの改ざんと別の鍵（Looptrack の鍵）を拒むことを確かめる（dist.sh sign-sums の出力と同じ形）。
func TestMinisignRealTool(t *testing.T) {
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	pubLines := strings.Split(strings.TrimSpace(string(read("minisign-test.pub"))), "\n")
	pub := pubLines[len(pubLines)-1]
	sums, sig := read("SHA256SUMS"), read("SHA256SUMS.minisig")
	if err := VerifyMinisign(pub, sums, sig); err != nil {
		t.Fatalf("minisign の署名を通さない: %v", err)
	}
	bad := append([]byte{}, sums...)
	bad[0] ^= 1
	if err := VerifyMinisign(pub, bad, sig); err == nil {
		t.Error("改ざんを通した")
	}
	if err := VerifyMinisign(MinisignPublicKey, sums, sig); err == nil || !strings.Contains(i18n.Text(i18n.JA, err), "鍵 ID") {
		t.Errorf("Looptrack の鍵で使い捨ての鍵の署名を通した: %v", err)
	}
}

// fakeInstall は install.sh の置き場（BIN と設定）を一時ディレクトリに作る（実際の /usr/local/bin・/etc は見ない）。
func fakeInstall(t *testing.T, markers ...string) (ServerInstall, string) {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "usr", "local", "bin", "looptrack")
	os.MkdirAll(filepath.Dir(bin), 0o755)
	if err := os.WriteFile(bin, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := ServerInstall{Bin: bin}
	for _, m := range []string{"etc/looptrack/install.conf", "etc/looptrack/.env", "etc/systemd/system/looptrack.service"} {
		s.Markers = append(s.Markers, filepath.Join(root, m))
	}
	for _, m := range markers {
		p := filepath.Join(root, m)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return s, bin
}

// TestInstalledServer は install.sh で入れたサーバの判定: linux・置き場が BIN・設定のどれかがある、のすべてで当たる。
func TestInstalledServer(t *testing.T) {
	for _, m := range []string{"etc/looptrack/install.conf", "etc/looptrack/.env", "etc/systemd/system/looptrack.service"} {
		s, bin := fakeInstall(t, m)
		if got, ok := InstalledServer("linux", bin, s); !ok || !strings.HasSuffix(filepath.ToSlash(got), m) {
			t.Errorf("%s: %q %v", m, got, ok)
		}
		if _, ok := InstalledServer("darwin", bin, s); ok {
			t.Errorf("%s: linux 以外で当たった", m)
		}
		other := placeExe(t, "x") // 別の置き場（手元の CLI）
		if _, ok := InstalledServer("linux", other, s); ok {
			t.Errorf("%s: 別の置き場で当たった", m)
		}
	}
	// 設定が無い（BIN だけ手で置いた）なら当たらない
	s, bin := fakeInstall(t)
	if _, ok := InstalledServer("linux", bin, s); ok {
		t.Error("設定が無いのに当たった")
	}
	// BIN がシンボリックリンクなら、その実体（exe は EvalSymlinks 後）と比べる
	if runtime.GOOS != "windows" {
		s, bin := fakeInstall(t, "etc/looptrack/install.conf")
		link := filepath.Join(t.TempDir(), "looptrack")
		if err := os.Symlink(bin, link); err != nil {
			t.Fatal(err)
		}
		real, _ := filepath.EvalSymlinks(bin)
		s.Bin = link
		if _, ok := InstalledServer("linux", real, s); !ok {
			t.Error("シンボリックリンクの BIN で当たらない")
		}
	}
}

// TestSelfUpdateInstalledServer は install.sh で入れたサーバの self-update: 置き換えずに、インストーラの 1 行の --upgrade を案内する。--check は動く。
func TestSelfUpdateInstalledServer(t *testing.T) {
	withoutKey(t)
	f := &fakeDist{}
	srv := f.serve(t)
	f.add(srv.URL+"/im", "looptrack_v1.1.0_linux_arm64", "v1.1.0", "linux", "arm64", []byte("new-binary"))
	run := func(s ServerInstall, exe string, args ...string) result {
		home := t.TempDir()
		var out, errb bytes.Buffer
		code := Main(args, Options{
			Env:    env.FromMap(map[string]string{"LOOPTRACK_API_URL": srv.URL + "/im", "LOOPTRACK_TOKEN": "tok", "LOOPTRACK_LANG": "ja", "HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config")}),
			Stdout: &out, Stderr: &errb, Version: "v1.0.0", GOOS: "linux", GOARCH: "arm64", Exe: exe, Install: &s,
		})
		return result{code, out.String(), errb.String()}
	}
	s, bin := fakeInstall(t, "etc/looptrack/install.conf")
	for _, args := range [][]string{nil, {"--force"}} {
		if r := run(s, bin, args...); r.code != 1 || content(t, bin) != "old-binary" || !strings.Contains(r.stderr, "main/deploy/install.sh | sudo sh -s -- --upgrade") {
			t.Errorf("%v: %+v", args, r)
		}
	}
	if r := run(s, bin, "--check"); r.code != 0 || content(t, bin) != "old-binary" || !strings.Contains(r.stdout, "更新があります") {
		t.Errorf("--check: %+v", r)
	}
	// 設定が無い・別の置き場ならこれまでどおり置き換える
	s2, bin2 := fakeInstall(t)
	if r := run(s2, bin2); r.code != 0 || content(t, bin2) != "new-binary" {
		t.Errorf("設定なし: %+v", r)
	}
	other := placeExe(t, "old-binary")
	if r := run(s, other); r.code != 0 || content(t, other) != "new-binary" {
		t.Errorf("別の置き場: %+v", r)
	}
}

// TestDefaultServerInstall は既定の置き場が deploy/install.sh の値と同じであることを確かめる。
func TestDefaultServerInstall(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	sh := string(b)
	for _, want := range []string{
		"BIN=" + DefaultServerInstall.Bin + "\n",
		"CONF_DIR=/etc/looptrack ",
		"STATE=$CONF_DIR/install.conf ",
		"UNIT=/etc/systemd/system/looptrack.service\n",
	} {
		if !strings.Contains(sh, want) {
			t.Errorf("deploy/install.sh に %q がない（DefaultServerInstall と合わせる）", want)
		}
	}
	want := []string{"/etc/looptrack/install.conf", "/etc/looptrack/.env", "/etc/systemd/system/looptrack.service"}
	if strings.Join(DefaultServerInstall.Markers, ",") != strings.Join(want, ",") {
		t.Errorf("Markers: %v", DefaultServerInstall.Markers)
	}
}

// TestSignedVersionRejectsDowngrade は、一覧の version を署名で守られた版（SHA256SUMS の中の名前と、リリースの
// trusted comment "looptrack <版> SHA256SUMS"）と照らし合わせることを確かめる。署名の正しい古い版の SHA256SUMS に
// 新しく見せかけた version を組にした一覧からは置き換えず、正直に古い版を配る一覧からは --force のときだけ置き換える。
// 対照として、名前・trusted comment・version がそろって手元より新しければ置き換えることも確かめる。
func TestSignedVersionRejectsDowngrade(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keyID := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	orig := MinisignPublicKey
	MinisignPublicKey = base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), keyID...), pub...))
	t.Cleanup(func() { MinisignPublicKey = orig })

	f := &fakeDist{}
	srv := f.serve(t)
	base := srv.URL + "/im"
	oldBody, newBody := []byte("old-release"), []byte("new-release")
	// dist に置くのはリリースと同じ形: 名前 looptrack_<版>_<os>_<arch>・trusted comment "looptrack <版> SHA256SUMS"
	release := func(name, listed string, body []byte, trusted string) {
		f.listing = Listing{SumsURL: base + "/api/v1/dist/bin/SHA256SUMS", MinisigURL: base + "/api/v1/dist/bin/SHA256SUMS.minisig"}
		f.bins = nil
		f.add(base, name, listed, "linux", "arm64", body)
		f.sums = []byte(sum(body) + "  " + name + "\n")
		f.sig = minisignSign(priv, keyID, f.sums, trusted)
	}
	run := func(lang, local string, args ...string) (result, string) {
		t.Helper()
		exe := placeExe(t, "local-binary")
		home := t.TempDir()
		var out, errb bytes.Buffer
		code := Main(args, Options{
			Env:    env.FromMap(map[string]string{"LOOPTRACK_API_URL": base, "LOOPTRACK_TOKEN": "tok", "LOOPTRACK_LANG": lang, "HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config")}),
			Stdout: &out, Stderr: &errb, Version: local, GOOS: "linux", GOARCH: "arm64", Exe: exe,
		})
		return result{code, out.String(), errb.String()}, content(t, exe)
	}

	// 対照: 名前・trusted comment・version がそろい、手元より新しい → 置き換える
	release("looptrack_v1.1.0_linux_arm64", "v1.1.0", newBody, "looptrack v1.1.0 SHA256SUMS")
	if r, got := run("ja", "v1.0.0"); r.code != 0 || got != "new-release" || !strings.Contains(r.stdout, "v1.0.0 → v1.1.0") {
		t.Fatalf("対照（一致して新しい）で置き換わらない。前提が崩れている: %+v %q", r, got)
	}

	// 攻撃: 署名の正しい v1.0.0 の SHA256SUMS に、新しく見せかけた version v9.9.9 を組にした一覧 → 置き換えない（--force でも）
	release("looptrack_v1.0.0_linux_arm64", "v9.9.9", oldBody, "looptrack v1.0.0 SHA256SUMS")
	for _, args := range [][]string{nil, {"--force"}} {
		r, got := run("ja", "v1.1.0", args...)
		if r.code != 1 || got != "local-binary" || !strings.Contains(r.stderr, "配布の一覧の版 v9.9.9（linux/arm64）の名前ではありません") {
			t.Errorf("version の偽装 %v: %+v %q", args, r, got)
		}
	}
	r, got := run("en", "v1.1.0")
	if r.code != 1 || got != "local-binary" || !strings.Contains(r.stderr, "is not the name for the listed version v9.9.9 (linux/arm64)") {
		t.Errorf("version の偽装（en）: %+v %q", r, got)
	}
	// 名前も新しく見せかけると、リリースの trusted comment の版で止まる
	f.listing.Binaries[0].Name = "looptrack_v9.9.9_linux_arm64"
	if r, got := run("ja", "v1.1.0"); r.code != 1 || got != "local-binary" || !strings.Contains(r.stderr, "SHA256SUMS の署名に書かれた版 v1.0.0 が、配布の一覧の版 v9.9.9 と違います") {
		t.Errorf("名前の偽装（リリースの trusted comment）: %+v %q", r, got)
	}
	// trusted comment が版を持たない形（dist.sh sign-sums の既定）でも、署名された SHA256SUMS に載っていないので置き換えない
	release("looptrack_v1.0.0_linux_arm64", "v9.9.9", oldBody, "looptrack SHA256SUMS dist")
	f.listing.Binaries[0].Name = "looptrack_v9.9.9_linux_arm64"
	if r, got := run("ja", "v1.1.0"); r.code != 1 || got != "local-binary" || !strings.Contains(r.stderr, "署名された SHA256SUMS に looptrack_v9.9.9_linux_arm64 がありません") {
		t.Errorf("名前の偽装（版の無い trusted comment）: %+v %q", r, got)
	}
	f.listing.Binaries[0].Name = "looptrack_v1.0.0_linux_arm64"
	if r, got := run("ja", "v1.1.0"); r.code != 1 || got != "local-binary" || !strings.Contains(r.stderr, "配布の一覧の版 v9.9.9（linux/arm64）の名前ではありません") {
		t.Errorf("version の偽装（版の無い trusted comment）: %+v %q", r, got)
	}

	// trusted comment の版と一覧の version が食い違う → 置き換えない
	release("looptrack_v1.1.0_linux_arm64", "v1.1.0", newBody, "looptrack v1.0.0 SHA256SUMS")
	if r, got := run("ja", "v1.0.0"); r.code != 1 || got != "local-binary" || !strings.Contains(r.stderr, "SHA256SUMS の署名に書かれた版 v1.0.0 が、配布の一覧の版 v1.1.0 と違います") {
		t.Errorf("trusted comment の食い違い: %+v %q", r, got)
	}
	if r, got := run("en", "v1.0.0"); r.code != 1 || got != "local-binary" || !strings.Contains(r.stderr, "The version v1.0.0 in the SHA256SUMS signature differs from the listed version v1.1.0") {
		t.Errorf("trusted comment の食い違い（en）: %+v %q", r, got)
	}

	// 正直に古い版を配る一覧（署名・名前・version がそろう）: 手元より古いので置き換えない。戻すのは --force のときだけ
	release("looptrack_v1.0.0_linux_arm64", "v1.0.0", oldBody, "looptrack v1.0.0 SHA256SUMS")
	if r, got := run("ja", "v1.1.0"); r.code != 0 || got != "local-binary" || !strings.Contains(r.stdout, "最新です") {
		t.Errorf("古い版: %+v %q", r, got)
	}
	if r, got := run("ja", "v1.1.0", "--force"); r.code != 0 || got != "old-release" {
		t.Errorf("古い版 --force: %+v %q", r, got)
	}
}

// TestSignedVersion は signedVersion の判定: 名前の形（windows は .exe）と、trusted comment のうちリリースの形だけを見ること。
func TestSignedVersion(t *testing.T) {
	win := &Binary{Name: "looptrack_v1.1.0_windows_amd64.exe", OS: "windows", Arch: "amd64", Version: "v1.1.0"}
	lin := &Binary{Name: "looptrack_v1.1.0_linux_amd64", OS: "linux", Arch: "amd64", Version: "v1.1.0"}
	for _, c := range []struct {
		trusted string
		b       *Binary
		ok      bool
	}{
		{"looptrack v1.1.0 SHA256SUMS", win, true},
		{"looptrack v1.1.0 SHA256SUMS", lin, true},
		{"looptrack SHA256SUMS dist", lin, true},          // dist.sh sign-sums の既定（版を持たない）は名前だけで確かめる
		{"timestamp:1 file:SHA256SUMS hashed", lin, true}, // minisign の既定
		{"looptrack v1.0.0 SHA256SUMS", lin, false},       // リリースの形で版が違う
		{"looptrack v1.1.0 SHA256SUMS", &Binary{Name: "looptrack_v1.1.0_windows_amd64", OS: "windows", Arch: "amd64", Version: "v1.1.0"}, false}, // .exe が無い
		{"looptrack v1.1.0 SHA256SUMS", &Binary{Name: "looptrack_v1.1.0_linux_arm64", OS: "linux", Arch: "amd64", Version: "v1.1.0"}, false},     // CPU が違う
		{"t", &Binary{Name: "looptrack__linux_amd64", OS: "linux", Arch: "amd64"}, false},                                                        // 版が空
	} {
		if err := signedVersion(c.trusted, c.b); (err == nil) != c.ok {
			t.Errorf("%q %s: %v", c.trusted, c.b.Name, err)
		}
	}
}

// TestSelfUpdateFromReleaseArchive は、GitHub Releases の形（書庫 looptrack_<版>_<os>_<arch>_server.tar.gz と、書庫の行に加えて
// 書庫の中の実行ファイルを従来の名前にした行を持つ署名つき SHA256SUMS）から、書庫の中の looptrack を従来の名前で配布ディレクトリに
// 置けば、鍵を持つビルドの self-update が署名と SHA-256 を確かめて置き換えることを確かめる（SHA256SUMS は Releases のものをそのまま置く）。
// 対照として、書庫の行しか無い SHA256SUMS（従来の名前の行が無い）からは置き換えない。
func TestSelfUpdateFromReleaseArchive(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keyID := []byte{8, 7, 6, 5, 4, 3, 2, 1}
	orig := MinisignPublicKey
	MinisignPublicKey = base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), keyID...), pub...))
	t.Cleanup(func() { MinisignPublicKey = orig })

	// Releases の書庫（dist.sh archive と同じ並び: 最上位のディレクトリの下に looptrack・NOTICE・OFL・LICENSE）
	const ver, base0 = "v1.1.0", "looptrack_v1.1.0_linux_arm64_server"
	bin := []byte("release-binary-in-archive")
	var tgz bytes.Buffer
	gz := gzip.NewWriter(&tgz)
	tw := tar.NewWriter(gz)
	for _, e := range []struct {
		name string
		mode int64
		body []byte
	}{{base0 + "/LICENSE", 0o644, []byte("MIT")}, {base0 + "/NOTICE", 0o644, []byte("notices")},
		{base0 + "/OFL-BIZUDGothic.txt", 0o644, []byte("ofl")}, {base0 + "/looptrack", 0o755, bin}} {
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Mode: e.mode, Size: int64(len(e.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write(e.body)
	}
	tw.Close()
	gz.Close()
	archive := tgz.Bytes()

	// 配布ディレクトリに置く looptrack は書庫から取り出したもの
	zr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	var extracted []byte
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		if h.Name == base0+"/looptrack" {
			extracted, _ = io.ReadAll(tr)
		}
	}
	if !bytes.Equal(extracted, bin) {
		t.Fatalf("書庫から取り出せない: %q", extracted)
	}

	f := &fakeDist{}
	srv := f.serve(t)
	base := srv.URL + "/im"
	publish := func(sums string) {
		f.listing = Listing{SumsURL: base + "/api/v1/dist/bin/SHA256SUMS", MinisigURL: base + "/api/v1/dist/bin/SHA256SUMS.minisig"}
		f.bins = nil
		f.add(base, "looptrack_v1.1.0_linux_arm64", ver, "linux", "arm64", extracted)
		f.sums = []byte(sums)
		f.sig = minisignSign(priv, keyID, f.sums, "looptrack "+ver+" SHA256SUMS")
	}
	archiveLine := sum(archive) + "  " + base0 + ".tar.gz\n"
	rawLine := sum(bin) + "  looptrack_v1.1.0_linux_arm64\n"
	noticeLine := sum([]byte("notices")) + "  NOTICE\n"

	// 対照: 書庫の行しか無い SHA256SUMS からは置き換えない
	publish(noticeLine + archiveLine)
	exe := placeExe(t, "old")
	if r := selfUpdate(t, srv.URL, exe, "v1.0.0"); r.code != 1 || !strings.Contains(r.stderr, "署名された SHA256SUMS に looptrack_v1.1.0_linux_arm64 がありません") || content(t, exe) != "old" {
		t.Fatalf("対照（書庫の行だけ）で止まらない。前提が崩れている: %+v", r)
	}

	// Releases と同じ形（書庫の行 + 書庫の中の実行ファイルの行）: 置き換える
	publish(noticeLine + archiveLine + rawLine)
	exe = placeExe(t, "old")
	if r := selfUpdate(t, srv.URL, exe, "v1.0.0"); r.code != 0 || content(t, exe) != "release-binary-in-archive" || !strings.Contains(r.stdout, "v1.0.0 → v1.1.0") {
		t.Errorf("書庫から出した実行ファイルで置き換わらない: %+v %q", r, content(t, exe))
	}
}
