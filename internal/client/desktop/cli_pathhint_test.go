package desktop

import (
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/setuppath"
)

// TestCLIPathHintUsesSetupRule は、置き場が PATH に無いときの案内（macOS・Linux）が、setup の取得の手順と同じ断片
// （internal/setuppath）で足し方を示し、起動ファイルの規則を別に書いていないことを確かめる。
// 対照: 置き場が PATH にあれば案内を出さない。
func TestCLIPathHintUsesSetupRule(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
		c := CLIInstall{GOOS: "darwin", Home: "/Users/someone", PathEnv: "/usr/bin:/bin"}
		hint := c.pathHint(lang)
		if !strings.Contains(hint, setuppath.PosixCommand(lang)) || strings.Contains(hint, "~/.zshrc") {
			t.Errorf("%s: 案内が setup の断片を使っていない（または起動ファイルを別に書いている）: %s", lang, hint)
		}
		c.PathEnv = "/usr/bin:/Users/someone/.local/bin"
		if hint := c.pathHint(lang); hint != "" {
			t.Errorf("%s: 置き場が PATH にあるのに案内を出した: %s", lang, hint)
		}
	}
}
