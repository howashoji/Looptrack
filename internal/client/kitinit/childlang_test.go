package kitinit

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/i18n"
)

// buildRealLooptrackForLangTest は実物の looptrack をビルドする（子プロセスの言語の回帰テスト専用）。
// ほかの kitinit のテストは stubs() で runCmd 自体を差し替えるので、cleanEnv が組み立てた環境で
// 実物の子プロセスがどちらの言語を出すかまでは確かめない。ここでは実物を子プロセスとして本当に起こす。
func buildRealLooptrackForLangTest(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("-short のため実物のビルドを省略")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go が無いため省略")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "looptrack")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command(goBin, "build", "-o", bin, "./cmd/looptrack")
	build.Dir, _ = filepath.Abs(filepath.Join("..", "..", ".."))
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("looptrack のビルド: %v\n%s", err, out)
	}
	return bin
}

// TestDoctorChildVersionLangMatchesParent は、子プロセスの言語が親と食い違っていた不具合の回帰テスト。
//
// doctor が起こす子プロセス（looptrack version）の版の行が、親（doctor が選んだ言語＝DoctorOptions.Lang）と
// 同じ言語で出ることを確かめる。cleanEnv が LOOPTRACK_LANG を落としたまま渡さなければ、子は機械の LANG に
// 従ってしまう（内容は internal/client/kitinit/verify.go の cleanEnv。実際に踏んだ食い違いから起票した回帰テスト）。
// 機械の LANG は各ケースの親の言語とは逆にしておき、一致が偶然（機械の既定と同じだった）では説明できないようにする。
// 落ちたときに期待する文面（want/notWant）を書き換えて通してはいけない。cleanEnv が LOOPTRACK_LANG を
// 渡し直さない限り、この食い違いは機械の LANG しだいで再現する。
func TestDoctorChildVersionLangMatchesParent(t *testing.T) {
	bin := buildRealLooptrackForLangTest(t)
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LOOPTRACK_LANG", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "")

	cases := []struct {
		name        string
		parentLang  i18n.Lang
		machineLANG string
		want        string
		notWant     string
	}{
		{"親が日本語・機械は英語", i18n.JA, "en_US.UTF-8", "（headless・", " (headless, "},
		{"親が英語・機械は日本語", i18n.EN, "ja_JP.UTF-8", " (headless, ", "（headless・"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("LANG", c.machineLANG)
			home := t.TempDir()
			var out, errb bytes.Buffer
			Doctor([]string{"--dir", t.TempDir(), "--offline"}, DoctorOptions{
				Env:    env.FromMap(map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config")}),
				Stdout: &out, Stderr: &errb, Version: "v1.0.0", Lang: c.parentLang,
			})
			got := out.String() + errb.String()
			if !strings.Contains(got, c.want) {
				t.Errorf("子（looptrack version）の版の行が親の言語（%s）になっていない:\n%s", c.parentLang, got)
			}
			if strings.Contains(got, c.notWant) {
				t.Errorf("子の版の行が機械の LANG（%s）に従っています（親は %s）:\n%s", c.machineLANG, c.parentLang, got)
			}
		})
	}
}

// TestVerifyChildVersionLangMatchesParent は同じ回帰を looptrack init の verify 経路（internal/client/kitinit/verify.go の
// runCmd(ctx, in.target, cleanEnv(r.lang), bin, "version") と、rules の注入で使う cleanEnv(r.lang, "CLAUDE_PROJECT_DIR=…")）
// で確かめる。verify は init の一部として動くので、cli.Main の "init" を通す（サーバは使われないポートを指す）。
func TestVerifyChildVersionLangMatchesParent(t *testing.T) {
	Register()
	bin := buildRealLooptrackForLangTest(t)
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LOOPTRACK_LANG", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "")

	cases := []struct {
		name        string
		parentLang  string // LOOPTRACK_LANG（init に渡す環境）
		machineLANG string
		want        string
		notWant     string
	}{
		{"親が日本語・機械は英語", "ja", "en_US.UTF-8", "（headless・", " (headless, "},
		{"親が英語・機械は日本語", "en", "ja_JP.UTF-8", " (headless, ", "（headless・"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("LANG", c.machineLANG)
			root := newRoot(t)
			r := runInitEnv(t, root, map[string]string{"LOOPTRACK_LANG": c.parentLang, "LOOPTRACK_API_URL": "http://127.0.0.1:9/im"},
				"--project", "demo", "--agent", "claude-code")
			got := r.stdout + r.stderr
			if !strings.Contains(got, c.want) {
				t.Errorf("子（looptrack version）の版の行が親の言語（%s）になっていない:\n%s", c.parentLang, got)
			}
			if strings.Contains(got, c.notWant) {
				t.Errorf("子の版の行が機械の LANG（%s）に従っています（親は %s）:\n%s", c.machineLANG, c.parentLang, got)
			}
		})
	}
}
