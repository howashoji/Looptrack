package setuppath

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
)

// TestPosixAddsPathOnce は、PATH を足す sh の断片を sh・bash・zsh で実際に流し、
//   - 利用者の既定のシェル（$SHELL）に合う起動ファイルに 1 行だけ足すこと（2 回流しても増えない）
//   - いまの PATH に置き場があれば何も書かないこと（対照: 同じ HOME で PATH に無ければ書く）
//   - 足した行をそのシェルで読むと置き場が PATH に入り、2 回読んでも重ならないこと
//
// を確かめる。HOME は一時ディレクトリ（利用者の起動ファイルには触れない）。ZDOTDIR・XDG_CONFIG_HOME も一時に向ける。
func TestPosixAddsPathOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh の断片は macOS・Linux 向け")
	}
	shells := []string{"/bin/sh"}
	for _, name := range []string{"bash", "zsh"} {
		if p, err := exec.LookPath(name); err == nil {
			shells = append(shells, p)
		} else {
			t.Logf("%s が無いので、%s では流さない", name, name)
		}
	}
	basePath := "/usr/bin:/bin"
	cmdText := PosixCommand(i18n.JA)
	run := func(t *testing.T, shell string, env []string) string {
		t.Helper()
		cmd := exec.Command(shell, "-c", cmdText)
		cmd.Env = env
		var se bytes.Buffer
		cmd.Stderr = &se
		if out, err := cmd.Output(); err != nil {
			t.Fatalf("断片が失敗した: %v\n%s%s", err, out, se.String())
		}
		return se.String()
	}
	count := func(t *testing.T, file, line string) int {
		t.Helper()
		b, err := os.ReadFile(file)
		if err != nil {
			return 0
		}
		return strings.Count(string(b), line+"\n")
	}
	for _, shell := range shells {
		t.Run(filepath.Base(shell), func(t *testing.T) {
			for _, tc := range []struct {
				label, login string
				pre          []string // 先に作っておく起動ファイル（HOME からの相対）
				want, absent []string // 行が 1 つ入るファイル・作らないファイル
				zdotdir      bool     // ZDOTDIR を環境に置く（HOME の下の zdot）
			}{
				{"zsh（ZDOTDIR が無い）", "/bin/zsh", nil, []string{".zshenv"}, []string{".profile", ".zshrc", "zdot/.zshenv"}, false},
				// ~/.zshenv の中で ZDOTDIR を決めている利用者のシェルから流した形: どちらにも書く
				{"zsh（ZDOTDIR が環境にある）", "/bin/zsh", []string{".zshenv"}, []string{".zshenv", "zdot/.zshenv"}, []string{".profile", ".zshrc"}, true},
				{"bash（ログインのファイルが無い）", "/bin/bash", nil, []string{".bashrc", ".profile"}, []string{".bash_profile"}, false},
				{"bash（~/.bash_profile がある）", "/usr/local/bin/bash", []string{".bash_profile"}, []string{".bashrc", ".bash_profile"}, []string{".profile"}, false},
				{"bash（~/.bash_login だけがある）", "/bin/bash", []string{".bash_login"}, []string{".bashrc", ".bash_login"}, []string{".profile", ".bash_profile"}, false},
				{"fish", "/opt/homebrew/bin/fish", nil, []string{"xdg/fish/conf.d/looptrack.fish"}, []string{".profile"}, false},
				// SHELL が無い形は試さない: bash（macOS の /bin/sh も）は SHELL が無ければ passwd のログインシェルを入れるので、
				// 結果が流すシェルとこの開発機の設定で変わる（それ自体は利用者の既定のシェルに合う正しい動き）
				{"sh", "/bin/sh", nil, []string{".profile"}, []string{".bashrc", ".zshenv"}, false},
			} {
				t.Run(tc.label, func(t *testing.T) {
					// HOME は ASCII だけの名前にする（t.TempDir はテストの名前の全角を含む。zsh 5.9 は環境の ZDOTDIR の
					// 全角を壊して読むので、ZDOTDIR の場合が zsh 自身の制約で落ちる。2026-10-01 に macOS の zsh 5.9 で実測）
					home, err := os.MkdirTemp("", "lt-setuppath-home")
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { os.RemoveAll(home) })
					for _, f := range tc.pre {
						body := "# 利用者のもの\n"
						if f == ".zshenv" {
							body += `export ZDOTDIR="$HOME/zdot"` + "\n"
						}
						if err := os.WriteFile(filepath.Join(home, f), []byte(body), 0o644); err != nil {
							t.Fatal(err)
						}
					}
					dir := filepath.Join(home, ".local", "bin")
					// UTF-8 のロケールで流す（macOS の bash 3.2 は、このときだけ変数名に続く全角の文字を名前の一部として読む）
					env := []string{"HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, "xdg"), "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8"}
					if tc.zdotdir {
						env = append(env, "ZDOTDIR="+filepath.Join(home, "zdot"))
					}
					if tc.login != "" {
						env = append(env, "SHELL="+tc.login)
					}

					// 対照: 置き場が PATH にあれば何も書かない（同じ HOME で、下では書く）
					stderr := run(t, shell, append(env, "PATH="+dir+":"+basePath))
					for _, f := range append(append([]string{}, tc.want...), tc.absent...) {
						if _, err := os.Stat(filepath.Join(home, f)); err == nil && !contains(tc.pre, f) {
							t.Errorf("置き場が PATH にあるのに %s を作った", f)
						}
					}
					if stderr != "" {
						t.Errorf("置き場が PATH にあるのに何か出した: %q", stderr)
					}
					// 末尾に / の付いた形も PATH にあるとみなす
					run(t, shell, append(env, "PATH="+dir+"/:"+basePath))

					// PATH に無い: 足す。2 回流しても 1 行のまま（2 回目は、流すシェルが zsh で足した先が .zshenv なら、
					// そのシェル自身が起動のときに読んで PATH に置き場が入るので、何も出さないこともある）
					stderr = run(t, shell, append(env, "PATH="+basePath))
					if !strings.Contains(stderr, "AI のアプリと端末を開き直す") {
						t.Errorf("開き直す案内が出ていない: %q", stderr)
					}
					run(t, shell, append(env, "PATH="+basePath))
					line := PosixLine
					if tc.label == "fish" {
						line = FishLine
					}
					for _, f := range tc.want {
						if n := count(t, filepath.Join(home, f), line); n != 1 {
							t.Errorf("%s の行が %d 本（1 本のはず）", f, n)
						}
						if !strings.Contains(stderr, "（"+filepath.Join(home, ".local", "bin")+"）") || !strings.Contains(stderr, filepath.Join(home, f)) {
							t.Errorf("案内に %s が出ていない: %q", f, stderr)
						}
					}
					for _, f := range tc.absent {
						if _, err := os.Stat(filepath.Join(home, f)); err == nil && !contains(tc.pre, f) {
							t.Errorf("%s を作った（作らないはず）", f)
						}
					}
					for _, f := range tc.pre {
						if b, _ := os.ReadFile(filepath.Join(home, f)); !strings.HasPrefix(string(b), "# 利用者のもの\n") {
							t.Errorf("%s の元の中身が変わった: %q", f, b)
						}
					}
					// zsh: ZDOTDIR を環境に持たない新しい zsh -c（新しい端末・GUI から起動した AI）と、ZDOTDIR を環境に持つ
					// zsh -c の両方で、置き場の looptrack が見つかる（対照: 足す前の HOME では見つからないことは TestZshFindsLooptrack）
					if strings.HasPrefix(tc.label, "zsh") {
						zshFinds(t, home, false)
						if tc.zdotdir {
							zshFinds(t, home, true)
						}
					}
				})
			}

			// 足した行をそのシェルで読む: 置き場が PATH の先頭に入り、2 回読んでも重ならない
			home := t.TempDir()
			rc := filepath.Join(home, "rc")
			if err := os.WriteFile(rc, []byte(PosixLine+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(shell, "-c", `. "$1"; . "$1"; echo "$PATH"`, "sh", rc)
			cmd.Env = []string{"HOME=" + home, "PATH=" + basePath}
			out, err := cmd.Output()
			want := filepath.Join(home, ".local", "bin") + ":" + basePath + "\n"
			if err != nil || string(out) != want {
				t.Errorf("足した行を読んだ後の PATH: %v %q（want %q）", err, out, want)
			}
		})
	}
}

// TestPosixBodyFailsSoft は、起動ファイルに書けないとき、理由を出して成功で終える（後ろの init を止めない）ことを確かめる。
// 対照: 同じ形で書ける HOME なら書けた旨が出る。
func TestPosixBodyFailsSoft(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh の断片は macOS・Linux 向け")
	}
	if os.Geteuid() == 0 {
		t.Skip("root は書けないファイルにも書ける")
	}
	cmdText := PosixCommand(i18n.JA) + ` && echo next`
	run := func(home string) (string, string) {
		cmd := exec.Command("/bin/sh", "-c", cmdText)
		cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin", "SHELL=/bin/sh"}
		var so, se bytes.Buffer
		cmd.Stdout, cmd.Stderr = &so, &se
		if err := cmd.Run(); err != nil {
			t.Fatalf("断片が失敗で終わった: %v\n%s", err, se.String())
		}
		return so.String(), se.String()
	}
	ok := t.TempDir()
	if so, se := run(ok); so != "next\n" || !strings.Contains(se, "開き直す") {
		t.Fatalf("前提が崩れています: 書ける HOME で足せていない: %q %q", so, se)
	}
	ro := t.TempDir()
	if err := os.WriteFile(filepath.Join(ro, ".profile"), []byte("# x\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	if so, se := run(ro); so != "next\n" || !strings.Contains(se, "足せませんでした") {
		t.Errorf("書けないときの扱い: %q %q", so, se)
	}
}

// TestCommandsAreWrapped は、単独で流す形が呼び出し元に残さない包み（sh はサブシェル・PowerShell は & { }）に入っていることを
// 確かめる（実行して残らないことは TestPosixCommandLeavesNothing と、取得 + init のテスト（internal/server）が確かめる）。
func TestCommandsAreWrapped(t *testing.T) {
	p, w := PosixCommand(i18n.EN), WinCommand(i18n.EN)
	if !strings.HasPrefix(p, `(D="$HOME/.local/bin" && case ":$PATH:"`) || !strings.HasSuffix(p, "esac)") {
		t.Errorf("sh の形: %s", p)
	}
	if !strings.HasPrefix(w, "& { $ErrorActionPreference = 'Stop'; $D = ") || !strings.HasSuffix(w, " }") ||
		!strings.Contains(w, WinEnvKey) || !strings.Contains(w, WinBroadcast) {
		t.Errorf("PowerShell の形: %s", w)
	}
	// setx は 1024 文字で切り詰めるので使わない。SetEnvironmentVariable で Path を書くと REG_SZ になるので使わない
	if strings.Contains(w, "setx") || strings.Contains(w, "SetEnvironmentVariable('Path'") {
		t.Errorf("PowerShell で Path を書く方法: %s", w)
	}
}

// TestPosixCommandLeavesNothing は、単独の形を流した後に、変数 D・L・W と関数 lt_rc を呼び出し元のシェルに残さないことを
// 確かめる。対照: 包む前の中身（D を代入して PosixBody）を同じシェルで流すと残る。
func TestPosixCommandLeavesNothing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh の断片は macOS・Linux 向け")
	}
	probe := `; echo "left=[${D-}|${L-}|${W-}|$(command -v lt_rc >/dev/null 2>&1 && echo fn)]"`
	run := func(text string) string {
		cmd := exec.Command("/bin/sh", "-c", text+probe)
		cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin"}
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("断片が失敗した: %v", err)
		}
		return string(out)
	}
	if got := run(PosixCommand(i18n.JA)); got != "left=[|||]\n" {
		t.Errorf("呼び出し元に残った: %q", got)
	}
	if got := run(`D="$HOME/.local/bin" && ` + PosixBody(i18n.JA)); !strings.HasSuffix(got, "|fn]\n") || strings.HasPrefix(got, "left=[|") {
		t.Errorf("前提が崩れています: 包まない形でも残らない（検査が空振りしている）: %q", got)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// TestFishLine は fish の conf.d に置く行を fish で 2 回読み、置き場が PATH に 1 回だけ入ることを確かめる（fish が無ければ省く）。
func TestFishLine(t *testing.T) {
	fish, err := exec.LookPath("fish")
	if err != nil {
		t.Skip("fish が無い")
	}
	home := t.TempDir()
	rc := filepath.Join(home, "looptrack.fish")
	if err := os.WriteFile(rc, []byte(FishLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(fish, "--no-config", "-c", `source $argv[1]; source $argv[1]; string join : $PATH`, rc)
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
	out, err := cmd.Output()
	if want := filepath.Join(home, ".local", "bin") + ":/usr/bin:/bin\n"; err != nil || string(out) != want {
		t.Errorf("fish で読んだ後の PATH: %v %q（want %q）", err, out, want)
	}
}

// TestWinAddsPathOnce は、PATH を足す PowerShell の形を Windows で実際に流して確かめる（Windows でだけ走る）。
// 利用者の環境変数を書き換えないよう、レジストリのキーを使い捨てのキー（HKCU\Software\LooptrackTest-…）に差し替え、
// 通知（WinBroadcast）は外す。確かめること:
//   - Path に無ければ足す。元の項目（%USERPROFILE% のような展開前の形）と値の種類（REG_EXPAND_SZ）を保つ
//   - 2 回流しても増えない。大小と末尾の \ だけが違う項目があれば足さない（対照: 無ければ足す）
//   - いまのプロセスの Path に置き場があれば何も書かない
func TestWinAddsPathOnce(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PowerShell の形は Windows でだけ実行できる")
	}
	psExe, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Fatalf("powershell.exe が無い（Windows 標準の一部のはず）: %v", err)
	}
	key := `Software\LooptrackTest-` + filepath.Base(t.TempDir())
	ps := func(t *testing.T, script string, env ...string) string {
		t.Helper()
		f := filepath.Join(t.TempDir(), "run.ps1")
		if err := os.WriteFile(f, []byte(script), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(psExe, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", f)
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("PowerShell が失敗した: %v\n%s", err, out)
		}
		return strings.ReplaceAll(string(out), "\r\n", "\n")
	}
	t.Cleanup(func() {
		_ = exec.Command(psExe, "-NoProfile", "-Command", `Remove-Item -Recurse -Force -ErrorAction SilentlyContinue 'HKCU:\`+key+`'`).Run()
	})
	cmdText := strings.ReplaceAll(WinCommand(i18n.EN), WinEnvKey, `[Microsoft.Win32.Registry]::CurrentUser.CreateSubKey('`+key+`')`)
	cmdText = strings.ReplaceAll(cmdText, WinBroadcast, "$null = 0")
	if strings.Contains(cmdText, "'Environment'") || strings.Contains(cmdText, "SetEnvironmentVariable") {
		t.Fatalf("前提が崩れています: 利用者の環境変数を指す式が残っている: %s", cmdText)
	}
	set := func(t *testing.T, value string) {
		t.Helper()
		ps(t, `$k = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey('`+key+`'); $k.SetValue('Path', '`+value+`', 'ExpandString'); $k.Close()`)
	}
	get := func(t *testing.T) string {
		t.Helper()
		return strings.TrimSpace(ps(t, `$k = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('`+key+`'); `+
			`Write-Output ("{0}|{1}" -f $k.GetValueKind('Path'), $k.GetValue('Path', '', 'DoNotExpandEnvironmentNames'))`))
	}
	local := t.TempDir()
	dir := filepath.Join(local, "Programs", "looptrack")
	cleanPath := `PATH=C:\Windows\System32;C:\Windows`

	// Path に無い: 足す（元の項目は展開前の形のまま・種類は REG_EXPAND_SZ のまま）。2 回流しても増えない
	set(t, `%USERPROFILE%\AppData\Local\Microsoft\WindowsApps`)
	for i := 0; i < 2; i++ {
		if out := ps(t, cmdText, "LOCALAPPDATA="+local, cleanPath); !strings.Contains(out, "Reopen the AI app") {
			t.Errorf("%d 回目: 開き直す案内が出ていない: %q", i+1, out)
		}
	}
	if got, want := get(t), `ExpandString|%USERPROFILE%\AppData\Local\Microsoft\WindowsApps;`+dir; got != want {
		t.Errorf("足した後の Path: %q（want %q）", got, want)
	}

	// 大小と末尾の \ だけが違う項目がある: 足さない
	set(t, `C:\x;`+strings.ToUpper(dir)+`\`)
	ps(t, cmdText, "LOCALAPPDATA="+local, cleanPath)
	if got, want := get(t), `ExpandString|C:\x;`+strings.ToUpper(dir)+`\`; got != want {
		t.Errorf("同じ項目があるのに書き換えた: %q（want %q）", got, want)
	}

	// いまのプロセスの Path に置き場がある: 何も書かず、何も出さない
	set(t, `C:\x`)
	if out := ps(t, cmdText, "LOCALAPPDATA="+local, `PATH=C:\Windows\System32;`+dir); strings.Contains(out, "Reopen") {
		t.Errorf("Path にあるのに案内が出た: %q", out)
	}
	if got := get(t); got != `ExpandString|C:\x` {
		t.Errorf("Path にあるのに書き換えた: %q", got)
	}
}

// zshFinds は、home/.local/bin に偽の looptrack を置き、PATH に置き場の無い新しい zsh -c で command -v looptrack が
// それを指すことを確かめる（withZdotdir なら ZDOTDIR=home/zdot を環境に置く）。zsh が無ければ何もしない。
func zshFinds(t *testing.T, home string, withZdotdir bool) {
	t.Helper()
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Log("zsh が無いので、新しい zsh で見つかるかは確かめない")
		return
	}
	bin := filepath.Join(home, ".local", "bin", "looptrack")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(zsh, "-c", "command -v looptrack || echo notfound")
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
	if withZdotdir {
		cmd.Env = append(cmd.Env, "ZDOTDIR="+filepath.Join(home, "zdot"))
	}
	if out, err := cmd.Output(); err != nil || strings.TrimSpace(string(out)) != bin {
		t.Errorf("新しい zsh -c（ZDOTDIR を環境に置く: %v）で looptrack が見つからない: %v %q", withZdotdir, err, out)
	}
}

// TestZshFindsLooptrack は zshFinds の対照: 起動ファイルに行を足す前の HOME では、新しい zsh -c で looptrack が見つからない
// （検査が実際に zsh の起動ファイルの効き目を見ていること）。
func TestZshFindsLooptrack(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh が無い")
	}
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin", "looptrack")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(zsh, "-c", "command -v looptrack || echo notfound")
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
	if out, _ := cmd.Output(); strings.TrimSpace(string(out)) != "notfound" {
		t.Fatalf("前提が崩れています: 行を足す前から looptrack が見つかる: %q", out)
	}
}
