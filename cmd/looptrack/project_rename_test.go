package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/testutil"
)

// looptrack project rename は本番と同じ権限のアプリ用ユーザーで表示名だけを変え、日英の文面を出す。
func TestProjectRenameCmd(t *testing.T) {
	admin, app := testutil.AppDB(t)
	ctx := context.Background()
	if _, err := store.CreateProject(ctx, admin, store.Project{Slug: "demo", Prefix: "DEMO", Width: 4, Name: "旧い名前"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := projectRename(ctx, app, i18n.JA, &out, "demo", "Looptrack-dev"); err != nil {
		t.Fatal(err)
	}
	if want := "表示名を変更: demo（旧い名前 → Looptrack-dev）\n"; out.String() != want {
		t.Errorf("ja: %q want %q", out.String(), want)
	}
	out.Reset()
	if err := projectRename(ctx, app, i18n.EN, &out, "demo", "Looptrack-dev"); err != nil {
		t.Fatal(err)
	}
	if want := "No change: the display name of demo is already Looptrack-dev\n"; out.String() != want {
		t.Errorf("en: %q want %q", out.String(), want)
	}
	p, err := store.ProjectBySlug(ctx, admin, "demo")
	if err != nil || p.Name != "Looptrack-dev" || p.Prefix != "DEMO" || p.Width != 4 {
		t.Errorf("変更後: %+v %v", p, err)
	}
	if err := projectRename(ctx, app, i18n.JA, &out, "demo", "  "); err == nil {
		t.Error("空白だけの名前を受け付けた")
	}
	if err := projectRename(ctx, app, i18n.JA, &out, "nope", "x"); err == nil {
		t.Error("無いプロジェクトを受け付けた")
	}
}
