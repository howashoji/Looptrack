package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 時刻を描くどの経路も、応答の最上位の timezone（IANA 名）に従う。
// 応答に timezone が無いとき（timezone を載せる前の古いサーバ）は Asia/Tokyo のまま描く。
// 端末のローカル時間帯には倒さない（決定済みの方針。新しい CLI + 古いサーバで、
// サーバを更新していない日本の利用者の表示が黙って変わる退行を出さないため）。

// tzInstant は検査で使う 1 つの瞬間。日本時間では 2026-09-18 12:04、
// America/Los_Angeles（PDT = UTC-7）では 2026-09-17 20:04 になる。
const (
	tzInstant = "2026-09-18T03:04:05Z"
	wantJST   = "2026-09-18 12:04"
	wantLA    = "2026-09-17 20:04"
	tzLA      = `"timezone":"America/Los_Angeles",`
	tzTokyo   = `"timezone":"Asia/Tokyo",`
)

// tzEpoch は tzInstant の UNIX 秒（activity の last_epoch）。
func tzEpoch(t *testing.T) string {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, tzInstant)
	if err != nil {
		t.Fatal(err)
	}
	return strconv.FormatFloat(float64(ts.Unix()), 'f', -1, 64)
}

// tzRun は偽サーバ（path → 本文）に対して CLI を 1 回動かす。文面は日本語に固定する。
func tzRun(t *testing.T, body func(r *http.Request) (string, string), args ...string) (int, string, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ct, b := body(r)
		w.Header().Set("Content-Type", ct)
		w.Write([]byte(b))
	}))
	defer srv.Close()
	return run(t, map[string]string{"LOOPTRACK_API_URL": srv.URL + "/im", "LOOPTRACK_PROJECT": "demo",
		"LOOPTRACK_TOKEN": "imp_lt", "LOOPTRACK_LANG": "ja"}, args...)
}

func jsonBody(s string) (string, string) { return "application/json", s }

// TestActivityUsesResponseTimezone は activity（epoch 秒を描く経路）。
func TestActivityUsesResponseTimezone(t *testing.T) {
	ep := tzEpoch(t)
	for _, c := range []struct{ name, tz, want string }{
		{"timezone が無い（載せる前のサーバ）", "", wantJST + ":05"},
		{"Asia/Tokyo", tzTokyo, wantJST + ":05"},
		{"America/Los_Angeles", tzLA, wantLA + ":05"},
	} {
		t.Run(c.name, func(t *testing.T) {
			// 端末のローカル時間帯を LA にしても、timezone が無ければ日本時間のまま（端末には倒さない）。
			defer withLocal(t, "America/Los_Angeles")()
			code, stdout, stderr := tzRun(t, func(r *http.Request) (string, string) {
				return jsonBody(fmt.Sprintf(`{%s"now_epoch":%s,"items":[{"id":"DEMO-0001","last_epoch":%s,"last_kind":"comment","events_since":1}]}`,
					c.tz, ep, ep))
			}, "activity", "DEMO-0001")
			if code != 0 {
				t.Fatalf("exit %d stderr %q", code, stderr)
			}
			if !strings.Contains(stdout, c.want) {
				t.Errorf("activity の時刻が %q でない:\n%s", c.want, stdout)
			}
		})
	}
}

// TestIndexUsesResponseTimezone は index（生成日時）。
func TestIndexUsesResponseTimezone(t *testing.T) {
	for _, c := range []struct{ name, tz, loc string }{
		{"timezone が無い（載せる前のサーバ）", "", "Asia/Tokyo"},
		{"Asia/Tokyo", tzTokyo, "Asia/Tokyo"},
		{"America/Los_Angeles", tzLA, "America/Los_Angeles"},
	} {
		t.Run(c.name, func(t *testing.T) {
			defer withLocal(t, "America/Denver")() // 端末は第三の時間帯。どちらにも一致しない
			loc, err := time.LoadLocation(c.loc)
			if err != nil {
				t.Fatal(err)
			}
			before := time.Now().In(loc).Format("2006-01-02 15:04")
			code, stdout, stderr := tzRun(t, func(r *http.Request) (string, string) {
				return jsonBody(fmt.Sprintf(`{%s"count":1,"items":[{"id":"DEMO-0001","type":"task","status":"Todo","priority":"P2","title":"一つ目"}]}`, c.tz))
			}, "index")
			after := time.Now().In(loc).Format("2006-01-02 15:04")
			if code != 0 {
				t.Fatalf("exit %d stderr %q", code, stderr)
			}
			if !strings.Contains(stdout, "> 生成日時: "+before) && !strings.Contains(stdout, "> 生成日時: "+after) {
				t.Errorf("index の生成日時が %s の時刻（%s / %s）でない:\n%s", c.loc, before, after, stdout)
			}
		})
	}
}

// TestSummaryUsesResponseTimezone は summary（フィードバックの行）。
func TestSummaryUsesResponseTimezone(t *testing.T) {
	for _, c := range []struct{ name, tz, want string }{
		{"timezone が無い（載せる前のサーバ）", "", wantJST},
		{"America/Los_Angeles", tzLA, wantLA},
	} {
		t.Run(c.name, func(t *testing.T) {
			defer withLocal(t, "America/Los_Angeles")()
			code, stdout, stderr := tzRun(t, func(r *http.Request) (string, string) {
				return jsonBody(fmt.Sprintf(`{%s"counts":{"open":1,"open_bugs":0},"in_progress":[],"in_review":[],`+
					`"feedback":{"count":1,"issue_count":1,"issues":[{"id":"DEMO-0001","first_at":%q,"pending":1,"excerpt":"反応"}]},`+
					`"ready":[],"ready_total":0,"requirements_ready":[],"requirements_ready_total":0,"usage_requests":[]}`, c.tz, tzInstant))
			}, "summary")
			if code != 0 {
				t.Fatalf("exit %d stderr %q", code, stderr)
			}
			if !strings.Contains(stdout, c.want+" から 1 件") {
				t.Errorf("summary の時刻が %q でない:\n%s", c.want, stdout)
			}
		})
	}
}

// TestVerifyLastUsesResponseTimezone は verify --last（直近の記録の時刻）。
func TestVerifyLastUsesResponseTimezone(t *testing.T) {
	for _, c := range []struct{ name, tz, want string }{
		{"timezone が無い（載せる前のサーバ）", "", wantJST},
		{"America/Los_Angeles", tzLA, wantLA},
	} {
		t.Run(c.name, func(t *testing.T) {
			defer withLocal(t, "America/Los_Angeles")()
			code, stdout, stderr := tzRun(t, func(r *http.Request) (string, string) {
				return jsonBody(fmt.Sprintf(`{%s"id":"DEMO-0001","commands":["go test ./..."],"body_sha256":"abc","current":true,`+
					`"last":{"at":%q,"ok":true,"passed":1,"failed":0,"via":"cli","host":"h","workspace":"w","results":[]}}`, c.tz, tzInstant))
			}, "verify", "DEMO-0001", "--last")
			if code != 0 {
				t.Fatalf("exit %d stderr %q", code, stderr)
			}
			if !strings.Contains(stdout, "直近の verify: "+c.want+" ") {
				t.Errorf("verify --last の時刻が %q でない:\n%s", c.want, stdout)
			}
		})
	}
}

// TestUsageRequestsUseResponseTimezone は usage requests（依頼の登録日時）。
func TestUsageRequestsUseResponseTimezone(t *testing.T) {
	for _, c := range []struct{ name, tz, want string }{
		{"timezone が無い（載せる前のサーバ）", "", wantJST},
		{"America/Los_Angeles", tzLA, wantLA},
	} {
		t.Run(c.name, func(t *testing.T) {
			defer withLocal(t, "America/Los_Angeles")()
			code, stdout, stderr := tzRun(t, func(r *http.Request) (string, string) {
				return jsonBody(fmt.Sprintf(`{%s"project":"demo","count":1,"items":[{"id":1,"created_at":%q,"requested_by":"carol","period":"2026-09","done":false}]}`, c.tz, tzInstant))
			}, "usage", "requests")
			if code != 0 {
				t.Fatalf("exit %d stderr %q", code, stderr)
			}
			if !strings.Contains(stdout, c.want) {
				t.Errorf("usage requests の時刻が %q でない:\n%s", c.want, stdout)
			}
		})
	}
}

// TestListAndShowPassServerTimeThrough は list・show。どちらの時刻もサーバが svc.Loc で描いた文字列で、
// CLI はそれをそのまま出す（CLI 側で描き直さない = 日本時間に固定し直さない）。
// サーバ側で LA になることは internal/server の TestAccountWebTimeFollowsLoc が確かめる。
func TestListAndShowPassServerTimeThrough(t *testing.T) {
	defer withLocal(t, "Asia/Tokyo")() // 端末が日本でも、サーバが LA で描いた文字列はそのまま出る
	code, stdout, stderr := tzRun(t, func(r *http.Request) (string, string) {
		return jsonBody(fmt.Sprintf(`{%s"count":1,"items":[{"id":"DEMO-0001","type":"task","status":"Todo","priority":"P2","title":"一つ目","updated":%q}]}`,
			tzLA, wantLA))
	}, "list", "--sort", "updated")
	if code != 0 {
		t.Fatalf("list: exit %d stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, wantLA) || strings.Contains(stdout, wantJST) {
		t.Errorf("list がサーバの時刻をそのまま出していない（期待 %q）:\n%s", wantLA, stdout)
	}
	md := "---\nid: DEMO-0001\nupdated: " + wantLA + "\n---\n"
	code, stdout, stderr = tzRun(t, func(r *http.Request) (string, string) {
		return "text/markdown; charset=utf-8", md
	}, "show", "DEMO-0001")
	if code != 0 {
		t.Fatalf("show: exit %d stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "updated: "+wantLA) || strings.Contains(stdout, wantJST) {
		t.Errorf("show がサーバの時刻をそのまま出していない（期待 %q）:\n%s", wantLA, stdout)
	}
}

// TestCLICanLoadIANAZones は、固定をやめる前提の裏取り。CLI の実行ファイルは tzdata を埋め込んでいるので、
// tzdata を持たない環境（scratch のコンテナなど）でも IANA の名前を読める（固定をやめることの受け入れ条件）。
func TestCLICanLoadIANAZones(t *testing.T) {
	for _, name := range []string{"America/Los_Angeles", "Asia/Tokyo", "Europe/Berlin", "UTC"} {
		if _, err := time.LoadLocation(name); err != nil {
			t.Errorf("time.LoadLocation(%q) = %v（tzdata の埋め込みが効いていない）", name, err)
		}
	}
	if defaultTZ.String() != "Asia/Tokyo" {
		t.Errorf("既定の時間帯 = %q、期待 Asia/Tokyo", defaultTZ.String())
	}
}

// withLocal は端末のローカル時間帯を name にして、戻す関数を返す。
// 「応答の timezone が無いときに端末へ倒していないか」を見るために使う。
func withLocal(t *testing.T, name string) func() {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	saved := time.Local
	time.Local = loc
	return func() { time.Local = saved }
}
