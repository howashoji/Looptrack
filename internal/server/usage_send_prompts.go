package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

// 指示文の作業名を送るか（プロジェクト別ルール usage.send_prompts。設計は DESIGN.md §5-4「指示文」）。
// Web のプロジェクト管理画面（POST /admin/projects/{slug}/send-prompts。管理者だけ）から切り替える。
// looptrack project rules set で usage.send_prompts を書いても同じ。CLI / フックは POST /usage の応答の send_prompts で知る。

// adminSendPrompts は POST /admin/projects/{slug}/send-prompts（enabled=on / off）。
func (s *Server) adminSendPrompts(w http.ResponseWriter, r *http.Request, p *principal) {
	ctx := r.Context()
	pr, err := store.ProjectBySlug(ctx, s.db, r.PathValue("slug"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.renderAdminProjects(w, r, p, adminResult{Error: i18n.T(reqLang(r), "server.api.err.project_not_found", "project", r.PathValue("slug")), status: http.StatusNotFound})
		} else {
			s.internalError(w, r, err)
		}
		return
	}
	var enabled bool
	switch r.PostFormValue("enabled") {
	case "on":
		enabled = true
	case "off":
	default:
		s.renderAdminProjects(w, r, p, adminResult{Error: i18n.T(reqLang(r), "server.web.admin.err_send_choice"), status: http.StatusBadRequest})
		return
	}
	res, err := s.svc.SetUsageSendPrompts(ctx, service.Actor{UserID: p.User.ID, Via: "web", Lang: reqLang(r)}, p.User, pr, enabled, s.clientIP(r))
	if err != nil {
		var se *service.Error
		if errors.As(err, &se) {
			s.renderAdminProjects(w, r, p, adminResult{Error: i18n.Text(reqLang(r), se), status: http.StatusForbidden})
			return
		}
		s.internalError(w, r, err)
		return
	}
	if res.Changed {
		s.cfg.Logger.Info("admin", "action", "usage_send_prompts", "actor", p.User.Login, "project", res.Project, "enabled", res.Enabled, "via", "web")
	}
	s.renderAdminProjects(w, r, p, adminResult{Notice: res.Message})
}

// gateRow はプロジェクト管理画面の 1 プロジェクト分の切り替え設定の表示。
type gateRow struct {
	Enabled bool
	Last    string // 直近の変更（「2026-09-18 21:00 alice が送るに（web）」。記録が無ければ空）
}

// sendPromptsRowOf はプロジェクト管理画面の 1 プロジェクト分の「指示文を送る」の表示。
func (s *Server) sendPromptsRowOf(ctx context.Context, lang i18n.Lang, pr store.Project) (gateRow, error) {
	row := gateRow{Enabled: service.UsageSendPromptsEnabled(pr)}
	cs, err := store.SettingChanges(ctx, s.db, store.UsageSendPromptsSetting(pr.Slug), 1)
	if err != nil || len(cs) == 0 {
		return row, err
	}
	c := cs[0]
	who := c.ActorLogin
	if who == "" {
		who = i18n.T(lang, "server.web.admin.actor_command")
	}
	what := i18n.T(lang, "server.web.admin.send_off")
	if c.NewValue == store.GateOn {
		what = i18n.T(lang, "server.web.admin.send_on")
	}
	row.Last = i18n.T(lang, "server.web.admin.setting_last", "at", s.fmtTime(c.At), "who", who, "what", what, "via", c.Via)
	return row, nil
}
