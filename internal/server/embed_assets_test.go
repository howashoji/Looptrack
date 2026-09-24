package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// テストコードが HTTP で配られていないことを確かめる。
//
// //go:embed が static/* （ディレクトリの中身を丸ごと）だったとき、static/ に置いてある
// node --test 用のテストコード render_test.mjs も実行ファイルに入り、routes の
// GET <base>/static/ （http.FileServerFS）から**認証なしで**配信されていた。
// 埋め込むパターンを配信するものだけに絞ったので、ここで 404 になることを実測する。
//
// 置き場所は動かしていない（CONTRIBUTING.md の手順と CI の
// cd internal/server/static && node --test render_test.mjs はそのまま）。絞ったのは埋め込みのパターンだけ。
//
// DB は使わない（New は db を持つだけで触らない。routes も静的配信に db を使わない）ので、
// このテストは MySQL が無くても走る。
func TestStaticRouteDoesNotServeTestCode(t *testing.T) {
	s, err := New(Config{BasePath: "/im"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.mux)
	defer srv.Close()

	get := func(path string) int {
		t.Helper()
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		return res.StatusCode
	}

	// 先に、この経路が生きていることを確かめる（404 が「経路ごと無い」ことの裏返しでないように）。
	if got := get("/im/static/render.js"); got != http.StatusOK {
		t.Fatalf("/im/static/render.js が %d です（200 のはず。静的配信の経路そのものが壊れています）", got)
	}
	if got := get("/im/static/render_test.mjs"); got != http.StatusNotFound {
		t.Errorf("/im/static/render_test.mjs が %d です（404 のはず）。"+
			"テストコードが実行ファイルに同梱され、認証なしで配信されています。"+
			"server.go の //go:embed を配信するものだけに絞ってください", got)
	}
}
