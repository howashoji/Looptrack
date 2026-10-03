package main

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/dbgrants"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/testutil"
)

func grantsEnv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// TestGrantsPrintUsesDSNNames は looptrack grants print が LOOPTRACK_DSN の DB 名・利用者名で GRANT 文を出すことを確かめる
// （対照として、DSN が無ければ grants.sql と同じ im・im_app で出す）。
func TestGrantsPrintUsesDSNNames(t *testing.T) {
	noAsk := func(i18n.Lang, string, bool) (string, bool, error) {
		t.Fatal("print は尋ねない")
		return "", false, nil
	}
	var out, errb bytes.Buffer
	code := runGrants(context.Background(), []string{"print"},
		grantsEnv(map[string]string{"LOOPTRACK_LANG": "ja", "LOOPTRACK_DSN": "lt_app:pw@tcp(db:3306)/ltdb?parseTime=true"}), &out, &errb, noAsk)
	if code != 0 || !strings.Contains(out.String(), "ON ltdb.issues") || !strings.Contains(out.String(), "TO 'lt_app'@'%';") ||
		strings.Contains(out.String(), "im_app") || strings.Contains(out.String(), "pw") {
		t.Fatalf("DSN の名前: %d %q %q", code, out.String(), errb.String())
	}
	out.Reset()
	if code := runGrants(context.Background(), []string{"print"}, grantsEnv(map[string]string{"LOOPTRACK_LANG": "ja"}), &out, &errb, noAsk); code != 0 ||
		!strings.Contains(out.String(), "ON im.issues") || !strings.Contains(out.String(), "TO 'im_app'@'%';") {
		t.Fatalf("既定の名前: %d %q", code, out.String())
	}
}

// TestGrantsApplySQLiteAndNoTerminal は SQLite では何もせず 0 で終わり、MySQL で端末も --admin-password-file も無いときは
// 接続する前に止まることを確かめる。
func TestGrantsApplySQLiteAndNoTerminal(t *testing.T) {
	asked := 0
	noTTY := func(i18n.Lang, string, bool) (string, bool, error) { asked++; return "", false, nil }
	var out, errb bytes.Buffer
	if code := runGrants(context.Background(), []string{"apply"},
		grantsEnv(map[string]string{"LOOPTRACK_LANG": "ja", "LOOPTRACK_DSN": "sqlite:/tmp/x.db"}), &out, &errb, noTTY); code != 0 ||
		!strings.Contains(out.String(), "SQLite") || asked != 0 {
		t.Fatalf("SQLite: %d %q %q", code, out.String(), errb.String())
	}
	out.Reset()
	errb.Reset()
	code := runGrants(context.Background(), []string{"apply"},
		grantsEnv(map[string]string{"LOOPTRACK_LANG": "ja", "LOOPTRACK_DSN": "lt_app:pw@tcp(127.0.0.1:1)/ltdb"}), &out, &errb, noTTY)
	if code != 1 || !strings.Contains(errb.String(), "端末がありません") || asked != 1 {
		t.Fatalf("端末なし: %d %q %q", code, out.String(), errb.String())
	}
}

// TestGrantsCheckMissingTableAndApply は looptrack grants check が、grants.sql どおりの利用者では 0（対照）、
// attachments の権限だけが無い利用者（表が増えた更新で、projects は読める）では足りない表を出して 3 で終わり、
// looptrack grants apply が与え直した後はまた 0 になることを確かめる（apply の最後の確かめも同じ不足を見る）。
func TestGrantsCheckMissingTableAndApply(t *testing.T) {
	admin := testutil.MigratedDB(t)
	noAsk := func(i18n.Lang, string, bool) (string, bool, error) {
		t.Fatal("尋ねない（--admin-user と --admin-password-file を渡している）")
		return "", false, nil
	}
	var out, errb bytes.Buffer
	if testutil.SQLite() {
		// SQLite には利用者の権限が無い。確かめは何もせずに 0
		if code := runGrants(context.Background(), []string{"check"},
			grantsEnv(map[string]string{"LOOPTRACK_LANG": "ja", "LOOPTRACK_DSN": "sqlite:" + filepath.Join(t.TempDir(), "x.db")}), &out, &errb, noAsk); code != 0 ||
			!strings.Contains(out.String(), "SQLite") {
			t.Fatalf("SQLite: %d %q %q", code, out.String(), errb.String())
		}
		t.Skip("SQLite には利用者の権限が無い（MySQL の確かめは LOOPTRACK_TEST_DB=mysql で回す）")
	}
	ctx := context.Background()
	root, err := store.NormalizeDSN(os.Getenv("LOOPTRACK_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	var db string
	if err := admin.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&db); err != nil {
		t.Fatal(err)
	}
	user, pass := "lt_chk_"+db[len(db)-6:], "chk-"+db[len(db)-12:]+"Aa1!"
	if _, err := admin.ExecContext(ctx, "CREATE USER '"+user+"'@'%' IDENTIFIED BY '"+pass+"'"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec("DROP USER '" + user + "'@'%'") })
	stmts, err := dbgrants.Statements(db, user)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbgrants.Apply(ctx, admin, stmts); err != nil {
		t.Fatal(err)
	}
	app := root.Clone()
	app.User, app.Passwd, app.DBName = user, pass, db
	env := grantsEnv(map[string]string{"LOOPTRACK_LANG": "ja", "LOOPTRACK_DSN": app.FormatDSN()})
	check := func() int {
		out.Reset()
		errb.Reset()
		return runGrants(ctx, []string{"check"}, env, &out, &errb, noAsk)
	}

	// 対照: grants.sql どおりならそろっている
	if code := check(); code != 0 || !strings.Contains(out.String(), "アプリ用の利用者 "+user+" に全部の表の権限があります") {
		t.Fatalf("そろった利用者: %d %q %q", code, out.String(), errb.String())
	}
	// 表が増えた更新と同じ形: attachments の権限が無い（projects は読める）
	if _, err := admin.ExecContext(ctx, "REVOKE ALL PRIVILEGES ON "+db+".attachments FROM '"+user+"'@'%'"); err != nil {
		t.Fatal(err)
	}
	if code := check(); code != grantsCheckMissing || !strings.Contains(errb.String(), "attachments (SELECT, INSERT)") ||
		strings.Contains(errb.String(), "projects") {
		t.Fatalf("attachments が欠けた利用者: %d %q %q", code, out.String(), errb.String())
	}
	// apply で与え直すと、またそろう（管理用の資格情報はファイルで渡す）
	pw := filepath.Join(t.TempDir(), "admin.pw")
	if err := os.WriteFile(pw, []byte(root.Passwd+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errb.Reset()
	if code := runGrants(ctx, []string{"apply", "--admin-user", root.User, "--admin-password-file", pw, "--yes"}, env, &out, &errb, noAsk); code != 0 ||
		!strings.Contains(out.String(), "権限を与えました") || !strings.Contains(out.String(), "に全部の表の権限があります") {
		t.Fatalf("apply: %d %q %q", code, out.String(), errb.String())
	}
	if code := check(); code != 0 {
		t.Fatalf("apply の後: %d %q %q", code, out.String(), errb.String())
	}
}

// TestGrantsApplyVerifiesWithCheck は、grants apply の最後の確かめが grants check と同じ確かめ（checkGrants）を通り、
// そこで不足が出れば apply が失敗として終わることを確かめる。GRANT を流した後は実物の権限がそろうので、
// 確かめを差し替えて「流しても足りない」形を作る（読めることだけを確かめる形に戻すと落ちる）。
func TestGrantsApplyVerifiesWithCheck(t *testing.T) {
	admin := testutil.MigratedDB(t)
	if testutil.SQLite() {
		t.Skip("SQLite には利用者の権限が無い（MySQL の確かめは LOOPTRACK_TEST_DB=mysql で回す）")
	}
	ctx := context.Background()
	root, err := store.NormalizeDSN(os.Getenv("LOOPTRACK_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	var db string
	if err := admin.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&db); err != nil {
		t.Fatal(err)
	}
	user, pass := "lt_ver_"+db[len(db)-6:], "ver-"+db[len(db)-12:]+"Aa1!"
	t.Cleanup(func() { admin.Exec("DROP USER IF EXISTS '" + user + "'@'%'") })
	app := root.Clone()
	app.User, app.Passwd, app.DBName = user, pass, db
	env := grantsEnv(map[string]string{"LOOPTRACK_LANG": "ja", "LOOPTRACK_DSN": app.FormatDSN()})
	pw := filepath.Join(t.TempDir(), "admin.pw")
	if err := os.WriteFile(pw, []byte(root.Passwd+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	noAsk := func(i18n.Lang, string, bool) (string, bool, error) {
		t.Fatal("尋ねない")
		return "", false, nil
	}
	calls := 0
	orig := checkGrants
	t.Cleanup(func() { checkGrants = orig })
	checkGrants = func(ctx context.Context, a *sql.DB) ([]dbgrants.Requirement, error) {
		calls++
		return []dbgrants.Requirement{{Table: "attachments", Privs: []string{"SELECT"}}}, nil
	}
	var out, errb bytes.Buffer
	code := runGrants(ctx, []string{"apply", "--admin-user", root.User, "--admin-password-file", pw, "--yes"}, env, &out, &errb, noAsk)
	if code == 0 || calls != 1 || !strings.Contains(errb.String(), "attachments (SELECT)") || !strings.Contains(out.String(), "権限を与えました") {
		t.Fatalf("足りないと確かめたのに: %d 呼んだ回数 %d %q %q", code, calls, out.String(), errb.String())
	}
	// 対照: 本物の確かめに戻すと、同じ apply が通る
	checkGrants = orig
	out.Reset()
	errb.Reset()
	if code := runGrants(ctx, []string{"apply", "--admin-user", root.User, "--admin-password-file", pw, "--yes"}, env, &out, &errb, noAsk); code != 0 {
		t.Fatalf("対照: %d %q %q", code, out.String(), errb.String())
	}
}
