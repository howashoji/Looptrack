package loop

// pre-tool-git-guard が「そのディレクトリはいまの作業ツリーか」を判定するためのパスの扱い。
//
// ここに置く関数は**純粋**（ファイルシステムも実行中の OS も見ない）。Windows の形のパスを、
// どの OS の上でも同じ結果で扱えるようにするため。path/filepath は動いている OS の規則でしか働かず
// （macOS では `\` は区切りではなく、`C:\a` は 1 つのファイル名になる）、Windows だけで落ちる欠陥が
// 手元の検査では捕まらない。判定そのものをここへ出して、OS に依らない単体テストで押さえる。
//
// 扱う差:
//
//   - 区切りの違いと混在（`C:\a\b`・`C:/a/b`・`C:/a\b`）
//   - 大文字小文字（Windows のファイル名は区別しない。`C:\Users` と `c:\users` は同じ場所）
//   - 末尾の区切りの有無（`C:\a\` と `C:\a`）と、区切りの重なり（`C:\a\\b`）
//   - UNC（`\\server\share\a`。先頭の 2 つの区切りは畳まない）
//
// 扱えない差（既知の限界）: 8.3 の短い名前（`RUNNER~1`）・大文字小文字を区別する設定のボリューム・
// symlink や junction の先。これらは文字列の比較では見分けられない。

import (
	"runtime"
	"strings"
)

// isDriveLetter は c が Windows のドライブ名の文字か。
func isDriveLetter(c byte) bool { return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' }

// hasDrivePrefix は p が `C:` で始まるか（`C:\a`・`C:/a` のほか、ドライブからの相対 `C:a` も含む）。
func hasDrivePrefix(p string) bool { return len(p) >= 2 && p[1] == ':' && isDriveLetter(p[0]) }

// winStylePath は p が Windows の形のパスか（ドライブ名つき・UNC・`\` を区切りに使うもの）。
func winStylePath(p string) bool {
	return strings.HasPrefix(p, `\\`) || hasDrivePrefix(p) || strings.ContainsRune(p, '\\')
}

// caseFold は a と b を大文字小文字を区別せずに比べるべきか
// （どちらかが Windows の形のパス、または Windows の上で動いている）。
func caseFold(a, b string) bool {
	return runtime.GOOS == "windows" || winStylePath(a) || winStylePath(b)
}

// normPath は比べるためにパスの形をそろえる。区切りを `/` にそろえ、重なりと末尾の区切りを落とす
// （ルート `/`・UNC の先頭 `//`・ドライブ直下 `C:/` は残す）。fold なら大文字小文字もそろえる。
// `.` や `..` は解決しない（呼ぶ側が filepath で解いてから渡す）。
func normPath(p string, fold bool) string {
	if p == "" {
		return ""
	}
	s := strings.ReplaceAll(p, `\`, "/")
	unc := strings.HasPrefix(s, "//")
	for strings.Contains(s, "//") {
		s = strings.ReplaceAll(s, "//", "/")
	}
	if unc {
		s = "/" + s
	}
	for len(s) > 1 && strings.HasSuffix(s, "/") && !isPathRoot(s) {
		s = s[:len(s)-1]
	}
	if fold {
		s = strings.ToLower(s)
	}
	return s
}

// isPathRoot は（normPath が区切りをそろえた後の）s がそれ以上短くできない根か。
func isPathRoot(s string) bool {
	switch s {
	case "/", "//":
		return true
	}
	return len(s) == 3 && hasDrivePrefix(s) && s[2] == '/' // `C:/`
}

// samePath は a と b が同じ場所を指すか。
func samePath(a, b string, fold bool) bool {
	if a == "" || b == "" {
		return false
	}
	return normPath(a, fold) == normPath(b, fold)
}

// underPath は child が parent と同じか、その下にあるか。
func underPath(parent, child string, fold bool) bool {
	p, c := normPath(parent, fold), normPath(child, fold)
	if p == "" || c == "" {
		return false
	}
	if c == p {
		return true
	}
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return strings.HasPrefix(c, p)
}

// isAbsPath は p が絶対パスか。動いている OS に依らず、Windows の形（`C:\a`・`C:/a`・UNC `\\server\share`）も
// 絶対として扱う（`C:a` はドライブからの相対なので絶対ではない）。
func isAbsPath(p string) bool {
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		return true
	}
	return len(p) >= 3 && hasDrivePrefix(p) && (p[2] == '/' || p[2] == '\\')
}

// lostSeparators は p が「Windows のパスから区切りが落ちたもの」に見えるか。
//
// シェルの語の分け方では `\` はエスケープなので、引用符で囲まない Windows のパスは区切りを失う
// （`C:\Users\x` → `C:Usersx`）。復元はできない（どこが区切りだったか情報が残らない）ので、
// **いまの作業ツリーだと確かめられないもの**として扱う（fail-closed）ための見分けだけを行う。
func lostSeparators(p string) bool {
	return hasDrivePrefix(p) && !isAbsPath(p) && len(p) > 2
}
