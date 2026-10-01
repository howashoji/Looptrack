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
