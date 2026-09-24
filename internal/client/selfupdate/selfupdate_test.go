package selfupdate

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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

// TestSelfUpdateInstalledServer は install.sh で入れたサーバの self-update: 置き換えずに install.sh --upgrade を案内する。--check は動く。
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
		if r := run(s, bin, args...); r.code != 1 || content(t, bin) != "old-binary" || !strings.Contains(r.stderr, "install.sh --upgrade") {
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
