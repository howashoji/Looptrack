package dbgrants

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/testutil"
)

// TestRequirementsCoverGrantsSQL は、Requirements が deploy/grants.sql の GRANT の行を 1 行ずつ表と権限にし、
// 取りこぼさないことを確かめる（確かめの対象が grants.sql の全部の表になる前提）。
func TestRequirementsCoverGrantsSQL(t *testing.T) {
	lines := grantLines(t)
	reqs, err := Requirements()
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != len(lines) {
		t.Fatalf("表の数: %d（grants.sql の GRANT は %d 行）", len(reqs), len(lines))
	}
	lineRe := regexp.MustCompile(`^GRANT\s+(.+?)\s+ON\s+im\.([a-z_]+)\s+TO`)
	for i, l := range lines {
		m := lineRe.FindStringSubmatch(l)
		if m == nil {
			t.Fatalf("%d 行目を読めない: %q", i, l)
		}
		want := strings.ReplaceAll(m[1], " ", "")
		if reqs[i].Table != m[2] || strings.Join(reqs[i].Privs, ",") != want {
			t.Errorf("%d 行目: %+v（表 %s・権限 %s のはず）", i, reqs[i], m[2], want)
		}
	}
}

// TestCheckFindsMissingPrivileges は、grants.sql どおりの利用者では Check が何も挙げず（対照）、
// 表が増えた更新と同じ形（attachments の権限だけが無い）と、ある表の権限が 1 つだけ欠けた形では、その表と権限を挙げることを確かめる。
// projects など、ほかの表が読めても見落とさないことが要点（読める表を 1 つ確かめるだけの確かめでは落ちる）。
func TestCheckFindsMissingPrivileges(t *testing.T) {
	admin, app := testutil.AppDB(t)
	if testutil.SQLite() {
		t.Skip("SQLite には利用者の権限が無い（Check は MySQL だけで使う）")
	}
	ctx := context.Background()
	var db, user string
	if err := admin.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&db); err != nil {
		t.Fatal(err)
	}
	if err := app.QueryRowContext(ctx, "SELECT SUBSTRING_INDEX(CURRENT_USER(), '@', 1)").Scan(&user); err != nil {
		t.Fatal(err)
	}
	countRows := func() int {
		var n int
		if err := admin.QueryRowContext(ctx, "SELECT COUNT(*) FROM projects").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := countRows()

	// 対照: grants.sql どおりならそろっている
	missing, err := Check(ctx, app)
	if err != nil || len(missing) != 0 {
		t.Fatalf("grants.sql どおりの利用者で不足を挙げた: %v %v", FormatMissing(missing), err)
	}

	// 表が増えた更新と同じ形: attachments の権限だけが無い（projects は読める）
	if _, err := admin.ExecContext(ctx, "REVOKE ALL PRIVILEGES ON "+db+".attachments FROM '"+user+"'@'%'"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := app.QueryRowContext(ctx, "SELECT COUNT(*) FROM projects").Scan(&n); err != nil {
		t.Fatalf("前提が崩れています: attachments を外した後に projects を読めない: %v", err)
	}
	missing, err = Check(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	if got := FormatMissing(missing); got != "attachments (SELECT, INSERT)" {
		t.Fatalf("attachments の不足: %q", got)
	}

	// 権限が 1 つだけ欠けた形（comments の INSERT だけ）。SELECT が読めても INSERT の不足を挙げる
	if _, err := admin.ExecContext(ctx, "REVOKE INSERT ON "+db+".comments FROM '"+user+"'@'%'"); err != nil {
		t.Fatal(err)
	}
	missing, err = Check(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	if got := FormatMissing(missing); got != "comments (INSERT); attachments (SELECT, INSERT)" {
		t.Fatalf("comments の INSERT と attachments の不足: %q", got)
	}

	// 与え直すと、またそろう（確かめは状態を見ている。結果を覚えていない）
	stmts, err := Statements(db, user)
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(ctx, admin, stmts); err != nil {
		t.Fatal(err)
	}
	if missing, err := Check(ctx, app); err != nil || len(missing) != 0 {
		t.Fatalf("与え直した後も不足を挙げた: %v %v", FormatMissing(missing), err)
	}
	if after := countRows(); after != before {
		t.Fatalf("確かめで行が変わった: projects %d → %d", before, after)
	}
}

// TestCheckFindsEachMissingPair は、grants.sql の（表, 権限）を 1 組ずつ外した利用者で、Check がその 1 組だけを不足として挙げる
// （多すぎも少なすぎもしない）ことを、全部の組で確かめる。最後の表を飛ばす・ある権限（DELETE など）を確かめない・
// ある権限の不足を別の権限の不足として数える、のどれでも落ちる。
func TestCheckFindsEachMissingPair(t *testing.T) {
	admin, app := testutil.AppDB(t)
	if testutil.SQLite() {
		t.Skip("SQLite には利用者の権限が無い（Check は MySQL だけで使う）")
	}
	ctx := context.Background()
	var db, user string
	if err := admin.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&db); err != nil {
		t.Fatal(err)
	}
	if err := app.QueryRowContext(ctx, "SELECT SUBSTRING_INDEX(CURRENT_USER(), '@', 1)").Scan(&user); err != nil {
		t.Fatal(err)
	}
	reqs, err := Requirements()
	if err != nil {
		t.Fatal(err)
	}
	pairs := 0
	for _, r := range reqs {
		for _, p := range r.Privs {
			pairs++
			on := db + "." + r.Table + " FROM '" + user + "'@'%'"
			if _, err := admin.ExecContext(ctx, "REVOKE "+p+" ON "+on); err != nil {
				t.Fatal(err)
			}
			missing, err := Check(ctx, app)
			if err != nil {
				t.Fatalf("%s の %s を外したとき: %v", r.Table, p, err)
			}
			if got, want := FormatMissing(missing), r.Table+" ("+p+")"; got != want {
				t.Errorf("%s の %s だけを外したのに: %q（%q のはず）", r.Table, p, got, want)
			}
			if _, err := admin.ExecContext(ctx, "GRANT "+p+" ON "+db+"."+r.Table+" TO '"+user+"'@'%'"); err != nil {
				t.Fatal(err)
			}
		}
	}
	// 前提: 全部の組を回した（grants.sql の組の数。いまは 73 組）
	if pairs < 70 {
		t.Fatalf("前提が崩れています: 回した組が %d しかない", pairs)
	}
	if missing, err := Check(ctx, app); err != nil || len(missing) != 0 {
		t.Fatalf("戻した後も不足を挙げた: %v %v", FormatMissing(missing), err)
	}
}
