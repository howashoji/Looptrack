package server

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/howashoji/looptrack/internal/testutil"
)

// サーバの振る舞いを CLI（looptrack issue …）越しに確かめるテストの共通部品（以前の CLI を撤去した後のもの）。
// CLI の出力そのもの（サブコマンドごとの文面・エラー）は internal/clitest の golden で確かめる。ここではサーバとの往復を見る。

// goCLIBuild は、このパッケージのテストで 1 回だけ go build した looptrack（テストの終わりに消す）。
var goCLIBuild struct {
	once sync.Once
	dir  string
	path string
	out  string
	err  error
}

func TestMain(m *testing.M) {
	code := m.Run()
	if goCLIBuild.dir != "" {
		os.RemoveAll(goCLIBuild.dir)
	}
	os.Exit(code)
}

// looptrackBin は Go の CLI の実行ファイル（LOOPTRACK_BIN か、このパッケージのテストで 1 回だけ go build したもの）。
func looptrackBin(t *testing.T) string {
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
	b := &goCLIBuild
	b.once.Do(func() {
		b.dir, b.err = os.MkdirTemp("", "looptrack-servertest-")
		if b.err != nil {
			return
		}
		b.path = filepath.Join(b.dir, "looptrack")
		if runtime.GOOS == "windows" {
			b.path += ".exe" // 拡張子が無いと exec が見つけない
		}
		cmd := exec.Command(gobin, "build", "-o", b.path, "./cmd/looptrack")
		cmd.Dir, _ = filepath.Abs(filepath.Join("..", ".."))
		out, err := cmd.CombinedOutput()
		b.out, b.err = string(out), err
	})
	if b.err != nil {
		t.Fatalf("looptrack を作れません: %v\n%s", b.err, b.out)
	}
	return b.path
}

// cliPATH は CLI を起動するテストに渡す最小の PATH（"PATH=…" の形。開発者の PATH の looptrack などを持ち込まない）。
// Windows はシステムのディレクトリが環境で違うので、今の PATH を渡す。
func cliPATH() string {
	if runtime.GOOS == "windows" {
		return "PATH=" + os.Getenv("PATH")
	}
	return "PATH=" + strings.Join([]string{"/usr/bin", "/bin", "/usr/local/bin", "/opt/homebrew/bin"}, string(os.PathListSeparator))
}

// cliHomeEnv は利用者の環境（LOOPTRACK_*・IM_*・実際の資格情報・AI のセッション）を持ち込まない子プロセスの環境の土台。
// HOME・USERPROFILE・APPDATA（Windows の資格情報の置き場）・XDG_CONFIG_HOME を home の下にする。
func cliHomeEnv(home string) []string {
	// LOOPTRACK_LANG: 下の検査は日本語の文面を見る。指定が無いと CLI は端末の設定に従い、
	// 日本語でなければ英語を出す（i18n.FromEnv）ので、ここで決めておく。
	// サーバから返る文面（next の案内・コメント追記・トークンレポートなど）も同じ値で決まる。
	// internal/client/api の client.go が i18n.FromEnv の結果を Accept-Language として送るので、
	// 子プロセスを使う検査はホストの LANG に依らず日本語の文面を見られる。
	env := []string{cliPATH(), "HOME=" + home, "USERPROFILE=" + home, "APPDATA=" + filepath.Join(home, "AppData", "Roaming"),
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"), "TMPDIR=" + os.TempDir(), "TZ=JST-9", "LOOPTRACK_LANG=ja"}
	for _, k := range []string{"SYSTEMROOT", "COMSPEC", "PATHEXT"} { // Windows の起動に要るもの（internal/clitest の ChildEnv と同じ）
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// cliAPIEnv は cliHomeEnv にテストのサーバの URL・プロジェクト・トークン（空なら付けない）を足す。テストのサーバ以外は指さない。
func cliAPIEnv(apiURL, project, token, home string) []string {
	if !strings.HasPrefix(apiURL, "http://127.0.0.1:") {
		panic("テストのサーバ以外を指している: " + apiURL)
	}
	env := append(cliHomeEnv(home), "LOOPTRACK_API_URL="+apiURL, "LOOPTRACK_PROJECT="+project)
	if token != "" {
		env = append(env, "LOOPTRACK_TOKEN="+token)
	}
	return env
}

// runCLI は looptrack を dir で起動する（args は「issue list」のように looptrack の引数全体）。
func runCLI(t *testing.T, dir string, env []string, stdin string, args ...string) cliResult {
	t.Helper()
	cmd := exec.Command(looptrackBin(t), args...)
	cmd.Dir, cmd.Env = dir, env
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return cliResult{stdout.String(), stderr.String(), code}
}

// issueCLI は looptrack issue <args> を、テストのサーバ・プロジェクト・トークンで起動する関数を返す（作業ディレクトリ・HOME は
// 一時ディレクトリ。extra は足す環境変数（"LOOPTRACK_USAGE=0" など））。
func issueCLI(t *testing.T, e *env, project, token string, extra ...string) func(args ...string) cliResult {
	t.Helper()
	dir, home := t.TempDir(), t.TempDir()
	return func(args ...string) cliResult {
		t.Helper()
		env := append(append(cliAPIEnv(e.srv.URL+"/im", project, token, home), "CLAUDE_PROJECT_DIR="+dir), extra...)
		return runCLI(t, dir, env, "", append([]string{"issue"}, args...)...)
	}
}

func testutilDSNOrSkip(t *testing.T) {
	switch testutil.Dialect() {
	case "":
		t.Skip("LOOPTRACK_TEST_DB=skip のため DB を使うテストを省略")
	case testutil.DialectMySQL:
		if os.Getenv("LOOPTRACK_TEST_DSN") == "" {
			t.Skip("LOOPTRACK_TEST_DSN が未設定のため DB を使うテストを省略")
		}
	}
}
