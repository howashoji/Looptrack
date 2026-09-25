package service

import (
	"context"
	"errors"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/testutil"
)

// アーカイブしたプロジェクトは一覧と解決（AccessibleProjects・MemberProjects）から消え、起票・コメント・状態・担当・編集を
// service の入口で拒む（Rejected / project_archived）。行・イシュー・コメント・イベントは残り、戻すと元どおり操作できる。
// 対照として、アーカイブしていないプロジェクトは同じ操作が通り、一覧にも出続ける。
func TestProjectArchiveRejectsWrites(t *testing.T) {
	db := testutil.MigratedDB(t)
	ctx := context.Background()
	s := New(db, nil)
	mk := func(slug, prefix string) store.Project {
		id, err := store.CreateProject(ctx, db, store.Project{Slug: slug, Prefix: prefix, Width: 4, Name: slug})
		if err != nil {
			t.Fatal(err)
		}
		p, err := store.ProjectByID(ctx, db, id)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	demo, other := mk("demo", "DEMO"), mk("other", "OTHER")
	userID, err := store.CreateUser(ctx, db, "ed", "Ed", "x", "member")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.CreateUser(ctx, db, "root", "Root", "x", "admin")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []store.Project{demo, other} {
		if err := store.SetMember(ctx, db, p.ID, userID, "editor"); err != nil {
			t.Fatal(err)
		}
	}
	a := Actor{UserID: userID, Via: "api", Lang: i18n.JA}
	create := func(p store.Project) (*Issue, error) {
		return s.Create(ctx, a, p, CreateInput{Title: "課題"}, i18n.JA)
	}
	it, err := create(demo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Comment(ctx, a, demo, it.Row.ID, "アーカイブ前のコメント"); err != nil {
		t.Fatal(err)
	}
	ctl, err := create(other)
	if err != nil {
		t.Fatal(err)
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	nIssues := count("SELECT COUNT(*) FROM issues WHERE project_id = ?", demo.ID)
	nComments := count("SELECT COUNT(*) FROM comments c JOIN issues i ON i.id = c.issue_id WHERE i.project_id = ?", demo.ID)
	nEvents := count("SELECT COUNT(*) FROM issue_events WHERE project_id = ?", demo.ID)

	res, err := s.SetProjectArchived(ctx, demo, true)
	if err != nil || !res.Changed || !res.Archived || res.Message().ID != "cmd.project.archived" {
		t.Fatalf("アーカイブ: %+v %v", res, err)
	}
	if res, err := s.SetProjectArchived(ctx, demo, true); err != nil || res.Changed || res.Message().ID != "cmd.project.archive_unchanged" {
		t.Errorf("二度目のアーカイブ: %+v %v", res, err)
	}

	// 書き込みはどれも拒む。呼ぶ側が持つ store.Project は古い（Archived が false）ままでも DB から読み直して拒む
	rejected := func(what string, err error) {
		t.Helper()
		var se *Error
		if !errors.As(err, &se) || se.Kind != Rejected || se.Code != "project_archived" {
			t.Errorf("%s: 拒まなかった: %v", what, err)
		}
	}
	if demo.Archived {
		t.Fatal("前提が崩れている: 古い store.Project のはずが Archived を持っている")
	}
	_, err = create(demo)
	rejected("起票", err)
	_, err = s.Comment(ctx, a, demo, it.Row.ID, "x")
	rejected("コメント", err)
	_, err = s.SetStatus(ctx, a, demo, it.Row.ID, "In Progress", "", "")
	rejected("状態", err)
	_, err = s.Assign(ctx, a, demo, it.Row.ID, "me", "")
	rejected("担当", err)
	title := "新しい題"
	cur, err := s.Detail(ctx, demo, it.Row.ID) // 読むのは拒まない（service の中では。一覧・解決からは外れる）
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Update(ctx, a, demo, it.Row.ID, cur.Row.Version, Patch{Title: &title})
	rejected("編集", err)

	// 対照: アーカイブしていないプロジェクトは同じ操作が通る
	if _, err := create(other); err != nil {
		t.Errorf("対照の起票: %v", err)
	}
	if _, err := s.Comment(ctx, a, other, ctl.Row.ID, "x"); err != nil {
		t.Errorf("対照のコメント: %v", err)
	}
	if _, err := s.SetStatus(ctx, a, other, ctl.Row.ID, "In Progress", "", ""); err != nil {
		t.Errorf("対照の状態: %v", err)
	}

	// 一覧と解決から消える（利用者も管理者も）。ListProjects（管理画面用）には Archived 付きで残る
	slugs := func(ps []store.Project) string {
		out := ""
		for _, p := range ps {
			out += p.Slug + ","
		}
		return out
	}
	u, _ := store.UserByID(ctx, db, userID)
	root, _ := store.UserByID(ctx, db, admin)
	if ps, _, err := store.MemberProjects(ctx, db, userID); err != nil || slugs(ps) != "other," {
		t.Errorf("MemberProjects: %s %v", slugs(ps), err)
	}
	if ps, _, err := store.AccessibleProjects(ctx, db, u); err != nil || slugs(ps) != "other," {
		t.Errorf("AccessibleProjects（利用者）: %s %v", slugs(ps), err)
	}
	if ps, _, err := store.AccessibleProjects(ctx, db, root); err != nil || slugs(ps) != "other," {
		t.Errorf("AccessibleProjects（管理者）: %s %v", slugs(ps), err)
	}
	all, err := store.ListProjects(ctx, db)
	if err != nil || slugs(all) != "demo,other," || !all[0].Archived || all[1].Archived {
		t.Errorf("ListProjects: %+v %v", all, err)
	}

	// 行は消えない
	if count("SELECT COUNT(*) FROM issues WHERE project_id = ?", demo.ID) != nIssues ||
		count("SELECT COUNT(*) FROM comments c JOIN issues i ON i.id = c.issue_id WHERE i.project_id = ?", demo.ID) != nComments ||
		count("SELECT COUNT(*) FROM issue_events WHERE project_id = ?", demo.ID) != nEvents {
		t.Error("アーカイブでイシュー・コメント・イベントが変わった")
	}

	// 戻すと元どおり
	if res, err := s.SetProjectArchived(ctx, demo, false); err != nil || !res.Changed || res.Archived || res.Message().ID != "cmd.project.unarchived" {
		t.Fatalf("戻す: %+v %v", res, err)
	}
	if res, err := s.SetProjectArchived(ctx, demo, false); err != nil || res.Changed || res.Message().ID != "cmd.project.unarchive_unchanged" {
		t.Errorf("二度目の戻す: %+v %v", res, err)
	}
	if ps, _, err := store.MemberProjects(ctx, db, userID); err != nil || slugs(ps) != "demo,other," {
		t.Errorf("戻した後の MemberProjects: %s %v", slugs(ps), err)
	}
	if _, err := s.Comment(ctx, a, demo, it.Row.ID, "戻した後のコメント"); err != nil {
		t.Errorf("戻した後のコメント: %v", err)
	}
	if again, err := create(demo); err != nil || again.Item.ID != "DEMO-0002" {
		t.Errorf("戻した後の起票（採番は続きから）: %v %v", again, err)
	}

	// 無いプロジェクトは NotFound
	var se *Error
	if _, err := s.SetProjectArchived(ctx, store.Project{ID: other.ID + 999, Slug: "nope"}, true); !errors.As(err, &se) || se.Kind != NotFound {
		t.Errorf("無いプロジェクト: %v", err)
	}
}
