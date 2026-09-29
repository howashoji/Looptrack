package service

import (
	"context"
	"errors"
	"testing"

	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/testutil"
)

// TestNewProjectDefaults は、省略した値の埋め方（prefix は slug の英大文字・表示名は slug・width は
// store.DefaultProjectWidth・並び順は store.DefaultProjectSortOrder）を確かめる。明示した値は変わらない
// （サーバの CLI の looptrack project create だけがこの埋め方を通らず、prefix と表示名を省略できなかった）。
func TestNewProjectDefaults(t *testing.T) {
	got := NewProjectDefaults(store.Project{Slug: " my-app "})
	if got.Slug != "my-app" || got.Prefix != "MY-APP" || got.Name != "my-app" ||
		got.Width != store.DefaultProjectWidth || got.SortOrder != store.DefaultProjectSortOrder {
		t.Errorf("省略した値の埋め方: %+v", got)
	}

	explicit := store.Project{Slug: "my-app", Prefix: "APP", Name: "アプリ", Width: 6, SortOrder: 5, Description: "説明"}
	if got := NewProjectDefaults(explicit); got.Prefix != "APP" || got.Name != "アプリ" || got.Width != 6 || got.SortOrder != 5 || got.Description != "説明" {
		t.Errorf("明示した値が変わった: %+v", got)
	}
}

// TestNewProjectDefaultsExplicitZero は、WidthExplicit・SortOrderExplicit を渡すと 0 が既定値に
// 置き換わらないこと（渡さなければこれまでどおり 0 は未指定として置き換わる対照つき）を確かめる。
func TestNewProjectDefaultsExplicitZero(t *testing.T) {
	zero := store.Project{Slug: "my-app", Width: 0, SortOrder: 0}
	if got := NewProjectDefaults(zero); got.Width != store.DefaultProjectWidth || got.SortOrder != store.DefaultProjectSortOrder {
		t.Errorf("対照（オプション無しの 0 は未指定）が崩れている: %+v", got)
	}
	if got := NewProjectDefaults(zero, WidthExplicit()); got.Width != 0 || got.SortOrder != store.DefaultProjectSortOrder {
		t.Errorf("WidthExplicit で明示した 0 が置き換わった: %+v", got)
	}
	if got := NewProjectDefaults(zero, SortOrderExplicit()); got.SortOrder != 0 || got.Width != store.DefaultProjectWidth {
		t.Errorf("SortOrderExplicit で明示した 0 が置き換わった: %+v", got)
	}
	if got := NewProjectDefaults(zero, WidthExplicit(), SortOrderExplicit()); got.Width != 0 || got.SortOrder != 0 {
		t.Errorf("両方明示した 0 が置き換わった: %+v", got)
	}
}

// TestCreateProjectNoMemberFillsDefaults は、CLI の looptrack project create・setup の最初のプロジェクトが
// 使う CreateProjectNoMember が、CreateProject（MCP・管理画面）と同じ既定値で埋め、DB にもそのまま残ることを確かめる。
func TestCreateProjectNoMemberFillsDefaults(t *testing.T) {
	db := testutil.MigratedDB(t)
	ctx := context.Background()

	p, err := CreateProjectNoMember(ctx, db, store.Project{Slug: "cli-made"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Prefix != "CLI-MADE" || p.Name != "cli-made" || p.Width != store.DefaultProjectWidth || p.SortOrder != store.DefaultProjectSortOrder {
		t.Errorf("既定値が埋まっていない: %+v", p)
	}
	got, err := store.ProjectBySlug(ctx, db, "cli-made")
	if err != nil {
		t.Fatal(err)
	}
	if got.SortOrder != store.DefaultProjectSortOrder || got.Prefix != "CLI-MADE" || got.Name != "cli-made" {
		t.Errorf("DB に既定値が保存されていない: %+v", got)
	}

	// 重複は Conflict（service.CreateProject と同じ判定）
	if _, err := CreateProjectNoMember(ctx, db, store.Project{Slug: "cli-made"}); err == nil {
		t.Error("重複した slug を受け付けた")
	} else {
		var se *Error
		if !errors.As(err, &se) || se.Kind != Conflict || se.Code != "project_exists" {
			t.Errorf("重複の判定: %v（Conflict / project_exists のはず）", err)
		}
	}
}

// TestCreateProjectNoMemberOrderZeroExplicit は、SortOrderExplicit を渡すと並び順に明示した 0 が
// そのまま DB に残ることを確かめる（48a939ee までの looptrack project create --order 0 と同じ）。
// 対照として、渡さなければ 0 は未指定として既定値に置き換わることもあわせて確かめる。
func TestCreateProjectNoMemberOrderZeroExplicit(t *testing.T) {
	db := testutil.MigratedDB(t)
	ctx := context.Background()

	p, err := CreateProjectNoMember(ctx, db, store.Project{Slug: "order-zero"}, SortOrderExplicit())
	if err != nil {
		t.Fatal(err)
	}
	if p.SortOrder != 0 {
		t.Errorf("明示した 0 が保存されていない: %+v", p)
	}
	got, err := store.ProjectBySlug(ctx, db, "order-zero")
	if err != nil {
		t.Fatal(err)
	}
	if got.SortOrder != 0 {
		t.Errorf("DB に明示した 0 が残っていない: %+v", got)
	}

	// 対照: オプション無しなら 0 は未指定として既定値に置き換わる
	p2, err := CreateProjectNoMember(ctx, db, store.Project{Slug: "order-default"})
	if err != nil {
		t.Fatal(err)
	}
	if p2.SortOrder != store.DefaultProjectSortOrder {
		t.Errorf("対照（未指定は既定値）が崩れている: %+v", p2)
	}
}

// TestCreateProjectNoMemberWidthZeroExplicit は、WidthExplicit を渡すと width に明示した 0 が
// 既定値に置き換わらず、store.ValidateNewProject の範囲検査（1〜9）でそのまま拒まれることを確かめる
// （48a939ee までの looptrack project create --width 0 と同じ「常に誤り」に戻す）。
func TestCreateProjectNoMemberWidthZeroExplicit(t *testing.T) {
	db := testutil.MigratedDB(t)
	ctx := context.Background()

	if _, err := CreateProjectNoMember(ctx, db, store.Project{Slug: "width-zero"}, WidthExplicit()); err == nil {
		t.Error("明示した width 0 を受け付けた（範囲検査で拒むはず）")
	} else {
		var se *Error
		if !errors.As(err, &se) || se.Kind != Invalid {
			t.Errorf("width 0 の判定: %v（Invalid のはず）", err)
		}
	}
}
