package server

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Copilot CLI のシェル（COPILOT_CLI=1 と COPILOT_AGENT_SESSION_ID）から打った looptrack issue の変更操作に、
// OpenTelemetry のファイル出力から作ったスナップショットが付き、usage missing（coverage）に残らない。
// OTel の 1 行の形は Copilot CLI 1.0.86 の実物: chat は cache_read・cache_write を持ち、根（invoke_agent）は input・output だけ。
func TestUsageCopilotCLIShellAttaches(t *testing.T) {
	e, _, ed := newAPIEnv(t)
	bin := looptrackBin(t)
	const sid = "5f0c1d2e-3a4b-4c5d-8e6f-7a8b9c0d1e2f"
	home := t.TempDir()
	ws := t.TempDir()
	otel := filepath.Join(home, ".copilot", "otel", "copilot-otel.jsonl")
	if err := os.MkdirAll(filepath.Dir(otel), 0o755); err != nil {
		t.Fatal(err)
	}
	span := func(trace, id, parent, op string, attrs string) string {
		par := "null"
		if parent != "" {
			par = fmt.Sprintf("%q", parent)
		}
		sec := e.clock.Now().Unix() - 60
		return fmt.Sprintf(`{"type":"span","traceId":%q,"spanId":%q,"parentSpanId":%s,"name":"%s x","kind":0,"startTime":[%d,0],"endTime":[%d,0],`+
			`"attributes":{"gen_ai.operation.name":%q,"gen_ai.conversation.id":%q,%s},"resource":{"attributes":{"service.version":"1.0.86"}}}`,
			trace, id, par, op, sec, sec+2, op, sid, attrs)
	}
	lines := []string{
		span("t1", "c1", "a1", "chat", `"gen_ai.response.model":"gpt-5.6-luna","gen_ai.usage.input_tokens":120000,"gen_ai.usage.cache_read.input_tokens":100000,"gen_ai.usage.cache_write.input_tokens":19990,"gen_ai.usage.output_tokens":500`),
		span("t1", "a1", "", "invoke_agent", `"gen_ai.usage.input_tokens":120000,"gen_ai.usage.output_tokens":500`),
	}
	write := func(ls []string) {
		t.Helper()
		if err := os.WriteFile(otel, []byte(strings.Join(ls, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(lines)
	run := func(copilot bool, args ...string) string {
		t.Helper()
		cmd := exec.Command(bin, append([]string{"issue"}, args...)...)
		cmd.Dir = ws
		env := append(cliAPIEnv(e.srv.URL+"/im", "req", ed.token, home), "LOOPTRACK_USAGE_DEBUG=1")
		if copilot {
			env = append(env, "COPILOT_CLI=1", "COPILOT_AGENT_SESSION_ID="+sid)
		}
		cmd.Env = env
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Run(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out.String())
		}
		return out.String()
	}
	count := func(where string) int {
		t.Helper()
		var n int
		if err := e.db.QueryRow("SELECT COUNT(*) FROM usage_snapshots WHERE " + where).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	run(true, "new", "Copilot のシェルから起票")
	if n := count(fmt.Sprintf("client = 'copilot' AND via = 'cli' AND trigger_kind = 'issue_op' AND op = 'create' AND session_id = '%s' AND issue_id IS NOT NULL", sid)); n != 1 {
		t.Fatalf("create のスナップショット = %d", n)
	}
	var in, cr, cw int64
	if err := e.db.QueryRow("SELECT main_input, main_cache_read, main_cache_create FROM usage_snapshots WHERE client = 'copilot' ORDER BY id LIMIT 1").Scan(&in, &cr, &cw); err != nil {
		t.Fatal(err)
	} else if in != 10 || cr != 100000 || cw != 19990 {
		t.Errorf("入力 = %d・読み %d・作成 %d（キャッシュを引いた 10 のはず）", in, cr, cw)
	}
	// 累計が増えたことを OTel に足す（同じ累計だと重複扱いになる）
	lines = append(lines, span("t2", "c2", "a2", "chat", `"gen_ai.response.model":"gpt-5.6-luna","gen_ai.usage.input_tokens":123000,"gen_ai.usage.cache_read.input_tokens":115000,"gen_ai.usage.cache_write.input_tokens":7985,"gen_ai.usage.output_tokens":450`))
	write(lines)
	run(true, "comment", "REQ-0001", "Copilot のシェルからコメント")
	lines = append(lines, span("t3", "c3", "a3", "chat", `"gen_ai.usage.input_tokens":5,"gen_ai.usage.output_tokens":1`))
	write(lines)
	if out := run(true, "usage", "attach", "REQ-0001"); !strings.Contains(out, "トークン情報: 付与") || !strings.Contains(out, "copilot") {
		t.Errorf("usage attach: %s", out)
	}
	if n := count("client = 'copilot' AND via = 'cli' AND issue_id IS NOT NULL"); n != 3 {
		t.Errorf("Copilot のスナップショット = %d（create・comment・manual）", n)
	}
	var cov coverageResp
	ed.json(200, "GET", "/projects/req/usage/coverage?mine=1", nil, &cov)
	if cov.Target != 2 || cov.Missing != 0 {
		t.Errorf("Copilot のシェルからの操作が未付与に残った: %+v", cov)
	}

}
