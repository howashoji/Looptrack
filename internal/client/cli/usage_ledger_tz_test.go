package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 台帳の一覧は、サーバのローカル時刻（応答の timezone。IANA 名）で時刻を描き、注記でもその時間帯を名乗る。
// timezone を載せる前のサーバの応答と Asia/Tokyo のときは、以前と一字も変わらない（日本時間のまま）。
func TestUsageLedgerListNamesServerTimezone(t *testing.T) {
	const items = `"items":[{"id":2,"from":"2024-04-01T00:00:00Z","data_end":"2024-04-30T12:00:00Z",` +
		`"created_at":"2024-05-01T01:00:00Z","total_tokens":123456,"name":"2024-04 月次"}]`
	for _, c := range []struct{ name, tz, wantRow, wantNote string }{
		{"timezone が無い（載せる前のサーバ）", "", "2024-04-01 09:00 2024-04-30 21:00 2024-05-01 10:00",
			"1 件。次の --since-last は 2024-04-30 21:00 から（日本時間）"},
		{"Asia/Tokyo", `"timezone":"Asia/Tokyo",`, "2024-04-01 09:00 2024-04-30 21:00 2024-05-01 10:00",
			"1 件。次の --since-last は 2024-04-30 21:00 から（日本時間）"},
		{"America/Los_Angeles", `"timezone":"America/Los_Angeles",`, "2024-03-31 17:00 2024-04-30 05:00 2024-04-30 18:00",
			"1 件。次の --since-last は 2024-04-30 05:00 から（America/Los_Angeles）"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var path string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path = r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"count":1,"next_from":"2024-04-30T12:00:00Z",` + c.tz + items + `}`))
			}))
			defer srv.Close()
			code, stdout, stderr := run(t, map[string]string{"LOOPTRACK_API_URL": srv.URL + "/im",
				"LOOPTRACK_PROJECT": "demo", "LOOPTRACK_TOKEN": "imp_lt"}, "usage", "ledger", "list")
			if code != 0 || path != "/im/api/v1/projects/demo/usage/ledger" {
				t.Fatalf("exit %d path %q stderr %q", code, path, stderr)
			}
			if !strings.Contains(stdout, c.wantRow) {
				t.Errorf("台帳の時刻が %q でない:\n%s", c.wantRow, stdout)
			}
			if !strings.Contains(stdout, c.wantNote) {
				t.Errorf("注記が %q でない:\n%s", c.wantNote, stdout)
			}
			if c.tz == `"timezone":"America/Los_Angeles",` && strings.Contains(stdout, "日本時間") {
				t.Errorf("America/Los_Angeles なのに日本時間と名乗っている:\n%s", stdout)
			}
		})
	}
}
