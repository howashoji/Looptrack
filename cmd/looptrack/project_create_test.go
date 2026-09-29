package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/migrations"
)

// TestProjectCreateCmdFillsDefaults は、サーバの CLI の looptrack project create が prefix・表示名・並び順を
// 省略できる（service.NewProjectDefaults で MCP・管理画面・setup と同じ既定値を埋める）ことを確かめる。
// 明示した値はそのまま使われることもあわせて確かめる。
func TestProjectCreateCmdFillsDefaults(t *testing.T) {
	ctx := context.Background()
	dsn := store.SQLitePrefix + filepath.Join(t.TempDir(), "im.db")
	setup, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Migrate(ctx, setup, migrations.FS); err != nil {
		t.Fatal(err)
	}
	setup.Close() // projectAdminCmd は LOOPTRACK_DSN から自分で開いて閉じる
	t.Setenv("LOOPTRACK_DSN", dsn)

	if code := projectAdminCmd([]string{"create", "myproj"}, i18n.JA, "usage"); code != 0 {
		t.Fatalf("prefix・name・order を省略した create が失敗（code=%d）", code)
	}
	if code := projectAdminCmd([]string{"create", "other", "--prefix", "OTH", "--name", "Other", "--order", "5"}, i18n.JA, "usage"); code != 0 {
		t.Fatalf("明示した値での create が失敗（code=%d）", code)
	}

	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	p, err := store.ProjectBySlug(ctx, db, "myproj")
	if err != nil {
		t.Fatal(err)
	}
	if p.Prefix != "MYPROJ" || p.Name != "myproj" || p.SortOrder != store.DefaultProjectSortOrder {
		t.Errorf("既定値が MCP・管理画面・setup とそろっていない: prefix=%q name=%q sort_order=%d（want MYPROJ / myproj / %d）",
			p.Prefix, p.Name, p.SortOrder, store.DefaultProjectSortOrder)
	}

	p2, err := store.ProjectBySlug(ctx, db, "other")
	if err != nil {
		t.Fatal(err)
	}
	if p2.Prefix != "OTH" || p2.Name != "Other" || p2.SortOrder != 5 {
		t.Errorf("明示した値が上書きされた: prefix=%q name=%q sort_order=%d（want OTH / Other / 5）", p2.Prefix, p2.Name, p2.SortOrder)
	}
}

// TestProjectCreateCmdOrderZeroExplicit は、--order 0 を明示しても既定値 100 に置き換わらず、
// 48a939ee までの looptrack project create --order 0 と同じく 0 のまま保存されることを確かめる。
func TestProjectCreateCmdOrderZeroExplicit(t *testing.T) {
	ctx := context.Background()
	dsn := store.SQLitePrefix + filepath.Join(t.TempDir(), "im.db")
	setup, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Migrate(ctx, setup, migrations.FS); err != nil {
		t.Fatal(err)
	}
	setup.Close()
	t.Setenv("LOOPTRACK_DSN", dsn)

	if code := projectAdminCmd([]string{"create", "zero-order", "--order", "0"}, i18n.JA, "usage"); code != 0 {
		t.Fatalf("--order 0 の create が失敗（code=%d）", code)
	}
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p, err := store.ProjectBySlug(ctx, db, "zero-order")
	if err != nil {
		t.Fatal(err)
	}
	if p.SortOrder != 0 {
		t.Errorf("明示した --order 0 が %d に置き換わった（want 0）", p.SortOrder)
	}
}

// TestProjectCreateCmdWidthZeroExplicitRejected は、--width 0 を明示すると（既定値に静かに置き換わらず）
// 範囲検査（1〜9）の誤りとして拒まれることを確かめる（48a939ee までと同じ「常に誤り」に戻す。並び順と
// 同じ形の制約なので同じ扱いにした）。
func TestProjectCreateCmdWidthZeroExplicitRejected(t *testing.T) {
	ctx := context.Background()
	dsn := store.SQLitePrefix + filepath.Join(t.TempDir(), "im.db")
	setup, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Migrate(ctx, setup, migrations.FS); err != nil {
		t.Fatal(err)
	}
	setup.Close()
	t.Setenv("LOOPTRACK_DSN", dsn)

	if code := projectAdminCmd([]string{"create", "zero-width", "--width", "0"}, i18n.JA, "usage"); code == 0 {
		t.Fatal("--width 0 の create が成功した（範囲検査で拒むはず）")
	}
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := store.ProjectBySlug(ctx, db, "zero-width"); err == nil {
		t.Error("拒んだはずの --width 0 でプロジェクトが作られた")
	}
}
