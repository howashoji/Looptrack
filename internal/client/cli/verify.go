package cli

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/client/verify"
	"github.com/howashoji/looptrack/internal/i18n"
)

// Exit は「エラー: …」を出さずに、その終了コードで終える（出力は済ませてある）。Main が Code を返す。
type Exit struct{ Code int }

func (e *Exit) Error() string { return fmt.Sprintf("exit %d", e.Code) }

// cmdVerify は受け入れ条件の検証コマンドを実行して記録する（DESIGN §5-8-2）。
// 終了コード: 全成功 0・失敗あり 1・節なし 2（何も記録しない）・記録の送信に失敗 1（手元の結果は表示する）。
func cmdVerify(c *Ctx, v *Values) error {
	cl, err := c.Client()
	if err != nil {
		return err
	}
	if cl.BaseURL == "" {
		fmt.Fprintln(c.Stderr, i18n.T(c.Lang, "cli.err.verify_api_only",
			"url_env", env.Name(env.APIURL), "project_env", env.Name(env.Project)))
		return &Exit{2}
	}
	issueID := v.Str("id")
	path, err := c.verifyPath(issueID)
	if err != nil {
		return err
	}
	plan, err := verify.Fetch(cl, path)
	if err != nil {
		return err
	}
	if id := plan.String("id"); id != "" && id != issueID {
		issueID = id
		if path, err = c.verifyPath(issueID); err != nil {
			return err
		}
	}
	commands := verify.Commands(plan)
	if len(commands) == 0 {
		if v.Bool("json") {
			c.PrintJSON(plan)
		} else if msg := plan.String("message"); msg != "" {
			c.Println(msg)
		} else {
			c.Println(i18n.T(c.Lang, "cli.verify.no_commands", "id", issueID))
		}
		return &Exit{2}
	}
	if v.Bool("list") || v.Bool("last") {
		switch {
		case v.Bool("json"):
			c.PrintJSON(plan)
		case v.Bool("list"):
			x, _ := plan.Get("text")
			c.Println(jsonorder.Str(x))
		default:
			c.printVerifyLast(plan)
		}
		return nil
	}
	timeout, total := v.Float("timeout"), v.Float("total_timeout")
	if !(timeout > 0) || !(total > 0) {
		return i18n.Errorf("cli.err.verify_timeout")
	}

	// 受け入れ条件の節が更新されたのに検証コマンドの節が起票時のままなら、走らせる前に注記を出す（§5-8-4）
	if x, _ := plan.Get("section_drift"); jsonorder.Truthy(x) && !v.Bool("json") {
		c.Println(i18n.T(c.Lang, "domain.verify.section_drift", "id", issueID, "command", "looptrack issue verify "+issueID))
	}

	dir := verify.Root(context.Background(), c.Env.Get("CLAUDE_PROJECT_DIR"))
	sha := plan.String("body_sha256")
	asJSON := v.Bool("json")
	if !asJSON {
		c.Println(i18n.T(c.Lang, "cli.verify.running", "id", issueID, "count", len(commands), "dir", dir, "sha", cutRunes(sha, 8)))
	}
	opts := verify.Options{
		Dir: dir, Env: verify.Env(os.Environ(), issueID),
		Timeout: seconds(timeout), TotalTimeout: seconds(total),
	}
	if !asJSON {
		opts.Before = func(i, n int, command string) { c.Printf("[%d/%d] $ %s\n", i, n, command) }
		opts.After = func(i, n int, r verify.Result) {
			c.Println(verify.Line(c.Lang, i, n, r))
			if r.Status == verify.StatusFail || r.Status == verify.StatusTimeout {
				if t := verify.IndentTail(r.OutputTail, verify.ShowLines); t != "" {
					c.Println(t)
				}
			}
		}
	}
	results := verify.RunAll(commands, opts)
	passed, failed := verify.Count(results)
	summary := i18n.T(c.Lang, "cli.verify.passed", "passed", passed, "total", len(results))
	if failed > 0 {
		summary += i18n.T(c.Lang, "cli.verify.failed", "failed", failed)
	}
	// 結果キャッシュの印（(cached)）があったら手元にも注記を出す（記録にも残る）
	cached := 0
	for _, r := range results {
		if r.Cached {
			cached++
		}
	}
	if cached > 0 && !asJSON {
		c.Println(i18n.T(c.Lang, "cli.verify.cached_note", "count", cached))
	}

	var res any
	var sendErr string
	sent := false
	cachedDropped := false
	body := verify.Body(sha, results, dir)
	r, err := verify.Send(cl, path, body)
	// cached の欄を知らない版のサーバは本文を丸ごと拒む。注記だけを捨てて、検証の記録そのものは残す
	// （記録が残らない方が害が大きい。注記が落ちたことは下で手元に出す）。
	if err != nil && unknownFieldRejected(err) && verify.StripCached(body) {
		if r2, err2 := verify.Send(cl, path, body); err2 == nil {
			r, err, cachedDropped = r2, nil, true
		}
	}
	if err != nil {
		var ae *api.Error
		var ce *api.ConnError
		switch {
		case errors.As(err, &ae):
			sendErr = ae.Text(c.Lang)
			if ae.Status == 401 {
				sendErr = i18n.T(c.Lang, "cli.err.unauthorized", "message", sendErr, "relogin", api.Relogin(c.Lang))
			}
		case errors.As(err, &ce):
			sendErr = i18n.Text(c.Lang, ce)
		default:
			return err
		}
	} else {
		res, sent = r, true
	}
	if sent && cachedDropped && !asJSON {
		c.Println(i18n.T(c.Lang, "cli.verify.cached_not_recorded"))
	}
	// 注記を落としたことは記録の側にも残す。残さないと、後からコメントを読む人には
	// 「キャッシュが無かった」のか「サーバが古くて落ちた」のか区別が付かない。
	// 古いサーバも知っている経路（コメントの追加）で 1 行足す。失敗しても検証の記録は済んでいるので、手元に出すだけにする。
	if sent && cachedDropped {
		if err := c.recordCachedDropped(cl, issueID, cached); err != nil {
			fmt.Fprintln(c.Stderr, i18n.T(c.Lang, "cli.verify.cached_dropped_comment_failed", "reason", sendErrText(c, err)))
		}
	}
	if asJSON {
		var errv any
		if !sent {
			errv = sendErr
		}
		c.PrintJSON(jsonorder.NewObject().Set("id", issueID).Set("body_sha256", sha).Set("ok", failed == 0).
			Set("passed", passed).Set("failed", failed).Set("results", verify.ResultsJSON(results)).
			Set("recorded", sent).Set("cached_not_recorded", cachedDropped).Set("response", res).Set("error", errv))
	} else {
		var ms int64
		for _, r := range results {
			ms += r.DurationMS
		}
		c.Println(i18n.T(c.Lang, "cli.verify.result", "summary", summary, "seconds", fmt.Sprintf("%.1f", float64(ms)/1000)))
	}
	if !sent {
		fmt.Fprintln(c.Stderr, i18n.T(c.Lang, "cli.err.prefix", "message", i18n.T(c.Lang, "cli.err.verify_send", "reason", sendErr)))
		return &Exit{1}
	}
	if !asJSON {
		msg := ""
		if o, ok := res.(*jsonorder.Object); ok {
			msg = o.String("message")
		}
		if msg == "" {
			msg = i18n.T(c.Lang, "cli.verify.recorded", "id", issueID)
		}
		c.Println(msg)
	}
	if err := c.afterChange(res, issueID, "verify"); err != nil {
		return err
	}
	if failed > 0 {
		return &Exit{1}
	}
	return nil
}

// recordCachedDropped は、結果キャッシュの注記を外して記録したことをイシューのコメントに 1 行残す。
func (c *Ctx) recordCachedDropped(cl *api.Client, id string, count int) error {
	path, err := c.issuePath(id, "/comments")
	if err != nil {
		return err
	}
	_, err = c.send(cl, http.MethodPost, path,
		jsonorder.NewObject().Set("text", i18n.T(c.Lang, "cli.verify.cached_dropped_comment", "count", count)))
	return err
}

// sendErrText は送信の誤りを利用者向けの文面にする。
func sendErrText(c *Ctx, err error) string {
	var ae *api.Error
	var ce *api.ConnError
	switch {
	case errors.As(err, &ae):
		return ae.Text(c.Lang)
	case errors.As(err, &ce):
		return i18n.Text(c.Lang, ce)
	}
	return err.Error()
}

// unknownFieldRejected は「本文に知らない欄がある」としてサーバが拒んだ応答か
// （サーバは JSON を解釈できない要求をすべて 400 invalid_json にする）。
func unknownFieldRejected(err error) bool {
	var ae *api.Error
	return errors.As(err, &ae) && ae.Status == http.StatusBadRequest && ae.Code == "invalid_json"
}

// verifyPath は /issues/<id>/verify?project=<slug>。
func (c *Ctx) verifyPath(id string) (string, error) {
	slug, err := c.Project()
	if err != nil {
		return "", err
	}
	return "/issues/" + api.PathEscape(id) + "/verify?" + url.Values{"project": {slug}}.Encode(), nil
}

// printVerifyLast は直近の verify の記録を 1 行で出す。
func (c *Ctx) printVerifyLast(plan *jsonorder.Object) {
	loc, _ := serverTZ(c.Lang, plan) // 時刻は応答の timezone（サーバのローカル時刻）で描く
	last := plan.Object("last")
	if last == nil || len(last.Keys()) == 0 {
		c.Println(i18n.T(c.Lang, "cli.verify.last.none"))
		return
	}
	num := func(k string) int64 { x, _ := last.Get(k); n, _ := jsonorder.Int(x); return n }
	passed, failed := num("passed"), num("failed")
	head := i18n.T(c.Lang, "cli.verify.passed", "passed", passed, "total", passed+failed)
	if failed != 0 {
		head += i18n.T(c.Lang, "cli.verify.failed", "failed", failed)
	}
	state := i18n.T(c.Lang, "cli.verify.last.stale")
	if x, _ := plan.Get("current"); jsonorder.Truthy(x) {
		state = i18n.T(c.Lang, "cli.verify.last.current")
	}
	// 直近の verify が MCP の自己申告（report_verify）なら印を足す
	if x, _ := last.Get("self_reported"); jsonorder.Truthy(x) || last.String("via") == "mcp" {
		state += i18n.T(c.Lang, "cli.sep.list") + i18n.T(c.Lang, "cli.verify.self_reported")
	}
	// 結果キャッシュの印があった記録なら注記を足す
	if x, _ := last.Get("cached"); jsonorder.Truthy(x) {
		state += i18n.T(c.Lang, "cli.sep.list") + i18n.T(c.Lang, "cli.verify.cached")
	}
	orDash := func(k string) string {
		if x, _ := last.Get(k); jsonorder.Truthy(x) {
			return jsonorder.Str(x)
		}
		return "-"
	}
	c.Println(i18n.T(c.Lang, "cli.verify.last", "time", localTimeIn(last.String("at"), loc), "head", head, "state", state,
		"host", orDash("host"), "workspace", orDash("workspace")))
	rv, _ := last.Get("results")
	list, _ := rv.([]any)
	for i, x := range list {
		r := verify.ResultFromJSON(x)
		c.Println(verify.Line(c.Lang, i+1, len(list), r))
		if r.Status != verify.StatusSkipped {
			if t := verify.IndentTail(r.OutputTail, math.MaxInt); t != "" {
				c.Println(t)
			}
		}
	}
}

// seconds は秒（小数可）を Duration にする。
func seconds(s float64) time.Duration {
	if s >= math.MaxInt64/float64(time.Second) {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(s * float64(time.Second))
}

func cutRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
