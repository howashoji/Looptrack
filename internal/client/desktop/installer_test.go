package desktop

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/client/env"
)

// issPath は Windows のインストーラの設定（Inno Setup 7）。
const issPath = "../../../deploy/release/windows/Looptrack.iss"

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
}

// .iss が呼ぶ looptrack desktop のフラグが、実際に Main にあることを確かめる（文字列の食い違いを防ぐ）。
func TestInstallerScriptFlagsExist(t *testing.T) {
	iss := readISS(t)
	want := map[string]bool{"--enable-autostart": false, "--install-cli": false, "--unregister": false, "--quit": false}
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
		if code := Main([]string{f}, Options{Env: e, Home: home, Alert: (&recorder{}).alert}); code == 2 {
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
