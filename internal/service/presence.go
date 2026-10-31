package service

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// 在席（DESIGN.md §9-9）。どの利用者のどの端末のセッションが、どのブランチ・イシューで動いているかを参加者に見せる。
// 規則（動いているかの判定・保持・文字の検査・作業ツリーのパスを持たないこと）はこのファイルの 1 か所に置き、REST・Web・summary はここの関数を呼ぶ。
//
// 権限: 参加者全員が読め、書けるのは本人のセッションだけ。行のキーに利用者が入るので、他人のセッション ID を名乗っても他人の行は変えられない。
// 着手中のイシューはクライアントから受け取らない。読むときに着手の記録（store.InProgressStarters）から引くので、全員に見える値を名乗らせずに済み、
// 終えたイシューが次に送るまで残ることもない。

const (
	// PresenceActive は最後に送ってから動いていると見なす長さ。
	PresenceActive = 15 * time.Minute
	// PresenceRetention は最後に送ってから行を残す長さ。
	PresenceRetention = 7 * 24 * time.Hour
	// maxPresenceRows は 1 回に返すセッションの数（summary は AI の文脈に入るので上限を置く）。
	maxPresenceRows = 50
	// maxPresencePerUser は利用者ごとにプロジェクトで持てる行の数。超えたら自分の古い行から消す
	// （セッション ID はクライアントが名乗る値なので、上限が無いと 1 人が一覧から他人を押し出せる）。
	maxPresencePerUser = 20
)

var (
	presenceAgentRe = regexp.MustCompile(`^[a-z0-9-]{0,32}$`)
	presenceHeadRe  = regexp.MustCompile(`^(?:[0-9a-f]{7,64})?$`)
)

// PresenceInput は hook が送る値。セッションは Actor.SessionID で受け取る。
type PresenceInput struct {
	Project      string
	Agent        string
	MachineLabel string
	Branch       string
	HeadSHA      string
	// Directory は作業ツリーのディレクトリの名前（最後の要素だけ）
	Directory string
}

// PresenceState は動いているセッション 1 本。
type PresenceState struct {
	store.Presence
	// Issues はこのセッションが着手した In Progress のイシュー
	Issues []string
	// Own は要求したセッション自身か
	Own bool
}

// cleanPresenceText は端末のラベル・ブランチ・ディレクトリの名前を確かめる。viewer も名乗れて summary で他人の AI の文脈に入るので、
// 文として読めない形（domain.PresenceText）だけを受け取り、資格情報も拒む。
func cleanPresenceText(field, v string) (string, error) {
	v = strings.TrimSpace(v)
	if !domain.PresenceText(v) {
		return "", errm(Invalid, "invalid_argument", i18n.M("service.err.presence.format", "field", field))
	}
	return v, checkMessageText(field, v)
}

// presenceSession は要求のセッション ID を確かめる。セッションの無い要求は在席を持たない。
func presenceSession(a Actor) error {
	if a.SessionID == "" {
		return errm(Invalid, "invalid_argument", i18n.M("service.err.presence.no_session"))
	}
	return checkMessageText("session", a.SessionID)
}

// ReportPresence は本人のセッションの在席を書く。役割は問わない（viewer の AI も動いているセッションなので一覧に出す）。
func (s *Service) ReportPresence(ctx context.Context, a Actor, in PresenceInput) error {
	if err := presenceSession(a); err != nil {
		return err
	}
	label, err := cleanPresenceText("machine_label", in.MachineLabel)
	if err != nil {
		return err
	}
	branch, err := cleanPresenceText("branch", in.Branch)
	if err != nil {
		return err
	}
	dir, err := cleanPresenceText("directory", in.Directory)
	if err != nil {
		return err
	}
	if strings.ContainsAny(dir, `/\`) { // 絶対パスを全員に見せない（ディレクトリの名前だけを受け取る）
		return errm(Invalid, "invalid_argument", i18n.M("service.err.presence.directory"))
	}
	agent, head := strings.TrimSpace(in.Agent), strings.TrimSpace(in.HeadSHA)
	if !presenceAgentRe.MatchString(agent) {
		return errm(Invalid, "invalid_argument", i18n.M("service.err.presence.format", "field", "agent"))
	}
	if !presenceHeadRe.MatchString(head) {
		return errm(Invalid, "invalid_argument", i18n.M("service.err.presence.format", "field", "head_sha"))
	}
	u, p, _, err := s.memoryAccess(ctx, a, strings.TrimSpace(in.Project))
	if err != nil {
		return err
	}
	if err := store.UpsertPresence(ctx, s.DB, store.Presence{ProjectID: p.ID, UserID: u.ID, SessionID: a.SessionID, Agent: agent, MachineLabel: label,
		Branch: branch, HeadSHA: head, Directory: dir, LastSeenAt: s.Now()}); err != nil {
		return err
	}
	return store.TrimPresence(ctx, s.DB, p.ID, u.ID, a.SessionID, maxPresencePerUser)
}

// presenceOwn は行が要求したセッション自身か。種類の違うセッション（MCP の接続 ID と会話の ID）は比べられないので、
// 同じ利用者の行は自分のものと見る（自分のセッションを「ほかのセッション」と決めつけないため）。
func presenceOwn(a Actor, r store.Presence) bool {
	if a.SessionID == "" || r.UserID != a.UserID {
		return false
	}
	return r.SessionID == a.SessionID || CrossPath(a.SessionID, r.SessionID)
}

// presenceIssues は行のセッションが着手したイシューを返す。利用者も比べる（セッション ID はクライアントが名乗る値なので、
// 他人の着手を自分のものとして見せない）。MCP で着手したイシューは会話に結んだ値（LinkedSessionID）で当たる。
func presenceIssues(r store.Presence, starters []store.Starter) []string {
	var out []string
	for _, x := range starters {
		if x.UserID == r.UserID && (x.SessionID == r.SessionID || x.LinkedSessionID == r.SessionID) {
			out = append(out, x.DisplayID)
		}
	}
	return out
}

// fairPresence は利用者ごとに新しい順の順位を付け、順位の小さい行から max 行を選び、新しい順に並べ直す（1 人の行で一覧が埋まらないように）。
func fairPresence(rows []store.Presence, max int) []store.Presence {
	if len(rows) <= max {
		return rows
	}
	seen := map[int64]int{}
	rank := make(map[*store.Presence]int, len(rows))
	picked := make([]*store.Presence, 0, len(rows))
	for i := range rows {
		rank[&rows[i]] = seen[rows[i].UserID]
		seen[rows[i].UserID]++
		picked = append(picked, &rows[i])
	}
	sort.SliceStable(picked, func(i, j int) bool { return rank[picked[i]] < rank[picked[j]] })
	picked = picked[:max]
	sort.SliceStable(picked, func(i, j int) bool { return picked[i].LastSeenAt.After(picked[j].LastSeenAt) })
	out := make([]store.Presence, 0, max)
	for _, p := range picked {
		out = append(out, *p)
	}
	return out
}

// EndPresence は本人のセッションの在席を外す（SessionEnd）。無ければ何もしない。
func (s *Service) EndPresence(ctx context.Context, a Actor, slug string) error {
	if err := presenceSession(a); err != nil {
		return err
	}
	u, p, _, err := s.memoryAccess(ctx, a, strings.TrimSpace(slug))
	if err != nil {
		return err
	}
	return store.DeletePresence(ctx, s.DB, p.ID, u.ID, a.SessionID)
}

// ProjectPresence はプロジェクトで動いているセッションを新しい順に返す（権限は呼ぶ側が確かめた後）。
func (s *Service) ProjectPresence(ctx context.Context, a Actor, p store.Project) ([]PresenceState, error) {
	rows, err := store.ActivePresence(ctx, s.DB, p.ID, s.Now().Add(-PresenceActive))
	if err != nil {
		return nil, err
	}
	rows = fairPresence(rows, maxPresenceRows)
	out := make([]PresenceState, 0, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	starters, err := store.InProgressStarters(ctx, s.DB, p.ID)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out = append(out, PresenceState{Presence: r, Own: presenceOwn(a, r), Issues: presenceIssues(r, starters)})
	}
	return out, nil
}

// OtherPresence は ProjectPresence から要求したセッション自身を除く（summary が使う）。
func (s *Service) OtherPresence(ctx context.Context, a Actor, p store.Project) ([]PresenceState, error) {
	all, err := s.ProjectPresence(ctx, a, p)
	if err != nil {
		return nil, err
	}
	out := make([]PresenceState, 0, len(all))
	for _, st := range all {
		if !st.Own {
			out = append(out, st)
		}
	}
	return out, nil
}

// ListPresence は参加者に動いているセッションを返す（Web の一覧）。
func (s *Service) ListPresence(ctx context.Context, a Actor, slug string) ([]PresenceState, store.Project, error) {
	_, p, _, err := s.memoryAccess(ctx, a, strings.TrimSpace(slug))
	if err != nil {
		return nil, p, err
	}
	list, err := s.ProjectPresence(ctx, a, p)
	return list, p, err
}

// PurgePresence は保持の長さを過ぎた在席を消す（定期の処理）。
func (s *Service) PurgePresence(ctx context.Context) error {
	return store.PurgePresence(ctx, s.DB, s.Now().Add(-PresenceRetention))
}
