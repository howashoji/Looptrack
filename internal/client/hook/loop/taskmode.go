package loop

// user-prompt-task-mode・pre-edit-task-mode-guard（kit/loop/hooks/user-prompt-task-mode.sh・pre-edit-task-mode-guard.sh の Go 版）。
//
// 「確認して」等 → 確認モード（investigate。編集は PreToolUse で deny）、「実装して」等 → 実行モード（execute）。
// 英語の依頼（please check … / implement …）も同じ（DESIGN.md §5-13。kit の bash 版には足さない）。
// モードはセッションごとに <状態>/task-mode.d/<session_id> に記録する（session_id が無ければ共有の <状態>/task-mode）。
// 7 日より古い記録は消し、ガードは 24 時間より古い記録を無効として扱う。
//
// bash 版との違い: ガードは、matcher を効かせない AI のために、ツールの種類が読み取り・シェル・MCP なら何もしない
// （bash 版は matcher の Edit|Write|NotebookEdit に任せ、ツール名を見なかった）。対象のパスは file_path・notebook_path に加えて
// Copilot の filePath・path も読む（hookio.Tool.FilePath）。worktree の判定は .git を読む（git を起動しない）。

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/hookio"
	"github.com/howashoji/looptrack/internal/i18n"
)

var (
	// 利用者が書いていないもの（harness が入れるブロック・通知）は判定の対象にしない。
	// Copilot の Stop の差し戻しの理由（次の利用者のメッセージとして渡る）も利用者の指示と見なさない。
	// <cross-session-message …> は他のセッションからの連絡。Claude Code は
	// 「Another Claude session sent a message:」に続けてこのタグで包んだ本文を、利用者の発話として UserPromptSubmit に渡す。
	// 本文には「確認」「review」等の語が普通に出るので、除かないとタスクモードが利用者の意思と無関係に往復する
	// （実測: 2026-09-20、他セッションの連絡 1 通で確認モードに落ち、次の連絡で実行モードに戻った）。
	// タスクの通知と同じく、ブロックを含むメッセージ全体を判定しない（Claude Code はこの形のメッセージに
	// 利用者自身の文を混ぜない。連絡の前後に付くのは harness の定型文だけ）。
	notificationRe = regexp.MustCompile(`<task-notification>|<cross-session-message[\s>]|\[SYSTEM NOTIFICATION|<local-command-caveat>|<command-name>|^\s*` +
		regexp.QuoteMeta(hookio.CopilotStopMarker))
	execWords = regexp.MustCompile(`(実行して|作業して|実装して|作成して|執筆して|修正して|直して|対応して|適用して|反映して|やって|作って|書いて|` +
		`追加して|削除して|変更して|更新して|移植して|コミットして|マージして|デプロイして|リリースして)`)
	investWords = regexp.MustCompile(`(確認して|確認だけ|調査して|調べて|洗い出して|監査して|チェックして|検証して|レビューして|分析して|整理して|選別して|` +
		`計画(を|案を)?(立てて|提示して|作って)|提案して|提示して)`)

	// 英語の別名（DESIGN.md §5-13）。単語の境界で、英字の大小を問わずに判定する（fixture を fix と読まない）。
	// \b は ASCII の単語の境界。語の間の空白は 1 つ以上
	execWordsEn   = regexp.MustCompile(`(?i)\b(implement|fix|apply|create|write|add|remove|update|commit|merge|deploy|release|go\s+ahead)\b`)
	investWordsEn = regexp.MustCompile(`(?i)\b(check|investigate|look\s+into|review|audit|analy[sz]e|explain|propose|plan)\b`)
)

// taskMarker は記録のパス（session_id が無ければ共有の記録）。
func taskMarker(ev hookio.Event, state string) string {
	if sid := sessionFile(ev); sid != "" {
		return filepath.Join(state, "task-mode.d", sid)
	}
	return filepath.Join(state, "task-mode")
}

// UserPromptTaskMode はタスクモード（確認 / 実行）を判定・記録し、確認モードの間は毎ターン短く注入する。
func UserPromptTaskMode(ctx context.Context, ev hookio.Event) (hookio.Result, error) {
	e := envFrom(ctx)
	prompt := ev.Prompt
	if prompt == "" || notificationRe.MatchString(prompt) {
		return hookio.Result{}, nil
	}
	r, state := root(ev, e), stateDir(ev, e)
	marker := taskMarker(ev, state)
	rel := filepath.ToSlash(marker) // コマンドに埋めるので区切りは /（relpath を参照）
	if under(marker, r) {
		rel = relpath(marker, r)
	}

	hit := func(base *regexp.Regexp, extra string) bool {
		if base.MatchString(prompt) {
			return true
		}
		if rx := compileUser(extra); rx != nil {
			return rx.MatchString(prompt)
		}
		return false
	}
	// 実行系が確認系に勝つ（日本語・英語とも）。日本語の語（と利用者が足した語）で決まればそれを使い、英語の語は
	// 日本語で決まらないときだけ見る（「deploy.sh を確認して」「コミットの update 漏れを確認して」のように、コマンド名・
	// ファイル名の英単語を含む日本語の依頼を今までどおり確認モードにする）
	mode := ""
	switch {
	case hit(execWords, e.env("LOOPTRACK_LOOP_TASK_MODE_EXEC_RE")):
		mode = "execute"
	case hit(investWords, e.env("LOOPTRACK_LOOP_TASK_MODE_INVEST_RE")):
		mode = "investigate"
	case execWordsEn.MatchString(prompt):
		mode = "execute"
	case investWordsEn.MatchString(prompt):
		mode = "investigate"
	}
	if mode != "" {
		if os.MkdirAll(filepath.Dir(marker), 0o777) == nil {
			_ = os.WriteFile(marker, []byte(mode+"\n"), 0o666)
		}
	}

	// 終わったセッションの記録を溜めない（ガードは 24 時間より古い記録を無効として扱う）
	d := filepath.Join(state, "task-mode.d")
	if ents, err := os.ReadDir(d); err == nil {
		now := e.Now()
		for _, ent := range ents {
			p := filepath.Join(d, ent.Name())
			if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && now.Sub(st.ModTime()) > 7*24*time.Hour {
				_ = os.Remove(p)
			}
		}
	}

	current := "execute"
	if t, err := os.ReadFile(marker); err == nil {
		if first := trimSpace(firstLine(string(t))); first != "" {
			current = first
		}
	}

	lang := e.lang()
	var msg string
	switch {
	case mode == "investigate":
		msg = i18n.T(lang, "loop.taskmode.investigate")
	case mode == "execute":
		msg = i18n.T(lang, "loop.taskmode.execute")
		if note := trimSpace(e.env("LOOPTRACK_LOOP_TASK_MODE_EXEC_NOTE")); note != "" {
			msg += note
		}
	case current == "investigate":
		msg = i18n.T(lang, "loop.taskmode.continued", "marker", rel)
	default:
		return hookio.Result{}, nil
	}
	return hookio.Result{Context: msg}, nil
}

// firstLine は最初の改行までの 1 行（改行を含む）。
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i+1]
	}
	return s
}

// PreEditTaskModeGuard は確認モード中のプロジェクト内（同じリポジトリの worktree を含む）の編集を deny する。
func PreEditTaskModeGuard(ctx context.Context, ev hookio.Event) (hookio.Result, error) {
	e := envFrom(ctx)
	if ev.Tool != nil {
		switch ev.Tool.Kind {
		case hookio.KindRead, hookio.KindBash, hookio.KindMCP:
			return hookio.Result{}, nil // matcher を効かせない AI（VS Code）から編集以外で呼ばれた
		}
	}
	r := strings.TrimRight(root(ev, e), "/")
	state := stateDir(ev, e)
	marker := taskMarker(ev, state)
	st, err := os.Stat(marker)
	if err != nil {
		return hookio.Result{}, nil
	}
	t, err := os.ReadFile(marker)
	if err != nil {
		return hookio.Result{}, nil
	}
	if trimSpace(firstLine(string(t))) != "investigate" || e.Now().Sub(st.ModTime()) > 24*time.Hour {
		return hookio.Result{}, nil
	}

	ti := toolInput(ev)
	p := toStr(ti["file_path"])
	if p == "" {
		p = toStr(ti["notebook_path"])
	}
	if p == "" && ev.Tool != nil {
		// Copilot の path・filePath・apply_patch の本文（複数のファイルなら、プロジェクト内の最初のもの）
		for _, c := range ev.Tool.FilePaths() {
			if res, ok := editGuardDeny(e, r, marker, c); ok {
				return res, nil
			}
		}
		return hookio.Result{}, nil
	}
	if p == "" {
		return hookio.Result{}, nil
	}
	res, _ := editGuardDeny(e, r, marker, p)
	return res, nil
}

// editGuardDeny は確認モード中に p の編集を止めるなら拒否の Result と true を返す（プロジェクトの外・例外のディレクトリ・
// 自セッションの記録なら false）。
func editGuardDeny(e *Env, r, marker, p string) (hookio.Result, bool) {
	if !filepath.IsAbs(p) {
		p = filepath.Join(r, p)
	}
	p = realpath(p)
	r = realpath(r)

	base := ""
	if under(p, r) {
		base = r
	} else if mine := gitCommonDir(r); mine != "" && gitCommonDir(filepath.Dir(p)) == mine {
		if top := gitTop(filepath.Dir(p)); top != "" {
			base = realpath(top)
		} else {
			base = r
		}
	}
	if base == "" {
		return hookio.Result{}, false // プロジェクトの外
	}
	if realpath(marker) == p {
		return hookio.Result{}, false // 自セッションの記録（明示の指示を引用したうえでの切り替え）
	}
	allow := strings.Fields(e.env("LOOPTRACK_LOOP_TASK_MODE_ALLOW_DIRS"))
	for _, a := range allow {
		a = strings.Trim(a, "/")
		if a != "" && (under(p, filepath.Join(r, a)) || under(p, filepath.Join(base, a))) {
			return hookio.Result{}, false
		}
	}

	shown := filepath.ToSlash(marker) // コマンドに埋めるので区切りは /（relpath を参照）
	if rm := realpath(marker); under(rm, r) {
		shown = relpath(rm, r)
	}
	lang := e.lang()
	reason := i18n.T(lang, "loop.taskmode.deny.reason", "marker", shown)
	if len(allow) > 0 {
		var ds []string
		for _, a := range allow {
			ds = append(ds, strings.TrimRight(a, "/")+"/")
		}
		reason += i18n.T(lang, "loop.taskmode.deny.allow_dirs", "dirs", strings.Join(ds, " "))
	}
	reason += i18n.T(lang, "loop.taskmode.deny.target", "path", p)
	return hookio.Result{Deny: reason}, true
}
