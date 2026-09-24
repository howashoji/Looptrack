// Package core は core の hook（鮮度ガード・トークンの送信と spool・SessionStart の summary）の Go 版（
// DESIGN.md §5-11）。元は以前の hook（1.0.0 より前）の鮮度ガード・トークンの送信と、以前の CLI の summary --agent / --hook-json。
//
// 各 hook は hookio.Event を読んで hookio.Result を返す（AI ごとの入出力の違いは internal/hookio が吸収する）。
// 名前 → 本体の登録表（Lookup・Names）を公開し、`looptrack hook <名前>` の配線（cmd/looptrack）は Main を呼ぶだけにする。
// 以前の実装（1.0.0 より前）は撤去した。
//
// 状態ファイルの置き場と形式:
//
//   - 鮮度ガード: <ルート>/.claude/.looptrack-freshness/（project.json と sessions/<session_id>/{session,engaged,work}）。
//     ルートは CLAUDE_PROJECT_DIR、無ければ作業ディレクトリ（以前の hook の ROOT と同じ。git のルートは探さない）。
//   - トークンの送信: looptrack の置き場（資格情報と同じ。internal/client/cred。Windows は %APPDATA%\looptrack、他は
//     $XDG_CONFIG_HOME（無ければ ~/.config）/looptrack）の {usage-spool,usage-last}/。以前の実装と共有した
//     置き場は読まない・移さない。作業名を送るかの記録（usage-send-prompts）は internal/client/usagesnap。
//
// 挙動は以前の実装（1.0.0 より前）に合わせて移した（撤去までは同じ入力・同じ状態ファイル・同じ偽 API から、出力・終了コード・
// 状態ファイル・送った要求が一致することを差分テストで確かめていた）。以前の実装と意図して変えたところ:
//
//   - fail-open を徹底する。読めない入力・panic・時間切れ・トークンが無い（以前は異常終了して exit 1）・プロジェクトが分からない
//     ときは、何も出さず何も記録せずに exit 0（hookio.Run）。以前は読めない入力を空とみなして鮮度ガードの器を作った。
//   - 出力は hookio.Render の形（stdout の JSON・exit 0）。鮮度ガードの差し戻しは以前の exit 2 + stderr と同じ意味の
//     decision: block。SessionStart の summary は Claude Code・Codex でも additionalContext の JSON にする（以前は素の文字列）。
//     --no-block（LOOPTRACK_LOOP_NO_BLOCK=1）なら差し戻さずに systemMessage で知らせる（loop の hook と同じ扱い）。
//   - どの AI かは配線の --agent で決める（usage の --client も受ける）。鮮度ガードは Claude Code 以外のツール名
//     （Copilot の edit・create・bash など）も hookio の種類で見分ける。
//   - ファイルモード（.claude/issues の Markdown）は持たない（§5-11）。鮮度ガードは API モードだけで動く。
//   - 鮮度ガードは `looptrack issue …`・`looptrack issue-freshness ack|reset` も以前の CLI・hook の呼び方と同じに数える。
//   - usage の送信の切り離しは、自分（looptrack）を子プロセスとして起動する（POSIX は setsid、Windows は DETACHED_PROCESS）。
//     以前は fork した。子への引き継ぎは標準入力の JSON（usageJob）。
//   - 利用者が渡す正規表現（LOOPTRACK_MCP_SERVER）は RE2 で解釈する。解釈できなければ一致しない（以前は正規表現の誤りで落ちた）。
package core

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/hook/loop"
	"github.com/howashoji/looptrack/internal/hookio"
	"github.com/howashoji/looptrack/internal/i18n"
)

// Hook は 1 本の hook の本体。error を返すと何も出さずに exit 0（fail-open・hookio.Run）。
type Hook func(ctx context.Context, c *Call, ev hookio.Event) (hookio.Result, error)

// Entry は登録表の 1 行。
type Entry struct {
	// Name は `looptrack hook <名前>` の名前。loop の hook（internal/client/hook/loop）の名前と重ならない。
	Name string
	// Event は入力にイベント名が無いときに使うイベント（空なら入力か --event に従う。mark・usage は複数のイベントに配線する）。
	Event hookio.Name
	// Timeout は本体の打ち切り（配線の timeout より短くする。超えたら何も出さずに exit 0）。
	Timeout time.Duration
	Hook    Hook
}

// 登録表。名前は以前の配線と 1 対 1（T11 の init が置き換える）。
var registry = map[string]Entry{}

func init() {
	for _, e := range []Entry{
		{"issue-freshness-mark", "", 4 * time.Second, FreshnessMark},
		{"issue-freshness-check", hookio.Stop, 9 * time.Second, FreshnessCheck},
		{"usage", "", 9 * time.Second, Usage},
		{"summary", hookio.SessionStart, 9 * time.Second, Summary},
	} {
		registry[e.Name] = e
	}
	// Copilot 向けの合成の SessionStart（looptrack hook session-start --parts summary,…）の部分として summary を登録する
	summary := registry["summary"]
	loop.RegisterPart("summary", summary.Timeout, func(ctx context.Context, getenv hookio.Getenv, getwd func() string, now func() time.Time,
		args []string, ev hookio.Event) (hookio.Result, error) {
		vars := env.FromFunc(getenv)
		return Summary(ctx, &Call{Env: &Env{Vars: &vars, Getwd: getwd, Now: now}, Args: args}, ev)
	})
}

// Lookup は名前の hook を返す。
func Lookup(name string) (Entry, bool) {
	e, ok := registry[name]
	return e, ok
}

// Names は登録されている hook の名前（名前順）。
func Names() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Env は hook が使う環境（テストで差し替える）。零値の項目は実際の環境になる。
type Env struct {
	// Vars は環境変数（nil なら env.OS()）。
	Vars *env.Env
	// Getwd は作業ディレクトリ（CLAUDE_PROJECT_DIR が無いときのルート・相対パスの基準）。
	Getwd func() string
	// Now は今の時刻。
	Now func() time.Time
	// Spawn は usage の送信を切り離した子プロセスで行う（job は子の標準入力に渡す JSON）。nil なら自分を起動する（detach.go）。
	Spawn func(job []byte, dir string) error
	// Sleep は Copilot の OTel の書き出し待ち（テストで縮める）。
	Sleep func(time.Duration)
}

// lang は利用者に見せる文面の言語（LOOPTRACK_LANG・LC_ALL・LC_MESSAGES・LANG の順に見る）。
// hook は 1 回ごとに独立して動くので、そのつど環境から決める（グローバルに持たない）。
// withDefaults の後に呼ぶ（Vars が埋まっている）。
func (e *Env) lang() i18n.Lang { return i18n.FromEnv(e.Vars.Get) }

func (e *Env) withDefaults() *Env {
	c := Env{}
	if e != nil {
		c = *e
	}
	if c.Vars == nil {
		o := env.OS()
		c.Vars = &o
	}
	if c.Getwd == nil {
		c.Getwd = func() string { wd, _ := os.Getwd(); return wd }
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Spawn == nil {
		c.Spawn = spawnSelf
	}
	if c.Sleep == nil {
		c.Sleep = time.Sleep
	}
	return &c
}

// Call は 1 回の呼び出しの文脈（環境・hook ごとの引数・デバッグの出力先）。
type Call struct {
	*Env
	// Args は hook ごとの引数（hookio.ParseArgs が読まなかった残り。usage の --client・summary の --limit）。
	Args []string
	// Stderr はデバッグの出力先（LOOPTRACK_USAGE_DEBUG など。nil なら捨てる）。
	Stderr io.Writer
}

func (c *Call) getenv(k string) string { return c.Vars.Get(k) }

// root はプロジェクトのルート（CLAUDE_PROJECT_DIR → 作業ディレクトリ。以前の hook・CLI の ROOT）。
func (c *Call) root() string {
	if d := c.getenv("CLAUDE_PROJECT_DIR"); d != "" {
		return d
	}
	return c.Getwd()
}

// arg は hook ごとの引数 name の値（--name v か --name=v。無ければ ""・false）。
func (c *Call) arg(name string) (string, bool) {
	for i, a := range c.Args {
		if a == name && i+1 < len(c.Args) {
			return c.Args[i+1], true
		}
		if v, ok := strings.CutPrefix(a, name+"="); ok {
			return v, true
		}
	}
	return "", false
}

func (c *Call) flag(name string) bool {
	for _, a := range c.Args {
		if a == name {
			return true
		}
	}
	return false
}

// Main は `looptrack hook <名前> [--agent …] [--event …] [--no-block] [hook ごとの引数…]` の本体。戻り値は終了コード（常に 0 か
// hookio.Render の終了コード）。知らない名前・読めない引数・本体のエラー・panic・時間切れは何も出さずに 0（fail-open）。
// env が nil なら実際の環境を使う。
func Main(ctx context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer, env *Env) int {
	return mainWith(ctx, name, args, stdin, stdout, stderr, env, nil)
}

// mainWith は Main に、読んだ引数の後で RunOptions を直す口を足したもの（テストが打ち切りを縮める）。
func mainWith(ctx context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer, env *Env, adjust func(*hookio.RunOptions)) (code int) {
	defer hookio.Recover(&code, nil)
	e := env.withDefaults()
	if name == "usage" && hasArg(args, usageJobFlag) {
		// 切り離した子プロセス（usage の送信の本体）。親が標準入力に usageJob を渡す
		runUsageJob(ctx, &Call{Env: e, Args: args, Stderr: stderr}, stdin)
		return 0
	}
	opts, rest, err := hookio.ParseArgs(args, e.Vars.Get)
	if err != nil {
		debugf(opts.Debug, stderr, "%v", err)
		return 0
	}
	entry, ok := Lookup(name)
	if !ok {
		debugf(opts.Debug, stderr, "%s", i18n.T(e.lang(), "core.err.unknown_hook", "name", name))
		return 0
	}
	opts.Parse.Getwd = e.Getwd
	if opts.Parse.Event == "" && entry.Event != "" {
		opts.Parse.Event = string(entry.Event)
	}
	opts.Timeout = entry.Timeout
	if adjust != nil {
		adjust(&opts)
	}
	c := &Call{Env: e, Args: rest, Stderr: stderr}
	return hookio.Run(stdin, stdout, stderr, opts, func(ev hookio.Event) (hookio.Result, error) {
		cx, cancel := context.WithTimeout(ctx, opts.Timeout)
		if opts.Timeout <= 0 {
			cx, cancel = context.WithCancel(ctx)
		}
		defer cancel()
		return entry.Hook(cx, c, ev)
	})
}

func hasArg(args []string, a string) bool {
	for _, x := range args {
		if x == a {
			return true
		}
	}
	return false
}

func debugf(on bool, w io.Writer, format string, a ...any) {
	if on && w != nil {
		io.WriteString(w, strings.TrimRight(fmt.Sprintf(format, a...), "\n")+"\n")
	}
}
