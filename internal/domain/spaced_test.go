package domain

import (
	"strings"
	"testing"
)

// 空白区切りで 1 要素に入った ID の分割（既存データの補正用）。
func TestSplitSpaced(t *testing.T) {
	for _, c := range []struct {
		in      []string
		want    string
		changed bool
	}{
		{[]string{}, "", false},
		{[]string{"FR-1", "FR-2"}, "FR-1|FR-2", false},
		{[]string{"FR-1", "FR-1"}, "FR-1|FR-1", false}, // 空白が無ければ触らない（重複もそのまま）
		{[]string{"NFR-SEC-003 NFR-SEC-004"}, "NFR-SEC-003|NFR-SEC-004", true},
		{[]string{"A  B\tC", "D"}, "A|B|C|D", true},
		{[]string{"A　B"}, "A|B", true}, // 全角空白
		{[]string{"A B", "B", "C A"}, "A|B|C", true},
	} {
		got, changed := SplitSpaced(c.in)
		if strings.Join(got, "|") != c.want || changed != c.changed {
			t.Errorf("SplitSpaced(%q) = %q %v, want %q %v", c.in, got, changed, c.want, c.changed)
		}
	}
	for key, want := range map[string]bool{"blocked_by": true, "traces": true, "refs": true, "labels": false, "parent": false} {
		if IsIDListField(key) != want {
			t.Errorf("IsIDListField(%s) = %v", key, !want)
		}
	}
}
