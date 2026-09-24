//go:build desktop && darwin

package tray

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
void looptrackInstallReopen(void);
*/
import "C"

import "sync"

var (
	reopenMu sync.Mutex
	reopenFn func()
)

// installReopen は、起動中の .app をもう一度開いたとき（Finder のダブルクリック・Dock・open）に f を呼ぶようにする。
// macOS は起動中のアプリを 2 つ目のプロセスとして立てず、Apple Event の reopen（kAEReopenApplication）を送るだけなので、
// ロックのファイルによる二重起動の防止（2 つ目のプロセスが画面を開いて終わる）は働かない。その代わりにここで画面を開く。
func installReopen(f func()) {
	reopenMu.Lock()
	reopenFn = f
	reopenMu.Unlock()
	C.looptrackInstallReopen()
}

//export looptrackReopen
func looptrackReopen() {
	reopenMu.Lock()
	f := reopenFn
	reopenMu.Unlock()
	if f != nil {
		go f() // メインスレッド（イベントループ）を止めない
	}
}
