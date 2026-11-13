package loop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/hookio"
)

// .codex/config.toml の読み方: init が書く節の形と、外に書いた shell_environment_policy.set の形を読む。
// ほかの節の set・エスケープを含む値・壊れた行は読まない（何も補わない）。
func TestCodexConnectionForms(t *testing.T) {
	cases := []struct {
		name, toml, url, project string
	}{
		{"節", "# x\n[shell_environment_policy]\nset = { LOOPTRACK_API_URL = \"https://a.example/im\", LOOPTRACK_PROJECT = \"abc\" }\n", "https://a.example/im", "abc"},
		{"点つき", "model = \"x\"\nshell_environment_policy.set = { LOOPTRACK_PROJECT = \"p\", LOOPTRACK_API_URL = \"http://h\" }\n", "http://h", "p"},
		{"ほかの節", "[other]\nset = { LOOPTRACK_API_URL = \"http://no\" }\n", "", ""},
		{"エスケープ", "[shell_environment_policy]\nset = { LOOPTRACK_API_URL = \"http://a\\\"b\" }\n", "", ""},
		{"壊れた行", "[shell_environment_policy]\nset = { LOOPTRACK_API_URL = \"http://a\"\n", "", ""},
	}
	for _, c := range cases {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".codex"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".codex", "config.toml"), []byte(c.toml), 0o644); err != nil {
			t.Fatal(err)
		}
		got := codexConnection(dir)
		if got[connURLEnv] != c.url || got[connProjectEnv] != c.project {
			t.Errorf("%s: %v（want %q %q）", c.name, got, c.url, c.project)
		}
	}
	if got := codexConnection(t.TempDir()); len(got) != 0 {
		t.Errorf("ファイルが無いのに読んだ: %v", got)
	}
}

// 導入先の設定から URL を補ったとき、子に渡す環境（Environ）と hook が読む値（Getenv）から環境のトークンを外す。
// 対照: 環境に URL もあるときは何も補わず、トークンは残る。値は出さずに有無だけを比べる。
func TestInstallConnectionDropsTokenFromChildEnv(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	toml := "[shell_environment_policy]\nset = { LOOPTRACK_API_URL = \"https://install.example/im\", LOOPTRACK_PROJECT = \"p\" }\n"
	if err := os.WriteFile(filepath.Join(dir, ".codex", "config.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	has := func(environ []string, key string) bool {
		for _, kv := range environ {
			if k, _, _ := strings.Cut(kv, "="); k == key {
				return true
			}
		}
		return false
	}
	ev := hookio.Event{Agent: hookio.Codex, ProjectDir: dir}
	mk := func(vars map[string]string) *Env {
		return &Env{Getenv: func(k string) string { return vars[k] }, Getwd: func() string { return dir },
			Environ: func() []string {
				var out []string
				for k, v := range vars {
					out = append(out, k+"="+v)
				}
				return out
			}}
	}
	got := withInstallConnection(mk(map[string]string{"LOOPTRACK_TOKEN": "t-env", "PATH": "/bin"}), ev)
	env := got.Environ()
	if has(env, "LOOPTRACK_TOKEN") || got.Getenv("LOOPTRACK_TOKEN") != "" {
		t.Errorf("補った URL の子の環境にトークンが残った（Environ %v・Getenv %v）", has(env, "LOOPTRACK_TOKEN"), got.Getenv("LOOPTRACK_TOKEN") != "")
	}
	if !has(env, "LOOPTRACK_API_URL") || !has(env, "PATH") {
		t.Errorf("前提が崩れています: 補った URL かほかの値が子の環境に無い")
	}
	ctrl := withInstallConnection(mk(map[string]string{"LOOPTRACK_TOKEN": "t-env", "LOOPTRACK_API_URL": "https://env.example/im", "LOOPTRACK_PROJECT": "p"}), ev)
	if !has(ctrl.Environ(), "LOOPTRACK_TOKEN") || ctrl.Getenv("LOOPTRACK_TOKEN") == "" {
		t.Errorf("対照: 環境に URL があるのにトークンを外した")
	}
}
