package cli

// セッション要約（DESIGN.md §9-7）のサブコマンド: `looptrack session digest`・`looptrack session pull`。
// 会話記録の搬送（§9-8）は 1 段下の `looptrack session transcript …`（transcript.go）。
//
//   - digest は本文を読み、欠けた見出しの行を足してから、issue comment と同じ経路（postComment。path_hints も同じ
//     pathhint.Collect の 1 か所）でコメントとして送る。検査（見出し・パスの正規化・秘密・上限）はサーバの service の 1 か所で、
//     ここでは行わない。拒否されたらサーバの理由（行番号と規則の語）だけを出して終了コード 1。
//   - pull はイシューのコメントのうち要約を新しい順に --limit 件取り出し、作業コピーと同じ置き場
//     （<プロジェクト>/.claude/.looptrack-work/<ID>-digest.md）に書いて、標準出力にも同じものを出す。
//     置き場の表示はプロジェクトのディレクトリからの相対（作業コピーと同じ rel）。

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/client/dashstdin"
	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/client/session"
	"github.com/howashoji/looptrack/internal/client/textenc"
	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
)

// defaultDigestPull は session pull の既定の件数。
const defaultDigestPull = 3

// SessionCommand は `looptrack session` の引数の木。
func SessionCommand(lang i18n.Lang) *Command {
	return &Command{
		Name:        "session",
		Description: i18n.T(lang, "cli.cmd.session.root.desc"),
		Subs: []*Command{
			{Name: "digest", Help: i18n.T(lang, "cli.cmd.session.digest"), Run: cmdSessionDigest, Args: []*Arg{
				{Name: "id"},
				{Name: "file", Nargs: "?", Default: dashstdin.Dash, Metavar: "FILE", Help: i18n.T(lang, "cli.arg.session.digest.file")},
			}},
			{Name: "pull", Help: i18n.T(lang, "cli.cmd.session.pull"), Run: cmdSessionPull, Args: []*Arg{
				{Name: "id"},
				{Name: "--limit", Kind: Int, Default: int64(defaultDigestPull), Help: i18n.T(lang, "cli.arg.session.pull.limit")},
			}},
			transcriptCommand(lang),
		},
	}
}

// SessionMain は `looptrack session` の入口。args は session の後ろの引数。終了コードを返す。
func SessionMain(args []string, stdio IO, e env.Env) int {
	return runGroup("looptrack session", SessionCommand, args, stdio, e)
}

// cmdSessionDigest は要約を送る。
func cmdSessionDigest(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	path, err := c.issuePath(v.Str("id"), "/comments")
	if err != nil {
		return err
	}
	text, err := c.readDigestBody(v.Str("file"))
	if err != nil {
		return err
	}
	wd, _ := os.Getwd()
	text, err = fillDigestHeader(text, c.Lang, c.digestValues(wd))
	if err != nil {
		return err
	}
	return c.postComment(cl, path, v.Str("id"), text, nil)
}

// readDigestBody は本文を読む（"-" なら標準入力、ほかはファイル）。空（空白だけ）なら送らずに誤りにする。
func (c *Ctx) readDigestBody(arg string) (string, error) {
	var raw []byte
	if arg == dashstdin.Dash {
		b, _, err := dashstdin.Resolve(arg, c.Stdin)
		if err != nil {
			return "", err
		}
		raw = b
	} else {
		b, err := os.ReadFile(arg)
		if err != nil {
			return "", i18n.Errorf("cli.err.session.read", "path", arg, "reason", err)
		}
		raw = b
	}
	text := string(textenc.Decode(raw))
	if trimSpace(text) == "" {
		return "", i18n.Errorf("cli.err.stdin_body_empty")
	}
	return text, nil
}

// digestValues は見出しに足す値を、要るときにだけ決める関数を返す（本文が見出しをそろえていれば git を起動しない）。
//   - label: LOOPTRACK_MACHINE_LABEL（前後の空白を除く）。無ければホスト名の最初の「.」より前
//   - branch: dir で `git rev-parse --abbrev-ref HEAD`（ブランチから外れていれば HEAD）
//   - head: dir で `git rev-parse HEAD`
//   - ai: 環境変数から決めた AI の種類（session.AIKind。分からなければ other）
func (c *Ctx) digestValues(dir string) func(key string) (string, error) {
	return func(key string) (string, error) {
		switch key {
		case "label":
			if l := strings.TrimSpace(c.Env.Value(env.MachineLabel)); l != "" {
				return l, nil
			}
			host, err := hostname()
			host, _, _ = strings.Cut(strings.TrimSpace(host), ".")
			if err != nil || host == "" {
				return "", i18n.Errorf("cli.err.session.label")
			}
			return host, nil
		case "branch":
			return gitValue(dir, key, "rev-parse", "--abbrev-ref", "HEAD")
		case "head":
			return gitValue(dir, key, "rev-parse", "HEAD")
		case "ai":
			return session.AIKind(c.Env), nil
		}
		return "", nil
	}
}

// hostname と gitOutput はテストで差し替える。
var (
	hostname  = os.Hostname
	gitOutput = func(dir string, args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		out, err := cmd.Output()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok && len(strings.TrimSpace(string(ee.Stderr))) > 0 {
				return "", &gitError{msg: strings.TrimSpace(string(ee.Stderr))}
			}
			return "", err
		}
		return string(out), nil
	}
)

type gitError struct{ msg string }

func (e *gitError) Error() string { return e.msg }

// gitValue は git の出力の 1 行目（前後の空白を除く）。決められなければ、見出しに手で書くよう案内する誤り。
func gitValue(dir, field string, args ...string) (string, error) {
	out, err := gitOutput(dir, args...)
	if err == nil {
		out, _, _ = strings.Cut(strings.TrimSpace(out), "\n")
		if out = strings.TrimSpace(out); out != "" {
			return out, nil
		}
	}
	reason := ""
	if err != nil {
		reason = err.Error()
	}
	return "", i18n.Errorf("cli.err.session.git", "field", field, "reason", reason)
}

// fillDigestHeader は本文に欠けている見出しの行を足す（DESIGN.md §9-7 の形）。value は足す値を決める関数
// （キーは label・branch・head・ai。要るキーだけを呼ぶ）。
//
//   - 先頭の空行・空白は除く（サーバは先頭の空白を許さないので、そのままでは要約と読まれない）。
//   - 本文が要約の印（「セッション要約:」「Session digest:」。domain.LeadWord）で始まらなければ、見出しの 4 行
//     （印と端末のラベル・branch・head・ai）と空行を本文の前に足す。印は利用者の言語のもの。
//   - 印で始まるなら、1 行目の端末のラベルが空のときだけラベルを足し、8 行目まで（domain.DigestHeaderLines）に
//     無い branch・head・ai の行を、この順で 1 行目の直後に足す。書いてある行は値の形が違っても書き換えない
//     （検査はサーバの 1 か所。誤りはサーバの理由として出る）。
//
// 行を足すと、元の 2 行目より後ろは足した行の数だけ下がる。元が 6〜8 行目にあった見出しの行が 8 行目より後ろに
// 押し出されると、サーバが「その行が無い」と拒む（黙って通らず、うるさく落ちる）。
func fillDigestHeader(text string, lang i18n.Lang, value func(key string) (string, error)) (string, error) {
	text = strings.TrimLeft(text, " \t\r\n")
	if domain.LeadWord(text) != domain.LeadDigest {
		lines := []string{}
		label, err := value("label")
		if err != nil {
			return "", err
		}
		lines = append(lines, i18n.T(lang, "cli.session.digest.lead")+" "+label)
		for _, key := range domain.DigestHeaderKeys {
			val, err := value(key)
			if err != nil {
				return "", err
			}
			lines = append(lines, key+": "+val)
		}
		return strings.Join(lines, "\n") + "\n\n" + text, nil
	}
	first, rest, hasRest := strings.Cut(text, "\n")
	nl := "\n"
	if strings.HasSuffix(first, "\r") {
		first, nl = strings.TrimSuffix(first, "\r"), "\r\n"
	}
	if _, after := domain.LeadWordRest(first); strings.TrimSpace(after) == "" {
		label, err := value("label")
		if err != nil {
			return "", err
		}
		first = strings.TrimRight(first, " \t") + " " + label
	}
	present := map[string]bool{}
	if hasRest {
		for i, l := range strings.Split(rest, "\n") {
			if i >= domain.DigestHeaderLines-1 {
				break
			}
			if key, _, ok := domain.DigestHeaderField(strings.TrimSuffix(l, "\r")); ok {
				present[key] = true
			}
		}
	}
	var b strings.Builder
	b.WriteString(first)
	for _, key := range domain.DigestHeaderKeys {
		if present[key] {
			continue
		}
		val, err := value(key)
		if err != nil {
			return "", err
		}
		b.WriteString(nl + key + ": " + val)
	}
	if hasRest {
		b.WriteString(nl + rest)
	}
	return b.String(), nil
}

// digestPath は pull が書くファイル（作業コピーと同じ置き場。ID は大文字にする）。
func (c *Ctx) digestPath(id string) string {
	return filepath.Join(c.workDir(), strings.ToUpper(id)+"-digest.md")
}

// cmdSessionPull は最新の要約を取り出す。
func cmdSessionPull(c *Ctx, v *Values) error {
	limit := v.Int("limit")
	if limit < 1 {
		return i18n.Errorf("cli.err.session.limit")
	}
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	id := v.Str("id")
	path, err := c.issuePath(id, "")
	if err != nil {
		return err
	}
	detail, err := c.getObject(cl, path)
	if err != nil {
		return err
	}
	comments, err := mustObjects(detail, "comments")
	if err != nil {
		return err
	}
	shownID := jsonorder.Str(mustOr(detail, "id", id))
	var picked []*jsonorder.Object
	for i := len(comments) - 1; i >= 0 && int64(len(picked)) < limit; i-- {
		if domain.LeadWord(comments[i].String("content")) == domain.LeadDigest {
			picked = append(picked, comments[i])
		}
	}
	if len(picked) == 0 {
		c.Println(i18n.T(c.Lang, "cli.session.pull.none", "id", shownID))
		return nil
	}
	text := renderDigests(c.Lang, shownID, picked)
	if err := os.MkdirAll(c.workDir(), 0o777); err != nil {
		return err
	}
	if err := writeText(filepath.Join(c.workDir(), ".gitignore"), "*\n"); err != nil {
		return err
	}
	file := c.digestPath(shownID)
	if err := writeText(file, text); err != nil {
		return err
	}
	c.Printf("%s", text)
	fmt.Fprintln(c.Stderr, i18n.T(c.Lang, "cli.session.pull.written", "path", c.rel(file)))
	return nil
}

// mustOr はキーの値（無ければ def）。
func mustOr(o *jsonorder.Object, key string, def any) any {
	if v, ok := o.Get(key); ok {
		return v
	}
	return def
}

// renderDigests は取り出した要約（新しい順）を 1 つの Markdown にする。本文はサーバに保存された形のまま
// （端末のパスはサーバが送られた手がかりで置き換え済み）で、ここで足すのは見出しの行だけ。
func renderDigests(lang i18n.Lang, id string, picked []*jsonorder.Object) string {
	var b strings.Builder
	b.WriteString(i18n.T(lang, "cli.session.pull.title", "id", id, "count", len(picked)) + "\n")
	for _, cm := range picked {
		seq, _ := cm.Get("seq")
		b.WriteString("\n" + i18n.T(lang, "cli.session.pull.entry", "ts", cm.String("ts"), "seq", strconv.FormatInt(intOf(seq), 10)) + "\n\n")
		content := strings.TrimRight(cm.String("content"), "\n")
		b.WriteString(content + "\n")
	}
	return b.String()
}
