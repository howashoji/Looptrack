package kitinit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
)

// TestVerifyRulesLang は、rules に英訳（en/）があるときも verify が通ることを確かめる。
//
// hook（session-start-rules）は EN の環境なら en/ の訳を注入するのに、verify が正本（日本語）の見出しだけを
// 探していたため、「英訳がある・環境が EN」のときだけ verify が「注入されていない」と誤判定していた。
// 言語の判定は hook と同じ（cleanEnv が通す LC_ALL / LC_MESSAGES / LANG。LOOPTRACK_* は cleanEnv が落とす）。
func TestVerifyRulesLang(t *testing.T) {
	const ja = "# 規律\n\n## 要点\n<!-- looptrack:inject session -->\n\n- 日本語の本文\n"
	const en = "# A discipline\n\n## Key points\n<!-- looptrack:inject session -->\n\n- The English body\n"
	for _, tc := range []struct{ name, lang, heading string }{
		{"EN", "en_US.UTF-8", "Key points"},
		{"JA", "ja_JP.UTF-8", "要点"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if r, err := filepath.EvalSymlinks(root); err == nil {
				root = r
			}
			dir := filepath.Join(root, filepath.FromSlash(loopRulesDir))
			if err := os.MkdirAll(filepath.Join(dir, "en"), 0o755); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(dir, "x.md"), ja)
			write(t, filepath.Join(dir, "en", "x.md"), en)
			t.Setenv("LC_ALL", "")
			t.Setenv("LC_MESSAGES", "")
			t.Setenv("LANG", tc.lang)
			in := &installer{target: root, plan: &Plan{Root: root, Lang: i18n.JA}}
			r := &verifyReport{lang: i18n.JA}
			in.verifyRules(context.Background(), "claude-code", r)
			if len(r.fails) > 0 {
				t.Fatalf("%s（見出し %q）で verify が失敗: %s", tc.lang, tc.heading, strings.Join(r.fails, " / "))
			}
			if r.rules != 1 {
				t.Errorf("注入の節の数: %d（want 1）", r.rules)
			}
		})
	}
}
