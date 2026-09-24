//go:build windows

package verify

// GetACP（kernel32 は run_windows.go の NewLazyDLL）。
var procGetACP = kernel32.NewProc("GetACP")

func init() {
	codePage = func() int {
		if procGetACP.Find() != nil {
			return 0
		}
		cp, _, _ := procGetACP.Call()
		return int(cp)
	}
}
