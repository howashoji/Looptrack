package cli

// 占有（DESIGN.md §9-9）のサブコマンド: `looptrack lease list|show|acquire|extend|release|break`。
// 検査（名前の形・期限の上限・権限・持ち主の照合・資格情報）はサーバの service の 1 か所で、ここでは行わない。
// 取れなければサーバの理由（取っている者・目的・期限）を出して終了コード 1。本番への反映の前の手順やスクリプトは、この終了コードで止められる。
// 書き込みには端末のラベルを添え、記録に残す。

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/i18n"
)

// LeaseCommand は `looptrack lease` の引数の木。
func LeaseCommand(lang i18n.Lang) *Command {
	name := func() *Arg { return &Arg{Name: "name", Help: i18n.T(lang, "cli.arg.lease.name")} }
	ttl := func() *Arg { return &Arg{Name: "--ttl", Metavar: "DURATION", Help: i18n.T(lang, "cli.arg.lease.ttl")} }
	token := func() *Arg { return &Arg{Name: "--token", Metavar: "TOKEN", Help: i18n.T(lang, "cli.arg.lease.token")} }
	return &Command{
		Name:        "lease",
		Description: i18n.T(lang, "cli.cmd.lease.root.desc"),
		Subs: []*Command{
			{Name: "list", Help: i18n.T(lang, "cli.cmd.lease.list"), Run: cmdLeaseList, Args: []*Arg{
				{Name: "--json", Kind: Bool, Help: i18n.T(lang, "cli.arg.lease.json")},
			}},
			{Name: "show", Help: i18n.T(lang, "cli.cmd.lease.show"), Run: cmdLeaseShow, Args: []*Arg{name(),
				{Name: "--json", Kind: Bool, Help: i18n.T(lang, "cli.arg.lease.json")},
			}},
			{Name: "acquire", Help: i18n.T(lang, "cli.cmd.lease.acquire"), Run: cmdLeaseAcquire, Args: []*Arg{name(), ttl(),
				{Name: "--purpose", Required: true, Metavar: "TEXT", Help: i18n.T(lang, "cli.arg.lease.purpose")},
			}},
			{Name: "extend", Help: i18n.T(lang, "cli.cmd.lease.extend"), Run: cmdLeaseExtend, Args: []*Arg{name(), ttl(), token()}},
			{Name: "release", Help: i18n.T(lang, "cli.cmd.lease.release"), Run: cmdLeaseRelease, Args: []*Arg{name(), token()}},
			{Name: "break", Help: i18n.T(lang, "cli.cmd.lease.break"), Run: cmdLeaseBreak, Args: []*Arg{name(),
				{Name: "--reason", Required: true, Metavar: "TEXT", Help: i18n.T(lang, "cli.arg.lease.reason")},
			}},
		},
	}
}

// LeaseMain は `looptrack lease` の入口。args は lease の後ろの引数。終了コードを返す。
func LeaseMain(args []string, stdio IO, e env.Env) int {
	return runGroup("looptrack lease", LeaseCommand, args, stdio, e)
}

// leaseItem は占有 1 件の応答。
type leaseItem struct {
	Name         string `json:"name"`
	Holder       string `json:"holder"`
	MachineLabel string `json:"machine_label"`
	SessionID    string `json:"session_id"`
	Purpose      string `json:"purpose"`
	ExpiresAt    string `json:"expires_at"`
	Expired      bool   `json:"expired"`
}

// leaseDo は占有の API を呼び、応答の本文を v に読む（v が nil なら読まない）。古いサーバ（経路が無い 404）は
// 「このサーバは占有に対応していない」にする。
func leaseDo(cl *api.Client, req api.Request, v any) (*api.Response, error) {
	req.Raw = true
	res, err := cl.Do(req)
	if err != nil {
		var ae *api.Error
		if errors.As(err, &ae) && ae.Status == http.StatusNotFound && ae.Code == "unknown_api" {
			return nil, i18n.Errorf("cli.err.lease.unsupported")
		}
		return nil, err
	}
	if v != nil {
		if err := json.Unmarshal(res.Raw, v); err != nil {
			return nil, i18n.Errorf("cli.err.lease.bad_response", "reason", err)
		}
	}
	return res, nil
}

// leasePath は /projects/<slug>/leases/<name><suffix>。
func (c *Ctx) leasePath(v *Values, suffix string) (string, error) {
	return c.ProjectPath("/leases/" + api.PathEscape(strings.TrimSpace(v.Str("name"))) + suffix)
}

// leaseTTLSeconds は --ttl（30m・2h のような Go の長さ）を秒にする。省いたら 0（サーバの既定）。
func (c *Ctx) leaseTTLSeconds(v *Values) (int64, error) {
	raw := strings.TrimSpace(v.Str("ttl"))
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < time.Second {
		return 0, i18n.Errorf("cli.err.lease.ttl")
	}
	return int64(d / time.Second), nil
}

// leaseTime は RFC 3339 の時刻を loc の分までの形にする（読めなければそのまま）。
func leaseTime(s string, loc *time.Location) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.In(loc).Format("2006-01-02 15:04")
}

// leaseLine は占有 1 件の 1 行（list と summary）。
func leaseLine(o *jsonorder.Object, loc *time.Location) string {
	return strings.Join([]string{getStr(o, "name", ""), getStr(o, "holder", ""), orDash(getStr(o, "machine_label", "")), orDash(getStr(o, "session_id", "")),
		leaseTime(getStr(o, "expires_at", ""), loc), getStr(o, "purpose", "")}, "  ")
}

// presenceLine は動いているほかのセッション 1 本の 1 行（summary）。
func presenceLine(o *jsonorder.Object, loc *time.Location) string {
	head := getStr(o, "head_sha", "")
	var issues []string
	if l, ok := get(o, "issues", nil).([]any); ok {
		for _, x := range l {
			if id, ok := x.(string); ok {
				issues = append(issues, id)
			}
		}
	}
	return strings.Join([]string{getStr(o, "user", ""), presenceClaim(getStr(o, "machine_label", "")), orDash(getStr(o, "agent", "")), presenceClaim(getStr(o, "branch", "")),
		orDash(head[:min(len(head), 8)]), presenceClaim(getStr(o, "directory", "")), orDash(strings.Join(issues, ",")), leaseTime(getStr(o, "last_seen_at", ""), loc)}, "  ")
}

// presenceClaim は参加者が名乗った値をバッククォートで囲む（AI の文脈で地の文に見えないように。サーバの検査で値にバッククォートは入らない）。
func presenceClaim(v string) string {
	if v == "" {
		return "-"
	}
	return "`" + v + "`"
}

func cmdLeaseList(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	path, err := c.ProjectPath("/leases")
	if err != nil {
		return err
	}
	res, err := leaseDo(cl, api.Request{Method: http.MethodGet, Path: path}, nil)
	if err != nil {
		return err
	}
	if v.Bool("json") {
		return c.printRawJSON(res.Raw)
	}
	val, err := jsonorder.Decode(res.Raw)
	if err != nil {
		return i18n.Errorf("cli.err.lease.bad_response", "reason", err)
	}
	body := asObject(val)
	loc, _ := serverTZ(c.Lang, body)
	items := objects(get(body, "leases", nil))
	if len(items) == 0 {
		c.Println(i18n.T(c.Lang, "cli.lease.list.none"))
		return nil
	}
	for _, it := range items {
		line := leaseLine(it, loc)
		if truthy(it, "expired") {
			line += "  " + i18n.T(c.Lang, "cli.lease.list.expired")
		}
		c.Println(line)
	}
	return nil
}

func cmdLeaseShow(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	path, err := c.leasePath(v, "")
	if err != nil {
		return err
	}
	res, err := leaseDo(cl, api.Request{Method: http.MethodGet, Path: path}, nil)
	if err != nil {
		return err
	}
	if v.Bool("json") {
		return c.printRawJSON(res.Raw)
	}
	val, err := jsonorder.Decode(res.Raw)
	if err != nil {
		return i18n.Errorf("cli.err.lease.bad_response", "reason", err)
	}
	body := asObject(val)
	loc, _ := serverTZ(c.Lang, body)
	lease := asObject(get(body, "lease", nil))
	if getStr(lease, "holder", "") == "" {
		c.Println(i18n.T(c.Lang, "cli.lease.show.free", "name", getStr(lease, "name", "")))
	} else {
		c.Println(leaseLine(lease, loc))
	}
	for _, e := range objects(get(body, "events", nil)) {
		note := getStr(e, "reason", "")
		if note == "" {
			note = getStr(e, "purpose", "")
		}
		fields := []string{leaseTime(getStr(e, "created_at", ""), loc), getStr(e, "action", ""), orDash(getStr(e, "user", "")), orDash(getStr(e, "machine_label", "")), note}
		if p := getStr(e, "prev_user", ""); p != "" {
			fields = append(fields, i18n.T(c.Lang, "cli.lease.show.prev", "user", p))
		}
		if truthy(e, "was_expired") {
			fields = append(fields, i18n.T(c.Lang, "cli.lease.list.expired"))
		}
		c.Println(strings.Join(fields, "  "))
	}
	return nil
}

func cmdLeaseAcquire(c *Ctx, v *Values) error {
	out, err := c.leaseSetExpiry(v, "", jsonorder.NewObject().Set("purpose", v.Str("purpose")))
	if err != nil {
		return err
	}
	c.Println(i18n.T(c.Lang, "cli.lease.acquired", "name", out.Name, "expires", out.expires(c.Lang)))
	c.Println("lease_token=" + out.Token) // スクリプトが読む行なので文面に混ぜない。extend と release の --token に渡す
	return nil
}

func cmdLeaseExtend(c *Ctx, v *Values) error {
	body := jsonorder.NewObject()
	if t := strings.TrimSpace(v.Str("token")); t != "" {
		body.Set("lease_token", t)
	}
	out, err := c.leaseSetExpiry(v, "/extend", body)
	if err != nil {
		return err
	}
	c.Println(i18n.T(c.Lang, "cli.lease.extended", "name", out.Name, "expires", out.expires(c.Lang)))
	return nil
}

// leaseWritten は取る・延ばすの応答。
type leaseWritten struct {
	leaseItem
	Token    string
	Timezone string
}

// expires は期限を list と同じ時間帯（サーバの timezone）で描く。
func (l *leaseWritten) expires(lang i18n.Lang) string {
	loc, _ := serverTZ(lang, jsonorder.NewObject().Set("timezone", l.Timezone))
	return leaseTime(l.ExpiresAt, loc)
}

// leaseSetExpiry は取る・延ばすの共通部分（期限を送り、今の占有を返す）。
func (c *Ctx) leaseSetExpiry(v *Values, suffix string, body *jsonorder.Object) (*leaseWritten, error) {
	cl, err := c.RequireAPI("")
	if err != nil {
		return nil, err
	}
	path, err := c.leasePath(v, suffix)
	if err != nil {
		return nil, err
	}
	ttl, err := c.leaseTTLSeconds(v)
	if err != nil {
		return nil, err
	}
	if ttl > 0 {
		body.Set("ttl_seconds", ttl)
	}
	var out struct {
		Lease    leaseItem `json:"lease"`
		Token    string    `json:"lease_token"`
		Timezone string    `json:"timezone"`
	}
	if _, err := leaseDo(cl, api.Request{Method: http.MethodPost, Path: path, Body: body.Set("machine_label", c.memLabel())}, &out); err != nil {
		return nil, err
	}
	return &leaseWritten{leaseItem: out.Lease, Token: out.Token, Timezone: out.Timezone}, nil
}

func cmdLeaseRelease(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	path, err := c.leasePath(v, "")
	if err != nil {
		return err
	}
	body := jsonorder.NewObject().Set("machine_label", c.memLabel())
	if t := strings.TrimSpace(v.Str("token")); t != "" {
		body.Set("lease_token", t)
	}
	if _, err := leaseDo(cl, api.Request{Method: http.MethodDelete, Path: path, Body: body}, nil); err != nil {
		return err
	}
	c.Println(i18n.T(c.Lang, "cli.lease.released", "name", strings.TrimSpace(v.Str("name"))))
	return nil
}

func cmdLeaseBreak(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	path, err := c.leasePath(v, "/break")
	if err != nil {
		return err
	}
	body := jsonorder.NewObject().Set("reason", v.Str("reason")).Set("machine_label", c.memLabel())
	var out struct {
		Holder       string `json:"holder"`
		NoticeFailed bool   `json:"notice_failed"`
	}
	if _, err := leaseDo(cl, api.Request{Method: http.MethodPost, Path: path, Body: body}, &out); err != nil {
		return err
	}
	c.Println(i18n.T(c.Lang, "cli.lease.broken", "name", strings.TrimSpace(v.Str("name"))))
	if out.NoticeFailed {
		c.Println(i18n.T(c.Lang, "cli.lease.notice_failed", "holder", orDash(out.Holder)))
	}
	return nil
}
