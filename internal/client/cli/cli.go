// Package cli は `looptrack issue …`（以前の CLI（1.0.0 より前）と互換）の骨組み（DESIGN.md §5-11）。
//
// 互換の範囲: サブコマンド・引数（argparse.go）・--json・標準出力と標準エラーの文面・終了コード（誤り 1・引数の誤り 2）。
// 確かめ方は internal/clitest の golden（LOOPTRACK_BIN=<looptrack> go test ./internal/clitest）。
// ファイルモード（.claude/issues/ の Markdown）は持たない。
//
// 各サブコマンドは Command.Run を持ち、失敗は error で返す。Main が「エラー: …」にして終了コード 1 にする。
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/setupwiz"
)

// DefaultURL は案内に出すサーバの URL（init で --url も LOOPTRACK_API_URL も無いときの既定・login の案内）。
// 公開版の既定は手元のローカルモードのアドレス（looptrack setup のローカル利用の既定のポートと接頭辞）。
// 運用中のサーバは設定（LOOPTRACK_API_URL・--url）で指す。
var DefaultURL = fmt.Sprintf("http://127.0.0.1:%d%s", setupwiz.DefaultPort, setupwiz.DefaultBasePath)

// IO は標準入出力。
type IO struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Ctx は 1 回の実行の文脈。
type Ctx struct {
	IO
	Env  env.Env
	Root string    // プロジェクトのディレクトリ（CLAUDE_PROJECT_DIR か作業ディレクトリ）
	Args []string  // issue の後ろの引数（案内の文面に使う）
	Lang i18n.Lang // 画面に出す文面の言語（環境から決める）

	client *api.Client
}

// Fail は「エラー: msg」で終了コード 1 にする誤り。
type Fail struct{ Msg string }

func (e *Fail) Error() string { return e.Msg }

// Failf は Fail を作る。
func Failf(format string, a ...any) error { return &Fail{fmt.Sprintf(format, a...)} }

// Main は `looptrack issue` の入口。args は issue の後ろの引数。終了コードを返す。
func Main(args []string, stdio IO, e env.Env) int {
	lang := i18n.FromEnv(func(k string) string { return e.Get(k) })
	root := e.Get("CLAUDE_PROJECT_DIR")
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(stdio.Stderr, i18n.T(lang, "cli.err.prefix", "message", i18n.T(lang, "cli.err.cwd", "reason", err)))
			return 1
		}
		root = wd
	}
	fixLocalZone(e)
	api.SetRoot(root)
	ctx := &Ctx{IO: stdio, Env: e, Root: root, Args: args, Lang: lang}
	cmd := IssueCommand(lang)
	vals, leaf, err := Parse(cmd, "looptrack issue", args)
	if err != nil {
		var ue *UsageError
		var hr *helpRequested
		switch {
		case errors.As(err, &hr):
			io.WriteString(stdio.Stdout, hr.text)
			return 0
		case errors.As(err, &ue):
			printUsageError(stdio.Stderr, ue)
			return 2
		}
		fmt.Fprintln(stdio.Stderr, i18n.T(lang, "cli.err.prefix", "message", err))
		return 2
	}
	if leaf.Run == nil {
		name := strings.Join(vals.Path, " ")
		fmt.Fprintln(stdio.Stderr, i18n.T(lang, "cli.err.prefix", "message",
			i18n.T(lang, "cli.err.not_implemented", "name", name, "planned", leaf.Planned, "entry", api.EntryCommand())))
		return 1
	}
	if err := leaf.Run(ctx, vals); err != nil {
		if ex := (*Exit)(nil); errors.As(err, &ex) {
			return ex.Code
		}
		// 要求の**後**に出る文面なので、サーバの宣言を取り込んだ ctx.Lang を使う（局所の lang は要求の前の値）
		fmt.Fprintln(stdio.Stderr, i18n.T(ctx.Lang, "cli.err.prefix", "message", Message(ctx.Lang, err)))
		return 1
	}
	return 0
}

// Message は誤りを利用者に出す文にする（以前の CLI と同じ添え書き）。
func Message(lang i18n.Lang, err error) string {
	var ae *api.Error
	if errors.As(err, &ae) {
		switch {
		case ae.Status == 401:
			return i18n.T(lang, "cli.err.unauthorized", "message", ae.Text(lang), "relogin", api.Relogin(lang))
		case ae.Status >= 500:
			return i18n.T(lang, "cli.err.server", "status", ae.Status, "message", ae.Text(lang))
		}
		return ae.Text(lang)
	}
	// i18n.Error は ID を持つので、ここで利用者の言語の文面にする
	return i18n.Text(lang, err)
}

// useServerLang は、サーバが応答で宣言した言語（Content-Language）を、この実行の表示の言語にする配線。
// api.Client を作るところすべてで呼ぶ（cli・login・summary・usage attach）。
//
// **利用者が LOOPTRACK_LANG で明示していれば環境が勝つ**ので、そのときは配線しない。「明示したか」の判定は
// 新しく作らず、サーバへ X-Looptrack-Lang を送るかを決めている判定（internal/client/api）と同じ形にする
// （読めない値は「明示なし」。i18n.FromEnv が端末の設定へ落ちるのと同じ扱い）。
//
// **配線する Ctx は、実際に文面を描く Ctx**（summary の hook の JSON のように Ctx を値で複写する経路があるので、
// クライアントを作った側の Ctx に束ねる）。
func (c *Ctx) useServerLang(cl *api.Client) {
	if _, ok := i18n.Parse(c.Env.Get("LOOPTRACK_LANG")); ok {
		return
	}
	cl.OnLang = func(lang i18n.Lang) { c.Lang = lang }
}

// Client は API の Client（1 回だけ作る）。
func (c *Ctx) Client() (*api.Client, error) {
	if c.client == nil {
		cl, err := api.New(c.Env)
		if err != nil {
			return nil, err
		}
		c.useServerLang(cl)
		c.client = cl
	}
	return c.client, nil
}

// RequireAPI はサーバの URL があることを確かめて Client を返す。無ければ設定の案内で止める。
// only が空でなければ「<サブコマンド> は API モードだけで使えます」の文面で止める。
func (c *Ctx) RequireAPI(only string) (*api.Client, error) {
	cl, err := c.Client()
	if err != nil {
		return nil, err
	}
	if cl.BaseURL != "" {
		return cl, nil
	}
	if only != "" {
		return nil, i18n.Errorf("cli.err.api_only", "name", only, "url_env", env.Name(env.APIURL), "project_env", env.Name(env.Project))
	}
	return nil, c.noAPI()
}

// noAPI はサーバの URL が無いときの案内（以前の CLI の文面から、ファイルモードが無い分だけ 1 行目を変えた）。
// .claude/settings.json の env にあれば、Claude Code の外のシェルで実行していると分かるので、その値を付けた実行例を出す。
func (c *Ctx) noAPI() error {
	quoted := make([]string, len(c.Args))
	for i, a := range c.Args {
		quoted[i] = shellQuote(a)
	}
	cmd := strings.TrimRight(api.EntryCommand()+" "+strings.Join(quoted, " "), " \t\n")
	urlVar, projVar := env.Name(env.APIURL), env.Name(env.Project)
	var found *jsonorder.Object
	for _, name := range []string{"settings.local.json", "settings.json"} {
		b, err := os.ReadFile(filepath.Join(c.Root, ".claude", name))
		if err != nil {
			continue
		}
		o, err := jsonorder.DecodeObject(b)
		if err != nil {
			continue
		}
		e := o.Object("env")
		if e == nil || found != nil {
			continue
		}
		if v, _ := e.Get(env.Name(env.APIURL)); jsonorder.Truthy(v) {
			found = e
		}
	}
	lines := []string{i18n.T(c.Lang, "cli.no_api.missing_url", "env", urlVar, "root", c.Root)}
	if found != nil {
		url, proj := "", ""
		if v, _ := found.Get(env.Name(env.APIURL)); jsonorder.Truthy(v) {
			url = strings.TrimRight(strings.TrimSpace(jsonorder.Str(v)), "/")
		}
		if v, _ := found.Get(env.Name(env.Project)); jsonorder.Truthy(v) {
			proj = jsonorder.Str(v)
		}
		if proj == "" {
			proj = i18n.T(c.Lang, "cli.no_api.project_placeholder")
		}
		lines = append(lines,
			i18n.T(c.Lang, "cli.no_api.shell_env", "url_env", urlVar, "project_env", projVar),
			i18n.T(c.Lang, "cli.no_api.run_like_this"),
			fmt.Sprintf("  %s=%s %s=%s %s", urlVar, url, projVar, shellQuote(proj), cmd))
	} else {
		lines = append(lines,
			i18n.T(c.Lang, "cli.no_api.set_env", "url_env", urlVar, "project_env", projVar),
			// URL は固定値を出さない（本番以外のサーバ・ローカルのサーバで誤りになる）
			i18n.T(c.Lang, "cli.no_api.example", "url_env", urlVar, "project_env", projVar, "cmd", cmd),
			i18n.T(c.Lang, "cli.no_api.where"))
	}
	return &Fail{strings.Join(lines, "\n")}
}

// Project はプロジェクトの slug（LOOPTRACK_PROJECT、無ければ .claude/issues の symlink 先の名前）。
func (c *Ctx) Project() (string, error) {
	if v := strings.TrimSpace(c.Env.Value(env.Project)); v != "" {
		return v, nil
	}
	issues := filepath.Join(c.Root, ".claude", "issues")
	if fi, err := os.Lstat(issues); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		if target, err := os.Readlink(issues); err == nil {
			return filepath.Base(strings.TrimRight(target, `/\`)), nil
		}
	}
	return "", i18n.Errorf("cli.err.project_unknown", "env", env.Name(env.Project))
}

// ProjectPath は /projects/<slug><suffix>。
func (c *Ctx) ProjectPath(suffix string) (string, error) {
	slug, err := c.Project()
	if err != nil {
		return "", err
	}
	return "/projects/" + api.PathEscape(slug) + suffix, nil
}

// PrintJSON は JSON を出す（非 ASCII はそのまま・2 字下げ）。
func (c *Ctx) PrintJSON(v any) {
	fmt.Fprintln(c.Stdout, jsonorder.Indent(v, 2))
}

// Println は print と同じ（末尾に改行）。
func (c *Ctx) Println(a ...any) {
	fmt.Fprintln(c.Stdout, a...)
}

// Printf は整形して出す（改行は付けない）。
func (c *Ctx) Printf(format string, a ...any) {
	fmt.Fprintf(c.Stdout, format, a...)
}

var shellUnsafe = regexp.MustCompile(`[^\w@%+=:,./-]`)

// shellQuote はシェルに渡せる形に引用符で囲む。
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			ascii = false
		}
	}
	if ascii && !shellUnsafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
