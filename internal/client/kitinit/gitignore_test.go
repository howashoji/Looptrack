package kitinit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// isolateGit は git の子プロセスが利用者の設定（グローバルの excludes＝core.excludesFile・~/.gitconfig・システムの設定）に
// 左右されないようにする。gitIgnores は os.Environ() を引き継ぐので、ここで環境変数を差し替えれば足りる。
// 左右されると、利用者の環境によって「無視済み」の答えが変わり、テストが揺れる。
func isolateGit(t *testing.T) (globalConfig string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git が無い")
	}
	home := t.TempDir()
	empty := filepath.Join(home, "gitconfig-empty")
	write(t, empty, "")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("USERPROFILE", home)
	t.Setenv("GIT_CONFIG_GLOBAL", empty)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, k := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	return empty
}

const freshnessLine = ".claude/.looptrack-freshness/"

// TestFreshnessGitignoreSkipsWhenAlreadyIgnored は、下位の .gitignore（.claude/.gitignore）が状態の置き場を
// 既に無視していれば、直下の .gitignore に足さないことを確かめる。同じ中で、下位の .gitignore が無いときは足す対照と、
// git の作業ツリーでないときは直下の行だけを見る従来どおりの対照を持つ。
func TestFreshnessGitignoreSkipsWhenAlreadyIgnored(t *testing.T) {
	globalConfig := isolateGit(t)
	stubs(t, true)

	gitInit := func(t *testing.T, ws string) {
		t.Helper()
		if out, err := exec.Command("git", "-C", ws, "init", "-q").CombinedOutput(); err != nil {
			t.Fatalf("git init: %v\n%s", err, out)
		}
	}

	t.Run("下位の .gitignore で無視済みなら計画にも書き込みにも入れない", func(t *testing.T) {
		root := newRoot(t)
		ws := filepath.Join(root, "ws")
		gitInit(t, ws)
		write(t, filepath.Join(ws, ".claude", ".gitignore"), ".looptrack-freshness/\n")
		// 前提: git がそのパスを無視済みと答える（ここが崩れたら、下の「足さない」は git の失敗でも通ってしまう）
		if !gitIgnores(ws, freshnessLine+"probe") {
			t.Fatalf("前提が崩れています: git check-ignore が .claude/.gitignore の行を無視済みと答えません")
		}
		dry := runInit(t, root, "", "--project", "demo", "--agent", "claude-code", "--no-loop", "--dry-run")
		if dry.code != 0 {
			t.Fatalf("dry-run: %d\n%s", dry.code, dry.stderr)
		}
		if strings.Contains(dry.stdout, "+"+freshnessLine) {
			t.Errorf("dry-run の計画に直下の .gitignore への追記が入っています:\n%s", dry.stdout)
		}
		if r := runInit(t, root, "", "--project", "demo", "--agent", "claude-code", "--no-loop"); r.code != 0 {
			t.Fatalf("init: %d\n%s", r.code, r.stderr)
		}
		if lexists(filepath.Join(ws, ".gitignore")) {
			t.Errorf("直下の .gitignore が書かれています:\n%s", read(t, filepath.Join(ws, ".gitignore")))
		}
	})

	t.Run("対照: 下位の .gitignore が無ければ従来どおり直下に足す", func(t *testing.T) {
		root := newRoot(t)
		ws := filepath.Join(root, "ws")
		gitInit(t, ws)
		if gitIgnores(ws, freshnessLine+"probe") {
			t.Fatalf("前提が崩れています: 何も無視していないのに無視済みと答えました")
		}
		dry := runInit(t, root, "", "--project", "demo", "--agent", "claude-code", "--no-loop", "--dry-run")
		if dry.code != 0 || !strings.Contains(dry.stdout, "+"+freshnessLine) {
			t.Errorf("dry-run の計画に直下の .gitignore への追記が入っていません: %d\n%s\n%s", dry.code, dry.stdout, dry.stderr)
		}
		if r := runInit(t, root, "", "--project", "demo", "--agent", "claude-code", "--no-loop"); r.code != 0 {
			t.Fatalf("init: %d\n%s", r.code, r.stderr)
		}
		if got := read(t, filepath.Join(ws, ".gitignore")); got != freshnessLine+"\n" {
			t.Errorf("直下の .gitignore = %q", got)
		}
	})

	t.Run("git の作業ツリーでなければ従来どおり直下の行を見て足す", func(t *testing.T) {
		root := newRoot(t)
		ws := filepath.Join(root, "ws")
		// 下位の .gitignore はあるが、git の作業ツリーではないので check-ignore は失敗（128）して効かない
		write(t, filepath.Join(ws, ".claude", ".gitignore"), ".looptrack-freshness/\n")
		if gitIgnores(ws, freshnessLine+"probe") {
			t.Fatalf("前提が崩れています: 作業ツリーでないのに無視済みと答えました（一時ディレクトリが git の中にありませんか）")
		}
		if r := runInit(t, root, "", "--project", "demo", "--agent", "claude-code", "--no-loop"); r.code != 0 {
			t.Fatalf("init: %d\n%s", r.code, r.stderr)
		}
		if got := read(t, filepath.Join(ws, ".gitignore")); got != freshnessLine+"\n" {
			t.Errorf("直下の .gitignore = %q", got)
		}
	})

	// plainIgnored は、素の `git check-ignore -q`（終了コードだけ）が無視済みと答えるか。
	// 下の 2 つは「その clone にしか効かない無視」なので、素の答えは無視済みなのに足す、が正しい。
	plainIgnored := func(ws string) bool {
		return exec.Command("git", "-C", ws, "check-ignore", "-q", "--", freshnessLine+"probe").Run() == nil
	}
	notShared := func(t *testing.T, name string, setup func(t *testing.T, ws string)) {
		t.Run(name, func(t *testing.T) {
			root := newRoot(t)
			ws := filepath.Join(root, "ws")
			gitInit(t, ws)
			setup(t, ws)
			// 前提: 素の check-ignore は無視済みと答える（これが崩れると、下の「足す」は何も確かめない）
			if !plainIgnored(ws) {
				t.Fatalf("前提が崩れています: 素の git check-ignore が無視済みと答えません")
			}
			if gitIgnores(ws, freshnessLine+"probe") {
				t.Errorf("gitIgnores が、その clone にしか効かない無視を「無視済み」と答えました")
			}
			if r := runInit(t, root, "", "--project", "demo", "--agent", "claude-code", "--no-loop"); r.code != 0 {
				t.Fatalf("init: %d\n%s", r.code, r.stderr)
			}
			if got := read(t, filepath.Join(ws, ".gitignore")); got != freshnessLine+"\n" {
				t.Errorf("直下の .gitignore = %q", got)
			}
		})
	}
	notShared(t, ".git/info/exclude だけで無視されていれば従来どおり直下に足す", func(t *testing.T, ws string) {
		f, err := os.OpenFile(filepath.Join(ws, ".git", "info", "exclude"), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		f.WriteString(".looptrack-freshness/\n")
	})
	notShared(t, "グローバルの core.excludesFile だけで無視されていれば従来どおり直下に足す", func(t *testing.T, ws string) {
		// よくある core.excludesFile = ~/.gitignore（絶対パスで、ファイル名も .gitignore）。これを防ぐのは絶対パスの検査だけ
		ex := filepath.Join(filepath.Dir(globalConfig), ".gitignore")
		write(t, ex, ".looptrack-freshness/\n")
		t.Cleanup(func() { write(t, globalConfig, "") }) // 後ろの部分テストに漏らさない
		write(t, globalConfig, "[core]\n\texcludesFile = "+filepath.ToSlash(ex)+"\n")
	})

	notShared(t, "作業ツリーの中の別名のファイルを core.excludesFile に相対パスで指しても足す（名前の検査）", func(t *testing.T, ws string) {
		write(t, filepath.Join(ws, "ex.txt"), ".looptrack-freshness/\n")
		t.Cleanup(func() { write(t, globalConfig, "") })
		write(t, globalConfig, "[core]\n\texcludesFile = ex.txt\n")
	})
	notShared(t, "core.excludesFile が ../.gitignore のような作業ツリーの外の相対パスでも足す", func(t *testing.T, ws string) {
		write(t, filepath.Join(filepath.Dir(ws), ".gitignore"), ".looptrack-freshness/\n")
		t.Cleanup(func() { write(t, globalConfig, "") })
		write(t, globalConfig, "[core]\n\texcludesFile = ../.gitignore\n")
	})

	for _, c := range []struct{ name, rel string }{
		{"./../.gitignore", "./../.gitignore"},
		{"x/../../.gitignore", "x/../../.gitignore"},
	} {
		notShared(t, "core.excludesFile が "+c.name+" のように字面に ../ を含む相対パスでも足す", func(t *testing.T, ws string) {
			os.MkdirAll(filepath.Join(ws, "x"), 0o755) // x/.. をたどれるように
			write(t, filepath.Join(filepath.Dir(ws), ".gitignore"), ".looptrack-freshness/\n")
			t.Cleanup(func() { write(t, globalConfig, "") })
			write(t, globalConfig, "[core]\n\texcludesFile = "+c.rel+"\n")
		})
	}

	t.Run("導入先のパスに日本語が入っていても、下位の .gitignore で無視済みなら足さない", func(t *testing.T) {
		repo := newRoot(t)
		gitInit(t, repo)
		root := filepath.Join(repo, "日本語")
		ws := filepath.Join(root, "ws")
		os.MkdirAll(ws, 0o755)
		write(t, filepath.Join(ws, ".claude", ".gitignore"), ".looptrack-freshness/\n")
		// 前提: -z なしの出力では source が引用される（-z を外すと読みが崩れる形になっている）
		plain, err := exec.Command("git", "-C", ws, "check-ignore", "-v", "--no-index", "--", freshnessLine+"probe").Output()
		if err != nil || !strings.HasPrefix(string(plain), `"`) {
			t.Fatalf("前提が崩れています: source が引用されていません: %v %q", err, plain)
		}
		if !gitIgnores(ws, freshnessLine+"probe") {
			t.Fatalf("日本語のディレクトリの下で、無視済みと答えません")
		}
		if r := runInit(t, root, "", "--project", "demo", "--agent", "claude-code", "--no-loop"); r.code != 0 {
			t.Fatalf("init: %d\n%s", r.code, r.stderr)
		}
		if lexists(filepath.Join(ws, ".gitignore")) {
			t.Errorf("直下の .gitignore が書かれています:\n%s", read(t, filepath.Join(ws, ".gitignore")))
		}
	})

	t.Run("否定（!）に当たるときは無視済みと数えない", func(t *testing.T) {
		root := newRoot(t)
		ws := filepath.Join(root, "ws")
		gitInit(t, ws)
		write(t, filepath.Join(ws, ".claude", ".gitignore"), "probe\n!probe\n")
		// 前提: -v は否定に当たっても終了コード 0 で、pattern に ! が付く（対象の規則を実際に通っている）
		out, err := exec.Command("git", "-C", ws, "check-ignore", "-v", "--no-index", "--", freshnessLine+"probe").Output()
		if err != nil || !strings.Contains(string(out), ":!probe\t") {
			t.Fatalf("前提が崩れています: %v %q", err, out)
		}
		if gitIgnores(ws, freshnessLine+"probe") {
			t.Errorf("否定に当たるのに無視済みと答えました")
		}
	})
}
