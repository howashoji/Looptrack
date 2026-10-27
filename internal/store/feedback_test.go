package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/mdformat"
)

// TestPendingFeedbacksDigestIsNotAResponse は、セッション要約（先頭語 digest）のコメントが
// 未応答のフィードバックを「応答済み」にしないことを、実データで固定する（MySQL でも SQLite でも走る）。
// 要約は hook が自動で付けうる記録で、人の応答ではない。
// 同じテストの中に対照を置く: ふつうのコメントや判断・差し戻しは、これまでどおり応答になる。
func TestPendingFeedbacksDigestIsNotAResponse(t *testing.T) {
	db := usageTestDB(t)
	ctx := context.Background()
	pid, err := CreateProject(ctx, db, Project{Slug: "req", Prefix: "REQ", Width: 4, Name: "req"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		comments []string
		pending  []int // 未応答として残るフィードバックの seq
	}{
		// 要約だけが続く形は未応答のまま
		{"日本語の要約", []string{"フィードバック: A", "セッション要約: 作業の文脈"}, []int{1}},
		{"全角コロンの要約", []string{"フィードバック: A", "セッション要約：作業の文脈"}, []int{1}},
		{"英語の要約", []string{"Feedback: A", "Session digest: context of the work"}, []int{1}},
		{"英語の要約（大小違い）", []string{"feedback: A", "SESSION DIGEST: context"}, []int{1}},
		{"要約が重なっても残る", []string{"フィードバック: A", "セッション要約: 1", "Session digest: 2"}, []int{1}},
		{"要約をまたいだ後のフィードバックも残る", []string{"フィードバック: A", "セッション要約: 1", "Feedback: B"}, []int{1, 3}},
		// 対照: 応答になるもの
		{"対照: ふつうのコメント", []string{"フィードバック: A", "REQ-0090 で起票した"}, nil},
		{"対照: 英語のふつうのコメント", []string{"Feedback: A", "Filed a follow-up"}, nil},
		{"対照: 判断", []string{"フィードバック: A", "判断: このまま"}, nil},
		{"対照: 差し戻し", []string{"フィードバック: A", "Changes requested: fix it"}, nil},
		{"対照: 要約の後のふつうのコメント", []string{"フィードバック: A", "セッション要約: 1", "対応した"}, nil},
		// 先頭語でないものは要約ではない（応答になる）
		{"対照: 先頭が空白の要約は先頭語でない", []string{"フィードバック: A", " セッション要約: x"}, nil},
		{"対照: 語の続きは要約でない", []string{"Feedback: A", "Session digests: x"}, nil},
	}
	at := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	want := map[string][]int{}
	for i, c := range cases {
		n := i + 1
		id := fmt.Sprintf("REQ-%04d", n)
		doc := &mdformat.Document{Front: []mdformat.Field{{Key: "id", Value: id}, {Key: "status", Value: "Todo"}}, GapNL: 2, TrailNL: 1}
		iid, err := InsertDocument(ctx, db, pid, n, id+".md", doc, "cli")
		if err != nil {
			t.Fatal(err)
		}
		for j, text := range c.comments {
			if _, err := db.ExecContext(ctx, `INSERT INTO comments (issue_id, seq, ts, content, created_at, via)
VALUES (?, ?, ?, ?, ?, 'cli')`, iid, j+1, "2026-10-25 10:00", text, at.Add(time.Duration(j)*time.Minute)); err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
		}
		if len(c.pending) > 0 {
			want[id] = c.pending
		}
	}
	fb, err := PendingFeedbacks(ctx, db, pid)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]int{}
	for _, f := range fb {
		got[f.DisplayID] = append(got[f.DisplayID], f.Seq)
	}
	for i, c := range cases {
		id := fmt.Sprintf("REQ-%04d", i+1)
		g, w := got[id], want[id]
		sort.Ints(g)
		if fmt.Sprint(g) != fmt.Sprint(w) {
			t.Errorf("%s (%s): 未応答の seq = %v（期待 %v）", c.name, id, g, w)
		}
	}
	// 対照が空振りでないこと: 未応答が残る側と応答済みの側の両方が実際に出ている。
	if len(want) == 0 || len(want) == len(cases) {
		t.Fatalf("前提が崩れています: 未応答の期待 %d 件 / 全 %d 件（残る側と外れる側の両方が要る）", len(want), len(cases))
	}
}

// TestLeadCondFromDomain は、先頭語の条件に綴りを domain から取っていること（文面を store に写していないこと）と、
// 要約・フィードバックの両方に日本語（半角・全角のコロン）と英語の綴りが入ることを固定する（DB 不要）。
func TestLeadCondFromDomain(t *testing.T) {
	d := leadCond("r", "digest")
	for _, w := range []string{"'セッション要約:%'", "'セッション要約：%'", "= 'session digest:'", "= 'SESSION DIGEST:'", "LEFT(r.content, 15)"} {
		if !strings.Contains(d, w) {
			t.Errorf("digest の条件に %s が無い: %s", w, d)
		}
	}
	f := leadCond("c", "feedback")
	for _, w := range []string{"'フィードバック:%'", "'フィードバック：%'", "= 'feedback:'", "= 'FEEDBACK:'", "LEFT(c.content, 9)"} {
		if !strings.Contains(f, w) {
			t.Errorf("feedback の条件に %s が無い: %s", w, f)
		}
	}
}
