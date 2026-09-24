package server

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/usage"
)

// 案件ラベル別の集計（group=label）。過去分の取り込み済みの行（ブランチ名だけ）と、今後の分（イシューのラベル優先）。

type caseResp struct {
	TotalTokens int64  `json:"total_tokens"`
	CasePattern string `json:"case_pattern"`
	Group       string `json:"group"`
	Groups      []struct {
		Key         string `json:"key"`
		TotalTokens int64  `json:"total_tokens"`
		Stages      int    `json:"stages"`
		Issues      int    `json:"issues"`
	} `json:"groups"`
	ByCase []struct {
		Key         string `json:"key"`
		TotalTokens int64  `json:"total_tokens"`
	} `json:"by_case"`
}

func (r caseResp) sums() (map[string]int64, int64) {
	m, sum := map[string]int64{}, int64(0)
	for _, g := range r.Groups {
		m[g.Key] += g.TotalTokens
		sum += g.TotalTokens
	}
	return m, sum
}

// importSnapshot は過去分の取り込み（trigger_kind = import）の行を入れる。イシューには付かず、ブランチ名だけが手がかり。
// 取り込みの道具は製品から外したが、取り込み済みの行は DB に残るので、集計が扱えることを確かめる。
// 会話 ID が空なら、セッション ID の先頭 8 桁（取り込みの既定と同じ）。received_at は at と同じ（作業した日で集計に載る）。
func importSnapshot(t *testing.T, db *sql.DB, projectID, userID int64, session, end, conv, branch string, total int64) {
	t.Helper()
	at, err := time.ParseInLocation("2006-01-02 15:04", end, time.FixedZone("JST", 9*3600))
	if err != nil {
		t.Fatal(err)
	}
	if conv == "" {
		conv = session[:8]
	}
	row := store.UsageSnapshot{ProjectID: projectID, UserID: userID, Client: "import-csv", SessionID: session, ConversationID: conv,
		Trigger: usage.TriggerImport, At: at, ReceivedAt: at, Counters: usage.Counters{Main: usage.Tokens{Input: total}, Responses: 1},
		Branch: branch, DedupeKey: "import:" + session}
	if _, _, err := store.InsertUsageSnapshot(context.Background(), db, row); err != nil {
		t.Fatal(err)
	}
}

func TestUsageReportByCase(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	web, other := e.project("web"), e.project("other")
	editor := e.user("editor", "editor-password-1", "member")
	store.SetMember(ctx, e.db, web.ID, editor.ID, "editor")
	store.SetMember(ctx, e.db, other.ID, editor.ID, "editor")
	if err := store.SetRules(ctx, e.db, web.ID, []byte(`{"usage": {"case_pattern": "CASE-\\d+"}}`)); err != nil {
		t.Fatal(err)
	}
	a := e.apiAs(editor)

	// 取り込み済みの過去分。イシューには付かず、ブランチ名だけが手がかり
	importSnapshot(t, e.app, web.ID, editor.ID, "aaaaaaaa-0001", "2026-09-12 11:00", "20260912090000-aaaaaa", "CASE-101", 1000)
	importSnapshot(t, e.app, web.ID, editor.ID, "aaaaaaaa-0002", "2026-09-12 13:00", "20260912090000-aaaaaa", "CASE-101", 1500) // 再開（同じ会話・差 500）
	importSnapshot(t, e.app, web.ID, editor.ID, "bbbbbbbb-0001", "2026-09-12 12:00", "20260912090000-bbbbbb", "CASE-102", 300)
	importSnapshot(t, e.app, web.ID, editor.ID, "cccccccc-0001", "2026-09-12 12:30", "20260912090000-cccccc", "master", 70)
	importSnapshot(t, e.app, web.ID, editor.ID, "dddddddd-0001", "2026-09-12 12:45", "", "", 5)

	// 今後の分: イシューのラベルに案件があればブランチより優先。無ければブランチ
	a.json(201, "POST", "/projects/web/issues", map[string]any{"title": "案件付き", "labels": []string{"R-01③", "CASE-103 保守リスク再監査 対応"}}, nil)
	a.json(201, "POST", "/projects/web/issues", map[string]any{"title": "案件なし", "labels": []string{"Wave2"}}, nil)
	post := func(slug, conv, trigger, issue, op, branch string, total int64) {
		t.Helper()
		b := usageBody(conv, "s-"+conv, trigger, issue, op, total, 0, 1)
		b["at"], b["branch"] = "2026-09-13T01:00:00Z", branch
		a.json(201, "POST", "/projects/"+slug+"/usage", b, nil)
	}
	post("web", "h1", "issue_op", "WEB-0001", "comment", "CASE-101", 40)           // ラベル CASE-103 が優先
	post("web", "h2", "issue_op", "WEB-0002", "comment", "feature/CASE-102-x", 20) // ラベルに案件なし → ブランチ
	post("web", "h3", "stop", "", "", "CASE-104", 8)                               // 未帰属 → ブランチ

	var rep caseResp
	a.json(200, "GET", "/projects/web/usage/report?from=2026-09-01&to=2026-09-30&group=label", nil, &rep)
	got, sum := rep.sums()
	if rep.Group != "label" || rep.CasePattern != `CASE-\d+` || rep.TotalTokens != 1943 || sum != rep.TotalTokens {
		t.Errorf("group=label: group=%q pattern=%q total=%d 案件の和=%d", rep.Group, rep.CasePattern, rep.TotalTokens, sum)
	}
	want := map[string]int64{"CASE-101": 1500, "CASE-102": 320, "CASE-103": 40, "CASE-104": 8, "": 75}
	if len(got) != len(want) {
		t.Errorf("案件: %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("案件 %q = %d, want %d", k, got[k], v)
		}
	}
	if len(rep.ByCase) != len(rep.Groups) || rep.Groups[0].Key != "CASE-101" || rep.Groups[0].Stages != 2 || rep.Groups[0].Issues != 0 {
		t.Errorf("by_case と groups: %+v / %+v", rep.ByCase, rep.Groups)
	}

	// 表示用の文: group=label は案件別の表だけ。指定なしは他の表と一緒に出す
	code, _, body := a.do("GET", "/projects/web/usage/report?from=2026-09-01&to=2026-09-30&group=label&format=md", nil)
	if s := string(body); code != 200 || !strings.Contains(s, "## 案件別") || !strings.Contains(s, "| CASE-101 | 1,500 | 2 | 0 |") ||
		!strings.Contains(s, "| （なし） | 75 |") || strings.Contains(s, "## イシュー別") {
		t.Errorf("group=label の md: %d %s", code, s)
	}
	code, _, body = a.do("GET", "/projects/web/usage/report?from=2026-09-01&to=2026-09-30&format=md", nil)
	if s := string(body); code != 200 || !strings.Contains(s, "## 案件別") || !strings.Contains(s, "## イシュー別") || !strings.Contains(s, "/CASE-\\d+/") {
		t.Errorf("md: %d %s", code, s)
	}
	// xlsx に案件別のシート
	code, _, body = a.do("GET", "/projects/web/usage/report.xlsx?from=2026-09-01&to=2026-09-30", nil)
	if code != 200 || !xlsxHasSheet(t, body, "案件別") {
		t.Errorf("xlsx に案件別のシートがない: %d", code)
	}
	// 他の切り口も選べる。知らない切り口は 400
	a.json(200, "GET", "/projects/web/usage/report?from=2026-09-01&to=2026-09-30&group=stage", nil, &rep)
	if _, s := rep.sums(); rep.Group != "stage" || s != rep.TotalTokens {
		t.Errorf("group=stage: %+v", rep)
	}
	if e := a.fail(400, "GET", "/projects/web/usage/report?from=2026-09-01&group=model", nil); !strings.Contains(e.Error.Message, "label") {
		t.Errorf("知らない切り口: %s", e.Error.Message)
	}

	// 正規表現が未設定のプロジェクトは、ブランチ名に案件の形があっても全部「案件なし」
	a.json(201, "POST", "/projects/other/issues", map[string]any{"title": "x", "labels": []string{"CASE-1"}}, nil)
	post("other", "o1", "issue_op", "OTHER-0001", "comment", "CASE-101", 30)
	post("other", "o2", "stop", "", "", "CASE-102", 12)
	a.json(200, "GET", "/projects/other/usage/report?from=2026-09-01&to=2026-09-30&group=label", nil, &rep)
	if got, sum := rep.sums(); rep.CasePattern != "" || rep.TotalTokens != 42 || sum != 42 || len(got) != 1 || got[""] != 42 {
		t.Errorf("未設定のプロジェクト: pattern=%q total=%d %v", rep.CasePattern, rep.TotalTokens, got)
	}
	code, _, body = a.do("GET", "/projects/other/usage/report?from=2026-09-01&to=2026-09-30&group=label&format=md", nil)
	if s := string(body); code != 200 || !strings.Contains(s, "未設定") {
		t.Errorf("未設定の md: %s", s)
	}
}

// xlsxHasSheet は xlsx（zip）の workbook.xml にそのシート名があるか。
func xlsxHasSheet(t *testing.T, b []byte, name string) bool {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if f.Name != "xl/workbook.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		x, _ := io.ReadAll(rc)
		return strings.Contains(string(x), `name="`+name+`"`)
	}
	return false
}
