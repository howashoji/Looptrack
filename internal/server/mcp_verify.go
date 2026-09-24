package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

// MCP の verify_issue と report_verify（DESIGN.md §5-8-2）。サーバはコマンドを実行しない。
// verify_issue は検証コマンドの一覧と直近の記録を返す（実行も記録もしない）。文言は GET /issues/{id}/verify と同じ
// （service.PlanVerify）で、MCP では末尾に report_verify の使い方を足す。
// report_verify は AI が手元で実行した結果を POST /issues/{id}/verify と同じ service.RecordVerify で記録する
// （本文の版 409・コマンドの並び 400・viewer 403・節なし 400 も同じ）。経路が mcp なので「MCP の自己申告」の印が付く。

// reportVerifyResult は report_verify の results[] の 1 件（store.VerifyResult に説明を付けたもの）。
type reportVerifyResult struct {
	Command    string `json:"command" jsonschema:"server.mcp.arg.report_verify.results.command"`
	Status     string `json:"status" jsonschema:"server.mcp.arg.report_verify.results.status"`
	ExitCode   *int   `json:"exit_code,omitempty" jsonschema:"server.mcp.arg.report_verify.results.exit_code"`
	DurationMS int64  `json:"duration_ms,omitempty" jsonschema:"server.mcp.arg.report_verify.results.duration_ms"`
	OutputTail string `json:"output_tail,omitempty" jsonschema:"server.mcp.arg.report_verify.results.output_tail"`
	Cached     bool   `json:"cached,omitempty" jsonschema:"server.mcp.arg.report_verify.results.cached"`
}

type reportVerifyIn struct {
	issueIDArg
	BodySHA256 string               `json:"body_sha256" jsonschema:"server.mcp.arg.report_verify.body_sha256"`
	Results    []reportVerifyResult `json:"results" jsonschema:"server.mcp.arg.report_verify.results"`
	Host       string               `json:"host,omitempty" jsonschema:"server.mcp.arg.report_verify.host"`
	Workspace  string               `json:"workspace,omitempty" jsonschema:"server.mcp.arg.report_verify.workspace"`
}

// reportVerifyHint は verify_issue の本文の末尾に足す report_verify の使い方。
func reportVerifyHint(bodySHA256 string) string {
	return fmt.Sprintf("CLI で記録できないとき（MCP だけの環境）は report_verify で送れる: 上のコマンドを手元のシェルで順に全部実行し（失敗しても止めない）、"+
		"id・body_sha256（%s）・results（コマンドごとに command（一覧の文字列そのまま・同じ順）・status（ok / fail / timeout / skipped）・exit_code・duration_ms・output_tail（出力の末尾））を渡す。"+
		"この記録には「%s」の印が付き、コメント・show・summary・next で見分けられる（検証の規則では数える）",
		bodySHA256, service.SelfReportedLabel)
}

func (s *Server) addVerifyMCPTools(srv *mcp.Server, lang i18n.Lang, ro *mcp.ToolAnnotations) {
	addTool(srv, lang, &mcp.Tool{Name: "verify_issue", Annotations: ro,
		Description: i18n.T(lang, "server.mcp.tool.verify_issue")},
		func(ctx context.Context, req *mcp.CallToolRequest, in issueIDArg) (*mcp.CallToolResult, any, error) {
			c, err := mcpCallOf(req)
			if err != nil {
				return nil, nil, err
			}
			pr, row, err := s.resolveIssue(ctx, c.lang, c.p.User, in.ID, in.Project, false)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "verify_issue", err)
			}
			it, err := s.svc.Detail(ctx, pr, row.ID)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "verify_issue", err)
			}
			plan, err := s.svc.PlanVerify(ctx, s.db, c.lang, row.ID, it.Item.ID, it.Row.Doc.BodyMain)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "verify_issue", err)
			}
			if len(plan.Commands) == 0 || plan.Problem != nil {
				return nil, nil, errors.New(plan.Message) // 節なし・上限超過は isError（CLI の exit 2 / 400 と同じ文言）
			}
			return result(plan.Text+"\n"+reportVerifyHint(plan.BodySHA256), verifyPlanView(plan, s.svc.Loc)), nil, nil
		})

	notDestructive := false
	addTool(srv, lang, &mcp.Tool{Name: "report_verify", Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive},
		Description: i18n.T(lang, "server.mcp.tool.report_verify")},
		func(ctx context.Context, req *mcp.CallToolRequest, in reportVerifyIn) (*mcp.CallToolResult, any, error) {
			c, pr, row, err := s.mcpIssue(ctx, req, in.issueIDArg, "report_verify")
			if err != nil {
				return nil, nil, err
			}
			res := make([]store.VerifyResult, len(in.Results))
			for i, r := range in.Results {
				res[i] = store.VerifyResult{Command: r.Command, Status: r.Status, ExitCode: r.ExitCode, DurationMS: r.DurationMS, OutputTail: r.OutputTail, Cached: r.Cached}
			}
			rec, err := s.svc.RecordVerify(ctx, c.actor, pr, row.ID, service.VerifyInput{BodySHA256: in.BodySHA256, Results: res, Host: in.Host, Workspace: in.Workspace})
			if err != nil {
				return nil, nil, s.toolError(c.lang, "report_verify", err)
			}
			it, d := rec.Issue, rec.Detail
			comment := it.Row.Doc.Comments[len(it.Row.Doc.Comments)-1].Content
			notice := s.usageNotice(ctx, c.lang, c.actor, pr, it, false)
			text := i18n.T(c.lang, "server.mcp.verify.recorded", "label", service.SelfReportedLabel, "id", it.Item.ID, "comment", comment)
			return result(withNotice(text, notice), map[string]any{"issue": toIssueJSON(it), "seq": d.CommentSeq, "ok": d.OK, "passed": d.Passed,
				"failed": d.Failed, "body_sha256": d.BodySHA256, "self_reported": d.SelfReported, "comment": comment, "usage_notice": notice}), nil, nil
		})
}
