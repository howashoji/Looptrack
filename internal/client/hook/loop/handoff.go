package loop

// post-work-complete-handoff-mark・stop-handoff-freshness（kit/loop/hooks/ の同名の .sh の Go 版）: 引き継ぎ鮮度ガード。
//
// 1 段目（PostToolUse）: 成功した git commit（--dry-run・nothing to commit・引き継ぎのファイルだけのコミットは除く）・
// CLI の close / status <ID> Done|Canceled（--help / -h は除く）・MCP の set_status で Done / Canceled を検知したら、
// <状態>/handoff-pending.d/<session_id> に「時刻<TAB>イベント」を 1 行足して、引き継ぎの更新を促す。
// 2 段目（Stop）: 積まれたマーカーより引き継ぎが古ければ差し戻す。新しければマーカーを畳む。鮮度の判定（LOOPTRACK_LOOP_HANDOFF_BACKEND）は
// file（既定。LOOPTRACK_LOOP_HANDOFF_FILE）・auto-memory（~/.claude/projects/<パス>/memory/MEMORY.md）・command（LOOPTRACK_LOOP_HANDOFF_CHECK_CMD を
// bash -c で実行。0 = 新しい / 1 = 古い / それ以外 = 判定できない）。
//
// bash 版との違い: シェルのツールは名前（Bash・bash・run_in_terminal）に加えて種類（hookio.KindBash）でも見分ける。ツールの結果は
// Copilot CLI の camelCase の toolResult も読む。git diff-tree は git を起動する。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/hookio"
	"github.com/howashoji/looptrack/internal/i18n"
)

var (
	envAssignRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	mcpSetStatus  = regexp.MustCompile(`^(mcp__.+__|mcp_.+_|[A-Za-z0-9_.]+-)set_status$`)
	notCommitted  = regexp.MustCompile(`nothing to commit|no changes added to commit|nothing added to commit`)
	commitSuccess = regexp.MustCompile(`\[[^\]]+ [0-9a-f]{7,}\]|files? changed`)
	shellTools    = map[string]bool{"Bash": true, "bash": true, "run_in_terminal": true}
)

// shellKeywords は「直後がコマンドの位置になる」シェルの予約語。
//
// commandSegments は区切り（; & | ( )）でしか区間を切らないので、
// `for i in ABC-0123 ABC-0124; do looptrack issue status $i Done; done` の区間は
// [for i in …] [do looptrack issue status $i Done] [done] になり、真ん中の先頭の語は `do` になる。
// 剥がさないと「looptrack でも git でもない」として区間ごと捨てられ、ループの中の完了が 1 件も残らない
// （同じ理由で `for d in a b; do git commit -m x; done` のコミットも数えられていなかった）。
//
// 入れるのは直後がコマンドの位置になる語だけ。`for` / `select` / `case` は直後が変数名・被検査語なので入れない
// （その中の本体は `do` / `)` で切れた次の区間に出るので、この表だけで届く）。同じ理由で
// `done` / `fi` / `esac` / `}` / `in` のような閉じ・つなぎの語も入れない。
//
// `function` は入れるが、`function f { looptrack …` は名前が挟まるので、この剥がしだけでは届かない。
// 前置きの語（sudo・env・xargs のような、後ろにコマンドを取る**コマンド**）まで剥がすと届くが、
// それは通す側を広げる変更で、実際には実行されない引数（`xargs -I{} looptrack …` の雛形）まで
// 完了として積んでしまうので採らない。
var shellKeywords = map[string]bool{
	"do": true, "then": true, "else": true, "elif": true,
	"if": true, "while": true, "until": true, "{": true,
	"!": true, "time": true, "coproc": true, "function": true,
}

// markHeredocs はヒアドキュメントの本文を外す（bash 版の post-work-complete-handoff-mark の strip_heredocs。
// 1 行に最初の 1 つだけを見る・開きの引用符と閉じの引用符が同じときだけ当たる＝ `<<-?\s*(["']?)(名前)\1`）。
func markHeredocs(cmd string) string {
	lines := strings.Split(cmd, "\n")
	var out []string
	i := 0
	for i < len(lines) {
		line := lines[i]
		out = append(out, line)
		i++
		if end, ok := heredocTag(line); ok {
			for i < len(lines) && trimSpace(lines[i]) != end {
				i++
			}
			i++
		}
	}
	return strings.Join(out, "\n")
}

// heredocTag は行の中で最初に `<<-?\s*(["']?)([A-Za-z_][A-Za-z0-9_]*)\1` に当たるものの名前。
func heredocTag(line string) (string, bool) {
	isStart := func(c byte) bool { return c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') }
	isPart := func(c byte) bool { return isStart(c) || (c >= '0' && c <= '9') }
	for i := 0; i+1 < len(line); i++ {
		if line[i] != '<' || line[i+1] != '<' {
			continue
		}
		j := i + 2
		if j < len(line) && line[j] == '-' {
			j++
		}
		for j < len(line) {
			r := []rune(line[j:])[0]
			if !isSpaceRune(r) {
				break
			}
			j += len(string(r))
		}
		var q byte
		if j < len(line) && (line[j] == '"' || line[j] == '\'') {
			q = line[j]
			j++
		}
		if j >= len(line) || !isStart(line[j]) {
			continue
		}
		k := j + 1
		for k < len(line) && isPart(line[k]) {
			k++
		}
		if q != 0 && (k >= len(line) || line[k] != q) {
			continue
		}
		return line[j:k], true
	}
	return "", false
}

// commandSegments はコマンドを区切り（; & | ( )）で分けた語の並び（引用符の中の区切りは分けない）。読めなければ nil。
func commandSegments(cmd string) [][]string {
	toks, err := shlexSplit(strings.ReplaceAll(markHeredocs(cmd), "\n", " ; "))
	if err != nil {
		return nil
	}
	var segs [][]string
	var seg []string
	for _, t := range toks {
		if t != "" && strings.Trim(t, ";&|()") == "" {
			if len(seg) > 0 {
				segs = append(segs, seg)
			}
			seg = nil
		} else {
			seg = append(seg, t)
		}
	}
	if len(seg) > 0 {
		segs = append(segs, seg)
	}
	return segs
}

// gitSub は git の語の並びからサブコマンドとその後ろを返す（-C・-c などの引数付きの前置きを飛ばす）。
func gitSub(seg []string) (string, []string) {
	i := 1
	for i < len(seg) {
		t := seg[i]
		switch t {
		case "-C", "-c", "--git-dir", "--work-tree", "--namespace":
			i += 2
			continue
		}
		if strings.HasPrefix(t, "-") {
			i++
			continue
		}
		return t, seg[i+1:]
	}
	return "", nil
}

// toolResponseText は bash 版と同じ形のツールの結果の文字列（文字列ならそのまま・tool_result の text_result_for_llm・
// それ以外は JSON）。Copilot CLI の camelCase の toolResult（textResultForLlm）も読む。
func toolResponseText(ev hookio.Event) string {
	var raw any
	if ev.Tool != nil && ev.Tool.Response != nil {
		raw = ev.Tool.Response.Raw
	}
	if v, ok := ev.Raw["tool_response"]; ok && v != nil {
		raw = v
	} else if m, ok := ev.Raw["tool_result"].(map[string]any); ok {
		raw = m["text_result_for_llm"]
	} else if m, ok := ev.Raw["toolResult"].(map[string]any); ok {
		raw = m["textResultForLlm"]
	}
	switch v := raw.(type) {
	case nil:
		return ""
	case string:
		return v
	}
	return toJSON(raw)
}

// toJSON は ASCII 以外をそのまま出す JSON の文字列（区切りは ", " と ": "）。
func toJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	// 区切りの形は判定（正規表現）に影響しない。読みやすさのため以前の CLI の出力に寄せる
	var sb strings.Builder
	inStr, esc := false, false
	for _, c := range string(b) {
		sb.WriteRune(c)
		switch {
		case esc:
			esc = false
		case c == '\\' && inStr:
			esc = true
		case c == '"':
			inStr = !inStr
		case !inStr && (c == ',' || c == ':'):
			sb.WriteByte(' ')
		}
	}
	return sb.String()
}

// pendingPath はマーカーのパス（session_id が無ければ共有のマーカー）。
func pendingPath(ev hookio.Event, state string) string {
	if sid := sessionFile(ev); sid != "" {
		return filepath.Join(state, "handoff-pending.d", sid)
	}
	return filepath.Join(state, "handoff-pending")
}

// PostWorkCompleteHandoffMark は作業完了のイベントを積む（引き継ぎ鮮度ガードの 1 段目）。
func PostWorkCompleteHandoffMark(ctx context.Context, ev hookio.Event) (hookio.Result, error) {
	e := envFrom(ctx)
	if ev.Tool == nil {
		return hookio.Result{}, nil
	}
	r, state := root(ev, e), stateDir(ev, e)
	ti := toolInput(ev)
	resp := toolResponseText(ev)
	tool := ev.Tool.RawName

	// 完了のイベントは言語に依らないキー（kind）と引数で持つ。マーカーのファイルに残って
	// あとから別の言語で読まれることがあるため、文面ではなくキーを書く（表示は handoffEvent が訳す）。
	//
	// 1 回のツールの呼び出しで完了が複数起きる（`looptrack issue status A Done; looptrack issue status B Done`）ので、
	// 見つけたものを**ためて全部書く**。1 つの変数に上書きすると最後の 1 件しか残らず、ほかは黙って記録から消える。
	var events [][]string
	addEvent := func(fields ...string) {
		line := strings.Join(fields, "\t")
		for _, prev := range events {
			if strings.Join(prev, "\t") == line {
				return // 同じ呼び出しの中の重複は 1 件にまとめる
			}
		}
		events = append(events, fields)
	}
	if shellTools[tool] || ev.Tool.Kind == hookio.KindBash {
		// コミットした作業ツリーを後で問い合わせるため、`cd <dir>` と `-C <dir>` を追う。
		// 記録は本体の <状態> に集まるので、別の作業ツリーでのコミットは本体の履歴には見えない。
		dir := ev.CWD
		if dir == "" {
			dir = e.Getwd()
		}
		commitDir := ""
		for _, seg := range commandSegments(toStr(ti["command"])) {
			// 環境変数の代入と、直後がコマンドの位置になる予約語を、先頭から剥がす
			// （`do FOO=1 looptrack …` のように混ざるので、1 つのループで両方見る）。
			for len(seg) > 0 && (envAssignRe.MatchString(seg[0]) || shellKeywords[seg[0]]) {
				seg = seg[1:]
			}
			if len(seg) == 0 {
				continue
			}
			head := baseName(seg[0])
			if head == "cd" {
				if len(seg) > 1 && seg[1] != "-" {
					dir = joinDir(dir, seg[1])
				}
				continue
			}
			if head == "git" {
				if sub, rest := gitSub(seg); sub == "commit" && !contains(rest, "--dry-run") {
					addEvent("commit")
					commitDir = gitDir(dir, seg)
				}
				continue
			}
			// looptrack issue …（args[0] が "issue"・args[1] がサブコマンド）
			if head != "looptrack" && head != "looptrack.exe" {
				continue
			}
			args := seg[1:]
			if len(args) < 2 || args[0] != "issue" {
				continue
			}
			// --help / -h を含む呼び出しは使い方を表示するだけで状態を変えない
			if contains(args[1:], "--help") || contains(args[1:], "-h") {
				continue
			}
			switch {
			case args[1] == "close":
				id := "?"
				if len(args) > 2 {
					id = args[2]
				}
				if id = literalID(id); statusApplied(ev, id, "Done", false) {
					addEvent("issue.close", id)
				}
			case args[1] == "status" && len(args) > 3 && (args[3] == "Done" || args[3] == "Canceled"):
				if id := literalID(args[2]); statusApplied(ev, id, args[3], false) {
					addEvent("issue.status", args[3], id)
				}
			}
		}
		for i, done := range events {
			if len(done) != 1 || done[0] != "commit" {
				continue
			}
			// addEvent が重複を畳むので "commit" は高々 1 件
			if commitCounts(ctx, ev, e, r, resp) {
				events[i] = append([]string{"commit"}, commitFields(ctx, e, commitDir, resp)...)
			} else {
				events = append(events[:i:i], events[i+1:]...)
			}
			break
		}
	} else if mcpSetStatus.MatchString(tool) {
		if st := toStr(ti["status"]); st == "Done" || st == "Canceled" {
			id := toStr(ti["id"])
			if id == "" {
				id = toStr(ti["issue_id"])
			}
			if id == "" {
				id = "?"
			}
			if statusApplied(ev, id, st, true) {
				addEvent("issue.status", st, id)
			}
		}
	}
	if len(events) == 0 {
		return hookio.Result{}, nil
	}

	pend := pendingPath(ev, state)
	if err := os.MkdirAll(filepath.Dir(pend), 0o777); err != nil {
		return hookio.Result{}, nil
	}
	f, err := os.OpenFile(pend, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o666)
	if err != nil {
		return hookio.Result{}, nil
	}
	lang := e.lang()
	now := e.Now().Local().Format("2006-01-02 15:04:05")
	var lines strings.Builder
	shown := make([]string, 0, len(events))
	for _, fields := range events {
		fmt.Fprintf(&lines, "%s\t%s\n", now, strings.Join(fields, "\t"))
		shown = append(shown, handoffEvent(lang, fields))
	}
	// 1 回で書く（O_APPEND なので、ほかのセッションの行と混ざらない）
	_, werr := f.WriteString(lines.String())
	if cerr := f.Close(); werr != nil || cerr != nil {
		return hookio.Result{}, nil
	}
	msg := i18n.T(lang, "loop.handoff.detected", "event", strings.Join(shown, i18n.T(lang, "loop.handoff.event.sep")))
	// 印で判定するときは、実物の行をここで渡す（AI は自分のセッション ID を知らないので、文面で説明するだけでは書けない）
	backend := trimSpace(e.env("LOOPTRACK_LOOP_HANDOFF_BACKEND"))
	if (backend == "" || backend == "file") && handoffWriterMode(e) == writerSession {
		if sid := sessionFile(ev); sid != "" {
			msg += "\n" + i18n.T(lang, "loop.handoff.mark.howto", "mark", writerMark(sid))
		}
	}
	return hookio.Result{Context: msg}, nil
}

// literalID は、展開されていないシェルの変数・コマンド置換を含む ID を「不明」（"?"）に倒す。
//
// この hook が読むのは Bash ツールに渡された**コマンドの文字列**であって、シェルを実行した結果ではない。
// `for i in A B C; do looptrack issue status $i Done; done` のソーステキストに現れる ID は `$i` そのものなので、
// そのまま書くと「`$i` を Done にした」という、次のセッションには読めない記録が残る。**展開後の値は原理的に
// 追えない**ので、分からないことが分かる形にする（`?` は ID の無い close と同じ表し方）。
func literalID(id string) string {
	if strings.ContainsAny(id, "$`") {
		return "?"
	}
	return id
}

// cliFailure は、シェルの出力に CLI の失敗の行があること（looptrack のエラーの接頭辞 cli.err.prefix を、
// 対訳表から両方の言語で作る。CLI の言語は hook の言語と同じとは限らないため。Claude Code がシェルの非 0 の終了を
// 文字列で渡すときの「Exit code N」も数える）。
var cliFailure = regexp.MustCompile(`(?m)^(?:` +
	regexp.QuoteMeta(i18n.T(i18n.JA, "cli.err.prefix", "message", "")) + `|` +
	regexp.QuoteMeta(i18n.T(i18n.EN, "cli.err.prefix", "message", "")) + `)|^Exit code [1-9]`)

// statusApplied は、見つけたイシューの完了（close・status Done / Canceled）を「実際に状態が変わった」と見るかを返す。
//
// 打ったコマンド（ツールへの入力）だけで積むと、サーバに拒否された close（受け入れ条件が雛形のまま等）まで完了として
// 積まれ、していない作業について引き継ぎを要求してしまう。そこで commitCounts と同じく応答を見る。判定の順:
//
//  1. 応答に成功の行（サーバの状態変更のメッセージ「<ID>: <前> → <後>」。言語に依らない）があれば積む。
//     1 回の呼び出しに複数のコマンドが並ぶとき、ほかのコマンドの失敗に巻き込まれないよう、先に ID ごとに見る。
//  2. 応答が構造で失敗を示す（isError・result_type が success 以外。Codex・Copilot）なら積まない。
//  3. 出力に CLI の失敗の行があれば積まない。
//  4. MCP で、応答に成否の欄が無い（Claude Code は content の配列だけを渡す）のに本文があって成功の行が無いなら積まない。
//     MCP の応答はサーバの文面そのもので、成功なら必ず 1. の行を含むため。
//  5. それ以外（出力が空・知らない形・出力を捨てた `> /dev/null` など）は積む。判定できないときに記録を落とすと
//     本当の完了の引き継ぎが要求されなくなるので、commitCounts の「出力が無ければ積む」と同じ側に倒す。
//     シェルの出力は加工されうる（パイプ・リダイレクト）ので、CLI では成功の行が無いことだけでは失敗と見ない。
func statusApplied(ev hookio.Event, id, status string, mcp bool) bool {
	text := toolResponseText(ev)
	var r *hookio.Response
	if ev.Tool != nil {
		r = ev.Tool.Response
	}
	if r != nil && r.Text != "" {
		text = r.Text // JSON に包まれていない本文（改行で行頭を見られる）
	}
	idPat := `[A-Za-z][A-Za-z0-9]*-[0-9]+`
	if id != "?" {
		idPat = regexp.QuoteMeta(id)
	}
	applied := regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_-])` + idPat + `: [^\n]*?→ ` + regexp.QuoteMeta(status) + `\b`)
	switch {
	case applied.MatchString(text):
		return true
	case r != nil && r.Failed:
		return false
	case cliFailure.MatchString(text):
		return false
	case mcp && trimSpace(text) != "" && !hasOutcome(r):
		return false
	}
	return true
}

// hasOutcome は、ツールの結果が成否の欄（isError / is_error / resultType / result_type）を持つか。
func hasOutcome(r *hookio.Response) bool {
	if r == nil {
		return false
	}
	m, ok := r.Raw.(map[string]any)
	if !ok {
		return false
	}
	for _, k := range []string{"isError", "is_error", "resultType", "result_type"} {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}

// commitCounts は、見つけた git commit を「積む価値のある完了」と見るかを返す。
//
// 出力から失敗が分かるとき（nothing to commit・成功の形が無い）と、引き継ぎのファイルだけのコミットのときは false。
// 出力が無いときは判定できないので積む側に倒す。
func commitCounts(ctx context.Context, ev hookio.Event, e *Env, r, resp string) bool {
	// 成功したコミットだけ（出力がある場合。無いときは積む）
	if trimSpace(resp) != "" {
		if notCommitted.MatchString(resp) || !commitSuccess.MatchString(resp) {
			return false
		}
	}
	relHandoff := relpath(realpath(handoffFile(ev, e)), realpath(r))
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	out, _, err := e.run(c, e.Getwd(), nil, "git", "-C", r, "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD")
	cancel()
	var files []string
	if err == nil {
		files = strings.Fields(string(out))
	}
	for _, f := range files {
		if f != relHandoff { // どちらも / 区切り（git の出力と relpath）
			return true
		}
	}
	return len(files) == 0
}

// commitHead はコミットの成功の行（`[<ブランチ> <短い SHA>] <件名>`。初回は `[main (root-commit) …]`、
// detached HEAD では `[detached HEAD …]`）。
var commitHead = regexp.MustCompile(`(?m)^\[([^\]\n]+?) ([0-9a-f]{7,})\]`)

// joinDir は cd・-C の引数を今の場所に継ぐ。`~` や変数で始まるなど読めないものは空（場所は分からない）にする。
func joinDir(base, d string) string {
	switch {
	case d == "" || strings.HasPrefix(d, "~") || strings.ContainsAny(d, "$`"):
		return ""
	case filepath.IsAbs(d):
		return d
	case base == "":
		return ""
	}
	return filepath.Join(base, d)
}

// gitDir は git の語の並びの -C（重ねて書けば順に継ぐ）を dir に当てた場所。
func gitDir(dir string, seg []string) string {
	for i := 1; i+1 < len(seg); i++ {
		switch seg[i] {
		case "-C":
			dir = joinDir(dir, seg[i+1])
			i++
		case "-c", "--git-dir", "--work-tree", "--namespace":
			i++
		default:
			if !strings.HasPrefix(seg[i], "-") {
				return dir
			}
		}
	}
	return dir
}

// commitFields は、積むコミットの短い SHA・ブランチ・作業ツリーのパス（記録の追加の欄）。
//
// 記録は本体の <状態> に集まるので、別の作業ツリーの（main に未マージの）コミットは本体の履歴に出ない。
// 欄が無いと、正当な記録が「無いはずのコミット」に見える。受け取った人が `git -C <作業ツリー> show <SHA>` に
// その場で当たれるよう、どこの・どのブランチの・どのコミットかを残す。
//
// SHA とブランチは、まずコミットの出力の `[<ブランチ> <SHA>]` から取る（そのコミットそのもの。後で HEAD が動いても変わらない）。
// 出力に無ければ、コミットした場所に問う。detached HEAD のブランチは git の表し方に合わせて `HEAD` にする。
// 取れなかった欄は `?` で埋め、**記録そのものは落とさない**（git が無い・場所が分からない・時間切れのときも積む）。
func commitFields(ctx context.Context, e *Env, dir, resp string) []string {
	sha, branch, worktree := "?", "?", "?"
	if ms := commitHead.FindAllStringSubmatch(resp, -1); len(ms) > 0 {
		m := ms[len(ms)-1] // 1 回の呼び出しに複数あれば最後のコミット
		sha, branch = m[2], strings.TrimSuffix(m[1], " (root-commit)")
		if branch == "detached HEAD" {
			branch = "HEAD"
		}
	}
	if dir != "" {
		c, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		ask := func(args ...string) string {
			out, code, err := e.run(c, e.Getwd(), nil, "git", append([]string{"-C", dir}, args...)...)
			if err != nil || code != 0 {
				return "?"
			}
			return trimSpace(string(out))
		}
		worktree = ask("rev-parse", "--show-toplevel")
		if sha == "?" {
			sha = ask("rev-parse", "--short", "HEAD")
		}
		if branch == "?" {
			branch = ask("rev-parse", "--abbrev-ref", "HEAD")
		}
	}
	fields := []string{sha, branch, worktree}
	for i, f := range fields {
		// 記録はタブ区切り・1 行 1 件なので、欄の中の空白の並び（タブ・改行）を 1 つの空白に潰す
		if f = strings.Join(strings.Fields(f), " "); f == "" {
			f = "?"
		}
		fields[i] = f
	}
	return fields
}

// handoffEvent は積んだイベント（キーと引数）を利用者の言語の 1 行にする。
//
// 知らないキーはそのまま出す。以前の版は文面そのもの（「コミット」「イシュー Done: TST-0009」）を
// 書いていたので、その行が残っていても読めるようにしておく。
func handoffEvent(lang i18n.Lang, fields []string) string {
	switch {
	case len(fields) == 1 && fields[0] == "commit":
		// 以前の版の記録（SHA・ブランチ・作業ツリーの欄が無い）
		return i18n.T(lang, "loop.handoff.event.commit")
	case len(fields) == 4 && fields[0] == "commit" && fields[2] == "HEAD":
		return i18n.T(lang, "loop.handoff.event.commit_detached", "sha", fields[1], "worktree", fields[3])
	case len(fields) == 4 && fields[0] == "commit":
		return i18n.T(lang, "loop.handoff.event.commit_at", "sha", fields[1], "branch", fields[2], "worktree", fields[3])
	case len(fields) == 2 && fields[0] == "issue.close":
		return i18n.T(lang, "loop.handoff.event.issue_close", "id", fields[1])
	case len(fields) == 3 && fields[0] == "issue.status":
		return i18n.T(lang, "loop.handoff.event.issue_status", "status", fields[1], "id", fields[2])
	}
	return strings.Join(fields, " ")
}

var nonAlnum = regexp.MustCompile(`[^A-Za-z0-9]`)

// blockedPath は Copilot で差し戻した記録（差し戻したときのマーカーの中身の写し）のパス。
func blockedPath(ev hookio.Event, state string) string {
	if sid := sessionFile(ev); sid != "" {
		return filepath.Join(state, "handoff-blocked.d", sid)
	}
	return filepath.Join(state, "handoff-blocked")
}

// StopHandoffFreshness は、完了のイベントが積まれているのに引き継ぎが古いまま止まるのを差し戻す（引き継ぎ鮮度ガードの 2 段目）。
//
// Copilot: Copilot CLI は差し戻しの理由を次の利用者のメッセージとして渡して続ける（Stop の systemMessage は捨てる）。
// 同じ積み残しで何度も差し戻さないよう、差し戻したときのマーカーの中身を <状態>/handoff-blocked.d/<session_id> に写し、
// 中身が変わらない（新しい完了が積まれていない）間は差し戻さずに通す。マーカーが無くなったら（畳んだ・消した）写しも消す。
func StopHandoffFreshness(ctx context.Context, ev hookio.Event) (hookio.Result, error) {
	e := envFrom(ctx)
	res, err := stopHandoffFreshness(ctx, ev, e)
	if err != nil || ev.StopHookActive {
		return res, err
	}
	// 本体以外の作業ツリーに取り残された引き継ぎを知らせる（止めない）
	if res.SystemMessage == "" {
		res.SystemMessage = strayHandoffNotice(ev, e)
	}
	return res, nil
}

func stopHandoffFreshness(ctx context.Context, ev hookio.Event, e *Env) (hookio.Result, error) {
	if ev.StopHookActive {
		return hookio.Result{}, nil
	}
	// 「更新が遅れている」より先に「前回あった記録が無い」を見る（別の文言で知らせる。recordloss.go）。
	// マーカーが積まれていなくても消失は知らせるので、鮮度の判定より前に置く
	if res := StopRecordLoss(ev, e); !res.IsZero() {
		return res, nil
	}
	r, state := root(ev, e), stateDir(ev, e)
	lang := e.lang()
	pend := pendingPath(ev, state)
	blocked := blockedPath(ev, state)
	st, err := os.Stat(pend)
	if err != nil || st.Size() == 0 {
		_ = os.Remove(blocked)
		return hookio.Result{}, nil
	}
	pendMT := st.ModTime()
	text, err := readText(pend)
	if err != nil {
		return hookio.Result{}, nil
	}
	var events []string
	for _, l := range textLines(text) {
		if trimSpace(l) != "" {
			events = append(events, l)
		}
	}

	backend := trimSpace(e.env("LOOPTRACK_LOOP_HANDOFF_BACKEND"))
	if backend == "" {
		backend = "file"
	}
	var fresh bool
	var where string
	var byMark bool // 印で判定したか（差し戻しの文に印の行を添えるかの判断に使う）
	switch backend {
	case "command":
		cmd := e.env("LOOPTRACK_LOOP_HANDOFF_CHECK_CMD")
		if cmd == "" {
			return hookio.Result{}, nil
		}
		c, cancel := context.WithTimeout(ctx, 8*time.Second)
		_, rc, err := e.run(c, r, []string{"LOOPTRACK_LOOP_HANDOFF_PENDING=" + pend}, "bash", "-c", cmd)
		cancel()
		if err != nil || (rc != 0 && rc != 1) {
			return hookio.Result{}, nil
		}
		fresh = rc == 0
		where = i18n.T(lang, "loop.handoff.where.command")
	case "auto-memory", "file":
		// 後方互換: 引き継ぎに印が 1 つも無いファイル（印を知らない既存のプロジェクト）は、従来どおり
		// 更新時刻で判定する。印が 1 つでも入っているファイルだけ「自分の印のある更新か」で厳密に見る。
		// こうしないと、配布物を更新しただけの既存のプロジェクトが、普通に引き継ぎを更新しても一斉に
		// 差し戻される（模擬環境の実測で再現した）。
		if backend == "file" && handoffWriterMode(e) == writerSession && handoffHasAnyWriterMark(ev, e) {
			if sid := sessionFile(ev); sid != "" {
				byMark = true
				fresh, where = freshByWriterMark(ev, e, lang, sid, pendMT)
				break
			}
		}
		targets := handoffTargets(ev, e, backend, r)
		// 見るのは「いちばん新しく更新されたもの」。auto-memory は、AI が本体の記憶に書くか
		// 作業ツリーの記憶に書くかが AI 側の都合で決まるので、どちらかが新しければ更新済みと見る
		// （片方だけを見ると、書いたのに差し戻す＝作業を止める）。
		var best string
		var bestMT time.Time
		for _, t := range targets {
			tst, err := os.Stat(t)
			if err != nil {
				continue
			}
			if best == "" || tst.ModTime().After(bestMT) {
				best, bestMT = t, tst.ModTime()
			}
		}
		if best != "" {
			fresh = !bestMT.Before(pendMT)
			where = i18n.T(lang, "loop.handoff.where.mtime", "path", best, "time", bestMT.Local().Format("2006-01-02 15:04:05"))
		} else {
			where = i18n.T(lang, "loop.handoff.where.missing", "path", targets[0])
		}
	default:
		return hookio.Result{}, nil // 知らないバックエンドは判定しない
	}

	if fresh {
		_ = os.Remove(pend)
		_ = os.Remove(blocked)
		return hookio.Result{}, nil
	}
	if ev.Agent == hookio.Copilot {
		if b, err := os.ReadFile(blocked); err == nil && string(b) == text {
			return hookio.Result{}, nil // 同じ積み残し（マーカーの中身が同じ）では 1 回だけ差し戻す
		}
		if os.MkdirAll(filepath.Dir(blocked), 0o777) == nil {
			_ = os.WriteFile(blocked, []byte(text), 0o666)
		}
	}
	shown := filepath.ToSlash(pend) // コマンドに埋めるので区切りは /（relpath を参照）
	if strings.HasPrefix(realpath(pend), realpath(r)+string(filepath.Separator)) {
		shown = relpath(pend, r)
	}
	if len(events) > 10 {
		events = events[len(events)-10:]
	}
	var list []string
	for _, ev := range events {
		ts, rest, found := strings.Cut(ev, "\t")
		if !found {
			list = append(list, "  - "+ev)
			continue
		}
		list = append(list, "  - "+ts+" "+handoffEvent(lang, strings.Split(rest, "\t")))
	}
	reason := i18n.T(lang, "loop.handoff.stop.reason", "list", strings.Join(list, "\n"), "where", where, "pending", shown)
	if byMark {
		if sid := sessionFile(ev); sid != "" {
			reason += "\n\n" + i18n.T(lang, "loop.handoff.mark.howto", "mark", writerMark(sid))
		}
	}
	// LOOPTRACK_LOOP_NO_BLOCK=1（--no-block）のときは hookio.Render が systemMessage に回す
	return hookio.Result{Block: reason}, nil
}

// 引き継ぎを「誰が書いたか」の判定（LOOPTRACK_LOOP_HANDOFF_WRITER）。
//
//   - session（既定・file バックエンドのみ）: 引き継ぎの本文に**印が 1 つでもあれば**、
//     **このセッションの印**で判定する。印が 1 つも無いファイルは mtime と同じに扱う（後方互換）。
//   - mtime: 最終更新時刻だけで判定する（以前の挙動）。
//
// 本体の作業ツリーに 1 本だけ置く形にしたので、引き継ぎのファイルは全セッション・全作業ツリーで同じ 1 つになった。
// 最終更新時刻だけで見ると、**ほかのセッションが書いただけで自分のマーカーが「更新済み」として畳まれ**、
// 自分の引き継ぎは黙って書かれないまま終わる。逆に、自分の申し送りを同じディレクトリの別のファイルに書いた
// セッションは「更新していない」と差し戻される。どちらも時刻からは書き手が分からないことが原因なので、
// **本文に書き手の印を入れてもらい、その印で判定する**（印の入った行は skill session-handoff が案内し、
// 作業完了のときと差し戻しのときに hook が実物の行をそのまま示す）。
const (
	writerSession = "session"
	writerMtime   = "mtime"
)

func handoffWriterMode(e *Env) string {
	if trimSpace(e.env("LOOPTRACK_LOOP_HANDOFF_WRITER")) == writerMtime {
		return writerMtime
	}
	return writerSession
}

// writerMark は引き継ぎの本文に入れてもらう、このセッションの印。
func writerMark(sid string) string { return writerMarkPrefix + sid + writerMarkSuffix }

// 印の形（見出しと閉じ）。印は 2 通りある。
//
//	<!-- looptrack:session <ID> -->                      時刻なし（古い形）
//	<!-- looptrack:session <ID> <RFC3339> -->            時刻つき
//
// なぜ時刻が要るか: 引き継ぎは全セッションで同じ 1 ファイルなので、ほかのセッションが追記するだけで
// ファイルの更新時刻は進む。「ファイルが完了より新しい」＋「自分の印がある」の 2 つだけで見ると、
// **一度書いた印が以後ずっと「今回も書いた証拠」として働く**（印自体は時刻を持たないので、いつ書かれたかが分からない）。
// 時刻つきの印なら、その時刻を完了のイベントと比べられる。
//
// **読める側（この判定）を先に入れ、書く側（追記の CLI）を後から時刻つきに切り替える。** 逆順にすると、
// 判定が印の全体の完全一致で見ている間に印の末尾が変わり、**印を書いたセッションが一斉に差し戻される**。
// しかも判定側のテストは自分の形の印しか与えないので、**テストは緑のまま**その状態になる。
//
// writerMarkPrefix は「どのセッションの印かを問わず、印が入っているか」を見るのにも使う。
//
// **この 2 つが、印の形の唯一の定義。** 書く側（handoff append / compact --drop-marks）も、
// 自分で同じ文字列を持たずにここを参照する（2 か所に置くと、片方を変えただけで判定が黙って外れる）。
// 重複が戻っていないことは TestWriterMarkLiteralDefinedOnce が原本を走査して確かめる。
const (
	writerMarkPrefix = "<!-- looptrack:session "
	writerMarkSuffix = " -->"
)

// writerMarkAt は時刻つきの印（新しい形）。書く側を切り替えるときに使う。
//
// 形を 1 か所に集めておく（書く側と読む側が別々に形を持つと、片方を変えただけで判定が黙って外れる）。
func writerMarkAt(sid string, t time.Time) string {
	return writerMarkPrefix + sid + " " + t.Format(time.RFC3339) + writerMarkSuffix
}

// writerMarkTimes は本文にある、このセッションの印を読む。
//
// 返すのは、時刻つきの印の時刻（複数可）と、時刻の無い印があったか。
// 時刻として読めない中身（知らない形）は印として数えない。
func writerMarkTimes(text, sid string) ([]time.Time, bool) {
	var times []time.Time
	timeless := false
	head := writerMarkPrefix + sid
	for i := 0; ; {
		j := strings.Index(text[i:], head)
		if j < 0 {
			return times, timeless
		}
		i += j + len(head)
		// ID の直後は必ず空白（別のセッションの ID の先頭一致を弾く。`<ID>` と `<ID>-2` は別物）
		if i >= len(text) || text[i] != ' ' {
			continue
		}
		line := text[i:]
		if nl := strings.IndexByte(line, '\n'); nl >= 0 {
			line = line[:nl]
		}
		k := strings.Index(line, writerMarkSuffix)
		if k < 0 {
			continue // 同じ行で閉じていないものは印ではない
		}
		switch mid := trimSpace(line[:k]); {
		case mid == "":
			timeless = true
		default:
			if ts, err := time.Parse(time.RFC3339, mid); err == nil {
				times = append(times, ts)
			}
		}
	}
}

// handoffHasAnyWriterMark は引き継ぎのファイルに（どのセッションのものでもよいので）印が入っているかを返す。
//
// 印を知らない既存のプロジェクトの引き継ぎには 1 つも入っていないので、そこでは厳密な判定に切り替えない
// （切り替えると、配布物を更新しただけで、普通に更新しても差し戻されるようになる）。
// 印を使い始めたプロジェクト（＝誰かが 1 度でも印を書いたファイル）だけが厳密になる。
func handoffHasAnyWriterMark(ev hookio.Event, e *Env) bool {
	t, err := readText(handoffFile(ev, e))
	return err == nil && strings.Contains(t, writerMarkPrefix)
}

// freshByWriterMark は「このセッションが今回の完了より後に書いたか」で鮮度を判定する。
//
// 見るのは引き継ぎのファイルと、同じディレクトリのほかの記憶（*.md）。判定は印の形で分かれる。
//
//   - 時刻つきの印: その**印の時刻**が完了のイベントより後なら「更新済み」。ほかのセッションの追記で
//     ファイルの更新時刻だけが進んでも、過去に書いた自分の印は通らない。
//     比べる相手は**秒に丸めた**完了の時刻。印は RFC3339（秒まで）なので、完了と同じ秒に書いた印は
//     切り捨てで完了より前に見える。丸めずに比べると、**完了の直後に書いたセッションだけが差し戻される**
//     （再現しにくく、原因も見えない）。
//   - 時刻の無い印（古い形）: 従来どおり、**ファイルの更新時刻**が完了より後なら「更新済み」
//     （印がいつ書かれたかは分からないので、これ以上は言えない。後方互換のために残す）。
//
// どちらの印も無い・印はあるが完了より古いときは「古い」。
func freshByWriterMark(ev hookio.Event, e *Env, lang i18n.Lang, sid string, pendMT time.Time) (bool, string) {
	h := handoffFile(ev, e)
	cands := []string{h}
	for _, p := range globMD(memoriesDir(ev, e, "")) {
		if realpath(p) != realpath(h) {
			cands = append(cands, p)
		}
	}
	for _, p := range cands {
		st, err := os.Stat(p)
		// 印は本文にあるので、ファイルが完了より古ければ印も古い（書けば更新時刻は必ず進む）
		if err != nil || !st.Mode().IsRegular() || st.ModTime().Before(pendMT) {
			continue
		}
		t, err := readText(p)
		if err != nil {
			continue
		}
		times, timeless := writerMarkTimes(t, sid)
		fresh := timeless
		// 印は秒までなので、完了の時刻も秒に丸めて比べる（同じ秒の書き込みを落とさない）
		pendSec := pendMT.Truncate(time.Second)
		for _, ts := range times {
			if !ts.Before(pendSec) {
				fresh = true
			}
		}
		if !fresh {
			continue
		}
		return true, i18n.T(lang, "loop.handoff.where.mark", "path", p, "time", st.ModTime().Local().Format("2006-01-02 15:04:05"))
	}
	return false, i18n.T(lang, "loop.handoff.where.nomark", "path", h, "mark", writerMark(sid))
}

// handoffTargets は鮮度の判定で見るファイル（先頭が代表。どれも無いときはこれを「ありません」と出す）。
//
//   - file: handoffFile（本体の作業ツリーで解決済み）の 1 つ。
//   - auto-memory: LOOPTRACK_LOOP_HANDOFF_MEMORY_DIR があればその 1 つ。無ければ**本体のルート**と**いまのルート**の 2 つ。
//     記憶の置き場は AI が自分の作業ディレクトリから決めるので、作業ツリーの中では別の MEMORY.md に書かれる。
//     本体だけを見ると「書いたのに差し戻す」ことになる。
func handoffTargets(ev hookio.Event, e *Env, backend, r string) []string {
	if backend != "auto-memory" {
		return []string{handoffFile(ev, e)}
	}
	if mem := e.env("LOOPTRACK_LOOP_HANDOFF_MEMORY_DIR"); mem != "" {
		return []string{filepath.Join(e.abs(mem), "MEMORY.md")}
	}
	home := e.env("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	var out []string
	seen := map[string]bool{}
	for _, d := range []string{memoriesRoot(ev, e), r} {
		p := filepath.Join(home, ".claude", "projects", nonAlnum.ReplaceAllString(realpath(d), "-"), "memory", "MEMORY.md")
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}
