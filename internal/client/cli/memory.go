package cli

// プロジェクト記憶（DESIGN.md §9-9）のサブコマンド: `looptrack memory list|show|append|replace|delete|import`。
// 検査（種類・名前の形・上限・資格情報・範囲と権限・版の照合）はサーバの service の 1 か所で、ここでは行わない。
// 拒否されたらサーバの理由（行番号と規則の語）だけを出して終了コード 1。
//
//   - append は版を問わず節を末尾に足す（同時に書いても両方が残る）。行が無ければ作る
//   - replace は --version に読んだ版を渡す（違えば 409 で何も書かない。行が無いときは --version 0 で作る）
//   - delete は消した印の版を足す（本文は履歴に残る）
//
// 書き込みには端末のラベル（LOOPTRACK_MACHINE_LABEL か、ホスト名の最初の「.」より前）を添え、履歴に残す。

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/dashstdin"
	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/i18n"
)

// memoryKinds は記憶の種類（サーバの service.MemoryKinds と同じ値）。
var memoryKinds = []string{"handoff", "decision", "caveat"}

// MemoryCommand は `looptrack memory` の引数の木。
func MemoryCommand(lang i18n.Lang) *Command {
	key := func() []*Arg {
		return []*Arg{
			{Name: "kind", Choices: memoryKinds, Help: i18n.T(lang, "cli.arg.memory.kind")},
			{Name: "name", Nargs: "?", Default: "", Help: i18n.T(lang, "cli.arg.memory.name")},
			{Name: "--personal", Kind: Bool, Help: i18n.T(lang, "cli.arg.memory.personal")},
		}
	}
	file := &Arg{Name: "--file", Default: dashstdin.Dash, Metavar: "FILE", Help: i18n.T(lang, "cli.arg.memory.file")}
	return &Command{
		Name:        "memory",
		Description: i18n.T(lang, "cli.cmd.memory.root.desc"),
		Subs: []*Command{
			{Name: "list", Help: i18n.T(lang, "cli.cmd.memory.list"), Run: cmdMemoryList, Args: []*Arg{
				{Name: "--deleted", Kind: Bool, Help: i18n.T(lang, "cli.arg.memory.deleted")},
				{Name: "--json", Kind: Bool, Help: i18n.T(lang, "cli.arg.memory.json")},
			}},
			{Name: "show", Help: i18n.T(lang, "cli.cmd.memory.show"), Run: cmdMemoryShow, Args: append(key(),
				&Arg{Name: "--version", Kind: Int, Metavar: "N", Help: i18n.T(lang, "cli.arg.memory.show_version")},
				&Arg{Name: "--history", Kind: Bool, Help: i18n.T(lang, "cli.arg.memory.history")},
				&Arg{Name: "--json", Kind: Bool, Help: i18n.T(lang, "cli.arg.memory.json")},
			)},
			{Name: "append", Help: i18n.T(lang, "cli.cmd.memory.append"), Run: cmdMemoryAppend, Args: append(key(), file)},
			{Name: "replace", Help: i18n.T(lang, "cli.cmd.memory.replace"), Run: cmdMemoryReplace, Args: append(key(), file,
				&Arg{Name: "--version", Kind: Int, Required: true, Metavar: "N", Help: i18n.T(lang, "cli.arg.memory.replace_version")},
			)},
			{Name: "delete", Help: i18n.T(lang, "cli.cmd.memory.delete"), Run: cmdMemoryDelete, Args: append(key(),
				&Arg{Name: "--version", Kind: Int, Metavar: "N", Help: i18n.T(lang, "cli.arg.memory.delete_version")},
			)},
			{Name: "purge", Help: i18n.T(lang, "cli.cmd.memory.purge"), Run: cmdMemoryPurge, Args: append(key(),
				&Arg{Name: "--version", Kind: Int, Metavar: "N", Help: i18n.T(lang, "cli.arg.memory.purge_version")},
				&Arg{Name: "--all-versions", Kind: Bool, Help: i18n.T(lang, "cli.arg.memory.purge_all_versions")},
				&Arg{Name: "--reason", Required: true, Metavar: "TEXT", Help: i18n.T(lang, "cli.arg.memory.purge_reason")},
				&Arg{Name: "--yes", Kind: Bool, Help: i18n.T(lang, "cli.arg.memory.purge_yes")},
			)},
			{Name: "import", Help: i18n.T(lang, "cli.cmd.memory.import"), Run: cmdMemoryImport, Args: []*Arg{
				{Name: "dir", Nargs: "?", Default: "", Help: i18n.T(lang, "cli.arg.memory.import_dir")},
			}},
		},
	}
}

// MemoryMain は `looptrack memory` の入口。args は memory の後ろの引数。終了コードを返す。
func MemoryMain(args []string, stdio IO, e env.Env) int {
	return runGroup("looptrack memory", MemoryCommand, args, stdio, e)
}

// memResult は書き込みの応答。
type memResult struct {
	Memory struct {
		Scope   string `json:"scope"`
		Kind    string `json:"kind"`
		Name    string `json:"name"`
		Version int64  `json:"version"`
	} `json:"memory"`
	Created bool `json:"created"`
}

// memDo は記憶の API を呼び、応答の本文を v に読む（v が nil なら読まない）。古いサーバ（経路が無い 404）は
// 「このサーバはプロジェクト記憶に対応していない」にする。
func memDo(cl *api.Client, req api.Request, v any) (*api.Response, error) {
	req.Raw = true
	res, err := cl.Do(req)
	if err != nil {
		var ae *api.Error
		if errors.As(err, &ae) && ae.Status == http.StatusNotFound && ae.Code == "unknown_api" {
			return nil, i18n.Errorf("cli.err.memory.unsupported")
		}
		return nil, err
	}
	if v != nil {
		if err := json.Unmarshal(res.Raw, v); err != nil {
			return nil, i18n.Errorf("cli.err.memory.bad_response", "reason", err)
		}
	}
	return res, nil
}

// memPath は /projects/<slug>/memory/<kind>/<name><suffix>?scope=…。名前を省いたら handoff は main（サーバと同じ既定）。
func (c *Ctx) memPath(v *Values, suffix string) (string, error) {
	name := strings.TrimSpace(v.Str("name"))
	if name == "" {
		if v.Str("kind") != "handoff" {
			return "", i18n.Errorf("cli.err.memory.name_required", "kind", v.Str("kind"))
		}
		name = memoryDefaultHandoff
	}
	p, err := c.ProjectPath("/memory/" + api.PathEscape(v.Str("kind")) + "/" + api.PathEscape(name) + suffix)
	if err != nil {
		return "", err
	}
	if v.Bool("personal") {
		p += "?scope=personal"
	}
	return p, nil
}

// memLabel は端末のラベル（決められなければ空。サーバは空を受け付ける）。
func (c *Ctx) memLabel() string {
	wd, _ := os.Getwd()
	l, err := c.digestValues(wd)("label")
	if err != nil {
		return ""
	}
	return l
}

func cmdMemoryList(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	path, err := c.ProjectPath("/memory")
	if err != nil {
		return err
	}
	if v.Bool("deleted") {
		path += "?deleted=1"
	}
	var body struct {
		Memories []struct {
			Scope     string `json:"scope"`
			Kind      string `json:"kind"`
			Name      string `json:"name"`
			Version   int64  `json:"version"`
			Summary   string `json:"summary"`
			Deleted   bool   `json:"deleted"`
			UpdatedBy string `json:"updated_by"`
		} `json:"memories"`
	}
	res, err := memDo(cl, api.Request{Method: http.MethodGet, Path: path}, &body)
	if err != nil {
		return err
	}
	if v.Bool("json") {
		return c.printRawJSON(res.Raw)
	}
	if len(body.Memories) == 0 {
		c.Println(i18n.T(c.Lang, "cli.memory.list.none"))
		return nil
	}
	for _, m := range body.Memories {
		state := ""
		if m.Deleted {
			state = "  " + i18n.T(c.Lang, "cli.memory.list.deleted")
		}
		c.Println(strings.Join([]string{m.Kind + "/" + m.Name, m.Scope, "v" + strconv.FormatInt(m.Version, 10), orDash(m.UpdatedBy), m.Summary}, "  ") + state)
	}
	return nil
}

func (c *Ctx) printRawJSON(raw []byte) error {
	val, err := jsonorder.Decode(raw)
	if err != nil {
		return i18n.Errorf("cli.err.memory.bad_response", "reason", err)
	}
	c.PrintJSON(val)
	return nil
}

func cmdMemoryShow(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	path, err := c.memPath(v, "")
	if err != nil {
		return err
	}
	if v.IsSet("version") {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		path += sep + "version=" + url.QueryEscape(strconv.FormatInt(v.Int("version"), 10))
	}
	var body struct {
		Memory struct {
			Version int64  `json:"version"`
			Content string `json:"content"`
		} `json:"memory"`
		Version struct {
			Version int64  `json:"version"`
			Content string `json:"content"`
		} `json:"version"`
		Versions []struct {
			Version      int64  `json:"version"`
			Op           string `json:"op"`
			User         string `json:"user"`
			MachineLabel string `json:"machine_label"`
			Purged       bool   `json:"purged"`
			CreatedAt    string `json:"created_at"`
		} `json:"versions"`
	}
	res, err := memDo(cl, api.Request{Method: http.MethodGet, Path: path}, &body)
	if err != nil {
		return err
	}
	if v.Bool("json") {
		return c.printRawJSON(res.Raw)
	}
	if v.IsSet("version") {
		c.Printf("%s", ensureNewline(body.Version.Content))
		return nil
	}
	if v.Bool("history") {
		for _, h := range body.Versions {
			line := []string{"v" + strconv.FormatInt(h.Version, 10), h.Op, orDash(h.User), orDash(h.MachineLabel), h.CreatedAt}
			if h.Purged {
				line = append(line, i18n.T(c.Lang, "cli.memory.history_purged"))
			}
			c.Println(strings.Join(line, "  "))
		}
		return nil
	}
	c.Printf("%s", ensureNewline(body.Memory.Content))
	c.eprintln(i18n.T(c.Lang, "cli.memory.show.version", "version", body.Memory.Version))
	return nil
}

func ensureNewline(s string) string {
	if s == "" || strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}

func (c *Ctx) eprintln(s string) {
	if c.Stderr != nil {
		_, _ = c.Stderr.Write([]byte(s + "\n"))
	}
}

func cmdMemoryAppend(c *Ctx, v *Values) error {
	return c.memWrite(v, http.MethodPost, "/append", "")
}

func cmdMemoryReplace(c *Ctx, v *Values) error {
	return c.memWrite(v, http.MethodPut, "", `"`+strconv.FormatInt(v.Int("version"), 10)+`"`)
}

// memWrite は append と replace の共通部分（本文を読み、送り、結果の 1 行を出す）。
func (c *Ctx) memWrite(v *Values, method, suffix, ifMatch string) error {
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	path, err := c.memPath(v, suffix)
	if err != nil {
		return err
	}
	text, err := c.readDigestBody(v.Str("file"))
	if err != nil {
		return err
	}
	req := api.Request{Method: method, Path: path, Body: jsonorder.NewObject().Set("content", text).Set("machine_label", c.memLabel())}
	if ifMatch != "" {
		req.Headers = map[string]string{"If-Match": ifMatch}
	}
	var body memResult
	if _, err := memDo(cl, req, &body); err != nil {
		return err
	}
	m := body.Memory
	c.Println(i18n.T(c.Lang, "cli.memory.written", "kind", m.Kind, "name", m.Name, "scope", m.Scope, "version", m.Version))
	return nil
}

// cmdMemoryPurge は版の本体を消す。戻せないので --yes が無ければ何も送らない。
func cmdMemoryPurge(c *Ctx, v *Values) error {
	if !v.Bool("yes") {
		return i18n.Errorf("cli.err.memory.purge_yes")
	}
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	path, err := c.memPath(v, "/purge")
	if err != nil {
		return err
	}
	req := jsonorder.NewObject().Set("reason", v.Str("reason"))
	if v.IsSet("version") == v.Bool("all_versions") || (v.IsSet("version") && v.Int("version") <= 0) {
		return i18n.Errorf("cli.err.memory.purge_select")
	}
	if v.IsSet("version") {
		req = req.Set("version", v.Int("version"))
	} else {
		req = req.Set("all_versions", true)
	}
	var body struct {
		Purged struct {
			Bodies int   `json:"bodies"`
			Bytes  int64 `json:"bytes"`
		} `json:"purged"`
	}
	if _, err := memDo(cl, api.Request{Method: http.MethodPost, Path: path, Body: req}, &body); err != nil {
		return err
	}
	c.Println(i18n.T(c.Lang, "cli.memory.purged", "bodies", body.Purged.Bodies, "bytes", body.Purged.Bytes))
	return nil
}

func cmdMemoryDelete(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	path, err := c.memPath(v, "")
	if err != nil {
		return err
	}
	req := api.Request{Method: http.MethodDelete, Path: path, Body: jsonorder.NewObject().Set("machine_label", c.memLabel())}
	if v.IsSet("version") {
		req.Headers = map[string]string{"If-Match": `"` + strconv.FormatInt(v.Int("version"), 10) + `"`}
	}
	var body memResult
	if _, err := memDo(cl, req, &body); err != nil {
		return err
	}
	m := body.Memory
	c.Println(i18n.T(c.Lang, "cli.memory.deleted", "kind", m.Kind, "name", m.Name, "scope", m.Scope, "version", m.Version))
	return nil
}
