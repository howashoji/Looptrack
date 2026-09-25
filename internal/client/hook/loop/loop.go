// Package loop は kit/loop の hook と gates の Go 版（DESIGN.md §5-11）。
//
// 各 hook は `func(ctx, hookio.Event) (hookio.Result, error)` の関数で、AI ごとの入出力の違いは internal/hookio が吸収する。
// 名前 → 関数の登録表（Registry）を公開し、`looptrack hook <名前>` の配線（cmd/）は Main を呼ぶだけにする。
// 以前の bash 版（kit/loop/hooks/*.sh・scripts/gates.sh）は撤去した。
//
// 挙動は以前の bash 版に合わせて移した（kit/loop/verify の全ケースを Go の表駆動テストに移し、撤去までは bash 版と
// 出力・状態ファイルを突き合わせていた）。意図して変えたところは各ファイルの冒頭に書く。bash 版との共通の違い:
//
//   - どの AI かは配線の --agent（Event.Agent）で決める。bash 版は環境変数（LOOPTRACK_LOOP_AGENT・CODEX_THREAD_ID）で推した。
//   - Claude Code 以外が Claude Code 向けの配線を起動したときに何もしない判定は hookio.Run が行う（Event.Foreign）。
//   - 出力の形（Copilot の写し・差し戻せない AI での systemMessage への格下げ・LOOPTRACK_LOOP_NO_BLOCK）は hookio.Render が行う。
//     bash 版は LOOPTRACK_LOOP_NO_BLOCK を Stop の 3 本だけが見たが、Go 版は --no-block（か LOOPTRACK_LOOP_NO_BLOCK=1）を全 hook で同じに扱う。
//   - 正規表現は Go の RE2。利用者が環境変数で渡す正規表現（LOOPTRACK_LOOP_TASK_MODE_*_RE・LOOPTRACK_LOOP_RUNAWAY_ALLOW）に
//     先読み・後読み・後方参照があると RE2 では解釈できず、bash 版の「正規表現の誤り」と同じ扱い（その指定を無視）になる。
//   - git の共通ディレクトリ・作業ツリーのルートは .git を読んで求める（git を起動しない）。git diff-tree と make と
//     判定コマンド（bash -c）と issue の CLI だけは起動する（internal/client の下なので許される）。
package loop

import (
	"context"
	"errors"
	"io"
	"os"
	"sort"
	"time"

	"github.com/howashoji/looptrack/internal/client/proc"
	"github.com/howashoji/looptrack/internal/hookio"
	"github.com/howashoji/looptrack/internal/i18n"
)

// Hook は 1 本の hook の本体。error を返すと何も出さずに exit 0（fail-open・hookio.Run）。
type Hook func(ctx context.Context, ev hookio.Event) (hookio.Result, error)

// Entry は登録表の 1 行。
type Entry struct {
	// Name は `looptrack hook <名前>` の名前（kit/loop/hooks/<名前>.sh と同じ）。
	Name string
	// Event はこの hook を配線するイベント（manifest の event）。
	Event hookio.Name
	// Timeout は本体の打ち切り（manifest の timeout より短くする。超えたら何も出さずに exit 0）。
	Timeout time.Duration
	Hook    Hook
}

var registry = map[string]Entry{}

func register(e Entry) { registry[e.Name] = e }

func init() {
	for _, e := range []Entry{
		{"session-start-rules", hookio.SessionStart, 4 * time.Second, SessionStartRules},
		{"session-start-memories", hookio.SessionStart, 4 * time.Second, SessionStartMemories},
		{"session-start-iteration", hookio.SessionStart, 9 * time.Second, SessionStartIteration},
		{"session-start-worktrees", hookio.SessionStart, 9 * time.Second, SessionStartWorktrees},
		{"user-prompt-rules", hookio.UserPromptSubmit, 4 * time.Second, UserPromptRules},
		{"user-prompt-task-mode", hookio.UserPromptSubmit, 4 * time.Second, UserPromptTaskMode},
		{"session-scope-guard", hookio.UserPromptSubmit, 9 * time.Second, SessionScopeGuard},
		{"user-prompt-stale-base", hookio.UserPromptSubmit, 4 * time.Second, UserPromptStaleBase},
		{"pre-edit-task-mode-guard", hookio.PreToolUse, 4 * time.Second, PreEditTaskModeGuard},
		{"pre-tool-scope-guard", hookio.PreToolUse, 4 * time.Second, PreToolScopeGuard},
		{"pre-tool-secrets-guard", hookio.PreToolUse, 4 * time.Second, PreToolSecretsGuard},
		{"pre-tool-git-guard", hookio.PreToolUse, 4 * time.Second, PreToolGitGuard},
		{"pre-tool-subagent-bound", hookio.PreToolUse, 4 * time.Second, PreToolSubagentBound},
		{"pre-tool-subagent-model", hookio.PreToolUse, 4 * time.Second, PreToolSubagentModel},
		{"pre-tool-wait-loop-guard", hookio.PreToolUse, 4 * time.Second, PreToolWaitLoopGuard},
		{"post-work-complete-handoff-mark", hookio.PostToolUse, 4 * time.Second, PostWorkCompleteHandoffMark},
		{"stop-tool-markup-guard", hookio.Stop, 4 * time.Second, StopToolMarkupGuard},
		{"stop-handoff-freshness", hookio.Stop, 9 * time.Second, StopHandoffFreshness},
		{"stop-runaway-background-process", hookio.Stop, 4 * time.Second, StopRunawayBackgroundProcess},
		{"subagent-stop-runaway-background-process", hookio.SubagentStop, 4 * time.Second, StopRunawayBackgroundProcess},
	} {
		register(e)
	}
}

// Lookup は名前の hook を返す（manifest の hook と、Copilot 向けの合成の hook＝session-start・user-prompt）。
func Lookup(name string) (Entry, bool) {
	if e, ok := registry[name]; ok {
		return e, true
	}
	e, ok := combined[name]
	return e, ok
}

// Names は登録されている hook（manifest の hook。合成の hook は含めない）の名前（名前順）。
func Names() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Registry は名前 → 本体の表の写し（書き換えても登録表は変わらない）。
func Registry() map[string]Hook {
	out := make(map[string]Hook, len(registry))
	for n, e := range registry {
		out[n] = e.Hook
	}
	return out
}

// Main は `looptrack hook <名前> [--agent …] [--event …] [--no-block] [hook ごとの引数…]` の本体。戻り値は終了コード（常に 0 か
// hookio.Render の終了コード）。知らない名前・読めない引数・本体のエラー・panic・時間切れは何も出さずに 0（fail-open）。
// env が nil なら実際の環境（os.Getenv・os.Getwd・os/exec・proc.List）を使う。
func Main(ctx context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer, env *Env) int {
	return mainWith(ctx, name, args, stdin, stdout, stderr, env, nil)
}

// mainWith は Main に、読んだ引数の後で RunOptions を直す口を足したもの（テストが実物確認用の TrustUnconfirmed を立てる）。
func mainWith(ctx context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer, env *Env, adjust func(*hookio.RunOptions)) int {
	if env == nil {
		env = &Env{}
	}
	e := env.withDefaults()
	opts, rest, err := hookio.ParseArgs(args, e.Getenv)
	if err != nil {
		if opts.Debug && stderr != nil {
			io.WriteString(stderr, err.Error()+"\n")
		}
		return 0
	}
	entry, ok := Lookup(name)
	if !ok {
		if opts.Debug && stderr != nil {
			io.WriteString(stderr, i18n.T(env.lang(), "loop.err.unknown_hook", "name", name)+"\n")
		}
		return 0
	}
	e.Args = append([]string(nil), rest...)
	opts.Parse.Getwd = e.Getwd
	if opts.Parse.Event == "" {
		// 入力に hook_event_name が無い（手で動かした・Copilot CLI の camelCase で --event を忘れた）ときは配線先のイベントとみなす
		opts.Parse.Event = string(entry.Event)
	}
	opts.Timeout = entry.Timeout
	if adjust != nil {
		adjust(&opts)
	}
	return hookio.Run(stdin, stdout, stderr, opts, func(ev hookio.Event) (hookio.Result, error) {
		c, cancel := context.WithTimeout(WithEnv(ctx, e), entry.Timeout)
		defer cancel()
		r, err := entry.Hook(c, ev)
		// 打ち切りの ctx を受けて本体が途中の結果を返しても出さない（hookio.Run の時間切れと同じ扱い）。
		// hookio.Run のタイマーと本体の ctx は同じ長さなので、どちらが先に届くかで出力が変わっていた（CI で実際に起きた）。
		if errors.Is(c.Err(), context.DeadlineExceeded) {
			return hookio.Result{}, i18n.Errorf("loop.err.timeout", "limit", entry.Timeout.String())
		}
		return r, err
	})
}

// Env は hook が使う環境（テストで差し替える）。零値の項目は実際の環境になる。
type Env struct {
	// Getenv は環境変数の読み取り。
	Getenv hookio.Getenv
	// Environ は起動するコマンドに渡す環境（KEY=VALUE の並び）。hook が足す値はこの後ろに付く。
	Environ func() []string
	// Getwd は作業ディレクトリ（入力に cwd が無いときのプロジェクトの推定・相対パスの基準）。
	Getwd func() string
	// Args は hook ごとの引数（ParseArgs が読まなかった残り。rules のディレクトリ・--prompt など）。
	Args []string
	// Now は今の時刻。
	Now func() time.Time
	// Run はコマンドを起動する（issue の CLI・git・make・判定コマンド）。
	Run Runner
	// Procs はプロセスの一覧（runaway の検知）。
	Procs func(ctx context.Context) ([]proc.Proc, error)
	// SelfPID は自分のプロセスの PID（runaway の検知で祖先をたどる起点）。
	SelfPID int
}

// Command は起動するコマンド。
type Command struct {
	Dir  string
	Env  []string // 環境の全部（KEY=VALUE。nil なら親と同じ。Env.run は Environ の後ろに hook が足す値を付けて渡す＝同じ名前は後ろが勝つ）
	Name string
	Args []string
}

// Runner はコマンドを起動して標準出力と終了コードを返す（標準エラーは捨てる）。起動できない・時間切れは error。
type Runner func(ctx context.Context, c Command) (stdout []byte, code int, err error)

type envKey struct{}

// WithEnv は ctx に hook の環境を載せる。
func WithEnv(ctx context.Context, e *Env) context.Context { return context.WithValue(ctx, envKey{}, e) }

// envFrom は ctx の環境（無ければ実際の環境）。
func envFrom(ctx context.Context) *Env {
	if e, ok := ctx.Value(envKey{}).(*Env); ok && e != nil {
		return e.withDefaults()
	}
	return (&Env{}).withDefaults()
}

func (e *Env) withDefaults() *Env {
	c := *e
	if c.Getenv == nil {
		c.Getenv = os.Getenv
	}
	if c.Environ == nil {
		c.Environ = os.Environ
	}
	if c.Getwd == nil {
		c.Getwd = func() string { wd, _ := os.Getwd(); return wd }
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Run == nil {
		c.Run = execRun
	}
	if c.Procs == nil {
		c.Procs = proc.List
	}
	if c.SelfPID == 0 {
		c.SelfPID = os.Getpid()
	}
	return &c
}

func (e *Env) env(k string) string { return e.Getenv(k) }

// lang は利用者に見せる文面の言語（LOOPTRACK_LANG・LC_ALL・LANG の順に見る）。
// hook は 1 回ごとに独立して動くので、そのつど環境から決める（グローバルに持たない）。
func (e *Env) lang() i18n.Lang { return i18n.FromEnv(e.Getenv) }
