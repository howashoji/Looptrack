package cli

// タイムライン（DESIGN.md §9-9）のサブコマンド: `looptrack timeline post|list|show|revise|withdraw|ack|status|check|unread|now|seen|commit`。
// 検査（種類・宛先・権限・期限・資格情報・未達と未了解の判定）はサーバの service の 1 か所で、ここでは行わない。
//
//   - post は種類（update / info / request / hold）と宛先（--all・--to <login>・--to <login> --session <ID>）を選ぶ。依頼と待機は
//     --to-issue <ID>・--to-branch <名前>・--to-machine <ラベル>・--others にも出せる（宛先のセッションはサーバが読む時点で解決する）。
//     本文は引数か --file（既定は標準入力）から読む（message send と同じ）。update（近況）は宛先を省くと全員になる
//   - commit は作業ディレクトリ（--dir）の git から、--since の後から HEAD までのコミット（省けば HEAD の 1 件）を送る
//     （hook が作業区切りで、切り離した子プロセスとして同じ規則で送る。--pending は送り終えたら消す hook の「送りかけ」の記録）
//   - list は既定でこのセッションに効く有効な書き込みを出す。--pending は未達と未了解だけ。読むだけでは受け取りを記録しない
//   - ack は --version を省くと最新の版を読み、本文を出してからその版を了解する
//   - unread は未読と未了解を出し、出し終えてから読んだ位置を進める（--no-seen と --json とセッションの無い読み手は進めない。--json は読んだ側が
//     advance_to を seen に渡して進める）
//   - seen は読んだ位置を進める（hook が注入の文を作り終えた後に送る）
//   - 本文は別のセッションが書いた文章なので、連絡と同じく行ごとの引用で出す

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/dashstdin"
	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/i18n"
)

// TimelineCommand は `looptrack timeline` の引数の木。
func TimelineCommand(lang i18n.Lang) *Command {
	id := func() *Arg { return &Arg{Name: "id", Kind: Int, Help: i18n.T(lang, "cli.arg.timeline.id")} }
	jsonArg := func() *Arg { return &Arg{Name: "--json", Kind: Bool, Help: i18n.T(lang, "cli.arg.timeline.json")} }
	body := func() *Arg {
		return &Arg{Name: "body", Nargs: "?", Default: "", Help: i18n.T(lang, "cli.arg.timeline.body")}
	}
	file := func() *Arg {
		return &Arg{Name: "--file", Default: dashstdin.Dash, Metavar: "FILE", Help: i18n.T(lang, "cli.arg.timeline.file")}
	}
	return &Command{
		Name:        "timeline",
		Description: i18n.T(lang, "cli.cmd.timeline.root.desc"),
		Subs: []*Command{
			{Name: "post", Help: i18n.T(lang, "cli.cmd.timeline.post"), Run: cmdTimelinePost, Args: []*Arg{
				{Name: "kind", Choices: []string{"update", "info", "request", "hold"}, Help: i18n.T(lang, "cli.arg.timeline.kind")},
				body(),
				{Name: "--all", Kind: Bool, Help: i18n.T(lang, "cli.arg.timeline.all")},
				{Name: "--to", Metavar: "LOGIN", Help: i18n.T(lang, "cli.arg.timeline.to")},
				{Name: "--session", Metavar: "ID", Help: i18n.T(lang, "cli.arg.timeline.session")},
				{Name: "--to-issue", Metavar: "ID", Help: i18n.T(lang, "cli.arg.timeline.to_issue")},
				{Name: "--to-branch", Metavar: "NAME", Help: i18n.T(lang, "cli.arg.timeline.to_branch")},
				{Name: "--to-machine", Metavar: "LABEL", Help: i18n.T(lang, "cli.arg.timeline.to_machine")},
				{Name: "--others", Kind: Bool, Help: i18n.T(lang, "cli.arg.timeline.others")},
				{Name: "--ttl", Metavar: "DURATION", Help: i18n.T(lang, "cli.arg.timeline.ttl")},
				{Name: "--issue", Metavar: "ID", Help: i18n.T(lang, "cli.arg.timeline.issue")},
				{Name: "--release-issue", Metavar: "ID", Help: i18n.T(lang, "cli.arg.timeline.release_issue")},
				{Name: "--release-lease", Metavar: "NAME", Help: i18n.T(lang, "cli.arg.timeline.release_lease")},
				file(),
			}},
			{Name: "list", Help: i18n.T(lang, "cli.cmd.timeline.list"), Run: cmdTimelineList, Args: []*Arg{
				{Name: "--sent", Kind: Bool, Help: i18n.T(lang, "cli.arg.timeline.sent")},
				{Name: "--pending", Kind: Bool, Help: i18n.T(lang, "cli.arg.timeline.pending")},
				jsonArg(),
			}},
			{Name: "show", Help: i18n.T(lang, "cli.cmd.timeline.show"), Run: cmdTimelineShow, Args: []*Arg{id(), jsonArg()}},
			{Name: "revise", Help: i18n.T(lang, "cli.cmd.timeline.revise"), Run: cmdTimelineRevise, Args: []*Arg{id(), body(),
				{Name: "--ttl", Metavar: "DURATION", Help: i18n.T(lang, "cli.arg.timeline.revise_ttl")},
				file(),
			}},
			{Name: "withdraw", Help: i18n.T(lang, "cli.cmd.timeline.withdraw"), Run: cmdTimelineWithdraw, Args: []*Arg{id(),
				{Name: "--reason", Metavar: "TEXT", Help: i18n.T(lang, "cli.arg.timeline.reason")},
			}},
			{Name: "ack", Help: i18n.T(lang, "cli.cmd.timeline.ack"), Run: cmdTimelineAck, Args: []*Arg{id(),
				{Name: "--version", Kind: Int, Metavar: "N", Help: i18n.T(lang, "cli.arg.timeline.version")},
			}},
			{Name: "status", Help: i18n.T(lang, "cli.cmd.timeline.status"), Run: cmdTimelineStatus, Args: []*Arg{id(), jsonArg()}},
			{Name: "check", Help: i18n.T(lang, "cli.cmd.timeline.check"), Run: cmdTimelineCheck, Args: []*Arg{jsonArg()}},
			{Name: "unread", Help: i18n.T(lang, "cli.cmd.timeline.unread"), Run: cmdTimelineUnread, Args: []*Arg{
				{Name: "--limit", Kind: Int, Metavar: "N", Help: i18n.T(lang, "cli.arg.timeline.limit")},
				{Name: "--now", Kind: Bool, Help: i18n.T(lang, "cli.arg.timeline.now")},
				{Name: "--no-seen", Kind: Bool, Help: i18n.T(lang, "cli.arg.timeline.no_seen")},
				jsonArg(),
			}},
			{Name: "now", Help: i18n.T(lang, "cli.cmd.timeline.now"), Run: cmdTimelineNow, Args: []*Arg{jsonArg()}},
			{Name: "seen", Help: i18n.T(lang, "cli.cmd.timeline.seen"), Run: cmdTimelineSeen, Args: []*Arg{
				{Name: "seq", Kind: Int, Help: i18n.T(lang, "cli.arg.timeline.seq")},
			}},
			{Name: "commit", Help: i18n.T(lang, "cli.cmd.timeline.commit"), Run: cmdTimelineCommit, Args: []*Arg{
				{Name: "--since", Metavar: "SHA", Help: i18n.T(lang, "cli.arg.timeline.since")},
				{Name: "--until", Metavar: "SHA", Help: i18n.T(lang, "cli.arg.timeline.until")},
				{Name: "--branch", Metavar: "NAME", Help: i18n.T(lang, "cli.arg.timeline.branch")},
				{Name: "--dir", Metavar: "DIR", Help: i18n.T(lang, "cli.arg.timeline.dir")},
				{Name: "--pending", Metavar: "FILE", Help: i18n.T(lang, "cli.arg.timeline.pending")},
			}},
		},
	}
}

// TimelineMain は `looptrack timeline` の入口。args は timeline の後ろの引数。終了コードを返す。
func TimelineMain(args []string, stdio IO, e env.Env) int {
	return runGroup("looptrack timeline", TimelineCommand, args, stdio, e)
}

// timelinePostItem は書き込み 1 件の応答。
type timelinePostItem struct {
	ID             int64  `json:"id"`
	Kind           string `json:"kind"`
	To             string `json:"to"`
	ToUser         string `json:"to_user"`
	ToSession      string `json:"to_session"`
	ToIssue        string `json:"to_issue"`
	ToBranch       string `json:"to_branch"`
	ToMachine      string `json:"to_machine"`
	From           string `json:"from"`
	FromMachine    string `json:"from_machine"`
	Issue          string `json:"issue"`
	Version        int64  `json:"version"`
	LastVersion    int64  `json:"last_version"`
	Seq            int64  `json:"seq"`
	Body           string `json:"body"`
	ExpiresAt      string `json:"expires_at"`
	State          string `json:"state"`
	WithdrawReason string `json:"withdraw_reason"`
	WithdrawnBy    string `json:"withdrawn_by"`
	SameUser       bool   `json:"same_user"`
	Undelivered    bool   `json:"undelivered"`
	Unacked        bool   `json:"unacked"`
}

// timelinePostDo はタイムラインの API を呼び、応答の本文を v に読む（v が nil なら読まない）。古いサーバ（経路が無い 404）は
// 「このサーバはタイムラインに対応していない」にする（占有と同じ扱い）。
func timelinePostDo(cl *api.Client, req api.Request, v any) (*api.Response, error) {
	req.Raw = true
	res, err := cl.Do(req)
	if err != nil {
		var ae *api.Error
		if errors.As(err, &ae) && ae.Status == http.StatusNotFound && ae.Code == "unknown_api" {
			return nil, i18n.Errorf("cli.err.timeline.unsupported")
		}
		return nil, err
	}
	if v != nil {
		if err := json.Unmarshal(res.Raw, v); err != nil {
			return nil, i18n.Errorf("cli.err.timeline.bad_response", "reason", err)
		}
	}
	return res, nil
}

// timelinePostPath は /projects/<slug>/timeline<suffix>。
func (c *Ctx) timelinePostPath(suffix string) (string, error) {
	return c.ProjectPath("/timeline" + suffix)
}

// timelinePostIDPath は /projects/<slug>/timeline/<id><suffix>。
func (c *Ctx) timelinePostIDPath(v *Values, suffix string) (string, error) {
	return c.timelinePostPath("/" + strconv.FormatInt(v.Int("id"), 10) + suffix)
}

// timelinePostTarget は宛先の表示（プロジェクト全員・利用者・利用者のセッション・イシュー・ブランチ・端末・自分以外）。
func timelinePostTarget(lang i18n.Lang, b timelinePostItem) string {
	switch b.To {
	case "user":
		return b.ToUser
	case "session":
		return b.ToUser + " / " + timelinePostClaim(b.ToSession)
	case "issue":
		return i18n.T(lang, "cli.timeline.to_issue", "id", b.ToIssue)
	case "branch":
		return i18n.T(lang, "cli.timeline.to_branch", "name", timelinePostClaim(b.ToBranch))
	case "machine":
		return i18n.T(lang, "cli.timeline.to_machine", "label", timelinePostClaim(b.ToMachine))
	case "others":
		return i18n.T(lang, "cli.timeline.to_others")
	}
	return i18n.T(lang, "cli.timeline.to_project")
}

// timelinePostHeading は見出しの 1 行（ID・版・種類・出した利用者と同じ利用者か・端末・宛先・期限・添えたイシュー・状態の印）。
func (c *Ctx) timelinePostHeading(b timelinePostItem, loc *time.Location) string {
	who := i18n.T(c.Lang, "cli.timeline.other_user")
	if b.SameUser {
		who = i18n.T(c.Lang, "cli.timeline.same_user")
	}
	parts := []string{"#" + strconv.FormatInt(b.ID, 10) + " v" + strconv.FormatInt(b.Version, 10), b.Kind,
		i18n.T(c.Lang, "cli.timeline.sender", "login", orDash(b.From), "who", who), timelinePostClaim(b.FromMachine), "-> " + timelinePostTarget(c.Lang, b)}
	if b.ExpiresAt != "" {
		parts = append(parts, i18n.T(c.Lang, "cli.timeline.expires", "at", leaseTime(b.ExpiresAt, loc)))
	}
	if b.Issue != "" {
		parts = append(parts, b.Issue)
	}
	if b.State != "" && b.State != "active" {
		parts = append(parts, "["+b.State+"]")
	}
	if b.Undelivered {
		parts = append(parts, i18n.T(c.Lang, "cli.timeline.mark_undelivered"))
	}
	if b.Unacked {
		parts = append(parts, i18n.T(c.Lang, "cli.timeline.mark_unacked"))
	}
	return strings.Join(parts, "  ")
}

// printTimelinePost は見出しと、引用の印を付けた本文を出す（別のセッションが書いた本文を AI が地の文と読まないように）。
// 取り下げ済みは本文の代わりに理由を出す。
func (c *Ctx) printTimelinePost(b timelinePostItem, loc *time.Location) {
	c.Println(c.timelinePostHeading(b, loc))
	body := b.Body
	if b.State == "withdrawn" {
		c.Println(i18n.T(c.Lang, "cli.timeline.withdrawn_by", "by", orDash(b.WithdrawnBy)))
		body = orDash(b.WithdrawReason)
	}
	c.Println(timelinePostQuote(body))
}

// timelinePostQuote は本文や取り下げの理由を行ごとの引用にする（見出しと同じ形の行があっても地の文にならない）。
// TimelineQuote はほかのセッションが書いた文を引用の形（各行の頭に > ）にする（hook の文と CLI の表示で同じ形）。
func TimelineQuote(text string) string { return timelinePostQuote(text) }

func timelinePostQuote(text string) string {
	text = strings.TrimRight(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	return "> " + strings.ReplaceAll(text, "\n", "\n> ")
}

// timelinePostClaim は参加者が名乗った値（端末のラベル・セッション）をバッククォートで囲む。値にバッククォートが入っていると
// 囲みを抜けて地の文に見えるので、その値は載せずに ? を出す（値そのものは --json で読める）。
func timelinePostClaim(v string) string {
	if strings.Contains(v, "`") {
		return "?"
	}
	return presenceClaim(v)
}

// timelinePostLoc は応答の timezone（サーバのローカル時刻）。
func timelinePostLoc(lang i18n.Lang, tz string) *time.Location {
	loc, _ := serverTZ(lang, jsonorder.NewObject().Set("timezone", tz))
	return loc
}

// timelinePostBody は本文を引数か --file（既定は標準入力）から読む。
func (c *Ctx) timelinePostBody(v *Values) (string, error) {
	if body := v.Str("body"); body != "" {
		return body, nil
	}
	return c.readDigestBody(v.Str("file"))
}

func cmdTimelinePost(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	path, err := c.timelinePostPath("")
	if err != nil {
		return err
	}
	// 宛先の値をどの組み合わせで受けるか（重ねたときに拒むか）はサーバが決める。ここでは指定された欄から種類を選んで値をそのまま送る
	to := ""
	switch {
	case v.Bool("all") || (v.Str("kind") == "update" && v.Str("to") == "" && v.Str("session") == "" && v.Str("to_issue") == "" &&
		v.Str("to_branch") == "" && v.Str("to_machine") == "" && !v.Bool("others")):
		to = "project"
	case v.Bool("others"):
		to = "others"
	case v.Str("to_issue") != "":
		to = "issue"
	case v.Str("to_branch") != "":
		to = "branch"
	case v.Str("to_machine") != "":
		to = "machine"
	case v.Str("session") != "":
		to = "session"
	case v.Str("to") != "":
		to = "user"
	}
	ttl, err := c.leaseTTLSeconds(v)
	if err != nil {
		return err
	}
	body, err := c.timelinePostBody(v)
	if err != nil {
		return err
	}
	req := jsonorder.NewObject().Set("kind", v.Str("kind")).Set("to", to).Set("user", v.Str("to")).Set("session", v.Str("session"))
	for _, k := range []string{"to_issue", "to_branch", "to_machine"} { // 古いサーバに知らない欄を送らない（空なら省く）
		if v.Str(k) != "" {
			req.Set(k, v.Str(k))
		}
	}
	req.Set("body", body).
		Set("issue", v.Str("issue")).Set("release_issue", v.Str("release_issue")).Set("release_lease", v.Str("release_lease")).Set("machine_label", c.memLabel())
	if ttl > 0 {
		req.Set("ttl_seconds", ttl)
	}
	var res struct {
		TimelinePost timelinePostItem `json:"post"`
		Timezone     string           `json:"timezone"`
	}
	if _, err := timelinePostDo(cl, api.Request{Method: http.MethodPost, Path: path, Body: req}, &res); err != nil {
		return err
	}
	c.Println(i18n.T(c.Lang, "cli.timeline.posted", "id", res.TimelinePost.ID, "kind", res.TimelinePost.Kind, "to", timelinePostTarget(c.Lang, res.TimelinePost)))
	if res.TimelinePost.ExpiresAt != "" {
		c.Println(i18n.T(c.Lang, "cli.timeline.expires", "at", leaseTime(res.TimelinePost.ExpiresAt, timelinePostLoc(c.Lang, res.Timezone))))
	}
	return nil
}

func cmdTimelineList(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	if v.Bool("sent") && v.Bool("pending") {
		return i18n.Errorf("cli.err.timeline.sent_pending")
	}
	suffix := ""
	switch {
	case v.Bool("pending"):
		suffix = "/pending"
	case v.Bool("sent"):
		suffix = "?sent=1"
	}
	path, err := c.timelinePostPath(suffix)
	if err != nil {
		return err
	}
	var body struct {
		TimelinePosts []timelinePostItem `json:"posts"`
		Undelivered   []timelinePostItem `json:"undelivered"`
		Unacked       []timelinePostItem `json:"unacked"`
		Timezone      string             `json:"timezone"`
	}
	res, err := timelinePostDo(cl, api.Request{Method: http.MethodGet, Path: path}, &body)
	if err != nil {
		return err
	}
	if v.Bool("json") {
		return c.printRawJSON(res.Raw)
	}
	list := body.TimelinePosts
	if v.Bool("pending") { // 未達と未了解を 1 本にまとめる（同じ書き込みは 1 度だけ。並びはサーバの順で未了解を先に）
		seen := map[int64]bool{}
		for _, b := range append(append([]timelinePostItem{}, body.Unacked...), body.Undelivered...) {
			if !seen[b.ID] {
				seen[b.ID] = true
				list = append(list, b)
			}
		}
	}
	if len(list) == 0 {
		c.Println(i18n.T(c.Lang, "cli.timeline.none"))
		return nil
	}
	loc := timelinePostLoc(c.Lang, body.Timezone)
	for i, b := range list {
		if i > 0 {
			c.Println("")
		}
		c.printTimelinePost(b, loc)
	}
	return nil
}

func cmdTimelineShow(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	path, err := c.timelinePostIDPath(v, "")
	if err != nil {
		return err
	}
	var body struct {
		TimelinePost timelinePostItem `json:"post"`
		Events       []struct {
			Action       string `json:"action"`
			Version      int64  `json:"version"`
			User         string `json:"user"`
			MachineLabel string `json:"machine_label"`
			Reason       string `json:"reason"`
			ExpiresAt    string `json:"expires_at"`
			CreatedAt    string `json:"created_at"`
		} `json:"events"`
		Timezone string `json:"timezone"`
	}
	res, err := timelinePostDo(cl, api.Request{Method: http.MethodGet, Path: path}, &body)
	if err != nil {
		return err
	}
	if v.Bool("json") {
		return c.printRawJSON(res.Raw)
	}
	loc := timelinePostLoc(c.Lang, body.Timezone)
	c.printTimelinePost(body.TimelinePost, loc)
	c.Println("")
	for _, e := range body.Events { // 出来事の記録（前の版の本文は --json で読む）
		fields := []string{leaseTime(e.CreatedAt, loc), "v" + strconv.FormatInt(e.Version, 10), e.Action, orDash(e.User), timelinePostClaim(e.MachineLabel)}
		if e.ExpiresAt != "" {
			fields = append(fields, i18n.T(c.Lang, "cli.timeline.expires", "at", leaseTime(e.ExpiresAt, loc)))
		}
		c.Println(strings.Join(fields, "  "))
		if e.Reason != "" { // 取り下げの理由は名乗った文なので、本文と同じく引用で出す
			c.Println(timelinePostQuote(e.Reason))
		}
	}
	return nil
}

// timelinePostWritten は改訂・取り下げ・了解の応答を読む。
func (c *Ctx) timelinePostWrite(v *Values, suffix string, req *jsonorder.Object) (*timelinePostItem, error) {
	cl, err := c.RequireAPI("")
	if err != nil {
		return nil, err
	}
	path, err := c.timelinePostIDPath(v, suffix)
	if err != nil {
		return nil, err
	}
	var res struct {
		TimelinePost timelinePostItem `json:"post"`
	}
	if _, err := timelinePostDo(cl, api.Request{Method: http.MethodPost, Path: path, Body: req}, &res); err != nil {
		return nil, err
	}
	return &res.TimelinePost, nil
}

func cmdTimelineRevise(c *Ctx, v *Values) error {
	ttl, err := c.leaseTTLSeconds(v)
	if err != nil {
		return err
	}
	req := jsonorder.NewObject().Set("machine_label", c.memLabel())
	body := v.Str("body")
	// 本文を省いて期限だけを延ばすときは標準入力を待たない（--file を明示したときは読む）
	if body == "" && (ttl == 0 || v.Str("file") != dashstdin.Dash) {
		if body, err = c.readDigestBody(v.Str("file")); err != nil {
			return err
		}
	}
	if body != "" {
		req.Set("body", body)
	}
	if ttl > 0 {
		req.Set("ttl_seconds", ttl)
	}
	b, err := c.timelinePostWrite(v, "/revise", req)
	if err != nil {
		return err
	}
	c.Println(i18n.T(c.Lang, "cli.timeline.revised", "id", b.ID, "version", b.Version))
	return nil
}

func cmdTimelineWithdraw(c *Ctx, v *Values) error {
	b, err := c.timelinePostWrite(v, "/withdraw", jsonorder.NewObject().Set("reason", v.Str("reason")).Set("machine_label", c.memLabel()))
	if err != nil {
		return err
	}
	c.Println(i18n.T(c.Lang, "cli.timeline.withdrawn", "id", b.ID))
	return nil
}

func cmdTimelineAck(c *Ctx, v *Values) error {
	version := v.Int("version")
	if !v.IsSet("version") { // 版を省いたら最新の版を読み、本文を出してからその版を了解する（読まずに了解しない）
		cl, err := c.RequireAPI("")
		if err != nil {
			return err
		}
		path, err := c.timelinePostIDPath(v, "")
		if err != nil {
			return err
		}
		var cur struct {
			TimelinePost timelinePostItem `json:"post"`
			Timezone     string           `json:"timezone"`
		}
		if _, err := timelinePostDo(cl, api.Request{Method: http.MethodGet, Path: path}, &cur); err != nil {
			return err
		}
		c.printTimelinePost(cur.TimelinePost, timelinePostLoc(c.Lang, cur.Timezone))
		version = cur.TimelinePost.Version
	}
	b, err := c.timelinePostWrite(v, "/ack", jsonorder.NewObject().Set("version", version))
	if err != nil {
		return err
	}
	c.Println(i18n.T(c.Lang, "cli.timeline.acked", "id", b.ID, "version", b.Version))
	return nil
}

func cmdTimelineStatus(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	path, err := c.timelinePostIDPath(v, "/status")
	if err != nil {
		return err
	}
	var body struct {
		TimelinePost timelinePostItem `json:"post"`
		Rows         []struct {
			User         string `json:"user"`
			MachineLabel string `json:"machine_label"`
			Session      string `json:"session"`
			Received     int64  `json:"received"`
			Acked        int64  `json:"acked"`
			LastSeenAt   string `json:"last_seen_at"`
			ResolvedBy   string `json:"resolved_by"`
		} `json:"rows"`
		Timezone string `json:"timezone"`
	}
	res, err := timelinePostDo(cl, api.Request{Method: http.MethodGet, Path: path}, &body)
	if err != nil {
		return err
	}
	if v.Bool("json") {
		return c.printRawJSON(res.Raw)
	}
	loc := timelinePostLoc(c.Lang, body.Timezone)
	c.Println(c.timelinePostHeading(body.TimelinePost, loc))
	c.Println(i18n.T(c.Lang, "cli.timeline.status_heading"))
	if len(body.Rows) == 0 {
		c.Println(i18n.T(c.Lang, "cli.timeline.status_none"))
	}
	for _, r := range body.Rows {
		seen := "-"
		if r.LastSeenAt != "" {
			seen = leaseTime(r.LastSeenAt, loc)
		}
		c.Println(strings.Join([]string{r.User, timelinePostClaim(r.MachineLabel), timelinePostClaim(r.Session), strconv.FormatInt(r.Received, 10),
			strconv.FormatInt(r.Acked, 10), seen, orDash(r.ResolvedBy)}, "  "))
	}
	return nil
}

func cmdTimelineCheck(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	path, err := c.timelinePostPath("/check")
	if err != nil {
		return err
	}
	var body struct {
		Seq         int64   `json:"seq"`
		Holds       []int64 `json:"holds"`
		ReadSeq     *int64  `json:"read_seq"`
		HasPosition bool    `json:"has_position"`
		Behind      bool    `json:"behind"`
	}
	res, err := timelinePostDo(cl, api.Request{Method: http.MethodGet, Path: path}, &body)
	if err != nil {
		return err
	}
	if v.Bool("json") {
		return c.printRawJSON(res.Raw)
	}
	holds := make([]string, 0, len(body.Holds))
	for _, id := range body.Holds {
		holds = append(holds, strconv.FormatInt(id, 10))
	}
	// スクリプトと hook が読む行なので文面に混ぜない（lease acquire の lease_token= と同じ）
	line := "seq=" + strconv.FormatInt(body.Seq, 10) + " holds=" + strings.Join(holds, ",")
	if body.ReadSeq != nil { // 読んだ位置を返さない古いサーバでは足さない
		read := "-"
		if body.HasPosition {
			read = strconv.FormatInt(*body.ReadSeq, 10)
		}
		behind := "0"
		if body.Behind {
			behind = "1"
		}
		line += " read=" + read + " behind=" + behind
	}
	c.Println(line)
	return nil
}

func cmdTimelineUnread(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	q := "/unread"
	params := []string{}
	if n := v.Int("limit"); n > 0 {
		params = append(params, "limit="+strconv.FormatInt(n, 10))
	}
	if v.Bool("now") {
		params = append(params, "now=1")
	}
	if len(params) > 0 {
		q += "?" + strings.Join(params, "&")
	}
	path, err := c.timelinePostPath(q)
	if err != nil {
		return err
	}
	var body struct {
		Text      string `json:"text"`
		AdvanceTo int64  `json:"advance_to"`
	}
	res, err := timelinePostDo(cl, api.Request{Method: http.MethodGet, Path: path}, &body)
	if err != nil {
		return err
	}
	if v.Bool("json") {
		return c.printRawJSON(res.Raw)
	}
	text := body.Text
	if text == "" {
		text = i18n.T(c.Lang, "cli.timeline.unread_none")
	}
	// 出し終えてから位置を進める。書き込みに失敗したら進めない（出せなかった分を読んだことにしない）
	if _, err := fmt.Fprintln(c.Stdout, text); err != nil {
		return err
	}
	// セッションの無い読み手は位置を持たない（サーバも拒む）ので進めない。判定は要求のヘッダに実際に載る値で行う
	if v.Bool("no_seen") || cl.Mark.Session == "" {
		return nil
	}
	return c.timelineSeen(cl, body.AdvanceTo, false)
}

func cmdTimelineNow(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	path, err := c.timelinePostPath("/now")
	if err != nil {
		return err
	}
	var body struct {
		Text string `json:"text"`
	}
	res, err := timelinePostDo(cl, api.Request{Method: http.MethodGet, Path: path}, &body)
	if err != nil {
		return err
	}
	if v.Bool("json") {
		return c.printRawJSON(res.Raw)
	}
	c.Println(body.Text)
	return nil
}

func cmdTimelineSeen(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	return c.timelineSeen(cl, v.Int("seq"), true)
}

// timelineSeen は読んだ位置を seq まで進める。print なら結果を 1 行で出す（hook とスクリプトが読む行なので文面に混ぜない）。
func (c *Ctx) timelineSeen(cl *api.Client, seq int64, print bool) error {
	path, err := c.timelinePostPath("/seen")
	if err != nil {
		return err
	}
	var body struct {
		Seq      int64 `json:"seq"`
		Advanced bool  `json:"advanced"`
	}
	if _, err := timelinePostDo(cl, api.Request{Method: http.MethodPost, Path: path, Body: jsonorder.NewObject().Set("seq", seq)}, &body); err != nil {
		return err
	}
	if print {
		c.Println("seq=" + strconv.FormatInt(body.Seq, 10) + " advanced=" + strconv.FormatBool(body.Advanced))
	}
	return nil
}

// timelineSinceRe は --since に受け取る SHA の形（git の選択肢として読まれる「-」で始まる値を渡さない）。
var timelineSinceRe = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)

// timelineCommitLog は dir の git から、since の後から until（空なら HEAD）までの自分の線のコミット（--first-parent。merge や pull で
// 取り込んだほかの人のコミットを載せない）のうち新しい limit 件を古い順に並べたものと、全体の件数を返す。
// since が空か、until の先祖でない（rebase で書き換わった・消えた）なら until の 1 件だけ。until が HEAD の先祖でない（rebase で
// 書き換わった）なら何も返さない（今の線に無いコミットは載せない）。件名は 1 行目（%s）。
func timelineCommitLog(dir, since, until string, limit int) ([][2]string, int, error) {
	if until == "" {
		until = "HEAD"
	} else if !timelineAncestor(dir, until, "HEAD") {
		return nil, 0, nil
	}
	rng := []string{"-1", until}
	total := 1
	if since != "" && timelineAncestor(dir, since, until) {
		rng = []string{"--first-parent", since + ".." + until}
		out, err := gitOutput(dir, "rev-list", "--count", "--first-parent", since+".."+until)
		if err != nil {
			return nil, 0, err
		}
		if total, err = strconv.Atoi(strings.TrimSpace(out)); err != nil {
			return nil, 0, err
		}
	}
	out, err := gitOutput(dir, append([]string{"log", "--format=%H%x1f%s", "-n", strconv.Itoa(limit)}, rng...)...)
	if err != nil {
		return nil, 0, err
	}
	var commits [][2]string
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		sha, subject, _ := strings.Cut(line, "\x1f")
		commits = append([][2]string{{sha, subject}}, commits...) // git log は新しい順なので、古い順に並べ直す
	}
	return commits, max(total, len(commits)), nil
}

// timelineAncestor は a が b の先祖か（同じも含む）。無いコミット（rebase の後に消えた）や先祖でないものは偽にする。
func timelineAncestor(dir, a, b string) bool {
	_, err := gitOutput(dir, "merge-base", "--is-ancestor", a, b)
	return err == nil
}

func cmdTimelineCommit(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	path, err := c.timelinePostPath("/commits")
	if err != nil {
		return err
	}
	wd := v.Str("dir") // hook が切り離した子プロセスで送るときはコミットした作業ツリーを渡す
	if wd == "" {
		wd, _ = os.Getwd()
	}
	since := strings.TrimSpace(v.Str("since"))
	if since != "" && !timelineSinceRe.MatchString(since) {
		return i18n.Errorf("cli.err.timeline.commit_since")
	}
	until := strings.TrimSpace(v.Str("until"))
	if until != "" && !timelineSinceRe.MatchString(until) {
		return i18n.Errorf("cli.err.timeline.commit_since")
	}
	commits, total, err := timelineCommitLog(wd, since, until, maxTimelineCommits)
	if err != nil {
		return i18n.Errorf("cli.err.timeline.commit_git", "reason", err)
	}
	branch := strings.TrimSpace(v.Str("branch"))
	if branch == "" {
		if branch, err = gitValue(wd, "branch", "rev-parse", "--abbrev-ref", "HEAD"); err != nil {
			return err
		}
	}
	pending := v.Str("pending")
	if len(commits) == 0 {
		timelineClearPending(pending)
		c.Println("recorded=0 omitted=0 withheld=0")
		return nil
	}
	type commitJSON struct {
		SHA     string `json:"sha"`
		Subject string `json:"subject"`
	}
	list := make([]commitJSON, 0, len(commits))
	for _, cm := range commits {
		list = append(list, commitJSON{SHA: cm[0], Subject: cm[1]})
	}
	req := jsonorder.NewObject().Set("branch", branch).Set("commits", list).Set("omitted", total-len(commits))
	var body struct {
		Recorded int `json:"recorded"`
		Omitted  int `json:"omitted"`
		Withheld int `json:"withheld"`
	}
	if _, err := timelinePostDo(cl, api.Request{Method: http.MethodPost, Path: path, Body: req}, &body); err != nil {
		// 届かない・サーバの一時の誤り（5xx）なら記録を残し、hook が次の区切りで同じ範囲を送り直す。
		// 恒久の拒否（4xx・経路の無い古いサーバ）では送り直しても通らないので消す
		var ae *api.Error
		if !Unreachable(err) && !(errors.As(err, &ae) && ae.Status >= 500) {
			timelineClearPending(pending)
		}
		return err
	}
	timelineClearPending(pending)
	// hook とスクリプトが読む行なので文面に混ぜない（check と同じ）
	c.Println("recorded=" + strconv.Itoa(body.Recorded) + " omitted=" + strconv.Itoa(body.Omitted) + " withheld=" + strconv.Itoa(body.Withheld))
	return nil
}

// timelineClearPending は hook の「送りかけ」の記録を消す（空なら何もしない）。
func timelineClearPending(path string) {
	if path != "" {
		_ = os.Remove(path)
	}
}

// maxTimelineCommits は 1 回に送るコミットの数（サーバの上限と同じ。超えた分は件数だけを送る）。
const maxTimelineCommits = 20
