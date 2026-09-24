package server

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/howashoji/looptrack/internal/client/report"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/usage"
)

// レポートの時刻の表記。集計の値が同じなら、?format=md・xlsx・PDF のどれでも同じ表記で出る
// （以前は xlsx の副題が RFC 3339 の生の文字列だったり、PDF が JST 固定だったりした）。

// tzEnv は 1 会話分のトークンを入れたプロジェクトを用意する。
func tzEnv(t *testing.T) (*env, *apiClient) {
	t.Helper()
	e := newEnv(t)
	pr := e.project("tz")
	editor := e.user("tz-editor", "tz-editor-password-1", "member")
	store.SetMember(context.Background(), e.db, pr.ID, editor.ID, "editor")
	a := e.apiAs(editor)
	a.json(201, "POST", "/projects/tz/issues", map[string]any{"title": "一つ目"}, nil)
	b := usageBody("c1", "s-c1", "issue_op", "TZ-0001", "comment", 300, 0, 300)
	b["at"] = "2026-09-10T01:23:45Z"
	a.json(201, "POST", "/projects/tz/usage", b, nil)
	return e, a
}

// sheetSub は xlsx の 1 枚目のシートの副題（2 行目）。
func sheetSub(t *testing.T, raw []byte) string {
	t.Helper()
	f, err := excelize.OpenReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		t.Fatal("シートがない")
	}
	v, err := f.GetCellValue(sheets[0], "A2")
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestReportDataEndSameInEveryFormat は、データ終端が ?format=md と xlsx で同じローカル時刻の表記で出ることを確かめる。
// 日本語・英語の両方で見る（言語は要求の Accept-Language / ?lang で固定する）。
func TestReportDataEndSameInEveryFormat(t *testing.T) {
	e, a := tzEnv(t)
	const period = "from=2026-09-01&to=2026-09-30"

	var rep struct {
		DataEnd  string `json:"data_end"`
		Timezone string `json:"timezone"`
	}
	a.json(200, "GET", "/projects/tz/usage/report?"+period, nil, &rep)
	end, err := time.Parse(time.RFC3339Nano, rep.DataEnd)
	if err != nil {
		t.Fatalf("data_end が RFC 3339 でない: %q", rep.DataEnd)
	}
	want := end.In(e.s.svc.Loc).Format("2006-01-02 15:04:05")

	// 既定の Loc（Asia/Tokyo）のときの注記は、時間帯を名乗るようにする前と一字も変わらない。
	for _, tc := range []struct{ name, query, lang, note string }{
		{"日本語", period, "ja", "（日本時間・終わりを含まない）"},
		{"英語", period + "&lang=en", "en", "(project local time, end exclusive)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _, body := a.do("GET", "/projects/tz/usage/report?"+tc.query+"&format=md", nil, "Accept-Language", tc.lang)
			if code != 200 || !strings.Contains(string(body), want) {
				t.Errorf("md のデータ終端に %q がない: %d %s", want, code, body)
			}
			if !strings.Contains(string(body), tc.note) {
				t.Errorf("md の対象期間の注記が %q でない: %s", tc.note, body)
			}
			code, _, raw := a.do("GET", "/projects/tz/usage/report.xlsx?"+tc.query, nil, "Accept-Language", tc.lang)
			if code != 200 {
				t.Fatalf("xlsx: %d", code)
			}
			sub := sheetSub(t, raw)
			if !strings.Contains(sub, want) {
				t.Errorf("xlsx の副題のデータ終端が md と違う: want %q in %q", want, sub)
			}
			if strings.Contains(sub, rep.DataEnd) || strings.Contains(sub, "T") && strings.Contains(sub, "Z") {
				t.Errorf("xlsx の副題に RFC 3339 の生の文字列が残っている: %q", sub)
			}
		})
	}
}

// TestReportTimezoneTravelsToPDF は、集計 JSON に載せた時間帯で PDF（report のブロック）が時刻を描き、
// ?format=md と一致することを確かめる。Asia/Tokyo 以外の Loc で見る。
func TestReportTimezoneTravelsToPDF(t *testing.T) {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skipf("tzdata がない: %v", err)
	}
	e, a := tzEnv(t)
	e.s.svc.Loc = loc // プロジェクトのローカル時刻を日本時間以外にする

	code, _, body := a.do("GET", "/projects/tz/usage/report?from=2026-09-01&to=2026-09-30", nil, "Accept-Language", "ja")
	if code != 200 {
		t.Fatalf("集計: %d %s", code, body)
	}
	rep, err := report.Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		DataEnd  string `json:"data_end"`
		Timezone string `json:"timezone"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	if raw.Timezone != "America/Los_Angeles" {
		t.Fatalf("集計 JSON の timezone = %q, want America/Los_Angeles", raw.Timezone)
	}
	end, err := time.Parse(time.RFC3339Nano, raw.DataEnd)
	if err != nil {
		t.Fatal(err)
	}
	wantMinute := end.In(loc).Format("2006-01-02 15:04") // PDF は分まで、md は秒まで
	if s := end.In(loc).Format("2006-01-02 15:04:05"); !strings.Contains(mdText(t, a, "ja"), s) {
		t.Errorf("md のデータ終端が %q でない", s)
	}
	// ?format=md の注記も、実際に描いた時間帯を名乗る。
	for _, c := range []struct{ lang, want string }{
		{"ja", "（America/Los_Angeles・終わりを含まない）"},
		{"en", "(America/Los_Angeles, end exclusive)"},
	} {
		md := mdText(t, a, c.lang)
		if !strings.Contains(md, c.want) {
			t.Errorf("%s: md の対象期間の注記が %q でない: %s", c.lang, c.want, md)
		}
		for _, ng := range []string{"日本時間", "project local time"} {
			if strings.Contains(md, ng) {
				t.Errorf("%s: America/Los_Angeles なのに %q と名乗っている: %s", c.lang, ng, md)
			}
		}
	}

	// PDF に描くブロック（出す側）。時刻はサーバと同じ時間帯になり、注記もその時間帯を名乗る。
	content := map[string]any{"title": "t", "sections": []any{
		map[string]any{"auto": "conversations"},
		map[string]any{"heading": "データの限界", "paragraphs": []any{"（検査用）"}}, // Check が必ず求める章
	}}
	for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
		blocks, err := report.Build(lang, rep, content)
		if err != nil {
			t.Fatalf("%v: %v", lang, err)
		}
		meta := metaOf(blocks)
		if !strings.Contains(meta, wantMinute) {
			t.Errorf("%v: PDF のデータ終端に %q がない: %s", lang, wantMinute, meta)
		}
		if strings.Contains(meta, "日本時間") || strings.Contains(meta, "JST") {
			t.Errorf("%v: 日本時間でないのに日本時間と注記している: %s", lang, meta)
		}
		if !strings.Contains(meta, "America/Los_Angeles") {
			t.Errorf("%v: 実際の時間帯の注記がない: %s", lang, meta)
		}
	}
}

// metaOf は PDF のブロックのうち、見出しの下の行（対象期間・データ終端など）をつないだもの。
func metaOf(blocks []report.Block) string {
	var out []string
	for _, b := range blocks {
		if b.Kind == report.Meta {
			out = append(out, b.Lines...)
		}
	}
	return strings.Join(out, "\n")
}

// mdText は ?format=md の本文を lang の利用者として取る。言語は ?lang= と Accept-Language の
// 両方で固定する（?lang= はサーバの優先順の 1 番。env.client が既定で Accept-Language: ja を
// 補うので、見出しだけに頼ると既定の言語を読んでいても気づけない）。
func mdText(t *testing.T, a *apiClient, lang string) string {
	t.Helper()
	code, _, body := a.do("GET", "/projects/tz/usage/report?from=2026-09-01&to=2026-09-30&format=md&lang="+lang,
		nil, "Accept-Language", lang)
	if code != 200 {
		t.Fatalf("md: %d", code)
	}
	return string(body)
}

// TestReportNotesNameTheTimezone は、サーバが描く注記が「実際に時刻を描いた時間帯」を名乗ることを確かめる。
// 既定（Asia/Tokyo）のときの文面は、時間帯を名乗るようにする前と一字も変わらない（日本語・英語とも）。
// 集計と Loc だけで決まる関数（reportText / reportTables / ledgerText / parseTimeArg）を直に呼ぶので DB は要らない。
func TestReportNotesNameTheTimezone(t *testing.T) {
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Skipf("tzdata がない: %v", err)
	}
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skipf("tzdata がない: %v", err)
	}
	at := time.Date(2026, 9, 18, 1, 0, 0, 0, time.UTC)
	rep := usageReportJSON{Project: "tz", DataEnd: at.Format(time.RFC3339Nano), Period: "（期間）"}
	rep.Conversations = []usage.ConversationGroup{{ConversationID: "c1", Client: "claude-code", FirstAt: at, LastAt: at}}
	items := []ledgerJSON{{ID: 1, Name: "2026-09", DataEnd: at.Format(time.RFC3339Nano), CreatedAt: at.Format(time.RFC3339Nano)}}

	for _, c := range []struct {
		name                                string
		loc                                 *time.Location
		lang                                i18n.Lang
		dataEnd, conv, xlsxSub, ledger, arg string
	}{
		{"既定・日本語", tokyo, i18n.JA, "（日本時間。data_end=", "（会話の時刻は日本時間）", " ／ 時刻は日本時間", "（日本時間）", "09:30（日本時間）または"},
		{"既定・英語", tokyo, i18n.EN, "(project local time; data_end=", "(conversation times are in project local time)",
			" / times are in project local time", "(project local time)", "09:30 (project local time) or"},
		{"Los_Angeles・日本語", la, i18n.JA, "（America/Los_Angeles。data_end=", "（会話の時刻はAmerica/Los_Angeles）",
			" ／ 時刻はAmerica/Los_Angeles", "（America/Los_Angeles）", "09:30（America/Los_Angeles）または"},
		{"Los_Angeles・英語", la, i18n.EN, "(America/Los_Angeles; data_end=", "(conversation times are in America/Los_Angeles)",
			" / times are in America/Los_Angeles", "(America/Los_Angeles)", "09:30 (America/Los_Angeles) or"},
	} {
		t.Run(c.name, func(t *testing.T) {
			md := reportText(c.lang, rep, c.loc)
			for _, want := range []string{c.dataEnd, c.conv} {
				if !strings.Contains(md, want) {
					t.Errorf("?format=md に %q がない:\n%s", want, md)
				}
			}
			var sub string
			for _, tb := range reportTables(c.lang, store.Project{Slug: "tz", Name: "tz"}, rep, c.loc) {
				if strings.Contains(tb.Sub, "時刻は") || strings.Contains(tb.Sub, "times are in") {
					sub = tb.Sub
				}
			}
			if !strings.Contains(sub, c.xlsxSub) {
				t.Errorf("xlsx の会話のシートの副題に %q がない: %q", c.xlsxSub, sub)
			}
			s := &Server{svc: &service.Service{Loc: c.loc}}
			if ledger := ledgerText(c.lang, items, s); !strings.Contains(ledger, c.ledger) {
				t.Errorf("台帳の注記に %q がない:\n%s", c.ledger, ledger)
			}
			_, err := parseTimeArg(c.lang, "from", "9 月", false, c.loc)
			if err == nil || !strings.Contains(err.Error(), c.arg) {
				t.Errorf("時刻の指定の誤りに %q がない: %v", c.arg, err)
			}
			if c.loc != la {
				return
			}
			// Asia/Tokyo でないときは、どの注記も日本時間と名乗らない。
			for _, text := range []string{md, sub, ledgerText(c.lang, items, s)} {
				for _, ng := range []string{"日本時間", "project local time", "JST"} {
					if strings.Contains(text, ng) {
						t.Errorf("America/Los_Angeles なのに %q と名乗っている: %s", ng, text)
					}
				}
			}
		})
	}
}
