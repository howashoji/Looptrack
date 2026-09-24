package desktop

import (
	"bytes"
	"encoding/xml"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/i18n"
)

func TestResolvePaths(t *testing.T) {
	for _, c := range []struct {
		goos      string
		vars      map[string]string
		home      string
		data, log string
	}{
		{"darwin", nil, "/Users/a", "/Users/a/Library/Application Support/Looptrack", "/Users/a/Library/Logs/Looptrack"},
		{"linux", nil, "/home/a", "/home/a/.local/share/looptrack", "/home/a/.local/state/looptrack"},
		{"linux", map[string]string{"XDG_DATA_HOME": "/x/data", "XDG_STATE_HOME": "/x/state"}, "/home/a", "/x/data/looptrack", "/x/state/looptrack"},
		{"windows", map[string]string{"LOCALAPPDATA": `C:\Users\a\AppData\Local`}, `C:\Users\a`, `C:\Users\a\AppData\Local\Looptrack`, `C:\Users\a\AppData\Local\Looptrack\logs`},
		{"windows", nil, `C:\Users\a`, `C:\Users\a\AppData\Local\Looptrack`, `C:\Users\a\AppData\Local\Looptrack\logs`},
		{"darwin", map[string]string{"LOOPTRACK_DATA_DIR": "/tmp/lt"}, "/Users/a", "/tmp/lt", "/tmp/lt/logs"},
	} {
		p, err := ResolvePaths(env.FromMap(c.vars), c.goos, c.home)
		if err != nil {
			t.Fatalf("%s %v: %v", c.goos, c.vars, err)
		}
		if p.DataDir != c.data || p.LogDir != c.log {
			t.Errorf("%s %v = %+v, want %s・%s", c.goos, c.vars, p, c.data, c.log)
		}
	}
	if _, err := ResolvePaths(env.FromMap(nil), "darwin", ""); err == nil {
		t.Error("ホームが分からないのにエラーにならない")
	}
}

func TestAutostartContent(t *testing.T) {
	argv := []string{"/Applications/Looptrack & Co.app/Contents/MacOS/looptrack", "desktop", "--background"}
	plist := launchAgentPlist("net.example.looptrack", argv)
	if err := xml.Unmarshal(plist, new(struct{})); err != nil {
		t.Fatalf("plist が XML として読めない: %v\n%s", err, plist)
	}
	for _, want := range []string{"<string>net.example.looptrack</string>", "Looptrack &amp; Co.app", "<key>RunAtLoad</key>", "<string>--background</string>"} {
		if !bytes.Contains(plist, []byte(want)) {
			t.Errorf("plist に %q が無い:\n%s", want, plist)
		}
	}
	if bytes.Contains(plist, []byte("KeepAlive")) {
		t.Error("KeepAlive を付けると終了しても再起動される")
	}

	entry := autostartDesktopEntry([]string{"/home/a/Apps/Looptrack v1.AppImage", "desktop", "--background"})
	if !strings.Contains(entry, "\nExec=\"/home/a/Apps/Looptrack v1.AppImage\" desktop --background\n") {
		t.Errorf(".desktop の Exec:\n%s", entry)
	}
	for in, want := range map[string]string{
		"/a/b":      "/a/b",
		"/a b":      `"/a b"`,
		`/a"b`:      `"/a\\"b"`,
		`/a$b`:      `"/a\\$b"`,
		`/a\b`:      `"/a\\\\b"`,
		"/100%/x":   "/100%%/x",
		"/a b/50%":  `"/a b/50%%"`,
		"/a`b":      "\"/a\\\\`b\"",
		"":          `""`,
		"/tmp/x;rm": `"/tmp/x;rm"`,
	} {
		if got := desktopExecQuote(in); got != want {
			t.Errorf("desktopExecQuote(%q) = %s, want %s", in, got, want)
		}
	}

	if got := windowsCommandLine([]string{`C:\Users\a b\Looptrack\Looptrack.exe`, "desktop", "--background"}); got != `"C:\Users\a b\Looptrack\Looptrack.exe" desktop --background` {
		t.Errorf("windowsCommandLine = %s", got)
	}
	if got := windowsQuoteArg(`C:\a b\`); got != `"C:\a b\\"` {
		t.Errorf("windowsQuoteArg = %s", got)
	}

	// Run の値から実行ファイルのパスを取り出す（アンインストールで「このアプリのものか」を見るのに使う）
	for in, want := range map[string]string{
		`"C:\Users\a b\Looptrack Desktop\Looptrack.exe" desktop --background`: `C:\Users\a b\Looptrack Desktop\Looptrack.exe`,
		`C:\apps\Looptrack.exe desktop --background`:                          `C:\apps\Looptrack.exe`,
		`  "C:\x\y.exe"`:   `C:\x\y.exe`,
		`"C:\unterminated`: `C:\unterminated`,
		"":                 "",
	} {
		if got := windowsCommandArg0(in); got != want {
			t.Errorf("windowsCommandArg0(%q) = %q, want %q", in, got, want)
		}
	}
	if !samePath(`C:\A\Looptrack.exe`, `c:/a/looptrack.exe`) {
		t.Error("samePath: 大小・区切りの違いを同じとみなしていない")
	}
	if samePath("", "") || samePath(`C:\a.exe`, `C:\b.exe`) {
		t.Error("samePath: 別のパス（か空）を同じとみなした")
	}
}

// アンインストールの後始末: このアプリの登録だけを消し、別の場所のアプリの登録は残す。
func TestAutostartRemoveIfOurs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("レジストリ（HKCU\\...\\Run）は実機で確かめる")
	}
	for _, goos := range []string{"darwin", "linux"} {
		home := t.TempDir()
		a := Autostart{GOOS: goos, Home: home, Label: "net.example.looptrack", Argv: []string{"/opt/Looptrack/looptrack", "desktop", "--background"}}
		if removed, err := a.RemoveIfOurs(); removed || err != nil {
			t.Errorf("%s: 登録が無いのに %v %v", goos, removed, err)
		}
		if err := a.Enable(); err != nil {
			t.Fatal(err)
		}
		// 別の場所に置いたアプリ（持ち運び用）からの呼び出しでは消さない
		other := a
		other.Argv = []string{"/home/a/Apps/Looptrack.AppImage", "desktop", "--background"}
		if removed, err := other.RemoveIfOurs(); removed || err != nil {
			t.Errorf("%s: 別のアプリの登録を消した: %v %v", goos, removed, err)
		}
		if on, _ := a.Enabled(); !on {
			t.Fatalf("%s: 登録が消えている", goos)
		}
		if removed, err := a.RemoveIfOurs(); !removed || err != nil {
			t.Errorf("%s: 自分の登録を消せない: %v %v", goos, removed, err)
		}
		if on, _ := a.Enabled(); on {
			t.Errorf("%s: 消えていない", goos)
		}
	}
}

// macOS・Linux の登録を一時的な HOME で作る・直す・消す（利用者の本物の ~/Library/LaunchAgents・~/.config/autostart には触れない）。
func TestAutostartEnableDisable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("レジストリ（HKCU\\...\\Run）は実機で確かめる")
	}
	for _, goos := range []string{"darwin", "linux"} {
		home := t.TempDir()
		a := Autostart{GOOS: goos, Home: home, Label: "net.example.looptrack", Argv: []string{"/opt/Looptrack/looptrack", "desktop", "--background"}}
		if !strings.HasPrefix(a.Path(), home) {
			t.Fatalf("%s: 登録の場所が一時的な HOME の外: %s", goos, a.Path())
		}
		if on, err := a.Enabled(); on || err != nil {
			t.Fatalf("%s: 最初から登録がある: %v %v", goos, on, err)
		}
		if err := a.Enable(); err != nil {
			t.Fatal(err)
		}
		if on, _ := a.Enabled(); !on || !a.Current() {
			t.Errorf("%s: 登録できていない", goos)
		}
		moved := a
		moved.Argv = []string{"/elsewhere/looptrack", "desktop", "--background"}
		if on, _ := moved.Enabled(); !on || moved.Current() {
			t.Errorf("%s: アプリを動かしたら Current が false になるはず", goos)
		}
		if err := a.Disable(); err != nil {
			t.Fatal(err)
		}
		if on, _ := a.Enabled(); on {
			t.Errorf("%s: 消えていない", goos)
		}
		if err := a.Disable(); err != nil {
			t.Errorf("%s: 無いものを消してエラー: %v", goos, err)
		}
	}
	a := Autostart{GOOS: "linux", Home: "/h", ConfigHome: "/cfg"}
	if a.Path() != filepath.Join("/cfg", "autostart", "looptrack.desktop") {
		t.Errorf("XDG_CONFIG_HOME を見ていない: %s", a.Path())
	}
}

func TestCLIInstallSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows はコピー（TestCLIInstallWindowsCopy）")
	}
	home := t.TempDir()
	apps := t.TempDir()
	app1 := filepath.Join(apps, "Looptrack.app", "Contents", "MacOS", "looptrack")
	app2 := filepath.Join(apps, "Looptrack-v2.AppImage")
	c := CLIInstall{GOOS: runtime.GOOS, Home: home, Launcher: app1, PathEnv: "/usr/bin"}
	if c.State() != CLIMissing {
		t.Fatal("最初から置き場にある")
	}
	msg, err := c.Install(i18n.JA)
	if err != nil {
		t.Fatal(err)
	}
	if dest, _ := os.Readlink(c.Target()); dest != app1 {
		t.Errorf("リンクの先 = %s", dest)
	}
	if !strings.Contains(msg, "PATH") {
		t.Errorf("PATH に無いときの案内が無い: %s", msg)
	}
	if c.State() != CLIOurs {
		t.Error("作った後が CLIOurs でない")
	}
	c2 := c
	c2.Launcher = app2
	c2.PathEnv = filepath.Join(home, ".local", "bin") + ":/usr/bin"
	if c2.State() != CLIStale {
		t.Error("別の場所のアプリへのリンクが CLIStale でない")
	}
	if fixed, err := c2.Refresh(); !fixed || err != nil {
		t.Errorf("Refresh = %v %v", fixed, err)
	}
	if dest, _ := os.Readlink(c.Target()); dest != app2 {
		t.Errorf("直した後のリンクの先 = %s", dest)
	}
	if msg, _ := c2.Install(i18n.JA); strings.Contains(msg, "PATH") {
		t.Errorf("PATH にあるのに案内が出る: %s", msg)
	}

	// 利用者が自分で作ったリンク・setup で入れた普通のファイルは触らない
	os.Remove(c.Target())
	os.Symlink("/opt/mine/looptrack", c.Target())
	if c.State() != CLIOther {
		t.Error("利用者のリンクが CLIOther でない")
	}
	if fixed, _ := c.Refresh(); fixed {
		t.Error("利用者のリンクを直した")
	}
	os.Remove(c.Target())
	os.WriteFile(c.Target(), []byte("setup で入れたもの"), 0o755)
	if msg, err := c.Install(i18n.JA); err != nil || !strings.Contains(msg, "既にあります") {
		t.Errorf("普通のファイルがあるとき: %s %v", msg, err)
	}
	if b, _ := os.ReadFile(c.Target()); string(b) != "setup で入れたもの" {
		t.Error("setup で入れたものを書き換えた")
	}
	// アンインストール: setup で入れた普通のファイルも、別の場所のアプリへのリンク（CLIStale）も残す
	if removed, err := c.Uninstall(); removed || err != nil {
		t.Errorf("setup で入れたものを消した: %v %v", removed, err)
	}
	os.Remove(c.Target())
	os.Symlink(app2, c.Target())
	if removed, err := c.Uninstall(); removed || err != nil {
		t.Errorf("別の場所のアプリへのリンクを消した: %v %v", removed, err)
	}
	// このアプリへのリンクだけを消す
	os.Remove(c.Target())
	if _, err := c.Install(i18n.JA); err != nil {
		t.Fatal(err)
	}
	if removed, err := c.Uninstall(); !removed || err != nil {
		t.Fatalf("Uninstall = %v %v", removed, err)
	}
	if _, err := os.Lstat(c.Target()); err == nil {
		t.Error("リンクが残っている")
	}
}

// Windows のコピーの手順（置き場の決め方・印・古いものの置き換え）は他の OS でも同じ処理なので、ここで確かめる。
func TestCLIInstallWindowsCopy(t *testing.T) {
	base := t.TempDir()
	app := filepath.Join(base, "Looptrack")
	os.MkdirAll(filepath.Join(app, "cli"), 0o755)
	bundled := filepath.Join(app, "cli", "looptrack.exe")
	os.WriteFile(bundled, []byte("v1"), 0o755)
	c := CLIInstall{GOOS: "windows", Home: base, LocalAppData: filepath.Join(base, "Local"), BundledCLI: bundled}
	if want := filepath.Join(base, "Local", "Programs", "looptrack", "looptrack.exe"); c.Target() != want {
		t.Fatalf("Target = %s, want %s", c.Target(), want)
	}
	if _, err := c.Install(i18n.JA); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(c.Target()); string(b) != "v1" || c.State() != CLIOurs {
		t.Fatalf("コピーできていない: %q %v", b, c.State())
	}
	os.WriteFile(bundled, []byte("v2-new"), 0o755) // アプリを更新した
	if c.State() != CLIStale {
		t.Error("同梱のものが新しくなったのに CLIStale でない")
	}
	if fixed, err := c.Refresh(); !fixed || err != nil {
		t.Fatalf("Refresh = %v %v", fixed, err)
	}
	if b, _ := os.ReadFile(c.Target()); string(b) != "v2-new" {
		t.Errorf("置き換わっていない: %q", b)
	}
	// アプリを CLI の置き場に展開していると（大小を区別しない Windows では同じ名前）、アプリ本体を上書きしないよう断る
	inPlace := c
	inPlace.Launcher = filepath.Join(c.Dir(), "Looptrack.exe")
	if err := inPlace.put(); err == nil || !strings.Contains(i18n.Text(i18n.JA, err), "CLI の置き場") {
		t.Errorf("アプリが CLI の置き場にあるのに断らない: %v", err)
	}
	// 置いた後に self-update で新しくしたものは、古い同梱のもので戻さない
	os.WriteFile(c.Target(), []byte("v9-self-updated"), 0o755)
	os.WriteFile(bundled, []byte("v2b"), 0o755)
	if c.State() != CLIOther {
		t.Error("self-update で変わったものが CLIOther でない")
	}
	if fixed, _ := c.Refresh(); fixed {
		t.Error("self-update で新しくしたものを置き換えた")
	}
	os.WriteFile(c.Target(), []byte("v2-new"), 0o755)
	// 印の無いもの（setup・self-update で入れた）は触らない
	os.Remove(filepath.Join(c.Dir(), markerName))
	os.WriteFile(bundled, []byte("v3"), 0o755)
	if c.State() != CLIOther {
		t.Error("印が無いのに CLIOther でない")
	}
	if fixed, _ := c.Refresh(); fixed {
		t.Error("setup で入れたものを置き換えた")
	}
	// アンインストール: 印の無いもの（setup で入れた）は残す
	if removed, err := c.Uninstall(); removed || err != nil {
		t.Errorf("setup で入れたものを消した: %v %v", removed, err)
	}
	// このアプリが置いたもの（印つき・同梱のものより古くても）は、印ごと消す
	os.Remove(c.Target())
	if _, err := c.Install(i18n.JA); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(bundled, []byte("v4"), 0o755) // 同梱のものが新しい＝CLIStale
	if c.State() != CLIStale {
		t.Fatalf("State = %v", c.State())
	}
	if removed, err := c.Uninstall(); !removed || err != nil {
		t.Fatalf("Uninstall = %v %v", removed, err)
	}
	for _, p := range []string{c.Target(), filepath.Join(c.Dir(), markerName)} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s が残っている", p)
		}
	}
	if removed, err := c.Uninstall(); removed || err != nil {
		t.Errorf("無いものを消して %v %v", removed, err)
	}
}

// インストーラの選択肢とアンインストールの後始末: looptrack desktop --enable-autostart / --install-cli / --unregister。
// サーバもロックも作らず、データは消さない。
func TestMainInstallerFlags(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("レジストリと symlink は実機で確かめる（Windows の CI は release.yml の smoke test）")
	}
	home, data := t.TempDir(), t.TempDir()
	// 出力の日本語を検査するので言語を固定する（指定が無いと機械の LANG で変わる）
	e := env.FromMap(map[string]string{"LOOPTRACK_DATA_DIR": data, "PATH": filepath.Join(home, ".local", "bin"), "LOOPTRACK_LANG": "ja"})
	run := func(args ...string) (int, string) {
		var out bytes.Buffer
		code := Main(args, Options{Env: e, Home: home, GOOS: runtime.GOOS, Stdout: &out, Stderr: &out, Alert: (&recorder{}).alert})
		return code, out.String()
	}
	if code, s := run("--enable-autostart", "--install-cli"); code != 0 {
		t.Fatalf("--enable-autostart --install-cli = %d: %s", code, s)
	}
	as := Autostart{GOOS: runtime.GOOS, Home: home, Label: BundleID}
	if on, err := as.Enabled(); !on || err != nil {
		t.Errorf("ログイン時の起動が登録されていない: %v %v", on, err)
	}
	cliPath := filepath.Join(home, ".local", "bin", "looptrack")
	if _, err := os.Lstat(cliPath); err != nil {
		t.Errorf("CLI が置かれていない: %v", err)
	}
	// サーバを上げない＝ロックも DB も作らない
	for _, f := range []string{"desktop.lock", "looptrack.db", "desktop.json"} {
		if _, err := os.Stat(filepath.Join(data, f)); err == nil {
			t.Errorf("%s を作った（サーバを上げてはいけない）", f)
		}
	}
	// データを残すことが分かる文を出す
	code, s := run("--unregister")
	if code != 0 {
		t.Fatalf("--unregister = %d: %s", code, s)
	}
	if !strings.Contains(s, "データは消していません") {
		t.Errorf("--unregister の出力: %s", s)
	}
	if on, _ := as.Enabled(); on {
		t.Error("ログイン時の起動の登録が残っている")
	}
	if _, err := os.Lstat(cliPath); err == nil {
		t.Error("CLI のリンクが残っている")
	}
	if _, err := os.Stat(data); err != nil {
		t.Errorf("データの置き場を消した: %v", err)
	}
}

type fakeUI struct {
	ran chan *App
}

func (f *fakeUI) Run(a *App) {
	f.ran <- a
	<-a.Done()
}

type recorder struct {
	mu     sync.Mutex
	opened []string
	alerts []string
}

func (r *recorder) open(u string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.opened = append(r.opened, u)
	return true
}

func (r *recorder) alert(title, msg string, isError bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.alerts = append(r.alerts, msg)
	return true
}

func (r *recorder) openedURLs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.opened...)
}

// 起動 → 画面（初回設定）が開く → 2 回目の起動は二重に立たず同じ URL を開くだけ → --status → トレイの「終了」で止まり、
// 次の起動は同じポートを使う（AI の接続設定の URL が変わらない）。データは一時ディレクトリ（LOOPTRACK_DATA_DIR）。
func TestMainSingleInstance(t *testing.T) {
	dir := t.TempDir()
	// 出力の日本語を検査するので言語を固定する（指定が無いと機械の LANG で変わる）
	e := env.FromMap(map[string]string{"LOOPTRACK_DATA_DIR": dir, "LOOPTRACK_DESKTOP_PORT": "0", "LOOPTRACK_LANG": "ja"})
	rec := &recorder{}
	ui := &fakeUI{ran: make(chan *App, 1)}
	var out bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- Main(nil, Options{Version: "vtest", Env: e, Stdout: &out, UI: ui, Open: rec.open, Alert: rec.alert, Home: dir})
	}()
	var app *App
	select {
	case app = <-ui.ran:
	case code := <-done:
		t.Fatalf("起動しないで終わった（%d）: %s %v", code, out.String(), rec.alerts)
	case <-time.After(20 * time.Second):
		t.Fatal("起動しない")
	}
	url := app.URL()
	if got := rec.openedURLs(); len(got) != 1 || got[0] != url {
		t.Fatalf("ブラウザで開いたもの = %v, want %s", got, url)
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther || !strings.HasSuffix(res.Header.Get("Location"), "/looptrack/first-run") {
		t.Errorf("管理者 0 人の画面 = %d %s, want 初回設定へ", res.StatusCode, res.Header.Get("Location"))
	}
	if _, err := app.MCPConfigs(); err != errNotSetUp {
		t.Errorf("初回設定の前の MCPConfigs = %v", err)
	}
	for _, f := range []string{"looptrack.db", "looptrack.db.secret-key", "desktop.json", "desktop.lock", filepath.Join("logs", "looptrack.log")} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s が無い: %v", f, err)
		}
	}

	// 2 回目: 二重に立たず、同じ URL を開いて 0 で終わる
	var out2 bytes.Buffer
	if code := Main(nil, Options{Version: "vtest", Env: e, Stdout: &out2, UI: &fakeUI{ran: make(chan *App, 1)}, Open: rec.open, Alert: rec.alert, Home: dir}); code != 0 {
		t.Fatalf("2 回目の起動 = %d: %s", code, out2.String())
	}
	if got := rec.openedURLs(); len(got) != 2 || got[1] != url {
		t.Errorf("2 回目に開いたもの = %v", got)
	}
	if !strings.Contains(out2.String(), "既に起動しています") {
		t.Errorf("2 回目の出力: %s", out2.String())
	}
	// --background は開かない
	Main([]string{"--background"}, Options{Env: e, Open: rec.open, Alert: rec.alert, Home: dir})
	if got := rec.openedURLs(); len(got) != 2 {
		t.Errorf("--background で開いた: %v", got)
	}
	var st bytes.Buffer
	if code := Main([]string{"--status"}, Options{Env: e, Stdout: &st, Home: dir}); code != 0 || strings.TrimSpace(st.String()) != url {
		t.Errorf("--status = %d %q", code, st.String())
	}

	app.Quit() // トレイの「終了」
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("終了コード %d", code)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("終了しない")
	}
	if _, err := http.Get(url); err == nil {
		t.Error("終了の後も応答する")
	}
	st.Reset()
	if code := Main([]string{"--status"}, Options{Env: e, Stdout: &st, Home: dir}); code != 1 {
		t.Errorf("止めた後の --status = %d %q", code, st.String())
	}
	s := readState(filepath.Join(dir, "desktop.json"))
	if s.PID != 0 || s.Port != app.port {
		t.Errorf("止めた後の desktop.json = %+v（pid なし・ポートは残す）", s)
	}
	if len(rec.alerts) != 0 {
		t.Errorf("知らせが出た: %v", rec.alerts)
	}
}

// --quit（トレイが出ない環境の逃げ道）: 起動中のインスタンスに SIGTERM を送り、止まるのを待つ。
func TestMainQuit(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows の --quit は、穏やかに止められないときだけ TerminateProcess に進む（テストのプロセス自身を
		// 終わらせてしまう）。穏やかな道（終了を頼む印。トレイの有無に依らない）は TestWatchQuitWithoutTray が
		// 確かめている。ここを Windows でも通すには、別プロセスで起動する形に組み直す必要がある。
		t.Skip("Windows は穏やかに止められないと TerminateProcess に進む（テストのプロセス自身を終わらせてしまう）")
	}
	dir := t.TempDir()
	e := env.FromMap(map[string]string{"LOOPTRACK_DATA_DIR": dir, "LOOPTRACK_DESKTOP_PORT": "0"})
	done := make(chan int, 1)
	go func() {
		done <- Main([]string{"--background", "--no-tray"}, Options{Env: e, Home: dir, Alert: (&recorder{}).alert})
	}()
	var st bytes.Buffer
	deadline := time.Now().Add(20 * time.Second)
	for Main([]string{"--status"}, Options{Env: e, Stdout: io.Discard, Home: dir}) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("起動しない")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if code := Main([]string{"--quit"}, Options{Env: e, Stdout: &st, Stderr: &st, Home: dir}); code != 0 {
		t.Fatalf("--quit = %d %s", code, st.String())
	}
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("止まらない")
	}
}
