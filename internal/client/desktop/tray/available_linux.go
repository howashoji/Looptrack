//go:build desktop && linux

package tray

import (
	"os"

	"github.com/godbus/dbus/v5"

	"github.com/howashoji/looptrack/internal/i18n"
)

// available は Linux のトレイ（D-Bus の StatusNotifierItem）が使えるか。セッションの D-Bus が無い（表示の無い環境・
// 最小のデスクトップ）と systray は何も出さず、終了のときに接続の無いまま閉じようとして止まるので、トレイを出さずに動かす。
// 同じ接続（dbus.SessionBus は共有）を systray がそのまま使う。
func available() (bool, string) {
	if _, err := dbus.SessionBus(); err != nil {
		return false, i18n.T(i18n.FromEnv(os.Getenv), "desktop.tray.no_dbus", "reason", err.Error())
	}
	return true, ""
}
