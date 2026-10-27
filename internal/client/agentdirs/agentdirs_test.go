package agentdirs

import (
	"path/filepath"
	"testing"
)

func getter(m map[string]string) Getenv { return func(k string) string { return m[k] } }

func TestResolve(t *testing.T) {
	home := filepath.FromSlash("/h")
	other := filepath.FromSlash("/o/cfg")
	cases := []struct {
		name string
		env  map[string]string
		home string
		fn   func(Getenv, string) string
		want string
	}{
		{"claude 既定", nil, home, ClaudeConfigDir, filepath.Join(home, ".claude")},
		{"claude 設定あり", map[string]string{"CLAUDE_CONFIG_DIR": other}, home, ClaudeConfigDir, other},
		{"claude 空白だけは無いものとする", map[string]string{"CLAUDE_CONFIG_DIR": "  "}, home, ClaudeConfigDir, filepath.Join(home, ".claude")},
		{"claude 前後の空白は除く", map[string]string{"CLAUDE_CONFIG_DIR": " " + other + " "}, home, ClaudeConfigDir, other},
		{"claude home も設定も無い", nil, "", ClaudeConfigDir, ""},
		{"claude home が無くても設定があれば使う", map[string]string{"CLAUDE_CONFIG_DIR": other}, "", ClaudeConfigDir, other},
		{"claude projects 既定", nil, home, ClaudeProjectsDir, filepath.Join(home, ".claude", "projects")},
		{"claude projects 設定あり", map[string]string{"CLAUDE_CONFIG_DIR": other}, home, ClaudeProjectsDir, filepath.Join(other, "projects")},
		{"claude projects 決められない", nil, "", ClaudeProjectsDir, ""},
		{"codex 既定", nil, home, CodexHome, filepath.Join(home, ".codex")},
		{"codex 設定あり", map[string]string{"CODEX_HOME": other}, home, CodexHome, other},
		{"codex は CLAUDE_CONFIG_DIR を見ない", map[string]string{"CLAUDE_CONFIG_DIR": other}, home, CodexHome, filepath.Join(home, ".codex")},
		{"codex sessions 既定", nil, home, CodexSessionsDir, filepath.Join(home, ".codex", "sessions")},
		{"codex sessions 設定あり", map[string]string{"CODEX_HOME": other}, home, CodexSessionsDir, filepath.Join(other, "sessions")},
		{"codex sessions 決められない", nil, "", CodexSessionsDir, ""},
	}
	for _, c := range cases {
		if got := c.fn(getter(c.env), c.home); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	if got := ClaudeConfigDir(nil, home); got != filepath.Join(home, ".claude") {
		t.Errorf("nil getenv: %q", got)
	}
}

func TestHome(t *testing.T) {
	if got := Home(getter(map[string]string{"HOME": "/a", "USERPROFILE": "/b"})); got != "/a" {
		t.Errorf("HOME 優先: %q", got)
	}
	if got := Home(getter(map[string]string{"USERPROFILE": "/b"})); got != "/b" {
		t.Errorf("USERPROFILE: %q", got)
	}
	if got := Home(getter(nil)); got != "" || Home(nil) != "" {
		t.Errorf("空: %q", got)
	}
}
