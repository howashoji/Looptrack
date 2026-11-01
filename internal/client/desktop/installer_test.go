package desktop

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/client/env"
)

// issPath は Windows のインストーラの設定（Inno Setup 7）。
const issPath = "../../../deploy/release/windows/Looptrack.iss"

// installerSmokePath はリリースの組み立て（release.yml の desktop-windows-installer）が無人の上書きを試す smoke の本体。
const installerSmokePath = "../../../deploy/release/installer_smoke.ps1"

// releaseYMLPath はリリースの組み立て。installer_smoke.ps1 を締切つきで呼ぶ段がある。
const releaseYMLPath = "../../../.github/workflows/release.yml"

// runDetachedPath は段のプロセスから切り離して締切つきの中身を起動する薄い役。
const runDetachedPath = "../../../deploy/release/run_detached.ps1"

// runWithDeadlinePath は切り離した側で中身を締切つきで動かすスクリプト。
const runWithDeadlinePath = "../../../deploy/release/run_with_deadline.ps1"

// smokeCleanupSeconds は締切どうしの間に置く後始末の時間。段の側の足し算は run_detached.ps1 の冒頭にある。
const smokeCleanupSeconds = 30

// psArgList は PowerShell の配列の 1 行（$name = @('a','b',…)）から、一重引用符の項目を順に取り出す（無ければ nil）。
// smoke の引数と Go の installerArgs を同じ読み方で比べるために使う。
func psArgList(text, name string) []string {
	m := regexp.MustCompile(`(?m)^\s*\$` + regexp.QuoteMeta(name) + `\s*=\s*@\(([^)]*)\)`).FindStringSubmatch(text)
	if m == nil {
		return nil
	}
	var out []string
	for _, q := range regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(m[1], -1) {
		out = append(out, q[1])
	}
	return out
}

func readISS(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(issPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Windows のインストーラの必須の設定（利用者の判断）を確かめる。ISCC は Windows でしか動かないので、
// 実際のコンパイルと導入は release.yml の desktop-windows-installer で行い、ここでは設定の中身だけを見る。
func TestInstallerScriptSettings(t *testing.T) {
	iss := readISS(t)
	for _, want := range []string{
		// 管理者権限が要らない・置き場・スタートメニュー
		"PrivilegesRequired=lowest",
		`DefaultDirName={localappdata}\Programs\Looptrack Desktop`,
		"DisableProgramGroupPage=yes",
		`Name: "{group}\Looptrack"`,
		// 起動中の上書き（Restart Manager と、[Code] の desktop --quit）
		"CloseApplications=yes",
		// 日本語と英語
		`MessagesFile: "compiler:Languages\Japanese.isl"`,
		`MessagesFile: "compiler:Default.isl"`,
		// 第三者のライセンス文
		`Source: "{#MySourceDir}\NOTICE"`,
		// 中身（持ち運び用の zip と同じもの）
		`Source: "{#MySourceDir}\Looptrack.exe"`,
		`Source: "{#MySourceDir}\cli\looptrack.exe"`,
		// 起動（postinstall）
		"Flags: nowait postinstall skipifsilent",
	} {
		if !strings.Contains(iss, want) {
			t.Errorf("Looptrack.iss に %q がありません", want)
		}
	}

	// AppId は固定（{#…} のマクロで版ごとに変わらない）。変えると更新が上書きにならない
	appID := regexp.MustCompile(`(?m)^AppId=\{\{[0-9A-Fa-f-]{36}\}\s*$`)
	if !appID.MatchString(iss) {
		t.Error("AppId が固定の GUID（AppId={{<GUID>}）ではありません")
	}

	// デスクトップのショートカットは出さない（利用者の判断 4）
	for _, ng := range []string{"{autodesktop}", "{userdesktop}", "{commondesktop}"} {
		if strings.Contains(iss, ng) {
			t.Errorf("デスクトップのショートカット（%s）は出さない決定です", ng)
		}
	}

	// 選択肢（Tasks）は「ログイン時に起動する」「CLI を使えるようにする」の 2 つだけ（データを消す選択肢は出さない）
	names := regexp.MustCompile(`(?m)^Name: "([a-z]+)"; Description:`).FindAllStringSubmatch(iss, -1)
	var got []string
	for _, m := range names {
		got = append(got, m[1])
	}
	if strings.Join(got, ",") != "startup,cli" {
		t.Errorf("[Tasks] = %v, want [startup cli]", got)
	}

	// アンインストールはデータを消さない（消す指示が無いこと）
	if strings.Contains(iss, "{localappdata}\\Looptrack\"") || strings.Contains(iss, "[UninstallDelete]") {
		t.Error("アンインストールでデータを消す指示があります（データは残す決定です）")
	}

	// 無人の上書き（1 クリックの置き換え）の後の起動し直し（受け入れ条件 7）。DeinitializeSetup は成功でも失敗でも呼ばれる
	// （Inno が戻したときも）ので、/RELAUNCH=1 なら成功・失敗を見ずに desktop --after-update を待たずに起こす。[Run] には足さない
	// （/VERYSILENT では skipifsilent で飛ばされ、失敗したときは [Run] まで進まない）
	m := regexp.MustCompile(`(?s)procedure DeinitializeSetup\(\);.*?\nend;`).FindString(iss)
	if m == "" {
		t.Fatal("Looptrack.iss の [Code] に DeinitializeSetup がありません")
	}
	for _, want := range []string{
		"ExpandConstant('{param:relaunch|0}') <> '1'",
		"Exec(AppExe(), 'desktop --after-update', '', SW_SHOWNORMAL, ewNoWait, Code)",
		"try", "except",
	} {
		if !strings.Contains(m, want) {
			t.Errorf("DeinitializeSetup に %q がありません", want)
		}
	}
	for _, ng := range []string{"CurStepChanged", "ssDone", "WizardSilent"} {
		if strings.Contains(m, ng) {
			t.Errorf("DeinitializeSetup が成功・失敗で分けています（%s）。どちらでも起動し直す決定です", ng)
		}
	}
	runSec := regexp.MustCompile(`(?s)\n\[Run\]\n(.*?)\n\[`).FindStringSubmatch(iss)
	if runSec == nil {
		t.Fatal("Looptrack.iss の [Run] を読めません")
	}
	if strings.Contains(runSec[1], "--after-update") {
		t.Error("[Run] に --after-update があります（起動し直しは DeinitializeSetup だけが行う）")
	}
	// 渡す側（Go の installerArgs）の /RELAUNCH=1 と、受ける側（{param:relaunch|0}）の名前が同じこと。Inno の {param} は大小を区別しない
	relaunch := ""
	for _, a := range installerArgs {
		if k, v, ok := strings.Cut(a, "="); ok && strings.EqualFold(k, "/RELAUNCH") {
			relaunch = v
		}
	}
	if relaunch != "1" {
		t.Errorf("installerArgs に /RELAUNCH=1 がありません: %v", installerArgs)
	}

	// smoke（installer_smoke.ps1）の無人の上書きの引数（$updateArgs）が Go の installerArgs と同じ並びで、/TASKS を含まないこと。
	// 対照: 同じ読み方で、最初の導入の引数（$setupArgs）は /TASKS=startup,cli を含むと読める
	yml, err := os.ReadFile(installerSmokePath)
	if err != nil {
		t.Fatal(err)
	}
	setupArgs := psArgList(string(yml), "setupArgs")
	if !slices.Contains(setupArgs, "/TASKS=startup,cli") {
		t.Fatalf("前提が崩れています: installer_smoke.ps1 の $setupArgs を読めないか /TASKS を見分けられません: %v", setupArgs)
	}
	updateArgs := psArgList(string(yml), "updateArgs")
	if !slices.Equal(updateArgs, installerArgs[:]) {
		t.Errorf("installer_smoke.ps1 の $updateArgs = %v, want（replace.go の installerArgs）%v", updateArgs, installerArgs)
	}
	for _, a := range updateArgs {
		if strings.HasPrefix(strings.ToUpper(a), "/TASKS") {
			t.Errorf("installer_smoke.ps1 の $updateArgs に /TASKS があります: %v", updateArgs)
		}
	}

	// 上の引数を持つ installer_smoke.ps1 を、release.yml が切り離した起動（run_detached.ps1）を通して呼び、
	// 中身の締切 < 段の側の締切 < 段の上限 の順になっていること。
	// 段の子として動かすと、残ったプロセスが受け継いだハンドルで段を終わらせなくなり、段の上限が効かなくなる
	relBytes, err := os.ReadFile(releaseYMLPath)
	if err != nil {
		t.Fatal(err)
	}
	rel := string(relBytes)
	call := regexp.MustCompile(`timeout-minutes: (\d+)\n\s*run: \|\n[^\n]*\n\s*pwsh\b[^\n]*-File deploy/release/run_detached\.ps1[^\n]*\n\s*-Script deploy/release/installer_smoke\.ps1 -Seconds (\d+) -OuterSeconds (\d+)`).FindStringSubmatch(rel)
	if call == nil {
		t.Fatalf("release.yml が run_detached.ps1 -Script deploy/release/installer_smoke.ps1 -Seconds <秒> -OuterSeconds <秒> を呼んでいません")
	}
	stepMin, _ := strconv.Atoi(call[1])
	inner, _ := strconv.Atoi(call[2])
	outer, _ := strconv.Atoi(call[3])
	if inner+smokeCleanupSeconds > outer || outer+smokeCleanupSeconds > stepMin*60 {
		t.Errorf("締切の間に後始末の %d 秒がありません: 中身 %d 秒・段の側 %d 秒・段の上限 %d 秒", smokeCleanupSeconds, inner, outer, stepMin*60)
	}
	if strings.Contains(rel, "-File deploy/release/run_with_deadline.ps1") {
		t.Errorf("release.yml が run_with_deadline.ps1 を段の子として直接起動しています")
	}

	// 切り離す役自身が段のパイプを受け継ぐ子を起こさないこと（WMI で起動し、Start-Process を使わない）
	detached, err := os.ReadFile(runDetachedPath)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^[^#\n]*Invoke-CimMethod -ClassName Win32_Process -MethodName Create`).Match(detached) {
		t.Errorf("run_detached.ps1 が WMI の Win32_Process Create で起動していません")
	}
	if regexp.MustCompile(`(?m)^[^#\n]*Start-Process`).Match(detached) {
		t.Errorf("run_detached.ps1 が Start-Process で子を起こしています")
	}

	// 環境の写しと終了コードのファイルの配線。-f の添字まで照合する。どちらかが切れると、
	// 切り離した側は環境変数を失うか、成功しても段が失敗で終わる
	cmdLine := regexp.MustCompile(`(?s)\$inner = \('([^'\n]*)' -f\s+([^)\n]*)\)`).FindStringSubmatch(string(detached))
	if cmdLine == nil {
		t.Fatalf("run_detached.ps1 の $inner の組み立てを読めません")
	}
	fArgs := strings.Split(cmdLine[2], ", ")
	for flag, want := range map[string]string{"-EnvFile": "$envFile", "-CodeFile": "$codeFile"} {
		m := regexp.MustCompile(regexp.QuoteMeta(flag) + ` "\{(\d+)\}"`).FindStringSubmatch(cmdLine[1])
		if m == nil {
			t.Errorf("run_detached.ps1 が %s を渡していません", flag)
			continue
		}
		i, _ := strconv.Atoi(m[1])
		if i >= len(fArgs) || fArgs[i] != want {
			t.Errorf("run_detached.ps1 の %s の値が %s ではありません: 添字 %d・引数 %v", flag, want, i, fArgs)
		}
	}
	supervisor, err := os.ReadFile(runWithDeadlinePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`[string]$EnvFile`, `[string]$CodeFile`, `if ($EnvFile) {`, `if ($CodeFile) {`, `-Destination $CodeFile`} {
		if !strings.Contains(string(supervisor), want) {
			t.Errorf("run_with_deadline.ps1 に %s がありません", want)
		}
	}
	// 終了は Exit-Run の中の 1 か所だけ。ほかで exit すると終了コードのファイルが書かれない
	if n := len(regexp.MustCompile(`(?m)^\s*exit\b`).FindAll(supervisor, -1)); n != 1 {
		t.Errorf("run_with_deadline.ps1 の exit が %d か所あります（Exit-Run の 1 か所だけにする）", n)
	}
}

// .iss が呼ぶ looptrack desktop のフラグが、実際に Main にあることを確かめる（文字列の食い違いを防ぐ）。
func TestInstallerScriptFlagsExist(t *testing.T) {
	iss := readISS(t)
	want := map[string]bool{"--enable-autostart": false, "--install-cli": false, "--unregister": false, "--quit": false, "--after-update": false}
	for _, m := range regexp.MustCompile(`desktop (--[a-z-]+)`).FindAllStringSubmatch(iss, -1) {
		if _, ok := want[m[1]]; !ok {
			t.Errorf("Looptrack.iss が知らないフラグを呼んでいます: %s", m[1])
		}
		want[m[1]] = true
	}
	for f, seen := range want {
		if !seen {
			t.Errorf("Looptrack.iss が %s を呼んでいません", f)
		}
	}
	if runtime.GOOS == "windows" {
		t.Skip("実際に呼ぶのは実機・CI の smoke test（レジストリと CLI の写しを触る）")
	}
	home, data := t.TempDir(), t.TempDir()
	e := env.FromMap(map[string]string{"LOOPTRACK_DATA_DIR": data, "LOOPTRACK_DESKTOP_PORT": "0"})
	for f := range want {
		args := []string{f}
		if f == "--after-update" {
			args = append(args, "--status") // 起動しないで終わる形で、フラグがあることだけを見る
		}
		if code := Main(args, Options{Env: e, Home: home, Alert: (&recorder{}).alert}); code == 2 {
			t.Errorf("looptrack desktop %s が引数の誤り（2）で終わりました", f)
		}
	}
}

// インストーラの置き場（%LOCALAPPDATA%\Programs\Looptrack Desktop）が、CLI の置き場と衝突しないことを確かめる。
// Windows のパスは大小を区別しないので、Looptrack と looptrack は同じフォルダになる（利用者の判断でそう扱う）。
func TestInstallerDirDiffersFromCLIDir(t *testing.T) {
	iss := readISS(t)
	m := regexp.MustCompile(`(?m)^DefaultDirName=\{localappdata\}\\(.+?)\s*$`).FindStringSubmatch(iss)
	if m == nil {
		t.Fatal("DefaultDirName を読めません")
	}
	appDir := filepath.Join(`C:\Local`, filepath.FromSlash(strings.ReplaceAll(m[1], `\`, "/")))
	c := CLIInstall{GOOS: "windows", LocalAppData: `C:\Local`}
	if strings.EqualFold(filepath.Clean(appDir), filepath.Clean(c.Dir())) {
		t.Errorf("インストール先 %s が CLI の置き場 %s と（大小の違いだけで）同じです", appDir, c.Dir())
	}
}
