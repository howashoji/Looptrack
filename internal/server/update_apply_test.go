package server

import (
	"bytes"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/updatecheck"
)

// 帯の「更新する」（デスクトップ版が Config.UpdateApplier を渡したときだけ。POST {base}/update/apply）。

// fakeApplier は置き換えの代わり（呼ばれた回数と、帯に出す状態を差し替えられる）。
type fakeApplier struct {
	mu                          sync.Mutex
	replaceable, running        bool
	failedVersion, failedReason string
	starts                      int
}

func (f *fakeApplier) UpdateReplaceable() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.replaceable
}

func (f *fakeApplier) StartUpdate() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.replaceable || f.running {
		return false
	}
	f.starts++
	f.running = true
	return true
}

func (f *fakeApplier) UpdateApplyState() (bool, string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running, f.failedVersion, f.failedReason
}

func (f *fakeApplier) set(fn func(*fakeApplier)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeApplier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts
}

const updateApplyForm = `<form class="appbar-update-form" method="post" action="/im/update/apply"><input type="hidden" name="csrf" value="`

func TestUpdateApplyButton(t *testing.T) {
	src := &noticeSource{}
	src.set(&updatecheck.Notice{Version: "v1.0.0-rc.3", Current: "v1.0.0-rc.2", URL: "https://github.com/howashoji/looptrack/releases/tag/v1.0.0-rc.3"})
	ap := &fakeApplier{replaceable: true}
	e := newEnvWith(t, func(cfg *Config) { cfg.LocalMode = true; cfg.UpdateNotice = src.get; cfg.UpdateApplier = ap })
	e.user("boss", "boss-password-12", "admin")
	e.project("demo")
	c := e.client()
	page := func(p string, header ...string) string {
		t.Helper()
		res, body := e.get(c, p, header...)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: %d %s", p, res.StatusCode, body)
		}
		return body
	}

	// 置き換えられるなら、帯（プロジェクトの一覧・ボード・アカウント設定）にボタンが出る。リリースのページへのリンクも残る
	for _, p := range []string{"/im/", "/im/p/demo/", "/im/account"} {
		body := page(p)
		for _, want := range []string{updateApplyForm, `<button type="submit" class="small">更新する</button>`, "リリースのページを開く"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s の帯に %q が無い", p, want)
			}
		}
		if strings.Count(body, `action="/im/update/apply"`) != 1 {
			t.Errorf("%s のボタンが 1 つでない", p)
		}
	}
	if en := page("/im/account", "Accept-Language", "en"); !strings.Contains(en, `<button type="submit" class="small">Update now</button>`) {
		t.Error("英語の帯にボタンが無い")
	}

	// CSRF の無い・違う POST は 403 で、置き換えを始めない
	for _, form := range []url.Values{{}, {"csrf": {"wrong"}}} {
		if res, _ := e.post(c, "/im/update/apply", form); res.StatusCode != http.StatusForbidden {
			t.Errorf("CSRF %v: %d（403 のはず）", form, res.StatusCode)
		}
	}
	if n := ap.count(); n != 0 {
		t.Fatalf("CSRF の通らない POST で置き換えを始めた（%d 回）", n)
	}

	// 画面のフォームから送ると 1 回だけ始め、元の画面（Referer のパス）へ 303 で戻す
	csrf := csrfOf(t, page("/im/p/demo/"))
	res, _ := e.do(c, http.MethodPost, "/im/update/apply", url.Values{"csrf": {csrf}}.Encode(),
		"Content-Type", "application/x-www-form-urlencoded", "Referer", e.srv.URL+"/im/p/demo/?status=open")
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/im/p/demo/?status=open" {
		t.Fatalf("POST: %d → %q", res.StatusCode, res.Header.Get("Location"))
	}
	if n := ap.count(); n != 1 {
		t.Fatalf("置き換えを始めた回数 = %d（1 のはず）", n)
	}
	// 別の出どころ・Referer 無しは {base}/ へ戻す（開いたままのリダイレクトにしない）
	for _, ref := range []string{"https://evil.example/im/p/demo/", ""} {
		ap.set(func(f *fakeApplier) { f.running = false })
		res, _ := e.do(c, http.MethodPost, "/im/update/apply", url.Values{"csrf": {csrf}}.Encode(),
			"Content-Type", "application/x-www-form-urlencoded", "Referer", ref)
		if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/im/" {
			t.Errorf("Referer %q: %d → %q", ref, res.StatusCode, res.Header.Get("Location"))
		}
	}
	ap.set(func(f *fakeApplier) { f.running = true })

	// 進行中はボタンの代わりに進行中の文面を出す
	body := page("/im/account")
	if !strings.Contains(body, "新しい版 v1.0.0-rc.3 に置き換えています。終わるとアプリが起動し直します。") || strings.Contains(body, "/im/update/apply") {
		t.Error("進行中（ja）: 文面が無いか、ボタンを出した")
	}
	if en := page("/im/account", "Accept-Language", "en"); !strings.Contains(en, "Replacing the app with the new version v1.0.0-rc.3.") {
		t.Error("進行中（en）の文面が無い")
	}

	// 失敗したら（この版のもの）失敗の文面とやり直しのボタンを出す
	ap.set(func(f *fakeApplier) {
		f.running, f.failedVersion, f.failedReason = false, "v1.0.0-rc.3", "download: HTTP 404"
	})
	body = page("/im/account")
	if !strings.Contains(body, "新しい版 v1.0.0-rc.3 に置き換えられませんでした。今の版のまま動いています: download: HTTP 404") ||
		!strings.Contains(body, updateApplyForm) {
		t.Error("失敗（ja）: 文面かボタンが無い")
	}
	if en := page("/im/account", "Accept-Language", "en"); !strings.Contains(en, "Could not replace the app with the new version v1.0.0-rc.3. The current version keeps running: download: HTTP 404") {
		t.Error("失敗（en）の文面が無い")
	}
	// 前の版の失敗は、新しい知らせには出さない
	ap.set(func(f *fakeApplier) { f.failedVersion = "v1.0.0-rc.1" })
	if body := page("/im/account"); strings.Contains(body, "置き換えられませんでした") {
		t.Error("別の版の失敗を出した")
	}

	// 置き換えられない（署名の無い結果・置き換えられない環境）ならボタンを出さず、POST しても始めない
	ap.set(func(f *fakeApplier) { f.replaceable = false })
	if body := page("/im/account"); strings.Contains(body, "/im/update/apply") || !strings.Contains(body, "リリースのページを開く") {
		t.Error("置き換えられないのにボタンを出したか、リンクが消えた")
	}
	before := ap.count()
	e.post(c, "/im/update/apply", url.Values{"csrf": {csrf}})
	if n := ap.count(); n != before {
		t.Errorf("置き換えられないのに始めた（%d → %d 回）", before, n)
	}
}

// CSRF の無い画面（データに CSRF を詰めない画面）には、ボタンを出さない（ログアウトと同じ）。
func TestUpdateApplyButtonNeedsCSRF(t *testing.T) {
	src := &noticeSource{}
	src.set(&updatecheck.Notice{Version: "v1.0.0-rc.3", Current: "v1.0.0-rc.2"})
	e := newEnvWith(t, func(cfg *Config) {
		cfg.LocalMode = true
		cfg.UpdateNotice = src.get
		cfg.UpdateApplier = &fakeApplier{replaceable: true}
	})
	render := func(data map[string]any) string {
		var b bytes.Buffer
		if err := e.s.tmpl.ExecuteTemplate(&b, "update_notice", data); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	if got := render(map[string]any{"Lang": i18n.Lang("ja"), "CSRF": "tok"}); !strings.Contains(got, `name="csrf" value="tok"`) {
		t.Fatalf("対照: CSRF のある画面にボタンが無い: %s", got)
	}
	if got := render(map[string]any{"Lang": i18n.Lang("ja")}); !strings.Contains(got, "appbar-update") || strings.Contains(got, "update/apply") {
		t.Errorf("CSRF の無い画面: 帯が無いか、ボタンを出した: %s", got)
	}
}

// UpdateApplier を渡さない（looptrack serve・チームのサーバ）ならボタンは出ず、POST は 404。サーバ版の帯（UpdateServer）でも出さない。
func TestUpdateApplyNotOutsideDesktop(t *testing.T) {
	src := &noticeSource{}
	src.set(&updatecheck.Notice{Version: "v1.0.0-rc.3", Current: "v1.0.0-rc.2"})

	e := newEnvWith(t, func(cfg *Config) { cfg.LocalMode = true; cfg.UpdateNotice = src.get })
	e.user("boss", "boss-password-12", "admin")
	c := e.client()
	_, body := e.get(c, "/im/account")
	if !strings.Contains(body, "appbar-update") || strings.Contains(body, "update/apply") {
		t.Error("UpdateApplier なし: 帯が無いか、ボタンを出した")
	}
	if res, _ := e.post(c, "/im/update/apply", url.Values{"csrf": {csrfOf(t, body)}}); res.StatusCode != http.StatusNotFound {
		t.Errorf("UpdateApplier なしの POST: %d（404 のはず）", res.StatusCode)
	}

	ap := &fakeApplier{replaceable: true}
	s := newEnvWith(t, func(cfg *Config) {
		cfg.AllowNoAdmin = true
		cfg.UpdateNotice, cfg.UpdateServer, cfg.UpdateApplier = src.get, true, ap
	})
	s.user("root", "root-password-12", "admin")
	sc := s.client()
	s.enroll(sc, "root", "root-password-12")
	_, body = s.get(sc, "/im/account")
	if !strings.Contains(body, "install.sh") || strings.Contains(body, "update/apply") {
		t.Error("サーバ版の帯: 更新の 1 行が無いか、ボタンを出した")
	}
	if res, _ := s.post(sc, "/im/update/apply", url.Values{"csrf": {csrfOf(t, body)}}); res.StatusCode != http.StatusNotFound || ap.count() != 0 {
		t.Errorf("サーバ版の POST: %d・始めた回数 %d（404・0 のはず）", res.StatusCode, ap.count())
	}
}
