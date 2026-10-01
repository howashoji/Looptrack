package kitinit

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/setuppath"
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

// TestDoctorKitSelfRepo: looptrack 自身（kit の正本・cli.IsSelfRepo が真）では、.looptrack-kit.json が無くても
// init を勧める注意（kitinit.doctor.kit.missing）を出さない。代わりに正本であることと .claude/ に手で配線している
// 旨（kitinit.doctor.kit.self_repo）を出す。対照として、正本でない一時ディレクトリで配線があり控えが無いときは、
// 従来どおり kitinit.doctor.kit.missing が出ることも同じテストで確かめる。
func TestDoctorKitSelfRepo(t *testing.T) {
	settings := `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "looptrack hook summary --agent claude-code", "timeout": 5}]}]}}`
	stubs(t, true)
	executable = func() (string, error) { return fakeBin, nil }

	wantMissing := i18n.T(i18n.JA, "kitinit.doctor.kit.missing", "file", kitJSON)
	wantSelfRepo := i18n.T(i18n.JA, "kitinit.doctor.kit.self_repo", "file", kitJSON)

	// 正本の形: kit/embed.go（ファイル）+ cmd/looptrack（ディレクトリ）。cli.IsSelfRepo（TestIsSelfRepo）が
	// 実物で見ている配置と同じもの。.looptrack-kit.json は置かない（init が拒否するので作られない）。
	self := t.TempDir()
	if err := os.MkdirAll(filepath.Join(self, "cmd", "looptrack"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(self, "kit"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(self, "kit", "embed.go"), "package kit\n")
	writeFile(t, filepath.Join(self, ".claude", "settings.json"), settings)
	code, out := doctor(t, self, nil)
	if !strings.Contains(out, "looptrack hook の配線 1 件") {
		t.Fatalf("前提が崩れています（配線が数えられていない。対照と比べる土台が無い）:\n%s", out)
	}
	if code != 0 || strings.Contains(out, "NG") {
		t.Errorf("正本: 終了コード %d:\n%s", code, out)
	}
	if !strings.Contains(out, wantSelfRepo) {
		t.Errorf("正本の案内（%q）が出ない:\n%s", wantSelfRepo, out)
	}
	if strings.Contains(out, wantMissing) {
		t.Errorf("正本なのに init を勧める文が出た:\n%s", out)
	}

	// 対照: 正本でない（配置が違う）一時ディレクトリで配線があり控えが無いときは、従来どおり kit.missing が出る。
	other := t.TempDir()
	writeFile(t, filepath.Join(other, ".claude", "settings.json"), settings)
	code, out = doctor(t, other, nil)
	if !strings.Contains(out, "looptrack hook の配線 1 件") {
		t.Fatalf("前提が崩れています（対照でも配線が数えられていない）:\n%s", out)
	}
	if code != 0 || strings.Contains(out, "NG") {
		t.Errorf("対照: 終了コード %d:\n%s", code, out)
	}
	if !strings.Contains(out, wantMissing) {
		t.Errorf("対照（正本でない）で kit.missing（%q）が出ない（前提が崩れています。IsSelfRepo が誤判定した可能性がある）:\n%s", wantMissing, out)
	}
	if strings.Contains(out, wantSelfRepo) {
		t.Errorf("対照なのに正本の案内が出た:\n%s", out)
	}
}

// サーバが server_update（admin のトークンのときだけ）を返すと、doctor は注意の行で新しい版と更新の 1 行を出す。
// 返さない（古いサーバ・member）ときは何も出さない。注意なので終了コードは 0 のまま。
func TestDoctorServerUpdate(t *testing.T) {
	var body atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/api/v1/dist") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body.Load().(string))
	}))
	defer srv.Close()
	stubs(t, true)
	executable = func() (string, error) { return fakeBin, nil }
	run := func() (int, string) {
		t.Helper()
		home := t.TempDir()
		m := map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"),
			"LOOPTRACK_API_URL": srv.URL + "/im", "LOOPTRACK_TOKEN": "lt_test"}
		var out bytes.Buffer
		code := Doctor([]string{"--dir", t.TempDir()}, DoctorOptions{Env: env.FromMap(m), Stdout: &out, Stderr: &out, Version: "v1.0.0", Lang: i18n.JA})
		return code, out.String()
	}
	const cmd = "curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh -s -- --upgrade"
	body.Store(`{"binaries":[],"server_update":{"version":"v1.1.0","current":"v1.0.0","command":"` + cmd + `"}}`)
	code, out := run()
	if want := "サーバに新しい版 v1.1.0 があります（サーバの版は v1.0.0）。install.sh で入れたサーバは、サーバで次を実行して更新します: " + cmd; code != 0 || !strings.Contains(out, want) {
		t.Errorf("server_update（1 行つき）: 終了コード %d・%q が無い:\n%s", code, want, out)
	}
	body.Store(`{"binaries":[],"server_update":{"version":"v1.1.0","current":"v1.0.0"}}`)
	if _, out := run(); !strings.Contains(out, "サーバに新しい版 v1.1.0 があります（サーバの版は v1.0.0）。サーバの管理者が更新します") {
		t.Errorf("server_update（1 行なし）:\n%s", out)
	}
	body.Store(`{"binaries":[]}`)
	if code, out := run(); code != 0 || strings.Contains(out, "サーバに新しい版") || !strings.Contains(out, "向けの looptrack がありません") {
		t.Errorf("server_update なし（対照: 配布の一覧は読めている）: %d\n%s", code, out)
	}
}

// TestDoctorRedoBySource は、doctor の案内が導入の置き方で分かれることを確かめる。控えの置き方が server（サーバ版の setup の
// 取得 + init）なら、配線のやり直しと更新は MCP の setup ツールの手順（--url の無い init や self-update ではない）、copy など
// それ以外なら従来どおり looptrack issue init の再実行と self-update。トークンが無いときのログインの案内は、PATH の
// looptrack ではなく実行中の looptrack の絶対パスで出す。分岐の両側を同じテストで通す。
func TestDoctorRedoBySource(t *testing.T) {
	stubs(t, true)
	executable = func() (string, error) { return fakeBin, nil }
	server, local := i18n.T(i18n.JA, "kitinit.doctor.redo.server"), i18n.T(i18n.JA, "kitinit.doctor.redo.local")
	if server == local || !strings.Contains(server, "setup ツール") || !strings.Contains(local, "looptrack issue init") {
		t.Fatalf("前提が崩れています: やり直しの文が置き方で分かれていない: %q / %q", server, local)
	}
	login := `トークンがありません（"` + fakeBin + `" issue login --browser --url https://example.invalid/im）`
	if runtime.GOOS == "windows" {
		login = `トークンがありません（& '` + fakeBin + `' issue login --browser --url https://example.invalid/im）`
	}
	for _, c := range []struct{ source, want, notWant string }{
		{"server", server, local},
		{"copy", local, server},
		{"link", local, server},
	} {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, ".codex", "hooks.json"),
			`{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "\"/nowhere/looptrack\" hook summary --agent codex"}]}]}}`)
		writeFile(t, filepath.Join(dir, ".claude", ".looptrack-kit.json"), `{"project": "demo", "source": "`+c.source+`"}`)
		code, out := doctor(t, dir, map[string]string{"LOOPTRACK_API_URL": "https://example.invalid/im"})
		missing := "実行ファイル /nowhere/looptrack がありません（" + c.want + "）"
		if code != 1 || !strings.Contains(out, missing) || strings.Contains(out, c.notWant) {
			t.Errorf("置き方 %s: やり直しの案内に %q が無い（または %q がある）:\n%s", c.source, missing, c.notWant, out)
		}
		if !strings.Contains(out, login) {
			t.Errorf("置き方 %s: ログインの案内が実行中の looptrack の絶対パスでない（%q が無い）:\n%s", c.source, login, out)
		}
	}
	// 更新の案内（配布の最新との比較。--offline では出ないので、選ぶ関数を直接確かめる）
	if _, u := doctorRedo(i18n.JA, "server"); u != i18n.T(i18n.JA, "kitinit.doctor.update.server") || strings.Contains(u, "self-update") {
		t.Errorf("server の更新の案内: %q", u)
	}
	if _, u := doctorRedo(i18n.JA, "copy"); u != i18n.T(i18n.JA, "kitinit.doctor.update.local") || !strings.Contains(u, "self-update") {
		t.Errorf("copy の更新の案内: %q", u)
	}
}

// TestDoctorPathMissingBySource は、PATH から looptrack を解決できないときの doctor の注意が、サーバ版の導入（控えの置き方が
// server）では直し方（setup の手順の再実行と、PATH を足すだけのコマンド）を示すことを確かめる。
// 検知しない場合（PATH にある）と、ローカルの導入（従来の文）も同じテストで確かめる。
func TestDoctorPathMissingBySource(t *testing.T) {
	serverMark := i18n.T(i18n.JA, "kitinit.doctor.redo.server")
	cmd := setuppath.PosixCommand(i18n.JA)
	if runtime.GOOS == "windows" {
		cmd = setuppath.WinCommand(i18n.JA)
	}
	for _, c := range []struct {
		source string
		onPath bool
		want   []string
		not    []string
	}{
		{"server", false, []string{"PATH で looptrack が見つかりません", serverMark, cmd}, []string{i18n.T(i18n.JA, "kitinit.doctor.path.missing")}},
		{"copy", false, []string{i18n.T(i18n.JA, "kitinit.doctor.path.missing")}, []string{serverMark, cmd}},
		{"server", true, []string{"PATH の looptrack: " + fakeBin}, []string{"PATH で looptrack が見つかりません", cmd}},
	} {
		stubs(t, c.onPath)
		executable = func() (string, error) { return fakeBin, nil }
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, ".claude", ".looptrack-kit.json"), `{"project": "demo", "source": "`+c.source+`"}`)
		_, out := doctor(t, dir, map[string]string{"LOOPTRACK_API_URL": "https://example.invalid/im"})
		for _, w := range c.want {
			if !strings.Contains(out, w) {
				t.Errorf("置き方 %s・PATH に %v: %q が無い:\n%s", c.source, c.onPath, w, out)
			}
		}
		for _, n := range c.not {
			if strings.Contains(out, n) {
				t.Errorf("置き方 %s・PATH に %v: %q がある:\n%s", c.source, c.onPath, n, out)
			}
		}
	}
}
