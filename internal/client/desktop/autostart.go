package desktop

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/howashoji/looptrack/internal/i18n"
)

// Autostart はログイン時の自動起動の登録（DESIGN.md §5-14）。どれも管理者権限は要らない。
//
//	macOS  : ~/Library/LaunchAgents/<BundleID>.plist（RunAtLoad。次のログインから効く）
//	Linux  : $XDG_CONFIG_HOME/autostart/looptrack.desktop（既定 ~/.config/autostart。XDG Autostart）
//	Windows: HKCU\Software\Microsoft\Windows\CurrentVersion\Run の値 Looptrack
//
// Argv は起動するコマンド（実行ファイルと引数。AppImage は $APPIMAGE のパス）。
type Autostart struct {
	GOOS       string
	Home       string
	ConfigHome string // XDG_CONFIG_HOME（空なら ~/.config）
	Label      string // macOS の LaunchAgent の Label（BundleID）
	Argv       []string
	Lang       i18n.Lang // Linux の .desktop の Comment の言語（空なら対訳表の正本の日本語）
}

// runKey・runValue は Windows の登録先。
const (
	runKey   = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValue = AppName
)

// Path は登録のファイル（Windows はレジストリなので空）。
func (a Autostart) Path() string {
	switch a.GOOS {
	case "darwin":
		return filepath.Join(a.Home, "Library", "LaunchAgents", a.Label+".plist")
	case "windows":
		return ""
	}
	cfg := a.ConfigHome
	if cfg == "" {
		cfg = filepath.Join(a.Home, ".config")
	}
	return filepath.Join(cfg, "autostart", "looptrack.desktop")
}

// Content は登録の中身（macOS は plist、Linux は .desktop、Windows は Run の値の文字列）。
func (a Autostart) Content() []byte {
	switch a.GOOS {
	case "darwin":
		return launchAgentPlist(a.Label, a.Argv)
	case "windows":
		return []byte(windowsCommandLine(a.Argv))
	}
	return []byte(autostartDesktopEntry(a.Argv, a.Lang))
}

// Enabled は登録があるか。中身が今の Argv と違っても（.app・AppImage を動かした）登録があれば true。
func (a Autostart) Enabled() (bool, error) {
	if a.GOOS == "windows" {
		v, err := regGetRun()
		return v != "", err
	}
	_, err := os.Stat(a.Path())
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// Current は登録の中身が今の Argv のものと同じか（違えば Enable で書き直す。起動のたびに直す＝.app・AppImage を動かしても続く）。
func (a Autostart) Current() bool {
	if a.GOOS == "windows" {
		v, _ := regGetRun()
		return v == string(a.Content())
	}
	b, err := os.ReadFile(a.Path())
	return err == nil && bytes.Equal(b, a.Content())
}

// Enable は登録する（あれば書き直す）。
func (a Autostart) Enable() error {
	if len(a.Argv) == 0 {
		return i18n.Errorf("desktop.err.autostart_no_argv")
	}
	if a.GOOS == "windows" {
		return regSetRun(string(a.Content()))
	}
	p := a.Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, a.Content(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// RemoveIfOurs は、登録が**このアプリ**（Argv[0] の実行ファイル）のものであれば消す（アンインストールで使う）。
// 別の場所に置いた Looptrack（持ち運び用の zip・別のインストール）の登録は残す。消したら true。
func (a Autostart) RemoveIfOurs() (bool, error) {
	if len(a.Argv) == 0 {
		return false, i18n.Errorf("desktop.err.autostart_no_argv")
	}
	if a.GOOS == "windows" {
		v, err := regGetRun()
		if err != nil || v == "" {
			return false, err
		}
		if !samePath(windowsCommandArg0(v), a.Argv[0]) {
			return false, nil
		}
		return true, regDeleteRun()
	}
	b, err := os.ReadFile(a.Path())
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !bytes.Contains(b, []byte(a.Argv[0])) {
		return false, nil
	}
	return true, os.Remove(a.Path())
}

// samePath は Windows のパスの比較（大小を区別しない・区切りを / と \ のどちらでもよい）。
func samePath(a, b string) bool {
	norm := func(s string) string {
		return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "/", `\`))
	}
	return norm(a) != "" && norm(a) == norm(b)
}

// Disable は登録を消す（無ければ何もしない）。
func (a Autostart) Disable() error {
	if a.GOOS == "windows" {
		return regDeleteRun()
	}
	err := os.Remove(a.Path())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// launchAgentPlist は macOS の LaunchAgent（ログイン時に 1 回起動する。KeepAlive は付けない＝終了したら再起動しない）。
// AssociatedBundleIdentifiers は「ログイン項目」の設定画面にアプリの名前で出すため（macOS 13 以降）。
func launchAgentPlist(label string, argv []string) []byte {
	var b strings.Builder
	esc := func(s string) string {
		var x bytes.Buffer
		xml.EscapeText(&x, []byte(s))
		return x.String()
	}
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + esc(label) + `</string>
	<key>AssociatedBundleIdentifiers</key>
	<string>` + esc(label) + `</string>
	<key>ProgramArguments</key>
	<array>
`)
	for _, a := range argv {
		b.WriteString("\t\t<string>" + esc(a) + "</string>\n")
	}
	b.WriteString(`	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>ProcessType</key>
	<string>Interactive</string>
</dict>
</plist>
`)
	return []byte(b.String())
}

// autostartDesktopEntry は XDG Autostart の .desktop。
func autostartDesktopEntry(argv []string, lang i18n.Lang) string {
	q := make([]string, len(argv))
	for i, a := range argv {
		q[i] = desktopExecQuote(a)
	}
	return fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=%s
Comment=%s
Exec=%s
Icon=looptrack
Terminal=false
X-GNOME-Autostart-enabled=true
`, AppName, i18n.T(lang, "desktop.autostart.comment", "app", AppName), strings.Join(q, " "))
}

// desktopExecQuote は Desktop Entry の Exec の 1 引数（空白・予約文字があれば二重引用符。中の " ` $ \ は \ を前に置く。
// さらに .desktop の文字列として \ を重ねる。% はフィールドコードにならないよう %% にする）。
func desktopExecQuote(s string) string {
	s = strings.ReplaceAll(s, "%", "%%")
	if s != "" && !strings.ContainsAny(s, " \t\n\"'\\><~|&;$*?#()`") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '`', '$':
			b.WriteString(`\\`) // 引用符の中の \ に、.desktop の文字列のエスケープの \ を重ねる
		case '\\':
			b.WriteString(`\\\`) // \ そのものは 4 つ（仕様の例のとおり）
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

// windowsCommandLine は Run の値（CommandLineToArgvW の規則で引用する）。
func windowsCommandLine(argv []string) string {
	q := make([]string, len(argv))
	for i, a := range argv {
		q[i] = windowsQuoteArg(a)
	}
	return strings.Join(q, " ")
}

// windowsCommandArg0 はコマンドラインの 1 つ目の引数＝実行ファイルのパス（CommandLineToArgvW の規則。
// argv[0] だけは特別で、引用符の中の \ はエスケープにならず、最初の " から次の " までがそのままパスになる）。
func windowsCommandArg0(cmdline string) string {
	s := strings.TrimLeft(cmdline, " \t")
	if s == "" {
		return ""
	}
	if s[0] == '"' {
		if i := strings.IndexByte(s[1:], '"'); i >= 0 {
			return s[1 : 1+i]
		}
		return s[1:]
	}
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i]
	}
	return s
}

// windowsQuoteArg は syscall.EscapeArg と同じ規則（Windows 以外でも組み立てて確かめられるよう、ここに持つ）。
func windowsQuoteArg(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t\"") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	slashes := 0
	for _, r := range s {
		switch r {
		case '\\':
			slashes++
		case '"':
			b.WriteString(strings.Repeat(`\`, slashes+1))
			slashes = 0
		default:
			slashes = 0
		}
		b.WriteRune(r)
	}
	b.WriteString(strings.Repeat(`\`, slashes))
	b.WriteByte('"')
	return b.String()
}
