package pathhint

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/howashoji/looptrack/internal/client/env"
)

func TestCollect(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real", "proj")
	if err := os.MkdirAll(filepath.Join(real, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(real, "internal", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "home")
	if err := os.MkdirAll(filepath.Join(home, ".claude", "projects"), 0o755); err != nil { // 実在する会話記録の置き場
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	symlinked := runtime.GOOS != "windows" && os.Symlink(filepath.Join(base, "real"), link) == nil
	dirs := []string{filepath.Join(real, "internal", "x"), filepath.Join(base, "not-git")}
	if symlinked {
		dirs = append(dirs, filepath.Join(link, "proj", "internal"))
	}
	scratch := filepath.Join(base, "scratch")
	e := env.FromMap(map[string]string{"HOME": home, "LOOPTRACK_MACHINE_LOCAL": scratch + string(os.PathListSeparator) + "relative/dir"})
	h := Collect(e, dirs, []string{"/tmp/claude-501/sp", ""})

	realResolved, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{real, realResolved} {
		if !slices.Contains(h.Roots, want) {
			t.Errorf("roots に %q が無い: %v", want, h.Roots)
		}
	}
	if symlinked && !slices.Contains(h.Roots, filepath.Join(link, "proj")) {
		t.Errorf("roots にシンボリックリンクの形（literal）が無い: %v", h.Roots)
	}
	for _, p := range h.Roots {
		if p == filepath.Join(base, "not-git") || p == "" {
			t.Errorf("git でないディレクトリが roots に入った: %v", h.Roots)
		}
	}
	if h.Home != home {
		t.Errorf("home = %q, want %q", h.Home, home)
	}
	for _, want := range []string{"/tmp/claude-501/sp", scratch, filepath.Join(home, ".claude", "projects")} {
		if !slices.Contains(h.MachineLocal, want) {
			t.Errorf("machine_local に %q が無い: %v", want, h.MachineLocal)
		}
	}
	for _, p := range h.MachineLocal {
		if p == "relative/dir" || p == "" || p == filepath.Join(home, ".codex", "sessions") {
			t.Errorf("machine_local に入れてはいけない値 %q: %v", p, h.MachineLocal)
		}
	}
	if h.Empty() {
		t.Error("Empty が true")
	}
	o := h.Object()
	if o.String("home") != home || !o.Has("roots") || !o.Has("machine_local") {
		t.Errorf("Object: %v", o.Keys())
	}

	// ルートそのもののホームは送らない（サーバが拒むため）。何も無ければ Empty
	h = Collect(env.FromMap(map[string]string{"HOME": "/"}), nil, nil)
	if !h.Empty() || h.Object().Has("home") {
		t.Errorf("ルートのホーム: %+v", h)
	}
}

// 会話記録の置き場は agentdirs の解決に従う（CLAUDE_CONFIG_DIR・CODEX_HOME があればその下も入る。実在するものだけ）。
func TestCollectAgentDirs(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	cfg := filepath.Join(base, "cfg-claude")
	cdx := filepath.Join(base, "cfg-codex")
	mk := func(p string) {
		t.Helper()
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	defClaude, defCodex := filepath.Join(home, ".claude", "projects"), filepath.Join(home, ".codex", "sessions")
	cfgProjects, cdxSessions := filepath.Join(cfg, "projects"), filepath.Join(cdx, "sessions")
	mk(defClaude)
	mk(defCodex)
	mk(cfgProjects)
	mk(cdxSessions)

	// 設定なし: 既定の置き場だけ（環境変数側の置き場は入らない）
	h := Collect(env.FromMap(map[string]string{"HOME": home}), nil, nil)
	for _, want := range []string{defClaude, defCodex} {
		if !slices.Contains(h.MachineLocal, want) {
			t.Errorf("設定なし: machine_local に %q が無い: %v", want, h.MachineLocal)
		}
	}
	for _, bad := range []string{cfgProjects, cdxSessions} {
		if slices.Contains(h.MachineLocal, bad) {
			t.Errorf("設定なしで %q が入った: %v", bad, h.MachineLocal)
		}
	}

	// 設定あり: その下の置き場も入る
	h = Collect(env.FromMap(map[string]string{"HOME": home, "CLAUDE_CONFIG_DIR": cfg, "CODEX_HOME": cdx}), nil, nil)
	for _, want := range []string{defClaude, defCodex, cfgProjects, cdxSessions} {
		if !slices.Contains(h.MachineLocal, want) {
			t.Errorf("設定あり: machine_local に %q が無い: %v", want, h.MachineLocal)
		}
	}

	// 設定先が実在しなければ入れない。HOME が無くても環境変数側は入る
	h = Collect(env.FromMap(map[string]string{"CLAUDE_CONFIG_DIR": cfg, "CODEX_HOME": filepath.Join(base, "missing")}), nil, nil)
	if !slices.Contains(h.MachineLocal, cfgProjects) || len(h.MachineLocal) != 1 {
		t.Errorf("HOME なし: machine_local = %v, want [%s]", h.MachineLocal, cfgProjects)
	}
}

func TestPrivateToggle(t *testing.T) {
	for in, want := range map[string]string{
		"/private/tmp/x":   "/tmp/x",
		"/private/var/f/T": "/var/f/T",
		"/private/etc":     "/etc",
		"/tmp/x":           "/private/tmp/x",
		"/var/folders/a":   "/private/var/folders/a",
		"/tmpfoo/x":        "",
		"/private/keys/x":  "",
		"/Users/me/proj":   "",
	} {
		if got := privateToggle(in); got != want {
			t.Errorf("privateToggle(%q) = %q, want %q", in, got, want)
		}
	}
	// variants: literal と /private の形がそろう
	v := variants("/tmp/does-not-exist-pathhint")
	if !slices.Contains(v, "/tmp/does-not-exist-pathhint") || !slices.Contains(v, "/private/tmp/does-not-exist-pathhint") {
		t.Errorf("variants: %v", v)
	}
}
