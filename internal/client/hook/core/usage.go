package core

// トークン消費のスナップショットをサーバへ送る hook（設計 §9-5「トークン計測」→「送信の経路」の経路 ②）。
//
//	PostToolUse（Copilot の postToolUse・Gemini の AfterTool）: MCP でイシューを変更したときだけ送る（create_issue・update_issue・
//	    add_comment・set_status で、サーバ名が LOOPTRACK_MCP_SERVER（既定 "looptrack"）に合うもの。失敗した操作は送らない）。
//	Stop（AfterAgent・agentStop）: 前回の送信から LOOPTRACK_USAGE_THROTTLE_MIN 分（既定 10）未満なら送らない。
//	SessionEnd: 常に送る（区間の一覧つき）。
//
// 送信は子プロセスに切り離し、hook 自体はすぐ 0 で終わる（LOOPTRACK_USAGE_FOREGROUND=1 なら同じプロセスで送る）。
// 送れなかった分は looptrack の置き場（資格情報と同じ。internal/client/cred）の usage-spool/ に置き、次の起動でまとめて送る
// （7 日で捨てる）。ファイルの形は以前の hook（1.0.0 より前）と同じ。以前の実装と共有した旧い置き場は
// 読まない・移さない（spool は送り損ねの再送用で、失っても次の送信で累計が届く）。

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
	"github.com/howashoji/looptrack/internal/client/cred"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/client/usagesnap"
	"github.com/howashoji/looptrack/internal/hookio"
	"github.com/howashoji/looptrack/internal/i18n"
)

var (
	toolRe        = regexp.MustCompile(`^mcp_+(?P<server>.+?)_+(?P<tool>create_issue|update_issue|add_comment|set_status)$`)
	copilotToolRe = regexp.MustCompile(`^(?:mcp[_-]+)?(?P<server>.+?)(?:__|[-_/.])(?P<tool>create_issue|update_issue|add_comment|set_status)$`)
	// createdRe は MCP の応答から ID を拾う判定用（表示しない）。サーバの応答の語なので訳さない
	createdRe = regexp.MustCompile(`作成: ([A-Z][A-Z0-9-]*-\p{Nd}+)`)
	opOf      = map[string]string{"create_issue": "create", "update_issue": "update", "add_comment": "comment", "set_status": "status"}
)

const (
	spoolKeepSec  = 7 * 24 * 3600
	usageJobFlag  = "--usage-job" // 切り離した子プロセスの印（利用者は使わない）
	usageSpoolDir = "usage-spool"
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

// stateDir は usage-spool・usage-last の置き場（資格情報と同じ looptrack の置き場。Windows は %APPDATA%\looptrack、
// 他は $XDG_CONFIG_HOME（無ければ ~/.config）/looptrack）。ホームが分からなければ ""（spool も間引きの記録もしない）。
func (c *Call) stateDir() string {
	p, err := cred.DefaultPaths(*c.Vars)
	if err != nil || p.Primary == "" {
		return ""
	}
	return filepath.Dir(p.Primary)
}

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
	switch strings.ToLower(ev.RawName) {
	case "posttooluse", "aftertool":
		re := toolRe
		if client == usagesnap.ClientCopilot {
			re = copilotToolRe
		}
		m := re.FindStringSubmatch(rawString(ev.Raw, "tool_name", "toolName"))
		if m == nil {
			return nil
		}
		server, tool := m[1], m[2]
		pat := c.setting("MCP_SERVER")
		if pat == "" {
			pat = "looptrack"
		}
		sre, err := regexp.Compile(pat)
		if err != nil || !sre.MatchString(server) {
			return nil
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
				return nil
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
		if tool == "create_issue" {
			// 応答は「作成: <ID> <タイトル>（version n）」。構造化の応答（id）があればそれを優先する
			text, isStr := resp.(string)
			if !isStr {
				text = jsonorder.Compact(toOrderedJSON(resp))
			}
			var sc map[string]any
			if r, ok := resp.(map[string]any); ok {
				if x, ok := r["structuredContent"].(map[string]any); ok && len(x) > 0 {
					sc = x
				} else if x, ok := r["structured_content"].(map[string]any); ok && len(x) > 0 {
					sc = x
				}
			}
			issueID = ""
			if truthy(sc["id"]) {
				issueID = toStr(sc["id"])
			} else if m2 := createdRe.FindStringSubmatch(text); m2 != nil {
				issueID = m2[1]
			}
		}
		if issueID == "" {
			return nil
		}
		useID := ""
		if ev.Tool != nil {
			useID = ev.Tool.UseID
		}
		return &usagePlan{Trigger: "issue_op", IssueID: strings.ToUpper(issueID), Op: opOf[tool], ToolUseID: useID}
	case "stop", "afteragent", "agentstop":
		return &usagePlan{Trigger: "stop"}
	case "sessionend":
		return &usagePlan{Trigger: "session_end"}
	}
	return nil
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
func Usage(ctx context.Context, c *Call, ev hookio.Event) (hookio.Result, error) {
	if c.setting("USAGE") == "0" || c.apiBase() == "" {
		return hookio.Result{}, nil
	}
	if _, err := c.project(); err != nil {
		return hookio.Result{}, nil // プロジェクトが分からない環境（LOOPTRACK_PROJECT なし）では何もしない
	}
	client := c.client(ev)
	p := c.plan(ev, client)
	if p == nil {
		return hookio.Result{}, nil
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
		return // トークンが無いなど（以前の CLI はここで異常終了した）
	}
	ok, err := c.send(payload)
	if err != nil {
		return
	}
	if ok {
		sid, _ := payload.Get("session_id")
		s, _ := sid.(string)
		if err := c.markSent(s); err != nil {
			c.debug("%s", i18n.T(c.lang(), "core.usage.debug.failed", "reason", err))
		}
		return
	}
	if err := c.spool(payload); err != nil {
		c.debug("%s", i18n.T(c.lang(), "core.usage.debug.failed", "reason", err))
	}
}

// send は 1 件送る。成功なら true。認証・入力の誤り（4xx）は再送しても直らないので捨てる（true）。
// トークンが無いなど、以前の CLI がここで異常終了した失敗のときは error（それ以上何もしない）。
func (c *Call) send(payload any) (bool, error) {
	timeout := 5.0
	if v := c.setting("USAGE_TIMEOUT"); v != "" {
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			c.debug("%s", i18n.T(c.lang(), "core.usage.debug.send_failed", "reason", err))
			return false, nil
		}
		timeout = f
	}
	slug, err := c.project()
	if err != nil {
		return false, err
	}
	cl, err := api.NewWithTimeout(*c.Vars, time.Duration(timeout*float64(time.Second)))
	if err != nil {
		return false, err
	}
	res, err := cl.Do(api.Request{Method: "POST", Path: "/projects/" + api.PathEscape(slug) + "/usage", Body: payload})
	if err != nil {
		var ae *api.Error
		var nt *api.NoTokenError
		switch {
		case errors.As(err, &nt):
			return false, err
		case errors.As(err, &ae):
			c.debug("%s", i18n.T(c.lang(), "core.usage.debug.rejected", "status", ae.Status, "message", ae.Message))
			return ae.Status >= 400 && ae.Status < 500, nil
		}
		c.debug("%s", i18n.T(c.lang(), "core.usage.debug.send_failed", "reason", err))
		return false, nil
	}
	// 次の送信から作業名を付けるか（usage.send_prompts）
	usagesnap.RememberSendPrompts(res.Value, usagesnap.Options{Env: *c.Vars})
	return true, nil
}

// spoolDir は送れなかった payload の置き場（置き場が分からなければ ""）。
func (c *Call) spoolDir() string {
	d := c.stateDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, usageSpoolDir)
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

// resendSpool は置いておいた payload を古い順に送る（7 日より古いものは捨てる。繋がらなければそこでやめる）。
func (c *Call) resendSpool() error {
	dir := c.spoolDir()
	if dir == "" {
		return nil
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	sort.Strings(paths)
	now := c.Now()
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
		ok, err := c.send(payload)
		if err != nil {
			return err
		}
		if !ok {
			break // 繋がらないときは残りも無理
		}
		_ = os.Remove(p)
	}
	return nil
}
