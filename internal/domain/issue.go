// Package domain はイシュー管理の意味論（列挙値・ready・並び順・matrix・index・採番用の名前・状態変更）を持つ。
// 以前の CLI（1.0.0 より前）と同じ結果を返すことが要件。比較テストは parity_test.go。
package domain

import (
	"strings"

	"github.com/howashoji/looptrack/internal/mdformat"
)

// 列挙値（以前の CLI と同じ順序）。
var (
	Statuses       = []string{"Backlog", "Todo", "In Progress", "In Review", "Done", "Canceled"}
	ClosedStatuses = []string{"Done", "Canceled"}
	Types          = []string{"requirement", "design", "task", "bug", "test", "epic"}
	Priorities     = []string{"P0", "P1", "P2", "P3"}
	ListFields     = []string{"labels", "blocked_by", "traces", "refs"}
	SortKeys       = []string{"priority", "id", "updated", "created", "status", "type", "title"}
)

// Issue は一覧・判定に使う項目。本文は mdformat.Document 側で持つ。
type Issue struct {
	ID        string
	Title     string
	Type      string
	Status    string
	Priority  string
	Parent    string
	Labels    []string
	BlockedBy []string
	Traces    []string
	Refs      []string
	Created   string
	Updated   string
	// 次の 2 つはキーの有無（frontmatter にキーがあるか）を区別するため
	hasID, hasTitle bool
}

// FromDocument は frontmatter から Issue を作る。
func FromDocument(d *mdformat.Document) Issue {
	var it Issue
	for _, f := range d.Front {
		switch f.Key {
		case "id":
			it.ID, it.hasID = scalar(f), true
		case "title":
			it.Title, it.hasTitle = scalar(f), true
		case "type":
			it.Type = scalar(f)
		case "status":
			it.Status = scalar(f)
		case "priority":
			it.Priority = scalar(f)
		case "parent":
			it.Parent = scalar(f)
		case "created":
			it.Created = scalar(f)
		case "updated":
			it.Updated = scalar(f)
		case "labels":
			it.Labels = list(f)
		case "blocked_by":
			it.BlockedBy = list(f)
		case "traces":
			it.Traces = list(f)
		case "refs":
			it.Refs = list(f)
		}
	}
	return it
}

// scalar はリスト表記の値を以前の CLI と同じく文字列として扱う（`[a, b]` は ", " で連結）。
func scalar(f mdformat.Field) string {
	if f.IsList {
		return "[" + strings.Join(f.List, ", ") + "]"
	}
	return f.Value
}

// list はスカラー値を要素 1 つのリストにする（以前の CLI と同じ。空文字は空リスト）。
func list(f mdformat.Field) []string {
	if f.IsList {
		return f.List
	}
	if f.Value == "" {
		return []string{}
	}
	return []string{f.Value}
}

// IsClosed は Done / Canceled か。
func (it Issue) IsClosed() bool { return contains(ClosedStatuses, it.Status) }

// Valid は値が列挙に含まれるか。
func Valid(allowed []string, v string) bool { return contains(allowed, v) }

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// rank は定義順の位置。未知の値は末尾。
func rank(v string, order []string) int {
	for i, x := range order {
		if x == v {
			return i
		}
	}
	return len(order)
}
