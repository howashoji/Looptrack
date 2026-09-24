//go:build !windows

package desktop

// watchQuit は非 Windows では何もしない（終了は SIGTERM で頼む。app.go が signal.Notify で受ける）。
func watchQuit(func()) (stop func()) { return func() {} }
