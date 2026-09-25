package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/mdformat"
	"github.com/howashoji/looptrack/internal/store"
)

// 検証コマンド（A-1。DESIGN.md §5-8）。
// サーバはコマンドを実行しない（各プロジェクトのチェックアウトが無く、任意コマンドの実行は RCE の入口になる）。
// 実行と計測は CLI（looptrack issue verify）が手元で行い、結果を POST /issues/{id}/verify で記録する。
// MCP の verify_issue は一覧と直近の結果を返して CLI の実行を促す（文言は VerifyPlan で CLI の GET と共有）。
// MCP の report_verify は AI が手元で実行した結果を同じ RecordVerify で記録する。経路が mcp の記録は「MCP の自己申告」
// の印を detail（self_reported）とコメントに付け、表示（next・verify_issue・summary）でも見分けられるようにする。
// 規則 verify.require_on_close は自己申告も数える（§5-8-2）。

// VerifyPlan は 1 イシューの検証コマンドと直近の記録（GET /issues/{id}/verify・MCP verify_issue・next で共通）。
type VerifyPlan struct {
	ID         string
	Commands   []string // 節が無ければ空
	BodySHA256 string
	Last       *store.VerifyEvent // 直近の記録（無ければ nil）
	Current    bool               // Last が現在の本文に対するものか
	Problem    *domain.VerifyLimitError
	Command    string // 実行するコマンド（looptrack issue verify <ID>）
	LastLine   string // 直近の記録の 1 行（next・GET・MCP で共通）
	Message    string // 次にすること（節なし・上限超過ならその理由）
	Text       string // 人が読む全文（CLI と MCP で同じ）
	// SectionDrift は「受け入れ条件の節は起票の後に更新されたのに、検証コマンドの節が起票時のまま」か
	// （§5-8-4。判定は domain.SectionDrift の 1 か所。節のハッシュを持たない古いイシューは常に false）
	SectionDrift bool
	// SectionDriftNote は SectionDrift のときの注記（そうでなければ空）
	SectionDriftNote string
}

// SelfReportedLabel は MCP（report_verify）から送られた記録のコメントに付ける印。
// コメントは DB に残る記録なので訳さない（記録した人の言語で変えない）。画面・MCP・summary に出す印は
// 対訳の service.verify.last.self_reported で、要求の言語で出す。
const SelfReportedLabel = "MCP の自己申告"

// CachedLabel は出力に結果キャッシュの印（(cached)）があった記録のコメントに付ける注記
// （SelfReportedLabel と同じく記録なので訳さない。表示は service.verify.last.cached）。
// 失敗にはしない（キャッシュが返っても、そのコマンド自体は走って 0 で終わっているため）。
const CachedLabel = "結果キャッシュあり"

// SelfReported は記録が MCP の自己申告か（detail の印、または issue_events の経路 mcp）。
func SelfReported(ev *store.VerifyEvent) bool {
	return ev != nil && (ev.SelfReported || ev.Via == "mcp")
}

// State は規則 verify.require_on_close の判定に使う直近の記録の状態。
func (p *VerifyPlan) State() string {
	switch {
	case p.Last == nil:
		return domain.VerifyNone
	case !p.Current:
		return domain.VerifyStale
	case !p.Last.OK:
		return domain.VerifyFailed
	}
	return domain.VerifyCurrent
}

// PlanVerify は本文から検証コマンドを取り出し、直近の記録と突き合わせる（変更はしない）。
// lang は案内の文面（LastLine・Message・Text・SectionDriftNote・Problem）の言語（要求の言語）。
func (s *Service) PlanVerify(ctx context.Context, q store.Queryer, lang i18n.Lang, issueID int64, displayID, bodyMain string) (*VerifyPlan, error) {
	p := &VerifyPlan{ID: displayID, Commands: domain.VerifyCommands(bodyMain), BodySHA256: domain.BodySHA256(bodyMain), Command: domain.VerifyCommand(displayID)}
	if p.Commands == nil {
		p.Commands = []string{}
	}
	last, err := store.LastVerify(ctx, q, issueID)
	if err != nil {
		return nil, err
	}
	p.Last = last
	p.Current = last != nil && last.BodySHA256 == p.BodySHA256
	p.Problem = domain.CheckVerifyCommands(lang, displayID, p.Commands)
	if len(p.Commands) > 0 {
		if p.SectionDrift, err = s.sectionDrift(ctx, q, issueID, bodyMain); err != nil {
			return nil, err
		}
		if p.SectionDrift {
			p.SectionDriftNote = domain.SectionDriftMsg(displayID).In(lang)
		}
	}
	p.LastLine = s.lastLine(lang, p)
	p.Message, p.Text = s.verifyText(lang, p)
	return p, nil
}

// sectionDrift は起票時（節のハッシュを持つ最初の記録）の節のハッシュと現在の本文を突き合わせる。
// 記録が無ければ（この仕組みより前に起票されたイシュー）判定しない。
func (s *Service) sectionDrift(ctx context.Context, q store.Queryer, issueID int64, bodyMain string) (bool, error) {
	raw, err := store.BaselineSections(ctx, q, issueID)
	if err != nil {
		return false, err
	}
	return driftFrom(raw, bodyMain), nil
}

// driftFrom は起票時の記録（detail.sections の JSON。無ければ nil）と現在の本文から食い違いを判定する。
// 記録が無い・読めないときは警告を出さない（この仕組みより前に起票されたイシューがここに来る。
// 古い記録で落ちてはいけないので、JSON が想定と違っても verify 自体は通す）。
func driftFrom(raw []byte, bodyMain string) bool {
	if len(raw) == 0 {
		return false
	}
	var base domain.SectionHashes
	if err := json.Unmarshal(raw, &base); err != nil {
		return false
	}
	return domain.SectionDrift(base, domain.NewSectionHashes(bodyMain))
}

// lastLine は直近の記録の 1 行（表示用。lang の文面）。コメントに残す文面（verifyComment）とは別。
func (s *Service) lastLine(lang i18n.Lang, p *VerifyPlan) string {
	if p.Last == nil {
		return i18n.T(lang, "service.verify.last.none")
	}
	l := p.Last
	head := i18n.T(lang, "service.verify.last.passed", "passed", l.Passed, "total", l.Passed+l.Failed)
	if l.Failed > 0 {
		head = i18n.T(lang, "service.verify.last.passed_failed", "passed", l.Passed, "total", l.Passed+l.Failed, "failed", l.Failed)
	}
	state := i18n.T(lang, "service.verify.last.current")
	if !p.Current {
		state = i18n.T(lang, "service.verify.last.stale")
	}
	if SelfReported(l) {
		state = i18n.T(lang, "service.verify.last.note", "state", state, "note", i18n.M("service.verify.last.self_reported"))
	}
	if l.Cached {
		state = i18n.T(lang, "service.verify.last.note", "state", state, "note", i18n.M("service.verify.last.cached"))
	}
	return i18n.T(lang, "service.verify.last.line", "at", l.At.In(s.Loc).Format("2006-01-02 15:04"), "head", head, "state", state)
}

func (s *Service) verifyText(lang i18n.Lang, p *VerifyPlan) (message, text string) {
	if len(p.Commands) == 0 {
		m := domain.NoVerifyCommandsMsg(p.ID).In(lang)
		return m, m
	}
	if p.Problem != nil {
		return p.Problem.Message, p.Problem.Message
	}
	message = i18n.T(lang, "service.verify.run", "command", p.Command)
	var b strings.Builder
	b.WriteString(i18n.T(lang, "service.verify.heading", "id", p.ID, "count", len(p.Commands), "sha", p.BodySHA256[:8]) + "\n")
	for i, c := range p.Commands {
		fmt.Fprintf(&b, "%d. %s\n", i+1, c)
	}
	b.WriteString(p.LastLine + "\n")
	if p.SectionDriftNote != "" {
		b.WriteString(p.SectionDriftNote + "\n")
	}
	b.WriteString(message)
	return message, b.String()
}

// Next は next（next.go）に、着手したイシューの検証コマンド（§5-8-1。節が無ければ nil）を添える。
func (s *Service) Next(ctx context.Context, a Actor, p store.Project, opt NextOptions) (*NextResult, error) {
	res, err := s.next(ctx, a, p, opt)
	if err != nil || res.Issue == nil {
		return res, err
	}
	plan, err := s.PlanVerify(ctx, s.DB, a.Lang, res.Issue.Row.ID, res.Issue.Item.ID, res.Issue.Row.Doc.BodyMain)
	if err != nil {
		return nil, err
	}
	if len(plan.Commands) > 0 {
		res.Verify = plan
	}
	return res, nil
}

// VerifyInput は verify の記録の要求（POST /issues/{id}/verify）。
type VerifyInput struct {
	BodySHA256 string
	Results    []store.VerifyResult
	Host       string
	Workspace  string
}

var verifyStatuses = []string{"ok", "fail", "timeout", "skipped"}

// VerifyRecord は記録の結果。
type VerifyRecord struct {
	Issue  *Issue
	Detail store.VerifyDetail
}

// RecordVerify は CLI（または MCP の report_verify）が手元で実行した結果を、コメントと issue_events kind verify として 1 トランザクションで残す。
// 検査: ① body_sha256 が現在の本文と違えば 409 body_changed ② コマンドが現在の節と同じ順で同じでなければ 400
// ③ 出力はマスクして 4,096 バイトに切る ④ クローズ済みにも記録できる（コメントの追記と同じ）。拒否したときは何も記録しない。
func (s *Service) RecordVerify(ctx context.Context, a Actor, p store.Project, issueID int64, in VerifyInput) (*VerifyRecord, error) {
	if strings.TrimSpace(in.BodySHA256) == "" {
		return nil, errm(Invalid, "invalid_argument", i18n.M("service.err.invalid.body_sha_required"))
	}
	if len(in.Results) == 0 {
		return nil, errm(Invalid, "invalid_argument", i18n.M("service.err.invalid.results_empty"))
	}
	for i, r := range in.Results {
		if !domain.Valid(verifyStatuses, r.Status) {
			return nil, errm(Invalid, "invalid_argument", i18n.M("service.err.invalid.result_status", "index", i, "statuses", strings.Join(verifyStatuses, " / "), "given", r.Status))
		}
		if r.DurationMS < 0 {
			return nil, errm(Invalid, "invalid_argument", i18n.M("service.err.invalid.duration_negative", "index", i))
		}
	}
	host, workspace := cleanName(in.Host), cleanName(in.Workspace)
	rules, err := rulesOf(p)
	if err != nil {
		return nil, err
	}
	var detail store.VerifyDetail
	it, err := s.mutate(ctx, a, p, issueID, 0, func(_ *sql.Tx, doc *mdformat.Document, cur domain.Issue, now string) (change, error) {
		sum := domain.BodySHA256(doc.BodyMain)
		if in.BodySHA256 != sum {
			return change{}, errm(Conflict, "body_changed", i18n.M("service.err.conflict.body_changed", "id", cur.ID, "command", domain.VerifyCommand(cur.ID)))
		}
		cmds := domain.VerifyCommands(doc.BodyMain)
		if len(cmds) == 0 {
			return change{}, errm(Invalid, "no_verify_commands", domain.NoVerifyCommandsMsg(cur.ID))
		}
		if e := domain.CheckVerifyCommands(a.Lang, cur.ID, cmds); e != nil {
			return change{}, errm(Invalid, e.Code, e.Msg)
		}
		if !sameCommands(cmds, in.Results) {
			return change{}, errm(Invalid, "verify_commands_mismatch", i18n.M("service.err.verify_mismatch", "id", cur.ID, "n", len(cmds), "command", domain.VerifyCommand(cur.ID)))
		}
		detail = newVerifyDetail(sum, host, workspace, a.Via == "mcp", in.Results)
		text := verifyComment(detail)
		if v := rules.CheckText(a.Lang, cur.ID, "", text); v != nil {
			return change{}, ruleError(v)
		}
		domain.AppendComment(doc, now, text)
		detail.CommentSeq = len(doc.Comments)
		m, err := toMap(detail)
		if err != nil {
			return change{}, err
		}
		return change{kind: "verify", detail: m}, nil
	})
	if err != nil {
		return nil, err
	}
	return &VerifyRecord{Issue: it, Detail: detail}, nil
}

// newVerifyDetail は記録の detail を組み立てる: 出力をマスクして切り、成功・失敗を数え、
// 結果キャッシュの注記（results のどれかに cached があれば detail にも付く）をまとめる。
// 注記は成否・件数には数えない（失敗にはしない）。
func newVerifyDetail(sum, host, workspace string, selfReported bool, results []store.VerifyResult) store.VerifyDetail {
	d := store.VerifyDetail{BodySHA256: sum, Host: host, Workspace: workspace,
		Results: make([]store.VerifyResult, len(results)), SelfReported: selfReported}
	for i, r := range results {
		r.OutputTail = domain.CleanVerifyOutput(r.OutputTail)
		d.Results[i] = r
		if r.Cached {
			d.Cached = true
		}
		if r.Status == "ok" {
			d.Passed++
		} else {
			d.Failed++
		}
	}
	d.OK = d.Failed == 0
	return d
}

func sameCommands(cmds []string, results []store.VerifyResult) bool {
	if len(cmds) != len(results) {
		return false
	}
	for i := range cmds {
		if cmds[i] != results[i].Command {
			return false
		}
	}
	return true
}

// cleanName はホスト名・ディレクトリ名（パスは送らない。§5-6 と同じ）を 1 行 128 文字以内にする。
func cleanName(v string) string {
	v = strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(v))
	if i := strings.LastIndexAny(v, `/\`); i >= 0 {
		v = v[i+1:]
	}
	if r := []rune(v); len(r) > 128 {
		v = string(r[:128])
	}
	return v
}

func toMap(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	err = json.Unmarshal(b, &m)
	return m, err
}

func seconds(ms int64) string { return fmt.Sprintf("%.1f 秒", float64(ms)/1000) }

func inlineCode(s string) string {
	if strings.Contains(s, "`") {
		return "`` " + s + " ``"
	}
	return "`" + s + "`"
}

// verifyComment はコメントの文面（§5-8-3）。DB に残る記録なので、記録した人の言語に依らず同じ文面で書く。出力はコメントに入れない（verify --last と GET …/verify で見る）。
// MCP の自己申告は見出しに印を付ける（「検証コマンド（MCP の自己申告）: …」）。
// 結果キャッシュの印があった記録も同じ形で注記する（「検証コマンド（結果キャッシュあり）: …」）。
func verifyComment(d store.VerifyDetail) string {
	var total int64
	for _, r := range d.Results {
		total += r.DurationMS
	}
	var b strings.Builder
	b.WriteString("検証コマンド")
	var notes []string
	if d.SelfReported {
		notes = append(notes, SelfReportedLabel)
	}
	if d.Cached {
		notes = append(notes, CachedLabel)
	}
	if len(notes) > 0 {
		b.WriteString("（" + strings.Join(notes, "・") + "）")
	}
	fmt.Fprintf(&b, ": %d/%d 成功", d.Passed, len(d.Results))
	if d.Failed > 0 {
		fmt.Fprintf(&b, "・%d 失敗", d.Failed)
	}
	fmt.Fprintf(&b, "（%s・本文 %s）", seconds(total), d.BodySHA256[:8])
	for _, r := range d.Results {
		switch r.Status {
		case "ok", "timeout":
			detail := seconds(r.DurationMS)
			if r.Cached {
				detail += "・" + CachedLabel
			}
			fmt.Fprintf(&b, "\n- %s %s（%s）", r.Status, inlineCode(r.Command), detail)
		case "fail":
			exit := "exit ?"
			if r.ExitCode != nil {
				exit = fmt.Sprintf("exit %d", *r.ExitCode)
			}
			fmt.Fprintf(&b, "\n- fail %s（%s・%s）", inlineCode(r.Command), exit, seconds(r.DurationMS))
		default:
			fmt.Fprintf(&b, "\n- %s %s", r.Status, inlineCode(r.Command))
		}
	}
	return b.String()
}
