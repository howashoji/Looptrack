package usagesnap

// 以前の CLI（1.0.0 より前）のテストの usage_snapshot のケース（27 件）を移したもの。
// どのケースも、Go の期待値を確かめたうえで、同じ呼び出しを以前の CLI にも流して payload の完全一致を確かめる（snap）。
// usage_hook・以前の CLI の usage show のケース（CopilotHookTest・HookPlanTest・UsageShowOrderTest）は T9・T4 で移す。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/client/jsonorder"
)

// ---------------------------------------------------------------- 値の取り出し

func pick(t testing.TB, v any, path ...any) any {
	t.Helper()
	for _, p := range path {
		switch k := p.(type) {
		case string:
			o, ok := v.(*jsonorder.Object)
			if !ok {
				t.Fatalf("%v: オブジェクトではない", path)
			}
			x, ok := o.Get(k)
			if !ok {
				t.Fatalf("%v: キー %q が無い", path, k)
			}
			v = x
		case int:
			a, ok := v.([]any)
			if !ok || k >= len(a) {
				t.Fatalf("%v: 配列の %d 番目が無い", path, k)
			}
			v = a[k]
		}
	}
	return v
}

// eq は値の JSON（記録と同じ形）が want と一致することを確かめる。
func eq(t testing.TB, v any, want string, path ...any) {
	t.Helper()
	if got := jsonorder.Compact(pick(t, v, path...)); got != want {
		t.Errorf("%v = %s, want %s", path, got, want)
	}
}

func segField(t testing.TB, p *jsonorder.Object, field string) string {
	t.Helper()
	segs := pick(t, p, "segments").([]any)
	var out []string
	for _, s := range segs {
		out = append(out, jsonorder.Compact(pick(t, s, field)))
	}
	return "[" + strings.Join(out, ", ") + "]"
}

func hasKey(v any, key string) bool {
	o, ok := v.(*jsonorder.Object)
	return ok && o.Has(key)
}

// ---------------------------------------------------------------- 合成の会話記録

func jsonLine(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

type M = map[string]any

func merge(a M, b M) M {
	c := M{}
	for k, v := range a {
		c[k] = v
	}
	for k, v := range b {
		c[k] = v
	}
	return c
}

func claudeLine(kind, ts string, extra M) string {
	d := M{"type": kind, "timestamp": ts, "sessionId": "sess-1", "gitBranch": "main", "version": "2.1.271"}
	return jsonLine(merge(d, extra))
}

func claudeUsage(inp, ccreate, cread, out int, model string) M {
	if model == "" {
		model = "claude-opus-5"
	}
	return M{"model": model, "usage": M{"input_tokens": inp, "cache_creation_input_tokens": ccreate,
		"cache_read_input_tokens": cread, "output_tokens": out,
		"cache_creation": M{"ephemeral_1h_input_tokens": ccreate, "ephemeral_5m_input_tokens": 0}}}
}

func withID(id string, m M) M { return merge(m, M{"id": id}) }

func writeLines(t testing.TB, path string, lines []string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeClaudeTranscript(t testing.TB, home, session string, lines []string) string {
	return writeLines(t, filepath.Join(home, ".claude", "projects", "-Users-x-proj", session+".jsonl"), lines)
}

func claudeSample() []string {
	return []string{
		claudeLine("user", "2026-09-18T01:00:00.000Z", M{"origin": M{"kind": "human"}, "message": M{"role": "user", "content": "最初の指示"}}),
		// 同じ応答が内容ブロックごとに 2 行（同じ message.id）→ 1 回だけ数える。usage は最後の行の値
		claudeLine("assistant", "2026-09-18T01:00:10.000Z", M{"message": withID("m1", claudeUsage(10, 100, 1000, 5, ""))}),
		claudeLine("assistant", "2026-09-18T01:00:11.000Z", M{"message": withID("m1", claudeUsage(10, 100, 1000, 50, ""))}),
		// ツール結果（Read）
		claudeLine("user", "2026-09-18T01:00:12.000Z", M{"toolUseResult": M{"type": "text", "file": M{"filePath": "a.go", "content": "x\ny\n", "numLines": 2}},
			"message": M{"role": "user", "content": []any{M{"type": "tool_result"}}}}),
		claudeLine("assistant", "2026-09-18T01:00:20.000Z", M{"message": withID("m2", claudeUsage(20, 0, 2000, 100, ""))}),
		// ターン中の割込
		claudeLine("attachment", "2026-09-18T01:00:25.000Z", M{"attachment": M{"type": "queued_command", "origin": M{"kind": "human"},
			"prompt": "割り込みの指示", "timestamp": "2026-09-18T01:00:25.000Z"}}),
		claudeLine("assistant", "2026-09-18T01:00:30.000Z", M{"message": withID("m3", claudeUsage(30, 0, 3000, 200, "claude-fable-5-1"))}),
		// 2 分後に次の指示（待ち 1.5 分）
		claudeLine("user", "2026-09-18T01:02:00.000Z", M{"origin": M{"kind": "human"}, "message": M{"role": "user", "content": "二つ目の指示"}}),
		claudeLine("assistant", "2026-09-18T01:02:05.000Z", M{"message": withID("m4", claudeUsage(40, 0, 4000, 300, "claude-fable-5-1"))}),
		claudeLine("user", "2026-09-18T01:02:06.000Z", M{"toolUseResult": M{"filePath": "b.go", "structuredPatch": []any{M{"lines": []any{"+a", "+b", "-c"}}}, "newString": "ab"},
			"message": M{"role": "user", "content": []any{M{"type": "tool_result"}}}}),
	}
}

func homeEnv(home string, extra ...string) map[string]string {
	m := map[string]string{"HOME": home}
	for i := 0; i+1 < len(extra); i += 2 {
		m[extra[i]] = extra[i+1]
	}
	return m
}

// ---------------------------------------------------------------- ClaudeCodeAdapterTest

func claudeSetup(t *testing.T) (home, path string) {
	home = t.TempDir()
	return home, writeClaudeTranscript(t, home, "sess-1", claudeSample())
}

func TestClaudeCumulativeAndSegments(t *testing.T) {
	home, path := claudeSetup(t)
	p := snapObj(t, call{Fn: "claude", Path: path, SID: "sess-1", WS: true, Env: homeEnv(home)})
	eq(t, p, `"claude-code"`, "client")
	eq(t, p, `"2.1.271"`, "client_version")
	eq(t, p, `"sess-1"`, "session_id")
	eq(t, p, `4`, "responses") // m1 は 2 行でも 1 回
	eq(t, p, `{"input": 100, "cache_create": 100, "cache_read": 10000, "output": 650}`, "tokens", "main")
	eq(t, p, `0`, "tokens", "sub", "output")
	eq(t, p, `2`, "by_model", "claude-opus-5", "responses")
	eq(t, p, `500`, "by_model", "claude-fable-5-1", "output")
	eq(t, p, `100`, "by_model", "claude-opus-5", "cache_create_1h")
	eq(t, p, `1`, "io", "reads")
	eq(t, p, `2`, "io", "rlines")
	eq(t, p, `1`, "io", "edits")
	eq(t, p, `2`, "io", "add")
	eq(t, p, `1`, "io", "del")
	eq(t, p, `"main"`, "branch")
	eq(t, p, `"2026-09-18T01:02:06.000000Z"`, "at")
	eq(t, p, `false`, "excluded")
	if got := segField(t, p, "kind"); got != `["human", "intr", "human"]` {
		t.Errorf("kinds = %s", got)
	}
	if hasKey(pick(t, p, "segments", 0), "label") {
		t.Error("指示文は既定で送らない")
	}
	eq(t, p, `1.5`, "segments", 2, "idle_m")
	eq(t, p, `2`, "human", "pure")
	eq(t, p, `1`, "human", "intr")
	eq(t, p, `0.8`, "human", "idle_median_m") // 割込の待ち 0.1 分と二つ目の指示の 1.5 分の中央値
}

func TestClaudeConversationIDSurvivesResume(t *testing.T) {
	home, path := claudeSetup(t)
	p1 := snapObj(t, call{Fn: "claude", Path: path, SID: "sess-1", Env: homeEnv(home)})
	// 再開: 履歴を複製した別ファイル（セッション ID が違う）+ 続きの応答
	lines := append(claudeSample(), claudeLine("assistant", "2026-09-18T01:10:00.000Z", M{"sessionId": "sess-2", "message": withID("m5", claudeUsage(1, 0, 1, 1, ""))}))
	p2path := writeClaudeTranscript(t, home, "sess-2", lines)
	p2 := snapObj(t, call{Fn: "claude", Path: p2path, SID: "sess-2", Env: homeEnv(home)})
	if jsonorder.Compact(pick(t, p1, "conversation_id")) != jsonorder.Compact(pick(t, p2, "conversation_id")) {
		t.Error("再開しても会話 ID は同じ")
	}
	if jsonorder.Compact(pick(t, p1, "session_id")) == jsonorder.Compact(pick(t, p2, "session_id")) {
		t.Error("セッション ID は違う")
	}
	r1, _ := jsonorder.Int(pick(t, p1, "responses"))
	r2, _ := jsonorder.Int(pick(t, p2, "responses"))
	if r2 <= r1 {
		t.Errorf("responses %d <= %d", r2, r1)
	}
}

func TestClaudeNoSegmentsByDefaultAndPromptsOptIn(t *testing.T) {
	home, path := claudeSetup(t)
	p := snapObj(t, call{Fn: "claude", Path: path, SID: "sess-1", Env: homeEnv(home)})
	if p.Has("segments") {
		t.Error("segments は既定で付けない")
	}
	p = snapObj(t, call{Fn: "claude", Path: path, SID: "sess-1", WS: true, Env: promptsEnv(t, home, "", "1")})
	if hasKey(pick(t, p, "segments", 0), "label") {
		t.Error("環境変数だけでは送らない（プロジェクトのルールが要る）")
	}
	p = snapObj(t, call{Fn: "claude", Path: path, SID: "sess-1", WS: true, Env: promptsEnv(t, home, "1", "")})
	eq(t, p, `"最初の指示"`, "segments", 0, "label")
}

func TestClaudeExclusionPhrase(t *testing.T) {
	home, _ := claudeSetup(t)
	path := writeClaudeTranscript(t, home, "sess-3", append(claudeSample(),
		claudeLine("user", "2026-09-18T01:03:00.000Z", M{"origin": M{"kind": "human"}, "message": M{"role": "user", "content": "本セッションはレポート対象外にして"}})))
	eq(t, snapObj(t, call{Fn: "claude", Path: path, SID: "sess-3", Env: homeEnv(home)}), `true`, "excluded")
	path = writeClaudeTranscript(t, home, "sess-4", append(claudeSample(),
		claudeLine("user", "2026-09-18T01:03:00.000Z", M{"origin": M{"kind": "human"}, "message": M{"role": "user", "content": "「本セッションはレポート対象外」と書かれたセッションを集計から外して"}})))
	eq(t, snapObj(t, call{Fn: "claude", Path: path, SID: "sess-4", Env: homeEnv(home)}), `false`, "excluded") // 引用は除外の指示ではない
}

func TestClaudeSubagentsCountedSeparately(t *testing.T) {
	home, path := claudeSetup(t)
	writeLines(t, filepath.Join(filepath.Dir(path), "sess-1", "subagents", "agent-1.jsonl"), []string{
		claudeLine("assistant", "2026-09-18T01:00:15.000Z", M{"message": withID("sub1", claudeUsage(5, 0, 500, 7, ""))}),
	})
	p := snapObj(t, call{Fn: "claude", Path: path, SID: "sess-1", WS: true, Env: homeEnv(home)})
	eq(t, p, `1`, "sub_responses")
	eq(t, p, `7`, "tokens", "sub", "output")
	eq(t, p, `650`, "tokens", "main", "output")
	eq(t, p, `512`, "segments", 0, "sub") // 起動時刻を含む区間に載る
}

func TestClaudeDetectByEnvAndGlob(t *testing.T) {
	home, path := claudeSetup(t)
	v := snap(t, call{Fn: "detect", CWD: "/nowhere", Env: homeEnv(home, "CLAUDE_CODE_SESSION_ID", "sess-1")})
	if got, want := jsonorder.Compact(v), jsonorder.Compact([]any{"claude-code", path, "sess-1"}); got != want {
		t.Errorf("detect = %s, want %s", got, want)
	}
}

func TestClaudeUnreadableReturnsNone(t *testing.T) {
	home, _ := claudeSetup(t)
	if v := snap(t, call{Fn: "collect", Client: "claude-code", Path: "/no/such/file", SID: "x", Env: homeEnv(home)}); v != nil {
		t.Error("読めない会話記録は None")
	}
	empty := writeClaudeTranscript(t, home, "sess-5", []string{"not json",
		claudeLine("user", "2026-09-18T01:00:00Z", M{"origin": M{"kind": "human"}, "message": M{"content": "x"}})})
	if v := snap(t, call{Fn: "claude", Path: empty, SID: "sess-5", Env: homeEnv(home)}); v != nil {
		t.Error("応答が 1 つも無ければ None")
	}
}

// ---------------------------------------------------------------- CodexAdapterTest

const codexTID1 = "0199aaaa-bbbb-cccc-dddd-eeeeffff0001"

func codexSetup(t *testing.T) (home, cwd, path string) {
	home = t.TempDir()
	cwd = filepath.Join(home, "proj")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(home, ".codex", "sessions", "2026", "09", "18", "rollout-2026-09-18T01-00-00-"+codexTID1+".jsonl")
	rows := []any{
		M{"timestamp": "2026-09-18T01:00:00.000Z", "type": "session_meta",
			"payload": M{"id": codexTID1, "cwd": cwd, "cli_version": "0.140.0", "originator": "codex_cli_rs"}},
		M{"timestamp": "2026-09-18T01:00:01.000Z", "type": "event_msg", "payload": M{"type": "user_message", "message": "直して"}},
		M{"timestamp": "2026-09-18T01:00:05.000Z", "type": "response_item", "payload": M{"type": "message", "role": "assistant", "content": []any{}}},
		M{"timestamp": "2026-09-18T01:00:05.500Z", "type": "event_msg", "payload": M{"type": "token_count", "info": M{
			"total_token_usage": M{"input_tokens": 1000, "cached_input_tokens": 600, "output_tokens": 80, "reasoning_output_tokens": 30, "total_tokens": 1080}}}},
		M{"timestamp": "2026-09-18T01:03:00.000Z", "type": "event_msg", "payload": M{"type": "user_message", "message": "ありがとう"}},
		M{"timestamp": "2026-09-18T01:03:02.000Z", "type": "response_item", "payload": M{"type": "message", "role": "assistant", "content": []any{}}},
		M{"timestamp": "2026-09-18T01:03:02.500Z", "type": "event_msg", "payload": M{"type": "token_count", "info": M{
			"total_token_usage": M{"input_tokens": 1500, "cached_input_tokens": 900, "output_tokens": 100, "reasoning_output_tokens": 40, "total_tokens": 1600}}}},
	}
	writeRows(t, path, rows)
	return
}

func writeRows(t testing.TB, path string, rows []any) string {
	lines := make([]string, len(rows))
	for i, r := range rows {
		lines[i] = jsonLine(r)
	}
	return writeLines(t, path, lines)
}

func TestCodexCumulative(t *testing.T) {
	home, _, path := codexSetup(t)
	p := snapObj(t, call{Fn: "codex", Path: path, WS: true, Env: homeEnv(home)})
	eq(t, p, `"codex"`, "client")
	eq(t, p, `"0.140.0"`, "client_version")
	eq(t, p, `"`+codexTID1+`"`, "session_id")
	eq(t, p, `{"input": 600, "cache_create": 0, "cache_read": 900, "output": 100}`, "tokens", "main")
	eq(t, p, `2`, "responses")
	eq(t, p, `40`, "by_model", "(codex)", "reasoning")
	if got := segField(t, p, "kind"); got != `["human", "human"]` {
		t.Errorf("kinds = %s", got)
	}
	eq(t, p, `2`, "human", "pure")
	eq(t, p, `"2026-09-18T01:03:02.500000Z"`, "at")
}

func TestCodexFindByCWD(t *testing.T) {
	home, cwd, path := codexSetup(t)
	if v := snap(t, call{Fn: "codex_find", CWD: cwd, Env: homeEnv(home)}); v != path {
		t.Errorf("find = %v, want %s", v, path)
	}
	if v := snap(t, call{Fn: "codex_find", CWD: home, Env: homeEnv(home)}); v != nil {
		t.Error("作業ディレクトリが違えば拾わない")
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if v := snap(t, call{Fn: "codex_find", CWD: cwd, Env: homeEnv(home)}); v != nil {
		t.Error("古い記録は拾わない")
	}
}

func TestCodexDetectByThreadID(t *testing.T) {
	// CODEX_THREAD_ID は rollout のファイル名・session_meta.id と同じ。作業ディレクトリが違っても、古い記録でも、ID で引ける
	home, _, path := codexSetup(t)
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	v := snap(t, call{Fn: "detect", CWD: "/nowhere", Env: homeEnv(home, "CODEX_THREAD_ID", codexTID1, "CODEX_SESSION_ID", "0199ffff-0000-0000-0000-000000000000")})
	if got, want := jsonorder.Compact(v), jsonorder.Compact([]any{"codex", path, codexTID1}); got != want {
		t.Errorf("detect = %s, want %s", got, want)
	}
	v = snap(t, call{Fn: "detect", CWD: "/nowhere", Env: homeEnv(home, "CODEX_SESSION_ID", "0199ffff-0000-0000-0000-000000000000")})
	if got := jsonorder.Compact(v); got != `[null, null, null]` {
		t.Errorf("親の会話の ID では別スレッドの記録を引かない: %s", got)
	}
}

// ---------------------------------------------------------------- CodexSplitRolloutTest

func codexTC(ts string, inp, cached, out int, total ...int) M {
	tot := inp + out
	if len(total) > 0 {
		tot = total[0]
	}
	u := M{"input_tokens": inp, "cached_input_tokens": cached, "output_tokens": out, "reasoning_output_tokens": 0, "total_tokens": tot}
	return M{"timestamp": ts, "type": "event_msg", "payload": M{"type": "token_count", "info": M{
		"total_token_usage": u, "last_token_usage": merge(u, nil), "model_context_window": 258400}}}
}

const splitTID = "01a0b47f-67a4-79b2-b34c-3429c49662c8"

type splitFixture struct {
	home, day, f1, f2 string
	f2Rows            []any
}

func splitSetup(t *testing.T) *splitFixture {
	s := &splitFixture{home: t.TempDir()}
	s.day = filepath.Join(s.home, ".codex", "sessions", "2026", "09", "18")
	meta := M{"session_id": splitTID, "id": splitTID, "cwd": "/tmp/x", "originator": "Codex Desktop",
		"cli_version": "0.155.0-alpha.9", "source": "vscode", "history_mode": "paginated"}
	// 1 本目: 中断したターン（最後の token_count は同じ値の繰り返し。user_message の event_msg は無い）
	s.f1 = writeRows(t, filepath.Join(s.day, "rollout-2026-09-18T21-30-43-"+splitTID+".jsonl"), []any{
		M{"timestamp": "2026-09-18T12:30:43.649Z", "type": "session_meta", "payload": merge(meta, M{"timestamp": "2026-09-18T12:30:43.649Z"})},
		M{"timestamp": "2026-09-18T12:30:46.412Z", "type": "response_item", "payload": M{"type": "message", "role": "user", "content": []any{}}},
		codexTC("2026-09-18T12:30:50.234Z", 30981, 10624, 32),
		codexTC("2026-09-18T12:30:52.506Z", 64392, 40704, 52),
		codexTC("2026-09-18T12:30:54.604Z", 105513, 73856, 68),
		codexTC("2026-09-18T12:30:57.623Z", 105513, 73856, 68),
		M{"timestamp": "2026-09-18T12:30:57.635Z", "type": "event_msg", "payload": M{"type": "turn_aborted", "reason": "interrupted"}},
	})
	// 2 本目: 同じスレッドの続き。累計は 0 から。assistant の message は最終回答だけ
	s.f2Rows = []any{
		M{"timestamp": "2026-09-18T12:31:28.521Z", "type": "session_meta", "payload": merge(meta, M{"timestamp": "2026-09-18T12:31:28.521Z", "multi_agent_version": "v1"})},
		codexTC("2026-09-18T12:31:33.102Z", 31033, 30080, 33),
		codexTC("2026-09-18T12:31:35.563Z", 64497, 60160, 85),
		codexTC("2026-09-18T12:31:47.063Z", 147717, 133632, 292),
		codexTC("2026-09-18T12:32:08.972Z", 238838, 219392, 808),
		codexTC("2026-09-18T12:32:28.387Z", 299768, 264832, 1403),
		M{"timestamp": "2026-09-18T12:33:25.150Z", "type": "response_item", "payload": M{"type": "message", "role": "assistant", "phase": "final_answer", "content": []any{}}},
	}
	s.f2 = filepath.Join(s.day, "rollout-2026-09-18T21-31-28-"+splitTID+"_01a0b480-1709-7d90-8d0e-1fbdc20f17ee.jsonl")
	// サブエージェント（guardian）は別のスレッド ID・session_id は親と同じ。本体の累計には入れない
	writeRows(t, filepath.Join(s.day, "rollout-2026-09-18T21-31-39-01a0b480-40f9-7f50-8b67-7d3b8be01c56.jsonl"), []any{
		M{"timestamp": "2026-09-18T12:31:39.309Z", "type": "session_meta", "payload": merge(meta, M{
			"id": "01a0b480-40f9-7f50-8b67-7d3b8be01c56", "parent_thread_id": splitTID, "source": M{"subagent": M{"other": "guardian"}}})},
		codexTC("2026-09-18T12:31:44.286Z", 11438, 4864, 149),
	})
	return s
}

func mainTokens(t testing.TB, p *jsonorder.Object) map[string]int64 {
	out := map[string]int64{}
	for _, k := range []string{"input", "cache_create", "cache_read", "output"} {
		v, _ := jsonorder.Int(pick(t, p, "tokens", "main", k))
		out[k] = v
	}
	return out
}

func intAt(t testing.TB, p *jsonorder.Object, path ...any) int64 {
	v, _ := jsonorder.Int(pick(t, p, path...))
	return v
}

func TestCodexSplitCumulativeIsMonotonicAcrossRolloutFiles(t *testing.T) {
	s := splitSetup(t)
	s861 := snapObj(t, call{Fn: "codex", Path: s.f1, SID: splitTID, Env: homeEnv(s.home)})
	eq(t, s861, `{"input": 31657, "cache_create": 0, "cache_read": 73856, "output": 68}`, "tokens", "main")
	eq(t, s861, `3`, "responses") // 同じ値の token_count の繰り返しは応答に数えない
	prev := s861
	for n := 2; n <= len(s.f2Rows); n++ {
		writeRows(t, s.f2, s.f2Rows[:n])
		cur := snapObj(t, call{Fn: "codex", Path: s.f2, SID: splitTID, Env: homeEnv(s.home)})
		if jsonorder.Compact(pick(t, cur, "conversation_id")) != jsonorder.Compact(pick(t, s861, "conversation_id")) {
			t.Errorf("%d 行目: 同じ会話として束ねられる", n)
		}
		pm, cm := mainTokens(t, prev), mainTokens(t, cur)
		for k, v := range cm {
			if v < pm[k] {
				t.Errorf("%d 行目で %s が巻き戻った", n, k)
			}
		}
		if intAt(t, cur, "responses") < intAt(t, prev, "responses") {
			t.Errorf("%d 行目で応答数が減った", n)
		}
		prev = cur
	}
	want := fmt.Sprintf(`{"input": %d, "cache_create": 0, "cache_read": %d, "output": %d}`, 31657+(299768-264832), 73856+264832, 68+1403)
	eq(t, prev, want, "tokens", "main")
	eq(t, prev, `8`, "responses") // 応答数は合計が増えた token_count の数
	eq(t, snapObj(t, call{Fn: "codex", Path: s.f1, SID: splitTID, Env: homeEnv(s.home)}), want, "tokens", "main")
	eq(t, prev, `"`+splitTID+`"`, "session_id")
}

func TestCodexSplitServerStagesAreConsistent(t *testing.T) {
	// サーバの差分の規則（internal/usage.Stages と同じ: 累計の合計順に並べ、前の最大値との差）で負にならない
	s := splitSetup(t)
	snaps := []map[string]int64{mainTokens(t, snapObj(t, call{Fn: "codex", Path: s.f1, SID: splitTID, Env: homeEnv(s.home)}))}
	for _, n := range []int{3, 4, 5, 6} {
		writeRows(t, s.f2, s.f2Rows[:n])
		snaps = append(snaps, mainTokens(t, snapObj(t, call{Fn: "codex", Path: s.f2, SID: splitTID, Env: homeEnv(s.home)})))
	}
	sum := func(m map[string]int64) int64 { return m["input"] + m["cache_create"] + m["cache_read"] + m["output"] }
	for i := range snaps {
		for j := i + 1; j < len(snaps); j++ {
			if sum(snaps[j]) < sum(snaps[i]) {
				snaps[i], snaps[j] = snaps[j], snaps[i]
			}
		}
	}
	peak := map[string]int64{}
	for _, p := range snaps {
		for k, v := range p {
			if v < peak[k] {
				t.Errorf("inconsistent（負の差）になる: %s", k)
			}
			peak[k] = max(peak[k], v)
		}
	}
}

func TestCodexSplitFindLatestFileOfThread(t *testing.T) {
	s := splitSetup(t)
	writeRows(t, s.f2, s.f2Rows)
	st, _ := os.Stat(s.f1)
	old := st.ModTime().Add(-time.Minute)
	if err := os.Chtimes(s.f1, old, old); err != nil {
		t.Fatal(err)
	}
	if v := snap(t, call{Fn: "codex_find", CWD: "/nowhere", SID: splitTID, Env: homeEnv(s.home)}); v != s.f2 {
		t.Errorf("CODEX_THREAD_ID からは <スレッド ID>_<別の ID> の新しいファイルも引く: %v", v)
	}
}

func TestCodexSplitCopiedHistoryIsNotCountedTwice(t *testing.T) {
	// 新しいファイルが前の記録を複製して続きを数える形でも、二度数えない
	s := splitSetup(t)
	writeRows(t, s.f2, []any{s.f2Rows[0],
		codexTC("2026-09-18T12:30:54.604Z", 105513, 73856, 68),
		codexTC("2026-09-18T12:31:33.102Z", 136546, 103936, 101)})
	p := snapObj(t, call{Fn: "codex", Path: s.f2, SID: splitTID, Env: homeEnv(s.home)})
	eq(t, p, fmt.Sprintf(`{"input": %d, "cache_create": 0, "cache_read": 103936, "output": 101}`, 136546-103936), "tokens", "main")
	eq(t, p, `4`, "responses")
}

// ---------------------------------------------------------------- CodexUserMessageTest

const umTID = "01a0c000-1111-7222-8333-444455556666"

func codexEv(ts, kind string, payload M) M {
	return M{"timestamp": ts, "type": "event_msg", "payload": merge(M{"type": kind}, payload)}
}

// codexUser は新しい版の人の指示（item_completed の UserMessage と、response_item の写し）。
func codexUser(ts, turn, text string, n int) []any {
	return []any{
		M{"timestamp": ts, "type": "response_item", "payload": M{
			"type": "message", "id": fmt.Sprintf("msg_%s-%d", turn, n), "role": "user", "content": []any{M{"type": "input_text", "text": text}},
			"internal_chat_message_metadata_passthrough": M{"turn_id": turn, "create_time": 1.0, "content_item_kinds": []any{"user.text"}}}},
		codexEv(ts, "item_completed", M{"thread_id": "t", "turn_id": turn, "item": M{
			"type": "UserMessage", "id": fmt.Sprintf("item-%s-%d", turn, n), "client_id": "c",
			"content": []any{M{"type": "text", "text": text, "text_elements": []any{}}}},
			"started_at_ms": 0, "completed_at_ms": 0}),
	}
}

func codexContext(ts, turn string) M {
	return M{"timestamp": ts, "type": "response_item", "payload": M{
		"type": "message", "id": "msg_ctx_" + turn, "role": "user",
		"content": []any{M{"type": "input_text", "text": "# AGENTS.md instructions for /tmp/x\n本セッションはレポート対象外"},
			M{"type": "input_text", "text": "<environment_context>\n  <cwd>/tmp/x</cwd>\n</environment_context>"}},
		"internal_chat_message_metadata_passthrough": M{"turn_id": turn, "create_time": 1.0,
			"content_item_kinds": []any{"agents_md.instructions", "environments.environment_context"}}}}
}

type umFixture struct {
	home, day string
	meta      M
}

func umSetup(t *testing.T) *umFixture {
	f := &umFixture{home: t.TempDir()}
	f.day = filepath.Join(f.home, ".codex", "sessions", "2026", "09", "18")
	f.meta = M{"session_id": umTID, "id": umTID, "cwd": "/tmp/x", "originator": "Codex Desktop", "cli_version": "0.155.0-alpha.9", "source": "vscode"}
	return f
}

func (f *umFixture) rows(first, second string) []any {
	if first == "" {
		first = "一つ目の指示"
	}
	if second == "" {
		second = "二つ目の指示"
	}
	var r []any
	r = append(r,
		M{"timestamp": "2026-09-18T01:00:00.000Z", "type": "session_meta", "payload": merge(f.meta, M{"timestamp": "2026-09-18T01:00:00.000Z"})},
		codexEv("2026-09-18T01:00:01.000Z", "task_started", M{"turn_id": "turn-1", "started_at": 1}),
		codexContext("2026-09-18T01:00:01.500Z", "turn-1"))
	r = append(r, codexUser("2026-09-18T01:00:02.000Z", "turn-1", first, 1)...)
	r = append(r,
		codexTC("2026-09-18T01:00:10.000Z", 1000, 600, 50),
		codexTC("2026-09-18T01:00:20.000Z", 3000, 2000, 120),
		codexTC("2026-09-18T01:00:20.100Z", 3000, 2000, 120), // 同じ値の繰り返し（応答ではない）
		M{"timestamp": "2026-09-18T01:00:21.000Z", "type": "response_item", "payload": M{"type": "message", "role": "assistant", "phase": "final_answer", "content": []any{}}},
		codexEv("2026-09-18T01:00:21.500Z", "task_complete", M{"turn_id": "turn-1"}),
		// 約 3 分後に次の指示
		codexEv("2026-09-18T01:03:00.000Z", "task_started", M{"turn_id": "turn-2", "started_at": 2}))
	r = append(r, codexUser("2026-09-18T01:03:00.100Z", "turn-2", second, 1)...)
	r = append(r,
		codexTC("2026-09-18T01:03:30.000Z", 5000, 3500, 200),
		codexEv("2026-09-18T01:03:31.000Z", "task_complete", M{"turn_id": "turn-2"}))
	return r
}

func (f *umFixture) write(t testing.TB, name string, rows []any) string {
	return writeRows(t, filepath.Join(f.day, name), rows)
}

func umName(tid string) string { return "rollout-2026-09-18T10-00-00-" + tid + ".jsonl" }

func TestCodexSegmentsAndMetricsFromUserMessageItems(t *testing.T) {
	f := umSetup(t)
	path := f.write(t, umName(umTID), f.rows("", ""))
	p := snapObj(t, call{Fn: "codex", Path: path, SID: umTID, WS: true, Env: homeEnv(f.home)})
	if got := segField(t, p, "kind"); got != `["human", "human"]` {
		t.Errorf("差し込み（AGENTS.md など）と response_item の写しは指示に数えない: %s", got)
	}
	eq(t, p, `2`, "human", "pure")
	eq(t, p, `2.7`, "segments", 1, "idle_m") // 前の区間の最後の応答（最終回答 01:00:21）から 01:03:00.1 まで
	eq(t, p, `0.3`, "segments", 0, "work_m")
	if got := segField(t, p, "responses"); got != `[2, 1]` {
		t.Errorf("responses = %s", got)
	}
	// 区間ごとの消費（その区間に増えた累計）。和は会話の累計（送る値）と一致する
	if got := segField(t, p, "main"); got != fmt.Sprintf("[%d, %d]", 3000+120, 2000+80) {
		t.Errorf("main = %s", got)
	}
	eq(t, p, `{"input": 1500, "cache_create": 0, "cache_read": 3500, "output": 200}`, "tokens", "main")
	eq(t, p, `3`, "responses")
	eq(t, p, `false`, "excluded") // 差し込みの中の文言では対象外にしない
	p = snapObj(t, call{Fn: "codex", Path: path, SID: umTID, WS: true, Env: promptsEnv(t, f.home, "1", "1")})
	if got := segField(t, p, "label"); got != `["一つ目の指示", "二つ目の指示"]` {
		t.Errorf("labels = %s", got)
	}
}

func TestCodexConversationIDFromFirstInstruction(t *testing.T) {
	// Claude Code と同じ規則（最初の指示の時刻 + 内容の MD5）。スレッド ID の先頭 8 桁ではない
	f := umSetup(t)
	path := f.write(t, umName(umTID), f.rows("", ""))
	p := snapObj(t, call{Fn: "codex", Path: path, SID: umTID, Env: homeEnv(f.home)})
	text := "一つ目の指示"
	eq(t, p, `"`+ConversationID("2026-09-18T01:00:02.000Z", &text, umTID)+`"`, "conversation_id")
	if jsonorder.Compact(pick(t, p, "conversation_id")) == `"`+umTID[:8]+`"` {
		t.Error("スレッド ID の先頭 8 桁ではない")
	}
	// 約 65 秒以内に始めた別のスレッド（UUIDv7 の先頭 8 桁が同じ）とは別の会話になる
	other := "01a0c000-9999-7aaa-8bbb-ccccddddeeee"
	rows := f.rows("別の指示", "")
	rows[0] = M{"timestamp": "2026-09-18T01:00:00.000Z", "type": "session_meta", "payload": merge(f.meta, M{"id": other, "session_id": other})}
	q := snapObj(t, call{Fn: "codex", Path: f.write(t, "rollout-2026-09-18T10-00-30-"+other+".jsonl", rows), SID: other, Env: homeEnv(f.home)})
	if jsonorder.Compact(pick(t, p, "conversation_id")) == jsonorder.Compact(pick(t, q, "conversation_id")) {
		t.Error("別のスレッドは別の会話")
	}
}

func TestCodexExclusionPhrase(t *testing.T) {
	f := umSetup(t)
	path := f.write(t, umName(umTID), f.rows("", "本セッションはレポート対象外にして"))
	eq(t, snapObj(t, call{Fn: "codex", Path: path, SID: umTID, Env: homeEnv(f.home)}), `true`, "excluded")
	path = f.write(t, umName(umTID), f.rows("", "「本セッションはレポート対象外」と書かれた会話を集計から外して"))
	eq(t, snapObj(t, call{Fn: "codex", Path: path, SID: umTID, Env: homeEnv(f.home)}), `false`, "excluded") // 引用は除外の指示ではない
}

func TestCodexInstructionDuringRunningTurnIsInterrupt(t *testing.T) {
	// 実行中のターン（task_started の後、task_complete の前）に届いた 2 件目の指示は割込
	f := umSetup(t)
	rows := f.rows("", "")
	at := -1
	for i, r := range rows {
		if r.(M)["timestamp"] == "2026-09-18T01:00:20.000Z" {
			at = i
			break
		}
	}
	ins := codexUser("2026-09-18T01:00:15.000Z", "turn-1", "割り込みの指示", 2)
	rows = append(rows[:at], append(ins, rows[at:]...)...)
	p := snapObj(t, call{Fn: "codex", Path: f.write(t, umName(umTID), rows), SID: umTID, WS: true, Env: homeEnv(f.home)})
	if got := segField(t, p, "kind"); got != `["human", "intr", "human"]` {
		t.Errorf("kinds = %s", got)
	}
	eq(t, p, `2`, "human", "pure")
	eq(t, p, `1`, "human", "intr")
}

func TestCodexBothFormsOfSameInstructionCountOnce(t *testing.T) {
	// 旧形式（user_message）と新形式（UserMessage）を両方書く版があっても 1 回
	f := umSetup(t)
	rows := f.rows("", "")
	ev := codexEv("2026-09-18T01:00:02.001Z", "user_message", M{"message": "一つ目の指示", "images": []any{}, "text_elements": []any{}})
	rows = append(rows[:4], append([]any{ev}, rows[4:]...)...)
	p := snapObj(t, call{Fn: "codex", Path: f.write(t, umName(umTID), rows), SID: umTID, WS: true, Env: homeEnv(f.home)})
	if got := segField(t, p, "kind"); got != `["human", "human"]` {
		t.Errorf("kinds = %s", got)
	}
}

func TestCodexSplitRolloutKeepsConversationID(t *testing.T) {
	// 分かれたファイル: 1 本目で指示して中断、2 本目で続けた。どちらの時点でも会話 ID は同じ
	f := umSetup(t)
	rows := f.rows("", "")
	f1rows := append(append([]any{}, rows[:6]...), codexEv("2026-09-18T01:00:12.000Z", "turn_aborted", M{"turn_id": "turn-1", "reason": "interrupted"}))
	f1 := f.write(t, umName(umTID), f1rows)
	s1 := snapObj(t, call{Fn: "codex", Path: f1, SID: umTID, Env: homeEnv(f.home)})
	meta2 := M{"timestamp": "2026-09-18T01:01:00.000Z", "type": "session_meta", "payload": merge(f.meta, M{"timestamp": "2026-09-18T01:01:00.000Z"})}
	tail := []any{meta2, codexEv("2026-09-18T01:01:01.000Z", "task_started", M{"turn_id": "turn-3", "started_at": 3})}
	tail = append(tail, codexUser("2026-09-18T01:01:02.000Z", "turn-3", "続けて", 1)...)
	tail = append(tail, codexTC("2026-09-18T01:01:10.000Z", 900, 500, 30))
	f2name := "rollout-2026-09-18T10-01-00-" + umTID + "_01a0c001-0000-7000-8000-000000000000.jsonl"
	var p *jsonorder.Object
	for n := 1; n <= len(tail); n++ {
		p = snapObj(t, call{Fn: "codex", Path: f.write(t, f2name, tail[:n]), SID: umTID, WS: true, Env: homeEnv(f.home)})
		if jsonorder.Compact(pick(t, p, "conversation_id")) != jsonorder.Compact(pick(t, s1, "conversation_id")) {
			t.Errorf("%d 行目: 会話 ID が変わった", n)
		}
	}
	if got := segField(t, p, "kind"); got != `["human", "human"]` {
		t.Errorf("kinds = %s", got)
	}
	var sum int64
	for _, s := range pick(t, p, "segments").([]any) {
		v, _ := jsonorder.Int(pick(t, s, "main"))
		sum += v
	}
	m := mainTokens(t, p)
	if sum != m["input"]+m["cache_create"]+m["cache_read"]+m["output"] {
		t.Errorf("区間の和 %d が会話の累計と一致しない", sum)
	}
}

// ---------------------------------------------------------------- CopilotOtelAdapterTest

const copilotSID = "5f0c1d2e-3a4b-4c5d-8e6f-7a8b9c0d1e2f"

// otelCLI は Copilot CLI の形とされる 1 行。conv が nil なら会話 ID を付けない。
func otelCLI(trace, span, op string, sec int64, conv *string, parent string, usage M) string {
	attrs := M{"gen_ai.operation.name": op, "gen_ai.provider.name": "github"}
	c := copilotSID
	if conv != nil {
		c = *conv
	}
	if c != "" {
		attrs["gen_ai.conversation.id"] = c
	}
	for k, v := range usage {
		switch k {
		case "model":
			attrs["gen_ai.response.model"] = v
		case "turns":
			attrs["github.copilot.turn_count"] = v
		default:
			attrs["gen_ai.usage."+k] = v
		}
	}
	kind := 0
	if op == "chat" {
		kind = 2
	}
	var par any
	if parent != "" {
		par = parent
	}
	return jsonLine(M{"type": "span", "traceId": trace, "spanId": span, "parentSpanId": par, "name": op + " x", "kind": kind,
		"startTime": []any{sec, 0}, "endTime": []any{sec + 2, 500000000}, "attributes": attrs,
		"resource": M{"attributes": M{"service.name": "github-copilot", "service.version": "1.0.83"}}})
}

func chatUsage(inp, cr, cc, out int, model string) M {
	if model == "" {
		model = "claude-sonnet-4.6"
	}
	return M{"input_tokens": inp, "cache_read.input_tokens": cr, "cache_creation.input_tokens": cc, "output_tokens": out, "model": model}
}

func strp(s string) *string { return &s }

type copilotFixture struct {
	home, otelDir, path string
	lines               []string
}

func copilotSetup(t *testing.T) *copilotFixture {
	f := &copilotFixture{home: t.TempDir()}
	f.otelDir = filepath.Join(f.home, ".copilot", "otel")
	if err := os.MkdirAll(f.otelDir, 0o755); err != nil {
		t.Fatal(err)
	}
	f.path = filepath.Join(f.otelDir, "copilot-otel.jsonl")
	const T = 1789700000
	root1 := merge(chatUsage(2500, 1800, 100, 130, ""), M{"turns": 2})
	root2 := merge(chatUsage(1000, 100, 0, 60, ""), M{"turns": 3})
	f.lines = []string{
		otelCLI("t1", "c1", "chat", T+1, nil, "a1", chatUsage(1000, 600, 100, 50, "")),
		otelCLI("t1", "c2", "chat", T+5, nil, "a1", chatUsage(1500, 1200, 0, 80, "")),
		otelCLI("t1", "c2", "chat", T+5, nil, "a1", chatUsage(1500, 1200, 0, 80, "")),
		otelCLI("t1", "a1", "invoke_agent", T, nil, "", root1),
		otelCLI("t9", "x1", "chat", T+3, strp("other-session"), "", chatUsage(99999, 0, 0, 99999, "")),
		otelCLI("t2", "c3", "chat", T+100, nil, "a2", chatUsage(400, 0, 0, 20, "")),
		otelCLI("t2", "c4", "chat", T+101, nil, "a2", nil),
		otelCLI("t2", "c5", "chat", T+102, strp(""), "a2", chatUsage(300, 100, 0, 10, "")),
		otelCLI("t2", "a2", "invoke_agent", T+99, nil, "", root2),
		otelCLI("t2", "e1", "execute_tool", T+101, nil, "", nil),
		"not json",
		jsonLine(M{"resource": M{}, "scopeMetrics": []any{M{"metrics": []any{M{"name": "gen_ai.client.token.usage"}}}}}),
	}
	return f
}

func (f *copilotFixture) write(t testing.TB, lines []string, path string) {
	if path == "" {
		path = f.path
	}
	writeLines(t, path, lines)
}

func (f *copilotFixture) env(extra ...string) map[string]string {
	return homeEnv(f.home, append([]string{"COPILOT_HOME", filepath.Join(f.home, ".copilot")}, extra...)...)
}

func TestCopilotCumulative(t *testing.T) {
	f := copilotSetup(t)
	f.write(t, f.lines, "")
	p := snapObj(t, call{Fn: "collect", Client: "copilot", SID: copilotSID, CWD: "/x/proj", WS: true, Env: f.env()})
	eq(t, p, `"copilot"`, "client")
	eq(t, p, `"1.0.83"`, "client_version")
	eq(t, p, `"`+copilotSID+`"`, "session_id")
	eq(t, p, `"`+copilotSID+`"`, "conversation_id") // 会話 ID はセッション ID
	eq(t, p, fmt.Sprintf(`{"input": %d, "cache_create": 100, "cache_read": %d, "output": %d}`, 600+900, 1800+100, 130+60), "tokens", "main")
	eq(t, p, `{"input": 0, "cache_create": 0, "cache_read": 0, "output": 0}`, "tokens", "sub")
	eq(t, p, `5`, "responses") // t1 は chat 2 回、t2 は根の turn_count 3
	if got := segField(t, p, "kind"); got != `["human", "human"]` {
		t.Errorf("区間は指示（トレース）ごと: %s", got)
	}
	eq(t, p, `"proj"`, "cwd_name")
	eq(t, p, `"2026-09-18T02:55:04.500000Z"`, "at") // 最後のスパンの終わり
	var in, resp int64
	bm := pick(t, p, "by_model").(*jsonorder.Object)
	for _, k := range bm.Keys() {
		in += intAt(t, p, "by_model", k, "input")
		resp += intAt(t, p, "by_model", k, "responses")
	}
	if in != 1500 || resp != 5 {
		t.Errorf("by_model の和 input=%d responses=%d", in, resp)
	}
}

func TestCopilotMonotonicWhileWritten(t *testing.T) {
	f := copilotSetup(t)
	var prev *jsonorder.Object
	for n := 1; n <= len(f.lines); n++ {
		f.write(t, f.lines[:n], "")
		cur := snapObj(t, call{Fn: "collect", Client: "copilot", SID: copilotSID, Env: f.env()})
		if cur == nil {
			if prev != nil {
				t.Fatalf("%d 行目で None に戻った", n)
			}
			continue
		}
		if prev != nil {
			pm, cm := mainTokens(t, prev), mainTokens(t, cur)
			for k, v := range cm {
				if v < pm[k] {
					t.Errorf("%d 行目で %s が巻き戻った", n, k)
				}
			}
			if intAt(t, cur, "responses") < intAt(t, prev, "responses") {
				t.Errorf("%d 行目で応答数が減った", n)
			}
		}
		prev = cur
	}
	if prev == nil {
		t.Fatal("一度も値が出ない")
	}
}

func TestCopilotUnmeasuredReturnsNone(t *testing.T) {
	f := copilotSetup(t)
	if v := snap(t, call{Fn: "collect", Client: "copilot", SID: copilotSID, Env: f.env()}); v != nil {
		t.Error("OTel のファイルが無ければ None")
	}
	f.write(t, f.lines[4:5], "")
	if v := snap(t, call{Fn: "collect", Client: "copilot", SID: copilotSID, Env: f.env()}); v != nil {
		t.Error("そのセッションのスパンが無ければ None")
	}
	if v := snap(t, call{Fn: "collect", Client: "copilot", Env: f.env()}); v != nil {
		t.Error("セッション ID が無ければ推測しない")
	}
}

func TestCopilotDiscoveryAndDedupeAcrossPaths(t *testing.T) {
	f := copilotSetup(t)
	f.write(t, f.lines, "")
	env := f.env("LOOPTRACK_USAGE_COPILOT_OTEL", f.path, "COPILOT_OTEL_FILE_EXPORTER_PATH", f.path)
	if got := jsonorder.Compact(snap(t, call{Fn: "copilot_files", Env: env})); got != jsonorder.Compact([]any{f.path}) {
		t.Errorf("files = %s", got)
	}
	p := snapObj(t, call{Fn: "collect", Client: "copilot", SID: copilotSID, Env: env})
	eq(t, p, `190`, "tokens", "main", "output")
	// 別の場所のファイルは LOOPTRACK_USAGE_COPILOT_OTEL で足す。複製された同じスパンは二度数えない
	other := filepath.Join(f.home, "vscode-otel.jsonl")
	f.write(t, f.lines[:2], other)
	env2 := f.env("LOOPTRACK_USAGE_COPILOT_OTEL", other)
	if n := len(snap(t, call{Fn: "copilot_files", Env: env2}).([]any)); n != 2 {
		t.Errorf("files = %d", n)
	}
	p2 := snapObj(t, call{Fn: "collect", Client: "copilot", SID: copilotSID, Env: env2})
	if jsonorder.Compact(pick(t, p2, "tokens", "main")) != jsonorder.Compact(pick(t, p, "tokens", "main")) {
		t.Error("複製を二度数えた")
	}
}

func TestCopilotVSCodeAndOTLPShapes(t *testing.T) {
	f := copilotSetup(t)
	vs := M{"name": "chat gpt-5", "kind": 2, "_spanContext": M{"traceId": "v1", "spanId": "s1", "traceFlags": 1},
		"startTime": []any{1789700000, 0}, "endTime": []any{1789700001, 0},
		"attributes": M{"gen_ai.operation.name": "chat", "gen_ai.conversation.id": copilotSID, "gen_ai.request.model": "gpt-5",
			"gen_ai.usage.input_tokens": 100, "gen_ai.usage.output_tokens": 7, "gen_ai.usage.cache_read.input_tokens": 40}}
	otlp := M{"traceId": "v2", "spanId": "s2", "name": "chat", "startTimeUnixNano": "1789700100000000000", "endTimeUnixNano": "1789700101000000000",
		"attributes": []any{M{"key": "gen_ai.operation.name", "value": M{"stringValue": "chat"}},
			M{"key": "gen_ai.conversation.id", "value": M{"stringValue": copilotSID}},
			M{"key": "gen_ai.usage.input_tokens", "value": M{"intValue": "50"}},
			M{"key": "gen_ai.usage.output_tokens", "value": M{"intValue": "5"}}}}
	f.write(t, []string{jsonLine(vs), jsonLine(otlp)}, "")
	p := snapObj(t, call{Fn: "collect", Client: "copilot", SID: copilotSID, Env: f.env()})
	eq(t, p, `{"input": 110, "cache_create": 0, "cache_read": 40, "output": 12}`, "tokens", "main")
	eq(t, p, `2`, "responses")
	eq(t, p, `60`, "by_model", "gpt-5", "input")
	eq(t, p, `"2026-09-18T02:55:01.000000Z"`, "at")
}

func TestCopilotInputWithoutCacheIsNotReduced(t *testing.T) {
	f := copilotSetup(t)
	f.write(t, []string{otelCLI("t1", "c1", "chat", 1789700000, nil, "", chatUsage(100, 500, 0, 5, ""))}, "")
	p := snapObj(t, call{Fn: "collect", Client: "copilot", SID: copilotSID, Env: f.env()})
	eq(t, p, `{"input": 100, "cache_create": 0, "cache_read": 500, "output": 5}`, "tokens", "main")
}

// Copilot CLI 1.0.86 の実データの形。chat は cache_read・cache_write を持ち input_tokens はキャッシュを含む。
// 根（invoke_agent）は input_tokens（chat の和＝キャッシュを含む）と output_tokens だけで cache の属性が無い。
// 根の入力は chat から引いたキャッシュの和を引いてから比べる（引かないと入力にキャッシュが二重に入る）。以前の CLI と一致すること（snap）。
func TestCopilotCLIRootWithoutCacheAttrs(t *testing.T) {
	f := copilotSetup(t)
	const T = 1789700000
	cw := func(inp, cr, cw, out int) M {
		return M{"input_tokens": inp, "cache_read.input_tokens": cr, "cache_write.input_tokens": cw, "output_tokens": out,
			"reasoning.output_tokens": 7, "model": "gpt-5.6-luna"}
	}
	lines := []string{
		otelCLI("t1", "c1", "chat", T+1, nil, "a1", cw(120000, 100000, 19990, 500)),
		otelCLI("t1", "c2", "chat", T+5, nil, "a1", cw(123000, 115000, 7985, 450)),
		otelCLI("t1", "c3", "chat", T+9, nil, "a1", cw(125266, 120719, 4533, 500)),
		otelCLI("t1", "a1", "invoke_agent", T, nil, "", M{"input_tokens": 368266, "output_tokens": 1450}),
	}
	f.write(t, lines, "")
	p := snapObj(t, call{Fn: "collect", Client: "copilot", SID: copilotSID, WS: true, Env: f.env()})
	eq(t, p, `{"input": 39, "cache_create": 32508, "cache_read": 335719, "output": 1450}`, "tokens", "main")
	if keys := pick(t, p, "by_model").(*jsonorder.Object).Keys(); len(keys) != 1 || keys[0] != "gpt-5.6-luna" {
		t.Errorf("根の分の（copilot）が出た: %v", keys)
	}
	eq(t, p, fmt.Sprint(39+32508+335719+1450), "segments", 0, "main")
	// 根だけが先に読めた（chat が欠けた・copilot-cli#4860）ときは引けるものが無いので、根の値のまま（キャッシュを含む過大な推定）
	f.write(t, lines[3:], "")
	p = snapObj(t, call{Fn: "collect", Client: "copilot", SID: copilotSID, Env: f.env()})
	eq(t, p, `368266`, "tokens", "main", "input")
	// キャッシュを含まない出力元の chat（input < cache の和）からは何も引いていないので、根からも引かない
	f.write(t, []string{otelCLI("t1", "c1", "chat", T+1, nil, "", chatUsage(100, 500, 0, 5, "")),
		otelCLI("t1", "a1", "invoke_agent", T, nil, "", M{"input_tokens": 100, "output_tokens": 5})}, "")
	p = snapObj(t, call{Fn: "collect", Client: "copilot", SID: copilotSID, Env: f.env()})
	eq(t, p, `{"input": 100, "cache_create": 0, "cache_read": 500, "output": 5}`, "tokens", "main")
}

func TestCopilotDetectDoesNotGuess(t *testing.T) {
	// Copilot CLI の環境変数（COPILOT_CLI=1 と COPILOT_AGENT_SESSION_ID）が無ければ、OTel のファイルがあっても detect は Copilot を推測しない
	f := copilotSetup(t)
	f.write(t, f.lines, "")
	if got := jsonorder.Compact(snap(t, call{Fn: "detect", CWD: "/nowhere", Env: f.env()})); got != `[null, null, null]` {
		t.Errorf("detect = %s", got)
	}
}

// Copilot CLI のシェル（COPILOT_CLI=1 と COPILOT_AGENT_SESSION_ID）では detect が Copilot のセッションを返し、
// collect は OTel のファイル出力から作る。以前の CLI（1.0.0 より前・凍結）の detect はこの分岐を持たないので、Go 版だけを確かめる（runGo）。
func TestCopilotDetectFromCLIEnv(t *testing.T) {
	f := copilotSetup(t)
	f.write(t, f.lines, "")
	det := func(extra ...string) string {
		t.Helper()
		got, _ := runGo(t, call{Fn: "detect", CWD: "/nowhere", Env: f.env(extra...)})
		return got
	}
	if got := det("COPILOT_CLI", "1", "COPILOT_AGENT_SESSION_ID", " "+copilotSID+" "); got != `["copilot", null, "`+copilotSID+`"]` {
		t.Errorf("detect = %s", got)
	}
	// COPILOT_CLI=1 と組み合わせたときだけ読む（internal/client/session と同じ判定）
	for _, extra := range [][]string{{"COPILOT_AGENT_SESSION_ID", copilotSID}, {"COPILOT_CLI", "1"}, {"COPILOT_CLI", "0", "COPILOT_AGENT_SESSION_ID", copilotSID}} {
		if got := det(extra...); got != `[null, null, null]` {
			t.Errorf("%v: detect = %s", extra, got)
		}
	}
	// 自動の collect（usage attach・変更操作の後の付与）が Copilot の OTel から作る
	_, v := runGo(t, call{Fn: "collect", CWD: "/x/proj", Env: f.env("COPILOT_CLI", "1", "COPILOT_AGENT_SESSION_ID", copilotSID)})
	p, _ := v.(*jsonorder.Object)
	if p == nil {
		t.Fatal("collect が None")
	}
	eq(t, p, `"copilot"`, "client")
	eq(t, p, fmt.Sprintf(`{"input": %d, "cache_create": 100, "cache_read": %d, "output": %d}`, 600+900, 1800+100, 130+60), "tokens", "main")
	// Claude Code の会話記録が見つかればそちら（Copilot の端末から Claude Code を起動したとき。session.Detect と同じ順）
	home, path := claudeSetup(t)
	got, _ := runGo(t, call{Fn: "detect", CWD: "/nowhere", Env: homeEnv(home, "CLAUDE_CODE_SESSION_ID", "sess-1", "COPILOT_CLI", "1", "COPILOT_AGENT_SESSION_ID", copilotSID)})
	if want := jsonorder.Compact([]any{"claude-code", path, "sess-1"}); got != want {
		t.Errorf("detect = %s, want %s", got, want)
	}
}

// 作業ディレクトリで Codex の会話記録を推測するより先に Copilot を見る（同じディレクトリの Codex の記録を Copilot の操作に付けない）。
func TestCopilotDetectBeforeCodexCwdGuess(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(home, "proj")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	dir := filepath.Join(home, ".codex", "sessions", now.Format("2006"), now.Format("01"), now.Format("02"))
	tid := "0199aaaa-bbbb-cccc-dddd-eeeeffff0009"
	writeLines(t, filepath.Join(dir, "rollout-"+now.Format("2006-01-02T15-04-05")+"-"+tid+".jsonl"), []string{
		jsonLine(M{"timestamp": now.Format(time.RFC3339Nano), "type": "session_meta", "payload": M{"id": tid, "cwd": cwd}}),
	})
	got, _ := runGo(t, call{Fn: "detect", CWD: cwd, Env: homeEnv(home)})
	if !strings.HasPrefix(got, `["codex", `) {
		t.Fatalf("前提: Copilot の変数が無ければ作業ディレクトリで Codex を推測する: %s", got)
	}
	got, _ = runGo(t, call{Fn: "detect", CWD: cwd, Env: homeEnv(home, "COPILOT_CLI", "1", "COPILOT_AGENT_SESSION_ID", copilotSID)})
	if got != `["copilot", null, "`+copilotSID+`"]` {
		t.Errorf("detect = %s", got)
	}
	// VS Code の Copilot のエージェント用ターミナル（セッション ID なし）でも Codex を推測しない
	for _, kv := range [][2]string{{"AI_AGENT", "github_copilot_vscode_agent"}, {"COPILOT_AGENT", "1"}} {
		if got, _ := runGo(t, call{Fn: "detect", CWD: cwd, Env: homeEnv(home, kv[0], kv[1])}); got != `[null, null, null]` {
			t.Errorf("%s: detect = %s", kv[0], got)
		}
	}
}
