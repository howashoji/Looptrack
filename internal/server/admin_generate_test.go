package server

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/howashoji/looptrack/internal/auth"
	"github.com/howashoji/looptrack/internal/store"
)

// lockedBuf はサーバのゴルーチンから書かれるログを受ける。
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

var generatedRe = regexp.MustCompile(`value="([^"]+)" autocomplete="off" data-generated-password`)

// generatedFrom は応答に描かれた生成値を取り出す。無ければ空。
func generatedFrom(body string) string {
	if m := generatedRe.FindStringSubmatch(body); m != nil {
		return m[1]
	}
	return ""
}

// 管理画面の追加と再設定で、入力せずにパスワードを自動生成できる。生成値は POST の応答にだけ 1 回描き、
// ログ・リダイレクト・cookie・DB には平文を残さない。手で入力する経路はこれまでどおり（対照）。
func TestAdminGeneratePassword(t *testing.T) {
	logs := &lockedBuf{}
	e := newEnvWith(t, func(cfg *Config) {
		cfg.AllowNoAdmin = true
		cfg.Logger = slog.New(slog.NewTextHandler(logs, nil))
	})
	ctx := context.Background()
	e.user("root", "root-password-12", "admin")
	c := e.client()
	e.enroll(c, "root", "root-password-12")

	// 画面に自動生成の選択肢がある（追加と再設定の両方）
	if _, page := e.get(c, "/im/admin/users"); !strings.Contains(page, `name="generate"`) || generatedFrom(page) != "" {
		t.Fatal("追加のフォームに自動生成の選択肢が無いか、GET で生成値が出る")
	}

	// 追加: 入力欄は空のまま生成を選ぶ
	res, body := e.formAt(c, "/im/admin/users", "/im/admin/users", url.Values{"login": {"bob"}, "role": {"member"}, "generate": {"1"}})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("生成つきの追加: %d", res.StatusCode)
	}
	pw := generatedFrom(body)
	if len([]rune(pw)) < auth.MinPasswordLen {
		t.Fatalf("応答に生成値が無いか短い: %d 文字", len([]rune(pw)))
	}
	if strings.Count(body, pw) != 1 {
		t.Errorf("生成値が応答に %d 回出る", strings.Count(body, pw))
	}
	checkNoLeak(t, res, pw)
	if res, _ := e.login(e.client(), "bob", pw); res.Header.Get("Location") != "/im/login/totp/setup" {
		t.Errorf("生成したパスワードでログインできない: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	// もう一度開いても生成値は出ない
	if _, page := e.get(c, "/im/admin/users"); strings.Contains(page, pw) {
		t.Error("一覧を開き直すと生成値がまた出る")
	}

	// 再設定: 生成を選ぶと古いパスワードは使えなくなり、新しい生成値で入れる
	if _, page := e.get(c, "/im/admin/users/bob"); !strings.Contains(page, `name="generate"`) {
		t.Fatal("再設定のフォームに自動生成の選択肢が無い")
	}
	res, body = e.formAt(c, "/im/admin/users/bob", "/im/admin/users/bob/password", url.Values{"generate": {"1"}, "password": {"ignored"}, "confirm": {"other"}})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("生成つきの再設定: %d", res.StatusCode)
	}
	pw2 := generatedFrom(body)
	if pw2 == "" || pw2 == pw {
		t.Fatal("再設定で新しい生成値が出ない")
	}
	checkNoLeak(t, res, pw2)
	if res, _ := e.login(e.client(), "bob", pw); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("再設定の前のパスワードで入れる: %d", res.StatusCode)
	}
	if res, _ := e.login(e.client(), "bob", pw2); res.Header.Get("Location") != "/im/login/totp/setup" {
		t.Errorf("再設定で生成したパスワードでログインできない: %d", res.StatusCode)
	}

	// 対照: 手で入力する経路は変わらない（空の入力は拒み、2 回の入力が合えば設定する。生成値は出さない）
	if res, body := e.formAt(c, "/im/admin/users/bob", "/im/admin/users/bob/password", url.Values{"password": {""}, "confirm": {""}}); res.StatusCode != http.StatusBadRequest || generatedFrom(body) != "" {
		t.Errorf("空の入力の再設定: %d", res.StatusCode)
	}
	if res, body := e.formAt(c, "/im/admin/users/bob", "/im/admin/users/bob/password", url.Values{"password": {"bob-typed-pass-1"}, "confirm": {"bob-typed-pass-1"}}); res.StatusCode != http.StatusOK || generatedFrom(body) != "" {
		t.Errorf("入力した再設定: %d", res.StatusCode)
	}
	if res, _ := e.login(e.client(), "bob", "bob-typed-pass-1"); res.Header.Get("Location") != "/im/login/totp/setup" {
		t.Errorf("入力したパスワードでログインできない: %d", res.StatusCode)
	}
	if res, body := e.formAt(c, "/im/admin/users", "/im/admin/users", url.Values{"login": {"carol"}, "role": {"member"}}); res.StatusCode != http.StatusBadRequest || generatedFrom(body) != "" {
		t.Errorf("パスワードも生成も無い追加: %d", res.StatusCode)
	}
	if _, err := store.UserByLogin(ctx, e.db, "carol"); err == nil {
		t.Error("パスワードの無い追加が通った")
	}

	// ログには操作が出る（対照）が、生成値は出ない
	out := logs.String()
	if !strings.Contains(out, "user_create") || !strings.Contains(out, "password_generated=true") {
		t.Fatal("前提が崩れています: 管理操作のログを受け取れていない")
	}
	for _, v := range []string{pw, pw2} {
		if strings.Contains(out, v) {
			t.Error("ログに生成値が出る")
		}
	}
	// DB にはハッシュだけを置く
	rows, err := e.db.QueryContext(ctx, "SELECT password_hash FROM users")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(h, pw) || strings.Contains(h, pw2) || !strings.HasPrefix(h, "$argon2id$") {
			t.Error("users.password_hash に平文がある、またはハッシュの形ではない")
		}
	}
}

// checkNoLeak は生成値を描いた応答がキャッシュされず、リダイレクトの URL と cookie に平文を載せないことを確かめる。
func checkNoLeak(t *testing.T, res *http.Response, pw string) {
	t.Helper()
	if cc := res.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q", cc)
	}
	if loc := res.Header.Get("Location"); loc != "" {
		t.Errorf("生成の応答がリダイレクトする: %s", loc)
	}
	for _, ck := range res.Header.Values("Set-Cookie") {
		if strings.Contains(ck, pw) {
			t.Error("cookie に生成値が載る")
		}
	}
	if strings.Contains(res.Request.URL.String(), pw) {
		t.Error("要求の URL に生成値が載る")
	}
}
