package cli

import (
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/transcript"
	"github.com/howashoji/looptrack/internal/client/usagesnap"
	"github.com/howashoji/looptrack/internal/domain"
)

const trTestSID = "00000000-0000-4000-8000-0000000000e1"

func trSrc(key string, kind transcript.Kind, data string) *trSource {
	return &trSource{TranscriptFile: usagesnap.TranscriptFile{FileKey: key}, kind: kind, data: []byte(data)}
}

var trTestOpt = transcript.Options{Agent: transcript.AgentClaude, Hints: transcript.Hints{Roots: []string{"/w/proj"}, Home: "/w"}}

// 送った続きの判断: 状態の長さとサーバの長さが同じなら直前 4KiB を比べ、違えば送らない（書き換えられた）。
// 状態が無いときは直前の 1 バイトが改行であることだけを確かめる。短くなったら送らない。json と text はファイル全体を 1 回だけ。
func TestTrPlanAllContinuation(t *testing.T) {
	line1 := `{"cwd":"/w/proj","n":1}` + "\n"
	line2 := `{"cwd":"/w/proj","n":2}` + "\n"
	n1 := int64(len(line1))
	main := trSrc(trTestSID+".jsonl", transcript.KindJSONL, line1+line2)
	sameTail := &trPushState{Sent: map[string]trSent{main.FileKey: {Length: n1, Tail: tailSum([]byte(line1), n1)}}}
	otherTail := &trPushState{Sent: map[string]trSent{main.FileKey: {Length: n1, Tail: tailSum([]byte(strings.Replace(line1, "1", "9", 1)), n1)}}}
	none := &trPushState{Sent: map[string]trSent{}}
	for _, c := range []struct {
		name      string
		offset    int64
		state     *trPushState
		rewritten bool
		chunks    int
	}{
		{"最初から", 0, none, false, 1},
		{"続き（直前 4KiB が同じ）", n1, sameTail, false, 1},
		{"送った部分が変わった", n1, otherTail, true, 0},
		{"状態なし・直前が改行", n1, none, false, 1},
		{"状態なし・直前が改行でない", n1 - 1, none, true, 0},
		{"短くなった", int64(len(line1+line2)) + 5, none, true, 0},
		{"送り終えた", int64(len(line1 + line2)), none, false, 0},
	} {
		plans, rejects := trPlanAll([]*trSource{main}, map[string]int64{main.FileKey: c.offset}, c.state, trTestOpt)
		if len(rejects) != 0 {
			t.Fatalf("%s: 止め %v", c.name, rejects)
		}
		p := plans[0]
		chunks := 0
		if p.res != nil {
			chunks = len(p.res.Chunks)
			if chunks > 0 && p.res.Chunks[0].Offset != c.offset {
				t.Errorf("%s: 最初のチャンクの位置 %d（%d のはず）", c.name, p.res.Chunks[0].Offset, c.offset)
			}
		}
		if p.rewritten != c.rewritten || chunks != c.chunks {
			t.Errorf("%s: rewritten %v・チャンク %d（%v・%d のはず）", c.name, p.rewritten, chunks, c.rewritten, c.chunks)
		}
	}
	meta := trSrc(trTestSID+"/subagents/a.meta.json", transcript.KindJSON, `{"a":1}`)
	text := trSrc(trTestSID+"/tool-results/t.txt", transcript.KindText, "out\n")
	plans, _ := trPlanAll([]*trSource{meta, text}, map[string]int64{}, none, trTestOpt)
	if plans[0].res == nil || len(plans[0].res.Chunks) != 1 || plans[1].res == nil || len(plans[1].res.Chunks) != 1 {
		t.Fatalf("json と text の最初の送信が 1 チャンクになっていません")
	}
	plans, _ = trPlanAll([]*trSource{meta, text}, map[string]int64{meta.FileKey: 7, text.FileKey: 4}, none, trTestOpt)
	if plans[0].res != nil || plans[0].rewritten || plans[1].res != nil || plans[1].rewritten {
		t.Fatalf("送り終えた json と text を送り直そうとしています")
	}
	plans, _ = trPlanAll([]*trSource{meta, text}, map[string]int64{meta.FileKey: 3, text.FileKey: 2}, none, trTestOpt)
	if plans[0].rewritten || !plans[0].changed || plans[0].res != nil {
		t.Fatalf("送った後に大きさの変わった json は送り直さず、「書き換えられた」とは別に数えるはず: %+v", plans[0])
	}
	if !plans[1].rewritten || plans[1].changed {
		t.Fatalf("送った後に大きさの変わった text を「書き換えられた」にしていません: %+v", plans[1])
	}
	empty := trSrc(trTestSID+"/subagents/b.meta.json", transcript.KindJSON, "  ")
	plans, rejects := trPlanAll([]*trSource{empty}, map[string]int64{}, none, trTestOpt)
	if len(rejects) != 0 || plans[0].res != nil || plans[0].rewritten {
		t.Fatalf("書きかけ（空）の json は次の回に回すはず: %v %+v", rejects, plans[0])
	}
}

// まだ送っていないサブエージェントの json は、同じ agent-<id>.meta.json が "finished": true を持つか、
// 同じ agent-<id>.jsonl の更新時刻が 30 分以上前のときだけ送る。どちらでもなければ送らず、書き換えられたにも数えない。
// 対照: 同じ組の jsonl は走っている間も送る。finished・30 分以上前のどちらかがあれば json も送る。
func TestTrPlanAllSubagentJSONWaitsForFinish(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	prev := trNow
	trNow = func() time.Time { return now }
	defer func() { trNow = prev }()
	sub := func(name string, kind usagesnap.TranscriptKind, tk transcript.Kind, data string, mod time.Time) *trSource {
		s := trSrc(trTestSID+"/subagents/"+name, tk, data)
		s.Kind, s.mod = kind, mod
		return s
	}
	none := &trPushState{Sent: map[string]trSent{}}
	for _, c := range []struct {
		name string
		meta string
		age  time.Duration // jsonl の更新からの時間
		send bool
	}{
		{"finished なし・jsonl が新しい", `{"agentType":"x"}`, time.Minute, false},
		{"finished なし・jsonl が 29 分前", `{"agentType":"x"}`, 29 * time.Minute, false},
		{"finished: true", `{"agentType":"x","finished":true}`, time.Minute, true},
		{"finished なし・jsonl が 30 分前", `{"agentType":"x"}`, 30 * time.Minute, true},
		{"finished なし・jsonl が 31 分前", `{"agentType":"x"}`, 31 * time.Minute, true},
	} {
		jl := sub("agent-a.jsonl", usagesnap.KindSubagent, transcript.KindJSONL, `{"a":1}`+"\n", now.Add(-c.age))
		meta := sub("agent-a.meta.json", usagesnap.KindSubagentMeta, transcript.KindJSON, c.meta, time.Time{})
		prefix := sub("agent-a.prefix.json", usagesnap.KindSubagentMeta, transcript.KindJSON, `{"p":1}`, time.Time{})
		// 別のサブエージェントの finished は効かない
		other := sub("agent-b.meta.json", usagesnap.KindSubagentMeta, transcript.KindJSON, `{"finished":true}`, time.Time{})
		plans, rejects := trPlanAll([]*trSource{jl, meta, prefix, other}, map[string]int64{}, none, trTestOpt)
		if len(rejects) != 0 {
			t.Fatalf("%s: 止め %v", c.name, rejects)
		}
		if plans[0].res == nil || len(plans[0].res.Chunks) != 1 {
			t.Errorf("%s: 前提が崩れています: 同じ組の jsonl を送っていません", c.name)
		}
		for _, p := range plans[1:3] {
			got := p.res != nil && len(p.res.Chunks) == 1
			if got != c.send || p.rewritten || p.changed {
				t.Errorf("%s: %s を送る %v・rewritten %v・changed %v（送る %v・false・false のはず）", c.name, p.src.FileKey, got, p.rewritten, p.changed, c.send)
			}
		}
		if plans[3].res == nil {
			t.Errorf("%s: finished: true の agent-b.meta.json を送っていません", c.name)
		}
	}
}

// 止め（読めない行）はファイルごとに数え、ほかのファイルの変換は続ける（呼ぶ側が 1 つでもあれば何も送らない）。
func TestTrPlanAllReject(t *testing.T) {
	bad := trSrc(trTestSID+".jsonl", transcript.KindJSONL, "{not json}\n")
	good := trSrc(trTestSID+"/subagents/a.jsonl", transcript.KindJSONL, `{"a":1}`+"\n")
	_, rejects := trPlanAll([]*trSource{bad, good}, map[string]int64{}, &trPushState{Sent: map[string]trSent{}}, trTestOpt)
	if len(rejects) != 1 || rejects[0].key != bad.FileKey || !transcript.IsReject(rejects[0].err) {
		t.Fatalf("止め: %+v", rejects)
	}
}

// stats のヘッダに載せるマスクの数のキーは、サーバの stats の検査（値ごとの FindSecret）に当たらない形にする。
// 対照: 規則の語そのまま（変換が数えたキー）はどれも FindSecret に当たる（直さないと拒まれる）。
func TestHeaderStatsLabelsPassSecretCheck(t *testing.T) {
	samples := []string{"password=Synthetic0Value9", "Bearer abcdefghijkl", "token: Synthetic0Value9", "imp_abcdef",
		"-----BEGIN RSA PRIVATE KEY-----\nAAAA\n-----END RSA PRIVATE KEY-----", "sk-abcdefghijklmnopqrstuvwxyz", "AKIAABCDEFGHIJKLMNOP"}
	st := transcript.Stats{Masked: map[string]int{}}
	for _, s := range samples {
		_, counts := domain.MaskSecretsCount(s)
		if len(counts) == 0 {
			t.Fatalf("前提が崩れています: %q がマスクに当たりません", s)
		}
		for k, n := range counts {
			st.Masked[k] += n
		}
	}
	raw := 0
	for k := range st.Masked {
		if _, hit := domain.FindSecret(k); hit {
			raw++
		}
	}
	if raw == 0 {
		t.Fatalf("前提が崩れています: 規則の語がどれも FindSecret に当たりません（直す理由が無くなった）")
	}
	got := headerStats(st)
	total := 0
	for k, n := range got.Masked {
		total += n
		if f, hit := domain.FindSecret(k); hit {
			t.Errorf("ヘッダのキー %q が FindSecret（%s）に当たります", k, f.Label)
		}
	}
	if total != len(samples) {
		t.Errorf("マスクの数の合計 %d（%d のはず）", total, len(samples))
	}
}

// 状態のファイルは API の URL とプロジェクトごとの置き場に書き、読み戻せる。版の違う・壊れたファイルは無いものとして扱う。
func TestPushStateFile(t *testing.T) {
	home := t.TempDir()
	e := env.FromMap(map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "APPDATA": filepath.Join(home, "AppData"),
		"LOOPTRACK_API_URL": "https://example.invalid/lt", "LOOPTRACK_PROJECT": "demo"})
	path := trStatePath(e, "claude-code", trTestSID)
	if path == "" || !strings.Contains(filepath.ToSlash(path), "/transcript-push/") || !strings.HasSuffix(path, "claude-code-"+trTestSID+".json") {
		t.Fatalf("状態のファイルのパス: %q", path)
	}
	if st := readPushState(path); st.TranscriptID != 0 || len(st.Sent) != 0 {
		t.Fatalf("無いファイルは空の状態のはず: %+v", st)
	}
	writePushState(path, &trPushState{Version: trStateVersion, TranscriptID: 9, Sent: map[string]trSent{"a.jsonl": {Length: 3, Tail: "x"}}})
	if st := readPushState(path); st.TranscriptID != 9 || st.Sent["a.jsonl"].Length != 3 {
		t.Fatalf("読み戻し: %+v", st)
	}
	if err := os.WriteFile(path, []byte(`{"version":99,"transcript_id":9,"sent":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if st := readPushState(path); st.TranscriptID != 0 {
		t.Fatalf("版の違うファイルを使っています: %+v", st)
	}
	if p := trStatePath(env.FromMap(map[string]string{"HOME": home}), "claude-code", trTestSID); p != "" {
		t.Fatalf("API の URL が無ければ状態のファイルを使わないはず: %q", p)
	}
}

// 記録の版は、版らしい形のときだけ送る（Claude は行の version、Codex は session_meta.payload.cli_version）。
func TestAgentVersion(t *testing.T) {
	claude := []byte(`{"type":"x"}` + "\n" + `{"version":"2.1.0","cwd":"/w"}` + "\n")
	if v := agentVersion("claude-code", claude); v != "2.1.0" {
		t.Errorf("Claude: %q", v)
	}
	codex := []byte(`{"type":"session_meta","payload":{"cli_version":"0.50.0"}}` + "\n")
	if v := agentVersion("codex", codex); v != "0.50.0" {
		t.Errorf("Codex: %q", v)
	}
	if v := agentVersion("claude-code", []byte(`{"version":"a b/c"}`+"\n")); v != "" {
		t.Errorf("版らしくない値を送ろうとしています: %q", v)
	}
	if v := agentVersion("codex", bytes.Repeat([]byte(`{"version":"1"}`+"\n"), 3)); v != "" {
		t.Errorf("Codex で session_meta 以外の版を使っています: %q", v)
	}
}

// 送った後に大きさが変わったサブエージェントの json（終わったときに "finished": true が足される）は、書き換えられたに数えない。
// push は終了コード 0 で、別の件数として出し、hook の分け方も「送った」になる（FailTranscriptRewritten にならない）。
// 対照（同じ回し方）: 送った後に短くなった jsonl と、大きさの変わった text（tool-results）は、これまでどおり
// 書き換えられたに数えて終了コード 1、hook の分け方は HookPushRewritten になる。
func TestPushJSONChangedAfterSendIsNotRewritten(t *testing.T) {
	main := jsonlLine("hello")
	sub := jsonlLine("sub") + jsonlLine("sub2")
	for _, c := range []struct {
		name    string
		change  func(t *testing.T, dir string)
		code    int
		changed bool // 「送った後に変わった json: 1」が出る
		outcome HookPushOutcome
	}{
		{"meta.json が 16 バイト増えた", func(t *testing.T, dir string) {
			trWrite(t, filepath.Join(dir, "subagents", "agent-a.meta.json"), `{"agentType":"x","finished":true}`)
		}, 0, true, HookPushSent},
		{"対照: jsonl が短くなった", func(t *testing.T, dir string) {
			trWrite(t, filepath.Join(dir, "subagents", "agent-a.jsonl"), jsonlLine("sub"))
		}, 1, false, HookPushRewritten},
		{"対照: text の大きさが変わった", func(t *testing.T, dir string) {
			trWrite(t, filepath.Join(dir, "tool-results", "t.txt"), "out\nmore\n")
		}, 1, false, HookPushRewritten},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &trFake{limit: 1 << 30, lengths: map[string]int64{}}
			srv := httptest.NewServer(f)
			defer srv.Close()
			vars, home, _ := trEnv(t, srv.URL)
			writeClaudeSession(t, home, main, sub)
			dir := filepath.Join(home, ".claude", "projects", "-work-proj", trTestSID)
			metaPath := filepath.Join(dir, "subagents", "agent-a.meta.json")
			trWrite(t, metaPath, `{"agentType":"x"}`)
			trWrite(t, filepath.Join(dir, "tool-results", "t.txt"), "out\n")
			// サブエージェントは終わっている（jsonl が 31 分前から更新されていない）ので、finished の無い meta.json も送る
			old := time.Now().Add(-31 * time.Minute)
			if err := os.Chtimes(filepath.Join(dir, "subagents", "agent-a.jsonl"), old, old); err != nil {
				t.Fatal(err)
			}
			code, stdout, stderr := runSession(t, vars, "transcript", "push", trTestSID, "--agent", "claude-code")
			metaKey := trTestSID + "/subagents/agent-a.meta.json"
			if code != 0 || f.lengths[metaKey] != int64(len(`{"agentType":"x"}`)) {
				t.Fatalf("前提が崩れています: 最初の push で meta.json を送れていません: 終了コード %d・長さ %v\nstdout %q\nstderr %q", code, f.lengths, stdout, stderr)
			}
			before, _ := os.Stat(metaPath)
			c.change(t, dir)
			if after, _ := os.Stat(metaPath); c.changed && after.Size()-before.Size() != 16 {
				t.Fatalf("前提が崩れています: meta.json の増分 %d バイト（16 のはず）", after.Size()-before.Size())
			}
			code, stdout, stderr = runSession(t, vars, "transcript", "push", trTestSID, "--agent", "claude-code")
			if code != c.code {
				t.Fatalf("終了コード %d（%d のはず）\nstdout %q\nstderr %q", code, c.code, stdout, stderr)
			}
			if got := strings.Contains(stdout, "送った後に変わった json: 1"); got != c.changed {
				t.Errorf("別の件数の行 %v（%v のはず）: %q", got, c.changed, stdout)
			}
			if got := strings.Contains(stderr, "送った後に元が短くなった・変わったファイルが 1 個"); got == c.changed {
				t.Errorf("書き換えられたの注意 %v（%v のはず）: %q", got, !c.changed, stderr)
			}
			_, dry, _ := runSession(t, vars, "transcript", "push", trTestSID, "--agent", "claude-code", "--dry-run")
			wantFiles := "書き換えられた 1"
			if c.changed {
				wantFiles = "書き換えられた 0"
			}
			if !strings.Contains(dry, wantFiles) {
				t.Errorf("dry-run の件数に %q がありません: %q", wantFiles, dry)
			}
			if r := PushTranscriptForHook(env.FromMap(vars), "claude-code", trTestSID); r.Outcome != c.outcome {
				t.Errorf("hook の分け方 %v（%v のはず）: %v", r.Outcome, c.outcome, r.Err)
			}
		})
	}
}

func trWrite(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
