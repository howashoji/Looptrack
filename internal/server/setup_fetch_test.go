package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/setuppath"
)

// TestSetupFetchReplacesUnlessSameSHA は setup の取得コマンドが、置き場の looptrack が配布物と SHA-256 で同じときだけ
// 取得を省き、違うとき・無いときは取得して確かめてから置き換え、置いた looptrack で init を行うことを、シェルで実際に
// 走らせて確かめる（サーバ版の利用者の手元にあるのは setup が配布物から置いた looptrack だけ、という前提で、版の新旧は比べず
// 配布物にそろえる）。zsh は変数の値を単語に分けないので、手順は sh・bash・zsh の全部で流す（無いシェルはその旨をログに出す）。
// 「同じなら取得しない」の対照として、同じテストの中で「違う」「無い」ときに取得の経路（curl）を通ることを確かめる。
// curl と looptrack は偽物（呼ばれた引数を記録するだけ）。PowerShell の形は手元で実行できないので文字列だけを確かめる。
func TestSetupFetchReplacesUnlessSameSHA(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("sh の取得コマンドは macOS・Linux 向け")
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("配布の対象でない CPU")
	}
	sumCmd := "shasum"
	if runtime.GOOS == "linux" {
		sumCmd = "sha256sum"
	}
	if _, err := exec.LookPath(sumCmd); err != nil {
		t.Skip(sumCmd + " が無い")
	}

	tmp := t.TempDir()
	fakeBin := filepath.Join(tmp, "fakebin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	// 偽の curl: 引数を記録し、-o の先に配布の中身（偽の looptrack）を書く
	// （書いた先も記録する: 失敗のときに .part を消したことの対照として、.part が一度は作られたことを示す）
	writeExec(t, filepath.Join(fakeBin, "curl"), "#!/bin/sh\necho \"$@\" >> \"$CURL_LOG\"\n"+
		"while [ $# -gt 0 ]; do if [ \"$1\" = -o ]; then cp \"$PAYLOAD\" \"$2\" && echo \"wrote $2\" >> \"$CURL_LOG\"; fi; shift; done\n")
	// 配布の中身（新しく置かれる looptrack）と、配布物と違う手元の looptrack。どちらも呼ばれた引数を印つきで記録する
	payload := "#!/bin/sh\necho \"dist $*\" >> \"$INIT_LOG\"\n"
	other := "#!/bin/sh\necho \"other $*\" >> \"$INIT_LOG\"\n"
	payloadPath := filepath.Join(tmp, "payload")
	writeExec(t, payloadPath, payload)
	sum := sha256.Sum256([]byte(payload))

	g := &goSetup{lang: i18n.JA, os: runtime.GOOS, bins: []distBinaryJSON{{
		Name: "looptrack_v1.0.0_" + runtime.GOOS + "_" + runtime.GOARCH, OS: runtime.GOOS, Arch: runtime.GOARCH,
		Version: "v1.0.0", SHA256: hex.EncodeToString(sum[:]), URL: "https://example.invalid/looptrack/setup/x/looptrack",
	}}}
	const tail = "issue init --project req --url https://example.invalid/looptrack --agent claude-code"
	cmdText := g.posixFetch(tail)
	skip := i18n.T(i18n.JA, "server.mcp.setup.fetch.skip_same")

	shells := []string{"/bin/sh"}
	for _, name := range []string{"bash", "zsh"} {
		if p, err := exec.LookPath(name); err == nil {
			shells = append(shells, p)
		} else {
			t.Logf("%s が無いので、%s では流さない", name, name)
		}
	}
	for _, shell := range shells {
		t.Run(filepath.Base(shell), func(t *testing.T) {
			run := func(label, home string) (curlLog, initLog, stderr string) {
				t.Helper()
				logs := t.TempDir()
				cmd := exec.Command(shell, "-c", cmdText)
				cmd.Env = []string{"HOME=" + home, "PATH=" + fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
					"CURL_LOG=" + filepath.Join(logs, "curl"), "INIT_LOG=" + filepath.Join(logs, "init"), "PAYLOAD=" + payloadPath}
				var se bytes.Buffer
				cmd.Stderr = &se
				if err := cmd.Run(); err != nil {
					t.Fatalf("%s: 取得コマンドが失敗した: %v\n%s", label, err, se.String())
				}
				c, _ := os.ReadFile(filepath.Join(logs, "curl"))
				i, _ := os.ReadFile(filepath.Join(logs, "init"))
				return string(c), string(i), se.String()
			}
			place := func(home string) string {
				t.Helper()
				bin := filepath.Join(home, ".local", "bin", "looptrack")
				if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
					t.Fatal(err)
				}
				return bin
			}
			wantFetched := func(label, home, curlLog, initLog string) {
				t.Helper()
				if !strings.Contains(curlLog, g.bins[0].URL) {
					t.Fatalf("%s: curl が呼ばれていない（取得の経路を通っていない）: %q", label, curlLog)
				}
				bin := filepath.Join(home, ".local", "bin", "looptrack")
				if fi, err := os.Lstat(bin); err != nil || !fi.Mode().IsRegular() {
					t.Errorf("%s: 置き場が普通のファイルでない: %v %v", label, fi, err)
				}
				if b, err := os.ReadFile(bin); err != nil || string(b) != payload {
					t.Errorf("%s: 取得した looptrack が置かれていない: %v\n%q", label, err, b)
				}
				if _, err := os.Stat(bin + ".part"); err == nil {
					t.Errorf("%s: 取得の途中のファイルが残っている", label)
				}
				if initLog != "dist "+tail+"\n" {
					t.Errorf("%s: 置いた looptrack で init が走っていない: %q", label, initLog)
				}
			}

			// (i) 配布物と同じ: 取得しない（ダウンロードの要求が来ない）・バイト単位で変わらない・その looptrack で init だけを行う
			home := t.TempDir()
			writeExec(t, place(home), payload)
			curlLog, initLog, stderr := run("同じ", home)
			if curlLog != "" {
				t.Errorf("配布物と同じなのに curl が呼ばれた: %q", curlLog)
			}
			if initLog != "dist "+tail+"\n" {
				t.Errorf("置き場の looptrack で init が走っていない: %q", initLog)
			}
			if !strings.Contains(stderr, skip) {
				t.Errorf("取得を省いた旨が出ていない: %q", stderr)
			}

			// (ii) 配布物と違う: 取得して置き換え、置き換えた looptrack で init する（違う looptrack では init しない）
			home = t.TempDir()
			writeExec(t, place(home), other)
			curlLog, initLog, stderr = run("違う", home)
			wantFetched("違う", home, curlLog, initLog)
			if strings.Contains(stderr, skip) {
				t.Errorf("違うのに取得を省いた旨が出た: %q", stderr)
			}

			// (iii) 配布物と違うファイルへの symlink: リンクを置き換え、リンクの先は書き換えない
			home = t.TempDir()
			target := filepath.Join(t.TempDir(), "looptrack-elsewhere")
			writeExec(t, target, other)
			if err := os.Symlink(target, place(home)); err != nil {
				t.Fatal(err)
			}
			curlLog, initLog, _ = run("symlink", home)
			wantFetched("symlink", home, curlLog, initLog)
			if b, err := os.ReadFile(target); err != nil || string(b) != other {
				t.Errorf("symlink の先が書き換わった: %v\n%q", err, b)
			}

			// (iv) 無い: 取得の経路を通り（curl が呼ばれ、SHA-256 を確かめて置く）、置いた looptrack で init する
			home = t.TempDir()
			curlLog, initLog, _ = run("無い", home)
			wantFetched("無い", home, curlLog, initLog)

			// (v) 実行の後に、変数 U・S・D と（/usr/bin/sum を覆い隠す）関数 sum を呼び出し元のシェルに残さない（手順は利用者の
			// 端末に貼られることがある）。対照: 包む前の中身を同じシェルで流すと残る（検査がシェルの状態を実際に見ていること）
			left := func(label, text string) string {
				t.Helper()
				logs := t.TempDir()
				cmd := exec.Command(shell, "-c", text+fetchLeftProbe)
				cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=" + fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
					"CURL_LOG=" + filepath.Join(logs, "curl"), "INIT_LOG=" + filepath.Join(logs, "init"), "PAYLOAD=" + payloadPath}
				var so, se bytes.Buffer
				cmd.Stdout, cmd.Stderr = &so, &se
				if err := cmd.Run(); err != nil {
					t.Fatalf("%s: 取得コマンドが失敗した: %v\n%s", label, err, se.String())
				}
				out := so.String() // sum -c の「…: OK」が前に出るので、探りの行から後ろだけを見る
				if i := strings.LastIndex(out, "left=["); i >= 0 {
					return out[i:]
				}
				return out
			}
			if got := left("包んだ形", cmdText); got != fetchLeftClean {
				t.Errorf("取得 + init の後にシェルへ変数・関数が残った: %q", got)
			}
			if got := left("包む前", g.posixFetchBody(tail)); !strings.HasPrefix(got, "left=["+g.bins[0].URL+"|"+g.bins[0].SHA256+"|") ||
				!strings.Contains(got, "|fn|"+setuppath.PosixLine+"|") || !strings.HasSuffix(got, "|ltfn]\n") {
				t.Errorf("前提が崩れています: 包まない形でもシェルに変数・関数が残らない（検査が空振りしている）: %q", got)
			}

			// (vi) 取得したものの SHA-256 が合わない: 失敗で終わり、.part を残さず、置き場にも置かず、init もしない
			home = t.TempDir()
			logs := t.TempDir()
			bad := filepath.Join(t.TempDir(), "bad")
			writeExec(t, bad, other)
			cmd := exec.Command(shell, "-c", cmdText)
			cmd.Env = []string{"HOME=" + home, "PATH=" + fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
				"CURL_LOG=" + filepath.Join(logs, "curl"), "INIT_LOG=" + filepath.Join(logs, "init"), "PAYLOAD=" + bad}
			if out, err := cmd.CombinedOutput(); err == nil {
				t.Errorf("SHA-256 が合わないのに成功した: %s", out)
			}
			bin := filepath.Join(home, ".local", "bin", "looptrack")
			if c, _ := os.ReadFile(filepath.Join(logs, "curl")); !strings.Contains(string(c), "wrote "+bin+".part") {
				t.Fatalf("前提が崩れています: 取得の経路で .part が作られていない（消したことを確かめられない）: %q", c)
			}
			if _, err := os.Lstat(bin + ".part"); err == nil {
				t.Errorf("SHA-256 が合わないときに .part が残った")
			}
			if _, err := os.Lstat(bin); err == nil {
				t.Errorf("SHA-256 が合わないのに置き場に置いた")
			}
			if i, _ := os.ReadFile(filepath.Join(logs, "init")); len(i) != 0 {
				t.Errorf("SHA-256 が合わないのに init が走った: %q", i)
			}
		})
	}

	// PowerShell: 置き場の looptrack.exe が配布物と同じ SHA-256 のときだけ取得を省く形（手元では実行できないので文字列だけ）
	g.os = "windows"
	g.bins = []distBinaryJSON{{OS: "windows", Arch: "amd64", SHA256: "ab", URL: "https://example.invalid/w"}}
	w := g.winFetch("issue init --project req")
	if !strings.HasPrefix(w, "& { $ErrorActionPreference = ") || !strings.HasSuffix(w, " }") {
		t.Errorf("PowerShell の取得コマンドが & { } で包まれていない: %s", w)
	}
	w = innerCmd(w)
	iCase := strings.Index(w, "switch ($env:PROCESSOR_ARCHITECTURE)")
	iTest := strings.Index(w, "if ((Test-Path -PathType Leaf $B) -and ((Get-FileHash -Algorithm SHA256 -Path $B).Hash -eq $S)) {")
	iElse := strings.Index(w, "} else {")
	iGet, iMove := strings.Index(w, "Invoke-WebRequest"), strings.Index(w, "Move-Item -Force $P $B }")
	if iCase < 0 || iTest < iCase || iElse < iTest || iGet < iElse || iMove < iGet || !strings.HasSuffix(w, goWinBin+" issue init --project req") {
		t.Errorf("PowerShell の取得コマンドの形: %s", w)
	}
}

// fetchLeftProbe は取得 + init の後ろに付けて、呼び出し元のシェルに残った変数 U・S・D と、sum が関数として残っているか
// （fn）、PATH を足す断片（internal/setuppath）の変数 L・W と関数 lt_rc（ltfn）を 1 行で出す。
// fetchLeftClean は何も残っていないときの行。
const (
	fetchLeftProbe = `; echo "left=[${U-}|${S-}|${D-}|$(command -v sum | grep -qx sum && echo fn)|${L-}|${W-}|$(command -v lt_rc | grep -qx lt_rc && echo ltfn)]"`
	fetchLeftClean = "left=[||||||]\n"
)

// fetchOnly は手順の取得 + init のコマンドから、前置と包みを外し、init の手前で切った取得だけの中身（包む前の形の対照に使う）。
func fetchOnly(t *testing.T, cmd string) string {
	t.Helper()
	c := innerCmd(cmd)
	cut := strings.Index(c, ` && "$HOME/.local/bin/looptrack" issue init`)
	if cut < 0 {
		t.Fatalf("取得コマンドの形: %s", cmd)
	}
	return c[:cut]
}

func writeExec(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestSetupFetchWinReplacesUnlessSameSHA は winFetch が返す PowerShell の取得コマンドを、Windows で実際に
// powershell.exe に実行させて確かめる（TestSetupFetchReplacesUnlessSameSHA の PowerShell 版）。
// Invoke-WebRequest は偽の関数に、looptrack.exe は go build で作った偽の実行ファイル（呼ばれた引数を記録するだけ）に
// 差し替える。LOCALAPPDATA は t.TempDir に向ける。文面は英語（i18n.EN）にする — Windows PowerShell 5.1 は BOM の無い
// スクリプトを既定のコードページで読むため、日本語の埋め込み文字列だと文字化けして strings.Contains の比較が
// 環境依存で崩れる（ASCII だけの英語なら崩れない）。
func TestSetupFetchWinReplacesUnlessSameSHA(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("winFetch の PowerShell は Windows でだけ実行できる")
	}
	psExe, err := exec.LookPath("powershell.exe")
	if err != nil {
		// .github/workflows/ci.yml の go-test-windows は runs-on: windows-latest（実機の Windows）。
		// powershell.exe は Windows 標準の一部で必ずあるので、無いのは環境の異常として Fatal にする（Skip にしない）。
		t.Fatalf("powershell.exe が無い（Windows 標準の一部のはず）: %v", err)
	}

	buildFakeExe := func(label string) string {
		t.Helper()
		dir := t.TempDir()
		src := fmt.Sprintf(`package main

import (
	"fmt"
	"os"
)

func main() {
	f, err := os.OpenFile(os.Getenv("INIT_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %%v\n", os.Args[1:])
}
`, label)
		srcPath := filepath.Join(dir, "main.go")
		if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		outPath := filepath.Join(dir, label+".exe")
		if out, err := exec.Command("go", "build", "-o", outPath, srcPath).CombinedOutput(); err != nil {
			t.Fatalf("偽の looptrack.exe（%s）のビルドに失敗: %v\n%s", label, err, out)
		}
		return outPath
	}
	existingExe := buildFakeExe("existing")
	distExe := buildFakeExe("dist")
	distBytes, err := os.ReadFile(distExe)
	if err != nil {
		t.Fatal(err)
	}
	existingBytes, err := os.ReadFile(existingExe)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(distBytes)

	g := &goSetup{lang: i18n.EN, os: "windows", bins: []distBinaryJSON{
		{OS: "windows", Arch: "amd64", SHA256: hex.EncodeToString(sum[:]), URL: "https://example.invalid/looptrack/setup/x/looptrack.exe"},
	}}
	tail := "issue init --project req --agent claude-code"
	// PATH を足す断片は利用者の環境変数（HKCU\Environment）を書くので、使い捨てのキーに差し替え、通知は外す
	// （断片そのものの振る舞いは internal/setuppath の TestWinAddsPathOnce が確かめる）
	testKey := `Software\LooptrackTest-` + filepath.Base(t.TempDir())
	t.Cleanup(func() {
		_ = exec.Command(psExe, "-NoProfile", "-Command", `Remove-Item -Recurse -Force -ErrorAction SilentlyContinue 'HKCU:\`+testKey+`'`).Run()
	})
	safeReg := func(c string) string {
		c = strings.ReplaceAll(c, setuppath.WinEnvKey, `[Microsoft.Win32.Registry]::CurrentUser.CreateSubKey('`+testKey+`')`)
		c = strings.ReplaceAll(c, setuppath.WinBroadcast, "$null = 0")
		if strings.Contains(c, "'Environment'") || strings.Contains(c, "SetEnvironmentVariable") {
			t.Fatalf("前提が崩れています: 利用者の環境変数を指す式が残っている: %s", c)
		}
		return c
	}
	cmdText := safeReg(g.winFetch(tail))

	// Invoke-WebRequest を偽の関数に差し替える。呼ばれたら $env:FETCH_LOG に URL を記録し、$env:PAYLOAD を $OutFile へ置く。
	fakeInvokeWebRequest := "function Invoke-WebRequest {\n" +
		"    param([switch]$UseBasicParsing, [string]$Uri, [string]$OutFile)\n" +
		"    Add-Content -Path $env:FETCH_LOG -Value $Uri\n" +
		"    Copy-Item -Path $env:PAYLOAD -Destination $OutFile -Force\n" +
		"}\n"
	script := fakeInvokeWebRequest + cmdText

	run := func(label, localAppData string) (initLog, fetchLog, out string) {
		t.Helper()
		logsDir := t.TempDir()
		scriptPath := filepath.Join(t.TempDir(), "run.ps1")
		if err := os.WriteFile(scriptPath, []byte(script), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(psExe, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", scriptPath)
		cmd.Env = append(os.Environ(),
			"LOCALAPPDATA="+localAppData,
			"PROCESSOR_ARCHITECTURE=AMD64",
			"INIT_LOG="+filepath.Join(logsDir, "init"),
			"FETCH_LOG="+filepath.Join(logsDir, "fetch"),
			"PAYLOAD="+distExe,
		)
		var buf bytes.Buffer
		cmd.Stdout, cmd.Stderr = &buf, &buf
		if err := cmd.Run(); err != nil {
			t.Fatalf("%s: PowerShell の取得コマンドが失敗した: %v\n%s", label, err, buf.String())
		}
		i, _ := os.ReadFile(filepath.Join(logsDir, "init"))
		f, _ := os.ReadFile(filepath.Join(logsDir, "fetch"))
		return string(i), string(f), buf.String()
	}

	skip := i18n.T(i18n.EN, "server.mcp.setup.fetch.skip_same")
	place := func(localAppData string, body []byte) string {
		t.Helper()
		binDir := filepath.Join(localAppData, "Programs", "looptrack")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		bin := filepath.Join(binDir, "looptrack.exe")
		if body != nil {
			if err := os.WriteFile(bin, body, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		return bin
	}
	wantFetched := func(label, bin, initLog, fetchLog string) {
		t.Helper()
		if !strings.Contains(fetchLog, g.bins[0].URL) {
			t.Fatalf("%s: Invoke-WebRequest が呼ばれていない（取得の経路を通っていない）: %q", label, fetchLog)
		}
		if b, err := os.ReadFile(bin); err != nil || !bytes.Equal(b, distBytes) {
			t.Errorf("%s: 取得した looptrack.exe が置かれていない: %v", label, err)
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(bin), "looptrack.part")); err == nil {
			t.Errorf("%s: 取得の途中のファイルが残っている", label)
		}
		if !strings.HasPrefix(initLog, "dist") || !strings.Contains(initLog, tail) {
			t.Errorf("%s: 置いた looptrack.exe で init が走っていない: %q", label, initLog)
		}
	}

	// (i) 配布物と同じ: Invoke-WebRequest が呼ばれない・バイト単位で変わらない・その exe で init が走る
	localAppData := t.TempDir()
	bin := place(localAppData, distBytes)
	initLog, fetchLog, out := run("同じ", localAppData)
	if fetchLog != "" {
		t.Errorf("配布物と同じなのに Invoke-WebRequest が呼ばれた: %q", fetchLog)
	}
	if b, err := os.ReadFile(bin); err != nil || !bytes.Equal(b, distBytes) {
		t.Errorf("置き場の looptrack.exe が変わった: %v", err)
	}
	if !strings.HasPrefix(initLog, "dist") || !strings.Contains(initLog, tail) {
		t.Errorf("置き場の looptrack.exe で init が走っていない: %q", initLog)
	}
	if !strings.Contains(out, skip) {
		t.Errorf("取得を省いた旨が出ていない: %q", out)
	}

	// (ii) 配布物と違う: Invoke-WebRequest で取得し、SHA-256 を確かめて置き換え、置き換えた exe で init する
	localAppData = t.TempDir()
	bin = place(localAppData, existingBytes)
	initLog, fetchLog, out = run("違う", localAppData)
	wantFetched("違う", bin, initLog, fetchLog)
	if strings.Contains(out, skip) {
		t.Errorf("違うのに取得を省いた旨が出た: %q", out)
	}

	// (iii) 無い: 取得して置き、置いた exe で init する
	localAppData = t.TempDir()
	bin = filepath.Join(localAppData, "Programs", "looptrack", "looptrack.exe")
	initLog, fetchLog, _ = run("無い", localAppData)
	wantFetched("無い", bin, initLog, fetchLog)

	// (iv) 実行の後に、$ErrorActionPreference・$ProgressPreference と $U・$S・$D・$B・$P を呼び出し元のセッションに残さない
	// （全体を & { } で包む）。対照: 包む前の中身を同じ形で流すと残る（検査がセッションの状態を実際に見ていること）
	left := func(label, body string) string {
		t.Helper()
		logsDir := t.TempDir()
		scriptPath := filepath.Join(t.TempDir(), "left.ps1")
		// $R は Close() の後のレジストリのキー。文字列に埋め込むと ToString が閉じたキーに触れて例外になり
		// （Cannot convert value to type System.String）、包まない形（下の対照）ではスクリプトごと止まる。
		// そのため $R だけは値ではなく、残っているかどうか（R か空）を出す
		probe := "\nWrite-Output \"left=[$U|$S|$D|$B|$P|$(if ($null -ne $R) { 'R' })|$O|$H|$K|$ErrorActionPreference|$ProgressPreference]\"\n"
		src := "$ErrorActionPreference = 'Continue'; $ProgressPreference = 'Continue'\n" + fakeInvokeWebRequest + body + probe
		if err := os.WriteFile(scriptPath, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(psExe, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", scriptPath)
		cmd.Env = append(os.Environ(), "LOCALAPPDATA="+t.TempDir(), "PROCESSOR_ARCHITECTURE=AMD64",
			"INIT_LOG="+filepath.Join(logsDir, "init"), "FETCH_LOG="+filepath.Join(logsDir, "fetch"), "PAYLOAD="+distExe)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: PowerShell の取得コマンドが失敗した: %v\n%s", label, err, out)
		}
		s := strings.ReplaceAll(string(out), "\r\n", "\n")
		if i := strings.LastIndex(s, "left=["); i >= 0 {
			return strings.TrimSpace(s[i:])
		}
		return s
	}
	if got := left("包んだ形", cmdText); got != "left=[|||||||||Continue|Continue]" {
		t.Errorf("取得 + init の後にセッションへ変数・設定が残った: %q", got)
	}
	// 対照は $R の印（|R|）も見る（包んだ形で $R の欄が空なのが、印を出す式の空振りでないこと）
	if got := left("包む前", safeReg(g.winFetchBody(tail))); !strings.HasPrefix(got, "left=["+g.bins[0].URL+"|") || !strings.Contains(got, "|R|") ||
		!strings.HasSuffix(got, "|Stop|SilentlyContinue]") {
		t.Errorf("前提が崩れています: 包まない形でもセッションに変数・設定が残らない（検査が空振りしている）: %q", got)
	}
}

// TestSetupFetchWrappedForEveryAgent は、取得 + init のコマンドがどの AI 向けでも（Codex・Copilot の環境変数の前置の
// 内側でも）sh はサブシェル、PowerShell は & { } で包まれていることを確かめる（実行して残らないことは
// TestSetupFetchReplacesUnlessSameSHA・TestSetupEndToEnd・TestSetupAgentEnvPrefix が確かめる）。
// 対照: 包む前の中身（posixFetchBody・winFetchBody）は包みの形に当たらない（判定が素通りしないこと）。
func TestSetupFetchWrappedForEveryAgent(t *testing.T) {
	g := &goSetup{lang: i18n.JA, os: "", bins: []distBinaryJSON{
		{OS: "darwin", Arch: "arm64", SHA256: "aa", URL: "https://example.invalid/d"},
		{OS: "windows", Arch: "amd64", SHA256: "bb", URL: "https://example.invalid/w.exe"},
	}}
	posixWrapped := func(c string) bool {
		return strings.HasPrefix(c, `(D="$HOME/.local/bin" && `) && strings.HasSuffix(c, ")")
	}
	winWrapped := func(c string) bool {
		return strings.HasPrefix(c, "& { $ErrorActionPreference = 'Stop'; ") && strings.HasSuffix(c, " }")
	}
	if posixWrapped(g.posixFetchBody("issue init")) || winWrapped(g.winFetchBody("issue init")) {
		t.Fatal("前提が崩れています: 包む前の中身が包みの形に当たる（判定が素通りする）")
	}
	for _, agent := range agentKinds {
		steps := setupStepsFor(i18n.JA, agent, "req", "https://example.invalid/lt", "https://example.invalid/lt/setup/x",
			installStateJSON{State: "missing"}, distLatest{}, g, "no")
		c, w := innerEnvCmd(steps[0].Command), innerEnvCmd(steps[0].CommandWindows)
		if !posixWrapped(c) || !strings.Contains(c, "curl -fsSL") {
			t.Errorf("%s: sh の取得 + init がサブシェルで包まれていない: %s", agent, steps[0].Command)
		}
		if !winWrapped(w) || !strings.Contains(w, "Invoke-WebRequest") {
			t.Errorf("%s: PowerShell の取得 + init が & { } で包まれていない: %s", agent, steps[0].CommandWindows)
		}
	}
}

// TestSetupSelfRepoFetchOmitsInit: looptrack 自身のリポジトリ（kit の正本）からの通知（self_repo）があった導入で
// looptrack が古いとき、setup の取得の手順は取得・置き換え・PATH の段だけで、init を含まない（init はクライアントが
// 「自身です」と拒否するので、含めると looptrack は置き換わったのにコマンド全体が失敗で終わる）。古いときの案内
// （update_command）と手順の見出しも init を書かず、同じ結果の loop の行（init を使えない）と食い違わない。
// 対照として、同じ中身で印の無い通知では従来どおり取得 + init を返すことを同じテストで確かめる。
// sh と PowerShell の両方を、OS の指定ごと（macOS・Linux・Windows・不明で両方）に見る。
func TestSetupSelfRepoFetchOmitsInit(t *testing.T) {
	withLoopKit(t, loopFixture())
	e, _, ed := newAPIEnv(t)
	withFakeDist(t, e) // 配布中は v1.0.0（全 OS）
	m := e.mcpAsClient(ed.token, map[string]string{"X-Looptrack-Project": "req"}, "claude-code", "2.1.0", "2025-06-18")
	latest, _ := latestDist()
	post := func(body map[string]any) installStateJSON {
		t.Helper()
		var st installStateJSON
		ed.json(200, "POST", "/projects/req/install", body, &st)
		return st
	}
	// stale は配布中の v1.0.0 より古い v0.9.0 の looptrack からの通知。loop は入れた（問いが出ない）か未選択
	body := func(self bool, loop map[string]any) map[string]any {
		b := installBody("claude-code", "hook", "server", latest.Core, loop)
		b["client"] = map[string]any{"version": "v0.9.0", "os": "darwin", "arch": "arm64"}
		if self {
			b["self_repo"] = true
		}
		return b
	}
	installed := map[string]any{"installed": true, "bundle_sha256": latest.Loop, "version": "v1.0.0"}
	fetchTitle := i18n.T(i18n.JA, "server.mcp.setup.step.fetch")
	selfTitle := i18n.T(i18n.JA, "server.mcp.setup.step.fetch_self_repo")
	refetch, selfRefetch := i18n.T(i18n.JA, "server.mcp.setup.update.refetch"), i18n.T(i18n.JA, "server.mcp.setup.update.refetch_self_repo")
	if selfTitle == "server.mcp.setup.step.fetch_self_repo" || selfRefetch == "server.mcp.setup.update.refetch_self_repo" ||
		i18n.T(i18n.EN, "server.mcp.setup.step.fetch_self_repo") == selfTitle || i18n.T(i18n.EN, "server.mcp.setup.update.refetch_self_repo") == selfRefetch {
		t.Fatalf("正本向けの文面が ja.json / en.json に無いか、日英が同じ")
	}
	// 本文の配布物の注記: 印なしは「init が kit を一覧の SHA-256 で確かめる」、正本は init に触れない文
	distInitNote := "init が kit を一覧の SHA-256 で確かめる"
	distSelfNote := "kit の SHA-256 は使わない"
	for _, k := range []string{"server.mcp.setup.text.dist_note_self_repo", "server.mcp.setup.step.fetch_manual_self_repo"} {
		if ja, en := i18n.T(i18n.JA, k), i18n.T(i18n.EN, k); ja == k || en == k || ja == en || strings.Contains(en, "init verifies") {
			t.Fatalf("%s が ja.json / en.json に無いか、日英が同じか、init に触れている: ja=%q en=%q", k, ja, en)
		}
	}
	posixEnd := "{ " + setuppath.PosixBody(i18n.JA) + "; })"
	winEnd := "; " + setuppath.WinBody(i18n.JA) + " }"
	osCases := []struct {
		name       string
		args       map[string]any
		posix, win bool // 手順に sh / PowerShell の形が出るか
	}{
		{"macOS", map[string]any{"os": "darwin"}, true, false},
		{"Linux", map[string]any{"os": "linux"}, true, false},
		{"Windows", map[string]any{"os": "windows"}, false, true},
		{"不明", map[string]any{}, true, true},
	}
	// fetchCmds は取得の手順の (sh, PowerShell) のコマンド。Windows を指定したときは主のコマンドが PowerShell
	fetchCmds := func(out loopSetupOut, win bool) (string, string) {
		if win && out.Steps[0].CommandWindows == "" {
			return "", out.Steps[0].Command
		}
		return out.Steps[0].Command, out.Steps[0].CommandWindows
	}

	// 対照（印なし・loop 入れた）: 取得 + init を返す
	if st := post(body(false, installed)); st.State != "stale" || st.SelfRepo || st.UpdateCommand != refetch {
		t.Fatalf("前提が崩れています（印なしの古い通知が stale にならない）: %+v", st)
	}
	for _, c := range osCases {
		out := loopSetupOf(t, second(m.call("setup", c.args, false)))
		if out.Ask != "" || len(out.Steps) != 2 || out.Steps[0].Title != fetchTitle {
			t.Fatalf("前提が崩れています（印なし・%s の手順）: ask=%q %+v", c.name, out.Ask, out.Steps)
		}
		sh, ps := fetchCmds(out, c.win && !c.posix)
		if c.posix && (!strings.Contains(sh, `curl -fsSL "$U"`) || !strings.Contains(sh, goPosixBin+" issue init --project req")) {
			t.Errorf("前提が崩れています（印なし・%s の sh に取得 + init が無い）: %s", c.name, sh)
		}
		if c.win && (!strings.Contains(ps, "Invoke-WebRequest") || !strings.Contains(ps, goWinBin+" issue init --project req")) {
			t.Errorf("前提が崩れています（印なし・%s の PowerShell に取得 + init が無い）: %s", c.name, ps)
		}
		if !strings.Contains(out.Text, distInitNote) {
			t.Errorf("前提が崩れています（印なし・%s の本文に「init が kit を確かめる」の文が無い）:\n%s", c.name, out.Text)
		}
	}

	// 正本（self_repo）: loop を入れた導入でも、未選択の導入でも、取得の手順は init を含まない
	for _, loop := range []map[string]any{installed, {"installed": false}} {
		st := post(body(true, loop))
		if st.State != "stale" || !st.SelfRepo || st.UpdateCommand != selfRefetch || strings.Contains(st.Message, refetch) ||
			!strings.Contains(st.Message, selfRefetch) {
			t.Errorf("self_repo（loop=%v）の古いときの案内: %+v", loop["installed"], st)
		}
		for _, c := range osCases {
			label := fmt.Sprintf("self_repo（loop=%v・%s）", loop["installed"], c.name)
			out := loopSetupOf(t, second(m.call("setup", c.args, false)))
			if out.Ask != "" || len(out.Steps) != 2 {
				t.Errorf("%s の手順: ask=%q %+v", label, out.Ask, out.Steps)
				continue
			}
			if out.Steps[0].Who != "ai" || out.Steps[0].Title != selfTitle || strings.Contains(out.Steps[0].Title, "init を行う") {
				t.Errorf("%s の取得の手順の見出し: %+v", label, out.Steps[0])
			}
			sh, ps := fetchCmds(out, c.win && !c.posix)
			if c.posix && (!strings.Contains(sh, `curl -fsSL "$U"`) || !strings.HasSuffix(sh, posixEnd)) {
				t.Errorf("%s の sh が取得・置き換え・PATH の段で終わっていない: %s", label, sh)
			}
			if c.win && (!strings.Contains(ps, "Invoke-WebRequest") || !strings.HasSuffix(ps, winEnd)) {
				t.Errorf("%s の PowerShell が取得・置き換え・PATH の段で終わっていない: %s", label, ps)
			}
			if (sh != "") != c.posix || (ps != "") != c.win {
				t.Errorf("%s の OS の形: sh=%q ps=%q", label, sh, ps)
			}
			// 手順のどこにも init のコマンドが無い（本文の辞退の勧めを含めて）。確認の issue installed は残す
			for _, s := range out.Steps {
				if strings.Contains(s.Command+s.CommandWindows+s.Title, "issue init") {
					t.Errorf("%s の手順に init がある: %+v", label, s)
				}
			}
			if strings.Contains(out.Text, "issue init") || !strings.Contains(out.Steps[1].Command+out.Steps[1].CommandWindows, "issue installed --agent claude-code") {
				t.Errorf("%s の本文か確認の手順:\n%s", label, out.Text)
			}
			// loop が未選択の正本では「init を使えない」の行が出る。手順もそれと食い違わない（上で init が無いことを確かめた）
			if loop["installed"] == false && !strings.Contains(out.Text, i18n.T(i18n.JA, "server.mcp.setup.loop.self_repo")) {
				t.Errorf("%s の本文に正本の loop の行が無い:\n%s", label, out.Text)
			}
			// 配布物の注記も init に触れない（手順に init が無いのに「init が kit を確かめる」と書かない）
			if strings.Contains(out.Text, distInitNote) || !strings.Contains(out.Text, distSelfNote) {
				t.Errorf("%s の本文の配布物の注記:\n%s", label, out.Text)
			}
		}
	}

	// 配布物にその OS 向けの looptrack が無い（取得の手順を出せず、利用者の手順になる）経路。
	// 配布は darwin/arm64 だけにして、os=linux で呼ぶ。対照: 印なしは init のコマンドを見出しに書く
	e.s.cfg.DistDir = t.TempDir()
	writeDist(t, e.s.cfg.DistDir, map[string]string{"looptrack_v1.0.0_darwin_arm64": "da"})
	manualSelf := i18n.T(i18n.JA, "server.mcp.setup.step.fetch_manual_self_repo", "reason",
		i18n.T(i18n.JA, "server.mcp.setup.dist.missing_os", "os", "linux"))
	if st := post(body(false, installed)); st.State != "stale" || st.SelfRepo {
		t.Fatalf("前提が崩れています（印なし・配布が darwin だけで stale にならない）: %+v", st)
	}
	out := loopSetupOf(t, second(m.call("setup", map[string]any{"os": "linux"}, false)))
	if len(out.Steps) == 0 || out.Steps[0].Who != "human" || out.Steps[0].Command != "" ||
		!strings.Contains(out.Steps[0].Title, "looptrack issue init --project req") || out.Steps[0].Title == manualSelf {
		t.Fatalf("前提が崩れています（印なし・配布の無い OS で init のコマンドを書いた利用者の手順にならない）: %+v", out.Steps)
	}
	if st := post(body(true, installed)); st.State != "stale" || !st.SelfRepo {
		t.Fatalf("self_repo・配布が darwin だけ: %+v", st)
	}
	out = loopSetupOf(t, second(m.call("setup", map[string]any{"os": "linux"}, false)))
	if len(out.Steps) == 0 || out.Steps[0].Who != "human" || out.Steps[0].Command != "" || out.Steps[0].Title != manualSelf {
		t.Errorf("self_repo・配布の無い OS の手順: %+v", out.Steps)
	}
	for _, st := range out.Steps {
		if strings.Contains(st.Title+st.Command+st.CommandWindows, "issue init") {
			t.Errorf("self_repo・配布の無い OS の手順に init がある: %+v", st)
		}
	}
	if strings.Contains(out.Text, "issue init") || strings.Contains(out.Text, distInitNote) {
		t.Errorf("self_repo・配布の無い OS の本文:\n%s", out.Text)
	}
}

// TestSetupFetchWithoutInitRuns: 取得の後ろの init を外した形（kit の正本向け。posixFetch("")）を sh で実際に流し、
// 取得して置き換え、PATH の段まで進んで成功で終わり、置いた looptrack を一度も呼ばないことを確かめる。
// 対照として、同じ材料で init を付けた形は置いた looptrack を呼ぶ（呼び出しの記録が検査として働いている）ことを同じテストで見る。
func TestSetupFetchWithoutInitRuns(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("sh の取得コマンドは macOS・Linux 向け")
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("配布の対象でない CPU")
	}
	sumCmd := "shasum"
	if runtime.GOOS == "linux" {
		sumCmd = "sha256sum"
	}
	if _, err := exec.LookPath(sumCmd); err != nil {
		t.Skip(sumCmd + " が無い")
	}
	tmp := t.TempDir()
	fakeBin := filepath.Join(tmp, "fakebin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExec(t, filepath.Join(fakeBin, "curl"), "#!/bin/sh\necho \"$@\" >> \"$CURL_LOG\"\n"+
		"while [ $# -gt 0 ]; do if [ \"$1\" = -o ]; then cp \"$PAYLOAD\" \"$2\"; fi; shift; done\n")
	payload := "#!/bin/sh\necho \"dist $*\" >> \"$INIT_LOG\"\n"
	payloadPath := filepath.Join(tmp, "payload")
	writeExec(t, payloadPath, payload)
	sum := sha256.Sum256([]byte(payload))
	g := &goSetup{lang: i18n.JA, os: runtime.GOOS, bins: []distBinaryJSON{{
		Name: "looptrack_v1.0.0_" + runtime.GOOS + "_" + runtime.GOARCH, OS: runtime.GOOS, Arch: runtime.GOARCH,
		Version: "v1.0.0", SHA256: hex.EncodeToString(sum[:]), URL: "https://example.invalid/looptrack/setup/x/looptrack",
	}}}
	run := func(cmdText string) (home, curlLog, initLog string) {
		t.Helper()
		home, logs := t.TempDir(), t.TempDir()
		cmd := exec.Command("/bin/sh", "-c", cmdText)
		cmd.Env = []string{"HOME=" + home, "SHELL=/bin/sh", "PATH=" + fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
			"CURL_LOG=" + filepath.Join(logs, "curl"), "INIT_LOG=" + filepath.Join(logs, "init"), "PAYLOAD=" + payloadPath}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("取得コマンドが失敗した: %v\n%s", err, out)
		}
		c, _ := os.ReadFile(filepath.Join(logs, "curl"))
		i, _ := os.ReadFile(filepath.Join(logs, "init"))
		return home, string(c), string(i)
	}
	const tail = "issue init --project req --url https://example.invalid/looptrack --agent claude-code"
	if _, _, initLog := run(g.posixFetch(tail)); initLog != "dist "+tail+"\n" {
		t.Fatalf("前提が崩れています（init を付けた形で、置いた looptrack の呼び出しが記録されない）: %q", initLog)
	}
	home, curlLog, initLog := run(g.posixFetch(""))
	if !strings.Contains(curlLog, g.bins[0].URL) {
		t.Errorf("init を外した形で取得の経路を通っていない: %q", curlLog)
	}
	if b, err := os.ReadFile(filepath.Join(home, ".local", "bin", "looptrack")); err != nil || string(b) != payload {
		t.Errorf("init を外した形で取得した looptrack が置かれていない: %v %q", err, b)
	}
	if initLog != "" {
		t.Errorf("init を外した形で置いた looptrack が呼ばれた: %q", initLog)
	}
	if strings.Contains(g.posixFetch("")+g.winFetch(""), "issue init") || strings.Contains(g.winFetch(""), goWinBin) {
		t.Errorf("init を外した形に looptrack の呼び出しが残っている")
	}
}
