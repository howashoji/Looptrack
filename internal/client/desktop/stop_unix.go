//go:build !windows

package desktop

import (
	"os"
	"syscall"

	"github.com/howashoji/looptrack/internal/i18n"
)

// askStop は起動中のインスタンスに終了を頼む（SIGTERM。受けたインスタンスはサーバを止めてから終わる）。
func askStop(_ i18n.Lang, pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(syscall.SIGTERM)
}

// errNoForceStop は強制終了の手立てが無いこと（unix は SIGTERM が届けば止まる。届かないなら pid か権限の問題で、
// SIGKILL で殺しても直らない）。Windows だけ TerminateProcess に進む。
var errNoForceStop = i18n.Errorf("desktop.err.force_stop_windows_only")

func forceStop(int) error { return errNoForceStop }
