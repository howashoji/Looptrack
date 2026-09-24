package server

import (
	"database/sql"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// /account の画面の経路（GET /account と POST /account/lang）で、表示の言語を選んで保存でき、
// 「設定なし」（NULL）に戻せることを確かめる。
//
// 選択肢は**値（value）で見る**。画面の枠組みと選択肢の表示名はまだ日本語で直書きされているので、
// 表示名で判定すると、枠組みを 2 言語にする作業でこの検査が「日本語のまま」を固定してしまう。
// 保存した言語が実際に効くことは、/account の雛形ではなく、対訳表を通る応答（404 の画面）で見る
// （TestAccountLangAppliesToServerMessages）。

// langOptionRe は言語の選択の選択肢（値と、選ばれているかどうか）。
var langOptionRe = regexp.MustCompile(`<option value="([a-z]*)"( selected)?>`)

// accountLangOptions は /account の言語の選択から、選択肢の値を現れた順に返す（選ばれている値も返す）。
func accountLangOptions(t *testing.T, page string) (values []string, selected string) {
	t.Helper()
	const action = `action="/im/account/lang"`
	i := strings.Index(page, action)
	if i < 0 {
		t.Fatalf("言語の選択のフォーム（%s）が画面に無い", action)
	}
	rest := page[i:]
	from, to := strings.Index(rest, `<select name="lang">`), strings.Index(rest, "</select>")
	if from < 0 || to < from {
		t.Fatalf(`言語の選択（<select name="lang">）が画面に無い`)
	}
	found := false
	for _, m := range langOptionRe.FindAllStringSubmatch(rest[from:to], -1) {
		values = append(values, m[1])
		if m[2] != "" {
			selected, found = m[1], true
		}
	}
	if !found {
		t.Fatal("言語の選択に、選ばれている選択肢が無い")
	}
	return values, selected
}

// userLangColumn は users.lang の実際の値（NULL かどうかを含む）を返す。
func (e *env) userLangColumn(userID int64) sql.NullString {
	e.t.Helper()
	var v sql.NullString
	if err := e.db.QueryRow("SELECT lang FROM users WHERE id = ?", userID).Scan(&v); err != nil {
		e.t.Fatal(err)
	}
	return v
}

// wantUserLang は users.lang が期待どおりか（want が空文字なら NULL）を確かめる。
func (e *env) wantUserLang(userID int64, want, when string) {
	e.t.Helper()
	got := e.userLangColumn(userID)
	if want == "" {
		if got.Valid {
			e.t.Errorf("%s: users.lang = %q, want NULL", when, got.String)
		}
		return
	}
	if !got.Valid || got.String != want {
		e.t.Errorf("%s: users.lang = %v, want %q", when, got, want)
	}
}

// accountLangUser は TOTP まで登録した利用者と、そのクライアントを返す。
// 要求ごとに Accept-Language を付け分けたいので、既定のヘッダを持たないクライアントを使う。
func accountLangUser(t *testing.T, e *env) (*http.Client, int64) {
	t.Helper()
	u := e.user("alice", "alice-password-1", "member")
	c := enClient()
	e.enroll(c, "alice", "alice-password-1")
	return c, u.ID
}

// TestAccountLangPageSavesAndClears は画面の経路での保存・「設定なし」への復帰・拒否を確かめる。
func TestAccountLangPageSavesAndClears(t *testing.T) {
	e := newEnv(t)
	c, uid := accountLangUser(t, e)

	// 画面に言語の選択があり、「設定なし」（値が空の選択肢）が含まれる。初期はそれが選ばれている。
	_, page := e.get(c, "/im/account")
	values, selected := accountLangOptions(t, page)
	if want := []string{"", "ja", "en"}; !slices.Equal(values, want) {
		t.Errorf("言語の選択肢 = %q, want %q（値が空の選択肢が「設定なし」）", values, want)
	}
	if selected != "" {
		t.Errorf("初期に選ばれている値 = %q, want \"\"（設定なし）", selected)
	}
	e.wantUserLang(uid, "", "初期")

	// ①② 選んで保存すると、その値が列に入り、再取得の画面でも選ばれている。
	for _, save := range []string{"ja", "en"} {
		res, _ := e.formAt(c, "/im/account", "/im/account/lang", url.Values{"lang": {save}})
		if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/im/account?done=lang" {
			t.Fatalf("lang=%s の保存: %d %s", save, res.StatusCode, res.Header.Get("Location"))
		}
		e.wantUserLang(uid, save, "lang="+save+" の保存後")
		if _, selected := accountLangOptions(t, mustGet(t, e, c, "/im/account")); selected != save {
			t.Errorf("再取得で選ばれている値 = %q, want %q（保存した値が読み戻せていない）", selected, save)
		}
	}

	// ④ 読めない値は拒否され、**保存された値が変わらない**（拒否の応答を返しつつ書き換える実装を捕まえる）。
	// 値の制限は store.SetUserLang が持つので、画面の経路でもその規則が効いていることをここで見る。
	// （"JA" や "ja-JP" は i18n.Parse が正規形の "ja" に直して保存するので、ここでは使わない。
	// 拒否されるのは、どの言語にも読めない値だけ。）
	for _, bad := range []string{"fr", "zz"} {
		res, _ := e.formAt(c, "/im/account", "/im/account/lang", url.Values{"lang": {bad}})
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("lang=%q: %d, want %d", bad, res.StatusCode, http.StatusBadRequest)
		}
		e.wantUserLang(uid, "en", "lang="+bad+" を拒否した後")
	}

	// ⑤ CSRF が無い・違うと拒否され、保存された値も変わらない（ほかの /account の POST と同じ作り）。
	if res, _ := e.post(c, "/im/account/lang", url.Values{"lang": {"ja"}}); res.StatusCode != http.StatusForbidden {
		t.Errorf("CSRF なし: %d", res.StatusCode)
	}
	if res, _ := e.post(c, "/im/account/lang", url.Values{"csrf": {"wrong"}, "lang": {"ja"}}); res.StatusCode != http.StatusForbidden {
		t.Errorf("CSRF 違い: %d", res.StatusCode)
	}
	e.wantUserLang(uid, "en", "CSRF を拒否した後")

	// ③ 「設定なし」に戻すと列が NULL に戻り、画面でも「設定なし」が選ばれている。
	res, _ := e.formAt(c, "/im/account", "/im/account/lang", url.Values{"lang": {""}})
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/im/account?done=lang" {
		t.Fatalf("「設定なし」への戻し: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	e.wantUserLang(uid, "", "「設定なし」に戻した後")
	if _, selected := accountLangOptions(t, mustGet(t, e, c, "/im/account")); selected != "" {
		t.Errorf("戻した後に選ばれている値 = %q, want \"\"（設定なし）", selected)
	}
}

// TestAccountLangAppliesToServerMessages は、**保存できたことと効いたことは別の根拠**なので、
// 画面から保存した言語が、サーバが対訳表を通して出す文面にも効くことを 1 つだけ確かめる。
// 見るのは 404 の画面（i18n.T を通る）。/account の雛形そのものの文面は、まだ日本語で直書きされて
// いるので見ない（枠組みの 2 言語化は別の作業）。
func TestAccountLangAppliesToServerMessages(t *testing.T) {
	e := newEnv(t)
	c, uid := accountLangUser(t, e)

	// 画面から日本語を保存する。要求は英語（Accept-Language: en）で出すので、
	// 保存した設定を見ていなければ英語の文面が返る。
	if res, _ := e.formAt(c, "/im/account", "/im/account/lang", url.Values{"lang": {"ja"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("lang=ja の保存: %d", res.StatusCode)
	}
	e.wantUserLang(uid, "ja", "lang=ja の保存後")

	res, page := e.get(c, "/im/no-such-page", "Accept-Language", "en-US,en;q=0.9")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /im/no-such-page: %d, want %d", res.StatusCode, http.StatusNotFound)
	}
	if !strings.Contains(page, "ページが見つかりません") {
		t.Errorf("保存した日本語が対訳表を通る文面に効いていない（Accept-Language は英語）:\n%s", page)
	}
	if strings.Contains(page, "Page not found") {
		t.Errorf("英語の文面が返っている（利用者の設定より Accept-Language が勝っている）:\n%s", page)
	}
}

// mustGet は画面を取って本文だけを返す。
func mustGet(t *testing.T, e *env, c *http.Client, path string) string {
	t.Helper()
	res, page := e.get(c, path)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d", path, res.StatusCode)
	}
	return page
}
