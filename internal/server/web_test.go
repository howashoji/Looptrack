package server

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/store"
)

// 閲覧画面（ハブ・ボード）と、そのデータ API。

func TestWebPages(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("req")
	e.project("secret")
	u := e.user("viewer", "viewer-password-1", "member")
	if err := store.SetMember(ctx, e.db, pr.ID, u.ID, "viewer"); err != nil {
		t.Fatal(err)
	}
	adm := e.apiAs(e.adminIn("root", "root-password-12", "req", "secret"))
	adm.json(201, "POST", "/projects/req/issues", map[string]any{"title": "表示するイシュー", "labels": []string{"ui"},
		"body": "本文 `code`\n\n- [ ] 受け入れ条件"}, nil)
	adm.json(201, "POST", "/projects/secret/issues", map[string]any{"title": "非公開の件"}, nil)

	// 未ログインはログイン画面へ
	anon := e.client()
	for _, p := range []string{"/im/", "/im/p/req/", "/im/p/req"} {
		if res, _ := e.get(anon, p); res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(res.Header.Get("Location"), "/im/login") {
			t.Errorf("未ログイン %s: %d %s", p, res.StatusCode, res.Header.Get("Location"))
		}
	}

	c := e.client()
	e.enroll(c, "viewer", "viewer-password-1")
	// ハブ: 表示名を出し、スクリプトは外部ファイル（CSP はインラインを禁じている）
	res, body := e.get(c, "/im/")
	if res.StatusCode != 200 || !strings.Contains(body, "viewer") || !strings.Contains(body, `src="/im/static/hub.js"`) {
		t.Fatalf("ハブ: %d %s", res.StatusCode, body)
	}
	if strings.Contains(body, "<script>") {
		t.Error("インラインスクリプトが入っている")
	}
	// ボード: 権限のあるプロジェクトだけ。末尾スラッシュへ寄せる
	if res, body := e.get(c, "/im/p/req/"); res.StatusCode != 200 || !strings.Contains(body, `data-slug="req"`) {
		t.Errorf("ボード: %d %s", res.StatusCode, body)
	}
	if res, _ := e.get(c, "/im/p/req"); res.StatusCode != http.StatusMovedPermanently || res.Header.Get("Location") != "/im/p/req/" {
		t.Errorf("末尾スラッシュ: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	for _, p := range []string{"/im/p/secret/", "/im/p/nope/", "/im/other"} {
		res, body := e.get(c, p)
		if res.StatusCode != http.StatusNotFound || strings.Contains(body, "非公開") {
			t.Errorf("%s: %d", p, res.StatusCode)
		}
	}
	// 静的ファイルが配信される
	for _, p := range []string{"/im/static/board.js", "/im/static/render.js", "/im/static/board.css", "/im/static/hub.js", "/im/static/hub.css", "/im/static/appbar.css"} {
		if res, _ := e.get(c, p); res.StatusCode != 200 {
			t.Errorf("%s: %d", p, res.StatusCode)
		}
	}
}

// ログイン後の全画面が同じヘッダを使い、アカウント設定・利用者管理・ログアウトはユーザー名のメニューにだけ出る。
// ブランド（ヘッダの先頭）と利用者メニュー（ヘッダの末尾）は全画面で同じテンプレートから出るので、同じ利用者なら文字列が一致する。
func TestAppBar(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("req")
	viewer := e.user("viewer", "viewer-password-1", "member")
	if err := store.SetMember(ctx, e.db, pr.ID, viewer.ID, "viewer"); err != nil {
		t.Fatal(err)
	}
	adm := e.apiAs(e.adminIn("root", "root-password-12", "req"))
	adm.json(201, "POST", "/projects/req/issues", map[string]any{"title": "担当を変える件"}, nil)
	_, reg := e.postJSON("/im/oauth/register", map[string]any{
		"client_name": "ヘッダ確認用", "redirect_uris": []string{"http://localhost:51000/callback"},
	})
	authorize := "/im/oauth/authorize?" + url.Values{"response_type": {"code"}, "client_id": {reg["client_id"].(string)},
		"redirect_uri": {"http://localhost:51000/callback"}, "code_challenge": {challengeOf("verifier-" + strings.Repeat("a", 40))},
		"code_challenge_method": {"S256"}, "state": {"st"}}.Encode()

	// start はヘッダの先頭からブランドまで、menu は利用者メニュー（CSRF の値を除く）。利用者ごとに全画面で同じはず
	type parts struct{ start, menu string }
	seen := map[string]parts{}
	check := func(user, page string, res *http.Response, body string, status int, admin bool) {
		t.Helper()
		if res.StatusCode != status {
			t.Errorf("%s: %d, want %d", page, res.StatusCode, status)
			return
		}
		if n := strings.Count(body, `<header class="appbar`); n != 1 {
			t.Errorf("%s: 共通ヘッダが %d 個", page, n)
			return
		}
		header := body[strings.Index(body, `<header class="appbar`):]
		header = header[:strings.Index(header, "</header>")]
		i, j := strings.Index(header, `<details class="usermenu">`), strings.Index(header, "</details>")
		if i < 0 || j < i {
			t.Errorf("%s: ユーザー名のメニューが無い", page)
			return
		}
		outside, menu := header[:i]+header[j:], header[i:j]
		if strings.Contains(outside, `href="/im/account"`) || strings.Contains(outside, `href="/im/admin/users"`) || strings.Contains(outside, `action="/im/logout"`) {
			t.Errorf("%s: メニューの外にアカウント設定・利用者管理・ログアウトがある", page)
		}
		if !strings.Contains(menu, `href="/im/account"`) || !strings.Contains(menu, `action="/im/logout"`) || !strings.Contains(menu, `name="csrf" value="`) {
			t.Errorf("%s: メニューにアカウント設定・ログアウトが無い: %s", page, menu)
		}
		if got := strings.Contains(menu, `href="/im/admin/users"`); got != admin {
			t.Errorf("%s: 利用者管理のリンク = %v, want %v", page, got, admin)
		}
		// 利用者メニューはヘッダの最後の要素（右端）。後ろに続くのは行とヘッダの閉じタグだけ
		// 行の後ろに置けるのは下段（appbar-below）だけ
		if rest := strings.TrimSpace(header[j+len("</details>"):]); !strings.HasPrefix(rest, "</div>") ||
			(strings.TrimSpace(rest[len("</div>"):]) != "" && !strings.HasPrefix(strings.TrimSpace(rest[len("</div>"):]), `<div class="filters appbar-below"`)) {
			t.Errorf("%s: 利用者メニューの後ろに要素がある: %q", page, rest)
		}
		// 差し込み枠は利用者メニューより前で閉じる（メニューは差し込み枠の外・行の右端）
		if k := strings.Index(header, `<div class="appbar-slot">`); k < 0 || k > i {
			t.Errorf("%s: 差し込み枠（appbar-slot）がブランドと利用者メニューの間に無い", page)
		}
		end := strings.Index(header, "</a>")
		got := parts{start: header[:end+len("</a>")], menu: csrfRe.ReplaceAllString(menu, `name="csrf" value="-"`)}
		if !strings.Contains(got.start, `class="appbar-brand"`) {
			t.Errorf("%s: ヘッダの先頭がブランドでない: %s", page, got.start)
		}
		if want, ok := seen[user]; !ok {
			seen[user] = got
		} else if got != want {
			t.Errorf("%s: ブランドか利用者メニューが他の画面と違う（同じテンプレートから出ていない）\n got %+v\nwant %+v", page, got, want)
		}
	}
	get := func(c *http.Client, user, page string, status int, admin bool) {
		t.Helper()
		res, body := e.get(c, page)
		check(user, page, res, body, status, admin)
	}

	c := e.client()
	e.enroll(c, "viewer", "viewer-password-1")
	for _, p := range []string{"/im/", "/im/p/req/", "/im/account", "/im/p/req/report-requests", authorize} {
		get(c, "viewer", p, 200, false)
	}
	for _, p := range []string{"/im/p/nope/", "/im/other", "/im/admin/users"} {
		get(c, "viewer", p, http.StatusNotFound, false)
	}
	get(c, "viewer", "/im/oauth/authorize?response_type=code&client_id=unknown", http.StatusBadRequest, false) // OAuth のエラー
	// 担当の変更の失敗画面（viewer は変えられない）
	res, body := e.formAt(c, "/im/p/req/", "/im/p/req/issues/REQ-0001/assign", url.Values{"assignee": {"me"}})
	check("viewer", "assign", res, body, http.StatusForbidden, false)

	rc := e.client()
	e.enroll(rc, "root", "root-password-12")
	for _, p := range []string{"/im/", "/im/p/req/", "/im/account", "/im/admin/users", "/im/admin/users/viewer", "/im/admin/projects",
		"/im/p/req/report-requests", authorize} {
		get(rc, "root", p, 200, true)
	}
	get(rc, "root", "/im/other", http.StatusNotFound, true)
	res, body = e.formAt(rc, "/im/p/req/", "/im/p/req/issues/REQ-0001/assign", url.Values{"assignee": {"nobody"}})
	check("root", "assign", res, body, res.StatusCode, true) // 状態コードは担当の変更のテストで見る。ここはヘッダだけ
	if res.StatusCode == http.StatusSeeOther {
		t.Error("存在しない担当者への変更が通った")
	}

	// メニューのログアウトでログアウトできる
	_, body = e.get(c, "/im/")
	menu := body[strings.Index(body, `<details class="usermenu">`):]
	if res, _ := e.post(c, "/im/logout", url.Values{"csrf": {csrfOf(t, menu)}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("メニューのログアウト: %d", res.StatusCode)
	}
	if res, _ := e.get(c, "/im/"); res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(res.Header.Get("Location"), "/im/login") {
		t.Errorf("ログアウト後: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
}

// ヘッダの組み立てとスタイルが 1 か所にあることを、テンプレートとスタイルシートの側から確かめる。
// 画面ごとにヘッダを書き起こす（ボード・担当の変更・レポート作成で実際に起きた）と、ここで落ちる。
func TestAppBarSource(t *testing.T) {
	// テンプレート: ブランドと利用者メニューの部品は layout.html だけにある。他の画面は nav か appbar_start … appbar_end を使う
	names, err := fs.Glob(assets, "templates/*.html")
	if err != nil || len(names) == 0 {
		t.Fatal(names, err)
	}
	for _, name := range names {
		b, err := fs.ReadFile(assets, name)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		if filepath.Base(name) == "layout.html" {
			if strings.Count(src, `class="appbar-brand"`) != 1 || strings.Count(src, `<header class="appbar"`) != 1 || strings.Count(src, `<details class="usermenu">`) != 1 {
				t.Errorf("layout.html: ブランド・ヘッダ・利用者メニューが 1 つずつでない")
			}
			continue
		}
		for _, raw := range []string{"<header", "appbar-brand", "appbar-row", "appbar-slot", `class="usermenu`, `{{template "usermenu"`} {
			if strings.Contains(src, raw) {
				t.Errorf("%s: ヘッダを直に書いている（%s）。layout.html の nav か appbar_start / appbar_end を使う", name, raw)
			}
		}
		count := func(tmpl string) int { return strings.Count(src, `{{template "`+tmpl+`" .}}`) }
		starts, ends, rowEnds, closes, navs := count("appbar_start"), count("appbar_end"), count("appbar_row_end"),
			strings.Count(src, `{{template "appbar_close"}}`), count("nav")
		if starts != ends+rowEnds || rowEnds != closes {
			t.Errorf("%s: appbar_start %d 回と appbar_end %d 回 / appbar_row_end %d 回・appbar_close %d 回が対になっていない",
				name, starts, ends, rowEnds, closes)
		}
		if starts+navs > 1 {
			t.Errorf("%s: ヘッダを %d 回使っている", name, starts+navs)
		}
		// ログイン前の画面（ログイン・確認コード・二段階認証の設定）だけがヘッダを持たない
		login := map[string]bool{"login.html": true, "totp.html": true, "totp_setup.html": true, "setup_required.html": true, // セットアップ未完了も
			"first_run.html": true}[filepath.Base(name)] // 初回設定（利用者がまだいない）
		if strings.Contains(src, `{{define "`+filepath.Base(name)+`"}}`) && !login && starts+navs == 0 {
			t.Errorf("%s: 共通ヘッダ（nav か appbar_start / appbar_end）を使っていない", name)
		}
	}

	// 行（ブランド・差し込み枠・利用者メニュー）は、広い画面の既定では折り返さない。折り返すと、利用者メニューが
	// 2 行目に落ちる（幅 601〜1000px のボードで起きた）。狭い画面の組み替え（@media）は
	// deploy/dev/appbar-check.sh が実際のブラウザで幅ごとに測る
	bar, _ := fs.ReadFile(assets, "static/appbar.css")
	for _, m := range regexp.MustCompile(`(?m)^\.appbar-row\{([^}]*)\}`).FindAllStringSubmatch(string(bar), -1) {
		if strings.Contains(strings.ReplaceAll(m[1], " ", ""), "flex-wrap:wrap") {
			t.Errorf("appbar.css: .appbar-row が折り返す: %s", m[0])
		}
	}

	// スタイル: ヘッダの部品のスタイルは appbar.css だけにある。画面ごとのスタイルシートが持ってよいのは、
	// ボードのヘッダを画面の上に留める指定（position / top / z-index）と、差し込み枠に入れた自分の部品の並べ方だけ
	rule := regexp.MustCompile(`([^{}]+)\{([^{}]*)\}`)
	part := regexp.MustCompile(`\.(appbar[\w-]*|usermenu[\w-]*|avatar)\b`)
	sheets, _ := fs.Glob(assets, "static/*.css")
	for _, name := range sheets {
		if filepath.Base(name) == "appbar.css" {
			continue
		}
		b, _ := fs.ReadFile(assets, name)
		for _, m := range rule.FindAllStringSubmatch(string(b), -1) {
			sel := m[1]
			if k := strings.LastIndex(sel, "*/"); k >= 0 { // 直前のコメントを除く
				sel = sel[k+2:]
			}
			for _, one := range strings.Split(sel, ",") {
				one = strings.TrimSpace(one)
				targets := part.FindAllStringSubmatch(one, -1)
				if len(targets) == 0 {
					continue
				}
				last := one[strings.LastIndexAny(one, " >+~")+1:] // セレクタが最後に指す要素
				switch {
				case one == ".appbar" && onlyProps(m[2], "position", "top", "z-index"):
				case !part.MatchString(last): // .appbar-row .grow のように、差し込んだ自分の部品を指す
				default:
					t.Errorf("%s: ヘッダの部品 %q のスタイルを持っている（appbar.css に置く）", name, one)
				}
			}
		}
	}
}

// onlyProps は CSS の宣言がすべて props のどれかであるか。
func onlyProps(decls string, props ...string) bool {
	for _, d := range strings.Split(decls, ";") {
		if d = strings.TrimSpace(d); d == "" {
			continue
		}
		name, _, _ := strings.Cut(d, ":")
		if !slices.Contains(props, strings.TrimSpace(name)) {
			return false
		}
	}
	return true
}

func TestBoardAPI(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("req")
	u := e.user("viewer", "viewer-password-1", "member")
	store.SetMember(ctx, e.db, pr.ID, u.ID, "viewer")
	adm := e.apiAs(e.adminIn("root", "root-password-12", "req"))
	var created struct {
		Issue issueDetailJSON `json:"issue"`
	}
	adm.json(201, "POST", "/projects/req/issues", map[string]any{"title": "要件", "type": "requirement",
		"body": "本文", "labels": []string{"ui"}}, &created)
	adm.json(201, "POST", "/projects/req/issues", map[string]any{"title": "実装", "traces": []string{"REQ-0001"},
		"blocked_by": []string{"REQ-0001"}}, nil)
	adm.json(201, "POST", "/issues/REQ-0001/comments", map[string]any{"text": "コメント本文"}, nil)

	var board struct {
		Slug           string           `json:"slug"`
		Project        string           `json:"project"`
		Prefix         string           `json:"prefix"`
		Generated      string           `json:"generated"`
		Statuses       []string         `json:"statuses"`
		ClosedStatuses []string         `json:"closed_statuses"`
		Types          []string         `json:"types"`
		Priorities     []string         `json:"priorities"`
		Issues         []boardIssueJSON `json:"issues"`
	}
	view := e.apiAs(u)
	view.json(200, "GET", "/projects/req/board", nil, &board)
	if board.Slug != "req" || board.Prefix != "REQ" || len(board.Statuses) != 6 || len(board.Types) != 6 || board.Generated == "" {
		t.Fatalf("board: %+v", board)
	}
	if len(board.Issues) != 2 {
		t.Fatalf("件数 = %d", len(board.Issues))
	}
	one := board.Issues[0]
	if one.ID != "REQ-0001" || one.Title != "要件" || !one.Ready || one.Path != "REQ-0001-要件.md" || one.Version < 2 {
		t.Errorf("項目: %+v", one)
	}
	if board.Issues[1].Ready {
		t.Error("blocked_by が未解決なのに着手可になっている")
	}
	// サーバで起票したものは imported=false（画面は「ファイル」行を出さない）。取り込み由来（import のイベントあり）だけ true
	if one.Imported || board.Issues[1].Imported {
		t.Errorf("API 起票が取り込み由来になっている: %v %v", one.Imported, board.Issues[1].Imported)
	}
	if _, err := e.db.ExecContext(ctx, `INSERT INTO issue_events (project_id, issue_id, kind, via, detail)
SELECT project_id, id, 'import', 'import', JSON_OBJECT('file', 'open/REQ-0002-実装.md') FROM issues WHERE display_id = 'REQ-0002'`); err != nil {
		t.Fatal(err)
	}
	view.json(200, "GET", "/projects/req/board", nil, &board)
	if board.Issues[0].Imported || !board.Issues[1].Imported || board.Issues[1].Path != "REQ-0002-実装.md" {
		t.Errorf("取り込み由来の判定: %+v / %+v", board.Issues[0], board.Issues[1])
	}
	if b, _ := json.Marshal(one); !strings.Contains(string(b), `"labels":["ui"]`) || !strings.Contains(string(b), `"refs":[]`) {
		t.Errorf("JSON: %s", b)
	}
	// 権限の無いプロジェクトは 404、未認証は 401
	view.fail(404, "GET", "/projects/nope/board", nil)
	if res, _ := e.get(e.client(), "/im/api/v1/projects/req/board"); res.StatusCode != 401 {
		t.Errorf("未認証: %d", res.StatusCode)
	}
}

// TestBoardImportedFlag は旧形式（フィクスチャ）の取り込み由来のイシューだけ imported=true になり、取り込み後に API で起票したものは
// false になることを確かめる（詳細ペインの「ファイル」行の出し分け）。
func TestBoardImportedFlag(t *testing.T) {
	e, _ := setupGolden(t, "demo")
	ctx := context.Background()
	p, _ := store.ProjectBySlug(ctx, e.db, "demo")
	u := e.user("editor86", "editor86-password-1", "member")
	store.SetMember(ctx, e.db, p.ID, u.ID, "editor")
	api := e.apiAs(u)
	var created struct {
		Issue issueDetailJSON `json:"issue"`
	}
	api.json(201, "POST", "/projects/demo/issues", map[string]any{"title": "取り込み後の起票"}, &created)
	var board struct {
		Issues []boardIssueJSON `json:"issues"`
	}
	api.json(200, "GET", "/projects/demo/board", nil, &board)
	imported := 0
	for _, it := range board.Issues {
		switch {
		case it.ID == created.Issue.ID:
			if it.Imported {
				t.Errorf("%s: API で起票したのに imported=true", it.ID)
			}
		case !it.Imported || it.Path == "":
			t.Errorf("%s: 取り込み由来なのに imported=%v path=%q", it.ID, it.Imported, it.Path)
		default:
			imported++
		}
	}
	if imported == 0 || imported != len(board.Issues)-1 {
		t.Errorf("取り込み由来 %d / 全 %d", imported, len(board.Issues))
	}
}

// TestRenderJS は表示用の変換（エスケープ・Markdown）を Node の検査で確かめる（XSS が起きないこと）。
func TestRenderJS(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node が無いため省略")
	}
	// static の *_test.mjs を全部回す（render.js と、ボードのデータの取り方 board_data.js）
	paths, _ := filepath.Glob(filepath.Join("static", "*_test.mjs"))
	if len(paths) < 2 {
		t.Fatalf("前提が崩れている: static の *_test.mjs が %v", paths)
	}
	for i, p := range paths {
		paths[i], _ = filepath.Abs(p)
	}
	out, err := exec.Command("node", append([]string{"--test"}, paths...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("node --test が失敗:\n%s", out)
	}
	if !strings.Contains(string(out), "fail 0") {
		t.Errorf("検査結果:\n%s", out)
	}
}
