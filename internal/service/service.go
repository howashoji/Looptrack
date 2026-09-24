// Package service はイシューの参照・変更の共通処理。REST API・MCP・Web 画面が同じ処理を通る（DESIGN.md §4）。
//
// 変更はすべて 1 トランザクションで行う: 対象行をロック → Document に復元 → domain の変更関数
// （以前の CLI と比較テスト済み）を適用 → 保存（コメントは追記のみ）→ issue_events に記録 → 版番号を進める。
package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // コンテナに tzdata が無くても Asia/Tokyo を使う

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/mdformat"
	"github.com/howashoji/looptrack/internal/store"
)

// Kind はエラーの種類（HTTP 等の応答に対応づける）。
type Kind int

const (
	Invalid              Kind = iota + 1 // 入力値の誤り
	NotFound                             // 対象が無い（権限が無い場合も含む）
	Forbidden                            // 閲覧はできるが変更の権限が無い
	Conflict                             // 版の不一致・採番の衝突
	Rejected                             // 規則により受け付けない（クローズ済みの編集等）
	PreconditionRequired                 // 版の指定が無い
)

// Error は利用者に返すエラー。Message は次に何をすべきかを含める。
//
// msg は同じ文面を「まだ言語を決めていない形」で持つ（errm / erri で作ったときだけ）。Unwrap で
// 返すので、表示する側が i18n.Text(lang, err) を通せば利用者の言語で出る（REST の serviceError・
// MCP の toolError・next の見送りの理由はどれもそうしている）。errm / erri の Message は ID を持たない
// 経路（ログ・Error()）の予備で、日本語で作る。ルール違反（ruleError）と next の全件見送りの Message は
// Actor.Lang（要求の言語）で作る（プロジェクトが設定した文面には ID が無く、表示側で訳し直せないため）。
type Error struct {
	Kind        Kind
	Code        string
	Message     string
	Current     *Issue // Conflict のとき現在の内容
	Rule        string // ルール違反のときのルール名
	Overridable bool   // 理由付きで上書きできるルール違反か
	msg         error  // i18n の ID を持つ文面（errf で作ったときだけ。無ければ nil）
}

// Unwrap は ID を持つ文面を返す。i18n.Text が errors.As でこれを見つけて訳す。
func (e *Error) Unwrap() error { return e.msg }

func ruleError(v *domain.Violation) *Error {
	e := &Error{Kind: Rejected, Code: "rule_violation", Message: v.Message, Rule: v.Rule, Overridable: v.Overridable}
	if v.Msg.ID != "" { // 既定の文面。プロジェクトが設定した文面には ID が無いので、そのまま（訳さずに）出す
		e.msg = &i18n.Error{Msg: v.Msg}
	}
	return e
}

// rulesOf はプロジェクトのルールを読む。壊れた設定は（ルールを黙って無効にしないため）エラーにする。
func rulesOf(p store.Project) (*domain.Rules, error) {
	r, err := domain.ParseRules(p.Rules)
	if err != nil {
		// domain の理由は ID を持つ error なので、Wrapf で包んで表示側の i18n.Text まで届ける
		return nil, i18n.Wrapf(err, "service.err.rules_config", "slug", p.Slug)
	}
	return r, nil
}

func (e *Error) Error() string { return e.Message }

func errorf(kind Kind, code, format string, args ...any) *Error {
	return &Error{Kind: kind, Code: code, Message: fmt.Sprintf(format, args...)}
}

// errm は文面を ID で持つエラーを作る（errorf の 2 言語版）。
//
// 文面は呼ぶ側が i18n.M("id", kv...) で作って渡す。ID をこの関数の引数で受け取ると、訳の
// 抜けを見つける検査（i18n の lint_test）が ID を追えなくなるため（caveat-i18n.md の 1）。
// Message には日本語を入れておく（表示側が i18n.Text を通していない経路は、これまでどおり
// 日本語が出る）。
func errm(kind Kind, code string, m i18n.Msg) *Error {
	return &Error{Kind: kind, Code: code, Message: m.In(i18n.JA), msg: &i18n.Error{Msg: m}}
}

// erri は、ほかの層（domain など）が返した「ID を持つ error」をそのまま service.Error にする。
// ID はその層のリテラルなので、ここで引数に取らない（i18n の lint が ID を追える）。
func erri(kind Kind, code string, err error) *Error {
	return &Error{Kind: kind, Code: code, Message: i18n.Text(i18n.JA, err), msg: err}
}

// Actor は変更の主体。
type Actor struct {
	UserID    int64
	TokenID   int64
	Via       string // cli / web / mcp / api
	SessionID string // Claude Code のセッション ID 等（鮮度ガード用）
	Agent     string // MCP の接続してきた AI（claude-code / codex / copilot / other。判定できなければ空）
	// SessionKind は SessionID の種類（X-Looptrack-Session-Kind。空＝会話のセッション ID）。
	// SessionKindHost は器（デスクトップ版のセッションの窓）の ID で、会話記録と結び付かない（§9-5）。
	SessionKind string
	// Lang はこの要求の表示の言語（REST は reqLang・MCP は mcpLang で決めて入れる。判定は server の langFor の 1 か所）。
	// ルール違反・next の見送りの理由など、service が作って返す文面をこの言語で作る。
	// 空（テストなどで指定しないとき）は i18n.T の既定どおり日本語になる。
	// DB に保存する文面（起票の雛形など）の言語はこれとは別に、書いた利用者の言語を呼ぶ側が渡す。
	Lang i18n.Lang
}

// SessionKindHost は、セッション ID が器（デスクトップ版のセッションの窓）の ID であることを表す
// （CLI が X-Looptrack-Session-Kind: host で伝える）。並行するセッションは見分けられるが、
// 会話記録のファイル名と一致しないのでトークン情報を付けられない。したがって付与の対象にしない（DESIGN.md §9-5）。
const SessionKindHost = "host"

// MCPSessionPrefix は、MCP の接続 ID（Mcp-Session-Id）をセッション ID として使うときに付ける印。
// クライアントが名乗るセッション ID（X-Looptrack-Session。環境変数由来で、同じ AI の会話の間は変わらない）とは
// 種類が違うので、混ぜて比べない（同じ AI が CLI と MCP を併用すると、同じセッションでも値が違うため）。
const MCPSessionPrefix = "mcp-conn:"

// ComparableSessions は 2 つのセッション ID を「同じか違うか」で比べてよいかを返す。
// どちらかが分からない（空）か、種類が違う（片方だけが MCP の接続 ID）なら比べない
// （比べると、見分けられないものを「別のセッション」と決めつけてしまう）。
func ComparableSessions(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return strings.HasPrefix(a, MCPSessionPrefix) == strings.HasPrefix(b, MCPSessionPrefix)
}

// CrossPath は、2 つのセッション ID が「種類の違う経路のもの」かを返す（判定は ComparableSessions の裏返し）。
// どちらも分かっているのに比べられない＝同じセッションかどうかを言えない状態なので、
// 黙って自分のものとして扱うのではなく、呼び出した側に「判定できない」と示す。
func CrossPath(a, b string) bool {
	return a != "" && b != "" && !ComparableSessions(a, b)
}

// usageSession は「その会話」（トークン情報が付いているかの判定。DESIGN.md §5-4・§9-5）に使うセッション ID。
// MCP の SessionID は接続 ID（Mcp-Session-Id）で、会話（スナップショットの session_id）とは結び付かないので空にし、
// 従来どおり「その利用者のスナップショット」で判定する（接続を張り直すたびに未付与と見なさない）。
func (a Actor) usageSession() string {
	if a.Via == "mcp" {
		return ""
	}
	return a.SessionID
}

// Service は DB と時計を持つ。
type Service struct {
	DB  *sql.DB
	Now func() time.Time
	Loc *time.Location // created / updated / コメント見出しの時刻（以前の CLI と同じローカル時刻。既定 Asia/Tokyo）
}

// New は Service を作る。
func New(db *sql.DB, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		loc = time.FixedZone("JST", 9*60*60)
	}
	return &Service{DB: db, Now: now, Loc: loc}
}

func (s *Service) stamp(t time.Time) string { return t.In(s.Loc).Format("2006-01-02 15:04") }

// Issue は 1 イシューの内容。
type Issue struct {
	Project  store.Project
	Row      store.StoredIssue
	Item     domain.Issue
	Markdown string // Detail で読んだときだけ
}

// Closed はクローズ済みか。
func (it *Issue) Closed() bool { return it.Item.IsClosed() }

// ProjectIssues はプロジェクトの全イシュー（frontmatter のみ）を読み、domain.Set にする。
func (s *Service) ProjectIssues(ctx context.Context, p store.Project) (*domain.Set, []Issue, error) {
	rows, err := store.LoadFronts(ctx, s.DB, p.ID)
	if err != nil {
		return nil, nil, err
	}
	items := make([]domain.Issue, 0, len(rows))
	out := make([]Issue, 0, len(rows))
	for _, r := range rows {
		it := domain.FromDocument(r.Doc)
		items = append(items, it)
		out = append(out, Issue{Project: p, Row: r, Item: it})
	}
	return domain.NewSet(items), out, nil
}

// Locate は ID でイシューを引く（projectID が 0 でなければそのプロジェクトに限る）。
func (s *Service) Locate(ctx context.Context, displayID string, projectID int64) (store.IssueRow, error) {
	r, err := store.FindIssue(ctx, s.DB, displayID, projectID, false)
	if errors.Is(err, store.ErrNotFound) {
		return r, notFound(displayID)
	}
	return r, err
}

func notFound(id string) *Error {
	return errm(NotFound, "not_found", i18n.M("service.err.not_found.issue_id", "id", strings.ToUpper(id)))
}

// Detail は 1 イシューの全文を読む。
func (s *Service) Detail(ctx context.Context, p store.Project, issueID int64) (*Issue, error) {
	r, err := store.LoadDocument(ctx, s.DB, issueID)
	if err != nil {
		return nil, err
	}
	return &Issue{Project: p, Row: r, Item: domain.FromDocument(r.Doc), Markdown: render(r)}, nil
}

// inTx は fn をトランザクションで実行する。デッドロックは数回まで再試行する。
func (s *Service) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	for attempt := 0; ; attempt++ {
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		err = fn(tx)
		if err == nil {
			err = tx.Commit()
		}
		if err == nil {
			return nil
		}
		_ = tx.Rollback()
		if attempt < 4 && store.IsRetryable(err) {
			time.Sleep(time.Duration(attempt+1) * 10 * time.Millisecond)
			continue
		}
		return err
	}
}

// CreateInput は起票の入力。
type CreateInput = domain.NewIssueInput

// Create は採番して起票する。lang は起票した利用者の言語（本文の雛形の見出しがこれで決まる・DESIGN §9-6）。
func (s *Service) Create(ctx context.Context, a Actor, p store.Project, in CreateInput, lang i18n.Lang) (*Issue, error) {
	in.Defaults()
	if err := in.Validate(); err != nil {
		return nil, erri(Invalid, "invalid_argument", err)
	}
	if strings.ContainsAny(in.Title, "\r\n") || strings.ContainsAny(in.Parent, "\r\n") {
		return nil, errm(Invalid, "invalid_argument", i18n.M("service.err.invalid.newline_in_title"))
	}
	var err error
	for _, l := range []struct {
		name string
		v    *[]string
	}{{"labels", &in.Labels}, {"blocked_by", &in.BlockedBy}, {"traces", &in.Traces}, {"refs", &in.Refs}} {
		if *l.v, err = cleanList(l.name, *l.v); err != nil {
			return nil, err
		}
	}
	rules, err := rulesOf(p)
	if err != nil {
		return nil, err
	}
	if v := rules.CheckText(lang, "", "", in.Body); v != nil { // id が空 = 起票（domain が起票用の文面を選ぶ）
		return nil, ruleError(v)
	}
	// 担当: 指定があれば検査し、無くて In Progress で起票するなら本人（R1）
	var assign *assignPlan
	if spec := strings.TrimSpace(in.Assignee); spec != "" && spec != Unassign {
		target, err := s.resolveAssignee(ctx, s.DB, a, p, spec)
		if err != nil {
			return nil, err
		}
		assign = &assignPlan{to: target, op: "create"}
	} else if spec == "" && in.Status == "In Progress" {
		self, err := s.actorAssignee(ctx, s.DB, a)
		if err != nil {
			return nil, err
		}
		assign = &assignPlan{to: self, auto: true, op: "create"}
	}
	now := s.Now()
	var out *Issue
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		var verifyOverride *domain.Override
		if rules != nil {
			if v := rules.CheckTransition(lang, domain.Transition{Type: in.Type, To: in.Status, Creating: true}); v != nil {
				return ruleError(v)
			}
			// 検証コマンドを持つ本文で Done に起票する場合（verify の記録はまだ無い）
			if len(domain.VerifyCommands(in.Body)) > 0 {
				vo, v := rules.CheckVerify(lang, domain.VerifyCheck{To: in.Status, HasCommands: true, State: domain.VerifyNone, Creating: true, OverrideReason: in.OverrideReason})
				if v != nil {
					return ruleError(v)
				}
				verifyOverride = vo
			}
		}
		n, err := store.NextNumber(ctx, tx, p.ID)
		if err != nil {
			return err
		}
		id := domain.FormatID(p.Prefix, p.Width, n)
		doc := domain.NewDocument(id, in, s.stamp(now), lang)
		fileName := domain.FileName(id, in.Title)
		issueID, err := store.InsertDocument(ctx, tx, p.ID, n, fileName, doc, a.Via)
		if store.IsDuplicateKey(err) {
			return errm(Conflict, "number_conflict", i18n.M("service.err.conflict.number_used", "id", id))
		}
		if err != nil {
			return err
		}
		if err := store.InsertEvent(ctx, tx, store.Event{ProjectID: p.ID, IssueID: issueID, Kind: "create", SessionID: a.SessionID,
			// sections は本文の節ごとのハッシュ（§5-8-4）。起票時の値が、後の食い違いの判定の基準になる
			Author: s.author(a, now), Detail: map[string]any{"status": in.Status, "type": in.Type, "sections": domain.NewSectionHashes(doc.BodyMain)}}); err != nil {
			return err
		}
		if err := s.recordOverride(ctx, tx, a, p, issueID, now, verifyOverride); err != nil {
			return err
		}
		evs, err := assign.apply(ctx, tx, issueID)
		if err != nil {
			return err
		}
		if err := s.recordEvents(ctx, tx, a, p, issueID, now, evs); err != nil {
			return err
		}
		row := store.StoredIssue{ID: issueID, ProjectID: p.ID, Number: n, FileName: fileName, Version: 1, Doc: doc}
		if assign != nil {
			row.Assignee = assign.to
		}
		out = &Issue{Project: p, Row: row, Item: domain.FromDocument(doc), Markdown: render(row)}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) author(a Actor, now time.Time) store.Author {
	return store.Author{UserID: a.UserID, TokenID: a.TokenID, Via: a.Via, At: now, Agent: a.Agent, SessionKind: a.SessionKind}
}

// change は変更の記録（イベントの kind と detail、ルールを上書きした場合はその内容、主のイベントの後に残す追加の記録）。
type change struct {
	kind      string
	detail    map[string]any
	overrides []*domain.Override
	extra     []event // 担当の変更（assign / assignee_takeover）・担当を替えない編集の上書き（assignee_override）
}

// recordEvents は追加の記録を順に残す。
func (s *Service) recordEvents(ctx context.Context, tx *sql.Tx, a Actor, p store.Project, issueID int64, now time.Time, evs []event) error {
	for _, e := range evs {
		if err := store.InsertEvent(ctx, tx, store.Event{ProjectID: p.ID, IssueID: issueID, Kind: e.kind, SessionID: a.SessionID,
			Author: s.author(a, now), Detail: e.detail}); err != nil {
			return err
		}
	}
	return nil
}

// recordOverride はルールの上書きを rule_override イベントとして残す（上書きした規則ごとに 1 件）。
func (s *Service) recordOverride(ctx context.Context, tx *sql.Tx, a Actor, p store.Project, issueID int64, now time.Time, overrides ...*domain.Override) error {
	for _, o := range overrides {
		if o == nil {
			continue
		}
		detail := map[string]any{"rule": o.Rule, "reason": o.Reason}
		if err := store.InsertEvent(ctx, tx, store.Event{ProjectID: p.ID, IssueID: issueID, Kind: "rule_override", SessionID: a.SessionID,
			Author: s.author(a, now), Detail: detail}); err != nil {
			return err
		}
	}
	return nil
}

// mutate は対象行をロックして Document を変更・保存し、イベントを記録する。
// fn は変更内容を返す。version が 0 でなければ版を確認する。
func (s *Service) mutate(ctx context.Context, a Actor, p store.Project, issueID int64, version int,
	fn func(tx *sql.Tx, doc *mdformat.Document, it domain.Issue, now string) (change, error)) (*Issue, error) {
	now := s.Now()
	var out *Issue
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		row, err := store.LoadDocumentForUpdate(ctx, tx, issueID)
		if errors.Is(err, store.ErrNotFound) {
			return errm(NotFound, "not_found", i18n.M("service.err.not_found.issue"))
		}
		if err != nil {
			return err
		}
		if version != 0 && row.Version != version {
			cur := &Issue{Project: p, Row: row, Item: domain.FromDocument(row.Doc), Markdown: render(row)}
			e := errm(Conflict, "version_mismatch", i18n.M("service.err.conflict.version", "id", cur.Item.ID, "given", version, "current", row.Version))
			e.Current = cur
			return e
		}
		existing := len(row.Doc.Comments)
		ch, err := fn(tx, row.Doc, domain.FromDocument(row.Doc), s.stamp(now))
		if err != nil {
			return err
		}
		v, err := store.SaveDocument(ctx, tx, row.ID, row.Version, row.Doc, existing, s.author(a, now))
		if err != nil {
			return err
		}
		if err := store.InsertEvent(ctx, tx, store.Event{ProjectID: p.ID, IssueID: row.ID, Kind: ch.kind, SessionID: a.SessionID,
			Author: s.author(a, now), Detail: ch.detail}); err != nil {
			return err
		}
		if err := s.recordEvents(ctx, tx, a, p, row.ID, now, ch.extra); err != nil {
			return err
		}
		if err := s.recordOverride(ctx, tx, a, p, row.ID, now, ch.overrides...); err != nil {
			return err
		}
		row.Version = v
		if row.Assignee, err = store.IssueAssignee(ctx, tx, row.ID); err != nil { // fn が担当を変えた場合
			return err
		}
		out = &Issue{Project: p, Row: row, Item: domain.FromDocument(row.Doc), Markdown: render(row)}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Comment はコメントを追記する（クローズ済みにも追記できる。以前の CLI と同じ）。
func (s *Service) Comment(ctx context.Context, a Actor, p store.Project, issueID int64, text string) (*Issue, error) {
	rules, err := rulesOf(p)
	if err != nil {
		return nil, err
	}
	return s.mutate(ctx, a, p, issueID, 0, func(_ *sql.Tx, doc *mdformat.Document, it domain.Issue, now string) (change, error) {
		if v := rules.CheckText(a.Lang, it.ID, "", text); v != nil {
			return change{}, ruleError(v)
		}
		domain.AppendComment(doc, now, text)
		return change{kind: "comment", detail: map[string]any{"seq": len(doc.Comments)}}, nil
	})
}

// StatusResult は状態変更の結果。
type StatusResult struct {
	Issue *Issue
	From  string
	// AssigneeChanged は同時に担当が変わったか。AssigneeFrom は変更前の login、
	// AssigneeAuto は In Progress で未設定の担当を本人にした（R1。CLI の出力行には出さない）
	AssigneeChanged bool
	AssigneeFrom    string
	AssigneeAuto    bool
}

// SetStatus は状態を変える。comment があれば同時に追記する（status --comment と同じ）。
func (s *Service) SetStatus(ctx context.Context, a Actor, p store.Project, issueID int64, status, comment, overrideReason string) (*StatusResult, error) {
	return s.setStatus(ctx, a, p, issueID, status, comment, overrideReason, "", "")
}

// SetStatusAssign は SetStatus に担当の指定（me / login / -。空なら指定なし）を加えたもの。
func (s *Service) SetStatusAssign(ctx context.Context, a Actor, p store.Project, issueID int64, status, comment, overrideReason, assignee string) (*StatusResult, error) {
	return s.setStatus(ctx, a, p, issueID, status, comment, overrideReason, "", assignee)
}

// setStatus は SetStatus の本体。expectFrom が空でなければ、ロックした時点の状態がそれと違うとき
// Conflict（status_changed）で何も変えない（next が同じイシューを二重に着手しないため）。
// assignee は担当の指定（空なら指定なし。In Progress にするなら未設定のとき本人・§5-1）。
func (s *Service) setStatus(ctx context.Context, a Actor, p store.Project, issueID int64, status, comment, overrideReason, expectFrom, assignee string) (*StatusResult, error) {
	if err := domain.ValidateValue("status", status, domain.Statuses); err != nil {
		return nil, erri(Invalid, "invalid_argument", err)
	}
	rules, err := rulesOf(p)
	if err != nil {
		return nil, err
	}
	var from string
	var assigned *assignPlan
	it, err := s.mutate(ctx, a, p, issueID, 0, func(tx *sql.Tx, doc *mdformat.Document, cur domain.Issue, now string) (change, error) {
		if expectFrom != "" && cur.Status != expectFrom {
			return change{}, errm(Conflict, "status_changed", i18n.M("service.err.conflict.status_changed", "id", cur.ID, "status", cur.Status))
		}
		curA, err := store.IssueAssignee(ctx, tx, issueID)
		if err != nil {
			return change{}, err
		}
		if expectFrom != "" && otherActive(curA, a) { // next: 判定の後に他人が担当になった。override で引き継がずに見送る
			return change{}, errm(Conflict, "status_changed", i18n.M("service.err.conflict.assignee_taken", "id", cur.ID, "login", curA.Login))
		}
		plan, err := s.planAssign(ctx, tx, a, p, cur.ID, curA, assignee, status == "In Progress", overrideReason, "status")
		if err != nil {
			return change{}, err
		}
		overrides, err := s.checkStatus(ctx, tx, a, rules, p, issueID, doc, cur, status, comment, overrideReason)
		if err != nil {
			return change{}, err
		}
		from = domain.SetStatus(doc, status, now)
		detail := map[string]any{"from": from, "to": status}
		if comment != "" {
			domain.AppendComment(doc, now, comment)
			detail["comment_seq"] = len(doc.Comments)
		}
		extra, err := plan.apply(ctx, tx, issueID)
		if err != nil {
			return change{}, err
		}
		assigned = plan
		return change{kind: "status", detail: detail, overrides: overrides, extra: extra}, nil
	})
	if err != nil {
		return nil, err
	}
	res := &StatusResult{Issue: it, From: from}
	if assigned != nil {
		res.AssigneeChanged, res.AssigneeFrom, res.AssigneeAuto = true, assigned.from.Login, assigned.auto
	}
	return res, nil
}

// UsageTarget は、トークン情報の付与を求める操作か（DESIGN.md §5-4「イベントとの突き合わせ」）。
// MCP と、セッション ID 付きの CLI（コーディング AI の Bash から）が対象。セッション ID の無い CLI は
// 人がターミナルから打った操作、web・api・admin・import は AI の操作ではないため対象外。
// 会話記録と結び付かない種類のセッション ID（器の ID。SessionKindHost）の操作も、経路によらず対象外（付けようがない）。
// 利用者が計測を有効にしたときだけ測れる AI（store.UsageOptInAgents。GitHub Copilot の OpenTelemetry のファイル出力）の操作は、
// その利用者のその AI のスナップショットが直近 store.UsageOptInWindow 以内に届いているときだけ対象（無ければ付けようがない）。
func (s *Service) UsageTarget(ctx context.Context, q store.Queryer, a Actor, projectID int64) (bool, error) {
	if !(a.Via == "mcp" || (a.Via == "cli" && a.SessionID != "")) {
		return false, nil
	}
	// 器のセッション ID（X-Looptrack-Session-Kind: host）は会話記録と結び付かないので、付けようがない（§9-5）。
	// セッションを見分けるために送ることと、トークン情報を付けられることは別の話なので、付与の対象からは外す。
	// 経路では絞らない（CLI でも MCP でも、印の付いたセッション ID なら同じ理由で付けようがない）。
	if a.SessionKind == SessionKindHost {
		return false, nil
	}
	if !store.UsageOptIn(a.Agent) {
		return true, nil
	}
	return store.HasRecentClientUsage(ctx, q, projectID, a.UserID, a.Agent, s.Now().Add(-store.UsageOptInWindow))
}

// hookWindow は「フックが働いている」とみなす直近の期間。
const hookWindow = 7 * 24 * time.Hour

// UsageNotice は、変更操作の応答に載せる付与の指示（経路 ③。DESIGN.md §5-4）。対象外の操作なら空。
// 応答の時点ではこの操作自体のスナップショットはまだ無い（CLI は応答の後に送り、MCP はフックが後から送る）ので、
// 指示は「この後に付かなかったら」実行するもの。CLI は自分の付与に失敗したときだけ表示する。
// MCP は、その利用者のフックが直近 7 日に届いていれば数秒後に付くので出さない（毎回の二重送信を避ける）。
// クローズ（Done / Canceled）で、その会話のトークン情報がまだイシューに 1 件も無いときは、既定（警告のみ）の警告を兼ねる。
func (s *Service) UsageNotice(ctx context.Context, lang i18n.Lang, a Actor, p store.Project, issueID int64, displayID string, closing bool) (string, error) {
	if target, err := s.UsageTarget(ctx, s.DB, a, p.ID); err != nil || !target {
		return "", err
	}
	cmd := domain.UsageAttachCommand(displayID)
	if closing {
		attached, err := store.HasUsageForIssue(ctx, s.DB, issueID, a.UserID, a.usageSession())
		if err != nil {
			return "", err
		}
		if !attached {
			return i18n.T(lang, "service.usage.notice.closed_without", "id", displayID, "command", cmd), nil
		}
	}
	if a.Via == "mcp" {
		hook, err := store.HasRecentHookUsage(ctx, s.DB, p.ID, a.UserID, s.Now().Add(-hookWindow))
		if err != nil || hook {
			return "", err
		}
	}
	return i18n.T(lang, "service.usage.notice.attach", "command", cmd), nil
}

// checkStatus は状態変更をプロジェクト別ルールで判定する（変更はしない）。違反は ruleError。
// 返すのは記録する上書き（遷移の規則と、クローズ時のトークン情報 usage.require_on_close）。
// next の候補の判定（In Progress 化）も同じ関数を通る。
func (s *Service) checkStatus(ctx context.Context, q store.Queryer, a Actor, rules *domain.Rules, p store.Project, issueID int64,
	doc *mdformat.Document, cur domain.Issue, status, comment, overrideReason string) ([]*domain.Override, error) {
	if rules == nil {
		return nil, nil
	}
	tr := domain.Transition{ID: cur.ID, Type: cur.Type, From: cur.Status, To: status, NewComment: comment}
	if doc.Preamble != nil {
		tr.Preamble = *doc.Preamble
	}
	for _, c := range doc.Comments {
		tr.Comments = append(tr.Comments, c.Content)
	}
	if v := rules.CheckTransition(a.Lang, tr); v != nil {
		return nil, ruleError(v)
	}
	if v := rules.CheckText(a.Lang, cur.ID, "", comment); v != nil {
		return nil, ruleError(v)
	}
	var overrides []*domain.Override
	// 受け入れ条件の記入（acceptance.require_on_close）。DB を引かないので verify の前。経路を問わない
	if rules.RequiresAcceptance(status) {
		override, v := rules.CheckAcceptance(a.Lang, domain.AcceptanceCheck{ID: cur.ID, To: status,
			HasSection: domain.HasAcceptanceSection(doc.BodyMain), Filled: domain.AcceptanceFilled(doc.BodyMain), OverrideReason: overrideReason})
		if v != nil {
			return nil, ruleError(v)
		}
		overrides = append(overrides, override)
	}
	// 検証コマンドの記録（verify.require_on_close）。usage の前。経路を問わない
	if rules.RequiresVerify(status) {
		if cmds := domain.VerifyCommands(doc.BodyMain); len(cmds) > 0 {
			plan, err := s.PlanVerify(ctx, q, a.Lang, issueID, cur.ID, doc.BodyMain)
			if err != nil {
				return nil, err
			}
			vc := domain.VerifyCheck{ID: cur.ID, To: status, HasCommands: true, State: plan.State(), OverrideReason: overrideReason}
			if plan.Last != nil {
				vc.Failed = plan.Last.Failed
			}
			override, v := rules.CheckVerify(a.Lang, vc)
			if v != nil {
				return nil, ruleError(v)
			}
			overrides = append(overrides, override)
		}
	}
	// クローズ時のトークン情報（usage.require_on_close）。判定は操作の前なので、
	// この操作自体のスナップショット（CLI の直後の付与・フック）はまだ無い。起票・着手・コメントのどれかで付いていれば通る
	target := false
	if rules.RequiresUsage(status) {
		t, err := s.UsageTarget(ctx, q, a, p.ID)
		if err != nil {
			return nil, err
		}
		target = t
	}
	if target {
		attached, err := store.HasUsageForIssue(ctx, q, issueID, a.UserID, a.usageSession())
		if err != nil {
			return nil, err
		}
		override, v := rules.CheckUsage(a.Lang, domain.UsageCheck{ID: cur.ID, To: status, Target: true, Attached: attached, OverrideReason: overrideReason})
		if v != nil {
			return nil, ruleError(v)
		}
		overrides = append(overrides, override)
	}
	return overrides, nil
}

// Patch は項目の部分更新。nil の項目は変えない。
type Patch struct {
	Title     *string
	Type      *string
	Priority  *string
	Parent    *string
	Labels    *[]string
	BlockedBy *[]string
	Traces    *[]string
	Refs      *[]string
	Markdown  *string // 全文（edit / push）。項目の指定と同時には使えない
	// OverrideReason は担当が他人のイシューを編集する理由（担当は替えない・assignee_override に残る）
	OverrideReason string
}

// Update は項目・本文を更新する。version は必須（楽観ロック）。
// id・created・status・コメント節は変えられない（状態は SetStatus、コメントは Comment）。クローズ済みは変更できない。
func (s *Service) Update(ctx context.Context, a Actor, p store.Project, issueID int64, version int, patch Patch) (*Issue, error) {
	if version <= 0 {
		return nil, errm(PreconditionRequired, "version_required", i18n.M("service.err.version_required"))
	}
	fieldsGiven := patch.Title != nil || patch.Type != nil || patch.Priority != nil || patch.Parent != nil ||
		patch.Labels != nil || patch.BlockedBy != nil || patch.Traces != nil || patch.Refs != nil
	if patch.Markdown != nil && fieldsGiven {
		return nil, errm(Invalid, "invalid_argument", i18n.M("service.err.invalid.markdown_and_fields"))
	}
	if patch.Markdown == nil && !fieldsGiven {
		return nil, errm(Invalid, "invalid_argument", i18n.M("service.err.invalid.no_fields"))
	}
	rules, err := rulesOf(p)
	if err != nil {
		return nil, err
	}
	return s.mutate(ctx, a, p, issueID, version, func(tx *sql.Tx, doc *mdformat.Document, it domain.Issue, now string) (change, error) {
		if it.IsClosed() {
			return change{}, errm(Rejected, "closed", i18n.M("service.err.closed.edit", "id", it.ID, "status", it.Status))
		}
		curA, err := store.IssueAssignee(ctx, tx, issueID)
		if err != nil {
			return change{}, err
		}
		extra, err := guardEdit(it.ID, curA, a, patch.OverrideReason)
		if err != nil {
			return change{}, err
		}
		var changed []string
		if patch.Markdown != nil {
			next, err := checkMarkdown(doc, *patch.Markdown)
			if err != nil {
				return change{}, err
			}
			if err := stripAssignee(next, curA); err != nil {
				return change{}, err
			}
			if v := rules.CheckText(a.Lang, it.ID, doc.BodyMain, next.BodyMain); v != nil {
				return change{}, ruleError(v)
			}
			changed = diffFields(doc, next)
			*doc = *next
		} else {
			var err error
			if changed, err = applyPatch(doc, patch); err != nil {
				return change{}, err
			}
		}
		for _, f := range []struct {
			name, value string
			allowed     []string
		}{{"type", domain.FromDocument(doc).Type, domain.Types}, {"priority", domain.FromDocument(doc).Priority, domain.Priorities}} {
			if err := domain.ValidateValue(f.name, f.value, f.allowed); err != nil {
				return change{}, erri(Invalid, "invalid_argument", err)
			}
		}
		domain.SetField(doc, "updated", now)
		// sections は更新後の本文の節ごとのハッシュ（§5-8-4。起票時の値が無いイシューは、この記録が基準になる）
		return change{kind: "update", detail: map[string]any{"fields": changed, "sections": domain.NewSectionHashes(doc.BodyMain)}, extra: extra}, nil
	})
}

func applyPatch(doc *mdformat.Document, patch Patch) ([]string, error) {
	var changed []string
	scalar := func(key string, v *string) error {
		if v == nil {
			return nil
		}
		if strings.ContainsAny(*v, "\r\n") {
			return errm(Invalid, "invalid_argument", i18n.M("service.err.invalid.newline_in_field", "field", key))
		}
		domain.SetField(doc, key, strings.TrimSpace(*v))
		changed = append(changed, key)
		return nil
	}
	list := func(key string, v *[]string) error {
		if v == nil {
			return nil
		}
		cleaned, err := cleanList(key, *v)
		if err != nil {
			return err
		}
		domain.SetList(doc, key, cleaned)
		changed = append(changed, key)
		return nil
	}
	for _, err := range []error{
		scalar("title", patch.Title), scalar("type", patch.Type), scalar("priority", patch.Priority), scalar("parent", patch.Parent),
		list("labels", patch.Labels), list("blocked_by", patch.BlockedBy), list("traces", patch.Traces), list("refs", patch.Refs),
	} {
		if err != nil {
			return nil, err
		}
	}
	return changed, nil
}

// cleanList はリスト項目の値を、ファイルに書いて読み戻したときと同じ形にする（前後の空白を除き、空の値を捨てる）。
// カンマと改行は frontmatter の 1 行リストを壊すため拒否する。
// ID の項目（blocked_by / traces / refs）は途中に空白を含む値も拒否する（"A B" を 1 要素で保存すると
// 逆引き・ready の判定に出ない。分割せずに拒否するのはカンマと同じ扱いにするため。理由は DESIGN.md §4）。
func cleanList(name string, values []string) ([]string, error) {
	out := []string{}
	for _, v := range values {
		if strings.ContainsAny(v, ",\r\n") {
			return nil, errm(Invalid, "invalid_argument", i18n.M("service.err.invalid.value_comma", "name", name, "value", strconv.Quote(v)))
		}
		if v = strings.TrimSpace(v); v == "" {
			continue
		}
		if domain.IsIDListField(name) && domain.HasSpace(v) {
			return nil, errm(Invalid, "invalid_argument", i18n.M("service.err.invalid.value_space_array", "name", name, "value", strconv.Quote(v), "example", quotedFields(v)))
		}
		out = append(out, v)
	}
	return out, nil
}

// quotedFields は "A B" を ["A", "B"] の表記にする（エラー文の例示用）。
func quotedFields(v string) string {
	var q []string
	for _, p := range strings.Fields(v) {
		q = append(q, fmt.Sprintf("%q", p))
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// checkSpacedIDs は全文の更新で ID の項目に新しく入った「空白を含む値」を拒否する。
// 既に入っている値（補正前の旧データ）は、本文だけを直す更新を妨げないよう通す（補正は looptrack repair-lists）。
func checkSpacedIDs(cur, next *mdformat.Document) error {
	for _, key := range domain.IDListFields {
		f := next.Field(key)
		if f == nil || !f.IsList {
			continue
		}
		var old []string
		if g := cur.Field(key); g != nil {
			old = g.List
		}
		for _, v := range f.List {
			if domain.HasSpace(v) && !slices.Contains(old, v) {
				return errm(Invalid, "invalid_argument", i18n.M("service.err.invalid.value_space_csv", "key", key, "value", strconv.Quote(v), "example", key+": ["+strings.Join(strings.Fields(v), ", ")+"]"))
			}
		}
	}
	return nil
}

// checkMarkdown は全文での更新を検査し、新しい Document を返す。
func checkMarkdown(cur *mdformat.Document, text string) (*mdformat.Document, error) {
	next, err := mdformat.Parse(text)
	if err != nil {
		return nil, errm(Invalid, "invalid_markdown", i18n.M("service.err.invalid.markdown_parse", "reason", err))
	}
	a, b := domain.FromDocument(cur), domain.FromDocument(next)
	for _, k := range []struct{ key, old, new string }{{"id", a.ID, b.ID}, {"created", a.Created, b.Created}, {"status", a.Status, b.Status}} {
		if k.old != k.new {
			// status のときだけ案内の 1 文が付く。文面を組み立てる場所は相手の言語を知らないので、
			// 文字列をつなぐのではなく ID を分ける（ID は必ずリテラルで渡す）。
			if k.key == "status" {
				return nil, errm(Rejected, "immutable_field", i18n.M("service.err.immutable.field_status", "field", k.key, "old", k.old, "new", k.new))
			}
			return nil, errm(Rejected, "immutable_field", i18n.M("service.err.immutable.field", "field", k.key, "old", k.old, "new", k.new))
		}
	}
	if err := checkSpacedIDs(cur, next); err != nil {
		return nil, err
	}
	if cur.HasCommentSection != next.HasCommentSection || !samePreamble(cur.Preamble, next.Preamble) || !commentsPrefix(cur.Comments, next.Comments) {
		return nil, errm(Rejected, "comments_changed", i18n.M("service.err.comments_changed"))
	}
	// 作業コピーを取った後に追記されたコメントは、サーバのものを残す（push --rebase でコメントの取り込みを不要にする）
	if len(next.Comments) < len(cur.Comments) {
		next.Comments = append([]mdformat.Comment(nil), cur.Comments...)
		next.TrailNL = cur.TrailNL
	}
	return next, nil
}

func samePreamble(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// commentsPrefix は next が cur の先頭部分（既存コメントを書き換えず、減らしてもいない古い写し）か。
func commentsPrefix(cur, next []mdformat.Comment) bool {
	if len(next) > len(cur) {
		return false
	}
	for i := range next {
		if cur[i] != next[i] {
			return false
		}
	}
	return true
}

// diffFields は変更された frontmatter のキーと本文の変更を列挙する（イベントの記録用）。
func diffFields(a, b *mdformat.Document) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range b.Front {
		seen[f.Key] = true
		if g := a.Field(f.Key); g == nil || mdformat.Render(&mdformat.Document{Front: []mdformat.Field{*g}}) != mdformat.Render(&mdformat.Document{Front: []mdformat.Field{f}}) {
			out = append(out, f.Key)
		}
	}
	for _, f := range a.Front {
		if !seen[f.Key] {
			out = append(out, f.Key)
		}
	}
	if a.BodyMain != b.BodyMain || a.GapNL != b.GapNL || a.TrailNL != b.TrailNL {
		out = append(out, "body")
	}
	return out
}
