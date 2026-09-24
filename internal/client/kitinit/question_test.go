package kitinit

import (
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
)

// TestLoopQuestion は対話のときだけ loop を問い、答えを .looptrack-kit.json に残すことを確かめる。
func TestLoopQuestion(t *testing.T) {
	for _, c := range []struct {
		stdin, want string
		installed   bool
	}{
		{"y\n", `"installed": true`, true},
		{"はい\n", `"installed": true`, true},
		{"n\n", `"declined_at"`, false},
		{"", `"installed": false`, false}, // EOF: 入れない・辞退も記録しない
	} {
		stubs(t, true)
		interactive = func(io.Reader) bool { return true }
		root := newRoot(t)
		r := runInit(t, root, c.stdin, "--project", "demo")
		files := map[string]string{}
		for n, b := range embedded() {
			if strings.HasPrefix(n, "kit/loop/") {
				files[n] = b
			}
		}
		h, ru, s := loopCounts(files)
		kj := read(t, filepath.Join(root, "ws", ".claude", ".looptrack-kit.json"))
		if r.code != 0 || !strings.Contains(r.stdout, loopQuestion(i18n.JA, h, ru, s)) || !strings.Contains(kj, c.want) ||
			lexists(filepath.Join(root, "ws", ".claude", "rules", "looptrack-loop")) != c.installed {
			t.Errorf("%q: %d\n%s\n%s", c.stdin, r.code, r.stdout, kj)
		}
		if c.stdin == "" && strings.Contains(kj, "declined_at") {
			t.Errorf("EOF で辞退を記録した")
		}
	}
}
