package main

import (
	"context"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/testutil"
)

// 最初の利用者を user add で作ったときの二段階認証の変更記録の備考は、動かした人の言語で残る
// （looptrack setup・画面版の初回設定と同じ文面。英語の利用者の記録に日本語が固定で残らない）。
func TestFirstUserRecordFollowsLang(t *testing.T) {
	for _, tc := range []struct {
		lang i18n.Lang
		want string
	}{
		{i18n.EN, "Set when the first user alice was created (looptrack user add)"},
		{i18n.JA, "最初の利用者 alice の作成時に指定（looptrack user add）"},
	} {
		db := testutil.MigratedDB(t)
		if _, err := addUser(context.Background(), db, tc.lang, "alice", "Alice", "hash", "admin", "optional"); err != nil {
			t.Fatalf("%s: %v", tc.lang, err)
		}
		cs, _ := store.SettingChanges(context.Background(), db, store.SettingTwoFactor, 10)
		if len(cs) != 1 || cs[0].Note != tc.want {
			t.Errorf("%s: 記録 = %+v, want 備考 %q", tc.lang, cs, tc.want)
		}
	}
}

// 利用者 0 人で user add するとき、二段階認証の指定が無ければ作らずに指定方法を示す。指定すれば保存する。
// 条件 6（管理コマンド）: settings two-factor で変更でき、記録が残る。
func TestFirstUserNeedsTwoFactor(t *testing.T) {
	db := testutil.MigratedDB(t)
	ctx := context.Background()

	for _, v := range []string{"", "maybe"} {
		_, err := addUser(ctx, db, i18n.JA, "alice", "Alice", "hash", "admin", v)
		if err == nil || !strings.Contains(i18n.Text(i18n.JA, err), "--two-factor required") || !strings.Contains(i18n.Text(i18n.JA, err), "--two-factor optional") {
			t.Errorf("%q: err = %v, want 指定方法を示すエラー", v, err)
		}
		if err := checkFirstUserTwoFactor(ctx, db, i18n.JA, v); err == nil {
			t.Errorf("%q: 事前検査が通った", v)
		}
	}
	if n, _ := store.CountUsers(ctx, db); n != 0 {
		t.Fatalf("指定なしで利用者が作られた: %d 人", n)
	}

	msg, err := addUser(ctx, db, i18n.JA, "alice", "Alice", "hash", "admin", "optional")
	if err != nil || !strings.Contains(msg, "任意") {
		t.Fatalf("msg = %q err = %v", msg, err)
	}
	policy, set, _ := store.TwoFactorPolicy(ctx, db)
	if !set || policy != store.TwoFactorOptional {
		t.Errorf("設定 = %q set=%v", policy, set)
	}
	cs, _ := store.SettingChanges(ctx, db, store.SettingTwoFactor, 10)
	if len(cs) != 1 || cs[0].Via != "command" || cs[0].OldValue != "" || cs[0].NewValue != "optional" || !strings.Contains(cs[0].Note, "alice") {
		t.Errorf("記録 = %+v", cs)
	}

	// 2 人目以降は指定不要。指定したら拒否する（変更は settings two-factor で）
	if _, err := addUser(ctx, db, i18n.JA, "bob", "", "hash", "member", "required"); err == nil || !strings.Contains(i18n.Text(i18n.JA, err), "settings two-factor") {
		t.Errorf("2 人目の --two-factor: %v", err)
	}
	if _, err := addUser(ctx, db, i18n.JA, "bob", "", "hash", "member", ""); err != nil {
		t.Errorf("2 人目: %v", err)
	}

	// 管理コマンドでの変更
	if _, err := setTwoFactor(ctx, db, i18n.JA, "nope"); err == nil {
		t.Error("不正な値を受け付けた")
	}
	msg, err = setTwoFactor(ctx, db, i18n.JA, "required")
	if err != nil || !strings.Contains(msg, "任意 → 必須") {
		t.Errorf("msg = %q err = %v", msg, err)
	}
	if msg, _ := setTwoFactor(ctx, db, i18n.JA, "required"); !strings.Contains(msg, "変更なし") {
		t.Errorf("同じ値: %q", msg)
	}
	out, err := showTwoFactor(ctx, db, i18n.JA)
	if err != nil || !strings.HasPrefix(out, "二段階認証: 必須\n") || strings.Count(out, "command") != 2 {
		t.Errorf("show = %q err = %v", out, err)
	}
}

// 利用者 0 人でも --admin を付けない最初の利用者にも指定を求める（決めないまま運用に入れない）。
func TestFirstMemberAlsoNeedsTwoFactor(t *testing.T) {
	db := testutil.MigratedDB(t)
	if _, err := addUser(context.Background(), db, i18n.JA, "bob", "", "hash", "member", ""); err == nil {
		t.Error("指定なしで最初の利用者を作った")
	}
}
