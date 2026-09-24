package kitinit

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/i18n"
)

func writeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func doctor(t *testing.T, dir string, vars map[string]string) (int, string) {
	t.Helper()
	home := t.TempDir()
	m := map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config")}
	for k, v := range vars {
		m[k] = v
	}
	var out, errb bytes.Buffer
	code := Doctor([]string{"--dir", dir, "--offline"}, DoctorOptions{Env: env.FromMap(m), Stdout: &out, Stderr: &errb, Version: "v1.0.0", Lang: i18n.JA})
	return code, out.String() + errb.String()
}

// TestDoctor は looptrack doctor が PATH・配線・導入の記録を確かめることを確かめる（読むだけ・サーバに送らない）。
func TestDoctor(t *testing.T) {
	settings := `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "looptrack hook summary --agent claude-code", "timeout": 5}]}],
  "PreToolUse": [{"matcher": "Edit", "hooks": [{"type": "command", "command": "bash \"$CLAUDE_PROJECT_DIR/.claude/hooks/my-own-guard.sh\""}]}]}}`

	// PATH にあり、配線が looptrack の名前: OK（手で置いた hook は数えない）
	stubs(t, true)
	executable = func() (string, error) { return fakeBin, nil }
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".claude", "settings.json"), settings)
	writeFile(t, filepath.Join(dir, ".claude", ".looptrack-kit.json"), `{"project": "demo", "source": "server", "loop": {"installed": true, "version": "1.2.0"}}`)
	code, out := doctor(t, dir, nil)
	for _, want := range []string{"OK    PATH の looptrack: " + fakeBin, "looptrack hook の配線 1 件",
		"プロジェクト demo・置き方 server・loop あり（1.2.0）", "サーバの URL（環境変数 LOOPTRACK_API_URL）がありません"} {
		if !strings.Contains(out, want) {
			t.Errorf("出力に %q が無い:\n%s", want, out)
		}
	}
	if code != 0 || strings.Contains(out, "NG") {
		t.Errorf("終了コード %d:\n%s", code, out)
	}

	// PATH に無いのに PATH の名前で配線している: NG
	stubs(t, false)
	code, out = doctor(t, dir, nil)
	if code != 1 || !strings.Contains(out, "NG    .claude/settings.json: summary の hook を PATH の looptrack で配線していますが、PATH にありません") {
		t.Errorf("PATH なし:\n%s", out)
	}

	// 手元専用の設定に絶対パス（あるファイル）: OK、無いファイル: NG。知らない hook の名前も NG
	bin := filepath.Join(t.TempDir(), "looptrack")
	writeFile(t, bin, "#!/bin/sh\n")
	// JSON に埋めるので \ をエスケープする（Windows のパス）
	jbin := strings.ReplaceAll(bin, `\`, `\\`)
	dir2 := t.TempDir()
	writeFile(t, filepath.Join(dir2, ".claude", "settings.local.json"),
		`{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "\"`+jbin+`\" hook summary --agent claude-code"}]}]}}`)
	code, out = doctor(t, dir2, map[string]string{"LOOPTRACK_API_URL": "https://example.invalid/im"})
	if code != 0 || !strings.Contains(out, "OK    .claude/settings.local.json: looptrack hook の配線 1 件") || !strings.Contains(out, "トークンがありません") {
		t.Errorf("絶対パス:\n%s", out)
	}
	writeFile(t, filepath.Join(dir2, ".codex", "hooks.json"),
		`{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "\"/nowhere/looptrack\" hook summary --agent codex"}]}],
		  "Stop": [{"hooks": [{"type": "command", "command": "\"`+jbin+`\" hook no-such-hook --agent codex"}]}]}}`)
	code, out = doctor(t, dir2, nil)
	if code != 1 || !strings.Contains(out, "実行ファイル /nowhere/looptrack がありません") || !strings.Contains(out, "looptrack に無い hook") {
		t.Errorf("無い実行ファイル・知らない hook:\n%s", out)
	}

	// 未導入のプロジェクト: 注意だけ（0）
	code, out = doctor(t, t.TempDir(), nil)
	if code != 0 || !strings.Contains(out, "looptrack hook の配線がありません") {
		t.Errorf("未導入:\n%s", out)
	}
}
