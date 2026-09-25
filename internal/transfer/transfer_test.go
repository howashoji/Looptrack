package transfer

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/mdformat"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/testdata"
	"github.com/howashoji/looptrack/internal/testutil"
)

func readSource(t *testing.T) []SourceProject {
	t.Helper()
	src, err := ReadSource(testdata.Root(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(src) != len(testdata.Slugs) {
		t.Fatalf("フィクスチャのプロジェクトが %d 件（want %d）", len(src), len(testdata.Slugs))
	}
	for _, p := range src {
		if len(p.Files) == 0 {
			t.Fatalf("%s: イシューファイルがありません", p.Project.Slug)
		}
	}
	return src
}

func mustVerify(t *testing.T, db any, reports []VerifyReport, err error) int {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	files := 0
	for _, r := range reports {
		files += r.Files
		for i, p := range r.Problems {
			if i < 10 {
				t.Errorf("%s: %s", r.Slug, p.In(i18n.JA))
			}
		}
		if len(r.Problems) > 10 {
			t.Errorf("%s: ほか %d 件", r.Slug, len(r.Problems)-10)
		}
	}
	return files
}

// TestImportVerifyFixtures は R1（取り込みと往復一致）の受け入れ条件を合成フィクスチャで検査する。
func TestImportVerifyFixtures(t *testing.T) {
	db := testutil.MigratedDB(t)
	ctx := context.Background()
	src := readSource(t)

	first, err := Import(ctx, db, src)
	if err != nil {
		t.Fatal(err)
	}
	reports, err := Verify(ctx, db, src)
	files := mustVerify(t, db, reports, err)

	// 2 回目の取り込みで件数・内容が変わらない
	second, err := Import(ctx, db, src)
	if err != nil {
		t.Fatal(err)
	}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("2 回目の取り込みで件数が変わった: %+v → %+v", first[i], second[i])
		}
	}
	reports, err = Verify(ctx, db, src)
	mustVerify(t, db, reports, err)

	var issues, comments, events int
	db.QueryRow("SELECT COUNT(*) FROM issues").Scan(&issues)
	db.QueryRow("SELECT COUNT(*) FROM comments").Scan(&comments)
	db.QueryRow("SELECT COUNT(*) FROM issue_events").Scan(&events)
	if issues != files || events != files {
		t.Errorf("issues=%d events=%d, want %d（2 回取り込んでも重複しない）", issues, events, files)
	}
	t.Logf("projects=%d files=%d comments=%d results=%+v", len(src), files, comments, second)
	checkColumns(t, db, src)

	// export したファイルが元とバイト一致（ファイル名のバイト列を含む）
	out := t.TempDir()
	n, err := Export(ctx, db, out, nil)
	if err != nil || n != files {
		t.Fatalf("export: n=%d err=%v", n, err)
	}
	for _, sp := range src {
		for _, f := range sp.Files {
			got, err := os.ReadFile(filepath.Join(out, sp.Project.Slug, filepath.FromSlash(f.Rel)))
			if err != nil {
				t.Errorf("export に無い: %s/%s", sp.Project.Slug, f.Rel)
				continue
			}
			if !bytes.Equal(got, f.Raw) {
				t.Errorf("export の内容が違う: %s/%s", sp.Project.Slug, f.Rel)
			}
		}
	}
}

// TestImportRefusesLiveProject は、API 等からの変更があるプロジェクトを置き換えないことを確かめる。
func TestImportRefusesLiveProject(t *testing.T) {
	db := testutil.MigratedDB(t)
	ctx := context.Background()
	src := readSource(t)[:1]
	if _, err := Import(ctx, db, src); err != nil {
		t.Fatal(err)
	}
	projects, _ := store.ListProjects(ctx, db)
	if _, err := db.Exec("INSERT INTO issue_events (project_id, kind, via) VALUES (?, 'comment', 'cli')", projects[0].ID); err != nil {
		t.Fatal(err)
	}
	_, err := Import(ctx, db, src)
	// 利用者に見せる文面は i18n.Text で作る（err.Error() は ID を返す）
	if err == nil || !strings.Contains(i18n.Text(i18n.JA, err), "置き換えを拒否") {
		t.Fatalf("err = %v, want 置き換えの拒否", err)
	}
}

// TestImportErrorKeepsFileName は、取り込めないファイルがあるとき、利用者に見せる文面（i18n.Text）に
// プロジェクトの slug とファイルの場所が残り、理由が利用者の言語で出ることを確かめる。
// 内側の理由が i18n.Error のとき、fmt.Errorf("%s: %w") で包むと i18n.Text は内側の文面だけを返し、
// どのファイルが悪いのかが落ちていた。日英の両方と、i18n の ID がそのまま出ないことを見る。
func TestImportErrorKeepsFileName(t *testing.T) {
	src := readSource(t)[:1]
	f := src[0].Files[0]
	cases := []struct {
		name string
		raw  []byte
		ja   string
		en   string
	}{
		// prefix の違う ID（store.ParseNumber）
		{"id_prefix", bytes.Replace(f.Raw, []byte("id: "+src[0].Project.Prefix+"-"), []byte("id: ZZZ-"), 1),
			"が prefix", "does not start with the prefix"},
		// frontmatter のキーの重複（DB へ入れる段の store.decompose が弾く）
		{"duplicate_key", bytes.Replace(f.Raw, []byte("\ntype: "), []byte("\ntype: task\ntype: "), 1),
			"が重複しています", "appears more than once"},
	}
	for _, tc := range cases {
		if bytes.Equal(tc.raw, f.Raw) {
			t.Fatalf("%s: フィクスチャを書き換えられない（前提が崩れている: id の行が見つからない）", tc.name)
		}
		db := testutil.MigratedDB(t)
		one := []SourceProject{src[0]}
		one[0].Files = []SourceFile{{Rel: f.Rel, Name: f.Name, Raw: tc.raw}}
		_, err := Import(context.Background(), db, one)
		if err == nil {
			t.Fatalf("%s: 取り込めてしまった", tc.name)
		}
		for _, l := range []struct {
			lang i18n.Lang
			want string
		}{{i18n.JA, tc.ja}, {i18n.EN, tc.en}} {
			got := i18n.Text(l.lang, err)
			if !strings.HasPrefix(got, src[0].Project.Slug+": "+f.Rel+": ") || !strings.Contains(got, l.want) || strings.Contains(got, "store.err.") || strings.Contains(got, "transfer.err.") {
				t.Errorf("%s %s: %q, want 先頭が %q で %q を含む", tc.name, l.lang, got, src[0].Project.Slug+": "+f.Rel+": ", l.want)
			}
		}
	}
}

// TestVerifyDetectsDifference は一致確認が差分を見逃さないことを確かめる。
func TestVerifyDetectsDifference(t *testing.T) {
	db := testutil.MigratedDB(t)
	ctx := context.Background()
	src := readSource(t)[:1]
	if _, err := Import(ctx, db, src); err != nil {
		t.Fatal(err)
	}
	tamperFirstComment(t, db)
	if _, err := db.Exec("UPDATE projects SET counter = counter + 1"); err != nil {
		t.Fatal(err)
	}
	reports, err := Verify(ctx, db, src)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, p := range reports[0].Problems {
		lines = append(lines, p.In(i18n.JA))
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "内容が違います") || !strings.Contains(joined, "counter が違います") {
		t.Fatalf("差分を検出しなかった: %q", joined)
	}
}

// tamperFirstComment は最初のコメントの末尾に空白を足す（DB 上の改ざんの再現）。
// comments は追記専用のため、SQLite ではトリガを外して書き換える（MySQL は管理者の接続なので通る）。
func tamperFirstComment(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck
	relock, err := store.UnlockAppendOnly(ctx, db, tx)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := tx.QueryRow("SELECT MIN(id) FROM comments").Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("UPDATE comments SET content = CONCAT(content, ' ') WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
	if err := relock(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// TestSaveRoundTripFixtures は API の更新経路（SaveDocument）と一覧用の読み込み（LoadFronts）が
// 旧形式のデータを壊さないことを確かめる。全イシューを読み直して同じ内容で保存し、verify が一致すること、
// frontmatter だけの読み込みが全文の読み込みと同じ frontmatter を返すことを検査する。
func TestSaveRoundTripFixtures(t *testing.T) {
	db := testutil.MigratedDB(t)
	ctx := context.Background()
	src := readSource(t)
	if _, err := Import(ctx, db, src); err != nil {
		t.Fatal(err)
	}
	projects, _ := store.ListProjects(ctx, db)
	saved := 0
	for _, p := range projects {
		full, err := store.LoadDocuments(ctx, db, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		fronts, err := store.LoadFronts(ctx, db, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(full) != len(fronts) {
			t.Fatalf("%s: 件数 %d != %d", p.Slug, len(full), len(fronts))
		}
		for i, s := range full {
			if a, b := mdformat.Render(&mdformat.Document{Front: s.Doc.Front}), mdformat.Render(&mdformat.Document{Front: fronts[i].Doc.Front}); a != b {
				t.Errorf("%s: frontmatter だけの読み込みが違う:\n%s\n%s", s.FileName, a, b)
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			row, err := store.LoadDocumentForUpdate(ctx, tx, s.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.SaveDocument(ctx, tx, row.ID, row.Version, row.Doc, len(row.Doc.Comments), store.Author{Via: "cli", At: time.Now()}); err != nil {
				t.Fatalf("%s: %v", s.FileName, err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			saved++
		}
	}
	reports, err := Verify(ctx, db, src)
	files := mustVerify(t, db, reports, err)
	var minVersion int
	db.QueryRow("SELECT MIN(version) FROM issues").Scan(&minVersion)
	if saved != files || minVersion != 2 {
		t.Errorf("saved=%d files=%d minVersion=%d", saved, files, minVersion)
	}
}

// checkColumns は取り込んだ DB の列（件数・状態・関係・コメント・タイムスタンプ・未知のキー）を、元ファイルを解釈した値と
// 突き合わせる。verify のバイト一致は再生成した全文の比較なので、列ごとの取り違え（例: traces と refs の入れ替え）を
// 全文が一致する範囲で見逃さないよう、列を直接確かめる。
func checkColumns(t *testing.T, db *sql.DB, src []SourceProject) {
	t.Helper()
	type row struct {
		file, status, parent, created, updated string
		values                                 map[string][]string
		comments                               []string // "時刻\x00本文"（seq の順）
		extra                                  map[string]string
	}
	known := []string{"id", "title", "type", "status", "priority", "labels", "parent", "blocked_by", "traces", "refs", "created", "updated"}
	for _, sp := range src {
		want := map[string]*row{}
		for _, f := range sp.Files {
			doc, err := mdformat.Parse(string(f.Raw))
			if err != nil {
				t.Fatal(err)
			}
			it := domain.FromDocument(doc)
			r := &row{file: f.Name, status: it.Status, parent: it.Parent, created: it.Created, updated: it.Updated,
				values: map[string][]string{"labels": it.Labels, "blocked_by": it.BlockedBy, "traces": it.Traces, "refs": it.Refs},
				extra:  map[string]string{}}
			for _, c := range doc.Comments {
				r.comments = append(r.comments, c.TS+"\x00"+c.Content)
			}
			for _, fl := range doc.Front {
				if !domain.Valid(known, fl.Key) {
					r.extra[fl.Key] = fl.Value
				}
			}
			want[it.ID] = r
		}

		got, byPK := map[string]*row{}, map[uint64]*row{}
		rows, err := db.Query(`SELECT i.id, i.display_id, i.file_name, i.status, i.parent, i.created, i.updated
FROM issues i JOIN projects p ON p.id = i.project_id WHERE p.slug = ? ORDER BY i.id`, sp.Project.Slug)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var pk uint64
			var id string
			r := &row{values: map[string][]string{}, extra: map[string]string{}}
			if err := rows.Scan(&pk, &id, &r.file, &r.status, &r.parent, &r.created, &r.updated); err != nil {
				t.Fatal(err)
			}
			got[id], byPK[pk] = r, r
		}
		rows.Close()
		for kind, q := range map[string]string{
			"values":   "SELECT v.issue_id, v.field, v.value FROM issue_values v JOIN issues i ON i.id = v.issue_id JOIN projects p ON p.id = i.project_id WHERE p.slug = ? ORDER BY v.issue_id, v.field, v.pos",
			"comments": "SELECT c.issue_id, c.ts, c.content FROM comments c JOIN issues i ON i.id = c.issue_id JOIN projects p ON p.id = i.project_id WHERE p.slug = ? ORDER BY c.issue_id, c.seq",
			"extra":    "SELECT e.issue_id, e.`key`, e.value FROM issue_extra e JOIN issues i ON i.id = e.issue_id JOIN projects p ON p.id = i.project_id WHERE p.slug = ? ORDER BY e.issue_id, e.`key`",
		} {
			rows, err := db.Query(q, sp.Project.Slug)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var pk uint64
				var a, b string
				if err := rows.Scan(&pk, &a, &b); err != nil {
					t.Fatal(err)
				}
				switch r := byPK[pk]; kind {
				case "values":
					r.values[a] = append(r.values[a], b)
				case "comments":
					r.comments = append(r.comments, a+"\x00"+b)
				default:
					r.extra[a] = b
				}
			}
			rows.Close()
		}

		if len(got) != len(want) {
			t.Errorf("%s: DB のイシュー %d 件、ファイル %d 件", sp.Project.Slug, len(got), len(want))
		}
		var nComments, nValues, nExtra int
		for id, w := range want {
			g, ok := got[id]
			if !ok {
				t.Errorf("%s: DB に無い", id)
				continue
			}
			if g.file != w.file || g.status != w.status || g.parent != w.parent || g.created != w.created || g.updated != w.updated {
				t.Errorf("%s: 列が違う: DB=%q %q %q %q %q ファイル=%q %q %q %q %q", id,
					g.file, g.status, g.parent, g.created, g.updated, w.file, w.status, w.parent, w.created, w.updated)
			}
			for field, v := range w.values {
				if strings.Join(g.values[field], "\x00") != strings.Join(v, "\x00") {
					t.Errorf("%s: %s が違う: DB=%q ファイル=%q", id, field, g.values[field], v)
				}
				nValues += len(v)
			}
			if strings.Join(g.comments, "\x01") != strings.Join(w.comments, "\x01") {
				t.Errorf("%s: コメント（時刻・本文・順序）が違う: DB %d 件 / ファイル %d 件", id, len(g.comments), len(w.comments))
			}
			if len(g.extra) != len(w.extra) {
				t.Errorf("%s: 未知のキーの数が違う: DB=%v ファイル=%v", id, g.extra, w.extra)
			}
			for k, v := range w.extra {
				if g.extra[k] != v {
					t.Errorf("%s: 未知のキー %s が違う: DB=%q ファイル=%q", id, k, g.extra[k], v)
				}
			}
			nComments += len(w.comments)
			nExtra += len(w.extra)
		}
		if nComments == 0 || nValues == 0 {
			t.Errorf("%s: コメント %d 件・関係の値 %d 件（フィクスチャが確かめる範囲を失っている）", sp.Project.Slug, nComments, nValues)
		}
		t.Logf("%s: issues=%d comments=%d values=%d extra=%d", sp.Project.Slug, len(want), nComments, nValues, nExtra)
	}
}

// TestImportKeepsNFDFileName は、濁点を分解した形（NFD。macOS で作ったファイル名にある）のファイル名を
// バイト列のまま取り込み・書き出しすることを確かめる。git（core.precomposeunicode）はコミット時に NFC へ
// 揃えてしまうため、フィクスチャのファイルとしては置けない。テストの中で NFD の名前を組み立てる。
func TestImportKeepsNFDFileName(t *testing.T) {
	db := testutil.MigratedDB(t)
	ctx := context.Background()
	src := readSource(t)[:1]
	f := src[0].Files[0]
	nfd := strings.Replace(f.Name, ".md", "-がぎ.md", 1) // 「がぎ」の NFD
	rel := strings.Replace(f.Rel, f.Name, nfd, 1)
	src[0].Files = []SourceFile{{Rel: rel, Name: nfd, Raw: f.Raw}}
	if _, err := Import(ctx, db, src); err != nil {
		t.Fatal(err)
	}
	var name []byte
	if err := db.QueryRow("SELECT file_name FROM issues").Scan(&name); err != nil || string(name) != nfd {
		t.Fatalf("DB のファイル名 = %q（%v）, want %q", name, err, nfd)
	}
	reports, err := Verify(ctx, db, src)
	if mustVerify(t, db, reports, err) != 1 {
		t.Fatal("verify の件数が 1 でない")
	}
	projects, _ := store.ListProjects(ctx, db)
	files, err := RenderProject(ctx, db, projects[0])
	if err != nil || len(files) != 1 || files[0].Rel != rel {
		t.Fatalf("書き出しのパス = %+v（%v）, want %q", files, err, rel)
	}
}
