package desktop

import "syscall"

// detachedProcess・createNewProcessGroup は CreateProcess の作成フラグ（syscall には DETACHED_PROCESS が無い）。
const (
	detachedProcess       = 0x00000008
	createNewProcessGroup = 0x00000200
)

// detachAttr は起動し直すもの（新しい版の Looptrack.exe・無人の setup.exe）を今のインスタンスから切り離す
// （コンソールを引き継がず、今のインスタンスへの Ctrl+C などのグループのシグナルにも巻き込まれない）。
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: detachedProcess | createNewProcessGroup}
}
