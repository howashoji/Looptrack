package loop

// pre-tool-wait-loop-guard: 上限の無い待ちループを**起動する前に**止める。
//
// 止めるのは「`until` / `while` のループ本体に `sleep` があり、ループに上限の式が無い」形だけに限る。
// 上限の式 = `break` / `timeout` / `seq` / `SECONDS` / `date +%s` / 数の比較（`-ge` `-gt` `-le` `-lt`）/
// 回数・締切のオプション（`--max` `--deadline` `--timeout` `--retries` `--tries` `--attempts`）。
//
// なぜ起動前か: Stop の stop-runaway-background-process は経過時間でしか見られないので、閾値より前に作られたものは
// 原理的に見えない。実測（2026-09-20）で 1 日に作られた 3 本は、いずれも閾値（既定 30 分）に達する前に人が `ps` で
// 見つけたもので、hook では捕まらなかった。形は起動前に分かるので、起動前に見る。
//
// なぜ ask ではなく deny か: bypass permissions の対話セッションでは `ask` が素通りする（pre-tool-git-guard の
// 冒頭に実測がある）。上限つきへの書き直しは常にできるので、誤って止めても作業は止まらない。
// **そのため deny の文面には、通る形への書き直し方（最大回数・締切・timeout）を必ず入れる。**
//
// 当たらないもの（判定は構文だけで、意味を推し量らない）:
//   - ループでない長時間のコマンド（`docker compose up`・dev サーバ・`go test ./...`）。`until` / `while` を含まない
//   - 上限のある待ち（`for i in $(seq 1 60)`・`[ $n -ge 60 ] && break`・`timeout 600 …`）
//   - `sleep` の無いループ（`while read -r line; do … done`・`for f in *.go; do … done`）
//   - 引用符の中の文字列（`echo 'until x; do sleep 1; done'`）。ただし `bash -c '…'`・`eval "…"` の中身は外して見る
//
// 包んだ形も見る: 前置の語（`sudo` / `env` / `nohup` / `setsid` / `time` …。hookcmd.PrefixRun）の直後は
// コマンドの位置として扱い、入れ子のシェル（`bash -c '…'`・`eval "…"`）は hookcmd.UnwrapNestedShell でほどく。
// 実測: `sudo sh -c 'until false; do sleep 5; done'`・`nohup bash -c '…' &`・`setsid sh -c '…'`・
// `eval "while true; do sleep 1; done"` が素通りしていた。背景に回す定番の書き方（nohup・setsid）がそのまま抜け道だった。
// 何を前置の語・入れ子のシェルとみなすかは秘密のガードと共有する（hookcmd。規則を 1 か所に置く）。
// 前置の語は一覧に限る（任意の語にすると `echo sh -c '…'`・`git grep while` のような引数の位置で誤発火する）。
// **限界**: 値を別の語で取る選択肢（`sudo -u deploy sh -c '…'`）は前置として読み切れないので拾えない。
//
// 例外は LOOPTRACK_LOOP_WAITLOOP_ALLOW（空白区切り。コマンドの文字列に含まれれば通す。pre-tool-git-guard と同じ形）。
//
// 待つ先の存在について（**止めない**）: 「まだ無いファイルができるのを待つ」のは完了マーカー待ちの正しい使い方なので、
// 待つ先が無いことでは止めない。害だったのは「上限が無い」ことで、それは上の deny が止める。
// ただし**待つ先の親ディレクトリが無い**ときは、そこにファイルが作られる見込み自体が薄いので、止めずに注意を返す
// （additionalContext）。2026-09-20 の 3 本のうち 1 本は、待つ先のパスがそもそも存在しなかった。

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/howashoji/looptrack/internal/client/hook/hookcmd"
	"github.com/howashoji/looptrack/internal/hookio"
	"github.com/howashoji/looptrack/internal/i18n"
)

var (
	// waitLoopKwRe はコマンドの位置に現れた until / while（変数名・語の一部には当たらない）。
	// 前置の語（sudo ・ nohup ・ time …）の直後もコマンドの位置に数える（`time while …` はそのまま動くループ。
	// `sudo sh -c '…'` は引用符を外すと `sudo  until …` になる）。
	waitLoopKwRe = regexp.MustCompile(`(?:^|[;&|(){}\n]|\bdo\b|\bthen\b|\belse\b)[ \t]*` + hookcmd.PrefixRun + `\b(until|while)\b`)
	// waitLoopDoneRe はループの終わり。
	waitLoopDoneRe = regexp.MustCompile(`\bdone\b`)
	// waitLoopSleepRe はループ本体の待ち。
	waitLoopSleepRe = regexp.MustCompile(`(?:^|[;&|(){}\n \t])sleep\b`)
	// waitLoopBoundRe は上限の式。
	waitLoopBoundRe = regexp.MustCompile(`\bbreak\b|\btimeout\b|\bseq\b|\bSECONDS\b|date\s+\+\S*%s|-(ge|gt|le|lt)\b|` +
		`--(max|deadline|timeout|retries|tries|attempts)\b`)
	// waitLoopTimeoutRe はループの外から丸ごと打ち切る形（`timeout 600 bash -c '…'`）。
	waitLoopTimeoutRe = regexp.MustCompile(`\btimeout\b`)
	// waitLoopTargetRe は待っている先のパス（`until [ -s PATH ]`・`until test -f PATH`・`while [ ! -f PATH ]`）。
	waitLoopTargetRe = regexp.MustCompile(`^(?:until|while)\s+(?:!\s+)?(?:\[\[?|test)\s+(?:!\s+)?-[a-zA-Z]\s+([^\s\]]+)`)
)

// waitLoop は見つかった待ちループ 1 つ。
type waitLoop struct {
	body    string // ループの本文（until / while から done まで）
	bounded bool   // 上限の式があるか
	target  string // 待っている先のリテラルのパス（変数展開・glob・引用符の中は読まないので ""）
}

// waitLoopText は判定に使う形に直したコマンド。ヒアドキュメントの本文を外し、`bash -c '…'`・`eval "…"` の中身は引用符を外して
// 出し、残る引用符の中は空にする（引用符の中の文字列をコマンドと取り違えない）。
//
// ほどくのは秘密のガード・git ガードと同じ hookcmd.UnwrapNestedShell で、位置はコマンドの位置だけ（hookcmd.HeadOnly。
// git ガードと同じく deny なので狭く取る）。`echo sh -c 'while …'` のような引数の位置は、この段を共有に移す前から
// 止めていなかった（引用符を外しても、ループの語がコマンドの位置に来なかったため）。
// 前置の語は落とさない（hookcmd.Normalize は呼ばない）。`timeout 600 bash -c '…'` の `timeout` は
// ループの外からの上限なので、落とすと上限が見えなくなる。前置の語の直後は waitLoopKwRe が PrefixRun で見る。
func waitLoopText(cmd string) string {
	t := stripHeredocs(cmd)
	for i := 0; i < 3; i++ { // 入れ子（`bash -c "bash -c '…'"`・`eval "bash -c '…'"`）を数段だけ外す
		u := hookcmd.UnwrapNestedShell(t, hookcmd.HeadOnly)
		if u == t {
			break
		}
		t = u
	}
	return quotedRe.ReplaceAllString(t, `""`)
}

// waitLoops は cmd の中の待ちループ（`until` / `while` のループ本体に `sleep` があるもの）。
func waitLoops(cmd string) []waitLoop {
	t := waitLoopText(cmd)
	outer := waitLoopTimeoutRe.MatchString(t)
	var out []waitLoop
	for _, m := range waitLoopKwRe.FindAllStringSubmatchIndex(t, -1) {
		span := t[m[2]:]
		if d := waitLoopDoneRe.FindStringIndex(span); d != nil {
			span = span[:d[1]]
		}
		if !waitLoopSleepRe.MatchString(span) {
			continue
		}
		w := waitLoop{body: trimSpace(span), bounded: outer || waitLoopBoundRe.MatchString(span)}
		if g := waitLoopTargetRe.FindStringSubmatch(w.body); g != nil && isLiteralPath(g[1]) {
			w.target = g[1]
		}
		out = append(out, w)
	}
	return out
}

// unboundedWaitLoops は上限の無い待ちループの本文の並び（見つかった順）。stop-runaway-background-process も使う。
func unboundedWaitLoops(cmd string) []string {
	var out []string
	for _, w := range waitLoops(cmd) {
		if !w.bounded {
			out = append(out, w.body)
		}
	}
	return out
}

// isLiteralPath は、そのまま実在を確かめられるパスか（変数展開・glob・コマンド置換・引用符の中は確かめられない）。
func isLiteralPath(p string) bool {
	if p == "" || strings.ContainsAny(p, "$*?`\"'") {
		return false
	}
	return strings.ContainsAny(p, `/\`)
}

// PreToolWaitLoopGuard は `looptrack hook pre-tool-wait-loop-guard`。
func PreToolWaitLoopGuard(ctx context.Context, ev hookio.Event) (hookio.Result, error) {
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
	for _, w := range strings.Fields(e.env("LOOPTRACK_LOOP_WAITLOOP_ALLOW")) {
		if strings.Contains(cmd, w) {
			return hookio.Result{}, nil
		}
	}
	lang := e.lang()
	loops := waitLoops(cmd)
	for _, w := range loops {
		if !w.bounded {
			return hookio.Result{Deny: i18n.T(lang, "loop.waitloop.deny", "cmd", headStr(w.body, 160))}, nil
		}
	}
	// ここから先は止めない（上限はあるので規律には反していない）。待つ先の置き場が無いときだけ注意を返す。
	cwd := ev.CWD
	if cwd == "" {
		cwd = e.Getwd()
	}
	for _, w := range loops {
		if w.target == "" {
			continue
		}
		p := w.target
		if !filepath.IsAbs(p) {
			p = filepath.Join(cwd, p)
		}
		if dir := filepath.Dir(p); !isDir(dir) {
			return hookio.Result{Context: i18n.T(lang, "loop.waitloop.missing_dir", "path", w.target, "dir", dir)}, nil
		}
	}
	return hookio.Result{}, nil
}
