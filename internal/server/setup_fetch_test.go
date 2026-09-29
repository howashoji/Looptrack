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
)

// TestSetupFetchKeepsExistingLooptrack は setup の取得コマンドが、置き場に looptrack が既にあるときは取得も置き換えもせず
// その looptrack で init だけを行うこと（配布が手元より古いと、置き換えは手元の新しい版を古い版に戻す）を、sh で実際に走らせて確かめる。
// 対照として、同じテストの中で looptrack が無いときは取得（curl）の経路を通って置くことも確かめる。
// curl と looptrack は偽物（呼ばれた引数を記録するだけ）。PowerShell の形は手元で実行できないので文字列だけを確かめる。
func TestSetupFetchKeepsExistingLooptrack(t *testing.T) {
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
	writeExec(t, filepath.Join(fakeBin, "curl"), "#!/bin/sh\necho \"$@\" >> \"$CURL_LOG\"\n"+
		"while [ $# -gt 0 ]; do if [ \"$1\" = -o ]; then cp \"$PAYLOAD\" \"$2\"; fi; shift; done\n")
	// 配布の中身（新しく置かれる looptrack）と、既に手元にある looptrack。どちらも呼ばれた引数を印つきで記録する
	payload := "#!/bin/sh\necho \"dist $*\" >> \"$INIT_LOG\"\n"
	existing := "#!/bin/sh\necho \"existing $*\" >> \"$INIT_LOG\"\n"
	payloadPath := filepath.Join(tmp, "payload")
	writeExec(t, payloadPath, payload)
	sum := sha256.Sum256([]byte(payload))

	g := &goSetup{lang: i18n.JA, os: runtime.GOOS, bins: []distBinaryJSON{{
		Name: "looptrack_v1.0.0_" + runtime.GOOS + "_" + runtime.GOARCH, OS: runtime.GOOS, Arch: runtime.GOARCH,
		Version: "v1.0.0", SHA256: hex.EncodeToString(sum[:]), URL: "https://example.invalid/looptrack/setup/x/looptrack",
	}}}
	cmdText := g.posixFetch("issue init --project req --agent claude-code")

	run := func(label, home string) (curlLog, initLog, stderr string) {
		t.Helper()
		logs := t.TempDir()
		cmd := exec.Command("/bin/sh", "-c", cmdText)
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

	// (i) 既にある: 取得しない・バイト単位で変わらない・その looptrack で init だけを行う
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin", "looptrack")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	writeExec(t, bin, existing)
	curlLog, initLog, stderr := run("既にある", home)
	if curlLog != "" {
		t.Errorf("既にあるのに curl が呼ばれた: %q", curlLog)
	}
	if b, err := os.ReadFile(bin); err != nil || string(b) != existing {
		t.Errorf("既にある looptrack が変わった: %v\n%q", err, b)
	}
	if _, err := os.Stat(bin + ".part"); err == nil {
		t.Errorf("取得の途中のファイルが残っている")
	}
	if initLog != "existing issue init --project req --agent claude-code\n" {
		t.Errorf("既にある looptrack で init が走っていない: %q", initLog)
	}
	if !strings.Contains(stderr, i18n.T(i18n.JA, "server.mcp.setup.fetch.skip_existing")) {
		t.Errorf("取得を省いた旨が出ていない: %q", stderr)
	}

	// (ii) 対照: 無ければ取得の経路を通り（curl が呼ばれ、SHA-256 を確かめて置く）、置いた looptrack で init する
	home = t.TempDir()
	bin = filepath.Join(home, ".local", "bin", "looptrack")
	curlLog, initLog, _ = run("無い", home)
	if !strings.Contains(curlLog, g.bins[0].URL) {
		t.Fatalf("無いのに curl が呼ばれていない（前提が崩れている: 取得の経路を通っていない）: %q", curlLog)
	}
	if b, err := os.ReadFile(bin); err != nil || string(b) != payload {
		t.Errorf("取得した looptrack が置かれていない: %v\n%q", err, b)
	}
	if initLog != "dist issue init --project req --agent claude-code\n" {
		t.Errorf("置いた looptrack で init が走っていない: %q", initLog)
	}

	// PowerShell: 既にあれば取得も置き換えもしない形（手元では実行できないので文字列だけ）
	g.os = "windows"
	g.bins = []distBinaryJSON{{OS: "windows", Arch: "amd64", SHA256: "ab", URL: "https://example.invalid/w"}}
	w := g.winFetch("issue init --project req")
	iTest, iElse := strings.Index(w, "if (Test-Path -PathType Leaf $B) {"), strings.Index(w, "} else {")
	iGet, iMove := strings.Index(w, "Invoke-WebRequest"), strings.Index(w, "Move-Item -Force $P $B }")
	if iTest < 0 || iElse < iTest || iGet < iElse || iMove < iGet || !strings.HasSuffix(w, goWinBin+" issue init --project req") {
		t.Errorf("PowerShell の取得コマンドの形: %s", w)
	}
}

func writeExec(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestSetupFetchWinKeepsExistingLooptrack は winFetch が返す PowerShell の取得コマンドを、Windows で実際に
// powershell.exe に実行させて確かめる（TestSetupFetchKeepsExistingLooptrack の PowerShell 版）。
// Invoke-WebRequest は偽の関数に、looptrack.exe は go build で作った偽の実行ファイル（呼ばれた引数を記録するだけ）に
// 差し替える。LOCALAPPDATA は t.TempDir に向ける。文面は英語（i18n.EN）にする — Windows PowerShell 5.1 は BOM の無い
// スクリプトを既定のコードページで読むため、日本語の埋め込み文字列だと文字化けして strings.Contains の比較が
// 環境依存で崩れる（ASCII だけの英語なら崩れない）。
func TestSetupFetchWinKeepsExistingLooptrack(t *testing.T) {
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
	cmdText := g.winFetch(tail)

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

	// (i) 既にある: Invoke-WebRequest が呼ばれない・既存の looptrack.exe がバイト単位で変わらない・その exe で init が走る
	localAppData := t.TempDir()
	binDir := filepath.Join(localAppData, "Programs", "looptrack")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(binDir, "looptrack.exe")
	if err := os.WriteFile(bin, existingBytes, 0o755); err != nil {
		t.Fatal(err)
	}
	initLog, fetchLog, out := run("既にある", localAppData)
	if fetchLog != "" {
		t.Errorf("既にあるのに Invoke-WebRequest が呼ばれた: %q", fetchLog)
	}
	if b, err := os.ReadFile(bin); err != nil || !bytes.Equal(b, existingBytes) {
		t.Errorf("既にある looptrack.exe が変わった: %v", err)
	}
	if _, err := os.Stat(filepath.Join(binDir, "looptrack.part")); err == nil {
		t.Errorf("取得の途中のファイルが残っている")
	}
	if !strings.Contains(initLog, "existing") || !strings.Contains(initLog, tail) {
		t.Errorf("既にある looptrack.exe で init が走っていない: %q", initLog)
	}
	if !strings.Contains(out, i18n.T(i18n.EN, "server.mcp.setup.fetch.skip_existing")) {
		t.Errorf("取得を省いた旨が出ていない: %q", out)
	}

	// (ii) 対照: 無ければ Invoke-WebRequest で取得し、SHA-256 を確かめて置き、置いた exe で init する
	localAppData = t.TempDir()
	binDir = filepath.Join(localAppData, "Programs", "looptrack")
	bin = filepath.Join(binDir, "looptrack.exe")
	initLog, fetchLog, _ = run("無い", localAppData)
	if !strings.Contains(fetchLog, g.bins[0].URL) {
		t.Fatalf("無いのに Invoke-WebRequest が呼ばれていない（前提が崩れている: 取得の経路を通っていない）: %q", fetchLog)
	}
	if b, err := os.ReadFile(bin); err != nil || !bytes.Equal(b, distBytes) {
		t.Errorf("取得した looptrack.exe が置かれていない: %v", err)
	}
	if _, err := os.Stat(filepath.Join(binDir, "looptrack.part")); err == nil {
		t.Errorf("取得の途中のファイルが残っている")
	}
	if !strings.Contains(initLog, "dist") || !strings.Contains(initLog, tail) {
		t.Errorf("置いた looptrack.exe で init が走っていない: %q", initLog)
	}
}
