package desktop

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/store"
)

// 画面の帯の「更新する」（localserve → server.Config.UpdateApplier → App.StartUpdate）。トレイを出さない起動（fakeUI）で、
// 画面から POST すると、トレイの ApplyUpdate と同じ置き換え（取得・照合・置き換え・起動し直し）に進む。

var bannerCSRF = regexp.MustCompile(`action="/looptrack/update/apply"><input type="hidden" name="csrf" value="([^"]+)"`)

// bannerClient は画面をたどるクライアント（Cookie を持ち、リダイレクトはたどらない。日本語の画面）。
// 最初の管理者を作る（ローカルモードは最初の管理者として通す。無ければ初回設定の画面に回される）。
func bannerClient(t *testing.T, app *App) *http.Client {
	t.Helper()
	if _, err := store.CreateUser(context.Background(), app.inst.DB, "boss", "boss", "", "admin"); err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func bannerGet(t *testing.T, c *http.Client, u string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("Accept-Language", "ja")
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d %s", u, res.StatusCode, b)
	}
	return string(b)
}

func bannerPost(t *testing.T, c *http.Client, app *App, csrf string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, app.URL()+"update/apply", strings.NewReader(url.Values{"csrf": {csrf}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", app.URL()+"account")
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res
}

func TestBannerApplyUpdateAppImage(t *testing.T) {
	app, done, cur, newBody, starts, rec := startLinuxApp(t, false)
	waitFor(t, "新しい版の知らせ", func() bool { return app.UpdateNotice() != nil && app.UpdateReplaceable() })
	c := bannerClient(t, app)
	page := bannerGet(t, c, app.URL()+"account")
	m := bannerCSRF.FindStringSubmatch(page)
	if m == nil || !strings.Contains(page, "新しい版 v1.1.0 があります") {
		app.Quit()
		waitDone(t, done, "対照")
		t.Fatalf("帯に「更新する」が無い: %s", page)
	}
	// CSRF の違う POST は置き換えを始めない（403）
	if res := bannerPost(t, c, app, "wrong"); res.StatusCode != http.StatusForbidden {
		t.Errorf("CSRF の違う POST: %d（403 のはず）", res.StatusCode)
	}
	if got := readFile(t, cur); got != "\x7fELF v1.0.0" || len(*starts) != 0 {
		app.Quit()
		waitDone(t, done, "CSRF")
		t.Fatalf("CSRF の違う POST で置き換えた: %q %v", got, *starts)
	}
	res := bannerPost(t, c, app, m[1])
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/looptrack/account" {
		t.Errorf("POST: %d → %q", res.StatusCode, res.Header.Get("Location"))
	}
	waitDone(t, done, "帯から置き換えた後")
	if got := readFile(t, cur); got != string(newBody) {
		t.Errorf("置き換えた後の中身 = %q", got)
	}
	if got := readFile(t, cur+".prev"); got != "\x7fELF v1.0.0" {
		t.Errorf(".prev = %q", got)
	}
	if len(*starts) != 1 || strings.Join((*starts)[0], " ") != cur+" desktop --after-update" {
		t.Errorf("起動し直し = %v", *starts)
	}
	if len(rec.alerts) != 0 {
		t.Errorf("成功したのに知らせを出した: %v", rec.alerts)
	}
}

// 置き換えに失敗したら（AppImage の置き場に書き込めない）今の版のまま動き続け、帯に失敗の版と理由を出して、ボタンを残す。
func TestBannerApplyUpdateFailure(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("書き込めない置き場を作れない（Windows の権限・root）")
	}
	app, done, cur, _, starts, _ := startLinuxApp(t, false)
	defer func() {
		app.Quit()
		waitDone(t, done, "失敗の後")
	}()
	waitFor(t, "新しい版の知らせ", func() bool { return app.UpdateNotice() != nil && app.UpdateReplaceable() })
	dir := filepath.Dir(cur)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o755)
	c := bannerClient(t, app)
	m := bannerCSRF.FindStringSubmatch(bannerGet(t, c, app.URL()+"account"))
	if m == nil {
		t.Fatal("帯に「更新する」が無い")
	}
	if res := bannerPost(t, c, app, m[1]); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST: %d", res.StatusCode)
	}
	waitFor(t, "置き換えの失敗", func() bool {
		running, v, _ := app.UpdateApplyState()
		return !running && v != ""
	})
	_, v, reason := app.UpdateApplyState()
	if v != "v1.1.0" || reason == "" {
		t.Errorf("失敗の記録 = %q %q", v, reason)
	}
	if got := readFile(t, cur); got != "\x7fELF v1.0.0" || len(*starts) != 0 {
		t.Errorf("失敗したのに置き換えた: %q %v", got, *starts)
	}
	page := bannerGet(t, c, app.URL()+"account")
	if !strings.Contains(page, "新しい版 v1.1.0 に置き換えられませんでした。今の版のまま動いています") || !bannerCSRF.MatchString(page) {
		t.Errorf("帯に失敗の文面かボタンが無い: %s", page)
	}
}
