package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// 写しの ID は記録ごとに決まり（2 回目の pull が同じ場所を見る）、元の ID とも、別の記録の ID とも違う。
func TestForkedID(t *testing.T) {
	a := forkedID("claude-code", trTestSID, 7, "2026-10-25T00:00:00Z")
	if a != forkedID("claude-code", trTestSID, 7, "2026-10-25T00:00:00Z") {
		t.Fatal("同じ記録で ID が変わります")
	}
	if !uuidV4.MatchString(a) || a == trTestSID {
		t.Fatalf("UUID の形でないか、元の ID と同じです: %s", a)
	}
	for _, b := range []string{forkedID("claude-code", trTestSID, 8, "2026-10-25T00:00:00Z"), forkedID("codex", trTestSID, 7, "2026-10-25T00:00:00Z"),
		forkedID("claude-code", trTestSID, 7, "2026-10-25T00:00:01Z")} {
		if b == a {
			t.Fatalf("別の記録で同じ ID になりました: %s", b)
		}
	}
}

// 使うのは最後の消去より後のチャンクだけ。消去済みしか無ければ purged。
func TestLiveChunks(t *testing.T) {
	live, purged := liveChunks([]trChunk{{Seq: 3}, {Seq: 1, Purged: true}, {Seq: 2, Purged: true}})
	if len(live) != 1 || live[0].Seq != 3 || !purged {
		t.Fatalf("消去の後に送り直したチャンクだけのはず: %+v %v", live, purged)
	}
	live, purged = liveChunks([]trChunk{{Seq: 1, Purged: true}})
	if len(live) != 0 || !purged {
		t.Fatalf("消去済みのはず: %+v %v", live, purged)
	}
	live, purged = liveChunks([]trChunk{{Seq: 2}, {Seq: 1}})
	if len(live) != 2 || live[0].Seq != 1 || purged {
		t.Fatalf("seq の順で全部のはず: %+v %v", live, purged)
	}
}

// Claude Code: 名前と jsonl・json の中の元の ID を新しい ID にし、text（tool-results）は中身を変えない。ほかのセッションのファイルは置かない。
// Codex（要実測）: 名前に元の ID を含む rollout だけ名前を変え、history_base の別のスレッドの rollout は元の名前のまま。
func TestForkFiles(t *testing.T) {
	const newID = "11111111-1111-4111-8111-111111111111"
	files := map[string][]byte{
		trTestSID + ".jsonl":                   []byte(`{"sessionId":"` + trTestSID + `","p":"/s/` + trTestSID + `/tool-results/t.txt"}` + "\n"),
		trTestSID + "/subagents/a.meta.json":   []byte(`{"parent":"` + trTestSID + `"}`),
		trTestSID + "/tool-results/t.txt":      []byte("id " + trTestSID + "\n"),
		trTestSID + "/subagents/agent-a.jsonl": []byte(`{"sessionId":"` + trTestSID + `"}` + "\n"),
	}
	placed, mainRel, err := forkFiles(trAgentClaude, trTestSID, newID, files)
	if err != nil || mainRel != newID+".jsonl" || len(placed) != 4 {
		t.Fatalf("forkFiles: %v %q %d", err, mainRel, len(placed))
	}
	for _, p := range placed {
		if !strings.HasPrefix(p.rel, newID) {
			t.Errorf("名前が新しい ID になっていません: %s", p.rel)
		}
		has := strings.Contains(string(p.data), trTestSID)
		if strings.Contains(p.rel, "/tool-results/") != has {
			t.Errorf("%s: 元の ID が残っている=%v（tool-results だけ残るはず）: %s", p.rel, has, p.data)
		}
	}
	if _, _, err := forkFiles(trAgentClaude, trTestSID, newID, map[string][]byte{"other.jsonl": nil}); err == nil {
		t.Fatal("ほかのセッションのファイルを置こうとしています")
	}
	const tid = "019a0000-0000-7000-8000-0000000000d1"
	const base = "019a0000-0000-7000-8000-0000000000d0"
	codex := map[string][]byte{
		"2026/10/25/rollout-2026-10-25T10-00-00-" + tid + ".jsonl":  []byte(`{"type":"session_meta","payload":{"id":"` + tid + `"}}` + "\n"),
		"2026/10/24/rollout-2026-10-24T10-00-00-" + base + ".jsonl": []byte(`{"type":"session_meta","payload":{"id":"` + base + `"}}` + "\n"),
	}
	placed, mainRel, err = forkFiles(trAgentCodex, tid, newID, codex)
	if err != nil || mainRel != "2026/10/25/rollout-2026-10-25T10-00-00-"+newID+".jsonl" {
		t.Fatalf("Codex: %v %q", err, mainRel)
	}
	for _, p := range placed {
		if strings.Contains(p.rel, base) && !strings.Contains(string(p.data), base) {
			t.Errorf("history_base の rollout の中身を変えています: %s", p.data)
		}
		if strings.Contains(p.rel, tid) || strings.Contains(string(p.data), tid) {
			t.Errorf("元のスレッド ID が残っています: %s %s", p.rel, p.data)
		}
	}
}

// 置く先の比べ方: 全部同じなら置き済み・1 つでも違えば止める・一部だけ（同じ中身）なら残りを書く。書くのは無いファイルだけ。
func TestComparePlacedAndWrite(t *testing.T) {
	dir := t.TempDir()
	placed := []trPlaced{{rel: "a.jsonl", data: []byte("a\n")}, {rel: "a/sub/b.jsonl", data: []byte("b\n")}}
	if st, _ := comparePlaced(dir, placed); st != placedNone {
		t.Fatalf("空の置き場: %v", st)
	}
	if n, err := writePlaced(dir, placed[:1]); err != nil || n != 1 {
		t.Fatalf("書き込み: %v %d", err, n)
	}
	if st, _ := comparePlaced(dir, placed); st != placedNone {
		t.Fatalf("一部だけある: %v", st)
	}
	if n, err := writePlaced(dir, placed); err != nil || n != 1 {
		t.Fatalf("残りの書き込み: %v %d（1 つのはず）", err, n)
	}
	if st, _ := comparePlaced(dir, placed); st != placedSame {
		t.Fatalf("全部同じ: %v", st)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.jsonl"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if st, where := comparePlaced(dir, placed); st != placedDiffer || where != filepath.Join(dir, "a.jsonl") {
		t.Fatalf("中身が違う: %v %s", st, where)
	}
}

// pull は AI を起動しない（再開のコマンドを表示するだけ）。transcript*.go が外部のコマンドを起動する呼び出しを持たないことを確かめる
// （語はこのファイル自身が当たらないよう分けて組む）。対照: 同じ走査で session.go（git を呼ぶ）には当たる。
func TestTranscriptNeverExecutes(t *testing.T) {
	files, err := filepath.Glob("transcript*.go")
	if err != nil || len(files) < 3 {
		t.Fatalf("前提が崩れています: transcript*.go が見つかりません（%v %v）", files, err)
	}
	re := regexp.MustCompile(`exec` + `\.Command`)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if re.Match(b) {
			t.Errorf("%s が外部のコマンドを起動しています", f)
		}
	}
	b, err := os.ReadFile("session.go")
	if err != nil || !re.Match(b) {
		t.Fatalf("前提が崩れています: session.go に外部のコマンドの起動が見つかりません（走査が死んでいる）")
	}
}
