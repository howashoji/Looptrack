package usage

import (
	"regexp"
	"testing"
	"time"
)

// レポート用の集計（期間・切り口・対象外・未帰属）。

func TestBuildReport(t *testing.T) {
	snaps := []Snapshot{
		// c1: 期間の前に起票（100）→ 期間内にコメント（+200）→ stop（+50。直前のイシューへ）→ 期間の後に状態変更（+300）
		{ID: 1, ConversationID: "c1", SessionID: "s1", Client: "claude-code", Trigger: TriggerIssueOp, IssueID: 1, IssueDisplayID: "REQ-0001", Op: "create", IssueStatus: "Todo", At: at(1), Counters: c(100, 0, 1)},
		{ID: 2, ConversationID: "c1", SessionID: "s1", Client: "claude-code", Trigger: TriggerIssueOp, IssueID: 1, IssueDisplayID: "REQ-0001", Op: "comment", IssueStatus: "Todo", At: at(12), Counters: c(300, 0, 2)},
		{ID: 3, ConversationID: "c1", SessionID: "s1b", Client: "claude-code", Trigger: TriggerStop, At: at(15), Counters: c(350, 0, 3)},
		{ID: 4, ConversationID: "c1", SessionID: "s1b", Client: "claude-code", Trigger: TriggerIssueOp, IssueID: 1, IssueDisplayID: "REQ-0001", Op: "status", IssueStatus: "Done", At: at(40), Counters: c(650, 0, 4)},
		// c2: 期間内。REQ-0002 の起票（70）と、クローズ済みの後の session_end（未帰属 30）
		{ID: 5, ConversationID: "c2", SessionID: "s2", Client: "codex", Trigger: TriggerIssueOp, IssueID: 2, IssueDisplayID: "REQ-0002", Op: "create", IssueStatus: "Done", At: at(20), Counters: c(70, 0, 1)},
		{ID: 6, ConversationID: "c2", SessionID: "s2", Client: "codex", Trigger: TriggerSessionEnd, At: at(25), Counters: c(100, 0, 2)},
		// c3: 対象外の会話（期間内 500）
		{ID: 7, ConversationID: "c3", SessionID: "s3", Client: "claude-code", Trigger: TriggerIssueOp, IssueID: 2, IssueDisplayID: "REQ-0002", Op: "comment", At: at(21), Counters: c(500, 0, 1), Excluded: true},
	}
	info := map[string]IssueInfo{
		"REQ-0001": {ID: "REQ-0001", Title: "一つ目", Type: "task", Status: "Done", Labels: []string{"api", "web"}},
		"REQ-0002": {ID: "REQ-0002", Title: "二つ目", Type: "bug", Status: "Done"},
	}
	rep := BuildReport(Stages(snaps, closed), at(10), at(30), func(id string) (IssueInfo, bool) { it, ok := info[id]; return it, ok }, nil)

	if rep.TotalTokens != 350 || rep.Stages != 4 {
		t.Errorf("合計 = %d（区間 %d）, want 350（4）", rep.TotalTokens, rep.Stages)
	}
	if rep.ExcludedTokens != 500 || rep.ExcludedStages != 1 || len(rep.ExcludedConversations) != 1 || rep.ExcludedConversations[0] != "c3" {
		t.Errorf("対象外: %d %d %v", rep.ExcludedTokens, rep.ExcludedStages, rep.ExcludedConversations)
	}
	if rep.Unattributed.TotalTokens != 30 || rep.Unattributed.Stages != 1 {
		t.Errorf("未帰属: %+v", rep.Unattributed)
	}
	if rep.FirstAt == nil || !rep.FirstAt.Equal(at(12)) || !rep.LastAt.Equal(at(25)) {
		t.Errorf("first / last: %v %v", rep.FirstAt, rep.LastAt)
	}
	if len(rep.ByIssue) != 2 || rep.ByIssue[0].ID != "REQ-0001" || rep.ByIssue[0].TotalTokens != 250 || rep.ByIssue[0].Stages != 2 ||
		rep.ByIssue[1].ID != "REQ-0002" || rep.ByIssue[1].TotalTokens != 70 || rep.ByIssue[1].Type != "bug" {
		t.Errorf("イシュー別: %+v", rep.ByIssue)
	}
	wantGroups := func(name string, got []Group, want map[string]int64) {
		t.Helper()
		if len(got) != len(want) {
			t.Errorf("%s: %+v", name, got)
			return
		}
		for _, g := range got {
			if want[g.Key] != g.TotalTokens {
				t.Errorf("%s[%q] = %d, want %d", name, g.Key, g.TotalTokens, want[g.Key])
			}
		}
	}
	wantGroups("ラベル別", rep.ByLabel, map[string]int64{"api": 250, "web": 250, "": 70})
	wantGroups("種類別", rep.ByType, map[string]int64{"task": 250, "bug": 70})
	wantGroups("段階別", rep.ByStage, map[string]int64{"comment": 200, "stop": 50, "create": 70, "session_end": 30})
	wantGroups("AI 別", rep.ByClient, map[string]int64{"claude-code": 250, "codex": 100})
	wantGroups("案件別（正規表現なし）", rep.ByCase, map[string]int64{"": 350})
	if rep.CasePattern != "" {
		t.Errorf("case_pattern = %q", rep.CasePattern)
	}
	if len(rep.Conversations) != 2 {
		t.Fatalf("会話別: %+v", rep.Conversations)
	}
	c1, c2 := rep.Conversations[0], rep.Conversations[1]
	if c1.ConversationID != "c1" || c1.TotalTokens != 250 || len(c1.Sessions) != 2 || len(c1.Issues) != 1 {
		t.Errorf("c1: %+v", c1)
	}
	if c2.ConversationID != "c2" || c2.TotalTokens != 100 || c2.Unattributed != 30 || c2.Client != "codex" {
		t.Errorf("c2: %+v", c2)
	}

	// 下限なし（from がゼロ値）は期間の前の起票も入る。to ちょうどの区間は入らない
	all := BuildReport(Stages(snaps, closed), time.Time{}, at(40), func(string) (IssueInfo, bool) { return IssueInfo{}, false }, nil)
	if all.TotalTokens != 450 {
		t.Errorf("下限なし = %d, want 450", all.TotalTokens)
	}
	if len(all.ByIssue) != 2 || all.ByIssue[0].Title != "" {
		t.Errorf("イシューが引けないときは ID だけ: %+v", all.ByIssue)
	}
}

// 案件別。帰属先イシューのラベルが先、無ければブランチ名。1 区間を 1 案件に数え、合計は全体と一致する。
func TestBuildReportByCase(t *testing.T) {
	snaps := []Snapshot{
		// c1（ブランチ CASE-7）: APP-DEV-0001（ラベルに案件 CASE-101）への操作 100 → stop 50（直前のイシューへ）
		{ID: 1, ConversationID: "c1", Trigger: TriggerIssueOp, IssueID: 1, IssueDisplayID: "APP-DEV-0001", Op: "comment", IssueStatus: "In Progress", At: at(11), Counters: c(100, 0, 1), Branch: "CASE-7"},
		{ID: 2, ConversationID: "c1", Trigger: TriggerStop, At: at(12), Counters: c(150, 0, 2), Branch: "CASE-7"},
		// c2: APP-DEV-0002（案件ラベルなし）への操作 40 → イシューが無いのでブランチ feature/CASE-7-fix の CASE-7
		{ID: 3, ConversationID: "c2", Trigger: TriggerIssueOp, IssueID: 2, IssueDisplayID: "APP-DEV-0002", Op: "create", IssueStatus: "Todo", At: at(13), Counters: c(40, 0, 1), Branch: "feature/CASE-7-fix"},
		// c3: 取り込み（未帰属）。ブランチ CASE-7 の 200 と、master の 60（案件なし）
		{ID: 4, ConversationID: "c3", Trigger: TriggerImport, At: at(14), Counters: c(200, 0, 1), Branch: "CASE-7"},
		{ID: 5, ConversationID: "c4", Trigger: TriggerImport, At: at(15), Counters: c(60, 0, 1), Branch: "master"},
		// c5: ブランチなし・未帰属 5
		{ID: 6, ConversationID: "c5", Trigger: TriggerStop, At: at(16), Counters: c(5, 0, 1)},
	}
	info := map[string]IssueInfo{
		"APP-DEV-0001": {ID: "APP-DEV-0001", Type: "task", Labels: []string{"R-01③", "CASE-101 保守リスク再監査 対応", "CASE-102"}},
		"APP-DEV-0002": {ID: "APP-DEV-0002", Type: "bug", Labels: []string{"Wave2"}},
	}
	lookup := func(id string) (IssueInfo, bool) { it, ok := info[id]; return it, ok }
	rep := BuildReport(Stages(snaps, closed), at(10), at(30), lookup, regexp.MustCompile(`CASE-\d+`))
	if rep.TotalTokens != 455 {
		t.Fatalf("合計 = %d, want 455", rep.TotalTokens)
	}
	var sum int64
	got := map[string]Group{}
	for _, g := range rep.ByCase {
		sum += g.TotalTokens
		got[g.Key] = g
	}
	if sum != rep.TotalTokens {
		t.Errorf("案件別の合計 %d != 全体 %d", sum, rep.TotalTokens)
	}
	// ラベル（最初に当たったもの）がブランチより優先。ラベルは一致した部分だけを案件名にする
	if g := got["CASE-101"]; g.TotalTokens != 150 || g.Stages != 2 || g.Issues != 1 {
		t.Errorf("CASE-101: %+v", g)
	}
	if g := got["CASE-7"]; g.TotalTokens != 240 || g.Stages != 2 || g.Issues != 1 {
		t.Errorf("CASE-7: %+v", g)
	}
	if g := got[""]; g.TotalTokens != 65 || g.Stages != 2 || g.Issues != 0 {
		t.Errorf("案件なし: %+v", g)
	}
	if len(rep.ByCase) != 3 || rep.ByCase[0].Key != "CASE-7" || rep.CasePattern != `CASE-\d+` {
		t.Errorf("並び・正規表現: %+v %q", rep.ByCase, rep.CasePattern)
	}

	// 捕捉グループがあれば 1 番目を案件名にする
	if k, src := CaseOf(regexp.MustCompile(`^(?:feature/)?(CASE-\d+)`), nil, "feature/CASE-7-fix"); k != "CASE-7" || src != "branch" {
		t.Errorf("捕捉グループ: %q %q", k, src)
	}
	if k, src := CaseOf(nil, []string{"CASE-1"}, "CASE-2"); k != "" || src != "" {
		t.Errorf("正規表現なし: %q %q", k, src)
	}
}
