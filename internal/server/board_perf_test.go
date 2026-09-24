package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/store"
)

// ボードの応答の大きさと見直しの費用（本文を載せない・304・gzip・本文の検索）の検査。

// boardPerfEnv は本文とコメントに目印を持つイシュー 2 件のあるプロジェクトと、閲覧者の API を作る。
func boardPerfEnv(t *testing.T) (*env, *apiClient, *apiClient) {
	t.Helper()
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("req")
	u := e.user("viewer", "viewer-password-1", "member")
	store.SetMember(ctx, e.db, pr.ID, u.ID, "viewer")
	adm := e.apiAs(e.adminIn("root", "root-password-12", "req"))
	adm.json(201, "POST", "/projects/req/issues", map[string]any{"title": "要件", "type": "requirement",
		"body": "本文の目印 BodyMarkerQZ"}, nil)
	adm.json(201, "POST", "/projects/req/issues", map[string]any{"title": "実装", "body": "別の本文"}, nil)
	adm.json(201, "POST", "/issues/REQ-0002/comments", map[string]any{"text": "コメントの目印 CommentMarkerQZ"}, nil)
	return e, adm, e.apiAs(u)
}

// TestBoardOmitsBody はボードの JSON に本文もコメントも載らないこと。
// 対照: 同じ目印が 1 件取得（詳細ドロワーが使う GET /issues/{id}）には載っている（目印が保存されていなければ、載らないのは当然になる）。
func TestBoardOmitsBody(t *testing.T) {
	_, _, view := boardPerfEnv(t)
	code, _, raw := view.do("GET", "/projects/req/board", nil)
	if code != 200 {
		t.Fatalf("board: %d %s", code, raw)
	}
	for _, marker := range []string{"BodyMarkerQZ", "CommentMarkerQZ", `"body"`} {
		if bytes.Contains(raw, []byte(marker)) {
			t.Errorf("ボードの応答に %s が載っている（本文は載せない）", marker)
		}
	}
	if !bytes.Contains(raw, []byte(`"title":"要件"`)) || !bytes.Contains(raw, []byte(`"version":`)) {
		t.Fatalf("前提が崩れている: ボードの応答に題名・版が無い: %s", raw)
	}
	for id, marker := range map[string]string{"REQ-0001": "BodyMarkerQZ", "REQ-0002": "CommentMarkerQZ"} {
		var d issueDetailJSON
		view.json(200, "GET", "/issues/"+id+"?project=req", nil, &d)
		if !strings.Contains(d.Markdown, marker) {
			t.Errorf("前提が崩れている: %s の 1 件取得に目印 %s が無い: %q", id, marker, d.Markdown)
		}
	}
}

// TestBoardSearchBody は本文の検索（GET …/board/search）が本文とコメントに当たった ID だけを返すこと（大文字小文字は区別しない）。
func TestBoardSearchBody(t *testing.T) {
	_, _, view := boardPerfEnv(t)
	search := func(q string) []string {
		t.Helper()
		var got struct {
			Q   string   `json:"q"`
			IDs []string `json:"ids"`
		}
		view.json(200, "GET", "/projects/req/board/search?q="+q, nil, &got)
		if got.IDs == nil {
			t.Fatalf("q=%q: ids が null（空の配列で返す）", q)
		}
		return got.IDs
	}
	cases := []struct {
		q    string
		want []string
	}{
		{"bodymarkerqz", []string{"REQ-0001"}},                   // 本文（大文字小文字を区別しない）
		{"COMMENTMARKERQZ", []string{"REQ-0002"}},                // コメント
		{"%E7%9B%AE%E5%8D%B0", []string{"REQ-0001", "REQ-0002"}}, // 「目印」（両方の本文にある）
		{"no-such-text-anywhere", []string{}},
		{"", []string{}},
	}
	for _, c := range cases {
		if got := search(c.q); !slices.Equal(got, c.want) {
			t.Errorf("q=%q: %v, want %v", c.q, got, c.want)
		}
	}
	view.fail(404, "GET", "/projects/nope/board/search?q=x", nil)
}

// TestBoardNotModified は、内容が変わらない見直しに 304（本文なし）を返し、generated（分単位の時刻）が進んだだけでは
// 変化として扱わないこと。対照: イシューが変わると同じ If-None-Match に 200 と新しい ETag を返す。
func TestBoardNotModified(t *testing.T) {
	e, adm, view := boardPerfEnv(t)
	code, h, raw := view.do("GET", "/projects/req/board", nil)
	tag := h.Get("ETag")
	if code != 200 || tag == "" || len(raw) == 0 {
		t.Fatalf("初回: %d ETag=%q", code, tag)
	}
	var first struct {
		Generated string `json:"generated"`
	}
	if err := json.Unmarshal(raw, &first); err != nil || first.Generated == "" || h.Get("X-Looptrack-Generated") != first.Generated {
		t.Fatalf("generated: %v %q / header %q", err, first.Generated, h.Get("X-Looptrack-Generated"))
	}

	e.clock.Add(3 * time.Minute) // generated だけが変わる
	code, h, raw = view.do("GET", "/projects/req/board", nil, "If-None-Match", tag)
	if code != http.StatusNotModified || len(raw) != 0 {
		t.Fatalf("変化なし: %d（304 のはず）本文 %d バイト", code, len(raw))
	}
	if h.Get("ETag") != tag || h.Get("X-Looptrack-Generated") == "" || h.Get("X-Looptrack-Generated") == first.Generated {
		t.Errorf("304 のヘッダ: ETag=%q generated=%q（前回 %q）", h.Get("ETag"), h.Get("X-Looptrack-Generated"), first.Generated)
	}
	// If-None-Match の書き方の揺れ（弱い比較・列挙・*）
	for _, inm := range []string{strings.TrimPrefix(tag, "W/"), `"other", ` + tag, "*"} {
		if code, _, _ := view.do("GET", "/projects/req/board", nil, "If-None-Match", inm); code != http.StatusNotModified {
			t.Errorf("If-None-Match %q: %d", inm, code)
		}
	}

	// 対照: 見出しを持たない If-None-Match・違う ETag は 200
	if code, _, _ := view.do("GET", "/projects/req/board", nil); code != 200 {
		t.Errorf("If-None-Match なし: %d", code)
	}
	if code, _, _ := view.do("GET", "/projects/req/board", nil, "If-None-Match", `W/"0"`); code != 200 {
		t.Errorf("違う ETag: %d", code)
	}
	// 対照: コメントの追記（題名・状態・updated は変わらなくても版が進む）で ETag が変わる
	adm.json(201, "POST", "/issues/REQ-0001/comments", map[string]any{"text": "追記"}, nil)
	code, h, raw = view.do("GET", "/projects/req/board", nil, "If-None-Match", tag)
	if code != 200 || len(raw) == 0 || h.Get("ETag") == tag {
		t.Errorf("変化あり: %d ETag %q → %q", code, tag, h.Get("ETag"))
	}
}

// TestAPIGzip は Accept-Encoding: gzip の要求に JSON を圧縮して返し、既存のセキュリティヘッダを残すこと。
// 対照: gzip を受けない要求には圧縮しない（同じ経路で Content-Encoding が付くのは要求したときだけ）。
func TestAPIGzip(t *testing.T) {
	_, _, view := boardPerfEnv(t)
	for _, path := range []string{"/projects/req/board", "/projects/req/board/search?q=marker", "/issues/REQ-0001?project=req"} {
		// Accept-Encoding を明示すると、Go の http.Client は自動で展開しない（生の圧縮された本文が見える）
		code, h, raw := view.do("GET", path, nil, "Accept-Encoding", "gzip, deflate, br")
		if code != 200 || h.Get("Content-Encoding") != "gzip" {
			t.Fatalf("%s: %d Content-Encoding=%q", path, code, h.Get("Content-Encoding"))
		}
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("%s: gzip として読めない: %v", path, err)
		}
		plain, err := io.ReadAll(zr)
		if err != nil || !json.Valid(plain) {
			t.Fatalf("%s: 展開した本文が JSON でない: %v %.80q", path, err, plain)
		}
		if !strings.Contains(h.Get("Content-Security-Policy"), "default-src 'self'") || h.Get("X-Content-Type-Options") != "nosniff" ||
			!strings.HasPrefix(h.Get("Content-Type"), "application/json") || !strings.Contains(strings.Join(h.Values("Vary"), ","), "Accept-Encoding") {
			t.Errorf("%s: ヘッダ: %v", path, h)
		}

		code, h, raw = view.do("GET", path, nil, "Accept-Encoding", "identity")
		if code != 200 || h.Get("Content-Encoding") != "" || !json.Valid(raw) {
			t.Errorf("%s（gzip を受けない）: %d Content-Encoding=%q", path, code, h.Get("Content-Encoding"))
		}
		if code, h, _ := view.do("GET", path, nil, "Accept-Encoding", "gzip;q=0"); code != 200 || h.Get("Content-Encoding") != "" {
			t.Errorf("%s（gzip;q=0）: %d Content-Encoding=%q", path, code, h.Get("Content-Encoding"))
		}
	}
	// 304 には本文も Content-Encoding も付けない
	_, h, _ := view.do("GET", "/projects/req/board", nil)
	code, h304, raw := view.do("GET", "/projects/req/board", nil, "If-None-Match", h.Get("ETag"), "Accept-Encoding", "gzip")
	if code != http.StatusNotModified || len(raw) != 0 || h304.Get("Content-Encoding") != "" {
		t.Errorf("304: %d 本文 %d バイト Content-Encoding=%q", code, len(raw), h304.Get("Content-Encoding"))
	}
}

func TestAcceptsGzip(t *testing.T) {
	for in, want := range map[string]bool{
		"": false, "gzip": true, "GZIP": true, "gzip, deflate, br": true, "br, gzip;q=0.5": true, "*": true,
		"identity": false, "gzip;q=0": false, "gzip; q=0.0": false, "deflate": false, "x-gzip": false,
	} {
		if got := acceptsGzip(in); got != want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", in, got, want)
		}
	}
}
