package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/server"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/migrations"
)

// TestPrepareDBRefusesNewerDB は、この版が知らない番号の適用記録（新しい版で migrate した DB）があると、
// serve の起動前の準備（prepareDB）がチームのサーバでもローカルモードでも止まることを確かめる。
// チームのサーバは起動で migrate しないので、前の版の実行ファイルに戻して起動する経路はここでしか止まらない。
func TestPrepareDBRefusesNewerDB(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := store.Open(store.SQLitePrefix + filepath.Join(t.TempDir(), "im.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	team, local := server.Config{}, server.Config{LocalMode: true}

	// 対照: 適用記録の表がまだ無い DB と、全部知っている番号の DB は通る
	if err := prepareDB(ctx, team, db, logger); err != nil {
		t.Fatalf("表の無い DB（チーム）: %v", err)
	}
	if _, err := store.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []server.Config{team, local} {
		if err := prepareDB(ctx, cfg, db, logger); err != nil {
			t.Fatalf("今の版の DB（LocalMode=%v）: %v", cfg.LocalMode, err)
		}
	}

	if _, err := db.ExecContext(ctx, "INSERT INTO schema_migrations (version, name) VALUES (9999, '9999_future.sql')"); err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []server.Config{team, local} {
		err := prepareDB(ctx, cfg, db, logger)
		if err == nil {
			t.Fatalf("新しい版の DB を受け付けた（LocalMode=%v）", cfg.LocalMode)
		}
		ja, en := i18n.Text(i18n.JA, err), i18n.Text(i18n.EN, err)
		if !strings.Contains(ja, "新しい版の looptrack で migrate した DB") || !strings.Contains(ja, "9999_future.sql") {
			t.Errorf("LocalMode=%v（ja）: %s", cfg.LocalMode, ja)
		}
		if !strings.Contains(en, "It was migrated by a newer looptrack") || !strings.Contains(en, "9999_future.sql") {
			t.Errorf("LocalMode=%v（en）: %s", cfg.LocalMode, en)
		}
	}
}
