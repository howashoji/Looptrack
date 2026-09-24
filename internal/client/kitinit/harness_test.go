package kitinit

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/client/cli"
	"github.com/howashoji/looptrack/internal/client/env"
)

// init のテストの仕掛け。導入先は t.TempDir() だけ（このリポジトリや実プロジェクトには init しない）。
// 本番に書き込まないよう、init には env.FromMap の環境だけを渡す（利用者の IM_*・LOOPTRACK_* は読まない）。
// サーバの URL は使われないポート（127.0.0.1:9）を指し、トークンは置かない。

var update = flag.Bool("update", false, "testdata/golden を書き直す")

const fakeURL = "http://127.0.0.1:9/im"

// fakeBin は PATH にある looptrack のふり（lookPath の差し替え）。
const fakeBin = "/fake/bin/looptrack"

type fakeRun struct {
	calls   []string
	version func() (string, int)
}

// stubs は外に出る口（PATH・子プロセス・時刻・対話）をテスト用に差し替える。
func stubs(t *testing.T, onPath bool) *fakeRun {
	t.Helper()
	Register()
	fr := &fakeRun{version: func() (string, int) { return "looptrack dev（headless・test）\n", 0 }}
	oldLook, oldExe, oldNow, oldTTY, oldRun := lookPath, executable, now, interactive, runCmd
	t.Cleanup(func() { lookPath, executable, now, interactive, runCmd = oldLook, oldExe, oldNow, oldTTY, oldRun })
	lookPath = func(name string) (string, error) {
		if name == "looptrack" && onPath {
			return fakeBin, nil
		}
		return "", exec.ErrNotFound
	}
	executable = func() (string, error) { return "/opt/lt/looptrack", nil }
	now = func() time.Time { return time.Date(2026, 9, 19, 12, 34, 56, 0, time.UTC) }
	interactive = func(r io.Reader) bool { return false }
	runCmd = func(ctx context.Context, dir string, envv []string, name string, args ...string) (string, int, error) {
		fr.calls = append(fr.calls, name+" "+strings.Join(args, " "))
		// LOOPTRACK_LANG だけは意図して渡す（子の版の行を親と同じ言語にそろえる）。
		for _, kv := range envv {
			if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "IM_") ||
				(strings.HasPrefix(k, "LOOPTRACK_") && k != "LOOPTRACK_BIN" && k != "LOOPTRACK_LANG") {
				t.Errorf("検査の子プロセスに %s が渡っています", k)
			}
		}
		switch {
		case len(args) == 1 && args[0] == "version":
			out, code := fr.version()
			return out, code, nil
		case len(args) == 2 && args[1] == "-h":
			return "usage: looptrack issue [-h] …\n", 0, nil
		}
		return "", 1, errors.New("想定しない起動: " + name)
	}
	return fr
}

type result struct {
	code           int
	stdout, stderr string
}

// runInit は ws で init を実行する（cli.Main を通す＝引数の解釈も本物）。サーバは使われないポートを指す。
func runInit(t *testing.T, root, stdin string, args ...string) result {
	t.Helper()
	return runInitIn(t, root, stdin, nil, append([]string{"--url", fakeURL}, args...)...)
}

// runInitEnv は環境変数を足して init を実行する（利用者の環境は読まない）。
func runInitEnv(t *testing.T, root string, extra map[string]string, args ...string) result {
	t.Helper()
	return runInitIn(t, root, "", extra, args...)
}

func runInitIn(t *testing.T, root, stdin string, extra map[string]string, args ...string) result {
	t.Helper()
	home := filepath.Join(root, "home")
	os.MkdirAll(home, 0o755)
	// LOOPTRACK_LANG: 下の検査と golden は日本語の文面を見る。指定が無いと CLI は環境に従い、
	// 日本語でなければ英語を出す（i18n.FromEnv）ので、ここで決めておく
	m := map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "APPDATA": home, "USERPROFILE": home,
		"CLAUDE_PROJECT_DIR": filepath.Join(root, "ws"), "LOOPTRACK_LANG": "ja"}
	for k, v := range extra {
		m[k] = v
	}
	var out, errb bytes.Buffer
	full := append([]string{"init", "--dir", filepath.Join(root, "ws")}, args...)
	code := cli.Main(full, cli.IO{Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errb}, env.FromMap(m))
	return result{code, out.String(), errb.String()}
}

func newRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	os.MkdirAll(filepath.Join(root, "ws"), 0o755)
	return root
}

func write(t *testing.T, p, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// snapshot は ws の中のファイル（中身・権限・symlink の先）を並べる。kit の本文そのもの（導入の形にしたもの）は名前だけにする
// （kit の文面を直すたびに golden が変わらないように。本文の一致は別に確かめる）。
func snapshot(t *testing.T, ws string) []string {
	t.Helper()
	files := embedded()
	var out []string
	filepath.WalkDir(ws, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == ws {
			return err
		}
		rel, _ := filepath.Rel(ws, p)
		rel = filepath.ToSlash(rel)
		if d.Type()&fs.ModeSymlink != 0 {
			tgt, _ := os.Readlink(p)
			out = append(out, fmt.Sprintf("%s -> %s", rel, filepath.ToSlash(tgt)))
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, _ := d.Info()
		body := read(t, p)
		mode := fmt.Sprintf("(%04o)", info.Mode().Perm())
		if runtime.GOOS == "windows" {
			mode = "(mode)"
		}
		if k := kitSource(files, body); k != "" {
			out = append(out, fmt.Sprintf("%s %s = %s", rel, mode, k))
			return nil
		}
		out = append(out, fmt.Sprintf("%s %s\n%s", rel, mode, indent(maskLoopBlock(body))))
		return nil
	})
	sort.Strings(out)
	return out
}

// kitSource は body が kit のどのファイルと同じか。
func kitSource(files map[string]string, body string) string {
	for _, n := range sortedKeys(files) {
		if body == files[n] {
			return "kit " + n
		}
	}
	return ""
}

var loopBlockRe = regexp.MustCompile(`(?s)` + regexp.QuoteMeta(loopBegin) + `\n.*?` + regexp.QuoteMeta(loopEnd))

// maskLoopBlock は AGENTS.md の loop 節の本文（kit/loop の rules から組み立てたもの）を印に置き換える。
func maskLoopBlock(s string) string {
	return loopBlockRe.ReplaceAllString(s, loopBegin+"\n（loop 節: kit/loop の rules・skill から組み立てた本文）\n"+loopEnd)
}

func indent(s string) string {
	if s == "" {
		return "    (空)"
	}
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	for i, l := range lines {
		if l == "" {
			lines[i] = ""
		} else {
			lines[i] = "    " + l
		}
	}
	return strings.Join(lines, "\n")
}

var hexRe = regexp.MustCompile(`\b[0-9a-f]{64}\b`)

func normalize(root, s string) string {
	s = strings.ReplaceAll(s, root, "$TMP")
	s = strings.ReplaceAll(s, filepath.ToSlash(root), "$TMP")
	if runtime.GOOS == "windows" { // 表示のパスの区切り（$TMP\ws・.claude\.looptrack-init-backup\…）
		s = strings.ReplaceAll(s, `$TMP\`, "$TMP/")
		s = strings.ReplaceAll(s, `.claude\.looptrack-init-backup\`, ".claude/.looptrack-init-backup/")
	}
	return hexRe.ReplaceAllString(s, "$$SHA256")
}

// golden は 1 回の実行の結果を golden の文字列にする。
func golden(t *testing.T, root, title string, r result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n#### exit %d\n\n#### stdout\n%s\n#### stderr\n%s\n#### files\n", title, r.code, maskLoopBlock(r.stdout), r.stderr)
	for _, f := range snapshot(t, filepath.Join(root, "ws")) {
		b.WriteString(f + "\n")
	}
	return normalize(root, b.String())
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", "golden", filepath.FromSlash(name)+".golden")
	if *update || os.Getenv("KITINIT_UPDATE") == "1" {
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("golden がありません（go test ./internal/client/kitinit -update で作る）: %v", err)
	}
	w := strings.ReplaceAll(string(want), "\r\n", "\n")
	if runtime.GOOS == "windows" { // Windows のファイルに POSIX の権限は無い
		w = regexp.MustCompile(`\(0[0-7]{3}\)`).ReplaceAllString(w, "(mode)")
	}
	if w != got {
		t.Errorf("golden と違います（%s）:\n%s\n%s", p, goldenHint, firstDiff(w, got))
	}
}

func firstDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	i := 0
	for i < len(w) && i < len(g) && w[i] == g[i] {
		i++
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d 行目から\n--- golden\n", i+1)
	for j := max(0, i-2); j < min(len(w), i+6); j++ {
		b.WriteString("  " + w[j] + "\n")
	}
	b.WriteString("--- 実行結果\n")
	for j := max(0, i-2); j < min(len(g), i+6); j++ {
		b.WriteString("  " + g[j] + "\n")
	}
	return b.String()
}
