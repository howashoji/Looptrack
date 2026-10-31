package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/service"
)

// 在席（docs/server/DESIGN.md §9-9）の REST と画面。動いているかの判定・権限・検査は service（presence.go）にあり、
// ここでは HTTP の形だけを決める。セッションは X-Looptrack-Session で受け取る。
//
//	POST   /api/v1/projects/{slug}/presence  在席を送る（{agent, machine_label, branch, head_sha, directory}）
//	DELETE /api/v1/projects/{slug}/presence  セッションの終わり（在席を外す）

// presenceJSON は動いているセッション 1 本。
type presenceJSON struct {
	User         string   `json:"user"`
	Agent        string   `json:"agent"`
	MachineLabel string   `json:"machine_label"`
	SessionID    string   `json:"session_id"`
	Branch       string   `json:"branch"`
	HeadSHA      string   `json:"head_sha"`
	Directory    string   `json:"directory"`
	Issues       []string `json:"issues"`
	LastSeenAt   string   `json:"last_seen_at"`
}

func presencesJSON(list []service.PresenceState) []presenceJSON {
	out := make([]presenceJSON, 0, len(list))
	for _, p := range list {
		issues := p.Issues
		if issues == nil {
			issues = []string{}
		}
		out = append(out, presenceJSON{User: p.UserLogin, Agent: p.Agent, MachineLabel: p.MachineLabel, SessionID: p.SessionID, Branch: p.Branch,
			HeadSHA: p.HeadSHA, Directory: p.Directory, Issues: issues, LastSeenAt: p.LastSeenAt.UTC().Format(time.RFC3339)})
	}
	return out
}

// presenceLines は動いているセッションを人が読む行にする（MCP の project_summary）。
func presenceLines(list []service.PresenceState, loc *time.Location) string {
	lines := make([]string, 0, len(list))
	for _, p := range list {
		lines = append(lines, strings.Join([]string{p.UserLogin, presenceClaim(p.MachineLabel), orDashText(p.Agent), presenceClaim(p.Branch), orDashText(presenceHead(p.HeadSHA)),
			presenceClaim(p.Directory), orDashText(strings.Join(p.Issues, ",")), p.LastSeenAt.In(loc).Format("2006-01-02 15:04")}, "  "))
	}
	return strings.Join(lines, "\n")
}

// presenceClaim は参加者が名乗った値をバッククォートで囲む（AI の文脈で地の文に見えないように。値にバッククォートは入らない）。
func presenceClaim(v string) string {
	if v == "" {
		return "-"
	}
	return "`" + v + "`"
}

func presenceHead(sha string) string {
	return sha[:min(len(sha), 8)]
}

type presenceRequest struct {
	Agent        string `json:"agent,omitempty"`
	MachineLabel string `json:"machine_label,omitempty"`
	Branch       string `json:"branch,omitempty"`
	HeadSHA      string `json:"head_sha,omitempty"`
	Directory    string `json:"directory,omitempty"`
}

// apiReportPresence は POST /projects/{slug}/presence。
func (s *Server) apiReportPresence(w http.ResponseWriter, r *http.Request) {
	var req presenceRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	err := s.svc.ReportPresence(r.Context(), actor(r), service.PresenceInput{Project: r.PathValue("slug"), Agent: req.Agent, MachineLabel: req.MachineLabel,
		Branch: req.Branch, HeadSHA: req.HeadSHA, Directory: req.Directory})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// apiEndPresence は DELETE /projects/{slug}/presence。
func (s *Server) apiEndPresence(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.EndPresence(r.Context(), actor(r), r.PathValue("slug")); err != nil {
		s.serviceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// presenceListPage は /p/{slug}/presence（動いているセッションの一覧。閲覧だけ）。
func (s *Server) presenceListPage(w http.ResponseWriter, r *http.Request, p *principal) {
	a := service.Actor{UserID: p.User.ID, Via: "web", Lang: reqLang(r)}
	list, pr, err := s.svc.ListPresence(r.Context(), a, r.PathValue("slug"))
	if err != nil {
		s.memoryPageError(w, r, err)
		return
	}
	type row struct {
		service.PresenceState
		Head, Seen, IssueList string
	}
	rows := make([]row, 0, len(list))
	for _, x := range list {
		rows = append(rows, row{x, presenceHead(x.HeadSHA), x.LastSeenAt.In(s.svc.Loc).Format("2006-01-02 15:04"), strings.Join(x.Issues, ", ")})
	}
	s.render(w, r, http.StatusOK, "presence.html", map[string]any{"User": p.User, "CSRF": p.Session.CSRFToken, "Base": s.cfg.BasePath,
		"Slug": pr.Slug, "Name": pr.Name, "Sessions": rows, "Minutes": int(service.PresenceActive / time.Minute)})
}
