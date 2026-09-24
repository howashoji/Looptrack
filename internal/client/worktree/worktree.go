// Package worktree は git の作業ツリー（worktree）とブランチの後始末を助ける。
//
// AI が課題ごとに worktree を切る運用だと、マージ済みのものが溜まって「どれが生きているか」が分からなくなる。
// 判断の材料（マージ済みか・未コミットが無いか・locked でないか・**いま誰かが使っていないか**）は毎回同じなので、
// ここで一度だけ集める。
//
// 見るのは git の作業ツリーの性質だけで、プロジェクトの中身は知らない（どのプロジェクトでも同じように働く）。
//
// 物差しは「最後に動いていた時刻」に一本化する。セッションの ID だけでは、終わったセッションの印と動いている
// セッションの印を見分けられない。知りたいのは「誰のものか」ではなく「いま使っているか」なので、印にも時刻を書き、
// 印が無いものは git の記録の更新時刻で代える。これで、印のあるものと無いものが同じ物差しで並ぶ。
package worktree

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
)

// SessionFile は mark が書く印の名前（.git/worktrees/<名前>/ の下）。
const SessionFile = "looptrack-session.json"

// DefaultMinAge は「まだ使っているかもしれない」とみなす長さ。これより新しい作業ツリーは片付けない。
const DefaultMinAge = 30 * time.Minute

// Entry.TouchFrom の値（「最後に動いていた時刻」をどこから取ったか）。
// 利用者には見せず、コードの中だけで見分けるための印なので訳さない。
const (
	TouchMark   = "mark"   // mark が書いた印（looptrack-session.json）
	TouchIndex  = "index"  // git の記録（.git/…/index）の更新時刻
	TouchCommit = "commit" // HEAD のコミットの時刻
)

// junk は「生成物だけの残骸」とみなす名前。これだけの変更は未コミットの変更と数えない
// （撤去した処理系のキャッシュ・依存の展開先・OS が置くファイルで、消えても失うものが無い）。
var junk = []string{
	"__pycache__", ".pytest_cache", ".mypy_cache", ".ruff_cache", "node_modules",
	".DS_Store", "Thumbs.db", ".venv", "venv",
}

// Entry は 1 つの作業ツリー。
type Entry struct {
	Path       string        // 作業ツリーのパス
	Name       string        // .git/worktrees/ の下の名前（本体は空）
	Branch     string        // ブランチ名（detached なら空）
	Head       string        // HEAD のコミット
	Main       bool          // 本体の作業ツリー（複数のセッションが同時に使う。印も付けず、消しもしない）
	Locked     bool          // git worktree lock されている
	LockReason string        //
	Missing    bool          // ディレクトリが無い（git worktree prune の対象）
	Merged     bool          // 既定のブランチに取り込み済み
	Published  bool          // リモートを追うブランチ（追い先がまだ在る）。常設の作業ツリーとみなして片付けない
	Dirty      bool          // 未コミットの変更がある（生成物だけの残骸は数えない）
	JunkOnly   bool          // 未コミットの変更が生成物だけ
	Changes    int           // 未コミットの変更の数（生成物を含む）
	LastCommit time.Time     // HEAD のコミットの時刻
	LastTouch  time.Time     // 最後に動いていた時刻
	TouchFrom  string        // LastTouch の出どころ（TouchMark・TouchIndex・TouchCommit のいずれか）
	Session    string        // 印に書かれたセッションの ID
	Age        time.Duration // いまから LastTouch までの長さ
}

// Branch は作業ツリーを持たないブランチ。
type Branch struct {
	Name       string
	Merged     bool
	Published  bool // リモートを追うブランチ（追い先がまだ在る）
	LastCommit time.Time
	Age        time.Duration
}

// Removable は片付けてよいかと、残すときの理由。作業ツリーと同じく、1 つでも欠ければ残す。
// 理由は利用者に見せる文面なので、ここで lang の文面にして返す
// （ID を返して呼び出し側で文面にすると、訳の抜けを見つけるテストが ID を追えなくなる）。
func (b Branch) Removable(lang i18n.Lang) (bool, string) {
	switch {
	case b.Published:
		return false, i18n.T(lang, "worktree.keep.published_branch")
	case !b.Merged:
		return false, i18n.T(lang, "worktree.keep.not_merged")
	}
	return true, ""
}

// Report は 1 つのリポジトリについて集めたもの。
type Report struct {
	Base     string  // 既定のブランチ
	Root     string  // 本体の作業ツリー（一覧のパスをここからの相対で出すため）
	Entries  []Entry // 作業ツリー（本体を含む。先頭が本体）
	Branches []Branch
}

// Options は List の引数。
type Options struct {
	Dir    string        // リポジトリの中のどこか（既定は作業ディレクトリ）
	Base   string        // 既定のブランチ（空なら main → master の順で探す）
	MinAge time.Duration // これより新しいものは片付けない（0 なら DefaultMinAge）
	Now    func() time.Time
	// Git は git を呼ぶ（テストの差し替え用。nil なら exec）。dir が空なら親のリポジトリで実行する。
	Git func(dir string, args ...string) (string, error)
}

func (o *Options) fill() {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.MinAge == 0 {
		o.MinAge = DefaultMinAge
	}
	if o.Git == nil {
		o.Git = runGit
	}
}

func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if ok := asExitError(err, &ee); ok && len(ee.Stderr) > 0 {
			return string(out), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(ee.Stderr)))
		}
		return string(out), fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func asExitError(err error, out **exec.ExitError) bool {
	ee, ok := err.(*exec.ExitError)
	if ok {
		*out = ee
	}
	return ok
}

// List はリポジトリの作業ツリーとブランチを調べる。
func List(o Options) (*Report, error) {
	o.fill()
	dir := o.Dir
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		dir = wd
	}
	// 本体のリポジトリ（副の作業ツリーから呼ばれても本体に揃える）
	common, err := o.Git(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, err
	}
	gitCommon := strings.TrimSpace(common)
	mainRoot := filepath.Dir(gitCommon) // <本体>/.git → <本体>

	base := o.Base
	if base == "" {
		base = detectBase(o, mainRoot)
	}
	rep := &Report{Base: base, Root: mainRoot}

	out, err := o.Git(mainRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	now := o.Now()
	pub := publishedBranches(o, mainRoot)
	for _, blk := range strings.Split(strings.TrimSpace(out), "\n\n") {
		e := parseWorktreeBlock(blk)
		if e.Path == "" {
			continue
		}
		e.Main = sameDir(e.Path, mainRoot)
		if !e.Main {
			e.Name = worktreeName(o, gitCommon, e.Path)
		}
		if st, err := os.Stat(e.Path); err != nil || !st.IsDir() {
			e.Missing = true
		}
		e.Published = e.Branch != "" && pub[e.Branch]
		fillState(o, &e, mainRoot, gitCommon, base, now)
		rep.Entries = append(rep.Entries, e)
	}
	rep.Branches = listBranches(o, mainRoot, base, rep.Entries, pub, now)
	return rep, nil
}

// publishedBranches は、リモートを追う設定があり、その追い先がまだ在るブランチを集める。
//
// リモートに公開されているブランチは、そのリポジトリで共有されている常設のもの（配置や切り替えに使う作業ツリーなど）で、
// 一時の作業ツリーではない。取り込み済み・未コミットの変更なしでも片付けない。
// 追い先が消えたもの（`gone`。PR がマージされてリモートのブランチが消えた後など）は、ふつうの一時のブランチとして扱う。
func publishedBranches(o Options, mainRoot string) map[string]bool {
	m := map[string]bool{}
	out, err := o.Git(mainRoot, "for-each-ref",
		"--format=%(refname:short)\t%(upstream)\t%(upstream:track,nobracket)", "refs/heads/")
	if err != nil {
		return m
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		// 追跡状況の欄は、リモートと同じところまで来ていると空になる。
		// いちばん最後の行では、その空の欄が行末の空白として削られて 2 つになるので、欄の数で弾かない。
		f := strings.Split(line, "\t")
		if len(f) < 2 || f[1] == "" {
			continue
		}
		if len(f) > 2 && strings.Contains(f[2], "gone") {
			continue
		}
		m[f[0]] = true
	}
	return m
}

// parseWorktreeBlock は git worktree list --porcelain の 1 かたまりを読む。
func parseWorktreeBlock(blk string) Entry {
	var e Entry
	for _, line := range strings.Split(blk, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			e.Path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "HEAD "):
			e.Head = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch "):
			e.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		case line == "locked" || strings.HasPrefix(line, "locked "):
			e.Locked = true
			e.LockReason = unquoteGit(strings.TrimSpace(strings.TrimPrefix(line, "locked")))
		case line == "detached":
			e.Branch = ""
		}
	}
	return e
}

// fillState はマージ済みか・未コミットがあるか・最後に動いていたのはいつか、を埋める。
//
// 「最後に動いていた時刻」は**いちばん先に**読む。git status は index の stat の記録を書き戻すことがあり、
// 後から読むと、調べたこちらの操作で「いま動いた」ことになってしまう（--no-optional-locks と併せた二重の用心）。
func fillState(o Options, e *Entry, mainRoot, gitCommon, base string, now time.Time) {
	e.LastTouch, e.TouchFrom, e.Session = lastTouch(e, gitCommon)
	if e.Head != "" && base != "" {
		if _, err := o.Git(mainRoot, "merge-base", "--is-ancestor", e.Head, base); err == nil {
			e.Merged = true
		}
	}
	if e.Head != "" {
		if s, err := o.Git(mainRoot, "log", "-1", "--format=%ct", e.Head); err == nil {
			if n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
				e.LastCommit = time.Unix(n, 0)
			}
		}
	}
	if !e.Missing {
		// --no-optional-locks: index を書き換えずに読む（調べるだけで最終更新を動かさない）
		if s, err := o.Git(e.Path, "--no-optional-locks", "status", "--porcelain", "--untracked-files=all"); err == nil {
			e.Changes, e.Dirty, e.JunkOnly = countChanges(s)
		}
	}
	if e.LastTouch.IsZero() {
		e.LastTouch, e.TouchFrom = e.LastCommit, TouchCommit
	}
	if !e.LastTouch.IsZero() {
		e.Age = now.Sub(e.LastTouch)
	}
}

// countChanges は git status --porcelain の出力を数える。生成物だけの残骸は未コミットの変更と数えない。
func countChanges(status string) (n int, dirty, junkOnly bool) {
	real := 0
	for _, line := range strings.Split(strings.TrimSpace(status), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		n++
		if !isJunk(pathOfStatusLine(line)) {
			real++
		}
	}
	return n, real > 0, n > 0 && real == 0
}

// pathOfStatusLine は `XY path` か `XY old -> new` からパスを取り出す。
func pathOfStatusLine(line string) string {
	if len(line) > 3 {
		line = line[3:]
	}
	if i := strings.Index(line, " -> "); i >= 0 {
		line = line[i+4:]
	}
	return unquoteGit(strings.TrimSpace(line))
}

// unquoteGit は git が非 ASCII や空白を含む文字列を出すときのクォート（"実機" → "\345\256\237…"）を元に戻す。
// 8 進のエスケープは 1 バイトずつなので、バイト列に戻してから文字列にする（rune として読むと壊れる）。
func unquoteGit(s string) string {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return s
	}
	body := s[1 : len(s)-1]
	b := make([]byte, 0, len(body))
	for i := 0; i < len(body); i++ {
		if body[i] != '\\' {
			b = append(b, body[i])
			continue
		}
		i++
		if i >= len(body) {
			break
		}
		switch e := body[i]; e {
		case 'n':
			b = append(b, '\n')
		case 't':
			b = append(b, '\t')
		case 'r':
			b = append(b, '\r')
		case '"', '\\':
			b = append(b, e)
		default:
			if e >= '0' && e <= '7' && i+2 < len(body) {
				if n, err := strconv.ParseUint(body[i:i+3], 8, 8); err == nil {
					b = append(b, byte(n))
					i += 2
					continue
				}
			}
			b = append(b, e)
		}
	}
	return string(b)
}

// isJunk は生成物だけの残骸か（パスのどこかに junk の名前が入っていれば真）。
func isJunk(p string) bool {
	p = filepath.ToSlash(p)
	for _, part := range strings.Split(strings.Trim(p, "/"), "/") {
		if part == "" {
			continue
		}
		for _, j := range junk {
			if part == j {
				return true
			}
		}
		if strings.HasSuffix(part, ".pyc") || strings.HasSuffix(part, ".pyo") {
			return true
		}
	}
	return false
}

// lastTouch は「最後に動いていた時刻」。印（mark が書く）→ git の記録（index）の順で見る。
func lastTouch(e *Entry, gitCommon string) (time.Time, string, string) {
	dir := gitCommon
	if !e.Main && e.Name != "" {
		dir = filepath.Join(gitCommon, "worktrees", e.Name)
	}
	if b, err := os.ReadFile(filepath.Join(dir, SessionFile)); err == nil {
		var m struct {
			Session string `json:"session"`
			At      string `json:"at"`
		}
		if json.Unmarshal(b, &m) == nil {
			if t, err := time.Parse(time.RFC3339, m.At); err == nil {
				return t, TouchMark, m.Session
			}
			return time.Time{}, "", m.Session
		}
	}
	if st, err := os.Stat(filepath.Join(dir, "index")); err == nil {
		return st.ModTime(), TouchIndex, ""
	}
	return time.Time{}, "", ""
}

// worktreeName は .git/worktrees/ の下の名前を返す（gitdir のファイルの中身がパスと一致するものを探す）。
func worktreeName(o Options, gitCommon, path string) string {
	ents, err := os.ReadDir(filepath.Join(gitCommon, "worktrees"))
	if err != nil {
		return ""
	}
	for _, ent := range ents {
		b, err := os.ReadFile(filepath.Join(gitCommon, "worktrees", ent.Name(), "gitdir"))
		if err != nil {
			continue
		}
		// gitdir には <作業ツリー>/.git が入っている
		if sameDir(filepath.Dir(strings.TrimSpace(string(b))), path) {
			return ent.Name()
		}
	}
	return ""
}

// listBranches は作業ツリーを持たないブランチを集める。
func listBranches(o Options, mainRoot, base string, entries []Entry, pub map[string]bool, now time.Time) []Branch {
	used := map[string]bool{base: true}
	for _, e := range entries {
		if e.Branch != "" {
			used[e.Branch] = true
		}
	}
	out, err := o.Git(mainRoot, "for-each-ref", "--format=%(refname:short)\t%(objectname)\t%(committerdate:unix)", "refs/heads/")
	if err != nil {
		return nil
	}
	var bs []Branch
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 3 || used[f[0]] {
			continue
		}
		b := Branch{Name: f[0], Published: pub[f[0]]}
		if n, err := strconv.ParseInt(f[2], 10, 64); err == nil {
			b.LastCommit = time.Unix(n, 0)
			b.Age = now.Sub(b.LastCommit)
		}
		if _, err := o.Git(mainRoot, "merge-base", "--is-ancestor", f[1], base); err == nil {
			b.Merged = true
		}
		bs = append(bs, b)
	}
	return bs
}

// detectBase は既定のブランチを探す（main → master）。
func detectBase(o Options, root string) string {
	for _, b := range []string{"main", "master"} {
		if _, err := o.Git(root, "rev-parse", "--verify", "--quiet", "refs/heads/"+b); err == nil {
			return b
		}
	}
	return ""
}

func sameDir(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	if err1 == nil && err2 == nil {
		return filepath.Clean(ra) == filepath.Clean(rb)
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// Removable は片付けてよいかと、残すときの理由。
// 1 つでも欠ければ残す（安全側。消す判断は人か、一覧を見た AI が決める）。
// 理由は利用者に見せる文面なので、ここで lang の文面にして返す（Branch.Removable と同じ理由）。
func (e Entry) Removable(lang i18n.Lang, minAge time.Duration) (bool, string) {
	switch {
	case e.Main:
		return false, i18n.T(lang, "worktree.keep.main")
	case e.Missing:
		return true, ""
	case e.Locked:
		if e.LockReason != "" {
			return false, i18n.T(lang, "worktree.keep.locked_reason", "reason", e.LockReason)
		}
		return false, i18n.T(lang, "worktree.keep.locked")
	case e.Dirty:
		return false, i18n.T(lang, "worktree.keep.dirty", "n", e.Changes)
	case e.Published:
		return false, i18n.T(lang, "worktree.keep.published")
	case e.Branch != "" && !e.Merged:
		return false, i18n.T(lang, "worktree.keep.not_merged")
	case e.Branch == "" && !e.Merged:
		return false, i18n.T(lang, "worktree.keep.detached")
	case e.Age > 0 && e.Age < minAge:
		return false, i18n.T(lang, "worktree.keep.recent")
	}
	return true, ""
}

// Losing は「中身が失われかけている」か。日をまたいだ未コミットの変更があるもの。
// 未コミットなら消さない、で守られはするが、そのまま忘れられるのが本当の問題なので、一覧で目立たせる。
func (e Entry) Losing() bool {
	return e.Dirty && e.Age >= 24*time.Hour
}

// Mark は、いまのセッションがこの作業ツリーを使っていることを記録する（.git/worktrees/<名前>/looptrack-session.json）。
// 本体の作業ツリーには付けない（複数のセッションが同時に使うため、誰か 1 人のものにしない）。
//
// 既存の印が別のセッションのものなら、既定では上書きしない（別のセッションが付けた印が警告なく消えないように）。
// 同じセッションの再 mark（同じセッションが動いている間、何度も呼ぶ）はいつでも通す。force が真なら、
// 別のセッションの印でも常に上書きする（利用者の明示の指示があるときだけ呼び出し側が force を渡す）。
func Mark(o Options, session string, force bool) (string, error) {
	o.fill()
	dir := o.Dir
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		dir = wd
	}
	gd, err := o.Git(dir, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return "", err
	}
	common, err := o.Git(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	gitDir, gitCommon := strings.TrimSpace(gd), strings.TrimSpace(common)
	if sameDir(gitDir, gitCommon) {
		return "", nil // 本体の作業ツリー。印を付けない
	}
	p := filepath.Join(gitDir, SessionFile)
	if !force {
		if err := checkMarkConflict(p, session); err != nil {
			return "", err
		}
	}
	b, err := json.Marshal(map[string]string{"session": session, "at": o.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(p, append(b, '\n'), 0o600); err != nil {
		return "", err
	}
	return p, nil
}

// checkMarkConflict は、p にある既存の印が session と違うセッションのものでないかを確かめる。
//
// 印が無い（初めて mark する）・同じセッションの印（再 mark）・印の session が空（誰のものか元々
// 書かれていない）のときは何もしない。読めない・壊れている印は「誰のものか分からない」ので、
// 別のセッションの印があるときと同じ扱いにする（安全側。Entry.Removable と同じ考え方）。
func checkMarkConflict(p, session string) error {
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return i18n.Errorf("worktree.mark.conflict_unreadable", "path", p, "reason", err)
	}
	var m struct {
		Session string `json:"session"`
		At      string `json:"at"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return i18n.Errorf("worktree.mark.conflict_unreadable", "path", p, "reason", err)
	}
	if m.Session == "" || m.Session == session {
		return nil
	}
	return i18n.Errorf("worktree.mark.conflict", "other", m.Session, "at", m.At)
}
