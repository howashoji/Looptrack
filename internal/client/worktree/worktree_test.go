package worktree

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/i18n"
)

// repo は一時ディレクトリの git リポジトリ（本体 + 作業ツリー）。
type repo struct {
	t      *testing.T
	root   string
	remote string // publish が作る bare のリモート（最初の publish まで空）
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git が無いので省略")
	}
	r := &repo{t: t, root: t.TempDir()}
	r.git(r.root, "init", "-q", "-b", "main")
	r.git(r.root, "config", "user.email", "t@example.invalid")
	r.git(r.root, "config", "user.name", "t")
	r.write("README.md", "hello\n")
	r.git(r.root, "add", "README.md")
	r.git(r.root, "commit", "-q", "-m", "init")
	return r
}

func (r *repo) git(dir string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s（%s）: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

func (r *repo) write(rel, body string) {
	r.t.Helper()
	p := filepath.Join(r.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o777); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o666); err != nil {
		r.t.Fatal(err)
	}
}

// addWorktree は <root>/wt/<名前> に作業ツリーを足す。merged なら中身を main に取り込む。
func (r *repo) addWorktree(name string, merged bool) string {
	r.t.Helper()
	path := filepath.Join(r.root, "wt", name)
	r.git(r.root, "worktree", "add", "-q", "-b", name, path)
	if err := os.WriteFile(filepath.Join(path, name+".txt"), []byte(name+"\n"), 0o666); err != nil {
		r.t.Fatal(err)
	}
	r.git(path, "add", ".")
	r.git(path, "commit", "-q", "-m", name)
	if merged {
		r.git(r.root, "merge", "-q", "--no-ff", "-m", "merge "+name, name)
	}
	return path
}

// publish は bare のリモートへブランチを push して追い先を設定する（リモートに公開された常設のブランチを作る）。
func (r *repo) publish(branch string) {
	r.t.Helper()
	if r.remote == "" {
		r.remote = filepath.Join(r.t.TempDir(), "origin.git")
		r.git(r.root, "init", "-q", "--bare", r.remote)
		r.git(r.root, "remote", "add", "origin", r.remote)
	}
	r.git(r.root, "push", "-q", "-u", "origin", branch)
}

// unpublish はリモートのブランチだけ消す（追い先が gone のローカルブランチを作る）。
func (r *repo) unpublish(branch string) {
	r.t.Helper()
	r.git(r.root, "push", "-q", "origin", "--delete", branch)
	r.git(r.root, "fetch", "-q", "--prune", "origin")
}

// mark は作業ツリーに印を書く（最後に動いていた時刻を作る）。
func (r *repo) mark(name, session string, at time.Time) {
	r.t.Helper()
	dir := filepath.Join(r.root, ".git", "worktrees", name)
	b, _ := json.Marshal(map[string]string{"session": session, "at": at.UTC().Format(time.RFC3339)})
	if err := os.WriteFile(filepath.Join(dir, SessionFile), b, 0o600); err != nil {
		r.t.Fatal(err)
	}
}

func (r *repo) list(o Options) *Report {
	r.t.Helper()
	o.Dir = r.root
	rep, err := List(o)
	if err != nil {
		r.t.Fatal(err)
	}
	return rep
}

func find(t *testing.T, rep *Report, branch string) Entry {
	t.Helper()
	for _, e := range rep.Entries {
		if e.Branch == branch {
			return e
		}
	}
	t.Fatalf("作業ツリーが見つかりません: %s（%d 件）", branch, len(rep.Entries))
	return Entry{}
}

// TestListJudges は一覧の判定: 取り込み済み・未取り込み・未コミット・locked・生成物だけ・本体。
func TestListJudges(t *testing.T) {
	r := newRepo(t)
	old := time.Now().Add(-48 * time.Hour)
	r.addWorktree("done", true)  // 取り込み済み・きれい → 片付けられる
	r.addWorktree("open", false) // まだ取り込まれていない → 残す
	dirty := r.addWorktree("dirty", true)
	junkWT := r.addWorktree("junky", true)
	locked := r.addWorktree("locked", true)
	if err := os.WriteFile(filepath.Join(dirty, "work.txt"), []byte("途中\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(junkWT, "sub", "__pycache__"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(junkWT, "sub", "__pycache__", "x.pyc"), []byte("x"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(junkWT, ".DS_Store"), []byte("x"), 0o666); err != nil {
		t.Fatal(err)
	}
	r.git(r.root, "worktree", "lock", locked, "--reason", "実機の確認に使う")
	for _, n := range []string{"done", "open", "dirty", "junky", "locked"} {
		r.mark(n, "sess-"+n, old) // すべて 2 日前に動いていたことにする
	}

	rep := r.list(Options{})
	if rep.Base != "main" {
		t.Errorf("取り込み先のブランチ: %q", rep.Base)
	}

	t.Run("本体は消さない", func(t *testing.T) {
		var main Entry
		for _, e := range rep.Entries {
			if e.Main {
				main = e
			}
		}
		if main.Path == "" {
			t.Fatal("本体の作業ツリーが一覧に無い")
		}
		if ok, why := main.Removable(i18n.JA, DefaultMinAge); ok || why != "本体の作業ツリー" {
			t.Errorf("本体を消す判定にしている: ok=%v why=%q", ok, why)
		}
	})

	t.Run("取り込み済みで変更が無ければ片付けられる", func(t *testing.T) {
		e := find(t, rep, "done")
		if ok, why := e.Removable(i18n.JA, DefaultMinAge); !ok {
			t.Errorf("片付けられるはず: %q", why)
		}
		if e.Session != "sess-done" {
			t.Errorf("印のセッションが一覧に出ない: %q", e.Session)
		}
		if e.TouchFrom != TouchMark {
			t.Errorf("最後に動いていた時刻の出どころ: %q", e.TouchFrom)
		}
	})

	t.Run("取り込まれていなければ残す", func(t *testing.T) {
		ok, why := find(t, rep, "open").Removable(i18n.JA, DefaultMinAge)
		if ok || !strings.Contains(why, "取り込まれていません") {
			t.Errorf("ok=%v why=%q", ok, why)
		}
	})

	t.Run("未コミットがあれば残し、古ければ失われかけていると言う", func(t *testing.T) {
		e := find(t, rep, "dirty")
		ok, why := e.Removable(i18n.JA, DefaultMinAge)
		if ok || !strings.Contains(why, "未コミット") {
			t.Errorf("ok=%v why=%q", ok, why)
		}
		if !e.Losing() {
			t.Errorf("2 日前の未コミットは「失われかけている」にする: age=%v dirty=%v", e.Age, e.Dirty)
		}
	})

	t.Run("生成物だけの残骸は未コミットと数えない", func(t *testing.T) {
		e := find(t, rep, "junky")
		if e.Dirty {
			t.Errorf("__pycache__ と .DS_Store だけで「未コミットあり」にしている（%d 件）", e.Changes)
		}
		if !e.JunkOnly {
			t.Error("生成物だけの残骸だと分かっていない")
		}
		if ok, why := e.Removable(i18n.JA, DefaultMinAge); !ok {
			t.Errorf("片付けられるはず: %q", why)
		}
		if e.Losing() {
			t.Error("生成物だけの残骸を「失われかけている」と言っている")
		}
	})

	t.Run("locked は消さない", func(t *testing.T) {
		e := find(t, rep, "locked")
		ok, why := e.Removable(i18n.JA, DefaultMinAge)
		if ok || !strings.Contains(why, "locked") {
			t.Errorf("ok=%v why=%q", ok, why)
		}
		if !strings.Contains(why, "実機の確認に使う") {
			t.Errorf("locked の理由を出していない: %q", why)
		}
	})
}

// TestRecentlyTouched は「最近まで動いていた作業ツリーは、ほかのセッションのものかもしれないので消さない」。
func TestRecentlyTouched(t *testing.T) {
	r := newRepo(t)
	r.addWorktree("busy", true)
	r.mark("busy", "sess-busy", time.Now().Add(-5*time.Minute))
	e := find(t, r.list(Options{}), "busy")
	if ok, why := e.Removable(i18n.JA, 30*time.Minute); ok {
		t.Errorf("5 分前まで動いていたものを消す判定にしている: %q", why)
	}
	if ok, _ := e.Removable(i18n.JA, 1*time.Minute); !ok {
		t.Error("--min-age を短くすれば片付けられるはず")
	}
}

// TestBranchesWithoutWorktree は、作業ツリーが無くなった後に残るブランチも見えること。
func TestBranchesWithoutWorktree(t *testing.T) {
	r := newRepo(t)
	p := r.addWorktree("gone", true)
	r.addWorktree("stay", true)
	r.git(r.root, "worktree", "remove", p) // 作業ツリーだけ消す（ブランチは残る）

	rep := r.list(Options{})
	var got *Branch
	for i := range rep.Branches {
		if rep.Branches[i].Name == "gone" {
			got = &rep.Branches[i]
		}
	}
	if got == nil {
		t.Fatalf("作業ツリーの無いブランチが一覧に出ない: %+v", rep.Branches)
	}
	if !got.Merged {
		t.Error("取り込み済みと分かっていない")
	}
	for _, b := range rep.Branches {
		if b.Name == "stay" {
			t.Error("作業ツリーのあるブランチを「作業ツリーが無い」に数えている")
		}
		if b.Name == "main" {
			t.Error("取り込み先のブランチを片付けの対象に数えている")
		}
	}
}

// TestPruneIsDryRunByDefault は、prune が --yes なしでは何も消さないこと。
func TestPruneIsDryRunByDefault(t *testing.T) {
	r := newRepo(t)
	path := r.addWorktree("done", true)
	r.mark("done", "s", time.Now().Add(-2*time.Hour))

	var out, errb bytes.Buffer
	if code := Main([]string{"prune", "--dir", r.root}, &out, &errb, env.FromMap(map[string]string{"LOOPTRACK_LANG": "ja"})); code != 0 {
		t.Fatalf("終了コード %d: %s", code, errb.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("--yes が無いのに消している: %v", err)
	}
	s := out.String()
	for _, w := range []string{"まだ何も消していません", "--yes", "done"} {
		if !strings.Contains(s, w) {
			t.Errorf("出力に %q が無い:\n%s", w, s)
		}
	}
}

// TestPruneYes は --yes で片付けること（消してよいものだけ）。
func TestPruneYes(t *testing.T) {
	r := newRepo(t)
	done := r.addWorktree("done", true)
	open := r.addWorktree("open", false)
	dirty := r.addWorktree("dirty", true)
	if err := os.WriteFile(filepath.Join(dirty, "work.txt"), []byte("途中\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"done", "open", "dirty"} {
		r.mark(n, "s", time.Now().Add(-2*time.Hour))
	}

	var out, errb bytes.Buffer
	if code := Main([]string{"prune", "--yes", "--dir", r.root}, &out, &errb, env.FromMap(map[string]string{"LOOPTRACK_LANG": "ja"})); code != 0 {
		t.Fatalf("終了コード %d: %s\n%s", code, errb.String(), out.String())
	}
	if _, err := os.Stat(done); err == nil {
		t.Error("取り込み済みで変更の無い作業ツリーが消えていない")
	}
	for _, p := range []string{open, dirty} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("消してはいけない作業ツリーを消した: %s", p)
		}
	}
	// 取り込み済みのブランチも一緒に消える。取り込まれていないブランチは残る
	branches := r.git(r.root, "branch", "--format=%(refname:short)")
	if strings.Contains(branches, "done") {
		t.Errorf("取り込み済みのブランチが残っている: %s", branches)
	}
	for _, b := range []string{"open", "dirty", "main"} {
		if !strings.Contains(branches, b) {
			t.Errorf("消してはいけないブランチ %s を消した: %s", b, branches)
		}
	}
}

// TestMarkSkipsMain は、本体の作業ツリーには印を付けないこと（複数のセッションが同時に使うため）。
func TestMarkSkipsMain(t *testing.T) {
	r := newRepo(t)
	p, err := Mark(Options{Dir: r.root}, "sess-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if p != "" {
		t.Errorf("本体に印を付けた: %s", p)
	}
	if _, err := os.Stat(filepath.Join(r.root, ".git", SessionFile)); err == nil {
		t.Error("本体の .git に印のファイルができている")
	}

	wt := r.addWorktree("sub", true)
	p, err = Mark(Options{Dir: wt, Now: func() time.Time { return time.Unix(1700000000, 0) }}, "sess-2", false)
	if err != nil {
		t.Fatal(err)
	}
	if p == "" {
		t.Fatal("副の作業ツリーに印を付けていない")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("印が JSON でない: %s", b)
	}
	if m["session"] != "sess-2" || m["at"] == "" {
		t.Errorf("印の中身: %v", m)
	}
	e := find(t, r.list(Options{}), "sub")
	if e.Session != "sess-2" || e.TouchFrom != TouchMark {
		t.Errorf("印が一覧に出ない: session=%q from=%q", e.Session, e.TouchFrom)
	}
}

// TestMarkConflict は、別のセッションの印がある作業ツリーへの mark を、既定では警告なく上書きしないこと。
// 同じセッションの再 mark（同じセッションが動いている間、何度も呼ぶ）はいつでも通り、force なら別のセッションでも上書きする。
func TestMarkConflict(t *testing.T) {
	r := newRepo(t)
	wt := r.addWorktree("sub", true)
	r.mark("sub", "sess-1", time.Now().Add(-time.Hour))

	// 別のセッションからの mark は、既定では拒否する（既存の印は変わらない）
	if _, err := Mark(Options{Dir: wt}, "sess-2", false); err == nil {
		t.Fatal("別のセッションの印を警告なく上書きした")
	}
	b, err := os.ReadFile(filepath.Join(r.root, ".git", "worktrees", "sub", SessionFile))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["session"] != "sess-1" {
		t.Errorf("拒否したはずなのに印が変わった: %v", m)
	}

	// 同じセッションの再 mark はいつでも通る
	p, err := Mark(Options{Dir: wt, Now: func() time.Time { return time.Unix(1700000100, 0) }}, "sess-1", false)
	if err != nil {
		t.Fatalf("同じセッションの再 mark が失敗した: %v", err)
	}
	if p == "" {
		t.Fatal("再 mark で印のパスが空")
	}

	// force なら別のセッションでも上書きする
	p, err = Mark(Options{Dir: wt, Now: func() time.Time { return time.Unix(1700000200, 0) }}, "sess-2", true)
	if err != nil {
		t.Fatalf("force での上書きが失敗した: %v", err)
	}
	b, err = os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	m = nil
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["session"] != "sess-2" {
		t.Errorf("force で上書きされていない: %v", m)
	}
}

// TestMarkConflictBrokenFile は、既存の印が JSON として読めない（壊れている）ときも、
// 別のセッションの印があるときと同じく、既定では上書きしないこと（誰のものか分からない印は安全側で扱う）。
func TestMarkConflictBrokenFile(t *testing.T) {
	r := newRepo(t)
	wt := r.addWorktree("sub2", true)
	dir := filepath.Join(r.root, ".git", "worktrees", "sub2")
	if err := os.WriteFile(filepath.Join(dir, SessionFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Mark(Options{Dir: wt}, "sess-1", false); err == nil {
		t.Fatal("壊れた印を警告なく上書きした")
	}
	if _, err := Mark(Options{Dir: wt}, "sess-1", true); err != nil {
		t.Fatalf("force での上書きが失敗した: %v", err)
	}
}

// TestListDoesNotTouch は、調べるだけで「最後に動いていた時刻」が動かないこと。
// git status は index の記録を書き戻すことがあるので、調べた側の操作で「いま動いた」ことにしてはいけない。
func TestListDoesNotTouch(t *testing.T) {
	r := newRepo(t)
	r.addWorktree("idle", true)
	idx := filepath.Join(r.root, ".git", "worktrees", "idle", "index")
	want := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(idx, want, want); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // 2 回調べても動かない
		e := find(t, r.list(Options{}), "idle")
		if e.TouchFrom != TouchIndex {
			t.Fatalf("出どころ: %q", e.TouchFrom)
		}
		if d := e.Age; d < 2*time.Hour {
			t.Fatalf("%d 回目で最終更新が動いた（%v）。調べるだけで index を書き換えている", i+1, d)
		}
	}
}

// TestIsJunk は生成物の見分け。
func TestIsJunk(t *testing.T) {
	for _, p := range []string{"__pycache__/x.pyc", "a/b/node_modules/c.js", ".DS_Store", "a/.venv/bin/x", "x.pyc"} {
		if !isJunk(p) {
			t.Errorf("生成物と見なすべき: %s", p)
		}
	}
	for _, p := range []string{"main.go", "docs/guide/README.md", "tools/hookprobe/main.go", "a/pycache/x"} {
		if isJunk(p) {
			t.Errorf("生成物ではない: %s", p)
		}
	}
}

// TestPublishedWorktreeIsKept は、リモートに公開されているブランチの作業ツリーを、
// 取り込み済み・きれい・古くても片付けないこと。
//
// 実例: 本番の配置に使う常設の作業ツリー（ブランチ server-app）が、未追跡の生成物しか無かったために
// 「取り込み済み・変更なし」と判定され、prune の対象に入っていた。
func TestPublishedWorktreeIsKept(t *testing.T) {
	r := newRepo(t)
	path := r.addWorktree("deploy", true) // 取り込み済み・きれい
	r.publish("deploy")
	r.mark("deploy", "s", time.Now().Add(-48*time.Hour)) // 古い

	rep := r.list(Options{})
	e := find(t, rep, "deploy")
	if !e.Published {
		t.Fatalf("リモートを追うブランチと分かっていない: %+v", e)
	}
	ok, why := e.Removable(i18n.JA, DefaultMinAge)
	if ok {
		t.Fatal("リモートに公開されている作業ツリーを片付けの対象にしている")
	}
	if !strings.Contains(why, "リモートに公開") {
		t.Errorf("残す理由が分かりにくい: %q", why)
	}

	// prune --yes でも残ること（実害そのものの再現）
	var out, errb bytes.Buffer
	if code := Main([]string{"prune", "--yes", "--dir", r.root, "--min-age", "1ms"}, &out, &errb, env.FromMap(map[string]string{"LOOPTRACK_LANG": "ja"})); code != 0 {
		t.Fatalf("終了コード %d: %s", code, errb.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("リモートに公開されている作業ツリーを消している: %v", err)
	}
}

// TestJunkOnlyPublishedWorktreeIsKept は、未追跡の生成物だけがある公開済みの作業ツリーも片付けないこと。
// 実際にこの片付けの対象に入っていたのは、この形（__pycache__ だけが残っている常設の作業ツリー）。
func TestJunkOnlyPublishedWorktreeIsKept(t *testing.T) {
	r := newRepo(t)
	path := r.addWorktree("deploy", true)
	r.publish("deploy")
	if err := os.MkdirAll(filepath.Join(path, "scripts", "__pycache__"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "scripts", "__pycache__", "x.pyc"), []byte("x"), 0o666); err != nil {
		t.Fatal(err)
	}
	r.mark("deploy", "s", time.Now().Add(-48*time.Hour))

	e := find(t, r.list(Options{}), "deploy")
	if !e.JunkOnly {
		t.Fatalf("生成物だけの残骸と分かっていない: %+v", e)
	}
	if ok, _ := e.Removable(i18n.JA, DefaultMinAge); ok {
		t.Fatal("生成物だけの残骸しか無い公開済みの作業ツリーを片付けの対象にしている")
	}
}

// TestGoneUpstreamIsRemovable は、追い先が消えたブランチ（PR のマージ後など）は
// ふつうの一時のブランチとして片付けられること。
func TestGoneUpstreamIsRemovable(t *testing.T) {
	r := newRepo(t)
	r.addWorktree("feature", true)
	r.publish("feature")
	r.unpublish("feature")
	r.mark("feature", "s", time.Now().Add(-48*time.Hour))

	e := find(t, r.list(Options{}), "feature")
	if e.Published {
		t.Fatal("追い先が消えている（gone）のに公開されていると見ている")
	}
	if ok, why := e.Removable(i18n.JA, DefaultMinAge); !ok {
		t.Fatalf("片付けられるはずが残している: %s", why)
	}
}

// TestPublishedBranchWithoutWorktreeIsKept は、作業ツリーの無いブランチも、
// リモートに公開されていれば取り込み済みでも消さないこと。
func TestPublishedBranchWithoutWorktreeIsKept(t *testing.T) {
	r := newRepo(t)
	p := r.addWorktree("shared", true)
	r.publish("shared")
	r.git(r.root, "worktree", "remove", p) // 作業ツリーだけ消す（ブランチは残る）

	rep := r.list(Options{})
	var got *Branch
	for i := range rep.Branches {
		if rep.Branches[i].Name == "shared" {
			got = &rep.Branches[i]
		}
	}
	if got == nil {
		t.Fatalf("作業ツリーの無いブランチが一覧に出ない: %+v", rep.Branches)
	}
	if !got.Published {
		t.Fatal("リモートに公開されていると分かっていない")
	}
	if ok, _ := got.Removable(i18n.JA); ok {
		t.Fatal("リモートに公開されているブランチを片付けの対象にしている")
	}

	var out, errb bytes.Buffer
	if code := Main([]string{"prune", "--yes", "--dir", r.root, "--min-age", "1ms"}, &out, &errb, env.FromMap(map[string]string{"LOOPTRACK_LANG": "ja"})); code != 0 {
		t.Fatalf("終了コード %d: %s", code, errb.String())
	}
	if !strings.Contains(r.git(r.root, "branch", "--list", "shared"), "shared") {
		t.Fatal("リモートに公開されているブランチを消している")
	}
}
