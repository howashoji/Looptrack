package server

import (
	"net/http"

	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

// 連絡の画面（/p/{slug}/messages。docs/server/DESIGN.md §9-9）。自分宛てのものだけを見せる閲覧の画面で、既読の印は付けない
// （既読はセッションごとの持ち物で、hook と CLI が付ける）。送るのは CLI・MCP・REST から行う。

func (s *Server) messagesPage(w http.ResponseWriter, r *http.Request, p *principal) {
	a := service.Actor{UserID: p.User.ID, Via: "web", Lang: reqLang(r)}
	res, err := s.svc.Inbox(r.Context(), a, r.PathValue("slug"), service.InboxQuery{})
	if err != nil {
		s.memoryPageError(w, r, err)
		return
	}
	type row struct {
		store.Message
		Created string
	}
	rows := make([]row, 0, len(res.Messages))
	for _, m := range res.Messages {
		rows = append(rows, row{m, m.CreatedAt.In(s.svc.Loc).Format("2006-01-02 15:04")})
	}
	s.render(w, r, http.StatusOK, "messages.html", map[string]any{"User": p.User, "CSRF": p.Session.CSRFToken, "Base": s.cfg.BasePath,
		"Slug": res.ProjectSlug, "Messages": rows})
}
