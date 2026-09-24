package cli

import (
	"encoding/json"
	"math/rand"
	"os"
	"strings"
	"testing"
)

func TestUnifiedDiffBasic(t *testing.T) {
	a := splitLinesKeep("a\nb\nc\nd\ne\nf\ng\nh\n")
	b := splitLinesKeep("a\nb\nc\nX\ne\nf\ng\nh\ni\n")
	want := "--- 前\n+++ 後\n@@ -1,8 +1,9 @@\n a\n b\n c\n-d\n+X\n e\n f\n g\n h\n+i\n"
	if got := unifiedDiff(a, b, "前", "後"); got != want {
		t.Errorf("%q", got)
	}
	if got := unifiedDiff(a, a, "x", "y"); got != "" {
		t.Errorf("同じなら空: %q", got)
	}
	if got := splitLinesKeep("a\r\nb\rc\x0bd e"); strings.Join(got, "|") != "a\r\n|b\r|c\x0b|d |e" {
		t.Errorf("行の区切り: %q", got)
	}
}

type diffPair struct{ A, B string }

// diffPairs は比較に使う行の並び（短い・長い・200 行以上の autojunk）。種を固定してあるので毎回同じ。
func diffPairs() []diffPair {
	rng := rand.New(rand.NewSource(20260919))
	alphabet := []string{"a\n", "b\n", "c\n", "\n", "## 見出し\n", "- [ ] 条件\n", "本文\r\n", "x", "d\n"}
	gen := func(n int) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteString(alphabet[rng.Intn(len(alphabet))])
		}
		return b.String()
	}
	var pairs []diffPair
	for i := 0; i < 400; i++ {
		n := rng.Intn(20)
		if i%20 == 0 {
			n = 200 + rng.Intn(150)
		}
		a := gen(n)
		// b は a を少し変えたもの（挿入・削除・置換）か、まったく別のもの
		var b string
		if rng.Intn(4) == 0 {
			b = gen(rng.Intn(20) + n/2)
		} else {
			lines := splitLinesKeep(a)
			for k := 0; k < 1+rng.Intn(4); k++ {
				p := 0
				if len(lines) > 0 {
					p = rng.Intn(len(lines))
				}
				switch rng.Intn(3) {
				case 0:
					lines = append(lines[:p], append([]string{alphabet[rng.Intn(len(alphabet))]}, lines[p:]...)...)
				case 1:
					if len(lines) > 0 {
						lines = append(lines[:p], lines[p+1:]...)
					}
				default:
					if len(lines) > 0 {
						lines[p] = alphabet[rng.Intn(len(alphabet))]
					}
				}
			}
			b = strings.Join(lines, "")
		}
		pairs = append(pairs, diffPair{a, b})
	}
	return pairs
}

// TestUnifiedDiffMatchesRecord は unifiedDiff の出力を記録（testdata/unified_diff.json）と比べる。
// 記録は、差分の出力を決めた実装（撤去した以前の CLI の統合差分）の結果を 400 通り控えたもので、
// 作業コピーの差分の見え方をこの先も変えないための固定の正解。手元に別の処理系を入れなくても流せる。
func TestUnifiedDiffMatchesRecord(t *testing.T) {
	b, err := os.ReadFile("testdata/unified_diff.json")
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Pairs []diffPair `json:"pairs"`
		Want  []string   `json:"want"`
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatal(err)
	}
	pairs := diffPairs()
	if len(pairs) != len(rec.Pairs) || len(pairs) != len(rec.Want) {
		t.Fatalf("記録の件数が合いません（記録 %d 組・%d 件、今回 %d 組）", len(rec.Pairs), len(rec.Want), len(pairs))
	}
	for i, p := range pairs {
		if p != rec.Pairs[i] {
			t.Fatalf("%d 番目の入力が記録と違います（diffPairs を変えたら記録も録り直す）", i)
		}
		got := unifiedDiff(splitLinesKeep(p.A), splitLinesKeep(p.B), "版 1（edit 時）", "版 2（サーバの最新）")
		if got != rec.Want[i] {
			t.Errorf("%d 番目が違う:\na=%q\nb=%q\n--- 記録\n%s\n--- 今回\n%s", i, p.A, p.B, rec.Want[i], got)
		}
	}
}
