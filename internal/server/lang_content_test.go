package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/howashoji/looptrack/internal/store"
)

// Content-Language（サーバが「この応答をどの言語で作ったか」を宣言するヘッダ）の試験。
//
// **対照の眼目**: 要求は `Accept-Language: en` だけを送り、明示の指定（X-Looptrack-Lang）は送らない。
// それでも `users.lang = ja` の利用者には `ja` が返る。利用者の設定はクライアントが知らない情報なので、
// これが返るということは「クライアントが送った値の反響」ではないことの証明になる
// （反響なら en が返る。ヘッダを認証より前の層で付けると、実際にそうなる）。

// setLang は利用者の表示の言語を保存する。
func setLang(t *testing.T, e *env, u store.User, lang string) {
	t.Helper()
	if err := store.SetUserLang(context.Background(), e.db, u.ID, lang); err != nil {
		t.Fatal(err)
	}
}

// TestContentLanguageIsUserSettingNotEcho は REST（通常モード）の経路。
func TestContentLanguageIsUserSettingNotEcho(t *testing.T) {
	e := newEnv(t)
	u := e.user("cl-ja", "cl-ja-password-1", "member")
	setLang(t, e, u, "ja")
	a := e.apiAs(u)

	// ① 対照: Accept-Language は en、明示の指定は無し → 利用者の設定の ja が返る（反響なら en）
	_, h, _ := a.do("GET", "/me", nil, "Accept-Language", "en-US,en;q=0.9")
	if got := h.Get("Content-Language"); got != "ja" {
		t.Errorf("Content-Language = %q（利用者の設定の ja のはず。en なら送った値の反響になっている）", got)
	}
	// ② 明示の指定はいちばん強い（利用者の設定より上）
	_, h, _ = a.do("GET", "/me", nil, "Accept-Language", "ja", "X-Looptrack-Lang", "en")
	if got := h.Get("Content-Language"); got != "en" {
		t.Errorf("Content-Language = %q（明示の en のはず）", got)
	}

	// ③ 設定なしの利用者は従来どおり Accept-Language で決まる
	v := e.user("cl-unset", "cl-unset-password-1", "member")
	b := e.apiAs(v)
	_, h, _ = b.do("GET", "/me", nil, "Accept-Language", "ja-JP,ja;q=0.9")
	if got := h.Get("Content-Language"); got != "ja" {
		t.Errorf("Content-Language = %q（設定なしなので Accept-Language の ja のはず）", got)
	}
	_, h, _ = b.do("GET", "/me", nil, "Accept-Language", "en-US,en;q=0.9")
	if got := h.Get("Content-Language"); got != "en" {
		t.Errorf("Content-Language = %q（設定なし・Accept-Language が en なので en のはず）", got)
	}
}

// TestContentLanguageOnWebPage は画面（通常モード）の経路。画面は実際に reqLang の言語で描かれるので、
// 同じ宣言が付くのが正しい。
func TestContentLanguageOnWebPage(t *testing.T) {
	e := newEnv(t)
	u := e.user("cl-web", "cl-web-password-1", "member")
	setLang(t, e, u, "ja")
	c := enClient() // Accept-Language を付けない（既定の英語）クライアント
	e.enroll(c, "cl-web", "cl-web-password-1")

	res, _ := e.get(c, "/im/account", "Accept-Language", "en-US,en;q=0.9")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("アカウント設定: %d", res.StatusCode)
	}
	if got := res.Header.Get("Content-Language"); got != "ja" {
		t.Errorf("画面の Content-Language = %q（利用者の設定の ja のはず）", got)
	}
}

// TestContentLanguageInLocalMode は、ローカルモードの API と画面にも同じ宣言が付くこと
// （経路によって効いたり効かなかったりしないことを、4 つの入口のうち残る 2 つで固定する）。
func TestContentLanguageInLocalMode(t *testing.T) {
	e := newLocalEnv(t)
	u := localSetup(t, e)
	setLang(t, e, u, "ja")

	res, _ := e.get(enClient(), "/im/api/v1/me", "Accept-Language", "en-US,en;q=0.9")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("me: %d", res.StatusCode)
	}
	if got := res.Header.Get("Content-Language"); got != "ja" {
		t.Errorf("ローカルモードの API の Content-Language = %q（利用者の設定の ja のはず）", got)
	}

	res, _ = e.get(enClient(), "/im/", "Accept-Language", "en-US,en;q=0.9")
	if got := res.Header.Get("Content-Language"); got != "ja" {
		t.Errorf("ローカルモードの画面の Content-Language = %q（利用者の設定の ja のはず。状態 %d）", got, res.StatusCode)
	}
}
