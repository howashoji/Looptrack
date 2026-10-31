package loop

import (
	"regexp"

	"github.com/howashoji/looptrack/internal/client/hook/hookcmd"
)

// spaceClass は hookcmd の空白文字の類（テストと正規表現の文字クラスで使う）。
const spaceClass = hookcmd.SpaceClass

// compatRe は \s・\S を Unicode の空白の意味のまま RE2 でコンパイルする（hookcmd.CompatRe）。
func compatRe(pattern string) *regexp.Regexp { return hookcmd.CompatRe(pattern) }

func compatPattern(p string) string { return hookcmd.CompatPattern(p) }
