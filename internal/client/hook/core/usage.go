package core

// トークン消費のスナップショットをサーバへ送る hook（設計 §9-5「トークン計測」→「送信の経路」の経路 ②）。
//
//	PostToolUse（Copilot の postToolUse・Gemini の AfterTool）: MCP でイシューを変更したときだけ送る（issue_events を書くツール
//	    ＝ usage.ToolOps の鍵で、サーバ名が LOOPTRACK_MCP_SERVER（既定 "looptrack"）に合うもの。失敗した操作は送らない）。
//	Stop（AfterAgent・agentStop）: 前回の送信から LOOPTRACK_USAGE_THROTTLE_MIN 分（既定 10）未満なら送らない。
//	SessionEnd: 常に送る（区間の一覧つき）。
//
// 送信は子プロセスに切り離し、hook 自体はすぐ 0 で終わる（LOOPTRACK_USAGE_FOREGROUND=1 なら同じプロセスで送る）。
// 送れなかった分は looptrack の置き場（資格情報と同じ。internal/client/cred）の usage-spool/<プロジェクトの鍵>/ に置き、
// 同じプロジェクト（API の URL + slug）の次の起動でまとめて送る（7 日で捨てる）。payload はプロジェクトを持たないので、
// 置き場を分けないと、別のプロジェクトのセッションが再送したときにそのプロジェクトへ送ってしまう（イシューの操作は 404 で
// 捨てられ、スナップショットは別のプロジェクトの消費として記録される）。鍵の無い usage-spool/*.json（以前の版の置き場）は
// 送り先が分からないので再送せず、7 日で捨てる。ファイルの形は以前の hook（1.0.0 より前）と同じ。以前の実装と共有した
// 旧い置き場は読まない・移さない（spool は送り損ねの再送用で、失っても次の送信で累計が届く）。
// 再送には退避してからの経過秒（resend_delay_sec）を付ける。サーバはそれを引いた「最初に送ろうとした時刻」で
// 付与漏れの窓（10 分）に入るかを見るので、再送が窓を過ぎても元の操作に付く。
// 送信の失敗は usage-failure.json に残し（usagesnap.RecordFailure）、`looptrack issue summary` が利用者に見せる。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/client/usagesnap"
	"github.com/howashoji/looptrack/internal/hookio"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/usage"
)

// 拾うツールと送る op は usage.ToolOps（サーバが受け付ける op・未付与の検知で数える kind と同じ一覧から作る）。
// ここに一覧を書き写さない（書き写すと、サーバがイベントを書くのに hook が拾わない操作が黙って生まれる）。
var (
	opOf          = usage.ToolOps
	toolAlt       = toolAlternation(opOf)
	toolRe        = regexp.MustCompile(`^mcp_+(?P<server>.+?)_+(?P<tool>` + toolAlt + `)$`)
	copilotToolRe = regexp.MustCompile(`^(?:mcp[_-]+)?(?P<server>.+?)(?:__|[-_/.])(?P<tool>` + toolAlt + `)$`)
	// createdRe・startedRe は MCP の応答の文から ID を拾う判定用（表示しない）。サーバの応答の語なので訳さない。
	// 応答の言語は接続ごとに変わるので、日英の両方の言い方を受ける（片方だけだと、もう片方の利用者では黙って拾えない）
	createdRe = regexp.MustCompile(`(?:作成|Created): ([A-Z][A-Z0-9-]*-\p{Nd}+)`)
	startedRe = regexp.MustCompile(`(?:着手|Started): ([A-Z][A-Z0-9-]*-\p{Nd}+):`)
)

// toolAlternation はツール名の一覧を正規表現の選択（a|b|c。長い順・同じ長さは名前順）にする。
func toolAlternation(ops map[string]string) string {
	names := make([]string, 0, len(ops))
	for k := range ops {
		names = append(names, regexp.QuoteMeta(k))
	}
	sort.Slice(names, func(i, j int) bool {
		if len(names[i]) != len(names[j]) {
			return len(names[i]) > len(names[j])
		}
		return names[i] < names[j]
	})
	return strings.Join(names, "|")
}

// mcpDataKey はサーバが MCP の応答の _meta に載せる構造化の値の鍵（internal/server の mcpDataMeta と同じ）。
const mcpDataKey = "looptrack/data"

// structuredOf は応答の構造化の値（structuredContent・structured_content・_meta の looptrack/data）。無ければ nil。
func structuredOf(resp any) map[string]any {
	r, ok := resp.(map[string]any)
	if !ok {
		return nil
	}
	for _, k := range []string{"structuredContent", "structured_content"} {
		if x, ok := r[k].(map[string]any); ok && len(x) > 0 {
			return x
		}
	}
	if m, ok := r["_meta"].(map[string]any); ok {
		if x, ok := m[mcpDataKey].(map[string]any); ok && len(x) > 0 {
			return x
		}
	}
	return nil
}

// responseText は応答を文字列にする（文字列でなければ JSON。ID を探すのにだけ使う）。
func responseText(resp any) string {
	if text, ok := resp.(string); ok {
		return text
	}
	return jsonorder.Compact(toOrderedJSON(resp))
}

// issueFromResponse は、引数に ID を持たないツール（create_issue・next）の対象を応答から取る。取れなければ ""。
// next は In Progress にしたとき（action started）だけ。着手中の再掲（resumed）・試行（would_start）は
// issue_events を書かないので送らない。
func issueFromResponse(tool string, resp any) string {
	sc := structuredOf(resp)
	switch tool {
	case "create_issue":
		if truthy(sc["id"]) {
			return toStr(sc["id"])
		}
		if m := createdRe.FindStringSubmatch(responseText(resp)); m != nil {
			return m[1]
		}
	case "next":
		if sc != nil {
			if toStr(sc["action"]) != "started" {
				return ""
			}
			if it, ok := sc["issue"].(map[string]any); ok && truthy(it["id"]) {
				return toStr(it["id"])
			}
		}
		if m := startedRe.FindStringSubmatch(responseText(resp)); m != nil {
			return m[1]
		}
	}
	return ""
}

const (
	spoolKeepSec  = 7 * 24 * 3600
	usageJobFlag  = "--usage-job" // 切り離した子プロセスの印（利用者は使わない）
	usageSpoolDir = usagesnap.SpoolDirName
	usageLastDir  = "usage-last"
)

// usagePlan は送るかどうかの判断。
type usagePlan struct {
	Trigger   string `json:"trigger"`
	IssueID   string `json:"issue,omitempty"`
	Op        string `json:"op,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`
}

// usageJob は切り離した子プロセスに渡す仕事（標準入力の JSON）。
type usageJob struct {
	usagePlan
	Client         string `json:"client"`
	TranscriptPath string `json:"transcript_path"`
	SessionID      string `json:"session_id"`
	CWD            string `json:"cwd"`
}

func (c *Call) debug(format string, a ...any) {
	if c.getenv("LOOPTRACK_USAGE_DEBUG") != "" && c.Stderr != nil {
		fmt.Fprintf(c.Stderr, "usage_hook: "+format+"\n", a...)
	}
}

// setting は LOOPTRACK_<name>（空白だけのものは無いとみなす）。
func (c *Call) setting(name string) string { return c.Vars.Value(name) }

// mcpServerVar は looptrack の MCP のサーバ名を当てる正規表現を渡す環境変数の名前（知らせの文面に出す）。
const mcpServerVar = "LOOPTRACK_MCP_SERVER"

// mcpServerMatch は、MCP のサーバ名が LOOPTRACK_MCP_SERVER（Go の正規表現・RE2。既定 "looptrack"）に合うかを返す。
// 正規表現が読めないときは「合わない」を返し、変数名と誤りを 1 行にした知らせも返す（既定に戻さない。
// 誤った設定で別のサーバを当てないため）。知らせは MCP のツールの呼び出しのときだけ出す側が呼ぶ。
func (c *Call) mcpServerMatch(server string) (matched bool, notice string) {
	pat := c.setting("MCP_SERVER")
	if pat == "" {
		pat = "looptrack"
	}
	sre, err := regexp.Compile(pat)
	if err != nil {
		return false, i18n.T(c.lang(), "core.mcpserver.invalid", "name", mcpServerVar, "reason", err)
	}
	return sre.MatchString(server), ""
}

// stateDir は usage-spool・usage-last の置き場（資格情報と同じ looptrack の置き場。Windows は %APPDATA%\looptrack、
// 他は $XDG_CONFIG_HOME（無ければ ~/.config）/looptrack）。ホームが分からなければ ""（spool も間引きの記録もしない）。
func (c *Call) stateDir() string { return usagesnap.StateDir(*c.Vars) }

// client はトークンを読む AI。配線の --client、LOOPTRACK_USAGE_CLIENT、--agent（推測でないとき）、
// 入力と環境変数からの推測の順。
func (c *Call) client(ev hookio.Event) string {
	if v, ok := c.arg("--client"); ok && v != "" {
		return v
	}
	if v := c.getenv("LOOPTRACK_USAGE_CLIENT"); v != "" {
		return v
	}
	if !ev.AgentGuessed && ev.Agent != "" {
		return string(ev.Agent)
	}
	tp := rawString(ev.Raw, "transcript_path", "transcriptPath")
	_, snake := ev.Raw["session_id"]
	_, camel := ev.Raw["sessionId"]
	switch {
	case (camel && !snake) || strings.Contains(tp, "/.copilot/"):
		return usagesnap.ClientCopilot
	case strings.Contains(tp, "/.codex/") || c.getenv("CODEX_THREAD_ID") != "" || c.getenv("CODEX_SESSION_ID") != "":
		return usagesnap.ClientCodex
	case strings.Contains(tp, "/.gemini/"):
		return "gemini-cli"
	case c.getenv("CLAUDECODE") != "" || strings.Contains(tp, "/.claude/") || truthy(ev.Raw["session_id"]) || truthy(ev.Raw["sessionId"]):
		return usagesnap.ClientClaudeCode
	}
	return ""
}

// toolResponse は tool_response（Copilot の toolResult・tool_result）。
func toolResponse(ev hookio.Event) (any, bool) {
	for _, k := range []string{"tool_response", "toolResult", "tool_result"} {
		if v, ok := ev.Raw[k]; ok {
			return v, true
		}
	}
	return nil, false
}

// plan はフックの入力から送るかどうかを決める。送らないなら nil。
func (c *Call) plan(ev hookio.Event, client string) *usagePlan {
	p, _ := c.planNotice(ev, client)
	return p
}

// planNotice は plan と、LOOPTRACK_MCP_SERVER が読めなかったときの知らせ（無ければ ""）を返す。
// 知らせが出るのは MCP のツールの呼び出しのときだけ（ツール名が MCP の形でなければ、正規表現を読まない）。
func (c *Call) planNotice(ev hookio.Event, client string) (*usagePlan, string) {
	switch strings.ToLower(ev.RawName) {
	case "posttooluse", "aftertool":
		re := toolRe
		if client == usagesnap.ClientCopilot {
			re = copilotToolRe
		}
		m := re.FindStringSubmatch(rawString(ev.Raw, "tool_name", "toolName"))
		if m == nil {
			return nil, ""
		}
		server, tool := m[1], m[2]
		if ok, notice := c.mcpServerMatch(server); !ok {
			return nil, notice
		}
		resp, _ := toolResponse(ev)
		if r, ok := resp.(map[string]any); ok {
			rt := any("success")
			if x := r["resultType"]; truthy(x) {
				rt = x
			} else if x := r["result_type"]; truthy(x) {
				rt = x
			}
			if truthy(r["isError"]) || truthy(r["is_error"]) || toStr(rt) != "success" {
				return nil, ""
			}
		}
		issueID := ""
		if ev.Tool != nil && ev.Tool.Input != nil {
			tin := ev.Tool.Input
			v := tin["id"]
			if !truthy(v) {
				v = tin["issue"]
			}
			if truthy(v) {
				issueID = toStr(v)
			}
		}
		if tool == "create_issue" || tool == "next" {
			// 応答は「作成: <ID> <タイトル>（version n）」・「着手: <ID>: <元の状態> → In Progress（…）」（英語は Created: / Started:）。
			// 構造化の応答があればそれを優先する
			issueID = issueFromResponse(tool, resp)
		}
		if issueID == "" {
			return nil, ""
		}
		useID := ""
		if ev.Tool != nil {
			useID = ev.Tool.UseID
		}
		return &usagePlan{Trigger: "issue_op", IssueID: strings.ToUpper(issueID), Op: opOf[tool], ToolUseID: useID}, ""
	case "stop", "afteragent", "agentstop":
		return &usagePlan{Trigger: "stop"}, ""
	case "sessionend":
		return &usagePlan{Trigger: "session_end"}, ""
	}
	return nil, ""
}

// toOrderedJSON は encoding/json で解いた値を jsonorder の値にする（キーの順は入力の順を保てないので名前順。応答を
// JSON にした文字列は「作成: <ID>」を探すのにだけ使う）。
func toOrderedJSON(v any) any {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		o := jsonorder.NewObject()
		for _, k := range keys {
			o.Set(k, toOrderedJSON(x[k]))
		}
		return o
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = toOrderedJSON(e)
		}
		return out
	case float64:
		return jsonorder.Number(strconv.FormatFloat(x, 'g', -1, 64))
	}
	return v
}

// lastFile は Stop の間引きの記録（usage-last/<session_id の先頭 128 文字>）。
func (c *Call) lastFile(sid string) string {
	if sid == "" {
		sid = "unknown"
	}
	if rs := []rune(sid); len(rs) > 128 {
		sid = string(rs[:128])
	}
	d := c.stateDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, usageLastDir, sid)
}

// throttled は Stop の送信を間引くか（前回の送信から LOOPTRACK_USAGE_THROTTLE_MIN 分未満）。
func (c *Call) throttled(sid, trigger string) bool {
	if trigger != "stop" {
		return false
	}
	minutes := 10
	if v := c.setting("USAGE_THROTTLE_MIN"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && isDigits(v) {
			minutes = n
		}
	}
	if minutes <= 0 {
		return false
	}
	p := c.lastFile(sid)
	if p == "" {
		return false
	}
	fi, err := os.Stat(p)
	if err != nil {
		return false
	}
	return c.Now().Sub(fi.ModTime()) < time.Duration(minutes)*time.Minute
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func (c *Call) markSent(sid string) error {
	p := c.lastFile(sid)
	if p == "" {
		return i18n.Errorf("core.usage.err.no_last_dir")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(jsonorder.FormatFloat(float64(c.Now().UnixNano())/1e9)), 0o644)
}

// Usage は `looptrack hook usage`。出力は無い（送信は切り離した子プロセスで行う）。
// 例外は LOOPTRACK_MCP_SERVER が RE2 で読めないときの知らせ 1 行だけ（MCP のツールの呼び出しのときに限る）。
func Usage(ctx context.Context, c *Call, ev hookio.Event) (hookio.Result, error) {
	// サーバの URL かプロジェクト（LOOPTRACK_API_URL・LOOPTRACK_PROJECT）が無いときは、何もせず、失敗の記録も残さない。
	// これは意図した振る舞い: hook は利用者の全体の設定（~/.claude など）に配線されることがあり、looptrack を使っていない
	// リポジトリのセッションでも起動する。そこで送らないのは正しく、失敗として残すと、使っていないプロジェクトのたびに
	// 「送れていない」と出てしまう。送り先が決まらないので退避もしない（退避しても、どこへ再送すべきかが分からない）。
	// looptrack を使っているのに設定が無い場合は、同じ設定を読む SessionStart の要約（summary）も出ないので、そちらで気づける。
	if c.setting("USAGE") == "0" || c.apiBase() == "" {
		return hookio.Result{}, nil
	}
	if _, err := c.project(); err != nil {
		return hookio.Result{}, nil
	}
	client := c.client(ev)
	p, notice := c.planNotice(ev, client)
	if p == nil {
		// LOOPTRACK_MCP_SERVER が読めないときだけ知らせる（送らないことは変えない）
		return hookio.Result{SystemMessage: notice}, nil
	}
	sid := rawString(ev.Raw, "session_id", "sessionId")
	if c.throttled(sid, p.Trigger) {
		return hookio.Result{}, nil
	}
	cwd := rawString(ev.Raw, "cwd")
	if cwd == "" {
		cwd = c.Getwd()
	}
	job := usageJob{usagePlan: *p, Client: client, TranscriptPath: rawString(ev.Raw, "transcript_path", "transcriptPath"),
		SessionID: sid, CWD: cwd}
	if c.setting("USAGE_FOREGROUND") != "1" {
		b, _ := json.Marshal(job)
		if err := c.Spawn(b, c.Getwd()); err == nil {
			return hookio.Result{}, nil
		} else {
			c.debug("%s", i18n.T(c.lang(), "core.usage.debug.detach_failed", "reason", err))
		}
	}
	c.work(ctx, job)
	return hookio.Result{}, nil
}

// runUsageJob は切り離した子プロセスの本体（標準入力の usageJob を送る）。
func runUsageJob(ctx context.Context, c *Call, stdin io.Reader) {
	defer hookio.Recover(nil, nil)
	b, err := io.ReadAll(io.LimitReader(stdin, hookio.MaxInput))
	if err != nil {
		return
	}
	var job usageJob
	if json.Unmarshal(b, &job) != nil {
		return
	}
	c.work(ctx, job)
}

// work は会話記録を読んで送る（送れなければ spool）。
func (c *Call) work(ctx context.Context, job usageJob) {
	defer hookio.Recover(nil, func(p any, _ []byte) {
		c.debug("%s", i18n.T(c.lang(), "core.usage.debug.failed", "reason", fmt.Sprint(p)))
	})
	if job.Client == usagesnap.ClientCopilot {
		// OTel のスパンはまとめて書き出される。この操作を呼んだ応答の chat が書かれるのを待つ（切り離した後なので操作は待たせない）
		wait := 6
		if v := c.setting("USAGE_COPILOT_WAIT_SEC"); v != "" && isDigits(v) {
			wait, _ = strconv.Atoi(v)
		}
		c.Sleep(time.Duration(wait) * time.Second)
	}
	o := usagesnap.Options{WithSegments: job.Trigger != "issue_op", Env: *c.Vars, Now: c.Now}
	payload, err := usagesnap.Collect(job.Client, job.TranscriptPath, job.SessionID, job.CWD, o)
	if err != nil || payload == nil {
		if job.Client == usagesnap.ClientCopilot {
			// Copilot は OTel の置き場の外に書くと読めない（出力先が hook に渡らない）。探した結果と置き場を案内する
			c.debug("%s", i18n.T(c.lang(), "core.usage.debug.no_transcript_hint", "client", job.Client, "path", job.TranscriptPath,
				"hint", usagesnap.CopilotMissHint(c.lang(), job.SessionID, o)))
		} else {
			c.debug("%s", i18n.T(c.lang(), "core.usage.debug.no_transcript", "client", job.Client, "path", job.TranscriptPath))
		}
		return
	}
	payload.Set("trigger", job.Trigger)
	if job.IssueID != "" {
		payload.Set("issue", job.IssueID).Set("op", job.Op).Set("via", "mcp")
	}
	if job.ToolUseID != "" {
		payload.Set("tool_use_id", job.ToolUseID)
	}
	if err := c.resendSpool(); err != nil {
		// トークンが無いなど（以前の CLI はここで異常終了した）。今回の分も退避して、ログインの後の送信で再送する
		c.fail(err, 0)
		c.keep(payload)
		return
	}
	r, status, err := c.send(payload)
	switch {
	case err != nil:
		c.fail(err, 0)
		c.keep(payload)
		return
	case r == sendRetry:
		c.fail(nil, 0)
		c.keep(payload)
		return
	case r == sendRejected:
		c.fail(nil, status) // 再送しても直らないので捨てる（失敗の記録だけ残す）
	}
	// 送った・拒否された（捨てた）ときは Stop の間引きの起点にする（拒否を毎ターン送り直さない）
	sid, _ := payload.Get("session_id")
	s, _ := sid.(string)
	if err := c.markSent(s); err != nil {
		c.debug("%s", i18n.T(c.lang(), "core.usage.debug.failed", "reason", err))
	}
}

// fail は送信の失敗を usage-failure.json に残す（err は送る前の失敗。status は拒否されたときの HTTP の状態）。
// 記録は次に送信が成功したときに消える（「いま hook が送れていない」ことを示すもの。拒否されて捨てた操作そのものは、
// サーバの未付与の一覧（usage missing・summary）に残る）。
func (c *Call) fail(err error, status int) {
	reason := usagesnap.FailUnreachable
	var nt *api.NoTokenError
	switch {
	case errors.As(err, &nt):
		reason = usagesnap.FailNoToken
	case err != nil:
		reason = usagesnap.FailClient // 資格情報を読めないなど、送る前の失敗
	case status != 0:
		reason = usagesnap.FailRejected
	}
	usagesnap.RecordFailure(c.stateDir(), c.Now(), reason, status)
}

// keep は送れなかった payload を退避する。退避できなければその payload は失うので、失敗として残す。
func (c *Call) keep(payload any) {
	if err := c.spool(payload); err != nil {
		c.debug("%s", i18n.T(c.lang(), "core.usage.debug.failed", "reason", err))
		usagesnap.RecordFailure(c.stateDir(), c.Now(), usagesnap.FailSpool, 0)
	}
}

// sendResult は 1 件の送信の結果。
type sendResult int

const (
	sendOK       sendResult = iota // 送った（重複を含む）
	sendRejected                   // 4xx で拒否された（認証・入力の誤り。再送しても直らないので捨てる）
	sendRetry                      // 届かない・5xx・時間切れ（退避して後で再送する）
)

// resendDelayKey は再送の payload に付ける、退避してから再送するまでの経過秒（サーバの usageRequest.ResendDelaySec）。
const resendDelayKey = "resend_delay_sec"

// send は 1 件送る。トークンが無いなど、以前の CLI がここで異常終了した失敗のときは error（それ以上何もしない）。
// 拒否されたときは HTTP の状態も返す。
func (c *Call) send(payload any) (sendResult, int, error) {
	timeout := 5.0
	if v := c.setting("USAGE_TIMEOUT"); v != "" {
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			c.debug("%s", i18n.T(c.lang(), "core.usage.debug.send_failed", "reason", err))
			return sendRetry, 0, nil
		}
		timeout = f
	}
	slug, err := c.project()
	if err != nil {
		return sendRetry, 0, err
	}
	cl, err := api.NewWithTimeout(*c.Vars, time.Duration(timeout*float64(time.Second)))
	if err != nil {
		return sendRetry, 0, err
	}
	post := func(body any) (*api.Response, error) {
		return cl.Do(api.Request{Method: "POST", Path: "/projects/" + api.PathEscape(slug) + "/usage", Body: body})
	}
	res, err := post(payload)
	if err != nil && isUnknownResendDelay(err) {
		// resend_delay_sec を知らない古いサーバ（知らない項目を 400 で拒む）。付けずに送り直す
		// （そのサーバでは再送が窓を過ぎると付かないが、スナップショット自体は届く。以前の振る舞いと同じ）
		if o, ok := payload.(*jsonorder.Object); ok {
			o = o.Clone()
			o.Delete(resendDelayKey)
			res, err = post(o)
		}
	}
	if err != nil {
		var ae *api.Error
		var nt *api.NoTokenError
		switch {
		case errors.As(err, &nt):
			return sendRetry, 0, err
		case errors.As(err, &ae):
			c.debug("%s", i18n.T(c.lang(), "core.usage.debug.rejected", "status", ae.Status, "message", ae.Message))
			if ae.Status >= 400 && ae.Status < 500 {
				return sendRejected, ae.Status, nil
			}
			return sendRetry, 0, nil
		}
		c.debug("%s", i18n.T(c.lang(), "core.usage.debug.send_failed", "reason", err))
		return sendRetry, 0, nil
	}
	usagesnap.ClearFailure(c.stateDir())
	// 次の送信から作業名を付けるか（usage.send_prompts）
	usagesnap.RememberSendPrompts(res.Value, usagesnap.Options{Env: *c.Vars})
	return sendOK, 0, nil
}

// isUnknownResendDelay は、古いサーバが resend_delay_sec を知らない項目として拒んだか（400 invalid_json。
// 理由は Go の JSON の誤りの文 `json: unknown field "resend_delay_sec"` がそのまま入り、訳されない）。
func isUnknownResendDelay(err error) bool {
	var ae *api.Error
	return errors.As(err, &ae) && ae.Status == 400 && ae.Code == "invalid_json" && strings.Contains(ae.Message, `"`+resendDelayKey+`"`)
}

// spoolDir はこのプロジェクト（API の URL + slug）の、送れなかった payload の置き場（置き場かプロジェクトが分からなければ ""）。
func (c *Call) spoolDir() string {
	slug, err := c.project()
	if err != nil {
		return ""
	}
	return usagesnap.SpoolDir(c.stateDir(), c.apiBase(), slug)
}

// expireSpool は、ほかのプロジェクトの置き場と鍵の無い以前の置き場（usage-spool/*.json）から 7 日より古いものを捨てる
// （そのプロジェクトのセッションが来ないと再送も掃除もされず、以前の置き場は再送しないので、ここで捨てないと残り続ける）。
func (c *Call) expireSpool(now time.Time) {
	d := c.stateDir()
	if d == "" {
		return
	}
	root := filepath.Join(d, usageSpoolDir)
	legacy, _ := filepath.Glob(filepath.Join(root, "*.json"))
	keyed, _ := filepath.Glob(filepath.Join(root, "*", "*.json"))
	for _, p := range append(legacy, keyed...) {
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && now.Sub(fi.ModTime()).Seconds() > spoolKeepSec {
			_ = os.Remove(p)
		}
	}
}

// spool は送れなかった payload を置く（<秒>-<16 進 8 桁>.json。ASCII 以外をそのまま出す JSON）。
func (c *Call) spool(payload any) error {
	dir := c.spoolDir()
	if dir == "" {
		return i18n.Errorf("core.usage.err.no_spool_dir")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var rnd [4]byte
	_, _ = rand.Read(rnd[:])
	name := fmt.Sprintf("%d-%s.json", c.Now().Unix(), hex.EncodeToString(rnd[:]))
	return os.WriteFile(filepath.Join(dir, name), []byte(jsonorder.Compact(payload)), 0o644)
}

// resendSpool はこのプロジェクトの置き場の payload を古い順に送る（7 日より古いものは捨てる。繋がらなければそこでやめる）。
// ほかのプロジェクトの置き場と鍵の無い以前の置き場は送らない（7 日より古いものを捨てるだけ）。
func (c *Call) resendSpool() error {
	now := c.Now()
	c.expireSpool(now)
	dir := c.spoolDir()
	if dir == "" {
		return nil
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	sort.Strings(paths)
	for _, p := range paths {
		if strings.HasPrefix(filepath.Base(p), ".") {
			continue // glob は . で始まる名前に当たらない
		}
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		if now.Sub(fi.ModTime()).Seconds() > spoolKeepSec {
			_ = os.Remove(p)
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		payload, err := jsonorder.Decode(b)
		if err != nil {
			continue
		}
		if o, ok := payload.(*jsonorder.Object); ok {
			// 退避してから今までの経過秒（この PC の時計どうしの差なので、サーバとの時計のずれに左右されない）
			if at, ok := usagesnap.SpooledAt(p); ok {
				if d := int64(now.Sub(at) / time.Second); d > 0 {
					o.Set(resendDelayKey, min(d, int64(spoolKeepSec)))
				}
			}
		}
		r, status, err := c.send(payload)
		if err != nil {
			return err
		}
		if r == sendRetry {
			break // 繋がらないときは残りも無理
		}
		if r == sendRejected {
			c.fail(nil, status)
		}
		_ = os.Remove(p)
	}
	return nil
}
