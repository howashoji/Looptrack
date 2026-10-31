package server

import (
	"net/http"

	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

// 占有の画面（/p/{slug}/leases と /p/{slug}/leases/{name}。docs/server/DESIGN.md §9-9）。
// 閲覧だけで、取る・返す・外すは CLI・MCP・REST から行う。見える範囲（参加者）は service が決める。

func (s *Server) leaseListPage(w http.ResponseWriter, r *http.Request, p *principal) {
	a := service.Actor{UserID: p.User.ID, Via: "web", Lang: reqLang(r)}
	list, pr, err := s.svc.ListLeases(r.Context(), a, r.PathValue("slug"))
	if err != nil {
		s.memoryPageError(w, r, err)
		return
	}
	type row struct {
		service.LeaseState
		Expires string
	}
	rows := make([]row, 0, len(list))
	for _, l := range list {
		rows = append(rows, row{l, l.ExpiresAt.In(s.svc.Loc).Format("2006-01-02 15:04")})
	}
	s.render(w, r, http.StatusOK, "leases.html", map[string]any{"User": p.User, "CSRF": p.Session.CSRFToken, "Base": s.cfg.BasePath,
		"Slug": pr.Slug, "Name": pr.Name, "Leases": rows})
}

func (s *Server) leaseDetailPage(w http.ResponseWriter, r *http.Request, p *principal) {
	a := service.Actor{UserID: p.User.ID, Via: "web", Lang: reqLang(r)}
	d, err := s.svc.GetLease(r.Context(), a, r.PathValue("slug"), r.PathValue("name"))
	if err != nil {
		s.memoryPageError(w, r, err)
		return
	}
	type ev struct {
		store.LeaseEvent
		Created string
	}
	events := make([]ev, 0, len(d.Events))
	for _, e := range d.Events {
		events = append(events, ev{e, e.CreatedAt.In(s.svc.Loc).Format("2006-01-02 15:04:05")})
	}
	expires := ""
	if d.Held() {
		expires = d.ExpiresAt.In(s.svc.Loc).Format("2006-01-02 15:04")
	}
	s.render(w, r, http.StatusOK, "lease_detail.html", map[string]any{"User": p.User, "CSRF": p.Session.CSRFToken, "Base": s.cfg.BasePath,
		"Slug": d.ProjectSlug, "Lease": d.LeaseState, "Expires": expires, "Events": events})
}
