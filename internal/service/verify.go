package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/mdformat"
	"github.com/howashoji/looptrack/internal/store"
)

// 検証コマンド（A-1。DESIGN.md §9-3）。
// サーバはコマンドを実行しない（各プロジェクトのチェックアウトが無く、任意コマンドの実行は RCE の入口になる）。
// 実行と計測は CLI（looptrack issue verify）が手元で行い、結果を POST /issues/{id}/verify で記録する。
// MCP の verify_issue は一覧と直近の結果を返して CLI の実行を促す（文言は VerifyPlan で CLI の GET と共有）。
// MCP の report_verify は AI が手元で実行した結果を同じ RecordVerify で記録する。経路が mcp の記録は「MCP の自己申告」
// の印を detail（self_reported）とコメントに付け、表示（next・verify_issue・summary）でも見分けられるようにする。
// 規則 verify.require_on_close は自己申告も数える（DESIGN.md §9-3-2）。

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
	// （DESIGN.md §9-3-4。判定は domain.SectionDrift の 1 か所。節のハッシュを持たない古いイシューは常に false）
	SectionDrift bool
	// SectionDriftNote は SectionDrift のときの注記（そうでなければ空）
	SectionDriftNote string
}

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
	b.WriteString(message + "\n")
	// 末尾はエビデンスの添付の案内（規則の本文は guide の共通規則「エビデンス」。CLI の --list・MCP の verify_issue・GET …/verify が同じ文面を出す。
	// next の text は guide_api.go が別に組むのでこの行は通らない。next には close の段の文面（server.api.next.step_close）で同じ指示を出す）
	b.WriteString(i18n.T(lang, "service.verify.evidence", "command", p.Command, "id", p.ID))
	return message, b.String()
}

// Next は next（next.go）に、着手したイシューの検証コマンド（DESIGN.md §9-3-1。節が無ければ nil）を添える。
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
	// Attachments はエビデンスとして記録に付ける、そのイシューの添付の ID（detail.attachments。空なら付けない）
	Attachments []int64
}

var verifyStatuses = []string{"ok", "fail", "timeout", "skipped"}

// VerifyRecord は記録の結果。
type VerifyRecord struct {
	Issue  *Issue
	Detail store.VerifyDetail
}

// RecordVerify は CLI（または MCP の report_verify）が手元で実行した結果を、コメントと issue_events kind verify として 1 トランザクションで残す。
// 検査: ① body_sha256 が現在の本文と違えば 409 body_changed ② コマンドが現在の節と同じ順で同じでなければ 400
// ③ 出力はマスクして 4,096 バイトに切る ④ クローズ済みにも記録できる（コメントの追記と同じ）
// ⑤ 添付の ID（エビデンス）はそのイシューの消去していない添付だけ（detail.attachments に残す）。拒否したときは何も記録しない。
// lang は記録した利用者の言語（コメントの文面がこれで決まる。DB に残る文面は書いた利用者の言語・DESIGN §9-6。Create と同じく呼ぶ側が渡す）。
func (s *Service) RecordVerify(ctx context.Context, a Actor, p store.Project, issueID int64, in VerifyInput, lang i18n.Lang) (*VerifyRecord, error) {
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
	it, err := s.mutate(ctx, a, p, issueID, 0, func(tx *sql.Tx, doc *mdformat.Document, cur domain.Issue, now string) (change, error) {
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
		refs, err := attachmentRefs(ctx, tx, issueID, in.Attachments)
		if err != nil {
			return change{}, err
		}
		detail = newVerifyDetail(sum, host, workspace, a.Via == "mcp", in.Results)
		detail.Attachments = refs
		text := verifyComment(lang, detail) // 添付はコメントの本文に書かない（本文はこれまでと同じ）
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

// cleanName はホスト名・ディレクトリ名（パスは送らない。DESIGN.md §6 と同じ）を 1 行 128 文字以内にする。
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

func inlineCode(s string) string {
	if strings.Contains(s, "`") {
		return "`` " + s + " ``"
	}
	return "`" + s + "`"
}

// seconds は所要時間の表示（CLI の verify の行と同じ文面）。
func seconds(lang i18n.Lang, ms int64) string {
	return i18n.T(lang, "verify.line.seconds", "secs", fmt.Sprintf("%.1f", float64(ms)/1000))
}

// verifyComment はコメントの文面（DESIGN.md §9-3-3）。DB に残る記録なので、記録した利用者の言語（lang）で書き、
// 後から表示の言語を変えても書き換えない（既存の記録はそのときの言語のまま）。出力はコメントに入れない（verify --last と GET …/verify で見る）。
// MCP の自己申告は見出しに印を付ける（「検証コマンド（MCP の自己申告）: …」）。
// 結果キャッシュの印があった記録も同じ形で注記する（「検証コマンド（結果キャッシュあり）: …」）。
// 印と成否の件数は、画面・MCP・summary に出す直近の記録の行と同じ対訳を使う（同じものに別の語を作らない）。
func verifyComment(lang i18n.Lang, d store.VerifyDetail) string {
	var total int64
	for _, r := range d.Results {
		total += r.DurationMS
	}
	join := func(a, b string) string { return i18n.T(lang, "service.verify.comment.join", "a", a, "b", b) }
	title := i18n.T(lang, "service.verify.comment.title")
	var notes []string
	if d.SelfReported {
		notes = append(notes, i18n.T(lang, "service.verify.last.self_reported"))
	}
	if d.Cached {
		notes = append(notes, i18n.T(lang, "service.verify.last.cached"))
	}
	if len(notes) > 0 {
		note := notes[0]
		for _, n := range notes[1:] {
			note = join(note, n)
		}
		title = i18n.T(lang, "service.verify.comment.title_note", "note", note)
	}
	counts := i18n.T(lang, "service.verify.last.passed", "passed", d.Passed, "total", len(d.Results))
	if d.Failed > 0 {
		counts = i18n.T(lang, "service.verify.last.passed_failed", "passed", d.Passed, "total", len(d.Results), "failed", d.Failed)
	}
	var b strings.Builder
	b.WriteString(i18n.T(lang, "service.verify.comment.summary", "title", title, "counts", counts, "secs", seconds(lang, total), "sha", d.BodySHA256[:8]))
	line := func(status, command, detail string) {
		b.WriteString("\n" + i18n.T(lang, "service.verify.comment.line", "status", status, "command", inlineCode(command), "detail", detail))
	}
	for _, r := range d.Results {
		switch r.Status {
		case "ok", "timeout":
			detail := seconds(lang, r.DurationMS)
			if r.Cached {
				detail = join(detail, i18n.T(lang, "service.verify.last.cached"))
			}
			line(r.Status, r.Command, detail)
		case "fail":
			code := "?"
			if r.ExitCode != nil {
				code = strconv.Itoa(*r.ExitCode)
			}
			line("fail", r.Command, i18n.T(lang, "verify.line.exit", "code", code, "secs", seconds(lang, r.DurationMS)))
		default:
			fmt.Fprintf(&b, "\n- %s %s", r.Status, inlineCode(r.Command))
		}
	}
	return b.String()
}
