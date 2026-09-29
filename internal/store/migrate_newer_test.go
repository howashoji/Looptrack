package store

import (
	"context"
	"database/sql"
	"io/fs"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/deploy"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/migrations"
)

// olderFS は、このリポジトリの最後のマイグレーションを持たない「1 つ前の版」の材料と、落とした最後のファイル名を返す。
// 番号を数字で書かないのは、マイグレーションを足しても同じ場面（新しい版で migrate した DB を 1 つ前の版で開く）を作るため。
func olderFS(t *testing.T) (fs.FS, string) {
	t.Helper()
	migs, err := LoadMigrations(migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if len(migs) < 2 {
		t.Fatalf("マイグレーションが %d 本しかなく、1 つ前の版を作れない（前提が崩れている）", len(migs))
	}
	last := migs[len(migs)-1]
	return migrationsUpTo(t, migrations.FS, migs[len(migs)-2].Version), last.Name
}

// wantNewerRecords は、err が「新しい版で migrate した DB」の拒否で、日英の文面に落としたファイル名が出ることを確かめる。
func wantNewerRecords(t *testing.T, what string, err error, name string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: 新しい版で migrate した DB を受け付けた", what)
	}
	ja, en := i18n.Text(i18n.JA, err), i18n.Text(i18n.EN, err)
	if !strings.Contains(ja, "新しい版の looptrack で migrate した DB") || !strings.Contains(ja, name) {
		t.Errorf("%s（ja）: %s", what, ja)
	}
	if !strings.Contains(en, "It was migrated by a newer looptrack") || !strings.Contains(en, name) {
		t.Errorf("%s（en）: %s", what, en)
	}
}

func TestCheckAppliedRecordsRefusesUnknownVersions(t *testing.T) {
	migs := []Migration{{Version: 1, Name: "0001_init.sql"}, {Version: 2, Name: "0002_new.sql"}}
	// 対照: 全部知っている番号（と、まだ当てていない番号）なら通る
	for _, applied := range []map[int]string{
		{1: "0001_init.sql", 2: "0002_new.sql"},
		{1: "0001_init.sql"},
	} {
		if err := checkAppliedRecords(applied, migs, false); err != nil {
			t.Errorf("知っている番号だけの記録 %v: %v", applied, err)
		}
	}
	for _, sqlite := range []bool{false, true} {
		err := checkAppliedRecords(map[int]string{1: "0001_init.sql", 2: "0002_new.sql", 4: "0004_later.sql", 3: "0003_next.sql"}, migs, sqlite)
		wantNewerRecords(t, "知らない番号", err, "0003_next.sql 0004_later.sql")
		if ja := i18n.Text(i18n.JA, err); !strings.Contains(ja, "0002_new.sql") {
			t.Errorf("この版が知っている最後のマイグレーションが出ない: %s", ja)
		}
	}
	// 同じ番号で名前が違う記録は、知らない番号より先に record_mismatch で拒む（別の版の DB であって、新しい版とは限らない）
	err := checkAppliedRecords(map[int]string{1: "0001_init.sql", 2: "0002_other.sql", 3: "0003_next.sql"}, migs, false)
	if err == nil || !strings.Contains(i18n.Text(i18n.JA, err), "別の版のマイグレーション") {
		t.Errorf("名前の違う記録: %v", err)
	}
}

// checkNewerDB は、新しい版で migrate した DB を 1 つ前の版の Migrate と CheckApplied が拒み、
// 対照として同じ DB を今の版なら両方とも通すことを確かめる。
func checkNewerDB(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	older, last := olderFS(t)
	if err := CheckApplied(ctx, db, migrations.FS); err != nil {
		t.Fatalf("適用記録の表が無い DB の CheckApplied: %v", err)
	}
	if _, err := Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	// 対照: 全部知っている番号なら通る
	if applied, err := Migrate(ctx, db, migrations.FS); err != nil || len(applied) != 0 {
		t.Fatalf("今の版の 2 回目の Migrate: %v %v", applied, err)
	}
	if err := CheckApplied(ctx, db, migrations.FS); err != nil {
		t.Fatalf("今の版の CheckApplied: %v", err)
	}
	before := migrationRecords(t, db)
	applied, err := Migrate(ctx, db, older)
	if len(applied) != 0 {
		t.Errorf("1 つ前の版が適用した: %v", applied)
	}
	wantNewerRecords(t, "1 つ前の版の Migrate", err, last)
	wantNewerRecords(t, "1 つ前の版の CheckApplied", CheckApplied(ctx, db, older), last)
	if got := migrationRecords(t, db); got != before {
		t.Errorf("拒んだのに記録が変わった: %s → %s", before, got)
	}
}

func TestMigrateRefusesNewerDBMySQL(t *testing.T) {
	db, _, _ := testDB(t)
	checkNewerDB(t, db)
}

func TestMigrateRefusesNewerDBSQLite(t *testing.T) {
	db, _ := sqliteTestDB(t)
	checkNewerDB(t, db)
}

// TestCheckAppliedAsAppUser は、チームのサーバが動く利用者（grants.sql の権限だけ）でも適用記録を突き合わせられることを確かめる。
// 表が見えずに「記録が無い」と読むと、新しい版の DB を黙って通してしまう。
func TestCheckAppliedAsAppUser(t *testing.T) {
	db, dbName, adminCfg := testDB(t)
	ctx := context.Background()
	if _, err := Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	user, pass := "im_app_t_"+randHex(t, 4), randHex(t, 16)
	if _, err := db.Exec("CREATE USER '" + user + "'@'%' IDENTIFIED BY '" + pass + "'"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec("DROP USER '" + user + "'@'%'") })
	grants := strings.NewReplacer("im.", dbName+".", "'im_app'@'%'", "'"+user+"'@'%'").Replace(deploy.GrantsSQL)
	for _, stmt := range SplitStatements(grants) {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	appCfg := adminCfg.Clone()
	appCfg.User, appCfg.Passwd = user, pass
	app, err := sql.Open("mysql", appCfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err := CheckApplied(ctx, app, migrations.FS); err != nil {
		t.Fatalf("今の版（アプリ用の利用者）: %v", err)
	}
	older, last := olderFS(t)
	wantNewerRecords(t, "1 つ前の版（アプリ用の利用者）", CheckApplied(ctx, app, older), last)
}
