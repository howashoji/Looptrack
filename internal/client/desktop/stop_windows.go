//go:build windows

package desktop

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/howashoji/looptrack/internal/i18n"
)

// trayWindowClass は fyne.io/systray が Windows で作る隠し窓のクラス名（通知領域のメッセージを受ける窓）。
const trayWindowClass = "SystrayClass"

// wmClose は WM_CLOSE。systray はこれを受けると窓を壊し、通知領域のアイコンを消してメッセージループを抜ける
// （＝トレイの「終了」と同じ道を通り、App.Quit → サーバの Shutdown まで進む）。
const wmClose = 0x0010

var (
	user32           = windows.NewLazySystemDLL("user32.dll")
	procPostMessageW = user32.NewProc("PostMessageW")
)

// askStop は起動中のインスタンスに終了を頼む。Windows の GUI のプロセスにはシグナルを送れないので、
// 2 つの手立てを順に試す（どちらもデータを書き終えてから終わる）。
//  1. 終了を頼む印（quitEventName の名前付きイベント）を立てる。相手は watchQuit で見張っている。
//     トレイの有無に依らないので、--no-tray やトレイを出せない環境でもこれで止まる。
//  2. 印が無いとき（印を作れなかった・印を知らない古い版が動いている）はトレイの窓に WM_CLOSE を送る。
//
// どちらも駄目ならエラーを返し、呼び出し側が forceStop（TerminateProcess）に進む。
func askStop(_ i18n.Lang, pid int) error {
	evErr := setQuitEvent(pid)
	if evErr == nil {
		return nil
	}
	h, err := findTrayWindow(uint32(pid))
	if err != nil {
		return i18n.Wrapf(err, "desktop.err.quit_event_and_window", "window", err, "event", evErr)
	}
	r, _, e := procPostMessageW.Call(uintptr(h), wmClose, 0, 0)
	if r == 0 {
		return i18n.Wrapf(e, "desktop.err.post_wm_close")
	}
	return nil
}

// setQuitEvent は pid のインスタンスが作った終了の印を立てる。印が無ければエラー。
func setQuitEvent(pid int) error {
	name, err := windows.UTF16PtrFromString(quitEventName(pid))
	if err != nil {
		return err
	}
	h, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, name)
	if err != nil {
		return i18n.Wrapf(err, "desktop.err.no_quit_event", "name", quitEventName(pid))
	}
	defer windows.CloseHandle(h)
	return windows.SetEvent(h)
}

// forceStop は TerminateProcess（SQLite は WAL なので、途中で終わっても DB は壊れない）。
func forceStop(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

// findTrayWindow は pid のプロセスが持つ SystrayClass の窓を探す。
func findTrayWindow(pid uint32) (windows.HWND, error) {
	var found windows.HWND
	buf := make([]uint16, 256)
	cb := windows.NewCallback(func(hwnd windows.HWND, _ uintptr) uintptr {
		var owner uint32
		if _, err := windows.GetWindowThreadProcessId(hwnd, &owner); err != nil || owner != pid {
			return 1 // 次の窓へ
		}
		n, err := windows.GetClassName(hwnd, &buf[0], int32(len(buf)))
		if err != nil || n <= 0 {
			return 1
		}
		if windows.UTF16ToString(buf[:n]) != trayWindowClass {
			return 1
		}
		found = hwnd
		return 0 // 見つけたので打ち切る
	})
	// EnumWindows は列挙を打ち切ると「失敗」を返すので、err は found が空のときだけ見る
	err := windows.EnumWindows(cb, unsafe.Pointer(nil))
	if found != 0 {
		return found, nil
	}
	if err != nil {
		return 0, err
	}
	return 0, i18n.Errorf("desktop.err.no_tray_window", "class", trayWindowClass)
}
