package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/guide"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/kit"
)

// guide（使い方とルールを 1 回で返す）・next（ループ運用の着手）・dist（配布物: kit と looptrack の実行ファイル）。
// REST と MCP が同じ組み立てを使う。設計は DESIGN.md §5-5。

// composeGuide はプロジェクトの guide を lang で組み立てる。
// 導入セットの loop の有無は呼び出した利用者の導入済み通知から AI ごとに引き、「次に読むもの」に AI ごとの行で並べる
// （呼んだ AI では出し分けない。CLI・REST・MCP で同じ Markdown にするため）。
// 言語は経路ごとに決めたものを受け取る（REST は reqLang、MCP は接続の lang）。ここで決め直さない。
func (s *Server) composeGuide(ctx context.Context, lang i18n.Lang, pr store.Project, role string, u store.User) (guide.Output, error) {
	in := guide.Input{Slug: pr.Slug, Name: pr.Name, Prefix: pr.Prefix, Width: pr.Width, Role: role, Rules: pr.Rules}
	installs, err := store.AgentInstalls(ctx, s.db, u.ID, pr.ID)
	if err != nil {
		return guide.Output{}, err
	}
	for i := range installs {
		a := guide.AgentLoop{Agent: installs[i].Agent, Label: agentLabel(lang, installs[i].Agent), Loop: loopStateOf(&installs[i])}
		if a.Loop == "installed" {
			a.Version = installs[i].LoopVersion
		}
		in.Agents = append(in.Agents, a)
	}
	g, err := store.GetProjectGuide(ctx, s.db, pr.ID)
	switch {
	case err == nil:
		in.Doc, in.DocSource, in.DocUpdated = g.Content, g.Source, g.UpdatedAt.In(s.svc.Loc).Format("2006-01-02 15:04")
	case !errors.Is(err, store.ErrNotFound):
		return guide.Output{}, err
	}
	return guide.Compose(lang, in), nil
}

// apiGuide は GET /projects/{slug}/guide（?format=md で Markdown だけ）。
func (s *Server) apiGuide(w http.ResponseWriter, r *http.Request) {
	pr, role, ok := s.projectFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	out, err := s.composeGuide(r.Context(), reqLang(r), pr, role, principalFrom(r.Context()).User)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if r.URL.Query().Get("format") == "md" {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Write([]byte(out.Markdown))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": pr.Slug, "markdown": out.Markdown, "common": out.Common,
		"rules": nonNilRules(out.Rules), "doc": out.Doc, "doc_source": out.DocInfo})
}

func nonNilRules(r []guide.Rule) []guide.Rule {
	if r == nil {
		return []guide.Rule{}
	}
	return r
}

// --- next

type relatedRefJSON struct {
	ID      string `json:"id"`
	Title   string `json:"title,omitempty"`
	Type    string `json:"type,omitempty"`
	Status  string `json:"status,omitempty"`
	Missing bool   `json:"missing,omitempty"` // このプロジェクトに無い ID
}

type relatedJSON struct {
	Parent    *relatedRefJSON  `json:"parent"`
	BlockedBy []relatedRefJSON `json:"blocked_by"`
	Traces    []relatedRefJSON `json:"traces"`
	Children  []relatedRefJSON `json:"children"`
}

type nextJSON struct {
	Action     string           `json:"action"` // resumed / started / would_start / none
	Message    string           `json:"message"`
	Issue      *issueDetailJSON `json:"issue"`
	From       string           `json:"from,omitempty"`
	Acceptance string           `json:"acceptance"`
	Verify     *nextVerifyJSON  `json:"verify"` // 検証コマンド（節が無ければ null）
	Related    *relatedJSON     `json:"related"`
	Others     []string         `json:"others"`
	// Containers / OutsideTypes は着手中として扱わなかった自分の In Progress（束ね・型の指定の対象外）
	Containers   []string `json:"containers"`
	OutsideTypes []string `json:"outside_types"`
	// OtherSessions は、同じ利用者の別のセッションが In Progress にしていて着手中に扱わなかったもの
	OtherSessions []string `json:"other_sessions"`
	// CrossPathSessions は、別の経路（CLI / MCP）で In Progress にしたもの（着手中としては返す）
	CrossPathSessions []string           `json:"cross_path_sessions"`
	Skipped           []service.NextSkip `json:"skipped"`
	Next              []string           `json:"next_steps"`
	Text              string             `json:"text"` // 人（と AI）が読む形。CLI はこれをそのまま出す
	// UsageNotice は started（In Progress にした）ときだけ載る、トークン情報の付与の指示（service.UsageNotice）
	UsageNotice string `json:"usage_notice,omitempty"`
}

func refOf(set *domain.Set, id string) relatedRefJSON {
	if it, ok := set.Get(id); ok {
		return relatedRefJSON{ID: it.ID, Title: it.Title, Type: it.Type, Status: it.Status}
	}
	return relatedRefJSON{ID: id, Missing: true}
}

func relatedOf(set *domain.Set, it domain.Issue) *relatedJSON {
	rel := &relatedJSON{BlockedBy: []relatedRefJSON{}, Traces: []relatedRefJSON{}, Children: []relatedRefJSON{}}
	if it.Parent != "" {
		p := refOf(set, it.Parent)
		rel.Parent = &p
	}
	for _, id := range it.BlockedBy {
		rel.BlockedBy = append(rel.BlockedBy, refOf(set, id))
	}
	for _, id := range it.Traces {
		rel.Traces = append(rel.Traces, refOf(set, id))
	}
	for _, c := range set.Items {
		if c.ID != "" && strings.EqualFold(c.Parent, it.ID) {
			rel.Children = append(rel.Children, refOf(set, c.ID))
		}
	}
	return rel
}

// nextView は service.Next の結果を応答の形にする（REST と MCP で共通）。
func nextView(lang i18n.Lang, res *service.NextResult) nextJSON {
	out := nextJSON{Action: res.Action, From: res.From, Others: res.Others, Skipped: res.Skipped}
	if out.Others == nil {
		out.Others = []string{}
	}
	if out.Skipped == nil {
		out.Skipped = []service.NextSkip{}
	}
	out.Containers, out.OutsideTypes, out.OtherSessions = res.Containers, res.OutsideTypes, res.OtherSessions
	if out.Containers == nil {
		out.Containers = []string{}
	}
	if out.OutsideTypes == nil {
		out.OutsideTypes = []string{}
	}
	if out.OtherSessions == nil {
		out.OtherSessions = []string{}
	}
	out.CrossPathSessions = res.CrossPathSessions
	if out.CrossPathSessions == nil {
		out.CrossPathSessions = []string{}
	}
	var b strings.Builder
	if res.Issue == nil {
		out.Message = i18n.T(lang, "server.api.next.none")
		out.Next = []string{}
		b.WriteString(out.Message)
		writeHeld(lang, &b, out.Containers, out.OutsideTypes)
		writeOtherSessions(lang, &b, out.OtherSessions)
		writeCrossPath(lang, &b, out.CrossPathSessions)
		writeSkipped(lang, &b, out.Skipped)
		out.Text = b.String()
		return out
	}
	it := res.Issue
	d := toDetailJSON(it)
	out.Issue = &d
	out.Acceptance = service.AcceptanceCriteria(it.Row.Doc.BodyMain)
	out.Verify = nextVerifyOf(res.Verify)
	out.Related = relatedOf(res.Set, it.Item)
	id := it.Item.ID
	switch res.Action {
	case "resumed":
		out.Message = i18n.T(lang, "server.api.next.resumed", "id", id, "title", it.Item.Title)
	case "started":
		out.Message = i18n.T(lang, "server.api.next.started", "id", id, "from", res.From, "title", it.Item.Title)
	case "would_start":
		out.Message = i18n.T(lang, "server.api.next.would_start", "id", id, "title", it.Item.Title, "status", res.From)
	}
	out.Next = []string{
		i18n.T(lang, "server.api.next.step_comment", "id", id),
	}
	if out.Verify != nil {
		out.Next = append(out.Next, i18n.T(lang, "server.api.next.step_verify", "command", out.Verify.Command))
	}
	out.Next = append(out.Next,
		i18n.T(lang, "server.api.next.step_close", "id", id),
		i18n.T(lang, "server.api.next.step_next"),
	)
	if len(d.Traces) == 0 && it.Item.Type != "requirement" && it.Item.Type != "epic" {
		out.Next = append(out.Next, i18n.T(lang, "server.api.next.step_traces", "id", id))
	}
	b.WriteString(out.Message + "\n")
	if len(out.Others) > 0 {
		b.WriteString(i18n.T(lang, "server.api.next.others", "ids", strings.Join(out.Others, ", ")) + "\n")
	}
	writeHeld(lang, &b, out.Containers, out.OutsideTypes)
	writeOtherSessions(lang, &b, out.OtherSessions)
	writeCrossPath(lang, &b, out.CrossPathSessions)
	writeSkipped(lang, &b, out.Skipped)
	b.WriteString("\n" + i18n.T(lang, "server.api.next.related_heading") + "\n")
	line := func(label string, refs []relatedRefJSON) {
		if len(refs) == 0 {
			fmt.Fprintf(&b, "%s: -\n", label)
			return
		}
		for _, r := range refs {
			if r.Missing {
				fmt.Fprintf(&b, "%s: %s\n", label, i18n.T(lang, "server.api.next.ref_missing", "id", r.ID))
			} else {
				fmt.Fprintf(&b, "%s: %s [%s・%s] %s\n", label, r.ID, r.Type, r.Status, r.Title)
			}
		}
	}
	var parent []relatedRefJSON
	if out.Related.Parent != nil {
		parent = []relatedRefJSON{*out.Related.Parent}
	}
	line(i18n.T(lang, "server.api.next.label_parent"), parent)
	line("blocked_by", out.Related.BlockedBy)
	line("traces", out.Related.Traces)
	line(i18n.T(lang, "server.api.next.label_children"), out.Related.Children)
	b.WriteString("\n" + i18n.T(lang, "server.api.next.acceptance_heading") + "\n")
	if out.Acceptance == "" {
		b.WriteString(i18n.T(lang, "server.api.next.acceptance_missing", "id", id) + "\n")
	} else {
		b.WriteString(out.Acceptance + "\n")
	}
	if res.Verify != nil {
		b.WriteString("\n" + i18n.T(lang, "server.api.next.verify_heading", "command", res.Verify.Command) + "\n")
		if res.Verify.Problem != nil {
			b.WriteString(res.Verify.Problem.Message + "\n")
		} else {
			for _, c := range res.Verify.Commands {
				b.WriteString("- " + c + "\n")
			}
			b.WriteString(res.Verify.LastLine + "\n")
		}
	}
	fmt.Fprintf(&b, "\n%s\n%s\n\n%s\n", i18n.T(lang, "server.api.next.body_heading", "version", it.Row.Version),
		strings.TrimRight(it.Markdown, "\n"), i18n.T(lang, "server.api.next.steps_heading"))
	for _, n := range out.Next {
		b.WriteString("- " + n + "\n")
	}
	out.Text = strings.TrimRight(b.String(), "\n")
	return out
}

// writeHeld は着手中として扱わなかった自分の In Progress を 1 行ずつ書く。
// none のときはメッセージの後に続くので、先頭で改行する。
func writeHeld(lang i18n.Lang, b *strings.Builder, containers, outsideTypes []string) {
	if len(containers) == 0 && len(outsideTypes) == 0 {
		return
	}
	if s := b.String(); s != "" && !strings.HasSuffix(s, "\n") {
		b.WriteString("\n")
	}
	if len(containers) > 0 {
		b.WriteString(i18n.T(lang, "server.api.next.held_containers", "ids", strings.Join(containers, ", ")) + "\n")
	}
	if len(outsideTypes) > 0 {
		b.WriteString(i18n.T(lang, "server.api.next.held_types", "ids", strings.Join(outsideTypes, ", ")) + "\n")
	}
}

// writeOtherSessions は、同じ利用者の別のセッションが着手中のものを注記する（summary・list と同じ文面）。
// 担当者欄では見分けられないので、next の出力でも名前を出して横取りを防ぐ。
func writeOtherSessions(lang i18n.Lang, b *strings.Builder, ids []string) {
	if len(ids) == 0 {
		return
	}
	if s := b.String(); s != "" && !strings.HasSuffix(s, "\n") {
		b.WriteString("\n")
	}
	b.WriteString(i18n.T(lang, "server.api.issue.other_session_note", "ids", strings.Join(ids, "・")) + "\n")
}

// writeCrossPath は、別の経路（CLI / MCP）で着手されたものを注記する（着手中としては返している）。
func writeCrossPath(lang i18n.Lang, b *strings.Builder, ids []string) {
	if len(ids) == 0 {
		return
	}
	if s := b.String(); s != "" && !strings.HasSuffix(s, "\n") {
		b.WriteString("\n")
	}
	b.WriteString(i18n.T(lang, "server.api.issue.cross_path_note", "ids", strings.Join(ids, "・")) + "\n")
}

func writeSkipped(lang i18n.Lang, b *strings.Builder, skipped []service.NextSkip) {
	for _, sk := range skipped {
		rule := sk.Rule
		if rule == "" {
			rule = i18n.T(lang, "server.api.next.skip_conflict")
		}
		b.WriteString("\n" + i18n.T(lang, "server.api.next.skipped", "id", sk.ID, "rule", rule, "reason", sk.Reason))
	}
	if len(skipped) > 0 {
		b.WriteString("\n")
	}
}

type nextRequest struct {
	DryRun         bool     `json:"dry_run"`
	Comment        string   `json:"comment"`
	OverrideReason string   `json:"override_reason"`
	Types          []string `json:"types"`
	Assignee       string   `json:"assignee"` // 着手と同時に設定する担当
}

// apiNext は POST /projects/{slug}/next。dry_run でも editor 以上を求める（着手できない利用者に候補を示しても使えない）。
func (s *Server) apiNext(w http.ResponseWriter, r *http.Request) {
	pr, role, ok := s.projectFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	if !canWrite(role) {
		lang := reqLang(r)
		s.serviceError(w, r, forbidden(lang, pr.Slug, i18n.T(lang, "server.api.err.what_next")))
		return
	}
	var req nextRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	res, err := s.svc.Next(r.Context(), actor(r), pr, service.NextOptions{DryRun: req.DryRun, Comment: req.Comment,
		OverrideReason: req.OverrideReason, Types: req.Types, Assignee: req.Assignee})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	v := nextView(reqLang(r), res)
	if res.Action == "started" {
		v.UsageNotice = s.usageNotice(r.Context(), reqLang(r), actor(r), pr, res.Issue, false)
	}
	if v.Issue != nil {
		w.Header().Set("ETag", etag(v.Issue.Version))
	}
	writeJSON(w, http.StatusOK, v)
}

// --- dist

type distFileJSON struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int    `json:"size"`
}

// distFiles は配布するファイルの一覧。kit/core・kit/loop（「kit/core/skills/…」の相対名）を名前順で並べる。
// 以前は scripts/ の CLI・フックも配っていた（撤去済み。CLI・hook は looptrack の実行ファイルにある）。
func distFiles() ([]distFileJSON, error) {
	names := kitNames()
	out := make([]distFileJSON, 0, len(names))
	for _, n := range names {
		b, err := distRead(n)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(b)
		out = append(out, distFileJSON{Name: n, SHA256: hex.EncodeToString(sum[:]), Size: len(b)})
	}
	return out, nil
}

// kitNames / kitRead は配る kit（kit/core・kit/loop）の名前と本体。テストは kit/loop の fixture に差し替える
// （kit/loop の無い版でも setup・導入状態の loop の扱いを試すため）。
var (
	kitNames = kit.Names
	kitRead  = kit.ReadFile
)

// distRead は配布するファイルの本体。一覧に無い名前は fs.ErrNotExist（埋め込みの他のファイル・テストを出さない）。
func distRead(name string) ([]byte, error) {
	if !strings.HasPrefix(name, "kit/") || !contains(kitNames(), name) {
		return nil, fs.ErrNotExist
	}
	return kitRead(name)
}

// distContentType は本体の Content-Type（拡張子から）。
func distContentType(name string) string {
	switch path.Ext(name) {
	case ".md":
		return "text/markdown; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	}
	return "text/plain; charset=utf-8"
}

// apiDist は GET /dist（配布する kit の一覧と SHA-256・looptrack の実行ファイル）。
func (s *Server) apiDist(w http.ResponseWriter, r *http.Request) {
	files, err := distFiles()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	// binaries: 配布ディレクトリの looptrack。無ければ空の一覧。URL は一覧を取った経路（API か券）に合わせる
	urlBase := s.publicBase(r) + s.cfg.BasePath + "/api/v1/dist/"
	if t := r.PathValue("ticket"); t != "" {
		urlBase = s.publicBase(r) + s.cfg.BasePath + "/setup/" + t + "/"
	}
	out := map[string]any{"files": files, "binaries": s.distBinaries(urlBase)}
	for k, v := range s.distSumsLinks(urlBase) {
		out[k] = v
	}
	writeJSON(w, http.StatusOK, out)
}

// apiDistFile は GET /dist/{name...}（本体。X-Looptrack-SHA256 にハッシュ）。kit の名前は「/」を含む。
// そのままの「/dist/kit/core/skills/issue/SKILL.md」と、1 セグメントにエンコードした「/dist/kit%2Fcore%2F…」のどちらでも取れる。
func (s *Server) apiDistFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if strings.HasPrefix(name, binPrefix) && s.serveDistBinary(w, r, name) {
		return
	}
	b, err := distRead(name)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", i18n.T(reqLang(r), "server.api.err.dist_not_found", "name", name))
		return
	}
	sum := sha256.Sum256(b)
	w.Header().Set("Content-Type", distContentType(name))
	w.Header().Set("X-Looptrack-SHA256", hex.EncodeToString(sum[:]))
	w.Write(b)
}
