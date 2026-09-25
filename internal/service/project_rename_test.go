package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/testutil"
)

// 表示名だけが変わり、slug・prefix・width・採番は変わらない。空・空白だけ・長すぎる名前は拒否する。
func TestRenameProject(t *testing.T) {
	db := testutil.MigratedDB(t)
	ctx := context.Background()
	id, err := store.CreateProject(ctx, db, store.Project{Slug: "demo", Prefix: "DEMO", Width: 4, Name: "旧い名前"})
	if err != nil {
		t.Fatal(err)
	}
	s := New(db, nil)
	p, err := store.ProjectByID(ctx, db, id)
	if err != nil {
		t.Fatal(err)
	}

	res, err := s.RenameProject(ctx, p, "Looptrack-dev")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || res.OldName != "旧い名前" || res.NewName != "Looptrack-dev" {
		t.Errorf("結果: %+v", res)
	}
	got, err := store.ProjectByID(ctx, db, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Looptrack-dev" || got.Slug != "demo" || got.Prefix != "DEMO" || got.Width != 4 || got.Counter != p.Counter {
		t.Errorf("変更後: %+v", got)
	}

	// 同じ名前なら変更なし
	if res, err := s.RenameProject(ctx, got, "Looptrack-dev"); err != nil || res.Changed {
		t.Errorf("同じ名前: %+v %v", res, err)
	}

	// 拒否する名前（作成と同じ規則）。対照として 255 文字ちょうどは通る
	for _, bad := range []string{"", "   ", "\t\n", strings.Repeat("あ", 256)} {
		_, err := s.RenameProject(ctx, got, bad)
		var se *Error
		if !errors.As(err, &se) || se.Kind != Invalid {
			t.Errorf("%q を拒否しなかった: %v", bad, err)
		}
	}
	if _, err := s.RenameProject(ctx, got, strings.Repeat("あ", 255)); err != nil {
		t.Errorf("255 文字を拒否した: %v", err)
	}
	after, _ := store.ProjectByID(ctx, db, id)
	if after.Name != strings.Repeat("あ", 255) {
		t.Errorf("拒否の後の名前: %q", after.Name)
	}

	// 無いプロジェクトは NotFound
	var se *Error
	if _, err := s.RenameProject(ctx, store.Project{ID: id + 999, Slug: "nope"}, "x"); !errors.As(err, &se) || se.Kind != NotFound {
		t.Errorf("無いプロジェクト: %v", err)
	}
}
