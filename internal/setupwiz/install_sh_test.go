package setupwiz

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
)

// deploy/install.sh が最後に出す MCP の接続設定を、looptrack setup の案内（MCPConfigs）と突き合わせる。
//
// install.sh は sh なので MCPConfigs を呼べず、同じ文字列を持っている。以前はそれが古く、
// X-Looptrack-Project を落としていて、直前に setup が出した案内と食い違っていた。
// install.sh 側はシェル変数 $URL（公開 URL + 接頭辞）と $proj（プロジェクトの slug）をそのまま書いているので、
// MCPConfigs にその 2 つを文字列として渡すと、install.sh に書いてあるとおりの行が出る。
const installSh = "../../deploy/install.sh"

func TestInstallShMCPConfigMatchesSetup(t *testing.T) {
	b, err := os.ReadFile(installSh)
	if err != nil {
		t.Fatal(err)
	}
	script := string(b)

	var want strings.Builder
	for _, c := range MCPConfigs(i18n.JA, "$URL", "$proj") {
		want.WriteString(i18n.T(i18n.JA, "setupwiz.done.mcp_item", "client", c.Client, "where", c.Where) + "\n")
		for _, l := range strings.Split(c.Text, "\n") {
			want.WriteString("    " + l + "\n")
		}
		if c.Note != "" {
			want.WriteString(i18n.T(i18n.JA, "setupwiz.done.mcp_note", "note", c.Note) + "\n")
		}
	}
	if !strings.Contains(script, want.String()) {
		t.Errorf("deploy/install.sh の mcp_config が looptrack setup の案内と違います。\n"+
			"install.sh の mcp_config のヒアドキュメントを次のとおりにしてください:\n%s", want.String())
	}
}

// installShFunc は deploy/install.sh から「name() {」の行から次の「}」だけの行までを取り出す。
func installShFunc(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(installSh)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	in := false
	for _, l := range strings.Split(string(b), "\n") {
		if !in && l == name+"() {" {
			in = true
		}
		if in {
			out = append(out, l)
			if l == "}" {
				return strings.Join(out, "\n") + "\n"
			}
		}
	}
	t.Fatalf("deploy/install.sh に %s() が見つかりません（前提が崩れています）", name)
	return ""
}

// install.sh の after_start_checks を、まわりの関数を差し替えて sh で動かす。
// check-proxy はホストに置いた $BIN で呼ぶこと（compose のコンテナは CA 証明書を持たず、https の公開 URL に TLS でつながらない）、
// features は app_run（DB を読む）で呼ぶこと、LOCAL_MODE=1 で check-proxy を飛ばすこと、--no-start で何もしないことを見る。
func TestInstallShAfterStartChecks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("install.sh は Linux のサーバで動く sh のスクリプト")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh がありません")
	}
	fn := installShFunc(t, "after_start_checks")
	if !strings.Contains(fn, `"$BIN" check-proxy --url "$URL"`) {
		t.Errorf("after_start_checks が check-proxy を \"$BIN\" check-proxy --url \"$URL\" で呼んでいません:\n%s", fn)
	}
	bin := filepath.Join(t.TempDir(), "looptrack")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho \"BIN $*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(t *testing.T, mode, noStart, local string) string {
		t.Helper()
		script := `step() { echo "step $*"; }
warn() { echo "warn $*"; }
app_run() { echo "app_run $*"; }
` + fn + `after_start_checks "$1"
`
		cmd := exec.Command(sh, "-c", script, "sh", mode)
		cmd.Env = append(os.Environ(), "BIN="+bin, "URL=https://example.invalid/looptrack", "NO_START="+noStart, "LOCAL_MODE="+local)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("after_start_checks が失敗しました: %v\n%s", err, out)
		}
		return string(out)
	}
	proxy := "BIN check-proxy --url https://example.invalid/looptrack"

	t.Run("install", func(t *testing.T) {
		out := run(t, "install", "0", "")
		if !strings.Contains(out, proxy) {
			t.Errorf("check-proxy を $BIN で呼んでいません:\n%s", out)
		}
		if strings.Contains(out, "app_run check-proxy") {
			t.Errorf("check-proxy を app_run で呼んでいます:\n%s", out)
		}
		if strings.Contains(out, "features") {
			t.Errorf("install で features を呼んでいます:\n%s", out)
		}
	})
	t.Run("upgrade", func(t *testing.T) {
		out := run(t, "upgrade", "0", "")
		if !strings.Contains(out, proxy) || !strings.Contains(out, "app_run features") {
			t.Errorf("upgrade で check-proxy（$BIN）と features（app_run）の両方を呼んでいません:\n%s", out)
		}
	})
	t.Run("LOCAL_MODE=1", func(t *testing.T) {
		out := run(t, "upgrade", "0", "1")
		if strings.Contains(out, "check-proxy") {
			t.Errorf("LOCAL_MODE=1 で check-proxy を呼んでいます:\n%s", out)
		}
		if !strings.Contains(out, "app_run features") {
			t.Errorf("LOCAL_MODE=1 の upgrade で features を呼んでいません（対照）:\n%s", out)
		}
	})
	t.Run("--no-start", func(t *testing.T) {
		if out := run(t, "upgrade", "1", ""); strings.TrimSpace(out) != "" {
			t.Errorf("--no-start で何かを動かしています:\n%s", out)
		}
	})
}
