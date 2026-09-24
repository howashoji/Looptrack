package usagesnap

// Copilot CLI 1.0.86 の実物（2026-09-19 に確かめた OTel のファイル出力と hook の環境）に合わせたテスト。
//   - 1 行の形: OTLP の JSON（type span / metric・startTime / endTime は [秒, ナノ秒]・attributes は辞書）
//   - chat の属性: input_tokens（キャッシュ込み）・cache_read / cache_write・output_tokens・reasoning.output_tokens
//     （最初の応答は cache_read が無く cache_write だけ・最後の応答は reasoning が無い）
//   - 根（invoke_agent）は input_tokens（chat の和）と output_tokens だけで cache の属性が無い
//   - hook とエージェントのシェルには COPILOT_OTEL_FILE_EXPORTER_PATH が渡らない。渡るのは COPILOT_CLI・COPILOT_CLI_BINARY_VERSION・
//     COPILOT_HOME・COPILOT_PROJECT_DIR・COPILOT_TRACEPARENT（と Claude Code 互換の CLAUDE_PROJECT_DIR）だけ
// 値は実物の会話（1 回の invoke_agent・chat 10 回）の数値。ID・ハッシュは合成した。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
)

const cp186Trace = "0a1b2c3d4e5f60718293a4b5c6d7e8f9"

// cp186Span は Copilot CLI 1.0.86 の 1 行（span）と同じ形。attrs は gen_ai.operation.name 以外の属性（JSON の中身）。
func cp186Span(span, parent, name, op string, start, end [2]int64, attrs string) string {
	par := "null"
	if parent != "" {
		par = `"` + parent + `"`
	}
	kind := 0
	if op == "chat" {
		kind = 2
	}
	return fmt.Sprintf(`{"type":"span","traceId":"%s","spanId":"%s","parentSpanId":%s,"name":"%s","kind":%d,`+
		`"startTime":[%d,%d],"endTime":[%d,%d],"attributes":{"gen_ai.operation.name":"%s","gen_ai.provider.name":"github",`+
		`"gen_ai.conversation.id":"%s",%s},"status":{"code":0},"events":[],`+
		`"resource":{"attributes":{"service.version":"1.0.86","service.name":"github-copilot"},"schemaUrl":"https://opentelemetry.io/schemas/1.44.0"},`+
		`"instrumentationScope":{"name":"github.copilot","version":"1.0.86"}}`,
		cp186Trace, span, par, name, kind, start[0], start[1], end[0], end[1], op, copilotSID, attrs)
}

// cp186Lines は実物の会話 1 つ分（chat 10・execute_tool・invoke_agent・metric）。
func cp186Lines() []string {
	// 開始 [秒, ナノ秒]・終了・input_tokens・cache_read（-1 は属性なし）・cache_write・output_tokens・reasoning（-1 は属性なし）
	chats := [][9]int64{
		{1789796621, 310000000, 1789796624, 368000000, 17114, -1, 17111, 48, 29},
		{1789796624, 369000000, 1789796626, 573000000, 19374, 17111, 2260, 130, 78},
		{1789796626, 574000000, 1789796627, 803000000, 19537, 19371, 163, 52, 8},
		{1789796627, 803000000, 1789796629, 124000000, 19723, 19534, 186, 52, 10},
		{1789796629, 124000000, 1789796631, 254000000, 19896, 19720, 173, 245, 52},
		{1789796632, 884000000, 1789796634, 653000000, 20365, 17111, 3251, 35, 14},
		{1789796634, 654000000, 1789796637, 339000000, 22593, 20362, 2228, 251, 120},
		{1789796637, 339000000, 1789796641, 242000000, 22950, 22590, 357, 354, 84},
		{1789796641, 242000000, 1789796643, 855000000, 23386, 22947, 436, 115, 38},
		{1789796643, 858000000, 1789796644, 996000000, 23804, 23383, 418, 87, -1},
	}
	var lines []string
	for i, c := range chats {
		a := []string{`"gen_ai.request.model":"auto"`, `"gen_ai.request.stream":true`,
			fmt.Sprintf(`"gen_ai.usage.input_tokens":%d`, c[4]), fmt.Sprintf(`"gen_ai.usage.output_tokens":%d`, c[7])}
		if c[5] >= 0 {
			a = append(a, fmt.Sprintf(`"gen_ai.usage.cache_read.input_tokens":%d`, c[5]))
		}
		a = append(a, fmt.Sprintf(`"gen_ai.usage.cache_write.input_tokens":%d`, c[6]))
		if c[8] >= 0 {
			a = append(a, fmt.Sprintf(`"gen_ai.usage.reasoning.output_tokens":%d`, c[8]))
		}
		a = append(a, `"gen_ai.response.model":"gpt-5.6-luna"`, fmt.Sprintf(`"gen_ai.response.id":"resp-%02d"`, i),
			`"github.copilot.cost":1.0`, `"github.copilot.initiator":"user"`)
		span := fmt.Sprintf("c%015d", i)
		lines = append(lines, cp186Span(span, "a000000000000001", "chat auto", "chat",
			[2]int64{c[0], c[1]}, [2]int64{c[2], c[3]}, strings.Join(a, ",")))
		// ツールの実行（使用量の属性は無い。MCP のサーバ名はハッシュで出る）
		lines = append(lines, cp186Span(fmt.Sprintf("e%015d", i), "a000000000000001", "execute_tool bash", "execute_tool",
			[2]int64{c[2], c[3]}, [2]int64{c[2], c[3] + 1000}, `"gen_ai.tool.name":"bash","gen_ai.tool.type":"function"`))
	}
	lines = append(lines,
		cp186Span("a000000000000001", "", "invoke_agent", "invoke_agent", [2]int64{1789796621, 109000000}, [2]int64{1789796645, 313000000},
			`"gen_ai.usage.input_tokens":208742,"gen_ai.usage.output_tokens":1369,"github.copilot.turn_count":10,`+
				`"github.copilot.context.mcp_server_names":"[\"4c96a3dc55eb446a58524cf5336260ffb09488a0e0ce48bff0368c261d2f7f6e\"]"`),
		// metric の行（数えない）
		`{"type":"metric","name":"gen_ai.client.token.usage","description":"Number of input and output tokens used.","unit":"{token}",`+
			`"dataPoints":[{"attributes":{"gen_ai.operation.name":"chat","gen_ai.provider.name":"github","gen_ai.response.model":"gpt-5.6-luna","gen_ai.token.type":"input"},`+
			`"startTime":[1789796618,407480000],"endTime":[1789796645,332697000],"value":{"count":10,"sum":208742}}]}`,
	)
	return lines
}

// hookEnv は Copilot CLI 1.0.86 が hook に渡した環境（COPILOT_OTEL_FILE_EXPORTER_PATH は無い）。
func hookEnv(home, copilotHome string) map[string]string {
	return homeEnv(home, "COPILOT_CLI", "1", "COPILOT_CLI_BINARY_VERSION", "1.0.86", "COPILOT_HOME", copilotHome,
		"COPILOT_PROJECT_DIR", filepath.Join(home, "proj"), "COPILOT_TRACEPARENT", "00-"+cp186Trace+"-89fb757792def800-01")
}

// 実物の形の読み取り: 属性名（cache_write）・[秒, ナノ秒]・根のキャッシュの補正・応答数・モデル名（response.model）・時刻。
func TestCopilot186RealShape(t *testing.T) {
	home := t.TempDir()
	ch := filepath.Join(home, "copilot-home")
	writeLines(t, filepath.Join(ch, "otel", "copilot-otel.jsonl"), cp186Lines())
	p := snapObj(t, call{Fn: "collect", Client: "copilot", SID: copilotSID, WS: true, Env: hookEnv(home, ch)})
	if p == nil {
		t.Fatal("実物の形の OTel を読めない")
	}
	// chat の和: input 208,742（キャッシュ込み）・cache_read 182,129・cache_write 26,583・output 1,369 → input は 30
	eq(t, p, `{"input": 30, "cache_create": 26583, "cache_read": 182129, "output": 1369}`, "tokens", "main")
	eq(t, p, `10`, "responses")
	eq(t, p, `"1.0.86"`, "client_version")
	if keys := pick(t, p, "by_model").(*jsonorder.Object).Keys(); len(keys) != 1 || keys[0] != "gpt-5.6-luna" {
		t.Errorf("by_model は response.model の 1 つだけ（根の（copilot）や request.model の auto が出ない）: %v", keys)
	}
	eq(t, p, `30`, "by_model", "gpt-5.6-luna", "input")
	eq(t, p, `26583`, "by_model", "gpt-5.6-luna", "cache_create")
	eq(t, p, `"2026-09-19T05:44:05.313000Z"`, "at") // 根の終わり [1789796645, 313000000]
	eq(t, p, `"2026-09-19T05:43:41.109000Z"`, "segments", 0, "start")
}

// 既定の置き場: COPILOT_OTEL_FILE_EXPORTER_PATH が無い環境（hook・シェル）でも、$COPILOT_HOME/otel/ の下
// （サブディレクトリも）、COPILOT_HOME が無ければ ~/.copilot/otel/ の下を必ず見る。
func TestCopilotDefaultDirWithoutExporterEnv(t *testing.T) {
	lines := cp186Lines()
	t.Run("COPILOT_HOME", func(t *testing.T) {
		home := t.TempDir()
		ch := filepath.Join(home, "ch")
		path := filepath.Join(ch, "otel", "runs", "2026-09-19", "run.jsonl")
		writeLines(t, path, lines)
		e := hookEnv(home, ch)
		if got := jsonorder.Compact(snap(t, call{Fn: "copilot_files", Env: e})); got != jsonorder.Compact([]any{path}) {
			t.Errorf("files = %s", got)
		}
		p := snapObj(t, call{Fn: "collect", Client: "copilot", SID: copilotSID, Env: e})
		if p == nil {
			t.Fatal("$COPILOT_HOME/otel/ の下のファイルを見つけない")
		}
		eq(t, p, `1369`, "tokens", "main", "output")
		// シェル（COPILOT_AGENT_SESSION_ID）からの自動の付与も同じ置き場を読む
		_, v := runGo(t, call{Fn: "collect", CWD: home, Env: merge2(e, "COPILOT_AGENT_SESSION_ID", copilotSID)})
		if v == nil {
			t.Fatal("シェルからの collect が None")
		}
	})
	t.Run("~/.copilot", func(t *testing.T) {
		home := t.TempDir()
		path := filepath.Join(home, ".copilot", "otel", "copilot-otel.jsonl")
		writeLines(t, path, lines)
		e := homeEnv(home) // COPILOT_HOME も COPILOT_OTEL_FILE_EXPORTER_PATH も無い
		if got := jsonorder.Compact(snap(t, call{Fn: "copilot_files", Env: e})); got != jsonorder.Compact([]any{path}) {
			t.Errorf("files = %s", got)
		}
		if p := snapObj(t, call{Fn: "collect", Client: "copilot", SID: copilotSID, Env: e}); p == nil {
			t.Fatal("~/.copilot/otel/ の下のファイルを見つけない")
		}
	})
	t.Run("既定の外", func(t *testing.T) {
		// 利用者が既定の外に書かせた。hook には出力先が渡らないので見つからない（CLI の実物の再現）
		home := t.TempDir()
		ch := filepath.Join(home, "ch")
		writeLines(t, filepath.Join(home, "elsewhere", "run.jsonl"), lines)
		e := hookEnv(home, ch)
		if p := snap(t, call{Fn: "collect", Client: "copilot", SID: copilotSID, Env: e}); p != nil {
			t.Fatal("既定の外のファイルを読んだ（前提が崩れた）")
		}
		o := Options{Env: env.FromMap(e), Home: home}
		hint := CopilotMissHint(copilotSID, o)
		for _, want := range []string{filepath.Join(ch, "otel") + " の下にありません", "hook にもシェルにも渡さない", "$COPILOT_HOME/otel/ の下",
			"COPILOT_OTEL_FILE_EXPORTER_PATH=" + filepath.Join(ch, "otel", "copilot-otel.jsonl")} {
			if !strings.Contains(hint, want) {
				t.Errorf("案内に %q が無い: %s", want, hint)
			}
		}
		// 置き場にファイルはあるが、このセッションのスパンが無い
		writeLines(t, filepath.Join(ch, "otel", "old.jsonl"), lines[:1])
		hint = CopilotMissHint("other-session", o)
		if !strings.Contains(hint, "1 個の OpenTelemetry のファイル出力に gen_ai.conversation.id = other-session のスパンがありません") {
			t.Errorf("案内: %s", hint)
		}
		// 出力先を既定の下へ移せば見つかる
		if err := os.Rename(filepath.Join(home, "elsewhere", "run.jsonl"), filepath.Join(ch, "otel", "run.jsonl")); err != nil {
			t.Fatal(err)
		}
		if p := snap(t, call{Fn: "collect", Client: "copilot", SID: copilotSID, Env: e}); p == nil {
			t.Fatal("$COPILOT_HOME/otel/ へ移しても見つからない")
		}
	})
}
