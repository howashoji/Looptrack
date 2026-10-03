package server

import (
	"context"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// MCP の一覧の件数・プロジェクト一覧の bug 数・サマリの bug 数は、件数が 1 のとき英語で単数になる
// （日本語は件数に関わらず同じ文面の型）。件数が 2 のとき複数になる対照を同じテストに置く。
func TestMCPCountTextsSingularAndPlural(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p := e.project("cnt")
	u := e.user("cnt-ai", "cnt-password-1234", "member")
	if err := store.SetMember(ctx, e.db, p.ID, u.ID, "editor"); err != nil {
		t.Fatal(err)
	}
	token := e.apiAs(u).token
	hdr := func(lang string) map[string]string {
		return map[string]string{"X-Looptrack-Project": "cnt", "Accept-Language": lang}
	}
	en, ja := e.mcpAs(token, hdr("en")), e.mcpAs(token, hdr("ja"))

	text, _ := en.call("create_issue", map[string]any{"title": "first bug", "type": "bug"}, false)
	if !strings.Contains(text, "CNT-0001") {
		t.Fatalf("前提が崩れている（1 件目の起票）: %q", text)
	}
	for _, c := range []struct {
		client *mcpClient
		tool   string
		args   map[string]any
		want   string
	}{
		{en, "list_issues", nil, "\n1 issue"},
		{ja, "list_issues", nil, "\n1 件"},
		{en, "list_projects", nil, "1 open bug"},
		{en, "project_summary", nil, "(1 bug)"},
		{ja, "project_summary", nil, "（bug 1 件）"},
	} {
		got, _ := c.client.call(c.tool, c.args, false)
		if !strings.Contains(got, c.want) {
			t.Errorf("件数 1 の %s: %q に %q が無い", c.tool, got, c.want)
		}
		if strings.Contains(got, "1 issues") || strings.Contains(got, "1 open bugs") || strings.Contains(got, "(1 bugs)") {
			t.Errorf("件数 1 の %s が複数形で出ている: %q", c.tool, got)
		}
	}

	en.call("create_issue", map[string]any{"title": "second bug", "type": "bug"}, false)
	for _, c := range []struct {
		client *mcpClient
		tool   string
		want   string
	}{
		{en, "list_issues", "\n2 issues"},
		{ja, "list_issues", "\n2 件"},
		{en, "list_projects", "2 open bugs"},
		{en, "project_summary", "(2 bugs)"},
		{ja, "project_summary", "（bug 2 件）"},
	} {
		got, _ := c.client.call(c.tool, nil, false)
		if !strings.Contains(got, c.want) {
			t.Errorf("件数 2 の %s: %q に %q が無い", c.tool, got, c.want)
		}
	}
}

// 付与の状況の文は、日数・AI 操作の数・対象外の数をそれぞれ独立に単数と複数にする。
// 日本語は件数に関わらず、これまでと同じ文面の型（対照）。
func TestUsageCoverageTextSingularAndPlural(t *testing.T) {
	rate := 1.0
	for _, c := range []struct {
		lang         i18n.Lang
		days, target int
		humans       int
		want         string
	}{
		{i18n.EN, 1, 1, 1, "(last 1 day: 1 of 1 AI operation by you carry usage, 0 do not, 1 human operation excluded)"},
		{i18n.EN, 7, 9, 2, "(last 7 days: 1 of 9 AI operations by you carry usage, 0 do not, 2 human operations excluded)"},
		{i18n.EN, 1, 9, 2, "(last 1 day: 1 of 9 AI operations by you carry usage, 0 do not, 2 human operations excluded)"},
		{i18n.JA, 1, 1, 1, "（直近 1 日・自分の AI 操作 1 件中 1 件に付与・未付与 0 件・対象外（人の操作）1 件）"},
		{i18n.JA, 7, 9, 2, "（直近 7 日・自分の AI 操作 9 件中 1 件に付与・未付与 0 件・対象外（人の操作）2 件）"},
	} {
		got := usageCoverageText(c.lang, usageCoverageJSON{Days: c.days, Mine: true, Target: c.target, Attached: 1, Rate: &rate, Humans: c.humans})
		if !strings.Contains(got, c.want) {
			t.Errorf("%s 日数 %d・AI 操作 %d・人の操作 %d: %q に %q が無い", c.lang, c.days, c.target, c.humans, got, c.want)
		}
	}
}

// 未付与の要約は、未付与の数と日数をそれぞれ独立に単数と複数にする。
func TestUsageMissingTextSingularAndPlural(t *testing.T) {
	for _, c := range []struct {
		lang          i18n.Lang
		missing, days int
		want          string
	}{
		{i18n.EN, 1, 1, "1 operation without token usage (last 1 day, "},
		{i18n.EN, 3, 7, "3 operations without token usage (last 7 days, "},
		{i18n.EN, 1, 7, "1 operation without token usage (last 7 days, "},
		{i18n.JA, 1, 1, "トークン情報の未付与 1 件（直近 1 日・"},
		{i18n.JA, 3, 7, "トークン情報の未付与 3 件（直近 7 日・"},
	} {
		got := usageMissingText(c.lang, usageCoverageJSON{Missing: c.missing, Days: c.days, Issues: []string{"CNT-0001"}})
		if !strings.Contains(got, c.want) {
			t.Errorf("%s 未付与 %d・日数 %d: %q に %q が無い", c.lang, c.missing, c.days, got, c.want)
		}
	}
}
