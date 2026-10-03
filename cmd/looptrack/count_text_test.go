package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

// exportThenImport は、SQLite の DB にイシュー issues 件とコメント comments 件を作り、export してから、
// 別の空の DB へ import する。LOOPTRACK_LANG は呼び出し側が決め、export と import の標準出力を返す。
func exportThenImport(t *testing.T, issues, comments int) (exportOut, importOut string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("LOOPTRACK_DSN", "sqlite:"+filepath.Join(dir, "a.db"))
	t.Setenv("LOOPTRACK_ATTACH_DIR", "")
	t.Setenv("STATE_DIRECTORY", "")
	if code, out, errOut := captureCmd(t, migrateCmd); code != 0 {
		t.Fatalf("migrate: %d\n%s%s", code, out, errOut)
	}
	db, err := openDB()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	pid, err := store.CreateProject(ctx, db, store.Project{Slug: "cnt", Prefix: "CNT", Width: 4, Name: "cnt"})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := store.ProjectByID(ctx, db, pid)
	uid, err := store.CreateUser(ctx, db, "ed", "ed", "x", "member")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetMember(ctx, db, pid, uid, "editor"); err != nil {
		t.Fatal(err)
	}
	svc := service.New(db, nil)
	a := service.Actor{UserID: uid, Via: "cli", Lang: i18n.JA}
	var first int64
	for i := 0; i < issues; i++ {
		it, err := svc.Create(ctx, a, p, service.CreateInput{Title: "t"}, i18n.JA)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = it.Row.ID
		}
	}
	for i := 0; i < comments; i++ {
		if _, err := svc.Comment(ctx, a, p, first, "c"); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	out := t.TempDir()
	code, exportOut, stderr := captureCmd(t, exportCmd, "--out", out)
	if code != 0 {
		t.Fatalf("export: %d\n%s%s", code, exportOut, stderr)
	}
	// export は設定（config.json）を書かないので、import が読めるよう添える
	if err := os.WriteFile(filepath.Join(out, "cnt", "config.json"), []byte(`{"prefix":"CNT","width":4,"name":"cnt"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOOPTRACK_DSN", "sqlite:"+filepath.Join(dir, "b.db"))
	if code, o, e := captureCmd(t, migrateCmd); code != 0 {
		t.Fatalf("migrate b: %d\n%s%s", code, o, e)
	}
	code, importOut, stderr = captureCmd(t, importCmd, "--root", out)
	if code != 0 {
		t.Fatalf("import: %d\n%s%s", code, importOut, stderr)
	}
	return exportOut, importOut
}

// export と import の完了の行は、件数が 1 のとき英語で単数になる（日本語は件数に関わらず同じ文面）。
// イシューとコメントの 2 つの件数は、それぞれ独立に単数と複数を選ぶ。
func TestExportImportDoneLinesSingularAndPlural(t *testing.T) {
	for _, c := range []struct {
		lang             string
		issues, comments int
		wantExport       string
		wantImport       string
	}{
		{"en", 1, 1, "Exported: 1 file to ", "Imported: cnt, 1 issue and 1 comment"},
		{"en", 2, 2, "Exported: 2 files to ", "Imported: cnt, 2 issues and 2 comments"},
		{"en", 1, 2, "Exported: 1 file to ", "Imported: cnt, 1 issue and 2 comments"},
		{"en", 2, 1, "Exported: 2 files to ", "Imported: cnt, 2 issues and 1 comment"},
		{"en", 1, 0, "Exported: 1 file to ", "Imported: cnt, 1 issue and 0 comments"},
		// 日本語の対照: 件数が 1 でも 2 でも同じ文面の型
		{"ja", 1, 1, "書き出し: 1 ファイル → ", "取り込み: cnt イシュー 1 件・コメント 1 件"},
		{"ja", 2, 2, "書き出し: 2 ファイル → ", "取り込み: cnt イシュー 2 件・コメント 2 件"},
	} {
		t.Setenv("LOOPTRACK_LANG", c.lang)
		exp, imp := exportThenImport(t, c.issues, c.comments)
		if !strings.Contains(exp, c.wantExport) {
			t.Errorf("%s 課題 %d・コメント %d の export: %q に %q が無い", c.lang, c.issues, c.comments, exp, c.wantExport)
		}
		if !strings.Contains(imp, c.wantImport) {
			t.Errorf("%s 課題 %d・コメント %d の import: %q に %q が無い", c.lang, c.issues, c.comments, imp, c.wantImport)
		}
	}
}
