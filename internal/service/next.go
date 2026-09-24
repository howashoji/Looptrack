package service

import (
	"context"
	"errors"
	"strings"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// next（設計は DESIGN.md §5-5）: ループ運用の「着手」を 1 回で行う。
//
// 規則:
//  1. 自分が着手中（In Progress）のイシューがあれば、状態を変えずにそれを返す（resumed）。
//     「自分」は担当が自分。セッション ID があれば、そのうち同じセッションが In Progress にしたものだけ
//     （並行して動く別の AI のものを横取りしない）。担当が未設定の In Progress（取り込んだまま等）は従来どおり
//     着手のイベントの主体で判定する。複数あれば優先度順の先頭を返し、残りは Others に並べる。
//     ただし作業の単位でないものは着手中として返さない: 未クローズの子を持つもの（束ね。epic・要件など。
//     子から進める）は Containers、型が Types に入らないもの（既定は epic。Types の指定は着手中の選び方にも効く）は
//     OutsideTypes に並べ、残りが無ければ規則 2 へ進む。規則 2 では Containers の子孫を先に見る（同じ束ねの中で進める）。
//     同じ利用者の別のセッションが着手したものは OtherSessions に並べ、呼び出した側に「別のセッションが着手中」として示す
//     （担当者欄は同じ利用者なので、状態からはセッション ID でしか見分けられない）。
//  2. 無ければ、着手可能（ready）のうち次を満たすものを優先度順（同順位は ID 昇順）に見る:
//     状態が Todo（Backlog・In Review は対象外）、型が Types に入る（既定は epic 以外）、未クローズの子イシューが無い
//     （子があれば子から着手する）、担当が自分か未設定（他人・権限を外された担当のものは取らない）。
//  3. 候補ごとにプロジェクト別ルールで In Progress への変更を判定し、違反する候補は見送って次を見る
//     最初に通った候補を In Progress にする（started）。
//     DryRun なら変えずに返す（would_start）。
//  4. 候補はあったが全てルールで見送った場合は、最初の違反を 422 で返す（同時コメントで通るルールなら --comment を促す）。
//     候補が無ければ none。

// NextOptions は next の指定。
type NextOptions struct {
	DryRun         bool
	Comment        string   // 着手と同時に残すコメント
	OverrideReason string   // 上書き可能なルール違反を通す理由
	Types          []string // 対象の型（空なら epic 以外）
	Assignee       string   // 着手（started）と同時に設定する担当（me / login。空なら未設定のとき本人）
}

// NextSkip は見送った候補。
type NextSkip struct {
	ID     string `json:"id"`
	Rule   string `json:"rule,omitempty"`
	Reason string `json:"reason"`
}

// NextResult は next の結果。Action は resumed / started / would_start / none。
type NextResult struct {
	Action string
	Issue  *Issue // 全文つき（none のときは nil）
	From   string // started のとき変更前の状態
	Others []string
	// Containers は自分の In Progress のうち束ね（未クローズの子を持つもの）として着手中に扱わなかったもの
	Containers []string
	// OutsideTypes は自分の In Progress のうち Types の対象外で着手中に扱わなかったもの
	OutsideTypes []string
	// OtherSessions は、同じ利用者の別のセッションが In Progress にしたため着手中に扱わなかったもの
	// （担当者欄では見分けられないので、状態として示す。セッション ID が無い経路では常に空）
	OtherSessions []string
	// CrossPathSessions は、別の経路（CLI / MCP）で In Progress にしたもの。着手中としては返すが、
	// 同じセッションかは判定できないので、その旨を呼び出した側に示す
	CrossPathSessions []string
	Skipped           []NextSkip
	Set               *domain.Set // 関連（親・blocked_by・traces・子）の組み立て用
	Verify            *VerifyPlan // 検証コマンド（節が無ければ nil。verify.go の Next が添える）
}

// maxNextCandidates は 1 回の next で判定する候補の上限（ルールで見送り続けるときの打ち切り）。
const maxNextCandidates = 50

// next は規則に従って次のイシューを選び、必要なら In Progress にする（公開の入口は verify.go の Next）。
func (s *Service) next(ctx context.Context, a Actor, p store.Project, opt NextOptions) (*NextResult, error) {
	for _, t := range opt.Types {
		if !domain.Valid(domain.Types, t) {
			return nil, errm(Invalid, "invalid_argument", i18n.M("service.err.invalid.type", "types", strings.Join(domain.Types, " / "), "given", t))
		}
	}
	set, rows, err := s.ProjectIssues(ctx, p)
	if err != nil {
		return nil, err
	}
	rowOf := map[string]int64{}
	assigneeOf := map[string]store.Assignee{}
	for _, r := range rows {
		rowOf[r.Item.ID] = r.Row.ID
		assigneeOf[r.Item.ID] = r.Row.Assignee
	}
	res := &NextResult{Action: "none", Set: set}

	// 1. 自分が着手中のもの
	starters, err := store.InProgressStarters(ctx, s.DB, p.ID)
	if err != nil {
		return nil, err
	}
	var mine []domain.Issue
	startedBy := map[string]store.Starter{}
	for _, st := range starters {
		startedBy[st.DisplayID] = st
	}
	for _, it := range set.List(domain.Filter{Status: "In Progress"}, "priority", false) {
		as, st := assigneeOf[it.ID], startedBy[it.ID]
		// 同じ利用者の、見分けられる別のセッションが着手したものか（判定は markOtherSession と同じ ComparableSessions）。
		// 着手のイベントにセッション ID が無いもの（画面・セッション ID を送らないクライアント・人の操作・
		// 取り込んだまま着手のイベントが無いもの）と、種類の違う ID（CLI のセッション ID と MCP の接続 ID）は
		// 比べられないので、従来どおり自分の着手として扱う（決めつけると、同じ AI が CLI と MCP を併用したときに
		// 片方の着手が消える）
		otherSess := ComparableSessions(a.SessionID, st.SessionID) && st.SessionID != a.SessionID && st.UserID == a.UserID
		crossPath := CrossPath(a.SessionID, st.SessionID) && st.UserID == a.UserID
		var ok bool
		switch {
		case as.UserID == a.UserID: // 担当が自分。自分の別のセッション（または端末）が着手したものは横取りしない
			ok = !otherSess
		case as.UserID == 0: // 担当が未設定（着手のイベントの主体で判定する）
			ok = !otherSess && st.UserID == a.UserID && st.UserID != 0
		}
		if !ok {
			if otherSess { // 取りに行かせないよう名前を出す
				res.OtherSessions = append(res.OtherSessions, it.ID)
			}
			continue
		}
		if crossPath { // 着手中としては返すが、同じセッションかは判定できない
			res.CrossPathSessions = append(res.CrossPathSessions, it.ID)
		}
		switch {
		case hasOpenChild(set, it.ID): // 束ね（epic・要件など未クローズの子があるもの）。作業の単位は子
			res.Containers = append(res.Containers, it.ID)
		case !typeWanted(it.Type, opt.Types): // 型の指定の対象外（既定では epic）
			res.OutsideTypes = append(res.OutsideTypes, it.ID)
		default:
			mine = append(mine, it)
		}
	}
	if len(mine) > 0 {
		mine = domain.SortIssues(mine, "priority", false)
		for _, it := range mine[1:] {
			res.Others = append(res.Others, it.ID)
		}
		if res.Issue, err = s.Detail(ctx, p, rowOf[mine[0].ID]); err != nil {
			return nil, err
		}
		res.Action = "resumed"
		return res, nil
	}

	// 2. 候補
	rules, err := rulesOf(p)
	if err != nil {
		return nil, err
	}
	var candidates []domain.Issue
	for _, it := range set.Ready() {
		if as := assigneeOf[it.ID]; it.Status == "Todo" && typeWanted(it.Type, opt.Types) && !hasOpenChild(set, it.ID) &&
			(as.UserID == 0 || as.UserID == a.UserID) {
			candidates = append(candidates, it)
		}
	}
	candidates = preferDescendants(set, candidates, res.Containers)
	var firstViolation *Error
	for i, it := range candidates {
		if i >= maxNextCandidates {
			break
		}
		id := rowOf[it.ID]
		stored, err := store.LoadDocument(ctx, s.DB, id)
		if err != nil {
			return nil, err
		}
		cur := domain.FromDocument(stored.Doc)
		if _, err := s.checkStatus(ctx, s.DB, a, rules, p, id, stored.Doc, cur, "In Progress", opt.Comment, opt.OverrideReason); err != nil {
			var se *Error
			if !errors.As(err, &se) {
				return nil, err
			}
			res.Skipped = append(res.Skipped, NextSkip{ID: it.ID, Rule: se.Rule, Reason: i18n.Text(a.Lang, se)})
			if firstViolation == nil {
				firstViolation = se
			}
			continue
		}
		if opt.DryRun {
			if res.Issue, err = s.Detail(ctx, p, id); err != nil {
				return nil, err
			}
			res.Action, res.From = "would_start", cur.Status
			return res, nil
		}
		st, err := s.setStatus(ctx, a, p, id, "In Progress", opt.Comment, opt.OverrideReason, cur.Status, opt.Assignee)
		if err != nil {
			var se *Error
			if errors.As(err, &se) && (se.Kind == Conflict || se.Kind == Rejected) {
				// 判定の後に他の操作が先に着手した・状態が変わった。次の候補へ
				res.Skipped = append(res.Skipped, NextSkip{ID: it.ID, Rule: se.Rule, Reason: i18n.Text(a.Lang, se)})
				continue
			}
			return nil, err
		}
		if res.Issue, err = s.Detail(ctx, p, id); err != nil {
			return nil, err
		}
		res.Action, res.From = "started", st.From
		return res, nil
	}
	if firstViolation != nil {
		var ids []string
		for _, sk := range res.Skipped {
			if sk.Rule != "" {
				ids = append(ids, sk.ID)
			}
		}
		// 文面は要求の言語（a.Lang）で作る。最初の違反の文面も同じ言語にそろえる（i18n.Text が ID から訳す。
		// プロジェクトが設定した文面は ID を持たないので、判定の時点で a.Lang で作った Message がそのまま出る）
		var hintComment, hintOverride any = "", ""
		if opt.Comment == "" {
			hintComment = i18n.M("service.next.all_skipped.hint_comment")
		}
		if firstViolation.Overridable {
			hintOverride = i18n.M("service.next.all_skipped.hint_override")
		}
		msg := i18n.T(a.Lang, "service.next.all_skipped", "message", i18n.Text(a.Lang, firstViolation),
			"count", len(ids), "ids", strings.Join(ids, ", "), "hint_comment", hintComment, "hint_override", hintOverride)
		return nil, &Error{Kind: Rejected, Code: "rule_violation", Message: msg, Rule: firstViolation.Rule, Overridable: firstViolation.Overridable}
	}
	return res, nil
}

func typeWanted(t string, types []string) bool {
	if len(types) == 0 {
		return t != "epic"
	}
	return domain.Valid(types, t)
}

// preferDescendants は、着手中の束ね（containers）の子孫を先に、それ以外を後に並べ直す（それぞれの中の順は保つ）。
func preferDescendants(set *domain.Set, candidates []domain.Issue, containers []string) []domain.Issue {
	if len(containers) == 0 {
		return candidates
	}
	in := map[string]bool{}
	for _, c := range containers {
		in[strings.ToUpper(c)] = true
	}
	under := func(it domain.Issue) bool {
		seen := map[string]bool{}
		for p := it.Parent; p != "" && !seen[strings.ToUpper(p)]; { // 親の循環で止まらないように辿った ID を覚える
			if in[strings.ToUpper(p)] {
				return true
			}
			seen[strings.ToUpper(p)] = true
			parent, ok := set.Get(p)
			if !ok {
				return false
			}
			p = parent.Parent
		}
		return false
	}
	var first, rest []domain.Issue
	for _, it := range candidates {
		if under(it) {
			first = append(first, it)
		} else {
			rest = append(rest, it)
		}
	}
	return append(first, rest...)
}

// hasOpenChild は parent に id を持つ未クローズのイシューがあるか。
func hasOpenChild(set *domain.Set, id string) bool {
	for _, it := range set.Items {
		if strings.EqualFold(it.Parent, id) && !it.IsClosed() {
			return true
		}
	}
	return false
}

// AcceptanceCriteria は本文から受け入れ条件の節の中身を取り出す（「## 受け入れ条件」または「## Acceptance criteria」から
// 次の ## 見出しまで。コードブロックの中の見出しは読まない）。無ければ空。規則は domain.AcceptanceCriteria（§5-13）。
func AcceptanceCriteria(body string) string { return domain.AcceptanceCriteria(body) }
