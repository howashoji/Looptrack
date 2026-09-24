package loop

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/hookio"
)

// fakeGit は git の呼び出しを「引数の並び → 標準出力」の表で答える Env を作る（起動しない）。
// 表に無い呼び出しは終了コード 1（git の失敗と同じ＝hook は黙る）。
func fakeGit(t *testing.T, dir string, now time.Time, out map[string]string, env map[string]string) (*Env, *[]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o777); err != nil {
		t.Fatal(err)
	}
	var calls []string
	e := &Env{
		Getenv: func(k string) string {
			if k == "LOOPTRACK_LANG" {
				return "ja"
			}
			return env[k]
		},
		Environ: func() []string { return nil },
		Getwd:   func() string { return dir },
		Now:     func() time.Time { return now },
		Run: func(_ context.Context, c Command) ([]byte, int, error) {
			key := strings.Join(c.Args, " ")
			calls = append(calls, c.Name+" "+key)
			if c.Name != "git" {
				return nil, 1, nil
			}
			if v, ok := out[key]; ok {
				return []byte(v), 0, nil
			}
			return nil, 1, nil
		},
	}
	return e, &calls
}

// staleBaseGitOut は、分岐点が age だけ前にある作業ツリーの git の答え（subject は分岐点のコミットの見出し）。
func staleBaseGitOut(now time.Time, age time.Duration, subject string, behind int) map[string]string {
	base := "abc1234"
	line := base + "\t" + fmt.Sprint(now.Add(-age).Unix()) + "\t" + subject
	return map[string]string{
		"rev-parse --verify --quiet origin/main^{commit}": "0000000\n",
		"merge-base HEAD origin/main":                     "abc1234deadbeef\n",
		"log -1 --format=%h%x09%ct%x09%s abc1234deadbeef": line + "\n",
		"rev-parse --abbrev-ref HEAD":                     "s40-work\n",
		"rev-list --count abc1234deadbeef..origin/main":   fmt.Sprintf("%d\n", behind),
	}
}

// TestStaleBaseBoundary は閾値の境界（ちょうど 4 時間・3 時間 59 分・4 時間 1 分）と、既定の閾値が 4 時間であること。
// **これが「壊すと落ちる」テスト**: stalebase.go の `b.Age <= limit` を `<` や `>=` に変えると、
// 「ちょうど 4 時間」か「4 時間 1 分」のどちらかが必ず落ちる。
func TestStaleBaseBoundary(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	if DefaultStaleBaseMaxAge != 4*time.Hour {
		t.Fatalf("既定の閾値は 4 時間（利用者の決定）: %s", DefaultStaleBaseMaxAge)
	}
	for _, tc := range []struct {
		name string
		age  time.Duration
		warn bool
	}{
		{"3 時間 59 分は黙る", 3*time.Hour + 59*time.Minute, false},
		{"ちょうど 4 時間は黙る（超えたときだけ出す）", 4 * time.Hour, false},
		{"4 時間 1 分で出す", 4*time.Hour + time.Minute, true},
		{"47 時間は出す", 47 * time.Hour, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			e, _ := fakeGit(t, dir, now, staleBaseGitOut(now, tc.age, "土台のコミット", 65), nil)
			res, err := UserPromptStaleBase(WithEnv(context.Background(), e), hookio.Event{Name: hookio.UserPromptSubmit, CWD: dir})
			if err != nil {
				t.Fatal(err)
			}
			if got := res.Context != ""; got != tc.warn {
				t.Errorf("出した=%v・出すべき=%v:\n%s", got, tc.warn, res.Context)
			}
		})
	}
}

// TestStaleBaseNoticeContents は出す 1 行の中身（作業ツリー名・基点の SHA・遅れの量・fetch を打たない断り）。
// 受け入れ条件「作業ツリー名・基点の SHA・遅れの量の 3 つが出力に現れる」に当たる。
func TestStaleBaseNoticeContents(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	e, calls := fakeGit(t, dir, now, staleBaseGitOut(now, 5*time.Hour+30*time.Minute, "土台のコミット", 153), nil)
	res, err := UserPromptStaleBase(WithEnv(context.Background(), e), hookio.Event{Name: hookio.UserPromptSubmit, CWD: dir})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{
		"s40-work",         // 作業ツリー名
		"abc1234",          // 基点の SHA
		"153 コミット",         // 遅れの量
		"5 時間 30 分前",       // 基点の経過時間（分まで出す）
		"閾値 4 時間",          // 閾値
		"origin/main",      // 基準
		"遅れの下限",            // fetch を打たない既知の穴
		"git fetch origin", // 次にやること
		"「土台のコミット」",        // 分岐点のコミットの見出し
	} {
		if !strings.Contains(res.Context, w) {
			t.Errorf("%q が無い:\n%s", w, res.Context)
		}
	}
	if n := strings.Count(strings.TrimRight(res.Context, "\n"), "\n"); n != 0 {
		t.Errorf("注入は 1 行（改行 %d 個）:\n%s", n, res.Context)
	}
	// hook の中では fetch を打たない（毎ターンのネットワーク待ちを作らない）
	for _, c := range *calls {
		for _, bad := range []string{"fetch", "remote update", "pull"} {
			if strings.Contains(c, bad) {
				t.Errorf("hook が %s を打っている: %q", bad, c)
			}
		}
	}
}

// TestStaleBaseEmptyLastField は、git の書式の**最後の欄が空になる行**でも判定を落とさないこと。
//
// %s（コミットの見出し）は空のコミット（--allow-empty-message）で空になり、そのとき行末の空白として削られて
// 欄が 1 つ足りなくなる（%(upstream:track) で既に踏んだ穴と同じ形）。欄の数で弾くと、たまたま基点が
// そういうコミットだった作業ツリーで**警告が黙って出なくなる**。
func TestStaleBaseEmptyLastField(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)

	t.Run("末尾の空の欄が削られても検知する", func(t *testing.T) {
		dir := t.TempDir()
		out := staleBaseGitOut(now, 6*time.Hour, "", 100)
		// 見出しが空 → 行末のタブが空白として削られ、欄は 3 ではなく 2 になる
		out["log -1 --format=%h%x09%ct%x09%s abc1234deadbeef"] = fmt.Sprintf("abc1234\t%d\n", now.Add(-6*time.Hour).Unix())
		e, _ := fakeGit(t, dir, now, out, nil)
		res, err := UserPromptStaleBase(WithEnv(context.Background(), e), hookio.Event{Name: hookio.UserPromptSubmit, CWD: dir})
		if err != nil {
			t.Fatal(err)
		}
		if res.Context == "" {
			t.Error("見出しが空の基点で黙ってしまった（欄の数で弾いている）")
		}
		if strings.Contains(res.Context, "「」") {
			t.Errorf("空の見出しを括弧だけで出している:\n%s", res.Context)
		}
	})

	t.Run("欄を数える箇所は足りない後ろの欄を空で埋める", func(t *testing.T) {
		for _, tc := range []struct {
			in   string
			want []string
		}{
			{"a\tb\tc", []string{"a", "b", "c"}},
			{"a\tb", []string{"a", "b", ""}},             // 最後の欄が空で削られた行
			{"a", []string{"a", "", ""}},                 // 欄が 1 つしか無い行
			{"", []string{"", "", ""}},                   // 空の行
			{"a\tb\tc\td", []string{"a", "b", "c", "d"}}, // 余った欄は落とさない
		} {
			got := tabFields(tc.in, 3)
			if len(got) < len(tc.want) || strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("tabFields(%q, 3) = %q・期待 %q", tc.in, got, tc.want)
			}
		}
	})
}

// TestStaleBaseSilent は、判定できないときに 1 行も出さないこと（fail-open）。
func TestStaleBaseSilent(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		out  map[string]string
		env  map[string]string
	}{
		{"基準（origin/main・origin/master）が無ければ黙る", map[string]string{"merge-base HEAD origin/main": "abc\n"}, nil},
		{"分岐点が基準の先端なら（1 コミットも遅れていない）黙る", func() map[string]string {
			out := staleBaseGitOut(now, 40*time.Hour, "x", 0)
			out["rev-parse --verify --quiet origin/main^{commit}"] = "abc1234deadbeef\n" // 先端 = merge-base
			return out
		}(), nil},
		{"git が全部失敗しても黙る", map[string]string{}, nil},
		{"利用者が切っていれば黙る", staleBaseGitOut(now, 40*time.Hour, "x", 600), map[string]string{"LOOPTRACK_LOOP_STALE_BASE_NOTICE": "0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			e, _ := fakeGit(t, dir, now, tc.out, tc.env)
			res, err := UserPromptStaleBase(WithEnv(context.Background(), e), hookio.Event{Name: hookio.UserPromptSubmit, CWD: dir})
			if err != nil {
				t.Fatalf("エラーを返している（fail-open のはず）: %v", err)
			}
			if res.Context != "" {
				t.Errorf("黙るべきなのに出した:\n%s", res.Context)
			}
		})
	}

	t.Run("git のリポジトリでなければ黙る", func(t *testing.T) {
		dir := t.TempDir()
		e, _ := fakeGit(t, dir, now, staleBaseGitOut(now, 40*time.Hour, "x", 600), nil)
		if err := os.RemoveAll(filepath.Join(dir, ".git")); err != nil {
			t.Fatal(err)
		}
		res, err := UserPromptStaleBase(WithEnv(context.Background(), e), hookio.Event{Name: hookio.UserPromptSubmit, CWD: dir})
		if err != nil || res.Context != "" {
			t.Errorf("git でない場所で出している: err=%v ctx=%q", err, res.Context)
		}
	})
}

// TestStaleBaseEnvThreshold は閾値を環境変数で変えられること（既定 4 時間・不正な値は既定に戻す）。
func TestStaleBaseEnvThreshold(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		value string
		age   time.Duration
		warn  bool
	}{
		{"閾値を 1 時間にすると 2 時間で出る", "1h", 2 * time.Hour, true},
		{"閾値を 12 時間にすると 5 時間では出ない", "12h", 5 * time.Hour, false},
		{"読めない値は既定の 4 時間に戻す（5 時間は出る）", "いつか", 5 * time.Hour, true},
		{"読めない値は既定の 4 時間に戻す（3 時間は出ない）", "0", 3 * time.Hour, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			e, _ := fakeGit(t, dir, now, staleBaseGitOut(now, tc.age, "x", 10),
				map[string]string{"LOOPTRACK_LOOP_STALE_BASE_MAX_AGE": tc.value})
			res, err := UserPromptStaleBase(WithEnv(context.Background(), e), hookio.Event{Name: hookio.UserPromptSubmit, CWD: dir})
			if err != nil {
				t.Fatal(err)
			}
			if got := res.Context != ""; got != tc.warn {
				t.Errorf("出した=%v・出すべき=%v:\n%s", got, tc.warn, res.Context)
			}
		})
	}
}

// TestStaleBaseRealGit は実際の git のリポジトリで（古い基点は検知し、新しい基点では 1 行も出さない）。
// origin/main は refs/remotes/origin/main をそのまま作る（hook が読むのと同じ ref）。
func TestStaleBaseRealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git が無いので省略")
	}
	root := t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(dir, name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0o666); err != nil {
			t.Fatal(err)
		}
		git(dir, "add", name)
		git(dir, "commit", "-q", "-m", name)
	}
	git(root, "init", "-q", "-b", "main")
	commit(root, "base.txt") // ← ここが分岐点になる
	baseSHA := git(root, "rev-parse", "HEAD")
	baseShort := git(root, "rev-parse", "--short", "HEAD")
	baseUnix := git(root, "show", "-s", "--format=%ct", "HEAD")
	git(root, "switch", "-q", "-c", "work")
	commit(root, "work.txt") // 作業ツリー側の進み
	git(root, "switch", "-q", "main")
	commit(root, "ahead-1.txt") // 基準側の進み（2 コミット）
	commit(root, "ahead-2.txt")
	git(root, "update-ref", "refs/remotes/origin/main", git(root, "rev-parse", "HEAD"))
	git(root, "switch", "-q", "work")

	var baseTime time.Time
	if _, err := fmt.Sscan(baseUnix); err == nil {
		var sec int64
		fmt.Sscan(baseUnix, &sec)
		baseTime = time.Unix(sec, 0)
	}
	if baseTime.IsZero() {
		t.Fatalf("分岐点の日時を読めません: %q", baseUnix)
	}
	newEnv := func(now time.Time) *Env {
		return &Env{
			Getenv: func(k string) string {
				if k == "LOOPTRACK_LANG" {
					return "ja"
				}
				return ""
			},
			Environ: func() []string { return nil },
			Getwd:   func() string { return root },
			Now:     func() time.Time { return now },
			Run: func(ctx context.Context, c Command) ([]byte, int, error) {
				cmd := exec.CommandContext(ctx, c.Name, c.Args...)
				cmd.Dir = c.Dir
				out, err := cmd.Output()
				if ee, ok := err.(*exec.ExitError); ok {
					return out, ee.ExitCode(), nil
				}
				return out, 0, err
			},
		}
	}

	t.Run("基点が古ければ作業ツリー名・基点の SHA・遅れの量を出す", func(t *testing.T) {
		e := newEnv(baseTime.Add(6 * time.Hour))
		res, err := UserPromptStaleBase(WithEnv(context.Background(), e), hookio.Event{Name: hookio.UserPromptSubmit, CWD: root})
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range []string{"work", baseShort, "2 コミット", "6 時間前", "origin/main", "遅れの下限"} {
			if !strings.Contains(res.Context, w) {
				t.Errorf("%q が無い:\n%s", w, res.Context)
			}
		}
		if strings.Contains(res.Context, baseSHA) {
			t.Errorf("長い SHA をそのまま出している:\n%s", res.Context)
		}
	})

	t.Run("基点が新しければ 1 行も出ない", func(t *testing.T) {
		e := newEnv(baseTime.Add(time.Hour))
		res, err := UserPromptStaleBase(WithEnv(context.Background(), e), hookio.Event{Name: hookio.UserPromptSubmit, CWD: root})
		if err != nil {
			t.Fatal(err)
		}
		if res.Context != "" {
			t.Errorf("新しい基点で出した:\n%s", res.Context)
		}
	})

	t.Run("基準と同じところに居れば黙る（分岐点は基準の先端）", func(t *testing.T) {
		git(root, "switch", "-q", "main")
		defer git(root, "switch", "-q", "work")
		e := newEnv(baseTime.Add(6 * time.Hour))
		res, err := UserPromptStaleBase(WithEnv(context.Background(), e), hookio.Event{Name: hookio.UserPromptSubmit, CWD: root})
		if err != nil {
			t.Fatal(err)
		}
		if res.Context != "" {
			t.Errorf("基準の先端に居るのに出した:\n%s", res.Context)
		}
	})
}
