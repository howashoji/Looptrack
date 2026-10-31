package cli

// 連絡（DESIGN.md §9-9）のサブコマンド: `looptrack message send|inbox|show`。
// 検査（宛先・権限・上限・資格情報）はサーバの service の 1 か所で、ここでは行わない。
//
//   - send は宛先を --all（プロジェクト全員）か --to <login>（利用者）か --to <login> --session <ID>（そのセッション）で選ぶ。
//     本文は引数か --file（既定は標準入力）から読む
//   - inbox は既定でこのセッションの未読を出す。--mark-read を付けると出した連絡を既読にする。読むだけでは既読にしない
//     （hook が注入と一緒に印を付ける）

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/dashstdin"
	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/i18n"
)

// MessageCommand は `looptrack message` の引数の木。
func MessageCommand(lang i18n.Lang) *Command {
	return &Command{
		Name:        "message",
		Description: i18n.T(lang, "cli.cmd.message.root.desc"),
		Subs: []*Command{
			{Name: "send", Help: i18n.T(lang, "cli.cmd.message.send"), Run: cmdMessageSend, Args: []*Arg{
				{Name: "body", Nargs: "?", Default: "", Help: i18n.T(lang, "cli.arg.message.body")},
				{Name: "--all", Kind: Bool, Help: i18n.T(lang, "cli.arg.message.all")},
				{Name: "--to", Metavar: "LOGIN", Help: i18n.T(lang, "cli.arg.message.to")},
				{Name: "--session", Metavar: "ID", Help: i18n.T(lang, "cli.arg.message.session")},
				{Name: "--issue", Metavar: "ID", Help: i18n.T(lang, "cli.arg.message.issue")},
				{Name: "--file", Default: dashstdin.Dash, Metavar: "FILE", Help: i18n.T(lang, "cli.arg.message.file")},
			}},
			{Name: "inbox", Help: i18n.T(lang, "cli.cmd.message.inbox"), Run: cmdMessageInbox, Args: []*Arg{
				{Name: "--all", Kind: Bool, Help: i18n.T(lang, "cli.arg.message.inbox_all")},
				{Name: "--sent", Kind: Bool, Help: i18n.T(lang, "cli.arg.message.sent")},
				{Name: "--mark-read", Kind: Bool, Help: i18n.T(lang, "cli.arg.message.mark_read")},
				{Name: "--limit", Kind: Int, Metavar: "N", Help: i18n.T(lang, "cli.arg.message.limit")},
				{Name: "--json", Kind: Bool, Help: i18n.T(lang, "cli.arg.message.json")},
			}},
			{Name: "show", Help: i18n.T(lang, "cli.cmd.message.show"), Run: cmdMessageShow, Args: []*Arg{
				{Name: "id", Kind: Int, Help: i18n.T(lang, "cli.arg.message.id")},
				{Name: "--json", Kind: Bool, Help: i18n.T(lang, "cli.arg.message.json")},
			}},
		},
	}
}

// MessageMain は `looptrack message` の入口。args は message の後ろの引数。終了コードを返す。
func MessageMain(args []string, stdio IO, e env.Env) int {
	return runGroup("looptrack message", MessageCommand, args, stdio, e)
}

// MessageItem は連絡 1 件。
type MessageItem struct {
	ID          int64  `json:"id"`
	To          string `json:"to"`
	ToUser      string `json:"to_user"`
	ToSession   string `json:"to_session"`
	From        string `json:"from"`
	FromMachine string `json:"from_machine"`
	Issue       string `json:"issue"`
	Body        string `json:"body"`
	CreatedAt   string `json:"created_at"`
	Read        bool   `json:"read"`
}

// MessageInbox は受信箱の応答。
type MessageInbox struct {
	Messages  []MessageItem `json:"messages"`
	Unread    int           `json:"unread"`
	UnreadIDs []int64       `json:"unread_ids"`
}

// Inbox はこのセッションの未読を limit 件まで読む（hook が使う。既読の印は付けない）。
func (m *MemoryAccess) Inbox(limit int) (*MessageInbox, error) {
	path, err := m.ctx.ProjectPath("/inbox?unread=1&limit=" + strconv.Itoa(limit))
	if err != nil {
		return nil, err
	}
	var body MessageInbox
	if _, err := memDo(m.cl, api.Request{Method: http.MethodGet, Path: path}, &body); err != nil {
		return nil, err
	}
	return &body, nil
}

// MarkRead は連絡をこのセッションの既読にする。
func (m *MemoryAccess) MarkRead(ids []int64) error {
	return markMessagesRead(m.ctx, m.cl, ids)
}

func markMessagesRead(c *Ctx, cl *api.Client, ids []int64) error {
	path, err := c.ProjectPath("/inbox/read")
	if err != nil {
		return err
	}
	list := make([]any, 0, len(ids))
	for _, id := range ids {
		list = append(list, id)
	}
	_, err = memDo(cl, api.Request{Method: http.MethodPost, Path: path, Body: jsonorder.NewObject().Set("ids", list)}, nil)
	return err
}

func cmdMessageSend(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	path, err := c.ProjectPath("/messages")
	if err != nil {
		return err
	}
	to := ""
	switch {
	case v.Bool("all"):
		to = "project"
	case v.Str("session") != "":
		to = "session"
	case v.Str("to") != "":
		to = "user"
	}
	body := v.Str("body")
	if body == "" {
		if body, err = c.readDigestBody(v.Str("file")); err != nil {
			return err
		}
	}
	req := jsonorder.NewObject().Set("to", to).Set("user", v.Str("to")).Set("session", v.Str("session")).Set("body", body).
		Set("issue", v.Str("issue")).Set("machine_label", c.memLabel())
	var res struct {
		Message MessageItem `json:"message"`
	}
	if _, err := memDo(cl, api.Request{Method: http.MethodPost, Path: path, Body: req}, &res); err != nil {
		return err
	}
	c.Println(i18n.T(c.Lang, "cli.message.sent", "id", res.Message.ID, "to", messageTarget(c.Lang, res.Message)))
	return nil
}

// messageTarget は宛先の表示（プロジェクト全員・利用者・利用者のセッション）。
func messageTarget(lang i18n.Lang, m MessageItem) string {
	switch m.To {
	case "user":
		return m.ToUser
	case "session":
		return m.ToUser + " / " + m.ToSession
	}
	return i18n.T(lang, "cli.message.to_project")
}

func cmdMessageInbox(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	if v.Bool("sent") && v.Bool("mark_read") {
		return i18n.Errorf("cli.err.message.sent_mark_read") // 送った一覧を見せたまま受信の未読を既読にしない
	}
	query := []string{}
	if v.Bool("sent") {
		query = append(query, "sent=1")
	} else if !v.Bool("all") {
		query = append(query, "unread=1")
	}
	if v.IsSet("limit") {
		query = append(query, "limit="+strconv.FormatInt(v.Int("limit"), 10))
	}
	path, err := c.ProjectPath("/inbox?" + strings.Join(query, "&"))
	if err != nil {
		return err
	}
	var body MessageInbox
	res, err := memDo(cl, api.Request{Method: http.MethodGet, Path: path}, &body)
	if err != nil {
		return err
	}
	if v.Bool("mark_read") && len(body.Messages) > 0 { // 出した分だけを既読にする（--limit の外は未読のまま）
		ids := make([]int64, 0, len(body.Messages))
		for _, m := range body.Messages {
			ids = append(ids, m.ID)
		}
		if err := markMessagesRead(c, cl, ids); err != nil {
			return err
		}
	}
	if v.Bool("json") {
		return c.printRawJSON(res.Raw)
	}
	if len(body.Messages) == 0 {
		c.Println(i18n.T(c.Lang, "cli.message.none"))
		return nil
	}
	for _, m := range body.Messages {
		c.printMessage(m)
	}
	return nil
}

// printMessage は見出しと、引用の印を付けた本文を出す（別の人が書いた本文を、AI が地の文と読まないように hook の注入と同じ形にする）。
func (c *Ctx) printMessage(m MessageItem) {
	c.Println(c.messageHeading(m))
	body := strings.TrimRight(strings.ReplaceAll(m.Body, "\r\n", "\n"), "\n")
	c.Println("> " + strings.ReplaceAll(body, "\n", "\n> "))
}

// messageHeading は連絡の見出しの 1 行。
func (c *Ctx) messageHeading(m MessageItem) string {
	parts := []string{"#" + strconv.FormatInt(m.ID, 10), m.CreatedAt, orDash(m.From), orDash(m.FromMachine), "-> " + messageTarget(c.Lang, m)}
	if m.Issue != "" {
		parts = append(parts, m.Issue)
	}
	return strings.Join(parts, "  ")
}

func cmdMessageShow(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	path, err := c.ProjectPath("/messages/" + strconv.FormatInt(v.Int("id"), 10))
	if err != nil {
		return err
	}
	var body struct {
		Message MessageItem `json:"message"`
	}
	res, err := memDo(cl, api.Request{Method: http.MethodGet, Path: path}, &body)
	if err != nil {
		return err
	}
	if v.Bool("json") {
		return c.printRawJSON(res.Raw)
	}
	c.printMessage(body.Message)
	return nil
}
