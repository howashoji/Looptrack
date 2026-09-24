package server

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

// api_tokens.created_at を DB の既定値（CURRENT_TIMESTAMP）任せにすると、トークンの「発行」だけが
// アプリの時計（Config.Now / env.clock）を経由せず実時刻になる。「期限」「最終利用」はアプリが書くので従う。
// この検査は /account の実物（HTML）で、発行も最終利用も注入した瞬間になることを確かめる。
// 時間帯の変換そのものは、DB を使わない internal/server/account_tz_test.go が見ている。
// 定数（webTZInstant ほか）は同じパッケージのそのファイルにある。

// setClock は時計を固定の時刻に合わせる（発行日時・最終利用を確定させる）。
func (e *env) setClock(t time.Time) {
	e.clock.mu.Lock()
	e.clock.t = t
	e.clock.mu.Unlock()
}

// TestAccountWebTimeFollowsLoc は /account の実物（トークンの作成日時と最終利用）が、
// アプリの時計（注入できるもの）で作られ、svc.Loc の時間帯で描かれること。
// created_at を DB の既定値に戻すと「発行」の列だけが実時刻になり、2 か所の期待が 1 か所になって落ちる。
// DB が要る。LOOPTRACK_TEST_DSN が無いと省略されず SQLite に落ちて走る（省略されるのは LOOPTRACK_TEST_DB=mysql を明示したときだけ）。
// LOOPTRACK_TEST_DB を明示しない限り方言は DSN の有無で決まる（internal/testutil/mysql.go の Dialect と skipWithoutDB）。
func TestAccountWebTimeFollowsLoc(t *testing.T) {
	instant, err := time.Parse(time.RFC3339, webTZInstant)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ tz, want, notWant string }{
		{"Asia/Tokyo", webTZWantJST, webTZOtherJST},
		{"America/Los_Angeles", webTZWantLA, webTZOtherLA},
	} {
		t.Run(c.tz, func(t *testing.T) {
			e := newEnv(t)
			loc, err := time.LoadLocation(c.tz)
			if err != nil {
				t.Fatal(err)
			}
			e.s.svc.Loc = loc
			e.user("tzalice", "tzalice-password-1", "member")
			cl := e.client() // Accept-Language: ja（文面は日本語で固定する）
			e.enroll(cl, "tzalice", "tzalice-password-1")
			e.setClock(instant) // 発行日時・最終利用をこの瞬間に固定する
			_, body := e.formAt(cl, "/im/account", "/im/account/tokens", url.Values{"name": {"時間帯の検査"}, "days": {"90"}})
			toks := patRe.FindAllString(body, -1)
			if len(toks) != 1 {
				t.Fatalf("発行できていない: %s", body)
			}
			if got := e.meStatus(toks[0]); got != 200 { // 1 回使って last_used_at を入れる
				t.Fatalf("/me = %d", got)
			}
			e.setClock(instant) // /me で時計が動いていても、最終利用はこの瞬間
			res, page := e.get(cl, "/im/account")
			if res.StatusCode != 200 {
				t.Fatalf("/account = %d", res.StatusCode)
			}
			if strings.Count(page, c.want) < 2 { // 作成日時と最終利用の 2 か所
				t.Errorf("Loc=%s: /account に %q が 2 か所出ない:\n%s", c.tz, c.want, page)
			}
			if strings.Contains(page, c.notWant) {
				t.Errorf("Loc=%s: /account に別の時間帯の時刻 %q が出ている", c.tz, c.notWant)
			}
		})
	}
}
