package server

import (
	"net/http"

	"github.com/howashoji/looptrack/internal/i18n"
)

// langQuery は、その要求だけ言語を変える問い合わせ（画面の確認・不具合の報告で使う）。
const langQuery = "lang"

// langHeader は、利用者が言語を明示したときにクライアントが送るヘッダ。
// CLI は LOOPTRACK_LANG が実際に設定されているときだけ送る（internal/client/api）。
// Accept-Language は環境変数が 1 つも無くても en を送るので、「明示したか」の判定には使えない。
const langHeader = "X-Looptrack-Lang"

// contentLanguageHeader は、その応答をどの言語で作ったかの宣言（RFC 9110 の Content-Language）。
const contentLanguageHeader = "Content-Language"

// langFor は表示の言語を決める**唯一の**場所（規則は 1 か所に置く。経路ごとに書き分けない）。
//
// 優先順:
//  1. query（?lang=。その要求だけ。画面を両方の言語で見たいとき。HTTP の要求だけが渡す）
//  2. explicit（X-Looptrack-Lang。利用者が明示した指定）
//  3. user（利用者の設定。users.lang。未設定は空文字で渡す）
//  4. accept（Accept-Language）
//  5. 既定は英語
//
// 読めない値（空文字・対応していない言語）は無視して次の手がかりへ進む。したがって
// 「設定なし（NULL）」の利用者は、この関数を通しても従来（Accept-Language → 英語）と同じ結果になる。
//
// サーバは同時に別々の利用者を相手にするので、**決めた言語は引数で持ち回る**
// （グローバル変数に「現在の言語」を置かない）。
func langFor(query, explicit, user, accept string) i18n.Lang {
	for _, v := range []string{query, explicit, user} {
		if lang, ok := i18n.Parse(v); ok {
			return lang
		}
	}
	return i18n.FromAcceptLanguage(accept)
}

// userLang は利用者が保存した表示の言語（設定なしなら空文字）。
func userLang(p *principal) string {
	if p == nil || !p.User.Lang.Valid {
		return ""
	}
	return p.User.Lang.String
}

// reqLang は HTTP の要求ごとの言語を決める。利用者の設定は、認証を通った要求の
// コンテキスト（withPrincipal）から引く。認証前・未ログインの要求では設定の段を飛ばす。
func reqLang(r *http.Request) i18n.Lang {
	if r == nil {
		return i18n.EN
	}
	return langFor(r.URL.Query().Get(langQuery), r.Header.Get(langHeader), userLang(principalFrom(r.Context())), r.Header.Get("Accept-Language"))
}

// setContentLanguage は、この要求で決めた言語を応答に宣言する（規則は 1 か所。呼ぶ側は経路ごとに書き分けない）。
// クライアントは、自分の手元で組み立てる文面の言語をこれに合わせられる。
//
// **利用者の設定（users.lang）はクライアントが知らない情報**なので、この宣言はクライアントが送った値の
// 反響にはならない（LOOPTRACK_LANG の無い利用者は、Accept-Language に en を送っても ja が返る）。
//
// 呼ぶ場所は withPrincipal を通した**後**の要求に限る。認証より前（ServeHTTP など）で呼ぶと
// principal が無く、利用者の設定の段を飛ばして送られてきた値をそのまま返すだけになる。
// 本文を書き始める前に呼ぶ（WriteHeader の後にヘッダを足しても応答には出ない）。
func setContentLanguage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(contentLanguageHeader, string(reqLang(r)))
}
