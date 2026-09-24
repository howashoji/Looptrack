package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// 二段階認証（TOTP）の必須 / 任意（DESIGN.md §3・§8）。
//
//	looptrack user add <login> --admin --two-factor required|optional   最初の利用者を作るときに決める（指定しなければ作らない）
//	looptrack settings two-factor                                       現在の設定と変更の記録を表示する
//	looptrack settings two-factor required|optional                     変更する（管理画面 <base path>/admin/security と同じ記録を残す）

// twoFactorHowTo は二段階認証の指定の仕方（誤りの文面に足す）。
func twoFactorHowTo(lang i18n.Lang) string { return i18n.T(lang, "cmd.two_factor.how_to") }

// checkFirstUserTwoFactor は、利用者が 0 人のとき（最初の利用者を作るとき）に二段階認証の指定を求める。
// 2 人目以降の指定は受け付けない（変更は settings two-factor で行い、記録を残すため）。
func checkFirstUserTwoFactor(ctx context.Context, db store.Queryer, lang i18n.Lang, twoFactor string) error {
	n, err := store.CountUsers(ctx, db)
	if err != nil {
		return err
	}
	if n > 0 {
		if twoFactor != "" {
			return i18n.Errorf("cmd.err.two_factor_not_first")
		}
		return nil
	}
	if twoFactor == "" {
		return i18n.Errorf("cmd.err.two_factor_missing", "how_to", twoFactorHowTo(lang))
	}
	if !store.ValidTwoFactor(twoFactor) {
		return i18n.Errorf("cmd.err.two_factor_invalid", "value", twoFactor, "how_to", twoFactorHowTo(lang))
	}
	return nil
}

// addUser は利用者を作る。最初の利用者なら二段階認証の設定も保存して記録する。表示するメッセージを返す。
func addUser(ctx context.Context, db *sql.DB, lang i18n.Lang, login, name, hash, role, twoFactor string) (string, error) {
	if err := checkFirstUserTwoFactor(ctx, db, lang, twoFactor); err != nil {
		return "", err
	}
	if _, err := store.CreateUser(ctx, db, login, name, hash, role); err != nil {
		return "", err
	}
	msg := i18n.T(lang, "cmd.user.created", "login", login, "role", role)
	if twoFactor != "" {
		// Note は書き込んだときの文面のまま記録に残る。looptrack setup・画面版の初回設定と同じ文面（provision.note.first_user）を
		// 動かした人の言語で書く（英語の利用者の記録に日本語が固定で残らないように）。
		if _, err := store.SetTwoFactorPolicy(ctx, db, twoFactor, store.SettingChange{
			Via: "command", Note: i18n.T(lang, "provision.note.first_user", "login", login, "source", "looptrack user add")}); err != nil {
			return "", i18n.Wrapf(err, "cmd.err.two_factor_save", "login", login, "value", twoFactor)
		}
		msg += i18n.T(lang, "cmd.user.two_factor_saved", "label", store.TwoFactorLabel(lang, twoFactor))
	}
	policy, _, err := store.TwoFactorPolicy(ctx, db)
	if err != nil {
		return "", err
	}
	if policy == store.TwoFactorRequired {
		msg += i18n.T(lang, "cmd.user.two_factor_required_note")
	} else {
		msg += i18n.T(lang, "cmd.user.two_factor_optional_note")
	}
	return msg, nil
}

func settingsCmd(args []string) int {
	const u = "settings two-factor [required|optional]"
	lang := cmdLang()
	if len(args) < 1 || args[0] != "two-factor" || len(args) > 2 {
		return usageErr(u)
	}
	ctx := context.Background()
	db, err := openDB()
	if err != nil {
		return fail(err)
	}
	defer db.Close()
	if len(args) == 1 {
		out, err := showTwoFactor(ctx, db, lang)
		if err != nil {
			return fail(err)
		}
		fmt.Print(out)
		return 0
	}
	msg, err := setTwoFactor(ctx, db, lang, args[1])
	if err != nil {
		return fail(err)
	}
	fmt.Println(msg)
	return 0
}

// setTwoFactor は管理コマンドから二段階認証の設定を変える（再認証は求めない: サーバ上の操作者は DB の資格情報を持つ）。
func setTwoFactor(ctx context.Context, db *sql.DB, lang i18n.Lang, value string) (string, error) {
	if !store.ValidTwoFactor(value) {
		return "", i18n.Errorf("cmd.err.two_factor_value", "value", value)
	}
	note := "looptrack settings two-factor"
	if who := os.Getenv("SUDO_USER"); who != "" {
		note += "（" + who + "）"
	}
	res, err := store.SetTwoFactorPolicy(ctx, db, value, store.SettingChange{Via: "command", Note: note})
	if err != nil {
		return "", err
	}
	if !res.Changed {
		return i18n.T(lang, "cmd.two_factor.unchanged", "label", store.TwoFactorLabel(lang, value)), nil
	}
	msg := i18n.T(lang, "cmd.two_factor.changed", "old", store.TwoFactorLabel(lang, res.Old), "new", store.TwoFactorLabel(lang, value))
	if value == store.TwoFactorRequired {
		msg += i18n.T(lang, "cmd.two_factor.revoked_sessions", "count", res.RevokedSessions)
	}
	return msg, nil
}

func showTwoFactor(ctx context.Context, db *sql.DB, lang i18n.Lang) (string, error) {
	policy, set, err := store.TwoFactorPolicy(ctx, db)
	if err != nil {
		return "", err
	}
	out := i18n.T(lang, "cmd.two_factor.show", "label", store.TwoFactorLabel(lang, policy))
	if !set {
		out += i18n.T(lang, "cmd.two_factor.show_unset")
	}
	out += "\n"
	changes, err := store.SettingChanges(ctx, db, store.SettingTwoFactor, 20)
	if err != nil {
		return "", err
	}
	for _, c := range changes {
		actor := c.ActorLogin
		if actor == "" {
			actor = "-"
		}
		out += fmt.Sprintf("  %s  %s → %s  %s  %s  %s\n", c.At.In(time.Local).Format("2006-01-02 15:04"),
			store.TwoFactorLabel(lang, c.OldValue), store.TwoFactorLabel(lang, c.NewValue), c.Via, actor, c.Note)
	}
	return out, nil
}
