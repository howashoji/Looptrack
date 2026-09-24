package loop

// pre-tool-subagent-bound: サブエージェントの起動（PreToolUse の Task / Agent）の指示文に、サブエージェントへの共通の注意
// （背景で待つループの上限・scratchpad の共有と計測用ファイルの衝突しない命名）をそのまま追記する（hookSpecificOutput.updatedInput）。
//
// なぜ要るか: rules（background-process.md）は**親セッションにしか注入されない**。親が毎回書き写さないと子へ降りず、
// 子は「親の完了マーカーを待つ」ために上限の無い until / while を自然に書く。実測では 1 日で 3 本作られ、うち 1 本は
// 待機対象のパスがそもそも存在しなかった。書き写しの手間を規律で埋めるのはもう試したので、配線で降ろす。
//
// 何もしないとき（この hook は**止めない**。追記だけ）:
//   - ツールがサブエージェントの起動でない
//   - 指示文が読めない（prompt が文字列でない）
//   - 指示文に既に印（backgroundBoundMarker）が入っている（二重に足さない。**語彙ではなく固定の印**で見る）
//   - その AI・イベントで入力を書き換えられない（hookio の capabilities。いま確かめてあるのは Claude Code だけ。
//     書き換えられない AI では hookio.Normalize が落とすので、結果として何も起きない）
//
// 追記したことは systemMessage で利用者にも見せる（黙って指示文を書き換えない）。

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/howashoji/looptrack/internal/hookio"
	"github.com/howashoji/looptrack/internal/i18n"
)

// backgroundBoundMarker は追記した文の先頭に置く固定の印。既に入っていれば足さない判定にも使う
// （「上限」「締切」などの語彙で判定すると、別の意味で同じ語を使った指示文に当たる）。
//
// 名は「背景プロセス」由来だが、いまは背景で待つループの上限だけでなく、サブエージェントへの共通の注意をまとめて運ぶ器に
// なっている（注意が増えても印は増やさない）。**名を変えない**: 既に印の入った指示文を見落として二重に追記する。
const backgroundBoundMarker = "[looptrack:background-bound]"

// subagentTools はサブエージェントを起動するツールの名前（Claude Code は Agent。Task は別名として同じものを指す）。
var subagentTools = map[string]bool{"Task": true, "Agent": true}

// PreToolSubagentBound は `looptrack hook pre-tool-subagent-bound`。
func PreToolSubagentBound(ctx context.Context, ev hookio.Event) (hookio.Result, error) {
	e := envFrom(ctx)
	if ev.Tool == nil || !subagentTools[ev.Tool.RawName] {
		return hookio.Result{}, nil
	}
	if !hookio.CanUpdateInput(ev.Agent, ev.Name) {
		return hookio.Result{}, nil // 書き換えられない AI では知らせも出さない（追記していないのに追記したと言わない）
	}
	ti := toolInput(ev)
	prompt, ok := ti["prompt"].(string)
	if !ok || trimSpace(prompt) == "" {
		return hookio.Result{}, nil
	}
	if hasBackgroundBound(prompt) {
		return hookio.Result{}, nil
	}
	lang := e.lang()
	updated := make(map[string]any, len(ti)+1)
	for k, v := range ti {
		updated[k] = v
	}
	updated["prompt"] = prompt + "\n\n" + i18n.T(lang, "loop.subagentbound.note", "mark", backgroundBoundMarker)
	b, err := json.Marshal(updated)
	if err != nil {
		return hookio.Result{}, nil
	}
	return hookio.Result{UpdatedInput: string(b),
		SystemMessage: i18n.T(lang, "loop.subagentbound.notice", "mark", backgroundBoundMarker)}, nil
}

// hasBackgroundBound は指示文に既に印が入っているか。
func hasBackgroundBound(prompt string) bool {
	return strings.Contains(prompt, backgroundBoundMarker)
}
