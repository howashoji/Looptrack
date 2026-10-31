package store

import (
	"context"
	"database/sql"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/howashoji/looptrack/migrations"
)

// migrationsBefore は migrations.FS のうち、番号が version より小さいものだけの FS を返す（古い形の DB を作るため）。
func migrationsBefore(t *testing.T, version string) fs.FS {
	t.Helper()
	out := fstest.MapFS{}
	err := fs.WalkDir(migrations.FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".sql") || path.Base(p) >= version {
			return err
		}
		b, err := fs.ReadFile(migrations.FS, p)
		if err != nil {
			return err
		}
		out[p] = &fstest.MapFile{Data: b}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// 0015 は既存の本文と要約を memory_bodies へ移し、版の行を残し、memory_versions から本文の列を外す。
func TestMigrateMovesMemoryBodies(t *testing.T) {
	run := func(t *testing.T, db *sql.DB) {
		ctx := context.Background()
		if _, err := Migrate(ctx, db, migrationsBefore(t, "0015")); err != nil {
			t.Fatal(err)
		}
		pid, err := CreateProject(ctx, db, Project{Slug: "mm", Prefix: "MM", Width: 4, Name: "mm"})
		if err != nil {
			t.Fatal(err)
		}
		res, err := db.ExecContext(ctx, `INSERT INTO memories (project_id, scope, owner_user_id, kind, name, version, summary, content, deleted)
VALUES (?, 'project', 0, 'handoff', 'main', 2, '二つ目', '二つ目の本文', FALSE)`, pid)
		if err != nil {
			t.Fatal(err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range []struct {
			n             int
			summary, body string
		}{{1, "一つ目", "一つ目の本文"}, {2, "二つ目", "二つ目の本文"}} {
			if _, err := db.ExecContext(ctx, `INSERT INTO memory_versions (memory_id, version, op, summary, content, machine_label, via)
VALUES (?, ?, 'append', ?, ?, 'box-a', 'cli')`, id, v.n, v.summary, v.body); err != nil {
				t.Fatal(err)
			}
		}

		applied, err := Migrate(ctx, db, migrations.FS)
		if err != nil {
			t.Fatal(err)
		}
		if len(applied) == 0 || applied[0] != "0015_memory_bodies.sql" { // 後の番号が続いても、0015 から適用することだけを見る
			t.Fatalf("適用した migrate: %v", applied)
		}
		vs, err := MemoryVersions(ctx, db, id)
		if err != nil || len(vs) != 2 || vs[0].Version != 2 || vs[1].Summary != "一つ目" || vs[1].MachineLabel != "box-a" || vs[1].Purged {
			t.Fatalf("版の行: %+v %v", vs, err)
		}
		v, err := MemoryVersionContent(ctx, db, id, 1)
		if err != nil || v.Content != "一つ目の本文" {
			t.Fatalf("移した本文: %+v %v", v, err)
		}
		if _, err := db.ExecContext(ctx, "SELECT content FROM memory_versions"); err == nil {
			t.Error("memory_versions に本文の列が残っている")
		}
	}
	t.Run("sqlite", func(t *testing.T) {
		db, err := Open(SQLitePrefix + filepath.Join(t.TempDir(), "m.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		run(t, db)
	})
	t.Run("mysql", func(t *testing.T) {
		db, _, _ := testDB(t)
		run(t, db)
	})
}

// MySQL の 0015 は DDL が巻き戻らないので、どの文の後で止まっても流し直せる。文ごとに途中まで流してから Migrate し、
// 本文が欠けず、列が消えることを確かめる。最後の 1 周は全部の文を流して適用の記録だけが残っていない形。
func TestMigrateMemoryBodiesResumes(t *testing.T) {
	b, err := fs.ReadFile(migrations.FS, "0015_memory_bodies.sql")
	if err != nil {
		t.Fatal(err)
	}
	stmts := SplitStatements(string(b))
	for cut := 1; cut <= len(stmts); cut++ {
		db, _, _ := testDB(t)
		ctx := context.Background()
		if _, err := Migrate(ctx, db, migrationsBefore(t, "0015")); err != nil {
			t.Fatal(err)
		}
		pid, err := CreateProject(ctx, db, Project{Slug: "mm", Prefix: "MM", Width: 4, Name: "mm"})
		if err != nil {
			t.Fatal(err)
		}
		res, err := db.ExecContext(ctx, `INSERT INTO memories (project_id, scope, owner_user_id, kind, name, version, summary, content, deleted)
VALUES (?, 'project', 0, 'handoff', 'main', 1, '要約', '本文', FALSE)`, pid)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		if _, err := db.ExecContext(ctx, `INSERT INTO memory_versions (memory_id, version, op, summary, content, via) VALUES (?, 1, 'append', '要約', '本文', 'cli')`, id); err != nil {
			t.Fatal(err)
		}
		conn, err := db.Conn(ctx) // SET @ と PREPARE は接続ごとなので 1 本で流す
		if err != nil {
			t.Fatal(err)
		}
		for _, st := range stmts[:cut] {
			if _, err := conn.ExecContext(ctx, st); err != nil {
				t.Fatalf("%d 文目まで: %v", cut, err)
			}
		}
		conn.Close()
		if _, err := Migrate(ctx, db, migrations.FS); err != nil {
			t.Fatalf("%d 文目で止めた後の再実行: %v", cut, err)
		}
		v, err := MemoryVersionContent(ctx, db, id, 1)
		if err != nil || v.Content != "本文" || v.Summary != "要約" {
			t.Fatalf("%d 文目で止めた後の本文: %+v %v", cut, v, err)
		}
		if _, err := db.ExecContext(ctx, "SELECT content FROM memory_versions"); err == nil {
			t.Fatalf("%d 文目で止めた後に列が残った", cut)
		}
	}
}

// SQLite は消した行を 0 で塗る（secure_delete）。無いと、消した本文がファイルの空き領域に残る。
func TestSQLiteSecureDelete(t *testing.T) {
	db, err := Open(SQLitePrefix + filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var on int
	if err := db.QueryRow("PRAGMA secure_delete").Scan(&on); err != nil || on != 1 {
		t.Fatalf("secure_delete = %d %v", on, err)
	}
}
