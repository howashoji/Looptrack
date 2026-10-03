package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/relsig"
	"github.com/howashoji/looptrack/internal/updatecheck"
)

// testKey はテスト用の minisign の鍵（その場で作り、どこにも書かない）。
type testKey struct {
	pub  string
	priv ed25519.PrivateKey
	id   []byte
}

func newTestKey(t *testing.T, id byte) testKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyID := []byte{id, 2, 3, 4, 5, 6, 7, 8}
	return testKey{pub: base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), keyID...), pub...)), priv: priv, id: keyID}
}

func (k testKey) sign(msg []byte, trusted string) []byte {
	return minisignSign(k.priv, k.id, msg, trusted)
}

// useKey は k を埋め込んだ公開鍵としてテストを流す。
func useKey(t *testing.T, k testKey) {
	t.Helper()
	orig := MinisignPublicKey
	MinisignPublicKey = k.pub
	t.Cleanup(func() { MinisignPublicKey = orig })
}

// entry は書庫の 1 項目。
type entry struct {
	name     string
	body     []byte
	symlink  bool
	hardlink bool // tar だけ（zip にはハードリンクの項目が無い）
}

func makeTarGz(t *testing.T, es []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range es {
		h := &tar.Header{Name: e.name, Mode: 0o755, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		if e.symlink {
			h = &tar.Header{Name: e.name, Mode: 0o777, Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}
		}
		if e.hardlink {
			h = &tar.Header{Name: e.name, Mode: 0o755, Typeflag: tar.TypeLink, Linkname: "/etc/passwd"}
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if !e.symlink && !e.hardlink {
			tw.Write(e.body)
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func makeZip(t *testing.T, es []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range es {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		mode := os.FileMode(0o755)
		if e.symlink {
			mode = os.ModeSymlink | 0o777
		}
		h.SetMode(mode)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(e.body)
	}
	zw.Close()
	return buf.Bytes()
}

// standardEntries は dist.sh archive と同じ並び（最上位のディレクトリの下に LICENSE・NOTICE・OFL・looptrack[.exe]）。
func standardEntries(tag, goos, arch string, bin []byte) []entry {
	base := strings.TrimSuffix(strings.TrimSuffix(relsig.ServerArchiveName(tag, goos, arch), ".tar.gz"), ".zip")
	exe := "looptrack"
	if goos == "windows" {
		exe += ".exe"
	}
	return []entry{{name: base + "/LICENSE", body: []byte("MIT")}, {name: base + "/NOTICE", body: []byte("notices")},
		{name: base + "/OFL-BIZUDGothic.txt", body: []byte("ofl")}, {name: base + "/" + exe, body: bin}}
}

// relOpts は壊したリリースを作るための指定（ゼロ値は正しいリリース）。
type relOpts struct {
	entries     []entry  // 書庫の中身（nil なら standardEntries）
	signWith    *testKey // 署名に使う鍵（nil なら正しい鍵）
	tamperSums  bool     // 署名の後で SHA256SUMS を書き換える
	rawSum      string   // 書庫の中の実行ファイルの行のハッシュ（空なら正しい値）
	swapArchive bool     // 署名の後で書庫を差し替える（実行ファイルは同じ。NOTICE だけ違う）
}

// fakeGitHub は GitHub の /releases と資産の取得を模す（TLS。実際の GitHub には繋がない）。受けた要求の道筋を数える。
type fakeGitHub struct {
	srv      *httptest.Server
	mu       sync.Mutex
	releases []map[string]any
	files    map[string][]byte
	paths    []string
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{files: map[string][]byte{}}
	g.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.paths = append(g.paths, r.URL.Path)
		rels, b, ok := g.releases, g.files[r.URL.Path], false
		if _, found := g.files[r.URL.Path]; found {
			ok = true
		}
		g.mu.Unlock()
		if r.URL.Path == "/releases" {
			json.NewEncoder(w).Encode(rels)
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGitHub) requests() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.paths...)
}

// add は署名つきのリリース（書庫・SHA256SUMS・.minisig）を足す。名前のついた資産のほかに、別の OS の書庫も 1 つ並べる。
func (g *fakeGitHub) add(t *testing.T, k testKey, tag string, prerelease bool, goos, arch string, bin []byte, o relOpts) {
	t.Helper()
	name := relsig.ServerArchiveName(tag, goos, arch)
	es := o.entries
	if es == nil {
		es = standardEntries(tag, goos, arch, bin)
	}
	var archive []byte
	if strings.HasSuffix(name, ".zip") {
		archive = makeZip(t, es)
	} else {
		archive = makeTarGz(t, es)
	}
	other := relsig.ServerArchiveName(tag, "linux", "amd64")
	if other == name {
		other = relsig.ServerArchiveName(tag, "darwin", "arm64")
	}
	otherBody := makeTarGz(t, standardEntries(tag, "linux", "amd64", []byte("other-os")))
	raw := o.rawSum
	if raw == "" {
		raw = sum(bin)
	}
	sums := []byte(sum(archive) + "  " + name + "\n" + raw + "  " + relsig.BinaryName(tag, goos, arch) + "\n" +
		sum(otherBody) + "  " + other + "\n")
	signer := k
	if o.signWith != nil {
		signer = *o.signWith
	}
	sig := signer.sign(sums, "looptrack "+tag+" SHA256SUMS")
	if o.tamperSums {
		sums = append(sums, []byte(strings.Repeat("0", 64)+"  extra\n")...)
	}
	if o.swapArchive {
		es := standardEntries(tag, goos, arch, bin)
		es[1].body = []byte("other notices")
		if strings.HasSuffix(name, ".zip") {
			archive = makeZip(t, es)
		} else {
			archive = makeTarGz(t, es)
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	var assets []map[string]any
	for n, b := range map[string][]byte{name: archive, other: otherBody, "SHA256SUMS": sums, "SHA256SUMS.minisig": sig} {
		p := "/d/" + tag + "_" + goos + "_" + arch + "/" + n
		g.files[p] = b
		assets = append(assets, map[string]any{"name": n, "browser_download_url": g.srv.URL + p, "size": len(b)})
	}
	g.releases = append(g.releases, map[string]any{"tag_name": tag, "prerelease": prerelease,
		"html_url": "https://example.invalid/releases/tag/" + tag, "assets": assets})
}

// addRaw はタグだけのリリース（資産なし。semver でないタグを並べる）。
func (g *fakeGitHub) addRaw(tag string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.releases = append(g.releases, map[string]any{"tag_name": tag, "prerelease": false})
}

// ghRun は GitHub の経路の self-update を流す（LOOPTRACK_API_URL は渡さない。確認先は偽の GitHub）。
type ghRun struct {
	g       *fakeGitHub
	version string
	goos    string
	arch    string
	env     map[string]string
	client  *http.Client
	install *ServerInstall
	lang    string
}

func (r ghRun) run(t *testing.T, exe string, args ...string) result {
	t.Helper()
	home := t.TempDir()
	lang := r.lang
	if lang == "" {
		lang = "ja"
	}
	m := map[string]string{"LOOPTRACK_LANG": lang, "HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config")}
	if r.g != nil {
		m["LOOPTRACK_UPDATE_URL"] = r.g.srv.URL + "/releases"
	}
	for k, v := range r.env {
		m[k] = v
	}
	hc := r.client
	if hc == nil && r.g != nil {
		hc = r.g.srv.Client()
	}
	goos, arch := r.goos, r.arch
	if goos == "" {
		goos, arch = "linux", "arm64"
	}
	inst := r.install
	if inst == nil {
		inst = &ServerInstall{} // 実際の /usr/local/bin・/etc は見ない
	}
	var out, errb bytes.Buffer
	code := Main(args, Options{Env: env.FromMap(m), Stdout: &out, Stderr: &errb, Version: r.version, GOOS: goos, GOARCH: arch,
		Exe: exe, Install: inst, HTTPClient: hc})
	return result{code, out.String(), errb.String()}
}

// countingTransport は要求を数え、どこにも繋がずに失敗させる（実際の GitHub に要求を出さないことを数えるため）。
type countingTransport struct {
	mu sync.Mutex
	n  int
}

func (c *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	return nil, errors.New("no network in tests")
}

func (c *countingTransport) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func hasLatest(paths []string) bool {
	for _, p := range paths {
		if strings.Contains(p, "/releases/latest") {
			return true
		}
	}
	return false
}

// TestSelfUpdateGitHub は URL の無いとき（と --from github）に GitHub のリリースの書庫から置き換えることを、tar.gz（linux・darwin）と
// zip（windows。replace の .old の分岐を通る）で確かめる。/releases/latest は呼ばない。
func TestSelfUpdateGitHub(t *testing.T) {
	k := newTestKey(t, 1)
	useKey(t, k)
	for _, c := range []struct{ goos, arch, ext string }{{"linux", "arm64", ".tar.gz"}, {"darwin", "amd64", ".tar.gz"}, {"windows", "amd64", ".zip"}} {
		t.Run(c.goos+"_"+c.arch, func(t *testing.T) {
			g := newFakeGitHub(t)
			bin := []byte("new-" + c.goos)
			g.add(t, k, "v1.1.0", false, c.goos, c.arch, bin, relOpts{})
			if !strings.HasSuffix(relsig.ServerArchiveName("v1.1.0", c.goos, c.arch), c.ext) {
				t.Fatalf("前提が崩れています: 書庫の拡張子が %s でない", c.ext)
			}
			exe := placeExe(t, "old")
			r := ghRun{g: g, version: "v1.0.0", goos: c.goos, arch: c.arch}.run(t, exe)
			if r.code != 0 || content(t, exe) != string(bin) || !strings.Contains(r.stdout, "v1.0.0 → v1.1.0") {
				t.Fatalf("置き換わらない: %+v %q", r, content(t, exe))
			}
			if c.goos == "windows" {
				// Windows の分岐: 実行中の exe を .old に退けてから置く（Windows の実行時は CI の windows のジョブで確かめる）
				if content(t, exe+".old") != "old" {
					t.Errorf(".old に元のものが無い: %v", onlyFile(t, filepath.Dir(exe)))
				}
			} else if names := onlyFile(t, filepath.Dir(exe)); len(names) != 1 {
				t.Errorf("一時ファイルが残った: %v", names)
			}
			if hasLatest(g.requests()) {
				t.Errorf("/releases/latest を呼んだ: %v", g.requests())
			}
			// --from github は LOOPTRACK_API_URL があっても GitHub から取る（サーバには行かない）
			exe = placeExe(t, "old")
			r = ghRun{g: g, version: "v1.0.0", goos: c.goos, arch: c.arch, env: map[string]string{"LOOPTRACK_API_URL": "https://example.invalid/im"}}.run(t, exe, "--from", "github")
			if r.code != 0 || content(t, exe) != string(bin) {
				t.Errorf("--from github: %+v", r)
			}
		})
	}
}

// TestSelfUpdateSource は取得元の決め方: --url か LOOPTRACK_API_URL があり --from が無ければサーバ（GitHub には要求を出さない）、
// --from github と --url は同時に渡せない（終了コード 2・どこにも要求を出さない）、--from の値の誤りも 2。
// 「GitHub への要求 0 件」が鍵の無いビルドの早期の停止で 0 になっただけでないよう、鍵を持つビルドでも同じことを確かめ、
// 同じ偽の GitHub が URL の無いときには要求を受けること（数え方の対照）も置く。
func TestSelfUpdateSource(t *testing.T) {
	k := newTestKey(t, 1)
	for _, keyed := range []bool{false, true} {
		t.Run(map[bool]string{false: "鍵なし", true: "鍵あり"}[keyed], func(t *testing.T) {
			if keyed {
				useKey(t, k)
			} else {
				withoutKey(t)
			}
			g := newFakeGitHub(t)
			g.add(t, k, "v1.1.0", false, "linux", "arm64", []byte("from-github"), relOpts{})
			f := &fakeDist{}
			srv := f.serve(t)
			base := srv.URL + "/im"
			f.add(base, "looptrack_v1.1.0_linux_arm64", "v1.1.0", "linux", "arm64", []byte("from-server"))
			// 鍵ありのビルドは署名の無いこの配布からは置き換えない（サーバの経路を通ったことは、配布の一覧への要求で見る）
			wantCode, wantBody := 0, "from-server"
			if keyed {
				wantCode, wantBody = 1, "old"
			}

			// LOOPTRACK_API_URL があればサーバ（GitHub の確認先を渡してあっても）
			exe := placeExe(t, "old")
			n := len(f.gets)
			r := ghRun{g: g, version: "v1.0.0", env: map[string]string{"LOOPTRACK_API_URL": base, "LOOPTRACK_TOKEN": "tok"}}.run(t, exe)
			if r.code != wantCode || content(t, exe) != wantBody || len(g.requests()) != 0 || len(f.gets) == n {
				t.Fatalf("環境変数でサーバ: %+v GitHub への要求 %v サーバへの要求 %v", r, g.requests(), f.gets[n:])
			}
			// --url でもサーバ
			exe = placeExe(t, "old")
			n = len(f.gets)
			r = ghRun{g: g, version: "v1.0.0", env: map[string]string{"LOOPTRACK_TOKEN": "tok"}}.run(t, exe, "--url", base)
			if r.code != wantCode || content(t, exe) != wantBody || len(g.requests()) != 0 || len(f.gets) == n {
				t.Fatalf("--url でサーバ: %+v GitHub への要求 %v", r, g.requests())
			}
			// --from server は URL が無ければ止まる（GitHub へは切り替えない）
			r = ghRun{g: g, version: "v1.0.0"}.run(t, exe, "--from", "server")
			if r.code != 1 || !strings.Contains(r.stderr, "サーバの URL がありません") || len(g.requests()) != 0 {
				t.Errorf("--from server・URL なし: %+v %v", r, g.requests())
			}
			before := len(f.gets)
			for _, args := range [][]string{{"--from", "github", "--url", base}, {"--url=" + base, "--from=github"}, {"--from", "gitlab"}} {
				exe := placeExe(t, "old")
				r := ghRun{g: g, version: "v1.0.0", env: map[string]string{"LOOPTRACK_TOKEN": "tok"}}.run(t, exe, args...)
				if r.code != 2 || content(t, exe) != "old" || !strings.Contains(r.stderr, "使い方") {
					t.Errorf("%v: %+v", args, r)
				}
			}
			if len(g.requests()) != 0 || len(f.gets) != before {
				t.Errorf("引数の誤りで要求を出した: GitHub %v サーバ %v", g.requests(), f.gets[before:])
			}
			if !keyed {
				return
			}
			// 数え方の対照: URL が無ければ同じ偽の GitHub が要求を受け、置き換わる
			exe = placeExe(t, "old")
			r = ghRun{g: g, version: "v1.0.0"}.run(t, exe)
			if r.code != 0 || len(g.requests()) == 0 || content(t, exe) != "from-github" {
				t.Fatalf("対照（URL なしで GitHub）が要求を受けない。数え方の前提が崩れています: %+v %v", r, g.requests())
			}
		})
	}
}

// TestSelfUpdateGitHubSignature は、別の鍵の .minisig・改ざんした SHA256SUMS・書庫の中身と合わない行のどれでも置き換えないことを、
// 同じテストの中の正しい署名なら置き換わる対照と並べて確かめる。
func TestSelfUpdateGitHubSignature(t *testing.T) {
	k := newTestKey(t, 1)
	other := newTestKey(t, 9)
	useKey(t, k)
	bin := []byte("signed-binary")

	g := newFakeGitHub(t)
	g.add(t, k, "v1.1.0", false, "linux", "arm64", bin, relOpts{})
	exe := placeExe(t, "old")
	if r := (ghRun{g: g, version: "v1.0.0"}).run(t, exe); r.code != 0 || content(t, exe) != string(bin) {
		t.Fatalf("対照（正しい署名）で置き換わらない。前提が崩れています: %+v", r)
	}

	for _, c := range []struct {
		name string
		o    relOpts
		want string
	}{
		{"別の鍵", relOpts{signWith: &other}, "鍵 ID"},
		{"SHA256SUMS の改ざん", relOpts{tamperSums: true}, "署名が一致しません"},
		{"書庫の中身と合わない行", relOpts{rawSum: strings.Repeat("ab", 32)}, "署名された SHA256SUMS の looptrack_v1.1.0_linux_arm64 の行と一致しません"},
		{"差し替えた書庫", relOpts{swapArchive: true}, "SHA-256 が署名された SHA256SUMS と一致しません"},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := newFakeGitHub(t)
			g.add(t, k, "v1.1.0", false, "linux", "arm64", bin, c.o)
			exe := placeExe(t, "old")
			r := ghRun{g: g, version: "v1.0.0"}.run(t, exe)
			if r.code != 1 || content(t, exe) != "old" || !strings.Contains(r.stderr, c.want) {
				t.Errorf("%+v %q", r, content(t, exe))
			}
			if names := onlyFile(t, filepath.Dir(exe)); len(names) != 1 {
				t.Errorf("一時ファイルが残った: %v", names)
			}
		})
	}
}

// TestSelfUpdateGitHubNoKey は公開鍵の無いビルドが、確認先が GitHub のとき要求を 1 件も出さずに止まり、確認先を差し替えても
// 置き換えないことを確かめる。対照として、鍵を持つビルドは同じ確認先に要求を出して置き換える。
func TestSelfUpdateGitHubNoKey(t *testing.T) {
	k := newTestKey(t, 1)
	g := newFakeGitHub(t)
	g.add(t, k, "v1.1.0", false, "linux", "arm64", []byte("new"), relOpts{})

	t.Run("鍵なし", func(t *testing.T) {
		withoutKey(t)
		// 既定の確認先（api.github.com）。要求は数えるだけでどこにも繋がない
		ct := &countingTransport{}
		exe := placeExe(t, "old")
		r := ghRun{version: "v1.0.0", client: &http.Client{Transport: ct}}.run(t, exe)
		if r.code != 1 || ct.count() != 0 || content(t, exe) != "old" || !strings.Contains(r.stderr, "公開鍵を持たない") {
			t.Errorf("既定の確認先: %+v 要求 %d 件", r, ct.count())
		}
		// LOOPTRACK_UPDATE_URL で差し替えても署名を確かめられないので置き換えない（要求も出さない）
		r = ghRun{g: g, version: "v1.0.0"}.run(t, exe)
		if r.code != 1 || len(g.requests()) != 0 || content(t, exe) != "old" {
			t.Errorf("差し替えた確認先: %+v 要求 %v", r, g.requests())
		}
		r = ghRun{g: g, version: "v1.0.0"}.run(t, exe, "--check")
		if r.code != 1 || len(g.requests()) != 0 {
			t.Errorf("--check: %+v 要求 %v", r, g.requests())
		}
	})
	t.Run("鍵あり（対照）", func(t *testing.T) {
		useKey(t, k)
		// 数え方の対照: 鍵ありでは同じ数える transport が要求を数える（どこにも繋がないので取得の失敗で止まる）
		ct := &countingTransport{}
		exe := placeExe(t, "old")
		if r := (ghRun{version: "v1.0.0", client: &http.Client{Transport: ct}}).run(t, exe); r.code != 1 || ct.count() == 0 || content(t, exe) != "old" {
			t.Fatalf("数える transport が要求を数えない。前提が崩れています: %+v 要求 %d 件", r, ct.count())
		}
		r := ghRun{g: g, version: "v1.0.0"}.run(t, exe)
		if r.code != 0 || len(g.requests()) == 0 || content(t, exe) != "new" {
			t.Errorf("鍵あり: %+v 要求 %v", r, g.requests())
		}
	})
}

// TestSelfUpdateGitHubOnlyNewer は今の版より新しい版があるときだけ置き換え、GitHub の経路では --force を受けない（終了コード 2）ことを確かめる。
func TestSelfUpdateGitHubOnlyNewer(t *testing.T) {
	k := newTestKey(t, 1)
	useKey(t, k)
	g := newFakeGitHub(t)
	g.add(t, k, "v1.0.0", false, "linux", "arm64", []byte("v1.0.0-bin"), relOpts{})
	g.add(t, k, "v0.9.0", false, "linux", "arm64", []byte("v0.9.0-bin"), relOpts{})

	// 古い版・同じ版しか無い: 置き換えない
	exe := placeExe(t, "current")
	r := ghRun{g: g, version: "v1.0.0"}.run(t, exe)
	if r.code != 0 || content(t, exe) != "current" || !strings.Contains(r.stdout, "最新です") {
		t.Fatalf("古い版だけ: %+v", r)
	}
	// --force は 2（要求も出さない）
	n := len(g.requests())
	if r := (ghRun{g: g, version: "v1.0.0"}).run(t, exe, "--force"); r.code != 2 || content(t, exe) != "current" || len(g.requests()) != n ||
		!strings.Contains(r.stderr, "--force を使えません") {
		t.Errorf("--force: %+v", r)
	}
	if r := (ghRun{g: g, version: "v1.0.0"}).run(t, exe, "--from", "github", "--force"); r.code != 2 {
		t.Errorf("--from github --force: %+v", r)
	}
	// 対照: 新しい版が足されたら置き換える
	g.add(t, k, "v1.0.1", false, "linux", "arm64", []byte("v1.0.1-bin"), relOpts{})
	r = ghRun{g: g, version: "v1.0.0"}.run(t, exe)
	if r.code != 0 || content(t, exe) != "v1.0.1-bin" {
		t.Errorf("新しい版: %+v", r)
	}
	// 比べられない版（dev）は置き換えない
	exe = placeExe(t, "dev-bin")
	if r := (ghRun{g: g, version: "dev"}).run(t, exe); r.code != 1 || content(t, exe) != "dev-bin" || !strings.Contains(r.stderr, "比べられない") {
		t.Errorf("dev: %+v", r)
	}
}

// TestSelfUpdateGitHubChannel は、今の版が rc なら rc も選び、正式版なら選ばないこと・LOOPTRACK_UPDATE_CHANNEL で変わること・
// semver でないタグを選ばず /releases/latest を呼ばないことを確かめる。
func TestSelfUpdateGitHubChannel(t *testing.T) {
	k := newTestKey(t, 1)
	useKey(t, k)
	g := newFakeGitHub(t)
	g.add(t, k, "v1.1.0", false, "linux", "arm64", []byte("v1.1.0-bin"), relOpts{})
	g.add(t, k, "v1.2.0-rc.1", true, "linux", "arm64", []byte("v1.2.0-rc.1-bin"), relOpts{})
	g.addRaw("appimage-runtime-source-20251108")
	g.addRaw("v9")
	for _, c := range []struct {
		current, channel, want string
	}{
		{"v1.0.0", "", "v1.1.0"},
		{"v1.1.0-rc.1", "", "v1.2.0-rc.1"},
		{"v1.0.0", "prerelease", "v1.2.0-rc.1"},
		{"v1.1.0-rc.1", "stable", "v1.1.0"},
	} {
		exe := placeExe(t, "old")
		r := ghRun{g: g, version: c.current, env: map[string]string{"LOOPTRACK_UPDATE_CHANNEL": c.channel}}.run(t, exe)
		if r.code != 0 || content(t, exe) != c.want+"-bin" {
			t.Errorf("%s・channel=%q: %+v %q", c.current, c.channel, r, content(t, exe))
		}
	}
	if hasLatest(g.requests()) {
		t.Errorf("/releases/latest を呼んだ: %v", g.requests())
	}
	// 正式版で追える新しい版が無い（rc だけ）: 置き換えない
	exe := placeExe(t, "current")
	if r := (ghRun{g: g, version: "v1.1.0"}).run(t, exe); r.code != 0 || content(t, exe) != "current" || !strings.Contains(r.stdout, "最新です") {
		t.Errorf("正式版は rc を選ばない: %+v", r)
	}
}

// TestSelfUpdateGitHubCheckOff は LOOPTRACK_UPDATE_CHECK=off なら GitHub に要求を出さずに止まり、サーバの経路は影響を受けないことを確かめる。
func TestSelfUpdateGitHubCheckOff(t *testing.T) {
	k := newTestKey(t, 1)
	useKey(t, k)
	g := newFakeGitHub(t)
	g.add(t, k, "v1.1.0", false, "linux", "arm64", []byte("new"), relOpts{})
	off := map[string]string{"LOOPTRACK_UPDATE_CHECK": "off"}

	exe := placeExe(t, "old")
	r := ghRun{g: g, version: "v1.0.0", env: off}.run(t, exe)
	if r.code != 1 || len(g.requests()) != 0 || content(t, exe) != "old" || !strings.Contains(r.stderr, "LOOPTRACK_UPDATE_CHECK=off") {
		t.Errorf("off: %+v 要求 %v", r, g.requests())
	}
	// 対照: off でなければ要求を出して置き換える
	r = ghRun{g: g, version: "v1.0.0"}.run(t, exe)
	if r.code != 0 || len(g.requests()) == 0 || content(t, exe) != "new" {
		t.Errorf("対照: %+v", r)
	}
	// サーバの経路は off に影響されない
	withoutKey(t)
	f := &fakeDist{}
	srv := f.serve(t)
	base := srv.URL + "/im"
	f.add(base, "looptrack_v1.1.0_linux_arm64", "v1.1.0", "linux", "arm64", []byte("from-server"))
	exe = placeExe(t, "old")
	r = ghRun{version: "v1.0.0", env: map[string]string{"LOOPTRACK_UPDATE_CHECK": "off", "LOOPTRACK_API_URL": base, "LOOPTRACK_TOKEN": "tok"}}.run(t, exe)
	if r.code != 0 || content(t, exe) != "from-server" {
		t.Errorf("サーバの経路: %+v", r)
	}
}

// TestSelfUpdateGitHubCheckOnly は --check が GitHub の経路でも置き換えず（書庫も取らない）、新しい版の有無だけを出すことを確かめる。
func TestSelfUpdateGitHubCheckOnly(t *testing.T) {
	k := newTestKey(t, 1)
	useKey(t, k)
	g := newFakeGitHub(t)
	g.add(t, k, "v1.1.0", false, "linux", "arm64", []byte("new"), relOpts{})
	exe := placeExe(t, "old")
	r := ghRun{g: g, version: "v1.0.0"}.run(t, exe, "--check")
	if r.code != 0 || content(t, exe) != "old" || !strings.Contains(r.stdout, "更新があります: looptrack v1.0.0 → v1.1.0") {
		t.Errorf("--check: %+v", r)
	}
	for _, p := range g.requests() {
		if strings.HasSuffix(p, ".tar.gz") {
			t.Errorf("--check で書庫を取った: %v", g.requests())
		}
	}
	r = ghRun{g: g, version: "v1.1.0"}.run(t, exe, "--check")
	if r.code != 0 || content(t, exe) != "old" || !strings.Contains(r.stdout, "最新です") {
		t.Errorf("--check（最新）: %+v", r)
	}
	// 英語の文面（言語を固定して見る）
	r = ghRun{g: g, version: "v1.0.0", lang: "en"}.run(t, exe, "--check")
	if r.code != 0 || !strings.Contains(r.stdout, "Update available: looptrack v1.0.0 -> v1.1.0 (GitHub releases") {
		t.Errorf("--check（en）: %+v", r)
	}
}

// TestExtract は書庫から <base>/looptrack[.exe] だけを取り出し、.. を含む名前・別名・上限を超える大きさ・ふつうでないファイル・
// 2 つ目を拒むことを、tar.gz と zip の両方で確かめる。対照として、正しい書庫からは取り出せる。
func TestExtract(t *testing.T) {
	for _, c := range []struct{ goos, arch string }{{"linux", "amd64"}, {"windows", "arm64"}} {
		name := relsig.ServerArchiveName("v1.1.0", c.goos, c.arch)
		inner := innerName(name, c.goos)
		mk := func(es []entry) []byte {
			if strings.HasSuffix(name, ".zip") {
				return makeZip(t, es)
			}
			return makeTarGz(t, es)
		}
		base := strings.TrimSuffix(inner, "/looptrack"+map[bool]string{true: ".exe", false: ""}[c.goos == "windows"])
		t.Run(c.goos, func(t *testing.T) {
			// 対照: 正しい書庫
			got, err := extract(mk(standardEntries("v1.1.0", c.goos, c.arch, []byte("bin"))), name, inner)
			if err != nil || string(got) != "bin" {
				t.Fatalf("対照で取り出せない。前提が崩れています: %q %v", got, err)
			}
			for _, k := range []struct {
				label string
				es    []entry
				want  string
			}{
				{"別名だけ", []entry{{name: base + "/looptrack-other", body: []byte("x")}, {name: "other/" + strings.TrimPrefix(inner, base+"/"), body: []byte("x")},
					{name: "./" + inner, body: []byte("x")}}, "selfupdate.err.archive_no_binary"},
				{"..", []entry{{name: base + "/../" + strings.TrimPrefix(inner, base+"/"), body: []byte("x")}, {name: inner, body: []byte("bin")}}, "selfupdate.err.archive_entry"},
				{"..（後ろ）", []entry{{name: inner, body: []byte("bin")}, {name: "../evil", body: []byte("x")}}, "selfupdate.err.archive_entry"},
				{"シンボリックリンク", []entry{{name: inner, symlink: true}}, "selfupdate.err.archive_not_regular"},
				{"2 つ目", []entry{{name: inner, body: []byte("a")}, {name: inner, body: []byte("b")}}, "selfupdate.err.archive_dup"},
				{"大文字小文字の違い", []entry{{name: strings.ToUpper(inner), body: []byte("x")}}, "selfupdate.err.archive_no_binary"},
				{"大文字小文字の違い（base）", []entry{{name: strings.ToUpper(base) + strings.TrimPrefix(inner, base), body: []byte("x")}}, "selfupdate.err.archive_no_binary"},
				{"絶対パス", []entry{{name: "/" + inner, body: []byte("x")}}, "selfupdate.err.archive_no_binary"},
				{"\\ 区切り", []entry{{name: strings.ReplaceAll(inner, "/", "\\"), body: []byte("x")}}, "selfupdate.err.archive_no_binary"},
				{"\\ 区切りの ..", []entry{{name: inner, body: []byte("bin")}, {name: base + "\\..\\evil", body: []byte("x")}}, "selfupdate.err.archive_entry"},
			} {
				_, err := extract(mk(k.es), name, inner)
				var ie *i18n.Error
				if !errors.As(err, &ie) || ie.Msg.ID != k.want {
					t.Errorf("%s: %v（期待 %s）", k.label, err, k.want)
				}
			}
			// ハードリンク（tar だけ。zip にはハードリンクの項目が無い）
			if strings.HasSuffix(name, ".tar.gz") {
				_, err := extract(mk([]entry{{name: inner, hardlink: true}}), name, inner)
				var ie *i18n.Error
				if !errors.As(err, &ie) || ie.Msg.ID != "selfupdate.err.archive_not_regular" {
					t.Errorf("ハードリンク: %v", err)
				}
			}
			// 上限を超える大きさ
			orig := maxBinaryBytes
			maxBinaryBytes = 4
			defer func() { maxBinaryBytes = orig }()
			_, err = extract(mk([]entry{{name: inner, body: []byte("12345")}}), name, inner)
			var ie *i18n.Error
			if !errors.As(err, &ie) || ie.Msg.ID != "selfupdate.err.binary_too_large" {
				t.Errorf("上限: %v", err)
			}
			if got, err := extract(mk([]entry{{name: inner, body: []byte("1234")}}), name, inner); err != nil || string(got) != "1234" {
				t.Errorf("上限ちょうど: %q %v", got, err)
			}
		})
	}
}

// TestSelfUpdateGitHubBadArchive は署名の正しいリリースでも、書庫の中身が取り出しの規則に合わなければ置き換えないことを、
// 書庫を通しで流して確かめる（.. を含む名前・取り出す名前が無い・上限を超える）。
func TestSelfUpdateGitHubBadArchive(t *testing.T) {
	k := newTestKey(t, 1)
	useKey(t, k)
	base := "looptrack_v1.1.0_linux_arm64_server"
	for _, c := range []struct {
		label string
		es    []entry
		limit int64
		want  string
	}{
		{"..", []entry{{name: base + "/looptrack", body: []byte("bin")}, {name: base + "/../looptrack", body: []byte("evil")}}, 0, ".. を含む名前"},
		{"別名", []entry{{name: "looptrack", body: []byte("bin")}}, 0, "がありません"},
		{"上限", []entry{{name: base + "/looptrack", body: []byte("bin")}}, 2, "上限"},
	} {
		t.Run(c.label, func(t *testing.T) {
			if c.limit > 0 {
				orig := maxBinaryBytes
				maxBinaryBytes = c.limit
				t.Cleanup(func() { maxBinaryBytes = orig })
			}
			g := newFakeGitHub(t)
			g.add(t, k, "v1.1.0", false, "linux", "arm64", []byte("bin"), relOpts{entries: c.es})
			exe := placeExe(t, "old")
			r := ghRun{g: g, version: "v1.0.0"}.run(t, exe)
			if r.code != 1 || content(t, exe) != "old" || !strings.Contains(r.stderr, c.want) {
				t.Errorf("%+v", r)
			}
		})
	}
	// 書庫そのものが上限を超える
	orig := maxArchiveBytes
	maxArchiveBytes = 16
	t.Cleanup(func() { maxArchiveBytes = orig })
	g := newFakeGitHub(t)
	g.add(t, k, "v1.1.0", false, "linux", "arm64", []byte("bin"), relOpts{})
	exe := placeExe(t, "old")
	if r := (ghRun{g: g, version: "v1.0.0"}).run(t, exe); r.code != 1 || content(t, exe) != "old" || !strings.Contains(r.stderr, "上限") {
		t.Errorf("書庫の上限: %+v", r)
	}
}

// TestSelfUpdateGitHubRefusesAppAndServer は .app の中と install.sh で入れたサーバを GitHub の経路でも置き換えない
// （--check は動く）ことを確かめる。
func TestSelfUpdateGitHubRefusesAppAndServer(t *testing.T) {
	k := newTestKey(t, 1)
	useKey(t, k)
	g := newFakeGitHub(t)
	g.add(t, k, "v1.1.0", false, "linux", "arm64", []byte("new"), relOpts{})
	gm := newFakeGitHub(t)
	gm.add(t, k, "v1.1.0", false, "darwin", "arm64", []byte("new"), relOpts{})

	app := filepath.Join(t.TempDir(), "Looptrack.app", "Contents", "MacOS")
	os.MkdirAll(app, 0o755)
	exe := filepath.Join(app, "looptrack")
	os.WriteFile(exe, []byte("old"), 0o755)
	if r := (ghRun{g: gm, version: "v1.0.0", goos: "darwin", arch: "arm64"}).run(t, exe); r.code != 1 || content(t, exe) != "old" || !strings.Contains(r.stderr, "アプリ") {
		t.Errorf(".app: %+v", r)
	}

	inst, bin := fakeInstall(t, "etc/looptrack/install.conf")
	if r := (ghRun{g: g, version: "v1.0.0", install: &inst}).run(t, bin); r.code != 1 || content(t, bin) == "new" || !strings.Contains(r.stderr, "--upgrade") {
		t.Errorf("install.sh のサーバ: %+v", r)
	}
	if r := (ghRun{g: g, version: "v1.0.0", install: &inst}).run(t, bin, "--check"); r.code != 0 || !strings.Contains(r.stdout, "更新があります") {
		t.Errorf("install.sh のサーバの --check: %+v", r)
	}
	// 対照: 同じ置き場でも設定が無ければ置き換える
	inst2, bin2 := fakeInstall(t)
	if r := (ghRun{g: g, version: "v1.0.0", install: &inst2}).run(t, bin2); r.code != 0 || content(t, bin2) != "new" {
		t.Errorf("対照: %+v", r)
	}
}

// TestFetchArchiveHTTPSOnly は書庫を https からだけ取ること（http の URL と、https から http への転送を拒む）を確かめる。
// 渡したクライアント（テストの TLS のクライアント）にも転送の制限が掛かることを見る。対照として https のままの転送なら取れる。
func TestFetchArchiveHTTPSOnly(t *testing.T) {
	body := []byte("archive-body")
	var plainHits int
	var mu sync.Mutex
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		plainHits++
		mu.Unlock()
		w.Write(body)
	}))
	t.Cleanup(plain.Close)
	var tlsSrv *httptest.Server
	tlsSrv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a":
			w.Write(body)
		case "/to-https":
			http.Redirect(w, r, tlsSrv.URL+"/a", http.StatusFound)
		case "/to-http":
			http.Redirect(w, r, plain.URL+"/a", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(tlsSrv.Close)
	asset := func(u string) *updatecheck.Asset {
		return &updatecheck.Asset{Name: "looptrack_v1.1.0_linux_arm64_server.tar.gz", URL: u, SHA256: sum(body)}
	}
	idOf := func(err error) string {
		var ie *i18n.Error
		if errors.As(err, &ie) {
			return ie.Msg.ID
		}
		return ""
	}
	ctx := context.Background()

	// 対照: https のまま（転送も https）なら取れる
	for _, p := range []string{"/a", "/to-https"} {
		if got, err := fetchArchive(ctx, tlsSrv.Client(), asset(tlsSrv.URL+p)); err != nil || string(got) != string(body) {
			t.Fatalf("対照（%s）で取れない。前提が崩れています: %q %v", p, got, err)
		}
	}
	// http の URL は要求を出さずに拒む
	if _, err := fetchArchive(ctx, plain.Client(), asset(plain.URL+"/a")); idOf(err) != "selfupdate.err.not_https" {
		t.Errorf("http の URL: %v", err)
	}
	// https から http への転送は拒む（転送先には行かない）
	if _, err := fetchArchive(ctx, tlsSrv.Client(), asset(tlsSrv.URL+"/to-http")); err == nil || !strings.Contains(i18n.Text(i18n.JA, err), "https でない配布先からは取りません") {
		t.Errorf("http への転送: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if plainHits != 0 {
		t.Errorf("http の配布先に %d 件の要求を出した", plainHits)
	}
}

// TestExtractUnknownType は扱えない形の書庫の名前を、言語ごとの文面で拒むことを確かめる。
func TestExtractUnknownType(t *testing.T) {
	_, err := extract([]byte("x"), "looptrack_v1.1.0_linux_arm64_server.rar", "looptrack_v1.1.0_linux_arm64_server/looptrack")
	var ie *i18n.Error
	if !errors.As(err, &ie) || ie.Msg.ID != "selfupdate.err.archive_type" {
		t.Fatalf("%v", err)
	}
	if ja, en := i18n.Text(i18n.JA, err), i18n.Text(i18n.EN, err); !strings.Contains(ja, "扱えない形の書庫") || !strings.Contains(en, "not an archive type") {
		t.Errorf("文面: %q / %q", ja, en)
	}
}
