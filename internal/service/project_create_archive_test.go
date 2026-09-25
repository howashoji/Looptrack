package service

import (
	"context"
	"errors"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/testutil"
)

// プロジェクトの作成（CreateProject）とアーカイブ（SetProjectArchived）の組み合わせ。
// 作ったプロジェクトはアーカイブされていない状態で始まり、一覧に出て起票できる。
// アーカイブしたプロジェクトの slug と prefix は使い回さない（行が残るので、同じ slug・prefix の作成は Conflict）。
// 対照として、使われていない slug と prefix なら、アーカイブ済みのプロジェクトがあっても作れる。
func TestCreateProjectAfterArchive(t *testing.T) {
	db := testutil.MigratedDB(t)
	ctx := context.Background()
	s := New(db, nil)
	adminID, err := store.CreateUser(ctx, db, "root", "Root", "x", "admin")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.UserByID(ctx, db, adminID)
	if err != nil {
		t.Fatal(err)
	}

	// (a) 作ったプロジェクトは archived_at が NULL で、一覧に出て、起票できる
	alpha, err := s.CreateProject(ctx, admin, store.Project{Slug: "alpha", Prefix: "ALP", Name: "アルファ"})
	if err != nil {
		t.Fatal(err)
	}
	if archived, err := store.ProjectArchived(ctx, db, alpha.ID); err != nil || archived {
		t.Fatalf("作ったプロジェクトがアーカイブ済みになっている: %v %v", archived, err)
	}
	if got, err := store.ProjectBySlug(ctx, db, "alpha"); err != nil || got.Archived {
		t.Fatalf("ProjectBySlug: %+v %v", got, err)
	}
	listed := func(slug string) bool {
		t.Helper()
		ps, _, err := store.AccessibleProjects(ctx, db, admin)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range ps {
			if p.Slug == slug {
				return true
			}
		}
		return false
	}
	if !listed("alpha") {
		t.Fatal("作ったプロジェクトが AccessibleProjects に出ない")
	}
	ms, err := store.UserMemberships(ctx, db, admin.ID)
	if err != nil || len(ms) != 1 {
		t.Fatalf("作った人の参加: %+v %v", ms, err)
	}
	a := Actor{UserID: admin.ID, Via: "api", Lang: i18n.JA}
	it, err := s.Create(ctx, a, alpha, CreateInput{Title: "最初の起票"}, i18n.JA)
	if err != nil || it.Item.ID != "ALP-0001" {
		t.Fatalf("作ったプロジェクトに起票できない: %+v %v", it, err)
	}

	// アーカイブすると一覧から消える（前提の確認）
	if res, err := s.SetProjectArchived(ctx, alpha, true); err != nil || !res.Changed {
		t.Fatalf("アーカイブ: %+v %v", res, err)
	}
	if listed("alpha") {
		t.Fatal("前提が崩れている: アーカイブしたプロジェクトが AccessibleProjects に出る")
	}

	// (b) アーカイブ済みと同じ slug・同じ prefix は Conflict（project_exists）。作られず、アーカイブ済みの行も変わらない
	for _, c := range []struct{ slug, prefix string }{{"alpha", "NEW"}, {"alpha-new", "ALP"}, {"alpha", "ALP"}} {
		_, err := s.CreateProject(ctx, admin, store.Project{Slug: c.slug, Prefix: c.prefix})
		var se *Error
		if !errors.As(err, &se) || se.Kind != Conflict || se.Code != "project_exists" {
			t.Errorf("アーカイブ済みと重なる %s/%s: %v（Conflict / project_exists のはず）", c.slug, c.prefix, err)
		}
	}
	if _, err := store.ProjectBySlug(ctx, db, "alpha-new"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("アーカイブ済みと prefix が重なるプロジェクトが作られた: %v", err)
	}
	got, err := store.ProjectBySlug(ctx, db, "alpha")
	if err != nil || !got.Archived || got.Prefix != "ALP" || got.Name != "アルファ" || got.Counter != 1 {
		t.Errorf("アーカイブ済みのプロジェクトが変わった: %+v %v", got, err)
	}

	// 対照: 使われていない slug と prefix なら作れて、そちらは使える
	beta, err := s.CreateProject(ctx, admin, store.Project{Slug: "beta", Prefix: "BET"})
	if err != nil {
		t.Fatalf("使われていない slug と prefix の作成が拒まれた: %v", err)
	}
	if !listed("beta") {
		t.Error("対照のプロジェクトが AccessibleProjects に出ない")
	}
	if it, err := s.Create(ctx, a, beta, CreateInput{Title: "対照"}, i18n.JA); err != nil || it.Item.ID != "BET-0001" {
		t.Errorf("対照のプロジェクトに起票できない: %+v %v", it, err)
	}
}
