package core

// usage の送信のケース（以前のテスト（1.0.0 より前）の hook の分と、
// 起動元の判定）。偽 API に送った要求・spool・usage-last を確かめる。

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/hookio"
)

func claudeLine(kind, ts string, extra map[string]any) string {
	d := map[string]any{"type": kind, "timestamp": ts, "sessionId": "sess-1", "gitBranch": "main", "version": "2.1.271"}
	for k, v := range extra {
		d[k] = v
	}
	b, _ := json.Marshal(d)
	return string(b)
}

func usageOf(inp, create, read, out int, model string) map[string]any {
	return map[string]any{"model": model, "usage": map[string]any{"input_tokens": inp, "cache_creation_input_tokens": create,
		"cache_read_input_tokens": read, "output_tokens": out,
		"cache_creation": map[string]any{"ephemeral_1h_input_tokens": create, "ephemeral_5m_input_tokens": 0}}}
}

func msg(id string, u map[string]any) map[string]any {
	u["id"] = id
	return map[string]any{"message": u}
}

// sampleTranscript は 以前のテストの SAMPLE と同じ会話記録（同じ message.id の 2 行・ツール結果・割込・2 つ目の指示）。
func sampleTranscript() string {
	lines := []string{
		claudeLine("user", "2026-09-18T01:00:00.000Z", map[string]any{"origin": map[string]any{"kind": "human"}, "message": map[string]any{"role": "user", "content": "最初の指示"}}),
		claudeLine("assistant", "2026-09-18T01:00:10.000Z", msg("m1", usageOf(10, 100, 1000, 5, "claude-opus-5"))),
		claudeLine("assistant", "2026-09-18T01:00:11.000Z", msg("m1", usageOf(10, 100, 1000, 50, "claude-opus-5"))),
		claudeLine("user", "2026-09-18T01:00:12.000Z", map[string]any{"toolUseResult": map[string]any{"type": "text", "file": map[string]any{"filePath": "a.go", "content": "x\ny\n", "numLines": 2}},
			"message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result"}}}}),
		claudeLine("assistant", "2026-09-18T01:00:20.000Z", msg("m2", usageOf(20, 0, 2000, 100, "claude-opus-5"))),
		claudeLine("attachment", "2026-09-18T01:00:25.000Z", map[string]any{"attachment": map[string]any{"type": "queued_command", "origin": map[string]any{"kind": "human"},
			"prompt": "割り込みの指示", "timestamp": "2026-09-18T01:00:25.000Z"}}),
		claudeLine("assistant", "2026-09-18T01:00:30.000Z", msg("m3", usageOf(30, 0, 3000, 200, "claude-fable-5-1"))),
		claudeLine("user", "2026-09-18T01:02:00.000Z", map[string]any{"origin": map[string]any{"kind": "human"}, "message": map[string]any{"role": "user", "content": "二つ目の指示"}}),
		claudeLine("assistant", "2026-09-18T01:02:05.000Z", msg("m4", usageOf(40, 0, 4000, 300, "claude-fable-5-1"))),
	}
	return strings.Join(lines, "\n") + "\n"
}

func (s *sandbox) transcript() string {
	return filepath.Join(s.home, ".claude", "projects", "-x-proj", "sess-1.jsonl")
}

func usageSetup(s *sandbox) { s.write(s.transcript(), sampleTranscript()) }

// usageIn は Claude Code の hook の入力。
func usageIn(s *sandbox, event string, extra map[string]any) map[string]any {
	in := map[string]any{"hook_event_name": event, "session_id": "sess-1", "transcript_path": s.transcript(), "cwd": s.proj}
	for k, v := range extra {
		in[k] = v
	}
	return in
}

func usageStep(name, event string, extra map[string]any, env map[string]string, want func(*testing.T, *sandbox, got)) step {
	return step{name: name, mk: func(s *sandbox) call {
		return call{hook: "usage", input: usageIn(s, event, extra), env: env}
	}, want: want}
}

// sent は偽 API に送った usage の本文。
func sent(s *sandbox) []map[string]any {
	var out []map[string]any
	for _, r := range s.api.requests() {
		if r.Method == "POST" && strings.HasSuffix(r.Path, "/usage") {
			m, _ := r.Body.(map[string]any)
			out = append(out, m)
		}
	}
	return out
}

func wantSent(n int, check func(t *testing.T, last map[string]any)) step {
	return step{name: "送った数", do: func(s *sandbox) {
		ps := sent(s)
		if len(ps) != n {
			s.t.Errorf("送った usage は %d 件のはず: %d", n, len(ps))
			return
		}
		if check != nil && n > 0 {
			check(s.t, ps[n-1])
		}
	}}
}

func spoolFiles(s *sandbox) []string {
	m, _ := filepath.Glob(filepath.Join(s.appDir(), "usage-spool", "*.json"))
	return m
}

func wantSpool(n int) step {
	return step{name: "spool の数", do: func(s *sandbox) {
		if got := len(spoolFiles(s)); got != n {
			s.t.Errorf("spool は %d 件のはず: %d", n, got)
		}
	}}
}

func setAPI(f func(a *fakeAPI)) step {
	return step{name: "偽 API を変える", do: func(s *sandbox) {
		s.api.mu.Lock()
		defer s.api.mu.Unlock()
		f(s.api)
	}}
}

func TestUsage(t *testing.T) {
	mcp := func(tool string, input map[string]any, resp any) map[string]any {
		m := map[string]any{"tool_name": tool, "tool_input": input, "tool_use_id": "t1"}
		if resp != nil {
			m["tool_response"] = resp
		}
		return m
	}
	yes := true
	cases := []scenario{
		{name: "Stop は送り、間引き、LOOPTRACK_USAGE_THROTTLE_MIN=0 なら間引かない", steps: []step{
			usageStep("stop", "Stop", nil, nil, wantQuiet),
			wantSent(1, func(t *testing.T, p map[string]any) {
				if p["trigger"] != "stop" || p["client"] != "claude-code" || p["session_id"] != "sess-1" || p["segments"] == nil {
					t.Errorf("payload: %v", p)
				}
			}),
			usageStep("2 回目は間引く", "Stop", nil, nil, wantQuiet), wantSent(1, nil),
			usageStep("SessionEnd は間引かない", "SessionEnd", nil, nil, wantQuiet),
			wantSent(2, func(t *testing.T, p map[string]any) {
				if p["trigger"] != "session_end" {
					t.Errorf("payload: %v", p)
				}
			}),
			usageStep("THROTTLE_MIN=0", "Stop", nil, map[string]string{"LOOPTRACK_USAGE_THROTTLE_MIN": "0"}, wantQuiet), wantSent(3, nil),
			usageStep("THROTTLE_MIN が数でなければ 10 分", "Stop", nil, map[string]string{"LOOPTRACK_USAGE_THROTTLE_MIN": "x"}, wantQuiet), wantSent(3, nil),
		}},
		{name: "MCP の変更操作は issue_op で送る", steps: []step{
			usageStep("add_comment", "PostToolUse", mcp("mcp__looptrack-oauth__add_comment", map[string]any{"id": "tst-0001", "text": "x"}, map[string]any{"content": []any{}}), nil, wantQuiet),
			wantSent(1, func(t *testing.T, p map[string]any) {
				if p["trigger"] != "issue_op" || p["issue"] != "TST-0001" || p["op"] != "comment" || p["via"] != "mcp" || p["tool_use_id"] != "t1" {
					t.Errorf("payload: %v", p)
				}
				if _, ok := p["segments"]; ok {
					t.Errorf("issue_op は区間を付けない: %v", p)
				}
			}),
			usageStep("create_issue（応答の文から ID）", "PostToolUse", mcp("mcp__looptrack__create_issue", map[string]any{"title": "x"},
				map[string]any{"content": []any{map[string]any{"type": "text", "text": "作成: TST-0007 x（version 1）"}}}), nil, wantQuiet),
			wantSent(2, func(t *testing.T, p map[string]any) {
				if p["issue"] != "TST-0007" || p["op"] != "create" {
					t.Errorf("payload: %v", p)
				}
			}),
			usageStep("create_issue（構造化の応答）", "PostToolUse", mcp("mcp__looptrack__create_issue", map[string]any{"title": "x"},
				map[string]any{"structuredContent": map[string]any{"id": "TST-0008"}, "content": []any{map[string]any{"type": "text", "text": "作成: TST-0007"}}}), nil, wantQuiet),
			usageStep("set_status（数の id）", "PostToolUse", mcp("mcp__looptrack__set_status", map[string]any{"issue": 12}, nil), nil, wantQuiet),
			wantSent(4, func(t *testing.T, p map[string]any) {
				if p["issue"] != "12" || p["op"] != "status" {
					t.Errorf("payload: %v", p)
				}
			}),
			usageStep("別のサーバ", "PostToolUse", mcp("mcp__linear__create_issue", map[string]any{"id": "TST-0001"}, nil), nil, wantQuiet),
			usageStep("読むだけのツール", "PostToolUse", mcp("mcp__looptrack__get_issue", map[string]any{"id": "TST-0001"}, nil), nil, wantQuiet),
			usageStep("失敗した操作", "PostToolUse", mcp("mcp__looptrack__add_comment", map[string]any{"id": "TST-0001"}, map[string]any{"isError": true}), nil, wantQuiet),
			usageStep("Bash", "PostToolUse", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "looptrack issue comment TST-0001 x"}}, nil, wantQuiet),
			usageStep("PreToolUse", "PreToolUse", mcp("mcp__looptrack__add_comment", map[string]any{"id": "TST-0001"}, nil), nil, wantQuiet),
			usageStep("id が無い", "PostToolUse", mcp("mcp__looptrack__update_issue", map[string]any{"title": "x"}, nil), nil, wantQuiet),
			usageStep("LOOPTRACK_MCP_SERVER で別のサーバ名", "PostToolUse", mcp("mcp__issues__add_comment", map[string]any{"id": "TST-0002"}, nil),
				map[string]string{"LOOPTRACK_MCP_SERVER": "^issues$"}, wantQuiet),
			wantSent(5, nil),
		}},
		{name: "送れなければ spool し、次に送れたら消す", steps: []step{
			setAPI(func(a *fakeAPI) { a.usageStatus = 503 }),
			usageStep("503", "SessionEnd", nil, nil, wantQuiet), wantSpool(1),
			usageStep("503 のまま", "SessionEnd", nil, nil, wantQuiet), wantSpool(2),
			setAPI(func(a *fakeAPI) { a.usageStatus = 0 }),
			// 503 の 2 回目は spool の 1 件目で諦める（2 件）。送れたら spool 2 件 + 今回 = 3 件で合計 6 件
			usageStep("送れる", "SessionEnd", nil, nil, wantQuiet), wantSpool(0), wantSent(6, nil),
		}},
		{name: "4xx は捨てる（spool しない）", steps: []step{
			setAPI(func(a *fakeAPI) { a.usageStatus = 400 }),
			usageStep("400", "Stop", nil, nil, wantQuiet), wantSpool(0), wantSent(1, nil),
			usageStep("送ったことになるので間引く", "Stop", nil, nil, wantQuiet), wantSent(1, nil),
		}},
		{name: "繋がらなければ spool", steps: []step{
			{name: "down", mk: func(s *sandbox) call { return call{hook: "usage", input: usageIn(s, "Stop", nil), downAPI: true} }, want: wantQuiet},
			wantSpool(1),
		}},
		{name: "7 日より古い spool は捨てる", steps: []step{
			{name: "古い spool を置く", do: func(s *sandbox) {
				p := filepath.Join(s.appDir(), "usage-spool", "1700000000-deadbeef.json")
				s.write(p, `{"client": "claude-code", "session_id": "old"}`)
				old := time.Now().Add(-8 * 24 * time.Hour)
				_ = os.Chtimes(p, old, old)
				s.write(filepath.Join(s.appDir(), "usage-spool", "1700000001-00000000.json"), `{"client": "claude-code", "session_id": "keep", "n": 1.5}`)
			}},
			usageStep("stop", "Stop", nil, nil, wantQuiet), wantSpool(0), wantSent(2, nil),
		}},
		{name: "作業名を送るかをサーバの応答から覚える", steps: []step{
			setAPI(func(a *fakeAPI) { a.sendPrompts = &yes }),
			usageStep("1 回目（覚える）", "SessionEnd", nil, nil, wantQuiet),
			usageStep("2 回目（作業名つき）", "SessionEnd", nil, nil, wantQuiet),
			wantSent(2, func(t *testing.T, p map[string]any) {
				segs, _ := p["segments"].([]any)
				if len(segs) == 0 {
					t.Fatalf("segments が無い: %v", p)
				}
				if seg, _ := segs[0].(map[string]any); seg["label"] == nil {
					t.Errorf("2 回目は作業名を付けるはず: %v", segs)
				}
			}),
		}},
		{name: "送らない条件", steps: []step{
			usageStep("LOOPTRACK_USAGE=0", "Stop", nil, map[string]string{"LOOPTRACK_USAGE": "0"}, wantQuiet),
			{name: "API なし", mk: func(s *sandbox) call { return call{hook: "usage", input: usageIn(s, "Stop", nil), noAPI: true} }, want: wantQuiet},
			usageStep("プロジェクトなし", "Stop", nil, map[string]string{"LOOPTRACK_PROJECT": ""}, wantQuiet),
			{name: "トークンなし", mk: func(s *sandbox) call { return call{hook: "usage", input: usageIn(s, "Stop", nil), noToken: true} }, want: wantQuiet},
			usageStep("会話記録が無い", "Stop", map[string]any{"transcript_path": "/nonexistent/x.jsonl"}, nil, wantQuiet),
			wantSent(0, nil), wantSpool(0),
		}},
		{name: "Copilot から .claude の配線が起動したら何もしない・--client copilot なら動く", steps: []step{
			{name: "Copilot から", mk: func(s *sandbox) call {
				return call{hook: "usage", bare: true, env: map[string]string{"COPILOT_AGENT": "1"}, input: map[string]any{"hook_event_name": "SessionEnd", "session_id": "s1"}}
			}, want: wantQuiet},
			{name: "--client copilot（OTel が無いので送らない）", mk: func(s *sandbox) call {
				return call{hook: "usage", agent: hookio.Copilot, bare: true, args: []string{"--client", "copilot", "--event", "sessionEnd"},
					env: map[string]string{"COPILOT_AGENT": "1", "LOOPTRACK_USAGE_COPILOT_WAIT_SEC": "0"}, input: map[string]any{"sessionId": "s1", "timestamp": 1}}
			}, want: wantQuiet},
			{name: "Copilot の MCP（camelCase）", mk: func(s *sandbox) call {
				return call{hook: "usage", agent: hookio.Copilot, bare: true, args: []string{"--client", "copilot", "--event", "postToolUse"},
					env: map[string]string{"LOOPTRACK_USAGE_COPILOT_WAIT_SEC": "0"},
					input: map[string]any{"sessionId": "s1", "timestamp": 1, "cwd": s.proj, "toolName": "looptrack-add_comment",
						"toolArgs": `{"id": "tst-0131", "text": "x"}`, "toolResult": map[string]any{"resultType": "success", "textResultForLlm": "ok"}}}
			}, want: wantQuiet},
			wantSent(0, nil),
		}},
	}
	for _, sc := range cases {
		sc.setup = chain(usageSetup, sc.setup)
		sc.run(t)
	}
}

// TestUsagePlanCopilot は Copilot のツール名の形（以前のテストのツール名と失敗のケース）。Go だけ。
func TestUsagePlanCopilot(t *testing.T) {
	e := (&Env{}).withDefaults()
	c := &Call{Env: e}
	plan := func(raw map[string]any, agent hookio.Agent) *usagePlan {
		ev := hookio.FromMap(raw, hookio.ParseOptions{Agent: agent, Getenv: func(string) string { return "" }})
		return c.plan(ev, string(agent))
	}
	base := func(name string) map[string]any {
		return map[string]any{"hook_event_name": "PostToolUse", "tool_name": name, "tool_input": map[string]any{"id": "REQ-0001"}}
	}
	for _, n := range []string{"looptrack-set_status", "mcp_looptrack_set_status", "looptrack/set_status", "mcp__looptrack-oauth__set_status", "looptrack.set_status"} {
		if p := plan(base(n), hookio.Copilot); p == nil || p.IssueID != "REQ-0001" || p.Op != "status" {
			t.Errorf("%s: %+v", n, p)
		}
	}
	if p := plan(base("linear-set_status"), hookio.Copilot); p != nil {
		t.Errorf("別のサーバ: %+v", p)
	}
	if p := plan(base("looptrack-set_status"), hookio.ClaudeCode); p != nil {
		t.Errorf("Copilot 以外は従来の形だけ: %+v", p)
	}
	failed := base("looptrack-set_status")
	failed["tool_result"] = map[string]any{"result_type": "failure"}
	if p := plan(failed, hookio.Copilot); p != nil {
		t.Errorf("失敗: %+v", p)
	}
	created := map[string]any{"hook_event_name": "PostToolUse", "tool_name": "looptrack-create_issue", "tool_input": map[string]any{"title": "x"},
		"tool_result": map[string]any{"result_type": "success", "text_result_for_llm": "作成: REQ-0009 x（version 1）"}}
	if p := plan(created, hookio.Copilot); p == nil || p.IssueID != "REQ-0009" || p.Op != "create" {
		t.Errorf("create: %+v", p)
	}
	if p := plan(map[string]any{"hook_event_name": "agentStop"}, hookio.Copilot); p == nil || p.Trigger != "stop" {
		t.Errorf("agentStop: %+v", p)
	}
	if p := plan(map[string]any{"hook_event_name": "AfterAgent"}, hookio.ClaudeCode); p == nil || p.Trigger != "stop" {
		t.Errorf("AfterAgent: %+v", p)
	}
	camel := map[string]any{"sessionId": "s", "toolName": "looptrack-add_comment", "toolArgs": `{"id": "req-0131"}`, "toolResult": map[string]any{"resultType": "success"}}
	ev := hookio.FromMap(camel, hookio.ParseOptions{Agent: hookio.Copilot, Event: "postToolUse", Getenv: func(string) string { return "" }})
	if p := c.plan(ev, "copilot"); p == nil || p.IssueID != "REQ-0131" || p.Op != "comment" {
		t.Errorf("camelCase: %+v", p)
	}
}

// TestUsageDebugCopilotMissHint は、Copilot の OTel を読めないときのデバッグ表示に置き場の案内が付くこと。Go だけ。
func TestUsageDebugCopilotMissHint(t *testing.T) {
	home := t.TempDir()
	// LOOPTRACK_LANG: 下の検査は日本語の文面を見る（既定は英語なので、指定が無いと実行する機械の LANG で結果が変わる）
	vars := env.FromMap(map[string]string{"HOME": home, "COPILOT_HOME": filepath.Join(home, "ch"), "LOOPTRACK_USAGE_DEBUG": "1",
		"LOOPTRACK_LANG": "ja"})
	run := func(client string) string {
		var buf bytes.Buffer
		c := &Call{Env: (&Env{Vars: &vars, Sleep: func(time.Duration) {}}).withDefaults(), Stderr: &buf}
		c.work(context.Background(), usageJob{usagePlan: usagePlan{Trigger: "stop"}, Client: client, SessionID: "sid-miss", CWD: home,
			TranscriptPath: filepath.Join(home, "missing.jsonl")})
		return buf.String()
	}
	out := run("copilot")
	for _, w := range []string{"会話記録を読めません（client=copilot", "OpenTelemetry のファイル出力（*.jsonl）が " + filepath.Join(home, "ch", "otel") + " の下にありません",
		"$COPILOT_HOME/otel/ の下"} {
		if !strings.Contains(out, w) {
			t.Errorf("Copilot の案内に %q が無い: %s", w, out)
		}
	}
	if out := run("claude-code"); !strings.Contains(out, "会話記録を読めません（client=claude-code") || strings.Contains(out, "OpenTelemetry") {
		t.Errorf("Copilot 以外は従来の表示: %s", out)
	}
}
