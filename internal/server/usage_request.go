package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

// トークンレポートの作成依頼（設計は docs/server/DESIGN.md §5-4）。
// 画面の「レポート作成」で依頼を登録し、次のセッション開始時の summary に未完了の依頼を出して AI に拾わせる。
// 完了は依頼の行を書き換えず、台帳（usage_reports）に request_id つきの行があることで判定する。
// GET  /projects/{slug}/usage/requests[?all=1] … 依頼の一覧（既定は未完了だけ）
// POST /projects/{slug}/usage/requests          … 依頼を登録（editor 以上）
// 画面: /im/p/{slug}/report-requests（フォームと一覧。POST は CSRF）
// MCP: list_usage_requests

const (
	maxRequestTarget = 500
	maxRequestNote   = 2000
	maxOpenRequests  = 20 // 未完了の依頼の上限（押し間違いの連打で summary が埋まらないように）
)

// requestIn は依頼の入力（REST の本文・画面のフォームで共通）。
type requestIn struct {
	SinceLast bool   `json:"since_last,omitempty"`
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
	Target    string `json:"target,omitempty"`
	Note      string `json:"note,omitempty"`
}

type requestReportJSON struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

type usageRequestJSON struct {
	ID          int64              `json:"id"`
	SinceLast   bool               `json:"since_last"`
	From        *string            `json:"from"` // since_last のときは null
	To          *string            `json:"to"`   // null は作成するときの今まで
	Period      string             `json:"period"`
	Target      string             `json:"target"`
	Note        string             `json:"note"`
	RequestedBy string             `json:"requested_by"`
	Via         string             `json:"via"`
	CreatedAt   string             `json:"created_at"`
	Done        bool               `json:"done"`
	Report      *requestReportJSON `json:"report"`  // 完了した台帳の行
	Command     string             `json:"command"` // 次に打つコマンド（未完了のとき）
}

// requestCommand は依頼に応えるための指示（summary・一覧・画面に出す）。
func requestCommand(lang i18n.Lang, id int64) string {
	return i18n.T(lang, "server.api.usage_request.command", "id", strconv.FormatInt(id, 10))
}

func (s *Server) requestPeriod(lang i18n.Lang, r store.UsageRequest) string {
	f := func(t time.Time) string { return t.In(s.svc.Loc).Format("2006-01-02 15:04") }
	to := i18n.T(lang, "server.api.usage_request.period_to_now")
	if r.PeriodTo != nil {
		to = i18n.T(lang, "server.api.usage_request.period_to", "to", f(*r.PeriodTo))
	}
	if r.SinceLast {
		return i18n.T(lang, "server.api.usage_request.period_since_last", "to", to)
	}
	return i18n.T(lang, "server.api.usage_request.period_range", "from", f(*r.PeriodFrom), "to", to)
}

func (s *Server) toRequestJSON(lang i18n.Lang, r store.UsageRequest) usageRequestJSON {
	out := usageRequestJSON{ID: r.ID, SinceLast: r.SinceLast, Period: s.requestPeriod(lang, r), Target: r.Target, Note: r.Note,
		RequestedBy: r.RequestedByLogin, Via: r.Via, CreatedAt: rfc3339(r.CreatedAt), Done: r.Done()}
	if r.PeriodFrom != nil {
		v := rfc3339(*r.PeriodFrom)
		out.From = &v
	}
	if r.PeriodTo != nil {
		v := rfc3339(*r.PeriodTo)
		out.To = &v
	}
	if r.Done() {
		out.Report = &requestReportJSON{ID: r.ReportID, Name: r.ReportName, CreatedAt: rfc3339(r.ReportCreatedAt)}
	} else {
		out.Command = requestCommand(lang, r.ID)
	}
	return out
}

func oneLine(s string) bool { return strings.IndexFunc(s, unicode.IsControl) < 0 }

// addRequest は検証して依頼を 1 行足す。
func (s *Server) addRequest(ctx context.Context, lang i18n.Lang, a service.Actor, pr store.Project, role string, in requestIn) (usageRequestJSON, error) {
	if !canWrite(role) {
		return usageRequestJSON{}, forbidden(lang, pr.Slug, i18n.T(lang, "server.api.err.what_report_request"))
	}
	loc, now := s.svc.Loc, s.svc.Now().UTC()
	rec := store.UsageRequest{ProjectID: pr.ID, SinceLast: in.SinceLast, Target: strings.TrimSpace(in.Target),
		Note: strings.TrimSpace(in.Note), RequestedBy: a.UserID, TokenID: a.TokenID, Via: a.Via}
	from, to := strings.TrimSpace(in.From), strings.TrimSpace(in.To)
	switch {
	case in.SinceLast && from != "":
		return usageRequestJSON{}, invalid(i18n.T(lang, "server.api.err.request_since_last_and_from"))
	case !in.SinceLast && from == "":
		return usageRequestJSON{}, invalid(i18n.T(lang, "server.api.err.request_period_required"))
	}
	if from != "" {
		t, err := parseTimeArg(lang, "from", from, false, loc)
		if err != nil {
			return usageRequestJSON{}, err
		}
		if t.After(now) {
			return usageRequestJSON{}, invalid(i18n.T(lang, "server.api.err.request_from_future"))
		}
		rec.PeriodFrom = &t
	}
	if to != "" {
		t, err := parseTimeArg(lang, "to", to, true, loc)
		if err != nil {
			return usageRequestJSON{}, err
		}
		if rec.PeriodFrom != nil && !rec.PeriodFrom.Before(t) {
			return usageRequestJSON{}, invalid(i18n.T(lang, "server.api.err.from_before_to"))
		}
		rec.PeriodTo = &t
	}
	if len([]rune(rec.Target)) > maxRequestTarget || !oneLine(rec.Target) {
		return usageRequestJSON{}, invalid(i18n.T(lang, "server.api.err.request_target_long", "max", maxRequestTarget))
	}
	if len([]rune(rec.Note)) > maxRequestNote {
		return usageRequestJSON{}, invalid(i18n.T(lang, "server.api.err.request_note_long", "max", maxRequestNote))
	}
	open, err := store.UsageRequests(ctx, s.db, pr.ID, true)
	if err != nil {
		return usageRequestJSON{}, err
	}
	if len(open) >= maxOpenRequests {
		return usageRequestJSON{}, &service.Error{Kind: service.Conflict, Code: "too_many_requests",
			Message: i18n.T(lang, "server.api.err.request_too_many", "count", len(open))}
	}
	id, err := store.InsertUsageRequest(ctx, s.db, rec, now)
	if err != nil {
		return usageRequestJSON{}, err
	}
	saved, err := store.UsageRequestByID(ctx, s.db, pr.ID, id)
	if err != nil {
		return usageRequestJSON{}, err
	}
	return s.toRequestJSON(lang, saved), nil
}

// requestList は依頼の一覧（新しい順）。all でなければ未完了だけ。
func (s *Server) requestList(ctx context.Context, lang i18n.Lang, pr store.Project, all bool) ([]usageRequestJSON, error) {
	list, err := store.UsageRequests(ctx, s.db, pr.ID, !all)
	if err != nil {
		return nil, err
	}
	out := []usageRequestJSON{}
	for _, r := range list {
		out = append(out, s.toRequestJSON(lang, r))
	}
	return out, nil
}

// requestForLedger は台帳に付ける依頼を確かめる（同じプロジェクトにあり、未完了であること）。
func (s *Server) requestForLedger(ctx context.Context, lang i18n.Lang, pr store.Project, id int64) error {
	req, err := store.UsageRequestByID(ctx, s.db, pr.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		return invalid(i18n.T(lang, "server.api.err.request_missing", "id", strconv.FormatInt(id, 10)))
	}
	if err != nil {
		return err
	}
	if req.Done() {
		return requestDone(lang, req.ID, req.ReportID, req.ReportName)
	}
	return nil
}

func requestDone(lang i18n.Lang, id, reportID int64, name string) error {
	msg := i18n.T(lang, "server.api.err.request_done", "id", strconv.FormatInt(id, 10))
	if reportID != 0 {
		msg = i18n.T(lang, "server.api.err.request_done_report", "id", strconv.FormatInt(id, 10),
			"report", strconv.FormatInt(reportID, 10), "name", name)
	}
	return &service.Error{Kind: service.Conflict, Code: "request_done", Message: msg}
}

// requestText は依頼の一覧を人が読む形にする（MCP の list_usage_requests・project_summary）。
func (s *Server) requestText(lang i18n.Lang, items []usageRequestJSON) string {
	var b strings.Builder
	for _, it := range items {
		b.WriteString(i18n.T(lang, "server.api.usage_request.row", "id", strconv.FormatInt(it.ID, 10),
			"at", loc2(it.CreatedAt, s.svc.Loc), "by", orNone(lang, it.RequestedBy), "period", it.Period))
		if it.Target != "" {
			b.WriteString(i18n.T(lang, "server.api.usage_request.row_target", "target", it.Target))
		}
		if it.Note != "" {
			b.WriteString(i18n.T(lang, "server.api.usage_request.row_note", "note", strings.Join(strings.Fields(it.Note), " ")))
		}
		if it.Done {
			b.WriteString(i18n.T(lang, "server.api.usage_request.row_done", "report", strconv.FormatInt(it.Report.ID, 10), "name", it.Report.Name))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (s *Server) apiUsageRequests(w http.ResponseWriter, r *http.Request) {
	pr, _, ok := s.projectFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	items, err := s.requestList(r.Context(), reqLang(r), pr, queryBool(r.URL.Query().Get("all")))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": pr.Slug, "items": items, "count": len(items),
		"timezone": s.svc.Loc.String()})
}

func (s *Server) apiAddUsageRequest(w http.ResponseWriter, r *http.Request) {
	pr, role, ok := s.projectFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	var in requestIn
	if !decodeJSON(w, r, &in) {
		return
	}
	req, err := s.addRequest(r.Context(), reqLang(r), actor(r), pr, role, in)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"request": req,
		"message": i18n.T(reqLang(r), "server.api.usage_request.added", "id", strconv.FormatInt(req.ID, 10))})
}

// ---- 画面（/im/p/{slug}/report-requests）

func (s *Server) reportRequestsPage(w http.ResponseWriter, r *http.Request, p *principal) {
	notice := ""
	if id, err := strconv.ParseInt(r.URL.Query().Get("done"), 10, 64); err == nil && id > 0 {
		notice = i18n.T(reqLang(r), "server.web.report_request.notice_added", "id", strconv.FormatInt(id, 10))
	}
	s.renderReportRequests(w, r, p, http.StatusOK, notice, "", requestIn{SinceLast: true})
}

func (s *Server) reportRequestsSubmit(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, role, err := s.resolveProject(r.Context(), reqLang(r), p.User, r.PathValue("slug"))
	if err != nil {
		s.notFoundPage(w, r, i18n.T(reqLang(r), "server.api.err.project_not_found", "project", r.PathValue("slug")))
		return
	}
	in := requestIn{SinceLast: r.PostFormValue("mode") != "period", Target: r.PostFormValue("target"), Note: r.PostFormValue("note"),
		To: r.PostFormValue("to")}
	if !in.SinceLast {
		in.From = r.PostFormValue("from")
	}
	req, err := s.addRequest(r.Context(), reqLang(r), service.Actor{UserID: p.User.ID, Via: "web", Lang: reqLang(r)}, pr, role, in)
	if err != nil {
		var se *service.Error
		if !errors.As(err, &se) {
			s.internalError(w, r, err)
			return
		}
		status := http.StatusBadRequest
		switch se.Kind {
		case service.Forbidden:
			status = http.StatusForbidden
		case service.Conflict:
			status = http.StatusConflict
		}
		s.renderReportRequests(w, r, p, status, "", i18n.Text(reqLang(r), se), in)
		return
	}
	s.cfg.Logger.Info("usage_request", "action", "create", "user", p.User.Login, "project", pr.Slug, "id", req.ID)
	http.Redirect(w, r, fmt.Sprintf("%s/p/%s/report-requests?done=%d", s.cfg.BasePath, pr.Slug, req.ID), http.StatusSeeOther)
}

func (s *Server) renderReportRequests(w http.ResponseWriter, r *http.Request, p *principal, status int, notice, errMsg string, form requestIn) {
	pr, role, err := s.resolveProject(r.Context(), reqLang(r), p.User, r.PathValue("slug"))
	if err != nil {
		s.notFoundPage(w, r, i18n.T(reqLang(r), "server.api.err.project_not_found", "project", r.PathValue("slug")))
		return
	}
	items, err := s.requestList(r.Context(), reqLang(r), pr, true)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	type row struct {
		usageRequestJSON
		Created string // 登録日時（プロジェクトの現地時刻。svc.Loc）
	}
	rows := []row{}
	for i, it := range items {
		if i == 50 {
			break
		}
		rows = append(rows, row{it, loc2(it.CreatedAt, s.svc.Loc)})
	}
	s.render(w, r, status, "report_requests.html", map[string]any{"User": p.User, "CSRF": p.Session.CSRFToken, "Base": s.cfg.BasePath,
		"Slug": pr.Slug, "Name": pr.Name, "CanWrite": canWrite(role), "Requests": rows, "Notice": notice, "Error": errMsg, "Form": form})
}

// ---- MCP（list_usage_requests）

type listRequestsIn struct {
	projectArg
	All bool `json:"all,omitempty" jsonschema:"server.mcp.arg.list_usage_requests.all"`
}

func (s *Server) addRequestMCPTools(srv *mcp.Server, lang i18n.Lang, ro *mcp.ToolAnnotations) {
	addTool(srv, lang, &mcp.Tool{Name: "list_usage_requests", Annotations: ro,
		Description: i18n.T(lang, "server.mcp.tool.list_usage_requests")},
		func(ctx context.Context, req *mcp.CallToolRequest, in listRequestsIn) (*mcp.CallToolResult, any, error) {
			pr, _, err := s.mcpProject(ctx, req, in.Project, "list_usage_requests")
			if err != nil {
				return nil, nil, err
			}
			lang := mcpLang(req)
			items, err := s.requestList(ctx, lang, pr, in.All)
			if err != nil {
				return nil, nil, s.toolError(lang, "list_usage_requests", err)
			}
			text := i18n.T(lang, "server.mcp.usage_request.none_open")
			if in.All {
				text = i18n.T(lang, "server.mcp.usage_request.none")
			}
			if len(items) > 0 {
				text = strings.TrimSpace(s.requestText(lang, items))
				if !in.All {
					text += "\n\n" + requestCommand(lang, items[len(items)-1].ID)
				}
			}
			return result(text, map[string]any{"project": pr.Slug, "items": items, "count": len(items),
				"timezone": s.svc.Loc.String()}), nil, nil
		})
}
