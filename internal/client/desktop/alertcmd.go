package desktop

// alertCommands は、知らせを出すために試すコマンドを上から順に返す（macOS・Linux。Windows は MessageBoxW を直に呼ぶ）。
//
// 文は必ず引数として渡す（AppleScript やシェルの文に埋め込まない）。埋め込むと、題や本文に含まれる引用符・改行で
// 文が壊れたり、思わぬものが実行されたりする。goos は runtime.GOOS（テストから両方を確かめるため引数にしてある）。
func alertCommands(goos, title, msg string, isError bool) [][]string {
	if goos == "darwin" {
		as := "critical"
		if !isError {
			as = "informational"
		}
		// osascript は argv で受け取り、display alert の引数に使う（文の組み立てに題と本文を混ぜない）。
		return [][]string{{"osascript",
			"-e", "on run argv",
			"-e", "display alert (item 1 of argv) message (item 2 of argv) as " + as,
			"-e", "end run", title, msg}}
	}
	kind, kd := "--error", "--error"
	if !isError {
		kind, kd = "--info", "--msgbox"
	}
	return [][]string{
		{"zenity", kind, "--title", title, "--text", msg, "--no-markup"},
		{"kdialog", "--title", title, kd, msg},
		{"notify-send", title, msg},
	}
}
