package clitest

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
)

var update = flag.Bool("update", false, "golden を Go 版（looptrack issue）の出力で書き直す（CLITEST_UPDATE=1 でも同じ）")

func TestMain(m *testing.M) {
	if BrowserHelperMain() {
		os.Exit(0)
	}
	code := m.Run()
	if builtDir != "" {
		os.RemoveAll(builtDir)
	}
	os.Exit(code)
}

// updating は golden を Go 版の出力で書き直すか（-update・CLITEST_UPDATE=1。CLITEST_UPDATE_GO=1 も同じ）。
func updating() bool {
	return *update || os.Getenv("CLITEST_UPDATE") == "1" || os.Getenv("CLITEST_UPDATE_GO") == "1"
}

const goldenDir = "testdata/golden"

func goldenPath(name string) string {
	return filepath.Join(goldenDir, filepath.FromSlash(name)+".golden")
}

var (
	builtOnce sync.Once
	builtDir  string // このテストで作った looptrack の置き場（TestMain の後で消す）
	builtBin  string
	builtErr  error
)

// goBin は Go 版の実行ファイル（LOOPTRACK_BIN か、このテストで 1 回だけ go build したもの）。go も無ければ省略する。
func goBin(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("LOOPTRACK_BIN"); bin != "" {
		abs, err := filepath.Abs(bin)
		if err != nil {
			t.Fatal(err)
		}
		return abs
	}
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go が無く LOOPTRACK_BIN も無いため省略")
	}
	builtOnce.Do(func() {
		if builtDir, builtErr = os.MkdirTemp("", "clitest-looptrack-"); builtErr != nil {
			return
		}
		builtBin = filepath.Join(builtDir, "looptrack")
		if runtime.GOOS == "windows" {
			builtBin += ".exe" // 拡張子が無いと exec が見つけない
		}
		cmd := exec.Command(gobin, "build", "-o", builtBin, "./cmd/looptrack")
		cmd.Dir, _ = filepath.Abs(filepath.Join("..", ".."))
		if b, err := cmd.CombinedOutput(); err != nil {
			builtErr = fmt.Errorf("%v\n%s", err, b)
		}
	})
	if builtErr != nil {
		t.Fatalf("looptrack を作れません: %v", builtErr)
	}
	return builtBin
}

// TestCLIGolden はすべてのケースを Go 版で流し、golden と比べる（-update なら Go 版の出力で書き直す）。
func TestCLIGolden(t *testing.T) {
	impl := GoImpl(goBin(t))
	for _, c := range allCases() {
		t.Run(c.Name, func(t *testing.T) {
			t.Parallel()
			if c.Browser && runtime.GOOS == "windows" {
				t.Skip("ブラウザの代わりはシェルスクリプトのため Windows では省略")
			}
			if c.PosixPerm && runtime.GOOS == "windows" {
				t.Skip("POSIX のファイルの権限を前提にするケースのため Windows では省略")
			}
			path := goldenPath(c.Name)
			got := stableVersion(Run(t, impl, c).Golden())
			if updating() {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("golden がありません（go test ./internal/clitest -update で作る）: %v", err)
			}
			if w, g := osLoose(stableVersion(string(want))), osLoose(got); w != g {
				t.Errorf("[%s] golden と違います（%s）:\n%s", impl.Name, path, lineDiff(w, g))
			}
		})
	}
}

// fileModeLine は golden の files の行の末尾の権限（「ws/out/.keep (0644)」の「 (0644)」）。
var fileModeLine = regexp.MustCompile(`(?m)^(\S.*) \(0[0-7]{3}\)$`)

// verifyVersion は init の verify の行の looptrack の版と OS/アーキテクチャ（「looptrack v0.0.0-…（headless・darwin/arm64）」、
// 英語は「looptrack v0.0.0-… (headless, darwin/arm64)」）。verify は PATH・~/.local/bin の looptrack を子プロセスで呼んで版を出す。
// その子は LOOPTRACK_* を外した環境で動く（kitinit の cleanEnv）が、表示の言語（LOOPTRACK_LANG）だけは親（verify）が選んだ
// 言語で渡し直すので、版の文面の言語は記録（golden）の LOOPTRACK_LANG（ja 固定。harness_run_test.go の ChildEnv）に揃う
// それでも版そのものと OS/アーキテクチャは実行する機械しだいで変わるので、両方の形を同じ「looptrack <版>」に置き換える。
var verifyVersion = regexp.MustCompile(`looptrack \S+(（(headless|desktop)・[a-z0-9]+/[a-z0-9]+）| \((headless|desktop), [a-z0-9]+/[a-z0-9]+\))`)

// stableVersion は verify の行の版を <版> に置き換える（golden の保存と比較の両方に使う）。
func stableVersion(s string) string {
	return verifyVersion.ReplaceAllString(s, "looptrack <版>")
}

// osLoose は golden との比較から OS の流儀の違いだけを除く（Windows のみ。ほかの OS はそのまま）。
// golden は POSIX での出力で、Windows の CLI はパスを \ で区切って表示し（filepath の流儀）、
// ファイルに POSIX の権限が無い（守りは ACL）。この 2 つは両側から同じように消して比べる。
func osLoose(s string) string {
	if runtime.GOOS != "windows" {
		return s
	}
	s = strings.ReplaceAll(s, `\`, "/")
	return fileModeLine.ReplaceAllString(s, "$1 (mode)")
}

// TestGoldenFilesHaveCases は、ケースの無い golden（改名・削除の残り）が無いことを確かめる。
func TestGoldenFilesHaveCases(t *testing.T) {
	names := map[string]bool{}
	for _, c := range allCases() {
		if names[c.Name] {
			t.Errorf("ケース名が重複しています: %s", c.Name)
		}
		names[c.Name] = true
	}
	var stale []string
	err := filepath.Walk(goldenDir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".golden") {
			return err
		}
		rel, _ := filepath.Rel(goldenDir, p)
		name := strings.TrimSuffix(filepath.ToSlash(rel), ".golden")
		if !names[name] {
			stale = append(stale, name)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("ケースの無い golden があります（削除してください）: %s", strings.Join(stale, ", "))
	}
}

// lineDiff は最初に違う行の前後を示す（全体の差分は -update の後に git diff で見る）。
func lineDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	i := 0
	for i < len(w) && i < len(g) && w[i] == g[i] {
		i++
	}
	from := max(0, i-3)
	var b strings.Builder
	fmt.Fprintf(&b, "%d 行目から違います\n--- golden\n", i+1)
	for j := from; j < min(len(w), i+8); j++ {
		fmt.Fprintf(&b, "  %s\n", w[j])
	}
	b.WriteString("--- 実行結果\n")
	for j := from; j < min(len(g), i+8); j++ {
		fmt.Fprintf(&b, "  %s\n", g[j])
	}
	return b.String()
}
