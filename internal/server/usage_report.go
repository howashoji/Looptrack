package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/reporttable"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/usage"
	"github.com/howashoji/looptrack/internal/xlsxreport"
)

// トークンレポートの集計と台帳（設計は docs/server/DESIGN.md §5-4）。
// GET  /projects/{slug}/usage/report?from=&to=  または ?since_last=1  … 期間の集計（?format=md で表示用の文）
//      &group=label（案件ラベル別）/ type / stage / client … その切り口を groups に入れる（md はその表だけ）
// GET  /projects/{slug}/usage/report.xlsx（同じ引数）                  … 同じ集計の数表
// GET  /projects/{slug}/usage/ledger                                    … 台帳の一覧（新しいデータ終端の順）
// POST /projects/{slug}/usage/ledger                                    … 台帳に 1 行足す（editor 以上）
// MCP の usage_report / list_usage_ledger / add_usage_ledger も同じ関数を通る。

const (
	maxLedgerName     = 200
	maxLedgerNote     = 10000
	maxLedgerExcluded = 1000
)

func invalid(msg string) error {
	return &service.Error{Kind: service.Invalid, Code: "invalid_argument", Message: msg}
}

// parseTimeArg は期間の指定を読む。RFC 3339 はそのまま、「YYYY-MM-DD[ HH:MM[:SS]]」はプロジェクトの現地時刻
// （loc。既定は Asia/Tokyo）。日付だけのときは、endOfDay なら翌日 0:00（その日を含む）、でなければその日の 0:00。
func parseTimeArg(lang i18n.Lang, name, v string, endOfDay bool, loc *time.Location) (time.Time, error) {
	v = strings.TrimSpace(v)
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t.UTC(), nil
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02 15:04", "2006-01-02T15:04"} {
		if t, err := time.ParseInLocation(layout, v, loc); err == nil {
			return t.UTC(), nil
		}
	}
	if t, err := time.ParseInLocation("2006-01-02", v, loc); err == nil {
		if endOfDay {
			t = t.AddDate(0, 0, 1)
		}
		return t.UTC(), nil
	}
	return time.Time{}, invalid(i18n.T(lang, "server.api.err.time_arg", "name", name, "value", v, "tz", tzLabel(lang, loc)))
}

// reportArgs はレポートの期間の指定。RequestID は作成依頼の期間を使う（他と併用しない）。
// Group は切り口を 1 つ選ぶ（reportGroups のキー。空は全部）。
type reportArgs struct {
	From      string
	To        string
	SinceLast bool
	RequestID int64
	Group     string
}

// reportGroups は group の指定で選べる切り口。label は案件ラベル（by_case。1 区間を 1 案件に数え、合計は全体と一致する）で、
// イシューのラベルをそれぞれに数える by_label（合計は一致しない）とは別。case は label の別名。
var reportGroups = map[string]struct {
	of func(usage.Report) []usage.Group
}{
	"label":  {func(r usage.Report) []usage.Group { return r.ByCase }},
	"case":   {func(r usage.Report) []usage.Group { return r.ByCase }},
	"type":   {func(r usage.Report) []usage.Group { return r.ByType }},
	"stage":  {func(r usage.Report) []usage.Group { return r.ByStage }},
	"client": {func(r usage.Report) []usage.Group { return r.ByClient }},
}

// groupLabels は切り口の見出しと列名。ID は文字列リテラルで書く（表に入れて変数で渡すと、
// 訳の抜けを見つけるテストが拾えない）。
func groupLabels(lang i18n.Lang, group string) (title, head string) {
	switch group {
	case "type":
		return i18n.T(lang, "server.api.report.type_title"), i18n.T(lang, "server.api.report.type_head")
	case "stage":
		return i18n.T(lang, "server.api.report.stage_title"), i18n.T(lang, "server.api.report.stage_head")
	case "client":
		return i18n.T(lang, "server.api.report.client_title"), i18n.T(lang, "server.api.report.client_head")
	}
	return i18n.T(lang, "server.api.report.case_title"), i18n.T(lang, "server.api.report.case_head")
}

type ledgerJSON struct {
	ID                    int64    `json:"id"`
	Name                  string   `json:"name"`
	From                  *string  `json:"from"`
	To                    string   `json:"to"`
	DataEnd               string   `json:"data_end"`
	ExcludedConversations []string `json:"excluded_conversations"`
	TotalTokens           *int64   `json:"total_tokens"`
	Note                  string   `json:"note"`
	RequestID             *int64   `json:"request_id"`
	CreatedBy             string   `json:"created_by"`
	Via                   string   `json:"via"`
	CreatedAt             string   `json:"created_at"`
	RecordedAt            string   `json:"recorded_at"`
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func toLedgerJSON(r store.UsageReport) ledgerJSON {
	out := ledgerJSON{ID: r.ID, Name: r.Name, To: rfc3339(r.PeriodTo), DataEnd: rfc3339(r.DataEnd),
		ExcludedConversations: r.ExcludedConversations, TotalTokens: r.TotalTokens, Note: r.Note, CreatedBy: r.CreatedByLogin,
		Via: r.Via, CreatedAt: rfc3339(r.CreatedAt), RecordedAt: rfc3339(r.RecordedAt)}
	if r.PeriodFrom != nil {
		s := rfc3339(*r.PeriodFrom)
		out.From = &s
	}
	if r.RequestID != 0 {
		id := r.RequestID
		out.RequestID = &id
	}
	if out.ExcludedConversations == nil {
		out.ExcludedConversations = []string{}
	}
	return out
}

type usageReportJSON struct {
	Project    string            `json:"project"`
	From       *string           `json:"from"` // null は最初のデータから
	To         string            `json:"to"`
	DataEnd    string            `json:"data_end"` // 台帳に登録するデータ終端（to と今の早い方）
	Period     string            `json:"period"`   // 表示用（プロジェクトのローカル時刻）
	Timezone   string            `json:"timezone"` // 時刻の時間帯（IANA 名。出す側の PDF はこれで描く。無い古い集計 JSON は Asia/Tokyo とみなす）
	SinceLast  bool              `json:"since_last"`
	LastReport *ledgerJSON       `json:"last_report"` // since_last の起点にした台帳の行（台帳が空なら null）
	Request    *usageRequestJSON `json:"request"`     // request で集計したときの依頼（ledger add --from-report が request_id に使う）
	Generated  string            `json:"generated"`
	Group      string            `json:"group,omitempty"`  // group の指定（あれば）
	Groups     []usage.Group     `json:"groups,omitempty"` // group で選んだ切り口の行（label は by_case と同じ）
	usage.Report
}

// usageReport は期間を決めて集計する。
func (s *Server) usageReport(ctx context.Context, lang i18n.Lang, pr store.Project, a reportArgs) (usageReportJSON, error) {
	loc, now := s.svc.Loc, s.svc.Now().UTC()
	out := usageReportJSON{Project: pr.Slug, Generated: s.stamp(), Timezone: loc.String()}
	group := strings.TrimSpace(a.Group)
	if _, ok := reportGroups[group]; group != "" && !ok {
		return out, invalid(i18n.T(lang, "server.api.err.report_group", "value", group))
	}
	rules, err := domain.ParseRules(pr.Rules)
	if err != nil {
		return out, i18n.Wrapf(err, "service.err.rules_config", "slug", pr.Slug)
	}
	if a.RequestID != 0 {
		if a.RequestID < 0 || a.From != "" || a.To != "" || a.SinceLast {
			return out, invalid(i18n.T(lang, "server.api.err.report_request_conflict"))
		}
		req, err := store.UsageRequestByID(ctx, s.db, pr.ID, a.RequestID)
		if errors.Is(err, store.ErrNotFound) {
			return out, &service.Error{Kind: service.NotFound, Code: "not_found",
				Message: i18n.T(lang, "server.api.err.request_missing", "id", strconv.FormatInt(a.RequestID, 10))}
		}
		if err != nil {
			return out, err
		}
		rj := s.toRequestJSON(lang, req)
		out.Request = &rj
		a = reportArgs{SinceLast: req.SinceLast, Group: group}
		if req.PeriodFrom != nil {
			a.From = rfc3339(*req.PeriodFrom)
		}
		if req.PeriodTo != nil {
			a.To = rfc3339(*req.PeriodTo)
		}
	}
	out.SinceLast = a.SinceLast
	var from time.Time
	to := now
	if a.To != "" {
		if to, err = parseTimeArg(lang, "to", a.To, true, loc); err != nil {
			return out, err
		}
	}
	switch {
	case a.SinceLast && a.From != "":
		return out, invalid(i18n.T(lang, "server.api.err.report_since_last_and_from"))
	case a.SinceLast:
		last, err := store.LastUsageReport(ctx, s.db, pr.ID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return out, err
		}
		if err == nil {
			from = last.DataEnd
			lj := toLedgerJSON(last)
			out.LastReport = &lj
		}
	case a.From != "":
		if from, err = parseTimeArg(lang, "from", a.From, false, loc); err != nil {
			return out, err
		}
	default:
		return out, invalid(i18n.T(lang, "server.api.err.report_period_required"))
	}
	if a.SinceLast && !from.IsZero() && !from.Before(to) {
		to = from // 前回のレポートの直後は、空の期間（0 件）を返す
	} else if !from.IsZero() && !from.Before(to) {
		return out, invalid(i18n.T(lang, "server.api.err.report_period_empty", "from", from.In(loc).Format("2006-01-02 15:04"), "to", to.In(loc).Format("2006-01-02 15:04")))
	}
	dataEnd := to
	if dataEnd.After(now) && (from.IsZero() || now.After(from)) {
		dataEnd = now
	}

	snaps, err := store.UsageForPeriod(ctx, s.db, pr.ID, from, to)
	if err != nil {
		return out, err
	}
	set, _, err := s.svc.ProjectIssues(ctx, pr)
	if err != nil {
		return out, err
	}
	info := func(id string) (usage.IssueInfo, bool) {
		it, ok := set.Get(id)
		if !ok {
			return usage.IssueInfo{}, false
		}
		return usage.IssueInfo{ID: it.ID, Title: it.Title, Type: it.Type, Status: it.Status, Labels: it.Labels}, true
	}
	out.Report = usage.BuildReport(usage.Stages(snaps, domain.ClosedStatuses), from, to, info, rules.CasePattern())
	if group != "" {
		out.Group, out.Groups = group, reportGroups[group].of(out.Report)
	}
	if !from.IsZero() {
		f := rfc3339(from)
		out.From = &f
	}
	out.To, out.DataEnd = rfc3339(to), rfc3339(dataEnd)
	fromLabel := i18n.T(lang, "server.api.report.from_start")
	if !from.IsZero() {
		fromLabel = from.In(loc).Format("2006-01-02 15:04")
	}
	out.Period = i18n.T(lang, "server.api.report.period", "from", fromLabel, "to", to.In(loc).Format("2006-01-02 15:04"), "tz", tzLabel(lang, loc))
	return out, nil
}

func reportArgsOf(r *http.Request) reportArgs {
	q := r.URL.Query()
	a := reportArgs{From: q.Get("from"), To: q.Get("to"), SinceLast: queryBool(q.Get("since_last")), Group: q.Get("group")}
	if v := q.Get("request"); v != "" {
		if a.RequestID, _ = strconv.ParseInt(v, 10, 64); a.RequestID <= 0 {
			a.RequestID = -1 // 数でない指定は usageReport が 400 にする
		}
	}
	return a
}

func (s *Server) apiUsageReport(w http.ResponseWriter, r *http.Request) {
	pr, _, ok := s.projectFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	rep, err := s.usageReport(r.Context(), reqLang(r), pr, reportArgsOf(r))
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	if r.URL.Query().Get("format") == "md" {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Write([]byte(reportText(reqLang(r), rep, s.svc.Loc)))
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (s *Server) apiUsageReportXLSX(w http.ResponseWriter, r *http.Request) {
	pr, _, ok := s.projectFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	rep, err := s.usageReport(r.Context(), reqLang(r), pr, reportArgsOf(r))
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	b, err := xlsxreport.BuildTables(reportTables(reqLang(r), pr, rep, s.svc.Loc))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	day := strings.ReplaceAll(strings.Fields(rep.Generated)[0], "-", "")
	name := fmt.Sprintf("usage-%s-%s.xlsx", pr.Slug, day)
	pretty := i18n.T(reqLang(r), "server.api.report.xlsx_name", "project", pr.Slug, "day", day)
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q; filename*=UTF-8''%s", name, url.PathEscape(pretty)))
	w.Header().Set("Content-Length", fmt.Sprint(len(b)))
	w.Header().Set("X-Looptrack-Rows", fmt.Sprint(len(rep.ByIssue)))
	w.WriteHeader(http.StatusOK)
	w.Write(b)
}

// ledgerIn は台帳に足す 1 行（REST の本文と MCP の引数で共通）。
type ledgerIn struct {
	Name                  string   `json:"name" jsonschema:"server.mcp.arg.add_usage_ledger.name"`
	From                  string   `json:"from,omitempty" jsonschema:"server.mcp.arg.add_usage_ledger.from"`
	To                    string   `json:"to" jsonschema:"server.mcp.arg.add_usage_ledger.to"`
	DataEnd               string   `json:"data_end,omitempty" jsonschema:"server.mcp.arg.add_usage_ledger.data_end"`
	ExcludedConversations []string `json:"excluded_conversations,omitempty" jsonschema:"server.mcp.arg.add_usage_ledger.excluded_conversations"`
	TotalTokens           *int64   `json:"total_tokens,omitempty" jsonschema:"server.mcp.arg.add_usage_ledger.total_tokens"`
	Note                  string   `json:"note,omitempty" jsonschema:"server.mcp.arg.add_usage_ledger.note"`
	CreatedAt             string   `json:"created_at,omitempty" jsonschema:"server.mcp.arg.add_usage_ledger.created_at"`
	RequestID             int64    `json:"request_id,omitempty" jsonschema:"server.mcp.arg.add_usage_ledger.request_id"`
}

// addLedger は検証して台帳に 1 行足す。
func (s *Server) addLedger(ctx context.Context, lang i18n.Lang, a service.Actor, pr store.Project, role string, in ledgerIn) (ledgerJSON, error) {
	if !canWrite(role) {
		return ledgerJSON{}, forbidden(lang, pr.Slug, i18n.T(lang, "server.api.err.what_ledger_add"))
	}
	loc, now := s.svc.Loc, s.svc.Now().UTC()
	name := strings.TrimSpace(in.Name)
	if name == "" || len([]rune(name)) > maxLedgerName || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return ledgerJSON{}, invalid(i18n.T(lang, "server.api.err.ledger_name", "max", maxLedgerName))
	}
	if strings.TrimSpace(in.To) == "" {
		return ledgerJSON{}, invalid(i18n.T(lang, "server.api.err.ledger_to"))
	}
	rec := store.UsageReport{ProjectID: pr.ID, Name: name, Note: in.Note, RequestID: in.RequestID,
		CreatedBy: a.UserID, TokenID: a.TokenID, Via: a.Via, ExcludedConversations: []string{}, TotalTokens: in.TotalTokens}
	var err error
	if rec.PeriodTo, err = parseTimeArg(lang, "to", in.To, true, loc); err != nil {
		return ledgerJSON{}, err
	}
	if strings.TrimSpace(in.From) != "" {
		from, err := parseTimeArg(lang, "from", in.From, false, loc)
		if err != nil {
			return ledgerJSON{}, err
		}
		if !from.Before(rec.PeriodTo) {
			return ledgerJSON{}, invalid(i18n.T(lang, "server.api.err.from_before_to"))
		}
		rec.PeriodFrom = &from
	}
	rec.DataEnd = rec.PeriodTo
	if strings.TrimSpace(in.DataEnd) != "" {
		if rec.DataEnd, err = parseTimeArg(lang, "data_end", in.DataEnd, true, loc); err != nil {
			return ledgerJSON{}, err
		}
	}
	if rec.DataEnd.After(rec.PeriodTo) || (rec.PeriodFrom != nil && rec.DataEnd.Before(*rec.PeriodFrom)) {
		return ledgerJSON{}, invalid(i18n.T(lang, "server.api.err.ledger_data_end_range"))
	}
	if rec.DataEnd.After(now.Add(usageFutureSlack)) {
		return ledgerJSON{}, invalid(i18n.T(lang, "server.api.err.ledger_data_end_future"))
	}
	rec.CreatedAt = now
	if strings.TrimSpace(in.CreatedAt) != "" {
		if rec.CreatedAt, err = parseTimeArg(lang, "created_at", in.CreatedAt, false, loc); err != nil {
			return ledgerJSON{}, err
		}
		if rec.CreatedAt.After(now.Add(usageFutureSlack)) {
			return ledgerJSON{}, invalid(i18n.T(lang, "server.api.err.ledger_created_at_future"))
		}
	}
	if len(in.ExcludedConversations) > maxLedgerExcluded {
		return ledgerJSON{}, invalid(i18n.T(lang, "server.api.err.ledger_excluded_max", "max", maxLedgerExcluded))
	}
	for _, c := range in.ExcludedConversations {
		if c = strings.TrimSpace(c); c == "" || len(c) > 64 {
			return ledgerJSON{}, invalid(i18n.T(lang, "server.api.err.ledger_excluded_id"))
		}
		rec.ExcludedConversations = append(rec.ExcludedConversations, c)
	}
	if rec.TotalTokens != nil && (*rec.TotalTokens < 0 || *rec.TotalTokens > maxUsageCount) {
		return ledgerJSON{}, invalid(i18n.T(lang, "server.api.err.ledger_total_tokens"))
	}
	if len([]rune(in.Note)) > maxLedgerNote {
		return ledgerJSON{}, invalid(i18n.T(lang, "server.api.err.ledger_note_long", "max", maxLedgerNote))
	}
	if in.RequestID < 0 {
		return ledgerJSON{}, invalid(i18n.T(lang, "server.api.err.ledger_request_id"))
	}
	if in.RequestID > 0 {
		if err := s.requestForLedger(ctx, lang, pr, in.RequestID); err != nil {
			return ledgerJSON{}, err
		}
	}
	id, err := store.InsertUsageReport(ctx, s.db, rec)
	if errors.Is(err, store.ErrDuplicateRequest) { // 同じ依頼への登録が同時に走ったとき
		return ledgerJSON{}, requestDone(lang, in.RequestID, 0, "")
	}
	if errors.Is(err, store.ErrDuplicateName) {
		return ledgerJSON{}, &service.Error{Kind: service.Conflict, Code: "duplicate",
			Message: i18n.T(lang, "server.api.err.ledger_duplicate_name", "name", name)}
	}
	if err != nil {
		return ledgerJSON{}, err
	}
	saved, err := store.UsageReportByID(ctx, s.db, pr.ID, id)
	if err != nil {
		return ledgerJSON{}, err
	}
	return toLedgerJSON(saved), nil
}

func (s *Server) ledgerList(ctx context.Context, pr store.Project) (map[string]any, []ledgerJSON, error) {
	list, err := store.UsageReports(ctx, s.db, pr.ID)
	if err != nil {
		return nil, nil, err
	}
	items := []ledgerJSON{}
	for _, r := range list {
		items = append(items, toLedgerJSON(r))
	}
	var next any // 次の「前回以降」の起点
	if len(items) > 0 {
		next = items[0].DataEnd
	}
	// timezone は時刻の時間帯（IANA 名）。台帳を描く CLI がこれで時刻を描き、注記でも名乗る（集計 JSON と同じ）。
	return map[string]any{"project": pr.Slug, "items": items, "count": len(items), "next_from": next,
		"timezone": s.svc.Loc.String()}, items, nil
}

func (s *Server) apiUsageLedger(w http.ResponseWriter, r *http.Request) {
	pr, _, ok := s.projectFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	data, _, err := s.ledgerList(r.Context(), pr)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, data)
}

func (s *Server) apiAddUsageLedger(w http.ResponseWriter, r *http.Request) {
	pr, role, ok := s.projectFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	var in ledgerIn
	if !decodeJSON(w, r, &in) {
		return
	}
	entry, err := s.addLedger(r.Context(), reqLang(r), actor(r), pr, role, in)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"entry": entry,
		"message": i18n.T(reqLang(r), "server.api.report.ledger_added", "id", strconv.FormatInt(entry.ID, 10), "name", entry.Name, "from", s.local(entry.DataEnd))})
}

// local は RFC 3339 の時刻をプロジェクトの現地時刻（svc.Loc）の表示にする。
func (s *Server) local(v string) string { return loc3(v, s.svc.Loc) }

// ---- 表示用の文（MCP のツール結果と ?format=md）

func num(n int64) string {
	s := fmt.Sprint(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func orNone(lang i18n.Lang, s string) string {
	if s == "" {
		return i18n.T(lang, "server.api.report.none_value")
	}
	return s
}

func reportText(lang i18n.Lang, rep usageReportJSON, loc *time.Location) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n%s\n", i18n.T(lang, "server.api.report.title", "project", rep.Project),
		i18n.T(lang, "server.api.report.period_line", "period", rep.Period))
	if q := rep.Request; q != nil {
		b.WriteString(i18n.T(lang, "server.api.report.request_line", "id", strconv.FormatInt(q.ID, 10),
			"at", loc2(q.CreatedAt, loc), "by", orNone(lang, q.RequestedBy)))
		if q.Target != "" {
			b.WriteString(i18n.T(lang, "server.api.usage_request.row_target", "target", q.Target))
		}
		if q.Note != "" {
			b.WriteString(i18n.T(lang, "server.api.usage_request.row_note", "note", strings.Join(strings.Fields(q.Note), " ")))
		}
		if q.Done {
			b.WriteString(i18n.T(lang, "server.api.report.request_done", "report", strconv.FormatInt(q.Report.ID, 10), "name", q.Report.Name) + "\n")
		} else {
			b.WriteString("\n" + i18n.T(lang, "server.api.report.request_open", "id", strconv.FormatInt(q.ID, 10)) + "\n")
		}
	}
	if rep.SinceLast {
		if rep.LastReport != nil {
			b.WriteString(i18n.T(lang, "server.api.report.origin_last", "name", rep.LastReport.Name) + "\n")
		} else {
			b.WriteString(i18n.T(lang, "server.api.report.origin_empty") + "\n")
		}
	}
	if _, err := time.Parse(time.RFC3339Nano, rep.DataEnd); err == nil {
		b.WriteString(i18n.T(lang, "server.api.report.data_end", "at", loc3(rep.DataEnd, loc), "raw", rep.DataEnd, "tz", tzLabel(lang, loc)) + "\n\n")
	}
	b.WriteString(i18n.T(lang, "server.api.report.totals", "total", num(rep.TotalTokens), "stages", rep.Stages,
		"conversations", len(rep.Conversations), "unattributed", num(rep.Unattributed.TotalTokens),
		"unattributed_stages", rep.Unattributed.Stages, "excluded", num(rep.ExcludedTokens),
		"excluded_conversations", len(rep.ExcludedConversations), "inconsistent", rep.Inconsistent) + "\n")
	t := rep.Total
	b.WriteString(i18n.T(lang, "server.api.report.breakdown", "input", num(t.Main.Input+t.Sub.Input),
		"cache_write", num(t.Main.CacheCreate+t.Sub.CacheCreate), "cache_read", num(t.Main.CacheRead+t.Sub.CacheRead),
		"output", num(t.Main.Output+t.Sub.Output), "sub", num(t.Sub.Total()), "responses", num(t.Responses+t.SubResponses)) + "\n")

	none := i18n.T(lang, "server.api.summary.none")
	groups := func(title, keyHead string, list []usage.Group) {
		fmt.Fprintf(&b, "\n## %s\n\n", title)
		if len(list) == 0 {
			b.WriteString(none + "\n")
			return
		}
		fmt.Fprintf(&b, "| %s | %s |\n| -- | --: | --: | --: |\n", keyHead, i18n.T(lang, "server.api.report.group_cols"))
		for _, g := range list {
			fmt.Fprintf(&b, "| %s | %s | %d | %d |\n", mdCell(orNone(lang, g.Key)), num(g.TotalTokens), g.Stages, g.Issues)
		}
	}
	caseTable := func() {
		caseTitle, caseHead := groupLabels(lang, "case")
		groups(caseTitle, caseHead, rep.ByCase)
		if rep.CasePattern == "" {
			b.WriteString("\n" + i18n.T(lang, "server.api.report.case_unset") + "\n")
		} else {
			b.WriteString("\n" + i18n.T(lang, "server.api.report.case_pattern", "pattern", rep.CasePattern) + "\n")
		}
	}
	if rep.Group != "" { // 切り口を 1 つだけ出す
		if rep.Group == "label" || rep.Group == "case" {
			caseTable()
		} else {
			title, head := groupLabels(lang, rep.Group)
			groups(title, head, rep.Groups)
		}
		return b.String()
	}

	fmt.Fprintf(&b, "\n## %s\n\n", i18n.T(lang, "server.api.report.by_issue"))
	if len(rep.ByIssue) == 0 {
		b.WriteString(none + "\n")
	} else {
		b.WriteString(i18n.T(lang, "server.api.report.issue_cols") + "\n| -- | -- | -- | --: | --: | -- |\n")
		for _, it := range rep.ByIssue {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %d | %s |\n", it.ID, it.Type, it.Status, num(it.TotalTokens), it.Stages, mdCell(it.Title))
		}
	}
	caseTable()
	groups(i18n.T(lang, "server.api.report.label_title"), i18n.T(lang, "server.api.report.label_head"), rep.ByLabel)
	typeTitle, typeHead := groupLabels(lang, "type")
	groups(typeTitle, typeHead, rep.ByType)
	stageTitle, stageHead := groupLabels(lang, "stage")
	groups(stageTitle, stageHead, rep.ByStage)
	clientTitle, clientHead := groupLabels(lang, "client")
	groups(clientTitle, clientHead, rep.ByClient)

	fmt.Fprintf(&b, "\n## %s\n\n", i18n.T(lang, "server.api.report.by_conversation"))
	if len(rep.Conversations) == 0 {
		b.WriteString(none + "\n")
	} else {
		b.WriteString(i18n.T(lang, "server.api.report.conversation_cols") + "\n| -- | -- | -- | -- | --: | --: | --: | -- |\n")
		for _, c := range rep.Conversations {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %d | %s |\n", mdCell(c.ConversationID), c.Client,
				c.FirstAt.In(loc).Format("2006-01-02 15:04"), c.LastAt.In(loc).Format("2006-01-02 15:04"), num(c.TotalTokens), num(c.Unattributed),
				c.Stages, strings.Join(c.Issues, ", "))
		}
		b.WriteString("\n" + i18n.T(lang, "server.api.report.conversation_tz", "tz", tzLabel(lang, loc)) + "\n")
	}
	if len(rep.ExcludedConversations) > 0 {
		b.WriteString("\n" + i18n.T(lang, "server.api.report.excluded_conversations", "ids", strings.Join(rep.ExcludedConversations, ", ")) + "\n")
	}
	return b.String()
}

// defaultTZ は時間帯を名乗る前のサーバが時刻を描いていた時間帯（internal/client/report の DefaultTZ と同じ）。
const defaultTZ = "Asia/Tokyo"

// tzName は loc の IANA 名。名前で引き直せない（tzdata が無くて固定の時差に倒れた）ときは
// 既定の Asia/Tokyo とみなす（その固定の時差は日本時間そのもの）。
func tzName(loc *time.Location) string {
	if loc == nil {
		return defaultTZ
	}
	name := loc.String()
	if name == defaultTZ {
		return defaultTZ
	}
	if _, err := time.LoadLocation(name); err != nil {
		return defaultTZ
	}
	return name
}

// tzLabel は注記（対象期間・データ終端・会話の時刻・台帳・時刻の指定の誤り）に差し込む時間帯の呼び名。
// 既定の Asia/Tokyo のときだけ訳のある呼び名（日本時間 / project local time）を出し、ほかは IANA 名を
// そのまま出す（PDF を描く internal/client/report の tz.label と同じ流儀）。
// 既定のときの文面は、時間帯を名乗るようにする前と一字も変わらない。
func tzLabel(lang i18n.Lang, loc *time.Location) string {
	if name := tzName(loc); name != defaultTZ {
		return name
	}
	return i18n.T(lang, "server.api.report.tz_jst")
}

// loc2 は RFC 3339 の時刻を現地時刻の「YYYY-MM-DD HH:MM」にする。
func loc2(v string, loc *time.Location) string {
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return v
	}
	return t.In(loc).Format("2006-01-02 15:04")
}

// loc3 は RFC 3339 の時刻を現地時刻の「YYYY-MM-DD HH:MM:SS」にする。
// データ終端は ?format=md・xlsx・台帳のどれでもこの表記にする（経路で表記が変わらないように 1 か所に集める）。
func loc3(v string, loc *time.Location) string {
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return v
	}
	return t.In(loc).Format("2006-01-02 15:04:05")
}

func mdCell(s string) string {
	return strings.NewReplacer("|", "\\|", "\n", " ").Replace(s)
}

func ledgerText(lang i18n.Lang, items []ledgerJSON, s *Server) string {
	if len(items) == 0 {
		return i18n.T(lang, "server.api.report.ledger_empty")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-6s %-19s %-19s %-19s %12s %s\n", "#", i18n.T(lang, "server.api.report.col_from"),
		i18n.T(lang, "server.api.report.col_data_end"), i18n.T(lang, "server.api.report.col_created"),
		i18n.T(lang, "server.api.report.col_tokens"), i18n.T(lang, "server.api.report.col_name"))
	for _, it := range items {
		from := i18n.T(lang, "server.api.report.from_beginning")
		if it.From != nil {
			from = s.local(*it.From)
		}
		total := "-"
		if it.TotalTokens != nil {
			total = num(*it.TotalTokens)
		}
		fmt.Fprintf(&b, "%-6d %-19s %-19s %-19s %12s %s\n", it.ID, from, s.local(it.DataEnd), s.local(it.CreatedAt), total, it.Name)
	}
	b.WriteString("\n" + i18n.T(lang, "server.api.report.ledger_footer", "count", len(items), "from", s.local(items[0].DataEnd),
		"tz", tzLabel(lang, s.svc.Loc)))
	return b.String()
}

// reportTables は集計を xlsx のシートにする。
func reportTables(lang i18n.Lang, pr store.Project, rep usageReportJSON, loc *time.Location) []reporttable.Table {
	sub := i18n.T(lang, "server.api.report.xlsx_sub", "period", rep.Period, "data_end", loc3(rep.DataEnd, loc), "generated", rep.Generated)
	tokCols := []reporttable.Column{{Head: i18n.T(lang, "server.api.report.col_input"), Number: true},
		{Head: i18n.T(lang, "server.api.report.col_cache_write"), Number: true}, {Head: i18n.T(lang, "server.api.report.col_cache_read"), Number: true},
		{Head: i18n.T(lang, "server.api.report.col_output"), Number: true}, {Head: i18n.T(lang, "server.api.report.col_total"), Number: true},
		{Head: i18n.T(lang, "server.api.report.col_stages"), Number: true, Width: 8}}
	tok := func(c usage.Counters, stages int) []any {
		return []any{c.Main.Input + c.Sub.Input, c.Main.CacheCreate + c.Sub.CacheCreate, c.Main.CacheRead + c.Sub.CacheRead,
			c.Main.Output + c.Sub.Output, c.Total(), stages}
	}
	title := func(s string) string {
		return i18n.T(lang, "server.api.report.xlsx_title", "project", pr.Name, "sheet", s)
	}

	summarySheet := i18n.T(lang, "server.api.report.sheet_summary")
	summary := reporttable.Table{Sheet: summarySheet, Title: title(summarySheet), Sub: sub,
		Columns: append([]reporttable.Column{{Head: i18n.T(lang, "server.api.report.col_item"), Width: 22}}, tokCols...)}
	summary.Rows = [][]any{
		append([]any{i18n.T(lang, "server.api.report.row_total")}, tok(rep.Total, rep.Stages)...),
		append([]any{i18n.T(lang, "server.api.report.row_unattributed")}, tok(rep.Unattributed.Total, rep.Unattributed.Stages)...),
		append([]any{i18n.T(lang, "server.api.report.row_excluded")}, tok(rep.Excluded, rep.ExcludedStages)...),
	}

	issueSheet := i18n.T(lang, "server.api.report.by_issue")
	issues := reporttable.Table{Sheet: issueSheet, Title: title(issueSheet), Sub: sub,
		Columns: append([]reporttable.Column{{Head: "ID", Width: 12}, {Head: i18n.T(lang, "server.api.report.type_head"), Width: 12},
			{Head: i18n.T(lang, "server.api.report.col_status"), Width: 12},
			{Head: i18n.T(lang, "server.api.report.label_head"), Width: 20},
			{Head: i18n.T(lang, "server.api.usage.col_title"), Width: 50}}, tokCols...)}
	for _, it := range rep.ByIssue {
		issues.Rows = append(issues.Rows, append([]any{it.ID, it.Type, it.Status, strings.Join(it.Labels, ", "), it.Title}, tok(it.Total, it.Stages)...))
	}
	issueCol := i18n.T(lang, "server.api.report.col_issues")
	group := func(sheet, head string, list []usage.Group) reporttable.Table {
		t := reporttable.Table{Sheet: sheet, Title: title(sheet), Sub: sub,
			Columns: append(append([]reporttable.Column{{Head: head, Width: 20}}, tokCols...), reporttable.Column{Head: issueCol, Number: true, Width: 10})}
		for _, g := range list {
			t.Rows = append(t.Rows, append(append([]any{orNone(lang, g.Key)}, tok(g.Total, g.Stages)...), g.Issues))
		}
		return t
	}
	convSheet := i18n.T(lang, "server.api.report.by_conversation")
	convs := reporttable.Table{Sheet: convSheet, Title: title(convSheet), Sub: sub + i18n.T(lang, "server.api.report.xlsx_sub_tz", "tz", tzLabel(lang, loc)),
		Columns: append([]reporttable.Column{{Head: i18n.T(lang, "server.api.report.col_conversation"), Width: 26}, {Head: "AI", Width: 12},
			{Head: i18n.T(lang, "server.api.report.col_first"), Width: 17}, {Head: i18n.T(lang, "server.api.report.col_last"), Width: 17}},
			append(tokCols, reporttable.Column{Head: i18n.T(lang, "server.api.report.col_unattributed"), Number: true},
				reporttable.Column{Head: issueCol, Width: 30})...)}
	for _, c := range rep.Conversations {
		row := append([]any{c.ConversationID, c.Client, c.FirstAt.In(loc).Format("2006-01-02 15:04"), c.LastAt.In(loc).Format("2006-01-02 15:04")},
			tok(c.Total, c.Stages)...)
		convs.Rows = append(convs.Rows, append(row, c.Unattributed, strings.Join(c.Issues, ", ")))
	}
	caseHead := i18n.T(lang, "server.api.report.case_head")
	cases := group(i18n.T(lang, "server.api.report.sheet_case"), caseHead, rep.ByCase)
	if rep.CasePattern == "" {
		cases.Sub += i18n.T(lang, "server.api.report.xlsx_case_unset")
	} else {
		cases.Sub += i18n.T(lang, "server.api.report.xlsx_case_pattern", "pattern", rep.CasePattern)
	}
	labelHead := i18n.T(lang, "server.api.report.label_head")
	typeHead := i18n.T(lang, "server.api.report.type_head")
	stageHead := i18n.T(lang, "server.api.report.stage_head")
	clientHead := i18n.T(lang, "server.api.report.client_head")
	return []reporttable.Table{summary, issues, cases,
		group(i18n.T(lang, "server.api.report.sheet_label"), labelHead, rep.ByLabel),
		group(i18n.T(lang, "server.api.report.sheet_type"), typeHead, rep.ByType),
		group(i18n.T(lang, "server.api.report.sheet_stage"), stageHead, rep.ByStage),
		group(i18n.T(lang, "server.api.report.sheet_client"), clientHead, rep.ByClient), convs}
}

// ---- MCP（usage_report / list_usage_ledger / add_usage_ledger）

type (
	usageReportIn struct {
		projectArg
		From      string `json:"from,omitempty" jsonschema:"server.mcp.arg.usage_report.from"`
		To        string `json:"to,omitempty" jsonschema:"server.mcp.arg.usage_report.to"`
		SinceLast bool   `json:"since_last,omitempty" jsonschema:"server.mcp.arg.usage_report.since_last"`
		RequestID int64  `json:"request_id,omitempty" jsonschema:"server.mcp.arg.usage_report.request_id"`
		Group     string `json:"group,omitempty" jsonschema:"server.mcp.arg.usage_report.group"`
		Format    string `json:"format,omitempty" jsonschema:"server.mcp.arg.usage_report.format"`
	}
	addLedgerIn struct {
		projectArg
		ledgerIn
	}
)

func (s *Server) addUsageMCPTools(srv *mcp.Server, lang i18n.Lang, ro *mcp.ToolAnnotations, notDestructive bool) {
	addTool(srv, lang, &mcp.Tool{Name: "usage_report", Annotations: ro,
		Description: i18n.T(lang, "server.mcp.tool.usage_report")},
		func(ctx context.Context, req *mcp.CallToolRequest, in usageReportIn) (*mcp.CallToolResult, any, error) {
			pr, _, err := s.mcpProject(ctx, req, in.Project, "usage_report")
			if err != nil {
				return nil, nil, err
			}
			lang := mcpLang(req)
			rep, err := s.usageReport(ctx, lang, pr, reportArgs{From: in.From, To: in.To, SinceLast: in.SinceLast, RequestID: in.RequestID, Group: in.Group})
			if err != nil {
				return nil, nil, s.toolError(lang, "usage_report", err)
			}
			if in.Format == "json" {
				b, _ := json.MarshalIndent(rep, "", "  ")
				return result(string(b), rep), nil, nil
			}
			return result(reportText(lang, rep, s.svc.Loc), rep), nil, nil
		})

	addTool(srv, lang, &mcp.Tool{Name: "list_usage_ledger", Annotations: ro,
		Description: i18n.T(lang, "server.mcp.tool.list_usage_ledger")},
		func(ctx context.Context, req *mcp.CallToolRequest, in projectArg) (*mcp.CallToolResult, any, error) {
			pr, _, err := s.mcpProject(ctx, req, in.Project, "list_usage_ledger")
			if err != nil {
				return nil, nil, err
			}
			data, items, err := s.ledgerList(ctx, pr)
			if err != nil {
				return nil, nil, s.toolError(mcpLang(req), "list_usage_ledger", err)
			}
			return result(ledgerText(mcpLang(req), items, s), data), nil, nil
		})

	addTool(srv, lang, &mcp.Tool{Name: "add_usage_ledger", Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive},
		Description: i18n.T(lang, "server.mcp.tool.add_usage_ledger")},
		func(ctx context.Context, req *mcp.CallToolRequest, in addLedgerIn) (*mcp.CallToolResult, any, error) {
			c, err := mcpCallOf(req)
			if err != nil {
				return nil, nil, err
			}
			pr, role, err := s.mcpProject(ctx, req, in.Project, "add_usage_ledger")
			if err != nil {
				return nil, nil, err
			}
			entry, err := s.addLedger(ctx, c.lang, c.actor, pr, role, in.ledgerIn)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "add_usage_ledger", err)
			}
			text := i18n.T(c.lang, "server.api.report.ledger_added", "id", strconv.FormatInt(entry.ID, 10), "name", entry.Name, "from", s.local(entry.DataEnd))
			return result(text, map[string]any{"entry": entry}), nil, nil
		})
}

// mcpProject は引数・ヘッダからプロジェクトを決め、利用者の権限と一緒に返す。
func (s *Server) mcpProject(ctx context.Context, req *mcp.CallToolRequest, arg, tool string) (store.Project, string, error) {
	c, err := mcpCallOf(req)
	if err != nil {
		return store.Project{}, "", err
	}
	slug, err := s.projectSlug(ctx, c, arg)
	if err != nil {
		return store.Project{}, "", s.toolError(c.lang, tool, err)
	}
	pr, role, err := s.resolveProject(ctx, c.lang, c.p.User, slug)
	if err != nil {
		return store.Project{}, "", s.toolError(c.lang, tool, err)
	}
	return pr, role, nil
}
