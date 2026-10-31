package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// 占有（leases・lease_events。マイグレーション 0014。docs/server/DESIGN.md §9-9）。
// leases は資源ごとの今の持ち主の 1 行で、lease_events は取る・延ばす・返す・外すの記録（追記専用）。
// 権限・期限・持ち主の照合は service が持つ。ここは読み書きだけを置く。

// 占有の操作（表の CHECK と同じ値）。
const (
	LeaseActionAcquire = "acquire"
	LeaseActionExtend  = "extend"
	LeaseActionRelease = "release"
	LeaseActionBreak   = "break"
)

// Lease は資源 1 つの今の持ち主（leases の 1 行）。HolderUserID が 0 なら空いている。
type Lease struct {
	ID           int64
	ProjectID    int64
	Name         string
	HolderUserID int64
	HolderLogin  string // 一覧と取得で users から引いた login（ロックの読みでは空）
	MachineLabel string
	SessionID    string
	Purpose      string
	TokenHash    string // 世代の鍵の SHA-256（応答・記録に出さない）
	AcquiredAt   time.Time
	ExpiresAt    time.Time
	UpdatedAt    time.Time
}

// Held は誰かの持ち物か（期限が切れていても、次に取られるか返されるまでは持ち主が残る）。
func (l Lease) Held() bool { return l.HolderUserID != 0 }

// LeaseEvent は占有の記録 1 件（lease_events の 1 行）。
type LeaseEvent struct {
	LeaseID          int64
	Action           string
	UserID           int64
	UserLogin        string // 読むときに users から引いた login
	MachineLabel     string
	SessionID        string
	TokenID          int64
	Via              string
	Purpose          string
	Reason           string
	ExpiresAt        time.Time
	PrevUserID       int64
	PrevUserLogin    string // 読むときに users から引いた login
	PrevMachineLabel string
	PrevSessionID    string
	WasExpired       bool
	CreatedAt        time.Time
}

const leaseColumns = `l.id, l.project_id, l.name, COALESCE(l.holder_user_id, 0), COALESCE(u.login, ''), l.machine_label, l.session_id, l.purpose, l.token_hash,
  l.acquired_at, l.expires_at, l.updated_at`

const leaseFrom = ` FROM leases l LEFT JOIN users u ON u.id = l.holder_user_id`

func scanLease(row interface{ Scan(...any) error }) (Lease, error) {
	var l Lease
	var acquired, expires sql.NullTime
	err := row.Scan(&l.ID, &l.ProjectID, &l.Name, &l.HolderUserID, &l.HolderLogin, &l.MachineLabel, &l.SessionID, &l.Purpose, &l.TokenHash, &acquired, &expires, &l.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return l, ErrNotFound
	}
	l.AcquiredAt, l.ExpiresAt = acquired.Time, expires.Time
	return l, err
}

// LeaseByName はプロジェクトの資源の行を返す（空いている行も返す。無ければ ErrNotFound）。
// forUpdate なら行をロックする（トランザクションの中で、行があると分かってから使う。無い行への FOR UPDATE はギャップロックになる）。
// ロックの読みは users を結ばない（users の行まで握らないため。login は空のまま）。
func LeaseByName(ctx context.Context, q execQuerier, projectID int64, name string, forUpdate bool) (Lease, error) {
	if forUpdate {
		return scanLease(q.QueryRowContext(ctx, `SELECT l.id, l.project_id, l.name, COALESCE(l.holder_user_id, 0), '', l.machine_label, l.session_id, l.purpose, l.token_hash,
  l.acquired_at, l.expires_at, l.updated_at FROM leases l WHERE l.project_id = ? AND l.name = ? FOR UPDATE`, projectID, name))
	}
	return scanLease(q.QueryRowContext(ctx, "SELECT "+leaseColumns+leaseFrom+" WHERE l.project_id = ? AND l.name = ?", projectID, name))
}

// EnsureLeaseRow は資源の行が無ければ空の行を作る。トランザクションの外（その文だけで確定する接続）で呼ぶ。
// 行が無いまま FOR UPDATE すると一意性の索引のギャップロックを取り、同時に取る 2 つのトランザクションが互いの INSERT を待って
// デッドロックになる。作るのと握るのを分ける（EnsureMemoryRow と同じ）。
func EnsureLeaseRow(ctx context.Context, q execQuerier, projectID int64, name string, now time.Time) error {
	_, err := q.ExecContext(ctx, "INSERT IGNORE INTO leases (project_id, name, updated_at) VALUES (?, ?, ?)", projectID, name, now)
	return err
}

// UpdateLease は今の持ち主を書き換える。HolderUserID が 0 なら空き（時刻は NULL）にする。
// 呼ぶ側は同じトランザクションで LeaseByName(forUpdate) を取ってから呼ぶ。
func UpdateLease(ctx context.Context, q execQuerier, l Lease) error {
	var acquired, expires any
	if l.Held() {
		acquired, expires = l.AcquiredAt, l.ExpiresAt
	}
	_, err := q.ExecContext(ctx, "UPDATE leases SET holder_user_id = ?, machine_label = ?, session_id = ?, purpose = ?, token_hash = ?, acquired_at = ?, expires_at = ?, updated_at = ? WHERE id = ?",
		nullID(l.HolderUserID), truncate(l.MachineLabel, 64), truncate(l.SessionID, 128), l.Purpose, l.TokenHash, acquired, expires, l.UpdatedAt, l.ID)
	return err
}

// InsertLeaseEvent は占有の記録を 1 行足す（追記専用）。
func InsertLeaseEvent(ctx context.Context, q execQuerier, e LeaseEvent) error {
	var expires any
	if !e.ExpiresAt.IsZero() {
		expires = e.ExpiresAt
	}
	_, err := q.ExecContext(ctx, `INSERT INTO lease_events (lease_id, action, user_id, machine_label, session_id, token_id, via, purpose, reason, expires_at,
  prev_user_id, prev_machine_label, prev_session_id, was_expired, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.LeaseID, e.Action, nullID(e.UserID), truncate(e.MachineLabel, 64), truncate(e.SessionID, 128), nullID(e.TokenID), e.Via, e.Purpose, e.Reason, expires,
		nullID(e.PrevUserID), truncate(e.PrevMachineLabel, 64), truncate(e.PrevSessionID, 128), e.WasExpired, e.CreatedAt)
	return err
}

// HeldLeases はプロジェクトの、持ち主のいる資源を名前の順に返す（期限が切れて取り直されていないものも含む）。
func HeldLeases(ctx context.Context, q execQuerier, projectID int64) ([]Lease, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+leaseColumns+leaseFrom+" WHERE l.project_id = ? AND l.holder_user_id IS NOT NULL ORDER BY l.name", projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Lease
	for rows.Next() {
		l, err := scanLease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// LeaseEvents は資源の記録を新しい順に limit 件返す。
func LeaseEvents(ctx context.Context, q execQuerier, leaseID int64, limit int) ([]LeaseEvent, error) {
	rows, err := q.QueryContext(ctx, `SELECT e.lease_id, e.action, COALESCE(e.user_id, 0), COALESCE(u.login, ''), e.machine_label, e.session_id, COALESCE(e.token_id, 0), e.via,
  e.purpose, e.reason, e.expires_at, COALESCE(e.prev_user_id, 0), COALESCE(p.login, ''), e.prev_machine_label, e.prev_session_id, e.was_expired, e.created_at
FROM lease_events e LEFT JOIN users u ON u.id = e.user_id LEFT JOIN users p ON p.id = e.prev_user_id
WHERE e.lease_id = ? ORDER BY e.id DESC LIMIT ?`, leaseID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LeaseEvent
	for rows.Next() {
		var e LeaseEvent
		var expires sql.NullTime
		if err := rows.Scan(&e.LeaseID, &e.Action, &e.UserID, &e.UserLogin, &e.MachineLabel, &e.SessionID, &e.TokenID, &e.Via, &e.Purpose, &e.Reason, &expires,
			&e.PrevUserID, &e.PrevUserLogin, &e.PrevMachineLabel, &e.PrevSessionID, &e.WasExpired, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.ExpiresAt = expires.Time
		out = append(out, e)
	}
	return out, rows.Err()
}
