package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
)

// TestEscapeCommands は差し戻しの文の CLI・逃げ道の呼び方。
// 導入の形に関わらず looptrack の呼び方で案内する。
func TestEscapeCommands(t *testing.T) {
	touch := func(root, rel string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("x"), 0o644)
	}
	for _, c := range []struct {
		name  string
		files []string
	}{
		{"init を通していない", nil},
		{"init で入れた導入", []string{".claude/.looptrack-kit.json"}},
	} {
		root := t.TempDir()
		for _, f := range c.files {
			touch(root, f)
		}
		cli, ack := escapeCommands(root)
		if cli != "looptrack issue" || ack != "looptrack issue-freshness" {
			t.Errorf("%s: %q %q", c.name, cli, ack)
		}
		// 言語を固定する（既定は英語なので、指定が無いと実行する機械の LANG で文面が変わる）
		msg := staleMessage(i18n.JA, []string{"編集"}, []staleIssue{{id: "TST-0001", status: "Todo", title: "x"}}, cli, ack)
		if !strings.Contains(msg, "   "+ack+" ack <ID>\n") || !strings.Contains(msg, "      "+cli+" comment <ID>") {
			t.Errorf("%s: 文面に反映されない:\n%s", c.name, msg)
		}
	}
}
