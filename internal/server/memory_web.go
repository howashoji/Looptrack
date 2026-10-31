package server

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

// プロジェクト記憶の画面（/p/{slug}/memory と /p/{slug}/memory/{scope}/{kind}/{name}。docs/server/DESIGN.md §9-9）。
// 閲覧だけで、書き込みは CLI・MCP・REST から行う。見える範囲（参加者・自分の personal）は service が決める。

func (s *Server) memoryListPage(w http.ResponseWriter, r *http.Request, p *principal) {
	a := service.Actor{UserID: p.User.ID, Via: "web", Lang: reqLang(r)}
	list, pr, err := s.svc.ListMemories(r.Context(), a, r.PathValue("slug"), false)
	if err != nil {
		s.memoryPageError(w, r, err)
		return
	}
	type row struct {
		store.Memory
		Updated string
	}
	rows := make([]row, 0, len(list))
	for _, m := range list {
		rows = append(rows, row{m, m.UpdatedAt.In(s.svc.Loc).Format("2006-01-02 15:04")})
	}
	s.render(w, r, http.StatusOK, "memory.html", map[string]any{"User": p.User, "CSRF": p.Session.CSRFToken, "Base": s.cfg.BasePath,
		"Slug": pr.Slug, "Name": pr.Name, "Memories": rows})
}

func (s *Server) memoryDetailPage(w http.ResponseWriter, r *http.Request, p *principal) {
	a := service.Actor{UserID: p.User.ID, Via: "web", Lang: reqLang(r)}
	key := service.MemoryKey{Project: r.PathValue("slug"), Scope: r.PathValue("scope"), Kind: r.PathValue("kind"), Name: r.PathValue("name")}
	d, err := s.svc.GetMemory(r.Context(), a, key)
	if err != nil {
		s.memoryPageError(w, r, err)
		return
	}
	content, shown := d.Memory.Content, d.Memory.Version
	if raw := r.URL.Query().Get("version"); raw != "" {
		n, perr := strconv.ParseInt(raw, 10, 64)
		if perr != nil || n <= 0 {
			s.notFoundPage(w, r, i18n.T(reqLang(r), "service.err.memory.version_invalid"))
			return
		}
		v, err := s.svc.GetMemoryVersion(r.Context(), a, key, n)
		if err != nil {
			s.memoryPageError(w, r, err)
			return
		}
		content, shown = v.Content, v.Version
	}
	type ver struct {
		store.MemoryVersion
		Created string
	}
	vs := make([]ver, 0, len(d.Versions))
	for _, v := range d.Versions {
		vs = append(vs, ver{v, v.CreatedAt.In(s.svc.Loc).Format("2006-01-02 15:04")})
	}
	s.render(w, r, http.StatusOK, "memory_detail.html", map[string]any{"User": p.User, "CSRF": p.Session.CSRFToken, "Base": s.cfg.BasePath,
		"Slug": d.ProjectSlug, "Memory": d.Memory, "Content": content, "Shown": shown, "Versions": vs,
		"Updated": d.Memory.UpdatedAt.In(s.svc.Loc).Format("2006-01-02 15:04")})
}

// memoryPageError は service のエラーを画面にする（見えない・無いものは 404 の画面、それ以外は内部エラー）。
func (s *Server) memoryPageError(w http.ResponseWriter, r *http.Request, err error) {
	var se *service.Error
	if errors.As(err, &se) {
		s.notFoundPage(w, r, i18n.Text(reqLang(r), se))
		return
	}
	s.internalError(w, r, err)
}
