package server

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/xlsxreport"
	"github.com/xuri/excelize/v2"
)

// 課題管理表（xlsx）の書き出し。画面のボタン（POST）と CLI / 絞り込み（GET）。

// headerRowIndex は見出しの行の添字（1 行目が表題、2 行目が対象の説明、3 行目が空行）。
const headerRowIndex = 3

type exportEnv struct {
	e      *env
	viewer store.User
	client *apiClient
}

func newExportEnv(t *testing.T) *exportEnv {
	t.Helper()
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("req")
	e.project("secret")
	viewer := e.user("viewer", "viewer-password-1", "member")
	if err := store.SetMember(ctx, e.db, pr.ID, viewer.ID, "viewer"); err != nil {
		t.Fatal(err)
	}
	adm := e.apiAs(e.adminIn("root", "root-password-12", "req"))
	for _, it := range []map[string]any{
		{"title": "要件", "type": "requirement", "priority": "P1"},
		{"title": "落ちる", "type": "bug", "priority": "P0", "labels": []string{"web", "api"}},
		{"title": "片付ける", "type": "task", "priority": "P3"},
	} {
		adm.json(201, "POST", "/projects/req/issues", it, nil)
	}
	// 1 件はクローズしておく（既定では出ない）
	adm.json(200, "POST", "/issues/REQ-0003/status", map[string]any{"status": "Done"}, nil)
	return &exportEnv{e: e, viewer: viewer, client: e.apiAs(viewer)}
}

// sheetOf は応答本文を xlsx として開き、行を返す。
func sheetOf(t *testing.T, body []byte) [][]string {
	t.Helper()
	f, err := excelize.OpenReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("xlsx として開けない（%d バイト）: %v", len(body), err)
	}
	defer f.Close()
	rows, err := f.GetRows(xlsxreport.SheetNameIn(i18n.JA))
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func idsOf(rows [][]string) []string {
	out := []string{}
	for _, r := range rows[4:] {
		if len(r) > 0 && r[0] != "" {
			out = append(out, r[0])
		}
	}
	return out
}

func TestExportGet(t *testing.T) {
	x := newExportEnv(t)

	code, head, body := x.client.do("GET", "/projects/req/issues.xlsx", nil)
	if code != 200 {
		t.Fatalf("GET: %d %s", code, body)
	}
	if ct := head.Get("Content-Type"); ct != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
		t.Errorf("Content-Type = %q", ct)
	}
	cd := head.Get("Content-Disposition")
	if !strings.HasPrefix(cd, `attachment; filename="issues-req-`) || !strings.Contains(cd, `.xlsx"; filename*=UTF-8''`) ||
		!strings.Contains(cd, url.PathEscape("課題管理表_req_")) {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if n := head.Get("X-Looptrack-Rows"); n != "2" {
		t.Errorf("X-Looptrack-Rows = %q, want 2", n)
	}
	rows := sheetOf(t, body)
	if got := idsOf(rows); len(got) != 2 || got[0] != "REQ-0002" || got[1] != "REQ-0001" {
		t.Errorf("既定（未クローズ・優先度順） = %v", got)
	}
	if got := rows[1][0]; !strings.Contains(got, "未クローズのみ") || !strings.Contains(got, "2 件") {
		t.Errorf("対象の説明 = %q", got)
	}

	// 絞り込みと並び順、クローズ済みを含める
	code, head, body = x.client.do("GET", "/projects/req/issues.xlsx?all=1&sort=id", nil)
	if code != 200 || head.Get("X-Looptrack-Rows") != "3" {
		t.Fatalf("all=1: %d %s", code, head.Get("X-Looptrack-Rows"))
	}
	if got := idsOf(sheetOf(t, body)); strings.Join(got, ",") != "REQ-0001,REQ-0002,REQ-0003" {
		t.Errorf("all=1&sort=id = %v", got)
	}
	code, _, body = x.client.do("GET", "/projects/req/issues.xlsx?type=bug", nil)
	if code != 200 {
		t.Fatalf("type=bug: %d", code)
	}
	rows = sheetOf(t, body)
	if got := idsOf(rows); len(got) != 1 || got[0] != "REQ-0002" {
		t.Errorf("type=bug = %v", got)
	}
	if got := rows[1][0]; !strings.Contains(got, "種類 bug") {
		t.Errorf("絞り込みの説明 = %q", got)
	}

	// 誤った引数・権限
	if code, _, _ := x.client.do("GET", "/projects/req/issues.xlsx?status=Nope", nil); code != http.StatusBadRequest {
		t.Errorf("状態が不正: %d", code)
	}
	if code, _, _ := x.client.do("GET", "/projects/secret/issues.xlsx", nil); code != http.StatusNotFound {
		t.Errorf("権限の無いプロジェクト: %d", code)
	}
	if res, _ := x.e.get(x.e.client(), "/im/api/v1/projects/req/issues.xlsx"); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("未認証: %d", res.StatusCode)
	}
}

func TestExportPostFromBoard(t *testing.T) {
	x := newExportEnv(t)
	c := x.e.client()
	x.e.enroll(c, "viewer", "viewer-password-1")
	_, page := x.e.get(c, "/im/p/req/")
	if !strings.Contains(page, `data-csrf="`) || !strings.Contains(page, `id="export"`) && !strings.Contains(page, "board.js") {
		t.Fatalf("ボード画面に書き出しの手がかりが無い: %s", page)
	}
	csrf := csrfOf(t, page)

	// 画面が並べた順のまま出す。クローズ済みも指定されれば出す
	res, body := x.e.post(c, "/im/api/v1/projects/req/issues.xlsx", url.Values{
		"csrf":   {csrf},
		"ids":    {"REQ-0003,REQ-0001,REQ-0002"},
		"filter": {"検索「落ちる」 / クローズも含む\n / 並び 優先度 昇順"},
	})
	if res.StatusCode != 200 {
		t.Fatalf("POST: %d %s", res.StatusCode, body)
	}
	rows := sheetOf(t, []byte(body))
	if got := idsOf(rows); strings.Join(got, ",") != "REQ-0003,REQ-0001,REQ-0002" {
		t.Errorf("画面の並び = %v", got)
	}
	if got := rows[1][0]; !strings.Contains(got, "検索「落ちる」 / クローズも含む / 並び 優先度 昇順") {
		t.Errorf("絞り込みの説明（改行は空白にする） = %q", got)
	}

	// 他のプロジェクトの ID は入らない
	res, body = x.e.post(c, "/im/api/v1/projects/req/issues.xlsx", url.Values{
		"csrf": {csrf}, "ids": {"REQ-0001,SEC-0001"}, "filter": {""},
	})
	if res.StatusCode != 200 {
		t.Fatalf("POST（他プロジェクトの ID 混在）: %d %s", res.StatusCode, body)
	}
	if got := idsOf(sheetOf(t, []byte(body))); strings.Join(got, ",") != "REQ-0001" {
		t.Errorf("混在時 = %v", got)
	}

	// CSRF なし・ID なし・権限の無いプロジェクト
	if res, _ := x.e.post(c, "/im/api/v1/projects/req/issues.xlsx", url.Values{"ids": {"REQ-0001"}}); res.StatusCode != http.StatusForbidden {
		t.Errorf("CSRF なし: %d", res.StatusCode)
	}
	if res, _ := x.e.post(c, "/im/api/v1/projects/req/issues.xlsx", url.Values{"csrf": {csrf}, "ids": {""}}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("ID なし: %d", res.StatusCode)
	}
	if res, _ := x.e.post(c, "/im/api/v1/projects/req/issues.xlsx", url.Values{"csrf": {csrf}, "ids": {"NOPE-0001"}}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("知らない ID だけ: %d", res.StatusCode)
	}
	if res, _ := x.e.post(c, "/im/api/v1/projects/secret/issues.xlsx", url.Values{"csrf": {csrf}, "ids": {"REQ-0001"}}); res.StatusCode != http.StatusNotFound {
		t.Errorf("権限の無いプロジェクト: %d", res.StatusCode)
	}
}

// CLI（looptrack issue export）でも同じファイルが保存できる。
func TestExportCLI(t *testing.T) {
	x := newExportEnv(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "課題管理表.xlsx")
	run := func(args ...string) (int, string) {
		t.Helper()
		env := append(cliAPIEnv(x.e.srv.URL+"/im", "req", x.client.token, dir), "CLAUDE_PROJECT_DIR="+dir)
		r := runCLI(t, dir, env, "", append([]string{"issue"}, args...)...)
		return r.code, r.stdout + r.stderr
	}

	if code, msg := run("export", "--xlsx", out, "--all", "--sort", "id"); code != 0 || !strings.Contains(msg, "3 件") {
		t.Fatalf("export: %d %s", code, msg)
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if got := idsOf(sheetOf(t, body)); strings.Join(got, ",") != "REQ-0001,REQ-0002,REQ-0003" {
		t.Errorf("CLI の出力 = %v", got)
	}
	if _, err := os.Stat(out + ".tmp"); !os.IsNotExist(err) {
		t.Error("一時ファイルが残っている")
	}

	// 誤った引数はエラーで終わり、サーバの URL が無ければ使えない
	if code, msg := run("export", "--xlsx", filepath.Join(dir, "x.xlsx"), "--status", "Nope"); code == 0 || !strings.Contains(msg, "Nope") {
		t.Errorf("状態が不正: %d %s", code, msg)
	}
	if r := runCLI(t, dir, append(cliHomeEnv(dir), "CLAUDE_PROJECT_DIR="+dir), "", "issue", "export", "--xlsx", filepath.Join(dir, "y.xlsx")); r.code == 0 ||
		!strings.Contains(r.stderr, "LOOPTRACK_API_URL") {
		t.Errorf("サーバの URL なし: %d %s", r.code, r.stderr)
	}
}

// firstCJK は文字列に含まれる最初の日本語の文字を返す。lang_web_test.go の jaRe より広く、
// 約物（「」・）と全角記号（／）も日本語として見る（英語の文面に全角が混ざるのも訳し漏れなので）。
func firstCJK(s string) (string, bool) {
	for _, r := range s {
		if unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Han, r) ||
			(r >= 0x3000 && r <= 0x303F) || (r >= 0xFF00 && r <= 0xFFEF) {
			return string(r), true
		}
	}
	return "", false
}

// 英語の利用者（Accept-Language: en）が落とす課題管理表。シート名・表題・対象の説明・見出しが英語になる。
// 以前は writeXLSX が言語を渡さず、ファイル名だけ訳されて中身は日本語のままだった。
func TestExportEnglish(t *testing.T) {
	x := newExportEnv(t)

	// テストの client() は Accept-Language: ja を強制するので、付けないクライアント（＝既定の英語）で落とす
	en := *x.client
	en.c = enClient()
	code, _, body := en.do("GET", "/projects/req/issues.xlsx", nil)
	if code != 200 {
		t.Fatalf("GET: %d %s", code, body)
	}
	f, err := excelize.OpenReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("xlsx として開けない（%d バイト）: %v", len(body), err)
	}
	defer f.Close()
	sheet := xlsxreport.SheetNameIn(i18n.EN)
	if got := f.GetSheetList(); len(got) != 1 || got[0] != sheet {
		t.Fatalf("シート名 = %v, want [%q]", got, sheet)
	}
	rows, err := f.GetRows(sheet)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := rows[0][0], "Issues — req (REQ-nnnn)"; got != want {
		t.Errorf("表題 = %q, want %q", got, want)
	}
	// 1 行目（表題）・2 行目（対象の説明）・4 行目（見出し）は製品の文面。日本語が 1 文字も混ざらないこと
	for _, n := range []int{0, 1, headerRowIndex} {
		for _, cell := range rows[n] {
			if c, ok := firstCJK(cell); ok {
				t.Errorf("%d 行目のセル %q に日本語 %q が混ざっている", n+1, cell, c)
			}
		}
	}
	if got := rows[headerRowIndex]; got[0] != "ID" || got[1] != "Type" || got[2] != "Status" {
		t.Errorf("見出し = %v", got)
	}

	// 日本語の利用者には今までどおり日本語で出る（同じサーバ・同じ要求）
	code, _, body = x.client.do("GET", "/projects/req/issues.xlsx", nil)
	if code != 200 {
		t.Fatalf("GET（ja）: %d %s", code, body)
	}
	if got := sheetOf(t, body)[0][0]; got != "課題管理表 — req（REQ-nnnn）" {
		t.Errorf("日本語の表題 = %q", got)
	}
}
