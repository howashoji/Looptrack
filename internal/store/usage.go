package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/usage"
)

// トークン消費のスナップショット（usage_snapshots。追記専用）。

// UsageSnapshot は保存する 1 行。JSON の列は生のまま持つ（サーバは中身を解釈しない）。
type UsageSnapshot struct {
	ProjectID      int64
	UserID         int64
	TokenID        int64 // 0 なら NULL
	Client         string
	ClientVersion  string
	SessionID      string
	ConversationID string
	Trigger        string
	IssueID        int64 // 0 なら NULL
	Op             string
	IssueStatus    string
	Via            string
	At             time.Time
	Counters       usage.Counters
	ByModel        json.RawMessage
	IO             json.RawMessage
	Human          json.RawMessage
	Segments       json.RawMessage
	Branch         string
	Branches       json.RawMessage
	CwdName        string
	Excluded       bool
	DedupeKey      string
	ReceivedAt     time.Time // 0 なら DB の現在時刻。通常の受信はサーバの時計（svc.Now）を入れ issue_events.at と突き合わせる。過去分の取り込みだけ at と同じ時刻を入れる
}

// InsertUsageSnapshot は 1 行足す。同じ dedupe_key の行が既にあれば何もせず duplicate = true を返す。
func InsertUsageSnapshot(ctx context.Context, q execQuerier, s UsageSnapshot) (id int64, duplicate bool, err error) {
	res, err := q.ExecContext(ctx, `INSERT INTO usage_snapshots (project_id, user_id, token_id, client, client_version,
  session_id, conversation_id, trigger_kind, issue_id, op, issue_status, via, at,
  main_input, main_cache_create, main_cache_read, main_output, sub_input, sub_cache_create, sub_cache_read, sub_output,
  responses, sub_responses, by_model, io, human, segments, branch, branches, cwd_name, excluded, dedupe_key, received_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, COALESCE(?, CURRENT_TIMESTAMP(6)))`,
		s.ProjectID, s.UserID, nullInt(s.TokenID), s.Client, s.ClientVersion,
		s.SessionID, s.ConversationID, s.Trigger, nullInt(s.IssueID), nullStr(s.Op), nullStr(s.IssueStatus), nullStr(s.Via), s.At.UTC(),
		s.Counters.Main.Input, s.Counters.Main.CacheCreate, s.Counters.Main.CacheRead, s.Counters.Main.Output,
		s.Counters.Sub.Input, s.Counters.Sub.CacheCreate, s.Counters.Sub.CacheRead, s.Counters.Sub.Output,
		s.Counters.Responses, s.Counters.SubResponses,
		nullJSON(s.ByModel), nullJSON(s.IO), nullJSON(s.Human), nullJSON(s.Segments),
		s.Branch, nullJSON(s.Branches), s.CwdName, s.Excluded, s.DedupeKey, nullTime(s.ReceivedAt))
	if err != nil {
		if IsDuplicateKey(err) { // 重複キー
			return 0, true, nil
		}
		return 0, false, err
	}
	id, err = res.LastInsertId()
	return id, false, err
}

const usageColumns = `s.id, s.client, s.session_id, s.conversation_id, s.trigger_kind, COALESCE(s.issue_id, 0), COALESCE(i.display_id, ''),
  COALESCE(s.op, ''), COALESCE(s.issue_status, ''), COALESCE(s.via, ''), s.at,
  s.main_input, s.main_cache_create, s.main_cache_read, s.main_output, s.sub_input, s.sub_cache_create, s.sub_cache_read, s.sub_output,
  s.responses, s.sub_responses, s.excluded, s.branch`

func scanUsage(rows *sql.Rows) (usage.Snapshot, error) {
	var s usage.Snapshot
	var excluded bool
	err := rows.Scan(&s.ID, &s.Client, &s.SessionID, &s.ConversationID, &s.Trigger, &s.IssueID, &s.IssueDisplayID,
		&s.Op, &s.IssueStatus, &s.Via, &s.At,
		&s.Counters.Main.Input, &s.Counters.Main.CacheCreate, &s.Counters.Main.CacheRead, &s.Counters.Main.Output,
		&s.Counters.Sub.Input, &s.Counters.Sub.CacheCreate, &s.Counters.Sub.CacheRead, &s.Counters.Sub.Output,
		&s.Counters.Responses, &s.Counters.SubResponses, &excluded, &s.Branch)
	s.Excluded = excluded
	return s, err
}

// queryUsage の並び（会話 → ID）は区間の差分を会話ごとに計算するためのもので、表示の順ではない。
// イシューの段階の一覧は usage.SortByTime で時刻順に並べ替えて返す（server.issueUsage）。
func queryUsage(ctx context.Context, q execQuerier, where string, args ...any) ([]usage.Snapshot, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+usageColumns+`
FROM usage_snapshots s LEFT JOIN issues i ON i.id = s.issue_id
WHERE `+where+` ORDER BY s.conversation_id, s.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []usage.Snapshot
	for rows.Next() {
		s, err := scanUsage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// UsageForIssue は、そのイシューに触れた会話の全スナップショットを返す（差分を出すには会話全体が要る）。
func UsageForIssue(ctx context.Context, q execQuerier, projectID, issueID int64) ([]usage.Snapshot, error) {
	return queryUsage(ctx, q, `s.project_id = ? AND s.conversation_id IN
  (SELECT DISTINCT x.conversation_id FROM usage_snapshots x WHERE x.project_id = ? AND x.issue_id = ?)`, projectID, projectID, issueID)
}

// UsageForProject は、期間 [from, to) に受け取ったスナップショットを含む会話の全スナップショットを返す。
func UsageForProject(ctx context.Context, q execQuerier, projectID int64, from, to time.Time) ([]usage.Snapshot, error) {
	return queryUsage(ctx, q, `s.project_id = ? AND s.conversation_id IN
  (SELECT DISTINCT x.conversation_id FROM usage_snapshots x WHERE x.project_id = ? AND x.received_at >= ? AND x.received_at < ?)`,
		projectID, projectID, from.UTC(), to.UTC())
}

// UsageForClient は、そのプロジェクトで client が一致するスナップショットを全部返す（過去分の取り込みの突き合わせ用）。
func UsageForClient(ctx context.Context, q execQuerier, projectID int64, client string) ([]usage.Snapshot, error) {
	return queryUsage(ctx, q, `s.project_id = ? AND s.client = ?`, projectID, client)
}

// HasUsageForIssue は、その会話（sessionID が空なら利用者の全会話）のスナップショットがそのイシューに 1 件以上あるか
// （クローズ時の必須化）。会話は conversation_id で束ねるので、再開でセッション ID が変わっても前の分を数える。
func HasUsageForIssue(ctx context.Context, q execQuerier, issueID, userID int64, sessionID string) (bool, error) {
	var n int
	var err error
	if sessionID == "" {
		err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_snapshots WHERE issue_id = ? AND user_id = ?`, issueID, userID).Scan(&n)
	} else {
		err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_snapshots s WHERE s.issue_id = ? AND (s.session_id = ? OR EXISTS
  (SELECT 1 FROM usage_snapshots t WHERE t.project_id = s.project_id AND t.conversation_id = s.conversation_id AND t.session_id = ?))`,
			issueID, sessionID, sessionID).Scan(&n)
	}
	return n > 0, err
}

// HasRecentHookUsage は、利用者のフック（MCP の操作の後に送る経路 ②）が since 以降に 1 件以上届いているか。
// 届いていれば、MCP の応答で付与を指示しなくても数秒後にフックが付ける。
func HasRecentHookUsage(ctx context.Context, q execQuerier, projectID, userID int64, since time.Time) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_snapshots WHERE project_id = ? AND received_at >= ? AND user_id = ? AND via = 'mcp'`,
		projectID, since.UTC(), userID).Scan(&n)
	return n > 0, err
}

// UsageOptInAgents は、利用者が計測を有効にしたときだけトークンを測れる AI（MCP の clientInfo からの判定。名前は usage_snapshots.client と同じ）。
// GitHub Copilot は OpenTelemetry のファイル出力（既定で無効）を有効にしたときだけ、hook（looptrack hook usage）がトークンを読んで client = copilot で送る
// （DESIGN.md §5-4「Copilot のトークン」）。有効にしていない利用者の操作は付けようがないので、付与の指示・クローズ時の必須・
// 未付与の検知の対象にしない。有効かどうかは「その利用者の、その AI のスナップショットが直近 UsageOptInWindow 以内に届いているか」で決める。
var UsageOptInAgents = []string{"copilot"}

// UsageOptInWindow は、利用者がその AI の計測を有効にしているとみなす期間（直近にその AI のスナップショットが届いていること）。
const UsageOptInWindow = 7 * 24 * time.Hour

// UsageOptIn は agent が、利用者が計測を有効にしたときだけ測れる AI か。
func UsageOptIn(agent string) bool {
	for _, a := range UsageOptInAgents {
		if agent == a {
			return true
		}
	}
	return false
}

// HasRecentClientUsage は、利用者のその AI（client）のスナップショットが since 以降に 1 件以上届いているか（計測を有効にしている目印）。
func HasRecentClientUsage(ctx context.Context, q execQuerier, projectID, userID int64, client string, since time.Time) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_snapshots WHERE project_id = ? AND received_at >= ? AND user_id = ? AND client = ?`,
		projectID, since.UTC(), userID, client).Scan(&n)
	return n > 0, err
}

// usageOptInCond は issue_events（別名 e）から、計測を有効にしていない利用者の UsageOptInAgents の MCP・CLI の操作を外す条件（detail の "agent"。CLI は X-Looptrack-Agent で送られた分）。
// 有効＝同じプロジェクト・同じ利用者の、その AI のスナップショットが操作の前 UsageOptInWindow から操作の後 UsageAttachWindow までに届いている
// （操作自体のスナップショットがフックで届けばそれも目印になる。有効にする前の操作は、後から有効にしても対象に戻らない）。
func usageOptInCond() string {
	q := make([]string, len(UsageOptInAgents))
	for i, a := range UsageOptInAgents {
		q[i] = "'" + a + "'"
	}
	agent := `COALESCE(JSON_UNQUOTE(JSON_EXTRACT(e.detail, '$.agent')), '')`
	return fmt.Sprintf(` AND NOT (e.via IN ('mcp', 'cli') AND %s IN (%s)
  AND NOT EXISTS (SELECT 1 FROM usage_snapshots m WHERE m.project_id = e.project_id AND m.user_id = e.actor_user_id AND m.client = %s
    AND m.received_at >= e.at - INTERVAL %d SECOND AND m.received_at < e.at + INTERVAL %d SECOND))`,
		agent, strings.Join(q, ", "), agent, int(UsageOptInWindow/time.Second), int(UsageAttachWindow/time.Second))
}

// SessionKindHost は issue_events.detail の "session_kind" に入る、器（デスクトップ版のセッションの窓）のセッション ID の印。
// service.SessionKindHost と同じ値（store は service を参照できないので、ここに定義を置く）。
const SessionKindHost = "host"

// usageHostSessionCond は issue_events（別名 e）から、器のセッション ID で送られた操作を外す条件
// （detail の "session_kind"）。会話記録と結び付かないセッション ID なので、トークン情報を付けようがない（DESIGN.md §9-5）。
// 経路では絞らない（印は送ってきた経路ではなくセッション ID の種類を表すので、CLI でも MCP でも同じ理由で外す）。
// 印の無い操作（印を送らない古い CLI・印を送らない MCP の接続・人の操作）は今までどおり残る。
func usageHostSessionCond() string {
	// COALESCE で NULL を空にする: 印の無い detail は JSON_EXTRACT が NULL を返し、NULL = 'host' も NULL なので、
	// そのままだと NOT (NULL) が NULL になって操作が丸ごと落ちる（実際に落ちた。テストが先に捕まえた）。
	return ` AND COALESCE(JSON_UNQUOTE(JSON_EXTRACT(e.detail, '$.session_kind')), '') <> '` + SessionKindHost + `'`
}

// UsageAttachWindow は、操作（issue_events）の後この時間内に同じイシュー・同じ利用者のスナップショットが届けば「付与済み」とみなす幅。
const UsageAttachWindow = 10 * time.Minute

// UsageEvent は付与漏れの判定の対象になる 1 操作。
type UsageEvent struct {
	EventID   int64
	IssueID   int64
	DisplayID string
	Title     string
	Kind      string
	Via       string
	At        time.Time
	SessionID string
	UserID    int64
	Login     string
	Attached  bool
}

// usageCountedKinds は未付与の検知で数える issue_events の kind（SQL の IN の右辺。**ここ 1 か所だけに書く**）。
//
// assign を入れているのは、担当者だけを変えた操作（MCP の assign_issue と、assignee を伴う update_issue）が
// kind assign を書くから。数えないと、その取りこぼしは未付与の一覧にすら現れず、
// 「未付与に出ていないから漏れは無い」という読み方が成り立たなくなる。
//
// 対象（AI の操作）と humans（人がターミナルから打った操作）の両方で同じ集合を使う。
// 片方だけを変えると充足率の分母と分子で数える操作が変わり、率だけが黙ってずれる。
const usageCountedKinds = `('create', 'update', 'comment', 'status', 'verify', 'assign')`

// UsageCoverage は、期間 [since, now) の変更操作のうち AI からのもの（via = mcp、または via = cli でセッション ID あり）と、
// それぞれにトークン情報が付いたかを新しい順に返す。計測を有効にしていない利用者の UsageOptInAgents（Copilot）の MCP の操作は含めない。付いた＝同じイシュー・同じ利用者のスナップショットが操作の後
// UsageAttachWindow 以内に届いた（CLI 内蔵・フック）か、操作の後に手動の付与（usage attach・回収）が届いた。humans は対象外（人がターミナルから打った操作）の件数。
// セッション ID なしでも AI と分かる CLI の操作（detail の "agent"。VS Code の Copilot のエージェント用ターミナル）は humans に数えない。
// 器のセッション ID で送られた操作（detail の "session_kind" = host）は、経路によらず、トークン情報を付けようがないので events にも humans にも数えない
// （計測を有効にしていない利用者の Copilot の操作と同じ扱い）。
// userID が 0 でなければその利用者の操作だけ。
func UsageCoverage(ctx context.Context, q execQuerier, projectID, userID int64, since time.Time) (events []UsageEvent, humans int, err error) {
	userCond, args := "", []any{int(UsageAttachWindow / time.Second), projectID, since.UTC()}
	if userID != 0 {
		userCond = " AND e.actor_user_id = ?"
		args = append(args, userID)
	}
	rows, err := q.QueryContext(ctx, `SELECT e.id, e.issue_id, i.display_id, i.title,
  e.kind, e.via, e.at, COALESCE(e.session_id, ''), COALESCE(e.actor_user_id, 0), COALESCE(u.login, ''),
  EXISTS (SELECT 1 FROM usage_snapshots s WHERE s.issue_id = e.issue_id AND s.user_id = e.actor_user_id
    AND s.received_at >= e.at AND (s.received_at < e.at + INTERVAL ? SECOND OR s.trigger_kind = 'manual'))
FROM issue_events e JOIN issues i ON i.id = e.issue_id LEFT JOIN users u ON u.id = e.actor_user_id
WHERE e.project_id = ? AND e.at >= ? AND e.kind IN `+usageCountedKinds+`
  AND (e.via = 'mcp' OR (e.via = 'cli' AND e.session_id IS NOT NULL))`+usageOptInCond()+usageHostSessionCond()+userCond+`
ORDER BY e.at DESC, e.id DESC`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var ev UsageEvent
		if err := rows.Scan(&ev.EventID, &ev.IssueID, &ev.DisplayID, &ev.Title, &ev.Kind, &ev.Via, &ev.At, &ev.SessionID,
			&ev.UserID, &ev.Login, &ev.Attached); err != nil {
			return nil, 0, err
		}
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	hargs := []any{projectID, since.UTC()}
	if userID != 0 {
		hargs = append(hargs, userID)
	}
	err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM issue_events e WHERE e.project_id = ? AND e.at >= ?
  AND e.kind IN `+usageCountedKinds+` AND e.via = 'cli' AND e.session_id IS NULL
  AND JSON_EXTRACT(e.detail, '$.agent') IS NULL`+userCond, hargs...).Scan(&humans)
	return events, humans, err
}

func nullInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullTime(v time.Time) any {
	if v.IsZero() {
		return nil
	}
	return v.UTC()
}

func nullStr(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

func nullJSON(v json.RawMessage) any {
	if len(v) == 0 || string(v) == "null" {
		return nil
	}
	return []byte(v)
}
