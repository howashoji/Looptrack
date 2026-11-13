package server

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
)

// タイムラインを読む側（docs/server/DESIGN.md §9-9「タイムラインの読み飛ばしを無くす仕組み」）の REST と MCP。
// 未読・読む窓・新しいセッション・「いま」・読んだ位置の規則と 1 行の文面は service（timeline_read.go）にあり、ここでは形だけを決める。
//
//	GET  /api/v1/projects/{slug}/timeline/unread?limit=N&now=1&hook=1   このセッションの未読と未了解（位置は動かさない。hook=1 は残りの知らせを hook 向けにする）
//	GET  /api/v1/projects/{slug}/timeline/now                    「いま」の表示
//	POST /api/v1/projects/{slug}/timeline/seen                   読んだ位置を進める（{seq}。要求のセッションの位置）

// timelineRowJSON は未読の出来事か近況 1 行。text は材料から組んだ 1 行で、参加者が書いた文は quote と個別の欄にだけ入る。
type timelineRowJSON struct {
	Seq      int64          `json:"seq"`
	Kind     string         `json:"kind"`
	Type     string         `json:"type"`
	User     string         `json:"user"`
	SameUser bool           `json:"same_user"`
	Machine  string         `json:"machine,omitempty"`
	Session  string         `json:"session,omitempty"`
	Issue    string         `json:"issue,omitempty"`
	At       string         `json:"at"`
	Detail   map[string]any `json:"detail"`
	Title    string         `json:"title,omitempty"`
	Subject  string         `json:"subject,omitempty"`
	Body     string         `json:"body,omitempty"`
	Purpose  string         `json:"purpose,omitempty"`
	Reason   string         `json:"reason,omitempty"`
	Text     string         `json:"text"`
	Quote    []string       `json:"quote"`
}

func toTimelineRowJSON(r service.TimelineRow) timelineRowJSON {
	q := r.Quote
	if q == nil {
		q = []string{}
	}
	return timelineRowJSON{Seq: r.Seq, Kind: r.Kind, Type: r.Type, User: r.User, SameUser: r.SameUser, Machine: r.Machine, Session: r.Session, Issue: r.Issue,
		At: timelinePostTime(r.At), Detail: r.Detail, Title: r.Title, Subject: r.Subject, Body: r.Body, Purpose: r.Purpose, Reason: r.Reason, Text: r.Text, Quote: q}
}

func timelineRowsJSON(rows []service.TimelineRow) []timelineRowJSON {
	out := make([]timelineRowJSON, 0, len(rows))
	for _, r := range rows {
		out = append(out, toTimelineRowJSON(r))
	}
	return out
}

func timelineNowJSON(n *service.TimelineNow) map[string]any {
	sessions := make([]map[string]any, 0, len(n.Sessions))
	for _, st := range n.Sessions {
		var up any
		if st.Update != nil {
			up = toTimelineRowJSON(*st.Update)
		}
		sessions = append(sessions, map[string]any{"user": st.User, "same_user": st.SameUser, "machine": st.Machine, "agent": st.Agent, "branch": st.Branch,
			"session": st.Session, "issues": st.Issues, "last_seen_at": timelinePostTime(st.LastSeenAt), "update": up, "text": st.Text})
	}
	leases := make([]map[string]any, 0, len(n.Leases))
	for _, l := range n.Leases {
		leases = append(leases, map[string]any{"name": l.Name, "holder": l.Holder, "machine": l.Machine, "purpose": l.Purpose,
			"expires_at": timelinePostTime(l.ExpiresAt), "text": l.Text})
	}
	return map[string]any{"sessions": sessions, "leases": leases, "holds": timelinePostsJSON(n.Holds), "requests": timelinePostsJSON(n.Requests)}
}

// timelineFeedJSON は読んだ結果の構造化の値（REST の応答と、next・set_status の結果の timeline）。
func timelineFeedJSON(f *service.TimelineFeed) map[string]any {
	// 最新の通し番号は head_seq。seen に渡すのは advance_to で、head_seq を渡すと上限で残した未読を読み飛ばす
	body := map[string]any{"project": f.ProjectSlug, "head_seq": f.Seq, "read_seq": f.ReadSeq, "has_position": f.HasPosition, "new_session": f.NewSession,
		"unacked_holds": timelinePostsJSON(f.UnackedHolds), "unacked_requests": timelinePostsJSON(f.UnackedRequests), "rows": timelineRowsJSON(f.Rows),
		"remaining": f.Remaining, "older": f.Older, "undelivered": timelinePostsJSON(f.Undelivered), "advance_to": f.AdvanceTo}
	if f.Now != nil {
		body["now"] = timelineNowJSON(f.Now)
	}
	return body
}

// timelineRowBlock は 1 行と引用の文。
func timelineRowBlock(r service.TimelineRow) string {
	lines := []string{r.Text}
	for _, q := range r.Quote {
		lines = append(lines, quoteBody(q))
	}
	return strings.Join(lines, "\n")
}

// timelineNowText は「いま」の表示の文面。待機と依頼は ID だけを並べる（本文は未了解の節か timeline_list で読む）。
func timelineNowText(lang i18n.Lang, n *service.TimelineNow) []string {
	lines := []string{i18n.T(lang, "server.timeline.feed.now")}
	if timelineNowEmpty(n) {
		return append(lines, i18n.T(lang, "server.timeline.feed.now_none"))
	}
	for _, st := range n.Sessions {
		lines = append(lines, st.Text)
		if st.Update != nil {
			for _, q := range st.Update.Quote {
				lines = append(lines, quoteBody(q))
			}
		}
	}
	for _, l := range n.Leases {
		lines = append(lines, l.Text)
		if l.Purpose != "" {
			lines = append(lines, quoteBody(l.Purpose))
		}
	}
	ids := func(vs []service.TimelinePostView) string {
		if len(vs) == 0 {
			return "-"
		}
		out := make([]string, 0, len(vs))
		for _, v := range vs {
			out = append(out, "#"+strconv.FormatInt(v.ID, 10))
		}
		return strings.Join(out, " ")
	}
	if len(n.Holds) > 0 || len(n.Requests) > 0 {
		lines = append(lines, i18n.T(lang, "server.timeline.feed.now_posts", "holds", ids(n.Holds), "requests", ids(n.Requests)))
	}
	return lines
}

// feedMode は読んだ結果を返す経路の違い（残りの知らせの文面を決める）。
type feedMode int

const (
	// feedReadOnly は読むだけで位置を進めない経路（REST の unread・MCP の timeline_unread）
	feedReadOnly feedMode = iota
	// feedAdvanced は返した後に位置を進める経路（MCP の next・set_status）
	feedAdvanced
	// feedNoSession は位置を進める経路だが、セッションが無く位置を記録できないとき
	feedNoSession
	// feedHook は hook が文脈に入れた後に位置を進める経路（REST の unread の hook=1。残りは次の場面で届く）
	feedHook
)

// timelineNowEmpty は「いま」に並べるものが何も無いか（読んだ結果の文面では節ごと省く）。
func timelineNowEmpty(n *service.TimelineNow) bool {
	return len(n.Sessions) == 0 && len(n.Leases) == 0 && len(n.Holds) == 0 && len(n.Requests) == 0
}

// timelineFeedText は読んだ結果を、並べる順（未了解の待機・未了解の依頼・「いま」・未読の出来事と近況・未達の知らせ）で文面にする。
// hint は位置を進める案内を末尾に付けるか（結果を返しただけで位置を進めない経路だけが付ける）。mode は残りの知らせの文面を経路に合わせる
// （読むだけ・返した後に位置を進めた・セッションが無く位置を記録できない）。何も無ければ空。
func timelineFeedText(lang i18n.Lang, f *service.TimelineFeed, loc *time.Location, hint bool, mode feedMode) string {
	showNow := f.Now != nil && !timelineNowEmpty(f.Now)
	if f.Empty() && !showNow {
		return ""
	}
	sections := []string{i18n.T(lang, "server.timeline.feed.heading")}
	posts := func(title string, vs []service.TimelinePostView) {
		if len(vs) == 0 {
			return
		}
		blocks := []string{title}
		for _, v := range vs {
			blocks = append(blocks, timelinePostBlock(lang, v, loc))
		}
		sections = append(sections, strings.Join(blocks, "\n"))
	}
	posts(i18n.T(lang, "server.timeline.feed.unacked_holds"), f.UnackedHolds)
	posts(i18n.T(lang, "server.timeline.feed.unacked_requests"), f.UnackedRequests)
	if showNow {
		sections = append(sections, strings.Join(timelineNowText(lang, f.Now), "\n"))
	}
	if len(f.Rows) > 0 || f.Remaining > 0 || f.Older > 0 {
		blocks := []string{i18n.T(lang, "server.timeline.feed.rows", "count", len(f.Rows))}
		for _, r := range f.Rows {
			blocks = append(blocks, timelineRowBlock(r))
		}
		if f.Older > 0 {
			blocks = append(blocks, i18n.T(lang, "server.timeline.feed.older", "count", f.Older))
		}
		switch {
		case f.Remaining > 0 && mode == feedAdvanced:
			blocks = append(blocks, i18n.T(lang, "server.timeline.feed.remaining_advanced", "count", f.Remaining))
		case f.Remaining > 0 && mode == feedHook:
			blocks = append(blocks, i18n.T(lang, "server.timeline.feed.remaining_hook", "count", f.Remaining))
		case f.Remaining > 0 && mode == feedNoSession:
			blocks = append(blocks, i18n.T(lang, "server.timeline.feed.remaining_no_session", "count", f.Remaining))
		case f.Remaining > 0:
			blocks = append(blocks, i18n.T(lang, "server.timeline.feed.remaining", "count", f.Remaining))
		}
		sections = append(sections, strings.Join(blocks, "\n"))
	}
	posts(i18n.T(lang, "server.timeline.feed.undelivered"), f.Undelivered)
	if hint {
		sections = append(sections, i18n.T(lang, "server.timeline.feed.seen_hint", "seq", f.AdvanceTo))
	}
	return strings.Join(sections, "\n\n")
}

// apiTimelineUnread は GET /projects/{slug}/timeline/unread。
func (s *Server) apiTimelineUnread(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	f, err := s.svc.ReadTimeline(r.Context(), actor(r), r.PathValue("slug"), service.TimelineReadQuery{Limit: max(limit, 0), Now: r.URL.Query().Get("now") == "1"})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	body := timelineFeedJSON(f)
	mode := feedReadOnly
	if r.URL.Query().Get("hook") == "1" { // hook は文脈に入れた後に自分で位置を進めるので、残りは次の場面で届くと書く
		mode = feedHook
	}
	body["text"] = timelineFeedText(reqLang(r), f, s.svc.Loc, false, mode)
	body["timezone"] = s.svc.Loc.String()
	writeJSON(w, http.StatusOK, body)
}

// apiTimelineNow は GET /projects/{slug}/timeline/now。
func (s *Server) apiTimelineNow(w http.ResponseWriter, r *http.Request) {
	n, err := s.svc.TimelineNowOf(r.Context(), actor(r), r.PathValue("slug"))
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	body := timelineNowJSON(n)
	body["text"] = strings.Join(timelineNowText(reqLang(r), n), "\n")
	body["timezone"] = s.svc.Loc.String()
	writeJSON(w, http.StatusOK, body)
}

// apiTimelineSeen は POST /projects/{slug}/timeline/seen。
func (s *Server) apiTimelineSeen(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Seq int64 `json:"seq"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	res, err := s.svc.SeeTimeline(r.Context(), actor(r), r.PathValue("slug"), req.Seq)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"seq": res.Seq, "advanced": res.Advanced})
}

// ---- next と set_status の結果に足す未読と未了解

// timelineStartFeed は着手と区切りの結果に添える未読と未了解。読めないときは nil（操作は拒まない。ログに残すだけ）。
func (s *Server) timelineStartFeed(ctx context.Context, a service.Actor, slug string) *service.TimelineFeed {
	f, err := s.svc.ReadTimeline(ctx, a, slug, service.TimelineReadQuery{})
	if err != nil {
		s.cfg.Logger.Warn("timeline feed", "project", slug, "err", err)
		return nil
	}
	return f
}

// statusFeedApplies は状態の変更の結果に未読と未了解を足すか（In Progress と Done・Canceled への変更）。
func statusFeedApplies(res *service.StatusResult) bool {
	if res.Issue == nil {
		return false
	}
	switch res.Issue.Item.Status {
	case "In Progress", "Done", "Canceled":
		return true
	}
	return false
}

// timelineMCPFeed は MCP の結果に足す文面を作り、結果が AI に届く前提（推定。実機の確認は hook の実装のとき）で位置を進めて受け取りを記録する（REST は位置を動かさない）。
// 足すものが無ければ空。進められなくても結果は返す（ログに残すだけ）。
func (s *Server) timelineMCPFeed(ctx context.Context, lang i18n.Lang, a service.Actor, f *service.TimelineFeed) string {
	if f == nil {
		return ""
	}
	mode := feedAdvanced
	if a.SessionID == "" { // セッションの無い読み手は位置を進めない（ConsumeTimelineFeed）
		mode = feedNoSession
	}
	text := timelineFeedText(lang, f, s.svc.Loc, false, mode)
	if err := s.svc.ConsumeTimelineFeed(ctx, a, f); err != nil {
		s.cfg.Logger.Warn("timeline seen", "project", f.ProjectSlug, "err", err)
	}
	return text
}

// withTimelineText は文面の末尾に空行を挟んで足す（足すものが無ければそのまま）。
func withTimelineText(text, add string) string {
	if add == "" {
		return text
	}
	if text == "" {
		return add
	}
	return strings.TrimRight(text, "\n") + "\n\n" + add
}

// ---- MCP（timeline_unread・timeline_now・timeline_seen）

type (
	timelineUnreadIn struct {
		projectArg
		Limit int  `json:"limit,omitempty" jsonschema:"server.mcp.arg.timeline.limit"`
		Now   bool `json:"now,omitempty" jsonschema:"server.mcp.arg.timeline.now"`
	}
	timelineSeenIn struct {
		projectArg
		Seq int64 `json:"seq" jsonschema:"server.mcp.arg.timeline.seq"`
	}
)

func (s *Server) addTimelineReadMCPTools(srv *mcp.Server, lang i18n.Lang, ro *mcp.ToolAnnotations) {
	notDestructive := false

	addTool(srv, lang, &mcp.Tool{Name: "timeline_unread", Annotations: ro, Description: i18n.T(lang, "server.mcp.tool.timeline_unread")},
		func(ctx context.Context, req *mcp.CallToolRequest, in timelineUnreadIn) (*mcp.CallToolResult, any, error) {
			c, slug, err := s.mcpTimelinePostCall(ctx, req, in.Project, "timeline_unread")
			if err != nil {
				return nil, nil, err
			}
			f, err := s.svc.ReadTimeline(ctx, c.actor, slug, service.TimelineReadQuery{Limit: max(in.Limit, 0), Now: in.Now})
			if err != nil {
				return nil, nil, s.toolError(c.lang, "timeline_unread", err)
			}
			text := timelineFeedText(c.lang, f, s.svc.Loc, true, feedReadOnly)
			if text == "" {
				text = i18n.T(c.lang, "server.mcp.timeline.unread_none", "project", f.ProjectSlug)
			}
			return result(text, timelineFeedJSON(f)), nil, nil
		})

	addTool(srv, lang, &mcp.Tool{Name: "timeline_now", Annotations: ro, Description: i18n.T(lang, "server.mcp.tool.timeline_now")},
		func(ctx context.Context, req *mcp.CallToolRequest, in projectArg) (*mcp.CallToolResult, any, error) {
			c, slug, err := s.mcpTimelinePostCall(ctx, req, in.Project, "timeline_now")
			if err != nil {
				return nil, nil, err
			}
			n, err := s.svc.TimelineNowOf(ctx, c.actor, slug)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "timeline_now", err)
			}
			return result(strings.Join(timelineNowText(c.lang, n), "\n"), timelineNowJSON(n)), nil, nil
		})

	addTool(srv, lang, &mcp.Tool{Name: "timeline_seen", Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive},
		Description: i18n.T(lang, "server.mcp.tool.timeline_seen")},
		func(ctx context.Context, req *mcp.CallToolRequest, in timelineSeenIn) (*mcp.CallToolResult, any, error) {
			c, slug, err := s.mcpTimelinePostCall(ctx, req, in.Project, "timeline_seen")
			if err != nil {
				return nil, nil, err
			}
			res, err := s.svc.SeeTimeline(ctx, c.actor, slug, in.Seq)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "timeline_seen", err)
			}
			return result(i18n.T(c.lang, "server.mcp.timeline.seen", "seq", res.Seq), map[string]any{"seq": res.Seq, "advanced": res.Advanced}), nil, nil
		})
}
