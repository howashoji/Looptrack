package domain

import (
	"sort"
	"strings"
)

// Set は 1 プロジェクト分のイシュー。順序は以前の CLI と同じ
// （open/ のファイル名順 → closed/ のファイル名順 ≒ 未クローズの ID 順 → クローズ済みの ID 順）。
type Set struct {
	Items []Issue
	byID  map[string]int
}

// NewSet は items を load_all と同じ順序に並べて Set を作る。ID が重複した場合は後のもの（closed 側）が勝つ。
func NewSet(items []Issue) *Set {
	s := &Set{Items: append([]Issue(nil), items...)}
	sort.SliceStable(s.Items, func(i, j int) bool {
		ci, cj := s.Items[i].IsClosed(), s.Items[j].IsClosed()
		if ci != cj {
			return !ci
		}
		return s.Items[i].ID < s.Items[j].ID
	})
	s.byID = map[string]int{}
	for i, it := range s.Items {
		if it.ID != "" {
			s.byID[it.ID] = i
		}
	}
	return s
}

// Get は ID（大文字小文字を区別しない。以前の CLI と同じ）で引く。
func (s *Set) Get(id string) (Issue, bool) {
	u := strings.ToUpper(id)
	for _, it := range s.Items {
		if strings.ToUpper(it.ID) == u {
			return it, true
		}
	}
	return Issue{}, false
}

// resolved は blocked_by の解決判定。存在しない ID は未解決（誤って ready に出さない）。
func (s *Set) resolved(id string) bool {
	if i, ok := s.byID[strings.ToUpper(id)]; ok {
		return s.Items[i].IsClosed()
	}
	if i, ok := s.byID[id]; ok {
		return s.Items[i].IsClosed()
	}
	return false
}

// IsReady は着手可能か（未クローズかつ In Progress でなく、blocked_by がすべて解決済み）。
func (s *Set) IsReady(it Issue) bool {
	if it.IsClosed() || it.Status == "In Progress" {
		return false
	}
	for _, d := range it.BlockedBy {
		if !s.resolved(d) {
			return false
		}
	}
	return true
}

// Ready は着手可能なイシューを優先度順（同順位は ID 昇順）で返す。
func (s *Set) Ready() []Issue {
	var out []Issue
	for _, it := range s.Items {
		if it.ID != "" && s.IsReady(it) {
			out = append(out, it)
		}
	}
	return SortIssues(out, "priority", false)
}

// Filter は一覧の絞り込み。
type Filter struct {
	Status, Type, Label, Ref string
	All                      bool
}

// List は以前の CLI と同じ絞り込み・並び順で返す。
// Status に Done / Canceled を指定したときは All が無くてもクローズ済みを含める。
func (s *Set) List(f Filter, sortKey string, reverse bool) []Issue {
	includeClosed := f.All || contains(ClosedStatuses, f.Status)
	var out []Issue
	for _, it := range s.Items {
		if it.ID == "" || (!includeClosed && it.IsClosed()) {
			continue
		}
		if f.Status != "" && it.Status != f.Status {
			continue
		}
		if f.Type != "" && it.Type != f.Type {
			continue
		}
		if f.Label != "" && !contains(it.Labels, f.Label) {
			continue
		}
		if f.Ref != "" && !containsFold(it.Refs, f.Ref) {
			continue
		}
		out = append(out, it)
	}
	return SortIssues(out, sortKey, reverse)
}

func containsFold(list []string, v string) bool {
	u := strings.ToUpper(v)
	for _, x := range list {
		if strings.ToUpper(x) == u {
			return true
		}
	}
	return false
}

// SortIssues は以前の CLI と同じ並び順にする。
// ID 昇順で安定ソートした後、主キーで安定ソートする（同順位は常に ID 昇順）。
// updated / created は既定で新しい順、reverse で既定の向きを反転する。
func SortIssues(items []Issue, key string, reverse bool) []Issue {
	out := append([]Issue(nil), items...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	desc := (key == "updated" || key == "created") != reverse
	primary := func(it Issue) (int, string) {
		switch key {
		case "priority":
			return rank(it.Priority, Priorities), ""
		case "status":
			return rank(it.Status, Statuses), ""
		case "type":
			return rank(it.Type, Types), ""
		case "id":
			return 0, it.ID
		case "updated":
			return 0, it.Updated
		case "created":
			return 0, it.Created
		case "title":
			return 0, it.Title
		}
		return 0, ""
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, si := primary(out[i])
		rj, sj := primary(out[j])
		if desc {
			ri, rj, si, sj = rj, ri, sj, si
		}
		if ri != rj {
			return ri < rj
		}
		return si < sj
	})
	return out
}
