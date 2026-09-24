package server

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/blake2b"
)

// 配布ディレクトリの looptrack（GET /api/v1/dist の binaries・本体）・版による導入状態の判定・setup の Go 版の手順。

// writeDist は配布ディレクトリに looptrack の偽物を置き、SHA256SUMS を書く（dist.sh sums と同じ形）。
func writeDist(t *testing.T, dir string, files map[string]string) map[string]string {
	t.Helper()
	sums := map[string]string{}
	var lines []string
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256([]byte(body))
		sums[name] = hex.EncodeToString(h[:])
		lines = append(lines, sums[name]+"  "+name)
	}
	if err := os.WriteFile(filepath.Join(dir, sumsName), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return sums
}

type distListing struct {
	Files     []distFileJSON   `json:"files"`
	Binaries  []distBinaryJSON `json:"binaries"`
	SumsURL   string           `json:"sha256sums_url"`
	MinisigRL string           `json:"sha256sums_minisig_url"`
	NoticeURL string           `json:"notice_url"`
}

func TestDistBinaries(t *testing.T) {
	e, _, ed := newAPIEnv(t)

	// 配布ディレクトリが無い: binaries は空の一覧（files は従来どおり）
	var got distListing
	ed.json(200, "GET", "/dist", nil, &got)
	if got.Binaries == nil || len(got.Binaries) != 0 || len(got.Files) == 0 || got.SumsURL != "" {
		t.Fatalf("配布ディレクトリなし: %+v", got)
	}

	dir := t.TempDir()
	e.s.cfg.DistDir = dir
	sums := writeDist(t, dir, map[string]string{
		"looptrack_v0.9.0_darwin_arm64":      "old-darwin-arm64",
		"looptrack_v1.0.0_darwin_arm64":      "darwin-arm64",
		"looptrack_v1.0.0_linux_amd64":       "linux-amd64",
		"looptrack_v1.0.0_windows_amd64.exe": "windows-amd64",
		"looptrack_dev_linux_arm64":          "dev-build",     // 比べられない版は出さない
		"looptrack_v1.0.0_windows_arm64":     "no-exe-suffix", // windows は .exe だけ
		"imserver_v1.0.0_linux_amd64":        "server",        // 別のコマンド（統合前のサーバの旧名）は出さない
	})
	// SHA256SUMS に無い（置き換えの途中）ものは出さない
	if err := os.WriteFile(filepath.Join(dir, "looptrack_v1.0.0_linux_arm64"), []byte("unlisted"), 0o755); err != nil {
		t.Fatal(err)
	}
	got = distListing{}
	ed.json(200, "GET", "/dist", nil, &got)
	var names []string
	for _, b := range got.Binaries {
		names = append(names, fmt.Sprintf("%s/%s %s", b.OS, b.Arch, b.Version))
		if b.SHA256 != sums[b.Name] || b.URL != e.srv.URL+"/im/api/v1/dist/bin/"+b.Name || b.Size != int64(len(b.Name)) && b.Size <= 0 {
			t.Errorf("binaries の項目: %+v", b)
		}
	}
	if strings.Join(names, ",") != "darwin/arm64 v1.0.0,linux/amd64 v1.0.0,windows/amd64 v1.0.0" {
		t.Errorf("binaries: %v", names)
	}
	if got.SumsURL != e.srv.URL+"/im/api/v1/dist/bin/SHA256SUMS" || got.MinisigRL != "" || got.NoticeURL != "" {
		t.Errorf("SHA256SUMS の URL: %+v", got)
	}
	// NOTICE（第三者のライセンス文）を置くと notice_url が出て、本文を text/plain で返す
	if err := os.WriteFile(filepath.Join(dir, noticeName), []byte("third-party notices"), 0o644); err != nil {
		t.Fatal(err)
	}
	got = distListing{}
	ed.json(200, "GET", "/dist", nil, &got)
	if got.NoticeURL != e.srv.URL+"/im/api/v1/dist/bin/NOTICE" {
		t.Errorf("notice_url: %+v", got)
	}
	if code, h, body := ed.do("GET", "/dist/bin/NOTICE", nil); code != 200 || string(body) != "third-party notices" || !strings.HasPrefix(h.Get("Content-Type"), "text/plain") {
		t.Errorf("NOTICE: %d %q %v", code, body, h)
	}

	// 本体（X-Looptrack-SHA256）。一覧に無いもの・他のファイル・パスの細工は 404
	code, h, body := ed.do("GET", "/dist/bin/looptrack_v1.0.0_darwin_arm64", nil)
	if code != 200 || string(body) != "darwin-arm64" || h.Get("X-Looptrack-SHA256") != sums["looptrack_v1.0.0_darwin_arm64"] || h.Get("Content-Type") != "application/octet-stream" {
		t.Errorf("本体: %d %q %v", code, body, h)
	}
	if code, _, body := ed.do("GET", "/dist/bin/SHA256SUMS", nil); code != 200 || !strings.Contains(string(body), "looptrack_v1.0.0_linux_amd64") {
		t.Errorf("SHA256SUMS: %d", code)
	}
	for _, name := range []string{"looptrack_v0.9.0_darwin_arm64", "looptrack_dev_linux_arm64", "imserver_v1.0.0_linux_amd64",
		"looptrack_v1.0.0_linux_arm64", "..%2Fdist_bin_test.go", "SHA256SUMS.minisig"} {
		if code, _, _ := ed.do("GET", "/dist/bin/"+name, nil); code != 404 {
			t.Errorf("%s: %d（404 のはず）", name, code)
		}
	}
	// 認証が要る（/api/v1/dist は公開しない）
	if res, _ := e.get(e.client(), "/im/api/v1/dist/bin/looptrack_v1.0.0_darwin_arm64"); res.StatusCode != 401 {
		t.Errorf("認証なし: %d", res.StatusCode)
	}

	// 券の一覧は券の URL で出す（トークンなしで取れる）
	m := e.mcpAsClient(ed.token, map[string]string{"X-Looptrack-Project": "req"}, "claude-code", "1", "2025-06-18")
	_, data := m.call("setup", map[string]any{"loop": "no"}, false)
	out := setupOf(t, data)
	res, lb := e.get(e.client(), strings.TrimPrefix(out.DistURL, e.srv.URL)+"/")
	var tl distListing
	if res.StatusCode != 200 || json.Unmarshal([]byte(lb), &tl) != nil || len(tl.Binaries) != 3 || !strings.HasPrefix(tl.Binaries[0].URL, out.DistURL+"/bin/") {
		t.Fatalf("券の一覧: %d %s", res.StatusCode, lb)
	}
	res, bb := e.get(e.client(), strings.TrimPrefix(tl.Binaries[1].URL, e.srv.URL))
	if res.StatusCode != 200 || bb != "linux-amd64" {
		t.Errorf("券の URL で本体: %d %q", res.StatusCode, bb)
	}

	// 版の比較: (os, arch) ごとの最新
	if v := e.s.latestClient("darwin", "arm64"); v != "v1.0.0" {
		t.Errorf("latestClient: %q", v)
	}
	if v := e.s.latestClient("linux", "arm64"); v != "" {
		t.Errorf("配っていない対象の latestClient: %q", v)
	}
}

// TestInstallGoClient は looptrack の通知（client {version, os, arch}）を版で判定し、client の無い通知（撤去した以前の CLI）は
// looptrack への置き換えを求めることを確かめる。
func TestInstallGoClient(t *testing.T) {
	e, _, ed := newAPIEnv(t)
	m := e.mcpAsClient(ed.token, map[string]string{"X-Looptrack-Project": "req"}, "claude-code", "2.1.0", "2025-06-18")
	post := func(body map[string]any) installStateJSON {
		var st installStateJSON
		ed.json(200, "POST", "/projects/req/install", body, &st)
		return st
	}
	goBody := func(version string, files map[string]string) map[string]any {
		return map[string]any{"agent": "claude-code", "trigger": "hook", "source": "", "files": files, "host": "mac", "workspace": "ws",
			"client": map[string]any{"version": version, "os": "darwin", "arch": "arm64"}}
	}

	// 入口の無い導入（files が空）も受ける（以前は 400 にしていた）。配布ディレクトリが無ければ版は比べない
	st := post(goBody("v1.0.0", map[string]string{}))
	if st.State != "current" || st.ClientVersion != "v1.0.0" || st.ClientOS != "darwin" || st.LatestClient != "" ||
		!strings.Contains(st.Message, "looptrack v1.0.0") {
		t.Fatalf("Go 版・配布なし: %+v", st)
	}
	// 以前の配布物のハッシュを送っても、版で判定する（ハッシュでは判定しない）
	if st := post(goBody("v1.0.0", map[string]string{"kit.tar.gz": strings.Repeat("a", 64)})); st.State != "current" {
		t.Errorf("Go 版・配布物のハッシュ: %+v", st)
	}
	var row struct{ os, arch, ver string }
	e.db.QueryRow("SELECT client_os, client_arch, client_version FROM agent_installs WHERE agent = 'claude-code'").Scan(&row.os, &row.arch, &row.ver)
	if row.os != "darwin" || row.arch != "arm64" || row.ver != "v1.0.0" {
		t.Errorf("保存: %+v", row)
	}

	// 配布中の最新より古い → 【配布スクリプトの更新】と self-update
	dir := t.TempDir()
	e.s.cfg.DistDir = dir
	writeDist(t, dir, map[string]string{"looptrack_v1.1.0_darwin_arm64": "new", "looptrack_v2.0.0_linux_amd64": "other-target"})
	st = post(goBody("v1.0.0", map[string]string{}))
	if st.State != "stale" || st.LatestClient != "v1.1.0" || strings.Join(st.StaleFiles, ",") != "looptrack" || st.UpdateCommand != "looptrack self-update" ||
		!strings.Contains(st.Message, "【配布スクリプトの更新】Claude Code の作業環境の looptrack（v1.0.0・darwin/arm64）") || !strings.Contains(st.Message, "配布中の最新 v1.1.0 より古い") {
		t.Errorf("Go 版・古い: %+v", st)
	}
	m.call("list_issues", map[string]any{}, false)
	if !strings.Contains(m.notice, "【配布スクリプトの更新】") || !strings.Contains(m.notice, "looptrack self-update") {
		t.Errorf("MCP の指示: %q", m.notice)
	}
	// setup は Go 版の導入が古ければ self-update を示す。loop が未選択なので 1 回目は問いだけ
	_, data := m.call("setup", map[string]any{}, false)
	if out := setupOf(t, data); out.Ask != "loop" || len(out.Steps) != 1 || out.Steps[0].Command != "" || strings.Contains(out.Text, "self-update") {
		t.Errorf("setup（Go 版の導入が古い・1 回目）: %+v\n%s", out.Steps, out.Text)
	}
	_, data = m.call("setup", map[string]any{"loop": "yes"}, false)
	if out := setupOf(t, data); len(out.Steps) != 3 || out.Steps[0].Command != "looptrack self-update" || strings.Contains(out.Text, "curl") ||
		!strings.HasPrefix(out.Steps[1].Command, "looptrack issue init --project req --agent claude-code") || !strings.HasSuffix(out.Steps[1].Command, " --loop") {
		t.Errorf("setup（Go 版の導入が古い・loop=yes）: %+v", out.Steps)
	}
	// 最新と同じ・比べられない版（dev）は current
	for _, v := range []string{"v1.1.0", "v1.2.0", "dev"} {
		if st := post(goBody(v, nil)); st.State != "current" {
			t.Errorf("%s: %+v", v, st)
		}
	}
	// 最低の対応版より古い
	e.s.cfg.ClientMinVersion = "v1.1.0"
	if st := post(goBody("v1.0.5", nil)); st.State != "stale" || !strings.Contains(st.Message, "最低の版 v1.1.0 より古い") || st.MinClient != "v1.1.0" {
		t.Errorf("最低の版: %+v", st)
	}
	e.s.cfg.ClientMinVersion = ""
	// kit が古い: init の再実行（版も古ければ self-update の後に）
	body := goBody("v1.0.0", nil)
	body["core"] = map[string]any{"bundle_sha256": strings.Repeat("0", 64)}
	st = post(body)
	if st.State != "stale" || st.UpdateCommand != "looptrack self-update の後に looptrack issue init --project req --agent claude-code を再実行する" {
		t.Errorf("版と kit が古い: %+v", st)
	}
	// 手順に載るのは、そのまま実行できるコマンド（UpdateCommand は利用者の言語の案内文なので、そこからは作らない）
	_, data = m.call("setup", map[string]any{"loop": "yes"}, false)
	if out := setupOf(t, data); len(out.Steps) != 3 ||
		out.Steps[0].Command != "looptrack self-update && looptrack issue init --project req --agent claude-code" {
		t.Errorf("手順のコマンド: %+v", out.Steps)
	}

	// client の無い通知（撤去した以前の CLI）: files は 1 件以上必須。フックから届いていても stale で、
	// looptrack の取得 + init への置き換えを求める
	ed.fail(400, "POST", "/projects/req/install", map[string]any{"agent": "codex", "trigger": "hook", "files": map[string]string{}})
	legacyFiles := map[string]string{"kit.tar.gz": strings.Repeat("b", 64)}
	if st := post(map[string]any{"agent": "claude-code", "trigger": "hook", "source": "server", "files": legacyFiles}); st.State != "stale" || st.ClientOS != "" ||
		strings.Join(st.StaleFiles, ",") != "looptrack" || !strings.Contains(st.Message, "1.0.0 より前の CLI で導入されています") ||
		st.UpdateCommand != "setup ツールの手順で looptrack を取得し、looptrack issue init --project req --agent claude-code を実行する" {
		t.Errorf("以前の CLI の通知: %+v", st)
	}
	// setup は取得 + init（hook を looptrack に置き換える）を示す
	_, data = m.call("setup", map[string]any{"loop": "no"}, false)
	if out := setupOf(t, data); out.Install.State != "stale" || len(out.Steps) == 0 || out.Steps[0].Who != "human" ||
		!strings.Contains(out.Steps[0].Title, "looptrack を用意して導入する") || !strings.Contains(out.Steps[0].Title, "--no-loop") {
		// 配布ディレクトリ（darwin/arm64 と linux/amd64 だけ）に、この接続の OS 向けが無いときは利用者の手順になる
		if len(out.Steps) == 0 || !strings.Contains(out.Steps[0].Command, "issue init --project req") || !strings.HasSuffix(out.Steps[0].Command, " --no-loop") {
			t.Errorf("setup（以前の CLI の導入）: %+v", out.Steps)
		}
	}
	// 入力の検査
	for _, bad := range []map[string]any{
		{"agent": "codex", "trigger": "hook", "client": map[string]any{"version": "", "os": "linux", "arch": "amd64"}},
		{"agent": "codex", "trigger": "hook", "client": map[string]any{"version": "v1", "os": "Linux!", "arch": "amd64"}},
		{"agent": "codex", "trigger": "hook", "client": map[string]any{"version": strings.Repeat("9", 65), "os": "linux", "arch": "amd64"}},
		{"agent": "codex", "trigger": "hook", "client": map[string]any{"version": "v1", "os": "linux", "arch": "amd64", "extra": 1}},
	} {
		ed.fail(400, "POST", "/projects/req/install", bad)
	}
}

// TestSetupGo は setup ツールの手順（cli の検査・OS ごとの取得コマンド・配布が無いとき）を確かめる。
func TestSetupGo(t *testing.T) {
	e, _, ed := newAPIEnv(t)
	m := e.mcpAsClient(ed.token, map[string]string{"X-Looptrack-Project": "req"}, "claude-code", "1", "2025-06-18")
	steps := func(out setupOut) string {
		var b strings.Builder
		for _, s := range out.Steps {
			b.WriteString(s.Title + "\n" + s.Command + "\n" + s.CommandWindows + "\n")
		}
		return b.String()
	}

	// 配布ディレクトリが無い: 以前の CLI の手順には戻さず、looptrack を配っていない旨と、用意した後の init を利用者の手順で示す。
	// loop が未選択なので、手順は利用者の答え（loop）を付けた呼び出しで見る
	text, data := m.call("setup", map[string]any{"loop": "yes"}, false)
	if out := setupOf(t, data); data["cli"] != "looptrack" || !strings.Contains(text, "配布ディレクトリに looptrack がありません。取得の手順を示せません") ||
		out.Steps[0].Who != "human" || out.Steps[0].Command != "" ||
		!strings.Contains(out.Steps[0].Title, "`looptrack issue init --project req --url "+e.srv.URL+"/im --agent claude-code --loop`") ||
		strings.Contains(steps(out), "curl") {
		t.Errorf("配布なし: %v %+v\n%s", data["cli"], out.Steps, text)
	}
	if out := setupOf(t, data); !strings.HasPrefix(out.Steps[1].Command, "looptrack issue login --browser") {
		t.Errorf("配布なしの login は PATH の looptrack: %+v", out.Steps[1])
	}
	// cli は looptrack だけ
	if text, _ := m.call("setup", map[string]any{"cli": "rust"}, true); !strings.Contains(text, "cli は省略するか looptrack を指定してください") {
		t.Errorf("cli: rust: %s", text)
	}
	m.call("setup", map[string]any{"cli": "looptrack", "os": "plan9"}, true)

	dir := t.TempDir()
	e.s.cfg.DistDir = dir
	sums := writeDist(t, dir, map[string]string{
		"looptrack_v1.0.0_darwin_arm64": "da", "looptrack_v1.0.0_darwin_amd64": "dx", "looptrack_v1.0.0_linux_amd64": "lx",
		"looptrack_v1.0.0_linux_arm64": "la", "looptrack_v1.0.0_windows_amd64.exe": "wx",
	})

	// macOS: curl + shasum、~/.local/bin、loop の問いの答えごとの取得 + init
	text, data = m.call("setup", map[string]any{"cli": "looptrack", "os": "darwin", "loop": "yes"}, false)
	out := setupOf(t, data)
	all := steps(out)
	if data["cli"] != "looptrack" || data["os"] != "darwin" || out.Steps[0].Command == "" {
		t.Fatalf("macOS: %v %+v", data["cli"], out.Steps[0])
	}
	for _, want := range []string{`D="$HOME/.local/bin"`, "Darwin/arm64) U='" + out.DistURL + "/bin/looptrack_v1.0.0_darwin_arm64'; S='" + sums["looptrack_v1.0.0_darwin_arm64"],
		`sum() { shasum -a 256 "$@"; };;`, `| sum -c -`, `curl -fsSL "$U"`, `"$HOME/.local/bin/looptrack" issue init --project req --url ` + e.srv.URL + "/im --agent claude-code --source server --dist '" + out.DistURL + "' --loop",
		`"$HOME/.local/bin/looptrack" issue login --browser`, "Claude Code を再起動", "looptrack doctor"} {
		if !strings.Contains(all, want) {
			t.Errorf("macOS の手順に %q が無い:\n%s", want, all)
		}
	}
	for _, bad := range []string{"Linux/", "Invoke-WebRequest"} {
		if strings.Contains(all, bad) {
			t.Errorf("macOS の手順に %q がある", bad)
		}
	}
	// 本文には取得コマンドが確かめる実行ファイルの SHA-256 だけ（一覧は出さない）。全件は構造化データの binaries にある
	if !strings.Contains(text, sums["looptrack_v1.0.0_darwin_arm64"]) || strings.Contains(text, sums["looptrack_v1.0.0_windows_amd64.exe"]) {
		t.Errorf("本文の実行ファイルの SHA-256:\n%s", text)
	}
	if b, _ := json.Marshal(data["binaries"]); !strings.Contains(string(b), sums["looptrack_v1.0.0_windows_amd64.exe"]) {
		t.Errorf("構造化データの binaries: %s", b)
	}

	// Windows: Invoke-WebRequest + Get-FileHash、%LOCALAPPDATA%\Programs\looptrack。ARM64 は amd64 を使う（arm64 を配っていない）
	_, data = m.call("setup", map[string]any{"cli": "looptrack", "os": "windows", "loop": "yes"}, false)
	out = setupOf(t, data)
	all = steps(out)
	for _, want := range []string{"Invoke-WebRequest -UseBasicParsing -Uri $U", "Get-FileHash -Algorithm SHA256", `Join-Path $env:LOCALAPPDATA 'Programs\looptrack'`,
		"'ARM64' { $U = '" + out.DistURL + "/bin/looptrack_v1.0.0_windows_amd64.exe'", "issue login --browser"} {
		if !strings.Contains(all, want) {
			t.Errorf("Windows の手順に %q が無い:\n%s", want, all)
		}
	}
	if strings.Contains(all, "curl") || strings.Contains(all, "$HOME") {
		t.Errorf("Windows の手順に sh が混ざる:\n%s", all)
	}

	// OS 不明: 両方（command と command_windows）。cli を省略しても looptrack
	_, data = m.call("setup", map[string]any{"agent": "other"}, false)
	b, _ := json.Marshal(data["steps"])
	if data["cli"] != "looptrack" || !strings.Contains(string(b), `"command_windows"`) || !strings.Contains(string(b), "installed --agent other") {
		t.Errorf("OS 不明・other: %v %s", data["cli"], b)
	}

	// 取得コマンドを実際に sh で流す（macOS / Linux。券の URL から取り、SHA-256 を確かめて置く）
	if runtime.GOOS == "windows" {
		return
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl が無い")
	}
	goos := runtime.GOOS
	if goos != "darwin" && goos != "linux" {
		return
	}
	_, data = m.call("setup", map[string]any{"cli": "looptrack", "os": goos, "loop": "no"}, false)
	cmd := setupOf(t, data).Steps[0].Command
	cut := strings.Index(cmd, ` && "$HOME/.local/bin/looptrack" issue init`)
	if cut < 0 {
		t.Fatalf("取得コマンドの形: %s", cmd)
	}
	home := t.TempDir()
	sh := exec.Command("sh", "-c", cmd[:cut])
	sh.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	outb, err := sh.CombinedOutput()
	arch := map[string]string{"arm64": "arm64", "amd64": "amd64"}[runtime.GOARCH]
	want := map[string]string{"darwin/arm64": "da", "darwin/amd64": "dx", "linux/amd64": "lx", "linux/arm64": "la"}[goos+"/"+arch]
	placed, _ := os.ReadFile(filepath.Join(home, ".local", "bin", "looptrack"))
	if err != nil || string(placed) != want {
		t.Errorf("取得コマンド: %v %s（置いたもの %q）", err, outb, placed)
	}
	fi, _ := os.Stat(filepath.Join(home, ".local", "bin", "looptrack"))
	if fi == nil || fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("実行権限: %v", fi)
	}
	// 手順を受け取った後に配布物が差し替わった（サーバは新しいものを配る）: 手順の SHA-256 と合わないので置かない
	os.Remove(filepath.Join(home, ".local", "bin", "looptrack"))
	name := "looptrack_v1.0.0_" + goos + "_" + arch
	time.Sleep(10 * time.Millisecond)
	writeDist(t, dir, map[string]string{name: "tampered"})
	sh = exec.Command("sh", "-c", cmd[:cut])
	sh.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	if outb, err := sh.CombinedOutput(); err == nil {
		t.Errorf("差し替えた配布物を置いた: %s", outb)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "bin", "looptrack")); err == nil {
		t.Error("SHA-256 が合わないのに置いた")
	}
}

// TestGoClientEndToEnd は実物の looptrack で、入口の無い導入の installed（files が空でも通る）→ 古い版に【配布スクリプトの更新】→
// self-update（実行中のファイルを置き換える）→ 更新後の通知で導入済み、を実サーバで確かめる。
func TestGoClientEndToEnd(t *testing.T) {
	testutilDSNOrSkip(t)
	e, _, ed := newAPIEnv(t)
	// SHA256SUMS の署名も通しで確かめるため、使い捨ての minisign の鍵の公開鍵を埋め込んだ looptrack を作る
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keyID := []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88}
	built := looptrackBinWithKey(t, base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), keyID...), pub...)))
	body, err := os.ReadFile(built)
	if err != nil {
		t.Fatal(err)
	}
	// 手元の looptrack を「古い版」として置き、同じ中身を配布ディレクトリに「新しい版」として置く（版は -X でなく名前で決まる）
	binDir := t.TempDir()
	exe := filepath.Join(binDir, "looptrack")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if err := os.WriteFile(exe, body, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	e.s.cfg.DistDir = dir
	name := "looptrack_v9.9.9_" + runtime.GOOS + "_" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	sums := writeDist(t, dir, map[string]string{name: string(body) + "\n// v9.9.9"})

	ws := t.TempDir() // 空の導入先
	home := t.TempDir()
	run := func(args ...string) (string, int) {
		cmd := exec.Command(exe, args...)
		cmd.Dir = ws
		cmd.Env = cliAPIEnv(e.srv.URL+"/im", "req", ed.token, home)
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return string(out), code
	}
	// 手元のビルドの版（dev）は比べられないので導入済み。installed は files が空でも 400 にならない
	// CLI は言語を送るのでサーバの文面は日本語（setup_test.go の cliNotice* の注記と同じ理由）
	if out, code := run("issue", "installed", "--agent", "other"); code != 0 || !strings.Contains(out, "導入済み（その他の AI・looptrack dev・配布物は最新") {
		t.Fatalf("installed（dev）: %d %s", code, out)
	}
	// 比べられる古い版として通知すると【配布スクリプトの更新】（版は通知の client.version。ここでは API で古い版を送る）
	var st installStateJSON
	ed.json(200, "POST", "/projects/req/install", map[string]any{"agent": "other", "trigger": "manual", "files": map[string]string{},
		"client": map[string]any{"version": "v1.0.0", "os": runtime.GOOS, "arch": runtime.GOARCH}}, &st)
	if st.State != "stale" || st.UpdateCommand != "looptrack self-update" || st.LatestClient != "v9.9.9" {
		t.Fatalf("古い版: %+v", st)
	}
	// self-update: --check は示すだけ、dev は --force で置き換える（SHA-256 を確かめて実行中のファイルを差し替える）
	if out, code := run("self-update", "--check"); code != 0 || !strings.Contains(out, "比べられません") {
		t.Errorf("self-update --check（dev）: %d %s", code, out)
	}
	// 公開鍵を持つビルドは、SHA256SUMS の署名が無い配布からは置き換えない
	if out, code := run("self-update", "--force"); code != 1 || !strings.Contains(out, "署名（.minisig）がありません") {
		t.Errorf("self-update --force（署名なし）: %d %s", code, out)
	}
	sumsBody, err := os.ReadFile(filepath.Join(dir, sumsName))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sumsSigName), testMinisign(priv, keyID, sumsBody), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := run("self-update", "--force")
	got, _ := os.ReadFile(exe)
	h := sha256.Sum256(got)
	if code != 0 || hex.EncodeToString(h[:]) != sums[name] || !strings.Contains(out, "dev → v9.9.9") || strings.Contains(out, "署名の確認は未設定") {
		t.Fatalf("self-update --force: %d %s", code, out)
	}
	// 置き換えた後も動く（中身は同じビルド + 末尾のコメント）。doctor は PATH に無いことを注意として出す
	if out, code := run("doctor", "--offline"); code != 0 || !strings.Contains(out, "PATH に looptrack がありません") {
		t.Errorf("doctor: %d %s", code, out)
	}
}

// looptrackBinWithKey は minisign の公開鍵（selfupdate.MinisignPublicKey）を差し替えた looptrack を go build する（dist.sh の RELEASE_MINISIGN_PUBKEY と同じ -X）。
func looptrackBinWithKey(t *testing.T, pub string) string {
	t.Helper()
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go が無いため省略")
	}
	out := filepath.Join(t.TempDir(), "looptrack")
	cmd := exec.Command(gobin, "build", "-ldflags", "-X github.com/howashoji/looptrack/internal/client/selfupdate.MinisignPublicKey="+pub, "-o", out, "./cmd/looptrack")
	cmd.Dir, _ = filepath.Abs(filepath.Join("..", ".."))
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("looptrack を作れません: %v\n%s", err, b)
	}
	return out
}

// testMinisign は minisign -S と同じ形の署名（ED＝BLAKE2b-512 の prehash・trusted comment つき）を作る。
func testMinisign(priv ed25519.PrivateKey, keyID, msg []byte) []byte {
	h := blake2b.Sum512(msg)
	sig := ed25519.Sign(priv, h[:])
	trusted := "timestamp:0\tfile:SHA256SUMS"
	global := ed25519.Sign(priv, append(append([]byte{}, sig...), trusted...))
	return []byte("untrusted comment: signature from minisign secret key\n" +
		base64.StdEncoding.EncodeToString(append(append([]byte("ED"), keyID...), sig...)) + "\n" +
		"trusted comment: " + trusted + "\n" + base64.StdEncoding.EncodeToString(global) + "\n")
}

// TestPosixFetchShells は、setup が返す取得のコマンドが zsh・bash・sh のどれでもそのまま通ることを確かめる。
// macOS の既定は zsh で、zsh は変数の値を単語に分けない。以前はハッシュの確認を変数（C='shasum -a 256'）に入れていたため、
// zsh では「command not found: shasum -a 256」で止まり、導入が途中で終わっていた。
//
// Windows では省略する。取得のコマンドは macOS・Linux 向けで、中の uname -s で配布物の対象を選ぶ。Windows にも
// Git for Windows の sh・bash はあるが、そこでの uname -s は MINGW64_NT… を返すので、コマンドは正しく
// 「配布が無い」と言って終了コード 1 で止まる（Windows 向けの取得は PowerShell の経路が別にある）。
func TestPosixFetchShells(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("取得のコマンドは POSIX 向け（Git Bash の uname は MINGW64_NT… を返す）")
	}
	src := filepath.Join(t.TempDir(), "looptrack-src")
	if err := os.WriteFile(src, []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(b))
	bins := []distBinaryJSON{}
	for _, osArch := range [][2]string{{"darwin", "arm64"}, {"darwin", "amd64"}, {"linux", "amd64"}, {"linux", "arm64"}} {
		bins = append(bins, distBinaryJSON{OS: osArch[0], Arch: osArch[1], URL: "file://" + src, SHA256: sum})
	}
	g := &goSetup{os: "posix", bins: bins}
	for _, sh := range []string{"sh", "bash", "zsh"} {
		shPath, err := exec.LookPath(sh)
		if err != nil {
			t.Logf("%s が無いので省略", sh)
			continue
		}
		home := t.TempDir()
		cmd := exec.Command(shPath, "-c", g.posixFetch("true"))
		cmd.Env = append(os.Environ(), "HOME="+home)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s で取得のコマンドが通らない: %v\n%s", sh, err, out)
		} else if _, err := os.Stat(filepath.Join(home, ".local", "bin", "looptrack")); err != nil {
			t.Errorf("%s: 置かれていない: %v\n%s", sh, err, out)
		}
	}
}
