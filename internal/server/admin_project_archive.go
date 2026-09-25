package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

// プロジェクト管理画面（/admin/projects）からの表示名の変更・アーカイブ・戻す。管理者だけ（adminForbidden。CSRF は s.web が検査）。
// 規則はどれも service に置く（表示名は RenameProject、アーカイブは SetProjectArchived）。画面は確認と表示だけを持つ。

// adminProjectOf は {slug} のプロジェクトを引く（アーカイブ済みも含む）。無ければ画面にエラーを出して false。
func (s *Server) adminProjectOf(w http.ResponseWriter, r *http.Request, p *principal) (store.Project, bool) {
	pr, err := store.ProjectBySlug(r.Context(), s.db, r.PathValue("slug"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.renderAdminProjects(w, r, p, adminResult{Error: i18n.T(reqLang(r), "server.api.err.project_not_found", "project", r.PathValue("slug")), status: http.StatusNotFound})
		} else {
			s.internalError(w, r, err)
		}
		return store.Project{}, false
	}
	return pr, true
}

// adminServiceError は service の拒否を画面のエラーとして出す（それ以外は 500）。
func (s *Server) adminServiceError(w http.ResponseWriter, r *http.Request, p *principal, err error) {
	var se *service.Error
	if errors.As(err, &se) {
		status := http.StatusBadRequest
		if se.Kind == service.NotFound {
			status = http.StatusNotFound
		}
		s.renderAdminProjects(w, r, p, adminResult{Error: i18n.Text(reqLang(r), se), status: status})
		return
	}
	s.internalError(w, r, err)
}

// adminProjectRename は POST /admin/projects/{slug}/rename（name）。slug・prefix・発番済みの ID は変わらない。
func (s *Server) adminProjectRename(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, ok := s.adminProjectOf(w, r, p)
	if !ok {
		return
	}
	lang := reqLang(r)
	res, err := s.svc.RenameProject(r.Context(), pr, strings.TrimSpace(r.PostFormValue("name")))
	if err != nil {
		s.adminServiceError(w, r, p, err)
		return
	}
	if !res.Changed {
		s.renderAdminProjects(w, r, p, adminResult{Notice: i18n.T(lang, "cmd.project.rename_unchanged", "slug", pr.Slug, "new", res.NewName)})
		return
	}
	s.cfg.Logger.Info("admin", "action", "project_rename", "actor", p.User.Login, "project", pr.Slug, "old", res.OldName, "new", res.NewName, "via", "web")
	s.renderAdminProjects(w, r, p, adminResult{Notice: i18n.T(lang, "cmd.project.renamed", "slug", pr.Slug, "old", res.OldName, "new", res.NewName)})
}

// adminProjectArchive は POST /admin/projects/{slug}/archive（confirm に slug を打ち込ませて確認する）。
func (s *Server) adminProjectArchive(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, ok := s.adminProjectOf(w, r, p)
	if !ok {
		return
	}
	if strings.TrimSpace(r.PostFormValue("confirm")) != pr.Slug {
		s.renderAdminProjects(w, r, p, adminResult{Error: i18n.T(reqLang(r), "server.web.admin_projects.err_archive_confirm", "slug", pr.Slug), status: http.StatusBadRequest})
		return
	}
	s.setArchived(w, r, p, pr, true)
}

// adminProjectUnarchive は POST /admin/projects/{slug}/unarchive。
func (s *Server) adminProjectUnarchive(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, ok := s.adminProjectOf(w, r, p)
	if !ok {
		return
	}
	s.setArchived(w, r, p, pr, false)
}

func (s *Server) setArchived(w http.ResponseWriter, r *http.Request, p *principal, pr store.Project, archived bool) {
	res, err := s.svc.SetProjectArchived(r.Context(), pr, archived)
	if err != nil {
		s.adminServiceError(w, r, p, err)
		return
	}
	if res.Changed {
		action := "project_unarchive"
		if archived {
			action = "project_archive"
		}
		s.cfg.Logger.Info("admin", "action", action, "actor", p.User.Login, "project", pr.Slug, "via", "web")
	}
	s.renderAdminProjects(w, r, p, adminResult{Notice: res.Message().In(reqLang(r))})
}
