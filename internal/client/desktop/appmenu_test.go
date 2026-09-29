package desktop

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/updatecheck"
)

// appMenuFixture は AppImage（中身のマウント先 AppDir にアイコンを持つ）と、登録先の XDG_DATA_HOME を作る。
func appMenuFixture(t *testing.T) (m AppMenu, icon []byte) {
	t.Helper()
	dir := t.TempDir()
	img := filepath.Join(dir, "Apps dir", "Looptrack_v1.0.0_linux_x86_64.AppImage")
	os.MkdirAll(filepath.Dir(img), 0o755)
	os.WriteFile(img, []byte("\x7fELF"), 0o755)
	appDir := filepath.Join(dir, "mnt")
	icon = []byte("png v1")
	ip := filepath.Join(appDir, "usr", "share", "icons", "hicolor", "256x256", "apps", "looptrack.png")
	os.MkdirAll(filepath.Dir(ip), 0o755)
	os.WriteFile(ip, icon, 0o644)
	return AppMenu{DataHome: filepath.Join(dir, "share"), Home: dir, AppDir: appDir, Launcher: img,
		OffMark: filepath.Join(dir, "data", "app-menu-off"), Lang: i18n.JA}, icon
}

// rawPathInDesktopEntry は、一時ディレクトリのパスが .desktop の Exec・TryExec にそのまま写るか。
// Windows のパスは \ を含み、Desktop Entry の書式では \\ に写る（引用も付く）ので、生のパスと比べる期待が成り立たない。
// 登録は Linux の AppImage だけ（appMenuFor は GOOS が linux 以外なら nil）なので、Windows ではその比べ方だけを外す。
func rawPathInDesktopEntry() bool { return runtime.GOOS != "windows" }

// .desktop の中身: Exec は AppImage の絶対パス（空白は引用）で desktop を起動、TryExec・Icon=looptrack・端末なし・分類。
func TestAppMenuContent(t *testing.T) {
	m, _ := appMenuFixture(t)
	got := string(m.Content())
	wants := []string{
		"[Desktop Entry]\n", "Type=Application\n", "Name=Looptrack\n",
		"Icon=looptrack\n", "Terminal=false\n", "Categories=Development;ProjectManagement;\n",
		"Comment=AI コーディングエージェントの外部記憶となり、ループエンジニアリングを実現するイシュー管理ツール\n",
	}
	if rawPathInDesktopEntry() {
		wants = append(wants, "Exec=\""+m.Launcher+"\" desktop\n", "TryExec="+m.Launcher+"\n")
	}
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf(".desktop に %q が無い:\n%s", want, got)
		}
	}
	if m.Path() != filepath.Join(m.DataHome, "applications", "looptrack.desktop") {
		t.Errorf("置き場 = %s", m.Path())
	}
	if m.IconPath() != filepath.Join(m.DataHome, "icons", "hicolor", "256x256", "apps", "looptrack.png") {
		t.Errorf("アイコンの置き場 = %s", m.IconPath())
	}
	// XDG_DATA_HOME が無ければ ~/.local/share
	m.DataHome = ""
	if want := filepath.Join(m.Home, ".local", "share", "applications", "looptrack.desktop"); m.Path() != want {
		t.Errorf("既定の置き場 = %s, want %s", m.Path(), want)
	}
	// 英語
	m.Lang = i18n.EN
	if !strings.Contains(string(m.Content()), "Comment=Issue tracker serving as external memory for AI coding agents, enabling loop engineering\n") {
		t.Errorf("英語の Comment が無い:\n%s", m.Content())
	}
}

// 起動のたびの登録（Refresh）: 無ければ書く → 同じなら書かない → AppImage を動かした・アイコンが変わったら書き直す →
// トレイで外した（Disable）ら消して印を作り、次の Refresh では書かない → 入れ直す（Enable）と印を消す。
func TestAppMenuRefreshLifecycle(t *testing.T) {
	m, icon := appMenuFixture(t)
	if fixed, err := m.Refresh(); err != nil || !fixed {
		t.Fatalf("最初の起動で登録しない: %v %v", fixed, err)
	}
	if b, _ := os.ReadFile(m.Path()); !bytes.Equal(b, m.Content()) {
		t.Errorf(".desktop の中身が違う:\n%s", b)
	}
	if b, _ := os.ReadFile(m.IconPath()); !bytes.Equal(b, icon) {
		t.Errorf("アイコンを写していない: %q", b)
	}
	if !m.Registered() || !m.Current() {
		t.Error("登録した直後に Registered / Current が false")
	}
	if fixed, _ := m.Refresh(); fixed {
		t.Error("同じ中身なのに書き直した")
	}
	// AppImage を動かした
	moved := m
	moved.Launcher = filepath.Join(filepath.Dir(m.Launcher), "Looptrack.AppImage")
	os.Rename(m.Launcher, moved.Launcher)
	if moved.Current() {
		t.Fatal("動かしたのに Current が true（対照が効いていない）")
	}
	if fixed, err := moved.Refresh(); err != nil || !fixed {
		t.Fatalf("動かした後に書き直さない: %v %v", fixed, err)
	}
	if b, _ := os.ReadFile(m.Path()); rawPathInDesktopEntry() && !bytes.Contains(b, []byte("TryExec="+moved.Launcher+"\n")) {
		t.Errorf("新しい場所になっていない:\n%s", b)
	}
	// 新しい版でアイコンが変わった
	os.WriteFile(filepath.Join(moved.AppDir, "usr", "share", "icons", "hicolor", "256x256", "apps", "looptrack.png"), []byte("png v2"), 0o644)
	if fixed, _ := moved.Refresh(); !fixed {
		t.Error("アイコンが変わったのに書き直さない")
	}
	if b, _ := os.ReadFile(moved.IconPath()); string(b) != "png v2" {
		t.Errorf("アイコン = %q", b)
	}
	// トレイで外す
	os.MkdirAll(filepath.Dir(moved.OffMark), 0o700)
	if err := moved.Disable(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{moved.Path(), moved.IconPath()} {
		if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("外したのに残っている: %s", p)
		}
	}
	if !moved.Off() {
		t.Error("外した印が無い")
	}
	if fixed, _ := moved.Refresh(); fixed || moved.Registered() {
		t.Error("外したのに次の起動で登録し直した")
	}
	if err := moved.Enable(); err != nil {
		t.Fatal(err)
	}
	if moved.Off() || !moved.Registered() {
		t.Error("入れ直した後に印が残っている・登録が無い")
	}
}

// AppDir（AppImage の中身）が無いときは .desktop だけを書く（アイコンは Icon=looptrack の名前で探される）。
func TestAppMenuWithoutAppDir(t *testing.T) {
	m, _ := appMenuFixture(t)
	m.AppDir = ""
	if _, err := m.Refresh(); err != nil {
		t.Fatal(err)
	}
	if !m.Registered() {
		t.Error(".desktop を書いていない")
	}
	if _, err := os.Stat(m.IconPath()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("アイコンの元が無いのにアイコンがある: %v", err)
	}
}

// RemoveIfOurs（--unregister）: この AppImage の登録だけを消す。別の場所の AppImage の登録は残す（対照）。
func TestAppMenuRemoveIfOurs(t *testing.T) {
	m, _ := appMenuFixture(t)
	other := m
	other.Launcher = "/opt/other/Looptrack.AppImage"
	if err := other.Enable(); err != nil {
		t.Fatal(err)
	}
	if removed, err := m.RemoveIfOurs(); err != nil || removed || !m.Registered() {
		t.Errorf("別の AppImage の登録を消した: %v %v", removed, err)
	}
	if err := m.Enable(); err != nil {
		t.Fatal(err)
	}
	if removed, err := m.RemoveIfOurs(); err != nil || !removed || m.Registered() {
		t.Errorf("この AppImage の登録を消さない: %v %v", removed, err)
	}
	if _, err := os.Stat(m.IconPath()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("アイコンが残っている: %v", err)
	}
}

// 起動（GOOS=linux・APPIMAGE あり）で登録し、トレイの入口で外すと印が残って次の起動では登録しない。
// Linux でない・APPIMAGE が無いときは登録しない（対照）。--unregister はこの AppImage の登録を消す。
func TestMainAppMenu(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "Looptrack.AppImage")
	os.WriteFile(img, []byte("\x7fELF"), 0o755)
	share := filepath.Join(dir, "share")
	entry := filepath.Join(share, "applications", "looptrack.desktop")
	start := func(goos string, vars map[string]string) *App {
		t.Helper()
		m := map[string]string{"LOOPTRACK_DATA_DIR": filepath.Join(dir, "data"), "LOOPTRACK_DESKTOP_PORT": "0", "LOOPTRACK_LANG": "ja",
			"XDG_DATA_HOME": share, updatecheck.EnvCheck: "off"}
		for k, v := range vars {
			m[k] = v
		}
		rec := &recorder{}
		ui := &fakeUI{ran: make(chan *App, 1)}
		done := make(chan int, 1)
		go func() {
			done <- Main([]string{"--background"}, Options{Version: "v1.0.0", Env: env.FromMap(m), UI: ui, Open: rec.open, Alert: rec.alert,
				Home: dir, GOOS: goos})
		}()
		var app *App
		select {
		case app = <-ui.ran:
		case code := <-done:
			t.Fatalf("起動しないで終わった（%d）: %v", code, rec.alerts)
		case <-time.After(20 * time.Second):
			t.Fatal("起動しない")
		}
		t.Cleanup(func() {
			app.Quit()
			select {
			case <-done:
			case <-time.After(20 * time.Second):
				t.Error("終了しない")
			}
		})
		return app
	}
	stop := func(a *App) {
		a.Quit()
		waitFor(t, "ロックが外れる", func() bool {
			release, ok, _ := tryLock(a.paths.Lock())
			if ok {
				release()
			}
			return ok
		})
	}

	// 対照: APPIMAGE が無い Linux・macOS では登録しない（トレイの項目も出さない）
	for _, c := range []struct {
		goos string
		vars map[string]string
	}{{"linux", nil}, {"darwin", map[string]string{"APPIMAGE": img}}} {
		a := start(c.goos, c.vars)
		if a.AppMenuSupported() {
			t.Errorf("%s %v: 登録できることになっている", c.goos, c.vars)
		}
		stop(a)
		if _, err := os.Stat(entry); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%s %v: 登録した", c.goos, c.vars)
		}
	}

	a := start("linux", map[string]string{"APPIMAGE": img})
	if !a.AppMenuSupported() || !a.AppMenuEnabled() {
		t.Fatalf("AppImage の起動で登録しない（supported=%v）", a.AppMenuSupported())
	}
	if b, _ := os.ReadFile(entry); rawPathInDesktopEntry() && !bytes.Contains(b, []byte("Exec="+img+" desktop\n")) {
		t.Errorf(".desktop = %s", b)
	}
	if a.SetAppMenu(false) {
		t.Error("外したのに登録が残っている")
	}
	stop(a)
	a = start("linux", map[string]string{"APPIMAGE": img})
	if a.AppMenuEnabled() {
		t.Error("外した後の起動で登録し直した")
	}
	if !a.SetAppMenu(true) {
		t.Error("入れ直せない")
	}
	stop(a)

	// --unregister（Linux・この AppImage）: 登録を消す
	var out bytes.Buffer
	code := Main([]string{"--unregister"}, Options{Version: "v1.0.0", GOOS: "linux", Home: dir, Stdout: &out,
		Env: env.FromMap(map[string]string{"XDG_DATA_HOME": share, "APPIMAGE": img, "LOOPTRACK_LANG": "ja"})})
	if _, err := os.Stat(entry); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("--unregister で消えない（%d）: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "アプリ一覧の登録を消しました") {
		t.Errorf("--unregister の出力に消したことが無い: %s", out.String())
	}
}
