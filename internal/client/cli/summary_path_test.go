package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/i18n"
)

// TestSummaryPathMissing は、要約（SessionStart の hook が出すもの）が、サーバ版の導入（控えの置き方が server）で
// PATH から looptrack を解決できないときに、直し方（setup の手順の再実行と doctor）を出すことを確かめる。
// 検知しない場合（PATH にある・控えが server でない・控えが無い）も同じテストで確かめる（検知する側が対照）。
// looptrack 自身のリポジトリ（kit の正本・IsSelfRepo）では、init に触れない正本用の文（.self_repo）を出す。
// 正本でない側の従来の文が対照。
func TestSummaryPathMissing(t *testing.T) {
	oldLook, oldExe := lookPathFn, executableFn
	t.Cleanup(func() { lookPathFn, executableFn = oldLook, oldExe })
	executableFn = func() (string, error) { return "/opt/lt/looptrack", nil }
	onPath := false
	lookPathFn = func(name string) (string, error) {
		if name == "looptrack" && onPath {
			return "/opt/lt/looptrack", nil
		}
		return "", exec.ErrNotFound
	}
	root := func(source string) string {
		dir := t.TempDir()
		if source != "" {
			if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".claude", ".looptrack-kit.json"), []byte(`{"project": "demo", "source": "`+source+`"}`), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	summary := func(dir string) string {
		t.Helper()
		var out bytes.Buffer
		c := &Ctx{IO: IO{Stdout: &out, Stderr: &out}, Root: dir, Lang: i18n.JA}
		data := jsonorder.NewObject().Set("in_progress", []any{}).Set("in_review", []any{}).Set("ready", []any{}).
			Set("ready_total", int64(0)).Set("counts", jsonorder.NewObject().Set("open", int64(0)).Set("open_bugs", int64(0)))
		if err := c.printSummary(data, 12); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	mark := "looptrack が PATH にありません"

	// 検知する: 控えが server で PATH に無い → setup の手順の再実行と、実行中の looptrack の doctor を示す
	got := summary(root("server"))
	if !strings.Contains(got, mark) || !strings.Contains(got, "setup ツール") || !strings.Contains(got, "/opt/lt/looptrack") {
		t.Fatalf("PATH に無いことを知らせていない:\n%s", got)
	}
	doctor := `"/opt/lt/looptrack" doctor`
	if runtime.GOOS == "windows" {
		doctor = "& '/opt/lt/looptrack' doctor"
	}
	normal := i18n.T(i18n.JA, "cli.summary.path_missing", "doctor", doctor)
	self := i18n.T(i18n.JA, "cli.summary.path_missing.self_repo", "doctor", doctor)
	if self == "cli.summary.path_missing.self_repo" || self == normal || !strings.Contains(self, "init はしない") ||
		i18n.T(i18n.EN, "cli.summary.path_missing.self_repo", "doctor", doctor) == self {
		t.Fatalf("前提が崩れています: 正本用の文が ja.json / en.json に無いか、従来の文と分かれていない: %q", self)
	}
	// 正本でない: 従来の文（取得と init をまとめた手順）
	if !strings.Contains(got, normal) || strings.Contains(got, self) {
		t.Errorf("正本でない導入先で従来の文が出ない（か正本用の文が出た）:\n%s", got)
	}
	// 正本: 取得と PATH だけで init に触れない文
	selfRoot := root("server")
	if err := os.MkdirAll(filepath.Join(selfRoot, "cmd", "looptrack"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(selfRoot, "kit"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(selfRoot, "kit", "embed.go"), []byte("package kit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := summary(selfRoot); !strings.Contains(got, self) || strings.Contains(got, normal) {
		t.Errorf("正本で正本用の文が出ない（か従来の文が出た）:\n%s", got)
	}
	// 検知しない: PATH にある / 控えが server でない（ローカルの導入）/ 控えが無い
	onPath = true
	if got := summary(root("server")); strings.Contains(got, mark) {
		t.Errorf("PATH にあるのに知らせた:\n%s", got)
	}
	onPath = false
	for _, source := range []string{"copy", "link", ""} {
		if got := summary(root(source)); strings.Contains(got, mark) {
			t.Errorf("控えの置き方 %q で知らせた（サーバ版だけが対象）:\n%s", source, got)
		}
	}
}
