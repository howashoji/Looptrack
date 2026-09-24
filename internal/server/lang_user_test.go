package server

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// userWithLang は表示の言語だけを持つ利用者（DB を使わずに優先順を確かめるため）。
// set が false のときは「設定なし」（列が NULL）。
func userWithLang(set bool, lang string) *principal {
	return &principal{User: store.User{ID: 1, Login: "u", Lang: sql.NullString{String: lang, Valid: set}}}
}

// reqWith は、要求ごとの材料（?lang= / X-Looptrack-Lang / Accept-Language）と利用者を持つ要求。
func reqWith(url, explicit, accept string, p *principal) *http.Request {
	r := httptest.NewRequest(http.MethodGet, url, nil)
	if explicit != "" {
		r.Header.Set(langHeader, explicit)
	}
	if accept != "" {
		r.Header.Set("Accept-Language", accept)
	}
	if p != nil {
		r = r.WithContext(withPrincipal(r.Context(), p))
	}
	return r
}

// TestLangForPriority は優先順の 4 段（と、その要求だけ効く ?lang=）を 1 か所で固定する。
// 判定を書き分けた実装に戻したら、どこかの段でここが落ちる。
func TestLangForPriority(t *testing.T) {
	for _, c := range []struct {
		name                          string
		query, explicit, user, accept string
		want                          i18n.Lang
	}{
		{name: "何も無ければ英語", want: i18n.EN},
		{name: "④ Accept-Language だけ", accept: "ja-JP,ja;q=0.9", want: i18n.JA},
		{name: "③ 利用者の設定は Accept-Language より強い", user: "ja", accept: "en-US,en;q=0.9", want: i18n.JA},
		{name: "③ 利用者の設定（英語）も Accept-Language より強い", user: "en", accept: "ja-JP,ja;q=0.9", want: i18n.EN},
		{name: "② 明示の指定は利用者の設定より強い", explicit: "en", user: "ja", accept: "ja", want: i18n.EN},
		{name: "② 明示の指定（日本語）も利用者の設定より強い", explicit: "ja", user: "en", accept: "en", want: i18n.JA},
		{name: "① ?lang= はどれよりも強い", query: "en", explicit: "ja", user: "ja", accept: "ja", want: i18n.EN},
		{name: "読めない明示の指定は無視して利用者の設定へ", explicit: "zz", user: "ja", accept: "en", want: i18n.JA},
		{name: "読めない利用者の設定は無視して Accept-Language へ", user: "zz", accept: "ja", want: i18n.JA},
		{name: "設定なし（空）は Accept-Language へ落ちる", user: "", accept: "ja", want: i18n.JA},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := langFor(c.query, c.explicit, c.user, c.accept); got != c.want {
				t.Errorf("langFor(%q, %q, %q, %q) = %q（%q のはず）", c.query, c.explicit, c.user, c.accept, got, c.want)
			}
		})
	}
}

// TestReqLangUserSettingBeatsAcceptLanguage は、**利用者の設定を見る処理を外すと落ちる**テスト。
// reqLang から利用者の設定の段（userLang / principalFrom）を取り除くと、Accept-Language の英語が
// 選ばれて JA を期待するここが落ちる。
func TestReqLangUserSettingBeatsAcceptLanguage(t *testing.T) {
	r := reqWith("/", "", "en-US,en;q=0.9", userWithLang(true, "ja"))
	if got := reqLang(r); got != i18n.JA {
		t.Errorf("reqLang = %q（利用者の設定の ja のはず。Accept-Language の en に負けている）", got)
	}
}

// TestReqLangExplicitHeaderBeatsUserSetting は、端末で明示した言語（CLI が X-Looptrack-Lang で送る）が
// 利用者の設定より強いこと。
func TestReqLangExplicitHeaderBeatsUserSetting(t *testing.T) {
	r := reqWith("/", "en", "ja", userWithLang(true, "ja"))
	if got := reqLang(r); got != i18n.EN {
		t.Errorf("reqLang = %q（明示の en のはず）", got)
	}
}

// TestReqLangUnsetUserKeepsOldBehaviour は、**設定なし（列が NULL）の利用者が従来と同じ結果になる**こと。
// 実装前の reqLang は ?lang= → Accept-Language → 英語だったので、同じ表を当てる。
func TestReqLangUnsetUserKeepsOldBehaviour(t *testing.T) {
	for _, c := range []struct {
		name, url, accept string
		want              i18n.Lang
	}{
		{"何も無ければ英語", "/", "", i18n.EN},
		{"Accept-Language が日本語", "/", "ja-JP,ja;q=0.9,en;q=0.8", i18n.JA},
		{"Accept-Language が英語", "/", "en-US,en;q=0.9", i18n.EN},
		{"?lang=ja は Accept-Language より強い", "/?lang=ja", "en-US,en;q=0.9", i18n.JA},
		{"読めない ?lang は無視して Accept-Language へ", "/?lang=zz", "ja-JP,ja;q=0.9", i18n.JA},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := reqLang(reqWith(c.url, "", c.accept, userWithLang(false, ""))); got != c.want {
				t.Errorf("reqLang = %q（%q のはず。設定なしの利用者は従来どおり）", got, c.want)
			}
		})
	}
}

// TestUserLangNullAndEmptyAreDifferentStates は、**NULL（設定なし）と空文字を別の状態として扱う**こと。
// 列の状態は別（Valid が false か true か）で、言語の判定ではどちらも次の段へ落ちる
// （「設定なし」の表し方を NULL の 1 つに絞るため、空文字は書き込み側で NULL に直す。store.SetUserLang）。
func TestUserLangNullAndEmptyAreDifferentStates(t *testing.T) {
	null := userWithLang(false, "")
	empty := userWithLang(true, "")
	if null.User.Lang.Valid == empty.User.Lang.Valid {
		t.Fatal("NULL と空文字が同じ状態になっている（sql.NullString の Valid で区別できていない）")
	}
	if got := userLang(null); got != "" {
		t.Errorf("userLang(NULL) = %q（空文字のはず）", got)
	}
	if got := userLang(empty); got != "" {
		t.Errorf("userLang(空文字) = %q（空文字のはず）", got)
	}
}

// TestReqLangUserLangNullFallsThrough は NULL の場合の試験（空文字の場合は次の試験と対にする）。
func TestReqLangUserLangNullFallsThrough(t *testing.T) {
	if got := reqLang(reqWith("/", "", "ja", userWithLang(false, ""))); got != i18n.JA {
		t.Errorf("reqLang = %q（設定なしなので Accept-Language の ja のはず）", got)
	}
}

// TestReqLangUserLangEmptyStringFallsThrough は空文字の場合の試験（NULL の場合と別に持つ）。
// 空文字は「設定なし」ではなく「読めない値」として無視され、次の段へ落ちる。
func TestReqLangUserLangEmptyStringFallsThrough(t *testing.T) {
	if got := reqLang(reqWith("/", "", "ja", userWithLang(true, ""))); got != i18n.JA {
		t.Errorf("reqLang = %q（空文字は無視して Accept-Language の ja のはず）", got)
	}
}

// mcpReq は、ヘッダと利用者を持つ MCP のツール呼び出し（認証が TokenInfo に置く形に合わせる）。
func mcpReq(explicit, accept string, p *principal) *mcp.CallToolRequest {
	extra := &mcp.RequestExtra{Header: http.Header{}}
	if explicit != "" {
		extra.Header.Set(langHeader, explicit)
	}
	if accept != "" {
		extra.Header.Set("Accept-Language", accept)
	}
	if p != nil {
		extra.TokenInfo = &auth.TokenInfo{Extra: map[string]any{"principal": p}}
	}
	return &mcp.CallToolRequest{Extra: extra}
}

// TestMcpLangUsesUserSettingWithoutHeaders は、**MCP は要求にヘッダが 1 つも無くても利用者の設定で返る**こと
// （MCP の接続設定はヘッダを持てないことが多い）。利用者の設定の段を外すと落ちる。
func TestMcpLangUsesUserSettingWithoutHeaders(t *testing.T) {
	if got := mcpLang(mcpReq("", "", userWithLang(true, "ja"))); got != i18n.JA {
		t.Errorf("mcpLang = %q（利用者の設定の ja のはず。ヘッダが無くても設定で決まる）", got)
	}
	if got := mcpLang(mcpReq("", "", userWithLang(false, ""))); got != i18n.EN {
		t.Errorf("mcpLang = %q（設定なし・ヘッダも無しなので英語のはず）", got)
	}
	if got := mcpLang(mcpReq("en", "", userWithLang(true, "ja"))); got != i18n.EN {
		t.Errorf("mcpLang = %q（明示の en のはず）", got)
	}
	if got := mcpLang(mcpReq("", "ja", nil)); got != i18n.JA {
		t.Errorf("mcpLang = %q（認証前でも Accept-Language は効くはず）", got)
	}
}

// TestSetupMCPLangUsesUserSetting は setup / prompt の経路も同じ 1 つの関数を通ること。
func TestSetupMCPLangUsesUserSetting(t *testing.T) {
	extra := &mcp.RequestExtra{Header: http.Header{}}
	if got := setupMCPLang(extra, userWithLang(true, "ja")); got != i18n.JA {
		t.Errorf("setupMCPLang = %q（利用者の設定の ja のはず）", got)
	}
	if got := setupMCPLang(nil, userWithLang(true, "ja")); got != i18n.JA {
		t.Errorf("setupMCPLang(nil) = %q（ヘッダが無くても設定で決まるはず）", got)
	}
	if got := setupMCPLang(nil, nil); got != i18n.EN {
		t.Errorf("setupMCPLang(nil, nil) = %q（英語のはず）", got)
	}
}
