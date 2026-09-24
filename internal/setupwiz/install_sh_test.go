package setupwiz

import (
	"os"
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
