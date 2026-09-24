package domain

import (
	"fmt"
	"sort"
	"strings"
)

// MatrixRow は要件 1 件分のトレース。
type MatrixRow struct {
	Requirement Issue
	Design      []Issue
	Task        []Issue
	Test        []Issue
}

// Orphan は存在しないイシュー ID を traces で指しているもの。
type Orphan struct {
	Target string   // 大文字化した traces の値
	From   []string // 指しているイシューの ID（load_all の順）
}

// Matrix はトレーサビリティ表と警告 2 種。
type Matrix struct {
	Rows     []MatrixRow
	Untested []Issue // test から trace されていない要件
	Orphans  []Orphan
}

// BuildMatrixData は以前の CLI と同じ判定で表と警告を作る。
func (s *Set) BuildMatrixData() Matrix {
	var reqs []Issue
	byTrace := map[string][]Issue{}
	for _, it := range s.Items {
		if it.Type == "requirement" {
			reqs = append(reqs, it)
		}
		for _, t := range it.Traces {
			u := strings.ToUpper(t)
			byTrace[u] = append(byTrace[u], it)
		}
	}
	sort.SliceStable(reqs, func(i, j int) bool { return reqs[i].ID < reqs[j].ID })
	pick := func(reqID, typ string) []Issue {
		var got []Issue
		for _, it := range byTrace[strings.ToUpper(reqID)] {
			if it.Type == typ {
				got = append(got, it)
			}
		}
		sort.SliceStable(got, func(i, j int) bool { return got[i].ID < got[j].ID })
		return got
	}
	var m Matrix
	for _, r := range reqs {
		row := MatrixRow{Requirement: r, Design: pick(r.ID, "design"), Task: pick(r.ID, "task"), Test: pick(r.ID, "test")}
		m.Rows = append(m.Rows, row)
		if len(row.Test) == 0 {
			m.Untested = append(m.Untested, r)
		}
	}
	known := map[string]bool{}
	for _, it := range s.Items {
		known[strings.ToUpper(it.ID)] = true
	}
	var targets []string
	for t := range byTrace {
		if !known[t] {
			targets = append(targets, t)
		}
	}
	sort.Strings(targets)
	for _, t := range targets {
		o := Orphan{Target: t}
		for _, it := range byTrace[t] {
			o.From = append(o.From, it.ID)
		}
		m.Orphans = append(m.Orphans, o)
	}
	return m
}

// BuildMatrixMarkdown は以前の CLI の matrix.md と同じ文字列を返す（now は "> 生成日時:" の値）。
func (s *Set) BuildMatrixMarkdown(now string) string {
	m := s.BuildMatrixData()
	lines := []string{"# トレーサビリティマトリクス", "",
		"> **自動生成** — `looptrack issue matrix` で再生成。手で編集しない。",
		"> 生成日時: " + now, "",
		"要件（type: requirement）に対し、`traces` で紐づく設計 / 実装 / テストを一覧する。", ""}
	if len(m.Rows) == 0 {
		lines = append(lines, "（要件イシューがまだありません）", "")
		return strings.Join(lines, "\n") + "\n"
	}
	cell := func(items []Issue) string {
		var parts []string
		for _, it := range items {
			parts = append(parts, fmt.Sprintf("%s(%s)", it.ID, it.Status))
		}
		if len(parts) == 0 {
			return "—"
		}
		return strings.Join(parts, ", ")
	}
	lines = append(lines, "| 要件 | ステータス | タイトル | 設計 | 実装 | テスト |", "| -- | -- | -- | -- | -- | -- |")
	for _, r := range m.Rows {
		q := r.Requirement
		lines = append(lines, fmt.Sprintf("| %s | %s | %s | %s | %s | %s |", q.ID, q.Status, q.Title, cell(r.Design), cell(r.Task), cell(r.Test)))
	}
	lines = append(lines, "", fmt.Sprintf("## ⚠️ テスト未紐づけの要件（%d 件）", len(m.Untested)), "")
	if len(m.Untested) > 0 {
		lines = append(lines, "受け入れ条件を検証する test イシューが無い。実装を Done にする前に起票すること。", "")
		for _, r := range m.Untested {
			lines = append(lines, fmt.Sprintf("- %s %s", r.ID, r.Title))
		}
	} else {
		lines = append(lines, "（なし）")
	}
	lines = append(lines, "")
	if len(m.Orphans) > 0 {
		lines = append(lines, "## ⚠️ 存在しないイシューを traces に指しているイシュー", "")
		for _, o := range m.Orphans {
			lines = append(lines, fmt.Sprintf("- `%s` ← %s", o.Target, strings.Join(o.From, ", ")))
		}
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n") + "\n"
}

// BuildIndexMarkdown は以前の CLI の index.md と同じ文字列を返す。
func (s *Set) BuildIndexMarkdown(now string) string {
	lines := []string{"# イシュー一覧", "",
		"> **自動生成** — `looptrack issue index` で再生成。手で編集しない。",
		"> 生成日時: " + now, "",
		"## 着手可能（ready）", "", "blocked_by が全て解決済みで、未着手のもの。", ""}
	if ready := s.Ready(); len(ready) > 0 {
		lines = append(lines, "| ID | 型 | 優先度 | タイトル |", "| -- | -- | -- | -- |")
		for _, it := range ready {
			lines = append(lines, fmt.Sprintf("| %s | %s | %s | %s |", it.ID, it.Type, it.Priority, it.Title))
		}
	} else {
		lines = append(lines, "（なし）")
	}
	lines = append(lines, "")
	for _, st := range Statuses {
		var group []Issue
		for _, it := range s.Items {
			if it.Status == st {
				group = append(group, it)
			}
		}
		sort.SliceStable(group, func(i, j int) bool { return group[i].ID < group[j].ID })
		lines = append(lines, fmt.Sprintf("## %s（%d 件）", st, len(group)), "")
		if len(group) > 0 {
			lines = append(lines, "| ID | 型 | 優先度 | ブロック元 | タイトル |", "| -- | -- | -- | -- | -- |")
			for _, it := range group {
				blocked := strings.Join(it.BlockedBy, ", ")
				if blocked == "" {
					blocked = "-"
				}
				lines = append(lines, fmt.Sprintf("| %s | %s | %s | %s | %s |", it.ID, it.Type, it.Priority, blocked, it.Title))
			}
		} else {
			lines = append(lines, "（なし）")
		}
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n") + "\n"
}
