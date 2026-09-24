package kitinit

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// legacyEntryRe は導入先に置いてはいけない、別の処理系が要る入口の拡張子
// （looptrack は実行ファイル 1 つで動く）。
var legacyEntryRe = regexp.MustCompile(`\.py[a-z]*\b`)

// TestNoLegacyEntrypointsPlaced は、
//   - 別の処理系が要る入口（拡張子 .py…）が 1 つも置かれない
//   - 置いたファイル（配線・permissions・案内節・skill・rules）にその呼び出しが残らない
//
// ことを AI 4 種 × loop の有無で確かめる。
func TestNoLegacyEntrypointsPlaced(t *testing.T) {
	for _, agent := range goldenAgents {
		if agent == "other" {
			continue // --agent other は案内を出すだけでファイルを書かない
		}
		for _, loop := range []bool{false, true} {
			name := agent
			if loop {
				name += "/loop"
			}
			t.Run(name, func(t *testing.T) {
				stubs(t, true)
				root := newRoot(t)
				ws := filepath.Join(root, "ws")
				args := []string{"--project", "demo", "--agent", agent}
				if loop {
					args = append(args, "--loop")
				} else {
					args = append(args, "--no-loop")
				}
				if r := runInit(t, root, "", args...); r.code != 0 {
					t.Fatalf("init %v: %d %s", args, r.code, r.stderr)
				}
				for rel, body := range tree(t, ws) {
					if legacyEntryRe.MatchString(rel) {
						t.Errorf("%s が置かれています（looptrack は実行ファイル 1 つで動きます）", rel)
						continue
					}
					for i, l := range strings.Split(body, "\n") {
						if !legacyEntryRe.MatchString(l) {
							continue
						}
						t.Errorf("%s:%d に以前の入口の記述が残っています: %s", rel, i+1, strings.TrimSpace(l))
					}
				}
			})
		}
	}
}
