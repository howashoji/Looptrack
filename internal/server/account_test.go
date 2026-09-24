package server

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/auth"
	"github.com/howashoji/looptrack/internal/store"
)

// アカウント設定（パスワード変更・トークン発行と失効）と利用者管理。

var patRe = regexp.MustCompile(`imp_[0-9a-f]{60}`)

// relogin は TOTP 登録済みの利用者で別のセッションを作る（同じコードは再利用できないので時計を進める）。
func (e *env) relogin(c *http.Client, login, password string, secret []byte) {
	e.t.Helper()
	e.clock.Add(30 * time.Second)
	res, _ := e.login(c, login, password)
	if res.Header.Get("Location") != "/im/login/totp" {
		e.t.Fatalf("パスワード後の遷移先 = %q", res.Header.Get("Location"))
	}
	_, page := e.get(c, "/im/login/totp")
	res, _ = e.post(c, "/im/login/totp", url.Values{"csrf": {csrfOf(e.t, page)}, "code": {e.code(secret)}})
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/im/" {
		e.t.Fatalf("TOTP 後: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
}

// formAt は画面を開いて CSRF トークンを取り、フォームを送る。
func (e *env) formAt(c *http.Client, page, action string, form url.Values) (*http.Response, string) {
	e.t.Helper()
	_, html := e.get(c, page)
	form.Set("csrf", csrfOf(e.t, html))
	return e.post(c, action, form)
}

func (e *env) meStatus(token string) int {
	e.t.Helper()
	res, _ := e.get(e.client(), "/im/api/v1/me", "Authorization", "Bearer "+token)
	return res.StatusCode
}

func (e *env) countTokens(userID int64) int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRow("SELECT COUNT(*) FROM api_tokens WHERE user_id = ?", userID).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestAccountTokens(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("req")
	alice := e.user("alice", "alice-password-1", "member")
	bob := e.user("bob", "bob-password-123", "member")
	if err := store.SetMember(ctx, e.db, pr.ID, alice.ID, "viewer"); err != nil {
		t.Fatal(err)
	}
	e.apiAs(e.adminIn("root", "root-password-12", "req")).json(201, "POST", "/projects/req/issues", map[string]any{"title": "一覧に出るイシュー"}, nil)

	// 未ログインはログイン画面へ（POST も含め何も起きない）
	anon := e.client()
	if res, _ := e.get(anon, "/im/account"); res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(res.Header.Get("Location"), "/im/login") {
		t.Errorf("未ログインの GET: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if res, _ := e.post(anon, "/im/account/tokens", url.Values{"name": {"x"}, "days": {"90"}}); res.StatusCode != http.StatusSeeOther {
		t.Errorf("未ログインの POST: %d", res.StatusCode)
	}

	c := e.client()
	e.enroll(c, "alice", "alice-password-1")
	res, page := e.get(c, "/im/account")
	if res.StatusCode != 200 || !strings.Contains(page, "アクセストークン") || strings.Contains(page, "<script") {
		t.Fatalf("アカウント設定: %d %s", res.StatusCode, page)
	}

	// CSRF が無い・違うと 403 で何も作らない
	if res, _ := e.post(c, "/im/account/tokens", url.Values{"name": {"x"}, "days": {"90"}}); res.StatusCode != http.StatusForbidden {
		t.Errorf("CSRF なし: %d", res.StatusCode)
	}
	if res, _ := e.post(c, "/im/account/tokens", url.Values{"csrf": {"wrong"}, "name": {"x"}, "days": {"90"}}); res.StatusCode != http.StatusForbidden {
		t.Errorf("CSRF 違い: %d", res.StatusCode)
	}
	// 入力誤り
	for _, f := range []url.Values{{"name": {""}, "days": {"90"}}, {"name": {"x"}, "days": {"7"}}, {"name": {"x"}, "days": {"0"}}, {"name": {strings.Repeat("あ", 256)}, "days": {"90"}}} {
		if res, _ := e.formAt(c, "/im/account", "/im/account/tokens", f); res.StatusCode != http.StatusBadRequest {
			t.Errorf("入力誤り %v: %d", f, res.StatusCode)
		}
	}
	if n := e.countTokens(alice.ID); n != 0 {
		t.Fatalf("拒否したのにトークンが %d 件ある", n)
	}

	// 発行: 平文は応答に 1 回だけ。DB はハッシュと先頭 12 文字
	res, body := e.formAt(c, "/im/account", "/im/account/tokens", url.Values{"name": {"MacBook の Claude Code"}, "days": {"90"}})
	toks := patRe.FindAllString(body, -1)
	if res.StatusCode != 200 || len(toks) != 1 || !strings.Contains(body, "looptrack issue login --url") {
		t.Fatalf("発行: %d tokens=%v %s", res.StatusCode, toks, body)
	}
	tok := toks[0]
	var prefix, name string
	var hash []byte
	var exp sql.NullTime
	if err := e.db.QueryRow("SELECT token_prefix, token_hash, name, expires_at FROM api_tokens WHERE user_id = ?", alice.ID).Scan(&prefix, &hash, &name, &exp); err != nil {
		t.Fatal(err)
	}
	if prefix != tok[:12] || !bytes.Equal(hash, auth.HashToken(tok)) || name != "MacBook の Claude Code" {
		t.Errorf("保存内容: prefix=%s name=%s", prefix, name)
	}
	if want := e.clock.Now().Add(90 * 24 * time.Hour); !exp.Valid || exp.Time.Sub(want).Abs() > time.Minute {
		t.Errorf("期限 = %v, want %v", exp, want)
	}
	var plain int
	e.db.QueryRow("SELECT COUNT(*) FROM api_tokens WHERE CAST(token_hash AS CHAR) LIKE ? OR name LIKE ?", "%"+tok+"%", "%"+tok+"%").Scan(&plain)
	if plain != 0 {
		t.Error("トークンの平文が DB にある")
	}
	if _, again := e.get(c, "/im/account"); strings.Contains(again, tok) || !strings.Contains(again, prefix) {
		t.Error("開き直した画面に平文が出る、または一覧に出ない")
	}
	if code := e.meStatus(tok); code != 200 {
		t.Fatalf("発行したトークンで /me: %d", code)
	}

	// 発行したトークンで looptrack issue login（標準入力に貼る）→ list
	{
		home, root := t.TempDir(), t.TempDir()
		run := func(stdin string, args ...string) (int, string) {
			t.Helper()
			env := append(cliAPIEnv(e.srv.URL+"/im", "req", "", home), "CLAUDE_PROJECT_DIR="+root)
			r := runCLI(t, root, env, stdin, append([]string{"issue"}, args...)...)
			return r.code, r.stdout + r.stderr
		}
		if code, out := run(tok+"\n", "login"); code != 0 || !strings.Contains(out, "ログイン: alice") {
			t.Fatalf("looptrack issue login: %d %s", code, out)
		}
		if code, out := run("", "list"); code != 0 || !strings.Contains(out, "REQ-0001") {
			t.Errorf("looptrack issue list: %d %s", code, out)
		}
	}

	// 他人のトークンは失効できない
	bobAPI := e.apiAs(bob)
	var bobTokenID int64
	e.db.QueryRow("SELECT id FROM api_tokens WHERE user_id = ?", bob.ID).Scan(&bobTokenID)
	if res, _ := e.formAt(c, "/im/account", "/im/account/tokens/revoke", url.Values{"token": {strconv.FormatInt(bobTokenID, 10)}}); res.StatusCode != http.StatusNotFound {
		t.Errorf("他人のトークンの失効: %d", res.StatusCode)
	}
	if code := e.meStatus(bobAPI.token); code != 200 {
		t.Errorf("他人のトークンが失効した: %d", code)
	}
	if res, _ := e.formAt(c, "/im/account", "/im/account/tokens/revoke", url.Values{"token": {"abc"}}); res.StatusCode != http.StatusNotFound {
		t.Errorf("不正な ID: %d", res.StatusCode)
	}

	// OAuth のトークンも一覧に出て失効できる。自分の PAT を失効すると 401
	otok, oprefix, _ := auth.NewOAuthToken()
	oid, err := store.CreateToken(ctx, e.db, alice.ID, "oauth", "Claude Code (im)", oprefix, auth.HashToken(otok), nil, e.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, page := e.get(c, "/im/account"); !strings.Contains(page, "OAuth") || !strings.Contains(page, oprefix) {
		t.Error("OAuth のトークンが一覧に無い")
	}
	if _, page := e.get(c, "/im/account"); strings.Contains(page, `<details class="inactive-tokens"`) || strings.Count(page, `class="danger small">失効</button>`) != 2 {
		t.Errorf("有効なトークンだけのとき折りたたみを出さず、2 件とも失効ボタンを出す: %s", page)
	}
	var tokID int64
	e.db.QueryRow("SELECT id FROM api_tokens WHERE user_id = ? AND kind = 'pat'", alice.ID).Scan(&tokID)
	for _, id := range []int64{tokID, oid} {
		if res, body := e.formAt(c, "/im/account", "/im/account/tokens/revoke", url.Values{"token": {strconv.FormatInt(id, 10)}}); res.StatusCode != 200 || !strings.Contains(body, "失効させました") {
			t.Errorf("失効 %d: %d", id, res.StatusCode)
		}
	}
	if code := e.meStatus(tok); code != http.StatusUnauthorized {
		t.Errorf("失効した PAT: %d", code)
	}
	if code := e.meStatus(otok); code != http.StatusUnauthorized {
		t.Errorf("失効した OAuth トークン: %d", code)
	}
	if res, _ := e.formAt(c, "/im/account", "/im/account/tokens/revoke", url.Values{"token": {strconv.FormatInt(tokID, 10)}}); res.StatusCode != http.StatusNotFound {
		t.Errorf("失効済みの再失効: %d", res.StatusCode)
	}

	// 失効・期限切れは既定で閉じた折りたたみに入る（行は消さない）
	past := e.clock.Now().Add(-time.Hour)
	if _, err := store.CreateToken(ctx, e.db, alice.ID, "pat", "期限切れ", "imp_expired0", auth.HashToken("imp_expired0-x"), &past, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	_, page = e.get(c, "/im/account")
	i := strings.Index(page, `<details class="inactive-tokens">`)
	if i < 0 || strings.Contains(page, "<details open") || !strings.Contains(page, "失効・期限切れのトークン（3 件）") || !strings.Contains(page, "有効なトークンはありません。") {
		t.Fatalf("失効・期限切れの折りたたみ: %s", page)
	}
	if active, inactive := page[:i], page[i:]; strings.Contains(active, prefix) || strings.Contains(active, oprefix) ||
		!strings.Contains(inactive, prefix) || !strings.Contains(inactive, oprefix) || !strings.Contains(inactive, "期限切れ") ||
		strings.Contains(page, `class="danger small">失効</button>`) {
		t.Errorf("失効・期限切れのトークンが折りたたみの外にある、または失効ボタンが出る: %s", page)
	}
	if n := e.countTokens(alice.ID); n != 3 {
		t.Errorf("トークンの行が %d 件（失効・期限切れも残す）", n)
	}
}

func TestAccountPassword(t *testing.T) {
	e := newEnv(t)
	e.user("alice", "alice-password-1", "member")
	c1, c2 := e.client(), e.client()
	secret := e.enroll(c1, "alice", "alice-password-1")
	e.relogin(c2, "alice", "alice-password-1", secret)

	change := func(current, next, confirm string) (*http.Response, string) {
		return e.formAt(c1, "/im/account", "/im/account/password", url.Values{"current": {current}, "new": {next}, "confirm": {confirm}})
	}
	if res, _ := change("wrong-password-1", "brand-new-pass-1", "brand-new-pass-1"); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("現在のパスワード違い: %d", res.StatusCode)
	}
	if res, _ := change("alice-password-1", "brand-new-pass-1", "brand-new-pass-2"); res.StatusCode != http.StatusBadRequest {
		t.Errorf("確認の不一致: %d", res.StatusCode)
	}
	if res, _ := change("alice-password-1", "short", "short"); res.StatusCode != http.StatusBadRequest {
		t.Errorf("短すぎる: %d", res.StatusCode)
	}
	// 拒否した間はどちらのセッションも生きていて、パスワードも変わっていない
	if res, _ := e.get(c2, "/im/"); res.StatusCode != 200 {
		t.Fatalf("拒否でセッションが消えた: %d", res.StatusCode)
	}
	u, _ := store.UserByLogin(context.Background(), e.db, "alice")
	if ok, _ := auth.VerifyPassword(u.PasswordHash, "alice-password-1"); !ok {
		t.Fatal("拒否したのにパスワードが変わった")
	}

	res, _ := change("alice-password-1", "brand-new-pass-1", "brand-new-pass-1")
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/im/account?done=password" {
		t.Fatalf("変更: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if res, page := e.get(c1, "/im/account?done=password"); res.StatusCode != 200 || !strings.Contains(page, "パスワードを変更しました") {
		t.Errorf("変更した画面のセッション: %d", res.StatusCode)
	}
	if res, _ := e.get(c2, "/im/"); res.StatusCode != http.StatusSeeOther {
		t.Errorf("他のセッションが残っている: %d", res.StatusCode)
	}
	if res, _ := e.login(e.client(), "alice", "alice-password-1"); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("古いパスワードでログインできる: %d", res.StatusCode)
	}
	e.relogin(e.client(), "alice", "brand-new-pass-1", secret)

	// 再認証の失敗はログインと同じ回数制限を受ける（直前のログイン成功から 5 回失敗 → 正しいパスワードでも 429）
	for i := 0; i < 5; i++ {
		if res, _ := change("wrong-password-1", "another-pass-12", "another-pass-12"); res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("失敗 %d 回目: %d", i+1, res.StatusCode)
		}
	}
	if res, _ := change("brand-new-pass-1", "another-pass-12", "another-pass-12"); res.StatusCode != http.StatusTooManyRequests {
		t.Errorf("回数制限: %d", res.StatusCode)
	}
}

func TestAdminUsers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("req")
	root := e.user("root", "root-password-12", "admin")
	e.user("alice", "alice-password-1", "member")

	// 管理者以外は 404 で何も変わらない
	ca := e.client()
	e.enroll(ca, "alice", "alice-password-1")
	for _, p := range []string{"/im/admin/users", "/im/admin/users/root"} {
		if res, body := e.get(ca, p); res.StatusCode != http.StatusNotFound || strings.Contains(body, "root-password") {
			t.Errorf("一般利用者の GET %s: %d", p, res.StatusCode)
		}
	}
	_, acct := e.get(ca, "/im/account")
	aCSRF := csrfOf(t, acct)
	if strings.Contains(acct, "/im/admin/users") {
		t.Error("一般利用者に利用者管理へのリンクが出る")
	}
	if res, _ := e.post(ca, "/im/admin/users", url.Values{"csrf": {aCSRF}, "login": {"mallory"}, "role": {"admin"},
		"password": {"mallory-pass-12"}, "confirm": {"mallory-pass-12"}}); res.StatusCode != http.StatusNotFound {
		t.Errorf("一般利用者の追加: %d", res.StatusCode)
	}
	for _, action := range []string{"disable", "totp-reset", "role"} {
		if res, _ := e.post(ca, "/im/admin/users/root/"+action, url.Values{"csrf": {aCSRF}, "role": {"member"}}); res.StatusCode != http.StatusNotFound {
			t.Errorf("一般利用者の %s: %d", action, res.StatusCode)
		}
	}
	if _, err := store.UserByLogin(ctx, e.db, "mallory"); err == nil {
		t.Error("一般利用者が利用者を追加できた")
	}
	if u, _ := store.UserByID(ctx, e.db, root.ID); u.Disabled || u.Role != "admin" {
		t.Error("一般利用者の操作で管理者が変わった")
	}

	c := e.client()
	e.enroll(c, "root", "root-password-12")
	res, page := e.get(c, "/im/admin/users")
	if res.StatusCode != 200 || !strings.Contains(page, `href="/im/admin/users/alice"`) {
		t.Fatalf("利用者一覧: %d %s", res.StatusCode, page)
	}
	if _, hub := e.get(c, "/im/"); !strings.Contains(hub, "/im/admin/users") || !strings.Contains(hub, "/im/account") {
		t.Error("ハブに設定・管理へのリンクが無い")
	}

	add := func(f url.Values) (*http.Response, string) {
		return e.formAt(c, "/im/admin/users", "/im/admin/users", f)
	}
	if res, _ := e.post(c, "/im/admin/users", url.Values{"login": {"carol"}, "role": {"member"}, "password": {"carol-pass-123"}, "confirm": {"carol-pass-123"}}); res.StatusCode != http.StatusForbidden {
		t.Errorf("CSRF なしの追加: %d", res.StatusCode)
	}
	for _, f := range []url.Values{
		{"login": {"a b"}, "role": {"member"}, "password": {"carol-pass-123"}, "confirm": {"carol-pass-123"}},
		{"login": {"carol"}, "role": {"owner"}, "password": {"carol-pass-123"}, "confirm": {"carol-pass-123"}},
		{"login": {"carol"}, "role": {"member"}, "password": {"short"}, "confirm": {"short"}},
		{"login": {"carol"}, "role": {"member"}, "password": {"carol-pass-123"}, "confirm": {"carol-pass-124"}},
		{"login": {"alice"}, "role": {"member"}, "password": {"carol-pass-123"}, "confirm": {"carol-pass-123"}},
	} {
		if res, body := add(f); res.StatusCode != http.StatusBadRequest || strings.Contains(body, "carol-pass-12") {
			t.Errorf("追加の入力誤り %v: %d", f, res.StatusCode)
		}
	}
	if _, err := store.UserByLogin(ctx, e.db, "carol"); err == nil {
		t.Fatal("拒否したのに carol がいる")
	}
	if res, body := add(url.Values{"login": {"carol"}, "name": {"キャロル"}, "role": {"member"}, "password": {"carol-pass-123"}, "confirm": {"carol-pass-123"}}); res.StatusCode != 200 || !strings.Contains(body, "追加しました") {
		t.Fatalf("追加: %d", res.StatusCode)
	}
	carol, err := store.UserByLogin(ctx, e.db, "carol")
	if err != nil || carol.Role != "member" || carol.DisplayName != "キャロル" || carol.TOTPEnabled {
		t.Fatalf("追加した利用者: %+v %v", carol, err)
	}
	cc := e.client()
	carolSecret := e.enroll(cc, "carol", "carol-pass-123") // 初回ログインで TOTP 登録へ進む

	act := func(action string, f url.Values) (*http.Response, string) {
		return e.formAt(c, "/im/admin/users/carol", "/im/admin/users/carol/"+action, f)
	}
	reload := func() store.User { u, _ := store.UserByID(ctx, e.db, carol.ID); return u }

	// 役割
	if res, _ := act("role", url.Values{"role": {"admin"}}); res.StatusCode != 200 || reload().Role != "admin" {
		t.Errorf("admin へ: %d %s", res.StatusCode, reload().Role)
	}
	if res, _ := act("role", url.Values{"role": {"member"}}); res.StatusCode != 200 || reload().Role != "member" {
		t.Errorf("member へ: %d", res.StatusCode)
	}
	if res, _ := act("role", url.Values{"role": {"root"}}); res.StatusCode != http.StatusBadRequest || reload().Role != "member" {
		t.Errorf("不正な役割: %d", res.StatusCode)
	}

	// プロジェクト権限
	memberRole := func() string {
		var role string
		e.db.QueryRow("SELECT role FROM project_members WHERE project_id = ? AND user_id = ?", pr.ID, carol.ID).Scan(&role)
		return role
	}
	for _, role := range []string{"editor", "viewer", "admin"} {
		if res, _ := act("member", url.Values{"project": {"req"}, "role": {role}}); res.StatusCode != 200 || memberRole() != role {
			t.Errorf("権限 %s: %d %s", role, res.StatusCode, memberRole())
		}
	}
	if res, _ := act("member", url.Values{"project": {"req"}, "role": {"owner"}}); res.StatusCode != http.StatusBadRequest || memberRole() != "admin" {
		t.Errorf("不正な権限: %d", res.StatusCode)
	}
	if res, _ := act("member", url.Values{"project": {"nope"}, "role": {"viewer"}}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("無いプロジェクト: %d", res.StatusCode)
	}
	if res, _ := act("member", url.Values{"project": {"req"}, "role": {""}}); res.StatusCode != 200 || memberRole() != "" {
		t.Errorf("権限の解除: %d %s", res.StatusCode, memberRole())
	}

	// 無効化: セッションもトークンも使えない。有効化で戻る
	carolAPI := e.apiAs(carol)
	if res, _ := act("disable", url.Values{}); res.StatusCode != 200 || !reload().Disabled {
		t.Fatalf("無効化: %d", res.StatusCode)
	}
	if res, _ := e.get(cc, "/im/"); res.StatusCode != http.StatusSeeOther {
		t.Errorf("無効化した利用者のセッション: %d", res.StatusCode)
	}
	if code := e.meStatus(carolAPI.token); code != http.StatusUnauthorized {
		t.Errorf("無効化した利用者のトークン: %d", code)
	}
	if res, _ := e.login(e.client(), "carol", "carol-pass-123"); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("無効化した利用者のログイン: %d", res.StatusCode)
	}
	if res, _ := act("enable", url.Values{}); res.StatusCode != 200 || reload().Disabled {
		t.Fatalf("有効化: %d", res.StatusCode)
	}
	if code := e.meStatus(carolAPI.token); code != 200 {
		t.Errorf("有効化後のトークン: %d", code)
	}

	// パスワード再設定: セッション破棄、新しいパスワードでログインできる
	e.relogin(cc, "carol", "carol-pass-123", carolSecret)
	if res, _ := act("password", url.Values{"password": {"short"}, "confirm": {"short"}}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("短いパスワード: %d", res.StatusCode)
	}
	if res, _ := act("password", url.Values{"password": {"carol-reset-123"}, "confirm": {"carol-reset-123"}}); res.StatusCode != 200 {
		t.Fatalf("再設定: %d", res.StatusCode)
	}
	if res, _ := e.get(cc, "/im/"); res.StatusCode != http.StatusSeeOther {
		t.Errorf("再設定前のセッションが残る: %d", res.StatusCode)
	}
	e.relogin(cc, "carol", "carol-reset-123", carolSecret)

	// TOTP リセット: 次回ログインで再登録
	if res, _ := act("totp-reset", url.Values{}); res.StatusCode != 200 || reload().TOTPEnabled {
		t.Fatalf("TOTP リセット: %d", res.StatusCode)
	}
	if res, _ := e.login(e.client(), "carol", "carol-reset-123"); res.Header.Get("Location") != "/im/login/totp/setup" {
		t.Errorf("リセット後のログイン: %s", res.Header.Get("Location"))
	}
	if res, _ := act("totp-reset", url.Values{}); res.StatusCode != 200 {
		t.Errorf("未登録の利用者の TOTP リセット: %d", res.StatusCode)
	}

	// トークンの失効（他人のトークン ID は拒否）
	rootAPI := e.apiAs(root)
	var rootTokenID, carolTokenID int64
	e.db.QueryRow("SELECT id FROM api_tokens WHERE user_id = ?", root.ID).Scan(&rootTokenID)
	e.db.QueryRow("SELECT id FROM api_tokens WHERE user_id = ?", carol.ID).Scan(&carolTokenID)
	if res, _ := act("revoke", url.Values{"token": {strconv.FormatInt(rootTokenID, 10)}}); res.StatusCode != http.StatusNotFound || e.meStatus(rootAPI.token) != 200 {
		t.Errorf("別の利用者のトークン ID: %d", res.StatusCode)
	}
	if res, body := act("revoke", url.Values{"token": {strconv.FormatInt(carolTokenID, 10)}}); res.StatusCode != 200 || e.meStatus(carolAPI.token) != http.StatusUnauthorized ||
		!strings.Contains(body, "失効・期限切れのトークン（1 件）") {
		t.Errorf("トークン失効: %d %s", res.StatusCode, body)
	}

	// 自分自身は変更できない（管理者が 0 人にならない）
	for _, action := range []string{"disable", "role", "totp-reset", "password"} {
		res, _ := e.formAt(c, "/im/admin/users/root", "/im/admin/users/root/"+action, url.Values{"role": {"member"}, "password": {"root-new-pass-1"}, "confirm": {"root-new-pass-1"}})
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("自分自身の %s: %d", action, res.StatusCode)
		}
	}
	if u, _ := store.UserByID(ctx, e.db, root.ID); u.Disabled || u.Role != "admin" || !u.TOTPEnabled {
		t.Errorf("自分自身が変わった: %+v", u)
	}
	if n, _ := store.CountActiveAdmins(ctx, e.db); n != 1 {
		t.Errorf("有効な管理者 = %d", n)
	}

	// 存在しない利用者・操作
	if res, _ := e.get(c, "/im/admin/users/nobody"); res.StatusCode != http.StatusNotFound {
		t.Errorf("存在しない利用者: %d", res.StatusCode)
	}
	if res, _ := act("explode", url.Values{}); res.StatusCode != http.StatusNotFound {
		t.Errorf("存在しない操作: %d", res.StatusCode)
	}
}
