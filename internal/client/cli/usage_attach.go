package cli

// トークン情報の付与（設計 §5-4 の経路 ①・③）と、usage attach・usage ledger add。
// 会話記録から累計を作るのは internal/client/usagesnap（以前の CLI と payload が完全に一致する）。

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/cred"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/client/textenc"
	"github.com/howashoji/looptrack/internal/client/usagesnap"
	"github.com/howashoji/looptrack/internal/i18n"
)

// attachState は usageAttach の結果。
type attachState int

const (
	attachNone   attachState = iota // 送るものが無い（AI の下でない・会話記録が無い・LOOPTRACK_USAGE=0）
	attachSent                      // 送った
	attachFailed                    // 送れなかった
)

// usageDefaultTimeout は付与の待ち時間の既定（秒。LOOPTRACK_USAGE_TIMEOUT）。
const usageDefaultTimeout = 2.0

// usageAttach は変更操作の直後に、その時点の会話の累計をサーバへ送る。
// どの AI の下でもない（人がターミナルから打った）ときや、会話記録が読めないときは何もしない（attachNone）。
// 失敗しても操作の結果と終了コードを変えない（attachFailed。LOOPTRACK_USAGE_DEBUG=1 で理由を標準エラーに出す）。
// quiet でなければ結果を 1 行出し、失敗は「エラー: トークン情報を送れませんでした: …」で止める（error を返す）。
// error はそのほか、トークンが無い・資格情報を読めないとき（以前の CLI と同じく操作の後でも止める）。
func (c *Ctx) usageAttach(issueID, op, trigger string, quiet bool) (*jsonorder.Object, attachState, error) {
	cl, err := c.Client()
	if err != nil {
		return nil, attachNone, err
	}
	if cl.BaseURL == "" || c.Env.Value("USAGE") == "0" {
		return nil, attachNone, nil
	}
	debug := c.Env.Value("USAGE_DEBUG") != ""
	fail := func(err error) (*jsonorder.Object, attachState, error) {
		if debug {
			fmt.Fprintln(c.Stderr, i18n.T(c.Lang, "cli.usage.debug.send_failed", "reason", err))
		}
		if !quiet {
			// Wrapf は使わない（包むと Message が api.Error を先に拾い、サーバの文面に置き換わる）
			return nil, attachFailed, i18n.Errorf("cli.err.usage_attach_failed", "reason", err)
		}
		return nil, attachFailed, nil
	}
	opts := usagesnap.Options{WithSegments: trigger != "issue_op", Env: c.Env}
	cwd, err := os.Getwd()
	if err != nil {
		return fail(err)
	}
	payload, err := usagesnap.Collect("", "", "", cwd, opts)
	if err != nil {
		return fail(err)
	}
	if payload == nil {
		if debug {
			fmt.Fprintln(c.Stderr, i18n.T(c.Lang, "cli.usage.debug.no_transcript"))
			if hint := c.copilotMissHint(); hint != "" {
				fmt.Fprintln(c.Stderr, "usage: "+hint)
			}
		}
		return nil, attachNone, nil
	}
	payload.Set("trigger", trigger).Set("via", "cli")
	if issueID != "" {
		payload.Set("issue", issueID)
	}
	if op != "" {
		payload.Set("op", op)
	}
	path, err := c.ProjectPath("/usage")
	if err != nil {
		return nil, attachNone, err
	}
	timeout := usageDefaultTimeout
	if raw, _ := c.Env.Setting("USAGE_TIMEOUT"); raw != "" {
		f, perr := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if perr != nil {
			return fail(fmt.Errorf("could not convert string to float: %s", quoteString(raw)))
		}
		timeout = f
	}
	ucl, err := api.NewWithTimeout(c.Env, seconds(timeout))
	if err != nil {
		return nil, attachNone, err
	}
	c.useServerLang(ucl)
	resp, err := ucl.Do(api.Request{Method: "POST", Path: path, Body: payload})
	if err != nil {
		var nt *api.NoTokenError
		var pe *cred.PermError
		var be *cred.BrokenError
		if errors.As(err, &nt) || errors.As(err, &pe) || errors.As(err, &be) {
			return nil, attachNone, err
		}
		return fail(err)
	}
	// 次の送信から作業名を付けるか（usage.send_prompts）
	usagesnap.RememberSendPrompts(resp.Value, usagesnap.Options{Env: c.Env})
	res, ok := resp.Value.(*jsonorder.Object)
	if !ok {
		return fail(fmt.Errorf("'%s' object has no attribute 'get'", typeName(resp.Value)))
	}
	if !quiet {
		what := i18n.T(c.Lang, "cli.usage.attached.new", "id", jsonorder.Str(get(res, "id", nil)))
		if truthy(res, "duplicate") {
			what = i18n.T(c.Lang, "cli.usage.attached.duplicate")
		}
		tok := payload.Object("tokens")
		total := sumValues(tok.Object("main")) + sumValues(tok.Object("sub"))
		client, _ := payload.Get("client")
		c.Println(i18n.T(c.Lang, "cli.usage.attached", "what", what, "client", jsonorder.Str(client), "total", comma(total)))
	}
	return payload, attachSent, nil
}

func typeName(v any) string {
	switch v.(type) {
	case string:
		return "str"
	case []any:
		return "list"
	case nil:
		return "NoneType"
	}
	return "object"
}

// afterChange は変更操作の後にトークン情報を付け（経路 ①）、付けられなかったときだけ
// サーバの指示（usage_notice。経路 ③）を標準エラーに出す。会話記録が無い（送るものが無い）ときは usage attach でも
// 付けられないため出さない。
func (c *Ctx) afterChange(res any, issueID, op string) error {
	_, st, err := c.usageAttach(issueID, op, "issue_op", true)
	if err != nil {
		return err
	}
	if st != attachFailed {
		return nil
	}
	if o, ok := res.(*jsonorder.Object); ok {
		if notice, _ := o.Get("usage_notice"); jsonorder.Truthy(notice) {
			fmt.Fprintln(c.Stderr, jsonorder.Str(notice))
		}
	}
	return nil
}

// cmdUsageAttach は usage attach <ID>（今の会話の累計を手動で付ける）。
func cmdUsageAttach(c *Ctx, v *Values) error {
	if _, err := c.usageAPI(); err != nil {
		return err
	}
	id := v.Str("id")
	if id == "" {
		return i18n.Errorf("cli.err.usage_attach_id")
	}
	_, st, err := c.usageAttach(id, "", "manual", false)
	if err != nil {
		return err
	}
	if st == attachNone {
		if hint := c.copilotMissHint(); hint != "" {
			return i18n.Errorf("cli.err.usage_attach_copilot", "hint", hint)
		}
		return i18n.Errorf("cli.err.usage_attach_no_transcript")
	}
	return nil
}

// copilotMissHint は Copilot CLI のシェルの中で OTel のスパンが見つからなかったときの案内。
// Copilot CLI の下でない・付与を止めている（LOOPTRACK_USAGE=0）ときは空。
func (c *Ctx) copilotMissHint() string {
	if c.Env.Value("USAGE") == "0" {
		return ""
	}
	cwd, _ := os.Getwd()
	o := usagesnap.Options{Env: c.Env}
	client, _, sid := usagesnap.Detect(cwd, o)
	if client != usagesnap.ClientCopilot {
		return ""
	}
	return usagesnap.CopilotMissHint(sid, o)
}

// cmdUsageLedgerAdd は usage ledger add（作ったレポートを台帳に 1 行登録する。追記のみ）。
func cmdUsageLedgerAdd(c *Ctx, v *Values) error {
	cl, err := c.usageAPI()
	if err != nil {
		return err
	}
	body := jsonorder.NewObject()
	if src := v.Str("from_report"); src != "" {
		rep, err := c.readReport(src)
		if err != nil {
			return i18n.Errorf("cli.err.from_report_unreadable", "reason", err)
		}
		for _, key := range []string{"from", "to", "data_end", "excluded_conversations", "total_tokens"} {
			if x, ok := rep.Get(key); ok && x != nil {
				body.Set(key, x)
			}
		}
		if req := rep.Object("request"); req != nil {
			if id, _ := req.Get("id"); jsonorder.Truthy(id) { // usage report --request で集計したとき
				body.Set("request_id", id)
			}
		}
		if p, _ := rep.Get("project"); jsonorder.Truthy(p) {
			slug, err := c.Project()
			if err != nil {
				return err
			}
			if s, ok := p.(string); !ok || s != slug {
				return i18n.Errorf("cli.err.from_report_other_project", "project", jsonorder.Str(p))
			}
		}
	}
	for _, kv := range [][2]string{{"from", v.Str("date_from")}, {"to", v.Str("date_to")}, {"data_end", v.Str("data_end")},
		{"note", v.Str("note")}, {"created_at", v.Str("created_at")}} {
		if kv[1] != "" {
			body.Set(kv[0], kv[1])
		}
	}
	if v.IsSet("excluded") {
		ex := []any{}
		for _, x := range strings.Split(v.Str("excluded"), ",") {
			if x = trimSpace(x); x != "" {
				ex = append(ex, x)
			}
		}
		body.Set("excluded_conversations", ex)
	}
	if v.IsSet("total_tokens") {
		body.Set("total_tokens", v.Int("total_tokens"))
	}
	if v.IsSet("request_id") {
		body.Set("request_id", v.Int("request_id"))
	}
	body.Set("name", v.Str("name"))
	if to, _ := body.Get("to"); !jsonorder.Truthy(to) {
		return i18n.Errorf("cli.err.ledger_period_required")
	}
	path, err := c.ProjectPath("/usage/ledger")
	if err != nil {
		return err
	}
	res, err := c.send(cl, "POST", path, body)
	if err != nil {
		return err
	}
	if v.Bool("json") {
		c.PrintJSON(res)
		return nil
	}
	msg, err := mustStr(res, "message")
	if err != nil {
		return err
	}
	c.Println(msg)
	return nil
}

// readReport は --from-report のファイル（- は標準入力）を JSON のオブジェクトとして読む。
// 読めないときの文面は以前の CLI（[Errno 2] No such file or directory: 'x'）に合わせる。
func (c *Ctx) readReport(src string) (*jsonorder.Object, error) {
	var raw []byte
	var err error
	if src == "-" {
		raw, err = io.ReadAll(c.Stdin)
	} else {
		p := src
		if p == "~" || strings.HasPrefix(p, "~/") || (runtime.GOOS == "windows" && strings.HasPrefix(p, `~\`)) {
			home := c.Env.Get("HOME")
			if runtime.GOOS == "windows" { // 以前の CLI と同じく、~ の展開に USERPROFILE を使う
				home = c.Env.Get("USERPROFILE")
			}
			if home != "" {
				p = home + p[1:]
			}
		}
		raw, err = os.ReadFile(p)
		var pe *os.PathError
		if errors.As(err, &pe) && errors.Is(err, fs.ErrNotExist) {
			// Windows の ERROR_FILE_NOT_FOUND も以前の CLI と同じ文面にそろえる（OS の文面は OS ごとに違う）
			return nil, fmt.Errorf("[Errno 2] No such file or directory: %s", quoteString(src))
		}
		if errors.As(err, &pe) {
			var errno syscall.Errno
			if errors.As(pe.Err, &errno) {
				msg := errno.Error()
				if msg != "" {
					msg = strings.ToUpper(msg[:1]) + msg[1:]
				}
				return nil, fmt.Errorf("[Errno %d] %s: %s", int(errno), msg, quoteString(src))
			}
		}
	}
	if err != nil {
		return nil, err
	}
	rep, err := jsonorder.DecodeObject(textenc.Decode(raw)) // PowerShell 5.1 の `>` は UTF-16LE で書く
	if err != nil {
		return nil, err
	}
	return rep, nil
}
