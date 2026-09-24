package loop

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/client/worktree"
	"github.com/howashoji/looptrack/internal/hookio"
	"github.com/howashoji/looptrack/internal/i18n"
)

// TestWorktreeNote は、知らせる文面の作り方（何も無ければ黙る・件数・失われかけの扱い）。
func TestWorktreeNote(t *testing.T) {
	now := time.Now()
	clean := worktree.Entry{Path: "/r/wt/done", Branch: "done", Merged: true, Age: 2 * time.Hour}
	losing := worktree.Entry{Path: "/r/wt/lost", Branch: "lost", Merged: true, Dirty: true, Changes: 12, Age: 72 * time.Hour}
	main := worktree.Entry{Path: "/r", Branch: "main", Main: true, Merged: true, Age: time.Minute}
	_ = now

	t.Run("知らせることが無ければ何も出さない", func(t *testing.T) {
		rep := &worktree.Report{Entries: []worktree.Entry{main,
			{Path: "/r/wt/busy", Branch: "busy", Merged: true, Age: time.Minute}}} // 動いたばかり＝片付けられない
		if got := worktreeNote(i18n.JA, rep, worktree.DefaultMinAge); got != "" {
			t.Errorf("黙るべきなのに出した:\n%s", got)
		}
	})

	t.Run("本体の作業ツリーは数えない", func(t *testing.T) {
		if got := worktreeNote(i18n.JA, &worktree.Report{Entries: []worktree.Entry{main}}, worktree.DefaultMinAge); got != "" {
			t.Errorf("本体を数えている:\n%s", got)
		}
	})

	t.Run("片付けられるものがあれば件数と次のコマンド", func(t *testing.T) {
		got := worktreeNote(i18n.JA, &worktree.Report{Entries: []worktree.Entry{main, clean}}, worktree.DefaultMinAge)
		for _, w := range []string{"片付けられる作業ツリー 1 件", "looptrack worktree list", "prune", "利用者に見せる"} {
			if !strings.Contains(got, w) {
				t.Errorf("%q が無い:\n%s", w, got)
			}
		}
		if strings.Contains(got, "失われかけ") {
			t.Errorf("失われかけているものは無いのに言っている:\n%s", got)
		}
	})

	t.Run("失われかけているものは名前と退避の勧めを出す", func(t *testing.T) {
		got := worktreeNote(i18n.JA, &worktree.Report{Entries: []worktree.Entry{main, losing}}, worktree.DefaultMinAge)
		for _, w := range []string{"失われかけている作業ツリー 1 件", "lost", "未コミット 12 件", "3 日", "退避", "ここで消さないこと"} {
			if !strings.Contains(got, w) {
				t.Errorf("%q が無い:\n%s", w, got)
			}
		}
	})

	t.Run("作業ツリーの無い取り込み済みのブランチも知らせる", func(t *testing.T) {
		rep := &worktree.Report{Entries: []worktree.Entry{main}, Branches: []worktree.Branch{
			{Name: "old-1", Merged: true}, {Name: "old-2", Merged: true}, {Name: "alive", Merged: false}}}
		got := worktreeNote(i18n.JA, rep, worktree.DefaultMinAge)
		if !strings.Contains(got, "ブランチ 2 件") {
			t.Errorf("取り込み済みのブランチだけを数えていない:\n%s", got)
		}
	})

	t.Run("失われかけが多いときは 3 件までにする", func(t *testing.T) {
		rep := &worktree.Report{Entries: []worktree.Entry{main}}
		for _, n := range []string{"a", "b", "c", "d", "e"} {
			rep.Entries = append(rep.Entries, worktree.Entry{Path: "/r/wt/" + n, Branch: n, Merged: true, Dirty: true, Changes: 1, Age: 48 * time.Hour})
		}
		got := worktreeNote(i18n.JA, rep, worktree.DefaultMinAge)
		if !strings.Contains(got, "…ほか 2 件") {
			t.Errorf("長い一覧を畳んでいない:\n%s", got)
		}
	})
}

// TestSessionStartWorktreesHook は hook 全体（実際の git のリポジトリで）。
// **何も消さない**ことと、git のリポジトリでなければ黙ることを確かめる。
func TestSessionStartWorktreesHook(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git が無いので省略")
	}
	root := t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	git(root, "init", "-q", "-b", "main")
	git(root, "config", "user.email", "t@example.invalid")
	git(root, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	git(root, "add", "a.txt")
	git(root, "commit", "-q", "-m", "init")
	wt := filepath.Join(root, "wt", "done")
	git(root, "worktree", "add", "-q", "-b", "done", wt)
	if err := os.WriteFile(filepath.Join(wt, "b.txt"), []byte("b\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	git(wt, "add", "b.txt")
	git(wt, "commit", "-q", "-m", "done")
	git(root, "merge", "-q", "--no-ff", "-m", "merge done", "done")
	// 取り込み済みで、しばらく動いていないことにする
	old := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(filepath.Join(root, ".git", "worktrees", "done", "index"), old, old); err != nil {
		t.Fatal(err)
	}

	var calls []string
	e := &Env{
		Getenv: func(k string) string {
			if k == "LOOPTRACK_LANG" {
				return "ja"
			}
			return ""
		},
		Environ: func() []string { return nil },
		Getwd:   func() string { return root },
		Run: func(ctx context.Context, c Command) ([]byte, int, error) {
			calls = append(calls, c.Name+" "+strings.Join(c.Args, " "))
			cmd := exec.CommandContext(ctx, c.Name, c.Args...)
			cmd.Dir = c.Dir
			out, err := cmd.Output()
			if ee, ok := err.(*exec.ExitError); ok {
				return out, ee.ExitCode(), nil
			}
			return out, 0, err
		},
	}
	ctx := WithEnv(context.Background(), e)
	res, err := SessionStartWorktrees(ctx, hookio.Event{Name: hookio.SessionStart, CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Context, "片付けられる作業ツリー 1 件") {
		t.Errorf("片付けられるものを知らせていない:\n%s", res.Context)
	}

	// 何も消さない（読むだけの git しか打たない）
	for _, c := range calls {
		for _, bad := range []string{"worktree remove", "worktree prune", "branch -d", "branch -D", "clean", "reset"} {
			if strings.Contains(c, bad) {
				t.Errorf("hook が消す操作を打っている: %q", c)
			}
		}
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("作業ツリーが消えている: %v", err)
	}

	t.Run("git のリポジトリでなければ黙る", func(t *testing.T) {
		plain := t.TempDir()
		e2 := *e
		e2.Getwd = func() string { return plain }
		res, err := SessionStartWorktrees(WithEnv(context.Background(), &e2), hookio.Event{Name: hookio.SessionStart, CWD: plain})
		if err != nil {
			t.Fatalf("エラーを返している（fail-open のはず）: %v", err)
		}
		if res.Context != "" {
			t.Errorf("何か出している:\n%s", res.Context)
		}
	})

	t.Run("git が起動できなくても黙る", func(t *testing.T) {
		e3 := *e
		e3.Run = func(context.Context, Command) ([]byte, int, error) { return nil, 127, nil }
		res, err := SessionStartWorktrees(WithEnv(context.Background(), &e3), hookio.Event{Name: hookio.SessionStart, CWD: root})
		if err != nil || res.Context != "" {
			t.Errorf("git が失敗したのに出している: err=%v ctx=%q", err, res.Context)
		}
	})
}
