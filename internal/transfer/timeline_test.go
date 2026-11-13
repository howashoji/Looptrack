package transfer

import (
	"context"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/testdata"
	"github.com/howashoji/looptrack/internal/testutil"
)

// 取り込み（import）は issue_events へ直接書く経路で、出来事を載せない。対照として、取り込んだプロジェクトへの起票は載る。
func TestTimelineImportWritesNoEvents(t *testing.T) {
	db := testutil.MigratedDB(t)
	ctx := context.Background()
	src, err := ReadSource(testdata.Root(t), []string{"mini"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Import(ctx, db, src); err != nil {
		t.Fatal(err)
	}
	count := func(q string) int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count("SELECT COUNT(*) FROM issue_events"); n == 0 {
		t.Fatal("前提が崩れています: 取り込みが issue_events を書いていません")
	}
	if n := count("SELECT COUNT(*) FROM timeline_events"); n != 0 {
		t.Errorf("取り込みで出来事が %d 件載りました", n)
	}
	p, err := store.ProjectBySlug(ctx, db, "mini")
	if err != nil {
		t.Fatal(err)
	}
	uid, err := store.CreateUser(ctx, db, "imp", "imp", "x", "member")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetMember(ctx, db, p.ID, uid, "editor"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.New(db, nil).Create(ctx, service.Actor{UserID: uid, Via: "cli"}, p, service.CreateInput{Title: "取り込みの後"}, i18n.JA); err != nil {
		t.Fatal(err)
	}
	if n := count("SELECT COUNT(*) FROM timeline_events WHERE kind = 'issue_create'"); n != 1 {
		t.Errorf("対照: 取り込みの後の起票の出来事 %d 件", n)
	}
}

// 取り込みで作ったプロジェクトにも通し番号の行がある（プロジェクトを作るときと同じ）。対照として、取り込む前には行が無い。
func TestTimelineImportCreatesHead(t *testing.T) {
	db := testutil.MigratedDB(t)
	ctx := context.Background()
	src, err := ReadSource(testdata.Root(t), []string{"mini"})
	if err != nil {
		t.Fatal(err)
	}
	heads := func() int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM timeline_heads h JOIN projects p ON p.id = h.project_id WHERE p.slug = 'mini'").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := heads(); n != 0 {
		t.Fatalf("前提が崩れています: 取り込む前に mini の行が %d 行", n)
	}
	if _, err := Import(ctx, db, src); err != nil {
		t.Fatal(err)
	}
	if n := heads(); n != 1 {
		t.Errorf("取り込んだプロジェクトの通し番号の行が %d 行", n)
	}
}
