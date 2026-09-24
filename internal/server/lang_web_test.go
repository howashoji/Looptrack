package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"regexp"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/store"
)

// noticeLine は画面の知らせの行（2 言語化した部分だけを見るため。画面の枠組みはまだ日本語）。
func noticeLine(page string) string {
	for _, line := range strings.Split(page, "\n") {
		if strings.Contains(line, `class="notice"`) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// jaRe は日本語（ひらがな・カタカナ・漢字）。英語で出したときに混ざっていないことを見る。
var jaRe = regexp.MustCompile(`[ぁ-んァ-ヶ一-龥]`)

// enClient は英語の利用者としてのクライアント（Accept-Language を付けない＝既定の英語）。
func enClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// TestWebInEnglish は、2 言語化した画面の文面が英語の利用者には英語で出ることを確かめる。
// 既定は英語（Accept-Language が無い＝英語）。テストの client() は日本語を付けているので、
// ここでは付けないクライアントを使う。
func TestWebInEnglish(t *testing.T) {
	e := newEnv(t)
	e.user("alice", "alice-password-1", "member")
	c := enClient()
	e.enroll(c, "alice", "alice-password-1")

	res, page := e.get(c, "/im/account?done=password")
	if res.StatusCode != 200 {
		t.Fatalf("アカウント設定: %d", res.StatusCode)
	}
	if !strings.Contains(page, "Your password has been changed") {
		t.Errorf("英語の知らせが出ていない:\n%s", page)
	}
	if jaRe.MatchString(noticeLine(page)) {
		t.Errorf("英語の利用者の知らせに日本語が混ざっている: %s", noticeLine(page))
	}
}

// TestWebLangQuery は ?lang= がその要求だけ言語を変えることを確かめる
// （画面を両方の言語で見たいとき・不具合の報告に使う）。
func TestWebLangQuery(t *testing.T) {
	e := newEnv(t)
	e.user("alice", "alice-password-1", "member")
	c := enClient() // 英語の利用者
	e.enroll(c, "alice", "alice-password-1")

	res, page := e.get(c, "/im/account?done=password&lang=ja")
	if res.StatusCode != 200 {
		t.Fatalf("?lang=ja: %d → %s", res.StatusCode, res.Header.Get("Location"))
	}
	if !strings.Contains(noticeLine(page), "パスワードを変更しました") {
		t.Errorf("?lang=ja で日本語にならない: %q", noticeLine(page))
	}
	// 次の要求では元に戻る（その要求だけ効く）
	if _, page := e.get(c, "/im/account?done=password"); jaRe.MatchString(noticeLine(page)) {
		t.Errorf("?lang=ja が次の要求にも残っている: %q", noticeLine(page))
	}
}

// TestAccountNoticesTranslated は、2 言語化したアカウント設定の知らせに日本語が混ざらないことを確かめる
// （移し忘れの検知。画面の枠組みはまだ日本語なので、知らせの行だけを見る）。
func TestAccountNoticesTranslated(t *testing.T) {
	e := newEnv(t)
	e.user("alice", "alice-password-1", "member")
	c := enClient()
	e.enroll(c, "alice", "alice-password-1")

	for _, done := range []string{"password", "totp", "totp-off"} {
		_, page := e.get(c, "/im/account?done="+done)
		for _, line := range strings.Split(page, "\n") {
			if !strings.Contains(line, `class="notice"`) {
				continue
			}
			if jaRe.MatchString(line) {
				t.Errorf("done=%s: 知らせに日本語が残っている: %s", done, strings.TrimSpace(line))
			}
		}
	}
}

// twoFactorLabelRe は管理画面の「現在の設定」に出る二段階認証の表示名（store.TwoFactorLabel）。
var twoFactorLabelRe = regexp.MustCompile(`data-two-factor="[a-z]*">([^<]*)<`)

// TestTwoFactorLabelInEnglish は、二段階認証の設定の表示名が英語の利用者には英語で出ることを確かめる。
// store.TwoFactorLabel が日本語のリテラルを返していたので、英語の画面にも「必須 / 任意 / 未設定」が出ていた。
// 変更の記録の行（From → To）で「未設定」と「必須」の両方を見る。
func TestTwoFactorLabelInEnglish(t *testing.T) {
	e := newEnv(t)
	e.setTwoFactor(store.TwoFactorRequired) // 未設定 → 必須 の記録が 1 件できる
	e.user("alice", "alice-password-1", "admin")
	c := enClient()
	e.enroll(c, "alice", "alice-password-1")

	res, page := e.get(c, "/im/admin/security")
	if res.StatusCode != 200 {
		t.Fatalf("管理画面: %d", res.StatusCode)
	}
	m := twoFactorLabelRe.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("現在の設定の表示名が見つからない:\n%s", page)
	}
	if m[1] != "Required" {
		t.Errorf("英語の利用者の現在の設定 = %q, want \"Required\"", m[1])
	}
	if !strings.Contains(page, "<td>Not set → Required</td>") {
		t.Errorf("英語の利用者の変更の記録に日本語の表示名が残っている:\n%s", page)
	}
	// 日本語の利用者の文面は変えない（?lang=ja はその要求だけ日本語にする）
	if _, ja := e.get(c, "/im/admin/security?lang=ja"); !strings.Contains(ja, "<td>未設定 → 必須</td>") {
		t.Errorf("日本語の利用者の変更の記録が日本語でない:\n%s", ja)
	}
}

// ---- HTTP のエラー（段階 6）

// apiIn は lang の利用者として API を呼び、エラーの JSON を返す（?lang= ではなく Accept-Language で決める）。
func (e *env) apiIn(lang, token, method, path string, body io.Reader) (int, apiErr) {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+"/im/api/v1"+path, body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if lang != "" {
		req.Header.Set("Accept-Language", lang)
	}
	res, err := (&http.Client{}).Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	var out apiErr
	if err := json.Unmarshal(b, &out); err != nil {
		e.t.Fatalf("%s %s: %v: %s", method, path, err, b)
	}
	return res.StatusCode, out
}

// TestAPIErrorsInBothLanguages は、HTTP のエラーが要求の言語で返ること、そして
// **文面の ID がそのまま出ていないこと**（i18n.Error の Error() は ID を返すので、Text を通し忘れると漏れる）を確かめる。
func TestAPIErrorsInBothLanguages(t *testing.T) {
	e, _, ed := newAPIEnv(t)
	ed.json(201, "POST", "/projects/req/issues", map[string]any{"title": "文面の検査"}, nil)
	// ここで見るのは internal/server が組み立てる文面。internal/service が返す文面（状態の規則・
	// イシューの取得など）は別の段階で 2 言語にするので、まだ日本語で返る。
	for _, c := range []struct {
		name           string
		method, path   string
		body           string
		status         int
		code           string
		wantJA, wantEN string
	}{
		{"知らないプロジェクト", "GET", "/projects/nope/issues", "", http.StatusNotFound, "not_found",
			"プロジェクトが見つかりません: nope", "Project not found: nope"},
		{"読めない JSON", "POST", "/projects/req/issues", "{", http.StatusBadRequest, "invalid_json",
			"リクエストの JSON を解釈できません", "Cannot parse the request JSON"},
		{"コメント本文なし", "POST", "/issues/REQ-0001/comments", "{}", http.StatusBadRequest, "invalid_argument",
			"text（コメント本文）を指定してください", "Specify text (the comment body)"},
		{"経路が無い", "GET", "/nothing-here", "", http.StatusNotFound, "unknown_api",
			"API が見つかりません", "No such API endpoint"},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, l := range []struct{ header, want string }{{"ja", c.wantJA}, {"", c.wantEN}, {"en-US,en;q=0.9", c.wantEN}} {
				status, got := e.apiIn(l.header, ed.token, c.method, c.path, strings.NewReader(c.body))
				if status != c.status || got.Error.Code != c.code {
					t.Fatalf("Accept-Language %q: %d %q, want %d %q", l.header, status, got.Error.Code, c.status, c.code)
				}
				if !strings.Contains(got.Error.Message, l.want) {
					t.Errorf("Accept-Language %q: %q に %q が無い", l.header, got.Error.Message, l.want)
				}
				// ID がそのまま出ていない（i18n.Text を通し忘れると "server.api.err.xxx" が利用者に出る）
				if strings.Contains(got.Error.Message, "server.") {
					t.Errorf("Accept-Language %q: 文面に ID が出ている: %q", l.header, got.Error.Message)
				}
			}
		})
	}
}

// TestAPIErrorsHaveNoIDLeak は、代表的なエラーの経路で文面の ID が漏れないことをまとめて確かめる
// （新しいエラーを足したときの取りこぼしを拾う）。
func TestAPIErrorsHaveNoIDLeak(t *testing.T) {
	e, pr, ed := newAPIEnv(t)
	viewer := e.user("v", "v-password-1234", "member")
	if err := store.SetMember(context.Background(), e.db, pr.ID, viewer.ID, "viewer"); err != nil {
		t.Fatal(err)
	}
	v := e.apiAs(viewer)
	for _, c := range []struct {
		name         string
		token        string
		method, path string
		body         string
		status       int
	}{
		{"閲覧のみの起票", v.token, "POST", "/projects/req/issues", `{"title":"x"}`, http.StatusForbidden},
		{"閲覧のみの着手", v.token, "POST", "/projects/req/next", `{}`, http.StatusForbidden},
		{"期間なしのレポート", ed.token, "GET", "/projects/req/usage/report", "", http.StatusBadRequest},
		{"台帳の名前なし", ed.token, "POST", "/projects/req/usage/ledger", `{"to":"2026-09-01"}`, http.StatusBadRequest},
		{"依頼の期間なし", ed.token, "POST", "/projects/req/usage/requests", `{}`, http.StatusBadRequest},
		{"days の範囲外", ed.token, "GET", "/projects/req/usage/coverage?days=999", "", http.StatusBadRequest},
		{"書き出す ID なし", ed.token, "POST", "/projects/req/issues.xlsx", "", http.StatusBadRequest},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, lang := range []string{"ja", ""} {
				status, got := e.apiIn(lang, c.token, c.method, c.path, strings.NewReader(c.body))
				if status != c.status {
					t.Fatalf("Accept-Language %q: %d, want %d（%s）", lang, status, c.status, got.Error.Message)
				}
				if got.Error.Message == "" || strings.Contains(got.Error.Message, "server.") {
					t.Errorf("Accept-Language %q: 文面が空か ID のまま: %q", lang, got.Error.Message)
				}
				if lang == "" && jaRe.MatchString(got.Error.Message) {
					t.Errorf("英語の利用者に日本語が出ている: %q", got.Error.Message)
				}
			}
		})
	}
}
