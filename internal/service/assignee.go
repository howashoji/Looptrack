package service

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/mdformat"
	"github.com/howashoji/looptrack/internal/store"
)

// 担当者（設計は DESIGN.md §5-1）。
//
// 規則（全経路共通。判定は行ロックの中）:
//
//	R1 In Progress にするとき担当が未設定（かつ指定なし）なら、操作した本人を担当にする。
//	R2 担当が「他人」（設定済み・本人でない・印なし）のイシューを In Progress にする・本文 / 項目を更新するのは 422。
//	   override の理由があれば通す。In Progress は担当を本人に替える（引き継ぎ）。本文 / 項目の更新は担当を替えない。
//	R3 担当を明示で変えるとき、他人の担当を替える・外すには override。同じ値なら何もしない。
//	R4 コメント・閲覧・In Progress 以外への状態変更は担当に関係なく従来どおり。

// Unassign は担当の解除を表す指定。
const Unassign = "-"

// AssigneeRule は担当の規則による拒否の rule 名。
const AssigneeRule = "assignee"

// assignPlan は担当の変更の予定（mutate の中で決め、保存と同時に反映する）。
type assignPlan struct {
	from, to     store.Assignee
	auto         bool   // R1（本人を自動で担当にした）
	reason       string // override の理由（引き継いだとき）
	takeover     bool   // 他人の担当を override で替えた
	fromInactive bool   // 印付き（権限を外された）担当から替えた
	op           string // status / update / assign / create
}

// otherActive は担当が「他人」（設定済み・本人でない・今も作業できる）か。
func otherActive(cur store.Assignee, a Actor) bool {
	return cur.UserID != 0 && cur.UserID != a.UserID && !cur.Inactive
}

// 拒否した操作の種類（422 assigned_to_other の文面を操作ごとに分ける）。
const (
	opStart  = iota // In Progress にする（status・next・new）: override で担当が本人に替わる
	opEdit          // 本文・項目の編集（PATCH）: override でも担当は替わらない
	opChange        // 担当の変更・解除（R3）
)

// assignedToOther は R2 / R3 の拒否。文面は操作ごとに、override を付けたときに起きること（担当が替わるか）を示す。
func assignedToOther(id string, cur store.Assignee, op int) *Error {
	var what, how i18n.Msg
	switch op {
	case opEdit:
		what, how = i18n.M("service.assignee.what.edit"), i18n.M("service.assignee.how.edit")
	case opChange:
		what, how = i18n.M("service.assignee.what.change"), i18n.M("service.assignee.how.change")
	default:
		what, how = i18n.M("service.assignee.what.start"), i18n.M("service.assignee.how.start")
	}
	e := errm(Rejected, "assigned_to_other", i18n.M("service.err.assigned_to_other", "id", id, "login", cur.Login, "what", what, "how", how))
	e.Rule, e.Overridable = AssigneeRule, true
	return e
}

// resolveAssignee は担当の指定（me / login / -）を利用者にする。空文字は呼び出し側で「変えない」とする。
func (s *Service) resolveAssignee(ctx context.Context, q store.Queryer, a Actor, p store.Project, spec string) (store.Assignee, error) {
	spec = strings.TrimSpace(spec)
	switch strings.ToLower(spec) {
	case Unassign:
		return store.Assignee{}, nil
	case "me":
		return s.actorAssignee(ctx, q, a)
	}
	members, err := store.AssignableMembers(ctx, q, p.ID)
	if err != nil {
		return store.Assignee{}, err
	}
	var logins []string
	for _, m := range members {
		if m.Login == spec {
			return store.Assignee{UserID: m.UserID, Login: m.Login}, nil
		}
		logins = append(logins, m.Login)
	}
	// 本人を login で指定したときは me と同じに扱う（参加していない利用者は書けないので、ここへ来るのは書ける本人だけ）
	if self, err := s.actorAssignee(ctx, q, a); err == nil && self.Login == spec {
		return self, nil
	}
	var list any = strings.Join(logins, ", ")
	if len(logins) == 0 {
		list = i18n.M("service.word.none")
	}
	return store.Assignee{}, errm(Invalid, "invalid_argument", i18n.M("service.err.invalid.assignee_not_member", "slug", p.Slug, "spec", spec, "list", list))
}

func (s *Service) actorAssignee(ctx context.Context, q store.Queryer, a Actor) (store.Assignee, error) {
	u, err := store.UserByID(ctx, q, a.UserID)
	if err != nil {
		return store.Assignee{}, err
	}
	return store.Assignee{UserID: u.ID, Login: u.Login}, nil
}

// planAssign は担当の変更を決める（R1〜R3）。spec は明示の指定（空なら指定なし）、starting は In Progress にする操作か。
// 変更が無ければ nil。
func (s *Service) planAssign(ctx context.Context, q store.Queryer, a Actor, p store.Project, id string, cur store.Assignee,
	spec string, starting bool, reason, op string) (*assignPlan, error) {
	reason = strings.TrimSpace(reason)
	other := otherActive(cur, a)
	if spec != "" {
		target, err := s.resolveAssignee(ctx, q, a, p, spec)
		if err != nil {
			return nil, err
		}
		if other && (target.UserID != cur.UserID || starting) && reason == "" {
			op := opStart
			if target.UserID != cur.UserID {
				op = opChange
			}
			return nil, assignedToOther(id, cur, op)
		}
		if target.UserID == cur.UserID {
			return nil, nil
		}
		pl := &assignPlan{from: cur, to: target, fromInactive: cur.Inactive && cur.UserID != 0, op: op}
		if other {
			pl.takeover, pl.reason = true, reason
		}
		return pl, nil
	}
	if !starting {
		return nil, nil
	}
	switch {
	case cur.UserID == a.UserID:
		return nil, nil
	case other && reason == "":
		return nil, assignedToOther(id, cur, opStart)
	}
	self, err := s.actorAssignee(ctx, q, a)
	if err != nil {
		return nil, err
	}
	pl := &assignPlan{from: cur, to: self, auto: cur.UserID == 0, fromInactive: cur.Inactive && cur.UserID != 0, op: op}
	if other {
		pl.takeover, pl.reason = true, reason
	}
	return pl, nil
}

// guardEdit は本文・項目の更新の R2。担当が他人なら拒否し、override があれば担当を替えずに通す
// （誰が理由付きで直したかを assignee_override に残す。引き継ぎは assign か In Progress の override で行う）。
func guardEdit(id string, cur store.Assignee, a Actor, reason string) ([]event, error) {
	if !otherActive(cur, a) {
		return nil, nil
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, assignedToOther(id, cur, opEdit)
	}
	return []event{{kind: "assignee_override", detail: map[string]any{"assignee": cur.Login, "reason": reason, "op": "update"}}}, nil
}

// apply は担当の変更を保存し、記録するイベントを返す（mutate が主のイベントの後に残す）。
func (pl *assignPlan) apply(ctx context.Context, tx *sql.Tx, issueID int64) ([]event, error) {
	if pl == nil {
		return nil, nil
	}
	if err := store.SetAssignee(ctx, tx, issueID, pl.to.UserID); err != nil {
		return nil, err
	}
	return pl.events(), nil
}

func (pl *assignPlan) events() []event {
	detail := map[string]any{"from": pl.from.Login, "to": pl.to.Login}
	if pl.auto {
		detail["auto"] = true
	}
	if pl.reason != "" {
		detail["reason"] = pl.reason
	}
	if pl.fromInactive {
		detail["from_inactive"] = true
	}
	out := []event{{kind: "assign", detail: detail}}
	if pl.takeover {
		out = append(out, event{kind: "assignee_takeover", detail: map[string]any{"from": pl.from.Login, "to": pl.to.Login, "reason": pl.reason, "op": pl.op}})
	}
	return out
}

// event は mutate が主のイベントの後に残す追加の記録。
type event struct {
	kind   string
	detail map[string]any
}

// render は表示用の全文（担当があれば frontmatter に assignee を差し込む）。
func render(row store.StoredIssue) string {
	return mdformat.Render(domain.WithAssignee(row.Doc, row.Assignee.Login))
}

// stripAssignee は全文での更新から assignee 行を取り除く。値が現在と違えば拒否（担当は assign で変える）。
func stripAssignee(next *mdformat.Document, cur store.Assignee) error {
	v, ok := domain.StripAssignee(next)
	if ok && strings.TrimSpace(v) != cur.Login {
		return errm(Rejected, "immutable_field", i18n.M("service.err.immutable.assignee_in_body", "current", orDash(cur.Login), "given", orDash(strings.TrimSpace(v))))
	}
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// AssignResult は担当の変更の結果。
type AssignResult struct {
	Issue   *Issue
	From    string // 変更前の login（未設定は ""）
	To      string
	Changed bool
}

// Message は CLI・MCP・画面が出す 1 行（lang は要求の言語）。
func (r *AssignResult) Message(lang i18n.Lang) string {
	if !r.Changed {
		return i18n.T(lang, "service.assignee.unchanged", "id", r.Issue.Item.ID, "to", orDash(r.To))
	}
	return i18n.T(lang, "service.assignee.changed", "id", r.Issue.Item.ID, "from", orDash(r.From), "to", orDash(r.To))
}

// Assign は担当を変える（R3）。spec は me / login / -（解除）。クローズ済みは変えられない。
func (s *Service) Assign(ctx context.Context, a Actor, p store.Project, issueID int64, spec, overrideReason string) (*AssignResult, error) {
	if strings.TrimSpace(spec) == "" {
		return nil, errm(Invalid, "invalid_argument", i18n.M("service.err.invalid.assignee_required"))
	}
	// 変わらないなら何も記録しない（ロックの外で先に確かめる。競合してもロックの中で判定し直す）
	cur, err := store.LoadDocument(ctx, s.DB, issueID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errm(NotFound, "not_found", i18n.M("service.err.not_found.issue"))
	}
	if err != nil {
		return nil, err
	}
	it := domain.FromDocument(cur.Doc)
	if it.IsClosed() {
		return nil, closedAssign(it)
	}
	target, err := s.resolveAssignee(ctx, s.DB, a, p, spec)
	if err != nil {
		return nil, err
	}
	if target.UserID == cur.Assignee.UserID {
		return &AssignResult{Issue: &Issue{Project: p, Row: cur, Item: it, Markdown: render(cur)}, From: cur.Assignee.Login, To: cur.Assignee.Login}, nil
	}
	res := &AssignResult{Changed: true}
	out, err := s.mutate(ctx, a, p, issueID, 0, func(tx *sql.Tx, doc *mdformat.Document, it domain.Issue, now string) (change, error) {
		if it.IsClosed() {
			return change{}, closedAssign(it)
		}
		curA, err := store.IssueAssignee(ctx, tx, issueID)
		if err != nil {
			return change{}, err
		}
		pl, err := s.planAssign(ctx, tx, a, p, it.ID, curA, spec, false, overrideReason, "assign")
		if err != nil {
			return change{}, err
		}
		if pl == nil { // ロックを待つ間に同じ値になった
			pl = &assignPlan{from: curA, to: curA, op: "assign"}
		}
		if err := store.SetAssignee(ctx, tx, issueID, pl.to.UserID); err != nil {
			return change{}, err
		}
		res.From, res.To = pl.from.Login, pl.to.Login
		domain.SetField(doc, "updated", now)
		evs := pl.events()
		return change{kind: evs[0].kind, detail: evs[0].detail, extra: evs[1:]}, nil
	})
	if err != nil {
		return nil, err
	}
	res.Issue = out
	return res, nil
}

func closedAssign(it domain.Issue) *Error {
	return errm(Rejected, "closed", i18n.M("service.err.closed.assignee", "id", it.ID, "status", it.Status))
}
