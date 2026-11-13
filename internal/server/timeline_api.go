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

// タイムライン（docs/server/DESIGN.md §9-9）の REST と MCP。種類・宛先・権限・検査・期限・解除・未達と未了解の判定は
// service（timeline.go・timeline_notice.go）にあり、ここでは HTTP と MCP の形だけを決める。人が使う画面は作らない。
//
//	GET  /api/v1/projects/{slug}/timeline?sent=1            自分に効く有効な書き込みの一覧（sent=1 は自分が出した有効なもの）
//	POST /api/v1/projects/{slug}/timeline                   投稿（{kind, to, user, session, to_issue, to_branch, to_machine, body, ttl_seconds, issue, release_issue,
//	                                                        release_lease, machine_label}）
//	GET  /api/v1/projects/{slug}/timeline/pending?limit=N   このセッションの未達と未了解
//	GET  /api/v1/projects/{slug}/timeline/check             軽い確認（通し番号・自分に効く有効な待機の ID・このセッションの読んだ位置と残りがあるか）
//	POST /api/v1/projects/{slug}/timeline/receipts          受け取りの記録（{items: [{id, version}]}。要求のセッションの記録）
//	POST /api/v1/projects/{slug}/timeline/commits           コミットの出来事（{branch, commits: [{sha, subject}], omitted}。hook が送る）
//	GET  /api/v1/projects/{slug}/timeline/unread・now、POST …/seen は timeline_read_api.go
//	GET  /api/v1/projects/{slug}/timeline/{id}              1 件と出来事（宛先と送り手だけ）
//	POST /api/v1/projects/{slug}/timeline/{id}/revise       改訂（{body, ttl_seconds, machine_label}。書いた本人だけ）
//	POST /api/v1/projects/{slug}/timeline/{id}/withdraw     取り下げ（{reason, machine_label}）
//	POST /api/v1/projects/{slug}/timeline/{id}/ack          了解（{version}。要求のセッションの記録）
//	GET  /api/v1/projects/{slug}/timeline/{id}/status       到達の状況

// timelinePostJSON は書き込み 1 件の、読む時点の状態。
type timelinePostJSON struct {
	ID             int64  `json:"id"`
	Project        string `json:"project"`
	Kind           string `json:"kind"`
	To             string `json:"to"`
	ToUser         string `json:"to_user,omitempty"`
	ToSession      string `json:"to_session,omitempty"`
	ToIssue        string `json:"to_issue,omitempty"`
	ToBranch       string `json:"to_branch,omitempty"`
	ToMachine      string `json:"to_machine,omitempty"`
	From           string `json:"from"`
	FromMachine    string `json:"from_machine,omitempty"`
	FromSession    string `json:"from_session,omitempty"`
	Issue          string `json:"issue,omitempty"`
	ReleaseIssue   string `json:"release_issue,omitempty"`
	ReleaseLease   string `json:"release_lease,omitempty"`
	Version        int64  `json:"version"`
	LastVersion    int64  `json:"last_version"`
	Seq            int64  `json:"seq"`
	Body           string `json:"body"`
	Author         string `json:"author"`
	ExpiresAt      string `json:"expires_at,omitempty"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
	State          string `json:"state"`
	WithdrawReason string `json:"withdraw_reason,omitempty"`
	WithdrawnBy    string `json:"withdrawn_by,omitempty"`
	SameUser       bool   `json:"same_user"`
	AppliesToMe    bool   `json:"applies_to_me"`
	Received       int64  `json:"received"`
	Acked          int64  `json:"acked"`
	Undelivered    bool   `json:"undelivered"`
	Unacked        bool   `json:"unacked"`
}

func timelinePostTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func toTimelinePostJSON(v service.TimelinePostView) timelinePostJSON {
	j := timelinePostJSON{ID: v.ID, Project: v.ProjectSlug, Kind: v.Kind, To: v.ToKind, ToUser: v.ToLogin, ToSession: v.ToSession, ToIssue: v.ToIssueDisplay, From: v.SenderLogin,
		FromMachine: v.SenderMachine, FromSession: v.SenderSession, Issue: v.IssueDisplay, ReleaseIssue: v.ReleaseIssueDisplay, ReleaseLease: v.ReleaseLease,
		Version: v.Version, LastVersion: v.LastVersion, Seq: v.LastSeq, Body: v.Body, Author: v.Author, ExpiresAt: timelinePostTime(v.ExpiresAt),
		CreatedAt: timelinePostTime(v.CreatedAt), UpdatedAt: timelinePostTime(v.UpdatedAt), State: v.State, WithdrawReason: v.WithdrawReason, WithdrawnBy: v.WithdrawnBy,
		SameUser: v.SameUser, AppliesToMe: v.AppliesToMe, Received: v.Received, Acked: v.Acked, Undelivered: v.Undelivered(), Unacked: v.Unacked()}
	switch v.ToKind {
	case store.TimelineToBranch:
		j.ToBranch = v.ToLabel
	case store.TimelineToMachine:
		j.ToMachine = v.ToLabel
	}
	return j
}

func timelinePostsJSON(list []service.TimelinePostView) []timelinePostJSON {
	out := make([]timelinePostJSON, 0, len(list))
	for _, v := range list {
		out = append(out, toTimelinePostJSON(v))
	}
	return out
}

// timelinePostEventJSON は出来事 1 件（投稿・改訂・取り下げ）。前の版の本文と書き手はここに残る。
type timelinePostEventJSON struct {
	Action       string `json:"action"`
	Version      int64  `json:"version"`
	Seq          int64  `json:"seq"`
	Body         string `json:"body,omitempty"`
	ExpiresAt    string `json:"expires_at,omitempty"`
	Reason       string `json:"reason,omitempty"`
	User         string `json:"user"`
	MachineLabel string `json:"machine_label,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	Via          string `json:"via"`
	CreatedAt    string `json:"created_at"`
}

func toTimelinePostEventJSON(e store.TimelinePostEvent) timelinePostEventJSON {
	return timelinePostEventJSON{Action: e.Action, Version: e.Version, Seq: e.Seq, Body: e.Body, ExpiresAt: timelinePostTime(e.ExpiresAt), Reason: e.Reason,
		User: e.ActorLogin, MachineLabel: e.ActorMachine, SessionID: e.ActorSession, Via: e.Via, CreatedAt: timelinePostTime(e.CreatedAt)}
}

// timelinePostDeliveryJSON は到達の状況の 1 行。
type timelinePostDeliveryJSON struct {
	User         string `json:"user"`
	MachineLabel string `json:"machine_label,omitempty"`
	Session      string `json:"session"`
	Received     int64  `json:"received"`
	Acked        int64  `json:"acked"`
	LastSeenAt   string `json:"last_seen_at,omitempty"`
	// ResolvedBy は宛先に当たった出どころ（record はサーバの着手の記録、presence は在席の名乗った値。受け取りの記録だけの行は省く）
	ResolvedBy string `json:"resolved_by,omitempty"`
}

func timelinePostDeliveryBody(d *service.TimelinePostDelivery) map[string]any {
	rows := make([]timelinePostDeliveryJSON, 0, len(d.Rows))
	for _, r := range d.Rows {
		rows = append(rows, timelinePostDeliveryJSON{User: r.User, MachineLabel: r.MachineLabel, Session: r.Session, Received: r.Received, Acked: r.Acked,
			LastSeenAt: timelinePostTime(r.LastSeenAt), ResolvedBy: r.ResolvedBy})
	}
	return map[string]any{"post": toTimelinePostJSON(d.TimelinePost), "rows": rows}
}

func timelinePostPendingBody(p *service.TimelinePostPending) map[string]any {
	return map[string]any{"undelivered": timelinePostsJSON(p.Undelivered), "unacked": timelinePostsJSON(p.Unacked), "undelivered_total": p.UndeliveredTotal,
		"unacked_total": p.UnackedTotal, "seq": p.Seq}
}

type timelinePostRequest struct {
	Kind         string `json:"kind,omitempty"`
	To           string `json:"to,omitempty"`
	User         string `json:"user,omitempty"`
	Session      string `json:"session,omitempty"`
	ToIssue      string `json:"to_issue,omitempty"`
	ToBranch     string `json:"to_branch,omitempty"`
	ToMachine    string `json:"to_machine,omitempty"`
	Body         string `json:"body,omitempty"`
	TTLSeconds   int64  `json:"ttl_seconds,omitempty"`
	Issue        string `json:"issue,omitempty"`
	ReleaseIssue string `json:"release_issue,omitempty"`
	ReleaseLease string `json:"release_lease,omitempty"`
	Reason       string `json:"reason,omitempty"`
	Version      int64  `json:"version,omitempty"`
	MachineLabel string `json:"machine_label,omitempty"`
}

// timelinePostID は経路の {id} を読む（読めなければ 400 を書いて false）。
func timelinePostID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_argument", i18n.T(reqLang(r), "server.api.err.timeline_post_id"))
		return 0, false
	}
	return id, true
}

// apiListTimelinePosts は GET /projects/{slug}/timeline。
func (s *Server) apiListTimelinePosts(w http.ResponseWriter, r *http.Request) {
	res, err := s.svc.ListTimelinePosts(r.Context(), actor(r), r.PathValue("slug"), service.TimelinePostListQuery{Sent: r.URL.Query().Get("sent") == "1"})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"posts": timelinePostsJSON(res.TimelinePosts), "count": len(res.TimelinePosts), "seq": res.Seq,
		"can_post": res.CanPost, "user": res.User, "timezone": s.svc.Loc.String()})
}

// apiPostTimelinePost は POST /projects/{slug}/timeline。
func (s *Server) apiPostTimelinePost(w http.ResponseWriter, r *http.Request) {
	var req timelinePostRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	v, err := s.svc.PostTimelinePost(r.Context(), actor(r), service.TimelinePostInput{Project: r.PathValue("slug"), Kind: req.Kind, To: req.To, User: req.User,
		Session: req.Session, ToIssue: req.ToIssue, ToBranch: req.ToBranch, ToMachine: req.ToMachine, Body: req.Body, TTLSeconds: req.TTLSeconds, Issue: req.Issue, ReleaseIssue: req.ReleaseIssue, ReleaseLease: req.ReleaseLease,
		MachineLabel: req.MachineLabel})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"post": toTimelinePostJSON(*v), "timezone": s.svc.Loc.String()})
}

// apiTimelinePostPending は GET /projects/{slug}/timeline/pending。
func (s *Server) apiTimelinePostPending(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	res, err := s.svc.PendingTimelinePosts(r.Context(), actor(r), r.PathValue("slug"), max(limit, 0))
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	body := timelinePostPendingBody(res)
	body["timezone"] = s.svc.Loc.String()
	writeJSON(w, http.StatusOK, body)
}

// apiTimelinePostCheck は GET /projects/{slug}/timeline/check。
func (s *Server) apiTimelinePostCheck(w http.ResponseWriter, r *http.Request) {
	res, err := s.svc.CheckTimelinePosts(r.Context(), actor(r), r.PathValue("slug"))
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"seq": res.Seq, "holds": res.Holds, "read_seq": res.ReadSeq, "has_position": res.HasPosition, "behind": res.Behind})
}

// apiTimelinePostReceipts は POST /projects/{slug}/timeline/receipts。
func (s *Server) apiTimelinePostReceipts(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Items []struct {
			ID      int64 `json:"id"`
			Version int64 `json:"version"`
		} `json:"items"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	items := make([]service.TimelinePostReceiptInput, 0, len(req.Items))
	for _, it := range req.Items {
		items = append(items, service.TimelinePostReceiptInput{ID: it.ID, Version: it.Version})
	}
	n, err := s.svc.RecordTimelinePostReceipts(r.Context(), actor(r), r.PathValue("slug"), items)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recorded": n})
}

// apiTimelineCommits は POST /projects/{slug}/timeline/commits。
func (s *Server) apiTimelineCommits(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Branch  string `json:"branch"`
		Commits []struct {
			SHA     string `json:"sha"`
			Subject string `json:"subject"`
		} `json:"commits"`
		Omitted int `json:"omitted"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	in := service.TimelineCommitsInput{Project: r.PathValue("slug"), Branch: req.Branch, Omitted: req.Omitted}
	for _, c := range req.Commits {
		in.Commits = append(in.Commits, service.TimelineCommit{SHA: c.SHA, Subject: c.Subject})
	}
	res, err := s.svc.RecordTimelineCommits(r.Context(), actor(r), in)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recorded": res.Recorded, "omitted": res.Omitted, "withheld": res.Withheld})
}

// apiGetTimelinePost は GET /projects/{slug}/timeline/{id}。
func (s *Server) apiGetTimelinePost(w http.ResponseWriter, r *http.Request) {
	id, ok := timelinePostID(w, r)
	if !ok {
		return
	}
	d, err := s.svc.GetTimelinePost(r.Context(), actor(r), r.PathValue("slug"), id)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	events := make([]timelinePostEventJSON, 0, len(d.Events))
	for _, e := range d.Events {
		events = append(events, toTimelinePostEventJSON(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{"post": toTimelinePostJSON(d.TimelinePostView), "events": events, "timezone": s.svc.Loc.String()})
}

// apiReviseTimelinePost は POST /projects/{slug}/timeline/{id}/revise。
func (s *Server) apiReviseTimelinePost(w http.ResponseWriter, r *http.Request) {
	s.timelinePostWrite(w, r, func(ctx context.Context, a service.Actor, slug string, id int64, req timelinePostRequest) (*service.TimelinePostView, error) {
		return s.svc.ReviseTimelinePost(ctx, a, service.TimelinePostReviseInput{Project: slug, ID: id, Body: req.Body, TTLSeconds: req.TTLSeconds, MachineLabel: req.MachineLabel})
	})
}

// apiWithdrawTimelinePost は POST /projects/{slug}/timeline/{id}/withdraw。
func (s *Server) apiWithdrawTimelinePost(w http.ResponseWriter, r *http.Request) {
	s.timelinePostWrite(w, r, func(ctx context.Context, a service.Actor, slug string, id int64, req timelinePostRequest) (*service.TimelinePostView, error) {
		return s.svc.WithdrawTimelinePost(ctx, a, service.TimelinePostWithdrawInput{Project: slug, ID: id, Reason: req.Reason, MachineLabel: req.MachineLabel})
	})
}

// apiAckTimelinePost は POST /projects/{slug}/timeline/{id}/ack。
func (s *Server) apiAckTimelinePost(w http.ResponseWriter, r *http.Request) {
	s.timelinePostWrite(w, r, func(ctx context.Context, a service.Actor, slug string, id int64, req timelinePostRequest) (*service.TimelinePostView, error) {
		return s.svc.AckTimelinePost(ctx, a, service.TimelinePostAckInput{Project: slug, ID: id, Version: req.Version})
	})
}

// timelinePostWrite は 1 件への書き込み（改訂・取り下げ・了解）の共通部分。本文は任意（空なら既定の値）。
func (s *Server) timelinePostWrite(w http.ResponseWriter, r *http.Request,
	fn func(ctx context.Context, a service.Actor, slug string, id int64, req timelinePostRequest) (*service.TimelinePostView, error)) {
	id, ok := timelinePostID(w, r)
	if !ok {
		return
	}
	var req timelinePostRequest
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	v, err := fn(r.Context(), actor(r), r.PathValue("slug"), id, req)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"post": toTimelinePostJSON(*v), "timezone": s.svc.Loc.String()})
}

// apiTimelinePostStatus は GET /projects/{slug}/timeline/{id}/status。
func (s *Server) apiTimelinePostStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := timelinePostID(w, r)
	if !ok {
		return
	}
	d, err := s.svc.TimelinePostDeliveryStatus(r.Context(), actor(r), r.PathValue("slug"), id)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	body := timelinePostDeliveryBody(d)
	body["timezone"] = s.svc.Loc.String()
	writeJSON(w, http.StatusOK, body)
}

// ---- summary と着手の知らせ

// timelineCounts は summary に添える件数。数えられなくても summary 全体は止めない（0 にしてログに残す。unread_messages と同じ）。
func (s *Server) timelineCounts(ctx context.Context, a service.Actor, slug string) service.TimelineCounts {
	n, err := s.svc.TimelineSummaryCounts(ctx, a, slug)
	if err != nil {
		s.cfg.Logger.Warn("timeline counts", "project", slug, "err", err)
		return service.TimelineCounts{}
	}
	return n
}

func timelineCountsJSON(n service.TimelineCounts) map[string]any {
	return map[string]any{"active": n.Active, "unacked": n.Unacked, "holds": n.Holds}
}

// timelineCountsLine は summary の 1 行（1 件も無ければ空）。
func timelineCountsLine(lang i18n.Lang, n service.TimelineCounts) string {
	if !n.Any() {
		return ""
	}
	return i18n.T(lang, "server.mcp.summary.timeline", "active", n.Active, "unacked", n.Unacked, "holds", n.Holds)
}

// startHoldNotice は着手（next と In Progress への変更）の結果に添える待機の知らせ（1 行）と、その ID。
type startHoldNotice struct {
	line string
	ids  []int64
}

// startHolds はこのセッションに効く有効な待機を引いて知らせを作る。待機が無い・確かめられないときは空
// （着手は拒まない。確かめられないことはログに残すだけ）。
func (s *Server) startHolds(ctx context.Context, lang i18n.Lang, a service.Actor, slug string) startHoldNotice {
	holds, err := s.svc.StartTimelinePostHolds(ctx, a, slug)
	if err != nil {
		s.cfg.Logger.Warn("timeline holds", "project", slug, "err", err)
		return startHoldNotice{}
	}
	if len(holds) == 0 {
		return startHoldNotice{}
	}
	items := make([]string, 0, len(holds))
	ids := make([]int64, 0, len(holds))
	for _, v := range holds {
		items = append(items, fmt.Sprintf("#%d %s", v.ID, timelinePostSender(lang, v)))
		ids = append(ids, v.ID)
	}
	return startHoldNotice{line: i18n.T(lang, "server.timeline.start_holds", "items", strings.Join(items, ", ")), ids: ids}
}

// nextHolds は next の結果に待機の知らせを足す（着手したか着手するイシューがあるときだけ。REST と MCP で同じ）。
func (s *Server) nextHolds(ctx context.Context, lang i18n.Lang, a service.Actor, pr store.Project, v *nextJSON) {
	if v.Issue == nil {
		return
	}
	n := s.startHolds(ctx, lang, a, pr.Slug)
	if n.line == "" {
		return
	}
	if v.Text != "" && !strings.HasSuffix(v.Text, "\n") {
		v.Text += "\n"
	}
	v.Text += n.line
	v.TimelinePostHolds = n.ids
}

// statusHolds は状態の変更の結果に添える待機の知らせ（In Progress にしたときだけ）。
func (s *Server) statusHolds(ctx context.Context, lang i18n.Lang, a service.Actor, pr store.Project, res *service.StatusResult) startHoldNotice {
	if res.Issue == nil || res.Issue.Item.Status != "In Progress" {
		return startHoldNotice{}
	}
	return s.startHolds(ctx, lang, a, pr.Slug)
}

// withStartHolds は状態の変更の出力行に待機の知らせを足し、body に ID を載せる（messages に入れるので古い CLI でも表示される）。
func withStartHolds(messages []string, body map[string]any, n startHoldNotice) []string {
	if n.line == "" {
		return messages
	}
	body["timeline_holds"] = n.ids
	return append(messages, n.line)
}

// timelinePostWho は同じ利用者が出したかの表示（サーバが認証した利用者で決めた値）。
func timelinePostWho(lang i18n.Lang, v service.TimelinePostView) string {
	if v.SameUser {
		return i18n.T(lang, "server.timeline.same_user")
	}
	return i18n.T(lang, "server.timeline.other_user")
}

// timelinePostSender は出した利用者と、同じ利用者かの表示。
func timelinePostSender(lang i18n.Lang, v service.TimelinePostView) string {
	return i18n.T(lang, "server.timeline.sender", "login", v.SenderLogin, "who", timelinePostWho(lang, v))
}

// ---- MCP の文面

// timelinePostTarget は宛先の表示。
func timelinePostTarget(lang i18n.Lang, v service.TimelinePostView) string {
	switch v.ToKind {
	case store.MessageToUser:
		return v.ToLogin
	case store.MessageToSession:
		return v.ToLogin + " / " + timelinePostClaim(v.ToSession)
	case store.TimelineToIssue:
		return i18n.T(lang, "server.timeline.to_issue", "id", v.ToIssueDisplay)
	case store.TimelineToBranch:
		return i18n.T(lang, "server.timeline.to_branch", "name", timelinePostClaim(v.ToLabel))
	case store.TimelineToMachine:
		return i18n.T(lang, "server.timeline.to_machine", "label", timelinePostClaim(v.ToLabel))
	case store.TimelineToOthers:
		return i18n.T(lang, "server.timeline.to_others")
	}
	return i18n.T(lang, "server.timeline.to_project")
}

// timelinePostHeading は書き込み 1 件の見出しの行（ID・版・種類・出した利用者と同じ利用者か・端末・宛先・期限・添えたイシュー・状態の印）。
// 端末のラベルとセッションは参加者が名乗った値なのでバッククォートで囲む。
func timelinePostHeading(lang i18n.Lang, v service.TimelinePostView, loc *time.Location) string {
	parts := []string{fmt.Sprintf("#%d v%d", v.ID, v.Version), v.Kind, timelinePostSender(lang, v), timelinePostClaim(v.SenderMachine),
		"-> " + timelinePostTarget(lang, v)}
	if !v.ExpiresAt.IsZero() {
		parts = append(parts, i18n.T(lang, "server.timeline.expires", "at", v.ExpiresAt.In(loc).Format("2006-01-02 15:04")))
	}
	if v.IssueDisplay != "" {
		parts = append(parts, v.IssueDisplay)
	}
	if v.State != service.TimelinePostActive {
		parts = append(parts, "["+v.State+"]")
	}
	if v.Undelivered() {
		parts = append(parts, i18n.T(lang, "server.timeline.mark_undelivered"))
	}
	if v.Unacked() {
		parts = append(parts, i18n.T(lang, "server.timeline.mark_unacked"))
	}
	return strings.Join(parts, "  ")
}

// timelinePostClaim は参加者が名乗った値（端末のラベル・セッション）をバッククォートで囲む。端末のラベルと宛先・受け取りのセッションは
// バッククォートを拒まないので、入っていると囲みを抜けて地の文に見える。その値は載せずに ? を出す（値そのものは構造化の結果にある）。
func timelinePostClaim(v string) string {
	if strings.Contains(v, "`") {
		return "?"
	}
	return presenceClaim(v)
}

// quoteBody は本文を行ごとの引用にする（連絡の read_inbox と同じ形。本文に見出しと同じ形の行があっても地の文にならない）。
func quoteBody(body string) string {
	return "> " + strings.ReplaceAll(strings.TrimRight(strings.ReplaceAll(body, "\r\n", "\n"), "\n"), "\n", "\n> ")
}

// timelinePostBlock は見出しと引用の本文。取り下げ済みは理由を本文の代わりに出す。
func timelinePostBlock(lang i18n.Lang, v service.TimelinePostView, loc *time.Location) string {
	if v.State == service.TimelinePostWithdrawn {
		return timelinePostHeading(lang, v, loc) + "\n" + i18n.T(lang, "server.timeline.withdrawn", "by", v.WithdrawnBy) + "\n" + quoteBody(orDashText(v.WithdrawReason))
	}
	return timelinePostHeading(lang, v, loc) + "\n" + quoteBody(v.Body)
}

// ---- MCP（timeline_post・timeline_list・timeline_revise・timeline_withdraw・timeline_ack・timeline_status）

type (
	timelinePostIDArg struct {
		projectArg
		ID int64 `json:"id" jsonschema:"server.mcp.arg.timeline.id"`
	}
	postTimelinePostIn struct {
		projectArg
		Kind         string `json:"kind" jsonschema:"server.mcp.arg.timeline.kind"`
		To           string `json:"to" jsonschema:"server.mcp.arg.timeline.to"`
		User         string `json:"user,omitempty" jsonschema:"server.mcp.arg.timeline.user"`
		Session      string `json:"session,omitempty" jsonschema:"server.mcp.arg.timeline.session"`
		ToIssue      string `json:"to_issue,omitempty" jsonschema:"server.mcp.arg.timeline.to_issue"`
		ToBranch     string `json:"to_branch,omitempty" jsonschema:"server.mcp.arg.timeline.to_branch"`
		ToMachine    string `json:"to_machine,omitempty" jsonschema:"server.mcp.arg.timeline.to_machine"`
		Body         string `json:"body" jsonschema:"server.mcp.arg.timeline.body"`
		TTLSeconds   int64  `json:"ttl_seconds,omitempty" jsonschema:"server.mcp.arg.timeline.ttl_seconds"`
		Issue        string `json:"issue,omitempty" jsonschema:"server.mcp.arg.timeline.issue"`
		ReleaseIssue string `json:"release_issue,omitempty" jsonschema:"server.mcp.arg.timeline.release_issue"`
		ReleaseLease string `json:"release_lease,omitempty" jsonschema:"server.mcp.arg.timeline.release_lease"`
		MachineLabel string `json:"machine_label,omitempty" jsonschema:"server.mcp.arg.timeline.machine_label"`
	}
	listTimelinePostsIn struct {
		projectArg
		Sent         bool `json:"sent,omitempty" jsonschema:"server.mcp.arg.timeline.sent"`
		MarkReceived bool `json:"mark_received,omitempty" jsonschema:"server.mcp.arg.timeline.mark_received"`
	}
	reviseTimelinePostIn struct {
		timelinePostIDArg
		Body         string `json:"body,omitempty" jsonschema:"server.mcp.arg.timeline.revise_body"`
		TTLSeconds   int64  `json:"ttl_seconds,omitempty" jsonschema:"server.mcp.arg.timeline.revise_ttl_seconds"`
		MachineLabel string `json:"machine_label,omitempty" jsonschema:"server.mcp.arg.timeline.machine_label"`
	}
	withdrawTimelinePostIn struct {
		timelinePostIDArg
		Reason       string `json:"reason,omitempty" jsonschema:"server.mcp.arg.timeline.reason"`
		MachineLabel string `json:"machine_label,omitempty" jsonschema:"server.mcp.arg.timeline.machine_label"`
	}
	ackTimelinePostIn struct {
		timelinePostIDArg
		Version int64 `json:"version" jsonschema:"server.mcp.arg.timeline.version"`
	}
)

// mcpTimelinePostCall は MCP の呼び出しの主体と対象プロジェクトを決める。
func (s *Server) mcpTimelinePostCall(ctx context.Context, req *mcp.CallToolRequest, project, tool string) (*mcpCall, string, error) {
	c, err := s.mcpCallOf(req)
	if err != nil {
		return nil, "", err
	}
	slug, err := s.projectSlug(ctx, c, project)
	if err != nil {
		return nil, "", s.toolError(c.lang, tool, err)
	}
	return c, slug, nil
}

func (s *Server) addTimelinePostMCPTools(srv *mcp.Server, lang i18n.Lang, ro *mcp.ToolAnnotations) {
	notDestructive := false

	addTool(srv, lang, &mcp.Tool{Name: "timeline_post", Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive},
		Description: i18n.T(lang, "server.mcp.tool.timeline_post")},
		func(ctx context.Context, req *mcp.CallToolRequest, in postTimelinePostIn) (*mcp.CallToolResult, any, error) {
			c, slug, err := s.mcpTimelinePostCall(ctx, req, in.Project, "timeline_post")
			if err != nil {
				return nil, nil, err
			}
			v, err := s.svc.PostTimelinePost(ctx, c.actor, service.TimelinePostInput{Project: slug, Kind: in.Kind, To: in.To, User: in.User, Session: in.Session,
				ToIssue: in.ToIssue, ToBranch: in.ToBranch, ToMachine: in.ToMachine, Body: in.Body, TTLSeconds: in.TTLSeconds, Issue: in.Issue, ReleaseIssue: in.ReleaseIssue, ReleaseLease: in.ReleaseLease, MachineLabel: in.MachineLabel})
			if err != nil {
				return nil, nil, s.toolError(c.lang, "timeline_post", err)
			}
			return result(i18n.T(c.lang, "server.mcp.timeline.posted", "id", v.ID, "kind", v.Kind)+"\n"+timelinePostHeading(c.lang, *v, s.svc.Loc),
				map[string]any{"post": toTimelinePostJSON(*v)}), nil, nil
		})

	addTool(srv, lang, &mcp.Tool{Name: "timeline_list", Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive}, Description: i18n.T(lang, "server.mcp.tool.timeline_list")},
		func(ctx context.Context, req *mcp.CallToolRequest, in listTimelinePostsIn) (*mcp.CallToolResult, any, error) {
			c, slug, err := s.mcpTimelinePostCall(ctx, req, in.Project, "timeline_list")
			if err != nil {
				return nil, nil, err
			}
			list, err := s.svc.ListTimelinePosts(ctx, c.actor, slug, service.TimelinePostListQuery{Sent: in.Sent})
			if err != nil {
				return nil, nil, s.toolError(c.lang, "timeline_list", err)
			}
			shown := list.TimelinePosts
			pending := &service.TimelinePostPending{Undelivered: []service.TimelinePostView{}, Unacked: []service.TimelinePostView{}}
			if !in.Sent {
				if pending, err = s.svc.PendingTimelinePosts(ctx, c.actor, slug, 0); err != nil {
					return nil, nil, s.toolError(c.lang, "timeline_list", err)
				}
				for _, v := range pending.Undelivered { // 取り下げの知らせ（前の版を受け取った書き込みの取り下げ）は一覧の外なので足す
					if !v.Active() {
						shown = append(shown, v)
					}
				}
			}
			blocks := make([]string, 0, len(shown))
			for _, v := range shown {
				blocks = append(blocks, timelinePostBlock(c.lang, v, s.svc.Loc))
			}
			var text string
			if len(blocks) == 0 {
				text = i18n.T(c.lang, "server.mcp.timeline.none", "project", list.ProjectSlug)
			} else {
				text = i18n.T(c.lang, "server.mcp.timeline.heading") + "\n\n" + strings.Join(blocks, "\n\n")
			}
			if in.MarkReceived && !in.Sent && len(shown) > 0 {
				items := make([]service.TimelinePostReceiptInput, 0, len(shown))
				for _, v := range shown {
					items = append(items, service.TimelinePostReceiptInput{ID: v.ID, Version: v.LastVersion})
				}
				if _, err := s.svc.RecordTimelinePostReceipts(ctx, c.actor, slug, items); err != nil {
					return nil, nil, s.toolError(c.lang, "timeline_list", err)
				}
			}
			return result(text, map[string]any{"posts": timelinePostsJSON(shown), "count": len(shown), "seq": list.Seq,
				"undelivered_total": pending.UndeliveredTotal, "unacked_total": pending.UnackedTotal}), nil, nil
		})

	addTool(srv, lang, &mcp.Tool{Name: "timeline_revise", Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive},
		Description: i18n.T(lang, "server.mcp.tool.timeline_revise")},
		func(ctx context.Context, req *mcp.CallToolRequest, in reviseTimelinePostIn) (*mcp.CallToolResult, any, error) {
			c, slug, err := s.mcpTimelinePostCall(ctx, req, in.Project, "timeline_revise")
			if err != nil {
				return nil, nil, err
			}
			v, err := s.svc.ReviseTimelinePost(ctx, c.actor, service.TimelinePostReviseInput{Project: slug, ID: in.ID, Body: in.Body, TTLSeconds: in.TTLSeconds,
				MachineLabel: in.MachineLabel})
			if err != nil {
				return nil, nil, s.toolError(c.lang, "timeline_revise", err)
			}
			return result(i18n.T(c.lang, "server.mcp.timeline.revised", "id", v.ID, "version", v.Version)+"\n"+timelinePostHeading(c.lang, *v, s.svc.Loc),
				map[string]any{"post": toTimelinePostJSON(*v)}), nil, nil
		})

	addTool(srv, lang, &mcp.Tool{Name: "timeline_withdraw", Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive},
		Description: i18n.T(lang, "server.mcp.tool.timeline_withdraw")},
		func(ctx context.Context, req *mcp.CallToolRequest, in withdrawTimelinePostIn) (*mcp.CallToolResult, any, error) {
			c, slug, err := s.mcpTimelinePostCall(ctx, req, in.Project, "timeline_withdraw")
			if err != nil {
				return nil, nil, err
			}
			v, err := s.svc.WithdrawTimelinePost(ctx, c.actor, service.TimelinePostWithdrawInput{Project: slug, ID: in.ID, Reason: in.Reason, MachineLabel: in.MachineLabel})
			if err != nil {
				return nil, nil, s.toolError(c.lang, "timeline_withdraw", err)
			}
			return result(i18n.T(c.lang, "server.mcp.timeline.withdrawn", "id", v.ID), map[string]any{"post": toTimelinePostJSON(*v)}), nil, nil
		})

	addTool(srv, lang, &mcp.Tool{Name: "timeline_ack", Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive}, Description: i18n.T(lang, "server.mcp.tool.timeline_ack")},
		func(ctx context.Context, req *mcp.CallToolRequest, in ackTimelinePostIn) (*mcp.CallToolResult, any, error) {
			c, slug, err := s.mcpTimelinePostCall(ctx, req, in.Project, "timeline_ack")
			if err != nil {
				return nil, nil, err
			}
			v, err := s.svc.AckTimelinePost(ctx, c.actor, service.TimelinePostAckInput{Project: slug, ID: in.ID, Version: in.Version})
			if err != nil {
				return nil, nil, s.toolError(c.lang, "timeline_ack", err)
			}
			return result(i18n.T(c.lang, "server.mcp.timeline.acked", "id", v.ID, "version", v.Version), map[string]any{"post": toTimelinePostJSON(*v)}), nil, nil
		})

	addTool(srv, lang, &mcp.Tool{Name: "timeline_status", Annotations: ro, Description: i18n.T(lang, "server.mcp.tool.timeline_status")},
		func(ctx context.Context, req *mcp.CallToolRequest, in timelinePostIDArg) (*mcp.CallToolResult, any, error) {
			c, slug, err := s.mcpTimelinePostCall(ctx, req, in.Project, "timeline_status")
			if err != nil {
				return nil, nil, err
			}
			d, err := s.svc.TimelinePostDeliveryStatus(ctx, c.actor, slug, in.ID)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "timeline_status", err)
			}
			lines := []string{timelinePostHeading(c.lang, d.TimelinePost, s.svc.Loc), i18n.T(c.lang, "server.mcp.timeline.status_heading")}
			if len(d.Rows) == 0 {
				lines = append(lines, i18n.T(c.lang, "server.mcp.timeline.status_none"))
			}
			for _, r := range d.Rows {
				seen := "-"
				if !r.LastSeenAt.IsZero() {
					seen = r.LastSeenAt.In(s.svc.Loc).Format("2006-01-02 15:04")
				}
				lines = append(lines, strings.Join([]string{r.User, timelinePostClaim(r.MachineLabel), timelinePostClaim(r.Session),
					strconv.FormatInt(r.Received, 10), strconv.FormatInt(r.Acked, 10), seen, orDashText(r.ResolvedBy)}, "  "))
			}
			return result(strings.Join(lines, "\n"), timelinePostDeliveryBody(d)), nil, nil
		})
}
