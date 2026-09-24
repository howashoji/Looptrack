package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// プロジェクトの参加の変更と担当の付け替え（設計は DESIGN.md §5-1「参加を外すときの担当」）。
//
// 参加を外す・役割を viewer に下げると、その人はそのプロジェクトで担当にできなくなる。その人が担当の
// 未クローズのイシューがあれば、代わりの担当（replacement: そのプロジェクトで担当にできる人の login か、
// 未設定にする "-"）を指定しないと変えられない。指定すると、参加の変更と同じトランザクションで担当を付け替え、
// イシューごとに assign（reason: 参加の解除 / 役割の変更（viewer））を残す。

// MemberRoles はプロジェクトの役割。
var MemberRoles = []string{"viewer", "editor", "admin"}

// 担当の付け替えの理由（assign イベントの reason）。
const (
	ReasonMemberRemoved = "参加の解除"
	ReasonMemberViewer  = "役割の変更（viewer）"
)

// ReplacementNeeded は、代わりの担当を指定せずに参加を外そうとしたときの拒否の中身（画面が件数と選択欄を出す）。
type ReplacementNeeded struct {
	Issues     []string           // 担当している未クローズのイシューの ID（番号順）
	Candidates []store.Assignable // 代わりにできる人（対象の本人を除く）
}

// ReplacementError は代わりの担当が要るときの拒否（Kind Rejected・Code "replacement_required" の *Error を包む）。
type ReplacementError struct {
	Need ReplacementNeeded
	Err  *Error
}

func (e *ReplacementError) Error() string { return e.Err.Message }
func (e *ReplacementError) Unwrap() error { return e.Err }

// MembershipResult は参加の変更の結果。
type MembershipResult struct {
	Message    string   // 画面・CLI に出す 1 行
	Reassigned []string // 担当を付け替えたイシューの ID
	To         string   // 付け替え先の login（未設定は ""）
}

// losesAssignable は、役割の変更で担当にできなくなるか（role が "" なら解除）。
func losesAssignable(role string) bool { return role == "" || role == "viewer" }

// SetMembership はプロジェクトの参加を付ける・変える・外す（role が "" なら外す）。
// 外す・viewer に下げるときに対象が担当の未クローズのイシューがあれば replacement（login か "-"）が要る。
// 指定が無ければ *ReplacementError（件数と候補）を返す。
// 担当を付け替えないとき（担当が無い・editor 以上への変更）は replacement を無視する。
func (s *Service) SetMembership(ctx context.Context, a Actor, p store.Project, target store.User, role, replacement string) (*MembershipResult, error) {
	role = strings.TrimSpace(role)
	if role != "" && !domain.Valid(MemberRoles, role) {
		return nil, errm(Invalid, "invalid_argument", i18n.M("service.err.invalid.role_required"))
	}
	replacement = strings.TrimSpace(replacement)
	now := s.Now()
	res := &MembershipResult{}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		res.Reassigned, res.To = nil, ""
		var issues []int64
		var ids []string
		if losesAssignable(role) {
			var err error
			if issues, ids, err = openAssigned(ctx, tx, p.ID, target.ID); err != nil {
				return err
			}
		}
		var to store.Assignee
		if len(issues) > 0 {
			members, err := store.AssignableMembers(ctx, tx, p.ID)
			if err != nil {
				return err
			}
			var cands []store.Assignable
			for _, m := range members {
				if m.UserID != target.ID {
					cands = append(cands, m)
				}
			}
			if replacement == "" {
				return &ReplacementError{Need: ReplacementNeeded{Issues: ids, Candidates: cands}, Err: &Error{Kind: Rejected, Code: "replacement_required",
					Message: fmt.Sprintf("%s は %s で未クローズのイシュー %d 件（%s）の担当です。%sには代わりの担当者（担当にできる参加者か、- で未設定）を指定してください",
						target.Login, p.Slug, len(ids), strings.Join(ids, ", "), membershipWhat(role))}}
			}
			if replacement != Unassign {
				found := false
				for _, c := range cands {
					if c.Login == replacement {
						to, found = store.Assignee{UserID: c.UserID, Login: c.Login}, true
					}
				}
				if !found {
					var logins []string
					for _, c := range cands {
						logins = append(logins, c.Login)
					}
					list := strings.Join(logins, ", ")
					if list == "" {
						list = "なし"
					}
					return errm(Invalid, "invalid_argument", i18n.M("service.err.invalid.replacement_not_member", "slug", p.Slug, "target", target.Login, "replacement", replacement, "list", list))
				}
			}
		}
		if role == "" {
			if err := store.RemoveMember(ctx, tx, p.ID, target.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
		} else if err := store.SetMember(ctx, tx, p.ID, target.ID, role); err != nil {
			return err
		}
		reason := ReasonMemberRemoved
		if role != "" {
			reason = ReasonMemberViewer
		}
		for _, id := range issues {
			row, err := store.LoadDocumentForUpdate(ctx, tx, id)
			if err != nil {
				return err
			}
			// 付け替えの前に参加を変えたので、元の担当は印付き（from_inactive）
			if row.Assignee.UserID != target.ID {
				continue // 行ロックを待つ間に替わった
			}
			if err := store.SetAssignee(ctx, tx, id, to.UserID); err != nil {
				return err
			}
			domain.SetField(row.Doc, "updated", s.stamp(now))
			if _, err := store.SaveDocument(ctx, tx, row.ID, row.Version, row.Doc, len(row.Doc.Comments), s.author(a, now)); err != nil {
				return err
			}
			detail := map[string]any{"from": target.Login, "to": to.Login, "reason": reason, "from_inactive": true, "op": "member"}
			if err := store.InsertEvent(ctx, tx, store.Event{ProjectID: p.ID, IssueID: id, Kind: "assign", SessionID: a.SessionID,
				Author: s.author(a, now), Detail: detail}); err != nil {
				return err
			}
			res.Reassigned = append(res.Reassigned, domain.FromDocument(row.Doc).ID)
		}
		res.To = to.Login
		return nil
	})
	if err != nil {
		return nil, err
	}
	if role == "" {
		res.Message = p.Slug + " の " + target.Login + " の権限を外しました。"
	} else {
		res.Message = p.Slug + " の " + target.Login + " の権限を " + role + " にしました。"
	}
	if len(res.Reassigned) > 0 {
		res.Message += fmt.Sprintf("担当していた %d 件（%s）の担当を %s にしました。", len(res.Reassigned), strings.Join(res.Reassigned, ", "), orUnset(res.To))
	}
	return res, nil
}

func membershipWhat(role string) string {
	if role == "" {
		return "参加を外す"
	}
	return "役割を viewer にする"
}

func orUnset(login string) string {
	if login == "" {
		return "未設定"
	}
	return login
}

// openAssigned はプロジェクトで userID が担当の未クローズのイシューを、行をロックして番号順に返す。
func openAssigned(ctx context.Context, tx *sql.Tx, projectID, userID int64) ([]int64, []string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, display_id FROM issues WHERE project_id = ? AND assignee_user_id = ?
AND status NOT IN ('Done', 'Canceled') ORDER BY number FOR UPDATE`, projectID, userID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var ids []int64
	var names []string
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, nil, err
		}
		ids = append(ids, id)
		names = append(names, name)
	}
	return ids, names, rows.Err()
}
