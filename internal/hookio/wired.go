package hookio

import "strings"

// WiredCommand は、AI の設定ファイルに書いた hook の 1 件（command と args）を、シェルで打つ形の 1 行にする。
//
// Claude Code は args のある hook（exec form）を、command を実行ファイルとしてシェルを通さずに起動する。
// Git Bash の無い Windows ではフックを PowerShell で実行するので、引用した絶対パスの後ろに語が続く shell form は
// 構文エラーになる。そこで、絶対パスで配線するときは exec form で書く。読む側（init の配線の判定・自己検査・doctor・
// 会話記録の配線の点検）は、exec form も shell form と同じ文字列（引用した command の後ろに args を空白でつないだ形）で
// 見分ける。args が無い・配列でないときは command をそのまま返す。
func WiredCommand(command string, args any) string {
	list, ok := args.([]any)
	if !ok || len(list) == 0 {
		return command
	}
	parts := []string{`"` + command + `"`}
	for _, a := range list {
		s, _ := a.(string)
		if s == "" || strings.ContainsAny(s, " \t\"'") {
			s = `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}
