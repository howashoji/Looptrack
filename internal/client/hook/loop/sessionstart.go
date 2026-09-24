package loop

// session-start（Copilot 向けの配線だけで使う合成の hook）。kit/loop に対応する bash 版は無い。
//
// Copilot CLI（1.0.86 で実測）は、同じイベントの複数の hook が additionalContext を出すと最後の 1 つしか AI に渡さない。
// loop を入れた Copilot の配線では SessionStart に summary（core）と session-start-rules・memories・iteration が並ぶため、
// summary（3 層の要約）が消えていた。Copilot 向けの配線では SessionStart をこの 1 本にまとめ、各部分の文脈を 1 回の出力に合成する。
//
//	looptrack hook session-start --agent copilot --parts summary,session-start-rules,session-start-memories,session-start-iteration [--limit N]
//
//   - 部分は RegisterPart で登録した hook（core が summary を登録する）か loop の hook の名前。知らない名前は飛ばす。--limit などの残りの引数は summary に渡す
//     （loop の部分には引数を渡さない＝個別に配線したときと同じ）。
//   - 部分は並行に動かし、それぞれの登録表の打ち切り（Timeout）で諦める。エラー・panic・時間切れの部分は飛ばす（fail-open）。
//   - 文脈（Context）は --parts の順に空行で区切ってつなぐ。systemMessage も同じ。差し戻し・拒否は SessionStart では使わないので捨てる。
//   - Claude Code・Codex は複数の hook の文脈をすべて渡すので、この hook を配線しない（kitinit は Copilot の配線だけでまとめる）。
//
// user-prompt: UserPromptSubmit も同じ（Copilot CLI 1.0.86 は最後の 1 つの additionalContext しか渡さず、loop ありでは
// user-prompt-task-mode が出したターンで user-prompt-rules の 1 行が消えていた）。Copilot の配線では UserPromptSubmit を
//
//	looptrack hook user-prompt --agent copilot --parts user-prompt-rules,user-prompt-task-mode,session-scope-guard
//
// の 1 本にまとめる。部分の動かし方・文脈のつなぎ方は session-start と同じ。UserPromptSubmit では差し戻し（session-scope-guard の
// Block）も捨てずに --parts の順に空行でつなぐ（出せない AI では hookio.Render が systemMessage に回す＝個別の配線と同じ）。
// 部分には同じ入力（Event）をそのまま渡すので、Copilot の Stop の差し戻し（先頭が「[Stop hook の差し戻し]」）を task-mode・scope が
// 判定しない扱いはそのまま効く。

import (
	"context"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/hookio"
)

// CombinedSessionStart は SessionStart の合成の hook の名前。
const CombinedSessionStart = "session-start"

// CombinedUserPrompt は UserPromptSubmit の合成の hook の名前。
const CombinedUserPrompt = "user-prompt"

// combinedTimeout は合成の hook 全体の打ち切り（部分の打ち切りの最大 9 秒より長く、配線の timeout 15 秒より短い）。
const combinedTimeout = 12 * time.Second

// combined は manifest に無い合成の hook（Lookup では引けるが Names・Registry には出さない）。
var combined = map[string]Entry{
	CombinedSessionStart: {CombinedSessionStart, hookio.SessionStart, combinedTimeout, SessionStartCombined},
	CombinedUserPrompt:   {CombinedUserPrompt, hookio.UserPromptSubmit, combinedTimeout, UserPromptCombined},
}

// IsCombined は name が合成の hook（session-start・user-prompt）か。
func IsCombined(name string) bool {
	_, ok := combined[name]
	return ok
}

// CombinedFor は event の合成の hook の名前（無ければ ""）。
func CombinedFor(event hookio.Name) string {
	for n, e := range combined {
		if e.Event == event {
			return n
		}
	}
	return ""
}

// PartHook は合成の SessionStart の部分として呼ぶ、loop の外の hook（core の summary）。args は --parts 以外の引数（--limit など）。
type PartHook func(ctx context.Context, getenv hookio.Getenv, getwd func() string, now func() time.Time, args []string, ev hookio.Event) (hookio.Result, error)

type partEntry struct {
	timeout time.Duration
	run     PartHook
}

var partHooks = map[string]partEntry{}

// RegisterPart は loop の外の hook を合成の SessionStart の部分として登録する（core が init で summary を登録する。
// loop から core を import すると循環するため）。
func RegisterPart(name string, timeout time.Duration, h PartHook) {
	partHooks[name] = partEntry{timeout, h}
}

// CombinedParts は合成の hook の配線の --parts の値（部分の名前の並び）。
func CombinedParts(args []string) (parts []string, rest []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, v, hasEq := strings.Cut(a, "=")
		if name != "--parts" {
			rest = append(rest, a)
			continue
		}
		if !hasEq {
			if i+1 >= len(args) {
				break
			}
			i++
			v = args[i]
		}
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				parts = append(parts, p)
			}
		}
	}
	return parts, rest
}

// SessionStartCombined は --parts の hook を並行に動かし、文脈を 1 つにまとめて返す（差し戻しは捨てる）。
func SessionStartCombined(ctx context.Context, ev hookio.Event) (hookio.Result, error) {
	return runCombined(ctx, ev, false)
}

// UserPromptCombined は UserPromptSubmit の合成の hook。session-start と同じく部分をまとめ、差し戻しの理由もつなぐ。
func UserPromptCombined(ctx context.Context, ev hookio.Event) (hookio.Result, error) {
	return runCombined(ctx, ev, true)
}

// runCombined は --parts の hook を並行に動かし、文脈（keepBlock なら差し戻しの理由も）を --parts の順に 1 つにまとめる。
func runCombined(ctx context.Context, ev hookio.Event, keepBlock bool) (hookio.Result, error) {
	e := envFrom(ctx)
	parts, rest := CombinedParts(e.Args)
	type part struct {
		timeout time.Duration
		run     func(context.Context) (hookio.Result, error)
	}
	var runs []part
	for _, name := range parts {
		if name == CombinedSessionStart || name == CombinedUserPrompt {
			continue // 合成の hook（自分自身を含む）は呼ばない（combined を引くと初期化が循環する）
		}
		if ph, ok := partHooks[name]; ok {
			args := append([]string(nil), rest...)
			runs = append(runs, part{ph.timeout, func(c context.Context) (hookio.Result, error) { return ph.run(c, e.Getenv, e.Getwd, e.Now, args, ev) }})
			continue
		}
		le, ok := registry[name]
		if !ok {
			continue
		}
		sub := *e
		sub.Args = nil
		runs = append(runs, part{le.Timeout, func(c context.Context) (hookio.Result, error) { return le.Hook(WithEnv(c, &sub), ev) }})
	}
	type ret struct {
		r  hookio.Result
		ok bool
	}
	chans := make([]chan ret, len(runs))
	start := time.Now()
	for i, p := range runs {
		ch := make(chan ret, 1)
		chans[i] = ch
		go func(p part) {
			defer hookio.Recover(nil, nil)
			c, cancel := context.WithTimeout(ctx, p.timeout)
			defer cancel()
			r, err := p.run(c)
			if err != nil || c.Err() != nil {
				ch <- ret{}
				return
			}
			ch <- ret{r, true}
		}(p)
	}
	var contexts, msgs, blocks []string
	for i, p := range runs {
		wait := p.timeout - time.Since(start)
		if wait < 0 {
			wait = 0
		}
		t := time.NewTimer(wait + 50*time.Millisecond)
		select {
		case got := <-chans[i]:
			if got.ok {
				if s := strings.TrimSpace(got.r.Context); s != "" {
					contexts = append(contexts, s)
				}
				if s := strings.TrimSpace(got.r.SystemMessage); s != "" {
					msgs = append(msgs, s)
				}
				if keepBlock {
					b := got.r.Block
					if b == "" {
						b = got.r.Deny
					}
					if s := strings.TrimSpace(b); s != "" {
						blocks = append(blocks, s)
					}
				}
			}
		case <-t.C:
		case <-ctx.Done():
		}
		t.Stop()
	}
	return hookio.Result{Context: strings.Join(contexts, "\n\n"), SystemMessage: strings.Join(msgs, "\n\n"), Block: strings.Join(blocks, "\n\n")}, nil
}
