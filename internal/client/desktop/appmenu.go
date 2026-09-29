package desktop

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/howashoji/looptrack/internal/i18n"
)

// AppMenu はアプリ一覧（ランチャー・アクティビティの検索）への登録（Linux の AppImage だけ。DESIGN.md §5-4「アプリ一覧への登録」）。
// AppImage には中の .desktop を外へ出す仕組みが無いので、アプリが自分で書く。どれも管理者権限は要らない。
//
//	.desktop: $XDG_DATA_HOME/applications/looptrack.desktop（既定 ~/.local/share/applications）
//	アイコン: $XDG_DATA_HOME/icons/hicolor/256x256/apps/looptrack.png（AppImage の中の同じ場所から写す）
//	外した印: データの置き場の app-menu-off（トレイで外したら作り、起動のたびの登録をしない）
//
// 起動のたびに（App.refresh）、外した印が無ければ登録し、中身が今の AppImage の場所と違えば（動かした・アイコンが変わった）書き直す。
// 置き換え（replace.go）は AppImage のファイル名を変えないので、置き換えた後も登録はそのまま効く。
type AppMenu struct {
	DataHome string // $XDG_DATA_HOME（空なら ~/.local/share）
	Home     string
	AppDir   string // $APPDIR（AppImage の中身のマウント先。アイコンを写す元。空ならアイコンは置かない）
	Launcher string // $APPIMAGE の絶対パス
	OffMark  string // 外した印のファイル（空なら印を使わない＝--unregister）
	Lang     i18n.Lang
}

// appMenuFile・appMenuIcon は置き場の名前（AppImage の中の looptrack.desktop・looptrack.png と同じ名前）。
const (
	appMenuFile = "looptrack.desktop"
	appMenuIcon = "looptrack.png"
)

func (m AppMenu) dataHome() string {
	if m.DataHome != "" {
		return m.DataHome
	}
	return filepath.Join(m.Home, ".local", "share")
}

// Path は .desktop の置き場。
func (m AppMenu) Path() string { return filepath.Join(m.dataHome(), "applications", appMenuFile) }

// IconPath はアイコンの置き場（hicolor の 256px。AppImage の中と同じ大きさ）。
func (m AppMenu) IconPath() string {
	return filepath.Join(m.dataHome(), "icons", "hicolor", "256x256", "apps", appMenuIcon)
}

// iconSource は AppImage の中のアイコン（無ければ空）。
func (m AppMenu) iconSource() []byte {
	if m.AppDir == "" {
		return nil
	}
	for _, p := range []string{
		filepath.Join(m.AppDir, "usr", "share", "icons", "hicolor", "256x256", "apps", appMenuIcon),
		filepath.Join(m.AppDir, appMenuIcon),
	} {
		if b, err := os.ReadFile(p); err == nil && len(b) > 0 {
			return b
		}
	}
	return nil
}

// Content は .desktop の中身。Exec は AppImage の絶対パスで desktop を起動する（起動中なら画面を開く）。
// TryExec で AppImage が無くなった（消した・動かした）ときは一覧に出さない（動かしたら次の起動で書き直す）。
func (m AppMenu) Content() []byte {
	return []byte(fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=%s
Comment=%s
Exec=%s desktop
TryExec=%s
Icon=looptrack
Terminal=false
StartupNotify=false
Categories=Development;ProjectManagement;
`, AppName, i18n.T(m.Lang, "desktop.appmenu.comment"), desktopExecQuote(m.Launcher), desktopStringEscape(m.Launcher)))
}

// desktopStringEscape は Desktop Entry の string の値（\ と改行などを \ で書く）。
func desktopStringEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`, "\t", `\t`, "\r", `\r`).Replace(s)
}

// Registered は .desktop があるか（中身が今の AppImage のものと違っても、あれば true）。
func (m AppMenu) Registered() bool {
	_, err := os.Stat(m.Path())
	return err == nil
}

// Off はトレイで外したか（外した印があるか）。
func (m AppMenu) Off() bool {
	if m.OffMark == "" {
		return false
	}
	_, err := os.Stat(m.OffMark)
	return err == nil
}

// Current は .desktop とアイコンが今の AppImage のものと同じか（違えば Enable で書き直す）。
func (m AppMenu) Current() bool {
	b, err := os.ReadFile(m.Path())
	if err != nil || !bytes.Equal(b, m.Content()) {
		return false
	}
	if src := m.iconSource(); src != nil {
		ib, err := os.ReadFile(m.IconPath())
		return err == nil && bytes.Equal(ib, src)
	}
	return true
}

// Enable は登録する（あれば書き直す）。外した印は消す。
func (m AppMenu) Enable() error {
	if m.Launcher == "" {
		return i18n.Errorf("desktop.err.app_menu_no_appimage")
	}
	if src := m.iconSource(); src != nil {
		if err := writeReplace(m.IconPath(), src); err != nil {
			return err
		}
	}
	if err := writeReplace(m.Path(), m.Content()); err != nil {
		return err
	}
	if m.OffMark != "" {
		if err := os.Remove(m.OffMark); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// Disable は登録を消し（.desktop とアイコン）、外した印を作る（次の起動で登録し直さない）。
func (m AppMenu) Disable() error {
	if err := m.remove(); err != nil {
		return err
	}
	if m.OffMark != "" {
		return os.WriteFile(m.OffMark, []byte("off\n"), 0o600)
	}
	return nil
}

func (m AppMenu) remove() error {
	for _, p := range []string{m.Path(), m.IconPath()} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// RemoveIfOurs は、登録が**この AppImage**（Launcher）のものであれば消す（--unregister）。別の場所の AppImage の登録は残す。
// 消したら true。
func (m AppMenu) RemoveIfOurs() (bool, error) {
	if m.Launcher == "" {
		return false, nil
	}
	b, err := os.ReadFile(m.Path())
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !bytes.Contains(b, []byte("TryExec="+desktopStringEscape(m.Launcher)+"\n")) {
		return false, nil
	}
	return true, m.remove()
}

// Refresh は起動のたびの登録: 外した印が無く、中身が今のものと違えば（まだ無い・AppImage を動かした・アイコンが変わった）書く。
// 書いたら true。
func (m AppMenu) Refresh() (bool, error) {
	if m.Off() || m.Current() {
		return false, nil
	}
	return true, m.Enable()
}

// writeReplace は一時名で書いて rename する（読みかけの .desktop を見せない）。
func writeReplace(p string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
