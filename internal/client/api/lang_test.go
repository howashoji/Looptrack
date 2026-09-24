package api

import (
	"net/http"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
)

// TestOnLangReadsContentLanguage は、応答の Content-Language を OnLang に渡すこと。
//
// **読めたときだけ渡す**のが要点。ヘッダを返さない古いサーバでも i18n.Parse は（en, false）を返すので、
// ok を捨てて渡すと、日本語の利用者の手元が黙って英語に化ける。
func TestOnLangReadsContentLanguage(t *testing.T) {
	for _, c := range []struct {
		name, header string
		want         i18n.Lang
		called       bool
	}{
		{name: "日本語の宣言", header: "ja", want: i18n.JA, called: true},
		{name: "英語の宣言", header: "en", want: i18n.EN, called: true},
		{name: "宣言の形（ja-JP）も読む", header: "ja-JP", want: i18n.JA, called: true},
		{name: "ヘッダが無い（古いサーバ）", header: "", called: false},
		{name: "読めない言語", header: "fr", called: false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFake(t, func(w http.ResponseWriter, r *http.Request, n int) {
				if c.header != "" {
					w.Header().Set("Content-Language", c.header)
				}
				jsonReply(w, 200, `{}`)
			})
			cl := newClient(t, map[string]string{"LOOPTRACK_API_URL": f.URL + "/im", "LOOPTRACK_TOKEN": "imp_t"})
			var got i18n.Lang
			calls := 0
			cl.OnLang = func(l i18n.Lang) { got, calls = l, calls+1 }
			if _, err := cl.Do(Request{Method: "GET", Path: "/me"}); err != nil {
				t.Fatal(err)
			}
			if c.called {
				if calls != 1 || got != c.want {
					t.Errorf("OnLang の呼び出し %d 回・値 %q（1 回・%q のはず）", calls, got, c.want)
				}
				return
			}
			if calls != 0 {
				t.Errorf("OnLang が %d 回呼ばれた（読めない宣言では呼ばないはず。値 %q）", calls, got)
			}
		})
	}
}

// TestOnLangNilIsSafe は、配線していない Client（OnLang が nil）でも通ること。
func TestOnLangNilIsSafe(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, n int) {
		w.Header().Set("Content-Language", "ja")
		jsonReply(w, 200, `{}`)
	})
	cl := newClient(t, map[string]string{"LOOPTRACK_API_URL": f.URL + "/im", "LOOPTRACK_TOKEN": "imp_t"})
	if _, err := cl.Do(Request{Method: "GET", Path: "/me"}); err != nil {
		t.Fatal(err)
	}
}
