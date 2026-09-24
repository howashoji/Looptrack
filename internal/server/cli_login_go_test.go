package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/privfile"
	"github.com/howashoji/looptrack/internal/store"
)

// Go 版（looptrack issue login）を本物のサーバ（httptest）に対して通しで動かす。
//
//   - login --browser → config → アクセストークンの期限切れ（401）での取り直し → 失効の後の案内
//   - 資格情報は looptrack の置き場だけに書く（以前の CLI の置き場は使わなくなった）
//
// ブラウザの代わりは BROWSER の「開いた URL をファイルに書くだけ」のスクリプトで、テストがログイン済みのブラウザとして
// 承認画面を通してコールバックへ戻す。子プロセスは IM_*・LOOPTRACK_* を除き、
// HOME・XDG_CONFIG_HOME・APPDATA・USERPROFILE を一時ディレクトリにして起動する（本番に触れない）。

type loginRig struct {
	t       *testing.T
	e       *env
	browser *http.Client
	base    string
	env     []string
	opened  string
	primary string // ~/.config/looptrack/credentials.json（looptrack）
	gobin   []string
}

func newLoginRig(t *testing.T) *loginRig {
	t.Helper()
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("req")
	u := e.user("alice", "alice-password-123", "member")
	store.SetMember(ctx, e.db, pr.ID, u.ID, "editor")
	browser := e.client()
	e.enroll(browser, "alice", "alice-password-123")

	tmp := t.TempDir()
	home, cfg, root := filepath.Join(tmp, "home"), filepath.Join(tmp, "xdg"), filepath.Join(tmp, "proj")
	os.MkdirAll(home, 0o755)
	os.MkdirAll(root, 0o755)
	r := &loginRig{t: t, e: e, browser: browser, base: e.srv.URL + "/im", opened: filepath.Join(tmp, "opened.txt"),
		primary: filepath.Join(cfg, "looptrack", "credentials.json")}
	if runtime.GOOS == "windows" { // looptrack の置き場は %APPDATA%\looptrack（下の APPDATA）
		r.primary = filepath.Join(home, "AppData", "looptrack", "credentials.json")
	}
	fake := fakeBrowser(t, tmp, r.opened)
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "IM_") || strings.HasPrefix(k, "LOOPTRACK_") || strings.HasPrefix(k, "CLAUDE") ||
			strings.HasPrefix(k, "CODEX_") || strings.HasPrefix(k, "COPILOT_") {
			continue
		}
		switch k {
		case "HOME", "XDG_CONFIG_HOME", "APPDATA", "USERPROFILE", "BROWSER":
			continue
		}
		r.env = append(r.env, kv)
	}
	// LOOPTRACK_LANG: 下の検査は日本語の文面を見る（指定が無ければ英語が出る）
	r.env = append(r.env, "HOME="+home, "XDG_CONFIG_HOME="+cfg, "APPDATA="+filepath.Join(home, "AppData"), "USERPROFILE="+home,
		"CLAUDE_PROJECT_DIR="+root, "BROWSER="+fake, "LOOPTRACK_LANG=ja")
	r.gobin = []string{looptrackBin(t), "issue"}
	return r
}

// fakeBrowser は BROWSER に入れるブラウザの代わり（開いた URL を opened に書くだけ）。POSIX はシェルスクリプト、
// Windows はシェルスクリプトを直接起動できない（cmd.exe の .cmd は URL の & を解釈する）ので、小さな Go のプログラムを作る。
func fakeBrowser(t *testing.T, dir, opened string) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		fake := filepath.Join(dir, "fake-browser")
		os.WriteFile(fake, []byte("#!/bin/sh\nprintf '%s' \"$1\" > '"+opened+"'\n"), 0o755)
		return fake
	}
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go が無いためブラウザの代わりを作れない")
	}
	src := filepath.Join(dir, "fakebrowser.go")
	prog := "package main\n\nimport \"os\"\n\nfunc main() {\n\tif err := os.WriteFile(" + strconv.Quote(opened) +
		", []byte(os.Args[1]), 0o644); err != nil {\n\t\tos.Exit(1)\n\t}\n}\n"
	if err := os.WriteFile(src, []byte(prog), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(dir, "fake-browser.exe")
	cmd := exec.Command(gobin, "build", "-o", fake, src)
	cmd.Dir = dir // モジュールの外（1 ファイルの main をそのまま作る）
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ブラウザの代わりを作れません: %v\n%s", err, b)
	}
	return fake
}

// privateFile は資格情報のファイルが本人だけの形か（POSIX は 0600、Windows は本人だけの ACL。internal/privfile）。
func privateFile(path string) error {
	if runtime.GOOS == "windows" {
		return privfile.Check(path)
	}
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if st.Mode().Perm() != 0o600 {
		return fmt.Errorf("権限 %v", st.Mode())
	}
	return nil
}

// run は CLI を 1 回動かす（サーバの URL とプロジェクトを付けて）。
func (r *loginRig) run(cli []string, args ...string) (int, string) {
	r.t.Helper()
	cmd := exec.Command(cli[0], append(append([]string{}, cli[1:]...), args...)...)
	cmd.Env = append(append([]string{}, r.env...), "LOOPTRACK_API_URL="+r.base, "LOOPTRACK_PROJECT=req")
	out, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), string(out)
	} else if err != nil {
		r.t.Fatal(err)
	}
	return 0, string(out)
}

// login は login --browser を動かし、ログイン済みのブラウザとして承認する。
func (r *loginRig) login(cli []string) (int, string) {
	t := r.t
	t.Helper()
	os.Remove(r.opened)
	cmd := exec.Command(cli[0], append(append([]string{}, cli[1:]...), "login", "--browser", "--url", r.base)...)
	cmd.Env = r.env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	fail := func(format string, a ...any) {
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatalf(format+"\n%s", append(a, out.String())...)
	}
	var authURL string
	for i := 0; i < 300 && authURL == ""; i++ {
		if b, err := os.ReadFile(r.opened); err == nil && len(b) > 0 {
			authURL = string(b)
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.HasPrefix(authURL, r.base+"/oauth/authorize?") {
		fail("認可の URL を開かない: %q", authURL)
	}
	res, page := r.e.get(r.browser, strings.TrimPrefix(authURL, r.e.srv.URL))
	// 承認画面にクライアント名（looptrack（host））が出る
	if res.StatusCode != 200 || !strings.Contains(page, "looptrack（") {
		fail("承認画面: %d %s", res.StatusCode, page)
	}
	q, _ := url.Parse(authURL)
	res, _ = r.e.postForm(r.browser, "/im/oauth/authorize", url.Values{"csrf": {csrfOf(t, page)}, "query": {q.RawQuery}, "approve": {"1"}})
	loc := res.Header.Get("Location")
	if !strings.HasPrefix(loc, "http://127.0.0.1:") {
		fail("承認後の戻り先: %d %s", res.StatusCode, loc)
	}
	cb, err := http.Get(loc)
	if err != nil {
		fail("コールバック: %v", err)
	}
	body, _ := io.ReadAll(cb.Body)
	cb.Body.Close()
	if cb.StatusCode != 200 || !strings.Contains(string(body), "閉じて") {
		t.Errorf("コールバックの応答: %d %s", cb.StatusCode, body)
	}
	err = cmd.Wait()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	}
	return code, out.String()
}

// entry は資格情報のファイルの、このサーバの項目（無ければ nil）。
func (r *loginRig) entry(path string) map[string]any {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	all := map[string]map[string]any{}
	json.Unmarshal(b, &all)
	return all[r.base]
}

// expire はサーバの時計をアクセストークンの期限の後へ進める（CLI の保存した期限は実時間なので、次の要求は 401 になる）。
func (r *loginRig) expire() { r.e.clock.Add(oauthTokenLife + time.Hour) }

// revokedRefresh は失効した更新トークンの数（再利用の検知が起きると系列ごと失効する）。
func (r *loginRig) revokedRefresh() int {
	var n int
	r.e.db.QueryRow("SELECT COUNT(*) FROM oauth_refresh_tokens WHERE revoked_at IS NOT NULL").Scan(&n)
	return n
}

func TestGoCLILoginBrowser(t *testing.T) {
	r := newLoginRig(t)
	code, out := r.login(r.gobin)
	if code != 0 || !strings.Contains(out, "ログイン: alice") || !strings.Contains(out, r.primary) {
		t.Fatalf("login --browser: %d %s", code, out)
	}
	if strings.Contains(out, "imo_") || strings.Contains(out, "imr_") || strings.Contains(out, "code=") {
		t.Errorf("出力にトークン・認可コードが出た: %s", out)
	}
	if err := privateFile(r.primary); err != nil {
		t.Errorf("credentials.json の権限: %v", err)
	}
	c1 := r.entry(r.primary)
	if !strings.HasPrefix(c1["token"].(string), "imo_") || !strings.HasPrefix(c1["refresh_token"].(string), "imr_") || c1["login"] != "alice" || c1["client_id"] == "" {
		t.Errorf("保存内容: %v", c1)
	}
	var name string
	r.e.db.QueryRow("SELECT client_name FROM oauth_clients WHERE client_id = ?", c1["client_id"]).Scan(&name)
	if host, _ := os.Hostname(); name != "looptrack（"+host+"）" { // トークンの名前は looptrack（ホスト名）
		t.Errorf("クライアント名: %q", name)
	}
	if code, out := r.run(r.gobin, "config"); code != 0 || !strings.Contains(out, "利用者: alice") || !strings.Contains(out, "権限 editor") {
		t.Errorf("config: %d %s", code, out)
	}

	// 2 回目: 控えた client_id を再利用し、別のポートでも通る
	if code, out := r.login(r.gobin); code != 0 || r.entry(r.primary)["client_id"] != c1["client_id"] {
		t.Errorf("2 回目の login: %d %s", code, out)
	}
	var clients int
	r.e.db.QueryRow("SELECT COUNT(*) FROM oauth_clients").Scan(&clients)
	if clients != 1 {
		t.Errorf("クライアントを登録し直した: %d", clients)
	}

	// 期限切れ（401）→ 更新トークンで取り直してやり直す
	before := r.entry(r.primary)
	r.expire()
	if code, out := r.run(r.gobin, "list"); code != 0 {
		t.Fatalf("期限切れの後の list: %d %s", code, out)
	}
	after := r.entry(r.primary)
	if after["token"] == before["token"] || after["refresh_token"] == before["refresh_token"] {
		t.Errorf("取り直した組を保存していない: %v", after)
	}
	if err := privateFile(r.primary); err != nil {
		t.Errorf("更新後の権限: %v", err)
	}
	if n := r.revokedRefresh(); n != 0 {
		t.Errorf("更新トークンが失効した: %d", n)
	}

	// アカウント画面でアクセストークンを失効 → 更新トークンも使えない → login --browser を案内
	var id, uid int64
	r.e.db.QueryRow("SELECT id, user_id FROM api_tokens WHERE token_prefix = ?", after["token"].(string)[:12]).Scan(&id, &uid)
	if err := store.RevokeUserToken(context.Background(), r.e.db, uid, id); err != nil {
		t.Fatal(err)
	}
	if code, out := r.run(r.gobin, "list"); code != 1 || !strings.Contains(out, "ログインの有効期限が切れました") || !strings.Contains(out, "login --browser") {
		t.Errorf("失効の後: %d %s", code, out)
	}
}
