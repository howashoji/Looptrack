package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
)

// プロジェクト記憶（memories・memory_versions。マイグレーション 0011。docs/server/DESIGN.md §9-9）。
// memories は今の版の 1 行で、追記・置き換え・消すたびに版を 1 進めて書き換える。memory_versions は書くたびに足す履歴で、追記専用。
// 版の本文と要約は memory_bodies に置き、版の行を残したまま本体だけを消せるようにする（消した記録は追記専用の memory_purges。0015）。
// 見える範囲（参加者・本人）・版の照合・上限・秘密の検査は service が持つ。ここは読み書きだけを置く。

// 記憶の範囲・種類・版の操作（表の CHECK と同じ値）。
const (
	MemoryScopeProject  = "project"
	MemoryScopePersonal = "personal"

	MemoryKindHandoff  = "handoff"
	MemoryKindDecision = "decision"
	MemoryKindCaveat   = "caveat"

	MemoryOpCreate  = "create"
	MemoryOpAppend  = "append"
	MemoryOpReplace = "replace"
	MemoryOpDelete  = "delete"

	// 本体を消した理由（memory_purges.cause）
	MemoryPurgeUser      = "user"      // 書ける利用者の消去
	MemoryPurgeOperator  = "operator"  // 運用者の消去（looptrack purge-memory）
	MemoryPurgeRetention = "retention" // 保持期間（memory_retention_days）
	MemoryPurgeThinning  = "thinning"  // 版の数の上限
)

// MaxRetentionDays は保持日数の上限（100 年。time.Duration は約 292 年であふれ、あふれると切れ目が未来へ飛んで新しいものまで消える）。
const MaxRetentionDays = 36500

// validRetentionDays は保持日数として受け付けるか（0 は無期限）。読みも同じ範囲で見て、外れた値は既定に戻す。
func validRetentionDays(n int64) bool { return n >= 0 && n <= MaxRetentionDays }

// 記憶の版の保持期間（system_settings）。
const (
	SettingMemoryRetentionDays       = "memory_retention_days" // この日数を過ぎた古い版の本体を消す（0 は無期限）
	DefaultMemoryRetentionDays int64 = 0
)

// Memory は記憶 1 件の今の版（memories の 1 行）。Version 0 はまだ何も書いていない下書きの行（EnsureMemoryRow）で、どの読みにも出さない。
type Memory struct {
	ID             int64
	ProjectID      int64
	Scope          string
	OwnerUserID    int64 // personal の持ち主（project は 0）
	Kind           string
	Name           string
	Version        int64
	Summary        string
	Content        string
	Deleted        bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
	UpdatedBy      int64  // 最後に書いた利用者（0 は不明）
	UpdatedByLogin string // 一覧・取得で users から引いた login（書き込みでは使わない）
}

// MemoryVersion は版の履歴 1 件（memory_versions の 1 行）。
type MemoryVersion struct {
	MemoryID     int64
	Version      int64
	Op           string
	Summary      string
	Content      string
	UserID       int64
	UserLogin    string // 読むときに users から引いた login
	MachineLabel string
	SessionID    string
	TokenID      int64
	Via          string
	CreatedAt    time.Time
	// IdempotencyKey は追記の冪等の鍵（無ければ空）。書くときだけ使い、読むときは入れない
	IdempotencyKey string
	// WrittenAt はクライアントが本文を作った時刻（無ければゼロ値）
	WrittenAt time.Time
	// Purged は本体（本文と要約）を消した版。Summary と Content は空になる
	Purged bool
}

// MemoryPurge は本体を消した記録 1 件（memory_purges の 1 行）。
type MemoryPurge struct {
	MemoryID       int64
	FromVersion    int64
	ThroughVersion int64
	Bodies         int
	Cause          string
	At             time.Time
	ActorUserID    int64 // 利用者の消去だけ（それ以外は 0）
	Via            string
	Reason         string
}

// MemoryBodySize は本体を持つ版 1 件の大きさと本文。
type MemoryBodySize struct {
	Version int64
	Bytes   int64
	Content string
}

const memoryColumns = `m.id, m.project_id, m.scope, m.owner_user_id, m.kind, m.name, m.version, m.summary, m.content, m.deleted,
  m.created_at, m.updated_at, COALESCE(m.updated_by, 0), COALESCE(u.login, '')`

// memoryLockColumns は行をロックして読むときの列（users を結ばない。login は空）。
const memoryLockColumns = `m.id, m.project_id, m.scope, m.owner_user_id, m.kind, m.name, m.version, m.summary, m.content, m.deleted,
  m.created_at, m.updated_at, COALESCE(m.updated_by, 0), ''`

const memoryFrom = ` FROM memories m LEFT JOIN users u ON u.id = m.updated_by`

// 版の行に本体を結ぶ。本体を消した版は行が残り、本体の列が NULL になる。
const (
	memoryBodyJoin     = ` LEFT JOIN memory_bodies b ON b.memory_id = v.memory_id AND b.version = v.version`
	memoryPurgedColumn = `CASE WHEN b.memory_id IS NULL THEN 1 ELSE 0 END`
)

func scanMemory(row interface{ Scan(...any) error }) (Memory, error) {
	var m Memory
	err := row.Scan(&m.ID, &m.ProjectID, &m.Scope, &m.OwnerUserID, &m.Kind, &m.Name, &m.Version, &m.Summary, &m.Content, &m.Deleted,
		&m.CreatedAt, &m.UpdatedAt, &m.UpdatedBy, &m.UpdatedByLogin)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}

// MemoryByKey は (プロジェクト, 範囲, 持ち主, 種類, 名前) の記憶を返す（消した印のものと下書きの行も返す。無ければ ErrNotFound）。
// forUpdate なら行をロックする（トランザクションの中で使う。追記と置き換えを 1 つずつにする）。
func MemoryByKey(ctx context.Context, q execQuerier, projectID int64, scope string, owner int64, kind, name string, forUpdate bool) (Memory, error) {
	query := "SELECT " + memoryColumns + memoryFrom +
		" WHERE m.project_id = ? AND m.scope = ? AND m.owner_user_id = ? AND m.kind = ? AND m.name = ?"
	if forUpdate {
		// ロックの読みは最新の確定した行を返す（MySQL の REPEATABLE READ でも）。同じ行をロックの無い読みで読み直すと
		// トランザクションの最初の読みの時点の版が返り、追記が先に確定した節を上書きしてしまうので、列はこの 1 文で取る。
		// users を結ぶと users の行まで握るので結ばない（login は空のまま。書き込みでは使わない）
		query = "SELECT " + memoryLockColumns + " FROM memories m" +
			" WHERE m.project_id = ? AND m.scope = ? AND m.owner_user_id = ? AND m.kind = ? AND m.name = ? FOR UPDATE"
	}
	return scanMemory(q.QueryRowContext(ctx, query, projectID, scope, owner, kind, name))
}

// EnsureMemoryRow は記憶の行が無ければ下書きの行（version 0・deleted）を作る。トランザクションの外（その文だけで確定する接続）で呼ぶ。
// 行が無いまま FOR UPDATE すると一意性の索引のギャップロックを取り、別の名前の記憶を同時に作る 2 つのトランザクションが
// 互いの INSERT を待ってデッドロックになる。トランザクションの中で「無ければ足す」と、既にある行への INSERT IGNORE が
// 共有ロックを取って続く FOR UPDATE で互いを待つので、作るのと握るのを分ける（EnsureTranscriptLock と同じ）。
func EnsureMemoryRow(ctx context.Context, q execQuerier, m Memory) error {
	_, err := q.ExecContext(ctx, `INSERT IGNORE INTO memories (project_id, scope, owner_user_id, kind, name, version, summary, content, deleted, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, 0, '', '', TRUE, ?, ?)`, m.ProjectID, m.Scope, m.OwnerUserID, m.Kind, m.Name, m.CreatedAt, m.UpdatedAt)
	return err
}

// UpdateMemory は記憶の今の版を書き換える（版・要約・本文・消した印・更新の時刻と書き手）。
// 呼ぶ側は同じトランザクションで MemoryByKey(forUpdate) を取ってから呼ぶ。
func UpdateMemory(ctx context.Context, q execQuerier, m Memory) error {
	_, err := q.ExecContext(ctx, "UPDATE memories SET version = ?, summary = ?, content = ?, deleted = ?, updated_at = ?, updated_by = ? WHERE id = ?",
		m.Version, m.Summary, m.Content, m.Deleted, m.UpdatedAt, nullID(m.UpdatedBy), m.ID)
	return err
}

// InsertMemoryVersion は版の履歴を 1 行と、その本体を足す。
func InsertMemoryVersion(ctx context.Context, q execQuerier, v MemoryVersion) error {
	if err := InsertMemoryVersionRow(ctx, q, v); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, "INSERT INTO memory_bodies (memory_id, version, summary, content) VALUES (?, ?, ?, ?)", v.MemoryID, v.Version, v.Summary, v.Content)
	return err
}

// InsertMemoryVersionRow は版の履歴の行だけを足す（本体は無い。今の版の本体を消したときの版）。
func InsertMemoryVersionRow(ctx context.Context, q execQuerier, v MemoryVersion) error {
	var key any
	if v.IdempotencyKey != "" {
		key = v.IdempotencyKey
	}
	var writtenAt any
	if !v.WrittenAt.IsZero() {
		writtenAt = v.WrittenAt
	}
	_, err := q.ExecContext(ctx, `INSERT INTO memory_versions (memory_id, version, op, user_id, machine_label, session_id, token_id, via, created_at, idempotency_key, written_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		v.MemoryID, v.Version, v.Op, nullID(v.UserID), truncate(v.MachineLabel, 64), truncate(v.SessionID, 128), nullID(v.TokenID), v.Via, v.CreatedAt, key, writtenAt)
	return err
}

// MemoryVersionByIdempotencyKey は、この記憶の版のうち同じ冪等の鍵を持つものを返す。無ければ ErrNotFound。
// 返すのは版の番号・要約・書いた利用者・時刻と、その版の本文の全体（本体を消した版は Purged で、要約と本文は空）。
func MemoryVersionByIdempotencyKey(ctx context.Context, q execQuerier, memoryID int64, key string) (MemoryVersion, error) {
	var v MemoryVersion
	err := q.QueryRowContext(ctx, `SELECT v.version, COALESCE(b.summary, ''), COALESCE(v.user_id, 0), COALESCE(u.login, ''), COALESCE(b.content, ''), v.created_at, `+memoryPurgedColumn+`
FROM memory_versions v LEFT JOIN users u ON u.id = v.user_id`+memoryBodyJoin+` WHERE v.memory_id = ? AND v.idempotency_key = ?`, memoryID, key).
		Scan(&v.Version, &v.Summary, &v.UserID, &v.UserLogin, &v.Content, &v.CreatedAt, &v.Purged)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}

// ListMemories はプロジェクトの記憶のうち、範囲が project のものと、userID が持ち主の personal のものを返す
// （ほかの利用者の personal は載らない）。includeDeleted でなければ消した印のものを除く。種類・範囲・名前の順。
func ListMemories(ctx context.Context, q execQuerier, projectID, userID int64, includeDeleted bool) ([]Memory, error) {
	query := "SELECT " + memoryColumns + memoryFrom +
		" WHERE m.project_id = ? AND m.version > 0 AND (m.scope = 'project' OR (m.scope = 'personal' AND m.owner_user_id = ?))"
	if !includeDeleted {
		query += " AND m.deleted = FALSE"
	}
	rows, err := q.QueryContext(ctx, query+" ORDER BY m.kind, m.scope, m.name", projectID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MemoryVersions は記憶の版の履歴を新しい順に返す（本文は入れない。Content は空。本体を消した版は Purged で、要約も空）。
func MemoryVersions(ctx context.Context, q execQuerier, memoryID int64) ([]MemoryVersion, error) {
	rows, err := q.QueryContext(ctx, `SELECT v.memory_id, v.version, v.op, COALESCE(b.summary, ''), COALESCE(v.user_id, 0), COALESCE(u.login, ''), v.machine_label, v.session_id,
  COALESCE(v.token_id, 0), v.via, v.created_at, v.written_at, `+memoryPurgedColumn+`
FROM memory_versions v LEFT JOIN users u ON u.id = v.user_id`+memoryBodyJoin+` WHERE v.memory_id = ? ORDER BY v.version DESC`, memoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MemoryVersion
	for rows.Next() {
		var v MemoryVersion
		var writtenAt sql.NullTime
		if err := rows.Scan(&v.MemoryID, &v.Version, &v.Op, &v.Summary, &v.UserID, &v.UserLogin, &v.MachineLabel, &v.SessionID, &v.TokenID, &v.Via, &v.CreatedAt, &writtenAt, &v.Purged); err != nil {
			return nil, err
		}
		v.WrittenAt = writtenAt.Time
		out = append(out, v)
	}
	return out, rows.Err()
}

// MemoryVersionContent は記憶の 1 つの版の本文を返す（無ければ ErrNotFound。本体を消した版は Purged で、要約と本文は空）。
func MemoryVersionContent(ctx context.Context, q execQuerier, memoryID, version int64) (MemoryVersion, error) {
	var v MemoryVersion
	err := q.QueryRowContext(ctx, `SELECT v.memory_id, v.version, v.op, COALESCE(b.summary, ''), COALESCE(b.content, ''), COALESCE(v.user_id, 0), COALESCE(u.login, ''), v.machine_label, v.session_id,
  COALESCE(v.token_id, 0), v.via, v.created_at, `+memoryPurgedColumn+`
FROM memory_versions v LEFT JOIN users u ON u.id = v.user_id`+memoryBodyJoin+` WHERE v.memory_id = ? AND v.version = ?`, memoryID, version).
		Scan(&v.MemoryID, &v.Version, &v.Op, &v.Summary, &v.Content, &v.UserID, &v.UserLogin, &v.MachineLabel, &v.SessionID, &v.TokenID, &v.Via, &v.CreatedAt, &v.Purged)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}

// MemoryByIDForUpdate は記憶の行をロックして読む（トランザクションの中で使う。保持期間の処理は ID から引く）。
func MemoryByIDForUpdate(ctx context.Context, q execQuerier, id int64) (Memory, error) {
	return scanMemory(q.QueryRowContext(ctx, "SELECT "+memoryLockColumns+" FROM memories m WHERE m.id = ? FOR UPDATE", id))
}

// MemoryBodySizes は本体を持つ版と大きさを古い順に返す（バイト数は方言で数え方が違うので Go で数える）。
func MemoryBodySizes(ctx context.Context, q execQuerier, memoryID int64) ([]MemoryBodySize, error) {
	rows, err := q.QueryContext(ctx, "SELECT version, content FROM memory_bodies WHERE memory_id = ? ORDER BY version", memoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MemoryBodySize
	for rows.Next() {
		var b MemoryBodySize
		if err := rows.Scan(&b.Version, &b.Content); err != nil {
			return nil, err
		}
		b.Bytes = int64(len(b.Content))
		out = append(out, b)
	}
	return out, rows.Err()
}

// MemoryVersionsBeyond は本体を持つ版のうち、新しい順に keep 個を超えた分を古い順に返す（版の数の上限で間引く版）。
func MemoryVersionsBeyond(ctx context.Context, q execQuerier, memoryID int64, keep int) ([]int64, error) {
	rows, err := q.QueryContext(ctx, "SELECT version FROM memory_bodies WHERE memory_id = ? ORDER BY version DESC LIMIT 1000000 OFFSET ?", memoryID, keep)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append([]int64{n}, out...)
	}
	return out, rows.Err()
}

// ExpiredMemoryIDs は cutoff より前に書いた版の本体を、保持期間で消せる記憶の ID を返す。消せるのは今の版より古い版と、
// 消した印の記憶の今の版（生きている記憶の今の版は残す）。
func ExpiredMemoryIDs(ctx context.Context, q execQuerier, cutoff time.Time) ([]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT m.id FROM memories m
JOIN memory_bodies b ON b.memory_id = m.id
JOIN memory_versions v ON v.memory_id = b.memory_id AND v.version = b.version
WHERE v.created_at < ? AND (b.version < m.version OR m.deleted = TRUE) ORDER BY m.id`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ExpiredMemoryVersions は記憶 m のうち保持期間で消す版を古い順に返す（条件は ExpiredMemoryIDs と同じ。m はロックして読んだ行）。
func ExpiredMemoryVersions(ctx context.Context, q execQuerier, m Memory, cutoff time.Time) ([]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT b.version FROM memory_bodies b
JOIN memory_versions v ON v.memory_id = b.memory_id AND v.version = b.version
WHERE b.memory_id = ? AND v.created_at < ? ORDER BY b.version`, m.ID, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		if n < m.Version || m.Deleted {
			out = append(out, n)
		}
	}
	return out, rows.Err()
}

// DeleteMemoryBodies は指定の版の本体を消す。呼ぶ側は同じトランザクションで記憶の行をロックしてから呼ぶ。
func DeleteMemoryBodies(ctx context.Context, q execQuerier, memoryID int64, versions []int64) error {
	for _, n := range versions {
		if _, err := q.ExecContext(ctx, "DELETE FROM memory_bodies WHERE memory_id = ? AND version = ?", memoryID, n); err != nil {
			return err
		}
	}
	return nil
}

// ClearMemoryContent は今の版の本体を消した記憶の行から本文と要約を外し、消した印を立てて版を version にする
// （消す前に読んだ版の置き換えを 409 にするため）。
func ClearMemoryContent(ctx context.Context, q execQuerier, id, version, actor int64, at time.Time) error {
	_, err := q.ExecContext(ctx, "UPDATE memories SET version = ?, summary = '', content = '', deleted = TRUE, updated_at = ?, updated_by = ? WHERE id = ?", version, at, nullID(actor), id)
	return err
}

// InsertMemoryPurge は本体を消した記録を 1 行足す（追記専用）。
func InsertMemoryPurge(ctx context.Context, q execQuerier, p MemoryPurge) error {
	_, err := q.ExecContext(ctx, `INSERT INTO memory_purges (memory_id, from_version, through_version, bodies, cause, at, actor_user_id, via, reason)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, p.MemoryID, p.FromVersion, p.ThroughVersion, p.Bodies, p.Cause, p.At, nullID(p.ActorUserID), p.Via, truncate(p.Reason, 255))
	return err
}

// ReadMemoryRetentionDays は記憶の版の保持日数を読む。行が無い・整数として読めない・負の値は既定（無期限）にする。
func ReadMemoryRetentionDays(ctx context.Context, q execQuerier) (int64, error) {
	raw, ok, err := GetSetting(ctx, q, SettingMemoryRetentionDays)
	if err != nil || !ok {
		return DefaultMemoryRetentionDays, err
	}
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil && validRetentionDays(n) {
		return n, nil
	}
	return DefaultMemoryRetentionDays, nil
}

// SetMemoryRetentionDays は記憶の版の保持日数を変え、変更を setting_changes に記録する（同じ値なら何もしない）。
func SetMemoryRetentionDays(ctx context.Context, db *sql.DB, days int64, ch SettingChange) (old string, changed bool, err error) {
	if !validRetentionDays(days) {
		return "", false, i18n.Errorf("store.err.retention_days", "value", days, "max", MaxRetentionDays)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback() //nolint:errcheck
	old, changed, err = setSettingValueTx(ctx, tx, SettingMemoryRetentionDays, strconv.FormatInt(days, 10), ch)
	if err != nil || !changed {
		return old, false, err
	}
	if err := tx.Commit(); err != nil {
		return old, false, err
	}
	return old, true, nil
}
