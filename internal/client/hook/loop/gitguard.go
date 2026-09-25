package loop

// pre-tool-git-guard: 巻き込みと取り返しのつかない git の操作を止める。
//
//   - `git add -A` / `git add .` / `git commit -a`: 触っていないパスまで stage する。作業ツリーを複数のセッションや
//     複数の作業で共有していると、関係のない変更を巻き込んだままコミットされる。`git add <パス>` と `git add -u` は通す。
//   - `git push --force` と `+` で始まる refspec（`git push origin +main`）: 相手の履歴を消す。
//     `--force-with-lease` / `--force-if-includes` は通す。
//   - `git reset --hard` / `git clean -f…` / `git switch --discard-changes`: 未コミットの変更が戻せなくなる。
//   - `git checkout` / `git restore` の**パス指定**（`.` だけでなく個別のパス・ディレクトリも）: そのパスの未コミットの
//     変更が戻せなくなる。ブランチの切り替え（`git checkout <ブランチ>`・`-b`）は通す。
//   - `-C <ディレクトリ>` / `--work-tree` / `--git-dir` が**いまの作業ツリーの外**を指す書き込み（読むだけの操作は通す）:
//     ほかのセッションが作業中の作業ツリーを壊しうる。
//
// ask と deny の使い分け（実測にもとづく）:
//
//	bypass permissions の対話セッションでは `permissionDecision: ask` は**素通りする**（確認が出ないまま実行される）。
//	実測: 使い捨てのリポジトリで未コミットの変更を作り `git checkout .` を実行 → 確認は出ず変更は消えた。
//	同じ版を `claude -p --permission-mode bypassPermissions` で試すと `deny` は止まった。
//	したがって **取り返しのつかないもの（未コミットの変更・履歴を捨てる・ほかの作業ツリーへの書き込み）は deny**、
//	**取り返しのつくもの（stage の巻き込み。unstage・amend で戻せる）は ask** にする。
//	deny の理由には、利用者に確かめる道と例外の指定のしかたを書く。
//
// 例外は LOOPTRACK_LOOP_GIT_GUARD_ALLOW（空白区切り。コマンドの文字列に含まれれば通す）。
//
// 判定は hookcmd.CommandWords で単純コマンドごとの語に分けてから行うので、引用符やヒアドキュメントの中に
// 同じ語があっても反応しない（`echo 'git add -A は禁止'`・コミットメッセージの本文など）。
// リダイレクト（`2>&1`・`>/dev/null`・`&>/dev/null`）も語から外す（記述子の `2` をパスと数えない）。
//
// 分ける前に hookcmd.Normalize で「実際に実行される語」に直す（入れ子のシェルを 1 段ほどく・
// 前置の語を落とす）。**秘密のガードと同じ処理を呼ぶ**ので、経路によって効いたり効かなかったりしない。
// 違うのは引数の hookcmd.HeadOnly だけ。`deny` は bypass permissions でも素通りしないので、
// 何も実行されない形（`echo bash -c '…'`）で止めると、このガードについて書く作業そのものが止まる。
// 止めないものは rules の working-discipline.md に挙げてある。
//
// なぜ要るか: 「触ったパスだけを stage する」「ほかのセッションの作業ツリーを戻さない」は rules
// （working-discipline.md の並行セッション）にも各プロジェクトの文書にも書かれているが、機械で止めるものが無い。
// 文章だけの禁止は、セッションが増えるほど破られる（実際に、ほかのセッションの未コミットの変更が失われた）。

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/howashoji/looptrack/internal/client/hook/hookcmd"
	"github.com/howashoji/looptrack/internal/hookio"
	"github.com/howashoji/looptrack/internal/i18n"
)

// gitGlobalOptArg は git 本体のオプションのうち、次の語を値として取るもの。
var gitGlobalOptArg = map[string]bool{"-C": true, "-c": true, "--git-dir": true, "--work-tree": true, "--namespace": true, "--exec-path": true}

// gitDirOpt は git 本体のオプションのうち、値が「操作の対象になるディレクトリ」のもの
// （`--git-dir` は `.git` を指すが、そこから作業ツリーをたどれるので同じに扱う）。
var gitDirOpt = map[string]bool{"-C": true, "--git-dir": true, "--work-tree": true}

// gitCmd は語の並びを git のコマンドとして読んだもの。
type gitCmd struct {
	sub  string   // サブコマンド（`git --version` のように無いこともある）
	args []string // サブコマンドより後ろの語
	dirs []string // -C / --git-dir / --work-tree が指したディレクトリ（書かれたまま）
}

// parseGitCmd は語の並びを git のコマンドとして読む（git でなければ ok = false）。
func parseGitCmd(words []string) (gitCmd, bool) {
	var c gitCmd
	if len(words) == 0 {
		return c, false
	}
	b := filepath.Base(path.Base(strings.ReplaceAll(words[0], `\`, "/")))
	if b != "git" && b != "git.exe" {
		return c, false
	}
	for i := 1; i < len(words); i++ {
		w := words[i]
		if !strings.HasPrefix(w, "-") {
			c.sub, c.args = w, words[i+1:]
			return c, true
		}
		if k, v, eq := strings.Cut(w, "="); eq {
			if gitDirOpt[k] {
				c.dirs = append(c.dirs, v)
			}
			continue
		}
		if gitGlobalOptArg[w] {
			if i+1 < len(words) {
				if gitDirOpt[w] {
					c.dirs = append(c.dirs, words[i+1])
				}
				i++ // 次の語は値
			}
		}
	}
	return c, true // サブコマンドが無い（`git --version` など）
}

// hasShortFlag は短縮フラグ（`-am` の a のように 1 文字ずつ結合されるもの）に c が含まれるか。
// 長いオプション（`--all`）と、値を伴う `-C` のような形は見ない。
func hasShortFlag(args []string, c byte) bool {
	for _, a := range args {
		if a == "--" {
			return false // これより後ろはパス
		}
		if len(a) < 2 || a[0] != '-' || a[1] == '-' {
			continue
		}
		if strings.IndexByte(a[1:], c) >= 0 {
			return true
		}
	}
	return false
}

// hasLongFlag は args に長いオプション（値付きの `--opt=v` を含む）があるか。
func hasLongFlag(args []string, names ...string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if i := strings.IndexByte(a, '='); i > 0 {
			a = a[:i]
		}
		for _, n := range names {
			if a == n {
				return true
			}
		}
	}
	return false
}

// hasWholeTreePath は args に「作業ツリー全体」を指すパス（`.`・`:/`・`:/.`）があるか。
func hasWholeTreePath(args []string) bool {
	for _, a := range args {
		switch a {
		case ".", "./", ":/", ":/.":
			return true
		}
	}
	return false
}

// gitReadOnlySub は、対象が別の作業ツリーでも通す（読むだけの）サブコマンド。
// ここにも gitListSub にも無い語は「書くもの」として扱う（知らないサブコマンドは fail-closed）。
var gitReadOnlySub = map[string]bool{
	"blame": true, "cat-file": true, "check-attr": true, "check-ignore": true, "count-objects": true,
	"describe": true, "diff": true, "diff-index": true, "diff-tree": true, "for-each-ref": true,
	"fsck": true, "grep": true, "help": true, "log": true, "ls-files": true, "ls-remote": true,
	"ls-tree": true, "merge-base": true, "name-rev": true, "rev-list": true, "rev-parse": true,
	"shortlog": true, "show": true, "show-ref": true, "status": true, "var": true,
	"verify-commit": true, "verify-tag": true, "version": true, "whatchanged": true,
}

// gitListSub は、一覧・表示のときだけ読むだけになるサブコマンド（`git branch` と `git branch -d` は別物）。
var gitListSub = map[string]bool{
	"branch": true, "config": true, "notes": true, "remote": true,
	"stash": true, "submodule": true, "tag": true, "worktree": true,
}

// gitListVerb は gitListSub の第 1 引数のうち、一覧・表示だけのもの。
var gitListVerb = map[string]bool{"list": true, "show": true, "status": true}

// gitListFlag は gitListSub に付いていても一覧・表示のままのオプション。
var gitListFlag = map[string]bool{
	"-l": true, "--list": true, "-v": true, "-vv": true, "--verbose": true, "-a": true, "--all": true,
	"--show-current": true, "--get": true, "--get-all": true, "--get-regexp": true, "--porcelain": true,
	"--contains": true, "--merged": true, "--no-merged": true, "--points-at": true, "--format": true,
}

// gitReadsOnly は sub が「読むだけ」か（別の作業ツリーを指していても通してよいか）。
func gitReadsOnly(sub string, args []string) bool {
	if sub == "" || gitReadOnlySub[sub] {
		return true
	}
	if !gitListSub[sub] {
		return false
	}
	verbs := 0
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			k, _, _ := strings.Cut(a, "=")
			if !gitListFlag[k] {
				return false
			}
			continue
		}
		if verbs == 0 && !gitListVerb[a] {
			return false
		}
		verbs++
	}
	// `git stash` だけは引数が無いと stash する（`git branch`・`git tag` は一覧）
	return sub != "stash" || verbs > 0
}

// dirOutsideWorktree は d（相対は cwd から解く）が、いまの作業ツリーの外を指すか。
// git のルート同士で比べる（この運用では作業ツリーがプロジェクトの中（`.claude/worktrees/…`）に置かれるので、
// 「cwd の下かどうか」だけでは別のセッションの作業ツリーを見分けられない）。
//
// **いまの作業ツリーだと確かめられないものは「外」として扱う（fail-closed）**。hookio.GitRoot は親を
// たどるので、実在しないパスを渡すと祖先の `.git` を拾って「同じ作業ツリー」に見えてしまう。
// Windows では、引用符で囲まない `C:\…` がシェルの語の分け方で区切りを失う（`C:\Users\x` → `C:Usersx`）ため、
// ほかの作業ツリーへの書き込みがこの経路で素通りしていた。パスの比較そのものは gitpath.go の純粋な関数で行う
// （区切りの混在・ドライブ名・大文字小文字・末尾の区切り・UNC を、動いている OS に依らず同じ結果で扱う）。
func dirOutsideWorktree(cwd, d string) bool {
	if cwd == "" || d == "" {
		return false
	}
	fold := caseFold(cwd, d)
	t := d
	if !isAbsPath(t) {
		if lostSeparators(t) {
			// `C:…`（ドライブからの相対）は cwd からは解けない。引用符で囲まない Windows のパスが
			// 区切りを失った形でもあり、どこを指していたかは復元できない。
			return true
		}
		t = filepath.Join(cwd, t)
	}
	if samePath(t, cwd, fold) {
		return false
	}
	if _, err := os.Stat(t); err != nil {
		return true // 実在しない。いまの作業ツリーだと確かめられない
	}
	if rt, rc := hookio.GitRoot(t), hookio.GitRoot(cwd); rt != "" && rc != "" {
		return !samePath(rt, rc, fold)
	}
	return !underPath(cwd, t, fold)
}

// gitBranchFlagArg は checkout / switch のオプションのうち、ブランチを作る・切り替えるもの（次の語は名前）。
var gitBranchFlagArg = map[string]bool{"-b": true, "-B": true, "-c": true, "-C": true, "--orphan": true}

// gitBranchFlag は checkout / switch のオプションのうち、対象がブランチ・コミットだと分かるもの（値は取らない）。
var gitBranchFlag = map[string]bool{"--detach": true, "--guess": true, "--no-guess": true, "-t": true, "--track": true, "--no-track": true}

// gitPathOptArg は checkout / restore のオプションのうち、次の語を値として取るもの（パスではない）。
var gitPathOptArg = map[string]bool{"-s": true, "--source": true, "--conflict": true, "--pathspec-from-file": true}

// gitCheckoutArgs は checkout / restore / switch の引数を、位置引数・`--` の後ろのパス・ブランチ操作かに分ける。
func gitCheckoutArgs(args []string) (pos, paths []string, branch bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			paths = append(paths, args[i+1:]...)
			return pos, paths, branch
		}
		if !strings.HasPrefix(a, "-") {
			pos = append(pos, a)
			continue
		}
		k, _, eq := strings.Cut(a, "=")
		switch {
		case gitBranchFlagArg[k]:
			branch = true
			if !eq {
				i++ // 次の語はブランチ名
			}
		case gitBranchFlag[k]:
			branch = true
		case gitPathOptArg[k] && !eq:
			i++ // 次の語は値
		}
	}
	return pos, paths, branch
}

// isPathArg は a が（ブランチ・コミットではなく）パスとして解釈される語か。
// pathspec の記法と実在で見る（存在しないブランチ名らしい語は通す＝ `git checkout feature/x` を止めない）。
func isPathArg(dir, a string) bool {
	if a == "" || strings.HasPrefix(a, "-") {
		return false
	}
	switch {
	case a == "." || a == "./" || strings.HasPrefix(a, ":"):
		return true
	case strings.HasPrefix(a, "./") || strings.HasPrefix(a, "../") || strings.HasPrefix(a, "/"):
		return true
	case strings.ContainsAny(a, "*?"):
		return true
	}
	if dir == "" {
		return false
	}
	p := a
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	_, err := os.Stat(p)
	return err == nil
}

// gitDiscardPaths は checkout / restore が未コミットの変更を捨てるパスを返す（捨てないなら空）。
func gitDiscardPaths(sub, dir string, args []string) []string {
	pos, paths, branch := gitCheckoutArgs(args)
	if sub == "restore" {
		// `--staged` だけ（`--worktree` を伴わない）は index を戻すだけで、作業ツリーのファイルは変えない
		if hasLongFlag(args, "--staged") && !hasLongFlag(args, "--worktree", "-W") && !hasShortFlag(args, 'W') {
			return nil
		}
		return append(pos, paths...)
	}
	// checkout
	if branch {
		return nil // ブランチを作る・切り替える形
	}
	switch {
	case len(paths) > 0:
		return paths // `--` の後ろは必ずパス
	case len(pos) >= 2:
		return pos[1:] // 先頭は tree-ish（`git checkout HEAD <パス>`）
	case len(pos) == 1 && isPathArg(dir, pos[0]):
		return pos
	}
	return nil
}

// gitPushOptArg は push のオプションのうち、次の語を値として取るもの（refspec ではない）。
var gitPushOptArg = map[string]bool{"-o": true, "--push-option": true, "--repo": true, "--receive-pack": true, "--exec": true}

// hasForcedRefspec は push の位置引数に `+` で始まる refspec（`+main`・`+HEAD:main`）があるか。
// `+` はその ref だけを `--force` と同じく強制で更新する（相手の履歴を消す）。
func hasForcedRefspec(args []string) bool {
	opts := true
	for i := 0; i < len(args); i++ {
		a := args[i]
		if opts && a == "--" {
			opts = false
			continue
		}
		if opts && strings.HasPrefix(a, "-") {
			if k, _, eq := strings.Cut(a, "="); !eq && gitPushOptArg[k] {
				i++ // 次の語は値
			}
			continue
		}
		if len(a) > 1 && a[0] == '+' {
			return true
		}
	}
	return false
}

// gitGuardReason は words が止めるべき git の操作なら、その理由と、deny か（false なら ask）を返す。
// cwd はそのコマンドが動く場所（`-C` の解決と、パスの実在の判定に使う）。
func gitGuardReason(lang i18n.Lang, cwd string, words []string) (reason string, deny bool) {
	c, ok := parseGitCmd(words)
	if !ok {
		return "", false
	}
	// ほかの作業ツリーを指した書き込み（サブコマンドの種類より先に見る。読むだけのものは通す）
	if !gitReadsOnly(c.sub, c.args) {
		for _, d := range c.dirs {
			if dirOutsideWorktree(cwd, d) {
				return i18n.T(lang, "loop.gitguard.other_worktree", "dir", d, "sub", c.sub), true
			}
		}
	}
	// 作業する場所（`-C` があればその先）。パスの実在をそこで見る
	dir := cwd
	for _, d := range c.dirs {
		if d != "" {
			if filepath.IsAbs(d) {
				dir = filepath.Clean(d)
			} else if cwd != "" {
				dir = filepath.Join(cwd, d)
			}
			break
		}
	}
	// stage は「触ったパスだけを stage する」の勧め（add / commit -a に共通で付ける）
	stage := func(s string) string { return s + i18n.T(lang, "loop.gitguard.stage") }
	switch c.sub {
	case "add":
		if hasShortFlag(c.args, 'A') || hasLongFlag(c.args, "--all", "--no-ignore-removal") {
			return stage(i18n.T(lang, "loop.gitguard.add_all")), false
		}
		if hasWholeTreePath(c.args) {
			return stage(i18n.T(lang, "loop.gitguard.add_dot")), false
		}
	case "commit":
		if hasShortFlag(c.args, 'a') || hasLongFlag(c.args, "--all") {
			return stage(i18n.T(lang, "loop.gitguard.commit_all")), false
		}
	case "push":
		if hasLongFlag(c.args, "--force-with-lease", "--force-if-includes") {
			return "", false
		}
		if hasShortFlag(c.args, 'f') || hasLongFlag(c.args, "--force") || hasForcedRefspec(c.args) {
			return i18n.T(lang, "loop.gitguard.push_force"), true
		}
	case "reset":
		if hasLongFlag(c.args, "--hard") {
			return i18n.T(lang, "loop.gitguard.reset_hard"), true
		}
	case "clean":
		if hasShortFlag(c.args, 'f') || hasLongFlag(c.args, "--force") {
			return i18n.T(lang, "loop.gitguard.clean_force"), true
		}
	case "switch":
		if hasLongFlag(c.args, "--discard-changes", "--force") || hasShortFlag(c.args, 'f') {
			return i18n.T(lang, "loop.gitguard.switch_force"), true
		}
	case "checkout", "restore":
		p := gitDiscardPaths(c.sub, dir, c.args)
		if len(p) == 0 {
			return "", false
		}
		if hasWholeTreePath(p) {
			return i18n.T(lang, "loop.gitguard.checkout_dot", "sub", c.sub), true
		}
		return i18n.T(lang, "loop.gitguard.checkout_path", "sub", c.sub, "path", p[0]), true
	}
	return "", false
}

// PreToolGitGuard は `looptrack hook pre-tool-git-guard`。
func PreToolGitGuard(ctx context.Context, ev hookio.Event) (hookio.Result, error) {
	e := envFrom(ctx)
	if ev.Tool == nil || ev.Tool.Kind != hookio.KindBash {
		return hookio.Result{}, nil
	}
	if v := ev.Raw["tool_input"]; truthy(v) {
		if _, isMap := v.(map[string]any); !isMap {
			return hookio.Result{}, nil
		}
	}
	cmd := toStr(toolInput(ev)["command"])
	if cmd == "" {
		return hookio.Result{}, nil
	}
	for _, w := range strings.Fields(e.env("LOOPTRACK_LOOP_GIT_GUARD_ALLOW")) {
		if strings.Contains(cmd, w) {
			return hookio.Result{}, nil
		}
	}
	// 判定に掛ける文字列は hookcmd の共通の段で作る（秘密のガードと同じものを呼ぶ）。
	// リダイレクト（`2>&1`・`>/dev/null`）は語から外す（記述子の `2` をパス指定の引数と数えない）。
	normalized := hookcmd.Normalize(cmd, hookcmd.HeadOnly)
	cwd := ev.CWD
	if cwd == "" {
		cwd = e.Getwd()
	}
	lang := e.lang()
	// posix の分け方（Git Bash も含む）と、Windows 規則の第 2 の分け方（PowerShell / cmd。`\` を
	// エスケープとして扱わないので、引用符で囲まない `C:\tools\git.exe` が区切りを失わず 1 語のまま読める）
	// の両方に掛け、どちらかが当たれば発火する（利用者・監督の決定「案 1」）。posix 側の結果は
	// 1 ビットも変わらないので Git Bash はこれまでどおり無傷。どちらも分解できなければ通す（fail-open）。
	for _, split := range []func(string) ([][]string, bool){hookcmd.CommandWords, hookcmd.CommandWordsWin} {
		cmds, ok := split(normalized)
		if !ok {
			continue
		}
		for _, words := range cmds {
			if reason, deny := gitGuardReason(lang, cwd, words); reason != "" {
				if deny {
					return hookio.Result{Deny: i18n.T(lang, "loop.gitguard.deny", "reason", reason)}, nil
				}
				return hookio.Result{Ask: i18n.T(lang, "loop.gitguard.ask", "reason", reason)}, nil
			}
		}
	}
	return hookio.Result{}, nil
}
