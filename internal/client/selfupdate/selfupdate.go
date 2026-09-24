// Package selfupdate は looptrack self-update（DESIGN.md §5-11「配布と更新」）。
//
//	looptrack self-update [--check] [--force] [--url URL]
//
// サーバの配布の一覧（GET /api/v1/dist の binaries）から自分の OS・CPU の版を取り、SHA-256 を確かめてから実行中のファイルを置き換える。
//   - 比べるのは relver の順（semver）。手元が最新以上なら何もしない。比べられない版（dev・日付-コミット ID）は --force のときだけ置き換える
//   - --check は更新の有無を示すだけ（置き換えない）
//   - 取得はサーバの API（トークンつき）。一覧の URL がサーバの外（公開後の GitHub Releases など）ならトークンを付けずに取る
//   - 署名（SHA256SUMS を minisign で署名する）: 公開鍵（MinisignPublicKey）を持つビルドでは、一覧の SHA256SUMS と
//     .minisig を取り、署名と、SHA256SUMS の中のハッシュが一覧と同じことを確かめる。署名が無い・合わなければ置き換えない。
//     公開鍵はソースに埋め込んである（リリースと go build はこれ）。署名しない配布（サーバの配布ディレクトリに置く配布。RELEASE.md §2-1）の
//     ビルドだけが -X で空にし、ハッシュの確認だけを行う
//   - 置き換え: 同じディレクトリに一時ファイルを書き、POSIX は rename で差し替える。Windows は実行中の exe を消せないので、
//     <名前>.old に改名してから新しいものを置く（.old は次の self-update で消す）。失敗したら元に戻す
//   - macOS のアプリ（.app の中の実体）は置き換えない（デスクトップ版はアプリごと更新する）
//   - deploy/install.sh で入れたサーバの実行ファイル（linux の /usr/local/bin/looptrack で、install.sh の設定がある）は置き換えない。
//     置き換えると migrate も再起動もされず、動いているサーバと実行ファイルの版がずれる。install.sh --upgrade で更新する
//     （DB の控え・migrate・再起動・版の確認を行う）。--check は動く
package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/relver"
)

// MinisignPublicKey は SHA256SUMS の署名を確かめる minisign の公開鍵（base64 の 1 行。deploy/release/minisign.pub の 2 行目。
// Looptrack 専用の鍵・鍵 ID 29D707D7EBFF246B）。秘密ではない。deploy/release/minisign.pub・deploy/install.sh と同じ値であることを
// テスト（TestEmbeddedPublicKey）で確かめる。
// 空にしたビルド（dist.sh の RELEASE_MINISIGN_PUBKEY= 。署名しない並行期間の配布だけ）は署名を確かめない。
var MinisignPublicKey = "RWRrJP/r1wfXKalGsxLnzFmmsExUd2azSJh4ccrYDJEBu8yE3N0ZlLJy"

// Binary は配布の一覧の実行ファイル 1 つ（サーバの distBinaryJSON）。
type Binary struct {
	Name    string `json:"name"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	URL     string `json:"url"`
}

// Listing は GET /api/v1/dist のうち self-update が使う部分。
type Listing struct {
	Binaries   []Binary `json:"binaries"`
	SumsURL    string   `json:"sha256sums_url"`
	MinisigURL string   `json:"sha256sums_minisig_url"`
}

// For は (os, arch) の実行ファイル（無ければ nil）。
func (l *Listing) For(goos, arch string) *Binary {
	for i := range l.Binaries {
		if l.Binaries[i].OS == goos && l.Binaries[i].Arch == arch {
			return &l.Binaries[i]
		}
	}
	return nil
}

// FetchListing は配布の一覧を取る（トークンが要る）。
func FetchListing(cl *api.Client) (*Listing, error) {
	res, err := cl.Do(api.Request{Method: http.MethodGet, Path: "/dist", Raw: true})
	if err != nil {
		return nil, err
	}
	var l Listing
	if err := json.Unmarshal(res.Raw, &l); err != nil {
		return nil, i18n.Errorf("selfupdate.err.listing_parse", "reason", err)
	}
	return &l, nil
}

// Options は self-update の実行の設定（テストで差し替える）。
type Options struct {
	Env     env.Env
	Stdout  io.Writer
	Stderr  io.Writer
	Version string // 実行中の版
	GOOS    string // 既定 runtime.GOOS
	GOARCH  string
	Exe     string // 置き換える実行ファイル（既定 os.Executable の実体）
	// Install は install.sh で入れたサーバの判定に使う置き場（nil なら DefaultServerInstall。テストで差し替える）
	Install *ServerInstall
}

// ServerInstall は deploy/install.sh が置く実行ファイルと、install.sh が作る設定の置き場。
type ServerInstall struct {
	Bin     string   // install.sh の BIN
	Markers []string // どれか 1 つがあれば install.sh で入れたサーバとみなす
}

// DefaultServerInstall は deploy/install.sh の値（BIN・STATE・CONF_DIR/.env・UNIT）。
// install.conf は systemd と compose のどちらでも最後に置く印。.env と unit は途中で止めた入れ方（印の前）も拾うため。
// compose の置き場（既定 /opt/looptrack）は --dir で変わり、install.conf が DIR を持つので見ない。
var DefaultServerInstall = ServerInstall{
	Bin: "/usr/local/bin/looptrack",
	Markers: []string{
		"/etc/looptrack/install.conf",
		"/etc/looptrack/.env",
		"/etc/systemd/system/looptrack.service",
	},
}

// InstalledServer は exe が install.sh で入れたサーバの実行ファイルかを判定する（linux のときだけ）。
// 当たれば見つかった設定のパスを返す。exe はシンボリックリンクを解いた実体でよい（Bin 側も解いて比べる）。
func InstalledServer(goos, exe string, s ServerInstall) (string, bool) {
	if goos != "linux" || s.Bin == "" || exe == "" {
		return "", false
	}
	if !samePath(exe, s.Bin) {
		return "", false
	}
	for _, m := range s.Markers {
		if _, err := os.Stat(m); err == nil {
			return m, true
		}
	}
	return "", false
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if a == b {
		return true
	}
	if r, err := filepath.EvalSymlinks(b); err == nil && filepath.Clean(r) == a {
		return true
	}
	return false
}

// Main は looptrack self-update の本体（終了コードを返す）。
func Main(args []string, o Options) int {
	lang := i18n.FromEnv(o.Env.Get)
	check, force, url := false, false, ""
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--check":
			check = true
		case a == "--force":
			force = true
		case a == "--url" && i+1 < len(args):
			i++
			url = args[i]
		case strings.HasPrefix(a, "--url="):
			url = strings.TrimPrefix(a, "--url=")
		case a == "-h" || a == "--help":
			fmt.Fprint(o.Stdout, usage(lang))
			return 0
		default:
			fmt.Fprint(o.Stderr, i18n.T(lang, "cmd.prefix.error", "msg", i18n.T(lang, "selfupdate.err.unknown_arg", "arg", a))+"\n"+usage(lang))
			return 2
		}
	}
	if err := run(lang, o, check, force, url); err != nil {
		fmt.Fprintln(o.Stderr, i18n.T(lang, "cmd.prefix.error", "msg", err))
		return 1
	}
	return 0
}

// usage は --help と、知らない引数のときに出す使い方。
func usage(lang i18n.Lang) string { return i18n.T(lang, "selfupdate.usage") }

func run(lang i18n.Lang, o Options, check, force bool, url string) error {
	if o.GOOS == "" {
		o.GOOS, o.GOARCH = runtime.GOOS, runtime.GOARCH
	}
	vars := map[string]string{}
	if url != "" {
		vars[env.Name(env.APIURL)] = url
	}
	cl, err := api.New(overlay(o.Env, vars))
	if err != nil {
		return err
	}
	if cl.BaseURL == "" {
		return i18n.Errorf("selfupdate.err.no_url", "env", env.Prefix+env.APIURL)
	}
	l, err := FetchListing(cl)
	if err != nil {
		return describe(lang, err)
	}
	b := l.For(o.GOOS, o.GOARCH)
	if b == nil {
		return i18n.Errorf("selfupdate.err.no_binary", "os", o.GOOS, "arch", o.GOARCH)
	}
	cmp, comparable := relver.Compare(o.Version, b.Version)
	switch {
	case comparable && cmp >= 0 && !force:
		fmt.Fprintln(o.Stdout, i18n.T(lang, "selfupdate.up_to_date", "local", o.Version, "dist", b.Version, "os", o.GOOS, "arch", o.GOARCH))
		return nil
	case !comparable && !force:
		fmt.Fprintln(o.Stdout, i18n.T(lang, "selfupdate.not_comparable", "local", o.Version, "dist", b.Version))
		return nil
	case check:
		fmt.Fprintln(o.Stdout, i18n.T(lang, "selfupdate.available", "local", o.Version, "dist", b.Version, "os", o.GOOS, "arch", o.GOARCH))
		return nil
	}
	exe := o.Exe
	if exe == "" {
		if exe, err = os.Executable(); err != nil {
			return i18n.Wrapf(err, "selfupdate.err.exe_unknown")
		}
		if r, err := filepath.EvalSymlinks(exe); err == nil {
			exe = r
		}
	}
	if strings.Contains(filepath.ToSlash(exe), ".app/Contents/") {
		return i18n.Errorf("selfupdate.err.in_app", "exe", exe)
	}
	inst := DefaultServerInstall
	if o.Install != nil {
		inst = *o.Install
	}
	if m, ok := InstalledServer(o.GOOS, exe, inst); ok {
		// 置き換えると migrate も再起動もされず、動いているサーバと実行ファイルの版がずれる
		return i18n.Errorf("selfupdate.err.installed_server", "exe", exe, "marker", m)
	}
	cleanupOld(exe)
	body, err := download(lang, cl, b)
	if err != nil {
		return err
	}
	if err := verifySignature(lang, cl, l, b); err != nil {
		return err
	}
	if err := replace(exe, body, o.GOOS == "windows"); err != nil {
		return err
	}
	fmt.Fprintln(o.Stdout, i18n.T(lang, "selfupdate.done", "from", o.Version, "to", b.Version, "exe", exe, "sha256", b.SHA256))
	if MinisignPublicKey == "" {
		fmt.Fprintln(o.Stdout, i18n.T(lang, "selfupdate.note.unsigned"))
	}
	fmt.Fprintln(o.Stdout, i18n.T(lang, "selfupdate.note.reinit"))
	return nil
}

// overlay は環境変数に上書きを重ねる（値が空なら消す）。
func overlay(e env.Env, vars map[string]string) env.Env {
	if len(vars) == 0 {
		return e
	}
	return env.Overlay(e, vars)
}

func describe(lang i18n.Lang, err error) error {
	var ae *api.Error
	if errors.As(err, &ae) {
		msg := ae.Text(lang)
		if ae.Status == http.StatusUnauthorized {
			msg += "（" + api.Relogin(lang) + "）"
		}
		return i18n.Errorf("selfupdate.err.listing_fetch", "msg", msg)
	}
	return err
}

// fetch は URL の本体を取る。サーバの API の中ならトークンを付け、外（公開後の配布先）なら付けない。
func fetch(lang i18n.Lang, cl *api.Client, url string) ([]byte, error) {
	if p, ok := strings.CutPrefix(url, cl.BaseURL+"/api/v1/"); ok {
		res, err := cl.Do(api.Request{Method: http.MethodGet, Path: "/" + p, Raw: true, Headers: map[string]string{"Accept": "*/*"}})
		if err != nil {
			return nil, describeGet(lang, err)
		}
		return res.Raw, nil
	}
	if !strings.HasPrefix(url, "https://") {
		return nil, i18n.Errorf("selfupdate.err.not_https", "url", url)
	}
	hc := &http.Client{Timeout: 10 * time.Minute, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: api.TLSConfig(cl.Env)}}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "looptrack/"+api.Version)
	res, err := hc.Do(req)
	if err != nil {
		return nil, i18n.Wrapf(err, "selfupdate.err.fetch", "url", url)
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return nil, i18n.Errorf("selfupdate.err.fetch_http", "url", url, "status", res.StatusCode)
	}
	return io.ReadAll(res.Body)
}

func describeGet(lang i18n.Lang, err error) error {
	var ae *api.Error
	if errors.As(err, &ae) {
		return i18n.Errorf("selfupdate.err.fetch_api", "msg", ae.Text(lang))
	}
	return err
}

func download(lang i18n.Lang, cl *api.Client, b *Binary) ([]byte, error) {
	body, err := fetch(lang, cl, b.URL)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, b.SHA256) {
		return nil, i18n.Errorf("selfupdate.err.sha_mismatch", "name", b.Name, "want", b.SHA256, "got", got)
	}
	if b.Size > 0 && int64(len(body)) != b.Size {
		return nil, i18n.Errorf("selfupdate.err.size_mismatch", "name", b.Name, "want", int(b.Size), "got", len(body))
	}
	return body, nil
}

// verifySignature は SHA256SUMS の署名を確かめ、その中の b のハッシュが一覧と同じことを確かめる（公開鍵が無ければ何もしない）。
func verifySignature(lang i18n.Lang, cl *api.Client, l *Listing, b *Binary) error {
	if MinisignPublicKey == "" {
		return nil
	}
	if l.SumsURL == "" || l.MinisigURL == "" {
		return i18n.Errorf("selfupdate.err.no_signature")
	}
	sums, err := fetch(lang, cl, l.SumsURL)
	if err != nil {
		return err
	}
	sig, err := fetch(lang, cl, l.MinisigURL)
	if err != nil {
		return err
	}
	if err := VerifyMinisign(MinisignPublicKey, sums, sig); err != nil {
		return i18n.Errorf("selfupdate.err.minisig_verify", "reason", err)
	}
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == b.Name {
			if !strings.EqualFold(f[0], b.SHA256) {
				return i18n.Errorf("selfupdate.err.sums_mismatch", "name", b.Name)
			}
			return nil
		}
	}
	return i18n.Errorf("selfupdate.err.sums_missing", "name", b.Name)
}

// cleanupOld は前回の Windows の置き換えで残った <名前>.old を消す（実行中なら消えないので、失敗は無視する）。
func cleanupOld(exe string) {
	_ = os.Remove(exe + ".old")
}

// replace は exe を body で置き換える。
func replace(exe string, body []byte, windows bool) error {
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".looptrack-new-*")
	if err != nil {
		return i18n.Wrapf(err, "selfupdate.err.tmp_write", "dir", dir)
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return err
	}
	if !windows {
		if err := os.Rename(tmpName, exe); err != nil {
			return i18n.Wrapf(err, "selfupdate.err.rename", "exe", exe)
		}
		ok = true
		return nil
	}
	// Windows: 実行中の exe は上書き・削除できないが改名はできる
	old := exe + ".old"
	if _, err := os.Stat(old); err == nil {
		old = fmt.Sprintf("%s.%d.old", exe, time.Now().UnixNano())
	}
	if err := os.Rename(exe, old); err != nil {
		return i18n.Wrapf(err, "selfupdate.err.rename_old", "exe", exe)
	}
	if err := os.Rename(tmpName, exe); err != nil {
		_ = os.Rename(old, exe) // 元に戻す
		return i18n.Wrapf(err, "selfupdate.err.place_back", "exe", exe)
	}
	ok = true
	return nil
}
