package verify

import (
	"strings"

	"github.com/howashoji/looptrack/internal/i18n"
)

// Windows のシェルの選び方（build tag を付けず、どの OS でも単体テストできるようにパスは文字列で扱う）。

// ErrNoShell は Windows で bash も sh も見つからないとき。
// 文面は ID で持ち、出すときは i18n.Text で利用者の言語にする（errors.Is で見分けられる）。
var ErrNoShell = i18n.Errorf("verify.err.no_shell")

// WindowsShell は Windows で検証コマンドを渡すシェル。以前の CLI の規則（PATH の bash、無ければ sh）に次を足した:
//
//  1. PATH の bash.exe。ただし WSL の起動口（%SystemRoot%\System32\bash.exe・…\WindowsApps\bash.exe）は飛ばす
//     （WSL の Linux の中で動き、作業ディレクトリ・PATH・環境変数が Windows 側と違う）
//  2. PATH の git.exe から Git for Windows の bash（<Git>\bin\bash.exe・<Git>\usr\bin\bash.exe）
//  3. 既定の置き場（%ProgramFiles%\Git・%ProgramW6432%\Git・%ProgramFiles(x86)%\Git・%LOCALAPPDATA%\Programs\Git）の bin\bash.exe
//  4. PATH の sh.exe（WSL の起動口は同じく飛ばす）
//  5. どれも無ければ ErrNoShell（cmd.exe・PowerShell には落とさない。POSIX の書き方のコマンドを別の意味で動かさない）
//
// lookPath は exec.LookPath、getenv は os.Getenv、exists はファイルがあるか。
func WindowsShell(lookPath func(string) (string, error), getenv func(string) string, exists func(string) bool) (string, error) {
	systemRoot := getenv("SystemRoot")
	if systemRoot == "" {
		systemRoot = `C:\Windows`
	}
	usable := func(p string) bool {
		q := strings.ToLower(winClean(p))
		if q == strings.ToLower(winJoin(systemRoot, "System32", "bash.exe")) ||
			q == strings.ToLower(winJoin(systemRoot, "Sysnative", "bash.exe")) ||
			strings.Contains(q, `\windowsapps\`) {
			return false
		}
		return true
	}
	if p, err := lookPath("bash"); err == nil && usable(p) {
		return p, nil
	}
	var roots []string
	if g, err := lookPath("git"); err == nil {
		dir := winDir(winClean(g)) // <Git>\cmd・<Git>\bin・<Git>\mingw64\bin
		switch strings.ToLower(winBase(dir)) {
		case "cmd", "bin":
			parent := winDir(dir)
			if strings.EqualFold(winBase(parent), "mingw64") || strings.EqualFold(winBase(parent), "mingw32") {
				parent = winDir(parent)
			}
			roots = append(roots, parent)
		}
	}
	for _, v := range []string{"ProgramFiles", "ProgramW6432", "ProgramFiles(x86)"} {
		if d := getenv(v); d != "" {
			roots = append(roots, winJoin(d, "Git"))
		}
	}
	if d := getenv("LOCALAPPDATA"); d != "" {
		roots = append(roots, winJoin(d, "Programs", "Git"))
	}
	for _, root := range roots {
		for _, rel := range [][]string{{"bin", "bash.exe"}, {"usr", "bin", "bash.exe"}} {
			if p := winJoin(append([]string{root}, rel...)...); exists(p) {
				return p, nil
			}
		}
	}
	if p, err := lookPath("sh"); err == nil && usable(p) {
		return p, nil
	}
	return "", ErrNoShell
}

func winClean(p string) string { return strings.ReplaceAll(p, "/", `\`) }

func winJoin(parts ...string) string {
	out := ""
	for _, p := range parts {
		p = winClean(p)
		if out == "" {
			out = p
			continue
		}
		out = strings.TrimRight(out, `\`) + `\` + strings.TrimLeft(p, `\`)
	}
	return out
}

func winDir(p string) string {
	p = strings.TrimRight(p, `\`)
	if i := strings.LastIndexByte(p, '\\'); i >= 0 {
		return p[:i]
	}
	return ""
}

func winBase(p string) string {
	p = strings.TrimRight(p, `\`)
	return p[strings.LastIndexByte(p, '\\')+1:]
}
