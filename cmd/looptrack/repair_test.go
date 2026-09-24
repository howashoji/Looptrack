package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/mdformat"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/testutil"
)

// looptrack repair-lists の dry-run（表示だけ）と --apply（補正と記録）。本番と同じ権限のアプリ用ユーザーで動かす
// （本番ではコンテナの中の looptrack … がコンテナの LOOPTRACK_DSN = im_app で動くため）。
func TestRepairListsCmd(t *testing.T) {
	admin, app := testutil.AppDB(t)
	ctx := context.Background()
	add := func(slug string, docs ...string) {
		id, err := store.UpsertProject(ctx, admin, store.Project{Slug: slug, Prefix: strings.ToUpper(slug), Width: 4, Name: slug})
		if err != nil {
			t.Fatal(err)
		}
		for i, text := range docs {
			doc, err := mdformat.Parse(text)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.InsertDocument(ctx, admin, id, i+1, domain.FileName(domain.FromDocument(doc).ID, "x"), doc, "import"); err != nil {
				t.Fatal(err)
			}
		}
	}
	doc := func(id, status, labels, refs, blocked string) string {
		return "---\nid: " + id + "\ntitle: x\ntype: task\nstatus: " + status + "\npriority: P2\nlabels: [" + labels + "]\nparent: \nblocked_by: [" +
			blocked + "]\ntraces: []\nrefs: [" + refs + "]\ncreated: 2026-09-01 10:00\nupdated: 2026-09-01 10:00\n---\n\n# " + id + " x\n\n## コメント\n"
	}
	add("req", doc("REQ-0001", "Done", "", "BD-UI-047 NFR-UX-001, DEC-1", ""), doc("REQ-0002", "Todo", "", "FR-1", "REQ-0001 REQ-0003"))
	add("hp", doc("HP-0001", "Done", "CASE-101 保守リスク再監査 対応, R-04", "", ""))

	var out bytes.Buffer
	if err := repairLists(ctx, app, i18n.JA, &out, nil, domain.IDListFields, false, "r"); err != nil {
		t.Fatal(err)
	}
	want := "hp: 0 件\n" +
		"req REQ-0001 (Done) refs: [BD-UI-047 NFR-UX-001, DEC-1] → [BD-UI-047, NFR-UX-001, DEC-1]\n" +
		"req REQ-0002 (Todo) blocked_by: [REQ-0001 REQ-0003] → [REQ-0001, REQ-0003]\n" +
		"req: 2 件\n合計: 2 件（dry-run。書き込むには --apply を付けて再実行）\n"
	if out.String() != want {
		t.Errorf("dry-run:\n%s\nwant\n%s", out.String(), want)
	}
	var events int
	admin.QueryRow("SELECT COUNT(*) FROM issue_events").Scan(&events)
	if events != 0 {
		t.Errorf("dry-run で %d 件記録した", events)
	}

	// slug を限る・labels も指定できる（既定では対象にしない）
	out.Reset()
	if err := repairLists(ctx, app, i18n.JA, &out, []string{"hp"}, []string{"labels"}, false, "r"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "hp HP-0001 (Done) labels: [CASE-101 保守リスク再監査 対応, R-04] → [CASE-101, 保守リスク再監査, 対応, R-04]\nhp: 1 件\n") {
		t.Errorf("labels の dry-run: %s", out.String())
	}
	if err := repairLists(ctx, app, i18n.JA, &out, []string{"nope"}, domain.IDListFields, false, "r"); err == nil {
		t.Error("無いプロジェクトを受け付けた")
	}

	out.Reset()
	if err := repairLists(ctx, app, i18n.JA, &out, []string{"req"}, domain.IDListFields, true, "空白区切りで 1 要素に入った ID を分割"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out.String(), "req: 2 件\n合計: 2 件を補正しました（issue_events kind repair_lists・via admin に変更前後を記録）\n") {
		t.Errorf("apply: %s", out.String())
	}
	var vals []string // MySQL・SQLite の両方で通る形（GROUP_CONCAT の SEPARATOR は MySQL だけ）
	rows, err := admin.Query("SELECT v.value FROM issue_values v JOIN issues i ON i.id = v.issue_id WHERE i.display_id = 'REQ-0001' AND v.field = 'refs' ORDER BY v.pos")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var v string
		rows.Scan(&v)
		vals = append(vals, v)
	}
	rows.Close()
	if refs := strings.Join(vals, "|"); refs != "BD-UI-047|NFR-UX-001|DEC-1" {
		t.Errorf("補正後の refs: %s", refs)
	}
	var n int
	admin.QueryRow("SELECT COUNT(*) FROM issue_events WHERE kind = 'repair_lists' AND via = 'admin' AND JSON_EXTRACT(detail, '$.reason') = '空白区切りで 1 要素に入った ID を分割'").Scan(&n)
	if n != 2 {
		t.Errorf("記録 %d 件", n)
	}
	out.Reset()
	repairLists(ctx, app, i18n.JA, &out, nil, domain.IDListFields, false, "r")
	if !strings.HasSuffix(out.String(), "合計: 0 件（dry-run。書き込むには --apply を付けて再実行）\n") {
		t.Errorf("補正後の dry-run: %s", out.String())
	}
}
