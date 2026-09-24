package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/howashoji/looptrack/migrations"
)

// langTestUser は SQLite の使い捨ての DB に利用者 1 人を作る（MySQL を使わずに列の状態を見るため）。
func langTestUser(t *testing.T) (*sql.DB, int64) {
	t.Helper()
	db, _ := sqliteTestDB(t)
	ctx := context.Background()
	if _, err := Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	id, err := CreateUser(ctx, db, "lang-user", "Lang User", "hash", "member")
	if err != nil {
		t.Fatal(err)
	}
	return db, id
}

// TestSetUserLangSavesAndClears は保存と「設定なし」への戻しを確かめる。
// 空文字を渡したときは **NULL に戻る**（空文字のままは保存しない）。
func TestSetUserLangSavesAndClears(t *testing.T) {
	d, id := langTestUser(t)
	ctx := context.Background()
	u, err := UserByID(ctx, d, id)
	if err != nil {
		t.Fatal(err)
	}
	if u.Lang.Valid {
		t.Fatalf("作ったばかりの利用者の lang が %#v（NULL のはず）", u.Lang)
	}
	if err := SetUserLang(ctx, d, id, "ja"); err != nil {
		t.Fatal(err)
	}
	u, err = UserByID(ctx, d, id)
	if err != nil {
		t.Fatal(err)
	}
	if !u.Lang.Valid || u.Lang.String != "ja" {
		t.Fatalf("lang = %#v（ja のはず）", u.Lang)
	}
	// ログイン名からの経路・一覧の経路でも同じ値が読めること（userColumns / scanUser の 1 か所に寄っている）
	if v, err := UserByLogin(ctx, d, "lang-user"); err != nil || v.Lang.String != "ja" {
		t.Fatalf("UserByLogin の lang = %#v err=%v", v.Lang, err)
	}
	us, err := ListUsers(ctx, d)
	if err != nil || len(us) == 0 || us[0].Lang.String != "ja" {
		t.Fatalf("ListUsers の lang = %#v err=%v", us, err)
	}
	if err := SetUserLang(ctx, d, id, ""); err != nil {
		t.Fatal(err)
	}
	u, err = UserByID(ctx, d, id)
	if err != nil {
		t.Fatal(err)
	}
	if u.Lang.Valid {
		t.Fatalf("「設定なし」に戻したのに lang = %#v（NULL のはず）", u.Lang)
	}
}

// TestUserLangNullIsNotEmptyString は、**NULL（設定なし）と空文字が別の状態として読める**ことを、
// 実際の列で確かめる（2 つの試験の結果が同じにならないこと）。
// 空文字は書き込みの経路では作れない（上の試験）が、外から入れられた場合に「設定なし」と混ざらないこと。
func TestUserLangNullIsNotEmptyString(t *testing.T) {
	d, id := langTestUser(t)
	ctx := context.Background()
	// ① NULL のとき
	null, err := UserByID(ctx, d, id)
	if err != nil {
		t.Fatal(err)
	}
	// ② 空文字を直に入れたとき（SetUserLang は通さない）
	if _, err := d.ExecContext(ctx, "UPDATE users SET lang = '' WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
	empty, err := UserByID(ctx, d, id)
	if err != nil {
		t.Fatal(err)
	}
	if null.Lang == empty.Lang {
		t.Fatalf("NULL と空文字が同じに読めている（%#v）。区別できないと「設定なし」が 2 通りになる", null.Lang)
	}
	if null.Lang.Valid {
		t.Errorf("NULL の lang が Valid=true（%#v）", null.Lang)
	}
	if !empty.Lang.Valid || empty.Lang.String != "" {
		t.Errorf("空文字の lang が %#v（Valid=true・空文字のはず）", empty.Lang)
	}
}

// TestSetUserLangRejectsNonCanonical は、**ja / en の正規形以外を SetUserLang が拒み、列を書き換えない**
// ことを確かめる。値の制限は（MySQL の CHECK が無い SQLite でも効くように）store にある。
//
// ここが無いと、2 つ目の書き込み経路が増えたときに任意の文字列が列に入る。読む側は読めない値を
// 「設定なし」として無視するので、**表示は壊れず、設定が黙って効かなくなるだけ**になり気づけない。
func TestSetUserLangRejectsNonCanonical(t *testing.T) {
	d, id := langTestUser(t)
	ctx := context.Background()
	// 先に正規形で ja を入れておく（拒んだときに前の値が残ることまで見る）
	if err := SetUserLang(ctx, d, id, "ja"); err != nil {
		t.Fatal(err)
	}
	// 実際に入りうる形: 別の言語・前後の空白・大文字・地域つき・3 文字・POSIX の値・改行
	for _, bad := range []string{"fr", "de", " ja ", "ja ", "JA", "En", "ja-JP", "ja_JP.UTF-8", "jpn", "posix", "c", "日本語", "ja\n", "ja;en", "' OR 1=1 --"} {
		t.Run(bad, func(t *testing.T) {
			err := SetUserLang(ctx, d, id, bad)
			if err == nil {
				t.Fatalf("SetUserLang(%q) がエラーを返さなかった（ja / en 以外は拒むはず）", bad)
			}
			u, err := UserByID(ctx, d, id)
			if err != nil {
				t.Fatal(err)
			}
			if !u.Lang.Valid || u.Lang.String != "ja" {
				t.Fatalf("拒んだのに列が %#v に変わった（ja のままのはず）", u.Lang)
			}
		})
	}
}

// TestSetUserLangAcceptsCanonical は、正規形の ja / en と「設定なし」への空文字が通ることを確かめる
// （上の試験の締めすぎで、正しい値まで拒むようになっていないこと）。
func TestSetUserLangAcceptsCanonical(t *testing.T) {
	d, id := langTestUser(t)
	ctx := context.Background()
	for _, want := range []string{"ja", "en"} {
		if err := SetUserLang(ctx, d, id, want); err != nil {
			t.Fatalf("SetUserLang(%q) が失敗した: %v", want, err)
		}
		u, err := UserByID(ctx, d, id)
		if err != nil {
			t.Fatal(err)
		}
		if !u.Lang.Valid || u.Lang.String != want {
			t.Fatalf("lang = %#v（%q のはず）", u.Lang, want)
		}
	}
	// 空文字は「設定なし」への戻しなので、拒まずに NULL にする（NULL と空文字の区別は変えない）
	if err := SetUserLang(ctx, d, id, ""); err != nil {
		t.Fatalf("空文字（設定なしへの戻し）が失敗した: %v", err)
	}
	u, err := UserByID(ctx, d, id)
	if err != nil {
		t.Fatal(err)
	}
	if u.Lang.Valid {
		t.Fatalf("空文字を渡したのに lang = %#v（NULL のはず）", u.Lang)
	}
}
