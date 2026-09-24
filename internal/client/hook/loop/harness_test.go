package loop

// hook の表駆動テストの仕掛け（元は kit/loop/verify のケース）。
//
// 1 つのケース（scenario）は、一時ディレクトリ（sandbox）の準備と、順に行う手順（step）の並び。手順は「ファイルを作る・時刻を変える」
// などの操作か、hook の呼び出し（call）。呼び出しの結果は Go 版の期待値（want）で確かめる。
// 以前は同じケースを bash 版（kit/loop/hooks/*.sh・scripts/gates.sh）にも与えて突き合わせていたが、bash 版は撤去した。
//
// 子プロセス（判定コマンド・スタブの CLI）には LOOPTRACK_* を渡さない（本番のサーバに書き込ませない）。環境は PATH・HOME・LANG と
// ケースが指定したものだけ。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/hookio"
)

// TestMain はスタブの CLI のビルドの置き場を後始末する。
func TestMain(m *testing.M) {
	code := m.Run()
	if stubCLIBuild.dir != "" {
		os.RemoveAll(stubCLIBuild.dir)
	}
	os.Exit(code)
}

// sandbox は 1 つのケースの一時ディレクトリ（実パス）。
type sandbox struct {
	t    *testing.T
	root string
}

func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &sandbox{t: t, root: d}
}

// newASCIISandbox は ASCII だけの名前の一時ディレクトリの sandbox（scenario.asciiRoot）。
func newASCIISandbox(t *testing.T) *sandbox {
	t.Helper()
	tmp, err := os.MkdirTemp("", "loop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })
	d, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatal(err)
	}
	return &sandbox{t: t, root: d}
}

// p は sandbox の中のパス。
func (s *sandbox) p(parts ...string) string {
	return filepath.Join(append([]string{s.root}, parts...)...)
}

func (s *sandbox) mkdir(parts ...string) string {
	s.t.Helper()
	d := s.p(parts...)
	if err := os.MkdirAll(d, 0o755); err != nil {
		s.t.Fatal(err)
	}
	return d
}

func (s *sandbox) write(path, text string) {
	s.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		s.t.Fatal(err)
	}
}

func (s *sandbox) read(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func (s *sandbox) exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// touch は touch -t と同じく更新時刻を変える。
func (s *sandbox) touch(path string, t time.Time) {
	s.t.Helper()
	if err := os.Chtimes(path, t, t); err != nil {
		s.t.Fatal(err)
	}
}

var (
	oldTime = time.Date(2020, 1, 1, 0, 0, 0, 0, time.Local)
	newTime = time.Date(2030, 1, 1, 0, 0, 0, 0, time.Local)
)

// git はテストの準備で git を動かす（LOOPTRACK_* を渡さない）。
func (s *sandbox) git(args ...string) string {
	s.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Env = baseEnviron(map[string]string{"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com",
		"GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com", "GIT_CONFIG_NOSYSTEM": "1", "HOME": s.root,
		// 日時を固定して、同じ手順なら同じコミット（同じ sha）にする
		"GIT_AUTHOR_DATE": "2026-01-01T00:00:00+0000", "GIT_COMMITTER_DATE": "2026-01-01T00:00:00+0000"})
	out, err := cmd.CombinedOutput()
	if err != nil {
		s.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git がありません")
	}
}

// call は hook の呼び出し（か gates の実行）。
type call struct {
	hook    string // registry の名前（gates なら "gates"）
	agent   hookio.Agent
	env     map[string]string
	args    []string
	cwd     string // 空なら sandbox のルート
	input   string // stdin
	bare    bool   // CLAUDECODE を付けない（Claude Code 以外が起動した状況）
	trust   bool   // 実物で確かめていない差し戻しも行う（hookio.RenderOptions.TrustUnconfirmed）
	timeout time.Duration
}

// got は呼び出しの結果。
type got struct {
	out  hookio.Output
	res  hookio.Result // ParseOutput で読んだもの
	json map[string]any
	code int // gates の終了コード
	// elapsed は hook の本体にかかった時間、limit はその呼び出しに効いた打ち切り（0 なら打ち切り無し）。
	// 失敗の理由を見分けるためだけに持つ（why を見よ）。
	elapsed time.Duration
	limit   time.Duration
}

// why は失敗のときに添える一言。hook が打ち切りに達していたらその事実を返す（そうでなければ ""）。
//
// hook は打ち切りに達すると**何も出さずに**終える（hookio.Run の時間切れと mainWith の loop.err.timeout。どちらも fail-open）。
// そのため検査の側からは「期待した語が無い」という内容の失敗にしか見えず、機械の負荷で子プロセスの起動が
// 間に合わなかったのか本当の不具合なのかを見分けられない（実際にこれで詰まったことがある）。
func (g got) why() string {
	if g.limit <= 0 || g.elapsed < g.limit {
		return ""
	}
	return fmt.Sprintf("\n（hook が打ち切り %s に達したので何も出していない: 経過 %s。"+
		"子プロセス（スタブの CLI・git・make）の起動が機械の負荷で間に合わなかった可能性が高い。"+
		"打ち切りに達した hook は無言で終えるので、この失敗は「期待した語が無い」の形になる）",
		g.limit, g.elapsed.Round(time.Millisecond))
}

func (g got) quiet() bool { return g.out.Stdout == "" && g.out.Stderr == "" }

// ctx は additionalContext（無ければ ""）。
func (g got) ctx() string { return g.res.Context }

// hookEventName は hookSpecificOutput.hookEventName。
func (g got) hookEventName() string {
	h, _ := g.json["hookSpecificOutput"].(map[string]any)
	s, _ := h["hookEventName"].(string)
	return s
}

// baseEnviron は子プロセスに渡す環境（PATH・LANG と extra だけ。LOOPTRACK_* や CLAUDE_* は渡さない）。
func baseEnviron(extra map[string]string) []string {
	m := map[string]string{"PATH": os.Getenv("PATH"), "HOME": os.Getenv("HOME")}
	for _, k := range []string{"LANG", "LC_ALL", "TMPDIR", "SYSTEMROOT", "TEMP", "TMP"} {
		if v := os.Getenv(k); v != "" {
			m[k] = v
		}
	}
	for k, v := range extra {
		m[k] = v
	}
	var out []string
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

func (c call) envMap() map[string]string {
	m := map[string]string{}
	agent := c.agent
	if agent == "" {
		agent = hookio.ClaudeCode
	}
	if agent == hookio.ClaudeCode && !c.bare {
		m["CLAUDECODE"] = "1" // Claude Code は hook に CLAUDECODE を渡す（Claude Code 以外が起動した判定に使う）
	}
	for k, v := range c.env {
		m[k] = v
	}
	return m
}

func (c call) agentOrDefault() hookio.Agent {
	if c.agent == "" {
		return hookio.ClaudeCode
	}
	return c.agent
}

// runGo は Go 版を動かす。
func (s *sandbox) runGo(c call) got {
	s.t.Helper()
	m := c.envMap()
	full := baseEnviron(m)
	// 検査は日本語の利用者として動かす（文面の既定は英語。個々の検査で上書きできるよう、先に入れて後から潰す）
	envGet := map[string]string{"LOOPTRACK_LANG": "ja"}
	for _, kv := range full {
		k, v, _ := strings.Cut(kv, "=")
		envGet[k] = v
	}
	cwd := c.cwd
	if cwd == "" {
		cwd = s.root
	}
	e := &Env{
		Getenv:  func(k string) string { return envGet[k] },
		Environ: func() []string { return full },
		Getwd:   func() string { return cwd },
		Run: func(ctx context.Context, cm Command) ([]byte, int, error) {
			if cm.Dir == "" {
				cm.Dir = cwd
			}
			return execRun(ctx, cm)
		},
	}
	var stdout, stderr bytes.Buffer
	if c.hook == "gates" {
		code := Gates(context.Background(), c.args, &stdout, e)
		return got{out: hookio.Output{Stdout: stdout.String(), ExitCode: code}, code: code}
	}
	args := append([]string{"--agent", string(c.agentOrDefault())}, c.args...)
	start := time.Now()
	code := mainWith(context.Background(), c.hook, args, strings.NewReader(c.input), &stdout, &stderr, e, func(o *hookio.RunOptions) {
		o.Render.TrustUnconfirmed = c.trust
		if c.timeout > 0 {
			o.Timeout = c.timeout
		}
	})
	g := s.result(c, hookio.Output{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: code})
	// この 2 行は必ず同じ経路に隣り合わせて置く。TestHarnessTimeoutDiagnosis は経過を打ち切りと突き合わせるだけなので、
	// 間に分岐や early return が入って経過だけ代入されなくなっても、検査は緑のまま通る。
	g.elapsed = time.Since(start)
	g.limit = callLimit(c)
	return g
}

// callLimit はその呼び出しに実際に効く打ち切り（entry.Timeout と call.timeout の短いほう）。
// mainWith は本体の ctx に entry.Timeout を必ず入れるので、call.timeout を長くしても entry.Timeout は越えられない。
func callLimit(c call) time.Duration {
	lim := time.Duration(0)
	if entry, ok := Lookup(c.hook); ok {
		lim = entry.Timeout
	}
	if c.timeout > 0 && (lim == 0 || c.timeout < lim) {
		lim = c.timeout
	}
	return lim
}

func (s *sandbox) result(c call, o hookio.Output) got {
	entry, _ := Lookup(c.hook)
	g := got{out: o, res: hookio.ParseOutput(c.agentOrDefault(), entry.Event, o), code: o.ExitCode}
	if strings.TrimSpace(o.Stdout) != "" {
		_ = json.Unmarshal([]byte(o.Stdout), &g.json)
	}
	return g
}

// step は手順。do（操作）か mk（呼び出し）のどちらか・両方。
type step struct {
	name string
	do   func(s *sandbox)
	mk   func(s *sandbox) call
	want func(t *testing.T, s *sandbox, g got)
}

// scenario は 1 つのケースの並び。
type scenario struct {
	name  string
	setup func(s *sandbox)
	steps []step
	// asciiRoot は一時ディレクトリを ASCII だけの名前で作る（t.TempDir はテスト名の日本語を含む）。Windows の make（mingw の
	// GNU Make）は ANSI コードページの外の文字を含むディレクトリで readdir に失敗して何もできない（CI で確認。
	// 利用者の環境の制約で、gates の不具合ではない）ため、make を起動するケースで使う。
	asciiRoot bool
}

// run はケースを動かす。
func (sc scenario) run(t *testing.T) {
	t.Helper()
	t.Run(sc.name, func(t *testing.T) {
		mk := newSandbox
		if sc.asciiRoot {
			mk = newASCIISandbox
		}
		a := mk(t)
		if sc.setup != nil {
			sc.setup(a)
		}
		for _, st := range sc.steps {
			label := st.name
			if label == "" {
				label = "step"
			}
			label = strings.ReplaceAll(label, " ", "_")
			if st.do != nil {
				st.do(a)
			}
			if st.mk == nil {
				continue
			}
			ga := a.runGo(st.mk(a))
			if st.want != nil {
				t.Run(label, func(t *testing.T) { st.want(t, a, ga) })
			}
		}
	})
}

// kitLoop は kit/loop のディレクトリ。
func kitLoop(t *testing.T) string {
	t.Helper()
	d, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "kit", "loop"))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// jsonInput は hook の入力の JSON（HTML のエスケープをしない）。
func jsonInput(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return buf.String()
}

// jstr は JSON の文字列リテラル（引用符つき）。手で組み立てる入力にパスを埋めるときに使う
// （Windows のパスの \ をそのまま埋めると不正な JSON になり、hook が入力を読めない）。
func jstr(s string) string {
	return strings.TrimSuffix(jsonInput(s), "\n")
}

// 結果の確かめ方（よく使うもの）。

func wantQuiet(t *testing.T, _ *sandbox, g got) {
	t.Helper()
	if !g.quiet() {
		t.Errorf("何も出さないはず: %q%s", g.out.Stdout, g.why())
	}
}

func wantBlock(t *testing.T, _ *sandbox, g got) {
	t.Helper()
	if g.json["decision"] != "block" || g.res.Block == "" {
		t.Errorf("decision: block のはず: %q%s", g.out.Stdout, g.why())
	}
}

func wantDeny(t *testing.T, _ *sandbox, g got) {
	t.Helper()
	if g.res.Deny == "" {
		t.Errorf("permissionDecision: deny のはず: %q%s", g.out.Stdout, g.why())
	}
}

func wantAsk(t *testing.T, _ *sandbox, g got) {
	t.Helper()
	if g.res.Ask == "" || g.hookEventName() != "PreToolUse" {
		t.Errorf("permissionDecision: ask のはず: %q%s", g.out.Stdout, g.why())
	}
}

// wantContext は additionalContext があり、hookEventName が ev で、want の語をすべて含み、deny の語を含まないこと。
func wantContext(ev string, want []string, deny []string) func(*testing.T, *sandbox, got) {
	return func(t *testing.T, s *sandbox, g got) {
		t.Helper()
		if g.hookEventName() != ev || g.ctx() == "" {
			t.Fatalf("hookEventName %q・additionalContext のはず: %q%s", ev, g.out.Stdout, g.why())
		}
		for _, w := range want {
			if !strings.Contains(g.ctx(), w) {
				t.Errorf("「%s」が無い:\n%s%s", w, g.ctx(), g.why())
			}
		}
		for _, w := range deny {
			if strings.Contains(g.ctx(), w) {
				t.Errorf("「%s」が出た:\n%s%s", w, g.ctx(), g.why())
			}
		}
	}
}

// wantSystemMessage は decision を出さず systemMessage に want を含むこと。
func wantSystemMessage(want string) func(*testing.T, *sandbox, got) {
	return func(t *testing.T, _ *sandbox, g got) {
		t.Helper()
		if _, ok := g.json["decision"]; ok {
			t.Errorf("decision を出さないはず: %q", g.out.Stdout)
		}
		if msg, _ := g.json["systemMessage"].(string); !strings.Contains(msg, want) {
			t.Errorf("systemMessage に「%s」が無い: %q%s", want, g.out.Stdout, g.why())
		}
	}
}

func all(fs ...func(*testing.T, *sandbox, got)) func(*testing.T, *sandbox, got) {
	return func(t *testing.T, s *sandbox, g got) {
		t.Helper()
		for _, f := range fs {
			f(t, s, g)
		}
	}
}

func removeFile(p string) error { return os.RemoveAll(p) }

// stubRule はスタブの CLI の 1 つの応答（testdata/stubcli の rule と同じ形）。
type stubRule struct {
	Args string `json:"args"` // 引数を空白でつないだもの（空ならどの引数にも一致）
	Env  string `json:"env"`  // "名前=値"（空ならどの環境にも一致）
	Out  string `json:"out"`  // 標準出力に出す 1 行
	Err  string `json:"err"`  // 標準エラーに出す 1 行
	Exit int    `json:"exit"` // 終了コード
}

// stubCLI はイシューの CLI のスタブを置き、そのパス（LOOPTRACK_LOOP_ISSUE_CLI に渡すもの）を返す。
// 応答は rules（最初に一致したもの。どれにも一致しなければ終了コード 2）。
//
// 中身は testdata/stubcli をビルドした実行ファイル（+ 規則の JSON）。以前は OS ごとに sh / cmd のスクリプトを置いていたが、
// Windows では cmd がスクリプトを書いたとおりに解釈せず、hook が CLI の出力を読めないまま通り過ぎていた。
func stubCLI(s *sandbox, name string, rules ...stubRule) string {
	s.t.Helper()
	p := s.p(name)
	if runtime.GOOS == "windows" {
		p += ".exe" // 拡張子が無いと Windows では起動できない
	}
	b, err := os.ReadFile(stubCLIExe(s.t))
	if err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o755); err != nil {
		s.t.Fatal(err)
	}
	j, err := json.Marshal(rules)
	if err != nil {
		s.t.Fatal(err)
	}
	s.write(p+".rules.json", string(j))
	// 写した実行ファイルを 1 度だけ空打ちして温める。
	// macOS は「新しく書いた実行ファイルの初回の起動」で署名の検査（syspolicy / XProtect）を行う。実測（2026-09-21）で
	// 初回 150ms・2 回目以降 3ms、同じ実行ファイルの複製 40 本を同時に起動すると中央値 2.5 秒・最大 4.7 秒まで伸びた。
	// go test ./... は新しい実行ファイル（各パッケージの検査の実体）を大量に作って同時に起動するので、この待ちが伸びる。
	// hook の打ち切りは 4〜9 秒（Lookup の entry.Timeout）で、達すると hook は何も出さずに終える。温めずに hook の中で
	// 初回の起動を行うと、この待ちが打ち切りを食い潰し、検査が「期待した語が無い」という内容の失敗に化ける
	// （実際に起きた）。温めるのは検査の準備の中＝打ち切りの外なので、遅くても判定には影響しない。
	warm, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_ = exec.CommandContext(warm, p).Run() // 規則に一致しなければ終了コード 2。スタブに副作用は無い
	return p
}

// stubCLIBuild は testdata/stubcli のビルド（1 回だけ。置き場は TestMain の後で消す）。
var stubCLIBuild struct {
	once sync.Once
	dir  string
	path string
	out  string
	err  error
}

// stubCLIExe はスタブの CLI の実行ファイル（stubCLI がここから写す）。go が無ければ省略する。
func stubCLIExe(t *testing.T) string {
	t.Helper()
	b := &stubCLIBuild
	b.once.Do(func() {
		gobin, err := exec.LookPath("go")
		if err != nil {
			b.err = err
			return
		}
		if b.dir, b.err = os.MkdirTemp("", "loop-stubcli-"); b.err != nil {
			return
		}
		b.path = filepath.Join(b.dir, "stubcli")
		if runtime.GOOS == "windows" {
			b.path += ".exe"
		}
		out, err := exec.Command(gobin, "build", "-o", b.path, "./testdata/stubcli").CombinedOutput()
		b.out, b.err = string(out), err
	})
	if b.path == "" {
		t.Skipf("スタブの CLI を作れないため省略: %v", b.err)
	}
	if b.err != nil {
		t.Fatalf("スタブの CLI を作れません: %v\n%s", b.err, b.out)
	}
	return b.path
}
