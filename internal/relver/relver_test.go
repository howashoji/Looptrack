package relver

import "testing"

func TestCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
		ok   bool
	}{
		{"v1.0.0", "v1.0.0", 0, true},
		{"1.0.0", "v1.0.0", 0, true},
		{"v1.0.0", "v1.0.1", -1, true},
		{"v1.2.0", "v1.10.0", -1, true},
		{"v2.0.0", "v1.99.99", 1, true},
		{"v1.0.0-rc1", "v1.0.0", -1, true},
		{"v1.0.0-rc1", "v1.0.0-rc2", -1, true},
		{"v1.0.0-alpha", "v1.0.0-alpha.1", -1, true},
		{"v1.0.0-alpha.1", "v1.0.0-alpha.beta", -1, true},
		{"v1.0.0-2", "v1.0.0-10", -1, true},
		{"v1.0.0+abc", "v1.0.0+def", 0, true},
		// 擬似版（RELEASE.md §2-1 の配布）は時刻の順
		{"v0.0.0-20260919030000-abc1234", "v0.0.0-20260919040000-0000000", -1, true},
		{"v0.0.0-20261231235959-fff", "v1.0.0-rc1", -1, true},
		// 比べられない版
		{"dev", "v1.0.0", 0, false},
		{"20260919-abc1234", "v1.0.0", 0, false},
		{"v1.0", "v1.0.0", 0, false},
		{"v01.0.0", "v1.0.0", 0, false},
		{"v1.0.0-", "v1.0.0", 0, false},
		{"v1.0.0-a..b", "v1.0.0", 0, false},
	} {
		got, ok := Compare(c.a, c.b)
		if ok != c.ok || got != c.want {
			t.Errorf("Compare(%q, %q) = %d, %v（want %d, %v）", c.a, c.b, got, ok, c.want, c.ok)
		}
		if c.ok {
			if back, _ := Compare(c.b, c.a); back != -c.want {
				t.Errorf("Compare(%q, %q) = %d（逆向きと合わない）", c.b, c.a, back)
			}
		}
	}
	if !Older("v1.0.0", "v1.0.1") || Older("dev", "v1.0.0") || Older("v1.0.0", "dev") {
		t.Error("Older の判定が違う")
	}
}
