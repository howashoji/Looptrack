package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

// REST API（DESIGN.md §4）。CLI の各サブコマンドが使う。

const maxRequestBody = 2 << 20 // 最大のイシューは 83KB

type issueJSON struct {
	ID        string         `json:"id"`
	Project   string         `json:"project"`
	Title     string         `json:"title"`
	Type      string         `json:"type"`
	Status    string         `json:"status"`
	Priority  string         `json:"priority"`
	Parent    string         `json:"parent"`
	Labels    []string       `json:"labels"`
	BlockedBy []string       `json:"blocked_by"`
	Traces    []string       `json:"traces"`
	Refs      []string       `json:"refs"`
	Created   string         `json:"created"`
	Updated   string         `json:"updated"`
	Closed    bool           `json:"closed"`
	Version   int            `json:"version"`
	FileName  string         `json:"file_name"`
	Extra     map[string]any `json:"extra,omitempty"`
	// Assignee は担当者の login、AssigneeInactive は担当が今そのプロジェクトで変更できない印。
	// 未設定なら出さない（ファイルモードの --json と同じキーのまま）
	Assignee         string `json:"assignee,omitempty"`
	AssigneeInactive bool   `json:"assignee_inactive,omitempty"`
	// CrossPathSession は「別の経路（CLI / MCP）で In Progress にした」印。同じセッションかは判定できない
	// （サーバに CLI のセッション ID と MCP の接続 ID を結ぶ情報が無い）ので、黙って自分のものとして扱わず示す
	CrossPathSession bool `json:"cross_path_session,omitempty"`
	// OtherSession は「別のセッションがこのイシューを In Progress にした」印（summary の in_progress だけに付ける）。
	// 同じ作業ツリーで複数の AI のセッションが動くとき、着手済みのものを取りに行かせないため。
	OtherSession bool `json:"other_session,omitempty"`
	// StartedAgo は In Progress にしてから経った時間（"3時間" の形。同上）。長いものは放置の目印になる
	StartedAgo string `json:"started_ago,omitempty"`
	// FeedbackPending は未応答のフィードバックの件数（has_feedback の一覧だけに付ける）
	FeedbackPending int `json:"feedback_pending,omitempty"`
}

type commentJSON struct {
	Seq     int    `json:"seq"`
	TS      string `json:"ts"`
	Content string `json:"content"`
}

type issueDetailJSON struct {
	issueJSON
	Body     string        `json:"body"`
	Preamble *string       `json:"preamble"`
	Comments []commentJSON `json:"comments"`
	Markdown string        `json:"markdown"`
	// UsageNotice は変更の応答だけに載る、トークン情報の付与の指示（service.UsageNotice）
	UsageNotice string `json:"usage_notice,omitempty"`
}

var knownKeys = map[string]bool{"id": true, "title": true, "type": true, "status": true, "priority": true, "parent": true,
	"labels": true, "blocked_by": true, "traces": true, "refs": true, "created": true, "updated": true, "assignee": true}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func toIssueJSON(it *service.Issue) issueJSON {
	x := it.Item
	out := issueJSON{ID: x.ID, Project: it.Project.Slug, Title: x.Title, Type: x.Type, Status: x.Status, Priority: x.Priority,
		Parent: x.Parent, Labels: nonNil(x.Labels), BlockedBy: nonNil(x.BlockedBy), Traces: nonNil(x.Traces), Refs: nonNil(x.Refs),
		Created: x.Created, Updated: x.Updated, Closed: x.IsClosed(), Version: it.Row.Version, FileName: it.Row.FileName,
		Assignee: it.Row.Assignee.Login, AssigneeInactive: it.Row.Assignee.Inactive}
	for _, f := range it.Row.Doc.Front {
		if knownKeys[f.Key] {
			continue
		}
		if out.Extra == nil {
			out.Extra = map[string]any{}
		}
		if f.IsList {
			out.Extra[f.Key] = nonNil(f.List)
		} else {
			out.Extra[f.Key] = f.Value
		}
	}
	return out
}

func toDetailJSON(it *service.Issue) issueDetailJSON {
	d := it.Row.Doc
	out := issueDetailJSON{issueJSON: toIssueJSON(it), Body: d.BodyMain, Preamble: d.Preamble, Comments: []commentJSON{}, Markdown: it.Markdown}
	for i, c := range d.Comments {
		out.Comments = append(out.Comments, commentJSON{Seq: i + 1, TS: c.TS, Content: c.Content})
	}
	return out
}

// actor は認証情報から変更の主体を作る。CLI は X-Looptrack-Client: cli、コーディング AI のセッションは X-Looptrack-Session、
// セッション ID で分からない AI（Copilot）は X-Looptrack-Agent で伝える。
func actor(r *http.Request) service.Actor {
	p := principalFrom(r.Context())
	a := service.Actor{UserID: p.User.ID, TokenID: p.TokenID, Via: "web", Lang: reqLang(r)}
	if p.Via == "api" {
		a.Via = "api"
		if strings.EqualFold(r.Header.Get("X-Looptrack-Client"), "cli") {
			a.Via = "cli"
		}
	}
	if sid := strings.TrimSpace(r.Header.Get("X-Looptrack-Session")); sid != "" && len(sid) <= 128 {
		a.SessionID = sid
	}
	// X-Looptrack-Agent: CLI を動かしている AI を CLI が環境変数から判定して送る（今は GitHub Copilot だけ）。
	// VS Code の Copilot はセッション ID を渡さないので、これが「セッション ID なしの AI の操作」の目印になる
	// （issue_events.detail の "agent"。人が端末から打った操作には付かない）。知らない値は読み捨てる。
	if ag := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Looptrack-Agent"))); ag != "" && ag != agentOther && contains(agentKinds, ag) {
		a.Agent = ag
	}
	// X-Looptrack-Session-Kind: セッション ID の種類。host は器（デスクトップ版の窓）の ID で、会話記録と結び付かない
	// （トークン情報を付けられないので付与の対象から外す。§9-5）。知らない値は読み捨てる＝今までどおり会話のセッション ID として扱う
	// （印を送らない古い CLI と同じ判定になる）。
	if a.SessionID != "" && strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Looptrack-Session-Kind")), service.SessionKindHost) {
		a.SessionKind = service.SessionKindHost
	}
	return a
}

// serviceError はサービスのエラーを HTTP の応答にする。
func (s *Server) serviceError(w http.ResponseWriter, r *http.Request, err error) {
	var se *service.Error
	if !errors.As(err, &se) {
		s.internalError(w, r, err)
		return
	}
	status := map[service.Kind]int{
		service.Invalid: http.StatusBadRequest, service.NotFound: http.StatusNotFound, service.Forbidden: http.StatusForbidden,
		service.Conflict: http.StatusConflict, service.Rejected: http.StatusUnprocessableEntity,
		service.PreconditionRequired: http.StatusPreconditionRequired,
	}[se.Kind]
	if status == 0 {
		status = http.StatusInternalServerError
	}
	var body struct {
		Error struct {
			Code        string           `json:"code"`
			Message     string           `json:"message"`
			Rule        string           `json:"rule,omitempty"`
			Overridable bool             `json:"overridable,omitempty"`
			Current     *issueDetailJSON `json:"current,omitempty"`
		} `json:"error"`
	}
	// 文面は要求ごとの言語で作る（se.Message は日本語。i18n.Text が Unwrap の先の ID から訳す）。
	body.Error.Code, body.Error.Message, body.Error.Rule, body.Error.Overridable = se.Code, i18n.Text(reqLang(r), se), se.Rule, se.Overridable
	if se.Current != nil {
		cur := toDetailJSON(se.Current)
		body.Error.Current = &cur
	}
	writeJSON(w, status, body)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", i18n.T(reqLang(r), "server.api.err.invalid_json", "reason", err.Error()))
		return false
	}
	return true
}

// resolveIssue は ID のイシューを、権限を確かめて引く。slug があればそのプロジェクト内だけを探す
// （ファイルモードの CLI はプロジェクト内だけを探すため）。権限の無いプロジェクトのイシューは「見つからない」。
// write なら変更の権限（editor 以上）も確かめる。
func (s *Server) resolveIssue(ctx context.Context, lang i18n.Lang, u store.User, id, slug string, write bool) (store.Project, store.IssueRow, error) {
	projects, roles, err := store.AccessibleProjects(ctx, s.db, u)
	if err != nil {
		return store.Project{}, store.IssueRow{}, err
	}
	byID := map[int64]store.Project{}
	var scope int64
	for _, pr := range projects {
		byID[pr.ID] = pr
		if pr.Slug == slug {
			scope = pr.ID
		}
	}
	if slug != "" && scope == 0 {
		return store.Project{}, store.IssueRow{}, &service.Error{Kind: service.NotFound, Code: "not_found", Message: i18n.T(lang, "server.api.err.project_not_found", "project", slug)}
	}
	row, err := s.svc.Locate(ctx, id, scope)
	if err == nil {
		if _, ok := byID[row.ProjectID]; !ok {
			err = &service.Error{Kind: service.NotFound, Code: "not_found", Message: i18n.T(lang, "server.api.err.issue_not_found", "id", strings.ToUpper(id))}
		}
	}
	if err != nil {
		return store.Project{}, store.IssueRow{}, err
	}
	pr := byID[row.ProjectID]
	if write && !canWrite(roles[pr.ID]) {
		return store.Project{}, store.IssueRow{}, forbidden(lang, pr.Slug, i18n.T(lang, "server.api.err.what_update"))
	}
	return pr, row, nil
}

// forbidden は閲覧のみの権限で変更しようとしたときのエラー。what は訳し終えた語を渡す
// （ID を変数で渡すと、訳の抜けを見つけるテストが拾えない）。
func forbidden(lang i18n.Lang, slug, what string) *service.Error {
	return &service.Error{Kind: service.Forbidden, Code: "forbidden",
		Message: i18n.T(lang, "server.api.err.forbidden", "project", slug, "what", what)}
}

// issueFor は resolveIssue の HTTP 版（?project=<slug>。エラーは応答に書く）。
func (s *Server) issueFor(w http.ResponseWriter, r *http.Request, write bool) (store.Project, store.IssueRow, bool) {
	pr, row, err := s.resolveIssue(r.Context(), reqLang(r), principalFrom(r.Context()).User, r.PathValue("id"), r.URL.Query().Get("project"), write)
	if err != nil {
		s.serviceError(w, r, err)
		return store.Project{}, store.IssueRow{}, false
	}
	return pr, row, true
}

func canWrite(role string) bool { return role == "editor" || role == "admin" }

func (s *Server) stamp() string { return s.svc.Now().In(s.svc.Loc).Format("2006-01-02 15:04") }

// --- プロジェクト

type projectSummaryJSON struct {
	projectJSON
	Counter int             `json:"counter"`
	Rules   json.RawMessage `json:"rules"`
	Counts  *countsJSON     `json:"counts,omitempty"`
}

type countsJSON struct {
	Total      int            `json:"total"`
	Closed     int            `json:"closed"`
	Updated    string         `json:"updated"`
	Open       int            `json:"open"`
	InProgress int            `json:"in_progress"`
	InReview   int            `json:"in_review"`
	Ready      int            `json:"ready"`
	OpenBugs   int            `json:"open_bugs"`
	ByStatus   map[string]int `json:"by_status"`
	// summary だけに付ける（§5-8-7）: 48 時間超の In Review・未応答のフィードバックの件数
	InReviewStale   *int `json:"in_review_stale,omitempty"`
	FeedbackPending *int `json:"feedback_pending,omitempty"`
}

func counts(set *domain.Set) *countsJSON {
	c := &countsJSON{ByStatus: map[string]int{}, Ready: len(set.Ready())}
	for _, it := range set.Items {
		if it.ID == "" {
			continue
		}
		c.Total++
		c.ByStatus[it.Status]++
		if it.Updated > c.Updated {
			c.Updated = it.Updated
		}
		if it.IsClosed() {
			c.Closed++
			continue
		}
		c.Open++
		if it.Type == "bug" {
			c.OpenBugs++
		}
	}
	c.InProgress, c.InReview = c.ByStatus["In Progress"], c.ByStatus["In Review"]
	return c
}

func summaryOf(pr store.Project, role string) projectSummaryJSON {
	rules := pr.Rules
	if rules == nil {
		rules = json.RawMessage("null")
	}
	return projectSummaryJSON{projectJSON: projectJSON{Slug: pr.Slug, Name: pr.Name, Description: pr.Description, Prefix: pr.Prefix, Width: pr.Width, Role: role}, Counter: pr.Counter, Rules: rules}
}

// apiProjects はプロジェクトの一覧。既定は参加しているプロジェクトだけ（admin の利用者も同じ。役割は
// project_members の値）。?all=1 は admin の利用者だけが使え、全プロジェクトを返す（参加していないものは
// 役割 viewer・member false。解決の役割と同じ閲覧のみ）。admin 以外が ?all=1 を付けると 403（黙って参加分を返すと取り違えるため）。
func (s *Server) apiProjects(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	all, ok := allParam(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_argument", i18n.T(reqLang(r), "server.api.err.all_param"))
		return
	}
	if all && p.User.Role != "admin" {
		writeError(w, http.StatusForbidden, "forbidden", i18n.T(reqLang(r), "server.api.err.all_admin_only"))
		return
	}
	projects, roles, err := s.listedProjects(r.Context(), p.User, all)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]projectSummaryJSON, 0, len(projects))
	for _, pr := range projects {
		set, _, err := s.svc.ProjectIssues(r.Context(), pr)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		role, member := roles[pr.ID], true
		if role == "" { // ?all=1 で参加していないプロジェクト
			role, member = store.NonMemberAdminRole, false
		}
		sum := summaryOf(pr, role)
		sum.Member = &member
		sum.Counts = counts(set)
		out = append(out, sum)
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": out, "generated": s.stamp()})
}

// allParam は ?all= を読む（無ければ false）。
func allParam(r *http.Request) (bool, bool) {
	switch strings.ToLower(r.URL.Query().Get("all")) {
	case "", "0", "false":
		return false, true
	case "1", "true":
		return true, true
	}
	return false, false
}

// listedProjects は一覧に出すプロジェクトと、参加しているものの役割（project_members の値）を返す。
// all なら全プロジェクト（呼び出し側で admin に限る）。参加していないものは roles に入らない。
func (s *Server) listedProjects(ctx context.Context, u store.User, all bool) ([]store.Project, map[int64]string, error) {
	projects, roles, err := store.MemberProjects(ctx, s.db, u.ID)
	if err != nil || !all {
		return projects, roles, err
	}
	everything, err := store.ListProjects(ctx, s.db)
	if err != nil {
		return nil, nil, err
	}
	projects = projects[:0:0]
	for _, pr := range everything {
		if !pr.Archived { // アーカイブ済みは一覧に出さない（AccessibleProjects と同じ扱い）
			projects = append(projects, pr)
		}
	}
	return projects, roles, nil
}

func (s *Server) apiProject(w http.ResponseWriter, r *http.Request) {
	pr, role, ok := s.projectFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	u := principalFrom(r.Context()).User
	member := true
	if u.Role == "admin" {
		mr, err := store.MemberRole(r.Context(), s.db, pr.ID, u.ID)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		member = mr != ""
	}
	sum := summaryOf(pr, role)
	sum.Member = &member
	writeJSON(w, http.StatusOK, sum)
}

// --- 一覧

func sortArgs(w http.ResponseWriter, r *http.Request) (string, bool, bool) {
	q := r.URL.Query()
	key := q.Get("sort")
	if key == "" {
		key = "priority"
	}
	if err := domain.ValidateValue("--sort", key, domain.SortKeys); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", i18n.Text(reqLang(r), err))
		return "", false, false
	}
	return key, queryBool(q.Get("reverse")), true
}

func queryBool(v string) bool {
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// markOtherSession は進行中のイシューに「別のセッションが着手した」印と経過時間を付ける。
// 同じ作業ツリーで複数の AI のセッションが動くとき、着手済みのものを取りに行かせないために出す
// （経過時間は、渡したまま放置されたものに気づく手がかりにもなる）。器が違うだけの同じ人の別セッションも
// 「別のセッション」として扱う。セッション ID が分からない経路（画面・セッション ID を送らない AI）では何もしない。
func markOtherSession(ctx context.Context, lang i18n.Lang, db *sql.DB, projectID int64, me string, items []issueJSON) {
	starters, err := store.InProgressStarters(ctx, db, projectID)
	if err != nil {
		return
	}
	st := map[string]store.Starter{}
	for _, x := range starters {
		st[x.DisplayID] = x
	}
	for i := range items {
		x, ok := st[items[i].ID]
		if !ok {
			continue
		}
		switch {
		case service.ComparableSessions(me, x.SessionID) && x.SessionID != me:
			items[i].OtherSession = true
		case service.CrossPath(me, x.SessionID): // 比べられない（CLI と MCP）。判定できないことを示す
			items[i].CrossPathSession = true
		}
		if !x.At.IsZero() {
			items[i].StartedAgo = reviewAge(lang, time.Since(x.At))
		}
	}
}

// otherSessionNote は「別のセッションが着手中」の注記（対象が無ければ空）。
func otherSessionNote(lang i18n.Lang, items []issueJSON) string {
	var ids []string
	for _, it := range items {
		if !it.OtherSession {
			continue
		}
		id := it.ID
		if it.StartedAgo != "" {
			id += "（" + it.StartedAgo + "）"
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return ""
	}
	return i18n.T(lang, "server.api.issue.other_session_note", "ids", strings.Join(ids, "・"))
}

// crossPathNote は「別の経路で着手されている（同じセッションかは判定できない）」の注記（対象が無ければ空）。
func crossPathNote(lang i18n.Lang, items []issueJSON) string {
	var ids []string
	for _, it := range items {
		if !it.CrossPathSession {
			continue
		}
		id := it.ID
		if it.StartedAgo != "" {
			id += "（" + it.StartedAgo + "）"
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return ""
	}
	return i18n.T(lang, "server.api.issue.cross_path_note", "ids", strings.Join(ids, "・"))
}

func itemsJSON(pr store.Project, rows []service.Issue, items []domain.Issue) []issueJSON {
	byID := map[string]*service.Issue{}
	for i := range rows {
		byID[rows[i].Item.ID] = &rows[i]
	}
	out := make([]issueJSON, 0, len(items))
	for _, it := range items {
		if row, ok := byID[it.ID]; ok {
			out = append(out, toIssueJSON(row))
		}
	}
	return out
}

func (s *Server) apiListIssues(w http.ResponseWriter, r *http.Request) {
	pr, _, ok := s.projectFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	key, reverse, ok := sortArgs(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	hasFeedback := queryBool(q.Get("has_feedback"))
	// 未応答のフィードバックで絞るときは既定でクローズ済みも含める（反応は Done の後に来る。§5-8-6）
	f := domain.Filter{Status: q.Get("status"), Type: q.Get("type"), Label: q.Get("label"), Ref: q.Get("ref"), All: queryBool(q.Get("all")) || hasFeedback}
	if f.Status != "" {
		if err := domain.ValidateValue("--status", f.Status, domain.Statuses); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_argument", i18n.Text(reqLang(r), err))
			return
		}
	}
	set, rows, err := s.svc.ProjectIssues(r.Context(), pr)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items := filterAssignee(itemsJSON(pr, rows, set.List(f, key, reverse)), q.Get("assignee"), principalFrom(r.Context()).User.Login)
	// 別のセッションが着手したものに印を付ける（空きを探す経路。summary と同じ判定。CLI が注記を出す）
	markOtherSession(r.Context(), reqLang(r), s.db, pr.ID, actor(r).SessionID, items)
	if hasFeedback {
		n, _, err := s.pendingByIssue(r.Context(), pr)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		items = withFeedbackPending(items, n, true)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items), "timezone": s.svc.Loc.String()})
}

// filterAssignee は一覧を担当で絞る。want は me・login・-（未設定）。空なら絞らない。
func filterAssignee(items []issueJSON, want, me string) []issueJSON {
	want = strings.TrimSpace(want)
	if want == "" {
		return items
	}
	switch strings.ToLower(want) {
	case "me":
		want = me
	case "-":
		want = ""
	}
	out := make([]issueJSON, 0, len(items))
	for _, it := range items {
		if it.Assignee == want {
			out = append(out, it)
		}
	}
	return out
}

func (s *Server) apiReady(w http.ResponseWriter, r *http.Request) {
	pr, _, ok := s.projectFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	key, reverse, ok := sortArgs(w, r)
	if !ok {
		return
	}
	set, rows, err := s.svc.ProjectIssues(r.Context(), pr)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items := filterAssignee(itemsJSON(pr, rows, domain.SortIssues(set.Ready(), key, reverse)), r.URL.Query().Get("assignee"),
		principalFrom(r.Context()).User.Login)
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items), "timezone": s.svc.Loc.String()})
}

type issueRefJSON struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Type   string `json:"type"`
	Status string `json:"status"`
}

func refs(items []domain.Issue) []issueRefJSON {
	out := make([]issueRefJSON, 0, len(items))
	for _, it := range items {
		out = append(out, issueRefJSON{it.ID, it.Title, it.Type, it.Status})
	}
	return out
}

func (s *Server) apiMatrix(w http.ResponseWriter, r *http.Request) {
	pr, _, ok := s.projectFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	set, _, err := s.svc.ProjectIssues(r.Context(), pr)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if r.URL.Query().Get("format") == "md" {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Write([]byte(s.matrixMarkdown(set)))
		return
	}
	m := set.BuildMatrixData()
	type rowJSON struct {
		Requirement issueRefJSON   `json:"requirement"`
		Design      []issueRefJSON `json:"design"`
		Task        []issueRefJSON `json:"task"`
		Test        []issueRefJSON `json:"test"`
	}
	type orphanJSON struct {
		Target string   `json:"target"`
		From   []string `json:"from"`
	}
	rows := make([]rowJSON, 0, len(m.Rows))
	for _, row := range m.Rows {
		rows = append(rows, rowJSON{refs([]domain.Issue{row.Requirement})[0], refs(row.Design), refs(row.Task), refs(row.Test)})
	}
	orphans := make([]orphanJSON, 0, len(m.Orphans))
	for _, o := range m.Orphans {
		orphans = append(orphans, orphanJSON{o.Target, o.From})
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows, "untested": refs(m.Untested), "orphans": orphans,
		"requirements_ready": closablesJSON(reqLang(r), set.ClosableRequirements())})
}

// summaryUsageDays は summary に出す未付与の期間。
const summaryUsageDays = 7

func (s *Server) apiSummary(w http.ResponseWriter, r *http.Request) {
	pr, role, ok := s.projectFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	limit := 5
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v >= 0 && v <= 100 {
		limit = v
	}
	set, rows, err := s.svc.ProjectIssues(r.Context(), pr)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	requests, err := s.requestList(r.Context(), reqLang(r), pr, false) // 未完了のレポート作成依頼
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	ready, closable := set.Ready(), set.ClosableRequirements()
	byStatus := func(status string) []issueJSON {
		return itemsJSON(pr, rows, set.List(domain.Filter{Status: status}, "priority", false))
	}
	// 呼び出した利用者の AI 操作のうち、直近 7 日でトークン情報が付いていないもの（次のセッションで回収させる）
	missing, err := s.usageCoverage(r.Context(), pr, principalFrom(r.Context()).User.ID, summaryUsageDays)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	// ② 人の判断待ち（滞留）・③ 外からの反応（未応答のフィードバック）。
	inReview, fb, cnt, err := s.loopLayers(r.Context(), reqLang(r), pr, rows, byStatus("In Review"), counts(set), limit)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	inProgress := byStatus("In Progress")
	markOtherSession(r.Context(), reqLang(r), s.db, pr.ID, actor(r).SessionID, inProgress)
	writeJSON(w, http.StatusOK, map[string]any{
		"usage_missing": map[string]any{"count": missing.Missing, "issues": missing.Issues, "days": missing.Days,
			"command": missing.Command, "message": usageMissingText(reqLang(r), missing)},
		"project":     summaryOf(pr, role),
		"counts":      cnt,
		"in_progress": inProgress,
		"in_review":   inReview,
		"feedback":    fb,
		"ready":       itemsJSON(pr, rows, ready[:min(limit, len(ready))]),
		"ready_total": len(ready),
		// ① の末尾: 下位がすべて完了した開いている要件（上位 limit 件と総数）
		"requirements_ready":       closablesJSON(reqLang(r), closable[:min(limit, len(closable))]),
		"requirements_ready_total": len(closable),
		"usage_requests":           requests,
		// timezone は時刻の時間帯（IANA 名）。応答の時刻を描く CLI がこれで描く（台帳・レポートと同じ形）。
		"timezone": s.svc.Loc.String(),
	})
}

// --- 起票・参照・変更

type createRequest struct {
	Title     string   `json:"title"`
	Type      string   `json:"type"`
	Status    string   `json:"status"`
	Priority  string   `json:"priority"`
	Parent    string   `json:"parent"`
	Labels    []string `json:"labels"`
	BlockedBy []string `json:"blocked_by"`
	Traces    []string `json:"traces"`
	Refs      []string `json:"refs"`
	Body      string   `json:"body"`
	// OverrideReason は上書き可能なルール違反（usage・verify の require_on_close）を理由付きで通す。理由はイベントに残る
	OverrideReason string `json:"override_reason"`
	// Assignee は担当者（me / login）
	Assignee string `json:"assignee"`
}

func (s *Server) apiCreateIssue(w http.ResponseWriter, r *http.Request) {
	pr, role, ok := s.projectFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	if !canWrite(role) {
		lang := reqLang(r)
		s.serviceError(w, r, forbidden(lang, pr.Slug, i18n.T(lang, "server.api.err.what_create")))
		return
	}
	var req createRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	a := actor(r)
	it, err := s.svc.Create(r.Context(), a, pr, service.CreateInput{Title: req.Title, Type: req.Type, Status: req.Status,
		Priority: req.Priority, Parent: req.Parent, Labels: req.Labels, BlockedBy: req.BlockedBy, Traces: req.Traces, Refs: req.Refs, Body: req.Body, OverrideReason: req.OverrideReason, Assignee: req.Assignee}, reqLang(r))
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	dir := "open"
	if it.Closed() {
		dir = "closed"
	}
	w.Header().Set("Location", s.cfg.BasePath+"/api/v1/issues/"+it.Item.ID)
	w.Header().Set("ETag", etag(it.Row.Version))
	body := map[string]any{"issue": toDetailJSON(it), "path": dir + "/" + it.Row.FileName}
	if n := s.usageNotice(r.Context(), reqLang(r), a, pr, it, false); n != "" {
		body["usage_notice"] = n
	}
	writeJSON(w, http.StatusCreated, body)
}

func etag(version int) string { return `"` + strconv.Itoa(version) + `"` }

func (s *Server) apiIssue(w http.ResponseWriter, r *http.Request) {
	pr, row, ok := s.issueFor(w, r, false)
	if !ok {
		return
	}
	it, err := s.svc.Detail(r.Context(), pr, row.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(it.Row.Version))
	if r.URL.Query().Get("format") == "md" {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Write([]byte(it.Markdown))
		return
	}
	writeJSON(w, http.StatusOK, toDetailJSON(it))
}

type commentRequest struct {
	Text *string `json:"text"`
}

func (s *Server) apiComment(w http.ResponseWriter, r *http.Request) {
	pr, row, ok := s.issueFor(w, r, true)
	if !ok {
		return
	}
	var req commentRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Text == nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", i18n.T(reqLang(r), "server.api.err.comment_text_required"))
		return
	}
	a := actor(r)
	it, err := s.svc.Comment(r.Context(), a, pr, row.ID, *req.Text)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(it.Row.Version))
	body := map[string]any{"issue": toIssueJSON(it), "seq": len(it.Row.Doc.Comments), "message": i18n.T(reqLang(r), "server.api.issue.comment_added", "id", it.Item.ID)}
	if n := s.usageNotice(r.Context(), reqLang(r), a, pr, it, false); n != "" {
		body["usage_notice"] = n
	}
	writeJSON(w, http.StatusCreated, body)
}

type statusRequest struct {
	Status         *string `json:"status"`
	Comment        string  `json:"comment"`
	OverrideReason string  `json:"override_reason"`
	Assignee       string  `json:"assignee"` // 担当者の指定（me / login / -）
}

func (s *Server) apiStatus(w http.ResponseWriter, r *http.Request) {
	pr, row, ok := s.issueFor(w, r, true)
	if !ok {
		return
	}
	var req statusRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Status == nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", i18n.T(reqLang(r), "server.api.err.status_required"))
		return
	}
	a := actor(r)
	res, err := s.svc.SetStatusAssign(r.Context(), a, pr, row.ID, *req.Status, req.Comment, req.OverrideReason, req.Assignee)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	it := res.Issue
	w.Header().Set("ETag", etag(it.Row.Version))
	body := map[string]any{"issue": toIssueJSON(it), "from": res.From, "to": it.Item.Status}
	// 下位の最後の 1 件を閉じたら要件の検証と close を促す。messages に入れるので古い CLI でも表示される
	body["messages"] = withClosable(reqLang(r), statusMessages(reqLang(r), res, req.Comment), body, s.closedRequirements(r.Context(), pr, res))
	if n := s.usageNotice(r.Context(), reqLang(r), a, pr, it, it.Closed() && res.From != it.Item.Status); n != "" {
		body["usage_notice"] = n
	}
	writeJSON(w, http.StatusOK, body)
}

type patchRequest struct {
	Title     *string   `json:"title"`
	Type      *string   `json:"type"`
	Priority  *string   `json:"priority"`
	Parent    *string   `json:"parent"`
	Labels    *[]string `json:"labels"`
	BlockedBy *[]string `json:"blocked_by"`
	Traces    *[]string `json:"traces"`
	Refs      *[]string `json:"refs"`
	Markdown  *string   `json:"markdown"`
	Status    *string   `json:"status"`
	// OverrideReason は担当が他人のイシューを編集する理由（担当は替えない）
	OverrideReason string `json:"override_reason"`
}

// ifMatch は If-Match の版番号を読む（"3" / 3 / W/"3"）。無ければ 0。
func ifMatch(r *http.Request) (int, bool) {
	v := strings.TrimSpace(r.Header.Get("If-Match"))
	if v == "" {
		return 0, true
	}
	v = strings.Trim(strings.TrimPrefix(v, "W/"), `"`)
	n, err := strconv.Atoi(v)
	return n, err == nil && n > 0
}

func (s *Server) apiUpdateIssue(w http.ResponseWriter, r *http.Request) {
	pr, row, ok := s.issueFor(w, r, true)
	if !ok {
		return
	}
	version, ok := ifMatch(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_argument", i18n.T(reqLang(r), "server.api.err.if_match"))
		return
	}
	var req patchRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Status != nil {
		writeError(w, http.StatusUnprocessableEntity, "use_status_endpoint", i18n.T(reqLang(r), "server.api.err.use_status_endpoint"))
		return
	}
	a := actor(r)
	it, err := s.svc.Update(r.Context(), a, pr, row.ID, version, service.Patch{Title: req.Title, Type: req.Type, Priority: req.Priority,
		Parent: req.Parent, Labels: req.Labels, BlockedBy: req.BlockedBy, Traces: req.Traces, Refs: req.Refs, Markdown: req.Markdown,
		OverrideReason: req.OverrideReason})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(it.Row.Version))
	d := toDetailJSON(it)
	d.UsageNotice = s.usageNotice(r.Context(), reqLang(r), a, pr, it, false)
	writeJSON(w, http.StatusOK, d)
}

// statusMessages は状態変更の CLI の出力行。担当を明示で変えた・引き継いだときはその行も足す
// （In Progress で未設定の担当を本人にした R1 は出さない。従来の出力のまま）。
func statusMessages(lang i18n.Lang, res *service.StatusResult, comment string) []string {
	it := res.Issue
	messages := []string{it.Item.ID + ": " + res.From + " → " + it.Item.Status}
	if res.AssigneeChanged && !res.AssigneeAuto {
		messages = append(messages, (&service.AssignResult{Issue: it, From: res.AssigneeFrom, To: it.Row.Assignee.Login, Changed: true}).Message(lang))
	}
	if comment != "" {
		messages = append(messages, i18n.T(lang, "server.api.issue.comment_added", "id", it.Item.ID))
	}
	return messages
}

type assignRequest struct {
	Assignee       *string `json:"assignee"`
	OverrideReason string  `json:"override_reason"`
}

// apiAssign は POST /issues/{id}/assign（担当者の変更）。
func (s *Server) apiAssign(w http.ResponseWriter, r *http.Request) {
	pr, row, ok := s.issueFor(w, r, true)
	if !ok {
		return
	}
	var req assignRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Assignee == nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", i18n.T(reqLang(r), "server.api.err.assignee_required"))
		return
	}
	res, err := s.svc.Assign(r.Context(), actor(r), pr, row.ID, *req.Assignee, req.OverrideReason)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(res.Issue.Row.Version))
	writeJSON(w, http.StatusOK, map[string]any{"issue": toIssueJSON(res.Issue), "from": res.From, "to": res.To, "changed": res.Changed,
		"message": res.Message(reqLang(r))})
}

// --- 鮮度ガード

type activityJSON struct {
	ID          string  `json:"id"`
	Project     string  `json:"project"`
	Title       string  `json:"title"`
	Status      string  `json:"status"`
	Closed      bool    `json:"closed"`
	LastAt      string  `json:"last_at"`
	LastEpoch   float64 `json:"last_epoch"`
	LastKind    string  `json:"last_kind"`
	LastVia     string  `json:"last_via"`
	EventsSince int     `json:"events_since"`
}

// apiActivity は ID ごとの最終更新イベントを返す（?ids=A,B&since=<UNIX 秒・小数可>）。見つからない・権限の無い ID は含めない。
func (s *Server) apiActivity(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var ids []string
	for _, id := range strings.Split(q.Get("ids"), ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 || len(ids) > 200 {
		writeError(w, http.StatusBadRequest, "invalid_argument", i18n.T(reqLang(r), "server.api.err.ids_required"))
		return
	}
	since := time.Unix(0, 0)
	if v := q.Get("since"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < 0 {
			writeError(w, http.StatusBadRequest, "invalid_argument", i18n.T(reqLang(r), "server.api.err.since_invalid"))
			return
		}
		since = time.Unix(0, int64(f*1e9))
	}
	p := principalFrom(r.Context())
	projects, _, err := store.AccessibleProjects(r.Context(), s.db, p.User)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	slugs := map[int64]string{}
	for _, pr := range projects {
		slugs[pr.ID] = pr.Slug
	}
	acts, err := store.IssueActivity(r.Context(), s.db, ids, since.UTC())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := []activityJSON{}
	for _, a := range acts {
		slug, ok := slugs[a.ProjectID]
		if !ok {
			continue
		}
		out = append(out, activityJSON{ID: a.DisplayID, Project: slug, Title: a.Title, Status: a.Status,
			Closed: domain.Issue{Status: a.Status}.IsClosed(), LastAt: a.LastAt.UTC().Format(time.RFC3339Nano),
			LastEpoch: float64(a.LastAt.UnixNano()) / 1e9, LastKind: a.LastKind, LastVia: a.LastVia, EventsSince: a.Since})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	now := s.cfg.Now()
	// timezone は時刻の時間帯（IANA 名）。last_epoch を描く CLI がこれで時刻を描く（台帳・レポートと同じ形）。
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "now_epoch": float64(now.UnixNano()) / 1e9,
		"timezone": s.svc.Loc.String()})
}
