package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/migrations"
)

// TestMigrateCheck は looptrack migrate --check が何も適用せず、未適用があれば 3・無ければ 0 で終わることを確かめる
// （install.sh の無人の更新が、置き換えの前に新しい版が DB の形を変えるかを知るのに使う）。
func TestMigrateCheck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "im.db")
	t.Setenv("LOOPTRACK_DSN", store.SQLitePrefix+path)
	t.Setenv("LOOPTRACK_LANG", "ja")
	if code := migrateCmd([]string{"--check"}); code != migrateExitPending {
		t.Fatalf("表の無い DB: 終了コード %d, want %d", code, migrateExitPending)
	}
	// 何も適用していない（対照: この後の migrate で適用される）
	db, err := store.Open(store.SQLitePrefix + path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	pending, recorded, err := store.Pending(context.Background(), db, migrations.FS)
	if err != nil || recorded != 0 || len(pending) == 0 {
		t.Fatalf("--check の後に適用記録ができた: 未適用 %d・記録 %d・%v", len(pending), recorded, err)
	}
	if code := migrateCmd(nil); code != 0 {
		t.Fatalf("migrate: %d", code)
	}
	if code := migrateCmd([]string{"--check"}); code != 0 {
		t.Errorf("最新の DB: 終了コード %d, want 0", code)
	}
	if code := migrateCmd([]string{"--dry-run"}); code != 2 {
		t.Errorf("不明な引数: 終了コード %d, want 2", code)
	}
}
