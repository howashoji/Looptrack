package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
)

// TestReqLang は要求ごとの言語の決め方を固定する。
// 同時に別々の利用者を相手にするので、要求から決まることが要点（前の要求の言語が残らない）。
func TestReqLang(t *testing.T) {
	for _, c := range []struct {
		name   string
		url    string
		accept string
		want   i18n.Lang
	}{
		{"何も無ければ英語", "/", "", i18n.EN},
		{"Accept-Language が日本語", "/", "ja-JP,ja;q=0.9,en;q=0.8", i18n.JA},
		{"Accept-Language が英語", "/", "en-US,en;q=0.9", i18n.EN},
		{"?lang=ja は Accept-Language より強い", "/?lang=ja", "en-US,en;q=0.9", i18n.JA},
		{"?lang=en は Accept-Language より強い", "/?lang=en", "ja-JP,ja;q=0.9", i18n.EN},
		{"読めない ?lang は無視して Accept-Language へ", "/?lang=zz", "ja-JP,ja;q=0.9", i18n.JA},
		{"空の ?lang は無視して Accept-Language へ", "/?lang=", "ja-JP,ja;q=0.9", i18n.JA},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, c.url, nil)
			if c.accept != "" {
				r.Header.Set("Accept-Language", c.accept)
			}
			if got := reqLang(r); got != c.want {
				t.Errorf("reqLang = %q（%q のはず）", got, c.want)
			}
		})
	}
}

// TestReqLangNil は、要求が無いとき（背景の処理から呼ばれた場合）に落ちないことを確かめる。
func TestReqLangNil(t *testing.T) {
	if got := reqLang(nil); got != i18n.EN {
		t.Errorf("reqLang(nil) = %q（英語のはず）", got)
	}
}
