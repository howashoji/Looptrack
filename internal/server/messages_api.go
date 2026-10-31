package server

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

// 連絡（docs/server/DESIGN.md §9-9）の REST と MCP。宛先・権限・検査・既読は service（messages.go）にあり、ここでは HTTP と MCP の形だけを決める。
//
//	POST /api/v1/projects/{slug}/messages         送る（{to, user, session, body, issue, machine_label}）
//	GET  /api/v1/projects/{slug}/messages/{id}    1 件（宛先と送り手だけ）
//	GET  /api/v1/projects/{slug}/inbox?unread=1&sent=1&limit=N   自分宛ての一覧（既読の印は付けない）
//	POST /api/v1/projects/{slug}/inbox/read       既読の印を付ける（{ids}。要求のセッションの既読）

type messageJSON struct {
	ID          int64  `json:"id"`
	Project     string `json:"project"`
	To          string `json:"to"`
	ToUser      string `json:"to_user,omitempty"`
	ToSession   string `json:"to_session,omitempty"`
	From        string `json:"from"`
	FromMachine string `json:"from_machine,omitempty"`
	FromSession string `json:"from_session,omitempty"`
	Issue       string `json:"issue,omitempty"`
	Body        string `json:"body"`
	CreatedAt   string `json:"created_at"`
	Read        bool   `json:"read"`
}

func toMessageJSON(slug string, m store.Message) messageJSON {
	return messageJSON{ID: m.ID, Project: slug, To: m.ToKind, ToUser: m.ToLogin, ToSession: m.ToSession, From: m.SenderLogin, FromMachine: m.SenderMachine,
		FromSession: m.SenderSession, Issue: m.IssueDisplay, Body: m.Body, CreatedAt: m.CreatedAt.UTC().Format(time.RFC3339), Read: m.Read}
}

func messagesJSON(slug string, list []store.Message) []messageJSON {
	out := make([]messageJSON, 0, len(list))
	for _, m := range list {
		out = append(out, toMessageJSON(slug, m))
	}
	return out
}

func inboxBody(res *service.InboxResult) map[string]any {
	return map[string]any{"messages": messagesJSON(res.ProjectSlug, res.Messages), "count": len(res.Messages), "unread": res.Unread,
		"unread_ids": res.UnreadIDs, "can_send": res.CanSend, "user": res.User}
}

type messageSendRequest struct {
	To           string `json:"to"`
	User         string `json:"user,omitempty"`
	Session      string `json:"session,omitempty"`
	Body         string `json:"body"`
	Issue        string `json:"issue,omitempty"`
	MachineLabel string `json:"machine_label,omitempty"`
}

// apiSendMessage は POST /projects/{slug}/messages。
func (s *Server) apiSendMessage(w http.ResponseWriter, r *http.Request) {
	var req messageSendRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	slug := r.PathValue("slug")
	m, err := s.svc.SendMessage(r.Context(), actor(r), service.MessageSendInput{Project: slug, To: req.To, User: req.User, Session: req.Session,
		Body: req.Body, Issue: req.Issue, MachineLabel: req.MachineLabel})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"message": toMessageJSON(slug, *m)})
}

// apiGetMessage は GET /projects/{slug}/messages/{id}。
func (s *Server) apiGetMessage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_argument", i18n.T(reqLang(r), "server.api.err.message_id"))
		return
	}
	slug := r.PathValue("slug")
	m, err := s.svc.GetMessage(r.Context(), actor(r), slug, id)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": toMessageJSON(slug, *m)})
}

// apiInbox は GET /projects/{slug}/inbox。
func (s *Server) apiInbox(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	res, err := s.svc.Inbox(r.Context(), actor(r), r.PathValue("slug"), service.InboxQuery{Unread: q.Get("unread") == "1", Sent: q.Get("sent") == "1", Limit: limit})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, inboxBody(res))
}

// apiInboxRead は POST /projects/{slug}/inbox/read。
func (s *Server) apiInboxRead(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	n, err := s.svc.MarkMessagesRead(r.Context(), actor(r), r.PathValue("slug"), req.IDs)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"marked": n})
}

// ---- MCP（send_message・read_inbox）

type (
	sendMessageIn struct {
		projectArg
		To           string `json:"to" jsonschema:"server.mcp.arg.send_message.to"`
		User         string `json:"user,omitempty" jsonschema:"server.mcp.arg.send_message.user"`
		Session      string `json:"session,omitempty" jsonschema:"server.mcp.arg.send_message.session"`
		Body         string `json:"body" jsonschema:"server.mcp.arg.send_message.body"`
		Issue        string `json:"issue,omitempty" jsonschema:"server.mcp.arg.send_message.issue"`
		MachineLabel string `json:"machine_label,omitempty" jsonschema:"server.mcp.arg.send_message.machine_label"`
	}
	readInboxIn struct {
		projectArg
		All      bool `json:"all,omitempty" jsonschema:"server.mcp.arg.read_inbox.all"`
		MarkRead bool `json:"mark_read,omitempty" jsonschema:"server.mcp.arg.read_inbox.mark_read"`
		Limit    int  `json:"limit,omitempty" jsonschema:"server.mcp.arg.read_inbox.limit"`
	}
)

func (s *Server) addMessageMCPTools(srv *mcp.Server, lang i18n.Lang) {
	notDestructive := false
	addTool(srv, lang, &mcp.Tool{Name: "send_message", Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive},
		Description: i18n.T(lang, "server.mcp.tool.send_message")},
		func(ctx context.Context, req *mcp.CallToolRequest, in sendMessageIn) (*mcp.CallToolResult, any, error) {
			c, err := s.mcpCallOf(req)
			if err != nil {
				return nil, nil, err
			}
			slug, err := s.projectSlug(ctx, c, in.Project)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "send_message", err)
			}
			m, err := s.svc.SendMessage(ctx, c.actor, service.MessageSendInput{Project: slug, To: in.To, User: in.User, Session: in.Session,
				Body: in.Body, Issue: in.Issue, MachineLabel: in.MachineLabel})
			if err != nil {
				return nil, nil, s.toolError(c.lang, "send_message", err)
			}
			return result(i18n.T(c.lang, "server.mcp.message.sent", "id", m.ID), map[string]any{"message": toMessageJSON(slug, *m)}), nil, nil
		})

	addTool(srv, lang, &mcp.Tool{Name: "read_inbox", Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive},
		Description: i18n.T(lang, "server.mcp.tool.read_inbox")},
		func(ctx context.Context, req *mcp.CallToolRequest, in readInboxIn) (*mcp.CallToolResult, any, error) {
			c, err := s.mcpCallOf(req)
			if err != nil {
				return nil, nil, err
			}
			slug, err := s.projectSlug(ctx, c, in.Project)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "read_inbox", err)
			}
			res, err := s.svc.Inbox(ctx, c.actor, slug, service.InboxQuery{Unread: !in.All, Limit: in.Limit})
			if err != nil {
				return nil, nil, s.toolError(c.lang, "read_inbox", err)
			}
			var b strings.Builder
			for _, m := range res.Messages {
				quoted := "> " + strings.ReplaceAll(strings.TrimRight(strings.ReplaceAll(m.Body, "\r\n", "\n"), "\n"), "\n", "\n> ")
				fmt.Fprintf(&b, "#%d %s (%s) %s\n%s\n\n", m.ID, m.SenderLogin, m.ToKind, m.CreatedAt.UTC().Format(time.RFC3339), quoted)
			}
			text := strings.TrimSpace(b.String())
			if text == "" {
				text = i18n.T(c.lang, "server.mcp.message.none", "project", res.ProjectSlug)
			} else {
				text = i18n.T(c.lang, "server.mcp.message.heading") + "\n\n" + text
			}
			if in.MarkRead {
				ids := make([]int64, 0, len(res.Messages))
				for _, m := range res.Messages {
					ids = append(ids, m.ID)
				}
				if _, err := s.svc.MarkMessagesRead(ctx, c.actor, slug, ids); err != nil {
					return nil, nil, s.toolError(c.lang, "read_inbox", err)
				}
			}
			return result(text, inboxBody(res)), nil, nil
		})
}
