//go:build windows

package desktop

import (
	"golang.org/x/sys/windows"
)

// showAlert は利用者に短い知らせを出す（MessageBoxW。GUI の実行ファイルなので端末が無い）。
func showAlert(title, msg string, isError bool) bool {
	t, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return false
	}
	m, err := windows.UTF16PtrFromString(msg)
	if err != nil {
		return false
	}
	var flags uint32 = windows.MB_OK | windows.MB_SETFOREGROUND
	if isError {
		flags |= windows.MB_ICONERROR
	} else {
		flags |= windows.MB_ICONINFORMATION
	}
	_, err = windows.MessageBox(0, m, t, flags)
	return err == nil
}
