//go:build windows

package desktop

import (
	"golang.org/x/sys/windows"
)

// watchQuit は終了を頼む印（quitEventName の名前付きイベント）を作り、立ったら onQuit を呼ぶ。
// 戻り値を呼ぶと見張りを終える。印を作れないときは何もしない（トレイの窓への WM_CLOSE が残りの手立て）。
func watchQuit(onQuit func()) (stop func()) {
	name, err := windows.UTF16PtrFromString(quitEventName(windows.Getpid()))
	if err != nil {
		return func() {}
	}
	// 手動リセット・初期状態は下り。作る側だけが持つので、既にあれば別のインスタンスのもの（そのまま使う）。
	ev, err := windows.CreateEvent(nil, 1, 0, name)
	if err != nil && ev == 0 {
		return func() {}
	}
	done, err := windows.CreateEvent(nil, 1, 0, nil) // 見張りを終えるための印（名前なし）
	if err != nil {
		windows.CloseHandle(ev)
		return func() {}
	}
	go func() {
		defer windows.CloseHandle(ev)
		defer windows.CloseHandle(done)
		n, err := windows.WaitForMultipleObjects([]windows.Handle{ev, done}, false, windows.INFINITE)
		if err == nil && n == windows.WAIT_OBJECT_0 {
			onQuit()
		}
	}()
	var once bool
	return func() {
		if once {
			return
		}
		once = true
		windows.SetEvent(done)
	}
}
