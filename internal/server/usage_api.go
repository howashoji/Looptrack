package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/usage"
)

// トークン消費のスナップショット（設計は docs/server/DESIGN.md §5-4）。
// POST /projects/{slug}/usage … CLI 内蔵・フックが「会話の累計」を 1 件送る
// GET  /issues/{id}/usage     … そのイシューの区間（段階）ごとの消費

const (
	maxUsageJSON     = 256 << 10 // segments などの JSON 1 つあたり
	maxUsageCount    = int64(1) << 50
	usageFutureSlack = 24 * time.Hour
)

var clientRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)

type usageRequest struct {
	Client         string          `json:"client"`
	ClientVersion  string          `json:"client_version"`
	SessionID      string          `json:"session_id"`
	ConversationID string          `json:"conversation_id"`
	Trigger        string          `json:"trigger"`
	Issue          string          `json:"issue"`
	Op             string          `json:"op"`
	Via            string          `json:"via"`
	At             string          `json:"at"`
	ToolUseID      string          `json:"tool_use_id"`
	Tokens         usageTokens     `json:"tokens"`
	Responses      int64           `json:"responses"`
	SubResponses   int64           `json:"sub_responses"`
	ByModel        json.RawMessage `json:"by_model"`
	IO             json.RawMessage `json:"io"`
	Human          json.RawMessage `json:"human"`
	Segments       json.RawMessage `json:"segments"`
	Branch         string          `json:"branch"`
	Branches       json.RawMessage `json:"branches"`
	CwdName        string          `json:"cwd_name"`
	Excluded       bool            `json:"excluded"`
}

type usageTokens struct {
	Main usage.Tokens `json:"main"`
	Sub  usage.Tokens `json:"sub"`
}

func (s *Server) apiPostUsage(w http.ResponseWriter, r *http.Request) {
	pr, role, ok := s.projectFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	lang := reqLang(r)
	if !canWrite(role) {
		s.serviceError(w, r, forbidden(lang, pr.Slug, i18n.T(lang, "server.api.err.what_usage_post")))
		return
	}
	var req usageRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	bad := func(msg string) { writeError(w, http.StatusBadRequest, "invalid_argument", msg) }

	req.Client = strings.ToLower(strings.TrimSpace(req.Client))
	if !clientRe.MatchString(req.Client) {
		bad(i18n.T(lang, "server.api.err.usage_client"))
		return
	}
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.ConversationID = strings.TrimSpace(req.ConversationID)
	if req.SessionID == "" || len(req.SessionID) > 128 || req.ConversationID == "" || len(req.ConversationID) > 64 {
		bad(i18n.T(lang, "server.api.err.usage_ids"))
		return
	}
	if !contains(usage.Triggers, req.Trigger) {
		bad(i18n.T(lang, "server.api.err.usage_trigger", "values", strings.Join(usage.Triggers, " / ")))
		return
	}
	if req.Via != "" && req.Via != "cli" && req.Via != "mcp" {
		bad(i18n.T(lang, "server.api.err.usage_via"))
		return
	}
	at, err := time.Parse(time.RFC3339Nano, req.At)
	if err != nil {
		bad(i18n.T(lang, "server.api.err.usage_at"))
		return
	}
	if at.After(s.svc.Now().Add(usageFutureSlack)) {
		bad(i18n.T(lang, "server.api.err.usage_at_future"))
		return
	}
	for _, v := range []int64{req.Tokens.Main.Input, req.Tokens.Main.CacheCreate, req.Tokens.Main.CacheRead, req.Tokens.Main.Output,
		req.Tokens.Sub.Input, req.Tokens.Sub.CacheCreate, req.Tokens.Sub.CacheRead, req.Tokens.Sub.Output, req.Responses, req.SubResponses} {
		if v < 0 || v > maxUsageCount {
			bad(i18n.T(lang, "server.api.err.usage_counts"))
			return
		}
	}
	for name, raw := range map[string]json.RawMessage{"by_model": req.ByModel, "io": req.IO, "human": req.Human, "segments": req.Segments, "branches": req.Branches} {
		if len(raw) > maxUsageJSON {
			bad(i18n.T(lang, "server.api.err.usage_json_big", "name", name))
			return
		}
	}
	if len(req.ClientVersion) > 64 || len(req.Branch) > 255 || len(req.CwdName) > 255 || len(req.ToolUseID) > 255 {
		bad(i18n.T(lang, "server.api.err.usage_fields_long"))
		return
	}

	// 指示文（区間の作業名 label）はプロジェクト別ルール usage.send_prompts が許すときだけ保存する（CLI も送らないが二重に守る）。
	// ルールが読めないときは許さない側に倒す
	rules, rerr := domain.ParseRules(pr.Rules)
	sendPrompts := rerr == nil && rules.SendPrompts()
	if !sendPrompts {
		stripped, err := stripSegmentLabels(req.Segments)
		if err != nil {
			bad(i18n.T(lang, "server.api.err.usage_segments"))
			return
		}
		req.Segments = stripped
	}

	snap := store.UsageSnapshot{
		ProjectID: pr.ID, UserID: principalFrom(r.Context()).User.ID, TokenID: principalFrom(r.Context()).TokenID,
		Client: req.Client, ClientVersion: req.ClientVersion, SessionID: req.SessionID, ConversationID: req.ConversationID,
		Trigger: req.Trigger, Via: req.Via, At: at,
		Counters: usage.Counters{Main: req.Tokens.Main, Sub: req.Tokens.Sub, Responses: req.Responses, SubResponses: req.SubResponses},
		ByModel:  req.ByModel, IO: req.IO, Human: req.Human, Segments: req.Segments,
		Branch: req.Branch, Branches: req.Branches, CwdName: req.CwdName, Excluded: req.Excluded,
		ReceivedAt: s.svc.Now(), // issue_events.at と同じ時計で持つ（付与漏れの突き合わせ）
	}
	// issue_op は issue と op が必須。manual（looptrack issue usage attach）は issue だけ付けられる。それ以外は付けられない
	if req.Trigger == usage.TriggerIssueOp && (strings.TrimSpace(req.Issue) == "" || !contains(usage.Ops, req.Op)) {
		bad(i18n.T(lang, "server.api.err.usage_issue_op", "values", strings.Join(usage.Ops, " / ")))
		return
	}
	if req.Trigger == usage.TriggerManual && req.Op != "" {
		bad(i18n.T(lang, "server.api.err.usage_manual_op"))
		return
	}
	if req.Trigger != usage.TriggerIssueOp && req.Trigger != usage.TriggerManual && (req.Issue != "" || req.Op != "") {
		bad(i18n.T(lang, "server.api.err.usage_issue_scope"))
		return
	}
	if req.Issue != "" {
		row, err := store.FindIssue(r.Context(), s.db, req.Issue, pr.ID, false)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", i18n.T(lang, "server.api.err.issue_not_found", "id", strings.ToUpper(req.Issue)))
			return
		}
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		var status string
		if err := s.db.QueryRowContext(r.Context(), "SELECT status FROM issues WHERE id = ?", row.ID).Scan(&status); err != nil {
			s.internalError(w, r, err)
			return
		}
		snap.IssueID, snap.Op, snap.IssueStatus = row.ID, req.Op, status
	}
	snap.DedupeKey = usageDedupeKey(req, snap)

	id, duplicate, err := store.InsertUsageSnapshot(r.Context(), s.db, snap)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if duplicate {
		writeJSON(w, http.StatusOK, map[string]any{"id": 0, "duplicate": true, "send_prompts": sendPrompts})
		return
	}
	// send_prompts: CLI / フックはこの値を覚えて、次の送信から作業名を付けるかを決める（呼び出しを増やさない）
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "duplicate": false, "send_prompts": sendPrompts})
}

// stripSegmentLabels は区間（segments の配列）から作業名 label を除く。label の無い区間はそのまま、
// 空・null は変えない。配列でなければ誤り。
func stripSegmentLabels(raw json.RawMessage) (json.RawMessage, error) {
	if s := strings.TrimSpace(string(raw)); s == "" || s == "null" {
		return raw, nil
	}
	var segs []json.RawMessage
	if err := json.Unmarshal(raw, &segs); err != nil {
		return nil, err
	}
	changed := false
	for i, seg := range segs {
		var m map[string]json.RawMessage
		if json.Unmarshal(seg, &m) != nil {
			continue // 区間がオブジェクトでなければ label は持てない
		}
		if _, ok := m["label"]; !ok {
			continue
		}
		delete(m, "label")
		b, err := json.Marshal(m)
		if err != nil {
			return nil, err
		}
		segs[i], changed = b, true
	}
	if !changed {
		return raw, nil
	}
	return json.Marshal(segs)
}

// usageDedupeKey は同じ内容の再送を 1 行にするための鍵。ツール呼び出しの ID があればそれ、無ければ操作と累計。
func usageDedupeKey(req usageRequest, snap store.UsageSnapshot) string {
	var key string
	if req.ToolUseID != "" {
		key = "tool:" + req.SessionID + "|" + req.ToolUseID
	} else {
		key = fmt.Sprintf("state:%s|%s|%d|%s|%d|%d|%d", req.SessionID, req.Trigger, snap.IssueID, req.Op,
			snap.Counters.Responses, snap.Counters.SubResponses, snap.Counters.Total())
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

type usageStageJSON struct {
	ID             int64          `json:"id"`
	At             string         `json:"at"`
	Trigger        string         `json:"trigger"`
	Op             string         `json:"op,omitempty"`
	Via            string         `json:"via,omitempty"`
	Client         string         `json:"client"`
	SessionID      string         `json:"session_id"`
	ConversationID string         `json:"conversation_id"`
	Delta          usage.Counters `json:"delta"`
	DeltaTotal     int64          `json:"delta_total"`
	Cumulative     usage.Counters `json:"cumulative"`
	Inconsistent   bool           `json:"inconsistent"`
	Excluded       bool           `json:"excluded"`
}

func (s *Server) apiIssueUsage(w http.ResponseWriter, r *http.Request) {
	pr, row, ok := s.issueFor(w, r, false)
	if !ok {
		return
	}
	res, err := s.issueUsage(r.Context(), pr, row)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// issueUsageJSON はイシューに帰属する区間の一覧と合計（REST の GET /issues/{id}/usage と MCP の issue_usage で共通）。
type issueUsageJSON struct {
	Issue         string           `json:"issue"`
	Project       string           `json:"project"`
	Stages        []usageStageJSON `json:"stages"`
	StageCount    int              `json:"stage_count"`
	Total         usage.Counters   `json:"total"`
	TotalTokens   int64            `json:"total_tokens"`
	ExcludedTotal usage.Counters   `json:"excluded_total"`
	Inconsistent  int              `json:"inconsistent"`
}

func (s *Server) issueUsage(ctx context.Context, pr store.Project, row store.IssueRow) (issueUsageJSON, error) {
	snaps, err := store.UsageForIssue(ctx, s.db, pr.ID, row.ID)
	if err != nil {
		return issueUsageJSON{}, err
	}
	stages := []usageStageJSON{}
	var counted, excluded []usage.Stage
	inconsistent := 0
	// 差分は会話ごとに計算し（usage.Stages）、一覧は時刻順（同時刻は ID 順）で返す（CLI・MCP・REST で同じ並び）
	all := usage.Stages(snaps, domain.ClosedStatuses)
	usage.SortByTime(all)
	for _, st := range all {
		if st.IssueID != row.ID {
			continue
		}
		stages = append(stages, usageStageJSON{
			ID: st.Snapshot.ID, At: st.Snapshot.At.UTC().Format(time.RFC3339Nano), Trigger: st.Snapshot.Trigger,
			Op: st.Snapshot.Op, Via: st.Snapshot.Via, Client: st.Snapshot.Client,
			SessionID: st.Snapshot.SessionID, ConversationID: st.Snapshot.ConversationID,
			Delta: st.Delta, DeltaTotal: st.Delta.Total(), Cumulative: st.Cumulative, Inconsistent: st.Inconsist, Excluded: st.Excluded,
		})
		if st.Inconsist {
			inconsistent++
		}
		if st.Excluded {
			excluded = append(excluded, st)
		} else {
			counted = append(counted, st)
		}
	}
	total, excludedTotal := usage.Sum(counted), usage.Sum(excluded)
	return issueUsageJSON{Issue: row.DisplayID, Project: pr.Slug, Stages: stages, StageCount: len(stages),
		Total: total, TotalTokens: total.Total(), ExcludedTotal: excludedTotal, Inconsistent: inconsistent}, nil
}

// issueUsageText は CLI の usage show と同じ形の表（MCP の issue_usage。時刻は UTC）。
func issueUsageText(lang i18n.Lang, u issueUsageJSON) string {
	var b strings.Builder
	b.WriteString(i18n.T(lang, "server.api.usage.issue_total", "issue", u.Issue, "total", num(u.TotalTokens), "stages", u.StageCount,
		"excluded", num(u.ExcludedTotal.Total()), "inconsistent", u.Inconsistent) + "\n")
	if len(u.Stages) == 0 {
		b.WriteString(i18n.T(lang, "server.api.usage.issue_empty", "issue", u.Issue))
		return b.String()
	}
	b.WriteString(i18n.T(lang, "server.api.usage.issue_table_head") + "\n| -- | -- | -- | -- | --: | --: | --: | --: | --: | -- |\n")
	for _, st := range u.Stages {
		d := st.Delta
		note := ""
		if st.Excluded {
			note = i18n.T(lang, "server.api.usage.note_excluded")
		} else if st.Inconsistent {
			note = i18n.T(lang, "server.api.usage.note_inconsistent")
		}
		op := st.Op
		if op == "" {
			op = "-"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", strings.Replace(st.At[:19], "T", " ", 1), st.Trigger, op, st.Client,
			num(d.Main.Input+d.Sub.Input), num(d.Main.CacheCreate+d.Sub.CacheCreate), num(d.Main.CacheRead+d.Sub.CacheRead),
			num(d.Main.Output+d.Sub.Output), num(st.DeltaTotal), note)
	}
	return strings.TrimRight(b.String(), "\n")
}

// --- 付与漏れ（DESIGN.md §5-4「イベントとの突き合わせ」）

// usageNotice は変更の応答に載せる付与の指示。失敗しても変更は成功しているので、記録して空を返す。
func (s *Server) usageNotice(ctx context.Context, lang i18n.Lang, a service.Actor, pr store.Project, it *service.Issue, closing bool) string {
	n, err := s.svc.UsageNotice(ctx, lang, a, pr, it.Row.ID, it.Item.ID, closing)
	if err != nil {
		s.cfg.Logger.Error("usage notice", "issue", it.Item.ID, "err", err)
		return ""
	}
	return n
}

type usageMissingJSON struct {
	Issue     string `json:"issue"`
	Title     string `json:"title"`
	Kind      string `json:"kind"`
	Via       string `json:"via"`
	At        string `json:"at"`
	SessionID string `json:"session_id,omitempty"`
	User      string `json:"user"`
}

type usageCoverageJSON struct {
	Project  string             `json:"project"`
	Days     int                `json:"days"`
	Since    string             `json:"since"`
	Mine     bool               `json:"mine"`
	Target   int                `json:"target"`   // AI からの変更操作の数
	Attached int                `json:"attached"` // そのうちトークン情報が付いた数
	Missing  int                `json:"missing_count"`
	Rate     *float64           `json:"rate"`   // attached / target（対象が 0 件なら null）
	Humans   int                `json:"humans"` // 対象外（人がターミナルから打った操作）の数
	Items    []usageMissingJSON `json:"missing"`
	Issues   []string           `json:"issues"` // 未付与の操作があるイシュー（新しい順・重複なし）
	Command  string             `json:"command"`
}

// usageCoverage は期間内の付与の状況を数える。userID が 0 でなければその利用者の操作だけ。
func (s *Server) usageCoverage(ctx context.Context, pr store.Project, userID int64, days int) (usageCoverageJSON, error) {
	since := s.svc.Now().Add(-time.Duration(days) * 24 * time.Hour)
	events, humans, err := store.UsageCoverage(ctx, s.db, pr.ID, userID, since)
	if err != nil {
		return usageCoverageJSON{}, err
	}
	out := usageCoverageJSON{Project: pr.Slug, Days: days, Since: since.UTC().Format(time.RFC3339), Mine: userID != 0,
		Target: len(events), Humans: humans, Items: []usageMissingJSON{}, Issues: []string{}, Command: domain.UsageAttachCommand("<ID>")}
	seen := map[string]bool{}
	for _, ev := range events {
		if ev.Attached {
			out.Attached++
			continue
		}
		out.Items = append(out.Items, usageMissingJSON{Issue: ev.DisplayID, Title: ev.Title, Kind: ev.Kind, Via: ev.Via,
			At: ev.At.UTC().Format(time.RFC3339), SessionID: ev.SessionID, User: ev.Login})
		if !seen[ev.DisplayID] {
			seen[ev.DisplayID] = true
			out.Issues = append(out.Issues, ev.DisplayID)
		}
	}
	out.Missing = len(out.Items)
	if out.Target > 0 {
		rate := float64(out.Attached) / float64(out.Target)
		out.Rate = &rate
	}
	return out, nil
}

// usageMissingText は未付与の要約 1〜2 行（summary・MCP 用）。0 件なら空。
func usageMissingText(lang i18n.Lang, c usageCoverageJSON) string {
	if c.Missing == 0 {
		return ""
	}
	ids := c.Issues
	more := ""
	if len(ids) > 5 {
		ids, more = ids[:5], i18n.T(lang, "server.api.usage.missing_more")
	}
	return i18n.T(lang, "server.api.usage.missing", "count", c.Missing, "days", c.Days,
		"ids", strings.Join(ids, " "), "more", more, "command", domain.UsageAttachCommand("<ID>"))
}

// apiUsageCoverage は GET /projects/{slug}/usage/coverage?days=&mine=。付与漏れの一覧と充足率。
func (s *Server) apiUsageCoverage(w http.ResponseWriter, r *http.Request) {
	pr, _, ok := s.projectFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 366 {
			writeError(w, http.StatusBadRequest, "invalid_argument", i18n.T(reqLang(r), "server.api.err.days_range"))
			return
		}
		days = n
	}
	var userID int64
	if queryBool(r.URL.Query().Get("mine")) {
		userID = principalFrom(r.Context()).User.ID
	}
	out, err := s.usageCoverage(r.Context(), pr, userID, days)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// usageCoverageText は付与の状況を CLI（looptrack issue usage missing）と同じ形の文にする。
func usageCoverageText(lang i18n.Lang, c usageCoverageJSON) string {
	var b strings.Builder
	who := i18n.T(lang, "server.api.usage.who_all")
	if c.Mine {
		who = i18n.T(lang, "server.api.usage.who_mine")
	}
	rate := "-"
	if c.Rate != nil {
		rate = fmt.Sprintf("%.1f%%", *c.Rate*100)
	}
	b.WriteString(i18n.T(lang, "server.api.usage.coverage", "rate", rate, "days", c.Days, "who", who,
		"target", c.Target, "attached", c.Attached, "missing", c.Missing, "humans", c.Humans) + "\n")
	if c.Missing == 0 {
		b.WriteString(i18n.T(lang, "server.api.usage.coverage_none"))
		return b.String()
	}
	fmt.Fprintf(&b, "%-20s %-9s %-8s %-4s %-12s %s\n", i18n.T(lang, "server.api.usage.col_at"), "ID",
		i18n.T(lang, "server.api.usage.col_op"), i18n.T(lang, "server.api.usage.col_via"),
		i18n.T(lang, "server.api.usage.col_user"), i18n.T(lang, "server.api.usage.col_title"))
	for _, it := range c.Items {
		fmt.Fprintf(&b, "%-20s %-9s %-8s %-4s %-12s %s\n", strings.Replace(it.At[:19], "T", " ", 1), it.Issue, it.Kind, it.Via, it.User, it.Title)
	}
	b.WriteString(i18n.T(lang, "server.api.usage.coverage_fix", "command", c.Command, "ids", strings.Join(c.Issues, " ")))
	return b.String()
}
