//go:build desktop && !darwin

package tray

// installReopen は macOS だけ（Windows・Linux はもう一度開くと 2 つ目のプロセスが立ち、ロックで二重起動を防いで画面を開く）。
func installReopen(func()) {}
