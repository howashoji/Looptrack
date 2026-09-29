package hookio

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
)

// MaxInput は読む入力の上限（これを超えた分は読み捨てる。Copilot の toolResult などに大きな本文が入ることがある）。
const MaxInput = 16 << 20

// Handler は hook の本体。error を返すと何も出さずに exit 0（fail-open）。
type Handler func(ev Event) (Result, error)

// RunOptions は Run の条件。
type RunOptions struct {
	Parse  ParseOptions
	Render RenderOptions
	// Timeout を超えたら何も出さずに exit 0 で終える（0 なら待ち続ける）。配線の timeout より短くする
	// （AI 側の時間切れは Copilot CLI の preToolUse では拒否になる＝ fail closed のため、先に自分で諦める）。
	// 時間切れの後も本体の goroutine は止まらない（Run の後すぐ os.Exit する前提。本体と共有する値は同期して扱う）。
	Timeout time.Duration
	// Debug なら、読めなかった入力・本体のエラー・panic を stderr に書く（終了コードは 0 のまま）。
	// 既定は何も書かない（VS Code などは stderr を利用者に見せることがある）。LOOPTRACK_HOOK_DEBUG=1 で有効にする想定。
	Debug bool
	// HookLog なら、判定を LogPath の返すファイルに 1 行ずつ追記する（LOOPTRACK_LOOP_HOOK_LOG=1。ParseArgs が読む）。
	// 判定・エラー・時間切れ・panic のすべてが Run を通るので、記録はここ 1 か所で書く（hook ごとに散らさない）。
	HookLog bool
	// HookName は記録に書く hook の名前（呼び出し側の登録表の名前）。
	HookName string
	// LogPath は記録のファイルのパス（イベントから決める。状態の置き場はプロジェクトのルートと AI で変わるため）。
	// nil か "" を返したら記録しない。
	LogPath func(ev Event) string
	// Now は記録の時刻（nil なら time.Now。テストで差し替える）。
	Now func() time.Time
}

// Run は hook を 1 回動かす: stdin を読んで Parse → Foreign なら何もしない → 本体 → Render → 書き出し。
// 戻り値は終了コード（呼び出し側は os.Exit に渡す）。入力が読めない・本体のエラー・panic・時間切れはすべて exit 0 で何も出さない。
//
//	func main() { os.Exit(hookio.Run(os.Stdin, os.Stdout, os.Stderr, opts, handler)) }
func Run(stdin io.Reader, stdout, stderr io.Writer, opts RunOptions, h Handler) (code int) {
	rec := &hookLog{opts: opts}
	defer rec.write()                       // Recover より後に動く（panic の後も 1 行書く）。書き込みの失敗は終了コードを変えない
	lang := i18n.FromEnv(opts.Parse.getenv) // 調べる人（LOOPTRACK_HOOK_DEBUG=1 を付けた人）の言語
	logf := func(format string, a ...any) {
		if opts.Debug && stderr != nil {
			for i, v := range a {
				if err, ok := v.(error); ok {
					a[i] = i18n.Text(lang, err) // ID を持つ error は文面にする（%v だと ID が出る）
				}
			}
			fmt.Fprintf(stderr, "hookio: "+format+"\n", a...)
		}
	}
	defer Recover(&code, func(p any, stack []byte) {
		logf("panic: %v\n%s", p, stack)
		rec.decision, rec.kind = DecisionPanic, ""
	})

	var input []byte
	if stdin != nil {
		b, err := io.ReadAll(io.LimitReader(stdin, MaxInput))
		if err != nil {
			logf("%s", i18n.T(lang, "hookio.debug.read_failed", "reason", err))
			rec.decision = DecisionError
			return 0
		}
		input = b
		_, _ = io.Copy(io.Discard, stdin) // 上限を超えた分を読み捨てる（書き手を詰まらせない）
	}
	ev, err := Parse(input, opts.Parse)
	if err != nil {
		logf("%v", err)
		rec.decision = DecisionError
		return 0
	}
	if ev.Foreign {
		return 0 // Claude Code 向けの配線を Copilot などが起動した（この hook の判定ではないので記録しない）
	}
	rec.ev, rec.parsed = ev, true

	res, failed := call(ev, h, opts.Timeout, logf)
	if failed != "" {
		rec.decision = failed
		return 0
	}
	rec.decision, rec.kind = decisionOf(Normalize(ev.Agent, ev.Name, res, opts.Render)), res.Kind
	if rec.decision == DecisionPass {
		rec.kind = "" // 何も出さないときは理由も無い
	}
	return Render(ev, res, opts.Render).Write(stdout, stderr)
}

// call は本体を呼ぶ。panic・エラー・時間切れのときは failed にその判定の語（DecisionPanic・DecisionError・DecisionTimeout）を返す。
func call(ev Event, h Handler, timeout time.Duration, logf func(string, ...any)) (Result, string) {
	type ret struct {
		r      Result
		failed string
	}
	run := func() (out ret) {
		out.failed = DecisionPanic // panic したら Recover の後にこの値のまま返る
		defer Recover(nil, func(p any, stack []byte) { logf("panic: %v\n%s", p, stack) })
		r, err := h(ev)
		if err != nil {
			logf("%v", err)
			return ret{failed: DecisionError}
		}
		return ret{r: r}
	}
	if timeout <= 0 {
		out := run()
		return out.r, out.failed
	}
	ch := make(chan ret, 1)
	go func() { ch <- run() }()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case out := <-ch:
		return out.r, out.failed
	case <-t.C:
		logf("%v", i18n.Errorf("hookio.debug.timeout", "timeout", timeout.String())) // logf が要求の言語の文面にする
		return Result{}, DecisionTimeout
	}
}

// Recover は defer で使い、panic を止めて終了コードを 0 にする（fail-open）。
// code が nil でなければ 0 を入れる。onPanic があれば panic の値とスタックを渡す（ログ用）。
//
//	func main() {
//		code := 0
//		defer func() { os.Exit(code) }()
//		defer hookio.Recover(&code, nil)
//		…
//	}
func Recover(code *int, onPanic func(p any, stack []byte)) {
	p := recover()
	if p == nil {
		return
	}
	if code != nil {
		*code = 0
	}
	if onPanic != nil {
		onPanic(p, debug.Stack())
	}
}

// ParseArgs は配線が hook に渡す共通の引数を読む（`looptrack hook <名前> --agent copilot --event postToolUse --no-block`）。
//
//	--agent claude-code|codex|copilot  どの AI の配線か（原則必須。無ければ推測＝予備）
//	--event <名前>                      イベント名（Copilot CLI の camelCase の配線では入力に入らないので必須）
//	--no-block                          差し戻さずに知らせるだけ（manifest の block: false）
//
// 環境変数 LOOPTRACK_LOOP_NO_BLOCK=1 も --no-block と同じ（今の bash の hook の配線との互換）。LOOPTRACK_HOOK_DEBUG=1 で Debug。
// LOOPTRACK_LOOP_HOOK_LOG=1 で HookLog（置き場と名前は呼び出し側が LogPath・HookName に入れる）。
// 知らない引数は rest に順に残す（hook ごとの引数）。--agent の値が知らないものなら error（呼び出し側は exit 0 にする）。
func ParseArgs(args []string, getenv Getenv) (opts RunOptions, rest []string, err error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	opts.Parse.Getenv = getenv
	lang := i18n.FromEnv(getenv)
	opts.Render.NoBlock = getenv("LOOPTRACK_LOOP_NO_BLOCK") == "1"
	opts.Debug = getenv("LOOPTRACK_HOOK_DEBUG") == "1"
	opts.HookLog = getenv(HookLogEnv) == "1"
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, v, hasEq := strings.Cut(a, "=")
		switch name {
		case "--agent", "--event":
			if !hasEq {
				if i+1 >= len(args) {
					return opts, rest, errors.New(i18n.T(lang, "hookio.err.no_value", "name", name))
				}
				i++
				v = args[i]
			}
			if name == "--event" {
				opts.Parse.Event = v
				continue
			}
			ag, ok := ParseAgent(v)
			if !ok {
				return opts, rest, errors.New(i18n.T(lang, "hookio.err.unknown_agent", "value", fmt.Sprintf("%q", v)))
			}
			opts.Parse.Agent = ag
		case "--no-block":
			if hasEq {
				rest = append(rest, a)
				continue
			}
			opts.Render.NoBlock = true
		default:
			rest = append(rest, a)
		}
	}
	return opts, rest, nil
}
