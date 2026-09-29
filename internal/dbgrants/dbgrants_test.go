package dbgrants

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// grantLines は deploy/grants.sql（リポジトリの実物）の GRANT の行。
func grantLines(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "deploy", "grants.sql"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "GRANT") {
			out = append(out, strings.TrimRight(l, " \r"))
		}
	}
	if len(out) < 20 {
		t.Fatalf("deploy/grants.sql の GRANT が %d 行しかない（読み方が壊れている）", len(out))
	}
	return out
}

// TestStatementsMatchGrantsSQL は、埋め込んだ grants.sql から組み立てた GRANT 文が、DB 名 im・利用者 im_app のとき
// deploy/grants.sql の GRANT の行とバイト列で同じになり、ほかの名前では DB 名と利用者だけが置き換わる（権限と表は同じ）ことを確かめる。
func TestStatementsMatchGrantsSQL(t *testing.T) {
	want := grantLines(t)
	got, err := Statements("im", "im_app")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("im・im_app の出力が deploy/grants.sql と違う:\n%s\n---\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	other, err := Statements("ltdb", "lt_app")
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != len(want) {
		t.Fatalf("行数: %d（%d のはず）", len(other), len(want))
	}
	norm := regexp.MustCompile(`\s+`)
	for i := range want {
		exp := strings.Replace(strings.Replace(want[i], " im.", " ltdb.", 1), "'im_app'@'%'", "'lt_app'@'%'", 1)
		if other[i] != exp {
			t.Errorf("%d 行目: %q（%q のはず）", i, other[i], exp)
		}
		if strings.Contains(other[i], "im.") || strings.Contains(other[i], "im_app") {
			t.Errorf("%d 行目に元の名前が残る: %q", i, other[i])
		}
		// 権限の並び（ON より前）は元と同じ
		if norm.ReplaceAllString(strings.SplitN(other[i], " ON ", 2)[0], " ") != norm.ReplaceAllString(strings.SplitN(want[i], " ON ", 2)[0], " ") {
			t.Errorf("%d 行目の権限が変わった: %q", i, other[i])
		}
	}
}

func TestStatementsRejectUnsafeNames(t *testing.T) {
	for _, c := range []struct{ db, user string }{
		{"im;DROP", "im_app"}, {"im`x", "im_app"}, {"", "im_app"}, {"im-db", "im_app"},
		{"im", "a'b"}, {"im", `a\b`}, {"im", "a b"}, {"im", ""},
	} {
		if _, err := Statements(c.db, c.user); err == nil {
			t.Errorf("%q・%q を通した", c.db, c.user)
		}
	}
	// 対照: 使える名前は通る
	if _, err := Statements("looptrack_prod", "lt.app-1"); err != nil {
		t.Errorf("使える名前を断った: %v", err)
	}
}

// fakeAdmin は ExecContext に渡された文を控える。
type fakeAdmin struct{ execs []string }

func (f *fakeAdmin) ExecContext(_ context.Context, q string, _ ...any) (sql.Result, error) {
	f.execs = append(f.execs, q)
	return nil, nil
}
func (f *fakeAdmin) QueryRowContext(context.Context, string, ...any) *sql.Row { return nil }

func TestCreateUserQuoting(t *testing.T) {
	ctx := context.Background()
	for _, pw := range []string{"", "a'b", `a\b`, "a\nb", "a\x00b"} {
		f := &fakeAdmin{}
		if err := CreateUser(ctx, f, "lt_app", pw); err == nil || len(f.execs) != 0 {
			t.Errorf("パスワード %q で作った: %v %v", pw, err, f.execs)
		}
	}
	// 対照: ふつうのパスワードは CREATE USER を 1 回流す
	f := &fakeAdmin{}
	if err := CreateUser(ctx, f, "lt_app", "S3cret-pass_word!"); err != nil || len(f.execs) != 1 ||
		f.execs[0] != "CREATE USER 'lt_app'@'%' IDENTIFIED BY 'S3cret-pass_word!'" {
		t.Errorf("CREATE USER: %v %q", err, f.execs)
	}
	if err := CreateDatabase(ctx, f, "bad name"); err == nil {
		t.Error("空白を含む DB 名で CREATE DATABASE を流した")
	}
}

func TestApplyStopsAtFirstError(t *testing.T) {
	stmts, _ := Statements("im", "im_app")
	e := &errAdmin{failAt: 3}
	if err := Apply(context.Background(), e, stmts); err == nil || e.n != 3 {
		t.Errorf("途中の失敗で止まらない: %v %d", err, e.n)
	}
}

type errAdmin struct{ n, failAt int }

func (e *errAdmin) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	e.n++
	if e.n == e.failAt {
		return nil, errors.New("denied")
	}
	return nil, nil
}
func (e *errAdmin) QueryRowContext(context.Context, string, ...any) *sql.Row { return nil }
