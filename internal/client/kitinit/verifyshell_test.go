package kitinit

import (
	"errors"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
)

// TestVerifyShellCheck は doctor の「検証コマンドのシェル」の検査。
// Windows の verify は bash でしか動かないので、無ければ閉じられなくなる前に知らせる（注意。NG にはしない）。
// 実機の Windows が無くても確かめられるように、OS と探し方は引数で渡す。
func TestVerifyShellCheck(t *testing.T) {
	notFound := func(string) (string, error) { return "", errors.New("見つかりません") }
	noEnv := func(string) string { return "" }
	noFile := func(string) bool { return false }
	sysRoot := func(k string) string {
		if k == "SystemRoot" {
			return `C:\Windows`
		}
		return ""
	}
	found := func(want, path string) func(string) (string, error) {
		return func(n string) (string, error) {
			if n == want {
				return path, nil
			}
			return "", errors.New("見つかりません")
		}
	}

	for _, goos := range []string{"darwin", "linux"} {
		if show, _, _ := verifyShellCheck(i18n.JA, goos, notFound, noEnv, noFile); show {
			t.Errorf("%s では出さない（bash か sh が必ず見つかる）", goos)
		}
	}

	t.Run("bash があれば OK", func(t *testing.T) {
		show, good, msg := verifyShellCheck(i18n.JA, "windows", found("bash", `C:\Program Files\Git\bin\bash.exe`), sysRoot, noFile)
		if !show || !good {
			t.Fatalf("OK にする: show=%v good=%v", show, good)
		}
		if !strings.Contains(msg, `C:\Program Files\Git\bin\bash.exe`) {
			t.Errorf("見つけたシェルの場所を出す: %q", msg)
		}
	})

	t.Run("bash が無ければ注意", func(t *testing.T) {
		show, good, msg := verifyShellCheck(i18n.JA, "windows", notFound, sysRoot, noFile)
		if !show || good {
			t.Fatalf("注意を出す: show=%v good=%v", show, good)
		}
		// 注意は doctorReport.note が受け取る（ng を増やさないので終了コードは 0 のまま）。
		for _, w := range []string{"verify", "閉じられない", "Git for Windows", "winget"} {
			if !strings.Contains(msg, w) {
				t.Errorf("何が起きるか・どう直すかが分からない（%q が無い）: %q", w, msg)
			}
		}
	})

	t.Run("WSL の bash は使わない", func(t *testing.T) {
		// System32\bash.exe は WSL の起動口で、Linux の中で動くため作業ディレクトリも PATH も違う。
		if _, good, _ := verifyShellCheck(i18n.JA, "windows", found("bash", `C:\Windows\System32\bash.exe`), sysRoot, noFile); good {
			t.Error("WSL の bash を検証コマンドのシェルと見なしている")
		}
	})

	t.Run("Git の既定の置き場を見る", func(t *testing.T) {
		env := func(k string) string {
			if k == "ProgramFiles" {
				return `C:\Program Files`
			}
			return sysRoot(k)
		}
		exists := func(p string) bool { return p == `C:\Program Files\Git\bin\bash.exe` }
		show, good, msg := verifyShellCheck(i18n.JA, "windows", notFound, env, exists)
		if !show || !good || !strings.Contains(msg, `C:\Program Files\Git\bin\bash.exe`) {
			t.Errorf("PATH に無くても %%ProgramFiles%%\\Git から見つける: show=%v good=%v msg=%q", show, good, msg)
		}
	})
}
