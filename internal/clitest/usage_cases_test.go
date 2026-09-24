package clitest

import (
	"strconv"
	"strings"
)

// 会話記録ありのトークン付与。合成の会話記録を HOME に置き、AI のセッションの
// 環境変数を付けて、変更操作の後の付与（after_change・trigger issue_op）・usage attach（manual・区間つき）・
// クローズ時の必須化（422 → 付与 → やり直し）・送信の失敗（usage_notice を標準エラーへ）を確かめる。
// 応答に send_prompts を入れない（覚えた値の置き場は以前の CLI と違う。そこはサーバとの突き合わせで確かめる）。

const (
	ccSession = "sess-golden-1"
	ccPath    = "home/.claude/projects/-work-demo/" + ccSession + ".jsonl"
	cxThread  = "0199aaaa-bbbb-cccc-dddd-eeeeffff0001"
	cxPath    = "home/.codex/sessions/2026/09/18/rollout-2026-09-18T10-00-00-" + cxThread + ".jsonl"
	pUsage    = pProj + "/usage"
)

// ccTranscript は Claude Code の会話記録（指示 2 件・応答 3 件・同じ message.id の 2 行・サブエージェントなし）。
var ccTranscript = strings.Join([]string{
	`{"type":"user","timestamp":"2024-05-02T01:00:00.000Z","gitBranch":"main","version":"2.1.271","cwd":"/work/demo","origin":{"kind":"human"},"message":{"role":"user","content":"ログイン画面の不具合を直して"}}`,
	`{"type":"assistant","timestamp":"2024-05-02T01:00:10.000Z","gitBranch":"main","version":"2.1.271","message":{"id":"m1","model":"claude-opus-5","usage":{"input_tokens":10,"cache_creation_input_tokens":100,"cache_read_input_tokens":1000,"output_tokens":20}}}`,
	`{"type":"assistant","timestamp":"2024-05-02T01:00:12.000Z","gitBranch":"main","version":"2.1.271","message":{"id":"m1","model":"claude-opus-5","usage":{"input_tokens":10,"cache_creation_input_tokens":100,"cache_read_input_tokens":1000,"output_tokens":50}}}`,
	`{"type":"user","timestamp":"2024-05-02T01:05:00.000Z","gitBranch":"fix/login","version":"2.1.271","origin":{"kind":"human"},"message":{"role":"user","content":"テストも足して"}}`,
	`{"type":"assistant","timestamp":"2024-05-02T01:05:30.000Z","gitBranch":"fix/login","version":"2.1.271","message":{"id":"m2","model":"claude-opus-5","usage":{"input_tokens":5,"cache_creation_input_tokens":40,"cache_read_input_tokens":1200,"output_tokens":80}}}`,
	`{"type":"assistant","timestamp":"2024-05-02T01:06:00.000Z","gitBranch":"fix/login","version":"2.1.271","message":{"id":"m3","model":"claude-sonnet-5","usage":{"input_tokens":3,"cache_creation_input_tokens":0,"cache_read_input_tokens":900,"output_tokens":12}}}`,
}, "\n") + "\n"

// cxTranscript は Codex の会話記録（旧形式の user_message・token_count の累計）。
var cxTranscript = strings.Join([]string{
	`{"timestamp":"2024-05-02T01:00:00.000Z","type":"session_meta","payload":{"id":"` + cxThread + `","cwd":"/work/demo","cli_version":"0.140.0","originator":"codex_cli_rs"}}`,
	`{"timestamp":"2024-05-02T01:00:01.000Z","type":"event_msg","payload":{"type":"user_message","message":"直して"}}`,
	`{"timestamp":"2024-05-02T01:00:05.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[]}}`,
	`{"timestamp":"2024-05-02T01:00:05.500Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1000,"cached_input_tokens":600,"output_tokens":80,"reasoning_output_tokens":30,"total_tokens":1080}}}}`,
	`{"timestamp":"2024-05-02T01:03:00.000Z","type":"event_msg","payload":{"type":"user_message","message":"ありがとう"}}`,
	`{"timestamp":"2024-05-02T01:03:02.500Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1500,"cached_input_tokens":900,"output_tokens":100,"reasoning_output_tokens":40,"total_tokens":1600}}}}`,
}, "\n") + "\n"

// Copilot CLI: シェルに COPILOT_CLI=1 と COPILOT_AGENT_SESSION_ID が渡り、トークンは OpenTelemetry のファイル出力から読む。
// 1 行の形は Copilot CLI 1.0.86 の実物: chat は cache_read・cache_write を持ち、根（invoke_agent）は input・output だけ。
const (
	cpSession = "5f0c1d2e-3a4b-4c5d-8e6f-7a8b9c0d1e2f"
	cpPath    = "home/.copilot/otel/copilot-otel.jsonl"
)

func cpSpan(span, parent, op string, sec int, attrs string) string {
	par := "null"
	if parent != "" {
		par = `"` + parent + `"`
	}
	return `{"type":"span","traceId":"t1","spanId":"` + span + `","parentSpanId":` + par + `,"name":"` + op + ` x","kind":0,` +
		`"startTime":[` + strconv.Itoa(sec) + `,0],"endTime":[` + strconv.Itoa(sec+2) + `,500000000],` +
		`"attributes":{"gen_ai.operation.name":"` + op + `","gen_ai.conversation.id":"` + cpSession + `",` + attrs + `},` +
		`"resource":{"attributes":{"service.name":"github-copilot","service.version":"1.0.86"}}}`
}

var cpOtel = strings.Join([]string{
	cpSpan("c1", "a1", "chat", 1714611601, `"gen_ai.response.model":"gpt-5.6-luna","gen_ai.usage.input_tokens":120000,"gen_ai.usage.cache_read.input_tokens":100000,"gen_ai.usage.cache_write.input_tokens":19990,"gen_ai.usage.output_tokens":500,"gen_ai.usage.reasoning.output_tokens":7`),
	cpSpan("c2", "a1", "chat", 1714611605, `"gen_ai.response.model":"gpt-5.6-luna","gen_ai.usage.input_tokens":123000,"gen_ai.usage.cache_read.input_tokens":115000,"gen_ai.usage.cache_write.input_tokens":7985,"gen_ai.usage.output_tokens":450`),
	cpSpan("a1", "", "invoke_agent", 1714611600, `"gen_ai.usage.input_tokens":243000,"gen_ai.usage.output_tokens":950,"github.copilot.turn_count":2`),
}, "\n") + "\n"

const (
	usageAttached  = `{"id":12,"duplicate":false,"message":"トークン情報を付与しました"}`
	usageDuplicate = `{"id":12,"duplicate":true}`
	usageNotice    = "トークン情報が未付与です。looptrack issue usage attach DEMO-0001 を実行してください"
)

func usageCases() []Case {
	cc := map[string]string{"CLAUDE_CODE_SESSION_ID": ccSession}
	ccSeed := []Seed{{Path: ccPath, Content: ccTranscript}}
	cp := map[string]string{"COPILOT_CLI": "1", "COPILOT_AGENT_SESSION_ID": cpSession}
	cpSeed := []Seed{{Path: cpPath, Content: cpOtel}}
	withEnv := func(extra map[string]string) map[string]string {
		m := map[string]string{"CLAUDE_CODE_SESSION_ID": ccSession}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	commentNotice := strings.Replace(commentRes, `"seq":2`, `"seq":2,"usage_notice":"`+usageNotice+`"`, 1)
	return []Case{
		// ---- usage attach（manual。区間 segments を付ける）
		{Name: "usage-attach/claude-code", Args: []string{"usage", "attach", "DEMO-0001"}, Env: cc, Seeds: ccSeed,
			Routes: []Route{r("POST", pUsage, created(usageAttached))}},
		{Name: "usage-attach/duplicate", Args: []string{"usage", "attach", "DEMO-0001"}, Env: cc, Seeds: ccSeed,
			Routes: []Route{r("POST", pUsage, ok(usageDuplicate))}},
		{Name: "usage-attach/codex", Args: []string{"usage", "attach", "DEMO-0002"}, Env: map[string]string{"CODEX_THREAD_ID": cxThread},
			Seeds: []Seed{{Path: cxPath, Content: cxTranscript}}, Routes: []Route{r("POST", pUsage, created(usageAttached))}},
		{Name: "usage-attach/post-fails", Args: []string{"usage", "attach", "DEMO-0001"}, Env: cc, Seeds: ccSeed,
			Routes: []Route{r("POST", pUsage, ErrorResponse(500, "internal", "内部エラー"))}},
		{Name: "usage-attach/down", Args: []string{"usage", "attach", "DEMO-0001"}, Env: cc, Seeds: ccSeed,
			Routes: []Route{r("POST", pUsage, Response{Drop: true})}},
		{Name: "usage-attach/disabled", Args: []string{"usage", "attach", "DEMO-0001"}, Env: withEnv(map[string]string{"LOOPTRACK_USAGE": "0"}), Seeds: ccSeed},
		{Name: "usage-attach/no-id", Args: []string{"usage", "attach"}, Env: cc, Seeds: ccSeed},
		// Copilot CLI のシェル: OTel のファイル出力から作って付ける。ファイルが無ければ付けない（会話記録が見つからない）
		{Name: "usage-attach/copilot", Args: []string{"usage", "attach", "DEMO-0001"}, Env: cp, Seeds: cpSeed,
			Routes: []Route{r("POST", pUsage, created(usageAttached))}},
		{Name: "usage-attach/copilot-no-otel", Args: []string{"usage", "attach", "DEMO-0001"}, Env: cp},
		// 既定の置き場（~/.copilot/otel/）にファイルはあるが、このセッションのスパンが無い（出力先を別の場所にした会話など）。
		// Copilot CLI は出力先をシェルにも hook にも渡さないので、$COPILOT_HOME/otel/ の下に書かせるよう案内する
		{Name: "usage-attach/copilot-other-session", Args: []string{"usage", "attach", "DEMO-0001"}, Seeds: cpSeed,
			Env: map[string]string{"COPILOT_CLI": "1", "COPILOT_AGENT_SESSION_ID": "0f0f0f0f-0000-4000-8000-000000000001"}},

		// ---- 変更操作の後の付与（trigger issue_op・区間なし）
		{Name: "new/with-transcript", Args: []string{"new", "最小の起票"}, Env: cc, Seeds: ccSeed,
			Routes: []Route{r("POST", pIssues, created(createRes)), r("POST", pUsage, created(usageAttached))}},
		{Name: "comment/with-transcript", Args: []string{"comment", "DEMO-0001", "原因が分かった。"}, Env: cc, Seeds: ccSeed,
			Routes: []Route{r("POST", pIssue1+"/comments", created(commentRes)), r("POST", pUsage, created(usageAttached))}},
		{Name: "comment/copilot-otel", Args: []string{"comment", "DEMO-0001", "原因が分かった。"}, Env: cp, Seeds: cpSeed,
			Routes: []Route{r("POST", pIssue1+"/comments", created(commentRes)), r("POST", pUsage, created(usageAttached))}},
		{Name: "status/with-transcript", Args: []string{"status", "DEMO-0002", "In Progress"}, Env: cc, Seeds: ccSeed,
			Routes: []Route{r("POST", pIssue2+"/status", ok(statusRes)), r("POST", pUsage, created(usageAttached))}},
		{Name: "next/with-transcript", Args: []string{"next"}, Env: cc, Seeds: ccSeed,
			Routes: []Route{r("POST", pProj+"/next", ok(nextStarted)), r("POST", pUsage, created(usageAttached))}},
		{Name: "next-dry-run/with-transcript", Args: []string{"next", "--dry-run"}, Env: cc, Seeds: ccSeed, // 着手しないので付与しない
			Routes: []Route{r("POST", pProj+"/next", ok(nextDry))}},
		{Name: "push/with-transcript", Args: []string{"push", "DEMO-0001"}, Env: cc, Seeds: append(append([]Seed(nil), ccSeed...), workSeeds...),
			Routes: []Route{r("PATCH", pIssue1, ok(pushRes)), r("POST", pUsage, created(usageAttached))}},
		{Name: "assign/with-transcript", Args: []string{"assign", "DEMO-0001", "me"}, Env: cc, Seeds: ccSeed, // assign は付与しない（以前の CLI と同じ）
			Routes: []Route{r("POST", pIssue1+"/assign", ok(assignRes))}},
		{Name: "verify/with-transcript", Args: []string{"verify", "DEMO-0001"}, Env: cc, Seeds: ccSeed, Routes: []Route{
			r("GET", pIssue1+"/verify", ok(verifyPlan([]string{"echo ok"}, ""))),
			r("POST", pIssue1+"/verify", created(verifyPosted)), r("POST", pUsage, created(usageAttached))}},
		{Name: "comment/with-transcript-codex", Args: []string{"comment", "DEMO-0001", "x"}, Env: map[string]string{"CODEX_THREAD_ID": cxThread},
			Seeds:  []Seed{{Path: cxPath, Content: cxTranscript}},
			Routes: []Route{r("POST", pIssue1+"/comments", created(commentRes)), r("POST", pUsage, created(usageAttached))}},

		// ---- 付与の失敗: 操作は成功のまま。サーバの usage_notice があれば標準エラーへ（LOOPTRACK_USAGE_DEBUG=1 なら理由も）
		{Name: "comment/usage-notice", Args: []string{"comment", "DEMO-0001", "x"}, Env: cc, Seeds: ccSeed,
			Routes: []Route{r("POST", pIssue1+"/comments", created(commentNotice)), r("POST", pUsage, Response{Drop: true})}},
		{Name: "comment/usage-notice-debug", Args: []string{"comment", "DEMO-0001", "x"}, Env: withEnv(map[string]string{"LOOPTRACK_USAGE_DEBUG": "1"}), Seeds: ccSeed,
			Routes: []Route{r("POST", pIssue1+"/comments", created(commentNotice)), r("POST", pUsage, ErrorResponse(403, "forbidden", "権限がありません"))}},
		{Name: "comment/usage-sent-no-notice", Args: []string{"comment", "DEMO-0001", "x"}, Env: cc, Seeds: ccSeed, // 送れたら notice は出さない
			Routes: []Route{r("POST", pIssue1+"/comments", created(commentNotice)), r("POST", pUsage, created(usageAttached))}},
		{Name: "comment/usage-disabled", Args: []string{"comment", "DEMO-0001", "x"}, Env: withEnv(map[string]string{"LOOPTRACK_USAGE": "0"}), Seeds: ccSeed,
			Routes: []Route{r("POST", pIssue1+"/comments", created(commentNotice))}},

		// ---- クローズ時の必須化（usage_required_on_close）: 先に付けて 1 回だけやり直す
		{Name: "close/422-usage-required-attached", Args: []string{"close", "DEMO-0001", "--comment", "検証: go test"}, Env: cc, Seeds: ccSeed,
			Routes: []Route{r("POST", pIssue1+"/status",
				status(422, `{"error":{"code":"usage_required","message":"AI の Done にはトークン情報が要ります（usage attach DEMO-0001）","rule":"usage_required_on_close"}}`),
				ok(closeRes)), r("POST", pUsage, created(usageAttached))}},
		{Name: "close/422-usage-required-attach-fails", Args: []string{"close", "DEMO-0001"}, Env: cc, Seeds: ccSeed,
			Routes: []Route{r("POST", pIssue1+"/status",
				status(422, `{"error":{"code":"usage_required","message":"AI の Done にはトークン情報が要ります（usage attach DEMO-0001）","rule":"usage_required_on_close"}}`)),
				r("POST", pUsage, Response{Drop: true})}},
	}
}
