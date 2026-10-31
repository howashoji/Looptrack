package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

// 占有（docs/server/DESIGN.md §9-9）の REST と MCP。権限・期限・持ち主の照合・資格情報の検査は service（lease.go）にあり、
// ここでは HTTP と MCP の形だけを決める。
//
//	GET    /api/v1/projects/{slug}/leases                一覧（持ち主のいるもの。期限切れは expired が true）
//	GET    /api/v1/projects/{slug}/leases/{name}         1 件と記録
//	POST   /api/v1/projects/{slug}/leases/{name}         取る（{ttl_seconds, purpose, machine_label}。応答の lease_token は取った者にだけ返す。取られていれば 409 lease_held）
//	POST   /api/v1/projects/{slug}/leases/{name}/extend  延ばす（{ttl_seconds, machine_label, lease_token}。取っている者だけ）
//	DELETE /api/v1/projects/{slug}/leases/{name}         返す（本文は任意。{machine_label, lease_token}。取っている者だけ）
//	POST   /api/v1/projects/{slug}/leases/{name}/break   外す（{reason, machine_label}。editor 以上）

// leaseJSON は資源 1 つの今の持ち主。
type leaseJSON struct {
	Project      string `json:"project"`
	Name         string `json:"name"`
	Holder       string `json:"holder"`
	MachineLabel string `json:"machine_label"`
	SessionID    string `json:"session_id"`
	Purpose      string `json:"purpose"`
	AcquiredAt   string `json:"acquired_at,omitempty"`
	ExpiresAt    string `json:"expires_at,omitempty"`
	Expired      bool   `json:"expired"`
}

func toLeaseJSON(l service.LeaseState) leaseJSON {
	out := leaseJSON{Project: l.ProjectSlug, Name: l.Name, Holder: l.HolderLogin, MachineLabel: l.MachineLabel, SessionID: l.SessionID, Purpose: l.Purpose, Expired: l.Expired}
	if l.Held() {
		out.AcquiredAt, out.ExpiresAt = l.AcquiredAt.UTC().Format(time.RFC3339), l.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return out
}

func leasesJSON(list []service.LeaseState) []leaseJSON {
	out := make([]leaseJSON, 0, len(list))
	for _, l := range list {
		out = append(out, toLeaseJSON(l))
	}
	return out
}

// leaseEventJSON は占有の記録 1 件。
type leaseEventJSON struct {
	Action           string `json:"action"`
	User             string `json:"user"`
	MachineLabel     string `json:"machine_label"`
	SessionID        string `json:"session_id"`
	Via              string `json:"via"`
	Purpose          string `json:"purpose"`
	Reason           string `json:"reason,omitempty"`
	ExpiresAt        string `json:"expires_at,omitempty"`
	PrevUser         string `json:"prev_user,omitempty"`
	PrevMachineLabel string `json:"prev_machine_label,omitempty"`
	PrevSessionID    string `json:"prev_session_id,omitempty"`
	WasExpired       bool   `json:"was_expired"`
	CreatedAt        string `json:"created_at"`
}

func toLeaseEventJSON(e store.LeaseEvent) leaseEventJSON {
	out := leaseEventJSON{Action: e.Action, User: e.UserLogin, MachineLabel: e.MachineLabel, SessionID: e.SessionID, Via: e.Via, Purpose: e.Purpose, Reason: e.Reason,
		PrevUser: e.PrevUserLogin, PrevMachineLabel: e.PrevMachineLabel, PrevSessionID: e.PrevSessionID, WasExpired: e.WasExpired, CreatedAt: e.CreatedAt.UTC().Format(time.RFC3339)}
	if !e.ExpiresAt.IsZero() {
		out.ExpiresAt = e.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return out
}

// leaseLines は占有の一覧を人が読む行にする（MCP の list_leases と project_summary）。
func leaseLines(lang i18n.Lang, list []service.LeaseState, loc *time.Location) string {
	lines := make([]string, 0, len(list))
	for _, l := range list {
		line := fmt.Sprintf("%s  %s  %s  %s  %s  %s", l.Name, l.HolderLogin, orDashText(l.MachineLabel), orDashText(l.SessionID),
			l.ExpiresAt.In(loc).Format("2006-01-02 15:04"), l.Purpose)
		if l.Expired {
			line += "  " + i18n.T(lang, "server.mcp.lease.expired")
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func orDashText(v string) string {
	if v == "" {
		return "-"
	}
	return v
}

type leaseRequest struct {
	TTLSeconds   int64  `json:"ttl_seconds,omitempty"`
	Purpose      string `json:"purpose,omitempty"`
	Reason       string `json:"reason,omitempty"`
	MachineLabel string `json:"machine_label,omitempty"`
	// LeaseToken は取ったときに返した世代の鍵（延ばす・返す）
	LeaseToken string `json:"lease_token,omitempty"`
}

// apiListLeases は GET /projects/{slug}/leases。
func (s *Server) apiListLeases(w http.ResponseWriter, r *http.Request) {
	list, _, err := s.svc.ListLeases(r.Context(), actor(r), r.PathValue("slug"))
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"leases": leasesJSON(list), "count": len(list), "timezone": s.svc.Loc.String()})
}

// apiGetLease は GET /projects/{slug}/leases/{name}。
func (s *Server) apiGetLease(w http.ResponseWriter, r *http.Request) {
	d, err := s.svc.GetLease(r.Context(), actor(r), r.PathValue("slug"), r.PathValue("name"))
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	events := make([]leaseEventJSON, 0, len(d.Events))
	for _, e := range d.Events {
		events = append(events, toLeaseEventJSON(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{"lease": toLeaseJSON(d.LeaseState), "events": events, "timezone": s.svc.Loc.String()})
}

// apiAcquireLease は POST /projects/{slug}/leases/{name}。
func (s *Server) apiAcquireLease(w http.ResponseWriter, r *http.Request) {
	var req leaseRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	l, err := s.svc.AcquireLease(r.Context(), actor(r), service.LeaseInput{Project: r.PathValue("slug"), Name: r.PathValue("name"), TTLSeconds: req.TTLSeconds,
		Purpose: req.Purpose, MachineLabel: req.MachineLabel})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"lease": toLeaseJSON(l.LeaseState), "lease_token": l.Token, "timezone": s.svc.Loc.String()})
}

// apiExtendLease は POST /projects/{slug}/leases/{name}/extend。
func (s *Server) apiExtendLease(w http.ResponseWriter, r *http.Request) {
	var req leaseRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	l, err := s.svc.ExtendLease(r.Context(), actor(r), service.LeaseInput{Project: r.PathValue("slug"), Name: r.PathValue("name"), TTLSeconds: req.TTLSeconds,
		MachineLabel: req.MachineLabel, Token: req.LeaseToken})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lease": toLeaseJSON(*l), "timezone": s.svc.Loc.String()})
}

// apiReleaseLease は DELETE /projects/{slug}/leases/{name}。
func (s *Server) apiReleaseLease(w http.ResponseWriter, r *http.Request) {
	var req leaseRequest
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	err := s.svc.ReleaseLease(r.Context(), actor(r), service.LeaseInput{Project: r.PathValue("slug"), Name: r.PathValue("name"), MachineLabel: req.MachineLabel, Token: req.LeaseToken})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"released": true})
}

// apiBreakLease は POST /projects/{slug}/leases/{name}/break。
func (s *Server) apiBreakLease(w http.ResponseWriter, r *http.Request) {
	var req leaseRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	res, err := s.svc.BreakLease(r.Context(), actor(r), service.LeaseInput{Project: r.PathValue("slug"), Name: r.PathValue("name"), Reason: req.Reason, MachineLabel: req.MachineLabel})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"broken": true, "holder": res.Holder, "notice_failed": res.NoticeFailed})
}

// ---- MCP（list_leases・acquire_lease・extend_lease・release_lease・break_lease）

type (
	leaseNameArg struct {
		projectArg
		Name         string `json:"name" jsonschema:"server.mcp.arg.lease.name"`
		MachineLabel string `json:"machine_label,omitempty" jsonschema:"server.mcp.arg.lease.machine_label"`
	}
	acquireLeaseIn struct {
		leaseNameArg
		Purpose    string `json:"purpose" jsonschema:"server.mcp.arg.lease.purpose"`
		TTLSeconds int64  `json:"ttl_seconds,omitempty" jsonschema:"server.mcp.arg.lease.ttl_seconds"`
	}
	extendLeaseIn struct {
		leaseNameArg
		TTLSeconds int64  `json:"ttl_seconds,omitempty" jsonschema:"server.mcp.arg.lease.ttl_seconds"`
		LeaseToken string `json:"lease_token,omitempty" jsonschema:"server.mcp.arg.lease.lease_token"`
	}
	releaseLeaseIn struct {
		leaseNameArg
		LeaseToken string `json:"lease_token,omitempty" jsonschema:"server.mcp.arg.lease.lease_token"`
	}
	breakLeaseIn struct {
		leaseNameArg
		Reason string `json:"reason" jsonschema:"server.mcp.arg.lease.reason"`
	}
)

// mcpLeaseInput は MCP の引数から service の入力を作る。
func (s *Server) mcpLeaseInput(ctx context.Context, c *mcpCall, tool string, in leaseNameArg) (service.LeaseInput, error) {
	slug, err := s.projectSlug(ctx, c, in.Project)
	if err != nil {
		return service.LeaseInput{}, s.toolError(c.lang, tool, err)
	}
	return service.LeaseInput{Project: slug, Name: in.Name, MachineLabel: in.MachineLabel}, nil
}

func (s *Server) addLeaseMCPTools(srv *mcp.Server, lang i18n.Lang, ro *mcp.ToolAnnotations) {
	notDestructive := false

	addTool(srv, lang, &mcp.Tool{Name: "list_leases", Annotations: ro, Description: i18n.T(lang, "server.mcp.tool.list_leases")},
		func(ctx context.Context, req *mcp.CallToolRequest, in projectArg) (*mcp.CallToolResult, any, error) {
			c, err := s.mcpCallOf(req)
			if err != nil {
				return nil, nil, err
			}
			slug, err := s.projectSlug(ctx, c, in.Project)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "list_leases", err)
			}
			list, p, err := s.svc.ListLeases(ctx, c.actor, slug)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "list_leases", err)
			}
			text := leaseLines(c.lang, list, s.svc.Loc)
			if text == "" {
				text = i18n.T(c.lang, "server.mcp.lease.none", "project", p.Slug)
			}
			return result(text, map[string]any{"leases": leasesJSON(list), "count": len(list)}), nil, nil
		})

	addTool(srv, lang, &mcp.Tool{Name: "acquire_lease", Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive}, Description: i18n.T(lang, "server.mcp.tool.acquire_lease")},
		func(ctx context.Context, req *mcp.CallToolRequest, in acquireLeaseIn) (*mcp.CallToolResult, any, error) {
			c, err := s.mcpCallOf(req)
			if err != nil {
				return nil, nil, err
			}
			li, err := s.mcpLeaseInput(ctx, c, "acquire_lease", in.leaseNameArg)
			if err != nil {
				return nil, nil, err
			}
			li.Purpose, li.TTLSeconds = in.Purpose, in.TTLSeconds
			l, err := s.svc.AcquireLease(ctx, c.actor, li)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "acquire_lease", err)
			}
			return result(i18n.T(c.lang, "server.mcp.lease.acquired", "name", l.Name, "expires", l.ExpiresAt.In(s.svc.Loc).Format("2006-01-02 15:04"), "token", l.Token),
				map[string]any{"lease": toLeaseJSON(l.LeaseState), "lease_token": l.Token}), nil, nil
		})

	addTool(srv, lang, &mcp.Tool{Name: "extend_lease", Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive}, Description: i18n.T(lang, "server.mcp.tool.extend_lease")},
		func(ctx context.Context, req *mcp.CallToolRequest, in extendLeaseIn) (*mcp.CallToolResult, any, error) {
			c, err := s.mcpCallOf(req)
			if err != nil {
				return nil, nil, err
			}
			li, err := s.mcpLeaseInput(ctx, c, "extend_lease", in.leaseNameArg)
			if err != nil {
				return nil, nil, err
			}
			li.TTLSeconds, li.Token = in.TTLSeconds, in.LeaseToken
			l, err := s.svc.ExtendLease(ctx, c.actor, li)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "extend_lease", err)
			}
			return result(i18n.T(c.lang, "server.mcp.lease.extended", "name", l.Name, "expires", l.ExpiresAt.In(s.svc.Loc).Format("2006-01-02 15:04")),
				map[string]any{"lease": toLeaseJSON(*l)}), nil, nil
		})

	addTool(srv, lang, &mcp.Tool{Name: "release_lease", Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive}, Description: i18n.T(lang, "server.mcp.tool.release_lease")},
		func(ctx context.Context, req *mcp.CallToolRequest, in releaseLeaseIn) (*mcp.CallToolResult, any, error) {
			c, err := s.mcpCallOf(req)
			if err != nil {
				return nil, nil, err
			}
			li, err := s.mcpLeaseInput(ctx, c, "release_lease", in.leaseNameArg)
			if err != nil {
				return nil, nil, err
			}
			li.Token = in.LeaseToken
			if err := s.svc.ReleaseLease(ctx, c.actor, li); err != nil {
				return nil, nil, s.toolError(c.lang, "release_lease", err)
			}
			return result(i18n.T(c.lang, "server.mcp.lease.released", "name", li.Name), map[string]any{"released": true}), nil, nil
		})

	addTool(srv, lang, &mcp.Tool{Name: "break_lease", Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive}, Description: i18n.T(lang, "server.mcp.tool.break_lease")},
		func(ctx context.Context, req *mcp.CallToolRequest, in breakLeaseIn) (*mcp.CallToolResult, any, error) {
			c, err := s.mcpCallOf(req)
			if err != nil {
				return nil, nil, err
			}
			li, err := s.mcpLeaseInput(ctx, c, "break_lease", in.leaseNameArg)
			if err != nil {
				return nil, nil, err
			}
			li.Reason = in.Reason
			res, err := s.svc.BreakLease(ctx, c.actor, li)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "break_lease", err)
			}
			text := i18n.T(c.lang, "server.mcp.lease.broken", "name", li.Name)
			if res.NoticeFailed {
				text += "\n" + i18n.T(c.lang, "server.mcp.lease.notice_failed", "holder", res.Holder)
			}
			return result(text, map[string]any{"broken": true, "holder": res.Holder, "notice_failed": res.NoticeFailed}), nil, nil
		})
}
