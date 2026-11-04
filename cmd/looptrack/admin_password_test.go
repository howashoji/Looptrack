package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/auth"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// user add / passwd の --generate は入力を読まずに生成し、平文を 1 回だけ返す。付けなければこれまでどおり入力を読む（対照）。
func TestNewPasswordHashGenerate(t *testing.T) {
	reads := 0
	read := func(i18n.Lang) (string, error) {
		reads++
		return auth.HashPassword("typed-password-123")
	}

	hash, generated, err := newPasswordHash(i18n.EN, true, read)
	if err != nil {
		t.Fatal(err)
	}
	if reads != 0 {
		t.Fatalf("--generate なのに入力を %d 回読んだ", reads)
	}
	if len([]rune(generated)) < auth.MinPasswordLen {
		t.Fatalf("生成した値が短い: %d 文字", len([]rune(generated)))
	}
	if strings.Contains(hash, generated) {
		t.Fatal("ハッシュに平文が含まれる")
	}
	if ok, err := auth.VerifyPassword(hash, generated); err != nil || !ok {
		t.Fatalf("生成した値とハッシュが対にならない: %v %v", ok, err)
	}

	// 対照: 付けなければ入力の経路を通り、生成値は返らない
	hash, generated, err = newPasswordHash(i18n.EN, false, read)
	if err != nil || generated != "" || reads != 1 {
		t.Fatalf("入力の経路: generated=%t reads=%d err=%v", generated != "", reads, err)
	}
	if ok, _ := auth.VerifyPassword(hash, "typed-password-123"); !ok {
		t.Fatal("入力したパスワードのハッシュになっていない")
	}
}

// 生成値は 1 行で 1 回だけ出る。生成していなければ何も出さない。
func TestPrintGeneratedPassword(t *testing.T) {
	var buf bytes.Buffer
	printGeneratedPassword(&buf, i18n.EN, "")
	if buf.Len() != 0 {
		t.Fatalf("生成していないのに出力した: %d バイト", buf.Len())
	}
	pw, err := auth.GeneratePassword()
	if err != nil {
		t.Fatal(err)
	}
	for _, lang := range []i18n.Lang{i18n.EN, i18n.JA} {
		buf.Reset()
		printGeneratedPassword(&buf, lang, pw)
		out := buf.String()
		if strings.Count(out, pw) != 1 || strings.Count(out, "\n") != 1 {
			t.Fatalf("%s: 生成値が 1 行に 1 回ではない（%d 回・%d 行）", lang, strings.Count(out, pw), strings.Count(out, "\n"))
		}
	}
}

// user passwd の引数の読み方（oneArg と --generate の FlagSet）を userCmd 越しに通す。
// --generate の位置は利用者名の前でも後でもよく、どちらも生成値の行を 1 回だけ出してハッシュを変える。
// 引数が 0 個・2 個なら 0 以外で終わり、ハッシュは変えない（対照）。手入力の経路は TestNewPasswordHashGenerate が見ている。
func TestUserPasswdArgs(t *testing.T) {
	t.Setenv("LOOPTRACK_DSN", store.SQLitePrefix+filepath.Join(t.TempDir(), "p.db"))
	t.Setenv("LOOPTRACK_LANG", "en")
	if code, out, errOut := captureCmd(t, migrateCmd); code != 0 {
		t.Fatalf("migrate: %d\n%s%s", code, out, errOut)
	}
	ctx := context.Background()
	db, err := openDB()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	initial, err := auth.HashPassword("initial-password-123")
	if err != nil {
		t.Fatal(err)
	}
	for _, login := range []string{"alice", "bob"} {
		if _, err := store.CreateUser(ctx, db, login, login, initial, "member"); err != nil {
			t.Fatal(err)
		}
	}
	hashOf := func(login string) string {
		u, err := store.UserByLogin(ctx, db, login)
		if err != nil {
			t.Fatal(err)
		}
		return u.PasswordHash
	}

	for _, tc := range []struct {
		login string
		args  []string
	}{
		{"alice", []string{"passwd", "alice", "--generate"}},
		{"bob", []string{"passwd", "--generate", "bob"}},
	} {
		before := hashOf(tc.login)
		code, stdout, stderr := captureCmd(t, userCmd, tc.args...)
		if code != 0 {
			t.Fatalf("%v: 終了コード %d（stderr の長さ %d）", tc.args, code, len(stderr))
		}
		after := hashOf(tc.login)
		if after == before {
			t.Fatalf("%v: ハッシュが変わらない", tc.args)
		}
		// 出力の語のうち新しいハッシュと対になるものが生成値。ちょうど 1 語で、出力に 1 回だけ現れる。
		matched := 0
		for _, w := range strings.Fields(stdout) {
			if ok, _ := auth.VerifyPassword(after, w); ok {
				matched++
				if n := strings.Count(stdout, w); n != 1 {
					t.Fatalf("%v: 生成値が出力に %d 回出た", tc.args, n)
				}
			}
		}
		if matched != 1 {
			t.Fatalf("%v: 生成値の語が %d 個（1 個のはず）。出力 %d 行", tc.args, matched, strings.Count(stdout, "\n"))
		}
	}

	// 対照: 引数が 0 個・2 個は失敗し、ハッシュは変わらない
	for _, args := range [][]string{
		{"passwd", "--generate"},
		{"passwd", "alice", "bob", "--generate"},
	} {
		before := map[string]string{"alice": hashOf("alice"), "bob": hashOf("bob")}
		code, stdout, _ := captureCmd(t, userCmd, args...)
		if code == 0 {
			t.Fatalf("%v: 終了コード 0", args)
		}
		if strings.TrimSpace(stdout) != "" {
			t.Fatalf("%v: 失敗なのに標準出力が %d バイト", args, len(stdout))
		}
		for login, h := range before {
			if hashOf(login) != h {
				t.Fatalf("%v: %s のハッシュが変わった", args, login)
			}
		}
	}
}
