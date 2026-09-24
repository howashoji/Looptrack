package server

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/xlsxreport"
)

// 課題管理表（xlsx）の書き出し。
// GET  …/issues.xlsx  絞り込みをサーバで適用する（CLI の looptrack issue export が使う）
// POST …/issues.xlsx  画面が今出している ID と絞り込みの説明を渡す（ボードのボタン）

const maxExportFilterLen = 200

// apiExportIssues は絞り込み（status / type / label / ref / all・並び順）を適用して書き出す。
func (s *Server) apiExportIssues(w http.ResponseWriter, r *http.Request) {
	pr, _, ok := s.projectFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	key, reverse, ok := sortArgs(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := domain.Filter{Status: q.Get("status"), Type: q.Get("type"), Label: q.Get("label"), Ref: q.Get("ref"), All: queryBool(q.Get("all"))}
	if f.Status != "" {
		if err := domain.ValidateValue("--status", f.Status, domain.Statuses); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_argument", i18n.Text(reqLang(r), err))
			return
		}
	}
	set, rows, err := s.svc.ProjectIssues(r.Context(), pr)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items := filterAssignee(itemsJSON(pr, rows, set.List(f, key, reverse)), q.Get("assignee"), principalFrom(r.Context()).User.Login)
	label := filterLabel(reqLang(r), f)
	if a := strings.TrimSpace(q.Get("assignee")); a != "" {
		label = i18n.T(reqLang(r), "server.api.export.filter_assignee", "assignee", a) + " / " + label
	}
	s.writeXLSX(w, r, pr, items, label)
}

// apiExportSelected は画面が並べている順・件数のまま書き出す。
func (s *Server) apiExportSelected(w http.ResponseWriter, r *http.Request) {
	pr, _, ok := s.projectFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	ids := splitIDs(r.PostFormValue("ids"))
	if len(ids) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_argument", i18n.T(reqLang(r), "server.api.err.export_empty"))
		return
	}
	set, rows, err := s.svc.ProjectIssues(r.Context(), pr)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	all := itemsJSON(pr, rows, set.List(domain.Filter{All: true}, "id", false))
	byID := make(map[string]issueJSON, len(all))
	for _, it := range all {
		byID[it.ID] = it
	}
	items := make([]issueJSON, 0, len(ids))
	for _, id := range ids {
		if it, ok := byID[strings.ToUpper(id)]; ok {
			items = append(items, it)
		}
	}
	if len(items) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_argument", i18n.T(reqLang(r), "server.api.err.export_no_match"))
		return
	}
	s.writeXLSX(w, r, pr, items, cleanFilterText(r.PostFormValue("filter")))
}

func (s *Server) writeXLSX(w http.ResponseWriter, r *http.Request, pr store.Project, items []issueJSON, filter string) {
	generated := s.stamp()
	rows := make([]xlsxreport.Row, 0, len(items))
	for _, it := range items {
		assignee := it.Assignee
		if it.AssigneeInactive {
			assignee += i18n.T(reqLang(r), "server.api.export.no_access")
		}
		rows = append(rows, xlsxreport.Row{ID: it.ID, Type: it.Type, Status: it.Status, Priority: it.Priority, Assignee: assignee,
			Title: it.Title, Labels: it.Labels, Parent: it.Parent, BlockedBy: it.BlockedBy, Traces: it.Traces,
			Created: it.Created, Updated: it.Updated})
	}
	b, err := xlsxreport.Build(xlsxreport.Report{Project: pr.Name, Prefix: pr.Prefix, Generated: generated,
		Filter: filter, Lang: reqLang(r), Rows: rows})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	name := xlsxreport.FileName(pr.Slug, generated)
	day := strings.ReplaceAll(strings.Fields(generated)[0], "-", "")
	pretty := i18n.T(reqLang(r), "server.api.export.xlsx_name", "project", pr.Slug, "day", day)
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	// filename は ASCII、filename* に日本語（RFC 6266）。古い実装は前者を使う
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q; filename*=UTF-8''%s", name, url.PathEscape(pretty)))
	w.Header().Set("Content-Length", fmt.Sprint(len(b)))
	w.Header().Set("X-Looptrack-Rows", fmt.Sprint(len(rows))) // CLI が「N 件」と出すため
	w.WriteHeader(http.StatusOK)
	w.Write(b)
}

// filterLabel は GET の絞り込みを表の先頭に出す文にする。
func filterLabel(lang i18n.Lang, f domain.Filter) string {
	var parts []string
	for _, p := range []struct{ label, value string }{
		{i18n.T(lang, "server.api.report.col_status"), f.Status}, {i18n.T(lang, "server.api.report.type_head"), f.Type},
		{i18n.T(lang, "server.api.report.label_head"), f.Label}, {i18n.T(lang, "server.api.export.filter_ref"), f.Ref},
	} {
		if p.value != "" {
			parts = append(parts, p.label+" "+p.value)
		}
	}
	if f.All {
		parts = append(parts, i18n.T(lang, "server.api.export.filter_all"))
	} else {
		parts = append(parts, i18n.T(lang, "server.api.export.filter_open"))
	}
	return strings.Join(parts, " / ")
}

func splitIDs(s string) []string {
	out := []string{}
	for _, id := range strings.Split(s, ",") {
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// cleanFilterText は画面から来る説明文を 1 行に収める（表のセルに入れるため）。
func cleanFilterText(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if runes := []rune(s); len(runes) > maxExportFilterLen {
		s = string(runes[:maxExportFilterLen]) + "…"
	}
	return s
}
