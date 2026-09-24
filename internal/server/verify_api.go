package server

import (
	"net/http"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

// 検証コマンド（A-1。DESIGN.md §5-8）。サーバはコマンドを実行しない。
// GET は本文の節から取り出したコマンドと直近の記録を返し、POST は CLI（looptrack issue verify）が手元で実行した結果を記録する。
// MCP の report_verify（mcp_verify.go）も POST と同じ service.RecordVerify を通る（経路 mcp は自己申告の印が付く）。

// verifyLastJSON は直近の記録の要約（next の verify.last）。
type verifyLastJSON struct {
	At      string `json:"at"`
	OK      bool   `json:"ok"`
	Passed  int    `json:"passed"`
	Failed  int    `json:"failed"`
	Current bool   `json:"current"` // 現在の本文に対するものか
	// SelfReported は MCP（report_verify）から送られた記録（AI の自己申告）か（CLI の記録は false で省く）
	SelfReported bool `json:"self_reported,omitempty"`
	// Cached は結果キャッシュの印（(cached)）があった注記（無ければ省く）
	Cached bool `json:"cached,omitempty"`
}

// verifyLastDetailJSON は GET …/verify の last（results の出力つき）。
type verifyLastDetailJSON struct {
	verifyLastJSON
	Via        string               `json:"via"`
	BodySHA256 string               `json:"body_sha256"`
	Host       string               `json:"host"`
	Workspace  string               `json:"workspace"`
	Results    []store.VerifyResult `json:"results"`
}

// nextVerifyJSON は next の応答の verify（節が無ければ null）。
type nextVerifyJSON struct {
	Commands   []string        `json:"commands"`
	BodySHA256 string          `json:"body_sha256"`
	Last       *verifyLastJSON `json:"last"`
	Command    string          `json:"command"`
	Message    string          `json:"message"`
	// SectionDrift は受け入れ条件の節が更新されたのに検証コマンドの節が起票時のままか（§5-8-4。false なら省く）
	SectionDrift bool `json:"section_drift,omitempty"`
}

// verifyPlanJSON は GET /issues/{id}/verify の応答（MCP verify_issue の構造化結果も同じ）。
type verifyPlanJSON struct {
	ID         string                `json:"id"`
	Commands   []string              `json:"commands"`
	BodySHA256 string                `json:"body_sha256"`
	Last       *verifyLastDetailJSON `json:"last"`
	Current    bool                  `json:"current"`
	Command    string                `json:"command"`
	Message    string                `json:"message"`
	Text       string                `json:"text"`
	// SectionDrift は受け入れ条件の節が更新されたのに検証コマンドの節が起票時のままか（§5-8-4。false なら省く）
	SectionDrift bool `json:"section_drift,omitempty"`
	// Timezone は last.at を描く時間帯（IANA 名）。CLI はこれで時刻を描く（台帳・レポートと同じ形）。
	Timezone string `json:"timezone"`
}

func verifyLastOf(p *service.VerifyPlan) *verifyLastJSON {
	if p.Last == nil {
		return nil
	}
	return &verifyLastJSON{At: p.Last.At.UTC().Format(time.RFC3339), OK: p.Last.OK, Passed: p.Last.Passed, Failed: p.Last.Failed, Current: p.Current,
		SelfReported: service.SelfReported(p.Last), Cached: p.Last.Cached}
}

func nextVerifyOf(p *service.VerifyPlan) *nextVerifyJSON {
	if p == nil {
		return nil
	}
	return &nextVerifyJSON{Commands: p.Commands, BodySHA256: p.BodySHA256, Last: verifyLastOf(p), Command: p.Command, Message: p.Message, SectionDrift: p.SectionDrift}
}

func verifyPlanView(p *service.VerifyPlan, loc *time.Location) verifyPlanJSON {
	out := verifyPlanJSON{Timezone: loc.String(), ID: p.ID, Commands: p.Commands, BodySHA256: p.BodySHA256, Current: p.Current, Command: p.Command, Message: p.Message, Text: p.Text, SectionDrift: p.SectionDrift}
	if l := verifyLastOf(p); l != nil {
		res := p.Last.Results
		if res == nil {
			res = []store.VerifyResult{}
		}
		out.Last = &verifyLastDetailJSON{verifyLastJSON: *l, Via: p.Last.Via, BodySHA256: p.Last.BodySHA256, Host: p.Last.Host, Workspace: p.Last.Workspace, Results: res}
	}
	return out
}

// planFor はイシューの検証コマンドと直近の記録を読む（閲覧の権限で足りる）。
func (s *Server) planFor(r *http.Request, pr store.Project, row store.IssueRow) (*service.VerifyPlan, error) {
	it, err := s.svc.Detail(r.Context(), pr, row.ID)
	if err != nil {
		return nil, err
	}
	return s.svc.PlanVerify(r.Context(), s.db, reqLang(r), row.ID, it.Item.ID, it.Row.Doc.BodyMain)
}

// apiGetVerify は GET /issues/{id}/verify。節が無ければ 404 ではなく commands: [] と message（CLI は exit 2）。
// 上限（20 コマンド・1,000 文字）を超えていれば 400（verify_commands_too_many / verify_command_too_long）。
func (s *Server) apiGetVerify(w http.ResponseWriter, r *http.Request) {
	pr, row, ok := s.issueFor(w, r, false)
	if !ok {
		return
	}
	plan, err := s.planFor(r, pr, row)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	if plan.Problem != nil {
		s.serviceError(w, r, &service.Error{Kind: service.Invalid, Code: plan.Problem.Code, Message: plan.Problem.Message})
		return
	}
	writeJSON(w, http.StatusOK, verifyPlanView(plan, s.svc.Loc))
}

type verifyRequest struct {
	BodySHA256 string               `json:"body_sha256"`
	Results    []store.VerifyResult `json:"results"`
	Host       string               `json:"host"`
	Workspace  string               `json:"workspace"`
}

// apiPostVerify は POST /issues/{id}/verify（editor 以上）。コメントと issue_events kind verify を 1 トランザクションで残す。
func (s *Server) apiPostVerify(w http.ResponseWriter, r *http.Request) {
	pr, row, ok := s.issueFor(w, r, true)
	if !ok {
		return
	}
	var req verifyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	a := actor(r)
	rec, err := s.svc.RecordVerify(r.Context(), a, pr, row.ID, service.VerifyInput{BodySHA256: req.BodySHA256, Results: req.Results, Host: req.Host, Workspace: req.Workspace})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	it := rec.Issue
	w.Header().Set("ETag", etag(it.Row.Version))
	d := rec.Detail
	body := map[string]any{"issue": toIssueJSON(it), "seq": d.CommentSeq, "ok": d.OK, "passed": d.Passed, "failed": d.Failed,
		"body_sha256": d.BodySHA256, "comment": it.Row.Doc.Comments[len(it.Row.Doc.Comments)-1].Content, "message": i18n.T(reqLang(r), "server.api.verify.recorded", "id", it.Item.ID)}
	if n := s.usageNotice(r.Context(), reqLang(r), a, pr, it, false); n != "" {
		body["usage_notice"] = n
	}
	writeJSON(w, http.StatusCreated, body)
}
