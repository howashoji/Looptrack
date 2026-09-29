package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
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
