package cli

// 統合差分（前後 3 行・行末は "\n" のまま・改行で区切って改行を残す）。
// push の競合（409）のときに、edit 時の版とサーバの最新版の差分を標準エラーに出すのに使う。
// 差分の切り方（どの行を同じとみなすか）まで以前の CLI（1.0.0 より前）と同じにするため、
// 一致部分を探す手順（頻出行を無視する仕掛けを含む）をそのまま移した。

import (
	"fmt"
	"sort"
	"strings"
)

// splitLinesKeep は行で区切る（区切りを残す。\r\n・\r・\n・\v・\f・\x1c-\x1e・\x85・ ・ ）。
func splitLinesKeep(s string) []string {
	var out []string
	start := 0
	rs := []rune(s)
	// バイト位置で切るため、rune ごとの位置を追う
	pos := 0
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		size := len(string(r))
		end := pos + size
		isBreak := false
		switch r {
		case '\n', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			isBreak = true
		case '\r':
			isBreak = true
			if i+1 < len(rs) && rs[i+1] == '\n' {
				i++
				end++
			}
		}
		pos = end
		if isBreak {
			out = append(out, s[start:end])
			start = end
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

type diffMatch struct{ a, b, size int }

type diffOpcode struct {
	tag            string
	i1, i2, j1, j2 int
}

// sequenceMatcher は 2 つの行の列の一致部分を探す（無視する行の指定は無し・よく出る行の除外は有効）。
type sequenceMatcher struct {
	a, b []string
	b2j  map[string][]int
}

func newSequenceMatcher(a, b []string) *sequenceMatcher {
	m := &sequenceMatcher{a: a, b: b, b2j: map[string][]int{}}
	for i, elt := range b {
		m.b2j[elt] = append(m.b2j[elt], i)
	}
	// よく出る行の除外: 200 行以上なら、1% を超えて現れる行を「よく出る行」として索引から外す
	if n := len(b); n >= 200 {
		ntest := n/100 + 1
		for elt, idx := range m.b2j {
			if len(idx) > ntest {
				delete(m.b2j, elt)
			}
		}
	}
	return m
}

func (m *sequenceMatcher) findLongestMatch(alo, ahi, blo, bhi int) diffMatch {
	besti, bestj, bestsize := alo, blo, 0
	j2len := map[int]int{}
	for i := alo; i < ahi; i++ {
		newj2len := map[int]int{}
		for _, j := range m.b2j[m.a[i]] {
			if j < blo {
				continue
			}
			if j >= bhi {
				break
			}
			k := j2len[j-1] + 1
			newj2len[j] = k
			if k > bestsize {
				besti, bestj, bestsize = i-k+1, j-k+1, k
			}
		}
		j2len = newj2len
	}
	// isjunk が無いので bjunk は空。よく出る行（popular）はここで前後に伸ばして拾う
	for besti > alo && bestj > blo && m.a[besti-1] == m.b[bestj-1] {
		besti, bestj, bestsize = besti-1, bestj-1, bestsize+1
	}
	for besti+bestsize < ahi && bestj+bestsize < bhi && m.a[besti+bestsize] == m.b[bestj+bestsize] {
		bestsize++
	}
	return diffMatch{besti, bestj, bestsize}
}

func (m *sequenceMatcher) matchingBlocks() []diffMatch {
	la, lb := len(m.a), len(m.b)
	type span struct{ alo, ahi, blo, bhi int }
	queue := []span{{0, la, 0, lb}}
	var blocks []diffMatch
	for len(queue) > 0 {
		q := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		x := m.findLongestMatch(q.alo, q.ahi, q.blo, q.bhi)
		if x.size > 0 {
			blocks = append(blocks, x)
			if q.alo < x.a && q.blo < x.b {
				queue = append(queue, span{q.alo, x.a, q.blo, x.b})
			}
			if x.a+x.size < q.ahi && x.b+x.size < q.bhi {
				queue = append(queue, span{x.a + x.size, q.ahi, x.b + x.size, q.bhi})
			}
		}
	}
	sort.Slice(blocks, func(i, j int) bool {
		if blocks[i].a != blocks[j].a {
			return blocks[i].a < blocks[j].a
		}
		if blocks[i].b != blocks[j].b {
			return blocks[i].b < blocks[j].b
		}
		return blocks[i].size < blocks[j].size
	})
	var out []diffMatch
	i1, j1, k1 := 0, 0, 0
	for _, x := range blocks {
		if i1+k1 == x.a && j1+k1 == x.b {
			k1 += x.size
		} else {
			if k1 > 0 {
				out = append(out, diffMatch{i1, j1, k1})
			}
			i1, j1, k1 = x.a, x.b, x.size
		}
	}
	if k1 > 0 {
		out = append(out, diffMatch{i1, j1, k1})
	}
	return append(out, diffMatch{la, lb, 0})
}

func (m *sequenceMatcher) opcodes() []diffOpcode {
	i, j := 0, 0
	var out []diffOpcode
	for _, x := range m.matchingBlocks() {
		tag := ""
		switch {
		case i < x.a && j < x.b:
			tag = "replace"
		case i < x.a:
			tag = "delete"
		case j < x.b:
			tag = "insert"
		}
		if tag != "" {
			out = append(out, diffOpcode{tag, i, x.a, j, x.b})
		}
		i, j = x.a+x.size, x.b+x.size
		if x.size > 0 {
			out = append(out, diffOpcode{"equal", x.a, i, x.b, j})
		}
	}
	return out
}

func (m *sequenceMatcher) groupedOpcodes(n int) [][]diffOpcode {
	codes := m.opcodes()
	if len(codes) == 0 {
		codes = []diffOpcode{{"equal", 0, 1, 0, 1}}
	}
	if c := &codes[0]; c.tag == "equal" {
		c.i1, c.j1 = max(c.i1, c.i2-n), max(c.j1, c.j2-n)
	}
	if c := &codes[len(codes)-1]; c.tag == "equal" {
		c.i2, c.j2 = min(c.i2, c.i1+n), min(c.j2, c.j1+n)
	}
	nn := n + n
	var groups [][]diffOpcode
	var group []diffOpcode
	for _, c := range codes {
		if c.tag == "equal" && c.i2-c.i1 > nn {
			group = append(group, diffOpcode{c.tag, c.i1, min(c.i2, c.i1+n), c.j1, min(c.j2, c.j1+n)})
			groups = append(groups, group)
			group = nil
			c.i1, c.j1 = max(c.i1, c.i2-n), max(c.j1, c.j2-n)
		}
		group = append(group, c)
	}
	if len(group) > 0 && !(len(group) == 1 && group[0].tag == "equal") {
		groups = append(groups, group)
	}
	return groups
}

func formatRangeUnified(start, stop int) string {
	beginning, length := start+1, stop-start
	if length == 1 {
		return fmt.Sprint(beginning)
	}
	if length == 0 {
		beginning--
	}
	return fmt.Sprintf("%d,%d", beginning, length)
}

// unifiedDiff は統合差分の行をつないだ 1 つの文字列。
func unifiedDiff(a, b []string, fromfile, tofile string) string {
	return strings.Join(UnifiedDiffLines(a, b, fromfile, tofile), "")
}

// SplitLinesKeep は行で区切って区切りを残す（init の差分の表示に使う）。
func SplitLinesKeep(s string) []string { return splitLinesKeep(s) }

// UnifiedDiffLines は統合差分（1 行ずつ。内容の行は元の行の区切りのまま）。
func UnifiedDiffLines(a, b []string, fromfile, tofile string) []string {
	var out []string
	started := false
	for _, group := range newSequenceMatcher(a, b).groupedOpcodes(3) {
		if !started {
			started = true
			out = append(out, "--- "+fromfile+"\n", "+++ "+tofile+"\n")
		}
		first, last := group[0], group[len(group)-1]
		out = append(out, fmt.Sprintf("@@ -%s +%s @@\n", formatRangeUnified(first.i1, last.i2), formatRangeUnified(first.j1, last.j2)))
		for _, c := range group {
			if c.tag == "equal" {
				for _, l := range a[c.i1:c.i2] {
					out = append(out, " "+l)
				}
				continue
			}
			if c.tag == "replace" || c.tag == "delete" {
				for _, l := range a[c.i1:c.i2] {
					out = append(out, "-"+l)
				}
			}
			if c.tag == "replace" || c.tag == "insert" {
				for _, l := range b[c.j1:c.j2] {
					out = append(out, "+"+l)
				}
			}
		}
	}
	return out
}
