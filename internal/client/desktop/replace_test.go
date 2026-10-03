package desktop

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/blake2b"

	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/updatecheck"
)

func sumHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// assetServer は資産を 1 つ返す https のサーバ（取得した回数を数える）。
func assetServer(t *testing.T, body []byte) (*httptest.Server, *int) {
	t.Helper()
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// signedResult は署名を確かめた available の結果（置き換えに使える形）。
func signedResult(url, name string, body []byte) *updatecheck.Result {
	return &updatecheck.Result{
		Status: updatecheck.StatusAvailable, Current: "v1.0.0", Signed: true,
		Latest: &updatecheck.Release{Tag: "v1.1.0"},
		Asset:  &updatecheck.Asset{Name: name, URL: url, Size: int64(len(body)), SHA256: sumHex(body)},
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// leftovers は dir の中の、置き換えの途中で作る一時のもの（.update-・.looptrack-update-・.failed）の名前。
func leftovers(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		n := e.Name()
		if strings.Contains(n, ".update-") || strings.HasPrefix(n, ".looptrack-update-") || strings.HasSuffix(n, ".failed") {
			out = append(out, n)
		}
	}
	return out
}

// TestReplaceTarget は置き換えるものを決める。Windows は Looptrack.exe（ふつうのファイル）を通し、隣の unins000.exe の
// 有無でインストーラ版か zip 版かを分ける（受け入れ条件 2。入れ方の判定は TestWindowsInstallKind）。
func TestReplaceTarget(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "Looptrack.AppImage")
	os.WriteFile(img, []byte("x"), 0o755)
	winZip, winInst := filepath.Join(dir, "zip"), filepath.Join(dir, "inst")
	for _, d := range []string{winZip, winInst} {
		os.MkdirAll(filepath.Join(d, "cli"), 0o755)
		os.WriteFile(filepath.Join(d, "Looptrack.exe"), []byte("MZ"), 0o755)
		os.WriteFile(filepath.Join(d, "cli", "looptrack.exe"), []byte("MZ"), 0o755)
	}
	// 同梱の CLI と、アプリが写した CLI の写し（%LOCALAPPDATA%\Programs\looptrack\looptrack.exe）。名前は大小を無視すれば Looptrack.exe と同じ
	cliCopy := filepath.Join(dir, "Programs", "looptrack", "looptrack.exe")
	os.MkdirAll(filepath.Dir(cliCopy), 0o755)
	os.WriteFile(cliCopy, []byte("MZ"), 0o755)
	// Looptrack.exe だけがあって隣に cli\looptrack.exe が無い（アプリの配置ではない）
	lone := filepath.Join(dir, "lone")
	os.MkdirAll(lone, 0o755)
	os.WriteFile(filepath.Join(lone, "Looptrack.exe"), []byte("MZ"), 0o755)
	os.WriteFile(filepath.Join(winInst, "unins000.exe"), []byte("MZ"), 0o755)
	os.WriteFile(filepath.Join(winZip, "looptrack-cli.exe"), []byte("MZ"), 0o755)
	for _, c := range []struct {
		name string
		r    replacer
		want string // 空なら誤り
		err  string
	}{
		{"macOS の .app", replacer{GOOS: "darwin", Launcher: "/Applications/Looptrack.app/Contents/MacOS/looptrack"}, "/Applications/Looptrack.app", ""},
		{"macOS の .app の外", replacer{GOOS: "darwin", Launcher: "/usr/local/bin/looptrack"}, "", "desktop.update.err.not_app"},
		{"Linux の AppImage", replacer{GOOS: "linux", AppImage: img}, img, ""},
		{"Linux で APPIMAGE が無い", replacer{GOOS: "linux"}, "", "desktop.update.err.not_appimage"},
		{"Linux で APPIMAGE が相対パス", replacer{GOOS: "linux", AppImage: "Looptrack.AppImage"}, "", "desktop.update.err.not_appimage"},
		{"Linux で APPIMAGE がディレクトリ", replacer{GOOS: "linux", AppImage: dir}, "", "desktop.update.err.not_appimage"},
		{"Windows の zip 版", replacer{GOOS: "windows", Launcher: filepath.Join(winZip, "Looptrack.exe")}, filepath.Join(winZip, "Looptrack.exe"), ""},
		{"Windows のインストーラ版", replacer{GOOS: "windows", Launcher: filepath.Join(winInst, "Looptrack.exe")}, filepath.Join(winInst, "Looptrack.exe"), ""},
		{"Windows の同梱の CLI から起動した", replacer{GOOS: "windows", Launcher: filepath.Join(winZip, "cli", "looptrack.exe")}, "", "desktop.update.err.not_windows_app"},
		{"Windows のインストーラ版の同梱の CLI から起動した", replacer{GOOS: "windows", Launcher: filepath.Join(winInst, "cli", "looptrack.exe")}, "", "desktop.update.err.not_windows_app"},
		{"Windows の CLI の写しから起動した", replacer{GOOS: "windows", Launcher: cliCopy}, "", "desktop.update.err.not_windows_app"},
		{"Windows で隣に cli\\looptrack.exe が無い", replacer{GOOS: "windows", Launcher: filepath.Join(lone, "Looptrack.exe")}, "", "desktop.update.err.not_windows_app"},
		{"Windows で Looptrack.exe が無い", replacer{GOOS: "windows", Launcher: filepath.Join(dir, "none", "Looptrack.exe")}, "", "desktop.update.err.not_windows_app"},
		{"Windows で Looptrack.exe でない", replacer{GOOS: "windows", Launcher: filepath.Join(winZip, "looptrack-cli.exe")}, "", "desktop.update.err.not_windows_app"},
		{"Windows で Launcher が空", replacer{GOOS: "windows"}, "", "desktop.update.err.not_windows_app"},
		{"Windows で Launcher が相対パス", replacer{GOOS: "windows", Launcher: "Looptrack.exe"}, "", "desktop.update.err.not_windows_app"},
		{"ほかの OS", replacer{GOOS: "freebsd", Launcher: "/usr/local/bin/looptrack"}, "", "desktop.update.err.unsupported_os"},
	} {
		// macOS の .app の組み立ては filepath（Windows では \ 区切り）で行うので、Windows で走らせると / の期待と比べられない。
		// この 1 件だけを外す（Windows の行はどの OS でも一時ディレクトリのパスで比べる）。
		if c.r.GOOS == "darwin" && c.want != "" && runtime.GOOS == "windows" {
			continue
		}
		got, err := c.r.target()
		if c.err != "" {
			if err == nil || err.Error() != c.err {
				t.Errorf("%s: err = %v, want %s", c.name, err, c.err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: target = %q, %v, want %q", c.name, got, err, c.want)
		}
	}
}

// TestCheckSource は、署名を確かめた結果で資産の URL と SHA-256 がそろったものだけを置き換えに使うことを確かめる。
func TestCheckSource(t *testing.T) {
	ok := signedResult("https://example.invalid/a", "Looptrack_v1.1.0_linux_x86_64.AppImage", []byte("x"))
	if err := check(ok); err != nil {
		t.Fatalf("対照（そろった結果）が通らない: %v", err)
	}
	for _, c := range []struct {
		name string
		mod  func(r *updatecheck.Result)
		want string
	}{
		{"署名を確かめていない", func(r *updatecheck.Result) { r.Signed = false }, "desktop.update.err.unsigned"},
		{"資産が無い", func(r *updatecheck.Result) { r.Asset = nil }, "desktop.update.err.unsigned"},
		{"ハッシュが無い", func(r *updatecheck.Result) { r.Asset.SHA256 = "" }, "desktop.update.err.unsigned"},
		{"https でない", func(r *updatecheck.Result) { r.Asset.URL = "http://example.invalid/a" }, "desktop.update.err.bad_asset"},
		{"名前に区切り", func(r *updatecheck.Result) { r.Asset.Name = "../x.AppImage" }, "desktop.update.err.bad_asset"},
		{"新しい版が無い", func(r *updatecheck.Result) { r.Status = updatecheck.StatusUpToDate }, "desktop.update.err.no_update"},
	} {
		r := *ok
		a := *ok.Asset
		r.Asset = &a
		c.mod(&r)
		if err := check(&r); err == nil || err.Error() != c.want {
			t.Errorf("%s: err = %v, want %s", c.name, err, c.want)
		}
	}
	if err := check(nil); err == nil {
		t.Error("nil の結果が通った")
	}
}

// Linux: 取得 → SHA-256 → ELF → 同じディレクトリで改名。前の版は .prev（前の .prev は消える）、起動し直しは desktop --after-update。
func TestApplyAppImage(t *testing.T) {
	newBody := []byte("\x7fELF new version")
	srv, _ := assetServer(t, newBody)
	dir := t.TempDir()
	cur := filepath.Join(dir, "Looptrack.AppImage")
	os.WriteFile(cur, []byte("\x7fELF old version"), 0o755)
	os.WriteFile(cur+".prev", []byte("older"), 0o755)
	var started [][]string
	r := &replacer{GOOS: "linux", AppImage: cur, Client: srv.Client(),
		Start: func(name string, args ...string) error {
			started = append(started, append([]string{name}, args...))
			return nil
		}}
	ap, err := r.apply(context.Background(), signedResult(srv.URL+"/a", "Looptrack_v1.1.0_linux_x86_64.AppImage", newBody))
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, cur); got != string(newBody) {
		t.Errorf("置き換えた後の中身 = %q", got)
	}
	// Windows には実行の権限のビットが無い（Mode は 0666 を返す）。AppImage は Linux だけのものなので、権限は Windows では見ない。
	if fi, _ := os.Stat(cur); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o755 {
		t.Errorf("置き換えた後の権限 = %v, want 0755", fi.Mode().Perm())
	}
	if got := readFile(t, cur+".prev"); got != "\x7fELF old version" {
		t.Errorf(".prev の中身 = %q（前の版を残していない）", got)
	}
	if l := leftovers(t, dir); len(l) != 0 {
		t.Errorf("一時のファイルが残っている: %v", l)
	}
	if len(started) != 0 {
		t.Fatalf("relaunch の前に起動した: %v", started)
	}
	if err := ap.relaunch(); err != nil {
		t.Fatal(err)
	}
	if want := []string{cur, "desktop", "--after-update"}; len(started) != 1 || strings.Join(started[0], " ") != strings.Join(want, " ") {
		t.Errorf("起動し直し = %v, want %v", started, want)
	}
	// 起動し直せなかったときの戻し: 前の版が元の名前に戻り、.prev と新しい版は残らない
	if err := ap.undo(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, cur); got != "\x7fELF old version" {
		t.Errorf("戻した後の中身 = %q", got)
	}
	if _, err := os.Stat(cur + ".prev"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("戻した後に .prev が残っている: %v", err)
	}
	if l := leftovers(t, dir); len(l) != 0 {
		t.Errorf("戻した後に一時のファイルが残っている: %v", l)
	}
}

// Linux の失敗: SHA-256 が違う・ELF でない・置き場に書けない。どれも今の版と前の .prev に手を付けず、一時のファイルも残さない。
func TestApplyAppImageFailures(t *testing.T) {
	newBody := []byte("\x7fELF new version")
	for _, c := range []struct {
		name  string
		serve []byte
		res   func(url string) *updatecheck.Result
		ro    bool
		want  string
	}{
		{"SHA-256 が違う", []byte("\x7fELF tampered!!!"), func(u string) *updatecheck.Result {
			return signedResult(u, "L.AppImage", newBody)
		}, false, "desktop.update.err.sha256"},
		{"ELF でない", []byte("#!/bin/sh"), func(u string) *updatecheck.Result {
			return signedResult(u, "L.AppImage", []byte("#!/bin/sh"))
		}, false, "desktop.update.err.not_elf"},
		{"一覧の大きさより大きい", newBody, func(u string) *updatecheck.Result {
			r := signedResult(u, "L.AppImage", newBody)
			r.Asset.Size = 4
			return r
		}, false, "desktop.update.err.too_large"},
		{"置き場に書けない", newBody, func(u string) *updatecheck.Result {
			return signedResult(u, "L.AppImage", newBody)
		}, true, "not_writable"},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := assetServer(t, c.serve)
			dir := t.TempDir()
			cur := filepath.Join(dir, "Looptrack.AppImage")
			os.WriteFile(cur, []byte("old"), 0o755)
			os.WriteFile(cur+".prev", []byte("older"), 0o755)
			if c.ro {
				if os.Geteuid() == 0 {
					t.Skip("root では書き込みを拒めない")
				}
				if runtime.GOOS == "windows" {
					t.Skip("Windows ではディレクトリの chmod（読み取り専用の属性も）で中に作ることを拒めない。置き換えは Linux・macOS だけ")
				}
				os.Chmod(dir, 0o555)
				t.Cleanup(func() { os.Chmod(dir, 0o755) })
			}
			r := &replacer{GOOS: "linux", AppImage: cur, Client: srv.Client(),
				Start: func(string, ...string) error { t.Error("失敗したのに起動した"); return nil }}
			_, err := r.apply(context.Background(), c.res(srv.URL+"/a"))
			var nw *notWritableError
			switch {
			case c.want == "not_writable":
				if !errors.As(err, &nw) || nw.Dir != dir || nw.DMG != "" {
					t.Fatalf("err = %v, want notWritableError（%s）", err, dir)
				}
			case err == nil || !strings.Contains(err.Error(), c.want):
				t.Fatalf("err = %v, want %s", err, c.want)
			}
			if got := readFile(t, cur); got != "old" {
				t.Errorf("今の版が変わった: %q", got)
			}
			if got := readFile(t, cur+".prev"); got != "older" {
				t.Errorf("前の .prev が変わった: %q", got)
			}
			if l := leftovers(t, dir); len(l) != 0 {
				t.Errorf("一時のファイルが残っている: %v", l)
			}
		})
	}
}

// TestSwapRestoresOnFailure は、新しいものを元の名前に改名できなかったら今のものを元の名前に戻すことを確かめる。
func TestSwapRestoresOnFailure(t *testing.T) {
	dir := t.TempDir()
	cur := filepath.Join(dir, "a")
	os.WriteFile(cur, []byte("old"), 0o644)
	if err := swap(cur, filepath.Join(dir, "missing"), cur+".prev"); err == nil {
		t.Fatal("無いものへの置き換えが通った")
	}
	if got := readFile(t, cur); got != "old" {
		t.Errorf("今のものが戻っていない: %q", got)
	}
	if _, err := os.Stat(cur + ".prev"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf(".prev が残っている: %v", err)
	}
}

// fakeMac は macOS の外部のコマンド（spctl・hdiutil・ditto・codesign・open）の代わり。呼ばれた順に記録し、fail に書いたものを失敗させる。
type fakeMac struct {
	mu      sync.Mutex
	calls   []string
	fail    map[string]bool   // "codesign --verify" のように、名前と最初の引数で失敗させる
	ids     map[string]string // パスの末尾（Looptrack.app か stage の中）→ codesign -dv の出力
	newTeam string
	curTeam string
	id      string
	body    string // dmg の中の .app の実行ファイルの中身
}

func (f *fakeMac) run(_ context.Context, name string, args ...string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	f.mu.Unlock()
	key := name
	if len(args) > 0 {
		key += " " + args[0]
	}
	if f.fail[key] {
		return "rejected", errors.New("exit status 1")
	}
	switch name {
	case "hdiutil":
		if args[0] == "attach" {
			mnt := args[len(args)-2]
			exe := filepath.Join(mnt, AppName+".app", "Contents", "MacOS", "looptrack")
			os.MkdirAll(filepath.Dir(exe), 0o755)
			return "", os.WriteFile(exe, []byte(f.body), 0o755)
		}
		if args[0] == "detach" {
			return "", os.RemoveAll(filepath.Join(args[1], AppName+".app"))
		}
	case "ditto":
		return "", os.CopyFS(args[1], os.DirFS(args[0]))
	case "codesign":
		if args[0] == "-dv" {
			team := f.newTeam
			if !strings.Contains(args[len(args)-1], ".looptrack-update-") {
				team = f.curTeam
			}
			if team == "" {
				team = "not set"
			}
			return fmt.Sprintf("Executable=%s\nIdentifier=%s\nFormat=app bundle\nTeamIdentifier=%s\n", args[len(args)-1], f.id, team), nil
		}
	}
	return "", nil
}

func (f *fakeMac) called(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func macFixture(t *testing.T) (dir, app, work string) {
	t.Helper()
	base := t.TempDir()
	dir = filepath.Join(base, "Applications")
	app = filepath.Join(dir, "Looptrack.app")
	exe := filepath.Join(app, "Contents", "MacOS", "looptrack")
	os.MkdirAll(filepath.Dir(exe), 0o755)
	os.WriteFile(exe, []byte("old app"), 0o755)
	os.MkdirAll(app+".prev", 0o755)
	os.WriteFile(filepath.Join(app+".prev", "older"), []byte("older"), 0o644)
	return dir, app, filepath.Join(base, "data", "updates")
}

// macOS: dmg を取得 → spctl（open）→ マウント → 同じディレクトリに ditto → codesign --verify・spctl（execute）・識別子・チーム
// → 今の .app を Looptrack.app.prev に改名して置き換える → マウントを外す。起動し直しは open -n <.app> --args desktop --after-update。
func TestApplyMac(t *testing.T) {
	dmgBody := []byte("dmg bytes")
	srv, _ := assetServer(t, dmgBody)
	dir, app, work := macFixture(t)
	f := &fakeMac{id: BundleID, newTeam: "TEAM1", curTeam: "TEAM1", body: "new app"}
	r := &replacer{GOOS: "darwin", Launcher: filepath.Join(app, "Contents", "MacOS", "looptrack"), WorkDir: work, BundleID: BundleID,
		Client: srv.Client(), Run: f.run}
	name := "Looptrack_v1.1.0_macos_universal.dmg"
	ap, err := r.apply(context.Background(), signedResult(srv.URL+"/d", name, dmgBody))
	if err != nil {
		t.Fatalf("%v（呼んだもの: %v）", err, f.calls)
	}
	if got := readFile(t, filepath.Join(app, "Contents", "MacOS", "looptrack")); got != "new app" {
		t.Errorf("置き換えた後の中身 = %q", got)
	}
	if got := readFile(t, filepath.Join(app+".prev", "Contents", "MacOS", "looptrack")); got != "old app" {
		t.Errorf("Looptrack.app.prev の中身 = %q（前の版を残していない）", got)
	}
	if _, err := os.Stat(filepath.Join(app+".prev", "older")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("前の .prev が消えていない（前の版は 1 つだけ残す）")
	}
	if l := leftovers(t, dir); len(l) != 0 {
		t.Errorf("一時のディレクトリが残っている: %v", l)
	}
	dmg := filepath.Join(work, name)
	for _, want := range []string{
		"spctl --assess --type open --context context:primary-signature --verbose=2 " + dmg,
		"hdiutil attach -nobrowse -readonly -noautoopen -mountpoint ",
		"ditto ",
		"codesign --verify --deep --strict --verbose=2 ",
		"spctl --assess --type execute --verbose=2 ",
		"hdiutil detach ",
	} {
		if len(f.called(want)) == 0 {
			t.Errorf("%q を呼んでいない（呼んだもの: %v）", want, f.calls)
		}
	}
	if _, err := os.Stat(dmg); err != nil {
		t.Errorf("起動し直す前に dmg を消した: %v", err)
	}
	if err := ap.relaunch(); err != nil {
		t.Fatal(err)
	}
	if got := f.called("open "); len(got) != 1 || got[0] != "open -n "+app+" --args desktop --after-update" {
		t.Errorf("起動し直し = %v", got)
	}
	ap.cleanup()
	if _, err := os.Stat(dmg); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("起動し直した後に dmg が残っている: %v", err)
	}
}

// macOS の失敗: 確認（spctl・codesign）に通らない・識別子やチームが違う。どれも今の .app と前の .prev に手を付けず、
// 写したもの・dmg を残さない。置き場に書けないときだけ dmg を残して notWritableError で返す（呼ぶ側が dmg を開く）。
func TestApplyMacFailures(t *testing.T) {
	dmgBody := []byte("dmg bytes")
	for _, c := range []struct {
		name                 string
		fail                 map[string]bool
		id, newTeam, curTeam string
		ro                   bool
		want                 string
	}{
		{"dmg が spctl に通らない", map[string]bool{"spctl --assess": true}, "", "", "", false, "desktop.update.err.verify"},
		{"codesign --verify に通らない", map[string]bool{"codesign --verify": true}, "", "", "", false, "desktop.update.err.verify"},
		{"識別子が違う", nil, "com.example.other", "", "", false, "desktop.update.err.bundle_id"},
		{"チームが違う", nil, "", "TEAM2", "TEAM1", false, "desktop.update.err.team"},
		{"置き場に書けない", nil, "", "", "", true, "not_writable"},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := assetServer(t, dmgBody)
			dir, app, work := macFixture(t)
			f := &fakeMac{fail: c.fail, id: c.id, newTeam: c.newTeam, curTeam: c.curTeam, body: "new app"}
			if f.id == "" {
				f.id = BundleID
			}
			if f.newTeam == "" && f.curTeam == "" {
				f.newTeam, f.curTeam = "TEAM1", "TEAM1"
			}
			if c.ro {
				if os.Geteuid() == 0 {
					t.Skip("root では書き込みを拒めない")
				}
				if runtime.GOOS == "windows" {
					t.Skip("Windows ではディレクトリの chmod（読み取り専用の属性も）で中に作ることを拒めない。置き換えは Linux・macOS だけ")
				}
				os.Chmod(dir, 0o555)
				t.Cleanup(func() { os.Chmod(dir, 0o755) })
			}
			r := &replacer{GOOS: "darwin", Launcher: filepath.Join(app, "Contents", "MacOS", "looptrack"), WorkDir: work, BundleID: BundleID,
				Client: srv.Client(), Run: f.run}
			name := "Looptrack_v1.1.0_macos_universal.dmg"
			_, err := r.apply(context.Background(), signedResult(srv.URL+"/d", name, dmgBody))
			dmg := filepath.Join(work, name)
			var nw *notWritableError
			if c.want == "not_writable" {
				if !errors.As(err, &nw) || nw.DMG != dmg || nw.Dir != dir {
					t.Fatalf("err = %v, want notWritableError（dmg %s）", err, dmg)
				}
				if _, err := os.Stat(dmg); err != nil {
					t.Errorf("開くための dmg が残っていない: %v", err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), c.want) {
					t.Fatalf("err = %v, want %s", err, c.want)
				}
				if _, err := os.Stat(dmg); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("確かめに通らなかった dmg が残っている: %v", err)
				}
			}
			if got := readFile(t, filepath.Join(app, "Contents", "MacOS", "looptrack")); got != "old app" {
				t.Errorf("今の .app が変わった: %q", got)
			}
			if _, err := os.Stat(filepath.Join(app+".prev", "older")); err != nil {
				t.Errorf("前の .prev が変わった: %v", err)
			}
			if l := leftovers(t, dir); len(l) != 0 {
				t.Errorf("一時のディレクトリが残っている: %v", l)
			}
			if f.called("hdiutil attach") != nil && f.called("hdiutil detach") == nil {
				t.Error("マウントを外していない")
			}
		})
	}
}

// --- 起動からの通し（Linux の AppImage の形。GOOS を linux にして、どの OS でも走らせる） ---

type testSigner struct {
	pub  string
	priv ed25519.PrivateKey
	id   []byte
}

func newTestSigner() testSigner {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	id := []byte{8, 7, 6, 5, 4, 3, 2, 1}
	return testSigner{pub: base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), id...), pub...)), priv: priv, id: id}
}

// sign は minisign の既定（ED: 本文の BLAKE2b-512）の .minisig を作る。
func (s testSigner) sign(msg []byte, trusted string) []byte {
	h := blake2b.Sum512(msg)
	sig := ed25519.Sign(s.priv, h[:])
	global := ed25519.Sign(s.priv, append(append([]byte{}, sig...), trusted...))
	return []byte("untrusted comment: signature from minisign secret key\n" +
		base64.StdEncoding.EncodeToString(append(append([]byte("ED"), s.id...), sig...)) + "\n" +
		"trusted comment: " + trusted + "\n" + base64.StdEncoding.EncodeToString(global) + "\n")
}

// signedSource は署名つきのリリース（AppImage・SHA256SUMS・.minisig）を返す確認先の代わり。
func signedSource(t *testing.T, s testSigner, tag, asset string, body []byte) *httptest.Server {
	t.Helper()
	sums := []byte(sumHex(body) + "  " + asset + "\n")
	files := map[string][]byte{"/d/" + asset: body, "/d/SHA256SUMS": sums, "/d/SHA256SUMS.minisig": s.sign(sums, "looptrack "+tag+" SHA256SUMS")}
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/releases" {
			var assets []map[string]any
			for _, n := range []string{asset, "SHA256SUMS", "SHA256SUMS.minisig"} {
				assets = append(assets, map[string]any{"name": n, "browser_download_url": srv.URL + "/d/" + n, "size": len(files["/d/"+n])})
			}
			json.NewEncoder(w).Encode([]map[string]any{{"tag_name": tag, "prerelease": false,
				"html_url": "https://example.invalid/releases/tag/" + tag, "assets": assets}})
			return
		}
		b, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// startLinuxApp は GOOS=linux・APPIMAGE つきで起動し、新しい版の知らせが出るまで待つ（auto なら控えに "auto": true を書いてから）。
func startLinuxApp(t *testing.T, auto bool) (app *App, done chan int, cur string, newBody []byte, starts *[][]string, rec *recorder) {
	t.Helper()
	s := newTestSigner()
	asset := updatecheck.DesktopAsset("linux", runtime.GOARCH)("v1.1.0")
	if asset == "" {
		t.Skipf("この アーキテクチャ（%s）には AppImage の配布物が無い", runtime.GOARCH)
	}
	newBody = []byte("\x7fELF v1.1.0")
	src := signedSource(t, s, "v1.1.0", asset, newBody)
	dir := t.TempDir()
	appDir := filepath.Join(dir, "apps")
	os.MkdirAll(appDir, 0o755)
	cur = filepath.Join(appDir, "Looptrack.AppImage")
	os.WriteFile(cur, []byte("\x7fELF v1.0.0"), 0o755)
	data := filepath.Join(dir, "data")
	os.MkdirAll(data, 0o700)
	if auto {
		os.WriteFile(filepath.Join(data, updatecheck.FileName), []byte(`{"auto": true}`), 0o600)
	}
	e := env.FromMap(map[string]string{"LOOPTRACK_DATA_DIR": data, "LOOPTRACK_DESKTOP_PORT": "0", "LOOPTRACK_LANG": "ja",
		updatecheck.EnvURL: src.URL + "/releases", "APPIMAGE": cur, "XDG_DATA_HOME": filepath.Join(dir, "share")})
	rec = &recorder{}
	ui := &fakeUI{ran: make(chan *App, 1)}
	var mu sync.Mutex
	var st [][]string
	starts = &st
	done = make(chan int, 1)
	go func() {
		done <- Main([]string{"--background"}, Options{Version: "v1.0.0", Env: e, UI: ui, Open: rec.open, Alert: rec.alert, Home: dir,
			GOOS: "linux", UpdatePublicKey: s.pub, UpdateClient: src.Client(),
			UpdateStart: func(name string, args ...string) error {
				mu.Lock()
				defer mu.Unlock()
				st = append(st, append([]string{name}, args...))
				return nil
			}})
	}()
	select {
	case app = <-ui.ran:
	case code := <-done:
		// 自動の置き換えでは、トレイに渡した直後に終わる（ran と done が同時に揃う）ことがある
		select {
		case app = <-ui.ran:
			d := make(chan int, 1)
			d <- code
			done = d
		default:
			t.Fatalf("起動しないで終わった（%d）: %v", code, rec.alerts)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("起動しない")
	}
	return app, done, cur, newBody, starts, rec
}

func waitDone(t *testing.T, done chan int, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("%s: 終了しない", what)
	}
}

// トレイの「新しい版 <版> に更新する」: 確認（署名つき）→ UpdateReplaceable → ApplyUpdate で取得・置き換え・起動し直し → 今のインスタンスは終わる。
func TestMainApplyUpdateAppImage(t *testing.T) {
	app, done, cur, newBody, starts, rec := startLinuxApp(t, false)
	waitFor(t, "新しい版の知らせ", func() bool { return app.UpdateNotice() != nil })
	if !app.ReplaceSupported() || !app.UpdateReplaceable() {
		app.Quit()
		waitDone(t, done, "対照")
		t.Fatalf("署名つきの AppImage なのに置き換えられない（supported=%v）", app.ReplaceSupported())
	}
	if app.UpdateAutoEnabled() {
		t.Error("自動の置き換えが既定で入っている")
	}
	if !app.ApplyUpdate() {
		app.Quit()
		waitDone(t, done, "失敗")
		t.Fatalf("置き換えに進まない: %v", rec.alerts)
	}
	waitDone(t, done, "置き換えの後")
	if got := readFile(t, cur); got != string(newBody) {
		t.Errorf("置き換えた後の中身 = %q", got)
	}
	if got := readFile(t, cur+".prev"); got != "\x7fELF v1.0.0" {
		t.Errorf(".prev = %q", got)
	}
	if len(*starts) != 1 || strings.Join((*starts)[0], " ") != cur+" desktop --after-update" {
		t.Errorf("起動し直し = %v", *starts)
	}
	if len(rec.alerts) != 0 {
		t.Errorf("成功したのに知らせを出した: %v", rec.alerts)
	}
}

// 控えに "auto": true があれば、知らせが出たところで自動で置き換えて起動し直す（対照は上の TestMainApplyUpdateAppImage。既定では置き換えない）。
func TestMainAutoUpdateAppImage(t *testing.T) {
	_, done, cur, newBody, starts, _ := startLinuxApp(t, true)
	waitDone(t, done, "自動の置き換え")
	if got := readFile(t, cur); got != string(newBody) {
		t.Errorf("置き換えた後の中身 = %q", got)
	}
	if len(*starts) != 1 {
		t.Errorf("起動し直し = %v", *starts)
	}
}

// 既定（自動を入れていない）では、知らせが出ても置き換えない（自動の置き換えの対照）。SetUpdateAuto で入れると控えに残り、その場で置き換える。
func TestMainAutoUpdateDefaultOff(t *testing.T) {
	app, done, cur, _, starts, _ := startLinuxApp(t, false)
	waitFor(t, "新しい版の知らせ", func() bool { return app.UpdateNotice() != nil })
	waitFor(t, "知らせの反映", func() bool { return app.updates.handled.Load() > 0 })
	if got := readFile(t, cur); got != "\x7fELF v1.0.0" || len(*starts) != 0 {
		t.Fatalf("既定なのに置き換えた: %q %v", got, *starts)
	}
	if !app.SetUpdateAuto(true) {
		t.Fatal("自動を入れられない")
	}
	waitDone(t, done, "自動を入れた後")
	b, _ := os.ReadFile(filepath.Join(app.paths.DataDir, updatecheck.FileName))
	if !bytes.Contains(b, []byte(`"auto": true`)) {
		t.Errorf("控えに auto が無い: %s", b)
	}
	if len(*starts) != 1 {
		t.Errorf("起動し直し = %v", *starts)
	}
}

// --after-update は前のインスタンスがロックを放すまで待ってから起動し、ブラウザを開かない。
func TestMainAfterUpdateWaitsForLock(t *testing.T) {
	dir := t.TempDir()
	e := env.FromMap(map[string]string{"LOOPTRACK_DATA_DIR": dir, "LOOPTRACK_DESKTOP_PORT": "0", "LOOPTRACK_LANG": "ja",
		updatecheck.EnvCheck: "off"})
	paths, _ := ResolvePaths(e, runtime.GOOS, dir)
	release, ok, err := tryLock(paths.Lock())
	if err != nil || !ok {
		t.Fatalf("ロックを取れない: %v", err)
	}
	// 前のインスタンスがいる形（desktop.json に pid。ふつうの起動ならここで 2 つ目として終わる）
	writeState(paths.State(), state{PID: os.Getpid(), Port: 1, URL: "http://127.0.0.1:1/looptrack/"})
	rec := &recorder{}
	ui := &fakeUI{ran: make(chan *App, 1)}
	done := make(chan int, 1)
	go func() {
		done <- Main([]string{"--after-update"}, Options{Version: "v1.1.0", Env: e, UI: ui, Open: rec.open, Alert: rec.alert, Home: dir})
	}()
	select {
	case <-ui.ran:
		t.Fatal("ロックを持っている間に起動した")
	case code := <-done:
		t.Fatalf("待たずに終わった（%d）", code)
	case <-time.After(1 * time.Second):
	}
	release()
	var app *App
	select {
	case app = <-ui.ran:
	case code := <-done:
		t.Fatalf("起動しないで終わった（%d）: %v", code, rec.alerts)
	case <-time.After(20 * time.Second):
		t.Fatal("ロックを放しても起動しない")
	}
	app.Quit()
	waitDone(t, done, "--after-update")
	if u := rec.openedURLs(); len(u) != 0 {
		t.Errorf("--after-update でブラウザを開いた: %v", u)
	}
}

// --- Windows（インストーラ版と zip 版。GOOS を windows にして、どの OS でも一時ディレクトリで走らせる） ---

// winFixture は Windows のアプリのフォルダ（Looptrack.exe・cli\looptrack.exe・NOTICE・OFL-BIZUDGothic.txt）を作る。
// installer なら隣に unins000.exe も置く（インストーラで入れた形）。
func winFixture(t *testing.T, installer bool) (dir, cur string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "Looptrack")
	os.MkdirAll(filepath.Join(dir, "cli"), 0o755)
	cur = filepath.Join(dir, "Looptrack.exe")
	for name, body := range map[string]string{"Looptrack.exe": "MZ old gui", "cli/looptrack.exe": "MZ old cli",
		"NOTICE": "old notice", "OFL-BIZUDGothic.txt": "old ofl"} {
		os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(body), 0o755)
	}
	if installer {
		os.WriteFile(filepath.Join(dir, "unins000.exe"), []byte("MZ uninstaller"), 0o755)
	}
	return dir, cur
}

// TestWindowsInstallKind は、実行ファイルの隣に unins000.exe があればインストーラ（setup.exe）、無ければ zip を確認で照らし、
// 資産の名前の末尾が入れ方と合わない結果（控えの last_ok が前の入れ方のものなど）は置き換えに使わないことを確かめる（受け入れ条件 2）。
func TestWindowsInstallKind(t *testing.T) {
	_, zipCur := winFixture(t, false)
	_, instCur := winFixture(t, true)
	if !installedByInstaller(instCur) || installedByInstaller(zipCur) || installedByInstaller("") {
		t.Fatalf("unins000.exe での判定が違う（inst=%v zip=%v）", installedByInstaller(instCur), installedByInstaller(zipCur))
	}
	// unins000.exe がディレクトリなら、インストーラで入れたものとはみなさない
	_, dirCur := winFixture(t, false)
	os.Mkdir(filepath.Join(filepath.Dir(dirCur), "unins000.exe"), 0o755)
	if installedByInstaller(dirCur) {
		t.Error("ディレクトリの unins000.exe でインストーラ版とみなした")
	}
	if got := desktopAssetFor("windows", "amd64", instCur)("v1.1.0"); got != "Looptrack_v1.1.0_windows_amd64_setup.exe" {
		t.Errorf("インストーラ版の資産 = %q", got)
	}
	if got := desktopAssetFor("windows", "arm64", zipCur)("v1.1.0"); got != "Looptrack_v1.1.0_windows_arm64.zip" {
		t.Errorf("zip 版の資産 = %q", got)
	}
	// unins000.exe は Windows のときだけ見る（ほかの OS は今までどおり）
	if got := desktopAssetFor("linux", "amd64", instCur)("v1.1.0"); got != "Looptrack_v1.1.0_linux_x86_64.AppImage" {
		t.Errorf("Linux の資産 = %q", got)
	}

	setup := signedResult("https://example.invalid/s", "Looptrack_v1.1.0_windows_amd64_setup.exe", []byte("MZ"))
	zipRes := signedResult("https://example.invalid/z", "Looptrack_v1.1.0_windows_amd64.zip", []byte("PK"))
	for _, c := range []struct {
		name string
		cur  string
		src  *updatecheck.Result
		want string // 空なら通る（対照）
	}{
		{"インストーラ版に setup.exe（対照）", instCur, setup, ""},
		{"zip 版に zip（対照）", zipCur, zipRes, ""},
		{"インストーラ版に zip", instCur, zipRes, "desktop.update.err.asset_not_installer"},
		{"zip 版に setup.exe", zipCur, setup, "desktop.update.err.asset_not_zip"},
	} {
		r := &replacer{GOOS: "windows", Launcher: c.cur}
		_, err := r.ready(c.src)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: 通らない: %v", c.name, err)
		case c.want != "" && (err == nil || err.Error() != c.want):
			t.Errorf("%s: err = %v, want %s", c.name, err, c.want)
		}
	}
	// 合わない結果では apply も何もしない（取得もしない）
	srv, hits := assetServer(t, []byte("PK"))
	r := &replacer{GOOS: "windows", Launcher: instCur, Client: srv.Client(), WorkDir: t.TempDir(),
		Start: func(string, ...string) error { t.Error("合わない資産で起動した"); return nil }}
	if _, err := r.apply(context.Background(), signedResult(srv.URL+"/z", "Looptrack_v1.1.0_windows_amd64.zip", []byte("PK"))); err == nil ||
		err.Error() != "desktop.update.err.asset_not_installer" {
		t.Errorf("apply: err = %v", err)
	}
	if *hits != 0 {
		t.Errorf("合わない資産を取得した（%d 回）", *hits)
	}
}

// hasTasksArg は setup.exe の引数に /TASKS（選択肢の指定）があるか。
func hasTasksArg(args []string) bool {
	for _, a := range args {
		if strings.HasPrefix(strings.ToUpper(a), "/TASKS") {
			return true
		}
	}
	return false
}

// インストーラ版: setup.exe をデータの置き場の updates に取得して SHA-256 を照らし、起動し直しで setup.exe を無人の引数で起こす。
// /TASKS は渡さない。Start が失敗しても Looptrack.exe は変わらず、.prev・.failed を作らない（受け入れ条件 3）。
func TestApplyWindowsInstaller(t *testing.T) {
	body := []byte("MZ setup v1.1.0")
	name := "Looptrack_v1.1.0_windows_amd64_setup.exe"
	// /TASKS を見分けられること（下の「含まない」の対照）。smoke の最初の導入は /TASKS=startup,cli を付ける
	if !hasTasksArg([]string{"/VERYSILENT", "/TASKS=startup,cli"}) {
		t.Fatal("前提が崩れています: /TASKS を含む引数を見分けられない")
	}
	for _, startErr := range []error{nil, errors.New("CreateProcess failed")} {
		t.Run(fmt.Sprintf("Start の誤り=%v", startErr), func(t *testing.T) {
			srv, _ := assetServer(t, body)
			dir, cur := winFixture(t, true)
			work := filepath.Join(t.TempDir(), "data", "updates")
			os.MkdirAll(work, 0o700)
			old := filepath.Join(work, "Looptrack_v1.0.5_windows_amd64_setup.exe")
			os.WriteFile(old, []byte("MZ older setup"), 0o644)
			var started [][]string
			r := &replacer{GOOS: "windows", Launcher: cur, WorkDir: work, Client: srv.Client(),
				Start: func(n string, args ...string) error {
					started = append(started, append([]string{n}, args...))
					return startErr
				}}
			ap, err := r.apply(context.Background(), signedResult(srv.URL+"/s", name, body))
			if err != nil {
				t.Fatal(err)
			}
			setup := filepath.Join(work, name)
			if got := readFile(t, setup); got != string(body) {
				t.Errorf("取得した setup.exe = %q", got)
			}
			if _, err := os.Stat(old); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("前に取得した setup.exe が残っている: %v", err)
			}
			if len(started) != 0 {
				t.Fatalf("relaunch の前に起動した: %v", started)
			}
			rerr := ap.relaunch()
			if !errors.Is(rerr, startErr) {
				t.Errorf("relaunch = %v, want %v", rerr, startErr)
			}
			if len(started) != 1 || started[0][0] != setup {
				t.Fatalf("起こしたもの = %v, want %s", started, setup)
			}
			args := started[0][1:]
			if len(args) != len(installerArgs)+1 || strings.Join(args[:len(installerArgs)], " ") != "/VERYSILENT /SUPPRESSMSGBOXES /NORESTART /SP- /RELAUNCH=1" {
				t.Errorf("setup.exe の引数 = %v", args)
			}
			if want := "/LOG=" + filepath.Join(work, "setup.log"); args[len(args)-1] != want {
				t.Errorf("/LOG = %q, want %q", args[len(args)-1], want)
			}
			if hasTasksArg(args) {
				t.Errorf("/TASKS を渡した（前回の選択肢を引き継がない）: %v", args)
			}
			// 戻すものは無い（undo は何もしない）。Looptrack.exe は変わらず、.prev・.failed は作らない
			if err := ap.undo(); err != nil {
				t.Errorf("undo = %v", err)
			}
			ap.cleanup()
			if got := readFile(t, cur); got != "MZ old gui" {
				t.Errorf("Looptrack.exe が変わった: %q", got)
			}
			for _, p := range []string{cur + ".prev", cur + ".failed"} {
				if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("%s ができた: %v", filepath.Base(p), err)
				}
			}
			if l := leftovers(t, dir); len(l) != 0 {
				t.Errorf("一時のものが残っている: %v", l)
			}
		})
	}
}

// インストーラ版の失敗: SHA-256 が違う・MZ でない。どちらも起こさず、取得物（.part・setup.exe）を残さない（受け入れ条件 3 の補い）。
func TestApplyWindowsInstallerFailures(t *testing.T) {
	name := "Looptrack_v1.1.0_windows_amd64_setup.exe"
	for _, c := range []struct {
		name        string
		serve, sign []byte
		want        string
	}{
		{"SHA-256 が違う", []byte("MZ tampered"), []byte("MZ setup"), "desktop.update.err.sha256"},
		{"MZ でない", []byte("#!/bin/sh"), []byte("#!/bin/sh"), "desktop.update.err.not_pe"},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := assetServer(t, c.serve)
			_, cur := winFixture(t, true)
			work := t.TempDir()
			r := &replacer{GOOS: "windows", Launcher: cur, WorkDir: work, Client: srv.Client(),
				Start: func(string, ...string) error { t.Error("失敗したのに起こした"); return nil }}
			res := signedResult(srv.URL+"/s", name, c.sign)
			res.Asset.Size = 0
			if _, err := r.apply(context.Background(), res); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %s", err, c.want)
			}
			ents, _ := os.ReadDir(work)
			if len(ents) != 0 {
				var names []string
				for _, e := range ents {
					names = append(names, e.Name())
				}
				t.Errorf("取得物が残っている: %v", names)
			}
		})
	}
}

// zipEntry は Windows の zip の 1 項目（dir なら body は使わない。symlink ならシンボリックリンクの項目）。
type zipEntry struct {
	name, body   string
	dir, symlink bool
	declared     uint64 // 0 でなければ、項目に書く大きさ（実際の中身より小さく偽る。圧縮せずに書く）
}

// appZip は desktop.sh の windows-zip と同じ形の zip（Looptrack/ の下に 4 ファイルとディレクトリの項目）を作る。
func appZip(t *testing.T, es []zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range es {
		if e.declared > 0 {
			h := &zip.FileHeader{Name: e.name, Method: zip.Store, CRC32: crc32.ChecksumIEEE([]byte(e.body)),
				CompressedSize64: uint64(len(e.body)), UncompressedSize64: e.declared}
			h.SetMode(0o755)
			w, err := zw.CreateRaw(h)
			if err != nil {
				t.Fatal(err)
			}
			w.Write([]byte(e.body))
			continue
		}
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		switch {
		case e.dir:
			h.SetMode(fs.ModeDir | 0o755)
		case e.symlink:
			h.SetMode(fs.ModeSymlink | 0o777)
		default:
			h.SetMode(0o755)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if !e.dir {
			w.Write([]byte(e.body))
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func goodZipEntries() []zipEntry {
	return []zipEntry{
		{name: "Looptrack/", dir: true},
		{name: "Looptrack/Looptrack.exe", body: "MZ new gui"},
		{name: "Looptrack/cli/", dir: true},
		{name: "Looptrack/cli/looptrack.exe", body: "MZ new cli"},
		{name: "Looptrack/NOTICE", body: "new notice"},
		{name: "Looptrack/OFL-BIZUDGothic.txt", body: "new ofl"},
	}
}

var winOld = map[string]string{"Looptrack.exe": "MZ old gui", "cli/looptrack.exe": "MZ old cli", "NOTICE": "old notice", "OFL-BIZUDGothic.txt": "old ofl"}

// assertWinFiles は dir の 4 ファイルの中身が want と同じか。
func assertWinFiles(t *testing.T, dir string, want map[string]string, suffix string) {
	t.Helper()
	for name, body := range want {
		if got := readFile(t, filepath.Join(dir, filepath.FromSlash(name))+suffix); got != body {
			t.Errorf("%s%s = %q, want %q", name, suffix, got, body)
		}
	}
}

// zip 版: 同じフォルダの一時のディレクトリに取得して 4 ファイルを取り出し、1 つずつ .prev に退けて置き換える。
// 起動し直しは Looptrack.exe desktop --after-update。起動し直せなければ 4 つとも前の版に戻す（受け入れ条件 4）。
func TestApplyWindowsZip(t *testing.T) {
	body := appZip(t, goodZipEntries())
	srv, _ := assetServer(t, body)
	dir, cur := winFixture(t, false)
	os.WriteFile(cur+".prev", []byte("MZ older gui"), 0o755)
	var started [][]string
	r := &replacer{GOOS: "windows", Launcher: cur, WorkDir: t.TempDir(), Client: srv.Client(),
		Start: func(n string, args ...string) error {
			started = append(started, append([]string{n}, args...))
			return nil
		}}
	ap, err := r.apply(context.Background(), signedResult(srv.URL+"/z", "Looptrack_v1.1.0_windows_amd64.zip", body))
	if err != nil {
		t.Fatal(err)
	}
	assertWinFiles(t, dir, map[string]string{"Looptrack.exe": "MZ new gui", "cli/looptrack.exe": "MZ new cli",
		"NOTICE": "new notice", "OFL-BIZUDGothic.txt": "new ofl"}, "")
	assertWinFiles(t, dir, winOld, ".prev") // 前の版は .prev に（前の Looptrack.exe.prev は消える）
	if l := leftovers(t, dir); len(l) != 0 {
		t.Errorf("一時のものが残っている: %v", l)
	}
	if len(started) != 0 {
		t.Fatalf("relaunch の前に起動した: %v", started)
	}
	if err := ap.relaunch(); err != nil {
		t.Fatal(err)
	}
	if want := []string{cur, "desktop", "--after-update"}; len(started) != 1 || strings.Join(started[0], " ") != strings.Join(want, " ") {
		t.Errorf("起動し直し = %v, want %v", started, want)
	}
	// 起動し直せなかったときの戻し: 4 つとも前の版に戻り、.prev と .failed は残らない
	if err := ap.undo(); err != nil {
		t.Fatal(err)
	}
	assertWinFiles(t, dir, winOld, "")
	for name := range winOld {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name)) + ".prev"); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("戻した後に %s.prev が残っている: %v", name, err)
		}
	}
	if l := leftovers(t, dir); len(l) != 0 {
		t.Errorf("戻した後に一時のものが残っている: %v", l)
	}
	if l := leftovers(t, filepath.Join(dir, "cli")); len(l) != 0 {
		t.Errorf("戻した後に cli に一時のものが残っている: %v", l)
	}
}

// zip 版で、今のフォルダに無いファイル（利用者が消した NOTICE など）は .prev を作らずに置き、戻すときは消す。
func TestApplyWindowsZipMissingFile(t *testing.T) {
	body := appZip(t, goodZipEntries())
	srv, _ := assetServer(t, body)
	dir, cur := winFixture(t, false)
	os.Remove(filepath.Join(dir, "NOTICE"))
	r := &replacer{GOOS: "windows", Launcher: cur, Client: srv.Client(), Start: func(string, ...string) error { return nil }}
	ap, err := r.apply(context.Background(), signedResult(srv.URL+"/z", "Looptrack_v1.1.0_windows_amd64.zip", body))
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dir, "NOTICE")); got != "new notice" {
		t.Errorf("NOTICE = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "NOTICE.prev")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("無かったものの .prev ができた: %v", err)
	}
	if err := ap.undo(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "NOTICE")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("戻した後に新しい NOTICE が残っている: %v", err)
	}
	if got := readFile(t, cur); got != "MZ old gui" {
		t.Errorf("戻した後の Looptrack.exe = %q", got)
	}
}

// zip 版の失敗: SHA-256 が違う・.. を含む項目・欠けたファイル・シンボリックリンク・同じ名前が 2 つ・大きすぎる・MZ でない・
// 2 つ目以降の swap の失敗・置き場に書けない。どれも 4 ファイルを今の版のまま（済んだ分は戻す）にし、一時のものを残さない（受け入れ条件 5）。
func TestApplyWindowsZipFailures(t *testing.T) {
	good := goodZipEntries()
	with := func(mod func([]zipEntry) []zipEntry) []zipEntry {
		return mod(append([]zipEntry(nil), good...))
	}
	for _, c := range []struct {
		name       string
		entries    []zipEntry
		tamper     bool  // 署名された SHA-256 と違うものを返す
		failSwapAt int   // この回の swap を失敗させる（1 から数える。0 は失敗させない）
		limit      int64 // maxZipEntry（0 は既定）
		ro         bool
		want       string
		keepPrev   bool // 前からある Looptrack.exe.prev が残ること（swap に入る前の失敗）
	}{
		{name: "SHA-256 が違う", entries: good, tamper: true, want: "desktop.update.err.sha256", keepPrev: true},
		{name: ".. を含む項目", entries: with(func(es []zipEntry) []zipEntry {
			return append(es, zipEntry{name: "Looptrack/../evil.exe", body: "MZ"})
		}), want: "selfupdate.err.archive_entry", keepPrev: true},
		{name: "欠けたファイル", entries: with(func(es []zipEntry) []zipEntry { return es[:len(es)-1] }),
			want: "selfupdate.err.archive_no_binary", keepPrev: true},
		{name: "シンボリックリンク", entries: with(func(es []zipEntry) []zipEntry {
			es[4] = zipEntry{name: "Looptrack/NOTICE", body: "/etc/passwd", symlink: true}
			return es
		}), want: "selfupdate.err.archive_not_regular", keepPrev: true},
		{name: "同じ名前が 2 つ", entries: with(func(es []zipEntry) []zipEntry {
			return append(es, zipEntry{name: "Looptrack/NOTICE", body: "again"})
		}), want: "selfupdate.err.archive_dup", keepPrev: true},
		{name: "大きすぎる", entries: good, limit: 4, want: "selfupdate.err.binary_too_large", keepPrev: true},
		{name: "大きさを偽った項目", entries: with(func(es []zipEntry) []zipEntry {
			es[4] = zipEntry{name: "Looptrack/NOTICE", body: "notice that is longer than declared", declared: 3}
			return es
		}), want: "selfupdate.err.archive_read", keepPrev: true},
		{name: "MZ でない", entries: with(func(es []zipEntry) []zipEntry {
			es[1] = zipEntry{name: "Looptrack/Looptrack.exe", body: "#!/bin/sh"}
			return es
		}), want: "desktop.update.err.not_pe", keepPrev: true},
		{name: "2 つ目の swap の失敗", entries: good, failSwapAt: 2, want: "desktop.update.err.replace"},
		{name: "4 つ目の swap の失敗", entries: good, failSwapAt: 4, want: "desktop.update.err.replace"},
		{name: "置き場に書けない", entries: good, ro: true, want: "not_writable", keepPrev: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			body := appZip(t, c.entries)
			served := body
			if c.tamper {
				served = appZip(t, with(func(es []zipEntry) []zipEntry {
					es[4].body = "tampered"
					return es
				}))
			}
			srv, _ := assetServer(t, served)
			dir, cur := winFixture(t, false)
			os.WriteFile(cur+".prev", []byte("MZ older gui"), 0o755)
			if c.limit > 0 {
				orig := maxZipEntry
				maxZipEntry = c.limit
				t.Cleanup(func() { maxZipEntry = orig })
			}
			if c.ro {
				if os.Geteuid() == 0 {
					t.Skip("root では書き込みを拒めない")
				}
				if runtime.GOOS == "windows" {
					t.Skip("Windows ではディレクトリの chmod（読み取り専用の属性も）で中に作ることを拒めない")
				}
				os.Chmod(dir, 0o555)
				t.Cleanup(func() { os.Chmod(dir, 0o755) })
			}
			swaps := 0
			r := &replacer{GOOS: "windows", Launcher: cur, Client: srv.Client(),
				Start: func(string, ...string) error { t.Error("失敗したのに起動した"); return nil },
				Swap: func(cur, next, prev string) error {
					swaps++
					if swaps == c.failSwapAt {
						return errors.New("rename: the process cannot access the file")
					}
					return swap(cur, next, prev)
				}}
			_, err := r.apply(context.Background(), signedResult(srv.URL+"/z", "Looptrack_v1.1.0_windows_amd64.zip", body))
			var nw *notWritableError
			switch {
			case c.want == "not_writable":
				if !errors.As(err, &nw) || nw.Dir != dir || nw.DMG != "" {
					t.Fatalf("err = %v, want notWritableError（%s）", err, dir)
				}
			case err == nil || !strings.Contains(err.Error(), c.want):
				t.Fatalf("err = %v, want %s", err, c.want)
			}
			// 失敗させた回まで swap に進んだこと（2 つ目以降の失敗の対照。進まずに落ちたなら戻しを確かめていない）
			if c.failSwapAt > 0 && swaps != c.failSwapAt {
				t.Errorf("swap を %d 回呼んだ（%d 回目で失敗させるはず）", swaps, c.failSwapAt)
			}
			assertWinFiles(t, dir, winOld, "")
			for _, name := range []string{"cli/looptrack.exe", "NOTICE", "OFL-BIZUDGothic.txt"} {
				if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name)) + ".prev"); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("%s.prev が残っている: %v", name, err)
				}
			}
			if c.keepPrev {
				if got := readFile(t, cur+".prev"); got != "MZ older gui" {
					t.Errorf("前からある Looptrack.exe.prev が変わった: %q", got)
				}
			}
			if l := leftovers(t, dir); len(l) != 0 {
				t.Errorf("一時のものが残っている: %v", l)
			}
			if l := leftovers(t, filepath.Join(dir, "cli")); len(l) != 0 {
				t.Errorf("cli に一時のものが残っている: %v", l)
			}
		})
	}
}

// TestCopyLimited は、項目に書かれた大きさとは別に、実際に読んだ大きさが maxZipEntry を超えたら止めることを確かめる
// （archive/zip は書かれた大きさより多く読むと誤りを返すので、zip を通すとこの分岐には届かない。ここで直に通す）。
func TestCopyLimited(t *testing.T) {
	orig := maxZipEntry
	maxZipEntry = 4
	t.Cleanup(func() { maxZipEntry = orig })
	var buf bytes.Buffer
	// 対照: ちょうど上限なら通る
	if err := copyLimited(&buf, strings.NewReader("1234"), "z.zip", "Looptrack/NOTICE"); err != nil || buf.String() != "1234" {
		t.Fatalf("前提が崩れています: 上限ちょうどが通らない: %v %q", err, buf.String())
	}
	buf.Reset()
	if err := copyLimited(&buf, strings.NewReader("12345"), "z.zip", "Looptrack/NOTICE"); err == nil ||
		!strings.Contains(err.Error(), "selfupdate.err.binary_too_large") {
		t.Errorf("上限を超えたのに止まらない: %v", err)
	}
}

// TestPlaceRemovesMadeDir は、置くために作ったディレクトリを戻すときに消し、前からあったディレクトリは残すことを確かめる。
func TestPlaceRemovesMadeDir(t *testing.T) {
	base := t.TempDir()
	for _, c := range []struct {
		name    string
		preDir  bool // 置き先のディレクトリが前からある（対照）
		wantDir bool // 戻した後にディレクトリが残る
	}{
		{"ディレクトリを作った", false, false},
		{"ディレクトリは前からある（対照）", true, true},
	} {
		dir := filepath.Join(base, c.name, "cli")
		os.MkdirAll(filepath.Dir(dir), 0o755)
		if c.preDir {
			os.Mkdir(dir, 0o755)
		}
		next := filepath.Join(base, c.name, "next")
		os.WriteFile(next, []byte("MZ"), 0o755)
		dst := filepath.Join(dir, "looptrack.exe")
		m, err := place(swap, dst, next)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := readFile(t, dst); got != "MZ" {
			t.Errorf("%s: 置いた中身 = %q", c.name, got)
		}
		if (m.madeDir != "") == c.preDir {
			t.Errorf("%s: madeDir = %q", c.name, m.madeDir)
		}
		if err := (&applied{moves: []move{m}}).undo(); err != nil {
			t.Fatalf("%s: undo: %v", c.name, err)
		}
		_, err = os.Stat(dir)
		if exists := err == nil; exists != c.wantDir {
			t.Errorf("%s: 戻した後にディレクトリが残っている = %v, want %v", c.name, exists, c.wantDir)
		}
		if _, err := os.Stat(dst); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s: 戻した後に置いたものが残っている: %v", c.name, err)
		}
	}
}
