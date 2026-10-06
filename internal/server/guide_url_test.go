package server

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// 利用者メニューの「ガイド」の行き先は、公開のリリースの版（プレリリースを含む）ならその版、
// それ以外はすべて latest。日本語の利用者には ja/ が付く。
func TestGuideSiteURL(t *testing.T) {
	const base = guideSiteBase
	cases := []struct {
		version, dir string
	}{
		{"v1.0.1", "v1.0.1/"},
		{"1.0.1", "v1.0.1/"}, // 先頭の v はちょうど 1 つ
		{"v1.0.0-rc.5", "v1.0.0-rc.5/"},
		{"1.0.0-rc.5", "v1.0.0-rc.5/"},
		{"v12.30.400", "v12.30.400/"},
		{"dev", "latest/"},
		{"20261006-abc1234", "latest/"},
		{"v0.0.0-20261006010203-abcdef123456", "latest/"},
		{"", "latest/"},
		{"vv1.0.1", "latest/"},
		{"v1.0", "latest/"},
		{"v1.0.1-rc.0", "latest/"}, // rc の番号は 1 から
		{"v1.0.1-beta.1", "latest/"},
		{"v1.0.1+build", "latest/"},
		{"v1.0.1\n", "latest/"}, // $ は末尾の改行を許さない
		{" v1.0.1", "latest/"},
		{"V1.0.1", "latest/"}, // v は小文字だけ
		{"v1.0.1 ", "latest/"},
		{"v1.0.1\t", "latest/"},
	}
	for _, c := range cases {
		for _, l := range []struct {
			lang   i18n.Lang
			suffix string
		}{{i18n.JA, "ja/"}, {i18n.EN, ""}} {
			if got, want := guideSiteURL(c.version, l.lang), base+c.dir+l.suffix; got != want {
				t.Errorf("guideSiteURL(%q, %s) = %q, want %q", c.version, l.lang, got, want)
			}
		}
	}
	// 対照: 版の行き先が全部 latest に落ちているのではない（上の表の v 付きの行が当たっている）
	if a, b := guideSiteURL("v1.0.1", i18n.EN), guideSiteURL("dev", i18n.EN); a == b {
		t.Errorf("版の違いが行き先に出ていない: %q", a)
	}
}

// 利用者メニューの「ガイド」: リンクと文面が ja・en の両方に出て、新しいタブで開く。
// 言語は要求ごとに固定する（env.client は ja、英語は Accept-Language で頼む）。
// デスクトップ版と同じローカルモードでも、チームのサーバと同じく出る。
func TestUserMenuGuideLink(t *testing.T) {
	menuOf := func(t *testing.T, page string) string {
		t.Helper()
		i := strings.Index(page, `<details class="usermenu">`)
		if i < 0 {
			t.Fatalf("利用者メニューが無い:\n%s", page)
		}
		menu := page[i:]
		return menu[:strings.Index(menu, "</details>")]
	}
	guideItem := regexp.MustCompile(`<a class="usermenu-item" href="([^"]*)" target="_blank" rel="noopener noreferrer">([^<]*)</a>`)
	cases := []struct {
		name, version, lang, href, label string
	}{
		{"ja・リリース", "v1.0.1", "ja", "https://howashoji.github.io/Looptrack/v1.0.1/ja/", "ガイド"},
		{"en・リリース", "v1.0.1", "en", "https://howashoji.github.io/Looptrack/v1.0.1/", "Guide"},
		{"ja・プレリリース", "v1.0.0-rc.5", "ja", "https://howashoji.github.io/Looptrack/v1.0.0-rc.5/ja/", "ガイド"},
		{"en・dev", "dev", "en", "https://howashoji.github.io/Looptrack/latest/", "Guide"},
		{"ja・dev", "dev", "ja", "https://howashoji.github.io/Looptrack/latest/ja/", "ガイド"},
	}
	for _, mode := range []string{"ローカルモード", "チームのサーバ"} {
		for _, c := range cases {
			t.Run(mode+"・"+c.name, func(t *testing.T) {
				e := newEnvWith(t, func(cfg *Config) {
					cfg.LocalMode = mode == "ローカルモード"
					cfg.AllowNoAdmin = mode != "ローカルモード"
					cfg.Version = c.version
				})
				cl := e.client()
				if mode == "チームのサーバ" {
					e.user("alice", "alice-password-1", "member")
					e.enroll(cl, "alice", "alice-password-1")
				} else {
					e.user("boss", "boss-password-12", "admin")
				}
				res, page := e.get(cl, "/im/account", "Accept-Language", c.lang)
				if res.StatusCode != 200 {
					t.Fatalf("/im/account: %d", res.StatusCode)
				}
				menu := menuOf(t, page)
				var found [][]string
				for _, m := range guideItem.FindAllStringSubmatch(menu, -1) {
					if m[1] == c.href {
						found = append(found, m)
					}
				}
				if len(found) != 1 || found[0][2] != c.label {
					t.Errorf("ガイドの項目 = %v, want href=%q 文面=%q を 1 つ\nメニュー: %s", found, c.href, c.label, menu)
				}
				// アカウント設定の次に並ぶ
				if a, g := strings.Index(menu, `href="/im/account"`), strings.Index(menu, c.href); a < 0 || g < a {
					t.Errorf("ガイドがアカウント設定の後ろに無い（%d, %d）", a, g)
				}
			})
		}
	}
}

// 言語を読めない画面（.Lang を詰めていない）では、文面の T と同じく日本語に倒れる。
func TestGuideURLFuncDefaultsToJapanese(t *testing.T) {
	e := newEnvWith(t, func(cfg *Config) { cfg.Version = "v1.0.1" })
	render := func(data map[string]any) string {
		var b bytes.Buffer
		if err := e.s.tmpl.ExecuteTemplate(&b, "usermenu", data); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	user := store.User{Login: "alice", Role: "member"}
	const ja, en = `href="https://howashoji.github.io/Looptrack/v1.0.1/ja/"`, `href="https://howashoji.github.io/Looptrack/v1.0.1/"`
	if got := render(map[string]any{"User": user, "Lang": i18n.EN}); !strings.Contains(got, en) || strings.Contains(got, ja) {
		t.Fatalf("対照: 英語の画面の行き先が英語のガイドでない: %s", got)
	}
	for name, data := range map[string]map[string]any{
		"Lang なし": {"User": user},
		"空の Lang": {"User": user, "Lang": ""},
	} {
		got := render(data)
		if !strings.Contains(got, ja) || !strings.Contains(got, ">ガイド</a>") {
			t.Errorf("%s: 日本語のガイドに倒れていない: %s", name, got)
		}
	}
}
