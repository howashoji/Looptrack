package cli

// 変更系のサブコマンド: new・comment・status・close・assign・next。
// 作業コピー（edit・push）は workcopy.go、トークン情報の付与（usage attach・変更の後の付与）と台帳の登録は usage_attach.go。
// 文面・送る本文のキーと順・誤りの扱いは以前の CLI（1.0.0 より前）と同じ。

import (
	"errors"
	"regexp"
	"strings"

	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/i18n"
)

// send は本文つきで API を呼び、応答をオブジェクトとして返す。
func (c *Ctx) send(cl *api.Client, method, path string, body any) (*jsonorder.Object, error) {
	res, err := cl.Do(api.Request{Method: method, Path: path, Body: body})
	if err != nil {
		return nil, err
	}
	return asObject(res.Value), nil
}

// mustStr は入れ子のキーをたどった値を表示用の文字列にする（途中のキーが無ければ must と同じ誤り）。
func mustStr(o *jsonorder.Object, keys ...string) (string, error) {
	var v any = o
	for _, k := range keys {
		x, err := must(asObject(v), k)
		if err != nil {
			return "", err
		}
		v = x
	}
	return jsonorder.Str(v), nil
}

// withAssignee は --assignee を指定したときだけ assignee を送る（古いサーバは未知のキーを拒否する）。
func withAssignee(body *jsonorder.Object, assignee string) *jsonorder.Object {
	if assignee != "" {
		body.Set("assignee", assignee)
	}
	return body
}

var reIDSep = regexp.MustCompile(`[,\s]+`)

// splitIDs はカンマか空白で区切る（空の要素は捨てる）。
func splitIDs(v string) []any {
	out := []any{}
	for _, x := range reIDSep.Split(v, -1) {
		if x != "" {
			out = append(out, x)
		}
	}
	return out
}

// splitComma はカンマで区切る（空なら空の配列。空の要素は残す）。
func splitComma(v string) []any {
	out := []any{}
	if v == "" {
		return out
	}
	for _, x := range strings.Split(v, ",") {
		out = append(out, x)
	}
	return out
}

// cmdNew はイシューを起票する。
func cmdNew(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	if err := validate("--type", v.Str("type"), types); err != nil {
		return err
	}
	if err := validate("--priority", v.Str("priority"), priorities); err != nil {
		return err
	}
	if err := validate("--status", v.Str("status"), statuses); err != nil {
		return err
	}
	path, err := c.ProjectPath("/issues")
	if err != nil {
		return err
	}
	body := jsonorder.NewObject().
		Set("title", v.Str("title")).Set("type", v.Str("type")).Set("status", v.Str("status")).Set("priority", v.Str("priority")).
		Set("labels", splitComma(v.Str("labels"))).Set("parent", v.Str("parent")).Set("blocked_by", splitIDs(v.Str("blocked_by"))).
		Set("traces", splitIDs(v.Str("traces"))).Set("refs", splitIDs(v.Str("refs"))).Set("body", v.Str("body")).
		Set("override_reason", v.Str("override"))
	res, err := c.send(cl, "POST", path, withAssignee(body, v.Str("assignee")))
	if err != nil {
		return err
	}
	id, err := mustStr(res, "issue", "id")
	if err != nil {
		return err
	}
	title, err := mustStr(res, "issue", "title")
	if err != nil {
		return err
	}
	c.Println(i18n.T(c.Lang, "cli.new.created", "id", id, "title", title))
	return c.afterChange(res, id, "create")
}

// cmdComment はコメントを足す。
func cmdComment(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	path, err := c.issuePath(v.Str("id"), "/comments")
	if err != nil {
		return err
	}
	res, err := c.send(cl, "POST", path, jsonorder.NewObject().Set("text", v.Str("text")))
	if err != nil {
		return err
	}
	msg, err := mustStr(res, "message")
	if err != nil {
		return err
	}
	c.Println(msg)
	id, err := mustStr(res, "issue", "id")
	if err != nil {
		return err
	}
	return c.afterChange(res, id, "comment")
}

// cmdStatus は状態を変える。
func cmdStatus(c *Ctx, v *Values) error {
	return c.changeStatus(v, v.Str("status"))
}

// cmdClose は状態を Done にする（cmdStatus と同じ経路）。
func cmdClose(c *Ctx, v *Values) error {
	return c.changeStatus(v, "Done")
}

func (c *Ctx) changeStatus(v *Values, status string) error {
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	if err := validate("status", status, statuses); err != nil {
		return err
	}
	id := v.Str("id")
	body := withAssignee(jsonorder.NewObject().Set("status", status).Set("comment", v.Str("comment")).
		Set("override_reason", v.Str("override")), v.Str("assignee"))
	path, err := c.issuePath(id, "/status")
	if err != nil {
		return err
	}
	res, err := c.send(cl, "POST", path, body)
	var ae *api.Error
	if err != nil && errors.As(err, &ae) && ae.Status == 422 && ae.Body.String("rule") == "usage_required_on_close" {
		// クローズ時のトークン情報の必須化（usage.require_on_close）。サーバの判定は操作の前、CLI の付与は
		// 操作の後なので、この会話でまだ 1 件も付いていないと拒否される。そのときだけ先に付けて 1 回だけやり直す
		// （拒否された操作は何も変えていない）。付けられなければ（会話記録が無い等）拒否のメッセージのまま終わる
		_, st, aerr := c.usageAttach(id, "", "manual", true)
		if aerr != nil {
			return aerr
		}
		if st == attachSent {
			res, err = c.send(cl, "POST", path, body)
		}
	}
	if err != nil {
		return err
	}
	msgs, err := must(res, "messages")
	if err != nil {
		return err
	}
	for _, line := range toList(msgs) {
		c.Println(jsonorder.Str(line))
	}
	issueID, err := mustStr(res, "issue", "id")
	if err != nil {
		return err
	}
	return c.afterChange(res, issueID, "status")
}

// toList は値を並びとして見る（配列はその要素、文字列は 1 文字ずつ、オブジェクトはキー）。
func toList(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case string:
		out := []any{}
		for _, r := range x {
			out = append(out, string(r))
		}
		return out
	case *jsonorder.Object:
		out := []any{}
		for _, k := range x.Keys() {
			out = append(out, k)
		}
		return out
	}
	return nil
}

// cmdAssign は担当を変える（login・me・- で解除。他の利用者の担当は --override "理由"）。
func cmdAssign(c *Ctx, v *Values) error {
	cl, err := c.Client()
	if err != nil {
		return err
	}
	if cl.BaseURL == "" {
		return i18n.Errorf("cli.err.assign_api_only")
	}
	path, err := c.issuePath(v.Str("id"), "/assign")
	if err != nil {
		return err
	}
	res, err := c.send(cl, "POST", path, jsonorder.NewObject().Set("assignee", v.Str("assignee")).Set("override_reason", v.Str("override")))
	if err != nil {
		return err
	}
	msg, err := mustStr(res, "message")
	if err != nil {
		return err
	}
	c.Println(msg)
	return nil
}

// cmdNext は次に着手するイシューを決めて In Progress にする（規則は DESIGN.md §5-5）。
func cmdNext(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("next")
	if err != nil {
		return err
	}
	ts := []any{}
	for _, t := range strings.Split(v.Str("type"), ",") {
		if t = trimSpace(t); t != "" {
			ts = append(ts, t)
		}
	}
	for _, t := range ts {
		if err := validate("type", t.(string), types); err != nil {
			return err
		}
	}
	path, err := c.ProjectPath("/next")
	if err != nil {
		return err
	}
	body := jsonorder.NewObject().Set("dry_run", v.Bool("dry_run")).Set("comment", v.Str("comment")).
		Set("override_reason", v.Str("override")).Set("types", ts)
	res, err := c.send(cl, "POST", path, withAssignee(body, v.Str("assignee")))
	if err != nil {
		return err
	}
	if v.Bool("json") {
		c.PrintJSON(res)
	} else {
		text, err := mustStr(res, "text")
		if err != nil {
			return err
		}
		c.Println(text)
	}
	if a, _ := res.Get("action"); a == "started" {
		id, err := mustStr(res, "issue", "id")
		if err != nil {
			return err
		}
		return c.afterChange(res, id, "status")
	}
	return nil
}

// isSpaceRune は空白文字の類か（Unicode。1 文字ずつ見る）。
func isSpaceRune(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\v', '\f', '\r', 0x1c, 0x1d, 0x1e, 0x1f, 0x85, 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// trimSpace は str.strip()。
func trimSpace(s string) string { return strings.TrimFunc(s, isSpaceRune) }

// splitFields は str.split()（空白の並びで区切り、空の要素を捨てる）。
func splitFields(s string) []string { return strings.FieldsFunc(s, isSpaceRune) }
