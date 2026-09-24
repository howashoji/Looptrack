package usage

import (
	"fmt"
	"testing"
	"time"
)

// 区間の消費と帰属の計算。

// 差分は会話ごとに計算したまま、表示の並びは時刻順（同時刻は ID 順）。
func TestSortByTime(t *testing.T) {
	snaps := []Snapshot{
		// 会話 "a" は会話 ID の並びでは先頭だが、時刻は後
		{ID: 10, ConversationID: "a", Trigger: TriggerIssueOp, IssueID: 1, IssueStatus: "Todo", At: at(50), Counters: c(1000, 0, 1)},
		{ID: 11, ConversationID: "a", Trigger: TriggerIssueOp, IssueID: 1, IssueStatus: "Todo", At: at(55), Counters: c(1200, 0, 2)},
		{ID: 1, ConversationID: "b", Trigger: TriggerIssueOp, IssueID: 1, IssueStatus: "Todo", At: at(1), Counters: c(100, 0, 1)},
		{ID: 3, ConversationID: "b", Trigger: TriggerIssueOp, IssueID: 1, IssueStatus: "Todo", At: at(20), Counters: c(300, 0, 2)},
		// 同時刻は ID 順
		{ID: 2, ConversationID: "c", Trigger: TriggerIssueOp, IssueID: 1, IssueStatus: "Todo", At: at(20), Counters: c(40, 0, 1)},
	}
	got := Stages(snaps, closed)
	if got[0].Snapshot.ConversationID != "a" {
		t.Fatalf("前提: Stages は会話 ID 順のはず: %+v", got[0].Snapshot)
	}
	deltas := map[int64]int64{}
	for _, s := range got {
		deltas[s.Snapshot.ID] = s.Delta.Main.Input
	}
	SortByTime(got)
	var ids []int64
	for _, s := range got {
		ids = append(ids, s.Snapshot.ID)
		if s.Delta.Main.Input != deltas[s.Snapshot.ID] {
			t.Errorf("ID %d の差分が並べ替えで変わった: %d, want %d", s.Snapshot.ID, s.Delta.Main.Input, deltas[s.Snapshot.ID])
		}
	}
	if want := []int64{1, 2, 3, 10, 11}; fmt.Sprint(ids) != fmt.Sprint(want) {
		t.Errorf("並び = %v, want %v", ids, want)
	}
	// 差分は会話ごと（b の 2 件目は 300 - 100、a の 2 件目は 1200 - 1000）
	if deltas[3] != 200 || deltas[11] != 200 || deltas[10] != 1000 || deltas[2] != 40 {
		t.Errorf("差分: %v", deltas)
	}
}

var closed = []string{"Done", "Canceled"}

func c(in, out int64, responses int64) Counters {
	return Counters{Main: Tokens{Input: in, Output: out}, Responses: responses}
}

func at(min int) time.Time { return time.Date(2026, 9, 18, 10, min, 0, 0, time.UTC) }

func TestStagesAttribution(t *testing.T) {
	snaps := []Snapshot{
		// 並びは逆順で渡す（累計の昇順に並べ直されること）
		{ID: 6, ConversationID: "c1", Trigger: TriggerSessionEnd, At: at(50), Counters: c(900, 90, 9)},
		{ID: 5, ConversationID: "c1", Trigger: TriggerIssueOp, IssueID: 2, IssueDisplayID: "REQ-0002", Op: "create", IssueStatus: "Todo", At: at(40), Counters: c(700, 70, 7)},
		{ID: 4, ConversationID: "c1", Trigger: TriggerStop, At: at(30), Counters: c(600, 60, 6)},
		{ID: 3, ConversationID: "c1", Trigger: TriggerIssueOp, IssueID: 1, IssueDisplayID: "REQ-0001", Op: "status", IssueStatus: "Done", At: at(20), Counters: c(500, 50, 5)},
		{ID: 2, ConversationID: "c1", Trigger: TriggerStop, At: at(15), Counters: c(300, 30, 3)},
		{ID: 1, ConversationID: "c1", Trigger: TriggerIssueOp, IssueID: 1, IssueDisplayID: "REQ-0001", Op: "create", IssueStatus: "In Progress", At: at(10), Counters: c(100, 10, 1)},
	}
	got := Stages(snaps, closed)
	want := []struct {
		id    int64
		in    int64
		issue int64
	}{
		{1, 100, 1}, // 起票の前の調査は起票したイシューへ
		{2, 200, 1}, // stop で閉じた区間は、直前に操作した未クローズのイシューへ
		{3, 200, 1}, // クローズの操作で閉じた区間はそのイシューへ
		{4, 100, 0}, // クローズ後の stop は未帰属
		{5, 100, 2}, // 次の起票の前の調査は次のイシューへ
		{6, 200, 2},
	}
	if len(got) != len(want) {
		t.Fatalf("区間数 = %d", len(got))
	}
	for i, w := range want {
		g := got[i]
		if g.Snapshot.ID != w.id || g.Delta.Main.Input != w.in || g.IssueID != w.issue || g.Inconsist {
			t.Errorf("%d: id=%d in=%d issue=%d inconsist=%v, want %+v", i, g.Snapshot.ID, g.Delta.Main.Input, g.IssueID, g.Inconsist, w)
		}
	}
	if total := Sum(got); total.Main.Input != 900 || total.Main.Output != 90 || total.Responses != 9 {
		t.Errorf("区間の合計が最後の累計と一致しない: %+v", total)
	}
}

// 再開でセッション ID が変わっても、会話 ID が同じなら二重に数えない。別の会話とは混ざらない。
func TestStagesResumeAndConversations(t *testing.T) {
	snaps := []Snapshot{
		{ID: 1, ConversationID: "c1", SessionID: "s1", Trigger: TriggerIssueOp, IssueID: 1, IssueStatus: "Todo", At: at(1), Counters: c(100, 0, 1)},
		{ID: 2, ConversationID: "c1", SessionID: "s1", Trigger: TriggerSessionEnd, At: at(2), Counters: c(150, 0, 2)},
		// 再開（履歴を複製して持つので累計は続きから）
		{ID: 3, ConversationID: "c1", SessionID: "s2", Trigger: TriggerIssueOp, IssueID: 1, IssueStatus: "Done", At: at(30), Counters: c(400, 0, 5)},
		{ID: 4, ConversationID: "c2", SessionID: "s3", Trigger: TriggerStop, At: at(5), Counters: c(70, 0, 1)},
	}
	got := Stages(snaps, closed)
	if total := Sum(got); total.Main.Input != 470 {
		t.Errorf("合計 = %d, want 400 + 70", total.Main.Input)
	}
	var issue1, unattributed int64
	for _, s := range got {
		if s.IssueID == 1 {
			issue1 += s.Delta.Main.Input
		} else {
			unattributed += s.Delta.Main.Input
		}
	}
	if issue1 != 400 || unattributed != 70 {
		t.Errorf("イシュー 1 = %d, 未帰属 = %d", issue1, unattributed)
	}
}

// 1 回の応答で複数のイシューを操作したとき（累計が同じ）、2 件目以降の差は 0。
func TestStagesSameCumulative(t *testing.T) {
	snaps := []Snapshot{
		{ID: 1, ConversationID: "c1", Trigger: TriggerIssueOp, IssueID: 1, IssueStatus: "Todo", At: at(1), Counters: c(100, 10, 1)},
		{ID: 2, ConversationID: "c1", Trigger: TriggerIssueOp, IssueID: 2, IssueStatus: "Todo", At: at(1), Counters: c(100, 10, 1)},
	}
	got := Stages(snaps, closed)
	if got[0].IssueID != 1 || got[0].Delta.Total() != 110 || got[1].IssueID != 2 || got[1].Delta.Total() != 0 {
		t.Errorf("%+v", got)
	}
}

// 累計が巻き戻った行（別の枝）は 0 にして印を付け、その後の差は最大値から取る。
func TestStagesInconsistent(t *testing.T) {
	snaps := []Snapshot{
		{ID: 1, ConversationID: "c1", Trigger: TriggerStop, At: at(1), Counters: Counters{Main: Tokens{Input: 100, Output: 50}}},
		{ID: 2, ConversationID: "c1", Trigger: TriggerStop, At: at(2), Counters: Counters{Main: Tokens{Input: 160, Output: 20}}},
		{ID: 3, ConversationID: "c1", Trigger: TriggerStop, At: at(3), Counters: Counters{Main: Tokens{Input: 200, Output: 60}}},
	}
	got := Stages(snaps, closed)
	if !got[1].Inconsist || got[1].Delta.Main.Input != 60 || got[1].Delta.Main.Output != 0 {
		t.Errorf("巻き戻り: %+v", got[1])
	}
	if got[2].Inconsist || got[2].Delta.Main.Input != 40 || got[2].Delta.Main.Output != 10 {
		t.Errorf("巻き戻りの次: %+v", got[2])
	}
}

// 対象外の指示が 1 件でもあれば、その会話の全区間に印が付く。
func TestStagesExcluded(t *testing.T) {
	snaps := []Snapshot{
		{ID: 1, ConversationID: "c1", Trigger: TriggerStop, At: at(1), Counters: c(10, 0, 1)},
		{ID: 2, ConversationID: "c1", Trigger: TriggerStop, At: at(2), Counters: c(20, 0, 2), Excluded: true},
		{ID: 3, ConversationID: "c2", Trigger: TriggerStop, At: at(3), Counters: c(5, 0, 1)},
	}
	got := Stages(snaps, closed)
	if !got[0].Excluded || !got[1].Excluded || got[2].Excluded {
		t.Errorf("%+v", got)
	}
}
