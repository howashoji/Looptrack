package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/store"
)

// looptrack project create（ADD-PROJECT.md）で作ったプロジェクトが、アプリ用ユーザーの権限でそのまま使えること。
func TestCreateProject(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.project("req") // 既存（slug req・prefix REQ）
	if _, err := store.CreateProject(ctx, e.db, store.Project{Slug: "req", Prefix: "NEW", Width: 4, Name: "x"}); !errors.Is(err, store.ErrProjectExists) {
		t.Errorf("slug の重複: %v", err)
	}
	if _, err := store.CreateProject(ctx, e.db, store.Project{Slug: "other", Prefix: "REQ", Width: 4, Name: "x"}); !errors.Is(err, store.ErrProjectExists) {
		t.Errorf("prefix の重複: %v", err)
	}
	if _, err := store.CreateProject(ctx, e.db, store.Project{Slug: "Bad", Prefix: "MYP", Width: 4, Name: "x"}); err == nil {
		t.Error("不正な slug を受け付けた")
	}
	if _, err := store.CreateProject(ctx, e.db, store.Project{Slug: "myp", Prefix: "MYP", Width: 3, Name: "新規", Description: "説明", SortOrder: 5}); err != nil {
		t.Fatal(err)
	}
	a := e.apiAs(e.adminIn("root", "root-password-12", "myp"))
	var res struct {
		Issue struct {
			ID string `json:"id"`
		} `json:"issue"`
	}
	a.json(201, "POST", "/projects/myp/issues", map[string]any{"title": "接続確認"}, &res)
	if res.Issue.ID != "MYP-001" {
		t.Errorf("採番 = %s, want MYP-001", res.Issue.ID)
	}
	p, _ := store.ProjectBySlug(ctx, e.db, "myp")
	if p.Name != "新規" || p.Description != "説明" || p.SortOrder != 5 || p.Counter != 1 || !strings.EqualFold(p.Prefix, "MYP") {
		t.Errorf("作成したプロジェクト: %+v", p)
	}
}
