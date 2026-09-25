package loop

// pre-tool-subagent-model: サブエージェントの起動（PreToolUse の Task / Agent）で model が未指定なら deny し、
// 難しさに応じたモデルの選び方を理由の文面で示す。
//
// なぜ要るか: rules（working-discipline.md の「サブエージェント」節）は「任せるタスクの難しさに応じてモデルを
// 切り替える」と求めているが、文章だけでは守られない。実例として、要約だけの棚卸しの調査が model を指定しない
// まま親と同じ重いモデルで走った。背景プロセスの上限（pre-tool-subagent-bound）と同じく、規律を配線で担保する。
//
// 止めないもの:
//   - サブエージェントの起動でないツール
//   - tool_input.model が指定されている（空文字は未指定として扱う）
//   - subagent_type が fork（モデルは親を継ぐ）
//   - subagent_type の定義ファイル（作業ディレクトリの .claude/agents/<名>.md か ~/.claude/agents/<名>.md）の
//     frontmatter に model: があるとき（その型は既に固定のモデルを持つ）
//   - 例外の環境変数 LOOPTRACK_LOOP_SUBAGENT_MODEL_ALLOW（空白区切りの subagent_type 名。"*" は全部を外す）
//
// ask ではなく deny にする理由は pre-tool-git-guard・pre-tool-wait-loop-guard と同じ: bypass permissions の
// 対話セッションでは permissionDecision: ask が素通りする（working-discipline.md に実測がある）。呼び直しの
// 手間は model を 1 つ足すだけなので、誤って止めても作業は止まらない。
//
// looptrack を導入していないディレクトリ（プロジェクトの設定・LOOPTRACK_PROJECT・サーバへの接続が無い場所）
// でも同じに動く: 見るのはツールの入力・環境変数・ローカルの定義ファイルだけで、サーバへは一切出ない
// （root や issueCLI のようなサーバ・プロジェクト設定に触れる補助は呼ばない）。

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/howashoji/looptrack/internal/hookio"
	"github.com/howashoji/looptrack/internal/i18n"
)

// subagentModelFrontmatterRe は定義ファイルの frontmatter の中の model: の行（値が空でないもの）。
var subagentModelFrontmatterRe = regexp.MustCompile(`(?m)^model\s*:\s*\S`)

// subagentFrontmatterRe は Markdown の frontmatter（先頭の --- … --- の間）を取り出す。
var subagentFrontmatterRe = regexp.MustCompile(`(?s)\A---[ \t]*\r?\n(.*?)\r?\n---[ \t]*(?:\r?\n|\z)`)

// PreToolSubagentModel は `looptrack hook pre-tool-subagent-model`。
func PreToolSubagentModel(ctx context.Context, ev hookio.Event) (hookio.Result, error) {
	e := envFrom(ctx)
	if ev.Tool == nil || !subagentTools[ev.Tool.RawName] {
		return hookio.Result{}, nil
	}
	ti := toolInput(ev)
	if trimSpace(toStr(ti["model"])) != "" {
		return hookio.Result{}, nil
	}
	subagentType := trimSpace(toStr(ti["subagent_type"]))
	if subagentType == "fork" {
		return hookio.Result{}, nil
	}
	for _, w := range strings.Fields(e.env("LOOPTRACK_LOOP_SUBAGENT_MODEL_ALLOW")) {
		if w == "*" || (subagentType != "" && w == subagentType) {
			return hookio.Result{}, nil
		}
	}
	if subagentType != "" && subagentDefHasModel(ev, e, subagentType) {
		return hookio.Result{}, nil
	}
	return hookio.Result{Deny: i18n.T(e.lang(), "loop.subagentmodel.deny",
		"env", "LOOPTRACK_LOOP_SUBAGENT_MODEL_ALLOW")}, nil
}

// subagentDefHasModel は subagent_type の定義ファイル（作業ディレクトリの .claude/agents/<名>.md か
// ~/.claude/agents/<名>.md）の frontmatter に model: があるか（読めなければ false。サーバへは出ない）。
func subagentDefHasModel(ev hookio.Event, e *Env, subagentType string) bool {
	cwd := ev.CWD
	if cwd == "" {
		cwd = e.Getwd()
	}
	home := e.env("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	name := subagentType + ".md"
	for _, dir := range []string{cwd, home} {
		if dir == "" {
			continue
		}
		if frontmatterHasModel(filepath.Join(dir, ".claude", "agents", name)) {
			return true
		}
	}
	return false
}

// frontmatterHasModel は path の frontmatter に model: の行（値が空でない）があるか。
// 読めない・frontmatter が無い・model: の値が空のときは false（サーバへは出ない。ローカルのファイルだけ読む）。
func frontmatterHasModel(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	m := subagentFrontmatterRe.FindSubmatch(b)
	if m == nil {
		return false
	}
	return subagentModelFrontmatterRe.Match(m[1])
}
