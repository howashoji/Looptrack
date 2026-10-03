package updatecheck

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/blake2b"

	"github.com/howashoji/looptrack/internal/i18n"
)

// realList は公開リポジトリの GitHub の実物の一覧（2026-09-25 に取得。DefaultURL の応答）と同じ形の 3 件。
// rc.2 は Latest として公開した（prerelease=false）、rc.1 は Pre-release、AppImage の runtime のソースは semver でないタグ。
const realList = `[
 {"tag_name":"v1.0.0-rc.2","draft":false,"prerelease":false,"html_url":"https://github.com/howashoji/looptrack/releases/tag/v1.0.0-rc.2","published_at":"2026-09-25T03:42:59Z","assets":[]},
 {"tag_name":"v1.0.0-rc.1","draft":false,"prerelease":true,"assets":[]},
 {"tag_name":"appimage-runtime-source-20251108","draft":false,"prerelease":false,"assets":[{"name":"SHA256SUMS","browser_download_url":"https://example.invalid/SHA256SUMS"}]}
]`

func parseList(t *testing.T, s string) []Release {
	t.Helper()
	var l []Release
	if err := json.Unmarshal([]byte(s), &l); err != nil {
		t.Fatal(err)
	}
	return l
}

func tag(r *Release) string {
	if r == nil {
		return "<nil>"
	}
	return r.Tag
}

// TestSelect は一覧から semver のタグだけを選び、下書きを除き、チャンネルでプレリリースを扱い分けることを確かめる。
func TestSelect(t *testing.T) {
	real := parseList(t, realList)
	// 実物の形: prerelease は rc.2 を選ぶ。stable は候補が無い（rc は Latest でもプレリリースの版なので除く。semver でないタグも除く）
	if got := Select(real, Prerelease); tag(got) != "v1.0.0-rc.2" {
		t.Errorf("prerelease（実物）: %s", tag(got))
	}
	if got := Select(real, Stable); got != nil {
		t.Errorf("stable（実物）: rc か semver でないタグを選んだ: %s", tag(got))
	}

	list := append(parseList(t, realList), parseList(t, `[
	 {"tag_name":"v1.0.1","draft":true,"prerelease":false},
	 {"tag_name":"v0.9.0","prerelease":false},
	 {"tag_name":"v0.9.5","prerelease":true},
	 {"tag_name":"v9.9","prerelease":false},
	 {"tag_name":"9.0.0","prerelease":false}
	]`)...)
	// 下書き（v1.0.1）・semver でない v9.9・v で始まらない 9.0.0・GitHub の印がプレリリースの v0.9.5 を除いて v0.9.0
	if got := Select(list, Stable); tag(got) != "v0.9.0" {
		t.Errorf("stable: %s", tag(got))
	}
	if got := Select(list, Prerelease); tag(got) != "v1.0.0-rc.2" {
		t.Errorf("prerelease: %s", tag(got))
	}
	// 対照: 下書きでなければ v1.0.1 が選ばれる（下書きを除く判定が効いていることの前提）
	for i := range list {
		if list[i].Tag == "v1.0.1" {
			list[i].Draft = false
		}
	}
	if got := Select(list, Stable); tag(got) != "v1.0.1" {
		t.Fatalf("対照（下書きでない v1.0.1）が選ばれない。前提が崩れている: %s", tag(got))
	}
}

// TestResolve は設定の決め方を確かめる: チャンネルの既定は今の版・環境変数 > 控え・どちらかの off で止める・分からない値は誤り。
func TestResolve(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	for _, c := range []struct {
		name    string
		env     map[string]string
		current string
		p       Prefs
		want    Settings
		errID   string
	}{
		{"rc は rc も追う", nil, "v1.0.0-rc.2", Prefs{}, Settings{Enabled: true, Channel: Prerelease, URL: DefaultURL}, ""},
		{"正式版は正式版だけ", nil, "v1.0.0", Prefs{}, Settings{Enabled: true, Channel: Stable, URL: DefaultURL}, ""},
		{"比べられない版は stable", nil, "dev", Prefs{}, Settings{Enabled: true, Channel: Stable, URL: DefaultURL}, ""},
		{"控えのチャンネル", nil, "v1.0.0", Prefs{Channel: Prerelease}, Settings{Enabled: true, Channel: Prerelease, URL: DefaultURL}, ""},
		{"環境変数が控えに勝つ", map[string]string{EnvChannel: "Stable"}, "v1.0.0-rc.2", Prefs{Channel: Prerelease}, Settings{Enabled: true, Channel: Stable, URL: DefaultURL}, ""},
		{"環境変数の off", map[string]string{EnvCheck: "OFF"}, "v1.0.0", Prefs{}, Settings{Enabled: false, Channel: Stable, URL: DefaultURL}, ""},
		{"控えの off は環境変数の on でも止める", map[string]string{EnvCheck: "on"}, "v1.0.0", Prefs{Check: "off"}, Settings{Enabled: false, Channel: Stable, URL: DefaultURL}, ""},
		{"確認先の差し替え", map[string]string{EnvURL: "https://mirror.example.invalid/releases.json"}, "v1.0.0", Prefs{}, Settings{Enabled: true, Channel: Stable, URL: "https://mirror.example.invalid/releases.json"}, ""},
		{"分からない check", map[string]string{EnvCheck: "no"}, "v1.0.0", Prefs{}, Settings{}, "updatecheck.err.bad_check"},
		{"分からないチャンネル", map[string]string{EnvChannel: "beta"}, "v1.0.0", Prefs{}, Settings{}, "updatecheck.err.bad_channel"},
		{"https でない確認先", map[string]string{EnvURL: "http://mirror.example.invalid/"}, "v1.0.0", Prefs{}, Settings{}, "updatecheck.err.bad_url"},
	} {
		got, err := Resolve(env(c.env), c.current, c.p)
		if c.errID != "" {
			if err == nil || err.Error() != c.errID {
				t.Errorf("%s: 誤りにならない: %+v %v", c.name, got, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: %+v %v", c.name, got, err)
		}
	}
}

// fakeGitHub は GitHub の /releases と資産の取得を模す（TLS。実際の GitHub には繋がない）。
type fakeGitHub struct {
	mu       sync.Mutex
	releases []Release
	files    map[string][]byte // 資産の path → 本体
	status   int               // 0 以外なら一覧をこの状態で返す
	hits     atomic.Int32      // 受けた要求の数（一覧と資産の合計）
	sumsHits atomic.Int32      // SHA256SUMS と .minisig の取得の数
	srv      *httptest.Server
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{files: map[string][]byte{}}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Path == "/repos/o/r/releases" {
			if f.status != 0 {
				w.WriteHeader(f.status)
				return
			}
			json.NewEncoder(w).Encode(f.releases)
			return
		}
		if strings.HasSuffix(r.URL.Path, "SHA256SUMS") || strings.HasSuffix(r.URL.Path, ".minisig") {
			f.sumsHits.Add(1)
		}
		b, ok := f.files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) listURL() string { return f.srv.URL + "/repos/o/r/releases" }

// add はリリースを足す（files は資産の名前 → 本体。nil の本体は一覧にだけ載せる）。
func (f *fakeGitHub) add(tagName string, pre bool, files map[string][]byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := Release{Tag: tagName, Prerelease: pre, URL: "https://example.invalid/releases/tag/" + tagName}
	for name, body := range files {
		p := "/download/" + tagName + "/" + name
		r.Assets = append(r.Assets, Asset{Name: name, URL: f.srv.URL + p, Size: int64(len(body))})
		if body != nil {
			f.files[p] = body
		}
	}
	f.releases = append(f.releases, r)
}

type signer struct {
	pub   string
	priv  ed25519.PrivateKey
	keyID []byte
}

func newSigner() signer {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	id := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	return signer{pub: base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), id...), pub...)), priv: priv, keyID: id}
}

// sign は minisign の既定（ED: 本文の BLAKE2b-512）の .minisig を作る。
func (s signer) sign(msg []byte, trusted string) []byte {
	h := blake2b.Sum512(msg)
	sig := ed25519.Sign(s.priv, h[:])
	global := ed25519.Sign(s.priv, append(append([]byte{}, sig...), trusted...))
	return []byte("untrusted comment: signature from minisign secret key\n" +
		base64.StdEncoding.EncodeToString(append(append([]byte("ED"), s.keyID...), sig...)) + "\n" +
		"trusted comment: " + trusted + "\n" + base64.StdEncoding.EncodeToString(global) + "\n")
}

func hexSum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// release は署名つきのリリースの資産（サーバ版の書庫・SHA256SUMS・.minisig）を作る。ArchiveAsset("linux", "amd64") が
// 探す名前（looptrack_<版>_linux_amd64_server.tar.gz）で作る。
func (s signer) release(version, trusted string, sumsNames ...string) map[string][]byte {
	body := []byte("binary " + version)
	name := "looptrack_" + version + "_linux_amd64_server.tar.gz"
	if len(sumsNames) == 0 {
		sumsNames = []string{name}
	}
	var sums strings.Builder
	for _, n := range sumsNames {
		sums.WriteString(hexSum(body) + "  " + n + "\n")
	}
	return map[string][]byte{name: body, "SHA256SUMS": []byte(sums.String()), "SHA256SUMS.minisig": s.sign([]byte(sums.String()), trusted)}
}

func (f *fakeGitHub) checker(current, key string, extra map[string]string) *Checker {
	env := map[string]string{EnvURL: f.listURL()}
	for k, v := range extra {
		env[k] = v
	}
	return &Checker{
		Current: current, PublicKey: key, Asset: ArchiveAsset("linux", "amd64"),
		Getenv: func(k string) string { return env[k] },
		Client: f.srv.Client(),
		Now:    func() time.Time { return time.Date(2026, 9, 26, 1, 2, 3, 0, time.UTC) },
	}
}

// TestCheckSigned は、署名を確かめてから新しい版があるとすることを確かめる（対照: 署名・版・名前がそろえば available）。
func TestCheckSigned(t *testing.T) {
	s := newSigner()
	f := newFakeGitHub(t)
	f.add("v1.1.0", false, s.release("v1.1.0", "looptrack v1.1.0 SHA256SUMS"))
	f.add("appimage-runtime-source-20251108", false, map[string][]byte{"SHA256SUMS": []byte("x")})

	res := f.checker("v1.0.0", s.pub, nil).Check(context.Background(), Prefs{})
	if res.Status != StatusAvailable || !res.Signed || tag(res.Latest) != "v1.1.0" || res.Asset == nil ||
		res.Asset.Name != "looptrack_v1.1.0_linux_amd64_server.tar.gz" || res.Asset.SHA256 != hexSum([]byte("binary v1.1.0")) || !strings.HasPrefix(res.Asset.URL, "https://") {
		t.Fatalf("対照（署名・版・名前がそろう）で available にならない。前提が崩れている: %+v", res)
	}
	if res.Latest.Assets != nil || res.Channel != Stable || res.URL != f.listURL() || !res.CheckedAt.Equal(time.Date(2026, 9, 26, 1, 2, 3, 0, time.UTC)) {
		t.Errorf("結果の中身: %+v", res)
	}

	// 版を持たない trusted comment でも、署名された SHA256SUMS に版つきの名前があれば通す
	f2 := newFakeGitHub(t)
	f2.add("v1.1.0", false, s.release("v1.1.0", "looptrack SHA256SUMS dist"))
	if res := f2.checker("v1.0.0", s.pub, nil).Check(context.Background(), Prefs{}); res.Status != StatusAvailable {
		t.Errorf("版の無い trusted comment + 名前: %+v", res)
	}
	// 名前で確かめない（Asset なし）と、版を署名で確かめられない
	c := f2.checker("v1.0.0", s.pub, nil)
	c.Asset = nil
	if res := c.Check(context.Background(), Prefs{}); res.Status != StatusError || !strings.Contains(res.Error[i18n.JA], "タグの版を署名で確かめられません") {
		t.Errorf("版の無い trusted comment だけ: %+v", res)
	}

	for _, tc := range []struct {
		name  string
		files func() map[string][]byte
		ja    string
		en    string
	}{
		{"署名が無い", func() map[string][]byte {
			m := s.release("v1.1.0", "looptrack v1.1.0 SHA256SUMS")
			delete(m, "SHA256SUMS.minisig")
			return m
		}, "v1.1.0 のリリースに SHA256SUMS と SHA256SUMS.minisig がそろっていません", "does not have both SHA256SUMS and SHA256SUMS.minisig"},
		{"SHA256SUMS の改ざん", func() map[string][]byte {
			m := s.release("v1.1.0", "looptrack v1.1.0 SHA256SUMS")
			m["SHA256SUMS"] = append(m["SHA256SUMS"], "0000  evil\n"...)
			return m
		}, "v1.1.0 の SHA256SUMS の署名を確かめられません: 署名が一致しません", "Cannot verify the SHA256SUMS signature of v1.1.0"},
		{"別の鍵の署名", func() map[string][]byte { return newSigner().release("v1.1.0", "looptrack v1.1.0 SHA256SUMS") },
			"v1.1.0 の SHA256SUMS の署名を確かめられません", "Cannot verify the SHA256SUMS signature of v1.1.0"},
		{"古い版の署名をタグだけ新しく見せる", func() map[string][]byte { return s.release("v1.1.0", "looptrack v1.0.0 SHA256SUMS") },
			"v1.1.0 の SHA256SUMS の署名に書かれた版 v1.0.0 がタグと違います", "The version v1.0.0 in the SHA256SUMS signature of v1.1.0 differs from the tag"},
		{"署名された SHA256SUMS にこの版の名前が無い", func() map[string][]byte {
			return s.release("v1.1.0", "looptrack SHA256SUMS dist", "looptrack_v1.0.0_linux_amd64_server.tar.gz")
		}, "v1.1.0 のリリース（署名された SHA256SUMS と資産の一覧）に looptrack_v1.1.0_linux_amd64_server.tar.gz がありません", "looptrack_v1.1.0_linux_amd64_server.tar.gz is not in the v1.1.0 release"},
	} {
		f := newFakeGitHub(t)
		f.add("v1.1.0", false, tc.files())
		res := f.checker("v1.0.0", s.pub, nil).Check(context.Background(), Prefs{})
		if res.Status != StatusError || res.Available() || res.Err == nil ||
			!strings.Contains(res.Error[i18n.JA], tc.ja) || !strings.Contains(res.Error[i18n.EN], tc.en) {
			t.Errorf("%s: %+v", tc.name, res)
		}
	}
}

// TestCheckChannel は rc を使う人は rc も追い、正式版を使う人は正式版だけを追うことを確かめる。
// 手元が最新以上なら SHA256SUMS を取りに行かない。
func TestCheckChannel(t *testing.T) {
	s := newSigner()
	f := newFakeGitHub(t)
	f.add("v1.0.0-rc.2", false, s.release("v1.0.0-rc.2", "looptrack v1.0.0-rc.2 SHA256SUMS")) // Latest として公開した rc
	f.add("v1.0.0-rc.1", true, s.release("v1.0.0-rc.1", "looptrack v1.0.0-rc.1 SHA256SUMS"))
	f.add("v0.9.0", false, s.release("v0.9.0", "looptrack v0.9.0 SHA256SUMS"))

	if res := f.checker("v1.0.0-rc.1", s.pub, nil).Check(context.Background(), Prefs{}); res.Status != StatusAvailable || tag(res.Latest) != "v1.0.0-rc.2" || res.Channel != Prerelease {
		t.Errorf("rc.1 → rc.2: %+v", res)
	}
	before := f.sumsHits.Load()
	res := f.checker("v0.9.0", s.pub, nil).Check(context.Background(), Prefs{})
	if res.Status != StatusUpToDate || tag(res.Latest) != "v0.9.0" || res.Channel != Stable {
		t.Errorf("正式版は rc を追わない: %+v", res)
	}
	if f.sumsHits.Load() != before {
		t.Errorf("最新なのに SHA256SUMS を取った（%d → %d）", before, f.sumsHits.Load())
	}
	// 設定で prerelease に変えれば、正式版の人も rc を知る（環境変数・控えのどちらでも）
	if res := f.checker("v0.9.0", s.pub, map[string]string{EnvChannel: "prerelease"}).Check(context.Background(), Prefs{}); res.Status != StatusAvailable || tag(res.Latest) != "v1.0.0-rc.2" {
		t.Errorf("環境変数の prerelease: %+v", res)
	}
	if res := f.checker("v0.9.0", s.pub, nil).Check(context.Background(), Prefs{Channel: Prerelease}); res.Status != StatusAvailable || tag(res.Latest) != "v1.0.0-rc.2" {
		t.Errorf("控えの prerelease: %+v", res)
	}
	// rc の人も stable に変えれば rc を追わない
	if res := f.checker("v1.0.0-rc.1", s.pub, map[string]string{EnvChannel: "stable"}).Check(context.Background(), Prefs{}); res.Status != StatusUpToDate || tag(res.Latest) != "v0.9.0" {
		t.Errorf("rc の人の stable: %+v", res)
	}
	// 一覧の取得の失敗
	f.mu.Lock()
	f.status = http.StatusForbidden
	f.mu.Unlock()
	if res := f.checker("v0.9.0", s.pub, nil).Check(context.Background(), Prefs{}); res.Status != StatusError || !strings.Contains(res.Error[i18n.EN], "HTTP 403") {
		t.Errorf("403: %+v", res)
	}
}

// countingTransport は要求の数を数える（通信しないことの確認。応答は返さない）。
type countingTransport struct{ n atomic.Int32 }

func (c *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	c.n.Add(1)
	return nil, errors.New("no network in tests")
}

// TestCheckNoNetwork は、確認しない設定・比べられない版・公開鍵を持たないビルドの GitHub では通信しないことを確かめる。
// 対照として、確かめる設定なら同じ Checker が要求を出すことも確かめる。
func TestCheckNoNetwork(t *testing.T) {
	s := newSigner()
	tr := &countingTransport{}
	mk := func(current, key string, env map[string]string) *Checker {
		return &Checker{Current: current, PublicKey: key, Getenv: func(k string) string { return env[k] }, Client: &http.Client{Transport: tr}}
	}
	// 対照: 既定（GitHub・公開鍵あり）なら GitHub へ要求を出す
	if res := mk("v1.0.0", s.pub, nil).Check(context.Background(), Prefs{}); res.Status != StatusError || tr.n.Load() != 1 || res.URL != DefaultURL {
		t.Fatalf("対照（確かめる設定）で要求を出さない。前提が崩れている: %+v %d", res, tr.n.Load())
	}
	tr.n.Store(0)
	for _, c := range []struct {
		name   string
		ch     *Checker
		p      Prefs
		status Status
	}{
		{"LOOPTRACK_UPDATE_CHECK=off", mk("v1.0.0", s.pub, map[string]string{EnvCheck: "off"}), Prefs{}, StatusDisabled},
		{"メニューで止めた", mk("v1.0.0", s.pub, nil), Prefs{Check: "off"}, StatusDisabled},
		{"分からない値", mk("v1.0.0", s.pub, map[string]string{EnvCheck: "maybe"}), Prefs{}, StatusError},
		{"比べられない版", mk("dev", s.pub, nil), Prefs{}, StatusNotComparable},
		{"公開鍵を持たないビルドの GitHub", mk("v1.0.0", "", nil), Prefs{}, StatusNoKey},
	} {
		res := c.ch.Check(context.Background(), c.p)
		if res.Status != c.status || tr.n.Load() != 0 {
			t.Errorf("%s: %+v 要求 %d 回", c.name, res, tr.n.Load())
		}
	}
	res := mk("v1.0.0", "", nil).Check(context.Background(), Prefs{})
	if !strings.Contains(res.Error[i18n.JA], "公開鍵を持たないので、GitHub のリリースから新しい版を確かめません") || !strings.Contains(res.Error[i18n.EN], "no public key") {
		t.Errorf("公開鍵なしの文面: %+v", res.Error)
	}
}

// TestCheckNoKeyCustomSource は、公開鍵を持たないビルドでも確認先を差し替えれば、署名なし（Signed=false）で知らせることを確かめる。
func TestCheckNoKeyCustomSource(t *testing.T) {
	f := newFakeGitHub(t)
	f.add("v1.1.0", false, newSigner().release("v1.1.0", "looptrack v1.1.0 SHA256SUMS"))
	res := f.checker("v1.0.0", "", nil).Check(context.Background(), Prefs{})
	if res.Status != StatusAvailable || res.Signed || res.Asset == nil || res.Asset.SHA256 != "" || f.sumsHits.Load() != 0 {
		t.Errorf("%+v sums %d", res, f.sumsHits.Load())
	}
}

// TestRunner は、起動時にすぐ確かめ、間隔ごとに確かめ直し、結果を控えのファイルに残すこと、
// メニューで止めると次の回から通信しないことを確かめる。
func TestRunner(t *testing.T) {
	s := newSigner()
	f := newFakeGitHub(t)
	f.add("v1.1.0", false, s.release("v1.1.0", "looptrack v1.1.0 SHA256SUMS"))
	st := &Store{Path: filepath.Join(t.TempDir(), FileName)}
	results := make(chan Result, 16)
	r := &Runner{Checker: f.checker("v1.0.0", s.pub, nil), Store: st, Interval: 50 * time.Millisecond, OnResult: func(res Result) { results <- res }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Run が止まらない")
		}
	}()
	next := func() Result {
		t.Helper()
		select {
		case res := <-results:
			return res
		case <-time.After(5 * time.Second):
			t.Fatal("5 秒待っても確認の結果が来ない")
		}
		return Result{}
	}
	// 起動時（間隔を待たない）と、間隔の後の 2 回目
	for i := 0; i < 2; i++ {
		if res := next(); res.Status != StatusAvailable {
			t.Fatalf("%d 回目: %+v", i+1, res)
		}
	}
	rec := st.Load()
	if rec.Last == nil || rec.Last.Status != StatusAvailable || tag(rec.Last.Latest) != "v1.1.0" || rec.Last.Asset == nil || rec.Last.Asset.SHA256 == "" {
		t.Errorf("控え: %+v", rec.Last)
	}
	// メニューで止める → 確かめ直し（Wake）の結果は disabled で、通信しない
	if err := st.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	if st.Load().Check != "off" {
		t.Fatalf("控えに off が残らない: %+v", st.Load())
	}
	// 止めた後の最初の結果が disabled になるまで読む（止める前に始まった回の結果が 1 つ挟まることがある）
	r.Wake()
	var res Result
	for i := 0; i < 3; i++ {
		if res = next(); res.Status == StatusDisabled {
			break
		}
	}
	if res.Status != StatusDisabled {
		t.Fatalf("止めた後も確かめている: %+v", res)
	}
	hits := f.hits.Load()
	for i := 0; i < 2; i++ {
		if res := next(); res.Status != StatusDisabled {
			t.Errorf("止めた後: %+v", res)
		}
	}
	if f.hits.Load() != hits {
		t.Errorf("止めた後に通信した（%d → %d）", hits, f.hits.Load())
	}
	// 入れ直せば、また確かめる
	if err := st.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	r.Wake()
	for i := 0; i < 3; i++ {
		if res = next(); res.Status == StatusAvailable {
			break
		}
	}
	if res.Status != StatusAvailable {
		t.Errorf("入れ直した後: %+v", res)
	}
}

// TestStoreChannelAndBrokenFile はチャンネルの控えと、壊れた控えを空として読むことを確かめる。
func TestStoreChannelAndBrokenFile(t *testing.T) {
	st := &Store{Path: filepath.Join(t.TempDir(), FileName)}
	if err := st.SetChannel(Prerelease); err != nil {
		t.Fatal(err)
	}
	if err := st.Save(Result{Status: StatusUpToDate, Current: "v1.0.0"}); err != nil {
		t.Fatal(err)
	}
	if r := st.Load(); r.Channel != Prerelease || r.Last == nil || r.Last.Status != StatusUpToDate {
		t.Errorf("%+v", r)
	}
	if err := os.WriteFile(st.Path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := st.Load(); r.Channel != "" || r.Last != nil {
		t.Errorf("壊れた控え: %+v", r)
	}
}

// TestNotice は結果から知らせる新しい版を作ることを確かめる: available のときだけ作り、リリースのページの URL は
// https:// のものだけを残す（確認先を差し替えたときの一覧の値を、そのままブラウザで開かない）。
func TestNotice(t *testing.T) {
	avail := Result{Status: StatusAvailable, Current: "v1.0.0-rc.2",
		Latest: &Release{Tag: "v1.0.0-rc.3", URL: "https://github.com/howashoji/looptrack/releases/tag/v1.0.0-rc.3"}}
	n := avail.Notice()
	if n == nil || n.Version != "v1.0.0-rc.3" || n.Current != "v1.0.0-rc.2" || n.URL != avail.Latest.URL {
		t.Fatalf("available の知らせ = %+v", n)
	}
	for _, u := range []string{"http://example.invalid/r", "javascript:alert(1)", "file:///tmp/x", "https://", ""} {
		r := avail
		r.Latest = &Release{Tag: "v1.0.0-rc.3", URL: u}
		if n := r.Notice(); n == nil || n.URL != "" {
			t.Errorf("URL %q の知らせ = %+v（知らせは作り、URL は空にする）", u, n)
		}
	}
	for _, st := range []Status{StatusUpToDate, StatusDisabled, StatusError, StatusNoKey, StatusNotComparable} {
		r := avail
		r.Status = st
		if n := r.Notice(); n != nil {
			t.Errorf("%s で知らせを作った: %+v", st, n)
		}
	}
	if n := (Result{Status: StatusAvailable}).Notice(); n != nil {
		t.Errorf("Latest の無い available で知らせを作った: %+v", n)
	}
}

// TestDesktopAsset はデスクトップ版の配布物の名前が、リリースの組み立て（release.yml の「デスクトップ版がそろっていることを
// 確かめる」の名前）と同じことを確かめる。
func TestDesktopAsset(t *testing.T) {
	for _, c := range []struct{ goos, arch, want string }{
		{"darwin", "arm64", "Looptrack_v1.0.0-rc.3_macos_universal.dmg"},
		{"darwin", "amd64", "Looptrack_v1.0.0-rc.3_macos_universal.dmg"},
		{"linux", "amd64", "Looptrack_v1.0.0-rc.3_linux_x86_64.AppImage"},
		{"linux", "arm64", "Looptrack_v1.0.0-rc.3_linux_aarch64.AppImage"},
		{"windows", "amd64", "Looptrack_v1.0.0-rc.3_windows_amd64.zip"},
		{"windows", "arm64", "Looptrack_v1.0.0-rc.3_windows_arm64.zip"},
		{"linux", "386", ""},
		{"freebsd", "amd64", ""},
	} {
		if got := DesktopAsset(c.goos, c.arch)("v1.0.0-rc.3"); got != c.want {
			t.Errorf("DesktopAsset(%s, %s) = %q, want %q", c.goos, c.arch, got, c.want)
		}
	}
}

// TestDesktopInstallerAsset は Windows のインストーラの名前が、desktop.sh の windows-installer
// （<出力先>/Looptrack_<版>_windows_<arch>_setup.exe）と同じことを確かめる。配っていない arch は空。
func TestDesktopInstallerAsset(t *testing.T) {
	for _, c := range []struct{ arch, want string }{
		{"amd64", "Looptrack_v1.0.0-rc.5_windows_amd64_setup.exe"},
		{"arm64", "Looptrack_v1.0.0-rc.5_windows_arm64_setup.exe"},
		{"386", ""},
		{"", ""},
	} {
		if got := DesktopInstallerAsset(c.arch)("v1.0.0-rc.5"); got != c.want {
			t.Errorf("DesktopInstallerAsset(%s) = %q, want %q", c.arch, got, c.want)
		}
	}
	b, err := os.ReadFile("../../deploy/release/desktop.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `base="${APP_NAME}_${version}_windows_${goarch}_setup"`) {
		t.Error("desktop.sh の windows-installer の出力名が DesktopInstallerAsset と同じ形か確かめられません（前提が崩れています）")
	}
}

// TestArchiveAsset は書庫の名前が、リリースの組み立て（deploy/release/dist.sh の server_base・archive_ext と
// release.yml の 6 対象。Releases に上げるのは書庫だけで、素の実行ファイルは無い）と同じことを確かめる。
// 配っていない組み合わせは空。
func TestArchiveAsset(t *testing.T) {
	for _, c := range []struct{ goos, arch, want string }{
		{"darwin", "amd64", "looptrack_v1.0.0-rc.3_darwin_amd64_server.tar.gz"},
		{"darwin", "arm64", "looptrack_v1.0.0-rc.3_darwin_arm64_server.tar.gz"},
		{"linux", "amd64", "looptrack_v1.0.0-rc.3_linux_amd64_server.tar.gz"},
		{"linux", "arm64", "looptrack_v1.0.0-rc.3_linux_arm64_server.tar.gz"},
		{"windows", "amd64", "looptrack_v1.0.0-rc.3_windows_amd64_server.zip"},
		{"windows", "arm64", "looptrack_v1.0.0-rc.3_windows_arm64_server.zip"},
		{"linux", "386", ""},
		{"freebsd", "amd64", ""},
	} {
		if got := ArchiveAsset(c.goos, c.arch)("v1.0.0-rc.3"); got != c.want {
			t.Errorf("ArchiveAsset(%s, %s) = %q, want %q", c.goos, c.arch, got, c.want)
		}
	}
}

// TestNoticeFor は、通信に失敗した回は前の回（最後に成功した確認）の知らせを残し、それ以外では消すことを確かめる。
// 結果はどれも実際の Check で作る（残る側と消える側を同じテストに置く）。
func TestNoticeFor(t *testing.T) {
	s := newSigner()
	ok := newFakeGitHub(t)
	ok.add("v1.1.0", false, s.release("v1.1.0", "looptrack v1.1.0 SHA256SUMS"))
	lastOK := ok.checker("v1.0.0", s.pub, nil).Check(context.Background(), Prefs{})
	if !lastOK.Available() || lastOK.Transient {
		t.Fatalf("前提: 最後に成功した確認が available でない: %+v", lastOK)
	}

	// 通信の失敗: 一覧が HTTP 500・SHA256SUMS が取れない（一覧には載っているが 404）
	down := newFakeGitHub(t)
	down.status = http.StatusInternalServerError
	sumsGone := newFakeGitHub(t)
	files := s.release("v1.1.0", "looptrack v1.1.0 SHA256SUMS")
	files["SHA256SUMS"] = nil
	sumsGone.add("v1.1.0", false, files)
	// 署名の確認の失敗（SHA256SUMS の改ざん）
	tampered := newFakeGitHub(t)
	files = s.release("v1.1.0", "looptrack v1.1.0 SHA256SUMS")
	files["SHA256SUMS"] = append(files["SHA256SUMS"], "0000  evil\n"...)
	tampered.add("v1.1.0", false, files)

	for _, c := range []struct {
		name      string
		res       Result
		transient bool
		keep      bool
	}{
		{"一覧の取得が HTTP 500（残る）", down.checker("v1.0.0", s.pub, nil).Check(context.Background(), Prefs{}), true, true},
		{"SHA256SUMS の取得が 404（残る）", sumsGone.checker("v1.0.0", s.pub, nil).Check(context.Background(), Prefs{}), true, true},
		{"成功した確認で新しい版が無い（消える）", ok.checker("v1.1.0", s.pub, nil).Check(context.Background(), Prefs{}), false, false},
		{"確認を止めた（消える）", ok.checker("v1.0.0", s.pub, nil).Check(context.Background(), Prefs{Check: "off"}), false, false},
		{"置き換え済み: 控えの版が今の版以下で通信の失敗（消える）", down.checker("v1.1.0", s.pub, nil).Check(context.Background(), Prefs{}), true, false},
		{"署名の確認の失敗（消える）", tampered.checker("v1.0.0", s.pub, nil).Check(context.Background(), Prefs{}), false, false},
		{"設定の誤り（消える）", ok.checker("v1.0.0", s.pub, map[string]string{EnvCheck: "maybe"}).Check(context.Background(), Prefs{}), false, false},
	} {
		if c.res.Transient != c.transient {
			t.Errorf("%s: Transient = %v, want %v（%+v）", c.name, c.res.Transient, c.transient, c.res)
		}
		n := NoticeFor(c.res, &lastOK)
		switch {
		case c.keep && (n == nil || n.Version != "v1.1.0" || n.Current != c.res.Current || n.URL != lastOK.Latest.URL):
			t.Errorf("%s: 前の回の知らせが残らない: %+v", c.name, n)
		case !c.keep && n != nil:
			t.Errorf("%s: 知らせが残った: %+v", c.name, n)
		}
	}
	// 今回が available なら今回の知らせ。最後に成功した結果が無ければ通信の失敗でも知らせない
	if n := NoticeFor(lastOK, nil); n == nil || n.Version != "v1.1.0" {
		t.Errorf("available の知らせ: %+v", n)
	}
	if n := NoticeFor(down.checker("v1.0.0", s.pub, nil).Check(context.Background(), Prefs{}), nil); n != nil {
		t.Errorf("前の回が無いのに通信の失敗で知らせた: %+v", n)
	}
}

// TestStoreLastOK は、控えに最後に成功した結果が残り、通信の失敗では上書きされず、確認を止めると消えることを確かめる。
func TestStoreLastOK(t *testing.T) {
	st := &Store{Path: filepath.Join(t.TempDir(), FileName)}
	avail := Result{Status: StatusAvailable, Current: "v1.0.0", Latest: &Release{Tag: "v1.1.0"}}
	st.Save(avail)
	st.Save(Result{Status: StatusError, Current: "v1.0.0", Transient: true})
	r := st.Load()
	if r.Last == nil || r.Last.Status != StatusError || r.LastOK == nil || r.LastOK.Status != StatusAvailable {
		t.Fatalf("通信の失敗の後の控え: last=%+v last_ok=%+v", r.Last, r.LastOK)
	}
	st.Save(Result{Status: StatusUpToDate, Current: "v1.1.0"})
	if r := st.Load(); r.LastOK == nil || r.LastOK.Status != StatusUpToDate {
		t.Errorf("成功した結果で LastOK が替わらない: %+v", r.LastOK)
	}
	st.Save(avail)
	st.SetEnabled(false)
	if r := st.Load(); r.LastOK != nil || r.Check != "off" {
		t.Errorf("止めた後も LastOK が残る: %+v", r)
	}
}
