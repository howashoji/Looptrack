//go:build !windows

package desktop

import (
	"os/exec"
	"runtime"
)

// showAlert は利用者に短い知らせを出す（ダブルクリックで起動したので端末が無い）。出せなければ false（ログには別に残す）。
// 試すコマンドと引数は alertCommands（macOS は osascript、Linux は zenity → kdialog → notify-send）。
func showAlert(title, msg string, isError bool) bool {
	for _, c := range alertCommands(runtime.GOOS, title, msg, isError) {
		p, err := exec.LookPath(c[0])
		if err != nil {
			continue
		}
		if exec.Command(p, c[1:]...).Run() == nil {
			return true
		}
	}
	return false
}
