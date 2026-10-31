package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/store"
)

// プロジェクト記憶の REST（memory_api.go）と画面（memory_web.go）。規則の本体は service（memory.go）にあり、
// ここでは HTTP の形（状態コード・code・ETag・current）と、どの経路でも見えない範囲が 404 になることを確かめる。

type memErrBody struct {
	Error struct {
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Current json.RawMessage `json:"current"`
	} `json:"error"`
}

func TestMemoryREST(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("mem")
	member := func(login, role string) store.User {
		u := e.user(login, login+"-password-1", "member")
		if role != "" {
			if err := store.SetMember(ctx, e.db, pr.ID, u.ID, role); err != nil {
				t.Fatal(err)
			}
		}
		return u
	}
	ed, vi := e.apiAs(member("mem-ed", "editor")), e.apiAs(member("mem-vi", "viewer"))
	stray := e.apiAs(member("mem-stray", ""))
	root := e.apiAs(e.user("mem-root", "root-password-1", "admin")) // 参加していない管理者

	// 追記（行が無ければ作る。201・ETag）→ もう一度追記（200）
	var w struct {
		Memory  memoryJSON `json:"memory"`
		Created bool       `json:"created"`
	}
	h := ed.json(http.StatusCreated, "POST", "/projects/mem/memory/handoff/main/append", map[string]any{"content": "## 1 つ目\n本文 1", "machine_label": "box-a"}, &w)
	if !w.Created || w.Memory.Version != 1 || h.Get("ETag") != `"1"` || w.Memory.Summary != "## 1 つ目" {
		t.Fatalf("最初の追記: %+v ETag=%s", w, h.Get("ETag"))
	}
	ed.json(http.StatusOK, "POST", "/projects/mem/memory/handoff/main/append", map[string]any{"content": "## 2 つ目\n本文 2"}, &w)
	if w.Created || w.Memory.Version != 2 {
		t.Fatalf("2 つ目の追記: %+v", w)
	}

	// 古い版の置き換えは 409（current に今の版）で何も書かない。If-Match が無ければ 428
	code, _, b := ed.do("PUT", "/projects/mem/memory/handoff/main", map[string]any{"content": "古い版から"}, "If-Match", `"1"`)
	var eb memErrBody
	_ = json.Unmarshal(b, &eb)
	if code != http.StatusConflict || eb.Error.Code != "memory_version" || string(eb.Error.Current) != "2" {
		t.Fatalf("古い版の置き換え: %d %s", code, b)
	}
	if code, _, b := ed.do("PUT", "/projects/mem/memory/handoff/main", map[string]any{"content": "x"}); code != http.StatusPreconditionRequired {
		t.Errorf("If-Match なし: %d %s", code, b)
	}
	// 対照: 今の版なら通る
	h = ed.json(http.StatusOK, "PUT", "/projects/mem/memory/handoff/main", map[string]any{"content": "書き直した\n"}, &w, "If-Match", `"2"`)
	if w.Memory.Version != 3 || h.Get("ETag") != `"3"` {
		t.Fatalf("今の版の置き換え: %+v", w)
	}

	// 取得（viewer も読める）と版の履歴
	var d struct {
		Memory   memoryJSON          `json:"memory"`
		Versions []memoryVersionJSON `json:"versions"`
	}
	vi.json(http.StatusOK, "GET", "/projects/mem/memory/handoff/main", nil, &d)
	if d.Memory.Content != "書き直した\n" || len(d.Versions) != 3 || d.Versions[0].Op != "replace" || d.Versions[2].MachineLabel != "box-a" ||
		d.Versions[0].User != "mem-ed" {
		t.Fatalf("取得: %+v", d)
	}
	var one struct {
		Version memoryVersionJSON `json:"version"`
	}
	vi.json(http.StatusOK, "GET", "/projects/mem/memory/handoff/main?version=1", nil, &one)
	if one.Version.Content != "## 1 つ目\n本文 1\n" {
		t.Errorf("版 1 の本文: %q", one.Version.Content)
	}

	// viewer は project の記憶を書けない（403）。参加していない利用者には無いものと同じ 404
	if code, _, b := vi.do("POST", "/projects/mem/memory/handoff/main/append", map[string]any{"content": "x"}); code != http.StatusForbidden {
		t.Errorf("viewer の追記: %d %s", code, b)
	}
	if code, _, b := stray.do("GET", "/projects/mem/memory", nil); code != http.StatusNotFound {
		t.Errorf("参加していない利用者の一覧: %d %s", code, b)
	}

	// personal: 本人だけ。管理者にも 404 で、一覧にも載らない
	ed.json(http.StatusCreated, "POST", "/projects/mem/memory", map[string]any{"scope": "personal", "kind": "caveat", "name": "mine", "content": "本人だけ"}, &w)
	for name, c := range map[string]*apiClient{"root": root, "viewer": vi} {
		if code, _, b := c.do("GET", "/projects/mem/memory/caveat/mine?scope=personal", nil); code != http.StatusNotFound {
			t.Errorf("%s が他人の personal を取得: %d %s", name, code, b)
		}
		var l struct {
			Memories []memoryJSON `json:"memories"`
		}
		c.json(http.StatusOK, "GET", "/projects/mem/memory?deleted=1", nil, &l)
		for _, m := range l.Memories {
			if m.Scope == "personal" {
				t.Errorf("%s の一覧に他人の personal が載った", name)
			}
		}
	}
	ed.json(http.StatusOK, "GET", "/projects/mem/memory/caveat/mine?scope=personal", nil, &d) // 対照: 本人には見える
	if d.Memory.Content != "本人だけ" {
		t.Errorf("本人の personal: %+v", d.Memory)
	}

	// 資格情報の形は 422 で、理由に値を出さない。対照の普通の本文は通る
	code, _, b = ed.do("POST", "/projects/mem/memory/caveat/conn/append", map[string]any{"content": "設定\n" + trSecret})
	if code != http.StatusUnprocessableEntity || !strings.Contains(string(b), "memory_secret") || strings.Contains(string(b), "Synthetic0Value9") {
		t.Errorf("資格情報の追記: %d %s", code, b)
	}
	ed.json(http.StatusCreated, "POST", "/projects/mem/memory/caveat/conn/append", map[string]any{"content": "設定は環境変数から読む"}, &w)

	// 範囲を経路が使わない側に書いた要求は 400 で、何も保存しない
	for _, c := range []struct {
		method, path string
		body         map[string]any
		header       []string
	}{
		{"POST", "/projects/mem/memory/caveat/one/append", map[string]any{"scope": "personal", "content": "x"}, nil},
		{"PUT", "/projects/mem/memory/caveat/one", map[string]any{"scope": "personal", "content": "x"}, []string{"If-Match", `"0"`}},
		{"DELETE", "/projects/mem/memory/caveat/mine", map[string]any{"scope": "personal"}, nil},
		{"POST", "/projects/mem/memory/caveat/one/append", map[string]any{"kind": "handoff", "content": "x"}, nil},
		{"POST", "/projects/mem/memory/caveat/one/append", map[string]any{"name": "other", "content": "x"}, nil},
		{"PUT", "/projects/mem/memory/caveat/one", map[string]any{"kind": "handoff", "content": "x"}, []string{"If-Match", `"0"`}},
		{"PUT", "/projects/mem/memory/caveat/one", map[string]any{"name": "other", "content": "x"}, []string{"If-Match", `"0"`}},
		{"POST", "/projects/mem/memory?scope=personal", map[string]any{"kind": "caveat", "name": "one", "content": "x"}, nil},
	} {
		if code, _, b := ed.do(c.method, c.path, c.body, c.header...); code != http.StatusBadRequest || !strings.Contains(string(b), "invalid_argument") {
			t.Errorf("%s %s: %d %s", c.method, c.path, code, b)
		}
	}
	if code, _, b := ed.do("GET", "/projects/mem/memory/caveat/one", nil); code != http.StatusNotFound {
		t.Errorf("拒んだ要求が project に残った: %d %s", code, b)
	}
	// 対照: 種類と名前を URL だけで渡せば通る
	ed.json(http.StatusCreated, "POST", "/projects/mem/memory/caveat/by-url/append", map[string]any{"content": "URL だけで渡す"}, &w)
	if w.Memory.Kind != "caveat" || w.Memory.Name != "by-url" {
		t.Errorf("URL の種類と名前: %+v", w.Memory)
	}
	ed.json(http.StatusOK, "PUT", "/projects/mem/memory/caveat/by-url", map[string]any{"content": "置き換え"}, &w, "If-Match", `"1"`)
	if w.Memory.Version != 2 {
		t.Errorf("URL だけの置き換え: %+v", w.Memory)
	}
	// 対照: 正しい渡し方は personal に入る
	ed.json(http.StatusCreated, "POST", "/projects/mem/memory/caveat/one/append?scope=personal", map[string]any{"content": "クエリで渡す"}, &w)
	if w.Memory.Scope != "personal" {
		t.Errorf("クエリの範囲: %+v", w.Memory)
	}
	ed.json(http.StatusCreated, "POST", "/projects/mem/memory", map[string]any{"scope": "personal", "kind": "caveat", "name": "two", "content": "本文で渡す"}, &w)
	if w.Memory.Scope != "personal" {
		t.Errorf("本文の範囲: %+v", w.Memory)
	}

	// 消す: 消した後の取得は 404（current に今の版）
	ed.json(http.StatusOK, "DELETE", "/projects/mem/memory/caveat/conn", nil, &w)
	code, _, b = ed.do("GET", "/projects/mem/memory/caveat/conn", nil)
	eb = memErrBody{}
	_ = json.Unmarshal(b, &eb)
	if code != http.StatusNotFound || string(eb.Error.Current) != "2" {
		t.Errorf("消したものの取得: %d %s", code, b)
	}
}

// 画面: 参加者は一覧と詳細を開け、参加していない利用者・他人の personal は 404。
func TestMemoryWeb(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("memw")
	ed := e.user("memw-ed", "editor-password-1", "member")
	vi := e.user("memw-vi", "viewer-password-1", "member")
	e.user("memw-out", "outside-password-1", "member")
	store.SetMember(ctx, e.db, pr.ID, ed.ID, "editor")
	store.SetMember(ctx, e.db, pr.ID, vi.ID, "viewer")
	api := e.apiAs(ed)
	api.json(http.StatusCreated, "POST", "/projects/memw/memory/decision/db/append", map[string]any{"content": "MySQL を使う <b>太字ではない</b>"}, nil)
	api.json(http.StatusCreated, "POST", "/projects/memw/memory/caveat/mine/append?scope=personal", map[string]any{"content": "本人だけのメモ"}, nil)

	vc, board, _ := e.boardSession("memw-vi", "viewer-password-1", "memw")
	if !strings.Contains(board, `href="/im/p/memw/memory"`) {
		t.Error("ボードに記憶の画面へのリンクが無い")
	}
	res, page := e.get(vc, "/im/p/memw/memory")
	if res.StatusCode != http.StatusOK || !strings.Contains(page, "/im/p/memw/memory/project/decision/db") || strings.Contains(page, "/caveat/mine") {
		t.Fatalf("viewer の一覧: %d", res.StatusCode)
	}
	res, page = e.get(vc, "/im/p/memw/memory/project/decision/db")
	if res.StatusCode != http.StatusOK || !strings.Contains(page, "&lt;b&gt;") || strings.Contains(page, "<b>太字") {
		t.Fatalf("viewer の詳細: %d（本文はエスケープして出す）", res.StatusCode)
	}
	if res, _ := e.get(vc, "/im/p/memw/memory/personal/caveat/mine"); res.StatusCode != http.StatusNotFound {
		t.Errorf("他人の personal の詳細: %d", res.StatusCode)
	}
	oc := e.client()
	e.enroll(oc, "memw-out", "outside-password-1")
	if res, _ := e.get(oc, "/im/p/memw/memory"); res.StatusCode != http.StatusNotFound {
		t.Errorf("参加していない利用者の一覧: %d", res.StatusCode)
	}
	// 対照: 本人には personal の詳細が見える
	ec, _, _ := e.boardSession("memw-ed", "editor-password-1", "memw")
	if res, page := e.get(ec, "/im/p/memw/memory/personal/caveat/mine"); res.StatusCode != http.StatusOK || !strings.Contains(page, "本人だけのメモ") {
		t.Errorf("本人の personal の詳細: %d", res.StatusCode)
	}
}

// MCP: 追記・読む・古い版の置き換えの拒否・今の版の置き換え（対照）。解決の経路（project の既定）は REST と別なので通しておく。
func TestMemoryMCP(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("memm")
	u := e.user("memm-ed", "editor-password-1", "member")
	if err := store.SetMember(ctx, e.db, pr.ID, u.ID, "editor"); err != nil {
		t.Fatal(err)
	}
	m := e.mcpAs(e.apiAs(u).token, map[string]string{"X-Looptrack-Project": "memm"})
	text, _ := m.call("list_memories", map[string]any{}, false)
	if !strings.Contains(text, "memm") {
		t.Errorf("空の一覧: %q", text)
	}
	m.call("write_memory", map[string]any{"op": "append", "kind": "handoff", "content": "## 節 1", "machine_label": "box-a"}, false)
	m.call("write_memory", map[string]any{"op": "append", "kind": "handoff", "content": "## 節 2", "machine_label": "box-b"}, false)
	text, data := m.call("get_memory", map[string]any{"kind": "handoff"}, false)
	if !strings.HasPrefix(text, "version: 2\n") || !strings.Contains(text, "## 節 1") || !strings.Contains(text, "## 節 2") {
		t.Fatalf("get_memory: %q %v", text, data)
	}
	// 古い版は isError で、本文は変わらない
	if text, _ := m.call("write_memory", map[string]any{"op": "replace", "kind": "handoff", "content": "古い版から", "version": 1}, true); text == "" {
		t.Error("古い版の replace の理由が空")
	}
	// version を省いた replace も isError（428 と同じ）
	m.call("write_memory", map[string]any{"op": "replace", "kind": "handoff", "content": "版なし"}, true)
	if text, _ := m.call("get_memory", map[string]any{"kind": "handoff"}, false); !strings.HasPrefix(text, "version: 2\n") {
		t.Fatalf("拒まれた後の版: %q", text)
	}
	// 対照: 今の版なら通る
	m.call("write_memory", map[string]any{"op": "replace", "kind": "handoff", "content": "書き直した", "version": 2}, false)
	if text, _ := m.call("get_memory", map[string]any{"kind": "handoff", "version": 1}, false); !strings.Contains(text, "## 節 1") {
		t.Errorf("版 1 の本文: %q", text)
	}
	if text, _ := m.call("list_memories", map[string]any{}, false); !strings.Contains(text, "handoff/main") || !strings.Contains(text, "v3") {
		t.Errorf("一覧: %q", text)
	}
}

// 不正な種類・範囲・版の 400 は入力の値を返さない。秘密の形の値を入れて確かめ、対照の正しい値は通す。
func TestMemoryRESTErrorsDoNotEchoInput(t *testing.T) {
	e := newEnv(t)
	pr := e.project("meme")
	u := e.user("meme-ed", "editor-password-1", "member")
	if err := store.SetMember(context.Background(), e.db, pr.ID, u.ID, "editor"); err != nil {
		t.Fatal(err)
	}
	ed := e.apiAs(u)
	const value = "Synthetic0Value9"
	secret := "password=" + value
	esc := url.QueryEscape(secret)
	for _, c := range []struct {
		method, path string
		body         any
		header       []string
	}{
		{"GET", "/projects/meme/memory/" + url.PathEscape(secret) + "/main", nil, nil},
		{"GET", "/projects/meme/memory/handoff/main?scope=" + esc, nil, nil},
		{"GET", "/projects/meme/memory/handoff/main?version=" + esc, nil, nil},
		{"PUT", "/projects/meme/memory/handoff/main", map[string]any{"content": "x"}, []string{"If-Match", secret}},
		{"POST", "/projects/meme/memory", map[string]any{"kind": secret, "name": "a", "content": "x"}, nil},
		{"POST", "/projects/meme/memory", map[string]any{"scope": secret, "kind": "caveat", "name": "a", "content": "x"}, nil},
	} {
		code, _, b := ed.do(c.method, c.path, c.body, c.header...)
		if code != http.StatusBadRequest || strings.Contains(string(b), value) {
			t.Errorf("%s %s: %d %s（400 で値を返さないはず）", c.method, c.path, code, b)
		}
	}
	// 対照: 正しい値は通る（検査の前で落ちていないことも示す）
	ed.json(http.StatusCreated, "POST", "/projects/meme/memory", map[string]any{"scope": "project", "kind": "caveat", "name": "a", "content": "x"}, nil)
	ed.json(http.StatusOK, "GET", "/projects/meme/memory/caveat/a?scope=project&version=1", nil, nil)
}
