package setupwiz

import (
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/howashoji/looptrack/internal/i18n"
)

// compose.yaml と Dockerfile の注釈も、setup を動かした人の言語で書く（英語を選んだら日本語が混ざらない）。
func TestComposeAndDockerfileInEnglish(t *testing.T) {
	now := time.Date(2026, 9, 25, 7, 0, 0, 0, time.UTC)
	hasJA := func(s string) bool {
		for _, r := range s {
			if unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Han, r) {
				return true
			}
		}
		return false
	}
	for _, store := range []string{StoreSQLite, StoreMySQL} {
		p := &Plan{Port: 9000, PublicURL: "https://x.example", BasePath: "/looptrack", Store: store}
		en := renderCompose(p, now, i18n.EN)
		if hasJA(en) || !strings.Contains(en, "Written by looptrack setup") || !strings.Contains(en, `"127.0.0.1:9000:9000"`) {
			t.Errorf("英語の compose.yaml（%s）:\n%s", store, en)
		}
		// 対照: 日本語を選べば日本語の注釈になる（上の検査が何も見ていないのではないことを確かめる）
		if ja := renderCompose(p, now, i18n.JA); !hasJA(ja) {
			t.Fatalf("日本語の compose.yaml に日本語が無い（検査の前提が崩れています）:\n%s", ja)
		}
	}
	if en := renderDockerfile(now, i18n.EN); hasJA(en) || !strings.Contains(en, "FROM scratch") || !strings.Contains(en, `CMD ["serve"]`) {
		t.Errorf("英語の Dockerfile:\n%s", en)
	}
}
