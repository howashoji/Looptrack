package server

import (
	"context"
	"html/template"
	"net/http"
	"net/url"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

func urlQueryEscape(s string) string { return url.QueryEscape(s) }

// templateURL は自前で組み立てた data: URL をテンプレートに渡す（html/template は data: を既定で無害化するため）。
func templateURL(s string) template.URL { return template.URL(s) } //nolint:gosec // サーバが生成した PNG の data URL のみ

func (s *Server) apiMe(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"login": p.User.Login, "display_name": p.User.DisplayName, "role": p.User.Role, "via": p.Via,
	})
}

type projectJSON struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Prefix      string `json:"prefix"`
	Width       int    `json:"width"`
	Role        string `json:"role"`
	// Member は利用者がプロジェクトに参加している（project_members に行がある）か。プロジェクトの一覧と
	// 1 件の取得だけに付ける。admin の利用者が参加していないプロジェクトを slug で開いたときは false。
	Member *bool `json:"member,omitempty"`
}

// resolveProject は利用者が見られるプロジェクトを slug で引く。権限が無い場合も「存在しない」と同じ NotFound にする。
func (s *Server) resolveProject(ctx context.Context, lang i18n.Lang, u store.User, slug string) (store.Project, string, error) {
	projects, roles, err := store.AccessibleProjects(ctx, s.db, u)
	if err != nil {
		return store.Project{}, "", err
	}
	for _, pr := range projects {
		if pr.Slug == slug {
			return pr, roles[pr.ID], nil
		}
	}
	return store.Project{}, "", &service.Error{Kind: service.NotFound, Code: "not_found", Message: i18n.T(lang, "server.api.err.project_not_found", "project", slug)}
}

// projectFor は resolveProject の HTTP 版（エラーは応答に書く）。
func (s *Server) projectFor(w http.ResponseWriter, r *http.Request, slug string) (store.Project, string, bool) {
	pr, role, err := s.resolveProject(r.Context(), reqLang(r), principalFrom(r.Context()).User, slug)
	if err != nil {
		s.serviceError(w, r, err)
		return store.Project{}, "", false
	}
	return pr, role, true
}
