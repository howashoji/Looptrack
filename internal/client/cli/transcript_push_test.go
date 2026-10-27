package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	if !plans[0].rewritten || !plans[1].rewritten {
		t.Fatalf("送った後に大きさの変わった json と text を「書き換えられた」にしていません")
	}
	empty := trSrc(trTestSID+"/subagents/b.meta.json", transcript.KindJSON, "  ")
	plans, rejects := trPlanAll([]*trSource{empty}, map[string]int64{}, none, trTestOpt)
	if len(rejects) != 0 || plans[0].res != nil || plans[0].rewritten {
		t.Fatalf("書きかけ（空）の json は次の回に回すはず: %v %+v", rejects, plans[0])
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
