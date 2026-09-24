package domain

import (
	"reflect"
	"testing"
)

func TestClosableRequirements(t *testing.T) {
	iss := func(id, typ, status string, traces ...string) Issue {
		return Issue{ID: id, Type: typ, Status: status, Title: "t" + id, Traces: traces}
	}
	set := NewSet([]Issue{
		iss("R-1", "requirement", "Todo"),        // 下位が全部 Done → 対象
		iss("R-2", "requirement", "In Progress"), // Done と Canceled が混在 → 対象
		iss("R-3", "requirement", "Todo"),        // 下位が全部 Canceled → 対象外
		iss("R-4", "requirement", "Todo"),        // 下位に未完了がある → 対象外
		iss("R-5", "requirement", "Done"),        // 要件が閉じている → 対象外
		iss("R-6", "requirement", "Todo"),        // 下位が無い → 対象外
		iss("R-7", "requirement", "In Review"),   // 人の判断待ち → 対象外（② に出る）
		iss("R-8", "requirement", "Backlog"),     // Backlog も開いている → 対象
		iss("D-1", "design", "Done", "R-1"),
		iss("T-1", "task", "Done", "r-1"), // 大文字小文字を区別しない
		iss("T-2", "task", "Done", "R-2"),
		iss("T-3", "task", "Canceled", "R-2"),
		iss("T-4", "task", "Canceled", "R-3"),
		iss("T-5", "task", "Done", "R-4"),
		iss("T-6", "task", "In Review", "R-4"),
		iss("T-7", "task", "Done", "R-5"),
		iss("T-8", "task", "Done", "R-7"),
		iss("T-9", "bug", "Done", "R-8", "R-1"),
		iss("X-1", "task", "Done", "NOPE-1"),
	})
	got := set.ClosableRequirements()
	var ids []string
	for _, c := range got {
		ids = append(ids, c.Requirement.ID)
	}
	if !reflect.DeepEqual(ids, []string{"R-1", "R-2", "R-8"}) {
		t.Fatalf("対象: %v", ids)
	}
	if c := got[0]; c.Done != 3 || c.Canceled != 0 || !reflect.DeepEqual(c.Children, []string{"D-1", "T-1", "T-9"}) {
		t.Errorf("R-1: %+v", c)
	}
	if c := got[1]; c.Done != 1 || c.Canceled != 1 {
		t.Errorf("R-2: %+v", c)
	}

	// 閉じたイシューが traces で指す要件のうち、それで下位がすべて完了したもの
	if got := set.ClosableFor(iss("T-9", "bug", "Done", "R-8", "R-1", "R-4")); len(got) != 2 || got[0].Requirement.ID != "R-8" || got[1].Requirement.ID != "R-1" {
		t.Errorf("ClosableFor: %+v", got)
	}
	if got := set.ClosableFor(iss("T-4", "task", "Canceled", "R-3")); len(got) != 0 {
		t.Errorf("全部 Canceled は対象外: %+v", got)
	}
}
